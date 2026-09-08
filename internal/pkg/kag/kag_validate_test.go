package kag

import "testing"

func TestValidateReadOnlyCypher(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want bool
	}{
		{"合法只读 MATCH", "MATCH (a:Entity {name: '张三'})-[:FATHER_OF]->(b) RETURN b.name", true},
		{"非 MATCH 开头", "RETURN 1", false},
		{"含写操作 CREATE", "MATCH (a:Entity) CREATE (b:Entity {name: 'x'}) RETURN b", false},
		{"含 MERGE", "MATCH (a:Entity) MERGE (a)-[:X]->(b:Entity) RETURN b", false},
		{"含 DELETE", "MATCH (a:Entity)-[r]->(b) DELETE r", false},
		{"含 DETACH DELETE", "MATCH (a:Entity) DETACH DELETE a", false},
		{"含 CALL 子查询", "MATCH (a:Entity) CALL db.indexes() RETURN a", false},
		{"含 SET 写属性", "MATCH (a:Entity {name: 'x'}) SET a.age = 1 RETURN a", false},
		{"多语句用分号分隔", "MATCH (a:Entity) RETURN a; DROP INDEX x", false},
		{"单行注释注入", "MATCH (a:Entity) RETURN a // drop all", false},
		{"块注释注入", "MATCH (a:Entity) RETURN a /* delete */", false},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := validateReadOnlyCypher(c.in); got != c.want {
				t.Fatalf("validateReadOnlyCypher(%q) = %v, want %v", c.in, got, c.want)
			}
		})
	}
}
