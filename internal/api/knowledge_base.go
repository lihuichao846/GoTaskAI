package api

import (
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"strings"
	"time"

	"gotaskai/internal/model"
	"gotaskai/internal/queue"

	"github.com/gabriel-vasile/mimetype"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

// KnowledgeBaseHandler 处理知识库的 CRUD 与文档上传。
type KnowledgeBaseHandler struct {
	manager *queue.TaskManager
}

func NewKnowledgeBaseHandler(manager *queue.TaskManager) *KnowledgeBaseHandler {
	return &KnowledgeBaseHandler{manager: manager}
}

// KnowledgeBaseRequest 定义创建知识库的请求体。
type KnowledgeBaseRequest struct {
	Name        string `json:"name" binding:"required"`
	Description string `json:"description"`
	Type        string `json:"type"` // rag / kag / hybrid
}

// CreateKnowledgeBase 处理 POST /api/knowledge-bases。
func (h *KnowledgeBaseHandler) CreateKnowledgeBase(c *gin.Context) {
	var req KnowledgeBaseRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	if req.Type == "" {
		req.Type = "rag"
	}

	kb := &model.KnowledgeBase{
		ID:          uuid.New().String(),
		UserID:      c.GetUint("userID"),
		Name:        req.Name,
		Description: req.Description,
		Type:        req.Type,
		Status:      "pending",
		CreatedAt:   time.Now(),
		UpdatedAt:   time.Now(),
	}

	if err := h.manager.CreateKnowledgeBase(kb); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "知识库创建失败"})
		return
	}

	c.JSON(http.StatusOK, gin.H{"message": "知识库创建成功", "knowledge_base": kb})
}

// ListKnowledgeBases 处理 GET /api/knowledge-bases。
func (h *KnowledgeBaseHandler) ListKnowledgeBases(c *gin.Context) {
	kbs := h.manager.ListKnowledgeBases(c.GetUint("userID"))
	c.JSON(http.StatusOK, kbs)
}

// GetKnowledgeBase 处理 GET /api/knowledge-bases/:id。
func (h *KnowledgeBaseHandler) GetKnowledgeBase(c *gin.Context) {
	kb, ok := h.manager.GetKnowledgeBase(c.Param("id"))
	if !ok {
		c.JSON(http.StatusNotFound, gin.H{"error": "知识库不存在"})
		return
	}
	if kb.UserID != c.GetUint("userID") {
		c.JSON(http.StatusForbidden, gin.H{"error": "无权访问此知识库"})
		return
	}
	c.JSON(http.StatusOK, kb)
}

// DeleteKnowledgeBase 处理 DELETE /api/knowledge-bases/:id。
func (h *KnowledgeBaseHandler) DeleteKnowledgeBase(c *gin.Context) {
	kb, ok := h.manager.GetKnowledgeBase(c.Param("id"))
	if !ok {
		c.JSON(http.StatusNotFound, gin.H{"error": "知识库不存在"})
		return
	}
	if kb.UserID != c.GetUint("userID") {
		c.JSON(http.StatusForbidden, gin.H{"error": "无权删除此知识库"})
		return
	}

	if err := h.manager.RequestKnowledgeBaseDelete(kb.ID); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "知识库删除任务提交失败"})
		return
	}
	c.JSON(http.StatusAccepted, gin.H{"message": "删除已提交，后台清理中"})
}

// AddDocumentRequest 定义上传文档的请求体（application/json 兼容方式）。
type AddDocumentRequest struct {
	Title   string `json:"title"`
	Content string `json:"content" binding:"required"`
}

// maxUploadSize 限制单文件最大上传体积为 10MB。
const maxUploadSize = 10 << 20

