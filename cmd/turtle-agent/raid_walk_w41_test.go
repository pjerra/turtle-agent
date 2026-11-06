// Walk 41 (TortoiseBots-audit research/zg-bosses/JEKLIK-W41.md §2/§3 fix 3): a boss that engages while the run is
// paused on its own boss row resumes the run instead of stopping it, and a dashboard boss fight that ended "kill"
// credits the goal boss even though the run had stopped.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/pjerra/turtle-agent/internal/dungeon"
	"github.com/pjerra/turtle-agent/internal/objects"
	"github.com/pjerra/turtle-agent/internal/packet"
	"github.com/pjerra/turtle-agent/internal/raidwalk"
	"github.com/pjerra/turtle-agent/internal/tbm"
)

// TestBossEngagedWhilePausedOnBossRowResumes is walk 41 tj-8. The run stood on Jeklik's own boss step and paused
// there when the pullback was refused ("no safe stand 35.0-25.0 yd from it"); the tank's own pull went out two
// seconds later and woke her under the pause. The walk used to read that as "boss engaged while paused" and send
// `.bot run stop` (exit 19), so the raid killed her without the run leader and the summary said not killed. The
// walk now sends `.bot run resume` and the run leader fights her: one resume, no run stop, the walk ends at its
// until.
func TestBossEngagedWhilePausedOnBossRowResumes(t *testing.T) {
	boss := zgBossStepOf(t, 14517)
	total := len(zgRows(t).Steps)
	f := newFixture(t)
	f.opts.plan, f.opts.holder = "zul_gurub", "raid-walk-zul_gurub"
	f.opts.until = fmt.Sprintf("step %d", boss+1)
	p, err := dungeon.Load(filepath.Join(f.opts.dir, "zul_gurub.json"))
	if err != nil {
		t.Fatal(err)
	}
	f.agent.instMap = p.Map
	start := fmt.Sprintf("TBM:RUN|zul_gurub|running|%d/%d|high_priestess_jeklik|move|begin|started|until step %d (step %d)", boss-1, total, boss+1, boss+1)
	f.agent.startReply = &commanderReply{Reply: tbm.Reply{Kind: tbm.KindAck, OK: true,
		Ack:  &tbm.Ack{Intent: "run start", Scope: "party", Count: 1, Executor: "-"},
		Line: start, Lines: []string{start, "TBM:ACTION_ACK|run start|party|1|-"}}}
	// The pause on her own row: walk 41 tj-8's 22:14:55.577 line.
	pause := fmt.Sprintf("TBM:RUN|zul_gurub|paused|%d/%d|high_priestess_jeklik|boss|begin|cmd pullback 17379391205577637935 refused: unreachable: The tank could not start moving to a firing position for the pull; the boss at -12289.1 -1384.1 134.3 is not reachable from this floor: no safe stand 35.0-25.0 yd from it (the lead bot 49.9 yd from it)|until step %d (step %d)", boss, total, boss+1, boss+1)
	f.agent.mu.Lock()
	f.agent.startQueue = []queuedEvent{{raw: pause}}
	f.agent.mu.Unlock()
	// Her own late pull: the frames hold her engaged (a target, 40 %) under the pause.
	f.agent.hostilesFn = func(n int) []tbm.HostileRec {
		return []tbm.HostileRec{{GUID: 9, Entry: 14517, HPPct: 40, Target: packet.GUID(1000), X: -12289.1, Y: -1384.1, Z: 134.3}}
	}
	// The resume's answer: the run goes on at her step (its own page, so the wait's
	// newest-status check sees running), then ends at its until on a later poll.
	f.agent.onResume = func() {
		f.agent.mu.Lock()
		defer f.agent.mu.Unlock()
		f.agent.lines = append(f.agent.lines,
			queuedEvent{raw: fmt.Sprintf("TBM:RUN|zul_gurub|running|%d/%d|high_priestess_jeklik|boss|fight|fighting|until step %d (step %d)", boss, total, boss+1, boss+1)})
		f.agent.viewHook = func() {
			f.agent.mu.Lock()
			defer f.agent.mu.Unlock()
			if len(f.agent.lines) == 0 {
				f.agent.lines = append(f.agent.lines,
					queuedEvent{raw: fmt.Sprintf("TBM:RUN|zul_gurub|done|%d/%d|high_priestess_jeklik|boss|fight|stopped before step %d (until step %d): the party stays where it stands", boss+1, total, boss+1, boss+1)})
			}
		}
	}
	if code := f.run(context.Background()); code != exitOK {
		t.Fatalf("exit %d, want 0: %s\n%s", code, f.stderr.String(), f.stdout.String())
	}
	f.agent.mu.Lock()
	verbs := append([]string{}, f.agent.runVerbs...)
	f.agent.mu.Unlock()
	if n := countPrefix(verbs, "resume"); n != 1 {
		t.Fatalf("run verbs %v: %d resumes, want 1 (the run paused on its boss row and the boss engaged)", verbs, n)
	}
	if n := countPrefix(f.agent.gmLines(), ".bot run stop"); n != 0 {
		t.Fatalf("%d run stops, want 0: the run leader fights its own boss\n%s", n, f.stdout.String())
	}
	if sum := f.summary(); !hasNote(sum, "resumed the run so the run leader fights it") {
		t.Fatalf("notes %q, want the resume note", sum.Notes)
	}
}

