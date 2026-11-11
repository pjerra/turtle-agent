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

	"github.com/pjerra/turtle-agent/internal/tbm"
)

// TestSaveFightsSkipsHistoryDetail: the first read (pre-placement, saveFightsAtStart) lists id A; a later read
// lists A and C. Only this walk's fight is saved: combat-fight-C.json exists, combat-fight-A.json does not,
// w.fights holds A with History=true and C with History=false, and the dashboard got no detail call for A.
func TestSaveFightsSkipsHistoryDetail(t *testing.T) {
	goal := zgBossStepOf(t, 14517)
	now := time.Now()
	w, d := fightCreditWalk(t, goal, goal, now)
	w.manifest.Character = "Bot"
	rowA := jeklikKillRow("68-1", false, now.Unix()-3600)
	rowA.Step = uint32(goal)
	d.fights = []walkFightRow{rowA}
	w.saveFightsAtStart() // the pre-placement read: A is an earlier walk's history
	rowC := walkFightRow{ID: "68-2", Key: "Bot\x0068-2", Dur: 45000, Outcome: "win", Step: uint32(goal), Deaths: 1}
	d.fights = []walkFightRow{rowA, rowC}
	w.saveFights()
	if _, err := os.Stat(filepath.Join(w.outDir, "combat-fight-68-2.json")); err != nil {
		t.Fatalf("combat-fight-68-2.json: %v (this walk's fight must be saved)", err)
	}
	if _, err := os.Stat(filepath.Join(w.outDir, "combat-fight-68-1.json")); !os.IsNotExist(err) {
		t.Fatalf("combat-fight-68-1.json err %v: an earlier walk's fight must not be copied into this walk's dir", err)
	}
	byID := map[string]bool{}
	var histA, histC bool
	var haveA, haveC bool
	for _, f := range w.fights {
		byID[f.ID] = f.History
	}
	if h, ok := byID["68-1"]; ok {
		haveA, histA = true, h
	}
	if h, ok := byID["68-2"]; ok {
		haveC, histC = true, h
	}
	if !haveA || !histA {
		t.Fatalf("w.fights %+v: history record 68-1 must be kept with History=true", w.fights)
	}
	if !haveC || histC {
		t.Fatalf("w.fights %+v: this walk's fight 68-2 must be kept with History=false", w.fights)
	}
	if len(d.fightKeys) != 1 || d.fightKeys[0] != "Bot\x0068-2" {
		t.Fatalf("fight details fetched %q, want only this walk's fight 68-2", d.fightKeys)
	}
	if !w.seenFights["68-1"] {
		t.Fatalf("history record 68-1 not marked seen at once: %+v", w.seenFights)
	}
}

// TestFightsRunJSON: fights-run.json carries the walk's start and the sorted at-start ids, rewritten on every
// successful saveFights with the same set (a fight that first appears mid-walk is this walk's, not history).
func TestFightsRunJSON(t *testing.T) {
	goal := zgBossStepOf(t, 14517)
	now := time.Now()
	w, d := fightCreditWalk(t, goal, goal, now)
	w.manifest.Character = "Bot"
	mk := func(id string) walkFightRow {
		r := jeklikKillRow(id, false, now.Unix()-3600)
		r.Step = uint32(goal)
		return r
	}
	d.fights = []walkFightRow{mk("68-3"), mk("68-1"), mk("68-2")}
	w.saveFightsAtStart()
	read := func() (int64, []string) {
		t.Helper()
		raw, err := os.ReadFile(filepath.Join(w.outDir, "fights-run.json"))
		if err != nil {
			t.Fatal(err)
		}
		var v struct {
			WalkStarted int64    `json:"walk_started"`
			HistoryIDs  []string `json:"history_ids"`
		}
		if err := json.Unmarshal(raw, &v); err != nil {
			t.Fatal(err)
		}
		return v.WalkStarted, v.HistoryIDs
	}
	ws, ids := read()
	if ws != now.Unix() {
		t.Fatalf("walk_started %d, want %d", ws, now.Unix())
	}
	if got, want := strings.Join(ids, ","), "68-1,68-2,68-3"; got != want {
		t.Fatalf("history_ids %q, want %q", got, want)
	}
	mid := mk("68-4")
	d.fights = append(d.fights, mid)
	w.saveFights()
	if _, ids = read(); strings.Join(ids, ",") != "68-1,68-2,68-3" {
		t.Fatalf("history_ids after a later read %q, want the at-start set unchanged", ids)
	}
}

