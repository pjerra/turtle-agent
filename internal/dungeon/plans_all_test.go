package dungeon

import (
	"bytes"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/pjerra/turtle-agent/internal/navmesh"
)

const plansDir = "../../dungeons"

func parseT(t *testing.T, doc string) (*Plan, error) {
	t.Helper()
	return Parse("t.json", []byte(doc))
}

const planHead = `{"version":1,"name":"t",%s"map":1,"entrance":{"x":0,"y":0,"z":0},%s"legs":[{"to":{"x":0,"y":0,"z":0},"pull_groups":[{"entries":[1],"positions":[{"x":0,"y":0,"z":0}]}]}]}`

func TestPlanKindStatusAndParty(t *testing.T) {
	doc := func(pre, post string) string { return fmt.Sprintf(planHead, pre, post) }
	p, err := parseT(t, doc(`"kind":"dungeon","status":"draft (not mapped, not live-checked)",`, `"hire_composition":["warrior:tank","priest:healer","mage:dps"],`))
	if err != nil {
		t.Fatal(err)
	}
	if p.Kind != KindDungeon || p.IsRaid() || p.Status != DraftStatus || len(p.HireComposition) != 3 {
		t.Fatalf("%+v", p)
	}
	r, err := parseT(t, doc(`"kind":"raid","status":"live-checked",`, `"raid":{"size":40,"tanks":4,"healers":10,"dps":26},`))
	if err != nil || !r.IsRaid() || r.Raid.Size != 40 || r.Status != "live-checked" {
		t.Fatalf("%v %+v", err, r)
	}
	for _, c := range []struct{ pre, post, want string }{
		{`"kind":"zoo",`, ``, `kind: "zoo"`},
		{`"status":"nearly done",`, ``, `status: "nearly done": start with draft, mapped or live-checked`},
		{``, `"hire_composition":["warrior:tank"],`, "hire_composition"},
		{``, `"hire_composition":["warrior:tank","priest:healer","mage:healer"],`, "hire_composition"},
		{`"kind":"raid",`, ``, "raid: required for a raid plan"},
		{``, `"raid":{"size":40,"tanks":4,"healers":10,"dps":26},`, "raid: only a plan of kind"},
		{`"kind":"raid",`, `"raid":{"size":25,"tanks":4,"healers":6,"dps":15},`, "raid.size: 25: use 10, 20 or 40"},
		{`"kind":"raid",`, `"raid":{"size":40,"tanks":4,"healers":10,"dps":20},`, "is 34, the size is 40"},
		{`"kind":"raid",`, `"raid":{"size":10,"tanks":0,"healers":3,"dps":7},`, "at least one tank"},
		{`"kind":"raid",`, `"hire_composition":["warrior:tank","priest:healer"],"raid":{"size":10,"tanks":2,"healers":3,"dps":5},`, "a raid plan has a raid block instead"},
	} {
		if _, err := parseT(t, doc(c.pre, c.post)); err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s %s: got %v, want %q", c.pre, c.post, err, c.want)
		}
	}
}

// offlineMapped are the plans whose legs, doors and steps were written by hand offline (no live walk yet), so they carry steps and are still
// drafts. A plan leaves the list when its status moves on: TestOfflineMappedPlansAreStillDrafts fails for one that does not.
var offlineMapped = map[string]bool{"deadmines": true, "wailing_caverns-extra": true}

func TestOfflineMappedPlansAreStillDrafts(t *testing.T) {
	for name := range offlineMapped {
		p, err := Load(filepath.Join(plansDir, name+".json"))
		if err != nil {
			t.Fatal(err)
		}
		if !strings.HasPrefix(p.Status, StatusDraft) {
			t.Errorf("%s is %q now: take it off offlineMapped (and drop the exemptions that are tied to it)", name, p.Status)
		}
	}
}

// Every plan file loads, says how far it has been checked, and is kept in the form Marshal writes. A raid plan has its group; a draft is a
// draft all the way (no hand-written steps in it, which would make "not mapped" a lie only in part).
func TestEveryPlanFileLoads(t *testing.T) {
	files, err := filepath.Glob(filepath.Join(plansDir, "*.json"))
	if err != nil || len(files) < 40 {
		t.Fatalf("%d plan files: %v", len(files), err)
	}
	drafts, raids := 0, 0
	for _, f := range files {
		p, err := Load(f)
		if err != nil {
			t.Errorf("%v", err)
			continue
		}
		if p.Status == "" {
			t.Errorf("%s: no status", f)
		}
		raw, _ := os.ReadFile(f)
		if out, err := Marshal(p); err != nil || !bytes.Equal(raw, out) {
			t.Errorf("%s is not in Marshal's form: run `go run ./tools/dungeongen -format %s`", f, f)
		}
		if p.IsRaid() {
			raids++
			if p.Raid == nil {
				t.Errorf("%s: raid plan without a raid block", f)
			}
		}
		if strings.HasPrefix(p.Status, StatusDraft) {
			drafts++
			for i, l := range p.Legs {
				if offlineMapped[p.Name] {
					continue // written by hand from the export, the scripts and the navmesh; still a draft until the mapping walk
				}
				if len(l.Steps) != 0 || len(l.AfterBoss) != 0 {
					t.Errorf("%s legs[%d]: a draft has no steps; they come with the mapping walk", f, i)
				}
			}
		}
	}
	if drafts < 40 || raids < 10 {
		t.Errorf("%d drafts, %d raids", drafts, raids)
	}
}

