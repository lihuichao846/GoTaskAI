package worker

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"gotaskai/internal/config"
	"gotaskai/internal/metrics"
	"gotaskai/internal/model"
	"gotaskai/internal/pkg/intent"
	"gotaskai/internal/pkg/kag"
	"gotaskai/internal/pkg/kbimport"
	"gotaskai/internal/pkg/llm"
	"gotaskai/internal/pkg/mcpclient"
	"gotaskai/internal/pkg/ragstore"
	"gotaskai/internal/pkg/rerank"
	"gotaskai/internal/pkg/toolregistry"
	"gotaskai/internal/queue"
	"log"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/hibiken/asynq"
	"github.com/sashabaranov/go-openai"
	"github.com/tmc/langchaingo/embeddings"
	"github.com/tmc/langchaingo/schema"
	"github.com/tmc/langchaingo/textsplitter"
)

// Pool 维护一个基于 Asynq 的 Worker 池。
type Pool struct {
	server       *asynq.Server
	mux          *asynq.ServeMux
	manager      *queue.TaskManager
	llmClient    *llm.Client
	mcpClient    *mcpclient.Wrapper
	ragStore     *ragstore.Store
	reranker     rerank.Reranker
	kagManager   *kag.KAGManager
	toolRegistry *toolregistry.Registry
	mcpPool      *mcpclient.Pool
	embedder     embeddings.Embedder
	// intentRouter 为检索/工具路由（成本优化）；未启用时其 Route 直接返回保守全量决策。
	intentRouter intent.Router

	// graphSem 为图谱阶段并发信号量（W1）：与向量阶段共用 low 队列，靠它进一步限制图谱并发。
	graphSem chan struct{}

	// inFlight 维护本进程正在处理的任务的取消函数，供取消控制指令中断进行中的 LLM 调用。
	inFlightMu sync.Mutex
	inFlight   map[string]context.CancelFunc
}

// NewPool 创建一个 Asynq Worker 服务器。
func NewPool(workers int, manager *queue.TaskManager, redisOpt asynq.RedisConnOpt, mcpClient *mcpclient.Wrapper, kagManager *kag.KAGManager) *Pool {
	srv := asynq.NewServer(
		redisOpt,
		asynq.Config{
			Concurrency: workers,
			Queues: map[string]int{
				"high":    6,
				"default": 3,
				"low":     1,
			},
		},
	)

	mux := asynq.NewServeMux()

	var embedder embeddings.Embedder
	var ragStore *ragstore.Store

	emb, merr := llm.NewEmbedder()
	if merr == nil {
		embedder = emb
		mc := config.AppConfig.Milvus
		store, serr := ragstore.New(context.Background(), ragstore.Config{
			Address:        fmt.Sprintf("%s:%d", mc.Host, mc.Port),
			CollectionName: mc.CollectionName,
			Dim:            mc.Dim,
			Embedder:       embedder,
		})
		if serr == nil {
			ragStore = store
			log.Println("RAG Vector Store (Milvus) initialized in Worker Pool")
		} else {
			log.Printf("Failed to init Milvus ragstore: %v", serr)
		}
	} else {
		log.Printf("Failed to init embedding for RAG: %v", merr)
	}

	// 初始化召回重排器（可选）：阿里云百炼 DashScope 的 qwen3-rerank 云端重排模型。
	var reranker rerank.Reranker
	if rc := config.AppConfig.Rerank; rc.Enabled && rc.BaseURL != "" && rc.APIKey != "" && rc.Model != "" {
		reranker = rerank.NewDashScope(rc.BaseURL, rc.APIKey, rc.Model)
		log.Printf("Reranker (DashScope) initialized with model %s", rc.Model)
	}

	// 初始化 Tool Registry：内置工具 + 已连接的 MCP 工具。
	toolRegistry := toolregistry.NewRegistry()
	toolregistry.RegisterBuiltins(toolRegistry)
	if mcpClient != nil {
		if mcpTools, terr := mcpClient.GetTools(context.Background()); terr == nil {
			for _, t := range mcpTools {
				toolRegistry.Register(toolregistry.NewMCPServerTool(mcpClient, t))
			}
			log.Printf("Registered %d MCP tools from search_mcp", len(mcpTools))
		}
	}

	llmClient := llm.NewClient()

	// 初始化意图路由（三级混合：规则 → 语义 → LLM）。
	// 语义层依赖 embedding，构造后异步预热；预热失败则语义层自动禁用（下传 LLM），不阻断任务。
	ic := config.AppConfig.Intent
	var semantic *intent.SemanticClassifier
	if ic.Enabled && (ic.Mode == intent.ModeSemantic || ic.Mode == intent.ModeHybrid) {
		semantic = intent.NewSemanticClassifier(llmClient, ic.SemanticHigh, ic.SemanticMargin, ic.SemanticAllowClose)
		go func() {
			warmCtx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
			defer cancel()
			if err := semantic.Warmup(warmCtx); err != nil {
				log.Printf("[Worker] intent semantic layer disabled (warmup failed): %v", err)
			}
		}()
	}
	intentRouter := intent.New(intent.Config{
		Enabled:            ic.Enabled,
		Mode:               ic.Mode,
		SemanticHigh:       ic.SemanticHigh,
		SemanticMargin:     ic.SemanticMargin,
		SemanticAllowClose: ic.SemanticAllowClose,
		LLMMinConfidence:   ic.LLMMinConfidence,
		EnableToolRouting:  ic.EnableToolRouting,
	}, llmClient, semantic)

	pool := &Pool{
		server:       srv,
		mux:          mux,
		manager:      manager,
		llmClient:    llmClient,
		mcpClient:    mcpClient,
		ragStore:     ragStore,
		reranker:     reranker,
		kagManager:   kagManager,
		toolRegistry: toolRegistry,
		mcpPool:      mcpclient.NewPool(),
		embedder:     embedder,
		intentRouter: intentRouter,
		inFlight:     make(map[string]context.CancelFunc),
	}

	if n := config.AppConfig.KB.GraphQueueConcurrency; n > 0 {
		pool.graphSem = make(chan struct{}, n)
	}

	mux.HandleFunc("task:process", pool.handleTaskProcess)
	mux.HandleFunc("kb:build", pool.handleKBBuild)
	mux.HandleFunc("kb:delete", pool.handleKBDelete)
	mux.HandleFunc("doc:delete", pool.handleDocDelete)
	mux.HandleFunc("kb:scan", pool.handleKBScan)

	// 订阅任务取消控制指令（NATS 广播）：命中本进程在途任务时中断其进行中的 LLM 调用。
	manager.SubscribeTaskCancel(pool.cancelTask)

	return pool
}

// Start 启动 Asynq Server 开始消费任务。
func (p *Pool) Start() {
	go func() {
		if err := p.server.Run(p.mux); err != nil {
			log.Fatalf("could not start asynq server: %v", err)
		}
	}()
	log.Println("Asynq Worker pool started")
}

// Stop 优雅地停止工作池。
func (p *Pool) Stop() {
	p.server.Stop()
	p.mcpPool.Close()
	log.Println("Asynq Worker pool stopped")
}

// registerInFlight 登记任务的可取消上下文，供取消控制指令命中。
func (p *Pool) registerInFlight(taskID string, cancel context.CancelFunc) {
	p.inFlightMu.Lock()
	p.inFlight[taskID] = cancel
	p.inFlightMu.Unlock()
}

