package dungeon

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/pjerra/turtle-agent/internal/packet"
	"github.com/pjerra/turtle-agent/internal/tbm"
)

// Ritual steps in remote mode (remote_ritual.go; TortoiseBots `cmd ritual`).

const goAltar = 130511

var altarAt = Point{30, 0, 0}

// ritualGame is an entryGame whose module also answers `cmd ritual entry:<id>@x,y,z <bot> <bot> ...` the way TortoiseBots does: ERR
// not-found / too-far for the object, no-bot for a name that is not in the party, not-enough for fewer bots than the altar needs, busy for
// an altar that is active already; otherwise the final `party|<n>|done` after the bots have walked and used it, with the altar then active.
// ritualAnswer is a scripted answer; fired says the altar had turned active by then (the ritual spell went out).
type ritualAnswer struct {
	res   tbm.OrderResult
	fired bool
}

type ritualGame struct {
	*entryGame
	need    int                 // participants the altar needs
	script  []ritualAnswer      // answers given to the first ritual orders, in order
	rituals []string            // every ritual order
	onDone  func(g *ritualGame) // after an order that succeeded
}

func newRitualGame(t *testing.T, need int) *ritualGame {
	g := &ritualGame{entryGame: newEntryGame(t, 3, true), need: need}
	g.world = []*worldObj{{guid: packet.GUID(0x1F1000000000BBB1), entry: goAltar, at: altarAt, state: "ready"}}
	for i, n := range []string{"Dpsy", "Rogy"} {
		g.party = append(g.party, &tbm.PartyRec{GUID: packet.GUID(0x4000 + i), Name: n, HP: 1000, MaxHP: 1000, Power: 1000, MaxPower: 1000, State: "n"})
	}
	return g
}

func (g *ritualGame) Order(ctx context.Context, command string) (tbm.OrderResult, error) {
	f := strings.Fields(command)
	if len(f) < 3 || f[1] != "ritual" {
		return g.entryGame.Order(ctx, command)
	}
	g.record(command)
	g.rituals = append(g.rituals, command)
	if len(g.script) > 0 {
		a := g.script[0]
		g.script = g.script[1:]
		a.res.Command = command
		if a.fired {
			g.world[0].state = "active" // the ritual's cast went out and the answer is not "done": the altar stays active until the bots move
		}
		g.advance(8_000_000_000)
		return a.res, nil
	}
	entry, pt, _ := entryFormOf(command)
	o, code := g.find(entry, pt, false)
	if code != "" {
		return errOrder(command, code), nil
	}
	seen := map[string]bool{}
	for _, name := range f[3:] {
		found := false
		for _, m := range g.party {
			found = found || strings.EqualFold(m.Name, name)
		}
		if !found || seen[strings.ToLower(name)] {
			return errOrder(command, "no-bot"), nil
		}
		seen[strings.ToLower(name)] = true
	}
	if len(f[3:]) < g.need {
		return errOrder(command, "not-enough"), nil
	}
	if o.state == "active" {
		return errOrder(command, "busy"), nil
	}
	g.advance(12_000_000_000) // the walk, three uses and the settle
	// The module lets the bots go with its final answer: their own movement ends the helper channel, a participant leaves the active
	// altar and the core sends it back to ready (GameObject::RemoveUniqueUse). So after `done` the altar reads ready.
	o.state = "ready"
	if g.onDone != nil {
		g.onDone(g)
	}
	return ritualAck(command, g.need), nil
}

func (g *ritualGame) lastIndexOf(prefix string) int {
	for i := len(g.orders) - 1; i >= 0; i-- {
		if strings.HasPrefix(g.orders[i], prefix) {
			return i
		}
	}
	return -1
}

func ritualAck(command string, n int) tbm.OrderResult {
	a := tbm.Ack{Intent: "cmd ritual", Scope: "party", Count: n, Executor: "done"}
	p := tbm.Pending{Intent: "cmd ritual", BudgetS: 40}
	return tbm.OrderResult{Command: command, Intent: "cmd ritual", Kind: tbm.KindAck, OK: true, Ack: &a, Pending: &p, Ritual: true, Participants: n,
		Final: fmt.Sprintf("TBM:ACTION_ACK|cmd ritual|party|%d|done", n), Lines: []string{}}
}

