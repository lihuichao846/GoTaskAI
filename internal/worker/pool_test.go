package worker

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"gotaskai/internal/config"
	"gotaskai/internal/model"
	"gotaskai/internal/pkg/intent"
	"gotaskai/internal/pkg/llm"
	"gotaskai/internal/pkg/toolregistry"

	"github.com/sashabaranov/go-openai"
	"github.com/tmc/langchaingo/schema"
)

func TestNormalizeScores(t *testing.T) {
	docs := []schema.Document{
		{PageContent: "a", Score: 0},
		{PageContent: "b", Score: 1},
		{PageContent: "c", Score: 0.5},
	}
	normalizeScores(docs)
	if docs[0].Score != 0 || docs[1].Score != 1 || docs[2].Score != 0.5 {
		t.Fatalf("unexpected normalized scores: %v %v %v", docs[0].Score, docs[1].Score, docs[2].Score)
	}
}

func TestNormalizeScoresAllEqual(t *testing.T) {
	docs := []schema.Document{{Score: 0.5}, {Score: 0.5}}
	normalizeScores(docs)
	if docs[0].Score != 0.5 || docs[1].Score != 0.5 {
		t.Fatalf("scores should be unchanged when all equal")
	}
}

func TestTruncateChunk(t *testing.T) {
	if got := truncateChunk("hello", 10); got != "hello" {
		t.Fatalf("expected no truncation, got %q", got)
	}
	if got := truncateChunk("hello世界", 6); got != "hello世..." {
		t.Fatalf("expected rune-aware truncation, got %q", got)
	}
	if got := truncateChunk("abc", 0); got != "abc" {
		t.Fatalf("expected no truncation for max<=0, got %q", got)
	}
}

func TestChunkByScore(t *testing.T) {
	cc := config.ContextConfig{MaxChunkChars: 10, RagScoreThreshold: 0.5}
	if got := chunkByScore(schema.Document{PageContent: "abcdefghijklmnop", Score: 0.9}, cc); got != "abcdefghij..." {
		t.Fatalf("expected full truncation, got %q", got)
	}
	if got := chunkByScore(schema.Document{PageContent: "abcdefghijklmnop", Score: 0.1}, cc); got != "abcde..." {
		t.Fatalf("expected short truncation, got %q", got)
	}

	// 阈值禁用：全部注入全文截断。
	ccNoThreshold := config.ContextConfig{MaxChunkChars: 10}
	if got := chunkByScore(schema.Document{PageContent: "abcdefghijklmnop", Score: 0.1}, ccNoThreshold); got != "abcdefghij..." {
		t.Fatalf("expected full when threshold disabled, got %q", got)
	}

	// 截断禁用：返回原文，不因阈值降级到 1 字符。
	ccNoTrunc := config.ContextConfig{MaxChunkChars: 0, RagScoreThreshold: 0.5}
	if got := chunkByScore(schema.Document{PageContent: "abcdefghijklmnop", Score: 0.1}, ccNoTrunc); got != "abcdefghijklmnop" {
		t.Fatalf("expected raw when truncation disabled, got %q", got)
	}
}

func TestWindowRange(t *testing.T) {
	cases := []struct {
		name           string
		center, window int
		total          int
		lo, hi         int
	}{
		{"middle", 3, 1, 10, 2, 4},
		{"left clip", 0, 1, 10, 0, 1},
		{"right clip", 9, 1, 10, 8, 9},
		{"zero window", 1, 0, 3, 1, 1},
		{"window exceeds head", 0, 5, 3, 0, 2},
		{"window exceeds tail", 2, 5, 3, 0, 2},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			lo, hi := windowRange(c.center, c.window, c.total)
			if lo != c.lo || hi != c.hi {
				t.Fatalf("windowRange(%d,%d,%d) = (%d,%d), want (%d,%d)", c.center, c.window, c.total, lo, hi, c.lo, c.hi)
			}
		})
	}
}

func TestAddWindowChunks_CrossSentence(t *testing.T) {
	// 语义割裂场景：主语在块 0，谓语/宾语在块 1。命中块 1（窗口 1）应补回前块 0，
	// 使注入上下文包含完整三元组（改造前命中块孤立，注入只见"公司 CEO"）。
	ordered := []schema.Document{
		{PageContent: "Alice 是", Metadata: map[string]any{"chunk_index": int64(0)}},
		{PageContent: "公司 CEO", Metadata: map[string]any{"chunk_index": int64(1)}},
		{PageContent: "他负责战略", Metadata: map[string]any{"chunk_index": int64(2)}},
	}
	seen := map[string]bool{}
	expanded := addWindowChunks(ordered, "doc1", 1, 0.9, 1, seen, nil)

	if len(expanded) != 3 {
		t.Fatalf("expected 3 chunks (2 neighbors + hit), got %d", len(expanded))
	}
	// 有序性：块 0 -> 块 1 -> 块 2。
	if idx, _ := expanded[0].Metadata["chunk_index"].(int64); idx != 0 {
		t.Fatalf("expected first chunk index 0, got %d", idx)
	}
	if idx, _ := expanded[1].Metadata["chunk_index"].(int64); idx != 1 {
		t.Fatalf("expected second chunk index 1, got %d", idx)
	}
	joined := ""
	for _, c := range expanded {
		joined += c.PageContent
	}
	if !strings.Contains(joined, "Alice 是") || !strings.Contains(joined, "公司 CEO") {
		t.Fatalf("expected complete triple in windowed context, got %q", joined)
	}
}

