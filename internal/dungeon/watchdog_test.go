package dungeon

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"
)

// wdRig is a watchdog on a clock the test moves, with the events and notes it produced.
type wdRig struct {
	t      *testing.T
	now    time.Time
	w      *watchdog
	events []map[string]any
	notes  []string
	pos    wdPos
	lead   Point
	ok     bool
}

func newWDRig(t *testing.T, cfg WatchdogConfig) *wdRig {
	r := &wdRig{t: t, now: time.Unix(2_000_000, 0)}
	r.w = newWatchdog(cfg, func() time.Time { return r.now }, wdHooks{
		look:  func() (wdPos, Point, bool) { return r.pos, r.lead, r.ok },
		event: func(name string, data map[string]any) { r.events = append(r.events, data) },
		note:  func(s string) { r.notes = append(r.notes, s) },
	})
	r.w.start()
	return r
}

func (r *wdRig) adv(s float64) { r.now = r.now.Add(time.Duration(s * float64(time.Second))) }

func (r *wdRig) watch() error { return r.w.watch(context.Background()) }

// levels are the stall levels the events so far reached, in order.
func (r *wdRig) levels() []string {
	var out []string
	for _, e := range r.events {
		out = append(out, e["level"].(string))
	}
	return out
}

func TestWatchdogConfigDefaultsMergeAndValidate(t *testing.T) {
	l := WatchdogConfig{}.limits()
	if l.warn != 120*time.Second || l.recover != 240*time.Second || l.fail != 420*time.Second {
		t.Fatalf("defaults %+v", l)
	}
	if l := (WatchdogConfig{WarnS: 30, FailS: -1}).limits(); l.warn != 30*time.Second || l.recover != 240*time.Second || l.fail != 0 {
		t.Fatalf("custom %+v", l)
	}
	m := WatchdogConfig{WarnS: 10, RecoverS: 20, FailS: 30}.Merge(WatchdogConfig{RecoverS: 25})
	if m != (WatchdogConfig{WarnS: 10, RecoverS: 25, FailS: 30}) {
		t.Fatalf("merge %+v", m)
	}
	for _, c := range []struct {
		c    WatchdogConfig
		want string
	}{
		{WatchdogConfig{}, ""},
		{WatchdogConfig{WarnS: 10, RecoverS: 20, FailS: 30}, ""},
		{WatchdogConfig{WarnS: -1, RecoverS: 20, FailS: 30}, ""},
		{WatchdogConfig{WarnS: -1, RecoverS: -1, FailS: -1}, ""},
		{WatchdogConfig{WarnS: 300}, "recover_s (240) must be above warn_s (300)"},
		{WatchdogConfig{FailS: 100}, "fail_s (100) must be above recover_s (240)"},
		{WatchdogConfig{WarnS: 50, RecoverS: 50, FailS: 60}, "recover_s (50) must be above warn_s (50)"},
		{WatchdogConfig{FailS: maxWatchdogS + 1}, "above"},
	} {
		err := c.c.Validate()
		if (c.want == "") != (err == nil) || (err != nil && !strings.Contains(err.Error(), c.want)) {
			t.Errorf("%+v: %v (want %q)", c.c, err, c.want)
		}
	}
}

func TestWatchdogPlanParameter(t *testing.T) {
	ok := `{"version":1,"name":"t","map":70,"entrance":{"x":0,"y":0,"z":0},"watchdog":{"warn_s":30,"recover_s":60,"fail_s":90},"legs":[{"to":{"x":0,"y":0,"z":0},"pull_groups":[{"entries":[1],"positions":[{"x":20,"y":0,"z":0}]}]}]}`
	p := mustPlan(t, ok)
	if p.Watchdog == nil || *p.Watchdog != (WatchdogConfig{WarnS: 30, RecoverS: 60, FailS: 90}) {
		t.Fatalf("%+v", p.Watchdog)
	}
	if got := watchdogFor(p, Config{Watchdog: WatchdogConfig{FailS: 100}}); got != (WatchdogConfig{WarnS: 30, RecoverS: 60, FailS: 100}) {
		t.Fatalf("the run parameter wins field by field: %+v", got)
	}
	if got := watchdogFor(mustPlan(t, restPlan), Config{}); got != (WatchdogConfig{}) {
		t.Fatalf("no plan setting: %+v", got)
	}
	_, err := Parse("t.json", []byte(strings.Replace(ok, `"fail_s":90`, `"fail_s":50`, 1)))
	if err == nil || !strings.Contains(err.Error(), "watchdog: ") || !strings.Contains(err.Error(), "fail_s (50) must be above recover_s (60)") {
		t.Fatalf("%v", err)
	}
	if _, err := Parse("t.json", []byte(strings.Replace(ok, `"warn_s":30`, `"warn":30`, 1))); err == nil {
		t.Fatal("an unknown watchdog field is refused")
	}
}

