package dungeon

import (
	"context"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/pjerra/turtle-agent/internal/packet"
)

const mageGUID packet.GUID = 0x13

// restFake is the fake party with a mage (class 8, mana) added: the healer is the priest.
func restFake() *fakeGame {
	g := newFake()
	g.add(201, 1, Point{20, 0, 0})
	g.party = append(g.party, Member{Name: "Mage", GUID: mageGUID, Online: true, Health: 400, MaxHealth: 400, PowerType: 0, Power: 400, MaxPower: 400})
	g.partyClass[mageGUID] = 8
	return g
}

func runRest(t *testing.T, g *fakeGame, plan string, cfg Config) *Driver {
	t.Helper()
	d := NewDriver(mustPlan(t, plan), g, cfg, g.clock())
	if err := d.Run(context.Background(), 0); err != nil {
		t.Fatalf("%v\n%+v", err, d.Report())
	}
	return d
}

// The healer is held to 50% mana, anyone else to 35%: a mage at 36% and a healer at 50% pass at once.
func TestRestGateTargetsHealerAndOthers(t *testing.T) {
	g := restFake()
	g.party[1].Power = 200 // healer 50%
	g.party[2].Power = 144 // mage 36%
	d := runRest(t, g, restPlan, Config{})
	for _, gate := range d.Report().RestGates {
		if gate.Seconds > 1 || gate.Result != "rested" || gate.DrinkOrderSent {
			t.Fatalf("%+v", gate)
		}
	}

	// A healer at 49% holds the gate (and drinks it to the target); a mage at 34% does too.
	for _, tc := range []struct {
		name  string
		which int
		power uint32
	}{{"Heal", 1, 196}, {"Mage", 2, 136}} {
		g = restFake()
		g.healOnRest = true
		g.party[tc.which].Power = tc.power
		d = runRest(t, g, restPlan, Config{})
		if rs := d.Report().RestSeconds; rs < 3 {
			t.Fatalf("%s: rest %.0f s %+v", tc.name, rs, d.Report().RestGates)
		}
	}
}

// A gate that waits for a healer whose mana stays at 29% ends at the 60 s cap (not 150), names the healer as short, and sent the
// healer one drink order of its own; the report totals the seconds.
func TestRestGateCapsAtSixtySecondsAndNudges(t *testing.T) {
	g := restFake()
	g.party[1].Power = 116 // 29%, below HealerManaMin: waited for, never let pass
	d := runRest(t, g, restPlan, Config{})
	rep := d.Report()
	if len(rep.RestGates) != 2 {
		t.Fatalf("%+v", rep.RestGates)
	}
	for _, gate := range rep.RestGates {
		if gate.Result != "gave_up" || gate.Seconds < 60 || gate.Seconds > 66 || gate.MaxS != 60 || !slices.Equal(gate.Short, []string{"Heal mana 29%"}) ||
			!gate.DrinkOrderSent || gate.DrinkOrderResult != "sent (unverified)" || !slices.Equal(gate.DrinkOrders, []string{"Heal"}) {
			t.Fatalf("%+v", gate)
		}
	}
	if n := g.count(".bot command Heal u drink"); n != 2 { // one per gate
		t.Fatalf("drink orders %d: %q", n, g.chats)
	}
	if rep.RestSeconds < 120 || rep.RestSeconds > 132 || rep.RestSeconds != rep.Legs[0].RestSeconds {
		t.Fatalf("rest %.0f legs %.0f", rep.RestSeconds, rep.Legs[0].RestSeconds)
	}
	if !strings.Contains(notes(d), "rest gate gave up after 60 s ([Heal mana 29%]); went on with at least 100% health") {
		t.Fatalf("%s", notes(d))
	}
}

