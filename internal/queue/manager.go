package queue

import (
	"context"
	"encoding/json"
	"errors"
	"gotaskai/internal/model"
	"sync"
	"time"

	"github.com/redis/go-redis/v9"
	"gorm.io/gorm"
)

// TaskManager 负责管理任务状态和任务队列
type TaskManager struct {
	db          *gorm.DB         // MySQL 数据库实例，用于持久化
	rdb         *redis.Client    // Redis 客户端，用于缓存任务状态
	ctx         context.Context  // Redis 所需的上下文
	HighQueue   chan *model.Task // 高优先级队列
	NormalQueue chan *model.Task // 普通优先级队列
	LowQueue    chan *model.Task // 低优先级队列

	// SSE 相关的订阅管理
	clientsMu sync.RWMutex
	clients   map[uint]map[chan *model.Task]bool // userID -> set of channels
}

// NewTaskManager 创建并初始化一个任务管理器
func NewTaskManager(queueSize int, db *gorm.DB, rdb *redis.Client) *TaskManager {
	return &TaskManager{
		db:          db,
		rdb:         rdb,
		ctx:         context.Background(),
		HighQueue:   make(chan *model.Task, queueSize),
		NormalQueue: make(chan *model.Task, queueSize),
		LowQueue:    make(chan *model.Task, queueSize),
		clients:     make(map[uint]map[chan *model.Task]bool),
	}
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

// Enqueue 将任务推入内存队列 (公开方法，以便重试时调用)
func (m *TaskManager) Enqueue(t *model.Task) error {
	var targetQueue chan *model.Task
	switch t.Priority {
	case model.PriorityHigh:
		targetQueue = m.HighQueue
	case model.PriorityLow:
		targetQueue = m.LowQueue
	default:
		targetQueue = m.NormalQueue
	}

	select {
	case targetQueue <- t:
		return nil
	default:
		// 如果队列已满，则拒绝接收新任务
		return errors.New("queue is full")
	}
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
