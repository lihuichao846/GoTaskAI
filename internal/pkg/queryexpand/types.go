// Package queryexpand 实现知识库「查询扩展」原型：在检索前对用户原始查询做
// 语义扩展、同义词扩展、实体链接扩展与多模态关联扩展，产出一组供 dense 多路召回
// 的改写变体，以及一组追加到 sparse（BM25）召回的加权扩展词。
//
// 设计原则：
//   - 所有扩展器（Expander）通过接口注入，便于用内存 fake 做确定性单元测试，
//     也便于在生产环境替换为 LLM / Neo4j / CLIP 的真实实现。
//   - 协调器（Coordinator）负责去重、加权、上限裁剪与安全兜底，保证扩展结果
//     不失控（词数、变体数、字符长度均有门禁）。
package queryexpand

import (
	"context"
	"sort"
	"strings"
)

// Term 表示一个带权重的扩展词，用于追加到 sparse（BM25）查询以增强词法召回。
type Term struct {
	Text   string  // 扩展词原文（归一化前）
	Weight float32 // 权重，越大越优先保留
	Source string  // 来源：synonym / entity / multimodal / semantic
}

// ExpandedQuery 是查询扩展的最终产物，供检索侧消费。
type ExpandedQuery struct {
	Original string   // 原始查询（不变）
	Variants []string // 语义改写变体（不含原始查询），供 dense 多路召回 + RRF 融合
	Terms    []Term   // 追加到 sparse 召回的加权扩展词（已去重、排序、裁剪）
}

// AllQueries 返回去重后的全部查询串（原始查询在首位），供 dense 召回逐条嵌入。
func (q ExpandedQuery) AllQueries() []string {
	out := make([]string, 0, 1+len(q.Variants))
	seen := map[string]bool{}
	for _, s := range append([]string{q.Original}, q.Variants...) {
		n := normalizeTerm(s)
		if n == "" || seen[n] {
			continue
		}
		seen[n] = true
		out = append(out, s)
	}
	return out
}

// Config 为查询扩展流水线配置。
type Config struct {
	Enabled    bool
	Semantic   SemanticConfig
	Synonym    SynonymConfig
	Entity     EntityConfig
	Multimodal MultimodalConfig
	Limits     Limits
}

type SemanticConfig struct {
	Enabled     bool
	MaxVariants int
}

type SynonymConfig struct {
	Enabled  bool
	MaxTerms int
}

type EntityConfig struct {
	Enabled     bool
	MaxEntities int
	MaxAliases  int
	GraphDepth  int
}

type MultimodalConfig struct {
	Enabled bool
	MaxTags int
}

type Limits struct {
	MaxTotalTerms   int
	MaxVariantChars int
}

// DefaultConfig 返回一套保守默认值：仅启用确定性、零外部依赖的扩展器
// （同义词 + 实体链接），语义与多模态默认关闭（需 LLM / CLIP，成本与延迟更高）。
func DefaultConfig() Config {
	return Config{
		Enabled: true,
		Semantic: SemanticConfig{
			Enabled:     false,
			MaxVariants: 3,
		},
		Synonym: SynonymConfig{
			Enabled:  true,
			MaxTerms: 8,
		},
		Entity: EntityConfig{
			Enabled:     true,
			MaxEntities: 5,
			MaxAliases:  3,
			GraphDepth:  1,
		},
		Multimodal: MultimodalConfig{
			Enabled: false,
			MaxTags: 4,
		},
		Limits: Limits{
			MaxTotalTerms:   20,
			MaxVariantChars: 256,
		},
	}
}

// Expander 是单个扩展器的统一接口：输入原始查询，输出加权扩展词与语义改写变体。
type Expander interface {
	Name() string
	Expand(ctx context.Context, query string) (terms []Term, variants []string)
}

// SynonymProvider 提供词项的同义词集合（字典式同义词扩展的注入点）。
type SynonymProvider interface {
	// Terms 返回字典中所有可匹配的词项（用于在查询中做包含匹配）。
	Terms() []string
	// Synonyms 返回 term 的同义词（不含 term 自身），无则返回 nil。
	Synonyms(term string) []string
}

// VariantGenerator 生成查询的语义改写变体（LLM 或规则实现的注入点）。
type VariantGenerator interface {
	Variants(ctx context.Context, query string, maxN int) ([]string, error)
}

// GraphResolver 解析实体与关联实体（Neo4j KAG 图或内存实现的注入点）。
// 实体链接扩展需要：用表面形式（规范名 + 别名）在查询中识别实体提及，
// 再归一化到规范实体，并追加其别名与关联实体。
type GraphResolver interface {
	// SurfaceForms 返回所有可用于查询匹配的表面形式（实体名 + 别名）。
	SurfaceForms() []string
	// Canonical 返回表面形式 surface 对应的规范实体名（无映射时返回原值）。
	Canonical(surface string) string
	// Aliases 返回规范实体 entity 的别名列表（不含 entity 自身）。
	Aliases(entity string) []string
	// Related 返回规范实体 entity 的 depth 跳关联实体名。
	Related(ctx context.Context, entity string, depth int) []string
}

// MultimodalProvider 提供查询视觉意图关键词对应的描述/标签扩展词（预留）。
type MultimodalProvider interface {
	VisualTerms(query string) []Term
}

// normalizeTerm 归一化词项：去首尾空白并转小写，作为去重 key。
func normalizeTerm(s string) string {
	return strings.ToLower(strings.TrimSpace(s))
}

// dedupTerms 按归一化文本去重，并对同词多来源的权重求和（封顶 2.0），
// 最后按权重降序、词字典序稳定排序。
func dedupTerms(terms []Term) []Term {
	if len(terms) == 0 {
		return nil
	}
	idx := make(map[string]int, len(terms))
	out := make([]Term, 0, len(terms))
	for _, t := range terms {
		key := normalizeTerm(t.Text)
		if key == "" {
			continue
		}
		if i, ok := idx[key]; ok {
			w := out[i].Weight + t.Weight
			if w > 2.0 {
				w = 2.0
			}
			out[i].Weight = w
			continue
		}
		idx[key] = len(out)
		t.Text = key
		out = append(out, t)
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Weight != out[j].Weight {
			return out[i].Weight > out[j].Weight
		}
		return out[i].Text < out[j].Text
	})
	return out
}

// truncateRunes 按 rune 截断字符串，避免破坏多字节字符；超长追加省略号。
func truncateRunes(s string, max int) string {
	if max <= 0 || len(s) <= max {
		return s
	}
	runes := []rune(s)
	if len(runes) <= max {
		return s
	}
	return string(runes[:max]) + "..."
}
