package dungeon

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/pjerra/turtle-agent/internal/navmesh"
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
}

func (a *avoidNav) SetAvoid(_ uint32, boxes []navmesh.Box) { a.boxes, a.set = boxes, a.set+1 }

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

// A bot that dies at full health from one frame to the next, or out of combat, marks its spot as deadly (the feed has no line for lava
// damage): no summon onto it. A bot worn down in a fight does not. A body in an avoid box is not raised in place.
func TestSilentDeathMarksTheSpot(t *testing.T) {
	g := newFakeRemote(t)
	d := NewRemoteDriver(remotePlan(Leg{Name: "a", To: Point{150, 0, 0}}), g, DefaultConfig(), opts(), g.clock())
	d.curLeg = 0
	frame := func(ps ...tbm.PartyRec) {
		d.fr = tbm.ViewFrame{Lead: gTank, Party: ps}
		for _, p := range ps {
			d.observeDeadly(p, p.Dead && !d.prevDead[p.GUID])
			d.prevDead[p.GUID] = p.Dead
		}
		g.advance(time.Second)
	}
	lava := Point{-204.6, -25.2, -64.9}
	full := tbm.PartyRec{GUID: gMage, Name: "Magey", HP: 532, MaxHP: 532, State: "n", X: -209.3, Y: -17.3, Z: -60.9}
	worn := tbm.PartyRec{GUID: gHealer, Name: "Healy", HP: 100, MaxHP: 900, State: "c", X: 50, Y: 50, Z: 0}
	lead := tbm.PartyRec{GUID: gTank, Name: "Tanky", HP: 900, MaxHP: 900, State: "c", X: 40, Y: 40, Z: 0}
	frame(lead, full, worn)
	deadMage := full
	deadMage.Dead, deadMage.HP, deadMage.State, deadMage.X, deadMage.Y, deadMage.Z = true, 0, "d", lava.X, lava.Y, lava.Z
	deadHealer := worn
	deadHealer.Dead, deadHealer.HP, deadHealer.State = true, 0, "d"
	frame(lead, deadMage, deadHealer)
	if why, bad := d.deadlyAt(Point{-202.2, -23.6, -64.8}, 0); !bad || !strings.Contains(why, "Magey died with no wound") {
		t.Fatalf("near the mage's body: %q %v", why, bad)
	}
	if _, bad := d.deadlyAt(Point{-204.6, -25.2, -58}, 0); bad {
		t.Fatal("the ledge 7 yd above the spot is not deadly")
	}
	if _, bad := d.deadlyAt(Point{50, 50, 0}, 0); bad {
		t.Fatal("the healer died worn down in a fight: its spot is not deadly")
	}
	// A body is raised in place unless it lies in an avoid box: the spot alone stops summons only.
	if got := d.corpsesOnDeadly([]tbm.PartyRec{deadMage, deadHealer}); len(got) != 0 {
		t.Fatalf("corpses on deadly ground without a box: %v", got)
	}
	d.plan.Avoid = []AvoidBox{{Name: "lava", Min: Point{-226, -64, -72}, Max: Point{-142, 6, -64.2}}}
	if got := d.corpsesOnDeadly([]tbm.PartyRec{deadMage, deadHealer}); len(got) != 1 || got[0] != `Magey in the avoid box "lava"` {
		t.Fatalf("corpses in the box: %v", got)
	}
	d.plan.Avoid = nil
	if !strings.Contains(notesOf(d.Report()), "Magey died at -204.6 -25.2 -64.9 at 100% health one frame before with no fight wearing it down") {
		t.Fatalf("notes\n%s", notesOf(d.Report()))
	}
	// The lead bot on the spot: no summon goes out.
	onSpot := lead
	onSpot.X, onSpot.Y, onSpot.Z = lava.X, lava.Y, lava.Z
	d.fr = tbm.ViewFrame{Lead: gTank, Party: []tbm.PartyRec{onSpot, deadMage}}
	if d.rejoinSummon(context.Background(), rejoinBot{guid: gHealer, name: "Healy", dist: 100}) || len(g.summoned) != 0 {
		t.Fatalf("summoned onto the spot: %v", g.orders)
	}
}

// pathGame is a fakeRemote that plans a given path (a detour round the box) to one point and straight lines to the rest.
type pathGame struct {
	*fakeRemote
	round Point
	via   []Point
}

func (p *pathGame) PlanPath(_ context.Context, _ uint32, from, to Point) ([]Point, error) {
	if to == p.round {
		return append(append([]Point{from}, p.via...), to), nil
	}
	return []Point{from, to}, nil
}

// A hostile near the next hop that the party reaches only round an avoid box is not pulled on the way: the tank would take the server's
// straight way to it, across the box (rfc-live-1e: the terrace pair under the ramp, 358 yd round, 41 in a line).
func TestHostileReachedOnlyRoundTheBoxIsNotPulledAhead(t *testing.T) {
	g := &pathGame{fakeRemote: newFakeRemote(t)}
	terrace := Point{-157.8, -21.5, -56.6}
	camp := Point{-199.0, -70.0, -62.5}
	g.round, g.via = terrace, []Point{{-286, -40, -60}, {-285, -84, -61}, {-152, -62, -58}}
	plan := remotePlan(Leg{Name: "a", To: Point{-170.8, 2.5, -50.8}})
	plan.Avoid = []AvoidBox{{Name: "lava", Min: Point{-226, -64, -72}, Max: Point{-142, 6, -64.2}}}
	d := NewRemoteDriver(plan, g, DefaultConfig(), opts(), g.clock())
	d.curLeg = 0
	here := Point{-192, 0, -59.5}
	if !d.aheadAcrossAvoid(here, terrace) {
		t.Fatal("the terrace pair, reached only round the basin, would be pulled")
	}
	if d.aheadAcrossAvoid(here, camp) {
		t.Fatal("a pack the planner reaches in a line (off the lava) is pulled as before")
	}
	plan.Avoid = nil
	if d.aheadAcrossAvoid(here, terrace) {
		t.Fatal("without avoid boxes nothing changes")
	}
}
