package kag

import "strings"

// RelationOntology 描述「关系本体」：白名单规范关系 + 语义等价类映射。
// 试点已证：字符串级归一化对关系收敛几乎零贡献（744→742），关系收敛只能靠抽取侧本体白名单，
// 故抽取时用白名单约束 LLM、用等价类把变体归一到规范关系。
type RelationOntology struct {
	// Canonical 白名单规范关系名（统一大写）。抽取时约束 LLM 只能产出这些关系。
	Canonical []string
	// Alias 语义等价类：变体关系名（大写）-> 规范关系名（大写），如 "HEADQUARTERED_IN" -> "LOCATED_IN"。
	Alias map[string]string
}

// CanonicalizeRelation 把任意关系名映射到规范关系名：
//  1. 先 cleanRelationName（非字母数字 -> 下划线 + 大写）；
//  2. 命中语义等价类 Alias 则映射到规范名；
//  3. 否则返回归一化后的原关系名（不在白名单内的关系保留原样，交由下游决定）。
func (o *RelationOntology) CanonicalizeRelation(rel string) string {
	norm := cleanRelationName(rel)
	if o == nil {
		return norm
	}
	if canon, ok := o.Alias[norm]; ok {
		return canon
	}
	return norm
}

// whitelistHint 生成注入抽取 prompt 的白名单关系列表文本；白名单为空返回 ""。
func (o *RelationOntology) whitelistHint() string {
	if o == nil || len(o.Canonical) == 0 {
		return ""
	}
	return strings.Join(o.Canonical, ", ")
}

// DefaultRelationOntology 返回内置的通用关系本体（经验起步集；≤120 目标需结合业务在 T0 落地时校准）。
// 覆盖空间/行政归属、影视作品、人物机构、人物属性、亲属社会、事件参与等常见事实关系。
func DefaultRelationOntology() *RelationOntology {
	return &RelationOntology{
		Canonical: []string{
			// 空间/行政归属
			"LOCATED_IN", "PART_OF", "HAS_PART", "CAPITAL_OF", "COUNTRY_OF", "CITY_OF", "REGION_OF",
			// 影视/作品
			"DIRECTED_BY", "PRODUCED_BY", "WRITTEN_BY", "MUSIC_COMPOSED_BY", "CINEMATOGRAPHY_BY",
			"STARRING", "CAST_MEMBER", "PERFORMED_BY", "IS_REMAKE_OF", "BASED_ON", "ADAPTED_FROM",
			"SEQUEL_OF", "PREQUEL_OF", "ORIGINAL_LANGUAGE_OF_WORK", "LANGUAGE_OF", "GENRE_OF",
			// 人物/机构关系
			"AUTHOR_OF", "CREATOR_OF", "FOUNDED_BY", "OWNED_BY", "OPERATED_BY", "MEMBER_OF",
			"EMPLOYEE_OF", "WORKS_FOR", "LEADER_OF", "HEAD_OF",
			// 人物属性
			"OCCUPATION", "PROFESSION", "NATIONALITY", "BORN_IN", "DIED_IN", "BORN_ON", "DIED_ON",
			// 亲属/社会关系
			"SPOUSE_OF", "PARENT_OF", "CHILD_OF", "SIBLING_OF", "FATHER_OF", "MOTHER_OF",
			// 事件/参与
			"PARTICIPATES_IN", "CONTESTED_OFFICE",
		},
		Alias: map[string]string{
			"HEADQUARTERED_IN":      "LOCATED_IN",
			"HEADQUARTER_IN":        "LOCATED_IN",
			"HEADQUARTERED_AT":      "LOCATED_IN",
			"HEADQUARTERS_IN":       "LOCATED_IN",
			"LOCATED_AT":            "LOCATED_IN",
			"ORIGINAL_LANG_OF_WORK": "ORIGINAL_LANGUAGE_OF_WORK",
			"OFFICE_CONTESTED":      "CONTESTED_OFFICE",
			"PARTICIPATED_IN":       "PARTICIPATES_IN",
			"BORN_AT":               "BORN_IN",
			"DIED_AT":               "DIED_IN",
			"WORKED_AT":             "WORKS_FOR",
		},
	}
}
