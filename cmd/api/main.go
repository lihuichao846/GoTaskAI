package main

import (
	"context"
	"errors"
	"fmt"
	"gotaskai/internal/api"
	"gotaskai/internal/config"
	"gotaskai/internal/db"
	"gotaskai/internal/middleware"
	"gotaskai/internal/model"
	"gotaskai/internal/pkg/kag"
	"gotaskai/internal/pkg/llm"
	"gotaskai/internal/queue"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/hibiken/asynqmon"
)

// API Server 启动入口
func main() {
	// 0. 初始化配置
	config.InitConfig("config/config.yaml")
	cfg := config.AppConfig

	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))
	slog.SetDefault(logger)
	slog.Info("Starting GoTaskAI API Server (Microservice Mode)...")

	// 1. 初始化数据库和缓存
	mysqlDB := db.InitMySQL(cfg.MySQL.DSN)
	sqlDB, err := mysqlDB.DB()
	if err == nil {
		sqlDB.SetMaxIdleConns(cfg.MySQL.MaxIdleConns)
		sqlDB.SetMaxOpenConns(cfg.MySQL.MaxOpenConns)
	}

	if err := mysqlDB.AutoMigrate(&model.Task{}, &model.User{}, &model.Agent{}); err != nil {
		slog.Error("Failed to auto migrate database", "error", err)
		os.Exit(1)
	}

	redisClient := db.InitRedis(cfg.Redis)

	// 2. 初始化核心任务管理器 (Task Manager - 作为生产者)
	redisOpt := db.NewAsynqRedisConnOpt(cfg.Redis)
	manager := queue.NewTaskManager(mysqlDB, redisClient, redisOpt)
	defer manager.Close()

	// 初始化 Neo4j 客户端及 KAG 管理器 (用于接收 KAG 知识入库)
	var kagManager *kag.KAGManager
	if cfg.Neo4j.URI != "" {
		neo4jDriver := db.InitNeo4j(cfg.Neo4j.URI, cfg.Neo4j.Username, cfg.Neo4j.Password)
		defer neo4jDriver.Close(context.Background())
		kagManager = kag.NewKAGManager(neo4jDriver, llm.NewClient())
		slog.Info("Connected to Neo4j successfully, KAG ingestion enabled")
	}

	// 3. 初始化 HTTP 路由与控制器 (API Handlers)
	handler := api.NewHandler(manager)
	authHandler := api.NewAuthHandler(mysqlDB, redisClient)
	ragHandler := api.NewRAGHandler(redisClient, kagManager)
	agentHandler := api.NewAgentHandler(manager)

	if cfg.Server.Mode == "release" {
		gin.SetMode(gin.ReleaseMode)
	}
	r := gin.Default()
	r.Use(middleware.CORSMiddleware())

	// 配置静态文件托管
	r.StaticFile("/", config.ResolveProjectPath("public", "index.html"))

	// 挂载 Asynq 官方的 Web UI 面板
	mon := asynqmon.New(asynqmon.Options{
		RootPath:     "/admin/tasks",
		RedisConnOpt: redisOpt,
	})
	r.Any("/admin/tasks", gin.WrapH(mon))
	r.Any("/admin/tasks/*any", gin.WrapH(mon))

	apiGroup := r.Group("/api")
	{
		adminGroup := apiGroup.Group("/admin")
		{
			adminGroup.POST("/rag/ingest", ragHandler.Ingest)
			adminGroup.POST("/kag/ingest", ragHandler.KAGIngest)
		}

		authGroup := apiGroup.Group("/auth")
		{
			authGroup.POST("/register", authHandler.Register)
			authGroup.POST("/login", authHandler.Login)
			authGroup.POST("/forgot-password/request-code", authHandler.RequestResetCode)
			authGroup.POST("/forgot-password/reset", authHandler.ResetPassword)
		}

		tasksGroup := apiGroup.Group("/tasks")
		tasksGroup.Use(middleware.AuthMiddleware())
		{
			tasksGroup.POST("/submit", middleware.RateLimitMiddleware(redisClient, 3, time.Minute), handler.SubmitTask)
			tasksGroup.POST("/batch-submit", middleware.RateLimitMiddleware(redisClient, 1, time.Minute), handler.BatchSubmitTasks)
			tasksGroup.POST("/optimize-prompt", middleware.RateLimitMiddleware(redisClient, 3, time.Minute), handler.OptimizePrompt)
			tasksGroup.GET("/:id", handler.GetTaskStatus)
			tasksGroup.GET("", handler.ListTasks)
			tasksGroup.DELETE("/:id", handler.DeleteTask)
			tasksGroup.DELETE("/session/:id", handler.DeleteSession)
			tasksGroup.POST("/:id/retry", handler.RetryTask)
			tasksGroup.POST("/:id/cancel", handler.CancelTask)
		}
		agentsGroup := apiGroup.Group("/agents")
		agentsGroup.Use(middleware.AuthMiddleware())
		{
			agentsGroup.POST("", agentHandler.CreateAgent)
			agentsGroup.GET("", agentHandler.ListAgents)
			agentsGroup.GET("/:id", agentHandler.GetAgent)
			agentsGroup.PUT("/:id", agentHandler.UpdateAgent)
			agentsGroup.DELETE("/:id", agentHandler.DeleteAgent)
			agentsGroup.POST("/:id/run", agentHandler.RunAgent)
		}

		apiGroup.GET("/tasks/stream", handler.StreamTasks)
	}

	srv := &http.Server{
		Addr:    fmt.Sprintf(":%d", cfg.Server.Port),
		Handler: r,
	}

	go func() {
		slog.Info("GoTaskAI API Server listening", "port", cfg.Server.Port)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			slog.Error("Server failed", "error", err)
			os.Exit(1)
		}
	}()

	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	<-quit
	slog.Info("Shutdown Signal Received, terminating API Server gracefully...")

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if err := srv.Shutdown(ctx); err != nil {
		slog.Error("Server Shutdown Error", "error", err)
	}

	slog.Info("API Server Exiting")
}
