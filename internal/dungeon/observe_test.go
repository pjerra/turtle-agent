package dungeon

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/pjerra/turtle-agent/internal/tbm"
)

// leaderStep is a status the fake run leader reaches after `at` of running time.
type leaderStep struct {
	at time.Duration
	st tbm.RunStatus
}

// fakeLeader is a fakeRemote with a TortoiseBots run leader on top: `.bot run list|start|status|pause|resume|stop` answer, and a script of
// TBM:RUN statuses plays while the run runs (a paused run does not advance).
type fakeLeader struct {
	*fakeRemote
	plan         string // the plan the server has
	listText     string // the answer of `run list`; empty: the server has no run leader and says nothing
	startErr     string // the code `run start` is refused with
	script       []leaderStep
	noStopReason bool // a module from before TortoiseBots #1070: `run stop <reason>` is invalid
	pushOff      bool // the feed does not deliver the pushed lines: only `run status` shows them

	running, paused bool
	progress        time.Duration
	lastNow         time.Time
	next            int
	cur             tbm.RunStatus
	haveCur         bool
	n               uint64
	stopped         int
	onStatus        func(l *fakeLeader, st tbm.RunStatus) // called for each status the leader emits
}

func newFakeLeader(t *testing.T, script ...leaderStep) *fakeLeader {
	l := &fakeLeader{fakeRemote: newFakeRemote(t), plan: "rt", script: script}
	l.listText = "rt (map 0, 5 steps)"
	l.lastNow = l.now
	l.onTick = func(g *fakeRemote) { l.advanceLeader() }
	return l
}

func rs(state string, step, steps int, leg, kind, phase, note string) tbm.RunStatus {
	return tbm.RunStatus{Plan: "rt", State: state, Step: step, Steps: steps, Leg: leg, Kind: kind, Phase: phase, Note: note}
}

func runLine(st tbm.RunStatus) string {
	d := func(s string) string {
		if s == "" {
			return "-"
		}
		return s
	}
	return fmt.Sprintf("TBM:RUN|%s|%s|%d/%d|%s|%s|%s|%s", d(st.Plan), st.State, st.Step, st.Steps, d(st.Leg), d(st.Kind), d(st.Phase), d(st.Note))
}

func (l *fakeLeader) emit(st tbm.RunStatus) {
	l.cur, l.haveCur = st, true
	l.n++
	if l.onStatus != nil {
		l.onStatus(l, st)
	}
}

func (l *fakeLeader) advanceLeader() {
	d := l.now.Sub(l.lastNow)
	l.lastNow = l.now
	if !l.running || l.paused {
		return
	}
	l.progress += d
	for l.next < len(l.script) && l.progress >= l.script[l.next].at {
		l.emit(l.script[l.next].st)
		l.next++
	}
}

func (l *fakeLeader) RunStatus() (tbm.RunStatus, uint64, bool) {
	if l.pushOff {
		return tbm.RunStatus{}, 0, false
	}
	return l.cur, l.n, l.haveCur
}

func (l *fakeLeader) statusLines() []string {
	if !l.haveCur {
		return []string{"TBM:RUN|-|none|0/0|-|-|-|no run"}
	}
	return []string{runLine(l.cur)}
}

