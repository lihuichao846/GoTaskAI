package queryexpand

import "context"

// MultimodalExpander 多模态关联扩展（预留）：当前知识库为纯文本（Milvus 文本 chunk），
// 无图像/音频向量索引。此扩展器在文本侧以「视觉意图关键词 → 描述/标签」的方式
// 桥接文本查询与多模态内容的语义鸿沟，为后续引入 CLIP 跨模态向量检索预留接入点。
// 默认关闭，仅在知识库具备多模态资产（caption/alt/tag 元数据）时启用。
type MultimodalExpander struct {
	provider MultimodalProvider
	maxTags  int
}

// NewMultimodalExpander 构建多模态关联扩展器；maxTags<=0 回退默认 4。
func NewMultimodalExpander(provider MultimodalProvider, maxTags int) *MultimodalExpander {
	if maxTags <= 0 {
		maxTags = 4
	}
	return &MultimodalExpander{provider: provider, maxTags: maxTags}
}

func (e *MultimodalExpander) Name() string { return "multimodal" }

func (e *MultimodalExpander) Expand(_ context.Context, query string) ([]Term, []string) {
	if e.provider == nil {
		return nil, nil
	}
	terms := e.provider.VisualTerms(query)
	if len(terms) > e.maxTags {
		terms = terms[:e.maxTags]
	}
	for i := range terms {
		terms[i].Source = "multimodal"
	}
	return terms, nil
}
