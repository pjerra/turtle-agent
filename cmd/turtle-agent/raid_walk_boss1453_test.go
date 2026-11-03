package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/pjerra/turtle-agent/internal/dungeon"
	"github.com/pjerra/turtle-agent/internal/events"
	"github.com/pjerra/turtle-agent/internal/packet"
	"github.com/pjerra/turtle-agent/internal/planexport"
	"github.com/pjerra/turtle-agent/internal/raidwalk"
	"github.com/pjerra/turtle-agent/internal/tbm"
)

// zgWalk25BossGUID is the unit guid the Zul'Gurub walk 25 test2 r1 frames carried (frames.jsonl): the spawn's
// guid, whose entry reads 14599 (High Priest Thekal) at first and 14509 after his phase change. A script that
// changes a creature's entry (core UpdateEntry) keeps the guid, and the guid still carries the spawn's entry.
const zgWalk25BossGUID = packet.GUID(0xF13000390700C09E)

// zgWalk25AddGUIDs are the Zulian Guardian guids of the same dump: 0xF130003ADC... carries entry 15068, the pair
// the encounter summons together (boss_thekal.cpp:41/:413), never the boss reborn.
var zgWalk25AddGUIDs = []packet.GUID{0xF130003ADC3D0D34, 0xF130003ADC3D0D35}

// zgStageFrames keys the frames of a Thekal replay on the run status the same tick read: the events hook of the
// fake runs before the view, so the frames answer for the line that just arrived.
func zgStageFrames(f *fixture, boss, total int) (stage *string, seen map[string]int) {
	s := ""
	m := map[string]int{}
	f.agent.eventsHook = func(raw string) {
		switch {
		case strings.Contains(raw, fmt.Sprintf("|%d/%d|", boss-1, total)):
			s = "fight"
		case strings.Contains(raw, fmt.Sprintf("|%d/%d|", boss, total)):
			s = "boss"
		case strings.Contains(raw, fmt.Sprintf("|%d/%d|", boss+1, total)):
			s = "after"
		}
	}
	return &s, m
}

// zgThekalBossRowPos puts the main tank at Thekal's boss row: the early hand-over of an engagement on the step
// before the boss step needs the party at the pull.
func zgThekalBossRowPos(f *fixture, boss int) {
	rows := zgRows(f.t)
	f.agent.pos = map[string][3]float32{"Bot01": {rows.Steps[boss-1].X, rows.Steps[boss-1].Y, rows.Steps[boss-1].Z}}
}

// TestRaidWalkThekalWalk25R1BossKill replays ZG walk 25 test2 r1 from 22:41:06 (spec 6, D5): the run is on the
// move step before Thekal's boss step, the frames show his guid engaged (the early hand-over lets the run fight
// him), the same guid changes entry 14599 -> 14509 while two 15068 Zulian Guardians stand up, 14509 is last seen
// at 6 %, and the until stop ends the run right after the boss step.
//
// This is TB's REAL sequence: NextStep overwrites the boss step's "boss down" note with the until stop's
// "stopped before step N (...)" in the same call, so no `boss down` line is ever sent when the boss is the row
// before the stop (runtime/DungeonRunPolicy.h, StopAtUntil). The walk credits the end right after the boss step
// (bossEndStatus): the frames saw the guid engaged and the end note does not name the boss as skipped. No pause and
// no `.bot run skip` are needed, and the phase comes from the entries the frames saw on the boss's guid: 1 + the
// one change = 2, the last entry 14509. The steps are the rows the walk walks (the plan JSON: Thekal's boss row is
// step 127 of its rows; the live walk's deployed rows had 274 of 1095, the same shape).
func TestRaidWalkThekalWalk25R1BossKill(t *testing.T) {
	f := zgThekalFixture(t, "zul_gurub")
	boss := zgBossStep(t)
	total := len(zgRows(t).Steps)
	f.agent.startReply = zgThekalStart(total, boss)
	stage, seen := zgStageFrames(f, boss, total)
	f.agent.mu.Lock()
	f.agent.startQueue = []queuedEvent{
		{raw: fmt.Sprintf("TBM:RUN|zul_gurub|running|%d/%d|high_priest_thekal|move|walk|fighting|until step %d (step %d)", boss-1, total, boss+1, boss+1)},
		{raw: fmt.Sprintf("TBM:RUN|zul_gurub|running|%d/%d|high_priest_thekal|boss|begin|arrived|until step %d (step %d)", boss, total, boss+1, boss+1)},
		{raw: fmt.Sprintf("TBM:RUN|zul_gurub|done|%d/%d|high_priest_thekal|move|walk|stopped before step %d (until step %d): the party stays where it stands", boss+1, total, boss+1, boss+1)},
	}
	f.agent.mu.Unlock()
	zgThekalBossRowPos(f, boss)
	f.agent.hostilesFn = func(n int) []tbm.HostileRec {
		switch *stage {
		case "fight", "boss":
			seen["live"]++
		default:
			return nil
		}
		switch {
		case seen["live"] == 1:
			return []tbm.HostileRec{{GUID: zgWalk25BossGUID, Entry: 14599, HPPct: 100, Target: packet.GUID(1000)}}
		case seen["live"] == 2:
			// The guid changes entry while the two 15068 Zulian Guardians stand up (22:49:19-22:49:45 of the dump).
			return []tbm.HostileRec{
				{GUID: zgWalk25BossGUID, Entry: 14509, HPPct: 6, Target: packet.GUID(1000)},
				{GUID: zgWalk25AddGUIDs[0], Entry: 15068, HPPct: 90, Target: packet.GUID(1000)},
				{GUID: zgWalk25AddGUIDs[1], Entry: 15068, HPPct: 80, Target: packet.GUID(1000)},
			}
		}
		return nil
	}
	code := f.run(context.Background())
	if code != exitOK {
		t.Fatalf("exit %d, want 0: %s\n%s", code, f.stderr.String(), f.stdout.String())
	}
	sum := f.summary()
	if len(sum.Pauses) != 0 {
		t.Fatalf("pauses %+v, want none: the run's own status reported the kill", sum.Pauses)
	}
	f.agent.mu.Lock()
	verbs := append([]string{}, f.agent.runVerbs...)
	f.agent.mu.Unlock()
	if countPrefix(verbs, "skip") != 0 {
		t.Fatalf("run verbs %v, want no skip: the run leader found the boss himself", verbs)
	}
	if sum.BossKill == nil {
		t.Fatalf("boss_kill absent, want entry 14599 at step %d, killed, phase 2 (14509)", boss)
	}
	k := *sum.BossKill
	if k.Entry != 14599 || k.Step != boss || !k.Killed || k.Phase != 2 || k.PhaseEntry != 14509 ||
		k.Evidence != "run ended right after the boss step; engaged" {
		t.Fatalf("boss_kill %+v, want {14599 %d true 2 14509 run ended right after the boss step; engaged}", k, boss)
	}
	if line := sum.Text(); !strings.Contains(line, fmt.Sprintf("boss kill: 14599 at step %d, killed, phase 2 (14509): run ended right after the boss step; engaged", boss)) {
		t.Fatalf("summary.txt has no boss kill line:\n%s", line)
	}
}

