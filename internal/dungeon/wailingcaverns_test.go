package dungeon

import (
	"encoding/csv"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
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

// distAt says whether point a stands within yd of b (3-D).
func distAt(a, b Point, yd float64) bool {
	return math.Sqrt((float64(a.X-b.X)*float64(a.X-b.X))+(float64(a.Y-b.Y)*float64(a.Y-b.Y))+(float64(a.Z-b.Z)*float64(a.Z-b.Z))) <= yd
}

// The plan is what the export and the instance script say: the five bosses in route order, Lady Anacondra's four spawn spots (spawn pool
// 10004) as one-member groups, every group member a spawn row of its entry, the one hop of the route a plain direct step with a check_skip (no GM).
func TestWailingCavernsPlanMatchesTheExport(t *testing.T) {
	p := loadPlan(t, wailingPlan)
	if p.Map != 43 || p.Name != "wailing_caverns" || p.EntranceTrigger != 228 || p.Portal == nil || p.Portal.Map != 1 {
		t.Fatalf("%+v", p)
	}
	if lo, hi := p.Band(); lo != 18 || hi != 23 || !strings.HasPrefix(p.Status, "mapped (not live-checked") {
		t.Fatalf("band %d-%d status %q", lo, hi, p.Status)
	}
	ss := wcSpawns(t)
	paths := wcPatrolPaths(t)
	var bosses []uint32
	ana := 0
	hops := 0
	for i, l := range p.Legs {
		if i >= 12 {
			// The Naralex event legs (12-20): steps, wave groups and the Mutanus hold; TestNaralexEventSteps holds them.
			continue
		}
		if len(l.Steps) != 0 || len(l.AfterBoss) != 0 {
			t.Errorf("legs[%d] (%s): the first-run plan has no steps (the event is the legs after verdan)", i, l.Name)
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
			// The way back to the Disciple (leg 12, return_gossip) is a gm_cross by design: the ramp cannot be climbed and the
			// waterfall hole behind Verdan is a fall, so a GM moves the party; the run leader's export cuts before that leg.
			if w.GMCross {
				if l.Name != "return_gossip" || !distAt(w.Point, Point{X: -131, Y: 127, Z: -78.6}, 0.1) {
					t.Errorf("legs[%d] (%s): a gm_cross hop outside the return_gossip leg: %v", i, l.Name, w)
				}
				continue
			}
			hops++
			// The hop down onto the ramp is a plain direct step (no GM): the check_skip is what keeps the plan check off it
			// (the gm_cross before it was dropped 2026-10-18, when the step was walked live: tests/wc-hop-probe-2).
			if w.CheckSkip == "" {
				t.Errorf("legs[%d] (%s): the direct hop %v has no check_skip", i, l.Name, w)
			}
		}
	}
	// The hop of Lord Serpentis lands on the ramp 2.9 yd in from the edge point: the probe of 2026-10-18 (tests/wc-hop-probe-2) put
	// the party on -287.5 5.0 -63.6 and showed that a stand on the old edge -289.1 3.2 -64.1 falls to the chasm floor (z -105.5).
	serp := -1
	for i, l := range p.Legs {
		if l.Name == "serpentis" {
			serp = i
		}
	}
	if serp < 0 {
		t.Fatalf("%s: no serpentis leg", p.Name)
	}
	landing := Point{X: -287.5, Y: 5.0, Z: -63.6}
	edge := Point{X: -289.1, Y: 3.2, Z: -64.1}
	lands := 0
	for j, w := range p.Legs[serp].Via {
		if w.Direct {
			lands++
			if d := dist3(w.Point, landing); d > 0.1 {
				t.Errorf("legs[%d] (%s) via[%d]: the hop lands at %v, %.2f yd from the probe's landing %v (0.1 yd)", serp, p.Legs[serp].Name, j, w.Point, d, landing)
			}
		}
		if d := dist3(w.Point, edge); d < 2 {
			t.Errorf("legs[%d] (%s) via[%d] %v is %.2f yd from the old edge point %v, which the probe showed is not a stand (a .go xyz there fell to the chasm floor)", serp, p.Legs[serp].Name, j, w.Point, d, edge)
		}
	}
	if lands != 1 {
		t.Errorf("legs[%d] (serpentis): %d direct landings, want 1", serp, lands)
	}
	// The point before the direct step is the foot, and it is an `exact` waypoint: the driver holds the lead bot on it (1.5 yd with a
	// stationary proof) instead of letting the walk's 5 yd arrival launch the step from short (PR 235; the probe of 2026-10-18 stood at 0.00 yd).
	via := p.Legs[serp].Via
	if len(via) < 2 {
		t.Fatalf("legs[%d] (serpentis): %d via points, want the foot and the direct landing", serp, len(via))
	}
	foot := Point{X: -293.3, Y: 0.5, Z: -58}
	if d := dist3(via[0].Point, foot); d > 0.1 {
		t.Errorf("legs[%d] (serpentis) via[0] %v is %.2f yd from the foot %v (0.1 yd)", serp, via[0].Point, d, foot)
	}
	if !via[0].Exact {
		t.Errorf("legs[%d] (serpentis) via[0] %v: the foot before the direct step is not exact", serp, via[0].Point)
	}
	if !via[1].Direct || via[1].GMCross {
		t.Errorf("legs[%d] (serpentis) via[1] %v: want the plain direct landing after the exact foot", serp, via[1].Point)
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
// the hop is the only break, and the whole walk to every member position is about 6240 yd (a change of route moves this number: update it with the plan's notes).
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
		// The route split of wailing_caverns: legs 0-11 are the mapped walk to Verdan (the walk of 2026-10-15), legs 12-20 the
		// Naralex event, written offline from script_waypoint 3678 (never walked). Each has its own distance bound.
		baseTotal, eventTotal := 0.0, 0.0
		baseHops, eventHops := 0, 0
		for i, l := range p.Legs {
			pts, direct := waypointsDirect(l)
			for j, w := range pts {
				to := [3]float32{w.X, w.Y, w.Z}
				if direct[j] {
					hops++
					if i < 12 {
						baseHops++
					} else {
						eventHops++
					}
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
			if i < 12 {
				baseTotal = total
			}
		}
		t.Logf("%s: %.0f yd of path, %d direct hops", p.Name, total, hops)
		if p.Name == "wailing_caverns" {
			// Legs 0-11 to Verdan: the walk of 2026-10-15 measured about 6240 yd with every member position and one direct hop.
			if baseTotal < 6150 || baseTotal > 6350 || baseHops != 1 {
				t.Errorf("%s: %.0f yd and %d hops over legs 0-11, expected about 6240 yd and the one hop onto the ramp", p.Name, baseTotal, baseHops)
			}
			// The event legs (12-20): the way back is a gm_cross (the ramp cannot be climbed, live 2026-10-18), then the escort
			// road to the ritual stop and the chamber (script_waypoint 3678, points 0-31): 1044 yd of mesh path, measured.
			eventTotal = total - baseTotal
			if eventTotal < 950 || eventTotal > 1150 || eventHops != 1 {
				t.Errorf("%s: %.0f yd and %d hops over legs 12-20, expected about 1044 yd and the one gm_cross", p.Name, eventTotal, eventHops)
			}
		}
	}
}

// naralexScriptWaypoints reads tools/dungeongen/testdata/map43/script_waypoint.tsv (export.sh): the escort's scripted
// path, script_waypoint entry 3678, points 0-31.
func naralexScriptWaypoints(t *testing.T) []Point {
	t.Helper()
	f, err := os.Open(filepath.Join(wailingData, "script_waypoint.tsv"))
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
	if recs[0][0] != "entry" {
		t.Fatalf("script_waypoint.tsv: no header row: %v", recs[0])
	}
	var out []Point
	for _, rec := range recs[1:] {
		if rec[0] != "3678" {
			continue
		}
		x, _ := strconv.ParseFloat(rec[2], 32)
		y, _ := strconv.ParseFloat(rec[3], 32)
		z, _ := strconv.ParseFloat(rec[4], 32)
		out = append(out, Point{float32(x), float32(y), float32(z)})
	}
	return out
}

// naralexLeg finds the event leg by name and fails when the plan does not have it.
func naralexLeg(t *testing.T, p *Plan, name string) *Leg {
	t.Helper()
	for i, l := range p.Legs {
		if i < 12 {
			continue
		}
		if l.Name == name {
			return &p.Legs[i]
		}
	}
	t.Fatalf("no event leg %q", name)
	return nil
}

// The Naralex event's steps (legs 12-20, after the verdan leg): the return by gm_cross and the gossip start, the escort's
// follow waits at the Disciple's stops (script_waypoint 3678), the wave pulls at the summon points (wailing_caverns.cpp
// Position[10][3]; no spawn rows: the testdata export has none of these entries), and Mutanus's fight held by the event's
// own instance data. The DB facts were read on 2026-10-20 through export.sh 43 and read-only SELECTs (menu 202 option 0,
// 32 script_waypoint rows, no ai_boss_tactic rows for 3654, Mutanus EventAI 1720974-6 set data 5 to 1/3/0).
func TestNaralexEventSteps(t *testing.T) {
	p := loadPlan(t, wailingPlan)
	if len(p.Legs) != 21 || p.Legs[11].Name != "verdan" {
		t.Fatalf("%d legs, want 21 after the verdan leg", len(p.Legs))
	}
	wps := naralexScriptWaypoints(t)
	if len(wps) != 32 {
		t.Fatalf("script_waypoint.tsv: %d waypoints of entry 3678, want 32", len(wps))
	}
	stand := Point{X: -134.965, Y: 125.402, Z: -78.0945}
	if d := distAt(wps[0], stand, 0.6); !d {
		t.Errorf("waypoint 0 %v is not the Disciple's stand %v", wps[0], stand)
	}
	// The Moccasins, the Ectoplasm and Mutanus have no spawn rows in the export; the Vipers (5755) do (in Cobrahn's
	// room), which is why their wait is anchored at the summon area round the stop, where no spawn row of the entry stands.
	ss := wcSpawns(t)
	for _, e := range []uint32{5762, 5763, 3654} {
		for _, s := range ss {
			if s.entry == e {
				t.Fatalf("entry %d has a spawn row at %v: the wave facts say it is summoned only", e, s.pos)
			}
		}
	}
	stop15 := Point{X: -54, Y: 276.2, Z: -92.8}
	for _, s := range ss {
		if s.entry == 5755 && distAt(s.pos, stop15, 45) {
			t.Fatalf("a static Deviate Viper (5755) stands within 45 yd of the second stop: the viper wait would fire on it")
		}
	}

	// The talk: a wait for the Disciple alive at his stand, then the gossip at option 0 of menu 202.
	l := naralexLeg(t, p, "return_gossip")
	if len(l.Via) != 1 || !l.Via[0].Direct || !l.Via[0].GMCross || distAt(l.Via[0].Point, Point{X: -131, Y: 127, Z: -78.6}, 0.1) == false {
		t.Errorf("return_gossip: via %+v, want one gm_cross at -131 127 -78.6", l.Via)
	}
	if distAt(l.To, stand, 0.1) == false {
		t.Errorf("return_gossip: to %v, want the Disciple's stand", l.To)
	}
	if len(l.Steps) != 2 {
		t.Fatalf("return_gossip: %d steps, want the presence wait and the gossip", len(l.Steps))
	}
	w := l.Steps[0].WaitFor
	if w == nil || w.Unit == nil || w.Unit.State != "seen" || len(w.Unit.Entries) != 1 || w.Unit.Entries[0] != 3678 ||
		w.Unit.Radius != 8 || w.Unit.Near == nil || distAt(*w.Unit.Near, stand, 0.1) == false || w.TimeoutS != 60 {
		t.Errorf("return_gossip step 0: %+v, want the Disciple seen within 8 yd of his stand", w)
	}
	g := l.Steps[1].Gossip
	if g == nil || g.NPC != 3678 || g.Option == nil || *g.Option != 0 || g.By != "tank" || distAt(g.Near, stand, 0.1) == false ||
		g.Stand == nil || dist3(*g.Stand, stand) > 5 || g.RetryS != 45 {
		t.Errorf("return_gossip step 1: %+v, want menu 202 option 0 by the tank, stand within 5 yd, retry_s 45", g)
	}

	// The follow legs wait for the Disciple at the script waypoints 7, 15, 30; the wave legs pull the summons at
	// the summon points and wait for them gone.
	follows := []struct {
		leg    string
		to     Point
		radius float32
	}{
		{"follow_ravager", Point{X: -102.4, Y: 232.8, Z: -91.5}, 9},
		{"follow_viper", Point{X: -54, Y: 276.2, Z: -92.8}, 8},
		{"follow_ritual", Point{X: 114.6, Y: 236.9, Z: -96}, 8},
	}
	for _, f := range follows {
		l := naralexLeg(t, p, f.leg)
		if distAt(l.To, f.to, 0.1) == false {
			t.Errorf("%s: to %v, want the stop %v", f.leg, l.To, f.to)
			continue
		}
		var found *WaitFor
		for i, st := range l.Steps {
			if st.WaitFor != nil && st.WaitFor.Unit != nil && st.WaitFor.Unit.State == "seen" && len(st.WaitFor.Unit.Entries) == 1 && st.WaitFor.Unit.Entries[0] == 3678 {
				found = st.WaitFor
				if i != 0 {
					t.Errorf("%s: the Disciple's arrival wait is not step 0", f.leg)
				}
			}
		}
		if found == nil {
			t.Errorf("%s: no wait for the Disciple's arrival", f.leg)
			continue
		}
		u := found.Unit
		if u.Radius != f.radius || distAt(*u.Near, f.to, 0.1) == false || found.TimeoutS < 180 {
			t.Errorf("%s: wait %+v, want radius %.0f round the stop and a ceiling of at least 180 s", f.leg, found.Unit, f.radius)
		}
	}
	for _, l := range p.Legs[12:] {
		if len(l.Skip) == 0 || !slices.Contains(l.Skip, 3678) || !slices.Contains(l.Skip, 3679) {
			t.Errorf("legs (%s): skip %v, want the Disciple (3678) and Naralex (3679) skipped", l.Name, l.Skip)
		}
	}

	// The wave legs: the pulls at the summon points, then the waits that end them.
	waves := []struct {
		leg    string
		entry  uint32
		radius float32
		goneR  float32
		spots  []Point
		stops  []Point
		extra  []uint32
	}{
		{"ravager_pack", 3636, 25, 40, []Point{
			Point{X: -102.4, Y: 232.8, Z: -91.5}, // the party's stop: the summons run at the Disciple, who stands at it
			Point{X: -69.8, Y: 211.3, Z: -93.5},  // the southern summon point (the first spawn is 3.6 yd from it)
		}, nil, nil},
		{"viper_pack", 5755, 25, 40, []Point{
			Point{X: -52.9, Y: 269.8, Z: -92.8},
			Point{X: -58.5, Y: 279.8, Z: -92.8},
			Point{X: -49.6, Y: 278.2, Z: -92.8},
		}, nil, []uint32{3671, 3840, 3840, 3636}}, // Anacondra's spot 4 pack, whose spawn rows stand in the stop's radius
		{"ritual_moccus", 5762, 25, 40, []Point{
			Point{X: 123.8, Y: 271.9, Z: -102.4},
			Point{X: 151.9, Y: 234.3, Z: -102.5},
			Point{X: 127.6, Y: 200.8, Z: -101.8},
		}, nil, nil},
	}
	for _, wv := range waves {
		l := naralexLeg(t, p, wv.leg)
		if len(l.PullGroups) != 1 {
			t.Fatalf("%s: %d pull groups", wv.leg, len(l.PullGroups))
		}
		grp := l.PullGroups[0]
		if wv.extra != nil {
			// The stop's own static neighbours are members too (the viper stop is Lady Anacondra's spot 4).
			var got []uint32
			for _, e := range grp.Entries {
				if e == wv.entry {
					got = append(got, e)
				}
			}
			if len(got) != 3 {
				t.Errorf("%s: %d of entry %d in the group, want the three summons", wv.leg, len(got), wv.entry)
			}
			if !slices.Equal(grp.Entries, append([]uint32{wv.entry, wv.entry, wv.entry}, wv.extra...)) {
				t.Errorf("%s: entries %v, want the three summons and the static members %v", wv.leg, grp.Entries, wv.extra)
			}
		} else {
			for _, e := range grp.Entries {
				if e != wv.entry {
					t.Errorf("%s: entry %d in the wave's group, want only %d", wv.leg, e, wv.entry)
				}
			}
		}
		if len(grp.Entries) < len(wv.spots) {
			t.Errorf("%s: %d entries for %d summon points", wv.leg, len(grp.Entries), len(wv.spots))
		}
		if grp.Radius != wv.radius {
			t.Errorf("%s: radius %.0f, want %.0f (the summons are pulled at their own points)", wv.leg, grp.Radius, wv.radius)
		}
		for _, spot := range wv.spots {
			ok := false
			for _, q := range grp.Positions {
				ok = ok || distAt(q, spot, 0.15)
			}
			if !ok {
				t.Errorf("%s: no pull position at the summon point %v", wv.leg, spot)
			}
		}
		var gone *WaitFor
		for _, st := range l.Steps {
			if st.WaitFor != nil && st.WaitFor.Unit != nil && st.WaitFor.Unit.State == "gone" {
				gone = st.WaitFor
			}
		}
		if gone == nil {
			t.Fatalf("%s: no wait for the wave to be gone", wv.leg)
		}
		if !slices.Contains(gone.Unit.Entries, wv.entry) || gone.Unit.Radius != wv.goneR {
			t.Errorf("%s: gone wait %+v, want entry %d within %.0f yd", wv.leg, gone.Unit, wv.entry, wv.goneR)
		}
	}

	// The Ectoplasm leg: seven summoned, then the wait for Mutanus before the boss leg runs.
	ectoLeg := naralexLeg(t, p, "ritual_ecto")
	if len(ectoLeg.PullGroups) != 1 || len(ectoLeg.PullGroups[0].Entries) != 7 {
		t.Fatalf("ritual_ecto: %+v, want seven 5763", ectoLeg.PullGroups)
	}
	ecto := ectoLeg.PullGroups[0]
	ectoSpots := []Point{
		Point{X: 142.7, Y: 254, Z: -102.2},
		Point{X: 140.5, Y: 219.8, Z: -102.4},
		Point{X: 92.2, Y: 261.9, Z: -101.5},
		Point{X: 100.3, Y: 268.6, Z: -102.2},
		Point{X: 123.8, Y: 271.9, Z: -102.4},
		Point{X: 151.9, Y: 234.3, Z: -102.5},
		Point{X: 127.6, Y: 200.8, Z: -101.8},
	}
	for _, spot := range ectoSpots {
		ok := false
		for _, q := range ecto.Positions {
			ok = ok || distAt(q, spot, 0.15)
		}
		if !ok {
			t.Errorf("ritual_ecto: no pull position at the summon point %v", spot)
		}
	}
	var mutWait *WaitFor
	for _, st := range ectoLeg.Steps {
		if st.WaitFor != nil && st.WaitFor.Unit != nil && st.WaitFor.Unit.State == "seen" {
			mutWait = st.WaitFor
		}
	}
	if mutWait == nil || !slices.Contains(mutWait.Unit.Entries, 3654) {
		t.Fatalf("ritual_ecto: no wait for Mutanus (3654) to appear before the boss leg")
	}

	// Mutanus's leg: the boss at the summon point, no tactics (ai_boss_tactic has no rows for him on map 43), and the
	// hold that ends the event by the instance data the script sets: TYPE_DISCIPLE (4) reads DONE (3) only when Mutanus
	// is dead (EventAI 1720976 sets data 5 DONE, the instance script copies it to data 4).
	l = naralexLeg(t, p, "mutanus")
	if l.Boss == nil || l.Boss.Entry != 3654 || distAt(l.Boss.Position, Point{X: 142.7, Y: 254.0, Z: -102.2}, 0.1) == false {
		t.Fatalf("mutanus: boss %+v, want 3654 at the summon point", l.Boss)
	}
	if l.Boss.Tactics != nil {
		t.Errorf("mutanus: boss tactics %+v: ai_boss_tactic has no rows for him (2026-10-20 read-only query), so none are set", l.Boss.Tactics)
	}
	h := l.Boss.Hold
	if h == nil || h.Data != 4 || h.Radius != 50 || h.TimeoutS != 600 || distAt(h.Spot, Point{X: 114.6, Y: 236.9, Z: -96}, 0.1) == false {
		t.Errorf("mutanus: hold %+v, want instance data 4 (TYPE_DISCIPLE) done at the ritual stop, radius 50, ceiling 600", h)
	}
	if len(l.AfterBoss) != 1 || l.AfterBoss[0].WaitFor == nil || l.AfterBoss[0].WaitFor.HostilesGone == nil {
		t.Errorf("mutanus: after_boss %+v, want one hostiles_gone wait", l.AfterBoss)
	}
}

// The escort legs walk the Disciple's own path: every stop (to) and every via point of the event legs stands at a
// script_waypoint 3678 row (the DB rounds its positions, 0.15 yd), so the party waits where the escort pauses. The
// gm_cross of return_gossip is the one exception: the party is MOVED to the entrance hall, not walked to a waypoint.
func TestNaralexEscortFollowsScriptWaypoints(t *testing.T) {
	p := loadPlan(t, wailingPlan)
	wps := naralexScriptWaypoints(t)
	if len(wps) != 32 {
		t.Fatalf("%d waypoints, want 32", len(wps))
	}
	at := func(q Point) string {
		for i, w := range wps {
			if distAt(w, q, 0.15) {
				return strconv.Itoa(i)
			}
		}
		return ""
	}
	legs := map[string]struct {
		via []string // expected waypoint numbers, "" for the gm_cross
		to  string
	}{
		"follow_ravager": {[]string{"2", "5"}, "7"},
		"follow_viper":   {[]string{"11", "13"}, "15"},
		"follow_ritual":  {[]string{"26", "28"}, "30"},
	}
	for name, want := range legs {
		l := naralexLeg(t, p, name)
		if len(l.Via) != len(want.via) {
			t.Fatalf("%s: %d via points, want %d", name, len(l.Via), len(want.via))
		}
		for j, w := range l.Via {
			if got := at(w.Point); got != want.via[j] {
				t.Errorf("%s: via[%d] %v is script waypoint %q, want %q", name, j, w.Point, got, want.via[j])
			}
		}
		if got := at(l.To); got != want.to {
			t.Errorf("%s: to %v is script waypoint %q, want %q", name, l.To, got, want.to)
		}
	}
	l := naralexLeg(t, p, "return_gossip")
	if got := at(l.To); got != "0" {
		t.Errorf("return_gossip: to %v is script waypoint %q, want 0 (the Disciple's stand)", l.To, got)
	}
	if len(l.Via) != 1 || !l.Via[0].GMCross {
		t.Fatalf("return_gossip: via %+v, want the one gm_cross", l.Via)
	}
	if got := at(l.Via[0].Point); got != "" {
		t.Errorf("return_gossip: the gm_cross point %v is script waypoint %s: the GM stand is not an escort stop", l.Via[0].Point, got)
	}
	// The ritual stop is the escort's last ground: points 30 and 31 stand on it (the final walk back is a zero-length
	// step of the script), and Naralex's spawn (33973, 115.4 240.0 -94.0) is within 12 yd of it.
	stop30 := Point{X: 114.6, Y: 236.9, Z: -96}
	if d := distAt(wps[31], stop30, 0.15); !d {
		t.Errorf("waypoint 31 %v is not the ritual stop %v", wps[31], stop30)
	}
	naralex := Point{X: 115.408, Y: 240.017, Z: -94.021}
	if !distAt(naralex, stop30, 12) {
		t.Errorf("Naralex (3679 at %v) is not within 12 yd of the ritual stop", naralex)
	}
}