// TestFightRecordKillCountsAsBossKill is walk 41 tj-8/tj-9. The run stopped on Jeklik's boss step (exit 19) two
// seconds before the tank's late pull landed; the raid killed her 153.7 s later and the dashboard filed
// combat-fight-68-1.json outcome "kill" for step 50. The next walk's summary carried that fight row but said
// boss_kill null, so the autopilot relaunched walks for a dead boss. A kill record of the goal boss's own row now
// credits the summary's boss_kills as killed; a record of another boss's row does not.
func TestFightRecordKillCountsAsBossKill(t *testing.T) {
	p, err := dungeon.Load("../../dungeons/zul_gurub.json")
	if err != nil {
		t.Fatal(err)
	}
	goal := zgBossStepOf(t, 14517)
	other := zgBossStepOf(t, 14507)
	if other == goal {
		t.Fatal("the fixture rows give Jeklik and Venoxis the same step")
	}
	w := bossSummaryWalk(t)
	now := time.Now()
	w.env = walkEnv{now: func() time.Time { return now }}
	w.plan = p
	w.outDir = t.TempDir()
	w.stdout = io.Discard
	w.started, w.ended = now, now
	w.opts.until = fmt.Sprintf("step %d", goal+1)
	w.startStep, w.untilStep = goal, goal+1
	// The run stopped on her own row (walk 41's exit 19), so the frames never reported a kill.
	feedStatus(w, tbm.RunStatus{Step: goal, Kind: "boss", Note: "fighting"}, now)
	feedStatus(w, tbm.RunStatus{State: tbm.RunPaused, Step: goal, Kind: "boss", Note: "cmd pullback 17379391205577637935 refused: unreachable"}, now)
	w.fights = []raidwalk.FightSummary{
		{ID: "68-1", Step: goal, Leg: "high_priestess_jeklik", DurMS: 153656, Outcome: "kill", BossName: "High Priestess Jeklik", BossPhase: 1, BossPhaseEntry: 14517},
		{ID: "99-1", Step: other, Leg: "high_priest_venoxis", DurMS: 90000, Outcome: "kill", BossName: "High Priestess Venoxis", BossPhase: 1, BossPhaseEntry: 14507},
	}
	w.finish(exitRaidWalkIncident)
	raw, err := os.ReadFile(filepath.Join(w.outDir, "summary.json"))
	if err != nil {
		t.Fatal(err)
	}
	var sum raidwalk.Summary
	if err := json.Unmarshal(raw, &sum); err != nil {
		t.Fatal(err)
	}
	k := sum.BossKill
	if k == nil || k.Step != goal || k.Entry != 14517 || !k.Killed {
		t.Fatalf("boss_kill %+v, want step %d entry 14517 killed by the fight record", k, goal)
	}
	if want := "fight 68-1: High Priestess Jeklik killed (outcome kill)"; k.Evidence != want {
		t.Fatalf("boss_kill evidence %q, want %q", k.Evidence, want)
	}
	if len(sum.BossKills) != 1 || sum.BossKills[0].Step != goal || !sum.BossKills[0].Killed {
		t.Fatalf("boss_kills %+v, want only the goal boss's killed record", sum.BossKills)
	}
	// The other boss's kill record is no goal boss: no track, no credit.
	for _, b := range sum.BossKills {
		if b.Step == other {
			t.Fatalf("boss_kills %+v credits the non-goal boss at step %d", sum.BossKills, other)
		}
	}
}

// Codex HIGH: the watch loop must not trust the resume's ACK. The ACK only says the command arrived; the run can
// stay paused or pause again at once on the same pullback failure. Until a FRESH RunRunning status for the boss
// row arrives, the incident still routes through handleBoss (no unmanaged fight), within pausedBossResumeWait.
// A fresh RunRunning status inside the deadline hands the fight to the run leader (the existing handoff).

// pausedBossFixture is TestBossEngagedWhilePausedOnBossRowResumes with the resume's answer scripted by answer
// (nil: ACK only, the run never shows running again after the resume).
func pausedBossFixture(t *testing.T, answer func(f *fixture, boss, total int)) (*fixture, int, int) {
	t.Helper()
	boss := zgBossStepOf(t, 14517)
	total := len(zgRows(t).Steps)
	f := newFixture(t)
	f.opts.plan, f.opts.holder = "zul_gurub", "raid-walk-zul_gurub"
	f.opts.until = fmt.Sprintf("step %d", boss+1)
	f.opts.pausedWait = 4 * time.Second
	p, err := dungeon.Load(filepath.Join(f.opts.dir, "zul_gurub.json"))
	if err != nil {
		t.Fatal(err)
	}
	f.agent.instMap = p.Map
	f.agent.enterMap = p.Map
	start := fmt.Sprintf("TBM:RUN|zul_gurub|running|%d/%d|high_priestess_jeklik|move|begin|started|until step %d (step %d)", boss-1, total, boss+1, boss+1)
	f.agent.startReply = &commanderReply{Reply: tbm.Reply{Kind: tbm.KindAck, OK: true,
		Ack:  &tbm.Ack{Intent: "run start", Scope: "party", Count: 1, Executor: "-"},
		Line: start, Lines: []string{start, "TBM:ACTION_ACK|run start|party|1|-"}}}
	// The pause on her own row: walk 41 tj-8's 22:14:55.577 line.
	pause := fmt.Sprintf("TBM:RUN|zul_gurub|paused|%d/%d|high_priestess_jeklik|boss|begin|cmd pullback 17379391205577637935 refused: unreachable: The tank could not start moving to a firing position for the pull; the boss at -12289.1 -1384.1 134.3 is not reachable from this floor: no safe stand 35.0-25.0 yd from it (the lead bot 49.9 yd from it)|until step %d (step %d)", boss, total, boss+1, boss+1)
	f.agent.mu.Lock()
	f.agent.startQueue = []queuedEvent{{raw: pause}}
	f.agent.mu.Unlock()
	// Her own late pull: the frames hold her engaged (a target, 40 %) under the pause.
	f.agent.hostilesFn = func(n int) []tbm.HostileRec {
		return []tbm.HostileRec{{GUID: 9, Entry: 14517, HPPct: 40, Target: packet.GUID(1000), X: -12289.1, Y: -1384.1, Z: 134.3}}
	}
	if answer != nil {
		answer(f, boss, total)
	}
	return f, boss, total
}

// TestPausedBossResumeAckWithoutRunningStillStopsTheRun is the Codex HIGH case: the resume is ACKed but the run
// never shows running again, so the walk must route the engaged boss through handleBoss (exit 19 with the boss
// note) instead of handing an unmanaged fight to the run leader (exit 18 on the pause).
func TestPausedBossResumeAckWithoutRunningStillStopsTheRun(t *testing.T) {
	f, boss, total := pausedBossFixture(t, nil)
	_ = total
	code := f.run(context.Background())
	if code != exitRaidWalkIncident {
		t.Fatalf("exit %d, want 19: %s\n%s", code, f.stderr.String(), f.stdout.String())
	}
	f.agent.mu.Lock()
	verbs := append([]string{}, f.agent.runVerbs...)
	f.agent.mu.Unlock()
	if n := countPrefix(verbs, "resume"); n != 1 {
		t.Fatalf("run verbs %v: %d resumes, want 1 (the walk still tries the resume once)", verbs, n)
	}
	if n := countPrefix(f.agent.gmLines(), ".bot run stop"); n != 1 {
		t.Fatalf("%d run stops, want 1: handleBoss stops the run for the engaged boss (an ACK proves nothing)\n%s", n, f.stdout.String())
	}
	if sum := f.summary(); !strings.Contains(sum.EndReason, fmt.Sprintf("boss 14517 engaged at step %d", boss)) {
		t.Fatalf("end %q, want the handleBoss stop reason for the engaged boss (notes %q)", sum.EndReason, sum.Notes)
	}
	if hasNote(f.summary(), "resumed the run so the run leader fights it") {
		t.Fatalf("notes %q: the resume note claims a handoff the run never showed", f.summary().Notes)
	}
}

