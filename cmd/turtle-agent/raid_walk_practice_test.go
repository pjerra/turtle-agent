package main

// The practice loop's tests, on the walk's fake environment: the clock, the frames, the events and the fake agent's
// practice GM hands. They pin the two-attempt shape (5 siphon cycles each, one reset with evade, revive, respawn and
// resume), the timer fallback when the cast events are missing, a boss death ending the practice without a kill, the
// three-failed-resets stop, the never-unselected respawn rule and the wall-time ceiling, plus the re-engagement clock,
// the spell_go-only cycle rule, the boss-guid binding, the never-engaged summary and the practice fight ids.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/pjerra/turtle-agent/internal/events"
	"github.com/pjerra/turtle-agent/internal/objects"
	"github.com/pjerra/turtle-agent/internal/packet"
	"github.com/pjerra/turtle-agent/internal/planexport"
	"github.com/pjerra/turtle-agent/internal/raidwalk"
	"github.com/pjerra/turtle-agent/internal/session"
	"github.com/pjerra/turtle-agent/internal/tbm"
)

// practiceBossGUID and practiceSonGUID are the practice boss's and Son's frame keys: creature guids encode their entry.
func practiceBossGUID() packet.GUID {
	return packet.GUID(uint64(packet.HighUnit)<<48 | 14834<<24 | 0x77)
}
func practiceSonGUID() packet.GUID {
	return packet.GUID(uint64(packet.HighUnit)<<48 | 11357<<24 | 0x21)
}

// practiceTrashGUID is a second unit of the boss's own entry (a duplicate-entry trash unit): the practice must never
// touch it.
func practiceTrashGUID() packet.GUID {
	return packet.GUID(uint64(packet.HighUnit)<<48 | 14834<<24 | 0x79)
}

// practiceScenario is the fake test server's practice state: the boss's HP and target, the Son's death and which hires
// are dead. The hooks below model what each GM command does.
type practiceScenario struct {
	mu       sync.Mutex
	armed    bool
	bossHP   int
	target   packet.GUID
	sonDead  bool
	dead     map[string]bool
	dropAt   time.Time
	dropped  bool
	killBoss bool
	noCasts  bool
	// firstCasts, when set, replaces the default cast queue of the first attempt (the START/GO and dedupe tests).
	firstCasts func(start time.Time)
	// pullDelay is how long after the evade the boss re-engages (a slow pull); noReengage never re-engages (a failed
	// reset). reengageAt is the clock time the pull is due and castsAt the time the fake actually re-engaged.
	pullDelay  time.Duration
	noReengage bool
	reengageAt time.Time
	evadeAt    time.Time
	castsAt    time.Time
	reengaged  bool
	// extraTrash adds a second unit of the boss's entry to the client's unit list (a duplicate-entry trash unit):
	// nothing may touch its guid.
	extraTrash bool
	// goneAfter, when set, is how long after the attempt start the boss disappears from every source (a real
	// despawn: the frames and the client stop listing it). goneAt is the clock time it did.
	goneAfter time.Duration
	goneAt    time.Time
	evadeN    int
	respawnN  int
	onEvade   func()
	attemptAt time.Time
}

// practiceFixture is a Zul'Gurub walk that starts on Hakkar's boss row with the frames holding him engaged, ready for
// the practice loop. The scenario's hooks are installed on the fake agent.
func practiceFixture(t *testing.T, sc *practiceScenario, cycles int) (*fixture, int, planexport.StepRow) {
	t.Helper()
	f := newFixture(t)
	f.opts.plan, f.opts.holder = "zul_gurub", "raid-walk-zul_gurub"
	f.opts.until = "none"
	f.opts.practiceEntry = 14834
	f.opts.practiceCycles = cycles
	f.opts.practiceMax = 20 * time.Minute
	f.opts.practiceRest = 30 * time.Second
	f.opts.practiceBossFloor = 40
	f.opts.practiceSustain = true
	f.opts.practiceWindow = 120 * time.Second
	p := zgPlan(t)
	f.agent.instMap = p.Map
	f.agent.enterMap = p.Map
	step, leg, row := zgBossRowOf(t, 14834)
	total := len(zgRows(t).Steps)
	start := zgRunLine(step, total, leg, "boss", "begin", "fighting", "")
	f.agent.startReply = &commanderReply{Reply: tbm.Reply{Kind: tbm.KindAck, OK: true,
		Ack:  &tbm.Ack{Intent: "run start", Scope: "party", Count: 1, Executor: "-"},
		Line: start, Lines: []string{start, "TBM:ACTION_ACK|run start|party|1|-"}}}
	sc.bossHP, sc.target, sc.dead = 100, packet.GUID(1000), map[string]bool{}
	// The fake server's run-status lines for the practice reset: pauseRun waits for a status that shows this plan
	// paused, and resumeRun for one that shows it running again. A test may replace either hook (an ACK with no
	// status, a resume the server never confirms).
	f.agent.onPause = func() {
		f.agent.push(fmt.Sprintf("TBM:RUN|zul_gurub|paused|%d/%d|hakkar|boss|begin|paused for the practice reset", step, total), "")
	}
	f.agent.onResume = func() {
		f.agent.push(fmt.Sprintf("TBM:RUN|zul_gurub|running|%d/%d|hakkar|boss|begin|fighting", step, total), "")
	}

	enqueue := func(startAt time.Time) {
		f.agent.mu.Lock()
		defer f.agent.mu.Unlock()
		for k := 1; k <= 5; k++ {
			at := startAt.Add(time.Duration(k*90) * time.Second)
			f.agent.spellQueue = append(f.agent.spellQueue, queuedSpell{at: at,
				ev: events.Event{Kind: events.KindSpellGo, SourceGUID: practiceBossGUID(), SpellID: 24324, Time: at}})
		}
	}

	f.agent.hostilesFn = func(n int) []tbm.HostileRec {
		f.agent.mu.Lock()
		started := f.agent.runStarted
		party := append([]string{}, f.agent.party...)
		f.agent.mu.Unlock()
		now := f.clock.now()
		sc.mu.Lock()
		if !sc.armed && started {
			sc.armed = true
			sc.attemptAt = now
			if sc.goneAfter > 0 {
				sc.goneAt = now.Add(sc.goneAfter)
			}
			if sc.dropAt.IsZero() {
				sc.dropAt = now.Add(95 * time.Second)
			}
			if !sc.killBoss && !sc.noCasts {
				if sc.firstCasts != nil {
					sc.firstCasts(now)
				} else {
					enqueue(now)
				}
			}
		}
		// The pull after a reset: the boss re-engages (targets a party member) once the delay passed, and its casts
		// are queued from that moment (the core's Reset() restarts the timer at the pull).
		if started && !sc.reengaged && !sc.reengageAt.IsZero() && !sc.noReengage && !now.Before(sc.reengageAt) {
			sc.reengaged = true
			sc.castsAt = now
			sc.target = packet.GUID(1000)
			if !sc.noCasts && !sc.killBoss {
				enqueue(now)
			}
		}
		if !sc.dropAt.IsZero() && !sc.dropped && !now.Before(sc.dropAt) {
			sc.dropped = true
			if sc.killBoss {
				sc.bossHP = 0
			} else {
				sc.bossHP = 30
				if len(party) > 1 {
					sc.dead[party[1]] = true
					f.agent.mu.Lock()
					f.agent.dead[party[1]] = true
					f.agent.mu.Unlock()
				}
			}
		}
		hp, target, sonDead := sc.bossHP, sc.target, sc.sonDead
		gone := !sc.goneAt.IsZero() && !now.Before(sc.goneAt)
		sc.mu.Unlock()
		var out []tbm.HostileRec
		if !gone {
			out = append(out, tbm.HostileRec{GUID: practiceBossGUID(), Entry: 14834, X: row.X, Y: row.Y, Z: row.Z,
				HPPct: hp, Target: target, Flags: "-"})
		}
		if !sonDead {
			out = append(out, tbm.HostileRec{GUID: practiceSonGUID(), Entry: 11357, X: row.X + 3, Y: row.Y, Z: row.Z,
				HPPct: 100, Flags: "-"})
		}
		return out
	}
	f.agent.unitsFn = func(n int) []objects.Unit {
		sc.mu.Lock()
		hp, sonDead, trash := sc.bossHP, sc.sonDead, sc.extraTrash
		gone := !sc.goneAt.IsZero() && !f.clock.now().Before(sc.goneAt)
		sc.mu.Unlock()
		var out []objects.Unit
		if !gone {
			out = append(out, objects.Unit{
				GUID: practiceBossGUID(), Entry: 14834, Type: "creature", Health: uint32(hp * 420), MaxHealth: 42000, Dead: hp == 0})
		}
		out = append(out, objects.Unit{GUID: practiceSonGUID(), Entry: 11357, Type: "creature", Health: map[bool]uint32{true: 0, false: 100}[sonDead],
			MaxHealth: 100, Dead: sonDead})
		if trash {
			// A second unit of the boss's entry the client holds (a duplicate-entry trash unit): never the boss.
			out = append(out, objects.Unit{GUID: practiceTrashGUID(), Entry: 14834, Type: "creature", Health: 42000, MaxHealth: 42000})
		}
		return out
	}
	f.agent.onEvade = func(g packet.GUID) {
		sc.mu.Lock()
		sc.evadeN++
		sc.bossHP, sc.target = 100, 0
		sc.reengaged = false
		sc.evadeAt = f.clock.now()
		// The reset's own verdict is read from the next frame (the boss home, 100 %, no target): the pull cannot come
		// before it, so the fake's base delay covers that observation.
		sc.reengageAt = sc.evadeAt.Add(4*time.Second + sc.pullDelay)
		hook := sc.onEvade
		sc.mu.Unlock()
		if hook != nil {
			hook()
		}
	}
	f.agent.onSetHP = func(g packet.GUID) {
		sc.mu.Lock()
		sc.bossHP = 100
		sc.mu.Unlock()
	}
	f.agent.onRespawn = func(g packet.GUID) {
		if g != practiceSonGUID() {
			return
		}
		sc.mu.Lock()
		sc.respawnN++
		sc.sonDead = false
		sc.mu.Unlock()
	}
	f.agent.onRevive = func(name string) {
		sc.mu.Lock()
		sc.reviveN++
		delete(sc.dead, name)
		sc.mu.Unlock()
		f.agent.mu.Lock()
		delete(f.agent.dead, name)
		f.agent.mu.Unlock()
	}
	return f, step, row
}

