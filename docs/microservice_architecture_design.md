# GoTaskAI 微服务架构设计说明

## 1. 架构演进背景
在传统的单体架构中，处理耗时较长的 AI 大模型推理或 GraphRAG 图谱构建任务时，极易阻塞 HTTP 线程，导致系统吞吐量断崖式下降。为了实现真正的高并发与资源隔离，GoTaskAI 采用了**读写分离与计算解耦**的微服务架构。

## 2. 核心微服务拆分
系统在物理层面拆分为两个独立运行的微服务进程：

### 2.1 API Server (网关与接入微服务)
- **入口代码**: `cmd/api/main.go`
- **核心职责**:
  - **接入与路由**: 接收前端 HTTP 请求，处理跨域与静态资源代理。
  - **安全与限流**: 执行 JWT 身份鉴权，基于 Redis 实现滑动窗口 Rate Limiting 防止恶意刷单。
  - **任务分发**: 将耗时的 AI 任务参数打包，生成全局唯一 Task ID，并推入 Asynq 消息队列，随后立即向前端返回 `202 Accepted` 及 Task ID。
  - **实时推送**: 维持与前端的单向长连接（SSE, Server-Sent Events），将后端计算的实时状态推流给客户端。

### 2.2 Worker Node (异步计算微服务)
- **入口代码**: `cmd/worker/main.go`
- **核心职责**:
  - **任务消费**: 监听 Redis 中的 Asynq 队列，按照高/中/低优先级抢占执行任务。
  - **AI 核心逻辑**: 执行大模型 API 调用、基于 Neo4j 的 KAG 图谱检索以及 Redis 向量检索。
  - **工具调用**: 集成 MCP (Model Context Protocol) 客户端，动态调用外部工具（如联网搜索插件）。
  - **状态反馈**: 任务执行期间及完成后，将状态更新写入数据库，并通过 Redis 事件总线广播。

## 3. 微服务通信与协同机制

两个物理隔离的进程之间，通过以下三种主要方式进行协同：

### 3.1 异步任务下发 (Redis + Asynq)
- **方向**: API Server -> Worker Node
- **机制**: API Server 作为 Producer，将任务持久化到 Redis 队列。Worker 作为 Consumer 异步安全地拉取处理。这实现了完美的**流量削峰**。

### 3.2 跨进程事件总线 (Redis Pub/Sub + SSE)
- **方向**: Worker Node -> API Server -> Client
- **机制**: 
  1. Worker 节点在执行任务（如生成 AI 文本流）时，向 Redis 的特定 Channel 发布消息（Pub）。
  2. API Server 一直在订阅该 Channel（Sub），以此跨进程实时获取 Worker 的工作进度。
  3. API Server 将获取到的数据，通过 SSE 流推送给对应的浏览器客户端，实现跨进程的实时无缝通信。

### 3.3 同步控制通道 (gRPC)
- **方向**: API Server -> Worker Node
- **机制**: Worker 节点内部启动了一个 gRPC Server (默认监听 50051 端口)。当用户主动请求“强制取消/熔断”某个任务时，异步队列无法立刻打断正在执行的逻辑，因此 API Server 作为 gRPC 客户端，发送强同步的 RPC 调用给 Worker，Worker 收到指令后立刻中止对应协程的执行。协议定义位于 `api/proto/worker/worker.proto`。

## 4. 架构优势总结
1. **极致解耦**: 接入层（I/O密集型）与计算层（CPU/网络延时密集型）分离。
2. **弹性扩容**: 当 AI 任务积压时，可直接在 K8s 或物理机上横向增加 Worker 节点数量，而无需重启或影响 API Server。
3. **高可用性**: Asynq 队列保障了任务不会因 Worker 崩溃而丢失，支持失败自动指数退避重试与死信队列机制。
