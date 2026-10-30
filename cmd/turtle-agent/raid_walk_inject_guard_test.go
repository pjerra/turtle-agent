// raid_walk_inject_guard_test.go: the selection guards of --inject (reviews ad-i2-r2, ad-i2-r3): every line that acts
// on the selection needs the identity read-back (`.die`, `.freeze`, `.unfreeze`), and cleanups retry within the
// recovery deadline, whose attempts and retry waits may not run past it. Each test here fails on the unguarded
// implementation.
package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/pjerra/turtle-agent/internal/packet"
	"github.com/pjerra/turtle-agent/internal/raidwalk"
)

// injectGMCount counts the gm lines with the prefix.
func (f *fixture) injectGMCount(prefix string) int {
	n := 0
	for _, line := range f.agent.gmLines() {
		if strings.HasPrefix(line, prefix) {
			n++
		}
	}
	return n
}

// injectTargetCount counts the target RPCs for g.
func (f *fixture) injectTargetCount(g packet.GUID) int {
	n := 0
	for _, got := range f.targetGUIDs() {
		if got == g {
			n++
		}
	}
	return n
}

// summaryText reads <out>/summary.txt.
func (f *fixture) summaryText() string {
	f.t.Helper()
	raw, err := os.ReadFile(filepath.Join(f.out, "summary.txt"))
	if err != nil {
		f.t.Fatalf("summary.txt: %v", err)
	}
	return string(raw)
}

// TestRaidWalkInjectKillMTNeedsIdentity: a coordinate-only match is not proof. `.pinfo` with no name names the
// selected player; `.die` goes out only when that is the MT. A mismatch is terminal (no retry, no `.die`).
func TestRaidWalkInjectKillMTNeedsIdentity(t *testing.T) {
	t.Run("pinfo names another bot", func(t *testing.T) {
		f := newFixture(t)
		f.setupInject()
		f.injFight()
		f.injectRun("pull", 20)
		f.agent.mu.Lock()
		f.agent.pinfoGUID = packet.GUID(1001) // Bot02, 1 yd from the MT: coordinates cannot tell them apart
		f.agent.mu.Unlock()
		f.opts.injects = []string{"kill-mt"}
		if code := f.run(context.Background()); code != exitOK {
			t.Fatalf("exit %d: %s", code, f.stderr.String())
		}
		if i := f.gmIndex(".die"); i >= 0 {
			t.Fatalf("`.die` was sent although .pinfo named another bot: %v", f.agent.gmLines())
		}
		if f.gmIndex(".pinfo") < 0 {
			t.Fatalf("no .pinfo identity read-back: %v", f.agent.gmLines())
		}
		if gps, pinfo := f.gmIndex(".gps"), f.gmIndex(".pinfo"); gps >= 0 && gps > pinfo {
			t.Fatalf(".pinfo came before .gps: %v", f.agent.gmLines())
		}
		if got := f.injectTargetCount(packet.GUID(1000)); got != 1 {
			t.Fatalf("%d MT targets, want 1 (an identity mismatch is not retried)", got)
		}
		sum := f.summary()
		if len(sum.Injections) != 1 || sum.Injections[0].Verdict != raidwalk.VerdictInconclusive ||
			!strings.Contains(sum.Injections[0].Reason, "selection not proven") {
			t.Fatalf("injections %+v, want one inconclusive 'selection not proven'", sum.Injections)
		}
	})

	t.Run("a selection one command behind a nearby bot", func(t *testing.T) {
		f := newFixture(t)
		f.setupInject()
		f.injFight()
		f.injectRun("pull", 20)
		f.agent.mu.Lock()
		f.agent.lagTargets = true
		f.agent.pinfoLagTargets = true
		f.agent.selGUID = packet.GUID(1001) // the previous selection, 1 yd from the MT
		f.agent.mu.Unlock()
		f.opts.injects = []string{"kill-mt"}
		if code := f.run(context.Background()); code != exitOK {
			t.Fatalf("exit %d: %s", code, f.stderr.String())
		}
		if i := f.gmIndex(".die"); i >= 0 {
			t.Fatalf("`.die` was sent against a selection one command behind: %v", f.agent.gmLines())
		}
		sum := f.summary()
		if len(sum.Injections) != 1 || !strings.Contains(sum.Injections[0].Reason, "selection not proven") {
			t.Fatalf("injections %+v, want one 'selection not proven'", sum.Injections)
		}
	})
}

