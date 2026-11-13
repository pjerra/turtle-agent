package main

// Walk 97 fight 9-1: the agent's RPC died mid-Hakkar (exit 18) and the --reuse-instance restart resumed the bound
// instance while Hakkar and his arena adds were still engaged. The quiet dismissed them (beyond the 25 yd landing
// reach, BEHIND), and the summon dropped the raid into the live fight. These tests pin the pre-placement boss-combat
// guard: a resumed or reused instance whose boss arena still holds a fight gets no placement — before the GM clear the
// walk waits for the reset and a fight that never resets asks for a fresh instance (exit 29), while a fight the final
// gate sees after the clear and the quiet asks for one at once, with no wait; a clear arena places as before.

import (
	"context"
	"fmt"
	"math"
	"strconv"
	"strings"
	"testing"

	"github.com/pjerra/turtle-agent/internal/dungeon"
	"github.com/pjerra/turtle-agent/internal/planexport"
	"github.com/pjerra/turtle-agent/internal/raidwalk"
	"github.com/pjerra/turtle-agent/internal/tbm"
)

// reuseBossGuardFixture is the walk-97 restart's shape: --reuse-instance resuming the commander's bind at the Hakkar
// leg's boss-approach row (walk 80's point, inside the boss arena read's reach), with the plan rows and the boss row
// returned. The caller answers the arena read through p.units.
func reuseBossGuardFixture(t *testing.T) (f *fixture, p *placeToolkit, boss planexport.StepRow) {
	t.Helper()
	return reuseBossGuardFixtureMode(t, "resume")
}

// reuseBossGuardFixtureMode is reuseBossGuardFixture for one of --reuse-instance's two paths: "resume" (the previous
// raid logged out, the commander's bind lives) and "reuse" (the raid is still alive in the copy).
func reuseBossGuardFixtureMode(t *testing.T, mode string) (f *fixture, p *placeToolkit, boss planexport.StepRow) {
	t.Helper()
	f, p = newPlaceFixture(t)
	f.opts.reuseInstance = true
	switch mode {
	case "reuse":
		f.agent.entered = true
	case "resume":
		f.agent.entered = false
		f.agent.instanceBinds = []fakeBind{{inst: 107, ttr: "1h 0m"}}
	default:
		t.Fatalf("mode %q", mode)
	}
	p.inst = 107
	rows, _ := placePlanRowsFrom(t, "hakkar")
	at := 0
	for i, r := range rows.Steps {
		if math.Abs(float64(r.X)+11807.651) < 0.05 && math.Abs(float64(r.Y)+1691.3049) < 0.05 {
			at = i + 1
			break
		}
	}
	if at == 0 {
		t.Fatal("no row at the Hakkar boss-approach point (-11807.651 -1691.3049): the walk 97 start is gone from the plan")
	}
	f.opts.from = fmt.Sprintf("step %d", at)
	start, leg, _, _, err := raidwalk.PlaceStart(rows, []string{"from", "step", strconv.Itoa(at)})
	if err != nil {
		t.Fatal(err)
	}
	point, ok := raidwalk.PlacePoint(rows, start)
	if !ok {
		t.Fatal("the start step has no point")
	}
	for _, r := range rows.Steps {
		if r.Kind == planexport.KindBoss && r.Leg == rows.Steps[at-1].Leg {
			boss = r
		}
	}
	if boss.Entry == 0 {
		t.Fatal("the Hakkar leg has no boss row")
	}
	// The premise of every test here: the boss arena stands inside the guard's read reach of this placement, or the
	// read would be skipped and the guard could not catch the fight.
	if d := placeDist2(boss.X, boss.Y, point.X, point.Y); d > placeBossReachYd {
		t.Fatalf("the boss row %d stands %.1f yd from the start point (%v %v %v), outside the %d yd read reach",
			boss.Entry, d, point.X, point.Y, point.Z, placeBossReachYd)
	}
	until, err := raidwalk.PlaceStop(rows, []string{"until", "boss"}, start)
	if err != nil {
		t.Fatal(err)
	}
	f.agent.startReply = placeStartReplyLeg("zul_gurub", leg, start, len(rows.Steps), until)
	// The run's own status lines: the placed start walks two steps and stops at its until, on the Hakkar leg.
	f.agent.startQueue = []queuedEvent{
		{raw: fmt.Sprintf("TBM:RUN|zul_gurub|running|%d/%d|%s|move|walk|walking", start, len(rows.Steps), leg)},
		{raw: fmt.Sprintf("TBM:RUN|zul_gurub|running|%d/%d|%s|move|walk|walking", start+1, len(rows.Steps), leg)},
		{raw: fmt.Sprintf("TBM:RUN|zul_gurub|done|%d/%d|%s|move|walk|stopped before step %d (until boss): the party stays where it stands", start+1, len(rows.Steps), leg, until)},
	}
	return f, p, boss
}

