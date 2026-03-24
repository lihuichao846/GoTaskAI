package jwt

import (
	"errors"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

// 在生产环境中，这个密钥应该从环境变量读取。这里为了演示硬编码在代码里。
var SecretKey = []byte("gotaskai-super-secret-key")

// Claims 定义了 JWT 载荷中包含的数据
type Claims struct {
	UserID uint `json:"user_id"`
	jwt.RegisteredClaims
}

// GenerateToken 为指定用户生成一个 JWT Token
func GenerateToken(userID uint) (string, error) {
	claims := Claims{
		UserID: userID,
		RegisteredClaims: jwt.RegisteredClaims{
			ExpiresAt: jwt.NewNumericDate(time.Now().Add(24 * time.Hour)), // Token 有效期设为 1 天
			IssuedAt:  jwt.NewNumericDate(time.Now()),
		},
	}
	// 使用 HS256 算法签名
	token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	return token.SignedString(SecretKey)
}

// ParseToken 解析并验证 JWT Token
func ParseToken(tokenStr string) (*Claims, error) {
	// 解析 Token
	token, err := jwt.ParseWithClaims(tokenStr, &Claims{}, func(token *jwt.Token) (interface{}, error) {
		return SecretKey, nil
	})

	if err != nil {
		return nil, err
	}

	// 验证 Claims 并返回
	if claims, ok := token.Claims.(*Claims); ok && token.Valid {
		return claims, nil
	}

	return nil, errors.New("invalid token")
}
