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
	"path/filepath"
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
	// zgPatrolSpreadYd is added to a patrolling unit's clearance: it walks its whole loop while unpulled, so a stand or
	// walk sample must clear every waypoint by the detection range plus the spread of the points it was measured at.
	zgPatrolSpreadYd = 3.0
	zgFixture        = "testdata/zg_mandokir_jindo_routes.json"
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
	movement, err := zgReadMovementPaths("testdata/map309/movement.tsv")
	if err != nil {
		t.Fatalf("movement.tsv: %v", err)
	}
	out := make([][]creature, len(leg.PullGroups))
	for gi, g := range leg.PullGroups {
		p, err := zgPack(g, cs, forms)
		if err != nil {
			t.Fatalf("pull group %d: %v", gi, err)
		}
		if zgLonePatrol(p) {
			p = zgExpandPatrols(p, movement)
		}
		out[gi] = p
	}
	return out
}

// zgLonePatrol reports whether a pack is one patrolling unit: it has no static footprint then, and its whole loop is
// where it stands, so a stand or a walk sample must clear every waypoint. A patroller merged into a multi-unit pack is
// one unit of that pack: the plan pulls the pack at its row, and a loop that crosses the route is a separate finding
// (clearing it would reorder the leg or split the pack).
func zgLonePatrol(pack []creature) bool {
	return len(pack) == 1 && pack[0].Patrol
}