// unregisterInFlight 移除任务的可取消上下文。
func (p *Pool) unregisterInFlight(taskID string) {
	p.inFlightMu.Lock()
	delete(p.inFlight, taskID)
	p.inFlightMu.Unlock()
}

// cancelTask 中断指定任务的进行中 LLM 调用（若有）。
func (p *Pool) cancelTask(taskID string) {
	p.inFlightMu.Lock()
	cancel, ok := p.inFlight[taskID]
	p.inFlightMu.Unlock()
	if ok {
		log.Printf("[Worker] cancelling in-flight task %s", taskID)
		cancel()
	}
}

// resolveTools 根据任务绑定的 Agent 返回可用的工具白名单，并动态加载私有 MCP 工具。
func (p *Pool) resolveTools(ctx context.Context, t model.Task) []toolregistry.Tool {
	if t.AgentID == "" {
		return p.toolRegistry.List()
	}

	bound := p.manager.ListAgentTools(t.AgentID)
	if len(bound) == 0 {
		return nil
	}

	out := make([]toolregistry.Tool, 0, len(bound))
	for _, bt := range bound {
		// 工具被停用（disabled）时，即使绑定到 Agent 也不加载，确保运行时彻底不可用。
		if bt.Status == "disabled" {
			continue
		}

		// 优先匹配静态注册的工具（内置 + search_mcp）。
		if static, ok := p.toolRegistry.Get(bt.Name); ok {
			out = append(out, static)
			continue
		}

		// 用户私有 MCP 工具：按 config 动态拉起连接（支持 stdio 子进程或远程 URL）。
		if bt.Type == "mcp" && bt.Config != "" {
			var cfg model.ToolConfig
			if err := json.Unmarshal([]byte(bt.Config), &cfg); err == nil && cfg.Valid() {
				wrapper, err := p.mcpPool.Get(ctx, bt.ID, cfg.Command, cfg.URL, cfg.Args, cfg.Env)
				if err != nil {
					log.Printf("[Worker] Failed to connect MCP tool %s: %v", bt.Name, err)
					continue
				}
				mcpTools, err := wrapper.GetTools(ctx)
				if err != nil {
					log.Printf("[Worker] Failed to list tools from %s: %v", bt.Name, err)
					continue
				}
				for _, mt := range mcpTools {
					out = append(out, toolregistry.NewMCPServerTool(wrapper, mt))
				}
			}
		}
	}
	return out
}

// hasToolName 判断工具列表中是否包含指定名称的工具。
func hasToolName(tools []toolregistry.Tool, name string) bool {
	for _, t := range tools {
		if t.Name() == name {
			return true
		}
	}
	return false
}

// routeIntent 组装路由上下文并执行意图路由，决定本次任务是否需要 RAG/KAG 检索。
// 未配置路由时返回保守的全量检索决策（等价改造前行为）。路由阶段使用独立超时并
// 登记取消，避免供应商挂起时阻塞任务且无法中断；同时上报指标与结构化日志。
func (p *Pool) routeIntent(ctx context.Context, t model.Task, tools []toolregistry.Tool, budget *llm.Budget, apiKey, baseURL, model string) intent.Decision {
	start := time.Now()
	decision := intent.FullPipeline(
		intent.Request{HasKB: p.ragStore != nil, HasKAG: p.kagManager != nil},
		intent.SourceDisabled,
	)

	if p.intentRouter != nil {
		routeTimeout := 5 * time.Second
		if s := config.AppConfig.Intent.RouteTimeoutSeconds; s > 0 {
			routeTimeout = time.Duration(s) * time.Second
		}
		routeCtx, routeCancel := context.WithTimeout(ctx, routeTimeout)
		p.registerInFlight(t.ID, routeCancel)
		decision = p.intentRouter.Route(routeCtx, intent.Request{
			Question:    t.Payload,
			HasKB:       p.ragStore != nil,
			HasKAG:      p.kagManager != nil,
			HasInternet: hasToolName(tools, "internet_search"),
			APIKey:      apiKey,
			BaseURL:     baseURL,
			Model:       model,
		}, budget)
		p.unregisterInFlight(t.ID)
		routeCancel()
	}

	metrics.IntentRouteLatency.Observe(time.Since(start).Seconds())
	metrics.IntentDecisions.WithLabelValues(string(decision.Source)).Inc()
	if !decision.NeedRAG {
		metrics.IntentRetrievalSkipped.Inc()
	}
	log.Printf("[Worker] task %s intent: rag=%v kag=%v tools=%v source=%s conf=%.2f",
		t.ID, decision.NeedRAG, decision.NeedKAG, decision.NeedTools, decision.Source, decision.Confidence)

	return decision
}

// retrieveKnowledge 返回注入 system_prompt 的知识库检索上下文。
// 采用渐进式披露：先注入命中文档的摘要层，再按 Score 分层注入截断后的命中片段，并受总量上限约束。
func (p *Pool) retrieveKnowledge(ctx context.Context, t model.Task) string {
	if p.ragStore == nil {
		return ""
	}

	cc := config.AppConfig.Context
	topK := 3
	if rc := config.AppConfig.Rerank; rc.TopN > 0 {
		topK = rc.TopN
	}

	var sb strings.Builder
	// write 在达到上下文总量上限时停止追加，返回是否继续。
	write := func(s string) bool {
		if cc.MaxContextChars > 0 && sb.Len()+len(s) > cc.MaxContextChars {
			return false
		}
		sb.WriteString(s)
		return true
	}

	// appendKBDocs 注入单个知识库的摘要层与命中层（按 Score 分层），返回是否继续追加。
	appendKBDocs := func(kbName string, docs []schema.Document) bool {
		normalizeScores(docs)

		if summaries := p.collectDocSummaries(docs); len(summaries) > 0 {
			if !write(fmt.Sprintf("【%s · 文档摘要】\n", kbName)) {
				return false
			}
			for _, summary := range summaries {
				if !write(fmt.Sprintf("- %s\n", truncateChunk(summary, cc.MaxChunkChars))) {
					return false
				}
			}
		}

		if !write(fmt.Sprintf("【%s · 片段】\n", kbName)) {
			return false
		}
		// 上下文感知分块（B1）：以命中块为中心做块级窗口扩展，补回原文档相邻上下文。
		expanded, err := p.expandChunks(ctx, docs, cc)
		if err != nil {
			// 扩窗失败不阻断注入，退回原始命中块（等价改造前行为）。
			expanded = docs
		}
		for i, doc := range expanded {
			if !write(fmt.Sprintf("片段 %d:\n%s\n\n", i+1, chunkByScore(doc, cc))) {
				return false
			}
		}
		return true
	}

	if t.AgentID != "" {
		kbs := p.manager.ListAgentKnowledgeBases(t.AgentID)
		if len(kbs) > 0 {
			for _, kb := range kbs {
				docs, err := p.retrieveTopDocs(ctx, t.Payload, kb.ID, topK)
				if err != nil {
					log.Printf("[Worker] KB %s retrieval failed: %v", kb.ID, err)
					continue
				}
				if len(docs) == 0 {
					continue
				}
				if !appendKBDocs(kb.Name, docs) {
					return sb.String()
				}
			}
			if sb.Len() > 0 {
				return sb.String()
			}
		}
	}

	// 未绑定知识库时回退公共/默认知识检索（同样注入摘要层 + 命中层）。
	docs, err := p.retrieveTopDocs(ctx, t.Payload, "", topK)
	if err == nil && len(docs) > 0 {
		appendKBDocs("公共知识", docs)
	}
	return sb.String()
}

