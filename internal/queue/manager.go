package queue

import (
	"context"
	"encoding/json"
	"gotaskai/internal/model"
	"sync"
	"time"

	"github.com/hibiken/asynq"
	"github.com/redis/go-redis/v9"
	"gorm.io/gorm"
)

// TaskManager 负责管理任务状态和任务队列
type TaskManager struct {
	db          *gorm.DB        // MySQL 数据库实例，用于持久化
	rdb         *redis.Client   // Redis 客户端，用于缓存任务状态
	asynqClient *asynq.Client   // Asynq 客户端，用于发布任务
	ctx         context.Context // Redis 所需的上下文

	// SSE 相关的订阅管理
	clientsMu sync.RWMutex
	clients   map[uint]map[chan *model.Task]bool // userID -> set of channels
}

// NewTaskManager 创建并初始化一个任务管理器
func NewTaskManager(db *gorm.DB, rdb *redis.Client, redisOpt asynq.RedisConnOpt) *TaskManager {
	return &TaskManager{
		db:          db,
		rdb:         rdb,
		asynqClient: asynq.NewClient(redisOpt),
		ctx:         context.Background(),
		clients:     make(map[uint]map[chan *model.Task]bool),
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

	// 3. 触发 SSE 广播，通知前端
	m.broadcast(t)
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
