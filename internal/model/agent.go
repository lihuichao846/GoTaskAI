package model

import "time"

// Agent 代表用户构建的一个智能体，即「持久化的可复用配置」。
// 相比每次请求临时拼 Prompt，Agent 把 system_prompt、模型等配置固化下来。
type Agent struct {
	ID           string    `json:"id" gorm:"primaryKey;type:varchar(36)"`
	UserID       uint      `json:"user_id" gorm:"index"`                       // 归属用户，用于多租户隔离
	Name         string    `json:"name" gorm:"type:varchar(100)"`              // Agent 名称
	SystemPrompt string    `json:"system_prompt" gorm:"type:text"`             // 系统角色设定
	Model        string    `json:"model" gorm:"type:varchar(100)"`             // 覆盖平台默认模型，为空则用默认
	Status       string    `json:"status" gorm:"type:varchar(20);index"`       // active/archived
	CreatedAt    time.Time `json:"created_at"`
	UpdatedAt    time.Time `json:"updated_at"`
}
