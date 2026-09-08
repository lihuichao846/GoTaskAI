# NATS 在 GoTaskAI 中的角色与技术选型

> 本文面向「面试 & 复盘」场景，讲清楚三件事：NATS 是什么、它在 GoTaskAI 里具体做了什么、以及为什么选它而不是 Redis Pub/Sub 或 Kafka。
> 配合 [项目面试总结.md](../interview/项目面试总结.md)、[microservice_architecture_design.md](./microservice_architecture_design.md) 一起看，可快速把「微服务解耦 + 实时推送」这条线讲完整。

---

## 一、一句话介绍

NATS 是一个**用 Go 编写的、云原生的轻量级消息系统**，核心是「基于主题（Subject）的发布/订阅（Pub/Sub）」模型。它由一个高性能的代理 `nats-server` 和对应客户端库 `nats.go` 组成，主打**低延迟、极轻量、易于内嵌部署**。

在 GoTaskAI 里，它承担的是 **Worker 与 API 两个进程之间「实时事件」的独立消息通道**：Worker 把任务状态/工具调用事件发布到 NATS 主题，API 订阅后再转成 SSE 推给前端浏览器。

---

## 二、NATS 的核心概念（先理解模型）

在进项目细节前，先建立对 NATS 的几个基础认知：

| 概念 | 含义 | 对应本项目 |
|---|---|---|
| **Subject（主题）** | 消息的「地址/频道」，类似邮件主题，由 `.` 分隔的字符串组成 | `global_task_updates`、`global_tool_events` |
| **Publish（发布）** | 生产者把消息发到某个 Subject | Worker 侧 `PublishJSON(...)` |
| **Subscribe（订阅）** | 消费者订阅某个 Subject 接收消息，支持通配符 | API 侧 `Subscribe(...)` |
| **nats-server（代理）** | 独立/内嵌运行的 broker，负责消息路由 | API 进程内嵌启动（`StartEmbeddedServer`） |
| **nats.go（客户端库）** | Go 官方客户端，被 API 与 Worker 共同使用 | `events.EventBus` 封装 |

NATS 有两种核心模式：

1. **Core NATS（本项目使用）**：纯粹的「发完即走、最多一次（at-most-once）」的 Pub/Sub，消息存入内存、不落盘，适合**实时、短暂、丢了也无所谓**的事件广播（如进度通知）。
2. **NATS JetStream**：带持久化、重放、多消费者组的新一代能力，适合需要可靠投递的场景。**本项目未使用**——因为任务状态本身就是权威源在 MySQL/Redis，前端丢了实时事件可以先拉全量快照，再靠后续增量补齐。

> 一句话：本项目用的是 **Core NATS**，看重的是它的「轻量、低延迟、可内嵌、Go 原生」，而不是它的持久化能力。

---

## 三、在本项目中的具体作用

### 3.1 背景：为什么需要一条「事件通道」

GoTaskAI 后端被拆成两个独立进程：

- **API Server**（接入层）：接 HTTP、鉴权、限流、托管前端、建任务，并**通过 SSE 把实时状态推给浏览器**。
- **Worker Node**（计算层）：异步消费 Asynq 队列、做 RAG/KAG、调 LLM、更新任务状态。

问题来了：Worker 更新了任务状态，**它怎么告诉 API**？两者是独立的 `.exe`，不能像单体那样直接调函数。

NATS 就是来解决这个「跨进程实时通信」的：**Worker 发布事件 → NATS 路由 → API 订阅 → SSE 推前端**。

### 3.2 部署拓扑：API 内嵌 server，Worker 作客户端

这是本项目最关键、也最「省事」的设计——**不额外部署 NATS 容器，而是把 nats-server 直接内嵌进 API 进程**。

```
┌─────────── API Server 进程 ───────────┐
│                                       │
│   ┌───────────────┐                   │
│   │ nats-server   │ ← 内嵌启动          │
│   │ (embedded)    │                   │
│   └───────┬───────┘                   │
│           │ nats.go 客户端             │
│    ┌──────┴───────┐                   │
│    │  EventBus    │                   │
│    └──────┬───────┘                   │
│           │ Subscribe                │
│           ▼                           │
│    TaskManager.listenForGlobalUpdates │
│           │ broadcast → SSE           │
└───────────┼───────────────────────────┘
            │ nats://api:4222（容器）/ nats://0.0.0.0:4222（本地）
            ▼
┌─────────── Worker Node 进程 ──────────┐
│   EventBus (nats.go 客户端)            │
│      │ PublishJSON                     │
│      └─ 更新任务状态 / 工具事件          │
└───────────────────────────────────────┘
```

