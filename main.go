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
	"gotaskai/internal/pkg/mcpclient"
	"gotaskai/internal/queue"
	"gotaskai/internal/worker"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/hibiken/asynq"
	"github.com/hibiken/asynqmon"
)

// main 是 GoTaskAI 平台的启动入口函数
func main() {
	// 0. 初始化配置 (企业级实践：使用配置文件 + 环境变量)
	config.InitConfig("config/config.yaml")
	cfg := config.AppConfig

	// 初始化企业级结构化日志 (使用 Go 1.21+ 标准库 log/slog)
	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))
	slog.SetDefault(logger)
	slog.Info("Starting GoTaskAI Application...")

	// 1. 初始化数据库和缓存
	mysqlDB := db.InitMySQL(cfg.MySQL.DSN)
	sqlDB, err := mysqlDB.DB()
	if err == nil {
		sqlDB.SetMaxIdleConns(cfg.MySQL.MaxIdleConns)
		sqlDB.SetMaxOpenConns(cfg.MySQL.MaxOpenConns)
	}

	// 自动迁移数据库表结构 (创建或更新 Task 表和 User 表)
	if err := mysqlDB.AutoMigrate(&model.Task{}, &model.User{}); err != nil {
		slog.Error("Failed to auto migrate database", "error", err)
		os.Exit(1)
	}

	redisClient := db.InitRedis(cfg.Redis.Addr, cfg.Redis.Password, cfg.Redis.DB)

	// 2. 初始化核心任务管理器 (Task Manager)
	redisOpt := asynq.RedisClientOpt{Addr: cfg.Redis.Addr, Password: cfg.Redis.Password, DB: cfg.Redis.DB}
	manager := queue.NewTaskManager(mysqlDB, redisClient, redisOpt)
	defer manager.Close()

	// 初始化 Neo4j 客户端及 KAG 管理器
	var kagManager *kag.KAGManager
	if cfg.Neo4j.URI != "" {
		neo4jDriver := db.InitNeo4j(cfg.Neo4j.URI, cfg.Neo4j.Username, cfg.Neo4j.Password)
		defer neo4jDriver.Close(context.Background())
		kagManager = kag.NewKAGManager(neo4jDriver, llm.NewClient())
		slog.Info("Connected to Neo4j successfully, KAG enabled")
	}

	// 初始化 MCP 客户端
	mcpCtx := context.Background()
	mcpServerPath, _ := filepath.Abs("bin/search_mcp.exe")
	mcpClient, err := mcpclient.NewWrapper(mcpCtx, mcpServerPath)
	if err != nil {
		slog.Warn("Failed to connect to MCP Server, tools will be disabled", "error", err)
	} else {
		slog.Info("Connected to MCP Server successfully")
		defer mcpClient.Close()
	}

	// 3. 初始化并发工作池 (Worker Pool)
	pool := worker.NewPool(cfg.Queue.Workers, manager, redisOpt, mcpClient, kagManager)
	pool.Start()
	slog.Info("Worker pool started", "workers", cfg.Queue.Workers)

	// 4. 初始化 HTTP 路由与控制器 (API Handlers)
	handler := api.NewHandler(manager, mcpClient)
	authHandler := api.NewAuthHandler(mysqlDB, redisClient)
	ragHandler := api.NewRAGHandler(redisClient, kagManager)

	// --- 引入 Gin 框架进行路由管理 ---
	if cfg.Server.Mode == "release" {
		gin.SetMode(gin.ReleaseMode)
	}
	r := gin.Default()

	// 配置静态文件托管
	r.StaticFile("/", "./public/index.html")

	// 挂载 Asynq 官方的 Web UI 面板
	mon := asynqmon.New(asynqmon.Options{
		RootPath:     "/admin/tasks",
		RedisConnOpt: redisOpt,
	})
	r.Any("/admin/tasks/*any", gin.WrapH(mon))

	// 使用 Gin 的“路由分组 (Group)”功能
	apiGroup := r.Group("/api")
	{
		adminGroup := apiGroup.Group("/admin")
		{
			adminGroup.POST("/rag/ingest", ragHandler.Ingest)
			adminGroup.POST("/kag/ingest", ragHandler.KAGIngest) // 新增 KAG 图谱录入接口
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
			tasksGroup.POST("/optimize-prompt", middleware.RateLimitMiddleware(redisClient, 3, time.Minute), handler.OptimizePrompt) // 新增：Prompt 优化接口并加入限流
			tasksGroup.GET("/:id", handler.GetTaskStatus)
			tasksGroup.GET("", handler.ListTasks)
			tasksGroup.DELETE("/:id", handler.DeleteTask)
			tasksGroup.DELETE("/session/:id", handler.DeleteSession)
			tasksGroup.POST("/:id/retry", handler.RetryTask)
			tasksGroup.POST("/:id/cancel", handler.CancelTask)
		}
		apiGroup.GET("/tasks/stream", handler.StreamTasks)
	}

	// 5. 启动 HTTP 监听服务并配置优雅重启 (Graceful Shutdown)
	srv := &http.Server{
		Addr:    fmt.Sprintf(":%d", cfg.Server.Port),
		Handler: r,
	}

	// 开启独立 goroutine 启动服务
	go func() {
		slog.Info("GoTaskAI server listening", "port", cfg.Server.Port)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			slog.Error("Server failed", "error", err)
			os.Exit(1)
		}
	}()

	// 6. 优雅退出处理：监听系统级退出信号
	quit := make(chan os.Signal, 1)
	// kill (no param) default send syscall.SIGTERM
	// kill -2 is syscall.SIGINT (Ctrl+C)
	// kill -9 is syscall.SIGKILL but can't be catch, so don't need add it
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	<-quit
	slog.Info("Shutdown Signal Received, terminating gracefully...")

	// 设置一个 5 秒的超时上下文，给服务 5 秒时间处理正在进行的请求
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if err := srv.Shutdown(ctx); err != nil {
		slog.Error("Server Shutdown Error", "error", err)
	}

	// 可以在这里做其他清理工作（如关闭数据库连接、停止 worker pool 等）
	pool.Stop() // 停止消费队列任务
	slog.Info("Server Exiting")
}
