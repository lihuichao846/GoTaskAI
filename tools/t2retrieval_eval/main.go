// Command t2retrieval_eval 用 C-MTEB T2Retrieval 数据集评测当前 Milvus 双路召回
// （dense + sparse + RRF）的检索质量，输出 recall / recall_cap / MRR / NDCG 四类指标。
//
// 数据由 convert.py 生成：corpus.jsonl / queries.jsonl / qrels.jsonl。
// 本工具复用在产 ragstore：向量化 corpus -> 写入 Milvus -> HybridSearch 检索 -> 对照 qrels 算指标。
package main

import (
	"bufio"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"math"
	"os"
	"path/filepath"
	"strings"
	"time"

	"gotaskai/internal/config"
	"gotaskai/internal/pkg/llm"
	"gotaskai/internal/pkg/ragstore"

	"github.com/tmc/langchaingo/schema"
)

type corpusDoc struct {
	ID   string `json:"id"`
	Text string `json:"text"`
}

type queryDoc struct {
	ID   string `json:"id"`
	Text string `json:"text"`
}

type qrel struct {
	QID   string `json:"qid"`
	PID   string `json:"pid"`
	Score int    `json:"score"`
}

func loadJSONL[T any](path string) ([]T, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	var out []T
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 1024*1024), 4*1024*1024)
	for sc.Scan() {
		line := sc.Bytes()
		if len(strings.TrimSpace(string(line))) == 0 {
			continue
		}
		var v T
		if err := json.Unmarshal(line, &v); err != nil {
			return nil, fmt.Errorf("parse line: %w", err)
		}
		out = append(out, v)
	}
	return out, sc.Err()
}

// recallAtK 计算单条 query 的召回率；cap 为 true 时分母截断到 K（recall_cap@K）。
func recallAtK(retrieved []string, rel map[string]bool, k int, cap bool) float64 {
	if len(rel) == 0 {
		return 0
	}
	hit := 0
	limit := k
	if len(retrieved) < limit {
		limit = len(retrieved)
	}
	for i := 0; i < limit; i++ {
		if rel[retrieved[i]] {
			hit++
		}
	}
	denom := len(rel)
	if cap && denom > k {
		denom = k
	}
	return float64(hit) / float64(denom)
}

// mrrAtK 计算单条 query 的倒数排名；无命中返回 0。
func mrrAtK(retrieved []string, rel map[string]bool, k int) float64 {
	limit := k
	if len(retrieved) < limit {
		limit = len(retrieved)
	}
	for i := 0; i < limit; i++ {
		if rel[retrieved[i]] {
			return 1.0 / float64(i+1)
		}
	}
	return 0
}

// ndcgAtK 计算单条 query 的 NDCG（qrels 均为二元相关 score=1）。
func ndcgAtK(retrieved []string, rel map[string]bool, k int) float64 {
	limit := k
	if len(retrieved) < limit {
		limit = len(retrieved)
	}
	dcg := 0.0
	for i := 0; i < limit; i++ {
		if rel[retrieved[i]] {
			dcg += 1.0 / math.Log2(float64(i+2))
		}
	}
	idealLen := len(rel)
	if idealLen > k {
		idealLen = k
	}
	idcg := 0.0
	for i := 0; i < idealLen; i++ {
		idcg += 1.0 / math.Log2(float64(i+2))
	}
	if idcg == 0 {
		return 0
	}
	return dcg / idcg
}

// maxTextBytes 对齐 Milvus fieldText VarChar 的 max_length(16384，按字节计)。
// T2Retrieval 存在超长段落，写入前按字节截断到安全长度，并避免切断多字节字符。
const maxTextBytes = 16000

func truncateText(s string) string {
	if len(s) <= maxTextBytes {
		return s
	}
	b := []byte(s)
	i := maxTextBytes
	for i > 0 && b[i]&0xC0 == 0x80 {
		i--
	}
	return string(b[:i])
}