// collectDocSummaries 按命中文档顺序返回其构建期摘要（去重、有序）。
func (p *Pool) collectDocSummaries(docs []schema.Document) []string {
	seen := make(map[string]bool)
	var docIDs []string
	for _, d := range docs {
		if id, ok := d.Metadata["doc_id"].(string); ok && id != "" && !seen[id] {
			seen[id] = true
			docIDs = append(docIDs, id)
		}
	}
	if len(docIDs) == 0 {
		return nil
	}
	summaries := p.manager.GetDocumentSummaries(docIDs)
	out := make([]string, 0, len(docIDs))
	for _, id := range docIDs {
		if s, ok := summaries[id]; ok {
			out = append(out, s)
		}
	}
	return out
}

// expandChunks 对命中块做上下文感知窗口扩展（B1 主路径）。
// 以命中块 chunk_index 为中心，从 Milvus 按 doc_id 取回该文档有序 chunk 序列，
// 取前后 ±ChunkContextWindow 个相邻块，汇总为局部连续上下文并按「doc_id:chunk_index」去重。
// 扩展邻居块沿用命中块的归一化 Score（保持同一分层截断口径）。
// 若当前 Collection 无 chunk_index（存量，B2）则回退 Document.Content 全文兜底（受 EnableParentContext 控制）；
// 会话阈值或未启用窗口时直接返回原始命中块（等价改造前行为）。
func (p *Pool) expandChunks(ctx context.Context, docs []schema.Document, cc config.ContextConfig) ([]schema.Document, error) {
	if cc.ChunkContextWindow <= 0 || p.ragStore == nil {
		return docs, nil
	}

	seen := make(map[string]bool, len(docs)*3)
	expanded := make([]schema.Document, 0, len(docs)*3)

	for _, d := range docs {
		docID, _ := d.Metadata["doc_id"].(string)
		if docID == "" {
			// 无文档归属的命中块（如默认公共块）无法扩窗，按原始方式注入。
			expanded = append(expanded, d)
			continue
		}
		hitIdx, _ := d.Metadata["chunk_index"].(int64)

		chunks, err := p.ragStore.GetChunksByDocID(ctx, docID)
		if err != nil {
			log.Printf("[Worker] expand window for doc %s failed: %v", docID, err)
			chunks = nil
		}

		if len(chunks) == 0 {
			// B2 降级：存量无 chunk_index 或取块失败。若启用父上下文兜底，注入全文。
			if cc.EnableParentContext {
				key := "doc-full:" + docID
				if !seen[key] {
					if d2, ok := p.manager.GetDocument(docID); ok && d2.Content != "" {
						seen[key] = true
						expanded = append(expanded, schema.Document{
							PageContent: d2.Content,
							Score:       d.Score,
							Metadata: map[string]any{
								"doc_id": docID,
							},
						})
					}
				}
				continue
			}
			key := fmt.Sprintf("doc:%s:%d", docID, hitIdx)
			if !seen[key] {
				seen[key] = true
				expanded = append(expanded, d)
			}
			continue
		}

		// 在有序块序列中定位命中块，取窗口并去重追加。
		expanded = addWindowChunks(chunks, docID, hitIdx, d.Score, cc.ChunkContextWindow, seen, expanded)
	}
	return expanded, nil
}

// addWindowChunks 在已知某文档的有序块序列 ordered 中，以命中块 hitIdx 为中心取前后 ±window 个相邻块，
// 通过 seen（key = "doc_id:chunk_index"）去重后追加到 expanded；邻居块 Score 统一置为命中块 hitScore，
// 以保持与命中块相同的分层截断口径。若在 ordered 中未定位到命中块，则原样返回 expanded（不追加）。
func addWindowChunks(ordered []schema.Document, docID string, hitIdx int64, hitScore float32, window int, seen map[string]bool, expanded []schema.Document) []schema.Document {
	center := -1
	for ci, c := range ordered {
		if ciIdx, _ := c.Metadata["chunk_index"].(int64); ciIdx == hitIdx {
			center = ci
			break
		}
	}
	if center < 0 {
		return expanded
	}

	lo, hi := windowRange(center, window, len(ordered))
	for ci := lo; ci <= hi; ci++ {
		c := ordered[ci]
		ciIdx, _ := c.Metadata["chunk_index"].(int64)
		key := fmt.Sprintf("doc:%s:%d", docID, ciIdx)
		if seen[key] {
			continue
		}
		seen[key] = true
		// 邻居块与命中块统一沿用命中块 Score，保持同一分层截断口径。
		c.Score = hitScore
		expanded = append(expanded, c)
	}
	return expanded
}

// windowRange 返回以 center 为中心、前后各 window 个相邻块的下标范围 [lo, hi]，
// 并将边界裁剪到合法区间 [0, total-1]（total<=0 时返回 [0, -1] 表示空窗口）。
func windowRange(center, window, total int) (lo, hi int) {
	lo = center - window
	if lo < 0 {
		lo = 0
	}
	hi = center + window
	if hi >= total {
		hi = total - 1
	}
	return lo, hi
}

// normalizeScores 将文档分数 min-max 归一化到 [0,1]（最低分=0、最高分=1），使 RRF 与 reranker 分数可统一比较。
func normalizeScores(docs []schema.Document) {
	if len(docs) <= 1 {
		return
	}
	min, max := docs[0].Score, docs[0].Score
	for _, d := range docs {
		if d.Score < min {
			min = d.Score
		}
		if d.Score > max {
			max = d.Score
		}
	}
	if max <= min {
		return
	}
	for i := range docs {
		docs[i].Score = (docs[i].Score - min) / (max - min)
	}
}

// chunkByScore 按归一化分数分层：高分注入全文截断，低分降级为更短截断。
func chunkByScore(doc schema.Document, cc config.ContextConfig) string {
	if cc.MaxChunkChars <= 0 {
		return doc.PageContent // 未启用截断
	}
	full := truncateChunk(doc.PageContent, cc.MaxChunkChars)
	if cc.RagScoreThreshold <= 0 {
		return full // 未启用分层
	}
	if doc.Score >= cc.RagScoreThreshold {
		return full
	}
	shortMax := cc.MaxChunkChars / 2
	if shortMax <= 0 {
		shortMax = 1
	}
	return truncateChunk(doc.PageContent, shortMax)
}

// truncateChunk 按字符（rune）截断超长片段，避免破坏多字节字符。
func truncateChunk(s string, max int) string {
	if max <= 0 || len(s) <= max {
		return s
	}
	runes := []rune(s)
	if len(runes) <= max {
		return s
	}
	return string(runes[:max]) + "..."
}

// retrieveTopDocs 先做 Milvus 双路召回，再调用 reranker 重排取前 topK。
// 未启用 reranker 时退化为直接召回 topK。
func (p *Pool) retrieveTopDocs(ctx context.Context, query, kbID string, topK int) ([]schema.Document, error) {
	if p.reranker == nil {
		return p.ragStore.SimilaritySearch(ctx, query, kbID, topK)
	}

	// 重排需要更宽的候选集，召回 3 倍候选再由 reranker 收敛到 topK。
	retrieveK := topK * 3
	if retrieveK < 5 {
		retrieveK = 5
	}
	candidates, err := p.ragStore.SimilaritySearch(ctx, query, kbID, retrieveK)
	if err != nil {
		return nil, err
	}
	if len(candidates) == 0 {
		return nil, nil
	}

	docs, err := p.reranker.Rerank(ctx, query, candidates, topK)
	if err != nil {
		log.Printf("[Worker] rerank failed, fallback to recall order: %v", err)
		if len(candidates) > topK {
			return candidates[:topK], nil
		}
		return candidates, nil
	}
	return docs, nil
}