// practiceRows reads <out>/practice.jsonl.
func (f *fixture) practiceRows(t *testing.T) []practiceRow {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(f.out, "practice.jsonl"))
	if err != nil {
		t.Fatalf("practice.jsonl: %v", err)
	}
	var out []practiceRow
	for _, line := range strings.Split(strings.TrimSpace(string(raw)), "\n") {
		if line == "" {
			continue
		}
		var r practiceRow
		if err := json.Unmarshal([]byte(line), &r); err != nil {
			t.Fatalf("row %q: %v", line, err)
		}
		out = append(out, r)
	}
	return out
}

// countLog counts the fake's log lines with a prefix.
func countLog(log []string, prefix string) int {
	n := 0
	for _, l := range log {
		if strings.HasPrefix(l, prefix) {
			n++
		}
	}
	return n
}

// TestPracticeTwoAttemptsTenCycles: 5 siphon casts per attempt, one reset in between, 10 cycles total. The reset's
// sequence is pause (the commander's run pause, never a run stop), evade, revive, respawn, resume; the sustain revives
// a dead hire and restores the boss's HP.
func TestPracticeTwoAttemptsTenCycles(t *testing.T) {
	sc := &practiceScenario{sonDead: true}
	f, _, _ := practiceFixture(t, sc, 10)
	// The reset clears the instance-data markers and sends the leg's lines again, as after a wipe. The boss's leg
	// carries one line here; `q inst` answers the copy.
	f.opts.gmInstanceData = stringList{"hakkar:1 3"}
	f.agent.ttFn = func(cmd string) (ttResult, error) {
		if cmd == "q inst" {
			return ttReplyOf(t, map[string]any{"map": 309, "inst": 77}), nil
		}
		return ttResult{}, nil
	}
	if code := f.run(context.Background()); code != exitOK {
		t.Fatalf("exit %d, want 0 (notes %v): %s\n%s", code, f.summaryNotes(), f.stderr.String(), f.stdout.String())
	}
	sum := f.summary()
	if sum.Practice == nil {
		t.Fatal("summary has no practice object")
	}
	if sum.Practice.Attempts != 2 || sum.Practice.Cycles != 10 || sum.Practice.Ended != "cycles" || sum.Practice.Rows != "practice.jsonl" {
		t.Fatalf("practice %+v, want 2 attempts, 10 cycles, ended cycles", *sum.Practice)
	}
	if !sum.Assisted {
		t.Fatal("a practice walk is not marked ASSISTED")
	}
	if sum.BossKill != nil || len(sum.BossKills) != 0 {
		t.Fatalf("a practice walk credited a boss kill: %+v %+v", sum.BossKill, sum.BossKills)
	}
	text := f.stdout.String()
	if !strings.Contains(text, "PRACTICE") || !strings.Contains(text, "ASSISTED") {
		t.Fatalf("the summary line does not say PRACTICE ASSISTED: %q", text)
	}
	if !strings.Contains(readFile(t, filepath.Join(f.out, "summary.txt")), "practice: 10 cycles in 2 attempts, ended: cycles") {
		t.Fatalf("summary.txt lacks the practice line:\n%s", readFile(t, filepath.Join(f.out, "summary.txt")))
	}
	rows := f.practiceRows(t)
	if len(rows) != 10 {
		t.Fatalf("%d practice rows, want 10", len(rows))
	}
	if rows[0].Attempt != 1 || rows[5].Attempt != 2 || rows[4].TotalCycle != 5 || rows[9].TotalCycle != 10 {
		t.Fatalf("row attempts/totals: %+v", rows)
	}
	if rows[0].Detect != "event" || rows[9].Detect != "event" {
		t.Fatalf("detect %q/%q, want event", rows[0].Detect, rows[9].Detect)
	}
	// The hire died and the boss dipped to 30 % between the cast and the row: the poll's sustain revived the hire and
	// the poll's floor check restored the boss before the row was written (not only at the row).
	if rows[0].SonsKnown != 1 || rows[0].BossHPPct < f.opts.practiceBossFloor || len(rows[0].Sustain.Revived) != 1 {
		t.Fatalf("cycle 1 row does not hold the sustain: %+v", rows[0])
	}
	if n := countLog(f.agent.log, "gm_set_hp "); n < 1 {
		t.Fatalf("no hp restore went out (log %v)", f.agent.log)
	}
	if rows[0].Sustain.BossHPSet {
		t.Fatal("the row itself restored the boss: the poll's floor check did not fire between the cast and the row")
	}
	if !strings.Contains(readFile(t, filepath.Join(f.out, "run.log")), "between rows: restored") {
		t.Fatalf("the poll's floor restore is not in run.log")
	}
	log := f.agent.log
	if n := countLog(log, "gm .bot run stop"); n != 0 {
		t.Fatalf("%d run stops, want 0 (a stop ends the run and erases its slot; log %v)", n, log)
	}
	if n := countLog(log, "cmdr run pause"); n != 1 {
		t.Fatalf("%d run pauses, want 1 (log %v)", n, log)
	}
	if sc.evadeN != 1 {
		t.Fatalf("%d evades, want 1", sc.evadeN)
	}
	if sc.respawnN != 1 {
		t.Fatalf("%d Son respawns, want 1", sc.respawnN)
	}
	if n := countLog(log, "gm_revive "); n != 1 {
		t.Fatalf("%d revives, want 1 (the dead hire at cycle 1; the reset finds it alive)", n)
	}
	if n := countLog(f.agent.runVerbs, "resume"); n != 1 {
		t.Fatalf("%d run resumes, want 1", n)
	}
	// The reset's order: pause, evade, revive, respawn, and the resume after them.
	idx := func(prefix string) int {
		for i, l := range log {
			if strings.HasPrefix(l, prefix) {
				return i
			}
		}
		return -1
	}
	pause, evade, respawn, inst := idx("cmdr run pause"), idx("gm_evade "), idx("gm_respawn "), idx("gm .debug instancedata")
	if pause < 0 || evade < 0 || respawn < 0 || inst < 0 || !(pause < evade && evade < respawn && respawn < inst) {
		t.Fatalf("reset order pause %d, evade %d, respawn %d, instancedata %d (log %v)", pause, evade, respawn, inst, log)
	}
	if !strings.Contains(log[inst], ".debug instancedata 1 3") {
		t.Fatalf("the instance data was not the leg's line: %q", log[inst])
	}
}

