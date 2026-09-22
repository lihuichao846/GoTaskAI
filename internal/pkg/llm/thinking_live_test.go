package llm

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	"gotaskai/internal/config"
)

// TestLiveThinkingModes 走真实的 Client → thinkingDoer → DeepSeek 链路，
// 对比开启/关闭思考模式的实际 token 消耗。
// 需要真实网络与密钥，默认跳过；显式开启：
//
//	$env:LLM_LIVE_TEST=1 ; go test -run TestLiveThinkingModes ./internal/pkg/llm/
func TestLiveThinkingModes(t *testing.T) {
	if os.Getenv("LLM_LIVE_TEST") != "1" {
		t.Skip("需要真实 LLM 调用，设置 LLM_LIVE_TEST=1 后运行")
	}
	config.InitConfig("")
	c := NewClient()
	lc := config.AppConfig.LLM
	if lc.APIKey == "" || lc.BaseURL == "" {
		t.Skip("未配置 LLM api_key/base_url")
	}
	fmt.Printf("base_url=%s model=%s context_window=%d output_reserve=%d max_output_tokens=%d max_load_tokens=%d\n",
		lc.BaseURL, lc.Model, lc.ContextWindow, lc.OutputReserve, lc.MaxOutputTokens,
		config.AppConfig.Compress.MaxLoadTokens)

	question := "一个农夫要带狼、羊、白菜过河，船一次只能带一样东西，农夫必须在场。最少需要几次渡河？给出步骤与理由。"

	for _, mode := range []string{"disabled", "enabled"} {
		config.AppConfig.LLM.ThinkingMode = mode
		b := &Budget{MaxCalls: 1}
		ctx, cancel := context.WithTimeout(context.Background(), 300*time.Second)
		start := time.Now()
		ans, err := c.GenerateWithToolsAndObserver(ctx, lc.APIKey, lc.BaseURL, lc.Model,
			"你是严谨的助手，回答要简洁。", nil, question, nil, nil, b)
		cancel()
		if err != nil {
			t.Logf("mode=%s 调用失败(耗时 %.1fs): %v", mode, time.Since(start).Seconds(), err)
			continue
		}
		fmt.Printf("mode=%-8s 耗时=%.1fs in=%d out=%d cached=%d 回答字符数=%d\n",
			mode, time.Since(start).Seconds(), b.TokensIn, b.TokensOut, b.TokensCached, len([]rune(ans)))
	}
}
