package dungeon

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/pjerra/turtle-agent/internal/objects"
	"github.com/pjerra/turtle-agent/internal/packet"
	"github.com/pjerra/turtle-agent/internal/tbm"
)

const (
	itMedallion = 7740
	itShaft     = 7741
	itStaff     = 7733
	goChest     = 123329
	goKeystone  = 124371
	goSeal      = 124372
)

var (
	chestGUID    = packet.GUID(0x1F1000000000AAA1)
	keystoneGUID = packet.GUID(0x1F1000000000AAA2)
)

// fakeCarrier is a fakeRemote that also gives the carrier orders: it answers `cmd open|loot|use|lootmethod` like the module would
// (the bags are kept per bot), after the scripted answers of a verb are used up.
type fakeCarrier struct {
	*fakeRemote
	bags      map[string]map[uint32]int
	gobjs     []objects.GameObject
	script    map[string][]tbm.OrderResult // verb -> answers given first, in order
	method    string
	methods   []string // every lootmethod ordered
	behaviors []string
	chestLoot []tbm.ItemCount
	loseLoot  bool                    // the chest opens but gives nothing
	recipe    func(bot string) string // after a `use`: what happened to the bags ("" nothing)
	invErr    bool
}

func newFakeCarrier(t *testing.T) *fakeCarrier {
	return &fakeCarrier{fakeRemote: newFakeRemote(t), bags: map[string]map[uint32]int{}, script: map[string][]tbm.OrderResult{}, method: "group"}
}

func (g *fakeCarrier) bag(bot string) map[uint32]int {
	if g.bags[bot] == nil {
		g.bags[bot] = map[uint32]int{}
	}
	return g.bags[bot]
}

func okOrder(cmd, bot, payload string) tbm.OrderResult {
	a := tbm.Ack{Intent: strings.Join(strings.Fields(cmd)[:2], " "), Scope: "bot:" + bot, Count: 1, Executor: payload}
	r := tbm.OrderResult{Command: cmd, Intent: a.Intent, Bot: bot, Kind: tbm.KindAck, OK: true, Ack: &a, Final: "TBM:ACTION_ACK|" + a.Intent + "|bot:" + bot + "|1|" + payload, Lines: []string{}}
	switch strings.Fields(cmd)[1] {
	case "loot", "open":
		if l, ok := tbm.ParseLoot(payload); ok {
			r.Loot = &l
		} else if payload == "opened" {
			r.Opened = true
		}
	case "use":
		if id, ok := strings.CutPrefix(payload, "spell:"); ok {
			n, _ := strconv.Atoi(id)
			r.Spell = uint32(n)
		}
	case "lootmethod":
		r.Method, r.Threshold, _ = strings.Cut(payload, ":")
	}
	return r
}

func errOrder(cmd, code string) tbm.OrderResult {
	intent := strings.Join(strings.Fields(cmd)[:2], " ")
	e := tbm.Err{Intent: intent, Code: code, Message: code + " message"}
	return tbm.OrderResult{Command: cmd, Intent: intent, Kind: tbm.KindErr, Err: &e, Final: "TBM:ACTION_ERR|" + intent + "|" + code + "|" + e.Message, Lines: []string{}}
}

func timeoutOrder(cmd string) tbm.OrderResult {
	p := tbm.Pending{Intent: strings.Join(strings.Fields(cmd)[:2], " "), BudgetS: 20}
	return tbm.OrderResult{Command: cmd, Intent: p.Intent, Kind: tbm.KindTimeout, Pending: &p, Lines: []string{}}
}

