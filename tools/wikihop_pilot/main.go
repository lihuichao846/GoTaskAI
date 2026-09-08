// Command wikihop_pilot 是阶段四「小范围试点」的验证工具（Neo4j-only，不调用 LLM，确定性、可复现）。
//
// 它在 wikihop_eval 遗留的图谱上量化「实体/关系归一化」修正方案的收益：
//   1) schema 收敛：关系类型总数、长尾(仅出现1次)占比 在归一化前后的变化；
//   2) 实体重复/碰撞：仅因大小写/空白不同的节点簇，即阶段一「精确匹配空返回」的根因量级；
//   3) 已知失败样本解析：对 3 个实测失败的查询，验证 精确匹配(name) 与 归一化匹配(toLower)
//      之间的命中差异，证明归一化可把「空返回」转为「命中」。
//
// 用法：go run ./tools/wikihop_pilot
package main

import (
	"context"
	"fmt"
	"regexp"
	"sort"
	"strings"

	"gotaskai/internal/config"
	"gotaskai/internal/db"

	"github.com/neo4j/neo4j-go-driver/v5/neo4j"
)

// relSynonym 关系语义等价类映射（Canonical direction），来自阶段二/三选型。
// 此处仅覆盖实测中出现过的长尾变体，用于演示收敛收益；生产应以本体白名单为主。
var relSynonym = map[string]string{
	"headquartered_in": "located_in", "headquarter_in": "located_in",
	"headquartered_at": "located_in", "located_at": "located_in",
	"location_of": "located_in",
	"original_language_of_work": "original_language_of_work",
	"original_lang_of_work":     "original_language_of_work",
	"office_contested":          "contested_office",
	"contested_office":          "contested_office",
	"participates_in":           "part_of", "participated_in": "part_of",
	"part_of": "part_of",
}

var (
	nonAlnumRe   = regexp.MustCompile(`[^a-zA-Z0-9]`)
	collapseSpRe = regexp.MustCompile(`\s+`)
)

// normalizeRel 关系名归一化：strip 非字母数字 -> 下划线、去冗余空格、统一小写。
func normalizeRel(s string) string {
	s = collapseSpRe.ReplaceAllString(strings.TrimSpace(s), " ")
	s = nonAlnumRe.ReplaceAllString(s, "_")
	s = strings.ToLower(s)
	return strings.Trim(s, "_")
}

// canonRel 施加语义等价类收敛。
func canonRel(s string) string {
	n := normalizeRel(s)
	if canon, ok := relSynonym[n]; ok {
		return canon
	}
	return n
}

type runner struct {
	ctx context.Context
	ses neo4j.SessionWithContext
}

func (r *runner) run(cypher string, params ...map[string]any) ([]map[string]any, error) {
	var p map[string]any
	if len(params) > 0 {
		p = params[0]
	}
	var out []map[string]any
	_, err := r.ses.ExecuteRead(r.ctx, func(tx neo4j.ManagedTransaction) (any, error) {
		res, err := tx.Run(r.ctx, cypher, p)
		if err != nil {
			return nil, err
		}
		for res.Next(r.ctx) {
			out = append(out, res.Record().AsMap())
		}
		return nil, res.Err()
	})
	return out, err
}

type relType struct {
	name  string
	count int
}