// TestPausedBossResumeSamePauseAgainRoutesToHandleBoss: the resume is ACKed, then the run pauses again at once on
// the SAME pause line (the pullback failure repeats), inside the wait. The re-paused incident must route through
// handleBoss at once (exit 19), not wait out pausedWait, and the walk must not send a second resume for the row.
func TestPausedBossResumeSamePauseAgainRoutesToHandleBoss(t *testing.T) {
	pauseNote := ""
	f, boss, total := pausedBossFixture(t, func(f *fixture, boss, total int) {
		f.agent.onResume = func() {
			f.agent.mu.Lock()
			defer f.agent.mu.Unlock()
			pauseNote = f.agent.history[len(f.agent.history)-1].raw
			f.agent.lines = append(f.agent.lines, queuedEvent{raw: pauseNote})
		}
	})
	_ = total
	code := f.run(context.Background())
	if code != exitRaidWalkIncident {
		t.Fatalf("exit %d, want 19: %s\n%s", code, f.stderr.String(), f.stdout.String())
	}
	f.agent.mu.Lock()
	verbs := append([]string{}, f.agent.runVerbs...)
	f.agent.mu.Unlock()
	if n := countPrefix(verbs, "resume"); n != 1 {
		t.Fatalf("run verbs %v: %d resumes, want 1 (no second resume for the same row)", verbs, n)
	}
	if n := countPrefix(f.agent.gmLines(), ".bot run stop"); n != 1 {
		t.Fatalf("%d run stops, want 1: handleBoss stops the run for the re-paused engage\n%s", n, f.stdout.String())
	}
	if sum := f.summary(); !strings.Contains(sum.EndReason, fmt.Sprintf("boss 14517 engaged at step %d", boss)) {
		t.Fatalf("end %q, want the handleBoss stop reason for the engaged boss (notes %q)", sum.EndReason, sum.Notes)
	}
	if sum := f.summary(); strings.Contains(sum.EndReason, "paused: ") {
		t.Fatalf("end %q: the re-paused run exits through handleBoss, not the generic pause wait", sum.EndReason)
	}
}

// TestPausedBossResumeFreshRunningKeepsTheHandoff pins the existing behaviour the brief keeps: the resume is ACKed
// and a FRESH RunRunning status for the boss row follows, so the run leader fights it (no handleBoss run stop, the
// walk ends at its until). The running line stands alone in its page: a done line in the same page is the newest
// status, and the handoff must not fire on the older line.
func TestPausedBossResumeFreshRunningKeepsTheHandoff(t *testing.T) {
	f, _, _ := pausedBossFixture(t, func(f *fixture, boss, total int) {
		f.agent.onResume = func() {
			f.agent.mu.Lock()
			defer f.agent.mu.Unlock()
			f.agent.lines = append(f.agent.lines,
				queuedEvent{raw: fmt.Sprintf("TBM:RUN|zul_gurub|running|%d/%d|high_priestess_jeklik|boss|fight|fighting|until step %d (step %d)", boss, total, boss+1, boss+1)})
			f.agent.viewHook = func() {
				f.agent.mu.Lock()
				defer f.agent.mu.Unlock()
				if len(f.agent.lines) == 0 {
					f.agent.lines = append(f.agent.lines,
						queuedEvent{raw: fmt.Sprintf("TBM:RUN|zul_gurub|done|%d/%d|high_priestess_jeklik|boss|fight|stopped before step %d (until step %d): the party stays where it stands", boss+1, total, boss+1, boss+1)})
				}
			}
		}
	})
	if code := f.run(context.Background()); code != exitOK {
		t.Fatalf("exit %d, want 0: %s\n%s", code, f.stderr.String(), f.stdout.String())
	}
	f.agent.mu.Lock()
	verbs := append([]string{}, f.agent.runVerbs...)
	f.agent.mu.Unlock()
	if n := countPrefix(verbs, "resume"); n != 1 {
		t.Fatalf("run verbs %v: %d resumes, want 1 (the walk resumes the run once)", verbs, n)
	}
	if n := countPrefix(f.agent.gmLines(), ".bot run stop"); n != 0 {
		t.Fatalf("%d run stops, want 0: the run leader fights its own boss\n%s", n, f.stdout.String())
	}
	if sum := f.summary(); !hasNote(sum, "resumed the run so the run leader fights it") {
		t.Fatalf("notes %q, want the resume note", sum.Notes)
	}
}

// TestPausedBossResumeRunningThenDoneInOnePageRoutesToHandleBoss is round-2 item 2: the wait
// succeeds only on the NEWEST accepted status. A running line of the boss row followed by a done
// line in ONE page (pageSize 2) while the boss stays engaged is a failed handoff: handleBoss runs
// (exit 19 with the boss note), and the resume handoff note never fires.
func TestPausedBossResumeRunningThenDoneInOnePageRoutesToHandleBoss(t *testing.T) {
	f, boss, total := pausedBossFixture(t, func(f *fixture, boss, total int) {
		f.agent.pageSize = 2
		f.agent.onResume = func() {
			f.agent.mu.Lock()
			defer f.agent.mu.Unlock()
			f.agent.lines = append(f.agent.lines,
				queuedEvent{raw: fmt.Sprintf("TBM:RUN|zul_gurub|running|%d/%d|high_priestess_jeklik|boss|fight|fighting|until step %d (step %d)", boss, total, boss+1, boss+1)},
				queuedEvent{raw: fmt.Sprintf("TBM:RUN|zul_gurub|done|%d/%d|high_priestess_jeklik|boss|fight|stopped before step %d (until step %d): the party stays where it stands", boss+1, total, boss+1, boss+1)})
		}
	})
	_ = total
	code := f.run(context.Background())
	if code != exitRaidWalkIncident {
		t.Fatalf("exit %d, want 19: %s\n%s", code, f.stderr.String(), f.stdout.String())
	}
	if sum := f.summary(); !strings.Contains(sum.EndReason, fmt.Sprintf("boss 14517 engaged at step %d", boss)) {
		t.Fatalf("end %q, want the handleBoss stop reason for the engaged boss (notes %q)", sum.EndReason, sum.Notes)
	}
	if hasNote(f.summary(), "resumed the run so the run leader fights it") {
		t.Fatalf("notes %q: a stale running line behind a done line must not hand off", f.summary().Notes)
	}
	if n := countPrefix(f.agent.gmLines(), ".bot run stop"); n != 1 {
		t.Fatalf("%d run stops, want 1: handleBoss stops the run for the engaged boss\n%s", n, f.stdout.String())
	}
}

