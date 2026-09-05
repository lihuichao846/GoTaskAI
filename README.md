# GoTaskAI：高并发微服务 AI 任务调度与 GraphRAG 对话平台

> **💡 一句话介绍本项目：**
> 这是一个基于 `Go + Gin + Vue3` 构建的企业级微服务架构异步任务调度与大模型对话平台。它利用 `Redis + Asynq` 实现了支持多优先级与故障重试的高并发消息队列，通过 `SSE` 技术实现跨进程前端状态实时流式推送；深度集成了 MCP 协议与 `LangChainGo` 框架，并引入 Neo4j 构建了前沿的 **GraphRAG (混合检索增强生成)** 引擎，完美解决了大模型调用耗时长、并发低和复杂知识推理盲区的痛点。

**GoTaskAI** 也是一个专为 Go 语言初学者和进阶者设计的**学习型高并发架构项目**。
它模拟了真实企业中“AI 耗时任务处理”（如大模型生成、知识图谱构建）的场景，带领你一步步从原生的 `net/http` 走到工业级的微服务 `Gin + GORM + Redis + Neo4j` 架构。

通过这个项目，你不仅能学到 Go 语言的语法，更能深刻理解**微服务解耦、高并发处理、队列调度、GraphRAG 双路召回、跨进程 SSE 推送**等核心后端工程思想。

---

## 🌟 核心特性与学习目标

### 1. 微服务与高可用架构 (企业级演进)
*   **服务拆分解耦**：彻底摒弃单体架构，将负责 HTTP 接客的 `API Server` 和负责计算重活的 `Worker Node` 物理拆分为两个独立微服务，支持针对计算节点进行 K8s 级水平扩容。
*   **跨进程事件驱动**：基于 Redis Pub/Sub 实现了跨进程状态同步，Worker 节点干完活后通过消息总线通知 API Server，再由 API Server 将状态通过 SSE 实时推给前端。

### 2. GraphRAG 混合检索架构 (亮点)
*   **KAG 图谱增强**：引入 **Neo4j** 图数据库，利用大模型自动提取文本实体关系三元组并入库。检索时通过 Text2Cypher 意图识别，实现精准的复杂关系逻辑推理。
*   **双路召回增强生成**：将传统的 RedisVector 向量模糊相似度检索 (RAG) 与 Neo4j 的确切图谱关系 (KAG) 相结合，双管齐下拼装 System Prompt，大幅降低模型幻觉。

### 3. 并发与队列调度 (核心精髓)
*   **Asynq 消息队列**：引入业界标准的基于 Redis 的 Asynq 队列，实现长耗时任务的绝对可靠执行与 API 流量削峰。
*   **多优先级队列**：实现了 高/中/低 三个级别的优先级调度，确保高优任务的绝对抢占权。
*   **失败与重试机制**：内置指数退避重试算法，支持手动触发失败任务“复活”并重新入队。

### 4. 工业级 Web 架构实践
*   **Gin 框架重构**：体验路由分组、参数绑定 (`ShouldBindJSON`) 以及 RESTful API 设计。
*   **Redis 限流中间件 (Rate Limiting)**：利用 Redis 和滑动窗口算法，实现了“用户维度”的防刷单限流机制，保护昂贵的 Token 资源。
*   **安全认证系统 (JWT + bcrypt)**：完整的用户注册/登录闭环。密码单向哈希加密入库，采用无状态的 JWT Token 进行接口鉴权。
*   **Cache-Aside 旁路缓存**：读操作先查 Redis，写操作落库后更新 Redis，实现 MySQL 与 Redis 双写，显著降低核心业务查询 QPS。

### 5. 动态工具调用与上下文管理
*   **MCP 协议集成**：率先在服务端集成 MCP (Model Context Protocol) 协议，赋予 AI 动态调用搜索引擎等外部工具的 Agent 能力，实现联网核查。
*   **多轮对话历史回溯**：设计基于 Session 的历史滑动窗口机制，让无状态的 LLM 拥有连续对话记忆。

---

## 🏗️ 架构图解

### 1. 核心数据流转架构

```text
[用户浏览器 (Vue3前端)] 
       │ 
       │ (1. HTTP: 提交任务 / GraphRAG问答) -> JWT 鉴权 -> RateLimit 限流
       ▼
[API Server (微服务网关层 - cmd/api)]
       │ 
       │ (2. 生成 UUID，持久化 MySQL，推入 Redis 队列)
       ▼
[Redis (Asynq 队列 & Pub/Sub 事件总线)] ──(3. Worker 独立进程监听并抢占执行)──▶ [Worker Node (计算微服务 - cmd/worker)]
       │                                                                               │
       │ (6. API Server 监听 Redis Pub/Sub，将完成状态通过 SSE 广播推给前端)           │ (4. KAG 图谱检索 / RAG 向量检索)
       ▼                                                                               ▼
[SSE Stream (长连接推送)] ◀─────────────────────────────────────────────────── [Neo4j & RedisVector]
                                                                                       │
                                                                                       │ (5. 注入 GraphRAG 上下文，调用大模型生成答案)
                                                                                       ▼
                                                                             [Ollama / Qwen2.5 / MCP 联网搜索]
```

