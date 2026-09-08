package config

import (
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/viper"
)

type Config struct {
	Server   ServerConfig   `mapstructure:"server"`
	MySQL    MySQLConfig    `mapstructure:"mysql"`
	Redis    RedisConfig    `mapstructure:"redis"`
	EventBus EventBusConfig `mapstructure:"eventbus"`
	Neo4j    Neo4jConfig    `mapstructure:"neo4j"`
	Queue    QueueConfig    `mapstructure:"queue"`
	LLM      LLMConfig      `mapstructure:"llm"`
	Rerank   RerankConfig   `mapstructure:"rerank"`
	Compress CompressConfig `mapstructure:"compress"`
	Context  ContextConfig  `mapstructure:"context"`
	Metrics  MetricsConfig  `mapstructure:"metrics"`
	Milvus   MilvusConfig   `mapstructure:"milvus"`
}

// EventBusConfig 配置 Worker 与 API 之间实时事件的独立消息通道（NATS）。
type EventBusConfig struct {
	Enabled   bool   `mapstructure:"enabled"`
	URL       string `mapstructure:"url"`       // NATS 地址，如 nats://0.0.0.0:4222
	Embedded  bool   `mapstructure:"embedded"`  // 是否在本进程内嵌启动 nats-server（仅 API 进程设置为 true）
}

type ServerConfig struct {
	Port int    `mapstructure:"port"`
	Mode string `mapstructure:"mode"`
}

type MySQLConfig struct {
	DSN          string `mapstructure:"dsn"`
	MaxIdleConns int    `mapstructure:"max_idle_conns"`
	MaxOpenConns int    `mapstructure:"max_open_conns"`
}

type RedisConfig struct {
	Mode          string   `mapstructure:"mode"`
	Addr          string   `mapstructure:"addr"`
	Password      string   `mapstructure:"password"`
	DB            int      `mapstructure:"db"`
	MasterName    string   `mapstructure:"master_name"`
	SentinelAddrs []string `mapstructure:"sentinel_addrs"`
}

func (c RedisConfig) UseSentinel() bool {
	return strings.EqualFold(strings.TrimSpace(c.Mode), "sentinel")
}

type Neo4jConfig struct {
	URI      string `mapstructure:"uri"`
	Username string `mapstructure:"username"`
	Password string `mapstructure:"password"`
}

type MilvusConfig struct {
	Host           string `mapstructure:"host"`
	Port           int    `mapstructure:"port"`
	CollectionName string `mapstructure:"collection_name"`
	Dim            int    `mapstructure:"dim"`
}

type QueueConfig struct {
	Capacity  int `mapstructure:"capacity"`
	Workers   int `mapstructure:"workers"`
	Processes int `mapstructure:"processes"`
}

type LLMConfig struct {
	APIKey           string `mapstructure:"api_key"`
	BaseURL          string `mapstructure:"base_url"`
	Model            string `mapstructure:"model"`
	EmbeddingModel   string `mapstructure:"embedding_model"`
	EmbeddingAPIKey  string `mapstructure:"embedding_api_key"`
	EmbeddingBaseURL string `mapstructure:"embedding_base_url"`
	// TimeoutSeconds 为单次 LLM 调用超时（秒），<=0 时回退到默认 120 秒。
	TimeoutSeconds int `mapstructure:"timeout_seconds"`
	// MaxCallsPerTask 为单个任务单次运行允许的最大模型调用次数（防工具乒乓），<=0 时由 worker 兜底为默认值。
	MaxCallsPerTask int `mapstructure:"max_calls_per_task"`
	// MaxTotalCallsPerTask 为单个任务跨重试累计允许的最大模型调用次数，<=0 时由 worker 兜底为默认值。
	MaxTotalCallsPerTask int `mapstructure:"max_total_calls_per_task"`
	// CostPer1MIn / CostPer1MOut 为模型输入/输出单价（美元 / 百万 token），用于 CostUSD 折算，未配置为 0。
	CostPer1MIn  float64 `mapstructure:"cost_per_1m_in"`
	CostPer1MOut float64 `mapstructure:"cost_per_1m_out"`
	// ContextWindow 为当前对话模型支持的上下文窗口（token 数），用于判断何时触发上下文压缩。
	ContextWindow int `mapstructure:"context_window"`
	// CompressThreshold 为触发压缩的占用比例（如 0.7 表示上下文达到窗口 70% 时压缩）。
	CompressThreshold float64 `mapstructure:"compress_threshold"`
}

// CompressConfig 为对话上下文自动压缩（摘要）提供配置。
type CompressConfig struct {
	Enabled    bool `mapstructure:"enabled"`
	MaxHistory int  `mapstructure:"max_history"` // 单次任务最多加载的历史轮数
	KeepRecent int  `mapstructure:"keep_recent"` // 压缩时保留下来的最近原始消息轮数
	MinTurns   int  `mapstructure:"min_turns"`   // 历史轮数少于该值时不做压缩
}

// RerankConfig 为召回重排（rerank）提供配置，默认指向阿里云百炼 DashScope 的 qwen3-rerank 云端接口。
type RerankConfig struct {
	Enabled bool   `mapstructure:"enabled"`
	BaseURL string `mapstructure:"base_url"`
	APIKey  string `mapstructure:"api_key"`
	Model   string `mapstructure:"model"`
	TopN    int    `mapstructure:"top_n"`
}

