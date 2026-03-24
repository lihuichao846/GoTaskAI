package api

import (
	"gotaskai/internal/model"
	"gotaskai/internal/pkg/jwt"
	"gotaskai/internal/queue"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

// Handler 封装了 HTTP API 的路由处理逻辑
type Handler struct {
	manager *queue.TaskManager // 依赖注入任务管理器
}

// NewHandler 初始化 API 处理器
func NewHandler(manager *queue.TaskManager) *Handler {
	return &Handler{manager: manager}
}

// BatchSubmitRequest 定义批量提交任务的请求体
type BatchSubmitRequest struct {
	Tasks []SubmitRequest `json:"tasks" binding:"required,min=1,max=100"` // 一次最多批量提交 100 个
}

// BatchSubmitTasks 处理 POST /api/tasks/batch-submit 请求，批量创建新任务
func (h *Handler) BatchSubmitTasks(c *gin.Context) {
	var req BatchSubmitRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	userID := c.GetUint("userID")
	var responseTasks []map[string]interface{}

	for _, taskReq := range req.Tasks {
		if taskReq.Priority == 0 {
			taskReq.Priority = model.PriorityNormal
		}

		task := &model.Task{
			ID:        uuid.New().String(),
			UserID:    userID,
			Type:      taskReq.Type,
			Priority:  taskReq.Priority,
			Payload:   taskReq.Payload,
			Status:    model.StatusPending,
			MaxRetry:  3,
			CreatedAt: time.Now(),
			UpdatedAt: time.Now(),
		}

		if err := h.manager.AddTask(task); err != nil {
			// 如果某个任务提交失败（如队列满），跳过或记录，不中断其他任务
			responseTasks = append(responseTasks, map[string]interface{}{
				"payload": taskReq.Payload,
				"error":   err.Error(),
				"status":  "failed_to_submit",
			})
			continue
		}

		responseTasks = append(responseTasks, map[string]interface{}{
			"id":      task.ID,
			"status":  string(task.Status),
			"payload": task.Payload,
		})
	}

	c.JSON(http.StatusAccepted, gin.H{
		"message": "批量任务处理完毕",
		"results": responseTasks,
	})
}

type SubmitRequest struct {
	Type     model.TaskType     `json:"type" binding:"required"`    // 任务类型，如 summary, generation 等
	Payload  string             `json:"payload" binding:"required"` // 任务的具体载荷/输入数据
	Priority model.TaskPriority `json:"priority"`                   // 任务优先级 (1:低, 2:普通, 3:高)
}

// SubmitTask 处理 POST /api/tasks/submit 请求，接收客户端参数并创建新任务
func (h *Handler) SubmitTask(c *gin.Context) {
	// 1. 自动解析和校验客户端传递的 JSON 数据
	var req SubmitRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	// 默认设置普通优先级
	if req.Priority == 0 {
		req.Priority = model.PriorityNormal
	}

	// 从中间件上下文中提取用户 ID
	userID := c.GetUint("userID")

	// 2. 构造完整的任务实体
	task := &model.Task{
		ID:        uuid.New().String(), // 使用 UUID 保证任务 ID 的全局唯一性
		UserID:    userID,              // 绑定任务与用户的关系
		Type:      req.Type,
		Priority:  req.Priority, // 设置任务优先级
		Payload:   req.Payload,
		Status:    model.StatusPending, // 新建任务初始状态为"等待中"
		MaxRetry:  3,                   // 默认最大允许重试 3 次
		CreatedAt: time.Now(),
		UpdatedAt: time.Now(),
	}

	// 3. 尝试将任务加入到队列管理器
	if err := h.manager.AddTask(task); err != nil {
		// 如果队列已满，返回 503 错误，提示服务不可用
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": err.Error()})
		return
	}

	// 4. 任务受理成功，返回任务 ID 和状态给客户端 (HTTP 202 Accepted)
	c.JSON(http.StatusAccepted, gin.H{"id": task.ID, "status": string(task.Status)})
}

// GetTaskStatus 处理 GET /api/tasks/:id 请求，查询指定任务的当前状态详情
func (h *Handler) GetTaskStatus(c *gin.Context) {
	// 1. 从 URL 的路径参数中提取任务 ID
	id := c.Param("id")
	if id == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Missing task id"})
		return
	}

	// 2. 从任务管理器中查找该任务
	task, ok := h.manager.GetTask(id)
	if !ok {
		// 找不到对应 ID 的任务返回 404
		c.JSON(http.StatusNotFound, gin.H{"error": "Task not found"})
		return
	}

	// 权限校验：只能查看自己的任务
	userID := c.GetUint("userID")
	if task.UserID != userID && task.UserID != 0 { // 兼容之前未绑定用户的旧测试数据
		c.JSON(http.StatusForbidden, gin.H{"error": "无权访问此任务"})
		return
	}

	// 3. 将任务详情序列化为 JSON 并返回
	c.JSON(http.StatusOK, task)
}

