package dungeon

import (
	"context"
	"strings"
	"testing"
)

// The hire spot refusals (#266): hires appear at the commander and muster there, so the commander must not stand under the portal's
// ground and must be of the run's team.

const portalMap = 1

// portalPoint is Ragefire Chasm's portal on the outer map (dungeons/ragefire_chasm.json).
var portalPoint = Point{X: 1818.4, Y: -4427.26, Z: -10.4478}

// hirePosFake is the hire fake with a game that knows where the commander stands (AgentPositioner); a plain hireFake gives none.
type hirePosFake struct {
	*hireFake
	pos Point
}

func (g *hirePosFake) AgentPosition() Point { return g.pos }

// hireSpotEnv is the prepare environment of the hire step over a plan, the way remote_prepare.go builds it.
func hireSpotEnv(g *hireFake, own Preparer, note func(string)) *prepEnv {
	clk := g.clock()
	return &prepEnv{g: own, plan: g.plan, spec: PrepareSpec{Party: PartyHire}, remote: true, maintain: true,
		now: clk.Now, sleep: clk.Sleep, note: note, command: g.Command}
}

// A commander that stands under the portal's ground hires nothing at all: the hires would appear and muster there, out of reach of the
// instance's entrance.
func TestHireRefusesUnderThePortal(t *testing.T) {
	g := newHireFake(t)
	g.agentMap = portalMap
	g.plan.Portal = &Portal{Map: portalMap, Point: portalPoint}
	pos := &hirePosFake{hireFake: g, pos: Point{X: 1821.5, Y: -4425.3, Z: -19.8}}
	e := hireSpotEnv(g, pos, func(string) {})
	_, err := e.hire(context.Background())
	if err == nil || !strings.Contains(err.Error(), "portal ground") || !strings.Contains(err.Error(), "below") {
		t.Fatalf("a hire under the portal ground: %v", err)
	}
	if len(g.hireLines) != 0 || g.count("cbind") != 0 {
		t.Fatalf("a hire under the portal still hired: hires %v, binds %d", g.hireLines, g.count("cbind"))
	}
}

// A commander of the other team hires nothing, in the five-man and in the raid flow: the module hires the commander's own faction, so
// every hire would fight for the other side.
func TestHireRefusesTheOtherTeamsCommander(t *testing.T) {
	g := newHireFake(t)
	g.team = "alliance"
	e := hireSpotEnv(g, g, func(string) {})
	e.team = "horde"
	_, err := e.hire(context.Background())
	if err == nil || !strings.Contains(err.Error(), "the run's team is horde") || !strings.Contains(err.Error(), "alliance") {
		t.Fatalf("a hire from the other team's commander: %v", err)
	}
	if len(g.hireLines) != 0 || g.count("cbind") != 0 {
		t.Fatalf("a hire of the other team's commander still hired: hires %v, binds %d", g.hireLines, g.count("cbind"))
	}

	r := newHireRaidFake(t, 2, 3, 5, 2)
	r.team = "alliance"
	clk := r.clock()
	re := &prepEnv{g: r, plan: raidPlan10(t), spec: PrepareSpec{Party: PartyHire, Hire: &HireSpec{}}, team: "horde",
		now: clk.Now, sleep: clk.Sleep, note: func(string) {}, command: r.Command}
	if _, err := re.raidHire(context.Background()); err == nil || !strings.Contains(err.Error(), "the run's team is horde") {
		t.Fatalf("a raid hire from the other team's commander: %v", err)
	}
	if r.idxOf("hire raid") >= 0 || r.idxOf("cbind") >= 0 {
		t.Fatalf("a raid hire of the other team's commander still hired: %v", r.events)
	}
}

// The spot is fine on the portal ground, at the edge of the drop, across the ledge, on another map, and whenever a team is unknown or
// the same in another case.
func TestHireSpotAllows(t *testing.T) {
	rfc := func() *Portal { return &Portal{Map: portalMap, Point: portalPoint} }
	for _, c := range []struct {
		name            string
		agentMap        uint32
		pos             Point
		team, agentTeam string
		portal          *Portal
	}{
		{"on the portal ground", portalMap, portalPoint, "", "", rfc()},
		{"five yards below the portal", portalMap, Point{X: 1818.4, Y: -4427.26, Z: -15.4478}, "", "", rfc()},
		{"twenty yards across", portalMap, Point{X: 1838.4, Y: -4427.26, Z: -19.4478}, "", "", rfc()},
		{"another map", outsideMap, Point{X: 1819.4, Y: -4426.26, Z: -30}, "", "", rfc()},
		{"the same team in another case", portalMap, portalPoint, "Horde", "horde", rfc()},
		{"no team in the run", portalMap, portalPoint, "", "alliance", rfc()},
		{"no team from the commander", portalMap, portalPoint, "horde", "", rfc()},
	} {
		t.Run(c.name, func(t *testing.T) {
			g := newHireFake(t)
			g.agentMap = c.agentMap
			g.team = c.agentTeam
			g.plan.Portal = c.portal
			e := hireSpotEnv(g, &hirePosFake{hireFake: g, pos: c.pos}, func(string) {})
			e.team = c.team
			if p := e.hireSpotProblem(); p != "" {
				t.Errorf("the spot is allowed: %s", p)
			}
		})
	}
}

// A game that gives no agent position cannot check the portal ground: it hires and says so.
func TestHireSpotWithoutPosition(t *testing.T) {
	g := newHireFake(t)
	g.agentMap = portalMap
	g.plan.Portal = &Portal{Map: portalMap, Point: portalPoint}
	var notes []string
	e := hireSpotEnv(g, g, func(s string) { notes = append(notes, s) })
	if p := e.hireSpotProblem(); p != "" {
		t.Fatalf("a game without an agent position refused the spot: %s", p)
	}
	if !strings.Contains(strings.Join(notes, "\n"), "the portal ground is not checked") {
		t.Fatalf("the skipped portal check is not noted: %v", notes)
	}
}
