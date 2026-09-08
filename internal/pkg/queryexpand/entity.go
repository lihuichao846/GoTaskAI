package queryexpand

import (
	"context"
	"strings"
)

// EntityLinkExpander 实体链接扩展：识别查询中的实体提及（规范名或别名），
// 归一化为图谱中的规范实体，并追加该实体的别名与一跳关联实体作为扩展词。
// 目的是让「口语别名 / 缩写」命中库里以规范名或关联实体存储的文档。
// 复用现有 KAG（Neo4j 图）基础设施：GraphResolver 由 Neo4j 适配实现。
type EntityLinkExpander struct {
	resolver    GraphResolver
	maxEntities int
	maxAliases  int
	graphDepth  int
}

// NewEntityLinkExpander 构建实体链接扩展器；零值参数回退默认。
func NewEntityLinkExpander(resolver GraphResolver, maxEntities, maxAliases, graphDepth int) *EntityLinkExpander {
	if maxEntities <= 0 {
		maxEntities = 5
	}
	if maxAliases <= 0 {
		maxAliases = 3
	}
	if graphDepth <= 0 {
		graphDepth = 1
	}
	return &EntityLinkExpander{
		resolver:    resolver,
		maxEntities: maxEntities,
		maxAliases:  maxAliases,
		graphDepth:  graphDepth,
	}
}

func (e *EntityLinkExpander) Name() string { return "entity" }

// Expand 对图中表面形式做「最长匹配」识别查询中的实体提及，追加规范实体 + 别名 + 关联实体。
func (e *EntityLinkExpander) Expand(ctx context.Context, query string) ([]Term, []string) {
	if e.resolver == nil {
		return nil, nil
	}
	surfaces := e.resolver.SurfaceForms()
	sortByLenDesc(surfaces)

	lower := strings.ToLower(query)
	var terms []Term
	matched := 0
	emitted := map[string]bool{} // 已输出的规范实体，避免同实体多次命中
	for _, sf := range surfaces {
		key := normalizeTerm(sf)
		if key == "" || !containsToken(lower, key) {
			continue
		}
		canonicalRaw := e.resolver.Canonical(sf)
		canonical := normalizeTerm(canonicalRaw)
		if canonical == "" {
			canonical = key
		}
		if emitted[canonical] {
			continue
		}
		emitted[canonical] = true

		terms = append(terms, Term{Text: canonical, Weight: 1.0, Source: "entity"})
		for _, a := range e.resolver.Aliases(canonicalRaw) {
			if n := normalizeTerm(a); n != "" && n != canonical {
				terms = append(terms, Term{Text: n, Weight: 0.8, Source: "entity"})
			}
		}
		for _, r := range e.resolver.Related(ctx, canonicalRaw, e.graphDepth) {
			if n := normalizeTerm(r); n != "" && n != canonical {
				terms = append(terms, Term{Text: n, Weight: 0.6, Source: "entity"})
			}
		}

		matched++
		if matched >= e.maxEntities {
			break
		}
	}
	return terms, nil
}
