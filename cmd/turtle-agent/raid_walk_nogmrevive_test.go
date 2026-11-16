package main

import (
	"context"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/pjerra/turtle-agent/internal/dungeon"
	"github.com/pjerra/turtle-agent/internal/objects"
)

// T45: --no-gm-revive is the owner's strict run. Walk 110 (--revive-at-raid=false --wipe-resume 3 --auto-resume 3)
// logged "near-wipe stop: recovering instead of ending the walk" and then sent 18-20 `.revive` lines. Under the flag
// the walk never sends `.revive`: a near-wipe rests and resumes with the dead hires left to the run, a wipe recovers
// only by the run's own corpse run (a self-regroup), else the walk ends, and `w.gm` fails closed on a missed path.

// TestRaidWalkNoGMReviveNearWipeSendsNoRevive is the T45 case: a near-wipe with someone alive used to send one
// `.revive` per dead hire. Under the flag nothing goes out, the walk resumes and the note names the flag. Without the
// flag's near-wipe branch the revive loop runs; with only the `w.gm` guard the walk would stop at exit 18 instead of
// resuming.
func TestRaidWalkNoGMReviveNearWipeSendsNoRevive(t *testing.T) {
	var resumed bool
	var pausedAt time.Time
	f, _ := nearWipeRevive(t, func(f *fixture) {
		f.opts.noGMRevive = true
		// Keep 3 hires dead (the lead and the rest alive): the near-wipe stops with some alive, so the dead-hire
		// revive branch is the one under test.
		prev := f.agent.onGM
		f.agent.onGM = func(line string) {
			prev(line)
			if !strings.HasPrefix(line, ".bot hire raid ") {
				return
			}
			f.agent.mu.Lock()
			for i, n := range f.agent.party {
				if i > 3 {
					f.agent.dead[n] = false
				}
			}
			f.agent.mu.Unlock()
		}
		// The run's living healers resurrect the dead hires while the recovery rests (the strict walk leaves them
		// to the run): the out-of-combat rest only goes quiet once nobody is dead.
		prevEvents := f.agent.eventsHook
		f.agent.eventsHook = func(served string) {
			if prevEvents != nil {
				prevEvents(served)
			}
			if pausedAt.IsZero() && strings.Contains(served, "3 near wipes within 10 min") {
				pausedAt = f.clock.now()
			}
		}
		f.agent.viewHook = func() {
			if pausedAt.IsZero() || f.clock.now().Sub(pausedAt) < 20*time.Second {
				return
			}
			f.agent.mu.Lock()
			for i, n := range f.agent.party {
				if i >= 1 && i <= 3 {
					f.agent.dead[n] = false
				}
			}
			f.agent.mu.Unlock()
		}
		prevResume := f.agent.onResume
		f.agent.onResume = func() {
			resumed = true
			prevResume()
		}
	})
	gm := f.agent.gmLines()
	if n := countPrefix(gm, ".revive "); n != 0 {
		t.Fatalf("%d .revive lines under --no-gm-revive, want none: %q", n, gm)
	}
	if n := countPrefix(gm, ".summon "); n != 0 {
		t.Fatalf("%d .summon lines under --no-gm-revive, want none: %q", n, gm)
	}
	if !resumed {
		t.Fatal("the run was not resumed: a near-wipe with someone alive must rest and resume")
	}
	sum := f.summary()
	if !sum.NoGMRevive {
		t.Fatalf("summary.no_gm_revive false, want true")
	}
	if !hasNote(sum, "near-wipe: --no-gm-revive: 3 dead hire(s) left to the run (no GM revive)") {
		t.Fatalf("notes %q, want the near-wipe strict note", sum.Notes)
	}
	if sum.WipeResumes != 1 || sum.EndReason != "until" {
		t.Fatalf("wipe_resumes %d, end %q: want 1 and the until", sum.WipeResumes, sum.EndReason)
	}
}

