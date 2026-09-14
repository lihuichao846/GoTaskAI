// Package intent 提供 Agent 检索/工具路由：在检索之前判断本次任务是否需要执行
// RAG（Milvus 知识库）与 KAG（Neo4j 图谱）检索，以降低成本。
//
// 路由采用三级混合架构，逐级升级、逐级兜底：
//  1. 规则层：仅一条高精度「纯寒暄」锚定全串规则，命中即关闭检索（零调用）。
//  2. 语义层：与意图原型库做向量比对，高置信即决策（1 次 embedding）；
//     默认只做正向加速（判「需要检索」），关闭检索需显式开启。
//  3. LLM 层：默认二分类（是否需要本地检索），开启工具裁剪时升级为四分类。
//
// 任一环节失败、低置信、疑似注入或检索能力不可用时，一律保守回退到全量检索。
package intent

import (
	"context"

	"gotaskai/internal/pkg/llm"
)

// Intent 为意图类别枚举；默认路由模式下不产出（仅开启工具裁剪时填充）。
type Intent string

const (
	IntentChat      Intent = "chat"
	IntentKnowledge Intent = "knowledge"
	IntentTool      Intent = "tool"
	IntentMixed     Intent = "mixed"
)

// Source 标识本次决策来源，用于指标与排障。
type Source string

const (
	// SourceDisabled 总开关关闭。
	SourceDisabled Source = "disabled"
	// SourceNoRetrievalCapability 无可用检索能力（无知识库且无图谱）。
	SourceNoRetrievalCapability Source = "no_retrieval_capability"
	// SourceRule 命中规则层。
	SourceRule Source = "rule"
	// SourceRuleOnly mode=rule 且规则未命中。
	SourceRuleOnly Source = "rule-only"
	// SourceSemantic 命中语义层。
	SourceSemantic Source = "semantic"
	// SourceSemanticMiss mode=semantic 且语义层未命中。
	SourceSemanticMiss Source = "semantic-miss"
	// SourceInjection 疑似提示注入，走保守回退。
	SourceInjection Source = "injection-guard"
	// SourceLLM LLM 分类层决策。
	SourceLLM Source = "llm"
	// SourceFallback 分类失败或置信度不足，走保守回退。
	SourceFallback Source = "fallback"
)

// Decision 是路由决策输出，供 Worker 决定是否执行检索与是否注入联网指令。
type Decision struct {
	// Intent 仅开启工具裁剪（A3）时填充；默认模式下为空。
	Intent Intent `json:"intent,omitempty"`
	// NeedRAG 表示是否需要执行 retrieveKnowledge（Milvus 知识库检索）。
	NeedRAG bool `json:"need_rag"`
	// NeedKAG 表示是否需要执行 KAG 图谱检索。
	NeedKAG bool `json:"need_kag"`
	// NeedTools 仅在开启工具裁剪时生效；默认不裁剪工具。
	NeedTools bool `json:"need_tools"`
	// Confidence 为决策置信度，供安全阀与指标使用。
	Confidence float64 `json:"confidence"`
	// Source 为决策来源。
	Source Source `json:"source"`
}

// Request 描述一次路由所需的上下文。
type Request struct {
	// Question 为用户问题（t.Payload）。
	Question string
	// HasKB 表示存在可用知识源（ragStore 就绪；未绑定知识库时会回退公共检索）。
	HasKB bool
	// HasKAG 表示图谱检索可用（kagManager 就绪）。
	HasKAG bool
	// HasInternet 表示本次任务绑定了 internet_search 工具。
	HasInternet bool

	// APIKey/BaseURL/Model 为 Agent 覆盖的模型凭证；为空时回退全局默认。
	APIKey  string
	BaseURL string
	Model   string
}

// Router 抽象路由能力，便于在 Worker 单测中注入 stub。
type Router interface {
	Route(ctx context.Context, req Request, budget *llm.Budget) Decision
}

// FullPipeline 返回保守的全量检索决策（安全阀）：能做检索的都做，工具保持可用。
func FullPipeline(req Request, src Source) Decision {
	return Decision{
		NeedRAG:    req.HasKB,
		NeedKAG:    req.HasKAG,
		NeedTools:  true,
		Confidence: 0,
		Source:     src,
	}
}
