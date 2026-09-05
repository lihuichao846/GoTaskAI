# GoTaskAI 代码阅读总路线

这份文档用于保存当前已经达成一致的代码阅读顺序和阅读目标，后续继续深入时，直接从这里往下接即可，不必每次重新整理上下文。

## 阅读目标

目标不是一上来把所有文件都读完，而是先建立运行态脑图，再沿着一条真实请求链路把项目串起来：

1. 先知道运行时到底有哪些进程，它们分别负责什么。
2. 再看 API Server 如何启动、如何挂路由、如何把请求分发到不同 Handler。
3. 然后看认证和鉴权是如何落地的。
4. 再看任务如何从 HTTP 请求变成 Redis 队列任务。
5. 再看 Worker 如何消费任务、调用 LLM、处理失败与重试。
6. 再看状态更新如何通过 Redis Pub/Sub + SSE 实时推给前端。
7. 再看 RAG 和 KAG 是怎样插入到 Worker 主链路里的。
8. 最后再看前端，因为前端本质上主要是在展示后端主链路的状态和结果。

## 先建立运行态脑图

从业务运行态看，这个项目最核心的是下面几类进程：

1. `API Server`
   - 入口：`cmd/api/main.go`
   - 职责：接 HTTP 请求、挂路由、托管静态页面、处理鉴权、提交任务、查询状态、建立 SSE 推送。
2. `Worker Node`
   - 入口：`cmd/worker/main.go`
   - 职责：监听 Asynq 队列、执行 AI 任务、调用 RAG/KAG、更新任务状态、提供 gRPC 取消任务能力。
3. `MCP Server`（可选）
   - 由 Worker 连接，用于外部工具调用，例如联网搜索。
4. 基础设施
   - `MySQL`：持久化用户和任务数据。
   - `Redis`：同时承担缓存、Asynq 队列、Pub/Sub 事件总线。
   - `Neo4j`：承载 KAG 图谱数据。
   - `Ollama` / OpenAI 兼容模型服务：承载向量化和大模型推理。

可以把主链路先理解成下面这条线：

```text
前端 -> API Server -> MySQL/Redis/Asynq -> Worker Node -> LLM/RAG/KAG
                                      -> Redis Pub/Sub -> API SSE -> 前端
```

## 总阅读顺序

### 第 1 步：先建立运行态脑图，弄清楚到底有几个进程、谁负责什么

建议先看：

- `README.md`
- `cmd/api/main.go`
- `cmd/worker/main.go`
- `internal/config/config.go`

这一轮不要抠实现细节，只回答几个问题：

- API 和 Worker 是不是两个独立进程？
- 配置文件从哪里加载？
- 前端是 API 托管还是独立部署？
- Redis 在项目里到底承担了几种角色？

### 第 2 步：看 API Server 怎么启动，路由是怎么挂上的

建议重点看：

- `cmd/api/main.go`
- `internal/api/handler.go`
- `internal/api/auth.go`
- `internal/api/rag.go`
- `internal/middleware/auth.go`
- `internal/middleware/ratelimit.go`
- `internal/middleware/cors.go`

阅读重点：

1. `config.InitConfig()` 先加载配置。
2. `db.InitMySQL()`、`db.InitRedis()` 初始化基础依赖。
3. `queue.NewTaskManager()` 创建任务管理器。
4. `api.NewHandler()`、`api.NewAuthHandler()`、`api.NewRAGHandler()` 组装 HTTP 控制器。
5. `gin.Default()` 创建路由引擎。
6. `r.StaticFile("/", ...)` 托管首页。
7. `apiGroup / authGroup / tasksGroup / adminGroup` 分组挂路由。

这一轮最关键的认识是：

- `cmd/api/main.go` 只负责装配依赖和挂路由。
- 真正处理请求逻辑的是 `internal/api/*` 下的 Handler。

### 第 3 步：看登录注册和 JWT 鉴权到底怎么落地

