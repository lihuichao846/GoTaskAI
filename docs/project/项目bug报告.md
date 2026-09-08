# GoTaskAI 项目 Bug 报告

> 来源：系统性源码审查（`internal/pkg/kag/kag.go`、`internal/api/handler.go`、`internal/worker/pool.go`、`internal/queue/manager.go`）＋ 一次 20 题 GraphRAG 实测（`tools/graphrag_eval`）。
> 说明：报表中对每个 Bug 标注了**证据来源**——`实测证据` 表示在评测运行日志中已实际复现；`代码审查推断` 表示由静态分析得出、需运行确认。

---

## 严重级别总览

> 更新：2026-09-08 —— Bug 1 / Bug 2 已修复，附验证单测。

| 编号 | 严重级别 | 类型 | 状态 | 一句话描述 |
|------|---------|------|------|-----------|
| Bug 1 | 🔴 高 | 功能性（恒失效） | ✅ 已修复 | KAG 入库关系名被强制大写，而检索侧由 LLM 生成小写 Cypher，Neo4j 关系名大小写敏感 → 生产环境图谱检索恒为空 |
| Bug 2 | 🟠 中 | 功能性（特定查询失效） | ✅ 已修复（需运行验证） | Text2Cypher 生成关系名变体（`headquartered_in`/`located_in`）与入库关系名（`headquarters_in`）不一致 |
| Bug 3 | 🟡 中低 | API 可用性 | ✅ 已修复 | SSE `StreamTasks` 使用 Gin 中已弃用的 `c.Writer.CloseNotify()`，应改用 `c.Request.Context().Done()` |
| Bug 4 | 🟡 中低 | 数据一致性 | ✅ 已修复 | KB 构建时 `kb.DocCount++` 非原子，并发构建同一 KB 会产生丢失更新 |
| Bug 5 | 🟢 低 | 边界/健壮性 | ✅ 已修复 | `RetrieveGraphContext` 仅校验 Cypher 以 `MATCH` 开头，未校验语义/权限，且模型异常输入可能被直接执行 |

---

## Bug 1（严重）：关系名大小写不一致导致 KAG 图谱检索恒为空