### 2. 先明确本项目的实际形态

为了避免第一次阅读时被目录结构绕晕，先明确 3 个关键事实：

1. **前端不是独立工程**：前端页面主要就是 `public/index.html`，由 API Server 直接托管；因此本项目不是典型的“前端单独部署、后端单独部署”的完全前后端分离架构。
2. **真正拆分的是后端**：后端被拆成了 `API Server` 和 `Worker Node` 两个独立进程。前者负责接客，后者负责耗时 AI 计算。
3. **核心源码主要集中在 `internal/`**：如果你只想快速看懂主链路，优先阅读 `cmd/`、`internal/`、`public/`、`config/` 这几部分即可，其他目录多为工具、文档或测试辅助代码。

### 3. 项目目录结构图

```text
GoTaskAI/
├── bin/                    # 编译后的可执行文件 (api.exe, worker.exe, MCP 插件等)
├── cmd/                    # 微服务启动入口 (解耦改造)
│   ├── api/                # API Server 微服务入口 (main.go)
│   └── worker/             # Worker Node 计算节点入口 (main.go)
├── config/                 # 项目配置文件 (config.yaml)
├── data/                   # 本地测试数据和 RAG 知识库原始文档 (.md)
├── docs/                   # 📚 核心原理与架构文档集 (强烈建议阅读！)
├── internal/               # 核心业务代码 (私有包，不对外暴露)
│   ├── api/                # API 接入层 (Gin Controllers，如 handler.go, auth.go, rag.go)
│   ├── config/             # 配置解析逻辑
│   ├── db/                 # 数据库初始化 (MySQL/Redis/Neo4j 链接)
│   ├── middleware/         # Gin 中间件 (JWT 鉴权, Redis 限流)
│   ├── model/              # 数据模型层 (GORM 实体定义: User, Task)
│   ├── pkg/                # 通用工具包 (JWT, LLM 客户端, KAG 管理器, MCP 客户端)
│   ├── queue/              # 任务调度层 (TaskManager, 封装 Asynq 和跨进程 Pub/Sub SSE)
│   └── worker/             # 任务执行层 (Pool, 消费队列并调用大模型/GraphRAG)
├── public/                 # 前端静态资源 (Vue3 + Tailwind 编写的 index.html)
├── test/                   # 针对各个模块的独立测试代码 (LLM, MCP等)
├── tools/                  # 独立工具脚本 (如 MCP 插件源码, 密码重置脚本)
├── docker-compose.yml      # 基础设施容器编排 (MySQL, Redis Stack, Neo4j, Nginx)
├── go.mod                  # Go 依赖管理文件
├── run_microservices.bat   # 🚀 一键在本地双开 API 与 Worker 微服务的启动脚本
└── nginx.conf              # Nginx 反向代理与动静分离配置
```

### 4. 建议的阅读顺序

如果你是第一次接触这个项目，推荐按下面的顺序理解，而不是一上来把所有目录都看一遍：

1. `cmd/api/main.go`
   先看 API Server 是怎么启动的，它负责路由、鉴权、限流、静态页面托管和任务接入。
2. `cmd/worker/main.go`
   再看 Worker Node 是怎么启动的，它负责队列消费、gRPC 控制和大模型执行。
3. `internal/api/handler.go`
   理解用户提交任务、查询任务、取消任务、建立 SSE 长连接的完整 HTTP 入口。
4. `internal/queue/manager.go`
   理解任务是如何写入 MySQL、缓存到 Redis、推入 Asynq，并通过 Pub/Sub 广播更新的。
5. `internal/worker/pool.go`
   理解 Worker 真正如何执行任务、调用 RAG/KAG、再调用大模型。
6. `internal/model/task.go`
   理解任务的状态流转：`pending -> processing -> completed / failed / cancelled`。
7. `public/index.html`
   最后再看前端页面如何调用后端接口、如何接收 SSE 推送。

### 5. 运行时有哪些进程

从“业务代码运行态”来看，核心通常是下面 2 到 3 个进程：

1. `API Server`
   监听 HTTP 请求，负责前端页面托管、JWT 鉴权、任务提交、状态查询和 SSE 推送。
2. `Worker Node`
   监听 Redis 队列并异步执行 AI 任务；当前启动入口已支持按配置自动拉起多个 Worker 处理进程，以便直接水平提升消费并发。
3. `MCP Server`（可选子进程）
   由 Worker 按需拉起，用于外部工具调用，例如联网搜索。

如果把基础设施也算上，运行时还会有 `MySQL`、`Redis`、`Neo4j`、`Nginx` 和 `Ollama` 这些独立服务。

---

## 📚 学习文档指南 (新手必看)

