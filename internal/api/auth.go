package api

import (
	"context"
	"fmt"
	"gotaskai/internal/model"
	"gotaskai/internal/pkg/jwt"
	"math/rand"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/redis/go-redis/v9"
	"golang.org/x/crypto/bcrypt"
	"gorm.io/gorm"
)

// AuthHandler 处理用户认证相关的请求
type AuthHandler struct {
	db  *gorm.DB
	rdb *redis.Client
}

func NewAuthHandler(db *gorm.DB, rdb *redis.Client) *AuthHandler {
	return &AuthHandler{db: db, rdb: rdb}
}

// AuthRequest 定义注册和登录时的 JSON 请求体
type AuthRequest struct {
	Username string `json:"username" binding:"required,min=3,max=20"`
	Password string `json:"password" binding:"required,min=6"`
}

// Register 处理用户注册请求
func (h *AuthHandler) Register(c *gin.Context) {
	var req AuthRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "用户名(3-20位)和密码(最少6位)格式不正确"})
		return
	}

	// 检查用户名是否已存在
	var count int64
	h.db.Model(&model.User{}).Where("username = ?", req.Username).Count(&count)
	if count > 0 {
		c.JSON(http.StatusConflict, gin.H{"error": "该用户名已被注册"})
		return
	}

	// 使用 bcrypt 将明文密码单向加密为哈希值，确保数据库被拖库也不会泄露密码
	hashedPassword, err := bcrypt.GenerateFromPassword([]byte(req.Password), bcrypt.DefaultCost)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "密码加密失败"})
		return
	}

	user := model.User{
		Username: req.Username,
		Password: string(hashedPassword),
	}

	// 保存到数据库
	if err := h.db.Create(&user).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "创建用户失败"})
		return
	}

	c.JSON(http.StatusOK, gin.H{"message": "注册成功，请去登录"})
}

// Login 处理用户登录请求
func (h *AuthHandler) Login(c *gin.Context) {
	var req AuthRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "参数格式不正确"})
		return
	}

	// 查找用户
	var user model.User
	if err := h.db.Where("username = ?", req.Username).First(&user).Error; err != nil {
		// 注意：为了安全，不要明确提示是“用户名不存在”还是“密码错误”，统一返回相同信息
		c.JSON(http.StatusUnauthorized, gin.H{"error": "用户名或密码错误"})
		return
	}

	// 校验密码
	if err := bcrypt.CompareHashAndPassword([]byte(user.Password), []byte(req.Password)); err != nil {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "用户名或密码错误"})
		return
	}

	// 生成 JWT Token，将用户 ID 编码进去
	token, err := jwt.GenerateToken(user.ID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "生成登录凭证失败"})
		return
	}

	// 返回 Token 和基础用户信息给前端
	c.JSON(http.StatusOK, gin.H{
		"message":  "登录成功",
		"token":    token,
		"username": user.Username,
	})
}

// RequestResetCode 请求密码重置验证码
func (h *AuthHandler) RequestResetCode(c *gin.Context) {
	var req struct {
		Username string `json:"username" binding:"required"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "参数错误"})
		return
	}

	// 检查用户是否存在
	var count int64
	h.db.Model(&model.User{}).Where("username = ?", req.Username).Count(&count)
	if count == 0 {
		c.JSON(http.StatusNotFound, gin.H{"error": "该用户不存在"})
		return
	}

	// 生成 6 位随机验证码
	rand.Seed(time.Now().UnixNano())
	code := fmt.Sprintf("%06d", rand.Intn(1000000))

	// 将验证码存入 Redis，有效期 5 分钟
	key := "reset_code:" + req.Username
	err := h.rdb.Set(context.Background(), key, code, 5*time.Minute).Err()
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "验证码生成失败，请稍后再试"})
		return
	}

	// 模拟发送邮件或短信的行为：将验证码打印到终端控制台
	fmt.Println("\n=======================================================")
	fmt.Printf("[模拟短信/邮件] 账号 %s 的密码重置验证码是: %s\n", req.Username, code)
	fmt.Println("请在 5 分钟内输入验证码完成重置。")
	fmt.Println("=======================================================\n")

	c.JSON(http.StatusOK, gin.H{"message": "验证码已发送，请检查控制台输出"})
}

// ResetPassword 提交新密码和验证码进行重置
func (h *AuthHandler) ResetPassword(c *gin.Context) {
	var req struct {
		Username    string `json:"username" binding:"required"`
		Code        string `json:"code" binding:"required,len=6"`
		NewPassword string `json:"new_password" binding:"required,min=6"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "参数格式不正确(密码至少6位，验证码6位)"})
		return
	}

	key := "reset_code:" + req.Username

	// 从 Redis 获取保存的验证码
	savedCode, err := h.rdb.Get(context.Background(), key).Result()
	if err != nil || savedCode == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "验证码已过期或不存在，请重新获取"})
		return
	}

	// 对比验证码
	if savedCode != req.Code {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "验证码错误"})
		return
	}

	// 验证成功，删除 Redis 中的验证码，防止重复使用
	h.rdb.Del(context.Background(), key)

	// 加密新密码
	hashedPassword, err := bcrypt.GenerateFromPassword([]byte(req.NewPassword), bcrypt.DefaultCost)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "密码加密失败"})
		return
	}

	// 更新数据库
	if err := h.db.Model(&model.User{}).Where("username = ?", req.Username).Update("password", string(hashedPassword)).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "密码重置失败"})
		return
	}

	c.JSON(http.StatusOK, gin.H{"message": "密码重置成功，请使用新密码登录"})
}
