package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"path/filepath"

	"gotaskai/internal/config"
	"gotaskai/internal/pkg/llm"
	"gotaskai/internal/pkg/ragstore"

	"github.com/tmc/langchaingo/schema"
	"github.com/tmc/langchaingo/textsplitter"
)

func main() {
	rebuild := flag.Bool("rebuild", false, "删除已有 Collection 后重建（切换 embedding 模型后建议开启）")
	flag.Parse()

	// 1. 初始化配置
	config.InitConfig("config/config.yaml")
	ctx := context.Background()

	// 2. 初始化阿里百炼 DashScope 向量化器（Qwen3-Embedding）
	embedder, err := llm.NewEmbedder()
	if err != nil {
		log.Fatalf("初始化 Embedder 失败: %v", err)
	}

	mc := config.AppConfig.Milvus
	storeCfg := ragstore.Config{
		Address:        fmt.Sprintf("%s:%d", mc.Host, mc.Port),
		CollectionName: mc.CollectionName,
		Dim:            mc.Dim,
		Embedder:       embedder,
	}

	if *rebuild {
		tmp, err := ragstore.New(ctx, storeCfg)
		if err != nil {
			log.Fatalf("初始化 Milvus 连接失败: %v", err)
		}
		if err := tmp.DropCollection(ctx); err != nil {
			log.Fatalf("删除旧 Collection 失败: %v", err)
		}
		_ = tmp.Close()
		fmt.Println("已删除旧 Collection，开始全量重建...")
	}

	store, err := ragstore.New(ctx, storeCfg)
	if err != nil {
		log.Fatalf("初始化 Milvus 向量库失败: %v", err)
	}
	defer store.Close()

	// 3. 收集待入库文件：默认 data 目录下所有 .md，也可用参数逐个指定
	files := flag.Args()
	if len(files) == 0 {
		matches, _ := filepath.Glob(filepath.Join(config.ResolveProjectPath("data"), "*.md"))
		files = matches
	}
	if len(files) == 0 {
		log.Fatal("未找到任何可入库的 .md 文档")
	}

	splitter := textsplitter.NewRecursiveCharacter()
	splitter.ChunkSize = 500
	splitter.ChunkOverlap = 50

	for _, f := range files {
		content, err := os.ReadFile(f)
		if err != nil {
			log.Printf("读取文件失败 %s: %v", f, err)
			continue
		}
		doc := schema.Document{
			PageContent: string(content),
			Metadata: map[string]any{
				"source": filepath.Base(f),
				"kb_id":  "default",
				"doc_id": filepath.Base(f),
			},
		}
		chunks, err := textsplitter.SplitDocuments(splitter, []schema.Document{doc})
		if err != nil {
			log.Printf("文档切分失败 %s: %v", f, err)
			continue
		}
		fmt.Printf("正在向量化并入库 %s（%d 个 Chunk）...\n", filepath.Base(f), len(chunks))
		n, err := store.AddDocuments(ctx, chunks)
		if err != nil {
			log.Printf("入库失败 %s: %v", f, err)
			continue
		}
		fmt.Printf("✅ %s 入库成功，共 %d 个 Chunk\n", filepath.Base(f), n)
	}
	fmt.Println("🎉 全部知识库重建完成！")
}
