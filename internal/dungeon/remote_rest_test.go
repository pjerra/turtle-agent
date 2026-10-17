package dungeon

import (
	"context"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/pjerra/turtle-agent/internal/tbm"
	"github.com/pjerra/turtle-agent/internal/trace"
	"github.com/pjerra/turtle-agent/internal/tracecheck"
	"github.com/pjerra/turtle-agent/internal/tracerun"
)

// refillFake is a kitFake whose bags fill when `.bot maintenance <bot>` is sent a second time (the preflight sent the first): after
// names the inventory the bot has from then on.
type refillFake struct {
	*botFake
	after  map[string][]string
	second map[string][]string    // the reply to the second maintenance line, when it is not the usual
	times  map[string][]time.Time // when each line was sent
}

func (g *refillFake) BotLines(ctx context.Context, command string, wait time.Duration, done func(string) bool) ([]string, bool, error) {
	if g.times == nil {
		g.times = map[string][]time.Time{}
	}
	g.times[command] = append(g.times[command], g.now)
	if name, found := strings.CutPrefix(command, "maintenance "); found && len(g.times[command]) == 2 && g.second[name] != nil {
		g.replies[command] = g.second[name]
	}
	lines, ok, err := g.botFake.BotLines(ctx, command, wait, done)
	if name, found := strings.CutPrefix(command, "maintenance "); found {
		if inv, has := g.after[name]; has && len(g.times[command]) >= 2 {
			g.replies["inv "+name] = inv
		}
	}
	return lines, ok, err
}

func newRefillFake(t *testing.T, magey uint32) *refillFake {
	g := &refillFake{botFake: kitFake(t), after: map[string][]string{}, second: map[string][]string{}}
	water := drinkEntries[0]
	g.replies["inv Magey"] = invLines("Magey", nil, map[uint32]int{water: int(magey)})
	g.after["Magey"] = invLines("Magey", nil, map[uint32]int{water: 14})
	g.party[2].Power = 300 // 30%: short of the 35% target, not under HealerManaMin: let pass after ManaStall
	return g
}

func onePull(t *testing.T) (*Plan, func(g *fakeRemote)) {
	return remotePlan(Leg{Name: "a", To: Point{20, 0, 0}}), func(*fakeRemote) {}
}

// A caster with few drinks left gets `.bot maintenance` once in the leg at the gate, and its bags are read again; the refill is in the
// report, the trace and the summary.
func TestRemoteRefillLowDrinks(t *testing.T) {
	g := newRefillFake(t, 2)
	plan, _ := onePull(t)
	dir := t.TempDir()
	rep, err := runRemote(t, g, plan, kitOpts(), func(c *Config) { c.Trace = tracerun.Config{On: true, Dir: dir, RunID: "r-refill"} })
	if err != nil || rep.State != "done" {
		t.Fatalf("%v %v", err, rep.Notes)
	}
	rf := rep.Remote.Refills
	if len(rf) != 1 || rf[0].Bot != "Magey" || rf[0].Why != "low-drinks" || rf[0].Before != 2 || rf[0].After != 14 || rf[0].Maintenance != "maintained" || rf[0].Leg != 0 {
		t.Fatalf("%+v", rf)
	}
	// Once: the preflight's and the gate's, though the gate went on for 30 s with the mana not rising.
	if n := len(g.sent("maintenance Magey")); n != 2 {
		t.Fatalf("maintenance sent %d times: %v", n, g.lines)
	}
	if !slices.Equal(rep.RestGates[0].Refills, []string{"Magey"}) {
		t.Fatalf("%+v", rep.RestGates[0])
	}
	// The party is told to rest again after the refill.
	if n := len(g.ordersOf("cmd rest")); n < 2 {
		t.Fatalf("cmd rest sent %d times: %v", n, g.orders)
	}
	if !strings.Contains(notesOf(rep), "Magey refilled (low-drinks): drinks 2 -> 14, maintenance maintained") {
		t.Fatalf("%s", notesOf(rep))
	}
	f, err := trace.Read(filepath.Join(dir, "r-refill.jsonl"))
	if err != nil || f.End == nil || f.End.Counter("refills") != 1 || evNames(f)["refill"] != 1 {
		t.Fatalf("%v %v %+v", err, evNames(f), f.End)
	}
}