func (g *fakeCarrier) Order(ctx context.Context, command string) (tbm.OrderResult, error) {
	g.orders = append(g.orders, command)
	g.orderAt = append(g.orderAt, g.now)
	f := strings.Fields(command)
	verb := f[1]
	if q := g.script[verb]; len(q) > 0 {
		g.script[verb] = q[1:]
		r := q[0]
		r.Command = command
		if r.Intent == "" {
			r.Intent = "cmd " + verb
		}
		g.advance(3 * time.Second) // an order that ran takes time
		return r, nil
	}
	switch verb {
	case "lootmethod":
		g.method = f[2]
		g.methods = append(g.methods, f[2])
		return okOrder(command, "x", f[2]+":2"), nil
	case "open":
		bot := f[3]
		g.advance(5 * time.Second)
		if f[2] == tbm.GUIDArg(chestGUID) {
			if g.loseLoot {
				return okOrder(command, bot, "-"), nil
			}
			var parts []string
			for _, it := range g.chestLoot {
				g.bag(bot)[it.Entry] += it.Count
				parts = append(parts, fmt.Sprintf("%d:%d", it.Entry, it.Count))
			}
			return okOrder(command, bot, strings.Join(append(parts, "copper:5"), ",")), nil
		}
		return okOrder(command, bot, "opened"), nil
	case "loot":
		bot := f[3]
		g.advance(6 * time.Second)
		if len(f) > 4 {
			id, _ := strconv.Atoi(f[4])
			g.bag(bot)[uint32(id)]++
			return okOrder(command, bot, fmt.Sprintf("%d:1", id)), nil
		}
		return okOrder(command, bot, "-"), nil
	case "use":
		bot := f[3]
		if g.recipe != nil {
			g.recipe(bot)
		}
		return okOrder(command, bot, "spell:9594"), nil
	}
	return errOrder(command, "invalid"), nil
}

func (g *fakeCarrier) Behavior(ctx context.Context, bot, key string, on bool) (tbm.Reply, error) {
	g.behaviors = append(g.behaviors, fmt.Sprintf("%s %s %v", bot, key, on))
	a := tbm.Ack{Intent: "behavior " + key, Scope: "bot:" + bot, Count: 1, Executor: "off"}
	return tbm.Reply{Kind: tbm.KindAck, OK: true, Ack: &a, Lines: []string{}}, nil
}

func (g *fakeCarrier) GameObjects() []objects.GameObject { return g.gobjs }

func (g *fakeCarrier) BotLines(ctx context.Context, command string, wait time.Duration, done func(string) bool) ([]string, bool, error) {
	f := strings.Fields(command)
	if f[0] != "inv" {
		return nil, false, nil
	}
	if g.invErr {
		l := "TBM:ACTION_ERR|inv|no-bot|not your bot"
		return []string{l}, done(l), nil
	}
	lines := []string{"TBM:INV_BEGIN|" + f[1] + "|100"}
	slot := 0
	for e, n := range g.bag(f[1]) {
		if n > 0 {
			lines = append(lines, fmt.Sprintf("TBM:INV_ITEM|255|%d|%d|%d|-", slot, e, n))
			slot++
		}
	}
	lines = append(lines, "TBM:INV_END|"+f[1]+"|10|16")
	return lines, true, nil
}

func chestObj() objects.GameObject {
	return objects.GameObject{GUID: chestGUID, Entry: goChest, X: 30, Y: 0, Z: 0, Distance: 80, State: "ready"}
}

func objOpts() RemoteOptions {
	o := opts()
	o.Tank, o.Healer, o.Preflight = "Tanky", "Healy", "off"
	return o
}

func runCarrier(t *testing.T, g *fakeCarrier, plan *Plan, o RemoteOptions) (Report, error) {
	t.Helper()
	cfg := DefaultConfig()
	cfg.PostFight, cfg.AddsWait = 2*time.Second, 4*time.Second
	d := NewRemoteDriver(plan, g, cfg, o, g.clock())
	err := d.Run(context.Background(), 0)
	rep, ended := d.Status()
	if !ended {
		t.Fatal("not ended")
	}
	return rep, err
}

func lootChestStep() Step {
	return Step{LootObject: &LootObject{ObjectRef: ObjectRef{Entry: goChest}, ExpectItem: &ItemRef{Entry: itMedallion, Name: "Gni'kiv Medallion"}}}
}

func carrierGame(t *testing.T) *fakeCarrier {
	g := newFakeCarrier(t)
	g.gobjs = []objects.GameObject{chestObj()}
	g.chestLoot = []tbm.ItemCount{{Entry: itMedallion, Count: 1}}
	return g
}

