package main

import (
	"context"
	"errors"
	"fmt"
	"gotaskai/internal/api"
	"gotaskai/internal/config"
	"gotaskai/internal/db"
	"gotaskai/internal/events"
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
	"github.com/prometheus/client_golang/prometheus/promhttp"
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

	if err := mysqlDB.AutoMigrate(&model.Task{}, &model.User{}, &model.Agent{}, &model.Tool{}, &model.KnowledgeBase{}, &model.AgentTool{}, &model.AgentKnowledge{}, &model.Document{}, &model.Conversation{}, &model.RunLog{}, &model.ToolCallLog{}, &model.UserMemory{}); err != nil {
		slog.Error("Failed to auto migrate database", "error", err)
		os.Exit(1)
	}

	redisClient := db.InitRedis(cfg.Redis)

	// 2. 初始化实时事件通道（NATS）：API 进程内嵌启动 nats-server，并建立客户端订阅跨进程事件。
	var eventBus *events.EventBus
	if cfg.EventBus.Enabled && cfg.EventBus.URL != "" {
		if cfg.EventBus.Embedded {
			if ns, err := events.StartEmbeddedServer(cfg.EventBus.URL); err != nil {
				slog.Error("Failed to start embedded nats-server, fall back to in-process broadcasting", "error", err)
			} else {
				defer ns.Shutdown()
				slog.Info("Embedded nats-server started", "url", cfg.EventBus.URL)
			}
		}
		bus, err := events.New(cfg.EventBus.URL)
		if err != nil {
			slog.Warn("Failed to connect to event bus (NATS), fall back to in-process broadcasting", "error", err)
		} else {
			eventBus = bus
			defer eventBus.Close()
			slog.Info("Connected to event bus (NATS)", "url", cfg.EventBus.URL)
		}
	} else {
		slog.Warn("Event bus (NATS) disabled, fall back to in-process broadcasting")
	}

	// 3. 初始化核心任务管理器 (Task Manager - 作为生产者)
	redisOpt := db.NewAsynqRedisConnOpt(cfg.Redis)
	manager := queue.NewTaskManager(mysqlDB, redisClient, redisOpt, eventBus)
	defer manager.Close()

	// 种子内置工具，保证工具列表与 Agent 绑定关系可用。
	seedBuiltinTools(manager)

	// 兼容回填：把历史任务的旧 SessionID 迁移为 Conversation 记录（幂等）。
	if err := manager.BackfillConversations(); err != nil {
		slog.Error("Failed to backfill conversations", "error", err)
	}

	// 初始化 Neo4j 客户端及 KAG 管理器 (用于接收 KAG 知识入库)
	var kagManager *kag.KAGManager
	if cfg.Neo4j.URI != "" {
		neo4jDriver := db.InitNeo4j(cfg.Neo4j.URI, cfg.Neo4j.Username, cfg.Neo4j.Password)
		defer neo4jDriver.Close(context.Background())
		kagManager = kag.NewKAGManager(neo4jDriver, llm.NewClient())
		slog.Info("Connected to Neo4j successfully, KAG ingestion enabled")
		// 为关系建立 source_doc 属性索引，使按来源文档的失效清理不必全图扫描（幂等，失败不致命）。
		if err := kagManager.EnsureSourceDocIndex(context.Background()); err != nil {
			slog.Warn("Failed to ensure graph source_doc index", "error", err)
		}
	}

	// 3. 初始化 HTTP 路由与控制器 (API Handlers)
	handler := api.NewHandler(manager)
	authHandler := api.NewAuthHandler(mysqlDB, redisClient)
	ragHandler := api.NewRAGHandler(redisClient, kagManager)
	agentHandler := api.NewAgentHandler(manager)
	toolHandler := api.NewToolHandler(manager, llm.NewClient())
	knowledgeBaseHandler := api.NewKnowledgeBaseHandler(manager)
	conversationHandler := api.NewConversationHandler(manager)
	memoryHandler := api.NewMemoryHandler(manager)

	if cfg.Server.Mode == "release" {
		gin.SetMode(gin.ReleaseMode)
	}
	r := gin.Default()
	r.Use(middleware.CORSMiddleware())

	// 暴露 Prometheus /metrics 端点。
	r.GET("/metrics", gin.WrapH(promhttp.Handler()))

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

			agentsGroup.POST("/:id/tools", agentHandler.BindTools)
			agentsGroup.GET("/:id/tools", agentHandler.ListAgentTools)
			agentsGroup.DELETE("/:id/tools/:toolId", agentHandler.UnbindTool)
			agentsGroup.POST("/:id/knowledge-bases", agentHandler.BindKnowledgeBases)
			agentsGroup.GET("/:id/knowledge-bases", agentHandler.ListAgentKnowledgeBases)
			agentsGroup.DELETE("/:id/knowledge-bases/:kbId", agentHandler.UnbindKnowledge)
		}

		toolsGroup := apiGroup.Group("/tools")
		toolsGroup.Use(middleware.AuthMiddleware())
		{
			toolsGroup.POST("", toolHandler.CreateTool)
			toolsGroup.POST("/search", toolHandler.SearchMCPServers)
			toolsGroup.POST("/test", toolHandler.TestToolConnection)
			toolsGroup.GET("", toolHandler.ListTools)
			toolsGroup.PUT("/:id", toolHandler.UpdateTool)
			toolsGroup.PATCH("/:id/status", toolHandler.UpdateToolStatus)
			toolsGroup.GET("/:id/agents", toolHandler.ListToolAgents)
			toolsGroup.DELETE("/:id", toolHandler.DeleteTool)
		}

		conversationsGroup := apiGroup.Group("/conversations")
		conversationsGroup.Use(middleware.AuthMiddleware())
		{
			conversationsGroup.POST("", conversationHandler.CreateConversation)
			conversationsGroup.GET("", conversationHandler.ListConversations)
			conversationsGroup.GET("/:id", conversationHandler.GetConversation)
			conversationsGroup.DELETE("/:id", conversationHandler.DeleteConversation)
		}

		knowledgeGroup := apiGroup.Group("/knowledge-bases")
		knowledgeGroup.Use(middleware.AuthMiddleware())
		{
			knowledgeGroup.POST("", knowledgeBaseHandler.CreateKnowledgeBase)
			knowledgeGroup.GET("", knowledgeBaseHandler.ListKnowledgeBases)
			knowledgeGroup.GET("/:id", knowledgeBaseHandler.GetKnowledgeBase)
			knowledgeGroup.DELETE("/:id", knowledgeBaseHandler.DeleteKnowledgeBase)
			knowledgeGroup.POST("/:id/documents", knowledgeBaseHandler.AddDocument)
			knowledgeGroup.DELETE("/:id/documents/:docId", knowledgeBaseHandler.DeleteDocument)
		}

		// 用户向长期记忆：写入受 memory.write_enabled 约束，删除始终允许（用户删除权）。
		memoriesGroup := apiGroup.Group("/memories")
		memoriesGroup.Use(middleware.AuthMiddleware())
		{
			memoriesGroup.POST("", memoryHandler.CreateMemory)
			memoriesGroup.GET("", memoryHandler.ListMemories)
			memoriesGroup.PUT("/:id", memoryHandler.UpdateMemory)
			memoriesGroup.DELETE("/:id", memoryHandler.DeleteMemory)
			memoriesGroup.DELETE("", memoryHandler.DeleteAllMemories)
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

// seedBuiltinTools 初始化内置工具（幂等），供工具列表与 Agent 绑定使用。
func seedBuiltinTools(manager *queue.TaskManager) {
	now := time.Now()
	builtins := []model.Tool{
		{
			ID:          "builtin_current_time",
			UserID:      0,
			Name:        "get_current_time",
			Type:        "builtin",
			DisplayName: "当前时间",
			Description: "获取当前的日期和时间",
			IsBuiltin:   true,
			Status:      "active",
			CreatedAt:   now,
			UpdatedAt:   now,
		},
		{
			ID:          "builtin_internet_search",
			UserID:      0,
			Name:        "internet_search",
			Type:        "mcp",
			DisplayName: "联网搜索",
			Description: "通过搜索引擎查询互联网上的实时信息",
			IsBuiltin:   true,
			Status:      "active",
			CreatedAt:   now,
			UpdatedAt:   now,
		},
	}

	for _, t := range builtins {
		if _, ok := manager.GetTool(t.ID); !ok {
			_ = manager.CreateTool(&t)
		}
	}
}