func (l *fakeLeader) Command(ctx context.Context, command string) (tbm.Reply, error) {
	f := strings.Fields(command)
	if f[0] != "run" {
		return l.fakeRemote.Command(ctx, command)
	}
	g := l.fakeRemote
	g.orders = append(g.orders, command)
	g.orderAt = append(g.orderAt, g.now)
	if g.panicOn != "" && strings.HasPrefix(command, g.panicOn) {
		g.panicOn = ""
		panic("boom")
	}
	intent := f[0] + " " + f[1]
	if l.listText == "" && f[1] == "list" {
		return tbm.Reply{Kind: tbm.KindTimeout, Lines: []string{}}, nil // an older server: .bot run is not a command
	}
	switch f[1] {
	case "list":
		a := tbm.Ack{Intent: intent, Scope: "plans", Count: 1, Executor: "-"}
		return tbm.Reply{Kind: tbm.KindAck, OK: true, Ack: &a, Lines: []string{"Dungeon plans: " + l.listText}}, nil
	case "start":
		if l.startErr != "" {
			return errReply(intent, l.startErr), nil
		}
		if l.running {
			return errReply(intent, "busy"), nil
		}
		if len(f) < 3 || f[2] != l.plan {
			return errReply(intent, "no-plan"), nil
		}
		l.running = true
		r := ack(intent)
		l.advanceLeader()
		r.Lines = l.statusLines()
		return r, nil
	case "status":
		r := ack(intent)
		r.Lines = l.statusLines()
		return r, nil
	case "pause":
		if !l.running || l.paused {
			return errReply(intent, "state"), nil
		}
		l.paused = true
		st := l.cur
		st.State, st.Note = tbm.RunPaused, "paused by .bot run pause"
		l.emit(st)
		r := ack(intent)
		r.Lines = l.statusLines()
		return r, nil
	case "resume":
		if !l.paused {
			return errReply(intent, "state"), nil
		}
		l.paused = false
		st := l.cur
		st.State, st.Note = tbm.RunRunning, "resumed"
		l.emit(st)
		r := ack(intent)
		r.Lines = l.statusLines()
		return r, nil
	case "stop":
		if len(f) > 2 && l.noStopReason {
			return errReply(intent, "invalid"), nil
		}
		if !l.running {
			return errReply(intent, "no-run"), nil
		}
		l.running, l.paused = false, false
		l.stopped++
		st := l.cur
		st.State, st.Note = tbm.RunDone, "stopped by .bot run stop"
		l.emit(st)
		r := ack(intent)
		r.Lines = l.statusLines()
		return r, nil
	}
	return errReply(intent, "invalid"), nil
}

var _ RunLeaderWatcher = (*fakeLeader)(nil)

func s(sec int) time.Duration { return time.Duration(sec) * time.Second }

// happyScript is a run of five steps in two legs.
func happyScript() []leaderStep {
	return []leaderStep{
		{0, rs("running", 1, 5, "first", "move", "walk", "walking to -1 2 3")},
		{s(5), rs("running", 2, 5, "first", "pull", "gather", "gathering")},
		{s(9), rs("running", 2, 5, "first", "pull", "pull", "pulling 299")},
		{s(20), rs("running", 3, 5, "first", "boss", "pull", "pulling boss")},
		{s(40), rs("running", 4, 5, "second", "move", "walk", "walking")},
		{s(50), rs("done", 5, 5, "second", "move", "walk", "plan done")},
	}
}

func observePlan() *Plan {
	return remotePlan(Leg{Name: "first", To: Point{100, 0, 0}}, Leg{Name: "second", To: Point{200, 0, 0}})
}

func observeOpts(spec ObserveSpec) RemoteOptions {
	o := opts()
	o.Observe = &spec
	return o
}

// cmdOrdersAfter are the `cmd` and `action` orders sent after the order at index i.
func cmdOrdersAfter(orders []string, i int) []string {
	var out []string
	for _, o := range orders[i+1:] {
		if strings.HasPrefix(o, "cmd ") || strings.HasPrefix(o, "action ") {
			out = append(out, o)
		}
	}
	return out
}

