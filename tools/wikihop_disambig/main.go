// Command wikihop_disambig 做「候选消歧 + 基线对照」评测，回答两个关键问题：
//   1) 我们的 RAG 召回是否显著优于朴素词法（BM25 近似）基线？
//   2) 把「召回是否含答案(recall)」升级成「LLM 从候选里选唯一答案(accuracy)」后，
//      检索上下文到底有没有正向价值（no-context / +RAG / +RAG+KAG 三档对照）？
//
// 复用上次全链路实测已建的 Milvus collection(kb_chunks_wikihop) 与 Neo4j 图谱，
// 因此本工具只需少量 LLM 消歧调用，不再重跑 embedding / 三元组抽取。
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"sort"
	"strings"
	"time"

	"gotaskai/internal/config"
	"gotaskai/internal/pkg/kag"
	"gotaskai/internal/pkg/llm"
	"gotaskai/internal/pkg/ragstore"

	"github.com/go-ego/gse"
	"github.com/neo4j/neo4j-go-driver/v5/neo4j"
)

type sample struct {
	ID         string   `json:"id"`
	Query      string   `json:"query"`
	Answer     string   `json:"answer"`
	Candidates []string `json:"candidates"`
	Supports   []string `json:"supports"`
	Hops       string   `json:"hops"`
}

const evalCollection = "kb_chunks_wikihop"

func loadSamples(path string) ([]sample, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var out []sample
	if err := json.Unmarshal(b, &out); err != nil {
		return nil, err
	}
	return out, nil
}

// tokenSet 用 gse 分词得到词元集合（与生产 ragstore 一致）。
func tokenSet(seg *gse.Segmenter, text string) map[string]bool {
	raw := seg.Cut(strings.ToLower(text), true)
	m := make(map[string]bool, len(raw))
	for _, t := range raw {
		if t = strings.TrimSpace(t); t != "" {
			m[t] = true
		}
	}
	return m
}

// lexicalRecall 在全局 supports 池上做词法重叠检索，返回 answer 被召回(recall)的样本数。
// 与 wikihop_eval 的 ragRecall 判定口径一致：召回 topK 文本是否包含 answer 原文。
func lexicalRecall(seg *gse.Segmenter, samples []sample, topK int) int {
	type doc struct {
		text  string
		token map[string]bool
	}
	var pool []doc
	for _, s := range samples {
		for _, sup := range s.Supports {
			pool = append(pool, doc{text: sup, token: tokenSet(seg, sup)})
		}
	}

	hit := 0
	for _, s := range samples {
		qTok := tokenSet(seg, s.Query)
		type scored struct {
			text  string
			score int
		}
		var sc []scored
		for _, d := range pool {
			score := 0
			for tok := range qTok {
				if d.token[tok] {
					score++
				}
			}
			if score > 0 {
				sc = append(sc, scored{text: d.text, score: score})
			}
		}
		sort.SliceStable(sc, func(i, j int) bool { return sc[i].score > sc[j].score })
		var sb strings.Builder
		for i := 0; i < len(sc) && i < topK; i++ {
			sb.WriteString(sc[i].text)
			sb.WriteString("\n")
		}
		if strings.Contains(strings.ToLower(sb.String()), strings.ToLower(s.Answer)) {
			hit++
		}
	}
	return hit
}

// disambiguate 让 LLM 从候选里选唯一答案；context 可为空(no-context)。
func disambiguate(ctx context.Context, c *llm.Client, query string, candidates []string, contextText string) string {
	systemPrompt := "你是多选题问答助手。请根据问题和候选答案列表，选出唯一正确的答案。" +
		"只输出答案原文本身，不要输出任何解释、编号、引号或多余文字。"

	var b strings.Builder
	b.WriteString("问题：")
	b.WriteString(query)
	b.WriteString("\n\n")
	if strings.TrimSpace(contextText) != "" {
		b.WriteString("参考上下文：\n")
		b.WriteString(strings.TrimSpace(contextText))
		b.WriteString("\n\n")
	}
	b.WriteString("候选答案：\n")
	for _, cand := range candidates {
		b.WriteString("- ")
		b.WriteString(cand)
		b.WriteString("\n")
	}
	b.WriteString("\n请从候选答案中选出唯一正确答案。")

	callCtx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	out, err := c.Generate(callCtx, systemPrompt, nil, b.String(), nil)
	if err != nil {
		log.Printf("disambiguate fail [%s]: %v", query, err)
		return ""
	}
	return strings.TrimSpace(out)
}

