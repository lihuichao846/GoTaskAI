# GoTaskAI 文档索引

> 本文档按类别梳理 `docs/` 下的全部技术文档，方便快速检索。
> 目录结构：`tech/`（技术文档）、`project/`（项目管理文档）、`interview/`（面试准备文档）。

---

## 📘 tech/ 技术文档

### 架构与设计
- [GoTaskAI 系统技术文档](./tech/GoTaskAI系统技术文档.md) — 完整技术架构与模块设计（底层依据）
- [Agent 构建平台设计](./tech/agent_platform_design.md) — 从「任务平台」演进为「自建 Agent 基础设施」的设计
- [Harness 工程化优化方向](./tech/harness工程优化方向.md) — 补齐「验证/控制/成本/可观测」护栏的演进规划
- [Harness 工程优化 - 实现记录](./tech/harness工程优化-实现记录.md) — 六项目标的落地实现说明
- [微服务架构设计](./tech/microservice_architecture_design.md) — 微服务拆分与解耦
- [微服务生态指南](./tech/microservices_ecosystem_guide.md) — 微服务相关组件梳理
- [GraphRAG 与微服务](./tech/graphrag_and_microservices_guide.md) — GraphRAG 与微服务协同

### RAG / 知识库
- [RAG 架构指南](./tech/rag_architecture_guide.md) — RAG 检索增强生成设计
- [RAG 与大模型对话核心问题与实战记录](./tech/rag_qna_record.md) — RAG/LLM 机制 FAQ
- [知识库修正技术路线调研与选型](./tech/知识库修正技术路线调研与选型.md) — 四大修正路线选型
- [知识库修正技术方案](./tech/知识库修正技术方案.md) — 完整可落地的修正方案
- [知识库问题盘点与异常分类](./tech/知识库问题盘点与异常分类.md) — 数据标注与异常分类报告

### 测试 / 实测
- [实测结论](./tech/实测结论.md) — GraphRAG vs 传统 RAG、并发成本/吞吐实测报告

### 功能实现说明
- [对话上下文自动压缩-实现说明](./tech/对话上下文自动压缩-实现说明.md) — 大模型摘要压缩实现详解

### 技术教程 / 原理
- [Asynq 消息队列指南](./tech/asynq_message_queue_guide.md)
- [MySQL 原理文档](./tech/mysql_explanation_guide.md)
- [MySQL 与 Redis 使用指南](./tech/db_and_redis_guide.md)
- [Redis 原理说明](./tech/redis_explanation_guide.md)
- [NATS 说明](./tech/nats_explanation_guide.md)
- [Gin 使用指南](./tech/gin_survival_guide.md)
- [Web 框架指南](./tech/web_framework_guide.md)
- [Go GMP 与 Channel](./tech/go_gmp_and_channel.md)
- [IO 多路复用 epoll](./tech/io_multiplexing_epoll.md)
- [JWT 架构优势](./tech/jwt_architecture_advantage.md)
- [HTTP vs HTTPS](./tech/http_vs_https_guide.md)
- [SSE vs 轮询](./tech/sse_vs_polling.md)
- [Nginx 架构指南](./tech/nginx_architecture_guide.md)
- [Docker 操作指南](./tech/docker_commands_guide.md)
- [Git 指南](./tech/git_guide.md)
- [Navicat 与 VS Code 数据库](./tech/navicat_vs_code_db.md)
- [代码阅读总路线](./tech/code_reading_route.md)

---

## 📁 project/ 项目管理文档

- [项目实战问题与解决方案 FAQ](./project/project_faq_and_troubleshooting.md) — 开发踩坑日记与技术栈演进
- [项目 Bug 报告](./project/项目bug报告.md) — 源码审查与实测发现的问题记录
- [项目流程回顾](./project/项目流程回顾.md) — 分析、讲解与 Redis Sentinel 改造回顾
- [项目精读日志](./project/项目精读日志.md) — 逐文件深读记录

---

## 🎯 interview/ 面试准备文档

- [项目面试总结](./interview/项目面试总结.md) — 按「面试怎么讲」组织的完整总结
- [项目面试场景题解析指南](./interview/interview_scenario_questions.md) — 12 个高频场景题满分话术
- [面试经典思维题](./interview/logical_thinking_questions.md) — 经典逻辑思维题解析
