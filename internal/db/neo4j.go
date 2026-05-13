package db

import (
	"context"
	"log"

	"github.com/neo4j/neo4j-go-driver/v5/neo4j"
)

// InitNeo4j 初始化并返回一个 Neo4j 客户端驱动
func InitNeo4j(uri, username, password string) neo4j.DriverWithContext {
	driver, err := neo4j.NewDriverWithContext(uri, neo4j.BasicAuth(username, password, ""))
	if err != nil {
		log.Fatalf("Failed to create Neo4j driver: %v", err)
	}

	// 验证连接
	ctx := context.Background()
	err = driver.VerifyConnectivity(ctx)
	if err != nil {
		log.Fatalf("Failed to connect to Neo4j: %v", err)
	}

	return driver
}