func TestObserveWatchesARunToTheEnd(t *testing.T) {
	g := newFakeLeader(t, happyScript()...)
	rep, err := runRemote(t, g, observePlan(), observeOpts(ObserveSpec{}), nil)
	if err != nil || rep.State != "done" {
		t.Fatalf("%v %s %s", err, rep.State, rep.Error)
	}
	// probe first (before anything is prepared), then the bind, then the start
	if g.orders[0] != "run list" || g.orders[1] != "commander bind Tanky Healy Magey" {
		t.Fatalf("orders: %v", g.orders)
	}
	start := g.indexOf("run start rt")
	if start < 0 {
		t.Fatalf("no run start: %v", g.orders)
	}
	// after the start the agent orders nothing: the only `cmd` is the end's stay, and that comes after the run is done
	after := cmdOrdersAfter(g.orders, start)
	if len(after) != 1 || after[0] != "cmd stay" {
		t.Fatalf("orders after the start: %v (all: %v)", after, g.orders)
	}
	if g.stopped != 0 || g.indexOf("run stop") >= 0 {
		t.Fatalf("a finished run was stopped: %v", g.orders)
	}
	if g.orders[len(g.orders)-1] != "commander unbind" {
		t.Fatalf("the binding is not let go: %v", g.orders)
	}
	r := rep.Remote
	if r == nil || r.Mode != "observe" || r.Observe == nil || !r.Observe.Started || r.Observe.Ended != "done" {
		t.Fatalf("remote %+v", r)
	}
	ob := r.Observe
	if ob.State != "done" || ob.Step != 5 || ob.Steps != 5 || ob.Leg != "second" || ob.Lines < 6 || len(ob.Changes) < 6 {
		t.Fatalf("observe %+v", ob)
	}
	if len(r.Orders) != 1 || r.Orders["stay"] != 1 {
		t.Fatalf("the observer's orders: %v (only the end's stay)", r.Orders)
	}
	// the legs are in the report with their times, the last step is the current one
	if len(rep.Legs) != 2 || rep.Legs[0].Name != "first" || rep.Legs[0].Result != "done" || rep.Legs[1].Name != "second" {
		t.Fatalf("legs %+v", rep.Legs)
	}
	if _, err := json.Marshal(rep); err != nil {
		t.Fatal(err)
	}
}

func TestObservePollsWhenNothingIsPushed(t *testing.T) {
	g := newFakeLeader(t, happyScript()...)
	g.pushOff = true
	rep, err := runRemote(t, g, observePlan(), observeOpts(ObserveSpec{Poll: s(3)}), nil)
	if err != nil || rep.State != "done" {
		t.Fatalf("%v %s %s", err, rep.State, rep.Error)
	}
	if rep.Remote.Observe.Polls < 5 || len(g.ordersOf("run status")) < 5 {
		t.Fatalf("polls %d: %v", rep.Remote.Observe.Polls, g.orders)
	}
	if rep.Remote.Observe.Step != 5 {
		t.Fatalf("%+v", rep.Remote.Observe)
	}
}

func TestObserveRefusesAServerWithoutTheRunLeaderBeforeHiring(t *testing.T) {
	g := newFakeLeader(t)
	g.listText = ""
	rep, err := runRemote(t, g, observePlan(), observeOpts(ObserveSpec{}), nil)
	if err == nil || !strings.Contains(err.Error(), "needs the TortoiseBots run leader") || rep.State != "failed" {
		t.Fatalf("%v %s", err, rep.State)
	}
	if len(g.orders) != 1 || g.orders[0] != "run list" {
		t.Fatalf("something was sent before the refusal: %v", g.orders)
	}
}

func TestObserveRefusesAServerWithoutThePlan(t *testing.T) {
	g := newFakeLeader(t)
	g.listText = "uldaman (map 70, 41 steps), other (map 1, 3 steps)"
	_, err := runRemote(t, g, observePlan(), observeOpts(ObserveSpec{}), nil)
	if err == nil || !strings.Contains(err.Error(), `no plan "rt"`) || !strings.Contains(err.Error(), "plan-export") {
		t.Fatalf("%v", err)
	}
	if len(g.orders) != 1 {
		t.Fatalf("%v", g.orders)
	}
}

func TestObserveRefusesWhenTheRunLeaderIsOff(t *testing.T) {
	g := newFakeLeader(t, happyScript()...)
	g.startErr = "disabled"
	_, err := runRemote(t, g, observePlan(), observeOpts(ObserveSpec{}), nil)
	if err == nil || !strings.Contains(err.Error(), "DungeonSelfClear") {
		t.Fatalf("%v", err)
	}
	if g.indexOf("run stop") >= 0 {
		t.Fatalf("a run that never started was stopped: %v", g.orders)
	}
	if g.orders[len(g.orders)-1] != "commander unbind" {
		t.Fatalf("%v", g.orders)
	}
}

