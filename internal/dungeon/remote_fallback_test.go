package dungeon

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/pjerra/turtle-agent/internal/objects"
	"github.com/pjerra/turtle-agent/internal/packet"
	"github.com/pjerra/turtle-agent/internal/tbm"
)

// The approach to an object and the agent's fallback (remote_approach.go, remote_fallback.go).

// handsGame is a nearGame whose agent can act: its own Opening cast and loot, its bags, the visibility lines and the trade.
type handsGame struct {
	*nearGame
	mine        map[uint32]uint32 // the agent's own bags
	chestHas    []ItemCount       // what the agent's Opening cast takes out of the chest
	lootErrs    []error           // answers of the next LootObject calls
	loots       int
	useObjs     int
	useItems    []uint32
	made        map[uint32]uint32 // item used -> item that appears in the agent's bags (the combine)
	gmLines     []string
	gmCombat    []bool // whether the party fought when each GM line went out
	visible     bool   // the GM agent is visible (starts invisible: the setup ran `gm visible off`)
	trades      []string
	tradeAnswer []TradeOutcome // answers of the next trades
	onLoot      func(g *handsGame)
	log         []string // every call of the hands and every GM line, in order
}

func newHandsGame(t *testing.T, level int) *handsGame {
	n := newNearGame(t, level)
	n.gobjs = []objects.GameObject{chestObj()}
	return &handsGame{nearGame: n, mine: map[uint32]uint32{}, made: map[uint32]uint32{}}
}

func (g *handsGame) LootObject(ctx context.Context, guid packet.GUID) (Looted, error) {
	g.log = append(g.log, "loot")
	g.loots++
	if g.onLoot != nil {
		g.onLoot(g)
	}
	if len(g.lootErrs) > 0 {
		err := g.lootErrs[0]
		g.lootErrs = g.lootErrs[1:]
		return Looted{}, err
	}
	g.advance(5 * time.Second) // the Opening cast
	for _, it := range g.chestHas {
		g.mine[it.Entry] += it.Count
	}
	return Looted{Items: g.chestHas, Money: 5}, nil
}

func (g *handsGame) LootCorpse(ctx context.Context, guid packet.GUID) (Looted, error) {
	return Looted{}, errors.New("not in this test")
}

func (g *handsGame) UseItem(ctx context.Context, entry uint32) error {
	g.log = append(g.log, fmt.Sprintf("use %d", entry))
	g.useItems = append(g.useItems, entry)
	if made, ok := g.made[entry]; ok {
		g.mine[made]++
	}
	return nil
}

func (g *handsGame) UseObject(ctx context.Context, guid packet.GUID) error {
	g.log = append(g.log, "useobject")
	g.useObjs++
	return nil
}

func (g *handsGame) Items() []objects.Item {
	var out []objects.Item
	for e, n := range g.mine {
		if n > 0 {
			out = append(out, objects.Item{Entry: e, Count: n})
		}
	}
	return out
}

func (g *handsGame) GM(ctx context.Context, text string) error {
	g.log = append(g.log, text)
	g.gmLines = append(g.gmLines, text)
	g.gmCombat = append(g.gmCombat, g.party[0].State == "c")
	g.visible = text == ".gm visible on"
	return nil
}

func (g *handsGame) Trade(ctx context.Context, to string, items []ItemCount) (TradeOutcome, error) {
	g.log = append(g.log, "trade")
	desc := to
	for _, it := range items {
		desc += fmt.Sprintf(" %dx%d", it.Entry, it.Count)
	}
	g.trades = append(g.trades, desc)
	g.advance(3 * time.Second)
	if len(g.tradeAnswer) > 0 {
		a := g.tradeAnswer[0]
		g.tradeAnswer = g.tradeAnswer[1:]
		return a, nil
	}
	if g.level > 0 && !g.visible {
		return TradeOutcome{Result: "no_target", Detail: "the bot cannot see the invisible GM"}, nil
	}
	for _, it := range items {
		if g.mine[it.Entry] < it.Count {
			return TradeOutcome{Result: "cancelled", Detail: "the agent does not hold the item"}, nil
		}
		g.mine[it.Entry] -= it.Count
		g.bag(to)[it.Entry] += int(it.Count)
	}
	return TradeOutcome{Result: "completed", Verified: true}, nil
}