func TestAddWindowChunks_Dedup(t *testing.T) {
	// 同一 doc 多个命中块重复扩窗，应通过 seen（doc_id:chunk_index）去重，不重复注入。
	ordered := []schema.Document{
		{PageContent: "c0", Metadata: map[string]any{"chunk_index": int64(0)}},
		{PageContent: "c1", Metadata: map[string]any{"chunk_index": int64(1)}},
		{PageContent: "c2", Metadata: map[string]any{"chunk_index": int64(2)}},
		{PageContent: "c3", Metadata: map[string]any{"chunk_index": int64(3)}},
	}
	seen := map[string]bool{}
	var expanded []schema.Document
	expanded = addWindowChunks(ordered, "doc", 1, 0.9, 1, seen, expanded) // 窗口 [0,2]
	expanded = addWindowChunks(ordered, "doc", 2, 0.8, 1, seen, expanded) // 窗口 [1,3]，1/2 已存在
	if len(expanded) != 4 {
		t.Fatalf("expected 4 unique chunks after dedup, got %d", len(expanded))
	}
}

func TestAddWindowChunks_ScorePropagation(t *testing.T) {
	// 邻居块应沿命中块 Score，保证与命中块同层截断口径。
	ordered := []schema.Document{
		{PageContent: "c0", Metadata: map[string]any{"chunk_index": int64(0)}},
		{PageContent: "c1", Metadata: map[string]any{"chunk_index": int64(1)}},
	}
	seen := map[string]bool{}
	expanded := addWindowChunks(ordered, "doc", 0, 0.9, 1, seen, nil)
	if len(expanded) != 2 {
		t.Fatalf("expected 2 chunks, got %d", len(expanded))
	}
	if expanded[1].Score != 0.9 {
		t.Fatalf("expected neighbor chunk carry hit score 0.9, got %v", expanded[1].Score)
	}
}

func TestAddWindowChunks_NotFound(t *testing.T) {
	// 命中 chunk_index 不在该文档有序序列中（理论不该发生），应不追加任何块。
	ordered := []schema.Document{
		{PageContent: "c0", Metadata: map[string]any{"chunk_index": int64(0)}},
	}
	seen := map[string]bool{}
	expanded := addWindowChunks(ordered, "doc", 5, 0.9, 1, seen, nil)
	if len(expanded) != 0 {
		t.Fatalf("expected no chunks when hit not found, got %d", len(expanded))
	}
}

func TestExpandChunks_WindowDisabled(t *testing.T) {
	// ChunkContextWindow <= 0 时关闭窗口扩展，应返回原始命中块（等价改造前行为）。
	// 该分支在调用 ragStore 之前提前返回，因此可在无 Milvus 环境下纯单测。
	p := &Pool{} // ragStore 为 nil 亦不会走到，因为窗口已关闭优先返回。
	docs := []schema.Document{
		{PageContent: "hit", Metadata: map[string]any{"doc_id": "d1", "chunk_index": int64(2)}},
	}
	cc := config.ContextConfig{ChunkContextWindow: 0}
	out, err := p.expandChunks(context.Background(), docs, cc)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(out) != 1 || out[0].PageContent != "hit" {
		t.Fatalf("expected original heads when window disabled, got %+v", out)
	}
}

func TestWindowRange_EmptyTotal(t *testing.T) {
	// total<=0 时返回空窗口（lo > hi），addWindowChunks 的 for ci:=lo; ci<=hi 循环不执行。
	lo, hi := windowRange(3, 1, 0)
	if lo <= hi {
		t.Fatalf("expected empty window (lo>hi), got lo=%d hi=%d", lo, hi)
	}
}

// ---- 性能基准：覆盖新增的窗口扩展热路径，确保窗口扩展/去重/排序不产生显著开销 ----

// benchDocs 构造 window+1 个有序块（chunk_index 0..window）。
func benchDocs(n int) []schema.Document {
	docs := make([]schema.Document, n)
	for i := 0; i < n; i++ {
		docs[i] = schema.Document{
			PageContent: fmt.Sprintf("chunk content %d: 上下文感知分块窗口扩展基准", i),
			Metadata:    map[string]any{"doc_id": "docA", "chunk_index": int64(i)},
		}
	}
	return docs
}

func BenchmarkWindowRange(b *testing.B) {
	for i := 0; i < b.N; i++ {
		windowRange(5, 1, 11) // 中间命中
		windowRange(0, 1, 11) // 边界裁剪
	}
}

