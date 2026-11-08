package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/pjerra/turtle-agent/internal/dungeon"
	"github.com/pjerra/turtle-agent/internal/raidwalk"
)

// still is a hire that stood at (x, y) for secs seconds up to now, one sample a second.
func still(now time.Time, x, y float32, secs int) []posSample {
	var out []posSample
	for s := secs; s >= 0; s-- {
		out = append(out, posSample{at: now.Add(-time.Duration(s) * time.Second), x: x, y: y})
	}
	return out
}

// walking is a hire that moved 3 yd every second for secs seconds up to now.
func walking(now time.Time, secs int) []posSample {
	var out []posSample
	for s := secs; s >= 0; s-- {
		out = append(out, posSample{at: now.Add(-time.Duration(s) * time.Second), x: float32(100 - 3*s), y: 50})
	}
	return out
}

func TestFrozenBots(t *testing.T) {
	now := time.Date(2026, 11, 8, 12, 0, 0, 0, time.UTC)
	place := [3]float32{-11563, -1316, 79}
	const spread = 5                           // the ring is spread + 6 = 11 yd
	mtAway := [3]float32{-11513, -1316, 79}    // 50 yd from the place point
	mtHome := [3]float32{-11559, -1316, 79}    // 4 yd from it
	mtJustOut := [3]float32{-11547, -1316, 79} // 16 yd from it
	mt20 := [3]float32{-11543, -1316, 79}      // 20 yd from it
	mtFar40 := [3]float32{-11523, -1316, 79}   // 40 yd from it
	atPlace := func(secs int) []posSample { return still(now, -11563, -1316, secs) }
	stillAt := func(dx float32, secs int) []posSample { return still(now, -11563+dx, -1316, secs) }
	bot := func(name string, s []posSample) unfreezeBot {
		return unfreezeBot{name: name, alive: true, samples: s}
	}
	cases := []struct {
		name string
		bots []unfreezeBot
		mt   [3]float32
		want []string
	}{
		{"within 1 yd of the place point for 10 s, MT 50 yd away: frozen (the failing case)",
			[]unfreezeBot{bot("priest", atPlace(10))}, mtAway, []string{"priest"}},
		{"on the island 8 yd from the place point: frozen",
			[]unfreezeBot{bot("priest", stillAt(8, 10))}, mtAway, []string{"priest"}},
		{"stationary healer 25 yd from the MT and 40 yd from the place point, raid in combat: NOT frozen",
			[]unfreezeBot{bot("healer", stillAt(40, 30))}, mtJustOut, nil},
		{"12 yd from the place point is outside spread + 6: not frozen",
			[]unfreezeBot{bot("priest", stillAt(12, 10))}, mtAway, nil},
		{"MT still at the place point, bot frozen: NOT frozen",
			[]unfreezeBot{bot("priest", atPlace(10))}, mtHome, nil},
		{"MT 16 yd out, the hire at the point is within 35 yd of it: NOT frozen",
			[]unfreezeBot{bot("priest", atPlace(10))}, mtJustOut, nil},
		{"MT 40 yd out: the raid has left the hire behind",
			[]unfreezeBot{bot("priest", atPlace(10))}, mtFar40, []string{"priest"}},
		{"a caster 8 yd from the point, MT 20 yd out and fighting: NOT frozen (the 35 yd case)",
			[]unfreezeBot{bot("mage", stillAt(8, 30))}, mt20, nil},
		{"a moving bot is not frozen",
			[]unfreezeBot{bot("rogue", walking(now, 12))}, mtAway, nil},
		{"the main tank is never frozen",
			[]unfreezeBot{{name: "tank", mt: true, alive: true, samples: atPlace(20)}}, mtAway, nil},
		{"only 6 s of history: not yet",
			[]unfreezeBot{bot("priest", atPlace(6))}, mtAway, nil},
		{"a dead bot is not frozen",
			[]unfreezeBot{{name: "priest", alive: false, samples: atPlace(10)}}, mtAway, nil},
		{"a last sample 8 s old still counts (a poll's waits)",
			[]unfreezeBot{bot("priest", still(now.Add(-8*time.Second), -11563, -1316, 6))}, mtAway, []string{"priest"}},
		{"a last sample 13 s old still counts (a poll with --gm-instance-data lines)",
			[]unfreezeBot{bot("priest", still(now.Add(-13*time.Second), -11563, -1316, 6))}, mtAway, []string{"priest"}},
		{"a stale last sample (16 s) says nothing",
			[]unfreezeBot{bot("priest", still(now.Add(-16*time.Second), -11563, -1316, 6))}, mtAway, nil},
		{"a bot that stepped 2 yd or more inside the window is not frozen",
			[]unfreezeBot{bot("priest", append(atPlace(8), posSample{at: now, x: -11560.5, y: -1316}))}, mtAway, nil},
		{"re-placed once: frozen again",
			[]unfreezeBot{{name: "priest", alive: true, replaced: 1, samples: atPlace(10)}}, mtAway, []string{"priest"}},
		{"re-placed twice: capped",
			[]unfreezeBot{{name: "priest", alive: true, replaced: unfreezeMaxReplace, samples: atPlace(10)}}, mtAway, nil},
		{"only the frozen one of four",
			[]unfreezeBot{bot("priest", atPlace(11)), bot("rogue", walking(now, 12)), bot("healer", stillAt(40, 11)),
				{name: "tank", mt: true, alive: true, samples: atPlace(11)}},
			mtAway, []string{"priest"}},
	}
	for _, c := range cases {
		got := frozenBots(c.bots, c.mt, place, spread, now)
		if strings.Join(got, ",") != strings.Join(c.want, ",") {
			t.Errorf("%s: frozenBots = %v, want %v", c.name, got, c.want)
		}
	}
}

