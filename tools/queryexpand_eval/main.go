// queryexpand_eval 是对「查询扩展」原型的离线评测：在小型标注语料上，
// 对比「基线词法检索」与「扩展后检索」的召回率、精确率、MRR 与单查询延迟，
// 验证四类扩展器（同义词/实体链接/语义/多模态）对检索质量的提升。
//
// 说明：本工具使用内存 corpus + 词法重叠检索，避免依赖 Milvus / LLM / Neo4j，
// 只验证扩展流水线本身的正确性与指标增益；生产指标需在真实知识库上复测。
package main

import (
	"context"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"time"
	"unicode"

	"gotaskai/internal/pkg/queryexpand"
)

type doc struct {
	id   string
	text string
}

type weightedToken struct {
	text   string
	weight float64
}

var asciiWordRe = regexp.MustCompile(`[a-z0-9]+`)

// tokenize 将文本切分为词法单元：ASCII 单词 + CJK 字符二元组。
func tokenize(s string) []string {
	lower := strings.ToLower(s)
	var out []string
	out = append(out, asciiWordRe.FindAllString(lower, -1)...)
	out = append(out, cjkBigrams(lower)...)
	return out
}

func cjkBigrams(s string) []string {
	var out []string
	var buf []rune
	flush := func() {
		for i := 0; i+1 < len(buf); i++ {
			out = append(out, string(buf[i:i+2]))
		}
		buf = nil
	}
	for _, r := range s {
		if isCJK(r) {
			buf = append(buf, r)
		} else {
			flush()
		}
	}
	flush()
	return out
}

func isCJK(r rune) bool {
	return unicode.Is(unicode.Han, r) || unicode.Is(unicode.Hiragana, r) ||
		unicode.Is(unicode.Katakana, r) || unicode.Is(unicode.Hangul, r)
}

// weightedTokens 把文本/扩展词转换为带权词元集合。
func weightedTokens(text string, weight float64) []weightedToken {
	toks := tokenize(text)
	out := make([]weightedToken, 0, len(toks))
	for _, t := range toks {
		out = append(out, weightedToken{text: t, weight: weight})
	}
	return out
}

// retrieve 用带权词元做词法重叠评分，返回 topK 文档 id（分数>0）。
func retrieve(docs []doc, toks []weightedToken, k int) []string {
	type scored struct {
		id    string
		score float64
	}
	setOf := func(text string) map[string]bool {
		m := map[string]bool{}
		for _, t := range tokenize(text) {
			m[t] = true
		}
		return m
	}
	var sc []scored
	for _, d := range docs {
		set := setOf(d.text)
		var s float64
		for _, t := range toks {
			if set[t.text] {
				s += t.weight
			}
		}
		sc = append(sc, scored{id: d.id, score: s})
	}
	sort.SliceStable(sc, func(i, j int) bool { return sc[i].score > sc[j].score })
	out := make([]string, 0, k)
	for _, s := range sc {
		if s.score <= 0 {
			break
		}
		out = append(out, s.id)
		if len(out) >= k {
			break
		}
	}
	return out
}

func contains(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}

func recallAtK(retrieved, gold []string, k int) float64 {
	if len(gold) == 0 {
		return 0
	}
	hit := 0
	for _, g := range gold {
		if contains(retrieved[:min(k, len(retrieved))], g) {
			hit++
		}
	}
	return float64(hit) / float64(len(gold))
}

func precisionAtK(retrieved, gold []string, k int) float64 {
	n := min(k, len(retrieved))
	if n == 0 {
		return 0
	}
	hit := 0
	for _, r := range retrieved[:n] {
		if contains(gold, r) {
			hit++
		}
	}
	return float64(hit) / float64(n)
}

