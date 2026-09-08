package queryexpand

import "context"

// MapSynonymProvider 基于 map 的同义词字典实现，供测试与离线评测使用。
// 键为规范词项，值为其同义词列表。
type MapSynonymProvider map[string][]string

func (m MapSynonymProvider) Terms() []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

func (m MapSynonymProvider) Synonyms(term string) []string { return m[term] }

// MapGraphResolver 基于内存图的实体解析实现，供测试与离线评测使用。
// canonicalOf 为「表面形式（规范名/别名）-> 规范名」映射；
// aliasesOf 为「规范名 -> 别名列表」；relatedOf 为「规范名 -> 关联实体列表」。
type MapGraphResolver struct {
	canonicalOf map[string]string
	aliasesOf   map[string][]string
	relatedOf   map[string][]string
}

// NewMapGraphResolver 构建内存图解析器。三个 map 均可为 nil（视为空）。
func NewMapGraphResolver(canonicalOf map[string]string, aliasesOf, relatedOf map[string][]string) *MapGraphResolver {
	if canonicalOf == nil {
		canonicalOf = map[string]string{}
	}
	if aliasesOf == nil {
		aliasesOf = map[string][]string{}
	}
	if relatedOf == nil {
		relatedOf = map[string][]string{}
	}
	return &MapGraphResolver{canonicalOf: canonicalOf, aliasesOf: aliasesOf, relatedOf: relatedOf}
}

func (r *MapGraphResolver) SurfaceForms() []string {
	out := make([]string, 0, len(r.canonicalOf))
	for k := range r.canonicalOf {
		out = append(out, k)
	}
	return out
}

func (r *MapGraphResolver) Canonical(surface string) string {
	if c, ok := r.canonicalOf[surface]; ok {
		return c
	}
	return surface
}

func (r *MapGraphResolver) Aliases(entity string) []string { return r.aliasesOf[entity] }

func (r *MapGraphResolver) Related(_ context.Context, entity string, depth int) []string {
	if depth <= 0 {
		return nil
	}
	return r.relatedOf[entity]
}

// MapMultimodalProvider 基于 map 的视觉关键词到描述/标签的实现，供测试与离线评测使用。
type MapMultimodalProvider map[string][]string

func (m MapMultimodalProvider) VisualTerms(query string) []Term {
	// 对每个视觉关键词做包含匹配，命中则追加其描述标签。
	var terms []Term
	for kw, tags := range m {
		if containsToken(normalizeTerm(query), normalizeTerm(kw)) {
			for _, t := range tags {
				terms = append(terms, Term{Text: t, Weight: 0.7})
			}
		}
	}
	return terms
}
