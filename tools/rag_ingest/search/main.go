package main

import (
	"context"
	"fmt"
	"log"

	"gotaskai/internal/config"
	"gotaskai/internal/pkg/llm"
	"gotaskai/internal/pkg/ragstore"
)

func main() {
	// 1. 初始化配置
	config.InitConfig("config/config.yaml")
	ctx := context.Background()

	// 2. 初始化阿里百炼 DashScope 向量化器（Qwen3-Embedding）
	embedder, err := llm.NewEmbedder()
	if err != nil {
		log.Fatalf("初始化 Embedder 失败: %v", err)
	}

	// 3. 初始化 Milvus 向量存储连接
	mc := config.AppConfig.Milvus
	store, err := ragstore.New(ctx, ragstore.Config{
		Address:        fmt.Sprintf("%s:%d", mc.Host, mc.Port),
		CollectionName: mc.CollectionName,
		Dim:            mc.Dim,
		Embedder:       embedder,
	})
	if err != nil {
		log.Fatalf("初始化 Milvus 向量库失败: %v", err)
	}
	defer store.Close()

	// 4. 模拟用户的提问
	question := "盘古系统是怎么解决大文件存储和节点离线问题的？"
	fmt.Printf("🧑 用户提问: \"%s\"\n\n", question)

	// 5. Milvus 双路召回（dense 语义 + BM25 稀疏）+ RRF 融合
	fmt.Println("正在通过 Milvus 双路召回（dense + BM25 稀疏）+ RRF 融合进行检索...")

	// kbID 为空表示检索默认知识库，与入库默认保持一致
	docs, err := store.SimilaritySearch(ctx, question, "", 2)
	if err != nil {
		log.Fatalf("检索失败: %v", err)
	}

	if len(docs) == 0 {
		fmt.Println("未检索到任何相关的知识库片段。")
		return
	}

	fmt.Printf("✅ 检索成功！找到最相关的 %d 个候选片段:\n\n", len(docs))

	for i, doc := range docs {
		fmt.Printf("🎯 [候选片段 %d]\n", i+1)
		if source, ok := doc.Metadata["source"]; ok {
			fmt.Printf("📂 来源: %v\n", source)
		}
		if docID, ok := doc.Metadata["doc_id"]; ok {
			fmt.Printf("📄 文档ID: %v\n", docID)
		}
		// 打印文档相似度得分 (Score)
		fmt.Printf("⭐ 相似度得分: %f\n", doc.Score)
		fmt.Printf("📄 文本内容:\n%s\n", doc.PageContent)
		fmt.Println("--------------------------------------------------")
	}
}
