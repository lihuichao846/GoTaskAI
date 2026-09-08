package mcpclient

import (
	"context"
	"sync"
)

// Pool 维护多个 MCP 连接的缓存，按 ID 懒加载并复用，避免每个任务都拉起子进程。
type Pool struct {
	mu      sync.Mutex
	clients map[string]*Wrapper
}

// NewPool 创建一个空的 MCP 连接池。
func NewPool() *Pool {
	return &Pool{clients: make(map[string]*Wrapper)}
}

// Get 返回指定 ID 的 MCP 连接；不存在时按配置创建并缓存。
// url 优先（远程 SSE/HTTP），否则回退到 stdio 子进程（command/args/env）。
func (p *Pool) Get(ctx context.Context, id, command, url string, args []string, env map[string]string) (*Wrapper, error) {
	p.mu.Lock()
	defer p.mu.Unlock()

	if w, ok := p.clients[id]; ok {
		return w, nil
	}

	var w *Wrapper
	var err error
	if url != "" {
		w, err = NewHTTPWrapper(ctx, url)
	} else {
		w, err = NewWrapperWithEnv(ctx, command, env, args...)
	}
	if err != nil {
		return nil, err
	}
	p.clients[id] = w
	return w, nil
}

// Close 关闭并清空所有 MCP 连接。
func (p *Pool) Close() {
	p.mu.Lock()
	defer p.mu.Unlock()

	for id, w := range p.clients {
		_ = w.Close()
		delete(p.clients, id)
	}
}
