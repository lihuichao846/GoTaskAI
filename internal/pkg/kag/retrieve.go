package kag

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"strings"

	"github.com/neo4j/neo4j-go-driver/v5/neo4j"
)

// defaultMaxHops 多跳松弛检索的最大展开跳数（含放宽）。
// 试点显示多跳停在中转层是命中率低的根因之一，适度加深跳数并用 topN 截断控制注入体积。
const defaultMaxHops = 3

// graphFact 表示一条从 Source 到 Target 的邻接事实，Hop 记录距起点实体的跳数（用于标注层级，避免中间层冒充终答）。
type graphFact struct {
	Source string
	Rel    string
	Target string
	Hop    int
}

// neighbor 表示某实体的一条邻接关系；Incoming 为 true 表示 Target -[Rel]-> 当前实体（入边）。
type neighbor struct {
	Rel      string
	Target   string
	Incoming bool
}

// RetrieveGraphContext 多跳松弛检索：定位起点实体 → 逐跳展开邻接事实 → 终点缺失时降级返回证据链 + 缺失提示。
// 替代旧的「LLM 一步生成单条 Cypher」方案：后者在 744 种关系下几乎必然生成不一致的关系名而空返回。
func (k *KAGManager) RetrieveGraphContext(ctx context.Context, question string, topN int) (string, error) {
	// 1. 起点定位：解析问题中提到的实体并在图中匹配（精确 + 归一化）。
	starts, err := k.locateStartEntities(ctx, question)
	if err != nil {
		log.Printf("[KAG] locate start entities failed: %v", err)
	}
	if len(starts) == 0 {
		// 起点不在图内（抽取覆盖缺口），无法检索，返回空。
		return "", nil
	}

	// 2. 逐跳展开，收集邻接事实（关系名经本体归一，并标注跳数）。
	facts := k.expandNeighbors(ctx, starts, defaultMaxHops, topN)

	// 3. 组装返回：有事实输出证据链；无事实输出缺失提示（而非空）。
	return formatGraphContext(starts, facts), nil
}

// locateStartEntities 返回图中与问题相关的起点实体名（去重）。
func (k *KAGManager) locateStartEntities(ctx context.Context, question string) ([]string, error) {
	mentions := k.extractMentions(ctx, question)
	var names []string
	if len(mentions) > 0 {
		for _, m := range mentions {
			names = append(names, k.matchEntity(ctx, m)...)
		}
	} else {
		// LLM 提及抽取失败时退化为确定性匹配：图里名称作为问题子串的实体。
		names = k.matchBySubstring(ctx, question)
	}
	return dedupeStrings(names), nil
}

// extractMentions 用 LLM 从问题中提取实体提及（人名/地名/组织/作品名等）。
func (k *KAGManager) extractMentions(ctx context.Context, question string) []string {
	systemPrompt := `你是实体识别助手。请从用户问题中提取所有提到的实体名称（人名、地名、组织名、作品名等）。
严格只返回 JSON 字符串数组，不要任何解释或 markdown 代码块。`
	userPrompt := "问题：" + question

	resp, err := k.llmClient.Generate(ctx, systemPrompt, nil, userPrompt, nil)
	if err != nil {
		log.Printf("[KAG] extract mentions failed: %v", err)
		return nil
	}
	resp = cleanJSONString(resp)

	var mentions []string
	if err := json.Unmarshal([]byte(resp), &mentions); err != nil {
		log.Printf("[KAG] parse mentions failed: %v, raw: %s", err, resp)
		return nil
	}
	return mentions
}

// matchEntity 在图里匹配实体提及：先精确 name，再归一化（toLower/trim）。
func (k *KAGManager) matchEntity(ctx context.Context, mention string) []string {
	session := k.neo4jDriver.NewSession(ctx, neo4j.SessionConfig{AccessMode: neo4j.AccessModeRead})
	defer session.Close(ctx)

	result, err := session.ExecuteRead(ctx, func(tx neo4j.ManagedTransaction) (any, error) {
		res, err := tx.Run(ctx,
			`MATCH (n:Entity) WHERE n.name = $exact OR toLower(trim(n.name)) = $norm RETURN n.name AS name LIMIT 10`,
			map[string]any{"exact": mention, "norm": strings.ToLower(strings.TrimSpace(mention))})
		if err != nil {
			return nil, err
		}
		var names []string
		for res.Next(ctx) {
			if v, ok := res.Record().Get("name"); ok {
				names = append(names, fmt.Sprintf("%v", v))
			}
		}
		return names, res.Err()
	})
	if err != nil {
		log.Printf("[KAG] match entity %q failed: %v", mention, err)
		return nil
	}
	if names, ok := result.([]string); ok {
		return names
	}
	return nil
}