// Each level fires once per stall, in order; progress ends the stall and the next one starts over; the recovery is attempted once per
// stall; the failure names the phase, the step, the time and the last progress.
func TestWatchdogLevelsFireOncePerStall(t *testing.T) {
	r := newWDRig(t, WatchdogConfig{})
	recovered := 0
	leave := r.w.enter("walk", "leg 1 (x): walk", func(context.Context) (string, error) { recovered++; return "re-sent the walk", nil })
	r.w.ordered("cmd move 1 2 3 -> ack")

	r.adv(119)
	if err := r.watch(); err != nil || len(r.events) != 0 {
		t.Fatalf("%v %v", err, r.events)
	}
	r.adv(2) // 121 s
	for i := 0; i < 3; i++ {
		if err := r.watch(); err != nil {
			t.Fatal(err)
		}
	}
	if !slices.Equal(r.levels(), []string{"warn"}) {
		t.Fatalf("warn once: %v", r.levels())
	}
	if n := r.notes[0]; !strings.Contains(n, "no progress for 121 s") || !strings.Contains(n, "walk leg 1 (x): walk") || !strings.Contains(n, "last order cmd move 1 2 3 -> ack") {
		t.Fatalf("the warning names what the run waits on: %q", n)
	}
	if got := r.w.report(); got.StallLevel != "warn" || got.StalledForS != 121 || got.LastProgress != "run started" {
		t.Fatalf("%+v", got)
	}

	r.adv(120) // 241 s
	for i := 0; i < 3; i++ {
		if err := r.watch(); err != nil {
			t.Fatal(err)
		}
	}
	if recovered != 1 || !slices.Equal(r.levels(), []string{"warn", "recover"}) || r.events[1]["recovery"] != "re-sent the walk" {
		t.Fatalf("recovered %d, %v, %v", recovered, r.levels(), r.events)
	}

	// Progress ends the stall: the levels fire again for the next one, and so does the recovery.
	r.w.progress("a kill")
	if got := r.w.report(); got.StallLevel != "none" || got.StalledForS != 0 || got.LastProgress != "a kill" {
		t.Fatalf("%+v", got)
	}
	r.events, r.notes = nil, nil
	r.adv(250)
	if err := r.watch(); err != nil {
		t.Fatal(err)
	}
	if recovered != 2 || !slices.Equal(r.levels(), []string{"warn", "recover"}) {
		t.Fatalf("a second stall: recovered %d, %v", recovered, r.levels())
	}

	r.adv(180) // 430 s without progress
	err := r.watch()
	if !errors.Is(err, errStalled) || err.Error() != "stalled: walk leg 1 (x): walk for 430 s, last progress a kill" {
		t.Fatalf("%v", err)
	}
	if !slices.Equal(r.levels(), []string{"warn", "recover", "fail"}) || recovered != 2 {
		t.Fatalf("%v %d", r.levels(), recovered)
	}
	if got := r.w.report(); got.StallLevel != "fail" || got.Warnings != 2 || got.Recoveries != 2 {
		t.Fatalf("%+v", got)
	}
	// The failure is sticky until the watchdog is stopped; progress after it does not clear the report.
	if err2 := r.watch(); err2 == nil || err2.Error() != err.Error() {
		t.Fatalf("%v", err2)
	}
	r.w.progress("late")
	if got := r.w.report(); got.StallLevel != "fail" {
		t.Fatalf("%+v", got)
	}
	r.w.stop()
	if err := r.watch(); err != nil {
		t.Fatalf("a stopped watchdog fails nothing: %v", err)
	}
	leave()
}

// A wait that is long in one go (the clock jumps past all three limits) fires the three levels in order, once each.
func TestWatchdogJumpFiresEveryLevelInOrder(t *testing.T) {
	r := newWDRig(t, WatchdogConfig{WarnS: 10, RecoverS: 20, FailS: 30})
	n := 0
	defer r.w.enter("rest gate", "", func(context.Context) (string, error) { n++; return "ended it", nil })()
	r.adv(100)
	err := r.watch()
	if !errors.Is(err, errStalled) || !slices.Equal(r.levels(), []string{"warn", "recover", "fail"}) || n != 1 {
		t.Fatalf("%v %v %d", err, r.levels(), n)
	}
}

// A level set to a negative number is off; a run with only a warning never fails.
func TestWatchdogLevelsCanBeTurnedOff(t *testing.T) {
	r := newWDRig(t, WatchdogConfig{RecoverS: -1, FailS: -1})
	r.adv(10_000)
	if err := r.watch(); err != nil || !slices.Equal(r.levels(), []string{"warn"}) {
		t.Fatalf("%v %v", err, r.levels())
	}
}

