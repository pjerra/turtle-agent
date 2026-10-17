package dungeon

import (
	"bytes"
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
	zulfarrakPlan = "../../dungeons/zulfarrak.json"
	zulfarrakData = "../../tools/dungeongen/testdata/zulfarrak" // the world database export the plans were written from
)

func loadPlan(t *testing.T, path string) *Plan {
	t.Helper()
	p, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	return p
}

// zfStepLegs are the legs of the plan that have steps: the Zum'rah leg and the stairs event's.
var zfStepLegs = map[string]bool{"zumrah": true, "stairs": true, "stairs_top": true, "bly": true, "weegli_door": true}

// zfLeg is the leg of the plan with that name.
func zfLeg(t *testing.T, p *Plan, name string) Leg {
	t.Helper()
	for _, l := range p.Legs {
		if l.Name == name {
			return l
		}
	}
	t.Fatalf("the plan has no leg %q", name)
	return Leg{}
}

func zfTSV(t *testing.T, name string) []map[string]string {
	t.Helper()
	f, err := os.Open(filepath.Join(zulfarrakData, name))
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

func TestZulfarrakPlansLoad(t *testing.T) {
	p := loadPlan(t, zulfarrakPlan)
	if p.Map != 209 || p.Name != "zulfarrak" || len(p.Legs) != 18 {
		t.Fatalf("%+v", p)
	}
	var names []string
	var bosses []uint32
	for _, l := range p.Legs {
		names = append(names, l.Name)
		if l.Boss != nil {
			bosses = append(bosses, l.Boss.Entry)
		}
		if (len(l.Steps) != 0 && !zfStepLegs[l.Name] && l.Name != "gahzrilla") || (len(l.AfterBoss) != 0 && l.Name != "stairs_base") {
			t.Errorf("the plan has steps only in the Zum'rah leg and the stairs event's legs and the gong: %s", l.Name)
		}
		for _, g := range l.PullGroups {
			if g.Team != "" || g.Ambush {
				t.Errorf("%s: unexpected gate on %v", l.Name, g.Entries)
			}
		}
	}
	if fmt.Sprint(names) != "[antusul_a antusul_a2 antusul_a3 antusul antusul_c antusul_b theka velratha velratha_b gahzrilla courtyard zumrah stairs stairs_top stairs_base weegli_door bly ukorz]" || fmt.Sprint(bosses) != "[8127 7272 7795 7273 7271 7275 7604 7267]" {
		t.Fatalf("%v %v", names, bosses)
	}
	// The road to Antu'sul is split where the agent's navmesh leaves the road (ZF-1 and ZF-2 hires, 2026-10-14): waypoints, and the
	// Shadowhunters' patrol on the two groups by its loop.
	var direct, patrols int
	for _, l := range p.Legs[:6] {
		for _, w := range l.Via {
			if w.Direct {
				direct++
			}
		}
		for _, g := range l.PullGroups {
			if g.Patrol != nil {
				patrols++
				if len(g.Patrol.Path) != 45 || g.Patrol.PeriodS != 126 || fmt.Sprint(g.Patrol.Entries) != "[7246]" {
					t.Errorf("%+v", g.Patrol)
				}
			}
		}
	}
	if direct != 0 || patrols != 2 { // the plan check (#187) turned the seven direct hops into plain waypoints: the navmesh joins them
		t.Errorf("direct hops %d, patrols %d", direct, patrols)
	}
	// The hired party brings a mage for the big packs (owner decision 2026-10-14). Its Polymorph goes on a Sandfury Shadowhunter in any
	// pack (Hex on the tank; owner rule), then Witch Doctors, then Shadowcasters.
	if fmt.Sprint(p.HireComposition) != "[warrior:tank priest:healer mage:dps hunter:dps warlock:dps]" || fmt.Sprint(p.CCPriority) != "[7246 5648]" || fmt.Sprint(p.CCAlways) != "[7246]" || fmt.Sprint(p.KillFirst) != "[7785 8179 5650 7247]" || fmt.Sprint(p.SummonedKillFirst) != "[7785 8179]" {
		t.Errorf("hire %v cc_priority %v cc_always %v kill_first %v", p.HireComposition, p.CCPriority, p.CCAlways, p.KillFirst)
	}
	// Entry: no game_tele inside the instance, so the plan names the portal only (area trigger 924 on map 1) and the outside tele.
	if p.EntranceTele != "" || p.OutsideTele != "farrak" || p.Portal == nil || p.Portal.Map != 1 || p.EntranceTrigger != 924 {
		t.Errorf("tele %q outside %q portal %+v trigger %d", p.EntranceTele, p.OutsideTele, p.Portal, p.EntranceTrigger)
	}
	// Zum'rah (faction 35 until area trigger 962) is a boss of the main plan only, behind the area_trigger step; Weegli's crew (7604-7608) is
	// in no pull group of either plan, and only Bly (with Raven, Oro and Murta first) is a boss: the bly leg, after his gossip turns the crew
	// hostile. Weegli (7607) is in no pull group, boss or kill list at all: he has to live to blow the End Door.
	for _, pl := range []*Plan{p} {
		for _, l := range pl.Legs {
			for _, g := range l.PullGroups {
				for _, e := range g.Entries {
					if e == 7271 || (e >= 7604 && e <= 7608) {
						t.Errorf("%s/%s: %d is not a mob to pull", pl.Name, l.Name, e)
					}
				}
			}
			if l.Boss != nil && (l.Boss.Entry == 7607 || slicesContains(l.Boss.First, 7607)) {
				t.Errorf("%s/%s: Weegli (7607) must not be killed: he blows the End Door", pl.Name, l.Name)
			}
			if l.Boss != nil && l.Boss.Entry >= 7604 && l.Boss.Entry <= 7608 && (pl != p || l.Name != "bly" || l.Boss.Entry != 7604 || len(l.Steps) < 2 || l.Steps[1].Gossip == nil || l.Steps[1].Gossip.NPC != 7604) {
				t.Errorf("%s/%s: the crew is a boss only in the bly leg, after Bly's gossip", pl.Name, l.Name)
			}
			if l.Boss != nil && l.Boss.Entry == 7271 && (pl != p || l.Name != "zumrah" || len(l.Steps) == 0 || l.Steps[len(l.Steps)-1].AreaTrigger == nil) {
				t.Errorf("%s/%s: Zum'rah is a boss only behind the area trigger of the zumrah leg", pl.Name, l.Name)
			}
		}
	}
	// The files are kept in the form Marshal writes.
	for _, f := range []string{zulfarrakPlan} {
		pl := loadPlan(t, f)
		raw, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		out, err := Marshal(pl)
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(raw, out) {
			t.Errorf("%s is not in Marshal's form: run `go run ./tools/dungeongen -format %s`", f, f)
		}
	}
}

// The stairs event in order: the cage, the hold at the top, wave 3 at the foot of the stairs (Nekrum first, then Sezz'ziz), then Weegli
// (gossip, gone, the End Door open) BEFORE Bly (gossip, hostile, the crew), then Ukorz with Ruuzlu first.
func TestZulfarrakStairsEventOrder(t *testing.T) {
	p := loadPlan(t, zulfarrakPlan)
	var names []string
	for _, l := range p.Legs[12:] {
		names = append(names, l.Name)
	}
	if fmt.Sprint(names) != "[stairs stairs_top stairs_base weegli_door bly ukorz]" {
		t.Fatalf("%v", names)
	}
	if b := zfLeg(t, p, "stairs_base").Boss; b == nil || b.Entry != 7275 || fmt.Sprint(b.First) != "[7796]" {
		t.Errorf("wave 3: Nekrum first, Sezz'ziz the boss: %+v", b)
	}
	w := zfLeg(t, p, "weegli_door")
	if len(w.Steps) != 4 || w.Steps[1].Gossip == nil || w.Steps[1].Gossip.NPC != 7607 || w.Steps[1].Gossip.By != "tank" ||
		w.Steps[2].WaitFor == nil || w.Steps[2].WaitFor.Unit == nil || w.Steps[2].WaitFor.Unit.State != "gone" ||
		w.Steps[3].WaitFor == nil || w.Steps[3].WaitFor.GameObject == nil || w.Steps[3].WaitFor.GameObject.Entry != 146084 {
		t.Errorf("weegli_door: %+v", w.Steps)
	}
	y := zfLeg(t, p, "bly")
	if y.Steps[1].Gossip == nil || y.Steps[1].Gossip.NPC != 7604 || y.Boss == nil || y.Boss.Entry != 7604 || fmt.Sprint(y.Boss.First) != "[7608 7606 7605]" {
		t.Errorf("bly: %+v %+v", y.Steps, y.Boss)
	}
	if u := zfLeg(t, p, "ukorz").Boss; u == nil || u.Entry != 7267 || fmt.Sprint(u.First) != "[7797]" {
		t.Errorf("ukorz: %+v", u)
	}
}

// Every pack member and boss the plans name is a spawn of the export at that place, and the Troll Cage and the Gong are objects of it.
// What the instance script summons (Gahz'rilla, Nekrum, Sezz'ziz, the waves) has no row and is not looked for.
func TestZulfarrakPlansMatchTheWorldExport(t *testing.T) {
	cs, gos := zfTSV(t, "creatures.tsv"), zfTSV(t, "gameobjects.tsv")
	has := func(rows []map[string]string, entry uint32, at Point) bool {
		for _, m := range rows {
			f := func(k string) float64 { v, _ := strconv.ParseFloat(m[k], 64); return v }
			if m["entry"] == strconv.Itoa(int(entry)) && math.Abs(f("x")-float64(at.X)) < 0.06 && math.Abs(f("y")-float64(at.Y)) < 0.06 && math.Abs(f("z")-float64(at.Z)) < 0.06 {
				return true
			}
		}
		return false
	}
	// What the instance script summons or moves has no row at the plan's place: Gahz'rilla (the Gong), Nekrum and Sezz'ziz (wave 3 of the
	// stairs event) have none at all; Bly and his crew have rows in the cages (1881-1891 1294-1299 z 48) and stand at 1874-1895 1199-1206 z 8.87
	// once the event is over (instance_zulfarrak.cpp, the move after PYRAMID_KILLED_ALL_TROLLS).
	summoned := map[uint32]bool{7273: true, 7275: true, 7604: true}
	scripted := map[uint32]bool{7796: true, 7605: true, 7606: true, 7608: true}
	patrolBosses := map[uint32]bool{7272: true, 7795: true} // Theka walks a 45 yd loop, Velratha the strip by the gong
	for _, f := range []string{zulfarrakPlan} {
		p := loadPlan(t, f)
		for i, l := range p.Legs {
			for j, g := range l.PullGroups {
				if len(g.Entries) != len(g.Positions) {
					t.Errorf("%s legs[%d].pull_groups[%d]: %d entries, %d positions", p.Name, i, j, len(g.Entries), len(g.Positions))
					continue
				}
				for k, at := range g.Positions {
					if !has(cs, g.Entries[k], at) {
						t.Errorf("%s legs[%d].pull_groups[%d]: no spawn of %d at %v", p.Name, i, j, g.Entries[k], at)
					}
				}
			}
			if l.Boss != nil && !summoned[l.Boss.Entry] && !has(cs, l.Boss.Entry, l.Boss.Position) {
				// A patrolling boss is met on its patrol (live walk 2026-10-14): the plan's position may be off the spawn row, within 80 yd of it (a boss fight looks for the entry within 80 yd of the lead bot).
				ok := false
				for _, m := range cs {
					x, _ := strconv.ParseFloat(m["x"], 64)
					y, _ := strconv.ParseFloat(m["y"], 64)
					z, _ := strconv.ParseFloat(m["z"], 64)
					if m["entry"] == strconv.Itoa(int(l.Boss.Entry)) && patrolBosses[l.Boss.Entry] && dist3(l.Boss.Position, Point{float32(x), float32(y), float32(z)}) < 80 {
						ok = true
					}
				}
				if !ok {
					t.Errorf("%s legs[%d].boss: no spawn of %d at %v", p.Name, i, l.Boss.Entry, l.Boss.Position)
				}
			}
			if l.Boss != nil {
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
					if !found && !scripted[e] {
						t.Errorf("%s legs[%d].boss.first: no spawn of %d within 25 yd of the boss", p.Name, i, e)
					}
				}
			}
			for j, s := range l.Steps {
				if r := objRef(s); r != nil && r.Near != nil && !has(gos, r.Entry, *r.Near) {
					t.Errorf("%s legs[%d].steps[%d]: no game object %d at %v", p.Name, i, j, r.Entry, *r.Near)
				}
			}
		}
	}
}

