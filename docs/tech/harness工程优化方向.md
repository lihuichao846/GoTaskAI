# GoTaskAI Harness 工程化优化方向

> **一句话定位**：把 GoTaskAI 从「异步 AI 任务调度平台」升级为「完整的 Agent Harness」——补齐模型之外那一层「验证、控制、成本、可观测」的护栏，让 Agent 不只在演示里能跑，更能在线上长期稳定地跑。

---

## 0. 为什么做（Harness 视角）

Harness 指「模型之外、环绕模型的那一层运行系统」。核心理念：**Agent 失败绝大多数不是模型失败，而是 harness 失败**——换底层模型输出只差 10-15%，但换 harness 设计决定系统到底能不能跑（即所谓的「80% 因素」）。

对照本项目的现状：

| Harness 要素 | GoTaskAI 现状 |
|---|---|
| Tool Interface（工具编排） | ✅ 已成熟：MCP 工具平台、toolregistry、`mcpPool` 懒加载、Agent 白名单 |
| Context Management（上下文） | ✅ 已成熟：RAG/KAG 双路召回 + systemPrompt 组装 + 最近 6 条会话记忆 |
| Agent Loop / 异步实时 | ✅ 已成熟：Asynq 队列 + NATS 事件 + SSE 实时推送 |
| **Verification（验证）** | ⚠️ 缺失：工具结果/输出无结构校验 |
| **Cost Control（成本）** | ⚠️ 薄弱：有 MaxRetry 限重试，但无调用次数/费用预算熔断 |
| **Control（确定性控制）** | ⚠️ 薄弱：gRPC `CancelTask` 桩存在但未注册未监听 |
| **Observability（可观测）** | ⚠️ 规划中：RunLog/ToolCall 现无，trace_id 未落地 |

**结论**：最难的「工具编排 + 上下文 + 异步/实时」三件套已就位，缺的是半块围栏——验证、成本、控制、可观测。这正是 Harness 强调的方向。

---

## 1. 六项优化目标总览

| # | 优化目标 | 优先级 | 核心价值 |
|---|---|---|---|
| 1 | 验证循环（Verification Loops） | P0 | 探测并纠正 Agent 错误，不让坏结果传染下一步 |
| 2 | 成本预算与熔断（Cost Envelope） | P0 | 防重试/恶性循环烧钱，最大线上风险 |
| 3 | 可观测性（Trace + RunLog/ToolCall） | P0 | 看清每一步在干什么，是一切优化的前置 |
| 4 | 控制机制的确定性（含 gRPC 取消） | P1 | 从软取消升级为硬控制，强制打断阻塞 LLM |
| 5 | 上下文渐进式披露（Progressive Disclosure） | P1 | 提质量省 token，改造有现成双路召回可复用 |
| 6 | Agent 评估 harness | P2 | 建立评测闭环，衡量 Agent+知识库组合质量 |

> 注：目标 2/3/6 与 [agent_platform_design.md](./agent_platform_design.md) 已有的「多租户 + 成本 + 观测」（Phase 3）演进行程呼应，Harness 视角是把它们统一编排、明确优先级。

---

## 2. 分项目标与实现步骤

### 目标一：验证循环（Verification Loops）— P0

- **现状**：工具调用结果与 LLM 输出直接采信，无结构化校验；失败只能靠整体重试。
- **目标**：在「工具调用返回」与「LLM 输出进入下一步」两个关口加校验闭环，失败时自动决定「重试工具 / 降级 / 中断」。
- **实现步骤**：
  1. 定义输出校验层：为工具参数与 LLM 输出引入 JSON Schema 校验，非法则标记为失败并触发决策。
  2. 工具调用失败降级：失败时把「工具返回错误」回传给 LLM 让其自我修正，而非拖垮整个任务（复用现有 `PublishToolEvent` 事件链路）。
  3. 校验失败重试策略：区分「可重试的工具错误」与「不可重试的输出错误」，分别走重试/置 `failed`。
- **验收**：注入一个返回错误结果的工具，任务能被探测、降级并回写正确状态，而非生成错误答案。

### 目标二：成本预算与熔断（Cost Envelope）— P0

- **现状**：`MaxRetry: 3` + 指数退避只能限重试次数，限不住「反复恶性循环」的累计调用与费用。
- **目标**：给每次任务设「模型调用次数 / 费用上限」，超限熔断置 `failed`，并记录用量。
- **实现步骤**：
  1. 在 Worker 处理循环中维护计数（调用次数、累计 TokensIn/Out）。
  2. 引入预算对象（上限阈值），每次调用前检查，超限即中止并回写失败原因。
  3. 结合 Agent 级 `model / api_key` 覆盖，按模型单价折算 `CostUSD` 写入观测记录（呼应 [agent_platform_design.md](./agent_platform_design.md) 的 Token 计量）。
- **验收**：人为拔掉模型可用性制造循环，任务在预算内熔断而非无限调 LLM。

### 目标三：可观测性（Trace + RunLog/ToolCall）— P0

