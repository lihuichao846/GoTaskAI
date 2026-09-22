package worker

import (
	"strings"
	"testing"
	"time"

	"gotaskai/internal/config"
	"gotaskai/internal/model"
	"gotaskai/internal/pkg/llm"
)

// TestMemoryRelevance 校验词面重叠打分：相关 > 0、无关 = 0。
// 这是 Phase A（无向量检索）唯一的"相关性判断"，判断失效就等于把无关记忆注入上下文。
func TestMemoryRelevance(t *testing.T) {
	mem := "用户常用 Go 语言写后端服务"

	if got := memoryRelevance(mem, "我常用什么语言写后端？"); got <= 0 {
		t.Fatalf("相关问题的重叠度应大于 0，实际 %v", got)
	}
	if got := memoryRelevance(mem, "今天杭州天气怎么样"); got != 0 {
		t.Fatalf("无关问题的重叠度应为 0，实际 %v", got)
	}
	if got := memoryRelevance("", "任意问题"); got != 0 {
		t.Fatalf("空记忆的重叠度应为 0，实际 %v", got)
	}
	if got := memoryRelevance(mem, ""); got != 0 {
		t.Fatalf("空问题的重叠度应为 0，实际 %v", got)
	}
}

// TestMemoryRelevanceEnglishWord 校验 ASCII 词按整词匹配（大小写不敏感）。
func TestMemoryRelevanceEnglishWord(t *testing.T) {
	if got := memoryRelevance("用户使用 Kubernetes 部署", "kubernetes 怎么调优"); got <= 0 {
		t.Fatalf("英文词应能匹配（大小写不敏感），实际 %v", got)
	}
	// 单字母不计入匹配单元，避免"到处都命中"
	if got := memoryRelevance("a", "a"); got != 0 {
		t.Fatalf("单字符不应作为匹配单元，实际 %v", got)
	}
}

// TestSelectRelevantMemories 校验选材：丢掉零重叠、按相关性排序、尊重条数上限。
func TestSelectRelevantMemories(t *testing.T) {
	now := time.Now()
	mems := []*model.UserMemory{
		{ID: "1", Content: "用户常用 Go 语言写后端服务", Confidence: 0.9, UpdatedAt: now},
		{ID: "2", Content: "用户偏好简洁回答", Confidence: 1.0, UpdatedAt: now},
		{ID: "3", Content: "用户上个月去过杭州", Confidence: 0.8, UpdatedAt: now},
	}

	got := selectRelevantMemories(mems, "我常用什么语言写后端", 10)
	if len(got) != 1 || got[0].ID != "1" {
		t.Fatalf("应只命中记忆 1，实际 %v", ids(got))
	}

	// 上限生效
	got = selectRelevantMemories(mems, "用户的偏好与常用语言", 1)
	if len(got) != 1 {
		t.Fatalf("应被 maxItems 截断为 1 条，实际 %d", len(got))
	}

	// 全部无关时返回空（宁可不注入）
	got = selectRelevantMemories(mems, "量子力学是什么", 10)
	if len(got) != 0 {
		t.Fatalf("无关问题不应召回任何记忆，实际 %v", ids(got))
	}
}

// TestSelectRelevantMemoriesOrdering 同分时按置信度、再按更新时间排序。
func TestSelectRelevantMemoriesOrdering(t *testing.T) {
	base := time.Now().Add(-time.Hour)
	mems := []*model.UserMemory{
		{ID: "low", Content: "用户偏好简洁", Confidence: 0.5, UpdatedAt: base},
		{ID: "high", Content: "用户偏好简洁", Confidence: 0.9, UpdatedAt: base},
		{ID: "new", Content: "用户偏好简洁", Confidence: 0.5, UpdatedAt: time.Now()},
	}
	got := selectRelevantMemories(mems, "用户偏好", 10)
	if len(got) != 3 {
		t.Fatalf("应召回 3 条，实际 %d", len(got))
	}
	if got[0].ID != "high" {
		t.Fatalf("同分时应按置信度优先，实际首位 %s", got[0].ID)
	}
	if got[1].ID != "new" {
		t.Fatalf("置信度相同时应按更新时间优先，实际次位 %s", got[1].ID)
	}
}

// TestRenderMemoryBlockTokenBudget 校验渲染的 token 上限：装不下就不装，且不返回半截表头。
func TestRenderMemoryBlockTokenBudget(t *testing.T) {
	mems := []*model.UserMemory{
		{ID: "1", Content: strings.Repeat("很长的记忆内容", 20)},
		{ID: "2", Content: "短记忆"},
	}

	// 预算极小：一条都装不下 → 返回空串（不能只留下表头）
	if got, used := renderMemoryBlock(mems, memoryProfileHeader, 1); got != "" || used != 0 {
		t.Fatalf("预算不足时应返回空块，实际 len=%d used=%d", len(got), used)
	}

	// 预算充足：两条都进，且返回的 token 数与文本一致
	got, used := renderMemoryBlock(mems, memoryProfileHeader, 10000)
	if !strings.Contains(got, "短记忆") || !strings.Contains(got, memoryProfileHeader) {
		t.Fatalf("充足预算下应完整渲染，实际 %q", got)
	}
	if used != llm.EstimateTokens(got) {
		t.Fatalf("返回的 used 应与文本 token 估算一致：used=%d est=%d", used, llm.EstimateTokens(got))
	}

	// 空列表 → 空块
	if got, used := renderMemoryBlock(nil, memoryProfileHeader, 10000); got != "" || used != 0 {
		t.Fatalf("空列表应返回空块，实际 %q used=%d", got, used)
	}
}

// TestLoadBudgetTokensDeductsMemory 锁死"记忆不得挤压历史装载预算"这条验收判据：
// 召回块占用的 token 必须从装载预算中扣除，扣多少就得少多少。
func TestLoadBudgetTokensDeductsMemory(t *testing.T) {
	lc := config.LLMConfig{ContextWindow: 100000}
	cc := config.CompressConfig{BudgetRatio: 0.6, MaxLoadTokens: 100000}

	base := loadBudgetTokens("system", "user", "", nil, 0, lc, cc)
	withMemory := loadBudgetTokens("system", "user", "", nil, 500, lc, cc)
	if base-withMemory != 500 {
		t.Fatalf("记忆 token 未被完整扣除：base=%d withMemory=%d", base, withMemory)
	}

	// 记忆超大时预算应夹到 0 而不是负数
	if got := loadBudgetTokens("system", "user", "", nil, 1<<30, lc, cc); got != 0 {
		t.Fatalf("预算应被夹到 0，实际 %d", got)
	}
}

// TestLoadBudgetTokensDeductsProfile 偏好块是拼进 systemPrompt 注入的，
// 因此它必须（通过 systemPrompt 的估算）间接体现在预算里。
func TestLoadBudgetTokensDeductsProfile(t *testing.T) {
	lc := config.LLMConfig{ContextWindow: 100000}
	cc := config.CompressConfig{BudgetRatio: 0.6, MaxLoadTokens: 100000}

	plain := loadBudgetTokens("system", "user", "", nil, 0, lc, cc)
	withProfile := loadBudgetTokens("system"+memoryProfileHeader+"- 用户偏好简洁回答", "user", "", nil, 0, lc, cc)
	if withProfile >= plain {
		t.Fatalf("注入偏好块后预算应变小：plain=%d withProfile=%d", plain, withProfile)
	}
}

func ids(mems []*model.UserMemory) []string {
	out := make([]string, 0, len(mems))
	for _, m := range mems {
		out = append(out, m.ID)
	}
	return out
}
