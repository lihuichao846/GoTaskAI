package model

import "time"

// TaskStatus 定义任务的生命周期状态
type TaskStatus string

const (
	StatusPending    TaskStatus = "pending"    // 任务在队列中等待处理
	StatusProcessing TaskStatus = "processing" // 任务正在被 Worker 处理中
	StatusCompleted  TaskStatus = "completed"  // 任务处理成功
	StatusFailed     TaskStatus = "failed"     // 任务处理失败（重试耗尽）
	StatusCancelled  TaskStatus = "cancelled"  // 任务被用户主动取消
)

// TaskType 定义系统支持的 AI 任务类型
type TaskType string

const (
	TypeCustom TaskType = "custom" // 通用自定义对话任务
)

// TaskPriority 定义任务优先级
type TaskPriority int

const (
	PriorityLow    TaskPriority = 1
	PriorityNormal TaskPriority = 2
	PriorityHigh   TaskPriority = 3
)

// Task 代表系统中的一个具体任务实例，并映射到 MySQL 数据库表中
type Task struct {
	ID           string       `json:"id" gorm:"primaryKey;type:varchar(36)"`    // 任务的全局唯一标识符 (UUID)
	UserID       uint         `json:"user_id" gorm:"index"`                     // 归属的用户 ID
	SessionID    string       `json:"session_id" gorm:"index;type:varchar(50)"` // 所属会话ID，用于关联上下文
	Type         TaskType     `json:"type" gorm:"type:varchar(50)"`             // 任务类型
	SystemPrompt string       `json:"system_prompt" gorm:"type:text"`           // AI 的系统角色设定 (可选)
	Priority     TaskPriority `json:"priority" gorm:"type:int;default:2"`       // 任务优先级 (1:低, 2:普通, 3:高)
	Payload      string       `json:"payload" gorm:"type:text"`                 // 任务的输入数据/请求内容
	Result       string       `json:"result,omitempty" gorm:"type:text"`        // 任务成功后的处理结果
	Status       TaskStatus   `json:"status" gorm:"type:varchar(20);index"`     // 任务当前状态，增加索引以便快速查询
	Error        string       `json:"error,omitempty" gorm:"type:text"`         // 任务失败时的错误信息
	Retries      int          `json:"retries"`                                  // 当前已重试的次数
	MaxRetry     int          `json:"max_retry"`                                // 允许的最大重试次数
	CreatedAt    time.Time    `json:"created_at" gorm:"index"`                  // 任务创建时间，增加索引用于按时间排序
	UpdatedAt    time.Time    `json:"updated_at"`                               // 任务状态最后更新时间
}
