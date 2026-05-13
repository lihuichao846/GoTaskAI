# RAG 架构与核心流程解析 (Retrieval-Augmented Generation)

在 GoTaskAI 项目中，我们利用 RAG（检索增强生成）技术，让原本只有“通用知识”的大语言模型（LLM）能够回答基于“项目私有知识库”的专业问题。

为了实现这一目标，我们引入了业界标准的框架和工具链。本文档将详细拆解当前系统中 RAG 的核心工作流。

---

## 🛠️ 一、 核心技术栈选型

*   **业务编排框架**：`LangChainGo` (`github.com/tmc/langchaingo`)
    *   **作用**：它是一个标准的 LLM 应用开发框架。我们利用它提供的抽象层（如 TextSplitter 文本切分、Document 加载、VectorStore 存储等）来统一不同底层组件的调用方式。
*   **向量数据库 (Vector Store)**：`Redis Stack` (`RedisVector`)
    *   **作用**：基于原有的 Redis 服务，启用了 RediSearch/RedisVector 模块。用于高效存储文本块及其对应的数学向量，并支持余弦相似度（Cosine Similarity）等近邻检索操作。
*   **向量化模型 (Embeddings)**：`Ollama` 驱动的 `bge-m3`（或其他文本嵌入模型）
    *   **作用**：将人类可读的文本段落，转换为机器可计算的高维浮点数数组（向量）。语义越相近的文本，其向量在多维空间中的距离越近。
*   **生成模型 (LLM)**：`Ollama` 驱动的大模型（如 `qwen` / `llama3`）
    *   **作用**：接收“用户问题”和“检索出的相关知识”，进行最终的阅读理解和回答生成。

---

## 🔄 二、 核心流程拆解

RAG 系统的生命周期可以严格划分为两个截然不同的阶段：**数据入库阶段 (Ingestion)** 和 **检索与生成阶段 (Retrieval & Generation)**。

### 阶段一：数据入库 (Data Ingestion)

这个阶段发生在用户**上传文档/构建知识库**时。目标是把长篇大论的文件，变成数据库里可检索的“知识碎片”。

1.  **文档接收**：
    *   前端通过 API 将文件内容（如 `.md`, `.txt`）发送给后端。
2.  **文本切分 (Chunking)**：
    *   使用 `LangChainGo` 的 `RecursiveCharacterTextSplitter`（递归字符切分器）。
    *   **原理**：大模型无法一次性吃下整本书，我们必须把长文档切成小块（如每块 500 字，且块与块之间保留 50 字的重叠内容以防语境截断）。
3.  **向量化 (Embedding)**：
    *   把切好的每一个文本块，发给 Ollama 的 Embedding 模型。
    *   模型会返回一个高维向量（例如 1024 维的浮点数数组）。
4.  **向量入库 (Storage)**：
    *   将 `[文本块内容]` + `[向量数据]` + `[元数据(如文件名)]` 作为一个整体，写入 `Redis Stack` 的向量索引中。

### 阶段二：检索与生成 (Retrieval & Generation)

这个阶段发生在用户**在聊天框提问**时。目标是带着用户的疑问，去资料库里“翻书”，然后让 AI 总结答案。

1.  **用户提问向量化**：
    *   用户输入问题（如：“本项目用什么做消息队列？”）。
    *   系统立即将这个**问题**发给 Embedding 模型，转换成一个“查询向量”。
2.  **相似度检索 (Similarity Search)**：
    *   系统拿着“查询向量”，去 `Redis Stack` 里进行 K-NN（K近邻）搜索。
    *   Redis 会计算库中所有文本块向量与查询向量的距离，返回最相似的前 `Top K`（如前 3 个）文本块。这些就是与问题最相关的**背景知识**。
3.  **Prompt 动态拼接 (Context Injection)**：
    *   这是 RAG 的核心魔法。系统并不是直接把问题丢给大模型，而是偷偷在后台修改了系统提示词（System Prompt）。
    *   将检索到的 3 个文本块拼接到 Prompt 中，通常使用 XML 标签包裹（如 `<knowledge_base_context>`）。
    *   **最终发给大模型的 Prompt 结构类似**：
        ```text
        你是一个智能助手。请根据以下参考资料回答用户问题。如果资料中没有答案，请直接说明。
        [参考资料]：
        <knowledge_base_context>
        片段1: ...项目中使用了 Asynq 作为消息队列...
        片段2: ...Redis Stack 也承担了队列存储...
        </knowledge_base_context>
        
        用户问题：本项目用什么做消息队列？
        ```
