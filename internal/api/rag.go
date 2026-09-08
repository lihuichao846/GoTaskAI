package api

import (
	"context"
	"fmt"
	"log"
	"net/http"

	"gotaskai/internal/config"
	"gotaskai/internal/pkg/kag"
	"gotaskai/internal/pkg/llm"
	"gotaskai/internal/pkg/ragstore"

	"github.com/gin-gonic/gin"
	"github.com/redis/go-redis/v9"
	"github.com/tmc/langchaingo/embeddings"
	"github.com/tmc/langchaingo/schema"
	"github.com/tmc/langchaingo/textsplitter"
)

type RAGHandler struct {
	rdb        *redis.Client
	llmClient  *llm.Client // 保留以兼容其他可能的方法
	kagManager *kag.KAGManager
	ragStore   *ragstore.Store
}

func NewRAGHandler(rdb *redis.Client, kagManager *kag.KAGManager) *RAGHandler {
	h := &RAGHandler{
		rdb:        rdb,
		llmClient:  llm.NewClient(),
		kagManager: kagManager,
	}

	mc := config.AppConfig.Milvus
	if mc.Host == "" {
		return h
	}
	emb, err := newEmbedder()
	if err != nil {
		log.Printf("Failed to init ollama embedder for RAG handler: %v", err)
		return h
	}
	store, serr := ragstore.New(context.Background(), ragstore.Config{
		Address:        fmt.Sprintf("%s:%d", mc.Host, mc.Port),
		CollectionName: mc.CollectionName,
		Dim:            mc.Dim,
		Embedder:       emb,
	})
	if serr != nil {
		log.Printf("Failed to init Milvus ragstore for RAG handler: %v", serr)
		return h
	}
	h.ragStore = store
	return h
}

// newEmbedder 构建基于阿里百炼 DashScope（Qwen3-Embedding）的文本嵌入器。
func newEmbedder() (embeddings.Embedder, error) {
	return llm.NewEmbedder()
}

// IngestRequest 定义上传知识库文档的请求体
type IngestRequest struct {
	Content string `json:"content" binding:"required"`
}

// KAGIngest 处理知识图谱录入请求
func (h *RAGHandler) KAGIngest(c *gin.Context) {
	var req IngestRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "无效的请求格式", "details": err.Error()})
		return
	}

	if h.kagManager == nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "KAG 功能未启用（未连接 Neo4j）"})
		return
	}

	ctx := c.Request.Context()

	// 1. 调用大模型抽取实体关系三元组
	relations, err := h.kagManager.ExtractKnowledge(ctx, req.Content)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "大模型知识抽取失败", "details": err.Error()})
		return
	}

	if len(relations) == 0 {
		c.JSON(http.StatusOK, gin.H{"message": "未提取到有效的实体关系", "total": 0})
		return
	}

	// 2. 将抽取出的三元组入库到 Neo4j
	if err := h.kagManager.IngestGraph(ctx, relations); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Neo4j 数据入库失败", "details": err.Error()})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"message": "KAG 知识图谱构建成功",
		"total":   len(relations),
		"data":    relations,
	})
}
func (h *RAGHandler) Ingest(c *gin.Context) {
	var req IngestRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "无效的请求格式", "details": err.Error()})
		return
	}

	ctx := c.Request.Context()

	if h.ragStore == nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "知识库未初始化（Milvus 未连接）"})
		return
	}

	// 1. 包装文档内容
	doc := schema.Document{
		PageContent: req.Content,
		Metadata: map[string]any{
			"source": "web_upload",
		},
	}

	// 2. 智能分块
	splitter := textsplitter.NewRecursiveCharacter()
	splitter.ChunkSize = 500
	splitter.ChunkOverlap = 50

	chunks, err := textsplitter.SplitDocuments(splitter, []schema.Document{doc})
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "文档切分失败", "details": err.Error()})
		return
	}

	// 为每个 chunk 打上其在原文档中的有序位置序号（chunk_index），
	// 与 Worker 侧 handleKBBuild 保持一致，供运行期窗口扩展（B1 主路径）。
	for i := range chunks {
		if chunks[i].Metadata == nil {
			chunks[i].Metadata = map[string]any{}
		}
		chunks[i].Metadata["chunk_index"] = i
	}

	// 3. 向量化并入库（dense + sparse 双路写入）
	n, err := h.ragStore.AddDocuments(ctx, chunks)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "向量数据入库失败", "details": err.Error()})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"message": "知识库构建成功",
		"total":   len(chunks),
		"success": n,
	})
}