func TestObservePauseAndResumeGoThroughTheRPC(t *testing.T) {
	g := newFakeLeader(t, happyScript()...)
	var d *RemoteDriver
	asked := 0
	g.onStatus = func(l *fakeLeader, st tbm.RunStatus) {
		// ask for a pause at step 2, for the resume when the run is paused
		switch {
		case st.State == tbm.RunRunning && st.Step == 2 && asked == 0:
			asked++
			if err := d.RunControl("pause"); err != nil {
				t.Error(err)
			}
		case st.State == tbm.RunPaused && asked == 1:
			asked++
			if err := d.RunControl("resume"); err != nil {
				t.Error(err)
			}
		}
	}
	cfg := DefaultConfig()
	d = NewRemoteDriver(observePlan(), g, cfg, observeOpts(ObserveSpec{}), g.clock())
	if err := d.RunControl("pause"); err == nil {
		t.Error("RunControl before the watch began was accepted")
	}
	err := d.Run(context.Background(), 0)
	rep, _ := d.Status()
	if err != nil || rep.State != "done" {
		t.Fatalf("%v %s %s", err, rep.State, rep.Error)
	}
	if g.indexOf("run pause") < 0 || g.indexOf("run resume") < g.indexOf("run pause") {
		t.Fatalf("pause/resume: %v", g.orders)
	}
	ob := rep.Remote.Observe
	if ob.Pauses != 1 || len(ob.Controls) != 2 || !strings.HasPrefix(ob.Controls[0], "pause: ack") || !strings.HasPrefix(ob.Controls[1], "resume: ack") {
		t.Fatalf("observe %+v", ob)
	}
	if err := d.RunControl("fly"); err == nil {
		t.Error("an unknown verb was accepted")
	}
}

func TestObserveAPausedRunThatNobodyResumesIsStopped(t *testing.T) {
	g := newFakeLeader(t,
		leaderStep{0, rs("running", 1, 5, "first", "move", "walk", "walking")},
		leaderStep{s(10), rs("paused", 2, 5, "first", "pull", "gather", "3 dead: regroup (cmd revive), then .bot run resume")})
	rep, err := runRemote(t, g, observePlan(), observeOpts(ObserveSpec{PausedWait: s(60)}), nil)
	if err == nil || !strings.Contains(err.Error(), "paused for 1m0s without a resume at step 2/5") || !strings.Contains(err.Error(), "3 dead") || rep.State != "failed" {
		t.Fatalf("%v %s", err, rep.State)
	}
	if g.stopped != 1 || rep.Remote.Observe.Ended == "" {
		t.Fatalf("stopped %d, %+v", g.stopped, rep.Remote.Observe)
	}
	// stop is the run leader's own command; no order of the bots was sent after the start
	if after := cmdOrdersAfter(g.orders, g.indexOf("run start rt")); len(after) != 1 || after[0] != "cmd stay" {
		t.Fatalf("%v", after)
	}
}

func TestObserveAStalledRunIsStopped(t *testing.T) {
	g := newFakeLeader(t, leaderStep{0, rs("running", 1, 5, "first", "pull", "pull", "pulling 299")})
	rep, err := runRemote(t, g, observePlan(), observeOpts(ObserveSpec{Stall: s(120)}), nil)
	if err == nil || !strings.Contains(err.Error(), "stalled") || g.stopped != 1 || rep.State != "failed" {
		t.Fatalf("%v stopped %d %s", err, g.stopped, rep.State)
	}
}

func TestObserveHasACeilingOnTheWholeRun(t *testing.T) {
	steps := []leaderStep{{0, rs("running", 1, 5, "first", "move", "walk", "walking")}}
	for i := 1; i <= 40; i++ { // the status keeps changing: only the ceiling stops it
		steps = append(steps, leaderStep{s(i * 30), rs("running", 1, 5, "first", "pull", "pull", fmt.Sprintf("fight %d", i))})
	}
	g := newFakeLeader(t, steps...)
	_, err := runRemote(t, g, observePlan(), observeOpts(ObserveSpec{MaxRun: s(600)}), nil)
	if err == nil || !strings.Contains(err.Error(), "took longer than 10m0s") || g.stopped != 1 {
		t.Fatalf("%v stopped %d", err, g.stopped)
	}
}

