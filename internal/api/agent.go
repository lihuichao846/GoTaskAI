package api

import (
	"net/http"
	"time"

	"gotaskai/internal/model"
	"gotaskai/internal/queue"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

// AgentHandler 处理 Agent 的增删改查与运行请求
type AgentHandler struct {
	manager *queue.TaskManager
}

func NewAgentHandler(manager *queue.TaskManager) *AgentHandler {
	return &AgentHandler{manager: manager}
}

// AgentRequest 定义创建/更新 Agent 的请求体
type AgentRequest struct {
	Name         string `json:"name" binding:"required"`
	SystemPrompt string `json:"system_prompt"`
	Model        string `json:"model"`
	APIKey       string `json:"api_key"`
	BaseURL      string `json:"base_url"`
}

// RunAgentRequest 定义运行 Agent 的请求体
type RunAgentRequest struct {
	SessionID string             `json:"session_id"`
	Payload   string             `json:"payload" binding:"required"`
	Priority  model.TaskPriority `json:"priority"`
}

// CreateAgent 处理 POST /api/agents
func (h *AgentHandler) CreateAgent(c *gin.Context) {
	var req AgentRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	agent := &model.Agent{
		ID:           uuid.New().String(),
		UserID:       c.GetUint("userID"),
		Name:         req.Name,
		SystemPrompt: req.SystemPrompt,
		Model:        req.Model,
		APIKey:       req.APIKey,
		BaseURL:      req.BaseURL,
		Status:       "active",
		CreatedAt:    time.Now(),
		UpdatedAt:    time.Now(),
	}

	if err := h.manager.CreateAgent(agent); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "创建 Agent 失败"})
		return
	}

	c.JSON(http.StatusOK, gin.H{"message": "Agent 创建成功", "agent": agent})
}

// ListAgents 处理 GET /api/agents
func (h *AgentHandler) ListAgents(c *gin.Context) {
	agents := h.manager.ListAgents(c.GetUint("userID"))
	c.JSON(http.StatusOK, agents)
}

// GetAgent 处理 GET /api/agents/:id
func (h *AgentHandler) GetAgent(c *gin.Context) {
	id := c.Param("id")
	userID := c.GetUint("userID")

	agent, ok := h.manager.GetAgent(id)
	if !ok || agent.UserID != userID {
		c.JSON(http.StatusNotFound, gin.H{"error": "Agent 不存在"})
		return
	}

	c.JSON(http.StatusOK, agent)
}

// UpdateAgent 处理 PUT /api/agents/:id
func (h *AgentHandler) UpdateAgent(c *gin.Context) {
	id := c.Param("id")
	userID := c.GetUint("userID")

	var req AgentRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	agent, ok := h.manager.GetAgent(id)
	if !ok || agent.UserID != userID {
		c.JSON(http.StatusNotFound, gin.H{"error": "Agent 不存在"})
		return
	}

	agent.Name = req.Name
	agent.SystemPrompt = req.SystemPrompt
	agent.Model = req.Model
	agent.APIKey = req.APIKey
	agent.BaseURL = req.BaseURL
	agent.UpdatedAt = time.Now()

	if err := h.manager.UpdateAgent(agent); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "更新 Agent 失败"})
		return
	}

	c.JSON(http.StatusOK, gin.H{"message": "Agent 更新成功", "agent": agent})
}

// DeleteAgent 处理 DELETE /api/agents/:id
func (h *AgentHandler) DeleteAgent(c *gin.Context) {
	id := c.Param("id")
	userID := c.GetUint("userID")

	agent, ok := h.manager.GetAgent(id)
	if !ok || agent.UserID != userID {
		c.JSON(http.StatusNotFound, gin.H{"error": "Agent 不存在"})
		return
	}

	if err := h.manager.DeleteAgent(id); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "删除 Agent 失败"})
		return
	}

	c.JSON(http.StatusOK, gin.H{"message": "Agent 删除成功"})
}