// TestPracticeTimerFallback: with no siphon cast events the attempts fall back to the boss's own timer after
// FirstAtS+10 s, and the rows say detect=timer.
func TestPracticeTimerFallback(t *testing.T) {
	sc := &practiceScenario{noCasts: true}
	f, _, _ := practiceFixture(t, sc, 2)
	// No spell events are queued: the fake's spell page is empty.
	if code := f.run(context.Background()); code != exitOK {
		t.Fatalf("exit %d, want 0 (notes %v): %s", code, f.summaryNotes(), f.stderr.String())
	}
	rows := f.practiceRows(t)
	if len(rows) != 2 {
		t.Fatalf("%d rows, want 2", len(rows))
	}
	for _, r := range rows {
		if r.Detect != "timer" {
			t.Fatalf("detect %q, want timer", r.Detect)
		}
	}
	if sum := f.summary(); sum.Practice == nil || sum.Practice.Ended != "cycles" {
		t.Fatalf("practice %+v, want ended cycles", sum.Practice)
	}
	if !hasNote(f.summary(), "falling back to the boss timer") {
		t.Fatalf("notes %v, want the fallback note", f.summary().Notes)
	}
}

// TestPracticeBossKilledEnds: a boss death during practice ends it at once, with no reset and no kill credited.
func TestPracticeBossKilledEnds(t *testing.T) {
	sc := &practiceScenario{killBoss: true}
	sc.dropAt = time.Time{}
	f, _, _ := practiceFixture(t, sc, 10)
	// The drop time is armed relative to the practice start; the killBoss flag makes it 0 %.
	if code := f.run(context.Background()); code != exitOK {
		t.Fatalf("exit %d, want 0 (notes %v): %s", code, f.summaryNotes(), f.stderr.String())
	}
	sum := f.summary()
	if sum.Practice == nil || sum.Practice.Ended != "boss killed during practice" {
		t.Fatalf("practice %+v, want the boss-killed ending", sum.Practice)
	}
	if sum.BossKill != nil || len(sum.BossKills) != 0 {
		t.Fatalf("the boss death was credited: %+v", sum.BossKill)
	}
	if sum.Assisted != true || !strings.Contains(readFile(t, filepath.Join(f.out, "summary.txt")), "PRACTICE") {
		t.Fatal("the practice marking is missing")
	}
	log := f.agent.log
	if countLog(log, "gm_evade ") != 0 || countLog(log, "gm .bot run stop") != 0 {
		t.Fatalf("a dead boss was reset: %v", log)
	}
}

// TestPracticeFailedResetsStop: three resets in a row that do not see the boss reset stop the practice with the
// phase-failure exit code and "practice stopped: 3 failed resets".
func TestPracticeFailedResetsStop(t *testing.T) {
	sc := &practiceScenario{}
	sc.mu.Lock()
	sc.bossHP, sc.target = 100, packet.GUID(1000)
	sc.mu.Unlock()
	f, _, row := practiceFixture(t, sc, 10)
	// Every hire is dead from the first frame on, so each attempt ends at once; the evade hook leaves the boss engaged
	// (target 1000) so the reset never sees it reset, and the revive hook does nothing so the hires stay dead.
	f.agent.onEvade = func(g packet.GUID) {
		sc.mu.Lock()
		sc.evadeN++
		sc.bossHP, sc.target = 100, packet.GUID(1000)
		sc.mu.Unlock()
	}
	f.agent.onRevive = func(name string) { sc.mu.Lock(); sc.reviveN++; sc.mu.Unlock() }
	f.agent.hostilesFn = func(n int) []tbm.HostileRec {
		f.agent.mu.Lock()
		party := append([]string{}, f.agent.party...)
		started := f.agent.runStarted
		if started {
			for _, name := range party {
				f.agent.dead[name] = true
			}
		}
		f.agent.mu.Unlock()
		return []tbm.HostileRec{{GUID: practiceBossGUID(), Entry: 14834, X: row.X, Y: row.Y, Z: row.Z, HPPct: 100, Target: packet.GUID(1000), Flags: "-"}}
	}
	if code := f.run(context.Background()); code != exitRaidWalkFailed {
		t.Fatalf("exit %d, want %d (notes %v): %s", code, exitRaidWalkFailed, f.summaryNotes(), f.stderr.String())
	}
	sum := f.summary()
	if sum.Practice == nil || sum.Practice.Ended != "3 failed resets" || sum.Practice.Attempts != 3 {
		t.Fatalf("practice %+v, want 3 attempts and 3 failed resets", sum.Practice)
	}
	if !strings.Contains(sum.EndReason, "practice stopped: 3 failed resets") {
		t.Fatalf("end reason %q, want the stopped reason", sum.EndReason)
	}
	if sc.evadeN != 3 {
		t.Fatalf("%d evades, want 3", sc.evadeN)
	}
}

// TestPracticeNoUnselectedRespawn: the walk never sends the bare `.respawn` GM line, and every gm_respawn RPC names a
// guid of the Son set (the selected dead form only).
func TestPracticeNoUnselectedRespawn(t *testing.T) {
	sc := &practiceScenario{sonDead: true}
	f, _, _ := practiceFixture(t, sc, 10)
	if code := f.run(context.Background()); code != exitOK {
		t.Fatalf("exit %d: %s", code, f.stderr.String())
	}
	for _, line := range f.agent.gm {
		if strings.TrimSpace(line) == ".respawn" {
			t.Fatalf("the bare .respawn went out: %v", f.agent.gm)
		}
	}
	if n := countLog(f.agent.log, "gm_respawn "); n != 1 {
		t.Fatalf("%d respawn RPCs, want 1 (log %v)", n, f.agent.log)
	}
	if !strings.HasPrefix(lastLog(f.agent.log, "gm_respawn "), "gm_respawn "+practiceSonGUID().String()) {
		t.Fatalf("the respawn did not name the Son: %v", f.agent.log)
	}
}

// TestPracticeMaxTime: --practice-max ends the loop with "practice stopped: max time".
func TestPracticeMaxTime(t *testing.T) {
	sc := &practiceScenario{noCasts: true}
	f, _, _ := practiceFixture(t, sc, 10)
	f.opts.practiceMax = 3 * time.Minute
	if code := f.run(context.Background()); code != exitRaidWalkFailed {
		t.Fatalf("exit %d, want %d (notes %v): %s", code, exitRaidWalkFailed, f.summaryNotes(), f.stderr.String())
	}
	sum := f.summary()
	if sum.Practice == nil || sum.Practice.Ended != "max time" {
		t.Fatalf("practice %+v, want max time", sum.Practice)
	}
	if !strings.Contains(sum.EndReason, "practice stopped: max time") {
		t.Fatalf("end reason %q", sum.EndReason)
	}
}

// readFile is a test's file read that fails loudly.
func readFile(t *testing.T, path string) string {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("%s: %v", path, err)
	}
	return string(raw)
}

// lastLog is the last fake log line with a prefix.
func lastLog(log []string, prefix string) string {
	for i := len(log) - 1; i >= 0; i-- {
		if strings.HasPrefix(log[i], prefix) {
			return log[i]
		}
	}
	return ""
}

// TestPracticePullDelay: the attempt after a reset is clocked from the re-engagement, not from the reset. The boss is
// pulled 20 s after it reset; the attempt's casts are 90 s after the pull (the core's Reset() timer starts then), so
// the rows shift with the pull.
func TestPracticePullDelay(t *testing.T) {
	sc := &practiceScenario{pullDelay: 20 * time.Second}
	f, _, _ := practiceFixture(t, sc, 10)
	if code := f.run(context.Background()); code != exitOK {
		t.Fatalf("exit %d, want 0 (notes %v): %s", code, f.summaryNotes(), f.stderr.String())
	}
	rows := f.practiceRows(t)
	if len(rows) != 10 || rows[5].Attempt != 2 {
		t.Fatalf("%d rows, want 10 with the second attempt at row 5: %+v", len(rows), rows)
	}
	if sc.evadeAt.IsZero() || sc.castsAt.IsZero() {
		t.Fatal("the fake never evaded or re-engaged")
	}
	if d := sc.castsAt.Sub(sc.evadeAt); d < 20*time.Second {
		t.Fatalf("the boss re-engaged %s after the reset, want at least the 20 s pull delay", d)
	}
	// The first cast of attempt 2 is 90 s after the RE-ENGAGEMENT, not 90 s after the reset (which would be early by
	// the pull delay).
	want := sc.castsAt.Add(90 * time.Second).Format(time.RFC3339)
	if rows[5].CastAt != want {
		t.Fatalf("attempt 2's first cast at %s, want %s (the re-engagement + FirstAtS)", rows[5].CastAt, want)
	}
	if rows[5].Detect != "event" {
		t.Fatalf("detect %q, want event", rows[5].Detect)
	}
}