// A recovery that fails is noted and not tried again in the same stall; one that ends the run (a wipe) ends it.
func TestWatchdogRecoveryFailureIsNotedAndNotRepeated(t *testing.T) {
	r := newWDRig(t, WatchdogConfig{WarnS: 10, RecoverS: 20, FailS: 100})
	n := 0
	defer r.w.enter("pull", "x", func(context.Context) (string, error) { n++; return "", errors.New("no ack") })()
	r.adv(25)
	for i := 0; i < 3; i++ {
		if err := r.watch(); err != nil {
			t.Fatal(err)
		}
	}
	if n != 1 || !strings.Contains(strings.Join(r.notes, "|"), "failed: no ack") || r.events[1]["recovery_error"] != "no ack" {
		t.Fatalf("%d %v %v", n, r.notes, r.events)
	}

	r = newWDRig(t, WatchdogConfig{WarnS: 10, RecoverS: 20, FailS: 100})
	defer r.w.enter("pull", "x", func(context.Context) (string, error) { return "", errWipe })()
	r.adv(25)
	if err := r.watch(); !errors.Is(err, errWipe) {
		t.Fatalf("%v", err)
	}
}

// With no recovery of its own a phase falls back to the enclosing phase's; with none at all the stall is only noted.
func TestWatchdogRecoveryFallsOutwardAndSilentFeedFirst(t *testing.T) {
	r := newWDRig(t, WatchdogConfig{WarnS: 10, RecoverS: 20, FailS: 100})
	outer := 0
	defer r.w.enter("object step", "leg 0: use", func(context.Context) (string, error) { outer++; return "outer", nil })()
	defer r.w.enter("walk", "", nil)()
	r.adv(25)
	if err := r.watch(); err != nil || outer != 1 {
		t.Fatalf("%v %d", err, outer)
	}

	r = newWDRig(t, WatchdogConfig{WarnS: 10, RecoverS: 20, FailS: 100})
	r.w.hooks.silent = func(context.Context) (string, bool, error) { return "view on again", true, nil }
	inner := 0
	defer r.w.enter("walk", "", func(context.Context) (string, error) { inner++; return "inner", nil })()
	r.adv(25)
	if err := r.watch(); err != nil || inner != 0 || r.events[1]["recovery"] != "view on again" {
		t.Fatalf("a silent feed is recovered by the feed's own recovery: %v %d %v", err, inner, r.events)
	}

	r = newWDRig(t, WatchdogConfig{WarnS: 10, RecoverS: 20, FailS: 100})
	r.adv(25)
	if err := r.watch(); err != nil || !strings.Contains(r.events[1]["recovery"].(string), "no recovery for the run phase") {
		t.Fatalf("%v %v", err, r.events)
	}
}

// The step advancing, the lead moving more than 5 yd, an ACKed order that is new, a kill and a fight that wears its mobs down are
// progress; the same order ACKed again, the lead jittering and a fight that stands still are not.
func TestWatchdogProgressKinds(t *testing.T) {
	r := newWDRig(t, WatchdogConfig{})
	r.ok, r.pos, r.lead = true, wdPos{0, "walk", 0}, Point{0, 0, 0}
	r.watch() // takes the baseline
	step := func(s float64) { r.adv(s); r.watch() }

	step(100)
	r.pos.phase = "pull_groups"
	step(10)
	if got := r.w.report(); got.LastProgress != "step advanced to leg 0 pull_groups 0" || got.StalledForS != 0 {
		t.Fatalf("%+v", got)
	}
	step(100)
	r.lead = Point{3, 3, 0} // 4.2 yd: jitter
	step(10)
	if got := r.w.report(); got.StalledForS != 110 {
		t.Fatalf("a short move is no progress: %+v", got)
	}
	r.lead = Point{6, 6, 0}
	step(1)
	if got := r.w.report(); got.LastProgress != "the lead moved" || got.StalledForS != 0 {
		t.Fatalf("%+v", got)
	}

	r.adv(60)
	r.w.acked("cmd move 1 2 3")
	if got := r.w.report(); got.LastProgress != "order acked: cmd move 1 2 3" || got.StalledForS != 0 {
		t.Fatalf("%+v", got)
	}
	r.adv(60)
	r.w.acked("cmd move 1 2 3")
	if got := r.w.report(); got.StalledForS != 60 {
		t.Fatalf("the same order again is no progress: %+v", got)
	}
	r.w.acked("cmd move 4 5 6")
	if got := r.w.report(); got.StalledForS != 0 {
		t.Fatalf("%+v", got)
	}
	r.adv(40)
	r.w.acked("cmd move 1 2 3") // alternating orders are the same few orders again
	r.w.acked("cmd move 4 5 6")
	if got := r.w.report(); got.StalledForS != 40 {
		t.Fatalf("alternating orders are no progress: %+v", got)
	}

	// A fight: the first look takes the state; a kill and less health are progress, the same health is not.
	r.w.fightBegin()
	r.w.fightSignal(0, 200)
	r.adv(50)
	r.w.fightSignal(0, 200)
	if got := r.w.report(); got.StalledForS != 90 {
		t.Fatalf("%+v", got)
	}
	r.w.fightSignal(0, 190)
	if got := r.w.report(); got.LastProgress != "the fought mobs lost health" || got.StalledForS != 0 {
		t.Fatalf("%+v", got)
	}
	r.adv(50)
	r.w.fightSignal(1, 190)
	if got := r.w.report(); got.LastProgress != "kill" || got.StalledForS != 0 {
		t.Fatalf("%+v", got)
	}
	r.adv(50)
	r.w.fightSignal(1, 290) // a mob joined: more health in the fight is no progress
	if got := r.w.report(); got.StalledForS != 50 {
		t.Fatalf("%+v", got)
	}
}

