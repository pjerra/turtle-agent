package dungeon

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/pjerra/turtle-agent/internal/tracerun"
)

// The pull-test scenario (pulltest.go) against the fake commander of start_test.go.

func pulltestPlan(equiv string) *Plan {
	return remotePlan(Leg{Name: "pack", To: Point{100, 0, 0}, PullGroups: []PullGroup{
		{Entries: []uint32{299}, Positions: []Point{{200, 0, 0}}, Equiv: equiv},
		{Entries: []uint32{299}, Positions: []Point{{400, 0, 0}}, Equiv: equiv},
		{Entries: []uint32{299}, Positions: []Point{{600, 0, 0}}, Equiv: equiv},
	}})
}

func ptOpts(spec PulltestSpec) RemoteOptions {
	o := startOpts()
	o.Tank = "Tanky"
	n := spec.PullGroup
	o.FromPullGroup = &n
	s := spec.Normalized()
	o.Pulltest = &s
	return o
}

func runPulltestOn(t *testing.T, g *startFake, plan *Plan, spec PulltestSpec) (Report, error) {
	t.Helper()
	return runFrom(t, g, plan, ptOpts(spec), spec.Leg)
}

// With equivalent packs the next try pulls the next pack of the set, the party walks there, no GM line is sent beyond the start, and each
// try is scored.
func TestPulltestEquivalentPacks(t *testing.T) {
	g := newStartFake(t)
	for _, x := range []float32{200, 400, 600} {
		g.addMob(299, Point{x, 0, 0})
	}
	rep, err := runPulltestOn(t, g, pulltestPlan("trio"), PulltestSpec{Leg: 0, PullGroup: 0, Tries: 3})
	if err != nil || rep.State != "done" {
		t.Fatalf("%v %s\n%s", err, rep.State, notesOf(rep))
	}
	pt := rep.Pulltest
	if pt == nil || len(pt.Tries) != 3 || pt.Stopped != "" || pt.Summary.Tries != 3 || pt.Summary.Killed != 3 {
		t.Fatalf("%+v", pt)
	}
	for i, tr := range pt.Tries {
		wantReset := "equivalent pack"
		if i == 0 {
			wantReset = "first spawn"
		}
		if tr.PullGroup != i || tr.Reset != wantReset || tr.Outcome != TryKilled || tr.Kills != 1 || tr.TimeToKillS <= 0 || tr.Seconds < tr.TimeToKillS || tr.Deaths != 0 || tr.Adds != 0 {
			t.Fatalf("try %d: %+v", i+1, tr)
		}
	}
	if len(g.mobs) != 0 || len(g.ordersOf("cmd pullback")) != 3 {
		t.Fatalf("mobs %v pullbacks %v", g.mobs, g.ordersOf("cmd pullback"))
	}
	// GM use: the start's two lines only; nothing was respawned.
	if g.respawns != 0 || rep.Remote.GMCommands != 2 || rep.Remote.RunGMAssists != 0 || len(pt.GMCommands) != 0 {
		t.Fatalf("respawns %d gm %d assists %d", g.respawns, rep.Remote.GMCommands, rep.Remote.RunGMAssists)
	}
	if pt.Summary.TimeToKillMeanS <= 0 || pt.Summary.HealerMinManaPct != 100 {
		t.Fatalf("summary %+v", pt.Summary)
	}
	if rep.Start == nil || *rep.Start.PullGroup != 0 {
		t.Fatalf("start %+v", rep.Start)
	}
	if _, err := json.Marshal(rep); err != nil {
		t.Fatal(err)
	}
}

