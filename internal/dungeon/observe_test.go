package dungeon

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/pjerra/turtle-agent/internal/packet"
	"github.com/pjerra/turtle-agent/internal/tbm"
	"github.com/pjerra/turtle-agent/internal/trace"
	"github.com/pjerra/turtle-agent/internal/tracecheck"
	"github.com/pjerra/turtle-agent/internal/tracerun"
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
	noStopReason bool     // a module from before TortoiseBots #1070: `run stop <reason>` is invalid
	pushOff      bool     // the feed does not deliver the pushed lines: only `run status` shows them
	startExtra   []string // extra lines the module's answer to `run start` carries (its start message)

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
	s := fmt.Sprintf("TBM:RUN|%s|%s|%d/%d|%s|%s|%s|%s", d(st.Plan), st.State, st.Step, st.Steps, d(st.Leg), d(st.Kind), d(st.Phase), d(st.Note))
	if st.Left > 0 {
		s += fmt.Sprintf("|left %d", st.Left)
	}
	return s
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
		r.Lines = append(slices.Clone(l.startExtra), l.statusLines()...)
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

// A done line with packs left behind keeps the count: the note is bare and the report carries Left.
func TestObserveKeepsThePacksLeftCount(t *testing.T) {
	done := rs("done", 5, 5, "second", "move", "walk", "plan done")
	done.Left = 2
	g := newFakeLeader(t,
		leaderStep{0, rs("running", 1, 5, "first", "move", "walk", "walking")},
		leaderStep{s(10), done})
	rep, err := runRemote(t, g, observePlan(), observeOpts(ObserveSpec{}), nil)
	if err != nil || rep.State != "done" {
		t.Fatalf("%v %s %s", err, rep.State, rep.Error)
	}
	ob := rep.Remote.Observe
	if ob.Left != 2 || ob.Note != "plan done" || ob.State != "done" {
		t.Fatalf("observe %+v", ob)
	}
	if last := ob.Changes[len(ob.Changes)-1]; last.Left != 2 || last.Note != "plan done" {
		t.Fatalf("last change %+v", last)
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

// wipeParty kills the whole fake party in place (or brings it back): the next frame the feed publishes shows it.
func wipeParty(g *fakeRemote, dead bool) {
	for _, p := range g.party {
		p.Dead, p.HP, p.State = dead, 0, "d"
		if !dead {
			p.HP, p.State = p.MaxHP, "n"
		}
	}
}

// wipeScript keeps the run going while the party dies and lives again: statuses every 5 s (no stall), done at 20 s.
func wipeScript() []leaderStep {
	return []leaderStep{
		{0, rs("running", 1, 5, "first", "move", "walk", "walking to -1 2 3")},
		{s(5), rs("running", 2, 5, "first", "pull", "pull", "pulling 299")},
		{s(10), rs("running", 3, 5, "first", "boss", "pull", "pulling boss")},
		{s(20), rs("done", 5, 5, "first", "move", "walk", "plan done")},
	}
}

// deathsOnTicks drives the party's deaths by the ticks of the watch (one sleep of cfg.Poll, one frame, one countDeaths each):
// first all dead for 3 polls, then alive for 1, then all dead again for 2, then alive to the end.
func deathsOnTicks(g *fakeLeader, deadTicks int, reviveAt int, deadAgainTo int) {
	base := g.onTick
	ticks := 0
	g.onTick = func(fr *fakeRemote) {
		base(fr)
		if !g.running {
			return
		}
		ticks++
		switch {
		case ticks <= deadTicks, ticks > reviveAt && ticks <= deadAgainTo:
			wipeParty(fr, true)
		default:
			wipeParty(fr, false)
		}
	}
}

// A wipe of an observed run is counted once, on the rising edge of "all dead": three polls of a dead party are one wipe, and the
// party coming back alive lets the next wipe count again. The run leader owns the run, so the watch neither stops nor revives.
func TestObserveCountsAWipeOnItsRisingEdge(t *testing.T) {
	g := newFakeLeader(t, wipeScript()...)
	g.party = append(g.party, &tbm.PartyRec{GUID: packet.GUID(0x2D), Name: "Loxy", HP: 1000, MaxHP: 1000, Power: 1000, MaxPower: 1000, PowerType: 0, State: "n"},
		&tbm.PartyRec{GUID: packet.GUID(0x2E), Name: "Grok", HP: 1000, MaxHP: 1000, Power: 1000, MaxPower: 1000, PowerType: 0, State: "n"})
	deathsOnTicks(g, 3, 4, 6)
	rep, err := runRemote(t, g, observePlan(), observeOpts(ObserveSpec{}), nil)
	if err != nil || rep.State != "done" || rep.Remote.Observe.Ended != "done" {
		t.Fatalf("%v %s %s", err, rep.State, rep.Error)
	}
	if rep.Wipes != 2 {
		t.Fatalf("wipes %d (want 2)\n%s", rep.Wipes, notesOf(rep))
	}
}

// A single member alive does not re-arm the wipe latch: all dead, one member alive (4 of 5), all dead again within the latch, then a
// real regroup (nobody dead), then all dead again: two collapses, so Wipes == 2 and exactly 2 trace "wipe" events, and the tracecheck
// deaths oracle reads them ("%d wipes" in its detail: stepFailedRecover never runs in an observed run).
func TestObserveWipeLatchReArmsBelowThreeDead(t *testing.T) {
	g := newFakeLeader(t, wipeScript()...)
	g.party = append(g.party, &tbm.PartyRec{GUID: packet.GUID(0x2D), Name: "Loxy", HP: 1000, MaxHP: 1000, Power: 1000, MaxPower: 1000, PowerType: 0, State: "n"},
		&tbm.PartyRec{GUID: packet.GUID(0x2E), Name: "Grok", HP: 1000, MaxHP: 1000, Power: 1000, MaxPower: 1000, PowerType: 0, State: "n"})
	// the dead count of the tick, alive when negative
	deadOf := map[int]int{1: 5, 2: 4, 3: 5, 4: 0, 5: 5}
	base := g.onTick
	ticks := 0
	g.onTick = func(fr *fakeRemote) {
		base(fr)
		if !g.running {
			return
		}
		ticks++
		n := deadOf[ticks]
		for i, p := range fr.party {
			if dead := i < n; dead {
				p.Dead, p.HP, p.State = true, 0, "d"
			} else {
				p.Dead, p.HP, p.State = false, p.MaxHP, "n"
			}
		}
	}
	dir := t.TempDir()
	rep, err := runRemote(t, g, observePlan(), observeOpts(ObserveSpec{Poll: s(3)}), func(c *Config) { c.Trace = tracerun.Config{On: true, Dir: dir, RunID: "r-wipe-hyst"} })
	if err != nil || rep.State != "done" || rep.Remote.Observe.Ended != "done" {
		t.Fatalf("%v %s %s", err, rep.State, rep.Error)
	}
	if rep.Wipes != 2 {
		t.Fatalf("wipes %d (want 2)\n%s", rep.Wipes, notesOf(rep))
	}
	f, err := trace.Read(filepath.Join(dir, "r-wipe-hyst.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	wipes := 0
	for _, e := range f.Events {
		if e.Ev == "wipe" {
			wipes++
		}
	}
	if wipes != 2 {
		t.Fatalf("%d wipe events in the trace (want 2)", wipes)
	}
	for _, c := range tracecheck.Score(f, tracecheck.Defaults()) {
		if c.ID == tracecheck.IDDeaths && !strings.Contains(c.Detail, ", 2 wipes") {
			t.Fatalf("the deaths oracle does not say 2 wipes: %+v", c)
		}
	}
}

// An observed run is driven by the server's run leader, which is the lead bot: the trace header names it as the tank, so the threat
// oracle does not have to guess the tank from the frames. A driven run keeps the tank it was given (TestTraceOfARemoteRun).
func TestObserveTraceNamesTheLeadAsTheTank(t *testing.T) {
	g := newFakeLeader(t, happyScript()...)
	dir := t.TempDir()
	rep, err := runRemote(t, g, observePlan(), observeOpts(ObserveSpec{}), func(c *Config) {
		c.Trace = tracerun.Config{On: true, Dir: dir, RunID: "r-observe-tank"}
	})
	if err != nil || rep.Trace == nil || rep.Trace.Path == "" {
		t.Fatalf("%v %+v", err, rep.Trace)
	}
	f, err := trace.Read(rep.Trace.Path)
	if err != nil {
		t.Fatal(err)
	}
	if f.Header.Lead != "Tanky" || f.Header.Tank != f.Header.Lead {
		t.Fatalf("header lead %q tank %q, want both Tanky", f.Header.Lead, f.Header.Tank)
	}
}

// A party with one bot alive is no wipe: four dead of five count nothing.
func TestObserveCountsNoWipeWhileABotLives(t *testing.T) {
	g := newFakeLeader(t, wipeScript()...)
	g.party = append(g.party, &tbm.PartyRec{GUID: packet.GUID(0x2D), Name: "Loxy", HP: 1000, MaxHP: 1000, Power: 1000, MaxPower: 1000, PowerType: 0, State: "n"},
		&tbm.PartyRec{GUID: packet.GUID(0x2E), Name: "Grok", HP: 1000, MaxHP: 1000, Power: 1000, MaxPower: 1000, PowerType: 0, State: "n"})
	base := g.onTick
	ticks := 0
	g.onTick = func(fr *fakeRemote) {
		base(fr)
		if !g.running {
			return
		}
		ticks++
		for i, p := range fr.party {
			p.Dead, p.State = i < 4, "n"
			if i < 4 {
				p.HP, p.State = 0, "d"
			}
		}
	}
	rep, err := runRemote(t, g, observePlan(), observeOpts(ObserveSpec{}), nil)
	if err != nil || rep.State != "done" {
		t.Fatalf("%v %s %s", err, rep.State, rep.Error)
	}
	if rep.Wipes != 0 {
		t.Fatalf("wipes %d (want 0)\n%s", rep.Wipes, notesOf(rep))
	}
}

// observeRunScript keeps an observed run going past the death tables of the window tests: a status every 20 s and done at 200 s.
func observeRunScript() []leaderStep {
	return []leaderStep{
		{0, rs("running", 1, 5, "first", "move", "walk", "walking")},
		{s(20), rs("running", 2, 5, "first", "pull", "pull", "pulling")},
		{s(40), rs("running", 3, 5, "first", "boss", "pull", "pulling")},
		{s(60), rs("running", 3, 5, "first", "boss", "fight", "fighting")},
		{s(80), rs("running", 4, 5, "first", "move", "walk", "walking")},
		{s(100), rs("running", 4, 5, "first", "move", "walk", "walking on")},
		{s(120), rs("running", 4, 5, "first", "move", "walk", "walking on")},
		{s(140), rs("running", 4, 5, "first", "move", "walk", "walking on")},
		{s(160), rs("running", 4, 5, "first", "move", "walk", "walking on")},
		{s(180), rs("running", 4, 5, "first", "move", "walk", "walking on")},
		{s(200), rs("done", 5, 5, "first", "move", "walk", "plan done")},
	}
}

// deathsOnTickTable kills the party indexes the table names for each tick of the watch (one cfg.Poll sleep, one frame): the ticks
// after the table's last key are all alive.
func deathsOnTickTable(g *fakeLeader, table map[int][]int) {
	base := g.onTick
	ticks := 0
	g.onTick = func(fr *fakeRemote) {
		base(fr)
		if !g.running {
			return
		}
		ticks++
		dead := map[int]bool{}
		for _, i := range table[ticks] {
			dead[i] = true
		}
		for i, p := range fr.party {
			if dead[i] {
				p.Dead, p.HP, p.State = true, 0, "d"
			} else {
				p.Dead, p.HP, p.State = false, p.MaxHP, "n"
			}
		}
	}
}

// fiveBotLeader is the party of the wipe tests: five bots, the lead bot first (the frame's Lead).
func fiveBotLeader(t *testing.T, script ...leaderStep) *fakeLeader {
	g := newFakeLeader(t, script...)
	g.party = append(g.party, &tbm.PartyRec{GUID: packet.GUID(0x2D), Name: "Loxy", HP: 1000, MaxHP: 1000, Power: 1000, MaxPower: 1000, PowerType: 0, State: "n"},
		&tbm.PartyRec{GUID: packet.GUID(0x2E), Name: "Grok", HP: 1000, MaxHP: 1000, Power: 1000, MaxPower: 1000, PowerType: 0, State: "n"})
	return g
}

// An observed run's report counts the lead bot's own deaths and revives from the feed: two deaths, each revived 5 s later, and no
// wipe (the other four never died).
func TestObserveCountsALeadDeathAndItsRevive(t *testing.T) {
	g := fiveBotLeader(t, observeRunScript()...)
	deathsOnTickTable(g, map[int][]int{2: {0}, 5: {0}})
	dir := t.TempDir()
	rep, err := runRemote(t, g, observePlan(), observeOpts(ObserveSpec{}), func(c *Config) {
		c.Poll = 5 * time.Second
		c.Trace = tracerun.Config{On: true, Dir: dir, RunID: "r-lead-death"}
	})
	if err != nil || rep.State != "done" {
		t.Fatalf("%v %s %s", err, rep.State, rep.Error)
	}
	if rep.LeaderDeaths != 2 || rep.Resurrections != 2 {
		t.Fatalf("leader deaths %d resurrections %d (want 2 and 2)\n%s", rep.LeaderDeaths, rep.Resurrections, notesOf(rep))
	}
	if rep.SecondsDead < 5 || rep.SecondsDead > 20 {
		t.Fatalf("seconds dead %v (want 5..20)", rep.SecondsDead)
	}
	if rep.Wipes != 0 {
		t.Fatalf("wipes %d (want 0)\n%s", rep.Wipes, notesOf(rep))
	}
	if len(rep.Deaths) != 2 {
		t.Fatalf("deaths %d (want 2): %+v", len(rep.Deaths), rep.Deaths)
	}
	legDeaths := 0
	for _, l := range rep.Legs {
		legDeaths += l.LeaderDeaths
	}
	if legDeaths != 2 {
		t.Fatalf("leg leader deaths %d (want 2): %+v", legDeaths, rep.Legs)
	}
	f, err := trace.Read(filepath.Join(dir, "r-lead-death.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	n := 0
	for _, e := range f.Events {
		if e.Ev == "leader_death" {
			n++
			if e.D["name"] != "Tanky" {
				t.Fatalf("leader_death names %v (want Tanky)", e.D["name"])
			}
		}
	}
	if n != 2 {
		t.Fatalf("%d leader_death events in the trace (want 2)", n)
	}
	if c := f.End.Counter("leader_deaths"); c != 2 {
		t.Fatalf("trace end leader_deaths %v (want 2)", c)
	}
}

// The RFC case: the lead bot revives, so the whole party is never dead in one frame, but all five died within 25 s: one wipe.
func TestObserveCountsAWipeSpreadOverOneEpisode(t *testing.T) {
	g := fiveBotLeader(t, observeRunScript()...)
	deathsOnTickTable(g, map[int][]int{2: {0}, 3: {1}, 4: {1, 2}, 5: {1, 2, 3}, 6: {1, 2, 3, 4}, 7: {0, 2, 3, 4}})
	dir := t.TempDir()
	rep, err := runRemote(t, g, observePlan(), observeOpts(ObserveSpec{}), func(c *Config) {
		c.Poll = 5 * time.Second
		c.Trace = tracerun.Config{On: true, Dir: dir, RunID: "r-window-wipe"}
	})
	if err != nil || rep.State != "done" {
		t.Fatalf("%v %s %s", err, rep.State, rep.Error)
	}
	if rep.Wipes != 1 {
		t.Fatalf("wipes %d (want 1)\n%s", rep.Wipes, notesOf(rep))
	}
	if rep.LeaderDeaths != 2 {
		t.Fatalf("leader deaths %d (want 2)", rep.LeaderDeaths)
	}
	f, err := trace.Read(filepath.Join(dir, "r-window-wipe.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	wipes := 0
	for _, e := range f.Events {
		if e.Ev == "wipe" {
			wipes++
			if e.D["window"] != true {
				t.Fatalf("the wipe event is not a window wipe: %+v", e.D)
			}
		}
	}
	if wipes != 1 {
		t.Fatalf("%d wipe events in the trace (want 1)", wipes)
	}
	for _, c := range tracecheck.Score(f, tracecheck.Defaults()) {
		if c.ID == tracecheck.IDDeaths && !strings.Contains(c.Detail, ", 1 wipes") {
			t.Fatalf("the deaths oracle does not say 1 wipes: %+v", c)
		}
	}
}

// The same deaths 30 s apart are outside the 90 s window: the lead's first death is 120 s older than the last member's, so no wipe.
func TestObserveNoWipeWhenTheDeathsAreOutsideTheWindow(t *testing.T) {
	g := fiveBotLeader(t,
		leaderStep{0, rs("running", 1, 5, "first", "move", "walk", "walking")},
		leaderStep{s(100), rs("running", 2, 5, "first", "pull", "pull", "pulling")},
		leaderStep{s(200), rs("running", 3, 5, "first", "boss", "pull", "pulling")},
		leaderStep{s(300), rs("running", 4, 5, "first", "move", "walk", "walking")},
		leaderStep{s(400), rs("done", 5, 5, "first", "move", "walk", "plan done")})
	deathsOnTickTable(g, map[int][]int{2: {0}, 3: {1}, 4: {1, 2}, 5: {1, 2, 3}, 6: {1, 2, 3, 4}, 7: {0, 2, 3, 4}})
	dir := t.TempDir()
	rep, err := runRemote(t, g, observePlan(), observeOpts(ObserveSpec{}), func(c *Config) {
		c.Poll = 30 * time.Second
		c.Trace = tracerun.Config{On: true, Dir: dir, RunID: "r-window-out"}
	})
	if err != nil || rep.State != "done" {
		t.Fatalf("%v %s %s", err, rep.State, rep.Error)
	}
	if rep.Wipes != 0 {
		t.Fatalf("wipes %d (want 0)\n%s", rep.Wipes, notesOf(rep))
	}
	f, err := trace.Read(filepath.Join(dir, "r-window-out.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range f.Events {
		if e.Ev == "wipe" {
			t.Fatalf("a wipe event outside the window: %+v", e.D)
		}
	}
}

// The window wipe and the all-dead latch are one wipe: counted at tick 6, the all-dead ticks 7 and 8 must not count it again.
func TestObserveWindowWipeIsNotCountedTwiceWhenTheyThenAllDieTogether(t *testing.T) {
	g := fiveBotLeader(t, observeRunScript()...)
	deathsOnTickTable(g, map[int][]int{2: {0}, 3: {1}, 4: {1, 2}, 5: {1, 2, 3}, 6: {1, 2, 3, 4}, 7: {0, 1, 2, 3, 4}, 8: {0, 1, 2, 3, 4}})
	dir := t.TempDir()
	rep, err := runRemote(t, g, observePlan(), observeOpts(ObserveSpec{}), func(c *Config) {
		c.Poll = 5 * time.Second
		c.Trace = tracerun.Config{On: true, Dir: dir, RunID: "r-window-once"}
	})
	if err != nil || rep.State != "done" {
		t.Fatalf("%v %s %s", err, rep.State, rep.Error)
	}
	if rep.Wipes != 1 {
		t.Fatalf("wipes %d (want 1)\n%s", rep.Wipes, notesOf(rep))
	}
	f, err := trace.Read(filepath.Join(dir, "r-window-once.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	wipes := 0
	for _, e := range f.Events {
		if e.Ev == "wipe" {
			wipes++
		}
	}
	if wipes != 1 {
		t.Fatalf("%d wipe events in the trace (want 1)", wipes)
	}
}

// A lone death after the counted wipe is no second wipe. Loxy has been dead for 100 s when the other four die: the all-dead frame counts
// the wipe while the window rule is false (Loxy's death is outside the 90 s), so the revive below three dead re-arms the latch. Loxy then
// dies again 10 s after the wipe: the window rule reads deaths newer than the counted wipe only, so the four deaths of the wipe do not
// make it fire again. The whole party dying inside the window cannot test this: the window rule then stays true for the 90 s of the
// counted wipe, the latch never re-arms, and a lone death counts nothing either way.
func TestObserveNoSecondWipeFromALoneDeathAfterAWindowWipe(t *testing.T) {
	g := fiveBotLeader(t, observeRunScript()...)
	base, ticks := g.onTick, 0
	g.onTick = func(fr *fakeRemote) {
		base(fr)
		if !g.running {
			return
		}
		ticks++
		dead := map[int]bool{}
		if ticks == 22 {
			dead = map[int]bool{0: true, 1: true, 2: true, 4: true}
		}
		if ticks == 24 {
			dead[3] = true
		}
		if ticks >= 2 && ticks <= 22 {
			dead[3] = true // Loxy stays down through the collapse: a death older than the wipe's window
		}
		for i, p := range fr.party {
			if dead[i] {
				p.Dead, p.HP, p.State = true, 0, "d"
			} else {
				p.Dead, p.HP, p.State = false, p.MaxHP, "n"
			}
		}
	}
	dir := t.TempDir()
	rep, err := runRemote(t, g, observePlan(), observeOpts(ObserveSpec{}), func(c *Config) {
		c.Poll = 5 * time.Second
		c.Trace = tracerun.Config{On: true, Dir: dir, RunID: "r-window-lone"}
	})
	if err != nil || rep.State != "done" {
		t.Fatalf("%v %s %s", err, rep.State, rep.Error)
	}
	if rep.Wipes != 1 {
		t.Fatalf("wipes %d (want 1)\n%s", rep.Wipes, notesOf(rep))
	}
	if len(rep.Deaths) != 6 {
		t.Fatalf("deaths %d (want 6: Loxy's two and the other four in the wipe): %+v", len(rep.Deaths), rep.Deaths)
	}
	f, err := trace.Read(filepath.Join(dir, "r-window-lone.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	wipes := 0
	for _, e := range f.Events {
		if e.Ev == "wipe" {
			wipes++
		}
	}
	if wipes != 1 {
		t.Fatalf("%d wipe events in the trace (want 1)", wipes)
	}
}

// TestModuleCountsParse reads the run leader's counts from its notes: the wipes, near wipes and lead deaths its
// DoneNote and self-regroup notes name. The wipes regexp needs digits directly before "wipe(s)", so "5 near wipes"
// is never also read as 5 wipes (no blank-out needed).
func TestModuleCountsParse(t *testing.T) {
	cases := []struct {
		note        string
		wipes, near int
		lead        int
		ok          bool
	}{
		{"plan done (speed 3.0 (test); 5 wipes, 5 near wipes)", 5, 5, 0, true},
		{"4 dead (1 wipe, 2 near wipes this run): regrouping by itself (1 of 2 at this step)", 1, 2, 0, true},
		{"plan done (1 near wipe)", 0, 1, 0, true},
		{"plan done (2 wipes, 1 near wipe, 4 lead deaths)", 2, 1, 4, true},
		{"fighting", 0, 0, 0, false},
	}
	for _, c := range cases {
		wipes, near, lead, ok := moduleCounts(c.note)
		if wipes != c.wipes || near != c.near || lead != c.lead || ok != c.ok {
			t.Errorf("moduleCounts(%q) = %d, %d, %d, %v (want %d, %d, %d, %v)", c.note, wipes, near, lead, ok, c.wipes, c.near, c.lead, c.ok)
		}
	}
}

// TestObserveReportCarriesTheModuleCounts: the observe report carries the run leader's own counts, the highest its
// notes named (the final done note here).
func TestObserveReportCarriesTheModuleCounts(t *testing.T) {
	script := observeRunScript()
	script[len(script)-1] = leaderStep{s(200), rs("done", 5, 5, "first", "move", "walk", "plan done (2 wipes, 1 near wipe, 4 lead deaths)")}
	g := fiveBotLeader(t, script...)
	rep, err := runRemote(t, g, observePlan(), observeOpts(ObserveSpec{}), nil)
	if err != nil || rep.State != "done" {
		t.Fatalf("%v %s %s", err, rep.State, rep.Error)
	}
	ob := rep.Remote.Observe
	if ob == nil || ob.ModuleWipes != 2 || ob.ModuleNearWipes != 1 || ob.ModuleLeadDeaths != 4 {
		t.Fatalf("module counts %+v (want 2, 1, 4)", ob)
	}
}

// deadIntervals converts per-bot [start, end) dead seconds of the RFC trace to the tick table deathsOnTickTable
// reads: one tick is one poll (5 s). Tick 1 is the watch's baseline frame (countDeaths and trackObservedDeaths take
// it as the reference, as in the window tests above whose tables start at tick 2), so the first live sample is tick
// 2: tick = (t-370)/5 + 2, and [a,b) covers ta..tb-1.
func deadIntervals(m map[int][][2]int) map[int][]int {
	out := map[int][]int{}
	for idx, ivs := range m {
		for _, iv := range ivs {
			ta, tb := (iv[0]-370)/5+2, (iv[1]-370)/5+2
			for tick := ta; tick < tb; tick++ {
				out[tick] = append(out[tick], idx)
			}
		}
	}
	return out
}

// TestObserveRfc19z16Run2bCountsOneWipeAndFourLeadDeaths replays the real RFC 2026-10-20 run 2b dead edges (the Part A
// test 1 intervals; 0x2e3 is the lead at index 0, 0x2e5 the heal at 2, 0x2e4 at 1, 0x2e6 at 3, 0x2e7 at 4 from the
// 999-1062 edge order). The lead dies at 374, 592, 999 and 2370 and is back up 10-14 s later; the others die at
// 406-463, 1007-1072, 1034-1072, 1035-1072, 1062-1072, 2402-2423, 2573-2614, 2584-2614, 2592-2614 and 2605-2616. All
// five died within 999-1062 (63 s): one window wipe although the party is never all dead in one frame. The 2370 death
// is 235 s older than the 2605 one, so the second collapse is no second wipe. It pins #294 against the real collapse.
func TestObserveRfc19z16Run2bCountsOneWipeAndFourLeadDeaths(t *testing.T) {
	table := deadIntervals(map[int][][2]int{
		0: {{374, 384}, {592, 604}, {999, 1009}, {2370, 2384}},
		1: {{1034, 1072}, {2573, 2614}},
		2: {{406, 463}, {1007, 1072}, {2592, 2614}},
		3: {{1062, 1072}, {2584, 2614}},
		4: {{1035, 1072}, {2402, 2423}, {2605, 2616}},
	})
	var script []leaderStep
	script = append(script, leaderStep{0, rs("running", 1, 28, "first", "move", "walk", "walking")})
	for i := 1; i <= 26; i++ {
		script = append(script, leaderStep{s(i * 100), rs("running", i+1, 28, "first", "move", "walk", fmt.Sprintf("walking %d", i))})
	}
	script = append(script, leaderStep{s(2700), rs("done", 28, 28, "first", "move", "walk", "plan done")})
	g := fiveBotLeader(t, script...)
	deathsOnTickTable(g, table)
	dir := t.TempDir()
	rep, err := runRemote(t, g, observePlan(), observeOpts(ObserveSpec{}), func(c *Config) {
		c.Poll = 5 * time.Second
		c.Trace = tracerun.Config{On: true, Dir: dir, RunID: "r-rfc-2b"}
	})
	if err != nil || rep.State != "done" {
		t.Fatalf("%v %s %s", err, rep.State, rep.Error)
	}
	if rep.Wipes != 1 {
		t.Fatalf("wipes %d (want 1)\n%s", rep.Wipes, notesOf(rep))
	}
	if len(rep.Deaths) != 14 {
		t.Fatalf("deaths %d (want 14: the second collapse's 2402-2616 deaths are replayed too): %+v", len(rep.Deaths), rep.Deaths)
	}
	if rep.LeaderDeaths != 4 {
		t.Fatalf("leader deaths %d (want 4)\n%s", rep.LeaderDeaths, notesOf(rep))
	}
	if rep.Resurrections != 4 {
		t.Fatalf("resurrections %d (want 4)\n%s", rep.Resurrections, notesOf(rep))
	}
	f, err := trace.Read(filepath.Join(dir, "r-rfc-2b.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	wipes := 0
	for _, e := range f.Events {
		if e.Ev == "wipe" {
			wipes++
			if e.D["window"] != true {
				t.Fatalf("the wipe event is not a window wipe: %+v", e.D)
			}
		}
	}
	if wipes != 1 {
		t.Fatalf("%d wipe events in the trace (want 1)", wipes)
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

func TestPlayerOrderOf(t *testing.T) {
	for _, c := range []struct {
		note  string
		order string
		ok    bool
	}{
		{"player order (cmd summon Healy): .bot run resume to go on", "cmd summon Healy", true},
		{"player order (move 1 2 3): .bot run resume to go on; cave cleared meanwhile: .bot run resume goes on with step 4", "move 1 2 3", true},
		{"player order (cmd revive)", "cmd revive", true},
		{"player order (cmd summon Healy): .bot run resume to go on; note (2): a later aside", "cmd summon Healy", true},
		{"3 dead: regroup (cmd revive), then .bot run resume", "", false},
		{"", "", false},
	} {
		order, ok := playerOrderOf(c.note)
		if ok != c.ok || order != c.order {
			t.Fatalf("playerOrderOf(%q) = %q, %v", c.note, order, ok)
		}
	}
}

// A pause the module made for a player's order (the observer's own `cmd summon`, a move) is the observer's doing: the report names the order,
// and the run going on again is recorded as the resume after it.
func TestObserveRecordsAPlayerOrderPauseAndItsResume(t *testing.T) {
	g := newFakeLeader(t,
		leaderStep{0, rs("running", 1, 5, "first", "move", "walk", "walking")},
		leaderStep{s(5), rs("paused", 2, 5, "first", "pull", "gather", "player order (cmd summon Healy): .bot run resume to go on")},
		leaderStep{s(10), rs("running", 3, 5, "first", "pull", "pull", "pulling 299")},
		leaderStep{s(20), rs("done", 5, 5, "second", "move", "walk", "plan done")})
	var d *RemoteDriver
	paused, asked := false, false
	g.onStatus = func(l *fakeLeader, st tbm.RunStatus) {
		// the module paused the run for the order: the leader holds it until a resume
		if st.State == tbm.RunPaused {
			paused = true
			l.paused = true
		}
	}
	tick := g.onTick
	g.onTick = func(gr *fakeRemote) {
		// the resume comes a look after the paused status: the watch serves requests before it reads the feed
		if paused && !asked {
			asked = true
			if err := d.RunControl("resume"); err != nil {
				t.Error(err)
			}
		}
		tick(gr)
	}
	d = NewRemoteDriver(observePlan(), g, DefaultConfig(), observeOpts(ObserveSpec{}), g.clock())
	err := d.Run(context.Background(), 0)
	rep, _ := d.Status()
	if err != nil || rep.State != "done" {
		t.Fatalf("%v %s %s", err, rep.State, rep.Error)
	}
	ob := rep.Remote.Observe
	if len(ob.PlayerOrders) != 1 || !strings.Contains(ob.PlayerOrders[0], "cmd summon Healy") || !strings.Contains(ob.PlayerOrders[0], "step 2/") {
		t.Fatalf("player orders %v", ob.PlayerOrders)
	}
	var order, resumed, ack bool
	for _, c := range ob.Controls {
		order = order || c == "player order: cmd summon Healy"
		resumed = resumed || strings.HasPrefix(c, "resumed after the player order")
		ack = ack || strings.HasPrefix(c, "resume: ack")
	}
	if !order || !resumed || !ack {
		t.Fatalf("controls %v", ob.Controls)
	}
}

// #270: a pause for a player order that ends in a stop did not resume: the latch lets go with no "resumed after" entry.
func TestObservePlayerOrderPauseEndingInAStopHasNoResume(t *testing.T) {
	g := newFakeLeader(t,
		leaderStep{0, rs("running", 1, 5, "first", "move", "walk", "walking")},
		leaderStep{s(5), rs("paused", 2, 5, "first", "pull", "gather", "player order (cmd summon Healy): .bot run resume to go on")},
		leaderStep{s(10), rs("done", 5, 5, "second", "move", "walk", "plan done")})
	rep, err := runRemote(t, g, observePlan(), observeOpts(ObserveSpec{}), nil)
	if err != nil || rep.State != "done" {
		t.Fatalf("%v %s %s", err, rep.State, rep.Error)
	}
	ob := rep.Remote.Observe
	if len(ob.PlayerOrders) != 1 {
		t.Fatalf("player orders %v", ob.PlayerOrders)
	}
	for _, c := range ob.Controls {
		if strings.HasPrefix(c, "resumed after the player order") {
			t.Fatalf("a stop wrote a resume: %v", ob.Controls)
		}
	}
}

func TestObserveDoesNotCountAnOrdinaryPauseAsAPlayerOrder(t *testing.T) {
	g := newFakeLeader(t,
		leaderStep{0, rs("running", 1, 5, "first", "move", "walk", "walking")},
		leaderStep{s(5), rs("paused", 2, 5, "first", "pull", "gather", "3 dead: regroup (cmd revive), then .bot run resume")},
		leaderStep{s(10), rs("running", 3, 5, "first", "pull", "pull", "pulling 299")},
		leaderStep{s(20), rs("done", 5, 5, "second", "move", "walk", "plan done")})
	var d *RemoteDriver
	paused, asked := false, false
	g.onStatus = func(l *fakeLeader, st tbm.RunStatus) {
		if st.State == tbm.RunPaused {
			paused = true
			l.paused = true
		}
	}
	tick := g.onTick
	g.onTick = func(gr *fakeRemote) {
		if paused && !asked {
			asked = true
			if err := d.RunControl("resume"); err != nil {
				t.Error(err)
			}
		}
		tick(gr)
	}
	d = NewRemoteDriver(observePlan(), g, DefaultConfig(), observeOpts(ObserveSpec{}), g.clock())
	err := d.Run(context.Background(), 0)
	rep, _ := d.Status()
	if err != nil || rep.State != "done" {
		t.Fatalf("%v %s %s", err, rep.State, rep.Error)
	}
	if g.indexOf("run resume") < 0 {
		t.Fatalf("the run was never resumed: %v", g.orders)
	}
	ob := rep.Remote.Observe
	if len(ob.PlayerOrders) != 0 {
		t.Fatalf("player orders %v", ob.PlayerOrders)
	}
	for _, c := range ob.Controls {
		if strings.HasPrefix(c, "player order") || strings.HasPrefix(c, "resumed after") {
			t.Fatalf("controls %v", ob.Controls)
		}
	}
}

// The module appends to the note of a pause it already made ("; boss meanwhile: ..."): that is the same player order, counted once.
func TestObservePlayerOrderPauseRepeatedNoteCountsOnce(t *testing.T) {
	g := newFakeLeader(t,
		leaderStep{0, rs("running", 1, 5, "first", "move", "walk", "walking")},
		leaderStep{s(5), rs("paused", 2, 5, "first", "pull", "gather", "player order (cmd summon Healy): .bot run resume to go on")},
		leaderStep{s(10), rs("paused", 2, 5, "first", "pull", "gather", "player order (cmd summon Healy): .bot run resume to go on; boss meanwhile: .bot run resume goes on with step 3")},
		leaderStep{s(20), rs("done", 5, 5, "second", "move", "walk", "plan done")})
	rep, err := runRemote(t, g, observePlan(), observeOpts(ObserveSpec{}), nil)
	if err != nil || rep.State != "done" {
		t.Fatalf("%v %s %s", err, rep.State, rep.Error)
	}
	if ob := rep.Remote.Observe; len(ob.PlayerOrders) != 1 {
		t.Fatalf("player orders %v", ob.PlayerOrders)
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

// A partial start (#248) goes out in the start line: the probe asks the resolver with the step count `.bot run list` gave, and
// startRunLeader sends its `from ...` words.
func TestObserveStartsPartway(t *testing.T) {
	g := newFakeLeader(t, happyScript()...)
	o := observeOpts(ObserveSpec{})
	asked := -1
	o.StartFrom = func(serverSteps int) (PartialStart, error) {
		asked = serverSteps
		return PartialStart{Words: "from step 3", Step: 3, Leg: -1, Point: Point{}, HavePoint: true}, nil
	}
	g.startExtra = []string{"The run starts at step 3 of 5 (leg first): the party is in the instance."}
	rep, err := runRemote(t, g, observePlan(), o, nil)
	if err != nil || rep.State != "done" {
		t.Fatalf("%v %s %s", err, rep.State, rep.Error)
	}
	if asked != 5 { // the fake lists "rt (map 0, 5 steps)"
		t.Fatalf("the resolver saw %d steps, want 5", asked)
	}
	if !slices.Contains(g.orders, "run start rt from step 3") {
		t.Fatalf("orders %v", g.orders)
	}
	if !strings.Contains(strings.Join(rep.Notes, "\n"), "starts partway") {
		t.Fatalf("notes %v", rep.Notes)
	}
}

// A partial start the resolver cannot map is refused at the probe, before a bot is hired or bound.
func TestObserveStartFromRefusedAtTheProbe(t *testing.T) {
	g := newFakeLeader(t, happyScript()...)
	o := observeOpts(ObserveSpec{})
	o.StartFrom = func(int) (PartialStart, error) {
		return PartialStart{}, errors.New("from_pull_group 3: the server's plan has 226 steps, the plan file's rows give 198")
	}
	_, err := runRemote(t, g, observePlan(), o, nil)
	if err == nil || !strings.Contains(err.Error(), "observe mode: from_pull_group 3") {
		t.Fatalf("%v", err)
	}
	if len(g.ordersOf("commander bind")) != 0 || len(g.ordersOf("run start")) != 0 {
		t.Fatalf("something was sent: %v", g.orders)
	}
}

// A busy run of the same plan is adopted for a partial start only when its status proves it is at the requested step (A).
func TestObserveBusyAtTheRequestedStepIsAdopted(t *testing.T) {
	g := newFakeLeader(t, happyScript()...)
	g.running = true
	g.emit(rs("running", 3, 5, "first", "move", "walk", "walking"))
	g.progress, g.next = s(20), 3
	o := observeOpts(ObserveSpec{})
	o.StartFrom = func(int) (PartialStart, error) {
		return PartialStart{Words: "from step 3", Step: 3, Leg: -1, Point: Point{}, HavePoint: true}, nil
	}
	rep, err := runRemote(t, g, observePlan(), o, nil)
	if err != nil || rep.State != "done" {
		t.Fatalf("%v %s %s", err, rep.State, rep.Error)
	}
	if !strings.Contains(strings.Join(rep.Notes, "\n"), "already going") {
		t.Fatalf("notes %v", rep.Notes)
	}
}

// At another step the run going is not watched: the watch would not be the requested start (A).
func TestObserveBusyAtAnotherStepIsRefused(t *testing.T) {
	g := newFakeLeader(t, happyScript()...)
	g.running = true
	g.emit(rs("running", 2, 5, "first", "pull", "pull", "pulling"))
	g.progress, g.next = s(9), 2
	o := observeOpts(ObserveSpec{})
	o.StartFrom = func(int) (PartialStart, error) {
		return PartialStart{Words: "from step 3", Step: 3, Leg: -1, Point: Point{}, HavePoint: true}, nil
	}
	_, err := runRemote(t, g, observePlan(), o, nil)
	if err == nil || !strings.Contains(err.Error(), "busy") || !strings.Contains(err.Error(), "step 2") || !strings.Contains(err.Error(), "requested step 3") {
		t.Fatalf("%v", err)
	}
	if g.indexOf("run stop") >= 0 {
		t.Fatalf("another run was stopped: %v", g.orders)
	}
}

// A leg start by name is adopted only when the status stands in that leg; a leg sent as a number cannot be compared, so it never adopts.
func TestObserveBusyLegStart(t *testing.T) {
	newBusy := func(leg string) *fakeLeader {
		g := newFakeLeader(t,
			leaderStep{0, rs("running", 4, 5, leg, "move", "walk", "walking")},
			leaderStep{s(30), rs("done", 5, 5, leg, "move", "walk", "plan done")})
		g.running = true
		g.emit(g.script[0].st) // the status `run status` answers with, before the clock moves
		g.next = 1
		return g
	}
	legStart := func(name string) func(int) (PartialStart, error) {
		return func(int) (PartialStart, error) {
			return PartialStart{Words: "from leg " + name, Leg: 1, LegNumber: 1, LegName: name, Point: Point{}, HavePoint: true}, nil
		}
	}
	o := observeOpts(ObserveSpec{})
	o.StartFrom = legStart("second")
	if rep, err := runRemote(t, newBusy("second"), observePlan(), o, nil); err != nil || rep.State != "done" {
		t.Fatalf("same leg: %v %s %s", err, rep.State, rep.Error)
	}
	o = observeOpts(ObserveSpec{})
	o.StartFrom = legStart("second")
	if _, err := runRemote(t, newBusy("first"), observePlan(), o, nil); err == nil || !strings.Contains(err.Error(), `leg "first"`) {
		t.Fatalf("another leg: %v", err)
	}
	o = observeOpts(ObserveSpec{})
	o.StartFrom = func(int) (PartialStart, error) {
		return PartialStart{Words: "from 1", Leg: 1, LegNumber: 1, Point: Point{}, HavePoint: true}, nil
	}
	if _, err := runRemote(t, newBusy("second"), observePlan(), o, nil); err == nil || !strings.Contains(err.Error(), "cannot be compared") {
		t.Fatalf("a numeric leg: %v", err)
	}
}

// The run leader's start message names the leg it really started in; a mismatch with the plan file is a drifted deployment: the run is
// stopped and not watched (D). The message is also the proof of the start step and of a start moved back into an event (G).
func TestObserveChecksTheStartMessage(t *testing.T) {
	partial := func() RemoteOptions {
		o := observeOpts(ObserveSpec{})
		o.StartFrom = func(int) (PartialStart, error) {
			return PartialStart{Words: "from leg second", Leg: 1, LegNumber: 1, LegName: "second", Point: Point{}, HavePoint: true}, nil
		}
		return o
	}
	g := newFakeLeader(t, happyScript()...)
	g.startExtra = []string{"The run starts at step 4 of 5 (leg second): the party is in the instance; the steps before it are not done or checked. The start was moved back to the step that starts the event it fell in."}
	rep, err := runRemote(t, g, observePlan(), partial(), nil)
	if err != nil || rep.State != "done" {
		t.Fatalf("%v %s %s", err, rep.State, rep.Error)
	}
	notes := strings.Join(rep.Notes, "\n")
	if !strings.Contains(notes, "starts at step 4 of 5 (leg second)") || !strings.Contains(notes, "moved the start back") {
		t.Fatalf("notes %v", rep.Notes)
	}

	g = newFakeLeader(t, happyScript()...)
	g.startExtra = []string{"The run starts at step 1 of 5 (leg first): the party is in the instance."}
	_, err = runRemote(t, g, observePlan(), partial(), nil)
	if err == nil || !strings.Contains(err.Error(), "not the requested leg") {
		t.Fatalf("%v", err)
	}
	if g.indexOf("run stop") < 0 {
		t.Fatalf("the drifted run was not stopped: %v", g.orders)
	}
	// Not watched: no status poll of the watch (the end's own `cmd stay` is every remote run's).
	if len(g.ordersOf("run status")) != 0 {
		t.Fatalf("the drifted run was watched: %v", g.orders)
	}
}

// A partway start is refused when the lead bot stands far from its step (E); observe.allow_far_start turns it into a note.
func TestObservePartwayStartChecksThePlace(t *testing.T) {
	at := func(allow bool) RemoteOptions {
		o := observeOpts(ObserveSpec{AllowFarStart: allow})
		o.StartFrom = func(int) (PartialStart, error) {
			return PartialStart{Words: "from step 3", Step: 3, Leg: -1, Point: Point{X: 300, Y: 0, Z: 0}, HavePoint: true}, nil
		}
		return o
	}
	g := newFakeLeader(t, happyScript()...)
	_, err := runRemote(t, g, observePlan(), at(false), nil)
	if err == nil || !strings.Contains(err.Error(), "yd from the start step's point") || !strings.Contains(err.Error(), "allow_far_start") {
		t.Fatalf("%v", err)
	}
	if g.indexOf("run start") >= 0 {
		t.Fatalf("the run was started from the wrong place: %v", g.orders)
	}
	g = newFakeLeader(t, happyScript()...)
	g.startExtra = []string{"The run starts at step 3 of 5 (leg first): the party is in the instance."}
	rep, err := runRemote(t, g, observePlan(), at(true), nil)
	if err != nil || rep.State != "done" {
		t.Fatalf("%v %s %s", err, rep.State, rep.Error)
	}
	if !strings.Contains(strings.Join(rep.Notes, "\n"), "allow_far_start") {
		t.Fatalf("notes %v", rep.Notes)
	}
}

// A partial start is proven, not trusted (fail closed): an ACK without the start message is asked of `run status`; at another step the
// newly started run is stopped and the watch refused (no polls after the proof ask).
func TestObserveACKOnlyStartWithoutProofIsStopped(t *testing.T) {
	g := newFakeLeader(t, happyScript()...) // its first status is step 1, the start asks for step 3
	o := observeOpts(ObserveSpec{})
	o.StartFrom = func(int) (PartialStart, error) {
		return PartialStart{Words: "from step 3", Step: 3, Leg: -1, Point: Point{}, HavePoint: true}, nil
	}
	_, err := runRemote(t, g, observePlan(), o, nil)
	if err == nil || !strings.Contains(err.Error(), "start was not proven") || !strings.Contains(err.Error(), "not the requested step 3") {
		t.Fatalf("%v", err)
	}
	if g.indexOf("run stop") < 0 {
		t.Fatalf("the unproven run was not stopped: %v", g.orders)
	}
	if n := len(g.ordersOf("run status")); n != 1 {
		t.Fatalf("%d run status asks: one for the proof, none for a watch: %v", n, g.orders)
	}
}

// The status line is the second source: a module that answers the start with an ACK alone is accepted when its status is at the step.
func TestObserveACKOnlyStartWithStatusProofIsAccepted(t *testing.T) {
	g := newFakeLeader(t,
		leaderStep{0, rs("running", 3, 5, "first", "move", "walk", "walking")},
		leaderStep{s(5), rs("done", 5, 5, "first", "move", "walk", "plan done")})
	o := observeOpts(ObserveSpec{})
	o.StartFrom = func(int) (PartialStart, error) {
		return PartialStart{Words: "from step 3", Step: 3, Leg: -1, Point: Point{}, HavePoint: true}, nil
	}
	rep, err := runRemote(t, g, observePlan(), o, nil)
	if err != nil || rep.State != "done" {
		t.Fatalf("%v %s %s", err, rep.State, rep.Error)
	}
	if !strings.Contains(strings.Join(rep.Notes, "\n"), "proves the start") {
		t.Fatalf("notes %v", rep.Notes)
	}
}

// No start-step point means the place cannot be checked: refused, unless observe.allow_far_start (fail closed).
func TestObservePartwayStartNeedsThePlace(t *testing.T) {
	o := func(allow bool) RemoteOptions {
		spec := ObserveSpec{AllowFarStart: allow}
		opts := observeOpts(spec)
		opts.StartFrom = func(int) (PartialStart, error) {
			return PartialStart{Words: "from step 3", Step: 3, Leg: -1}, nil // no point
		}
		return opts
	}
	g := newFakeLeader(t, happyScript()...)
	_, err := runRemote(t, g, observePlan(), o(false), nil)
	if err == nil || !strings.Contains(err.Error(), "start step's point is not known") || !strings.Contains(err.Error(), "allow_far_start") {
		t.Fatalf("%v", err)
	}
	if g.indexOf("run start") >= 0 {
		t.Fatalf("the run was started without a place to check: %v", g.orders)
	}
	g = newFakeLeader(t, happyScript()...)
	g.startExtra = []string{"The run starts at step 3 of 5 (leg first): the party is in the instance."}
	rep, err := runRemote(t, g, observePlan(), o(true), nil)
	if err != nil || rep.State != "done" {
		t.Fatalf("%v %s %s", err, rep.State, rep.Error)
	}
	if !strings.Contains(strings.Join(rep.Notes, "\n"), "point is not known") || !strings.Contains(strings.Join(rep.Notes, "\n"), "allow_far_start") {
		t.Fatalf("notes %v", rep.Notes)
	}
}

// The parsed start reply must report the requested step and leg (round 4): a reply at another step is stopped once, without a watch
// poll, for a from_step start and for a from_pull_group start; the requested one is accepted; the module's documented move-back into a
// scripted event is the only step difference accepted, with a note.
func TestObserveStartReplyStepMustMatch(t *testing.T) {
	opts := func(ps PartialStart) RemoteOptions {
		o := observeOpts(ObserveSpec{})
		o.StartFrom = func(int) (PartialStart, error) { return ps, nil }
		return o
	}
	stepStart := PartialStart{Words: "from step 3", Step: 3, Leg: -1, Point: Point{}, HavePoint: true}
	for _, c := range []struct {
		name  string
		start PartialStart
		line  string
		want  string
	}{
		{"from_step at another step", stepStart, "The run starts at step 1 of 5 (leg first): the party is in the instance.", "not the requested step 3"},
		{"from_pull_group at another step", PartialStart{Words: "from step 4", Step: 4, Leg: 1, LegNumber: 1, LegName: "second", ExpectLeg: "second", Point: Point{}, HavePoint: true},
			"The run starts at step 2 of 5 (leg second): the party is in the instance.", "not the requested step 4"},
		{"from_step in another leg", PartialStart{Words: "from step 3", Step: 3, Leg: -1, ExpectLeg: "first", Point: Point{}, HavePoint: true},
			"The run starts at step 3 of 5 (leg second): the party is in the instance.", `not the requested leg "first"`},
	} {
		g := newFakeLeader(t, happyScript()...)
		g.startExtra = []string{c.line}
		_, err := runRemote(t, g, observePlan(), opts(c.start), nil)
		if err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: %v", c.name, err)
			continue
		}
		if n := len(g.ordersOf("run stop")); n != 1 {
			t.Errorf("%s: %d run stops, want exactly 1: %v", c.name, n, g.orders)
		}
		if n := len(g.ordersOf("run status")); n != 0 {
			t.Errorf("%s: %d status asks, want none (no watch): %v", c.name, n, g.orders)
		}
	}

	g := newFakeLeader(t, happyScript()...)
	g.startExtra = []string{"The run starts at step 3 of 5 (leg first): the party is in the instance."}
	if rep, err := runRemote(t, g, observePlan(), opts(stepStart), nil); err != nil || rep.State != "done" {
		t.Fatalf("the requested step: %v %s %s", err, rep.State, rep.Error)
	}

	g = newFakeLeader(t, happyScript()...)
	g.startExtra = []string{"The run starts at step 2 of 5 (leg first): the party is in the instance. The start was moved back to the step that starts the event it fell in."}
	rep, err := runRemote(t, g, observePlan(), opts(stepStart), nil)
	if err != nil || rep.State != "done" {
		t.Fatalf("the documented move-back: %v %s %s", err, rep.State, rep.Error)
	}
	if n := len(g.ordersOf("run stop")); n != 0 {
		t.Fatalf("the documented move-back was stopped: %v", g.orders)
	}
	if !strings.Contains(strings.Join(rep.Notes, "\n"), "moved the start back") {
		t.Fatalf("notes %v", rep.Notes)
	}
}

// A run starts by undoing the `loot off` a crashed run's boss may have left (lootOnAtStart). An observed run may keep the operator's
// setting instead (observe.keep_loot_setting): the graves legs turn the companions' looting off by themselves, and an observed run never
// reaches them. The setting is observe only: a driven run always sends the orders.
func TestLootOnAtStartSendsBehaviorOrdersUnlessKept(t *testing.T) {
	all := []string{"Tanky loot true", "Healy loot true", "Magey loot true"}
	for _, c := range []struct {
		name string
		o    RemoteOptions
		want []string
	}{
		{"driven", opts(), all},
		{"observe", observeOpts(ObserveSpec{}), all},
		{"observe with keep_loot_setting", observeOpts(ObserveSpec{KeepLootSetting: true}), nil},
	} {
		g := newFakeCarrier(t)
		d := NewRemoteDriver(observePlan(), g, DefaultConfig(), c.o, g.clock())
		d.fr.Party = []tbm.PartyRec{{Name: "Tanky"}, {Name: "Healy"}, {Name: "Magey"}}
		d.lootOnAtStart(context.Background())
		if !slices.Equal(g.behaviors, c.want) {
			t.Errorf("%s: behaviors %v, want %v", c.name, g.behaviors, c.want)
		}
		notes := strings.Join(d.Report().Notes, "\n")
		if c.want == nil {
			if !strings.Contains(notes, "loot setting kept as the operator set it") {
				t.Errorf("%s: no note about the kept setting: %v", c.name, notes)
			}
		} else if strings.Contains(notes, "loot setting kept") {
			t.Errorf("%s: the kept-setting note went out: %v", c.name, notes)
		}
	}
}

// restScript walks, rests 20 s in the pull, pulls, rests 10 s at the boss, pulls, and is done 40 s later.
func restScript() []leaderStep {
	return []leaderStep{
		{0, rs("running", 1, 5, "first", "move", "walk", "walking")},
		{s(10), rs("running", 2, 5, "first", "pull", "rest", "resting")},
		{s(30), rs("running", 2, 5, "first", "pull", "pull", "pulling")},
		{s(50), rs("running", 3, 5, "first", "boss", "rest", "resting")},
		{s(60), rs("running", 3, 5, "first", "boss", "pull", "pulling")},
		{s(100), rs("done", 5, 5, "second", "move", "walk", "plan done")},
	}
}

// restEvents are the trace's "rest" events.
func restEvents(f *trace.File) []trace.Event {
	var out []trace.Event
	for _, e := range f.Events {
		if e.Ev == "rest" {
			out = append(out, e)
		}
	}
	return out
}

// The run leader's own rest phase is the observed run's rest: an observe run has no gate of the agent's, so the time a status says
// phase rest counts as a rest gate, in the report, the leg and the trace.
func TestObserveCountsTheRunLeadersRestPhase(t *testing.T) {
	g := newFakeLeader(t, restScript()...)
	dir := t.TempDir()
	rep, err := runRemote(t, g, observePlan(), observeOpts(ObserveSpec{}), func(c *Config) {
		c.Trace = tracerun.Config{On: true, Dir: dir, RunID: "r-observe-rest"}
	})
	if err != nil || rep.State != "done" {
		t.Fatalf("%v %s %s", err, rep.State, rep.Error)
	}
	if rep.RestSeconds < 25 || rep.RestSeconds > 35 {
		t.Fatalf("rest seconds %v (want 30, 20 s + 10 s)\n%s", rep.RestSeconds, notesOf(rep))
	}
	if len(rep.RestGates) != 2 {
		t.Fatalf("rest gates %+v (want 2)", rep.RestGates)
	}
	for _, gate := range rep.RestGates {
		if gate.Result != "run_leader" {
			t.Errorf("gate %+v: result %q, want run_leader", gate, gate.Result)
		}
	}
	var sum float64
	for _, l := range rep.Legs {
		sum += l.RestSeconds
	}
	if diff := sum - rep.RestSeconds; diff > 0.001 || diff < -0.001 {
		t.Errorf("legs rest %v, report rest %v", sum, rep.RestSeconds)
	}
	if rep.RestShare <= 0 {
		t.Errorf("rest share %v", rep.RestShare)
	}
	f, err := trace.Read(filepath.Join(dir, "r-observe-rest.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	rests := restEvents(f)
	if len(rests) != 2 {
		t.Fatalf("%d rest events in the trace (want 2)", len(rests))
	}
	for _, e := range rests {
		if e.D["source"] != "run_leader" {
			t.Errorf("rest event %v: source %v", e.Ev, e.D["source"])
		}
		if secs, ok := e.D["seconds"].(float64); !ok || secs <= 0 {
			t.Errorf("rest event seconds %v (%T)", e.D["seconds"], e.D["seconds"])
		}
	}
	for _, c := range tracecheck.Score(f, tracecheck.Defaults()) {
		if c.ID == tracecheck.IDRestShare && c.Value <= 0 {
			t.Fatalf("the rest_share check reads %v: %+v", c.Value, c)
		}
	}
}

// A pause is not rest: a paused status still carries the phase it was paused in, but the time a run waits paused is not the run
// leader resting (PausedWait is 10 min, and that would fail rest_share). The open rest closes when the run pauses; a resume in the
// same phase opens a new one.
func TestObservePausedTimeIsNotRest(t *testing.T) {
	g := newFakeLeader(t,
		leaderStep{0, rs("running", 1, 5, "first", "move", "walk", "walking")},
		leaderStep{s(10), rs("running", 2, 5, "first", "pull", "rest", "resting")},
		leaderStep{s(20), rs("paused", 2, 5, "first", "pull", "rest", "3 dead: regroup (cmd revive), then .bot run resume")},
		leaderStep{s(300), rs("running", 2, 5, "first", "pull", "pull", "pulling 299")},
		leaderStep{s(320), rs("done", 5, 5, "first", "pull", "pull", "plan done")},
	)
	rep, err := runRemote(t, g, observePlan(), observeOpts(ObserveSpec{}), nil)
	if err != nil || rep.State != "done" {
		t.Fatalf("%v %s %s", err, rep.State, rep.Error)
	}
	if rep.RestSeconds < 9 || rep.RestSeconds > 11 {
		t.Fatalf("rest seconds %v (want about 10: the rest before the pause, not the 280 s pause)\n%s", rep.RestSeconds, notesOf(rep))
	}
	if len(rep.RestGates) != 1 {
		t.Fatalf("rest gates %+v (want 1)", rep.RestGates)
	}
	if rep.RestGates[0].Result != "run_leader" {
		t.Errorf("gate %+v: result %q, want run_leader", rep.RestGates[0], rep.RestGates[0].Result)
	}
}

// A rest the run leader is still in when the run ends is closed at the end, so its time is not lost.
func TestObserveCountsARestThatIsOpenWhenTheRunEnds(t *testing.T) {
	g := newFakeLeader(t,
		leaderStep{0, rs("running", 1, 5, "first", "move", "walk", "walking")},
		leaderStep{s(10), rs("running", 2, 5, "first", "pull", "rest", "resting")},
		leaderStep{s(40), rs("done", 5, 5, "first", "move", "rest", "plan done")},
	)
	dir := t.TempDir()
	rep, err := runRemote(t, g, observePlan(), observeOpts(ObserveSpec{}), func(c *Config) {
		c.Trace = tracerun.Config{On: true, Dir: dir, RunID: "r-observe-open-rest"}
	})
	if err != nil || rep.State != "done" {
		t.Fatalf("%v %s %s", err, rep.State, rep.Error)
	}
	if len(rep.RestGates) != 1 {
		t.Fatalf("rest gates %+v (want 1)", rep.RestGates)
	}
	if rep.RestSeconds < 25 {
		t.Fatalf("rest seconds %v (want about 30)", rep.RestSeconds)
	}
	f, err := trace.Read(filepath.Join(dir, "r-observe-open-rest.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	if rests := restEvents(f); len(rests) != 1 {
		t.Fatalf("%d rest events in the trace (want 1)", len(rests))
	}
}

// A run that never says phase rest has no rest: the leader's phase is not a rest gate until it says so.
func TestObserveWithNoRestPhaseCountsNoRest(t *testing.T) {
	g := newFakeLeader(t, happyScript()...)
	dir := t.TempDir()
	rep, err := runRemote(t, g, observePlan(), observeOpts(ObserveSpec{}), func(c *Config) {
		c.Trace = tracerun.Config{On: true, Dir: dir, RunID: "r-observe-no-rest"}
	})
	if err != nil || rep.State != "done" {
		t.Fatalf("%v %s %s", err, rep.State, rep.Error)
	}
	if rep.RestSeconds != 0 || len(rep.RestGates) != 0 {
		t.Fatalf("rest %v in %+v, want none", rep.RestSeconds, rep.RestGates)
	}
	f, err := trace.Read(filepath.Join(dir, "r-observe-no-rest.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	if rests := restEvents(f); len(rests) != 0 {
		t.Fatalf("%d rest events in the trace (want none)", len(rests))
	}
}

// instanceEvents are the trace's "instance" events.
func instanceEvents(f *trace.File) []trace.Event {
	var out []trace.Event
	for _, e := range f.Events {
		if e.Ev == "instance" {
			out = append(out, e)
		}
	}
	return out
}

// The lead bot's instance id comes from the feed: the report carries it, the trace has one instance event, and the summary reads it
// from that event.
func TestObserveExposesTheInstanceId(t *testing.T) {
	g := newFakeLeader(t, happyScript()...)
	g.party[0].Instance = 4321
	dir := t.TempDir()
	rep, err := runRemote(t, g, observePlan(), observeOpts(ObserveSpec{}), func(c *Config) {
		c.Trace = tracerun.Config{On: true, Dir: dir, RunID: "r-observe-inst"}
	})
	if err != nil || rep.State != "done" || rep.Trace == nil {
		t.Fatalf("%v %s %s %+v", err, rep.State, rep.Error, rep.Trace)
	}
	if rep.Instance != 4321 {
		t.Fatalf("report instance %d, want 4321", rep.Instance)
	}
	f, err := trace.Read(rep.Trace.Path)
	if err != nil {
		t.Fatal(err)
	}
	insts := instanceEvents(f)
	if len(insts) != 1 {
		t.Fatalf("%d instance events in the trace (want 1)", len(insts))
	}
	if v, ok := insts[0].D["instance"].(float64); !ok || v != 4321 {
		t.Fatalf("instance event %v", insts[0].D)
	}
	if _, ok := insts[0].D["map"]; !ok {
		t.Fatalf("the instance event has no map: %v", insts[0].D)
	}
	b, err := os.ReadFile(trace.SummaryPath(rep.Trace.Path))
	if err != nil {
		t.Fatal(err)
	}
	var sum trace.Summary
	if err := json.Unmarshal(b, &sum); err != nil {
		t.Fatal(err)
	}
	if sum.Instance != 4321 {
		t.Fatalf("summary instance %d, want 4321: %s", sum.Instance, b)
	}

	// A feed with no instance id says none, and writes no instance event.
	g = newFakeLeader(t, happyScript()...)
	dir = t.TempDir()
	rep, err = runRemote(t, g, observePlan(), observeOpts(ObserveSpec{}), func(c *Config) {
		c.Trace = tracerun.Config{On: true, Dir: dir, RunID: "r-observe-no-inst"}
	})
	if err != nil || rep.State != "done" || rep.Trace == nil {
		t.Fatalf("%v %s %s %+v", err, rep.State, rep.Error, rep.Trace)
	}
	if rep.Instance != 0 {
		t.Fatalf("report instance %d, want 0", rep.Instance)
	}
	f, err = trace.Read(rep.Trace.Path)
	if err != nil {
		t.Fatal(err)
	}
	if insts := instanceEvents(f); len(insts) != 0 {
		t.Fatalf("%d instance events in the trace (want none)", len(insts))
	}
}

// TestSamePauseNoteWithRunLabel: an until or test-speed run's status lines carry a trailing label; the parser takes it
// off the note, so a pause the module extends with an aside is still the latched pause.
func TestSamePauseNoteWithRunLabel(t *testing.T) {
	a, _ := tbm.ParseRunLine("TBM:RUN|zul_gurub|paused|29/1104|j|pull|pull|15 dead: regroup (cmd revive party), then .bot run resume|until boss (step 41)")
	b, _ := tbm.ParseRunLine("TBM:RUN|zul_gurub|paused|29/1104|j|pull|pull|15 dead: regroup (cmd revive party), then .bot run resume; boss meanwhile: x|until boss (step 41)")
	if !samePauseNote(a.Note, b.Note) {
		t.Fatalf("%q then %q: a new pause", a.Note, b.Note)
	}
	if a.Label != "until boss (step 41)" {
		t.Fatalf("label %q", a.Label)
	}
}