// The drink order goes out only when the mana has not risen at all in DrinkNudge: not before, and not to a member that drinks.
func TestRestGateNudgeWaitsForNoRise(t *testing.T) {
	g := restFake()
	g.party[1].Power = 116
	var at time.Time
	g.onChat = func(g *fakeGame, text string) {
		if text == ".bot command Heal u drink" && at.IsZero() {
			at = g.now
		}
	}
	start := g.now
	runRest(t, g, restPlan, Config{})
	if at.IsZero() || at.Sub(start) < 15*time.Second || at.Sub(start) > 20*time.Second {
		t.Fatalf("order at %v after the start", at.Sub(start))
	}

	// A healer who drinks (mana rising each look) is never nudged.
	g = restFake()
	g.healOnRest = true
	g.party[1].Power = 100
	d := runRest(t, g, restPlan, Config{})
	if n := g.count(".bot command Heal u drink"); n != 0 {
		t.Fatalf("a member who drinks got %d orders", n)
	}
	for _, gate := range d.Report().RestGates {
		if gate.DrinkOrderSent {
			t.Fatalf("%+v", gate)
		}
	}

	// A rise (here two points in the first looks) restarts the window: the order comes 15 s after the last rise, not after the first look.
	g = restFake()
	g.party[1].Power = 116
	start, at = g.now, time.Time{}
	g.onChat = func(g *fakeGame, text string) {
		if text == ".bot command Heal u drink" && at.IsZero() {
			at = g.now
		}
	}
	g.onTick = func(g *fakeGame) {
		if g.resting && g.party[1].Power < 118 {
			g.party[1].Power++
		}
	}
	runRest(t, g, restPlan, Config{})
	if at.IsZero() || at.Sub(start) < 20*time.Second {
		t.Fatalf("order at %v: the rise at ~6 s should have restarted the window", at.Sub(start))
	}
}

// The party rest order is refused (TB #916): the members that are short are ordered one by one at once, not after DrinkNudge.
func TestRestGateFallsBackWhenTheRestOrderIsRefused(t *testing.T) {
	g := restFake()
	g.party[1].Power = 116
	g.party[0].Health = 700 // the tank is short on health too
	g.onChat = func(g *fakeGame, text string) {
		if text == ".bot action rest" {
			g.refusals = append(g.refusals, Refusal{Intent: "rest", Code: "failed", Message: "No scoped bot accepted the mature action."})
		}
	}
	start := g.now
	var at time.Time
	prev := g.onChat
	g.onChat = func(g *fakeGame, text string) {
		prev(g, text)
		if text == ".bot command Heal u drink" && at.IsZero() {
			at = g.now
		}
	}
	d := runRest(t, g, restPlan, Config{RestTimeout: 20 * time.Second})
	if at.IsZero() || at.Sub(start) > 10*time.Second {
		t.Fatalf("order at %v", at.Sub(start))
	}
	if g.count(".bot command Tank u food") == 0 {
		t.Fatalf("%q", g.chats)
	}
	gate := d.Report().RestGates[0]
	if !gate.PartyRestRefused || !gate.DrinkOrderSent || countOf(g.chats, ".bot command Heal u drink") != 2 { // once per gate
		t.Fatalf("%+v %q", gate, g.chats)
	}
}

// A leg's rest overrides the targets and the cap: max_s 20 ends the gate at 20 s, healer_mana 90 holds a healer at 80%.
func TestRestGatePlanOverrides(t *testing.T) {
	plan := `{"version":1,"name":"t","map":70,"entrance":{"x":0,"y":0,"z":0},"legs":[
	 {"to":{"x":0,"y":0,"z":0},"rest":{"healer_mana":90,"max_s":20},"pull_groups":[{"entries":[1],"positions":[{"x":20,"y":0,"z":0}]}]}]}`
	g := restFake()
	g.party[1].Power = 320 // 80%: above the default 50, below 90
	d := runRest(t, g, plan, Config{})
	for _, gate := range d.Report().RestGates {
		if gate.MaxS != 20 || gate.Result != "gave_up" || gate.Seconds < 20 || gate.Seconds > 26 {
			t.Fatalf("%+v", gate)
		}
	}

	// mana and health: a mage at 60% is short of a 70% target, a tank at 85% of a 95% target.
	plan = strings.Replace(plan, `"healer_mana":90,"max_s":20`, `"mana":70,"health":95,"max_s":10`, 1)
	g = restFake()
	g.party[2].Power = 240
	g.party[0].Health = 765
	d = runRest(t, g, plan, Config{})
	gate := d.Report().RestGates[0]
	if gate.MaxS != 10 || !slices.Contains(gate.Short, "Mage mana 60%") || !slices.Contains(gate.Short, "Tank health 85%") {
		t.Fatalf("%+v", gate)
	}
}