// loadConversationHistory 按 token 预算加载会话历史，并在上下文达到阈值时自动压缩较早轮次为摘要。
// 返回给 LLM 的消息切片：最前是一条「此前对话摘要」system 消息（若已存在摘要），其后为最近保留的原始轮次。
func (p *Pool) loadConversationHistory(ctx context.Context, conversationID string, systemPrompt string, userPrompt string) []openai.ChatCompletionMessage {
	cc := config.AppConfig.Compress
	lc := config.AppConfig.LLM

	// 触发压缩的 token 阈值 = 上下文窗口 × 压缩比例。
	threshold := int(float64(lc.ContextWindow) * lc.CompressThreshold)
	if lc.ContextWindow <= 0 || lc.CompressThreshold <= 0 {
		threshold = 0 // 未配置阈值时不压缩
	}

	// 配置缺省值兜底。
	maxHistory := cc.MaxHistory
	if maxHistory <= 0 {
		maxHistory = 20
	}
	keepRecent := cc.KeepRecent
	if keepRecent <= 0 {
		keepRecent = 6
	}
	minTurns := cc.MinTurns
	if minTurns <= 0 {
		minTurns = keepRecent + 2
	}

	// 已压缩到哪条任务：避免重复取到已被记忆进摘要的旧轮次。
	summary, cutoff := p.manager.GetConversationCompressState(conversationID)

	var turns []*model.Task
	if cc.Enabled {
		turns = p.manager.GetConversationHistorySince(conversationID, cutoff, maxHistory)
	} else {
		turns = p.manager.GetConversationHistory(conversationID, maxHistory)
	}

	// taskToMessages 把一条任务换算为 user/assistant 两个消息。
	taskToMessages := func(ht *model.Task) []openai.ChatCompletionMessage {
		msgs := []openai.ChatCompletionMessage{
			{Role: openai.ChatMessageRoleUser, Content: ht.Payload},
		}
		if ht.Result != "" {
			msgs = append(msgs, openai.ChatCompletionMessage{
				Role:    openai.ChatMessageRoleAssistant,
				Content: ht.Result,
			})
		}
		return msgs
	}

	// buildHistory 组装「摘要 system 消息 + 最近原始轮次」。
	buildHistory := func(summaryText string, recent []*model.Task) []openai.ChatCompletionMessage {
		var hist []openai.ChatCompletionMessage
		if strings.TrimSpace(summaryText) != "" {
			hist = append(hist, openai.ChatCompletionMessage{
				Role:    openai.ChatMessageRoleSystem,
				Content: "以下是此前对话的压缩摘要，请结合它理解上下文，避免与用户重复确认过去已确定的内容：\n" + strings.TrimSpace(summaryText),
			})
		}
		for _, t := range recent {
			hist = append(hist, taskToMessages(t)...)
		}
		return hist
	}

	hist := buildHistory(summary, turns)

	// 未启用压缩、或阈值未配置：直接返回原始历史。
	if !cc.Enabled || threshold <= 0 {
		return hist
	}

	used := llm.EstimateMessagesTokens(systemPrompt, hist, userPrompt)
	if used <= threshold {
		return hist
	}

	// 全部轮次都在保留窗口内（无需/无法压缩），保持原样。
	if len(turns) <= keepRecent || len(turns) < minTurns {
		return hist
	}

	// 需要压缩：把最早的 (len(turns)-keepRecent) 轮压缩为摘要，仅保留最近 keepRecent 轮原始消息。
	split := len(turns) - keepRecent
	toCompress := turns[:split]
	recent := turns[split:]

	var toCompressMsgs []openai.ChatCompletionMessage
	for _, t := range toCompress {
		toCompressMsgs = append(toCompressMsgs, taskToMessages(t)...)
	}

	metrics.CompressTotal.Inc()
	newSummary, err := p.llmClient.CompressHistory(ctx, "", summary, toCompressMsgs)
	if err != nil {
		// 压缩失败属可告警信号：静默回退全量历史会放大后续 token 成本，需能被观测/告警。
		metrics.CompressFailed.Inc()
		log.Printf("[Worker][ALERT] conversation compress failed, keeping full history: %v", err)
		return hist
	}

	// 持久化摘要与 cutoff（cutoff 指向被压缩的最后一轮的创建时间）。
	cutoffTime := toCompress[len(toCompress)-1].CreatedAt
	if err := p.manager.SaveConversationSummary(conversationID, newSummary, &cutoffTime); err != nil {
		log.Printf("[Worker] failed to persist conversation summary: %v", err)
	}

	log.Printf("[Worker] conversation %s compressed %d historic turns into summary", conversationID, len(toCompress))
	return buildHistory(newSummary, recent)
}