// zgNoPhaseFixture is the Zul'Gurub walk of a plan copy without `boss.phase_entries`: the boss's reborn form 14509
// is then in no plan field at all, so the guid rule is the only thing that can call it the boss (spec 6, D5 b).
func zgNoPhaseFixture(t *testing.T) *fixture {
	t.Helper()
	raw, err := os.ReadFile("../../dungeons/zul_gurub.json")
	if err != nil {
		t.Fatal(err)
	}
	var keep []string
	for _, l := range strings.Split(string(raw), "\n") {
		if strings.Contains(l, `"phase_entries"`) {
			continue
		}
		keep = append(keep, l)
	}
	dir := t.TempDir()
	plan := filepath.Join(dir, "zul_gurub.json")
	if err := os.WriteFile(plan, []byte(strings.Join(keep, "\n")), 0o600); err != nil {
		t.Fatal(err)
	}
	f := newFixture(t)
	f.opts.dir = dir
	f.opts.plan, f.opts.holder = "zul_gurub", "raid-walk-zul_gurub"
	f.opts.until = fmt.Sprintf("step %d", zgBossStep(t)+1)
	p, err := dungeon.Load(plan)
	if err != nil {
		t.Fatal(err)
	}
	for i := range p.Legs {
		if b := p.Legs[i].Boss; b != nil && b.Entry == 14599 && len(b.PhaseEntries) != 0 {
			t.Fatalf("the plan copy still carries phase_entries %v", b.PhaseEntries)
		}
	}
	f.agent.instMap = p.Map
	return f
}

// TestRaidWalkBossGUIDIsBossUnit is the old-module path (spec 6, D5 b): the run leader never finds the boss and
// pauses with "boss 14599 not found" on the boss's own step, and the frames show the boss's guid last as 14509 at
// 0 %. 14509 is in no plan field (the plan copy has no phase_entries), so only the guid rule can call that row a
// boss unit: the walk skips the step once. The frames also show a second unit of 14509 standing while the boss's
// guid is alive (the pair of the same entry the add rule watches): a boss unit is never an add, so the boss's own
// 0 % row is still the kill.
func TestRaidWalkBossGUIDIsBossUnit(t *testing.T) {
	f := zgNoPhaseFixture(t)
	boss := zgBossStep(t)
	total := len(zgRows(t).Steps)
	f.opts.pausedWait = 30 * time.Second
	f.agent.startReply = zgThekalStart(total, boss)
	stage, seen := zgStageFrames(f, boss, total)
	var q []queuedEvent
	for range 12 {
		q = append(q, queuedEvent{raw: fmt.Sprintf("TBM:RUN|zul_gurub|running|%d/%d|high_priest_thekal|boss|begin|boss 14599 not at his spot: waiting for him|until step %d (step %d)", boss, total, boss+1, boss+1)})
	}
	q = append(q, queuedEvent{raw: zgBossNotFoundLine(boss, total, boss+1)})
	f.agent.mu.Lock()
	f.agent.startQueue = q
	f.agent.mu.Unlock()
	zgThekalBossRowPos(f, boss)
	f.agent.hostilesFn = func(n int) []tbm.HostileRec {
		switch *stage {
		case "fight", "boss":
			seen["live"]++
		default:
			return nil
		}
		switch {
		case seen["live"] == 1:
			return []tbm.HostileRec{{GUID: zgWalk25BossGUID, Entry: 14599, HPPct: 60, Target: packet.GUID(1000)}}
		case seen["live"] <= 4:
			// The boss's guid reads 14509 while another unit of the same entry stands: 14509 is an add entry by
			// the pair rule then, and the boss's guid must still outrank it (bossKilled).
			return []tbm.HostileRec{
				{GUID: zgWalk25BossGUID, Entry: 14509, HPPct: 6, Target: packet.GUID(1000)},
				{GUID: zgWalk25AddGUIDs[0], Entry: 14509, HPPct: 6, Target: packet.GUID(1000)},
			}
		}
		return []tbm.HostileRec{{GUID: zgWalk25BossGUID, Entry: 14509, HPPct: 0}}
	}
	zgThekalDoneOnSkip(f, boss, total)
	if code := f.run(context.Background()); code != exitOK {
		t.Fatalf("exit %d, want 0: %s\n%s", code, f.stderr.String(), f.stdout.String())
	}
	f.agent.mu.Lock()
	verbs := append([]string{}, f.agent.runVerbs...)
	f.agent.mu.Unlock()
	if countPrefix(verbs, "skip") != 1 {
		t.Fatalf("run verbs %v, want exactly one skip: the frames show the boss guid dead", verbs)
	}
	sum := f.summary()
	if sum.BossKill == nil || !sum.BossKill.Killed {
		t.Fatalf("boss_kill %+v, want killed", sum.BossKill)
	}
	if k := *sum.BossKill; k.Entry != 14599 || k.Step != boss || k.Phase != 2 || k.PhaseEntry != 14509 {
		t.Fatalf("boss_kill %+v, want entry 14599 at step %d, phase 2 (14509)", k, boss)
	}
}