// zulfarrakMmaps finds the navmesh files of map 209 (209.mmap, 209yyxx.mmtile): $TURTLE_AGENT_MMAPS or the test VM's / server host's folder.
func zulfarrakMmaps() string {
	home, _ := os.UserHomeDir()
	for _, d := range []string{os.Getenv("TURTLE_AGENT_MMAPS"), filepath.Join(home, "tb-ci", "mmaps-turtle209"), filepath.Join(home, "tortoise-wow-server", "data", "mmaps")} {
		if d == "" {
			continue
		}
		if _, err := os.Stat(filepath.Join(d, "209.mmap")); err != nil {
			continue
		}
		if tiles, _ := filepath.Glob(filepath.Join(d, "209*.mmtile")); len(tiles) > 0 {
			return d
		}
	}
	return ""
}

// Every waypoint of the plans is on the navmesh (within 2.5 yd: the Sul'lithuz Hatchlings stand on a cliff edge) and reachable from the one before it, and no leg's route passes the End Door (the
// navmesh has no doors, so the shortest Theka -> Velratha path would walk through it; the Velratha leg starts west of the junction to avoid that;
// the ukorz leg, which comes after the door was blown up, is the one that goes through) and none but the legs that have the bots' looting off for the
// whole leg (zumrah, and stairs, which walks out of the graveyard) crosses the graveyard of Shallow Grave chests and traps.
func TestZulfarrakOnNavmesh(t *testing.T) {
	dir := zulfarrakMmaps()
	if dir == "" {
		t.Skip("no map 209 navmesh files: set TURTLE_AGENT_MMAPS to a folder holding 209.mmap and the 209*.mmtile tiles")
	}
	endDoor := Point{1854.5, 1142.9, 16.1}
	pl := navmesh.NewPlanner(dir)
	defer pl.Close()
	for _, f := range []string{zulfarrakPlan} {
		p := loadPlan(t, f)
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
					t.Fatalf("%s legs[%d] waypoint %d: %v", p.Name, i, j, err)
				}
				if path.Status == navmesh.Partial && isPullPosition(l, w) && len(path.Points) > 0 {
					// A pack off the mesh (on a ledge or in a pit) is pulled from where the party can stand: the nearest reachable point within 20 yd is enough.
					e := path.Points[len(path.Points)-1]
					if math.Hypot(float64(e[0]-w.X), float64(e[1]-w.Y)) <= 20 && math.Abs(float64(e[2]-w.Z)) <= 4 {
						from = [3]float32{e[0], e[1], e[2]}
						checked++
						continue
					}
				}
				if path.Status != navmesh.Complete {
					t.Errorf("%s legs[%d] (%s) waypoint %d %v from %v: %s %s", p.Name, i, l.Name, j, w, from, path.Status, path.Reason)
					continue
				}
				end := path.Points[len(path.Points)-1]
				if d := math.Hypot(float64(end[0]-w.X), float64(end[1]-w.Y)); d > 2.5 || math.Abs(float64(end[2]-w.Z)) > 4 {
					t.Errorf("%s legs[%d] waypoint %d: the path ends %.1f yd (z %.1f) from %v", p.Name, i, j, d, end[2]-w.Z, w)
				}
				if p.Name == "zulfarrak" {
					// The route is the straight lines between the corners: sample them every 2 yd.
					for k := 1; k < len(path.Points); k++ {
						a, b := path.Points[k-1], path.Points[k]
						n := int(math.Hypot(float64(b[0]-a[0]), float64(b[1]-a[1]))/2) + 1
						for q := 0; q <= n; q++ {
							f := float32(q) / float32(n)
							x, y := a[0]+(b[0]-a[0])*f, a[1]+(b[1]-a[1])*f
							if l.Name != "ukorz" && math.Hypot(float64(x-endDoor.X), float64(y-endDoor.Y)) < 12 { // the ukorz leg goes through it: the weegli_door leg waited for it to open
								t.Errorf("%s legs[%d] (%s) waypoint %d: the route passes the End Door at (%.0f %.0f)", p.Name, i, l.Name, j, x, y)
								break
							}
							if l.Name != "zumrah" && (l.CompanionsLoot == nil || *l.CompanionsLoot) && x > 1824 && x < 1906 && y > 959 && y < 1062 { // the graveyard of Shallow Grave chests and traps, 8 yd margin (the zumrah leg is the one that goes in: companions_loot false)
								t.Errorf("%s legs[%d] (%s) waypoint %d: the route crosses the graveyard at (%.0f %.0f)", p.Name, i, l.Name, j, x, y)
								break
							}
						}
					}
				}
				from = to
				checked++
			}
		}
		if p.Name == "zulfarrak" && checked < 25 {
			t.Fatalf("only %d waypoints checked", checked)
		}
		t.Logf("%s: %d waypoints on the map 209 navmesh in %s", p.Name, checked, dir)
	}
}

