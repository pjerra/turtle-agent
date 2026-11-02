package main

// The Mandokir and Jin'do legs after walk 28 (issue #394): the ring sweep of the Mandokir terrace and the Jin'do corridor's
// first fifteen pulls. Walk 28 wiped at step 431 (20 dead) and step 531 (21 dead) because the approach walks ran through
// the pack field: the plan's WALK 29 note has the frames' numbers and TortoiseBots-audit research/zg-w28/MANDOKIR-JINDO.md
// the analysis. These tests pin the pull order, the stands (19.5-28 yd from their own pack, >= 20 yd from every pack
// pulled later) and the exported rows' navmesh walks (a committed polyline fixture, recomputed where the server's mmaps
// are on the machine).

import (
	"encoding/json"
	"flag"
	"fmt"
	"math"
	"os"
	"testing"

	"github.com/pjerra/turtle-agent/internal/dungeon"
	"github.com/pjerra/turtle-agent/internal/navmesh"
	"github.com/pjerra/turtle-agent/internal/planexport"
)

const (
	zgLaterYd    = 20.0 // a stand and a walk keep this far from every pack pulled later (3D yards)
	zgOwnMinYd   = 19.5 // ... and a stand this far from its own pack (the party stays outside its detection)
	zgOwnMaxYd   = 28.0 // ... and no farther (the tank's shot reaches the nearest unit)
	zgCentreYd   = 50.0 // ... and within the run leader's reach of the row's point (dungeon.RunLeaderReachYd)
	zgSampleStep = 2.0  // the straight segments' sampling
	zgFixture    = "testdata/zg_mandokir_jindo_routes.json"
)

var zgUpdateRoutes = flag.Bool("update-zg-routes", false, "rewrite "+zgFixture+" from the map-309 navmesh")

// zgFormation is a set that comes as one: every hostile spawn of a group_leader, the leader included. Map 309 has no
// creature_linking rows (links.tsv is a header), so the world's links are the creature_groups alone.
type zgFormation struct {
	Lead   uint64
	Member []creature
}

func zgHostile(c creature) bool { return c.Faction != 35 && c.LevelMax > 1 }

func zgFormations(cs []creature) map[uint64]*zgFormation {
	out := map[uint64]*zgFormation{}
	for _, c := range cs {
		if !zgHostile(c) {
			continue
		}
		lead := c.GroupLeader
		if lead == 0 {
			lead = c.GUID
		}
		f := out[lead]
		if f == nil {
			f = &zgFormation{Lead: lead}
			out[lead] = f
		}
		f.Member = append(f.Member, c)
	}
	return out
}

// zgPack is the pack a plan group pulls: the spawn nearest each of its positions, expanded to the whole formation.
func zgPack(g dungeon.PullGroup, cs []creature, forms map[uint64]*zgFormation) ([]creature, error) {
	var leads []uint64
	for _, pos := range g.Positions {
		var best creature
		bd := math.Inf(1)
		for _, c := range cs {
			if d := dist(pos, c.Pos); d < bd {
				bd, best = d, c
			}
		}
		if bd > 0.7 {
			return nil, fmt.Errorf("position %.1f %.1f %.1f: nearest spawn %.2f yd away (%s %d)", pos.X, pos.Y, pos.Z, bd, best.Name, best.Entry)
		}
		lead := best.GroupLeader
		if lead == 0 {
			lead = best.GUID
		}
		found := false
		for _, l := range leads {
			if l == lead {
				found = true
			}
		}
		if !found {
			leads = append(leads, lead)
		}
	}
	var out []creature
	seen := map[uint64]bool{}
	for _, l := range leads {
		if f := forms[l]; f != nil {
			for _, m := range f.Member {
				if !seen[m.GUID] {
					seen[m.GUID] = true
					out = append(out, m)
				}
			}
		}
	}
	return out, nil
}

func zgPacks(t *testing.T, leg dungeon.Leg, cs []creature) [][]creature {
	t.Helper()
	forms := zgFormations(cs)
	out := make([][]creature, len(leg.PullGroups))
	for gi, g := range leg.PullGroups {
		p, err := zgPack(g, cs, forms)
		if err != nil {
			t.Fatalf("pull group %d: %v", gi, err)
		}
		out[gi] = p
	}
	return out
}

