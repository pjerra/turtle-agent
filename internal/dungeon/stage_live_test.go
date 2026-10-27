package dungeon

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/pjerra/turtle-agent/internal/navmesh"
	"github.com/pjerra/turtle-agent/internal/tbm"
)

// stageFake is the StageGame fake: a scripted world with a fake clock. Now
// only moves when Sleep is called; SummonBot lands the named bot at the
// commander's point unless neverArrive names it.
type stageFake struct {
	mu           sync.Mutex
	now          time.Time
	account      int
	accountKnown bool
	agent        Point
	agentMap     uint32
	lead         string
	members      []string
	dead         map[string]bool
	units        []StageUnit
	unitsSeq     [][]StageUnit
	summonTimes  map[string][]time.Time
	gm           []string
	commands     []string
	neverArrive  map[string]bool
	inCombat     map[string]bool
	statusLines  []string
	statusErr    *tbm.Err
	runLine      string
	runSent      []string
	floorZ       string
	inView       map[string]Point
	runState     tbm.RunStatus
	runSeq       uint64
	runHave      bool
	runSeqs      []tbm.RunStatus
	listReply    []string
}

func newStageFake() *stageFake {
	return &stageFake{
		now:          time.Date(2026, 10, 27, 12, 0, 0, 0, time.UTC),
		account:      1,
		accountKnown: true,
		agentMap:     1,
		lead:         "Lead",
		members:      []string{"Lead", "B1", "B2", "B3", "B4"},
		dead:         map[string]bool{},
		summonTimes:  map[string][]time.Time{},
		inCombat:     map[string]bool{},
		inView:       map[string]Point{},
		runSeq:       1,
		runHave:      true,
		listReply:    []string{"TBM:ACTION_ACK|run list|plans|0|ok", "Dungeon plans: t (map 1, 10 steps)"},
	}
}

func (f *stageFake) Now() time.Time {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.now
}

func (f *stageFake) CommandWait(ctx context.Context, command string, wait time.Duration) (tbm.Reply, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.commands = append(f.commands, command)
	switch {
	case command == "run status":
		return tbm.Reply{Kind: tbm.KindAck, OK: true, Lines: f.statusLines}, nil
	case command == "run list":
		return tbm.Reply{Kind: tbm.KindAck, OK: true, Lines: f.listReply}, nil
	case strings.HasPrefix(command, "run start "):
		f.runSent = append(f.runSent, command)
		if f.statusErr != nil {
			return tbm.Reply{Kind: tbm.KindErr, Err: f.statusErr, Lines: []string{}}, nil
		}
		return tbm.Reply{Kind: tbm.KindAck, OK: true, Lines: []string{f.runLine}}, nil
	case command == "run resume":
		f.runSent = append(f.runSent, command)
		if f.statusErr != nil {
			return tbm.Reply{Kind: tbm.KindErr, Err: f.statusErr, Lines: []string{}}, nil
		}
		if f.runLine != "" {
			return tbm.Reply{Kind: tbm.KindAck, OK: true, Lines: []string{f.runLine}}, nil
		}
		return tbm.Reply{Kind: tbm.KindAck, OK: true}, nil
	case command == "run stop":
		f.runSent = append(f.runSent, command)
		return tbm.Reply{Kind: tbm.KindAck, OK: true}, nil
	case command == "commander status":
		names := []string{}
		for _, m := range f.members {
			n := m
			if f.dead[m] {
				n += " (dead)"
			}
			names = append(names, n)
		}
		lines := []string{fmt.Sprintf("You command %s's group: %s.", f.lead, strings.Join(names, ", "))}
		return tbm.Reply{Kind: tbm.KindAck, OK: true,
			Ack:   &tbm.Ack{Intent: "commander status", Scope: "party", Count: len(f.members), Executor: f.lead},
			Lines: lines}, nil
	}
	return tbm.Reply{Kind: tbm.KindAck, OK: true}, nil
}

func (f *stageFake) AccountLevel(ctx context.Context) (int, bool, error) {
	return f.account, f.accountKnown, nil
}

