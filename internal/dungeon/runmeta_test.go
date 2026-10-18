package dungeon

import (
	"context"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/pjerra/turtle-agent/internal/trace"
	"github.com/pjerra/turtle-agent/internal/tracerun"
)

// RunMeta (runmeta.go): the driver value joins model, effort and run with colons, and Check refuses what the server's
// driver= field cannot carry.
func TestRunMetaDriverAndCheck(t *testing.T) {
	m := RunMeta{Model: "haiku", Effort: "max", Run: "zf-ab-2-s5"}
	if m.Driver() != "haiku:max:zf-ab-2-s5" {
		t.Fatalf("driver %q", m.Driver())
	}
	if (RunMeta{}).Set() || !(RunMeta{Run: "x"}).Set() {
		t.Fatal("Set is wrong")
	}
	if (RunMeta{}).Driver() != "-:-:-" {
		t.Fatalf("empty driver %q", (RunMeta{}).Driver())
	}
	for _, bad := range []RunMeta{{Model: "a b"}, {Effort: "x/y"}, {Run: "a:b"}, {Model: strings.Repeat("m", 65)}} {
		if bad.Check() == "" {
			t.Fatalf("%+v passed", bad)
		}
	}
	// The server stores the joined driver value, at most 96 characters: 96 passes, 97 is refused.
	full := RunMeta{Model: strings.Repeat("m", 32), Effort: strings.Repeat("e", 31), Run: strings.Repeat("r", 31)}
	if len(full.Driver()) != 96 || full.Check() != "" {
		t.Fatalf("driver %d chars refused: %q", len(full.Driver()), full.Check())
	}
	over := RunMeta{Model: strings.Repeat("m", 32), Effort: strings.Repeat("e", 32), Run: strings.Repeat("r", 31)}
	if len(over.Driver()) != 97 || over.Check() == "" {
		t.Fatalf("driver %d chars passed", len(over.Driver()))
	}
	if (RunMeta{ServerTrace: true}).Check() == "" {
		t.Fatalf("server_trace without a driver accepted")
	}
	if m.Check() != "" || (RunMeta{}).Check() != "" {
		t.Fatalf("good meta refused: %q", m.Check())
	}
}

// A trace_meta key the caller set keeps its value when run_meta is merged into the trace header: the driver's keys never
// overwrite it.
func TestRunMetaHeaderKeepsCallerKeys(t *testing.T) {
	got := runMetaHeader(map[string]string{"run_model": "caller-model", "tb_sha": "abc"}, RunMeta{Model: "haiku", Effort: "max", Run: "zf-1"})
	if got["run_model"] != "caller-model" || got["run_effort"] != "max" || got["run_label"] != "zf-1" || got["tb_sha"] != "abc" {
		t.Fatalf("header %v", got)
	}
	if runMetaHeader(nil, RunMeta{}) != nil {
		t.Fatal("empty merge is not nil")
	}
}

// A remote run started with run_meta sends `.bot trace start <run_id> ... driver=<model>:<effort>:<run>` using the
// agent's own run id, and writes the meta into the trace header and the run report.
func TestRemoteRunMetaSendsTraceStart(t *testing.T) {
	g := newFakeRemote(t)
	g.addMob(299, Point{150, 5, 0})
	plan := remotePlan(Leg{Name: "first", To: Point{100, 0, 0}, PullGroups: []PullGroup{{Entries: []uint32{299}, Positions: []Point{{150, 5, 0}}}}},
		Leg{Name: "second", To: Point{200, 0, 0}})
	o := opts()
	meta := RunMeta{Model: "haiku", Effort: "max", Run: "zf-ab-2-s5", ServerTrace: true}
	o.RunMeta = meta
	rep, err := runRemote(t, g, plan, o, nil)
	if err != nil {
		t.Fatalf("%v %+v", err, rep)
	}
	want := "trace start " + rep.RunID + " driver=haiku:max:zf-ab-2-s5"
	found := false
	for _, cmd := range g.orders {
		if cmd == want {
			found = true
		}
		if strings.HasPrefix(cmd, "trace start ") && cmd != want {
			t.Fatalf("wrong trace start: %q", cmd)
		}
	}
	if !found {
		t.Fatalf("no %q in %v", want, g.orders)
	}
	if rep.RunMeta == nil || *rep.RunMeta != meta {
		t.Fatalf("report meta %+v", rep.RunMeta)
	}
}