// Enough drinks and a caster that drinks: no refill, and none for a caster whose drink count is fine but whose mana does not rise
// until the drink nudge fires (15 s): then it is refilled once, at the nudge.
func TestRemoteRefillNudgeAndPlentyOfDrinks(t *testing.T) {
	g := newRefillFake(t, 12)
	plan, _ := onePull(t)
	rep, err := runRemote(t, g, plan, kitOpts(), nil)
	if err != nil || len(rep.Remote.Refills) != 1 || rep.Remote.Refills[0].Why != "nudge" || rep.Remote.Refills[0].Before != 12 {
		t.Fatalf("%v %+v", err, rep.Remote.Refills)
	}
	ts := g.times["maintenance Magey"]
	if len(ts) != 2 || ts[1].Sub(ts[0]) < 15*time.Second {
		t.Fatalf("the refill came %v after the preflight's maintenance", ts[len(ts)-1].Sub(ts[0]))
	}

	// Nobody short of mana: nothing is sent beyond the preflight's.
	g = newRefillFake(t, 2)
	g.party[2].Power = 1000
	rep, err = runRemote(t, g, plan, kitOpts(), nil)
	if err != nil || len(rep.Remote.Refills) != 0 || len(g.sent("maintenance")) != 3 {
		t.Fatalf("%v %+v %v", err, rep.Remote.Refills, g.lines)
	}

	// RefillBelow < 0 turns the refills off.
	g = newRefillFake(t, 2)
	rep, err = runRemote(t, g, plan, kitOpts(), func(c *Config) { c.RefillBelow = -1 })
	if err != nil || len(rep.Remote.Refills) != 0 || len(g.sent("maintenance")) != 3 {
		t.Fatalf("%v %+v %v", err, rep.Remote.Refills, g.lines)
	}

	// Maintenance switched off on the server: the preflight finds out and the gates send nothing more.
	g = newRefillFake(t, 2)
	g.replies["maintenance Magey"] = []string{"Owned-bot maintenance is disabled (AiPlayerbot.OwnedBotMaintenanceEnabled = 0)."}
	rep, err = runRemote(t, g, plan, kitOpts(), nil)
	if err != nil || len(rep.Remote.Refills) != 0 || len(g.sent("maintenance")) != 3 {
		t.Fatalf("%v %+v %v", err, rep.Remote.Refills, g.lines)
	}

	// Switched off after the preflight: the first refill finds out and is recorded, no other is tried.
	g = newRefillFake(t, 2)
	g.second["Magey"] = []string{"Owned-bot maintenance is disabled (AiPlayerbot.OwnedBotMaintenanceEnabled = 0)."}
	rep, err = runRemote(t, g, plan, kitOpts(), nil)
	if err != nil || len(rep.Remote.Refills) != 1 || rep.Remote.Refills[0].Maintenance != "disabled" || len(g.sent("maintenance")) != 4 {
		t.Fatalf("%v %+v %v", err, rep.Remote.Refills, g.lines)
	}
}