func mrr(retrieved, gold []string) float64 {
	for i, r := range retrieved {
		if contains(gold, r) {
			return 1.0 / float64(i+1)
		}
	}
	return 0
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func main() {
	ctx := context.Background()

	docs := []doc{
		{"d1", "DeepSeek is a Chinese AI company focused on large language models."},
		{"d2", "The company headquartered in Hangzhou develops the DeepSeek model."},
		{"d3", "Automobile engines need periodic maintenance and oil."},
		{"d4", "Electric vehicles use motors instead of combustion engines."},
		{"d5", "The quarterly report shows revenue up forty percent."},
		{"d6", "Machine learning advances drive the artificial intelligence field."},
	}

	type query struct {
		text string
		gold []string
	}
	queries := []query{
		{"深度求索是做什么的", []string{"d1", "d2"}}, // 实体链接：别名 -> 规范实体 DeepSeek
		{"car upkeep", []string{"d3"}},      // 同义词：car->automobile, upkeep->maintenance
		{"electric car", []string{"d4"}},    // 同义词：car->vehicles
		{"revenue report", []string{"d5"}},  // 对照：基线即可命中
	}

	syn := queryexpand.MapSynonymProvider{
		"car":    {"automobile", "vehicles"},
		"upkeep": {"maintenance"},
	}
	gr := queryexpand.NewMapGraphResolver(
		map[string]string{"DeepSeek": "DeepSeek", "深度求索": "DeepSeek"},
		map[string][]string{"DeepSeek": {"深度求索"}},
		map[string][]string{"DeepSeek": {"DeepSeek-V3"}},
	)

	cfg := queryexpand.DefaultConfig()
	cfg.Semantic.Enabled = true // 规则语义改写（确定性，离线评测用）
	coord := queryexpand.NewCoordinator(cfg, syn, queryexpand.RuleVariantGenerator{}, gr, nil)

	const k = 3
	var sumBaseR, sumExpR, sumBaseP, sumExpP, sumBaseMRR, sumExpMRR float64
	var sumExpLatency time.Duration

	fmt.Printf("%-22s | %-24s | %-24s | %-10s\n", "query", "baseline", "expanded", "exp_latency")
	fmt.Println(strings.Repeat("-", 90))

	for _, q := range queries {
		// 基线：仅原始查询词元（权重 1.0）。
		base := retrieve(docs, weightedTokens(q.text, 1.0), k)

		// 扩展：原始词元 + 扩展词（按其权重）。
		start := time.Now()
		expanded := coord.Expand(ctx, q.text)
		lat := time.Since(start)
		etoks := weightedTokens(q.text, 1.0)
		for _, t := range expanded.Terms {
			etoks = append(etoks, weightedTokens(t.Text, float64(t.Weight))...)
		}
		exp := retrieve(docs, etoks, k)

		sumExpLatency += lat
		sumBaseR += recallAtK(base, q.gold, k)
		sumExpR += recallAtK(exp, q.gold, k)
		sumBaseP += precisionAtK(base, q.gold, k)
		sumExpP += precisionAtK(exp, q.gold, k)
		sumBaseMRR += mrr(base, q.gold)
		sumExpMRR += mrr(exp, q.gold)

		fmt.Printf("%-22s | %-24v | %-24v | %-10s\n", q.text, base, exp, fmt.Sprintf("%dns", lat.Nanoseconds()))
	}

	n := float64(len(queries))
	fmt.Println(strings.Repeat("-", 90))
	fmt.Println("平均指标（Recall@k / Precision@k / MRR）：")
	fmt.Printf("  基线   : R@%d=%.3f  P@%d=%.3f  MRR=%.3f\n", k, sumBaseR/n, k, sumBaseP/n, sumBaseMRR/n)
	fmt.Printf("  扩展后 : R@%d=%.3f  P@%d=%.3f  MRR=%.3f\n", k, sumExpR/n, k, sumExpP/n, sumExpMRR/n)
	fmt.Printf("  扩展平均延迟（确定性扩展器，无 LLM/网络）: %.0fns\n", float64(sumExpLatency.Nanoseconds())/n)
}