func TestCarrierLootsAChest(t *testing.T) {
	g := carrierGame(t)
	plan := remotePlan(Leg{Name: "c", To: Point{20, 0, 0}, Steps: []Step{lootChestStep()}})
	rep, err := runCarrier(t, g, plan, objOpts())
	if err != nil {
		t.Fatalf("%v %v", err, rep.Notes)
	}
	// Free-for-all around the chest, the open order at the chest's spawn GUID for the carrier (a DPS: not the lead, tank or healer).
	ffa, open, back := g.indexOf("cmd lootmethod ffa"), g.indexOf("cmd open"), g.indexOf("cmd lootmethod group")
	if ffa < 0 || open < ffa || back < open {
		t.Fatalf("%v", g.orders)
	}
	if g.orders[open] != "cmd open "+tbm.GUIDArg(chestGUID)+" Magey" {
		t.Fatalf("%s", g.orders[open])
	}
	r := rep.Remote
	if r.Carrier != "Magey" || len(r.ObjectSteps) != 1 {
		t.Fatalf("%+v", r)
	}
	s := r.ObjectSteps[0]
	if s.Kind != "loot_object" || s.Carrier != "Magey" || s.Result != "done" || len(s.Orders) != 3 || s.Seconds <= 0 || fmt.Sprint(s.Gained) != "[7740 x1 copper 5]" {
		t.Fatalf("%+v", s)
	}
	if s.Orders[1].Result != "ack" || !strings.Contains(s.Orders[1].Reply, "7740:1") {
		t.Fatalf("%+v", s.Orders)
	}
	if r.Orders["open"] != 1 || r.Orders["lootmethod"] != 2 || r.OrdersRefused != 0 {
		t.Fatalf("%+v", r)
	}
}

func TestCarrierOptionAndDefault(t *testing.T) {
	g := carrierGame(t)
	plan := remotePlan(Leg{Name: "c", To: Point{20, 0, 0}, Steps: []Step{lootChestStep()}})
	o := objOpts()
	o.Carrier = "Healy"
	if _, err := runCarrier(t, g, plan, o); err != nil || !strings.HasSuffix(g.orders[g.indexOf("cmd open")], " Healy") {
		t.Fatalf("%v %v", err, g.orders)
	}
	g = carrierGame(t)
	o.Carrier = "Nobody"
	if _, err := runCarrier(t, g, plan, o); err == nil || !strings.Contains(err.Error(), "carrier Nobody is not in the party") || g.indexOf("cmd open") >= 0 {
		t.Fatalf("%v", err)
	}
}

func TestCarrierChestAlreadyOpened(t *testing.T) {
	g := carrierGame(t)
	g.bag("Magey")[itMedallion] = 1
	plan := remotePlan(Leg{Name: "c", To: Point{20, 0, 0}, Steps: []Step{lootChestStep()}})
	rep, err := runCarrier(t, g, plan, objOpts())
	if err != nil || g.indexOf("cmd") < 0 || g.indexOf("cmd open") >= 0 || rep.Remote.ObjectSteps[0].Result != "skipped" {
		t.Fatalf("%v %v %+v", err, g.orders, rep.Remote.ObjectSteps)
	}
}

func TestCarrierChestNeedsTheAgentInView(t *testing.T) {
	g := carrierGame(t)
	g.gobjs = nil
	plan := remotePlan(Leg{Name: "c", To: Point{20, 0, 0}, Steps: []Step{lootChestStep()}})
	rep, err := runCarrier(t, g, plan, objOpts())
	if err == nil || !strings.Contains(err.Error(), "the agent must be within view of game object 123329") || g.indexOf("cmd open") >= 0 || g.unbinds != 1 || rep.State != "failed" {
		t.Fatalf("%v", err)
	}
	// An object whose position the server never sent is not in view either.
	g = carrierGame(t)
	g.gobjs[0].Distance = -1
	if _, err := runCarrier(t, g, plan, objOpts()); err == nil || !strings.Contains(err.Error(), "within view") {
		t.Fatalf("%v", err)
	}
}

func TestCarrierChestOpensButHoldsNothing(t *testing.T) {
	g := carrierGame(t)
	g.loseLoot = true
	plan := remotePlan(Leg{Name: "c", To: Point{20, 0, 0}, Steps: []Step{lootChestStep()}})
	rep, err := runCarrier(t, g, plan, objOpts())
	if err == nil || !strings.Contains(err.Error(), "Gni'kiv Medallion (item 7740) is not in its bags") || g.indexOf("cmd lootmethod group") < 0 {
		t.Fatalf("%v %v", err, g.orders)
	}
	if s := rep.Remote.ObjectSteps[0]; s.Result != "failed" || s.Error == "" {
		t.Fatalf("%+v", s)
	}
}