func ritualOpts() RemoteOptions {
	o := objOpts()
	o.Bots = []string{"Healy", "Magey", "Dpsy", "Rogy"}
	return o
}

func ritualRun(t *testing.T, g *ritualGame, plan *Plan) (Report, error) {
	t.Helper()
	g.agent = Point{-300, 0, 0}
	cfg := DefaultConfig()
	cfg.PostFight, cfg.AddsWait = 2_000_000_000, 4_000_000_000
	cfg.Setup = []string{"gm visible off"}
	d := NewRemoteDriver(plan, g, cfg, ritualOpts(), g.clock())
	err := d.Run(context.Background(), 0)
	rep, ended := d.Status()
	if !ended {
		t.Fatal("not ended")
	}
	return rep, err
}

func ritualPlan(participants int) *Plan {
	return remotePlan(Leg{Name: "altar", To: Point{20, 0, 0}, Steps: []Step{
		{Ritual: &Ritual{Entry: goAltar, Position: altarAt, Participants: participants}},
	}})
}

func TestRitualStepOrdersThreeBotsAndTheAckIsTheResult(t *testing.T) {
	g := newRitualGame(t, 3)
	rep, err := ritualRun(t, g, ritualPlan(3))
	if err != nil {
		t.Fatalf("%v\n%s", err, notesOf(rep))
	}
	// One order, by entry and point: the carrier (a DPS, not the lead, tank or healer) first, then two more DPS.
	want := "cmd ritual entry:130511@30.00,0.00,0.00 Magey Dpsy Rogy"
	if len(g.rituals) != 1 || g.rituals[0] != want {
		t.Fatalf("want %q, got %v", want, g.rituals)
	}
	// The party was held by the altar for it, and sent back to following after.
	ritual, state, follow := g.indexOf(want), g.lastIndexOf("cmd state entry:130511"), g.lastIndexOf("cmd follow")
	if g.indexOf("cmd stay") < 0 || g.indexOf("cmd stay") > ritual || follow < ritual {
		t.Fatalf("stay before, follow after: %v", g.orders)
	}
	// The ACK is the result: nothing asks for the altar's state afterwards (it reads ready again once the bots are let go: rule 6, the fake
	// does the same), so no wait on it can fail the run.
	if state > ritual || g.world[0].state != "ready" {
		t.Fatalf("the altar was polled after the ritual (state at %d, ritual at %d): %v", state, ritual, g.orders)
	}
	if rep.State != "done" {
		t.Fatalf("state %q\n%s", rep.State, notesOf(rep))
	}
	r := rep.Remote
	s := r.ObjectSteps[0]
	if s.Kind != "ritual" || s.Result != "done" || s.Carrier != "Magey" || fmt.Sprint(s.Participants) != "[Magey Dpsy Rogy]" || len(s.Orders) != 1 || s.Orders[0].Result != "ack" {
		t.Fatalf("%+v", s)
	}
	if r.Orders["ritual"] != 1 || r.OrdersRefused != 0 {
		t.Fatalf("%+v", r)
	}
	if len(g.appears) != 0 || r.RunGMAssists != 0 || r.GMCommands != 0 {
		t.Fatalf("the ritual needs no GM: appears %v %+v", g.appears, r)
	}
	if !hasNote(rep, "3 bots used the ritual together (Magey, Dpsy, Rogy)") {
		t.Fatalf("%s", notesOf(rep))
	}
}

// A plan whose next step waits for something that lasts (here a door the ritual's work opens, polled with cmd state) goes on after the ACK
// although the altar itself reads ready again.
func TestRitualStepThenAWaitOnWhatLastsGoesOn(t *testing.T) {
	g := newRitualGame(t, 3)
	doorAt := Point{40, 0, 0}
	g.world = append(g.world, &worldObj{guid: packet.GUID(0x1F1000000000BBB2), entry: 124367, at: doorAt, state: "ready"})
	g.onDone = func(g *ritualGame) { g.world[1].state = "active" } // the Keepers died: the door opened
	plan := ritualPlan(3)
	plan.Legs[0].Steps = append(plan.Legs[0].Steps, Step{WaitFor: &WaitFor{GameObject: &ObjectState{ObjectRef: ObjectRef{Entry: 124367, Near: &doorAt}, State: "active"}, TimeoutS: 20}})
	rep, err := ritualRun(t, g, plan)
	if err != nil || rep.State != "done" || len(g.rituals) != 1 || g.world[0].state != "ready" {
		t.Fatalf("%v %v\n%s", err, g.rituals, notesOf(rep))
	}
}

