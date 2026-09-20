package queue

import (
	"context"
	"encoding/json"
	"gotaskai/internal/events"
	"gotaskai/internal/model"
	"log/slog"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/hibiken/asynq"
	"github.com/nats-io/nats.go"
	"github.com/redis/go-redis/v9"
	"gorm.io/gorm"
)

// TaskManager 负责管理任务状态和任务队列
type TaskManager struct {
	db          *gorm.DB        // MySQL 数据库实例，用于持久化
	rdb         *redis.Client   // Redis 客户端，用于缓存任务/Agent 状态（不再承担事件广播）
	asynqClient *asynq.Client   // Asynq 客户端，用于发布任务
	ctx         context.Context // Redis 所需的上下文

	// eventBus 是 Worker 与 API 之间实时事件的独立消息通道（NATS），与 Redis 队列解耦。
	eventBus *events.EventBus

	// SSE 相关的订阅管理
	clientsMu   sync.RWMutex
	clients     map[uint]map[chan *model.Task]bool          // userID -> set of task channels
	toolClients map[uint]map[chan *model.ToolCallEvent]bool // userID -> set of tool event channels
}

// NewTaskManager 创建并初始化一个任务管理器。
// eventBus 为可选的实时事件通道；若为 nil，则回退到进程内广播（单机部署场景）。
func NewTaskManager(db *gorm.DB, rdb *redis.Client, redisOpt asynq.RedisConnOpt, eventBus *events.EventBus) *TaskManager {
	m := &TaskManager{
		db:          db,
		rdb:         rdb,
		asynqClient: asynq.NewClient(redisOpt),
		ctx:         context.Background(),
		eventBus:    eventBus,
		clients:     make(map[uint]map[chan *model.Task]bool),
		toolClients: make(map[uint]map[chan *model.ToolCallEvent]bool),
	}

	// 微服务解耦：启动后台协程，通过 NATS 监听跨进程的全局状态更新。
	go m.listenForGlobalUpdates()

	return m
}

// listenForGlobalUpdates 通过 NATS 订阅跨进程任务与工具事件。
func (m *TaskManager) listenForGlobalUpdates() {
	if m.eventBus == nil {
		return
	}

	err := m.eventBus.Subscribe(events.SubjectTaskUpdates, func(msg *nats.Msg) {
		var t model.Task
		if err := json.Unmarshal(msg.Data, &t); err == nil {
			m.broadcast(&t)
		}
	})
	if err != nil {
		slog.Error("subscribe task updates failed", "subject", events.SubjectTaskUpdates, "error", err)
	}

	err = m.eventBus.Subscribe(events.SubjectToolEvents, func(msg *nats.Msg) {
		var ev model.ToolCallEvent
		if err := json.Unmarshal(msg.Data, &ev); err == nil {
			m.broadcastToolEvent(&ev)
		}
	})
	if err != nil {
		slog.Error("subscribe tool events failed", "subject", events.SubjectToolEvents, "error", err)
	}
}

// Close 关闭 Asynq 客户端连接
func (m *TaskManager) Close() error {
	return m.asynqClient.Close()
}

// Subscribe 为指定用户订阅任务状态更新
func (m *TaskManager) Subscribe(userID uint) chan *model.Task {
	m.clientsMu.Lock()
	defer m.clientsMu.Unlock()

	if m.clients[userID] == nil {
		m.clients[userID] = make(map[chan *model.Task]bool)
	}

	ch := make(chan *model.Task, 10) // 缓冲通道防止阻塞
	m.clients[userID][ch] = true
	return ch
}

// Unsubscribe 取消订阅
func (m *TaskManager) Unsubscribe(userID uint, ch chan *model.Task) {
	m.clientsMu.Lock()
	defer m.clientsMu.Unlock()

	if userClients, ok := m.clients[userID]; ok {
		delete(userClients, ch)
		close(ch)
		if len(userClients) == 0 {
			delete(m.clients, userID)
		}
	}
}

// broadcast 向特定用户的客户端广播任务状态
func (m *TaskManager) broadcast(t *model.Task) {
	m.clientsMu.RLock()
	defer m.clientsMu.RUnlock()

	if userClients, ok := m.clients[t.UserID]; ok {
		for ch := range userClients {
			select {
			case ch <- t: // 尝试推送
			default: // 如果通道满了就丢弃，防止阻塞整个系统
			}
		}
	}
}

// SubscribeToolEvents 为指定用户订阅工具调用过程事件。
func (m *TaskManager) SubscribeToolEvents(userID uint) chan *model.ToolCallEvent {
	m.clientsMu.Lock()
	defer m.clientsMu.Unlock()

	if m.toolClients[userID] == nil {
		m.toolClients[userID] = make(map[chan *model.ToolCallEvent]bool)
	}

	ch := make(chan *model.ToolCallEvent, 10)
	m.toolClients[userID][ch] = true
	return ch
}

// UnsubscribeToolEvents 取消工具事件订阅。
func (m *TaskManager) UnsubscribeToolEvents(userID uint, ch chan *model.ToolCallEvent) {
	m.clientsMu.Lock()
	defer m.clientsMu.Unlock()

	if userClients, ok := m.toolClients[userID]; ok {
		delete(userClients, ch)
		close(ch)
		if len(userClients) == 0 {
			delete(m.toolClients, userID)
		}
	}
}

// broadcastToolEvent 向特定用户广播工具调用事件。
func (m *TaskManager) broadcastToolEvent(ev *model.ToolCallEvent) {
	m.clientsMu.RLock()
	defer m.clientsMu.RUnlock()

	if userClients, ok := m.toolClients[ev.UserID]; ok {
		for ch := range userClients {
			select {
			case ch <- ev:
			default:
			}
		}
	}
}

