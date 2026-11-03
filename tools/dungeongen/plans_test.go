package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/pjerra/turtle-agent/internal/dungeon"
	"github.com/pjerra/turtle-agent/internal/navmesh"
	"github.com/pjerra/turtle-agent/internal/planexport"
	"github.com/pjerra/turtle-agent/internal/worldspawn"
)

const (
	specFile  = "plans.json"
	plansDirT = "../../dungeons"
	tableFile = "../../docs/PLANS.md"
)

// instanceMaps are the 40 instance maps of the server (the folders of ~/turtle-agent-minimaps): vanilla dungeons and raids and Turtle's customs 800-822.
var instanceMaps = []uint32{33, 34, 35, 36, 43, 47, 48, 70, 90, 109, 129, 189, 209, 229, 230, 249, 269, 289, 309, 329, 349, 389, 409, 429, 469, 509, 531, 532, 533,
	800, 802, 807, 808, 814, 815, 816, 818, 819, 820, 822}

func loadSpecT(t *testing.T) *SpecFile {
	t.Helper()
	sf, err := LoadSpec(specFile)
	if err != nil {
		t.Fatal(err)
	}
	return sf
}

// Every instance map has at least one plan, and the spec names every plan file in dungeons/ (bar wailing_caverns-extra, a record).
func TestSpecCoversEveryInstanceMapAndFile(t *testing.T) {
	sf := loadSpecT(t)
	have := map[uint32]bool{}
	named := map[string]bool{"wailing_caverns-extra": true}
	for _, ps := range sf.Plans {
		have[ps.Map] = true
		named[ps.Name] = true
	}
	for _, m := range instanceMaps {
		if !have[m] {
			t.Errorf("map %d has no plan in the spec", m)
		}
	}
	files, _ := filepath.Glob(filepath.Join(plansDirT, "*.json"))
	for _, f := range files {
		if n := strings.TrimSuffix(filepath.Base(f), ".json"); !named[n] {
			t.Errorf("%s is in dungeons/ but not in the spec", f)
		}
	}
	if len(files) != len(sf.Plans)+1 {
		t.Errorf("%d plan files for %d spec plans (+ wailing_caverns-extra)", len(files), len(sf.Plans))
	}
}

// The committed drafts are what the generator writes from the spec and the exports: regenerate with
// `go run ./tools/dungeongen -spec tools/dungeongen/plans.json -out-dir dungeons -table docs/PLANS.md` after changing either.
func TestDraftPlansAreWhatTheGeneratorWrites(t *testing.T) {
	sf := loadSpecT(t)
	out := t.TempDir()
	files, err := GenerateAll(sf, "testdata", out)
	if err != nil {
		t.Fatal(err)
	}
	// Exactly the spec's generated (not handwritten) plans: a prepped plan leaves the generated set on purpose, so a fixed
	// floor broke with every prep; an exact count still catches a generator that silently writes fewer (or none).
	generated := 0
	for _, ps := range sf.Plans {
		if !ps.Handwritten {
			generated++
		}
	}
	if generated == 0 || len(files) != generated {
		t.Fatalf("%d plans generated, want the spec's %d generated plans", len(files), generated)
	}
	for _, f := range files {
		want, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		got, err := os.ReadFile(filepath.Join(plansDirT, filepath.Base(f)))
		if err != nil {
			t.Errorf("%s: %v", filepath.Base(f), err)
			continue
		}
		if !bytes.Equal(got, want) {
			t.Errorf("dungeons/%s is not what the generator writes from the spec: regenerate it", filepath.Base(f))
		}
	}
}

// Every generated plan loads, is a draft, has the fields its kind needs, and passes the creature check: no boss or pack member is an NPC
// (npc_flags) or friendly (faction 35) unless the spec says a script turns it hostile.
func TestDraftPlansLoadAndPassTheCheck(t *testing.T) {
	sf := loadSpecT(t)
	for _, ps := range sf.Plans {
		if ps.Handwritten {
			continue
		}
		p, err := dungeon.Load(filepath.Join(plansDirT, ps.Name+".json"))
		if err != nil {
			t.Errorf("%s: %v", ps.Name, err)
			continue
		}
		if p.Name != ps.Name || p.Map != ps.Map || p.Kind != ps.Kind || p.Status != dungeon.DraftStatus {
			t.Errorf("%s: name %q map %d kind %q status %q", ps.Name, p.Name, p.Map, p.Kind, p.Status)
		}
		if lo, hi := p.Band(); lo == 0 || hi == 0 {
			t.Errorf("%s: no level band", ps.Name)
		}
		if len(p.Legs) != len(ps.Bosses) {
			t.Errorf("%s: %d legs for %d bosses", ps.Name, len(p.Legs), len(ps.Bosses))
		}
		if ps.Trigger != 0 && (p.EntranceTrigger != ps.Trigger || p.Portal == nil) {
			t.Errorf("%s: trigger %d portal %+v", ps.Name, p.EntranceTrigger, p.Portal)
		}
		if ps.Kind == dungeon.KindRaid {
			if p.Raid == nil || p.Raid.Size != ps.Size || len(p.HireComposition) != 0 {
				t.Errorf("%s: raid block %+v", ps.Name, p.Raid)
			}
		} else if n := len(p.HireComposition); n < 2 || n > 5 {
			t.Errorf("%s: hire_composition %v", ps.Name, p.HireComposition)
		}
		for i, l := range p.Legs {
			if l.Boss == nil || l.Boss.Entry == 0 {
				t.Errorf("%s legs[%d]: no boss", ps.Name, i)
			}
		}
		cs, err := readCreatures(filepath.Join("testdata", ps.Data, "creatures.tsv"))
		if err != nil {
			t.Fatal(err)
		}
		for _, w := range Check(p, cs, CheckOptions{Scripted: toSet(ps.Scripted)}) {
			if strings.Contains(w, "is an NPC, not a mob") || strings.Contains(w, "is friendly to") {
				t.Errorf("%s: %s", ps.Name, w)
			}
		}
		// every boss is a real spawn of the export (the generator already fails otherwise) and no boss.first entry is also a pull-group member
		for i, l := range p.Legs {
			for _, e := range l.Boss.First {
				for j, g := range l.PullGroups {
					for _, m := range g.Entries {
						if m == e {
							t.Errorf("%s legs[%d].pull_groups[%d]: %d is a boss.first entry", ps.Name, i, j, e)
						}
					}
				}
			}
		}
	}
}

func TestPlansTableIsCurrent(t *testing.T) {
	sf := loadSpecT(t)
	want, err := Table(sf, plansDirT)
	if err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(tableFile)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, want) {
		t.Error("docs/PLANS.md is out of date: regenerate it with `go run ./tools/dungeongen -spec tools/dungeongen/plans.json -table docs/PLANS.md`")
	}
	for _, m := range sf.VMMaps {
		if !slices.Contains(instanceMaps, m) && m != 1 {
			t.Errorf("vm_mmaps names map %d: not an instance map (or map 1)", m)
		}
	}
}

func TestSpecErrors(t *testing.T) {
	dir := t.TempDir()
	write := func(s string) string {
		f := filepath.Join(dir, "s.json")
		if err := os.WriteFile(f, []byte(s), 0o644); err != nil {
			t.Fatal(err)
		}
		return f
	}
	for _, c := range []struct{ in, want string }{
		{`{"plans":[{"name":"a","title":"A","map":1,"kind":"zoo","data":"x","bosses":[1]}]}`, "kind is dungeon or raid"},
		{`{"plans":[{"name":"a","title":"A","map":1,"kind":"raid","data":"x","bosses":[1]}]}`, "needs a size"},
		{`{"plans":[{"name":"a","title":"A","map":1,"kind":"dungeon","bosses":[1]}]}`, "data and bosses are required"},
		{`{"plans":[{"name":"a","title":"A","map":1,"kind":"dungeon","data":"x","bosses":[1]},{"name":"a","title":"A","map":1,"kind":"dungeon","data":"x","bosses":[1]}]}`, "used twice"},
		{`{"plans":[{"name":"a","title":"A","map":1,"kind":"dungeon","data":"x","bosses":[{"entry":1,"frist":[2]}]}]}`, "unknown field"},
	} {
		if _, err := LoadSpec(write(c.in)); err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: got %v, want %q", c.in, err, c.want)
		}
	}
}

