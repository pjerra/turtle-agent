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

// Fleet bookings: 15 members started at hh:00:00 are booked inside a window that begins at 03:05, so the 15th waits for the
// throttle window (realmd lets 14 in, the 15th has to wait for the first to age out).
func TestFleetBooksLoginsAfterTheHourStartHold(t *testing.T) {
	t.Setenv(loginHourHoldEnv, "")
	b := newLoginBudget(realmdThrottleLogins-1, realmdThrottleWindow)
	start := time.Date(2026, 11, 8, 14, 0, 0, 0, time.UTC)
	open := start.Add(3*time.Minute + 5*time.Second)
	var last time.Time
	for i := 1; i <= 15; i++ {
		at, hold := bookFleetLogin(b, start)
		if hold != 185*time.Second {
			t.Fatalf("member %d: hold %s, want 3m5s", i, hold)
		}
		if at.Before(open) {
			t.Fatalf("member %d booked at %s, inside the hold (before %s)", i, at.Format("15:04:05"), open.Format("15:04:05"))
		}
		if i < 15 && !at.Equal(open) {
			t.Fatalf("member %d booked at %s, want %s", i, at.Format("15:04:05"), open.Format("15:04:05"))
		}
		last = at
	}
	if last.Before(open.Add(realmdThrottleWindow)) {
		t.Errorf("member 15 booked at %s, want it at least %s after the window opened", last.Format("15:04:05"), realmdThrottleWindow)
	}
	// Off, or outside the window: booked now.
	t.Setenv(loginHourHoldEnv, "0")
	if at, hold := bookFleetLogin(newLoginBudget(14, realmdThrottleWindow), start); hold != 0 || !at.Equal(start) {
		t.Errorf("env off: booked %s hold %s", at, hold)
	}
}

// The fleet does not start a child inside the hour-start window: it says so and waits. Deleting the booking change from runFleet
// fails this test (the first member would start at once).
func TestFleetHoldsStartsInsideTheHourStartWindow(t *testing.T) {
	t.Setenv(loginHourHoldEnv, "")
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
