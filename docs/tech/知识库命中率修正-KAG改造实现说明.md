# 知识库命中率修正 —— KAG 改造实现说明

> **记录时间**：2026-09-09 03:57:58 +08:00（Asia/Shanghai）
> **版本**：v1.0（对应阶段四试点验证后的首次代码落地）
> **关联方案**：`docs/知识库修正技术方案（终稿-阶段四试点验证）.md`
> **面向读者**：希望看懂「为什么要改、改了什么、每段代码在做什么」的维护者。若读完仍有疑问，可随时追问。

---

## 0. 一句话总结

改造前，KAG 图谱检索走的是「**LLM 一步生成一条 Cypher 精确匹配**」，在开放域图谱里几乎必然因「关系名对不上、多跳中途断链、终点实体没建图」而**返回空**，导致命中率低。

这次改造把 KAG 从「归一化」路线切换到终稿方案的三大修正重心：

> **本体引导抽取 + 三元组补全 + 多跳松弛检索**（而非归一化）

即：抽取时用「关系白名单」约束、入库前补「图里缺的事实」、检索时改成「定位起点 → 逐跳展开 → 找不到就降级返回证据链」，从根上减少「空返回」。

---

## 1. 背景：命中率为什么低（试点实测结论）

阶段四小范围试点（`tools/wikihop_pilot`，Neo4j-only、确定性可复现）对 WikiHop 20 样本遗留图谱（1863 节点 / 2338 关系）做了实测，得出几个**修正性的关键结论**：

| 发现 | 数据 | 结论 |
|------|------|------|
| 关系类型数 | **744 种**（远高于先前估测的 350+） | 关系 schema 爆炸 |
| 长尾（仅出现 1 次）占比 | **58%** | 大量关系是语义不同的动词 |
| 字符串归一化收敛收益 | 744 → 742（**约 0.3%**） | **事后归一化对收敛几乎无效** |
| 实体名大小写/空白碰撞 | 仅占节点 **1.3%** | 并非主导根因 |

据此，真正的根因按影响排序是：

1. **抽取覆盖/知识补全不足（主导）**：`ExtractKnowledge` 无本体约束、无证据校验，开放域事实稀疏、大量主体/三元组缺失。
2. **关系 schema 爆炸（744 种、58% 单例）**：Text2Cypher 很难生成与已入库 744 种之一**精确一致**的关系名 → 检索恒空。
3. **多跳深度不足**：检索停在中转层或因终点缺失返回空，无松弛/降级策略。
4. **实体名大小写/空白碰撞（次要，1.3%）**：真实但影响小，作为低成本卫生项保留。

> 关键转折：原先「把实体名大小写归一化」被当作主根因，**实测证伪**。真正失败在「事实/关系缺失」「终点缺失」「主体未抽取」，而非名字对不上。

---

## 2. 改造前的问题（旧代码为什么失败）

改造前，核心逻辑集中在 `internal/pkg/kag/kag.go` 三个函数：

```go
// 1) 抽取：完全开放，无任何关系约束
func (k *KAGManager) ExtractKnowledge(ctx context.Context, text string) ([]EntityRelation, error)

// 2) 入库：只做 cleanRelationName（转大写 + 去非法字符）
func (k *KAGManager) IngestGraph(ctx context.Context, relations []EntityRelation) error

// 3) 检索：LLM 生成单条 Cypher 精确匹配
func (k *KAGManager) RetrieveGraphContext(ctx context.Context, question string, topN int) (string, error)
```

三个致命问题：

1. **抽取侧**：prompt 只提示「关系名尽量简短、动词化」，LLM 可以自由发挥任意关系名 → 图里积累出 744 种、58% 单例。
2. **检索侧**：`RetrieveGraphContext` 让 LLM 直接生成 Cypher，再靠 `normalizeCypherRelationNames` 把关系名统一大写。但大写归一化解决不了「`directed_by` vs `music_composed_by`」这类**语义不同**的关系，也解决不了「LLM 生成了库里根本没有的关系名」。
3. **空返回无兜底**：一旦实体不在图内、或终点缺失、或停在中间层，直接返回空字符串，图谱上下文从不注入。

一句话：**旧方案把希望全押在「名字归一化 + LLM 猜对 Cypher」上，而这两个在开放域都不可靠。**