func TestRitualStepNeedsEnoughLivingBots(t *testing.T) {
	g := newRitualGame(t, 4)
	g.party = g.party[:3] // Dpsy and Rogy are not there: Tanky, Healy and Magey are left
	_, err := ritualRun(t, g, ritualPlan(4))
	if err == nil || !strings.Contains(err.Error(), "the ritual needs 4 bots, the party has 3 living") || len(g.rituals) != 0 {
		t.Fatalf("%v %v", err, g.rituals)
	}
}

// A refusal of the module stands, with its hint: not-enough is the plan naming fewer bots than the altar's template wants.
func TestRitualStepNotEnoughIsRefused(t *testing.T) {
	g := newRitualGame(t, 3)
	_, err := ritualRun(t, g, ritualPlan(2))
	if err == nil || !strings.Contains(err.Error(), "not-enough") || !strings.Contains(err.Error(), "more different bots") || len(g.rituals) != 1 {
		t.Fatalf("%v %v", err, g.rituals)
	}
}

// An interrupted ritual (a bot moved) before the altar turned active is ordered once more; the module has let every bot go. Twice interrupted
// ends the step: the order is never repeated without end.
func TestRitualStepInterruptedIsTriedOnceMore(t *testing.T) {
	g := newRitualGame(t, 3)
	g.script = []ritualAnswer{{res: errOrder("cmd ritual", "interrupted")}}
	rep, err := ritualRun(t, g, ritualPlan(3))
	if err != nil || len(g.rituals) != 2 || !hasNote(rep, "the ritual was interrupted") {
		t.Fatalf("%v %v\n%s", err, g.rituals, notesOf(rep))
	}
	g = newRitualGame(t, 3)
	g.script = []ritualAnswer{{res: errOrder("cmd ritual", "interrupted")}, {res: errOrder("cmd ritual", "interrupted")}, {res: errOrder("cmd ritual", "interrupted")}}
	rep, err = ritualRun(t, g, ritualPlan(3))
	if err == nil || !strings.Contains(err.Error(), "interrupted") || len(g.rituals) != 2 {
		t.Fatalf("%v %v", err, g.rituals)
	}
	if rep.Remote.ObjectSteps[0].Result != "failed" || rep.Remote.Refused["ritual:interrupted"] != 2 {
		t.Fatalf("%+v", rep.Remote)
	}
}

// A ritual that has fired is never sent again: `in-combat` or `interrupted` after the altar turned active (the Keepers woke into a fight, a
// bot moved after the cast went out) ends the step as done, with one order only, whatever the answer says.
func TestRitualStepIsNeverSentAgainAfterItFired(t *testing.T) {
	for _, code := range []string{"in-combat", "interrupted"} {
		g := newRitualGame(t, 3)
		g.script = []ritualAnswer{{res: errOrder("cmd ritual", code), fired: true}}
		rep, err := ritualRun(t, g, ritualPlan(3))
		if err != nil || len(g.rituals) != 1 || rep.State != "done" || !hasNote(rep, "the altar is active: the ritual's cast went out") {
			t.Fatalf("%s: %v %v\n%s", code, err, g.rituals, notesOf(rep))
		}
		if s := rep.Remote.ObjectSteps[0]; s.Result != "done" {
			t.Fatalf("%s: %+v", code, s)
		}
	}
}