func handsRun(t *testing.T, g *handsGame, plan *Plan, mod func(*RemoteOptions)) (Report, error) {
	t.Helper()
	g.agent = Point{-300, 0, 0}
	cfg := DefaultConfig()
	cfg.PostFight, cfg.AddsWait = 2*time.Second, 4*time.Second
	if g.level > 0 {
		cfg.Setup = []string{"gm visible off"}
	}
	o := objOpts()
	if mod != nil {
		mod(&o)
	}
	d := NewRemoteDriver(plan, g, cfg, o, g.clock())
	err := d.Run(context.Background(), 0)
	rep, ended := d.Status()
	if !ended {
		t.Fatal("not ended")
	}
	return rep, err
}

func chestPlan() *Plan {
	return remotePlan(Leg{Name: "c", To: Point{20, 0, 0}, Steps: []Step{lootChestStep()}})
}

func openFails(code string, n int) []tbm.OrderResult {
	var out []tbm.OrderResult
	for i := 0; i < n; i++ {
		out = append(out, errOrder("cmd open", code))
	}
	return out
}

func indexIn(list []string, want string) int {
	for i, s := range list {
		if s == want {
			return i
		}
	}
	return -1
}

// The carrier's open fails twice: the agent (a GM made invisible by the setup) opens the chest itself, goes visible for the trade only,
// hands the Medallion over and `.bot inv` shows it.
func TestAgentOpensTheChestWhenTheCarrierFailsTwice(t *testing.T) {
	g := newHandsGame(t, 3)
	g.chestHas = []ItemCount{{Entry: itMedallion, Count: 1}}
	g.script["open"] = openFails("failed", 2)
	rep, err := handsRun(t, g, chestPlan(), nil)
	if err != nil {
		t.Fatalf("%v\n%s", err, notesOf(rep))
	}
	if n := len(g.ordersOf("cmd open")); n != 2 {
		t.Fatalf("the carrier was sent %d opens: %v", n, g.orders)
	}
	if g.loots != 1 || fmt.Sprint(g.trades) != "[Magey 7740x1]" || g.bag("Magey")[itMedallion] != 1 {
		t.Fatalf("loots %d trades %v bag %v", g.loots, g.trades, g.bag("Magey"))
	}
	// Visible only around the trade: on, trade, off.
	if fmt.Sprint(g.gmLines) != "[.gm visible on .gm visible off]" {
		t.Fatalf("%v", g.gmLines)
	}
	on, tr, off := indexIn(g.log, ".gm visible on"), indexIn(g.log, "trade"), indexIn(g.log, ".gm visible off")
	if !(indexIn(g.log, "loot") < on && on < tr && tr < off) {
		t.Fatalf("%v", g.log)
	}
	r := rep.Remote
	s := r.ObjectSteps[0]
	if s.Result != "done" || !s.FallbackUsed || s.Fallback == nil || s.Fallback.Result != "done" || s.CarrierResult != "failed" {
		t.Fatalf("%+v", s)
	}
	tr2 := s.Fallback.Trade
	if tr2 == nil || tr2.Result != "completed" || !tr2.Verified || !tr2.InBags || fmt.Sprint(tr2.Items) != "[7740 x1]" || tr2.Visible == "" {
		t.Fatalf("%+v", tr2)
	}
	if fmt.Sprint(s.Fallback.Items) != "[7740 x1]" || fmt.Sprint(s.Gained) != "[7740 x1 copper 5]" || len(s.Orders) != 4 {
		t.Fatalf("%+v", s)
	}
	// Each visibility line is a GM assist (plus the appears that brought the agent up); gm_commands stays 0 and the setup list names them.
	if r.GMCommands != 0 || s.Fallback.GMAssists < 2 || r.RunGMAssists < 2 || !strings.Contains(strings.Join(rep.SetupShortcuts, "|"), ".gm visible on (GM assist during the run)") ||
		!strings.Contains(strings.Join(rep.SetupShortcuts, "|"), ".gm visible off (GM assist during the run)") {
		t.Fatalf("%+v setup %v", r, rep.SetupShortcuts)
	}
	if !strings.Contains(notesOf(rep), "the agent takes over (loot_object)") {
		t.Fatalf("%s", notesOf(rep))
	}
	// The party was held for the order and sent back to following afterwards.
	if g.indexOf("cmd stay") < 0 || g.indexOf("cmd follow") < g.indexOf("cmd stay") {
		t.Fatalf("%v", g.orders)
	}
}

