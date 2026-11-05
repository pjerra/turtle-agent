package planexport

import (
	"strings"
	"testing"
)

// As renames every plan row to the shadow name and nothing else (spec piece 4).
func TestAsRenamesEveryRow(t *testing.T) {
	r := build(t, parsePlan(t, small))
	got := As(r, "tiny_cave_x1").Rows
	if got.Plan.Plan != "tiny_cave_x1" {
		t.Fatalf("plan row %q", got.Plan.Plan)
	}
	if got.Plan.Map != r.Plan.Map || len(got.Legs) != len(r.Legs) || len(got.Steps) != len(r.Steps) {
		t.Fatalf("shadow changed more than the name: %+v", got.Plan)
	}
	for _, l := range got.Legs {
		if l.Plan != "tiny_cave_x1" {
			t.Fatalf("leg row %q", l.Plan)
		}
	}
	for _, s := range got.Steps {
		if s.Plan != "tiny_cave_x1" {
			t.Fatalf("step row %s %q", s.Kind, s.Plan)
		}
	}
	strip := func(r Rows) Rows {
		r.Plan.Plan, r.Plan.Source = "", ""
		for i := range r.Legs {
			r.Legs[i].Plan = ""
		}
		for i := range r.Steps {
			r.Steps[i].Plan = ""
		}
		return r
	}
	r.Plan.Source = got.Plan.Source
	if err := Equal([]Rows{strip(r)}, []Rows{strip(got)}); err != nil {
		t.Fatalf("shadow rows differ: %v", err)
	}
	gotSQL := WriteSQL([]Rows{got})
	if strings.Contains(gotSQL, "uldaman") {
		t.Fatalf("shadow SQL names the base plan: %q", gotSQL[:200])
	}
}

// Shadow DELETEs touch only the shadow name, so the base plan's rows survive the deploy.
func TestAsDeletesOnlyTheShadow(t *testing.T) {
	r := build(t, parsePlan(t, small))
	sql := WriteSQL([]Rows{As(r, "tiny_cave_x1").Rows})
	for _, line := range strings.Split(sql, "\n") {
		if strings.HasPrefix(line, "DELETE FROM") {
			if !strings.Contains(line, "tiny_cave_x1") {
				t.Fatalf("a shadow DELETE misses the shadow name: %s", line)
			}
			if strings.Contains(strings.ReplaceAll(line, "tiny_cave_x1", ""), "tiny_cave") {
				t.Fatalf("a shadow DELETE touches the base plan: %s", line)
			}
		}
	}
}

func TestBasePlan(t *testing.T) {
	for _, c := range []struct{ in, want string }{
		{"zul_gurub_x1", "zul_gurub"}, {"zul_gurub_x2", "zul_gurub"}, {"tiny_cave_x1", "tiny_cave"},
	} {
		if got, ok := BasePlan(c.in); !ok || got != c.want {
			t.Errorf("%s: %q %v, want %q", c.in, got, ok, c.want)
		}
	}
	for _, in := range []string{"zul_gurub", "zul_gurub_x3", "zul_gurub_x", "x1", "zul-gurub_x1", "zul_gurub_X1", strings.Repeat("a", 30) + "_x1"} {
		if got, ok := BasePlan(in); ok {
			t.Errorf("%s: maps to %q, want no mapping", in, got)
		}
	}
}