// TestPracticeNoReengageIsFailedReset: a reset after which the boss never fights again is a failed reset. Three in a
// row end the practice with "3 failed resets" (exit 18), even though the boss itself reset.
func TestPracticeNoReengageIsFailedReset(t *testing.T) {
	sc := &practiceScenario{noReengage: true}
	f, _, _ := practiceFixture(t, sc, 10)
	if code := f.run(context.Background()); code != exitRaidWalkFailed {
		t.Fatalf("exit %d, want %d (notes %v): %s", code, exitRaidWalkFailed, f.summaryNotes(), f.stderr.String())
	}
	sum := f.summary()
	if sum.Practice == nil || sum.Practice.Ended != "3 failed resets" || sum.Practice.Cycles != 5 {
		t.Fatalf("practice %+v, want 3 failed resets after the first attempt's 5 cycles", sum.Practice)
	}
	// 1 real attempt + 3 attempts whose reset never brought the boss back.
	if sum.Practice.Attempts != 4 {
		t.Fatalf("%d attempts, want 4", sum.Practice.Attempts)
	}
	if !hasNote(sum, "did not re-engage within") {
		t.Fatalf("notes %v, want the failed re-engagement note", sum.Notes)
	}
}

// TestPracticeSpellGoOnly: one cast is one spell_go. A spell_start and a duplicate spell_go within EverySec/2 are the
// same cast, spread over separate events pages, and yield one cycle.
func TestPracticeSpellGoOnly(t *testing.T) {
	sc := &practiceScenario{}
	casts := make(chan time.Time, 1)
	var f *fixture
	sc.firstCasts = func(start time.Time) {
		f.agent.mu.Lock()
		defer f.agent.mu.Unlock()
		goAt := start.Add(90 * time.Second)
		casts <- goAt
		f.agent.spellQueue = append(f.agent.spellQueue,
			queuedSpell{at: goAt, ev: events.Event{Kind: events.KindSpellStart, SourceGUID: practiceBossGUID(), SpellID: 24324, Time: goAt}},
			queuedSpell{at: goAt.Add(time.Second), ev: events.Event{Kind: events.KindSpellGo, SourceGUID: practiceBossGUID(), SpellID: 24324, Time: goAt.Add(time.Second)}},
			queuedSpell{at: goAt.Add(11 * time.Second), ev: events.Event{Kind: events.KindSpellGo, SourceGUID: practiceBossGUID(), SpellID: 24324, Time: goAt.Add(11 * time.Second)}},
		)
	}
	f, _, _ = practiceFixture(t, sc, 1)
	if code := f.run(context.Background()); code != exitOK {
		t.Fatalf("exit %d, want 0 (notes %v): %s", code, f.summaryNotes(), f.stderr.String())
	}
	var goAt time.Time
	select {
	case goAt = <-casts:
	default:
		t.Fatal("the fake never queued the casts")
	}
	rows := f.practiceRows(t)
	if len(rows) != 1 {
		t.Fatalf("%d rows, want 1 (the spell_start and the duplicate spell_go are one cast): %+v", len(rows), rows)
	}
	if rows[0].Detect != "event" || rows[0].CastAt != goAt.Add(time.Second).Format(time.RFC3339) {
		t.Fatalf("row %+v, want the spell_go at %s", rows[0], goAt.Add(time.Second).Format(time.RFC3339))
	}
}

// TestPracticeNeverEngagedSummary: a --practice walk whose loop never started still carries the practice object (ended
// "never engaged"), so the walk is ASSISTED and BuildSummary clears every boss kill.
func TestPracticeNeverEngagedSummary(t *testing.T) {
	w := &walk{opts: raidWalkOptions{practiceEntry: 14834}}
	got := w.practiceSummary()
	if got == nil || got.Entry != 14834 || got.Ended != "never engaged" || got.Rows != "practice.jsonl" || got.FightIDs == nil {
		t.Fatalf("practiceSummary %+v, want the never-engaged object", got)
	}
	sum := raidwalk.BuildSummary(nil, raidwalk.SummaryMeta{
		Plan: "zul_gurub", Practice: got,
		BossKill:  &raidwalk.BossKillSummary{Entry: 14834, Killed: true},
		BossKills: []raidwalk.BossKillSummary{{Entry: 14834, Killed: true}},
	})
	if !sum.Assisted || sum.Practice == nil || sum.Practice.Ended != "never engaged" {
		t.Fatalf("summary %+v, want practice + assisted", sum.Practice)
	}
	if sum.BossKill != nil || len(sum.BossKills) != 0 {
		t.Fatalf("a practice walk kept a boss kill: %+v %+v", sum.BossKill, sum.BossKills)
	}
}

// TestPracticeFightIDs: the practice object names the dashboard fights that started at or after the practice start, and
// fights-run.json marks the run as a practice one (the fight tools skip those).
func TestPracticeFightIDs(t *testing.T) {
	sc := &practiceScenario{sonDead: true}
	f, _, _ := practiceFixture(t, sc, 10)
	f.opts.dashURL, f.opts.dashPassFile = "http://dash.test", "/tmp/pass"
	oldStart := f.clock.now().Add(-time.Hour).Unix()
	newStart := f.clock.now().Add(time.Hour).Unix()
	f.dash.fights = []walkFightRow{
		{ID: "before-practice", Key: "k1", Start: oldStart, Outcome: "kill"},
		{ID: "during-practice", Key: "k2", Start: newStart, Outcome: "kill"},
	}
	if code := f.run(context.Background()); code != exitOK {
		t.Fatalf("exit %d, want 0 (notes %v): %s", code, f.summaryNotes(), f.stderr.String())
	}
	sum := f.summary()
	if sum.Practice == nil || len(sum.Practice.FightIDs) != 1 || sum.Practice.FightIDs[0] != "during-practice" {
		t.Fatalf("practice fight_ids %v, want [during-practice]", sum.Practice)
	}
	var run struct {
		Practice bool `json:"practice"`
	}
	if err := json.Unmarshal([]byte(readFile(t, filepath.Join(f.out, "fights-run.json"))), &run); err != nil {
		t.Fatalf("fights-run.json: %v", err)
	}
	if !run.Practice {
		t.Fatalf("fights-run.json does not mark the practice run: %s", readFile(t, filepath.Join(f.out, "fights-run.json")))
	}
}

// TestPracticeDuplicateEntryTrashUntouched: a second unit of the boss's entry (a duplicate-entry trash unit) is never
// the boss. The practice binds the engaged one and every GM line names its guid, never the trash's.
func TestPracticeDuplicateEntryTrashUntouched(t *testing.T) {
	sc := &practiceScenario{sonDead: true, extraTrash: true}
	f, _, _ := practiceFixture(t, sc, 10)
	if code := f.run(context.Background()); code != exitOK {
		t.Fatalf("exit %d, want 0 (notes %v): %s", code, f.summaryNotes(), f.stderr.String())
	}
	for _, line := range f.agent.log {
		if strings.Contains(line, practiceTrashGUID().String()) {
			t.Fatalf("a GM line touched the duplicate-entry trash unit: %q", line)
		}
	}
	if n := countLog(f.agent.log, "gm_evade "+practiceBossGUID().String()); n != 1 {
		t.Fatalf("%d evades of the bound boss, want 1 (log %v)", n, f.agent.log)
	}
}

// TestPracticeAmbiguousEntryRefused: several units of the entry with no single engaged one cannot be told apart, so the
// practice fails before ANY GM action (never resets a guessed creature).
func TestPracticeAmbiguousEntryRefused(t *testing.T) {
	sc := &practiceScenario{extraTrash: true}
	f, _, row := practiceFixture(t, sc, 10)
	// Both units of the entry are engaged (each targets a party member): no single guid is the boss.
	f.agent.hostilesFn = func(n int) []tbm.HostileRec {
		return []tbm.HostileRec{
			{GUID: practiceBossGUID(), Entry: 14834, X: row.X, Y: row.Y, Z: row.Z, HPPct: 100, Target: 1000, Flags: "-"},
			{GUID: practiceTrashGUID(), Entry: 14834, X: row.X + 3, Y: row.Y, Z: row.Z, HPPct: 100, Target: 1000, Flags: "-"},
		}
	}
	if code := f.run(context.Background()); code != exitRaidWalkFailed {
		t.Fatalf("exit %d, want %d (notes %v): %s", code, exitRaidWalkFailed, f.summaryNotes(), f.stderr.String())
	}
	for _, meth := range []string{"gm_evade ", "gm_set_hp ", "gm_respawn ", "gm_revive "} {
		if n := countLog(f.agent.log, meth); n != 0 {
			t.Fatalf("%d %s calls before the boss was bound (log %v)", n, meth, f.agent.log)
		}
	}
	sum := f.summary()
	if sum.Practice == nil || sum.Practice.Ended != "boss not bound" {
		t.Fatalf("practice %+v, want the not-bound ending", sum.Practice)
	}
	if !strings.Contains(sum.EndReason, "cannot tell the boss from the trash") {
		t.Fatalf("end reason %q, want the ambiguity reason", sum.EndReason)
	}
}