---

## 3. 修正重心与整体设计

三大支柱，分别对应三大根因：

| 支柱 | 解决什么根因 | 落点 |
|------|------------|------|
| **本体引导抽取** | 关系 schema 爆炸（744 种） | `ExtractKnowledge` 注入白名单 + 抽取后归一到规范关系 |
| **三元组补全** | 事实缺失（主导根因） | 新增 `GapMine` / `CompleteGraph` |
| **多跳松弛检索** | 多跳停中间层 / 终点缺失空返回 | 重写 `RetrieveGraphContext` |

整体流程图：

```
文档/问题
   │
   ├─ 抽取期：ExtractKnowledge（白名单约束 + recall 导向）→ CanonicalizeRelation 归一
   │           └─ 补全期：GapMine 找「文档有、图里无」→ CompleteGraph 补建
   │
   └─ 检索期：RetrieveGraphContext
               └─ 起点定位 → 逐跳 BFS 展开 → 有事实输出证据链 / 无事实输出缺失提示
```

---

## 4. 改动文件清单

| 文件 | 操作 | 说明 |
|------|------|------|
| `internal/pkg/kag/ontology.go` | **新增** | 关系本体：白名单 + 语义等价类 + 规范化 |
| `internal/pkg/kag/kag.go` | 改造 | `KAGManager` 增加本体字段；`ExtractKnowledge` 改本体引导；`IngestGraph` 加空值守卫 |
| `internal/pkg/kag/complete.go` | **新增** | 三元组补全 `GapMine` / `CompleteGraph` |
| `internal/pkg/kag/retrieve.go` | **新增** | 多跳松弛检索（重写 `RetrieveGraphContext`） |
| `internal/pkg/kag/ontology_test.go` | **新增** | 本体规范化、降级提示、去重等单元测试 |
| `internal/pkg/kag/kag_normalize_test.go` | 删除 | 原 `normalizeCypherRelationNames` 测试（函数已删） |
| `internal/pkg/kag/kag_validate_test.go` | 删除 | 原 `validateReadOnlyCypher` 测试（函数已删） |

> 对外签名 `NewKAGManager`、`ExtractKnowledge`、`IngestGraph`、`RetrieveGraphContext` **全部保持不变**，因此 `api/rag.go`、`worker/pool.go`、各评测工具无需改动。

---

## 5. 逐文件详解

### 5.1 ontology.go —— 关系本体

这是「本体引导抽取」的基础。核心是 `RelationOntology` 结构体：

```go
type RelationOntology struct {
    Canonical []string            // 白名单规范关系名（统一大写）
    Alias     map[string]string   // 语义等价类：变体 -> 规范名
}
```

- **`Canonical`**：允许 LLM 产出的关系白名单（如 `LOCATED_IN`、`DIRECTED_BY`、`FATHER_OF`…）。这是把 744 种收敛到 ≤120 种的**唯一有效手段**（试点已证字符串归一化做不到）。
- **`Alias`**：语义等价类，把「同一个意思的不同写法」归一到规范名，例如：

```go
Alias: map[string]string{
    "HEADQUARTERED_IN": "LOCATED_IN",
    "HEADQUARTER_IN":   "LOCATED_IN",
    "OFFICE_CONTESTED": "CONTESTED_OFFICE",
    "PARTICIPATED_IN":  "PARTICIPATES_IN",
    // ...
}
```

关键方法 `CanonicalizeRelation` 做了三件事：

```go
func (o *RelationOntology) CanonicalizeRelation(rel string) string {
    norm := cleanRelationName(rel)        // 1. 非字母数字→下划线 + 转大写
    if o == nil { return norm }           //    本体为空时退化为纯归一化
    if canon, ok := o.Alias[norm]; ok {   // 2. 命中等价类 → 映射到规范名
        return canon
    }
    return norm                            // 3. 不在白名单内则保留原样
}
```

> `DefaultRelationOntology()` 是内置的**经验起步集**，覆盖空间归属、影视作品、人物机构、人物属性、亲属社会、事件参与等常见关系。`≤120` 是经验目标，最终需要结合业务本体在 T0 校准。

---

### 5.2 kag.go —— 抽取改造（本体引导抽取）

`KAGManager` 增加了一个字段：

