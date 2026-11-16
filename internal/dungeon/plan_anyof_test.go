package dungeon

import (
	"fmt"
	"slices"
	"strings"
	"testing"
)

// anyOfSrc is a valid plan: legs[0] has a pull group and the any_of boss the caller writes, legs[1] has a pull group
// (entry 7) and its own boss 103, legs[2] its boss 120. The clash cases below move one of those entries into the any_of
// list.
func anyOfSrc(boss0, boss1 string) string {
	return `{"version":1,"name":"t","map":1,"entrance":{"x":0,"y":0,"z":0},"legs":[
 {"name":"one","to":{"x":1,"y":0,"z":0},"pull_groups":[{"entries":[5],"positions":[{"x":2,"y":0,"z":0}]}],
  "boss":{` + boss0 + `,"position":{"x":3,"y":0,"z":0}}},
 {"name":"two","to":{"x":4,"y":0,"z":0},"pull_groups":[{"entries":[7],"positions":[{"x":5,"y":0,"z":0}]}],
  "boss":{` + boss1 + `,"position":{"x":6,"y":0,"z":0}}},
 {"name":"three","to":{"x":7,"y":0,"z":0},"boss":{"entry":120,"position":{"x":8,"y":0,"z":0}}}]}`
}

// TestBossAnyOfValidation: boss.any_of is the one boss of several an event makes active. It needs 2-16 distinct entries,
// one of them the row's canonical entry, and neither a `first` list nor a hold (the exported row's entries column can
// mean only one thing). No alternative may be another leg's boss, phase entry or pulled pack entry, because raid-walk
// counts a kill of any of them as this row's boss.
func TestBossAnyOfValidation(t *testing.T) {
	valid := anyOfSrc(`"entry":100,"any_of":[100,101,102]`, `"entry":103`)
	p, err := Parse("t.json", []byte(valid))
	if err != nil {
		t.Fatalf("a valid any_of boss was refused: %v", err)
	}
	b := p.Legs[0].Boss
	if b == nil || !slices.Equal(b.AnyOf, []uint32{100, 101, 102}) {
		t.Fatalf("boss = %+v, want any_of [100 101 102]", b)
	}
	for _, e := range []uint32{100, 101, 102} {
		if !b.Is(e) {
			t.Errorf("Boss.Is(%d) = false, want true: it is the row's entry or one of its alternatives", e)
		}
	}
	if b.Is(103) || b.Is(0) {
		t.Errorf("Boss.Is(103) or Is(0) = true: neither is a unit of the row")
	}
	if got, want := b.Units(), []uint32{100, 101, 102}; !slices.Equal(got, want) {
		t.Errorf("Boss.Units() = %v, want %v (the canonical entry first, each alternative once)", got, want)
	}
	// The file stays in Marshal's form: a reparse of the marshalled plan is the same plan, and any_of is written.
	out, err := Marshal(p)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	if !strings.Contains(string(out), `"any_of": [100, 101, 102]`) {
		t.Fatalf("Marshal lacks any_of:\n%s", out)
	}
	q, err := Parse("t.json", out)
	if err != nil || q.Legs[0].Boss == nil || !slices.Equal(q.Legs[0].Boss.AnyOf, []uint32{100, 101, 102}) {
		t.Fatalf("a marshalled any_of boss did not read back: %v", err)
	}

	// A boss without any_of has just its entry as a unit.
	if got := (&Boss{Entry: 100}).Units(); !slices.Equal(got, []uint32{100}) {
		t.Errorf("Boss.Units() without any_of = %v, want [100]", got)
	}
	var nilBoss *Boss
	if nilBoss.Is(100) || nilBoss.Units() != nil {
		t.Error("a nil boss is nobody's and has no units")
	}

	long := make([]string, 0, MaxAnyOfEntries+1)
	for e := uint32(100); len(long) < MaxAnyOfEntries+1; e++ {
		long = append(long, fmt.Sprint(e))
	}
	longList := strings.Join(long, ",")

	for _, tc := range []struct {
		name  string
		boss0 string
		boss1 string
		want  string
	}{
		{
			name:  "one entry",
			boss0: `"entry":100,"any_of":[100]`,
			boss1: `"entry":103`,
			want:  "legs[0].boss.any_of: 1 entries: list 2-16 alternatives",
		},
		{
			name:  "seventeen entries",
			boss0: `"entry":100,"any_of":[` + longList + `]`,
			boss1: `"entry":103`,
			want:  "legs[0].boss.any_of: 17 entries: list 2-16 alternatives",
		},
		{
			name:  "a duplicate",
			boss0: `"entry":100,"any_of":[100,101,101]`,
			boss1: `"entry":103`,
			want:  "legs[0].boss.any_of[2]: 101 is listed twice",
		},
		{
			name:  "a zero",
			boss0: `"entry":100,"any_of":[100,0,102]`,
			boss1: `"entry":103`,
			want:  "legs[0].boss.any_of[1]: 0 is not a creature entry",
		},
		{
			name:  "the entry is not among them",
			boss0: `"entry":100,"any_of":[101,102]`,
			boss1: `"entry":103`,
			want:  "legs[0].boss.entry: 100 is not in any_of",
		},
		{
			name:  "with a first add",
			boss0: `"entry":100,"any_of":[100,101],"first":[99]`,
			boss1: `"entry":103`,
			want:  "legs[0].boss.any_of: a boss with any_of has no first",
		},
		{
			name:  "with a hold",
			boss0: `"entry":100,"any_of":[100,101],"hold":{"entries":[99]}`,
			boss1: `"entry":103`,
			want:  "legs[0].boss.any_of: a boss with any_of has no hold",
		},
		{
			name:  "another leg's pulled pack entry",
			boss0: `"entry":100,"any_of":[100,7,102]`,
			boss1: `"entry":103`,
			want:  "legs[0].boss.any_of[1]: any_of entry 7 of the boss of legs[0] (one) is also in legs[1] (two).pull_groups[0].entries",
		},
		{
			name:  "another leg's boss entry",
			boss0: `"entry":100,"any_of":[100,103,102]`,
			boss1: `"entry":103`,
			want:  "legs[0].boss.any_of[1]: any_of entry 103 of the boss of legs[0] (one) is also the boss entry of legs[1] (two)",
		},
		{
			name:  "another leg's phase entry",
			boss0: `"entry":100,"any_of":[100,105,102]`,
			boss1: `"entry":103,"phase_entries":[105]`,
			want:  "legs[0].boss.any_of[1]: any_of entry 105 of the boss of legs[0] (one) is also in legs[1] (two).boss.phase_entries",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := Parse("t.json", []byte(anyOfSrc(tc.boss0, tc.boss1)))
			if err == nil {
				t.Fatalf("want a refusal with %q", tc.want)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error %q, want it to contain %q", err, tc.want)
			}
		})
	}
}