func main() {
	dataDir := flag.String("data", "data/t2retrieval_jsonl", "convert.py 输出目录")
	collPrefix := flag.String("coll", "kb_t2retrieval", "Milvus collection 前缀")
	topK := flag.Int("topk", 5, "检索 TopK")
	limitCorpus := flag.Int("limit_corpus", 0, "仅向量化前 N 段 corpus（0=全量，调试用）")
	limitQueries := flag.Int("limit_queries", 0, "仅评测前 N 条 query（0=全量）")
	batch := flag.Int("batch", 10, "embedding 批大小")
	flag.Parse()

	config.InitConfig("config/config.yaml")
	ctx := context.Background()

	corpus, err := loadJSONL[corpusDoc](filepath.Join(*dataDir, "corpus.jsonl"))
	if err != nil {
		log.Fatalf("load corpus: %v", err)
	}
	queries, err := loadJSONL[queryDoc](filepath.Join(*dataDir, "queries.jsonl"))
	if err != nil {
		log.Fatalf("load queries: %v", err)
	}
	qrelsRaw, err := loadJSONL[qrel](filepath.Join(*dataDir, "qrels.jsonl"))
	if err != nil {
		log.Fatalf("load qrels: %v", err)
	}

	if *limitCorpus > 0 && *limitCorpus < len(corpus) {
		corpus = corpus[:*limitCorpus]
	}
	if *limitQueries > 0 && *limitQueries < len(queries) {
		queries = queries[:*limitQueries]
	}

	// qrels: qid -> 相关 pid 集合
	rel := make(map[string]map[string]bool)
	for _, q := range qrelsRaw {
		if rel[q.QID] == nil {
			rel[q.QID] = map[string]bool{}
		}
		rel[q.QID][q.PID] = true
	}
	log.Printf("corpus=%d queries=%d qrels=%d", len(corpus), len(queries), len(qrelsRaw))

	embedder, err := llm.NewEmbedder()
	if err != nil {
		log.Fatalf("embedder: %v", err)
	}
	batchSize := *batch
	mc := config.AppConfig.Milvus
	coll := fmt.Sprintf("%s_%d", *collPrefix, time.Now().UnixNano())
	store, err := ragstore.New(ctx, ragstore.Config{
		Address:        fmt.Sprintf("%s:%d", mc.Host, mc.Port),
		CollectionName: coll,
		Dim:            mc.Dim,
		Embedder:       embedder,
	})
	if err != nil {
		log.Fatalf("milvus: %v", err)
	}
	defer store.Close()
	log.Printf("collection=%s", coll)

	// 分批向量化写入 corpus
	log.Printf("indexing corpus...")
	for i := 0; i < len(corpus); i += batchSize {
		end := i + batchSize
		if end > len(corpus) {
			end = len(corpus)
		}
		docs := make([]schema.Document, 0, end-i)
		for _, c := range corpus[i:end] {
			docs = append(docs, schema.Document{
				PageContent: truncateText(c.Text),
				Metadata:    map[string]any{"doc_id": c.ID, "kb_id": "default"},
			})
		}
		if _, err := store.AddDocuments(ctx, docs); err != nil {
			log.Fatalf("index batch %d: %v", i, err)
		}
		if (end/batchSize)%50 == 0 || end == len(corpus) {
			log.Printf("indexed %d/%d", end, len(corpus))
		}
	}

	// 检索 + 算指标
	var sumRecall, sumRecallCap, sumMRR, sumNDCG float64
	for i, q := range queries {
		docs, err := store.SimilaritySearch(ctx, q.Text, "", *topK)
		if err != nil {
			log.Printf("search %q: %v", q.Text, err)
			continue
		}
		retrieved := make([]string, 0, len(docs))
		for _, d := range docs {
			if pid, ok := d.Metadata["doc_id"].(string); ok && pid != "" {
				retrieved = append(retrieved, pid)
			}
		}
		r := rel[q.ID]
		sumRecall += recallAtK(retrieved, r, *topK, false)
		sumRecallCap += recallAtK(retrieved, r, *topK, true)
		sumMRR += mrrAtK(retrieved, r, *topK)
		sumNDCG += ndcgAtK(retrieved, r, *topK)
		if (i+1)%100 == 0 {
			log.Printf("evaluated %d/%d queries", i+1, len(queries))
		}
	}

	n := float64(len(queries))
	fmt.Println("\n============ T2Retrieval 检索召回评测 ============")
	fmt.Printf("queries=%d  topK=%d\n", len(queries), *topK)
	fmt.Printf("recall@%d      = %.4f\n", *topK, sumRecall/n)
	fmt.Printf("recall_cap@%d  = %.4f\n", *topK, sumRecallCap/n)
	fmt.Printf("MRR@%d         = %.4f\n", *topK, sumMRR/n)
	fmt.Printf("NDCG@%d        = %.4f\n", *topK, sumNDCG/n)
}
