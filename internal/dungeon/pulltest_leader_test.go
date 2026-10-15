package dungeon

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

// leaderFake is the fake game with the optional halves the leader-mode pull test uses: a `.respawn` that brings the dead back, and the
// combat log's totals of the character.
type leaderFake struct {
	*fakeGame
	respawns        int
	damage, healing int64
}

func (g *leaderFake) Respawn(ctx context.Context) error {
	g.respawns++
	for _, u := range g.units {
		if u.Dead {
			u.Dead, u.Health, u.InCombat, u.Target = false, 100, false, 0
		}
	}
	return nil
}

func (g *leaderFake) CombatMark() uint64 { return 7 }
func (g *leaderFake) CombatSince(uint64) CombatTotals {
	return CombatTotals{Damage: g.damage, Healing: g.healing}
}

// statBrain is a fakeBrain whose counters the test scripts: each try reads the next one.
type statBrain struct {
	fakeBrain
	resets int
	tries  []CharacterStats
	read   int
}

func (b *statBrain) ResetStats() { b.resets++ }
func (b *statBrain) CharacterStats() CharacterStats {
	s := b.tries[min(b.read, len(b.tries)-1)]
	b.read++
	return s
}

const ptLeaderPlan = `{"version":1,"name":"t","map":70,"entrance":{"x":0,"y":0,"z":0},"legs":[
 {"name":"a","to":{"x":10,"y":0,"z":0},
  "pull_groups":[
   {"entries":[1],"positions":[{"x":30,"y":0,"z":0}],"equiv":"x"},
   {"entries":[2],"positions":[{"x":30,"y":40,"z":0}],"equiv":"x"},
   {"entries":[3],"positions":[{"x":30,"y":-40,"z":0}],"equiv":"x"}]},
 {"name":"b","to":{"x":95,"y":0,"z":0},"boss":{"entry":7,"position":{"x":90,"y":0,"z":0}}}]}`

func leaderRig(t *testing.T) (*leaderFake, *Plan) {
	t.Helper()
	g := newFake()
	g.pos = Point{0, 0, 0}
	g.add(101, 1, Point{30, 0, 0})
	g.add(102, 2, Point{30, 40, 0})
	g.add(103, 3, Point{30, -40, 0})
	g.add(107, 7, Point{90, 0, 0}) // the boss of leg b, alive
	return &leaderFake{fakeGame: g, damage: 1000, healing: 200}, mustPlan(t, ptLeaderPlan)
}

func scripted() *statBrain {
	return &statBrain{tries: []CharacterStats{
		{Actions: map[string]int{"frostbolt": 5, "frost_nova": 1}, CastFailed: map[string]int{"out_of_range": 2}, CombatS: 10, IdleCombatS: 2, MobOnMeS: 3, MobsOnMe: 1, PowerSpent: 500, MinPowerPct: 60},
		{Actions: map[string]int{"frostbolt": 6}, CastFailed: map[string]int{"out_of_range": 1, "no_power": 1}, CombatS: 10, IdleCombatS: 1, MobOnMeS: 0, MobsOnMe: 0, PowerSpent: 400, MinPowerPct: 55},
		{Actions: map[string]int{"frostbolt": 7}, CombatS: 10, IdleCombatS: 0, PowerSpent: 400, MinPowerPct: 50},
	}}
}

func runLeaderPulltest(t *testing.T, g *leaderFake, p *Plan, cfg Config, spec PulltestSpec) (*Driver, error) {
	t.Helper()
	cfg.Pulltest = &spec
	d := NewDriver(p, g, cfg, g.clock())
	return d, d.Run(context.Background(), spec.Leg)
}

