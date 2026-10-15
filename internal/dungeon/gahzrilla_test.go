package dungeon

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/pjerra/turtle-agent/internal/objects"
	"github.com/pjerra/turtle-agent/internal/packet"
	"github.com/pjerra/turtle-agent/internal/tbm"
)

// The gahzrilla leg's machinery (PR A, TortoiseBots #984): an optional leg that needs an item, the give_items run option that puts the item in
// a bot's bags with a GM `.additem`, and the gong step that a bot carrying the item does.

const (
	itMallet = 9240
	goGong   = 141832
	npcGahz  = 7273
)

var gongGUID = packet.GUID(0x1F1000000000AAB1)

var malletRef = &ItemRef{Entry: itMallet, Name: "Mallet of Zul'Farrak"}

// gongLeg is a small copy of the gahzrilla leg: a pack, the gong, the wait for the boss, the boss.
func gongLeg() Leg {
	return Leg{
		Name: "gahzrilla", To: Point{20, 0, 0},
		PullGroups: []PullGroup{{Entries: []uint32{299}, Positions: []Point{{40, 5, 0}}}},
		Optional:   &OptionalLeg{Item: itMallet, Name: "Mallet of Zul'Farrak"},
		Steps: []Step{
			{UseObject: &UseObject{ObjectRef: ObjectRef{Entry: goGong, Near: &Point{20, 0, 0}}, Stand: &Point{20, 0, 0}, RequiresItem: malletRef}},
			{WaitFor: &WaitFor{Unit: &UnitWait{Entries: []uint32{npcGahz}, State: UnitSeen}, TimeoutS: 30}},
		},
		Boss: &Boss{Entry: npcGahz, Position: Point{60, 0, 0}},
	}
}

// gongGame is a carrier game with the gong at the leg's stand and a pack; ringing it (the first `cmd open`) puts Gahz'rilla in view.
func gongGame(t *testing.T) *fakeCarrier {
	g := newFakeCarrier(t)
	g.gobjs = []objects.GameObject{{GUID: gongGUID, Entry: goGong, X: 20, Y: 0, Z: 0, Distance: 3, State: "ready"}}
	g.addMob(299, Point{40, 5, 0})
	rung := false
	g.fakeRemote.onTick = func(*fakeRemote) {
		if !rung && len(g.ordersOf("cmd open")) > 0 {
			rung = true
			g.addMob(npcGahz, Point{60, 0, 0})
		}
	}
	return g
}

func gongPlan() *Plan {
	return remotePlan(gongLeg(), Leg{Name: "after", To: Point{100, 0, 0}})
}

// The gong is rung by the bot that carries the Mallet, never by the agent, after the packs and before the boss: pack, gong, wait, boss.
func TestGongLegOrderAndHolder(t *testing.T) {
	for _, tc := range []struct{ holder, other string }{{"Tanky", "Magey"}, {"Magey", "Tanky"}} { // the lead bot, then the carrier
		g := gongGame(t)
		g.bag(tc.holder)[itMallet] = 1
		rep, err := runCarrier(t, g, gongPlan(), objOpts())
		if err != nil || rep.State != "done" {
			t.Fatalf("%s holds it: %v\n%s", tc.holder, err, notesOf(rep))
		}
		open := g.indexOf("cmd open " + tbm.GUIDArg(gongGUID) + " " + tc.holder)
		pull, atk := g.indexOf("cmd pullback"), -1
		for i, o := range g.orders {
			if strings.HasPrefix(o, "cmd attack") && i > open {
				atk = i
				break
			}
		}
		if pull < 0 || open < pull || atk < open || len(g.ordersOf("cmd open")) != 1 {
			t.Fatalf("%s: pack %d, gong %d, boss attack %d: %v", tc.holder, pull, open, atk, g.orders)
		}
		if strings.Contains(strings.Join(g.orders, "\n"), "cmd open "+tbm.GUIDArg(gongGUID)+" "+tc.other) || rep.LeaderGM || len(rep.Shortcuts) != 0 || rep.Remote.GMCommands != 0 {
			t.Fatalf("the other bot rang the gong, or a GM did: %v %+v", g.orders, rep.Shortcuts)
		}
		if len(rep.Legs) != 2 || rep.Legs[0].Result != "done" || rep.Legs[1].Result != "done" {
			t.Fatalf("legs %+v", rep.Legs)
		}
		for _, m := range g.mobs {
			if m.entry == npcGahz && m.hp > 0 {
				t.Fatalf("%s: Gahz'rilla was not killed", tc.holder)
			}
		}
	}
}