// zgExpandPatrols replaces each patrolling member of a pack by its spawn and every waypoint of its loop (movement.tsv):
// the whole path is a danger point while the pack is unpulled, since the creature walks it. A patrolling spawn with no
// rows keeps its spawn alone.
func zgExpandPatrols(pack []creature, movement map[uint64][]dungeon.Point) []creature {
	out := make([]creature, 0, len(pack))
	for _, c := range pack {
		if !c.Patrol {
			out = append(out, c)
			continue
		}
		for _, at := range append([]dungeon.Point{c.Pos}, movement[c.GUID]...) {
			q := c
			q.Pos = at
			q.patrolPoint = true
			out = append(out, q)
		}
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

func zgSampleMin(samples []dungeon.Point, cs []creature) (float64, dungeon.Point, creature) {
	best, at := math.Inf(1), dungeon.Point{}
	var who creature
	for _, s := range samples {
		if d, c := zgMin3(s, cs); d < best {
			best, at, who = d, s, c
		}
	}
	return best, at, who
}

// zgSplit separates a pack's static spawns from the waypoints of a lone patroller's loop.
func zgSplit(cs []creature) (static, patrol []creature) {
	for _, c := range cs {
		if c.patrolPoint {
			patrol = append(patrol, c)
		} else {
			static = append(static, c)
		}
	}
	return static, patrol
}

// zgClearSamples asserts that every sample keeps staticYd from a static spawn and patrolYd from every waypoint of an
// unpulled patrol (20 yd detection + 3 yd spread on these legs).
func zgClearSamples(t *testing.T, label string, samples []dungeon.Point, cs []creature, staticYd, patrolYd float64) {
	t.Helper()
	static, patrol := zgSplit(cs)
	if d, at, who := zgSampleMin(samples, static); d < staticYd {
		t.Errorf("%s passes %.1f yd from %s %d at %.1f %.1f, want >= %g", label, d, who.Name, who.Entry, at.X, at.Y, staticYd)
	}
	if d, at, who := zgSampleMin(samples, patrol); d < patrolYd {
		t.Errorf("%s passes %.1f yd from a waypoint of %s %d's patrol (at %.1f %.1f %.1f), want >= %g", label, d, who.Name, who.Entry, at.X, at.Y, at.Z, patrolYd)
	}
}

func zgLater(packs [][]creature, gi int) []creature {
	var out []creature
	for i := gi + 1; i < len(packs); i++ {
		out = append(out, packs[i]...)
	}
	return out
}

// zgCheckStands asserts the stand rules from the first pull on: each plain pull has a stand 19.5-28 yd from its own pack
// (a stand_pull's last approach is a 30+ yd staging point, 19.5-50 yd), within 50 yd of its row's point (of its nearest
// patrol waypoint for a patrol row), and the walk from the previous stand stays >= 19 yd from its own pack and >= 20 yd
// from every pack pulled later. A patrolling unit's whole loop counts: every waypoint of an unpulled patrol stays
// >= 23 yd (20 yd detection + 3 yd spread) from a stand or walk sample, since it walks the loop while the party waits.
// skipFirst leaves the first group to its leg's own `to` (the party arrives there).
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
			// the party stages at the first stand: a lone patroller's stand must still reach it (19.5-28 yd to the
			// closest waypoint, the tank's shot) and keep the detection range plus spread from every other waypoint
			if _, patrol := zgSplit(packs[gi]); len(patrol) > 0 {
				own, who := zgMin3(prev, packs[gi])
				if own < zgOwnMinYd || own > zgOwnMaxYd {
					t.Errorf("%s pull group %d: stand %.1f %.1f %.1f is %.1f yd from %s %d's patrol, want %g-%g", legName, gi, prev.X, prev.Y, prev.Z, own, who.Name, who.Entry, zgOwnMinYd, zgOwnMaxYd)
				}
				for _, c := range patrol {
					if d := dist(prev, c.Pos); d < zgLaterYd+zgPatrolSpreadYd {
						t.Errorf("%s pull group %d: stand %.1f %.1f %.1f is %.1f yd from a waypoint %.1f %.1f %.1f of %s %d's patrol, want >= %g", legName, gi, prev.X, prev.Y, prev.Z, d, c.Pos.X, c.Pos.Y, c.Pos.Z, c.Name, c.Entry, zgLaterYd+zgPatrolSpreadYd)
					}
				}
			}
			continue
		}
		if len(g.Approach) == 0 {
			t.Errorf("%s pull group %d (%d units): no stand", legName, gi, len(packs[gi]))
			continue
		}
		stand := g.Approach[len(g.Approach)-1]
		// A stand_pull's last approach is the staging point (30+ yd from its own pack, walk 29's fix): the run leader
		// reads its own firing stand from there, so the tank's 28.5 yd shot is not the plan-side bound. A plain pull's
		// approach is its firing stand and keeps the 19.5-28 yd band.
		maxOwn := float64(zgOwnMaxYd)
		if g.StandPull {
			maxOwn = zgCentreYd
		}
		own, who := zgMin3(stand, packs[gi])
		if own < zgOwnMinYd || own > maxOwn {
			t.Errorf("%s pull group %d: stand %.1f %.1f %.1f is %.1f yd from %s %d, want %g-%g", legName, gi, stand.X, stand.Y, stand.Z, own, who.Name, who.Entry, zgOwnMinYd, maxOwn)
		}
		// a patrol-only pack walks its whole loop while unpulled: the stand keeps the detection range plus the spread
		// from every waypoint, so the party is not woken before the pull (the pull reaches it at the nearest one)
		if _, patrol := zgSplit(packs[gi]); len(patrol) > 0 {
			for _, c := range patrol {
				if d := dist(stand, c.Pos); d < zgLaterYd+zgPatrolSpreadYd {
					t.Errorf("%s pull group %d: stand %.1f %.1f %.1f is %.1f yd from a waypoint %.1f %.1f %.1f of %s %d's patrol, want >= %g", legName, gi, stand.X, stand.Y, stand.Z, d, c.Pos.X, c.Pos.Y, c.Pos.Z, c.Name, c.Entry, zgLaterYd+zgPatrolSpreadYd)
				}
			}
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
		// A patrol row's reach is to its loop, not its spawn (the run leader waits at the stand for a waypoint to come
		// into shot): walk 29's 49705 row stands at the ramp top, 118 yd from the spawn and 23.6 yd from the loop.
		if g.Patrol != nil && len(g.Patrol.Path) > 0 {
			best := math.Inf(1)
			for _, q := range g.Patrol.Path {
				if d := dist(q, stand); d < best {
					best, cen = d, q
				}
			}
		}
		if d := dist(cen, stand); d > zgCentreYd {
			t.Errorf("%s pull group %d: stand is %.1f yd from the row's point, want <= %g", legName, gi, d, zgCentreYd)
		}
		walk := zgSamplesAlong(prev, stand, zgSampleStep)
		zgClearSamples(t, fmt.Sprintf("%s pull group %d: the walk from the previous stand (own pack)", legName, gi), walk, packs[gi], 19, zgLaterYd+zgPatrolSpreadYd)
		zgClearSamples(t, fmt.Sprintf("%s pull group %d: the walk from the previous stand", legName, gi), walk, zgLater(packs, gi), zgLaterYd, zgLaterYd+zgPatrolSpreadYd)
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
		n := int(math.Ceil(math.Sqrt(float64(zgSq32(u1[0]-u0[0])+zgSq32(u1[1]-u0[1])+zgSq32(u1[2]-u0[2]))) / 1))
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
	fixture := zgRoutes{Note: "Navmesh polylines of the Mandokir and Jin'do walk-28 fix (issue #394), sampled every yard. Regenerate with: go test -run 'TestZulGurubMandokirSweepClearOfLaterPacks|TestZulGurubJindoCorridorClearOfLaterPacks' ./tools/dungeongen/ -update-zg-routes (needs the map-309 mmaps)."}
	fixture.Routes = out
	b, err := json.MarshalIndent(fixture, "", " ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(zgFixture, append(b, '\n'), 0o644); err != nil {
		t.Fatal(err)
	}
}

// zgNextPull is the pull a walk from party point i approaches: the first pull row after i (its group and stand). A
// move row between them is part of the approach.
func zgNextPull(w zgWalk, i int) (int, dungeon.Point, bool) {
	for j := i + 1; j <= w.Last && j < len(w.Parties); j++ {
		if w.Parties[j].Turn {
			return w.Parties[j].Group, w.Parties[j].P, true
		}
	}
	return -1, dungeon.Point{}, false
}

// zgApproachMin is the least distance from a walk's samples to the pack it approaches. When the walk ends at the stand,
// the samples within the last yard of walk before it are left out (the stand sits 19.5-28 yd from its own pack by
// design).
func zgApproachMin(samples []dungeon.Point, pack []creature, endAtStand bool) (float64, dungeon.Point, creature, bool) {
	best, ok := math.Inf(1), false
	var at dungeon.Point
	var who creature
	rem := 0.0 // the walk's length from sample k to its end
	for k := len(samples) - 1; k >= 0; k-- {
		if k < len(samples)-1 {
			rem += dist(samples[k], samples[k+1])
		}
		if endAtStand && rem < 1 {
			continue
		}
		if d, c := zgMin3(samples[k], pack); d < best {
			best, at, who, ok = d, samples[k], c, true
		}
	}
	return best, at, who, ok
}

// zgCheckWalk checks the exported rows' walk: every consecutive pair's straight line and committed navmesh polyline stays
// >= 20 yd from every pack pulled later (the pack the row approaches excluded), >= 23 yd from every waypoint of an
// unpulled patrol, and, with the mmaps, the navmesh walk is the fixture's walk to within a yard.
func zgCheckWalk(t *testing.T, p *dungeon.Plan, pl *navmesh.Planner, w zgWalk, packs [][]creature) {
	t.Helper()
	fixture := zgLoadRoutes(t)
	for i := 0; i < w.Last && i+1 < len(w.Parties); i++ {
		a, b := w.Parties[i], w.Parties[i+1]
		later := zgLaterOf(w, packs, i)
		if len(later) > 0 {
			zgClearSamples(t, fmt.Sprintf("%s rows %d (%s) -> %d (%s): the straight walk", w.Name, a.Seq, a.Label, b.Seq, b.Label), zgSamplesAlong(a.P, b.P, zgSampleStep), later, zgLaterYd, zgLaterYd+zgPatrolSpreadYd)
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
		// a walk must not walk into the pack the next pull approaches before its stand: the navmesh polyline stays
		// >= zgLaterYd (23 for a patrol's waypoints) from every unit of that pack, except the last yard before the
		// stand (19.5-28 yd by design)
		// A roaming pull row (a patrol block, no stand_pull) keeps its party point at its DB spawn: the walk from it
		// to the next stand starts inside the terrace's pack field, and no reachable mesh point there is 20 yd from
		// the next pack (the 49705 row's spawn is west of the 11-pull pack, its stand north-east; the planner's
		// points between them all pass within 12 yd of the pack). The walk's later-pull clearance is checked below.
		roaming := false
		for k := i; k >= 0; k-- {
			if !w.Parties[k].Turn {
				continue
			}
			if g0i := w.Parties[k].Group; g0i >= 0 && g0i < len(p.Legs[5].PullGroups) {
				g0 := p.Legs[5].PullGroups[g0i]
				roaming = g0.Patrol != nil && !g0.StandPull
			}
			break
		}
		if g, stand, ok := zgNextPull(w, i); ok && g >= 0 && g < len(packs) && !roaming {
			atStand := dist(b.P, stand) < 0.01
			static, patrol := zgSplit(packs[g])
			if d, at, who, found := zgApproachMin(samples, static, atStand); found && d < zgLaterYd {
				t.Errorf("%s rows %d (%s) -> %d (%s): the navmesh walk passes %.1f yd from the pack of pull group %d at %.1f %.1f %.1f (%s %d), want >= %g before the stand",
					w.Name, a.Seq, a.Label, b.Seq, b.Label, d, g, at.X, at.Y, at.Z, who.Name, who.Entry, zgLaterYd)
			}
			if d, at, who, found := zgApproachMin(samples, patrol, atStand); found && d < zgLaterYd+zgPatrolSpreadYd {
				t.Errorf("%s rows %d (%s) -> %d (%s): the navmesh walk passes %.1f yd from a waypoint of %s %d's patrol in pull group %d (at %.1f %.1f %.1f), want >= %g before the stand",
					w.Name, a.Seq, a.Label, b.Seq, b.Label, d, who.Name, who.Entry, g, at.X, at.Y, at.Z, zgLaterYd+zgPatrolSpreadYd)
			}
		}
		if len(later) > 0 {
			zgClearSamples(t, fmt.Sprintf("%s rows %d (%s) -> %d (%s): the navmesh walk", w.Name, a.Seq, a.Label, b.Seq, b.Label), samples, later, zgLaterYd, zgLaterYd+zgPatrolSpreadYd)
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

// The helpers below are local copies of the ones the walk-26 Marli tests carry on their branch: this file must compile
// on a base that has neither (plan tests here), so they are prefixed and do not collide when the two meet.
func zgSq32(v float32) float32 { return v * v }

// zgSamplesAlong returns the points of the straight segment a-b every step yd, both ends included.
func zgSamplesAlong(a, b dungeon.Point, step float64) []dungeon.Point {
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

// zgReadMovementPaths reads movement.tsv (export.sh): the waypoints of each patrolling spawn by guid, in point order.
func zgReadMovementPaths(path string) (map[uint64][]dungeon.Point, error) {
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

// zgPlannerOrNil is the map-309 navmesh when the machine has the server's mmaps, nil otherwise.
func zgPlannerOrNil(t *testing.T) *navmesh.Planner {
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
	// 91493 moved to leg 1 group 18 (BOTS-PATHING-WAYPOINTS item 2): the leg starts with its 11-pull pack.
	wantOrder := [][]uint64{
		{49705},
		{49776},
		{49778},
		{51460},
		{51986},
		{49259, 49272, 49282, 51985},
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
	pl := zgPlannerOrNil(t)
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

// TestZulGurubJindoCorridorClearOfLaterPacks covers the Jin'do corridor through the group-13 pull (the Venombroods at the
// south hall's entry): the two packs that straddle the walk, the `to` moved off 49114's spawn, the swept north corridor,
// the 1173080 split and the 49421 group's radius. The south hall's pulls 14-17 keep their existing stands (not touched).
func TestZulGurubJindoCorridorClearOfLaterPacks(t *testing.T) {
	p, cs := zgLoadPlanAndExport(t)
	leg := p.Legs[6]
	packs := zgPacks(t, leg, cs)
	zgCheckStands(t, "jindo_the_hexxer", leg, packs, true, 12)
	// the order: the corridor packs 49741 and 49119 first, then 49114 and west; the Jin'do fix drops the
	// Sacrificed Troll pack (group 8 on the old base: respawn 10 s, wasted pull) and reroutes the walk
	// east of the pit, up to 49387-96's pack (group 11)
	want := [][]uint64{
		{49741},                   // 0 (moved here from the Hakkar leg)
		{49114}, {49747}, {51948}, // 1, 2, 3 (49118/49119 left for leg 0's canal stand pull, BOTS-PATHING-WAYPOINTS item 6)
		{49397, 49398, 49399, 49400}, // 4
		{49373, 49374, 49375, 49376, 49377, 49378, 49379, 49380, 49381, 49382}, // 5
		{1173082}, {49363}, // 6, 7
		{49402, 49403, 49421}, // 8 (the honour guard, a stand pull now; walk 36: pulled before the two north packs)
		{49383, 49384, 49385, 49386, 1173080}, // 9 (walk 36: the two north packs are one pull)
		{49387, 49388, 49389, 49390, 49391, 49392, 49393, 49394, 49395, 49396}, // 10
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
	// the 49421 group's circle must not reach Jin'do or another group's spawns. Found by formation, not by index:
	// walk 36 merged the two north packs into one and pulled the honour guard first, so the indices after 7 moved.
	guard := -1
	for gi := range leg.PullGroups {
		for _, c := range packs[gi] {
			if c.GUID == 49421 {
				guard = gi
			}
		}
	}
	if guard < 0 {
		t.Fatal("no pull group holds the 49421 honour-guard formation")
	}
	g := leg.PullGroups[guard]
	var cen dungeon.Point
	for _, c := range packs[guard] {
		cen.X += c.Pos.X
		cen.Y += c.Pos.Y
		cen.Z += c.Pos.Z
	}
	cn := float32(len(packs[guard]))
	cen.X, cen.Y, cen.Z = cen.X/cn, cen.Y/cn, cen.Z/cn
	for _, c := range packs[guard] {
		if d := dist(cen, c.Pos); d > float64(g.Radius)+3 {
			t.Errorf("the honour-guard pull is %.1f yd from its own %s %d, radius %g + 3", d, c.Name, c.Entry, g.Radius)
		}
	}
	if boss := leg.Boss; boss != nil {
		if d := dist(cen, boss.Position); d <= float64(g.Radius)+3 {
			t.Errorf("the honour-guard circle is %.1f yd from Jin'do at %.1f %.1f %.1f: the pull would wake him (radius %g + 3)", d, boss.Position.X, boss.Position.Y, boss.Position.Z, g.Radius)
		}
		// and no other pack's spawn inside the circle
		for gi := range leg.PullGroups {
			if gi == guard {
				continue
			}
			for _, c := range packs[gi] {
				if d := dist(cen, c.Pos); d <= float64(g.Radius) {
					t.Errorf("the honour-guard circle (radius %g) holds %s %d of pull group %d at %.1f yd", g.Radius, c.Name, c.Entry, gi, d)
				}
			}
		}
	}
	parties := zgParties(t, p, 6)
	last := -1
	for i, q := range parties {
		if q.Turn && q.Group == 12 { // the 51875 Venombrood pull closes the corridor (group 13 before walk 36's merge)
			last = i
			break
		}
	}
	if last < 0 {
		t.Fatal("no pull row for the Venombrood pack (51875)")
	}
	w := zgWalk{Name: "leg6 jindo corridor", Parties: parties, Last: last}
	pl := zgPlannerOrNil(t)
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
