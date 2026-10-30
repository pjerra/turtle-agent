package raidwalk

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/pjerra/turtle-agent/internal/dungeon"
	"github.com/pjerra/turtle-agent/internal/packet"
	"github.com/pjerra/turtle-agent/internal/tbm"
)

func newOnyTracker(t *testing.T) *Tracker {
	t.Helper()
	return NewTracker(loadRaidPlan(t, "onyxias_lair"), botNames(39))
}

func botNames(n int) []string {
	out := make([]string, n)
	for i := range out {
		out[i] = fmt.Sprintf("Bot%02d", i)
	}
	return out
}

func runLine(plan, state string, step, steps int, leg, kind, phase, note string) tbm.RunStatus {
	line := fmt.Sprintf("TBM:RUN|%s|%s|%d/%d|%s|%s|%s|%s", plan, state, step, steps, leg, kind, phase, note)
	st, ok := ParseRun(line)
	if !ok {
		panic("bad test run line: " + line)
	}
	return st
}

func partyBot(guid uint64, name string, dead bool) tbm.PartyRec {
	state := "n"
	if dead {
		state = "d"
	}
	return tbm.PartyRec{GUID: packet.GUID(guid), Name: name, HP: 1, MaxHP: 1, Dead: dead, State: state}
}

func party(n int, dead bool) []tbm.PartyRec {
	out := make([]tbm.PartyRec, n)
	for i := range out {
		out[i] = partyBot(uint64(1000+i), fmt.Sprintf("Bot%02d", i), dead)
	}
	return out
}

func wipeFrom(evs []Incident) *IncidentWipe {
	for _, ev := range evs {
		if w, ok := ev.(IncidentWipe); ok {
			return &w
		}
	}
	return nil
}

func bossFrom(evs []Incident) *IncidentBossEngaged {
	for _, ev := range evs {
		if b, ok := ev.(IncidentBossEngaged); ok {
			return &b
		}
	}
	return nil
}

func stallFrom(evs []Incident) *IncidentStall {
	for _, ev := range evs {
		if s, ok := ev.(IncidentStall); ok {
			return &s
		}
	}
	return nil
}

