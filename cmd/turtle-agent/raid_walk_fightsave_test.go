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
	w.phaseTeardown()
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
	w.phaseTeardown()
	if _, err := os.Stat(filepath.Join(w.outDir, "combat-fight-68-9.json")); !os.IsNotExist(err) {
		t.Fatalf("combat-fight-68-9.json err %v: a fight still live after 30 s must not be saved", err)
	}
	var giveups []string
	for _, n := range w.notes {
		if strings.Contains(n, "still live after 30 s") {
			giveups = append(giveups, n)
		}
	}
	if len(giveups) != 1 {
		t.Fatalf("give-up notes %q, want exactly one", giveups)
	}
	if !strings.Contains(giveups[0], "68-9") {
		t.Fatalf("give-up note %q, want it to name live fight 68-9", giveups[0])
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
