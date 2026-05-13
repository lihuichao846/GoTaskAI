package api

import (
	"net/http"
	"strings"

	"gotaskai/internal/config"
	"gotaskai/internal/pkg/kag"
	"gotaskai/internal/pkg/llm"

	"github.com/gin-gonic/gin"
	"github.com/redis/go-redis/v9"
	"github.com/tmc/langchaingo/embeddings"
	"github.com/tmc/langchaingo/llms/ollama"
	"github.com/tmc/langchaingo/schema"
	"github.com/tmc/langchaingo/textsplitter"
	"github.com/tmc/langchaingo/vectorstores/redisvector"
)

type RAGHandler struct {
	rdb        *redis.Client
	llmClient  *llm.Client // 保留以兼容其他可能的方法
	kagManager *kag.KAGManager
}

func NewRAGHandler(rdb *redis.Client, kagManager *kag.KAGManager) *RAGHandler {
	return &RAGHandler{
		rdb:        rdb,
		llmClient:  llm.NewClient(),
		kagManager: kagManager,
	}
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

	// 1. 初始化 Ollama Embedder
	llm, err := ollama.New(
		ollama.WithServerURL("http://localhost:11434"),
		ollama.WithModel("bge-m3"),
	)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "初始化模型失败", "details": err.Error()})
		return
	}
	embedder, err := embeddings.NewEmbedder(llm)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "初始化 Embedder 失败", "details": err.Error()})
		return
	}

	// 2. 包装文档内容
	doc := schema.Document{
		PageContent: req.Content,
		Metadata: map[string]any{
			"source": "web_upload",
		},
	}

	// 3. 智能分块
	splitter := textsplitter.NewRecursiveCharacter()
	splitter.ChunkSize = 500
	splitter.ChunkOverlap = 50

	chunks, err := textsplitter.SplitDocuments(splitter, []schema.Document{doc})
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "文档切分失败", "details": err.Error()})
		return
	}

	// 4. 初始化 Redis 向量存储连接
	redisURL := config.AppConfig.Redis.Addr
	if !strings.HasPrefix(redisURL, "redis://") {
		redisURL = "redis://" + redisURL
	}

	store, err := redisvector.New(
		ctx,
		redisvector.WithConnectionURL(redisURL),
		redisvector.WithIndexName("idx:pangu_v2", true),
		redisvector.WithEmbedder(embedder),
	)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "初始化 Redis 向量库失败", "details": err.Error()})
		return
	}

	// 5. 向量化并入库
	_, err = store.AddDocuments(ctx, chunks)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "向量数据入库失败", "details": err.Error()})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"message": "知识库构建成功",
		"total":   len(chunks),
		"success": len(chunks), // LangChainGo 的 AddDocuments 要么全成功要么报错
	})
}