// A wait with a limit of its own (a death's recovery, a plan wait) is not a stall; the clock starts over when it ends.
func TestWatchdogHoldPausesTheClock(t *testing.T) {
	r := newWDRig(t, WatchdogConfig{})
	release := r.w.hold()
	r.adv(1000)
	if err := r.watch(); err != nil || len(r.events) != 0 || r.w.report().StalledForS != 0 {
		t.Fatalf("%v %v", err, r.events)
	}
	release()
	release() // twice is once
	r.adv(10)
	if got := r.w.report(); got.StalledForS != 10 {
		t.Fatalf("%+v", got)
	}
	r.adv(111)
	r.watch()
	if !slices.Equal(r.levels(), []string{"warn"}) {
		t.Fatalf("%v", r.levels())
	}
}

// A nil watchdog (a driver built by hand) is inert.
func TestWatchdogNilIsInert(t *testing.T) {
	var w *watchdog
	w.start()
	w.progress("x")
	w.acked("x")
	w.ordered("x")
	w.fightBegin()
	w.fightSignal(1, 1)
	w.hold()()
	w.enter("a", "b", nil)()
	w.stop()
	if w.watch(context.Background()) != nil || w.report() != nil {
		t.Fatal("a nil watchdog does something")
	}
}

// After the recovery has fired, what a stuck run can fake by repeating itself (a pull started, an order ACKed, a gate passed) is not
// progress: only something real is, and then the stall is over.
func TestWatchdogSoftProgressDoesNotCountAfterTheRecovery(t *testing.T) {
	r := newWDRig(t, WatchdogConfig{WarnS: 10, RecoverS: 20, FailS: 60})
	defer r.w.enter("pull", "boss", func(context.Context) (string, error) { return "followed", nil })()
	r.adv(15)
	r.w.softProgress("pull started: a") // before the recovery it is progress
	if got := r.w.report(); got.StalledForS != 0 {
		t.Fatalf("%+v", got)
	}
	r.adv(25)
	r.watch() // recover fires
	for i := 0; i < 3; i++ {
		r.adv(10)
		r.w.softProgress("pull started: boss")
		r.w.acked("cmd pullback " + string(rune('a'+i)))
		r.w.softProgress("rest gate finished")
	}
	if got := r.w.report(); got.StalledForS != 55 || got.StallLevel != "recover" {
		t.Fatalf("soft progress after the recovery: %+v", got)
	}
	r.adv(35)
	if err := r.watch(); !errors.Is(err, errStalled) {
		t.Fatalf("fail_s is a ceiling: %v", err)
	}

	r = newWDRig(t, WatchdogConfig{WarnS: 10, RecoverS: 20, FailS: 60})
	r.adv(25)
	r.watch()
	r.w.progress("a kill") // real progress ends the degraded state
	r.adv(5)
	r.w.softProgress("pull started: x")
	if got := r.w.report(); got.StalledForS != 0 || got.LastProgress != "pull started: x" {
		t.Fatalf("%+v", got)
	}
}

// Time held (a rest gate with limits of its own) is not counted, but the stall before it is: holding does not start the clock over.
func TestWatchdogHoldKeepsTheStallBeforeIt(t *testing.T) {
	r := newWDRig(t, WatchdogConfig{})
	r.adv(100)
	release := r.w.hold()
	r.adv(500)
	release()
	if got := r.w.report(); got.StalledForS != 100 {
		t.Fatalf("%+v", got)
	}
}