// Without an equivalent pack the commander's own .respawn brings the pack back: the party walks back to the gather spot first, and the GM
// assist is counted in run_gm_assists, setup_shortcuts and the result.
func TestPulltestRespawn(t *testing.T) {
	g := newStartFake(t)
	g.addMob(299, Point{200, 0, 0})
	g.respawnFn = func(g *startFake) { g.addMob(299, Point{200, 0, 0}) }
	rep, err := runPulltestOn(t, g, pulltestPlan(""), PulltestSpec{Leg: 0, PullGroup: 0, Tries: 3})
	if err != nil || rep.State != "done" {
		t.Fatalf("%v %s\n%s", err, rep.State, notesOf(rep))
	}
	pt := rep.Pulltest
	if len(pt.Tries) != 3 || pt.Summary.Killed != 3 || pt.Tries[1].Reset != "respawn" || pt.Tries[2].Reset != "respawn" || pt.Tries[1].PullGroup != 0 {
		t.Fatalf("%+v", pt)
	}
	if g.respawns != 2 || rep.Remote.RunGMAssists != 2 || rep.Remote.GMCommands != 2 || len(pt.GMCommands) != 2 {
		t.Fatalf("respawns %d assists %d gm %d / %v", g.respawns, rep.Remote.RunGMAssists, rep.Remote.GMCommands, pt.GMCommands)
	}
	n := 0
	for _, s := range rep.SetupShortcuts {
		if s == ".respawn (GM assist during the run: pulltest reset)" {
			n++
		}
	}
	if n != 2 {
		t.Fatalf("setup shortcuts %v", rep.SetupShortcuts)
	}
}

// With neither an equivalent pack nor the right to respawn, the scenario says so and stops after one try. The same for reset none.
func TestPulltestOneTryPerSpawn(t *testing.T) {
	for _, c := range []struct {
		name  string
		level int
		reset string
		want  string
	}{
		{"player level", 1, "", ".respawn needs account level 3, the commander's is 1"},
		{"reset none", 3, ResetNone, "reset is none"},
		{"equivalent only", 3, ResetEquivalent, "no equivalent pack left"},
	} {
		g := newStartFake(t)
		g.level = c.level
		g.addMob(299, Point{200, 0, 0})
		g.respawnFn = func(g *startFake) { g.addMob(299, Point{200, 0, 0}) }
		rep, err := runPulltestOn(t, g, pulltestPlan(""), PulltestSpec{Leg: 0, PullGroup: 0, Tries: 3, Reset: c.reset})
		if err != nil || rep.State != "done" {
			t.Fatalf("%s: %v %s\n%s", c.name, err, rep.State, notesOf(rep))
		}
		pt := rep.Pulltest
		if len(pt.Tries) != 1 || !strings.Contains(pt.Stopped, "one try per spawn") || !strings.Contains(pt.Stopped, c.want) || g.respawns != 0 {
			t.Fatalf("%s: %+v respawns %d", c.name, pt, g.respawns)
		}
	}
}

// A respawn that brings nothing back ends the scenario and the empty try is not scored as a kill.
func TestPulltestRespawnThatBringsNothing(t *testing.T) {
	g := newStartFake(t)
	g.addMob(299, Point{200, 0, 0})
	rep, err := runPulltestOn(t, g, pulltestPlan(""), PulltestSpec{Leg: 0, PullGroup: 0, Tries: 3})
	if err != nil {
		t.Fatalf("%v\n%s", err, notesOf(rep))
	}
	pt := rep.Pulltest
	if len(pt.Tries) != 2 || pt.Tries[1].Outcome != TryNoPack || pt.Summary.Tries != 1 || pt.Summary.Killed != 1 || !strings.Contains(pt.Stopped, "pack was not found") {
		t.Fatalf("%+v", pt)
	}
	// The pack was not looked for forever: the second try ended on the plan's own bounds.
	if g.respawns != 1 {
		t.Fatalf("respawns %d", g.respawns)
	}
}