// The feed says a caster's last action was a drink that failed for want of one: it is refilled at once, with a count that looked fine.
// A drink that fails with no reason, twice, does the same.
func TestRemoteRefillOnDrinkNoItem(t *testing.T) {
	g := newRefillFake(t, 12)
	g.party[2].Action, g.party[2].Result, g.party[2].Reason = "drink", "F", "no-item"
	plan, _ := onePull(t)
	rep, err := runRemote(t, g, plan, kitOpts(), nil)
	if err != nil || len(rep.Remote.Refills) != 1 || rep.Remote.Refills[0].Why != "no-item" {
		t.Fatalf("%v %+v", err, rep.Remote.Refills)
	}
	ts := g.times["maintenance Magey"]
	if len(ts) != 2 || ts[1].Sub(ts[0]) >= 15*time.Second {
		t.Fatalf("the refill came %v after the preflight's maintenance: not at once", ts[len(ts)-1].Sub(ts[0]))
	}

	// A drink that fails with no reason (TortoiseBots reports item-action failures as `drink,F,-`) that is still failing in the next frame
	// counts as a bot that cannot drink: refilled within the poll after, long before the nudge.
	g = newRefillFake(t, 12)
	g.party[2].Action, g.party[2].Result, g.party[2].Reason = "drink", "F", ""
	rep, err = runRemote(t, g, plan, kitOpts(), nil)
	ts = g.times["maintenance Magey"]
	if err != nil || len(rep.Remote.Refills) != 1 || rep.Remote.Refills[0].Why != "drink-failed" || len(ts) != 2 || ts[1].Sub(ts[0]) >= 15*time.Second {
		t.Fatalf("%v %+v %v", err, rep.Remote.Refills, ts)
	}

	// One failed drink, gone in the next frame, is no cause; nor is another failed action, or a drink that went well.
	g = newRefillFake(t, 12)
	g.party[2].Action, g.party[2].Result = "drink", "F"
	g.onTick = func(g *fakeRemote) {
		if len(g.ordersOf("cmd rest")) > 0 {
			g.party[2].Action, g.party[2].Result = "eat", "O"
		}
	}
	rep, err = runRemote(t, g, plan, kitOpts(), func(c *Config) { c.DrinkNudge = time.Hour })
	if err != nil || len(rep.Remote.Refills) != 0 {
		t.Fatalf("a single failed drink: %v %+v", err, rep.Remote.Refills)
	}
	for _, c := range [][3]string{{"heal", "F", "no-item"}, {"heal", "F", ""}, {"drink", "O", ""}} {
		g = newRefillFake(t, 12)
		g.party[2].Action, g.party[2].Result, g.party[2].Reason = c[0], c[1], c[2]
		rep, err = runRemote(t, g, plan, kitOpts(), func(cfg *Config) { cfg.DrinkNudge = time.Hour })
		if err != nil || len(rep.Remote.Refills) != 0 {
			t.Fatalf("%v: %v %+v", c, err, rep.Remote.Refills)
		}
	}
}

// A game that cannot send `.bot` lines is not refilled, and says so once.
func TestRemoteRefillNeedsBotLines(t *testing.T) {
	g := newFakeRemote(t)
	g.party[2].Action, g.party[2].Result, g.party[2].Reason = "drink", "F", "no-item"
	g.party[2].Power = 400
	rep, err := runRemote(t, g, remotePlan(Leg{Name: "a", To: Point{20, 0, 0}}), opts(), nil)
	if err != nil || len(rep.Remote.Refills) != 0 || strings.Count(notesOf(rep), "refill: skipped") != 1 {
		t.Fatalf("%v %+v\n%s", err, rep.Remote.Refills, notesOf(rep))
	}
}

// A group that left everyone at the targets costs no gate time: every gate is skipped and counted, the summary carries rest_share, and
// the trace's rest_share check reads it.
func TestRemoteTrivialGatesAreSkipped(t *testing.T) {
	g := newFakeRemote(t)
	g.addMob(299, Point{150, 5, 0})
	plan := remotePlan(Leg{Name: "first", To: Point{100, 0, 0}, PullGroups: []PullGroup{{Entries: []uint32{299}, Positions: []Point{{150, 5, 0}}}}})
	dir := t.TempDir()
	rep, err := runRemote(t, g, plan, opts(), func(c *Config) { c.Trace = tracerun.Config{On: true, Dir: dir, RunID: "r-skip"} })
	if err != nil || rep.State != "done" {
		t.Fatalf("%v %v", err, rep.Notes)
	}
	if len(rep.RestGates) < 2 || rep.RestGatesSkipped != len(rep.RestGates) || rep.RestSeconds > 1 || rep.RestShare > 1 {
		t.Fatalf("%d skipped of %+v, rest %.1f s, share %.1f", rep.RestGatesSkipped, rep.RestGates, rep.RestSeconds, rep.RestShare)
	}
	for _, gate := range rep.RestGates {
		if !gate.Skipped || gate.Result != "rested" || gate.Seconds > 1 {
			t.Fatalf("%+v", gate)
		}
	}
	sum := summaryOf(rep)
	if sum["rest_gates_skipped"] != rep.RestGatesSkipped || sum["rest_share"] != rep.RestShare {
		t.Fatalf("%v", sum)
	}
	f, err := trace.Read(filepath.Join(dir, "r-skip.jsonl"))
	if err != nil || f.End.Counter("rest_gates_skipped") != float64(rep.RestGatesSkipped) {
		t.Fatalf("%v %+v", err, f.End)
	}
	var share *trace.Check
	for _, c := range tracecheck.Score(f, tracecheck.Defaults()) {
		if c.ID == tracecheck.IDRestShare {
			share = &c
		}
	}
	if share == nil || share.Verdict == trace.Fail {
		t.Fatalf("%+v", share)
	}
	// A gate the party waits in is not skipped: the healer short of mana holds it.
	g = newFakeRemote(t)
	g.party[1].Power = 290
	o := opts()
	o.Healer = "Healy"
	rep, _ = runRemote(t, g, remotePlan(Leg{Name: "a", To: Point{20, 0, 0}}), o, func(c *Config) { c.RestTimeout = 20 * time.Second })
	if rep.RestGatesSkipped != 0 || rep.RestGates[0].Skipped || rep.RestShare < 50 {
		t.Fatalf("%+v share %.1f", rep.RestGates, rep.RestShare)
	}
}

