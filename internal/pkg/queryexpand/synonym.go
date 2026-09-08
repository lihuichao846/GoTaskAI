package queryexpand

import (
	"context"
	"strings"
)

// SynonymExpander 基于字典的同义词扩展：对字典中出现在查询里的词项，
// 追加其同义词作为加权扩展词，用于缓解查询与文档之间的「词法鸿沟」。
// 该扩展器确定性、零外部依赖、近零延迟，是召回增强的性价比首选。
type SynonymExpander struct {
	provider SynonymProvider
	maxTerms int
}

// NewSynonymExpander 构建同义词扩展器；maxTerms<=0 时回退默认 8。
func NewSynonymExpander(provider SynonymProvider, maxTerms int) *SynonymExpander {
	if maxTerms <= 0 {
		maxTerms = 8
	}
	return &SynonymExpander{provider: provider, maxTerms: maxTerms}
}

func (e *SynonymExpander) Name() string { return "synonym" }

// Expand 在查询中做包含匹配（最长优先），命中词项的同义词追加为扩展词。
func (e *SynonymExpander) Expand(_ context.Context, query string) ([]Term, []string) {
	if e.provider == nil {
		return nil, nil
	}
	// 按字典词项长度降序做包含匹配，避免短词先命中后覆盖长词。
	dict := e.provider.Terms()
	sortByLenDesc(dict)

	lower := strings.ToLower(query)
	seenKey := map[string]bool{}
	var terms []Term
	for _, term := range dict {
		key := normalizeTerm(term)
		if key == "" || seenKey[key] {
			continue
		}
		// 仅当词项是查询中的独立词（拉丁）或子串（CJK）时命中，降低误扩展。
		if !containsToken(lower, key) {
			continue
		}
		seenKey[key] = true
		for _, syn := range e.provider.Synonyms(term) {
			if n := normalizeTerm(syn); n != "" && n != key {
				terms = append(terms, Term{Text: n, Weight: 0.9, Source: "synonym"})
			}
		}
	}
	if len(terms) > e.maxTerms {
		terms = terms[:e.maxTerms]
	}
	return terms, nil
}

// sortByLenDesc 按字符串 rune 长度降序排序（稳定，不改变等长项的相对顺序）。
func sortByLenDesc(items []string) {
	// 使用简单冒泡保证稳定且无需 import sort 的额外函数（数量小）。
	for i := 0; i < len(items); i++ {
		for j := i + 1; j < len(items); j++ {
			if len([]rune(items[j])) > len([]rune(items[i])) {
				items[i], items[j] = items[j], items[i]
			}
		}
	}
}

// containsToken 判断 key 是否作为「词」出现在 text 中：
//   - 纯 ASCII 词按空白/标点边界匹配，避免 "car" 误命中 "carbon"；
//   - 含 CJK 时按子串匹配（字典词通常为完整中文词）。
func containsToken(text, key string) bool {
	if key == "" {
		return false
	}
	if isASCII(key) {
		for _, tok := range tokenizeASCII(text) {
			if tok == key {
				return true
			}
		}
		return false
	}
	return strings.Contains(text, key)
}

// isASCII 判断字符串是否全部由 ASCII 字母/数字组成。
func isASCII(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] >= 0x80 {
			return false
		}
	}
	return true
}

// tokenizeASCII 将 ASCII 文本按非字母数字字符切分为小写词。
func tokenizeASCII(s string) []string {
	return strings.FieldsFunc(strings.ToLower(s), func(r rune) bool {
		return !(r >= 'a' && r <= 'z' || r >= '0' && r <= '9')
	})
}
