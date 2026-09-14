package main

import (
	"context"
	"errors"
	"fmt"
	"gotaskai/internal/config"
	"gotaskai/internal/db"
	"gotaskai/internal/events"
	"gotaskai/internal/pkg/kag"
	"gotaskai/internal/pkg/llm"
	"gotaskai/internal/pkg/mcpclient"
	"gotaskai/internal/queue"
	"gotaskai/internal/worker"
	"log/slog"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"strconv"
	"sync"
	"syscall"
	"time"

	"github.com/hibiken/asynq"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

const workerChildIndexEnv = "GOTASKAI_WORKER_CHILD_INDEX"

// Worker Node 启动入口
func main() {
	// 0. 初始化配置
	config.InitConfig("config/config.yaml")
	cfg := config.AppConfig

	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))
	slog.SetDefault(logger)
	slog.Info("Starting GoTaskAI Worker Node (Microservice Mode)...")

	if os.Getenv(workerChildIndexEnv) == "" {
		runSupervisor(cfg)
		return
	}

	runWorkerProcess(cfg)
}

func runSupervisor(cfg config.Config) {
	processes := cfg.Queue.Processes
	if processes <= 0 {
		processes = 1
	}

	slog.Info("Launching worker processes", "processes", processes, "workers_per_process", cfg.Queue.Workers)

	// W2：kb:scan 周期任务只在 worker 主实例（supervisor）注册，避免每个子进程各注册一份导致重复投递（R4）。
	if scheduler := startKBScanScheduler(cfg); scheduler != nil {
		defer scheduler.Shutdown()
	}

	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	defer signal.Stop(quit)

	var wg sync.WaitGroup
	children := make([]*exec.Cmd, 0, processes)

	for i := 0; i < processes; i++ {
		cmd := exec.Command(os.Args[0])
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
		cmd.Stdin = os.Stdin
		cmd.Env = append(os.Environ(), fmt.Sprintf("%s=%d", workerChildIndexEnv, i))
		children = append(children, cmd)

		wg.Add(1)
		go func(index int, child *exec.Cmd) {
			defer wg.Done()
			if err := child.Run(); err != nil {
				slog.Error("Worker child process exited with error", "child_index", index, "error", err)
				return
			}
			slog.Info("Worker child process exited", "child_index", index)
		}(i, cmd)
	}

	<-quit
	slog.Info("Shutdown signal received, stopping worker child processes...")

	for index, child := range children {
		if child.Process == nil {
			continue
		}
		if err := child.Process.Signal(syscall.SIGTERM); err != nil {
			slog.Warn("Failed to signal worker child process", "child_index", index, "error", err)
		}
	}

	wg.Wait()
	slog.Info("Worker supervisor exiting")
}

// startKBScanScheduler 在 worker 主实例注册 kb:scan 周期任务（W2 定时增量入库）。
// 单实例投递：只在 supervisor 注册一次，子进程不注册，避免重复调度（R4）。
// 返回 nil 表示未启用或 cron 非法（不致命，仅告警）。
// 说明：扫描源不可读时仍会启动调度器——因为删除补偿（W3 6.4）复用本任务，
// 此时只做补偿、不扫源，避免删除任务重试耗尽后无人接管。
func startKBScanScheduler(cfg config.Config) *asynq.Scheduler {
	if !cfg.KB.ScanEnabled {
		return nil
	}
	if cfg.KB.ScanCron == "" {
		slog.Warn("kb:scan scheduler disabled: scan_cron is empty")
		return nil
	}

	if len(cfg.KB.ScanSources) == 0 {
		slog.Warn("kb:scan: no scan_sources configured, only delete compensation will run")
	} else {
		// 启动时校验源目录可读性：容器/多节点部署下本地路径可能不可见（R8）。
		readable := 0
		for _, src := range cfg.KB.ScanSources {
			if src.KBID == "" || src.Path == "" {
				slog.Warn("kb:scan source ignored: kb_id and path are required")
				continue
			}
			info, err := os.Stat(src.Path)
			if err != nil || !info.IsDir() {
				slog.Warn("kb:scan source unreadable, will be skipped at runtime", "path", src.Path, "error", err)
				continue
			}
			readable++
		}
		if readable == 0 {
			slog.Warn("kb:scan: no readable scan source, only delete compensation will run")
		}
	}

	scheduler := asynq.NewScheduler(db.NewAsynqRedisConnOpt(cfg.Redis), &asynq.SchedulerOpts{Location: time.Local})
	task := asynq.NewTask("kb:scan", []byte("{}"), asynq.Queue("low"))
	if _, err := scheduler.Register(cfg.KB.ScanCron, task); err != nil {
		slog.Error("kb:scan scheduler registration failed", "cron", cfg.KB.ScanCron, "error", err)
		return nil
	}
	if err := scheduler.Start(); err != nil {
		slog.Error("kb:scan scheduler start failed", "error", err)
		return nil
	}
	slog.Info("kb:scan scheduler started", "cron", cfg.KB.ScanCron, "sources", len(cfg.KB.ScanSources))
	return scheduler
}

