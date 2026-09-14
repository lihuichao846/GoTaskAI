// Command wikihop_eval 用公开数据集 QAngaroo/WikiHop 实测「传统 RAG vs GraphRAG(RAG+KAG)」。
//
// 与 graphrag_eval 的区别：
//   - 数据源为真实公开数据集 wikihop dev.json 的抽样子集（含 query/answer/candidates/supports），
//     而非人工构造的单关系文档。
//   - 建图链路为【全链路】：对每个样本的 supports 文本调用 LLM 抽取三元组(ExtractKnowledge)，
//     再经 IngestGraph 写入 Neo4j，以暴露真实 LLM 抽取质量（而非手工指定 ground-truth 三元组）。
//
// 判定方式（多选题）：
//   - recall：检索召回内容是否包含 ground-truth answer 实体（宽松）。
//   - exclusivity：检索召回内容是否包含 answer，且不包含 candidates 中的其它干扰项（严格）。
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"strings"

	"gotaskai/internal/config"
	"gotaskai/internal/db"
	"gotaskai/internal/pkg/kag"
	"gotaskai/internal/pkg/llm"
	"gotaskai/internal/pkg/ragstore"

	"github.com/neo4j/neo4j-go-driver/v5/neo4j"
	"github.com/tmc/langchaingo/schema"
	"github.com/tmc/langchaingo/textsplitter"
)

// sample 对应 wikihop 单条样本。
type sample struct {
	ID         string   `json:"id"`
	Query      string   `json:"query"`
	Answer     string   `json:"answer"`
	Candidates []string `json:"candidates"`
	Supports   []string `json:"supports"`
	Hops       string   `json:"hops"`
}

const evalCollection = "kb_chunks_wikihop"

func textSplitter() textsplitter.RecursiveCharacter {
	sp := textsplitter.NewRecursiveCharacter()
	sp.ChunkSize = 500
	sp.ChunkOverlap = 50
	return sp
}

func clearGraph(driver neo4j.DriverWithContext, ctx context.Context) {
	session := driver.NewSession(ctx, neo4j.SessionConfig{})
	defer session.Close(ctx)
	_, err := session.ExecuteWrite(ctx, func(tx neo4j.ManagedTransaction) (any, error) {
		_, err := tx.Run(ctx, "MATCH (n) DETACH DELETE n", map[string]any{})
		return nil, err
	})
	if err != nil {
		log.Fatalf("clear graph: %v", err)
	}
}

func loadSamples(path string) ([]sample, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read subset: %w", err)
	}
	var out []sample
	if err := json.Unmarshal(b, &out); err != nil {
		return nil, fmt.Errorf("parse subset: %w", err)
	}
	return out, nil
}