// TestRaidWalkInjectFreezeNeedsIdentity: every line that acts on the selection needs the identity read-back, the
// reversible `.freeze` included. `.pinfo` that names an untracked nearby player is not the commander, so nothing is
// frozen and the fault ends inconclusive (a coordinate match would have sent the line).
func TestRaidWalkInjectFreezeNeedsIdentity(t *testing.T) {
	f := newFixture(t)
	f.setupInject() // Bot01..Bot30 one yard apart: a coordinate match cannot name its unit
	f.defaultRun("onyxias_lair")
	f.agent.mu.Lock()
	f.agent.pinfoGUID = packet.GUID(0x4D2)
	f.agent.pinfoName = "Stranger"
	f.agent.mu.Unlock()
	f.opts.injects = []string{"runaway"}
	if code := f.run(context.Background()); code != exitOK {
		t.Fatalf("exit %d: %s", code, f.stderr.String())
	}
	if n := f.injectGMCount(".freeze "); n != 0 {
		t.Fatalf("%d .freeze lines although .pinfo named a stranger: %v", n, f.agent.gmLines())
	}
	if n := f.injectGMCount(".unfreeze "); n != 0 {
		t.Fatalf("%d .unfreeze lines: %v", n, f.agent.gmLines())
	}
	if f.gmIndex(".pinfo") < 0 {
		t.Fatalf("no .pinfo identity read-back: %v", f.agent.gmLines())
	}
	if got := f.injectTargetCount(injOwnGUID); got != 1 {
		t.Fatalf("%d self targets, want 1 (an identity mismatch is not retried): %v", got, f.targetGUIDs())
	}
	sum := f.summary()
	if len(sum.Injections) != 1 || sum.Injections[0].Verdict != raidwalk.VerdictInconclusive ||
		!strings.Contains(sum.Injections[0].Reason, "selection not proven") {
		t.Fatalf("injections %+v, want one inconclusive 'selection not proven'", sum.Injections)
	}
}

// logTimes is the fake-clock time of every run.log line that contains s, in order.
func (f *fixture) logTimes(s string) []time.Time {
	f.t.Helper()
	raw, err := os.ReadFile(filepath.Join(f.out, "run.log"))
	if err != nil {
		f.t.Fatalf("run.log: %v", err)
	}
	var out []time.Time
	for _, line := range strings.Split(string(raw), "\n") {
		if len(line) < 13 || !strings.Contains(line, s) {
			continue
		}
		ts, err := time.Parse("15:04:05.000", line[:12])
		if err != nil {
			continue
		}
		out = append(out, ts)
	}
	return out
}

// freezeThenInterrupt runs a runaway walk and cancels it as soon as the victim is frozen (the bot is left frozen and
// only Cleanup can release it).
func freezeThenInterrupt(t *testing.T, f *fixture, victim string) (context.Context, context.CancelFunc) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	f.agent.mu.Lock()
	f.agent.onGM = func(line string) {
		if line == ".freeze "+victim {
			cancel()
		}
	}
	f.agent.mu.Unlock()
	return ctx, cancel
}

