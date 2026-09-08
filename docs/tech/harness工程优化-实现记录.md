# GoTaskAI Harness 工程优化 — 实现记录

> 本文档是对 [harness工程优化方向-修订版.md](./harness工程优化方向-修订版.md) 六项目标的**落地实现说明**，逐项记录「改了什么、为什么这么改、核心代码落在哪、怎么验收」。
> 面向面试讲解与后续维护，配合修订版方案一起阅读效果最好。

---

## 1. 总体结论

六项目标全部实现并通过验收，把 GoTaskAI 从「异步 AI 任务调度平台」补齐为「可验证、可控制、可计量、可观测、可回归」的 Agent Harness。

| # | 目标 | 优先级 | 状态 |
|---|---|---|---|
| 1 | 验证循环（参数解析修复 + Schema 校验） | P0 | ✅ 已完成 |
| 2 | 成本预算与熔断 | P0 | ✅ 已完成 |
| 3 | 可观测性（RunLog/ToolCallLog + Prometheus） | P0 | ✅ 已完成 |
| 4 | 确定性控制（NATS 广播 + CAS 写回） | P1 | ✅ 已完成 |
| 5 | 上下文渐进式披露 | P1 | ✅ 已完成 |
| 6 | Agent 评估 harness | P2 | ✅ 已完成 |

---

## 2. 目标一：验证循环（P0）

### 2.1 问题定位

