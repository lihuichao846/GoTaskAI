package model

import "time"

// Document 表示知识库中的一份文档，异步构建时逐份处理。
type Document struct {
	ID         string    `json:"id" gorm:"primaryKey;type:varchar(36)"`
	KBID       string    `json:"kb_id" gorm:"index"`
	Title      string    `json:"title"`
	Content    string    `json:"content" gorm:"type:text"`
	Summary    string    `json:"summary" gorm:"type:text"`       // 构建期生成的文档摘要（用于上下文渐进式披露的摘要层）
	Status     string    `json:"status" gorm:"type:varchar(20)"` // pending / processing / ready / failed
	ChunkCount int       `json:"chunk_count"`
	CreatedAt  time.Time `json:"created_at"`
}