// TestPausedBossResumeWipeInsideWaitRunsWipeRecovery is round-2 item 1: a wipe during the resume's
// 10 s wait is collected and fed to the normal handler exactly as phaseWatch would, before deciding
// the handoff. The wipe handler runs (the wipe note, no handoff note, no handleBoss ending): any
// wipe during the wait is a failed handoff.
func TestPausedBossResumeWipeInsideWaitRunsWipeRecovery(t *testing.T) {
	f, boss, total := pausedBossFixture(t, func(f *fixture, boss, total int) {
		f.agent.unitsFn = func(n int) []objects.Unit { return []objects.Unit{} }
		running := fmt.Sprintf("TBM:RUN|zul_gurub|running|%d/%d|high_priestess_jeklik|boss|fight|fighting|until step %d (step %d)", boss, total, boss+1, boss+1)
		// The resume's answer: the run goes on at her step — and the whole raid dies on that
		// same poll, so the wait drains both the running line and the wipe.
		f.agent.onResume = func() {
			f.agent.mu.Lock()
			defer f.agent.mu.Unlock()
			f.agent.lines = append(f.agent.lines, queuedEvent{raw: running})
			prev := f.agent.eventsHook
			f.agent.eventsHook = func(raw string) {
				if prev != nil {
					prev(raw)
				}
				if raw != running {
					return
				}
				for _, n := range f.agent.party {
					f.agent.dead[n] = true
				}
			}
		}
	})
	_ = total
	code := f.run(context.Background())
	if code != exitRaidWalkIncident {
		t.Fatalf("exit %d, want 19: %s\n%s", code, f.stderr.String(), f.stdout.String())
	}
	sum := f.summary()
	if hasNote(sum, "resumed the run so the run leader fights it") {
		t.Fatalf("notes %q: the wait's running line must not hand off when a wipe drained in the same wait", sum.Notes)
	}
	if !hasNote(sum, fmt.Sprintf("wipe at step %d", boss)) {
		t.Fatalf("notes %q, want the wipe ending (the drained wipe ran its normal handler)", sum.Notes)
	}
	if strings.Contains(sum.EndReason, fmt.Sprintf("boss 14517 engaged at step %d", boss)) {
		t.Fatalf("end %q: the drained wipe owns the ending, not handleBoss", sum.EndReason)
	}
}

// TestPausedBossResumeWipeRecoveredInsideWaitKeepsWatching is round 3 (Codex HIGH): a wipe drained
// inside the resume wait that RECOVERS (--wipe-resume; the run regroups by itself) must send the walk
// back to its watch loop. The pre-wipe boss-engaged incident is stale: handleBoss with it stops the
// freshly resumed run and ends the walk on the wrong encounter. The walk must run to its until
// instead (the done line after the recovery), with no run stop and no boss-incident ending.
func TestPausedBossResumeWipeRecoveredInsideWaitKeepsWatching(t *testing.T) {
	f, boss, _ := pausedBossFixture(t, func(f *fixture, boss, total int) {
		f.opts.wipeResume = 2
		f.opts.wipeRest = 5 * time.Minute
		f.agent.unitsFn = func(n int) []objects.Unit { return []objects.Unit{} }
		// The resume's answer: the run goes on at her step, and the whole raid dies on that same
		// poll. The line is the run's own self-regroup ("regrouping by itself", walk 17b), so the
		// drained wipe recovers without a rest and without a second resume.
		wipe := fmt.Sprintf("TBM:RUN|zul_gurub|running|%d/%d|high_priestess_jeklik|boss|fight|20 dead (1 wipe, 1 near wipe this run): regrouping by itself (1 of 2 at this step)|until step %d (step %d)", boss, total, boss+1, boss+1)
		f.agent.onResume = func() {
			f.agent.mu.Lock()
			defer f.agent.mu.Unlock()
			f.agent.lines = append(f.agent.lines, queuedEvent{raw: wipe})
			prev := f.agent.eventsHook
			f.agent.eventsHook = func(raw string) {
				if prev != nil {
					prev(raw)
				}
				if raw != wipe {
					return
				}
				for _, n := range f.agent.party {
					f.agent.dead[n] = true
				}
			}
			// Once the wipe line is served, the next page carries the run's own end at its until:
			// the watch loop must be there to read it.
			f.agent.viewHook = func() {
				f.agent.mu.Lock()
				defer f.agent.mu.Unlock()
				if len(f.agent.lines) == 0 {
					f.agent.lines = append(f.agent.lines,
						queuedEvent{raw: fmt.Sprintf("TBM:RUN|zul_gurub|done|%d/%d|high_priestess_jeklik|boss|fight|stopped before step %d (until step %d): the party stays where it stands", boss+1, total, boss+1, boss+1)})
				}
			}
		}
	})
	if code := f.run(context.Background()); code != exitOK {
		t.Fatalf("exit %d, want 0 (notes %v): %s\n%s", code, f.summaryNotes(), f.stderr.String(), f.stdout.String())
	}
	gm := f.agent.gmLines()
	if n := countPrefix(gm, ".bot run stop"); n != 0 {
		t.Fatalf("%d run stops, want 0: a wipe recovered inside the wait must not end on the stale boss incident (gm %q)", n, gm)
	}
	sum := f.summary()
	if sum.WipeResumes != 1 {
		t.Fatalf("wipe_resumes %d, want 1: the wait's wipe recovered (notes %q)", sum.WipeResumes, sum.Notes)
	}
	if !hasNote(sum, "the run regroups by itself") {
		t.Fatalf("notes %q, want the self-regroup recovery note", sum.Notes)
	}
	if hasNote(sum, "resumed the run so the run leader fights it") {
		t.Fatalf("notes %q: a recovered wipe is no handoff, the note must not fire", sum.Notes)
	}
	if strings.Contains(sum.EndReason, fmt.Sprintf("boss 14517 engaged at step %d", boss)) {
		t.Fatalf("end %q: the recovered wipe must return to the watch loop, not end on the pre-wipe incident", sum.EndReason)
	}
	if !strings.Contains(sum.EndReason, "until") || sum.StepsReached != boss+1 {
		t.Fatalf("end %q at step %d, want the run's own until ending after the recovery (step %d)", sum.EndReason, sum.StepsReached, boss+1)
	}
	f.agent.mu.Lock()
	verbs := append([]string{}, f.agent.runVerbs...)
	f.agent.mu.Unlock()
	if n := countPrefix(verbs, "resume"); n != 1 {
		t.Fatalf("%d resumes, want 1 (the wipe's recovery resumed nothing: the run regroups by itself)", n)
	}
}

