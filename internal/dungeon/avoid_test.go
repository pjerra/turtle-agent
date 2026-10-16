package dungeon

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/pjerra/turtle-agent/internal/navmesh"
	"github.com/pjerra/turtle-agent/internal/packet"
	"github.com/pjerra/turtle-agent/internal/tbm"
)

// The avoid boxes (avoid.go, remote_avoid.go): Ragefire Chasm's lava basin is ground in the navmesh tiles and killed 11 bots in
// rfc-live-1d and 1e.

const avoidPlanHead = `{"version": 1, "name": "t", "map": 389, "entrance": {"x": 0, "y": 0, "z": 0},
  "avoid": [{"name": "lava", "min": {"x": -10, "y": -10, "z": -70}, "max": {"x": 10, "y": 10, "z": -64.2}}],
  "legs": [`

// The loader refuses a plan point that stands in an avoid box, and a box that is not one.
func TestPlanRefusesAPointInAnAvoidBox(t *testing.T) {
	ok := avoidPlanHead + `{"to": {"x": 0, "y": 0, "z": -60}, "pull_groups": [{"entries": [1], "positions": [{"x": 5, "y": 5, "z": -61}]}]}]}`
	p, err := Parse("ok", []byte(ok))
	if err != nil {
		t.Fatalf("a point above the box's floor band is fine: %v", err)
	}
	if len(p.Avoid) != 1 || p.Avoid[0].Name != "lava" || p.Avoid[0].Max.Z != -64.2 {
		t.Fatalf("avoid %+v", p.Avoid)
	}
	if b, in := p.InAvoid(Point{1, 1, -65}); !in || b.Name != "lava" {
		t.Fatalf("InAvoid of a point on the lava floor: %v %v", b, in)
	}
	for _, c := range []struct{ name, leg, want string }{
		{"to", `{"to": {"x": 1, "y": 2, "z": -65.4}}`, `legs[0].to: 1.0 2.0 -65.4 lies in avoid box "lava"`},
		{"via", `{"via": [{"x": -9, "y": 9, "z": -65}], "to": {"x": 0, "y": 0, "z": -60}}`, `legs[0].via[0]`},
		{"pull group", `{"to": {"x": 0, "y": 0, "z": -60}, "pull_groups": [{"entries": [1], "positions": [{"x": 0, "y": 0, "z": -60}, {"x": 3, "y": 3, "z": -65}]}]}`, `legs[0].pull_groups[0].positions[1]`},
		{"boss", `{"to": {"x": 0, "y": 0, "z": -60}, "boss": {"entry": 9, "position": {"x": 0, "y": 0, "z": -66}}}`, `legs[0].boss.position`},
	} {
		_, err := Parse(c.name, []byte(avoidPlanHead+c.leg+`]}`))
		if err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: %v, want %q", c.name, err, c.want)
		}
	}
	bad := strings.Replace(avoidPlanHead, `"max": {"x": 10`, `"max": {"x": -20`, 1) + `{"to": {"x": 0, "y": 0, "z": -60}}]}`
	if _, err := Parse("bad", []byte(bad)); err == nil || !strings.Contains(err.Error(), "avoid[0]: min.x -10.00 is not below max.x -20.00") {
		t.Errorf("a box with min over max: %v", err)
	}
	noName := strings.Replace(avoidPlanHead, `"name": "lava", `, ``, 1) + `{"to": {"x": 0, "y": 0, "z": -60}}]}`
	if _, err := Parse("noname", []byte(noName)); err == nil || !strings.Contains(err.Error(), "avoid[0].name: required") {
		t.Errorf("a box without a name: %v", err)
	}
}

// avoidNav is a fakeNav that records the boxes the check gives it.
type avoidNav struct {
	fakeNav
	boxes []navmesh.Box
	set   int
	cutAt *Point
}

func (a *avoidNav) SetAvoid(_ uint32, boxes []navmesh.Box) { a.boxes, a.set = boxes, a.set+1 }

// Plan finds no path to cutAt while boxes are set: the marks took the only way there.
func (a *avoidNav) Plan(m uint32, from, to navmesh.Vec3) (navmesh.Path, error) {
	if len(a.boxes) > 0 && a.cutAt != nil && (Point{to[0], to[1], to[2]}) == *a.cutAt {
		return navmesh.Path{Status: navmesh.NoPath, Reason: "Detour found no path", Points: []navmesh.Vec3{}}, nil
	}
	return a.fakeNav.Plan(m, from, to)
}