// PublishToolEvent 将工具调用事件发布到 NATS，供 API 进程转发给 SSE 客户端。
func (m *TaskManager) PublishToolEvent(ev *model.ToolCallEvent) {
	if m.eventBus == nil {
		return
	}
	_ = m.eventBus.PublishJSON(events.SubjectToolEvents, ev)
}

// cacheTask 将任务状态缓存到 Redis 中
func (m *TaskManager) cacheTask(t *model.Task) {
	data, err := json.Marshal(t)
	if err == nil {
		// 设置缓存过期时间为 24 小时
		m.rdb.Set(m.ctx, "task:"+t.ID, data, 24*time.Hour)
	}
}

// AddTask 将新任务加入管理器并推入队列
func (m *TaskManager) AddTask(t *model.Task) error {
	// 1. 持久化到 MySQL
	if err := m.db.Create(t).Error; err != nil {
		return err
	}

	// 2. 缓存到 Redis
	m.cacheTask(t)

	// 3. 将任务推入相应的优先级队列
	return m.Enqueue(t)
}

// Enqueue 将任务推入 Asynq 队列
func (m *TaskManager) Enqueue(t *model.Task) error {
	var queueName string
	switch t.Priority {
	case model.PriorityHigh:
		queueName = "high"
	case model.PriorityLow:
		queueName = "low"
	default:
		queueName = "default"
	}

	payload, err := json.Marshal(t)
	if err != nil {
		return err
	}

	task := asynq.NewTask("task:process", payload)
	_, err = m.asynqClient.Enqueue(task, asynq.Queue(queueName), asynq.MaxRetry(3))
	return err
}

// GetTask 根据 ID 获取任务的当前状态
func (m *TaskManager) GetTask(id string) (*model.Task, bool) {
	// 1. 尝试从 Redis 缓存获取
	val, err := m.rdb.Get(m.ctx, "task:"+id).Result()
	if err == nil && val != "" {
		var t model.Task
		if err := json.Unmarshal([]byte(val), &t); err == nil {
			return &t, true
		}
	}

	// 2. 如果缓存未命中，从 MySQL 获取
	var t model.Task
	if err := m.db.Where("id = ?", id).First(&t).Error; err != nil {
		return nil, false
	}

	// 3. 将数据库查询到的结果回写到缓存
	m.cacheTask(&t)
	return &t, true
}

// DeleteSession 批量删除属于某个 SessionID 的所有任务
func (m *TaskManager) DeleteSession(sessionID string, userID uint) error {
	// 1. 查找属于该用户和会话的所有任务
	// 注意：session_id 可能为空，如果为空说明这是一个没有子任务的根任务，应该匹配它的自身 ID
	var tasks []model.Task
	if err := m.db.Where("(session_id = ? OR id = ?) AND user_id = ?", sessionID, sessionID, userID).Find(&tasks).Error; err != nil {
		return err
	}

	if len(tasks) == 0 {
		return nil // 如果没有任务，直接返回成功
	}

	// 2. 从数据库批量删除
	if err := m.db.Where("(session_id = ? OR id = ?) AND user_id = ?", sessionID, sessionID, userID).Delete(&model.Task{}).Error; err != nil {
		return err
	}

	// 3. 从 Redis 缓存批量删除，并尝试从 Asynq 队列中取消它们
	for _, t := range tasks {
		m.rdb.Del(m.ctx, "task:"+t.ID)

		// 如果任务还在排队或处理中，尝试让 Asynq 忽略它
		if t.Status == model.StatusPending || t.Status == model.StatusProcessing {
			// 将状态标记为已取消，Worker 在处理时会直接跳过
			t.Status = model.StatusCancelled
			m.UpdateTask(&t)
		}
	}

	return nil
}

// GetTaskHistory 获取指定 SessionID 的历史完成任务，用于构建大模型上下文
func (m *TaskManager) GetTaskHistory(sessionID string, limit int) []*model.Task {
	var tasks []*model.Task
	if sessionID == "" {
		return tasks
	}

	// 查询该会话下，已成功完成的任务，按照创建时间倒序（最近的在前面）
	// 修改：同时匹配 session_id 或者本身 id 就是这个 sessionID 的任务，这样无 session 的单次任务也能被用作后续对话的上文
	m.db.Where("(session_id = ? OR id = ?) AND status = ?", sessionID, sessionID, model.StatusCompleted).
		Order("created_at desc").
		Limit(limit).
		Find(&tasks)

	// 将结果反转，使得最老的对话在前面，符合大模型 prompt 的阅读顺序
	for i, j := 0, len(tasks)-1; i < j; i, j = i+1, j-1 {
		tasks[i], tasks[j] = tasks[j], tasks[i]
	}
	return tasks
}

// GetConversationHistory 获取指定会话（按 ConversationID）的历史完成任务。
func (m *TaskManager) GetConversationHistory(conversationID string, limit int) []*model.Task {
	var tasks []*model.Task
	if conversationID == "" {
		return tasks
	}

	m.db.Where("(conversation_id = ? OR id = ?) AND status = ?", conversationID, conversationID, model.StatusCompleted).
		Order("created_at desc").
		Limit(limit).
		Find(&tasks)

	for i, j := 0, len(tasks)-1; i < j; i, j = i+1, j-1 {
		tasks[i], tasks[j] = tasks[j], tasks[i]
	}
	return tasks
}

