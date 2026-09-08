package queryexpand

import (
	"context"
	"strings"
)

// SemanticExpander 语义扩展：为查询生成若干语义等价/相关的改写变体，
// 供 dense 多路召回后经 RRF 融合，弥补「单一查询向量」对表述差异的敏感。
// 生产实现走 LLM（一次调用生成 N 个改写），延迟与成本较高；
// 测试/离线场景可用规则生成器（RuleVariantGenerator）做确定性验证。
type SemanticExpander struct {
	gen         VariantGenerator
	maxVariants int
}

// NewSemanticExpander 构建语义扩展器；maxVariants<=0 时回退默认 3。
func NewSemanticExpander(gen VariantGenerator, maxVariants int) *SemanticExpander {
	if maxVariants <= 0 {
		maxVariants = 3
	}
	return &SemanticExpander{gen: gen, maxVariants: maxVariants}
}

func (e *SemanticExpander) Name() string { return "semantic" }

func (e *SemanticExpander) Expand(ctx context.Context, query string) ([]Term, []string) {
	if e.gen == nil {
		return nil, nil
	}
	variants, err := e.gen.Variants(ctx, query, e.maxVariants)
	if err != nil || len(variants) == 0 {
		return nil, nil
	}
	seen := map[string]bool{normalizeTerm(query): true}
	out := make([]string, 0, len(variants))
	for _, v := range variants {
		key := normalizeTerm(v)
		if key == "" || seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, v)
		if len(out) >= e.maxVariants {
			break
		}
	}
	return nil, out
}

// FuncVariantGenerator 将一个函数适配为 VariantGenerator 接口。
type FuncVariantGenerator func(ctx context.Context, query string, maxN int) ([]string, error)

func (f FuncVariantGenerator) Variants(ctx context.Context, query string, maxN int) ([]string, error) {
	return f(ctx, query, maxN)
}

// RuleVariantGenerator 是确定性的规则改写生成器，用于原型/测试与离线评测：
// 它不产生真正语义改写，而是做「问句→陈述句」式轻量变换，验证流水线串联与融合正确性。
type RuleVariantGenerator struct{}

func (RuleVariantGenerator) Variants(_ context.Context, query string, maxN int) ([]string, error) {
	var out []string
	// 中文问句尾缀 → 陈述句核心词，供评测中触发更多词法命中。
	rewrites := []struct{ in, out string }{
		{"什么是", ""},
		{"是什么", ""},
		{"有哪些", ""},
		{"如何", ""},
		{"吗", ""},
		{"?", ""},
		{"？", ""},
		{"请", ""},
	}
	for _, r := range rewrites {
		if strings.Contains(query, r.in) {
			v := strings.Replace(query, r.in, r.out, 1)
			if v != query && strings.TrimSpace(v) != "" {
				out = append(out, v)
			}
			break
		}
	}
	if len(out) == 0 && strings.TrimSpace(query) != "" {
		out = append(out, query)
	}
	if len(out) > maxN {
		out = out[:maxN]
	}
	return out, nil
}