// A leg the navmesh reaches only through an avoid box fails the check as cut by the box, naming it, instead of as a plain unreachable
// point: the box's edge took a polygon the route needs.
func TestPlanCheckNamesTheBoxThatCutsALeg(t *testing.T) {
	nav := &avoidNav{cutAt: &Point{40, 0, -60}}
	p := &Plan{Name: "t", Map: 389, Avoid: []AvoidBox{{Name: "lava", Min: Point{-10, -10, -70}, Max: Point{10, 10, -64.2}}}, Entrance: Point{50, 0, -60},
		Legs: []Leg{{Name: "a", To: Point{20, 0, -60}}, {Name: "b", To: Point{40, 0, -60}}}}
	res, err := CheckPlan(p, nav, 0)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := issueReasons(res.Issues), "1/to/0/cut by an avoid box"; got != want || !strings.Contains(res.Issues[0].Detail, `avoid box "lava" cuts the route`) {
		t.Fatalf("issues %q (want %q): %+v", got, want, res.Issues)
	}
	if len(nav.boxes) != 1 {
		t.Fatalf("the boxes were not set again after the check without them: %v", nav.boxes)
	}
}

// The plan check tells a navmesh that can take them the plan's boxes, and fails a point on the magma (a plan built by hand: Parse
// refuses it already).
func TestPlanCheckRefusesAPointOnMagma(t *testing.T) {
	nav := &avoidNav{}
	box := AvoidBox{Name: "lava", Min: Point{-10, -10, -70}, Max: Point{10, 10, -64.2}}
	p := &Plan{Name: "t", Map: 389, Avoid: []AvoidBox{box}, Entrance: Point{50, 0, -60}, Legs: []Leg{
		{Name: "a", To: Point{20, 0, -60}},
		{Name: "b", To: Point{0, 0, -65.4}},
		{Name: "c", Via: []Waypoint{{Point: Point{5, 5, -65}, Direct: true}}, To: Point{30, 0, -60}},
	}}
	res, err := CheckPlan(p, nav, 0)
	if err != nil {
		t.Fatal(err)
	}
	if nav.set != 1 || len(nav.boxes) != 1 || nav.boxes[0].Min != (navmesh.Vec3{-10, -10, -70}) || nav.boxes[0].Max != (navmesh.Vec3{10, 10, -64.2}) {
		t.Fatalf("the navmesh got %d calls, boxes %v", nav.set, nav.boxes)
	}
	if got, want := issueReasons(res.Issues), "1/to/0/in an avoid box 2/direct via 0/0/in an avoid box"; got != want {
		t.Fatalf("issues %q, want %q", got, want)
	}
	if !strings.Contains(res.Issues[0].Detail, `"lava"`) {
		t.Fatalf("detail %q", res.Issues[0].Detail)
	}
	// A plan with no boxes clears the navmesh's.
	res, err = CheckPlan(&Plan{Name: "t", Map: 389, Legs: []Leg{{Name: "a", To: Point{0, 0, -65.4}}}}, nav, 0)
	if err != nil || !res.OK() || nav.set != 2 || nav.boxes != nil {
		t.Fatalf("no boxes: %v %+v set %d boxes %v", err, res.Issues, nav.set, nav.boxes)
	}
}

// A hop whose straight line passes over an avoid box is cut at the path's corner: the server walks a `move` on its own navmesh, which does
// not know the box, and would take the straight way across (rfc-live-1e: from the south rim straight over the basin).
func TestHopIsCutAtTheCornerBeforeAnAvoidBox(t *testing.T) {
	box := AvoidBox{Name: "lava", Min: Point{2, 5, -7}, Max: Point{8, 15, -2}}
	path := []Point{{0, 0, 0}, {10, 0, 0}, {10, 20, 0}}
	far, ok := alongPath(path, path[0], 25)
	if !ok || far.X != 10 || far.Y < 20-0.01 && far.Y < 13 {
		t.Fatalf("alongPath %v %v", far, ok)
	}
	if !chordNearAvoid(path[0], far, []AvoidBox{box}) {
		t.Fatalf("the line to %v does not pass over the box", far)
	}
	got, ok := alongPathAvoid(path, path[0], 25, []AvoidBox{box})
	if !ok || got != (Point{10, 0, 0}) {
		t.Fatalf("alongPathAvoid = %v %v, want the corner 10 0 0", got, ok)
	}
	// A box well under the hop (another floor, more than avoidChordDZ below) does not cut it.
	low := AvoidBox{Name: "deep", Min: Point{2, 5, -30}, Max: Point{8, 15, -20}}
	if got, ok := alongPathAvoid(path, path[0], 25, []AvoidBox{low}); !ok || got != far {
		t.Fatalf("a box far below: %v %v, want %v", got, ok, far)
	}
	// Past the corner the next hop runs along the second segment, clear of the box.
	if got, ok := alongPathAvoid(path[1:], path[1], 25, []AvoidBox{box}); !ok || got != (Point{10, 20, 0}) {
		t.Fatalf("from the corner: %v %v", got, ok)
	}
}

