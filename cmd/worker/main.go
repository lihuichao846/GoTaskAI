package main

import (
	"context"
	"fmt"
	"gotaskai/internal/config"
	"gotaskai/internal/db"
	"gotaskai/internal/pkg/kag"
	"gotaskai/internal/pkg/llm"
	"gotaskai/internal/pkg/mcpclient"
	"gotaskai/internal/queue"
	"gotaskai/internal/worker"
	"log/slog"
	"os"
	"os/exec"
	"os/signal"
	"strconv"
	"sync"
	"syscall"
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

func runWorkerProcess(cfg config.Config) {
	childIndex, err := strconv.Atoi(os.Getenv(workerChildIndexEnv))
	if err != nil {
		childIndex = 0
	}

	slog.Info("Starting worker child process", "child_index", childIndex)

	// 1. 初始化数据库和缓存 (Worker 也需要写数据库更新任务状态)
	mysqlDB := db.InitMySQL(cfg.MySQL.DSN)
	redisClient := db.InitRedis(cfg.Redis)

	// 2. 初始化核心任务管理器 (Task Manager - 作为消费者状态更新器)
	redisOpt := db.NewAsynqRedisConnOpt(cfg.Redis)
	manager := queue.NewTaskManager(mysqlDB, redisClient, redisOpt)
	defer manager.Close()

	// 初始化 Neo4j 客户端及 KAG 管理器 (用于 Worker 图谱检索)
	var kagManager *kag.KAGManager
	if cfg.Neo4j.URI != "" {
		neo4jDriver := db.InitNeo4j(cfg.Neo4j.URI, cfg.Neo4j.Username, cfg.Neo4j.Password)
		defer neo4jDriver.Close(context.Background())
		kagManager = kag.NewKAGManager(neo4jDriver, llm.NewClient())
		slog.Info("Connected to Neo4j successfully, KAG retrieval enabled")
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

	// 4. 优雅退出处理：监听系统级退出信号
	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	defer signal.Stop(quit)
	<-quit
	slog.Info("Shutdown signal received, terminating worker child gracefully...", "child_index", childIndex)

	pool.Stop() // 停止消费队列任务，等待当前任务完成
	slog.Info("Worker child exiting", "child_index", childIndex)
}