// The numbers a try holds: a mob off the tank for a while, an add that joins, the healer's lowest mana, a death.
func TestPulltestScoresTheFight(t *testing.T) {
	g := newStartFake(t)
	pack := g.addMob(299, Point{200, 0, 0})
	pack.hold = gMage // the mob goes for the mage until the tank is sent at it
	add := g.addMob(777, Point{210, 0, 0})
	died := false
	g.extra = func(g *startFake) {
		engaged := false
		for _, m := range g.mobs {
			engaged = engaged || m.engaged
		}
		if pack.engaged && !add.engaged {
			add.pulled, add.engaged, add.attacked = true, true, false
		}
		healer := g.party[1]
		if engaged {
			healer.Power = 250
			if !died {
				died = true
				g.party[2].Dead, g.party[2].HP, g.party[2].State = true, 0, "d"
			}
		} else {
			healer.Power = healer.MaxPower
		}
	}
	o := ptOpts(PulltestSpec{Leg: 0, PullGroup: 0, Tries: 1})
	o.Healer = "Healy"
	rep, err := runFrom(t, g, pulltestPlan(""), o, 0)
	if err != nil {
		t.Fatalf("%v\n%s", err, notesOf(rep))
	}
	tr := rep.Pulltest.Tries[0]
	if tr.Outcome != TryKilled || tr.Deaths != 1 || fmt.Sprint(tr.DeathNames) != "[Magey]" {
		t.Fatalf("%+v", tr)
	}
	if tr.OffTankS <= 0 || tr.OffTankMobs < 1 || tr.MaxOffTankS <= 0 || tr.MaxOffTankS > tr.OffTankS+0.01 {
		t.Fatalf("off tank: %+v", tr)
	}
	if tr.Adds != 1 || fmt.Sprint(tr.AddEntries) != "[777]" {
		t.Fatalf("adds: %+v", tr)
	}
	if tr.HealerMinManaPct != 25 || rep.Pulltest.Healer != "Healy" {
		t.Fatalf("healer: %+v %s", tr, rep.Pulltest.Healer)
	}
	if tr.RestS <= 0 || tr.TimeToKillS <= 0 {
		t.Fatalf("rest/ttk: %+v", tr)
	}
	if rep.Pulltest.Summary.Deaths != 1 || rep.Pulltest.Summary.Adds != 1 || rep.Pulltest.Summary.HealerMinManaPct != 25 {
		t.Fatalf("summary %+v", rep.Pulltest.Summary)
	}
}

// A wipe ends the scenario with the try scored as a wipe, and the run fails (it does not go on).
func TestPulltestWipeEndsTheScenario(t *testing.T) {
	g := newStartFake(t)
	g.addMob(299, Point{200, 0, 0})
	g.extra = func(g *startFake) {
		for _, m := range g.mobs {
			if m.engaged {
				for _, p := range g.party {
					p.Dead, p.HP, p.State = true, 0, "d"
				}
			}
		}
	}
	rep, err := runPulltestOn(t, g, pulltestPlan(""), PulltestSpec{Leg: 0, PullGroup: 0, Tries: 3})
	if err == nil || rep.State != "failed" || rep.Wipes != 1 {
		t.Fatalf("%v %s wipes %d", err, rep.State, rep.Wipes)
	}
	pt := rep.Pulltest
	if pt == nil || len(pt.Tries) != 1 || pt.Tries[0].Outcome != TryWipe || pt.Tries[0].Deaths != 3 || pt.Summary.Killed != 0 || g.unbinds != 1 || g.respawns != 0 {
		t.Fatalf("%+v unbinds %d", pt, g.unbinds)
	}
}

// The result is written beside the trace and read back, and the table and the comparison print.
func TestPulltestResultFileAndCompare(t *testing.T) {
	dir := t.TempDir()
	g := newStartFake(t)
	for _, x := range []float32{200, 400} {
		g.addMob(299, Point{x, 0, 0})
	}
	o := ptOpts(PulltestSpec{Leg: 0, PullGroup: 0, Tries: 2})
	cfg := DefaultConfig()
	cfg.PostFight, cfg.AddsWait = 2*time.Second, 4*time.Second
	cfg.Trace = tracerun.Config{On: true, Dir: dir, RunID: "pt-a"}
	d := NewRemoteDriver(pulltestPlan("duo"), g, cfg, o, g.clock())
	if err := d.Run(context.Background(), 0); err != nil {
		t.Fatal(err)
	}
	rep := d.Report()
	file := filepath.Join(dir, "pt-a.pulltest.json")
	if rep.Pulltest == nil || rep.Pulltest.File != file {
		t.Fatalf("file %+v", rep.Pulltest)
	}
	data, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	a, err := LoadPulltest(data)
	if err != nil || len(a.Tries) != 2 || a.RunID != "pt-a" || a.Summary.Killed != 2 {
		t.Fatalf("%v %+v", err, a)
	}
	// The same result inside a dungeon_status answer reads too.
	wrapped, _ := json.Marshal(map[string]any{"result": map[string]any{"state": "done", "report": map[string]any{"pulltest": a}}})
	if w, err := LoadPulltest(wrapped); err != nil || w.Scenario() != a.Scenario() {
		t.Fatalf("%v %+v", err, w)
	}
	table := FormatPulltest(a)
	for _, want := range []string{"pulltest rt leg 0 (pack) pull group 0 [299]", "tries 2/2", "first spawn", "equivalent pack", "killed", "sum  killed 2/2", "GM resets: none"} {
		if !strings.Contains(table, want) {
			t.Fatalf("table lacks %q:\n%s", want, table)
		}
	}
	b := *a
	b.RunID = "pt-b"
	b.Tries = append([]PulltestTry(nil), a.Tries...)
	b.Tries[0].TimeToKillS = a.Tries[0].TimeToKillS + 10
	b.Tries[1].Deaths = 1
	b.Summarize()
	cmp := ComparePulltest(a, &b)
	for _, want := range []string{"A: rt leg 0", "WARNING: fewer than 3 scored tries", "time to kill, mean s", "worse", "deaths", "+1.0"} {
		if !strings.Contains(cmp, want) {
			t.Fatalf("compare lacks %q:\n%s", want, cmp)
		}
	}
	other := b
	other.PullGroup = 1
	if !strings.Contains(ComparePulltest(a, &other), "not the same pull") {
		t.Fatal("different pulls compare without a warning")
	}
	for _, bad := range []string{`{}`, `[1]`, `{"version":9,"tries":[]}`, `not json`} {
		if _, err := LoadPulltest([]byte(bad)); err == nil {
			t.Errorf("%q read as a result", bad)
		}
	}
}