// TestRaidWalkGuardianDeathIsNoKill: only the 15068 Zulian Guardians' rows reach 0 % (the pair the encounter
// summons together, boss_thekal.cpp:41/:413). An add's corpse is no boss kill: no skip, the pause path is
// unchanged (wait, exit 18), and boss_kill says the kill does not hold.
func TestRaidWalkGuardianDeathIsNoKill(t *testing.T) {
	f := zgThekalFixture(t, "zul_gurub")
	boss := zgBossStep(t)
	total := len(zgRows(t).Steps)
	f.opts.pausedWait = 4 * time.Second
	f.agent.startReply = zgThekalStart(total, boss)
	f.agent.ttFn = ttUnitsFn(t) // no corpse of a boss unit at the boss row
	stage, seen := zgStageFrames(f, boss, total)
	var q []queuedEvent
	for range 12 {
		q = append(q, queuedEvent{raw: fmt.Sprintf("TBM:RUN|zul_gurub|running|%d/%d|high_priest_thekal|boss|begin|boss 14599 not at his spot: waiting for him|until step %d (step %d)", boss, total, boss+1, boss+1)})
	}
	q = append(q, queuedEvent{raw: zgBossNotFoundLine(boss, total, boss+1)})
	f.agent.mu.Lock()
	f.agent.startQueue = q
	f.agent.mu.Unlock()
	zgThekalBossRowPos(f, boss)
	f.agent.hostilesFn = func(n int) []tbm.HostileRec {
		switch *stage {
		case "fight", "boss":
			seen["live"]++
		default:
			return nil
		}
		switch {
		case seen["live"] == 1:
			return []tbm.HostileRec{{GUID: zgWalk25BossGUID, Entry: 14599, HPPct: 100, Target: packet.GUID(1000)}}
		case seen["live"] <= 4:
			// The pair the encounter summons together: two LIVE 15068 rows at once are an add, never the boss.
			return []tbm.HostileRec{
				{GUID: zgWalk25AddGUIDs[0], Entry: 15068, HPPct: 90, Target: packet.GUID(1000)},
				{GUID: zgWalk25AddGUIDs[1], Entry: 15068, HPPct: 80, Target: packet.GUID(1000)},
			}
		case seen["live"] <= 7:
			return []tbm.HostileRec{{GUID: zgWalk25AddGUIDs[0], Entry: 15068, HPPct: 0}}
		}
		return nil
	}
	code := f.run(context.Background())
	if code != exitRaidWalkFailed {
		t.Fatalf("exit %d, want 18: %s\n%s", code, f.stderr.String(), f.stdout.String())
	}
	f.agent.mu.Lock()
	verbs := append([]string{}, f.agent.runVerbs...)
	f.agent.mu.Unlock()
	if countPrefix(verbs, "skip") != 0 {
		t.Fatalf("run verbs %v, want no skip: a guardian's corpse is no boss kill", verbs)
	}
	sum := f.summary()
	if sum.BossKill == nil {
		t.Fatalf("boss_kill absent: the boss was engaged")
	}
	if sum.BossKill.Killed {
		t.Fatalf("boss_kill %+v, want not killed: a guardian's corpse is no boss kill", *sum.BossKill)
	}
}