// Nobody carries the Mallet: the leg is skipped as a whole with the reason in its result, the run goes on and does not fail, and no pack is
// pulled, no gong rung.
func TestOptionalLegIsSkippedWithoutTheMallet(t *testing.T) {
	g := gongGame(t)
	rep, err := runCarrier(t, g, gongPlan(), objOpts())
	if err != nil || rep.State != "done" || rep.Error != "" {
		t.Fatalf("a skipped optional leg is no failure: %v %s %s", err, rep.State, rep.Error)
	}
	if len(rep.Legs) != 2 || rep.Legs[1].Name != "after" || rep.Legs[1].Result != "done" {
		t.Fatalf("legs %+v", rep.Legs)
	}
	if got := rep.Legs[0].Result; !strings.HasPrefix(got, "skipped: no Mallet of Zul'Farrak (item 9240) in the bags of Magey or Tanky") {
		t.Fatalf("result %q", got)
	}
	if g.indexOf("cmd pullback") >= 0 || g.indexOf("cmd open") >= 0 || g.indexOf("cmd attack") >= 0 {
		t.Fatalf("the skipped leg did something: %v", g.orders)
	}
	noted := false
	for _, n := range rep.Notes {
		noted = noted || strings.Contains(n, "leg 0 (gahzrilla): skipped: no Mallet")
	}
	if !noted {
		t.Fatalf("no note: %v", rep.Notes)
	}
	if rep.Legs[0].Seconds != 0 || rep.Legs[0].Pulls != 0 {
		t.Fatalf("%+v", rep.Legs[0])
	}
}

// A bag that cannot be read is not "no Mallet": the run stops with the read's own error instead of skipping the leg on a guess.
func TestOptionalLegDoesNotSkipOnAFailedBagRead(t *testing.T) {
	g := gongGame(t)
	g.invErr = true
	_, err := runCarrier(t, g, gongPlan(), objOpts())
	if err == nil || !strings.Contains(err.Error(), ".bot inv") || g.indexOf("cmd pullback") >= 0 {
		t.Fatalf("%v %v", err, g.orders)
	}
}

// A leg that is not optional keeps failing for the item it lacks, naming it.
func TestNonOptionalLegStillFailsWithoutItsItem(t *testing.T) {
	g := gongGame(t)
	leg := gongLeg()
	leg.Optional = nil
	_, err := runCarrier(t, g, remotePlan(leg), objOpts())
	if err == nil || !strings.Contains(err.Error(), "does not carry Mallet of Zul'Farrak (item 9240)") {
		t.Fatalf("%v", err)
	}
}