func TestLeaderPulltestScoresEachTryAndTheCharacter(t *testing.T) {
	g, p := leaderRig(t)
	br := scripted()
	d, err := runLeaderPulltest(t, g, p, Config{Brain: br, BrainName: "frost_mage+rot:frost_mage@abc"}, PulltestSpec{Leg: 0, PullGroup: 0, Tries: 3})
	if err != nil {
		t.Fatalf("%v\n%+v", err, d.Report())
	}
	rep := d.Report()
	pr := rep.Pulltest
	if rep.State != "done" || pr == nil || pr.Mode != ModeLeader || pr.Brain != "frost_mage+rot:frost_mage@abc" || pr.Character != "Lead" || len(pr.Tries) != 3 || pr.Stopped != "" {
		t.Fatalf("%+v\n%+v", rep, pr)
	}
	wantReset := []string{"first spawn", "equivalent pack", "equivalent pack"}
	for i, tr := range pr.Tries {
		if tr.Outcome != TryKilled || tr.Kills != 1 || tr.PullGroup != i || tr.Reset != wantReset[i] || tr.TimeToKillS <= 0 || tr.Deaths != 0 || tr.Leader == nil {
			t.Fatalf("try %d: %+v", i+1, tr)
		}
	}
	l := pr.Tries[0].Leader
	if l.DamageDone != 1000 || l.HealingDone != 200 || l.DPS != round1(1000/pr.Tries[0].TimeToKillS) || l.Actions != 6 || l.CastsFailed != 2 || l.CastsFailedReasons["out_of_range"] != 2 ||
		l.IdlePct != 20 || l.MobsOnMe != 1 || l.MobOnMeS != 3 || l.PowerSpent != 500 || l.DamagePerPower != 2 || l.HealingPerPower != 0.4 || l.MinPowerPct != 60 {
		t.Fatalf("%+v", l)
	}
	s := pr.Summary.Leader
	if s == nil || s.Tries != 3 || s.CastsFailed != 4 || s.CastsFailedByReason["out_of_range"] != 3 || s.CastsFailedByReason["no_power"] != 1 || s.MobsOnMe != 1 || s.MinPowerPct != 50 || s.CastsFailedPerTry != 1.3 {
		t.Fatalf("%+v", s)
	}
	if br.resets != 3 || g.respawns != 0 || len(pr.GMCommands) != 0 || len(rep.Shortcuts) != 0 {
		t.Fatalf("resets %d respawns %d %v %v", br.resets, g.respawns, pr.GMCommands, rep.Shortcuts)
	}
	// The party rested before each pull and after each kill; the brain ran for the leg.
	if rep.Legs[0].RestSeconds < 0 || strings.Count(strings.Join(br.log, ","), "start") != 1 {
		t.Fatalf("%v", br.log)
	}
	// The result file round trips through the CLI's reader, and prints.
	b, _ := json.Marshal(pr)
	back, err := LoadPulltest(b)
	if err != nil || back.Summary.Leader == nil || back.Tries[2].Leader.Actions != 7 {
		t.Fatalf("%v %+v", err, back)
	}
	out := FormatPulltest(back)
	if !strings.Contains(out, "leader Lead  brain frost_mage+rot:frost_mage@abc") || !strings.Contains(out, "me: dps ") || !strings.Contains(out, "failed 2 (out_of_range 2)") || !strings.Contains(out, "\nme   tries 3") {
		t.Fatalf("%s", out)
	}
}

func TestLeaderPulltestRespawnsAPackThatHasNoEquivalent(t *testing.T) {
	g, p := leaderRig(t)
	p.Legs[0].PullGroups[0].Equiv = "" // no equivalent pack: the reset is the leader's `.respawn`
	br := scripted()
	d, err := runLeaderPulltest(t, g, p, Config{Brain: br}, PulltestSpec{Leg: 0, PullGroup: 0, Tries: 2})
	if err != nil {
		t.Fatalf("%v", err)
	}
	pr := d.Report().Pulltest
	if len(pr.Tries) != 2 || pr.Tries[1].Reset != "respawn" || pr.Tries[1].PullGroup != 0 || pr.Tries[1].Outcome != TryKilled || g.respawns != 1 {
		t.Fatalf("%+v respawns %d", pr, g.respawns)
	}
	rep := d.Report()
	if len(pr.GMCommands) != 1 || pr.GMCommands[0] != ".respawn" || len(rep.Shortcuts) != 1 || rep.Shortcuts[0].Kind != "assist" {
		t.Fatalf("%v %+v", pr.GMCommands, rep.Shortcuts)
	}
}