// zgLeaders is the formation leaders of a pack, sorted, for the order pins.
func zgLeaders(p []creature) []uint64 {
	var out []uint64
	for _, c := range p {
		lead := c.GroupLeader
		if lead == 0 {
			lead = c.GUID
		}
		found := false
		for _, l := range out {
			if l == lead {
				found = true
			}
		}
		if !found {
			out = append(out, lead)
		}
	}
	for i := 0; i < len(out); i++ {
		for j := i + 1; j < len(out); j++ {
			if out[j] < out[i] {
				out[i], out[j] = out[j], out[i]
			}
		}
	}
	return out
}

func zgSameLeaders(got, want []uint64) bool {
	if len(got) != len(want) {
		return false
	}
	for i := range got {
		if got[i] != want[i] {
			return false
		}
	}
	return true
}

func zgMin3(p dungeon.Point, cs []creature) (float64, creature) {
	best := math.Inf(1)
	var who creature
	for _, c := range cs {
		if d := dist(p, c.Pos); d < best {
			best, who = d, c
		}
	}
	return best, who
}

func zgMinSegment(a, b dungeon.Point, cs []creature) (float64, dungeon.Point) {
	best, at := math.Inf(1), dungeon.Point{}
	for _, s := range samplesAlong(a, b, zgSampleStep) {
		if d, _ := zgMin3(s, cs); d < best {
			best, at = d, s
		}
	}
	return best, at
}

func zgLater(packs [][]creature, gi int) []creature {
	var out []creature
	for i := gi + 1; i < len(packs); i++ {
		out = append(out, packs[i]...)
	}
	return out
}

// zgCheckStands asserts the stand rules from the first pull on: each group has a stand 19.5-28 yd from its own pack and
// within 50 yd of its row's point, and the walk from the previous stand stays >= 19 yd from its own pack and >= 20 yd
// from every pack pulled later. skipFirst leaves the first group to its leg's own `to` (the party arrives there).
func zgCheckStands(t *testing.T, legName string, leg dungeon.Leg, packs [][]creature, skipFirst bool, lastGroup int) {
	t.Helper()
	prev := dungeon.Point{}
	for gi, g := range leg.PullGroups {
		if lastGroup >= 0 && gi > lastGroup {
			break
		}
		if skipFirst && gi == 0 {
			if len(g.Approach) > 0 {
				prev = g.Approach[len(g.Approach)-1]
			} else {
				prev = g.Positions[0]
			}
			continue
		}
		if len(g.Approach) == 0 {
			t.Errorf("%s pull group %d (%d units): no stand", legName, gi, len(packs[gi]))
			continue
		}
		stand := g.Approach[len(g.Approach)-1]
		own, who := zgMin3(stand, packs[gi])
		if own < zgOwnMinYd || own > zgOwnMaxYd {
			t.Errorf("%s pull group %d: stand %.1f %.1f %.1f is %.1f yd from %s %d, want %g-%g", legName, gi, stand.X, stand.Y, stand.Z, own, who.Name, who.Entry, zgOwnMinYd, zgOwnMaxYd)
		}
		var cen dungeon.Point
		n := float32(len(g.Positions))
		for _, q := range g.Positions {
			cen.X += q.X
			cen.Y += q.Y
			cen.Z += q.Z
		}
		cen.X /= n
		cen.Y /= n
		cen.Z /= n
		if d := dist(cen, stand); d > zgCentreYd {
			t.Errorf("%s pull group %d: stand is %.1f yd from the row's point, want <= %g", legName, gi, d, zgCentreYd)
		}
		if d, at := zgMinSegment(prev, stand, packs[gi]); d < 19 {
			t.Errorf("%s pull group %d: the walk from the previous stand passes %.1f yd from its own pack at %.1f %.1f", legName, gi, d, at.X, at.Y)
		}
		if d, at := zgMinSegment(prev, stand, zgLater(packs, gi)); d < zgLaterYd {
			t.Errorf("%s pull group %d: the walk from the previous stand passes %.1f yd from a later pack at %.1f %.1f, want >= %g", legName, gi, d, at.X, at.Y, zgLaterYd)
		}
		prev = stand
	}
}

// zgParty is one party position of the exported rows.
type zgParty struct {
	Seq   int
	Label string
	P     dungeon.Point
	Group int // the leg group the row pulls (-1: a move)
	Turn  bool
}

