package intent

import (
	"context"
	"fmt"
	"log"
	"math"
	"sync"
)

// Embedder 是语义层依赖的最小向量化能力（*llm.Client 已实现该接口）。
type Embedder interface {
	CreateEmbedding(ctx context.Context, text string) ([]float32, error)
}

// protoVec 是一条已向量化的意图原型。
type protoVec struct {
	label Intent
	vec   []float32
}

// SemanticClassifier 是路由的语义快路径：将用户问题与意图原型库做向量比对，
// 高置信时直接决策，从而避免调用 LLM。
//
// 安全约束：默认只做「正向加速」——高相似命中 knowledge/mixed 时判「需要检索」
// （假阳性只是多检索）；「关闭检索」必须显式开启 allowClose，且须通过评测门槛
// （语义层关闭检索的漏检率 ≤2%）后才可开启。
type SemanticClassifier struct {
	embedder   Embedder
	high       float64
	margin     float64
	allowClose bool

	mu     sync.RWMutex
	protos []protoVec
	ready  bool
}

// NewSemanticClassifier 创建语义分类器；high/margin <=0 时回退默认 0.86 / 0.05。
func NewSemanticClassifier(embedder Embedder, high, margin float64, allowClose bool) *SemanticClassifier {
	if high <= 0 {
		high = 0.86
	}
	if margin <= 0 {
		margin = 0.05
	}
	return &SemanticClassifier{
		embedder:   embedder,
		high:       high,
		margin:     margin,
		allowClose: allowClose,
	}
}

// Warmup 计算原型库向量，成功后语义层才就绪。
// 失败（embedding 不可用等）时语义层保持未就绪，等效禁用，不阻断任务。
func (s *SemanticClassifier) Warmup(ctx context.Context) error {
	if s == nil || s.embedder == nil {
		return fmt.Errorf("semantic: embedder not configured")
	}

	protos := make([]protoVec, 0, 32)
	for label, samples := range prototypes {
		for _, text := range samples {
			vec, err := s.embedder.CreateEmbedding(ctx, text)
			if err != nil {
				return fmt.Errorf("embed prototype %q: %w", text, err)
			}
			protos = append(protos, protoVec{label: label, vec: vec})
		}
	}
	if len(protos) == 0 {
		return fmt.Errorf("semantic: empty prototype bank")
	}

	s.mu.Lock()
	s.protos = protos
	s.ready = true
	s.mu.Unlock()
	log.Printf("[Intent] semantic layer ready with %d prototypes", len(protos))
	return nil
}

// Ready 返回语义层是否已完成初始化。
func (s *SemanticClassifier) Ready() bool {
	if s == nil {
		return false
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.ready
}

// Classify 返回决策与是否命中；未命中（含未就绪、低置信、embedding 失败）
// 由调用方继续下传 LLM。
func (s *SemanticClassifier) Classify(ctx context.Context, req Request) (Decision, bool) {
	if s == nil || s.embedder == nil || !s.Ready() {
		return Decision{}, false
	}

	q, err := s.embedder.CreateEmbedding(ctx, req.Question)
	if err != nil {
		return Decision{}, false
	}

	s.mu.RLock()
	protos := s.protos
	s.mu.RUnlock()

	label, s1, s2 := nearest(protos, q)
	if s1 < s.high || s1-s2 < s.margin {
		return Decision{}, false
	}

	switch label {
	case IntentKnowledge, IntentMixed:
		// 正向方向：恒采信（假阳性只是多检索，符合保守原则）。
		return Decision{
			NeedRAG:    req.HasKB,
			NeedKAG:    req.HasKAG,
			NeedTools:  true,
			Confidence: s1,
			Source:     SourceSemantic,
		}, true
	case IntentChat:
		if !s.allowClose {
			return Decision{}, false
		}
		return Decision{
			NeedRAG:    false,
			NeedKAG:    false,
			NeedTools:  false,
			Confidence: s1,
			Source:     SourceSemantic,
		}, true
	default:
		// IntentTool：工具意图无法推断「是否需要检索」，交 LLM 判定。
		return Decision{}, false
	}
}

// nearest 返回 top1 类别、top1 相似度，以及「不同类别」中的最高相似度（用于间隔判定）。
func nearest(protos []protoVec, q []float32) (Intent, float64, float64) {
	var best Intent
	s1 := math.Inf(-1)
	for _, p := range protos {
		if sim := cosine(q, p.vec); sim > s1 {
			s1 = sim
			best = p.label
		}
	}
	if math.IsInf(s1, -1) {
		return "", 0, 0
	}

	// top2 取「异类」最高分：同类原型的次高分不代表类别歧义。
	s2 := math.Inf(-1)
	for _, p := range protos {
		if p.label == best {
			continue
		}
		if sim := cosine(q, p.vec); sim > s2 {
			s2 = sim
		}
	}
	if math.IsInf(s2, -1) {
		s2 = 0
	}
	return best, s1, s2
}

// cosine 计算两个向量的余弦相似度；维度不一致或零向量返回 0。
func cosine(a, b []float32) float64 {
	if len(a) == 0 || len(a) != len(b) {
		return 0
	}
	var dot, na, nb float64
	for i := range a {
		dot += float64(a[i]) * float64(b[i])
		na += float64(a[i]) * float64(a[i])
		nb += float64(b[i]) * float64(b[i])
	}
	if na == 0 || nb == 0 {
		return 0
	}
	return dot / (math.Sqrt(na) * math.Sqrt(nb))
}