func BenchmarkAddWindowChunks(b *testing.B) {
	docs := benchDocs(21) // chunk_index 0..20
	for i := 0; i < b.N; i++ {
		seen := map[string]bool{}
		expanded := addWindowChunks(docs, "docA", 10, 0.9, 1, seen, nil)
		if len(expanded) != 3 {
			b.Fatalf("expected 3 window chunks, got %d", len(expanded))
		}
	}
}

func BenchmarkAddWindowChunks_Dedup(b *testing.B) {
	docs := benchDocs(100)
	// 多次命中同一窗口区间，验证去重 map 性能。
	for i := 0; i < b.N; i++ {
		seen := map[string]bool{}
		var expanded []schema.Document
		for hit := 5; hit <= 95; hit += 5 {
			expanded = addWindowChunks(docs, "docA", int64(hit), 0.9, 1, seen, expanded)
		}
	}
}

func BenchmarkChunkByScore(b *testing.B) {
	cc := config.ContextConfig{MaxChunkChars: 800, RagScoreThreshold: 0.5}
	d := schema.Document{PageContent: benchDocs(1)[0].PageContent, Score: 0.9}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = chunkByScore(d, cc)
	}
}

// ---- 意图路由接线：验证 Worker 侧的能力组装、凭证透传与保守兜底 ----

// stubIntentRouter 记录调用参数并返回预置决策，用于在无外部依赖下测试路由接线。
type stubIntentRouter struct {
	decision intent.Decision
	calls    int
	lastReq  intent.Request
}

func (s *stubIntentRouter) Route(_ context.Context, req intent.Request, _ *llm.Budget) intent.Decision {
	s.calls++
	s.lastReq = req
	return s.decision
}

// fakeNamedTool 仅用于构造按名称判定的工具集合。
type fakeNamedTool struct{ name string }

func (f fakeNamedTool) Name() string                                            { return f.name }
func (f fakeNamedTool) Description() string                                     { return "" }
func (f fakeNamedTool) ToOpenAITool() openai.Tool                               { return openai.Tool{} }
func (f fakeNamedTool) Execute(context.Context, map[string]any) (string, error) { return "", nil }

func newTestPool(r intent.Router) *Pool {
	// inFlight 必须初始化：routeIntent 会登记/注销路由阶段的取消函数。
	return &Pool{intentRouter: r, inFlight: make(map[string]context.CancelFunc)}
}

func TestRouteIntent_NilRouterIsConservative(t *testing.T) {
	p := newTestPool(nil)
	d := p.routeIntent(context.Background(), model.Task{ID: "t1", Payload: "我们的部署文档在哪"}, nil, nil, "", "", "")
	if d.Source != intent.SourceDisabled {
		t.Fatalf("expected %q when router not configured, got %q", intent.SourceDisabled, d.Source)
	}
	if !d.NeedTools {
		t.Fatal("conservative default should keep tools available")
	}
}

func TestRouteIntent_ChatSkipsRetrieval(t *testing.T) {
	r := &stubIntentRouter{decision: intent.Decision{Intent: intent.IntentChat, Source: intent.SourceRule, Confidence: 0.95}}
	p := newTestPool(r)

	d := p.routeIntent(context.Background(), model.Task{ID: "t2", Payload: "你好"}, nil, nil, "", "", "")
	if r.calls != 1 {
		t.Fatalf("expected router called once, got %d", r.calls)
	}
	if d.NeedRAG || d.NeedKAG || d.Intent != intent.IntentChat {
		t.Fatalf("chat decision must skip retrieval, got %+v", d)
	}
}

func TestRouteIntent_PassesCapabilityAndCredentials(t *testing.T) {
	r := &stubIntentRouter{}
	p := newTestPool(r)

	// ragStore/kagManager 均为 nil：能力标记应为 false（用于上层短路判断）。
	p.routeIntent(context.Background(), model.Task{ID: "t3", Payload: "问题"}, nil, nil, "k", "u", "m")

	if r.lastReq.HasKB || r.lastReq.HasKAG {
		t.Fatalf("expected HasKB/HasKAG false without rag/kag, got %+v", r.lastReq)
	}
	if r.lastReq.APIKey != "k" || r.lastReq.BaseURL != "u" || r.lastReq.Model != "m" {
		t.Fatalf("expected agent credentials forwarded, got %+v", r.lastReq)
	}
}

func TestRouteIntent_PassesInternetFlag(t *testing.T) {
	r := &stubIntentRouter{}
	p := newTestPool(r)

	tools := []toolregistry.Tool{fakeNamedTool{name: "internet_search"}}
	p.routeIntent(context.Background(), model.Task{ID: "t4", Payload: "查一下最新新闻"}, tools, nil, "", "", "")
	if !r.lastReq.HasInternet {
		t.Fatalf("expected HasInternet=true when internet_search bound, got %+v", r.lastReq)
	}

	p.routeIntent(context.Background(), model.Task{ID: "t5", Payload: "查一下最新新闻"}, []toolregistry.Tool{fakeNamedTool{name: "other"}}, nil, "", "", "")
	if r.lastReq.HasInternet {
		t.Fatalf("expected HasInternet=false for other tools, got %+v", r.lastReq)
	}
}