func TestCarrierOrderErrors(t *testing.T) {
	for _, tc := range []struct{ code, want string }{
		{"no-item", "no key or item"}, {"locked", "lacks the skill"}, {"unavailable", "left the party"}, {"denied", "not the bot's owner"},
		{"invalid-target", "no such corpse or object"}, {"failed", "failed message"}, {"unreachable", "no way to it"}, {"timeout", "budget"},
	} {
		g := carrierGame(t)
		g.script["open"] = []tbm.OrderResult{errOrder("cmd open", tc.code)}
		plan := remotePlan(Leg{Name: "c", To: Point{20, 0, 0}, Steps: []Step{lootChestStep()}})
		rep, err := runCarrier(t, g, plan, objOpts())
		if err == nil || !strings.Contains(err.Error(), "refused: "+tc.code) || !strings.Contains(err.Error(), tc.want) {
			t.Fatalf("%s: %v", tc.code, err)
		}
		if len(g.ordersOf("cmd open")) != 1 || g.indexOf("cmd lootmethod group") < 0 || g.unbinds != 1 {
			t.Fatalf("%s: %v", tc.code, g.orders)
		}
		if rep.Remote.Refused["open:"+tc.code] != 1 {
			t.Fatalf("%s: %+v", tc.code, rep.Remote.Refused)
		}
	}
}

func TestCarrierBusyAndCancelledAreRetried(t *testing.T) {
	g := carrierGame(t)
	g.script["open"] = []tbm.OrderResult{errOrder("cmd open", "busy"), errOrder("cmd open", "busy"), errOrder("cmd open", "cancelled")}
	plan := remotePlan(Leg{Name: "c", To: Point{20, 0, 0}, Steps: []Step{lootChestStep()}})
	rep, err := runCarrier(t, g, plan, objOpts())
	if err != nil || len(g.ordersOf("cmd open")) != 4 {
		t.Fatalf("%v %v", err, g.orders)
	}
	s := rep.Remote.ObjectSteps[0]
	if s.Orders[1].Result != "busy" || s.Orders[3].Result != "cancelled" || s.Orders[4].Result != "ack" {
		t.Fatalf("%+v", s.Orders)
	}
	// A carrier that stays busy fails the step.
	g = carrierGame(t)
	for i := 0; i < 9; i++ {
		g.script["open"] = append(g.script["open"], errOrder("cmd open", "busy"))
	}
	if _, err := runCarrier(t, g, plan, objOpts()); err == nil || !strings.Contains(err.Error(), "refused: busy") {
		t.Fatalf("%v", err)
	}
	// ... and one that keeps being cancelled.
	g = carrierGame(t)
	for i := 0; i < 5; i++ {
		g.script["open"] = append(g.script["open"], errOrder("cmd open", "cancelled"))
	}
	if _, err := runCarrier(t, g, plan, objOpts()); err == nil || !strings.Contains(err.Error(), "refused: cancelled") || len(g.ordersOf("cmd open")) != 3 {
		t.Fatalf("%v %v", err, g.ordersOf("cmd open"))
	}
}

func TestCarrierInCombatWaitsForTheFight(t *testing.T) {
	g := carrierGame(t)
	g.script["open"] = []tbm.OrderResult{errOrder("cmd open", "in-combat"), errOrder("cmd open", "in-combat")}
	plan := remotePlan(Leg{Name: "c", To: Point{20, 0, 0}, Steps: []Step{lootChestStep()}})
	if _, err := runCarrier(t, g, plan, objOpts()); err != nil || len(g.ordersOf("cmd open")) != 3 {
		t.Fatalf("%v %v", err, g.orders)
	}
	g = carrierGame(t)
	for i := 0; i < 100; i++ {
		g.script["open"] = append(g.script["open"], errOrder("cmd open", "in-combat"))
	}
	if _, err := runCarrier(t, g, plan, objOpts()); err == nil || !strings.Contains(err.Error(), "refused: in-combat") {
		t.Fatalf("%v", err)
	}
}

func TestCarrierTooFarWalksThePartyNearer(t *testing.T) {
	g := carrierGame(t)
	g.gobjs[0].X = 100
	g.script["open"] = []tbm.OrderResult{errOrder("cmd open", "too-far")}
	plan := remotePlan(Leg{Name: "c", To: Point{20, 0, 0}, Steps: []Step{lootChestStep()}})
	moves := len(g.ordersOf("cmd move"))
	_, err := runCarrier(t, g, plan, objOpts())
	if err != nil || len(g.ordersOf("cmd open")) != 2 || len(g.ordersOf("cmd move")) <= moves+1 {
		t.Fatalf("%v %v", err, g.orders)
	}
	if l := g.lead(); l.X < 90 {
		t.Fatalf("the lead bot stopped at %v", l.X)
	}
	// Still too far after two walks: the step fails and says why.
	g = carrierGame(t)
	for i := 0; i < 4; i++ {
		g.script["open"] = append(g.script["open"], errOrder("cmd open", "too-far"))
	}
	if _, err := runCarrier(t, g, plan, objOpts()); err == nil || !strings.Contains(err.Error(), "150 yd") || len(g.ordersOf("cmd open")) != 3 {
		t.Fatalf("%v %v", err, g.ordersOf("cmd open"))
	}
}