// A player's account (no GM line at all): the agent walks to the chest, opens it and trades.
func TestAgentThatIsNoGMOpensAndTradesWithoutGMLines(t *testing.T) {
	g := newHandsGame(t, 0)
	g.chestHas = []ItemCount{{Entry: itMedallion, Count: 1}}
	g.script["open"] = openFails("failed", 2)
	rep, err := handsRun(t, g, chestPlan(), nil)
	if err != nil {
		t.Fatalf("%v\n%s", err, notesOf(rep))
	}
	if len(g.gmLines) != 0 || len(g.appears) != 0 || len(g.walks) == 0 || fmt.Sprint(g.trades) != "[Magey 7740x1]" || rep.Remote.RunGMAssists != 0 {
		t.Fatalf("gm %v appears %v walks %v trades %v %+v", g.gmLines, g.appears, g.walks, g.trades, rep.Remote)
	}
	// The last walk puts the agent within reach of the chest.
	if w := g.walks[len(g.walks)-1]; dist3(w, Point{30, 0, 0}) > UseRange {
		t.Fatalf("%v", g.walks)
	}
}

// The first failure is tried again: a second answer of ack ends the step with no agent.
func TestCarrierSecondTryIsEnough(t *testing.T) {
	g := newHandsGame(t, 3)
	g.chestLoot = []tbm.ItemCount{{Entry: itMedallion, Count: 1}}
	g.script["open"] = openFails("failed", 1)
	rep, err := handsRun(t, g, chestPlan(), nil)
	if err != nil || g.loots != 0 || len(g.trades) != 0 || len(g.gmLines) != 0 || len(g.ordersOf("cmd open")) != 2 {
		t.Fatalf("%v loots %d %v %v", err, g.loots, g.orders, notesOf(rep))
	}
	if s := rep.Remote.ObjectSteps[0]; s.FallbackUsed || s.Fallback != nil || s.Result != "done" || s.CarrierResult != "ack" {
		t.Fatalf("%+v", s)
	}
}

// object_fallback off: the step is the carrier's single try, as before.
func TestObjectFallbackOff(t *testing.T) {
	g := newHandsGame(t, 3)
	g.script["open"] = openFails("failed", 2)
	rep, err := handsRun(t, g, chestPlan(), func(o *RemoteOptions) { o.ObjectFallback = "off" })
	if err == nil || !strings.Contains(err.Error(), "refused: failed") || len(g.ordersOf("cmd open")) != 1 || g.loots != 0 || len(g.gmLines) != 0 {
		t.Fatalf("%v %v", err, g.orders)
	}
	if s := rep.Remote.ObjectSteps[0]; s.FallbackUsed || s.Result != "failed" {
		t.Fatalf("%+v", s)
	}
}

// A game without hands (and a timeout, which is no refusal) keeps the single try.
func TestNoFallbackWithoutHandsOrOnATimeout(t *testing.T) {
	g := carrierGame(t)
	g.script["open"] = openFails("failed", 2)
	if _, err := runCarrier(t, g, chestPlan(), objOpts()); err == nil || len(g.ordersOf("cmd open")) != 1 {
		t.Fatalf("%v %v", err, g.orders)
	}
	h := newHandsGame(t, 3)
	h.script["open"] = []tbm.OrderResult{timeoutOrder("cmd open")}
	rep, err := handsRun(t, h, chestPlan(), nil)
	if err == nil || h.loots != 0 || len(h.ordersOf("cmd open")) != 1 || rep.Remote.ObjectSteps[0].FallbackUsed {
		t.Fatalf("%v %v", err, h.orders)
	}
}

// In a fight the agent does not open, trade or go visible: after the carrier's two failures it waits the fight out and gives up.
func TestNoFallbackInCombat(t *testing.T) {
	g := newHandsGame(t, 3)
	g.script["open"] = openFails("failed", 2)
	g.onTick = func(f *fakeRemote) {
		if len(f.ordersOf("cmd open")) >= 1 {
			f.combatUntil = f.now.Add(10 * time.Minute) // a fight that does not end
		}
	}
	rep, err := handsRun(t, g, chestPlan(), nil)
	if err == nil || !strings.Contains(err.Error(), "does not open the object in a fight") {
		t.Fatalf("%v\n%s", err, notesOf(rep))
	}
	if g.loots != 0 || len(g.trades) != 0 || len(g.gmLines) != 0 {
		t.Fatalf("loots %d trades %v gm %v", g.loots, g.trades, g.gmLines)
	}
	if s := rep.Remote.ObjectSteps[0]; s.Result != "failed" || s.Fallback == nil || s.Fallback.Result != "failed" {
		t.Fatalf("%+v", s)
	}
}