// TestRaidWalkInjectCleanupRetries: a failed release is retried with a fresh context every 3 s, and an exhausted
// recovery deadline is a BLOCKING incident that names the frozen bots and the manual command, not a silent exit.
func TestRaidWalkInjectCleanupRetries(t *testing.T) {
	const victim = "Bot39"

	t.Run("a failed release is retried", func(t *testing.T) {
		f := newFixture(t)
		f.setupInject()
		f.defaultRun("onyxias_lair")
		f.opts.injects = []string{"runaway"}
		ctx, cancel := freezeThenInterrupt(t, f, victim)
		defer cancel()
		var mu sync.Mutex
		releases := 0
		f.agent.mu.Lock()
		f.agent.gmFail = func(line string) error {
			if !strings.HasPrefix(line, ".unfreeze ") {
				return nil
			}
			mu.Lock()
			defer mu.Unlock()
			releases++
			if releases == 1 {
				return errors.New("the RPC timed out")
			}
			return nil
		}
		f.agent.mu.Unlock()
		if code := f.run(ctx); code != exitRaidWalkFailed {
			t.Fatalf("exit %d, want %d: %s", code, exitRaidWalkFailed, f.stderr.String())
		}
		if n := f.injectGMCount(".unfreeze "); n < 2 {
			t.Fatalf("%d .unfreeze attempts, want a retry: %v", n, f.agent.gmLines())
		}
		sum := f.summary()
		if len(sum.Incidents) != 0 {
			t.Fatalf("a recovered cleanup wrote incidents: %+v", sum.Incidents)
		}
	})

	t.Run("rpc error until the recovery deadline", func(t *testing.T) {
		f := newFixture(t)
		f.setupInject()
		f.defaultRun("onyxias_lair")
		f.opts.injects = []string{"runaway"}
		ctx, cancel := freezeThenInterrupt(t, f, victim)
		defer cancel()
		f.agent.mu.Lock()
		f.agent.gmFail = func(line string) error {
			if strings.HasPrefix(line, ".unfreeze ") {
				return errors.New("the RPC timed out")
			}
			return nil
		}
		f.agent.mu.Unlock()
		if code := f.run(ctx); code < exitRaidWalkInjectFailed {
			t.Fatalf("exit %d, want at least %d: %s", code, exitRaidWalkInjectFailed, f.stderr.String())
		}
		if n := f.injectGMCount(".unfreeze "); n < 2 {
			t.Fatalf("%d .unfreeze attempts, want retries: %v", n, f.agent.gmLines())
		}
		sum := f.summary()
		if len(sum.Incidents) != 1 {
			t.Fatalf("incidents %+v, want one blocking", sum.Incidents)
		}
		inc := sum.Incidents[0]
		if inc.Severity != "blocking" || len(inc.Bots) != 1 || inc.Bots[0] != victim {
			t.Fatalf("incident %+v, want blocking with %s", inc, victim)
		}
		if !strings.Contains(inc.Manual, ".unfreeze "+victim) {
			t.Fatalf("manual %q does not name the exact release", inc.Manual)
		}
		inj := f.injectJSON()
		faults, _ := inj["incidents"].([]any)
		if len(faults) != 1 {
			t.Fatalf("inject.json incidents %v", inj["incidents"])
		}
		if raw, ok := faults[0].(map[string]any); !ok || raw["severity"] != "blocking" {
			t.Fatalf("inject.json incident %v", faults[0])
		}
		if !strings.Contains(f.summaryText(), "BLOCKING") {
			t.Fatalf("summary.txt has no BLOCKING incident:\n%s", f.summaryText())
		}
	})

	t.Run("pinfo naming a stranger keeps the bot frozen", func(t *testing.T) {
		f := newFixture(t)
		f.setupInject()
		f.defaultRun("onyxias_lair")
		f.opts.injects = []string{"runaway"}
		ctx, cancel := freezeThenInterrupt(t, f, victim)
		defer cancel()
		f.agent.mu.Lock()
		old := f.agent.onGM
		f.agent.onGM = func(line string) {
			if old != nil {
				old(line)
			}
			if line == ".freeze "+victim {
				// The release's selection now names an untracked nearby player: `.unfreeze` must not go out.
				f.agent.mu.Lock()
				f.agent.pinfoGUID = packet.GUID(0x4D2)
				f.agent.pinfoName = "Stranger"
				f.agent.mu.Unlock()
			}
		}
		f.agent.mu.Unlock()
		if code := f.run(ctx); code < exitRaidWalkInjectFailed {
			t.Fatalf("exit %d, want at least %d: %s", code, exitRaidWalkInjectFailed, f.stderr.String())
		}
		if n := f.injectGMCount(".unfreeze "); n != 0 {
			t.Fatalf("%d .unfreeze lines against a stranger: %v", n, f.agent.gmLines())
		}
		if n := f.injectTargetCount(injOwnGUID); n < 5 {
			t.Fatalf("%d self targets, want cleanup retries: %v", n, f.targetGUIDs())
		}
		sum := f.summary()
		if len(sum.Incidents) != 1 || sum.Incidents[0].Severity != "blocking" {
			t.Fatalf("incidents %+v, want one blocking", sum.Incidents)
		}
	})

	t.Run("a slow attempt cannot run past the recovery deadline", func(t *testing.T) {
		f := newFixture(t)
		f.setupInject()
		f.defaultRun("onyxias_lair")
		f.opts.injects = []string{"runaway"}
		ctx, cancel := freezeThenInterrupt(t, f, victim)
		defer cancel()
		f.agent.mu.Lock()
		old := f.agent.onGM
		f.agent.onGM = func(line string) {
			if old != nil {
				old(line)
			}
			if line == ".freeze "+victim {
				// Every cleanup selection now waits out its `.pinfo` deadline: the old unbounded attempt ran past 60 s.
				f.agent.mu.Lock()
				f.agent.noPInfo = true
				f.agent.mu.Unlock()
			}
		}
		f.agent.mu.Unlock()
		if code := f.run(ctx); code < exitRaidWalkInjectFailed {
			t.Fatalf("exit %d, want at least %d: %s", code, exitRaidWalkInjectFailed, f.stderr.String())
		}
		freezes := f.logTimes("> .freeze " + victim)
		gpss := f.logTimes("> .gps")
		blocking := f.logTimes("BLOCKING:")
		if len(freezes) != 1 || len(blocking) != 1 {
			t.Fatalf("freeze times %v, blocking times %v", freezes, blocking)
		}
		gps := time.Time{}
		for _, ts := range gpss {
			if ts.After(freezes[0]) {
				gps = ts
				break
			}
		}
		if gps.IsZero() {
			t.Fatalf("no cleanup .gps after the freeze: %v", gpss)
		}
		// Cleanup entered injectSelectSettle before its first `.gps`.
		started := gps.Add(-injectSelectSettle)
		if d := blocking[0].Sub(started); d <= 0 || d > injectCleanupDeadline {
			t.Fatalf("cleanup recorded the incident after %s, want at most %s", d, injectCleanupDeadline)
		}
	})

	t.Run("release timeout until the recovery deadline", func(t *testing.T) {
		f := newFixture(t)
		f.setupInject()
		f.defaultRun("onyxias_lair")
		f.opts.injects = []string{"runaway"}
		ctx, cancel := freezeThenInterrupt(t, f, victim)
		defer cancel()
		f.agent.mu.Lock()
		f.agent.gmFail = func(line string) error {
			if strings.HasPrefix(line, ".unfreeze ") {
				return context.DeadlineExceeded
			}
			return nil
		}
		f.agent.mu.Unlock()
		if code := f.run(ctx); code < exitRaidWalkInjectFailed {
			t.Fatalf("exit %d, want at least %d: %s", code, exitRaidWalkInjectFailed, f.stderr.String())
		}
		if n := f.injectGMCount(".unfreeze "); n < 2 {
			t.Fatalf("%d .unfreeze attempts, want retries: %v", n, f.agent.gmLines())
		}
		sum := f.summary()
		if len(sum.Incidents) != 1 || sum.Incidents[0].Severity != "blocking" {
			t.Fatalf("incidents %+v, want one blocking", sum.Incidents)
		}
	})
}

