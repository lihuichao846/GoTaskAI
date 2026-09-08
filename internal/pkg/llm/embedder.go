package llm

import (
	"context"
	"fmt"

	"gotaskai/internal/config"

	"github.com/sashabaranov/go-openai"
	"github.com/tmc/langchaingo/embeddings"
)

// defaultEmbeddingBaseURL 是阿里百炼 DashScope 的 OpenAI 兼容接口地址。
const defaultEmbeddingBaseURL = "https://dashscope.aliyuncs.com/compatible-mode/v1"

// DashScopeEmbedder 实现 langchaingo embeddings.Embedder 接口，
// 通过阿里百炼 DashScope 的 OpenAI 兼容接口生成文本向量（Qwen3-Embedding 系）。
type DashScopeEmbedder struct {
	client *openai.Client
	model  string
	dim    int
}

// NewEmbedder 根据全局配置构建基于 DashScope 的文本向量化器。
// 优先使用 llm.embedding_* 配置；缺失时回退到 OpenAI 兼容默认值。
func NewEmbedder() (embeddings.Embedder, error) {
	cfg := config.AppConfig.LLM
	dim := config.AppConfig.Milvus.Dim
	return NewDashScopeEmbedder(cfg.EmbeddingAPIKey, cfg.EmbeddingBaseURL, cfg.EmbeddingModel, dim)
}

// NewDashScopeEmbedder 基于 DashScope OpenAI 兼容接口构建文本向量化器。
// dim 为 milvus 中 dense 向量维度，qwen3-embedding 默认 1024。
func NewDashScopeEmbedder(apiKey, baseURL, model string, dim int) (embeddings.Embedder, error) {
	if apiKey == "" {
		return nil, fmt.Errorf("llm: embedding api key is empty")
	}
	if model == "" {
		model = "text-embedding-v4"
	}
	if baseURL == "" {
		baseURL = defaultEmbeddingBaseURL
	}
	if dim <= 0 {
		dim = 1024
	}
	clientConfig := openai.DefaultConfig(apiKey)
	clientConfig.BaseURL = baseURL
	return &DashScopeEmbedder{
		client: openai.NewClientWithConfig(clientConfig),
		model:  model,
		dim:    dim,
	}, nil
}

// EmbedDocuments 为一批文本生成向量，返回顺序与输入一致。
func (e *DashScopeEmbedder) EmbedDocuments(ctx context.Context, texts []string) ([][]float32, error) {
	if len(texts) == 0 {
		return nil, nil
	}
	resp, err := e.client.CreateEmbeddings(ctx, openai.EmbeddingRequest{
		Input:      texts,
		Model:      openai.EmbeddingModel(e.model),
		Dimensions: e.dim,
	})
	if err != nil {
		return nil, fmt.Errorf("llm: dashscope embed documents: %w", err)
	}
	if len(resp.Data) == 0 {
		return nil, fmt.Errorf("llm: dashscope embed documents: empty response")
	}
	result := make([][]float32, len(texts))
	for _, d := range resp.Data {
		if d.Index >= 0 && d.Index < len(result) {
			result[d.Index] = d.Embedding
		}
	}
	return result, nil
}

// EmbedQuery 为单个文本（查询）生成向量。
func (e *DashScopeEmbedder) EmbedQuery(ctx context.Context, text string) ([]float32, error) {
	resp, err := e.client.CreateEmbeddings(ctx, openai.EmbeddingRequest{
		Input:      []string{text},
		Model:      openai.EmbeddingModel(e.model),
		Dimensions: e.dim,
	})
	if err != nil {
		return nil, fmt.Errorf("llm: dashscope embed query: %w", err)
	}
	if len(resp.Data) == 0 {
		return nil, fmt.Errorf("llm: dashscope embed query: empty response")
	}
	return resp.Data[0].Embedding, nil
}