// TestRaidWalkBossKillNotKilled: the run reaches the boss step, the run leader fights the boss, and then the party
// is gone (a wipe) while the run pauses on the boss step. The walk never sees a `boss down` note: boss_kill says
// not killed, with the reason, and the walk keeps the old pause ending (exit 18, no skip).
func TestRaidWalkBossKillNotKilled(t *testing.T) {
	f := zgThekalFixture(t, "zul_gurub")
	boss := zgBossStep(t)
	total := len(zgRows(t).Steps)
	f.opts.pausedWait = 4 * time.Second
	f.agent.startReply = zgBossStepStart(total, boss)
	f.agent.eventsHook = func(raw string) {
		if strings.Contains(raw, "|running|") {
			f.agent.mu.Lock()
			f.agent.noPartyFrames = true
			f.agent.mu.Unlock()
		}
	}
	f.agent.hostilesFn = func(n int) []tbm.HostileRec {
		// Hurt: the boss latch fires with no party frame to read a target from (the hires are gone).
		return []tbm.HostileRec{{GUID: zgWalk25BossGUID, Entry: 14599, HPPct: 90, Target: packet.GUID(1000)}}
	}
	f.agent.mu.Lock()
	f.agent.startQueue = []queuedEvent{
		{raw: fmt.Sprintf("TBM:RUN|zul_gurub|running|%d/%d|high_priest_thekal|boss|begin|fighting|until step %d (step %d)", boss, total, boss+1, boss+1)},
		{raw: fmt.Sprintf("TBM:RUN|zul_gurub|paused|%d/%d|high_priest_thekal|boss|begin|2 dead: regroup (cmd revive), then .bot run resume|until step %d (step %d)", boss, total, boss+1, boss+1)},
	}
	f.agent.mu.Unlock()
	code := f.run(context.Background())
	if code != exitRaidWalkFailed {
		t.Fatalf("exit %d, want 18: %s\n%s", code, f.stderr.String(), f.stdout.String())
	}
	sum := f.summary()
	if sum.BossKill == nil {
		t.Fatalf("boss_kill absent: the boss was engaged at step %d", boss)
	}
	k := *sum.BossKill
	if k.Killed || k.Evidence == "" {
		t.Fatalf("boss_kill %+v, want not killed with a reason", k)
	}
	if line := sum.Text(); !strings.Contains(line, "not killed (") {
		t.Fatalf("summary.txt has no not-killed line:\n%s", line)
	}
}

// zgBossStepStart is the run start's answer for a walk that starts ON the boss step.
func zgBossStepStart(total, boss int) *commanderReply {
	line := fmt.Sprintf("TBM:RUN|zul_gurub|running|%d/%d|high_priest_thekal|boss|begin|fighting|until step %d (step %d)", boss, total, boss+1, boss+1)
	return &commanderReply{Reply: tbm.Reply{Kind: tbm.KindAck, OK: true,
		Ack:   &tbm.Ack{Intent: "run start", Scope: "party", Count: 1, Executor: "-"},
		Line:  line,
		Lines: []string{line, "TBM:ACTION_ACK|run start|party|1|-"}}}
}

// bossSummaryWalk is a walk with just the state the summary reads: the ZG rows and empty boss maps. The summary
// functions do not touch the network or the frames, so the tests drive observeBossStatus and the skip directly.
func bossSummaryWalk(t *testing.T) *walk {
	t.Helper()
	return &walk{
		rows:       zgRows(t),
		bossTracks: map[bossKey]*bossStepTrack{},
		bossGUIDs:  map[string]uint32{},
		bossForms:  map[string][]uint32{},
	}
}

// feedStatus runs one status through the walk's status pipeline the way pollOnce, checkStartLine and
// readEventsPage do: the boss rule sees the PREVIOUS status in lastStatus, then this one replaces it (the end rule
// reads whether the walk stood on the boss step just before).
func feedStatus(w *walk, st tbm.RunStatus, now time.Time) {
	w.observeBossStatus(st, now)
	w.lastStatus, w.haveStatus, w.statusSeq = st, true, w.statusSeq+1
}

// TestBossKillSummariesUnengagedThenKilled is #1453 round 3 (a): a boss row the run only walked past must not be
// marked killed when a later boss row's fight reports `boss down`. Each boss row's step keeps its own record, so the
// first says not engaged and the second says killed.
func TestBossKillSummariesUnengagedThenKilled(t *testing.T) {
	w := bossSummaryWalk(t)
	now := time.Now()
	// Step 38 (the ZG plan's first boss row, entry 14517) is walked past: a move status, not an engagement.
	w.observeBossStatus(tbm.RunStatus{Step: 44, Kind: "move", Note: "walking"}, now)
	// Step 84 (the second boss row, entry 14507) is engaged.
	w.observeBossStatus(tbm.RunStatus{Step: 100, Kind: "boss", Note: "fighting"}, now)
	// The run leaves step 100 and reports the kill on the leaving status.
	w.observeBossStatus(tbm.RunStatus{Step: 101, Kind: "move", Note: "boss down: he died as 14507"}, now)
	list := w.bossKillSummaries()
	if len(list) != 2 {
		t.Fatalf("bossKills %d records, want 2: %+v", len(list), list)
	}
	if k := list[0]; k.Step != 44 || k.Entry != 14517 || k.Engaged || k.Killed {
		t.Fatalf("first record %+v, want step 44 entry 14517 not engaged and not killed", k)
	}
	if k := list[1]; k.Step != 100 || k.Entry != 14507 || !k.Engaged || !k.Killed {
		t.Fatalf("second record %+v, want step 100 entry 14507 engaged and killed", k)
	}
	if single := w.bossKillSummary(); single == nil || single.Step != 100 || !single.Killed {
		t.Fatalf("single boss_kill %+v, want the last engaged boss at step 100 killed", single)
	}
}