// unfreezeScene is a walk in the middle of a watch, driven the way phaseWatch drives it: ZG's map, a main tank and three
// hires in the fake agent's frames, every turn one real pollOnce (whose events and view waits take wait on the clock,
// so its frames are wait old when it returns), the tick with the poll's own time, then the loop's 2 s sleep.
type unfreezeScene struct {
	t        *testing.T
	w        *walk
	f        *fixture
	log      string
	wait     time.Duration
	summoned map[string]bool // hires a `.summon` reached (a failed one does not)
}

var unfreezePoint = [2]float32{-11563, -1316}

func newUnfreezeScene(t *testing.T, wait time.Duration) *unfreezeScene {
	t.Helper()
	f := newFixture(t)
	logPath := filepath.Join(t.TempDir(), "run.log")
	lf, err := os.Create(logPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { lf.Close() })
	plan := &dungeon.Plan{Name: "zul_gurub", Map: 309}
	w := &walk{plan: plan, agent: f.agent, env: walkEnv{now: f.clock.now, sleep: f.clock.sleep},
		hires: map[string]bool{"Tank": true, "Priest": true, "Rogue": true, "Mage": true}, mt: "Tank", ots: []string{"Priest"}, rest: []string{"Rogue", "Mage"},
		botAt: map[string]botPos{}, hostiles: map[string]hostileState{}, bossRows: map[uint32]hostileState{}, partyG: map[uint64]bool{}, logF: lf,
		place: &raidwalk.PlacePlan{Point: dungeon.Point{X: unfreezePoint[0], Y: unfreezePoint[1], Z: 79}}}
	w.tracker = raidwalk.NewTracker(plan, w.hireList())
	sc := &unfreezeScene{t: t, w: w, f: f, log: logPath, wait: wait, summoned: map[string]bool{}}
	f.agent.mu.Lock()
	f.agent.party = []string{"Tank", "Priest", "Rogue", "Mage"}
	f.agent.entered, f.agent.instMap = true, 309
	f.agent.pos = map[string][3]float32{}
	f.agent.combatBots = map[string]bool{}
	f.agent.mu.Unlock()
	f.agent.viewHook = func() { _ = f.clock.sleep(context.Background(), sc.wait) }
	f.agent.onGM = func(line string) {
		if name, ok := strings.CutPrefix(line, ".summon "); ok {
			sc.summoned[name] = true
		}
	}
	return sc
}

// put sets where a hire stands in the next frames (and whether it fights or is dead).
func (sc *unfreezeScene) put(name string, x, y float32, combat bool) {
	a := sc.f.agent
	a.mu.Lock()
	a.pos[name] = [3]float32{x, y, 79}
	a.combatBots[name] = combat
	a.mu.Unlock()
}