// TestPracticeCommanderRevived: a commander that died during the attempt is revived with the dead hires in the reset.
func TestPracticeCommanderRevived(t *testing.T) {
	sc := &practiceScenario{sonDead: true}
	f, _, _ := practiceFixture(t, sc, 10)
	f.agent.status.Health, f.agent.status.MaxHealth = 0, 100
	if code := f.run(context.Background()); code != exitOK {
		t.Fatalf("exit %d, want 0 (notes %v): %s", code, f.summaryNotes(), f.stderr.String())
	}
	if n := countLog(f.agent.log, "gm_revive Commander"); n != 1 {
		t.Fatalf("%d commander revives, want 1 (log %v)", n, f.agent.log)
	}
}

// TestPracticeResumeTimeoutIs18: resumeRun's absolute-deadline failure is a *recoveryTimeout, which the wipe paths map
// to exit 19. During practice it is a practice failure: exit 18 with its own reason, never a wipe.
func TestPracticeResumeTimeoutIs18(t *testing.T) {
	sc := &practiceScenario{sonDead: true}
	f, _, _ := practiceFixture(t, sc, 10)
	// The run's own time ceiling passes during the first attempt, and the resumed run does not come back: the resume's
	// wait runs into the absolute recovery deadline.
	f.opts.maxRunMinutes = 3
	f.agent.onResume = func() {
		f.agent.push("TBM:RUN|zul_gurub|paused|700/733|hakkar|boss|begin|the run did not come back", "")
	}
	if code := f.run(context.Background()); code != exitRaidWalkFailed {
		t.Fatalf("exit %d, want %d (notes %v): %s", code, exitRaidWalkFailed, f.summaryNotes(), f.stderr.String())
	}
	sum := f.summary()
	if sum.ExitCode != exitRaidWalkFailed || !strings.Contains(sum.EndReason, "the run did not resume after the reset") {
		t.Fatalf("exit %d reason %q, want the practice resume timeout", sum.ExitCode, sum.EndReason)
	}
	if sum.Practice == nil || sum.Practice.Ended != "resume timed out" {
		t.Fatalf("practice %+v, want the resume-timeout ending", sum.Practice)
	}
}

// TestPracticeGMRefusesLostLease: a lost lease ends the practice before any GM action.
func TestPracticeGMRefusesLostLease(t *testing.T) {
	w := &walk{}
	w.leaseLost.Store(true)
	err := w.practiceGM(context.Background(), "gm_evade", map[string]any{"guid": practiceBossGUID().String()})
	if err == nil || !strings.Contains(err.Error(), "lease lost") {
		t.Fatalf("%v, want the lease-lost refusal", err)
	}
}

// TestPracticePauseRefusedNoReset: a pause the commander refuses ends the practice before ANY GM action of the reset
// (H1): no evade, no revive, no respawn, no hp restore. The boss never dips, so the only GM hands of the run would be
// the reset's.
func TestPracticePauseRefusedNoReset(t *testing.T) {
	sc := &practiceScenario{}
	f, _, _ := practiceFixture(t, sc, 10)
	sc.mu.Lock()
	sc.dropAt = f.clock.now().Add(time.Hour)
	sc.mu.Unlock()
	fails := true
	f.agent.pauseErr = &fails
	if code := f.run(context.Background()); code != exitRaidWalkFailed {
		t.Fatalf("exit %d, want %d (notes %v): %s", code, exitRaidWalkFailed, f.summaryNotes(), f.stderr.String())
	}
	sum := f.summary()
	if sum.Practice == nil || sum.Practice.Ended != "run pause refused" || sum.Practice.Attempts != 1 {
		t.Fatalf("practice %+v, want one attempt ended run pause refused", sum.Practice)
	}
	if !strings.Contains(sum.EndReason, "the run pause before the reset failed") {
		t.Fatalf("end reason %q, want the pause failure", sum.EndReason)
	}
	for _, meth := range []string{"gm_evade ", "gm_set_hp ", "gm_respawn ", "gm_revive "} {
		if n := countLog(f.agent.log, meth); n != 0 {
			t.Fatalf("%d %s calls after a refused pause (log %v)", n, meth, f.agent.log)
		}
	}
}

// TestPracticePauseACKWithoutStatusRefused: an ACK alone is no pause. With no paused status for this plan, the reset
// is refused before any GM action (H2), with the wait's own reason.
func TestPracticePauseACKWithoutStatusRefused(t *testing.T) {
	sc := &practiceScenario{}
	f, _, _ := practiceFixture(t, sc, 10)
	sc.mu.Lock()
	sc.dropAt = f.clock.now().Add(time.Hour)
	sc.mu.Unlock()
	// The commander ACKs but the server never sends the paused line.
	f.agent.onPause = nil
	if code := f.run(context.Background()); code != exitRaidWalkFailed {
		t.Fatalf("exit %d, want %d (notes %v): %s", code, exitRaidWalkFailed, f.summaryNotes(), f.stderr.String())
	}
	sum := f.summary()
	if sum.Practice == nil || sum.Practice.Ended != "run pause refused" {
		t.Fatalf("practice %+v, want the pause refusal", sum.Practice)
	}
	if !strings.Contains(sum.EndReason, "no paused status for zul_gurub") {
		t.Fatalf("end reason %q, want the missing paused status", sum.EndReason)
	}
	for _, meth := range []string{"gm_evade ", "gm_set_hp ", "gm_respawn ", "gm_revive "} {
		if n := countLog(f.agent.log, meth); n != 0 {
			t.Fatalf("%d %s calls after an unconfirmed pause (log %v)", n, meth, f.agent.log)
		}
	}
}

// TestPracticePauseNotRunningProceeds: a reset whose pause answers "The run is not running" (the live practice 2/3
// incident) proceeds with the reset instead of exiting 18: evade, revive, respawn go out, no resume follows (no run
// slot is left), and the next attempt's re-engagement starts it again.
func TestPracticePauseNotRunningProceeds(t *testing.T) {
	sc := &practiceScenario{}
	f, _, _ := practiceFixture(t, sc, 10)
	sc.mu.Lock()
	sc.dropAt = f.clock.now().Add(time.Hour)
	sc.mu.Unlock()
	fails := true
	f.agent.pauseErr = &fails
	f.agent.pauseErrMsg = "The run is not running"
	if code := f.run(context.Background()); code != exitOK {
		t.Fatalf("exit %d, want 0 (notes %v): %s", code, exitRaidWalkFailed, f.summaryNotes(), f.stderr.String())
	}
	sum := f.summary()
	if sum.Practice == nil || sum.Practice.Attempts != 2 || sum.Practice.Cycles != 10 || sum.Practice.Ended != "cycles" {
		t.Fatalf("practice %+v, want 2 attempts, 10 cycles, ended cycles (the reset proceeded)", sum.Practice)
	}
	if n := countLog(f.agent.log, "cmdr run pause"); n != 1 {
		t.Fatalf("%d run pauses, want 1 (log %v)", n, f.agent.log)
	}
	if n := countLog(f.agent.runVerbs, "resume"); n != 0 {
		t.Fatalf("%d run resumes, want 0 (no run slot left to resume into)", n)
	}
	if sc.evadeN != 1 {
		t.Fatalf("%d evades, want 1", sc.evadeN)
	}
	if !hasNote(f.summary(), "the run already ended before the reset pause") {
		t.Fatalf("notes %v, want the already-ended note", f.summary().Notes)
	}
}

