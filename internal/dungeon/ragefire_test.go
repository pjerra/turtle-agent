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
type ragefireExemption struct{ Reason, Settled string }

// The six mobs of the terrace below the cave's north-east ledge and the ramp to Oggleflint (z -57 to -59, 17-20 yd under the route). Their floor
// joins the cave only by the south loop (the path to them from the tunnel is 378 yd, through the camps in the south of the cave), so a pull group
// would drag the party through everything the route avoids. They may aggro as the party passes above and then come round the loop. Settle on the
// walk: either it shows they do not aggro (they stay listed, Settled gets the walk's date and the finding) or the plan gets a controlled pull.
var ragefireExemptions = map[string]ragefireExemption{
	"38050": {Reason: "terrace pack of the ledge (Trogg, with 48637)"},
	"48637": {Reason: "terrace pack of the ledge (Shaman, with 38050)"},
	"48638": {Reason: "terrace Trogg, south loop only"},
	"38049": {Reason: "terrace pit under the Oggleflint ramp (Shaman)"},
	"43580": {Reason: "terrace pit under the Oggleflint ramp (Shaman)"},
	"43581": {Reason: "terrace pit under the Oggleflint ramp (Trogg)"},
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

// While the plan is a draft, every patrol has period_estimate: true (the walk measures them), and the marker is on all six.
func TestRagefirePatrolPeriodsAreMarkedEstimated(t *testing.T) {
	p := loadPlan(t, ragefirePlan)
	n := 0
	for _, l := range p.Legs {
		for _, g := range l.PullGroups {
			if g.Patrol != nil {
				n++
				if p.Status == DraftStatus && !g.Patrol.PeriodEstimate {
					t.Errorf("%s: the patrol of %v is not marked period_estimate", l.Name, g.Patrol.Entries)
				}
			}
		}
	}
	if n != 6 {
		t.Errorf("%d patrols", n)
	}
}

func TestRagefirePlanLoads(t *testing.T) {
	p := loadPlan(t, ragefirePlan)
	if p.Map != 389 || p.Name != "ragefire_chasm" || p.Status != DraftStatus || len(p.Legs) != 9 {
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
	if fmt.Sprint(p.Fleers) != "[11319 11324]" || fmt.Sprint(p.CCPriority) != "[11319 11324 11322]" || fmt.Sprint(p.CCAlways) != "[11319 11324]" || fmt.Sprint(p.KillFirst) != "[11319 11324 11322]" {
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
			for k, at := range g.Positions {
				if !has(g.Entries[k], at) {
					t.Errorf("legs[%d].pull_groups[%d]: no spawn of %d at %v", i, j, g.Entries[k], at)
				}
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
	if patrols != 6 {
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
				for _, e := range q.Boss.First {
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
