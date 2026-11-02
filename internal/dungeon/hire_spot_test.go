package dungeon

import (
	"cmp"
	"context"
	"math"
	"slices"
	"strings"
	"testing"
)

// The hire spot refusals (#266): hires appear at the commander and muster there, so the commander must not stand under the portal's
// ground and must be of the run's team. The portal ground is the navmesh floor below the trigger centre, not the trigger centre's own z.

const portalMap = 1

// portalPoint is Ragefire Chasm's portal on the outer map (dungeons/ragefire_chasm.json). (1821.5,-4425.3) near it carries BOTH floors,
// -30.6 (the lower level) and -20.3 (the portal ground): the RFC fixture for the settled-lower, settled-upper and guessed cases.
var portalPoint = Point{X: 1818.4, Y: -4427.26, Z: -10.4478}

// hirePosFake is the hire fake with a game that knows where the commander stands and whether its height is a guess (AgentPositioner)
// and, when floors is set, the navmesh floors at a point (FloorLister). A plain hireFake gives neither.
type hirePosFake struct {
	*hireFake
	pos      Point
	zUnknown bool
	floors   map[[2]float32][]float32
}

func (g *hirePosFake) AgentPosition() (Point, bool) { return g.pos, g.zUnknown }

// FloorsAt answers the fixture floors of the nearest point within a yard, highest first, within 12 yd of z, as the navmesh does.
func (g *hirePosFake) FloorsAt(ctx context.Context, mapID uint32, x, y, z float32) ([]float32, bool) {
	return g.FloorsWithin(ctx, mapID, x, y, z-12, z+12)
}

// FloorsWithin answers the fixture floors of the nearest point within a yard, highest first, between lo and hi.
func (g *hirePosFake) FloorsWithin(_ context.Context, _ uint32, x, y, lo, hi float32) ([]float32, bool) {
	best := math.Inf(1)
	var out []float32
	found := false
	for k, zs := range g.floors {
		if d := math.Hypot(float64(k[0]-x), float64(k[1]-y)); d <= 1 && d < best {
			best, found = d, true
			out = out[:0]
			for _, z := range zs {
				if z >= lo && z <= hi {
					out = append(out, z)
				}
			}
		}
	}
	slices.SortFunc(out, func(a, b float32) int { return cmp.Compare(b, a) })
	return out, found && len(out) > 0
}

// hireSpotEnv is the prepare environment of the hire step over a plan, the way remote_prepare.go builds it.
func hireSpotEnv(g *hireFake, own Preparer, note func(string)) *prepEnv {
	clk := g.clock()
	return &prepEnv{g: own, plan: g.plan, spec: PrepareSpec{Party: PartyHire}, remote: true, maintain: true,
		now: clk.Now, sleep: clk.Sleep, note: note, command: g.Command}
}

// A commander who stands on a floor more than 5 yd below the portal ground hires nothing at all: the hires would appear and muster there,
// out of reach of the instance's entrance.
func TestHireRefusesBelowThePortalGround(t *testing.T) {
	g := newHireFake(t)
	g.agentMap = portalMap
	g.plan.Portal = &Portal{Map: portalMap, Point: portalPoint}
	pos := &hirePosFake{hireFake: g, pos: Point{X: 1821.5, Y: -4425.3, Z: -30.5}, floors: map[[2]float32][]float32{
		{portalPoint.X, portalPoint.Y}: {-20.306},
		{1821.5, -4425.3}:              {-30.6},
	}}
	e := hireSpotEnv(g, pos, func(string) {})
	_, err := e.hire(context.Background())
	if err == nil || !strings.Contains(err.Error(), "portal ground") || !strings.Contains(err.Error(), "below") {
		t.Fatalf("a hire under the portal ground: %v", err)
	}
	if len(g.hireLines) != 0 || g.count("cbind") != 0 {
		t.Fatalf("a hire under the portal still hired: hires %v, binds %d", g.hireLines, g.count("cbind"))
	}
}