// TestPracticeFailedResetCountedOnce: a reset that did not land is counted once. The pull after it never re-engages,
// and that adds nothing (M1): the practice needs three bad resets, so it makes three, not two.
func TestPracticeFailedResetCountedOnce(t *testing.T) {
	sc := &practiceScenario{noCasts: true}
	f, _, row := practiceFixture(t, sc, 10)
	// Every hire is dead from the first frame on: each attempt ends at once. The evade leaves the boss at 100 % with
	// no target but still flagged in combat, so the reset never sees it home and the pull never re-engages.
	f.agent.onEvade = func(g packet.GUID) {
		sc.mu.Lock()
		sc.evadeN++
		sc.bossHP, sc.target = 100, 0
		sc.mu.Unlock()
	}
	f.agent.onRevive = func(name string) { sc.mu.Lock(); sc.reviveN++; sc.mu.Unlock() }
	f.agent.hostilesFn = func(n int) []tbm.HostileRec {
		f.agent.mu.Lock()
		party := append([]string{}, f.agent.party...)
		started := f.agent.runStarted
		if started {
			for _, name := range party {
				f.agent.dead[name] = true
			}
		}
		sc.mu.Lock()
		target := sc.target
		sc.mu.Unlock()
		f.agent.mu.Unlock()
		return []tbm.HostileRec{{GUID: practiceBossGUID(), Entry: 14834, X: row.X, Y: row.Y, Z: row.Z,
			HPPct: 100, Target: target, Flags: "e"}}
	}
	if code := f.run(context.Background()); code != exitRaidWalkFailed {
		t.Fatalf("exit %d, want %d (notes %v): %s", code, exitRaidWalkFailed, f.summaryNotes(), f.stderr.String())
	}
	sum := f.summary()
	if sum.Practice == nil || sum.Practice.Ended != "3 failed resets" {
		t.Fatalf("practice %+v, want 3 failed resets", sum.Practice)
	}
	// With the bad reset counted twice the practice would stop after two resets.
	if sum.Practice.Attempts != 3 || sc.evadeN != 3 {
		t.Fatalf("%d attempts, %d evades; want 3 and 3 (a bad reset is one failure, not two)", sum.Practice.Attempts, sc.evadeN)
	}
}

// TestPracticeBindRefusesUnengagedCandidate: the frames name one engaged guid, but the client's units of the entry hold
// only a different one (a single trash unit of the entry). The bind is refused before any GM action (M2).
func TestPracticeBindRefusesUnengagedCandidate(t *testing.T) {
	sc := &practiceScenario{}
	f, _, row := practiceFixture(t, sc, 10)
	f.agent.hostilesFn = func(n int) []tbm.HostileRec {
		return []tbm.HostileRec{{GUID: practiceBossGUID(), Entry: 14834, X: row.X, Y: row.Y, Z: row.Z,
			HPPct: 100, Target: 1000, Flags: "-"}}
	}
	f.agent.unitsFn = func(n int) []objects.Unit {
		return []objects.Unit{
			{GUID: practiceTrashGUID(), Entry: 14834, Type: "creature", Health: 42000, MaxHealth: 42000},
			{GUID: practiceSonGUID(), Entry: 11357, Type: "creature", Health: 100, MaxHealth: 100},
		}
	}
	if code := f.run(context.Background()); code != exitRaidWalkFailed {
		t.Fatalf("exit %d, want %d (notes %v): %s", code, exitRaidWalkFailed, f.summaryNotes(), f.stderr.String())
	}
	sum := f.summary()
	if sum.Practice == nil || sum.Practice.Ended != "boss not bound" {
		t.Fatalf("practice %+v, want the not-bound ending", sum.Practice)
	}
	if !strings.Contains(sum.EndReason, "is not among the client's") {
		t.Fatalf("end reason %q, want the engaged-guid refusal", sum.EndReason)
	}
	for _, meth := range []string{"gm_evade ", "gm_set_hp ", "gm_respawn ", "gm_revive "} {
		if n := countLog(f.agent.log, meth); n != 0 {
			t.Fatalf("%d %s calls before the boss was bound (log %v)", n, meth, f.agent.log)
		}
	}
}

// TestPracticeKeptPageTail: the attempt stops at the first counted cast of an events page; the rest of that page is
// kept for the next read (L1). Both casts are served in one page, 45 s apart (each a cycle), so the second row proves
// the tail was not dropped.
func TestPracticeKeptPageTail(t *testing.T) {
	sc := &practiceScenario{}
	var f *fixture
	var firstAt time.Time
	sc.firstCasts = func(start time.Time) {
		firstAt = start
		f.agent.mu.Lock()
		defer f.agent.mu.Unlock()
		f.agent.spellQueue = append(f.agent.spellQueue,
			queuedSpell{at: start, ev: events.Event{Kind: events.KindSpellGo, SourceGUID: practiceBossGUID(), SpellID: 24324,
				Time: start.Add(90 * time.Second)}},
			queuedSpell{at: start, ev: events.Event{Kind: events.KindSpellGo, SourceGUID: practiceBossGUID(), SpellID: 24324,
				Time: start.Add(135 * time.Second)}},
		)
	}
	f, _, _ = practiceFixture(t, sc, 2)
	if code := f.run(context.Background()); code != exitOK {
		t.Fatalf("exit %d, want 0 (notes %v): %s", code, f.summaryNotes(), f.stderr.String())
	}
	if firstAt.IsZero() {
		t.Fatal("the fake never queued the casts")
	}
	rows := f.practiceRows(t)
	if len(rows) != 2 {
		t.Fatalf("%d rows, want 2 (the page's second cast was dropped): %+v", len(rows), rows)
	}
	if rows[0].CastAt != firstAt.Add(90*time.Second).Format(time.RFC3339) ||
		rows[1].CastAt != firstAt.Add(135*time.Second).Format(time.RFC3339) {
		t.Fatalf("rows %s / %s, want the two page casts", rows[0].CastAt, rows[1].CastAt)
	}
	if sum := f.summary(); sum.Practice == nil || sum.Practice.Attempts != 1 || sum.Practice.Cycles != 2 {
		t.Fatalf("practice %+v, want one attempt and 2 cycles", sum.Practice)
	}
}

// attempt1BossGUID is the bound guid in the real attempt-1 frames (zgP-hk5p-1-1/frames.jsonl, 00:05:50-00:06:08):
// a creature guid carrying the 0xF130 high part the frames print.
func attempt1BossGUID() packet.GUID {
	g, err := packet.ParseGUID("0xF1300039F200C20E")
	if err != nil {
		panic(err)
	}
	return g
}

// attempt1Frame is the real attempt-1 frame at 00:06:08.490, read back into a perception frame: Hakkar alive at 94 %,
// target 0x...0CF4 (a bot), with the party standing next to him. The practice must never call this boss gone.
func attempt1Frame() *tbm.ViewFrame {
	return &tbm.ViewFrame{Seq: 741, Hostiles: []tbm.HostileRec{
		{GUID: attempt1BossGUID(), Entry: 14834, X: -11782, Y: -1654.4, Z: 54, HPPct: 94, Target: packet.GUID(0xCF4), Flags: "-"},
	}}
}

// TestPracticeBossFrameSightNotGone: the live failure. Every frame of the practice window held the boss alive (94-99 %,
// same guid), but the walk's tracker row was stamped before the poll's slow event wait, so the old check read the
// boss as absent and three polls ended the practice at 00:06:13, 23 s in. The frame itself is a source: a boss it
// shows is never gone, whatever the tracker's row age.
func TestPracticeBossFrameSightNotGone(t *testing.T) {
	now := time.Date(2026, 11, 12, 0, 6, 8, 0, time.Local)
	clock := now
	w := &walk{
		env:       walkEnv{now: func() time.Time { return clock }},
		opts:      raidWalkOptions{server: "m910q", practiceSustain: true, practiceBossFloor: 40},
		agent:     &practiceMissAgent{},
		hostiles:  map[string]hostileState{}, // the one list the old check read: no row at all
		viewFrame: attempt1Frame(),
		viewAt:    now,
	}
	p := &practiceState{bossGUID: attempt1BossGUID(), bossMax: 42000, bossSeenAt: now}
	for i := 1; i <= 5; i++ {
		clock = clock.Add(5 * time.Second) // each poll's event wait + view, as the live trace ran
		done, err := w.practiceSustainBoss(context.Background(), p)
		if err != nil {
			t.Fatalf("poll %d: %v", i, err)
		}
		if done || p.ended != "" {
			t.Fatalf("poll %d: done %v ended %q, want the boss in the frame to keep the attempt alive", i, done, p.ended)
		}
	}
	if hp, at, ok, err := w.practiceBossSight(context.Background(), p); err != nil || !ok || hp != 94 || !at.Equal(now) {
		t.Fatalf("sight hp %d at %s ok %v, want the 94 %% frame reading", hp, at, ok)
	}
}