// handleTaskProcess 是 Asynq 路由的处理函数，负责处理实际的 AI 任务。
func (p *Pool) handleTaskProcess(ctx context.Context, asynqTask *asynq.Task) error {
	var t model.Task
	if err := json.Unmarshal(asynqTask.Payload(), &t); err != nil {
		return fmt.Errorf("json.Unmarshal failed: %v", err)
	}

	latestTask, ok := p.manager.GetTask(t.ID)
	if ok {
		if latestTask.Status == model.StatusCancelled {
			log.Printf("[Worker] Task %s was cancelled before processing, skipping", t.ID)
			return nil
		}
		// 以 DB 最新状态为准覆盖 payload 旧值：asynq 重试时复用原始 payload，
		// 其中的 Retries 不会随每次重试递增，若不覆盖，业务级 MaxRetry 将失效。
		t.Retries = latestTask.Retries
		if latestTask.MaxRetry > 0 {
			t.MaxRetry = latestTask.MaxRetry
		}
	}

	t.Status = model.StatusProcessing
	if !p.manager.UpdateTaskIfActive(&t) {
		log.Printf("[Worker] Task %s was cancelled before processing, skipping", t.ID)
		return nil
	}

	log.Printf("[Worker] Processing task %s (%s)", t.ID, t.Type)

	// 提前解析本次任务可用的工具，用于决定是否注入「强制联网搜索」指令。
	tools := p.resolveTools(ctx, t)

	// 运行级成本预算：单次运行模型调用次数上限（防工具乒乓）+ 任务级总上限（跨重试累计）+ token 计量 + CostUSD。
	// 提前构造，使检索阶段的关联度过滤 LLM 调用同样纳入计量。
	maxCalls := config.AppConfig.LLM.MaxCallsPerTask
	if maxCalls <= 0 {
		maxCalls = 15
		log.Printf("[Worker] max_calls_per_task not configured, using default %d", maxCalls)
	}
	maxTotalCalls := config.AppConfig.LLM.MaxTotalCallsPerTask
	if maxTotalCalls <= 0 {
		maxTotalCalls = maxCalls * 2
		log.Printf("[Worker] max_total_calls_per_task not configured, using default %d", maxTotalCalls)
	}
	budget := &llm.Budget{
		MaxCalls:           maxCalls,
		MaxTotalCalls:      maxTotalCalls,
		PriceInUSDPerMTok:  config.AppConfig.LLM.CostPer1MIn,
		PriceOutUSDPerMTok: config.AppConfig.LLM.CostPer1MOut,
	}
	if usageTask, ok := p.manager.GetTask(t.ID); ok {
		budget.SetBase(usageTask.TotalCalls, usageTask.TotalTokensIn, usageTask.TotalTokensOut)
	}

	// 支持每个 Agent 指定自己的模型与云厂商接口：优先使用 Agent 配置的 api_key/base_url/model，为空则回退全局默认。
	// 提前解析：意图路由的 LLM 分类需要与主生成使用同一套凭证。
	var agentAPIKey, agentBaseURL, agentModel string
	if t.AgentID != "" {
		if agent, ok := p.manager.GetAgent(t.AgentID); ok {
			agentAPIKey = agent.APIKey
			agentBaseURL = agent.BaseURL
			agentModel = agent.Model
		}
	}

	// 意图路由：判断本次任务是否需要执行 RAG/KAG 检索（成本优化）。
	start := time.Now()
	decision := p.routeIntent(ctx, t, tools, budget, agentAPIKey, agentBaseURL, agentModel)

	systemPrompt := t.SystemPrompt
	if systemPrompt == "" {
		systemPrompt = "你是一个有用的 AI 助手。请尽力解答用户的问题或完成用户指定的任务。"
	}

	// 仅当实际绑定了 internet_search 工具、且意图非纯寒暄时才注入强制搜索指令：
	// 对寒暄类问题注入「必须搜索」会误导模型；其余意图保持原有行为。
	if p.mcpClient != nil && decision.Intent != intent.IntentChat && hasToolName(tools, "internet_search") {
		systemPrompt += `

[特别指示 - 联网工具强制使用规范]:
1. 【必须搜索】：当用户询问客观事实、新闻、人物信息、作品年代等你不确定或需要查证的内容时，你**必须**首先调用 'internet_search' 工具进行谷歌搜索。
2. 【调用格式】：为了保证系统能正确执行搜索，如果你决定调用工具，请在回复中**仅输出工具调用的 JSON 格式**（可放在代码块内或直接输出），不要包含任何其他分析或废话。格式必须为：
{"name": "internet_search", "arguments": {"query": "你要搜索的具体关键词"}}
3. 【禁止抢答】：在系统实际返回搜索结果之前，**绝对不要**直接回答问题，也**绝对不要**提前说“抱歉，由于网络问题我没有找到相关信息”。只有当你看到了工具返回的具体内容后，才能根据内容作答。
4. 【严格基于结果】：只有当你看到搜索结果后，再依据搜索到的内容整理答案。如果工具真的返回了“未找到结果”，你才可以向用户道歉。`
	}

	var kbContext string
	if decision.NeedRAG {
		kbContext = p.retrieveKnowledge(ctx, t)
	}
	if kbContext != "" {
		systemPrompt += fmt.Sprintf(`

[系统内部提供的本地知识库参考信息]:
以下是从系统内部知识库中检索到的可能相关的背景信息片段。请优先参考这些信息来回答用户的问题。
如果你发现这些片段与用户的问题无关，请忽略它们，并继续根据你的常识（或调用搜索引擎）进行回答。

<knowledge_base_context>
%s
</knowledge_base_context>`, kbContext)
	}

	if p.kagManager != nil && decision.NeedKAG {
		kagResult, err := p.kagManager.RetrieveGraphContext(ctx, t.Payload, config.AppConfig.Context.GraphTopN)
		if err == nil && kagResult != "" {
			// KAG 关联度过滤：仅注入与问题相关的图谱上下文，降低无关实体关系噪声。
			// 该次判定纳入 budget 计量，避免绕过任务级调用次数与成本上限。
			if relevant, rerr := p.llmClient.IsContextRelevant(ctx, t.Payload, kagResult, budget); rerr == nil && !relevant {
				kagResult = ""
			}
			// 联合总量约束：图谱上下文与知识库上下文共享 max_context_chars 总量，
			// 按 RAG 已用剩余量截断，避免两路叠加突破上限。
			if kagResult != "" {
				remaining := config.AppConfig.Context.MaxContextChars
				if remaining > 0 {
					remaining -= len(kbContext)
					if remaining <= 0 {
						kagResult = ""
					}
				}
				if kagResult != "" {
					kagResult = truncateChunk(kagResult, remaining)
					systemPrompt += fmt.Sprintf(`

[系统内部提供的本地知识图谱参考信息]:
以下是从系统内部知识图谱中通过逻辑推理检索到的相关实体关系信息。请优先参考这些确切的关系信息来回答用户的问题。
如果你发现这些信息与用户的问题无关，请忽略它们。

<knowledge_graph_context>
%s
</knowledge_graph_context>`, kagResult)
				}
			}
		}
	}

	var historyMessages []openai.ChatCompletionMessage
	conversationID := t.ConversationID
	if conversationID == "" {
		conversationID = t.SessionID // 兼容旧数据：无 ConversationID 时回退 SessionID
	}
	if conversationID != "" {
		// 按 token 预算加载会话历史；当上下文达到窗口阈值时自动压缩较早轮次为摘要。
		historyMessages = p.loadConversationHistory(ctx, conversationID, systemPrompt, t.Payload)
	}

	// 可取消 ctx 派生：以 asynq 传入的 ctx 为父（而非 context.Background()），
	// 并叠加按配置的超时。取消控制指令或 asynq 取消都会中断进行中的 LLM 调用。
	timeout := 2 * time.Minute
	if s := config.AppConfig.LLM.TimeoutSeconds; s > 0 {
		timeout = time.Duration(s) * time.Second
	}
	apiCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	p.registerInFlight(t.ID, cancel)
	defer p.unregisterInFlight(t.ID)

	observer := func(obs llm.ToolCallObservation) {
		ev := &model.ToolCallEvent{
			TaskID:    t.ID,
			TraceID:   t.TraceID,
			UserID:    t.UserID,
			AgentID:   t.AgentID,
			ToolName:  obs.ToolName,
			Arguments: obs.Arguments,
			Status:    obs.Status,
			Result:    obs.Result,
			Error:     obs.Error,
			Timestamp: time.Now(),
		}
		metrics.ToolCalls.WithLabelValues(obs.Status).Inc()
		p.manager.PublishToolEvent(ev)
		if err := p.manager.SaveToolCallLog(&model.ToolCallLog{
			ID:        uuid.New().String(),
			TaskID:    t.ID,
			TraceID:   t.TraceID,
			UserID:    t.UserID,
			AgentID:   t.AgentID,
			ToolName:  obs.ToolName,
			Arguments: obs.Arguments,
			Status:    obs.Status,
			Result:    obs.Result,
			Error:     obs.Error,
			CreatedAt: time.Now(),
		}); err != nil {
			log.Printf("[Worker][ALERT] failed to persist tool call log for task %s: %v", t.ID, err)
		}
	}

	result, err := p.llmClient.GenerateWithToolsAndObserver(apiCtx, agentAPIKey, agentBaseURL, agentModel, systemPrompt, historyMessages, t.Payload, tools, observer, budget)

	checkTask, ok := p.manager.GetTask(t.ID)
	if ok && checkTask.Status == model.StatusCancelled {
		log.Printf("[Worker] Task %s was cancelled during API call, aborting", t.ID)
		p.saveRunLog(&t, model.StatusCancelled, "", budget, start, "cancelled during API call")
		return nil
	}

	if err != nil {
		if errors.Is(err, llm.ErrBudgetExceeded) {
			// 预算耗尽属不可重试错误：重试只会继续被累计上限拦截，直接置 failed。
			t.Status = model.StatusFailed
			t.Error = fmt.Sprintf("budget exceeded: %v", err)
			p.manager.UpdateTaskIfActive(&t)
			log.Printf("[Worker] Task %s budget exceeded, permanently failing", t.ID)
			p.saveRunLog(&t, model.StatusFailed, "", budget, start, t.Error)
			return nil
		}

		log.Printf("[Worker] Task %s AI call failed: %v", t.ID, err)
		if t.Retries < t.MaxRetry {
			t.Retries++
			metrics.TaskRetries.Inc()
			t.Status = model.StatusPending
			t.Error = fmt.Sprintf("AI API error, retrying %d/%d: %v", t.Retries, t.MaxRetry, err)
			if !p.manager.UpdateTaskIfActive(&t) {
				log.Printf("[Worker] Task %s cancelled concurrently, dropping retry", t.ID)
				p.saveRunLog(&t, model.StatusCancelled, "", budget, start, t.Error)
				return nil
			}
			p.saveRunLog(&t, model.StatusPending, "", budget, start, t.Error)
			return err
		} else {
			t.Status = model.StatusFailed
			t.Error = fmt.Sprintf("Failed after %d retries. Last error: %v", t.MaxRetry, err)
			p.manager.UpdateTaskIfActive(&t)
			log.Printf("[Worker] Task %s permanently failed", t.ID)
			p.saveRunLog(&t, model.StatusFailed, "", budget, start, t.Error)
			return nil
		}
	}

	t.Status = model.StatusCompleted
	t.Result = result
	t.Error = ""
	if !p.manager.UpdateTaskIfActive(&t) {
		log.Printf("[Worker] Task %s cancelled concurrently, dropping result", t.ID)
		p.saveRunLog(&t, model.StatusCancelled, "", budget, start, "cancelled after completion")
		return nil
	}
	log.Printf("[Worker] Completed task %s successfully", t.ID)
	p.saveRunLog(&t, model.StatusCompleted, result, budget, start, "")

	return nil
}