// The plan format: optional {item, name} reads, writes back, and must name an item a use_object step of the leg requires.
func TestPlanOptionalLeg(t *testing.T) {
	const src = `{"version":1,"name":"t","map":209,"entrance":{"x":0,"y":0,"z":0},"legs":[
 {"name":"gong","to":{"x":20,"y":0,"z":0},"optional":{"item":9240,"name":"Mallet of Zul'Farrak"},
  "steps":[{"use_object":{"entry":141832,"requires_item":{"entry":9240,"name":"Mallet of Zul'Farrak"}}},{"wait_for":{"unit":{"entries":[7273],"state":"seen"},"timeout_s":30}}],
  "boss":{"entry":7273,"position":{"x":60,"y":0,"z":0}}}]}`
	p := mustPlan(t, src)
	if o := p.Legs[0].Optional; o == nil || o.Item != 9240 || o.Name != "Mallet of Zul'Farrak" || o.Ref().String() != "Mallet of Zul'Farrak (item 9240)" {
		t.Fatalf("%+v", o)
	}
	b, err := Marshal(p)
	if err != nil || !strings.Contains(string(b), `"optional": {"item": 9240, "name": "Mallet of Zul'Farrak"}`) {
		t.Fatalf("%v\n%s", err, b)
	}
	if q, err := Parse("t.json", b); err != nil || q.Legs[0].Optional == nil || q.Legs[0].Optional.Item != 9240 {
		t.Fatalf("%v", err)
	}
	for _, c := range []struct{ from, to, want string }{
		{`"optional":{"item":9240,"name":"Mallet of Zul'Farrak"}`, `"optional":{"item":0}`, "legs[0].optional.item: required"},
		{`"optional":{"item":9240,"name":"Mallet of Zul'Farrak"}`, `"optional":{"item":1234}`, "legs[0].optional.item: 1234 is required by no use_object step"},
		{`"optional":{"item":9240,"name":"Mallet of Zul'Farrak"}`, `"optional":{"item":9240,"name":"x","color":1}`, "color"},
	} {
		if _, err := Parse("t.json", []byte(strings.Replace(src, c.from, c.to, 1))); err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: %v", c.to, err)
		}
	}
}

// The run-start checks leave an optional leg out: a start past the leg that makes the item, and the leader-mode check of the bags, do not fail
// for a leg that is skipped when the item is missing.
func TestOptionalLegIsLeftOutOfTheStartChecks(t *testing.T) {
	const src = `{"version":1,"name":"t","map":209,"entrance":{"x":0,"y":0,"z":0},"legs":[
 {"name":"a","to":{"x":10,"y":0,"z":0},"boss":{"entry":7,"position":{"x":20,"y":0,"z":0},"loot_item":{"entry":9240,"name":"Mallet of Zul'Farrak"}}},
 {"name":"gong","to":{"x":30,"y":0,"z":0},"steps":[{"use_object":{"entry":141832,"requires_item":{"entry":9240,"name":"Mallet of Zul'Farrak"}}}]}]}`
	p := mustPlan(t, src)
	if err := p.SkippedNeeds(1, nil); err == nil || !strings.Contains(err.Error(), "Mallet of Zul'Farrak (item 9240) from skipped leg 0") {
		t.Fatalf("a plain leg needs it: %v", err)
	}
	opt := mustPlan(t, strings.Replace(src, `"name":"gong","to":{"x":30,"y":0,"z":0},`, `"name":"gong","to":{"x":30,"y":0,"z":0},"optional":{"item":9240,"name":"Mallet of Zul'Farrak"},`, 1))
	if err := opt.SkippedNeeds(1, nil); err != nil {
		t.Fatalf("an optional leg is skipped, not failed: %v", err)
	}
	// Leader mode: the bags are known and hold no Mallet.
	g, _ := runFixture(t)
	g.bag = []objects.Item{{Entry: 6948, Count: 1}}
	if err := NewDriver(p, g, Config{GM: GMNone}, g.clock()).checkRequiredItems(1); err == nil || !strings.Contains(err.Error(), "does not carry Mallet of Zul'Farrak") {
		t.Fatalf("%v", err)
	}
	if err := NewDriver(opt, g, Config{GM: GMNone}, g.clock()).checkRequiredItems(1); err != nil {
		t.Fatalf("%v", err)
	}
}

