package brain

import (
	"context"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/pjerra/turtle-agent/internal/objects"
	"github.com/pjerra/turtle-agent/internal/packet"
)

// Follow-ups of the move lock wiring (#80): the review leftovers of #91.

// ---- a: an emergency during a slow cancel is retried, not held back ----

func TestEmergencyEscapeDuringASlowCancelIsRetriedNextTick(t *testing.T) {
	r, pf := newPosRig(t, Params{})
	pf.stubborn = make(chan struct{}) // the walker answers no cancel until the test lets it
	tankFights(r.g, 8, 12, math.Pi)
	if d := r.tick(100 * time.Millisecond); d == nil || d.Action != "reposition" || d.Result != ResultOK {
		t.Fatalf("%+v idle %q", d, r.idle())
	}
	if !r.b.lock.stepping() {
		t.Fatal("no step under way")
	}
	// Fire under the player: leaving it cancels the step, which does not stop within the wait.
	pf.areas = []objects.DynamicObject{{GUID: 0xF100000000000001, Spell: spRainOfFire, Caster: mobA, Radius: 8, X: 0, Y: 0}}
	d := r.tick(100 * time.Millisecond)
	if d == nil || d.Action != "leave_ground_aoe" || d.Result != ResultError {
		t.Fatalf("%+v idle %q", d, r.idle())
	}
	if !strings.Contains(d.Error, "still stopping") || strings.Contains(d.Error, "driver") {
		t.Fatalf("the message blames the wrong party: %q", d.Error)
	}
	if why, held := r.b.mem.actionHeld("leave_ground_aoe", r.g.clk.Now().Add(time.Millisecond)); held {
		t.Fatalf("held back (%s): the retry would wait a second", why)
	}
	// The walker answers: the very next tick escapes.
	close(pf.stubborn)
	r.settleStep(time.Second)
	at := here(r.g) // the cancelled step still arrived at its spot: the fire is wherever the player stands now
	pf.areas[0].X, pf.areas[0].Y = at[0], at[1]
	d = r.tick(100 * time.Millisecond)
	if d == nil || d.Action != "leave_ground_aoe" || d.Result != ResultOK {
		t.Fatalf("no retry: %+v idle %q", d, r.idle())
	}
}

func TestWalkBlamesTheDriverOnlyWhenTheDriverHoldsTheLock(t *testing.T) {
	r, pf := newPosRig(t, Params{})
	pf.hold = make(chan struct{})
	tankFights(r.g, 8, 12, math.Pi)
	c := r.b.newCtx(r.g.clk.Now())
	if err := c.walk(context.Background(), Vec3{1, 2, 3}); err != nil {
		t.Fatal(err)
	}
	if err := c.walk(context.Background(), Vec3{1, 2, 3}); err != errStepStopping { // a step is registered, no driver
		t.Fatalf("%v", err)
	}
	rel, _ := r.b.lock.Hold(context.Background())
	defer rel()
	if err := c.walk(context.Background(), Vec3{1, 2, 3}); err != errMoveLocked {
		t.Fatalf("%v", err)
	}
}

// ---- b: Stop waits for a step that is under way ----

func TestStopWaitsForTheStepUnderWayAndARestartMeetsAFreeLock(t *testing.T) {
	r, pf := newPosRig(t, Params{})
	pf.stubborn = make(chan struct{})
	tankFights(r.g, 8, 12, math.Pi)
	m := newManual()
	cfg := DefaultConfig()
	cfg.Now = r.g.clk.Now
	cfg.NewTicker = m.ticker
	ctl := NewController(context.Background(), pf, cfg)
	defer ctl.Close()
	if _, err := ctl.Start(Params{Strategies: []string{"frost_mage"}}); err != nil {
		t.Fatal(err)
	}
	r.g.clk.advance(100 * time.Millisecond)
	m.feed(t, 1)
	waitFor(t, "a step", func() bool { return ctl.lock.stepping() })
	stopped := make(chan struct{})
	go func() { ctl.Stop(); close(stopped) }()
	select {
	case <-stopped:
		t.Fatal("Stop returned with a step still under way")
	case <-time.After(150 * time.Millisecond):
	}
	close(pf.stubborn)
	select {
	case <-stopped:
	case <-time.After(3 * time.Second):
		t.Fatal("Stop never returned")
	}
	if ctl.lock.stepping() {
		t.Fatal("the step outlived Stop")
	}
	// A quick restart finds the lock free at once.
	if _, done, ok := ctl.lock.begin(context.Background()); !ok {
		t.Fatal("the lock was still taken after Stop")
	} else {
		done()
	}
	if _, err := ctl.Start(Params{Strategies: []string{"frost_mage"}}); err != nil {
		t.Fatal(err)
	}
}

