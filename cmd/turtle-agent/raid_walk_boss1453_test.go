package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/pjerra/turtle-agent/internal/dungeon"
	"github.com/pjerra/turtle-agent/internal/packet"
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
// at 6 %, and after the boss step the run reports the kill with "boss down: he died as 14509".
//
// The walk records the summary's boss_kill from the run leader's own note (the last note on the boss step or the
// first status after it), so no pause and no `.bot run skip` are needed, and the phase comes from the entries the
// frames saw on the boss's guid: 1 + the one change = 2, the last entry 14509. The steps are the rows the walk
// walks (the plan JSON: Thekal's boss row is step 127 of its rows; the live walk's deployed rows had 274 of 1095,
// the same shape).
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
		{raw: fmt.Sprintf("TBM:RUN|zul_gurub|running|%d/%d|high_priest_thekal|move|walk|boss down: he died as 14509|until step %d (step %d)", boss+1, total, boss+1, boss+1)},
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
		k.Evidence != "boss down: he died as 14509" {
		t.Fatalf("boss_kill %+v, want {14599 %d true 2 14509 boss down: he died as 14509}", k, boss)
	}
	if line := sum.Text(); !strings.Contains(line, fmt.Sprintf("boss kill: 14599 at step %d, killed, phase 2 (14509): boss down: he died as 14509", boss)) {
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
