package rerank

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/tmc/langchaingo/schema"
)

// Reranker 对候选文档按与 query 的相关度进行重排。
type Reranker interface {
	Rerank(ctx context.Context, query string, docs []schema.Document, topN int) ([]schema.Document, error)
}

// DashScope 通过阿里云百炼 DashScope 的 qwen3-rerank 云端重排模型，
// 对候选文档进行 cross-encoder 相关性打分并排序。
type DashScope struct {
	baseURL string
	apiKey  string
	model   string
	hc      *http.Client
}

// NewDashScope 创建一个基于阿里云百炼 DashScope rerank 接口的 reranker。
// baseURL 形如 https://dashscope.aliyuncs.com/compatible-api/v1/reranks。
func NewDashScope(baseURL, apiKey, model string) *DashScope {
	return &DashScope{
		baseURL: strings.TrimRight(baseURL, "/"),
		apiKey:  apiKey,
		model:   model,
		hc:      &http.Client{Timeout: 30 * time.Second},
	}
}

type rerankRequest struct {
	Model     string   `json:"model"`
	Query     string   `json:"query"`
	Documents []string `json:"documents"`
	TopN      int      `json:"top_n"`
}

type rerankResponse struct {
	Results []struct {
		Index          int     `json:"index"`
		RelevanceScore float64 `json:"relevance_score"`
	} `json:"results"`
}

// Rerank 一次性批量对候选文档打分，返回按相关度降序排列的前 topN 个文档。
func (d *DashScope) Rerank(ctx context.Context, query string, docs []schema.Document, topN int) ([]schema.Document, error) {
	if d.baseURL == "" || d.apiKey == "" || d.model == "" {
		return nil, fmt.Errorf("rerank: empty base_url, api_key or model")
	}
	if len(docs) == 0 {
		return nil, nil
	}
	if topN <= 0 || topN > len(docs) {
		topN = len(docs)
	}

	texts := make([]string, len(docs))
	for i, doc := range docs {
		texts[i] = doc.PageContent
	}

	payload, err := json.Marshal(rerankRequest{
		Model:     d.model,
		Query:     query,
		Documents: texts,
		TopN:      topN,
	})
	if err != nil {
		return nil, fmt.Errorf("rerank: marshal request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, d.baseURL, bytes.NewReader(payload))
	if err != nil {
		return nil, fmt.Errorf("rerank: build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+d.apiKey)

	resp, err := d.hc.Do(req)
	if err != nil {
		return nil, fmt.Errorf("rerank: do request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return nil, fmt.Errorf("rerank: unexpected status %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}

	var rr rerankResponse
	if err := json.NewDecoder(resp.Body).Decode(&rr); err != nil {
		return nil, fmt.Errorf("rerank: decode response: %w", err)
	}

	out := make([]schema.Document, 0, len(rr.Results))
	for _, r := range rr.Results {
		if r.Index < 0 || r.Index >= len(docs) {
			continue
		}
		src := docs[r.Index]
		out = append(out, schema.Document{
			PageContent: src.PageContent,
			Score:       float32(r.RelevanceScore),
			Metadata:    src.Metadata,
		})
	}
	return out, nil
}
