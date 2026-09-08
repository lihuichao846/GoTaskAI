// eval 命令行入口：对内置评测数据集执行固定采样、多次独立运行，
// 输出统计稳定的质量报告（均值、方差、置信区间），并可作为 CI 回归闸门。
//
// 用法：
//
//	go run ./cmd/eval -threshold 0.8
//
// 当任一用例的工具命中率均值低于 -threshold 时，进程以非零退出码结束。
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"time"

	"gotaskai/internal/config"
	"gotaskai/internal/eval"
	"gotaskai/internal/pkg/llm"
	"gotaskai/internal/pkg/mcpclient"
	"gotaskai/internal/pkg/toolregistry"
)

func main() {
	threshold := flag.Float64("threshold", 0.8, "最低工具命中率均值阈值，低于此值评测失败")
	flag.Parse()

	config.InitConfig("config/config.yaml")

	// 初始化工具白名单：内置工具 + 可选的 search_mcp。
	toolRegistry := toolregistry.NewRegistry()
	toolregistry.RegisterBuiltins(toolRegistry)

	mcpCtx := context.Background()
	mcpServerPath := config.ResolveProjectPath("bin", "search_mcp.exe")
	if mcpClient, err := mcpclient.NewWrapper(mcpCtx, mcpServerPath); err == nil {
		if mcpTools, terr := mcpClient.GetTools(mcpCtx); terr == nil {
			for _, t := range mcpTools {
				toolRegistry.Register(toolregistry.NewMCPServerTool(mcpClient, t))
			}
		}
		defer mcpClient.Close()
	}

	// 内置示例评测数据集：问题 + 期望工具调用 + 期望答案（意图）。
	cases := []eval.Case{
		{Question: "现在几点了？", ExpectedTools: []string{"get_current_time"}, ExpectedAnswer: "当前日期和时间"},
		{Question: "今天是几月几号？", ExpectedTools: []string{"get_current_time"}, ExpectedAnswer: "今天的日期"},
	}

	cfg := eval.Config{
		Model:      config.AppConfig.LLM.Model,
		Sampling:   &llm.Sampling{Temperature: 0.2, TopP: 0.9},
		Iterations: 5,
	}

	runner := eval.NewRunner(llm.NewClient(), toolRegistry.List(), "你是一个有用的 AI 助手。", "", "")
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	report, err := runner.Run(ctx, cases, cfg)
	if err != nil {
		fmt.Fprintf(os.Stderr, "eval failed: %v\n", err)
		os.Exit(2)
	}

	out, err := json.MarshalIndent(report, "", "  ")
	if err != nil {
		fmt.Fprintf(os.Stderr, "marshal report failed: %v\n", err)
		os.Exit(2)
	}
	fmt.Println(string(out))

	// 回归护栏：任一用例的工具命中率均值低于阈值，返回非零退出码，便于接入 CI。
	for _, cr := range report.Cases {
		if cr.ToolMean < *threshold {
			fmt.Fprintf(os.Stderr, "eval FAILED: case %q tool_mean=%.3f below threshold %.3f\n", cr.Question, cr.ToolMean, *threshold)
			os.Exit(1)
		}
	}
}
