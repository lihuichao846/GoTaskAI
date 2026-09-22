package api

import (
	"errors"
	"net/http"
	"strconv"
	"strings"
	"unicode/utf8"

	"gotaskai/internal/config"
	"gotaskai/internal/model"
	"gotaskai/internal/queue"

	"github.com/gin-gonic/gin"
)

// MemoryHandler 处理用户向长期记忆的增删查改。
//
// 隐私约束：所有操作都以当前登录用户为作用域（c.GetUint("userID")），
// 且删除【不受 memory.write_enabled 约束】——用户的删除权不能被运维开关挡住。
type MemoryHandler struct {
	manager *queue.TaskManager
}

func NewMemoryHandler(manager *queue.TaskManager) *MemoryHandler {
	return &MemoryHandler{manager: manager}
}

// MemoryRequest 定义创建/更新一条记忆的请求体。
type MemoryRequest struct {
	Content string `json:"content" binding:"required"`
	// MemKey 为归一化的「主体::谓词」（如 preference::answer_style），用于冲突消解。
	// 留空表示"这条记忆不参与取代判定"，可与其它记忆共存。
	MemKey string `json:"mem_key"`
	// Type 取 semantic / preference / episodic / procedural，默认 semantic。
	Type string `json:"type"`
	// AgentID 留空表示"该用户全局"（在任意 Agent 的对话中都可召回）。
	AgentID    string  `json:"agent_id"`
	Confidence float64 `json:"confidence"`
	// 来源溯源（可选）：让每条记忆都能回答"从哪儿来"。
	SourceConversationID string `json:"source_conversation_id"`
	SourceTaskID         string `json:"source_task_id"`
}

// CreateMemory 处理 POST /api/memories。
func (h *MemoryHandler) CreateMemory(c *gin.Context) {
	mc := config.AppConfig.Memory
	if !mc.WriteEnabled {
		c.JSON(http.StatusForbidden, gin.H{"error": "记忆写入未开启（memory.write_enabled=false）"})
		return
	}

	var req MemoryRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	content, memType, ok := validateMemoryContent(c, req)
	if !ok {
		return
	}

	mem := &model.UserMemory{
		UserID:               c.GetUint("userID"),
		AgentID:              req.AgentID,
		MemKey:               strings.TrimSpace(req.MemKey),
		Type:                 memType,
		Content:              content,
		Confidence:           req.Confidence,
		Status:               model.MemoryStatusActive,
		SourceConversationID: req.SourceConversationID,
		SourceTaskID:         req.SourceTaskID,
	}

	if err := h.manager.CreateUserMemory(mem, mc.PerUserLimit); err != nil {
		if errors.Is(err, queue.ErrMemoryQuotaExceeded) {
			c.JSON(http.StatusConflict, gin.H{"error": "记忆条数已达上限，请先清理"})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": "写入记忆失败"})
		return
	}

	c.JSON(http.StatusOK, gin.H{"message": "记忆已保存", "memory": mem})
}

// ListMemories 处理 GET /api/memories。
func (h *MemoryHandler) ListMemories(c *gin.Context) {
	agentID := c.Query("agent_id")
	status := c.DefaultQuery("status", model.MemoryStatusActive)
	page, _ := strconv.Atoi(c.DefaultQuery("page", "1"))
	pageSize, _ := strconv.Atoi(c.DefaultQuery("page_size", "20"))
	if page < 1 {
		page = 1
	}
	if pageSize <= 0 || pageSize > 200 {
		pageSize = 20
	}

	list, total := h.manager.ListUserMemories(c.GetUint("userID"), agentID, status, pageSize, (page-1)*pageSize)
	c.JSON(http.StatusOK, gin.H{
		"items":     list,
		"total":     total,
		"page":      page,
		"page_size": pageSize,
	})
}

// UpdateMemory 处理 PUT /api/memories/:id。
func (h *MemoryHandler) UpdateMemory(c *gin.Context) {
	if !config.AppConfig.Memory.WriteEnabled {
		c.JSON(http.StatusForbidden, gin.H{"error": "记忆写入未开启（memory.write_enabled=false）"})
		return
	}

	var req MemoryRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	content, _, ok := validateMemoryContent(c, req)
	if !ok {
		return
	}

	// 归属校验与更新在同一条 SQL 的 WHERE 里完成（id + user_id），
	// 避免"先查后改"之间的越权窗口。
	ok, err := h.manager.UpdateUserMemoryContent(c.Param("id"), c.GetUint("userID"), content, strings.TrimSpace(req.MemKey))
	if err != nil {
		// 最可能的失败原因是 MemKey 与另一条生效记忆冲突（命中唯一索引 idx_mem_active）。
		// 这种情况需要用户先处理那条记忆，报 409 比笼统的 500 更有指导性。
		c.JSON(http.StatusConflict, gin.H{"error": "更新失败：mem_key 可能与另一条生效记忆冲突"})
		return
	}
	if !ok {
		c.JSON(http.StatusNotFound, gin.H{"error": "记忆不存在"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"message": "记忆已更新"})
}

// DeleteMemory 处理 DELETE /api/memories/:id。
// 刻意不受 write_enabled 约束：删除权是用户对自己数据的处置权。
func (h *MemoryHandler) DeleteMemory(c *gin.Context) {
	rows, err := h.manager.DeleteUserMemory(c.Param("id"), c.GetUint("userID"))
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "删除记忆失败"})
		return
	}
	if rows == 0 {
		c.JSON(http.StatusNotFound, gin.H{"error": "记忆不存在"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"message": "记忆已删除", "deleted": rows})
}

// DeleteAllMemories 处理 DELETE /api/memories（清空当前用户的全部记忆）。
func (h *MemoryHandler) DeleteAllMemories(c *gin.Context) {
	rows, err := h.manager.DeleteAllUserMemories(c.GetUint("userID"))
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "清空记忆失败"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"message": "记忆已清空", "deleted": rows})
}

// validateMemoryContent 校验写入内容与类型（闸门之一：类型白名单 + 长度上限）。
// 校验失败时直接写回响应并返回 ok=false。
func validateMemoryContent(c *gin.Context, req MemoryRequest) (content, memType string, ok bool) {
	content = strings.TrimSpace(req.Content)
	if content == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "记忆内容不能为空"})
		return "", "", false
	}

	maxChars := config.AppConfig.Memory.MaxContentChars
	if maxChars <= 0 {
		maxChars = 512
	}
	if utf8.RuneCountInString(content) > maxChars {
		c.JSON(http.StatusBadRequest, gin.H{"error": "记忆内容超出长度上限（memory.max_content_chars=" + strconv.Itoa(maxChars) + "）"})
		return "", "", false
	}

	memType = strings.TrimSpace(req.Type)
	if memType == "" {
		memType = model.MemoryTypeSemantic
	}
	if !model.IsValidMemoryType(memType) {
		c.JSON(http.StatusBadRequest, gin.H{"error": "不支持的记忆类型：" + memType})
		return "", "", false
	}
	return content, memType, true
}
