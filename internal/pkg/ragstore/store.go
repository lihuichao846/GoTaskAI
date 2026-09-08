package ragstore

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/go-ego/gse"
	milvusclient "github.com/milvus-io/milvus-sdk-go/v2/client"
	"github.com/milvus-io/milvus-sdk-go/v2/entity"
	"github.com/tmc/langchaingo/embeddings"
	"github.com/tmc/langchaingo/schema"
)

// defaultKBID 是未显式指定知识库时的兜底分区标识，用于存放公共/盘古等默认知识。
const defaultKBID = "default"

// Config 为 Milvus 向量存储提供连接与分词参数。
type Config struct {
	Address        string // Milvus gRPC 地址，如 "127.0.0.1:19530"
	CollectionName string // Collection 名称
	Dim            int    // 密集向量维度（由 embedding 模型决定）
	Embedder       embeddings.Embedder
}

// Store 封装 Milvus 的建库、入库与 HybridSearch + RRF 检索。
type Store struct {
	client milvusclient.Client
	coll   string
	dim    int
	embed  embeddings.Embedder
	seg    gse.Segmenter
	// hasChunkIndex 标记当前 Collection 是否已包含 chunk_index 列。
	// 存量 Collection 若在改造前建立则缺该列，需走 B2 降级（无窗口扩展），
	// 新建 Collection（ensureCollection 含新列）则为 true。
	hasChunkIndex bool
}

// New 连接 Milvus 并确保 Collection、索引已创建且已加载。
func New(ctx context.Context, cfg Config) (*Store, error) {
	if cfg.Address == "" || cfg.CollectionName == "" || cfg.Dim <= 0 || cfg.Embedder == nil {
		return nil, fmt.Errorf("ragstore: incomplete config (address/collection/dim/embedder)")
	}

	client, err := milvusclient.NewClient(ctx, milvusclient.Config{Address: cfg.Address})
	if err != nil {
		return nil, fmt.Errorf("ragstore: connect milvus: %w", err)
	}

	seg, err := gse.NewEmbed()
	if err != nil {
		_ = client.Close()
		return nil, fmt.Errorf("ragstore: init gse segmenter: %w", err)
	}

	s := &Store{client: client, coll: cfg.CollectionName, dim: cfg.Dim, embed: cfg.Embedder, seg: seg}
	if err := s.ensureCollection(ctx); err != nil {
		_ = client.Close()
		return nil, err
	}
	// 探测当前 Collection 是否已含 chunk_index 列（存量/新建兼容）。
	if err := s.detectChunkIndex(ctx); err != nil {
		_ = client.Close()
		return nil, err
	}
	return s, nil
}

// detectChunkIndex 用 DescribeCollection 检测是否存在 chunk_index 列。
// 存量 Collection（改造前建立）若缺该列则 hasChunkIndex=false，检索/写入走 B2 降级。
func (s *Store) detectChunkIndex(ctx context.Context) error {
	coll, err := s.client.DescribeCollection(ctx, s.coll)
	if err != nil {
		return fmt.Errorf("ragstore: describe collection: %w", err)
	}
	if coll.Schema == nil {
		return nil
	}
	for _, f := range coll.Schema.Fields {
		if f.Name == fieldChunkIndex {
			s.hasChunkIndex = true
			break
		}
	}
	return nil
}

// Close 关闭 Milvus 连接。
func (s *Store) Close() error {
	return s.client.Close()
}

// DropCollection 删除当前 Collection，用于切换 embedding 模型后的全量重新入库。
func (s *Store) DropCollection(ctx context.Context) error {
	if err := s.client.DropCollection(ctx, s.coll); err != nil {
		return fmt.Errorf("ragstore: drop collection: %w", err)
	}
	return nil
}

