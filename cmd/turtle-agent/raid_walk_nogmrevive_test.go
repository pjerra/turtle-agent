package main

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/pjerra/turtle-agent/internal/dungeon"
	"github.com/pjerra/turtle-agent/internal/objects"
	"github.com/pjerra/turtle-agent/internal/packet"
	"github.com/pjerra/turtle-agent/internal/raidwalk"
	"github.com/pjerra/turtle-agent/internal/tbm"
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
	want := "strict: --no-gm-revive: no .revive is sent; a wipe recovers only by the run's own corpse run (self-regroup), else the walk ends; a near-wipe rests and resumes without reviving, and a hire still dead when the rest's window ends the walk; --no-place-revive is on; --practice is refused\n"
	if !strings.Contains(f.stdout.String(), want) {
		t.Fatalf("dry-run plan lacks %q:\n%s", want, f.stdout.String())
	}
}

// TestRaidWalkNoGMReviveRefusesPractice is the first layer of the T45 review 1 fix: strict and practice are
// opposites, because a practice cycle's sustain revives the dead hires through gm_revive. The parse refuses the
// pair with exit 2 before any slot, lease or GM line.
func TestRaidWalkNoGMReviveRefusesPractice(t *testing.T) {
	for _, args := range [][]string{
		{"onyxias_lair", "--no-gm-revive", "--practice", "14834", "--place", "--from", "onyxia"},
		{"onyxias_lair", "--practice", "14834", "--place", "--from", "onyxia", "--no-gm-revive"},
		{"onyxias_lair", "--practice=14834", "--place", "--from", "onyxia", "--no-gm-revive"},
	} {
		var stderr bytes.Buffer
		if _, code := parseRaidWalkArgs(args, &stderr); code != exitUsage {
			t.Fatalf("%q: exit %d, want %d: %s", args, code, exitUsage, stderr.String())
		}
		line := stderr.String()
		if !strings.Contains(line, "--no-gm-revive") || !strings.Contains(line, "--practice") {
			t.Fatalf("%q: stderr %q, want a line naming both flags", args, line)
		}
	}
	// Each flag on its own still parses: the refusal is the pair, not the strict flag.
	if _, code := parseRaidWalkArgs([]string{"onyxias_lair", "--no-gm-revive"}, io.Discard); code != exitOK {
		t.Fatalf("--no-gm-revive alone: exit %d, want 0", code)
	}
}

