package main

import (
	"bytes"
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
	// (b) raid clearance: S0 (pack 0's approach point) clears every STATIC unit of pack 0 and every unit of pack 1,
	// and S1 (pack 1's stand) clears pack 1. Both rows have an approach.
	stands := make([]dungeon.Point, 2)
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
	check(0, append(append([]creature{}, static0...), pack1...), marliStandYd)
	// The patroller 49202's own SPAWN is the one exception to marliStandYd for S0: the patroller belongs to the very
	// pack group 0's plain pull wakes (the pull walks the gathered party to a firing stand in the pack's shot), so its
	// spawn is inside the pack this row pulls by design, and only has to stay out of the band that woke it in walk 25.
	for _, c := range pack0 {
		if c.Patrol {
			if d := dist(stands[0], c.Pos); d < marliPatrollerSpawnYd {
				t.Errorf("pull_groups[0] approach %.1f %.1f %.1f is %.1f yd from the patroller %s %d's spawn, want >= %d", stands[0].X, stands[0].Y, stands[0].Z, d, c.Name, c.GUID, marliPatrollerSpawnYd)
			}
		}
	}
	check(1, pack1, marliStandYd)
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
	// (d) the kinds of the two rows (SPEC-1-AMEND): pack 0 is a plain pull from S0, pack 1 keeps the stand pull.
	if leg.PullGroups[0].StandPull {
		t.Errorf("pull_groups[0] is a stand pull: its leader 49202 walks a loop that never comes within the gate's 27.5 yd of S0, so the gate would wait 600 s and pause the run; the pack is a plain pull from the stand")
	}
	if !leg.PullGroups[1].StandPull {
		t.Errorf("pull_groups[1] is not a stand pull: it keeps one for its patrol clearance (a stand pull carries the row's clear_yd in its param)")
	}
	// (e) the exporter's own --approach-hops cut must not put a row of leg 2 inside pack 0's aggro before group 0's
	// pull row. With the old S0 -12245.333 -1685.3334 the hop to the row centre -12300.49 -1685.68 was 55.16 yd, past
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
func marliExportCut(pl *navmesh.Planner) func(uint32, dungeon.Point, dungeon.Point) ([]dungeon.Point, error) {
	return func(mapID uint32, from, to dungeon.Point) ([]dungeon.Point, error) {
		path, err := pl.Plan(mapID, navmesh.Vec3{from.X, from.Y, from.Z}, navmesh.Vec3{to.X, to.Y, to.Z})
		if err != nil {
			return nil, err
		}
		if path.Status != navmesh.Complete || len(path.Points) < 2 {
			return nil, fmt.Errorf("no complete path (%s %s)", path.Status, path.Reason)
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
		return via, nil
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