func (sc *unfreezeScene) dead(name string, dead bool) {
	a := sc.f.agent
	a.mu.Lock()
	a.dead[name] = dead
	a.mu.Unlock()
}

// poll is one phaseWatch turn: pollOnce, the tick (which reads the poll's own time itself), the 2 s sleep.
func (sc *unfreezeScene) poll() {
	sc.t.Helper()
	ctx := context.Background()
	if _, err := sc.w.pollOnce(ctx, false); err != nil {
		sc.t.Fatalf("pollOnce: %v", err)
	}
	sc.w.placeUnfreezeTick(ctx)
	_ = sc.f.clock.sleep(ctx, 2*time.Second)
}

// frozenScene is the walk-55 picture for poll i: the MT walks on 60 yd from the point and fights, Rogue runs with it,
// Mage casts in place 40 yd from the point, Priest stands frozen on the point until a summon reaches it, then follows.
func (sc *unfreezeScene) frozenScene(i int) {
	x := float32(-11503 + 2*i)
	sc.put("Tank", x, -1316, true)
	sc.put("Rogue", x-3, -1316, true)
	sc.put("Mage", -11523, -1310, true)
	if sc.summoned["Priest"] {
		sc.put("Priest", x-2, -1314, true)
	} else {
		sc.put("Priest", unfreezePoint[0], unfreezePoint[1], true)
	}
}

func (sc *unfreezeScene) gmPrefixed(prefix string) []string {
	var out []string
	for _, l := range sc.f.agent.gmLines() {
		if strings.HasPrefix(l, prefix) {
			out = append(out, l)
		}
	}
	return out
}

func (sc *unfreezeScene) runLog() string {
	raw, _ := os.ReadFile(sc.log)
	return string(raw)
}

func (sc *unfreezeScene) notes() string { return strings.Join(sc.w.notesCopy(), "\n") }

func (sc *unfreezeScene) arm() { sc.w.placeUnfreezeStart(sc.w.place.Point, placeSpread) }

// The F1 failing case: polls whose waits take 6 s, so every frame is 6 s old when the tick runs. Judged against the
// poll's own time the frozen Priest is re-placed (one `.go xyz` onto the MT at z+2, one `.summon`), and nothing goes out
// before 15 s after the first watch sample; the caster standing 40 yd from the point and the runner are never touched.
func TestPlaceUnfreezeReplacesOnlyTheFrozenBot(t *testing.T) {
	sc := newUnfreezeScene(t, 6*time.Second)
	sc.arm()
	var first time.Time
	for i := 0; i < 12; i++ {
		sc.frozenScene(i)
		at := sc.f.clock.now()
		if i == 0 {
			first = at
		}
		before := len(sc.f.agent.gmLines())
		sc.poll()
		if at.Sub(first) < unfreezeFirstCheck && len(sc.f.agent.gmLines()) != before {
			t.Fatalf("poll %d (%v after the first sample): gm lines %v", i, at.Sub(first), sc.f.agent.gmLines())
		}
	}
	summons := sc.gmPrefixed(".summon ")
	if len(summons) != 1 || summons[0] != ".summon Priest" {
		t.Fatalf("summons %v, want only .summon Priest", summons)
	}
	gos := sc.gmPrefixed(".go xyz ")
	if len(gos) != 1 || !strings.HasPrefix(gos[0], ".go xyz -11") || !strings.HasSuffix(gos[0], " -1316 81 309") {
		t.Fatalf("commander moves %v, want one onto the main tank at its z + 2", gos)
	}
	sc.w.placeUnfreezeFinish()
	for _, want := range []string{"placement: re-placed Priest (frozen at -11563.0,-1316.0,79.0 for ", "placement: frozen=1 re-placed=1"} {
		if !strings.Contains(sc.runLog(), want) {
			t.Errorf("run.log lacks %q:\n%s", want, sc.runLog())
		}
	}
}

// A poll that also sends --gm-instance-data lines (two 3 s gaps) lasts about 13 s: its frames are 13 s old when the
// tick runs, beyond the freshness limit against the clock, fresh against the poll's own time.
func TestPlaceUnfreezeLongPollFramesStayFresh(t *testing.T) {
	sc := newUnfreezeScene(t, 13*time.Second)
	sc.arm()
	for i := 0; i < 6; i++ {
		sc.frozenScene(i)
		sc.poll()
	}
	if s := sc.gmPrefixed(".summon "); len(s) != 1 || s[0] != ".summon Priest" {
		t.Fatalf("summons %v, want .summon Priest", s)
	}
}

