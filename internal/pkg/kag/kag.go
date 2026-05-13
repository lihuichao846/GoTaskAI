package kag

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"regexp"
	"strings"

	"gotaskai/internal/pkg/llm"

	"github.com/neo4j/neo4j-go-driver/v5/neo4j"
)

// KAGManager 处理图谱相关操作
type KAGManager struct {
	neo4jDriver neo4j.DriverWithContext
	llmClient   *llm.Client
}

func NewKAGManager(driver neo4j.DriverWithContext, llmClient *llm.Client) *KAGManager {
	return &KAGManager{
		neo4jDriver: driver,
		llmClient:   llmClient,
	}
}

// EntityRelation 定义实体关系三元组
type EntityRelation struct {
	Source   string `json:"source"`
	Relation string `json:"relation"`
	Target   string `json:"target"`
}

// ExtractKnowledge 从文本中抽取三元组
func (k *KAGManager) ExtractKnowledge(ctx context.Context, text string) ([]EntityRelation, error) {
	systemPrompt := `你是一个知识图谱信息抽取专家。请阅读以下文本，并从中提取实体和关系。
提取结果必须严格使用 JSON 数组格式返回，不要包含任何其他说明文字，也不要使用 markdown 代码块标记。
JSON 格式要求：
[
  {"source": "实体A", "relation": "关系名称", "target": "实体B"}
]
注意：关系名称应尽量简短、动词化（如 "is_author_of", "located_in", "father_of" 等）。`

	userPrompt := "请抽取以下文本的实体关系：\n\n" + text

	resp, err := k.llmClient.Generate(ctx, systemPrompt, nil, userPrompt, nil)
	if err != nil {
		return nil, fmt.Errorf("llm extract failed: %v", err)
	}

	// 清理可能包含在 markdown 代码块中的 JSON
	resp = cleanJSONString(resp)

	var relations []EntityRelation
	if err := json.Unmarshal([]byte(resp), &relations); err != nil {
		log.Printf("Failed to unmarshal extracted JSON: %v, raw text: %s", err, resp)
		return nil, fmt.Errorf("json parse error: %v", err)
	}

	return relations, nil
}

// IngestGraph 将三元组存入 Neo4j 图数据库
func (k *KAGManager) IngestGraph(ctx context.Context, relations []EntityRelation) error {
	session := k.neo4jDriver.NewSession(ctx, neo4j.SessionConfig{AccessMode: neo4j.AccessModeWrite})
	defer session.Close(ctx)

	for _, rel := range relations {
		_, err := session.ExecuteWrite(ctx, func(tx neo4j.ManagedTransaction) (any, error) {
			// MERGE 会保证节点不存在时创建，存在时不重复创建
			query := fmt.Sprintf(`
				MERGE (a:Entity {name: $source})
				MERGE (b:Entity {name: $target})
				MERGE (a)-[r:%s]->(b)
				RETURN id(r)
			`, cleanRelationName(rel.Relation))

			result, err := tx.Run(ctx, query, map[string]any{
				"source": rel.Source,
				"target": rel.Target,
			})
			if err != nil {
				return nil, err
			}
			return result.Consume(ctx)
		})

		if err != nil {
			log.Printf("Failed to insert relation %s-%s-%s: %v", rel.Source, rel.Relation, rel.Target, err)
		}
	}
	return nil
}

// Text2Cypher 将用户自然语言问题转换为 Cypher 语句，并查询图数据库返回结果
func (k *KAGManager) RetrieveGraphContext(ctx context.Context, question string) (string, error) {
	systemPrompt := `你是一个 Neo4j Cypher 查询专家。我们的图数据库中包含标签为 Entity 的节点，以及它们之间的关系。
节点的唯一标识属性是 name。
请根据用户的提问，写出对应的 Cypher 查询语句。
你必须严格只返回 Cypher 语句本身，不要有任何其他解释，也不要有 markdown 代码块标记。
示例提问：张三是谁的爸爸？
示例返回：MATCH (a:Entity {name: '张三'})-[:father_of]->(b:Entity) RETURN b.name LIMIT 5`

	cypherQuery, err := k.llmClient.Generate(ctx, systemPrompt, nil, question, nil)
	if err != nil {
		return "", fmt.Errorf("llm cypher generation failed: %v", err)
	}

	cypherQuery = cleanJSONString(cypherQuery) // 顺便清理可能存在的代码块

	if !strings.HasPrefix(strings.ToUpper(cypherQuery), "MATCH") {
		// 如果大模型回复的不是一个 MATCH 语句，很可能是它判断无法生成 Cypher
		log.Printf("[KAG] Model did not return a valid Cypher query: %s", cypherQuery)
		return "", nil
	}

	log.Printf("[KAG] Generated Cypher: %s", cypherQuery)

	// 执行查询
	session := k.neo4jDriver.NewSession(ctx, neo4j.SessionConfig{AccessMode: neo4j.AccessModeRead})
	defer session.Close(ctx)

	result, err := session.ExecuteRead(ctx, func(tx neo4j.ManagedTransaction) (any, error) {
		res, err := tx.Run(ctx, cypherQuery, nil)
		if err != nil {
			return nil, err
		}

		var answers []string
		for res.Next(ctx) {
			record := res.Record()
			if len(record.Values) > 0 {
				answers = append(answers, fmt.Sprintf("%v", record.Values[0]))
			}
		}
		return strings.Join(answers, ", "), nil
	})

	if err != nil {
		return "", fmt.Errorf("neo4j query execution failed: %v", err)
	}

	if resultStr, ok := result.(string); ok && resultStr != "" {
		return resultStr, nil
	}

	return "", nil
}

// 辅助函数：清理可能包裹的 markdown 代码块
func cleanJSONString(s string) string {
	s = strings.TrimSpace(s)
	if strings.HasPrefix(s, "```json") {
		s = strings.TrimPrefix(s, "```json")
	} else if strings.HasPrefix(s, "```cypher") {
		s = strings.TrimPrefix(s, "```cypher")
	} else if strings.HasPrefix(s, "```") {
		s = strings.TrimPrefix(s, "```")
	}
	if strings.HasSuffix(s, "```") {
		s = strings.TrimSuffix(s, "```")
	}
	return strings.TrimSpace(s)
}

// 辅助函数：清理关系名称（Cypher 关系名称只能包含字母、数字和下划线，且不能以数字开头）
func cleanRelationName(s string) string {
	reg := regexp.MustCompile(`[^a-zA-Z0-9_]`)
	s = reg.ReplaceAllString(s, "_")
	if len(s) > 0 && s[0] >= '0' && s[0] <= '9' {
		s = "_" + s
	}
	return strings.ToUpper(s)
}
