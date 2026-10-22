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
	deadminesPlan = "../../dungeons/deadmines.json"
	deadminesData = "../../tools/dungeongen/testdata/map36" // the world database export the plan was written from
)

func dmTSV(t *testing.T, name string) []map[string]string {
	t.Helper()
	f, err := os.Open(filepath.Join(deadminesData, name))
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

func dmFloat(m map[string]string, k string) float64 {
	v, _ := strconv.ParseFloat(m[k], 64)
	return v
}

func dmLegSteps(p *Plan, name string) (l Leg) {
	for _, q := range p.Legs {
		if q.Name == name {
			return q
		}
	}
	return l
}

func TestDeadminesPlanLoads(t *testing.T) {
	p := loadPlan(t, deadminesPlan)
	if p.Map != 36 || p.Name != "deadmines" || p.Status != DraftStatus || len(p.Legs) != 16 {
		t.Fatalf("%s %q %d, %d legs", p.Name, p.Status, p.Map, len(p.Legs))
	}
	var names []string
	var bosses []uint32
	for _, l := range p.Legs {
		names = append(names, l.Name)
		if l.Boss != nil {
			bosses = append(bosses, l.Boss.Entry)
		}
		for _, g := range l.PullGroups {
			if g.Team != "" || len(g.Entries) != len(g.Positions) {
				t.Errorf("%s: unexpected group %+v", l.Name, g.Entries)
			}
		}
	}
	if fmt.Sprint(names) != "[mine_a rhahk_zor factory sneed west_area hall harvester gilnid cannon_a cannon dock mr_smite deck_a deck_b captain_greenskin edwin_vancleef]" {
		t.Fatalf("%v", names)
	}
	// Rhahk'Zor, Sneed's Shredder, the Masterpiece Harvester (Turtle's Foe Reaper, a side branch the navmesh reaches before the foundry), Gilnid,
	// Mr. Smite, Captain Greenskin, Edwin VanCleef.
	if fmt.Sprint(bosses) != "[644 642 61963 1763 646 647 639]" {
		t.Fatalf("%v", bosses)
	}
	// Entry: area trigger 78 on map 0, game_tele 174 `deadmines` inside and 288 `TheDeadmines` outside.
	if p.EntranceTrigger != 78 || p.EntranceTele != "deadmines" || p.OutsideTele != "TheDeadmines" || p.Portal == nil || p.Portal.Map != 0 {
		t.Errorf("trigger %d tele %q outside %q portal %+v", p.EntranceTrigger, p.EntranceTele, p.OutsideTele, p.Portal)
	}
	// EventAI "flee at 15 %" (command 47) of the entries the plan fights: miners, engineers, overseers, watchmen, evokers, craftsmen,
	// squallshapers, shipbuilders and wizards.
	if fmt.Sprint(p.Fleers) != "[598 622 634 1725 1729 1731 1732 3947 4418]" || fmt.Sprint(p.CCPriority) != "[622 1731 4418 1729 1732]" || fmt.Sprint(p.CCAlways) != "[622]" || fmt.Sprint(p.KillFirst) != "[636 622]" {
		t.Errorf("fleers %v cc_priority %v cc_always %v kill_first %v", p.Fleers, p.CCPriority, p.CCAlways, p.KillFirst)
	}
	if lo, hi := p.Band(); lo != 18 || hi != 22 {
		t.Errorf("band %d-%d", lo, hi)
	}
	if len(p.HireComposition) != 5 {
		t.Errorf("hire_composition %v", p.HireComposition)
	}
	// The plan is kept in the form Marshal writes.
	raw, err := os.ReadFile(deadminesPlan)
	if err != nil {
		t.Fatal(err)
	}
	out, err := Marshal(p)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(raw, out) {
		t.Errorf("%s is not in Marshal's form: run `go run ./tools/dungeongen -format %s`", deadminesPlan, deadminesPlan)
	}
}

// The doors and the event, as the instance script and the gameobject_scripts rows have them: every door the clear needs is opened by a step or a wait,
// in the order the route meets them, and the cannon needs the Defias Gunpowder before it.
func TestDeadminesDoorsAndEvents(t *testing.T) {
	p := loadPlan(t, deadminesPlan)
	gostate := func(s Step) (uint32, string) {
		if s.WaitFor != nil && s.WaitFor.GameObject != nil {
			return s.WaitFor.GameObject.Entry, s.WaitFor.GameObject.State
		}
		return 0, ""
	}
	// Rhahk'Zor's death opens the Factory Door 13965.
	if l := dmLegSteps(p, "rhahk_zor"); len(l.AfterBoss) != 1 {
		t.Fatalf("rhahk_zor after_boss %+v", l.AfterBoss)
	} else if e, st := gostate(l.AfterBoss[0]); e != 13965 || st != "active" {
		t.Errorf("rhahk_zor waits for %d %s", e, st)
	}
	// The two Heavy Doors that have no script are clicked, then waited for.
	for leg, entry := range map[string]uint32{"factory": 17153, "west_area": 17154} {
		l := dmLegSteps(p, leg)
		if len(l.Steps) != 2 || l.Steps[0].UseObject == nil || l.Steps[0].UseObject.Entry != entry || l.Steps[0].UseObject.Stand == nil {
			t.Fatalf("%s steps %+v", leg, l.Steps)
		}
		if e, st := gostate(l.Steps[1]); e != entry || st != "active" {
			t.Errorf("%s waits for %d %s", leg, e, st)
		}
	}
	// Sneed's Shredder: the ejected Sneed is killed (no spawn row, so a wait for the hostiles to be gone), then the door he opens.
	sn := dmLegSteps(p, "sneed")
	if len(sn.AfterBoss) != 2 || sn.AfterBoss[0].WaitFor == nil || sn.AfterBoss[0].WaitFor.HostilesGone == nil {
		t.Fatalf("sneed after_boss %+v", sn.AfterBoss)
	}
	if e, st := gostate(sn.AfterBoss[1]); e != 17153 || st != "active" || sn.AfterBoss[1].WaitFor.GameObject.Near.X > -290 || sn.AfterBoss[1].WaitFor.GameObject.Near.X < -291 {
		t.Errorf("sneed waits for %d %s %+v", e, st, sn.AfterBoss[1].WaitFor.GameObject)
	}
	// Gilnid's death opens spawn 26182 at -168.5 -579.9.
	gi := dmLegSteps(p, "gilnid")
	if len(gi.AfterBoss) != 1 {
		t.Fatalf("gilnid after_boss %+v", gi.AfterBoss)
	}
	if e, st := gostate(gi.AfterBoss[0]); e != 17153 || st != "active" || gi.AfterBoss[0].WaitFor.GameObject.Near.X < -169 || gi.AfterBoss[0].WaitFor.GameObject.Near.X > -168 {
		t.Errorf("gilnid waits for %d %s", e, st)
	}
	// The cannon: gunpowder (item 5397) looted, the Overseer it summons killed, the cannon used with the item, the Iron Clad Door at ACTIVE_ALTERNATIVE.
	c := dmLegSteps(p, "cannon")
	if len(c.Steps) != 4 {
		t.Fatalf("cannon steps %+v", c.Steps)
	}
	lo := c.Steps[0].LootObject
	if lo == nil || lo.Entry != 17155 || lo.ExpectItem == nil || lo.ExpectItem.Entry != 5397 {
		t.Errorf("cannon steps[0] %+v", lo)
	}
	if hg := c.Steps[1].WaitFor; hg == nil || hg.HostilesGone == nil || hg.HostilesGone.Radius < 40 {
		t.Errorf("cannon steps[1] %+v", c.Steps[1].WaitFor)
	}
	uo := c.Steps[2].UseObject
	if uo == nil || uo.Entry != 16398 || uo.RequiresItem == nil || uo.RequiresItem.Entry != 5397 {
		t.Errorf("cannon steps[2] %+v", uo)
	}
	if e, st := gostate(c.Steps[3]); e != 16397 || st != "active_alt" {
		t.Errorf("cannon waits for %d %s", e, st)
	}
	// Mr. Smite's and VanCleef's Blackguards are stealthed: ambush groups before the boss, not boss.first.
	for _, leg := range []string{"mr_smite", "edwin_vancleef"} {
		l := dmLegSteps(p, leg)
		last := l.PullGroups[len(l.PullGroups)-1]
		if !last.Ambush || fmt.Sprint(last.Entries) != "[636 636]" || len(l.Boss.First) != 0 {
			t.Errorf("%s: last group %+v first %v", leg, last, l.Boss.First)
		}
	}
	// Greenskin's Defias Companion (3450, no spawn row) and his Pirate are his first; the Squallshaper of his formation is crowd-controlled and killed last.
	if g := dmLegSteps(p, "captain_greenskin"); fmt.Sprint(g.Boss.First) != "[3450 657]" {
		t.Errorf("greenskin first %v", g.Boss.First)
	}
}

// Every pack member and boss the plan names is a spawn of the export at that place, the boss's escorts stand within 25 yd of it, the objects of
// the steps are spawns of the export, and a patrol's path is the creature_movement of the spawn its source names.
func TestDeadminesPlanMatchesTheWorldExport(t *testing.T) {
	cs, gos, mv := dmTSV(t, "creatures.tsv"), dmTSV(t, "gameobjects.tsv"), dmTSV(t, "movement.tsv")
	near := func(a, b float64) bool { return math.Abs(a-b) < 0.06 }
	hasC := func(entry uint32, at Point) bool {
		for _, m := range cs {
			if m["entry"] == strconv.Itoa(int(entry)) && near(dmFloat(m, "x"), float64(at.X)) && near(dmFloat(m, "y"), float64(at.Y)) && near(dmFloat(m, "z"), float64(at.Z)) {
				return true
			}
		}
		return false
	}
	hasG := func(entry uint32, at Point) bool {
		for _, m := range gos {
			if m["entry"] == strconv.Itoa(int(entry)) && near(dmFloat(m, "x"), float64(at.X)) && near(dmFloat(m, "y"), float64(at.Y)) && near(dmFloat(m, "z"), float64(at.Z)) {
				return true
			}
		}
		return false
	}
	p := loadPlan(t, deadminesPlan)
	spawn := regexp.MustCompile(`creature_movement of spawn (\d+) `)
	patrols := 0
	for i, l := range p.Legs {
		for j, g := range l.PullGroups {
			if len(g.Entries) != len(g.Positions) {
				t.Errorf("legs[%d].pull_groups[%d]: %d entries, %d positions", i, j, len(g.Entries), len(g.Positions))
				continue
			}
			for k, at := range g.Positions {
				if g.Entries[k] == defiasCompanion {
					// A level 14-15 pet the server makes with a Pirate at the start of the instance (no spawn row; 9 seen on the 2026-10-15 walk, 2.3 to 4.8 yd
					// from a Pirate of the same group, or at the spawn point of a patrolling Pirate).
					near := false
					for q, e := range g.Entries {
						if e == 657 && dist3(g.Positions[q], at) <= 5.5 {
							near = true
						}
					}
					if !near {
						t.Errorf("legs[%d] (%s).pull_groups[%d]: the Defias Companion at %v is not within 5.5 yd of a Pirate of the group", i, l.Name, j, at)
					}
					continue
				}
				if !hasC(g.Entries[k], at) && !onOwnPatrolPath(g, g.Entries[k], at) {
					t.Errorf("legs[%d] (%s).pull_groups[%d]: no spawn of %d at %v", i, l.Name, j, g.Entries[k], at)
				}
			}
			if pt := g.Patrol; pt != nil {
				patrols++
				m := spawn.FindStringSubmatch(pt.Source)
				if m == nil {
					t.Errorf("legs[%d].pull_groups[%d].patrol: the source names no spawn: %q", i, j, pt.Source)
					continue
				}
				var rows []map[string]string
				for _, r := range mv {
					if r["guid"] == m[1] {
						rows = append(rows, r)
					}
				}
				if len(rows) != len(pt.Path) || len(rows) < 2 {
					t.Errorf("legs[%d].pull_groups[%d].patrol: %d points, creature_movement of %s has %d", i, j, len(pt.Path), m[1], len(rows))
					continue
				}
				for k, r := range rows {
					q := pt.Path[k]
					if math.Abs(dmFloat(r, "x")-float64(q.X)) > 0.06 || math.Abs(dmFloat(r, "y")-float64(q.Y)) > 0.06 || math.Abs(dmFloat(r, "z")-float64(q.Z)) > 0.06 {
						t.Errorf("legs[%d].pull_groups[%d].patrol point %d is %v, creature_movement of %s says %s %s %s", i, j, k, q, m[1], r["x"], r["y"], r["z"])
					}
				}
			}
		}
		if l.Boss != nil {
			if !hasC(l.Boss.Entry, l.Boss.Position) {
				t.Errorf("legs[%d].boss: no spawn of %d at %v", i, l.Boss.Entry, l.Boss.Position)
			}
			for _, e := range l.Boss.First {
				if e == defiasCompanion {
					continue // Greenskin's Pirate carries one
				}
				found := false
				for _, m := range cs {
					if m["entry"] == strconv.Itoa(int(e)) && dist3(l.Boss.Position, Point{float32(dmFloat(m, "x")), float32(dmFloat(m, "y")), float32(dmFloat(m, "z"))}) < 25 {
						found = true
					}
				}
				if !found {
					t.Errorf("legs[%d].boss.first: no spawn of %d within 25 yd of the boss", i, e)
				}
			}
		}
		for _, steps := range [][]Step{l.Steps, l.AfterBoss} {
			for j, s := range steps {
				if r := objRef(s); r != nil && r.Near != nil && !hasG(r.Entry, *r.Near) {
					t.Errorf("legs[%d] (%s) step %d: no game object %d at %v", i, l.Name, j, r.Entry, *r.Near)
				}
			}
		}
	}
	if patrols < 20 {
		t.Errorf("only %d patrols", patrols)
	}
}

// onOwnPatrolPath reports whether at is a point of the patrol path of the group's own patrol, and the entry is one the patrol lists: a patrolling
// unit the plan counts in a group it passes (the Manufactured Golem 2582923 walks to the Harvester room's door: harvester group 0 lists its entry
// at the path point nearest the group, so that the pull's pack, and the joiner estimate, count it) stands at a point of its beat, not at its spawn.
func onOwnPatrolPath(g PullGroup, entry uint32, at Point) bool {
	if g.Patrol == nil || !slicesContains(g.Patrol.Entries, entry) {
		return false
	}
	for _, q := range g.Patrol.Path {
		if dist3(q, at) < 0.06 {
			return true
		}
	}
	return false
}

// deadminesPatrolGuids are the spawn rows of the patrols the plan carries: the spawn its source names and its formation members.
func deadminesPatrolGuids(p *Plan) map[string]bool {
	out := map[string]bool{}
	num := regexp.MustCompile(`\d{5,}`)
	spawn := regexp.MustCompile(`creature_movement of spawn (\d+) `)
	for _, l := range p.Legs {
		for _, g := range l.PullGroups {
			if g.Patrol == nil {
				continue
			}
			if m := spawn.FindStringSubmatch(g.Patrol.Source); m != nil {
				out[m[1]] = true
			}
			if i := strings.Index(g.Patrol.Source, "formation members "); i >= 0 {
				rest := g.Patrol.Source[i+len("formation members "):]
				if j := strings.Index(rest, ";"); j >= 0 {
					rest = rest[:j]
				}
				for _, n := range num.FindAllString(rest, -1) {
					out[n] = true
				}
			}
		}
	}
	return out
}

// deadminesPlanned reports whether the plan accounts for a spawn: a pull group member, a boss, a boss.first escort, or an entry the leg skips.
func deadminesPlanned(p *Plan, l Leg, entry uint32, at Point, guid string) bool {
	if deadminesPatrolGuids(p)[guid] {
		return true // a member of a patrol the plan carries: it is met on its path, not at its spawn point
	}
	if slicesContains(l.Skip, entry) {
		return true
	}
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
			if dist3(q.Boss.Position, at) < 6 {
				return true // the boss's own formation (Greenskin's Squallshaper, spawn 79335)
			}
			if slicesContains(q.Boss.First, entry) && dist3(q.Boss.Position, at) < 25 {
				return true
			}
		}
	}
	return false
}