建议重点看：

- `internal/api/auth.go`
- `internal/middleware/auth.go`
- `internal/pkg/jwt/jwt.go`
- `internal/model/user.go`

阅读重点：

1. 注册时如何校验参数、如何用 `bcrypt` 存密码。
2. 登录时如何查数据库、比对密码、签发 JWT。
3. 鉴权中间件如何从请求里取 Token。
4. 中间件如何把 `userID` 放进 Gin 上下文。
5. 业务 Handler 如何通过 `c.GetUint("userID")` 获取当前用户身份。

这一步看完后，应该能回答：

- “登录成功以后，后端到底怎么知道这是哪个用户？”
- “为什么 `tasksGroup.Use(middleware.AuthMiddleware())` 就能保护整组接口？”

### 第 4 步：看一个任务是怎么从 HTTP 请求变成 Redis 队列任务的

建议重点看：

- `internal/api/handler.go`
- `internal/queue/manager.go`
- `internal/model/task.go`

建议只先跟一条链路：`POST /api/tasks/submit`

阅读顺序：

1. `handler.SubmitTask()`
2. `manager.AddTask()`
3. `manager.cacheTask()`
4. `manager.Enqueue()`
5. `model.Task`

这条链路中发生的事情：

1. API 从 HTTP 请求中解析 JSON。
2. 从上下文里拿 `userID`。
3. 构造一个 `Task` 实体。
4. 先写入 MySQL。
5. 再写 Redis 缓存。
6. 最后用 Asynq 推进 Redis 队列。

这一步看完后，应该能回答：

- 为什么提交任务返回的是 `202 Accepted` 而不是直接返回模型结果？
- 为什么任务需要同时进入 MySQL、Redis、Asynq 三处？

### 第 5 步：看 Worker Node 怎么消费任务、怎么调 LLM、怎么失败重试

建议重点看：

- `cmd/worker/main.go`
- `internal/worker/pool.go`
- `internal/pkg/llm/client.go`
- `internal/queue/manager.go`

阅读顺序：

1. `worker.NewPool()`
2. `pool.Start()`
3. `pool.handleTaskProcess()`
4. `llm.Client.Generate()`
5. `manager.UpdateTask()`

阅读重点：

1. Worker 启动后如何创建 Asynq Server。
2. `mux.HandleFunc("task:process", ...)` 是怎么把队列任务绑定到处理函数上的。
3. 任务开始处理时为什么先更新为 `processing`。
4. 大模型调用失败后为什么返回错误给 Asynq，Asynq 又如何自动重试。
5. 什么时候任务会进入 `failed`，什么时候只是回到 `pending`。

这一步看完后，应该能真正理解项目最核心的“异步执行”机制。

### 第 6 步：看状态更新为什么能实时推给前端，SSE + Redis Pub/Sub 是怎么串起来的

建议重点看：

- `internal/api/handler.go` 中的 `StreamTasks()`
- `internal/queue/manager.go` 中的 `Subscribe()` / `broadcast()` / `UpdateTask()` / `listenForGlobalUpdates()`

这一段主链路建议这样理解：

1. 前端调用 `/api/tasks/stream?token=...` 建立 SSE 长连接。
2. API 进程里通过 `manager.Subscribe(userID)` 给当前用户挂一个本地 channel。
3. Worker 完成任务后调用 `manager.UpdateTask()`。
4. `UpdateTask()` 不只更新 MySQL 和 Redis，还会往 Redis Pub/Sub 的 `global_task_updates` 频道发消息。
5. API 进程里的 `listenForGlobalUpdates()` 一直订阅这个频道。
6. API 收到消息后调用 `broadcast()`，把更新扔到当前用户对应的 channel。
7. `StreamTasks()` 从 channel 取到任务更新，再用 `c.SSEvent()` 推给前端。

这一步是整个微服务解耦的关键。

一句话概括就是：

