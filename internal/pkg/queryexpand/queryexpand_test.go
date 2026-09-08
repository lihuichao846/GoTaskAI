package queryexpand

import (
	"context"
	"testing"
)

func TestDedupTerms(t *testing.T) {
	terms := []Term{
		{Text: "汽车", Weight: 0.9, Source: "synonym"},
		{Text: "car", Weight: 0.8, Source: "entity"},
		{Text: "汽车", Weight: 0.9, Source: "entity"}, // 同词多来源合并权重
		{Text: "", Weight: 0.5},                     // 空词剔除
		{Text: "  ", Weight: 0.5},
	}
	got := dedupTerms(terms)
	if len(got) != 2 {
		t.Fatalf("expected 2 terms, got %d: %+v", len(got), got)
	}
	// "汽车" 权重合并为 1.8，应排第一。
	if got[0].Text != "汽车" || got[0].Weight != 1.8 {
		t.Fatalf("expected merged 汽车/1.8 first, got %+v", got[0])
	}
	if got[1].Text != "car" {
		t.Fatalf("expected car second, got %+v", got[1])
	}
}

func TestDedupTerms_CapWeight(t *testing.T) {
	terms := []Term{
		{Text: "x", Weight: 1.5, Source: "synonym"},
		{Text: "x", Weight: 1.5, Source: "entity"},
	}
	got := dedupTerms(terms)
	if len(got) != 1 || got[0].Weight != 2.0 {
		t.Fatalf("expected weight capped at 2.0, got %+v", got)
	}
}

func TestSynonymExpander_ASCIIWordBoundary(t *testing.T) {
	sp := MapSynonymProvider{"car": {"automobile", "vehicle"}}
	e := NewSynonymExpander(sp, 10)
	terms, _ := e.Expand(context.Background(), "How fast is this car?")
	got := map[string]bool{}
	for _, tm := range terms {
		got[tm.Text] = true
	}
	if !got["automobile"] || !got["vehicle"] {
		t.Fatalf("expected automobile+vehicle, got %+v", terms)
	}
	// "carbon" 不应命中 "car"
	terms2, _ := e.Expand(context.Background(), "carbon footprint")
	if len(terms2) != 0 {
		t.Fatalf("expected no expansion for carbon, got %+v", terms2)
	}
}

func TestSynonymExpander_CJKSubstring(t *testing.T) {
	sp := MapSynonymProvider{"汽车": {"机动车", "轿车"}}
	e := NewSynonymExpander(sp, 10)
	terms, _ := e.Expand(context.Background(), "新能源汽车有哪些品牌")
	got := map[string]bool{}
	for _, tm := range terms {
		got[tm.Text] = true
	}
	if !got["机动车"] || !got["轿车"] {
		t.Fatalf("expected 机动车+轿车, got %+v", terms)
	}
}

func TestSemanticExpander_Rule(t *testing.T) {
	e := NewSemanticExpander(RuleVariantGenerator{}, 3)
	_, variants := e.Expand(context.Background(), "什么是知识图谱")
	if len(variants) != 1 || variants[0] != "知识图谱" {
		t.Fatalf("expected [知识图谱], got %+v", variants)
	}
	// 与原始查询重复时被剔除
	_, variants = e.Expand(context.Background(), "知识图谱")
	if len(variants) != 0 {
		t.Fatalf("expected no variant for plain query, got %+v", variants)
	}
}

func TestEntityLinkExpander(t *testing.T) {
	gr := NewMapGraphResolver(
		map[string]string{"DeepSeek": "DeepSeek", "深度求索": "DeepSeek"},
		map[string][]string{"DeepSeek": {"深度求索"}},
		map[string][]string{"DeepSeek": {"深度求索公司", "DeepSeek-V3"}},
	)
	e := NewEntityLinkExpander(gr, 5, 3, 1)
	terms, _ := e.Expand(context.Background(), "DeepSeek 是谁创立的")
	got := map[string]float32{}
	for _, tm := range terms {
		got[tm.Text] = tm.Weight
	}
	if got["deepseek"] != 1.0 {
		t.Fatalf("expected canonical deepseek/1.0, got %+v", got)
	}
	if got["深度求索"] != 0.8 {
		t.Fatalf("expected alias 深度求索/0.8, got %+v", got)
	}
	if got["深度求索公司"] != 0.6 || got["deepseek-v3"] != 0.6 {
		t.Fatalf("expected related entities, got %+v", got)
	}
}

func TestMultimodalExpander(t *testing.T) {
	mp := MapMultimodalProvider{"图表": {"柱状图", "数据可视化"}}
	e := NewMultimodalExpander(mp, 4)
	terms, _ := e.Expand(context.Background(), "营收趋势图表")
	got := map[string]bool{}
	for _, tm := range terms {
		got[tm.Text] = true
	}
	if !got["柱状图"] || !got["数据可视化"] {
		t.Fatalf("expected multimodal tags, got %+v", terms)
	}
	if terms[0].Source != "multimodal" {
		t.Fatalf("expected source multimodal, got %s", terms[0].Source)
	}
}

func TestCoordinator_MergeAndCap(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Semantic.Enabled = true
	cfg.Limits.MaxTotalTerms = 3
	c := NewCoordinator(
		cfg,
		MapSynonymProvider{"汽车": {"机动车", "轿车", "车辆", "座驾"}},
		RuleVariantGenerator{},
		NewMapGraphResolver(nil, nil, nil),
		nil,
	)
	got := c.Expand(context.Background(), "什么是汽车")
	if len(got.Terms) != 3 {
		t.Fatalf("expected 3 terms after cap, got %d: %+v", len(got.Terms), got.Terms)
	}
	if len(got.Variants) != 1 || got.Variants[0] != "汽车" {
		t.Fatalf("expected 1 variant 汽车, got %+v", got.Variants)
	}
	if len(got.AllQueries()) != 2 {
		t.Fatalf("expected 2 queries (original+variant), got %+v", got.AllQueries())
	}
}

func TestCoordinator_DisabledFallback(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Enabled = false
	c := NewCoordinator(cfg, nil, nil, nil, nil)
	got := c.Expand(context.Background(), "任意查询")
	if got.Original != "任意查询" || len(got.Terms) != 0 || len(got.Variants) != 0 {
		t.Fatalf("expected passthrough, got %+v", got)
	}
}

func TestCoordinator_EmptyQuery(t *testing.T) {
	c := NewCoordinator(DefaultConfig(), nil, nil, nil, nil)
	got := c.Expand(context.Background(), "   ")
	if got.Original != "" {
		t.Fatalf("expected empty original, got %q", got.Original)
	}
}

func TestTruncateRunes(t *testing.T) {
	if got := truncateRunes("hello世界", 6); got != "hello世..." {
		t.Fatalf("expected hello世..., got %s", got)
	}
	if got := truncateRunes("abc", 10); got != "abc" {
		t.Fatalf("expected abc, got %s", got)
	}
}
