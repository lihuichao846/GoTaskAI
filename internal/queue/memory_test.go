package queue

import (
	"strings"
	"testing"
)

// TestMemoryScopeWhereFailClosed 锁死最重要的一条隐私约束：
// 拿不到 user_id 时必须【放弃查询】，绝不能退化成"查全部"或"查公共集合"。
// 对照：RAG 在 Agent 未绑定知识库时会回退公共知识检索（pool.go 的 retrieveKnowledge），
// 记忆不允许有这种回退——回退即跨用户泄漏。
func TestMemoryScopeWhereFailClosed(t *testing.T) {
	clause, args, ok := memoryScopeWhere(0, "")
	if ok {
		t.Fatalf("userID=0 必须 fail-closed，实际 ok=true clause=%q", clause)
	}
	if clause != "" || args != nil {
		t.Fatalf("fail-closed 时不应返回任何条件：clause=%q args=%v", clause, args)
	}
}

// TestMemoryScopeWhereContainsUser 断言作用域条件里【一定】带 user_id 过滤。
func TestMemoryScopeWhereContainsUser(t *testing.T) {
	clause, args, ok := memoryScopeWhere(42, "")
	if !ok {
		t.Fatal("合法 userID 不应 fail-closed")
	}
	if !strings.Contains(clause, "user_id = ?") {
		t.Fatalf("作用域条件缺少 user_id 过滤：%q", clause)
	}
	if len(args) != 1 || args[0].(uint) != uint(42) {
		t.Fatalf("作用域参数不正确：%v", args)
	}
}

// TestMemoryScopeWhereAgentScope 断言 Agent 维度只召回"专属 + 用户全局"两类，
// 即默认不在 Agent 之间共享记忆。
func TestMemoryScopeWhereAgentScope(t *testing.T) {
	clause, args, ok := memoryScopeWhere(7, "agent-a")
	if !ok {
		t.Fatal("合法作用域不应 fail-closed")
	}
	if !strings.Contains(clause, "agent_id IN ?") {
		t.Fatalf("带 Agent 的作用域缺少 agent_id 过滤：%q", clause)
	}
	if len(args) != 2 {
		t.Fatalf("期望 (user_id, agent 列表) 两个参数，实际 %v", args)
	}
	ids, isSlice := args[1].([]string)
	if !isSlice || len(ids) != 2 || ids[0] != "agent-a" || ids[1] != "" {
		t.Fatalf("Agent 作用域应为 [当前 Agent, 空串(全局)]，实际 %v", args[1])
	}
}

// TestMemoryScopeWhereEmptyAgent 断言不带 Agent 时不做 agent 过滤，
// 这样"该用户全局"的记忆在任何 Agent 的对话里都能被召回。
func TestMemoryScopeWhereEmptyAgent(t *testing.T) {
	clause, args, ok := memoryScopeWhere(7, "")
	if !ok {
		t.Fatal("合法作用域不应 fail-closed")
	}
	if strings.Contains(clause, "agent_id") {
		t.Fatalf("无 Agent 时不应附加 agent_id 条件：%q", clause)
	}
	if len(args) != 1 {
		t.Fatalf("期望仅 user_id 参数，实际 %v", args)
	}
}