// The ceiling's stop says why: `run stop time limit reached (<N>h)` (TortoiseBots #1070 logs it as the stop reason); max_run_s sets the ceiling
// and the other endings send the plain line.
func TestObserveCeilingStopCarriesTheReason(t *testing.T) {
	run := func(max time.Duration) *fakeLeader {
		steps := []leaderStep{{0, rs("running", 1, 5, "first", "move", "walk", "walking")}}
		for i := 1; i <= 400; i++ {
			steps = append(steps, leaderStep{s(i * 30), rs("running", 1, 5, "first", "pull", "pull", fmt.Sprintf("fight %d", i))})
		}
		g := newFakeLeader(t, steps...)
		_, err := runRemote(t, g, observePlan(), observeOpts(ObserveSpec{MaxRun: max}), nil)
		if err == nil || g.stopped != 1 {
			t.Fatalf("%v stopped %d", err, g.stopped)
		}
		return g
	}
	if g := run(3 * time.Hour); g.indexOf("run stop time limit reached (3h)") < 0 || len(g.ordersOf("run stop")) != 1 {
		t.Fatalf("3 h ceiling: %v", g.orders)
	}
	// A longer ceiling (observe.max_run_s) names its hours, a short one its fraction of one.
	if g := run(90 * time.Minute); g.indexOf("run stop time limit reached (1.5h)") < 0 {
		t.Fatalf("1.5 h ceiling: %v", g.orders)
	}
	// A module that reads no reason refuses the line as invalid: the plain stop follows and the run is stopped.
	old := newFakeLeader(t, leaderStep{0, rs("running", 1, 5, "first", "move", "walk", "walking")}, leaderStep{s(700), rs("running", 1, 5, "first", "pull", "pull", "fight")})
	old.noStopReason = true
	if _, err := runRemote(t, old, observePlan(), observeOpts(ObserveSpec{MaxRun: s(600)}), nil); err == nil || old.stopped != 1 || old.indexOf("run stop time limit") < 0 {
		t.Fatalf("%v stopped %d %v", err, old.stopped, old.orders)
	}
	// A stall stops the run with the plain line.
	g := newFakeLeader(t, leaderStep{0, rs("running", 1, 5, "first", "pull", "pull", "pulling 299")})
	if _, err := runRemote(t, g, observePlan(), observeOpts(ObserveSpec{Stall: s(120)}), nil); err == nil || len(g.ordersOf("run stop")) != 1 || g.indexOf("run stop time limit") >= 0 || g.indexOf("run stop ") >= 0 {
		t.Fatalf("%v %v", err, g.orders)
	}
}

func TestObserveStopThroughTheContextStopsTheRun(t *testing.T) {
	g := newFakeLeader(t, happyScript()...)
	ctx, cancel := context.WithCancel(context.Background())
	g.onStatus = func(l *fakeLeader, st tbm.RunStatus) {
		if st.Step == 3 {
			cancel() // dungeon_stop
		}
	}
	d := NewRemoteDriver(observePlan(), g, DefaultConfig(), observeOpts(ObserveSpec{}), g.clock())
	err := d.Run(ctx, 0)
	r, _ := d.Status()
	if err == nil || r.State != "stopped" {
		t.Fatalf("%v %s", err, r.State)
	}
	if g.stopped != 1 || g.indexOf("run stop") < 0 {
		t.Fatalf("the run leader was not stopped: %v", g.orders)
	}
	if g.orders[len(g.orders)-1] != "commander unbind" {
		t.Fatalf("%v", g.orders)
	}
	if c := r.Remote.Observe.Controls; len(c) != 1 || !strings.HasPrefix(c[0], "stop (") {
		t.Fatalf("controls %v", c)
	}
}

func TestObserveAnEndBeforeTheLastStepIsAFailure(t *testing.T) {
	g := newFakeLeader(t,
		leaderStep{0, rs("running", 1, 5, "first", "move", "walk", "walking")},
		leaderStep{s(10), rs("done", 2, 5, "first", "pull", "pull", "stopped by .bot run stop")})
	rep, err := runRemote(t, g, observePlan(), observeOpts(ObserveSpec{}), nil)
	if err == nil || !strings.Contains(err.Error(), "ended the run at step 2/5") || rep.State != "failed" {
		t.Fatalf("%v %s", err, rep.State)
	}
	if g.indexOf("run stop") >= 0 {
		t.Fatalf("a run that the server ended was stopped again: %v", g.orders)
	}
}