// TestBossKillSummariesSkipDoesNotMarkOtherKilled is #1453 round 3 (b): a skip the walk sends for boss B must not
// mark boss A killed. The skip is stored on B's own record; A's stays engaged and not killed.
func TestBossKillSummariesSkipDoesNotMarkOtherKilled(t *testing.T) {
	w := bossSummaryWalk(t)
	now := time.Now()
	// Boss A (step 44) is engaged and leaves without a `boss down` note.
	w.observeBossStatus(tbm.RunStatus{Step: 44, Kind: "boss", Note: "fighting"}, now)
	w.observeBossStatus(tbm.RunStatus{Step: 45, Kind: "move", Note: "walking"}, now)
	// Boss B (step 100) is skipped: the walk's own `.bot run skip` for its step.
	w.bossTrackAt(100, 14507).skipped = true
	w.bossTrackAt(100, 14507).evidence = "14507 seen dead in the frames"
	list := w.bossKillSummaries()
	if len(list) != 2 {
		t.Fatalf("bossKills %d records, want 2: %+v", len(list), list)
	}
	// Sorted by step: A (38) first, B (84) second.
	if k := list[0]; k.Step != 44 || !k.Engaged || k.Killed {
		t.Fatalf("A %+v, want step 44 engaged and not killed", k)
	}
	if k := list[1]; k.Step != 100 || !k.Skipped || !k.Killed {
		t.Fatalf("B %+v, want step 100 skipped and killed (skip is kill evidence)", k)
	}
	// The single field is the last engaged boss: A alone was engaged.
	if single := w.bossKillSummary(); single == nil || single.Step != 44 || single.Killed {
		t.Fatalf("single boss_kill %+v, want A at step 44 not killed", single)
	}
}

// TestBossFormsResetOnWipe is #1453 round 3 (c): a wipe recovery clears the boss guid's form history back to its
// identity entry, so the retry's phase counts only the retry's changes: the wiped attempt's change is gone. The
// summary reads phase 2, not 4.
func TestBossFormsResetOnWipe(t *testing.T) {
	w := bossSummaryWalk(t)
	g := zgWalk25BossGUID.String()
	w.bossGUIDs[g] = 14599
	// The first attempt changes the entry twice: 14599 -> 14509 -> 14599.
	w.bossForms[g] = []uint32{14599, 14509, 14599}
	// A wipe recovery starts: the form history goes back to the identity mapping.
	w.resetBossForms()
	if forms := w.bossForms[g]; len(forms) != 1 || forms[0] != 14599 {
		t.Fatalf("forms after reset %v, want [14599]", forms)
	}
	// The retry changes the entry once: 14599 -> 14509. Without the reset the
	// history would read 4 forms (phase 4); with it the retry reads phase 2.
	w.bossForms[g] = append(w.bossForms[g], 14509)
	tk := w.bossTrackAt(127, 14599)
	tk.guid = g
	k := w.bossKillSummaryFor(tk)
	if k.Phase != 2 || k.PhaseEntry != 14509 {
		t.Fatalf("phase %d entry %d, want 2 14509: the wipe's change must not count", k.Phase, k.PhaseEntry)
	}
}

// TestBossKillSameStepNonBossStatus is #1453 round 4 (a): the boss step reports a boss status, then the SAME step
// reports another kind (the run left the boss track on the step it stood on). The leaving status must be read like
// the first status after the step: either the stored note or its own `boss down` note is the kill. The early return
// every status on a boss row took kept such a kill unread and the record said killed:false.
func TestBossKillSameStepNonBossStatus(t *testing.T) {
	now := time.Now()
	for _, tc := range []struct {
		name        string
		bossKind    tbm.RunStatus // the boss-kind status on the boss step
		leavingKind tbm.RunStatus // the same step reporting another kind
	}{
		{
			name:        "the leaving status carries the kill note",
			bossKind:    tbm.RunStatus{Step: 44, Kind: "boss", Note: "fighting"},
			leavingKind: tbm.RunStatus{Step: 44, Kind: "move", Note: "boss down: he died as 14517"},
		},
		{
			name:        "the boss status carried the kill note",
			bossKind:    tbm.RunStatus{Step: 44, Kind: "boss", Note: "boss down: he died as 14517"},
			leavingKind: tbm.RunStatus{Step: 44, Kind: "move", Note: "walking"},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := bossSummaryWalk(t)
			w.observeBossStatus(tc.bossKind, now)
			w.observeBossStatus(tc.leavingKind, now)
			k := w.bossKillSummary()
			if k == nil || k.Step != 44 || !k.Engaged || !k.Killed || k.Evidence != "boss down: he died as 14517" {
				t.Fatalf("boss_kill %+v, want step 44, engaged, killed, evidence %q", k, "boss down: he died as 14517")
			}
		})
	}
}

