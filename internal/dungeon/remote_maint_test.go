package dungeon

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/pjerra/turtle-agent/internal/trace"
	"github.com/pjerra/turtle-agent/internal/tracerun"
)

// maintPlan is n legs, each a walk of 60 yd; leg 3 (when n > 3) also has a pull.
func maintPlan(n int) *Plan {
	var legs []Leg
	for i := 0; i < n; i++ {
		l := Leg{Name: string(rune('a' + i)), To: Point{float32(60 * (i + 1)), 0, 0}}
		if i == 3 {
			l.PullGroups = []PullGroup{{Entries: []uint32{299}, Positions: []Point{{float32(60*(i+1) + 15), 5, 0}}}}
		}
		legs = append(legs, l)
	}
	return remotePlan(legs...)
}

// maintFake records where the lead bot stood and whether the lead bot's state was combat when each maintenance line went out.
type maintFake struct {
	*botFake
	sentAt []maintSend
}

type maintSend struct {
	bot    string
	x      float32
	combat bool
}

func (g *maintFake) BotLines(ctx context.Context, command string, wait time.Duration, done func(string) bool) ([]string, bool, error) {
	if name, ok := strings.CutPrefix(command, "maintenance "); ok {
		g.sentAt = append(g.sentAt, maintSend{bot: name, x: g.lead().X, combat: g.now.Before(g.combatUntil)})
	}
	return g.botFake.BotLines(ctx, command, wait, done)
}

func newMaintFake(t *testing.T) *maintFake {
	g := &maintFake{botFake: kitFake(t)}
	g.addMob(299, Point{75 + 180, 5, 0})
	return g
}

func runMaint(t *testing.T, g RemoteGame, plan *Plan, o RemoteOptions, dir string, mod func(*Config, *RemoteDriver)) (Report, *RemoteDriver, error) {
	t.Helper()
	cfg := DefaultConfig()
	cfg.PostFight, cfg.AddsWait = 2*time.Second, 4*time.Second
	if dir != "" {
		cfg.Trace = tracerun.Config{On: true, Dir: dir, RunID: "r-maint"}
	}
	clk := g.(interface{ clock() Clock }).clock()
	d := NewRemoteDriver(plan, g, cfg, o, clk)
	if mod != nil {
		mod(&cfg, d)
	}
	err := d.Run(context.Background(), 0)
	rep, ended := d.Status()
	if !ended {
		t.Fatal("the report does not say the run ended")
	}
	return rep, d, err
}