// matchBySubstring 确定性兜底：返回图中名称作为问题子串（大小写不敏感）的实体。
func (k *KAGManager) matchBySubstring(ctx context.Context, question string) []string {
	q := strings.ToLower(question)
	session := k.neo4jDriver.NewSession(ctx, neo4j.SessionConfig{AccessMode: neo4j.AccessModeRead})
	defer session.Close(ctx)

	result, err := session.ExecuteRead(ctx, func(tx neo4j.ManagedTransaction) (any, error) {
		res, err := tx.Run(ctx, `MATCH (n:Entity) RETURN n.name AS name LIMIT 2000`, nil)
		if err != nil {
			return nil, err
		}
		var names []string
		for res.Next(ctx) {
			if v, ok := res.Record().Get("name"); ok {
				name := fmt.Sprintf("%v", v)
				if strings.Contains(q, strings.ToLower(name)) {
					names = append(names, name)
				}
			}
		}
		return names, res.Err()
	})
	if err != nil {
		log.Printf("[KAG] match by substring failed: %v", err)
		return nil
	}
	if names, ok := result.([]string); ok {
		return names
	}
	return nil
}

// expandNeighbors 从起点集合出发做 BFS，收集最多 maxHops 层邻接事实，用 topN 截断（<=0 不截断）。
func (k *KAGManager) expandNeighbors(ctx context.Context, starts []string, maxHops int, topN int) []graphFact {
	var facts []graphFact
	visited := make(map[string]bool, len(starts))
	frontier := make([]string, 0, len(starts))
	for _, s := range starts {
		if !visited[s] {
			visited[s] = true
			frontier = append(frontier, s)
		}
	}

	for hop := 1; hop <= maxHops; hop++ {
		var next []string
		for _, name := range frontier {
			for _, nb := range k.queryNeighbors(ctx, name) {
				src, dst := name, nb.Target
				if nb.Incoming {
					src, dst = nb.Target, name
				}
				facts = append(facts, graphFact{
					Source: src,
					Rel:    k.ontology.CanonicalizeRelation(nb.Rel),
					Target: dst,
					Hop:    hop,
				})
				if topN > 0 && len(facts) >= topN {
					return facts
				}
				if !visited[nb.Target] {
					visited[nb.Target] = true
					next = append(next, nb.Target)
				}
			}
		}
		if len(next) == 0 {
			break
		}
		frontier = next
	}
	return facts
}

// queryNeighbors 返回某实体的一跳邻接（出边 + 入边）。
func (k *KAGManager) queryNeighbors(ctx context.Context, name string) []neighbor {
	session := k.neo4jDriver.NewSession(ctx, neo4j.SessionConfig{AccessMode: neo4j.AccessModeRead})
	defer session.Close(ctx)

	result, err := session.ExecuteRead(ctx, func(tx neo4j.ManagedTransaction) (any, error) {
		var out []neighbor
		collect := func(cypher string, incoming bool) error {
			res, err := tx.Run(ctx, cypher, map[string]any{"name": name})
			if err != nil {
				return err
			}
			for res.Next(ctx) {
				rec := res.Record()
				rel, _ := rec.Get("rel")
				target, _ := rec.Get("target")
				out = append(out, neighbor{
					Rel:      fmt.Sprintf("%v", rel),
					Target:   fmt.Sprintf("%v", target),
					Incoming: incoming,
				})
			}
			return res.Err()
		}
		if err := collect(`MATCH (n:Entity {name: $name})-[r]->(m:Entity) RETURN type(r) AS rel, m.name AS target`, false); err != nil {
			return nil, err
		}
		if err := collect(`MATCH (n:Entity {name: $name})<-[r]-(m:Entity) RETURN type(r) AS rel, m.name AS target`, true); err != nil {
			return nil, err
		}
		return out, nil
	})
	if err != nil {
		log.Printf("[KAG] query neighbors for %q failed: %v", name, err)
		return nil
	}
	if out, ok := result.([]neighbor); ok {
		return out
	}
	return nil
}

// formatGraphContext 组装图谱上下文：输出证据链（含跳数层级），无事实时输出缺失提示（而非空）。
func formatGraphContext(starts []string, facts []graphFact) string {
	if len(facts) == 0 {
		return fmt.Sprintf("[缺失提示] 起点实体「%s」已建图，但未发现可达邻接关系（可能为抽取覆盖不足导致事实缺失）。", strings.Join(starts, "、"))
	}
	var b strings.Builder
	for _, f := range facts {
		fmt.Fprintf(&b, "%s -[%s]-> %s (hop=%d)\n", f.Source, f.Rel, f.Target, f.Hop)
	}
	return strings.TrimSpace(b.String())
}

// dedupeStrings 保序去重字符串切片。
func dedupeStrings(in []string) []string {
	seen := make(map[string]bool, len(in))
	out := make([]string, 0, len(in))
	for _, s := range in {
		if s == "" || seen[s] {
			continue
		}
		seen[s] = true
		out = append(out, s)
	}
	return out
}
