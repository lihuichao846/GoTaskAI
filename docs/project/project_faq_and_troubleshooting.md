# GoTaskAI 项目实战问题与解决方案记录 (FAQ)

这份文档专门用于记录在 `GoTaskAI` 项目开发过程中遇到的实际问题、报错信息、排查思路以及最终的解决方案。
它将作为你的“项目踩坑日记”，方便日后快速复盘和查阅。

## 🛠️ 当前核心技术栈 (Tech Stack)

本项目在演进过程中，技术栈不断升级以满足生产级需求，当前核心架构包含：

*   **基础语言与 Web 框架**：基于 **Go** 语言开发，使用 **Gin** 作为高性能 HTTP 路由框架，处理 API 请求、限流及中间件逻辑。
*   **数据持久化**：使用 **MySQL** 作为关系型数据库落盘核心业务数据，通过 **GORM** 进行数据对象映射与操作。
*   **高性能缓存与多面手中枢**：采用 **Redis Stack**。不仅使用 Redis 作为 Cache-Aside 模式的查询缓存和 API 接口限流器，还利用其高级特性支撑队列与向量检索功能。
*   **异步任务调度**：引入 **Asynq** (基于 Redis) 替换了早期的纯内存 Go Channel 方案，实现了高可靠、支持持久化、多优先级及延迟重试的工业级异步任务队列，主要用于大模型对话等耗时任务的排队与调度。
*   **AI 与 RAG (检索增强生成)**：全面集成 **LangChainGo** 框架构建 RAG 工作流。使用其内置的文档加载器、`RecursiveCharacterTextSplitter` 文本切分器，结合 **Ollama** 本地嵌入模型（如 `bge-m3`），并将向量化后的数据存储至 **RedisVector** 进行高效的语义相似度检索，最后动态注入到系统提示词中供大模型生成回答。
*   **前端交互**：原生 HTML/JS/CSS 开发轻量级交互，通过 Fetch API 与后端通信，并使用 **SSE (Server-Sent Events)** 技术实现 AI 生成内容的流式（打字机）输出与任务状态的实时更新。
*   **运行环境**：依赖 **Docker** 及 Docker Compose 快速编排运行 MySQL 与 Redis Stack 等基础设施服务。

### 问题 5：调度层真的是一个“消息队列”吗？
*   **问题描述**：理解架构时，认为调度层是由一个消息队列（如 RabbitMQ/Kafka）解决的，这种理解准确吗？
*   **原理解析**：这种理解**大方向是正确的**，且随着项目的演进，我们已经真正落地了工业级方案。
    *   **早期架构 (Go Channel 模拟)**：在项目初期，调度层没有使用外部中间件，而是使用 **Go 语言原生的 Channel（通道）** 模拟消息队列（基于内存）。优点是极度轻量、性能极高；缺点是宕机会丢失排队任务，且无法横向扩展。
    *   **当前架构 (基于 Redis 的 Asynq 队列)**：为了解决内存队列无法持久化、缺乏重试与死信机制的问题，项目现已**重构升级**，全面引入了基于 Redis 的 **Asynq** 库作为核心调度层。这不仅实现了多优先级队列调度，还能防止大模型耗时任务因宕机而丢失。
    *   **技术选型考量**：对于异步任务调度（如大模型调用队列），如果不需要高吞吐的流式数据处理（如日志聚合），选择 Redis-based Asynq 而非 Kafka/RabbitMQ 是一种极佳的折中。它能极大降低运维复杂度（复用已有的 Redis 服务，无需额外部署），且原生提供延迟重试等更契合任务调度的功能。
    *   **总结**：在本项目中，早期的调度层是一个内存模拟队列，但现在它已经演变为一个**真正持久化、高可靠的工业级异步消息队列**（Asynq + Redis）。