// zgParties builds the plan's exported rows of one leg (the stands are explicit, so nothing is cut) and the party
// positions in row order.
func zgParties(t *testing.T, p *dungeon.Plan, legNo int) []zgParty {
	t.Helper()
	res, err := planexport.Build(p, planexport.Options{})
	if err != nil {
		t.Fatalf("plan-export: %v", err)
	}
	leg := p.Legs[legNo]
	var out []zgParty
	for _, s := range res.Rows.Steps {
		if s.Leg != legNo {
			continue
		}
		label := s.Kind
		if s.Comment != "" {
			label += " " + s.Comment
		}
		switch s.Kind {
		case planexport.KindMove, planexport.KindMoveDirect, planexport.KindMoveHopExact:
			out = append(out, zgParty{Seq: s.Seq, Label: label, P: dungeon.Point{X: s.X, Y: s.Y, Z: s.Z}, Group: -1})
		case planexport.KindPull, planexport.KindBoss, planexport.KindHold:
			at := dungeon.Point{X: s.X, Y: s.Y, Z: s.Z}
			gi := -1
			if n := zgPullGroupIndex(s.Comment); n >= 0 && n < len(leg.PullGroups) {
				gi = n
				if ap := leg.PullGroups[n].Approach; len(ap) > 0 {
					at = ap[len(ap)-1]
				}
			} else if s.Kind == planexport.KindBoss && leg.Boss != nil && len(leg.Boss.Approach) > 0 {
				at = leg.Boss.Approach[len(leg.Boss.Approach)-1]
			}
			out = append(out, zgParty{Seq: s.Seq, Label: label, P: at, Group: gi, Turn: true})
		}
	}
	return out
}

func zgPullGroupIndex(comment string) int {
	var gi int
	if _, err := fmt.Sscanf(comment, "pull group %d", &gi); err != nil {
		return -1
	}
	return gi
}

// zgWalk is one tested walk: the leg's party positions in order and the index of the last point checked.
type zgWalk struct {
	Name    string
	Parties []zgParty
	Last    int
}

// zgLaterOf is what the segment ending at party point i must keep clear of: every group not pulled at or before i, less
// the group the next pull approaches (its stand is 19.5-28 yd from it by design) and the group whose own pull point is i.
func zgLaterOf(w zgWalk, packs [][]creature, i int) []creature {
	next := -1
	for j := i + 1; j <= w.Last && j < len(w.Parties); j++ {
		if w.Parties[j].Turn {
			next = w.Parties[j].Group
			break
		}
	}
	pulled := map[int]bool{}
	for j := 0; j <= i && j < len(w.Parties); j++ {
		if w.Parties[j].Turn && w.Parties[j].Group >= 0 {
			pulled[w.Parties[j].Group] = true
		}
	}
	var out []creature
	for gi := range packs {
		if gi == next || pulled[gi] {
			continue
		}
		out = append(out, packs[gi]...)
	}
	return out
}

type zgVec [3]float32

type zgRoute struct {
	Walk    string  `json:"walk"`
	FromSeq int     `json:"from_seq"`
	From    string  `json:"from"`
	ToSeq   int     `json:"to_seq"`
	To      string  `json:"to"`
	FromPt  zgVec   `json:"from_point"`
	ToPt    zgVec   `json:"to_point"`
	Points  []zgVec `json:"points"`
}

type zgRoutes struct {
	Note   string    `json:"note"`
	Routes []zgRoute `json:"routes"`
}

func zgLoadRoutes(t *testing.T) zgRoutes {
	t.Helper()
	b, err := os.ReadFile(zgFixture)
	if err != nil {
		return zgRoutes{Note: "not written yet: run with -update-zg-routes and the map-309 mmaps"}
	}
	var r zgRoutes
	if err := json.Unmarshal(b, &r); err != nil {
		t.Fatalf("%s: %v", zgFixture, err)
	}
	return r
}

// zgPolyline is the navmesh walk a->b sampled every yard, as the fixture stores it.
func zgPolyline(pl *navmesh.Planner, p *dungeon.Plan, a, b dungeon.Point) ([]dungeon.Point, error) {
	path, err := pl.Plan(p.Map, navmesh.Vec3{a.X, a.Y, a.Z}, navmesh.Vec3{b.X, b.Y, b.Z})
	if err != nil {
		return nil, err
	}
	if path.Status != navmesh.Complete || len(path.Points) == 0 {
		return nil, fmt.Errorf("no complete path (%s %s)", path.Status, path.Reason)
	}
	var out []dungeon.Point
	for j := 0; j+1 < len(path.Points); j++ {
		u0, u1 := path.Points[j], path.Points[j+1]
		n := int(math.Ceil(math.Sqrt(float64(sq32(u1[0]-u0[0])+sq32(u1[1]-u0[1])+sq32(u1[2]-u0[2]))) / 1))
		if n < 1 {
			n = 1
		}
		for k := 0; k <= n; k++ {
			f := float32(k) / float32(n)
			out = append(out, dungeon.Point{X: u0[0] + (u1[0]-u0[0])*f, Y: u0[1] + (u1[1]-u0[1])*f, Z: u0[2] + (u1[2]-u0[2])*f})
		}
	}
	return out, nil
}