func TestObserveAdoptsARunOfTheSamePlan(t *testing.T) {
	g := newFakeLeader(t, happyScript()...)
	g.running = true
	g.emit(rs("running", 2, 5, "first", "pull", "pull", "pulling 299"))
	g.progress = s(9)
	g.next = 3
	rep, err := runRemote(t, g, observePlan(), observeOpts(ObserveSpec{}), nil)
	if err != nil || rep.State != "done" {
		t.Fatalf("%v %s %s", err, rep.State, rep.Error)
	}
	if !strings.Contains(strings.Join(rep.Notes, "\n"), "already going") {
		t.Fatalf("notes %v", rep.Notes)
	}
}

func TestObserveIgnoresAnotherPlansLines(t *testing.T) {
	other := rs("running", 9, 9, "elsewhere", "pull", "pull", "x")
	other.Plan = "other"
	g := newFakeLeader(t, append([]leaderStep{{0, other}}, happyScript()...)...)
	rep, err := runRemote(t, g, observePlan(), observeOpts(ObserveSpec{}), nil)
	if err != nil || rep.State != "done" {
		t.Fatalf("%v %s", err, rep.State)
	}
	for _, c := range rep.Remote.Observe.Changes {
		if c.Leg == "elsewhere" {
			t.Fatalf("another plan's line was taken: %+v", c)
		}
	}
}

func TestRunControlNeedsAnObservedRun(t *testing.T) {
	g := newFakeRemote(t)
	d := NewRemoteDriver(observePlan(), g, DefaultConfig(), opts(), g.clock())
	if err := d.RunControl("pause"); err == nil {
		t.Fatal("a driven run took a run control")
	}
}

// maintenanceFake answers plain `.bot` lines (the preflight's maintenance, inv and talents) and records them, the way the real game does.
type maintenanceFake struct {
	*fakeLeader
	botCmds []string
}

func (g *maintenanceFake) BotLines(_ context.Context, command string, _ time.Duration, done func(string) bool) ([]string, bool, error) {
	g.botCmds = append(g.botCmds, command)
	return nil, false, nil
}

// maintenanceCmds are the `.bot maintenance` lines among the plain lines.
func maintenanceCmds(cmds []string) []string {
	var out []string
	for _, c := range cmds {
		if strings.HasPrefix(c, "maintenance ") {
			out = append(out, c)
		}
	}
	return out
}

// An observed run's preflight sends no `.bot maintenance` (it walks every bot to a vendor and scatters the party right after the hires'
// autogear, which pauses the run leader with "the lead bot is not on the plan's map", #253); the kit check's reads still run.
func TestObserveStartSendsNoMaintenanceByDefault(t *testing.T) {
	g := &maintenanceFake{fakeLeader: newFakeLeader(t, happyScript()...)}
	rep, err := runRemote(t, g, observePlan(), observeOpts(ObserveSpec{}), nil)
	if err != nil || rep.State != "done" {
		t.Fatalf("%v %s %s", err, rep.State, rep.Error)
	}
	if got := maintenanceCmds(g.botCmds); len(got) != 0 {
		t.Fatalf("an observed start sent maintenance: %v", got)
	}
	if !slices.Contains(g.botCmds, "inv Tanky") || !slices.Contains(g.botCmds, "command Tanky talents") {
		t.Fatalf("the kit check's reads did not run: %v", g.botCmds)
	}
	if !strings.Contains(strings.Join(rep.Notes, "\n"), "maintenance skipped (observe mode") {
		t.Fatalf("notes %v", rep.Notes)
	}
}

// observe.maintenance turns it back on, one line per bot of the first frame.
func TestObserveMaintenanceOption(t *testing.T) {
	g := &maintenanceFake{fakeLeader: newFakeLeader(t, happyScript()...)}
	rep, err := runRemote(t, g, observePlan(), observeOpts(ObserveSpec{Maintenance: true}), nil)
	if err != nil || rep.State != "done" {
		t.Fatalf("%v %s %s", err, rep.State, rep.Error)
	}
	want := []string{"maintenance Tanky", "maintenance Healy", "maintenance Magey"}
	if got := maintenanceCmds(g.botCmds); !slices.Equal(got, want) {
		t.Fatalf("maintenance %v, want %v", got, want)
	}
}