// TestBossFormsWipeResetCountsOnlyTheRetry is #1453 round 4 (b): the frames the reset's own polls read — the
// wiped attempt's last form, then the boss's original one — belong to the attempt that wiped. The form history is
// cleared once that reset is over (after the reset wait and the wipe recovery's revive/rest), right before the
// retry's first poll, so the retry's phase counts only the entries the retry saw.
func TestBossFormsWipeResetCountsOnlyTheRetry(t *testing.T) {
	// frame is one stage of the frames: the form under the boss guid and whether it is being fought.
	type frame struct {
		entry  uint32
		hppct  int
		target uint64
	}
	bossFrames := func(fr frame) []tbm.HostileRec {
		return []tbm.HostileRec{{GUID: zgWalk25BossGUID, Entry: fr.entry, HPPct: fr.hppct, Target: packet.GUID(fr.target)}}
	}

	// The wipe-resume path: the wipe lands on the boss step before the run engaged him, the revive and the rest
	// read the wipe's reset (14509, then 14599), and the retry engages him and reports the kill. The phase must be
	// 2 (14509): the reset's frames are not the retry's changes.
	t.Run("wipe-resume", func(t *testing.T) {
		f := zgThekalFixture(t, "zul_gurub")
		boss, total := zgBossStep(t), len(zgRows(t).Steps)
		f.agent.startReply = zgThekalStart(total, boss)
		zgThekalBossRowPos(f, boss)
		var wipeAt time.Time
		var retry bool
		f.agent.mu.Lock()
		f.agent.startQueue = []queuedEvent{
			{raw: fmt.Sprintf("TBM:RUN|zul_gurub|running|%d/%d|high_priest_thekal|move|walk|walking|until step %d (step %d)", boss, total, boss+1, boss+1)},
		}
		f.agent.mu.Unlock()
		f.agent.eventsHook = func(raw string) {
			if !wipeAt.IsZero() || !strings.Contains(raw, fmt.Sprintf("|%d/%d|", boss, total)) {
				return
			}
			wipeAt = f.clock.now()
			f.agent.mu.Lock()
			for _, n := range f.agent.party {
				f.agent.dead[n] = true
			}
			f.agent.mu.Unlock()
		}
		f.agent.hostilesFn = func(int) []tbm.HostileRec {
			now := f.clock.now()
			switch {
			case retry:
				return bossFrames(frame{14509, 6, 1000}) // the retry's fight
			case now.Sub(wipeAt) < 60*time.Second:
				return bossFrames(frame{14599, 100, 0}) // in view, not engaged
			case now.Sub(wipeAt) < 120*time.Second:
				return bossFrames(frame{14509, 100, 0}) // the wiped attempt's last form
			}
			return bossFrames(frame{14599, 100, 0}) // the reset
		}
		f.agent.onResume = func() {
			retry = true
			f.agent.mu.Lock()
			defer f.agent.mu.Unlock()
			f.agent.lines = append(f.agent.lines,
				queuedEvent{raw: fmt.Sprintf("TBM:RUN|zul_gurub|running|%d/%d|high_priest_thekal|boss|begin|arrived|until step %d (step %d)", boss, total, boss+1, boss+1)},
				queuedEvent{raw: fmt.Sprintf("TBM:RUN|zul_gurub|running|%d/%d|high_priest_thekal|move|walk|boss down: he died as 14509|until step %d (step %d)", boss+1, total, boss+1, boss+1)},
				queuedEvent{raw: fmt.Sprintf("TBM:RUN|zul_gurub|done|%d/%d|high_priest_thekal|move|walk|stopped before step %d (until step %d): the party stays where it stands", boss+1, total, boss+1, boss+1)},
			)
		}
		code := f.run(context.Background())
		if code != exitOK {
			t.Fatalf("exit %d, want 0: %s\n%s", code, f.stderr.String(), f.stdout.String())
		}
		sum := f.summary()
		if sum.WipeResumes != 1 {
			t.Fatalf("wipe_resumes %d, want 1: %q", sum.WipeResumes, sum.Notes)
		}
		k := sum.BossKill
		if k == nil || k.Step != boss || !k.Killed || k.Phase != 2 || k.PhaseEntry != 14509 {
			t.Fatalf("boss_kill %+v, want step %d killed, phase 2 (14509): the reset's frames must not count", k, boss)
		}
	})

	// The boss reset's own wait: the run is stopped at the boss step, the raid wipes, and the reset wait reads
	// 14509 then 14599 before it is quiet. The history is cleared after that wait (before the revive): nothing
	// the wait saw is left in the record.
	t.Run("boss reset wait", func(t *testing.T) {
		f := zgThekalFixture(t, "zul_gurub")
		boss, total := zgBossStep(t), len(zgRows(t).Steps)
		// --until boss: the walk owns the fight (a run line of kind boss would let the run leader fight it).
		f.opts.until = "boss"
		firstBoss := 0
		for i, r := range zgRows(t).Steps {
			if r.Kind == planexport.KindBoss {
				firstBoss = i + 1
				break
			}
		}
		line := fmt.Sprintf("TBM:RUN|zul_gurub|running|%d/%d|high_priest_thekal|move|walk|walking|until boss (step %d)", boss-1, total, firstBoss)
		f.agent.startReply = &commanderReply{Reply: tbm.Reply{Kind: tbm.KindAck, OK: true,
			Ack:  &tbm.Ack{Intent: "run start", Scope: "party", Count: 1, Executor: "-"},
			Line: line, Lines: []string{line, "TBM:ACTION_ACK|run start|party|1|-"}}}
		f.agent.mu.Lock()
		f.agent.startQueue = []queuedEvent{
			{raw: fmt.Sprintf("TBM:RUN|zul_gurub|running|%d/%d|high_priest_thekal|boss|begin|arrived|until boss (step %d)", boss, total, firstBoss)},
		}
		f.agent.mu.Unlock()
		// The first watch frame shows the boss engaged (the boss path takes the walk), the second kills the raid
		// (the frame's party, read before the hook, is still alive; the wipe lands on the next frame).
		var wipeAt time.Time
		watchFrames := 0
		f.agent.hostilesFn = func(int) []tbm.HostileRec {
			if !f.agent.runStarted {
				return nil // the entry's own views see no boss: nothing latches before the fight
			}
			watchFrames++
			now := f.clock.now()
			switch {
			case watchFrames <= 2:
				if watchFrames == 2 {
					wipeAt = now
					f.agent.mu.Lock()
					for _, n := range f.agent.party {
						f.agent.dead[n] = true
					}
					f.agent.mu.Unlock()
				}
				return bossFrames(frame{14599, 100, 1000}) // engaged: the boss path
			case now.Sub(wipeAt) < 30*time.Second:
				return bossFrames(frame{14599, 100, 0}) // the target gone: the reset is read from its age
			case now.Sub(wipeAt) < 40*time.Second:
				return bossFrames(frame{14509, 100, 0}) // the wiped attempt's last form, during the reset wait
			}
			return bossFrames(frame{14599, 100, 0}) // the reset
		}
		code := f.run(context.Background())
		if code != exitRaidWalkIncident {
			t.Fatalf("exit %d, want 19: %s\n%s", code, f.stderr.String(), f.stdout.String())
		}
		sum := f.summary()
		k := sum.BossKill
		if k == nil || k.Step != boss || k.Killed {
			t.Fatalf("boss_kill %+v, want step %d not killed (the wipe)", k, boss)
		}
		if k.Phase != 1 || k.PhaseEntry != 14599 {
			t.Fatalf("phase %d (%d), want 1 (14599): the reset wait's own frames must not count", k.Phase, k.PhaseEntry)
		}
	})
}