func TestCarrierNoFinalAnswer(t *testing.T) {
	g := carrierGame(t)
	g.script["open"] = []tbm.OrderResult{timeoutOrder("cmd open")}
	plan := remotePlan(Leg{Name: "c", To: Point{20, 0, 0}, Steps: []Step{lootChestStep()}})
	rep, err := runCarrier(t, g, plan, objOpts())
	if err == nil || !strings.Contains(err.Error(), "no final answer 20 s after it reported running") {
		t.Fatalf("%v", err)
	}
	if rep.Remote.OrdersUnanswered != 1 || rep.Remote.ObjectSteps[0].Orders[1].Result != "timeout" || !rep.Remote.ObjectSteps[0].Orders[1].Pending {
		t.Fatalf("%+v", rep.Remote)
	}
	// The loot method still goes back.
	if g.indexOf("cmd lootmethod group") < 0 {
		t.Fatalf("%v", g.orders)
	}
}

func TestCarrierCannotSetFreeForAll(t *testing.T) {
	g := carrierGame(t)
	g.script["lootmethod"] = []tbm.OrderResult{errOrder("cmd lootmethod", "failed")}
	plan := remotePlan(Leg{Name: "c", To: Point{20, 0, 0}, Steps: []Step{lootChestStep()}})
	_, err := runCarrier(t, g, plan, objOpts())
	if err == nil || !strings.Contains(err.Error(), "could not set free-for-all loot") || g.indexOf("cmd open") >= 0 {
		t.Fatalf("%v", err)
	}
	// An ACK that reads another method back is no free-for-all either.
	g = carrierGame(t)
	g.script["lootmethod"] = []tbm.OrderResult{okOrder("cmd lootmethod ffa", "x", "group:2")}
	if _, err := runCarrier(t, g, plan, objOpts()); err == nil || !strings.Contains(err.Error(), `the party's method is "group"`) {
		t.Fatalf("%v", err)
	}
}

func TestCarrierLootRestoreOption(t *testing.T) {
	g := carrierGame(t)
	plan := remotePlan(Leg{Name: "c", To: Point{20, 0, 0}, Steps: []Step{lootChestStep()}})
	o := objOpts()
	o.LootRestore = "RR"
	if _, err := runCarrier(t, g, plan, o); err != nil || fmt.Sprint(g.methods) != "[ffa rr]" {
		t.Fatalf("%v %v", err, g.methods)
	}
}

func medallionCombine() Step {
	return Step{UseItem: &UseItem{Entry: itMedallion, Name: "Gni'kiv Medallion", RequiresItem: &ItemRef{Entry: itShaft, Name: "The Shaft of Tsol"}, ExpectItem: &ItemRef{Entry: itStaff, Name: "Staff of Prehistoria"}}}
}

func combineRecipe(g *fakeCarrier) func(string) string {
	return func(bot string) string {
		b := g.bag(bot)
		if b[itMedallion] > 0 && b[itShaft] > 0 {
			b[itMedallion]--
			b[itShaft]--
			b[itStaff]++
		}
		return ""
	}
}

func TestCarrierCombinesTheStaff(t *testing.T) {
	g := carrierGame(t)
	g.recipe = combineRecipe(g)
	g.bag("Magey")[itMedallion] = 1
	g.bag("Magey")[itShaft] = 1
	plan := remotePlan(Leg{Name: "c", To: Point{20, 0, 0}, AfterBoss: nil, Steps: []Step{medallionCombine()}})
	rep, err := runCarrier(t, g, plan, objOpts())
	if err != nil {
		t.Fatalf("%v %v", err, rep.Notes)
	}
	if g.indexOf("cmd use 7740 Magey") < 0 || g.indexOf("cmd lootmethod") >= 0 {
		t.Fatalf("%v", g.orders)
	}
	s := rep.Remote.ObjectSteps[0]
	if s.Kind != "use_item" || s.Result != "done" || fmt.Sprint(s.Gained) != "[7733 x1]" || !strings.Contains(s.Orders[0].Reply, "spell:9594") {
		t.Fatalf("%+v", s)
	}
}