// bossArenaAnswer answers the guard's arena read (`q units 60 <boss anchor> all`) with the boss unit and whether it is
// in combat for that read; every other `q units` read stays empty.
func bossArenaAnswer(boss planexport.StepRow, engaged func(read int) bool) func(string) ([]any, bool) {
	prefix := fmt.Sprintf("q units %d ", placeBossArenaYd)
	reads := 0
	return func(cmd string) ([]any, bool) {
		if !strings.HasPrefix(cmd, prefix) {
			return nil, false
		}
		reads++
		u := map[string]any{"g": "0xF1300039F232B9", "e": boss.Entry, "n": "Hakkar",
			"p": []float32{boss.X, boss.Y, boss.Z}, "d": 2.0, "dz": 0.5}
		if engaged(reads) {
			u["cb"] = true
		}
		return []any{u}, false
	}
}

// bossPointAnswer answers the guard's arena read at one anchor (`q units <r> <x> <y> <z> all`) with one unit, in
// combat for the reads engaged says so; every other arena read stays empty. Unlike bossArenaAnswer it matches the
// anchor's own point, so the other bosses whose arenas stand in reach of the landing stay clear.
func bossPointAnswer(x, y, z float32, entry uint32, name string, engaged func(read int) bool) func(string) ([]any, bool) {
	prefix := fmt.Sprintf("q units %d %s %s %s ", placeBossArenaYd, num32(x), num32(y), num32(z))
	reads := 0
	return func(cmd string) ([]any, bool) {
		if !strings.HasPrefix(cmd, prefix) {
			return nil, false
		}
		reads++
		u := map[string]any{"g": "0xF1300039F232B9", "e": entry, "n": name, "p": []float32{x, y, z}, "d": 2.0, "dz": 0.5}
		if engaged(reads) {
			u["cb"] = true
		}
		return []any{u}, false
	}
}

// placeStartReplyUntilStep is the run start's answer for a placed walk under `--until step N`: the run running at step
// of steps with the until label the run leader writes for its stop ("until step N (step N)").
func placeStartReplyUntilStep(plan, leg string, step, steps, untilStep int) *commanderReply {
	label := fmt.Sprintf("until step %d (step %d)", untilStep, untilStep)
	lines := []string{
		fmt.Sprintf("TBM:RUN|%s|running|%d/%d|%s|move|walk|walking|%s", plan, step, steps, leg, label),
		fmt.Sprintf("The run starts at step %d of %d (leg %s)", step, steps, leg),
		"TBM:ACTION_ACK|run start|party|1|-",
	}
	return &commanderReply{Reply: tbm.Reply{Kind: tbm.KindAck, OK: true,
		Ack:  &tbm.Ack{Intent: "run start", Scope: "party", Count: 1, Executor: "-"},
		Line: lines[0], Lines: lines}}
}

