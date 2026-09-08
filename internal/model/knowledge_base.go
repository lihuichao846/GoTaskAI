package model

import "time"

// KnowledgeBase 代表用户构建的一个知识库。
type KnowledgeBase struct {
	ID          string    `json:"id" gorm:"primaryKey;type:varchar(36)"`
	UserID      uint      `json:"user_id" gorm:"index"`
	Name        string    `json:"name" gorm:"type:varchar(100)"`
	Description string    `json:"description" gorm:"type:text"`
	Type        string    `json:"type" gorm:"type:varchar(20)"`           // rag / kag / hybrid
	Status      string    `json:"status" gorm:"type:varchar(20);index"`   // pending / building / ready / failed
	DocCount    int       `json:"doc_count"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
}