// MetricsConfig 为 Prometheus /metrics 端点配置。
type MetricsConfig struct {
	// Port 为 Worker 子进程 /metrics 监听基础端口（子进程实际使用 Port + childIndex + 1）。
	// API 进程的 /metrics 挂载在 server.port 上，不使用本端口。
	Port int `mapstructure:"port"`
}

// ContextConfig 控制知识库上下文的渐进式披露（目标五）：
// 通过摘要层、命中片段截断与总量上限，在知识量大时压低注入上下文体积。
type ContextConfig struct {
	// MaxChunkChars 为单个命中片段的最大注入字符数，超长截断；<=0 不截断。
	MaxChunkChars int `mapstructure:"max_chunk_chars"`
	// MaxContextChars 为知识上下文（含摘要层与命中层）的总字符数上限；<=0 不限制。
	MaxContextChars int `mapstructure:"max_context_chars"`
	// RagScoreThreshold 为 RAG 分层阈值（分数已 min-max 归一化到 [0,1]，最低分=0、最高分=1）：
	// Score >= 阈值注入全文，否则降级为短截断；<=0 不启用分层。
	RagScoreThreshold float32 `mapstructure:"rag_score_threshold"`
	// GraphTopN 为 KAG 图谱检索注入的命中关系条数上限；<=0 不限制。
	GraphTopN int `mapstructure:"graph_top_n"`
	// ChunkContextWindow 为上下文感知分块（B1）窗口大小：
	// 命中块前后各扩展的相邻块数；<=0 关闭窗口扩展（等价改造前行为）。默认 1。
	ChunkContextWindow int `mapstructure:"chunk_context_window"`
	// EnableParentContext 控制是否启用上一级文档上下文兜底（B1 扩展失败/存量缺 chunk_index 时，
	// 回退为 Document.Content 全文）。默认 true。
	EnableParentContext bool `mapstructure:"enable_parent_context"`
}

var AppConfig Config
var ProjectRoot string

// InitConfig 初始化配置，支持从文件和环境变量读取
func InitConfig(configPath string) {
	resolvedConfigPath, projectRoot, err := resolveConfigPath(configPath)
	if err != nil {
		log.Fatalf("Error reading config file: %v", err)
	}

	ProjectRoot = projectRoot
	viper.SetConfigFile(resolvedConfigPath)
	viper.AutomaticEnv()                                   // 读取环境变量
	viper.SetEnvKeyReplacer(strings.NewReplacer(".", "_")) // 例如 MYSQL_DSN

	if err := viper.ReadInConfig(); err != nil {
		log.Fatalf("Error reading config file: %v", err)
	}

	if err := viper.Unmarshal(&AppConfig); err != nil {
		log.Fatalf("Unable to decode into struct: %v", err)
	}

	log.Println("Configuration loaded successfully")
}

// ResolveProjectPath 将相对项目根目录的路径解析为绝对路径。
func ResolveProjectPath(parts ...string) string {
	if ProjectRoot == "" {
		root, err := findProjectRoot()
		if err != nil {
			return filepath.Join(parts...)
		}
		ProjectRoot = root
	}

	allParts := append([]string{ProjectRoot}, parts...)
	return filepath.Join(allParts...)
}

func resolveConfigPath(configPath string) (string, string, error) {
	if configPath == "" {
		configPath = filepath.Join("config", "config.yaml")
	}
	if filepath.IsAbs(configPath) {
		if !fileExists(configPath) {
			return "", "", fmt.Errorf("config file %q not found", configPath)
		}
		root, err := findProjectRootFrom(filepath.Dir(configPath))
		if err != nil {
			root = filepath.Dir(filepath.Dir(configPath))
		}
		return configPath, root, nil
	}

	relativePath := filepath.FromSlash(configPath)
	for _, startDir := range candidateStartDirs() {
		dir := startDir
		for {
			candidate := filepath.Join(dir, relativePath)
			if fileExists(candidate) {
				return candidate, dir, nil
			}

			parent := filepath.Dir(dir)
			if parent == dir {
				break
			}
			dir = parent
		}
	}

	return "", "", fmt.Errorf("config file %q not found from current working directory or executable path", configPath)
}

func findProjectRoot() (string, error) {
	for _, startDir := range candidateStartDirs() {
		if root, err := findProjectRootFrom(startDir); err == nil {
			return root, nil
		}
	}
	return "", fmt.Errorf("project root not found")
}

func findProjectRootFrom(startDir string) (string, error) {
	dir := startDir
	for {
		if fileExists(filepath.Join(dir, "go.mod")) && fileExists(filepath.Join(dir, "config", "config.yaml")) {
			return dir, nil
		}

		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}

	return "", fmt.Errorf("project root not found from %q", startDir)
}

func candidateStartDirs() []string {
	var dirs []string
	seen := make(map[string]struct{})

	addDir := func(dir string) {
		if dir == "" {
			return
		}
		cleanDir := filepath.Clean(dir)
		if _, ok := seen[cleanDir]; ok {
			return
		}
		seen[cleanDir] = struct{}{}
		dirs = append(dirs, cleanDir)
	}

	if cwd, err := os.Getwd(); err == nil {
		addDir(cwd)
	}
	if exePath, err := os.Executable(); err == nil {
		addDir(filepath.Dir(exePath))
	}

	return dirs
}

func fileExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && !info.IsDir()
}
