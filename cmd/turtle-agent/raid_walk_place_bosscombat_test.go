package main

// Walk 97 fight 9-1: the agent's RPC died mid-Hakkar (exit 18) and the --reuse-instance restart resumed the bound
// instance while Hakkar and his arena adds were still engaged. The quiet dismissed them (beyond the 25 yd landing
// reach, BEHIND), and the summon dropped the raid into the live fight. These tests pin the pre-placement boss-combat
// guard: a resumed or reused instance whose boss arena still holds a fight gets no placement, the walk waits for the
// reset, and a fight that never resets asks for a fresh instance (exit 29); a clear arena places as before.

import (
	"context"
	"fmt"
	"math"
	"strconv"
	"strings"
	"testing"

	"github.com/pjerra/turtle-agent/internal/planexport"
	"github.com/pjerra/turtle-agent/internal/raidwalk"
)

// reuseBossGuardFixture is the walk-97 restart's shape: --reuse-instance resuming the commander's bind at the Hakkar
// leg's boss-approach row (walk 80's point, inside the boss arena read's reach), with the plan rows and the boss row
// returned. The caller answers the arena read through p.units.
func reuseBossGuardFixture(t *testing.T) (f *fixture, p *placeToolkit, boss planexport.StepRow) {
	t.Helper()
	f, p = newPlaceFixture(t)
	f.opts.reuseInstance = true
	f.agent.entered = false
	f.agent.instanceBinds = []fakeBind{{inst: 107, ttr: "1h 0m"}}
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

// A resumed instance whose boss is still in combat gets no placement: the arena is read, the walk waits out the reset
// window, and with no reset it asks for a fresh instance (exit 29, the autopilot's own fresh-instance retry). No
// `.summon` goes out: the raid never lands in the live fight (walk 97 fight 9-1).
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
