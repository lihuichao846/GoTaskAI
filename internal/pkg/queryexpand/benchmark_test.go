package queryexpand

import (
	"context"
	"testing"
)

// 基准：衡量各扩展器与整体协调器的单次扩展耗时（确定性、无外部依赖的实现）。

var benchCtx = context.Background()
var benchSyn = MapSynonymProvider{"汽车": {"机动车", "轿车", "车辆"}, "人工智能": {"AI", "机器学习"}}
var benchGr = NewMapGraphResolver(
	map[string]string{"DeepSeek": "DeepSeek", "深度求索": "DeepSeek"},
	map[string][]string{"DeepSeek": {"深度求索"}},
	map[string][]string{"DeepSeek": {"深度求索公司", "DeepSeek-V3"}},
)

func BenchmarkSynonymExpand(b *testing.B) {
	e := NewSynonymExpander(benchSyn, 10)
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		_, _ = e.Expand(benchCtx, "新能源汽车与人工智能的关系")
	}
}

func BenchmarkEntityExpand(b *testing.B) {
	e := NewEntityLinkExpander(benchGr, 5, 3, 1)
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		_, _ = e.Expand(benchCtx, "DeepSeek 是谁创立的")
	}
}

func BenchmarkDedupTerms(b *testing.B) {
	terms := []Term{
		{Text: "汽车", Weight: 0.9, Source: "synonym"},
		{Text: "car", Weight: 0.8, Source: "entity"},
		{Text: "汽车", Weight: 0.9, Source: "entity"},
		{Text: "AI", Weight: 0.7, Source: "synonym"},
	}
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		_ = dedupTerms(terms)
	}
}

func BenchmarkCoordinatorExpand(b *testing.B) {
	cfg := DefaultConfig()
	cfg.Semantic.Enabled = true
	c := NewCoordinator(cfg, benchSyn, RuleVariantGenerator{}, benchGr, nil)
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		_ = c.Expand(benchCtx, "什么是汽车？DeepSeek 是谁创立的")
	}
}