// AddDocuments 将切分后的文档分块向量化并写入 Milvus。
// 返回成功写入的 chunk 数量。kb_id / doc_id 从 chunk 元数据读取，缺省落到 defaultKBID。
func (s *Store) AddDocuments(ctx context.Context, chunks []schema.Document) (int, error) {
	if len(chunks) == 0 {
		return 0, nil
	}

	texts := make([]string, len(chunks))
	kbIDs := make([]string, len(chunks))
	docIDs := make([]string, len(chunks))
	tokenLists := make([][]string, len(chunks))
	chunkIdx := make([]int64, len(chunks))
	for i, ch := range chunks {
		texts[i] = ch.PageContent
		kbIDs[i] = metaString(ch, "kb_id")
		if kbIDs[i] == "" {
			kbIDs[i] = defaultKBID
		}
		docIDs[i] = metaString(ch, "doc_id")
		tokenLists[i] = s.tokenize(ch.PageContent)
		chunkIdx[i] = metaInt64(ch, "chunk_index")
	}

	// 生成密集向量。
	dense, err := s.embed.EmbedDocuments(ctx, texts)
	if err != nil {
		return 0, fmt.Errorf("ragstore: embed documents: %w", err)
	}

	// 计算平均词长，用于 BM25 文档长度归一化。
	avgDL := 0.0
	for _, toks := range tokenLists {
		avgDL += float64(len(toks))
	}
	if len(tokenLists) > 0 {
		avgDL /= float64(len(tokenLists))
	}

	sparse := s.buildDocumentSparse(tokenLists, avgDL)

	cols := []entity.Column{
		entity.NewColumnVarChar(fieldKBID, kbIDs),
		entity.NewColumnVarChar(fieldDocID, docIDs),
		entity.NewColumnVarChar(fieldText, texts),
		entity.NewColumnFloatVector(fieldDense, s.dim, dense),
		entity.NewColumnSparseVectors(fieldSparse, sparse),
	}
	// 仅当 Collection 含 chunk_index 列时才写入；存量 Collection 缺该列时忽略，走 B2 降级。
	if s.hasChunkIndex {
		cols = append(cols, entity.NewColumnInt64(fieldChunkIndex, chunkIdx))
	}
	if _, err := s.client.Insert(ctx, s.coll, "", cols...); err != nil {
		return 0, fmt.Errorf("ragstore: insert: %w", err)
	}
	// 请求落盘，保证后续检索可见。
	if err := s.client.Flush(ctx, s.coll, false); err != nil {
		return 0, fmt.Errorf("ragstore: flush: %w", err)
	}

	return len(chunks), nil
}

// SimilaritySearch 执行 Milvus 双路召回（dense COSINE + sparse BM25）+ RRF 融合。
// kbID 为空时仅检索 defaultKBID 下的公共知识。
func (s *Store) SimilaritySearch(ctx context.Context, query, kbID string, topK int) ([]schema.Document, error) {
	if topK <= 0 {
		topK = 5
	}

	denseVec, err := s.embed.EmbedQuery(ctx, query)
	if err != nil {
		return nil, fmt.Errorf("ragstore: embed query: %w", err)
	}

	positions, values := s.buildQuerySparse(s.tokenize(query))
	sparseVec, err := entity.NewSliceSparseEmbedding(positions, values)
	if err != nil {
		return nil, fmt.Errorf("ragstore: sparse query: %w", err)
	}

	expr := s.filterExpr(kbID)
	denseSP, _ := entity.NewIndexHNSWSearchParam(64)
	sparseSP, _ := entity.NewIndexSparseInvertedSearchParam(0.0)

	// 每路候选集取 2*topK，由 RRF 融合后收敛到 topK。
	routeN := topK * 2
	reqs := []*milvusclient.ANNSearchRequest{
		milvusclient.NewANNSearchRequest("dense", entity.COSINE, expr, []entity.Vector{entity.FloatVector(denseVec)}, denseSP, routeN),
		milvusclient.NewANNSearchRequest("sparse", entity.IP, expr, []entity.Vector{sparseVec}, sparseSP, routeN),
	}

	outputFields := []string{fieldText, fieldKBID, fieldDocID}
	if s.hasChunkIndex {
		outputFields = append(outputFields, fieldChunkIndex)
	}

	results, err := s.client.HybridSearch(ctx, s.coll, nil, topK, outputFields, milvusclient.NewRRFReranker(), reqs)
	if err != nil {
		return nil, fmt.Errorf("ragstore: hybrid search: %w", err)
	}
	if len(results) == 0 {
		return nil, nil
	}

	res := results[0]
	textCol := res.Fields.GetColumn(fieldText)
	kbCol := res.Fields.GetColumn(fieldKBID)
	docCol := res.Fields.GetColumn(fieldDocID)
	idxCol := res.Fields.GetColumn(fieldChunkIndex)

	docs := make([]schema.Document, 0, res.ResultCount)
	for i := 0; i < res.ResultCount; i++ {
		text := ""
		if textCol != nil {
			if tv, err := textCol.Get(i); err == nil {
				text, _ = tv.(string)
			}
		}
		if strings.TrimSpace(text) == "" {
			continue
		}
		meta := map[string]any{}
		if kbCol != nil {
			if v, err := kbCol.Get(i); err == nil {
				if s, ok := v.(string); ok && s != "" {
					meta["kb_id"] = s
				}
			}
		}
		if docCol != nil {
			if v, err := docCol.Get(i); err == nil {
				if s, ok := v.(string); ok && s != "" {
					meta["doc_id"] = s
				}
			}
		}
		if idxCol != nil {
			if v, err := idxCol.Get(i); err == nil {
				if idx, ok := v.(int64); ok {
					meta["chunk_index"] = idx
				}
			}
		}
		docs = append(docs, schema.Document{
			PageContent: text,
			Score:       res.Scores[i],
			Metadata:    meta,
		})
	}
	return docs, nil
}