func (f *stageFake) AgentPlace() (Point, uint32, bool) { return f.agent, f.agentMap, true }

func (f *stageFake) Appear(ctx context.Context, player string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.gm = append(f.gm, ".appear "+player)
	f.agentMap = 1
	return nil
}

func (f *stageFake) GoXYZ(ctx context.Context, mapID uint32, at Point) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.gm = append(f.gm, fmt.Sprintf(".go xyz %.2f %.2f %.2f %d", at.X, at.Y, at.Z, mapID))
	f.agent, f.agentMap = at, mapID
	return nil
}

func (f *stageFake) GPS(ctx context.Context) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.gm = append(f.gm, ".gps")
	return f.floorZ, nil
}

func (f *stageFake) SummonBot(ctx context.Context, name string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.gm = append(f.gm, ".summon "+name)
	f.summonTimes[name] = append(f.summonTimes[name], f.now)
	if !f.neverArrive[name] {
		f.inView[name] = f.agent
	}
	return nil
}

func (f *stageFake) StageUnits() []StageUnit {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.unitsSeq) > 0 {
		i := len(f.commands)
		if i >= len(f.unitsSeq) {
			i = len(f.unitsSeq) - 1
		}
		out := append([]StageUnit{}, f.unitsSeq[i]...)
		for name, at := range f.inView {
			found := false
			for _, u := range out {
				if strings.EqualFold(u.Name, name) {
					found = true
				}
			}
			if !found {
				out = append(out, StageUnit{Name: name, Player: true, At: at})
			}
		}
		return out
	}
	out := append([]StageUnit{}, f.units...)
	for name, at := range f.inView {
		found := false
		for _, u := range out {
			if strings.EqualFold(u.Name, name) {
				found = true
			}
		}
		if !found {
			out = append(out, StageUnit{Name: name, Player: true, At: at})
		}
	}
	return out
}

func (f *stageFake) RunStatus() (tbm.RunStatus, uint64, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.runSeqs) > 0 {
		i := len(f.runSent)
		if i >= len(f.runSeqs) {
			i = len(f.runSeqs) - 1
		}
		f.runSeq++
		return f.runSeqs[i], f.runSeq, true
	}
	return f.runState, f.runSeq, f.runHave
}

func (f *stageFake) sleep(ctx context.Context, d time.Duration) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.now = f.now.Add(d)
	return nil
}

// atPoint puts the commander's agent at at so no .appear is sent.
func (f *stageFake) atPoint(at Point) {
	f.agent = at
	f.inView = map[string]Point{}
}

func mkLivePlan() *Plan {
	return &Plan{
		Name:     "t",
		Map:      1,
		Entrance: Point{X: 0, Y: 0, Z: 0},
		Legs: []Leg{
			{Name: "a", To: Point{X: 60, Y: 0, Z: 0}},
			{Name: "b", To: Point{X: 120, Y: 0, Z: 0}},
		},
	}
}

func liveReq(f *stageFake, p *Plan, leg int) StageRequest {
	return StageRequest{
		Plan: p, Leg: leg,
		Opt: StageOptions{ClearedThrough: -1},
		Nav: &stageNav{},
		FirstRow: func(serverSteps int) (Point, bool, string) {
			return Point{}, false, ""
		},
		QuietWait: 20 * time.Second,
	}
}