// F2: a caster holding 8 yd from the point while the MT fights a pack 20 yd out stands still in the ring with the MT
// more than 15 yd from the point, but within 35 yd of the MT: it is doing its job and is never summoned.
func TestPlaceUnfreezeLeavesACasterNearThePointWhileTheMTFights(t *testing.T) {
	sc := newUnfreezeScene(t, 6*time.Second)
	sc.arm()
	for i := 0; i < 18; i++ {
		sc.put("Tank", unfreezePoint[0]+20, unfreezePoint[1], true)
		sc.put("Rogue", unfreezePoint[0]+21, unfreezePoint[1]+1, true)
		sc.put("Priest", unfreezePoint[0]+14, unfreezePoint[1]+12, true)
		sc.put("Mage", unfreezePoint[0]+8, unfreezePoint[1], true)
		sc.poll()
	}
	if got := sc.f.agent.gmLines(); len(got) != 0 {
		t.Fatalf("gm lines %v: the caster beside the fight was re-placed", got)
	}
}

// The cap: a hire the summons never help is re-placed twice and then left alone, and the check ends 120 s after its
// first watch sample with the summary.
func TestPlaceUnfreezeCapsAtTwoAndStopsAfterTheWindow(t *testing.T) {
	sc := newUnfreezeScene(t, 6*time.Second)
	sc.arm()
	for i := 0; i < 20; i++ {
		sc.put("Tank", -11500, -1250, true)
		sc.put("Priest", unfreezePoint[0], unfreezePoint[1], true) // the summons never help
		sc.poll()
	}
	if n := len(sc.gmPrefixed(".summon Priest")); n != unfreezeMaxReplace {
		t.Fatalf("%d summons of the frozen bot, want %d", n, unfreezeMaxReplace)
	}
	if sc.w.unfreeze.active {
		t.Fatalf("the check is still on 160 s after the first sample")
	}
	if !strings.Contains(sc.notes(), "placement: frozen=1 re-placed=2") {
		t.Fatalf("notes lack the summary: %s", sc.notes())
	}
}

// F4: the clock starts at the first watch sample, not at the place: a placement whose verify, release and run start
// took 115 s still gets its full window.
func TestPlaceUnfreezeWindowStartsAtTheFirstWatchSample(t *testing.T) {
	sc := newUnfreezeScene(t, 6*time.Second)
	sc.arm()
	_ = sc.f.clock.sleep(context.Background(), 115*time.Second)
	for i := 0; i < 8; i++ {
		sc.frozenScene(i)
		sc.poll()
	}
	if s := sc.gmPrefixed(".summon "); len(s) != 1 || s[0] != ".summon Priest" {
		t.Fatalf("summons %v, want .summon Priest", s)
	}
}

// --no-place-unfreeze: the same picture that re-places Priest with the check on sends nothing with it off.
func TestPlaceUnfreezeOffSendsNothing(t *testing.T) {
	for _, off := range []bool{false, true} {
		sc := newUnfreezeScene(t, 6*time.Second)
		sc.w.opts.noPlaceUnfreeze = off
		sc.arm()
		for i := 0; i < 8; i++ {
			sc.frozenScene(i)
			sc.poll()
		}
		got := sc.f.agent.gmLines()
		if off && (len(got) != 0 || strings.Contains(sc.notes(), "unfreeze check armed")) {
			t.Fatalf("check off: gm lines %v, notes %s", got, sc.notes())
		}
		if !off && len(sc.gmPrefixed(".summon Priest")) != 1 {
			t.Fatalf("check on: gm lines %v, want the frozen Priest summoned (the off case proves nothing otherwise)", got)
		}
	}
}

