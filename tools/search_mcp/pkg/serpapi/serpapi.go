package serpapi

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"

	"github.com/mark3labs/mcp-go/mcp"
)

// SearchResponse 代表 SerpApi 返回的 JSON 结构
type SearchResponse struct {
	OrganicResults []struct {
		Title   string `json:"title"`
		Link    string `json:"link"`
		Snippet string `json:"snippet"`
	} `json:"organic_results"`
	Error string `json:"error"`
}

// GetTool 返回 SerpApi 搜索引擎工具的定义
func GetTool() mcp.Tool {
	return mcp.NewTool("internet_search",
		mcp.WithDescription("这是首选且最强大的全局搜索引擎工具（谷歌搜索）。当用户询问任何你不确定的知识、历史、代码问题、最新资讯或百科时，都必须优先调用此工具获取答案。"),
		mcp.WithString("query",
			mcp.Required(),
			mcp.Description("用于搜索引擎搜索的关键词。请务必使用核心名词或问句，如 '今天武汉天气' 或 '量子力学最新进展'"),
		),
	)
}

// Handler 实现 SerpApi 联网搜索的实际逻辑
func Handler(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	args, ok := request.Params.Arguments.(map[string]interface{})
	if !ok {
		return mcp.NewToolResultError("arguments must be a map"), nil
	}

	query, ok := args["query"].(string)
	if !ok {
		return mcp.NewToolResultError("query must be a string"), nil
	}

	apiKey := "5c8e77bae8cdb7c83dbd42416595dbfdd7ca16f430f2613829dac2d045a97f24"
	searchURL := fmt.Sprintf("https://serpapi.com/search.json?engine=google&q=%s&api_key=%s&hl=zh-cn&gl=cn", url.QueryEscape(query), apiKey)

	req, err := http.NewRequestWithContext(ctx, "GET", searchURL, nil)
	if err != nil {
		return mcp.NewToolResultError(fmt.Sprintf("failed to create request: %v", err)), nil
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return mcp.NewToolResultError(fmt.Sprintf("failed to execute request: %v", err)), nil
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		return mcp.NewToolResultError(fmt.Sprintf("search API returned status: %d", resp.StatusCode)), nil
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return mcp.NewToolResultError(fmt.Sprintf("failed to read response: %v", err)), nil
	}

	var searchResp SearchResponse
	if err := json.Unmarshal(body, &searchResp); err != nil {
		return mcp.NewToolResultError(fmt.Sprintf("failed to parse JSON response: %v", err)), nil
	}

	if searchResp.Error != "" {
		return mcp.NewToolResultError(fmt.Sprintf("SerpApi error: %s", searchResp.Error)), nil
	}

	if len(searchResp.OrganicResults) == 0 {
		return mcp.NewToolResultText("No results found in search engine for the query. Please tell the user that you don't know the answer or the information is not available."), nil
	}

	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("以下是来自谷歌搜索关于 '%s' 的严格事实搜索结果：\n", query))
	sb.WriteString("【重要指令】：请严格基于以下内容回答用户的问题。如果以下内容不足以回答问题，请直接告知用户“搜索到的资料不足”。不要自行编造未出现在以下内容中的事实！\n\n")

	limit := 5 // 限制返回前5条结果
	if len(searchResp.OrganicResults) < limit {
		limit = len(searchResp.OrganicResults)
	}

	for i := 0; i < limit; i++ {
		res := searchResp.OrganicResults[i]
		sb.WriteString(fmt.Sprintf("%d. Title: %s\n", i+1, res.Title))
		sb.WriteString(fmt.Sprintf("   Summary: %s\n", res.Snippet))
		sb.WriteString(fmt.Sprintf("   Link: %s\n\n", res.Link))
	}

	return mcp.NewToolResultText(sb.String()), nil
}