// saveRunLog 累加任务计量并写入一次任务运行的观测记录。
func (p *Pool) saveRunLog(t *model.Task, status model.TaskStatus, result string, budget *llm.Budget, start time.Time, errMsg string) {
	// 跨重试累计：把本次运行的调用/token 计量原子累加到任务累计字段。
	p.manager.AccumulateTaskUsage(t.ID, budget.Calls, budget.TokensIn, budget.TokensOut)

	// Prometheus 指标：任务状态/耗时、模型调用/token/成本（task:process 队列）。
	statusStr := string(status)
	metrics.TaskTotal.WithLabelValues(statusStr).Inc()
	metrics.TaskDuration.WithLabelValues(statusStr).Observe(time.Since(start).Seconds())
	metrics.LLMCalls.WithLabelValues("task:process").Add(float64(budget.Calls))
	metrics.LLMTokens.WithLabelValues("task:process", "in").Add(float64(budget.TokensIn))
	metrics.LLMTokens.WithLabelValues("task:process", "out").Add(float64(budget.TokensOut))
	metrics.LLMCost.WithLabelValues("task:process").Add(budget.CostUSD())

	modelName := config.AppConfig.LLM.Model
	if t.AgentID != "" {
		if agent, ok := p.manager.GetAgent(t.AgentID); ok && agent.Model != "" {
			modelName = agent.Model
		}
	}
	if err := p.manager.SaveRunLog(&model.RunLog{
		ID:        uuid.New().String(),
		TaskID:    t.ID,
		TraceID:   t.TraceID,
		UserID:    t.UserID,
		AgentID:   t.AgentID,
		Model:     modelName,
		Status:    string(status),
		Calls:     budget.Calls,
		TokensIn:  budget.TokensIn,
		TokensOut: budget.TokensOut,
		CostUSD:   budget.CostUSD(),
		Duration:  time.Since(start).Milliseconds(),
		Error:     errMsg,
		CreatedAt: time.Now(),
	}); err != nil {
		log.Printf("[Worker][ALERT] failed to persist run log for task %s: %v", t.ID, err)
	}
}

// handleKBBuild 处理知识库文档异步构建任务。
func (p *Pool) handleKBBuild(ctx context.Context, t *asynq.Task) error {
	var payload struct {
		KBID  string `json:"kb_id"`
		DocID string `json:"doc_id"`
	}
	if err := json.Unmarshal(t.Payload(), &payload); err != nil {
		return fmt.Errorf("invalid kb:build payload: %w", err)
	}

	doc, ok := p.manager.GetDocument(payload.DocID)
	if !ok {
		return fmt.Errorf("document %s not found", payload.DocID)
	}

	// 记录构建前的状态，用于避免文档重建时重复累加 kb.doc_count。
	wasReady := doc.Status == "ready"

	doc.Status = "processing"
	_ = p.manager.UpdateDocument(doc)

	if p.ragStore == nil {
		doc.Status = "failed"
		_ = p.manager.UpdateDocument(doc)
		metrics.KBBuildTotal.WithLabelValues("failed").Inc()
		return fmt.Errorf("rag store unavailable")
	}

	// 构建期生成 per-doc 摘要（摘要层），供运行期渐进式披露注入，避免运行期临时调 LLM 生成摘要。
	if doc.Summary == "" {
		summaryCtx, summaryCancel := context.WithTimeout(ctx, 2*time.Minute)
		sumBudget := &llm.Budget{
			PriceInUSDPerMTok:  config.AppConfig.LLM.CostPer1MIn,
			PriceOutUSDPerMTok: config.AppConfig.LLM.CostPer1MOut,
		}
		summary, serr := p.llmClient.SummarizeText(summaryCtx, doc.Content, sumBudget)
		summaryCancel()
		// 计量 kb:build 队列成本（LLM 摘要），与 task:process 队列分开观测。
		metrics.LLMCalls.WithLabelValues("kb:build").Add(float64(sumBudget.Calls))
		metrics.LLMTokens.WithLabelValues("kb:build", "in").Add(float64(sumBudget.TokensIn))
		metrics.LLMTokens.WithLabelValues("kb:build", "out").Add(float64(sumBudget.TokensOut))
		metrics.LLMCost.WithLabelValues("kb:build").Add(sumBudget.CostUSD())
		if serr != nil {
			log.Printf("[Worker][ALERT] failed to summarize document %s: %v", doc.ID, serr)
			// 摘要生成失败不阻断入库：仅缺失摘要层，命中层仍可用。
		} else {
			doc.Summary = summary
		}
	}

	splitter := textsplitter.NewRecursiveCharacter()
	splitter.ChunkSize = 500
	splitter.ChunkOverlap = 50

	chunks, err := textsplitter.SplitDocuments(splitter, []schema.Document{
		{
			PageContent: doc.Content,
			Metadata: map[string]any{
				"kb_id":  payload.KBID,
				"doc_id": doc.ID,
			},
		},
	})
	if err != nil {
		doc.Status = "failed"
		_ = p.manager.UpdateDocument(doc)
		metrics.KBBuildTotal.WithLabelValues("failed").Inc()
		return err
	}

	// 为每个 chunk 打上其在原文档中的有序位置序号（chunk_index），
	// 供运行期按 doc_id + chunk_index 做窗口扩展（上下文感知分块 B1 主路径）。
	for i := range chunks {
		if chunks[i].Metadata == nil {
			chunks[i].Metadata = map[string]any{}
		}
		chunks[i].Metadata["chunk_index"] = i
	}

	if _, err := p.ragStore.AddDocuments(ctx, chunks); err != nil {
		doc.Status = "failed"
		_ = p.manager.UpdateDocument(doc)
		metrics.KBBuildTotal.WithLabelValues("failed").Inc()
		return err
	}

	doc.Status = "ready"
	doc.ChunkCount = len(chunks)
	// 图谱阶段（W1）：失败不回滚向量成果，仅记录 graph_status；开关关闭或无 KAG 时置 skipped。
	doc.GraphStatus = p.runGraphStage(ctx, doc)
	_ = p.manager.UpdateDocument(doc)
	metrics.KBBuildTotal.WithLabelValues("ready").Inc()

	// 原子自增文档计数（避免读-改-写竞态导致并发丢失更新）；文档重建时通过 wasReady 防止重复累加。
	if !wasReady {
		if !p.manager.IncrementKnowledgeBaseDocCount(payload.KBID) {
			log.Printf("[Worker] KB %s not found while incrementing doc_count", payload.KBID)
		}
	}

	log.Printf("[Worker] KB %s document %s built with %d chunks", payload.KBID, payload.DocID, len(chunks))
	return nil
}

