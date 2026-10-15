package dungeon

import (
	"context"
	"fmt"
	"strings"
	"testing"
)

// gm_cross (remote_gmcross.go): a waypoint a GM account moves the party to, for a gap no walk crosses.

func gmCrossPlan() *Plan {
	return remotePlan(
		Leg{Name: "a", To: Point{100, 0, 0}, PullGroups: []PullGroup{{Entries: []uint32{299}, Positions: []Point{{150, 5, 0}}}}},
		Leg{Name: "b", Via: []Waypoint{{Point: Point{200, 0, 0}}, {Point: Point{300, 0, 0}, Direct: true, GMCross: true}},
			To: Point{330, 0, 0}, PullGroups: []PullGroup{{Entries: []uint32{300}, Positions: []Point{{350, 5, 0}}}}})
}

func TestGMCrossIsReadAndChecked(t *testing.T) {
	doc := func(via string) string {
		return `{"version":1,"name":"g","map":1,"entrance":{"x":0,"y":0,"z":0},"legs":[{"name":"a","via":[` + via + `],"to":{"x":10,"y":0,"z":0},"pull_groups":[{"entries":[1],"positions":[{"x":11,"y":0,"z":0}]}]}]}`
	}
	// Far from anything, with no via point before it: a GM moves the party, so neither the direct limits nor a gathering point apply.
	p, err := Parse("g.json", []byte(doc(`{"x":1500,"y":-900,"z":40,"direct":true,"gm_cross":true}`)))
	if err != nil {
		t.Fatal(err)
	}
	if w := p.Legs[0].Via[0]; !w.GMCross || !w.Direct || !p.HasGMCross(0) || p.HasGMCross(1) {
		t.Fatalf("%+v", w)
	}
	b, err := Marshal(p)
	if err != nil || !strings.Contains(string(b), `"gm_cross": true`) {
		t.Fatalf("%v %s", err, b)
	}
	if _, err := Parse("g.json", []byte(doc(`{"x":1,"y":2,"z":3,"gm_cross":true}`))); err == nil || !strings.Contains(err.Error(), "gm_cross is a direct waypoint") {
		t.Fatalf("a gm_cross that is not direct: %v", err)
	}
	// A plain direct step keeps its limits.
	if _, err := Parse("g.json", []byte(doc(`{"x":0,"y":0,"z":0},{"x":90,"y":0,"z":0,"direct":true}`))); err == nil || !strings.Contains(err.Error(), "direct step") {
		t.Fatalf("a long direct step: %v", err)
	}
}

// The plan check takes a gm_cross point without a path to it (listed as exempt), and still wants floor under it.
func TestPlanCheckAcceptsAGMCrossWithoutAPath(t *testing.T) {
	nav := fakeNav{
		block: func(from, to Point) bool { return to == (Point{300, 0, 0}) }, // no mesh link to the point
		off:   map[Point]bool{{400, 0, 0}: true},
	}
	leg := Leg{Name: "a", To: Point{10, 0, 0}, Via: []Waypoint{{Point: Point{300, 0, 0}, Direct: true, GMCross: true}}}
	res := checkLegs(t, nav, 0, leg)
	if !res.OK() || len(res.Exempt) != 1 || !strings.Contains(res.Exempt[0].Detail, "gm_cross") || res.Exempt[0].Part != "gm_cross via 0" {
		t.Fatalf("issues %+v exempt %+v", res.Issues, res.Exempt)
	}
	// The same point as a plain walk fails; and a gm_cross point with no mesh floor under it fails too.
	plain := Leg{Name: "a", To: Point{10, 0, 0}, Via: []Waypoint{{Point: Point{300, 0, 0}}}}
	if res := checkLegs(t, nav, 0, plain); res.OK() {
		t.Fatalf("a walk to a point without a path passed: %+v", res)
	}
	off := Leg{Name: "a", To: Point{10, 0, 0}, Via: []Waypoint{{Point: Point{400, 0, 0}, Direct: true, GMCross: true}}}
	if res := checkLegs(t, nav, 0, off); res.OK() || issueReasons(res.Issues) != "0/gm_cross via 0/0/off the mesh" {
		t.Fatalf("%s", issueReasons(res.Issues))
	}
}

