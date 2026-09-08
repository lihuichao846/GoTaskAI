// Package events 提供 Worker 与 API 之间实时事件（任务更新、工具事件）的独立消息通道。
//
// 设计动机：实时事件原先与 Asynq 队列、任务/Agent 缓存共用同一个 Redis。
// 为了让任务队列与实时事件通道解耦（避免相互影响、单一 Redis 故障波及全链路），
// 这里改用 NATS：API 进程内嵌启动 nats-server，Worker 与 API 均作为客户端接入。
// Redis 仅保留队列（Asynq）与缓存职责，不再承担事件广播。
package events

import (
	"encoding/json"
	"errors"
	"log"
	"net/url"
	"strconv"
	"time"

	"github.com/nats-io/nats-server/v2/server"
	"github.com/nats-io/nats.go"
)

// errServerNotReady 表示内嵌 nats-server 在超时时间内未能就绪。
var errServerNotReady = errors.New("embedded nats-server not ready within timeout")

// 事件频道名（与原 Redis Pub/Sub 频道保持一致，避免破坏消费端语义）。
const (
	SubjectTaskUpdates = "global_task_updates" // 任务状态更新
	SubjectToolEvents  = "global_tool_events"  // 工具调用过程事件
	SubjectTaskCancel  = "global_task_cancel"  // 任务取消控制指令（广播到所有 worker 进程）
)

// EventBus 封装 NATS 客户端，提供发布与订阅能力。
type EventBus struct {
	conn *nats.Conn
	subs []*nats.Subscription
}

// New 建立到指定 NATS 地址的客户端连接。
func New(url string) (*EventBus, error) {
	nc, err := nats.Connect(url, nats.RetryOnFailedConnect(false), nats.MaxReconnects(-1))
	if err != nil {
		return nil, err
	}
	return &EventBus{conn: nc}, nil
}

// Close 关闭连接并反注册所有订阅。
func (b *EventBus) Close() {
	for _, sub := range b.subs {
		_ = sub.Unsubscribe()
	}
	if b.conn != nil {
		b.conn.Close()
	}
}

// PublishJSON 将任意对象序列化为 JSON 后发布到指定频道。
func (b *EventBus) PublishJSON(subject string, v any) error {
	data, err := json.Marshal(v)
	if err != nil {
		return err
	}
	return b.conn.Publish(subject, data)
}

// Subscribe 订阅指定频道，收到消息时把原始消息交给 handler 处理。
// 反序列化由调用方完成，从而让订阅方能拿到完整对象做业务分发。
func (b *EventBus) Subscribe(subject string, handler func(msg *nats.Msg)) error {
	sub, err := b.conn.Subscribe(subject, handler)
	if err != nil {
		return err
	}
	if err := b.conn.Flush(); err != nil {
		return err
	}
	b.subs = append(b.subs, sub)
	return nil
}

// StartEmbeddedServer 在当前进程内嵌启动一个 nats-server。
// 固定监听 0.0.0.0（兼容容器网络），端口从地址中解析。
// 返回 server 实例，可用于优雅关闭。仅应由一个进程（目前为 API）调用。
func StartEmbeddedServer(addr string) (*server.Server, error) {
	opts := &server.Options{
		Host:                  "0.0.0.0",
		Port:                  -1,
		NoLog:                 false,
		NoSigs:                true,
		MaxConn:               1024,
		DisableShortFirstPing: false,
	}
	if u, err := url.Parse(addr); err == nil && u.Port() != "" {
		if port, perr := strconv.Atoi(u.Port()); perr == nil {
			opts.Port = port
		}
	}

	ns, err := server.NewServer(opts)
	if err != nil {
		return nil, err
	}

	ns.Start()
	if !ns.ReadyForConnections(5 * time.Second) {
		ns.Shutdown()
		return nil, errServerNotReady
	}
	log.Printf("[events] embedded nats-server started at %s (port %d)", addr, opts.Port)
	return ns, nil
}