func TestPlanRestValidation(t *testing.T) {
	mk := func(rest string) string {
		return `{"version":1,"name":"t","map":70,"entrance":{"x":0,"y":0,"z":0},"legs":[{"to":{"x":0,"y":0,"z":0},"rest":` + rest + `,"pull_groups":[{"entries":[1],"positions":[{"x":20,"y":0,"z":0}]}]}]}`
	}
	p, err := Parse("t.json", []byte(mk(`{"health":80,"healer_mana":60,"mana":30,"max_s":45}`)))
	if err != nil || *p.Legs[0].Rest != (RestSpec{Health: 80, HealerMana: 60, Mana: 30, MaxS: 45}) {
		t.Fatalf("%v %+v", err, p)
	}
	for in, want := range map[string]string{
		`{"health":101}`: "rest.health", `{"mana":-1}`: "rest.mana", `{"healer_mana":150}`: "rest.healer_mana",
		`{"max_s":601}`: "rest.max_s", `{"mana_pct":30}`: "unknown field",
	} {
		if _, err := Parse("t.json", []byte(mk(in))); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%s: %v, want %q", in, err, want)
		}
	}
}

// Remote mode: the gate takes the same targets from the feed. The healer named in the options is held to 50%, the others to 35%;
// a member whose mana does not rise gets `cmd rest` once more after DrinkNudge; the cap is 60 s.
func TestRemoteRestGateTargetsAndNudge(t *testing.T) {
	plan := remotePlan(Leg{Name: "a", To: Point{20, 0, 0}})
	o := opts()
	o.Healer = "Healy"

	g := newFakeRemote(t)
	g.party[1].Power = 500 // Healy 50%: rested
	g.party[2].Power = 360 // Magey 36%: rested
	rep, err := runRemote(t, g, plan, o, nil)
	if err != nil || rep.RestGates[0].Seconds > 1 || rep.RestGates[0].Result != "rested" || len(g.ordersOf("cmd rest")) != 0 {
		t.Fatalf("%v %+v %v", err, rep.RestGates, g.orders)
	}

	// Healy at 29% is below HealerManaMin: the gate waits for her to its cap, here 40 s.
	g = newFakeRemote(t)
	g.party[1].Power = 290
	rep, err = runRemote(t, g, plan, o, func(c *Config) { c.RestTimeout = 40 * time.Second })
	gate := rep.RestGates[0]
	if err != nil || gate.Result != "gave_up" || gate.Seconds < 40 || gate.Seconds > 46 || !slices.Equal(gate.Short, []string{"Healy mana 29%"}) {
		t.Fatalf("%v %+v", err, gate)
	}

	// Magey at 34% is short of her 35% but is not drinking: she is let pass after ManaStall, with the gate marked "stalled".
	g = newFakeRemote(t)
	g.party[2].Power = 340
	rep, err = runRemote(t, g, plan, o, nil)
	gate = rep.RestGates[0]
	if err != nil || gate.Result != "stalled" || gate.Seconds < 30 || gate.Seconds > 36 || !gate.DrinkOrderSent {
		t.Fatalf("%v %+v", err, gate)
	}

	// No healer named: every caster is held to the healer target, so a mage at 40% is short.
	g = newFakeRemote(t)
	g.party[2].Power = 400
	rep, err = runRemote(t, g, plan, opts(), func(c *Config) { c.RestTimeout = 20 * time.Second })
	if err != nil || rep.RestGates[0].Result != "gave_up" || !slices.Equal(rep.RestGates[0].Short, []string{"Magey mana 40%"}) {
		t.Fatalf("%v %+v", err, rep.RestGates)
	}

	// The default cap is 60 s; the nudge is a second `cmd rest` at 15 s (the first went out at the start), recorded and counted once.
	g = newFakeRemote(t)
	g.party[1].Power = 290
	rep, err = runRemote(t, g, plan, o, nil)
	gate = rep.RestGates[0]
	if err != nil || gate.Seconds < 60 || gate.Seconds > 66 || gate.MaxS != 60 || !gate.DrinkOrderSent || !slices.Equal(gate.DrinkOrders, []string{"Healy"}) {
		t.Fatalf("%v %+v", err, gate)
	}
	if n := len(g.ordersOf("cmd rest")); n != 2 {
		t.Fatalf("cmd rest sent %d times: %v", n, g.orders)
	}
	if rep.RestSeconds < 60 || rep.RestSeconds != rep.Legs[0].RestSeconds {
		t.Fatalf("%v %v", rep.RestSeconds, rep.Legs[0].RestSeconds)
	}

	// A refused `cmd rest` is told apart in the gate's record.
	g = newFakeRemote(t)
	g.party[1].Power = 290
	g.cmdErr["rest"] = []string{"failed", "failed"}
	rep, _ = runRemote(t, g, plan, o, func(c *Config) { c.RestTimeout = 20 * time.Second })
	if !rep.RestGates[0].PartyRestRefused {
		t.Fatalf("%+v", rep.RestGates[0])
	}
}