// TestRaidWalkInjectCleanupRunsOnPanic: a panic in the watch still runs the injector's cleanup before it propagates.
func TestRaidWalkInjectCleanupRunsOnPanic(t *testing.T) {
	inj := installFakeInjector(t, false)
	testFakeInjectorPanicOnTick = true
	t.Cleanup(func() { testFakeInjectorPanicOnTick = false })
	f := newFixture(t)
	f.defaultRun("onyxias_lair")
	f.opts.injects = []string{"kill-mt"}
	func() {
		defer func() {
			if r := recover(); r == nil {
				t.Fatal("the fake injector's panic did not propagate")
			}
			fake := inj()
			if fake == nil {
				t.Fatal("the walk did not create an injector")
			}
			if fake.cleanups != 1 {
				t.Fatalf("Cleanup ran %d times on panic, want once", fake.cleanups)
			}
		}()
		f.run(context.Background())
	}()
}

// TestRaidWalkInjectErroredFreezeStillReleases: a `.freeze` whose RPC fails may have landed - the GM command took
// effect and only the call failed - so the victim is treated as maybe-frozen and Cleanup unfreezes it. The fault
// still ends inconclusive; the release is the safety net, not a verdict.
func TestRaidWalkInjectErroredFreezeStillReleases(t *testing.T) {
	const victim = "Bot39"

	t.Run("the freeze lands and the RPC fails", func(t *testing.T) {
		f := newFixture(t)
		f.setupInject()
		f.defaultRun("onyxias_lair")
		f.opts.injects = []string{"runaway"}
		f.agent.mu.Lock()
		f.agent.gmFail = func(line string) error {
			if line != ".freeze "+victim {
				return nil
			}
			// The server cast the freeze (spell 9454); only the RPC's answer was lost.
			return errors.New("the RPC timed out")
		}
		f.agent.mu.Unlock()
		if code := f.run(context.Background()); code != exitOK {
			t.Fatalf("exit %d: %s", code, f.stderr.String())
		}
		freeze, unfreeze := f.gmIndex(".freeze "+victim), f.gmIndex(".unfreeze "+victim)
		if freeze < 0 || unfreeze < 0 {
			t.Fatalf("freeze %d unfreeze %d in %v", freeze, unfreeze, f.agent.gmLines())
		}
		if unfreeze < freeze {
			t.Fatalf(".unfreeze (%d) came before the freeze (%d): %v", unfreeze, freeze, f.agent.gmLines())
		}
		sum := f.summary()
		if len(sum.Injections) != 1 || sum.Injections[0].Verdict != raidwalk.VerdictInconclusive {
			t.Fatalf("injections %+v, want one inconclusive", sum.Injections)
		}
		if len(sum.Incidents) != 0 {
			t.Fatalf("the released victim left incidents %+v", sum.Incidents)
		}
	})

	t.Run("the call's context ends", func(t *testing.T) {
		f := newFixture(t)
		f.setupInject()
		f.defaultRun("onyxias_lair")
		f.opts.injects = []string{"runaway"}
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		f.agent.mu.Lock()
		f.agent.gmFail = func(line string) error {
			if line != ".freeze "+victim {
				return nil
			}
			// The freeze landed, then the walk's context ended inside the call.
			cancel()
			return ctx.Err()
		}
		f.agent.mu.Unlock()
		if code := f.run(ctx); code != exitRaidWalkFailed {
			t.Fatalf("exit %d, want %d: %s", code, exitRaidWalkFailed, f.stderr.String())
		}
		freeze, unfreeze, stop := f.gmIndex(".freeze "+victim), f.gmIndex(".unfreeze "+victim), f.gmIndex(".bot run stop")
		if freeze < 0 || unfreeze < 0 || stop < 0 {
			t.Fatalf("freeze %d unfreeze %d stop %d in %v", freeze, unfreeze, stop, f.agent.gmLines())
		}
		if unfreeze > stop {
			t.Fatalf(".unfreeze (%d) does not come before .bot run stop (%d): %v", unfreeze, stop, f.agent.gmLines())
		}
		sum := f.summary()
		if len(sum.Injections) != 1 || sum.Injections[0].Verdict != raidwalk.VerdictInconclusive {
			t.Fatalf("injections %+v, want one inconclusive", sum.Injections)
		}
		if len(sum.Incidents) != 0 {
			t.Fatalf("the released victim left incidents %+v", sum.Incidents)
		}
	})
}
