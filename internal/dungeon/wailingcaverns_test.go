package dungeon

import (
	"encoding/csv"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/pjerra/turtle-agent/internal/navmesh"
)

const (
	wailingPlan  = "../../dungeons/wailing_caverns.json"
	wailingExtra = "../../dungeons/wailing_caverns-extra.json"
	wailingData  = "../../tools/dungeongen/testdata/map43" // the world database export the plans were written from
)

type wcSpawn struct {
	entry uint32
	pos   Point
}

// wcSpawns reads the creature spawns of the export.
func wcSpawns(t *testing.T) []wcSpawn {
	t.Helper()
	f, err := os.Open(filepath.Join(wailingData, "creatures.tsv"))
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
	col := map[string]int{}
	for i, h := range recs[0] {
		col[h] = i
	}
	var out []wcSpawn
	for _, rec := range recs[1:] {
		e, _ := strconv.ParseUint(rec[col["entry"]], 10, 32)
		x, _ := strconv.ParseFloat(rec[col["x"]], 32)
		y, _ := strconv.ParseFloat(rec[col["y"]], 32)
		z, _ := strconv.ParseFloat(rec[col["z"]], 32)
		out = append(out, wcSpawn{uint32(e), Point{float32(x), float32(y), float32(z)}})
	}
	return out
}

// wcPatrolPaths reads movement.tsv (export.sh): the waypoints of each patrolling spawn by guid.
func wcPatrolPaths(t *testing.T) map[string][]Point {
	t.Helper()
	f, err := os.Open(filepath.Join(wailingData, "movement.tsv"))
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
	out := map[string][]Point{}
	for _, rec := range recs[1:] {
		x, _ := strconv.ParseFloat(rec[2], 32)
		y, _ := strconv.ParseFloat(rec[3], 32)
		z, _ := strconv.ParseFloat(rec[4], 32)
		out[rec[0]] = append(out[rec[0]], Point{float32(x), float32(y), float32(z)})
	}
	return out
}

func wcNear(ss []wcSpawn, entry uint32, p Point, yd float64) bool {
	for _, s := range ss {
		if s.entry == entry && math.Hypot(float64(s.pos.X-p.X), float64(s.pos.Y-p.Y)) <= yd && math.Abs(float64(s.pos.Z-p.Z)) <= 6 {
			return true
		}
	}
	return false
}