### 问题 6：这个项目能用到 Nginx 吗？其主要起什么作用？
*   **问题描述**：在当前的 GoTaskAI 架构中，是否有必要引入 Nginx？如果引入，它能解决什么问题？
*   **原理解析**：
    当前我们的项目是直接让前端访问 Gin 启动的 HTTP 服务（比如直接访问 `http://localhost:8080`）。在本地开发和小规模测试时完全没问题。但在**真实的生产环境（线上部署）**中，**强烈建议甚至必须引入 Nginx**。
    
    如果把咱们的 Go 后端比作“大厨”，Nginx 就是挡在前面的“餐厅前台接待员”。它的核心作用如下：

    1.  **反向代理 (Reverse Proxy) 与端口隐藏**
        *   **现状**：用户需要带上丑陋的端口号访问 `http://yourdomain.com:8080`。
        *   **Nginx 作用**：Nginx 监听默认的 80 (HTTP) 和 443 (HTTPS) 端口。用户直接访问 `http://yourdomain.com`，Nginx 在内部悄悄把请求转发给本地的 8080 端口。外网根本不知道你内部跑的是什么端口。
    2.  **HTTPS (SSL/TLS) 证书终结**
        *   **现状**：我们的 Gin 代码里没有配置 HTTPS 证书，数据是明文传输的，极不安全。
        *   **Nginx 作用**：通常我们不会在 Go 代码里去加载证书。而是把 SSL 证书配置在 Nginx 上。Nginx 负责和用户建立加密的 HTTPS 连接，解密后，再用普通的 HTTP 转发给内部的 Go 服务。这叫“SSL 终结”，极大降低了后端代码的复杂度。
    3.  **负载均衡 (Load Balancing)**
        *   **现状**：目前我们的 GoTaskAI 只有一个实例在跑。
        *   **Nginx 作用**：如果以后访问量大增，你启动了 3 个 GoTaskAI 实例（分别跑在 8080、8081、8082）。Nginx 可以作为流量分发器，把用户的请求均匀地轮询分发给这 3 个实例，实现系统的横向扩展。
    4.  **静态资源分离 (动静分离)**
        *   **现状**：在 [main.go](../main.go) 中，我们用了 `r.StaticFile("/", "./public/index.html")` 让 Go 去返回前端的 HTML 文件。
        *   **Nginx 作用**：处理静态文件（HTML/CSS/JS/图片）是 Nginx 的强项，速度极快。生产环境中，通常让 Nginx 直接返回这些静态资源，只有遇到 `/api/*` 这种动态数据请求时，才转发给 Go 处理。这样能大大减轻 Go 服务的压力。

*   **总结**：对于 GoTaskAI 来说，加入 Nginx 是走向**生产环境**的必经之路，它能为项目带来**更安全的访问(HTTPS)、更优雅的域名、更高的并发上限(动静分离)以及横向扩展(负载均衡)的能力**。

---

## 📅 [2026-03-30] 网络与 Git 环境问题

### 问题 1：Git Push 报错 `ssh: connect to host github.com port 22: Connection timed out`
*   **问题描述**：在终端执行 `git push -u origin master` 时，提示连接 GitHub 的 22 端口超时。
*   **原因分析**：本地开启了 VPN 代理软件（如 Clash 等），代理软件默认只接管 HTTP/HTTPS (80/443端口) 流量，没有接管 SSH (22端口) 流量。导致 SSH 连接被阻断或超时。
*   **解决方案**：修改 SSH 配置文件，让连接 GitHub 的 SSH 流量走 443 端口。
    1.  打开 `~/.ssh/config`（Windows 下为 `C:\Users\用户名\.ssh\config`）。
    2.  在文件末尾追加以下内容：
        ```text
        Host github.com
            HostName ssh.github.com
            Port 443
            User git
        ```
    3.  保存后，使用 `ssh -T git@github.com` 测试连接成功。