// containsAny 判断文本 text 是否出现集合 items 中的任意一个。
func containsAny(text string, items []string) []string {
	hits := []string{}
	for _, it := range items {
		if it == "" {
			continue
		}
		if strings.Contains(strings.ToLower(text), strings.ToLower(it)) {
			hits = append(hits, it)
		}
	}
	return hits
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

	embedder, err := llm.NewEmbedder()
	if err != nil {
		log.Fatalf("embedder: %v", err)
	}

	mc := config.AppConfig.Milvus
	store, err := ragstore.New(ctx, ragstore.Config{
		Address:        fmt.Sprintf("%s:%d", mc.Host, mc.Port),
		CollectionName: evalCollection,
		Dim:            mc.Dim,
		Embedder:       embedder,
	})
	if err != nil {
		log.Fatalf("milvus: %v", err)
	}
	defer store.Close()

	if err := store.DropCollection(ctx); err != nil {
		log.Fatalf("drop collection: %v", err)
	}
	store, err = ragstore.New(ctx, ragstore.Config{
		Address:        fmt.Sprintf("%s:%d", mc.Host, mc.Port),
		CollectionName: evalCollection,
		Dim:            mc.Dim,
		Embedder:       embedder,
	})
	if err != nil {
		log.Fatalf("milvus re-create: %v", err)
	}

	var kagMgr *kag.KAGManager
	var driver neo4j.DriverWithContext
	if config.AppConfig.Neo4j.URI != "" {
		driver = db.InitNeo4j(config.AppConfig.Neo4j.URI, config.AppConfig.Neo4j.Username, config.AppConfig.Neo4j.Password)
		defer driver.Close(ctx)
		kagMgr = kag.NewKAGManager(driver, llm.NewClient())
		clearGraph(driver, ctx)
	} else {
		log.Fatal("neo4j 未配置，无法评测 KAG")
	}

	// ---------- 数据入库：向量化 supports 文档 ----------
	splitter := textSplitter()
	docCount := 0
	for i, s := range samples {
		for _, sup := range s.Supports {
			doc := schema.Document{PageContent: sup, Metadata: map[string]any{"source": s.ID, "kb_id": "default", "doc_id": s.ID}}
			chunks, err := textsplitter.SplitDocuments(splitter, []schema.Document{doc})
			if err != nil {
				log.Printf("split fail [%s]: %v", s.ID, err)
				continue
			}
			if _, err := store.AddDocuments(ctx, chunks); err != nil {
				log.Printf("add fail [%s]: %v", s.ID, err)
				continue
			}
			docCount++
		}
		log.Printf("vectorized sample %d/%d (%s), total=%d", i+1, len(samples), s.ID, docCount)
	}
	log.Printf("vectorized %d supports docs across %d samples", docCount, len(samples))

	// ---------- 全链路建图：LLM 抽取三元组 -> IngestGraph ----------
	tripleTotal := 0
	failedExtract := 0
	for i, s := range samples {
		text := strings.Join(s.Supports, "\n---\n")
		log.Printf("extracting sample %d/%d (%s)...", i+1, len(samples), s.ID)
		rels, err := kagMgr.ExtractKnowledge(ctx, text)
		if err != nil {
			log.Printf("extract fail [%s]: %v", s.ID, err)
			failedExtract++
			continue
		}
		if err := kagMgr.IngestGraph(ctx, rels); err != nil {
			log.Printf("ingest fail [%s]: %v", s.ID, err)
		}
		tripleTotal += len(rels)
		log.Printf("extracted %d triples for %s (total=%d)", len(rels), s.ID, tripleTotal)
	}
	log.Printf("ingested %d triples (extract failures across %d samples)", tripleTotal, failedExtract)

	// ---------- 跑评测 ----------
	topK := 3
	graphTopN := config.AppConfig.Context.GraphTopN
	if graphTopN <= 0 {
		graphTopN = 10
	}

	type result struct {
		ragRecall bool
		kagRecall bool
		ragExcl   bool
		kagExcl   bool
		ragTxt    string
		kagTxt    string
	}

	fmt.Printf("topK=%d, graphTopN=%d, samples=%d\n\n", topK, graphTopN, len(samples))

	results := make([]result, 0, len(samples))
	ragRecall, kagRecall := 0, 0
	ragExcl, kagExcl := 0, 0
	multiRagR, multiKagR := 0, 0
	multiRagE, multiKagE := 0, 0
	multiN := 0

	for _, s := range samples {
		r := result{}

		chunks, err := store.SimilaritySearch(ctx, s.Query, "", topK)
		if err != nil {
			log.Printf("rag search fail [%s]: %v", s.ID, err)
		}
		for _, c := range chunks {
			r.ragTxt += c.PageContent + "\n"
		}
		r.ragRecall = strings.Contains(strings.ToLower(r.ragTxt), strings.ToLower(s.Answer))
		r.ragExcl = r.ragRecall && len(containsAny(r.ragTxt, s.Candidates)) == 1

		graphCtx, gerr := kagMgr.RetrieveGraphContext(ctx, s.Query, graphTopN)
		if gerr != nil {
			log.Printf("kag search fail [%s]: %v", s.ID, gerr)
		}
		r.kagTxt = graphCtx
		r.kagRecall = r.ragRecall || strings.Contains(strings.ToLower(r.kagTxt), strings.ToLower(s.Answer))
		r.kagExcl = r.kagRecall && len(containsAny(r.ragTxt+"\n"+r.kagTxt, s.Candidates)) == 1

		if s.Hops == "multi" {
			multiN++
			if r.ragRecall {
				multiRagR++
			}
			if r.kagRecall {
				multiKagR++
			}
			if r.ragExcl {
				multiRagE++
			}
			if r.kagExcl {
				multiKagE++
			}
		}
		if r.ragRecall {
			ragRecall++
		}
		if r.kagRecall {
			kagRecall++
		}
		if r.ragExcl {
			ragExcl++
		}
		if r.kagExcl {
			kagExcl++
		}

		results = append(results, r)

		fmt.Printf("【%s/%s】%s\n        gt=%s\n        RAG recall=%v excl=%v | KAG recall=%v excl=%v\n",
			s.Hops, s.ID, s.Query, s.Answer, r.ragRecall, r.ragExcl, r.kagRecall, r.kagExcl)
		if !r.ragRecall || s.Hops == "multi" {
			fmt.Printf("        ┌ RAG-only 命中片段:\n%s        └ 图谱上下文: %s\n", r.ragTxt, strings.TrimSpace(r.kagTxt))
		}
		fmt.Println()
	}

	// ---------- 汇总 ----------
	n := len(samples)
	pct := func(a, b int) float64 { return 100 * float64(a) / float64(b) }
	fmt.Println("============ 汇总 ============")
	fmt.Printf("样本总数: %d（单跳 %d，多跳 %d）\n", n, n-multiN, multiN)
	fmt.Println("\n[宽松] 答案实体是否被召回")
	fmt.Printf("RAG-only  准确率: %d/%d = %.1f%%\n", ragRecall, n, pct(ragRecall, n))
	fmt.Printf("RAG+KAG   准确率: %d/%d = %.1f%%\n", kagRecall, n, pct(kagRecall, n))
	fmt.Printf("相对提升: +%.1f 个百分点\n", pct(kagRecall, n)-pct(ragRecall, n))
	if multiN > 0 {
		fmt.Printf("—— 仅多跳子集 ——\n")
		fmt.Printf("RAG-only  多跳准确率: %d/%d = %.1f%%\n", multiRagR, multiN, pct(multiRagR, multiN))
		fmt.Printf("RAG+KAG   多跳准确率: %d/%d = %.1f%%\n", multiKagR, multiN, pct(multiKagR, multiN))
		fmt.Printf("多跳相对提升: +%.1f 个百分点\n", pct(multiKagR, multiN)-pct(multiRagR, multiN))
	}
	fmt.Println("\n[严格] 答案被召回且未混入其它候选")
	fmt.Printf("RAG-only  准确率: %d/%d = %.1f%%\n", ragExcl, n, pct(ragExcl, n))
	fmt.Printf("RAG+KAG   准确率: %d/%d = %.1f%%\n", kagExcl, n, pct(kagExcl, n))
	if multiN > 0 {
		fmt.Printf("—— 仅多跳子集 ——\n")
		fmt.Printf("RAG-only  多跳准确率: %d/%d = %.1f%%\n", multiRagE, multiN, pct(multiRagE, multiN))
		fmt.Printf("RAG+KAG   多跳准确率: %d/%d = %.1f%%\n", multiKagE, multiN, pct(multiKagE, multiN))
	}
}