// TestNearWipePauseOnBossRowSendsNoResume is round 4 item 1 (Opus HIGH): the paused-boss resume
// exists for ONE pause, the run leader's pullback refused unreachable ("cmd pullback <guid> refused:
// unreachable: ...", DungeonRunPolicy PullbackRefusedUnreachable's ApproachNoWay pause). A near-wipe
// stop on the same boss row ("... near wipes ... the run stops here") with the boss engaged must NOT
// send `.bot run resume` into the boss with a half-dead raid: it takes the old boss path (handleBoss
// stops the run and ends on the encounter).
func TestNearWipePauseOnBossRowSendsNoResume(t *testing.T) {
	f, boss, _ := pausedBossFixture(t, func(f *fixture, boss, total int) {
		nearWipe := fmt.Sprintf("TBM:RUN|zul_gurub|paused|%d/%d|high_priestess_jeklik|boss|begin|19 dead: 3 near wipes within 10 min (54): the run stops here. Regroup (c...)|until step %d (step %d)", boss, total, boss+1, boss+1)
		f.agent.mu.Lock()
		f.agent.startQueue = []queuedEvent{{raw: nearWipe}}
		f.agent.mu.Unlock()
	})
	code := f.run(context.Background())
	if code != exitRaidWalkIncident {
		t.Fatalf("exit %d, want 19: %s\n%s", code, f.stderr.String(), f.stdout.String())
	}
	f.agent.mu.Lock()
	verbs := append([]string{}, f.agent.runVerbs...)
	f.agent.mu.Unlock()
	if n := countPrefix(verbs, "resume"); n != 0 {
		t.Fatalf("run verbs %v: %d resumes, want 0 (a near-wipe stop is no pullback pause)", verbs, n)
	}
	if n := countPrefix(f.agent.gmLines(), ".bot run stop"); n != 1 {
		t.Fatalf("%d run stops, want 1: the old boss path owns the engaged boss\n%s", n, f.stdout.String())
	}
	sum := f.summary()
	if hasNote(sum, "resumed the run so the run leader fights it") {
		t.Fatalf("notes %q: the near-wipe pause was resumed", sum.Notes)
	}
	if !strings.Contains(sum.EndReason, fmt.Sprintf("boss 14517 engaged at step %d", boss)) {
		t.Fatalf("end %q, want the boss path's ending (notes %q)", sum.EndReason, sum.Notes)
	}
}

// TestPausedBossResumeUnderUntilNoneResumesTheRun is round 4 item 2 (Codex HIGH): --until none used
// to bypass the paused-boss resume entirely, leaving the pullback-paused run standing in an active
// boss fight until --paused-wait ended the walk. The same pullback-only resume applies there.
func TestPausedBossResumeUnderUntilNoneResumesTheRun(t *testing.T) {
	f, _, _ := pausedBossFixture(t, func(f *fixture, boss, total int) {
		f.opts.until = "none"
		f.agent.onResume = func() {
			f.agent.mu.Lock()
			defer f.agent.mu.Unlock()
			f.agent.lines = append(f.agent.lines, queuedEvent{raw: fmt.Sprintf("TBM:RUN|zul_gurub|running|%d/%d|high_priestess_jeklik|boss|fight|fighting", boss, total)})
			f.agent.viewHook = func() {
				f.agent.mu.Lock()
				defer f.agent.mu.Unlock()
				if len(f.agent.lines) == 0 {
					f.agent.lines = append(f.agent.lines,
						queuedEvent{raw: fmt.Sprintf("TBM:RUN|zul_gurub|done|%d/%d|high_priestess_jeklik|boss|fight|plan done (speed 1.0 (test); 0 wipes this run)", total, total)})
				}
			}
		}
	})
	if code := f.run(context.Background()); code != exitOK {
		t.Fatalf("exit %d, want 0 (notes %v): %s\n%s", code, f.summaryNotes(), f.stderr.String(), f.stdout.String())
	}
	f.agent.mu.Lock()
	verbs := append([]string{}, f.agent.runVerbs...)
	f.agent.mu.Unlock()
	if n := countPrefix(verbs, "resume"); n != 1 {
		t.Fatalf("run verbs %v: %d resumes, want 1 (the pullback pause on the boss row resumes under --until none)", verbs, n)
	}
	if n := countPrefix(f.agent.gmLines(), ".bot run stop"); n != 0 {
		t.Fatalf("%d run stops, want 0: the resumed run leader fights its own boss\n%s", n, f.stdout.String())
	}
	sum := f.summary()
	if !hasNote(sum, "resumed the run so the run leader fights it") {
		t.Fatalf("notes %q, want the resume handoff note", sum.Notes)
	}
	if sum.EndReason != "plan done" {
		t.Fatalf("end %q, want the run's own plan done", sum.EndReason)
	}
}

// TestPausedBossResumeUnderUntilNoneFailedResumeIsNoHandoff is round 5 (Codex HIGH): under --until none a resume
// whose wait ends without a fresh RunRunning (ACK only, the run stays paused) used to be reported as a successful
// boss handoff. It is no handoff: neither handoff note fires, no run stop (--until none never stops the run on a
// boss), and the old path owns the paused run: the watch loop's pause handling ends the walk on the pause.
func TestPausedBossResumeUnderUntilNoneFailedResumeIsNoHandoff(t *testing.T) {
	f, _, _ := pausedBossFixture(t, func(f *fixture, boss, total int) {
		f.opts.until = "none"
	})
	code := f.run(context.Background())
	sum := f.summary()
	f.agent.mu.Lock()
	verbs := append([]string{}, f.agent.runVerbs...)
	f.agent.mu.Unlock()
	if n := countPrefix(verbs, "resume"); n != 1 {
		t.Fatalf("run verbs %v: %d resumes, want 1", verbs, n)
	}
	if hasNote(sum, "resumed the run so the run leader fights it") || hasNote(sum, "under --until none: the run leader fights it") {
		t.Fatalf("notes %q: a failed resume must not claim a boss handoff", sum.Notes)
	}
	if code == exitOK {
		t.Fatalf("exit 0 (notes %q): the paused run was reported as a handoff and the walk ended clean", sum.Notes)
	}
	if !strings.Contains(sum.EndReason, "paused: ") {
		t.Fatalf("end %q, want the generic pause ending (notes %q)", sum.EndReason, sum.Notes)
	}
}