func TestStopGivesUpOnAStepThatNeverStops(t *testing.T) {
	l := NewMoveLock()
	_, done, _ := l.begin(context.Background())
	defer done()
	start := time.Now()
	if l.settle(50 * time.Millisecond) {
		t.Fatal("settled with a step under way")
	}
	if time.Since(start) > time.Second {
		t.Fatal("the wait is not bounded")
	}
	var nilLock *MoveLock
	if !nilLock.settle(time.Millisecond) || !NewMoveLock().settle(time.Millisecond) {
		t.Fatal("an idle lock must settle at once")
	}
}

// ---- c: a line_of_sight refusal is a wall to the fight's mob only when the last cast aimed at it ----

func TestLineOfSightRefusalOfACastAtAnotherTargetMarksNoWall(t *testing.T) {
	r, _ := newPosRig(t, Params{})
	tankFights(r.g, 22, 24, 0)
	r.tick(100 * time.Millisecond) // the fight's mob is mobA
	los := codeOf(t, "line_of_sight")
	for _, c := range []struct {
		name   string
		target packet.GUID
		marked bool
	}{
		{"a heal on a party member", tankG, false},
		{"a cast on the caster", selfG, false},
		{"a cast at the fight's mob", mobA, true},
	} {
		r.g.clk.advance(10 * time.Second) // earlier marks are over
		r.b.mem.mu.Lock()
		r.b.mem.lastCastTarget = c.target
		r.b.mem.mu.Unlock()
		r.g.st.CastRefused(spFrostbolt, los)
		ctx := r.b.newCtx(r.g.clk.Now())
		r.b.observeFailures(ctx)
		mob, _ := ctx.Unit(mobA)
		if got := losBlocked(ctx, mob); got != c.marked {
			t.Fatalf("%s: marked %v, want %v", c.name, got, c.marked)
		}
	}
}

func TestACastRemembersItsTarget(t *testing.T) {
	r, _ := newPosRig(t, Params{})
	tankFights(r.g, 22, 24, 0)
	r.tick(100 * time.Millisecond)
	if d := r.tick(2200 * time.Millisecond); d == nil || d.Action != "frostbolt" {
		t.Fatalf("%+v", d)
	}
	r.b.mem.mu.Lock()
	got := r.b.mem.lastCastTarget
	r.b.mem.mu.Unlock()
	if got != mobA {
		t.Fatalf("the last cast aimed at %v, want the mob %v", got, mobA)
	}
}

// Every cast of the brain must go through Ctx.cast, or the line-of-sight refusal cannot tell whom it was for.
func TestEveryCastGoesThroughCtxCast(t *testing.T) {
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") {
			continue
		}
		b, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		for i, line := range strings.Split(string(b), "\n") {
			if strings.Contains(line, ".G.Cast(") && !strings.Contains(line, "return c.G.Cast(ctx, spell, target)") {
				t.Errorf("%s:%d sends a cast around Ctx.cast: %s", f, i+1, strings.TrimSpace(line))
			}
		}
	}
}

// ---- d: a Ctx without a tick context keeps its step alive past the action ----

func TestStepOfAHandBuiltCtxOutlivesTheActionsContext(t *testing.T) {
	r, pf := newPosRig(t, Params{})
	pf.hold = make(chan struct{})
	tankFights(r.g, 8, 12, math.Pi)
	c := r.b.newCtx(r.g.clk.Now()) // no tick: c.run is nil
	if c.run != nil {
		t.Fatal("a Ctx not made by a tick has a run context")
	}
	actCtx, cancel := context.WithCancel(context.Background())
	if err := c.walk(actCtx, Vec3{4, 5, 6}); err != nil {
		t.Fatal(err)
	}
	cancel() // the action returned
	time.Sleep(30 * time.Millisecond)
	if !r.b.lock.stepping() {
		t.Fatal("the step ended with the action that started it")
	}
	close(pf.hold)
	waitFor(t, "the walk to land", func() bool { return len(pf.walkList()) == 1 })
}