func TestCarrierCombineChecksTheBags(t *testing.T) {
	plan := remotePlan(Leg{Name: "c", To: Point{20, 0, 0}, Steps: []Step{medallionCombine()}})
	// No Shaft: named, and no order is sent.
	g := carrierGame(t)
	g.recipe = combineRecipe(g)
	g.bag("Magey")[itMedallion] = 1
	if _, err := runCarrier(t, g, plan, objOpts()); err == nil || !strings.Contains(err.Error(), "Magey does not carry The Shaft of Tsol (item 7741)") || g.indexOf("cmd use") >= 0 {
		t.Fatalf("%v", err)
	}
	// No Medallion.
	g = carrierGame(t)
	g.bag("Magey")[itShaft] = 1
	if _, err := runCarrier(t, g, plan, objOpts()); err == nil || !strings.Contains(err.Error(), "does not carry Gni'kiv Medallion") {
		t.Fatalf("%v", err)
	}
	// Someone else holds them: the carrier is the one checked.
	g = carrierGame(t)
	g.bag("Healy")[itMedallion], g.bag("Healy")[itShaft] = 1, 1
	if _, err := runCarrier(t, g, plan, objOpts()); err == nil || !strings.Contains(err.Error(), "Magey does not carry") {
		t.Fatalf("%v", err)
	}
	// The Staff is there already (a resumed run): nothing is used.
	g = carrierGame(t)
	g.bag("Magey")[itStaff] = 1
	rep, err := runCarrier(t, g, plan, objOpts())
	if err != nil || g.indexOf("cmd use") >= 0 || rep.Remote.ObjectSteps[0].Result != "skipped" {
		t.Fatalf("%v %v", err, g.orders)
	}
	// The cast is over but the bags did not change.
	g = carrierGame(t)
	g.bag("Magey")[itMedallion], g.bag("Magey")[itShaft] = 1, 1
	if _, err := runCarrier(t, g, plan, objOpts()); err == nil || !strings.Contains(err.Error(), "Staff of Prehistoria (item 7733) is not in its bags") {
		t.Fatalf("%v", err)
	}
	// .bot inv refused.
	g = carrierGame(t)
	g.invErr = true
	if _, err := runCarrier(t, g, plan, objOpts()); err == nil || !strings.Contains(err.Error(), ".bot inv Magey refused: no-bot") {
		t.Fatalf("%v", err)
	}
}

func TestCarrierUseNoItemAnswer(t *testing.T) {
	g := carrierGame(t)
	g.bag("Magey")[itMedallion], g.bag("Magey")[itShaft] = 1, 1
	g.script["use"] = []tbm.OrderResult{errOrder("cmd use", "no-item")}
	plan := remotePlan(Leg{Name: "c", To: Point{20, 0, 0}, Steps: []Step{medallionCombine()}})
	if _, err := runCarrier(t, g, plan, objOpts()); err == nil || !strings.Contains(err.Error(), "refused: no-item") {
		t.Fatalf("%v", err)
	}
}

func keystoneStep() []Step {
	return []Step{
		{UseObject: &UseObject{ObjectRef: ObjectRef{Entry: goKeystone}, RequiresItem: &ItemRef{Entry: itStaff, Name: "Staff of Prehistoria"}}},
		{WaitFor: &WaitFor{GameObject: &ObjectState{ObjectRef: ObjectRef{Entry: goSeal}, State: "active"}, TimeoutS: 60}},
	}
}

func TestCarrierOpensTheKeystone(t *testing.T) {
	g := carrierGame(t)
	g.gobjs = []objects.GameObject{{GUID: keystoneGUID, Entry: goKeystone, X: 20, Y: 0, Z: 0, Distance: 3, State: "ready"}}
	g.bag("Magey")[itStaff] = 1
	plan := remotePlan(Leg{Name: "k", To: Point{20, 0, 0}, Steps: keystoneStep()})
	rep, err := runCarrier(t, g, plan, objOpts())
	if err != nil {
		t.Fatalf("%v %v", err, rep.Notes)
	}
	if g.indexOf("cmd open "+tbm.GUIDArg(keystoneGUID)+" Magey") < 0 || g.indexOf("cmd lootmethod") >= 0 {
		t.Fatalf("%v", g.orders)
	}
	s := rep.Remote.ObjectSteps[0]
	if s.Kind != "use_object" || s.Result != "done" || !strings.HasSuffix(s.Orders[0].Reply, "|opened") {
		t.Fatalf("%+v", s)
	}
	// The Seal is not in the agent's view: its state is not checked, the run goes on after the settle time and says so.
	noted := false
	for _, n := range rep.Notes {
		noted = noted || strings.Contains(n, "cannot see game object 124372")
	}
	if !noted {
		t.Fatalf("%v", rep.Notes)
	}
}