// GetChunksByDocID 按 doc_id 从 Milvus 取回该文档的全部 chunk，并按 chunk_index 升序返回。
// 用于上下文感知分块 B1 主路径：检索命中块后，据 chunk_index 定位其前后邻居块做窗口扩展。
// 若当前 Collection 已含 chunk_index 列且存在索引未同步（Query 等待落盘），返回有序 chunk 序列。
func (s *Store) GetChunksByDocID(ctx context.Context, docID string) ([]schema.Document, error) {
	if docID == "" {
		return nil, nil
	}
	// 存量 Collection（改造前建立）缺 chunk_index 列时，无法按原文档顺序还原，
	// 返回空切片，由上层走 B2 降级（Document.Content 全文兜底）。
	if !s.hasChunkIndex {
		return nil, nil
	}

	expr := fmt.Sprintf(`%s == "%s"`, fieldDocID, escapeExpr(docID))
	rs, err := s.client.Query(ctx, s.coll, nil, expr, []string{fieldKBID, fieldText, fieldChunkIndex})
	if err != nil {
		return nil, fmt.Errorf("ragstore: query chunks by doc: %w", err)
	}

	kbCol := rs.GetColumn(fieldKBID)
	textCol := rs.GetColumn(fieldText)
	idxCol := rs.GetColumn(fieldChunkIndex)

	n := rs.Len()
	docs := make([]schema.Document, 0, n)
	for i := 0; i < n; i++ {
		text := ""
		if textCol != nil {
			if v, err := textCol.Get(i); err == nil {
				text, _ = v.(string)
			}
		}
		if strings.TrimSpace(text) == "" {
			continue
		}
		meta := map[string]any{"doc_id": docID}
		if kbCol != nil {
			if v, err := kbCol.Get(i); err == nil {
				if s, ok := v.(string); ok && s != "" {
					meta["kb_id"] = s
				}
			}
		}
		if idxCol != nil {
			if v, err := idxCol.Get(i); err == nil {
				if idx, ok := v.(int64); ok {
					meta["chunk_index"] = idx
				}
			}
		}
		docs = append(docs, schema.Document{PageContent: text, Metadata: meta})
	}

	// 按 chunk_index 升序排序，保证窗口扩展上下文连续。
	sort.SliceStable(docs, func(i, j int) bool {
		a, _ := docs[i].Metadata["chunk_index"].(int64)
		b, _ := docs[j].Metadata["chunk_index"].(int64)
		return a < b
	})
	return docs, nil
}
func (s *Store) tokenize(text string) []string {
	raw := s.seg.Cut(text, true)
	out := make([]string, 0, len(raw))
	for _, t := range raw {
		if t = strings.TrimSpace(t); t != "" {
			out = append(out, t)
		}
	}
	return out
}
