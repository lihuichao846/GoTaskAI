package main

import (
	"context"
	"fmt"
	"log"
	"path/filepath"
	"time"

	"gotaskai/internal/config"
	"gotaskai/internal/pkg/llm"
	"gotaskai/internal/pkg/mcpclient"
)

func main() {
	// 1. 初始化配置
	config.InitConfig("config/config.yaml")

	fmt.Println("=== 正在测试大模型 + MCP 联网搜索 ===")

	// 2. 启动 MCP Server (联网搜索子进程)
	ctx := context.Background()
	mcpServerPath, _ := filepath.Abs("bin/search_mcp.exe")
	
	// 注意：确保之前已通过 go build -o bin/search_mcp.exe ./tools/search_mcp/main.go 编译了 Server
	mcpClient, err := mcpclient.NewWrapper(ctx, mcpServerPath)
	if err != nil {
		log.Fatalf("❌ 无法连接到 MCP Server: %v", err)
	}
	defer mcpClient.Close()
	
	fmt.Println("✅ 成功连接到 MCP Server (联网搜索功能已挂载)")

	// 3. 初始化大模型客户端
	llmClient := llm.NewClient()

	// 4. 设置测试提示词
	systemPrompt := "你是一个有用的 AI 助手。如果用户问关于知识类或者不知道的事情，请使用百科工具联网搜索后回答。"
	userPrompt := "请问‘黑神话悟空’是什么？请用百科工具查一下并给我详细解释。"

	fmt.Printf("\n用户提问: %s\n", userPrompt)
	fmt.Println("--- 开始思考并调用工具... ---")

	// 5. 发起请求并允许大模型调用 MCP 工具
	reqCtx, cancel := context.WithTimeout(ctx, 120*time.Second) // 增加超时时间以等待工具执行
	defer cancel()

	startTime := time.Now()
	
	// 将 mcpClient 传入 Generate 方法，开启 Tool Calling 循环
	result, err := llmClient.Generate(reqCtx, systemPrompt, nil, userPrompt, mcpClient)
	if err != nil {
		log.Fatalf("❌ API 调用失败: %v", err)
	}

	// 6. 打印最终结果
	fmt.Printf("\n✅ 回答生成完毕！总耗时: %v\n", time.Since(startTime))
	fmt.Println("🤖 模型最终回复：")
	fmt.Println("--------------------------------------------------")
	fmt.Println(result)
	fmt.Println("--------------------------------------------------")
}