// The group driver skips an optional leg too, when the leader's bags are known and lack the item; with the item the leg runs.
func TestGroupDriverSkipsAnOptionalLeg(t *testing.T) {
	src := strings.Replace(runPlanKey, `{"name":"a","to":{"x":10,"y":0,"z":0},`, `{"name":"a","to":{"x":10,"y":0,"z":0},"optional":{"item":7733,"name":"Staff of Prehistoria"},`, 1)
	g, _ := runFixture(t)
	g.bag = []objects.Item{{Entry: 6948, Count: 1}}
	d := NewDriver(mustPlan(t, src), g, Config{GM: GMNone}, g.clock())
	if err := d.Run(context.Background(), 0); err != nil {
		t.Fatalf("%v\n%+v", err, d.Report())
	}
	rep := d.Report()
	if rep.State != "done" || len(rep.Legs) != 2 || !strings.HasPrefix(rep.Legs[0].Result, "skipped: no Staff of Prehistoria (item 7733) in the leader's bags") || rep.Legs[1].Result != "done" || len(g.used) != 0 {
		t.Fatalf("%+v used %v", rep.Legs, g.used)
	}
	g, _ = runFixture(t)
	g.bag = []objects.Item{{Entry: 6948, Count: 1}, staff()}
	d = NewDriver(mustPlan(t, src), g, Config{GM: GMNone}, g.clock())
	if err := d.Run(context.Background(), 0); err != nil {
		t.Fatalf("%v\n%+v", err, d.Report())
	}
	if rep := d.Report(); len(rep.Legs) != 2 || rep.Legs[0].Result != "done" || len(g.used) != 1 {
		t.Fatalf("%+v used %v", rep.Legs, g.used)
	}
}

// ---- give_items ----

// giveFake is a prepare fake whose bots have bags: `.bot inv <bot>` answers from them and `.additem` (GiveItem) fills them.
type giveFake struct {
	*prepFake
	bags     map[string]map[uint32]int
	gave     []string // "<bot> <guid> <entry>"
	loseGive bool     // the GM line runs but the item never arrives
	noGUID   bool
}

func newGiveFake(t *testing.T) *giveFake {
	g := newPrepFake(t, map[string]string{"Tanky": "online", "Healy": "online", "Magey": "online"})
	return &giveFake{prepFake: g, bags: map[string]map[uint32]int{}}
}

func (g *giveFake) bag(bot string) map[uint32]int {
	if g.bags[bot] == nil {
		g.bags[bot] = map[uint32]int{}
	}
	return g.bags[bot]
}

func (g *giveFake) BotLines(ctx context.Context, command string, wait time.Duration, done func(string) bool) ([]string, bool, error) {
	f := strings.Fields(command)
	if f[0] != "inv" || len(f) != 2 {
		return g.prepFake.BotLines(ctx, command, wait, done)
	}
	g.log("inv %s", f[1])
	lines := []string{"TBM:INV_BEGIN|" + f[1] + "|100"}
	for e, n := range g.bag(f[1]) {
		lines = append(lines, fmt.Sprintf("TBM:INV_ITEM|255|0|%d|%d|-", e, n))
	}
	return append(lines, "TBM:INV_END|"+f[1]+"|10|16"), true, nil
}

func (g *giveFake) GiveItem(_ context.Context, who string, guid packet.GUID, entry uint32) error {
	g.log("additem %s %d", who, entry)
	g.gave = append(g.gave, fmt.Sprintf("%s %d %d", who, uint32(guid), entry))
	if !g.loseGive {
		g.bag(who)[entry]++
	}
	return nil
}

func (g *giveFake) run(items []GiveItem) (Report, error) {
	o := RemoteOptions{Lead: "Tanky", Bots: []string{"Healy", "Magey"}, Tank: "Tanky", Healer: "Healy", GiveItems: items, Preflight: PreflightOff}
	return runRemote(g.t, g, g.plan, o, nil)
}

