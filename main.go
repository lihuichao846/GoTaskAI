package main

import (
	"gotaskai/internal/api"
	"gotaskai/internal/db"
	"gotaskai/internal/middleware"
	"gotaskai/internal/model"
	"gotaskai/internal/queue"
	"gotaskai/internal/worker"
	"log"
	"time"

	"github.com/gin-gonic/gin"
)

// main 是 GoTaskAI 平台的启动入口函数
func main() {
	// 0. 初始化数据库和缓存
	// 在生产环境中这些配置应当通过环境变量或配置文件注入
	// 注意：因为本地 3306 端口被占用，我们已将 Docker 的 MySQL 映射到 3307 端口
	dsn := "root:root@tcp(127.0.0.1:3307)/gotaskai?charset=utf8mb4&parseTime=True&loc=Local"
	mysqlDB := db.InitMySQL(dsn)

	// 自动迁移数据库表结构 (创建或更新 Task 表和 User 表)
	err := mysqlDB.AutoMigrate(&model.Task{}, &model.User{})
	if err != nil {
		log.Fatalf("Failed to auto migrate database: %v", err)
	}

	redisClient := db.InitRedis("127.0.0.1:6379", "", 0)

	// 1. 初始化核心任务管理器 (Task Manager)
	// 注入 MySQL 和 Redis 实例，设置内存队列 (Channel) 的缓冲容量为 1000
	manager := queue.NewTaskManager(1000, mysqlDB, redisClient)

	// 2. 初始化并发工作池 (Worker Pool)
	// 设置并发数为 5，意味着服务端可以同时处理 5 个 AI 任务
	pool := worker.NewPool(5, manager)
	// 启动后台 Goroutines 进行队列消费
	pool.Start()

	// 3. 初始化 HTTP 路由与控制器 (API Handlers)
	handler := api.NewHandler(manager)
	authHandler := api.NewAuthHandler(mysqlDB)

	// --- 引入 Gin 框架进行路由管理 ---

	// 初始化一个带有默认日志(Logger)和错误恢复(Recovery)中间件的 Gin 引擎
	r := gin.Default()

	// 4. 配置静态文件托管
	// 直接将 "/" 映射到我们前端的入口文件
	r.StaticFile("/", "./public/index.html")

	// 5. 使用 Gin 的“路由分组 (Group)”功能
	// 这样可以更好地组织代码，所有 /api 开头的请求都在这个分组里
	apiGroup := r.Group("/api")
	{
		// 认证相关的路由 (不需要登录)
		authGroup := apiGroup.Group("/auth")
		{
			authGroup.POST("/register", authHandler.Register)
			authGroup.POST("/login", authHandler.Login)
		}

		// 在 /api 下继续细分出 /tasks 组
		tasksGroup := apiGroup.Group("/tasks")
		// 应用 Auth 中间件，保护 tasks 接口
		tasksGroup.Use(middleware.AuthMiddleware())
		{
			// Gin 会自动根据 HTTP Method 进行精准匹配，省去了原先在 handler 里手动写 if r.Method 校验的麻烦
			// 为提交接口单独增加“限流中间件”：每个用户（IP）每分钟最多只能提交 3 个任务，防止恶意刷单
			tasksGroup.POST("/submit", middleware.RateLimitMiddleware(redisClient, 3, time.Minute), handler.SubmitTask)             // 处理提交任务
			tasksGroup.POST("/batch-submit", middleware.RateLimitMiddleware(redisClient, 1, time.Minute), handler.BatchSubmitTasks) // 处理批量提交任务，限流更严格
			tasksGroup.GET("/:id", handler.GetTaskStatus)                                                                           // 处理查询单任务状态
			tasksGroup.GET("", handler.ListTasks)                                                                                   // 处理获取所有任务列表
			tasksGroup.DELETE("/:id", handler.DeleteTask)                                                                           // 处理删除单任务
			tasksGroup.POST("/:id/retry", handler.RetryTask)                                                                        // 处理失败任务重试
			tasksGroup.POST("/:id/cancel", handler.CancelTask)                                                                      // 处理任务取消
		}
		// SSE 接口由于不能自定义 Header，通常通过 URL 传 token，我们把它挂在鉴权中间件外面或者单独处理
		apiGroup.GET("/tasks/stream", handler.StreamTasks)
	}

	// 6. 启动 HTTP 监听服务
	log.Println("GoTaskAI server listening on :8080")
	if err := r.Run(":8080"); err != nil {
		log.Fatalf("Server failed: %v", err)
	}
}
