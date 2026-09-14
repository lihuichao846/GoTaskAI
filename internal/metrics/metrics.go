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

	// CompressTotal 记录上下文压缩尝试次数（用于计算压缩失败率）。
	CompressTotal = promauto.NewCounter(prometheus.CounterOpts{
		Name: "gotaskai_context_compress_total",
		Help: "上下文压缩尝试次数",
	})

	// CompressFailed 记录上下文压缩失败次数（CompressFailed / CompressTotal 即失败率）。
	CompressFailed = promauto.NewCounter(prometheus.CounterOpts{
		Name: "gotaskai_context_compress_failed_total",
		Help: "上下文压缩失败次数",
	})

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
)
