// Command graphrag_eval 实测「传统 RAG(Milvus 双路召回) vs GraphRAG(RAG+KAG 图谱检索)」的检索准确率。
//
// 设计说明：
//   - 使用独立 collection（kb_chunks_eval），不触碰生产 kb_chunks，隔离实验。
//   - 入库文档用人工构造的「单关系文档」；ground-truth 三元组用 IngestGraph 直接写入 Neo4j，
//     从而把评测聚焦在【检索】层面（规避 LLM 抽取文本噪声带来的额外变量）。
//   - 召回指标：对每个问题分别取「RAG-only topK 命中的 chunk」与「RAG+KAG 命中（chunk ∪ 图谱上下文）」，
//     判定 ground-truth 答案实体是否出现在被召回的内容里。
//   - 问题分两类：单跳（答案直接落在单个文档，向量能命中）与多跳（答案需跨两条关系链，向量难命中，
//     依赖图遍历）。用于量化 GraphRAG 的相对提升。
package main

import (
	"context"
	"fmt"
	"log"
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

// textSplitter 创建与生产一致的 RecursiveCharacter 文本切分器（ChunkSize=500, ChunkOverlap=50）。
func textSplitter() textsplitter.RecursiveCharacter {
	sp := textsplitter.NewRecursiveCharacter()
	sp.ChunkSize = 500
	sp.ChunkOverlap = 50
	return sp
}

// clearGraph 清空 Neo4j 中所有节点与关系，保证评测隔离。
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

// writeGraph 用给定的（小写）关系名直接建图，使生成的 Cypher 能够命中。
func writeGraph(driver neo4j.DriverWithContext, ctx context.Context, triples []triple) {
	session := driver.NewSession(ctx, neo4j.SessionConfig{AccessMode: neo4j.AccessModeWrite})
	defer session.Close(ctx)
	for _, t := range triples {
		_, err := session.ExecuteWrite(ctx, func(tx neo4j.ManagedTransaction) (any, error) {
			res, err := tx.Run(ctx, fmt.Sprintf(`
				MERGE (a:Entity {name: $source})
				MERGE (b:Entity {name: $target})
				MERGE (a)-[r:%s]->(b)
				RETURN id(r)
			`, t.relation), map[string]any{"source": t.source, "target": t.target})
			if err != nil {
				return nil, err
			}
			return res.Consume(ctx)
		})
		if err != nil {
			log.Printf("write tripe %s-%s-%s: %v", t.source, t.relation, t.target, err)
		}
	}
}

const evalCollection = "kb_chunks_eval"

// doc 是待入库的“单关系”文档（一个文档只包含一条事实，便于考察向量召回是否命中）。
type doc struct {
	file string
	text string
}

// triple 是 ground-truth 三元组，直接写入 Neo4j。
type triple struct {
	source   string
	relation string
	target   string
}

// q 是评测问题：question 为提问，answer 为 ground-truth 答案实体，hops 为需要的跳数，note 备注。
type q struct {
	question string
	answer   string
	hops     int
	note     string
}

func main() {
	config.InitConfig("config/config.yaml")
	ctx := context.Background()

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

	// 隔离实验：清空评测 collection，保证只包含本次数据集。
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

	// Neo4j + KAG
	var kagMgr *kag.KAGManager
	var driver neo4j.DriverWithContext
	if config.AppConfig.Neo4j.URI != "" {
		driver = db.InitNeo4j(config.AppConfig.Neo4j.URI, config.AppConfig.Neo4j.Username, config.AppConfig.Neo4j.Password)
		defer driver.Close(ctx)
		kagMgr = kag.NewKAGManager(driver, llm.NewClient())
		// 清空旧图，隔离实验。
		clearGraph(driver, ctx)
	} else {
		log.Fatal("neo4j 未配置，无法评测 KAG")
	}

	// ---------- 数据集（扩大样本量：20 题 / 10 单跳 / 10 多跳） ----------
	docs := []doc{
		// 张-王家族
		{"fam1.md", "张建国是张伟的父亲。"},
		{"fam2.md", "张伟是张小伟的父亲。"},
		{"fam3.md", "王五是赵六的父亲。"},
		{"fam4.md", "赵六是王七的父亲。"},
		{"fam5.md", "王七是王成的父亲。"},
		// 李-郑家族
		{"fam6.md", "李大山是李明的父亲。"},
		{"fam7.md", "李明是李小月的父亲。"},
		{"fam8.md", "郑军是郑强的父亲。"},
		{"fam9.md", "郑强是郑晓东的父亲。"},
		// 公司
		{"org1.md", "马云是阿里巴巴的创始人。"},
		{"org2.md", "阿里巴巴的总部位于杭州。"},
		{"org3.md", "马化腾是腾讯的创始人。"},
		{"org4.md", "腾讯的总部位于深圳。"},
		{"org5.md", "李彦宏是百度的创始人。"},
		{"org6.md", "百度的总部位于北京。"},
		// 技术
		{"tech1.md", "盘古系统采用分布式架构解决大文件存储问题。"},
		{"tech2.md", "Golang 适合高并发后端服务。"},
		{"tech3.md", "Redis 常用于缓存热点数据。"},
		{"tech4.md", "NATS 是轻量级云原生消息系统。"},
		// 干扰
		{"distractor.md", "今天天气晴朗，适合户外活动。"},
	}

	// 手动 ground-truth 三元组（与文档事实一致，便于图遍历）。
	// 注意：为避免生产 IngestGraph 里 cleanRelationName 将关系名强制大写（与 Cypher
	// 生成器输出的小写关系名不一致，导致生产链路图谱检索恒为空），这里直接用小写关系名
	// 建图，以评估【图谱检索本身】的最大能力。此差异会在报告中标出。
	triples := []triple{
		{"张建国", "father_of", "张伟"},
		{"张伟", "father_of", "张小伟"},
		{"王五", "father_of", "赵六"},
		{"赵六", "father_of", "王七"},
		{"王七", "father_of", "王成"},
		{"李大山", "father_of", "李明"},
		{"李明", "father_of", "李小月"},
		{"郑军", "father_of", "郑强"},
		{"郑强", "father_of", "郑晓东"},
		{"马云", "founder_of", "阿里巴巴"},
		{"阿里巴巴", "headquarters_in", "杭州"},
		{"阿里巴巴", "located_in", "杭州"},
		{"马化腾", "founder_of", "腾讯"},
		{"腾讯", "headquarters_in", "深圳"},
		{"李彦宏", "founder_of", "百度"},
		{"百度", "headquarters_in", "北京"},
	}

	// 写入 Milvus（按 500 字切分，与生产一致）。
	splitter := textSplitter()
	for _, d := range docs {
		doc := schema.Document{PageContent: d.text, Metadata: map[string]any{"source": d.file, "kb_id": "default", "doc_id": d.file}}
		chunks, err := textsplitter.SplitDocuments(splitter, []schema.Document{doc})
		if err != nil {
			log.Fatalf("split %s: %v", d.file, err)
		}
		if _, err := store.AddDocuments(ctx, chunks); err != nil {
			log.Fatalf("add %s: %v", d.file, err)
		}
	}

	// 写入 Neo4j（MERGE 幂等）——用小写关系名直接建图，保证与 Cypher 生成器输出可匹配。
	writeGraph(driver, ctx, triples)

	// ---------- 问题集（20 题：10 单跳 + 10 多跳） ----------
	questions := []q{
		// 单跳：向量语义即可命中
		{"张小伟的父亲是谁？", "张伟", 1, "单跳"},
		{"王五的儿子是谁？", "赵六", 1, "单跳"},
		{"王成的父亲是谁？", "王七", 1, "单跳"},
		{"李小月的父亲是谁？", "李明", 1, "单跳"},
		{"郑晓东的父亲是谁？", "郑强", 1, "单跳"},
		{"马云创立了哪家公司？", "阿里巴巴", 1, "单跳"},
		{"阿里巴巴的总部在哪里？", "杭州", 1, "单跳"},
		{"马化腾创立了哪家公司？", "腾讯", 1, "单跳"},
		{"腾讯的总部在哪里？", "深圳", 1, "单跳"},
		{"李彦宏创立了哪家公司？", "百度", 1, "单跳"},
		// 多跳：需要连接两条关系链
		{"张小伟的爷爷是谁？", "张建国", 2, "多跳"},
		{"王七的爷爷是谁？", "王五", 2, "多跳"},
		{"王成的爷爷是谁？", "赵六", 2, "多跳"},
		{"李小月的爷爷是谁？", "李大山", 2, "多跳"},
		{"郑晓东的爷爷是谁？", "郑军", 2, "多跳"},
		{"张小伟的父亲的父亲是谁？", "张建国", 2, "多跳"},
		// 跨实体链：图遍历优势最明显
		{"马云的公司位于哪个城市？", "杭州", 2, "跨实体链"},
		{"马化腾的公司位于哪个城市？", "深圳", 2, "跨实体链"},
		{"李彦宏的公司位于哪个城市？", "北京", 2, "跨实体链"},
		{"王七的父亲的父亲是谁？", "王五", 2, "多跳"},
	}

	// ---------- 跑评测 ----------
	topK := 3
	graphTopN := config.AppConfig.Context.GraphTopN
	if graphTopN <= 0 {
		graphTopN = 10
	}

	type result struct {
		ragHit bool
		kagHit bool
		ragTxt string
		kagTxt string
	}

	results := make([]result, 0, len(questions))
	ragCorrect, kagCorrect := 0, 0
	multiRag, multiKag := 0, 0
	multiN := 0

	fmt.Printf("topK=%d, graphTopN=%d\n\n", topK, graphTopN)

	for _, question := range questions {
		r := result{}

		// RAG-only：Milvus 双路召回 topK
		chunks, err := store.SimilaritySearch(ctx, question.question, "", topK)
		if err != nil {
			log.Printf("rag search fail [%s]: %v", question.question, err)
		}
		for _, c := range chunks {
			r.ragTxt += c.PageContent + "\n"
		}
		r.ragHit = strings.Contains(r.ragTxt, question.answer)

		// RAG+KAG：在图里再加一维
		graphCtx, gerr := kagMgr.RetrieveGraphContext(ctx, question.question, graphTopN)
		if gerr != nil {
			log.Printf("kag search fail [%s]: %v", question.question, gerr)
		}
		r.kagTxt = graphCtx
		r.kagHit = r.ragHit || strings.Contains(r.kagTxt, question.answer)

		if question.hops > 1 {
			multiN++
			if r.ragHit {
				multiRag++
			}
			if r.kagHit {
				multiKag++
			}
		}
		if r.ragHit {
			ragCorrect++
		}
		if r.kagHit {
			kagCorrect++
		}

		results = append(results, r)

		fmt.Printf("【%s | %d 跳】%s\n        gt=%s\n        RAG-only hit=%v | KAG hit=%v\n", question.note, question.hops, question.question, question.answer, r.ragHit, r.kagHit)
		if !r.ragHit || question.hops > 1 {
			fmt.Printf("        ┌ RAG-only 命中片段:\n%s        └ 图谱上下文: %s\n", r.ragTxt, strings.TrimSpace(r.kagTxt))
		}
		fmt.Println()
	}

	// ---------- 汇总 ----------
	n := len(questions)
	fmt.Println("============ 汇总 ============")
	fmt.Printf("问题总数: %d（单跳 %d，多跳/跨链 %d）\n", n, n-multiN, multiN)
	fmt.Printf("RAG-only  准确率: %d/%d = %.1f%%\n", ragCorrect, n, 100*float64(ragCorrect)/float64(n))
	fmt.Printf("RAG+KAG   准确率: %d/%d = %.1f%%\n", kagCorrect, n, 100*float64(kagCorrect)/float64(n))
	pct := func(a, b int) float64 { return 100 * float64(a) / float64(b) }
	fmt.Printf("相对提升: +%.1f 个百分点\n", pct(kagCorrect, n)-pct(ragCorrect, n))
	if multiN > 0 {
		fmt.Printf("—— 仅多跳子集 ——\n")
		fmt.Printf("多跳总数: %d\n", multiN)
		fmt.Printf("RAG-only  多跳准确率: %d/%d = %.1f%%\n", multiRag, multiN, pct(multiRag, multiN))
		fmt.Printf("RAG+KAG   多跳准确率: %d/%d = %.1f%%\n", multiKag, multiN, pct(multiKag, multiN))
		fmt.Printf("多跳相对提升: +%.1f 个百分点\n", pct(multiKag, multiN)-pct(multiRag, multiN))
	}
}