// Before a long walk the gate at the leg's start lets the party rest on the way when everyone is above the walking floors (health 70%,
// healer mana 40%); a short walk, a bot under a floor, or a plan that says so holds it as before.
func TestRemoteWalkingRest(t *testing.T) {
	o := opts()
	o.Healer = "Healy"
	long := func(rest *RestSpec) *Plan { return remotePlan(Leg{Name: "a", To: Point{100, 0, 0}, Rest: rest}) }
	run := func(name string, plan *Plan, mod func(g *fakeRemote)) RestGate {
		t.Helper()
		g := newFakeRemote(t)
		mod(g)
		rep, err := runRemote(t, g, plan, o, func(c *Config) { c.RestTimeout = 20 * time.Second })
		if err != nil || len(rep.RestGates) == 0 {
			t.Fatalf("%s: %v %+v", name, err, rep.RestGates)
		}
		if gate := rep.RestGates[0]; gate.Result == "walking" && (rep.RestWalkPasses != 1 || rep.RestGatesSkipped != 1) {
			t.Fatalf("%s: %d walk passes, %d skipped", name, rep.RestWalkPasses, rep.RestGatesSkipped)
		}
		return rep.RestGates[0]
	}
	healy45 := func(g *fakeRemote) { g.party[1].Power = 450 } // under the healer target (50), over the walking floor (40)
	healy35 := func(g *fakeRemote) { g.party[1].Power = 350 }
	hurt75 := func(g *fakeRemote) { g.party[0].HP = 750 } // under the health target (90), over the floor (70)

	for name, mod := range map[string]func(g *fakeRemote){"mana": healy45, "health": hurt75} {
		if gate := run(name, long(nil), mod); gate.Result != "walking" || !gate.Skipped || gate.Seconds > 1 || len(gate.Short) == 0 {
			t.Fatalf("%s: %+v", name, gate)
		}
	}
	// Under a floor, or a walk shorter than 60 yd: held (the fake's healing party rests the health back, the mana waits out the cap).
	if gate := run("under the floor", long(nil), healy35); gate.Result == "walking" || gate.Skipped || gate.Seconds < 10 {
		t.Fatalf("%+v", gate)
	}
	if gate := run("short walk", remotePlan(Leg{Name: "a", To: Point{30, 0, 0}}), healy45); gate.Result == "walking" || gate.Skipped || gate.Seconds < 10 {
		t.Fatalf("%+v", gate)
	}
	// The plan's say: -1 turns it off, a larger walk_yd moves the line, higher floors move the floors.
	for name, spec := range map[string]*RestSpec{"off": {WalkYd: -1}, "far": {WalkYd: 150}, "floor": {WalkHealerMana: 46}} {
		if gate := run(name, long(spec), healy45); gate.Result == "walking" || gate.Skipped || gate.Seconds < 10 {
			t.Fatalf("%s: %+v", name, gate)
		}
	}
	if gate := run("lower floor", long(&RestSpec{WalkHealerMana: 30}), healy35); gate.Result != "walking" {
		t.Fatalf("%+v", gate)
	}
	// A leg that does not begin with a walk holds its gate: a pull is next.
	pull := remotePlan(Leg{Name: "a", To: Point{0, 0, 0}, PullGroups: []PullGroup{{Entries: []uint32{299}, Positions: []Point{{150, 5, 0}}}}})
	g := newFakeRemote(t)
	g.addMob(299, Point{150, 5, 0})
	healy45(g)
	rep, err := runRemote(t, g, pull, o, func(c *Config) { c.RestTimeout = 20 * time.Second })
	if err != nil || rep.RestWalkPasses != 0 {
		t.Fatalf("%v %+v", err, rep.RestGates)
	}
}