// F6 and Codex high: a `.summon` that fails once is logged as a failure, spends nothing, and the hire is tried again
// on a later poll at least 5 s on (polls 3 s apart here), where the summon takes.
func TestPlaceUnfreezeSummonFailsOnceThenSucceeds(t *testing.T) {
	sc := newUnfreezeScene(t, time.Second)
	fails := 0
	sc.f.agent.gmFail = func(line string) error {
		if line == ".summon Priest" && fails == 0 {
			fails++
			return errors.New("player not found")
		}
		return nil
	}
	sc.arm()
	for i := 0; i < 16; i++ {
		sc.frozenScene(i)
		sc.poll()
	}
	var at []time.Time
	a := sc.f.agent
	a.mu.Lock()
	for i, l := range a.gm {
		if l == ".summon Priest" {
			at = append(at, a.gmAt[i])
		}
	}
	a.mu.Unlock()
	if len(at) != 2 {
		t.Fatalf("%d summons of Priest, want the failed one and the one that took", len(at))
	}
	if gap := at[1].Sub(at[0]); gap < unfreezeFailGap {
		t.Fatalf("the retry went out %v after the failure, want at least %v", gap, unfreezeFailGap)
	}
	sc.w.placeUnfreezeFinish()
	log := sc.runLog()
	for _, want := range []string{"placement: re-place failed Priest (", "placement: re-placed Priest (", "placement: frozen=1 re-placed=1"} {
		if !strings.Contains(log, want) {
			t.Errorf("run.log lacks %q:\n%s", want, log)
		}
	}
	if strings.Index(log, "re-place failed Priest") > strings.Index(log, "re-placed Priest") {
		t.Errorf("the success line comes before the failure:\n%s", log)
	}
	if sc.w.unfreeze.replaced["Priest"] != 1 {
		t.Errorf("replaced %d, want 1 (the failure spends nothing)", sc.w.unfreeze.replaced["Priest"])
	}
}

// Repeated `.summon` failures stop at three per hire; none counts as a re-place.
func TestPlaceUnfreezeSummonFailuresStopAtTheCap(t *testing.T) {
	sc := newUnfreezeScene(t, time.Second)
	sc.f.agent.gmFail = func(line string) error {
		if line == ".summon Priest" {
			return errors.New("player not found")
		}
		return nil
	}
	sc.arm()
	for i := 0; i < 40; i++ {
		sc.frozenScene(i)
		sc.poll()
	}
	if n := len(sc.gmPrefixed(".summon Priest")); n != unfreezeMaxFails {
		t.Fatalf("%d summons of Priest, want %d", n, unfreezeMaxFails)
	}
	if n := strings.Count(sc.runLog(), "placement: re-place failed Priest ("); n != unfreezeMaxFails {
		t.Fatalf("%d failure lines, want %d:\n%s", n, unfreezeMaxFails, sc.runLog())
	}
	sc.w.placeUnfreezeFinish()
	if !strings.Contains(sc.notes(), "placement: frozen=1 re-placed=0") || strings.Contains(sc.runLog(), "re-placed Priest") {
		t.Fatalf("notes %s / run.log %s: a failed summon counted as a re-place", sc.notes(), sc.runLog())
	}
}

// F7: a commander `.go xyz` onto the MT that keeps failing stops the check after three tries with a note.
func TestPlaceUnfreezeGoFailuresStopTheCheck(t *testing.T) {
	sc := newUnfreezeScene(t, time.Second)
	sc.f.agent.gmFail = func(line string) error {
		if strings.HasPrefix(line, ".go xyz ") {
			return errors.New("teleport refused")
		}
		return nil
	}
	sc.arm()
	for i := 0; i < 30; i++ {
		sc.frozenScene(i)
		sc.poll()
	}
	if n := len(sc.gmPrefixed(".go xyz ")); n != unfreezeMaxGoFails {
		t.Fatalf("%d commander moves, want %d", n, unfreezeMaxGoFails)
	}
	if s := sc.gmPrefixed(".summon "); len(s) != 0 {
		t.Fatalf("summons %v without the commander beside the MT", s)
	}
	if sc.w.unfreeze.active || !strings.Contains(sc.notes(), "placement: unfreeze check stopped (the commander's .go onto Tank failed 3 times)") {
		t.Fatalf("active %v, notes %s", sc.w.unfreeze.active, sc.notes())
	}
}

