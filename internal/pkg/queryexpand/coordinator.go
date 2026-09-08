package queryexpand

import (
	"context"
	"strings"
)

// Coordinator 编排多个扩展器：并行收集扩展词与语义变体，统一去重、加权、裁剪，
// 并对任何扩展器异常做安全兜底（失败不影响主链路，退回原始查询）。
type Coordinator struct {
	cfg       Config
	expanders []Expander
	// termOnly 与 variantOnly 分开处理，语义变体走 dense 多路召回，词项走 sparse 召回。
}

// NewCoordinator 依据配置组装已启用的扩展器。
// sp/vg/gr/mp 分别为同义词/语义/实体/多模态的依赖注入；对应扩展器未启用时可为 nil。
func NewCoordinator(cfg Config, sp SynonymProvider, vg VariantGenerator, gr GraphResolver, mp MultimodalProvider) *Coordinator {
	c := &Coordinator{cfg: cfg}
	if cfg.Synonym.Enabled {
		c.expanders = append(c.expanders, NewSynonymExpander(sp, cfg.Synonym.MaxTerms))
	}
	if cfg.Semantic.Enabled {
		c.expanders = append(c.expanders, NewSemanticExpander(vg, cfg.Semantic.MaxVariants))
	}
	if cfg.Entity.Enabled {
		c.expanders = append(c.expanders, NewEntityLinkExpander(gr, cfg.Entity.MaxEntities, cfg.Entity.MaxAliases, cfg.Entity.GraphDepth))
	}
	if cfg.Multimodal.Enabled {
		c.expanders = append(c.expanders, NewMultimodalExpander(mp, cfg.Multimodal.MaxTags))
	}
	return c
}

// Expand 执行完整扩展流水线，返回最终扩展产物。任何单扩展器 panic 均被捕获，
// 保证主检索链路不被扩展功能拖垮（扩展失败 = 等价于未扩展）。
func (c *Coordinator) Expand(ctx context.Context, query string) (res ExpandedQuery) {
	res = ExpandedQuery{Original: strings.TrimSpace(query)}
	if !c.cfg.Enabled || res.Original == "" {
		return res
	}

	var terms []Term
	var variants []string
	for _, e := range c.expanders {
		func() {
			defer func() { _ = recover() }() // 单扩展器失败不影响整体
			ts, vs := e.Expand(ctx, res.Original)
			terms = append(terms, ts...)
			variants = append(variants, vs...)
		}()
	}

	res.Terms = c.finalizeTerms(terms)
	res.Variants = c.finalizeVariants(variants, res.Original)
	return res
}

// finalizeTerms 去重、合并权重、按权重裁剪到 MaxTotalTerms。
func (c *Coordinator) finalizeTerms(terms []Term) []Term {
	terms = dedupTerms(terms)
	if max := c.cfg.Limits.MaxTotalTerms; max > 0 && len(terms) > max {
		terms = terms[:max]
	}
	return terms
}

// finalizeVariants 去重、剔除与原始查询重复项、截断到 MaxVariantChars 与 Semantic.MaxVariants。
func (c *Coordinator) finalizeVariants(variants []string, original string) []string {
	seen := map[string]bool{normalizeTerm(original): true}
	out := make([]string, 0, len(variants))
	maxChars := c.cfg.Limits.MaxVariantChars
	maxN := c.cfg.Semantic.MaxVariants
	for _, v := range variants {
		key := normalizeTerm(v)
		if key == "" || seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, truncateRunes(v, maxChars))
		if maxN > 0 && len(out) >= maxN {
			break
		}
	}
	return out
}
