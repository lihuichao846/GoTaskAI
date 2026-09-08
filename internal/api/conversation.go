package api

import (
	"net/http"
	"strings"
	"time"

	"gotaskai/internal/model"
	"gotaskai/internal/queue"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

// ConversationHandler 处理会话的增删查。
type ConversationHandler struct {
	manager *queue.TaskManager
}

func NewConversationHandler(manager *queue.TaskManager) *ConversationHandler {
	return &ConversationHandler{manager: manager}
}

// CreateConversationRequest 定义创建会话的请求体。
type CreateConversationRequest struct {
	AgentID string `json:"agent_id"`
	Title   string `json:"title"`
}

// resolveConversationID 查找或创建会话并返回其 ID，供任务提交时写入 ConversationID。
func resolveConversationID(manager *queue.TaskManager, userID uint, agentID, sessionID string) string {
	conv, err := manager.GetOrCreateConversation(userID, agentID, sessionID)
	if err != nil {
		return ""
	}
	return conv.ID
}

// generateTitle 从消息内容生成会话标题（截取前 30 个字符，去除换行）。
func generateTitle(payload string) string {
	payload = strings.TrimSpace(payload)
	if payload == "" {
		return ""
	}
	payload = strings.NewReplacer("\n", " ", "\r", " ", "\t", " ").Replace(payload)
	runes := []rune(payload)
	const maxLen = 30
	if len(runes) > maxLen {
		return string(runes[:maxLen]) + "..."
	}
	return payload
}

// ensureConversationTitle 当会话标题为空时，用消息内容自动生成标题。
func ensureConversationTitle(manager *queue.TaskManager, conversationID, payload string) {
	if conversationID == "" {
		return
	}
	_ = manager.EnsureConversationTitle(conversationID, generateTitle(payload))
}

// CreateConversation 处理 POST /api/conversations。
func (h *ConversationHandler) CreateConversation(c *gin.Context) {
	var req CreateConversationRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	conv := &model.Conversation{
		ID:        uuid.New().String(),
		UserID:    c.GetUint("userID"),
		AgentID:   req.AgentID,
		Title:     req.Title,
		CreatedAt: time.Now(),
		UpdatedAt: time.Now(),
	}
	if err := h.manager.CreateConversation(conv); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "会话创建失败"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"message": "会话创建成功", "conversation": conv})
}

// ListConversations 处理 GET /api/conversations。
func (h *ConversationHandler) ListConversations(c *gin.Context) {
	convs := h.manager.ListConversations(c.GetUint("userID"))
	c.JSON(http.StatusOK, convs)
}

// GetConversation 处理 GET /api/conversations/:id。
func (h *ConversationHandler) GetConversation(c *gin.Context) {
	conv, ok := h.manager.GetConversation(c.Param("id"))
	if !ok || conv.UserID != c.GetUint("userID") {
		c.JSON(http.StatusNotFound, gin.H{"error": "会话不存在"})
		return
	}
	c.JSON(http.StatusOK, conv)
}

// DeleteConversation 处理 DELETE /api/conversations/:id。
func (h *ConversationHandler) DeleteConversation(c *gin.Context) {
	if err := h.manager.DeleteConversation(c.Param("id"), c.GetUint("userID")); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "会话删除失败"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"message": "会话已删除"})
}
