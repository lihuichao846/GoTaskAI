package api

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"os"
	"sort"
	"strings"
	"time"

	"gotaskai/internal/model"
	"gotaskai/internal/pkg/llm"
	"gotaskai/internal/pkg/mcpclient"
	"gotaskai/internal/queue"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

// ToolHandler 处理工具的注册、列表、删除与 MCP 工具发现。
type ToolHandler struct {
	manager   *queue.TaskManager
	llmClient *llm.Client
}

func NewToolHandler(manager *queue.TaskManager, llmClient *llm.Client) *ToolHandler {
	return &ToolHandler{manager: manager, llmClient: llmClient}
}

// ToolRequest 定义注册 MCP 工具的请求体。
type ToolRequest struct {
	Name        string `json:"name" binding:"required"`
	Type        string `json:"type" binding:"required"` // mcp
	DisplayName string `json:"display_name"`
	Description string `json:"description"`
	Config      string `json:"config"` // JSON：{"command":"...","args":[...],"url":"...","env":{...}}
}

// CreateTool 处理 POST /api/tools，注册用户私有 MCP 工具。
func (h *ToolHandler) CreateTool(c *gin.Context) {
	var req ToolRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	if req.Type != "mcp" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "仅支持注册 mcp 类型工具"})
		return
	}

	// 校验 config 为合法 JSON
	if !json.Valid([]byte(req.Config)) {
		c.JSON(http.StatusBadRequest, gin.H{"error": "config 必须是合法 JSON"})
		return
	}

	// 校验 config 提供了可用的连接方式
	var cfg model.ToolConfig
	if err := json.Unmarshal([]byte(req.Config), &cfg); err != nil || !cfg.Valid() {
		c.JSON(http.StatusBadRequest, gin.H{"error": "config 必须包含 command 或 url"})
		return
	}

	tool := &model.Tool{
		ID:          uuid.New().String(),
		UserID:      c.GetUint("userID"),
		Name:        req.Name,
		Type:        req.Type,
		DisplayName: req.DisplayName,
		Description: req.Description,
		Config:      req.Config,
		IsBuiltin:   false,
		Status:      "active",
		CreatedAt:   time.Now(),
		UpdatedAt:   time.Now(),
	}

	if err := h.manager.CreateTool(tool); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "工具注册失败"})
		return
	}

	c.JSON(http.StatusOK, gin.H{"message": "工具注册成功", "tool": tool})
}

// ListTools 处理 GET /api/tools，返回内置 + 用户私有工具。
func (h *ToolHandler) ListTools(c *gin.Context) {
	tools := h.manager.ListTools(c.GetUint("userID"))
	c.JSON(http.StatusOK, tools)
}

// DeleteTool 处理 DELETE /api/tools/:id，删除用户私有工具。
func (h *ToolHandler) DeleteTool(c *gin.Context) {
	id := c.Param("id")
	tool, ok := h.manager.GetTool(id)
	if !ok {
		c.JSON(http.StatusNotFound, gin.H{"error": "工具不存在"})
		return
	}
	if tool.IsBuiltin || tool.UserID != c.GetUint("userID") {
		c.JSON(http.StatusForbidden, gin.H{"error": "无权删除此工具"})
		return
	}

	if err := h.manager.DeleteTool(id); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "工具删除失败"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"message": "工具已删除"})
}

// UpdateTool 处理 PUT /api/tools/:id，编辑用户私有工具。
func (h *ToolHandler) UpdateTool(c *gin.Context) {
	id := c.Param("id")
	tool, ok := h.manager.GetTool(id)
	if !ok {
		c.JSON(http.StatusNotFound, gin.H{"error": "工具不存在"})
		return
	}
	if tool.IsBuiltin || tool.UserID != c.GetUint("userID") {
		c.JSON(http.StatusForbidden, gin.H{"error": "无权编辑此工具"})
		return
	}

	var req ToolRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	if req.Type != "mcp" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "仅支持编辑 mcp 类型工具"})
		return
	}
	if !json.Valid([]byte(req.Config)) {
		c.JSON(http.StatusBadRequest, gin.H{"error": "config 必须是合法 JSON"})
		return
	}
	var cfg model.ToolConfig
	if err := json.Unmarshal([]byte(req.Config), &cfg); err != nil || !cfg.Valid() {
		c.JSON(http.StatusBadRequest, gin.H{"error": "config 必须包含 command 或 url"})
		return
	}

	tool.Name = req.Name
	tool.DisplayName = req.DisplayName
	tool.Description = req.Description
	tool.Config = req.Config
	if err := h.manager.UpdateTool(tool); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "工具编辑失败"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"message": "工具已更新", "tool": tool})
}

