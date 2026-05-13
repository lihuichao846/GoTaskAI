package main

import (
	"gotaskai/tools/search_mcp/pkg/serpapi"

	"github.com/mark3labs/mcp-go/server"
)

func main() {
	// 1. 创建 MCP Server
	s := server.NewMCPServer(
		"Multi-Tool-Search",
		"1.0.0",
	)

	// 2. 注册 SerpApi 谷歌搜索 Tool (作为唯一的全局搜索工具)
	s.AddTool(serpapi.GetTool(), serpapi.Handler)

	// 3. 通过 Stdio 启动 Server
	if err := server.ServeStdio(s); err != nil {
		panic(err)
	}
}
