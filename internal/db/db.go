package db

import (
	"context"
	"log"

	"gotaskai/internal/config"

	"github.com/hibiken/asynq"
	"github.com/redis/go-redis/v9"
	"gorm.io/driver/mysql"
	"gorm.io/gorm"
)

// InitMySQL 初始化并返回一个 MySQL GORM 数据库实例
func InitMySQL(dsn string) *gorm.DB {
	db, err := gorm.Open(mysql.Open(dsn), &gorm.Config{})
	if err != nil {
		log.Fatalf("Failed to connect to MySQL: %v", err)
	}
	return db
}

// InitRedis 初始化并返回一个 Redis 客户端实例，支持单机和 Sentinel。
func InitRedis(cfg config.RedisConfig) *redis.Client {
	var rdb *redis.Client
	if cfg.UseSentinel() {
		if cfg.MasterName == "" || len(cfg.SentinelAddrs) == 0 {
			log.Fatal("Failed to connect to Redis: sentinel mode requires master_name and sentinel_addrs")
		}
		rdb = redis.NewFailoverClient(&redis.FailoverOptions{
			MasterName:    cfg.MasterName,
			SentinelAddrs: cfg.SentinelAddrs,
			Password:      cfg.Password,
			DB:            cfg.DB,
		})
	} else {
		if cfg.Addr == "" {
			log.Fatal("Failed to connect to Redis: standalone mode requires addr")
		}
		rdb = redis.NewClient(&redis.Options{
			Addr:     cfg.Addr,
			Password: cfg.Password,
			DB:       cfg.DB,
		})
	}

	// 测试连接
	if err := rdb.Ping(context.Background()).Err(); err != nil {
		log.Fatalf("Failed to connect to Redis: %v", err)
	}
	return rdb
}

// NewAsynqRedisConnOpt 返回 Asynq 使用的 Redis 连接配置。
func NewAsynqRedisConnOpt(cfg config.RedisConfig) asynq.RedisConnOpt {
	if cfg.UseSentinel() {
		if cfg.MasterName == "" || len(cfg.SentinelAddrs) == 0 {
			log.Fatal("Failed to initialize Asynq Redis connection: sentinel mode requires master_name and sentinel_addrs")
		}
		return asynq.RedisFailoverClientOpt{
			MasterName:    cfg.MasterName,
			SentinelAddrs: cfg.SentinelAddrs,
			Password:      cfg.Password,
			DB:            cfg.DB,
		}
	}

	if cfg.Addr == "" {
		log.Fatal("Failed to initialize Asynq Redis connection: standalone mode requires addr")
	}
	return asynq.RedisClientOpt{
		Addr:     cfg.Addr,
		Password: cfg.Password,
		DB:       cfg.DB,
	}
}
