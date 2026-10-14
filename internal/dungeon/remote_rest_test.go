package dungeon

import (
	"context"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

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
	if pull.Result == "walking" || pull.Skipped || pull.Seconds < 15 || !slices.Equal(pull.Short, []string{"Healy mana 45%"}) {
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