// The plan is what the export and the instance script say: the five bosses in route order, Lady Anacondra's four spawn spots (spawn pool
// 10004) as one-member groups, every group member a spawn row of its entry, the one hop of the route a direct step with its exemption.
func TestWailingCavernsPlanMatchesTheExport(t *testing.T) {
	p := loadPlan(t, wailingPlan)
	if p.Map != 43 || p.Name != "wailing_caverns" || p.EntranceTrigger != 228 || p.Portal == nil || p.Portal.Map != 1 {
		t.Fatalf("%+v", p)
	}
	if lo, hi := p.Band(); lo != 18 || hi != 23 || p.Status != "mapped (not live-checked)" {
		t.Fatalf("band %d-%d status %q", lo, hi, p.Status)
	}
	ss := wcSpawns(t)
	paths := wcPatrolPaths(t)
	var bosses []uint32
	ana := 0
	hops := 0
	for i, l := range p.Legs {
		if len(l.Steps) != 0 || len(l.AfterBoss) != 0 {
			t.Errorf("legs[%d] (%s): the first-run plan has no steps (the event is in the extra file)", i, l.Name)
		}
		if l.Boss != nil {
			bosses = append(bosses, l.Boss.Entry)
			if !wcNear(ss, l.Boss.Entry, l.Boss.Position, 1) {
				t.Errorf("legs[%d] (%s): boss %d is not at a spawn row of the export: %v", i, l.Name, l.Boss.Entry, l.Boss.Position)
			}
		}
		for gi, g := range l.PullGroups {
			if len(g.Entries) != len(g.Positions) {
				t.Errorf("legs[%d] pull_groups[%d]: %d entries, %d positions", i, gi, len(g.Entries), len(g.Positions))
				continue
			}
			for k, e := range g.Entries {
				// A position may be snapped to the navmesh floor (up to 6 yd) from the spawn row.
				if !wcNear(ss, e, g.Positions[k], 6) {
					t.Errorf("legs[%d] (%s) pull_groups[%d]: no spawn row of entry %d near %v", i, l.Name, gi, e, g.Positions[k])
				}
				switch e {
				case 2914, 3835, 3653, 3678, 3679, 5912, 3669, 3670, 3673, 3674, 5775:
					t.Errorf("legs[%d] (%s) pull_groups[%d]: entry %d is a critter, a neutral, a friendly, the rare or a boss", i, l.Name, gi, e)
				}
			}
			for _, e := range g.Entries {
				if e == 3671 {
					ana++
					if len(g.Entries) < 2 {
						t.Errorf("legs[%d] pull_groups[%d]: Lady Anacondra alone in a group: she stands with the pack at her spot", i, gi)
					}
				}
			}
			if g.Patrol != nil {
				if g.Patrol.PeriodEstimate {
					t.Errorf("legs[%d] pull_groups[%d]: the patrol periods were measured on the live walk", i, gi)
				}
				// Source is "creature_movement guid N (entry E)": the path is that guid's waypoints (thinned to 40 points at most).
				var guid string
				var entry int
				if n, _ := fmt.Sscanf(g.Patrol.Source, "creature_movement guid %s (entry %d)", &guid, &entry); n != 2 {
					t.Errorf("legs[%d] pull_groups[%d]: patrol source %q", i, gi, g.Patrol.Source)
					continue
				}
				ref := paths[guid]
				if len(ref) == 0 || len(g.Patrol.Path) < 2 || len(g.Patrol.Path) > len(ref) {
					t.Errorf("legs[%d] pull_groups[%d]: patrol of guid %s has %d points, movement.tsv has %d", i, gi, guid, len(g.Patrol.Path), len(ref))
				}
				for _, q := range g.Patrol.Path {
					ok := false
					for _, w := range ref {
						ok = ok || (math.Abs(float64(w.X-q.X)) < 0.06 && math.Abs(float64(w.Y-q.Y)) < 0.06 && math.Abs(float64(w.Z-q.Z)) < 0.06)
					}
					if !ok {
						t.Errorf("legs[%d] pull_groups[%d]: patrol point %v is not a waypoint of guid %s", i, gi, q, guid)
					}
				}
			}
		}
		for _, w := range l.Via {
			if !w.Direct {
				continue
			}
			hops++
			if !w.GMCross {
				t.Errorf("legs[%d] (%s): the direct hop %v is the gm_cross over the gap", i, l.Name, w)
			}
		}
	}
	// The four Fanglords open the Disciple's second menu (instance_wailing_caverns.cpp): Cobrahn, Pythas and Serpentis are bosses of the plan.
	want := []uint32{3669, 3670, 3674, 3673, 5775}
	if len(bosses) != len(want) {
		t.Fatalf("bosses %v, want %v", bosses, want)
	}
	for i := range want {
		if bosses[i] != want[i] {
			t.Fatalf("bosses %v, want %v", bosses, want)
		}
	}
	if ana != 4 {
		t.Errorf("%d groups with Lady Anacondra as a member, want one per spawn spot (four)", ana)
	}
	spots := 0
	for _, s := range ss {
		if s.entry == 3671 {
			spots++
		}
	}
	if spots != 4 {
		t.Errorf("the export has %d Lady Anacondra spawn rows, the plan has four spots", spots)
	}
	if hops != 1 {
		t.Errorf("%d direct hops, want the one onto the ramp", hops)
	}
	for _, e := range []uint32{3840, 3674} {
		found := false
		for _, f := range p.Fleers {
			found = found || f == e
		}
		if !found {
			t.Errorf("entry %d runs at 15 percent hp (EventAI) and belongs in fleers", e)
		}
	}
}

// The live walk of 2026-10-15 found two packs within aggro of the road that the first version left out. No hostile spawn of the export that
// no pull group claims (within its radius, by entry) may stand within 15 yd (3-D) of a walk point (a mob that stands that near aggroes on its own) of the plan: the to, via and pull positions
// and the boss. The boss's own adds (first) and a patrol's entries are claimed by those lists.
func TestWailingCavernsNoUnclaimedMobNearAWalkPoint(t *testing.T) {
	p := loadPlan(t, wailingPlan)
	ss := wcSpawns(t)
	skip := map[uint32]bool{2914: true, 3835: true, 3653: true, 3678: true, 3679: true, 5912: true, 61218: true}
	for _, l := range p.Legs {
		for _, e := range l.Skip {
			skip[e] = true
		}
	}
	d3 := func(a, b Point) float64 {
		return math.Sqrt(float64((a.X-b.X)*(a.X-b.X) + (a.Y-b.Y)*(a.Y-b.Y) + (a.Z-b.Z)*(a.Z-b.Z)))
	}
	var points []Point
	for _, l := range p.Legs {
		for _, w := range l.Via {
			points = append(points, w.Point)
		}
		points = append(points, l.To)
		for _, g := range l.PullGroups {
			points = append(points, g.Positions...)
		}
		if l.Boss != nil {
			points = append(points, l.Boss.Position)
		}
	}
	claimed := func(s wcSpawn) bool {
		for _, l := range p.Legs {
			if l.Boss != nil && (l.Boss.Entry == s.entry || func() bool {
				for _, f := range l.Boss.First {
					if f == s.entry && d3(l.Boss.Position, s.pos) <= 25 {
						return true
					}
				}
				return false
			}()) {
				return true
			}
			for _, g := range l.PullGroups {
				r := float64(g.Radius)
				if r == 0 {
					r = 25
				}
				ents := append([]uint32{}, g.Entries...)
				if g.Patrol != nil {
					ents = append(ents, g.Patrol.Entries...)
					for _, w := range g.Patrol.Path { // a patrol's spawn row is a waypoint of its path
						for _, e := range g.Patrol.Entries {
							if e == s.entry && d3(w, s.pos) <= 8 {
								return true
							}
						}
					}
				}
				for _, e := range ents {
					if e != s.entry {
						continue
					}
					for _, q := range g.Positions {
						if d3(q, s.pos) <= r {
							return true
						}
					}
				}
			}
		}
		return false
	}
	for _, s := range ss {
		if skip[s.entry] || s.entry == 3678 || claimed(s) {
			continue
		}
		for _, q := range points {
			if d := d3(q, s.pos); d <= 15 {
				t.Errorf("a spawn of entry %d at %.1f %.1f %.1f is in no pull group and %.0f yd from the walk point %v", s.entry, s.pos.X, s.pos.Y, s.pos.Z, d, q)
				break
			}
		}
	}
}

