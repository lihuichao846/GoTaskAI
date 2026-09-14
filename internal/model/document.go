package model

import "time"

// Document 表示知识库中的一份文档，异步构建时逐份处理。
type Document struct {
	ID    string `json:"id" gorm:"primaryKey;type:varchar(36)"`
	KBID  string `json:"kb_id" gorm:"index"`
	Title string `json:"title"`
	// Content 为文档正文全文；用 mediumtext（上限 16MB）对齐入库闸门（扫描/上传各 10MB）。
	// 若退回 text（MySQL 上限 65535 字节），超过约 6.4 万字符的正文会在写入时溢出。
	Content    string    `json:"content" gorm:"type:mediumtext"`
	Summary    string    `json:"summary" gorm:"type:text"`       // 构建期生成的文档摘要（用于上下文渐进式披露的摘要层）
	Status     string    `json:"status" gorm:"type:varchar(20)"` // pending / processing / ready / failed / deleting
	ChunkCount int       `json:"chunk_count"`
	CreatedAt  time.Time `json:"created_at"`

	// 以下字段用于文档版本管理与失效清理（W3）。
	ContentHash string    `json:"content_hash" gorm:"type:varchar(64);index"` // 内容 sha256，用于增量去重与变更检测
	SourcePath  string    `json:"source_path" gorm:"type:varchar(512);index"` // 源文件路径，与 kb_id 组成唯一键（手动上传为空）
	GraphStatus string    `json:"graph_status" gorm:"type:varchar(20)"`       // 图谱阶段状态：pending / ready / failed / skipped（与 status 分离）
	UpdatedAt   time.Time `json:"updated_at"`                                 // 内容变更时间（GORM 自动维护）
}