// GetConversationHistorySince 获取指定会话中创建时间晚于 after 的历史完成任务，用于避免重复压缩已摘要内容。
// after 为零值时退化为 GetConversationHistory 的默认行为（取最近 limit 条）。
func (m *TaskManager) GetConversationHistorySince(conversationID string, after *time.Time, limit int) []*model.Task {
	var tasks []*model.Task
	if conversationID == "" {
		return tasks
	}

	query := m.db.Where("(conversation_id = ? OR id = ?) AND status = ?", conversationID, conversationID, model.StatusCompleted)
	if after != nil && !after.IsZero() {
		query = query.Where("created_at > ?", *after)
	}

	query.Order("created_at desc").Limit(limit).Find(&tasks)

	// 反之，使最老的对话在最前，符合大模型 prompt 的阅读顺序
	for i, j := 0, len(tasks)-1; i < j; i, j = i+1, j-1 {
		tasks[i], tasks[j] = tasks[j], tasks[i]
	}
	return tasks
}

// CountConversationHistorySince 统计游标之后的历史完成任务【总轮数】（不受 LIMIT 影响）。
//
// 这是"真实溢出"的度量基础：装载查询本身带 LIMIT，看不到窗口之外的部分；
// 只有拿到全量计数，调用方才能发现"还有多少轮没有被装载、因而需要被压缩"。
func (m *TaskManager) CountConversationHistorySince(conversationID string, after *time.Time) int64 {
	if conversationID == "" {
		return 0
	}
	query := m.db.Model(&model.Task{}).
		Where("(conversation_id = ? OR id = ?) AND status = ?", conversationID, conversationID, model.StatusCompleted)
	if after != nil && !after.IsZero() {
		query = query.Where("created_at > ?", *after)
	}
	var n int64
	if err := query.Count(&n).Error; err != nil {
		slog.Error("count conversation history failed", "conversation_id", conversationID, "error", err)
		return 0
	}
	return n
}

// GetOldestConversationBatch 取游标之后【最旧】的一批历史完成任务（升序）。
//
// 与 GetConversationHistorySince（倒序取最新 N 条）方向相反，专供压缩使用：
// 压缩必须沿时间轴【由旧向新】推进游标，否则"装载窗口之外"的更早轮次永远不会被覆盖。
func (m *TaskManager) GetOldestConversationBatch(conversationID string, after *time.Time, limit int) []*model.Task {
	var tasks []*model.Task
	if conversationID == "" || limit <= 0 {
		return tasks
	}
	query := m.db.Where("(conversation_id = ? OR id = ?) AND status = ?",
		conversationID, conversationID, model.StatusCompleted)
	if after != nil && !after.IsZero() {
		query = query.Where("created_at > ?", *after)
	}
	query.Order("created_at asc").Limit(limit).Find(&tasks)
	return tasks
}

// GetAllTasks 获取当前系统中的所有任务列表（已废弃，改为按用户获取）
// GetTasksByUserID 获取当前登录用户的所有任务列表
func (m *TaskManager) GetTasksByUserID(userID uint) []*model.Task {
	var tasks []*model.Task
	// 从 MySQL 获取属于该用户的最近 100 条任务，按时间倒序
	m.db.Where("user_id = ?", userID).Order("created_at desc").Limit(100).Find(&tasks)
	return tasks
}

// UpdateTask 更新任务的状态
func (m *TaskManager) UpdateTask(t *model.Task) {
	t.UpdatedAt = time.Now()

	// 1. 更新 MySQL
	m.db.Save(t)

	// 2. 更新 Redis 缓存
	m.cacheTask(t)

	// 3. 触发跨进程广播通知（微服务架构改造核心）
	// 在单体架构中，直接调用 m.broadcast(t) 即可。
	// 但现在 Worker 和 API 是两个独立的进程：
	//  - Worker 负责更新任务状态；API 负责把状态推送给前端（SSE）。
	//  - Worker 通过 NATS 发布事件，API 订阅后转发给 SSE 客户端。
	// 这样实时事件通道与 Redis 队列/缓存彻底解耦。
	if m.eventBus != nil {
		_ = m.eventBus.PublishJSON(events.SubjectTaskUpdates, t)
	}
	m.broadcast(t)
}

// UpdateTaskIfActive 仅在任务仍处于非终态（非 cancelled/completed）时执行写回。
// 用于 worker 侧的终态/中间态回写，防止「取消/完成」被并发的 worker 结果覆盖。
// 返回 false 表示条件不满足（0 行受影响），说明任务已被并发修改，调用方应丢弃本次结果。
func (m *TaskManager) UpdateTaskIfActive(t *model.Task) bool {
	t.UpdatedAt = time.Now()

	res := m.db.Model(&model.Task{}).
		Where("id = ? AND status NOT IN ?", t.ID, []string{string(model.StatusCancelled), string(model.StatusCompleted)}).
		Updates(map[string]any{
			"status":     string(t.Status),
			"result":     t.Result,
			"error":      t.Error,
			"retries":    t.Retries,
			"updated_at": t.UpdatedAt,
		})
	if res.Error != nil || res.RowsAffected == 0 {
		return false
	}

	// 回读最新状态并刷新缓存/广播。
	if updated, ok := m.GetTask(t.ID); ok {
		m.cacheTask(updated)
		if m.eventBus != nil {
			_ = m.eventBus.PublishJSON(events.SubjectTaskUpdates, updated)
		}
		m.broadcast(updated)
	}
	return true
}

// SaveRunLog 写入一条任务运行观测记录。
func (m *TaskManager) SaveRunLog(r *model.RunLog) error {
	return m.db.Create(r).Error
}