// teardownFightsWalk is a walk ready for phaseTeardown with a recording sleep and a canned teardown report.
func teardownFightsWalk(t *testing.T, now time.Time, slept *time.Duration) (*walk, *fakeDash) {
	t.Helper()
	goal := zgBossStepOf(t, 14517)
	w, d := fightCreditWalk(t, goal, goal, now)
	w.manifest.Character = "Bot"
	w.slotDir = "slot"
	nowMut := now
	w.env = walkEnv{
		now: func() time.Time { return nowMut },
		sleep: func(ctx context.Context, dur time.Duration) error {
			*slept += dur
			nowMut = nowMut.Add(dur)
			return nil
		},
		teardown: func(args []string, stdout, stderr io.Writer) int {
			fmt.Fprint(stdout, `{"server":"m910q","slot":"2","steps":[],"failed":0}`)
			return 0
		},
	}
	return w, d
}

// TestTeardownWaitsForLiveFight: reads 1-2 list C live, read 3 lists C finished as kill. The kill fight is saved
// and the summary carries one wait note.
func TestTeardownWaitsForLiveFight(t *testing.T) {
	goal := zgBossStepOf(t, 14517)
	now := time.Now()
	var slept time.Duration
	w, d := teardownFightsWalk(t, now, &slept)
	d.fights = nil
	w.saveFightsAtStart() // the walk starts with an empty dashboard: nothing is history
	live := jeklikKillRow("68-9", true, now.Unix()-600)
	live.Step = uint32(goal)
	done := jeklikKillRow("68-9", false, now.Unix()-600)
	done.Step = uint32(goal)
	d.fightsSeq = [][]walkFightRow{{live}, {live}, {done}}
	w.phaseTeardown(context.Background())
	if _, err := os.Stat(filepath.Join(w.outDir, "combat-fight-68-9.json")); err != nil {
		t.Fatalf("combat-fight-68-9.json: %v (the live kill fight must be saved once it ends)", err)
	}
	var waits []string
	for _, n := range w.notes {
		if strings.Contains(n, "at teardown for live fight") {
			waits = append(waits, n)
		}
	}
	if len(waits) != 1 {
		t.Fatalf("wait notes %q, want exactly one", waits)
	}
	if !strings.Contains(waits[0], "68-9") || !strings.Contains(waits[0], "waited 6 s") {
		t.Fatalf("wait note %q, want the 6 s wait for live fight 68-9", waits[0])
	}
	if slept != 6*time.Second {
		t.Fatalf("slept %v, want 6 s", slept)
	}
}

// TestTeardownGivesUpAfter30s: C stays live through the whole wait. The give-up note names it, the total fake
// sleep is 30 s, and no file is written.
func TestTeardownGivesUpAfter30s(t *testing.T) {
	goal := zgBossStepOf(t, 14517)
	now := time.Now()
	var slept time.Duration
	w, d := teardownFightsWalk(t, now, &slept)
	d.fights = nil
	w.saveFightsAtStart()
	live := jeklikKillRow("68-9", true, now.Unix()-600)
	live.Step = uint32(goal)
	d.fightsSeq = [][]walkFightRow{{live}}
	w.phaseTeardown(context.Background())
	if _, err := os.Stat(filepath.Join(w.outDir, "combat-fight-68-9.json")); !os.IsNotExist(err) {
		t.Fatalf("combat-fight-68-9.json err %v: a fight still live after 30 s must not be saved", err)
	}
	var giveups []string
	for _, n := range w.notes {
		if strings.Contains(n, "still live after") {
			giveups = append(giveups, n)
		}
	}
	if len(giveups) != 1 {
		t.Fatalf("give-up notes %q, want exactly one", giveups)
	}
	if !strings.Contains(giveups[0], "68-9") {
		t.Fatalf("give-up note %q, want it to name live fight 68-9", giveups[0])
	}
	if !strings.Contains(giveups[0], "30s") {
		t.Fatalf("give-up note %q, want the seconds from the const (30s)", giveups[0])
	}
	if slept != 30*time.Second {
		t.Fatalf("slept %v, want 30 s", slept)
	}
	for _, f := range w.fights {
		if f.ID == "68-9" {
			t.Fatalf("w.fights %+v: a still-live fight must not be recorded", w.fights)
		}
	}
}