func TestTrackerEndsAndIncidents(t *testing.T) {
	t0 := time.Date(2026, 10, 28, 20, 0, 0, 0, time.UTC)
	const plan = "onyxias_lair"

	t.Run("ends", func(t *testing.T) {
		tr := newOnyTracker(t)
		tr.Feed(t0, runLine(plan, tbm.RunRunning, 28, 29, "onyxia", "move", "walk", "walking"))
		tr.Feed(t0.Add(time.Second), runLine(plan, tbm.RunDone, 28, 29, "onyxia", "move", "walk",
			"stopped before step 29 (until boss): the party stays where it stands"))
		if got := tr.End(); got.Kind != EndUntil || got.Step != 28 {
			t.Fatalf("until end = %+v", got)
		}

		tr = newOnyTracker(t)
		tr.Feed(t0, runLine(plan, tbm.RunDone, 29, 29, "onyxia", "boss", "", "plan done (1 pack left alive)"))
		if got := tr.End().Kind; got != EndPlanDone {
			t.Fatalf("plan done end = %q", got)
		}

		tr = newOnyTracker(t)
		tr.Feed(t0, runLine(plan, tbm.RunDone, 12, 29, "onyxia", "pull", "", "stopped by .bot run stop"))
		if got := tr.End(); got.Kind != EndStoppedOther || !strings.Contains(got.Note, "stopped by") {
			t.Fatalf("stopped end = %+v", got)
		}

		tr = newOnyTracker(t)
		tr.Feed(t0, runLine(plan, tbm.RunNone, 0, 29, "-", "-", "-", "-"))
		if got := tr.End().Kind; got != EndNoRun {
			t.Fatalf("none end = %q", got)
		}
	})

	t.Run("wipe", func(t *testing.T) {
		tr := newOnyTracker(t)
		tr.Feed(t0, runLine(plan, tbm.RunRunning, 23, 29, "onyxia", "pull", "pull", "pulling"))
		tr.Frame(t0, tbm.ViewFrame{Party: party(39, true)})
		w := wipeFrom(tr.Events())
		if w == nil || w.Step != 23 {
			t.Fatalf("wipe = %+v, events %+v", w, tr.Events())
		}
		if got := tr.Wipes(); len(got) != 1 || got[0] != 23 {
			t.Fatalf("Wipes = %v", got)
		}
	})

	t.Run("not a wipe while the unseen are recent and alive or stale", func(t *testing.T) {
		tr := newOnyTracker(t)
		tr.Feed(t0, runLine(plan, tbm.RunRunning, 20, 29, "onyxia", "pull", "pull", "pulling"))
		tr.Frame(t0, tbm.ViewFrame{Party: party(39, false)})
		tr.Frame(t0.Add(30*time.Second), tbm.ViewFrame{Party: party(20, true)})
		if w := wipeFrom(tr.Events()); w != nil {
			t.Fatalf("wipe from 20 of 39: %+v", w)
		}
	})

	t.Run("every state must be fresh", func(t *testing.T) {
		tr := newOnyTracker(t)
		tr.Feed(t0, runLine(plan, tbm.RunRunning, 21, 29, "onyxia", "pull", "pull", "pulling"))
		tr.Frame(t0, tbm.ViewFrame{Party: party(19, true)})
		tr.Frame(t0.Add(11*time.Second), tbm.ViewFrame{Party: party(39, true)[19:]})
		if w := wipeFrom(tr.Events()); w != nil {
			t.Fatalf("wipe with 19 stale dead states: %+v", w)
		}
	})

	t.Run("truncated frames merge", func(t *testing.T) {
		tr := newOnyTracker(t)
		tr.Feed(t0, runLine(plan, tbm.RunRunning, 24, 29, "onyxia", "pull", "pull", "pulling"))
		all := party(39, true)
		tr.Frame(t0, tbm.ViewFrame{Party: all[:20], Truncated: true})
		if w := wipeFrom(tr.Events()); w != nil {
			t.Fatalf("wipe from a truncated first frame: %+v", w)
		}
		tr.Frame(t0.Add(2*time.Second), tbm.ViewFrame{Party: all[20:], Truncated: true})
		if w := wipeFrom(tr.Events()); w == nil || w.Step != 24 {
			t.Fatalf("no wipe from the merged frames")
		}
	})

	t.Run("boss engaged", func(t *testing.T) {
		tr := newOnyTracker(t)
		tr.Feed(t0, runLine(plan, tbm.RunRunning, 10, 29, "onyxia", "move", "walk", "walking"))
		tank := partyBot(1, "Tank", false)
		tr.Frame(t0, tbm.ViewFrame{
			Party:    []tbm.PartyRec{tank},
			Hostiles: []tbm.HostileRec{{GUID: 9, Entry: 10184, HPPct: 100, Target: tank.GUID}},
		})
		b := bossFrom(tr.Events())
		if b == nil || b.Entry != 10184 || b.Step != 10 {
			t.Fatalf("boss engaged = %+v", b)
		}
		if entry, step, ok := tr.BossEngaged(); !ok || entry != 10184 || step != 10 {
			t.Fatalf("BossEngaged = %d %d %v", entry, step, ok)
		}

		tr = newOnyTracker(t)
		tr.Feed(t0, runLine(plan, tbm.RunRunning, 10, 29, "onyxia", "move", "walk", "walking"))
		tr.Frame(t0, tbm.ViewFrame{
			Party:    []tbm.PartyRec{tank},
			Hostiles: []tbm.HostileRec{{GUID: 9, Entry: 10184, HPPct: 99, Target: 0}},
		})
		if b := bossFrom(tr.Events()); b == nil {
			t.Fatal("a boss below 100%% with no target did not engage")
		}

		tr = newOnyTracker(t)
		tr.Feed(t0, runLine(plan, tbm.RunRunning, 10, 29, "onyxia", "move", "walk", "walking"))
		tr.Frame(t0, tbm.ViewFrame{
			Party:    []tbm.PartyRec{tank},
			Hostiles: []tbm.HostileRec{{GUID: 9, Entry: 12129, HPPct: 100, Target: tank.GUID}},
		})
		if b := bossFrom(tr.Events()); b != nil {
			t.Fatalf("trash engaged a boss incident: %+v", b)
		}
	})

	t.Run("stall", func(t *testing.T) {
		tr := newOnyTracker(t)
		line := runLine(plan, tbm.RunRunning, 5, 29, "onyxia", "move", "walk", "walking")
		tr.Feed(t0, line)
		tr.Feed(t0.Add(9*time.Minute+59*time.Second), line)
		if s := stallFrom(tr.Events()); s != nil {
			t.Fatalf("stall before 10 min: %+v", s)
		}
		tr.Feed(t0.Add(10*time.Minute), line)
		if s := stallFrom(tr.Events()); s == nil || s.Step != 5 {
			t.Fatalf("no stall after 10 min")
		}
		tr.Feed(t0.Add(11*time.Minute), line)
		if s := stallFrom(tr.Events()); s != nil {
			t.Fatalf("stall repeated: %+v", s)
		}
	})

	t.Run("deaths, timeline and pauses", func(t *testing.T) {
		tr := newOnyTracker(t)
		tr.Feed(t0, runLine(plan, tbm.RunRunning, 1, 29, "onyxia", "move", "walk", "walking"))
		tr.Feed(t0.Add(10*time.Second), runLine(plan, tbm.RunRunning, 2, 29, "onyxia", "pull", "pull", "pulling"))
		tr.Feed(t0.Add(20*time.Second), runLine(plan, tbm.RunPaused, 2, 29, "onyxia", "pull", "pull", "3 dead: regroup"))
		tr.Feed(t0.Add(30*time.Second), runLine(plan, tbm.RunPaused, 2, 29, "onyxia", "pull", "pull", "3 dead: regroup"))
		tr.Feed(t0.Add(40*time.Second), runLine(plan, tbm.RunRunning, 2, 29, "onyxia", "pull", "pull", "pulling"))
		tr.Feed(t0.Add(50*time.Second), runLine(plan, tbm.RunRunning, 3, 29, "other_leg", "move", "walk", "walking"))
		tr.Feed(t0.Add(60*time.Second), runLine(plan, tbm.RunDone, 3, 29, "other_leg", "move", "walk", "plan done"))

		tl := tr.Timeline()
		if len(tl) != 3 {
			t.Fatalf("timeline = %+v", tl)
		}
		if tl[0].Step != 1 || tl[0].Seconds != 10 || tl[1].Step != 2 || tl[1].Seconds != 40 || tl[2].Step != 3 || tl[2].Seconds != 10 {
			t.Fatalf("timeline seconds = %+v", tl)
		}
		legs := tr.Legs()
		if len(legs) != 2 || legs[0].Leg != "onyxia" || legs[0].Seconds != 50 || legs[1].Leg != "other_leg" || legs[1].Seconds != 10 {
			t.Fatalf("legs = %+v", legs)
		}
		pauses := tr.Pauses()
		if len(pauses) != 1 || pauses[0].Step != 2 || pauses[0].Seconds != 20 || !pauses[0].Resumed || !strings.Contains(pauses[0].Note, "3 dead") {
			t.Fatalf("pauses = %+v", pauses)
		}

		tr.Frame(t0.Add(61*time.Second), tbm.ViewFrame{Party: party(39, false)})
		tr.Frame(t0.Add(63*time.Second), tbm.ViewFrame{Party: party(39, true)})
		tr.Frame(t0.Add(65*time.Second), tbm.ViewFrame{Party: party(39, true)})
		total, byStep, byBot := tr.Deaths()
		if total != 39 || byStep[3] != 39 || byBot["Bot00"] != 1 || byBot["Bot38"] != 1 {
			t.Fatalf("deaths total %d by step %v by bot %v", total, byStep, byBot)
		}

		sum := BuildSummary(tr, SummaryMeta{
			Plan: plan, Map: 249, Server: "m910q", Slot: 2, Character: "Commander",
			Until: "boss", Speed: 1, Started: t0, Ended: t0.Add(65 * time.Second), ExitCode: 0,
			Dashboard: "off",
		})
		if sum.Format != "tb-raid-walk" || sum.Version != 1 {
			t.Fatalf("summary header = %q v%d", sum.Format, sum.Version)
		}
		if sum.EndReason != "plan done" || sum.StepsReached != 3 || sum.StepsTotal != 29 {
			t.Fatalf("summary end/steps = %q %d/%d", sum.EndReason, sum.StepsReached, sum.StepsTotal)
		}
		if len(sum.StepTimeline) != 3 || len(sum.Legs) != 2 || sum.Deaths.Total != 39 || len(sum.Pauses) != 1 {
			t.Fatalf("summary detail = %+v", sum)
		}
		if text := sum.Text(); !strings.Contains(text, "steps 3/29") {
			t.Fatalf("summary text = %q", text)
		}
	})
}