func TestPulltestSpecValidation(t *testing.T) {
	p := pulltestPlan("")
	p.Legs[0].PullGroups[2].Ambush = true
	for _, c := range []struct {
		s    PulltestSpec
		want string
	}{
		{PulltestSpec{Leg: 5}, "the plan has legs 0-0"},
		{PulltestSpec{PullGroup: 3}, "has 3 pull groups"},
		{PulltestSpec{PullGroup: -1}, "pulltest.pull_group"},
		{PulltestSpec{Tries: 11}, "use 1-10"},
		{PulltestSpec{Tries: -1}, "use 1-10"},
		{PulltestSpec{Reset: "fast"}, "pulltest.reset"},
		{PulltestSpec{PullGroup: 2}, "an ambush"},
	} {
		if err := c.s.Validate(p); err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%+v: %v, want %q", c.s, err, c.want)
		}
	}
	if err := (PulltestSpec{Reset: " Respawn "}).Validate(p); err != nil {
		t.Fatal(err)
	}
	if s := (PulltestSpec{}).Normalized(); s.Tries != 3 || s.Reset != ResetAuto {
		t.Fatalf("%+v", s)
	}
}

// A wipe ends the scenario also with recover_wipe on: the scenario measures a pull, it does not keep going after one that killed the party.
func TestPulltestWipeWithRecoverWipeOn(t *testing.T) {
	g := newStartFake(t)
	g.addMob(299, Point{200, 0, 0})
	g.extra = func(g *startFake) {
		for _, m := range g.mobs {
			if m.engaged {
				for _, p := range g.party {
					p.Dead, p.HP, p.State = true, 0, "d"
				}
			}
		}
	}
	cfg := DefaultConfig()
	cfg.PostFight, cfg.AddsWait, cfg.RecoverWipe = 2*time.Second, 4*time.Second, true
	d := NewRemoteDriver(pulltestPlan(""), g, cfg, ptOpts(PulltestSpec{Tries: 3}), g.clock())
	err := d.Run(context.Background(), 0)
	rep, _ := d.Status()
	if err == nil || rep.State != "failed" || len(rep.Pulltest.Tries) != 1 || rep.Pulltest.Tries[0].Outcome != TryWipe || g.unbinds != 1 {
		t.Fatalf("%v %s %+v", err, rep.State, rep.Pulltest)
	}
}

// A bound that holds across the scenario: a pull that never draws a fight ends the try on the plan's own limits, not on a wait of the scenario's.
func TestPulltestPackThatNeverComes(t *testing.T) {
	g := newStartFake(t)
	m := g.addMob(299, Point{200, 0, 0})
	m.inert = true // the pull is acknowledged and nothing comes
	rep, err := runPulltestOn(t, g, pulltestPlan(""), PulltestSpec{Leg: 0, PullGroup: 0, Tries: 3})
	pt := rep.Pulltest
	if pt == nil || len(pt.Tries) != 1 || pt.Tries[0].Outcome == TryKilled || g.now.Sub(time.Unix(1_000_000, 0)) > 30*time.Minute {
		t.Fatalf("%v %+v at %s", err, pt, g.now)
	}
}