// Every waypoint of the plan is on the navmesh and reachable from the one before it (the navmesh has no doors, so the door steps are checked in
// the game), and a pack off the mesh is pulled from where the party can stand: within 20 yd and 4 up or down.
func TestDeadminesOnNavmesh(t *testing.T) {
	dir := mapMmaps(36)
	if dir == "" {
		t.Skip("no map 36 navmesh files: set TURTLE_AGENT_MMAPS to a folder holding 036.mmap and the 036*.mmtile tiles")
	}
	p := loadPlan(t, deadminesPlan)
	pl := navmesh.NewPlanner(dir)
	defer pl.Close()
	from := [3]float32{p.Entrance.X, p.Entrance.Y, p.Entrance.Z}
	checked, length := 0, 0.0
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
			length += float64(path.Length)
			from = to
			checked++
		}
	}
	if checked < 150 {
		t.Fatalf("only %d waypoints checked", checked)
	}
	t.Logf("%d waypoints on the map 36 navmesh in %s, %.0f yd of path", checked, dir, length)
}

// Every plan point has navmesh floor under it (a creature's own spawn gets 4 up or down and 2.5 on the ground, the rest meshMaxDZ and meshMaxDH).
func TestDeadminesPlanPointsOnMesh(t *testing.T) {
	dir := mapMmaps(36)
	if dir == "" {
		t.Skip("no map 36 navmesh files: set TURTLE_AGENT_MMAPS to a folder holding 036.mmap and the 036*.mmtile tiles")
	}
	p := loadPlan(t, deadminesPlan)
	pl := navmesh.NewPlanner(dir)
	defer pl.Close()
	checked := 0
	for i, l := range p.Legs {
		pts, labels, _ := planPoints(l)
		for j, w := range pts {
			where := fmt.Sprintf("legs[%d] (%s) %s %v", i, l.Name, labels[j], w)
			n, ok, err := pl.Nearest(p.Map, navmesh.Vec3{w.X, w.Y, w.Z})
			if errors.Is(err, navmesh.ErrNoEngine) {
				t.Skip("built without cgo: no navmesh engine")
			}
			if err != nil {
				t.Fatalf("%s: %v", where, err)
			}
			maxDZ, maxDH := meshMaxDZ, meshMaxDH
			if isPullPosition(l, w) || labels[j] == "boss.position" {
				maxDZ, maxDH = 4, 2.5
			}
			if !ok {
				t.Errorf("%s is not on the navmesh (no walkable polygon within 4 yd)", where)
			} else if dz, dh := math.Abs(float64(n[2]-w.Z)), math.Hypot(float64(n[0]-w.X), float64(n[1]-w.Y)); dz > maxDZ || dh > maxDH {
				t.Errorf("%s: the nearest navmesh floor %.1f %.1f %.1f is %.1f yd above or below it and %.1f yd off on the ground", where, n[0], n[1], n[2], dz, dh)
			}
			checked++
		}
	}
	if checked < 150 {
		t.Fatalf("only %d points checked", checked)
	}
}

