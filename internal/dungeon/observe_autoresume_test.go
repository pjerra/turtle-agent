package dungeon

import (
	"strings"
	"testing"
	"time"

	"github.com/pjerra/turtle-agent/internal/tbm"
)

const didNotHoldNote = "3 dead: regroup (cmd revive party), then .bot run resume (2 regroups by itself at this step did not hold)"

// autoResumeScript pauses at step 2 with the did-not-hold note, then goes on
// to done once resumed.
func autoResumeScript() []leaderStep {
	return []leaderStep{
		{0, rs("running", 1, 5, "first", "move", "walk", "walking to -1 2 3")},
		{s(5), rs("paused", 2, 5, "first", "pull", "gather", didNotHoldNote)},
		{s(30), rs("running", 3, 5, "first", "pull", "pull", "pulling 299")},
		{s(40), rs("done", 5, 5, "second", "move", "walk", "plan done")},
	}
}

// holdPauses keeps the fake leader in every pause until a resume order comes.
func holdPauses(g *fakeLeader) {
	g.onStatus = func(l *fakeLeader, st tbm.RunStatus) {
		if st.State == tbm.RunPaused {
			l.paused = true
		}
	}
}

func TestObserveAutoResumesTheDidNotHoldPause(t *testing.T) {
	g := newFakeLeader(t, autoResumeScript()...)
	holdPauses(g)
	var pausedAt time.Time
	tick := g.onTick
	g.onTick = func(gr *fakeRemote) {
		if g.haveCur && g.cur.State == tbm.RunPaused && pausedAt.IsZero() {
			pausedAt = g.now
		}
		tick(gr)
	}
	rep, err := runRemote(t, g, observePlan(), observeOpts(ObserveSpec{PausedWait: s(60), AutoResume: 1}), nil)
	if err != nil || rep.State != "done" {
		t.Fatalf("%v %s %s", err, rep.State, rep.Error)
	}
	if pausedAt.IsZero() {
		t.Fatal("the pause never came")
	}
	if n := len(g.ordersOf("run resume")); n != 1 {
		t.Fatalf("run resume sent %d times: %v", n, g.orders)
	}
	if d := g.orderAt[g.indexOf("run resume")].Sub(pausedAt); d < 13*time.Second || d > 30*time.Second {
		t.Fatalf("resumed after %s, want on the first look at least 15 s after the pause", d)
	}
	if rep.Remote.Observe.Ended != "done" {
		t.Fatalf("ended %q", rep.Remote.Observe.Ended)
	}
	var auto, ack bool
	for _, c := range rep.Remote.Observe.Controls {
		auto = auto || c == "auto resume 1 of 1 at step 2/5: "+didNotHoldNote
		ack = ack || c == "resume: ack"
	}
	if !auto || !ack {
		t.Fatalf("controls %v", rep.Remote.Observe.Controls)
	}
	for _, n := range rep.Notes {
		if n == "observe mode: auto resume: auto resume 1 of 1 at step 2/5: "+didNotHoldNote {
			return
		}
	}
	t.Fatalf("no auto resume note in %v", rep.Notes)
}

func TestObserveAutoResumeIsOffByDefault(t *testing.T) {
	g := newFakeLeader(t, autoResumeScript()...)
	holdPauses(g)
	rep, err := runRemote(t, g, observePlan(), observeOpts(ObserveSpec{PausedWait: s(60)}), nil)
	if err == nil || !strings.Contains(err.Error(), "paused for 1m0s without a resume") || rep.State != "failed" {
		t.Fatalf("%v %s", err, rep.State)
	}
	if i := g.indexOf("run resume"); i >= 0 {
		t.Fatalf("resumed with auto_resume off: %v", g.orders)
	}
	for _, c := range rep.Remote.Observe.Controls {
		if strings.Contains(c, "auto resume") {
			t.Fatalf("controls %v", rep.Remote.Observe.Controls)
		}
	}
}

