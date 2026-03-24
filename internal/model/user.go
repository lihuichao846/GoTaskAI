package model

import "time"

// User 代表系统中的一个注册用户
type User struct {
	ID        uint      `json:"id" gorm:"primaryKey"`
	Username  string    `json:"username" gorm:"uniqueIndex;type:varchar(50);not null"`
	Password  string    `json:"-" gorm:"type:varchar(255);not null"` // 使用 "-" 标签，确保密码不会在转成 JSON 时暴露给前端
	CreatedAt time.Time `json:"created_at"`
}