4.  **大模型生成 (Generation)**：
    *   LLM 阅读了包含私有知识的 Prompt，开始“照本宣科”，流式（SSE）返回最终的精准答案。

---

## 🗺️ 三、 核心代码映射

如果你想查看具体的实现细节，可以顺着以下路径在源码中寻找：

*   **数据入库 API**：[internal/api/rag.go](file:///d:/Program%20Files/GoTaskAI/internal/api/rag.go)
    *   关注 `Ingest` 函数。这里实现了文本接收、`RecursiveCharacterTextSplitter` 实例化、以及调用 `redisvector.AddDocuments()` 存入 Redis。
*   **检索与生成流**：[internal/worker/pool.go](file:///d:/Program%20Files/GoTaskAI/internal/worker/pool.go)
    *   关注 `handleTaskProcess` 方法。
    *   这里在调用大模型之前，通过 `p.ragStore.SimilaritySearch(ctx, t.Payload, 3)` 检索了前 3 条相关文档。
    *   随后，使用 `fmt.Sprintf` 将结果包装入 `<knowledge_base_context>` 标签，合并到了系统提示词 `systemPrompt` 中。
*   **CLI 调试工具**：[tools/rag_ingest/](file:///d:/Program%20Files/GoTaskAI/tools/rag_ingest/)
    *   这里的 `ingest/main.go` 和 `search/main.go` 是独立的脚本，非常适合脱离 Web 环境单独测试切分和检索的逻辑。

---

## 💡 四、 架构总结与展望：Redis 向量数据库的局限与演进

目前的 **LangChainGo + Redis Stack** 方案极大地简化了早期粗糙的实现。LangChainGo 提供的统一抽象（如 `Document` 和 `VectorStore` 接口）为我们带来了极好的扩展性。

很多同学会问：“**这个向量数据库的存储一直存放在 Redis 中吗？它适合长期用吗？**”

答案是：**适合前期快速跑通 MVP（最小可行性产品），但不适合后期的海量数据生产环境。**

### 1. 为什么初期选择 Redis Stack？
*   **架构复用**：因为我们在做高并发缓存、API 限流、Asynq 队列时，系统里已经必须存在一个 Redis 了。开启 Redis Stack 的向量模块，**不需要额外部署新的数据库组件**，对新手和本地开发极其友好。
*   **检索极快**：毕竟是纯内存操作，加上 HNSW（分层导航小世界）算法索引，在几十万级别的文本块下，检索速度是毫秒级的。

### 2. Redis 做向量库的致命痛点（为什么不能一直用？）
*   **内存成本太高（贵！）**：向量数据（特别是 1024 维以上的浮点数数组）体积非常庞大。100 万条普通的文本，存进 MySQL 也就几十兆；但如果是 100 万条高维向量存进 Redis，可能直接吃掉几个 GB 的纯内存！在云服务器上，内存是最昂贵的资源。
*   **虽然能持久化，但恢复慢**：Redis 确实可以通过 RDB/AOF 机制把数据持久化到硬盘（停电不会丢），但每次重启 Redis 时，它必须把**所有的**向量数据重新加载回内存才能提供服务。如果向量数据有几十个 G，重启一次可能要等很久。

### 3. 未来的演进方向（如何替换？）
真正的企业级 RAG 架构，通常会采用**基于磁盘（Mmap 技术）**或**混合存储**的专业向量数据库，比如 **Qdrant**、**Milvus** 或 **Chroma**。它们可以把庞大的向量数据放在便宜的 SSD 硬盘上，只把最常用的热数据放在内存里。

**如何在这个项目中无痛迁移？**
得益于我们引入了 `LangChainGo` 框架，代码实现了高度解耦。如果未来知识库达到了百万级，我们**不需要修改任何核心业务流**：
1.  用 Docker 跑一个 `qdrant` 实例。
2.  在 `internal/api/rag.go` 和 `internal/worker/pool.go` 中，把 `redisvector.New(...)` 这行初始化代码，直接替换成 `qdrant.New(...)`。
3.  因为它们都实现了 LangChainGo 的 `vectorstores.VectorStore` 接口，所以底层的 `AddDocuments` 和 `SimilaritySearch` 方法调用**完全不用改**。

这就是优秀架构设计的魅力：**随时可以根据业务体量，平滑替换底层基础设施。**