// A fight that begins after the agent opened the chest holds the trade, and so the visibility, until it is over.
func TestVisibleOnlyOutOfCombat(t *testing.T) {
	g := newHandsGame(t, 3)
	g.chestHas = []ItemCount{{Entry: itMedallion, Count: 1}}
	g.script["open"] = openFails("failed", 2)
	var fightEnds time.Time
	g.onLoot = func(h *handsGame) {
		fightEnds = h.now.Add(25 * time.Second)
		h.combatUntil = fightEnds
	}
	rep, err := handsRun(t, g, chestPlan(), nil)
	if err != nil {
		t.Fatalf("%v\n%s", err, notesOf(rep))
	}
	if fmt.Sprint(g.gmLines) != "[.gm visible on .gm visible off]" || g.gmCombat[0] || g.gmCombat[1] {
		t.Fatalf("%v %v", g.gmLines, g.gmCombat)
	}
	if len(g.trades) != 1 {
		t.Fatalf("%v", g.trades)
	}
}

// The trade fails: the item is not handed over, the step fails with the carrier's error and the trade's, and the GM is invisible again.
func TestFailedTradeEndsVisible(t *testing.T) {
	g := newHandsGame(t, 3)
	g.chestHas = []ItemCount{{Entry: itMedallion, Count: 1}}
	g.script["open"] = openFails("failed", 2)
	g.tradeAnswer = []TradeOutcome{{Result: "too_far"}, {Result: "too_far", Detail: "the bot is 14 yd away"}}
	rep, err := handsRun(t, g, chestPlan(), nil)
	if err == nil || !strings.Contains(err.Error(), "trade with Magey ended too_far") || !strings.Contains(err.Error(), "the agent's fallback failed too") {
		t.Fatalf("%v", err)
	}
	if len(g.trades) != 2 || fmt.Sprint(g.gmLines) != "[.gm visible on .gm visible off]" || g.visible {
		t.Fatalf("trades %v gm %v visible %v", g.trades, g.gmLines, g.visible)
	}
	s := rep.Remote.ObjectSteps[0]
	if s.Result != "failed" || s.Fallback == nil || s.Fallback.Trade == nil || s.Fallback.Trade.Result != "too_far" || s.Fallback.Trade.InBags {
		t.Fatalf("%+v", s)
	}
}

// A completed trade that `.bot inv` does not confirm is a failure.
func TestTradeWithoutTheItemInTheCarriersBagsFails(t *testing.T) {
	g := newHandsGame(t, 0)
	g.chestHas = []ItemCount{{Entry: itMedallion, Count: 1}}
	g.script["open"] = openFails("failed", 2)
	g.tradeAnswer = []TradeOutcome{{Result: "completed", Verified: true}} // says so, moves nothing
	_, err := handsRun(t, g, chestPlan(), nil)
	if err == nil || !strings.Contains(err.Error(), "completed but Gni'kiv Medallion (item 7740) is not in its bags") {
		t.Fatalf("%v", err)
	}
}

func TestAgentOpensButTheChestHasNotTheItem(t *testing.T) {
	g := newHandsGame(t, 0)
	g.script["open"] = openFails("locked", 2)
	_, err := handsRun(t, g, chestPlan(), nil)
	if err == nil || !strings.Contains(err.Error(), "is not in its bags") || len(g.trades) != 0 || g.loots != 1 {
		t.Fatalf("%v loots %d", err, g.loots)
	}
}

func TestAgentsOwnOpeningIsTriedTwice(t *testing.T) {
	g := newHandsGame(t, 0)
	g.chestHas = []ItemCount{{Entry: itMedallion, Count: 1}}
	g.script["open"] = openFails("failed", 2)
	g.lootErrs = []error{errors.New("the cast was interrupted")}
	_, err := handsRun(t, g, chestPlan(), nil)
	if err != nil || g.loots != 2 || len(g.trades) != 1 {
		t.Fatalf("%v loots %d", err, g.loots)
	}
}

// ---- the combine and the Keystone ----