// mapMmaps finds the navmesh files of an instance map (<map>.mmap and <map>*.mmtile, three digits): $TURTLE_AGENT_MMAPS, the VM's per-map folder
// or the server host's folder.
func mapMmaps(m uint32) string {
	home, _ := os.UserHomeDir()
	n := fmt.Sprintf("%03d", m)
	for _, d := range []string{os.Getenv("TURTLE_AGENT_MMAPS"), filepath.Join(home, "tb-ci", "mmaps-turtle"+n), filepath.Join(home, "tortoise-wow-server", "data", "mmaps")} {
		if d == "" {
			continue
		}
		if _, err := os.Stat(filepath.Join(d, n+".mmap")); err != nil {
			continue
		}
		if tiles, _ := filepath.Glob(filepath.Join(d, n+"*.mmtile")); len(tiles) > 0 {
			return d
		}
	}
	return ""
}

// Every plan's walk points against the navmesh of their map, where the machine has it: each must have mesh floor under it (meshMaxDZ up or down,
// meshMaxDH sideways; a pull position, a creature's own, gets 4 and 2.5). A point the generator could not snap to the mesh (tools/dungeongen
// snaps.json) carries needs_walk and is left out here, and counted in the log. The maps without navmesh files are listed, so a run says what
// it did not check.
func TestPlansOnNavmeshWhereTheMachineHasIt(t *testing.T) {
	files, _ := filepath.Glob(filepath.Join(plansDir, "*.json"))
	var missing []string
	seen := map[uint32]bool{}
	checkedAny := false
	for _, f := range files {
		p, err := Load(f)
		if err != nil {
			t.Fatal(err)
		}
		if p.Name == "uldaman" || strings.HasPrefix(p.Name, "zulfarrak") {
			continue
		}
		dir := mapMmaps(p.Map)
		if dir == "" {
			if !seen[p.Map] {
				missing = append(missing, fmt.Sprint(p.Map))
				seen[p.Map] = true
			}
			continue
		}
		pl := navmesh.NewPlanner(dir)
		on, total, walk := 0, 0, 0
		for i, l := range p.Legs {
			pts, labels, _ := planPoints(l)
			flagged := needsWalkPoints(l)
			for j, w := range pts {
				if flagged[w] {
					walk++
					continue
				}
				n, ok, err := pl.Nearest(p.Map, navmesh.Vec3{w.X, w.Y, w.Z})
				if errors.Is(err, navmesh.ErrNoEngine) {
					pl.Close()
					t.Skip("built without cgo: no navmesh engine")
				}
				if err != nil {
					pl.Close()
					t.Fatalf("%s: %v", p.Name, err)
				}
				total++
				maxDZ, maxDH := meshMaxDZ, meshMaxDH
				if isPullPosition(l, w) {
					maxDZ, maxDH = 4, 2.5
				}
				if ok && math.Abs(float64(n[2]-w.Z)) <= maxDZ && math.Hypot(float64(n[0]-w.X), float64(n[1]-w.Y)) <= maxDH {
					on++
				} else {
					t.Errorf("%s legs[%d] (%s) %s %v is not on the navmesh: run `go run ./tools/dungeongen -spec tools/dungeongen/plans.json -snap-update ~/tb-ci` and regenerate, or mark it needs_walk", p.Name, i, l.Name, labels[j], w)
				}
			}
		}
		pl.Close()
		checkedAny = true
		t.Logf("%s (%s): %d of %d points on the map %d navmesh in %s (%d more marked needs_walk)", p.Name, p.Status, on, total, p.Map, dir, walk)
	}
	if len(missing) > 0 {
		t.Logf("no navmesh files for maps %s (copy <map>.mmap and <map>*.mmtile from m910q's ~/tortoise-wow-server/data/mmaps to ~/tb-ci/mmaps-turtle<map>)", strings.Join(missing, ", "))
	}
	if !checkedAny {
		t.Skip("no draft plan's map has navmesh files here")
	}
}

// needsWalkPoints are the points of a leg marked needs_walk: its To, a flagged pull group's positions, a flagged boss's position.
func needsWalkPoints(l Leg) map[Point]bool {
	m := map[Point]bool{}
	if l.NeedsWalk {
		m[l.To] = true
	}
	for _, g := range l.PullGroups {
		if g.NeedsWalk {
			for _, q := range g.Positions {
				m[q] = true
			}
		}
	}
	if l.Boss != nil && l.Boss.NeedsWalk {
		m[l.Boss.Position] = true
	}
	return m
}

// A plan's own party is the hire step's default; the prepare block still wins.
func TestHireTakesThePlansComposition(t *testing.T) {
	g := newHireFake(t)
	g.plan.HireComposition = []string{"warrior:tank", "priest:healer", "mage:dps"}
	rep, err := g.run(hireSpec())
	if err != nil || rep.State != "done" {
		t.Fatalf("%v %s\n%s", err, rep.State, notesOf(rep))
	}
	if got := strings.Join(g.hireLines, "|"); got != "hire warrior tank level=43|hire priest healer level=43|hire mage dps level=43" {
		t.Fatalf("%s", got)
	}
	g = newHireFake(t)
	g.plan.HireComposition = []string{"warrior:tank", "priest:healer", "mage:dps"}
	spec := hireSpec()
	spec.Hire = &HireSpec{Composition: []string{"warrior", "priest"}}
	if rep, err = g.run(spec); err != nil || rep.State != "done" {
		t.Fatalf("%v %s\n%s", err, rep.State, notesOf(rep))
	}
	if got := strings.Join(g.hireLines, "|"); got != "hire warrior tank level=43|hire priest healer level=43" {
		t.Fatalf("%s", got)
	}
}