// deadminesExemption is a spawn the route test lets stand within 20 yd of the route without a pull group. Settled is empty until the mapping walk
// has decided it (the date of the walk and what it found); TestDeadminesStatusNeedsSettledExemptions refuses a mapped or live-checked status
// while any is open.
type deadminesExemption struct{ Reason, Settled string }

const defiasCompanion = 3450 // summoned with a Pirate (EventAI 65701, Bloodsail Companion 5172): a level 14-15 hostile, no spawn row

// deadminesDoorsVerified is the finding of the 2026-10-15 walk on the two script-less Heavy Doors (spawns 26183 and 26184): shut at the start (state ready,
// flags 32), one click opens each (state active, flags 33), and they stayed open for 50 minutes.
const deadminesDoorsVerified = "2026-10-15 walk (tests/walk-dm): one click opens each, both start shut and stay open"

// Three Defias Overseers stand on a catwalk at z 57.8 over the goblin hall, with no ramp near the route (the plan check of #192 warns "detour" for
// a pack like that: the way to them is longer than twice the line plus 25 yd). They may see the party from above and join a fight. Settle on the
// walk: either they do not aggro (they stay listed and Settled gets the walk's date and the finding) or the plan gets a controlled pull.
var deadminesExemptions = map[string]deadminesExemption{
	"2583051": {Reason: "Defias Overseer on the z 57.8 catwalk over the goblin hall: it PATROLS two points along y -584 (x -92.5 to -64.5); the party's look points on the way to the Harvester come within 13.9 yd of its line in 3-D (the route 0.9 yd on the ground, 6.6 yd in 3-D at the pull stand -82 -583 51), the aggro radius 18 yd (level 18 against a level-20 party) and 2 yd are 20 yd; the packs harvester:3, :4 and the Harvester stand under or beside the line; no mesh link to the floor; live aggro test pending"},
	"2583058": {Reason: "Defias Overseer on the z 57.8 catwalk, static at -107.6 -556.7: the look points of harvester:3 to :5 are 14.3 to 19.4 yd from it in 3-D (aggro radius 18 and 2 yd: 20); a clear look point exists within the view of each hall pack but the plan has no field that places a look point (it is the standoff of the path), so each would need a leg of its own; no mesh link to the floor; live aggro test pending"},
	"2583061": {Reason: "Defias Overseer on the z 57.8 catwalk, static at -68.4 -556.3: the look of harvester:7 (the pack at -63.8 -559.3, 4.7 yd from it) is 18.3 yd away in 3-D and no floor point within the 21 yd view of that pack is 20 yd from it and from the other two (0 of 108 points, probed 2026-10-16); the Harvester himself stands 15.3 yd from it in 3-D; no mesh link to the floor; live aggro test pending"},
}

