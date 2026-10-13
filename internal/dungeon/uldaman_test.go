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
	uldamanPlan = "../../dungeons/uldaman.json"
	uldamanData = "../../tools/dungeongen/testdata/uldaman" // the world database export the plan was written from
)

func loadUldaman(t *testing.T) *Plan {
	t.Helper()
	p, err := Load(uldamanPlan)
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func TestUldamanPlanLoads(t *testing.T) {
	p := loadUldaman(t)
	if p.Map != 70 || p.Name != "uldaman" || len(p.Legs) != 4 {
		t.Fatalf("%+v", p)
	}
	var names []string
	var bosses []uint32
	for _, l := range p.Legs {
		names = append(names, l.Name)
		var b uint32
		if l.Boss != nil {
			b = l.Boss.Entry
		}
		bosses = append(bosses, b)
	}
	if fmt.Sprint(names) != "[baelog revelosh keystone ironaya]" || fmt.Sprint(bosses) != "[6906 6910 0 7228]" {
		t.Fatalf("%v %v", names, bosses)
	}
	// Eric and Olaf are fought before Baelog: no script in the world database makes them die with him.
	if b := p.Legs[0].Boss; fmt.Sprint(b.First) != "[6907 6908]" || len(p.Legs[0].Skip) != 0 {
		t.Fatalf("%+v skip %v", b, p.Legs[0].Skip)
	}
	// The Keystone leg has no boss; the door steps are the Keystone, then the Seal of Khaz'Mul opening.
	k := p.Legs[2]
	if k.Boss != nil || len(k.Steps) != 2 || k.Steps[0].UseObject.Entry != 124371 || k.Steps[0].UseObject.Stand == nil ||
		k.Steps[1].WaitFor.GameObject.Entry != 124372 || k.Steps[1].WaitFor.GameObject.State != "active" {
		t.Fatalf("%+v", k)
	}
	// The file is kept in the form Marshal writes, so regenerating it shows only real changes.
	raw, err := os.ReadFile(uldamanPlan)
	if err != nil {
		t.Fatal(err)
	}
	out, err := Marshal(p)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(raw, out) {
		t.Error("dungeons/uldaman.json is not in Marshal's form: run `go run ./tools/dungeongen -format dungeons/uldaman.json`")
	}
}

func tsv(t *testing.T, name string) []map[string]string {
	t.Helper()
	f, err := os.Open(filepath.Join(uldamanData, name))
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

func near(m map[string]string, p Point) bool {
	f := func(k string) float64 { v, _ := strconv.ParseFloat(m[k], 64); return v }
	return math.Abs(f("x")-float64(p.X)) < 0.06 && math.Abs(f("y")-float64(p.Y)) < 0.06 && math.Abs(f("z")-float64(p.Z)) < 0.06
}

// Every spawn the plan names is a spawn in the world database export, at that place; the plan invents none.
func TestUldamanPlanMatchesTheWorldExport(t *testing.T) {
	p := loadUldaman(t)
	cs, gos := tsv(t, "creatures.tsv"), tsv(t, "gameobjects.tsv")
	hasCreature := func(entry uint32, at Point) bool {
		for _, m := range cs {
			if m["entry"] == strconv.Itoa(int(entry)) && near(m, at) {
				return true
			}
		}
		return false
	}
	hasObject := func(entry uint32, at Point) bool {
		for _, m := range gos {
			if m["entry"] == strconv.Itoa(int(entry)) && near(m, at) {
				return true
			}
		}
		return false
	}
	for i, l := range p.Legs {
		for j, g := range l.PullGroups {
			for k, at := range g.Positions {
				if !hasCreature(g.Entries[k], at) {
					t.Errorf("legs[%d].pull_groups[%d]: no spawn of %d at %v", i, j, g.Entries[k], at)
				}
			}
		}
		if l.Boss != nil && !hasCreature(l.Boss.Entry, l.Boss.Position) {
			t.Errorf("legs[%d].boss: no spawn of %d at %v", i, l.Boss.Entry, l.Boss.Position)
		}
		if l.Boss != nil {
			for _, e := range l.Boss.First { // killed before the boss: they stand within the encounter's reach of it
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
		for j, s := range l.Steps {
			for _, r := range []*ObjectRef{objRef(s)} {
				if r != nil && r.Near != nil && !hasObject(r.Entry, *r.Near) {
					t.Errorf("legs[%d].steps[%d]: no game object %d at %v", i, j, r.Entry, *r.Near)
				}
			}
		}
	}
}

func objRef(s Step) *ObjectRef {
	switch {
	case s.UseObject != nil:
		return &s.UseObject.ObjectRef
	case s.WaitFor != nil && s.WaitFor.GameObject != nil:
		return &s.WaitFor.GameObject.ObjectRef
	}
	return nil
}

// mmapsDir finds the server's navmesh files of map 70 (070.mmap and the 070yyxx.mmtile tiles): $TURTLE_AGENT_MMAPS,
// or one of the folders where the test VM and the server host keep them. "" when there are none.
func mmapsDir() string {
	home, _ := os.UserHomeDir()
	for _, d := range []string{os.Getenv("TURTLE_AGENT_MMAPS"), filepath.Join(home, "tb-ci", "mmaps-turtle070"), filepath.Join(home, "tortoise-wow-server", "data", "mmaps")} {
		if d == "" {
			continue
		}
		if _, err := os.Stat(filepath.Join(d, "070.mmap")); err != nil {
			continue
		}
		if tiles, _ := filepath.Glob(filepath.Join(d, "070*.mmtile")); len(tiles) > 0 {
			return d
		}
	}
	return ""
}

// waypoints lists the places a leg sends the player to, in the order the driver goes: the leg's To, each pack member
// (the approach to the pull), the stand points of the steps, the boss.
func waypoints(l Leg) []Point {
	pts := []Point{l.To}
	for _, g := range l.PullGroups {
		pts = append(pts, g.Positions...)
	}
	for _, s := range l.Steps {
		if s.UseObject != nil && s.UseObject.Stand != nil {
			pts = append(pts, *s.UseObject.Stand)
		}
	}
	if l.Boss != nil {
		pts = append(pts, l.Boss.Position)
	}
	return pts
}

// Every waypoint of the Uldaman plan is on the server's navmesh and reachable from the one before it, walking the way
// the driver will (path_to / walk_path). Runs only where the map 70 mmaps are (see mmapsDir); needs the cgo build.
func TestUldamanOnNavmesh(t *testing.T) {
	dir := mmapsDir()
	if dir == "" {
		t.Skip("no map 70 navmesh files: set TURTLE_AGENT_MMAPS to a folder holding 070.mmap and the 070*.mmtile tiles")
	}
	p := loadUldaman(t)
	pl := navmesh.NewPlanner(dir)
	defer pl.Close()
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
			if path.Status != navmesh.Complete {
				t.Errorf("legs[%d] (%s) waypoint %d %v from %v: %s %s", i, l.Name, j, w, from, path.Status, path.Reason)
				continue
			}
			end := path.Points[len(path.Points)-1]
			if d := math.Hypot(float64(end[0]-w.X), float64(end[1]-w.Y)); d > 1 || math.Abs(float64(end[2]-w.Z)) > 4 {
				t.Errorf("legs[%d] waypoint %d: the path ends %.1f yd (z %.1f) from %v", i, j, d, end[2]-w.Z, w)
			}
			from = to
			checked++
		}
	}
	if checked < 30 {
		t.Fatalf("only %d waypoints checked", checked)
	}
	t.Logf("%d waypoints on the map 70 navmesh in %s", checked, dir)
}