// bwlBossHoldFixture is a --place restart at the first boss-hold row of Blackwing Lair whose leg starts inside the
// guard's read reach (Vaelastrasz's hold: step 5 -> step 10, 35 yd apart), for one of --reuse-instance's two paths.
// The exporter writes a boss fought on the spot as a KindHold row commented "boss hold", its entries the boss first
// (Vaelastrasz 13020). The caller answers the arena read through p.units.
func bwlBossHoldFixture(t *testing.T, mode string) (*fixture, *placeToolkit, planexport.StepRow) {
	t.Helper()
	plan, err := dungeon.Load("../../dungeons/blackwing_lair.json")
	if err != nil {
		t.Fatal(err)
	}
	res, err := planexport.Build(plan, planexport.Options{})
	if err != nil {
		t.Fatal(err)
	}
	rows := res.Rows
	holdStep, start := 0, 0
	for i, s := range rows.Steps {
		if s.Kind != planexport.KindHold || !strings.HasPrefix(s.Comment, "boss hold") || len(s.Entries) == 0 {
			continue
		}
		lo := i
		for lo > 0 && rows.Steps[lo-1].Leg == s.Leg {
			lo--
		}
		point, ok := raidwalk.PlacePoint(rows, lo+1)
		if ok && placeDist2(point.X, point.Y, s.X, s.Y) <= placeBossReachYd {
			holdStep, start = i+1, lo+1
			break
		}
	}
	if holdStep == 0 {
		t.Fatal("blackwing_lair has no boss-hold row whose leg starts inside the guard's read reach")
	}
	hold := rows.Steps[holdStep-1]
	leg := rows.Legs[hold.Leg].Name
	f, p := newPlaceFixture(t)
	f.opts.plan = "blackwing_lair"
	f.opts.holder = "raid-walk-blackwing_lair"
	f.opts.tier = 1
	f.opts.from = fmt.Sprintf("step %d", start)
	f.opts.until = fmt.Sprintf("step %d", holdStep)
	f.agent.instMap = 469
	f.opts.reuseInstance = true
	p.planMap = 469
	p.inst = 107
	switch mode {
	case "reuse":
		f.agent.entered = true
	case "resume":
		f.agent.entered = false
		f.agent.instanceBinds = []fakeBind{{inst: 107, ttr: "1h 0m"}}
	default:
		t.Fatalf("mode %q", mode)
	}
	f.agent.startReply = placeStartReplyUntilStep("blackwing_lair", leg, start, len(rows.Steps), holdStep)
	label := fmt.Sprintf("until step %d (step %d)", holdStep, holdStep)
	f.agent.startQueue = []queuedEvent{
		{raw: fmt.Sprintf("TBM:RUN|blackwing_lair|running|%d/%d|%s|move|walk|walking|%s", start, len(rows.Steps), leg, label)},
		{raw: fmt.Sprintf("TBM:RUN|blackwing_lair|running|%d/%d|%s|move|walk|walking|%s", start+1, len(rows.Steps), leg, label)},
		{raw: fmt.Sprintf("TBM:RUN|blackwing_lair|done|%d/%d|%s|move|walk|stopped before step %d (%s): the party stays where it stands", start+1, len(rows.Steps), leg, holdStep, label)},
	}
	return f, p, hold
}

// A resumed or reused instance whose boss-hold encounter is still engaged gets no placement: the hold row is a boss
// fought on the spot (KindHold, comment "boss hold"), not a KindBoss row, and the round-1 guard read only boss rows,
// so Vaelastrasz and his encounter units stayed invisible while the raid was summoned in. The hold's arena is read
// at its spot, the fight never resets, and the walk asks for a fresh instance with no `.summon` (exit 29).
func TestReuseBossHoldGuardBlocksReuseAndResumePlacement(t *testing.T) {
	for _, mode := range []string{"reuse", "resume"} {
		t.Run(mode, func(t *testing.T) {
			f, p, hold := bwlBossHoldFixture(t, mode)
			p.units = bossPointAnswer(hold.X, hold.Y, hold.Z, hold.Entries[0], "Vaelastrasz the Corrupt",
				func(int) bool { return true })
			if code := f.run(context.Background()); code != exitRaidWalkPlaceFailed {
				t.Fatalf("exit %d, want 29: %s\n%s", code, f.stderr.String(), f.stdout.String())
			}
			sum := f.summary()
			if !strings.Contains(sum.EndReason, "fresh instance needed") || !strings.Contains(sum.EndReason, "did not reset in 90s") {
				t.Fatalf("end reason %q, want the no-reset / fresh-instance reason", sum.EndReason)
			}
			if !hasNote(sum, "13020 Vaelastrasz the Corrupt") {
				t.Fatalf("notes %q, want the engaged boss-hold encounter named", sum.Notes)
			}
			read := fmt.Sprintf("tt q units %d %s %s %s ", placeBossArenaYd, num32(hold.X), num32(hold.Y), num32(hold.Z))
			if !hasPrefix(f.agentLog(), read) {
				t.Fatalf("the boss-hold arena was never read:\n%s", strings.Join(f.agentLog(), "\n"))
			}
			if len(p.places) != 0 {
				t.Fatalf("the raid was placed into the live fight: %v", p.places)
			}
			if hasSummon(f.agentLog()) {
				t.Fatalf("a .summon went out while the boss-hold encounter fought:\n%s", strings.Join(f.agentLog(), "\n"))
			}
		})
	}
}