func runLive(t *testing.T, f *stageFake, req StageRequest) StageReport {
	t.Helper()
	s := &Stager{G: f, Now: f.Now, Sleep: f.sleep, Poll: time.Millisecond}
	rep, err := s.Stage(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	return rep
}

// 13. The summon burst: gm_commands order exactly .go xyz, .gps, .summon
// Lead, B1..B4; all SummonBot calls share one Now().
func TestStageLiveSummonsTheWholePartyInOneBurst(t *testing.T) {
	p := mkLivePlan()
	f := newStageFake()
	f.atPoint(Point{X: 120, Y: 0, Z: 0})
	f.units = []StageUnit{{Name: "Lead", Player: true, At: Point{X: 120, Y: 0, Z: 0}}}
	rep := runLive(t, f, liveReq(f, p, 1))
	if !rep.Staged {
		t.Fatalf("not staged: %+v", rep)
	}
	want := []string{".go xyz 120.00 0.00 0.00 1", ".gps", ".summon Lead", ".summon B1", ".summon B2", ".summon B3", ".summon B4"}
	if len(f.gm) != len(want) {
		t.Fatalf("gm %v, want %v", f.gm, want)
	}
	for i, w := range want {
		if f.gm[i] != w {
			t.Fatalf("gm[%d] %q, want %q (all %v)", i, f.gm[i], w, f.gm)
		}
	}
	var first time.Time
	for _, m := range []string{"Lead", "B1", "B2", "B3", "B4"} {
		ts := f.summonTimes[m]
		if len(ts) != 1 {
			t.Fatalf("%s summoned %d times: %v", m, len(ts), ts)
		}
		if first.IsZero() {
			first = ts[0]
		} else if !ts[0].Equal(first) {
			t.Fatalf("summon times differ: %v vs %v", first, ts[0])
		}
	}
}

// 14. Agent on map 0: the first GM line is .appear Lead.
func TestStageLiveAppearsWhenOutsideTheInstance(t *testing.T) {
	f := newStageFake()
	f.agentMap = 0
	f.units = []StageUnit{{Name: "Lead", Player: true, At: Point{X: 120, Y: 0, Z: 0}}}
	rep := runLive(t, f, liveReq(f, mkLivePlan(), 1))
	if !rep.Staged {
		t.Fatalf("not staged: %+v", rep)
	}
	if len(f.gm) == 0 || f.gm[0] != ".appear Lead" {
		t.Fatalf("first GM line %v", f.gm)
	}
}

// 15. A hostile at 15 yd refuses "is not quiet" and nothing is summoned.
func TestStageLiveRefusesANoisySpot(t *testing.T) {
	f := newStageFake()
	f.atPoint(Point{X: 120, Y: 0, Z: 0})
	f.units = []StageUnit{
		{Name: "Lead", Player: true, At: Point{X: 120, Y: 0, Z: 0}},
		{Name: "Skipped", Entry: 100, Hostile: true, At: Point{X: 135, Y: 0, Z: 0}},
	}
	rep := runLive(t, f, liveReq(f, mkLivePlan(), 1))
	if rep.Staged {
		t.Fatalf("staged into a noisy spot: %+v", rep)
	}
	if !strings.Contains(rep.Refusal, "is not quiet") {
		t.Errorf("no quiet refusal: %q", rep.Refusal)
	}
	for _, g := range f.gm {
		if strings.HasPrefix(g, ".summon") {
			t.Errorf("summoned at a noisy spot: %v", f.gm)
		}
	}
}

// 16. A roamer at 60 yd 2D moving 10 yd between the reads is refused.
func TestStageLiveRefusesARoamer(t *testing.T) {
	f := newStageFake()
	f.atPoint(Point{X: 120, Y: 0, Z: 0})
	here := []StageUnit{
		{Name: "Lead", Player: true, At: Point{X: 120, Y: 0, Z: 0}},
		{Name: "Roamer", Entry: 200, Hostile: true, At: Point{X: 180, Y: 0, Z: 0}},
	}
	moved := []StageUnit{
		{Name: "Lead", Player: true, At: Point{X: 120, Y: 0, Z: 0}},
		{Name: "Roamer", Entry: 200, Hostile: true, Moving: true, At: Point{X: 190, Y: 0, Z: 0}},
	}
	f.unitsSeq = [][]StageUnit{here, moved}
	rep := runLive(t, f, liveReq(f, mkLivePlan(), 1))
	if rep.Staged {
		t.Fatalf("staged with a roamer: %+v", rep)
	}
	if !strings.Contains(rep.Refusal, "is not quiet") {
		t.Errorf("no quiet refusal: %q", rep.Refusal)
	}
}

// 17. A hostile at 25 yd matching an own pull group (entry + place) stages.
func TestStageLiveAllowsTheLegsOwnPackAt25Yd(t *testing.T) {
	p := mkLivePlan()
	p.Legs[1].PullGroups = []PullGroup{{Entries: []uint32{9319}, Positions: []Point{{X: 145, Y: 0, Z: 0}}}}
	f := newStageFake()
	f.atPoint(Point{X: 120, Y: 0, Z: 0})
	f.units = []StageUnit{
		{Name: "Lead", Player: true, At: Point{X: 120, Y: 0, Z: 0}},
		{Name: "Own", Entry: 9319, Hostile: true, At: Point{X: 145, Y: 0, Z: 0}},
	}
	rep := runLive(t, f, liveReq(f, p, 1))
	if !rep.Staged {
		t.Fatalf("refused an own pack at 25 yd: %q %+v", rep.Refusal, rep)
	}
}

// 18. B2 never arrives: exactly two .summon B2, refusal names "B2 not in view".
func TestStageLiveRetriesTheSummonOnceThenFails(t *testing.T) {
	f := newStageFake()
	f.atPoint(Point{X: 120, Y: 0, Z: 0})
	f.neverArrive = map[string]bool{"B2": true}
	f.units = []StageUnit{{Name: "Lead", Player: true, At: Point{X: 120, Y: 0, Z: 0}}}
	rep := runLive(t, f, liveReq(f, mkLivePlan(), 1))
	if rep.Staged {
		t.Fatalf("staged without B2: %+v", rep)
	}
	n := 0
	for _, g := range f.gm {
		if g == ".summon B2" {
			n++
		}
	}
	if n != 2 {
		t.Errorf(".summon B2 sent %d times, want 2: %v", n, f.gm)
	}
	if !strings.Contains(rep.Refusal, "B2 not in view") {
		t.Errorf("no B2 arrival refusal: %q", rep.Refusal)
	}
}

// 19. run start with a begin line and running status: started, start_step 7.
func TestStageLiveStartWaitsForTheBeginLine(t *testing.T) {
	f := newStageFake()
	f.atPoint(Point{X: 120, Y: 0, Z: 0})
	f.units = []StageUnit{{Name: "Lead", Player: true, At: Point{X: 120, Y: 0, Z: 0}}}
	f.runLine = "The run starts at step 7 of 20 (leg b)"
	f.runSeqs = []tbm.RunStatus{
		{Plan: "t", State: tbm.RunRunning, Step: 7, Steps: 20, Leg: "b"},
	}
	req := liveReq(f, mkLivePlan(), 1)
	req.Start = true
	rep := runLive(t, f, req)
	if !rep.Started {
		t.Fatalf("not started: %+v", rep)
	}
	if rep.Run == nil || rep.Run.StartStep != 7 {
		t.Fatalf("bad run report: %+v", rep.Run)
	}
}

// 20. RunStatus leg a after starting leg b: run stop sent, refusal names
// "started in leg a, not b".
func TestStageLiveStopsOnALegMismatch(t *testing.T) {
	f := newStageFake()
	f.atPoint(Point{X: 120, Y: 0, Z: 0})
	f.units = []StageUnit{{Name: "Lead", Player: true, At: Point{X: 120, Y: 0, Z: 0}}}
	f.runLine = "The run starts at step 1 of 20 (leg b)"
	f.runSeqs = []tbm.RunStatus{
		{Plan: "t", State: tbm.RunRunning, Step: 1, Steps: 20, Leg: "a"},
	}
	req := liveReq(f, mkLivePlan(), 1)
	req.Start = true
	rep := runLive(t, f, req)
	if rep.Started {
		t.Fatalf("started in the wrong leg: %+v", rep)
	}
	if !strings.Contains(rep.Refusal, "started in leg a, not b") {
		t.Errorf("no mismatch refusal: %q", rep.Refusal)
	}
	found := false
	for _, s := range f.runSent {
		if s == "run stop" {
			found = true
		}
	}
	if !found {
		t.Errorf("no run stop sent: %v", f.runSent)
	}
}

// 21. Running and paused runs are refused without resume.
func TestStageLiveRefusesARunningRun(t *testing.T) {
	f := newStageFake()
	f.statusLines = []string{"TBM:RUN|t|running|5/20|b|move|walk|going|"}
	rep := runLive(t, f, liveReq(f, mkLivePlan(), 1))
	if rep.Staged || !strings.Contains(rep.Refusal, "has a run going") {
		t.Errorf("running not refused: %+v", rep)
	}
}

func TestStageLiveRefusesAPausedRunWithoutResume(t *testing.T) {
	f := newStageFake()
	f.statusLines = []string{"TBM:RUN|t|paused|5/20|b|move|walk|waiting|"}
	rep := runLive(t, f, liveReq(f, mkLivePlan(), 1))
	if rep.Staged || !strings.Contains(rep.Refusal, "has a paused run") {
		t.Errorf("paused not refused: %+v", rep)
	}
}

// 22. A dead listing member refuses with "B1 is dead".
func TestStageLiveRefusesADeadMember(t *testing.T) {
	f := newStageFake()
	f.dead = map[string]bool{"B1": true}
	f.atPoint(Point{X: 120, Y: 0, Z: 0})
	f.units = []StageUnit{{Name: "Lead", Player: true, At: Point{X: 120, Y: 0, Z: 0}}}
	rep := runLive(t, f, liveReq(f, mkLivePlan(), 1))
	if rep.Staged || !strings.Contains(rep.Refusal, "B1 is dead") {
		t.Errorf("dead member not refused: %+v", rep)
	}
}

// 23. A player account refuses; nothing is sent.
func TestStageLiveRefusesAPlayerAccount(t *testing.T) {
	f := newStageFake()
	f.account, f.accountKnown = 0, true
	f.atPoint(Point{X: 120, Y: 0, Z: 0})
	f.units = []StageUnit{{Name: "Lead", Player: true, At: Point{X: 120, Y: 0, Z: 0}}}
	rep := runLive(t, f, liveReq(f, mkLivePlan(), 1))
	if rep.Staged {
		t.Errorf("staged on a player account: %+v", rep)
	}
	if len(f.gm) != 0 {
		t.Errorf("GM lines sent to refuse: %v", f.gm)
	}
}

// 24. Dry run sends only run status and run list; no GM line.
func TestStageLiveDryRunSendsNothing(t *testing.T) {
	f := newStageFake()
	req := liveReq(f, mkLivePlan(), 1)
	req.DryRun = true
	rep := runLive(t, f, req)
	if rep.Staged || rep.Refusal != "" {
		t.Errorf("dry run staged or refused: %+v", rep)
	}
	for _, c := range f.commands {
		if c != "run status" && c != "run list" {
			t.Errorf("dry run sent %q: %v", c, f.commands)
		}
	}
	if len(f.gm) != 0 {
		t.Errorf("dry run sent GM lines: %v", f.gm)
	}
}

// 25. Force passes the quiet check; the warning holds the quiet refusal.
func TestStageLiveForcePassesTheQuietCheck(t *testing.T) {
	f := newStageFake()
	f.atPoint(Point{X: 120, Y: 0, Z: 0})
	f.units = []StageUnit{
		{Name: "Lead", Player: true, At: Point{X: 120, Y: 0, Z: 0}},
		{Name: "Skipped", Entry: 100, Hostile: true, At: Point{X: 135, Y: 0, Z: 0}},
	}
	req := liveReq(f, mkLivePlan(), 1)
	req.Opt.Force = true
	rep := runLive(t, f, req)
	if !rep.Staged {
		t.Fatalf("force did not stage: %+v", rep)
	}
	found := false
	for _, w := range rep.Warnings {
		if strings.Contains(w, "is not quiet") {
			found = true
		}
	}
	if !found {
		t.Errorf("no quiet warning: %+v", rep.Warnings)
	}
}

var _ = navmesh.PathNormal
