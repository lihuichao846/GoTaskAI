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

// Task 代表系统中的一个具体任务实例，并映射到 MySQL 数据库表中。
//
// 索引说明：复合索引 idx_tasks_conv_status_created (conversation_id, status, created_at)
// 服务"会话历史装载/压缩"链路。列序遵循「等值条件在前、范围与排序在后」：
//
//	WHERE (conversation_id = ? OR id = ?) AND status = ? AND created_at > ? ORDER BY created_at
//
// 的 conversation_id 分支可全走索引且免 filesort；COUNT(*) 也能仅扫索引不回表。
// 注意：`id = ?` 兼容分支不进该索引（id 是主键），优化器可能退化为 index_merge 或主键扫描。
type Task struct {
	ID             string       `json:"id" gorm:"primaryKey;type:varchar(36)"`                                                        // 任务的全局唯一标识符 (UUID)
	TraceID        string       `json:"trace_id" gorm:"index;type:varchar(36)"`                                                       // 分布式追踪 ID，随 Asynq payload 与工具事件透传
	UserID         uint         `json:"user_id" gorm:"index"`                                                                         // 归属的用户 ID
	SessionID      string       `json:"session_id" gorm:"index;type:varchar(50)"`                                                     // 所属会话ID，用于关联上下文
	ConversationID string       `json:"conversation_id" gorm:"index;index:idx_tasks_conv_status_created,priority:1;type:varchar(36)"` // 关联的会话记录 ID（新模型，兼容 SessionID）
	AgentID        string       `json:"agent_id" gorm:"index;type:varchar(36)"`                                                       // 关联的 Agent 配置 ID（可空）
	Type           TaskType     `json:"type" gorm:"type:varchar(50)"`                                                                 // 任务类型
	SystemPrompt   string       `json:"system_prompt" gorm:"type:text"`                                                               // AI 的系统角色设定 (可选)
	Priority       TaskPriority `json:"priority" gorm:"type:int;default:2"`                                                           // 任务优先级 (1:低, 2:普通, 3:高)
	Payload        string       `json:"payload" gorm:"type:text"`                                                                     // 任务的输入数据/请求内容
	Result         string       `json:"result,omitempty" gorm:"type:text"`                                                            // 任务成功后的处理结果
	Status         TaskStatus   `json:"status" gorm:"type:varchar(20);index;index:idx_tasks_conv_status_created,priority:2"`          // 任务当前状态，增加索引以便快速查询
	Error          string       `json:"error,omitempty" gorm:"type:text"`                                                             // 任务失败时的错误信息
	Retries        int          `json:"retries"`                                                                                      // 当前已重试的次数
	MaxRetry       int          `json:"max_retry"`                                                                                    // 允许的最大重试次数
	TotalCalls     int          `json:"total_calls"`                                                                                  // 累计模型调用次数（跨重试，用于任务级成本上限）
	TotalTokensIn  int          `json:"total_tokens_in"`                                                                              // 累计输入 token（跨重试）
	TotalTokensOut int          `json:"total_tokens_out"`                                                                             // 累计输出 token（跨重试）
	CreatedAt      time.Time    `json:"created_at" gorm:"index;index:idx_tasks_conv_status_created,priority:3"`                       // 任务创建时间，增加索引用于按时间排序
	UpdatedAt      time.Time    `json:"updated_at"`                                                                                   // 任务状态最后更新时间
}
