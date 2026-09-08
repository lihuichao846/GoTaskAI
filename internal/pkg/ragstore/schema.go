package ragstore

import (
	"context"
	"fmt"

	"github.com/milvus-io/milvus-sdk-go/v2/entity"
)

// 字段名常量，与 schema 和检索输出保持一致。
const (
	fieldChunkID    = "chunk_id"
	fieldKBID       = "kb_id"
	fieldDocID      = "doc_id"
	fieldText       = "text"
	fieldDense      = "dense"
	fieldSparse     = "sparse"
	fieldChunkIndex = "chunk_index"
)

// ensureCollection 创建 Collection、索引并加载。若已存在则直接加载，保证幂等。
func (s *Store) ensureCollection(ctx context.Context) error {
	has, err := s.client.HasCollection(ctx, s.coll)
	if err != nil {
		return fmt.Errorf("ragstore: has collection: %w", err)
	}
	if has {
		return s.loadCollection(ctx)
	}

	schema := entity.NewSchema().
		WithName(s.coll).
		WithAutoID(true).
		WithDescription("GoTaskAI 知识库 chunks（dense + sparse 双路召回）")

	schema.WithField(entity.NewField().
		WithName(fieldChunkID).
		WithDataType(entity.FieldTypeInt64).
		WithIsPrimaryKey(true).
		WithIsAutoID(true))
	schema.WithField(entity.NewField().
		WithName(fieldKBID).
		WithDataType(entity.FieldTypeVarChar).
		WithMaxLength(128))
	schema.WithField(entity.NewField().
		WithName(fieldDocID).
		WithDataType(entity.FieldTypeVarChar).
		WithMaxLength(128))
	schema.WithField(entity.NewField().
		WithName(fieldChunkIndex).
		WithDataType(entity.FieldTypeInt64))
	schema.WithField(entity.NewField().
		WithName(fieldText).
		WithDataType(entity.FieldTypeVarChar).
		WithMaxLength(16384))
	schema.WithField(entity.NewField().
		WithName(fieldDense).
		WithDataType(entity.FieldTypeFloatVector).
		WithDim(int64(s.dim)))
	schema.WithField(entity.NewField().
		WithName(fieldSparse).
		WithDataType(entity.FieldTypeSparseVector))

	if err := s.client.CreateCollection(ctx, schema, 1); err != nil {
		return fmt.Errorf("ragstore: create collection: %w", err)
	}

	// 密集向量：COSINE + HNSW。
	denseIdx, err := entity.NewIndexHNSW(entity.COSINE, 16, 200)
	if err != nil {
		return fmt.Errorf("ragstore: dense index: %w", err)
	}
	if err := s.client.CreateIndex(ctx, s.coll, fieldDense, denseIdx, false); err != nil {
		return fmt.Errorf("ragstore: create dense index: %w", err)
	}

	// 稀疏向量：IP + SPARSE_INVERTED_INDEX，用于 BM25 精确召回。
	sparseIdx, err := entity.NewIndexSparseInverted(entity.IP, 0.0)
	if err != nil {
		return fmt.Errorf("ragstore: sparse index: %w", err)
	}
	if err := s.client.CreateIndex(ctx, s.coll, fieldSparse, sparseIdx, false); err != nil {
		return fmt.Errorf("ragstore: create sparse index: %w", err)
	}

	return s.loadCollection(ctx)
}

// loadCollection 将 Collection 加载进内存供检索。
func (s *Store) loadCollection(ctx context.Context) error {
	if err := s.client.LoadCollection(ctx, s.coll, false); err != nil {
		return fmt.Errorf("ragstore: load collection: %w", err)
	}
	return nil
}