// A fight before the altar fired: the order is refused, the fight is waited for (it is over at once here) and the order goes out again, at
// most ritualOrders orders in all; each refusal first asks the altar.
func TestRitualStepInCombatBeforeItFiredIsOrderedAgain(t *testing.T) {
	g := newRitualGame(t, 3)
	g.script = []ritualAnswer{{res: errOrder("cmd ritual", "in-combat")}, {res: errOrder("cmd ritual", "in-combat")}}
	rep, err := ritualRun(t, g, ritualPlan(3))
	if err != nil || len(g.rituals) != 3 || !hasNote(rep, "waiting for the fight to end") {
		t.Fatalf("%v %v\n%s", err, g.rituals, notesOf(rep))
	}
	// The altar was asked between the orders (and the probe at the start).
	if n := len(g.ordersOf("cmd state")); n < 3 {
		t.Fatalf("%v", g.orders)
	}
	g = newRitualGame(t, 3)
	g.script = []ritualAnswer{{res: errOrder("cmd ritual", "in-combat")}, {res: errOrder("cmd ritual", "in-combat")}, {res: errOrder("cmd ritual", "in-combat")}, {res: errOrder("cmd ritual", "in-combat")}}
	if _, err := ritualRun(t, g, ritualPlan(3)); err == nil || !strings.Contains(err.Error(), "in-combat") || len(g.rituals) != ritualOrders {
		t.Fatalf("%v %v", err, g.rituals)
	}
}

// An altar that is active already when the order is given is `busy`: retried a few times by the order loop, then the step ends.
func TestRitualStepBusyEnds(t *testing.T) {
	g := newRitualGame(t, 3)
	g.world[0].state = "active"
	_, err := ritualRun(t, g, ritualPlan(3))
	if err == nil || !strings.Contains(err.Error(), "busy") || len(g.rituals) != 1+objectBusyTries {
		t.Fatalf("%v %v", err, g.rituals)
	}
}

// Nothing of that entry near the point: not-found names the entry and the point, with no retry by GUID (a ritual has no GUID form here).
func TestRitualStepNotFound(t *testing.T) {
	g := newRitualGame(t, 3)
	g.world = nil
	_, err := ritualRun(t, g, ritualPlan(3))
	if err == nil || !strings.Contains(err.Error(), "not-found") || len(g.rituals) != 1 {
		t.Fatalf("%v %v", err, g.rituals)
	}
}

// A module that does not take objects by entry cannot be asked: the step says so before any order.
func TestRitualStepNeedsEntryTargets(t *testing.T) {
	g := newRitualGame(t, 3)
	g.modern = false
	_, err := ritualRun(t, g, ritualPlan(3))
	if err == nil || !strings.Contains(err.Error(), "does not take objects by entry") || len(g.rituals) != 0 {
		t.Fatalf("%v %v", err, g.rituals)
	}
}

func TestPickRitualBots(t *testing.T) {
	party := []ritualMember{
		{Name: "Tanky", Lead: true, Tank: true},
		{Name: "Healy", Healer: true},
		{Name: "Magey"},
		{Name: "Rogy"},
		{Name: "Warry", Tank: true},
	}
	cases := []struct {
		carrier string
		n       int
		dead    string
		want    string
		err     string
	}{
		{"Magey", 3, "", "[Magey Rogy Healy]", ""},                                      // DPS first, then the healer
		{"Rogy", 3, "", "[Rogy Magey Healy]", ""},                                       // the carrier leads whoever it is
		{"Magey", 5, "", "[Magey Rogy Healy Warry Tanky]", ""},                          // healer, tank, the lead bot last
		{"Magey", 2, "", "[Magey Rogy]", ""},                                            //
		{"Magey", 3, "Rogy", "[Magey Healy Warry]", ""},                                 // the dead are skipped
		{"Magey", 5, "Rogy", "", "the ritual needs 5 bots, the party has 4 living"},     //
		{"Nobody", 3, "", "", "the carrier Nobody is not a living bot of the party"},    //
		{"Magey", 3, "Magey", "", "the carrier Magey is not a living bot of the party"}, //
	}
	for _, c := range cases {
		p := append([]ritualMember(nil), party...)
		for i := range p {
			p[i].Dead = p[i].Name == c.dead
		}
		got, err := PickRitualBots(p, c.carrier, c.n)
		if c.err != "" {
			if err == nil || err.Error() != c.err {
				t.Errorf("%+v: err %v, want %q", c, err, c.err)
			}
			continue
		}
		if err != nil || fmt.Sprint(got) != c.want {
			t.Errorf("%+v: %v %v, want %s", c, got, err, c.want)
		}
	}
}