### 问题 2：在 PowerShell 中执行 `git branch -a` 报错
*   **问题描述**：输入 `gi branch -a` 时，PowerShell 报错 `Get-Item : A parameter cannot be found that matches parameter name 'a'.`。
*   **原因分析**：输入时少打了一个字母 `t`，将 `git` 拼写成了 `gi`。在 PowerShell 中，`gi` 是系统内置命令 `Get-Item` 的缩写别名，系统尝试执行 `Get-Item branch -a` 从而引发参数错误。
*   **解决方案**：补全命令，正确输入 `git branch -a` 即可。

---

## 🏗️ 架构与高并发设计理解

### 问题 3：服务端的高并发仅仅是“用线程池限制 Goroutine 数量”吗？
*   **问题描述**：在查看 `internal/worker/pool.go` 时，疑惑通过协程池限制 Goroutine 数量是否就是高并发的全部。
*   **原理解析**：Goroutine 池（并发限流）只是**必要但不充分**条件。它只解决了“单服务实例瞬时并发量可控”的问题。
*   **完整的高并发体系**应包含：
    1.  **并发控制**：Worker Pool 限制 CPU/内存过度占用。
    2.  **资源约束**：数据库连接池（如 GORM 的 MaxOpenConns）、Redis 连接池，防止下游被打挂。
    3.  **流量治理**：入口限流（令牌桶）、任务排队背压（队列满则拒绝）。
    4.  **缓存架构**：使用 Redis（Cache-Aside 模式）抵挡海量读请求，保护 MySQL。
    5.  **容灾降级**：超时控制、失败重试、服务熔断。

### 问题 4：GoTaskAI 的整体架构设计与核心数据流是怎样的？
*   **问题描述**：希望详细了解当前项目的分层架构以及数据是如何在各个组件中流转的。
*   **架构分层解析**：
    1.  **接入层 (API Layer)**：基于 `Gin` 框架。负责路由分发、中间件拦截（JWT 鉴权、Redis 限流）以及参数的绑定与校验（`ShouldBindJSON`）。
    2.  **调度层 (Manager Layer)**：`queue.TaskManager` 充当大脑。负责将任务封装后推入 `Asynq` 的多优先级队列中，协调 MySQL 与 Redis（Cache-Aside 模式）以保证数据一致性，并维护客户端长连接进行 SSE 广播。
    3.  **执行层 (Worker Layer)**：`worker.Pool`。通过启动 `Asynq` 的 Server 实例，从队列中消费任务。执行大模型对话前，利用 `LangChainGo` 结合 `RedisVector` 进行 RAG (检索增强生成)，将获取的本地知识库片段注入到 Prompt 中再调用模型。
    4.  **存储层 (Storage Layer)**：MySQL 作为数据最终归宿（持久化），Redis Stack 承担了“缓存挡箭牌”、API 限流器、异步队列（Asynq 存储介质）以及向量数据库（RedisVector）等多重角色。
*   **核心数据流转图 (Data Flow)**：
    ```text
    [用户浏览器] -> (HTTP POST /submit) -> [Gin Router 安检/限流] -> [TaskManager 生成任务记录并落库/缓存]
                                                                        |
    (HTTP 200 立即返回，不阻塞) <---------------------------------------| (将任务推入基于 Redis 的 Asynq 队列)
                                                                        |
                                                                        v
    [Asynq Worker Pool] <- (空闲 Worker 根据优先级从 Redis 队列拉取任务) 
          |
          |--> 1. 更新状态为 Processing (落库+缓存)，触发 SSE 广播告诉前端开始处理
          |--> 2. RAG 知识检索：通过 LangChainGo 查询 RedisVector 获取相关文本片段
          |--> 3. 拼接 Prompt，调用本地 Ollama 大模型生成回复 (结果流式输出)
          |--> 4. 更新状态为 Completed (落库+缓存)，触发 SSE 广播结束信号 -> [前端页面完成更新]
    ```

