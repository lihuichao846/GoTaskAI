# GoTaskAI：从零到一的高并发任务调度平台 (新手进阶学习项目)

**GoTaskAI** 是一个专为 Go 语言初学者和进阶者设计的**学习型高并发架构项目**。
它模拟了真实企业中“AI 耗时任务处理”（如大模型生成、OCR 识别）的场景，带领你一步步从原生的 `net/http` 走到工业级的 `Gin + GORM + Redis` 架构。

通过这个项目，你不仅能学到 Go 语言的语法，更能深刻理解**高并发处理、队列调度、缓存一致性、SSE 实时推送**等核心后端工程思想。

---

## 🌟 核心特性与学习目标

### 1. 并发与队列调度 (核心精髓)
*   **Worker Pool 模式**：不直接在 HTTP 请求里处理耗时任务，而是利用 Go 的 Goroutine 和 Channel 构建了后台工作池，实现了任务的**异步解耦**。
*   **多优先级队列**：实现了 高/中/低 三个级别的优先级调度，利用 `select + default` 非阻塞机制确保高优任务的绝对抢占权。
*   **任务生命周期管理**：支持 Pending -> Processing -> Completed/Failed/Cancelled 的完整状态流转。
*   **失败与重试机制**：内置重试计数器，支持手动触发失败任务“复活”并重新入队。
*   **优雅中断**：支持在任务排队或执行过程中被用户主动取消 (Cancel)。

### 2. 工业级 Web 架构实践
*   **Gin 框架重构**：体验路由分组、参数绑定 (`ShouldBindJSON`) 以及 RESTful API 设计。
*   **Redis 限流中间件 (Rate Limiting)**：利用 Redis 和固定窗口算法，实现了“用户 IP 维度”的防刷单限流机制，保护服务器不被压垮。
*   **安全认证系统 (JWT + bcrypt)**：完整的用户注册/登录闭环。密码单向哈希加密入库，采用无状态的 JWT Token 进行接口鉴权。
*   **Cache-Aside 旁路缓存**：读操作先查 Redis，写操作落库后更新 Redis。彻底弄懂缓存是如何充当 MySQL 的“挡箭牌”的。

### 3. 现代化前端交互
*   **SSE 实时进度推送**：抛弃了极其消耗性能的“定时器轮询”，采用了 Server-Sent Events (SSE) 技术，实现了后端向前端的“打字机式”单向实时状态广播。
*   **Vue 3 响应式界面**：无需复杂的 Node.js 环境，纯 HTML + CDN 引入 Vue 3 和 Tailwind CSS，实现了美观且功能齐全的单页应用 (SPA)。包含批量提交、一键复制、动态标签等交互细节。

---

## 🏗️ 架构图解

```text
[用户浏览器 (Vue3)] 
       │ 
       │ (1. 提交任务 / 批量提交) -> JWT 鉴权 -> IP 限流校验
       ▼
[Gin HTTP Server (API 层)]
       │ 
       │ (2. 生成 UUID，持久化到 MySQL，更新 Redis)
       ▼
[Task Manager (调度层)] ──(3. 根据优先级推入 Channel)──▶ [High / Normal / Low Queues]
       │                                                         │
       │ (5. 状态变更触发 SSE 广播推给前端)                      │ (4. Worker 空闲时抢占执行)
       ▼                                                         ▼
[SSE Stream (长连接推送)] ◀───────────────────────────── [Worker Pool (后台并发池)]
```

---

## 📚 学习文档指南 (新手必看)

项目 `docs` 目录下包含了专门为你编写的通俗易懂的原理解析文档，建议结合代码一起阅读：
*   [Web 框架进化史：net/http vs Gin](./docs/web_framework_guide.md)
*   [Gin 框架 4 大核心方法速查表](./docs/gin_survival_guide.md)
*   [GORM 与 Redis 极简入门指南](./docs/db_and_redis_guide.md)
*   [文件柜与办公桌：为什么我们需要 Redis？](./docs/redis_explanation_guide.md)
*   [告别轮询：SSE 实时推送原理解析](./docs/sse_vs_polling.md)
*   [数据库解惑：Navicat、代码与 Docker 卷](./docs/navicat_vs_code_db.md)

---

## 🚀 快速启动

### 1. 环境准备
你需要安装好以下环境：
*   Go 1.20+
*   Docker & Docker Compose (用于启动 MySQL 和 Redis)

### 2. 启动基础设施 (数据库与缓存)
进入项目根目录，通过 Docker 启动所需环境（注意：为了防止 Windows 本地端口冲突，MySQL 被映射到了 `3307` 端口）：
```bash
docker compose up -d
```

### 3. 运行 Go 后端服务
GORM 会在首次启动时自动为你创建和迁移所有数据库表（`users` 和 `tasks`）。
```bash
go run main.go
```

### 4. 访问页面
打开浏览器，访问：[http://localhost:8080](http://localhost:8080)
1. 注册一个账号并登录。
2. 尝试提交几个任务（输入 `fail` 可以模拟执行失败）。
3. 开启“批量模式”体验高并发提交。
4. 感受 SSE 带来的无刷新实时状态跳动！

---
*本项目从零起步构建，保留了清晰的迭代脉络，是掌握 Go 语言后端工程化的绝佳练手场！*
