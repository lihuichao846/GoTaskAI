package llm

import (
	"context"
	"encoding/json"
	"fmt"
	"gotaskai/internal/config"
	"gotaskai/internal/pkg/mcpclient"
	"log"
	"regexp"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/sashabaranov/go-openai"
	"github.com/sashabaranov/go-openai/jsonschema"
)

type Client struct {
	client         *openai.Client
	model          string
	embeddingModel string
}

// NewClient 初始化一个兼容 OpenAI 格式的大模型客户端
func NewClient() *Client {
	cfg := config.AppConfig.LLM

	clientConfig := openai.DefaultConfig(cfg.APIKey)
	if cfg.BaseURL != "" {
		clientConfig.BaseURL = cfg.BaseURL
	}

	embModel := cfg.EmbeddingModel
	if embModel == "" {
		embModel = "bge-m3" // 默认使用 bge-m3
	}

	return &Client{
		client:         openai.NewClientWithConfig(clientConfig),
		model:          cfg.Model,
		embeddingModel: embModel,
	}
}

// CreateEmbedding 调用大模型将一段文本向量化
func (c *Client) CreateEmbedding(ctx context.Context, text string) ([]float32, error) {
	req := openai.EmbeddingRequest{
		Input: []string{text},
		Model: openai.EmbeddingModel(c.embeddingModel),
	}

	resp, err := c.client.CreateEmbeddings(ctx, req)
	if err != nil {
		return nil, fmt.Errorf("embedding creation failed: %w", err)
	}

	if len(resp.Data) == 0 {
		return nil, fmt.Errorf("no embedding data returned from API")
	}

	return resp.Data[0].Embedding, nil
}

// mcpToolToOpenAITool 将 MCP Tool 转换为 OpenAI Tool
func mcpToolToOpenAITool(t mcp.Tool) openai.Tool {
	var params jsonschema.Definition
	// 将 mcp.Tool 的 InputSchema 映射到 openai 的 jsonschema
	schemaBytes, _ := json.Marshal(t.InputSchema)
	json.Unmarshal(schemaBytes, &params)

	return openai.Tool{
		Type: openai.ToolTypeFunction,
		Function: &openai.FunctionDefinition{
			Name:        t.Name,
			Description: t.Description,
			Parameters:  params,
		},
	}
}

// Generate 调用大模型生成文本回复，支持传入多轮历史对话上下文，并支持可选的 MCP 客户端用于工具调用
func (c *Client) Generate(ctx context.Context, systemPrompt string, history []openai.ChatCompletionMessage, userPrompt string, mcpClient *mcpclient.Wrapper) (string, error) {
	messages := make([]openai.ChatCompletionMessage, 0, len(history)+2)

	// 1. 添加 System Prompt
	messages = append(messages, openai.ChatCompletionMessage{
		Role:    openai.ChatMessageRoleSystem,
		Content: systemPrompt,
	})

	// 2. 添加历史上下文记录
	messages = append(messages, history...)

	// 3. 添加当前用户提问
	messages = append(messages, openai.ChatCompletionMessage{
		Role:    openai.ChatMessageRoleUser,
		Content: userPrompt,
	})

	var tools []openai.Tool
	var mcpTools []mcp.Tool
	var err error

	// 如果传入了 MCP 客户端，获取工具列表
	if mcpClient != nil {
		mcpTools, err = mcpClient.GetTools(ctx)
		if err != nil {
			log.Printf("Warning: failed to get tools from MCP server: %v\n", err)
		} else {
			for _, t := range mcpTools {
				tools = append(tools, mcpToolToOpenAITool(t))
			}
		}
	}

	for {
		req := openai.ChatCompletionRequest{
			Model:    c.model,
			Messages: messages,
		}

		if len(tools) > 0 {
			req.Tools = tools
		}

		resp, err := c.client.CreateChatCompletion(ctx, req)
		if err != nil {
			return "", fmt.Errorf("AI API call failed: %w", err)
		}

		if len(resp.Choices) == 0 {
			return "", fmt.Errorf("AI returned empty response")
		}

		msg := resp.Choices[0].Message

		// ====== 针对某些开源模型 (如 Qwen 2.5) 的特殊处理 ======
		// 它们有时没有正确使用 openai 的 tool_calls 字段，而是直接将 XML 标签写在了 Content 里
		// 或者根本没有 </tool_call> 标签，仅仅是在内容中输出了工具调用的 JSON 格式
		if len(msg.ToolCalls) == 0 && msg.Content != "" {
			// 使用 (?s) 允许跨行匹配，尝试在内容中匹配 JSON 对象形式的 tool call
			re := regexp.MustCompile(`(?s)\{\s*"name"\s*:\s*"[^"]+"\s*,\s*"arguments"\s*:\s*\{.*?\}\s*\}`)
			match := re.FindString(msg.Content)

			if match != "" {
				var callData struct {
					Name      string                 `json:"name"`
					Arguments map[string]interface{} `json:"arguments"`
				}
				if err := json.Unmarshal([]byte(match), &callData); err == nil && callData.Name != "" {
					// 提取成功，手动将其转化为 ToolCall
					argsBytes, _ := json.Marshal(callData.Arguments)
					msg.ToolCalls = []openai.ToolCall{
						{
							ID:   "call_" + callData.Name,
							Type: openai.ToolTypeFunction,
							Function: openai.FunctionCall{
								Name:      callData.Name,
								Arguments: string(argsBytes),
							},
						},
					}
					// 清空内容避免返回脏字符
					msg.Content = ""
				} else {
					log.Printf("Regex matched but failed to unmarshal JSON: %v, match: %s", err, match)
				}
			}
		}
		// =======================================================

		// 在特殊处理之后，再把最终被修正过的 msg 加到历史上下文中
		messages = append(messages, msg)

		// 如果模型没有调用工具，直接返回结果
		if len(msg.ToolCalls) == 0 {
			return msg.Content, nil
		}

		// 处理工具调用
		for _, toolCall := range msg.ToolCalls {
			if toolCall.Type == openai.ToolTypeFunction {
				log.Printf("AI invoked tool: %s\n", toolCall.Function.Name)

				var args map[string]interface{}
				if err := json.Unmarshal([]byte(toolCall.Function.Arguments), &args); err != nil {
					log.Printf("Failed to parse tool arguments: %v\n", err)
					continue
				}

				// 调用 MCP Server 实际执行工具
				result, err := mcpClient.CallTool(ctx, toolCall.Function.Name, args)
				var contentStr string

				if err != nil {
					log.Printf("Tool execution failed: %v\n", err)
					contentStr = fmt.Sprintf("Error executing tool: %v", err)
				} else {
					// 提取 MCP 返回的结果文本
					for _, c := range result.Content {
						if textContent, ok := c.(mcp.TextContent); ok {
							contentStr += textContent.Text + "\n"
						} else {
							// 尝试序列化其他内容类型
							b, _ := json.Marshal(c)
							contentStr += string(b) + "\n"
						}
					}
					log.Printf("Tool raw response: %s\n", contentStr)
				}

				// 将工具执行结果作为 Tool 消息添加回上下文中
				messages = append(messages, openai.ChatCompletionMessage{
					Role:       openai.ChatMessageRoleTool,
					Content:    contentStr,
					Name:       toolCall.Function.Name,
					ToolCallID: toolCall.ID,
				})
			}
		}
		// 循环继续，将带有工具结果的 messages 发送给 LLM 进行最终回答
	}
}