// TestBossKillRunEndedAfterBossStep is H1 (#1453 walk side): TB's NextStep overwrites the boss step's `boss down`
// note with the until stop's "stopped before step N (...)" (StopAtUntil) or DoneNote's "plan done (...)" in the same
// call, so a plan that ends on a boss never sends `boss down` at all (43 of 46 plans). The walk credits the end when
// its last status stood on the boss step, the end note does not name the boss in its "boss skipped, not seen
// killed: ..." list, and the frames saw the boss guid dead in this attempt or the boss step was engaged.
func TestBossKillRunEndedAfterBossStep(t *testing.T) {
	now := time.Now()
	// The until stop's real line: the run stood on the boss step (38, entry 14517) and stops before step 45.
	stop := tbm.RunStatus{State: tbm.RunDone, Step: 45, Kind: "move",
		Note: "stopped before step 45 (until step 45): the party stays where it stands"}
	for _, tc := range []struct {
		name   string
		before func(w *walk)
		killed bool
		ev     string
	}{
		{
			name:   "engaged then the until stop",
			before: func(w *walk) {},
			killed: true,
			ev:     "run ended right after the boss step; engaged",
		},
		{
			name: "frames saw the boss guid dead",
			before: func(w *walk) {
				g := zgWalk25BossGUID.String()
				w.bossGUIDs[g] = 14517
				w.bossAttemptAt = map[uint32]time.Time{14517: now.Add(-time.Minute)}
				w.hostiles = map[string]hostileState{g: {entry: 14517, hppct: 0, at: now.Add(-time.Second)}}
			},
			killed: true,
			ev:     "run ended right after the boss step; frames",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := bossSummaryWalk(t)
			tc.before(w)
			feedStatus(w, tbm.RunStatus{Step: 44, Kind: "boss", Note: "fighting"}, now)
			feedStatus(w, stop, now)
			k := w.bossKillSummary()
			if k == nil || k.Step != 44 || k.Entry != 14517 || !k.Engaged || k.Killed != tc.killed {
				t.Fatalf("boss_kill %+v, want step 44 entry 14517 engaged, killed %v", k, tc.killed)
			}
			if tc.killed && k.Evidence != tc.ev {
				t.Fatalf("evidence %q, want %q", k.Evidence, tc.ev)
			}
		})
	}

	// The plan end on its LAST boss row: the status line clamps the step there (StatusParts), so the done line
	// carries the boss step itself and its kind is boss.
	lastBoss := func(t *testing.T) *walk {
		w := bossSummaryWalk(t)
		w.rows = planexport.Rows{Steps: []planexport.StepRow{
			{Kind: planexport.KindMove},
			{Kind: planexport.KindBoss, Entry: 14517},
		}}
		return w
	}
	t.Run("plan done on the last boss row", func(t *testing.T) {
		w := lastBoss(t)
		feedStatus(w, tbm.RunStatus{Step: 2, Kind: "boss", Note: "fighting"}, now)
		feedStatus(w, tbm.RunStatus{State: tbm.RunDone, Step: 2, Steps: 2, Kind: "boss", Note: "plan done"}, now)
		k := w.bossKillSummary()
		if k == nil || k.Step != 2 || !k.Killed || k.Evidence != "run ended right after the boss step; engaged" {
			t.Fatalf("boss_kill %+v, want step 2 killed, evidence engaged", k)
		}
	})
	t.Run("the skipped list names the boss", func(t *testing.T) {
		w := lastBoss(t)
		feedStatus(w, tbm.RunStatus{Step: 2, Kind: "boss", Note: "fighting"}, now)
		feedStatus(w, tbm.RunStatus{State: tbm.RunDone, Step: 2, Steps: 2, Kind: "boss",
			Note: "plan done (boss skipped, not seen killed: boss 14517 (step 2))"}, now)
		k := w.bossKillSummary()
		if k == nil || k.Step != 2 || k.Killed {
			t.Fatalf("boss_kill %+v, want step 2 NOT killed: the end note named the boss as skipped", k)
		}
	})
}