func TestParseRitualStep(t *testing.T) {
	plan := func(step string) string {
		return `{"version":1,"name":"t","map":1,"entrance":{"x":0,"y":0,"z":0},"legs":[{"to":{"x":0,"y":0,"z":0},"steps":[` + step + `]}]}`
	}
	for _, in := range []string{
		`{"ritual":{"entry":130511,"position":[104.8,272.5,-26.5],"participants":3}}`,
		`{"ritual":{"entry":130511,"position":{"x":104.8,"y":272.5,"z":-26.5},"participants":3,"stand":{"x":101.5,"y":271.2,"z":-26.5}}}`,
	} {
		p, err := Parse("t.json", []byte(plan(in)))
		if err != nil {
			t.Fatalf("%s: %v", in, err)
		}
		r := p.Legs[0].Steps[0].Ritual
		if r == nil || r.Entry != 130511 || r.Position != (Point{104.8, 272.5, -26.5}) || r.Participants != 3 {
			t.Fatalf("%s: %+v", in, r)
		}
	}
	for _, c := range []struct{ in, want string }{
		{`{"ritual":{"position":[1,2,3],"participants":3}}`, "ritual.entry: required"},
		{`{"ritual":{"entry":5,"participants":3}}`, "ritual.position: required"},
		{`{"ritual":{"entry":5,"position":[1,2],"participants":3}}`, "a position array is [x, y, z]"},
		{`{"ritual":{"entry":5,"position":[1,2,null],"participants":3}}`, "a position array is [x, y, z]"},
		{`{"ritual":{"entry":5,"position":[1,2,3]}}`, "ritual.participants: required"},
		{`{"ritual":{"entry":5,"position":[1,2,3],"participants":1}}`, "ritual.participants: 1 is outside 2-5"},
		{`{"ritual":{"entry":5,"position":[1,2,3],"participants":6}}`, "ritual.participants: 6 is outside 2-5"},
		{`{"ritual":{"entry":5,"position":[1,2,3],"participants":3,"bots":3}}`, "unknown field"},
		{`{"ritual":{"entry":5,"position":[1,2,3],"participants":3},"use_object":{"entry":6}}`, "more than one of"},
	} {
		if _, err := Parse("t.json", []byte(plan(c.in))); err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: %v, want %q", c.in, err, c.want)
		}
	}
}

// The Stepper returns a ritual step as one action with the entry, the point and the number of participants, and goes on after Complete.
func TestStepperRitualAction(t *testing.T) {
	p := remotePlan(Leg{To: Point{0, 0, 0}, Steps: []Step{{Ritual: &Ritual{Entry: goAltar, Position: altarAt, Participants: 3}}}},
		Leg{To: Point{5, 0, 0}, Steps: []Step{{WaitFor: &WaitFor{HostilesGone: &HostilesGone{Radius: 5}, TimeoutS: 5}}}})
	s := NewStepper(p, &fakeWorld{}, func() time.Time { return time.Unix(1_000_000, 0) })
	a := s.Next()
	if a.Kind != ActRitual || a.Entry != goAltar || a.EntryAt == nil || *a.EntryAt != altarAt || a.Participants != 3 || a.Step != 0 {
		t.Fatalf("%+v", a)
	}
	if again := s.Next(); again.Kind != ActRitual {
		t.Fatalf("not repeated: %+v", again)
	}
	s.Complete()
	if next := s.Next(); next.Kind == ActRitual || next.Kind == ActFailed {
		t.Fatalf("%+v", next)
	}
}

// Group mode cannot take a ritual step yet: the local driver says why instead of "unknown action".
func TestLocalDriverRefusesARitualStep(t *testing.T) {
	g := newFake()
	p := ritualPlan(3)
	p.Map = 70
	d := NewDriver(p, g, DefaultConfig(), g.clock())
	err := d.Run(context.Background(), 0)
	if err == nil || !strings.Contains(err.Error(), "remote mode only") {
		t.Fatalf("%v", err)
	}
}
