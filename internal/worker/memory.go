package worker

import (
	"sort"
	"strings"
	"time"
	"unicode"

	"gotaskai/internal/config"
	"gotaskai/internal/metrics"
	"gotaskai/internal/model"
	"gotaskai/internal/pkg/llm"
)

// 用户向长期记忆的【装配层】。
//
// 与 queue/memory.go 的分工：那里只负责存取与作用域安全；这里负责"取哪些、怎么排、
// 放多少 token、拼成什么文本"。所有纯函数（打分/选材/渲染）都不依赖 DB，可直接单测。
//
// 注入位置（与上下文改造的分工）：
//   - 偏好块（长期稳定）→ 拼进 systemPrompt；
//   - 召回块（随问题变化）→ 作为一条独立 system 消息插在 history 之后、user 之前。
//
// 这样做的理由：召回块每轮都可能不同，若拼进 system prompt 尾部会把前缀缓存彻底打散
// （DeepSeek 命中价约为未命中价的 1/120）；而"独立 system 消息"在项目里已有先例
// ——滚动摘要就是这样注入的（见 buildHistory）。

const (
	// memoryProfileHeader 偏好块的表头。偏好对任何问题都适用，故无条件注入。
	memoryProfileHeader = "[长期记忆 · 用户偏好]\n" +
		"以下是系统长期记住的该用户偏好，请在回答风格与取舍上予以遵守；若与当前对话冲突，以当前对话为准：\n"
	// memoryRecallHeader 召回块的表头。事实类记忆只在与当前问题相关时才注入。
	memoryRecallHeader = "[长期记忆 · 与本次问题相关]\n" +
		"以下是从该用户长期记忆中召回的条目，供参考；若与当前对话冲突，以当前对话为准：\n"
)

// isCJK 判断是否为中日韩表意文字（这些文字之间没有空格，需按二元组切分）。
func isCJK(r rune) bool {
	return unicode.Is(unicode.Han, r) || unicode.Is(unicode.Hiragana, r) || unicode.Is(unicode.Katakana, r)
}

// memoryUnits 把文本切成用于"词面重叠"的匹配单元：
// ASCII 词（长度 ≥2，小写化）与 CJK 二元组（bigram）。
//
// 之所以自造而不是引入分词器：这里只需要"有没有交集"的粗判，且零依赖、可单测；
// Phase C 引入向量召回后，本函数会被语义相似度取代。
func memoryUnits(s string) []string {
	var units []string
	var cjk, word []rune

	flushCJK := func() {
		switch {
		case len(cjk) >= 2:
			for i := 0; i+1 < len(cjk); i++ {
				units = append(units, string(cjk[i:i+2]))
			}
		case len(cjk) == 1:
			units = append(units, string(cjk))
		}
		cjk = cjk[:0]
	}
	flushWord := func() {
		if len(word) >= 2 {
			units = append(units, strings.ToLower(string(word)))
		}
		word = word[:0]
	}

	for _, r := range s {
		switch {
		case isCJK(r):
			flushWord()
			cjk = append(cjk, r)
		case unicode.IsLetter(r) || unicode.IsDigit(r):
			flushCJK()
			word = append(word, r)
		default:
			flushCJK()
			flushWord()
		}
	}
	flushCJK()
	flushWord()
	return units
}

// memoryRelevance 计算记忆与问题之间的词面重叠度（去重后的命中单元数 / 记忆侧单元数）。
// 返回 0 表示"完全没有共同用词"。
//
// 为什么 Phase A 必须有这道相关性判断：此时还没有语义检索，若不加判断，
// 召回就退化成"把该用户所有记忆都塞进上下文"——而注入无关记忆（记忆污染）
// 比不注入更糟，会让模型把不相关的信息当作当前语境。
func memoryRelevance(content, question string) float64 {
	memUnits := memoryUnits(content)
	if len(memUnits) == 0 {
		return 0
	}
	qUnits := memoryUnits(question)
	if len(qUnits) == 0 {
		return 0
	}
	set := make(map[string]struct{}, len(qUnits))
	for _, u := range qUnits {
		set[u] = struct{}{}
	}

	seen := make(map[string]struct{}, len(memUnits))
	hit := 0
	for _, u := range memUnits {
		if _, dup := seen[u]; dup {
			continue
		}
		seen[u] = struct{}{}
		if _, ok := set[u]; ok {
			hit++
		}
	}
	if hit == 0 {
		return 0
	}
	return float64(hit) / float64(len(seen))
}

