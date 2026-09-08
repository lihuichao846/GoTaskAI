package kag

import "testing"

func TestNormalizeCypherRelationNames(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{
			name: "小写关系名转大写",
			in:   "MATCH (a:Entity {name: '张三'})-[:father_of]->(b:Entity) RETURN b.name",
			want: "MATCH (a:Entity {name: '张三'})-[:FATHER_OF]->(b:Entity) RETURN b.name",
		},
		{
			name: "变量关系 r:rel 转大写",
			in:   "MATCH (a:Entity {name: '腾讯'})-[r:headquarters_in]->(b:Entity) RETURN b.name",
			want: "MATCH (a:Entity {name: '腾讯'})-[r:HEADQUARTERS_IN]->(b:Entity) RETURN b.name",
		},
		{
			name: "带路径长度的关系转大写",
			in:   "MATCH (a:Entity {name: '王成'})-[:father_of*1..2]->(b:Entity) RETURN b.name",
			want: "MATCH (a:Entity {name: '王成'})-[:FATHER_OF*1..2]->(b:Entity) RETURN b.name",
		},
		{
			name: "不应误伤节点 label 与属性 map 键",
			in:   "MATCH (a:Entity {name: 'x'})-[:father_of]->(b:Entity) WHERE b.age > 1 RETURN b.name",
			want: "MATCH (a:Entity {name: 'x'})-[:FATHER_OF]->(b:Entity) WHERE b.age > 1 RETURN b.name",
		},
		{
			name: "已是大写的关系名保持不变",
			in:   "MATCH (a:Entity {name: '张三'})-[:FATHER_OF]->(b:Entity) RETURN b.name",
			want: "MATCH (a:Entity {name: '张三'})-[:FATHER_OF]->(b:Entity) RETURN b.name",
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := normalizeCypherRelationNames(c.in)
			if got != c.want {
				t.Fatalf("normalizeCypherRelationNames(%q)\n  got:  %q\n  want: %q", c.in, got, c.want)
			}
		})
	}
}