// TestTeardownUnreachableDashboardRetriesOnceThenStops: every Fights call times out during the wait. The first
// failed read is retried once, the second stops the wait (total bounded by the const), teardown proceeds, and
// the notes name the retry and the stop.
func TestTeardownUnreachableDashboardRetriesOnceThenStops(t *testing.T) {
	goal := zgBossStepOf(t, 14517)
	now := time.Now()
	var slept time.Duration
	w, d := teardownFightsWalk(t, now, &slept)
	d.fights = nil
	w.saveFightsAtStart() // the walk starts with an empty dashboard: nothing is history
	live := jeklikKillRow("68-9", true, now.Unix()-600)
	live.Step = uint32(goal)
	d.fights = []walkFightRow{live}
	w.saveFights()                                      // one successful read: the wait has a live row of this walk to wait on
	d.fightsStallFirst, d.fightsStall = 1000, time.Hour // every later Fights call blocks until its context ends
	before := time.Now()
	w.phaseTeardown(context.Background())
	// The wait spends its whole budget before it gives up (it sleeps up to the deadline and reads until the
	// context's), so the wall clock lands on teardownLiveFightWait: allow the scheduler tick, not another poll.
	if took := time.Since(before); took > teardownLiveFightWait+time.Second {
		t.Fatalf("teardown took %v: an unreachable dashboard must not hold it past the wait", took)
	}
	if slept > teardownLiveFightWait {
		t.Fatalf("slept %v, want at most %s", slept, durWords(teardownLiveFightWait))
	}
	var retried, stopped []string
	for _, n := range w.notes {
		if strings.Contains(n, "retries after a failed dashboard fights read") {
			retried = append(retried, n)
		}
		if strings.Contains(n, "the teardown live-fight wait stopped") {
			stopped = append(stopped, n)
		}
	}
	if len(retried) != 1 {
		t.Fatalf("retry notes %q, want exactly one", retried)
	}
	if len(stopped) != 1 {
		t.Fatalf("stop notes %q, want exactly one naming the failed read", stopped)
	}
	if w.teardownWd.Failed {
		t.Fatalf("teardown %+v: the wait's failed read must not fail teardown itself", w.teardownWd)
	}
}

// TestTeardownLiveFightSkippedOnSignalStop: a cancelled walk context skips the wait entirely (no sleep, no retry),
// and teardown proceeds with a note naming the skip.
func TestTeardownLiveFightSkippedOnSignalStop(t *testing.T) {
	goal := zgBossStepOf(t, 14517)
	now := time.Now()
	var slept time.Duration
	w, d := teardownFightsWalk(t, now, &slept)
	d.fights = nil
	w.saveFightsAtStart()
	live := jeklikKillRow("68-9", true, now.Unix()-600)
	live.Step = uint32(goal)
	d.fights = []walkFightRow{live}
	w.saveFights()
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // the signal path: the walk context is already cancelled
	w.phaseTeardown(ctx)
	if slept != 0 {
		t.Fatalf("slept %v: a signal stop must skip the live-fight wait", slept)
	}
	var skipped []string
	for _, n := range w.notes {
		if strings.Contains(n, "live-fight wait is skipped") {
			skipped = append(skipped, n)
		}
	}
	if len(skipped) != 1 {
		t.Fatalf("skip notes %q, want exactly one", skipped)
	}
	if w.teardownWd.Failed {
		t.Fatalf("teardown %+v: skipping the wait must not fail teardown itself", w.teardownWd)
	}
}