// A guessed commander height (a far teleport leaves status.z_unknown set for good: walk 23) never refuses: it may sit on any floor at its
// x/y, so with floors stacked at the commander's x/y the step warns naming both candidate floors and hires anyway. A known height picks
// the floor at its own x/y nearest that height: known on the lower floor refuses, known on the upper floor hires.
func TestHireSpotStackedFloors(t *testing.T) {
	for _, c := range []struct {
		name     string
		zUnknown bool
		z        float32
		want     string
		notes    string
		warns    bool
	}{
		{"known z on the lower floor", false, -30.6, "portal ground", "", false},
		{"known z on the upper floor", false, -20.3, "", "", false},
		{"unknown z", true, -20.3, "", "lower floor -30.6 or the portal ground -20.3", true},
		{"unknown z 20 yd too high", true, -0.3, "", "", false},
	} {
		t.Run(c.name, func(t *testing.T) {
			g := newHireFake(t)
			g.agentMap = portalMap
			g.plan.Portal = &Portal{Map: portalMap, Point: portalPoint}
			var notes []string
			pos := &hirePosFake{hireFake: g, pos: Point{X: 1821.5, Y: -4425.3, Z: c.z}, zUnknown: c.zUnknown,
				floors: map[[2]float32][]float32{
					{portalPoint.X, portalPoint.Y}: {-20.306},
					{1821.5, -4425.3}:              {-20.3, -30.6},
				}}
			e := hireSpotEnv(g, pos, func(s string) { notes = append(notes, s) })
			p, err := e.hireSpotProblem(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			if c.want == "" {
				if p != "" {
					t.Fatalf("allowed: %q", p)
				}
			} else if !strings.Contains(p, c.want) {
				t.Fatalf("refused: %q, want %q", p, c.want)
			}
			if !c.warns {
				if strings.Contains(strings.Join(notes, "\n"), "own height is a guess") {
					t.Fatalf("an unexpected guess warning: %v", notes)
				}
				return
			}
			if !strings.Contains(strings.Join(notes, "\n"), c.notes) {
				t.Fatalf("no warning naming both floors: %v", notes)
			}
		})
	}
}

// One floor within 30 yd of a guessed height is the commander's floor: Wailing Caverns' portal has one, so a guessed height there hires
// with no warning (the widened look does not invent a lower level).
func TestHireSpotGuessedHeightWithOneFloor(t *testing.T) {
	g := newHireFake(t)
	g.agentMap = portalMap
	g.plan.Portal = &Portal{Map: portalMap, Point: Point{X: -753.596, Y: -2212.78, Z: 21.5403}}
	pos := &hirePosFake{hireFake: g, pos: Point{X: -753.4, Y: -2212.8, Z: 30}, zUnknown: true,
		floors: map[[2]float32][]float32{
			{-753.596, -2212.78}: {15.4},
			{-753.4, -2212.8}:    {15.4},
		}}
	e := hireSpotEnv(g, pos, func(string) {})
	if p, err := e.hireSpotProblem(context.Background()); err != nil || p != "" {
		t.Fatalf("a guessed height with one floor is allowed: %s %v", p, err)
	}
}

// The review's real samples stand below their trigger centre but on the portal ground floor: the floor snap allows them (WC 6.1 yd, RFC
// 9.4 yd, Sunken Temple 5.2 yd and Dire Maul East 4.6 yd under the trigger). A guessed height on a single floor does not matter: the
// floor at the commander's x/y is the same.
func TestHireSpotAllowsTheRealPortalSamples(t *testing.T) {
	rfc := Point{X: 1818.4, Y: -4427.26, Z: -10.4478}
	for _, c := range []struct {
		name            string
		agentMap        uint32
		portal          Portal
		portalFloors    []float32
		pos             Point
		zUnknown        bool
		commanderFloors []float32
	}{
		{"wailing caverns", 1, Portal{Map: 1, Point: Point{X: -753.596, Y: -2212.78, Z: 21.5403}},
			[]float32{15.4}, Point{X: -753.4, Y: -2212.8, Z: 15.4}, false, []float32{15.4}},
		{"ragefire chasm", 1, Portal{Map: 1, Point: rfc},
			[]float32{-20.306}, Point{X: 1821.5, Y: -4425.3, Z: -19.8}, false, []float32{-20.3, -30.6}},
		{"sunken temple", 0, Portal{Map: 0, Point: Point{X: -10162.7, Y: -3998.65, Z: -107.978}},
			[]float32{-113.443}, Point{X: -10168, Y: -3997.1, Z: -113.2}, false, []float32{-113.66}},
		{"dire maul east", 1, Portal{Map: 1, Point: Point{X: -3981.58, Y: 771.193, Z: 160.962}},
			[]float32{156.4}, Point{X: -3976.1, Y: 773.2, Z: 156.4}, false, []float32{156.4}},
		{"a guessed height on the portal ground", 1, Portal{Map: 1, Point: rfc},
			[]float32{-20.306}, Point{X: 1821.5, Y: -4425.3, Z: -30}, true, []float32{-20.3, -30.6}},
	} {
		t.Run(c.name, func(t *testing.T) {
			g := newHireFake(t)
			g.agentMap = c.agentMap
			g.plan.Portal = &c.portal
			pos := &hirePosFake{hireFake: g, pos: c.pos, zUnknown: c.zUnknown, floors: map[[2]float32][]float32{
				{c.portal.X, c.portal.Y}: c.portalFloors,
				{c.pos.X, c.pos.Y}:       c.commanderFloors,
			}}
			e := hireSpotEnv(g, pos, func(string) {})
			if p, err := e.hireSpotProblem(context.Background()); err != nil || p != "" {
				t.Errorf("the spot is allowed: %s %v", p, err)
			}
		})
	}
}

// No navmesh for the map means no portal-ground check, with a note.
func TestHireSpotWithoutNavmesh(t *testing.T) {
	g := newHireFake(t)
	g.agentMap = portalMap
	g.plan.Portal = &Portal{Map: portalMap, Point: portalPoint}
	var notes []string
	pos := &hirePosFake{hireFake: g, pos: Point{X: 1818.4, Y: -4427.26, Z: -30}}
	e := hireSpotEnv(g, pos, func(s string) { notes = append(notes, s) })
	if p, err := e.hireSpotProblem(context.Background()); err != nil || p != "" {
		t.Fatalf("no navmesh refused the spot: %s %v", p, err)
	}
	if !strings.Contains(strings.Join(notes, "\n"), "hire: portal ground not checked (no navmesh)") {
		t.Fatalf("the skipped portal check is not noted: %v", notes)
	}
}

// A commander that stands in a kept binding's spot after a resume hires nothing new: the spot is not judged (nothing appears there).
func TestHireSpotSkippedForAKeptBinding(t *testing.T) {
	g := newHireFake(t)
	for _, c := range [][2]string{{"warrior", "tank"}, {"priest", "healer"}, {"hunter", "dps"}, {"rogue", "dps"}, {"warlock", "dps"}} {
		g.preHire(c[0], c[1], 43)
	}
	g.nextName, g.raid = 5, true
	g.preBind()
	g.agentMap = portalMap
	g.plan.Portal = &Portal{Map: portalMap, Point: portalPoint}
	pos := &hirePosFake{hireFake: g, pos: Point{X: 1821.5, Y: -4425.3, Z: -30.5}, floors: map[[2]float32][]float32{
		{portalPoint.X, portalPoint.Y}: {-20.306},
		{1821.5, -4425.3}:              {-30.6},
	}}
	e := hireSpotEnv(g, pos, func(string) {})
	if p, err := e.hireSpotProblem(context.Background()); err != nil || p == "" {
		t.Fatalf("the spot itself is refused: %q %v", p, err)
	}
	if _, err := e.hire(context.Background()); err != nil {
		t.Fatalf("a kept binding is refused by the spot: %v", err)
	}
	if len(g.hireLines) != 0 {
		t.Fatalf("a kept binding hired: %v", g.hireLines)
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

// The spot is fine on the portal ground, on another map, and whenever a team is unknown or the same in another case.
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
			if p, err := e.hireSpotProblem(context.Background()); err != nil || p != "" {
				t.Errorf("the spot is allowed: %s %v", p, err)
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
	if p, err := e.hireSpotProblem(context.Background()); err != nil || p != "" {
		t.Fatalf("a game without an agent position refused the spot: %s %v", p, err)
	}
	if !strings.Contains(strings.Join(notes, "\n"), "the portal ground is not checked") {
		t.Fatalf("the skipped portal check is not noted: %v", notes)
	}
}