// TestPracticeGMReviveRefusedUnderNoGMRevive is the defence in depth: the practice RPC itself refuses gm_revive
// under the flag, so even a practice loop started past the parse guard sends nothing.
func TestPracticeGMReviveRefusedUnderNoGMRevive(t *testing.T) {
	a := newFakeAgent(t, newFakeClock())
	w := &walk{
		env:   walkEnv{now: a.clock.now, sleep: a.clock.sleep},
		agent: a,
		opts:  raidWalkOptions{noGMRevive: true, server: "m910q"},
	}
	err := w.practiceGMRevive(context.Background(), "Bot02")
	if err == nil {
		t.Fatal("practiceGMRevive sent gm_revive under --no-gm-revive")
	}
	if code, reason := stopInfo(err); code != exitRaidWalkFailed || !strings.Contains(reason, "--no-gm-revive") {
		t.Fatalf("stop %d %q, want exit %d naming the flag", code, reason, exitRaidWalkFailed)
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	for _, entry := range a.log {
		if strings.HasPrefix(entry, "gm_revive") {
			t.Fatalf("%q was sent: the refused revive must send nothing", entry)
		}
	}
	if len(a.log) != 0 {
		t.Fatalf("RPCs went out for a refused revive: %q", a.log)
	}
}

// TestWaitCorpseRunPartialFrameKeepsWaiting is the review 1 fix for waitCorpseRun: a hire missing from the frame is
// unread, not alive. A partial frame with none dead must not finish the wait; only the frame that reads every hire
// and shows none dead does. The timeout's note counts the unread apart from the dead.
func TestWaitCorpseRunPartialFrameKeepsWaiting(t *testing.T) {
	clock := newFakeClock()
	a := newFakeAgent(t, clock)
	a.mu.Lock()
	a.party = []string{"Bot01", "Bot02", "Bot03"}
	a.mu.Unlock()
	a.viewFn = func(int) (bool, string) { return false, "Bot03" } // Bot03 is not in the frame; nobody is dead
	w := &walk{
		env:   walkEnv{now: clock.now, sleep: clock.sleep},
		agent: a,
		mt:    "Bot01", ots: []string{"Bot02"}, rest: []string{"Bot03"},
		botAt:    map[string]botPos{},
		partyG:   map[uint64]bool{},
		hostiles: map[string]hostileState{},
		opts:     raidWalkOptions{wipeRest: 10 * time.Minute},
	}
	start := clock.now()
	const window = 15 * time.Second
	err := w.waitCorpseRun(context.Background(), start.Add(window))
	timeout, ok := err.(*recoveryTimeout)
	if !ok {
		t.Fatalf("waitCorpseRun returned %v (%T), want the recovery timeout: a partial frame must keep the wait going", err, err)
	}
	if elapsed := clock.now().Sub(start); elapsed < window {
		t.Fatalf("the wait finished after %s, want at least %s (it never saw every hire)", elapsed, window)
	}
	note := timeout.end(7).(*timeoutWalkStop).note
	if !strings.Contains(note, "0 hire(s) still dead, 1 not in the frame (unread)") {
		t.Fatalf("timeout note %q, want the unread hire counted apart from the dead", note)
	}
}

// TestRaidWalkNoGMReviveNearWipeDeadLeftEndsPromptly is the second review 1 fix: under --no-gm-revive the near-wipe
// rest can never go quiet while a hire is dead, so once it has waited its window with a hire still dead the walk ends
// at once (exit 19) with the strict reason, instead of waiting out the recovery's long deadline (3 x the rest).
func TestRaidWalkNoGMReviveNearWipeDeadLeftEndsPromptly(t *testing.T) {
	f := newFixture(t)
	f.opts.noGMRevive = true
	f.opts.reviveAtRaid = true
	f.opts.wipeResume = 3
	f.agent.startReply = startAt("onyxias_lair", 10)
	f.agent.unitsFn = func(int) []objects.Unit { return []objects.Unit{} }
	var pausedAt, lastViewAt time.Time
	prevEvents := f.agent.eventsHook
	f.agent.eventsHook = func(served string) {
		if prevEvents != nil {
			prevEvents(served)
		}
		if pausedAt.IsZero() && strings.Contains(served, "3 near wipes within 10 min") {
			pausedAt = f.clock.now()
		}
	}
	prevView := f.agent.viewHook
	f.agent.viewHook = func() {
		if prevView != nil {
			prevView()
		}
		lastViewAt = f.clock.now()
	}
	f.agent.mu.Lock()
	f.agent.startQueue = []queuedEvent{
		{raw: "TBM:RUN|onyxias_lair|paused|10/29|onyxia|move|walk|19 dead: 3 near wipes within 10 min (54): the run stops here. Regroup (c...)"},
	}
	f.agent.mu.Unlock()
	a := [3]float32{100, 100, 50}
	f.agent.onGM = func(line string) {
		if !strings.HasPrefix(line, ".bot hire raid ") {
			return
		}
		f.agent.mu.Lock()
		defer f.agent.mu.Unlock()
		if len(f.agent.party) < 39 {
			return
		}
		if f.agent.pos == nil {
			f.agent.pos = map[string][3]float32{}
		}
		for i, n := range f.agent.party {
			if i == 0 {
				f.agent.pos[n] = a
				continue
			}
			f.agent.dead[n] = true
			f.agent.pos[n] = [3]float32{a[0] + reviveFar, a[1], a[2]}
		}
	}
	// The living healers never bring the dead hires back: every frame shows them dead to the end.
	if code := f.run(context.Background()); code != exitRaidWalkIncident {
		t.Fatalf("exit %d, want 19: %s\n%s", code, f.stderr.String(), f.stdout.String())
	}
	if pausedAt.IsZero() {
		t.Fatal("the near-wipe pause was never served")
	}
	f.agent.mu.Lock()
	dead := len(f.agent.party) - 1
	f.agent.mu.Unlock()
	sum := f.summary()
	want := fmt.Sprintf("near-wipe at step 10: %d hire(s) still dead, no GM revive (--no-gm-revive)", dead)
	if sum.EndReason != want {
		t.Fatalf("end %q, want %q", sum.EndReason, want)
	}
	if !hasNote(sum, fmt.Sprintf("near-wipe rest at step 10: --no-gm-revive: %d hire(s) still dead after 10m, no GM revive: the walk ends", dead)) {
		t.Fatalf("notes %q, want the strict rest note", sum.Notes)
	}
	gm := f.agent.gmLines()
	if n := countPrefix(gm, ".revive "); n != 0 {
		t.Fatalf("%d .revive lines under --no-gm-revive, want none: %q", n, gm)
	}
	// The ending is the corpse-run window (the rest's own 10m), not the recovery's long timeout (3 x the rest).
	waited := lastViewAt.Sub(pausedAt)
	if waited < f.opts.wipeRest {
		t.Fatalf("the rest ended %s after the pause, want at least the %s window", waited, f.opts.wipeRest)
	}
	if waited >= 3*f.opts.wipeRest {
		t.Fatalf("the rest ended %s after the pause, want under the long %s timeout", waited, 3*f.opts.wipeRest)
	}
}

// TestHandleBossWipeNoGMReviveSendsNoRevive is review 2's agy HIGH: handleBoss's boss-wipe reset still called
// w.revive, so under --no-gm-revive the revive recipe sent its `.go xyz` and then died in the w.gm guard (exit 18).
// A boss wipe the walk owns must take the wipe's strict path: nothing goes out, the reset is waited for and the walk
// ends 19 with the no-GM reason.
func TestHandleBossWipeNoGMReviveSendsNoRevive(t *testing.T) {
	f := newFixture(t)
	f.opts.noGMRevive = true
	f.agent.startReply = startAt("onyxias_lair", 10)
	// The boss is engaged first (a target), then it resets: the frame row is back at 100 % with no target, and the
	// boss path's reset branch is the one under test.
	f.agent.hostilesFn = func(n int) []tbm.HostileRec {
		targeted := n < 8
		f.agent.mu.Lock()
		f.agent.targeted = targeted
		f.agent.mu.Unlock()
		if targeted {
			return []tbm.HostileRec{{GUID: 9, Entry: 10184, HPPct: 100, Target: packet.GUID(1000)}}
		}
		return []tbm.HostileRec{{GUID: 9, Entry: 10184, HPPct: 100}}
	}
	retirePack0Patrol(f)
	f.allDeadFromHires()
	// The boss path's own run stop is where the boss wipe begins: every GM line after it belongs to the wipe (the
	// placement's entrance `.go xyz` and the hire lines precede it), and none of them may be the revive recipe's.
	stopAt := -1
	hire := f.agent.onGM
	f.agent.onGM = func(line string) {
		if hire != nil {
			hire(line)
		}
		if stopAt < 0 && strings.HasPrefix(line, ".bot run stop") {
			stopAt = len(f.agent.gmLines())
		}
	}
	if code := f.run(context.Background()); code != exitRaidWalkIncident {
		t.Fatalf("exit %d, want 19: %s\n%s", code, f.stderr.String(), f.stdout.String())
	}
	gm := f.agent.gmLines()
	if stopAt < 0 {
		t.Fatalf("the boss path never stopped the run: no wipe to measure from (gm %q)", gm)
	}
	for _, line := range gm[stopAt:] {
		for _, prefix := range []string{".revive ", ".summon ", ".go xyz", ".gm visible off"} {
			if strings.HasPrefix(line, prefix) {
				t.Fatalf("%q went out under --no-gm-revive: the boss wipe must send nothing of the revive recipe: %q", line, gm)
			}
		}
	}
	sum := f.summary()
	if !strings.Contains(sum.EndReason, "no GM revive") {
		t.Fatalf("end %q, want the strict wipe ending (notes %q)", sum.EndReason, sum.Notes)
	}
	if !hasNote(sum, "wipe at step 10: --no-gm-revive: no GM revive, the walk ends") {
		t.Fatalf("notes %q, want the strict wipe note", sum.Notes)
	}
	if sum.WipeResumes != 0 {
		t.Fatalf("wipe_resumes %d, want 0 (a strict walk recovers nothing here)", sum.WipeResumes)
	}
}

// TestWaitCorpseRunTransientViewErrorRetries is review 2's Codex/agy MEDIUM: one failed bot_view read ended the whole
// corpse-run wait (exit 19). A failed or empty read is unread, not a verdict: the wait polls again and finishes on
// the clean frame that reads every hire alive.
func TestWaitCorpseRunTransientViewErrorRetries(t *testing.T) {
	clock := newFakeClock()
	a := newFakeAgent(t, clock)
	a.mu.Lock()
	a.party = []string{"Bot01", "Bot02"}
	a.mu.Unlock()
	fails := 1
	a.viewFn = func(int) (bool, string) {
		if fails > 0 {
			fails--
			return true, ""
		}
		return false, ""
	}
	w := &walk{
		env:   walkEnv{now: clock.now, sleep: clock.sleep},
		agent: a,
		mt:    "Bot01", ots: []string{"Bot02"},
		botAt:    map[string]botPos{},
		partyG:   map[uint64]bool{},
		hostiles: map[string]hostileState{},
		opts:     raidWalkOptions{wipeRest: 10 * time.Minute},
	}
	start := clock.now()
	if err := w.waitCorpseRun(context.Background(), start.Add(60*time.Second)); err != nil {
		t.Fatalf("waitCorpseRun returned %v, want nil: one failed read must not end the wait", err)
	}
	if got := clock.now().Sub(start); got < corpseRunPoll {
		t.Fatalf("the wait finished after %s, want it to poll again after the %s poll", got, corpseRunPoll)
	}
}

// TestWaitCorpseRunNotesOnceNotPerPoll is review 2's agy MEDIUM: the wait noted every 5 s poll into summary.notes.
// The summary gets one note when the wait starts and one when it ends; the polls' lines are run.log's.
func TestWaitCorpseRunNotesOnceNotPerPoll(t *testing.T) {
	clock := newFakeClock()
	a := newFakeAgent(t, clock)
	a.mu.Lock()
	a.party = []string{"Bot01"}
	a.dead["Bot01"] = true
	a.mu.Unlock()
	polls := 0
	a.viewHook = func() {
		a.mu.Lock()
		defer a.mu.Unlock()
		polls++
		if polls >= 6 {
			a.dead["Bot01"] = false // the run's corpse run brings the hire back on the sixth read
		}
	}
	w := &walk{
		env:      walkEnv{now: clock.now, sleep: clock.sleep},
		agent:    a,
		mt:       "Bot01",
		botAt:    map[string]botPos{},
		partyG:   map[uint64]bool{},
		hostiles: map[string]hostileState{},
		opts:     raidWalkOptions{wipeRest: 10 * time.Minute},
	}
	start := clock.now()
	if err := w.waitCorpseRun(context.Background(), start.Add(60*time.Second)); err != nil {
		t.Fatalf("waitCorpseRun returned %v, want nil", err)
	}
	if polls < 6 {
		t.Fatalf("the wait read %d frames, want at least 6", polls)
	}
	w.outMu.Lock()
	notes := append([]string(nil), w.notes...)
	w.outMu.Unlock()
	if len(notes) != 2 {
		t.Fatalf("%d notes after a 6-poll wait, want 2 (start and end): %q", len(notes), notes)
	}
	if !strings.Contains(notes[0], "corpse run (--no-gm-revive)") || !strings.Contains(notes[1], "brought the raid back") {
		t.Fatalf("notes %q, want the wait's start and end", notes)
	}
}

// TestNearWipeStaleDeadRowNotCounted is review 2's Codex MEDIUM: completeQuietView counted a party row dead without
// checking the botAt observation's age, so a stale frame ended a valid run (exit 19). A dead row needs a fresh
// observation (10 s, the quiet rule's own freshness); a stale one is unread, and the fresh control row still counts.
func TestNearWipeStaleDeadRowNotCounted(t *testing.T) {
	clock := newFakeClock()
	a := newFakeAgent(t, clock)
	a.mu.Lock()
	a.party = []string{"Bot01"}
	a.dead["Bot01"] = true
	a.frameAt = clock.now().Add(-11 * time.Second) // the frame's own stamp: older than the 10 s freshness
	a.mu.Unlock()
	plan := &dungeon.Plan{Map: 249}
	w := &walk{
		env:      walkEnv{now: clock.now, sleep: clock.sleep},
		agent:    a,
		plan:     plan,
		tracker:  raidwalk.NewTracker(plan, []string{"Bot01"}),
		mt:       "Bot01",
		botAt:    map[string]botPos{},
		partyG:   map[uint64]bool{},
		hostiles: map[string]hostileState{},
		opts:     raidWalkOptions{wipeRest: 10 * time.Minute},
	}
	quiet, dead, unread := w.completeQuietView(context.Background())
	if dead != 0 || unread != 1 || quiet {
		t.Fatalf("completeQuietView = quiet %v dead %d unread %d, want false, 0 and 1: an 11 s old dead row is unread, not a death", quiet, dead, unread)
	}
	a.mu.Lock()
	a.frameAt = clock.now()
	a.mu.Unlock()
	quiet, dead, unread = w.completeQuietView(context.Background())
	if dead != 1 || unread != 0 || quiet {
		t.Fatalf("completeQuietView = quiet %v dead %d unread %d, want false, 1 and 0: a fresh dead row is a death", quiet, dead, unread)
	}
}

// strictRegroupLine10 is TB's own corpse-run regroup on the step the fixture walks on: the line handleWipe's
// self-regroup rule reads (and selfRegroupRunning for the boss path).
const strictRegroupLine10 = "TBM:RUN|onyxias_lair|running|10/21|onyxia|move|walk|19 dead (1 wipe): regrouping by itself (1 of 2 at this step)"

// strictCorpseRunNeverEnds is the fixture for a strict colony whose corpse run never brings the raid back: nobody is
// revived by the walk (it is strict), so every frame keeps the hires dead to the end.
func strictDeadForever(f *fixture) {
	f.opts.noGMRevive = true
	f.agent.startReply = startAt("onyxias_lair", 10)
	f.agent.unitsFn = func(int) []objects.Unit { return nil }
	retirePack0Patrol(f)
}

// TestStrictWipeCorpseRunTimeoutExits19WithNote is review 3's HIGH: strictWipeEnding returned waitCorpseRun's
// timeout as an unwrapped *timeoutWalkStop, which stopInfo reads as an unknown error: exit 18 and the note dropped.
// A strict corpse-run timeout ends exit 19 with its note on both paths that carry it — endWipe's ending and the boss
// wipe handleBoss owns.
func TestStrictWipeCorpseRunTimeoutExits19WithNote(t *testing.T) {
	t.Run("endWipe", func(t *testing.T) {
		f := newFixture(t)
		strictDeadForever(f)
		f.opts.wipeResume = 0 // no resume slots: the wipe takes endWipe, with TB's regroup running
		selfRegroupKill(f, strictRegroupLine10)
		var regroupAt time.Time
		prevEvents := f.agent.eventsHook
		f.agent.eventsHook = func(served string) {
			if prevEvents != nil {
				prevEvents(served)
			}
			if regroupAt.IsZero() && served == strictRegroupLine10 {
				regroupAt = f.clock.now()
			}
		}
		if code := f.run(context.Background()); code != exitRaidWalkIncident {
			t.Fatalf("exit %d, want 19 (the strict corpse-run timeout is a wipe ending): %s\n%s", code, f.stderr.String(), f.stdout.String())
		}
		sum := f.summary()
		if !hasNote(sum, "wipe recovery timed out") {
			t.Fatalf("notes %q, want the recovery-timeout note (it was dropped with the unwrapped timeout)", sum.Notes)
		}
		if !strings.Contains(sum.EndReason, "wipe at step 10") {
			t.Fatalf("end %q, want the wipe ending", sum.EndReason)
		}
		strictStopAfterWait(t, f, regroupAt)
	})
	t.Run("handleBoss", func(t *testing.T) {
		f := newFixture(t)
		strictDeadForever(f)
		// The boss is engaged first (a target), then it resets: handleBoss owns the wipe and its ending.
		f.agent.hostilesFn = func(n int) []tbm.HostileRec {
			targeted := n < 8
			f.agent.mu.Lock()
			f.agent.targeted = targeted
			f.agent.mu.Unlock()
			if targeted {
				return []tbm.HostileRec{{GUID: 9, Entry: 10184, HPPct: 100, Target: packet.GUID(1000)}}
			}
			return []tbm.HostileRec{{GUID: 9, Entry: 10184, HPPct: 100}}
		}
		// The run is regrouping by itself when the boss path takes over, and its corpse run never finishes. The line is
		// re-served so it stays inside the self-regroup window through the boss wait: the live run keeps reporting it.
		selfRegroupKill(f, strictRegroupLine10)
		prevView := f.agent.viewHook
		f.agent.viewHook = func() {
			if prevView != nil {
				prevView()
			}
			f.agent.mu.Lock()
			defer f.agent.mu.Unlock()
			if f.agent.viewN%5 == 0 {
				f.agent.lines = append(f.agent.lines, queuedEvent{raw: strictRegroupLine10})
			}
		}
		var regroupAt time.Time
		prevEvents := f.agent.eventsHook
		f.agent.eventsHook = func(served string) {
			if prevEvents != nil {
				prevEvents(served)
			}
			if regroupAt.IsZero() && served == strictRegroupLine10 {
				regroupAt = f.clock.now()
			}
		}
		if code := f.run(context.Background()); code != exitRaidWalkIncident {
			t.Fatalf("exit %d, want 19 (the strict corpse-run timeout is a wipe ending): %s\n%s", code, f.stderr.String(), f.stdout.String())
		}
		sum := f.summary()
		if !hasNote(sum, "wipe recovery timed out") {
			t.Fatalf("notes %q, want the recovery-timeout note (it was dropped with the unwrapped timeout)", sum.Notes)
		}
		if !strings.Contains(sum.EndReason, "wipe at step 10") {
			t.Fatalf("end %q, want the wipe ending", sum.EndReason)
		}
		gm := f.agent.gmLines()
		for _, line := range gm {
			for _, prefix := range []string{".revive ", ".summon ", ".gm visible off"} {
				if strings.HasPrefix(line, prefix) {
					t.Fatalf("%q went out under --no-gm-revive: %q", line, gm)
				}
			}
		}
		strictStopAfterWait(t, f, regroupAt)
	})
}

// strictStopAfterWait says the walk's own ending did not stop the run inside the corpse-run wait: w.stop stops the run
// for every incident ending, but a --no-gm-revive wipe that is waiting TB's regroup out must not kill it first.
func strictStopAfterWait(t *testing.T, f *fixture, regroupAt time.Time) {
	t.Helper()
	if regroupAt.IsZero() {
		t.Fatal("TB's regroup line was never served: no wipe to measure from")
	}
	gm := f.agent.gmLines()
	for i, line := range gm {
		if !strings.HasPrefix(line, ".bot run stop") {
			continue
		}
		if waited := f.agent.gmAt[i].Sub(regroupAt); waited < 3*f.opts.wipeRest {
			t.Fatalf("the run was stopped %s after TB's regroup line, inside the %s corpse-run wait: stopping it kills the only recovery a strict wipe has (gm %q)",
				waited, 3*f.opts.wipeRest, gm)
		}
	}
}

// TestEndWipeNoGMReviveWaitsForRunningSelfRegroup is review 3's MEDIUM: strictWipeEnding asked selfRegroupRunning
// after handleWipe had cleared w.runLines and endWipe had stopped the run, so the strict corpse-run wait never
// happened through endWipe. The fact is read before the clear and before stopRun: TB's own regroup is waited out,
// and it is not killed first.
func TestEndWipeNoGMReviveWaitsForRunningSelfRegroup(t *testing.T) {
	f := newFixture(t)
	strictDeadForever(f)
	f.opts.wipeResume = 0 // endWipe's strict branch, with the regroup line still in the run's observations
	selfRegroupKill(f, strictRegroupLine10)
	var regroupAt time.Time
	prevEvents := f.agent.eventsHook
	f.agent.eventsHook = func(served string) {
		if prevEvents != nil {
			prevEvents(served)
		}
		if regroupAt.IsZero() && served == strictRegroupLine10 {
			regroupAt = f.clock.now()
		}
	}
	if code := f.run(context.Background()); code != exitRaidWalkIncident {
		t.Fatalf("exit %d, want 19: %s\n%s", code, f.stderr.String(), f.stdout.String())
	}
	sum := f.summary()
	if !hasNote(sum, "wipe recovery timed out") {
		t.Fatalf("notes %q, want the corpse-run wait's timeout note: the strict ending skipped the wait it had a regroup for", sum.Notes)
	}
	strictStopAfterWait(t, f, regroupAt)
}