// SaveToolCallLog 写入一条工具调用观测记录。
func (m *TaskManager) SaveToolCallLog(l *model.ToolCallLog) error {
	return m.db.Create(l).Error
}

// AccumulateTaskUsage 将本次运行的模型调用计量原子累加到任务累计字段（跨重试续算）。
// 用于任务级总调用上限与成本的可跨重试累计。
func (m *TaskManager) AccumulateTaskUsage(taskID string, calls, tokensIn, tokensOut int) {
	if calls == 0 && tokensIn == 0 && tokensOut == 0 {
		return
	}
	if err := m.db.Model(&model.Task{}).Where("id = ?", taskID).Updates(map[string]any{
		"total_calls":      gorm.Expr("total_calls + ?", calls),
		"total_tokens_in":  gorm.Expr("total_tokens_in + ?", tokensIn),
		"total_tokens_out": gorm.Expr("total_tokens_out + ?", tokensOut),
	}).Error; err != nil {
		slog.Error("accumulate task usage failed", "task_id", taskID, "error", err)
		return
	}
	// 失效 Redis 缓存：否则下次 GetTask 读到过期的累计计量，跨重试熔断会失效。
	m.rdb.Del(m.ctx, "task:"+taskID)
}

// CancelTaskIfActive 仅当任务仍处于 pending/processing 时将其标记为 cancelled，
// 避免覆盖已进入 completed/failed 终态的任务。返回 false 表示条件不满足（任务已终态）。
func (m *TaskManager) CancelTaskIfActive(id string) bool {
	res := m.db.Model(&model.Task{}).
		Where("id = ? AND status IN ?", id, []string{string(model.StatusPending), string(model.StatusProcessing)}).
		Updates(map[string]any{
			"status":     string(model.StatusCancelled),
			"error":      "Task was cancelled by user",
			"updated_at": time.Now(),
		})
	if res.Error != nil || res.RowsAffected == 0 {
		return false
	}
	if updated, ok := m.GetTask(id); ok {
		m.cacheTask(updated)
		if m.eventBus != nil {
			_ = m.eventBus.PublishJSON(events.SubjectTaskUpdates, updated)
		}
		m.broadcast(updated)
	}
	return true
}

// PublishTaskCancel 向所有 worker 进程广播任务取消控制指令（NATS 广播，替代 gRPC 单播）。
// 事件不可达时，取消仍通过 HTTP 直改任务状态的既有链路生效。
func (m *TaskManager) PublishTaskCancel(taskID string) {
	if m.eventBus == nil {
		return
	}
	_ = m.eventBus.PublishJSON(events.SubjectTaskCancel, map[string]string{"task_id": taskID})
}

// SubscribeTaskCancel 订阅任务取消控制指令，收到后调用 handler（用于 worker 中断进行中的 LLM 调用）。
func (m *TaskManager) SubscribeTaskCancel(handler func(taskID string)) {
	if m.eventBus == nil {
		return
	}
	err := m.eventBus.Subscribe(events.SubjectTaskCancel, func(msg *nats.Msg) {
		var payload struct {
			TaskID string `json:"task_id"`
		}
		if err := json.Unmarshal(msg.Data, &payload); err == nil && payload.TaskID != "" {
			handler(payload.TaskID)
		}
	})
	if err != nil {
		slog.Error("subscribe task cancel failed", "subject", events.SubjectTaskCancel, "error", err)
	}
}

// DeleteTask 从系统中删除指定的任务
func (m *TaskManager) DeleteTask(id string) error {
	// 1. 从 MySQL 中删除记录
	if err := m.db.Where("id = ?", id).Delete(&model.Task{}).Error; err != nil {
		return err
	}

	// 2. 从 Redis 缓存中删除记录
	m.rdb.Del(m.ctx, "task:"+id)
	return nil
}

// GetAgent 根据 ID 获取 Agent 配置（Cache-Aside：先 Redis 后 MySQL）
func (m *TaskManager) GetAgent(id string) (*model.Agent, bool) {
	// 1. 尝试从 Redis 缓存获取
	val, err := m.rdb.Get(m.ctx, "agent:"+id).Result()
	if err == nil && val != "" {
		var a model.Agent
		if err := json.Unmarshal([]byte(val), &a); err == nil {
			return &a, true
		}
	}

	// 2. 缓存未命中，从 MySQL 获取
	var a model.Agent
	if err := m.db.Where("id = ?", id).First(&a).Error; err != nil {
		return nil, false
	}

	// 3. 回写缓存
	m.cacheAgent(&a)
	return &a, true
}

// ListAgents 获取指定用户的全部 Agent
func (m *TaskManager) ListAgents(userID uint) []*model.Agent {
	var agents []*model.Agent
	m.db.Where("user_id = ?", userID).Order("created_at desc").Find(&agents)
	return agents
}

// CreateAgent 新建 Agent 并写缓存
func (m *TaskManager) CreateAgent(a *model.Agent) error {
	if err := m.db.Create(a).Error; err != nil {
		return err
	}
	m.cacheAgent(a)
	return nil
}

// UpdateAgent 更新 Agent 并失效缓存（实现配置热更新）
func (m *TaskManager) UpdateAgent(a *model.Agent) error {
	if err := m.db.Save(a).Error; err != nil {
		return err
	}
	m.rdb.Del(m.ctx, "agent:"+a.ID)
	return nil
}