// TestRaidWalkNoGMReviveWipeEndsWithoutRevive: the T45 ending. A wipe with no "regrouping by itself" line has no
// corpse run, so a strict walk never recovers it: no reset-and-revive recipe, exit 19, and the reason says why.
func TestRaidWalkNoGMReviveWipeEndsWithoutRevive(t *testing.T) {
	f := newFixture(t)
	f.opts.noGMRevive = true
	f.opts.wipeResume = 3
	f.agent.startReply = startAt("onyxias_lair", 13)
	retirePack0Patrol(f)
	f.agent.unitsFn = func(int) []objects.Unit { return nil }
	f.allDeadFromHires()
	if code := f.run(context.Background()); code != exitRaidWalkIncident {
		t.Fatalf("exit %d, want 19: %s\n%s", code, f.stderr.String(), f.stdout.String())
	}
	gm := f.agent.gmLines()
	if n := countPrefix(gm, ".revive "); n != 0 {
		t.Fatalf("%d .revive lines under --no-gm-revive, want none: %q", n, gm)
	}
	if n := countPrefix(gm, ".summon "); n != 0 {
		t.Fatalf("%d .summon lines under --no-gm-revive, want none: %q", n, gm)
	}
	sum := f.summary()
	if !strings.Contains(sum.EndReason, "no GM revive") {
		t.Fatalf("end %q, want the strict wipe ending", sum.EndReason)
	}
	if !hasNote(sum, "wipe at step 13: --no-gm-revive: no GM revive, the walk ends") {
		t.Fatalf("notes %q, want the strict wipe note", sum.Notes)
	}
	if sum.WipeResumes != 0 {
		t.Fatalf("wipe_resumes %d, want 0 (nothing revived or resumed)", sum.WipeResumes)
	}
}

// TestRaidWalkNoGMReviveSelfRegroupWaitsForCorpseRun: with the run regrouping by itself, the run's own corpse run is
// the strict recovery. The walk sends nothing and polls the frames; once the hires are up again the ordinary
// self-regroup flow goes on and the wipe-resume is counted.
func TestRaidWalkNoGMReviveSelfRegroupWaitsForCorpseRun(t *testing.T) {
	var wipeAt time.Time
	gmAtWipe := -1
	f, _ := selfRegroupWipeWith(t, nil, func(f *fixture) {
		f.opts.noGMRevive = true
		// Remember the regroup line's arrival: every GM line after it belongs to the wipe (the portal and hire
		// lines precede it), and a strict walk must send none of the revive recipe's.
		prevEvents := f.agent.eventsHook
		f.agent.eventsHook = func(served string) {
			if prevEvents != nil {
				prevEvents(served)
			}
			if wipeAt.IsZero() && strings.Contains(served, "regrouping by itself") {
				wipeAt = f.clock.now()
				f.agent.mu.Lock()
				gmAtWipe = len(f.agent.gm)
				f.agent.mu.Unlock()
			}
		}
		// The corpse run: the hires show alive by themselves on the third read after the wipe.
		var polls int
		f.agent.viewHook = func() {
			f.agent.mu.Lock()
			defer f.agent.mu.Unlock()
			if len(f.agent.party) == 0 {
				return
			}
			for _, n := range f.agent.party {
				if !f.agent.dead[n] {
					return // not the wiped state: nothing to bring back
				}
			}
			polls++
			if polls < 3 {
				return
			}
			if polls > 3 {
				return
			}
			for _, n := range f.agent.party {
				f.agent.dead[n] = false
			}
			f.agent.lines = append(f.agent.lines,
				queuedEvent{raw: "TBM:RUN|molten_core|done|1/1|lucifron|move|walk|stopped before step 1 (until step 1): the party stays where it stands"})
		}
	})
	gm := f.agent.gmLines()
	if gmAtWipe < 0 {
		t.Fatalf("the regroup line was never served: no wipe to measure from (gm %q)", gm)
	}
	for _, line := range gm[gmAtWipe:] {
		for _, prefix := range []string{".revive ", ".summon ", ".go xyz"} {
			if strings.HasPrefix(line, prefix) {
				t.Fatalf("%q after the wipe under --no-gm-revive: the corpse run must send nothing: %q", line, gm)
			}
		}
	}
	sum := f.summary()
	if !sum.NoGMRevive {
		t.Fatalf("summary.no_gm_revive false, want true")
	}
	if sum.WipeResumes != 1 {
		t.Fatalf("wipe_resumes %d, want 1", sum.WipeResumes)
	}
	if !hasNote(sum, "--no-gm-revive: the run's corpse run brought the raid back (no GM revive)") {
		t.Fatalf("notes %q, want the corpse-run note", sum.Notes)
	}
}