// TestBossLeaveCreditsTheNearestBossRow is M2: the leaving status credits the nearest boss ROW at or below its step,
// created when the run's statuses never stood on it. A boss row walked past earlier (A, step 44, entry 14517) must
// not take boss B's kill (B, step 100, entry 14507, no status of its own).
func TestBossLeaveCreditsTheNearestBossRow(t *testing.T) {
	w := bossSummaryWalk(t)
	now := time.Now()
	feedStatus(w, tbm.RunStatus{Step: 44, Kind: "boss", Note: "fighting"}, now)
	feedStatus(w, tbm.RunStatus{Step: 45, Kind: "move", Note: "walking"}, now)
	feedStatus(w, tbm.RunStatus{Step: 101, Kind: "move", Note: "boss down: he died as 14507"}, now)
	list := w.bossKillSummaries()
	if len(list) != 2 {
		t.Fatalf("bossKills %d records, want 2: %+v", len(list), list)
	}
	if k := list[0]; k.Step != 44 || k.Entry != 14517 || k.Killed {
		t.Fatalf("A %+v, want step 44 entry 14517 NOT killed: the `boss down` at 91 is B's", k)
	}
	if k := list[1]; k.Step != 100 || k.Entry != 14507 || !k.Killed || k.Evidence != "boss down: he died as 14507" {
		t.Fatalf("B %+v, want step 100 entry 14507 killed by the `boss down` at 91", k)
	}
}

// TestBossRecordSkippedOnUnverifiedRows is M3: without --sql the rows come from the plan JSON, which need not be
// the rows the server walks. A boss-kind status whose step is no boss row there records nothing and says why; with
// --sql (the deployed rows) the same status is believed.
func TestBossRecordSkippedOnUnverifiedRows(t *testing.T) {
	st := tbm.RunStatus{Step: 1, Kind: "boss", Note: "fighting"}
	w := bossSummaryWalk(t)
	if _, ok := w.bossRowAt(st.Step); ok {
		t.Fatalf("step %d is a boss row: pick a move step", st.Step)
	}
	w.observeBossStatus(st, time.Now())
	if got := w.bossKillSummaries(); len(got) != 0 {
		t.Fatalf("bossKills %+v, want none: the plan-JSON row is no boss row", got)
	}
	if !strings.Contains(strings.Join(w.notes, "\n"), "boss record skipped: plan rows are not the deployed rows") {
		t.Fatalf("notes %q, want the skipped-record note", w.notes)
	}
	w2 := bossSummaryWalk(t)
	feedStatus(w2, tbm.RunStatus{Step: 44, Kind: "boss", Note: "fighting"}, time.Now())
	if k := w2.bossKillSummary(); k == nil || k.Step != 44 || !k.Engaged {
		t.Fatalf("boss_kill %+v, want step 44 engaged: a boss row that matches is recorded", k)
	}
}

// TestBossStatusOnInjectorPageMarksKill is M1: the injector reads events pages itself (readEventsPage), and a
// `boss down` line consumed there must mark the kill as pollOnce's lines do. Without the observe call the page's
// kill was lost.
func TestBossStatusOnInjectorPageMarksKill(t *testing.T) {
	w := bossSummaryWalk(t)
	now := time.Now()
	w.env = walkEnv{now: func() time.Time { return now }}
	w.plan = &dungeon.Plan{Name: "zul_gurub"}
	page := events.Page{Events: []events.Event{
		{Kind: events.KindSystem, Text: "TBM:RUN|zul_gurub|running|44/1095|high_priest_thekal|boss|begin|fighting"},
		{Kind: events.KindSystem, Text: "TBM:RUN|zul_gurub|running|45/1095|high_priest_thekal|move|walk|boss down: he died as 14517"},
	}}
	(&raidInjector{w: w}).readEventsPage(page)
	k := w.bossKillSummary()
	if k == nil || k.Step != 44 || !k.Killed || k.Evidence != "boss down: he died as 14517" {
		t.Fatalf("boss_kill %+v, want step 44 killed by the injector page's note", k)
	}
}

// TestBossKillTrackerFallbackInFinish is L4: the tracker's one-shot latch is inserted as a boss record in finish,
// before the summary reads the tracks (so boss_kill and boss_kills both carry it), and only for a boss row the rows
// know (bossStepOf). The run's own statuses never named this step here: the frames' latch alone did.
func TestBossKillTrackerFallbackInFinish(t *testing.T) {
	p, err := dungeon.Load("../../dungeons/zul_gurub.json")
	if err != nil {
		t.Fatal(err)
	}
	w := bossSummaryWalk(t)
	w.plan = p
	w.outDir = t.TempDir()
	w.stdout = io.Discard
	w.started = time.Now()
	w.ended = w.started
	w.tracker = raidwalk.NewTracker(p, nil)
	w.tracker.Frame(w.started, tbm.ViewFrame{
		Party:    []tbm.PartyRec{{GUID: 1000, Name: "Tank"}},
		Hostiles: []tbm.HostileRec{{GUID: zgWalk25BossGUID, Entry: 14517, HPPct: 90, Target: 1000}},
	})
	w.finish(exitOK)
	raw, err := os.ReadFile(filepath.Join(w.outDir, "summary.json"))
	if err != nil {
		t.Fatal(err)
	}
	var sum raidwalk.Summary
	if err := json.Unmarshal(raw, &sum); err != nil {
		t.Fatal(err)
	}
	k := sum.BossKill
	if k == nil || k.Step != 44 || k.Entry != 14517 || !k.Engaged {
		t.Fatalf("boss_kill %+v, want the tracker's fallback record at step 44 entry 14517", k)
	}
	if len(sum.BossKills) != 1 || sum.BossKills[0].Step != 44 {
		t.Fatalf("boss_kills %+v, want the same record in the list", sum.BossKills)
	}
}