func staffPlan(extra ...Step) *Plan {
	steps := []Step{{UseItem: &UseItem{Entry: itMedallion, RequiresItem: &ItemRef{Entry: itShaft}, ExpectItem: &ItemRef{Entry: itStaff, Name: "Staff of Prehistoria"}}}}
	return remotePlan(Leg{Name: "s", To: Point{20, 0, 0}, Steps: append(steps, extra...)})
}

func TestAgentCombinesWhenTheCarrierCannotUseTheMedallion(t *testing.T) {
	g := newHandsGame(t, 3)
	g.bag("Magey")[itMedallion], g.bag("Magey")[itShaft] = 1, 1
	g.mine[itMedallion], g.mine[itShaft] = 1, 1
	g.made[itMedallion] = itStaff
	g.script["use"] = []tbm.OrderResult{errOrder("cmd use", "failed"), errOrder("cmd use", "failed")}
	rep, err := handsRun(t, g, staffPlan(), nil)
	if err != nil {
		t.Fatalf("%v\n%s", err, notesOf(rep))
	}
	if fmt.Sprint(g.useItems) != "[7740]" || fmt.Sprint(g.trades) != "[Magey 7733x1]" || g.bag("Magey")[itStaff] != 1 {
		t.Fatalf("use %v trades %v bag %v", g.useItems, g.trades, g.bag("Magey"))
	}
	if s := rep.Remote.ObjectSteps[0]; s.Kind != "use_item" || !s.FallbackUsed || s.Fallback.Did != "use_item" || s.Result != "done" {
		t.Fatalf("%+v", s)
	}
}

func TestCombineFallbackNeedsBothPartsInTheAgentsBags(t *testing.T) {
	g := newHandsGame(t, 3)
	g.bag("Magey")[itMedallion], g.bag("Magey")[itShaft] = 1, 1
	g.mine[itMedallion] = 1 // the Shaft is not the agent's
	g.script["use"] = []tbm.OrderResult{errOrder("cmd use", "failed"), errOrder("cmd use", "failed")}
	_, err := handsRun(t, g, staffPlan(), nil)
	if err == nil || !strings.Contains(err.Error(), "the agent does not take this step over") || len(g.useItems) != 0 || len(g.trades) != 0 {
		t.Fatalf("%v", err)
	}
}

func keystonePlan(extra ...Step) *Plan {
	st := Step{UseObject: &UseObject{ObjectRef: ObjectRef{Entry: goKeystone}, RequiresItem: &ItemRef{Entry: itStaff, Name: "Staff of Prehistoria"}}}
	return remotePlan(Leg{Name: "k", To: Point{20, 0, 0}, Steps: append([]Step{st}, extra...)})
}

func keystoneGame(t *testing.T) *handsGame {
	g := newHandsGame(t, 3)
	k := chestObj()
	k.GUID, k.Entry = keystoneGUID, goKeystone
	g.gobjs = []objects.GameObject{k}
	return g
}

func TestAgentOpensTheKeystoneWithTheStaffItHolds(t *testing.T) {
	g := keystoneGame(t)
	g.bag("Magey")[itStaff] = 1
	g.mine[itStaff] = 1
	g.script["open"] = openFails("failed", 2)
	rep, err := handsRun(t, g, keystonePlan(), nil)
	if err != nil || g.useObjs != 1 || len(g.trades) != 0 || len(g.gmLines) != 0 {
		t.Fatalf("%v use %d trades %v\n%s", err, g.useObjs, g.trades, notesOf(rep))
	}
	if s := rep.Remote.ObjectSteps[0]; !s.FallbackUsed || s.Fallback.Did != "use_object" {
		t.Fatalf("%+v", s)
	}
	// The agent holds the Staff, the carrier does not: the agent opens it without asking the carrier at all.
	g = keystoneGame(t)
	g.mine[itStaff] = 1
	rep, err = handsRun(t, g, keystonePlan(), nil)
	if err != nil || g.useObjs != 1 || len(g.ordersOf("cmd open")) != 0 {
		t.Fatalf("%v %v", err, g.orders)
	}
}

func TestKeystoneFallbackLeavesTheStaffToALaterStep(t *testing.T) {
	g := keystoneGame(t)
	g.bag("Magey")[itStaff] = 1
	g.mine[itStaff] = 1
	g.script["open"] = openFails("failed", 2)
	// A later step that uses the Staff on a bot: the agent keeps its hands off.
	later := Step{UseObject: &UseObject{ObjectRef: ObjectRef{Entry: goSeal}, RequiresItem: &ItemRef{Entry: itStaff}}}
	_, err := handsRun(t, g, keystonePlan(later), nil)
	if err == nil || g.useObjs != 0 || !strings.Contains(err.Error(), "the agent does not take this step over") {
		t.Fatalf("%v use %d", err, g.useObjs)
	}
}

