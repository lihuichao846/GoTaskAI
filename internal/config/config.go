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
	KB       KBConfig       `mapstructure:"kb"`
	LLM      LLMConfig      `mapstructure:"llm"`
	Rerank   RerankConfig   `mapstructure:"rerank"`
	Compress CompressConfig `mapstructure:"compress"`
	Context  ContextConfig  `mapstructure:"context"`
	Intent   IntentConfig   `mapstructure:"intent"`
	Metrics  MetricsConfig  `mapstructure:"metrics"`
	Milvus   MilvusConfig   `mapstructure:"milvus"`
}

// EventBusConfig 配置 Worker 与 API 之间实时事件的独立消息通道（NATS）。
type EventBusConfig struct {
	Enabled  bool   `mapstructure:"enabled"`
	URL      string `mapstructure:"url"`      // NATS 地址，如 nats://0.0.0.0:4222
	Embedded bool   `mapstructure:"embedded"` // 是否在本进程内嵌启动 nats-server（仅 API 进程设置为 true）
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

// KBConfig 为知识库建库链路的开关与限流配置（W1 图谱抽取入队 / W2 定时增量入库）。
type KBConfig struct {
	// GraphEnabled 控制建库任务是否执行图谱抽取阶段；默认 false，灰度开启。
	GraphEnabled bool `mapstructure:"graph_enabled"`
	// GraphQueueConcurrency 为图谱阶段并发上限（与向量阶段共用 low 队列，靠信号量限流）；<=0 表示不限制。
	GraphQueueConcurrency int `mapstructure:"graph_queue_concurrency"`

	// ScanEnabled 控制是否注册周期扫描任务 kb:scan（默认 false，先在单实例灰度）。
	ScanEnabled bool `mapstructure:"scan_enabled"`
	// ScanCron 为扫描周期（标准 5 段 cron，如 "0 3 * * *" 表示每日 03:00）；为空时不注册。
	ScanCron string `mapstructure:"scan_cron"`
	// ScanBatchSize 为单次扫描窗口最多入队的文档数，防雪崩；<=0 表示不限制。
	ScanBatchSize int `mapstructure:"scan_batch_size"`
	// ScanSources 为增量扫描的源目录列表，每个源绑定一个知识库。
	ScanSources []KBSourceConfig `mapstructure:"scan_sources"`
}

// KBSourceConfig 描述一个增量扫描源：把 Path 目录下的文件同步进 KBID 知识库。
// 源文件与文档以 (kb_id, source_path) 为唯一键，内容 sha256 作为变更判据。
type KBSourceConfig struct {
	KBID string `mapstructure:"kb_id"`
	Path string `mapstructure:"path"`
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
	// CostPer1MIn / CostPer1MInCached / CostPer1MOut 为模型输入（未命中缓存）/ 输入（命中缓存）/ 输出
	// 单价（美元 / 百万 token），用于 CostUSD 折算，未配置为 0。
	// CostPer1MInCached 未配置（为 0）时按 CostPer1MIn 计价，即不区分缓存命中。
	CostPer1MIn       float64 `mapstructure:"cost_per_1m_in"`
	CostPer1MInCached float64 `mapstructure:"cost_per_1m_in_cached"`
	CostPer1MOut      float64 `mapstructure:"cost_per_1m_out"`
	// ContextWindow 为当前对话模型支持的上下文窗口（token 数），用于上下文装载预算与溢出校验。
	ContextWindow int `mapstructure:"context_window"`
	// OutputReserve 为每次请求为模型输出预留的 token 数（从装载预算中扣除），<=0 时不预留。
	OutputReserve int `mapstructure:"output_reserve"`
	// MaxOutputTokens 为请求中显式设置的 max_tokens；<=0 时不设置（沿用供应商默认值）。
	MaxOutputTokens int `mapstructure:"max_output_tokens"`
}

// CompressConfig 为对话上下文自动压缩（摘要）提供配置。
//
// 语义说明（v1.1 起）：历史装载由 token 预算驱动，MaxLoadTurns 仅作安全上限；
// 压缩的触发条件是"有历史装不下"（真实溢出），而非"上下文占用超过窗口比例"。
type CompressConfig struct {
	Enabled bool `mapstructure:"enabled"`
	// MaxLoadTurns 为单次任务装载历史的安全上限（实际装载量由 token 预算决定，本值仅防极端）。
	// 旧配置项 max_history 已废弃（语义不同，不再读取）。
	MaxLoadTurns int `mapstructure:"max_load_turns"`
	// KeepRecent 为压缩时保留下来的最近原始轮数，同时作为装载的最低保障轮数。
	KeepRecent int `mapstructure:"keep_recent"`
	// MinTurns 为触发压缩所需的最小溢出轮数（溢出少于该值时可容忍不压）。
	MinTurns int `mapstructure:"min_turns"`
	// BudgetRatio 为装载预算占上下文窗口的比例（如 0.6 表示用 60% 窗口装载历史）。
	BudgetRatio float64 `mapstructure:"budget_ratio"`
	// MaxCompressInputTokens 为单批压缩请求的输入 token 上限（含已有摘要与提示词模板开销）。
	MaxCompressInputTokens int `mapstructure:"max_compress_input_tokens"`
	// MaxCompressTurns 为单批压缩的轮数上限。
	MaxCompressTurns int `mapstructure:"max_compress_turns"`
	// MaxCompressRounds 为单次任务运行内最多执行的压缩批数（限制单次收敛速度，防超长会话拖垮请求）。
	// 一批 = 一次 LLM 调用，因此本项已同时约束单次运行的压缩调用次数。
	MaxCompressRounds int `mapstructure:"max_compress_rounds"`
	// MaxSummaryTokens 为滚动摘要的长度上限（超出时触发一次精简），<=0 时不限制。
	MaxSummaryTokens int `mapstructure:"max_summary_tokens"`
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

// IntentConfig 控制 Agent 检索/工具路由（成本优化）：在检索之前判断本次任务
// 是否需要执行 RAG（Milvus）与 KAG（Neo4j）检索，跳过不必要的检索与 LLM 调用。
// 采用「规则兜底 + 语义快路径 + LLM 结构化分类」三级混合，任一环节不确定即保守回退全量检索。
type IntentConfig struct {
	// Enabled 为总开关；false 时行为与改造前完全一致（不产生任何新增调用）。
	Enabled bool `mapstructure:"enabled"`
	// Mode 为路由模式：rule（仅寒暄短路）| semantic（+语义层）| hybrid（+LLM 兜底）；默认 rule。
	Mode string `mapstructure:"mode"`
	// SemanticHigh 为语义层采信阈值（相似度低于该值则下传下一层）。
	SemanticHigh float64 `mapstructure:"semantic_high"`
	// SemanticMargin 为语义层 top1 与「异类 top2」的最小间隔，用于排除类别歧义。
	SemanticMargin float64 `mapstructure:"semantic_margin"`
	// SemanticAllowClose 是否允许语义层「关闭检索」（默认 false，需评测漏检率 ≤2% 才可开启）。
	SemanticAllowClose bool `mapstructure:"semantic_allow_close"`
	// LLMMinConfidence 为 LLM 分类结果的采信阈值，低于该值走安全阀回退。
	LLMMinConfidence float64 `mapstructure:"llm_min_confidence"`
	// RouteTimeoutSeconds 为路由阶段（语义+LLM）的总超时（秒），<=0 时回退默认 5 秒。
	RouteTimeoutSeconds int `mapstructure:"route_timeout_seconds"`
	// EnableToolRouting 开启时 LLM 使用四分类提示词并据此裁剪工具（默认 false，暂不裁剪）。
	EnableToolRouting bool `mapstructure:"enable_tool_routing"`
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
