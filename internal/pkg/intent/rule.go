package intent

import "regexp"

// chitChatRe 是唯一的「关闭检索」规则：锚定全串匹配纯寒暄/致谢/告别。
//
// 采用 ^...$ 全串锚定而非裸子串，确保任何附带需求的输入都不命中
// （例如「你好，帮我查下文档」不应被判为 chat），从而避免确定性漏检索。
// (?i) 使英文寒暄大小写不敏感（Hi / Hello / OK 等）。
var chitChatRe = regexp.MustCompile(
	`(?i)^\s*(你好|您好|hi|hello|hey|嗨|哈喽|在吗|早上好|下午好|晚上好|` +
		`谢谢|感谢|thx|thanks?|再见|拜拜|bye|ok|okay|好的|收到)[\s!！。.~,，、?？]*$`)

// RuleClassifier 是路由的规则兜底层，零成本、确定性。
//
// 硬约束：只有本层可以关闭检索，且仅限上述高精度锚定规则。
// 新增规则时不得置 NeedRAG=false，除非其精度经过标注集验证。
type RuleClassifier struct{}

// NewRuleClassifier 创建规则分类器。
func NewRuleClassifier() *RuleClassifier { return &RuleClassifier{} }

// Classify 命中纯寒暄时返回 chat 决策；未命中返回 ok=false，交由上层继续路由。
func (r *RuleClassifier) Classify(req Request) (Decision, bool) {
	if !chitChatRe.MatchString(req.Question) {
		return Decision{}, false
	}
	return Decision{
		Intent:     IntentChat,
		NeedRAG:    false,
		NeedKAG:    false,
		NeedTools:  false,
		Confidence: 0.95,
		Source:     SourceRule,
	}, true
}
