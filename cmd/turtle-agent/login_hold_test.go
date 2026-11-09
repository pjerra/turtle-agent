package main

import (
	"bytes"
	"context"
	"strings"
	"testing"
	"time"
)

// holdAt runs the hold with a fake clock at the given UTC time and returns what it slept and logged.
func holdAt(t *testing.T, at string) (slept time.Duration, log string) {
	t.Helper()
	now, err := time.Parse("15:04:05", at)
	if err != nil {
		t.Fatal(err)
	}
	oldNow, oldSleep, oldJitter := loginHoldNow, loginHoldSleep, loginHoldJitter
	t.Cleanup(func() { loginHoldNow, loginHoldSleep, loginHoldJitter = oldNow, oldSleep, oldJitter })
	loginHoldJitter = func() time.Duration { return 0 }
	loginHoldNow = func() time.Time { return now.AddDate(2026, 10, 13) }
	loginHoldSleep = func(_ context.Context, d time.Duration) error { slept = d; return nil }
	var out bytes.Buffer
	if err := holdLoginForHourStart(context.Background(), &out); err != nil {
		t.Fatal(err)
	}
	return slept, out.String()
}

func TestLoginHoldWaitsUntilThreeOhFiveAtMinuteOne(t *testing.T) {
	t.Setenv(loginHourHoldEnv, "")
	slept, log := holdAt(t, "14:01:30")
	if slept != 95*time.Second {
		t.Fatalf("slept %s, want 1m35s (until 14:03:05)", slept)
	}
	if want := "login: waiting 95 s for the realmd hour-start window\n"; log != want {
		t.Fatalf("log %q, want %q", log, want)
	}
}

func TestLoginHoldWindowEdges(t *testing.T) {
	t.Setenv(loginHourHoldEnv, "")
	for at, want := range map[string]time.Duration{
		"14:00:00": 185 * time.Second,
		"14:03:04": time.Second,
		"14:03:05": 0,
		"14:03:10": 0,
		"14:30:00": 0,
		"13:59:56": 0,                 // before the window
		"13:59:57": 188 * time.Second, // the window starts 3 s before the hour, for clock skew
		"13:59:58": 187 * time.Second, // the L1 case
		"13:59:59": 186 * time.Second,
	} {
		if slept, _ := holdAt(t, at); slept != want {
			t.Errorf("at %s slept %s, want %s", at, slept, want)
		}
	}
}

func TestLoginHoldNoWaitAfterWindowLogsNothing(t *testing.T) {
	t.Setenv(loginHourHoldEnv, "")
	slept, log := holdAt(t, "14:03:10")
	if slept != 0 || log != "" {
		t.Fatalf("slept %s log %q, want neither", slept, log)
	}
}

func TestLoginHoldEnvOffMeansNoWait(t *testing.T) {
	t.Setenv(loginHourHoldEnv, "0")
	slept, log := holdAt(t, "14:01:30")
	if slept != 0 || strings.Contains(log, "waiting") {
		t.Fatalf("slept %s log %q with %s=0", slept, log, loginHourHoldEnv)
	}
}

func TestLoginHoldUsesUTC(t *testing.T) {
	t.Setenv(loginHourHoldEnv, "")
	if d := hourStartHold(time.Date(2026, 11, 8, 15, 1, 30, 0, time.FixedZone("x", 3600))); d != 95*time.Second {
		// 15:01:30 at +01:00 is 14:01:30 UTC
		t.Fatalf("hold %s, want 1m35s", d)
	}
}

func TestLoginHoldCutOffByContext(t *testing.T) {
	t.Setenv(loginHourHoldEnv, "")
	oldNow, oldSleep := loginHoldNow, loginHoldSleep
	t.Cleanup(func() { loginHoldNow, loginHoldSleep = oldNow, oldSleep })
	loginHoldNow = func() time.Time { return time.Date(2026, 11, 8, 14, 1, 0, 0, time.UTC) }
	loginHoldSleep = oldSleep
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := holdLoginForHourStart(ctx, &bytes.Buffer{}); err == nil {
		t.Fatal("a cancelled context must end the hold with an error")
	}
}

// After the hold a per-process jitter spreads the logins that were held; it is not added when nothing was held.
func TestLoginHoldAddsJitterAfterTheWindowOnly(t *testing.T) {
	t.Setenv(loginHourHoldEnv, "")
	oldNow, oldSleep, oldJitter := loginHoldNow, loginHoldSleep, loginHoldJitter
	t.Cleanup(func() { loginHoldNow, loginHoldSleep, loginHoldJitter = oldNow, oldSleep, oldJitter })
	var slept []time.Duration
	loginHoldSleep = func(_ context.Context, d time.Duration) error { slept = append(slept, d); return nil }
	loginHoldJitter = func() time.Duration { return 7 * time.Second }
	loginHoldNow = func() time.Time { return time.Date(2026, 11, 8, 14, 1, 30, 0, time.UTC) }
	if err := holdLoginForHourStart(context.Background(), &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}
	if len(slept) != 2 || slept[0] != 95*time.Second || slept[1] != 7*time.Second {
		t.Fatalf("slept %v, want the hold then the 7 s jitter", slept)
	}
	slept = nil
	loginHoldNow = func() time.Time { return time.Date(2026, 11, 8, 14, 30, 0, 0, time.UTC) }
	if err := holdLoginForHourStart(context.Background(), &bytes.Buffer{}); err != nil || len(slept) != 0 {
		t.Fatalf("outside the window: slept %v err %v", slept, err)
	}
}

// loginDeadline gives a parent the hold on top of its base wait, and the jitter's most; off or outside the window it is the base.
func TestLoginDeadlineAddsTheHold(t *testing.T) {
	t.Setenv(loginHourHoldEnv, "")
	at := time.Date(2026, 11, 8, 14, 0, 30, 0, time.UTC)
	want := at.Add(90*time.Second + 155*time.Second + loginHoldJitterMax)
	if got := loginDeadline(at, 90*time.Second); !got.Equal(want) {
		t.Errorf("deadline %s, want %s", got, want)
	}
	if got := loginDeadline(at.Add(10*time.Minute), 90*time.Second); !got.Equal(at.Add(10*time.Minute + 90*time.Second)) {
		t.Errorf("outside the window the deadline is the base, got %s", got)
	}
	t.Setenv(loginHourHoldEnv, "0")
	if got := loginDeadline(at, 90*time.Second); !got.Equal(at.Add(90 * time.Second)) {
		t.Errorf("env off: deadline %s", got)
	}
}
