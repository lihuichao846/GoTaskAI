package model

import "time"

// Conversation 代表用户与某个 Agent 之间的一段持续对话会话。
// 兼容策略：历史任务中的 SessionID 会作为 Conversation.ID 复用，
// 使旧的多轮对话链路平滑迁移到新的会话模型。
type Conversation struct {
	ID            string     `json:"id" gorm:"primaryKey;type:varchar(36)"`
	UserID        uint       `json:"user_id" gorm:"index"`                          // 归属用户
	AgentID       string     `json:"agent_id" gorm:"index;type:varchar(36)"`        // 关联的 Agent（可空）
	Title         string     `json:"title" gorm:"type:varchar(255)"`                // 会话标题（可空）
	Summary       string     `json:"summary" gorm:"type:longtext"`                  // 对话滚动摘要（上下文自动压缩时生成）
	SummaryCutoff *time.Time `json:"summary_cutoff,omitempty" gorm:"type:datetime"` // 摘要已覆盖到的最早任务创建时间（用于避免重复压缩）
	CreatedAt     time.Time  `json:"created_at"`
	UpdatedAt     time.Time  `json:"updated_at"`
}