func TestCarrierSealInViewIsChecked(t *testing.T) {
	g := carrierGame(t)
	g.gobjs = []objects.GameObject{
		{GUID: keystoneGUID, Entry: goKeystone, X: 20, Y: 0, Z: 0, Distance: 3, State: "ready"},
		{GUID: packet.GUID(0x1F1000000000AAA3), Entry: goSeal, X: 20, Y: 50, Z: 0, Distance: 50, State: "ready"},
	}
	g.bag("Magey")[itStaff] = 1
	opened := false
	g.fakeRemote.onTick = func(*fakeRemote) {
		if len(g.ordersOf("cmd open")) > 0 && !opened {
			opened = true
			g.gobjs[1].State = "active"
		}
	}
	plan := remotePlan(Leg{Name: "k", To: Point{20, 0, 0}, Steps: keystoneStep()})
	rep, err := runCarrier(t, g, plan, objOpts())
	if err != nil || !opened {
		t.Fatalf("%v %v", err, rep.Notes)
	}
	for _, n := range rep.Notes {
		if strings.Contains(n, "cannot see") {
			t.Fatalf("a visible Seal was not checked: %v", n)
		}
	}
	// A Seal that stays closed fails after its timeout.
	g2 := carrierGame(t)
	g2.gobjs = []objects.GameObject{g.gobjs[0], g.gobjs[1]}
	g2.gobjs[1].State = "ready"
	g2.bag("Magey")[itStaff] = 1
	if _, err := runCarrier(t, g2, plan, objOpts()); err == nil || !strings.Contains(err.Error(), "waited 60 s") {
		t.Fatalf("%v", err)
	}
}

func TestCarrierKeystoneNeedsTheStaffInTheCarriersBags(t *testing.T) {
	g := carrierGame(t)
	g.gobjs = []objects.GameObject{{GUID: keystoneGUID, Entry: goKeystone, X: 20, Y: 0, Z: 0, Distance: 3, State: "ready"}}
	g.bag("Healy")[itStaff] = 1 // another bot holds it
	plan := remotePlan(Leg{Name: "k", To: Point{20, 0, 0}, Steps: keystoneStep()})
	_, err := runCarrier(t, g, plan, objOpts())
	if err == nil || !strings.Contains(err.Error(), "Magey does not carry Staff of Prehistoria (item 7733)") || g.indexOf("cmd open") >= 0 {
		t.Fatalf("%v", err)
	}
	// A locked answer from the module is shown.
	g = carrierGame(t)
	g.gobjs = []objects.GameObject{{GUID: keystoneGUID, Entry: goKeystone, X: 20, Y: 0, Z: 0, Distance: 3, State: "ready"}}
	g.bag("Magey")[itStaff] = 1
	g.script["open"] = []tbm.OrderResult{errOrder("cmd open", "no-item")}
	if _, err := runCarrier(t, g, plan, objOpts()); err == nil || !strings.Contains(err.Error(), "refused: no-item") {
		t.Fatalf("%v", err)
	}
}

func bossPlan(off bool) *Plan {
	b := &Boss{Entry: 6910, Position: Point{50, 0, 0}, LootItem: &ItemRef{Entry: itShaft, Name: "The Shaft of Tsol"}}
	if off {
		f := false
		b.CompanionsLoot = &f
	}
	return remotePlan(Leg{Name: "r", To: Point{20, 0, 0}, Boss: b, AfterBoss: []Step{medallionCombine()}})
}

