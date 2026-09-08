package model

import "time"

// RunLog 记录一次任务运行的观测信息（模型调用次数、token 消耗、成本、耗时等）。
// 与 Task 的最终状态解耦，用于可观测性与成本分析。
type RunLog struct {
	ID        string    `json:"id" gorm:"primaryKey;type:varchar(36)"`
	TaskID    string    `json:"task_id" gorm:"index;type:varchar(36)"`
	TraceID   string    `json:"trace_id" gorm:"index;type:varchar(36)"`
	UserID    uint      `json:"user_id" gorm:"index"`
	AgentID   string    `json:"agent_id" gorm:"index;type:varchar(36)"`
	Model     string    `json:"model" gorm:"type:varchar(100)"`
	Status    string    `json:"status" gorm:"type:varchar(20);index"` // completed / failed / cancelled
	Calls     int       `json:"calls"`
	TokensIn  int       `json:"tokens_in"`
	TokensOut int       `json:"tokens_out"`
	CostUSD   float64   `json:"cost_usd"`
	Duration  int64     `json:"duration_ms"`
	Error     string    `json:"error,omitempty" gorm:"type:text"`
	CreatedAt time.Time `json:"created_at"`
}