func TestGiveItemsSendsAdditemAndChecksTheBag(t *testing.T) {
	g := newGiveFake(t)
	rep, err := g.run([]GiveItem{{Item: itMallet, Name: "Mallet of Zul'Farrak"}}) // no bot: the lead
	if err != nil || rep.State != "done" {
		t.Fatalf("%v %s\n%s", err, rep.State, notesOf(rep))
	}
	if len(g.gave) != 1 || !strings.HasPrefix(g.gave[0], "Tanky ") || !strings.HasSuffix(g.gave[0], " 9240") {
		t.Fatalf("gave %v", g.gave)
	}
	// Bag first (nothing there), then the GM line, then the bag again: the item is in the lead bot's bags.
	if i, a := g.idxOf("inv Tanky"), g.idxOf("additem Tanky 9240"); i < 0 || a < i || g.count("inv Tanky") < 2 || g.bag("Tanky")[itMallet] != 1 {
		t.Fatalf("%v", g.events)
	}
	want := "additem 9240 on Tanky (test setup: Mallet of Zul'Farrak)"
	if len(rep.SetupShortcuts) != 1 || rep.SetupShortcuts[0] != want || rep.Prepare.GMCommands != 1 {
		t.Fatalf("%v %+v", rep.SetupShortcuts, rep.Prepare)
	}
	if st := rep.Prepare.Steps; len(st) != 1 || st[0].Name != "give_items" || !strings.Contains(st[0].Detail, "Tanky got Mallet of Zul'Farrak") {
		t.Fatalf("%+v", st)
	}
}

// The item's name for the setup line is the entry's own, else the plan's name for it, else "item <entry>"; the carrier role is the bot the
// object steps use (not the lead, the tank or the healer).
func TestGiveItemsCarrierRoleAndPlanName(t *testing.T) {
	e := &prepEnv{plan: gongPlan()}
	if n := e.giveName(GiveItem{Item: itMallet}); n != "Mallet of Zul'Farrak" {
		t.Fatalf("the plan's name for the item: %q", n)
	}
	if n := e.giveName(GiveItem{Item: itMallet, Name: "x"}); n != "x" || e.giveName(GiveItem{Item: 5}) != "item 5" {
		t.Fatalf("names %q %q", n, e.giveName(GiveItem{Item: 5}))
	}
	g := newGiveFake(t)
	rep, err := g.run([]GiveItem{{Bot: "carrier", Item: itMallet}, {Bot: "Healy", Item: 1234}})
	if err != nil || rep.State != "done" {
		t.Fatalf("%v %s\n%s", err, rep.State, notesOf(rep))
	}
	if strings.Join(rep.SetupShortcuts, "|") != "additem 9240 on Magey (test setup: item 9240)|additem 1234 on Healy (test setup: item 1234)" {
		t.Fatalf("%v", rep.SetupShortcuts)
	}
	if g.bag("Magey")[itMallet] != 1 || g.bag("Tanky")[itMallet] != 0 {
		t.Fatalf("bags %v", g.bags)
	}
	// With the run's carrier option, "carrier" is that bot.
	g = newGiveFake(t)
	o := RemoteOptions{Lead: "Tanky", Bots: []string{"Healy", "Magey"}, Tank: "Tanky", Healer: "Healy", Carrier: "Healy", GiveItems: []GiveItem{{Bot: "CARRIER", Item: itMallet}}, Preflight: PreflightOff}
	if _, err := runRemote(g.t, g, g.plan, o, nil); err != nil || g.bag("Healy")[itMallet] != 1 {
		t.Fatalf("%v %v", err, g.bags)
	}
}

// A GM account only: `.additem` is a developer's command, and the line would be said aloud in chat from a player's account. Nothing is
// sent, and the run does not start.
func TestGiveItemsRefusedWithoutAGMAccount(t *testing.T) {
	for _, level := range []int{0, 1, 2} {
		g := newGiveFake(t)
		g.level = level
		rep, err := g.run([]GiveItem{{Item: itMallet}})
		if err == nil || rep.State != "failed" || !strings.Contains(err.Error(), "give_items sends .additem, which needs a GM account of level 3") {
			t.Fatalf("level %d: %v %s", level, err, rep.State)
		}
		if len(g.gave) != 0 || g.count("inv ") != 0 || len(rep.SetupShortcuts) != 0 || g.indexOf("commander bind") >= 0 {
			t.Fatalf("level %d: gave %v events %v setup %v", level, g.gave, g.events, rep.SetupShortcuts)
		}
	}
	g := newGiveFake(t)
	g.known = false
	if _, err := g.run([]GiveItem{{Item: itMallet}}); err == nil || !strings.Contains(err.Error(), "account level is unknown") || len(g.gave) != 0 {
		t.Fatalf("%v", err)
	}
}

