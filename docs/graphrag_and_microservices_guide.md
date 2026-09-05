# GraphRAG 与微服务解耦架构原理解析

本文档专门用于记录 GoTaskAI 项目从单体应用升级为 **微服务架构 (Microservices)**，以及从单纯的 RAG 升级为 **GraphRAG (混合检索增强生成)** 的核心原理解析。

---

## 1. GraphRAG (KAG + RAG) 双路召回是怎么执行的？

在传统的 RAG 架构中，我们仅仅依赖 RedisVector 进行文本切片的相似度匹配。这在处理客观定义时很好用，但在面对复杂的逻辑推理（比如：“张三的孙子在哪个公司上班？”）时往往无能为力。
因此，我们引入了 **Neo4j 图数据库** 构建了 KAG (Knowledge-Augmented Generation)，并实现了双路召回。

### KAG 的两阶段工作流程：

#### 阶段一：知识图谱构建 (Ingestion)
当用户在“系统管理”页面粘贴长篇文本并点击“构建 KAG (Neo4j)”时：
1. **API 层接收**：`cmd/api/main.go` 接收请求并路由到 `api/rag.go` 中的 `KAGIngest` 方法。
2. **LLM 知识抽取**：系统调用 `kagManager.ExtractKnowledge()`。它会向大模型发送一段包含特定 Prompt 的请求，要求大模型阅读全文，并强行吐出 `[Source, Relation, Target]` 格式的 JSON 三元组数组。
   * *例如提取出：`[{"source": "张三", "relation": "FATHER_OF", "target": "李四"}]`*
3. **Cypher 入库**：拿到三元组后，调用 `kagManager.IngestGraph()`。它会将 JSON 数据转化为 Neo4j 的图查询语言 (Cypher) 语句：`MERGE (a:Entity {name: '张三'})-[r:FATHER_OF]->(b:Entity {name: '李四'})`，并在图数据库中持久化。

#### 阶段二：Text2Cypher 检索与双路增强 (Retrieval)
当用户在聊天框提问时，任务被 `cmd/worker/main.go` 消费，进入 `pool.go` 执行：
1. **KAG 图谱检索 (Text2Cypher)**：
   * Worker 调用 `kagManager.RetrieveGraphContext()`，它不会拿用户的问题去图数据库里直接搜，因为数据库听不懂自然语言。
   * 它会先求助大模型：“请把用户的这句话翻译成 Neo4j 的 Cypher 查询语句”。
   * 大模型返回类似于 `MATCH (a:Entity {name: '张三'})-[:FATHER_OF]->()-[:FATHER_OF]->(grandson) RETURN grandson.name` 的代码。
   * 系统拿着这句代码去查 Neo4j，瞬间得到精准的逻辑推导结果：“王五”。
2. **RAG 向量检索**：
   * 同步地，Worker 也会拿用户的问题去 RedisVector 里做传统的模糊相似度查询，捞出相关的文本片段。
3. **混合注入 (Hybrid Context Injection)**：
   * 最后，Worker 把从 Neo4j 查出的精确关系（王五），以及从 Redis 查出的长篇大论片段，统统塞进给大模型的最终 `System Prompt` 中。
   * 大模型根据这两路铁证，结合自身能力，生成最终的完美回答。

---

## 2. 微服务架构改造：API 与 Worker 是如何解耦的？

为了防止大模型计算把 CPU 跑满导致网页卡死，我们将项目从单体拆分成了两个物理上完全独立的进程：
*   **API Server (`cmd/api/main.go`)**：轻量级网关，只负责鉴权、限流、接客、发号施令。
*   **Worker Node (`cmd/worker/main.go`)**：计算节点，默默蹲守在后台，专门和 LLM、Neo4j 打交道干重活。

### 核心难点：拆分后，前端的 SSE 状态怎么实时更新？

在单体架构时，API 和 Worker 在同一个内存里，Worker 干完活直接调个函数，API 就能把进度推给前端。但现在它们变成了两个独立的 `.exe` 程序，它们之间怎么通信呢？

**解决方案：基于 Redis Pub/Sub 的跨进程事件总线**

1. **API Server 的监听**：
   在 `cmd/api` 启动时，`TaskManager` 开启了一个后台协程 `listenForGlobalUpdates()`。它像个接线员一样，死死盯住 Redis 里的一个专属频道 `global_task_updates`。
2. **Worker Node 的广播**：
   当 `cmd/worker` 里的某个任务跑到一半，或者彻底跑完把结果存进 MySQL 时，它会调用 `manager.UpdateTask()`。在这个方法里，Worker 会把最新的任务状态（包含大模型的最终回答）打包成 JSON，通过 `rdb.Publish()` 发射到 Redis 的那个频道里。
3. **API 接收并推送给前端**：
   API Server 的接线员立刻捕获到了这条消息，它一看：“哟，这是张三的任务更新了！” 于是它顺着自己内存里维护的 SSE 管道，把这个结果立刻推给了张三的浏览器。

这就是微服务中非常经典的**“事件驱动解耦 (Event-Driven Architecture)”**。API Server 完全不需要知道 Worker Node 是部署在北京的机房还是上海的机房，有 1 台还是 100 台，它们只通过 Redis 这个中间人进行解耦交流，实现了极致的高可用和可扩展性。