func TestLeaderPulltestStopsWhenTheResetWouldBeUnsafe(t *testing.T) {
	// reset none: one try.
	g, p := leaderRig(t)
	d, err := runLeaderPulltest(t, g, p, Config{Brain: scripted()}, PulltestSpec{Leg: 0, PullGroup: 0, Tries: 3, Reset: ResetNone})
	if pr := d.Report().Pulltest; err != nil || len(pr.Tries) != 1 || !strings.Contains(pr.Stopped, "reset is none") {
		t.Fatalf("%v %+v", err, pr)
	}
	// A player's account cannot respawn.
	g, p = leaderRig(t)
	p.Legs[0].PullGroups[0].Equiv = ""
	d, err = runLeaderPulltest(t, g, p, Config{Brain: scripted(), GM: GMNone}, PulltestSpec{Leg: 0, PullGroup: 0, Tries: 3})
	if pr := d.Report().Pulltest; err != nil || len(pr.Tries) != 1 || !strings.Contains(pr.Stopped, "needs account level 3") || g.respawns != 0 {
		t.Fatalf("%v %+v", err, pr)
	}
	// A boss of the plan within range and not seen alive would come back with the pack.
	g, p = leaderRig(t)
	p.Legs[0].PullGroups[0].Equiv = ""
	g.units[3].Dead, g.units[3].Health = true, 0
	d, err = runLeaderPulltest(t, g, p, Config{Brain: scripted()}, PulltestSpec{Leg: 0, PullGroup: 0, Tries: 3})
	if pr := d.Report().Pulltest; err != nil || len(pr.Tries) != 1 || !strings.Contains(pr.Stopped, "respawn would revive boss 7 of leg 1") || g.respawns != 0 {
		t.Fatalf("%v %+v", err, pr)
	}
	// A game that cannot respawn.
	g2, p2 := leaderRig(t)
	p2.Legs[0].PullGroups[0].Equiv = ""
	d2 := NewDriver(p2, g2.fakeGame, Config{Brain: scripted(), Pulltest: &PulltestSpec{Leg: 0, PullGroup: 0, Tries: 2}}, g2.clock())
	if err := d2.Run(context.Background(), 0); err != nil || !strings.Contains(d2.Report().Pulltest.Stopped, "cannot send .respawn") {
		t.Fatalf("%v %+v", err, d2.Report().Pulltest)
	}
}

func TestLeaderPulltestRefusesWhatItCannotRun(t *testing.T) {
	g, p := leaderRig(t)
	if _, err := runLeaderPulltest(t, g, p, Config{}, PulltestSpec{Leg: 0, PullGroup: 9}); err == nil || !strings.Contains(err.Error(), "pulltest.pull_group") {
		t.Fatalf("%v", err)
	}
	g, p = leaderRig(t)
	g.mapID = 1
	if _, err := runLeaderPulltest(t, g, p, Config{}, PulltestSpec{Leg: 0, PullGroup: 0}); err == nil || !strings.Contains(err.Error(), "stand in the instance") {
		t.Fatalf("%v", err)
	}
	g, p = leaderRig(t)
	g.party = nil
	if _, err := runLeaderPulltest(t, g, p, Config{}, PulltestSpec{Leg: 0, PullGroup: 0}); err == nil || !strings.Contains(err.Error(), "no party") {
		t.Fatalf("%v", err)
	}
}