// ListTasks 处理 GET /api/tasks 请求，获取当前登录用户所有的任务列表
func (h *Handler) ListTasks(c *gin.Context) {
	userID := c.GetUint("userID")
	// 1. 调用管理器的方法获取该用户的所有任务数据
	tasks := h.manager.GetTasksByUserID(userID)

	// 2. 返回包含所有任务的 JSON 数组
	c.JSON(http.StatusOK, tasks)
}

// DeleteTask 处理 DELETE /api/tasks/:id 请求，删除指定任务
func (h *Handler) DeleteTask(c *gin.Context) {
	// 1. 从 URL 的路径参数中提取任务 ID
	id := c.Param("id")
	if id == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Missing task id"})
		return
	}

	// 先获取任务，用于权限校验
	task, ok := h.manager.GetTask(id)
	if !ok {
		c.JSON(http.StatusNotFound, gin.H{"error": "Task not found"})
		return
	}

	userID := c.GetUint("userID")
	if task.UserID != userID && task.UserID != 0 {
		c.JSON(http.StatusForbidden, gin.H{"error": "无权删除此任务"})
		return
	}

	// 2. 调用管理器删除任务
	if err := h.manager.DeleteTask(id); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to delete task"})
		return
	}

	// 3. 返回成功响应
	c.JSON(http.StatusOK, gin.H{"message": "Task deleted successfully"})
}

// RetryTask 处理 POST /api/tasks/:id/retry 请求，手动重试失败的任务
func (h *Handler) RetryTask(c *gin.Context) {
	id := c.Param("id")
	if id == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Missing task id"})
		return
	}

	task, ok := h.manager.GetTask(id)
	if !ok {
		c.JSON(http.StatusNotFound, gin.H{"error": "Task not found"})
		return
	}

	userID := c.GetUint("userID")
	if task.UserID != userID && task.UserID != 0 {
		c.JSON(http.StatusForbidden, gin.H{"error": "无权操作此任务"})
		return
	}

	// 只有 failed 状态的任务才能重试
	if task.Status != model.StatusFailed {
		c.JSON(http.StatusBadRequest, gin.H{"error": "只能重试已失败的任务"})
		return
	}

	// 重置任务状态
	task.Status = model.StatusPending
	task.Retries = 0
	task.Error = ""

	// 更新数据库和缓存
	h.manager.UpdateTask(task)

	// 重新入队
	if err := h.manager.Enqueue(task); err != nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "队列已满，请稍后再试"})
		return
	}

	c.JSON(http.StatusOK, gin.H{"message": "任务已重新进入队列等待处理"})
}

// CancelTask 处理 POST /api/tasks/:id/cancel 请求，取消等待中或处理中的任务
func (h *Handler) CancelTask(c *gin.Context) {
	id := c.Param("id")
	if id == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Missing task id"})
		return
	}

	task, ok := h.manager.GetTask(id)
	if !ok {
		c.JSON(http.StatusNotFound, gin.H{"error": "Task not found"})
		return
	}

	userID := c.GetUint("userID")
	if task.UserID != userID && task.UserID != 0 {
		c.JSON(http.StatusForbidden, gin.H{"error": "无权操作此任务"})
		return
	}

	// 只有 pending 或 processing 状态的任务才能取消
	if task.Status != model.StatusPending && task.Status != model.StatusProcessing {
		c.JSON(http.StatusBadRequest, gin.H{"error": "当前状态无法取消"})
		return
	}

	// 将状态标记为 cancelled
	task.Status = model.StatusCancelled
	task.Error = "Task was cancelled by user"
	h.manager.UpdateTask(task)

	c.JSON(http.StatusOK, gin.H{"message": "任务已取消"})
}

// StreamTasks 处理 GET /api/tasks/stream 请求，建立 SSE 长连接
func (h *Handler) StreamTasks(c *gin.Context) {
	// 因为 EventSource 不支持自定义 Header (比如 Authorization: Bearer xxx)，
	// 只能通过 URL 参数传递 Token 来做鉴权
	tokenStr := c.Query("token")
	if tokenStr == "" {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "Missing token"})
		return
	}

	// 验证 Token
	claims, err := jwt.ParseToken(tokenStr)
	if err != nil {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "Invalid token"})
		return
	}
	userID := claims.UserID

	// 设置 SSE 必需的 HTTP Header
	c.Writer.Header().Set("Content-Type", "text/event-stream")
	c.Writer.Header().Set("Cache-Control", "no-cache")
	c.Writer.Header().Set("Connection", "keep-alive")
	// 允许跨域（视情况配置）
	c.Writer.Header().Set("Access-Control-Allow-Origin", "*")

	// 向管理器订阅当前用户的任务更新事件
	ch := h.manager.Subscribe(userID)
	// 确保连接断开时取消订阅，防止内存泄漏
	defer h.manager.Unsubscribe(userID, ch)

	// 监听客户端连接断开事件
	clientGone := c.Writer.CloseNotify()

	// 持续监听管道，有数据就推给前端
	for {
		select {
		case <-clientGone:
			// 客户端主动断开了连接 (例如关闭了浏览器标签页)
			return
		case task := <-ch:
			// 接收到状态更新，推送到前端
			c.SSEvent("task_update", task)
			// 必须调用 Flush，把缓冲的数据立即发出去
			c.Writer.Flush()
		}
	}
}
