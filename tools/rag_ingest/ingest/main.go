package main

import (
	"context"
	"fmt"
	"log"
	"os"

	"gotaskai/internal/config"

	"github.com/tmc/langchaingo/embeddings"
	"github.com/tmc/langchaingo/llms/ollama"
	"github.com/tmc/langchaingo/schema"
	"github.com/tmc/langchaingo/textsplitter"
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

	// 3. 读取原始文件
	content, err := os.ReadFile("data/pangu_knowledge.md")
	if err != nil {
		log.Fatalf("读取文件失败: %v", err)
	}

	// 把原始文本包装成 LangChain 标准的 Document 结构
	doc := schema.Document{
		PageContent: string(content),
		Metadata: map[string]any{
			"source": "pangu_knowledge.md",
		},
	}

	// 4. 智能分块
	splitter := textsplitter.NewRecursiveCharacter()
	splitter.ChunkSize = 500
	splitter.ChunkOverlap = 50

	chunks, err := textsplitter.SplitDocuments(splitter, []schema.Document{doc})
	if err != nil {
		log.Fatalf("文档切分失败: %v", err)
	}
	fmt.Printf("智能切分完成，共 %d 个 Chunk\n", len(chunks))

	// 5. 初始化 Redis 向量存储连接
	redisURL := config.AppConfig.Redis.VectorStoreURL()
	if redisURL == "" {
		log.Fatalf("RedisVector 需要配置 redis.addr 直连地址")
	}

	store, err := redisvector.New(
		ctx,
		redisvector.WithConnectionURL(redisURL),
		redisvector.WithIndexName("idx:pangu_v2", true),
		redisvector.WithEmbedder(embedder),
	)
	if err != nil {
		log.Fatalf("初始化 Redis 向量库失败: %v", err)
	}

	// 6. 一键向量化并入库
	fmt.Println("正在向量化并入库，请稍候...")
	_, err = store.AddDocuments(ctx, chunks)
	if err != nil {
		log.Fatalf("入库失败: %v", err)
	}

	fmt.Println("🎉 知识库录入成功（使用 LangChainGo）！")
}
