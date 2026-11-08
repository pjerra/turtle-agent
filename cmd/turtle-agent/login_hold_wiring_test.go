package main

import (
	"bytes"
	"context"
	"errors"
	"io"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/pjerra/turtle-agent/internal/auth"
	"github.com/pjerra/turtle-agent/internal/config"
)

// holdTrace pins the hold's clock at at (UTC) with the env on, records every hold sleep and every realmd login in order, and makes
// the realmd login fail so that nothing after it runs.
type holdTrace struct {
	mu    sync.Mutex
	steps []string
}

func (h *holdTrace) add(s string) { h.mu.Lock(); h.steps = append(h.steps, s); h.mu.Unlock() }

func (h *holdTrace) get() []string {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([]string{}, h.steps...)
}

func pinHold(t *testing.T, at time.Time) *holdTrace {
	t.Helper()
	t.Setenv(loginHourHoldEnv, "")
	h := &holdTrace{}
	oldNow, oldSleep, oldJitter, oldAuth := loginHoldNow, loginHoldSleep, loginHoldJitter, authLogin
	t.Cleanup(func() {
		loginHoldNow, loginHoldSleep, loginHoldJitter, authLogin = oldNow, oldSleep, oldJitter, oldAuth
	})
	loginHoldNow = func() time.Time { return at }
	loginHoldJitter = func() time.Duration { return 0 }
	loginHoldSleep = func(_ context.Context, d time.Duration) error { h.add("sleep " + d.String()); return nil }
	authLogin = func(context.Context, string, string, string) (*auth.Ticket, error) {
		h.add("auth")
		return nil, errors.New("stop here")
	}
	return h
}

// The login command holds out realmd's hour-start window before its handshake. Deleting the call from loginCmd fails this test.
func TestLoginCmdHoldsBeforeAuthLogin(t *testing.T) {
	h := pinHold(t, time.Date(2026, 11, 8, 14, 1, 0, 0, time.UTC))
	var out, errOut bytes.Buffer
	code := run(context.Background(), []string{"login", "--config", settingsFile(t, unreachable), "harness", "Harnessb"}, &out, &errOut)
	if code != exitAuth {
		t.Fatalf("exit %d, stderr %s", code, errOut.String())
	}
	if got := strings.Join(h.get(), ","); got != "sleep 2m5s,auth" {
		t.Fatalf("steps %q, want the 2m5s hold (14:01:00 to 14:03:05) before the realmd login", got)
	}
	if !strings.Contains(errOut.String(), "login: waiting 125 s for the realmd hour-start window") {
		t.Errorf("no hold line:\n%s", errOut.String())
	}
}

// A reconnect attempt holds before its handshake too. Deleting the call from reconnector.login fails this test.
func TestReconnectorLoginHoldsBeforeAuthLogin(t *testing.T) {
	h := pinHold(t, time.Date(2026, 11, 8, 14, 1, 0, 0, time.UTC))
	r := &reconnector{window: &loginWindow{}, st: config.Settings{Realmd: "127.0.0.1:1"}, account: "harness", password: "pw", character: "Harnessb", stderr: io.Discard}
	if _, err := r.login(context.Background()); err == nil {
		t.Fatal("the stubbed realmd login must fail the attempt")
	}
	if got := strings.Join(h.get(), ","); got != "sleep 2m5s,auth" {
		t.Fatalf("steps %q, want the hold before the realmd login", got)
	}
	// The window books the login after the hold, once.
	if n := len(r.window.times); n != 1 {
		t.Errorf("window noted %d logins, want 1", n)
	}
}

// A reconnect attempt whose wait ends inside the window is planned to the window's end (plus the jitter), so the shared budget and
// this process's window book the time the login really happens. Deleting the hold from reconnector.wait fails this test.
func TestReconnectorWaitIncludesTheHold(t *testing.T) {
	pinHold(t, time.Date(2026, 11, 8, 14, 0, 0, 0, time.UTC))
	loginHoldJitter = func() time.Duration { return 4 * time.Second }
	r := &reconnector{used: 1, window: &loginWindow{}, stderr: io.Discard}
	// The first backoff is 5 s: the login would start at 14:00:05, 180 s short of 14:03:05.
	if got, want := r.wait(context.Background()), backoff(1)+180*time.Second+4*time.Second; got != want {
		t.Errorf("wait %s, want %s (backoff, the rest of the hold, the jitter)", got, want)
	}
	t.Setenv(loginHourHoldEnv, "0")
	if got := r.wait(context.Background()); got != backoff(1) {
		t.Errorf("env off: wait %s, want the backoff %s", got, backoff(1))
	}
}