// runGraphStage 执行建库的图谱阶段（W1）：本体引导抽取三元组，并带来源文档归属写入图谱。
// 返回该文档的 graph_status：skipped（开关关闭或无 KAG）/ ready / failed。
// 设计要点：
//   - 与向量阶段是两个独立失败域：图谱失败不回滚已入库的向量，仅记录状态，避免整任务重试导致向量重复入库；
//   - 关系以 source_doc 参与 MERGE，同一文档重复建库不会重复建边（幂等），但会重复计费（W3 指纹去重为后续必要前置）；
//   - 抽取使用独立 budget，单独计量到 kb:build 队列，使该阶段成本可观测。
func (p *Pool) runGraphStage(ctx context.Context, doc *model.Document) string {
	if !config.AppConfig.KB.GraphEnabled || p.kagManager == nil {
		return "skipped"
	}

	// 限流：与向量阶段共用 low 队列，再用信号量限制图谱阶段并发，避免抽取调用打满队列。
	if p.graphSem != nil {
		p.graphSem <- struct{}{}
		defer func() { <-p.graphSem }()
	}

	graphCtx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()

	graphBudget := &llm.Budget{
		PriceInUSDPerMTok:  config.AppConfig.LLM.CostPer1MIn,
		PriceOutUSDPerMTok: config.AppConfig.LLM.CostPer1MOut,
	}

	relations, err := p.kagManager.ExtractKnowledgeWithBudget(graphCtx, doc.Content, graphBudget)
	metrics.LLMCalls.WithLabelValues("kb:build").Add(float64(graphBudget.Calls))
	metrics.LLMTokens.WithLabelValues("kb:build", "in").Add(float64(graphBudget.TokensIn))
	metrics.LLMTokens.WithLabelValues("kb:build", "out").Add(float64(graphBudget.TokensOut))
	metrics.LLMCost.WithLabelValues("kb:build").Add(graphBudget.CostUSD())
	if err != nil {
		log.Printf("[Worker][ALERT] graph extraction failed for document %s: %v", doc.ID, err)
		return "failed"
	}

	if len(relations) == 0 {
		log.Printf("[Worker] document %s extracted no graph relations", doc.ID)
		return "ready"
	}

	if err := p.kagManager.IngestGraphWithSource(graphCtx, relations, doc.ID); err != nil {
		log.Printf("[Worker][ALERT] graph ingest failed for document %s: %v", doc.ID, err)
		return "failed"
	}

	log.Printf("[Worker] document %s graphed: %d relations", doc.ID, len(relations))
	return "ready"
}

// handleKBDelete 处理知识库删除任务，执行三段式失效清理：Milvus → Neo4j → MySQL。
// MySQL 的文档行与 KB 行最后删除，任一步失败即返回错误由 Asynq 重试，保证可重入、不产生幽灵数据。
func (p *Pool) handleKBDelete(ctx context.Context, t *asynq.Task) error {
	var payload struct {
		KBID string `json:"kb_id"`
	}
	if err := json.Unmarshal(t.Payload(), &payload); err != nil {
		return fmt.Errorf("invalid kb:delete payload: %w", err)
	}
	if payload.KBID == "" {
		return fmt.Errorf("kb:delete payload missing kb_id")
	}

	// 先取文档 ID：MySQL 行删除后无法再定位图谱来源，故必须在清理前读取。
	docIDs := p.manager.ListDocumentIDsByKB(payload.KBID)

	// ① 向量库：按 kb_id 批量清理（内部强制 Flush，避免删除后仍被召回）。
	if p.ragStore != nil {
		if err := p.ragStore.DeleteByKBID(ctx, payload.KBID); err != nil {
			return fmt.Errorf("delete vectors for kb %s: %w", payload.KBID, err)
		}
	}

	// ② 图谱：按来源文档清理关系与由此产生的孤立实体节点。
	if p.kagManager != nil {
		for _, docID := range docIDs {
			if _, err := p.kagManager.DeleteBySourceDoc(ctx, docID); err != nil {
				return fmt.Errorf("delete graph for doc %s: %w", docID, err)
			}
		}
	}

	// ③ MySQL：删除文档行，最后删除知识库行。
	if err := p.manager.DeleteDocumentsByKB(payload.KBID); err != nil {
		return fmt.Errorf("delete document rows for kb %s: %w", payload.KBID, err)
	}
	if err := p.manager.DeleteKnowledgeBaseRow(payload.KBID); err != nil {
		return fmt.Errorf("delete kb row %s: %w", payload.KBID, err)
	}

	log.Printf("[Worker] KB %s deleted: vectors pruned, %d source docs graphed out, MySQL rows removed", payload.KBID, len(docIDs))
	return nil
}

// handleDocDelete 处理文档删除任务，执行三段式失效清理：Milvus → Neo4j → MySQL。
// 任一步失败即返回错误由 Asynq 重试；MySQL 行最后删除，重复投递时视为已完成（幂等）。
func (p *Pool) handleDocDelete(ctx context.Context, t *asynq.Task) error {
	var payload struct {
		DocID string `json:"doc_id"`
	}
	if err := json.Unmarshal(t.Payload(), &payload); err != nil {
		return fmt.Errorf("invalid doc:delete payload: %w", err)
	}
	if payload.DocID == "" {
		return fmt.Errorf("doc:delete payload missing doc_id")
	}

	doc, ok := p.manager.GetDocument(payload.DocID)
	if !ok {
		// 已删除（重复投递或上次已跑完）：视为成功，保证幂等。
		return nil
	}

	// ① 向量库：按 doc_id 清理（内部 Flush，避免删除后仍被召回）。
	if p.ragStore != nil {
		if err := p.ragStore.DeleteByDocID(ctx, payload.DocID); err != nil {
			return fmt.Errorf("delete vectors for doc %s: %w", payload.DocID, err)
		}
	}

	// ② 图谱：按来源文档清理关系与孤立实体节点。
	if p.kagManager != nil {
		if _, err := p.kagManager.DeleteBySourceDoc(ctx, payload.DocID); err != nil {
			return fmt.Errorf("delete graph for doc %s: %w", payload.DocID, err)
		}
	}

	// ③ MySQL：删除文档行并递减知识库计数。
	if err := p.manager.DeleteDocumentRow(payload.DocID); err != nil {
		return fmt.Errorf("delete document row %s: %w", payload.DocID, err)
	}
	p.manager.DecrementKnowledgeBaseDocCount(doc.KBID)

	log.Printf("[Worker] document %s deleted (kb %s)", payload.DocID, doc.KBID)
	return nil
}

