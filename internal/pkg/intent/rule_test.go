package intent

import "testing"

func TestRuleClassifier_Hit(t *testing.T) {
	r := NewRuleClassifier()
	hits := []string{"你好", "您好", "Hi", "Hello", "OK", "谢谢", "再见", " 你好！ ", "thanks"}
	for _, q := range hits {
		d, ok := r.Classify(Request{Question: q})
		if !ok {
			t.Fatalf("expected rule hit for %q", q)
		}
		if d.Intent != IntentChat || d.NeedRAG || d.NeedKAG || d.NeedTools {
			t.Fatalf("unexpected decision for %q: %+v", q, d)
		}
		if d.Source != SourceRule {
			t.Fatalf("expected source %q, got %q", SourceRule, d.Source)
		}
	}
}

func TestRuleClassifier_AdversarialMiss(t *testing.T) {
	// 对抗性负样本：任何附带需求的输入都不得命中，否则会造成确定性漏检索。
	r := NewRuleClassifier()
	miss := []string{
		"你好，帮我查下文档",
		"你好呀",
		"时间复杂度",
		"现在几点",
		"",
		"谢谢，另外我们的部署文档在哪",
	}
	for _, q := range miss {
		if _, ok := r.Classify(Request{Question: q}); ok {
			t.Fatalf("expected rule miss for %q", q)
		}
	}
}