func TestObserveAutoResumeBudgetPerStep(t *testing.T) {
	g := newFakeLeader(t,
		leaderStep{0, rs("running", 1, 5, "first", "move", "walk", "walking to -1 2 3")},
		leaderStep{s(5), rs("paused", 2, 5, "first", "pull", "gather", didNotHoldNote)},
		leaderStep{s(30), rs("running", 3, 5, "first", "pull", "pull", "pulling 299")},
		leaderStep{s(35), rs("paused", 2, 5, "first", "pull", "gather", didNotHoldNote)})
	holdPauses(g)
	rep, err := runRemote(t, g, observePlan(), observeOpts(ObserveSpec{PausedWait: s(60), AutoResume: 1}), nil)
	if err == nil || !strings.Contains(err.Error(), "paused for 1m0s without a resume") || rep.State != "failed" {
		t.Fatalf("%v %s", err, rep.State)
	}
	if n := len(g.ordersOf("run resume")); n != 1 {
		t.Fatalf("run resume sent %d times, want 1: %v", n, g.orders)
	}
	n := 0
	for _, c := range rep.Remote.Observe.Controls {
		if strings.Contains(c, "auto resume") {
			n++
		}
	}
	if n != 1 {
		t.Fatalf("auto resume controls %d, want 1: %v", n, rep.Remote.Observe.Controls)
	}
}

func TestObserveAutoResumeLeavesOtherPausesAlone(t *testing.T) {
	for _, note := range []string{
		"player order (cmd summon Healy): .bot run resume to go on",
		"fight > 180 s: the fight is taking too long, paused; .bot run resume to go on",
	} {
		g := newFakeLeader(t,
			leaderStep{0, rs("running", 1, 5, "first", "move", "walk", "walking")},
			leaderStep{s(10), rs("paused", 2, 5, "first", "pull", "gather", note)})
		rep, err := runRemote(t, g, observePlan(), observeOpts(ObserveSpec{PausedWait: s(60), AutoResume: 1}), nil)
		if err == nil || !strings.Contains(err.Error(), "paused for 1m0s without a resume") {
			t.Fatalf("%q: %v %s", note, err, rep.State)
		}
		if i := g.indexOf("run resume"); i >= 0 {
			t.Fatalf("%q: auto resumed: %v", note, g.orders)
		}
		for _, c := range rep.Remote.Observe.Controls {
			if strings.Contains(c, "auto resume") {
				t.Fatalf("%q: controls %v", note, rep.Remote.Observe.Controls)
			}
		}
	}
}

// One pause is resumed once: a later status of the same pause (the module
// appends "; X meanwhile: ...") is not a new pause, so without the attempt
// mark the same pause would fire again on the next look. (The resume is
// refused — the fake leader was never paused — so the pause persists until
// PausedWait.)
func TestObserveAutoResumeSendsOneResumePerPause(t *testing.T) {
	g := newFakeLeader(t,
		leaderStep{0, rs("running", 1, 5, "first", "move", "walk", "walking to -1 2 3")},
		leaderStep{s(5), rs("paused", 2, 5, "first", "pull", "gather", didNotHoldNote)},
		leaderStep{s(40), rs("paused", 2, 5, "first", "pull", "gather", didNotHoldNote+"; boss meanwhile: waiting")})
	rep, err := runRemote(t, g, observePlan(), observeOpts(ObserveSpec{AutoResume: 2}), nil)
	if err == nil || !strings.Contains(err.Error(), "paused for 10m0s without a resume") || rep.State != "failed" {
		t.Fatalf("%v %s", err, rep.State)
	}
	if n := len(g.ordersOf("run resume")); n != 1 {
		t.Fatalf("run resume sent %d times, want 1: %v", n, g.orders)
	}
}