// The driver does not walk the hop: the commander goes to the point, the lead bot is summoned and held, the others are summoned, the party
// follows again, and the walk goes on from the point.
func TestRemoteGMCrossMovesTheWholeParty(t *testing.T) {
	g := newStartFake(t)
	g.addMob(299, Point{150, 5, 0})
	g.addMob(300, Point{350, 5, 0})
	g.apart["Healy"] = true
	g.drift = &Point{290, 0, 0}
	rep, err := runFrom(t, g, gmCrossPlan(), startOpts(), 0)
	if err != nil || rep.State != "done" {
		t.Fatalf("%v %s\n%s", err, rep.State, notesOf(rep))
	}
	// `.go xyz` to the point, once; the lead bot summoned by `.bot summon`; Healy by `cmd summon`.
	if fmt.Sprint(g.gos) != "[300 0 0 0]" || fmt.Sprint(g.botLines) != "[summon Tanky]" || fmt.Sprint(g.summonsLog) != "[Healy]" {
		t.Fatalf("gos %v bot lines %v cmd summons %v", g.gos, g.botLines, g.summonsLog)
	}
	if n := notesOf(rep); !strings.Contains(n, "gm_cross: gm_cross at 300.0 0.0 0.0: 3 bots moved") {
		t.Fatalf("no crossing note:\n%s", n)
	}
	if rep.Remote.GMCrosses != 1 || rep.Remote.GMCommands < 2 || rep.Start != nil {
		t.Fatalf("remote %+v start %+v", rep.Remote, rep.Start)
	}
	if got := fmt.Sprint(rep.SetupShortcuts); !strings.Contains(got, ".go xyz 300.00 0.00 0.00 0 (GM crossing)") {
		t.Fatalf("setup shortcuts %v", rep.SetupShortcuts)
	}
	// No order walked the hop (a `cmd move 300 ... direct`), the party follows again after the move, and the leg's walk and pull went on.
	if i := g.indexOf("cmd move 300.00 0.00 0.00 direct"); i >= 0 {
		t.Fatalf("the hop was walked: %v", g.orders)
	}
	if g.indexOf("cmd follow") < 0 || g.indexOf("cmd follow") > g.indexOf("cmd move 330") {
		t.Fatalf("orders %v", g.orders)
	}
	if g.mobAlive(300) || g.mobAlive(299) {
		t.Fatalf("mobs %v", g.mobs)
	}
	l := g.lead()
	if l.X < 300 {
		t.Fatalf("the lead bot is at %.0f", l.X)
	}
}

// A party that already stands within the radius of the point (but not on it) needs no GM line.
func TestRemoteGMCrossAlreadyThere(t *testing.T) {
	g := newStartFake(t)
	g.addMob(300, Point{350, 5, 0})
	plan := gmCrossPlan()
	plan.Legs = plan.Legs[1:]
	plan.Legs[0].Via = []Waypoint{{Point: Point{GMCrossRadius - 2, 0, 0}, Direct: true, GMCross: true}} // the lead bot starts at the origin
	rep, err := runFrom(t, g, plan, startOpts(), 0)
	if err != nil {
		t.Fatalf("%v\n%s", err, notesOf(rep))
	}
	if len(g.gos) != 0 || len(g.botLines) != 0 || rep.Remote.GMCrosses != 0 || !strings.Contains(notesOf(rep), "already: no GM move") {
		t.Fatalf("gos %v lines %v crosses %d\n%s", g.gos, g.botLines, rep.Remote.GMCrosses, notesOf(rep))
	}
}

// Without a GM account the run is refused before a single command goes out; a game that cannot move the commander is refused the same.
func TestRemoteGMCrossNeedsAGMAndSendsNothingWithout(t *testing.T) {
	g := newStartFake(t)
	g.level = 0
	_, err := runFrom(t, g, gmCrossPlan(), startOpts(), 0)
	if err == nil || !strings.Contains(err.Error(), "needs a GM commander") || !strings.Contains(err.Error(), "nothing was sent") {
		t.Fatalf("%v", err)
	}
	if len(g.orders) != 0 || len(g.gos) != 0 || len(g.botLines) != 0 || len(g.appears) != 0 {
		t.Fatalf("something was sent: orders %v gos %v lines %v", g.orders, g.gos, g.botLines)
	}
	g = newStartFake(t)
	g.known = false
	if _, err := runFrom(t, g, gmCrossPlan(), startOpts(), 0); err == nil || len(g.orders) != 0 {
		t.Fatalf("an unknown level: %v orders %v", err, g.orders)
	}
	// Starting after the gap needs no GM.
	g = newStartFake(t)
	g.level = 0
	g.addMob(300, Point{350, 5, 0})
	plan := gmCrossPlan()
	plan.Legs[1].Via = plan.Legs[1].Via[:1]
	if _, err := runFrom(t, g, plan, startOpts(), 0); err != nil && strings.Contains(err.Error(), "gm_cross") {
		t.Fatalf("a plan without the crossing was refused: %v", err)
	}
	// A game without the commander's own character.
	plain := newFakeRemote(t)
	err = NewRemoteDriver(gmCrossPlan(), plain, DefaultConfig(), opts(), plain.clock()).Run(context.Background(), 0)
	if err == nil || !strings.Contains(err.Error(), "cannot move the commander's own character") || len(plain.orders) != 0 {
		t.Fatalf("%v orders %v", err, plain.orders)
	}
}

// The group driver refuses a plan with a gm_cross, from the leg the run starts at on.
func TestGroupDriverRefusesAGMCross(t *testing.T) {
	g := newFake()
	p := gmCrossPlan()
	d := NewDriver(p, g, Config{}, g.clock())
	if err := d.Run(context.Background(), 0); err == nil || !strings.Contains(err.Error(), "gm_cross") || !strings.Contains(err.Error(), "nothing was sent") {
		t.Fatalf("%v", err)
	}
	if len(g.chats) != 0 || len(g.gms) != 0 {
		t.Fatalf("chats %v gms %v", g.chats, g.gms)
	}
}