// avoidRun runs plan with a GM commander, as rejoinRun does, with the game given (a nearGame with or without a floor snap).
func avoidRun(t *testing.T, g RemoteGame, c Clock, plan *Plan) (Report, error) {
	t.Helper()
	cfg := DefaultConfig()
	cfg.PostFight, cfg.AddsWait = 2*time.Second, 4*time.Second
	d := NewRemoteDriver(plan, g, cfg, objOpts(), c)
	err := d.Run(context.Background(), 0)
	rep, ended := d.Status()
	if !ended {
		t.Fatal("not ended")
	}
	return rep, err
}

func lavaAtStart() []AvoidBox {
	return []AvoidBox{{Name: "lava", Min: Point{-3, -3, -1}, Max: Point{3, 3, 1}}}
}

// The lead bot stands in the lava and no safe floor is known near it: no bot is summoned onto it. The far bot is summoned once the walk has
// taken the lead bot out (rfc-live-1e r1: the lead bot stood in the basin and the summoned bots died there within 2 s).
func TestRejoinSummonsNoBotOntoTheLava(t *testing.T) {
	g := newNearGame(t, 3)
	g.agent = Point{0, 0, 0}
	farMage(g.fakeRemote, -100)
	plan := rejoinPlan()
	plan.Avoid = lavaAtStart()
	var summonedAt []Point
	g.onTick = func(f *fakeRemote) {
		if len(f.summoned) > len(summonedAt) {
			summonedAt = append(summonedAt, Point{f.lead().X, f.lead().Y, f.lead().Z})
		}
	}
	rep, err := avoidRun(t, g, g.clock(), plan)
	if err != nil || rep.State != "done" {
		t.Fatalf("%v %s\n%s", err, rep.State, notesOf(rep))
	}
	if len(g.summoned) != 1 || g.indexOf("cmd summon") < g.indexOf("cmd move") {
		t.Fatalf("summoned %v orders %v", g.summoned, g.orders)
	}
	for _, at := range summonedAt {
		if plan.Avoid[0].Contains(at) {
			t.Fatalf("a bot was summoned onto the lead bot at %v, in the box", at)
		}
	}
	if !strings.Contains(notesOf(rep), "no safe floor is known near it: no bot is summoned onto it") {
		t.Fatalf("notes\n%s", notesOf(rep))
	}
}

// snapGame is a nearGame with a floor snap: the nearest safe floor to anything in the box is (5, 0, 0).
type snapGame struct {
	*nearGame
	snaps int
}

func (s *snapGame) SnapToFloor(_ context.Context, _ uint32, p Point) (Point, bool) {
	s.snaps++
	if lavaAtStart()[0].Contains(p) {
		return Point{5, 0, 0}, true
	}
	return p, true
}

// A summon target on the lava is moved: the lead bot is sent to the nearest safe floor first, and the bot is summoned to it there.
func TestRejoinMovesTheLeadOffTheLavaFirst(t *testing.T) {
	g := &snapGame{nearGame: newNearGame(t, 3)}
	g.agent = Point{0, 0, 0}
	farMage(g.fakeRemote, -100)
	plan := rejoinPlan()
	plan.Avoid = lavaAtStart()
	rep, err := avoidRun(t, g, g.clock(), plan)
	if err != nil || rep.State != "done" {
		t.Fatalf("%v %s\n%s", err, rep.State, notesOf(rep))
	}
	first := g.indexOf("cmd move")
	if first < 0 || g.orders[first] != "cmd move 5.00 0.00 0.00" {
		t.Fatalf("the first move is not to the safe floor: %v", g.orders)
	}
	if s := g.indexOf("cmd summon"); s < first || len(g.summoned) != 1 {
		t.Fatalf("summon %d after the move %d? %v", s, first, g.orders)
	}
	if !strings.Contains(notesOf(rep), "it is moved to 5.0 0.0 0.0 before any bot is summoned to it") {
		t.Fatalf("notes\n%s", notesOf(rep))
	}
}