// TestHistoryLiveAtStartFinishedGetsFile: a history row live in the at-start read (walk 41 tj-8's shape: the
// earlier raid tore down while the fight was still open) gets its detail file when it finishes during this
// walk, still tagged History=true. An old finished history row gets no file.
func TestHistoryLiveAtStartFinishedGetsFile(t *testing.T) {
	goal := zgBossStepOf(t, 14517)
	now := time.Now()
	w, d := fightCreditWalk(t, goal, goal, now)
	w.manifest.Character = "Bot"
	live := jeklikKillRow("68-1", true, now.Unix()-600)
	live.Step = uint32(goal)
	old := jeklikKillRow("68-2", false, now.Unix()-7200)
	old.Step = uint32(goal)
	d.fights = []walkFightRow{live, old}
	w.saveFightsAtStart() // both are history; 68-1 was still live
	done := jeklikKillRow("68-1", false, now.Unix()-600)
	done.Step = uint32(goal)
	d.fights = []walkFightRow{done, old}
	w.saveFights()
	if _, err := os.Stat(filepath.Join(w.outDir, "combat-fight-68-1.json")); err != nil {
		t.Fatalf("combat-fight-68-1.json: %v (a history fight live at start must be saved once it finishes)", err)
	}
	if _, err := os.Stat(filepath.Join(w.outDir, "combat-fight-68-2.json")); !os.IsNotExist(err) {
		t.Fatalf("combat-fight-68-2.json err %v: an old finished history fight gets no file", err)
	}
	byID := map[string]bool{}
	for _, f := range w.fights {
		byID[f.ID] = f.History
	}
	if h, ok := byID["68-1"]; !ok || !h {
		t.Fatalf("w.fights %+v: finished history fight 68-1 must be kept with History=true", w.fights)
	}
	if h, ok := byID["68-2"]; !ok || !h {
		t.Fatalf("w.fights %+v: old history fight 68-2 must be kept with History=true", w.fights)
	}
	if len(d.fightKeys) != 1 || d.fightKeys[0] != "Bot\x0068-1" {
		t.Fatalf("fight details fetched %q, want only the live-started history fight 68-1", d.fightKeys)
	}
}

// TestHistoryQualifyingFightDetailRetry: the first detail request for a qualifying history fight fails, the
// next succeeds. The file is written exactly once with History=true, and the fight is credited once (no
// duplicate row on the retry poll).
func TestHistoryQualifyingFightDetailRetry(t *testing.T) {
	goal := zgBossStepOf(t, 14517)
	now := time.Now()
	w, d := fightCreditWalk(t, goal, goal, now)
	w.manifest.Character = "Bot"
	// The run stood on the goal boss row (a qualifying history kill credits only then).
	feedStatus(w, tbm.RunStatus{Step: goal, Kind: "boss", Note: "fighting"}, now)
	live := jeklikKillRow("68-1", true, now.Unix()-600)
	live.Step = uint32(goal)
	d.fights = []walkFightRow{live}
	w.saveFightsAtStart() // 68-1 is history, live at start
	done := jeklikKillRow("68-1", false, now.Unix()-600)
	done.Step = uint32(goal)
	d.fights = []walkFightRow{done}
	d.failFight = 1 // the first detail request fails
	w.saveFights()
	if _, err := os.Stat(filepath.Join(w.outDir, "combat-fight-68-1.json")); !os.IsNotExist(err) {
		t.Fatalf("combat-fight-68-1.json err %v: no file yet, the first detail request failed", err)
	}
	if w.seenFights["68-1"] {
		t.Fatalf("seenFights %+v: a qualifying history fight is seen only after its detail parses", w.seenFights)
	}
	if _, ok := w.pendingFights["68-1"]; !ok {
		t.Fatalf("pendingFights %+v: the failed history fight must wait for a retry", w.pendingFights)
	}
	w.saveFights() // the retry succeeds
	if _, err := os.Stat(filepath.Join(w.outDir, "combat-fight-68-1.json")); err != nil {
		t.Fatalf("combat-fight-68-1.json: %v (the retry must write the history file)", err)
	}
	count := 0
	for _, f := range w.fights {
		if f.ID == "68-1" {
			count++
			if !f.History {
				t.Fatalf("w.fights %+v: the retried fight must keep History=true", w.fights)
			}
		}
	}
	if count != 1 {
		t.Fatalf("w.fights %+v: the history fight must be recorded exactly once", w.fights)
	}
	if !w.seenFights["68-1"] {
		t.Fatalf("seenFights %+v: the retried history fight must be seen now", w.seenFights)
	}
	if len(d.fightKeys) != 2 {
		t.Fatalf("fight details fetched %q, want exactly two requests", d.fightKeys)
	}
	if k := creditedBoss(t, w, goal); k == nil || !k.Killed {
		t.Fatalf("boss_kill %+v, want the retried history kill credited (notes %q)", k, w.notes)
	}
}
