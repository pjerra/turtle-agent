package dungeon

import (
	"encoding/csv"
	"math"
	"os"
	"path/filepath"
	"strconv"
	"testing"
)

// The LBRS prep's census decisions, pinned so a later edit cannot quietly undo them (PREP-LOWER-BLACKROCK-SPIRE-2026-10-24):
// the UBRS level at z > 100 is out, the four static rares are pack members, the Mor Grayhoof event pair is out, no pull is over six
// units, every radius covers its own members (and stays short of a neighbour pack), and the patrol blocks carry the exported path.
const (
	lbrsPlan = "../../dungeons/lower_blackrock_spire.json"
	lbrsData = "../../tools/dungeongen/testdata/map229"
)

func lbrsSpawns(t *testing.T) []map[string]string {
	t.Helper()
	f, err := os.Open(filepath.Join(lbrsData, "creatures.tsv"))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	r := csv.NewReader(f)
	r.Comma, r.LazyQuotes = '\t', true
	recs, err := r.ReadAll()
	if err != nil {
		t.Fatal(err)
	}
	var out []map[string]string
	for _, rec := range recs[1:] {
		m := map[string]string{}
		for i, h := range recs[0] {
			m[h] = rec[i]
		}
		out = append(out, m)
	}
	return out
}

func lbrsF(t *testing.T, m map[string]string, k string) float32 {
	t.Helper()
	v, err := strconv.ParseFloat(m[k], 32)
	if err != nil {
		t.Fatalf("creatures.tsv %s = %q", k, m[k])
	}
	return float32(v)
}

func TestLowerBlackrockSpirePlanIsTheCensusFix(t *testing.T) {
	p, err := Load(lbrsPlan)
	if err != nil {
		t.Fatal(err)
	}
	if p.Map != 229 || p.Kind != KindDungeon {
		t.Fatalf("map %d kind %q", p.Map, p.Kind)
	}
	want := []uint32{9196, 9236, 9237, 10596, 9736, 10220, 9568}
	if len(p.Legs) != len(want) {
		t.Fatalf("%d legs, want %d", len(p.Legs), len(want))
	}
	spawns := lbrsSpawns(t)
	type key struct {
		entry   uint32
		x, y, z float32
	}
	spawnAt := map[key]bool{}
	for _, s := range spawns {
		e := uint32(lbrsF(t, s, "entry"))
		spawnAt[key{e, lbrsF(t, s, "x"), lbrsF(t, s, "y"), lbrsF(t, s, "z")}] = true
	}

	rares := map[uint32]bool{9217: false, 9218: false, 9718: false, 10376: false}
	for li, l := range p.Legs {
		if l.Boss == nil || l.Boss.Entry != want[li] {
			t.Fatalf("leg %d boss %+v, want %d", li, l.Boss, want[li])
		}
		if len(l.Skip) != 0 {
			t.Errorf("leg %d has a skip list %v: the rares are members and Mor Grayhoof is out of the plan", li, l.Skip)
		}
		for gi, g := range l.PullGroups {
			if len(g.Entries) > 6 {
				t.Errorf("leg %d group %d: %d units, the census caps a pull at 6", li, gi, len(g.Entries))
			}
			if g.Radius < 5 || g.Radius > 16 {
				t.Errorf("leg %d group %d: radius %.1f outside 5-16", li, gi, g.Radius)
			}
			c := Point{}
			for _, q := range g.Positions {
				c.X += q.X / float32(len(g.Positions))
				c.Y += q.Y / float32(len(g.Positions))
				c.Z += q.Z / float32(len(g.Positions))
			}
			for k, e := range g.Entries {
				q := g.Positions[k]
				if !spawnAt[key{e, q.X, q.Y, q.Z}] {
					t.Errorf("leg %d group %d: %d at %.4f %.4f %.4f is no spawn row", li, gi, e, q.X, q.Y, q.Z)
				}
				if q.Z > 100 {
					t.Errorf("leg %d group %d: %d stands at z %.1f, the UBRS level", li, gi, e, q.Z)
				}
				if e == 16080 || e == 16044 {
					t.Errorf("leg %d group %d: %d is the Mor Grayhoof event, not trash", li, gi, e)
				}
				if d := math.Sqrt(math.Pow(float64(q.X-c.X), 2) + math.Pow(float64(q.Y-c.Y), 2) + math.Pow(float64(q.Z-c.Z), 2)); d > float64(g.Radius) {
					t.Errorf("leg %d group %d: %d stands %.1f yd from the centre, radius %.1f", li, gi, e, d, g.Radius)
				}
				if _, ok := rares[e]; ok {
					rares[e] = true
				}
			}
			if g.Patrol != nil {
				if len(g.Patrol.Path) < 2 || g.Patrol.PeriodS <= 0 || g.Patrol.PeriodS > 900 {
					t.Errorf("leg %d group %d: patrol path %d points, period %.1f", li, gi, len(g.Patrol.Path), g.Patrol.PeriodS)
				}
				if g.Patrol.Source == "" {
					t.Errorf("leg %d group %d: patrol without a source", li, gi)
				}
			}
		}
	}
	for e, found := range rares {
		if !found {
			t.Errorf("rare %d is no pull-group member", e)
		}
	}
	if got := len(p.Fleers); got != 16 {
		t.Errorf("%d fleers, want the 16 EventAI flee entries", got)
	}
	for _, e := range p.KillFirst {
		for _, c := range p.CCPriority {
			if e == c {
				t.Errorf("kill_first %d is also in cc_priority", e)
			}
		}
	}
}
