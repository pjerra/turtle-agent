package brain

import (
	"context"
	"runtime"
	"sync"
	"testing"
	"time"

	"github.com/pjerra/turtle-agent/internal/events"
)

func TestTheDefaultTickIs200ms(t *testing.T) {
	if DefaultConfig().Every != 200*time.Millisecond {
		t.Fatal(DefaultConfig().Every)
	}
}

// manual is a ticker the test feeds by hand; it records the period it was asked for.
type manual struct {
	mu      sync.Mutex
	c       chan time.Time
	period  time.Duration
	stopped int
}

func newManual() *manual { return &manual{c: make(chan time.Time)} }

func (m *manual) ticker(d time.Duration) (<-chan time.Time, func()) {
	m.mu.Lock()
	m.period = d
	m.mu.Unlock()
	return m.c, func() { m.mu.Lock(); m.stopped++; m.mu.Unlock() }
}

func (m *manual) feed(t *testing.T, n int) {
	t.Helper()
	for i := 0; i < n; i++ {
		select {
		case m.c <- time.Time{}:
		case <-time.After(2 * time.Second):
			t.Fatal("the brain does not take ticks")
		}
	}
}

func waitFor(t *testing.T, what string, ok func() bool) {
	t.Helper()
	for i := 0; i < 400; i++ {
		if ok() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

func ctlOn(t *testing.T, g *fakeGame, m *manual) *Controller {
	t.Helper()
	cfg := DefaultConfig()
	cfg.Now = g.clk.Now
	if m != nil {
		cfg.NewTicker = m.ticker
	}
	ctl := NewController(context.Background(), g, cfg)
	t.Cleanup(func() { ctl.Close() })
	return ctl
}

func TestTheControllerTicksOncePerTickerTick(t *testing.T) {
	g, m := newFake(spFrostbolt), newManual()
	ctl := ctlOn(t, g, m)
	st, err := ctl.Start(Params{Strategies: []string{"frost_mage"}})
	if err != nil || !st.Running {
		t.Fatalf("%+v %v", st, err)
	}
	m.feed(t, 5) // the ticker exists once the loop takes its first tick
	m.mu.Lock()
	period := m.period
	m.mu.Unlock()
	if period != 200*time.Millisecond {
		t.Fatalf("period %v", period)
	}
	waitFor(t, "5 ticks", func() bool { return ctl.Status().Ticks == 5 })
	time.Sleep(50 * time.Millisecond)
	if n := ctl.Status().Ticks; n != 5 {
		t.Fatalf("ticks without ticker ticks: %d", n)
	}
}

func TestStartStopStatusLifecycle(t *testing.T) {
	g, m := newFake(spFrostbolt), newManual()
	ctl := ctlOn(t, g, m)
	if st := ctl.Status(); st.Running || len(st.Strategies) != 0 || st.Last == nil || st.Holds == nil {
		t.Fatalf("an idle controller: %+v", st)
	}
	if st := ctl.Stop(); st.Running { // stopping nothing is fine
		t.Fatalf("%+v", st)
	}
	if _, err := ctl.Start(Params{Strategies: []string{"nope"}}); err == nil || ctl.Running() {
		t.Fatalf("a bad start must leave the controller idle: %v", err)
	}
	if _, err := ctl.Start(Params{Strategies: []string{"frost_mage"}}); err != nil {
		t.Fatal(err)
	}
	if _, err := ctl.Start(Params{Strategies: []string{"frost_mage"}}); err != ErrRunning {
		t.Fatalf("a second start: %v", err)
	}
	st := ctl.Stop()
	if st.Running || len(st.Strategies) != 4 {
		t.Fatalf("the stopped brain still reports what it ran: %+v", st)
	}
	m.mu.Lock()
	stopped := m.stopped
	m.mu.Unlock()
	if stopped != 1 {
		t.Fatalf("the ticker must be stopped: %d", stopped)
	}
	states := g.events(events.KindBrainState)
	if len(states) != 2 || states[0].Data["state"] != "started" || states[1].Data["state"] != "stopped" {
		t.Fatalf("%+v", states)
	}
	if _, err := ctl.Start(Params{Strategies: []string{"frost_mage"}}); err != nil { // and it can start again
		t.Fatal(err)
	}
}

func TestNoGoroutineIsLeftBehind(t *testing.T) {
	g := newFake(spFrostbolt)
	before := runtime.NumGoroutine()
	for i := 0; i < 5; i++ {
		cfg := DefaultConfig()
		cfg.Every = 2 * time.Millisecond // the real ticker
		ctl := NewController(context.Background(), g, cfg)
		if _, err := ctl.Start(Params{Strategies: []string{"frost_mage"}}); err != nil {
			t.Fatal(err)
		}
		waitFor(t, "ticks", func() bool { return ctl.Status().Ticks >= 2 })
		ctl.Stop()
	}
	waitFor(t, "goroutines to end", func() bool { return runtime.NumGoroutine() <= before })
}

func TestTheRootContextEndsTheBrain(t *testing.T) {
	g := newFake(spFrostbolt)
	root, cancel := context.WithCancel(context.Background())
	cfg := DefaultConfig()
	cfg.Every = 2 * time.Millisecond
	ctl := NewController(root, g, cfg)
	if _, err := ctl.Start(Params{Strategies: []string{"frost_mage"}}); err != nil {
		t.Fatal(err)
	}
	cancel()
	waitFor(t, "the brain to end", func() bool { return !ctl.Running() })
	if n := len(g.events(events.KindBrainState)); n != 2 {
		t.Fatalf("%d state events", n)
	}
}

func TestAStuckGameCallDoesNotBlockStop(t *testing.T) {
	g := newFake(spFrostbolt, spFireBlast)
	buffUp(g)
	g.block = make(chan struct{}) // a Cast that never returns until its context ends
	g.addUnit(mob(mobA, selfG), 3, 0)
	cfg := DefaultConfig()
	cfg.Every = 2 * time.Millisecond
	cfg.DoTimeout = time.Minute
	ctl := NewController(context.Background(), g, cfg)
	if _, err := ctl.Start(Params{Strategies: []string{"frost_mage"}}); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "a tick", func() bool { return ctl.Status().Ticks >= 1 })
	time.Sleep(30 * time.Millisecond) // the first tick is now inside Cast
	done := make(chan struct{})
	go func() { ctl.Stop(); close(done) }()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("Stop is stuck behind a game call")
	}
	if ctl.Running() {
		t.Fatal("still running")
	}
}

func TestStatusIsSafeWhileTheBrainRuns(t *testing.T) {
	g := newFake(spFrostbolt, spFireBlast)
	buffUp(g)
	g.addUnit(mob(mobA, selfG), 3, 0)
	cfg := DefaultConfig()
	cfg.Every = time.Millisecond
	ctl := NewController(context.Background(), g, cfg)
	if _, err := ctl.Start(Params{Strategies: []string{"frost_mage"}}); err != nil {
		t.Fatal(err)
	}
	defer ctl.Stop()
	for i := 0; i < 200; i++ {
		ctl.Status()
		ctl.Rest(i%2 == 0)
		time.Sleep(time.Millisecond)
	}
}