func isPullPosition(l Leg, w Point) bool {
	for _, g := range l.PullGroups {
		for _, q := range g.Positions {
			if q == w {
				return true
			}
		}
	}
	return false
}

// zulfarrakOutsideMmaps finds the navmesh files of map 1 (001.mmap and the Tanaris tiles round the portal).
func zulfarrakOutsideMmaps() string {
	home, _ := os.UserHomeDir()
	for _, d := range []string{os.Getenv("TURTLE_AGENT_MMAPS"), filepath.Join(home, "tb-ci", "mmaps-turtle001"), filepath.Join(home, "tortoise-wow-server", "data", "mmaps")} {
		if d == "" {
			continue
		}
		if _, err := os.Stat(filepath.Join(d, "001.mmap")); err != nil {
			continue
		}
		if tiles, _ := filepath.Glob(filepath.Join(d, "001*.mmtile")); len(tiles) > 0 {
			return d
		}
	}
	return ""
}

// The portal the lead bot is walked to has navmesh floor under it, as TestUldamanPlanPointsOnMesh asks of every Uldaman point. ZF-1 run 1
// (2026-10-14): at z 15.1 (the area trigger's centre, 5 yd above the sand) `cmd move` was refused unreachable 8 times; the bots stood at
// z 9.2-10.5 by the portal, and z 10.0 worked.
func TestZulfarrakPortalOnMesh(t *testing.T) {
	dir := zulfarrakOutsideMmaps()
	if dir == "" {
		t.Skip("no map 1 navmesh files: set TURTLE_AGENT_MMAPS to a folder holding 001.mmap and the Tanaris 001*.mmtile tiles")
	}
	p := loadPlan(t, zulfarrakPlan)
	if p.Portal == nil {
		t.Fatal("no portal")
	}
	pl := navmesh.NewPlanner(dir)
	defer pl.Close()
	w := p.Portal
	n, ok, err := pl.Nearest(w.Map, navmesh.Vec3{w.X, w.Y, w.Z})
	if errors.Is(err, navmesh.ErrNoEngine) {
		t.Skip("built without cgo: no navmesh engine")
	}
	if err != nil {
		t.Fatal(err)
	}
	if !ok {
		t.Fatalf("the portal %v is not on the map %d navmesh (no walkable polygon within 4 yd)", *w, w.Map)
	}
	if dz, dh := math.Abs(float64(n[2]-w.Z)), math.Hypot(float64(n[0]-w.X), float64(n[1]-w.Y)); dz > meshMaxDZ || dh > meshMaxDH {
		t.Errorf("the portal %v: the nearest navmesh floor %.1f %.1f %.1f is %.1f yd above or below it and %.1f yd off on the ground", *w, n[0], n[1], n[2], dz, dh)
	}
}

