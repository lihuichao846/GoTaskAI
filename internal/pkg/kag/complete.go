package kag

import (
	"context"
	"fmt"
	"log"
	"strings"

	"github.com/neo4j/neo4j-go-driver/v5/neo4j"
)

// GapMine 挖掘「文档有、图里无」的缺失三元组（主导根因：抽取覆盖不足 / 事实缺失）。
// 流程：1) 本体引导抽取三元组；2) 逐个做图内存在性校验；3) 返回图里缺失的三元组。
func (k *KAGManager) GapMine(ctx context.Context, text string) ([]EntityRelation, error) {
	rels, err := k.ExtractKnowledge(ctx, text)
	if err != nil {
		return nil, err
	}

	missing := make([]EntityRelation, 0, len(rels))
	for _, r := range rels {
		exists, err := k.tripleExists(ctx, r)
		if err != nil {
			return nil, err
		}
		if !exists {
			missing = append(missing, r)
		}
	}
	return missing, nil
}

// CompleteGraph 补全图谱：挖掘缺失三元组并直接补建（MERGE 幂等），返回补建条数。
func (k *KAGManager) CompleteGraph(ctx context.Context, text string) (int, error) {
	missing, err := k.GapMine(ctx, text)
	if err != nil {
		return 0, err
	}
	if len(missing) == 0 {
		return 0, nil
	}
	if err := k.IngestGraph(ctx, missing); err != nil {
		return 0, err
	}
	return len(missing), nil
}

// tripleExists 判断三元组 source -[relation]-> target 是否已存在于图（起点/终点按规范化名匹配）。
func (k *KAGManager) tripleExists(ctx context.Context, r EntityRelation) (bool, error) {
	rel := cleanRelationName(r.Relation)
	if rel == "" || strings.TrimSpace(r.Source) == "" || strings.TrimSpace(r.Target) == "" {
		return false, nil
	}

	session := k.neo4jDriver.NewSession(ctx, neo4j.SessionConfig{AccessMode: neo4j.AccessModeRead})
	defer session.Close(ctx)

	result, err := session.ExecuteRead(ctx, func(tx neo4j.ManagedTransaction) (any, error) {
		cypher := fmt.Sprintf(`
			MATCH (a:Entity)-[r:%s]->(b:Entity)
			WHERE toLower(trim(a.name)) = toLower(trim($source)) AND toLower(trim(b.name)) = toLower(trim($target))
			RETURN count(r) AS c`, rel)
		res, err := tx.Run(ctx, cypher, map[string]any{"source": r.Source, "target": r.Target})
		if err != nil {
			return nil, err
		}
		if res.Next(ctx) {
			v, _ := res.Record().Get("c")
			return v, nil
		}
		return int64(0), res.Err()
	})
	if err != nil {
		log.Printf("[KAG] triple exists check failed: %v", err)
		return false, err
	}

	switch v := result.(type) {
	case int64:
		return v > 0, nil
	case float64:
		return v > 0, nil
	}
	return false, nil
}