// zgWriteRoutes rewrites the walk's routes in the fixture from the map-309 navmesh (needs the mmaps).
func zgWriteRoutes(t *testing.T, p *dungeon.Plan, pl *navmesh.Planner, w zgWalk) {
	t.Helper()
	// the other walk's routes of the same file are kept: only this walk's are replaced
	old := zgLoadRoutes(t)
	var out []zgRoute
	for _, r := range old.Routes {
		if r.Walk != w.Name {
			out = append(out, r)
		}
	}
	for i := 0; i < w.Last && i+1 < len(w.Parties); i++ {
		a, b := w.Parties[i], w.Parties[i+1]
		pts, err := zgPolyline(pl, p, a.P, b.P)
		if err != nil {
			t.Fatalf("%s rows %d -> %d: %v", w.Name, a.Seq, b.Seq, err)
		}
		vecs := make([]zgVec, 0, len(pts))
		for _, q := range pts {
			vecs = append(vecs, zgVec{q.X, q.Y, q.Z})
		}
		out = append(out, zgRoute{Walk: w.Name, FromSeq: a.Seq, From: a.Label, ToSeq: b.Seq, To: b.Label,
			FromPt: zgVec{a.P.X, a.P.Y, a.P.Z}, ToPt: zgVec{b.P.X, b.P.Y, b.P.Z}, Points: vecs})
	}
	fixture := zgRoutes{Note: "Navmesh polylines of the Mandokir and Jin'do walk-28 fix (issue #394), sampled every yard. Regenerate with: go test -run 'TestZulGurubMandokirSweepClearOfLaterPacks|TestZulGurubJindoCorridorClearOfLaterPacks' -update-zg-routes ./tools/dungeongen/ (needs the map-309 mmaps)."}
	fixture.Routes = out
	b, err := json.MarshalIndent(fixture, "", " ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(zgFixture, append(b, '\n'), 0o644); err != nil {
		t.Fatal(err)
	}
}

// zgCheckWalk checks the exported rows' walk: every consecutive pair's straight line and committed navmesh polyline stays
// >= 20 yd from every pack pulled later (the pack the row approaches excluded), and, with the mmaps, the navmesh walk is
// the fixture's walk to within a yard.
func zgCheckWalk(t *testing.T, p *dungeon.Plan, pl *navmesh.Planner, w zgWalk, packs [][]creature) {
	t.Helper()
	fixture := zgLoadRoutes(t)
	for i := 0; i < w.Last && i+1 < len(w.Parties); i++ {
		a, b := w.Parties[i], w.Parties[i+1]
		later := zgLaterOf(w, packs, i)
		if len(later) > 0 {
			if d, at := zgMinSegment(a.P, b.P, later); d < zgLaterYd {
				t.Errorf("%s rows %d (%s) -> %d (%s): the straight walk passes %.1f yd from a later pack at %.1f %.1f, want >= %g",
					w.Name, a.Seq, a.Label, b.Seq, b.Label, d, at.X, at.Y, zgLaterYd)
			}
		}
		var fix *zgRoute
		for k := range fixture.Routes {
			if fixture.Routes[k].Walk == w.Name && fixture.Routes[k].FromSeq == a.Seq && fixture.Routes[k].ToSeq == b.Seq {
				fix = &fixture.Routes[k]
				break
			}
		}
		if fix == nil {
			t.Errorf("%s rows %d -> %d: no committed navmesh route; regenerate with -update-zg-routes and the map-309 mmaps", w.Name, a.Seq, b.Seq)
			continue
		}
		if fix.From != a.Label || fix.To != b.Label || fix.FromPt != (zgVec{a.P.X, a.P.Y, a.P.Z}) || fix.ToPt != (zgVec{b.P.X, b.P.Y, b.P.Z}) {
			t.Errorf("%s rows %d -> %d: the committed route names %q -> %q, the plan has %q -> %q; regenerate with -update-zg-routes",
				w.Name, a.Seq, b.Seq, fix.From, fix.To, a.Label, b.Label)
		}
		samples := make([]dungeon.Point, 0, len(fix.Points))
		for _, q := range fix.Points {
			samples = append(samples, dungeon.Point{X: q[0], Y: q[1], Z: q[2]})
		}
		if len(later) > 0 {
			best, at := math.Inf(1), dungeon.Point{}
			var who creature
			for _, s := range samples {
				if d, c := zgMin3(s, later); d < best {
					best, at, who = d, s, c
				}
			}
			if best < zgLaterYd {
				t.Errorf("%s rows %d (%s) -> %d (%s): the navmesh walk passes %.1f yd from a later pack at %.1f %.1f %.1f (%s %d), want >= %g",
					w.Name, a.Seq, a.Label, b.Seq, b.Label, best, at.X, at.Y, at.Z, who.Name, who.Entry, zgLaterYd)
			}
		}
		if pl == nil {
			continue
		}
		now, err := zgPolyline(pl, p, a.P, b.P)
		if err != nil {
			t.Errorf("%s rows %d -> %d: %v", w.Name, a.Seq, b.Seq, err)
			continue
		}
		worst := 0.0
		for _, s := range now {
			best := math.Inf(1)
			for _, q := range samples {
				if d := dist(s, q); d < best {
					best = d
				}
			}
			if best > worst {
				worst = best
			}
		}
		if worst > 1 {
			t.Errorf("%s rows %d -> %d: the committed navmesh route is %.1f yd from the plan's own walk; regenerate with -update-zg-routes and the map-309 mmaps",
				w.Name, a.Seq, b.Seq, worst)
		}
	}
}

