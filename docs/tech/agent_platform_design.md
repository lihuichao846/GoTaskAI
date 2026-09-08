# Agent 构建平台设计文档（GoTaskAI 演进方向）

> 本文件描述如何把 GoTaskAI 从「演示型 AI 任务平台」演进为「让用户自建 Agent 的基础设施平台」。重点是**现实工程因素**：数据模型、多租户隔离、安全、成本、可观测性、高可用，而不只是概念。当前代码为基线，改动尽量复用现有骨架。

---

## 1. 定位与目标

**一句话**：让用户能创建、配置、运行自己的 Agent，平台提供「工具调用、知识库构建、异步调度、流式推送」四类底层能力。

**关键边界（区别于单一 Agent 产品）**：

- 平台不定义「用户该用什么 Agent」，而是提供**构建 Agent 的能力**。
- Agent 是用户创建出来的资源，不是平台写死的功能。
- 平台的护城河是**后端工程能力**（调度、工具注册、知识库、多租户、成本控制），不是某个具体 Prompt。

---

## 2. 核心概念

| 概念 | 说明 | 对应现有代码 |
|---|---|---|
| **Agent** | 用户配置的一个智能体：system_prompt + 模型 + 工具 + 知识库 | 现无实体，由每次请求临时拼 |
| **Tool** | 可被 Agent 调用的工具（内置 / MCP 外部） | [mcpclient/client.go](file:///d:/Program%20Files/GoTaskAI/internal/pkg/mcpclient/client.go) 只支持一个 search_mcp |
| **KnowledgeBase** | 文档构建出的知识库（RAG 向量 + KAG 图谱） | [api/rag.go](file:///d:/Program%20Files/GoTaskAI/internal/api/rag.go) + [pkg/kag/kag.go](file:///d:/Program%20Files/GoTaskAI/internal/pkg/kag/kag.go) |
| **Task** | 某个 Agent 的一次运行实例 | [model/task.go](file:///d:/Program%20Files/GoTaskAI/internal/model/task.go) |
| **Session** | 一轮多轮对话 | 现有 `session_id` 字段 |
| **RunLog / ToolCall** | 观测与审计记录 | 现无 |

---

## 3. 总体架构

复用现有「API Server + Worker Node」双进程架构，新增「配置管理」和「观测」两条线。

```text
┌────────────── API Server ──────────────┐
│  Agent/Tool/KB 管理 API、鉴权、限流      │
│  运行入口（提交任务）、SSE 推送           │
└───────────────┬────────────────────────┘
                │ ① 任务入队（Asynq）
                ▼
        ┌──────── Redis ────────┐
        │ 缓存 + Asynq 队列       │
        └────────┬───────┬──────┘
                 │       │ （事件不再走 Redis）
       ② 消费任务 │       └───────────────┐
                 ▼                        ▼
┌────────────── Worker Node ──────────────┐        ┌────── NATS ──────┐
│  dispatch → 加载 Agent 配置              │  ③ 状态→  │  跨进程实时事件总线  │
│  → 解析工具（Tool Registry）             │────发布──▶│  任务/工具事件      │
│  → 挂载知识库（RAG/KAG）                 │           └────────┬───────┘
│  → LLM 工具调用循环 → 回写 + 观测        │  ④ SSE    └───────▶ API Server
└───────────────┬────────────────────────┘            （SSE 推给前端）
                │ ④ 日志/指标/追踪
                ▼
        ┌── 可观测性 ──┐
        │ MySQL + 日志  │
        │ + Prometheus │
        └──────────────┘

外部依赖：MySQL / Redis(向量+缓存+队列) / NATS(事件总线) / Neo4j / 对象存储 / Ollama / MCP 工具子进程
```

**关键变化**：Worker 的 `handleTaskProcess` 从「写死 LLM 逻辑」改为「按 Agent 配置动态组装」，但调度骨架（入队、消费、重试、状态回写、SSE）完全不变。

---

## 4. 数据模型设计

### 4.1 多租户隔离策略（现实工程考量）

- **选型**：`shared schema + tenant_id`（即所有表带 `user_id` 或 `tenant_id` 列做行级隔离）。
- **理由**：当前规模用共享 schema + 行级隔离最经济；独立 schema 或独立库是规模化后再考虑。
- **强制约束**：所有查询必须带 `user_id` 条件，代码层用 `AuthMiddleware` 注入的 `userID` 做过滤，避免越权。

### 4.2 新增实体（GORM 结构示意）

```go
// Agent：用户构建的智能体
type Agent struct {
    ID           string    `gorm:"primaryKey;type:varchar(36)"`
    UserID       uint      `gorm:"index"`
    Name         string    `gorm:"type:varchar(100)"`
    Description  string    `gorm:"type:text"`
    SystemPrompt string    `gorm:"type:text"`
    Model        string    `gorm:"type:varchar(100)"`   // 覆盖平台默认模型
    Status       string    `gorm:"type:varchar(20);index"` // draft/active/archived
    Version      int       `gorm:"default:1"`
    Config       string    `gorm:"type:text"` // JSON：temperature、max_tokens 等
    CreatedAt    time.Time
    UpdatedAt    time.Time
}

// Tool：可调用工具
type Tool struct {
    ID          string `gorm:"primaryKey;type:varchar(36)"`
    Name        string `gorm:"uniqueIndex"` // 全局唯一，内置/MCP 统一命名空间
    Type        string // builtin / mcp
    DisplayName string
    Description string
    Schema      string `gorm:"type:text"` // JSON Schema
    Config      string `gorm:"type:text"` // MCP 连接配置（加密存储）
    IsBuiltin   bool
    Status      string // active / disabled
}

// AgentTool：Agent 与工具的多对多（白名单）
type AgentTool struct {
    AgentID string `gorm:"primaryKey"`
    ToolID  string `gorm:"primaryKey"`
}

// KnowledgeBase：知识库
type KnowledgeBase struct {
    ID           string `gorm:"primaryKey;type:varchar(36)"`
    UserID       uint   `gorm:"index"`
    Name         string
    Type         string // rag / kag / hybrid
    VectorIndex  string // RedisVector 索引名（按租户隔离，如 idx:kb:{kbID}）
    Status       string // pending/building/ready/failed
    DocCount     int
    CreatedAt    time.Time
    UpdatedAt    time.Time
}

// Document：知识库中的文档
type Document struct {
    ID         string `gorm:"primaryKey;type:varchar(36)"`
    KBID       string `gorm:"index"`
    Title      string
    Source     string `gorm:"type:text"` // 原始内容或对象存储 key
    Status     string // uploaded/chunking/embedding/ready/failed
    ChunkCount int
    CreatedAt  time.Time
}

// AgentKB：Agent 与知识库的多对多
type AgentKB struct {
    AgentID string `gorm:"primaryKey"`
    KBID    string `gorm:"primaryKey"`
}
```

### 4.3 Task 表改造（最小改动）

在 [model/task.go](file:///d:/Program%20Files/GoTaskAI/internal/model/task.go) 的 `Task` 上新增：

```go
AgentID string `json:"agent_id" gorm:"index;type:varchar(36)"` // 关联的 Agent，可空（兼容旧逻辑）
```

### 4.4 观测与审计表

```go
// RunLog：一次 Agent 运行的完整记录
type RunLog struct {
    ID        string
    TaskID    string `gorm:"index"`
    AgentID   string `gorm:"index"`
    UserID    uint   `gorm:"index"`
    Status    string
    StartedAt time.Time
    EndedAt   time.Time
    LatencyMs int64
    TokensIn  int
    TokensOut int
    CostUSD   float64
}

// ToolCallLog：工具调用链
type ToolCallLog struct {
    ID        string
    RunID     string `gorm:"index"`
    ToolName  string
    Arguments string `gorm:"type:text"`
    Result    string `gorm:"type:text"`
    LatencyMs int64
    Status    string // success/failed
    CreatedAt time.Time
}
```

---

## 5. API 设计

统一前缀 `/api`，全部走 JWT 鉴权 + 按 `user_id` 过滤。

### 5.1 Agent 管理

```
POST   /api/agents                 创建 Agent
GET    /api/agents                 当前用户的 Agent 列表
GET    /api/agents/:id             详情
PUT    /api/agents/:id             更新（version +1）
DELETE /api/agents/:id             删除（软删，关联资源保留）
POST   /api/agents/:id/tools       绑定工具（body: tool_ids）
POST   /api/agents/:id/knowledge-bases  绑定知识库
```

### 5.2 工具管理

```
GET    /api/tools                  工具列表（内置 + 可用的 MCP 工具）
POST   /api/tools                  注册 MCP 工具（body: 连接配置）
DELETE /api/tools/:id              注销（仅 MCP，内置不可删）
GET    /api/tools/:id/schema       工具参数 Schema
```

### 5.3 知识库构建

```
POST   /api/knowledge-bases                创建知识库
GET    /api/knowledge-bases                列表
POST   /api/knowledge-bases/:id/documents  上传文档（异步触发构建）
GET    /api/knowledge-bases/:id/build-status  构建进度
DELETE /api/knowledge-bases/:id            删除
```

### 5.4 运行与观测

```
POST   /api/agents/:id/run        运行 Agent（等价于提交任务 + agent_id）
GET    /api/agents/:id/stream      SSE 流（复用现有 StreamTasks 思路）
GET    /api/runs                  运行历史
GET    /api/runs/:id              单次运行详情（含工具调用链）
```

> 说明：运行接口实际是现有 `POST /api/tasks/submit` 的扩展——`submit` 增加 `agent_id` 字段，`Task.Type` 增加 `agent` 类型，复用整条入队/消费/回推链路。

---

## 6. 核心执行流程（Worker 改造）

现有 [pool.go](file:///d:/Program%20Files/GoTaskAI/internal/worker/pool.go) 的 `handleTaskProcess` 是写死的。改造目标：**统一 dispatch + 按 Agent 配置组装**。

```text
handleTaskProcess(task)
  │
  ├─ 若 task.AgentID 为空 → 走旧逻辑（兼容）
  │
  ├─ 1. 加载 Agent 配置（带缓存，见 6.1）
  ├─ 2. 解析工具：查 agent_tools → Tool Registry 取 Handler（见 7）
  ├─ 3. 挂载知识库：查 agent_kbs → 加载对应 RAG store / KAG manager
  ├─ 4. 拼装 system_prompt：
  │       agent.SystemPrompt
  │       + 知识库检索上下文（RAG top-k + KAG 关系）
  │       + 工具使用规范
  ├─ 5. 拼装历史会话（复用 GetTaskHistory）
  ├─ 6. LLM 工具调用循环（复用 llm.Client.Generate）
  ├─ 7. 回写结果 + RunLog + ToolCallLog + Token 用量
  └─ 8. UpdateTask → NATS → SSE
```

### 6.1 Agent 配置加载与热加载

- **加载**：`agent:{id}` 存 Redis 缓存（TTL 短，如 60s），未命中查 MySQL 回写。
- **热加载**：更新 Agent 时 `DEL agent:{id}`，Worker 下次自然取到新配置，无需重启。
- **一致性**：配置属于「低频读、低频写」，Cache-Aside 足够，无需强一致。

### 6.2 知识库检索的租户隔离

- RAG：每个知识库一个独立 RedisVector 索引（`idx:kb:{kbID}`），替换现在全局 `idx:pangu_v2`。
- KAG：Neo4j 节点带 `kb_id` 属性，查询时按 `kb_id` 过滤，避免跨租户串数据。

---

## 7. 工具系统（Tool Registry）

### 7.1 设计

```go
// 统一工具抽象
type ToolHandler interface {
    Name() string
    Schema() jsonschema.Definition
    Execute(ctx, args map[string]any) (string, error)
}

// 注册中心
type ToolRegistry struct {
    builtin map[string]ToolHandler        // 代码内置
    mcp     map[string]*mcpclient.Wrapper // 运行时加载的 MCP 工具
}
```

- **内置工具**：代码注册（如计算器、当前时间、HTTP 请求等），启动时装入。
- **MCP 工具**：从 `tools` 表读取连接配置，`mcpclient.NewWrapper` 建立 stdio 连接，`GetTools` 拉取工具列表注册。
- **白名单**：执行前校验 `agent_tools` 里是否绑定该工具，未绑定的一律拒绝调用。

### 7.2 现实工程考量

- **连接管理**：MCP stdio 是子进程，需要**连接池/懒加载 + 空闲回收**，避免每个任务都拉起子进程。
- **凭证加密**：MCP 连接配置中的 API Key 用 KMS/环境密钥加密后落库，不在日志里打印。
- **失败隔离**：单个工具挂掉不拖垮整个任务，工具调用失败降级为「工具返回错误」回传给 LLM，而非任务失败。
- **超时**：每个工具调用设独立超时（如 10s），防止工具卡死阻塞 Worker。

---

## 8. 知识库构建（异步）

复用现有队列能力，把「文档入库」变成异步任务，避免上传大文档时阻塞 HTTP。

```text
上传文档
  → 存对象存储/DB，Document 状态 = uploaded
  → 入队 "kb:build"（Asynq，低优先级）
  → Worker 消费：
      分块(500/50) → embedding(bge-m3) → 写 RedisVector
      → （hybrid 类型）LLM 抽三元组 → 写 Neo4j
      → Document 状态 = ready，KB.DocCount+1
  → 构建完成 → NATS → 前端刷新状态
```

- **幂等**：文档以 `content hash` 去重，重复上传同一内容不重复构建。
- **失败重试**：复用 Asynq 指数退避，超限进入死信，支持手动重试。
- **大文件**：分块流式处理，单文档过大时拆分多个 chunk 批处理。

---

## 9. 会话与记忆

- 新增独立 `sessions` 表（session_id, user_id, agent_id, title, created_at），替代现在仅靠 `task.session_id` 隐式关联。
- 记忆策略沿用**滑动窗口**（取最近 N 条已完成任务），N 可配，Agent 级别覆盖。
- 会话标题用首条用户输入的摘要生成，提升前端可读性。

---

## 10. 流式推送（SSE）

- 复用现有 `StreamTasks` + NATS 链路，**无需改动**。
- 增强点：事件区分类型（`task_update` / `token_stream` / `tool_call`），让前端能展示「工具调用过程」和「打字机效果」。
- 现实考量：SSE 是单向，若未来要「用户在生成中打断」，配合现有 gRPC `CancelTask` 即可。

---

## 11. 多租户与权限

- 所有资源（Agent/Tool/KB/Run）带 `user_id`，查询强制过滤。
- **工具可见性**：内置工具全租户可见；MCP 工具默认私有（`owner_id`），可扩展「共享/发布」。
- **权限模型**：Phase 1 用「资源归属 = 访问权限」的简单模型；团队协作留到 Phase 3 再上 RBAC。

---

## 12. 安全设计（Agent 平台的难点）

| 风险 | 对策 |
|---|---|
| **工具越权调用** | agent_tools 白名单 + 执行前校验；危险工具（如 shell、文件写）默认禁用或需显式授权 |
| **Prompt 注入** | 用户输入与 system_prompt 严格分层，检索到的外部内容用 `<context>` 标记并声明「仅供参考，不可覆盖系统指令」 |
| **输出注入/敏感信息** | 输出做敏感词过滤；工具返回内容先清洗再回填 |
| **知识库越权** | RAG 索引按 kb_id 隔离，KAG 查询带 kb_id 过滤 |
| **凭证泄露** | MCP 凭证加密存储，日志脱敏，`Secret` 不进日志 |
| **滥用/爆破** | 每 Agent 限流 + 配额（见 13） |

---

## 13. 成本与配额

LLM 成本是 Agent 平台最大的隐性成本，必须显式治理：

- **Token 计量**：每次调用记录 `TokensIn / TokensOut`，按模型单价折算 `CostUSD`，写入 RunLog。
- **配额体系**：
  - 用户级：日/月 token 上限、并发运行上限。
  - Agent 级：单 Agent 的 token 上限（防止单个 Agent 失控）。
- **限流与熔断**：超配额返回 429 或直接熔断该 Agent 的运行，避免成本雪崩。
- **降级**：可配置「超预算时降级到更小模型」。

---

## 14. 可观测性

- **结构化日志**：沿用现有 `slog`，关键日志统一带 `trace_id / task_id / agent_id / user_id`。
- **指标（Prometheus）**：任务数、耗时分布、队列深度、token 成本、工具调用失败率、错误率。
- **链路追踪**：`trace_id` 贯穿 API → 队列 → Worker → LLM → 工具调用，落在 RunLog 和 ToolCallLog 上。
- **队列监控**：继续用 Asynqmon，补充「按 Agent 维度」的失败/重试统计。

---

## 15. 高可用与扩展

- **Worker 无状态**：配置在 MySQL/Redis，运行无本地状态，天然支持 K8s 水平扩容（Deployment.replicas）。
- **Redis**：沿用 Sentinel（已支持），规模化后演进 Cluster；向量索引按 kb 分片。
- **MySQL**：主从读写分离；观测表（RunLog/ToolCallLog）量大，可独立库或分表。
- **MCP 子进程**：Worker 容器内预置 MCP 工具二进制（解决当前容器内 `search_mcp.exe` 缺失的问题）。

---

## 16. 分阶段落地计划

### Phase 1：最小闭环（先跑通）
1. 新增 `Agent` 模型 + 表，`Task` 加 `agent_id`。
2. Worker 改成「按 Agent 配置加载 system_prompt/模型」，其余沿用。
3. 新增 Agent 管理 API + 运行 API。
> 目标：能用「持久化的 Agent 配置」代替「每次临时拼 Prompt」。

### Phase 2：工具 + 知识库平台化
1. 引入 Tool Registry，支持内置 + MCP 多工具。
2. 知识库异步构建，RAG 索引按 kb 隔离。
3. agent_tools / agent_kbs 绑定关系。

### Phase 3：多租户 + 成本 + 观测
1. 全量 `user_id` 隔离 + 权限校验。
2. Token 计量 + 配额 + 限流熔断。
3. RunLog/ToolCallLog + Prometheus 指标。

### Phase 4：安全加固 + 高可用
1. 工具白名单、Prompt 注入防护、输出过滤。
2. Worker K8s 化、Redis/MySQL 高可用、MCP 工具容器化。

---

## 17. 技术风险与权衡

| 风险/权衡 | 说明 | 决策 |
|---|---|---|
| 多租户 schema | 共享 schema 简单但隔离靠代码 | Phase 1 用共享 schema + 强制 user_id 过滤 |
| 向量库隔离成本 | 每 KB 一个索引，索引多时内存/维护成本上升 | 先用按 kb 分索引，规模大再考虑统一索引 + 元数据过滤 |
| MCP stdio vs HTTP | stdio 是子进程，难做连接复用；HTTP 需额外部署 | 短期 stdio + 连接池，长期支持 HTTP 传输 |
| 配置热加载 vs 强一致 | 热加载简单但有短暂不一致窗口 | Agent 配置用 Cache-Aside + 短 TTL |
| Token 成本不可控 | 用户滥用会成本失控 | 必须 Phase 3 上配额，不能后补 |
| 工具安全 | 工具是最大攻击面 | 白名单 + 默认拒绝，危险工具显式授权 |

---

## 18. 与现有代码的对照改造点

| 现有文件 | 改造内容 |
|---|---|
| [model/task.go](file:///d:/Program%20Files/GoTaskAI/internal/model/task.go) | 加 `AgentID`，新增 Agent/Tool/KB/RunLog 模型 |
| [internal/api/handler.go](file:///d:/Program%20Files/GoTaskAI/internal/api/handler.go) | submit 支持 `agent_id`；新增 agent/tool/kb 管理 handler |
| [internal/worker/pool.go](file:///d:/Program%20Files/GoTaskAI/internal/worker/pool.go) | handleTaskProcess 改成 dispatch + 按 Agent 组装 |
| [internal/pkg/mcpclient/client.go](file:///d:/Program%20Files/GoTaskAI/internal/pkg/mcpclient/client.go) | 从单工具改为 Tool Registry 多工具 |
| [internal/api/rag.go](file:///d:/Program%20Files/GoTaskAI/internal/api/rag.go) | 向量索引按 kb_id 隔离，入库改异步 |
| [internal/pkg/kag/kag.go](file:///d:/Program%20Files/GoTaskAI/internal/pkg/kag/kag.go) | Neo4j 节点带 kb_id，查询按 kb 过滤 |
| [cmd/worker/main.go](file:///d:/Program%20Files/GoTaskAI/cmd/worker/main.go) | 注册工具、预置 MCP 工具二进制 |

---

## 19. 一句话总结演进价值

> 从「我会把一堆 AI 技术串起来」升级为「我设计了一个可运营、可扩展、可控成本的 Agent 基础设施平台」，后端调度/多租户/安全/成本这些工程能力成为项目的真正壁垒。

---

## 20. 本轮落地执行流程（数据模型演进）

> 本章记录从「任务对话中心」向「Agent 平台」演进时，底层数据模型的具体改造顺序与当前进展。前文第 4 节为完整目标结构，本章聚焦**执行路径**，避免一步到位式的大迁移。

### 20.1 现状基线（已就绪的部分）

- [Agent](file:///d:/Program%20Files/GoTaskAI/internal/model/agent.go) 已是独立表，具备 `ID / UserID / Name / SystemPrompt / Model / Status / 时间`。
- [Task](file:///d:/Program%20Files/GoTaskAI/internal/model/task.go) 已有 `AgentID` 字段，`RunAgent` 会写入 `AgentID`，Worker 会按 `AgentID` 加载 Agent 的 `system_prompt`。
- 前端工作台已从「会话列表 + 对话」改为「Agent 列表 + 对话」，发送走 `POST /api/agents/:id/run`。

**结论**：最小适配（Agent 表 + Task.AgentID）已完成，不需要为「绑定 Agent」再动表结构。

### 20.2 仍需补齐的数据缺口

| 缺口 | 现状 | 影响 |
|---|---|---|
| 会话不是实体 | 仅靠 `Task.SessionID` 字符串隐式关联 | 无法管理会话标题、归属 Agent、独立生命周期 |
| 无工具表 | 仅 [mcpclient](file:///d:/Program%20Files/GoTaskAI/internal/pkg/mcpclient/client.go) 单工具 | 撑不起「工具平台」 |
| 无知识库表 | RAG/KAG 逻辑存在，但无持久化实体 | 无法按 Agent 绑定知识库 |
| Task 职责偏重 | 同时承载「对话消息」与「后台任务」 | 语义混叠，长期需拆分 |

### 20.3 目标数据模型（增量）

在现有 `User / Agent / Task` 基础上新增：

```text
User 1 ──── N Conversation
Agent 1 ─── N Conversation
Conversation 1 ─ N Task

Agent N ──── N Tool          (通过 AgentTool)
Agent N ──── N KnowledgeBase (通过 AgentKnowledge)
```

新增表：

- `Conversation`：`ID / UserID / AgentID / Title / CreatedAt / UpdatedAt`
- `Tool`、`KnowledgeBase`、`AgentTool`、`AgentKnowledge`（结构见第 4.2 节）
- `Task` 增加 `ConversationID`，逐步替代 `SessionID` 字符串。

### 20.4 实现流程（5 阶段）

**阶段 1：定义模型 + 迁移（纯后端）**
- 在 [internal/model](file:///d:/Program%20Files/GoTaskAI/internal/model) 新增 `conversation.go`、`tool.go`、`knowledge_base.go` 及关联表结构。
- 给 [Task](file:///d:/Program%20Files/GoTaskAI/internal/model/task.go) 加 `ConversationID`。
- 在 [cmd/api/main.go](file:///d:/Program%20Files/GoTaskAI/cmd/api/main.go) 的 `AutoMigrate` 注册新表。
- 兼容策略：保留 `SessionID`，用回填脚本把旧 `SessionID` 迁移为 `Conversation` 记录。

**阶段 2：数据访问层（CRUD）**
- 在 [manager.go](file:///d:/Program%20Files/GoTaskAI/internal/queue/manager.go) 或拆出 `ConversationManager / ToolManager / KnowledgeManager`。
- `RunAgent` / `SubmitTask` 从「直接写 SessionID」改为「查找或创建 Conversation，再写 ConversationID」。

**阶段 3：API 层**
- 新增 `/api/conversations`、`/api/tools`、`/api/knowledge-bases` 及 `/api/agents/:id/tools`、`/api/agents/:id/knowledge-bases` 路由。
- 保持现有 `/api/tasks`、`/api/agents` 兼容。

**阶段 4：Worker 适配**
- [pool.go](file:///d:/Program%20Files/GoTaskAI/internal/worker/pool.go) 的多轮上下文按 `ConversationID` 拉取。
- 运行 Agent 时加载其绑定的工具 / 知识库，完成「工具平台」闭环。

**阶段 5：前端联动**
- 工作台「Agent 列表 + 对话」对接 `/api/conversations`。
- 「工具」「知识库」占位面板对接真实接口。
- 任务中心弱化为「原始任务查询」，不再作为中心入口。

### 20.5 推荐落地顺序

1. 先做「阶段 1 + 阶段 2 的 Conversation 部分 + 阶段 5 的工作台对接」，直接呼应工作台已改为 Agent 对话的方向。
2. 下一轮再补「工具 / 知识库」表与接口。

### 20.6 当前进展记录

- 已完成：前端工作台由「会话列表 + 对话」改为「Agent 列表 + 对话」，输入区通过 `runAgent` 绑定 Agent。
- 已完成：任务中心与工作台去重，删减重叠的会话任务列表。
- 待办：数据模型阶段 1（`Conversation` 表 + `Task.ConversationID` 迁移）。
