package toolregistry

import (
	"context"
	"sync"

	"github.com/sashabaranov/go-openai"
)

// Tool 是 LLM 可调用的统一工具抽象，内置工具与 MCP 工具都实现该接口。
type Tool interface {
	Name() string
	Description() string
	ToOpenAITool() openai.Tool
	Execute(ctx context.Context, args map[string]any) (string, error)
}

// Registry 维护一个有序的工具集合，供 Worker 按 Agent 白名单组装。
type Registry struct {
	mu    sync.RWMutex
	tools map[string]Tool
	order []string
}

func NewRegistry() *Registry {
	return &Registry{
		tools: make(map[string]Tool),
	}
}

// Register 注册一个工具，同名工具会被覆盖。
func (r *Registry) Register(t Tool) {
	r.mu.Lock()
	defer r.mu.Unlock()

	if _, exists := r.tools[t.Name()]; !exists {
		r.order = append(r.order, t.Name())
	}
	r.tools[t.Name()] = t
}

// Get 按名称获取工具。
func (r *Registry) Get(name string) (Tool, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	t, ok := r.tools[name]
	return t, ok
}

// List 按注册顺序返回全部工具。
func (r *Registry) List() []Tool {
	r.mu.RLock()
	defer r.mu.RUnlock()

	out := make([]Tool, 0, len(r.order))
	for _, name := range r.order {
		if t, ok := r.tools[name]; ok {
			out = append(out, t)
		}
	}
	return out
}

// ToOpenAITools 将全部工具转换为 OpenAI 工具声明。
func (r *Registry) ToOpenAITools() []openai.Tool {
	list := r.List()
	out := make([]openai.Tool, 0, len(list))
	for _, t := range list {
		out = append(out, t.ToOpenAITool())
	}
	return out
}

// Execute 执行指定工具并返回文本结果。
func (r *Registry) Execute(ctx context.Context, name string, args map[string]any) (string, error) {
	t, ok := r.Get(name)
	if !ok {
		return "", &NotFoundError{Name: name}
	}
	return t.Execute(ctx, args)
}

// NotFoundError 表示请求的工具未注册。
type NotFoundError struct {
	Name string
}

func (e *NotFoundError) Error() string {
	return "tool not found: " + e.Name
}
