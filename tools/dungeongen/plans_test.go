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
	// the pack-1 group's stand clears pack 1. Both rows have an approach.
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
	check(g1, pack1, marliStandYd)
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

// arlokkTerraceRowYd is how near a walk's straight segment may pass a pack that a later pull row owns: the mobs detect at
// 20 yd (the map-309 spawns' detection_range), so a sample inside that band wakes the pack before its pull row.
const arlokkTerraceRowYd = 20

// TestZulGurubArlokkTerraceRouteClearOfLaterPacks: walk 25's Arlokk leg walked the raid onto the three-level terrace in the
// plan's group order 11, 12, 13, ... and the move from group 11's approach to group 12's crossed the spawn ring of group 15's
// four Shadow Hunters 11339 at z 71.4 (13.3 yd from 51354), which woke groups 12, 14, 15 and 16 at once: 3 of 4 attempts
// wiped, the MT idle under 11 elites (TortoiseBots-audit tests/raid-zg-walk25/summary-autofix.md "Arlokk"). The leg pulls
// the upper level first now (11 -> 15 -> 14 -> 17) and each stand sits on its pack's own level, clear of every pack pulled
// later. The test walks the exported leg-4 rows from group 11's approach through the last terrace pull: for every pair of
// consecutive move rows, every spawn of a formation that a later pull row owns stays arlokkTerraceRowYd out of the straight
// segment sampled every 2 yd. "Later" leaves out the pack the walk is approaching: its own pull row comes after the move, and
// its firing stand is 12-25 yd from it by design (the group-15 stand, D2, is 21.9 yd from 51354). Group 13's row must own its
// linked Blood Drinkers 51594/51595 (leader 49343) as well: walk 25's radius 12 stopped 14.4-15.3 yd short of them, so the
// pack's four Panthers came alone and the two Drinkers joined group 14's fight.
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
	// The terrace is plan leg 4's pull groups 11-17 (the array indices the plan and the export's comments name): from the
	// first group's approach row through the last group's pull row.
	first, last := -1, -1
	for i, s := range leg4 {
		if strings.HasPrefix(s.Comment, "pull group 11 approach") {
			first = i
		}
		if s.Comment == "pull group 17" {
			last = i
		}
	}
	if first < 0 || last <= first {
		t.Fatalf("no terrace rows in the exported leg 4 (group 11 approach at %d, group 17 pull at %d)", first, last)
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
	if len(moves) < 6 {
		t.Fatalf("%d move rows in the terrace, want the seven groups' approaches", len(moves))
	}
	// D4: the row that owns group 13's pack owns its linked Blood Drinkers too (51594/51595, leader 49343 = the pack's
	// first Panther). Walk 25's radius 12 owned the four Panthers only.
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
			t.Errorf("the pull row %.0f %.0f %.0f (radius %.1f, entries %v) does not own the Blood Drinker %d (%s, entry %d) at %.1f %.1f %.1f: %.1f yd out",
				pulls[row13].X, pulls[row13].Y, pulls[row13].Z, pulls[row13].Radius, pulls[row13].Entries, guid, c.Name, c.Entry, c.Pos.X, c.Pos.Y, c.Pos.Z, rowDist(pulls[row13], c))
		}
	}
	// Every pair of consecutive move rows: the packs a later pull row owns stay arlokkTerraceRowYd out of the straight walk.
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
		samples := samplesAlong(rowPoint(a), rowPoint(b), 2)
		for _, later := range pulls[next+1:] {
			for _, lead := range leaders {
				fam := formations[lead]
				if !rowOwns(later, fam) {
					continue
				}
				for _, c := range fam {
					worst, at := math.Inf(1), dungeon.Point{}
					for _, s := range samples {
						if d := dist(s, c.Pos); d < worst {
							worst, at = d, s
						}
					}
					if worst < arlokkTerraceRowYd {
						t.Errorf("leg 4 rows %d (%s %.1f %.1f) -> %d (%s %.1f %.1f) passes %.1f yd from %s %d (entry %d) at %.1f %.1f %.1f, owned by row %d (%s): %.0f yd wanted",
							a.Seq, a.Comment, a.X, a.Y, b.Seq, b.Comment, b.X, b.Y, worst, c.Name, c.GUID, c.Entry, at.X, at.Y, at.Z, later.Seq, later.Comment, float64(arlokkTerraceRowYd))
					}
				}
			}
		}
	}
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
// 20 yd is the detection of these spiders (Creature::GetAttackDistance: detection_range 20 minus the level
// difference; packs 0-1 are level 56-61 against a level-60 raid). The walk-26b wake happened at 6.9-9.6 yd from
// 49202's dragged path, well inside the 10 yd family-assistance ring (CreatureFamilyAssistanceRadius).
const marliLaterPackYd = 20

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
// pack 1 (23.5 yd from the target, which three Polymorphs push onto the Broodwidow 49250) is 20.0 yd from 49202's
// loop: it is logged below as the validation walk's watch item, not asserted, because it is the run leader's choice
// and not a plan point.
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
	// Watch item, logged only: the firing stand the run leader reads for pack 1 is 23.5 yd from its pull target; with
	// three Polymorphs on the nearest Venombroods the target is the Broodwidow 49250 (walk 26b pulled 11370 guid
	// ...c062 at step 204). Its stand's clearance to 49202's loop is the number the validation walk must watch.
	if pl != nil {
		var nonCC []creature
		for _, c := range formations[pack1] {
			if c.Entry != 14532 {
				nonCC = append(nonCC, c)
			}
		}
		for _, target := range nonCC {
			camp := marliFiringStands(pl, p.Map, stand, []creature{target})[0]
			d, what := clearance(camp, pack0)
			t.Logf("watch: step 203's firing stand for %s %d (%.1f %.1f) is %.1f yd from %s, want >= %d (the spread round the camp closes it)", target.Name, target.GUID, camp.X, camp.Y, d, what, marliLaterPackYd)
		}
	}
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

// samplesAlong returns the points of the straight segment a-b every step yd, both ends included.

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

// rowPoint and rowDist are an exported row's point and its 3-D distance to a spawn.
