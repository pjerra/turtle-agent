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

// distAt says whether point a stands within yd of b (3-D).
func distAt(a, b Point, yd float64) bool {
	return math.Sqrt((float64(a.X-b.X)*float64(a.X-b.X))+(float64(a.Y-b.Y)*float64(a.Y-b.Y))+(float64(a.Z-b.Z)*float64(a.Z-b.Z))) <= yd
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

// The extra plan is the Naralex event as a segment of its own: 12 legs from the return_gossip leg to mutanus, a draft. It is loaded here (and
// walked on the navmesh by TestWailingCavernsOnNavmesh) so that it cannot rot; its return from Verdan is the live-probed hop pair (no
// gm_cross anywhere: TestNaralexReturnHop pins the pair), and its status says so.
func TestWailingCavernsExtraPlanLoads(t *testing.T) {
	p := loadPlan(t, wailingExtra)
	if p.Name != "wailing_caverns-extra" || p.Map != 43 || len(p.Legs) != 12 || !strings.HasPrefix(p.Status, "draft") {
		t.Fatalf("%+v", p)
	}
	if !strings.Contains(p.Status, "hop pair") || strings.Contains(p.Status, "GM commander") {
		t.Errorf("status %q does not say that the return is the live-probed hop pair (no GM)", p.Status)
	}
	if len(p.Notes) == 0 || !strings.HasPrefix(p.Notes[0], "Naralex event: the return from Verdan is a live-probed hop pair, no GM") {
		t.Errorf("the first plan note must be the event banner: %q", p.Notes)
	}
	last := p.Legs[len(p.Legs)-1]
	if last.Name != "mutanus" || last.Boss == nil || last.Boss.Entry != 3654 {
		t.Fatalf("the last leg ends with Mutanus: %+v", last)
	}
	for i, l := range p.Legs {
		for _, w := range l.Via {
			if w.GMCross {
				t.Errorf("legs[%d] (%s): a gm_cross at %v: the return is the hop pair, the event needs no GM", i, l.Name, w.Point)
			}
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

// The mapped route (the base plan) needs no GM from any start leg (a gm_cross anywhere in the base plan would make the group driver refuse
// the dungeon from every leg and a remote run with a non-GM commander refuse every leg up to it); nor does the extra plan.
func TestWailingCavernsBasePlanNeedsNoGM(t *testing.T) {
	p := loadPlan(t, wailingPlan)
	if len(p.Legs) != 15 || p.Legs[len(p.Legs)-1].Name != "verdan" {
		t.Fatalf("%d legs, want the 15 legs of the walk to Verdan", len(p.Legs))
	}
	for from := 0; from <= len(p.Legs); from++ {
		if p.HasGMCross(from) {
			t.Errorf("HasGMCross(%d) is true: the base plan must not need a GM from any start leg", from)
		}
	}
	// The extra plan needs none either since the return became the hop pair (it had the one gm_cross of the return until 2026-10-22).
	e := loadPlan(t, wailingExtra)
	for from := 0; from <= len(e.Legs); from++ {
		if e.HasGMCross(from) {
			t.Errorf("extra plan: HasGMCross(%d) is true: the return is the hop pair, the event needs no GM", from)
		}
	}
}

// Every wait of the wave legs ends on a state that needs no sighting. A leg runs its pull groups before its steps (stepper.go), so a wait
// for "gone" (which passes only after it has itself seen a living unit: remote_trigger.go unitGone, driver.go UnitHolds) never saw the summons
// alive when the pull has already killed them (TEMPSUMMON_DEAD_DESPAWN: the corpses vanish at once) and times out the run. "dead" also counts
// the kills the run saw. No wait of the event may be "gone".
func TestNaralexWaveWaitsNeedNoSighting(t *testing.T) {
	p := loadPlan(t, wailingExtra)
	waves := 0
	for _, l := range p.Legs {
		var pulled []uint32
		for _, g := range l.PullGroups {
			pulled = append(pulled, g.Entries...)
		}
		for _, st := range l.Steps {
			if st.WaitFor == nil || st.WaitFor.Unit == nil {
				continue
			}
			u := st.WaitFor.Unit
			if u.State == UnitGone {
				t.Errorf("%s: a wait for %v gone: it needs a sighting the pull may have made impossible", l.Name, u.Entries)
			}
			for _, e := range u.Entries {
				if slices.Contains(pulled, e) && u.State != UnitDead {
					t.Errorf("%s: the wait for the pulled entry %d is %q, want \"dead\" (the pull runs before the steps)", l.Name, e, u.State)
				}
			}
			if u.State == UnitDead {
				waves++
			}
		}
	}
	if waves != 4 {
		t.Errorf("%d dead waits, want 4 (ravager_pack, viper_pack, ritual_moccus, ritual_ecto)", waves)
	}
}

// The escort fails when every member of the group is more than 100 yd from the Disciple while he is out of combat (core
// ScriptedEscortAI.cpp:226-234 and 303-312), and bots run ahead of a walking NPC. Each follow leg is a stop of the party at a point of
// the escort road with a wait for the Disciple seen within the stop's radius; the straight line from one stop to the next (the stand is
// the first stop) and from a stop to every via point after it stays within 80 yd, so the party is never more than 80 + 9 yd ahead of him.
func TestNaralexFollowStretches(t *testing.T) {
	p := loadPlan(t, wailingExtra)
	const maxStretch = 80.0
	stop := p.Legs[0].To
	follows := 0
	longest := 0.0
	for _, l := range p.Legs {
		if !strings.HasPrefix(l.Name, "follow_") {
			stop = l.To
			continue
		}
		follows++
		for _, w := range l.Via {
			if d := dist3(w.Point, stop); d > maxStretch {
				t.Errorf("%s: via %v is %.0f yd from the stop before it %v (max %.0f)", l.Name, w.Point, d, stop, maxStretch)
			}
		}
		d := dist3(l.To, stop)
		longest = math.Max(longest, d)
		if d > maxStretch {
			t.Errorf("%s: %.0f yd in a straight line from the stop before it %v to %v (max %.0f)", l.Name, d, stop, l.To, maxStretch)
		}
		var w *UnitWait
		for _, st := range l.Steps {
			if st.WaitFor != nil && st.WaitFor.Unit != nil && slices.Equal(st.WaitFor.Unit.Entries, []uint32{3678}) {
				w = st.WaitFor.Unit
			}
		}
		if w == nil || w.State != UnitSeen || w.Near == nil || dist3(*w.Near, l.To) > 0.1 || w.Radius == 0 || w.Radius > 9 {
			t.Errorf("%s: no wait for the Disciple seen within 9 yd of its stop: %+v", l.Name, w)
		}
		stop = l.To
	}
	if follows != 6 {
		t.Errorf("%d follow legs, want 6", follows)
	}
	t.Logf("longest stretch %.1f yd", longest)
	// The rule is written down in the plan notes (a reader of the plan must find why the roads are split).
	notes := strings.Join(p.Notes, "\n")
	for _, want := range []string{"ScriptedEscortAI.cpp lines 226-234 and 303-312", "100 yd", "80 yd"} {
		if !strings.Contains(notes, want) {
			t.Errorf("the plan notes lack the escort rule (%q)", want)
		}
	}
}

// escortWalkMax is the longest straight-line distance a point of a follow leg's walk may be from the stop before it: the core fails the
// escort at 100 yd from the Disciple, who stands within 9 yd of that stop.
const escortWalkMax = 90.0

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
		if f == wailingExtra {
			// The event is the segment after the verdan leg: its first leg walks back from Verdan's room to the ramp lip (the upper part of the mesh).
			vp := loadPlan(t, wailingPlan)
			end := vp.Legs[len(vp.Legs)-1].To
			from = [3]float32{end.X, end.Y, end.Z}
		}
		total := 0.0
		hops := 0
		stop := p.Legs[0].To // the previous stop of the escort: the stand, then each leg's own stop
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
				// The escort rule (ScriptedEscortAI.cpp:226-234, 303-312): the party runs ahead of the Disciple, who waits for it
				// within the follow stop's radius (9 yd at most), so no point of the walk from one stop to the next may be 90 yd or
				// more from the stop it leaves (100 yd is the core's limit).
				if strings.HasPrefix(l.Name, "follow_") {
					for _, q := range path.Points {
						if d := math.Hypot(float64(q[0]-stop.X), float64(q[1]-stop.Y)); d > escortWalkMax {
							t.Errorf("%s legs[%d] (%s): the walk to waypoint %d passes %v, %.0f yd from the previous stop %v (more than %.0f)", p.Name, i, l.Name, j, q, d, stop, escortWalkMax)
						}
					}
				}
				total += float64(path.Length)
				from = to
			}
			stop = l.To
		}
		t.Logf("%s: %.0f yd of path, %d direct hops", p.Name, total, hops)
		if p.Name == "wailing_caverns" && (total < 8160 || total > 8290 || hops != 1) {
			t.Errorf("%s: %.0f yd and %d hops, expected about 8189 yd and one hop", p.Name, total, hops)
		}
		// The Naralex event, walked from the end of the verdan leg: the walk to the ramp lip, the hop down to the foot (the ramp
		// cannot be walked back, live 2026-10-18; the hop worked 3 of 3 on 2026-10-22), the long walk to the Disciple's stand,
		// then the escort road to the ritual stop and the chamber (script_waypoint 3678, points 0-31): 2987 yd of mesh path, measured.
		if p.Name == "wailing_caverns-extra" && (total < 2900 || total > 3070 || hops != 1) {
			t.Errorf("%s: %.0f yd and %d hops, expected about 2987 yd and the one hop pair", p.Name, total, hops)
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

// naralexLeg finds the event leg (of the extra plan) by name and fails when the plan does not have it.
func naralexLeg(t *testing.T, p *Plan, name string) *Leg {
	t.Helper()
	for i, l := range p.Legs {
		if l.Name == name {
			return &p.Legs[i]
		}
	}
	t.Fatalf("no event leg %q", name)
	return nil
}

// The Naralex event's steps (the legs of wailing_caverns-extra.json): the return by gm_cross and the gossip start, the escort's
// follow waits at the Disciple's stops (script_waypoint 3678), the wave pulls at the summon points (wailing_caverns.cpp
// Position[10][3]; no spawn rows: the testdata export has none of these entries), and Mutanus's fight held by the event's
// own instance data. The DB facts were read on 2026-10-20 through export.sh 43 and read-only SELECTs (menu 202 option 0,
// 32 script_waypoint rows, no ai_boss_tactic rows for 3654, Mutanus EventAI 1720974-6 set data 5 to 1/3/0).
func TestNaralexEventSteps(t *testing.T) {
	p := loadPlan(t, wailingExtra)
	if len(p.Legs) != 12 || p.Legs[0].Name != "return_gossip" || p.Legs[11].Name != "mutanus" {
		t.Fatalf("%d legs, want the 12 event legs from return_gossip to mutanus", len(p.Legs))
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
	// The way back from Verdan is the hop pair (TestNaralexReturnHop pins it); no GM move is left.
	if len(l.Via) != 2 {
		t.Errorf("return_gossip: via %+v, want the hop pair (the exact lip, the direct foot)", l.Via)
	}
	// The target is the gossip's stand, not the Disciple's spawn point (the lead bot would walk onto the NPC).
	gossipStand := Point{X: -134, Y: 128.5, Z: -78.1}
	if distAt(l.To, gossipStand, 0.1) == false || distAt(l.To, stand, 2) {
		t.Errorf("return_gossip: to %v, want the gossip's stand %v, not the Disciple's spawn point %v", l.To, gossipStand, stand)
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

	// The follow legs wait for the Disciple at the script waypoints 5, 7, 15, 22, 25, 30 (the long roads are split, see
	// TestNaralexFollowStretches); the wave legs pull the summons at the summon points and wait for them dead.
	follows := []struct {
		leg    string
		to     Point
		radius float32
	}{
		{"follow_ravager_road", Point{X: -108.6, Y: 195.4, Z: -80.6}, 9},
		{"follow_ravager", Point{X: -102.4, Y: 232.8, Z: -91.5}, 9},
		{"follow_viper", Point{X: -54, Y: 276.2, Z: -92.8}, 8},
		{"follow_ritual_a", Point{X: 10.3, Y: 279.2, Z: -85.8}, 8},
		{"follow_ritual_b", Point{X: 43.3, Y: 233, Z: -87.6}, 8},
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
	for _, l := range p.Legs {
		if len(l.Skip) == 0 || !slices.Contains(l.Skip, 3678) || !slices.Contains(l.Skip, 3679) {
			t.Errorf("legs (%s): skip %v, want the Disciple (3678) and Naralex (3679) skipped", l.Name, l.Skip)
		}
	}

	// The wave legs: the pulls at the summon points, then the waits that end them.
	waves := []struct {
		leg    string
		entry  uint32
		radius float32
		deadR  float32
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
		// The wave's end is the state "dead": the pull has run before the steps (stepper.go), so a "gone" wait would never
		// have seen the wave alive (TestNaralexWaveWaitsNeedNoSighting).
		var dead *WaitFor
		for _, st := range l.Steps {
			if st.WaitFor != nil && st.WaitFor.Unit != nil && st.WaitFor.Unit.State == UnitDead {
				dead = st.WaitFor
			}
		}
		if dead == nil {
			t.Fatalf("%s: no wait for the wave to be dead", wv.leg)
		}
		if !slices.Contains(dead.Unit.Entries, wv.entry) || dead.Unit.Radius != wv.deadR {
			t.Errorf("%s: dead wait %+v, want entry %d within %.0f yd", wv.leg, dead.Unit, wv.entry, wv.deadR)
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
	p := loadPlan(t, wailingExtra)
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
		"follow_ravager_road": {[]string{"2"}, "5"},
		"follow_ravager":      {nil, "7"},
		"follow_viper":        {[]string{"11", "13"}, "15"},
		"follow_ritual_a":     {[]string{"18", "20"}, "22"},
		"follow_ritual_b":     {[]string{"24"}, "25"},
		"follow_ritual":       {[]string{"26", "28"}, "30"},
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
	if got := at(l.To); got != "" {
		t.Errorf("return_gossip: to %v is script waypoint %q: the target is the gossip's stand, not the Disciple's spawn point (waypoint 0)", l.To, got)
	}
	if len(l.Via) != 2 {
		t.Fatalf("return_gossip: via %+v, want the hop pair", l.Via)
	}
	for _, w := range l.Via {
		if got := at(w.Point); got != "" {
			t.Errorf("return_gossip: the hop point %v is script waypoint %s: the lip and the foot are not escort stops", w.Point, got)
		}
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

// The return from Verdan is the live-probed hop pair, not a GM move (the probe of 2026-10-22, r9-19z26 slot 1: from the ramp lip one direct
// move to the foot landed the lead on the foot, 0.0 yd, 3 of 3): the first via is the ramp lip as an `exact` plain waypoint, the second the
// foot as a direct step, and the pair is the Serpentis hop of the base plan taken the other way, point for point. The plan export writes the
// pair as an exact move and a move_hop_exact (planexport.TestNaralexReturnHopRows).
func TestNaralexReturnHop(t *testing.T) {
	base := loadPlan(t, wailingPlan)
	extra := loadPlan(t, wailingExtra)
	l := naralexLeg(t, extra, "return_gossip")
	var serp Leg
	for _, bl := range base.Legs {
		if bl.Name == "serpentis" {
			serp = bl
		}
	}
	if len(serp.Via) != 2 || len(l.Via) != 2 {
		t.Fatalf("serpentis via %d, return_gossip via %d, want two each (the hop pair)", len(serp.Via), len(l.Via))
	}
	foot, lip := serp.Via[0].Point, serp.Via[1].Point
	if d := dist3(foot, Point{X: -293.3, Y: 0.5, Z: -58}); d > 0.1 {
		t.Fatalf("the base plan's foot %v moved (%.2f yd): the probe's foot is -293.3 0.5 -58", foot, d)
	}
	if d := dist3(lip, Point{X: -287.5, Y: 5, Z: -63.6}); d > 0.1 {
		t.Fatalf("the base plan's landing %v moved (%.2f yd): the probe's lip is -287.5 5 -63.6", lip, d)
	}
	up, down := l.Via[0], l.Via[1]
	if d := dist3(up.Point, lip); d > 0.1 {
		t.Errorf("return_gossip via[0] %v is %.2f yd from the ramp lip %v (the hop's launch point, 0.1 yd)", up.Point, d, lip)
	}
	if !up.Exact || up.Direct || up.GMCross {
		t.Errorf("return_gossip via[0] %+v: want the lip as a plain exact waypoint (the lead bot held on it before the hop)", up)
	}
	if d := dist3(down.Point, foot); d > 0.1 {
		t.Errorf("return_gossip via[1] %v is %.2f yd from the foot %v (the hop's landing, 0.1 yd)", down.Point, d, foot)
	}
	if !down.Direct || down.Exact || down.GMCross {
		t.Errorf("return_gossip via[1] %+v: want the foot as a plain direct step", down)
	}
	if up.CheckSkip == "" || down.CheckSkip == "" {
		t.Errorf("return_gossip: the lip (check_skip %q) and the foot (check_skip %q) both need a check_skip: the mesh cannot judge the walk to the lip from the entrance nor the jump", up.CheckSkip, down.CheckSkip)
	}
	// The direct step is within the limits of a direct step (the loader checks it against the previous via; this pins the numbers).
	if h := math.Hypot(float64(down.X-up.X), float64(down.Y-up.Y)); h > DirectMaxDistance || math.Abs(float64(down.Z-up.Z)) > DirectMaxVertical {
		t.Errorf("the hop is %.1f yd across and %.1f yd down, over the limits of a direct step", h, down.Z-up.Z)
	}
	// The navmesh facts the plan rests on: the lip is walkable from the end of Verdan's room (the upper part), and the walk from the foot
	// to the Disciple's stand is complete (the lower part); the lip is not walkable to the foot (the whole reason for the hop).
	dir := mapMmaps(43)
	if dir == "" {
		t.Skip("no map 43 navmesh files: set TURTLE_AGENT_MMAPS to a folder holding 043.mmap and the 043*.mmtile tiles")
	}
	pl := navmesh.NewPlanner(dir)
	defer pl.Close()
	pt := func(p Point) [3]float32 { return [3]float32{p.X, p.Y, p.Z} }
	verdan := base.Legs[len(base.Legs)-1].To
	plan := func(from, to Point) navmesh.Path {
		path, err := pl.Plan(43, pt(from), pt(to))
		if errors.Is(err, navmesh.ErrNoEngine) {
			t.Skip("built without cgo: no navmesh engine")
		}
		if err != nil {
			t.Fatal(err)
		}
		return path
	}
	if p := plan(verdan, lip); p.Status != navmesh.Complete {
		t.Errorf("Verdan's room %v to the lip %v: %s %s, want a complete walk", verdan, lip, p.Status, p.Reason)
	}
	if p := plan(foot, l.To); p.Status != navmesh.Complete || p.Length < 1600 || p.Length > 1800 {
		t.Errorf("the foot %v to the gossip stand %v: %s, %.0f yd, want a complete walk of about 1706 yd", foot, l.To, p.Status, float64(p.Length))
	}
	if p := plan(lip, foot); p.Status == navmesh.Complete {
		t.Errorf("the lip %v to the foot %v is walkable on the mesh: the hop is no longer needed (drop it, and the check_skips)", lip, foot)
	}
	if p := plan(extra.Entrance, lip); p.Status == navmesh.Complete {
		t.Errorf("the entrance %v to the lip is walkable: the event would need no hop (or the mesh changed)", extra.Entrance)
	}
}
