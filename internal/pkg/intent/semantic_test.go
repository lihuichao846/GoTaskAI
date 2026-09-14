package intent

import (
	"context"
	"errors"
	"testing"
)

// fakeEmbedder 按「问题文本 -> 预置向量」返回，便于确定性测试语义层。
type fakeEmbedder struct {
	byText map[string][]float32
	err    error
	calls  int
}

func (f *fakeEmbedder) CreateEmbedding(_ context.Context, text string) ([]float32, error) {
	f.calls++
	if f.err != nil {
		return nil, f.err
	}
	if v, ok := f.byText[text]; ok {
		return v, nil
	}
	return []float32{0, 0}, nil
}

// testSemantic 直接构造已就绪的语义层，绕开 Warmup 与真实 API。
func testSemantic(fe Embedder, protos []protoVec, allowClose bool) *SemanticClassifier {
	return &SemanticClassifier{
		embedder:   fe,
		high:       0.86,
		margin:     0.05,
		allowClose: allowClose,
		protos:     protos,
		ready:      true,
	}
}

func TestSemanticClassifier_PositiveHit(t *testing.T) {
	q := "我们的接口文档在哪"
	fe := &fakeEmbedder{byText: map[string][]float32{q: {0, 1}}}
	s := testSemantic(fe, []protoVec{
		{label: IntentChat, vec: []float32{1, 0}},
		{label: IntentKnowledge, vec: []float32{0, 1}},
	}, false)

	d, ok := s.Classify(context.Background(), Request{Question: q, HasKB: true, HasKAG: true})
	if !ok {
		t.Fatal("expected semantic hit")
	}
	if !d.NeedRAG || !d.NeedKAG || d.Source != SourceSemantic {
		t.Fatalf("unexpected decision: %+v", d)
	}
}

func TestSemanticClassifier_ToolLabelNotTrusted(t *testing.T) {
	q := "现在几点"
	fe := &fakeEmbedder{byText: map[string][]float32{q: {0, 0, 1}}}
	s := testSemantic(fe, []protoVec{
		{label: IntentChat, vec: []float32{1, 0, 0}},
		{label: IntentTool, vec: []float32{0, 0, 1}},
	}, true)

	if _, ok := s.Classify(context.Background(), Request{Question: q, HasKB: true}); ok {
		t.Fatal("tool label must not be trusted by semantic layer")
	}
}

func TestSemanticClassifier_CloseRequiresAllowClose(t *testing.T) {
	q := "你好"
	fe := &fakeEmbedder{byText: map[string][]float32{q: {1, 0}}}
	protos := []protoVec{
		{label: IntentChat, vec: []float32{1, 0}},
		{label: IntentKnowledge, vec: []float32{0, 1}},
	}

	// 默认不允许关闭检索：chat 命中也不采信。
	s := testSemantic(fe, protos, false)
	if _, ok := s.Classify(context.Background(), Request{Question: q, HasKB: true}); ok {
		t.Fatal("chat label must not close retrieval when allowClose=false")
	}

	// 显式开启后才采信。
	sClose := testSemantic(fe, protos, true)
	d, ok := sClose.Classify(context.Background(), Request{Question: q, HasKB: true})
	if !ok || d.NeedRAG || d.NeedKAG {
		t.Fatalf("expected close decision when allowClose=true, got ok=%v d=%+v", ok, d)
	}
}

func TestSemanticClassifier_LowConfidenceMiss(t *testing.T) {
	q := "随便问一句"
	// 与任一原型余弦均为 1/sqrt(2) ≈ 0.707 < 0.86。
	fe := &fakeEmbedder{byText: map[string][]float32{q: {1, 1}}}
	s := testSemantic(fe, []protoVec{
		{label: IntentChat, vec: []float32{1, 0}},
		{label: IntentKnowledge, vec: []float32{0, 1}},
	}, false)

	if _, ok := s.Classify(context.Background(), Request{Question: q, HasKB: true}); ok {
		t.Fatal("expected miss when similarity below threshold")
	}
}

func TestSemanticClassifier_MarginMiss(t *testing.T) {
	q := "边界问题"
	fe := &fakeEmbedder{byText: map[string][]float32{q: {1, 0}}}
	// top1(chat)=1.0，异类 top2(knowledge)≈0.995，间隔 < 0.05 → 判为不确定。
	s := testSemantic(fe, []protoVec{
		{label: IntentChat, vec: []float32{1, 0}},
		{label: IntentKnowledge, vec: []float32{0.995, 0.0999}},
	}, false)

	if _, ok := s.Classify(context.Background(), Request{Question: q, HasKB: true}); ok {
		t.Fatal("expected miss when class margin too small")
	}
}

func TestSemanticClassifier_EmbeddingFailureDegrades(t *testing.T) {
	fe := &fakeEmbedder{err: errors.New("boom")}
	s := testSemantic(fe, []protoVec{
		{label: IntentKnowledge, vec: []float32{0, 1}},
	}, false)

	if _, ok := s.Classify(context.Background(), Request{Question: "任意", HasKB: true}); ok {
		t.Fatal("expected miss (degrade) when embedding fails")
	}
}

func TestSemanticClassifier_NotReadyMiss(t *testing.T) {
	s := &SemanticClassifier{embedder: &fakeEmbedder{}, high: 0.86, margin: 0.05}
	if _, ok := s.Classify(context.Background(), Request{Question: "你好"}); ok {
		t.Fatal("expected miss when semantic layer not warmed up")
	}
}

func TestSemanticClassifier_WarmupAndFailure(t *testing.T) {
	// Warmup 成功：就绪并可按原型判定。
	fe := &fakeEmbedder{byText: map[string][]float32{}}
	for _, samples := range prototypes {
		for _, text := range samples {
			fe.byText[text] = []float32{1, 0, 0, 0}
		}
	}
	s := NewSemanticClassifier(fe, 0, 0, false)
	if err := s.Warmup(context.Background()); err != nil {
		t.Fatalf("warmup should succeed: %v", err)
	}
	if !s.Ready() {
		t.Fatal("expected ready after warmup")
	}

	// Warmup 失败：保持未就绪（等效禁用）。
	fail := NewSemanticClassifier(&fakeEmbedder{err: errors.New("api down")}, 0, 0, false)
	if err := fail.Warmup(context.Background()); err == nil {
		t.Fatal("warmup should fail when embedder errors")
	}
	if fail.Ready() {
		t.Fatal("expected not ready after failed warmup")
	}
}

func TestCosine_DimensionMismatch(t *testing.T) {
	if got := cosine([]float32{1, 2}, []float32{1, 2, 3}); got != 0 {
		t.Fatalf("expected 0 for dimension mismatch, got %v", got)
	}
	if got := cosine(nil, nil); got != 0 {
		t.Fatalf("expected 0 for empty vectors, got %v", got)
	}
}
