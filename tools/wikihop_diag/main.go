// Command wikihop_diag 诊断 KAG 在图谱上返回空上下文的根因。
// 用途：连接到 wkihop 评测后遗留的 Neo4j 图谱，打印图规模/关系类型，
// 并对给定的查询逐个执行 RetrieveGraphContext（打印生成的 Cypher 与返回结果）。
//
// 用法：go run ./tools/wikihop_diag "query1" "query2" ...
package main

import (
	"context"
	"fmt"
	"os"

	"gotaskai/internal/config"
	"gotaskai/internal/db"
	"gotaskai/internal/pkg/kag"
	"gotaskai/internal/pkg/llm"

	"github.com/neo4j/neo4j-go-driver/v5/neo4j"
)

func main() {
	config.InitConfig("config/config.yaml")
	ctx := context.Background()

	driver := db.InitNeo4j(config.AppConfig.Neo4j.URI, config.AppConfig.Neo4j.Username, config.AppConfig.Neo4j.Password)
	defer driver.Close(ctx)

	printGraphStats(ctx, driver)

	kagMgr := kag.NewKAGManager(driver, llm.NewClient())

	queries := os.Args[1:]
	if len(queries) == 0 {
		queries = []string{
			"original_language_of_work solva sawan",
			"located_in_the_administrative_territorial_entity upper bicutan national high school",
			"office_contested danish general election",
		}
	}

	fmt.Printf("\n===== RetrieveGraphContext 行为诊断 (%d 查询) =====\n", len(queries))
	for _, q := range queries {
		fmt.Printf("\n---- 查询: %q ----\n", q)
		gctx, err := kagMgr.RetrieveGraphContext(ctx, q, 10)
		if err != nil {
			fmt.Printf("  [!] error: %v\n", err)
		} else if gctx == "" {
			fmt.Printf("  [!] 图谱上下文为空\n")
		} else {
			fmt.Printf("  [+] 图谱上下文: %s\n", gctx)
		}
	}
}

func printGraphStats(ctx context.Context, driver neo4j.DriverWithContext) {
	session := driver.NewSession(ctx, neo4j.SessionConfig{AccessMode: neo4j.AccessModeRead})
	defer session.Close(ctx)

	run := func(cypher string) string {
		res, err := session.ExecuteRead(ctx, func(tx neo4j.ManagedTransaction) (any, error) {
			r, err := tx.Run(ctx, cypher, nil)
			if err != nil {
				return nil, err
			}
			out := ""
			for r.Next(ctx) {
				out += fmt.Sprintf("%v\n", r.Record().Values)
			}
			return out, nil
		})
		if err != nil {
			return fmt.Sprintf("<err: %v>", err)
		}
		return fmt.Sprintf("%v", res)
	}

	fmt.Println("===== 图谱规模 =====")
	fmt.Printf("节点数: %s", run("MATCH (n:Entity) RETURN count(n)"))
	fmt.Printf("关系数: %s", run("MATCH ()-[r]->() RETURN count(r)"))
	fmt.Printf("关系类型: %s", run("CALL db.relationshipTypes() YIELD relationshipType RETURN relationshipType"))
	// 样例节点名（观察大小写/是否含空格，与查询中的归一化实体是否一致）
	fmt.Printf("样例节点: %s", run("MATCH (n:Entity) RETURN n.name LIMIT 12"))
}
