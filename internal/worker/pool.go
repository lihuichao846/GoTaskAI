package worker

import (
	"context"
	"encoding/json"
	"fmt"
	"gotaskai/internal/config"
	"gotaskai/internal/model"
	"gotaskai/internal/pkg/kag"
	"gotaskai/internal/pkg/llm"
	"gotaskai/internal/pkg/mcpclient"
	"gotaskai/internal/queue"
	"log"
	"strings"
	"time"

	"github.com/hibiken/asynq"
	"github.com/sashabaranov/go-openai"
	"github.com/tmc/langchaingo/embeddings"
	"github.com/tmc/langchaingo/llms/ollama"
	"github.com/tmc/langchaingo/vectorstores/redisvector"
)

// Pool 维护一个基于 Asynq 的 Worker 池
type Pool struct {
	server     *asynq.Server
	mux        *asynq.ServeMux
	manager    *queue.TaskManager
	llmClient  *llm.Client
	mcpClient  *mcpclient.Wrapper
	ragStore   *redisvector.Store // 增加 RAG 向量存储客户端
	kagManager *kag.KAGManager    // 增加 KAG 图谱管理器
}

// NewPool 创建一个 Asynq Worker 服务器
func NewPool(workers int, manager *queue.TaskManager, redisOpt asynq.RedisConnOpt, mcpClient *mcpclient.Wrapper, kagManager *kag.KAGManager) *Pool {
	srv := asynq.NewServer(
		redisOpt,
		asynq.Config{
			// 并发数
			Concurrency: workers,
			// 指定不同队列的优先级 (数字越大，优先级越高)
			Queues: map[string]int{
				"high":    6,
				"default": 3,
				"low":     1,
			},
		},
	)

	mux := asynq.NewServeMux()

	// 初始化 RAG Vector Store
	var ragStore *redisvector.Store
	ollamaClient, err := ollama.New(
		ollama.WithServerURL("http://localhost:11434"),
		ollama.WithModel("bge-m3"),
	)
	if err == nil {
		embedder, err := embeddings.NewEmbedder(ollamaClient)
		if err == nil {
			redisURL := config.AppConfig.Redis.Addr
			if !strings.HasPrefix(redisURL, "redis://") {
				redisURL = "redis://" + redisURL
			}
			store, err := redisvector.New(
				context.Background(),
				redisvector.WithConnectionURL(redisURL),
				redisvector.WithIndexName("idx:pangu_v2", false), // false 表示不主动创建
				redisvector.WithEmbedder(embedder),
			)
			if err == nil {
				ragStore = store
				log.Println("RAG Vector Store initialized in Worker Pool")
			} else {
				log.Printf("Failed to init redisvector: %v", err)
			}
		}
	} else {
		log.Printf("Failed to init ollama embedder for RAG: %v", err)
	}

	pool := &Pool{
		server:     srv,
		mux:        mux,
		manager:    manager,
		llmClient:  llm.NewClient(),
		mcpClient:  mcpClient,
		ragStore:   ragStore,
		kagManager: kagManager,
	}

	// 注册处理函数
	mux.HandleFunc("task:process", pool.handleTaskProcess)

	return pool
}

// Start 启动 Asynq Server 开始消费任务
func (p *Pool) Start() {
	go func() {
		if err := p.server.Run(p.mux); err != nil {
			log.Fatalf("could not start asynq server: %v", err)
		}
	}()
	log.Println("Asynq Worker pool started")
}

// Stop 优雅地停止工作池
func (p *Pool) Stop() {
	p.server.Stop()
	log.Println("Asynq Worker pool stopped")
}