func maintEvents(t *testing.T, dir string) []trace.Event {
	t.Helper()
	f, err := trace.Read(filepath.Join(dir, "r-maint.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	var out []trace.Event
	for _, e := range f.Events {
		if e.Ev == "maintenance" {
			out = append(out, e)
		}
	}
	return out
}

// With the default (every 3 legs) the whole party is maintained at the start of legs 3 and 6 of eight, on top of the preflight's; a trace
// event and the report's counter say so. Leg 0 is not one of them.
func TestMaintenanceFiresOnLegs3And6(t *testing.T) {
	g := newMaintFake(t)
	dir := t.TempDir()
	rep, _, err := runMaint(t, g, maintPlan(8), kitOpts(), dir, nil)
	if err != nil || rep.State != "done" {
		t.Fatalf("%v %v", err, rep.Notes)
	}
	if rep.Remote.MaintenanceRuns != 2 || len(rep.Remote.MaintenanceSkips) != 0 {
		t.Fatalf("%+v", rep.Remote)
	}
	for _, n := range []string{"Tanky", "Healy", "Magey"} {
		if got := len(g.sent("maintenance " + n)); got != 3 { // the preflight, leg 3, leg 6
			t.Fatalf("%s: maintenance sent %d times: %v", n, got, g.lines)
		}
	}
	ev := maintEvents(t, dir)
	if len(ev) != 2 {
		t.Fatalf("%d maintenance events", len(ev))
	}
	for i, want := range []float64{3, 6} {
		if ev[i].D["leg"] != want {
			t.Fatalf("event %d: %+v", i, ev[i].D)
		}
	}
	// The first round of leg 3 went out when the lead bot had arrived at leg 2 (X 180), not at leg 6's end.
	if x := g.sentAt[3].x; x < 170 || x > 200 {
		t.Fatalf("leg 3's round went out at X %v", x)
	}
	if !strings.Contains(notesOf(rep), "maintenance (leg)") {
		t.Fatalf("%s", notesOf(rep))
	}
}

// 0 turns it off, in the tunable and in the config; fewer than 4 legs never reach leg 3.
func TestMaintenanceOffAndShortRuns(t *testing.T) {
	g := newMaintFake(t)
	rep, _, err := runMaint(t, g, maintPlan(8), kitOpts(), "", func(c *Config, d *RemoteDriver) { d.cfg.MaintenanceEveryLegs = 0 })
	if err != nil || rep.Remote.MaintenanceRuns != 0 || len(g.sent("maintenance Tanky")) != 1 {
		t.Fatalf("%v %+v %v", err, rep.Remote, g.lines)
	}
	g = newMaintFake(t)
	rep, _, err = runMaint(t, g, maintPlan(3), kitOpts(), "", nil)
	if err != nil || rep.Remote.MaintenanceRuns != 0 || len(g.sent("maintenance Tanky")) != 1 {
		t.Fatalf("%v %+v %v", err, rep.Remote, g.lines)
	}
	// Every 2 legs: legs 2, 4 and 6 of eight.
	g = newMaintFake(t)
	rep, _, err = runMaint(t, g, maintPlan(8), kitOpts(), "", func(c *Config, d *RemoteDriver) { d.cfg.MaintenanceEveryLegs = 2 })
	if err != nil || rep.Remote.MaintenanceRuns != 3 {
		t.Fatalf("%v %+v", err, rep.Remote)
	}
}

// A party in combat when leg 3 starts is maintained at the next calm moment of the same leg, not at the start and never in combat.
func TestMaintenanceWaitsForCalmWithinTheLeg(t *testing.T) {
	g := newMaintFake(t)
	dir := t.TempDir()
	rep, _, err := runMaint(t, g, maintPlan(5), kitOpts(), dir, func(c *Config, d *RemoteDriver) {
		g.onTick = func(g *fakeRemote) {
			if (d.curLeg == 2 && g.lead().X >= 170) || (d.curLeg == 3 && g.lead().X < 235) {
				g.combatUntil = g.now.Add(5 * time.Second)
			}
		}
	})
	if err != nil || rep.State != "done" {
		t.Fatalf("%v %v", err, rep.Notes)
	}
	if rep.Remote.MaintenanceRuns != 1 || rep.Remote.MaintenanceSkips["combat"] != 0 {
		t.Fatalf("%+v\n%s", rep.Remote, notesOf(rep))
	}
	if len(g.sentAt) != 6 {
		t.Fatalf("%v", g.lines)
	}
	for _, s := range g.sentAt[3:] {
		if s.combat {
			t.Fatalf("maintenance went out in combat: %+v", s)
		}
		if s.x < 235 || s.x > 300 {
			t.Fatalf("the round was not in leg 3 after the walk: %+v", s)
		}
	}
	ev := maintEvents(t, dir)
	if len(ev) != 1 || ev[0].D["leg"] != float64(3) {
		t.Fatalf("%+v", ev)
	}
}

// A leg that never has a calm moment gives its slot up when it ends; the counter says why, and the next slot (leg 6) still runs.
func TestMaintenanceGivesUpWhenTheLegEnds(t *testing.T) {
	g := newMaintFake(t)
	dir := t.TempDir()
	rep, _, err := runMaint(t, g, maintPlan(8), kitOpts(), dir, func(c *Config, d *RemoteDriver) {
		g.onTick = func(g *fakeRemote) {
			if (d.curLeg == 2 && g.lead().X >= 170) || d.curLeg == 3 {
				g.combatUntil = g.now.Add(5 * time.Second)
			}
		}
	})
	if err != nil || rep.State != "done" {
		t.Fatalf("%v %v", err, rep.Notes)
	}
	if rep.Remote.MaintenanceRuns != 1 || rep.Remote.MaintenanceSkips["combat"] != 1 {
		t.Fatalf("%+v\n%s", rep.Remote, notesOf(rep))
	}
	for _, s := range g.sentAt[3:] {
		if s.combat || s.x < 330 {
			t.Fatalf("%+v", s)
		}
	}
	if ev := maintEvents(t, dir); len(ev) != 2 || ev[0].D["skipped"] != "combat" {
		t.Fatalf("%+v", ev)
	}
}

// After a recovered wipe the party is maintained once, apart from the per-leg slot (the run has one leg: its slot never comes).
func TestMaintenanceAfterAWipeRecovery(t *testing.T) {
	g := newMaintFake(t)
	g.mobs = nil
	g.addMob(299, Point{30, 0, 0})
	g.killRate = 25
	first := true
	g.onTick = func(g *fakeRemote) {
		if first {
			for _, m := range g.mobs {
				if m.engaged {
					first = false
					for _, p := range g.party {
						p.Dead, p.HP = true, 0
					}
					m.hp, m.engaged, m.pulled, m.attacked, m.target = 100, false, false, false, 0
				}
			}
		}
	}
	dir := t.TempDir()
	rep, _, err := runMaint(t, g, plan1(), kitOpts(), dir, func(c *Config, d *RemoteDriver) { d.cfg.RecoverWipe = true })
	if err != nil || rep.Wipes != 1 || rep.State != "done" {
		t.Fatalf("%v wipes %d %v", err, rep.Wipes, rep.Notes)
	}
	if rep.Remote.MaintenanceRuns != 1 || len(g.sentAt) != 6 {
		t.Fatalf("%+v %v", rep.Remote, g.lines)
	}
	for _, s := range g.sentAt[3:] {
		if s.combat {
			t.Fatalf("%+v", s)
		}
	}
	ev := maintEvents(t, dir)
	if len(ev) != 1 || ev[0].D["reasons"].([]any)[0] != "wipe" {
		t.Fatalf("%+v", ev)
	}
	// With the tunable at 0 the recovery sends nothing.
	g2 := newMaintFake(t)
	g2.mobs = nil
	g2.addMob(299, Point{30, 0, 0})
	g2.killRate = 25
	first = true
	g2.onTick = func(g *fakeRemote) {
		if first {
			for _, m := range g.mobs {
				if m.engaged {
					first = false
					for _, p := range g.party {
						p.Dead, p.HP = true, 0
					}
					m.hp, m.engaged, m.pulled, m.attacked, m.target = 100, false, false, false, 0
				}
			}
		}
	}
	rep, _, err = runMaint(t, g2, plan1(), kitOpts(), "", func(c *Config, d *RemoteDriver) { d.cfg.RecoverWipe, d.cfg.MaintenanceEveryLegs = true, 0 })
	if err != nil || rep.Wipes != 1 || rep.Remote.MaintenanceRuns != 0 || len(g2.sentAt) != 3 {
		t.Fatalf("%v %+v", err, rep.Remote)
	}
}

// Maintenance that the server turned off (at the preflight, or in the middle of the run) is not sent again: the due rounds are counted as
// skipped, "disabled".
func TestMaintenanceDisabledKit(t *testing.T) {
	off := []string{"Owned-bot maintenance is disabled (AiPlayerbot.OwnedBotMaintenanceEnabled = 0)."}
	g := newMaintFake(t)
	g.replies["maintenance Tanky"] = off
	rep, _, err := runMaint(t, g, maintPlan(8), kitOpts(), "", nil)
	if err != nil || rep.Remote.MaintenanceRuns != 0 || rep.Remote.MaintenanceSkips["disabled"] != 2 || len(g.sent("maintenance")) != 1 {
		t.Fatalf("%v %+v %v", err, rep.Remote, g.lines)
	}
	// Off only from leg 3 on: the round of leg 3 gets the verdict and stops; leg 6 sends nothing.
	g = newMaintFake(t)
	rep, _, err = runMaint(t, g, maintPlan(8), kitOpts(), "", func(c *Config, d *RemoteDriver) {
		g.onTick = func(*fakeRemote) {
			if d.curLeg >= 2 {
				g.replies["maintenance Tanky"] = off
			}
		}
	})
	if err != nil || rep.Remote.MaintenanceRuns != 0 || rep.Remote.MaintenanceSkips["disabled"] != 2 || len(g.sent("maintenance")) != 4 {
		t.Fatalf("%v %+v %v", err, rep.Remote, g.lines)
	}
}

// A game that cannot send bot lines sends none, and says so in the counter.
func TestMaintenanceWithoutBotLines(t *testing.T) {
	g := newFakeRemote(t)
	rep, err := runRemote(t, g, maintPlan(8), opts(), nil)
	if err != nil || rep.Remote.MaintenanceRuns != 0 || rep.Remote.MaintenanceSkips["no bot lines"] != 2 {
		t.Fatalf("%v %+v", err, rep.Remote)
	}
}