// TestPracticeAbsentBossGoneAfterGrace: gone takes the whole grace window with every source missing the guid. One poll,
// a stale tracker row and an old reading are frame gaps, never a despawn.
func TestPracticeAbsentBossGoneAfterGrace(t *testing.T) {
	start := time.Date(2026, 11, 12, 0, 6, 8, 0, time.Local)
	clock := start
	w := &walk{
		env:      walkEnv{now: func() time.Time { return clock }},
		opts:     raidWalkOptions{server: "m910q", practiceSustain: true, practiceBossFloor: 40},
		agent:    &practiceMissAgent{},
		hostiles: map[string]hostileState{},
	}
	p := &practiceState{bossGUID: attempt1BossGUID(), bossMax: 42000, bossSeenAt: start}
	for _, step := range []time.Duration{5 * time.Second, practiceBossGoneGrace / 2, practiceBossGoneGrace - time.Second} {
		clock = start.Add(step)
		if done, err := w.practiceSustainBoss(context.Background(), p); err != nil || done || p.ended != "" {
			t.Fatalf("at +%s: done %v err %v ended %q, want the attempt alive inside the grace", step, done, err, p.ended)
		}
	}
	clock = start.Add(practiceBossGoneGrace)
	done, err := w.practiceSustainBoss(context.Background(), p)
	if err != nil || !done {
		t.Fatalf("at the grace: done %v err %v, want gone", done, err)
	}
	if p.ended != "the boss is gone" {
		t.Fatalf("ended %q, want the boss is gone", p.ended)
	}
	if !strings.Contains(strings.Join(w.notes, "\n"), "every source missed it") {
		t.Fatalf("notes %v, want the grace reason", w.notes)
	}
}

// TestPracticeCommanderDeadIsDead: the commander's unit under the bound guid saying Dead, or at raw health 0, is the
// ONLY death signal. A same-poll frame row at 0 % ("seen, health unknown") never masks it.
func TestPracticeCommanderDeadIsDead(t *testing.T) {
	now := time.Date(2026, 11, 12, 0, 6, 8, 0, time.Local)
	for _, tc := range []struct {
		name string
		unit objects.Unit
	}{
		{"dead flag", objects.Unit{GUID: attempt1BossGUID(), Entry: 14834, Health: 42000, MaxHealth: 42000, Dead: true}},
		{"raw health 0", objects.Unit{GUID: attempt1BossGUID(), Entry: 14834, Health: 0, MaxHealth: 42000}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := &walk{
				env:      walkEnv{now: func() time.Time { return now }},
				opts:     raidWalkOptions{server: "m910q", practiceSustain: true, practiceBossFloor: 40},
				agent:    &practiceUnitsAgent{units: []objects.Unit{tc.unit}},
				hostiles: map[string]hostileState{attempt1BossGUID().String(): {entry: 14834, hppct: 0, at: now}},
			}
			p := &practiceState{bossGUID: attempt1BossGUID(), bossMax: 42000, bossSeenAt: now}
			if !w.practiceBossDead(context.Background(), p) {
				t.Fatal("the commander's dead unit was not read as the boss's death")
			}
			done, err := w.practiceSustainBoss(context.Background(), p)
			if err != nil || !done {
				t.Fatalf("done %v err %v, want the commander's dead reading to end the attempt", done, err)
			}
			if p.ended != "boss killed during practice" {
				t.Fatalf("ended %q, want the practice's kill ending", p.ended)
			}
		})
	}
}

// TestPracticeFrameZeroIsNotDead: a frame row at 0 % is "seen, health unknown": frames round a live boss under 1 % to
// 0, so neither practiceBossDead nor the sustain may read it as a kill. With a live commander unit the sight reports
// its clamped 1 %; without one the frame is still a sighting, just with no health reading.
func TestPracticeFrameZeroIsNotDead(t *testing.T) {
	now := time.Date(2026, 11, 12, 0, 6, 8, 0, time.Local)
	frame := &tbm.ViewFrame{Seq: 742, Hostiles: []tbm.HostileRec{
		{GUID: attempt1BossGUID(), Entry: 14834, HPPct: 0, Flags: "-"},
	}}
	for _, tc := range []struct {
		name   string
		units  []objects.Unit
		wantHP int
	}{
		{"live commander", []objects.Unit{{GUID: attempt1BossGUID(), Entry: 14834, Health: 1, MaxHealth: 200}}, 1},
		{"no commander unit", nil, -1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := &walk{
				env:       walkEnv{now: func() time.Time { return now }},
				opts:      raidWalkOptions{server: "m910q", practiceSustain: true, practiceBossFloor: 40},
				agent:     &practiceUnitsAgent{units: tc.units},
				hostiles:  map[string]hostileState{attempt1BossGUID().String(): {entry: 14834, hppct: 0, at: now.Add(-time.Second)}},
				viewFrame: frame,
				viewAt:    now.Add(-time.Second),
			}
			p := &practiceState{bossGUID: attempt1BossGUID(), bossMax: 200, bossSeenAt: now}
			if w.practiceBossDead(context.Background(), p) {
				t.Fatal("a 0 % frame row was read as the boss's death")
			}
			hp, _, ok, err := w.practiceBossSight(context.Background(), p)
			if err != nil || !ok || hp != tc.wantHP {
				t.Fatalf("sight hp %d ok %v err %v, want the 0 %% frame as a sighting (hp %d)", hp, ok, err, tc.wantHP)
			}
			done, err := w.practiceSustainBoss(context.Background(), p)
			if err != nil || done || p.ended != "" {
				t.Fatalf("sustain done %v err %v ended %q, want the 0 %% frame to keep the attempt alive", done, err, p.ended)
			}
		})
	}
}

// TestPracticeFrameHealthSurvivesPartyTargets: the newest frame names the boss twice, as a hostile row at 42 % and as
// two party members' target. The party rows are hp-unknown evidence at the same frameAt; the known health outranks
// them, so the sight keeps 42 % and the frame's time.
func TestPracticeFrameHealthSurvivesPartyTargets(t *testing.T) {
	now := time.Date(2026, 11, 12, 0, 6, 8, 0, time.Local)
	frame := &tbm.ViewFrame{Seq: 743,
		Hostiles: []tbm.HostileRec{{GUID: attempt1BossGUID(), Entry: 14834, HPPct: 42, Flags: "-"}},
		Party: []tbm.PartyRec{
			{Name: "one", Target: attempt1BossGUID()},
			{Name: "two", Target: attempt1BossGUID()},
		},
	}
	w := &walk{
		env:       walkEnv{now: func() time.Time { return now }},
		opts:      raidWalkOptions{server: "m910q", practiceSustain: true, practiceBossFloor: 40},
		agent:     &practiceUnitsAgent{},
		hostiles:  map[string]hostileState{},
		viewFrame: frame,
		viewAt:    now,
	}
	p := &practiceState{bossGUID: attempt1BossGUID(), bossMax: 42000, bossSeenAt: now}
	hp, at, ok, err := w.practiceBossSight(context.Background(), p)
	if err != nil || !ok || hp != 42 || !at.Equal(now) {
		t.Fatalf("sight hp %d at %s ok %v err %v, want the frame's 42 %% at %s", hp, at, ok, err, now)
	}
}

// TestPracticeBossLiveUnderOnePercentIsNotDead: the commander's percent is integer division, so a live boss with 1 of
// 200 hp reads 0 %. Death is the unit's Dead flag or raw health 0 only: a live unit with positive raw health is at
// least 1 %, never the practice's kill ending.
func TestPracticeBossLiveUnderOnePercentIsNotDead(t *testing.T) {
	now := time.Date(2026, 11, 12, 0, 6, 8, 0, time.Local)
	a := &practiceUnitsAgent{units: []objects.Unit{{
		GUID: attempt1BossGUID(), Entry: 14834, Health: 1, MaxHealth: 200,
	}}}
	w := &walk{
		env:      walkEnv{now: func() time.Time { return now }},
		opts:     raidWalkOptions{server: "m910q", practiceSustain: true, practiceBossFloor: 40},
		agent:    a,
		hostiles: map[string]hostileState{},
	}
	p := &practiceState{bossGUID: attempt1BossGUID(), bossMax: 200, bossSeenAt: now}
	hp, at, ok, err := w.practiceBossSight(context.Background(), p)
	if err != nil || !ok || hp != 1 || !at.Equal(now) {
		t.Fatalf("sight hp %d at %s ok %v err %v, want a 1 %% live reading", hp, at, ok, err)
	}
	done, err := w.practiceSustainBoss(context.Background(), p)
	if err != nil || done || p.ended != "" {
		t.Fatalf("sustain done %v err %v ended %q, want the under-1 %% boss alive", done, err, p.ended)
	}
}