// DeleteAgent 删除 Agent 并清除缓存
func (m *TaskManager) DeleteAgent(id string) error {
	if err := m.db.Where("id = ?", id).Delete(&model.Agent{}).Error; err != nil {
		return err
	}
	m.rdb.Del(m.ctx, "agent:"+id)
	return nil
}

// cacheAgent 将 Agent 配置写入 Redis 缓存（短 TTL，以便配置热更新及时生效）
func (m *TaskManager) cacheAgent(a *model.Agent) {
	data, err := json.Marshal(a)
	if err == nil {
		m.rdb.Set(m.ctx, "agent:"+a.ID, data, 5*time.Minute)
	}
}

// ===== Tool 管理 =====

// CreateTool 新建工具（内置或用户私有 MCP 工具）。
func (m *TaskManager) CreateTool(t *model.Tool) error {
	return m.db.Create(t).Error
}

// ListTools 获取当前用户可见的工具（内置全局 + 用户私有）。
func (m *TaskManager) ListTools(userID uint) []*model.Tool {
	var tools []*model.Tool
	if err := m.db.Where("is_builtin = ? OR user_id = ?", true, userID).
		Order("is_builtin desc, created_at asc").
		Find(&tools).Error; err != nil {
		slog.Error("list tools failed", "user_id", userID, "error", err)
		return nil
	}
	return tools
}

// GetTool 根据 ID 获取工具。
func (m *TaskManager) GetTool(id string) (*model.Tool, bool) {
	var t model.Tool
	if err := m.db.Where("id = ?", id).First(&t).Error; err != nil {
		return nil, false
	}
	return &t, true
}

// DeleteTool 删除工具（仅限用户私有 MCP 工具，内置工具应禁止删除）。
func (m *TaskManager) DeleteTool(id string) error {
	return m.db.Where("id = ?", id).Delete(&model.Tool{}).Error
}

// UpdateTool 更新工具的可编辑字段（名称、展示名、描述与 MCP 配置）。
func (m *TaskManager) UpdateTool(t *model.Tool) error {
	return m.db.Model(t).Select("name", "display_name", "description", "config", "updated_at").
		Updates(map[string]any{
			"name":         t.Name,
			"display_name": t.DisplayName,
			"description":  t.Description,
			"config":       t.Config,
			"updated_at":   time.Now(),
		}).Error
}

// UpdateToolStatus 更新工具状态（active / disabled）。
func (m *TaskManager) UpdateToolStatus(id, status string) error {
	return m.db.Model(&model.Tool{}).Where("id = ?", id).
		Updates(map[string]any{"status": status, "updated_at": time.Now()}).Error
}

// ListToolAgents 反查绑定该工具的所有 Agent。
func (m *TaskManager) ListToolAgents(toolID string) []*model.Agent {
	var bindings []model.AgentTool
	m.db.Where("tool_id = ?", toolID).Find(&bindings)
	ids := make([]string, 0, len(bindings))
	for _, b := range bindings {
		ids = append(ids, b.AgentID)
	}
	if len(ids) == 0 {
		return nil
	}
	var agents []*model.Agent
	m.db.Where("id IN ?", ids).Find(&agents)
	return agents
}

// ===== KnowledgeBase 管理 =====

// CreateKnowledgeBase 新建知识库。
func (m *TaskManager) CreateKnowledgeBase(kb *model.KnowledgeBase) error {
	return m.db.Create(kb).Error
}

// ListKnowledgeBases 获取当前用户的知识库列表。
func (m *TaskManager) ListKnowledgeBases(userID uint) []*model.KnowledgeBase {
	var kbs []*model.KnowledgeBase
	m.db.Where("user_id = ?", userID).Order("created_at desc").Find(&kbs)
	return kbs
}

// GetKnowledgeBase 根据 ID 获取知识库。
func (m *TaskManager) GetKnowledgeBase(id string) (*model.KnowledgeBase, bool) {
	var kb model.KnowledgeBase
	if err := m.db.Where("id = ?", id).First(&kb).Error; err != nil {
		return nil, false
	}
	return &kb, true
}

// UpdateKnowledgeBase 更新知识库。
func (m *TaskManager) UpdateKnowledgeBase(kb *model.KnowledgeBase) error {
	return m.db.Save(kb).Error
}

// IncrementKnowledgeBaseDocCount 原子累加知识库文档计数，并置为 ready。
// 通过数据库原子表达式 doc_count + 1 取代读-改-写，避免并发构建同一 KB 时丢失更新。
// 返回 false 表示知识库不存在或更新失败。
func (m *TaskManager) IncrementKnowledgeBaseDocCount(kbID string) bool {
	res := m.db.Model(&model.KnowledgeBase{}).
		Where("id = ?", kbID).
		Updates(map[string]any{
			"doc_count":  gorm.Expr("doc_count + 1"),
			"status":     "ready",
			"updated_at": time.Now(),
		})
	if res.Error != nil || res.RowsAffected == 0 {
		return false
	}
	return true
}

// RequestKnowledgeBaseDelete 发起知识库删除：先把 KB 标记为 deleting（保证中断可恢复），
// 再入队异步清理任务。真正的三层清理（Milvus / Neo4j / MySQL）由 Worker 的 kb:delete 完成，
// 失败可被 Asynq 重试，不阻塞用户请求。
func (m *TaskManager) RequestKnowledgeBaseDelete(id string) error {
	res := m.db.Model(&model.KnowledgeBase{}).
		Where("id = ?", id).
		Updates(map[string]any{"status": "deleting", "updated_at": time.Now()})
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected == 0 {
		return gorm.ErrRecordNotFound
	}
	return m.EnqueueKBDelete(id)
}