// handleKBScan 处理周期增量扫描任务（W2）：
// 按配置的扫描源逐个做「指纹比对 → 新建 / 跳过 / 变更重建」，单次窗口最多处理 scan_batch_size 个文件。
// 幂等：指纹相同直接跳过（零成本）；变更重建会先清理旧的向量与图谱边，避免同一文档新旧事实并存。
func (p *Pool) handleKBScan(ctx context.Context, t *asynq.Task) error {
	kc := config.AppConfig.KB

	// 补偿（W3 6.4）：重投因重试耗尽而滞留的删除任务。即使未配置扫描源也执行，保证删除最终一致。
	p.compensateStaleDeleting()

	if len(kc.ScanSources) == 0 {
		log.Printf("[Worker] kb:scan source scan skipped: no scan_sources configured")
		return nil
	}

	remaining := kc.ScanBatchSize // <=0 表示不限制
	created, rebuilt, skipped := 0, 0, 0

	for _, src := range kc.ScanSources {
		if src.KBID == "" || src.Path == "" {
			continue
		}
		if _, ok := p.manager.GetKnowledgeBase(src.KBID); !ok {
			log.Printf("[Worker][ALERT] kb:scan source skipped: knowledge base %s not found", src.KBID)
			continue
		}

		files, err := kbimport.ScanDir(src.Path, remaining)
		if err != nil {
			// 源不可读：记录告警并跳过该源，不影响其他源（启动期不可读同样按此降级）。
			log.Printf("[Worker][ALERT] kb:scan source %s unreadable: %v", src.Path, err)
			continue
		}

		for _, f := range files {
			action, serr := p.syncScannedFile(ctx, src.KBID, f)
			if serr != nil {
				log.Printf("[Worker][ALERT] kb:scan sync %s failed: %v", f.Path, serr)
				continue
			}
			switch action {
			case kbimport.ActionCreate:
				created++
			case kbimport.ActionRebuild:
				rebuilt++
			case kbimport.ActionSkip:
				skipped++
			}
		}

		if remaining > 0 {
			remaining -= len(files)
			if remaining <= 0 {
				break
			}
		}
	}

	log.Printf("[Worker] kb:scan done: created=%d rebuilt=%d skipped=%d", created, rebuilt, skipped)
	return nil
}

// syncScannedFile 依据「库中已有指纹 vs 扫描到的新指纹」同步单个源文件，返回实际执行的动作。
func (p *Pool) syncScannedFile(ctx context.Context, kbID string, f kbimport.File) (kbimport.Action, error) {
	existing, found := p.manager.GetDocumentBySource(kbID, f.Path)
	existingHash := ""
	if found {
		existingHash = existing.ContentHash
	}

	switch action := kbimport.Decide(existingHash, f.Hash); action {
	case kbimport.ActionSkip:
		return action, nil

	case kbimport.ActionCreate:
		doc := &model.Document{
			ID:          uuid.New().String(),
			KBID:        kbID,
			Title:       filepath.Base(f.Path),
			Content:     f.Content,
			ContentHash: f.Hash,
			SourcePath:  f.Path,
			Status:      "pending",
			GraphStatus: "pending",
			CreatedAt:   time.Now(),
		}
		if err := p.manager.CreateDocument(doc); err != nil {
			return action, err
		}
		if err := p.manager.EnqueueKBBuild(kbID, doc.ID); err != nil {
			return action, err
		}
		return action, nil

	default: // ActionRebuild：内容变更，先失效清理再重建。
		// 正在删除或已在队列中：本轮跳过，避免重复入队（R3 手动上传与扫描并发）。
		switch existing.Status {
		case "deleting", "processing", "pending":
			return kbimport.ActionSkip, nil
		}
		// 体积校验必须前置到下面两次删除之前：正文超出存储上限时 UpdateDocument 必然失败，
		// 而此时 Milvus 向量与 Neo4j 边已被清空，会留下「状态仍为 ready、实际搜不到」的幽灵文档。
		// 失败置为 failed 而非静默保留旧状态，使该文档在列表中可见。
		if len(f.Content) > kbimport.MaxFileSize {
			existing.Status = "failed"
			if uerr := p.manager.UpdateDocument(existing); uerr != nil {
				log.Printf("[Worker][ALERT] mark oversized document %s failed: %v", existing.ID, uerr)
			}
			return kbimport.ActionSkip, fmt.Errorf("kbimport: content too large for document %s: %d bytes (limit %d)", existing.ID, len(f.Content), kbimport.MaxFileSize)
		}
		if p.ragStore != nil {
			if err := p.ragStore.DeleteByDocID(ctx, existing.ID); err != nil {
				return action, err
			}
		}
		if p.kagManager != nil {
			if _, err := p.kagManager.DeleteBySourceDoc(ctx, existing.ID); err != nil {
				return action, err
			}
		}
		existing.Content = f.Content
		existing.ContentHash = f.Hash
		existing.Summary = "" // 内容已变，摘要层需重建
		existing.ChunkCount = 0
		existing.Status = "pending"
		existing.GraphStatus = "pending"
		if err := p.manager.UpdateDocument(existing); err != nil {
			return action, err
		}
		if err := p.manager.EnqueueKBBuild(kbID, existing.ID); err != nil {
			return action, err
		}
		return action, nil
	}
}

// 删除补偿参数：删除行超过 staleDeletingGrace 仍未清完，说明任务重试已耗尽，由周期扫描重投。
const (
	staleDeletingGrace = 10 * time.Minute
	staleDeletingBatch = 200
)

// compensateStaleDeleting 重投滞留的删除任务（W3 6.4 周期补偿）。
// 只重投仍处于 deleting 状态且已超过宽限期的行；doc:delete / kb:delete 本身幂等，重复重投无副作用。
func (p *Pool) compensateStaleDeleting() {
	cutoff := time.Now().Add(-staleDeletingGrace)

	docIDs := p.manager.ListStaleDeletingDocumentIDs(cutoff, staleDeletingBatch)
	for _, id := range docIDs {
		if err := p.manager.EnqueueDocDelete(id); err != nil {
			log.Printf("[Worker][ALERT] requeue stale deleting document %s failed: %v", id, err)
		}
	}

	kbIDs := p.manager.ListStaleDeletingKnowledgeBaseIDs(cutoff, staleDeletingBatch)
	for _, id := range kbIDs {
		if err := p.manager.EnqueueKBDelete(id); err != nil {
			log.Printf("[Worker][ALERT] requeue stale deleting knowledge base %s failed: %v", id, err)
		}
	}

	if len(docIDs) > 0 || len(kbIDs) > 0 {
		log.Printf("[Worker] delete compensation: requeued %d document(s), %d knowledge base(s)", len(docIDs), len(kbIDs))
	}
}
