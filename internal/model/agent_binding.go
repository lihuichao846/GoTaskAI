package model

// AgentTool 表示 Agent 与工具的多对多绑定关系（白名单）。
type AgentTool struct {
	AgentID string `json:"agent_id" gorm:"primaryKey;type:varchar(36)"`
	ToolID  string `json:"tool_id" gorm:"primaryKey;type:varchar(36)"`
}

// AgentKnowledge 表示 Agent 与知识库的多对多绑定关系。
type AgentKnowledge struct {
	AgentID         string `json:"agent_id" gorm:"primaryKey;type:varchar(36)"`
	KnowledgeBaseID string `json:"knowledge_base_id" gorm:"primaryKey;type:varchar(36)"`
}