// TestPausedBossResumeUnderUntilNoneRePausedIsNoHandoff: the same under --until none when the run pauses again on
// the same pullback line inside the wait: no handoff note, the pause ending owns the run.
func TestPausedBossResumeUnderUntilNoneRePausedIsNoHandoff(t *testing.T) {
	f, _, _ := pausedBossFixture(t, func(f *fixture, boss, total int) {
		f.opts.until = "none"
		f.agent.onResume = func() {
			f.agent.mu.Lock()
			defer f.agent.mu.Unlock()
			f.agent.lines = append(f.agent.lines, queuedEvent{raw: f.agent.history[len(f.agent.history)-1].raw})
		}
	})
	code := f.run(context.Background())
	sum := f.summary()
	if hasNote(sum, "resumed the run so the run leader fights it") || hasNote(sum, "under --until none: the run leader fights it") {
		t.Fatalf("notes %q: a re-paused resume must not claim a boss handoff", sum.Notes)
	}
	if code == exitOK || !strings.Contains(sum.EndReason, "paused: ") {
		t.Fatalf("exit %d end %q, want the generic pause ending (notes %q)", code, sum.EndReason, sum.Notes)
	}
}

// TestRunningBossHandoffHasNoResumeNote is round 4 item 3 (Opus MEDIUM): the watch loop used to note
// "while the run is paused on its boss row: resumed the run" on every ok handoff, including a plain
// running boss handoff where no resume went out. The note is gated on waitOut.running.
func TestRunningBossHandoffHasNoResumeNote(t *testing.T) {
	boss := zgBossStepOf(t, 14517)
	total := len(zgRows(t).Steps)
	f := newFixture(t)
	f.opts.plan, f.opts.holder = "zul_gurub", "raid-walk-zul_gurub"
	f.opts.until = fmt.Sprintf("step %d", boss+1)
	p, err := dungeon.Load(filepath.Join(f.opts.dir, "zul_gurub.json"))
	if err != nil {
		t.Fatal(err)
	}
	f.agent.instMap = p.Map
	f.agent.enterMap = p.Map
	start := fmt.Sprintf("TBM:RUN|zul_gurub|running|%d/%d|high_priestess_jeklik|boss|fight|fighting|until step %d (step %d)", boss, total, boss+1, boss+1)
	f.agent.startReply = &commanderReply{Reply: tbm.Reply{Kind: tbm.KindAck, OK: true,
		Ack:  &tbm.Ack{Intent: "run start", Scope: "party", Count: 1, Executor: "-"},
		Line: start, Lines: []string{start, "TBM:ACTION_ACK|run start|party|1|-"}}}
	f.agent.hostilesFn = func(n int) []tbm.HostileRec {
		return []tbm.HostileRec{{GUID: 9, Entry: 14517, HPPct: 40, Target: packet.GUID(1000)}}
	}
	// The run is RUNNING on her row when the boss incident fires; its own ending arrives after that.
	f.agent.viewHook = func() {
		f.agent.mu.Lock()
		defer f.agent.mu.Unlock()
		if !f.agent.runStarted || len(f.agent.lines) != 0 {
			return
		}
		f.agent.lines = append(f.agent.lines,
			queuedEvent{raw: fmt.Sprintf("TBM:RUN|zul_gurub|done|%d/%d|high_priestess_jeklik|boss|fight|stopped before step %d (until step %d): the party stays where it stands", boss+1, total, boss+1, boss+1)})
	}
	if code := f.run(context.Background()); code != exitOK {
		t.Fatalf("exit %d, want 0 (notes %v): %s\n%s", code, f.summaryNotes(), f.stderr.String(), f.stdout.String())
	}
	sum := f.summary()
	if hasNote(sum, "resumed the run so the run leader fights it") {
		t.Fatalf("notes %q: no resume went out on a running boss handoff", sum.Notes)
	}
	if !hasNote(sum, fmt.Sprintf("boss 14517 engaged at step %d: the run is on its boss step", boss)) {
		t.Fatalf("notes %q, want the plain handoff note", sum.Notes)
	}
	f.agent.mu.Lock()
	verbs := append([]string{}, f.agent.runVerbs...)
	f.agent.mu.Unlock()
	if n := countPrefix(verbs, "resume"); n != 0 {
		t.Fatalf("run verbs %v: %d resumes, want 0", verbs, n)
	}
	if n := countPrefix(f.agent.gmLines(), ".bot run stop"); n != 0 {
		t.Fatalf("%d run stops, want 0: the run leader fights its own boss\n%s", n, f.stdout.String())
	}
}