// Without run_meta nothing is sent: no `trace start` order goes out and the report carries no meta.
func TestRemoteRunWithoutMetaSendsNoTraceStart(t *testing.T) {
	g := newFakeRemote(t)
	g.addMob(299, Point{150, 5, 0})
	plan := remotePlan(Leg{Name: "first", To: Point{100, 0, 0}, PullGroups: []PullGroup{{Entries: []uint32{299}, Positions: []Point{{150, 5, 0}}}}},
		Leg{Name: "second", To: Point{200, 0, 0}})
	rep, err := runRemote(t, g, plan, opts(), nil)
	if err != nil {
		t.Fatalf("%v %+v", err, rep)
	}
	for _, cmd := range g.orders {
		if strings.HasPrefix(cmd, "trace start ") {
			t.Fatalf("trace start sent without run_meta: %q", cmd)
		}
		if cmd == "trace stop" {
			t.Fatalf("trace stop sent without run_meta")
		}
	}
	if rep.RunMeta != nil {
		t.Fatalf("report meta %+v", rep.RunMeta)
	}
}

// The run ends the server trace it started: `trace stop` goes out after an acked start, in order, and never without
// run_meta. A refused start leaves nothing running, so no stop follows it.
func TestRemoteRunMetaStopsTraceAtEnd(t *testing.T) {
	planOf := func() *Plan {
		return remotePlan(Leg{Name: "first", To: Point{100, 0, 0}, PullGroups: []PullGroup{{Entries: []uint32{299}, Positions: []Point{{150, 5, 0}}}}},
			Leg{Name: "second", To: Point{200, 0, 0}})
	}
	g := newFakeRemote(t)
	g.addMob(299, Point{150, 5, 0})
	o := opts()
	o.RunMeta = RunMeta{Model: "haiku", Effort: "max", Run: "zf-ab-2-s5", ServerTrace: true}
	rep, err := runRemote(t, g, planOf(), o, nil)
	if err != nil {
		t.Fatalf("%v %+v", err, rep)
	}
	start, stop := -1, -1
	for i, cmd := range g.orders {
		if cmd == "trace start "+rep.RunID+" driver=haiku:max:zf-ab-2-s5" {
			start = i
		}
		if cmd == "trace stop" {
			stop = i
		}
	}
	if start < 0 || stop < 0 || stop < start {
		t.Fatalf("start %d stop %d in %v", start, stop, g.orders)
	}
	// A refused start: no stop follows.
	g2 := newFakeRemote(t)
	g2.addMob(299, Point{150, 5, 0})
	g2.traceErr = "invalid"
	rep2, err := runRemote(t, g2, planOf(), o, nil)
	if err != nil {
		t.Fatalf("%v %+v", err, rep2)
	}
	for _, cmd := range g2.orders {
		if cmd == "trace stop" {
			t.Fatalf("trace stop after a refused start: %v", g2.orders)
		}
	}
}

