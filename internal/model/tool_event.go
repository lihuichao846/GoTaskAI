package model

import "time"

// ToolCallEvent 表示一次工具调用的过程事件，用于 SSE 实时推送运行可视化。
type ToolCallEvent struct {
	TaskID    string    `json:"task_id"`
	TraceID   string    `json:"trace_id"`
	UserID    uint      `json:"user_id"`
	AgentID   string    `json:"agent_id"`
	ToolName  string    `json:"tool_name"`
	Arguments string    `json:"arguments"`
	Status    string    `json:"status"` // started / success / failed
	Result    string    `json:"result"`
	Error     string    `json:"error"`
	Timestamp time.Time `json:"timestamp"`
}