func main() {
	config.InitConfig("config/config.yaml")
	ctx := context.Background()
	driver := db.InitNeo4j(config.AppConfig.Neo4j.URI, config.AppConfig.Neo4j.Username, config.AppConfig.Neo4j.Password)
	defer driver.Close(ctx)

	ses := driver.NewSession(ctx, neo4j.SessionConfig{AccessMode: neo4j.AccessModeRead})
	defer ses.Close(ctx)
	r := &runner{ctx: ctx, ses: ses}

	fmt.Println("===== 阶段四试点：实体/关系归一化修正收益（Neo4j-only，确定性）=====")

	// ---- 0. 图规模 ----
	printScalar(r, "节点数", `MATCH (n:Entity) RETURN count(n) AS v`)
	printScalar(r, "关系数", `MATCH ()-[r]->() RETURN count(r) AS v`)

	// ---- 1. 关系 schema 收敛 ----
	rows, err := r.run(`MATCH ()-[r]->() WITH type(r) AS t, count(*) AS c RETURN t, c`)
	if err != nil || len(rows) == 0 {
		fmt.Printf("\n[提示] 图谱无关系（%v），跳过 schema 收敛评估。\n", err)
	} else {
		var types []relType
		for _, row := range rows {
			var cnt int64
			switch c := row["c"].(type) {
			case int64:
				cnt = c
			case float64:
				cnt = int64(c)
			}
			types = append(types, relType{fmt.Sprintf("%v", row["t"]), int(cnt)})
		}
		// 长尾优先输出，便于核对是否纳入本体白名单
		sort.Slice(types, func(i, j int) bool { return types[i].count < types[j].count })
		fmt.Printf("\n--- 关系类型 schema 收敛（原始 %d 种）---\n", len(types))
		fmt.Println("Top-25 长尾关系（低次数）:")
		for i := 0; i < len(types) && i < 25; i++ {
			fmt.Printf("  %-46s x%d\n", types[i].name, types[i].count)
		}

		before := len(types)
		normCnt, canonCnt := map[string]int{}, map[string]int{}
		normOne, canonOne := 0, 0
		for _, t := range types {
			normCnt[normalizeRel(t.name)] += t.count
			canonCnt[canonRel(t.name)] += t.count
		}
		for _, c := range normCnt {
			if c == 1 {
				normOne++
			}
		}
		for _, c := range canonCnt {
			if c == 1 {
				canonOne++
			}
		}
		fmt.Printf("\n[关系收敛] 原始=%d\n", before)
		fmt.Printf("  -> 仅大小写折叠+下划线: %d 种 (降 %.0f%%), 长尾(1次)占比=%.0f%%\n",
			len(normCnt), deltaPct(before, len(normCnt)), pct(len(types), normOne))
		if len(canonCnt) < len(normCnt) {
			fmt.Printf("  -> 叠加语义等价类(如 headquartered_in/located_in): %d 种 (较归一化再降 %d 种), 长尾占比=%.0f%%\n",
				len(canonCnt), len(normCnt)-len(canonCnt), pct(len(types), canonOne))
		}
	}

	// ---- 2. 大小写/空白碰撞节点簇（实体关联错误根因量级） ----
	dupRows, err := r.run(`MATCH (n:Entity)
		WITH toLower(replace(replace(n.name,' ',''),'-',' ')) AS k, collect(n) AS ns
		WHERE size(ns) > 1
		RETURN k, [x IN ns | x.name] AS names, size(ns) AS cnt
		ORDER BY cnt DESC LIMIT 30`)
	if err != nil {
		fmt.Printf("[2] 归一化碰撞查询失败: %v\n", err)
	} else {
		totalExtra := 0
		for _, g := range dupRows {
			if a, ok := g["names"].([]any); ok {
				totalExtra += len(a) - 1
			}
		}
		fmt.Printf("\n--- 实体名仅大小写/空白差异的碰撞簇（阶段一「精确匹配空返回」根因量级）---\n")
		fmt.Printf("碰撞簇数=%d, 造成「不可见/重复」的额外节点数=%d\n", len(dupRows), totalExtra)
		for i, g := range dupRows {
			if i >= 8 {
				fmt.Printf("  ... 其余 %d 簇略\n", len(dupRows)-8)
				break
			}
			fmt.Printf("  键=%-40q 节点数=%v  名称=%v\n", g["k"], g["cnt"], g["names"])
		}
		if totalExtra == 0 {
			fmt.Println("  [提示] 暂无仅大小写/空白不同的碰撞，需扩大样本或依赖实体消解再做分析。")
		}
	}

	// ---- 3. 已知失败样本：精确匹配 vs 归一化匹配 ----
	known := []struct {
		note      string
		mention   string
		exactName string
	}{
		{"original_language_of_work solva sawan", "solva sawan", "Solva Sawan"},
		{"located_in upper bicutan national high school", "upper bicutan national high school", "Upper Bicutan National High School"},
		{"office_contested danish general election", "danish general election", "danish general election"},
	}
	fmt.Println("\n--- 已知失败样本：精确匹配(name) vs 归一化匹配(toLower trim) ---")
	for _, k := range known {
		exact := r.scalarInt(`MATCH (n:Entity) WHERE n.name = $x RETURN count(n) AS v`, k.exactName)
		normNames := r.stringCol(`MATCH (n:Entity) WHERE toLower(trim(n.name)) = $x RETURN n.name AS name`, k.mention)
		fmt.Printf("  %-48s 提及=%q\n", k.note, k.mention)
		fmt.Printf("     精确匹配 name=%q 命中节点数=%d\n", k.exactName, exact)
		if len(normNames) == 0 {
			fmt.Printf("     归一化匹配命中=0（该实体未建图，或名称差异超出去空白/大小写）\n")
		} else {
			fmt.Printf("     归一化匹配命中的真实节点名=%v\n", normNames)
		}
		// 若该实体已建图，列出其邻接关系，核对「为何生成的 Cypher 未命中」
		if exact >= 1 {
			fmt.Printf("     该实体邻接连接(仅展示关系类型与末端):\n")
			for _, line := range relsOf(r, normNames[0]) {
				fmt.Printf("       %s\n", line)
			}
		}
	}
}