// TestRaidWalkNoGMReviveSelfRegroupTimesOut: the corpse run never brings the raid back. The strict walk keeps sending
// nothing and ends the existing recovery-timeout way (exit 19).
func TestRaidWalkNoGMReviveSelfRegroupTimesOut(t *testing.T) {
	f := newFixture(t)
	f.opts.noGMRevive = true
	f.opts.wipeResume = 2
	f.agent.startReply = startAt("onyxias_lair", 10)
	f.agent.unitsFn = func(int) []objects.Unit { return nil }
	selfRegroupKill(f, "TBM:RUN|onyxias_lair|running|10/21|onyxia|move|walk|19 dead (1 wipe): regrouping by itself (1 of 2 at this step)")
	if code := f.run(context.Background()); code != exitRaidWalkIncident {
		t.Fatalf("exit %d, want 19: %s\n%s", code, f.stderr.String(), f.stdout.String())
	}
	gm := f.agent.gmLines()
	if n := countPrefix(gm, ".revive "); n != 0 {
		t.Fatalf("%d .revive lines under --no-gm-revive, want none: %q", n, gm)
	}
	if n := countPrefix(gm, ".summon "); n != 0 {
		t.Fatalf("%d .summon lines under --no-gm-revive, want none: %q", n, gm)
	}
	sum := f.summary()
	if sum.WipeResumes != 0 {
		t.Fatalf("wipe_resumes %d, want 0 (the corpse run never brought the raid back)", sum.WipeResumes)
	}
	if !hasNote(sum, "wipe recovery timed out") {
		t.Fatalf("notes %q, want the recovery-timeout note", sum.Notes)
	}
	if !strings.Contains(sum.EndReason, "wipe at step") {
		t.Fatalf("end %q, want the wipe ending", sum.EndReason)
	}
}

// TestRaidWalkNoGMReviveWipeEndFastLeavesDead: --wipe-end fast at a boss row still skips the reset wait (the copy is
// discarded), and under the flag its bare revive is skipped too: the dead hires stay dead.
func TestRaidWalkNoGMReviveWipeEndFastLeavesDead(t *testing.T) {
	f := newFixture(t)
	f.opts.noGMRevive = true
	f.opts.wipeResume = 0
	f.opts.wipeEndFast = true
	f.agent.startReply = startAt("onyxias_lair", 20) // step 21 is the boss row
	retirePack0Patrol(f)
	f.agent.unitsFn = func(int) []objects.Unit { return nil }
	f.allDeadFromHires()
	if code := f.run(context.Background()); code != exitRaidWalkIncident {
		t.Fatalf("exit %d, want 19: %s\n%s", code, f.stderr.String(), f.stdout.String())
	}
	gm := f.agent.gmLines()
	if n := countPrefix(gm, ".revive "); n != 0 {
		t.Fatalf("%d .revive lines under --no-gm-revive, want none: %q", n, gm)
	}
	for _, line := range gm {
		if strings.HasPrefix(line, ".summon ") || strings.HasPrefix(line, ".gm visible off") {
			t.Fatalf("the revive recipe ran under --no-gm-revive: %q", gm)
		}
	}
	sum := f.summary()
	if sum.WipeResumes != 0 {
		t.Fatalf("wipe_resumes %d, want 0", sum.WipeResumes)
	}
	if !hasNote(sum, "--no-gm-revive: the dead hires are left dead") {
		t.Fatalf("notes %q, want the fast-wipe strict note", sum.Notes)
	}
	if !strings.Contains(sum.EndReason, "no GM revive") {
		t.Fatalf("end %q, want the strict wipe ending", sum.EndReason)
	}
}