// silentRig is a driver fed hand-made frames: frame observes the party as countDeaths does and moves the clock a second.
type silentRig struct {
	t *testing.T
	g *fakeRemote
	d *RemoteDriver
}

func newSilentRig(t *testing.T) *silentRig {
	g := newFakeRemote(t)
	d := NewRemoteDriver(remotePlan(Leg{Name: "a", To: Point{150, 0, 0}}), g, DefaultConfig(), opts(), g.clock())
	d.curLeg = 0
	return &silentRig{t: t, g: g, d: d}
}

func (r *silentRig) frame(ps ...tbm.PartyRec) {
	r.d.fr = tbm.ViewFrame{Lead: gTank, Party: ps}
	for _, p := range ps {
		r.d.observeDeadly(p, p.Dead && !r.d.prevDead[p.GUID])
		r.d.prevDead[p.GUID] = p.Dead
	}
	r.d.deadlyFrame()
	r.g.advance(time.Second)
}

func member(g packet.GUID, name string, hp uint32, state string, at Point) tbm.PartyRec {
	return tbm.PartyRec{GUID: g, Name: name, HP: hp, MaxHP: 500, State: state, X: at.X, Y: at.Y, Z: at.Z}
}

func dead(p tbm.PartyRec, at Point) tbm.PartyRec {
	p.Dead, p.HP, p.State, p.X, p.Y, p.Z = true, 0, "d", at.X, at.Y, at.Z
	return p
}

// A low-health death, an in-combat death and one lone full-health death out of a box mark nothing: one such death is no evidence (Codex
// review of #211). A second full-health death out of combat within 6 yd makes the spot deadly for summons; it is forgotten after 5 min.
func TestSilentDeathNeedsStrongEvidence(t *testing.T) {
	r := newSilentRig(t)
	lava := Point{-204.6, -25.2, -64.9}
	lead := member(gTank, "Tanky", 500, "n", Point{40, 40, 0})
	low := member(gHealer, "Healy", 100, "n", Point{-205, -24, -64.9}) // 20 %, out of combat
	fight := member(gMage, "Magey", 500, "c", Point{-203, -26, -64.9}) // full, in combat
	r.frame(lead, low, fight)
	r.frame(lead, dead(low, Point{-205, -24, -64.9}), dead(fight, Point{-203, -26, -64.9}))
	if _, bad := r.d.deadlyAt(lava, 0); bad || len(r.d.deadly.lone) != 0 {
		t.Fatalf("a low-health or in-combat death marked the spot: lone %v spots %v", r.d.deadly.lone, r.d.deadly.spots)
	}
	// One full-health death out of combat: a lone one, still no deadly spot.
	m1 := member(gHealer+100, "Mali", 500, "n", Point{-209.3, -17.3, -60.9})
	r.frame(lead, m1)
	r.frame(lead, dead(m1, lava))
	if _, bad := r.d.deadlyAt(lava, 0); bad || len(r.d.deadly.lone) != 1 {
		t.Fatalf("one isolated death made a deadly spot: %v", r.d.deadly.spots)
	}
	// A second within 6 yd: the spot is deadly.
	m2 := member(gHealer+101, "Gorka", 500, "n", Point{-21, -40, -21})
	r.frame(lead, m2)
	r.frame(lead, dead(m2, Point{-206.6, -27.4, -65}))
	if why, bad := r.d.deadlyAt(Point{-202.2, -23.6, -64.8}, 0); !bad || !strings.Contains(why, "Mali and Gorka") {
		t.Fatalf("two deaths: %q %v", why, bad)
	}
	// The lead bot on the spot: no summon goes out.
	onSpot := lead
	onSpot.X, onSpot.Y, onSpot.Z = lava.X, lava.Y, lava.Z
	r.d.fr = tbm.ViewFrame{Lead: gTank, Party: []tbm.PartyRec{onSpot}}
	if r.d.rejoinSummon(context.Background(), rejoinBot{guid: gHealer, name: "Healy", dist: 100}) || len(r.g.summoned) != 0 {
		t.Fatalf("summoned onto the spot: %v", r.g.orders)
	}
	// A body is raised in place unless it lies in an avoid box: the spot alone stops summons only.
	if got := r.d.corpsesOnDeadly([]tbm.PartyRec{dead(m1, lava)}); len(got) != 0 {
		t.Fatalf("corpses on deadly ground without a box: %v", got)
	}
	r.d.plan.Avoid = []AvoidBox{{Name: "lava", Min: Point{-226, -64, -72}, Max: Point{-142, 6, -64.2}}}
	if got := r.d.corpsesOnDeadly([]tbm.PartyRec{dead(m1, lava)}); len(got) != 1 || got[0] != `Mali in the avoid box "lava"` {
		t.Fatalf("corpses in the box: %v", got)
	}
	r.d.plan.Avoid = nil
	// Five minutes on, the spot is forgotten.
	r.g.advance(deadlySpotTTL)
	r.frame(lead)
	if _, bad := r.d.deadlyAt(lava, 0); bad {
		t.Fatal("the spot outlived its 5 min")
	}
	n := notesOf(r.d.Report())
	for _, want := range []string{"one such death is no evidence yet", "a deadly spot; no bot is summoned", "deadly spot(s) forgotten"} {
		if !strings.Contains(n, want) {
			t.Fatalf("no note %q:\n%s", want, n)
		}
	}
}