// The bag check: a GM line that put nothing in the bags fails the step, after the wait, and says what to look at.
func TestGiveItemsFailsWhenTheBagStaysEmpty(t *testing.T) {
	g := newGiveFake(t)
	g.loseGive = true
	rep, err := g.run([]GiveItem{{Item: itMallet}})
	if err == nil || rep.State != "failed" || !strings.Contains(err.Error(), ".additem 9240 was sent for Tanky but Tanky's bags (.bot inv) do not show item 9240 after 8 s") {
		t.Fatalf("%v %s", err, rep.State)
	}
	if g.indexOf("commander bind") >= 0 {
		t.Fatalf("the run went on: %v", g.orders)
	}
}

// A bot that carries the item already gets no second one.
func TestGiveItemsSkipsABotThatCarriesIt(t *testing.T) {
	g := newGiveFake(t)
	g.bag("Tanky")[itMallet] = 1
	rep, err := g.run([]GiveItem{{Item: itMallet}})
	if err != nil || rep.State != "done" || len(g.gave) != 0 || len(rep.SetupShortcuts) != 0 || g.bag("Tanky")[itMallet] != 1 {
		t.Fatalf("%v %s gave %v setup %v", err, rep.State, g.gave, rep.SetupShortcuts)
	}
}

func TestGiveItemsNeedsABotOfTheRunAndAGame(t *testing.T) {
	g := newGiveFake(t)
	if _, err := g.run([]GiveItem{{Bot: "Stranger", Item: itMallet}}); err == nil || !strings.Contains(err.Error(), `"Stranger" is not a bot of the run (Tanky, Healy, Magey)`) || len(g.gave) != 0 {
		t.Fatalf("%v", err)
	}
	// A game that cannot send .additem is refused, not ignored.
	pf := newPrepFake(t, map[string]string{"Tanky": "online"})
	o := RemoteOptions{Lead: "Tanky", Bots: []string{"Healy", "Magey"}, GiveItems: []GiveItem{{Item: itMallet}}, Preflight: PreflightOff}
	rep, err := runRemote(t, pf, pf.plan, o, nil)
	if err == nil || !strings.Contains(err.Error(), "ItemGiver (give_items sends .additem)") || !strings.Contains(err.Error(), "nothing was hired or sent") {
		t.Fatalf("%v", err)
	}
	// It fails before the prepare step: no roster, no hire, no login, no GM line, no bind.
	if len(pf.events) != 0 || pf.indexOf("commander bind") >= 0 || len(rep.SetupShortcuts) != 0 {
		t.Fatalf("something was sent: %v %v", pf.events, pf.orders)
	}
	// With a hired party the missing ability is named before a single hire.
	hf := newPrepFake(t, map[string]string{"Tanky": "online"})
	o = RemoteOptions{Prepare: PrepareSpec{Party: PartyHire}, GiveItems: []GiveItem{{Item: itMallet}}, Preflight: PreflightOff}
	if _, err := runRemote(t, hf, hf.plan, o, nil); err == nil || !strings.Contains(err.Error(), "HireGame") || !strings.Contains(err.Error(), "ItemGiver") || len(hf.events) != 0 {
		t.Fatalf("%v %v", err, hf.events)
	}
}

