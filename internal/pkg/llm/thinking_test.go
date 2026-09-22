package llm

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
)

const (
	testChatPath  = "https://api.deepseek.com/v1/chat/completions"
	testEmbedPath = "https://api.deepseek.com/v1/embeddings"
	testChatBody  = `{"model":"deepseek-v4-pro","messages":[{"role":"user","content":"hi"}]}`
)

func newTestReq(t *testing.T, ctx context.Context, url, body string) *http.Request {
	t.Helper()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, strings.NewReader(body))
	if err != nil {
		t.Fatalf("构造请求失败: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	return req
}

func decodeBody(t *testing.T, req *http.Request) map[string]any {
	t.Helper()
	raw, err := io.ReadAll(req.Body)
	if err != nil {
		t.Fatalf("读请求体失败: %v", err)
	}
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatalf("请求体不是合法 JSON: %v (raw=%s)", err, raw)
	}
	return m
}

func thinkingType(body map[string]any) string {
	v, ok := body["thinking"].(map[string]any)
	if !ok {
		return ""
	}
	s, _ := v["type"].(string)
	return s
}

// TestInjectThinkingByMode 覆盖三种情况：显式关闭 / 显式开启 / 未标记（沿用供应商默认）。
func TestInjectThinkingByMode(t *testing.T) {
	cases := []struct {
		name string
		ctx  context.Context
		want string
	}{
		{"disabled", withThinkingMode(context.Background(), "disabled"), "disabled"},
		{"enabled", withThinkingMode(context.Background(), "enabled"), "enabled"},
		{"未标记不注入", context.Background(), ""},
		{"空模式不注入", withThinkingMode(context.Background(), ""), ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := newTestReq(t, tc.ctx, testChatPath, testChatBody)
			out, err := injectThinking(req)
			if err != nil {
				t.Fatalf("injectThinking 出错: %v", err)
			}
			body := decodeBody(t, out)
			if got := thinkingType(body); got != tc.want {
				t.Fatalf("thinking.type = %q, 期望 %q", got, tc.want)
			}
			// 注入不得破坏原有字段
			if body["model"] != "deepseek-v4-pro" || body["messages"] == nil {
				t.Fatalf("原有字段被破坏: %v", body)
			}
		})
	}
}

// TestInjectThinkingPassthrough 注入失败/不适用时必须原样透传，绝不破坏请求。
func TestInjectThinkingPassthrough(t *testing.T) {
	disabled := withThinkingMode(context.Background(), "disabled")

	t.Run("非对话补全路径", func(t *testing.T) {
		req := newTestReq(t, disabled, testEmbedPath, testChatBody)
		out, err := injectThinking(req)
		if err != nil {
			t.Fatalf("injectThinking 出错: %v", err)
		}
		if got := thinkingType(decodeBody(t, out)); got != "" {
			t.Fatalf("embeddings 路径不该注入，实际 thinking.type=%q", got)
		}
	})

	t.Run("请求体不可解析", func(t *testing.T) {
		const malformed = `not-json`
		req := newTestReq(t, disabled, testChatPath, malformed)
		out, err := injectThinking(req)
		if err != nil {
			t.Fatalf("injectThinking 出错: %v", err)
		}
		raw, err := io.ReadAll(out.Body)
		if err != nil {
			t.Fatalf("读请求体失败: %v", err)
		}
		if string(raw) != malformed {
			t.Fatalf("请求体应原样发回，实际 %q", raw)
		}
		if int(out.ContentLength) != len(malformed) {
			t.Fatalf("ContentLength = %d, 期望 %d", out.ContentLength, len(malformed))
		}
	})
}

// TestApplyThinkingModeConfig 校验配置值到 ctx 标记的映射：只认 enabled/disabled，其余不标记。
func TestApplyThinkingModeConfig(t *testing.T) {
	for _, mode := range []string{"", "   ", "default", "off", "true"} {
		ctx := applyThinkingMode(context.Background(), mode)
		if _, marked := ctx.Value(thinkingModeKey{}).(string); marked {
			t.Fatalf("mode=%q 不该产生标记（应沿用供应商默认）", mode)
		}
	}
	for _, tc := range []struct{ in, want string }{
		{"disabled", "disabled"},
		{" DISABLED ", "disabled"},
		{"Enabled", "enabled"},
	} {
		ctx := applyThinkingMode(context.Background(), tc.in)
		got, _ := ctx.Value(thinkingModeKey{}).(string)
		if got != tc.want {
			t.Fatalf("mode=%q → %q, 期望 %q", tc.in, got, tc.want)
		}
	}
}

// TestApplyThinkingModePrecedence 既有标记优先于入口配置：
// 压缩摘要等辅助调用先标记 disabled，之后经过按主对话处理的 generate 入口时不得被改回 enabled。
func TestApplyThinkingModePrecedence(t *testing.T) {
	ctx := withThinkingMode(context.Background(), "disabled")

	for _, mode := range []string{"enabled", "disabled", ""} {
		got, _ := applyThinkingMode(ctx, mode).Value(thinkingModeKey{}).(string)
		if got != "disabled" {
			t.Fatalf("既有标记被 mode=%q 覆盖为 %q", mode, got)
		}
	}

	// 反向：先标记 enabled，后续 aux 默认（disabled）同样不得覆盖
	ctx = withThinkingMode(context.Background(), "enabled")
	got, _ := applyThinkingMode(ctx, "disabled").Value(thinkingModeKey{}).(string)
	if got != "enabled" {
		t.Fatalf("既有标记被覆盖为 %q", got)
	}
}

// TestThinkingDoerPatchesRequest 校验包装层确实改写了发给上游的请求。
type captureDoer struct{ body map[string]any }

func (c *captureDoer) Do(req *http.Request) (*http.Response, error) {
	c.body = decodeBodyNoT(req)
	return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader("{}"))}, nil
}

func decodeBodyNoT(req *http.Request) map[string]any {
	raw, err := io.ReadAll(req.Body)
	if err != nil {
		return nil
	}
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		return nil
	}
	return m
}

func TestThinkingDoerPatchesRequest(t *testing.T) {
	cap := &captureDoer{}
	doer := wrapDoerWithThinking(cap)

	req := newTestReq(t, withThinkingMode(context.Background(), "disabled"), testChatPath, testChatBody)
	if _, err := doer.Do(req); err != nil {
		t.Fatalf("Do 出错: %v", err)
	}
	if got := thinkingType(cap.body); got != "disabled" {
		t.Fatalf("上游收到的 thinking.type = %q, 期望 disabled", got)
	}

	// 幂等：重复包装不应嵌套
	if again := wrapDoerWithThinking(doer); again != doer {
		t.Fatalf("重复包装未去重")
	}
}