// A living member that stands on a deadly spot for 5 s and lives clears it.
func TestDeadlySpotIsClearedByABotThatLivesOnIt(t *testing.T) {
	r := newSilentRig(t)
	spot := Point{100, 100, 0}
	lead := member(gTank, "Tanky", 500, "n", Point{0, 0, 0})
	a := member(gHealer, "Healy", 500, "n", Point{90, 90, 0})
	b := member(gMage, "Magey", 500, "n", Point{90, 90, 0})
	r.frame(lead, a, b)
	r.frame(lead, dead(a, spot), dead(b, Point{102, 100, 0}))
	if _, bad := r.d.deadlyAt(spot, 0); !bad {
		t.Fatal("no deadly spot")
	}
	lead.X, lead.Y = 101, 100
	for range 7 {
		r.frame(lead, dead(a, spot), dead(b, Point{102, 100, 0}))
	}
	if _, bad := r.d.deadlyAt(spot, 0); bad || !strings.Contains(notesOf(r.d.Report()), "and lives: the spot is cleared") {
		t.Fatalf("the spot stays after a bot lived on it: %v\n%s", r.d.deadly.spots, notesOf(r.d.Report()))
	}
}

// stuckGame is a nearGame whose floor snap finds only lava (the lead bot cannot be moved off it nearby) and whose planner plans straight lines.
type stuckGame struct{ *nearGame }

func (s *stuckGame) SnapToFloor(_ context.Context, _ uint32, p Point) (Point, bool) {
	return Point{1, 1, 0}, true
}
func (s *stuckGame) PlanPath(_ context.Context, _ uint32, from, to Point) ([]Point, error) {
	return []Point{from, to}, nil
}

// stuckRig: the lead bot stands at (25, 0, 0) (beyond the 20 yd a move off may go) for a few frames (its history), then is put in the lava box at the origin; Magey is 100 yd
// off. The plan's leg goes to (60, 0, 0), ahead, never walked.
func stuckRig(t *testing.T) (*stuckGame, *RemoteDriver) {
	g := &stuckGame{nearGame: newNearGame(t, 3)}
	g.agent = Point{0, 0, 0}
	farMage(g.fakeRemote, -100)
	g.lead().X = 25
	plan := remotePlan(Leg{Name: "a", To: Point{60, 0, 0}})
	plan.Avoid = lavaAtStart()
	d := NewRemoteDriver(plan, g, DefaultConfig(), objOpts(), g.clock())
	d.curLeg = 0
	for range 6 { // the feed's first frames (the lead bot's history at 20 0 0), and the calm the rejoin waits for
		if err := d.tick(context.Background(), time.Second); err != nil {
			t.Fatal(err)
		}
	}
	g.lead().X = 0
	return g, d
}