func TestCarrierLootsTheBossItem(t *testing.T) {
	g := carrierGame(t)
	g.recipe = combineRecipe(g)
	g.bag("Magey")[itMedallion] = 1
	boss := g.addMob(6910, Point{50, 0, 0})
	rep, err := runCarrier(t, g, bossPlan(true), objOpts())
	if err != nil {
		t.Fatalf("%v %v", err, rep.Notes)
	}
	want := fmt.Sprintf("cmd loot %s Magey %d", tbm.GUIDArg(boss.guid), itShaft)
	ffa, loot, back, use := g.indexOf("cmd lootmethod ffa"), g.indexOf(want), g.indexOf("cmd lootmethod group"), g.indexOf("cmd use")
	if ffa < 0 || loot < ffa || back < loot || use < back {
		t.Fatalf("want %q in %v", want, g.orders)
	}
	// companions_loot false: the others' looting is off for the fight and back on after the loot; the carrier's is left alone.
	if fmt.Sprint(g.behaviors) != "[Tanky loot false Healy loot false Tanky loot true Healy loot true]" {
		t.Fatalf("%v", g.behaviors)
	}
	if g.bag("Magey")[itStaff] != 1 {
		t.Fatalf("%v", g.bag("Magey"))
	}
	var kinds []string
	for _, s := range rep.Remote.ObjectSteps {
		kinds = append(kinds, s.Kind+":"+s.Result+":"+fmt.Sprint(s.Gained))
	}
	if fmt.Sprint(kinds) != "[boss loot_item:done:[7741 x1] use_item:done:[7733 x1]]" {
		t.Fatalf("%v", kinds)
	}
}

func TestCarrierBossItemFailures(t *testing.T) {
	// The item comes out of the order, but the order says failed (round robin hid it): the hint names the cause.
	g := carrierGame(t)
	g.addMob(6910, Point{50, 0, 0})
	g.script["loot"] = []tbm.OrderResult{errOrder("cmd loot", "failed")}
	_, err := runCarrier(t, g, bossPlan(true), objOpts())
	if err == nil || !strings.Contains(err.Error(), "refused: failed") || !strings.Contains(err.Error(), "companions_loot") || g.indexOf("cmd lootmethod group") < 0 {
		t.Fatalf("%v %v", err, g.orders)
	}
	if len(g.behaviors) != 4 { // looting is turned back on after a failure too
		t.Fatalf("%v", g.behaviors)
	}
	// A carrier that already took it from the corpse (looting on) has nothing to fetch.
	g = carrierGame(t)
	g.addMob(6910, Point{50, 0, 0})
	g.bag("Magey")[itShaft] = 1
	g.bag("Magey")[itMedallion] = 1
	g.recipe = combineRecipe(g)
	rep, err := runCarrier(t, g, bossPlan(false), objOpts())
	if err != nil || g.indexOf("cmd loot") >= 0 || len(g.behaviors) != 0 {
		t.Fatalf("%v %v %v", err, g.orders, g.behaviors)
	}
	if rep.Remote.ObjectSteps[0].Result != "skipped" {
		t.Fatalf("%+v", rep.Remote.ObjectSteps)
	}
}

func TestCarrierWithoutOrdersFailsAsBefore(t *testing.T) {
	g := newFakeRemote(t) // no OrderGame
	plan := remotePlan(Leg{Name: "c", To: Point{20, 0, 0}, Steps: []Step{medallionCombine()}})
	_, err := runRemote(t, g, plan, opts(), nil)
	if err == nil || !strings.Contains(err.Error(), "remote mode: object step needs the agent nearby") {
		t.Fatalf("%v", err)
	}
}

func TestCarrierLeavesPartyAndBindingErrors(t *testing.T) {
	g := carrierGame(t)
	g.script["open"] = []tbm.OrderResult{errOrder("cmd open", "not-bound")}
	plan := remotePlan(Leg{Name: "c", To: Point{20, 0, 0}, Steps: []Step{lootChestStep()}})
	if _, err := runCarrier(t, g, plan, objOpts()); err == nil || !strings.Contains(err.Error(), "binding ended") {
		t.Fatalf("%v", err)
	}
}

func TestCarrierRejoinSendsFollow(t *testing.T) {
	g := carrierGame(t)
	plan := remotePlan(Leg{Name: "c", To: Point{20, 0, 0}, Steps: []Step{lootChestStep()}})
	// The carrier stays away for 20 s after its order.
	away := time.Time{}
	g.onTick = func(f *fakeRemote) {
		if len(g.ordersOf("cmd open")) > 0 {
			if away.IsZero() {
				away = f.now
			}
			if f.now.Sub(away) < 20*time.Second {
				f.party[2].X += 100
			}
		}
	}
	rep, err := runCarrier(t, g, plan, objOpts())
	if err != nil || g.indexOf("cmd follow") < 0 || rep.Remote.ObjectSteps[0].Result != "done" {
		t.Fatalf("%v %v", err, g.orders)
	}
}

var _ = packet.GUID(0)