```go
type KAGManager struct {
    neo4jDriver neo4j.DriverWithContext
    llmClient   *llm.Client
    ontology    *RelationOntology // 关系本体
}

func NewKAGManager(driver neo4j.DriverWithContext, llmClient *llm.Client) *KAGManager {
    return NewKAGManagerWithOntology(driver, llmClient, DefaultRelationOntology())
}
```

`ExtractKnowledge` 的两个关键改动：

**改动 1：注入白名单约束**

```go
if hint := k.ontology.whitelistHint(); hint != "" {
    systemPrompt += fmt.Sprintf("\n关系名称必须仅从以下规范关系名中选择（保持大写，不要自创同义词）：\n%s", hint)
}
```

这样 LLM 在抽取时就被限制在规范关系内，**从源头**避免再产生 744 种关系。

**改动 2：抽取结果归一**

```go
for i := range relations {
    relations[i].Relation = k.ontology.CanonicalizeRelation(relations[i].Relation)
}
```

即使 LLM 偶尔产出变体（如 `headquartered_in`），也会被归一到规范名（`LOCATED_IN`）。

另外，`IngestGraph` 增加了一个空值守卫，跳过源/终点/关系为空的脏三元组，避免生成非法 Cypher：

```go
if strings.TrimSpace(rel.Source) == "" || strings.TrimSpace(rel.Target) == "" || strings.TrimSpace(rel.Relation) == "" {
    continue
}
```

---

### 5.3 complete.go —— 三元组补全

这是针对**主导根因（事实缺失）**的补全模块。

```go
// GapMine 找「文档有、图里无」的缺失三元组
func (k *KAGManager) GapMine(ctx context.Context, text string) ([]EntityRelation, error) {
    rels, err := k.ExtractKnowledge(ctx, text)   // 1. 抽取
    if err != nil { return nil, err }

    missing := make([]EntityRelation, 0, len(rels))
    for _, r := range rels {
        exists, err := k.tripleExists(ctx, r)    // 2. 逐个查图是否存在
        if err != nil { return nil, err }
        if !exists { missing = append(missing, r) } // 3. 收集缺失
    }
    return missing, nil
}

// CompleteGraph 补全图谱：挖缺失 + 直接补建
func (k *KAGManager) CompleteGraph(ctx context.Context, text string) (int, error) {
    missing, err := k.GapMine(ctx, text)
    if err != nil { return 0, err }
    if len(missing) == 0 { return 0, nil }
    if err := k.IngestGraph(ctx, missing); err != nil { return 0, err }
    return len(missing), nil
}
```

`tripleExists` 判断某个三元组 `source -[relation]-> target` 是否已在图里，起点/终点都用 `toLower(trim(name))` 做规范化匹配，避免大小写/空白导致的误判缺失。

> 对应试点里的三个失败样本：`Solva Sawan` 缺 `original language` 关系、`Upper Bicutan National High School` 缺终点 `capital region`、`danish general election` 未建图——都可交给补全模块定向补齐。

---

### 5.4 retrieve.go —— 多跳松弛检索（最核心）

重写的 `RetrieveGraphContext` 由「单条 Cypher 精确匹配」升级为四步：

```go
func (k *KAGManager) RetrieveGraphContext(ctx context.Context, question string, topN int) (string, error) {
    // 1. 起点定位
    starts, err := k.locateStartEntities(ctx, question)
    if len(starts) == 0 { return "", nil }  // 起点不在图内 → 覆盖缺口

    // 2. 逐跳 BFS 展开
    facts := k.expandNeighbors(ctx, starts, defaultMaxHops, topN)

    // 3. 组装（有事实输出证据链 / 无事实输出缺失提示）
    return formatGraphContext(starts, facts), nil
}
```

**第 1 步 起点定位**：先用 LLM 从问题里抽实体提及，再到图里做「精确 + 归一化」匹配；LLM 抽取失败时退化为「图里名称是问题子串」的确定性匹配。

**第 2 步 逐跳 BFS**：这是替代 Text2Cypher 的关键。不再是「让 LLM 猜一条 Cypher」，而是从起点出发、确定性一层层展开邻接事实：

