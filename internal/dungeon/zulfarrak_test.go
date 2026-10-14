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
				t.Errorf("%s legs[%d].boss: no spawn of %d at %v", p.Name, i, l.Boss.Entry, l.Boss.Position)
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
// navmesh has no doors, so the shortest Theka -> Velratha path would walk through it; the Velratha leg starts west of the junction to avoid that).
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
				if path.Status != navmesh.Complete {
					t.Errorf("%s legs[%d] (%s) waypoint %d %v from %v: %s %s", p.Name, i, l.Name, j, w, from, path.Status, path.Reason)
					continue
				}
				end := path.Points[len(path.Points)-1]
				if d := math.Hypot(float64(end[0]-w.X), float64(end[1]-w.Y)); d > 2.5 || math.Abs(float64(end[2]-w.Z)) > 4 {
					t.Errorf("%s legs[%d] waypoint %d: the path ends %.1f yd (z %.1f) from %v", p.Name, i, j, d, end[2]-w.Z, w)
				}
				if p.Name == "zulfarrak" {
					for _, c := range path.Points {
						if math.Hypot(float64(c[0]-endDoor.X), float64(c[1]-endDoor.Y)) < 12 {
							t.Errorf("%s legs[%d] (%s) waypoint %d: the route passes the End Door at %v", p.Name, i, l.Name, j, c)
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