// The walking pass is only the start gate's: the gate before the first pull of the same leg still waits for the targets.
func TestRemoteWalkingRestThenHeldPullGate(t *testing.T) {
	o := opts()
	o.Healer = "Healy"
	g := newFakeRemote(t)
	g.addMob(299, Point{150, 5, 0})
	g.party[1].Power = 450 // 45%: under the healer target (50), over the walking floor (40)
	plan := remotePlan(Leg{Name: "a", To: Point{100, 0, 0}, PullGroups: []PullGroup{{Entries: []uint32{299}, Positions: []Point{{150, 5, 0}}}}})
	rep, err := runRemote(t, g, plan, o, func(c *Config) { c.RestTimeout = 20 * time.Second })
	if err != nil || len(rep.RestGates) < 2 {
		t.Fatalf("%v %+v", err, rep.RestGates)
	}
	first, pull := rep.RestGates[0], rep.RestGates[1]
	if first.Result != "walking" || !first.Skipped || first.Seconds > 1 || rep.RestWalkPasses != 1 {
		t.Fatalf("%+v", first)
	}
	if pull.Result == "walking" || pull.Skipped || pull.Seconds < 15 || !strings.Contains(strings.Join(pull.Short, " "), "Healy") || !strings.Contains(strings.Join(pull.Short, " "), "45%") {
		t.Fatalf("the pull gate did not hold: %+v", pull)
	}
	if rep.RestSeconds < 15 {
		t.Fatalf("rest %.1f s", rep.RestSeconds)
	}
}

func TestPlanRestWalkValidation(t *testing.T) {
	mk := func(rest string) string {
		return `{"version":1,"name":"t","map":70,"entrance":{"x":0,"y":0,"z":0},"legs":[{"to":{"x":0,"y":0,"z":0},"rest":` + rest + `,"pull_groups":[{"entries":[1],"positions":[{"x":20,"y":0,"z":0}]}]}]}`
	}
	if p, err := Parse("t.json", []byte(mk(`{"walk_yd":-1,"walk_health":60,"walk_healer_mana":30,"walk_mana":20}`))); err != nil || p.Legs[0].Rest.WalkYd != -1 || p.Legs[0].Rest.WalkMana != 20 {
		t.Fatalf("%v", err)
	}
	for in, want := range map[string]string{`{"walk_yd":-2}`: "rest.walk_yd", `{"walk_yd":9000}`: "rest.walk_yd", `{"walk_health":101}`: "rest.walk_health", `{"walk_mana":-1}`: "rest.walk_mana"} {
		if _, err := Parse("t.json", []byte(mk(in))); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%s: %v, want %q", in, err, want)
		}
	}
}

// pullFloorRun runs one pull with the tank at tankHP/1000 (and the healer at healerPower/1000 mana), nobody regenerating but by regen,
// which sets the values over time. It returns the report, how long after the start the pull began and the tank's health (per mille) and
// the healer's mana at that moment.
func pullFloorRun(t *testing.T, tankHP, healerPower uint32, regen func(g *fakeRemote, since time.Duration)) (Report, time.Duration, uint32, uint32) {
	t.Helper()
	g := newFakeRemote(t)
	g.healRate = 0
	g.party[0].HP, g.party[1].Power = tankHP, healerPower
	g.addMob(299, Point{40, 0, 0})
	start := g.now
	var pulledAt time.Duration
	var tankAt, manaAt uint32
	g.onTick = func(g *fakeRemote) {
		since := g.now.Sub(start)
		if regen != nil {
			regen(g, since)
		}
		for _, m := range g.mobs {
			if m.pulled && pulledAt == 0 {
				pulledAt, tankAt, manaAt = since, g.party[0].HP, g.party[1].Power
			}
		}
	}
	plan := remotePlan(Leg{Name: "p", To: Point{0, 0, 0}, PullGroups: []PullGroup{{Entries: []uint32{299}, Positions: []Point{{40, 0, 0}}}}})
	o := objOpts()
	o.Healer = "Healy"
	rep, err := runRemote(t, g, plan, o, nil)
	if err != nil || rep.State != "done" {
		t.Fatalf("%v %s\n%s", err, rep.State, notesOf(rep))
	}
	return rep, pulledAt, tankAt, manaAt
}

