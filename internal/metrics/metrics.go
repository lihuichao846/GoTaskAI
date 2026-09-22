// Package metrics 定义 Prometheus 指标，供 API 与 Worker 进程暴露 /metrics 供采集。
// 各进程独立使用 prometheus.DefaultRegisterer，指标进程内累计、由各自 /metrics 端点暴露。
package metrics

import (
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

var (
	// TaskDuration 记录单次任务运行的耗时（秒），按状态分桶。
	// 采用适合 LLM 任务的 bucket，覆盖 1s ~ 300s 长尾（默认超时 120s）。
	TaskDuration = promauto.NewHistogramVec(prometheus.HistogramOpts{
		Name:    "gotaskai_task_duration_seconds",
		Help:    "单次任务运行耗时（秒），按状态分桶",
		Buckets: []float64{1, 5, 10, 30, 60, 120, 300},
	}, []string{"status"})

	// TaskTotal 记录任务运行次数（含重试），按状态分桶。
	// 注意：一次任务若重试多次，会按每次运行的状态各记一次；终态任务数可用 status=completed/failed 过滤。
	TaskTotal = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "gotaskai_task_runs_total",
		Help: "任务运行次数（含重试，按状态）",
	}, []string{"status"})

	// TaskRetries 记录任务进入重试的次数。
	TaskRetries = promauto.NewCounter(prometheus.CounterOpts{
		Name: "gotaskai_task_retries_total",
		Help: "任务重试次数",
	})

	// LLMCalls 记录模型调用次数，按队列（task:process / kb:build）分桶。
	LLMCalls = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "gotaskai_llm_calls_total",
		Help: "模型调用次数（按队列）",
	}, []string{"queue"})

	// LLMTokens 记录模型 token 消耗，按队列与方向（in/out）分桶。
	LLMTokens = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "gotaskai_llm_tokens_total",
		Help: "模型 token 消耗（按队列与方向）",
	}, []string{"queue", "direction"})

	// LLMCost 记录模型成本（美元），按队列分桶。
	LLMCost = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "gotaskai_llm_cost_usd_total",
		Help: "模型成本（美元，按队列）",
	}, []string{"queue"})

	// ToolCalls 记录工具调用次数，按状态分桶。
	ToolCalls = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "gotaskai_tool_calls_total",
		Help: "工具调用次数（按状态）",
	}, []string{"status"})

	// KBBuildTotal 记录知识库文档构建数量，按状态分桶。
	KBBuildTotal = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "gotaskai_kb_build_total",
		Help: "知识库文档构建数量（按状态）",
	}, []string{"status"})

	// CompressTotal 记录上下文压缩【批次】尝试次数。
	// 粒度说明：一次分批压缩中的每一批各计一次（与压缩调用一一对应），
	// 因此 CompressFailed / CompressTotal 即批次级失败率。
	CompressTotal = promauto.NewCounter(prometheus.CounterOpts{
		Name: "gotaskai_context_compress_total",
		Help: "上下文压缩批次尝试次数",
	})

	// CompressFailed 记录上下文压缩批次失败次数（CompressFailed / CompressTotal 即失败率）。
	CompressFailed = promauto.NewCounter(prometheus.CounterOpts{
		Name: "gotaskai_context_compress_failed_total",
		Help: "上下文压缩批次失败次数",
	})

	// ContextTotalTurns 记录每次上下文装载时「游标之后的历史总轮数」（SQL COUNT 结果）。
	// 它与 ContextLoadedTurns 的差值即"真实溢出"轮数，是判断压缩是否在推进的关键依据。
	ContextTotalTurns = promauto.NewHistogram(prometheus.HistogramOpts{
		Name:    "gotaskai_context_total_turns",
		Help:    "游标之后的历史总轮数（每次上下文装载）",
		Buckets: []float64{1, 5, 10, 20, 50, 100, 500, 1000, 5000},
	})

	// ContextLoadedTurns 记录每次实际装载进 prompt 的历史轮数。
	ContextLoadedTurns = promauto.NewHistogram(prometheus.HistogramOpts{
		Name:    "gotaskai_context_loaded_turns",
		Help:    "实际装载进 prompt 的历史轮数",
		Buckets: []float64{1, 5, 10, 20, 50, 100, 500, 1000},
	})

	// ContextUnloadedTurns 记录每次装载后「未装载（真实溢出）」的轮数。
	// 该值长期不下降意味着压缩没有推进（回归告警依据）。
	ContextUnloadedTurns = promauto.NewHistogram(prometheus.HistogramOpts{
		Name:    "gotaskai_context_unloaded_turns",
		Help:    "未装载（溢出）的历史轮数",
		Buckets: []float64{1, 5, 10, 20, 50, 100, 500, 1000, 5000},
	})

	// ContextBudgetUsedRatio 记录装载预算的使用率（已用 token / 装载预算）。
	ContextBudgetUsedRatio = promauto.NewHistogram(prometheus.HistogramOpts{
		Name:    "gotaskai_context_budget_used_ratio",
		Help:    "历史装载预算使用率",
		Buckets: []float64{0.1, 0.2, 0.3, 0.4, 0.5, 0.6, 0.7, 0.8, 0.9, 1.0},
	})

	// CompressRoundsPerRequest 记录单次上下文装载触发的压缩批数（观察是否长期触顶 max_compress_rounds）。
	CompressRoundsPerRequest = promauto.NewHistogram(prometheus.HistogramOpts{
		Name:    "gotaskai_context_compress_rounds_per_request",
		Help:    "单次上下文装载触发的压缩批数",
		Buckets: []float64{1, 2, 3, 5, 8, 10, 20},
	})

	// SummaryCompressIneffective 记录摘要二次精简"未变短"的次数（用于确认收敛判据生效）。
	SummaryCompressIneffective = promauto.NewCounter(prometheus.CounterOpts{
		Name: "gotaskai_context_summary_compress_ineffective_total",
		Help: "摘要二次精简未产生更短结果的次数",
	})

	// LLMCachedTokens 记录命中 prompt 缓存的输入 token 数（按队列）。
	// 用于量化缓存收益，并使成本折算能区分缓存命中/未命中单价。
	LLMCachedTokens = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "gotaskai_llm_cached_tokens_total",
		Help: "命中 prompt 缓存的输入 token 数（按队列）",
	}, []string{"queue"})

	// IntentDecisions 记录意图路由决策次数，按决策来源（rule/semantic/llm/fallback 等）分桶。
	// 各层命中率可直接由 source 分布计算。
	IntentDecisions = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "gotaskai_intent_decisions_total",
		Help: "意图路由决策次数（按决策来源）",
	}, []string{"source"})

	// IntentRouteLatency 记录路由阶段（语义+LLM）的耗时（秒）。
	IntentRouteLatency = promauto.NewHistogram(prometheus.HistogramOpts{
		Name:    "gotaskai_intent_route_latency_seconds",
		Help:    "意图路由阶段耗时（秒）",
		Buckets: []float64{0.01, 0.05, 0.1, 0.25, 0.5, 1, 2, 5},
	})

	// IntentRetrievalSkipped 记录跳过 RAG 检索的次数（NeedRAG=false），用于量化成本节省。
	IntentRetrievalSkipped = promauto.NewCounter(prometheus.CounterOpts{
		Name: "gotaskai_intent_retrieval_skipped_total",
		Help: "因意图路由跳过 RAG 检索的次数",
	})

	// MemoryWriteTotal 记录长期记忆的写入结果，按结果分桶。
	// result=created 正常写入；rejected 被闸门拒绝（类型/长度/配额）；superseded 由新事实取代旧事实。
	MemoryWriteTotal = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "gotaskai_memory_write_total",
		Help: "长期记忆写入次数（按结果）",
	}, []string{"result"})

	// MemoryRecallItems 记录单次召回注入的记忆条数。
	MemoryRecallItems = promauto.NewHistogram(prometheus.HistogramOpts{
		Name:    "gotaskai_memory_recall_items",
		Help:    "单次召回注入的记忆条数",
		Buckets: []float64{0, 1, 2, 4, 8, 16, 32},
	})

	// MemoryRecallTokens 记录单次召回注入占用的 token 数。
	// 它与 ContextBudgetUsedRatio 一起用于确认"记忆没有挤压历史装载预算"。
	MemoryRecallTokens = promauto.NewHistogram(prometheus.HistogramOpts{
		Name:    "gotaskai_memory_recall_tokens",
		Help:    "单次召回注入的 token 数",
		Buckets: []float64{0, 50, 100, 200, 400, 800, 1600},
	})

	// MemoryRecallFailClosed 记录因作用域非法（拿不到 user_id）而【放弃】召回的次数。
	// 该指标必须恒为 0 或仅有理论值：一旦持续 >0，说明调用链丢了用户身份，
	// 而记忆的隔离要求是 fail-closed（宁可不注入，也绝不回退到任何"公共"集合）。
	MemoryRecallFailClosed = promauto.NewCounter(prometheus.CounterOpts{
		Name: "gotaskai_memory_recall_fail_closed_total",
		Help: "因作用域非法（user_id 缺失）放弃召回的记忆次数",
	})
)