- **本地开发**：`config.yaml` 中 `eventbus.url = nats://0.0.0.0:4222`，API 内嵌 server 监听 `0.0.0.0:4222`，Worker 直连本机该地址。
- **容器部署**：`config.docker.yaml` 中 `eventbus.url = nats://api:4222`，即 Worker 通过 Docker 网络访问 **API 容器**内嵌的 nats-server，`embedded: true` 仍只对 API 生效。

### 3.3 两个主题：任务更新 + 工具事件

NATS 围绕两个 Subject 工作（见 [events.go](file:///d:/Program%20Files/GoTaskAI/internal/events/events.go#L24-L28)）：

| Subject | 发布方 | 内容 | 订阅方 | 最终去向 |
|---|---|---|---|---|
| `global_task_updates` | Worker | 任务的完整状态对象（`model.Task`） | API | SSE `task_update` |
| `global_tool_events` | Worker | 工具调用过程（`model.ToolCallEvent`） | API | SSE `tool_call` |

> 主题名与原先 Redis Pub/Sub 的频道名保持一致（注释里也写明了），这样消费端语义无需改动，只是「通道」从 Redis 换成了 NATS。

### 3.4 一条事件的完整链路（结合代码）

以「任务状态更新」为例（[manager.go](file:///d:/Program%20Files/GoTaskAI/internal/queue/manager.go#L350-L370) 的 `UpdateTask`）：

```go
func (m *TaskManager) UpdateTask(t *model.Task) {
	t.UpdatedAt = time.Now()
	m.db.Save(t)        // 1. 更新 MySQL（权威状态）
	m.cacheTask(t)      // 2. 更新 Redis 缓存

	// 3. 跨进程广播：Worker 通过 NATS 发布，API 订阅后转发给 SSE
	if m.eventBus != nil {
		_ = m.eventBus.PublishJSON(events.SubjectTaskUpdates, t)
	}
	m.broadcast(t)      // 4. 进程内广播（兼容单机/内嵌场景）
}
```

完整时序：

1. Worker 调 `UpdateTask` → 写 MySQL + Redis 缓存 → `eventBus.PublishJSON("global_task_updates", t)`。
2. NATS server（API 内嵌）收到消息，路由给所有已订阅 `global_task_updates` 的客户端。
3. API 进程的 `TaskManager` 在 `NewTaskManager` 时已启动 `go m.listenForGlobalUpdates()`（[manager.go](file:///d:/Program%20Files/GoTaskAI/internal/queue/manager.go#L54-L79)），该协程一直 `Subscribe` NATS 主题。
4. `listenForGlobalUpdates` 反序列化出 `model.Task`，调用 `m.broadcast(t)`。
5. `broadcast` 根据 `t.UserID` 找到该用户的所有 SSE channel，把消息塞进缓冲 channel（channel 满则丢弃，防止阻塞）。
6. `StreamTasks`（[handler.go](file:///d:/Program%20Files/GoTaskAI/internal/api/handler.go#L363-L408)）从 channel 取到消息，用 `c.SSEvent("task_update", ...) + Flush` 推给浏览器。

工具事件 `global_tool_events` 走完全一样的流程，只是由 `PublishToolEvent`（[manager.go](file:///d:/Program%20Files/GoTaskAI/internal/queue/manager.go#L172-L178)）发布、`broadcastToolEvent` 分发、最终以 SSE `tool_call` 事件呈现。

> 关键点：**NATS 只负责「跨进程搬运」，SSE 负责「到浏览器最后一公里」**。二者衔接点是 API 进程里的 `broadcast`（把 NATS 消息转成用户 channel 里的 SSE 事件）。

### 3.5 与 Redis 的职责划分（解耦核心）

改动前，Redis「身兼三职」，一条事件同时挤在 Redis 上：

- 任务队列（Asynq 底层）
- 任务/Agent 缓存
- 实时事件广播（Pub/Sub）

现在重新划分（[config.yaml](file:///d:/Program%20Files/GoTaskAI/config/config.yaml#L21-L27) 注释即为动机）：

| 组件 | 职责 |
|---|---|
| **Redis** | 仅保留 **Asynq 队列 + 缓存（任务/Agent）**，不再承担事件广播 |
| **NATS** | 独立承担 **跨进程实时事件总线**（任务状态 + 工具事件） |
| **MySQL** | 权威持久化 |

好处是：

- **故障隔离**：单一 Redis 出问题（如切主抖动、OOM）不会波及实时推送链路；反之 NATS 抖动也不会影响队列和缓存。
- **避免相互挤占**：高频的进度/工具事件不再和队列、缓存哄抢同一个 Redis 实例的带宽与连接。
- **语义更贴切**：队列本质是「任务调度」，事件本质是「实时广播」，二者本来就不是一类事务。

### 3.6 配置说明

`eventbus` 配置块（[config.go](file:///d:/Program%20Files/GoTaskAI/internal/config/config.go#L26-L31)）：

```yaml
eventbus:
  enabled: true      # 是否启用实时事件通道
  url: "nats://0.0.0.0:4222"  # NATS 地址（容器内为 nats://api:4222）
  embedded: true     # 是否在本进程内嵌启动 nats-server（仅 API 设为 true）
```

- **API**（[main.go](file:///d:/Program%20Files/GoTaskAI/cmd/api/main.go#L52-L70)）：`enabled && url 非空` 时，若 `embedded=true` 先 `StartEmbeddedServer` 内嵌启动 server，再 `events.New(url)` 建客户端连接做订阅。任何一步失败都会**降级为进程内广播**（`eventBus = nil`）。
- **Worker**（[main.go](file:///d:/Program%20Files/GoTaskAI/cmd/worker/main.go#L105-L118)）：只作为客户端 `events.New(url)` 连接，`embedded` 不生效（Worker 不内嵌 server），主要做「发布」。

降级逻辑很关键：`NewTaskManager` 接收的 `eventBus` 允许为 `nil`。为 `nil` 时 `UpdateTask` 只做进程内 `broadcast`，也就是**单机部署时事件照常工作，只是跨进程不生效**（[manager.go](file:///d:/Program%20Files/GoTaskAI/internal/queue/manager.go#L35-L36)）。

---

## 四、为什么选 NATS（技术选型）

这一步是面试最容易被追问的地方。我们把候选方案摆在一起，说明「为什么是 NATS」。

### 4.1 候选方案对比

| 方案 | 亮点 | 在本项目的短板 |
|---|---|---|
| **Redis Pub/Sub（原方案）** | 复用已有 Redis 基础设施，不用新增组件 | 与队列/缓存共用同一 Redis，故障相互波及；Pub/Sub 是「发完即走」，无优雅的断线重连投递语义；Redis 既要扛队列又要扛广播，职责过载 |
| **Kafka** | 高吞吐、可持久化、支持回放与多消费组 | 过重：需要额外部署 ZK/KRaft 集群与 broker，对「实时进度广播」场景是大材小用 |
| **RabbitMQ** | 功能全、路由灵活、ACK/重投 | 偏重：引入独立 broker 与 Erlang 运行时，运维成本高；实时广播用不上这么多可靠性语义 |
| **WebSocket 直连广播** | 全双工、浏览器直接连接 | Worker 要持久维护与前端连接，跨队列/跨进程耦合重，和「API 只做轻量网关」的设计冲突 |
| **数据库轮询** | 实现最简单 | 前端高频轮询数据库，压力大、延迟高，完全背离「实时」初衷 |
| **NATS（选用）** | 轻量、低延迟、Go 原生、可内嵌、天然 Pub/Sub | 仅保留最简部署；at-most-once（非持久化） |

### 4.2 选择的核心理由（面试可背这 4 条）

1. **「解耦+高可用」优先**：把实时事件从 Redis 里拆出来，避免单一 Redis（队列+缓存+事件）成为全链路单点。这是本次改造的**根本动机**。
2. **极轻、可内嵌**：`nats-server` 本身就是 Go 写的单二进制，可直接嵌入 API 进程（`StartEmbeddedServer`），**无需额外部署容器**，运维成本几乎为零——这在 docker-compose 里体现得很明显（没有 nats 服务，靠 API 内嵌）。
3. **与现有技术栈零摩擦**：项目后端全是 Go，`nats.go` 是官方 Go 客户端，`nats-server` 也用 Go，生态一致、内存占用小、调用直观（`Publish`/`Subscribe`）。
4. **天然契合实时 SSE 的「广播」模型**：SSE 本质是「后端 → 多前端」的扇出（fan-out），NATS 的 Pub/Sub 正是这种一对多广播的惯用法；Subject 的命名/订阅非常对应「按主题分发的实时事件」。

**一句话总结选型**：我要的是「把实时事件从 Redis 解耦出来的一条轻量、低延迟、Go 原生、能内嵌的广播通道」，NATS 在这个约束下是性价比和复杂度最合适的选择；Kafka/RabbitMQ 太重，而 Redis Pub/Sub 正是我们要摆脱的旧方案。

### 4.3 为什么不用 Redis Pub/Sub 坚持换掉它

面试官常追问「Redis 本来就有 Pub/Sub，你何必再引 NATS？」可以这样答：

- **职责过载**：Redis 在本项目真正关键的是「Asynq 队列」和「缓存」，这两条数据链路太重要。实时事件是高频、大量、短小的广播，混在一起会让 Redis 同时扛「调度、缓存、广播」三种负载，任何一方异常都可能互相放大。
- **可靠性语义**：虽然两者都是「发完即走」，但 NATS 的客户端（本项目配置 `MaxReconnects(-1)`、`RetryOnFailedConnect(false)`）在断线重连、连接管理上的支持更成熟、语义更清晰，适合「API 与 Worker 长期保持连接做实时通信」。
- **明确的边界**：让「队列/缓存归 Redis，事件归 NATS」各司其职，比让一个组件全能更利于排查问题、扩容和演进（未来 Redis 可上 Cluster，NATS 可独立集群，互不牵扯）。

---

## 五、使用 NATS 的边界与取舍（体现思考深度）

1. **at-most-once（最多一次）投递**：Core NATS 消息存在内存、不落盘，API 万一短暂断连，期间的实时事件会丢。
   - **为什么可以接受**：前端刷新页面时会拉一次全量任务列表（`refreshTasks`），之后的实时事件只是增量；任务状态**最终一致**的权威源是 MySQL，丢了中间态不影响最终结果。
2. **「内嵌 server」带来的单点**：nats-server 只有一个，且住在 API 进程里。若 API 重启，内嵌 server 会被 `defer ns.Shutdown()` 关掉，Worker 端 `nats.go` 会自动重连（`MaxReconnects(-1)`）。
   - **扩展方向**：若未来 API 多副本横向扩容，单个内嵌 server 不够用，可改为**独立部署 NATS 集群（`embedded: false` + 指向集群地址）**，这正是配置里保留 `embedded` 开关的原因。
3. **降级路径**：`enabled=false` 或连接失败时 `eventBus` 为 `nil`，`UpdateTask` 退化为进程内 `broadcast`（单机语义）。所以即使 NATS 不可用，系统依然能跑，只是跨进程实时推送失效。
4. **为什么不做持久化**：实时进度/工具过程属于「短暂的中间状态」，没必要承担 JetStream 的持久化与回放成本；真要可靠，任务本身已在 MySQL/Asynq 里。

---

## 六、关联文件与阅读指引

如果想在代码里验证上面每一句话，按这个顺序看：

1. [internal/events/events.go](file:///d:/Program%20Files/GoTaskAI/internal/events/events.go) —— 事件通道封装：`EventBus`、`PublishJSON`、`Subscribe`、`StartEmbeddedServer`、两个主题常量。
2. [internal/queue/manager.go](file:///d:/Program%20Files/GoTaskAI/internal/queue/manager.go#L54-L79) —— API 侧 `listenForGlobalUpdates`（订阅 NATS）。
3. [internal/queue/manager.go](file:///d:/Program%20Files/GoTaskAI/internal/queue/manager.go#L350-L370) —— 发布侧 `UpdateTask`。
4. [cmd/api/main.go](file:///d:/Program%20Files/GoTaskAI/cmd/api/main.go#L52-L70) —— API 内嵌启动 nats-server + 建立客户端。
5. [cmd/worker/main.go](file:///d:/Program%20Files/GoTaskAI/cmd/worker/main.go#L105-L118) —— Worker 作为客户端连接。
6. [internal/config/config.go](file:///d:/Program%20Files/GoTaskAI/internal/config/config.go#L26-L31) 与 [config/config.yaml](file:///d:/Program%20Files/GoTaskAI/config/config.yaml#L21-L27) —— `eventbus` 配置。

> 依赖版本：`github.com/nats-io/nats.go v1.49.0`（客户端）、`github.com/nats-io/nats-server/v2 v2.12.5`（内嵌 server）。
