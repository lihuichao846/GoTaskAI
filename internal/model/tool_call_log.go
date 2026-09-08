package model

import "time"

// ToolCallLog 记录一次工具调用的观测信息，供可观测性与运行回溯。
type ToolCallLog struct {
	ID        string    `json:"id" gorm:"primaryKey;type:varchar(36)"`
	TaskID    string    `json:"task_id" gorm:"index;type:varchar(36)"`
	TraceID   string    `json:"trace_id" gorm:"index;type:varchar(36)"`
	UserID    uint      `json:"user_id" gorm:"index"`
	AgentID   string    `json:"agent_id" gorm:"index;type:varchar(36)"`
	ToolName  string    `json:"tool_name" gorm:"type:varchar(100);index"`
	Arguments string    `json:"arguments" gorm:"type:text"`
	Status    string    `json:"status" gorm:"type:varchar(20)"` // started / success / failed
	Result    string    `json:"result" gorm:"type:text"`
	Error     string    `json:"error" gorm:"type:text"`
	CreatedAt time.Time `json:"created_at"`
}