`Worker 不直接连前端，而是先发 Redis Pub/Sub；API 再把消息转成 SSE 推给浏览器。`

### 第 7 步：看 RAG 和 KAG 是在 Worker 里怎么插进去的

建议重点看：

- `internal/worker/pool.go`
- `internal/api/rag.go`
- `internal/pkg/kag/kag.go`
- `internal/pkg/llm/client.go`

阅读重点：

1. Worker 在处理任务时，会先尝试从 RedisVector 做 RAG 检索。
2. 如果启用了 Neo4j，还会通过 `kagManager.RetrieveGraphContext()` 做 KAG 检索。
3. RAG 和 KAG 的结果都不是直接返回给前端，而是被拼接进 `systemPrompt`。
4. 然后统一交给 `llmClient.Generate()` 去生成最终结果。

建议区分两条子链：

1. 知识入库链
   - `internal/api/rag.go`
   - 包括文本切块、Embedding、向量入库、KAG 三元组抽取和 Neo4j 入库。
2. 在线检索链
   - `internal/worker/pool.go`
   - 包括 RAG 相似度检索、KAG 图查询、上下文注入、大模型生成。

### 第 8 步：最后再看前端

建议重点看：

- `public/index.html`
- `web/src/*`（如果你想看独立前端改造）

这一步放最后的原因是：

1. 前端主要是在调后端接口、显示任务状态、接收 SSE 推送。
2. 如果后端主链路已经清楚，前端基本就是“把状态展示出来”。
3. 如果先看前端，很容易被页面逻辑和交互细节分散注意力。

## 推荐的最小阅读主线

如果只想先看懂 80% 的核心逻辑，可以优先读这几个文件：

1. `cmd/api/main.go`
2. `internal/api/handler.go`
3. `internal/queue/manager.go`
4. `cmd/worker/main.go`
5. `internal/worker/pool.go`
6. `internal/model/task.go`

只把这 6 个文件串起来，就已经能看懂：

- 请求怎么进来
- 任务怎么入队
- Worker 怎么处理
- 状态怎么更新
- 为什么前端能实时收到结果

## 阅读方法建议

为了避免被 import 和跨文件调用绕晕，建议采用下面的方法：

1. 一次只追一条主链路，不要同时追登录、任务、SSE、RAG、KAG 多条线。
2. 优先看 `main`、`NewXxx`、`HandleXxx`、`SubmitXxx`、`UpdateXxx`、`ProcessXxx` 这些“动词函数”。
3. 遇到 import 很多的文件，不是每个依赖都展开，只看当前函数真正调用到的那个符号。
4. `internal/pkg/*` 第一轮可以先当黑盒，只理解它“提供什么能力”。
5. `api/proto/*.pb.go`、`third_party/`、`test/`、`tools/` 第一轮可以先跳过。

## 当前阶段的结论

到目前为止，这个项目最值得先建立的核心认识有 4 个：

1. 这是一个双进程后端项目：`API Server + Worker Node`。
2. API 负责接客，Worker 负责干重活。
3. Redis 在这个项目里同时扮演缓存、消息队列和事件总线三个角色。
4. SSE 能实时更新，不是因为 Worker 直接推前端，而是因为 `Worker -> Redis Pub/Sub -> API -> SSE -> 浏览器` 这条链路成立。

## 后续继续深入时的建议起点

下次继续深入时，建议直接从下面这条调用链开始展开：

```text
cmd/api/main.go
  -> internal/api/handler.go: SubmitTask
  -> internal/queue/manager.go: AddTask / Enqueue
  -> cmd/worker/main.go
  -> internal/worker/pool.go: handleTaskProcess
  -> internal/queue/manager.go: UpdateTask
  -> internal/api/handler.go: StreamTasks
```

如果后面要继续精读，可以按下面的专题往下拆：

1. JWT 鉴权专题
2. Asynq 队列与重试专题
3. SSE 推送专题
4. RAG / KAG 增强专题
5. 前端如何消费任务状态专题