// AddDocument 处理 POST /api/knowledge-bases/:id/documents，异步触发构建。
// 支持两种提交方式：
//   - multipart/form-data：直接上传文件，字段名为 file，可选 title；
//   - application/json：{ "title": "...", "content": "..." }（保留兼容）。
func (h *KnowledgeBaseHandler) AddDocument(c *gin.Context) {
	kbID := c.Param("id")
	kb, ok := h.manager.GetKnowledgeBase(kbID)
	if !ok {
		c.JSON(http.StatusNotFound, gin.H{"error": "知识库不存在"})
		return
	}
	if kb.UserID != c.GetUint("userID") {
		c.JSON(http.StatusForbidden, gin.H{"error": "无权操作此知识库"})
		return
	}

	var title, content string
	if strings.HasPrefix(c.GetHeader("Content-Type"), "multipart/form-data") {
		fileHeader, err := c.FormFile("file")
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "请选择要上传的文件"})
			return
		}
		title = c.PostForm("title")
		if title == "" {
			title = fileHeader.Filename
		}
		content, err = readUploadedFile(fileHeader)
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}
	} else {
		var req AddDocumentRequest
		if err := c.ShouldBindJSON(&req); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}
		if req.Content == "" {
			c.JSON(http.StatusBadRequest, gin.H{"error": "文档内容不能为空"})
			return
		}
		title = req.Title
		content = req.Content
	}

	doc := &model.Document{
		ID:        uuid.New().String(),
		KBID:      kbID,
		Title:     title,
		Content:   content,
		Status:    "pending",
		CreatedAt: time.Now(),
	}

	if err := h.manager.CreateDocument(doc); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "文档创建失败"})
		return
	}

	kb.Status = "building"
	kb.UpdatedAt = time.Now()
	_ = h.manager.UpdateKnowledgeBase(kb)

	if err := h.manager.EnqueueKBBuild(kbID, doc.ID); err != nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "构建任务入队失败"})
		return
	}

	c.JSON(http.StatusAccepted, gin.H{"message": "文档已提交，异步构建中", "document_id": doc.ID})
}

// DeleteDocument 处理 DELETE /api/knowledge-bases/:id/documents/:docId，异步删除文档。
// 删除走标记 + 后台三段式清理（Milvus / Neo4j / MySQL），失败可重试，不阻塞请求。
func (h *KnowledgeBaseHandler) DeleteDocument(c *gin.Context) {
	kb, ok := h.manager.GetKnowledgeBase(c.Param("id"))
	if !ok {
		c.JSON(http.StatusNotFound, gin.H{"error": "知识库不存在"})
		return
	}
	if kb.UserID != c.GetUint("userID") {
		c.JSON(http.StatusForbidden, gin.H{"error": "无权操作此知识库"})
		return
	}

	docID := c.Param("docId")
	doc, ok := h.manager.GetDocument(docID)
	if !ok || doc.KBID != kb.ID {
		c.JSON(http.StatusNotFound, gin.H{"error": "文档不存在"})
		return
	}

	if err := h.manager.RequestDocumentDelete(docID); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "文档删除任务提交失败"})
		return
	}
	c.JSON(http.StatusAccepted, gin.H{"message": "删除已提交，后台清理中"})
}

// readUploadedFile 读取上传文件并解码为 UTF-8 文本，仅允许文本类文件。
func readUploadedFile(fh *multipart.FileHeader) (string, error) {
	if fh.Size > maxUploadSize {
		return "", fmt.Errorf("文件过大，最大支持 %dMB", maxUploadSize>>20)
	}
	f, err := fh.Open()
	if err != nil {
		return "", fmt.Errorf("打开文件失败：%w", err)
	}
	defer f.Close()

	data, err := io.ReadAll(f)
	if err != nil {
		return "", fmt.Errorf("读取文件失败：%w", err)
	}

	mime := mimetype.Detect(data).String()
	if !isTextMime(mime) {
		return "", fmt.Errorf("暂不支持 %s 类型文件，请上传文本/代码/Markdown/CSV/JSON 等", mime)
	}

	content := string(data)
	content = strings.TrimPrefix(content, "\ufeff") // 去除 UTF-8 BOM
	return content, nil
}

// isTextMime 判断 MIME 类型是否为可处理的纯文本。
func isTextMime(mime string) bool {
	base := mime
	if idx := strings.Index(mime, ";"); idx >= 0 {
		base = strings.TrimSpace(mime[:idx])
	}
	if strings.HasPrefix(base, "text/") {
		return true
	}
	switch base {
	case "application/json",
		"application/xml",
		"application/x-yaml",
		"application/yaml",
		"application/csv",
		"application/javascript",
		"application/x-httpd-php",
		"application/rtf":
		return true
	}
	return strings.HasSuffix(base, "+json") || strings.HasSuffix(base, "+xml")
}
