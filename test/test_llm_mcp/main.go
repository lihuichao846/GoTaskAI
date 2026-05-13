package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"os/signal"
	"syscall"

	"gotaskai/internal/config"
	"gotaskai/internal/pkg/llm"
	"gotaskai/internal/pkg/mcpclient"

	"github.com/sashabaranov/go-openai"
)

func main() {
	// 加载配置
	config.InitConfig("config/config.yaml")

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// 启动 MCP Client
	mcpWrapper, err := mcpclient.NewWrapper(ctx, "d:\\Program Files\\GoTaskAI\\tools\\search_mcp\\search_mcp.exe")
	if err != nil {
		log.Fatalf("Failed to start MCP client: %v", err)
	}
	defer mcpWrapper.Close()

	// 启动 LLM Client
	llmClient := llm.NewClient()

	systemPrompt := `[特别指示 - 联网工具使用规范]:
1. 【首选谷歌搜索】：当你遇到不确定的知识、实时信息、新闻、甚至历史事件时，请**优先调用 'internet_search' 工具进行谷歌搜索**，不要轻易说自己不知道。
2. 【严格禁止幻觉】：当工具返回了搜索结果后，你必须**严格且仅基于工具返回的内容**来回答用户。
3. 【禁止脑补】：如果工具返回“未找到结果”或报错，你必须直接告诉用户“抱歉，我没有搜索到相关信息”，绝对不能使用你自带的旧知识进行猜测或编造。
4. 【客观陈述】：请客观地复述搜索结果，不要在搜索结果上过度发散或添加未经验证的细节。`

	fmt.Println("正在向大模型提问: olivia rodrigo是谁")

	result, err := llmClient.Generate(ctx, systemPrompt, []openai.ChatCompletionMessage{}, "olivia rodrigo是谁", mcpWrapper)
	if err != nil {
		log.Fatalf("Generate error: %v", err)
	}

	fmt.Println("\n最终结果:")
	fmt.Println(result)

	// 等待退出
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	<-sigCh
}
