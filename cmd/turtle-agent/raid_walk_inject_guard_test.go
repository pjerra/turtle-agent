// raid_walk_inject_guard_test.go: the selection guards of --inject (review ad-i2-r2): a destructive line needs the
// identity read-back, a reversible line only goes to an isolated victim, and cleanups retry until the bots are free or
// the recovery deadline leaves a BLOCKING incident. Each test here fails on the unguarded implementation.
package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

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

// TestRaidWalkInjectFreezeNeedsAnIsolatedTarget: a reversible line's .gps match is ambiguous while another unit stands
// within 6 yd of the victim. The crowd is retried once, then the fault is inconclusive and nothing is frozen.
func TestRaidWalkInjectFreezeNeedsAnIsolatedTarget(t *testing.T) {
	t.Run("crowded target", func(t *testing.T) {
		f := newFixture(t)
		f.setupInject() // Bot02..Bot30 one yard apart: the lag victim Bot30 is crowded
		f.defaultRun("onyxias_lair")
		f.opts.injects = []string{"lag"}
		if code := f.run(context.Background()); code != exitOK {
			t.Fatalf("exit %d: %s", code, f.stderr.String())
		}
		if n := f.injectGMCount(".freeze "); n != 0 {
			t.Fatalf("%d .freeze lines for a crowded target: %v", n, f.agent.gmLines())
		}
		if n := f.injectGMCount(".unfreeze "); n != 0 {
			t.Fatalf("%d .unfreeze lines: %v", n, f.agent.gmLines())
		}
		if got := f.injectTargetCount(injOwnGUID); got != 2 {
			t.Fatalf("%d self targets, want 2 (one retry): %v", got, f.targetGUIDs())
		}
		sum := f.summary()
		if len(sum.Injections) != 1 || sum.Injections[0].Verdict != raidwalk.VerdictInconclusive ||
			!strings.Contains(sum.Injections[0].Reason, "crowded") {
			t.Fatalf("injections %+v, want one inconclusive 'crowded'", sum.Injections)
		}
	})

	t.Run("isolated target", func(t *testing.T) {
		f := newFixture(t)
		f.setupInject()
		f.agent.mu.Lock()
		lead := [3]float32{injLeadX, injLeadY, injLeadZ}
		for i := 2; i <= 29; i++ {
			f.agent.pos[fmt.Sprintf("Bot%02d", i)] = lead
		}
		f.agent.pos["Bot30"] = [3]float32{lead[0] + 20, lead[1], lead[2]}
		f.agent.mu.Unlock()
		f.defaultRun("onyxias_lair")
		f.opts.injects = []string{"lag"}
		if code := f.run(context.Background()); code != exitOK {
			t.Fatalf("exit %d: %s", code, f.stderr.String())
		}
		if n := f.injectGMCount(".freeze Bot30"); n == 0 {
			t.Fatalf("no .freeze for an isolated victim: %v", f.agent.gmLines())
		}
		if got := f.injectTargetCount(injOwnGUID); got < 1 {
			t.Fatalf("%d self targets, want the freeze's selection: %v", got, f.targetGUIDs())
		}
	})
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

	t.Run("selection mismatch until the recovery deadline", func(t *testing.T) {
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
				f.agent.mu.Lock()
				f.agent.statusTargetMismatch = true
				f.agent.mu.Unlock()
			}
		}
		f.agent.mu.Unlock()
		if code := f.run(ctx); code < exitRaidWalkInjectFailed {
			t.Fatalf("exit %d, want at least %d: %s", code, exitRaidWalkInjectFailed, f.stderr.String())
		}
		if n := f.injectTargetCount(injOwnGUID); n < 5 {
			t.Fatalf("%d self targets, want cleanup retries: %v", n, f.targetGUIDs())
		}
		sum := f.summary()
		if len(sum.Incidents) != 1 || sum.Incidents[0].Severity != "blocking" {
			t.Fatalf("incidents %+v, want one blocking", sum.Incidents)
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