func runWorkerProcess(cfg config.Config) {
	childIndex, err := strconv.Atoi(os.Getenv(workerChildIndexEnv))
	if err != nil {
		childIndex = 0
	}

	slog.Info("Starting worker child process", "child_index", childIndex)

	// 1. 初始化数据库和缓存 (Worker 也需要写数据库更新任务状态)
	mysqlDB := db.InitMySQL(cfg.MySQL.DSN)
	redisClient := db.InitRedis(cfg.Redis)

	// 2. 初始化实时事件通道（NATS 客户端）：Worker 作为事件发布方连接到 API 内嵌的 NATS Server。
	var eventBus *events.EventBus
	if cfg.EventBus.Enabled && cfg.EventBus.URL != "" {
		bus, err := events.New(cfg.EventBus.URL)
		if err != nil {
			slog.Warn("Failed to connect to event bus (NATS), real-time events disabled", "error", err)
		} else {
			eventBus = bus
			defer eventBus.Close()
			slog.Info("Connected to event bus (NATS)", "url", cfg.EventBus.URL)
		}
	} else {
		slog.Warn("Event bus (NATS) disabled, cross-process real-time events will not be delivered")
	}

	// 3. 初始化核心任务管理器 (Task Manager - 作为消费者状态更新器)
	redisOpt := db.NewAsynqRedisConnOpt(cfg.Redis)
	manager := queue.NewTaskManager(mysqlDB, redisClient, redisOpt, eventBus)
	defer manager.Close()

	// 初始化 Neo4j 客户端及 KAG 管理器 (用于 Worker 图谱检索)
	var kagManager *kag.KAGManager
	if cfg.Neo4j.URI != "" {
		neo4jDriver := db.InitNeo4j(cfg.Neo4j.URI, cfg.Neo4j.Username, cfg.Neo4j.Password)
		defer neo4jDriver.Close(context.Background())
		kagManager = kag.NewKAGManager(neo4jDriver, llm.NewClient())
		slog.Info("Connected to Neo4j successfully, KAG retrieval enabled")
		// 为关系建立 source_doc 属性索引，使按来源文档的失效清理不必全图扫描（幂等，失败不致命）。
		if err := kagManager.EnsureSourceDocIndex(context.Background()); err != nil {
			slog.Warn("Failed to ensure graph source_doc index", "error", err)
		}
	}

	// 初始化 MCP 客户端 (赋予 Worker 大模型工具调用能力)
	mcpCtx := context.Background()
	mcpServerPath := config.ResolveProjectPath("bin", "search_mcp.exe")
	mcpClient, err := mcpclient.NewWrapper(mcpCtx, mcpServerPath)
	if err != nil {
		slog.Warn("Failed to connect to MCP Server, tools will be disabled", "error", err)
	} else {
		slog.Info("Connected to MCP Server successfully")
		defer mcpClient.Close()
	}

	// 3. 初始化并启动并发工作池 (Worker Pool)
	pool := worker.NewPool(cfg.Queue.Workers, manager, redisOpt, mcpClient, kagManager)
	pool.Start()
	slog.Info("Worker pool started", "child_index", childIndex, "workers", cfg.Queue.Workers)

	// 启动 Prometheus /metrics 端点（子进程用独立端口，避免多子进程冲突）。
	startMetricsServer(cfg, childIndex)

	// 4. 优雅退出处理：监听系统级退出信号
	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	defer signal.Stop(quit)
	<-quit
	slog.Info("Shutdown signal received, terminating worker child gracefully...", "child_index", childIndex)

	pool.Stop() // 停止消费队列任务，等待当前任务完成
	slog.Info("Worker child exiting", "child_index", childIndex)
}

// startMetricsServer 启动 Prometheus /metrics HTTP 端点。
// Worker 子进程使用独立端口（base + childIndex + 1），避免 supervisor 多子进程端口冲突。
func startMetricsServer(cfg config.Config, childIndex int) {
	if cfg.Metrics.Port <= 0 {
		return
	}
	port := cfg.Metrics.Port + childIndex + 1
	mux := http.NewServeMux()
	mux.Handle("/metrics", promhttp.Handler())
	srv := &http.Server{Addr: fmt.Sprintf(":%d", port), Handler: mux}
	go func() {
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			slog.Error("metrics server failed", "port", port, "error", err)
		}
	}()
	slog.Info("metrics server started", "port", port)
}
