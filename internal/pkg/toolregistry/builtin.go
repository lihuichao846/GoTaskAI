package toolregistry

import (
	"context"
	"time"

	"github.com/sashabaranov/go-openai"
	"github.com/sashabaranov/go-openai/jsonschema"
)

// CurrentTimeTool 是平台内置工具，返回当前时间。
type CurrentTimeTool struct{}

func (CurrentTimeTool) Name() string        { return "get_current_time" }
func (CurrentTimeTool) Description() string { return "获取当前的日期和时间" }

func (CurrentTimeTool) ToOpenAITool() openai.Tool {
	return openai.Tool{
		Type: openai.ToolTypeFunction,
		Function: &openai.FunctionDefinition{
			Name:        "get_current_time",
			Description: "获取当前的日期和时间",
			Parameters: jsonschema.Definition{
				Type:       jsonschema.Object,
				Properties: map[string]jsonschema.Definition{},
			},
		},
	}
}

func (CurrentTimeTool) Execute(_ context.Context, _ map[string]any) (string, error) {
	return time.Now().Format("2006-01-02 15:04:05"), nil
}

// RegisterBuiltins 注册所有平台内置工具。
func RegisterBuiltins(r *Registry) {
	r.Register(CurrentTimeTool{})
}