// A lead bot stuck on deadly ground still gets the party summoned: after two failed tries it walks back to where it last stood and lived
// (25 0 0, from its history), not to the leg's `to` ahead (60 0 0, past packs not cleared), and the far bot is summoned there.
func TestStuckLeadWalksBackToWhereItLived(t *testing.T) {
	g, d := stuckRig(t)
	ctx := context.Background()
	// The first call still has the frame from before the lead bot was put in the box: its summon is refused at the lead bot's new place.
	for i := 0; i < 4 && len(g.summoned) == 0; i++ {
		if err := d.rejoinFar(ctx); err != nil {
			t.Fatalf("call %d: %v\n%s", i, err, notesOf(d.Report()))
		}
	}
	l := g.lead()
	if len(g.summoned) != 1 || dist3(Point{l.X, l.Y, l.Z}, Point{25, 0, 0}) > 6 {
		t.Fatalf("summoned %v, lead at %.0f %.0f; orders %v\n%s", g.summoned, l.X, l.Y, g.orders, notesOf(d.Report()))
	}
	for _, o := range g.orders {
		if strings.HasPrefix(o, "cmd move 6") || strings.HasPrefix(o, "cmd move 5") {
			t.Fatalf("the lead bot was sent ahead: %v", g.orders)
		}
	}
	n := notesOf(d.Report())
	if strings.Count(n, "no safe floor is known near it") != 2 || !strings.Contains(n, "it walks back to 25.0 0.0 0.0, where it stood and lived") {
		t.Fatalf("notes\n%s", n)
	}
}

// The fallback is bounded: a walk back that fails ends the step with errLeadStuck, and so does any later need in the leg, instead of two
// more tries and another walk every rejoin.
func TestStuckLeadFallbackIsBounded(t *testing.T) {
	g, d := stuckRig(t)
	g.moveErrs = slices.Repeat([]string{"no-path"}, 200)
	ctx := context.Background()
	var err error
	calls := 0
	for ; calls < 10 && err == nil; calls++ {
		err = d.rejoinFar(ctx)
	}
	if !errors.Is(err, errLeadStuck) || calls != 4 || !strings.Contains(err.Error(), "the walk back to 25.0 0.0 0.0 failed") {
		t.Fatalf("after %d calls: %v", calls, err)
	}
	if err := d.rejoinFar(ctx); !errors.Is(err, errLeadStuck) || !strings.Contains(err.Error(), "used already") {
		t.Fatalf("the next call: %v", err)
	}
	if len(g.summoned) != 0 {
		t.Fatalf("summoned %v onto the lava", g.summoned)
	}
}

// The fallback stand comes from the lead bot's history, newest first, and its route must keep out of deadly spots: a route whose middle
// passes a spot is refused for an older place reached clear of it; with no clear route the recovery gives up with the reason.
func TestFallbackStandRouteAvoidsDeadlySpots(t *testing.T) {
	g := &stuckGame{nearGame: newNearGame(t, 3)}
	plan := remotePlan(Leg{Name: "a", To: Point{60, 0, 0}})
	d := NewRemoteDriver(plan, g, DefaultConfig(), objOpts(), g.clock())
	d.deadly.history = []histPoint{{at: Point{0, 30, 0}}, {at: Point{30, 0, 0}}}
	d.deadly.spots = []deadlySpot{{at: Point{12, 1, 0}, who: "Mali and Gorka", last: g.now}}
	at, why, ok := d.fallbackStand(context.Background(), Point{0, 0, 0})
	if !ok || at != (Point{0, 30, 0}) {
		t.Fatalf("got %v %q %v, want the older place reached clear of the spot", at, why, ok)
	}
	d.deadly.spots = append(d.deadly.spots, deadlySpot{at: Point{1, 8, 0}, who: "Wiline and Gargha", last: g.now})
	if at, why, ok := d.fallbackStand(context.Background(), Point{0, 0, 0}); ok || !strings.Contains(why, "no safe route") {
		t.Fatalf("both routes cross a spot: %v %q %v", at, why, ok)
	}
	d.deadly.history = nil
	if _, why, ok := d.fallbackStand(context.Background(), Point{0, 0, 0}); ok || !strings.Contains(why, "no safe ground") {
		t.Fatalf("no history: %q %v (the leg's to ahead is never taken)", why, ok)
	}
}