// The guard reads the arena once before the quiet and again as the final gate immediately before the first summon: a
// fight that begins between the two reads (the reused copy keeps live raid members) must stop the summon. The round-1
// guard read once, passed, and summoned after the quiet had taken the arena away from the read's view. The round-2
// final gate waited for the reset instead, re-opening the race (a patrol can walk in during the wait): now an engaged
// final read ends the placement at once, with no wait and no further read, even when the fight never resets.
func TestReuseBossGuardRechecksCombatAfterTheClearAndQuiet(t *testing.T) {
	for _, mode := range []string{"resume", "reuse"} {
		t.Run(mode, func(t *testing.T) {
			f, p, boss := reuseBossGuardFixtureMode(t, mode)
			// The first read (before the clear and the quiet) is clear; every read after it sees the fight.
			p.units = bossPointAnswer(boss.X, boss.Y, boss.Z, boss.Entry, "Hakkar", func(read int) bool { return read > 1 })
			if code := f.run(context.Background()); code != exitRaidWalkPlaceFailed {
				t.Fatalf("exit %d, want 29: %s\n%s\n%s", code, f.stderr.String(), f.stdout.String(), strings.Join(f.agentLog(), "\n"))
			}
			sum := f.summary()
			if !strings.Contains(sum.EndReason, "fresh instance needed") || !strings.Contains(sum.EndReason, "the fight began after the first read") {
				t.Fatalf("end reason %q, want the final gate's immediate fresh-instance reason", sum.EndReason)
			}
			if strings.Contains(sum.EndReason, "did not reset") {
				t.Fatalf("end reason %q: the final gate waited for a reset", sum.EndReason)
			}
			if !hasNote(sum, "the fight began after the first read: the final gate waits for no reset") {
				t.Fatalf("notes %q, want the final gate's re-read", sum.Notes)
			}
			if hasNote(sum, "waiting up to") || hasNote(sum, "the boss arena reset after") {
				t.Fatalf("notes %q: the final gate waited instead of failing at once", sum.Notes)
			}
			if n := countArenaReads(f.agentLog(), boss); n != 2 {
				t.Fatalf("%d boss-arena reads, want 2 (one before the clear, one at the final gate): the gate polled after seeing the fight", n)
			}
			if len(p.places) != 0 {
				t.Fatalf("the raid was placed into the fight that began after the first read: %v", p.places)
			}
			if hasSummon(f.agentLog()) {
				t.Fatalf("a .summon went out after the fight began:\n%s", strings.Join(f.agentLog(), "\n"))
			}
		})
	}
}

// The final gate's read is the last word even when the fight would reset on the next poll: the gate fails straight to
// the fresh instance, so the reset the round-2 code waited for never happens and no summon goes out. This is the race
// the round-2 wait re-opened: the arena clears, and a patrol can walk into the landing before the summon. The incident's
// answer is the fresh-instance restart, not another read after a wait.
func TestReuseBossGuardFinalGateDoesNotWaitForTheReset(t *testing.T) {
	for _, mode := range []string{"resume", "reuse"} {
		t.Run(mode, func(t *testing.T) {
			f, p, boss := reuseBossGuardFixtureMode(t, mode)
			// The final gate (read 2) sees the fight; read 3 would find it reset, as a 5 s wait would have.
			p.units = bossPointAnswer(boss.X, boss.Y, boss.Z, boss.Entry, "Hakkar", func(read int) bool { return read == 2 })
			if code := f.run(context.Background()); code != exitRaidWalkPlaceFailed {
				t.Fatalf("exit %d, want 29: %s\n%s\n%s", code, f.stderr.String(), f.stdout.String(), strings.Join(f.agentLog(), "\n"))
			}
			sum := f.summary()
			if !strings.Contains(sum.EndReason, "fresh instance needed") || !strings.Contains(sum.EndReason, "the fight began after the first read") {
				t.Fatalf("end reason %q, want the final gate's immediate fresh-instance reason", sum.EndReason)
			}
			if n := countArenaReads(f.agentLog(), boss); n != 2 {
				t.Fatalf("%d boss-arena reads, want 2: the final gate waited for another read", n)
			}
			if len(p.places) != 0 {
				t.Fatalf("the raid was placed although the final gate saw the fight: %v", p.places)
			}
			if hasSummon(f.agentLog()) {
				t.Fatalf("a .summon went out although the final gate saw the fight:\n%s", strings.Join(f.agentLog(), "\n"))
			}
		})
	}
}

// hasPrefix says any of log's lines starts with prefix.
func hasPrefix(log []string, prefix string) bool {
	for _, l := range log {
		if strings.HasPrefix(l, prefix) {
			return true
		}
	}
	return false
}