// A refused auto resume does not renew the pause: the run still ends at
// PausedWait, not MaxRun. (The fake leader was never paused, so it refuses
// the resume with "state".)
func TestObserveRefusedAutoResumeStillEndsAtPausedWait(t *testing.T) {
	g := newFakeLeader(t,
		leaderStep{0, rs("running", 1, 5, "first", "move", "walk", "walking to -1 2 3")},
		leaderStep{s(5), rs("paused", 2, 5, "first", "pull", "gather", didNotHoldNote)})
	rep, err := runRemote(t, g, observePlan(), observeOpts(ObserveSpec{PausedWait: s(60), MaxRun: s(600), AutoResume: 1}), nil)
	if err == nil || !strings.Contains(err.Error(), "paused for 1m0s without a resume") || rep.State != "failed" {
		t.Fatalf("%v %s", err, rep.State)
	}
	if n := len(g.ordersOf("run resume")); n != 1 {
		t.Fatalf("run resume sent %d times, want 1: %v", n, g.orders)
	}
}

// A new pause after a refused resume is resumed again: the first resume is
// refused (the fake leader was never paused, so no running status clears the
// latch), and the next status is another eligible pause with a different step
// and note. It arrives as paused -> paused — the observed sequence of the
// real miss, where the watcher keeps only the newest status — so the old
// latch must not block it: the latch re-arms, pausedAt restarts, and the new
// pause is auto-resumed (and refused again, so PausedWait ends it).
func TestObserveAutoResumeRearmsForANewPause(t *testing.T) {
	note2 := "1 dead: regroup (cmd revive party), then .bot run resume (1 regroups by itself at this step did not hold)"
	g := newFakeLeader(t,
		leaderStep{0, rs("running", 1, 5, "first", "move", "walk", "walking to -1 2 3")},
		leaderStep{s(5), rs("paused", 2, 5, "first", "pull", "gather", didNotHoldNote)},
		leaderStep{s(40), rs("paused", 3, 5, "first", "pull", "gather", note2)})
	var firstPausedAt time.Time
	tick := g.onTick
	g.onTick = func(gr *fakeRemote) {
		if g.haveCur && g.cur.State == tbm.RunPaused && firstPausedAt.IsZero() {
			firstPausedAt = g.now
		}
		tick(gr)
	}
	rep, err := runRemote(t, g, observePlan(), observeOpts(ObserveSpec{PausedWait: s(60), MaxRun: s(600), AutoResume: 1}), nil)
	if err == nil || !strings.Contains(err.Error(), "paused for 1m0s without a resume") || rep.State != "failed" {
		t.Fatalf("%v %s", err, rep.State)
	}
	var resumeAt []time.Time
	for i, o := range g.orders {
		if strings.HasPrefix(o, "run resume") {
			resumeAt = append(resumeAt, g.orderAt[i])
		}
	}
	if len(resumeAt) != 2 {
		t.Fatalf("run resume sent %d times, want 2: %v", len(resumeAt), g.orders)
	}
	if d := resumeAt[1].Sub(firstPausedAt); d < 45*time.Second {
		t.Fatalf("second resume after %s, want pausedAt to have restarted at the new pause", d)
	}
	if d := g.now.Sub(firstPausedAt); d < 90*time.Second {
		t.Fatalf("run ended %s after the first pause, want PausedWait from the new pause", d)
	}
}

// The same pause with the module's appended aside is not a new pause: after
// a refused resume, a paused -> paused status with the same step but more
// note text still ends at PausedWait with no second resume.
func TestObserveAutoResumeAppendedAsideIsNoNewPause(t *testing.T) {
	g := newFakeLeader(t,
		leaderStep{0, rs("running", 1, 5, "first", "move", "walk", "walking to -1 2 3")},
		leaderStep{s(5), rs("paused", 2, 5, "first", "pull", "gather", didNotHoldNote)},
		leaderStep{s(40), rs("paused", 2, 5, "first", "pull", "gather", didNotHoldNote+"; boss meanwhile: waiting")})
	rep, err := runRemote(t, g, observePlan(), observeOpts(ObserveSpec{PausedWait: s(60), MaxRun: s(600), AutoResume: 1}), nil)
	if err == nil || !strings.Contains(err.Error(), "paused for 1m0s without a resume") || rep.State != "failed" {
		t.Fatalf("%v %s", err, rep.State)
	}
	if n := len(g.ordersOf("run resume")); n != 1 {
		t.Fatalf("run resume sent %d times, want 1: %v", n, g.orders)
	}
}