func TestValidateGiveItems(t *testing.T) {
	for _, ok := range [][]GiveItem{nil, {{Item: 9240}}, {{Bot: "lead", Item: 9240}, {Bot: "Carrier", Item: 1}, {Bot: "Magey", Item: 2, Name: "x"}}} {
		if err := ValidateGiveItems(ok); err != nil {
			t.Errorf("%+v: %v", ok, err)
		}
	}
	for _, c := range []struct {
		g    []GiveItem
		want string
	}{
		{[]GiveItem{{Bot: "lead"}}, "give_items[0].item: required"},
		{[]GiveItem{{Bot: "the lead", Item: 1}}, "neither"},
		{[]GiveItem{{Bot: " lead", Item: 1}}, "spaces"},
		{[]GiveItem{{Bot: "Tanky", Item: 1, Name: "a\nb"}}, "give_items[0].name"},
		{make([]GiveItem, 5), "at most 4"},
	} {
		if err := ValidateGiveItems(c.g); err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%+v: %v", c.g, err)
		}
	}
	// A group run has no prepare step to give items in.
	if err := (PrepareSpec{GiveItems: []GiveItem{{Item: 1}}}).Validate(false); err == nil {
		t.Error("group mode accepted give_items")
	}
}

// The plan check covers the gong's stand: a stand off the navmesh fails with the step named.
func TestPlanCheckCoversTheGongStand(t *testing.T) {
	leg := gongLeg()
	plan := &Plan{Name: "t", Map: 1, Legs: []Leg{leg}}
	nav := fakeNav{}
	res, err := CheckPlanWith(plan, nav, CheckOptions{})
	if err != nil || !res.OK() {
		t.Fatalf("%v %s", err, res.Summary())
	}
	counted := res.Points
	bad := Point{20, 90, 0}
	leg.Steps = append([]Step(nil), leg.Steps...)
	leg.Steps[0] = Step{UseObject: &UseObject{ObjectRef: leg.Steps[0].UseObject.ObjectRef, Stand: &bad, RequiresItem: malletRef}}
	res, err = CheckPlanWith(&Plan{Name: "t", Map: 1, Legs: []Leg{leg}}, fakeNav{block: func(from, to Point) bool { return to.Y == 90 }}, CheckOptions{})
	if err != nil || res.OK() || len(res.Issues) != 1 || res.Issues[0].Part != "steps[0].use_object.stand" || res.Issues[0].Leg != 0 {
		t.Fatalf("%v %+v (ok run counted %d points)", err, res.Issues, counted)
	}
}

// RemoteAbilities names every ability the options need and the game lacks, and passes a game that has them all.
func TestRemoteAbilities(t *testing.T) {
	plain := newPrepFake(t, nil)
	if err := RemoteAbilities(plain, RemoteOptions{}, plain.plan); err != nil {
		t.Fatalf("no options need nothing: %v", err)
	}
	if err := RemoteAbilities(plain, RemoteOptions{Prepare: PrepareSpec{Online: true}}, plain.plan); err != nil {
		t.Fatalf("a Preparer runs prepare: %v", err)
	}
	if err := RemoteAbilities(struct{}{}, RemoteOptions{Prepare: PrepareSpec{Online: true}}, nil); err == nil || !strings.Contains(err.Error(), "Preparer") {
		t.Fatalf("%v", err)
	}
	if err := RemoteAbilities(plain, RemoteOptions{GiveItems: []GiveItem{{Item: 1}}}, nil); err == nil || !strings.Contains(err.Error(), "ItemGiver") || strings.Contains(err.Error(), "BotLiner") {
		t.Fatalf("%v", err)
	}
	if err := RemoteAbilities(newGiveFake(t), RemoteOptions{GiveItems: []GiveItem{{Item: 1}}}, nil); err != nil {
		t.Fatalf("%v", err)
	}
	// An optional leg needs the bags read.
	if err := RemoteAbilities(struct{}{}, RemoteOptions{}, gongPlan()); err == nil || !strings.Contains(err.Error(), "BotLiner (leg 0 (gahzrilla) is optional") {
		t.Fatalf("%v", err)
	}
}