// ---- the approach ----

// The order goes to the carrier only after the party was walked to the chest, told to stay, and the carrier stood still near the chest.
func TestApproachWaitsForAStillCarrier(t *testing.T) {
	g := carrierGame(t)
	var stayAt time.Time
	g.onTick = func(f *fakeRemote) {
		if i := f.indexOf("cmd stay"); i >= 0 && stayAt.IsZero() {
			stayAt = f.orderAt[i]
		}
		// The carrier is still running about at the chest for 6 s after the stay order.
		if !stayAt.IsZero() && f.now.Before(stayAt.Add(6*time.Second)) {
			f.party[2].X += float32(len(f.trace)%2*2 - 1)
		}
	}
	rep, err := runCarrier(t, g, chestPlan(), objOpts())
	if err != nil {
		t.Fatalf("%v\n%s", err, notesOf(rep))
	}
	mv, stay, open := g.indexOf("cmd move"), g.indexOf("cmd stay"), g.indexOf("cmd open")
	if !(mv >= 0 && mv < stay && stay < open) {
		t.Fatalf("%v", g.orders)
	}
	if wait := g.orderAt[open].Sub(g.orderAt[stay]); wait < 6*time.Second || wait > 10*time.Second {
		t.Fatalf("the open went %v after the stay", wait)
	}
	// The walk went to the chest's stand point: 3 yd short of (30,0,0) on the line from the lead bot.
	f := strings.Fields(g.orders[stay-1]) // the move right before the stay
	if g.orders[stay-1][:8] != "cmd move" || f[2] != "27.00" || f[3] != "0.00" {
		t.Fatalf("%v", g.orders[mv])
	}
	s := rep.Remote.ObjectSteps[0]
	if !strings.Contains(s.Approach, "Magey still") || s.Result != "done" {
		t.Fatalf("%+v", s)
	}
	if g.indexOf("cmd follow") < open {
		t.Fatalf("no follow after the order: %v", g.orders)
	}
}

// A carrier that never settles gets the order after the wait, and a note says so.
func TestApproachGivesUpOnACarrierThatNeverStops(t *testing.T) {
	g := carrierGame(t)
	g.onTick = func(f *fakeRemote) {
		if f.indexOf("cmd stay") >= 0 {
			f.party[2].X += float32(len(f.trace)%2*2 - 1)
		}
	}
	rep, err := runCarrier(t, g, chestPlan(), objOpts())
	if err != nil {
		t.Fatalf("%v\n%s", err, notesOf(rep))
	}
	stay, open := g.indexOf("cmd stay"), g.indexOf("cmd open")
	if wait := g.orderAt[open].Sub(g.orderAt[stay]); wait < objectApproachWait || wait > objectApproachWait+3*time.Second {
		t.Fatalf("%v", wait)
	}
	if !strings.Contains(notesOf(rep), "the order goes out anyway, with its own walk") {
		t.Fatalf("%s", notesOf(rep))
	}
}

// The plan's own stand point is where the party is walked.
func TestApproachUsesThePlansStandPoint(t *testing.T) {
	g := carrierGame(t)
	st := lootChestStep()
	st.LootObject.Stand = &Point{28, 1, 0}
	if rep, err := runCarrier(t, g, remotePlan(Leg{Name: "c", To: Point{20, 0, 0}, Steps: []Step{st}}), objOpts()); err != nil {
		t.Fatalf("%v %s", err, notesOf(rep))
	}
	stay := g.indexOf("cmd stay")
	f := strings.Fields(g.orders[stay-1])
	if f[1] != "move" || f[2] != "28.00" || f[3] != "1.00" {
		t.Fatalf("%v", g.orders)
	}
}

// A step that is skipped (the carrier has the item) walks nobody.
func TestApproachNotForASkippedStep(t *testing.T) {
	g := carrierGame(t)
	g.bag("Magey")[itMedallion] = 1
	if _, err := runCarrier(t, g, chestPlan(), objOpts()); err != nil || len(g.ordersOf("cmd stay")) != 1 { // the end of the run's only
		t.Fatalf("%v %v", err, g.orders)
	}
}
