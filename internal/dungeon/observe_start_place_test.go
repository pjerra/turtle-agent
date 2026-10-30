package dungeon

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/pjerra/turtle-agent/internal/tbm"
)

// observeStartFake is the run leader of observe_test.go with the agent's own GM character of start_test.go: an observed run that starts
// partway (from_leg) and moves the party there.
type observeStartFake struct {
	*startFake
	leader *fakeLeader
}

func newObserveStartFake(t *testing.T, script ...leaderStep) *observeStartFake {
	l := newFakeLeader(t, script...)
	sf := &startFake{fakeRemote: l.fakeRemote, level: 3, known: true, agentMap: 1, agent: Point{-500, 0, 0}, apart: map[string]bool{},
		arrive: map[string]time.Time{}, stuckBots: map[string]bool{}}
	l.fakeRemote.onTick = func(g *fakeRemote) {
		sf.tick(g)
		l.advanceLeader()
	}
	return &observeStartFake{startFake: sf, leader: l}
}

func (g *observeStartFake) Command(ctx context.Context, command string) (tbm.Reply, error) {
	if strings.HasPrefix(command, "run ") {
		return g.leader.Command(ctx, command)
	}
	return g.startFake.Command(ctx, command)
}

func (g *observeStartFake) RunStatus() (tbm.RunStatus, uint64, bool) { return g.leader.RunStatus() }

var _ RunLeaderWatcher = (*observeStartFake)(nil)

// An observed from_leg start moves the party to the run leader's own start step (PartialStart.Point: the first row of the leg in the
// deployed rows), not to where the plan file's leg before it ended (LegStart): the run leader walks its rows from where the party
// stands, and the place check measures the party against the start step. Live 2026-10-30 (dragonmaw_retreat, from_leg searistrasz): the
// GM move went to the plan file's leg-before boss (Gowlfang, 750 yd from the leg's first row) and the run failed.
func TestObserveFromLegMovesThePartyToTheStartStep(t *testing.T) {
	script := []leaderStep{
		{0, rs("running", 4, 5, "second", "move", "walk", "walking")},
		{s(20), rs("done", 5, 5, "second", "move", "walk", "plan done")},
	}
	g := newObserveStartFake(t, script...)
	g.leader.startExtra = []string{"The run starts at step 4 of 5 (leg second): the party is in the instance."}
	o := observeOpts(ObserveSpec{})
	o.Preflight = PreflightOff
	step := Point{170, 0, 0} // the leg's first row; the plan's leg 0 ends at its To 100 0 0
	o.StartFrom = func(int) (PartialStart, error) {
		return PartialStart{Words: "from leg second", Leg: 1, LegNumber: 1, LegName: "second", Point: step, HavePoint: true}, nil
	}
	rep, err := runFrom(t, g, observePlan(), o, 1)
	if err != nil || rep.State != "done" {
		t.Fatalf("%v %s\n%s", err, rep.State, notesOf(rep))
	}
	if fmt.Sprint(g.gos) != "[170 0 0 0]" {
		t.Fatalf("the GM move went to %v, want the start step 170 0 0 (not the leg before's end 100 0 0)", g.gos)
	}
	if rep.Start == nil || rep.Start.Point != step || rep.Start.Kind != StartStep {
		t.Fatalf("start %+v", rep.Start)
	}
	if !strings.Contains(strings.Join(g.orders, "\n"), "run start rt from leg second") {
		t.Fatalf("orders %v", g.orders)
	}
}

// A start step with no known point (the deployed rows were not reproduced from the plan file) moves nobody: the place check then refuses
// the start, before `run start` (observe.allow_far_start overrides), instead of a GM move to a point of the plan file.
func TestObserveFromLegWithoutAStartPointMovesNobody(t *testing.T) {
	g := newObserveStartFake(t, happyScript()...)
	o := observeOpts(ObserveSpec{})
	o.Preflight = PreflightOff
	o.StartFrom = func(int) (PartialStart, error) {
		return PartialStart{Words: "from leg second", Leg: 1, LegNumber: 1, LegName: "second"}, nil
	}
	_, err := runFrom(t, g, observePlan(), o, 1)
	if err == nil || !strings.Contains(err.Error(), "start step's point is not known") {
		t.Fatalf("%v", err)
	}
	if len(g.gos) != 0 || len(g.appears) != 0 {
		t.Fatalf("a GM move with no start point: gos %v appears %v", g.gos, g.appears)
	}
	if g.indexOf("run start") >= 0 {
		t.Fatalf("the run was started: %v", g.orders)
	}
	// Refused right after the probe: no prepare, bind or entry past `run list` (Codex adversarial review 2026-10-30).
	if fmt.Sprint(g.orders) != "[run list]" {
		t.Fatalf("sent past the probe: %v", g.orders)
	}
}

// The unsafe-start gate of an observed from_leg start measures the run leader's start step, the point the party is moved to, not the
// plan file's LegStart: a step 2 yd from a pack of a skipped leg in a fresh instance is refused before any GM move (Codex review
// 2026-10-30), though the leg before's end (100 0 0) is 72 yd from that pack.
func TestObserveFromLegGatesTheStartStep(t *testing.T) {
	p := remotePlan(
		Leg{Name: "first", To: Point{100, 0, 0}, PullGroups: []PullGroup{{Entries: []uint32{1}, Positions: []Point{{172, 0, 0}}}}},
		Leg{Name: "second", To: Point{200, 0, 0}},
	)
	p.OutsideTele = "uldaman"
	g := newObserveStartFake(t, happyScript()...)
	o := observeOpts(ObserveSpec{})
	o.Preflight = PreflightOff
	o.Prepare = PrepareSpec{Fresh: true}
	o.StartFrom = func(int) (PartialStart, error) {
		return PartialStart{Words: "from leg second", Leg: 1, LegNumber: 1, LegName: "second", Point: Point{170, 0, 0}, HavePoint: true}, nil
	}
	_, err := runFrom(t, g, p, o, 1)
	if err == nil || !strings.Contains(err.Error(), "the run leader's start step 170 0 0 is 2 yd from pull group 1") {
		t.Fatalf("%v", err)
	}
	// Nothing past the probe: no bind, no instance entry, no GM line, no move (Codex adversarial review 2026-10-30).
	if len(g.gos) != 0 || len(g.appears) != 0 || fmt.Sprint(g.orders) != "[run list]" {
		t.Fatalf("sent past the probe: gos %v appears %v orders %v", g.gos, g.appears, g.orders)
	}
	for _, c := range g.calls {
		if !strings.HasPrefix(c, "command run list") {
			t.Fatalf("a call past the probe: %v", g.calls)
		}
	}
}