// F9: the main tank dies inside the window: the check stops for this placement, nothing is sent.
func TestPlaceUnfreezeStopsWhenTheMTDies(t *testing.T) {
	sc := newUnfreezeScene(t, 6*time.Second)
	sc.arm()
	for i := 0; i < 10; i++ {
		sc.frozenScene(i)
		sc.dead("Tank", i == 1)
		sc.poll()
	}
	if got := sc.f.agent.gmLines(); len(got) != 0 {
		t.Fatalf("gm lines %v after the main tank died", got)
	}
	if sc.w.unfreeze.active || !strings.Contains(sc.notes(), "placement: unfreeze check stopped (the main tank died)") {
		t.Fatalf("active %v, notes %s", sc.w.unfreeze.active, sc.notes())
	}
}

// F9: a wipe stops the check, both from the frames (every hire dead) and from the walk's own wipe handler.
func TestPlaceUnfreezeStopsOnAWipe(t *testing.T) {
	sc := newUnfreezeScene(t, 6*time.Second)
	sc.arm()
	for i := 0; i < 10; i++ {
		sc.frozenScene(i)
		for _, n := range []string{"Tank", "Priest", "Rogue", "Mage"} {
			sc.dead(n, i == 1)
		}
		sc.poll()
	}
	if got := sc.f.agent.gmLines(); len(got) != 0 || sc.w.unfreeze.active ||
		!strings.Contains(sc.notes(), "placement: unfreeze check stopped (the raid wiped)") {
		t.Fatalf("gm %v, active %v, notes %s", got, sc.w.unfreeze.active, sc.notes())
	}

	sc = newUnfreezeScene(t, 6*time.Second)
	sc.arm()
	sc.frozenScene(0)
	sc.poll()
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // the recovery's reset wait ends at once: only the stop at its start is under test
	_ = sc.w.handleWipe(ctx, 516)
	if sc.w.unfreeze.active || !strings.Contains(sc.notes(), "placement: unfreeze check stopped (the raid wiped)") {
		t.Fatalf("handleWipe left the check on: notes %s", sc.notes())
	}
}

// A hire that dies on the point and revives there inside the window needs a fresh 10 s still window after the revive:
// its corpse's rest is no history.
func TestPlaceUnfreezeDieAndReviveInsideTheWindow(t *testing.T) {
	sc := newUnfreezeScene(t, 6*time.Second)
	sc.arm()
	var revived time.Time
	for i := 0; i < 12; i++ {
		sc.frozenScene(i)
		sc.dead("Priest", i >= 1 && i <= 3)
		if i == 4 {
			revived = sc.f.clock.now()
		}
		sc.poll()
	}
	var at []time.Time
	a := sc.f.agent
	a.mu.Lock()
	for i, l := range a.gm {
		if l == ".summon Priest" {
			at = append(at, a.gmAt[i])
		}
	}
	a.mu.Unlock()
	if len(at) != 1 {
		t.Fatalf("%d summons of Priest, want one after the revive", len(at))
	}
	if at[0].Sub(revived) < unfreezeStillFor {
		t.Fatalf("summoned %v after the revive, want at least %v of fresh history", at[0].Sub(revived), unfreezeStillFor)
	}
}

// Two hires frozen in one tick: one commander move, then one summon each, in that order.
func TestPlaceUnfreezeTwoFrozenInOneTick(t *testing.T) {
	sc := newUnfreezeScene(t, 6*time.Second)
	sc.arm()
	for i := 0; i < 6; i++ {
		sc.frozenScene(i)
		if !sc.summoned["Mage"] {
			sc.put("Mage", unfreezePoint[0]+8, unfreezePoint[1], false) // the island seat, 8 yd from the point
		}
		sc.poll()
	}
	var got []string
	for _, l := range sc.f.agent.gmLines() {
		if strings.HasPrefix(l, ".go xyz ") {
			l = ".go xyz"
		}
		got = append(got, l)
	}
	if strings.Join(got, "|") != ".go xyz|.summon Priest|.summon Mage" {
		t.Fatalf("gm lines %v, want one .go then both summons", got)
	}
}