const zgPlan = "../../dungeons/zul_gurub.json"

func zgLoadPlanAndExport(t *testing.T) (*dungeon.Plan, []creature) {
	t.Helper()
	p, err := dungeon.Load(zgPlan)
	if err != nil {
		t.Fatal(err)
	}
	cs, err := readCreatures("testdata/map309/creatures.tsv")
	if err != nil {
		t.Fatal(err)
	}
	return p, cs
}

// TestZulGurubMandokirSweepClearOfLaterPacks: walk 28's wipe at step 431 was a 135 yd exporter-cut approach through the
// terrace's packs; the leg now sweeps the room from the ramp top, each pack from a stand out of every later pack's aggro.
func TestZulGurubMandokirSweepClearOfLaterPacks(t *testing.T) {
	p, cs := zgLoadPlanAndExport(t)
	leg := p.Legs[5]
	packs := zgPacks(t, leg, cs)
	zgCheckStands(t, "bloodlord_mandokir", leg, packs, true, -1)
	// the leg's own pull order, by formation leader sets
	got := make([][]uint64, len(packs))
	for i := range packs {
		got[i] = zgLeaders(packs[i])
	}
	wantOrder := [][]uint64{
		{91493},
		{49776},
		{49778},
		{51460},
		{49705, 51985, 51986},
		{49272},
		{49259, 49282},
		{49261},
		{49788},
		{49781},
		{49268, 49269, 51573, 51574},
		{49277, 51970, 51971, 51972},
		{51456},
		{49789},
		{51570},
		{49793},
		{49274},
		{49797},
		{49288},
		{49270},
		{51453},
	}
	for i := range wantOrder {
		if !zgSameLeaders(got[i], wantOrder[i]) {
			t.Errorf("pull group %d has formations %v, want %v (the ring sweep of WALK 29)", i, got[i], wantOrder[i])
		}
	}
	parties := zgParties(t, p, 5)
	w := zgWalk{Name: "leg5 bloodlord_mandokir", Parties: parties, Last: len(parties) - 1}
	pl := marliPlannerOrNil(t)
	if pl != nil {
		defer pl.Close()
	}
	if *zgUpdateRoutes {
		if pl == nil {
			t.Fatal("-update-zg-routes needs the map-309 mmaps")
		}
		zgWriteRoutes(t, p, pl, w)
	}
	zgCheckWalk(t, p, pl, w, packs)
}