// handleTaskProcess 是 Asynq 路由的处理函数，负责处理实际的 AI 任务
func (p *Pool) handleTaskProcess(ctx context.Context, asynqTask *asynq.Task) error {
	var t model.Task
	if err := json.Unmarshal(asynqTask.Payload(), &t); err != nil {
		return fmt.Errorf("json.Unmarshal failed: %v", err)
	}

	// 在真正开始处理前，检查任务是否被取消
	latestTask, ok := p.manager.GetTask(t.ID)
	if ok && latestTask.Status == model.StatusCancelled {
		log.Printf("[Worker] Task %s was cancelled before processing, skipping", t.ID)
		return nil
	}

	// 更新内存和数据库状态为"处理中"
	t.Status = model.StatusProcessing
	p.manager.UpdateTask(&t)

	log.Printf("[Worker] Processing task %s (%s)", t.ID, t.Type)

	systemPrompt := t.SystemPrompt
	if systemPrompt == "" {
		systemPrompt = "你是一个有用的 AI 助手。请尽力解答用户的问题或完成用户指定的任务。"
	}

	// 动态强化 MCP 工具调用提示词 (如果注入了 mcpClient)
	if p.mcpClient != nil {
		systemPrompt += `

[特别指示 - 联网工具强制使用规范]:
1. 【必须搜索】：当用户询问客观事实、新闻、人物信息、作品年代等你不确定或需要查证的内容时，你**必须**首先调用 'internet_search' 工具进行谷歌搜索。
2. 【调用格式】：为了保证系统能正确执行搜索，如果你决定调用工具，请在回复中**仅输出工具调用的 JSON 格式**（可放在代码块内或直接输出），不要包含任何其他分析或废话。格式必须为：
{"name": "internet_search", "arguments": {"query": "你要搜索的具体关键词"}}
3. 【禁止抢答】：在系统实际返回搜索结果之前，**绝对不要**直接回答问题，也**绝对不要**提前说“抱歉，由于网络问题我没有找到相关信息”。只有当你看到了工具返回的具体内容后，才能根据内容作答。
4. 【严格基于结果】：只有当你看到搜索结果后，再依据搜索到的内容整理答案。如果工具真的返回了“未找到结果”，你才可以向用户道歉。`
	}

	// === RAG 知识库增强 ===
	if p.ragStore != nil {
		log.Printf("[Worker] Searching RAG knowledge base for task %s", t.ID)
		// 检索最相关的 3 个文档片段
		docs, err := p.ragStore.SimilaritySearch(ctx, t.Payload, 3)
		if err == nil && len(docs) > 0 {
			contextStr := ""
			for i, doc := range docs {
				contextStr += fmt.Sprintf("片段 %d:\n%s\n\n", i+1, doc.PageContent)
			}

			// 将检索到的上下文追加到 systemPrompt 中
			systemPrompt += fmt.Sprintf(`

[系统内部提供的本地知识库参考信息]:
以下是从系统内部知识库中检索到的可能相关的背景信息片段。请优先参考这些信息来回答用户的问题。
如果你发现这些片段与用户的问题无关，请忽略它们，并继续根据你的常识（或调用搜索引擎）进行回答。

<knowledge_base_context>
%s
</knowledge_base_context>`, contextStr)

			log.Printf("[Worker] RAG context injected (%d documents found)", len(docs))
		} else if err != nil {
			log.Printf("[Worker] RAG search error: %v", err)
		} else {
			log.Printf("[Worker] No relevant RAG context found")
		}
	}
	// ======================

	// === KAG 图谱知识库增强 ===
	if p.kagManager != nil {
		log.Printf("[Worker] Searching KAG knowledge graph for task %s", t.ID)
		kagResult, err := p.kagManager.RetrieveGraphContext(ctx, t.Payload)
		if err == nil && kagResult != "" {
			systemPrompt += fmt.Sprintf(`

[系统内部提供的本地知识图谱参考信息]:
以下是从系统内部知识图谱中通过逻辑推理检索到的相关实体关系信息。请优先参考这些确切的关系信息来回答用户的问题。
如果你发现这些信息与用户的问题无关，请忽略它们。

<knowledge_graph_context>
%s
</knowledge_graph_context>`, kagResult)
			log.Printf("[Worker] KAG context injected: %s", kagResult)
		} else if err != nil {
			log.Printf("[Worker] KAG search error: %v", err)
		} else {
			log.Printf("[Worker] No relevant KAG context found")
		}
	}
	// ======================

	var historyMessages []openai.ChatCompletionMessage
	if t.SessionID != "" {
		historyTasks := p.manager.GetTaskHistory(t.SessionID, 6)
		for _, ht := range historyTasks {
			historyMessages = append(historyMessages, openai.ChatCompletionMessage{
				Role:    openai.ChatMessageRoleUser,
				Content: ht.Payload,
			})
			if ht.Result != "" {
				historyMessages = append(historyMessages, openai.ChatCompletionMessage{
					Role:    openai.ChatMessageRoleAssistant,
					Content: ht.Result,
				})
			}
		}
	}

	// 调用大模型 API (设置 2 分钟超时)
	apiCtx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	result, err := p.llmClient.Generate(apiCtx, systemPrompt, historyMessages, t.Payload, p.mcpClient)

	// 再次检查任务在请求 API 期间是否被取消
	checkTask, ok := p.manager.GetTask(t.ID)
	if ok && checkTask.Status == model.StatusCancelled {
		log.Printf("[Worker] Task %s was cancelled during API call, aborting", t.ID)
		return nil
	}

	if err != nil {
		log.Printf("[Worker] Task %s AI call failed: %v", t.ID, err)
		if t.Retries < t.MaxRetry {
			t.Retries++
			t.Status = model.StatusPending
			t.Error = fmt.Sprintf("AI API error, retrying %d/%d: %v", t.Retries, t.MaxRetry, err)
			p.manager.UpdateTask(&t)

			// 返回错误，Asynq 会自动帮我们重试，并且使用指数退避算法
			return err
		} else {
			t.Status = model.StatusFailed
			t.Error = fmt.Sprintf("Failed after %d retries. Last error: %v", t.MaxRetry, err)
			p.manager.UpdateTask(&t)
			log.Printf("[Worker] Task %s permanently failed", t.ID)
			return nil // 不再让 Asynq 重试
		}
	}

	// 任务处理成功
	t.Status = model.StatusCompleted
	t.Result = result
	t.Error = ""
	p.manager.UpdateTask(&t)
	log.Printf("[Worker] Completed task %s successfully", t.ID)

	return nil
}