// A plan that says mapped or live-checked has had its route corrected by a mapping walk (README "Dungeon plans"): while an exemption is unsettled,
// a patrol's period is still the 2.5 yd/s estimate, or one of the two clicked Heavy Doors is unverified, the plan must stay a draft, so that
// dungeon_run refuses it without allow_draft.
func TestDeadminesStatusNeedsSettledExemptions(t *testing.T) {
	p := loadPlan(t, deadminesPlan)
	if p.Status == DraftStatus {
		return
	}
	for guid, e := range deadminesExemptions {
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
	if deadminesDoorsVerified == "" {
		t.Errorf("status %q: the clicked Heavy Doors (spawns 26183 and 26184) have no finding recorded", p.Status)
	}
}

// The patrol periods were timed on the 2026-10-15 walk (tests/walk-dm/patrol_period.json, 0.6 s polls): each carries the measured round and no
// period_estimate; the catwalk Overseer 2583051 was out of range of every sampling spot and keeps its calculated 22 s as an estimate.
func TestDeadminesPatrolPeriodsAreMeasured(t *testing.T) {
	p := loadPlan(t, deadminesPlan)
	measured := map[string]float32{"79144": 107.5, "79138": 33, "2583011": 58, "79188": 81, "79189": 37.5, "79147": 66.5, "79152": 49.5, "79374": 86.5,
		"79171": 112.5, "79170": 70, "79207": 47.5, "79229": 57.5, "79246": 69.5, "79233": 29, "79284": 130.5, "2582923": 62, "2583073": 16.5, "79213": 85,
		"79214": 65, "79260": 52.5, "79289": 25, "79290": 25, "79302": 68, "79313": 71.5, "79310": 58, "79322": 80}
	spawn := regexp.MustCompile(`creature_movement of spawn (\d+) `)
	seen := map[string]bool{}
	for _, l := range p.Legs {
		for _, g := range l.PullGroups {
			if g.Patrol == nil {
				continue
			}
			m := spawn.FindStringSubmatch(g.Patrol.Source)
			if m == nil {
				t.Errorf("%s: patrol of %v: the source names no spawn", l.Name, g.Patrol.Entries)
				continue
			}
			if m[1] == "2583051" {
				if !g.Patrol.PeriodEstimate {
					t.Errorf("%s: the catwalk Overseer's patrol keeps period_estimate: true while it is untimed", l.Name)
				}
				continue
			}
			want, ok := measured[m[1]]
			if !ok {
				t.Errorf("%s: patrol of spawn %s has no measured period", l.Name, m[1])
				continue
			}
			seen[m[1]] = true
			if g.Patrol.PeriodEstimate || g.Patrol.PeriodS != want {
				t.Errorf("%s: patrol of spawn %s: period_s %g estimate %v, measured %g", l.Name, m[1], g.Patrol.PeriodS, g.Patrol.PeriodEstimate, want)
			}
		}
	}
	if len(seen) != len(measured) {
		t.Errorf("%d measured patrols carried, want %d", len(seen), len(measured))
	}
}

// dmCutTail is the path without its last `tail` yards: the walk of a party that stops where it looks at a pack (StandoffPoint), not at the pack.
func dmCutTail(path []Point, tail float64) []Point {
	total := 0.0
	for i := 1; i < len(path); i++ {
		total += dist3(path[i-1], path[i])
	}
	keep := total - tail
	if keep <= 0 {
		return path[:1]
	}
	out := []Point{path[0]}
	for i := 1; i < len(path); i++ {
		seg := dist3(path[i-1], path[i])
		if seg >= keep {
			f := float32(keep / seg)
			a, b := path[i-1], path[i]
			return append(out, Point{a.X + f*(b.X-a.X), a.Y + f*(b.Y-a.Y), a.Z + f*(b.Z-a.Z)})
		}
		keep -= seg
		out = append(out, path[i])
	}
	return out
}

// No hostile spawn of the export stands near the route without being in the plan: every mob within routeAggro yd of the walk of the lead bot
// (from the entrance: the `via` points, `to` and the step stands in full; each pack and each boss only up to LookStandoff yd short of the first
// position, where the lead bot stops to look at it and pulls) and on its floor (within 9 yd up or down) is a pull group member, a boss or its
// formation, a boss.first escort, or an entry the leg skips. A spawn of another leg counts: the doors keep the legs apart.
func TestDeadminesRouteHasNoUnplannedPack(t *testing.T) {
	dir := mapMmaps(36)
	if dir == "" {
		t.Skip("no map 36 navmesh files: set TURTLE_AGENT_MMAPS to a folder holding 036.mmap and the 036*.mmtile tiles")
	}
	const routeAggro = 20
	p := loadPlan(t, deadminesPlan)
	pl := navmesh.NewPlanner(dir)
	defer pl.Close()
	cs := dmTSV(t, "creatures.tsv")
	from := Point{p.Entrance.X, p.Entrance.Y, p.Entrance.Z}
	flagged := map[string]bool{}
	for i, l := range p.Legs {
		walk := func(to Point, look bool) {
			path, err := pl.Plan(p.Map, [3]float32{from.X, from.Y, from.Z}, [3]float32{to.X, to.Y, to.Z})
			if errors.Is(err, navmesh.ErrNoEngine) {
				t.Skip("built without cgo: no navmesh engine")
			}
			if err != nil {
				t.Fatal(err)
			}
			pts := make([]Point, len(path.Points))
			for k, q := range path.Points {
				pts[k] = Point{q[0], q[1], q[2]}
			}
			if len(pts) == 0 {
				return
			}
			if look {
				pts = dmCutTail(pts, LookStandoff)
			}
			from = pts[len(pts)-1]
			for k := 1; k < len(pts); k++ {
				a, b := pts[k-1], pts[k]
				for _, m := range cs {
					if m["faction"] == "35" || m["faction"] == "31" || m["entry"] == "3586" {
						continue // friendly, a critter (rats), the rare Miner Johnson
					}
					e64, _ := strconv.ParseUint(m["entry"], 10, 32)
					at := Point{float32(dmFloat(m, "x")), float32(dmFloat(m, "y")), float32(dmFloat(m, "z"))}
					entry := uint32(e64)
					if flagged[m["guid"]] || deadminesExemptions[m["guid"]].Reason != "" || distToSegment(at, a, b) > routeAggro || math.Abs(float64(at.Z-a.Z)) > 9 || deadminesPlanned(p, l, entry, at, m["guid"]) {
						continue
					}
					flagged[m["guid"]] = true
					t.Errorf("legs[%d] (%s): %s %d (spawn row %s) at %.1f %.1f %.1f stands %.1f yd from the route and is in no pull group", i, l.Name, m["name"], entry, m["guid"], at.X, at.Y, at.Z, distToSegment(at, a, b))
				}
			}
		}
		for _, w := range l.Via {
			walk(w.Point, false)
		}
		walk(l.To, false)
		for _, g := range l.PullGroups {
			walk(g.Positions[0], true)
		}
		for _, s := range l.Steps {
			if s.UseObject != nil && s.UseObject.Stand != nil {
				walk(*s.UseObject.Stand, false)
			}
			if s.LootObject != nil && s.LootObject.Stand != nil {
				walk(*s.LootObject.Stand, false)
			}
		}
		if l.Boss != nil {
			walk(l.Boss.Position, true)
		}
		for _, s := range l.AfterBoss {
			if s.LootObject != nil && s.LootObject.Stand != nil {
				walk(*s.LootObject.Stand, false)
			}
		}
	}
}
