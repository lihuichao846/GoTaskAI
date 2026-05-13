package main

import (
	"context"
	"fmt"
	"log"
	"time"

	"gotaskai/internal/config"
	"gotaskai/internal/pkg/llm"
)

func main() {
	// 1. 初始化配置，读取刚刚修改的 yaml 文件
	config.InitConfig("config/config.yaml")
	cfg := config.AppConfig.LLM

	fmt.Println("=== 正在测试云大模型 API ===")
	fmt.Printf("Base URL: %s\n", cfg.BaseURL)
	fmt.Printf("Model:    %s\n", cfg.Model)
	fmt.Printf("API Key:  %s***\n", cfg.APIKey[:3]) // 隐藏完整 Key

	// 2. 初始化大模型客户端
	client := llm.NewClient()

	// 3. 设置测试的提示词
	systemPrompt := "你是一个有用的 AI 助手。"
	userPrompt := "请用一句话做个自我介绍，并告诉我今天天气怎么样（假设你是万能的）。"

	fmt.Println("\n--- 发送请求中... ---")

	// 4. 发起请求并设置超时
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second) // 增加超时时间到 60 秒
	defer cancel()

	startTime := time.Now()
	result, err := client.Generate(ctx, systemPrompt, nil, userPrompt, nil)

	if err != nil {
		log.Fatalf("❌ API 调用失败: %v", err)
	}

	// 5. 打印结果
	fmt.Printf("✅ 调用成功！耗时: %v\n\n", time.Since(startTime))
	fmt.Println("🤖 模型回复：")
	fmt.Println("--------------------------------------------------")
	fmt.Println(result)
	fmt.Println("--------------------------------------------------")
}
