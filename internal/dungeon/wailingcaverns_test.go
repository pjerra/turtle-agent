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
	guid  string
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
		out = append(out, wcSpawn{rec[col["guid"]], uint32(e), Point{float32(x), float32(y), float32(z)}})
	}
	return out
}

// wcOnPatrol: p is a waypoint of the movement of a spawn row of entry (the spot a patrolling boss is met at, instead of his
// spawn row).
func wcOnPatrol(ss []wcSpawn, paths map[string][]Point, entry uint32, p Point) bool {
	for _, s := range ss {
		if s.entry != entry {
			continue
		}
		for _, q := range paths[s.guid] {
			if math.Hypot(float64(q.X-p.X), float64(q.Y-p.Y)) <= 1 && math.Abs(float64(q.Z-p.Z)) <= 1 {
				return true
			}
		}
	}
	return false
}

// wcPatrolGapS: the longest time a patrol is farther than radius from p while it walks its waypoints in order (the core's
// waypoint movement goes on from the last to the first), at the export's walk speed (2.5 yd/s).
func wcPatrolGapS(path []Point, p Point, radius float64) float64 {
	if len(path) < 2 {
		return 0
	}
	walk := append(append([]Point{}, path...), path[0])
	gap, worst := 0.0, 0.0
	for i := 0; i+1 < len(walk); i++ {
		a, b := walk[i], walk[i+1]
		d := math.Hypot(float64(b.X-a.X), float64(b.Y-a.Y))
		n := int(math.Ceil(d / 0.5))
		if n < 1 {
			n = 1
		}
		for k := 0; k < n; k++ {
			f := float64(k) / float64(n)
			q := Point{float32(float64(a.X) + float64(b.X-a.X)*f), float32(float64(a.Y) + float64(b.Y-a.Y)*f), 0}
			if math.Hypot(float64(q.X-p.X), float64(q.Y-p.Y)) <= radius {
				if gap > worst {
					worst = gap
				}
				gap = 0
			} else {
				gap += d / float64(n) / 2.5
			}
		}
	}
	if gap > worst {
		worst = gap
	}
	return worst
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

// The plan is what the export and the instance script say: the seven bosses in route order (the five of the script and the two Turtle
// bosses of the west wing and the east spur, added 2026-10-26), Lady Anacondra's four spawn spots (spawn pool
// 10004) as one-member groups, every group member a spawn row of its entry, the one hop of the route a plain direct step with a check_skip (no GM).
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
	kresh := 0
	hops := 0
	for i, l := range p.Legs {
		if len(l.Steps) != 0 || len(l.AfterBoss) != 0 {
			t.Errorf("legs[%d] (%s): the first-run plan has no steps (the event is in the extra file)", i, l.Name)
		}
		if l.Boss != nil {
			bosses = append(bosses, l.Boss.Entry)
			// The point is a spawn row, or a waypoint of the boss's own patrol: a patroller is fought where the step's
			// wait meets him (Kresh), not where he spawns.
			if !wcNear(ss, l.Boss.Entry, l.Boss.Position, 1) && !wcOnPatrol(ss, paths, l.Boss.Entry, l.Boss.Position) {
				t.Errorf("legs[%d] (%s): boss %d is not at a spawn row or a waypoint of his own patrol: %v", i, l.Name, l.Boss.Entry, l.Boss.Position)
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
				case 2914, 3835, 3678, 3679, 5912, 3669, 3670, 3673, 3674, 5775:
					t.Errorf("legs[%d] (%s) pull_groups[%d]: entry %d is a critter, a friendly, the rare or a boss", i, l.Name, gi, e)
				case 3653:
					// Kresh is the neutral turtle of the river: a boss row of the kresh leg (a neutral mob is fought
					// only when a step makes the party attack it), never a pull member of any leg.
					kresh++
					t.Errorf("legs[%d] (%s) pull_groups[%d]: Kresh is the kresh leg's boss, not a pull member: %+v", i, l.Name, gi, g)
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
			// The hop down onto the ramp is a plain direct step (no GM): the check_skip is what keeps the plan check off it
			// (the gm_cross before it was dropped 2026-10-18, when the step was walked live: tests/wc-hop-probe-2).
			if w.GMCross {
				t.Errorf("legs[%d] (%s): the direct hop %v still asks for a GM (gm_cross)", i, l.Name, w)
			}
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
	// The four Fanglords open the Disciple's second menu (instance_wailing_caverns.cpp): Cobrahn, Pythas and Serpentis are bosses of the
	// plan; Skum and Verdan follow the script; Vangros (west wing) and Zandara (east spur) are Turtle's, added 2026-10-26.
	want := []uint32{3653, 3669, 3670, 61968, 3674, 61965, 3673, 5775}
	if len(bosses) != len(want) {
		t.Fatalf("bosses %v, want %v", bosses, want)
	}
	for i := range want {
		if bosses[i] != want[i] {
			t.Fatalf("bosses %v, want %v", bosses, want)
		}
	}
	if kresh != 0 {
		t.Errorf("%d pull groups with Kresh, want none (the kresh leg's boss)", kresh)
	}
	// Kresh: his own leg where the river road passes the middle of his beat, a boss row (the step that makes the party
	// attack a neutral), and a wait that covers his round: the run leader stands at the boss point for the row's
	// timeout_s, so wait_s must be longer than the longest he is out of the row's 30 yd radius.
	kl := -1
	for i, l := range p.Legs {
		if l.Name == "kresh" {
			kl = i
		}
	}
	if kl < 0 {
		t.Fatalf("no kresh leg")
	}
	if kl != 3 || p.Legs[kl].Boss == nil || p.Legs[kl].Boss.Entry != 3653 || len(p.Legs[kl].PullGroups) != 0 ||
		dist3(p.Legs[kl].To, Point{X: 4, Y: 254, Z: -105.5}) > 0.1 || p.Legs[kl].Boss.WaitS != 300 {
		t.Fatalf("legs[%d] (%s): want the kresh boss leg (3653, no pulls, wait_s 300) at 4 254 -105.5: %+v", kl, p.Legs[kl].Name, p.Legs[kl])
	}
	wp := Point{X: -3.64081, Y: 229.268, Z: -106.189}
	if dist3(p.Legs[kl].Boss.Position, wp) > 0.1 {
		t.Errorf("the kresh boss point %v is not his third waypoint %v", p.Legs[kl].Boss.Position, wp)
	}
	if gap := wcPatrolGapS(paths["27368"], p.Legs[kl].Boss.Position, 30); float64(p.Legs[kl].Boss.WaitS) < math.Ceil(gap)+30 {
		t.Errorf("wait_s %d does not cover the %.0f s Kresh is out of the boss row's 30 yd radius", p.Legs[kl].Boss.WaitS, gap)
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

// A boss who stands beside a pack is fought as one encounter: no pull group of his own leg may stand within 20 yd (3-D) of him (the pack
// would drag him in and his row would end as "pack cleared"), and the leg before the next boss stops far enough short that the walk cannot
// pull him. Serpentis's Druid and Venomwing are his `first`; Verdan's leg stops at least 25 yd short (live 2026-10-20, #250).
func TestWailingCavernsBossesAreEncounters(t *testing.T) {
	p := loadPlan(t, wailingPlan)
	for _, name := range []string{"serpentis", "verdan"} {
		l := zfLeg(t, p, name)
		if l.Boss == nil {
			t.Fatalf("leg %q has no boss", name)
		}
		for gi, g := range l.PullGroups {
			for k, q := range g.Positions {
				if d := dist3(q, l.Boss.Position); d < 20 {
					t.Errorf("leg %q pull_groups[%d] member %d (%v) is %.1f yd from boss %d: a pack beside a boss is his first, not a pull group",
						name, gi, k, q, d, l.Boss.Entry)
				}
			}
		}
	}
	serp := zfLeg(t, p, "serpentis")
	if b := serp.Boss; b == nil || len(b.First) != 2 || b.First[0] != 3840 || b.First[1] != 5756 {
		t.Errorf("serpentis boss.first = %+v, want [3840 5756] (the Druid and the Venomwing beside him)", serp.Boss)
	}
	verdan := zfLeg(t, p, "verdan")
	if verdan.Boss == nil {
		t.Fatalf("verdan leg has no boss")
	}
	if d := dist3(verdan.To, verdan.Boss.Position); d < 25 {
		t.Errorf("verdan.to %v is %.1f yd from Verdan %v: want at least 25 yd (the walk to it pulled him at 11.6 yd)", verdan.To, d, verdan.Boss.Position)
	}
}

// The route is one complete navmesh walk from the entrance to Verdan: every plain waypoint is reached by a complete path from the one before,
// the hop is the only break, and the whole walk to every member position is about 8189 yd (8223 before the Serpentis pack became the
// boss's first on 2026-10-29, 8195 before the kresh leg of 2026-10-28, 6240 before the vangros and zandara legs of 2026-10-26; a change
// of route moves this number: update it with the plan's notes).
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
		if p.Name == "wailing_caverns" && (total < 8160 || total > 8290 || hops != 1) {
			t.Errorf("%s: %.0f yd and %d hops, expected about 8189 yd and one hop", p.Name, total, hops)
		}
	}
}