func TestTrackerWipeFromDashboardSnapshot(t *testing.T) {
	t0 := time.Date(2026, 10, 28, 20, 0, 0, 0, time.UTC)
	names := botNames(39)
	dash := make([]DashBot, 39)
	for i := range names {
		dash[i] = DashBot{Name: names[i], Alive: i != 7}
	}
	tr := NewTracker(loadRaidPlan(t, "onyxias_lair"), names)
	tr.Feed(t0, runLine("onyxias_lair", tbm.RunRunning, 12, 29, "onyxia", "move", "walk", "walking"))
	tr.Snapshot(t0, dash)
	if w := wipeFrom(tr.Events()); w != nil {
		t.Fatalf("wipe while one bot is alive: %+v", w)
	}
	for i := range dash {
		dash[i].Alive = false
	}
	tr.Snapshot(t0.Add(10*time.Second), dash)
	if w := wipeFrom(tr.Events()); w == nil || w.Step != 12 {
		t.Fatal("no wipe from an all-dead dashboard snapshot")
	}
}

// A plan without a boss still tracks: the Tracker must not need one to report a stall or a wipe.
func TestTrackerWithoutBossEntries(t *testing.T) {
	p := &dungeon.Plan{Name: "empty_raid", Kind: dungeon.KindRaid}
	tr := NewTracker(p, nil)
	tr.Feed(time.Now(), runLine("empty_raid", tbm.RunRunning, 1, 3, "leg", "move", "walk", "walking"))
	if got := tr.End().Kind; got != EndRunning {
		t.Fatalf("end before any done = %q", got)
	}
}