// F3: every placement move that sets a point arms the check again with that move's own point and spread: the summon
// wave of a retry or retreat arms a 6 yd ring at its point, and the clock and history start over.
func TestPlaceUnfreezeRearmsAtANewPoint(t *testing.T) {
	sc := newUnfreezeScene(t, 6*time.Second)
	sc.arm()
	sc.frozenScene(0)
	sc.poll()
	// The retreat: a new point 30 yd back, armed by the summon wave (spread 0).
	sc.w.place.Point = dungeon.Point{X: unfreezePoint[0] - 30, Y: unfreezePoint[1], Z: 79}
	sc.w.placeUnfreezeStart(sc.w.place.Point, 0)
	if u := sc.w.unfreeze; !u.started.IsZero() || len(u.hist) != 0 || u.point[0] != unfreezePoint[0]-30 || u.spread != 0 {
		t.Fatalf("re-armed state %+v, want a fresh check at the new point", u)
	}
	for i := 0; i < 8; i++ {
		x := float32(-11503 + 2*i)
		sc.put("Tank", x, -1316, true)
		sc.put("Rogue", x-3, -1316, true)
		sc.put("Priest", unfreezePoint[0], unfreezePoint[1], true) // the OLD point: 30 yd from the new one, not judged
		if !sc.summoned["Mage"] {
			sc.put("Mage", unfreezePoint[0]-30+5, unfreezePoint[1], true) // 5 yd from the new point, inside its 6 yd ring
		}
		sc.poll()
	}
	if s := sc.gmPrefixed(".summon "); len(s) != 1 || s[0] != ".summon Mage" {
		t.Fatalf("summons %v, want only the hire frozen at the new point", s)
	}
	if !strings.Contains(sc.notes(), fmt.Sprintf("placement: unfreeze check armed at %s -1316 79 (ring 6 yd)", num32(unfreezePoint[0]-30))) {
		t.Fatalf("notes lack the re-arm: %s", sc.notes())
	}
}

// R2-2: polls of 7 s whose every other view fails (no new frame). Against the poll's own time the last frame is 9 s
// old and the history holds, so the frozen Priest is re-placed; against the clock after the poll's waits it would be
// 16 s old, every hire's history would be dropped on each failed view and nobody would ever be judged.
func TestPlaceUnfreezeFailedViewKeepsThePollTime(t *testing.T) {
	sc := newUnfreezeScene(t, 7*time.Second)
	sc.f.agent.viewFn = func(n int) (bool, string) { return n%2 == 0, "" }
	sc.arm()
	for i := 0; i < 8; i++ {
		sc.frozenScene(i)
		sc.poll()
	}
	if s := sc.gmPrefixed(".summon "); len(s) != 1 || s[0] != ".summon Priest" {
		t.Fatalf("summons %v, want .summon Priest", s)
	}
}

// Codex high (round 2): the commander's `.go` is accepted at once but lands 2 s later. The summon must wait for the
// landing (it lands the hire at the commander), and a landing that never comes is a `.go` failure with no summon.
func TestPlaceUnfreezeWaitsForTheLanding(t *testing.T) {
	sc := newUnfreezeScene(t, 6*time.Second)
	a := sc.f.agent
	a.mu.Lock()
	a.runStarted = true
	a.goLagEvery = 3 // the position lands 3 status reads (1 s apart) after the go
	a.mu.Unlock()
	var goAt, summonAt time.Time
	var target, cmdrAtSummon [3]float32
	onGM := a.onGM
	a.onGM = func(line string) {
		a.mu.Lock()
		switch {
		case strings.HasPrefix(line, ".go xyz "):
			goAt, target = sc.f.clock.now(), a.goTarget
		case line == ".summon Priest":
			summonAt, cmdrAtSummon = sc.f.clock.now(), [3]float32{a.status.X, a.status.Y, a.status.Z}
		}
		a.mu.Unlock()
		onGM(line)
	}
	sc.arm()
	for i := 0; i < 8; i++ {
		sc.frozenScene(i)
		sc.poll()
	}
	if summonAt.IsZero() {
		t.Fatalf("no summon: gm %v, notes %s", a.gmLines(), sc.notes())
	}
	if d := summonAt.Sub(goAt); d < time.Second {
		t.Fatalf("summoned %v after the .go, before the landing", d)
	}
	if cmdrAtSummon != target {
		t.Fatalf("the commander stood at %v at the summon, want the landed target %v", cmdrAtSummon, target)
	}

	// The landing never comes: each try is a .go failure, nothing is summoned, the third stops the check.
	sc = newUnfreezeScene(t, 6*time.Second)
	a = sc.f.agent
	a.mu.Lock()
	a.runStarted = true
	a.goStuckFrom = 1
	a.mu.Unlock()
	sc.arm()
	for i := 0; i < 12; i++ {
		sc.frozenScene(i)
		sc.poll()
	}
	if s := sc.gmPrefixed(".summon "); len(s) != 0 {
		t.Fatalf("summons %v without a landing", s)
	}
	if !strings.Contains(sc.notes(), "the commander did not land within 5 yd") ||
		!strings.Contains(sc.notes(), "placement: unfreeze check stopped (the commander's .go onto Tank failed 3 times)") {
		t.Fatalf("notes %s, want three landing failures and the stop", sc.notes())
	}

	// The landing comes 7 yd off the target: inside the revive's 10 yd, outside the 5 yd a summon needs.
	sc = newUnfreezeScene(t, 6*time.Second)
	a = sc.f.agent
	a.mu.Lock()
	a.runStarted = true
	a.goLagEvery = 1
	a.mu.Unlock()
	onGM = a.onGM
	a.onGM = func(line string) {
		if strings.HasPrefix(line, ".go xyz ") {
			a.mu.Lock()
			a.goPend[0] += 7
			a.mu.Unlock()
		}
		onGM(line)
	}
	sc.arm()
	for i := 0; i < 12; i++ {
		sc.frozenScene(i)
		sc.poll()
	}
	if s := sc.gmPrefixed(".summon "); len(s) != 0 || !strings.Contains(sc.notes(), "the commander did not land within 5 yd") {
		t.Fatalf("summons %v, notes %s: a landing 7 yd off counted", s, sc.notes())
	}
}