func TestLeaderPulltestWithoutABrainSaysSoAndStillScoresTheParty(t *testing.T) {
	g, p := leaderRig(t)
	d, err := runLeaderPulltest(t, g, p, Config{}, PulltestSpec{Leg: 0, PullGroup: 0, Tries: 1})
	if err != nil {
		t.Fatal(err)
	}
	pr := d.Report().Pulltest
	if len(pr.Tries) != 1 || pr.Tries[0].Outcome != TryKilled || pr.Tries[0].Leader == nil || pr.Tries[0].Leader.DamageDone != 1000 || pr.Tries[0].Leader.Actions != 0 {
		t.Fatalf("%+v", pr)
	}
	noted := strings.Join(d.Report().Notes, "|")
	if !strings.Contains(noted, "no brain plays the character") {
		t.Fatalf("%s", noted)
	}
}

func TestLeaderPulltestWipeEndsTheScenario(t *testing.T) {
	g, p := leaderRig(t)
	g.dieIn = 30 * time.Second
	g.onTick = func(g *fakeGame) {
		if len(g.targets) > 0 && g.units[0].InCombat && g.now.After(time.Unix(1_000_000, 0).Add(5*time.Second)) {
			g.self.Health = 0
			g.party[0].Dead, g.party[1].Dead = true, true
		}
	}
	d, err := runLeaderPulltest(t, g, p, Config{Brain: scripted()}, PulltestSpec{Leg: 0, PullGroup: 0, Tries: 3})
	pr := d.Report().Pulltest
	if err == nil && (pr == nil || len(pr.Tries) != 1 || pr.Tries[0].Outcome != TryWipe) {
		t.Fatalf("%v %+v", err, pr)
	}
	if err != nil && !strings.Contains(err.Error(), "wipe") {
		t.Fatalf("%v", err)
	}
}

// ---- the compare ----

func leaderResult(brain string, dps, perPower, idle float64, failed int, reasons map[string]int) *PulltestReport {
	r := &PulltestReport{Version: PulltestVersion, Plan: "t", Leg: 0, PullGroup: 0, Entries: []uint32{1}, TriesAsked: 3, Reset: "auto", Mode: ModeLeader, Brain: brain, Character: "Lead"}
	for i := 1; i <= 3; i++ {
		r.Tries = append(r.Tries, PulltestTry{Try: i, Outcome: TryKilled, Kills: 1, TimeToKillS: 10, HealerMinManaPct: -1,
			Leader: &LeaderTry{DPS: dps, DamagePerPower: perPower, IdlePct: idle, CastsFailed: failed, CastsFailedReasons: reasons, Actions: 8, CombatS: 10, MinPowerPct: 40}})
	}
	r.Summarize()
	return r
}

func TestComparePulltestScoresTheCharacter(t *testing.T) {
	a := leaderResult("frost_mage", 100, 2, 30, 2, map[string]int{"out_of_range": 2})
	b := leaderResult("frost_mage+rot:frost_mage@abc", 120, 2.5, 20, 1, map[string]int{"no_power": 1})
	out := ComparePulltest(a, b)
	for _, want := range []string{"A plays: frost_mage (Lead)", "B plays: frost_mage+rot:frost_mage@abc (Lead)", "the character:", "damage per s, mean", "better", "damage per power",
		"idle in combat, mean %", "out_of_range per try", "no_power per try", "casts failed per try"} {
		if !strings.Contains(out, want) {
			t.Errorf("no %q in\n%s", want, out)
		}
	}
	if strings.Contains(out, "WARNING") {
		t.Errorf("a clean A/B warns:\n%s", out)
	}
	for _, l := range strings.Split(out, "\n") {
		if strings.Contains(l, "damage per s, mean") && !strings.Contains(l, "better") {
			t.Errorf("more damage per second is better: %q", l)
		}
		if strings.Contains(l, "idle in combat") && !strings.Contains(l, "better") {
			t.Errorf("less idle is better: %q", l)
		}
	}
	if o := ComparePulltest(a, a); !strings.Contains(o, "the noise") {
		t.Errorf("one arm against itself says so:\n%s", o)
	}
	// A commander result on one side.
	c := leaderResult("", 0, 0, 0, 0, nil)
	c.Mode, c.Tries[0].Leader = "", nil
	if o := ComparePulltest(a, c); !strings.Contains(o, "WARNING: one result is a leader run") {
		t.Errorf("%s", o)
	}
}