// The extra plan is a record: the escort stops, the summoned waves (pull groups at the places the script summons them) and Mutanus. It is a
// draft like the plan, so it holds no steps: the waits the driver cannot do are in its notes.
func TestWailingCavernsExtraPlanLoads(t *testing.T) {
	p := loadPlan(t, wailingExtra)
	if p.Name != "wailing_caverns-extra" || p.Map != 43 || len(p.Legs) != 3 || p.Status[:5] != "draft" {
		t.Fatalf("%+v", p)
	}
	last := p.Legs[len(p.Legs)-1]
	if last.Boss == nil || last.Boss.Entry != 3654 || len(last.PullGroups) != 2 {
		t.Fatalf("the last leg ends with Mutanus after the Moccasins and the ectoplasm: %+v", last)
	}
	for i, l := range p.Legs {
		if len(l.Steps) != 0 || len(l.AfterBoss) != 0 {
			t.Errorf("legs[%d]: a draft has no steps", i)
		}
	}
}

// The route is one complete navmesh walk from the entrance to Verdan: every plain waypoint is reached by a complete path from the one before,
// the hop is the only break, and the whole walk to every member position is about 6480 yd (a change of route moves this number: update it with the plan's notes).
func TestWailingCavernsOnNavmesh(t *testing.T) {
	dir := mapMmaps(43)
	if dir == "" {
		t.Skip("no map 43 navmesh files: set TURTLE_AGENT_MMAPS to a folder holding 043.mmap and the 043*.mmtile tiles")
	}
	pl := navmesh.NewPlanner(dir)
	defer pl.Close()
	for _, f := range []string{wailingPlan, wailingExtra} {
		p := loadPlan(t, f)
		from := [3]float32{p.Entrance.X, p.Entrance.Y, p.Entrance.Z}
		total := 0.0
		hops := 0
		for i, l := range p.Legs {
			pts, direct := waypointsDirect(l)
			for j, w := range pts {
				to := [3]float32{w.X, w.Y, w.Z}
				if direct[j] {
					hops++
					from = to
					continue
				}
				path, err := pl.Plan(p.Map, from, to)
				if errors.Is(err, navmesh.ErrNoEngine) {
					t.Skip("built without cgo: no navmesh engine")
				}
				if err != nil {
					t.Fatalf("%s legs[%d] waypoint %d: %v", p.Name, i, j, err)
				}
				if path.Status != navmesh.Complete {
					t.Errorf("%s legs[%d] (%s) waypoint %d %v from %v: %s %s", p.Name, i, l.Name, j, w, from, path.Status, path.Reason)
					continue
				}
				end := path.Points[len(path.Points)-1]
				if d := math.Hypot(float64(end[0]-w.X), float64(end[1]-w.Y)); d > 2.5 || math.Abs(float64(end[2]-w.Z)) > 4 {
					t.Errorf("%s legs[%d] waypoint %d: the path ends %.1f yd (z %.1f) from %v", p.Name, i, j, d, end[2]-w.Z, w)
				}
				total += float64(path.Length)
				from = to
			}
		}
		t.Logf("%s: %.0f yd of path, %d direct hops", p.Name, total, hops)
		if p.Name == "wailing_caverns" && (total < 6200 || total > 6800 || hops != 1) {
			t.Errorf("%s: %.0f yd and %d hops, expected about 6480 yd and one hop", p.Name, total, hops)
		}
	}
}
