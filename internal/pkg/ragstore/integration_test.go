package ragstore

import (
	"context"
	"fmt"
	"testing"
	"time"

	milvusclient "github.com/milvus-io/milvus-sdk-go/v2/client"
	"github.com/tmc/langchaingo/schema"
)

// fakeEmbedder 返回确定性的固定维度向量，用于在无真实 embedding API 的环境下
// 集成验证 Milvus 的 chunk_index 往返（写入 / 查询 / 检索输出）。dim 与 Config.Dim 必须一致。
type fakeEmbedder struct{ dim int }

func (f *fakeEmbedder) EmbedDocuments(_ context.Context, texts []string) ([][]float32, error) {
	out := make([][]float32, len(texts))
	for i, t := range texts {
		out[i] = f.vec(t)
	}
	return out, nil
}

func (f *fakeEmbedder) EmbedQuery(_ context.Context, text string) ([]float32, error) {
	return f.vec(text), nil
}

func (f *fakeEmbedder) vec(text string) []float32 {
	v := make([]float32, f.dim)
	seed := 0
	for _, r := range text {
		seed += int(r)
	}
	for i := range v {
		v[i] = float32((seed + i) % 7)
	}
	return v
}

// newTestStore 连接本地 Milvus（127.0.0.1:19530）并确保唯一测试 Collection 创建。
// 若 Milvus 不可达则跳过测试（t.Skip）。
func newTestStore(t *testing.T, dim int) *Store {
	t.Helper()
	addr := "127.0.0.1:19530"
	ctx := context.Background()
	// 预检连接，不可达时跳过集成测试。
	probe, err := milvusclient.NewClient(ctx, milvusclient.Config{Address: addr})
	if err != nil {
		t.Skipf("milvus not reachable at %s: %v", addr, err)
	}
	_ = probe.Close()

	coll := fmt.Sprintf("kb_test_chunkidx_%d", time.Now().UnixNano())
	s, err := New(ctx, Config{
		Address:        addr,
		CollectionName: coll,
		Dim:            dim,
		Embedder:       &fakeEmbedder{dim: dim},
	})
	if err != nil {
		t.Fatalf("New store: %v", err)
	}
	t.Cleanup(func() {
		_ = s.DropCollection(ctx)
		_ = s.Close()
	})
	return s
}

// TestStore_ChunkIndexRoundTrip 集成验证 B1 主路径：写入带 chunk_index 的块，
// GetChunksByDocID 按 chunk_index 有序还原，SimilaritySearch 输出 chunk_index 供窗口定位。
func TestStore_ChunkIndexRoundTrip(t *testing.T) {
	const dim = 8
	s := newTestStore(t, dim)
	ctx := context.Background()

	chunks := []schema.Document{
		{PageContent: "chunk-0", Metadata: map[string]any{"kb_id": "kb1", "doc_id": "docA", "chunk_index": int64(0)}},
		{PageContent: "chunk-1", Metadata: map[string]any{"kb_id": "kb1", "doc_id": "docA", "chunk_index": int64(1)}},
		{PageContent: "chunk-2", Metadata: map[string]any{"kb_id": "kb1", "doc_id": "docA", "chunk_index": int64(2)}},
		{PageContent: "other-doc", Metadata: map[string]any{"kb_id": "kb1", "doc_id": "docB", "chunk_index": int64(0)}},
	}

	if n, err := s.AddDocuments(ctx, chunks); err != nil {
		t.Fatalf("AddDocuments: %v", err)
	} else if n != 4 {
		t.Fatalf("expected 4 inserted, got %d", n)
	}

	if !s.hasChunkIndex {
		t.Fatal("new collection should detect chunk_index column")
	}

	// 按 doc_id 还原有序块序列。
	got, err := s.GetChunksByDocID(ctx, "docA")
	if err != nil {
		t.Fatalf("GetChunksByDocID: %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("expected 3 chunks for docA, got %d", len(got))
	}
	for i, g := range got {
		idx, _ := g.Metadata["chunk_index"].(int64)
		if idx != int64(i) {
			t.Fatalf("chunk order broken: pos %d has chunk_index %d", i, idx)
		}
		if g.PageContent != fmt.Sprintf("chunk-%d", i) {
			t.Fatalf("chunk %d mismatch: %q", i, g.PageContent)
		}
	}

	// docB 独立还原，不应混入 docA 的块。
	gotB, err := s.GetChunksByDocID(ctx, "docB")
	if err != nil {
		t.Fatalf("GetChunksByDocID docB: %v", err)
	}
	if len(gotB) != 1 || gotB[0].PageContent != "other-doc" {
		t.Fatalf("expected only docB chunk, got %+v", gotB)
	}
}

// TestStore_SimilaritySearchOutputChunkIndex 集成验证检索结果带出 chunk_index，
// 供上层按命中块做窗口扩展。
func TestStore_SimilaritySearchOutputChunkIndex(t *testing.T) {
	const dim = 8
	s := newTestStore(t, dim)
	ctx := context.Background()

	chunks := []schema.Document{
		{PageContent: "Alice 是", Metadata: map[string]any{"kb_id": "kb1", "doc_id": "docA", "chunk_index": int64(0)}},
		{PageContent: "公司 CEO", Metadata: map[string]any{"kb_id": "kb1", "doc_id": "docA", "chunk_index": int64(1)}},
	}
	if _, err := s.AddDocuments(ctx, chunks); err != nil {
		t.Fatalf("AddDocuments: %v", err)
	}

	hits, err := s.SimilaritySearch(ctx, "Alice 是 公司 CEO", "kb1", 2)
	if err != nil {
		t.Fatalf("SimilaritySearch: %v", err)
	}
	if len(hits) == 0 {
		t.Fatal("expected at least one hit")
	}
	found := false
	for _, h := range hits {
		if _, ok := h.Metadata["chunk_index"].(int64); ok {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected chunk_index in hit metadata, got %+v", hits)
	}
}

// TestGetChunksByDocID_NoChunkIndexFallback 验证 B2 降级：存量 Collection 缺 chunk_index 时，
// GetChunksByDocID 直接返回空切片，由上层回退 Document.Content 全文兜底（不触达 Milvus）。
func TestGetChunksByDocID_NoChunkIndexFallback(t *testing.T) {
	s := &Store{hasChunkIndex: false}
	docs, err := s.GetChunksByDocID(context.Background(), "docA")
	if err != nil {
		t.Fatalf("expected nil error on fallback, got %v", err)
	}
	if len(docs) != 0 {
		t.Fatalf("expected empty chunks for legacy collection, got %d", len(docs))
	}
}
