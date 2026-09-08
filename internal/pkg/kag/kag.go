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

// RetrieveGraphContext 将用户自然语言问题转换为 Cypher 语句，并查询图数据库返回结果。
// topN 限制返回的命中关系条数（<=0 表示不限制），用于控制注入上下文体积。
func (k *KAGManager) RetrieveGraphContext(ctx context.Context, question string, topN int) (string, error) {
	// 查询图中实际存在的关系类型，注入 Text2Cypher prompt，约束 LLM 只能使用这些关系名，
	// 缓解 LLM 生成同义变体（如 headquartered_in / located_in）而匹配不到已入库关系的问题。
	knownTypes := k.knownRelationTypes(ctx)

	var typeHint string
	if len(knownTypes) > 0 {
		typeHint = fmt.Sprintf(`
图中实际存在的关系类型如下（必须严格使用其中一种，关系名已统一为大写）：
%s`, strings.Join(knownTypes, ", "))
	}

	systemPrompt := fmt.Sprintf(`你是一个 Neo4j Cypher 查询专家。我们的图数据库包含标签为 Entity 的节点及其关系。
节点的唯一标识属性是 name。%s
请根据用户的提问，写出对应的 Cypher 查询语句。
你必须严格只返回 Cypher 语句本身，不要有任何其他解释，也不要有 markdown 代码块标记。
注意：文中关系名使用大写（如 FATHER_OF），请保持一致，不要自创同义词。
示例提问：张三是谁的爸爸？
示例返回：MATCH (a:Entity {name: '张三'})-[:FATHER_OF]->(b:Entity) RETURN b.name LIMIT 5`, typeHint)

	cypherQuery, err := k.llmClient.Generate(ctx, systemPrompt, nil, question, nil)
	if err != nil {
		return "", fmt.Errorf("llm cypher generation failed: %v", err)
	}

	cypherQuery = cleanJSONString(cypherQuery) // 顺便清理可能存在的代码块

	if !validateReadOnlyCypher(cypherQuery) {
		// 大模型输出不是合法的只读 MATCH 查询（可能是无法生成，或含写操作/注释/多语句），拒绝执行以保护图数据。
		log.Printf("[KAG] Model did not return a valid read-only Cypher query: %s", cypherQuery)
		return "", nil
	}

	// 大小写归一化：把 LLM 生成 Cypher 中的关系名统一转为大写，与 IngestGraph 入库侧 cleanRelationName 保持一致。
	// Neo4j 关系名大小写敏感，若不归一化，`-[father_of]->` 将匹配不到已入库的 `-[:FATHER_OF]->`，导致图谱上下文恒为空。
	cypherQuery = normalizeCypherRelationNames(cypherQuery)

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
			if topN > 0 && len(answers) >= topN {
				break
			}
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

// forbiddenCypherKeywords 代表只读 MATCH 查询中不应出现的写操作或敏感语句关键词（后置空格用于避免误匹配如 CREATE_like 之类词）。
// 用于拦截 LLM 生成的越权/写操作 Cypher，避免误改图数据。
var forbiddenCypherKeywords = []string{
	"CREATE ", "MERGE ", "SET ", "DELETE ", "DETACH ", "REMOVE ", "DROP ", "CALL ", "LOAD CSV ",
}

// validateReadOnlyCypher 仅允许以 MATCH 开头的只读查询，并拒绝可能修改图数据的语句（含写操作、CALL、多语句、注释）。
// 防止 LLM 生成的异常/越权 Cypher 被直接执行。
func validateReadOnlyCypher(s string) bool {
	trimmed := strings.TrimSpace(s)
	upper := strings.ToUpper(trimmed)

	if !strings.HasPrefix(upper, "MATCH") {
		return false
	}
	// 多语句或注释注入风险
	if strings.Contains(trimmed, ";") || strings.Contains(trimmed, "//") || strings.Contains(trimmed, "/*") {
		return false
	}
	for _, kw := range forbiddenCypherKeywords {
		if strings.Contains(upper, kw) {
			return false
		}
	}
	return true
}

// knownRelationTypes 查询图中实际存在的关系类型列表，供 Text2Cypher 约束生成（缓解关系名变体/同义词问题）。
// 查询失败时返回 nil，调用方退化为仅依赖大小写归一化兜底。
func (k *KAGManager) knownRelationTypes(ctx context.Context) []string {
	session := k.neo4jDriver.NewSession(ctx, neo4j.SessionConfig{AccessMode: neo4j.AccessModeRead})
	defer session.Close(ctx)

	result, err := session.ExecuteRead(ctx, func(tx neo4j.ManagedTransaction) (any, error) {
		res, err := tx.Run(ctx, "CALL db.relationshipTypes() YIELD relationshipType RETURN relationshipType", nil)
		if err != nil {
			return nil, err
		}
		var types []string
		for res.Next(ctx) {
			if v, ok := res.Record().Get("relationshipType"); ok {
				types = append(types, fmt.Sprintf("%v", v))
			}
		}
		return types, nil
	})
	if err != nil {
		log.Printf("[KAG] failed to fetch relation types: %v", err)
		return nil
	}
	if types, ok := result.([]string); ok {
		return types
	}
	return nil
}

// relTypePattern 匹配方括号关系定义中的关系类型名（如 [:FATHER_OF]、[r:father_of]），并捕获关系名主体。
// 形如 `[var:REL]` 或 `[:REL]`，可带 `*N..M` 路径长度或属性 map；不会误伤节点 label（不在方括号内）或 map 键。
var relTypePattern = regexp.MustCompile(`\[\s*(?:[a-zA-Z_][a-zA-Z0-9_]*\s*)?:\s*([A-Za-z_][A-Za-z0-9_]*)`)

// normalizeCypherRelationNames 将 Cypher 中方括号关系定义处的所有关系名统一转为大写，
// 使其与 IngestGraph 通过 cleanRelationName 入库时的命名规则（大写）保持一致，从而对齐检索侧与写入侧的关系名大小写。
func normalizeCypherRelationNames(cypher string) string {
	idxs := relTypePattern.FindAllStringSubmatchIndex(cypher, -1)
	// 从后往前替换，避免改动下标影响后续位置
	for i := len(idxs) - 1; i >= 0; i-- {
		m := idxs[i]
		nameStart, nameEnd := m[2], m[3]
		cypher = cypher[:nameStart] + strings.ToUpper(cypher[nameStart:nameEnd]) + cypher[nameEnd:]
	}
	return cypher
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