### 问题 7：本项目的 Redis 到底存了什么数据？用的什么结构？具体怎么存的？
*   **问题描述**：对项目中 Redis 的具体应用场景和底层存储方式感到模糊。
*   **原理解析**：
    在 `GoTaskAI` 项目中，Redis 主要承担了两项核心工作：**任务状态缓存** 和 **API 接口限流**。
    
    #### 1. 任务状态缓存 (Task Cache)
    *   **存了什么**：存的是用户提交的任务的详细信息（ID、状态、优先级、结果等），也就是 `model.Task` 结构体的数据。
    *   **用的什么结构**：使用了 Redis 最基础的 **String（字符串）** 类型。
    *   **具体怎么存的（代码映射）**：
        由于 Redis 的 String 只能存文本，我们不能直接把 Go 的结构体塞进去。所以在 `internal/queue/manager.go` 中：
        1.  **序列化**：先把 Go 的 `model.Task` 结构体，通过 `json.Marshal(t)` 转化为一长串 **JSON 字符串**。
        2.  **设置 Key**：为了防止键名冲突，我们加了前缀，Key 的格式为 `task:<任务ID>`（例如 `task:12345`）。
        3.  **存入 Redis**：调用 `rdb.Set(ctx, "task:"+t.ID, data, 24*time.Hour)`，将这串 JSON 文本存入，并设置了 24 小时的过期时间 (TTL) 作为兜底。
        4.  **反序列化（读取时）**：前端来查询时，拿到的是 JSON 字符串，再通过 `json.Unmarshal` 变回 Go 的结构体。

    #### 2. API 接口限流 (Rate Limiting)
    *   **存了什么**：存的是某个用户（或某个 IP）在特定时间窗口内**访问接口的次数**。
    *   **用的什么结构**：依然是 **String（字符串）** 类型，但利用了 Redis 的数值操作特性（底层以整形存储以便自增）。
    *   **具体怎么存的**：
        1.  **设置 Key**：通常格式为 `rate_limit:<用户IP>:<接口路径>`。
        2.  **原子自增**：每次请求进来，调用 Redis 的 `INCR` 命令让这个 Key 的值加 1。
        3.  **判断拦截**：如果发现值超过了阈值（比如 1 分钟内 > 60 次），就直接在 Gin 中间件里拦截并返回 HTTP 429 (Too Many Requests)。
    *   **总结**：项目中目前 Redis 用的全是 **String** 结构，通过 JSON 序列化解决了复杂对象的存储问题，通过 `INCR` 解决了计数器和限流问题。

### 问题 8：前端报错 `Unexpected non-whitespace character after JSON`
*   **问题描述**：在点击登录或注册时，前端弹窗报错 `网络错误: Unexpected non-whitespace character after JSON at position 4`。
*   **原因分析**：
    1.  这个错误是典型的“前端期望收到 JSON，但后端返回了纯文本”导致的。
    2.  当我们调用 `res.json()` 时，如果后端返回了 `404 page not found`，前 3 个字符是 `404`（被当做数字解析成功），遇到第 4 个字符空格或 `p` 时就会报错。
    3.  **根本原因**：你可能在修改了 `main.go`（比如新增了 `/api/auth/login` 路由）后，**没有重新编译或重启后端服务**。导致你现在跑的还是旧版本的后端程序，它根本不认识 `/api/auth/login` 这个路径，所以返回了 404。
*   **解决方案**：
    1.  **重启后端**：杀掉旧的终端进程或后台进程，重新执行 `go run main.go`。
    2.  **前端鲁棒性优化**：在 `index.html` 的 `fetch` 请求中，先判断 `res.headers.get("content-type")` 是否包含 `application/json`，如果是再调用 `res.json()`，否则当作普通文本抛出异常，这样能看到更清晰的报错（如 `HTTP error 404 page not found`）。

---

## 📝 待解决/探索的问题池
*(在开发中遇到新的疑惑，可以先记录在这里，后续逐一攻克)*

*   [ ] 如何在 GoTaskAI 中引入 Redis 集群模式，并解决哈希标签（Hash Tags）的分配问题？
*   [ ] 任务队列满了之后的“背压（Backpressure）”策略在代码中如何优雅地实现？
*   [ ] ...