// EnqueueKBDelete 将知识库清理任务入队（低优先级队列）。
func (m *TaskManager) EnqueueKBDelete(kbID string) error {
	payload, err := json.Marshal(map[string]string{"kb_id": kbID})
	if err != nil {
		return err
	}
	task := asynq.NewTask("kb:delete", payload)
	_, err = m.asynqClient.Enqueue(task, asynq.Queue("low"), asynq.MaxRetry(5))
	return err
}

// ListDocumentIDsByKB 返回知识库下的全部文档 ID，供失效清理定位图谱来源。
func (m *TaskManager) ListDocumentIDsByKB(kbID string) []string {
	var ids []string
	if err := m.db.Model(&model.Document{}).Where("kb_id = ?", kbID).Pluck("id", &ids).Error; err != nil {
		slog.Error("list document ids failed", "kb_id", kbID, "error", err)
		return nil
	}
	return ids
}

// DeleteDocumentsByKB 物理删除知识库下的全部文档行。
func (m *TaskManager) DeleteDocumentsByKB(kbID string) error {
	return m.db.Where("kb_id = ?", kbID).Delete(&model.Document{}).Error
}

// DeleteKnowledgeBaseRow 物理删除知识库行（三层清理完成后的最后一步）。
func (m *TaskManager) DeleteKnowledgeBaseRow(id string) error {
	return m.db.Where("id = ?", id).Delete(&model.KnowledgeBase{}).Error
}

// ===== Conversation 管理 =====

// CreateConversation 新建会话记录。
func (m *TaskManager) CreateConversation(c *model.Conversation) error {
	return m.db.Create(c).Error
}

// GetConversation 根据 ID 获取会话。
func (m *TaskManager) GetConversation(id string) (*model.Conversation, bool) {
	var c model.Conversation
	if err := m.db.Where("id = ?", id).First(&c).Error; err != nil {
		return nil, false
	}
	return &c, true
}

// GetConversationCompressState 读取会话的滚动摘要及其已覆盖的任务创建时间（cutoff）。
func (m *TaskManager) GetConversationCompressState(id string) (summary string, cutoff *time.Time) {
	if id == "" {
		return "", nil
	}
	var c model.Conversation
	if err := m.db.Select("summary", "summary_cutoff").Where("id = ?", id).First(&c).Error; err != nil {
		return "", nil
	}
	return c.Summary, c.SummaryCutoff
}

// SaveConversationSummaryCAS 以 CAS（compare-and-swap）语义保存会话摘要与压缩游标：
// 仅当新游标【严格前进】时才写入，返回 true 表示本次写入生效。
//
// 为什么要 CAS：同一会话可能被并发处理（Asynq 多协程），而"读游标 → 压缩 → 写回"是典型的
// 读改写。若无条件 Updates，后写者会覆盖先写者的摘要，且游标可能【倒退】，
// 导致已压缩轮次被重复压缩、进度丢失。写入失败时调用方应丢弃本次摘要（下次请求会自然收敛）。
func (m *TaskManager) SaveConversationSummaryCAS(id string, summary string, cutoff time.Time) bool {
	if id == "" {
		return false
	}
	res := m.db.Model(&model.Conversation{}).
		Where("id = ? AND (summary_cutoff IS NULL OR summary_cutoff < ?)", id, cutoff).
		Updates(map[string]any{
			"summary":        summary,
			"summary_cutoff": cutoff,
			"updated_at":     time.Now(),
		})
	if res.Error != nil {
		slog.Error("save conversation summary failed", "conversation_id", id, "error", res.Error)
		return false
	}
	// GORM 在影响 0 行时不会报错，必须用 RowsAffected 判定是否真正写入。
	return res.RowsAffected == 1
}

// ListConversations 获取当前用户的全部会话（按最近更新倒序）。
func (m *TaskManager) ListConversations(userID uint) []*model.Conversation {
	var convs []*model.Conversation
	m.db.Where("user_id = ?", userID).Order("updated_at desc").Find(&convs)
	return convs
}

// DeleteConversation 删除指定会话（仅限归属用户）。
func (m *TaskManager) DeleteConversation(id string, userID uint) error {
	return m.db.Where("id = ? AND user_id = ?", id, userID).Delete(&model.Conversation{}).Error
}

// GetOrCreateConversation 按 sessionID 查找或创建会话，返回会话记录。
// 兼容策略：若 sessionID 非空，优先以 sessionID 作为会话 ID 复用旧会话；
// 否则生成新的 UUID 会话。
func (m *TaskManager) GetOrCreateConversation(userID uint, agentID, sessionID string) (*model.Conversation, error) {
	if sessionID != "" {
		if conv, ok := m.GetConversation(sessionID); ok && conv.UserID == userID {
			return conv, nil
		}
		conv := &model.Conversation{
			ID:        sessionID,
			UserID:    userID,
			AgentID:   agentID,
			CreatedAt: time.Now(),
			UpdatedAt: time.Now(),
		}
		if err := m.CreateConversation(conv); err != nil {
			return nil, err
		}
		return conv, nil
	}

	conv := &model.Conversation{
		ID:        uuid.New().String(),
		UserID:    userID,
		AgentID:   agentID,
		CreatedAt: time.Now(),
		UpdatedAt: time.Now(),
	}
	if err := m.CreateConversation(conv); err != nil {
		return nil, err
	}
	return conv, nil
}

// TouchConversation 更新会话的最近更新时间与标题。
func (m *TaskManager) TouchConversation(id string, title string) error {
	updates := map[string]any{"updated_at": time.Now()}
	if title != "" {
		updates["title"] = title
	}
	return m.db.Model(&model.Conversation{}).Where("id = ?", id).Updates(updates).Error
}