// UpdateToolStatus 处理 PATCH /api/tools/:id/status，启用/停用工具。
func (h *ToolHandler) UpdateToolStatus(c *gin.Context) {
	id := c.Param("id")
	tool, ok := h.manager.GetTool(id)
	if !ok {
		c.JSON(http.StatusNotFound, gin.H{"error": "工具不存在"})
		return
	}
	if tool.IsBuiltin || tool.UserID != c.GetUint("userID") {
		c.JSON(http.StatusForbidden, gin.H{"error": "无权修改此工具"})
		return
	}

	var req struct {
		Status string `json:"status" binding:"required"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	if req.Status != "active" && req.Status != "disabled" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "status 只能为 active 或 disabled"})
		return
	}

	if err := h.manager.UpdateToolStatus(id, req.Status); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "工具状态更新失败"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"message": "工具状态已更新", "status": req.Status})
}

// ListToolAgents 处理 GET /api/tools/:id/agents，返回绑定该工具的 Agent 列表。
func (h *ToolHandler) ListToolAgents(c *gin.Context) {
	id := c.Param("id")
	if _, ok := h.manager.GetTool(id); !ok {
		c.JSON(http.StatusNotFound, gin.H{"error": "工具不存在"})
		return
	}
	agents := h.manager.ListToolAgents(id)
	if agents == nil {
		agents = []*model.Agent{}
	}
	c.JSON(http.StatusOK, agents)
}

// TestToolRequest 定义测试 MCP 连接配置的请求体。
type TestToolRequest struct {
	Config string `json:"config" binding:"required"`
}

// TestToolConnection 处理 POST /api/tools/test，验证 MCP 配置并列出服务端暴露的工具。
func (h *ToolHandler) TestToolConnection(c *gin.Context) {
	var req TestToolRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	if !json.Valid([]byte(req.Config)) {
		c.JSON(http.StatusBadRequest, gin.H{"error": "config 必须是合法 JSON"})
		return
	}
	var cfg model.ToolConfig
	if err := json.Unmarshal([]byte(req.Config), &cfg); err != nil || !cfg.Valid() {
		c.JSON(http.StatusBadRequest, gin.H{"error": "config 必须包含 command 或 url"})
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	tools, err := testMCPConfig(ctx, cfg)
	if err != nil {
		c.JSON(http.StatusOK, gin.H{"ok": false, "error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"ok": true, "tools": tools})
}

// testMCPConfig 按配置建立 MCP 连接并返回服务端暴露的工具名称列表。
func testMCPConfig(ctx context.Context, cfg model.ToolConfig) ([]string, error) {
	var wrapper *mcpclient.Wrapper
	var err error
	if cfg.URL != "" {
		wrapper, err = mcpclient.NewHTTPWrapper(ctx, cfg.URL)
	} else {
		wrapper, err = mcpclient.NewWrapperWithEnv(ctx, cfg.Command, cfg.Env, cfg.Args...)
	}
	if err != nil {
		return nil, err
	}
	defer wrapper.Close()

	tools, err := wrapper.GetTools(ctx)
	if err != nil {
		return nil, err
	}

	names := make([]string, 0, len(tools))
	for _, t := range tools {
		if t.Name != "" {
			names = append(names, t.Name)
		}
	}
	return names, nil
}

// ===== GitHub/npm MCP 工具发现 =====

// SearchToolsRequest 定义 MCP 工具搜索请求体。
type SearchToolsRequest struct {
	Query string `json:"query" binding:"required"` // 自然语言描述，例如「一个能查天气的 mcp 工具」
}

// mcpSearchIntent 是大模型解析出的搜索意图。
type mcpSearchIntent struct {
	Keywords    string `json:"keywords"`    // GitHub 搜索关键词（英文，空格分隔）
	NpmSearch   string `json:"npm_search"`  // npm 搜索关键词（英文）
	Description string `json:"description"` // 对所需工具功能的描述
}

// mcpDiscoveryItem 是一个可注册的候选 MCP 工具。
type mcpDiscoveryItem struct {
	Name           string         `json:"name"`
	DisplayName    string         `json:"display_name"`
	Description    string         `json:"description"`
	RepoURL        string         `json:"repo_url"`
	Stars          int            `json:"stars"`
	Source         string         `json:"source"` // github / npm
	InstallCommand string         `json:"install_command"`
	Config         map[string]any `json:"config"` // 可直接用于注册的连接配置
}

// SearchMCPServers 处理 POST /api/tools/search。
// 流程：自然语言 -> 大模型解析搜索意图 -> 调用 GitHub/npm 搜索候选 MCP 工具。
func (h *ToolHandler) SearchMCPServers(c *gin.Context) {
	var req SearchToolsRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	intent, err := h.parseMCPSearchIntent(ctx, req.Query)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "搜索意图解析失败: " + err.Error()})
		return
	}

	items := make([]mcpDiscoveryItem, 0, 20)
	items = append(items, h.searchNPM(ctx, intent.NpmSearch)...)
	items = append(items, h.searchGitHub(ctx, intent.Keywords)...)

	// 按 star 数排序，npm 结果 star 计为 0 时排在前面展示。
	sort.SliceStable(items, func(i, j int) bool {
		return items[i].Stars > items[j].Stars
	})

	c.JSON(http.StatusOK, gin.H{
		"parsed": intent,
		"items":  items,
	})
}

// parseMCPSearchIntent 调用大模型将自然语言解析为结构化的搜索意图。
func (h *ToolHandler) parseMCPSearchIntent(ctx context.Context, query string) (*mcpSearchIntent, error) {
	system := `你是一个 MCP (Model Context Protocol) 工具检索助手。用户会用自然语言描述他需要一个什么功能的 MCP 工具。
请解析这段描述，并严格按以下 JSON 格式输出（不要输出任何多余文字、不要使用代码块）：
{"keywords": "用于 GitHub 仓库搜索的英文关键词，用空格分隔", "npm_search": "用于在 npm 上搜索 mcp 包的英文关键词", "description": "对该工具功能的一句话描述"}
示例：用户说「查天气的mcp工具」，你应输出 {"keywords":"weather mcp server","npm_search":"mcp server weather","description":"查询天气数据的 MCP 工具"}`

	resp, err := h.llmClient.Generate(ctx, system, nil, query, nil)
	if err != nil {
		return nil, err
	}

	// 容错提取 JSON 内容
	content := strings.TrimSpace(resp)
	if i := strings.Index(content, "{"); i >= 0 {
		content = content[i:]
	}
	if j := strings.LastIndex(content, "}"); j >= 0 {
		content = content[:j+1]
	}

	var intent mcpSearchIntent
	if err := json.Unmarshal([]byte(content), &intent); err != nil {
		// 解析失败时回退到原始关键词
		return &mcpSearchIntent{
			Keywords:    query,
			NpmSearch:   query,
			Description: query,
		}, nil
	}
	if intent.Keywords == "" {
		intent.Keywords = intent.NpmSearch
	}
	if intent.NpmSearch == "" {
		intent.NpmSearch = intent.Keywords
	}
	return &intent, nil
}

// searchGitHub 通过 GitHub 仓库搜索 API 查找 MCP 相关仓库。
func (h *ToolHandler) searchGitHub(ctx context.Context, keywords string) []mcpDiscoveryItem {
	if keywords == "" {
		return nil
	}

	// 确保搜索词聚焦 MCP 相关仓库，避免返回无关结果。
	q := keywords
	if !strings.Contains(strings.ToLower(q), "mcp") {
		q = q + " mcp"
	}
	query := url.QueryEscape(q)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet,
		"https://api.github.com/search/repositories?q="+query+"&sort=stars&order=desc&per_page=10", nil)
	if err != nil {
		return nil
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("User-Agent", "GoTaskAI-MCP-Discovery")
	if token := os.Getenv("GITHUB_TOKEN"); token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil
	}

	var gh struct {
		Items []struct {
			FullName    string `json:"full_name"`
			Name        string `json:"name"`
			Description string `json:"description"`
			Stargazers  int    `json:"stargazers_count"`
			HTMLURL     string `json:"html_url"`
		} `json:"items"`
	}
	if err := json.Unmarshal(body, &gh); err != nil {
		return nil
	}

	items := make([]mcpDiscoveryItem, 0, len(gh.Items))
	for _, it := range gh.Items {
		if it.FullName == "" {
			continue
		}
		// 只保留 MCP 相关仓库（名称或描述包含 mcp）。
		if !strings.Contains(strings.ToLower(it.FullName), "mcp") && !strings.Contains(strings.ToLower(it.Description), "mcp") {
			continue
		}
		desc := it.Description
		if desc == "" {
			desc = "GitHub MCP 工具仓库"
		}
		items = append(items, mcpDiscoveryItem{
			Name:           normalizeToolName(it.FullName),
			DisplayName:    it.Name,
			Description:    desc,
			RepoURL:        it.HTMLURL,
			Stars:          it.Stargazers,
			Source:         "github",
			InstallCommand: "npx -y " + it.Name + "（具体启动方式以仓库 README 为准）",
			Config: map[string]any{
				"command": "npx",
				"args":    []string{"-y", it.Name},
			},
		})
	}
	return items
}

// searchNPM 通过 npm registry 搜索 MCP 相关包。
func (h *ToolHandler) searchNPM(ctx context.Context, text string) []mcpDiscoveryItem {
	if text == "" {
		return nil
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet,
		"https://registry.npmjs.org/-/v1/search?text="+url.QueryEscape(text)+"&size=10", nil)
	if err != nil {
		return nil
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "GoTaskAI-MCP-Discovery")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil
	}

	var npm struct {
		Objects []struct {
			Package struct {
				Name        string `json:"name"`
				Description string `json:"description"`
				Version     string `json:"version"`
			} `json:"package"`
		} `json:"objects"`
	}
	if err := json.Unmarshal(body, &npm); err != nil {
		return nil
	}

	items := make([]mcpDiscoveryItem, 0, len(npm.Objects))
	for _, obj := range npm.Objects {
		p := obj.Package
		if p.Name == "" {
			continue
		}
		// 过滤掉明显不是 MCP 服务端的包
		if !strings.Contains(strings.ToLower(p.Name), "mcp") && !strings.Contains(strings.ToLower(p.Description), "mcp") {
			continue
		}
		desc := p.Description
		if desc == "" {
			desc = "npm MCP 工具包"
		}
		items = append(items, mcpDiscoveryItem{
			Name:           normalizeToolName(p.Name),
			DisplayName:    p.Name,
			Description:    desc,
			Source:         "npm",
			InstallCommand: "npx -y " + p.Name,
			Config: map[string]any{
				"command": "npx",
				"args":    []string{"-y", p.Name},
			},
		})
	}
	return items
}

// normalizeToolName 将包名/仓库名转换为合法的工具 name（小写、下划线）。
func normalizeToolName(s string) string {
	// 去掉 scope 前缀（如 @scope/name）与仓库 owner 前缀
	if i := strings.LastIndex(s, "/"); i >= 0 {
		s = s[i+1:]
	}
	s = strings.NewReplacer("@", "", "/", "_", "-", "_", ".", "_", " ", "_").Replace(s)
	s = strings.ToLower(s)
	return s
}