// RunAgent 处理 POST /api/agents/:id/run，等价于提交一个绑定该 Agent 的任务
func (h *AgentHandler) RunAgent(c *gin.Context) {
	id := c.Param("id")
	userID := c.GetUint("userID")

	agent, ok := h.manager.GetAgent(id)
	if !ok || agent.UserID != userID {
		c.JSON(http.StatusNotFound, gin.H{"error": "Agent 不存在"})
		return
	}

	var req RunAgentRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	if req.Priority == 0 {
		req.Priority = model.PriorityNormal
	}

	// 查找或创建会话，写入 ConversationID（同时保留 SessionID 兼容旧逻辑）。
	conversationID := resolveConversationID(h.manager, userID, id, req.SessionID)
	ensureConversationTitle(h.manager, conversationID, req.Payload)

	task := &model.Task{
		ID:             uuid.New().String(),
		UserID:         userID,
		AgentID:        id,
		SessionID:      req.SessionID,
		ConversationID: conversationID,
		Type:           model.TypeCustom,
		Priority:       req.Priority,
		Payload:        req.Payload,
		Status:         model.StatusPending,
		MaxRetry:       3,
		CreatedAt:      time.Now(),
		UpdatedAt:      time.Now(),
	}

	if err := h.manager.AddTask(task); err != nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": err.Error()})
		return
	}

	c.JSON(http.StatusAccepted, gin.H{"id": task.ID, "status": string(task.Status)})
}

// BindToolsRequest 定义绑定工具的请求体。
type BindToolsRequest struct {
	ToolIDs []string `json:"tool_ids" binding:"required"`
}

// BindKnowledgeRequest 定义绑定知识库的请求体。
type BindKnowledgeRequest struct {
	KnowledgeBaseIDs []string `json:"knowledge_base_ids" binding:"required"`
}

// ownedAgent 校验请求中的 Agent 归属当前用户。
func (s *AgentHandler) ownedAgent(c *gin.Context) (*model.Agent, bool) {
	id := c.Param("id")
	agent, ok := s.manager.GetAgent(id)
	if !ok || agent.UserID != c.GetUint("userID") {
		return nil, false
	}
	return agent, true
}

// BindTools 处理 POST /api/agents/:id/tools。
func (s *AgentHandler) BindTools(c *gin.Context) {
	if _, ok := s.ownedAgent(c); !ok {
		c.JSON(http.StatusNotFound, gin.H{"error": "Agent 不存在"})
		return
	}
	var req BindToolsRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	if err := s.manager.ReplaceAgentTools(c.Param("id"), req.ToolIDs); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "工具绑定失败"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"message": "工具绑定成功"})
}

// ListAgentTools 处理 GET /api/agents/:id/tools。
func (s *AgentHandler) ListAgentTools(c *gin.Context) {
	if _, ok := s.ownedAgent(c); !ok {
		c.JSON(http.StatusNotFound, gin.H{"error": "Agent 不存在"})
		return
	}
	c.JSON(http.StatusOK, s.manager.ListAgentTools(c.Param("id")))
}

// UnbindTool 处理 DELETE /api/agents/:id/tools/:toolId。
func (s *AgentHandler) UnbindTool(c *gin.Context) {
	if _, ok := s.ownedAgent(c); !ok {
		c.JSON(http.StatusNotFound, gin.H{"error": "Agent 不存在"})
		return
	}
	_ = s.manager.UnbindToolFromAgent(c.Param("id"), c.Param("toolId"))
	c.JSON(http.StatusOK, gin.H{"message": "工具解绑成功"})
}

// BindKnowledgeBases 处理 POST /api/agents/:id/knowledge-bases。
func (s *AgentHandler) BindKnowledgeBases(c *gin.Context) {
	if _, ok := s.ownedAgent(c); !ok {
		c.JSON(http.StatusNotFound, gin.H{"error": "Agent 不存在"})
		return
	}
	var req BindKnowledgeRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	if err := s.manager.ReplaceAgentKnowledgeBases(c.Param("id"), req.KnowledgeBaseIDs); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "知识库绑定失败"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"message": "知识库绑定成功"})
}

// ListAgentKnowledgeBases 处理 GET /api/agents/:id/knowledge-bases。
func (s *AgentHandler) ListAgentKnowledgeBases(c *gin.Context) {
	if _, ok := s.ownedAgent(c); !ok {
		c.JSON(http.StatusNotFound, gin.H{"error": "Agent 不存在"})
		return
	}
	c.JSON(http.StatusOK, s.manager.ListAgentKnowledgeBases(c.Param("id")))
}

// UnbindKnowledge 处理 DELETE /api/agents/:id/knowledge-bases/:kbId。
func (s *AgentHandler) UnbindKnowledge(c *gin.Context) {
	if _, ok := s.ownedAgent(c); !ok {
		c.JSON(http.StatusNotFound, gin.H{"error": "Agent 不存在"})
		return
	}
	_ = s.manager.UnbindKnowledgeFromAgent(c.Param("id"), c.Param("kbId"))
	c.JSON(http.StatusOK, gin.H{"message": "知识库解绑成功"})
}
