package model

import "time"

// 记忆类型：决定"这条记忆该不该常驻、会不会过期"。
const (
	// MemoryTypeSemantic 关于用户的稳定事实（如"用户所在城市是杭州"）。
	MemoryTypeSemantic = "semantic"
	// MemoryTypePreference 用户的偏好（如"偏好简洁回答"）。常驻注入，最稳定。
	MemoryTypePreference = "preference"
	// MemoryTypeEpisodic 一次性的经历/结论（如"用户上个月那个项目叫 Atlas"）。受 TTL 约束。
	MemoryTypeEpisodic = "episodic"
	// MemoryTypeProcedural 可复用的做事方式（如"查成本要先调 CostExplorer"）。
	MemoryTypeProcedural = "procedural"
)

// 记忆状态：与双时间轴配合，表达"历史保留、当前生效"。
const (
	// MemoryStatusActive 当前生效（valid_until 为 NULL）。
	MemoryStatusActive = "active"
	// MemoryStatusSuperseded 已被新事实取代（valid_until 已关闭），历史保留可查。
	MemoryStatusSuperseded = "superseded"
	// MemoryStatusArchived 因超出配额被淘汰出召回范围（未删除）。
	MemoryStatusArchived = "archived"
)

// UserMemory 一条【用户向】的长期记忆（跨会话）。
//
// 与 Conversation.Summary 的分工：Summary 是"会话内不丢信息"，本表是"换会话还记得"。
// 与 RAG/KAG 的分工：RAG/KAG 存的是关于世界的文档知识，本表存的是关于用户自身的状态。
//
// 时间语义（双时间轴）：
//   - ValidFrom / ValidUntil —— 事实在【现实中】何时为真（ValidUntil 为 NULL 表示仍然为真）
//   - CreatedAt / SupersededAt —— 【系统】何时获知、何时认定其被取代
//
// 冲突时关闭 ValidUntil 并置 SupersededAt，绝不物理删除，从而既能回答"现在是什么"，
// 也能回答"当时是什么"。
type UserMemory struct {
	ID string `json:"id" gorm:"primaryKey;type:varchar(36)"`
	// UserID 是记忆的唯一归属维度，也是隐私边界：任何查询都必须带它（fail-closed）。
	UserID uint `json:"user_id" gorm:"index;index:idx_mem_scope,priority:1;index:idx_mem_active,priority:1"`
	// AgentID 为空表示"该用户全局"（不绑定具体 Agent）；非空则只在该 Agent 的对话中召回。
	// 默认跨 Agent 不共享——与知识库的归属模型保持一致。
	AgentID string `json:"agent_id" gorm:"index;index:idx_mem_scope,priority:2;index:idx_mem_active,priority:2;type:varchar(36)"`

	// MemKey 为归一化的「主体::谓词」（如 preference::answer_style），是冲突消解的判定依据。
	// 没有它就只能靠语义相似度猜"这两条是不是同一件事"，准确率与成本都更差。
	MemKey string `json:"mem_key" gorm:"index;type:varchar(128)"`
	// ActiveKey 生效时等于 MemKey，失效时置 NULL。
	// 依赖 MySQL 唯一索引允许多个 NULL 这一点，实现「同一 key 同时只能有一条生效记录，
	// 而历史记录可以保留任意多条」。刻意不用空串——空串会与其它失效行撞唯一键
	// （与 documents.source_path 踩过的坑同源）。
	ActiveKey *string `json:"-" gorm:"uniqueIndex:idx_mem_active,priority:3;type:varchar(128)"`

	Type string `json:"type" gorm:"index;type:varchar(16)"`
	// Content 单条记忆的正文；长度上限由 memory.max_content_chars 在写入侧把关。
	// 列宽刻意取 varchar(1024) 与闸门同源（按 utf8mb4 每字符最多 4 字节约 4KB），
	// 避免出现 documents.content 那种"列容量与业务闸门差 160 倍"的错配。
	Content    string  `json:"content" gorm:"type:varchar(1024)"`
	Confidence float64 `json:"confidence" gorm:"type:decimal(4,3);default:1"`
	Status     string  `json:"status" gorm:"index;type:varchar(16);default:'active'"`

	ValidFrom    time.Time  `json:"valid_from" gorm:"type:datetime(3)"`
	ValidUntil   *time.Time `json:"valid_until,omitempty" gorm:"type:datetime(3)"`
	SupersededAt *time.Time `json:"superseded_at,omitempty" gorm:"type:datetime(3)"`

	// 来源溯源：让每条记忆都能回答"这是从哪儿来的"。
	SourceConversationID string `json:"source_conversation_id,omitempty" gorm:"index;type:varchar(36)"`
	SourceTaskID         string `json:"source_task_id,omitempty" gorm:"index;type:varchar(36)"`

	HitCount  int        `json:"hit_count" gorm:"default:0"`
	LastHitAt *time.Time `json:"last_hit_at,omitempty" gorm:"type:datetime(3)"`
	CreatedAt time.Time  `json:"created_at"`
	UpdatedAt time.Time  `json:"updated_at"`
}

// ValidMemoryTypes 为写入侧的类型白名单（闸门之一：不在白名单内的一律拒绝）。
func ValidMemoryTypes() []string {
	return []string{MemoryTypeSemantic, MemoryTypePreference, MemoryTypeEpisodic, MemoryTypeProcedural}
}

// IsValidMemoryType 判断类型是否在白名单内。
func IsValidMemoryType(t string) bool {
	for _, v := range ValidMemoryTypes() {
		if v == t {
			return true
		}
	}
	return false
}