// TestRaidWalkGMRefusesReviveUnderNoGMRevive is the fail-closed guard: a `.revive` that reaches w.gm under the flag is
// refused with exit 18 and never sent, whatever case or spacing it has.
func TestRaidWalkGMRefusesReviveUnderNoGMRevive(t *testing.T) {
	a := newFakeAgent(t, newFakeClock())
	w := &walk{
		plan:  &dungeon.Plan{Map: 309},
		env:   walkEnv{now: a.clock.now, sleep: a.clock.sleep},
		agent: a,
		mt:    "Tank",
		hires: map[string]bool{"Tank": true},
		opts:  raidWalkOptions{noGMRevive: true},
	}
	for _, line := range []string{".revive Tank", "  .REVIVE Tank", ".Revive Tank extra"} {
		err := w.gm(context.Background(), line)
		if err == nil {
			t.Fatalf("%q was sent under --no-gm-revive", line)
		}
		code, reason := stopInfo(err)
		if code != exitRaidWalkFailed || !strings.Contains(reason, "--no-gm-revive") {
			t.Fatalf("%q: stop %d %q, want exit %d naming the flag", line, code, reason, exitRaidWalkFailed)
		}
	}
	if gm := a.gmLines(); len(gm) != 0 {
		t.Fatalf("refused lines were logged as sent: %q", gm)
	}
	// The guard is narrow: every other GM line still goes out.
	if err := w.gm(context.Background(), ".bot run stop"); err != nil {
		t.Fatalf(".bot run stop refused: %v", err)
	}
}

// TestRaidWalkNoGMReviveImpliesNoPlaceRevive: the flag turns the placement's revive off at parse time; the placement's
// own fast fail does not depend on it.
func TestRaidWalkNoGMReviveImpliesNoPlaceRevive(t *testing.T) {
	o, code := parseRaidWalkArgs([]string{"onyxias_lair", "--no-gm-revive"}, io.Discard)
	if code != exitOK {
		t.Fatalf("parse exit %d, want 0", code)
	}
	if !o.noGMRevive {
		t.Fatal("--no-gm-revive did not set noGMRevive")
	}
	if !o.noPlaceRevive {
		t.Fatal("--no-gm-revive did not imply --no-place-revive")
	}
	plain, code := parseRaidWalkArgs([]string{"onyxias_lair"}, io.Discard)
	if code != exitOK {
		t.Fatalf("parse exit %d, want 0", code)
	}
	if plain.noGMRevive || plain.noPlaceRevive {
		t.Fatalf("flag off: noGMRevive %v noPlaceRevive %v, want both false", plain.noGMRevive, plain.noPlaceRevive)
	}
}

// TestRaidWalkNoGMReviveDryRunPrintsIt: --dry-run says what a strict walk does (and that no `.revive` goes out).
func TestRaidWalkNoGMReviveDryRunPrintsIt(t *testing.T) {
	f := newFixture(t)
	f.opts.dryRun = true
	f.opts.noGMRevive = true
	f.opts.decisions = false
	if code := f.run(context.Background()); code != exitOK {
		t.Fatalf("dry run exit %d: %s", code, f.stderr.String())
	}
	want := "strict: --no-gm-revive: no .revive is sent; a wipe recovers only by the run's own corpse run (self-regroup), else the walk ends; a near-wipe rests and resumes without reviving; --no-place-revive is on\n"
	if !strings.Contains(f.stdout.String(), want) {
		t.Fatalf("dry-run plan lacks %q:\n%s", want, f.stdout.String())
	}
}