// An observed run with run_meta sends the same trace start (its own run id, the agent's trace id): the run leader's
// `.bot run start` is untouched. Without run_meta the observer sends nothing extra.
func TestObserveRunMetaSendsTraceStart(t *testing.T) {
	g := newFakeLeader(t, happyScript()...)
	o := observeOpts(ObserveSpec{})
	o.RunMeta = RunMeta{Model: "haiku", Effort: "max", Run: "zf-ab-2-s5", ServerTrace: true}
	rep, err := runRemote(t, g, observePlan(), o, nil)
	if err != nil || rep.State != "done" {
		t.Fatalf("%v %s %s", err, rep.State, rep.Error)
	}
	want := "trace start " + rep.RunID + " driver=haiku:max:zf-ab-2-s5"
	found, runStarts := false, 0
	for _, cmd := range g.orders {
		if cmd == want {
			found = true
		}
		if strings.HasPrefix(cmd, "run start ") {
			runStarts++
			if strings.Contains(cmd, "driver=") {
				t.Fatalf("run start carries driver=: %q", cmd)
			}
		}
	}
	if !found {
		t.Fatalf("no %q in %v", want, g.orders)
	}
	if runStarts != 1 {
		t.Fatalf("%d run starts: %v", runStarts, g.orders)
	}
	if rep.RunMeta == nil || *rep.RunMeta != o.RunMeta {
		t.Fatalf("report meta %+v", rep.RunMeta)
	}

	g2 := newFakeLeader(t, happyScript()...)
	rep2, err := runRemote(t, g2, observePlan(), observeOpts(ObserveSpec{}), nil)
	if err != nil || rep2.State != "done" {
		t.Fatalf("%v %s %s", err, rep2.State, rep2.Error)
	}
	for _, cmd := range g2.orders {
		if strings.HasPrefix(cmd, "trace start ") {
			t.Fatalf("trace start sent without run_meta: %q", cmd)
		}
	}
	if rep2.RunMeta != nil {
		t.Fatalf("report meta %+v", rep2.RunMeta)
	}
}

