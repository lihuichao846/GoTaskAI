package main

import (
	"context"
	"fmt"
	"log"
	"strings"

	"gotaskai/internal/config"

	"github.com/tmc/langchaingo/embeddings"
	"github.com/tmc/langchaingo/llms/ollama"
	"github.com/tmc/langchaingo/vectorstores/redisvector"
)

func main() {
	// 1. 初始化配置
	config.InitConfig("config/config.yaml")
	ctx := context.Background()

	// 2. 初始化 Ollama 模型客户端 (用于生成 Embedding 向量)
	llm, err := ollama.New(
		ollama.WithServerURL("http://localhost:11434"),
		ollama.WithModel("bge-m3"),
	)
	if err != nil {
		log.Fatalf("初始化 Ollama 失败: %v", err)
	}
	embedder, err := embeddings.NewEmbedder(llm)
	if err != nil {
		log.Fatalf("初始化 Embedder 失败: %v", err)
	}

	// 3. 初始化 Redis 向量存储连接
	redisURL := config.AppConfig.Redis.Addr
	if !strings.HasPrefix(redisURL, "redis://") {
		redisURL = "redis://" + redisURL
	}

	store, err := redisvector.New(
		ctx,
		redisvector.WithConnectionURL(redisURL),
		redisvector.WithIndexName("idx:pangu_v2", false), // false 表示不主动创建，必须存在
		redisvector.WithEmbedder(embedder),
	)
	if err != nil {
		log.Fatalf("初始化 Redis 向量库失败: %v", err)
	}

	// 4. 模拟用户的提问
	question := "盘古系统是怎么解决大文件存储和节点离线问题的？"
	fmt.Printf("🧑 用户提问: \"%s\"\n\n", question)

	// 5. 向量检索
	fmt.Println("正在通过 LangChainGo 进行检索...")
	
	// 一行代码搞定检索！自动调用 embedder 生成查询向量，自动去 redis 查询
	// 并且返回的结果已经反序列化为 Document 结构了
	docs, err := store.SimilaritySearch(ctx, question, 2)
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
		// 如果在 SimilaritySearch 时想拿 score，langchaingo 提供 WithScoreThreshold 等选项，
		// 返回的 doc.Score 也是有的 (如果版本支持)。这里简单打印内容。
		fmt.Printf("📄 文本内容:\n%s\n", doc.PageContent)
		fmt.Println("--------------------------------------------------")
	}
}
