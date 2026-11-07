package main

// The practice loop's tests, on the walk's fake environment: the clock, the frames, the events and the fake agent's
// practice GM hands. They pin the two-attempt shape (5 siphon cycles each, one reset with evade, revive, respawn and
// resume), the timer fallback when the cast events are missing, a boss death ending the practice without a kill, the
// three-failed-resets stop, the never-unselected respawn rule and the wall-time ceiling.

import (
	"context"
	"encoding/json"
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
	"github.com/pjerra/turtle-agent/internal/tbm"
)

// practiceBossGUID and practiceSonGUID are the practice boss's and Son's frame keys: creature guids encode their entry.
func practiceBossGUID() packet.GUID {
	return packet.GUID(uint64(packet.HighUnit)<<48 | 14834<<24 | 0x77)
}
func practiceSonGUID() packet.GUID {
	return packet.GUID(uint64(packet.HighUnit)<<48 | 11357<<24 | 0x21)
}

// practiceScenario is the fake test server's practice state: the boss's HP and target, the Son's death and which hires
// are dead. The hooks below model what each GM command does.
type practiceScenario struct {
	mu        sync.Mutex
	armed     bool
	bossHP    int
	target    packet.GUID
	sonDead   bool
	dead      map[string]bool
	dropAt    time.Time
	dropped   bool
	killBoss  bool
	noCasts   bool
	evadeN    int
	respawnN  int
	reviveN   int
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

	enqueue := func(startAt time.Time) {
		f.agent.mu.Lock()
		defer f.agent.mu.Unlock()
		for k := 1; k <= 5; k++ {
			at := startAt.Add(time.Duration(k*90) * time.Second)
			f.agent.spellQueue = append(f.agent.spellQueue, queuedSpell{at: at,
				ev: events.Event{Kind: events.KindSpellStart, SourceGUID: practiceBossGUID(), SpellID: 24324, Time: at}})
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
		hp, sonDead := sc.bossHP, sc.sonDead
		sc.mu.Unlock()
		return []objects.Unit{
			{GUID: practiceBossGUID(), Entry: 14834, Type: "creature", Health: uint32(hp * 420), MaxHealth: 42000, Dead: hp == 0},
			{GUID: practiceSonGUID(), Entry: 11357, Type: "creature", Health: map[bool]uint32{true: 0, false: 100}[sonDead],
				MaxHealth: 100, Dead: sonDead},
		}
	}
	f.agent.onEvade = func(g packet.GUID) {
		sc.mu.Lock()
		sc.evadeN++
		sc.bossHP, sc.target = 100, 0
		hook := sc.onEvade
		sc.mu.Unlock()
		if hook != nil {
			hook()
		}
	}
	f.agent.onSetHP = func(g packet.GUID, hp uint32) {
		sc.mu.Lock()
		sc.bossHP = int(hp / 420)
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
// sequence is stop, evade, revive, respawn, resume; the sustain revives a dead hire and restores the boss's HP.
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
	// The next attempt's casts are queued when the boss is evaded, as the live core's Reset() starts them again.
	sc.mu.Lock()
	sc.onEvade = func() {
		f.agent.mu.Lock()
		defer f.agent.mu.Unlock()
		start := f.clock.now()
		for k := 1; k <= 5; k++ {
			at := start.Add(time.Duration(k*90) * time.Second)
			f.agent.spellQueue = append(f.agent.spellQueue, queuedSpell{at: at,
				ev: events.Event{Kind: events.KindSpellStart, SourceGUID: practiceBossGUID(), SpellID: 24324, Time: at}})
		}
	}
	sc.mu.Unlock()
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
	if rows[0].SonsKnown != 1 || rows[0].BossHPPct >= f.opts.practiceBossFloor || !rows[0].Sustain.BossHPSet || len(rows[0].Sustain.Revived) != 1 {
		t.Fatalf("cycle 1 row does not hold the sustain: %+v", rows[0])
	}
	log := f.agent.log
	if n := countLog(log, "gm .bot run stop"); n != 1 {
		t.Fatalf("%d run stops, want 1 (log %v)", n, log)
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
	// The reset's order: stop, evade, revive, respawn, and the resume after them.
	idx := func(prefix string) int {
		for i, l := range log {
			if strings.HasPrefix(l, prefix) {
				return i
			}
		}
		return -1
	}
	stop, evade, respawn, inst := idx("gm .bot run stop"), idx("gm_evade "), idx("gm_respawn "), idx("gm .debug instancedata")
	if stop < 0 || evade < 0 || respawn < 0 || inst < 0 || !(stop < evade && evade < respawn && respawn < inst) {
		t.Fatalf("reset order stop %d, evade %d, respawn %d, instancedata %d (log %v)", stop, evade, respawn, inst, log)
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
	sc.mu.Lock()
	sc.onEvade = func() {
		f.agent.mu.Lock()
		defer f.agent.mu.Unlock()
		start := f.clock.now()
		for k := 1; k <= 5; k++ {
			at := start.Add(time.Duration(k*90) * time.Second)
			f.agent.spellQueue = append(f.agent.spellQueue, queuedSpell{at: at,
				ev: events.Event{Kind: events.KindSpellStart, SourceGUID: practiceBossGUID(), SpellID: 24324, Time: at}})
		}
	}
	sc.mu.Unlock()
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
