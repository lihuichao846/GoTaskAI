package ragstore

import (
	"testing"

	"github.com/tmc/langchaingo/schema"
)

func TestMetaInt64(t *testing.T) {
	doc := schema.Document{Metadata: map[string]any{
		"chunk_index": 3,        // int（API / worker 侧 Split 注入的是 int）
		"as_int64":    int64(7), // Milvus 查询返回的是 int64
		"as_float":    3.0,      // 容忍意外浮点
	}}
	if got := metaInt64(doc, "chunk_index"); got != 3 {
		t.Fatalf("int chunk_index: want 3, got %d", got)
	}
	if got := metaInt64(doc, "as_int64"); got != 7 {
		t.Fatalf("int64 chunk_index: want 7, got %d", got)
	}
	if got := metaInt64(doc, "as_float"); got != 3 {
		t.Fatalf("float chunk_index: want 3, got %d", got)
	}
	if got := metaInt64(doc, "missing"); got != 0 {
		t.Fatalf("missing chunk_index: want 0 sentinel, got %d", got)
	}
	if got := metaInt64(schema.Document{}, "chunk_index"); got != 0 {
		t.Fatalf("nil metadata: want 0 sentinel, got %d", got)
	}
}

func TestFilterExpr(t *testing.T) {
	s := &Store{}
	if got := s.filterExpr("kb123"); got != `kb_id == "kb123"` {
		t.Fatalf("filter expr with kbID mismatch: %q", got)
	}
	// 空 kbID 落到 defaultKBID。
	if got := s.filterExpr(""); got != `kb_id == "default"` {
		t.Fatalf("filter expr default kbID mismatch: %q", got)
	}
}

func TestEscapeExpr(t *testing.T) {
	// 双引号与反斜杠需转义，避免注入或语法错误。
	if got := escapeExpr(`a"b\c`); got != `a\"b\\c` {
		t.Fatalf("escape expr mismatch: %q", got)
	}
}

func TestTokenPosDeterministic(t *testing.T) {
	// 同一词元的哈希位置必须稳定，保证索引与查询一致。
	if tokenPos("hello") != tokenPos("hello") {
		t.Fatal("tokenPos not deterministic for same token")
	}
	// 不同词元大概率不同位置（哈希离散）。
	if tokenPos("hello") == tokenPos("world") {
		t.Log("note: hash collision between hello/world (acceptable)")
	}
}

func TestBM25TFWeight(t *testing.T) {
	// 饱和行为：tf 越大权重越高，但存在边际递减（k1 项）。
	w1 := bm25TFWeight(1, 10, 10)
	w5 := bm25TFWeight(5, 10, 10)
	if w5 <= w1 {
		t.Fatalf("tf=5 weight should exceed tf=1: w1=%v w5=%v", w1, w5)
	}
	// avgDL / dl 为 0 时不应 panic（内部兜底为 1）。
	if got := bm25TFWeight(3, 0, 0); got <= 0 {
		t.Fatalf("expected positive weight on zero length guards, got %v", got)
	}
}