// Fleet bookings: 15 members started at hh:00:00 are booked inside a window that begins at 03:05, each at its own jittered time (the
// fleet starts the child then, so the child adds no hold and no jitter of its own), and the throttle window counts from those
// booked times: member 15 waits until the first booking has aged out.
func TestFleetBooksLoginsAfterTheHourStartHoldWithJitter(t *testing.T) {
	t.Setenv(loginHourHoldEnv, "")
	oldJitter := loginHoldJitter
	t.Cleanup(func() { loginHoldJitter = oldJitter })
	n := 0
	loginHoldJitter = func() time.Duration { n++; return time.Duration(n) * time.Second } // 1 s, 2 s, ...
	b := newLoginBudget(realmdThrottleLogins-1, realmdThrottleWindow)
	start := time.Date(2026, 11, 8, 14, 0, 0, 0, time.UTC)
	open := start.Add(3*time.Minute + 5*time.Second)
	seen := map[time.Time]bool{}
	var first, last time.Time
	for i := 1; i <= 15; i++ {
		at, hold := bookFleetLogin(b, start)
		if hold != 185*time.Second {
			t.Fatalf("member %d: hold %s, want 3m5s", i, hold)
		}
		if i < 15 {
			if want := open.Add(time.Duration(i) * time.Second); !at.Equal(want) {
				t.Fatalf("member %d booked at %s, want %s (window end plus its own jitter)", i, at.Format("15:04:05"), want.Format("15:04:05"))
			}
		}
		if seen[at] {
			t.Fatalf("member %d booked at %s, the same instant as another", i, at.Format("15:04:05"))
		}
		seen[at] = true
		if i == 1 {
			first = at
		}
		last = at
	}
	// Member 15 asked for open+15 s and the 14 bookings fill the window: it waits until the first booked login (open+1 s) has aged out.
	if last.Before(first.Add(realmdThrottleWindow)) {
		t.Errorf("member 15 booked at %s, want at least %s after the first booking (%s)", last.Format("15:04:05"), realmdThrottleWindow, first.Format("15:04:05"))
	}
	// Off, or outside the window: booked now, no jitter.
	t.Setenv(loginHourHoldEnv, "0")
	n = 0
	if at, hold := bookFleetLogin(newLoginBudget(14, realmdThrottleWindow), start); hold != 0 || !at.Equal(start) || n != 0 {
		t.Errorf("env off: booked %s hold %s jitter calls %d", at, hold, n)
	}
}

// N1: a parent that reads its clock just before the window (hh:59:56.99) still allows for the hold, because the child it starts
// reads hh:59:57 or later. Deleting the lookahead fails this test.
func TestParentAllowsForTheHoldJustBeforeTheWindow(t *testing.T) {
	t.Setenv(loginHourHoldEnv, "")
	oldJitter := loginHoldJitter
	t.Cleanup(func() { loginHoldJitter = oldJitter })
	loginHoldJitter = func() time.Duration { return 0 }
	at := time.Date(2026, 11, 8, 13, 59, 56, 990_000_000, time.UTC)
	if h := hourStartHold(at); h != 0 {
		t.Fatalf("setup: the window is not open at %s yet (hold %s)", at, h)
	}
	// The window ends at 14:03:05, 188.01 s later.
	want := 188*time.Second + 10*time.Millisecond
	if got := loginHoldHorizon(at); got != want {
		t.Errorf("horizon %s, want %s", got, want)
	}
	if got := loginDeadline(at, 90*time.Second); !got.Equal(at.Add(90*time.Second + want + loginHoldJitterMax)) {
		t.Errorf("deadline %s does not include the hold", got)
	}
	if b, hold := bookFleetLogin(newLoginBudget(14, realmdThrottleWindow), at); hold != want || b.Before(at.Add(want)) {
		t.Errorf("fleet booked %s (hold %s), want it at or after the window's end", b, hold)
	}
	// Well before the window: nothing.
	if h := loginHoldHorizon(at.Add(-time.Minute)); h != 0 {
		t.Errorf("a minute earlier the horizon is %s, want 0", h)
	}
}