// R2-3: the `.go` cap counts consecutive failures: two failures, a landing, two more failures leave the check on.
func TestPlaceUnfreezeGoFailuresResetOnALanding(t *testing.T) {
	sc := newUnfreezeScene(t, time.Second)
	gos := 0
	sc.f.agent.gmFail = func(line string) error {
		if strings.HasPrefix(line, ".go xyz ") {
			gos++
			if gos != 3 && gos != 6 {
				return errors.New("teleport refused")
			}
		}
		return nil
	}
	sc.arm()
	for i := 0; i < 30; i++ {
		sc.put("Tank", -11500, -1250, true)
		sc.put("Priest", unfreezePoint[0], unfreezePoint[1], true) // the summons never help
		sc.poll()
	}
	if gos < 6 || strings.Contains(sc.notes(), "unfreeze check stopped") {
		t.Fatalf("%d .go tries, notes %s: four scattered failures stopped the check", gos, sc.notes())
	}
	if n := len(sc.gmPrefixed(".summon Priest")); n != unfreezeMaxReplace {
		t.Fatalf("%d summons, want %d (one after each landing)", n, unfreezeMaxReplace)
	}
}

// R2-4: a re-arm that replaces a running check writes the old check's summary first.
func TestPlaceUnfreezeRearmWritesTheOldSummary(t *testing.T) {
	sc := newUnfreezeScene(t, 6*time.Second)
	sc.arm()
	for i := 0; i < 4; i++ {
		sc.frozenScene(i)
		sc.poll()
	}
	sc.w.placeUnfreezeStart(sc.w.place.Point, 0)
	notes := sc.notes()
	sum := strings.Index(notes, "placement: frozen=1 re-placed=1")
	rearm := strings.LastIndex(notes, "placement: unfreeze check armed")
	if sum < 0 || sum > rearm || !sc.w.unfreeze.active {
		t.Fatalf("notes %s: want the first arm's summary before the re-arm, and the new check on", notes)
	}
}

func TestParseNoPlaceUnfreezeFlag(t *testing.T) {
	var stderr bytes.Buffer
	args := []string{"onyxias_lair", "--place", "--from", "step 5", "--sql", "walk.sql"}
	o, code := parseRaidWalkArgs(args, &stderr)
	if code != exitOK || o.noPlaceUnfreeze {
		t.Fatalf("default: exit %d, noPlaceUnfreeze %v, stderr %q", code, o.noPlaceUnfreeze, stderr.String())
	}
	o, code = parseRaidWalkArgs(append(args, "--no-place-unfreeze"), &stderr)
	if code != exitOK || !o.noPlaceUnfreeze {
		t.Fatalf("--no-place-unfreeze: exit %d, noPlaceUnfreeze %v, stderr %q", code, o.noPlaceUnfreeze, stderr.String())
	}
}