// EnsureConversationTitle 当会话标题为空时，用给定文本生成标题；同时更新会话的最近活动时间。
func (m *TaskManager) EnsureConversationTitle(id string, title string) error {
	if id == "" {
		return nil
	}
	var conv model.Conversation
	if err := m.db.Where("id = ?", id).First(&conv).Error; err != nil {
		return err
	}
	if conv.Title != "" {
		// 标题已存在，仅刷新最近活动时间
		return m.TouchConversation(id, "")
	}
	return m.TouchConversation(id, title)
}

// BackfillConversations 将历史任务的 SessionID 迁移为 Conversation 记录（幂等）。
func (m *TaskManager) BackfillConversations() error {
	var tasks []model.Task
	if err := m.db.Where("session_id <> '' AND (conversation_id = '' OR conversation_id IS NULL)").
		Find(&tasks).Error; err != nil {
		return err
	}
	for _, t := range tasks {
		conv, err := m.GetOrCreateConversation(t.UserID, t.AgentID, t.SessionID)
		if err != nil {
			return err
		}
		if err := m.db.Model(&model.Task{}).Where("id = ?", t.ID).
			Update("conversation_id", conv.ID).Error; err != nil {
			return err
		}
	}
	return nil
}

// ===== Agent 绑定关系 =====

// BindToolToAgent 将工具绑定到 Agent。
func (m *TaskManager) BindToolToAgent(agentID, toolID string) error {
	return m.db.Create(&model.AgentTool{AgentID: agentID, ToolID: toolID}).Error
}

// UnbindToolFromAgent 将工具从 Agent 解绑。
func (m *TaskManager) UnbindToolFromAgent(agentID, toolID string) error {
	return m.db.Where("agent_id = ? AND tool_id = ?", agentID, toolID).Delete(&model.AgentTool{}).Error
}

// ListAgentToolIDs 返回 Agent 绑定的工具 ID 列表。
func (m *TaskManager) ListAgentToolIDs(agentID string) []string {
	var bindings []model.AgentTool
	m.db.Where("agent_id = ?", agentID).Find(&bindings)
	ids := make([]string, 0, len(bindings))
	for _, b := range bindings {
		ids = append(ids, b.ToolID)
	}
	return ids
}

// ListAgentTools 返回 Agent 绑定的完整工具列表。
func (m *TaskManager) ListAgentTools(agentID string) []*model.Tool {
	ids := m.ListAgentToolIDs(agentID)
	if len(ids) == 0 {
		return nil
	}
	var tools []*model.Tool
	m.db.Where("id IN ?", ids).Find(&tools)
	return tools
}

// BindKnowledgeToAgent 将知识库绑定到 Agent。
func (m *TaskManager) BindKnowledgeToAgent(agentID, kbID string) error {
	return m.db.Create(&model.AgentKnowledge{AgentID: agentID, KnowledgeBaseID: kbID}).Error
}

// UnbindKnowledgeFromAgent 将知识库从 Agent 解绑。
func (m *TaskManager) UnbindKnowledgeFromAgent(agentID, kbID string) error {
	return m.db.Where("agent_id = ? AND knowledge_base_id = ?", agentID, kbID).Delete(&model.AgentKnowledge{}).Error
}

// ListAgentKnowledgeIDs 返回 Agent 绑定的知识库 ID 列表。
func (m *TaskManager) ListAgentKnowledgeIDs(agentID string) []string {
	var bindings []model.AgentKnowledge
	m.db.Where("agent_id = ?", agentID).Find(&bindings)
	ids := make([]string, 0, len(bindings))
	for _, b := range bindings {
		ids = append(ids, b.KnowledgeBaseID)
	}
	return ids
}

// ListAgentKnowledgeBases 返回 Agent 绑定的完整知识库列表。
func (m *TaskManager) ListAgentKnowledgeBases(agentID string) []*model.KnowledgeBase {
	ids := m.ListAgentKnowledgeIDs(agentID)
	if len(ids) == 0 {
		return nil
	}
	var kbs []*model.KnowledgeBase
	m.db.Where("id IN ?", ids).Find(&kbs)
	return kbs
}

// ===== Document 管理 =====

// CreateDocument 新建文档。
func (m *TaskManager) CreateDocument(d *model.Document) error {
	return m.db.Create(d).Error
}

// GetDocument 根据 ID 获取文档。
func (m *TaskManager) GetDocument(id string) (*model.Document, bool) {
	var d model.Document
	if err := m.db.Where("id = ?", id).First(&d).Error; err != nil {
		return nil, false
	}
	return &d, true
}

// GetDocumentBySource 按 (kb_id, source_path) 查询文档，用于增量扫描的幂等判定。
// 手动上传的文档 source_path 为空，不参与来源匹配。
func (m *TaskManager) GetDocumentBySource(kbID, sourcePath string) (*model.Document, bool) {
	if kbID == "" || sourcePath == "" {
		return nil, false
	}
	var d model.Document
	if err := m.db.Where("kb_id = ? AND source_path = ?", kbID, sourcePath).First(&d).Error; err != nil {
		return nil, false
	}
	return &d, true
}

