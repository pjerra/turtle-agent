package main

// The practice loop's tests, on the walk's fake environment: the clock, the frames, the events and the fake agent's
// practice GM hands. They pin the two-attempt shape (5 siphon cycles each, one reset with evade, revive, respawn and
// resume), the timer fallback when the cast events are missing, a boss death ending the practice without a kill, the
// three-failed-resets stop, the never-unselected respawn rule and the wall-time ceiling, plus the re-engagement clock,
// the spell_go-only cycle rule, the boss-guid binding, the never-engaged summary and the practice fight ids.

import (
	"context"
	"encoding/json"
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
	evadeN     int
	respawnN   int
	reviveN    int
	onEvade    func()
	attemptAt  time.Time
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
		sc.mu.Unlock()
		out := []tbm.HostileRec{{GUID: practiceBossGUID(), Entry: 14834, X: row.X, Y: row.Y, Z: row.Z,
			HPPct: hp, Target: target, Flags: "-"}}
		if !sonDead {
			out = append(out, tbm.HostileRec{GUID: practiceSonGUID(), Entry: 11357, X: row.X + 3, Y: row.Y, Z: row.Z,
				HPPct: 100, Flags: "-"})
		}
		return out
	}
	f.agent.unitsFn = func(n int) []objects.Unit {
		sc.mu.Lock()
		hp, sonDead, trash := sc.bossHP, sc.sonDead, sc.extraTrash
		sc.mu.Unlock()
		out := []objects.Unit{
			{GUID: practiceBossGUID(), Entry: 14834, Type: "creature", Health: uint32(hp * 420), MaxHealth: 42000, Dead: hp == 0},
			{GUID: practiceSonGUID(), Entry: 11357, Type: "creature", Health: map[bool]uint32{true: 0, false: 100}[sonDead],
				MaxHealth: 100, Dead: sonDead},
		}
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

// TestPracticeBossSampleFreshness: a stale or absent boss frame is no sample (L2): the sustain must not restore on it.
func TestPracticeBossSampleFreshness(t *testing.T) {
	now := time.Date(2026, 10, 29, 20, 0, 0, 0, time.Local)
	w := &walk{env: walkEnv{now: func() time.Time { return now }},
		hostiles: map[string]hostileState{practiceBossGUID().String(): {entry: 14834, hppct: 30, at: now.Add(-5 * time.Second)}}}
	p := &practiceState{bossGUID: practiceBossGUID(), bossMax: 42000}
	if hp, fresh := w.practiceBossSample(p); fresh || hp != 30 {
		t.Fatalf("hp %d fresh %v on a 5 s old frame, want stale", hp, fresh)
	}
	h := w.hostiles[practiceBossGUID().String()]
	h.at = now.Add(-time.Second)
	w.hostiles[practiceBossGUID().String()] = h
	if hp, fresh := w.practiceBossSample(p); !fresh || hp != 30 {
		t.Fatalf("hp %d fresh %v on a 1 s old frame, want fresh 30", hp, fresh)
	}
	delete(w.hostiles, practiceBossGUID().String())
	if _, fresh := w.practiceBossSample(p); fresh {
		t.Fatal("an absent frame read as fresh")
	}
}

// TestPracticeMissingUnitMissSkipsPoll: a gm_set_hp whose unit the client cannot find is a missed poll, not exit 18
// (L2). Three misses in a row end the attempt, and the practice goes on to collect its cycles.
func TestPracticeMissingUnitMissSkipsPoll(t *testing.T) {
	sc := &practiceScenario{sonDead: true}
	f, _, _ := practiceFixture(t, sc, 10)
	f.agent.setHPErr = fmt.Errorf("gm set hp: %w", session.ErrUnitNotInView)
	if code := f.run(context.Background()); code != exitOK {
		t.Fatalf("exit %d, want 0 (notes %v): %s", code, f.summaryNotes(), f.stderr.String())
	}
	sum := f.summary()
	if sum.Practice == nil || sum.Practice.Ended != "cycles" || sum.Practice.Cycles != 10 {
		t.Fatalf("practice %+v, want 10 cycles", sum.Practice)
	}
	if !hasNote(sum, "miss 3 of 3") || !hasNote(sum, "ending the attempt") {
		t.Fatalf("notes %v, want the missing-unit misses and the attempt end", sum.Notes)
	}
	if n := countLog(f.agent.log, "gm_set_hp "); n != practiceHPMisses {
		t.Fatalf("%d hp restores, want the %d missed polls of the first attempt (log %v)", n, practiceHPMisses, f.agent.log)
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