func main() {
	config.InitConfig("config/config.yaml")
	ctx := context.Background()

	subsetPath := "tools/graphrag_eval/wikihop_subset.json"
	if len(os.Args) > 1 {
		subsetPath = os.Args[1]
	}
	samples, err := loadSamples(subsetPath)
	if err != nil {
		log.Fatalf("load samples: %v", err)
	}
	log.Printf("loaded %d samples", len(samples))

	seg, err := gse.NewEmbed()
	if err != nil {
		log.Fatalf("gse: %v", err)
	}

	// ---------- 1. BM25/词法基线 recall ----------
	const topK = 3
	lexHit := lexicalRecall(&seg, samples, topK)
	fmt.Printf("\n[1] 词法(BM25 近似)基线 recall@%d: %d/%d = %.1f%%\n",
		topK, lexHit, len(samples), 100*float64(lexHit)/float64(len(samples)))

	// ---------- 2. no-context 候选消歧（只需 LLM，云端可用） ----------
	llmClient := llm.NewClient()
	noCtxCorrect, multiN, multiNoCtx := 0, 0, 0

	fmt.Printf("\n[2] no-context 候选消歧 accuracy（LLM 只看题目+候选，不看文档）\n")
	for i, s := range samples {
		log.Printf("no-context disambiguating %d/%d (%s)...", i+1, len(samples), s.ID)
		ans := disambiguate(ctx, llmClient, s.Query, s.Candidates, "")
		ok := strings.Contains(strings.ToLower(ans), strings.ToLower(s.Answer))
		if ok {
			noCtxCorrect++
		}
		if s.Hops == "multi" {
			multiN++
			if ok {
				multiNoCtx++
			}
		}
		fmt.Printf("  %-28s [%s] -> %v\n", s.Query, s.Hops, ok)
	}
	n := len(samples)
	pct := func(a, b int) float64 { return 100 * float64(a) / float64(b) }
	fmt.Printf("\nno-context accuracy: %d/%d = %.1f%%\n", noCtxCorrect, n, pct(noCtxCorrect, n))
	if multiN > 0 {
		fmt.Printf("  仅多跳: %d/%d = %.1f%%\n", multiNoCtx, multiN, pct(multiNoCtx, multiN))
	}

	// ---------- 3. 连接 Milvus/Neo4j（超时+降级），跑 +RAG/+RAG+KAG ----------
	embedder, err := llm.NewEmbedder()
	if err != nil {
		log.Printf("embedder unavailable, skip RAG context: %v", err)
		embedder = nil
	}

	var store *ragstore.Store
	if embedder != nil {
		mc := config.AppConfig.Milvus
		connCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
		srv, serr := ragstore.New(connCtx, ragstore.Config{
			Address:        fmt.Sprintf("%s:%d", mc.Host, mc.Port),
			CollectionName: evalCollection,
			Dim:            mc.Dim,
			Embedder:       embedder,
		})
		cancel()
		if serr != nil {
			log.Printf("milvus unavailable, skip RAG context: %v", serr)
		} else {
			store = srv
			defer store.Close()
			log.Printf("milvus connected (%s)", evalCollection)
		}
	}

	var kagMgr *kag.KAGManager
	if config.AppConfig.Neo4j.URI != "" {
		connCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
		driver, derr := neo4j.NewDriverWithContext(config.AppConfig.Neo4j.URI, neo4j.BasicAuth(config.AppConfig.Neo4j.Username, config.AppConfig.Neo4j.Password, ""))
		if derr == nil {
			if verr := driver.VerifyConnectivity(connCtx); verr == nil {
				kagMgr = kag.NewKAGManager(driver, llm.NewClient())
				defer driver.Close(ctx)
				log.Printf("neo4j connected")
			} else {
				log.Printf("neo4j unavailable, skip KAG context: %v", verr)
				_ = driver.Close(ctx)
			}
		} else {
			log.Printf("neo4j driver fail: %v", derr)
		}
		cancel()
	}

	graphTopN := config.AppConfig.Context.GraphTopN
	if graphTopN <= 0 {
		graphTopN = 10
	}

	// ---------- 4. 带上下文候选消歧（仅在 Milvus/Neo4j 可用时） ----------
	if store == nil && kagMgr == nil {
		fmt.Println("\n============ 汇总 ============")
		fmt.Printf("词法基线 recall@%d: %.1f%%\n", topK, pct(lexHit, n))
		fmt.Printf("no-context accuracy: %.1f%%\n", pct(noCtxCorrect, n))
		fmt.Println("(Milvus/Neo4j 不可用，+RAG/+RAG+KAG 档跳过)")
		return
	}

	fmt.Printf("\n[3] 带上下文候选消歧 accuracy\n")
	fmt.Printf("%-28s | %-9s | %-9s\n", "query", "+RAG", "+RAG+KAG")
	ragCorrect, kagCorrect := 0, 0
	multiRag, multiKag := 0, 0
	for i, s := range samples {
		log.Printf("context disambiguating %d/%d (%s)...", i+1, len(samples), s.ID)
		var ragText strings.Builder
		if store != nil {
			chunks, _ := store.SimilaritySearch(ctx, s.Query, "", topK)
			for _, c := range chunks {
				ragText.WriteString(c.PageContent)
				ragText.WriteString("\n")
			}
		}
		var graphCtx string
		if kagMgr != nil {
			graphCtx, _ = kagMgr.RetrieveGraphContext(ctx, s.Query, graphTopN)
		}
		ragAns := disambiguate(ctx, llmClient, s.Query, s.Candidates, ragText.String())
		kagAns := disambiguate(ctx, llmClient, s.Query, s.Candidates, ragText.String()+"\n"+graphCtx)
		eq := func(got string) bool {
			return strings.Contains(strings.ToLower(got), strings.ToLower(s.Answer))
		}
		ragOK, kagOK := eq(ragAns), eq(kagAns)
		if ragOK {
			ragCorrect++
		}
		if kagOK {
			kagCorrect++
		}
		if s.Hops == "multi" {
			if ragOK {
				multiRag++
			}
			if kagOK {
				multiKag++
			}
		}
		mark := func(b bool) string {
			if b {
				return "✓"
			}
			return "✗"
		}
		fmt.Printf("%-28s | %-9s | %-9s\n", s.Query, mark(ragOK), mark(kagOK))
	}

	fmt.Println("\n============ 汇总 ============")
	fmt.Printf("词法基线 recall@%d: %.1f%%\n", topK, pct(lexHit, n))
	fmt.Printf("no-context accuracy : %.1f%%\n", pct(noCtxCorrect, n))
	fmt.Printf("+RAG chunk accuracy : %.1f%%\n", pct(ragCorrect, n))
	fmt.Printf("+RAG+KAG accuracy   : %.1f%%\n", pct(kagCorrect, n))
	if multiN > 0 {
		fmt.Println("\n[仅多跳子集 accuracy]")
		fmt.Printf("no-context : %d/%d = %.1f%%\n", multiNoCtx, multiN, pct(multiNoCtx, multiN))
		fmt.Printf("+RAG       : %d/%d = %.1f%%\n", multiRag, multiN, pct(multiRag, multiN))
		fmt.Printf("+RAG+KAG   : %d/%d = %.1f%%\n", multiKag, multiN, pct(multiKag, multiN))
	}
}