// TestFightRecordKillAtWalkStartNeedsNoLiveSighting is round 4 item 4 (Opus MEDIUM): w.fights is the
// commander's whole dashboard history, so a "kill" record already listed when the walk began may be
// an earlier walk's or instance's. Such a record credits the goal only when this walk's frames never
// showed the boss alive; a record that first appears during the walk credits freely.
func TestFightRecordKillAtWalkStartNeedsNoLiveSighting(t *testing.T) {
	for _, tc := range []struct {
		name         string
		alive        bool
		wantCredited bool
	}{
		{"stale pre-walk kill, boss seen alive this walk", true, false},
		{"pre-walk record the walk never contradicted (tj-9)", false, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p, err := dungeon.Load("../../dungeons/zul_gurub.json")
			if err != nil {
				t.Fatal(err)
			}
			goal := zgBossStepOf(t, 14517)
			w := bossSummaryWalk(t)
			now := time.Now()
			w.env = walkEnv{now: func() time.Time { return now }}
			w.plan = p
			w.outDir = t.TempDir()
			w.stdout = io.Discard
			w.started, w.ended = now, now
			w.opts.until = fmt.Sprintf("step %d", goal+1)
			w.startStep, w.untilStep = goal, goal+1
			w.hostiles = map[string]hostileState{}
			w.botAt = map[string]botPos{}
			w.partyG = map[uint64]bool{}
			w.bossRule = raidwalk.NewBossRows(p)
			w.bossAttemptAt = map[uint32]time.Time{}
			w.bossRows = map[uint32]hostileState{}
			if tc.alive {
				// The walk's own frames hold Jeklik alive (and engaged) on her row.
				w.trackFrame(now, &tbm.ViewFrame{Hostiles: []tbm.HostileRec{{GUID: 9, Entry: 14517, HPPct: 40, Target: packet.GUID(1000)}}})
			}
			// The run stopped on her own row before any kill: the frames never reported the kill.
			feedStatus(w, tbm.RunStatus{Step: goal, Kind: "boss", Note: "fighting"}, now)
			feedStatus(w, tbm.RunStatus{State: tbm.RunPaused, Step: goal, Kind: "boss", Note: "cmd pullback 17379391205577637935 refused: unreachable"}, now)
			// The dashboard listed the goal boss's kill when the walk began (an earlier walk's or instance's).
			w.fightIDsAtStart = map[string]bool{"68-1": true}
			w.fights = []raidwalk.FightSummary{
				{ID: "68-1", Step: goal, Leg: "high_priestess_jeklik", DurMS: 153656, Outcome: "kill", BossName: "High Priestess Jeklik", BossPhase: 1, BossPhaseEntry: 14517},
			}
			w.finish(exitRaidWalkIncident)
			raw, err := os.ReadFile(filepath.Join(w.outDir, "summary.json"))
			if err != nil {
				t.Fatal(err)
			}
			var sum raidwalk.Summary
			if err := json.Unmarshal(raw, &sum); err != nil {
				t.Fatal(err)
			}
			k := sum.BossKill
			if tc.wantCredited {
				if k == nil || k.Step != goal || k.Entry != 14517 || !k.Killed {
					t.Fatalf("boss_kill %+v, want step %d entry 14517 killed by the pre-walk record (notes %q)", k, goal, sum.Notes)
				}
				return
			}
			if k != nil && k.Killed {
				t.Fatalf("boss_kill %+v: a pre-walk kill record must not credit a boss the frames saw alive (notes %q)", k, sum.Notes)
			}
		})
	}
}

// TestPausedPullbackRefusedUnreachableMatcher pins the one pause text the paused-boss resume may
// undo (round 4 item 1): TortoiseBots' DungeonRunPolicy PullbackRefusedUnreachable note. Another
// refusal of the same pullback (a busy tank, no tank) and any other pause are not it.
func TestPausedPullbackRefusedUnreachableMatcher(t *testing.T) {
	for _, tc := range []struct {
		note string
		want bool
	}{
		{"cmd pullback 17379391205577637935 refused: unreachable: The tank could not start moving to a firing position for the pull; the boss at -12289.1 -1384.1 134.3 is not reachable from this floor: no safe stand 35.0-25.0 yd from it (the lead bot 49.9 yd from it)", true},
		{"cmd pullback 12 refused: unreachable", true},
		{"cmd pullback 12 refused: unreachable: The tank could not start moving; the boss at 1 2 3 is not reachable from this floor: no safe stand 15.0-25.0 yd from it the lead bot has a path to (the lead bot 60.0 yd from it)", true},
		{"cmd pullback 12 refused: tank-busy", false},
		{"cmd pullback 12 refused: too-far", false},
		{"cmd pullback refused: unreachable", false},
		{"cmd pullback 12 refused: invalid-target", false},
		{"19 dead: 3 near wipes within 10 min (54): the run stops here. Regroup (c...)", false},
		{"no run is active", false},
		{"", false},
	} {
		if got := pausedPullbackRefusedUnreachable(tc.note); got != tc.want {
			t.Errorf("pausedPullbackRefusedUnreachable(%q) = %v, want %v", tc.note, got, tc.want)
		}
	}
}

// fightCreditWalk is a bare walk set up the way TestFightRecordKillAtWalkStartNeedsNoLiveSighting builds one,
// with the fake dashboard wired so saveFights itself runs.
func fightCreditWalk(t *testing.T, goal, startStep int, now time.Time) (*walk, *fakeDash) {
	t.Helper()
	p, err := dungeon.Load("../../dungeons/zul_gurub.json")
	if err != nil {
		t.Fatal(err)
	}
	w := bossSummaryWalk(t)
	w.env = walkEnv{now: func() time.Time { return now }}
	w.plan = p
	w.outDir = t.TempDir()
	w.stdout = io.Discard
	w.started, w.ended = now, now
	w.opts.until = fmt.Sprintf("step %d", goal+1)
	w.startStep, w.untilStep = startStep, goal+1
	w.hostiles = map[string]hostileState{}
	w.botAt = map[string]botPos{}
	w.partyG = map[uint64]bool{}
	w.bossRule = raidwalk.NewBossRows(p)
	w.bossAttemptAt = map[uint32]time.Time{}
	w.bossRows = map[uint32]hostileState{}
	w.seenFights = map[string]bool{}
	w.pendingFights = map[string]int{}
	d := &fakeDash{}
	w.dash = d
	return w, d
}

func creditedBoss(t *testing.T, w *walk, goal int) *raidwalk.BossKillSummary {
	t.Helper()
	w.finish(exitRaidWalkIncident)
	raw, err := os.ReadFile(filepath.Join(w.outDir, "summary.json"))
	if err != nil {
		t.Fatal(err)
	}
	var sum raidwalk.Summary
	if err := json.Unmarshal(raw, &sum); err != nil {
		t.Fatal(err)
	}
	return sum.BossKill
}