// N2: when the budget or this process's own window pushes the start into the hour-start window, the wait is extended again. Here
// the window's busy time pushes a 5 s backoff to 14:00:00 exactly. Removing the second check fails this test.
func TestReconnectorWaitRechecksTheHoldAfterThePush(t *testing.T) {
	pinHold(t, time.Date(2026, 11, 8, 13, 50, 0, 0, time.UTC))
	loginHoldJitter = func() time.Duration { return 4 * time.Second }
	w := &loginWindow{busyUntil: time.Date(2026, 11, 8, 14, 0, 0, 0, time.UTC)}
	r := &reconnector{used: 1, window: w, stderr: io.Discard}
	want := 10*time.Minute + 185*time.Second + 4*time.Second
	if got := r.wait(context.Background()); got != want {
		t.Errorf("wait %s, want %s (the pushed start, the window, the jitter)", got, want)
	}
}

// N3: the timeout messages carry the real elapsed time, which with the hold is more than the base wait.
func TestElapsedSecs(t *testing.T) {
	if got := elapsedSecs(277*time.Second + 400*time.Millisecond); got != 277 {
		t.Errorf("elapsedSecs = %d, want 277", got)
	}
}

// The fleet does not start a child inside the hour-start window: it says so and waits. Deleting the booking change from runFleet
// fails this test (the first member would start at once).
func TestFleetHoldsStartsInsideTheHourStartWindow(t *testing.T) {
	t.Setenv(loginHourHoldEnv, "")
	oldJitter := loginHoldJitter
	t.Cleanup(func() { loginHoldJitter = oldJitter })
	loginHoldJitter = func() time.Duration { return 0 }
	ff := newFakeFleet()
	o := fastOpts
	o.now = func() time.Time { return time.Date(2026, 11, 8, 14, 0, 0, 0, time.UTC) }
	ctx, cancel := context.WithCancel(context.Background())
	var errOut syncBuf
	code := make(chan int, 1)
	go func() { code <- runFleet(ctx, party, ff.deps(), o, io.Discard, &errOut) }()
	waitFor(t, "hold line", func() bool {
		return strings.Contains(errOut.String(), "waiting 3m5s to log in Tankone: the realmd hour-start window")
	})
	if ff.child("127.0.0.1:9001") != nil {
		t.Error("the first member started inside the hold")
	}
	cancel()
	<-code
}

// phaseLogin gives the login child the hold on top of its 90 s: at a fake 14:00:30 the child is held until 14:03:05, and the walk
// still reaches the world instead of failing "not in world after 90 s".
func TestPhaseLoginDoesNotTimeOutInTheHourStartHold(t *testing.T) {
	t.Setenv(loginHourHoldEnv, "")
	f := newFixture(t)
	f.clock.t = time.Date(2026, 11, 8, 14, 0, 30, 0, time.UTC)
	// 100 status reads fail (2 s each in the fake clock: 200 s, past 90 s and past the 14:03:05 hold's end), then the agent is in world.
	f.agent.statusFailNext = 100
	f.defaultRun("onyxias_lair")
	code := f.run(context.Background())
	if code != exitOK || strings.Contains(f.summary().EndReason, "not in world after") {
		t.Fatalf("the login did not get through the hold: exit %d, reason %q", code, f.summary().EndReason)
	}
	if !strings.Contains(runLog(t, f), "login: waiting for the realmd hour-start window") {
		t.Errorf("run.log lacks the waiting line")
	}
	// Control: at a fake 14:30 the same slow login does time out after 90 s.
	f2 := newFixture(t)
	f2.clock.t = time.Date(2026, 11, 8, 14, 30, 0, 0, time.UTC)
	f2.agent.statusFailNext = 100
	f2.defaultRun("onyxias_lair")
	if c := f2.run(context.Background()); c != exitRaidWalkFailed || !strings.Contains(f2.summary().EndReason, "not in world after 90 s") {
		t.Fatalf("control: exit %d reason %q, want the 90 s timeout outside the window", c, f2.summary().EndReason)
	}
}