- **现状**：NATS 工具事件已实时流式推送，但缺持久化的运行记录与链路追踪。
- **目标**：落地 `RunLog`（一次 Agent 运行完整记录）与 `ToolCallLog`（工具调用链），用 `trace_id` 贯穿 API → 队列 → Worker → LLM → 工具调用。
- **实现步骤**：
  1. 新增 `RunLog` / `ToolCallLog` 模型（字段参考 [agent_platform_design.md](./agent_platform_design.md) 第 4.4 节）。
  2. 生成 `trace_id` 并注入任务上下文，随 `PublishToolEvent` 传递，各环节写入观测表。
  3. 暴露 Prometheus 指标（任务耗时、成功率、重试率、成本），接入可观测面板。
- **验收**：能从一次提交追踪到每个工具调用、每次 LLM 调用的时序、Token 与成本。

### 目标四：控制机制的确定性（含 gRPC 取消）— P1

- **现状**：取消走「HTTP 直改状态 + 关键阶段重查」，是软取消；`GrpcServer.CancelTask` 已写入但**未注册、未监听、未被调用**。
- **目标**：补一条跨进程同步控制通道，能强制打断正在阻塞的 LLM 调用与工具执行，并把工具选择、输出格式变为确定性行为。
- **实现步骤**：
  1. 注册并监听 `worker.proto` 的 `CancelTask`（启动 gRPC Server，注册到 Worker 进程）。
  2. API 侧取消接口改为调用 gRPC，将「HTTP 直改」升级为「同步控制 + 状态回写」双通道。
  3. Worker 在关键阶段（消费前、LLM 返回后、工具调用中）接入强制取消信号。
  4. 为工具选择与输出格式添加确定性约束（如白名单强制、Schema 严格校验）。
- **验收**：发出取消请求能在阻塞中的 LLM 调用期间被强制中断；取消后的任务不被回写成 completed。

### 目标五：上下文渐进式披露（Progressive Disclosure）— P1

- **现状**：知识库上下文一次性被注入 systemPrompt，Token 成本随知识量线性增长。
- **目标**：改为「分层、按问题动态注入」——先注入命中片段/摘要，再按需扩展 RAG/KAG 关系层。
- **实现步骤**：
  1. 拆出「摘要层 / 命中层 / 关系层」三级上下文，按检索得分决定注入深度。
  2. 复用现有 RAG 向量检索 + KAG Text2Cypher 检索，仅调整注入策略与窗口管理。
  3. 对超长上下文做分片/裁剪，控制单次调用 Token 上限。
- **验收**：相同问题在知识量大时，上下文体积下降但回答质量不降（可用目标六评估）。

### 目标六：Agent 评估 harness（Eval）— P2

- **现状**：无自动化评测，Agent 质量靠人工判断。
- **目标**：建立一套「Agent + 知识库组合」的离线评测脚手架，衡量问答质量与工具使用正确率。
- **实现步骤**：
  1. 提供评测数据集（问题 + 期望答案/意图 + 期望工具调用）。
  2. 跑批量任务，汇总准确率、工具调用正确率、平均成本。
  3. 结果为「上下文演进 / 验证循环」等目标提供量化基线。
- **验收**：能对某 Agent+KB 组合产出可复现的质量报告，作为后续改动的回归护栏。

---

## 3. 分阶段落地路线

| 阶段 | 内容 | 说明 |
|---|---|---|
| **Phase 0（打底）** | 目标三 可观测性 + 目标一 验证循环 | 先看清、先防错，是一切优化的前置 |
| **Phase 1（止血）** | 目标二 成本熔断 + 目标四 gRPC 取消 | 解决线上最痛的两个风险 |
| **Phase 2（提效）** | 目标五 上下文渐进式披露 | 复用现有双路召回，低成本高收益 |
| **Phase 3（闭环）** | 目标六 评估 harness + 并入多租户/成本/观测 | 与 agent_platform_design 的 Phase 3 合流 |

---

## 4. 涉及代码落点

| 改动 | 位置 |
|---|---|
| 成本预算/熔断计数 | [worker 任务处理](file:///d:/Program%20Files/GoTaskAI/internal/worker/) |
| 可观测模型 | [model](file:///d:/Program%20Files/GoTaskAI/internal/model/)（新增 RunLog/ToolCallLog） |
| gRPC 取消接入 | [grpc_server.go](file:///d:/Program%20Files/GoTaskAI/internal/worker/grpc_server.go#L23-L50)、[worker.proto](file:///d:/Program%20Files/GoTaskAI/api/proto/worker/worker.proto) |
| 上下文注入策略 | Worker 组装 systemPrompt / RAG-KAG 检索处 |
| Agent 运行入口 | [TaskManager 处理链](file:///d:/Program%20Files/GoTaskAI/internal/queue/manager.go) |

---

## 5. 成功度量

- **可靠性**：Agent 任务失败中，可被验证循环探测、并给出明确失败原因的比例显著提升。
- **成本**：单任务出现「重试恶性循环」导致的超额调用被熔断拦截，平均单任务成本下降。
- **可控性**：取消任务可强制中断阻塞调用，不再可能把取消任务回写成 completed。
- **可观测**：任意一次任务可从 `trace_id` 完整还原每一步工具调用与 Token 用量。
- **质量**：通过评估 harness 给出可复现的 Agent 质量基线，作为后续改动回归护栏。