// TestFightRecordAtStartNeedsTheRunToReachTheBossRow is round 6 item 1 (Opus HIGH): a kill record listed when the walk
// began credits the goal boss only when this run reached the goal row. A walk that ended on earlier trash (a stall, a
// wipe, the time limit) never reached the boss, and the record (Jin'do 18-4 sits in later walks' lists) must neither
// credit it nor create a boss track.
func TestFightRecordAtStartNeedsTheRunToReachTheBossRow(t *testing.T) {
	goal := zgBossStepOf(t, 14517)
	rows := zgRows(t)
	if goal < 3 || rows.Steps[goal-2].Kind == "boss" {
		t.Fatalf("fixture: step %d before the goal is a boss row", goal-1)
	}
	for _, tc := range []struct {
		name    string
		reached bool
		want    bool
	}{
		{"walk ended on earlier trash", false, false},
		{"walk stood on the boss row", true, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			now := time.Now()
			w, _ := fightCreditWalk(t, goal, goal-1, now)
			feedStatus(w, tbm.RunStatus{Step: goal - 1, Kind: "move", Note: "walking"}, now)
			if tc.reached {
				feedStatus(w, tbm.RunStatus{Step: goal, Kind: "boss", Note: "fighting"}, now)
			}
			w.fightIDsAtStart = map[string]bool{"68-1": true}
			w.fights = []raidwalk.FightSummary{
				{ID: "68-1", Step: goal, Leg: "high_priestess_jeklik", DurMS: 153656, Outcome: "kill", BossName: "High Priestess Jeklik", BossPhase: 1, BossPhaseEntry: 14517},
			}
			k := creditedBoss(t, w, goal)
			if tc.want && (k == nil || !k.Killed) {
				t.Fatalf("boss_kill %+v, want credited (notes %q)", k, w.notes)
			}
			if !tc.want {
				if k != nil && k.Killed {
					t.Fatalf("boss_kill %+v: an at-start record must not credit a boss the run never reached (notes %q)", k, w.notes)
				}
				if len(w.bossTracks) != 0 {
					t.Fatalf("boss tracks %v: creditFightKills created a track for a boss the run never reached", w.bossTracks)
				}
			}
		})
	}
}

func jeklikKillRow(id string, live bool, start int64) walkFightRow {
	r := walkFightRow{ID: id, Key: "Bot\x00" + id, Dur: 153656, Outcome: "kill", BossName: "High Priestess Jeklik",
		Leg: "high_priestess_jeklik", Step: 0, BossPhase: 1, BossPhaseEntry: 14517, Start: start}
	if live {
		r.Outcome, r.Live = "live", true
	}
	return r
}

// TestSaveFightsFailedFirstReadThenKillIsCredited is round 6 item 2 (Codex HIGH): the first dashboard read failed, so
// the first successful read came after the walk's own fight had been filed, and that read must not file this walk's
// kill as history. With no read before placement the set comes from the record's start time against the walk's start.
func TestSaveFightsFailedFirstReadThenKillIsCredited(t *testing.T) {
	goal := zgBossStepOf(t, 14517)
	for _, tc := range []struct {
		name  string
		start func(now time.Time) int64
		want  bool
	}{
		{"record started after the walk", func(now time.Time) int64 { return now.Unix() + 60 }, true},
		{"record started before the walk", func(now time.Time) int64 { return now.Unix() - 3600 }, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			now := time.Now()
			w, d := fightCreditWalk(t, goal, goal, now)
			w.manifest.Character = "Bot"
			w.trackFrame(now, &tbm.ViewFrame{Hostiles: []tbm.HostileRec{{GUID: 9, Entry: 14517, HPPct: 40, Target: packet.GUID(1000)}}})
			feedStatus(w, tbm.RunStatus{Step: goal, Kind: "boss", Note: "fighting"}, now)
			d.fightsErr = errors.New("dashboard timeout")
			w.saveFightsAtStart() // the pre-placement read fails
			d.fightsErr = nil
			row := jeklikKillRow("68-1", false, tc.start(now))
			row.Step = uint32(goal)
			d.fights = []walkFightRow{row}
			w.saveFights() // the first successful read comes after the kill
			k := creditedBoss(t, w, goal)
			if tc.want && (k == nil || !k.Killed) {
				t.Fatalf("boss_kill %+v, want this walk's own kill credited (notes %q)", k, w.notes)
			}
			if !tc.want && k != nil && k.Killed {
				t.Fatalf("boss_kill %+v: a record that started before the walk is history (notes %q)", k, w.notes)
			}
		})
	}
}

// TestSaveFightsLiveAtStartRecordStaysAtStart: a record still live when the walk first read the list is part of the
// at-start set, so when it later finishes as a kill it is history, not this walk's.
func TestSaveFightsLiveAtStartRecordStaysAtStart(t *testing.T) {
	goal := zgBossStepOf(t, 14517)
	now := time.Now()
	w, d := fightCreditWalk(t, goal, goal, now)
	w.manifest.Character = "Bot"
	w.trackFrame(now, &tbm.ViewFrame{Hostiles: []tbm.HostileRec{{GUID: 9, Entry: 14517, HPPct: 40, Target: packet.GUID(1000)}}})
	feedStatus(w, tbm.RunStatus{Step: goal, Kind: "boss", Note: "fighting"}, now)
	live := jeklikKillRow("68-1", true, now.Unix()-600)
	live.Step = uint32(goal)
	d.fights = []walkFightRow{live}
	w.saveFightsAtStart()
	done := jeklikKillRow("68-1", false, now.Unix()-600)
	done.Step = uint32(goal)
	d.fights = []walkFightRow{done}
	w.saveFights()
	if k := creditedBoss(t, w, goal); k != nil && k.Killed {
		t.Fatalf("boss_kill %+v: a record live at the walk's start finished as a kill, which is history (notes %q)", k, w.notes)
	}
}

// TestPhaseStartPreplacementFightReadHasItsOwnShortDeadline is the last Codex MEDIUM: the optional dashboard read
// before the walk's placement used to share saveFights' 20 s limit, so a slow or unreachable dashboard could hold the
// run start for 20 s. It has its own short deadline now (injected here), and on a timeout the at-start set falls back
// to the records' start times on the first later successful read, with one note that the read timed out.
func TestPhaseStartPreplacementFightReadHasItsOwnShortDeadline(t *testing.T) {
	old := preplacementFightsTimeout
	preplacementFightsTimeout = 100 * time.Millisecond
	t.Cleanup(func() { preplacementFightsTimeout = old })
	f := newFixture(t)
	f.opts.dashURL, f.opts.dashPassFile = "http://dash.test", "/tmp/pass"
	f.dash.fightsStallFirst, f.dash.fightsStall = 1, 15*time.Second // the first Fights call (the pre-placement read) stalls
	f.defaultRun("onyxias_lair")
	began := time.Now()
	code := f.run(context.Background())
	if took := time.Since(began); took > 5*time.Second {
		t.Fatalf("the walk took %v: the stalled pre-placement dashboard read held it", took)
	}
	if code != exitOK {
		t.Fatalf("exit %d, want 0: %s\n%s", code, f.stderr.String(), f.stdout.String())
	}
	sum := f.summary()
	if !hasNote(sum, "pre-placement dashboard read timed out") {
		t.Fatalf("notes %q, want the one line that the pre-placement read timed out", sum.Notes)
	}
	if !hasNote(sum, "came from their start times") {
		t.Fatalf("notes %q, want the timestamp fallback for the at-start set", sum.Notes)
	}
}