// A group run sends no server trace line at all, with or without run_meta: party chat carries no answer, so a start
// the server refused (a trace someone started by hand is running: the server records one at a time) would look sent,
// and the run's stop could end that other trace. run_meta still goes into the run report and the trace header, and
// the report notes that the server trace was not sent.
func TestGroupRunMetaSendsNoServerTrace(t *testing.T) {
	g, p := runFixture(t)
	meta := RunMeta{Model: "haiku", Effort: "max", Run: "zf-ab-2-s5", ServerTrace: true}
	cfg := Config{Trace: tracerun.Config{On: true, Dir: t.TempDir(), RunID: "r-group-meta"}, RunMeta: meta}
	d := NewDriver(p, g, cfg, g.clock())
	if err := d.Run(context.Background(), 0); err != nil {
		t.Fatalf("%v\n%+v", err, d.Report())
	}
	for _, c := range g.chats {
		if c == ".bot trace stop" || (len(c) > 16 && c[:16] == ".bot trace start") {
			t.Fatalf("server trace line sent in group mode: %q", c)
		}
	}
	rep := d.Report()
	if rep.RunMeta == nil || *rep.RunMeta != meta {
		t.Fatalf("report meta %+v", rep.RunMeta)
	}
	if !slices.Contains(rep.Notes, "server trace not sent in group mode") {
		t.Fatalf("no group-mode note in %q", rep.Notes)
	}
	f, err := trace.Read(filepath.Join(cfg.Trace.Dir, "r-group-meta.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	if f.Header.Meta["run_model"] != "haiku" || f.Header.Meta["run_effort"] != "max" || f.Header.Meta["run_label"] != "zf-ab-2-s5" {
		t.Fatalf("header meta %v", f.Header.Meta)
	}

	// Without server_trace (the default) the meta goes into the report and the header, nothing goes to the server, and
	// there is nothing to note.
	g3, p3 := runFixture(t)
	plain := RunMeta{Model: "haiku", Effort: "max", Run: "zf-ab-2-s5"}
	d3 := NewDriver(p3, g3, Config{Trace: tracerun.Config{On: true, Dir: t.TempDir(), RunID: "r-group-default"}, RunMeta: plain}, g3.clock())
	if err := d3.Run(context.Background(), 0); err != nil {
		t.Fatalf("%v\n%+v", err, d3.Report())
	}
	for _, c := range g3.chats {
		if strings.HasPrefix(c, ".bot trace") {
			t.Fatalf("server trace line sent without server_trace: %q", c)
		}
	}
	if rep3 := d3.Report(); rep3.RunMeta == nil || *rep3.RunMeta != plain || slices.Contains(rep3.Notes, "server trace not sent in group mode") {
		t.Fatalf("report meta %+v notes %q", rep3.RunMeta, rep3.Notes)
	}

	g2, p2 := runFixture(t)
	d2 := NewDriver(p2, g2, Config{Trace: tracerun.Config{On: true, Dir: t.TempDir(), RunID: "r-group-plain"}}, g2.clock())
	if err := d2.Run(context.Background(), 0); err != nil {
		t.Fatalf("%v\n%+v", err, d2.Report())
	}
	rep2 := d2.Report()
	if rep2.RunMeta != nil {
		t.Fatalf("report meta without run_meta %+v", rep2.RunMeta)
	}
	if slices.Contains(rep2.Notes, "server trace not sent in group mode") {
		t.Fatalf("group-mode note without run_meta: %q", rep2.Notes)
	}
}

// runPanicking runs d and reports its error and whether a panic escaped Run (it should not: runRecovering turns it
// into a failed run; the recover is the test's belt-and-braces that the deferred trace stop lets the panic go on).
func runPanicking(t *testing.T, d *RemoteDriver) (err error, panicked bool) {
	t.Helper()
	func() {
		defer func() {
			if r := recover(); r != nil {
				panicked = true
			}
		}()
		err = d.Run(context.Background(), 0)
	}()
	return err, panicked
}

// traceStartStop finds where the run's trace start, its (last) trace stop and how many stops went out.
func traceStartStop(orders []string, runID string, meta RunMeta) (start, stop, stops int) {
	want := "trace start " + runID + " driver=" + meta.Driver()
	for i, cmd := range orders {
		if cmd == want {
			start = i
		}
		if cmd == "trace stop" {
			stop, stops = i, stops+1
		}
	}
	return start, stop, stops
}

// stopBeforeUnbind says the trace stop went out of the deferred cleanup, during the panic's unwind and before the
// run's own deferred unbind — not from finish, which runs only after runRecovering turned the panic into a failed run.
func stopBeforeUnbind(orders []string, stop int) bool {
	unbind := slices.Index(orders, "commander unbind")
	return unbind >= 0 && stop >= 0 && stop < unbind
}

// A panic after an acknowledged trace start ends the server's trace exactly once: the deferred stop, armed right
// after the ack, carries the panic path (finish runs only after a normal return). The panic goes on — runRecovering
// turns it into a failed run — and the test's recover catches it if it ever escapes Run.
func TestRemoteRunMetaStopsTraceOnPanic(t *testing.T) {
	planOf := func() *Plan {
		return remotePlan(Leg{Name: "first", To: Point{100, 0, 0}, PullGroups: []PullGroup{{Entries: []uint32{299}, Positions: []Point{{150, 5, 0}}}}},
			Leg{Name: "second", To: Point{200, 0, 0}})
	}
	o := opts()
	o.RunMeta = RunMeta{Model: "haiku", Effort: "max", Run: "zf-ab-2-s5", ServerTrace: true}
	g := newFakeRemote(t)
	g.addMob(299, Point{150, 5, 0})
	g.panicOn = "cmd move" // the first walk order after the acknowledged trace start
	d := NewRemoteDriver(planOf(), g, DefaultConfig(), o, g.clock())
	err, panicked := runPanicking(t, d)
	if !panicked && (err == nil || !strings.Contains(err.Error(), "internal error: boom")) {
		t.Fatalf("run error %v (panicked %v)", err, panicked)
	}
	rep, ended := d.Status()
	if !ended || (!panicked && rep.State != "failed") {
		t.Fatalf("run state %s ended %v (panicked %v, err %v)", rep.State, ended, panicked, err)
	}
	start, stop, stops := traceStartStop(g.orders, rep.RunID, o.RunMeta)
	if start < 0 || stops != 1 || stop < start {
		t.Fatalf("trace start %d, stop %d, %d stops: %v", start, stop, stops, g.orders)
	}
	if !stopBeforeUnbind(g.orders, stop) {
		t.Fatalf("trace stop %d did not go out of the deferred cleanup (unbind %d): %v", stop, slices.Index(g.orders, "commander unbind"), g.orders)
	}

	// A trace someone started by hand is already running: the server refuses our start, and nothing of this run —
	// neither the panic's deferred stop nor finish — may end the user's trace.
	g2 := newFakeRemote(t)
	g2.addMob(299, Point{150, 5, 0})
	g2.traceErr = "busy" // the server records one trace at a time: the manual trace is still running
	g2.panicOn = "cmd move"
	d2 := NewRemoteDriver(planOf(), g2, DefaultConfig(), o, g2.clock())
	err2, panicked2 := runPanicking(t, d2)
	if !panicked2 && (err2 == nil || !strings.Contains(err2.Error(), "internal error: boom")) {
		t.Fatalf("run error %v (panicked %v)", err2, panicked2)
	}
	for _, cmd := range g2.orders {
		if cmd == "trace stop" {
			t.Fatalf("trace stop after a refused start: %v", g2.orders)
		}
	}
}

// The same in observe mode: a panic after the acknowledged trace start ends the server's trace exactly once, and the
// panic goes on.
func TestObserveRunMetaStopsTraceOnPanic(t *testing.T) {
	g := newFakeLeader(t, happyScript()...)
	g.panicOn = "run start" // the first order an observed run sends after the acknowledged trace start
	o := observeOpts(ObserveSpec{})
	o.RunMeta = RunMeta{Model: "haiku", Effort: "max", Run: "zf-ab-2-s5", ServerTrace: true}
	d := NewRemoteDriver(observePlan(), g, DefaultConfig(), o, g.clock())
	err, panicked := runPanicking(t, d)
	if !panicked && (err == nil || !strings.Contains(err.Error(), "internal error: boom")) {
		t.Fatalf("run error %v (panicked %v)", err, panicked)
	}
	rep, ended := d.Status()
	if !ended || (!panicked && rep.State != "failed") {
		t.Fatalf("run state %s ended %v (panicked %v, err %v)", rep.State, ended, panicked, err)
	}
	start, stop, stops := traceStartStop(g.orders, rep.RunID, o.RunMeta)
	if start < 0 || stops != 1 || stop < start {
		t.Fatalf("trace start %d, stop %d, %d stops: %v", start, stop, stops, g.orders)
	}
	if !stopBeforeUnbind(g.orders, stop) {
		t.Fatalf("trace stop %d did not go out of the deferred cleanup (unbind %d): %v", stop, slices.Index(g.orders, "commander unbind"), g.orders)
	}
}

// shortTraceStop shortens the server trace stop's bounds for the test.
func shortTraceStop(t *testing.T) {
	t.Helper()
	w, try, gap := serverTraceStopWait, serverTraceStopTry, serverTraceStopGap
	serverTraceStopWait, serverTraceStopTry, serverTraceStopGap = 300*time.Millisecond, 80*time.Millisecond, 5*time.Millisecond
	t.Cleanup(func() { serverTraceStopWait, serverTraceStopTry, serverTraceStopGap = w, try, gap })
}

// metaRun runs a remote run with run_meta on a fake whose trace recorder answers the scripted way (fakeRemote.traceAns);
// panicOn, when set, panics the run at that order.
func metaRun(t *testing.T, ans map[string][]string, panicOn string) (*fakeRemote, Report) {
	t.Helper()
	plan := remotePlan(Leg{Name: "first", To: Point{100, 0, 0}, PullGroups: []PullGroup{{Entries: []uint32{299}, Positions: []Point{{150, 5, 0}}}}},
		Leg{Name: "second", To: Point{200, 0, 0}})
	o := opts()
	o.RunMeta = RunMeta{Model: "haiku", Effort: "max", Run: "zf-ab-2-s5", ServerTrace: true}
	g := newFakeRemote(t)
	g.addMob(299, Point{150, 5, 0})
	g.traceAns = ans
	g.panicOn = panicOn
	d := NewRemoteDriver(plan, g, DefaultConfig(), o, g.clock())
	if _, panicked := runPanicking(t, d); panicked {
		t.Fatalf("a panic escaped Run")
	}
	rep, ended := d.Status()
	if !ended {
		t.Fatalf("run did not end")
	}
	return g, rep
}

func countOrders(orders []string, cmd string) int {
	n := 0
	for _, o := range orders {
		if o == cmd {
			n++
		}
	}
	return n
}

// The run owns the server trace until a stop is answered: a stop that failed (a transport error, no answer) is tried
// again, after a `trace status` shows the trace running is still this run's own, and exactly one stop succeeds. A stop
// the server applied but whose answer was lost is not sent again: the status shows the trace has ended.
func TestRemoteRunMetaRetriesFailedTraceStop(t *testing.T) {
	shortTraceStop(t)
	for _, tc := range []struct {
		name          string
		stop          []string
		panicOn       string
		stops, status int
	}{
		{"error then ack", []string{"error"}, "", 2, 1},
		{"timeout then ack", []string{"timeout"}, "", 2, 1},
		{"applied, answer lost", []string{"lost"}, "", 1, 1},
		{"non-terminal refusal then ack", []string{"busy"}, "", 2, 1},
		{"panic, error then ack", []string{"error"}, "cmd move", 2, 1},
		{"panic, timeout twice then ack", []string{"timeout", "timeout"}, "cmd move", 3, 2},
	} {
		t.Run(tc.name, func(t *testing.T) {
			g, rep := metaRun(t, map[string][]string{"stop": tc.stop}, tc.panicOn)
			if g.traceRun != "" {
				t.Fatalf("the server trace %q is still running: %v", g.traceRun, g.orders)
			}
			stops, status := countOrders(g.orders, "trace stop"), countOrders(g.orders, "trace status")
			if stops != tc.stops || status != tc.status {
				t.Fatalf("%d stops, %d status (want %d, %d): %v", stops, status, tc.stops, tc.status, g.orders)
			}
			if tc.panicOn != "" {
				_, last, _ := traceStartStop(g.orders, rep.RunID, RunMeta{Model: "haiku", Effort: "max", Run: "zf-ab-2-s5", ServerTrace: true})
				if !stopBeforeUnbind(g.orders, last) {
					t.Fatalf("the acked stop did not go out of the deferred cleanup: %v", g.orders)
				}
			}
			for _, n := range rep.Notes {
				if strings.Contains(n, "may still be running") {
					t.Fatalf("give-up note after an acked stop: %q", rep.Notes)
				}
			}
		})
	}
}

// A refused stop is the server's answer (trying again changes nothing): it is noted and not sent again.
func TestRemoteRunMetaRefusedTraceStopNotRetried(t *testing.T) {
	shortTraceStop(t)
	g, rep := metaRun(t, map[string][]string{"stop": {"forbidden"}}, "")
	if n := countOrders(g.orders, "trace stop"); n != 1 {
		t.Fatalf("%d stops: %v", n, g.orders)
	}
	if !slices.Contains(rep.Notes, "trace stop refused: forbidden") {
		t.Fatalf("no refusal note in %q", rep.Notes)
	}
}

// A stop that never gets an answer is tried a bounded number of times by the deferred stop and again by finish, and the
// report says the server trace may still be running.
func TestRemoteRunMetaTraceStopGivesUp(t *testing.T) {
	shortTraceStop(t)
	all := make([]string, 2*serverTraceStopTries)
	for i := range all {
		all[i] = "timeout"
	}
	g, rep := metaRun(t, map[string][]string{"stop": all}, "")
	if n := countOrders(g.orders, "trace stop"); n != 2*serverTraceStopTries {
		t.Fatalf("%d stops: %v", n, g.orders)
	}
	found := false
	for _, n := range rep.Notes {
		found = found || strings.Contains(n, "the server trace may still be running")
	}
	if !found {
		t.Fatalf("no give-up note in %q", rep.Notes)
	}
}

// The deferred stop never holds the panic's unwind for longer than serverTraceStopWait, however long the server takes.
func TestDeferredTraceStopIsBounded(t *testing.T) {
	shortTraceStop(t)
	serverTraceStopWait, serverTraceStopTry = 150*time.Millisecond, time.Second // only the whole bound can cut the tries short
	g := newFakeRemote(t)
	g.traceAns = map[string][]string{"stop": {"hang", "hang", "hang"}, "status": {"hang", "hang"}}
	d := NewRemoteDriver(remotePlan(Leg{Name: "first", To: Point{100, 0, 0}}), g, DefaultConfig(), opts(), g.clock())
	d.serverTrace = true
	at := time.Now()
	d.stopServerTraceDeferred()
	if took := time.Since(at); took > serverTraceStopWait+150*time.Millisecond {
		t.Fatalf("the deferred stop took %v (bound %v)", took, serverTraceStopWait)
	}
	if !d.serverTrace {
		t.Fatalf("the run gave up the trace without an answered stop")
	}
}

// A trace start with no answer may have been applied: the run sends no stop for it (a blind stop could end a trace
// someone started by hand: the server's stop takes no run id), and the report says the server trace state is unknown.
func TestRemoteRunMetaUnansweredStartSendsNoStop(t *testing.T) {
	shortTraceStop(t)
	for _, ans := range []string{"cancel", "lost"} {
		t.Run(ans, func(t *testing.T) {
			g, rep := metaRun(t, map[string][]string{"start": {ans}}, "")
			if n := countOrders(g.orders, "trace stop"); n != 0 {
				t.Fatalf("%d stops after an unacknowledged start: %v", n, g.orders)
			}
			if !slices.Contains(rep.Notes, serverTraceUnknownNote) {
				t.Fatalf("no unknown-state note in %q", rep.Notes)
			}
		})
	}
}

// Only the server's terminal stop refusals (not-running, forbidden) end the run's ownership: any other refusal keeps it,
// is tried again, and when every try is refused the report says the trace may still be running.
func TestRemoteRunMetaNonTerminalStopErrorKeepsOwnership(t *testing.T) {
	shortTraceStop(t)
	all := make([]string, 2*serverTraceStopTries)
	for i := range all {
		all[i] = "internal"
	}
	g, rep := metaRun(t, map[string][]string{"stop": all}, "")
	if n := countOrders(g.orders, "trace stop"); n != 2*serverTraceStopTries {
		t.Fatalf("%d stops: %v", n, g.orders)
	}
	if g.traceRun == "" {
		t.Fatalf("the fake's trace ended: %v", g.orders)
	}
	found := false
	for _, n := range rep.Notes {
		found = found || strings.Contains(n, "trace stop refused: internal: the server trace may still be running")
	}
	if !found {
		t.Fatalf("no give-up note in %q", rep.Notes)
	}
}

// By default (no server_trace) a remote run, driven or observed, sends no trace command at all, whatever the run_meta:
// the meta still goes into the report.
func TestRunMetaWithoutServerTraceSendsNothing(t *testing.T) {
	meta := RunMeta{Model: "haiku", Effort: "max", Run: "zf-ab-2-s5"}
	g := newFakeRemote(t)
	g.addMob(299, Point{150, 5, 0})
	o := opts()
	o.RunMeta = meta
	plan := remotePlan(Leg{Name: "first", To: Point{100, 0, 0}, PullGroups: []PullGroup{{Entries: []uint32{299}, Positions: []Point{{150, 5, 0}}}}},
		Leg{Name: "second", To: Point{200, 0, 0}})
	rep, err := runRemote(t, g, plan, o, nil)
	if err != nil {
		t.Fatalf("%v %+v", err, rep)
	}
	gl := newFakeLeader(t, happyScript()...)
	ol := observeOpts(ObserveSpec{})
	ol.RunMeta = meta
	repl, err := runRemote(t, gl, observePlan(), ol, nil)
	if err != nil || repl.State != "done" {
		t.Fatalf("%v %s %s", err, repl.State, repl.Error)
	}
	for name, c := range map[string]struct {
		orders []string
		rep    Report
	}{"driven": {g.orders, rep}, "observed": {gl.orders, repl}} {
		for _, cmd := range c.orders {
			if strings.HasPrefix(cmd, "trace ") {
				t.Fatalf("%s: trace command without server_trace: %q", name, cmd)
			}
		}
		if c.rep.RunMeta == nil || *c.rep.RunMeta != meta {
			t.Fatalf("%s: report meta %+v", name, c.rep.RunMeta)
		}
	}
}