// TestZulGurubJindoCorridorClearOfLaterPacks covers the Jin'do corridor through the group-14 pull (the Venombroods at the
// south hall's entry): the two packs that straddle the walk, the `to` moved off 49114's spawn, the swept north corridor,
// the 1173080 split and the 49421 group's radius. The south hall's pulls 15-20 keep their existing stands (not touched).
func TestZulGurubJindoCorridorClearOfLaterPacks(t *testing.T) {
	p, cs := zgLoadPlanAndExport(t)
	leg := p.Legs[6]
	packs := zgPacks(t, leg, cs)
	zgCheckStands(t, "jindo_the_hexxer", leg, packs, true, 14)
	// the order: the corridor packs 49741 and 49119 first, then 49114 and west, up to 49387-96's pack (group 13)
	want := [][]uint64{
		{49741},                   // 0 (moved here from the Hakkar leg)
		{49118, 49119},            // 1 (the 4 Razzashi spawns that straddle the walk)
		{49114}, {49747}, {51948}, // 2, 3, 4
		{49397, 49398, 49399, 49400}, // 5
		{49373, 49374, 49375, 49376, 49377, 49378, 49379, 49380, 49381, 49382}, // 6
		{1173082}, {49363}, {49383, 49384, 49385, 49386}, // 7, 8, 9
		{1173080}, {49402, 49403, 49421}, {49078, 49079, 49080, 49081, 49082, 49083, 49084, 49085, 49086, 49087}, // 10, 11, 12
		{49387, 49388, 49389, 49390, 49391, 49392, 49393, 49394, 49395, 49396}, // 13
	}
	if len(packs) < len(want) {
		t.Fatalf("%d pull groups, want at least %d", len(packs), len(want))
	}
	for i := range want {
		if got := zgLeaders(packs[i]); !zgSameLeaders(got, want[i]) {
			t.Errorf("pull group %d has formations %v, want %v (WALK 29's corridor sweep)", i, got, want[i])
		}
	}
	// the leg's `to` is the first stand and no via stands in a pack: >= 20 yd from every spawning cluster of the leg
	var all []creature
	for _, pk := range packs {
		all = append(all, pk...)
	}
	for _, v := range leg.Via {
		if d, who := zgMin3(v.Point, all); d < zgLaterYd {
			t.Errorf("via %.1f %.1f %.1f is %.1f yd from %s %d, want >= %g", v.X, v.Y, v.Z, d, who.Name, who.Entry, zgLaterYd)
		}
	}
	if len(leg.PullGroups[0].Approach) == 0 {
		t.Fatalf("pull group 0 has no stand: the leg's `to` must be its stand")
	}
	to := leg.PullGroups[0].Approach[len(leg.PullGroups[0].Approach)-1]
	if dist(leg.To, to) > 0.01 {
		t.Errorf("the leg's `to` %.1f %.1f %.1f is not the first stand %.1f %.1f %.1f", leg.To.X, leg.To.Y, leg.To.Z, to.X, to.Y, to.Z)
	}
	// the 49421 group's circle must not reach Jin'do or another group's spawns
	g := leg.PullGroups[11]
	for _, pk := range packs[11] {
		var cen dungeon.Point
		n := float32(len(packs[11]))
		for _, c := range packs[11] {
			cen.X += c.Pos.X
			cen.Y += c.Pos.Y
			cen.Z += c.Pos.Z
		}
		cen.X /= n
		cen.Y /= n
		cen.Z /= n
		if d := dist(cen, pk.Pos); d > float64(g.Radius)+3 {
			t.Errorf("pull group 11 is %.1f yd from its own %s %d, radius %g + 3", d, pk.Name, pk.Entry, g.Radius)
		}
		_ = pk
	}
	if boss := leg.Boss; boss != nil {
		var cen dungeon.Point
		n := float32(len(packs[11]))
		for _, c := range packs[11] {
			cen.X += c.Pos.X
			cen.Y += c.Pos.Y
			cen.Z += c.Pos.Z
		}
		cen.X /= n
		cen.Y /= n
		cen.Z /= n
		if d := dist(cen, boss.Position); d <= float64(g.Radius)+3 {
			t.Errorf("pull group 11's circle %.1f yd from Jin'do at %.1f %.1f %.1f: the pull would wake him (radius %g + 3)", d, boss.Position.X, boss.Position.Y, boss.Position.Z, g.Radius)
		}
	}
	parties := zgParties(t, p, 6)
	last := -1
	for i, q := range parties {
		if q.Turn && q.Group == 14 { // the group-14 pull (51875: the Venombroods) closes the corridor
			last = i
			break
		}
	}
	if last < 0 {
		t.Fatal("no pull row for group 14")
	}
	w := zgWalk{Name: "leg6 jindo corridor", Parties: parties, Last: last}
	pl := marliPlannerOrNil(t)
	if pl != nil {
		defer pl.Close()
	}
	if *zgUpdateRoutes {
		if pl == nil {
			t.Fatal("-update-zg-routes needs the map-309 mmaps")
		}
		zgWriteRoutes(t, p, pl, w)
	}
	zgCheckWalk(t, p, pl, w, packs)
}
