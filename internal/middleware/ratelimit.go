package middleware

import (
	"context"
	"fmt"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/redis/go-redis/v9"
)

// RateLimitMiddleware 是一个基于 Redis 固定窗口算法的限流中间件
// maxRequests: 在指定时间窗口内允许的最大请求数
// window: 时间窗口大小
func RateLimitMiddleware(rdb *redis.Client, maxRequests int, window time.Duration) gin.HandlerFunc {
	return func(c *gin.Context) {
		// 1. 获取用户标识
		// 优先使用已登录用户的 userID，如果没有登录则降级使用 IP 地址
		var key string
		userID, exists := c.Get("userID")
		if exists {
			key = "rate_limit:user:" + fmt.Sprint(userID)
		} else {
			clientIP := c.ClientIP()
			key = "rate_limit:ip:" + clientIP
		}

		ctx := context.Background()

		// 2. 利用 Redis 的 INCR 命令使计数器加 1
		// INCR 是原子操作，在并发环境下也是安全的
		count, err := rdb.Incr(ctx, key).Result()
		if err != nil {
			// 如果 Redis 出现异常，为了保证业务高可用（降级），这里选择放行请求
			c.Next()
			return
		}

		// 3. 如果是这个时间窗口内的第一次请求，则设置该 Key 的过期时间
		if count == 1 {
			rdb.Expire(ctx, key, window)
		}

		// 4. 检查是否超过了限制
		if int(count) > maxRequests {
			// 如果超过限流阈值，直接返回 429 Too Many Requests 错误，并终止请求传递
			c.JSON(http.StatusTooManyRequests, gin.H{
				"error": "您的操作太频繁啦，请稍后（最多1分钟）再试",
			})
			c.Abort() // 拦截请求，不再执行后面的 handler
			return
		}

		// 5. 没有超过限制，正常放行，执行后续业务逻辑
		c.Next()
	}
}
