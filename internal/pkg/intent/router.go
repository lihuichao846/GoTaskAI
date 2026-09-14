package intent

import (
	"context"

	"gotaskai/internal/pkg/llm"
)

// 路由模式。
const (
	// ModeRule 仅规则层（纯寒暄短路），不产生任何新增模型调用。
	ModeRule = "rule"
	// ModeSemantic 规则层 + 语义层，不调用 LLM。
	ModeSemantic = "semantic"
	// ModeHybrid 规则层 + 语义层 + LLM 兜底。
	ModeHybrid = "hybrid"
)

// Config 描述路由层配置。
type Config struct {
	// Enabled 为总开关；关闭时 Route 直接返回全量检索（等价改造前行为）。
	Enabled bool
	// Mode 为路由模式：rule | semantic | hybrid，空值回退 rule。
	Mode string
	// SemanticHigh 为语义层采信阈值。
	SemanticHigh float64
	// SemanticMargin 为 top1 与异类 top2 的最小间隔。
	SemanticMargin float64
	// SemanticAllowClose 是否允许语义层关闭检索（默认 false）。
	SemanticAllowClose bool
	// LLMMinConfidence 为 LLM 结果的采信阈值。
	LLMMinConfidence float64
	// EnableToolRouting 开启时 LLM 使用四分类提示词并驱动工具裁剪（A3）。
	EnableToolRouting bool
}

// semanticLayer 抽象语义层，便于单测注入。
type semanticLayer interface {
	Classify(ctx context.Context, req Request) (Decision, bool)
}

// llmLayer 抽象 LLM 分类层，便于单测注入。
type llmLayer interface {
	Classify(ctx context.Context, req Request, budget *llm.Budget) (Decision, error)
}

type router struct {
	rule     *RuleClassifier
	semantic semanticLayer
	llm      llmLayer
	mode     string
	minConf  float64
	enabled  bool
}

// New 构造路由编排器。llmClient 或 semantic 可为 nil（对应层自动跳过）。
func New(cfg Config, llmClient *llm.Client, semantic *SemanticClassifier) Router {
	var sem semanticLayer
	if semantic != nil {
		sem = semantic
	}
	var lc llmLayer
	if llmClient != nil {
		lc = NewLLMClassifier(llmClient, cfg.EnableToolRouting)
	}
	return newRouter(cfg, NewRuleClassifier(), sem, lc)
}

// newRouter 供包内单测注入 stub 使用。
func newRouter(cfg Config, rule *RuleClassifier, sem semanticLayer, lc llmLayer) *router {
	mode := cfg.Mode
	switch mode {
	case ModeRule, ModeSemantic, ModeHybrid:
	default:
		mode = ModeRule
	}
	minConf := cfg.LLMMinConfidence
	if minConf <= 0 {
		minConf = 0.6
	}
	return &router{
		rule:     rule,
		semantic: sem,
		llm:      lc,
		mode:     mode,
		minConf:  minConf,
		enabled:  cfg.Enabled,
	}
}

// Route 执行「规则 → 能力预检 → 语义 → 注入预检 → LLM → 安全阀」三级路由。
func (r *router) Route(ctx context.Context, req Request, budget *llm.Budget) Decision {
	if r == nil || !r.enabled {
		return FullPipeline(req, SourceDisabled)
	}

	// ① 规则层：唯一可关闭检索的高精度规则（纯寒暄）。
	if d, ok := r.rule.Classify(req); ok {
		return d
	}

	// ② 能力预检：无知识库且无图谱时，检索恒不执行，无需任何模型调用。
	if !req.HasKB && !req.HasKAG {
		return FullPipeline(req, SourceNoRetrievalCapability)
	}

	// ③ 仅规则模式：规则未命中即维持现状（不新增调用）。
	if r.mode == ModeRule {
		return FullPipeline(req, SourceRuleOnly)
	}

	// ④ 语义层：高置信快路径。
	if r.semantic != nil {
		if d, ok := r.semantic.Classify(ctx, req); ok {
			return d
		}
	}

	// ⑤ 仅语义模式：语义未命中即维持现状（不调用 LLM）。
	if r.mode == ModeSemantic {
		return FullPipeline(req, SourceSemanticMiss)
	}

	// ⑥ 注入预检：疑似提示注入直接保守回退。
	if isSuspectedInjection(req.Question) {
		return FullPipeline(req, SourceInjection)
	}

	// ⑦ LLM 分类层。
	if r.llm == nil {
		return FullPipeline(req, SourceFallback)
	}
	d, err := r.llm.Classify(ctx, req, budget)
	if err != nil || d.Confidence < r.minConf {
		return FullPipeline(req, SourceFallback)
	}

	// ⑧ 能力裁剪：即便模型判需要检索，也要与真实可用能力求交。
	d.Source = SourceLLM
	d.NeedRAG = d.NeedRAG && req.HasKB
	d.NeedKAG = d.NeedKAG && req.HasKAG
	return d
}
