package model

import "time"

// Tool 代表一个可被 Agent 调用的工具（内置或 MCP 外部工具）。
type Tool struct {
	ID          string    `json:"id" gorm:"primaryKey;type:varchar(36)"`
	UserID      uint      `json:"user_id" gorm:"index"` // 0 表示内置全局工具
	Name        string    `json:"name" gorm:"uniqueIndex;type:varchar(100)"`
	Type        string    `json:"type" gorm:"type:varchar(20)"` // builtin / mcp
	DisplayName string    `json:"display_name" gorm:"type:varchar(100)"`
	Description string    `json:"description" gorm:"type:text"`
	Config      string    `json:"config" gorm:"type:text"` // JSON：MCP 连接配置
	IsBuiltin   bool      `json:"is_builtin"`
	Status      string    `json:"status" gorm:"type:varchar(20);index"` // active / disabled
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
}

// ToolConfig 描述 MCP 工具的连接配置，支持两种连接方式：
//   - 本地子进程（stdio）：配置 Command + Args（可选 Env）
//   - 远程服务（SSE/HTTP）：配置 URL
type ToolConfig struct {
	Command string            `json:"command"`
	Args    []string          `json:"args"`
	URL     string            `json:"url"` // 远程 HTTP/SSE MCP 服务地址
	Env     map[string]string `json:"env"` // 传递给 stdio 子进程的额外环境变量
}

// Valid 判断配置是否提供了可用的连接方式。
func (c ToolConfig) Valid() bool {
	return c.Command != "" || c.URL != ""
}