项目 `docs` 目录下包含了专门为你编写的通俗易懂的原理解析文档，建议结合代码一起阅读：
*   [Web 框架进化史：net/http vs Gin](./docs/web_framework_guide.md)
*   [Gin 框架 4 大核心方法速查表](./docs/gin_survival_guide.md)
*   [GORM 与 Redis 极简入门指南](./docs/db_and_redis_guide.md)
*   [文件柜与办公桌：为什么我们需要 Redis？](./docs/redis_explanation_guide.md)
*   [告别轮询：SSE 实时推送原理解析](./docs/sse_vs_polling.md)
*   [数据库解惑：Navicat、代码与 Docker 卷](./docs/navicat_vs_code_db.md)
*   [计算机网络底层：I/O 多路复用与 epoll 原理](./docs/io_multiplexing_epoll.md)
*   [图解网络安全：HTTP vs HTTPS 与握手过程](./docs/http_vs_https_guide.md)
*   [Go 面试必考：GMP 调度模型与 Channel 原理](./docs/go_gmp_and_channel.md)
*   [高并发终极方案：Nginx 与 Go 的企业级架构实战](./docs/nginx_architecture_guide.md)

---

## 🚀 快速启动

### 1. 环境准备
你需要安装好以下环境：
*   Go 1.20+
*   Docker & Docker Compose (用于启动 MySQL 和 Redis)

### 2. 启动基础设施 (数据库、缓存与图数据库)
进入项目根目录，通过 Docker 启动所需环境（注意：为了防止 Windows 本地端口冲突，MySQL 被映射到了 `3307` 端口）：
```bash
docker compose up -d
```

### 3. 后端启动方式
现在推荐区分两种启动模式：

1. **开发模式（推荐日常开发使用）**
   - `run_backend_dev.bat`：启动 `API + Worker`
   - `run_frontend_dev.bat`：只启动独立前端 `web`
   - `run_fullstack_dev.bat`：一键启动 `API + Worker + web`

2. **二进制模式（适合演示或已编译完成后使用）**
   - `run_microservices.bat`：优先启动 `bin/api.exe` 与 `bin/worker.exe`
   - 如果本地还没有编译好的二进制文件，脚本会自动回退到 `go run ./cmd/api` 和 `go run ./cmd/worker`

例如，后端开发模式直接双击：
```bash
run_backend_dev.bat
```

二进制/兼容模式继续使用：
```bash
run_microservices.bat
```

也可以手动从项目根目录执行：
```bash
go run ./cmd/api
go run ./cmd/worker
```

现在 `API` 和 `Worker` 已支持自动定位项目根目录下的 `config/config.yaml`、`public/`、`bin/` 资源，因此不再要求你必须卡死在某个固定目录启动。

`Worker` 进程的总消费并发现在由两层配置共同决定：

```yaml
queue:
  workers: 5     # 单个 Worker 进程内的并发协程数
  processes: 2   # Worker 启动时自动拉起的处理进程数
```

总理论消费并发约等于 `workers * processes`。例如上面的配置会启动 `2` 个 Worker 处理进程，每个进程内部再以 `5` 个并发消费者处理队列任务，总体可同时处理约 `10` 个任务。

### 4. 前后端分离改造的当前进展

为了便于后续演进到独立前端工程，项目现在同时保留了两种前端入口：

1. **兼容模式（原有方式）**
   API Server 仍然会继续托管 `public/index.html`，保证项目可以按原方式直接运行。
2. **独立前端入口（第一阶段改造产物）**
   新增了 `web/index.html`，它默认通过 `http://localhost:8080` 调用后端 API，可作为后续拆分为独立前端工程的起点。

API Server 已补充 CORS 支持，因此独立前端页面可以跨域访问当前的 Go 后端接口。

### 5. 独立前端工程（第二阶段改造产物）

`web/` 目录现在已初始化为一个独立的 `Vue + Vite` 前端工程骨架：

```bash
cd web
npm install
npm run dev
```

默认开发地址为 `http://localhost:5173`，默认后端地址通过 `.env` 中的 `VITE_API_BASE_URL` 指定（参考 `web/.env.example`）。
如果不想手动切目录，直接双击项目根目录下的：

```bash
run_frontend_dev.bat
```

旧版单文件页面已保留为 `web/public/legacy.html`，可作为迁移期间的过渡入口。

### 6. 访问页面
打开浏览器，访问：[http://localhost:8080](http://localhost:8080)
1. 注册一个账号并登录。
2. 体验提交任务，你可以随时关闭 Worker 节点的终端，感受 API Server 依然秒开、任务进入队列挂起排队的高可用容错能力。
3. 进入“系统管理”，粘贴任意带有实体关系的文章，点击“构建 KAG (Neo4j)”。
4. 点击“预览图谱”查看 Neo4j 中提取的知识关系网。
5. 在聊天框向大模型提问，感受 GraphRAG 混合双路召回带来的惊艳推理回答！

---
*本项目从零起步构建，保留了清晰的迭代脉络，是掌握 Go 语言后端工程化的绝佳练手场！*