// relsOf 返回某实体的所有邻接连接「type -> 末端实体名」与「<- type 源实体名」。
func relsOf(r *runner, name string) []string {
	rows, err := r.run(`MATCH (n:Entity {name:$x})-[rel]->(m) RETURN type(rel) AS t, m.name AS m
		UNION
		MATCH (n:Entity {name:$x})<-[rel]-(m) RETURN type(rel) AS t, m.name AS m`,
		map[string]any{"x": name})
	if err != nil {
		return []string{fmt.Sprintf("<err: %v>", err)}
	}
	var out []string
	for _, row := range rows {
		t := row["t"]
		mv := row["m"]
		nameOf := func(v any) string {
			if s, ok := v.(string); ok {
				return s
			}
			return fmt.Sprintf("%v", v)
		}
		// 无法区分方向，统一标注，仅在报告中呈现关系类型与末端实体
		out = append(out, fmt.Sprintf("%-36s -> %s", fmt.Sprintf("%v", t), nameOf(mv)))
	}
	return out
}

// ---------- 查询辅助 ----------

func (r *runner) scalarInt(cypher, paramVal string) int {
	rows, err := r.run(cypher, map[string]any{"x": paramVal})
	if err != nil || len(rows) == 0 {
		return -1
	}
	for _, v := range rows[0] {
		switch t := v.(type) {
		case int64:
			return int(t)
		case float64:
			return int(t)
		}
	}
	return 0
}

func (r *runner) stringCol(cypher, paramVal string) []string {
	rows, err := r.run(cypher, map[string]any{"x": paramVal})
	if err != nil {
		return nil
	}
	var out []string
	for _, row := range rows {
		for _, v := range row {
			if s, ok := v.(string); ok {
				out = append(out, s)
			}
		}
	}
	return out
}

func printScalar(r *runner, caption, cypher string) {
	rows, err := r.run(cypher)
	if err != nil {
		fmt.Printf("%s: 查询失败 %v\n", caption, err)
		return
	}
	if len(rows) > 0 {
		for _, v := range rows[0] {
			fmt.Printf("%s: %v\n", caption, v)
		}
	}
}

func pct(total, n int) float64 {
	if total == 0 {
		return 0
	}
	return float64(n) / float64(total) * 100
}

func deltaPct(before, after int) float64 {
	if before == 0 {
		return 0
	}
	return float64(before-after) / float64(before) * 100
}