// Without the hired names the tracker takes the plan's raid size - 1 as the count, so a truncated first frame
// cannot look like the whole party.
func TestTrackerInfersTheHireCountFromThePlan(t *testing.T) {
	t0 := time.Date(2026, 10, 28, 20, 0, 0, 0, time.UTC)
	tr := NewTracker(loadRaidPlan(t, "onyxias_lair"), nil)
	tr.Feed(t0, runLine("onyxias_lair", tbm.RunRunning, 7, 29, "onyxia", "pull", "pull", "pulling"))
	tr.Frame(t0, tbm.ViewFrame{Party: party(20, true), Truncated: true})
	if w := wipeFrom(tr.Events()); w != nil {
		t.Fatalf("wipe from 20 of the plan's 39 hires: %+v", w)
	}
	tr.Frame(t0.Add(time.Second), tbm.ViewFrame{Party: party(39, true)[20:], Truncated: true})
	if w := wipeFrom(tr.Events()); w == nil || w.Step != 7 {
		t.Fatal("no wipe once all 39 of the plan's hires were named dead")
	}

	dash := make([]DashBot, 39)
	for i := range dash {
		dash[i] = DashBot{Name: fmt.Sprintf("Bot%02d", i)}
	}
	tr = NewTracker(loadRaidPlan(t, "onyxias_lair"), nil)
	tr.Feed(t0, runLine("onyxias_lair", tbm.RunRunning, 8, 29, "onyxia", "pull", "pull", "pulling"))
	tr.Snapshot(t0, dash)
	if w := wipeFrom(tr.Events()); w == nil || w.Step != 8 {
		t.Fatal("no wipe from a dashboard snapshot of all 39 hires")
	}
}
