package ragstore

import (
	"fmt"
	"hash/fnv"
	"strings"

	"github.com/milvus-io/milvus-sdk-go/v2/entity"
	"github.com/tmc/langchaingo/schema"
)

// BM25 参数。
const (
	bm25K1 = 1.2  // 词频饱和系数
	bm25B  = 0.75 // 文档长度归一化系数
)

// tokenPos 使用 FNV-1a 将词元哈希为稀疏向量位置，保证同一词元在索引与查询中位置一致。
func tokenPos(token string) uint32 {
	h := fnv.New32a()
	_, _ = h.Write([]byte(token))
	return h.Sum32()
}

// buildDocumentSparse 依据 BM25 词频饱和公式，为每个文档生成稀疏向量。
// avgDL 为当前批次平均词长，用于文档长度归一化。
func (s *Store) buildDocumentSparse(tokenLists [][]string, avgDL float64) []entity.SparseEmbedding {
	vecs := make([]entity.SparseEmbedding, len(tokenLists))
	for i, tokens := range tokenLists {
		posW := make(map[uint32]float64)
		tfMap := make(map[string]int, len(tokens))
		for _, tok := range tokens {
			tfMap[tok]++
		}
		dl := float64(len(tokens))
		for tok, tf := range tfMap {
			w := bm25TFWeight(float64(tf), dl, avgDL)
			posW[tokenPos(tok)] += w
		}
		vecs[i] = newSparseEmbedding(posW)
	}
	return vecs
}

// buildQuerySparse 为查询生成稀疏向量，查询端每个词元等权（权重 1.0）。
// 结合 Milvus SPARSE_INVERTED_INDEX 的内积检索实现 BM25 精确召回。
func (s *Store) buildQuerySparse(tokens []string) ([]uint32, []float32) {
	posW := make(map[uint32]float32)
	for _, tok := range tokens {
		posW[tokenPos(tok)] += 1.0
	}
	positions := make([]uint32, 0, len(posW))
	values := make([]float32, 0, len(posW))
	for pos, w := range posW {
		positions = append(positions, pos)
		values = append(values, w)
	}
	return positions, values
}

// bm25TFWeight 计算 BM25 的词频部分：tf*(k1+1)/(tf+k1*(1-b+b*dl/avgDL))。
func bm25TFWeight(tf, dl, avgDL float64) float64 {
	if tf <= 0 {
		return 0
	}
	if avgDL <= 0 {
		avgDL = 1
	}
	if dl <= 0 {
		dl = 1
	}
	denom := tf + bm25K1*(1-bm25B+bm25B*dl/avgDL)
	return tf * (bm25K1 + 1) / denom
}

// newSparseEmbedding 将 position->weight 映射转换为 Milvus 稀疏向量。
func newSparseEmbedding(posW map[uint32]float64) entity.SparseEmbedding {
	positions := make([]uint32, 0, len(posW))
	values := make([]float32, 0, len(posW))
	for pos, w := range posW {
		positions = append(positions, pos)
		values = append(values, float32(w))
	}
	se, err := entity.NewSliceSparseEmbedding(positions, values)
	if err != nil {
		// NewSliceSparseEmbedding 只在长度不匹配时报错，此处长度必然一致。
		panic(fmt.Sprintf("ragstore: build sparse embedding: %v", err))
	}
	return se
}

// filterExpr 依据 kbID 生成布尔过滤表达式；空值落到 defaultKBID。
func (s *Store) filterExpr(kbID string) string {
	if kbID == "" {
		kbID = defaultKBID
	}
	return fmt.Sprintf(`%s == "%s"`, fieldKBID, escapeExpr(kbID))
}

// escapeExpr 转义表达式字符串中的引号与反斜杠，避免注入或语法错误。
func escapeExpr(s string) string {
	s = strings.ReplaceAll(s, `\`, `\\`)
	return strings.ReplaceAll(s, `"`, `\"`)
}

// metaString 从文档元数据中读取字符串值。
func metaString(d schema.Document, key string) string {
	if d.Metadata == nil {
		return ""
	}
	v, ok := d.Metadata[key]
	if !ok {
		return ""
	}
	if s, ok := v.(string); ok {
		return s
	}
	return fmt.Sprint(v)
}

// metaInt64 从文档元数据中读取整数值；缺省或非整数返回 0（哨兵：未设置 chunk_index）。
func metaInt64(d schema.Document, key string) int64 {
	if d.Metadata == nil {
		return 0
	}
	v, ok := d.Metadata[key]
	if !ok {
		return 0
	}
	switch n := v.(type) {
	case int:
		return int64(n)
	case int64:
		return n
	case float64:
		return int64(n)
	case float32:
		return int64(n)
	}
	return 0
}