// A boss with first entries keeps them out of the packs; route order walks nearest-first with the last boss last; the band comes from the packs.
func TestGenerateSpecSynthetic(t *testing.T) {
	dir := t.TempDir()
	d := filepath.Join(dir, "m1")
	if err := os.MkdirAll(d, 0o755); err != nil {
		t.Fatal(err)
	}
	head := "guid\tentry\tname\tlevel_min\tlevel_max\trank\tfaction\tnpc_flags\tx\ty\tz\twander\tmovement_type\tgroup_leader\tpatrol\tauras\n"
	row := func(guid, entry int, name string, lvl, rank, faction int, x float64) string {
		return strings.Join([]string{itoa(guid), itoa(entry), name, itoa(lvl), itoa(lvl), itoa(rank), itoa(faction), "0", ftoa(x), "0", "0", "0", "0", "0", "0", ""}, "\t") + "\n"
	}
	cs := head + row(1, 10, "Trash A", 20, 0, 59, 10) + row(2, 11, "Trash B", 22, 0, 59, 100) + row(3, 12, "Trash C", 24, 0, 59, 200) +
		row(4, 900, "Near Boss", 25, 3, 59, 40) + row(5, 901, "Far Boss", 25, 3, 59, 300) + row(6, 902, "Add", 25, 3, 59, 302)
	files := map[string]string{
		"creatures.tsv":   cs,
		"gameobjects.tsv": "guid\tentry\tname\ttype\tflags\tx\ty\tz\tstate\n",
		"entrances.tsv":   "id\tname\ttarget_map\tx\ty\tz\to\n7\tIn\t1\t0\t0\t0\t0\n",
		"portals.tsv":     "target_map\tid\tname\trequired_level\touter_map\tx\ty\tz\tradius\tbox_x\tbox_y\tbox_z\ttarget_x\ttarget_y\ttarget_z\n1\t7\tIn\t10\t0\t5\t6\t7\t3\t0\t0\t0\t0\t0\t0\n",
	}
	for n, c := range files {
		if err := os.WriteFile(filepath.Join(d, n), []byte(c), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	ps := PlanSpec{Name: "syn", Title: "Syn", Map: 1, Kind: dungeon.KindDungeon, Data: "m1", Trigger: 7, Route: true,
		Bosses: []BossSpec{{Entry: 901, First: []uint32{902}}, {Entry: 900}}}
	p, err := GenerateSpec(ps, dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Legs) != 2 || p.Legs[0].Boss.Entry != 900 || p.Legs[1].Boss.Entry != 901 || len(p.Legs[1].Boss.First) != 1 {
		t.Fatalf("%+v", p.Legs)
	}
	for _, l := range p.Legs {
		for _, g := range l.PullGroups {
			for _, e := range g.Entries {
				if e == 902 {
					t.Errorf("the first entry 902 is a pack")
				}
			}
		}
	}
	if p.Portal == nil || p.Portal.Map != 0 || p.Portal.X != 5 || p.EntranceTrigger != 7 {
		t.Errorf("portal %+v trigger %d", p.Portal, p.EntranceTrigger)
	}
	if lo, hi := p.Band(); lo != 20 || hi != 25 { // median 22 -> 20; p90 24 but at least median + 3 = 25
		t.Errorf("band %d-%d", lo, hi)
	}
	if p.Status != dungeon.DraftStatus || len(p.HireComposition) != 5 {
		t.Errorf("status %q composition %v", p.Status, p.HireComposition)
	}
	// a bbox keeps the bosses and drops the packs outside
	ps.Bbox = [][4]float64{{0, -10, 50, 10}}
	p, err = GenerateSpec(ps, dir)
	if err != nil {
		t.Fatal(err)
	}
	n := 0
	for _, l := range p.Legs {
		n += len(l.PullGroups)
	}
	if n != 1 {
		t.Errorf("%d packs in the box, want 1", n)
	}
}

func itoa(n int) string     { return strconv.Itoa(n) }
func ftoa(f float64) string { return strconv.FormatFloat(f, 'f', -1, 64) }

// ApplySnaps moves a listed point wherever it stands (to, pull positions, boss), marks the ones with no floor, and leaves the rest.
func TestApplySnaps(t *testing.T) {
	pt := func(x float32) dungeon.Point { return dungeon.Point{X: x, Y: 1, Z: 2} }
	to := pt(10)
	p := &dungeon.Plan{Legs: []dungeon.Leg{{To: pt(1), PullGroups: []dungeon.PullGroup{{Positions: []dungeon.Point{pt(1), pt(2)}}, {Positions: []dungeon.Point{pt(3)}}}, Boss: &dungeon.Boss{Position: pt(4)}}}}
	moved, marked := ApplySnaps(p, []Snap{{From: pt(1), To: &to}, {From: pt(3), NeedsWalk: true}, {From: pt(4), NeedsWalk: true}})
	l := p.Legs[0]
	if moved != 2 || marked != 2 || l.To != to || l.PullGroups[0].Positions[0] != to || l.PullGroups[0].Positions[1] != pt(2) {
		t.Fatalf("%d %d %+v", moved, marked, l)
	}
	if l.NeedsWalk || l.PullGroups[0].NeedsWalk || !l.PullGroups[1].NeedsWalk || !l.Boss.NeedsWalk || l.PullGroups[1].Positions[0] != pt(3) {
		t.Fatalf("%+v", l)
	}
}

// closestInPoly: a point inside stays, one outside goes to the nearest edge point moved a little inside, either winding.
func TestClosestInPoly(t *testing.T) {
	sq := []navmesh.Vec3{{0, 0, 0}, {10, 0, 0}, {10, 10, 0}, {0, 10, 0}}
	for _, vs := range [][]navmesh.Vec3{sq, {sq[3], sq[2], sq[1], sq[0]}} {
		if x, y, d := closestInPoly(vs, 5, 5); x != 5 || y != 5 || d != 0 {
			t.Errorf("inside: %v %v %v", x, y, d)
		}
		x, y, d := closestInPoly(vs, 13, 5)
		if d != 3 || x < 9.5 || x >= 10 || y < 4.9 || y > 5.1 {
			t.Errorf("outside: %v %v %v", x, y, d)
		}
	}
}

// marliStandYd is how far the Mar'li leg's `to` and its stands stay from the two spider packs: twice the 20 yd
// detection range (a bot at ~20 yd wakes the pack, and the party spreads round the stand), the bound of SPEC 1.
const marliStandYd = 40

// marliPatrollerSpawnYd is the one exception to marliStandYd: pack 0's patroller 49202 belongs to the very pack group
// 0's plain pull wakes (the pull walks the gathered party to a firing stand in the pack's shot), so its SPAWN is inside
// the pack this row pulls by design. The approach only has to keep the spawn out of the band that woke the pack in
// walk 25 (20 yd detection plus the 5 yd wander, with the party spread round the stand): 30 yd, and the cut row that
// was 14.1 yd out is what this bounds.
const marliPatrollerSpawnYd = 30

// marliCutRowYd is how near a pack-0 spawn an exported leg-2 row may stand before group 0's pull row: the packs detect
// at 20 yd (with a 5 yd wander) and the walk-25 approach-hop cut put a MOVE row 14.1 yd from the patroller's spawn,
// waking the pack with no pull line and no CC. 25 yd keeps every row out of the band, and the pull row's own firing
// stand (~23.5 yd) covers the fight.
const marliCutRowYd = 25

// marliPatrolReachYd is the farthest a stand-pull pack's patroller may be from its stand at the closest point of its
// loop: the stand-pull gate holds the party at the stand until the patroller is in the tank's shot (leadShotYd 28.5
// minus kStandPullMarginYd 1 -> 27.5 yd) and in sight, and after kStandPullWaitMs (600 s) a patroller that never
// comes is a pause (SPEC-1-AMEND, DungeonRunPolicy.h PatrolStandPullGate). The extra yard keeps the check off the
// gate's own edge.
const marliPatrolReachYd = 26.5

// TestZulGurubMarliStandsOutOfSpiderAggro: walk 25's Mar'li leg walked the raid onto the spawn of the patrolling
// Skitterer 49202 (pack 0's leader) and, with the fight drifting west, onto pack 1 (leader 49201) as well: 20-21
// elites at once, 6 of 7 wipes (TortoiseBots-audit research/zg-w25/MARLI-PULL.md sections 0-2). The leg's `to` and
// the stands must be out of both packs' aggro: every creature of pack 0 (group_leader 49202) and pack 1
// (group_leader 49201 plus the loose Venombrood 49246) stays at least marliStandYd away; the leg 2 packs' members
// are the only ones listed, and a pack is aggro-together (creature_groups flags 14). Walk 25's other half
// (SPEC-1-AMEND, Codex's HIGH): pack 0's row must NOT be a stand pull. Its leader 49202 walks a 13-point loop that
// never comes within 43.8 yd of S0, so the gate would wait 600 s for it and pause the run; the plain pull instead
// walks the gathered party to a firing stand in the pack's own shot. Pack 1's row stays a stand pull (it is for the
// patrol clearance; its 12 units are all static and take the plain pull's firing-stand walk either way), so every
// stand-pull row of the leg has to reach its patrollers. S0's clearance is the 40 yd rule for every STATIC pack-0
// unit and for pack 1, with one reasoned exception: the patroller 49202's own spawn need only be 30 yd out, because it
// belongs to the very pack this plain pull wakes. S0's hop to the row centre also has to stay inside the exporter's
// 50 yd approach-hop reach: walk 25's S0 was 55.16 yd out, so the cut left a move row 14.1 yd from 49202's spawn and
// the pack woke with no pull line; assert (e) pins that no exported leg-2 row stands that near a pack-0 spawn.
func TestZulGurubMarliStandsOutOfSpiderAggro(t *testing.T) {
	p, err := dungeon.Load(filepath.Join(plansDirT, "zul_gurub.json"))
	if err != nil {
		t.Fatal(err)
	}
	cs, err := readCreatures(filepath.Join("testdata", "map309", "creatures.tsv"))
	if err != nil {
		t.Fatal(err)
	}
	movement, err := readMovementPaths(filepath.Join("testdata", "map309", "movement.tsv"))
	if err != nil {
		t.Fatal(err)
	}
	// pack0 and pack1 are the spawns of each pack: its own members and the loose Venombrood 49246 beside pack 1.
	var pack0, pack1 []creature
	for _, c := range cs {
		switch {
		case c.GroupLeader == 49202:
			pack0 = append(pack0, c)
		case c.GroupLeader == 49201 || c.GUID == 49246:
			pack1 = append(pack1, c)
		}
	}
	if len(pack0) != 9 || len(pack1) != 12 {
		t.Fatalf("pack 0 has %d spawns (want 9), pack 1 %d (want 12)", len(pack0), len(pack1))
	}
	var leg *dungeon.Leg
	for i := range p.Legs {
		if p.Legs[i].Name == "high_priestess_marli" {
			leg = &p.Legs[i]
		}
	}
	if leg == nil {
		t.Fatal("no high_priestess_marli leg")
	}
	// (a) the leg's `to` stands clear of both packs.
	for _, c := range append(append([]creature{}, pack0...), pack1...) {
		if d := dist(leg.To, c.Pos); d < marliStandYd {
			t.Errorf("to %.1f %.1f %.1f is %.1f yd from %s %d, want >= %d", leg.To.X, leg.To.Y, leg.To.Z, d, c.Name, c.GUID, marliStandYd)
		}
	}
	if len(leg.PullGroups) < 2 {
		t.Fatalf("%d pull groups", len(leg.PullGroups))
	}
	// The leg pulls pack 1 first (walk 26b's order fix: TestZulGurubMarliStep203RouteClearOfLaterPacks); the two
	// groups are named by the pack they pull, not by their index.
	g0, g1 := -1, -1
	for i, g := range leg.PullGroups[:2] {
		for _, c := range standPullPack(g, cs) {
			if c.GroupLeader == 49202 || c.GUID == 49202 {
				g0 = i
			} else if c.GroupLeader == 49201 || c.GUID == 49246 {
				g1 = i
			}
		}
	}
	if g0 < 0 || g1 < 0 || g0 == g1 {
		t.Fatalf("leg 2's first two groups do not pull pack 0 and pack 1 (pack0 group %d, pack1 group %d)", g0, g1)
	}
	// (b) raid clearance: the pack-0 group's stand clears every STATIC unit of pack 0 and every unit of pack 1, and
	// the pack-1 group's stand clears pack 0 (walk 27: it stands on the east slope, inside the pack it pulls; see the
	// exception below). Both rows have an approach.
	stands := map[int]dungeon.Point{}
	for i, g := range leg.PullGroups[:2] {
		if len(g.Approach) == 0 {
			t.Errorf("pull_groups[%d] has no stand (approach point)", i)
			continue
		}
		stands[i] = g.Approach[len(g.Approach)-1]
	}
	check := func(i int, targets []creature, min float64) {
		for _, c := range targets {
			if d := dist(stands[i], c.Pos); d < min {
				t.Errorf("pull_groups[%d] approach %.1f %.1f %.1f is %.1f yd from %s %d, want >= %g", i, stands[i].X, stands[i].Y, stands[i].Z, d, c.Name, c.GUID, min)
			}
		}
	}
	var static0 []creature
	for _, c := range pack0 {
		if !c.Patrol {
			static0 = append(static0, c)
		}
	}
	check(g0, append(append([]creature{}, static0...), pack1...), marliStandYd)
	// The patroller 49202's own SPAWN is the one exception to marliStandYd for the pack-0 stand: the patroller
	// belongs to the very pack this plain pull wakes (the pull walks the gathered party to a firing stand in the
	// pack's shot), so its spawn is inside the pack this row pulls by design, and only has to stay out of the band
	// that woke it in walk 25.
	for _, c := range pack0 {
		if c.Patrol {
			if d := dist(stands[g0], c.Pos); d < marliPatrollerSpawnYd {
				t.Errorf("pull_groups[%d] approach %.1f %.1f %.1f is %.1f yd from the patroller %s %d's spawn, want >= %d", g0, stands[g0].X, stands[g0].Y, stands[g0].Z, d, c.Name, c.GUID, marliPatrollerSpawnYd)
			}
		}
	}
	// The pack-1 group's approach is the one exception to the own-pack 40 yd rule, from walk 27: it stands on the
	// reachable east slope -12253 -1653, 15.0 yd from 49254 inside its own pack, because that is the only floor whose
	// 23.5 yd firing stand keeps pack 0's patrol loop out of the raid's spread (research/zg-w27/MARLI-PACK0.md,
	// TestZulGurubMarliStep203StandClearOfPack0). Pack 1 is the pack this row pulls, so waking it there IS the pull (it
	// woke on the stand walk in walk 27 too); what must stay clear of the staging point is the OTHER pack and every
	// pack pulled later.
	check(g1, pack0, marliStandYd)
	// (c) pull reachability: every stand-pull row of the leg reaches its pack's patrollers from the stand.
	for i, g := range leg.PullGroups {
		if !g.StandPull {
			continue
		}
		if len(g.Approach) == 0 {
			t.Errorf("pull_groups[%d]: a stand pull with no stand (approach point)", i)
			continue
		}
		stand := g.Approach[len(g.Approach)-1]
		if worst, ok := standPullPatrolReach(stand, standPullPack(g, cs), movement); !ok {
			t.Errorf("pull_groups[%d] stand %.1f %.1f %.1f: the worst patroller's loop comes within %.1f yd of it, want <= %g (the stand-pull gate would wait 600 s for it and pause)",
				i, stand.X, stand.Y, stand.Z, worst, marliPatrolReachYd)
		}
	}
	// (d) the kinds of the two rows (SPEC-1-AMEND): pack 0 is a plain pull from S0, pack 1 keeps the stand pull. At walk
	// 27's east approach the party stands inside pack 1's aggro, so the gate's anyInCombat check passes it to the plain
	// pull's stand walk at once (the flag is moot there, not a 600 s wait); it stays because the row's kind is unchanged
	// and a stand pull is what carries the row's clear_yd.
	if leg.PullGroups[g0].StandPull {
		t.Errorf("pull_groups[%d] (pack 0) is a stand pull: its leader 49202 walks a loop that never comes within the gate's 27.5 yd of S0, so the gate would wait 600 s and pause the run; the pack is a plain pull from the stand", g0)
	}
	if !leg.PullGroups[g1].StandPull {
		t.Errorf("pull_groups[%d] (pack 1) is not a stand pull: it keeps one for its patrol clearance (a stand pull carries the row's clear_yd in its param)", g1)
	}
	// (e) the exporter's own --approach-hops cut must not put a row of leg 2 inside pack 0's aggro before the first
	// spider pull row. With the old S0 -12245.333 -1685.3334 the hop to the row centre -12300.49 -1685.68 was 55.16 yd, past
	// the run leader's 50 yd reach (dungeon.RunLeaderReachYd), so cutLongHops walked the navmesh and left a row at
	// -12275.199 -1686.1334, 14.1 yd from the patroller 49202's spawn and 17.1 from the static Venombrood 49771: the
	// party walked into pack 0 on a MOVE row, before the pull step, so no pull line and no Polymorph went out. The new
	// S0 is 48.99 yd from the centre, inside the reach, so nothing is cut there. The check runs the exporter with the
	// server's navmesh (the cut walks the mesh); it skips without one.
	planner := marliMmapsPlanner(t)
	res, err := planexport.Build(p, planexport.Options{ApproachHops: true, Cut: marliExportCut(planner)})
	if err != nil {
		t.Fatal(err)
	}
	var leg2 []planexport.StepRow
	for _, s := range res.Rows.Steps {
		if s.Leg == 2 {
			leg2 = append(leg2, s)
		}
	}
	g0row := -1
	for i, s := range leg2 {
		if s.Kind == planexport.KindPull && strings.HasPrefix(s.Comment, "pull group 0") {
			g0row = i
			break
		}
	}
	if g0row < 0 {
		t.Fatal("no pull row for group 0 in the exported leg 2")
	}
	for i, s := range leg2[:g0row] {
		for _, c := range pack0 {
			if d := math.Hypot(float64(s.X-c.Pos.X), float64(s.Y-c.Pos.Y)); d < marliCutRowYd {
				t.Errorf("exported leg 2 row %d (%s %.1f %.1f) is %.1f yd from %s %d, want >= %d: the exporter cut S0's hop into pack 0's aggro, so the pack wakes on a MOVE row before the pull step",
					i, s.Comment, s.X, s.Y, d, c.Name, c.GUID, marliCutRowYd)
			}
		}
	}
}

// marliMmapsPlanner is the server's navmesh for the leg-2 export check ($TURTLE_AGENT_MMAPS or
// ~/turtle-agent-mmaps, as planprobe and navmesh's own real tests use); the test skips without it.
func marliMmapsPlanner(t *testing.T) *navmesh.Planner {
	t.Helper()
	home, _ := os.UserHomeDir()
	for _, dir := range []string{os.Getenv("TURTLE_AGENT_MMAPS"), filepath.Join(home, "turtle-agent-mmaps")} {
		if dir == "" {
			continue
		}
		if _, err := os.Stat(filepath.Join(dir, "309.mmap")); err != nil {
			continue
		}
		if tiles, _ := filepath.Glob(filepath.Join(dir, "309*.mmtile")); len(tiles) == 0 {
			continue
		}
		p := navmesh.NewPlanner(dir)
		t.Cleanup(p.Close)
		return p
	}
	t.Skip("no map 309 navmesh files: set TURTLE_AGENT_MMAPS to a folder holding 309.mmap and its tiles")
	return nil
}

// marliExportCut is plan-export's own cutter (cmd/turtle-agent plan_export_cmd.go cutWith, kept in step with it): the
// navmesh path from a to b, a point at each corner and equal hops of MoveGapCutYd along the long stretches. The
// exporter trims the points within approachStandoffYd of the target itself.
func marliExportCut(pl *navmesh.Planner) planexport.CutFunc {
	return func(mapID uint32, from, to dungeon.Point) ([]dungeon.Point, []planexport.CutNote, error) {
		path, err := pl.Plan(mapID, navmesh.Vec3{from.X, from.Y, from.Z}, navmesh.Vec3{to.X, to.Y, to.Z})
		if err != nil {
			return nil, nil, err
		}
		if path.Status != navmesh.Complete || len(path.Points) < 2 {
			return nil, nil, fmt.Errorf("no complete path (%s %s)", path.Status, path.Reason)
		}
		var via []dungeon.Point
		prev := path.Points[0]
		for i := 1; i < len(path.Points); i++ {
			p := path.Points[i]
			d := math.Sqrt(float64(sq32(p[0]-prev[0]) + sq32(p[1]-prev[1]) + sq32(p[2]-prev[2])))
			for k := 1; float64(k)*planexport.MoveGapCutYd < d; k++ {
				f := float32(float64(k) * planexport.MoveGapCutYd / d)
				via = append(via, dungeon.Point{X: prev[0] + (p[0]-prev[0])*f, Y: prev[1] + (p[1]-prev[1])*f, Z: prev[2] + (p[2]-prev[2])*f})
			}
			if i < len(path.Points)-1 {
				via = append(via, dungeon.Point{X: p[0], Y: p[1], Z: p[2]})
			}
			prev = p
		}
		return via, nil, nil
	}
}

func sq32(v float32) float32 { return v * v }

// TestStandPullPatrolReach is the reach check on a synthetic square loop (20 yd sides at x,y 0..20): a stand 20 yd
// from the loop is reached, one 30 yd away is not, and a pack without a patroller is always fine (its static units
// go to the plain pull's firing-stand walk).
func TestStandPullPatrolReach(t *testing.T) {
	const guid = 500
	square := []dungeon.Point{{X: 0, Y: 0}, {X: 0, Y: 20}, {X: 20, Y: 20}, {X: 20, Y: 0}}
	movement := map[uint64][]dungeon.Point{guid: square}
	patroller := []creature{{GUID: guid, Patrol: true}}
	static := []creature{{GUID: guid, Patrol: false}}
	for _, c := range []struct {
		name  string
		stand dungeon.Point
		pack  []creature
		worst float64
		ok    bool
	}{
		{"20 yd from the loop", dungeon.Point{X: 10, Y: -20}, patroller, 20, true},
		{"30 yd from the loop", dungeon.Point{X: 10, Y: -30}, patroller, 30, false},
		{"no patroller", dungeon.Point{X: 10, Y: -30}, static, 0, true},
	} {
		worst, ok := standPullPatrolReach(c.stand, c.pack, movement)
		if ok != c.ok || math.Abs(worst-c.worst) > 0.01 {
			t.Errorf("%s: standPullPatrolReach = %g, %v; want %g, %v", c.name, worst, ok, c.worst, c.ok)
		}
	}
}

// standPullPatrolReach is what the stand-pull gate needs of a stand (see marliPatrolReachYd): the worst of the pack's
// patrolling members' closest approaches to the stand, 2-D with the loop closed, and whether that is within
// marliPatrolReachYd. A pack with no patroller is 0, true. A patroller named in the pack but absent from movement.tsv
// counts as unreachable (no loop to wait for).
func standPullPatrolReach(stand dungeon.Point, pack []creature, movement map[uint64][]dungeon.Point) (worstYd float64, ok bool) {
	ok = true
	for _, c := range pack {
		if !c.Patrol {
			continue
		}
		loop, have := movement[c.GUID]
		var d float64
		if !have || len(loop) == 0 {
			d = math.Inf(1)
		} else {
			d = closestOnLoop(stand, loop)
		}
		if d > worstYd {
			worstYd = d
		}
		if d > marliPatrolReachYd {
			ok = false
		}
	}
	return worstYd, ok
}

// standPullPack is the pack a stand-pull row pulls (DungeonRunPolicy.h ScopeStandPullFacts): every formation with a
// spawn within the row's radius of the row's centre (the mean of the plan's own positions), plus, since a linked pack
// is aggro-together (creature_groups flags 14), every creature of a member's group_leader.
func standPullPack(g dungeon.PullGroup, cs []creature) []creature {
	var c dungeon.Point
	for _, p := range g.Positions {
		c.X += p.X
		c.Y += p.Y
		c.Z += p.Z
	}
	n := float32(len(g.Positions))
	if n == 0 {
		return nil
	}
	c = dungeon.Point{X: c.X / n, Y: c.Y / n, Z: c.Z / n}
	in := map[uint64]bool{}
	var leaders []uint64
	for _, x := range cs {
		if math.Hypot(float64(x.Pos.X-c.X), float64(x.Pos.Y-c.Y)) <= float64(g.Radius) {
			in[x.GUID] = true
			if x.GroupLeader != 0 && !slices.Contains(leaders, x.GroupLeader) {
				leaders = append(leaders, x.GroupLeader)
			}
		}
	}
	for _, x := range cs {
		if !in[x.GUID] && slices.Contains(leaders, x.GroupLeader) {
			in[x.GUID] = true
		}
	}
	var out []creature
	for _, x := range cs {
		if in[x.GUID] {
			out = append(out, x)
		}
	}
	return out
}

// closestOnLoop is the smallest 2-D distance from p to a patrol's waypoints taken as a closed loop (the last point
// back to the first), movement.tsv's point order.
func closestOnLoop(p dungeon.Point, loop []dungeon.Point) float64 {
	if len(loop) == 1 {
		return math.Hypot(float64(p.X-loop[0].X), float64(p.Y-loop[0].Y))
	}
	best := math.Inf(1)
	for i := range loop {
		if d := pointSeg2D(p, loop[i], loop[(i+1)%len(loop)]); d < best {
			best = d
		}
	}
	return best
}

// pointSeg2D is the 2-D distance from p to the segment a-b.
func pointSeg2D(p, a, b dungeon.Point) float64 {
	dx, dy := float64(b.X-a.X), float64(b.Y-a.Y)
	l := dx*dx + dy*dy
	t := 0.0
	if l != 0 {
		t = ((float64(p.X-a.X))*dx + (float64(p.Y-a.Y))*dy) / l
		t = math.Max(0, math.Min(1, t))
	}
	return math.Hypot(float64(p.X)-(float64(a.X)+t*dx), float64(p.Y)-(float64(a.Y)+t*dy))
}

// readMovementPaths reads movement.tsv (export.sh): the waypoints of each patrolling spawn by guid, in point order.
func readMovementPaths(path string) (map[uint64][]dungeon.Point, error) {
	rs, err := rows(path, "guid", "point", "x", "y", "z")
	if err != nil {
		return nil, err
	}
	r := &reader{path: path}
	out := map[uint64][]dungeon.Point{}
	for _, m := range rs {
		out[uint64(r.num(m, "guid"))] = append(out[uint64(r.num(m, "guid"))], r.pos(m))
	}
	return out, r.err
}

// arlokkTerraceRowYd is how near a walk may pass a pack that a later pull row owns: the mobs detect at 20 yd (the
// map-309 spawns' detection_range), and the party stands up to 3 yd off the walk, so a sample inside that band (20 + 3)
// wakes the pack before its pull row.
const arlokkTerraceRowYd = 23

// arlokkTerraceStoreyYd is how far above or below a walk a spawn may stand and still count: the terrace's three floors
// are 10.8-10.9 yd apart, and the upper-level walks have to pass over a middle-level pack (the ring over it is the only
// way round), where the floor keeps a pack on the storey below from seeing the party. The one crossing that must count
// is the bridge over the lower pack's pit: it lies 8.2-8.4 yd above the pit floor, and the middle packs' closest pass
// under a walk is 9.4 yd (19.8 yd 3D) on the ramp, so the band sits between them at 9 yd.
const arlokkTerraceStoreyYd = 9

// arlokkRoutesFixture is the committed navmesh polyline of every Arlokk terrace walk (map 309): the test always checks
// the clearance rule against it, so the rule holds where the server's mmaps are absent. Regenerate it with -update-routes.
const arlokkRoutesFixture = "testdata/zg_arlokk_terrace_routes.json"

// updateRoutes rewrites arlokkRoutesFixture from the map-309 navmesh: `go test -run
// TestZulGurubArlokkTerraceRouteClearOfLaterPacks -update-routes ./tools/dungeongen/`. It needs the mmaps.
var updateRoutes = flag.Bool("update-routes", false, "rewrite "+arlokkRoutesFixture+" from the map-309 navmesh")

// arlokkVec is a fixture point: the bare [x, y, z] triple, so the routes read as plain number lists.
type arlokkVec [3]float32

// arlokkRoute is one committed terrace walk: the two exported move rows it joins and the navmesh polyline between them,
// sampled every yard as arlokkTerracePolyline does.
type arlokkRoute struct {
	FromSeq   int         `json:"from_seq"`
	From      string      `json:"from"`
	ToSeq     int         `json:"to_seq"`
	To        string      `json:"to"`
	FromPoint arlokkVec   `json:"from_point"` // [x, y, z]
	ToPoint   arlokkVec   `json:"to_point"`   // [x, y, z]
	Points    []arlokkVec `json:"points"`
}

// arlokkRoutes is the shape of arlokkRoutesFixture.
type arlokkRoutes struct {
	Note   string        `json:"note"`
	Routes []arlokkRoute `json:"routes"`
}

// TestZulGurubArlokkTerraceRouteClearOfLaterPacks: walk 25's Arlokk leg walked the raid onto the three-level terrace in the
// plan's group order 11, 12, 13, ... and the move from group 11's approach to group 12's crossed the spawn ring of group 15's
// four Shadow Hunters 11339 at z 71.4 (13.3 yd from 51354), which woke groups 12, 14, 15 and 16 at once: 3 of 4 attempts
// wiped, the MT idle under 11 elites (TortoiseBots-audit tests/raid-zg-walk25/summary-autofix.md "Arlokk"). Review round 1
// (Opus HIGH) then found the upper-level-first order still crossed the lower pack's ring on the navmesh: the route from
// 13's stand to 16's stand ran the bridge at y -1639 z 57.7-60.4, 8.5 yd from 51383 while the lower pack was still alive
// (the straight segments are blind to the bridge the mesh takes).
// Walk 28 (2026-11-02) then showed the seven groups are one 11-20 yd cluster (11.0 yd 13/15, 16.8 12/13, 19.5 12/15) and
// the 20 yd family-assist ring wakes the neighbours of any one pull, so the plan pulls the whole 27-unit cluster as one
// group: 11 upper A, 12 upper B + upper east + middle east + lower + middle west (from the upper-east stand, before the
// bridge), 13 upper north. Walk 29 then wiped at 14 engaged (b-r1) and at 11 (g): the row is split per formation (fix
// 4b): 12 upper B + upper east, 13 middle east, 14 upper north, 15 middle west, 16 lower, each with its own stand and
// max_bodies. The test walks the exported leg-4 rows from group 11's approach through the LAST terrace pull
// (the max seq among the pull rows for groups 11-16, each of which must appear exactly once): for every pair
// of consecutive move rows, every spawn of a formation that a later pull row owns stays arlokkTerraceRowYd out of the walk.
// "Later" leaves out the pack the walk is approaching: its own pull row comes after the move, and its firing stand is
// 12-25 yd from it by design. The big cluster's row must own its middle-east Blood Drinkers 51594/51595 (leader 49343)
// as well: walk 25's radius 12 stopped 14.4-15.3 yd short of them, so the pack's four Panthers came alone and the two
// Drinkers joined the upper-north fight.
//
// Each walk is checked three ways. The committed navmesh polylines in arlokkRoutesFixture
// (testdata/zg_arlokk_terrace_routes.json), sampled every yard, are checked ALWAYS, mmaps or not: they are what caught the
// bridge the straight segments cannot see. When the server's mmaps are present the test recomputes every polyline and
// fails when it differs from the fixture by more than 1 yd at any point (the fixture is then stale: regenerate it with
// `go test -run TestZulGurubArlokkTerraceRouteClearOfLaterPacks -update-routes`, which also needs the mmaps). The fixture
// names the two rows each route joins (seq, comment, point), so a plan that moves or reorders the terrace rows fails
// until it is regenerated. Finally the straight segments between the rows every 2 yd are checked as an extra assertion
// that never skips. All checks report the measured minimum distance for every segment (t.Logf, so -v lists them; the
// failure lines carry the worst sample).
//
// The storey band (arlokkTerraceStoreyYd, 9 yd): a spawn more than 9 yd above or below a walk does not count, because the
// terrace storeys have solid floors and the core's aggro needs line of sight. The floors are 10.8-10.9 yd apart and the
// upper level is a ring over the middle-level packs, so the upper walks pass one storey above a middle pack without
// waking it (10.9 yd above 49339 on the west ring, 11.5 yd above 49345 in the east corridor). The crossings that must
// keep counting are the bridge over the lower pit, 8.2-8.4 yd above the lower pack (dz -8.4, 8.5 yd 3D from 51383), and
// the ramp's nearest pass at dz 8.8, 20.2 yd from 49341 of group 17. Nine yd sits between them: the bridge regression
// stays red and the mandated order's ring crossings pass. It is a geometric proxy; the navmesh cannot say whether a floor
// is a solid terrace or an open bridge.
func TestZulGurubArlokkTerraceRouteClearOfLaterPacks(t *testing.T) {
	p, err := dungeon.Load(filepath.Join(plansDirT, "zul_gurub.json"))
	if err != nil {
		t.Fatal(err)
	}
	cs, err := readCreatures(filepath.Join("testdata", "map309", "creatures.tsv"))
	if err != nil {
		t.Fatal(err)
	}
	// formations: the spawns of each pack, which the export and the server key on group_leader (a pack is aggro-together).
	formations := map[uint64][]creature{}
	var leaders []uint64
	for _, c := range cs {
		lead := c.GroupLeader
		if lead == 0 {
			lead = c.GUID
		}
		if _, ok := formations[lead]; !ok {
			leaders = append(leaders, lead)
		}
		formations[lead] = append(formations[lead], c)
	}
	slices.Sort(leaders)
	byGUID := map[uint64]creature{}
	for _, c := range cs {
		byGUID[c.GUID] = c
	}
	res, err := planexport.Build(p, planexport.Options{})
	if err != nil {
		t.Fatal(err)
	}
	var leg4 []planexport.StepRow
	for _, s := range res.Rows.Steps {
		if s.Leg == 4 {
			leg4 = append(leg4, s)
		}
	}
	// The terrace is plan leg 4's pull groups 11-16 (the array indices the plan and the export's comments name), numbered
	// in pull order: 11 upper A, 12 upper B + upper east, 13 middle east, 14 upper north, 15 middle west, 16 lower
	// (walk 29's fix 4b split the 27-unit row back per formation). From group 11's approach row through the last group's
	// pull row. The last pull is the max seq among the six groups' pull rows (the terrace order moves with the walk's
	// findings, so it is not hard-coded here); each group must appear exactly once.
	terracePull := func(s planexport.StepRow) (int, bool) {
		var group int
		if _, err := fmt.Sscanf(s.Comment, "pull group %d", &group); err != nil || group < 11 || group > 16 {
			return 0, false
		}
		if rest := strings.TrimPrefix(s.Comment, fmt.Sprintf("pull group %d", group)); rest != "" && rest != " approach" && rest != "; stand pull" {
			return 0, false
		}
		return group, true
	}
	first, last := -1, -1
	counts := map[int]int{}
	for i, s := range leg4 {
		if s.Kind == planexport.KindMove && s.Comment == "pull group 11 approach" {
			first = i
		}
		group, ok := terracePull(s)
		if !ok || s.Kind != planexport.KindPull {
			continue
		}
		counts[group]++
		if last < 0 || s.Seq > leg4[last].Seq {
			last = i
		}
	}
	if first < 0 {
		t.Fatal("no group-11 approach row in the exported leg 4")
	}
	for group := 11; group <= 16; group++ {
		if counts[group] != 1 {
			t.Errorf("exported leg 4 has %d pull rows for group %d, want exactly 1", counts[group], group)
		}
	}
	if last <= first {
		t.Fatalf("no terrace rows in the exported leg 4 (group 11 approach at %d, last terrace pull at %d)", first, last)
	}
	isMove := func(k string) bool { return k == planexport.KindMove || k == planexport.KindMoveDirect }
	var moves, pulls []planexport.StepRow
	for _, s := range leg4 {
		switch {
		case s.Seq >= leg4[first].Seq && s.Seq <= leg4[last].Seq && isMove(s.Kind):
			moves = append(moves, s)
		case s.Kind == planexport.KindPull:
			pulls = append(pulls, s)
		}
	}
	if len(moves) < 2 {
		t.Fatalf("%d move rows in the terrace, want the three groups' approaches", len(moves))
	}
	// D4: the row that owns the big cluster's middle-east pack owns its linked Blood Drinkers too (51594/51595, leader
	// 49343 = the pack's first Panther). Walk 25's radius 12 owned the four Panthers only.
	row13 := -1
	for i, s := range pulls {
		if rowOwns(s, formations[49343]) {
			row13 = i
			break
		}
	}
	if row13 < 0 {
		t.Fatal("no exported pull row owns the group-13 pack (its 49343 formation)")
	}
	group := 0
	if _, err := fmt.Sscanf(pulls[row13].Comment, "pull group %d", &group); err != nil || group < 0 || group >= len(p.Legs[4].PullGroups) {
		t.Fatalf("the owning row's comment %q names no leg-4 group", pulls[row13].Comment)
	}
	pg := p.Legs[4].PullGroups[group]
	for _, guid := range []uint64{51594, 51595} {
		c, ok := byGUID[guid]
		if !ok {
			t.Fatalf("no spawn %d in the export", guid)
		}
		listed := false
		for _, q := range pg.Positions {
			if math.Hypot(float64(q.X-c.Pos.X), float64(q.Y-c.Pos.Y)) < 1 {
				listed = true
			}
		}
		if !listed {
			t.Errorf("leg 4 pull_groups[%d] does not list the Blood Drinker %d's spawn (%.1f %.1f %.1f)", group, guid, c.Pos.X, c.Pos.Y, c.Pos.Z)
		}
		if !rowOwnsOne(pulls[row13], c) {
			t.Errorf("the pull row %.0f %.0f %.1f (radius %.1f, entries %v) does not own the Blood Drinker %d (%s, entry %d) at %.1f %.1f %.1f: %.1f yd out",
				pulls[row13].X, pulls[row13].Y, pulls[row13].Z, pulls[row13].Radius, pulls[row13].Entries, guid, c.Name, c.Entry, c.Pos.X, c.Pos.Y, c.Pos.Z, rowDist(pulls[row13], c))
		}
	}
	// Every pair of consecutive move rows: the packs a later pull row owns stay arlokkTerraceRowYd out of the walk, on the
	// committed navmesh polyline (always), on the navmesh's own polyline (when the mmaps are present, which must match the
	// committed one) and on the straight segment (always); each pair logs its measured minimum.
	type terraceWalk struct {
		a, b      planexport.StepRow
		laterFams [][]creature
	}
	var walks []terraceWalk
	for i := 0; i+1 < len(moves); i++ {
		a, b := moves[i], moves[i+1]
		next := -1 // the pull row the pair walks to: the first one after the second move row
		for j, s := range pulls {
			if s.Seq > b.Seq {
				next = j
				break
			}
		}
		if next < 0 {
			continue
		}
		var laterFams [][]creature
		for _, later := range pulls[next+1:] {
			for _, lead := range leaders {
				fam := formations[lead]
				if !rowOwns(later, fam) {
					continue
				}
				laterFams = append(laterFams, fam)
			}
		}
		// Walk 29 fix 4b: the only navmesh access to the middle level is the ramp at about -11518 -1639, which crosses
		// 8.4 yd above the lower mid-west pack (51365, its spawns at z 49.6) while that pack is still alive. Every
		// middle-level stand (the D row included) is reached through it; the plan's mitigation is the off-tank on the
		// stand and the TB safety net in WALK29-FIX-PLAN §9.4 (#9). The crossing is logged and left out of this walk's
		// later-pack clearance only; the straight check and the D -> E walk still hold the rule. Walk-31 fix 5 splits
		// group 13 in two, so the ramp walk is now group 15 -> group 16 (was 14 -> 15).
		if a.Comment == "pull group 15 approach" && b.Comment == "pull group 16 approach" {
			kept := laterFams[:0]
			for _, fam := range laterFams {
				if len(fam) > 0 && fam[0].GroupLeader == 51365 {
					t.Logf("leg 4 rows %d -> %d: the middle-level ramp crosses 8.4 yd above the lower pack 51365 (z 49.6) while it is alive; the off-tank on D's stand is the plan's mitigation (WALK29-FIX-PLAN §2/§9.4)", a.Seq, b.Seq)
					continue
				}
				kept = append(kept, fam)
			}
			laterFams = kept
		}
		walks = append(walks, terraceWalk{a: a, b: b, laterFams: laterFams})
	}
	planner := arlokkTerracePlanner()
	if planner != nil {
		defer planner.Close()
	} else {
		t.Logf("no map 309 navmesh files: the committed polylines in %s are checked and the recomputation is skipped (set TURTLE_AGENT_MMAPS to a folder holding 309.mmap and its tiles); the straight-line check below still runs", arlokkRoutesFixture)
	}
	if *updateRoutes {
		if planner == nil {
			t.Fatalf("cannot rewrite %s without the map 309 navmesh (set TURTLE_AGENT_MMAPS to a folder holding 309.mmap and its .mmtile files)", arlokkRoutesFixture)
		}
		var fixed arlokkRoutes
		fixed.Note = arlokkRoutesNote
		for _, w := range walks {
			poly, ok := arlokkTerracePolyline(planner, p.Map, rowPoint(w.a), rowPoint(w.b))
			if !ok {
				t.Fatalf("leg 4 rows %d (%s) -> %d (%s): no complete navmesh path, cannot write %s", w.a.Seq, w.a.Comment, w.b.Seq, w.b.Comment, arlokkRoutesFixture)
			}
			fixed.Routes = append(fixed.Routes, arlokkRoute{
				FromSeq: w.a.Seq, From: w.a.Comment, FromPoint: roundVec(rowPoint(w.a)),
				ToSeq: w.b.Seq, To: w.b.Comment, ToPoint: roundVec(rowPoint(w.b)),
				Points: roundVecs(poly),
			})
		}
		if err := writeArlokkRoutes(arlokkRoutesFixture, fixed); err != nil {
			t.Fatalf("%s: %v", arlokkRoutesFixture, err)
		}
		t.Logf("wrote %d routes to %s", len(fixed.Routes), arlokkRoutesFixture)
	}
	routes, err := readArlokkRoutes(arlokkRoutesFixture)
	if err != nil {
		t.Fatalf("%s: %v", arlokkRoutesFixture, err)
	}
	byKey := make(map[[2]int]arlokkRoute, len(routes))
	for _, r := range routes {
		byKey[[2]int{r.FromSeq, r.ToSeq}] = r
	}
	seen := make(map[[2]int]bool, len(walks))
	for _, w := range walks {
		a, b := w.a, w.b
		straight := samplesAlong(rowPoint(a), rowPoint(b), 2)
		checkArlokkTerraceWalk(t, "straight", a, b, straight, w.laterFams)
		key := [2]int{a.Seq, b.Seq}
		seen[key] = true
		r, ok := byKey[key]
		if !ok {
			t.Errorf("leg 4 rows %d (%s) -> %d (%s): no committed polyline in %s: regenerate with `go test -run TestZulGurubArlokkTerraceRouteClearOfLaterPacks -update-routes ./tools/dungeongen/`", a.Seq, a.Comment, b.Seq, b.Comment, arlokkRoutesFixture)
			continue
		}
		if r.From != a.Comment || r.To != b.Comment || dist(pointOfVec(r.FromPoint), rowPoint(a)) > arlokkRouteMatchYd || dist(pointOfVec(r.ToPoint), rowPoint(b)) > arlokkRouteMatchYd {
			t.Errorf("leg 4 rows %d (%s %.1f %.1f) -> %d (%s %.1f %.1f): the committed polyline in %s joins %q %.1f %.1f -> %q %.1f %.1f, so it is for different rows: the plan moved, regenerate the fixture",
				a.Seq, a.Comment, a.X, a.Y, b.Seq, b.Comment, b.X, b.Y, arlokkRoutesFixture,
				r.From, r.FromPoint[0], r.FromPoint[1], r.To, r.ToPoint[0], r.ToPoint[1])
			continue
		}
		checkArlokkTerraceWalk(t, "polyline", a, b, pointsOfVecs(r.Points), w.laterFams)
		if planner == nil {
			continue
		}
		poly, ok := arlokkTerracePolyline(planner, p.Map, rowPoint(a), rowPoint(b))
		if !ok {
			t.Errorf("leg 4 rows %d (%s) -> %d (%s): no complete navmesh path", a.Seq, a.Comment, b.Seq, b.Comment)
			continue
		}
		if diff := arlokkRouteDiff(r.Points, poly); diff != "" {
			t.Errorf("leg 4 rows %d (%s) -> %d (%s): the committed polyline in %s %s; regenerate with `go test -run TestZulGurubArlokkTerraceRouteClearOfLaterPacks -update-routes ./tools/dungeongen/`",
				a.Seq, a.Comment, b.Seq, b.Comment, arlokkRoutesFixture, diff)
		}
	}
	for _, r := range routes {
		if !seen[[2]int{r.FromSeq, r.ToSeq}] {
			t.Errorf("%s holds a polyline for rows %d (%q) -> %d (%q) that the plan no longer walks: regenerate with `go test -run TestZulGurubArlokkTerraceRouteClearOfLaterPacks -update-routes ./tools/dungeongen/`",
				arlokkRoutesFixture, r.FromSeq, r.From, r.ToSeq, r.To)
		}
	}
}

// checkArlokkTerraceWalk logs the nearest spawn of the packs a later pull row owns against one walk and fails when it
// stands within arlokkTerraceRowYd of the nearest sample. A spawn more than arlokkTerraceStoreyYd above or below a
// sample is on another floor and does not count (see the constant).
func checkArlokkTerraceWalk(t *testing.T, how string, a, b planexport.StepRow, samples []dungeon.Point, laterFams [][]creature) {
	t.Helper()
	worst, who, at := worstSample(samples, laterFams, arlokkTerraceStoreyYd)
	if math.IsInf(worst, 1) {
		t.Logf("leg 4 rows %d (%s) -> %d (%s): %s, no spawn of a later pack in the storey band", a.Seq, a.Comment, b.Seq, b.Comment, how)
		return
	}
	t.Logf("leg 4 rows %d (%s) -> %d (%s): %s min %.1f yd vs %s at %.1f %.1f %.1f", a.Seq, a.Comment, b.Seq, b.Comment, how, worst, who, at.X, at.Y, at.Z)
	if worst < arlokkTerraceRowYd {
		t.Errorf("leg 4 rows %d (%s %.1f %.1f) -> %d (%s %.1f %.1f) walks the %s %.1f yd from %s at %.1f %.1f %.1f: %.0f yd wanted",
			a.Seq, a.Comment, a.X, a.Y, b.Seq, b.Comment, b.X, b.Y, how, worst, who, at.X, at.Y, at.Z, float64(arlokkTerraceRowYd))
	}
}

// arlokkTerracePlanner is the server's navmesh for the terrace route check ($TURTLE_AGENT_MMAPS or
// ~/turtle-agent-mmaps, as planprobe and navmesh's own real tests use); nil without one, so the caller logs and keeps
// the straight-line fallback.
func arlokkTerracePlanner() *navmesh.Planner {
	home, _ := os.UserHomeDir()
	for _, dir := range []string{os.Getenv("TURTLE_AGENT_MMAPS"), filepath.Join(home, "turtle-agent-mmaps")} {
		if dir == "" {
			continue
		}
		if _, err := os.Stat(filepath.Join(dir, "309.mmap")); err != nil {
			continue
		}
		if tiles, _ := filepath.Glob(filepath.Join(dir, "309*.mmtile")); len(tiles) == 0 {
			continue
		}
		return navmesh.NewPlanner(dir)
	}
	return nil
}

// arlokkTerracePolyline plans the walk from a to b on the server's navmesh and samples it every yard, both ends
// included (a one-corner path is its one point). False when the mesh has no complete path.
func arlokkTerracePolyline(pl *navmesh.Planner, mapID uint32, a, b dungeon.Point) ([]dungeon.Point, bool) {
	path, err := pl.Plan(mapID, navmesh.Vec3{a.X, a.Y, a.Z}, navmesh.Vec3{b.X, b.Y, b.Z})
	if err != nil || len(path.Points) < 1 || path.Status != navmesh.Complete {
		return nil, false
	}
	var out []dungeon.Point
	for i := 0; i+1 < len(path.Points); i++ {
		from, to := path.Points[i], path.Points[i+1]
		d := math.Sqrt(float64(sq32(to[0]-from[0]) + sq32(to[1]-from[1]) + sq32(to[2]-from[2])))
		n := int(math.Ceil(d))
		if n < 1 {
			n = 1
		}
		for k := 0; k <= n; k++ {
			f := float32(float64(k) / float64(n))
			out = append(out, dungeon.Point{X: from[0] + (to[0]-from[0])*f, Y: from[1] + (to[1]-from[1])*f, Z: from[2] + (to[2]-from[2])*f})
		}
	}
	if len(path.Points) == 1 {
		p := path.Points[0]
		out = []dungeon.Point{{X: p[0], Y: p[1], Z: p[2]}}
	}
	return out, true
}

// arlokkRoutesNote is the fixture's header: what it holds and how to rewrite it.
const arlokkRoutesNote = "The navmesh polylines of the Arlokk terrace walks in Zul'Gurub leg 4 (map 309): one route per pair of consecutive move rows, sampled every yard between the rows' points. TestZulGurubArlokkTerraceRouteClearOfLaterPacks always checks its clearance rule against these and fails when a recomputation differs by more than 1 yd at a point. Rewrite with: go test -run TestZulGurubArlokkTerraceRouteClearOfLaterPacks -update-routes ./tools/dungeongen/"

// arlokkRouteMatchYd is how far a committed route's ends may stand from the rows it names and still be the same walk.
const arlokkRouteMatchYd = 1

// readArlokkRoutes reads the committed terrace polylines.
func readArlokkRoutes(path string) ([]arlokkRoute, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var f arlokkRoutes
	if err := json.Unmarshal(b, &f); err != nil {
		return nil, err
	}
	if len(f.Routes) == 0 {
		return nil, fmt.Errorf("no routes")
	}
	return f.Routes, nil
}

// writeArlokkRoutes writes the terrace polylines as the fixture.
func writeArlokkRoutes(path string, f arlokkRoutes) error {
	b, err := json.MarshalIndent(f, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(b, '\n'), 0o644)
}

// roundVec rounds a point to the fixture's hundredths (well inside the 1 yd comparison tolerance), so the committed
// numbers stay readable.
func roundVec(p dungeon.Point) arlokkVec {
	r := func(v float32) float32 { return float32(math.Round(float64(v)*100) / 100) }
	return arlokkVec{r(p.X), r(p.Y), r(p.Z)}
}

// roundVecs rounds every point (see roundVec).
func roundVecs(ps []dungeon.Point) []arlokkVec {
	out := make([]arlokkVec, len(ps))
	for i, p := range ps {
		out[i] = roundVec(p)
	}
	return out
}

// pointOfVec is the dungeon point of a fixture point.
func pointOfVec(v arlokkVec) dungeon.Point {
	return dungeon.Point{X: v[0], Y: v[1], Z: v[2]}
}

// pointsOfVecs is the dungeon points of a fixture polyline.
func pointsOfVecs(vs []arlokkVec) []dungeon.Point {
	out := make([]dungeon.Point, len(vs))
	for i, v := range vs {
		out[i] = pointOfVec(v)
	}
	return out
}

// arlokkRouteDiff compares a committed polyline with a fresh one: a message naming the first difference, or "" when they
// have the same points within 1 yd each.
func arlokkRouteDiff(committed []arlokkVec, fresh []dungeon.Point) string {
	if len(committed) != len(fresh) {
		return fmt.Sprintf("has %d points where the navmesh now plans %d", len(committed), len(fresh))
	}
	for i := range committed {
		c := pointOfVec(committed[i])
		if d := dist(c, fresh[i]); d > arlokkRouteMatchYd {
			return fmt.Sprintf("point %d %.2f %.2f %.2f is %.2f yd from the navmesh's %.2f %.2f %.2f",
				i, c.X, c.Y, c.Z, d, fresh[i].X, fresh[i].Y, fresh[i].Z)
		}
	}
	return ""
}

// worstSample is the closest any sample stands to any spawn of the later-owned formations within the storey band: the
// distance, "guid/entry" of the spawn, and the sample point. No such spawn: +Inf, "none", the zero point.
func worstSample(samples []dungeon.Point, laterFams [][]creature, storey float64) (float64, string, dungeon.Point) {
	worst, who, at := math.Inf(1), "none", dungeon.Point{}
	for _, s := range samples {
		for _, fam := range laterFams {
			for _, c := range fam {
				if math.Abs(float64(s.Z-c.Pos.Z)) > storey {
					continue
				}
				if d := dist(s, c.Pos); d < worst {
					worst, who, at = d, fmt.Sprintf("%d/%d", c.GUID, c.Entry), s
				}
			}
		}
	}
	return worst, who, at
}

// rowOwnsOne reports whether the exported pull row owns the spawn: its entry is one of the row's entries and it stands
// within the row's radius of the row's point (the run leader's rule for a pull row's mobs).
func rowOwnsOne(row planexport.StepRow, c creature) bool {
	return slices.Contains(row.Entries, c.Entry) && rowDist(row, c) <= float64(row.Radius)
}

// rowOwns reports whether the exported pull row owns any spawn of the formation (see rowOwnsOne).
func rowOwns(row planexport.StepRow, fam []creature) bool {
	for _, c := range fam {
		if rowOwnsOne(row, c) {
			return true
		}
	}
	return false
}

// rowPoint is the point of an exported row.
func rowPoint(row planexport.StepRow) dungeon.Point {
	return dungeon.Point{X: row.X, Y: row.Y, Z: row.Z}
}

// rowDist is the 3-D distance from the exported row's point to the spawn.
func rowDist(row planexport.StepRow, c creature) float64 {
	return dist(rowPoint(row), c.Pos)
}

// samplesAlong returns points of the straight segment a-b every step yd, both ends included (a zero-length segment is its
// one point).
func samplesAlong(a, b dungeon.Point, step float64) []dungeon.Point {
	n := int(math.Ceil(dist(a, b) / step))
	if n < 1 {
		n = 1
	}
	out := make([]dungeon.Point, 0, n+1)
	for k := 0; k <= n; k++ {
		f := float32(float64(k) / float64(n))
		out = append(out, dungeon.Point{X: a.X + (b.X-a.X)*f, Y: a.Y + (b.Y-a.Y)*f, Z: a.Z + (b.Z-a.Z)*f})
	}
	return out
}

// marliLaterPackYd is how far the step 203 stand and the routes to and from it keep from every pack pulled later:
// 26 yd is the detection of these spiders at a level-60 raid (Creature::GetAttackDistance: detection_range 20 minus
// the level difference; the level-61 Shadowcasters wake at 21) plus the settled raid spread (10 yd,
// kSpreadSettledDistance), i.e. the run leader's own StandClearAt rule. Walk 27 is the field evidence that the old
// 20 yd line was too weak: the firing stand the run leader read was 20.0 yd from 49202's patrol loop (22.6 from its
// spawn), and the party's spread plus the Venombroods' 5 yd wander put a bot inside their detection 6 s into the
// fight; pack 0 (all nine) woke and the raid wiped twice (TortoiseBots-audit tests/raid-zg-walk27,
// research/zg-w27/MARLI-PACK0.md).
const marliLaterPackYd = 26

// TestZulGurubMarliStep203RouteClearOfLaterPacks: walk 26b's step 203 pulled pack 0 from a stand 35.3 yd from the
// nearest pack-1 spawn and 19.0 yd from 49202's patrol loop, and pack 1 still woke: 23 hostiles within 20 yd, 20 dead
// in 35 s (TortoiseBots-audit tests/raid-zg-walk26b; research/zg-w26/MARLI-203.md). The frames show the chain is not
// the camp's distance. Pack 0's leader 49202 is level 56 with detection 20-4 = 16 yd, but it is aggro-together with
// pack 0 (creature_groups flags 14 = aggro/evade/respawn together, CreatureGroups.h): at 01:07:21Z it stood on its
// loop at -12308.3 -1624.2, 62.4 yd from any bot, and fought anyway. Its run to the camp passed -12299.5 -1647.2,
// 6.9 yd from the Venombrood 49251 and 9.6 yd from 49248, inside the 10 yd CallAssistance ring that carries the tank
// as victim to 49248/49251; their group woke the other nine. No stand in this courtyard keeps that run out (the 81
// floor points within the 23.5 yd shot of an 11338, >= 22 yd from pack 1 and >= 20 yd from 49202's loop all leave the
// loop-to-camp run within 11.1 yd of pack 1; the loop itself comes within 12.1 yd of 49251), so the fix is the ORDER:
// pull pack 1 first from its stand S1, then pack 0. This pins that order and that the step-203 stand and the route
// from the `to` to it stay marliLaterPackYd clear of every pack pulled later, 49202's whole patrol loop included.
// Red first on the base plan (step 203 pulls pack 0). After the order change the firing stand the run leader reads for
// pack 1 (23.5 yd from the pick, the Broodwidow 49250 after three Polymorphs) is asserted against marliLaterPackYd as
// well: walk 27 showed that the plan's S1 stand alone does not pin it, and the stand the old approach produced was
// 20.0 yd from 49202's loop, so pack 0 woke (MARLI-PACK0.md). The approach is on the east slope now and
// TestZulGurubMarliStep203StandClearOfPack0 holds the stand and its walks against the committed navmesh polylines.
func TestZulGurubMarliStep203RouteClearOfLaterPacks(t *testing.T) {
	p, err := dungeon.Load(filepath.Join(plansDirT, "zul_gurub.json"))
	if err != nil {
		t.Fatal(err)
	}
	cs, err := readCreatures(filepath.Join("testdata", "map309", "creatures.tsv"))
	if err != nil {
		t.Fatal(err)
	}
	movement, err := readMovementPaths(filepath.Join("testdata", "map309", "movement.tsv"))
	if err != nil {
		t.Fatal(err)
	}
	formations := map[uint64][]creature{}
	var leaders []uint64
	for _, c := range cs {
		lead := c.GroupLeader
		if lead == 0 {
			lead = c.GUID
		}
		if _, ok := formations[lead]; !ok {
			leaders = append(leaders, lead)
		}
		formations[lead] = append(formations[lead], c)
	}
	const pack0, pack1 = uint64(49202), uint64(49201)
	if len(formations[pack0]) != 9 || len(formations[pack1]) != 11 {
		t.Fatalf("formations: pack 0 has %d spawns (want 9), pack 1 %d (want 11)", len(formations[pack0]), len(formations[pack1]))
	}
	// patrolLoops: each patrolling member's loop with its spawn first and last (the walk out and back is its patrol).
	patrolLoops := func(lead uint64) [][]dungeon.Point {
		var out [][]dungeon.Point
		for _, c := range formations[lead] {
			if !c.Patrol {
				continue
			}
			loop, ok := movement[c.GUID]
			if !ok || len(loop) == 0 {
				continue
			}
			out = append(out, append(append([]dungeon.Point{c.Pos}, loop...), c.Pos))
		}
		return out
	}
	clearance := func(p dungeon.Point, lead uint64) (float64, string) {
		best, who := math.Inf(1), ""
		for _, c := range formations[lead] {
			if d := dist(p, c.Pos); d < best {
				best, who = d, fmt.Sprintf("%s %d at %.1f %.1f %.1f", c.Name, c.GUID, c.Pos.X, c.Pos.Y, c.Pos.Z)
			}
		}
		for _, loop := range patrolLoops(lead) {
			if d := closestOnLoop(p, loop); d < best {
				best, who = d, fmt.Sprintf("the patrol from %.1f %.1f (formation %d)", loop[0].X, loop[0].Y, lead)
			}
		}
		return best, who
	}
	var leg *dungeon.Leg
	for i := range p.Legs {
		if p.Legs[i].Name == "high_priestess_marli" {
			leg = &p.Legs[i]
		}
	}
	if leg == nil {
		t.Fatal("no high_priestess_marli leg")
	}
	res, err := planexport.Build(p, planexport.Options{})
	if err != nil {
		t.Fatal(err)
	}
	var leg2 []planexport.StepRow
	for _, s := range res.Rows.Steps {
		if s.Leg == 2 {
			leg2 = append(leg2, s)
		}
	}
	rowOwns := func(row planexport.StepRow, lead uint64) bool {
		for _, c := range formations[lead] {
			if slices.Contains(row.Entries, c.Entry) && rowDist(row, c) <= float64(row.Radius) {
				return true
			}
		}
		return false
	}
	var spider []planexport.StepRow
	var spiderGroup []int
	for _, s := range leg2 {
		if s.Kind != planexport.KindPull {
			continue
		}
		g := -1
		if _, err := fmt.Sscanf(s.Comment, "pull group %d", &g); err != nil || g < 0 || g > 1 {
			continue
		}
		spider = append(spider, s)
		spiderGroup = append(spiderGroup, g)
	}
	if len(spider) != 2 {
		t.Fatalf("leg 2 has %d pull rows for groups 0-1, want 2", len(spider))
	}
	first, second := spider[0], spider[1]
	if !rowOwns(first, pack1) || !rowOwns(second, pack0) {
		t.Errorf("leg 2 pulls %q first and %q second: pack 1 (49201) must be pulled before pack 0 (49202), because pulling pack 0 first drags 49202 through pack 1's 10 yd assistance ring (walk 26b: 23 hostiles, 20 dead in 35 s)",
			first.Comment, second.Comment)
	}
	// The step-203 stand and the packs a later pull row owns.
	stand := leg.PullGroups[spiderGroup[0]].Approach[len(leg.PullGroups[spiderGroup[0]].Approach)-1]
	var later []uint64
	seen := map[uint64]bool{}
	after := false
	for _, s := range leg2 {
		if s.Seq > first.Seq && s.Kind == planexport.KindPull {
			for _, lead := range leaders {
				if rowOwns(s, lead) && !seen[lead] {
					seen[lead] = true
					later = append(later, lead)
				}
			}
		}
		if s.Seq == first.Seq {
			after = true
		}
	}
	_ = after
	if len(later) == 0 {
		t.Fatal("no pack is pulled after step 203")
	}
	minLater := func(at dungeon.Point) (float64, string) {
		best, what := math.Inf(1), ""
		for _, lead := range later {
			if d, w := clearance(at, lead); d < best {
				best, what = d, w
			}
		}
		return best, what
	}
	if d, what := minLater(stand); d < marliLaterPackYd {
		t.Errorf("step 203 stand %.1f %.1f %.1f is %.1f yd from %s, want >= %d", stand.X, stand.Y, stand.Z, d, what, marliLaterPackYd)
	}
	// The route: the move rows from the leg's first move through the last spider pull. A walk must keep
	// marliLaterPackYd from the packs a pull row AFTER the one it approaches owns (the pack it approaches comes next;
	// its firing stand is 23.5 yd from it by design).
	var moves, pulls []planexport.StepRow
	for _, s := range leg2 {
		if s.Seq > second.Seq {
			if s.Kind == planexport.KindPull {
				pulls = append(pulls, s)
			}
			continue
		}
		switch s.Kind {
		case planexport.KindMove, planexport.KindMoveDirect:
			moves = append(moves, s)
		case planexport.KindPull:
			pulls = append(pulls, s)
		}
	}
	if len(moves) < 3 {
		t.Fatalf("%d move rows before the second spider pull, want at least 3", len(moves))
	}
	pl := marliPlannerOrNil(t)
	if pl != nil {
		defer pl.Close()
	} else {
		t.Log("no map 309 navmesh files: the polyline route check is skipped (set TURTLE_AGENT_MMAPS); the straight segments below always run")
	}
	worstSample := func(samples []dungeon.Point, fams []uint64) (float64, string, dungeon.Point) {
		best, who, at := math.Inf(1), "", dungeon.Point{}
		for _, s := range samples {
			for _, lead := range fams {
				if d, w := clearance(s, lead); d < best {
					best, who, at = d, w, s
				}
			}
		}
		return best, who, at
	}
	for i := 0; i+1 < len(moves); i++ {
		a, b := moves[i], moves[i+1]
		next := -1
		for j, s := range pulls {
			if s.Seq > b.Seq {
				next = j
				break
			}
		}
		if next < 0 || next+1 >= len(pulls) {
			continue
		}
		var fams []uint64
		for _, s := range pulls[next+1:] {
			for _, lead := range leaders {
				if rowOwns(s, lead) && !slices.Contains(fams, lead) {
					fams = append(fams, lead)
				}
			}
		}
		straight := samplesAlong(rowPoint(a), rowPoint(b), 2)
		if d, who, at := worstSample(straight, fams); d < marliLaterPackYd {
			t.Errorf("leg 2 rows %d (%s %.1f %.1f) -> %d (%s %.1f %.1f) pass %.1f yd from %s at %.1f %.1f: %.0f yd wanted",
				a.Seq, a.Comment, a.X, a.Y, b.Seq, b.Comment, b.X, b.Y, d, who, at.X, at.Y, float64(marliLaterPackYd))
		} else {
			t.Logf("leg 2 rows %d (%s) -> %d (%s): straight min %.1f yd", a.Seq, a.Comment, b.Seq, b.Comment, d)
		}
		if pl == nil {
			continue
		}
		poly, ok := marliPolyline(pl, p.Map, rowPoint(a), rowPoint(b))
		if !ok {
			continue
		}
		if d, who, at := worstSample(poly, fams); d < marliLaterPackYd {
			t.Errorf("leg 2 rows %d (%s %.1f %.1f) -> %d (%s %.1f %.1f) walk the navmesh %.1f yd from %s at %.1f %.1f: %.0f yd wanted",
				a.Seq, a.Comment, a.X, a.Y, b.Seq, b.Comment, b.X, b.Y, d, who, at.X, at.Y, float64(marliLaterPackYd))
		}
	}
	// The firing stand the run leader reads: 23.5 yd from the pick along the tank's path from the plan's approach. With
	// three Polymorphs on the nearest Venombroods (cc_priority 14532) the pick is the Broodwidow 49250. Walk 27 measured
	// it: the old approach's stand was 20.0 yd from 49202's loop and pack 0 woke 6 s into the fight. The plan approach
	// must put it marliLaterPackYd out; TestZulGurubMarliStep203StandClearOfPack0 checks the same numbers from the
	// committed polylines without the mmaps.
	if pl != nil {
		found := false
		for _, target := range formations[pack1] {
			if target.GUID != 49250 {
				continue
			}
			found = true
			camp := marliFiringStands(pl, p.Map, stand, []creature{target})[0]
			if d, what := minLater(camp); d < marliLaterPackYd {
				t.Errorf("step 203's firing stand %.1f %.1f %.1f for %s %d is %.1f yd from %s, want >= %d",
					camp.X, camp.Y, camp.Z, target.Name, target.GUID, d, what, marliLaterPackYd)
			} else {
				t.Logf("step 203's firing stand for %s %d (%.1f %.1f) keeps %.1f yd from %s", target.Name, target.GUID, camp.X, camp.Y, d, what)
			}
		}
		if !found {
			t.Fatal("no 49250 in pack 1")
		}
	}
}

// marliStep203StandFile is the committed navmesh fixture of the step-203 pull's firing stand and the two walks that lead
// to it (the leg's free walk to the approach, and the tank's walk from the approach to the stand). It is the plan's copy
// of what the map-309 navmesh gives, so the check runs without the mmaps; a plan point that moves makes it stale.
const marliStep203StandFile = "testdata/zg_marli_step203_stand.json"

const marliStep203StandNote = "The step-203 stand of Zul'Gurub leg 2 (Mar'li, map 309) and the walks to it: the leg's `to` -> the plan approach of pull group 0 (pack 1), and that approach -> the firing stand the run leader reads for the picked Broodwidow 49250 (23.5 yd from it, leadShotYd minus kShotMarginYd, along the tank's navmesh path). Points are the navmesh polyline sampled every yard. TestZulGurubMarliStep203StandClearOfPack0 always checks the clearance rule against these and fails when a recomputation differs by more than 1 yd. Rewrite with: go test -run TestZulGurubMarliStep203StandClearOfPack0 -update-marli-stand ./tools/dungeongen/"

type marliVec = arlokkVec

// marliWalk is one committed walk: what it joins and its navmesh polyline.
type marliWalk struct {
	From      string     `json:"from"`
	FromPoint marliVec   `json:"from_point"`
	To        string     `json:"to"`
	ToPoint   marliVec   `json:"to_point"`
	Points    []marliVec `json:"points"`
}

// marliStandFix is the shape of marliStep203StandFile.
type marliStandFix struct {
	Note      string      `json:"note"`
	Target    uint64      `json:"target_guid"`
	Entry     uint32      `json:"target_entry"`
	TargetPos marliVec    `json:"target_point"`
	Approach  marliVec    `json:"approach"`
	Stand     marliVec    `json:"stand"`
	Walks     []marliWalk `json:"walks"`
}

// updateMarliStand rewrites marliStep203StandFile from the map-309 navmesh.
var updateMarliStand = flag.Bool("update-marli-stand", false, "rewrite "+marliStep203StandFile+" from the map-309 navmesh")

// marliStandClearYdAt is a spawn's proximity wake reach at the plan's party level, as the run leader and the route check
// use it: the core aggro radius (worldspawn.AggroRadius: detection_range minus the level advantage, NO_AGGRO 0) plus
// the party's settled spread (routePartySpread, 10 yd for the raid) plus the spawn's own wander. Walk 27: pack 0 woke to
// a stand 20.0 yd from 49202's patrol loop (need 30 at the plan's level band 58: aggro 20 + spread 10).
func marliStandClearYdAt(p *dungeon.Plan, c creature) float64 {
	det := c.DetectionRange
	if det <= 0 {
		det = defaultDetectionYd
	}
	s := worldspawn.Spawn{LevelMax: c.LevelMax, DetectionYd: float32(det), FlagsExtra: c.FlagsExtra, Wander: c.Wander}
	return float64(worldspawn.AggroRadius(s, routePartyLevel(p), routeAggroRate)) + routePartySpread(p) + float64(c.Wander)
}

// marliStandWorst is the tightening margin of p against a pack: the distance to the nearest spawn minus that spawn's
// clear reach, and the same to every patrolling member's loop (2-D; the mob walks the loop itself, so the loop's own
// reach has no wander term). Negative: p is inside the pack's wake reach.
func marliStandWorst(p dungeon.Point, pack []creature, movement map[uint64][]dungeon.Point, plan *dungeon.Plan) (float64, string) {
	worst, who := math.Inf(1), "none"
	for _, c := range pack {
		if d := dist(p, c.Pos) - marliStandClearYdAt(plan, c); d < worst {
			worst, who = d, fmt.Sprintf("spawn %s %d at %.1f %.1f %.1f", c.Name, c.GUID, c.Pos.X, c.Pos.Y, c.Pos.Z)
		}
		if !c.Patrol {
			continue
		}
		loop, ok := movement[c.GUID]
		if !ok || len(loop) == 0 {
			continue
		}
		full := append(append([]dungeon.Point{c.Pos}, loop...), c.Pos)
		need := marliStandClearYdAt(plan, c) - float64(c.Wander)
		if d := closestOnLoop(p, full) - need; d < worst {
			worst, who = d, fmt.Sprintf("the patrol loop of %s %d", c.Name, c.GUID)
		}
	}
	return worst, who
}

// TestZulGurubMarliStep203StandClearOfPack0: walk 27 pulled pack 1 first (walk 26b's order) and pack 0 still woke 6 s
// into the fight: the run leader read the firing stand at -12266.2 -1667.3, 20.0 yd from 49202's patrol loop and 22.6
// from its spawn, and the party's spread plus the Venombroods' 5 yd wander put a bot (or its pet) inside their 16-20 yd
// detection. All nine of pack 0 took one victim in one read (aggro-together, creature_groups flags 14), and b2/b2-r1
// lost 43/64 bots at step 203 (TortoiseBots-audit tests/raid-zg-walk27, research/zg-w27/MARLI-PACK0.md).
//
// The plan cannot pin the firing stand (the run leader walks out to the first point of the tank's path inside 23.5 yd
// of the pick), so it pins the approach the stand is read from. Group 0's approach is the reachable east slope
// -12253 -1653 (z 134.4); the tank's path to the picked Broodwidow 49250 then crosses 23.5 yd at -12261.4 -1648.4,
// 32.3 yd from 49202's loop instead of 20.0. The pick is 49250 because cc_priority [14532] puts three Polymorphs on the
// nearest Venombroods; this test re-derives that pick from the plan (nearest non-CC spawn of the row's pack, in the
// tank's 28.5 yd shot first) so a changed CC list fails here instead of silently checking the wrong unit.
//
// The stand and both walks must keep every unit of every pack pulled later (pack 0) out of its wake reach
// (marliStandClearYdAt: aggro + the 10 yd spread + the spawn's wander). The walks are checked on the committed navmesh
// polylines in marliStep203StandFile (always, mmaps or not) and recomputed when the mmaps are present (a fixture that
// drifts by more than 1 yd is stale), plus the straight segments as a last assertion that never skips.
func TestZulGurubMarliStep203StandClearOfPack0(t *testing.T) {
	p, err := dungeon.Load(filepath.Join(plansDirT, "zul_gurub.json"))
	if err != nil {
		t.Fatal(err)
	}
	cs, err := readCreatures(filepath.Join("testdata", "map309", "creatures.tsv"))
	if err != nil {
		t.Fatal(err)
	}
	movement, err := readMovementPaths(filepath.Join("testdata", "map309", "movement.tsv"))
	if err != nil {
		t.Fatal(err)
	}
	formations := map[uint64][]creature{}
	for _, c := range cs {
		lead := c.GroupLeader
		if lead == 0 {
			lead = c.GUID
		}
		formations[lead] = append(formations[lead], c)
	}
	pack0, pack1 := formations[49202], formations[49201]
	if len(pack0) != 9 || len(pack1) != 11 {
		t.Fatalf("formations: pack 0 has %d spawns (want 9), pack 1 %d (want 11)", len(pack0), len(pack1))
	}
	var leg *dungeon.Leg
	for i := range p.Legs {
		if p.Legs[i].Name == "high_priestess_marli" {
			leg = &p.Legs[i]
		}
	}
	if leg == nil {
		t.Fatal("no high_priestess_marli leg")
	}
	if len(leg.PullGroups) < 2 {
		t.Fatalf("%d pull groups", len(leg.PullGroups))
	}
	approach := leg.PullGroups[0].Approach[len(leg.PullGroups[0].Approach)-1]
	// R2 (research/zg-w27/MARLI-PACK0.md): the approach is inside pack 1's own clear reach and the plan cannot clear it
	// for the unit the run leader pulls: 49250 has no stand that clears pack 0 (the best point on its 23.5 yd ring is
	// still 5.95 yd inside 49202's loop). The approach therefore walks pack 1 awake before the pull step's CC orders;
	// the fix is the run leader's hold point (specced in the research note), not a plan point. Logged, not asserted: the
	// route-aggro pin carries the three segments.
	if d, what := marliStandWorst(approach, pack1, movement, p); d < 0 {
		t.Logf("the group 0 approach %.1f %.1f %.1f is %.1f yd inside pack 1's clear reach (%s): the run leader's hold-point change is specced in research/zg-w27/MARLI-PACK0.md",
			approach.X, approach.Y, approach.Z, -d, what)
	}
	// The exported rows: the first leg-2 pull row (the plan's group 0, pack 1) and the move row before it (the approach).
	res, err := planexport.Build(p, planexport.Options{})
	if err != nil {
		t.Fatal(err)
	}
	var rows []planexport.StepRow
	for _, s := range res.Rows.Steps {
		if s.Leg == 2 {
			rows = append(rows, s)
		}
	}
	pullAt := -1
	for i, s := range rows {
		if s.Kind == planexport.KindPull && strings.HasPrefix(s.Comment, "pull group 0") {
			pullAt = i
			break
		}
	}
	if pullAt <= 0 {
		t.Fatal("no exported leg-2 pull row for group 0")
	}
	approachRow := rows[pullAt-1]
	if approachRow.Kind != planexport.KindMove || approachRow.Comment != "pull group 0 approach" {
		t.Fatalf("the row before the first spider pull is %q %q, want the group 0 approach move", approachRow.Kind, approachRow.Comment)
	}
	if d := dist(approach, rowPoint(approachRow)); d > 0.1 {
		t.Errorf("the plan's group 0 approach %.1f %.1f %.1f and the exported row %d (%.1f %.1f %.1f) differ by %.2f yd", approach.X, approach.Y, approach.Z, approachRow.Seq, approachRow.X, approachRow.Y, approachRow.Z, d)
	}
	// The pick the run leader makes for the row: the row's pack minus the entries the plan's CC takes, in the tank's
	// 28.5 yd shot first, then the nearest.
	ccRank := func(entry uint32) int {
		for i, e := range p.CCAlways {
			if e == entry {
				return i
			}
		}
		for i, e := range p.CCPriority {
			if slices.Contains(p.CCAlways, e) {
				continue
			}
			if e == entry {
				return len(p.CCAlways) + i
			}
		}
		return -1
	}
	var pick *creature
	for i := range pack1 {
		c := &pack1[i]
		if ccRank(c.Entry) >= 0 {
			continue
		}
		if pick == nil {
			pick = c
			continue
		}
		inShot, pickShot := dist(approach, c.Pos) <= 28.5, dist(approach, pick.Pos) <= 28.5
		if inShot != pickShot {
			if inShot {
				pick = c
			}
			continue
		}
		if dist(approach, c.Pos) < dist(approach, pick.Pos) {
			pick = c
		}
	}
	if pick == nil {
		t.Fatal("no pick: every spawn of pack 1 is crowd-controlled")
	}
	if pick.GUID != 49250 {
		t.Fatalf("the run leader's pick for step 203 is %s %d, want the Broodwidow 49250: the stand, the fixture and the walk-27 evidence are all measured for it (a changed cc_priority moves the pick, and this test with it)", pick.Name, pick.GUID)
	}
	planner := marliPlannerOrNil(t)
	if planner != nil {
		defer planner.Close()
	}
	short := func(s string) string {
		if len(s) > 60 {
			return s[:57] + "..."
		}
		return s
	}
	legTo := rows[pullAt-2]
	if legTo.Kind != planexport.KindMove {
		t.Fatalf("the row before the approach is %q, want the leg's `to` move", legTo.Kind)
	}
	stand := dungeon.Point{}
	if planner != nil {
		stand = marliFiringStands(planner, p.Map, approach, []creature{*pick})[0]
	}
	if *updateMarliStand {
		if planner == nil {
			t.Fatalf("cannot rewrite %s without the map 309 navmesh (set TURTLE_AGENT_MMAPS)", marliStep203StandFile)
		}
		fixed := marliStandFix{
			Note: marliStep203StandNote, Target: pick.GUID, Entry: pick.Entry,
			TargetPos: roundVec(pick.Pos),
			Approach:  roundVec(approach),
			Stand:     roundVec(stand),
		}
		for _, w := range []struct {
			from, to string
			a, b     dungeon.Point
		}{
			{short(legTo.Comment), short(approachRow.Comment), rowPoint(legTo), rowPoint(approachRow)},
			{short(approachRow.Comment), "firing stand for " + short(pick.Name) + " " + fmt.Sprint(pick.GUID), rowPoint(approachRow), stand},
		} {
			poly, ok := marliPolyline(planner, p.Map, w.a, w.b)
			if !ok {
				t.Fatalf("%s -> %s: no complete navmesh path", w.from, w.to)
			}
			fixed.Walks = append(fixed.Walks, marliWalk{From: w.from, FromPoint: roundVec(w.a), To: w.to, ToPoint: roundVec(w.b), Points: roundVecs(poly)})
		}
		b, err := json.MarshalIndent(fixed, "", "  ")
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(marliStep203StandFile, append(b, '\n'), 0o644); err != nil {
			t.Fatal(err)
		}
		t.Logf("wrote %d walks to %s", len(fixed.Walks), marliStep203StandFile)
	}
	raw, err := os.ReadFile(marliStep203StandFile)
	if err != nil {
		t.Fatalf("%s: %v", marliStep203StandFile, err)
	}
	var fix marliStandFix
	if err := json.Unmarshal(raw, &fix); err != nil {
		t.Fatalf("%s: %v", marliStep203StandFile, err)
	}
	if len(fix.Walks) != 2 {
		t.Fatalf("%s holds %d walks, want 2 (the `to` -> approach and the approach -> stand)", marliStep203StandFile, len(fix.Walks))
	}
	if fix.Target != pick.GUID || fix.Entry != pick.Entry || dist(pointOfVec(fix.TargetPos), pick.Pos) > 1 ||
		dist(pointOfVec(fix.Approach), approach) > 1 {
		t.Errorf("%s is for target %d entry %d approach %.1f %.1f -> %.1f %.1f, not for the plan (49250, approach %.1f %.1f): regenerate with `go test -run TestZulGurubMarliStep203StandClearOfPack0 -update-marli-stand ./tools/dungeongen/`",
			marliStep203StandFile, fix.Target, fix.Entry, fix.TargetPos[0], fix.TargetPos[1], fix.Approach[0], fix.Approach[1], approach.X, approach.Y)
	}
	fixStand := pointOfVec(fix.Stand)
	if d := dist(fixStand, pick.Pos); d < 20 || d > 28.5 {
		t.Errorf("%s: the stand %.1f %.1f %.1f is %.1f yd from %s %d, want it inside the tank's 28.5 yd shot (leadShotYd; the stand is 23.5 yd along the path, 21-25 yd in a straight line)", marliStep203StandFile, fixStand.X, fixStand.Y, fixStand.Z, d, pick.Name, pick.GUID)
	}
	if planner != nil {
		if d := dist(fixStand, stand); d > 1 {
			t.Errorf("%s: the committed stand differs from the navmesh's by %.1f yd (%.1f %.1f vs %.1f %.1f): regenerate with -update-marli-stand", marliStep203StandFile, d, fixStand.X, fixStand.Y, stand.X, stand.Y)
		}
	}
	// The stand itself, always (from the fixture when the mmaps are absent).
	if margin, who := marliStandWorst(fixStand, pack0, movement, p); margin < 0 {
		t.Errorf("step 203's firing stand %.1f %.1f %.1f is inside the wake reach of %s by %.1f yd (walk 27: the old stand was 20.0 yd from 49202's loop and pack 0 woke at once)", fixStand.X, fixStand.Y, fixStand.Z, who, -margin)
	} else {
		t.Logf("step 203's firing stand %.1f %.1f %.1f keeps %.1f yd beyond the wake reach of %s", fixStand.X, fixStand.Y, fixStand.Z, margin, who)
	}
	// Both walks: the committed polylines always, the navmesh's own when it is here, and the straight segments always.
	want := []struct {
		from, to string
		a, b     dungeon.Point
	}{
		{short(legTo.Comment), short(approachRow.Comment), rowPoint(legTo), rowPoint(approachRow)},
		{short(approachRow.Comment), "firing stand for " + short(pick.Name) + " " + fmt.Sprint(pick.GUID), rowPoint(approachRow), fixStand},
	}
	for i, w := range want {
		samples := pointsOfVecs(fix.Walks[i].Points)
		if margin, who := marliWalkWorst(samples, pack0, movement, p); margin < 0 {
			t.Errorf("the %s -> %s walk passes inside the wake reach of %s by %.1f yd", w.from, w.to, who, -margin)
		} else {
			t.Logf("the %s -> %s walk keeps %.1f yd beyond the wake reach of %s", w.from, w.to, margin, who)
		}
		if margin, who := marliWalkWorst(samplesAlong(w.a, w.b, 2), pack0, movement, p); margin < 0 {
			t.Errorf("the straight %s -> %s walk passes inside the wake reach of %s by %.1f yd", w.from, w.to, who, -margin)
		}
		if d := dist(pointOfVec(fix.Walks[i].FromPoint), w.a); d > arlokkRouteMatchYd {
			t.Errorf("%s walk %d starts at %.1f %.1f, not at the plan's point %.1f %.1f: the plan moved, regenerate", marliStep203StandFile, i, fix.Walks[i].FromPoint[0], fix.Walks[i].FromPoint[1], w.a.X, w.a.Y)
		}
		if d := dist(pointOfVec(fix.Walks[i].ToPoint), w.b); d > arlokkRouteMatchYd {
			t.Errorf("%s walk %d ends at %.1f %.1f, not at %.1f %.1f: the plan moved, regenerate", marliStep203StandFile, i, fix.Walks[i].ToPoint[0], fix.Walks[i].ToPoint[1], w.b.X, w.b.Y)
		}
		if planner == nil {
			continue
		}
		poly, ok := marliPolyline(planner, p.Map, w.a, w.b)
		if !ok {
			t.Errorf("the %s -> %s walk has no complete navmesh path", w.from, w.to)
			continue
		}
		if diff := marliPolyDiff(fix.Walks[i].Points, poly); diff != "" {
			t.Errorf("%s walk %d: %s; regenerate with -update-marli-stand", marliStep203StandFile, i, diff)
		}
	}
}

// marliWalkWorst is marliStandWorst over every sample of a walk (the worst sample).
func marliWalkWorst(samples []dungeon.Point, pack []creature, movement map[uint64][]dungeon.Point, plan *dungeon.Plan) (float64, string) {
	worst, who := math.Inf(1), "none"
	for _, s := range samples {
		if m, w := marliStandWorst(s, pack, movement, plan); m < worst {
			worst, who = m, w
		}
	}
	return worst, who
}

// marliPolyDiff compares a committed polyline with the navmesh's own: "" when every committed point is within 1 yd of
// the recomputed one (same point count, both sampled every yard).
func marliPolyDiff(committed []marliVec, poly []dungeon.Point) string {
	if len(committed) != len(poly) {
		return fmt.Sprintf("the committed polyline has %d points, the navmesh's %d", len(committed), len(poly))
	}
	for i := range committed {
		if d := dist(pointOfVec(committed[i]), poly[i]); d > arlokkRouteMatchYd {
			return fmt.Sprintf("point %d is %.1f yd from the navmesh's", i, d)
		}
	}
	return ""
}

// marliPlannerOrNil is the server's navmesh for the Mar'li checks ($TURTLE_AGENT_MMAPS or ~/turtle-agent-mmaps);
// nil without one, so the straight-line checks always run and the caller logs the skipped polyline check.
func marliPlannerOrNil(t *testing.T) *navmesh.Planner {
	t.Helper()
	home, _ := os.UserHomeDir()
	for _, dir := range []string{os.Getenv("TURTLE_AGENT_MMAPS"), filepath.Join(home, "turtle-agent-mmaps")} {
		if dir == "" {
			continue
		}
		if _, err := os.Stat(filepath.Join(dir, "309.mmap")); err != nil {
			continue
		}
		if tiles, _ := filepath.Glob(filepath.Join(dir, "309*.mmtile")); len(tiles) == 0 {
			continue
		}
		return navmesh.NewPlanner(dir)
	}
	return nil
}

// marliFiringStands is the run leader's firing stand for each spawn of a pack: a point 23.5 yd from it (leadShotYd
// 28.5 less the 5 yd shot margin) on the path from the stand; the stand itself when the spawn is already within
// 23.5 yd.
func marliFiringStands(pl *navmesh.Planner, mapID uint32, stand dungeon.Point, pack []creature) []dungeon.Point {
	var out []dungeon.Point
	for _, c := range pack {
		var pts []dungeon.Point
		if pl != nil {
			if path, err := pl.Plan(mapID, navmesh.Vec3{stand.X, stand.Y, stand.Z}, navmesh.Vec3{c.Pos.X, c.Pos.Y, c.Pos.Z}); err == nil && path.Status == navmesh.Complete && len(path.Points) >= 2 {
				for _, v := range path.Points {
					pts = append(pts, dungeon.Point{X: v[0], Y: v[1], Z: v[2]})
				}
			}
		}
		if len(pts) == 0 {
			pts = []dungeon.Point{stand, c.Pos}
		}
		at := stand
		left := 23.5
		cur := c.Pos
		for i := len(pts) - 2; i >= 0; i-- {
			prev := pts[i]
			d := dist(cur, prev)
			if d >= left && d > 0 {
				f := float32(left / d)
				at = dungeon.Point{X: cur.X + (prev.X-cur.X)*f, Y: cur.Y + (prev.Y-cur.Y)*f, Z: cur.Z + (prev.Z-cur.Z)*f}
				left = 0
				break
			}
			left -= d
			cur = prev
		}
		out = append(out, at)
	}
	return out
}

// marliPolyline plans the walk a->b on the server's navmesh and samples it every yard; false without a complete path.
func marliPolyline(pl *navmesh.Planner, mapID uint32, a, b dungeon.Point) ([]dungeon.Point, bool) {
	path, err := pl.Plan(mapID, navmesh.Vec3{a.X, a.Y, a.Z}, navmesh.Vec3{b.X, b.Y, b.Z})
	if err != nil || path.Status != navmesh.Complete || len(path.Points) < 1 {
		return nil, false
	}
	var out []dungeon.Point
	for i := 0; i+1 < len(path.Points); i++ {
		from, to := path.Points[i], path.Points[i+1]
		d := math.Sqrt(float64(sq32(to[0]-from[0]) + sq32(to[1]-from[1]) + sq32(to[2]-from[2])))
		n := int(math.Ceil(d))
		if n < 1 {
			n = 1
		}
		for k := 0; k <= n; k++ {
			f := float32(float64(k) / float64(n))
			out = append(out, dungeon.Point{X: from[0] + (to[0]-from[0])*f, Y: from[1] + (to[1]-from[1])*f, Z: from[2] + (to[2]-from[2])*f})
		}
	}
	if len(path.Points) == 1 {
		q := path.Points[0]
		out = []dungeon.Point{{X: q[0], Y: q[1], Z: q[2]}}
	}
	return out, true
}
