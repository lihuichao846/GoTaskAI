package middleware

import (
	"gotaskai/internal/pkg/jwt"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
)

// AuthMiddleware 是一个验证 JWT Token 的中间件
func AuthMiddleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		// 1. 从 HTTP 头获取 Authorization 字段
		authHeader := c.GetHeader("Authorization")
		if authHeader == "" {
			c.JSON(http.StatusUnauthorized, gin.H{"error": "未提供认证 Token，请先登录"})
			c.Abort() // 拦截请求
			return
		}

		// 2. 解析 Bearer Token 格式 (例如: "Bearer xxxxx.yyyyy.zzzzz")
		parts := strings.SplitN(authHeader, " ", 2)
		if len(parts) != 2 || parts[0] != "Bearer" {
			c.JSON(http.StatusUnauthorized, gin.H{"error": "Token 格式错误，应为 Bearer <token>"})
			c.Abort()
			return
		}

		// 3. 验证并解析 Token
		claims, err := jwt.ParseToken(parts[1])
		if err != nil {
			c.JSON(http.StatusUnauthorized, gin.H{"error": "Token 无效或已过期，请重新登录"})
			c.Abort()
			return
		}

		// 4. 验证成功，将 UserID 存入 Gin 的 Context 中，供后续路由使用
		c.Set("userID", claims.UserID)
		
		// 5. 放行
		c.Next()
	}
}
