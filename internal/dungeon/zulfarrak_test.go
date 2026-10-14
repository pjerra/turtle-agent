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
	zulfarrakPlan  = "../../dungeons/zulfarrak.json"
	zulfarrakExtra = "../../dungeons/zulfarrak-extra.json"
	zulfarrakData  = "../../tools/dungeongen/testdata/zulfarrak" // the world database export the plans were written from
)

func loadPlan(t *testing.T, path string) *Plan {
	t.Helper()
	p, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	return p
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
	if p.Map != 209 || p.Name != "zulfarrak" || len(p.Legs) != 3 {
		t.Fatalf("%+v", p)
	}
	var names []string
	var bosses []uint32
	for _, l := range p.Legs {
		names = append(names, l.Name)
		bosses = append(bosses, l.Boss.Entry)
		if len(l.Steps) != 0 || len(l.AfterBoss) != 0 {
			t.Errorf("the first-run plan has no steps: %s", l.Name)
		}
		for _, g := range l.PullGroups {
			if g.Team != "" || g.Ambush {
				t.Errorf("%s: unexpected gate on %v", l.Name, g.Entries)
			}
		}
	}
	if fmt.Sprint(names) != "[antusul theka velratha]" || fmt.Sprint(bosses) != "[8127 7272 7795]" {
		t.Fatalf("%v %v", names, bosses)
	}
	// Entry: no game_tele inside the instance, so the plan names the portal only (area trigger 924 on map 1) and the outside tele.
	if p.EntranceTele != "" || p.OutsideTele != "farrak" || p.Portal == nil || p.Portal.Map != 1 || p.EntranceTrigger != 924 {
		t.Errorf("tele %q outside %q portal %+v trigger %d", p.EntranceTele, p.OutsideTele, p.Portal, p.EntranceTrigger)
	}
	// Zum'rah (faction 35 until area trigger 962) and Weegli's crew are in no pull group, boss or step of either plan.
	x := loadPlan(t, zulfarrakExtra)
	if len(x.Legs) != 3 || x.Legs[0].Name != "gong" || x.Legs[1].Name != "stairs" || x.Legs[2].Name != "ukorz" {
		t.Fatalf("%+v", x.Legs)
	}
	for _, pl := range []*Plan{p, x} {
		for _, l := range pl.Legs {
			for _, g := range l.PullGroups {
				for _, e := range g.Entries {
					if e == 7271 || (e >= 7604 && e <= 7608) {
						t.Errorf("%s/%s: %d is not a mob to pull", pl.Name, l.Name, e)
					}
				}
			}
			if l.Boss != nil && l.Boss.Entry == 7271 {
				t.Errorf("%s/%s: Zum'rah is no boss until the area trigger is supported", pl.Name, l.Name)
			}
		}
	}
	// The gong needs the Mallet, which an honest run does not have: it is only in the extra plan.
	if us := x.Legs[0].Steps[0].UseObject; us == nil || us.Entry != 141832 || us.RequiresItem == nil || us.RequiresItem.Entry != 9240 {
		t.Errorf("%+v", x.Legs[0].Steps)
	}
	// The files are kept in the form Marshal writes.
	for _, f := range []string{zulfarrakPlan, zulfarrakExtra} {
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
	summoned := map[uint32]bool{7273: true}
	patrolBosses := map[uint32]bool{7272: true, 7795: true} // Theka walks a 45 yd loop, Velratha the strip by the gong
	for _, f := range []string{zulfarrakPlan, zulfarrakExtra} {
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
					if !found {
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
// navmesh has no doors, so the shortest Theka -> Velratha path would walk through it; the Velratha leg starts west of the junction to avoid that) and
// none crosses the graveyard of Shallow Grave chests and traps.
func TestZulfarrakOnNavmesh(t *testing.T) {
	dir := zulfarrakMmaps()
	if dir == "" {
		t.Skip("no map 209 navmesh files: set TURTLE_AGENT_MMAPS to a folder holding 209.mmap and the 209*.mmtile tiles")
	}
	endDoor := Point{1854.5, 1142.9, 16.1}
	pl := navmesh.NewPlanner(dir)
	defer pl.Close()
	for _, f := range []string{zulfarrakPlan, zulfarrakExtra} {
		p := loadPlan(t, f)
		from := [3]float32{p.Entrance.X, p.Entrance.Y, p.Entrance.Z}
		checked := 0
		for i, l := range p.Legs {
			for j, w := range waypoints(l) {
				if p.Name == "zulfarrak-extra" && i > 0 && j == 0 {
					from = [3]float32{w.X, w.Y, w.Z} // the extra legs are separate starts, not one walk
				}
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
							if math.Hypot(float64(x-endDoor.X), float64(y-endDoor.Y)) < 12 {
								t.Errorf("%s legs[%d] (%s) waypoint %d: the route passes the End Door at (%.0f %.0f)", p.Name, i, l.Name, j, x, y)
								break
							}
							if x > 1824 && x < 1906 && y > 959 && y < 1062 { // the graveyard of Shallow Grave chests and traps, 8 yd margin
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
	extra := loadPlan(t, zulfarrakExtra)
	planned := func(entry uint32, at Point) bool {
		for _, q := range []*Plan{p, extra} {
			for _, l := range q.Legs {
				for _, g := range l.PullGroups {
					for k, pos := range g.Positions {
						if g.Entries[k] == entry && dist3(pos, at) < 1 {
							return true
						}
					}
				}
				if l.Boss != nil && l.Boss.Entry == entry {
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
