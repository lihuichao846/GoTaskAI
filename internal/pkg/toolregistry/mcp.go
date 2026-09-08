package toolregistry

import (
	"context"
	"encoding/json"

	"gotaskai/internal/pkg/mcpclient"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/sashabaranov/go-openai"
	"github.com/sashabaranov/go-openai/jsonschema"
)

// MCPServerTool 把一个 MCP 服务中的单个工具适配为 Tool。
type MCPServerTool struct {
	wrapper *mcpclient.Wrapper
	tool    mcp.Tool
}

func NewMCPServerTool(wrapper *mcpclient.Wrapper, tool mcp.Tool) *MCPServerTool {
	return &MCPServerTool{wrapper: wrapper, tool: tool}
}

func (t *MCPServerTool) Name() string        { return t.tool.Name }
func (t *MCPServerTool) Description() string { return t.tool.Description }

func (t *MCPServerTool) ToOpenAITool() openai.Tool {
	var params jsonschema.Definition
	if b, err := json.Marshal(t.tool.InputSchema); err == nil {
		_ = json.Unmarshal(b, &params)
	}

	return openai.Tool{
		Type: openai.ToolTypeFunction,
		Function: &openai.FunctionDefinition{
			Name:        t.tool.Name,
			Description: t.tool.Description,
			Parameters:  params,
		},
	}
}

func (t *MCPServerTool) Execute(ctx context.Context, args map[string]any) (string, error) {
	result, err := t.wrapper.CallTool(ctx, t.tool.Name, args)
	if err != nil {
		return "", err
	}

	var content string
	for _, c := range result.Content {
		if text, ok := c.(mcp.TextContent); ok {
			content += text.Text + "\n"
			continue
		}
		if b, err := json.Marshal(c); err == nil {
			content += string(b) + "\n"
		}
	}
	return content, nil
}