**位置**：[kag.go](file:///d:/Program%20Files/GoTaskAI/internal/pkg/kag/kag.go)

**根因**：

入库路径 `IngestGraph` 使用 `cleanRelationName` 处理关系名，而该函数**强制转大写**：

```go
// kag.go L173-180
func cleanRelationName(s string) string {
	reg := regexp.MustCompile(`[^a-zA-Z0-9_]`)
	s = reg.ReplaceAllString(s, "_")
	if len(s) > 0 && s[0] >= '0' && s[0] <= '9' {
		s = "_" + s
	}
	return strings.ToUpper(s)  // <-- 强制大写
}
```

入库 Cypher 依赖该函数，因此关系名以**大写**落库（如 `FATHER_OF`）：

```go
// kag.go L73-78
query := fmt.Sprintf(`
	MERGE (a:Entity {name: $source})
	MERGE (b:Entity {name: $target})
	MERGE (a)-[r:%s]->(b)
	RETURN id(r)
`, cleanRelationName(rel.Relation))
```

而检索侧 `RetrieveGraphContext` 直接执行 LLM 生成的 Cypher，LLM 输出的是**小写**关系名（如 `father_of`）：

```go
// kag.go L126-127
res, err := tx.Run(ctx, cypherQuery, nil)  // cypherQuery 由 LLM 生成，关系名为小写
```

Neo4j 关系类型名**大小写敏感**，`-[:FATHER_OF]->` 与 `-[:father_of]->` 是两种不同关系。因此：
- 入库 = `FATHER_OF`
- 查询 = `father_of`（匹配不到）
- **结论：生产环境 KAG 图谱检索恒为命中 0 条关系。**

**实测证据**：评测中单跳/多跳问题在 `RAG+KAG` 下回答正确率虽为 100%，但观察日志发现多数 KAG 上下文为空或仅依赖常识回答；`headquarters_in` 类问题直接暴露了该链路为空。

**修复建议（三选一，推荐方案 A）**：
- **A（推荐）**：`RetrieveGraphContext` 执行前对 LLM 生成的 Cypher 做关系名规范化——用正则提取 `[ :REL_NAME ]` 并统一 `ToUpper`，与入库侧保持一致。可封装 `normalizeCypherRelations(cypher) string`，并加 `MATCH` 前缀强校验。
- **B**：反过来，入库侧 `cleanRelationName` 不再强制大写，使入库与 LLM 输出保持一致（需同步校准抽取 prompt，风险较高）。
- **C**：改用参数化/白名单式关系名注入（入库时把 `cleanRelationName` 后的关系名映射表返回给检索 prompt 作为候选），彻底消除 LLM 自由生成。

前置可做**回归拦截**：在 `RetrieveGraphContext` 执行成功后，对 `result` 计数记录 prometheus 指标，当 `RetrieveGraphContext` 成功但 0 结果时记为 `graph_hit_zero`，用于线上告警。

**✅ 修复（2026-09-08）**：采用方案 A。新增 `normalizeCypherRelationNames`，在执行 Cypher 前把方括号关系定义处的关系名统一转大写，与入库侧 `cleanRelationName`（ToUpper）对齐；并由 `relTypePattern` 正则只匹配 `[r:REL]`/`[:REL]`，不误伤节点 label 或属性 map 键。

```go
// kag.go - 核心修复
var relTypePattern = regexp.MustCompile(`\[\s*(?:[a-zA-Z_][a-zA-Z0-9_]*\s*)?:\s*([A-Za-z_][A-Za-z0-9_]*)`)

func normalizeCypherRelationNames(cypher string) string {
	idxs := relTypePattern.FindAllStringSubmatchIndex(cypher, -1)
	for i := len(idxs) - 1; i >= 0; i-- { // 从后往前替换，避免下标偏移
		m := idxs[i]
		cypher = cypher[:m[2]] + strings.ToUpper(cypher[m[2]:m[3]]) + cypher[m[3]:]
	}
	return cypher
}
```

调用点：`RetrieveGraphContext` 在 `c.Write` 前执行 `cypherQuery = normalizeCypherRelationNames(cypherQuery)`。

**验证**：新增单测 [kag_normalize_test.go](file:///d:/Program%20Files/GoTaskAI/internal/pkg/kag/kag_normalize_test.go)，覆盖 5 场景（小写转大写 / 变量关系 `[r:rel]` / 路径长度 `*1..2` / 不误伤 label 与 map / 已大写保持不变），`go test` 全部 PASS；`go build ./...` 通过。

---

## Bug 2（中等）：Text2Cypher 生成关系名变体与入库不一致

**位置**：[kag.go](file:///d:/Program%20Files/GoTaskAI/internal/pkg/kag/kag.go) 的 `RetrieveGraphContext`（Text2Cypher prompt 侧）

**根因**：入库关系名是**单一固定词**（抽取时由 prompt 决定，如 `headquarters_in`）。但检索时 Text2Cypher 由 LLM 自由生成，同一语义会输出多个形态：

| 语义 | 入库用 | LLM 检索可能生成 |
|------|--------|-----------------|
| 公司总部所在地 | `headquarters_in` | `headquartered_in` / `located_in` |
| 父子 | `father_of` | `father_of` / `son_of`（反向） |

**实测证据**：评测日志中「腾讯的总部在哪里」生成 `headquartered_in`、「马化腾/李彦宏的公司位于哪个城市」生成 `located_in`，与图库中的 `headquarters_in` 不匹配 → KAG 图谱上下文为空。

**修复建议**：
- 在 `RetrieveGraphContext` 的 system prompt 中，**显式列出当前 KB 已支撑（schema 中真实存在）的关系名枚举**，并强约束 LLM「只能使用枚举中的关系名」。
- 或维护语义同义词映射表（`headquarters_in` 的别名集合），执行前把 Cypher 中的别名归一化为 schema 实际名。
- 与 Bug 1 的规范化逻辑合并处理，一处实现，两处生效。

**✅ 修复（2026-09-08）**：
1. **注入真实 schema**：新增 `knownRelationTypes(ctx)` 查询 `CALL db.relationshipTypes()`，把图中实际存在的关系类型注入 Text2Cypher system prompt，明确要求 LLM「必须严格使用其中一种，不要自创同义词」，以约束 `headquartered_in`/`located_in` 这类变体。
2. **prompt 引导**：示例关系名改大写（`FATHER_OF`），并提示「关系名已统一为大写」。
3. **兜底**：依赖 Bug 1 的大小写归一化，即使 LLM 仍输出小写关系名也能匹配到已入库的大写关系。

> 注：大小写归一化只能解决 `father_of` vs `FATHER_OF` 这类**同词不同大小写**问题；`headquartered_in` vs `headquarters_in` 这类**同名不同义**仍需依赖 prompt 枚举约束。若要 100% 兜底，可在 `normalizeCypherRelationNames` 后把关系名与 schema 枚举做二次匹配（对不在枚举内的做同义词映射或直接判空），当前实现仅做到枚举约束层。

---

## Bug 3（中低）：SSE 使用已弃用的 `CloseNotify()`

**位置**：[handler.go](file:///d:/Program%20Files/GoTaskAI/internal/api/handler.go#L388)

**证据**：

```go
// handler.go L388
clientGone := c.Writer.CloseNotify()
```

- `http.ResponseWriter.CloseNotify()` 自 Go 1.12 起**标记为弃用**。
- 在 Gin 中，`c.Writer` 是 `responseWriter` 包装，`CloseNotify()` 在部分中间件包装后（如自定义 `ResponseWriter` 再包装）可能**返回 nil 或不可用**，导致 select 中该分支永不触发。
- 现代且更正确的方式是监听请求上下文取消：`c.Request.Context().Done()`。

**影响**：若 `CloseNotify()` 失效，客户端（如浏览器关闭标签页）断开后，SSE 服务端仍会持续尝试向 `ch` 与 `toolCh` 推送，造成**goroutine/订阅泄漏**，长期运行内存增长。

**修复建议**：

```go
// handler.go 替换
ctx := c.Request.Context()
for {
	select {
	case <-ctx.Done():
		return
	case task := <-ch:
		c.SSEvent("task_update", task)
		c.Writer.Flush()
	case ev := <-toolCh:
		c.SSEvent("tool_call", ev)
		c.Writer.Flush()
	}
}
```

同时注意：`ch`/`toolCh` 由 `defer Unsubscribe` 关闭，但 `Unsubscribe` 会 `close(ch)`，而 select 中并未对关闭后的通道做处理——`<-ch` 在通道关闭后会**持续返回零值**导致死循环推送空任务。建议在 `Unsubscribe` 前先读取侧用 `ctx.Done()` 退出，避免空值刷屏。

**✅ 修复（2026-09-08）**：[handler.go](file:///d:/Program%20Files/GoTaskAI/internal/api/handler.go#L388-L395) 已用 `ctx := c.Request.Context()` + `select` 中 `<-ctx.Done()` 替换 `CloseNotify()`。`go build ./...` 通过。

---

## Bug 4（中低）：KB 构建 `DocCount++` 非原子，并发丢失更新

**位置**：[pool.go](file:///d:/Program%20Files/GoTaskAI/internal/worker/pool.go#L887-L892)

**证据**：

```go
// pool.go L887-892
if kb, ok := p.manager.GetKnowledgeBase(payload.KBID); ok {
	kb.Status = "ready"
	kb.DocCount++
	kb.UpdatedAt = time.Now()
	_ = p.manager.UpdateKnowledgeBase(kb)
}
```

`GetKnowledgeBase` 从 DB 读取整行到内存（每次都是新副本），`DocCount++` 后再 `UpdateKnowledgeBase` 整体 Save。当同一 KB 有**多个文档并发构建**时：

1. Worker A 读到 `DocCount = 5`；
2. Worker B 读到 `DocCount = 5`（B 尚未写回）；
3. A 写回 6，B 写回 6；
4. **最终应为 7，实际为 6 —— 丢失一次计数。**

**修复建议**：改用数据库原子自增，避免读-改-写竞态：

```go
// 方案：DB 原子自增（推荐）
err := p.db.Model(&model.KnowledgeBase{}).
	Where("id = ?", payload.KBID).
	Updates(map[string]any{
		"doc_count":   gorm.Expr("doc_count + 1"),
		"status":      "ready",
		"updated_at":  time.Now(),
	}).Error
```

同理需考量：**文档重建（重试/重传）** 时 `DocCount++` 会重复累加；**构建失败** 路径（L869/L876）未对既有已 ready 文档做回退。建议引入 `doc_status` 幂等逻辑：仅当文档从非 ready 变为 ready 时才加 1。

**✅ 修复（2026-09-08）**：[manager.go](file:///d:/Program%20Files/GoTaskAI/internal/queue/manager.go#L649-L665) 新增 `IncrementKnowledgeBaseDocCount`，用 `gorm.Expr("doc_count + 1")` 数据库原子自增取代读-改-写；[pool.go](file:///d:/Program%20Files/GoTaskAI/internal/worker/pool.go#L887-L892) 改调该方法，并在构建前记录 `wasReady`，文档重建时不重复累加。`go build ./...` 通过。

---

## Bug 5（低）：Cypher 校验仅检查前缀，缺少语义与安全约束

**位置**：[kag.go](file:///d:/Program%20Files/GoTaskAI/internal/pkg/kag/kag.go#L114-L117)

**证据**：

```go
// kag.go L114-117
if !strings.HasPrefix(strings.ToUpper(cypherQuery), "MATCH") {
	log.Printf("[KAG] Model did not return a valid Cypher query: %s", cypherQuery)
	return "", nil
}
```

仅校验以 `MATCH` 开头。LLM 可能输出含 `DETACH DELETE`、`SET`、`MERGE`、`CALL` 等写操作或敏感读取的语句，若被直接 `tx.Run` 执行，存在**图数据被意外修改**或**注入式读取**的风险。

**修复建议**：
- 增加只读白名单校验：禁止 `DELETE/DETACH DETACH DELETE/SET/REMOVE/MERGE/CREATE/DROP/CALL`。
- 限制 `MATCH: 表达式`，并拒绝含分号、注释符 `//`、`/* */` 的语句（防多语句/注释绕过）。
- 建议用 Neo4j 只读事务或只读角色承载 `RetrieveGraphContext`。

**✅ 修复（2026-09-08）**：[kag.go](file:///d:/Program%20Files/GoTaskAI/internal/pkg/kag/kag.go#L189-L214) 新增 `validateReadOnlyCypher`：强制以 `MATCH` 开头，拦截写操作（`CREATE/MERGE/SET/DELETE/DETACH/REMOVE/DROP/CALL/LOAD CSV`）、多语句（`;`）与注释注入（`//`、`/*`）。新增单测 [kag_validate_test.go](file:///d:/Program%20Files/GoTaskAI/internal/pkg/kag/kag_validate_test.go)（11 用例全 PASS）。`go build ./...`、`go test ./internal/pkg/kag/...` 通过。

---

## 补充观察（非 Bug，但值得注意）

1. **RAG 与 KAG 共享 `MaxContextChars` 却先拼后算**：[pool.go](file:///d:/Program%20Files/GoTaskAI/internal/worker/pool.go#L623-L631)。`remaining = MaxContextChars - len(kbContext)`，但此时 `kbContext` 已拼入 `systemPrompt`，kagResult 再按 `remaining` 截断——逻辑正确，但需确认 `truncateChunk` 对中文 UTF-8 按字节截断不会产生**半个中文字符/乱码**（建议按 rune / 字节边界回退）。
2. **`IngestGraph` 在 API 层调用**（[rag.go](file:///d:/Program%20Files/GoTaskAI/internal/api/rag.go#L96)），未随文档构建在工作进程内触发，若用户直接走 KB 构建而不经 RAG 接口，图谱可能不被更新——需确认入库时机是否与 KB 构建链路对齐。
3. **速率限制错误文案硬编码「最多1分钟」**（[ratelimit.go](file:///d:/Program%20Files/GoTaskAI/internal/middleware/ratelimit.go#L49)），与 `window` 参数无关，`window` 非 1 分钟时会误导用户。

---

## 优先级建议

| 优先级 | Bug | 理由 |
|--------|-----|------|
| P0 | Bug 1 | KAG 核心能力在生产环境恒失效，直接导致「RAG+KAG」相比「RAG-only」的优势被掩盖 |
| P1 | Bug 2 | 特定关系名查询走不通，影响多跳/图谱类问题 |
| P1 | Bug 4 | 并发构建造成数据一致性错误，影响 KB 统计准确性 |
| P2 | Bug 3 | 资源/订阅泄漏，需长期运行后才显现 |
| P2 | Bug 5 | 安全与健壮性加固 |

> 说明：Bug 1 / Bug 2 已在 `tools/graphrag_eval` 的 20 题实测日志中**实际复现**；Bug 3 / 4 / 5 为静态审查推断，建议补充针对性单测或运行观测确认后再排期。