// rfc-live-2: a pull must not begin with the tank hurt. The tank at 65 % (target 90, give-up health 50) used to pass the gate at its 60 s
// cap and pull; now the gate keeps waiting for the pull floor (80 %), up to RestPullExtra past the cap, and the pull begins once the tank is
// above it.
func TestRestGateWaitsPastTheCapForTheTankToReachThePullFloor(t *testing.T) {
	rep, at, tank, _ := pullFloorRun(t, 650, 1000, func(g *fakeRemote, since time.Duration) {
		if since >= 100*time.Second {
			g.party[0].HP = 850 // 85 %: under the 90 % target, over the 80 % floor
		}
	})
	if at < 100*time.Second || tank < 800 {
		t.Fatalf("the pull began at %v with the tank at %d/1000\n%s", at, tank, notesOf(rep))
	}
	if g := rep.RestGates[0]; !g.PullFloorWait || g.Seconds < 95 {
		t.Fatalf("gate %+v", g)
	}
	if !strings.Contains(notesOf(rep), "keeps waiting for the pull floors (Tanky health 65%)") {
		t.Fatalf("%s", notesOf(rep))
	}
}

// The wait is bounded: a tank that never rises past the floor holds the gate for the cap plus RestPullExtra, then the run goes on (health
// over rest_min_health_pct) as before.
func TestRestGatePullFloorWaitIsBounded(t *testing.T) {
	cfg := DefaultConfig()
	rep, at, _, _ := pullFloorRun(t, 650, 1000, nil)
	limit := cfg.RestTimeout + cfg.RestPullExtra
	if at < limit {
		t.Fatalf("the pull began at %v, before the cap and the extra wait (%v)\n%s", at, limit, notesOf(rep))
	}
	// The first gate holds for the floors the extra time; once that ran out, the later gates of the leg give up at the plain cap.
	first := rep.RestGates[0]
	if first.Result != "gave_up" || !first.PullFloorWait || first.Seconds < limit.Seconds()-1 || first.Seconds > limit.Seconds()+10 {
		t.Fatalf("first gate %+v: want %v", first, limit)
	}
	for i, g := range rep.RestGates[1:] {
		if g.PullFloorWait || g.Seconds > cfg.RestTimeout.Seconds()+10 {
			t.Fatalf("gate %d %+v: the later gates of the leg wait for the floors again", i+1, g)
		}
	}
	if !strings.Contains(notesOf(rep), "stops waiting for the pull floors after 90 s") {
		t.Fatalf("%s", notesOf(rep))
	}
}

// A healer under its mana floor whose mana does not rise is no longer let pass as "not drinking" at once: the gate waits for the floor
// (60 %) too, and passes when it is reached.
func TestRestGateWaitsForTheHealerManaFloorOfAStalledCaster(t *testing.T) {
	rep, at, _, mana := pullFloorRun(t, 1000, 550, func(g *fakeRemote, since time.Duration) {
		if since >= 70*time.Second {
			g.party[1].Power = 700
		}
	})
	if at < 70*time.Second || mana < 600 {
		t.Fatalf("the pull began at %v with the healer at %d/1000 mana\n%s", at, mana, notesOf(rep))
	}
	if g := rep.RestGates[0]; !g.PullFloorWait {
		t.Fatalf("gate %+v", g)
	}
}

// A leg whose own rest targets are lower keeps the lower floors: the pull floors never ask for more than the gate's target.
func TestRestPullFloorsAreHeldDownByTheLegsTargets(t *testing.T) {
	g := newFakeRemote(t)
	g.party[0].HP = 700
	d := NewRemoteDriver(remotePlan(Leg{Name: "a", To: Point{10, 0, 0}}), g, DefaultConfig(), objOpts(), g.clock())
	d.fr.Lead, d.fr.Party = gTank, []tbm.PartyRec{*g.party[0], *g.party[1]}
	if s := d.pullFloorShort(restTarget{health: 90, healerMana: 60}); len(s) != 1 || s[0] != "Tanky health 70%" {
		t.Fatalf("%v", s)
	}
	if s := d.pullFloorShort(restTarget{health: 65, healerMana: 60}); len(s) != 0 {
		t.Fatalf("%v", s)
	}
}