```go
for hop := 1; hop <= maxHops; hop++ {
    for _, name := range frontier {
        for _, nb := range k.queryNeighbors(ctx, name) {
            src, dst := name, nb.Target
            if nb.Incoming { src, dst = nb.Target, name }  // 校正入边方向
            facts = append(facts, graphFact{Source: src, Rel: k.ontology.CanonicalizeRelation(nb.Rel), Target: dst, Hop: hop})
            if topN > 0 && len(facts) >= topN { return facts } // 置信度截断
            // ...
        }
    }
    // ...
}
```

- 每条事实都带上 `Hop`（跳数），用于**标注层级**，让中间层实体作为证据链而不是终答。
- `defaultMaxHops = 3`，适度加深跳数以覆盖多跳问答，同时用 `topN` 截断控制注入体积。

**第 3 步 降级（不再空返回）**：

```go
func formatGraphContext(starts []string, facts []graphFact) string {
    if len(facts) == 0 {
        return fmt.Sprintf("[缺失提示] 起点实体「%s」已建图，但未发现可达邻接关系（可能为抽取覆盖不足导致事实缺失）。", strings.Join(starts, "、"))
    }
    // 有事实则逐条输出： Source -[REL]-> Target (hop=N)
    ...
}
```

> 这直接对应试点里 `upper bicutan` 的场景：即使终点 `capital region` 不在图内，也能返回已探明的最近层（如 `Taguig City`）作为证据链，而不是空。

**顺带的安全收益**：新方案不再执行 LLM 生成的 Cypher（所有 Cypher 都是我们自己写的、参数化查询），从根上消除了原来的 Cypher 注入风险。

---

## 6. 验证方式

已在本地验证通过：

```bash
go build ./...                      # 全量编译通过（含所有 tools）
go test ./internal/pkg/kag/...      # 单元测试通过
gofmt -l internal/pkg/kag           # 无格式问题
go vet ./internal/pkg/kag/...       # 无 vet 告警
```

单元测试覆盖：
- `CanonicalizeRelation` 的大小写折叠、等价类映射、非白名单保留、数字开头前缀、nil 本体兜底；
- `whitelistHint` 的拼接与空值；
- `formatGraphContext` 的证据链层级输出与「缺失提示」降级；
- `dedupeStrings` 保序去重。

---

## 7. 兼容性说明

- 公开签名全部不变，`api/rag.go`、`worker/pool.go`、`tools/wikihop_eval`、`tools/graphrag_eval`、`tools/wikihop_diag` 无需改动。
- 各评测工具用 `strings.Contains` 判定命中，新证据链格式 `Source -[REL]-> Target (hop=N)` 包含目标实体名，判定逻辑兼容。

---

## 8. 遗留 / 待办（诚实声明）

1. **白名单是经验起步集**：`≤120` 硬门禁需结合业务本体在 T0 校准；`relation_schema` 嵌入集合（Milvus 相似度注入白名单）尚未落地。
2. **修正后端到端重测未执行**：本次是确定性代码改造，「修正后 20 样本准确率」需在真实 LLM + Neo4j 环境重跑 `wikihop_eval`。
3. **校验闸门 VerifyTriple / HITL 人工审核**：终稿中的「LLM 证据校验 + 人工兜底」尚未实现，属 T0/T1 后续项。
4. **MedHop 未试点**：实体为掩码 ID，需在建图前置下评测。
5. **成本/延迟未实测**：新增的意图解析 LLM 调用与原 Text2Cypher 调用**数量持平**，但增量 token 需在实现后回归，纳入 `llm.Budget` 计量。

---

## 9. 代码位置速查

| 内容 | 文件 |
|------|------|
| 关系本体 + 默认白名单 + 规范化 | `internal/pkg/kag/ontology.go` |
| 本体引导抽取 + 建图 | `internal/pkg/kag/kag.go` |
| 三元组补全 GapMine / CompleteGraph | `internal/pkg/kag/complete.go` |
| 多跳松弛检索 RetrieveGraphContext | `internal/pkg/kag/retrieve.go` |
| 单元测试 | `internal/pkg/kag/ontology_test.go` |
| 关联方案文档 | `docs/知识库修正技术方案（终稿-阶段四试点验证）.md` |

---

## 10. 版本记录

| 版本 | 时间 | 说明 |
|------|------|------|
| v1.0 | 2026-09-09 03:57:58 +08:00 | 首次落地：本体引导抽取 + 三元组补全 + 多跳松弛检索 |