// GetDocumentSummaries 返回指定文档 ID 集合中已生成摘要的文档（docID -> summary）。
// 供上下文渐进式披露注入摘要层。
func (m *TaskManager) GetDocumentSummaries(docIDs []string) map[string]string {
	if len(docIDs) == 0 {
		return nil
	}
	var docs []model.Document
	if err := m.db.Select("id", "summary").Where("id IN ?", docIDs).Find(&docs).Error; err != nil {
		return nil
	}
	out := make(map[string]string, len(docs))
	for _, d := range docs {
		if d.Summary != "" {
			out[d.ID] = d.Summary
		}
	}
	return out
}

// UpdateDocument 更新文档状态。
func (m *TaskManager) UpdateDocument(d *model.Document) error {
	return m.db.Save(d).Error
}

// RequestDocumentDelete 发起文档删除：先把文档置为 deleting（保证中断可恢复），再入队异步清理。
// 三段式清理（Milvus / Neo4j / MySQL）由 Worker 的 doc:delete 任务完成，失败可重试，不阻塞请求。
func (m *TaskManager) RequestDocumentDelete(docID string) error {
	res := m.db.Model(&model.Document{}).
		Where("id = ?", docID).
		Update("status", "deleting")
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected == 0 {
		return gorm.ErrRecordNotFound
	}
	return m.EnqueueDocDelete(docID)
}

// EnqueueDocDelete 将文档清理任务入队（低优先级队列）。
func (m *TaskManager) EnqueueDocDelete(docID string) error {
	payload, err := json.Marshal(map[string]string{"doc_id": docID})
	if err != nil {
		return err
	}
	task := asynq.NewTask("doc:delete", payload)
	_, err = m.asynqClient.Enqueue(task, asynq.Queue("low"), asynq.MaxRetry(5))
	return err
}

// DeleteDocumentRow 物理删除文档行（三层清理完成后的最后一步）。
func (m *TaskManager) DeleteDocumentRow(docID string) error {
	return m.db.Where("id = ?", docID).Delete(&model.Document{}).Error
}

// DecrementKnowledgeBaseDocCount 原子递减知识库文档计数（下限为 0）。
func (m *TaskManager) DecrementKnowledgeBaseDocCount(kbID string) {
	if kbID == "" {
		return
	}
	if err := m.db.Model(&model.KnowledgeBase{}).
		Where("id = ? AND doc_count > 0", kbID).
		UpdateColumn("doc_count", gorm.Expr("doc_count - 1")).Error; err != nil {
		slog.Error("decrement doc_count failed", "kb_id", kbID, "error", err)
	}
}

// ListStaleDeletingDocumentIDs 返回更新时间早于 cutoff 且仍处于 deleting 状态的文档 ID（限量）。
// 供周期扫描做删除补偿：删除任务重试耗尽后，由扫描重新投递以保证最终一致。
func (m *TaskManager) ListStaleDeletingDocumentIDs(cutoff time.Time, limit int) []string {
	var ids []string
	q := m.db.Model(&model.Document{}).
		Where("status = ? AND updated_at < ?", "deleting", cutoff).
		Order("updated_at asc")
	if limit > 0 {
		q = q.Limit(limit)
	}
	if err := q.Pluck("id", &ids).Error; err != nil {
		slog.Error("list stale deleting documents failed", "error", err)
		return nil
	}
	return ids
}

// ListStaleDeletingKnowledgeBaseIDs 返回更新时间早于 cutoff 且仍处于 deleting 状态的知识库 ID（限量）。
func (m *TaskManager) ListStaleDeletingKnowledgeBaseIDs(cutoff time.Time, limit int) []string {
	var ids []string
	q := m.db.Model(&model.KnowledgeBase{}).
		Where("status = ? AND updated_at < ?", "deleting", cutoff).
		Order("updated_at asc")
	if limit > 0 {
		q = q.Limit(limit)
	}
	if err := q.Pluck("id", &ids).Error; err != nil {
		slog.Error("list stale deleting knowledge bases failed", "error", err)
		return nil
	}
	return ids
}

// EnqueueKBBuild 将知识库文档构建任务入队（低优先级队列）。
func (m *TaskManager) EnqueueKBBuild(kbID, docID string) error {
	payload, err := json.Marshal(map[string]string{"kb_id": kbID, "doc_id": docID})
	if err != nil {
		return err
	}
	task := asynq.NewTask("kb:build", payload)
	// 显式收紧 kb:build 的重试上限：默认约 25 次重试会反复重跑 LLM 实体抽取 + embedding，
	// 成本被队列级重试放大。asynq 层仅作传输级兜底，业务重试以预算为真上限。
	_, err = m.asynqClient.Enqueue(task, asynq.Queue("low"), asynq.MaxRetry(3))
	return err
}

// ReplaceAgentTools 全量替换 Agent 绑定的工具。
func (m *TaskManager) ReplaceAgentTools(agentID string, toolIDs []string) error {
	return m.db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Where("agent_id = ?", agentID).Delete(&model.AgentTool{}).Error; err != nil {
			return err
		}
		for _, toolID := range toolIDs {
			if err := tx.Create(&model.AgentTool{AgentID: agentID, ToolID: toolID}).Error; err != nil {
				return err
			}
		}
		return nil
	})
}

// ReplaceAgentKnowledgeBases 全量替换 Agent 绑定的知识库。
func (m *TaskManager) ReplaceAgentKnowledgeBases(agentID string, kbIDs []string) error {
	return m.db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Where("agent_id = ?", agentID).Delete(&model.AgentKnowledge{}).Error; err != nil {
			return err
		}
		for _, kbID := range kbIDs {
			if err := tx.Create(&model.AgentKnowledge{AgentID: agentID, KnowledgeBaseID: kbID}).Error; err != nil {
				return err
			}
		}
		return nil
	})
}