// selectRelevantMemories 按相关性挑选要注入的记忆：先过滤掉零重叠的，
// 再按（相关性 → 置信度 → 更新时间）排序并截断。
//
// 零重叠一律不注入：这是"宁可不注入，也不注入无关记忆"的取舍。
func selectRelevantMemories(mems []*model.UserMemory, question string, maxItems int) []*model.UserMemory {
	type scored struct {
		mem *model.UserMemory
		s   float64
	}
	candidates := make([]scored, 0, len(mems))
	for _, m := range mems {
		if m == nil || strings.TrimSpace(m.Content) == "" {
			continue
		}
		s := memoryRelevance(m.Content, question)
		if s <= 0 {
			continue
		}
		candidates = append(candidates, scored{mem: m, s: s})
	}

	sort.SliceStable(candidates, func(i, j int) bool {
		if candidates[i].s != candidates[j].s {
			return candidates[i].s > candidates[j].s
		}
		if candidates[i].mem.Confidence != candidates[j].mem.Confidence {
			return candidates[i].mem.Confidence > candidates[j].mem.Confidence
		}
		return candidates[i].mem.UpdatedAt.After(candidates[j].mem.UpdatedAt)
	})

	if maxItems > 0 && len(candidates) > maxItems {
		candidates = candidates[:maxItems]
	}
	out := make([]*model.UserMemory, 0, len(candidates))
	for _, c := range candidates {
		out = append(out, c.mem)
	}
	return out
}

// renderMemoryBlock 把记忆列表渲染成可注入的文本块，并返回其 token 估算值。
// maxTokens <= 0 表示不限制。返回空串表示一条都没装下（调用方据此跳过注入）。
func renderMemoryBlock(mems []*model.UserMemory, header string, maxTokens int) (string, int) {
	if len(mems) == 0 {
		return "", 0
	}
	var sb strings.Builder
	sb.WriteString(header)
	used := llm.EstimateTokens(header)
	added := 0

	for _, m := range mems {
		content := strings.TrimSpace(m.Content)
		if content == "" {
			continue
		}
		line := "- " + content + "\n"
		cost := llm.EstimateTokens(line)
		if maxTokens > 0 && used+cost > maxTokens {
			break
		}
		sb.WriteString(line)
		used += cost
		added++
	}

	if added == 0 {
		return "", 0
	}
	// 返回"实际文本"的 token 数而非循环内的增量累加值：后者包含被 TrimRight 去掉的末尾换行，
	// 会让调用方多扣一点预算，且与文本对不上（单测会直接暴露这种不一致）。
	text := strings.TrimRight(sb.String(), "\n")
	return text, llm.EstimateTokens(text)
}

// loadUserMemory 装配本次任务的长期记忆，返回（偏好块, 召回块, 命中的记忆 ID）。
//
// 两条查询各自带 LIMIT，互不挤占：偏好是"基本不变、无条件注入"，事实/经历是"按相关性注入"。
// 记忆未启用时不做任何查询，行为与改造前完全一致。
func (p *Pool) loadUserMemory(t model.Task) (profile string, recall string, hitIDs []string) {
	mc := config.AppConfig.Memory
	if !mc.Enabled {
		return "", "", nil
	}

	maxItems := mc.MaxRecallItems
	if maxItems <= 0 {
		maxItems = 8
	}
	// 经历类记忆的时效窗：过期的经历不再召回（事实与偏好不受时间驱动，
	// 它们只能被"新事实取代"来失效）。
	var episodicSince time.Time
	if mc.EpisodicTTLDays > 0 {
		episodicSince = time.Now().AddDate(0, 0, -mc.EpisodicTTLDays)
	}

	// 偏好块：无条件注入，只受 token 上限约束。
	prefs := p.manager.RecallUserMemories(t.UserID, t.AgentID,
		[]string{model.MemoryTypePreference}, time.Time{}, maxItems)
	profile, profileTokens := renderMemoryBlock(prefs, memoryProfileHeader, mc.MaxProfileTokens)

	// 召回块：只在与当前问题有词面重叠时才注入。
	candidates := p.manager.RecallUserMemories(t.UserID, t.AgentID,
		[]string{model.MemoryTypeSemantic, model.MemoryTypeEpisodic, model.MemoryTypeProcedural},
		episodicSince, maxItems*4)
	selected := selectRelevantMemories(candidates, t.Payload, maxItems)
	recall, recallTokens := renderMemoryBlock(selected, memoryRecallHeader, mc.MaxRecallTokens)

	// 命中的记忆 ID 只含"按相关性召回"的那些：偏好块是无条件注入的，
	// 把它也算作命中会让 hit_count 每轮自增、失去信号价值。
	for _, m := range selected {
		hitIDs = append(hitIDs, m.ID)
	}

	metrics.MemoryRecallItems.Observe(float64(len(selected)))
	metrics.MemoryRecallTokens.Observe(float64(profileTokens + recallTokens))
	return profile, recall, hitIDs
}