// countArenaReads counts the guard's arena reads at one boss anchor (`tt q units 60 <x> <y> <z> ...`) in the agent log:
// one read per guard pass, and none while a pass waits for the reset.
func countArenaReads(log []string, boss planexport.StepRow) int {
	prefix := fmt.Sprintf("tt q units %d %s %s %s ", placeBossArenaYd, num32(boss.X), num32(boss.Y), num32(boss.Z))
	n := 0
	for _, l := range log {
		if strings.HasPrefix(l, prefix) {
			n++
		}
	}
	return n
}

func TestReuseBossArenaGuardAsksForAFreshInstanceWhenTheFightNeverResets(t *testing.T) {
	f, p, boss := reuseBossGuardFixture(t)
	p.units = bossArenaAnswer(boss, func(int) bool { return true })
	if code := f.run(context.Background()); code != exitRaidWalkPlaceFailed {
		t.Fatalf("exit %d, want 29: %s\n%s", code, f.stderr.String(), f.stdout.String())
	}
	sum := f.summary()
	if !strings.Contains(sum.EndReason, "fresh instance needed") || !strings.Contains(sum.EndReason, "did not reset in 90s") {
		t.Fatalf("end reason %q, want the no-reset / fresh-instance reason", sum.EndReason)
	}
	if !hasNote(sum, "no placement into a live boss fight; waiting up to 90s for the reset") {
		t.Fatalf("notes %q, want the guard's wait", sum.Notes)
	}
	if !hasNote(sum, "Hakkar") {
		t.Fatalf("notes %q, want the fight named", sum.Notes)
	}
	if len(p.places) != 0 {
		t.Fatalf("the raid was placed into the live fight: %v", p.places)
	}
	for _, l := range f.agentLog() {
		if strings.HasPrefix(l, "gm .summon ") {
			t.Fatalf("a .summon went out while the boss fought:\n%s", strings.Join(f.agentLog(), "\n"))
		}
	}
}

// The same restart when the fight resets while the guard waits: the placement goes on in the resumed instance, exactly
// as before the guard existed.
func TestReuseBossArenaGuardPlacesAfterTheFightResets(t *testing.T) {
	f, p, boss := reuseBossGuardFixture(t)
	// The first read sees the fight; the first poll after the 5 s wait finds it reset.
	p.units = bossArenaAnswer(boss, func(read int) bool { return read == 1 })
	if code := f.run(context.Background()); code != exitOK {
		t.Fatalf("exit %d: %s\n%s", code, f.stderr.String(), f.stdout.String())
	}
	sum := f.summary()
	if !hasNote(sum, "the boss arena reset after 5s (2 reads): placing into the instance") {
		t.Fatalf("notes %q, want the reset note", sum.Notes)
	}
	if !hasSummon(f.agentLog()) {
		t.Fatalf("the raid was not summoned after the reset:\n%s", strings.Join(f.agentLog(), "\n"))
	}
	if sum.Place == nil || sum.Place.Instance != 107 {
		t.Fatalf("place summary %+v, want the resumed instance 107", sum.Place)
	}
}

// A clear arena places as before: the guard reads the boss arena once and lets the placement go on.
func TestReuseBossArenaGuardPlacesWhenTheArenaIsClear(t *testing.T) {
	f, p, boss := reuseBossGuardFixture(t)
	p.units = bossArenaAnswer(boss, func(int) bool { return false })
	if code := f.run(context.Background()); code != exitOK {
		t.Fatalf("exit %d: %s\n%s", code, f.stderr.String(), f.stdout.String())
	}
	sum := f.summary()
	if hasNote(sum, "waiting up to") {
		t.Fatalf("notes %q: the guard waited on a clear arena", sum.Notes)
	}
	arena := 0
	for _, l := range f.agentLog() {
		if strings.HasPrefix(l, "tt q units "+strconv.Itoa(placeBossArenaYd)+" ") {
			arena++
		}
	}
	if arena == 0 {
		t.Fatalf("the boss arena was never read:\n%s", strings.Join(f.agentLog(), "\n"))
	}
	if !hasSummon(f.agentLog()) {
		t.Fatalf("the raid was not summoned after a clear arena:\n%s", strings.Join(f.agentLog(), "\n"))
	}
	if sum.Place == nil || sum.Place.Instance != 107 {
		t.Fatalf("place summary %+v, want the resumed instance 107", sum.Place)
	}
}

// hasSummon says any `.summon` line went out.
func hasSummon(log []string) bool {
	for _, l := range log {
		if strings.HasPrefix(l, "gm .summon ") {
			return true
		}
	}
	return false
}
