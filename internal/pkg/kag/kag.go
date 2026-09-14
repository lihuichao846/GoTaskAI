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
	ontology    *RelationOntology // 关系本体：抽取侧白名单约束 + 语义等价类收敛（非事后字符串归一化）
}

func NewKAGManager(driver neo4j.DriverWithContext, llmClient *llm.Client) *KAGManager {
	return NewKAGManagerWithOntology(driver, llmClient, DefaultRelationOntology())
}

// NewKAGManagerWithOntology 构建带自定义关系本体的 KAGManager，便于按业务领域校准白名单（≤120 目标）。
func NewKAGManagerWithOntology(driver neo4j.DriverWithContext, llmClient *llm.Client, ontology *RelationOntology) *KAGManager {
	return &KAGManager{
		neo4jDriver: driver,
		llmClient:   llmClient,
		ontology:    ontology,
	}
}

// EntityRelation 定义实体关系三元组
type EntityRelation struct {
	Source   string `json:"source"`
	Relation string `json:"relation"`
	Target   string `json:"target"`
}

// ExtractKnowledge 从文本中抽取三元组（无预算计量，保留给调试接口与评测工具）。
func (k *KAGManager) ExtractKnowledge(ctx context.Context, text string) ([]EntityRelation, error) {
	return k.ExtractKnowledgeWithBudget(ctx, text, nil)
}

// ExtractKnowledgeWithBudget 从文本中抽取三元组（本体引导抽取）：
//   - 注入本体白名单约束 LLM 只能产出规范关系，从源头收敛 schema（试点已证事后字符串归一化无效）；
//   - recall 导向：尽可能完整抽取，缓解「文档有、图里无」的覆盖不足（命中率低的主导根因）；
//   - 抽取结果经 CanonicalizeRelation 归一到规范关系名（语义等价类 + 大小写折叠）。
//
// budget 非空时，本次抽取的 LLM 调用纳入计量（kb:build 图谱阶段成本观测）。
func (k *KAGManager) ExtractKnowledgeWithBudget(ctx context.Context, text string, budget *llm.Budget) ([]EntityRelation, error) {
	systemPrompt := `你是一个知识图谱信息抽取专家。请阅读以下文本，并从中提取实体和关系。
提取结果必须严格使用 JSON 数组格式返回，不要包含任何其他说明文字，也不要使用 markdown 代码块标记。
JSON 格式要求：
[
  {"source": "实体A", "relation": "关系名称", "target": "实体B"}
]
注意：请尽可能完整地抽取文本中出现的所有事实三元组，不要遗漏（recall 优先）。`

	if hint := k.ontology.whitelistHint(); hint != "" {
		systemPrompt += fmt.Sprintf("\n关系名称必须仅从以下规范关系名中选择（保持大写，不要自创同义词）：\n%s", hint)
	}

	userPrompt := "请抽取以下文本的实体关系：\n\n" + text

	resp, err := k.llmClient.GenerateWithBudget(ctx, systemPrompt, userPrompt, budget)
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

	// 本体收敛：把 LLM 产出的关系归一到规范关系名（等价类 + 大小写）。
	for i := range relations {
		relations[i].Relation = k.ontology.CanonicalizeRelation(relations[i].Relation)
	}

	return relations, nil
}

// IngestGraph 将三元组存入 Neo4j 图数据库（不带来源归属，保留给调试接口与评测工具）。
func (k *KAGManager) IngestGraph(ctx context.Context, relations []EntityRelation) error {
	return k.IngestGraphWithSource(ctx, relations, "")
}