// TestPracticeCommanderUnreadableIsNotGone: frame evidence absent plus a failing commander units read is one unknown
// source, never "the boss is gone": the gone timer holds while it fails, and practiceCommanderUnreadable of failures
// ends the attempt with the distinct reason "commander unreadable".
func TestPracticeCommanderUnreadableIsNotGone(t *testing.T) {
	start := time.Date(2026, 11, 12, 0, 6, 8, 0, time.Local)
	clock := start
	w := &walk{
		env:      walkEnv{now: func() time.Time { return clock }},
		opts:     raidWalkOptions{server: "m910q", practiceSustain: true, practiceBossFloor: 40},
		agent:    &practiceUnitsAgent{err: errors.New("commander units: no reply")},
		hostiles: map[string]hostileState{},
	}
	p := &practiceState{bossGUID: attempt1BossGUID(), bossMax: 42000, bossSeenAt: start}
	clock = start.Add(5 * time.Second)
	if done, err := w.practiceSustainBoss(context.Background(), p); err != nil || done || p.ended != "" {
		t.Fatalf("the first failed read: done %v err %v ended %q, want the attempt alive", done, err, p.ended)
	}
	unknownAt := clock
	for _, step := range []time.Duration{practiceBossGoneGrace, practiceCommanderUnreadable / 2, practiceCommanderUnreadable - time.Second} {
		clock = unknownAt.Add(step)
		if done, err := w.practiceSustainBoss(context.Background(), p); err != nil || done || p.ended != "" {
			t.Fatalf("commander unknown %s with no frame evidence: done %v err %v ended %q, want no gone", step, done, err, p.ended)
		}
	}
	clock = unknownAt.Add(practiceCommanderUnreadable)
	done, err := w.practiceSustainBoss(context.Background(), p)
	if err != nil || !done {
		t.Fatalf("commander unknown for %s: done %v err %v, want the distinct end", practiceCommanderUnreadable, done, err)
	}
	if p.ended != "commander unreadable" {
		t.Fatalf("ended %q, want commander unreadable (gone would blame a source that was never readable)", p.ended)
	}
	if !strings.Contains(strings.Join(w.notes, "\n"), "unreadable") {
		t.Fatalf("notes %v, want the commander-unreadable reason", w.notes)
	}
}

// TestPracticeUnresolvedRestoreIsNotGone: a gm_set_hp the client cannot resolve is one source missing the unit while
// the frames still show it: a note, not a despawn. The attempt goes on and the next restore still runs.
func TestPracticeUnresolvedRestoreIsNotGone(t *testing.T) {
	now := time.Date(2026, 11, 12, 0, 6, 8, 0, time.Local)
	a := &practiceMissAgent{err: fmt.Errorf("gm set hp: %w", session.ErrUnitNotInView)}
	w := &walk{
		env:      walkEnv{now: func() time.Time { return now }},
		opts:     raidWalkOptions{server: "m910q", practiceSustain: true, practiceBossFloor: 40},
		agent:    a,
		hostiles: map[string]hostileState{attempt1BossGUID().String(): {entry: 14834, hppct: 30, at: now}},
	}
	p := &practiceState{bossGUID: attempt1BossGUID(), bossMax: 42000, bossSeenAt: now}
	for i := 1; i <= 3; i++ {
		done, err := w.practiceSustainBoss(context.Background(), p)
		if err != nil || done || p.ended != "" {
			t.Fatalf("poll %d: done %v err %v ended %q, want the attempt alive", i, done, err, p.ended)
		}
	}
	if a.calls != 3 {
		t.Fatalf("%d gm_set_hp calls, want one per poll", a.calls)
	}
	if !strings.Contains(strings.Join(w.notes, "\n"), "the frames still show it") {
		t.Fatalf("notes %v, want the unresolved restore noted", w.notes)
	}
}

// practiceMissAgent answers gm_set_hp with err and counts the calls: a client that cannot resolve the bound unit. Every
// other method (units) answers nothing: no client-side source.
type practiceMissAgent struct {
	mu    sync.Mutex
	calls int
	err   error
}

func (a *practiceMissAgent) Call(ctx context.Context, method string, params, out any, timeout time.Duration) error {
	if method != "gm_set_hp" {
		return nil
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	a.calls++
	return a.err
}

// practiceUnitsAgent is the commander's own client with a fixed unit list: the units read answers units, or its error
// when err is set (an unreadable source). Every other RPC answers nil, so no GM hand is faked.
type practiceUnitsAgent struct {
	units []objects.Unit
	err   error
}

func (a *practiceUnitsAgent) Call(ctx context.Context, method string, params, out any, timeout time.Duration) error {
	if method != "units" {
		return nil
	}
	if a.err != nil {
		return a.err
	}
	if us, ok := out.(*[]objects.Unit); ok {
		*us = a.units
	}
	return nil
}

// TestPracticeBossStaysAliveAcrossSlowPolls is the live failing case (m910q, zgP-hk5p-1-1 attempt 1): the frames hold
// Hakkar at 94 % with the same guid through the whole practice window, but every poll waits ~5 s for its event page
// before it fetches the frame. The old check stamped the frame with the poll's start, read every poll as "the frames
// do not show the boss" and ended the practice 23 s in with exit 18, "the boss is gone". The frames are a source: the
// practice must run on to its own wall-time ceiling instead.
func TestPracticeBossStaysAliveAcrossSlowPolls(t *testing.T) {
	sc := &practiceScenario{noCasts: true, sonDead: true}
	f, _, _ := practiceFixture(t, sc, 10)
	sc.mu.Lock()
	sc.bossHP = 94 // the attempt-1 frame at 00:06:08.490: Hakkar 94 %, target a bot
	sc.mu.Unlock()
	f.opts.practiceMax = 40 * time.Second
	f.agent.viewHook = func() {
		f.agent.mu.Lock()
		started := f.agent.runStarted
		f.agent.mu.Unlock()
		if started {
			_ = f.clock.sleep(context.Background(), 5*time.Second) // the poll's event wait, before the frame read
		}
	}
	code := f.run(context.Background())
	sum := f.summary()
	if sum.Practice == nil {
		t.Fatalf("practice summary missing: %s", f.stderr.String())
	}
	if sum.Practice.Ended == "the boss is gone" || strings.Contains(sum.EndReason, "the boss is gone") {
		t.Fatalf("practice ended %q while the frames held the boss at 94 %% (notes %v)", sum.Practice.Ended, sum.Notes)
	}
	if code != exitRaidWalkFailed || sum.Practice.Ended != "max time" {
		t.Fatalf("exit %d practice %+v, want the 40 s max-time end", code, sum.Practice)
	}
}

// TestPracticeBossDespawnEnds: a boss that leaves every source (a despawn, not a slow poll) ends the attempt with "the
// boss is gone" once the whole grace window has passed without a sighting from any of them.
func TestPracticeBossDespawnEnds(t *testing.T) {
	sc := &practiceScenario{noCasts: true, sonDead: true, goneAfter: 10 * time.Second}
	f, _, _ := practiceFixture(t, sc, 10)
	f.opts.practiceMax = 5 * time.Minute
	code := f.run(context.Background())
	sum := f.summary()
	if code != exitRaidWalkFailed {
		t.Fatalf("exit %d, want %d (%s)", code, exitRaidWalkFailed, f.stderr.String())
	}
	if sum.Practice == nil || sum.Practice.Ended != "the boss is gone" {
		t.Fatalf("practice %+v, want the boss-is-gone ending", sum.Practice)
	}
	if !hasNote(sum, "every source missed it") {
		t.Fatalf("notes %v, want the grace reason", sum.Notes)
	}
}

// TestPracticeFightIDsCoverThePull: the practice's fight_ids reach practiceFightIDBack before its own start, so the
// first pull's fight (which starts at the engagement, a few seconds before the bind) is skipped by the tools too.
func TestPracticeFightIDsCoverThePull(t *testing.T) {
	start := time.Date(2026, 10, 29, 20, 0, 0, 0, time.Local)
	w := &walk{practice: &practiceState{startedAt: start}, fightRows: []walkFightRow{
		{ID: "the-pull", Start: start.Add(-10 * time.Second).Unix()},
		{ID: "long-before", Start: start.Add(-time.Minute).Unix()},
		{ID: "during", Start: start.Add(time.Minute).Unix()},
	}}
	got := w.practiceFightIDs()
	if len(got) != 2 || got[0] != "during" || got[1] != "the-pull" {
		t.Fatalf("fight ids %v, want [during the-pull]", got)
	}
}