// N3: a login that really does time out in the hold reports the time it waited (the 90 s plus the hold), not "90 s".
func TestPhaseLoginTimeoutNamesTheRealElapsedTime(t *testing.T) {
	t.Setenv(loginHourHoldEnv, "")
	f := newFixture(t)
	f.clock.t = time.Date(2026, 11, 8, 14, 1, 0, 0, time.UTC)
	f.agent.statusFailNext = 1000
	f.defaultRun("onyxias_lair")
	// 90 s + 125 s of hold (14:01:00 to 14:03:05) + 20 s of jitter, then the next 2 s poll: 236 s.
	if c, reason := f.run(context.Background()), f.summary().EndReason; c != exitRaidWalkFailed || !strings.Contains(reason, "not in world after 236 s") {
		t.Fatalf("exit %d, reason %q, want it to name 236 s", c, reason)
	}
}

// Round 3: the budget stores the login's final time. When the budget pushes a login into the hour-start window, the stored slot is the
// time after the hold and jitter, not the time before them (which would let the slot expire minutes early).
func TestBudgetStoresTheFinalLoginTimeAfterTheHold(t *testing.T) {
	t.Setenv(loginHourHoldEnv, "")
	oldJitter := loginHoldJitter
	t.Cleanup(func() { loginHoldJitter = oldJitter })
	jitterCalls := 0
	loginHoldJitter = func() time.Duration { jitterCalls++; return 4 * time.Second }
	b := newLoginBudget(1, 300*time.Second) // margin 10 s: a slot at 13:54:50 leaves the window at 14:00:00
	earlier := time.Date(2026, 11, 8, 13, 54, 50, 0, time.UTC)
	b.reserve(earlier)
	got := b.reserveAdjusted(time.Date(2026, 11, 8, 13, 55, 5, 0, time.UTC), holdAdjuster())
	// The budget pushes the login to 14:00:00, inside the window; the hold moves it to 14:03:05 plus the 4 s jitter.
	want := time.Date(2026, 11, 8, 14, 3, 9, 0, time.UTC)
	if !got.Equal(want) {
		t.Fatalf("booked %s, want %s", got.Format("15:04:05"), want.Format("15:04:05"))
	}
	if len(b.slots) != 2 || !b.slots[0].Equal(earlier) || !b.slots[1].Equal(want) {
		t.Fatalf("stored slots %v, want [%s %s]: the slot must be the final login time", b.slots, earlier.Format("15:04:05"), want.Format("15:04:05"))
	}
	if jitterCalls != 1 {
		t.Errorf("the jitter was sampled %d times, want once per booking", jitterCalls)
	}
	// The next login can not come before the stored slot has left the window: 14:03:09 + 310 s.
	if next := b.reserve(want); !next.Equal(want.Add(310 * time.Second)) {
		t.Errorf("next login at %s, want %s", next.Format("15:04:05"), want.Add(310*time.Second).Format("15:04:05"))
	}
}

// The same through a fleet child: the reconnector asks the supervisor's budget over the port, and the slot the supervisor stores is
// the time the reconnect will really log in (now plus the wait it got). Booking at the pushed time and adding the hold afterwards
// fails this test.
func TestReconnectBudgetSlotIsTheFinalReconnectTime(t *testing.T) {
	now := time.Date(2026, 11, 8, 13, 55, 0, 0, time.UTC)
	pinHold(t, now)
	loginHoldJitter = func() time.Duration { return 4 * time.Second }
	b := newLoginBudget(1, 300*time.Second)
	earlier := time.Date(2026, 11, 8, 13, 54, 50, 0, time.UTC)
	b.reserve(earlier)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	addr, stop, err := serveLoginBudget(ctx, b, testBudgetToken)
	if err != nil {
		t.Fatal(err)
	}
	defer stop()
	r := &reconnector{used: 1, window: &loginWindow{}, budget: newBudgetClient(addr, testBudgetToken), stderr: io.Discard}
	wait := r.wait(ctx)
	final := now.Add(wait)
	if want := time.Date(2026, 11, 8, 14, 3, 9, 0, time.UTC); !final.Equal(want) {
		t.Fatalf("the reconnect lands at %s, want %s (pushed to 14:00:00, then the hold and the jitter)", final.Format("15:04:05"), want.Format("15:04:05"))
	}
	if len(b.slots) != 2 || !b.slots[1].Equal(final) {
		t.Fatalf("stored slots %v, want the second slot at the final reconnect time %s", b.slots, final.Format("15:04:05"))
	}
}