// IngestGraphWithSource 将三元组存入 Neo4j，并给关系打上来源文档归属属性 source_doc。
// docID 非空时写入来源，使关系可按来源文档失效清理（W3 / D2 修复）；为空时保持旧行为。
func (k *KAGManager) IngestGraphWithSource(ctx context.Context, relations []EntityRelation, docID string) error {
	session := k.neo4jDriver.NewSession(ctx, neo4j.SessionConfig{AccessMode: neo4j.AccessModeWrite})
	defer session.Close(ctx)

	sourceDoc := strings.TrimSpace(docID)
	for _, rel := range relations {
		if strings.TrimSpace(rel.Source) == "" || strings.TrimSpace(rel.Target) == "" || strings.TrimSpace(rel.Relation) == "" {
			continue
		}
		relType := cleanRelationName(rel.Relation)
		params := map[string]any{
			"source": rel.Source,
			"target": rel.Target,
		}
		var query string
		if sourceDoc == "" {
			query = fmt.Sprintf(`
				MERGE (a:Entity {name: $source})
				MERGE (b:Entity {name: $target})
				MERGE (a)-[r:%s]->(b)
				RETURN id(r)
			`, relType)
		} else {
			// source_doc 参与 MERGE：同一文档重复入图不会产生重复边（幂等）。
			query = fmt.Sprintf(`
				MERGE (a:Entity {name: $source})
				MERGE (b:Entity {name: $target})
				MERGE (a)-[r:%s {source_doc: $doc_id}]->(b)
				RETURN id(r)
			`, relType)
			params["doc_id"] = sourceDoc
		}

		_, err := session.ExecuteWrite(ctx, func(tx neo4j.ManagedTransaction) (any, error) {
			// MERGE 会保证节点不存在时创建，存在时不重复创建
			result, err := tx.Run(ctx, query, params)
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

// DeleteBySourceDoc 删除来源归属于指定文档的全部关系，并清理因此产生的孤立实体节点。
// 返回删除的关系数。用于文档 / 知识库的失效清理（W3）。
// 关系类型由本体动态生成，无法按固定类型匹配，故按关系属性 source_doc 过滤。
func (k *KAGManager) DeleteBySourceDoc(ctx context.Context, docID string) (int64, error) {
	sourceDoc := strings.TrimSpace(docID)
	if sourceDoc == "" {
		return 0, nil
	}
	session := k.neo4jDriver.NewSession(ctx, neo4j.SessionConfig{AccessMode: neo4j.AccessModeWrite})
	defer session.Close(ctx)

	var deleted int64
	_, err := session.ExecuteWrite(ctx, func(tx neo4j.ManagedTransaction) (any, error) {
		countRes, err := tx.Run(ctx,
			"MATCH ()-[r]->() WHERE r.source_doc = $doc_id RETURN count(r) AS c",
			map[string]any{"doc_id": sourceDoc})
		if err != nil {
			return nil, err
		}
		rec, err := countRes.Single(ctx)
		if err != nil {
			return nil, err
		}
		if c, ok := rec.Get("c"); ok {
			deleted, _ = c.(int64)
		}

		delRes, err := tx.Run(ctx,
			"MATCH ()-[r]->() WHERE r.source_doc = $doc_id DELETE r",
			map[string]any{"doc_id": sourceDoc})
		if err != nil {
			return nil, err
		}
		if _, err := delRes.Consume(ctx); err != nil {
			return nil, err
		}

		// 删除关系后清理不再被任何关系引用的孤立实体节点。
		orphanRes, err := tx.Run(ctx, "MATCH (n:Entity) WHERE NOT (n)--() DELETE n", nil)
		if err != nil {
			return nil, err
		}
		_, err = orphanRes.Consume(ctx)
		return nil, err
	})
	if err != nil {
		return 0, fmt.Errorf("kag: delete by source doc %s: %w", sourceDoc, err)
	}
	return deleted, nil
}

// EnsureSourceDocIndex 为现有全部关系类型建立 source_doc 属性索引（Neo4j 5 关系索引），
// 使「按来源文档清理」不必全图扫描。幂等：已存在的索引不会重复创建。
// 关系类型由本体动态生成，故先枚举现有类型再逐个建索引；新建类型需再次调用本方法。
func (k *KAGManager) EnsureSourceDocIndex(ctx context.Context) error {
	session := k.neo4jDriver.NewSession(ctx, neo4j.SessionConfig{AccessMode: neo4j.AccessModeWrite})
	defer session.Close(ctx)

	result, err := session.Run(ctx,
		"CALL db.relationshipTypes() YIELD relationshipType RETURN relationshipType", nil)
	if err != nil {
		return fmt.Errorf("kag: list relationship types: %w", err)
	}
	var relTypes []string
	for result.Next(ctx) {
		v, ok := result.Record().Get("relationshipType")
		if !ok {
			continue
		}
		if s, ok := v.(string); ok && s != "" {
			relTypes = append(relTypes, s)
		}
	}
	if err := result.Err(); err != nil {
		return fmt.Errorf("kag: iterate relationship types: %w", err)
	}

	for _, rt := range relTypes {
		idxName := "rel_source_doc_" + strings.ToLower(rt)
		stmt := fmt.Sprintf("CREATE INDEX %s IF NOT EXISTS FOR ()-[r:%s]-() ON (r.source_doc)", idxName, rt)
		if _, err := session.ExecuteWrite(ctx, func(tx neo4j.ManagedTransaction) (any, error) {
			res, err := tx.Run(ctx, stmt, nil)
			if err != nil {
				return nil, err
			}
			return res.Consume(ctx)
		}); err != nil {
			return fmt.Errorf("kag: create source_doc index for %s: %w", rt, err)
		}
	}
	return nil
}

// cleanJSONString 清理可能包裹的 markdown 代码块
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

// cleanRelationName 清理关系名称（Cypher 关系名称只能包含字母、数字和下划线，且不能以数字开头）
func cleanRelationName(s string) string {
	reg := regexp.MustCompile(`[^a-zA-Z0-9_]`)
	s = reg.ReplaceAllString(s, "_")
	if len(s) > 0 && s[0] >= '0' && s[0] <= '9' {
		s = "_" + s
	}
	return strings.ToUpper(s)
}
