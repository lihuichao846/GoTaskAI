package mcpclient

import (
	"context"
	"fmt"
	"log"
	"os"

	"github.com/mark3labs/mcp-go/client"
	"github.com/mark3labs/mcp-go/mcp"
)

// Wrapper 封装了对外部 MCP Server 的连接
type Wrapper struct {
	Client *client.Client
}

// NewWrapper 启动一个 MCP Server 子进程，并通过 Stdio 与之建立连接
func NewWrapper(ctx context.Context, command string, args ...string) (*Wrapper, error) {
	return NewWrapperWithEnv(ctx, command, nil, args...)
}

// NewWrapperWithEnv 启动一个 Stdio MCP Server，可额外注入指定的环境变量。
func NewWrapperWithEnv(ctx context.Context, command string, extraEnv map[string]string, args ...string) (*Wrapper, error) {
	env := os.Environ()
	for k, v := range extraEnv {
		env = append(env, k+"="+v)
	}

	c, err := client.NewStdioMCPClient(command, env, args...)
	if err != nil {
		return nil, fmt.Errorf("failed to create stdio mcp client: %w", err)
	}
	return initWrapper(ctx, c)
}

// NewHTTPWrapper 通过远程 URL（SSE/HTTP）连接 MCP Server。
func NewHTTPWrapper(ctx context.Context, url string) (*Wrapper, error) {
	c, err := client.NewSSEMCPClient(url)
	if err != nil {
		return nil, fmt.Errorf("failed to create sse mcp client: %w", err)
	}
	return initWrapper(ctx, c)
}

// initWrapper 对已创建的 MCP Client 执行握手初始化。
func initWrapper(ctx context.Context, c *client.Client) (*Wrapper, error) {
	initRequest := mcp.InitializeRequest{}
	initRequest.Params.ProtocolVersion = mcp.LATEST_PROTOCOL_VERSION
	initRequest.Params.ClientInfo = mcp.Implementation{
		Name:    "GoTaskAI-Client",
		Version: "1.0.0",
	}

	initResult, err := c.Initialize(ctx, initRequest)
	if err != nil {
		return nil, fmt.Errorf("failed to initialize mcp connection: %w", err)
	}

	log.Printf("Connected to MCP Server: %s (v%s)", initResult.ServerInfo.Name, initResult.ServerInfo.Version)

	return &Wrapper{
		Client: c,
	}, nil
}

// GetTools 获取 MCP Server 提供的所有工具
func (m *Wrapper) GetTools(ctx context.Context) ([]mcp.Tool, error) {
	req := mcp.ListToolsRequest{}
	resp, err := m.Client.ListTools(ctx, req)
	if err != nil {
		return nil, err
	}
	return resp.Tools, nil
}

// CallTool 执行具体的 MCP 工具
func (m *Wrapper) CallTool(ctx context.Context, name string, arguments map[string]interface{}) (*mcp.CallToolResult, error) {
	req := mcp.CallToolRequest{}
	req.Params.Name = name
	req.Params.Arguments = arguments

	return m.Client.CallTool(ctx, req)
}

// Close 关闭连接和子进程
func (m *Wrapper) Close() error {
	if m.Client != nil {
		return m.Client.Close()
	}
	return nil
}
