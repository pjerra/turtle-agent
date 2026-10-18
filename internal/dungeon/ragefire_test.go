package dungeon

import (
	"bytes"
	"encoding/csv"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"github.com/pjerra/turtle-agent/internal/navmesh"
)

const voidwalkerMinion = 8996 // the Warlock's pet: the summon spell 12746's misc value, a level 13-15 hostile (faction 554), no spawn row

const (
	ragefirePlan = "../../dungeons/ragefire_chasm.json"
	ragefireData = "../../tools/dungeongen/testdata/map389" // the world database export the plan was written from
)

func rfTSV(t *testing.T, name string) []map[string]string {
	t.Helper()
	f, err := os.Open(filepath.Join(ragefireData, name))
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

// ragefireExemption is a spawn the route test lets stand within 20 yd of the route without a pull group. Settled is empty until the mapping
// walk has decided it (the date of the walk and what it found); TestRagefireStatusNeedsSettledExemptions refuses a mapped or live-checked
// status while any is open.
type ragefireExemption struct {
	Reason, Settled string
	Level           int // the live level seen on the 2026-10-15 walk; the aggro radius is 20 - (16 - Level) yd for a level-16 party
}

// The six mobs of the terrace below the cave's north-east ledge and the ramp to Oggleflint (z -57 to -59, 17-20 yd under the route). Their floor
// joins the cave only by the south loop (the path to them from the tunnel is 320-337 yd), so a pull group would drag the party through the
// camps the route avoids. Settled on the mapping walk of 2026-10-15 (tests/walk-rfc/FINDINGS.txt): kept out of range by stand placement
// (TestRagefireStandsKeepClear: every look point of the plan is 20 yd or more from all six), the route past the ledge as far as the floor allows.
// A live aggro test with a visible character will confirm the line of sight (no RPC gives it).
const ragefireSettled = "2026-10-15 walk: kept out of range by stand placement, measured; live aggro test pending"

var ragefireExemptions = map[string]ragefireExemption{
	"38050": {Level: 15, Reason: "terrace pack of the ledge (Trogg, with 48637): route closest 20.6 yd, aggro radius 19", Settled: ragefireSettled},
	"48637": {Level: 13, Reason: "terrace pack of the ledge (Shaman, level 13, with 38050): route closest 18.4 yd, aggro radius 17", Settled: ragefireSettled},
	"48638": {Level: 15, Reason: "terrace Trogg, south loop only: route closest 20.2 yd, aggro radius 19", Settled: ragefireSettled},
	"38049": {Level: 13, Reason: "terrace pit under the Oggleflint ramp (Shaman): pull stand 20 yd or more away", Settled: ragefireSettled},
	"43580": {Level: 15, Reason: "terrace pit under the Oggleflint ramp (Shaman): pull stand 20 yd or more away", Settled: ragefireSettled},
	"43581": {Level: 13, Reason: "terrace pit under the Oggleflint ramp (Trogg, level 13): route closest 18.8 yd at the ramp foot, pull stand 25 yd", Settled: ragefireSettled},
}

// A plan that says mapped or live-checked has had its route corrected by a mapping walk (README "Dungeon plans"): while a terrace exemption is
// unsettled or a patrol's period is still the 2.5 yd/s estimate, the plan must stay a draft, so that dungeon_run refuses it without allow_draft.
func TestRagefireStatusNeedsSettledExemptions(t *testing.T) {
	p := loadPlan(t, ragefirePlan)
	if p.Status != DraftStatus && !strings.HasPrefix(p.Status, "mapped") && !strings.HasPrefix(p.Status, "live-checked") {
		t.Fatalf("status %q", p.Status)
	}
	if p.Status == DraftStatus {
		return
	}
	for guid, e := range ragefireExemptions {
		if e.Settled == "" {
			t.Errorf("status %q but the exemption of spawn %s (%s) is not settled: record the walk's date and finding in Settled, or add a controlled pull", p.Status, guid, e.Reason)
		}
	}
	for _, l := range p.Legs {
		for _, g := range l.PullGroups {
			if g.Patrol != nil && g.Patrol.PeriodEstimate {
				t.Errorf("status %q but the patrol of %v in %s still has period_estimate: true: measure it on the walk and drop the flag", p.Status, g.Patrol.Entries, l.Name)
			}
		}
	}
}

// The patrol periods were measured on the 2026-10-15 walk (1 Hz samples, tests/walk-rfc/patrol_*.jsonl): no patrol is marked period_estimate any
// more, and each carries the measured round (the Molten Elemental walks at 4.0 yd/s, not 2.5: 63.5 s, not the calculated 98).
func TestRagefirePatrolPeriodsAreMeasured(t *testing.T) {
	p := loadPlan(t, ragefirePlan)
	measured := map[string]float32{"48648": 115.7, "48655": 66, "48675": 120, "48698": 63.5, "48696": 173.5, "48707": 74.5}
	spawn := regexp.MustCompile(`creature_movement of spawn (\d+) `)
	seen := map[string]bool{}
	for _, l := range p.Legs {
		for _, g := range l.PullGroups {
			if g.Patrol == nil {
				continue
			}
			m := spawn.FindStringSubmatch(g.Patrol.Source)
			if m == nil || measured[m[1]] == 0 {
				t.Errorf("%s: patrol of %v: source names no known spawn", l.Name, g.Patrol.Entries)
				continue
			}
			seen[m[1]] = true
			if g.Patrol.PeriodEstimate || g.Patrol.PeriodS != measured[m[1]] {
				t.Errorf("%s: patrol of spawn %s: period_s %g estimate %v, measured %g", l.Name, m[1], g.Patrol.PeriodS, g.Patrol.PeriodEstimate, measured[m[1]])
			}
		}
	}
	if len(seen) != 6 {
		t.Errorf("%d patrolling spawns carried, want 6", len(seen))
	}
}

func TestRagefirePlanLoads(t *testing.T) {
	p := loadPlan(t, ragefirePlan)
	if p.Map != 389 || p.Name != "ragefire_chasm" || p.Status != "mapped (not live-checked)" || len(p.Legs) != 9 {
		t.Fatalf("%s %q %d, %d legs", p.Name, p.Status, p.Map, len(p.Legs))
	}
	var names []string
	var bosses []uint32
	for _, l := range p.Legs {
		names = append(names, l.Name)
		if l.Boss != nil {
			bosses = append(bosses, l.Boss.Entry)
		}
		if len(l.Steps) != 0 || len(l.AfterBoss) != 0 {
			t.Errorf("the map has no door, lever, gossip or item to use: %s has steps", l.Name)
		}
		for _, g := range l.PullGroups {
			if g.Team != "" || g.Ambush || len(g.Entries) != len(g.Positions) {
				t.Errorf("%s: unexpected group %+v", l.Name, g.Entries)
			}
		}
	}
	if fmt.Sprint(names) != "[tunnel tunnel_end trogg_cave oggleflint trogg_west molten_cave taragaman jergosh bazzalan]" {
		t.Fatalf("%v", names)
	}
	// Oggleflint, Taragaman, Jergosh, Bazzalan: the order the map's corridors lead through (TestRagefireOnNavmesh walks it).
	if fmt.Sprint(bosses) != "[11517 11520 11518 11519]" {
		t.Fatalf("%v", bosses)
	}
	// Entry: the area trigger 2230 on map 1, game_tele 209 `rfc` inside (so a remote run can also be teleported in), game_tele 46 outside.
	if p.EntranceTrigger != 2230 || p.EntranceTele != "rfc" || p.OutsideTele != "RagefireChasm" || p.Portal == nil || p.Portal.Map != 1 {
		t.Errorf("trigger %d tele %q outside %q portal %+v", p.EntranceTrigger, p.EntranceTele, p.OutsideTele, p.Portal)
	}
	// The healers and casters: Ragefire Shaman 11319 (Healing Wave) and Searing Blade Warlock 11324 flee at 15 % (EventAI 1131901, 1132401).
	// The Shaman is in kill_first too (rfc-live-2, leg 2 group 3): one is sheeped (cc_always), the others are killed first.
	if fmt.Sprint(p.Fleers) != "[11319 11324]" || fmt.Sprint(p.CCPriority) != "[11319 11324 11322]" || fmt.Sprint(p.CCAlways) != "[11319 11324]" || fmt.Sprint(p.KillFirst) != "[11319]" {
		t.Errorf("fleers %v cc_priority %v cc_always %v kill_first %v", p.Fleers, p.CCPriority, p.CCAlways, p.KillFirst)
	}
	if lo, hi := p.Band(); lo != 13 || hi != 18 {
		t.Errorf("band %d-%d", lo, hi)
	}
	// The plan is kept in the form Marshal writes.
	raw, err := os.ReadFile(ragefirePlan)
	if err != nil {
		t.Fatal(err)
	}
	out, err := Marshal(p)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(raw, out) {
		t.Errorf("%s is not in Marshal's form: run `go run ./tools/dungeongen -format %s`", ragefirePlan, ragefirePlan)
	}
}

// Every pack member and boss the plan names is a spawn of the export at that place, the boss's escorts stand within 25 yd of it, and a patrol's
// path is the creature_movement of the spawn its source names.
func TestRagefirePlanMatchesTheWorldExport(t *testing.T) {
	cs, mv := rfTSV(t, "creatures.tsv"), rfTSV(t, "movement.tsv")
	near := func(a, b float64) bool { return math.Abs(a-b) < 0.06 }
	has := func(entry uint32, at Point) bool {
		for _, m := range cs {
			x, _ := strconv.ParseFloat(m["x"], 64)
			y, _ := strconv.ParseFloat(m["y"], 64)
			z, _ := strconv.ParseFloat(m["z"], 64)
			if m["entry"] == strconv.Itoa(int(entry)) && near(x, float64(at.X)) && near(y, float64(at.Y)) && near(z, float64(at.Z)) {
				return true
			}
		}
		return false
	}
	p := loadPlan(t, ragefirePlan)
	spawn := regexp.MustCompile(`creature_movement of spawn (\d+) `)
	patrols := 0
	for i, l := range p.Legs {
		for j, g := range l.PullGroups {
			warlocks := 0
			for k, at := range g.Positions {
				if g.Entries[k] == 11324 {
					warlocks++
				}
				if g.Entries[k] == voidwalkerMinion {
					// Summoned by the Warlock when it spawns (EventAI 1132402): no spawn row. Its place is the live sighting of the 2026-10-15 walk, 2-4 yd
					// from a Searing Blade Warlock (11324) of the same group.
					near := false
					for q, e := range g.Entries {
						if e == 11324 && dist3(g.Positions[q], at) <= 4.5 {
							near = true
						}
					}
					if !near {
						t.Errorf("legs[%d].pull_groups[%d]: the Voidwalker Minion at %v is not within 4.5 yd of a Warlock of the group", i, j, at)
					}
					continue
				}
				if !has(g.Entries[k], at) {
					t.Errorf("legs[%d].pull_groups[%d]: no spawn of %d at %v", i, j, g.Entries[k], at)
				}
			}
			// Every Warlock of the route comes with its Voidwalker Minion (10 of 10 seen out before any combat on the walk).
			voids := 0
			for _, e := range g.Entries {
				if e == voidwalkerMinion {
					voids++
				}
			}
			if warlocks != voids {
				t.Errorf("legs[%d].pull_groups[%d]: %d Warlocks and %d Voidwalker Minions", i, j, warlocks, voids)
			}
			if g.Patrol == nil {
				continue
			}
			patrols++
			m := spawn.FindStringSubmatch(g.Patrol.Source)
			if m == nil {
				t.Errorf("legs[%d].pull_groups[%d].patrol: the source names no spawn: %q", i, j, g.Patrol.Source)
				continue
			}
			var rows []map[string]string
			for _, r := range mv {
				if r["guid"] == m[1] {
					rows = append(rows, r)
				}
			}
			if len(rows) != len(g.Patrol.Path) {
				t.Errorf("legs[%d].pull_groups[%d].patrol: %d points, the export has %d for spawn %s", i, j, len(g.Patrol.Path), len(rows), m[1])
				continue
			}
			for q, r := range rows {
				x, _ := strconv.ParseFloat(r["x"], 64)
				y, _ := strconv.ParseFloat(r["y"], 64)
				z, _ := strconv.ParseFloat(r["z"], 64)
				w := g.Patrol.Path[q]
				if math.Abs(x-float64(w.X)) > 0.06 || math.Abs(y-float64(w.Y)) > 0.06 || math.Abs(z-float64(w.Z)) > 0.06 {
					t.Errorf("legs[%d].pull_groups[%d].patrol point %d: %v, the export has %v %v %v", i, j, q, w, x, y, z)
				}
			}
			var self bool
			for _, r := range cs {
				if r["guid"] == m[1] && r["movement_type"] == "2" {
					self = true
				}
			}
			if !self {
				t.Errorf("legs[%d].pull_groups[%d].patrol: spawn %s is not a movement type 2 spawn", i, j, m[1])
			}
		}
		if l.Boss == nil {
			continue
		}
		if !has(l.Boss.Entry, l.Boss.Position) {
			t.Errorf("legs[%d].boss: no spawn of %d at %v", i, l.Boss.Entry, l.Boss.Position)
		}
		for _, e := range l.Boss.First {
			found := false
			for _, m := range cs {
				x, _ := strconv.ParseFloat(m["x"], 64)
				y, _ := strconv.ParseFloat(m["y"], 64)
				z, _ := strconv.ParseFloat(m["z"], 64)
				if m["entry"] == strconv.Itoa(int(e)) && dist3(l.Boss.Position, Point{float32(x), float32(y), float32(z)}) < 25 {
					found = true
				}
			}
			if !found {
				t.Errorf("legs[%d].boss.first: no spawn of %d within 25 yd of the boss", i, e)
			}
		}
	}
	if patrols != 6 { // six patrolling spawns, one group each: 48648's patrol block stays on the first pack of the cave only (route edit R2, 2026-10-16: the tunnel Earthborer never met it)
		t.Errorf("%d patrols, want the six patrolling mobs of the route", patrols)
	}
}

// Every waypoint of the plan is on the navmesh (within 2.5 yd; a pack off the floor within 20 yd) and reachable from the one before it, and
// the party never leaves the one dungeon: the routes between the legs' ends (boss to boss) are complete paths.
func from0(p *Plan) [3]float32 { return [3]float32{p.Entrance.X, p.Entrance.Y, p.Entrance.Z} }

func TestRagefireOnNavmesh(t *testing.T) {
	dir := mapMmaps(389)
	if dir == "" {
		t.Skip("no map 389 navmesh files: set TURTLE_AGENT_MMAPS to a folder holding 389.mmap and the 389*.mmtile tiles")
	}
	pl := navmesh.NewPlanner(dir)
	defer pl.Close()
	p := loadPlan(t, ragefirePlan)
	pl.SetAvoid(p.Map, NavBoxes(p.Avoid)) // the run's planner keeps out of the lava basin: the route must stand without it
	from := [3]float32{p.Entrance.X, p.Entrance.Y, p.Entrance.Z}
	checked := 0
	for i, l := range p.Legs {
		for j, w := range waypoints(l) {
			to := [3]float32{w.X, w.Y, w.Z}
			path, err := pl.Plan(p.Map, from, to)
			if errors.Is(err, navmesh.ErrNoEngine) {
				t.Skip("built without cgo: no navmesh engine")
			}
			if err != nil {
				t.Fatalf("legs[%d] waypoint %d: %v", i, j, err)
			}
			if path.Status == navmesh.Partial && isPullPosition(l, w) && len(path.Points) > 0 {
				e := path.Points[len(path.Points)-1]
				if math.Hypot(float64(e[0]-w.X), float64(e[1]-w.Y)) <= 20 && math.Abs(float64(e[2]-w.Z)) <= 4 {
					from = [3]float32{e[0], e[1], e[2]}
					checked++
					continue
				}
			}
			if path.Status != navmesh.Complete {
				t.Errorf("legs[%d] (%s) waypoint %d %v from %v: %s %s", i, l.Name, j, w, from, path.Status, path.Reason)
				continue
			}
			end := path.Points[len(path.Points)-1]
			if d := math.Hypot(float64(end[0]-w.X), float64(end[1]-w.Y)); d > 2.5 || math.Abs(float64(end[2]-w.Z)) > 4 {
				t.Errorf("legs[%d] waypoint %d: the path ends %.1f yd (z %.1f) from %v", i, j, d, end[2]-w.Z, w)
			}
			from = to
			checked++
		}
	}
	if checked < 80 {
		t.Fatalf("only %d waypoints checked", checked)
	}
	// The corridor from the entrance through the four bosses in order is one connected walk of about 1540 yd (navmesh shortest paths,
	// no detours for packs): Oggleflint 493, Taragaman +643, Jergosh +175, Bazzalan +230.
	at, total := from0(p), 0.0
	for _, l := range p.Legs {
		if l.Boss == nil {
			continue
		}
		to := [3]float32{l.Boss.Position.X, l.Boss.Position.Y, l.Boss.Position.Z}
		path, err := pl.Plan(p.Map, at, to)
		if err != nil || path.Status != navmesh.Complete {
			t.Fatalf("%s: no complete path to the boss from %v: %v %s", l.Name, at, err, path.Status)
		}
		for k := 1; k < len(path.Points); k++ {
			a, b := path.Points[k-1], path.Points[k]
			total += math.Sqrt(float64((a[0]-b[0])*(a[0]-b[0]) + (a[1]-b[1])*(a[1]-b[1]) + (a[2]-b[2])*(a[2]-b[2])))
		}
		at = to
	}
	if total < 1500 || total > 1600 {
		t.Errorf("the boss-to-boss corridor is %.0f yd, expected about 1540", total)
	}
	t.Logf("%d waypoints on the map 389 navmesh in %s, %.0f yd of corridor from the entrance through the four bosses", checked, dir, total)
}

// The mobs that stand with a boss and join his pull through creature_groups (flags 2). The plan's boss.first lists are empty since the
// 2026-10-16 route edits (a first entry is matched by id anywhere in view, and Oggleflint's drew a pull of a terrace Shaman); the route test
// still counts these as planned.
var ragefireBossEscort = map[uint32][]uint32{11517: {11319, 11318}, 11518: {11322}, 11519: {11322}}

// No hostile spawn of the export stands near the route without being in the plan: every mob within routeAggro yd (3-D, as the core's aggro
// check measures) of the navmesh path the lead bot walks from the entrance through the waypoints of each leg, in order, is a pull group
// member or a boss of the plan, a boss's escort (boss.first), or a skipped entry; the mobs the plan leaves out on purpose are listed here
// with their reason.
func TestRagefireRouteHasNoUnplannedPack(t *testing.T) {
	dir := mapMmaps(389)
	if dir == "" {
		t.Skip("no map 389 navmesh files: set TURTLE_AGENT_MMAPS to a folder holding 389.mmap and the 389*.mmtile tiles")
	}
	const routeAggro = 20
	p := loadPlan(t, ragefirePlan)
	planned := func(l Leg, entry uint32, at Point) bool {
		for _, q := range p.Legs {
			for _, g := range q.PullGroups {
				for k, pos := range g.Positions {
					if g.Entries[k] == entry && dist3(pos, at) < 1 {
						return true
					}
				}
			}
			if q.Boss != nil {
				if q.Boss.Entry == entry && dist3(q.Boss.Position, at) < 1 {
					return true
				}
				for _, e := range append(append([]uint32{}, q.Boss.First...), ragefireBossEscort[q.Boss.Entry]...) {
					if e == entry && dist3(q.Boss.Position, at) < 25 {
						return true
					}
				}
			}
		}
		return slicesContains(l.Skip, entry)
	}
	pl := navmesh.NewPlanner(dir)
	defer pl.Close()
	pl.SetAvoid(p.Map, NavBoxes(p.Avoid))
	cs := rfTSV(t, "creatures.tsv")
	from := [3]float32{p.Entrance.X, p.Entrance.Y, p.Entrance.Z}
	flagged := map[string]bool{}
	// The lead bot looks at a pack from up to lookYd away (LookPoint: the standoff, within the 25 yd view less its stop) and the tank pulls it
	// from there, so the route to a pull group ends at the first point of the path within lookYd of the group's first position, and only the
	// `via` points, `to` and those look walks are the route. (TestZulfarrakRouteHasNoUnplannedPack walks to every position; a cave this
	// dense chains every pull into the camp beside it.)
	const lookYd = 21
	type goal struct {
		at   Point
		look bool
	}
	for i, l := range p.Legs {
		var goals []goal
		for _, w := range l.Via {
			goals = append(goals, goal{w.Point, false})
		}
		goals = append(goals, goal{l.To, false})
		for _, g := range l.PullGroups {
			goals = append(goals, goal{g.Positions[0], true})
		}
		if l.Boss != nil {
			goals = append(goals, goal{l.Boss.Position, true})
		}
		for _, gl := range goals {
			to := [3]float32{gl.at.X, gl.at.Y, gl.at.Z}
			path, err := pl.Plan(p.Map, from, to)
			if errors.Is(err, navmesh.ErrNoEngine) {
				t.Skip("built without cgo: no navmesh engine")
			}
			if err != nil {
				t.Fatal(err)
			}
			pts := make([]Point, len(path.Points))
			for k, v := range path.Points {
				pts[k] = Point{v[0], v[1], v[2]}
			}
			if gl.look { // cut the route where the lead bot stops to look
				for k, q := range pts {
					if dist3(q, gl.at) <= lookYd {
						pts = pts[:k+1]
						break
					}
				}
			}
			if len(pts) > 0 {
				e := pts[len(pts)-1]
				from = [3]float32{e.X, e.Y, e.Z}
			}
			for k := 1; k < len(pts); k++ {
				a, b := pts[k-1], pts[k]
				for _, m := range cs {
					if m["faction"] == "35" {
						continue // friendly: Maur Grimtotem
					}
					e64, _ := strconv.ParseUint(m["entry"], 10, 32)
					x, _ := strconv.ParseFloat(m["x"], 64)
					y, _ := strconv.ParseFloat(m["y"], 64)
					z, _ := strconv.ParseFloat(m["z"], 64)
					at := Point{float32(x), float32(y), float32(z)}
					if flagged[m["guid"]] || ragefireExemptions[m["guid"]].Reason != "" || planned(l, uint32(e64), at) || distToSegment(at, a, b) > routeAggro {
						continue
					}
					flagged[m["guid"]] = true
					t.Errorf("legs[%d] (%s): %s %d (spawn row %s) at %.1f %.1f %.1f stands within %d yd of the route and is in no pull group", i, l.Name, m["name"], e64, m["guid"], x, y, z, routeAggro)
				}
			}
		}
	}
}

// ragefireLook is one look the plan check makes: where the lead bot stands to look at pack, and the route it walked there (the start of the
// path up to the stand).
type ragefireLook struct {
	Pack, Stand Point
	Route       []Point
}

// ragefireLooks runs the plan check with a look function that records every look point and the route to it.
func ragefireLooks(t *testing.T, p *Plan, pl *navmesh.Planner) []ragefireLook {
	t.Helper()
	var out []ragefireLook
	rec := func(path []Point, pack Point, standoff, view float32, snap func(Point) Point) (Point, bool) {
		s, ok := LookPoint(path, pack, standoff, view, snap)
		var route []Point
		for k := 1; k < len(path); k++ {
			route = append(route, path[k-1])
			if distToSegment(s, path[k-1], path[k]) < 0.6 {
				break
			}
		}
		out = append(out, ragefireLook{pack, s, append(route, s)})
		return s, ok
	}
	if _, err := checkPlan(p, pl, CheckOptions{}, rec); err != nil {
		if errors.Is(err, navmesh.ErrNoEngine) {
			t.Skip("built without cgo: no navmesh engine")
		}
		t.Fatal(err)
	}
	return out
}

func rfSpawn(t *testing.T, guid string) Point {
	t.Helper()
	for _, m := range rfTSV(t, "creatures.tsv") {
		if m["guid"] == guid {
			x, _ := strconv.ParseFloat(m["x"], 64)
			y, _ := strconv.ParseFloat(m["y"], 64)
			z, _ := strconv.ParseFloat(m["z"], 64)
			return Point{float32(x), float32(y), float32(z)}
		}
	}
	t.Fatalf("no spawn row %s", guid)
	return Point{}
}

func minDistToRoute(r []Point, at Point) float64 {
	best := math.MaxFloat64
	for k := 1; k < len(r); k++ {
		best = math.Min(best, distToSegment(at, r[k-1], r[k]))
	}
	return best
}

// The terrace exemptions are settled by where the party stands (README "The Ragefire Chasm plan"): measured on the 2026-10-15 walk, an
// attacker's aggro radius is 20 - (party level - mob level) yd, 3-D, so 16-19 yd for a level-16 party and these mobs. Every look point of the
// plan stays an aggro radius and 2 yd (19 yd for a level-13 mob, 21 for a level-15) from the six terrace mobs and from the south-west camp; the route past the cave opening is as far as the ledge
// allows (the mobs are 16.6 to 18.3 yd below it: no lateral move gains the missing 1-2 yd); Bazzalan's left add is pulled from 29 yd or more.
func TestRagefireStandsKeepClear(t *testing.T) {
	dir := mapMmaps(389)
	if dir == "" {
		t.Skip("no map 389 navmesh files: set TURTLE_AGENT_MMAPS to a folder holding 389.mmap and the 389*.mmtile tiles")
	}
	pl := navmesh.NewPlanner(dir)
	defer pl.Close()
	p := loadPlan(t, ragefirePlan)
	pl.SetAvoid(p.Map, NavBoxes(p.Avoid))
	looks := ragefireLooks(t, p, pl)
	if len(looks) < 60 {
		t.Fatalf("only %d looks", len(looks))
	}
	var terrace, camp []Point
	var terraceNeed []float64
	for guid, e := range ragefireExemptions {
		terrace = append(terrace, rfSpawn(t, guid))
		terraceNeed = append(terraceNeed, float64(20-(16-e.Level)+2)) // its aggro radius and a 2 yd margin
	}
	for _, guid := range []string{"48665", "48667", "48671"} {
		camp = append(camp, rfSpawn(t, guid))
	}
	bazzalan := rfSpawn(t, "48750")
	for _, lk := range looks {
		for k, q := range terrace {
			if d := dist3(lk.Stand, q); d < terraceNeed[k] {
				t.Errorf("the look point %v of the pack at %v is %.1f yd from the terrace mob at %v, want %.0f or more (its aggro radius and 2 yd)", lk.Stand, lk.Pack, d, q, terraceNeed[k])
			}
		}
		for _, q := range camp {
			if d := dist3(lk.Stand, q); d < 20 {
				t.Errorf("the look point %v of the pack at %v is %.1f yd from the camp mob at %v", lk.Stand, lk.Pack, d, q)
			}
		}
		if dist3(lk.Pack, rfSpawn(t, "48751")) < 0.5 {
			if d := dist3(lk.Stand, bazzalan); d < 29 {
				t.Errorf("the pull of Bazzalan's left add stands %.1f yd from him, want 29 or more", d)
			}
		}
		t.Logf("pack %.1f %.1f %.1f from %.1f %.1f %.1f", lk.Pack.X, lk.Pack.Y, lk.Pack.Z, lk.Stand.X, lk.Stand.Y, lk.Stand.Z)
	}
	// The route's closest approach to each terrace mob (walk 2026-10-15, one walker on the line: 48637 18.4, 48638 20.2, 38050 20.6, pit Trogg
	// 43581 18.8): the plan's route must not come nearer than the walk measured, less a yard.
	want := map[string]float64{"48637": 17.4, "48638": 19.2, "38050": 19.6, "43581": 17.8, "38049": 19, "43580": 19}
	for guid, min := range want {
		at := rfSpawn(t, guid)
		best := math.MaxFloat64
		for _, lk := range looks {
			best = math.Min(best, minDistToRoute(lk.Route, at))
		}
		t.Logf("route to terrace mob %s: closest %.1f yd (the walk measured %.1f)", guid, best, min+1)
		if best < min {
			t.Errorf("the route comes %.1f yd from the terrace mob %s, the walk measured %.1f", best, guid, min+1)
		}
	}
}

// ragefireLavaDeaths are the bodies of the bots that died in rfc-live-1d and 1e with no damage in the feed (botwatch.jsonl and the 1d trace
// frames, the first frame that shows each dead): all on the trogg cave's lava basin floor, under the magma surface (z -64.5).
var ragefireLavaDeaths = []Point{
	{-204.6, -25.2, -64.9}, {-206.5, -26.2, -65}, {-206.6, -27.4, -65}, {-198.8, -57.8, -64.6}, // 1e, first wipe of leg 3
	{-204.8, -56.4, -64.9}, {-209.4, -57.4, -64.9}, {-206, -54.9, -65}, {-201, -52.5, -64.6}, // 1e, the walk back across the basin
	{-204.7, -31.7, -64.9}, {-202.2, -23.6, -64.8}, {-206.9, -24.4, -64.9}, // 1e r1, summoned into the basin
	{-199.8, -53.9, -65.5}, {-206.2, -26.9, -64.9}, {-217.7, -56.7, -65.8}, // 1d: the tank, the healer, a dps
}

// The plan's avoid box holds every lava death of the two runs, and the floors the route walks next to the basin stay outside it.
func TestRagefireLavaBoxHoldsTheDeaths(t *testing.T) {
	p := loadPlan(t, ragefirePlan)
	if len(p.Avoid) != 1 {
		t.Fatalf("avoid %+v, want the lava basin", p.Avoid)
	}
	for _, at := range ragefireLavaDeaths {
		if _, in := p.InAvoid(at); !in {
			t.Errorf("the lava death at %v is outside the avoid box", at)
		}
	}
	// The strip and the ramp foot (leg 2's last packs and its end), the ramp stand of leg 3 and the south rim (-219.8 -64.2, where 1e's
	// tank fought) are walkable.
	for _, at := range []Point{{-215.45, -15.93, -61.8}, {-204.72, -1.24, -61.68}, {-183.39, 2.8, -56.81}, {-192, 0, -59.5}, {-170.8, 2.5, -50.8}, {-219.8, -64.2, -61.2}, {-209.3, -17.3, -60.9}} {
		if b, in := p.InAvoid(at); in {
			t.Errorf("%v lies in the avoid box %q", at, b.Name)
		}
	}
}

// On the map's navmesh the planner, told the plan's avoid box, keeps out of the basin: the walk from the south rim back to the ramp's foot
// (rfc-live-1e: planned straight over the basin floor, cmd move -207.8 -56.1 -65.5, three bots dead) goes round by the west, no lava death
// spot has a walkable point under the lava within 5 yd, and leg 3's stand is reached.
func TestRagefirePlannerKeepsOutOfTheLava(t *testing.T) {
	dir := mapMmaps(389)
	if dir == "" {
		t.Skip("no map 389 navmesh files: set TURTLE_AGENT_MMAPS to a folder holding 389.mmap and the 389*.mmtile tiles")
	}
	p := loadPlan(t, ragefirePlan)
	pl := navmesh.NewPlanner(dir)
	defer pl.Close()
	from, to := vec(Point{-219.8, -64.2, -61.2}), vec(Point{-186.3, -0.3, -54.4})
	inLava := func(path navmesh.Path) int {
		n := 0
		for _, seg := range append([][]navmesh.Vec3{path.Points}, path.Ground...) {
			for _, q := range seg {
				if _, in := p.InAvoid(point(q)); in {
					n++
				}
			}
		}
		return n
	}
	before, err := pl.Plan(p.Map, from, to)
	if errors.Is(err, navmesh.ErrNoEngine) {
		t.Skip("built without cgo: no navmesh engine")
	}
	if err != nil || inLava(before) == 0 {
		t.Fatalf("without the box the path should cross the basin (the run's): %v, %d points in the box", err, inLava(before))
	}
	pl.SetAvoid(p.Map, NavBoxes(p.Avoid))
	after, err := pl.Plan(p.Map, from, to)
	if err != nil || after.Status != navmesh.Complete || inLava(after) != 0 {
		t.Fatalf("with the box: %v %s, %d points in the box: %v", err, after.Status, inLava(after), after.Points)
	}
	// No death spot keeps a walkable floor under the magma surface (z -64.5, plus the 0.1 yd the core still counts as in it) near it.
	for _, at := range ragefireLavaDeaths {
		if n, ok, err := pl.Nearest(p.Map, vec(at)); err != nil || (ok && dist3(point(n), at) < 5 && n[2] < -64.4) {
			t.Errorf("a walkable point %v %v under the lava within 5 yd of the death at %v", n, ok, at)
		}
	}
	// Every leg of the plan stays reachable with the box set: the plan check (which sets it) passes.
	if res, err := CheckPlan(p, pl, 0); err != nil || !res.OK() {
		t.Fatalf("plan check with the avoid box: %v\n%s", err, res.Summary())
	}
	stand := p.Legs[3].To
	if path, err := pl.Plan(p.Map, vec(Point{-183.39, 2.8, -56.81}), vec(stand)); err != nil || path.Status != navmesh.Complete || path.Length > 20 {
		t.Fatalf("leg 3's stand from leg 2's end: %v %+v", err, path)
	}
}

// rfc-observe-1 (2026-10-17) paused twice at step 13: the five-pack (trogg_cave pull group 2) was 95 yd from the party's stand and the run
// leader's own hops were undone by its gather back to the last point it had walked to. Pull groups 1 and 2 of the cave carry the stand the
// party walks to before each pull: in the run leader's view of the pack (60 yd), outside the aggro range of every member, outside the avoid box.
func TestRagefireTroggCavePullsHaveApproachStands(t *testing.T) {
	p := loadPlan(t, ragefirePlan)
	var cave *Leg
	for i := range p.Legs {
		if p.Legs[i].Name == "trogg_cave" {
			cave = &p.Legs[i]
		}
	}
	if cave == nil {
		t.Fatal("no trogg_cave leg")
	}
	for gi, want := range map[int]Point{1: {-205, -34, -49.9}, 2: {-221.2, -34.7, -54.7}} {
		g := cave.PullGroups[gi]
		if len(g.Approach) != 1 || g.Approach[0] != want {
			t.Errorf("trogg_cave pull group %d: approach %v, want [%v]", gi, g.Approach, want)
			continue
		}
		a := g.Approach[0]
		if b, in := p.InAvoid(a); in {
			t.Errorf("trogg_cave pull group %d: approach %v lies in the avoid box %q", gi, a, b.Name)
		}
		nearest, farthest := math.MaxFloat64, 0.0
		for _, m := range g.Positions {
			d := math.Hypot(float64(a.X-m.X), float64(a.Y-m.Y))
			nearest, farthest = math.Min(nearest, d), math.Max(farthest, d)
		}
		if nearest < 25 || farthest > 50 {
			t.Errorf("trogg_cave pull group %d: approach %v stands %.1f to %.1f yd from the pack, want 25 to 50", gi, a, nearest, farthest)
		}
	}
}