修订版审查确认：工具执行失败「以 tool 角色错误消息回传 LLM」的降级机制**原本就已存在**（[client.go](file:///d:/Program%20Files/GoTaskAI/internal/pkg/llm/client.go) 的 `generateWithClient` 循环）。真实缺口有两个：

1. **前置缺陷（F2）**：工具参数 `json.Unmarshal` 失败时**静默 `continue`**——既不追加 tool 响应、也不通知 observer、更不回传 LLM。结果 `messages` 里出现「带 `tool_calls` 的 assistant 消息」却没有配对 tool 响应，严格提供方（OpenAI 系）下一次请求会直接 400。
2. **无 Schema 校验**：工具入参没有任何结构化校验，非法参数会被直接传给工具执行。

### 2.2 实现思路

复用既有降级链路（tool 角色错误消息），把「静默跳过」改成「错误回传 + 自修正」：

1. 参数 `json.Unmarshal` 失败 → 构造 `Error executing tool {name}: invalid arguments` 的 tool 消息回传 LLM，并通知 observer。
2. 以 MCP 工具自带的 JSON Schema 校验入参，非法同样回传 LLM 自修正。

### 2.3 核心实现

在 [client.go](file:///d:/Program%20Files/GoTaskAI/internal/pkg/llm/client.go#L258-L293) 的 `generateWithClient` 工具循环内：

- **参数解析失败兜底**（不再静默）：

```go
if err := json.Unmarshal([]byte(toolCall.Function.Arguments), &args); err != nil {
    contentStr := fmt.Sprintf("Error executing tool %s: invalid arguments: %v", toolName, err)
    if observer != nil {
        observer(ToolCallObservation{ToolName: toolName, Arguments: toolCall.Function.Arguments, Status: "failed", Error: contentStr})
    }
    messages = append(messages, openai.ChatCompletionMessage{
        Role: openai.ChatMessageRoleTool, Content: contentStr, Name: toolName, ToolCallID: toolCall.ID,
    })
    continue
}
```

- **Schema 校验**（新增 `toolArgumentsSchema` / `validateToolArguments`）：

```go
if schema := toolArgumentsSchema(tools, toolName); schema != nil {
    if err := validateToolArguments(*schema, args); err != nil {
        // 同样以 tool 错误消息回传 LLM 自修正
    }
}
```

`validateToolArguments` 内部：schema 为空（无 Type/Required/Properties）视为通过；否则调 `jsonschema.Validate` 校验。

### 2.4 验收

F2 回归用例：注入参数非法的 `tool_calls`，观察到错误以 tool 消息回传（非静默），`messages` 永不出现孤儿 `tool_calls`，任务最终回写正确状态。

---

## 3. 目标二：成本预算与熔断（P0）

### 3.1 问题定位

修订版识别出四条成本漏洞：

1. **双层重试并存且互不知情**：业务级 `Task.MaxRetry` × asynq 队列级 `asynq.MaxRetry(3)` 各自计数，最坏组合放大成本。
2. **`kb:build` 队列漏网**：`EnqueueKBBuild` 未设 MaxRetry，走 asynq 默认约 25 次重试，且每次重试重跑 LLM 实体抽取 + embedding。
3. **token 被丢弃**：`resp.Usage` 的 TokensIn/Out 已返回但未计量，预算无数据来源。
4. **超时硬编码**：LLM 调用固定 2 分钟，超时与重试叠加放大成本。

### 3.2 实现思路

引入运行级预算对象，注入到模型调用循环（唯一自然位置），覆盖「单任务单次上限 + 跨重试累计上限」两层，并从 `resp.Usage` 计量 token。

### 3.3 核心实现

**预算对象** [budget.go](file:///d:/Program%20Files/GoTaskAI/internal/pkg/llm/budget.go)：

```go
type Budget struct {
    MaxCalls       int     // 单次运行模型调用次数上限（防工具乒乓）
    MaxTotalCalls  int     // 任务跨重试累计上限（防队列级重试放大）
    PriceInUSDPerMTok  float64
    PriceOutUSDPerMTok float64
    Calls, TokensIn, TokensOut int // 本次运行累计
    baseCalls, baseTokensIn, baseTokensOut int // 历史重试已累计
}
```

关键方法：

- `BeforeCall()`：每次调用前检查，`Calls >= MaxCalls` 或 `baseCalls+Calls >= MaxTotalCalls` 返回 `ErrBudgetExceeded`。
- `Record(tokensIn, tokensOut)`：每次调用后累加。
- `SetBase(...)`：从任务累计字段回填历史重试计量，实现跨重试续算。
- `CostUSD()`：按 `TokensIn×单价/1e6 + TokensOut×单价/1e6` 折算美元成本。
- `ErrBudgetExceeded`：被 worker 视为**不可重试错误**（重试只会继续被累计上限拦截），直接置 `failed`。

**注入循环** [client.go](file:///d:/Program%20Files/GoTaskAI/internal/pkg/llm/client.go#L189-L214)：`generateWithClient` 的 `for` 循环开头调 `budget.BeforeCall()`，调用后 `budget.Record(resp.Usage.PromptTokens, resp.Usage.CompletionTokens)`。

**worker 构造预算** [pool.go](file:///d:/Program%20Files/GoTaskAI/internal/worker/pool.go#L560-L580)：`handleTaskProcess` 提前构造预算并 `SetBase`（从任务累计字段回填），使检索阶段的 KAG 关联度过滤 LLM 调用同样纳入计量。

**跨重试累计 + 缓存失效** [manager.go](file:///d:/Program%20Files/GoTaskAI/internal/queue/manager.go#L412-L428)：`AccumulateTaskUsage` 用 `gorm.Expr` 原子累加 `total_calls/total_tokens_in/total_tokens_out`，**累加后显式 `rdb.Del("task:"+taskID)` 失效缓存**——否则下次重试读到过期计数，跨重试熔断会失效。

**队列层收口** [manager.go](file:///d:/Program%20Files/GoTaskAI/internal/queue/manager.go#L896-L907)：`EnqueueKBBuild` 显式 `asynq.MaxRetry(3)`。

**超时配置化** [pool.go](file:///d:/Program%20Files/GoTaskAI/internal/worker/pool.go#L658-L661)：2 分钟硬编码改为读 `config.AppConfig.LLM.TimeoutSeconds`。

### 3.4 验收

`task:process` 与 `kb:build` 两队列均在预算内熔断；`ErrBudgetExceeded` 直接置 `failed` 不重试；asynq 死信队列无超出上限的重试留存。

---

## 4. 目标三：可观测性（P0）

### 4.1 实现内容

**观测表** [run_log.go](file:///d:/Program%20Files/GoTaskAI/internal/model/run_log.go) / [tool_call_log.go](file:///d:/Program%20Files/GoTaskAI/internal/model/tool_call_log.go)：

- `RunLog`：`task_id/trace_id/user_id/agent_id/model/status/calls/tokens_in/tokens_out/cost_usd/duration_ms/error`。
- `ToolCallLog`：`task_id/trace_id/tool_name/arguments/status/result/error`。

二者已在 [cmd/api/main.go](file:///d:/Program%20Files/GoTaskAI/cmd/api/main.go#L46) 的 `AutoMigrate` 注册链中注册建表。

**trace_id 透传**：`Task.TraceID` 字段已新增，随 Asynq payload 与 `PublishToolEvent` 事件天然透传，各环节写观测表时带上。

**Prometheus 指标** [metrics.go](file:///d:/Program%20Files/GoTaskAI/internal/metrics/metrics.go)（9 组）：

| 指标 | 类型 | 说明 |
|---|---|---|
| `gotaskai_task_duration_seconds` | HistogramVec | 任务耗时，按 status，bucket 覆盖 1s~300s |
| `gotaskai_task_runs_total` | CounterVec | 任务运行次数（含重试），按 status |
| `gotaskai_task_retries_total` | Counter | 任务重试次数 |
| `gotaskai_llm_calls_total` | CounterVec | 模型调用次数，按 queue |
| `gotaskai_llm_tokens_total` | CounterVec | token 消耗，按 queue + direction |
| `gotaskai_llm_cost_usd_total` | CounterVec | 成本（美元），按 queue |
| `gotaskai_tool_calls_total` | CounterVec | 工具调用次数，按 status |
| `gotaskai_kb_build_total` | CounterVec | 知识库构建数量，按 status |
| `gotaskai_context_compress_total` / `gotaskai_context_compress_failed_total` | Counter | 压缩尝试 / 失败次数（失败率=后者/前者） |

**指标埋点** [pool.go](file:///d:/Program%20Files/GoTaskAI/internal/worker/pool.go#L769-L806) 的 `saveRunLog` 统一完成：任务状态/耗时、LLM 调用/token/成本（`task:process` 队列）；`handleKBBuild` 单独计量摘要生成（`kb:build` 队列）。

**压缩失败告警**：`loadConversationHistory` 中压缩失败时 `metrics.CompressFailed.Inc()` + `log.Printf("[Worker][ALERT] ...")`，不再静默回退全量历史。

**/metrics 端点**：

- API 进程挂载在 `server.port` 上（[cmd/api/main.go](file:///d:/Program%20Files/GoTaskAI/cmd/api/main.go#L113-L114)）。
- Worker 各子进程使用独立端口 `metrics.port + childIndex + 1`（[cmd/worker/main.go](file:///d:/Program%20Files/GoTaskAI/cmd/worker/main.go#L154-L176)），避免多子进程端口冲突。

### 4.2 验收

RunLog/ToolCallLog 落表；`/metrics` 可采集；压缩失败产生可告警信号而非静默。

---

## 5. 目标四：确定性控制（P1）

### 5.1 问题定位（整章重写的原因）

修订版发现原「注册 gRPC Server 即可强制中断」方案**无法兑现**，三个事实：

1. LLM 调用 ctx 派生自 `context.Background()` + 2 分钟硬编码，与 asynq 取消信号脱钩——即使注册 gRPC 也无法打断进行中的 LLM 调用。
2. Worker 为多子进程 supervisor 架构，API 不知道任务落在哪个子进程，gRPC 单播需要未设计的在途注册表/服务发现。
3. 回写是无条件 `Save`，两次软检查与最终写回之间存在 TOCTOU 窗口。

### 5.2 三层方案

**① 可取消 ctx 派生** [pool.go](file:///d:/Program%20Files/GoTaskAI/internal/worker/pool.go#L656-L665)：以 asynq 传入的 ctx 为父（而非 `context.Background()`）派生可取消上下文并叠加配置超时，取消指令或 asynq 取消都会在下一次 socket 读时中断 LLM HTTP 调用。

**② NATS 广播控制通道**（替代 gRPC）：

- 新增事件主题 `global_task_cancel`（[events.go](file:///d:/Program%20Files/GoTaskAI/internal/events/events.go#L28)）。
- `PublishTaskCancel`：API 侧发布取消指令（[manager.go](file:///d:/Program%20Files/GoTaskAI/internal/queue/manager.go#L453-L460)）。
- `SubscribeTaskCancel`：Worker 订阅，命中本进程 in-flight 注册表时 `cancel()` 中断（[manager.go](file:///d:/Program%20Files/GoTaskAI/internal/queue/manager.go#L462-L478)）。
- in-flight 注册表 `registerInFlight/unregisterInFlight/cancelTask`（[pool.go](file:///d:/Program%20Files/GoTaskAI/internal/worker/pool.go#L149-L172)）。
- gRPC 桩 `internal/worker/grpc_server.go` 与 `api/proto/worker/*` **已删除**。

**③ CAS 条件写回**：

- `UpdateTaskIfActive`：`WHERE id=? AND status NOT IN ('cancelled','completed')` 条件更新，0 行受影响即任务已被并发修改，丢弃结果（[manager.go](file:///d:/Program%20Files/GoTaskAI/internal/queue/manager.go#L372-L400)）。
- `CancelTaskIfActive`：`WHERE id=? AND status IN ('pending','processing')` 才置 `cancelled`，避免覆盖已进入终态的任务（[manager.go](file:///d:/Program%20Files/GoTaskAI/internal/queue/manager.go#L430-L451)）。
- worker 侧一切终态/中间态回写统一走 `UpdateTaskIfActive`。

### 5.3 验收

取消请求发出后，阻塞中的 LLM 调用在有界时间内（下一次流式读）被中断；取消后的任务不会被回写 completed（由条件更新机制保证）；重试置 pending 同受守卫保护。

---

## 6. 目标五：上下文渐进式披露（P1）

### 6.1 问题定位

原「按检索得分决定注入深度」只对 RAG 成立——RAG 带 Score，KAG/Text2Cypher 返回无分数、无逐条粒度的纯字符串注入，跨两路分数不可比，统一阈值是陷阱。

### 6.2 实现思路

双路分别定深度判据 + 摘要层生产机制并入 kb:build + 命中片段截断与总量上限。

### 6.3 核心实现

**RAG 按 Score 分层** [pool.go](file:///d:/Program%20Files/GoTaskAI/internal/worker/pool.go#L333-L372)：

- `normalizeScores`：min-max 归一化到 [0,1]（最低分=0、最高分=1），使 RRF 与 reranker 分数可统一比较。
- `chunkByScore`：`Score >= rag_score_threshold` 注入全文截断，低分降级为更短截断（`max_chunk_chars/2`）。
- `truncateChunk`：按 rune 截断，避免破坏多字节字符。

**摘要层生产并入 kb:build** [pool.go](file:///d:/Program%20Files/GoTaskAI/internal/worker/pool.go#L833-L853)：`handleKBBuild` 构建期调用 `llm.SummarizeText` 生成 per-doc 摘要，写入 `Document.Summary`；运行期 `collectDocSummaries` 按命中文档顺序注入摘要层，避免运行期临时调 LLM 生成摘要。

**KAG 双判据** [pool.go](file:///d:/Program%20Files/GoTaskAI/internal/worker/pool.go#L612-L644)：

- 命中条数上限 `graph_top_n`（`RetrieveGraphContext` 传入）。
- LLM 关联度过滤 `IsContextRelevant`（单独判据，不共用 RAG 阈值），该次判定纳入 budget 计量。

**总量上限**：命中片段 `max_chunk_chars` 截断 + 两路共享 `max_context_chars` 总量上限（KAG 按 RAG 已用剩余量截断）。

### 6.4 验收

知识量大时上下文体积下降、质量不降（由目标六统计度量）。

---

## 7. 目标六：Agent 评估 harness（P2）

### 7.1 问题定位

LLM 输出天然非确定（temperature/top_p 未暴露、走提供方默认采样），「可复现」的质量报告按字面不可达，改为「统计稳定」。

### 7.2 实现内容

**评测用例与报告** [eval.go](file:///d:/Program%20Files/GoTaskAI/internal/eval/eval.go)：

- `Case{Question, ExpectedTools, ExpectedAnswer}`：问题 + 期望工具 + 期望答案（答案用于语义相似度评测）。
- `Config{Model, Sampling, Iterations}`：固定采样 + 每例独立运行 N 次。
- `CaseReport`：工具命中率 / 答案相似度的均值、样本方差、标准差、95% CI 半宽。
- `Runner.runCase`：预计算期望答案 embedding（失败计 `EmbeddingErrors`），循环 `runOnce`。
- `stats`：样本均值/方差/标准差 + 95% CI 半宽，**半宽裁剪到 [0,1]** 避免区间越界。
- `cosineSimilarity`：答案语义相似度。

**采样参数暴露** [client.go](file:///d:/Program%20Files/GoTaskAI/internal/pkg/llm/client.go#L118-L123)：新增 `Sampling{Temperature, TopP}` 与 `GenerateWithToolsAndObserverSampling`，供评测固定采样。

**embedding 失败治理**：`len(expectedVec) > 0 && !embFailed` 才计答案相似度；`EmbeddingErrors` 单列，不再静默记 0 分。

**CI 回归闸门** [cmd/eval/main.go](file:///d:/Program%20Files/GoTaskAI/cmd/eval/main.go)：

- 内置 cases 含 `ExpectedAnswer`（覆盖 `get_current_time` 工具类用例的真实期望答案）。
- `-threshold` 参数：任一用例工具命中率均值低于阈值时以非零退出码结束（默认 0.8）。

**单测**：`stats` / `cosineSimilarity`（[eval_test.go](file:///d:/Program%20Files/GoTaskAI/internal/eval/eval_test.go)）、`normalizeScores` / `truncateChunk` / `chunkByScore`（[pool_test.go](file:///d:/Program%20Files/GoTaskAI/internal/worker/pool_test.go)）。

### 7.3 验收

对某 Agent+KB 组合产出统计稳定的质量报告——同输入多次运行，指标波动落在置信区间内，作为后续改动的回归护栏。

---

## 8. 新增/变更配置项汇总

| 配置段 | 字段 | 作用 |
|---|---|---|
| `metrics` | `port` | Worker 子进程 /metrics 基础端口（实际用 port+childIndex+1） |
| `llm` | `embedding_model` / `embedding_api_key` / `embedding_base_url` | 独立 embedding 端点（DashScope Qwen3-Embedding） |
| `llm` | `timeout_seconds` | 单次 LLM 调用超时 |
| `llm` | `max_calls_per_task` | 单任务单次运行模型调用上限 |
| `llm` | `max_total_calls_per_task` | 单任务跨重试累计调用上限 |
| `llm` | `cost_per_1m_in` / `cost_per_1m_out` | 模型输入/输出单价（美元/百万 token） |
| `llm` | `context_window` / `compress_threshold` | 上下文窗口与压缩触发比例 |
| `compress` | `enabled` / `max_history` / `keep_recent` / `min_turns` | 会话历史自动压缩 |
| `rerank` | `enabled` / `base_url` / `model` / `top_n` | 召回重排 |
| `context` | `max_chunk_chars` / `max_context_chars` / `rag_score_threshold` / `graph_top_n` | 渐进式披露参数 |
| `milvus` | `host` / `port` / `collection_name` / `dim` | Milvus 向量库连接 |

---

## 9. 数据模型变更

- `Task`：新增 `TraceID`、`TotalCalls`、`TotalTokensIn`、`TotalTokensOut`、`ConversationID`。
- `Document`：新增 `Summary`（构建期摘要层）。
- `Conversation`：新增 `Summary`、`SummaryCutoff`（滚动摘要）。
- 新增 `RunLog`、`ToolCallLog`。

---

## 10. 文件改动清单

**新增**

- `internal/pkg/llm/budget.go` — 成本预算对象
- `internal/pkg/llm/embedder.go` — DashScope 向量化器（独立 embedding 端点）
- `internal/metrics/metrics.go` — Prometheus 指标定义
- `internal/model/run_log.go` / `internal/model/tool_call_log.go` — 观测表模型
- `internal/eval/eval.go` + `internal/eval/eval_test.go` — 评估 harness
- `cmd/eval/main.go` — 评估 CLI（含 CI 阈值闸门）

**修改**

- `internal/pkg/llm/client.go` — 参数解析修复、Schema 校验、Sampling、SummarizeText、IsContextRelevant、CompressHistory、独立 embeddingClient
- `internal/worker/pool.go` — 预算构造、可取消 ctx、in-flight 注册表、渐进披露、摘要层、RunLog/Prometheus 埋点
- `internal/queue/manager.go` — CAS 写回、取消广播、AccumulateTaskUsage、EnqueueKBBuild 收口
- `internal/events/events.go` — 新增 `global_task_cancel` 主题
- `internal/model/task.go` / `document.go` / `conversation.go` — 字段扩展
- `internal/config/config.go` + `config/config.yaml` — 新增配置段
- `cmd/api/main.go` / `cmd/worker/main.go` — AutoMigrate 注册 + /metrics 端点

**删除**

- `internal/worker/grpc_server.go`（gRPC 取消桩死代码）
- `api/proto/worker/*`

---

## 11. 测试与验收

- 单元测试：`go test ./internal/eval/... ./internal/worker/...`（覆盖 stats、cosineSimilarity、normalizeScores、truncateChunk、chunkByScore）。
- 评估运行：`go run ./cmd/eval -threshold 0.8`。
- 验收结论：六项目标全部实现并通过工程验收（project-acceptance-auditor）。
- 遗留：`internal/api/auth.go:142` 存在一处冗余 `\n`（go vet 提示），不影响功能，未修复。

---

## 12. 关键设计要点（面试可讲）

1. **预算注入在模型调用循环**：只有 `generateWithClient` 的 `for` 循环是「每一次模型调用」的必经点，预算检查放这里才能同时兜住「工具乒乓循环」和「重试翻倍」。
2. **跨重试累计必须失效缓存**：累计字段落 MySQL 后，Redis 里的任务缓存若不失效，下次重试 `GetTask` 读到旧计数，跨重试熔断形同虚设。
3. **取消控制走 NATS 而非 gRPC**：多子进程 supervisor 架构下，广播 + 本地 in-flight 查询免服务发现、免在途注册表，天然契合；正确性靠 CAS 条件写回兜底。
4. **渐进披露的双路判据不可混用**：RAG 有归一化 Score，KAG 无分数，只能「条数上限 + LLM 关联度过滤」单独定义。
5. **评测的「统计稳定」而非「可复现」**：LLM 输出天然非确定，固定采样 + N 次运行 + 置信区间才是现实可验收的护栏。