// Every point of the Zul'Farrak plan has navmesh floor under it (TestUldamanPlanPointsOnMesh's rule), the points of a pack off the mesh
// (on a ledge, TestZulfarrakOnNavmesh) within 4 yd vertically.
func TestZulfarrakPlanPointsOnMesh(t *testing.T) {
	dir := zulfarrakMmaps()
	if dir == "" {
		t.Skip("no map 209 navmesh files: set TURTLE_AGENT_MMAPS to a folder holding 209.mmap and the 209*.mmtile tiles")
	}
	p := loadPlan(t, zulfarrakPlan)
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
			if isPullPosition(l, w) {
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
	if checked < 25 {
		t.Fatalf("only %d points checked", checked)
	}
	t.Logf("%d plan points on the map 209 navmesh", checked)
}

// No hostile spawn of the export stands near the route without being in the plan: every mob within routeAggro yd of the navmesh path the
// lead bot walks (from the entrance through the waypoints of each leg, in order) is a pull group member or a boss of the plan, or is one
// of the entries the leg skips. ZF-1 (2026-10-14) walked into the pack of spawn rows 38009-38011, 12 yd off the road and in no group.
func TestZulfarrakRouteHasNoUnplannedPack(t *testing.T) {
	dir := zulfarrakMmaps()
	if dir == "" {
		t.Skip("no map 209 navmesh files: set TURTLE_AGENT_MMAPS to a folder holding 209.mmap and the 209*.mmtile tiles")
	}
	const routeAggro = 20
	p := loadPlan(t, zulfarrakPlan)
	planned := func(entry uint32, at Point) bool {
		for _, q := range []*Plan{p} {
			for _, l := range q.Legs {
				for _, g := range l.PullGroups {
					for k, pos := range g.Positions {
						if g.Entries[k] == entry && dist3(pos, at) < 1 {
							return true
						}
					}
				}
				if l.Boss != nil && (l.Boss.Entry == entry || slicesContains(l.Boss.First, entry)) {
					return true
				}
			}
		}
		return false
	}
	pl := navmesh.NewPlanner(dir)
	defer pl.Close()
	cs := zfTSV(t, "creatures.tsv")
	from := [3]float32{p.Entrance.X, p.Entrance.Y, p.Entrance.Z}
	flagged := map[string]bool{}
	for i, l := range p.Legs {
		for _, w := range waypoints(l) {
			to := [3]float32{w.X, w.Y, w.Z}
			path, err := pl.Plan(p.Map, from, to)
			if errors.Is(err, navmesh.ErrNoEngine) {
				t.Skip("built without cgo: no navmesh engine")
			}
			if err != nil {
				t.Fatal(err)
			}
			if len(path.Points) > 0 {
				e := path.Points[len(path.Points)-1]
				from = [3]float32{e[0], e[1], e[2]}
			}
			for k := 1; k < len(path.Points); k++ {
				a := Point{path.Points[k-1][0], path.Points[k-1][1], path.Points[k-1][2]}
				b := Point{path.Points[k][0], path.Points[k][1], path.Points[k][2]}
				for _, m := range cs {
					if m["faction"] == "35" {
						continue // friendly
					}
					e64, _ := strconv.ParseUint(m["entry"], 10, 32)
					x, _ := strconv.ParseFloat(m["x"], 64)
					y, _ := strconv.ParseFloat(m["y"], 64)
					z, _ := strconv.ParseFloat(m["z"], 64)
					at := Point{float32(x), float32(y), float32(z)}
					entry := uint32(e64)
					// Two lone Shadowhunters are left out of the plan on purpose (zf1-run6: three pulls drew no fight at 1591 1047.7; no reachable look
					// point at 1626.5 1136): the pulls round them take them in.
					if entry == 7246 && ((math.Abs(x-1591) < 0.2 && math.Abs(y-1047.7) < 0.2) || (math.Abs(x-1626.5) < 0.2 && math.Abs(y-1136) < 0.2)) {
						continue
					}
					// The lone patrolling Soul Eater of the courtyard (spawn row 81550) is left out too (route edit Z10, 2026-10-16): it aggroes on the walk.
					if entry == 7247 && math.Abs(x-1757.16) < 0.2 && math.Abs(y-1141.25) < 0.2 {
						continue
					}
					if flagged[m["guid"]] || slicesContains(l.Skip, entry) || distToSegment(at, a, b) > routeAggro || planned(entry, at) {
						continue
					}
					flagged[m["guid"]] = true
					t.Errorf("legs[%d] (%s): %s %d (spawn row %s) at %.1f %.1f %.1f stands within %d yd of the route and is in no pull group", i, l.Name, m["name"], entry, m["guid"], x, y, z, routeAggro)
				}
			}
		}
	}
}

func slicesContains(s []uint32, v uint32) bool {
	for _, x := range s {
		if x == v {
			return true
		}
	}
	return false
}

// The courtyard leg (PR B): the pull groups of the pyramid courtyard after Velratha. TestZulfarrakPlansMatchTheWorldExport checks every
// member against its spawn row; this pins the groups themselves (the design's list minus group 6 at 1926 1131, 52 yd off the route, and the lone patrolling Soul Eater at 1757 1141, met on the walk: route edits Z6 and Z10 of 2026-10-16) and
// that the patrolling members' groups have the road packs' 30 yd radius.
func TestZulfarrakCourtyardGroups(t *testing.T) {
	p := loadPlan(t, zulfarrakPlan)
	var l Leg
	for _, x := range p.Legs {
		if x.Name == "courtyard" {
			l = x
		}
	}
	if l.Name == "" || p.Legs[len(p.Legs)-8].Name != "courtyard" || p.Legs[len(p.Legs)-9].Name != "gahzrilla" || p.Legs[len(p.Legs)-10].Name != "velratha_b" || p.Legs[len(p.Legs)-7].Name != "zumrah" {
		t.Fatalf("the courtyard leg follows the gahzrilla leg, which follows Velratha's")
	}
	if l.To != (Point{1822, 1177, 9.1}) || l.Boss != nil || len(l.Steps) != 0 || l.CompanionsLoot != nil {
		t.Errorf("%+v", l)
	}
	want := []struct {
		entries string
		at      Point // the first position
		radius  float32
	}{
		{"[7247 5649]", Point{1821.7, 1177, 9}, 30},
		{"[5650]", Point{1863.11, 1199.95, 9}, 15},
		{"[7246]", Point{1878.71, 1159.65, 9.6}, 15},
		{"[5649 5649 5650 5649]", Point{1903.87, 1133.99, 9}, 15},
		{"[7246 8095 7246 5650]", Point{1865.24, 1118.27, 10.9}, 30},
		{"[5650 5650 7246]", Point{1913.5, 1199.53, 9}, 15},
		{"[7247 7246 5649]", Point{1899.24, 1214.72, 9}, 15},
	}
	if len(l.PullGroups) != len(want) {
		t.Fatalf("%d groups", len(l.PullGroups))
	}
	for i, w := range want {
		g := l.PullGroups[i]
		if fmt.Sprint(g.Entries) != w.entries || g.Positions[0] != w.at || g.Radius != w.radius || len(g.Positions) != len(g.Entries) {
			t.Errorf("group %d: %v %v radius %g", i, g.Entries, g.Positions, g.Radius)
		}
	}
	if fmt.Sprint(l.Skip) != "[7269 10080 10082]" {
		t.Errorf("skip %v", l.Skip)
	}
}

// The Zum'rah leg (PR B): the grave steps, then the trigger's zone, the stand inside it, the boss, the kill_first ward and the looting that
// stays off for every bot but the carrier.
func TestZulfarrakZumrahLeg(t *testing.T) {
	p := loadPlan(t, zulfarrakPlan)
	l := zfLeg(t, p, "zumrah")
	if l.Name != "zumrah" || l.To != (Point{1859, 1058, 9.3}) || len(l.Via) != 1 || l.Via[0].Point != (Point{1892.3, 1198.4, 8.9}) || l.Via[0].Direct || len(l.PullGroups) != 0 {
		t.Fatalf("%s to %v via %d groups %d", l.Name, l.To, len(l.Via), len(l.PullGroups))
	}
	if l.CompanionsLoot == nil || *l.CompanionsLoot {
		t.Errorf("companions_loot must be false: only the carrier opens the graves")
	}
	n := len(l.Steps)
	if n != 32 || l.Steps[n-1].AreaTrigger == nil {
		t.Fatalf("%d steps", n)
	}
	for i, s := range l.Steps[:n-1] {
		if s.LootObject == nil {
			t.Fatalf("steps[%d] is no loot_object", i)
		}
	}
	a := l.Steps[n-1].AreaTrigger
	// at_zumrah: area trigger 962, centre 1909.27 1015.11 11.5, radius 10 (zf-probe-events, 2026-10-15).
	if a.ID != 962 || a.Center != (Point{1909.27, 1015.11, 11.5}) || a.Radius != 10 || a.By != TriggerAuto || a.Expect != "unit 7271 hostile" {
		t.Errorf("%+v", a)
	}
	// The stand is inside the zone and clear of the Campfire (2061) that stands on the centre; the boss is within the plan check's 30 yd.
	if d := dist3(a.Stand, a.Center); d > float64(a.Radius) || d < 3 {
		t.Errorf("the stand is %.1f yd from the centre", d)
	}
	if l.Boss == nil || l.Boss.Entry != 7271 || dist3(a.Stand, l.Boss.Position) > TriggerMaxStandToBoss {
		t.Fatalf("%+v", l.Boss)
	}
	// The ward (7785) is killed first.
	if l.Boss.Tactics == nil || fmt.Sprint(l.Boss.Tactics.KillFirst) != "[7785]" {
		t.Errorf("%+v", l.Boss.Tactics)
	}
}

// The grave steps (owner order 2026-10-15): every one is an entry 128403 spawn of the export (the only game object Zum'rah's spell 10731 uses:
// spell_script_target; each grave has the linked trap that summons a Zombie and a Dead Hero), once each, with a stand within the use range; the
// 9 graves of entry 128308 (the spell does not use them) are not among them. Owner: "the boss opens all the graveyards, and if not all are opened
// before, they get pulled while they try to kill the boss. There can be 1 or 2 graveyards left but not more": at most two of the graves he can
// awaken (entry 128403 within 100 yd of him) are left unopened when the area trigger fires; the plan opens all 31, the three nearer ones too
// (he is friendly until the trigger).
func TestZulfarrakGraveSteps(t *testing.T) {
	p := loadPlan(t, zulfarrakPlan)
	l := zfLeg(t, p, "zumrah")
	gos := zfTSV(t, "gameobjects.tsv")
	all := map[string]Point{}
	total := 0
	for _, m := range gos {
		if m["entry"] == "128403" {
			x, _ := strconv.ParseFloat(m["x"], 64)
			y, _ := strconv.ParseFloat(m["y"], 64)
			z, _ := strconv.ParseFloat(m["z"], 64)
			all[m["guid"]] = Point{float32(x), float32(y), float32(z)}
			total++
		}
	}
	zumrah := l.Boss.Position
	far := 0
	for _, at := range all {
		if hdist(at, zumrah) > 30 {
			far++
		}
	}
	seen := map[Point]bool{}
	for i, s := range l.Steps {
		lo := s.LootObject
		if lo == nil {
			continue
		}
		where := fmt.Sprintf("steps[%d]", i)
		if lo.Entry != 128403 || lo.Near == nil || lo.Stand == nil || lo.Range != 2 || lo.ExpectItem != nil {
			t.Errorf("%s: %+v", where, lo)
			continue
		}
		match := false
		for _, at := range all {
			if dist3(at, *lo.Near) < 0.1 {
				match = true
			}
		}
		if !match {
			t.Errorf("%s: no grave of entry 128403 at %v", where, *lo.Near)
		}
		if d := dist3(*lo.Stand, *lo.Near); d > UseRange {
			t.Errorf("%s: the stand is %.1f yd from the grave", where, d)
		}
		// The spawn guid is the export's gameobject.guid of that exact grave.
		spawnOK := false
		for g, at := range all {
			if dist3(at, *lo.Near) < 0.1 && g == strconv.Itoa(int(lo.Spawn)) {
				spawnOK = true
			}
		}
		if !spawnOK {
			t.Errorf("%s: spawn %d is not the guid of the grave at %v", where, lo.Spawn, *lo.Near)
		}
		if seen[*lo.Near] {
			t.Errorf("%s: grave %v twice", where, *lo.Near)
		}
		seen[*lo.Near] = true
	}
	if total != 31 || far != 28 || len(seen) != total {
		t.Errorf("%d graves of 128403 in the export, %d beyond 30 yd, %d steps", total, far, len(seen))
	}
	// The graves the boss can awaken and the plan leaves unopened before the trigger.
	left := 0
	for g, at := range all {
		if hdist(at, zumrah) > 100 {
			continue
		}
		opened := false
		for _, s := range l.Steps {
			if lo := s.LootObject; lo != nil && strconv.Itoa(int(lo.Spawn)) == g {
				opened = true
			}
		}
		if !opened {
			left++
		}
	}
	if left > 2 {
		t.Errorf("%d summon-usable graves are left unopened before the trigger: at most 2 may be", left)
	}
}

// The Sul'Farrak Hatchlings (8130) cannot be attacked by the bots: antusul_b has no pull group of them, and skips the entry.
func TestZulfarrakHatchlingsAreSkippedNotPulled(t *testing.T) {
	for _, l := range loadPlan(t, zulfarrakPlan).Legs {
		for _, g := range l.PullGroups {
			if slicesContains(g.Entries, 8130) {
				t.Errorf("%s pulls the Hatchlings", l.Name)
			}
		}
		if (l.Name == "antusul_b" || l.Name == "theka") && !slicesContains(l.Skip, 8130) { // theka walks back past them
			t.Errorf("%s skip %v", l.Name, l.Skip)
		}
	}
}

// antusul_a2 ends where its first pull is pulled from, not inside the pack: the leg's To is outside the aggro range (about 20 yd; checked at 22)
// of every member of its first group (zf-zum round 1: the healer died at the old To 1310 834.3, 10-18 yd from them, in 2 of 4 runs).
func TestZulfarrakAntusulA2EndsOutsideTheFirstPacksAggro(t *testing.T) {
	for _, l := range loadPlan(t, zulfarrakPlan).Legs {
		if l.Name != "antusul_a2" {
			continue
		}
		for _, at := range l.PullGroups[0].Positions {
			if d := hdist(l.To, at); d < 22 {
				t.Errorf("To %v is %.1f yd from the member at %v", l.To, d, at)
			}
		}
		return
	}
	t.Fatal("no antusul_a2")
}

// The way out of the courtyard to the graveyard never passes the End Door ramp, from any of the courtyard's pack positions (live: the hop at
// 1847.1 1164 on the ramp path was unreachable six times): every start reaches the zumrah leg's first via on a path at least 15 yd from the door.
func TestZulfarrakCourtyardExitsAvoidTheEndDoor(t *testing.T) {
	dir := zulfarrakMmaps()
	if dir == "" {
		t.Skip("no map 209 navmesh files: set TURTLE_AGENT_MMAPS to a folder holding 209.mmap and the 209*.mmtile tiles")
	}
	p := loadPlan(t, zulfarrakPlan)
	var courtyard, zumrah Leg
	for _, l := range p.Legs {
		switch l.Name {
		case "courtyard":
			courtyard = l
		case "zumrah":
			zumrah = l
		}
	}
	pl := navmesh.NewPlanner(dir)
	defer pl.Close()
	door := Point{1854.5, 1142.9, 16.1}
	to := zumrah.Via[0].Point
	starts := []Point{courtyard.To}
	for _, g := range courtyard.PullGroups {
		starts = append(starts, g.Positions...)
	}
	for _, s := range starts {
		path, err := pl.Plan(209, [3]float32{s.X, s.Y, s.Z}, [3]float32{to.X, to.Y, to.Z})
		if errors.Is(err, navmesh.ErrNoEngine) {
			t.Skip("built without cgo: no navmesh engine")
		}
		if err != nil || path.Status != navmesh.Complete {
			t.Errorf("from %v: %v %s", s, err, path.Status)
			continue
		}
		for k := 1; k < len(path.Points); k++ {
			a, b := path.Points[k-1], path.Points[k]
			n := int(math.Hypot(float64(b[0]-a[0]), float64(b[1]-a[1]))/2) + 1
			for q := 0; q <= n; q++ {
				f := float32(q) / float32(n)
				if d := math.Hypot(float64(a[0]+(b[0]-a[0])*f-door.X), float64(a[1]+(b[1]-a[1])*f-door.Y)); d < 15 {
					t.Errorf("from %v: the path passes %.1f yd from the End Door", s, d)
					break
				}
			}
		}
	}
}

// The gahzrilla leg (PR A, TortoiseBots #984): the optional gong leg between Velratha and the courtyard. Pins what the world database says:
// gong 141832 (spawn row 27140, lock 459, event 2488), the Mallet 9240, Gahz'rilla 7273 summoned at the pit and walked out by event 2489 to
// 1702.58 1224.19, and the eight packs on or near that walk (TestZulfarrakPlansMatchTheWorldExport checks every member against its spawn row,
// TestZulfarrakRouteHasNoUnplannedPack that no other mob stands near the route).
func TestZulfarrakGahzrillaLeg(t *testing.T) {
	p := loadPlan(t, zulfarrakPlan)
	var l Leg
	idx := -1
	for i, x := range p.Legs {
		if x.Name == "gahzrilla" {
			l, idx = x, i
		}
	}
	if idx != 9 || p.Legs[idx-1].Name != "velratha_b" || p.Legs[idx+1].Name != "courtyard" {
		t.Fatalf("the leg is at %d", idx)
	}
	if l.Optional == nil || l.Optional.Item != 9240 || l.Optional.Name != "Mallet of Zul'Farrak" {
		t.Fatalf("optional %+v", l.Optional)
	}
	if l.To != (Point{1652.84, 1168.99, 10.67}) {
		t.Errorf("the walk ends at the gong stand, not %v", l.To)
	}
	// The order: the packs of the pool ring, then the Mallet used on the gong (item 9240's spell 11434 opens the gong's lock; a plain use of the
	// gong did nothing live), by a bot that carries it, 3.5 yd from the gong; the wait for him to show; the boss.
	if len(l.Steps) != 2 || l.Steps[0].UseObject == nil || l.Steps[1].WaitFor == nil || len(l.AfterBoss) != 0 {
		t.Fatalf("steps %+v", l.Steps)
	}
	u := l.Steps[0].UseObject
	if u.Entry != 141832 || *u.Near != (Point{1650.9, 1171.9, 10.9}) || *u.Stand != l.To || u.RequiresItem == nil || u.RequiresItem.Entry != 9240 || !u.ViaItem || dist3(*u.Stand, *u.Near) > UseRange {
		t.Errorf("use_object %+v", u)
	}
	w := l.Steps[1].WaitFor
	if w.Unit == nil || fmt.Sprint(w.Unit.Entries) != "[7273]" || w.Unit.State != UnitSeen || w.TimeoutS != 30 || w.FailText == "" {
		t.Errorf("wait_for %+v", w)
	}
	if l.Boss == nil || l.Boss.Entry != 7273 || l.Boss.Position != (Point{1702.58, 1224.19, 8.88}) {
		t.Errorf("boss %+v", l.Boss)
	}
	if l.CompanionsLoot != nil || fmt.Sprint(l.Skip) != "[7269 10080 10082 15578]" {
		t.Errorf("companions_loot %v skip %v", l.CompanionsLoot, l.Skip)
	}
	// The packs, in pull order (the first position of each), and how far each is from the walk event 2489 makes (pit to 1702.58 1224.19).
	want := []struct {
		entries string
		at      Point
		radius  float32
	}{
		{"[5649 5649 5650]", Point{1624.85, 1192.94, 9.09}, 15},
		{"[5650]", Point{1643.59, 1200.11, 9.05}, 15},
		{"[7246 7246 8095]", Point{1652.63, 1210.28, 9.17}, 30},
		{"[5649]", Point{1652.06, 1219.91, 9.67}, 15},
		{"[5649]", Point{1666.97, 1233.6, 9.41}, 15},
		{"[5649]", Point{1699.81, 1233.84, 8.96}, 15},
		{"[5650 7246]", Point{1700.27, 1248.3, 9.8}, 15},
		{"[5649]", Point{1714.38, 1218.58, 8.96}, 15},
	}
	if len(l.PullGroups) != len(want) {
		t.Fatalf("%d groups", len(l.PullGroups))
	}
	walk := []Point{{1664.65, 1187.67, -2.89}, {1672.66, 1195.93, 0.96}, {1681.74, 1204.13, 5.67}, {1686.09, 1207.3, 9.75}, {1690.54, 1211.53, 10.18}, {1692.57, 1213.66, 8.88}, {1702.58, 1224.19, 8.88}}
	for i, g := range l.PullGroups {
		if fmt.Sprint(g.Entries) != want[i].entries || g.Positions[0] != want[i].at || g.Radius != want[i].radius || len(g.Positions) != len(g.Entries) {
			t.Errorf("group %d: %v %v radius %g", i, g.Entries, g.Positions, g.Radius)
		}
		near := 1e9
		for _, at := range g.Positions {
			for k := 1; k < len(walk); k++ {
				near = math.Min(near, distToSegment(at, walk[k-1], walk[k]))
			}
		}
		if near > 45 {
			t.Errorf("group %d (%v) is %.0f yd from his walk: it is not on it", i, g.Entries, near)
		}
	}
}

// The gong and the Mallet are what the server database says: object 141832 at the leg's `near` is in the export, and the optional item is the
// one the step requires.
func TestZulfarrakGongIsInTheExport(t *testing.T) {
	gos := zfTSV(t, "gameobjects.tsv")
	found := false
	for _, m := range gos {
		if m["entry"] == "141832" {
			x, _ := strconv.ParseFloat(m["x"], 64)
			y, _ := strconv.ParseFloat(m["y"], 64)
			z, _ := strconv.ParseFloat(m["z"], 64)
			found = math.Abs(x-1650.91) < 0.06 && math.Abs(y-1171.88) < 0.06 && math.Abs(z-10.901) < 0.06
		}
	}
	if !found {
		t.Error("no gong 141832 at 1650.91 1171.88 10.901 in the export")
	}
}
