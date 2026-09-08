package kag

import (
	"strings"
	"testing"
)

func TestCanonicalizeRelation(t *testing.T) {
	o := DefaultRelationOntology()

	cases := []struct {
		name string
		in   string
		want string
	}{
		{"大小写折叠", "located_in", "LOCATED_IN"},
		{"语义等价类映射", "headquartered_in", "LOCATED_IN"},
		{"等价类(非字母数字转下划线)", "headquartered in", "LOCATED_IN"},
		{"已规范关系保持不变", "DIRECTED_BY", "DIRECTED_BY"},
		{"非白名单关系保留原样(大写)", "some_custom_relation", "SOME_CUSTOM_RELATION"},
		{"数字开头加下划线前缀", "1st_place", "_1ST_PLACE"},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := o.CanonicalizeRelation(c.in); got != c.want {
				t.Fatalf("CanonicalizeRelation(%q) = %q, want %q", c.in, got, c.want)
			}
		})
	}
}

func TestCanonicalizeRelationNilOntology(t *testing.T) {
	var o *RelationOntology
	if got := o.CanonicalizeRelation("father_of"); got != "FATHER_OF" {
		t.Fatalf("nil ontology should fall back to cleanRelationName, got %q", got)
	}
}

func TestWhitelistHint(t *testing.T) {
	var o *RelationOntology
	if got := o.whitelistHint(); got != "" {
		t.Fatalf("nil ontology hint should be empty, got %q", got)
	}

	o = &RelationOntology{Canonical: []string{"A", "B"}}
	if got := o.whitelistHint(); got != "A, B" {
		t.Fatalf("hint should join canonical names, got %q", got)
	}
}

func TestFormatGraphContext(t *testing.T) {
	t.Run("有事实输出证据链含层级", func(t *testing.T) {
		facts := []graphFact{
			{Source: "A", Rel: "LOCATED_IN", Target: "B", Hop: 1},
			{Source: "B", Rel: "PART_OF", Target: "C", Hop: 2},
		}
		got := formatGraphContext([]string{"A"}, facts)
		if !strings.Contains(got, "LOCATED_IN") || !strings.Contains(got, "hop=1") || !strings.Contains(got, "hop=2") {
			t.Fatalf("unexpected format: %s", got)
		}
	})

	t.Run("无事实输出缺失提示", func(t *testing.T) {
		got := formatGraphContext([]string{"X"}, nil)
		if !strings.Contains(got, "缺失提示") || !strings.Contains(got, "X") {
			t.Fatalf("expected degradation hint, got %s", got)
		}
	})
}

func TestDedupeStrings(t *testing.T) {
	got := dedupeStrings([]string{"a", "", "b", "a", "c"})
	want := []string{"a", "b", "c"}
	if len(got) != len(want) {
		t.Fatalf("dedupeStrings = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("dedupeStrings = %v, want %v", got, want)
		}
	}
}
