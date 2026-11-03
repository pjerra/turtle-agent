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

// Object steps by entry and game-object waits on cmd state (remote_entry.go; TortoiseBots #933).

// worldObj is a game object or a corpse the fake module knows, which the agent does not see.
type worldObj struct {
	guid   packet.GUID
	entry  uint32
	at     Point
	state  string // "ready", "active", "alternative"
	corpse bool
}

// entryGame is a nearGame (a Follower, so keep-near is observable) whose module answers `cmd state` and the entry form of `cmd
// open|loot|use` the way TortoiseBots #933 does: the nearest object of the entry within 30 yd of the point, `too-far` for a point beyond
// reach, `not-found` for nothing, one ACTION_TARGET before the final answer. With modern false it is an older module: `invalid`.
type entryGame struct {
	*nearGame
	modern   bool
	reach    float64
	world    []*worldObj
	stateErr string // when set, `cmd state` is refused with this code
	silent   bool   // `cmd state` is never answered
	asked    []string
	targets  []tbm.Target
}

func newEntryGame(t *testing.T, level int, modern bool) *entryGame {
	return &entryGame{nearGame: newNearGame(t, level), modern: modern, reach: 150}
}

func (g *entryGame) record(command string) int {
	g.orders = append(g.orders, command)
	g.orderAt = append(g.orderAt, g.now)
	return len(g.orders) - 1
}

// find is the module's search: entry, the point (30 yd) or the lead bot (reach), the nearest, the lowest GUID on a tie.
func (g *entryGame) find(entry uint32, at *Point, corpse bool) (*worldObj, string) {
	l := g.lead()
	from := Point{l.X, l.Y, l.Z}
	radius := g.reach
	if at != nil {
		if dist3(*at, from) > g.reach {
			return nil, "too-far"
		}
		from, radius = *at, 30
	}
	var best *worldObj
	bestD := 0.0
	for _, o := range g.world {
		if o.entry != entry || (o.corpse && !corpse) {
			continue
		}
		d := dist3(o.at, from)
		if d > radius || (best != nil && (d > bestD || (d == bestD && o.guid > best.guid))) {
			continue
		}
		best, bestD = o, d
	}
	if best == nil {
		return nil, "not-found"
	}
	return best, ""
}

func (g *entryGame) Order(ctx context.Context, command string) (tbm.OrderResult, error) {
	f := strings.Fields(command)
	verb, at := f[1], -1
	for i, a := range f {
		if strings.HasPrefix(a, "entry:") {
			at = i
		}
	}
	if verb == "state" {
		g.record(command)
		g.asked = append(g.asked, command)
		g.advance(300 * time.Millisecond)
		if !g.modern {
			return errOrder(command, "invalid"), nil
		}
		if g.silent {
			return tbm.OrderResult{Command: command, Intent: "cmd state", Kind: tbm.KindTimeout, Lines: []string{}}, nil
		}
		if g.stateErr != "" {
			return errOrder(command, g.stateErr), nil
		}
		entry, pt, _ := entryFormOf(command)
		o, code := g.find(entry, pt, false)
		if code != "" {
			return errOrder(command, code), nil
		}
		payload := fmt.Sprintf("guid:%d,entry:%d,type:0,state:%s,loot:not-ready,flags:none", uint64(o.guid), o.entry, o.state)
		r := okOrder(command, "party", payload)
		r.Ack.Scope, r.Final = "party", "TBM:ACTION_ACK|cmd state|party|1|"+payload
		st, _ := tbm.ParseGOState(payload)
		r.State = &st
		return r, nil
	}
	if at < 0 {
		return g.nearGame.Order(ctx, command)
	}
	if !g.modern {
		g.record(command)
		return errOrder(command, "invalid"), nil
	}
	entry, pt, _ := entryFormOf(command)
	o, code := g.find(entry, pt, verb == "loot")
	if code != "" {
		g.record(command)
		return errOrder(command, code), nil
	}
	tg := tbm.Target{Intent: "cmd " + verb, Bot: f[3], GUID: o.guid, Entry: entry}
	if verb == "use" {
		tg.Bot = f[3]
	}
	g.targets = append(g.targets, tg)
	f[at] = tbm.GUIDArg(o.guid)
	idx := len(g.orders)
	res, err := g.nearGame.Order(ctx, strings.Join(f, " "))
	g.orders[idx] = command
	res.Command = command
	res.Target = &tg
	return res, err
}

func entryRun(t *testing.T, g *entryGame, plan *Plan) (Report, error) {
	t.Helper()
	g.agent = Point{-300, 0, 0} // far behind the party's start, out of any view
	cfg := DefaultConfig()
	cfg.PostFight, cfg.AddsWait = 2*time.Second, 4*time.Second
	cfg.Setup = []string{"gm visible off"}
	d := NewRemoteDriver(plan, g, cfg, objOpts(), g.clock())
	err := d.Run(context.Background(), 0)
	rep, ended := d.Status()
	if !ended {
		t.Fatal("not ended")
	}
	return rep, err
}

var (
	chestAt    = Point{30, 0, 0}
	keystoneAt = Point{20, 0, 0}
)

func entryChestStep() Step {
	return Step{LootObject: &LootObject{ObjectRef: ObjectRef{Entry: goChest, Near: &chestAt}, ExpectItem: &ItemRef{Entry: itMedallion, Name: "Gni'kiv Medallion"}}}
}

func entryChestGame(t *testing.T, level int, modern bool) *entryGame {
	g := newEntryGame(t, level, modern)
	g.chestLoot = []tbm.ItemCount{{Entry: itMedallion, Count: 1}}
	g.world = []*worldObj{{guid: chestGUID, entry: goChest, at: chestAt, state: "ready"}}
	return g
}

func hasNote(rep Report, sub string) bool { return strings.Contains(notesOf(rep), sub) }

func TestEntryTargetsOpenAChestTheAgentNeverSees(t *testing.T) {
	g := entryChestGame(t, 3, true) // a GM account, 300 yd behind, seeing nothing
	plan := remotePlan(Leg{Name: "c", To: Point{200, 0, 0}, Steps: []Step{entryChestStep()}})
	plan.Legs[0].Steps[0].LootObject.Near = &Point{200, 0, 0}
	g.world[0].at = Point{200, 0, 0}
	rep, err := entryRun(t, g, plan)
	if err != nil {
		t.Fatalf("%v\n%s", err, notesOf(rep))
	}
	want := "cmd open entry:123329@200.00,0.00,0.00 Magey"
	if g.indexOf(want) < 0 || g.indexOf("cmd open 0x") >= 0 {
		t.Fatalf("want %q in %v", want, g.orders)
	}
	// The agent was not brought up: no .appear, no walk, so run_gm_assists is 0.
	r := rep.Remote
	if len(g.appears) != 0 || len(g.walks) != 0 || r.RunGMAssists != 0 || r.AgentWalks != 0 || r.GMCommands != 0 || fmt.Sprint(rep.SetupShortcuts) != "[gm visible off]" {
		t.Fatalf("appears %v walks %v %+v setup %v", g.appears, g.walks, r, rep.SetupShortcuts)
	}
	if r.EntryTargets != "on" || !hasNote(rep, "run_gm_assists stays 0") {
		t.Fatalf("%q\n%s", r.EntryTargets, notesOf(rep))
	}
	// The module's choice is in the step report.
	s := r.ObjectSteps[0]
	if s.Result != "done" || s.TargetGUID != tbm.GUIDArg(chestGUID) || !strings.Contains(s.Orders[1].Target, "entry 123329") || fmt.Sprint(s.Gained) != "[7740 x1 copper 5]" {
		t.Fatalf("%+v", s)
	}
}

func TestEntryTargetsGUIDIsTheRetryWhenTheServerFindsNothing(t *testing.T) {
	g := entryChestGame(t, 3, true)
	g.world[0].at = Point{100, 0, 0} // 70 yd from the plan's point: the search around the point finds nothing
	g.gobjs = []objects.GameObject{chestObj()}
	plan := remotePlan(Leg{Name: "c", To: Point{20, 0, 0}, Steps: []Step{entryChestStep()}})
	rep, err := entryRun(t, g, plan)
	// The by-entry order is refused (not-found); the agent's own GUID of the chest then goes out. (The fake only answers the GUID order
	// for the chest it knows by GUID.)
	if err != nil {
		t.Fatalf("%v\n%s", err, notesOf(rep))
	}
	e, gd := g.indexOf("cmd open entry:123329@30.00,0.00,0.00"), g.indexOf("cmd open "+tbm.GUIDArg(chestGUID))
	if e < 0 || gd < e || !hasNote(rep, "nothing by entry 123329; trying the GUID") {
		t.Fatalf("%v", g.orders)
	}
}

func TestEntryTargetsNotFoundNamesTheEntryAndPoint(t *testing.T) {
	g := entryChestGame(t, 3, true)
	g.world = nil
	plan := remotePlan(Leg{Name: "c", To: Point{20, 0, 0}, Steps: []Step{entryChestStep()}})
	rep, err := entryRun(t, g, plan)
	if err == nil || !strings.Contains(err.Error(), "not-found") || !strings.Contains(err.Error(), "entry 123329 within 30 yd of 30 0 0") {
		t.Fatalf("%v", err)
	}
	// One order, no GUID to retry with (the agent saw nothing), and the loot method went back.
	if n := len(g.ordersOf("cmd open")); n != 1 || g.indexOf("cmd lootmethod group") < 0 || rep.Remote.ObjectSteps[0].Result != "failed" {
		t.Fatalf("%v %+v", g.orders, rep.Remote.ObjectSteps)
	}
}

func TestEntryTargetsTooFarWalksThePartyOnceThenRetries(t *testing.T) {
	g := entryChestGame(t, 3, true)
	g.script["open"] = []tbm.OrderResult{errOrder("cmd open", "too-far")}
	plan := remotePlan(Leg{Name: "c", To: Point{20, 0, 0}, Steps: []Step{entryChestStep()}})
	rep, err := entryRun(t, g, plan)
	if err != nil || len(g.ordersOf("cmd open")) != 2 || !hasNote(rep, "walking the party toward it") {
		t.Fatalf("%v %v", err, g.orders)
	}
	// Still too far after the one walk: the step fails (an order by GUID would have had a second walk).
	g = entryChestGame(t, 3, true)
	for i := 0; i < 4; i++ {
		g.script["open"] = append(g.script["open"], errOrder("cmd open", "too-far"))
	}
	if _, err := entryRun(t, g, plan); err == nil || !strings.Contains(err.Error(), "too-far") || len(g.ordersOf("cmd open")) != 2 {
		t.Fatalf("%v %v", err, g.ordersOf("cmd open"))
	}
}

func TestEntryTargetsBossCorpseNeedsNoViewOfIt(t *testing.T) {
	g := newEntryGame(t, 3, true)
	g.recipe = combineRecipe(g.fakeCarrier)
	g.bag("Magey")[itMedallion] = 1
	boss := g.addMob(6910, Point{50, 0, 0})
	g.world = []*worldObj{{guid: boss.guid, entry: 6910, at: Point{50, 0, 0}, corpse: true}}
	rep, err := entryRun(t, g, bossPlan(true))
	if err != nil {
		t.Fatalf("%v\n%s", err, notesOf(rep))
	}
	want := fmt.Sprintf("cmd loot entry:6910@50.00,0.00,0.00 Magey %d", itShaft)
	if g.indexOf(want) < 0 || g.indexOf("cmd loot 0x") >= 0 {
		t.Fatalf("want %q in %v", want, g.orders)
	}
	if len(g.appears) != 0 || rep.Remote.RunGMAssists != 0 || g.bag("Magey")[itStaff] != 1 {
		t.Fatalf("appears %v %+v %v", g.appears, rep.Remote, g.bag("Magey"))
	}
	if s := rep.Remote.ObjectSteps[0]; s.Kind != "boss loot_item" || s.TargetGUID != tbm.GUIDArg(boss.guid) {
		t.Fatalf("%+v", s)
	}
}

// ---- cmd state ----

func sealAtEntry() *Point { return &Point{20, 60, 0} }

func entrySealStep(timeout int) []Step {
	return []Step{
		{UseObject: &UseObject{ObjectRef: ObjectRef{Entry: goKeystone}, RequiresItem: &ItemRef{Entry: itStaff, Name: "Staff of Prehistoria"}}},
		{WaitFor: &WaitFor{GameObject: &ObjectState{ObjectRef: ObjectRef{Entry: goSeal, Near: sealAtEntry()}, State: "active"}, TimeoutS: timeout}},
	}
}

func entryKeystoneGame(t *testing.T, level int, modern bool) *entryGame {
	g := newEntryGame(t, level, modern)
	g.bag("Magey")[itStaff] = 1
	g.world = []*worldObj{
		{guid: keystoneGUID, entry: goKeystone, at: keystoneAt, state: "ready"},
		{guid: 0x1F1000000000AAA3, entry: goSeal, at: *sealAtEntry(), state: "ready"},
	}
	return g
}

func TestEntryTargetsWaitPollsCmdStateUntilActive(t *testing.T) {
	g := entryKeystoneGame(t, 3, true)
	g.fakeRemote.onTick = func(*fakeRemote) {
		if len(g.asked) >= 4 { // the Seal opens a few polls after the Keystone was used
			g.world[1].state = "active"
		}
	}
	plan := remotePlan(Leg{Name: "k", To: Point{20, 0, 0}, Steps: entrySealStep(60)}, Leg{Name: "i", To: Point{20, 20, 0}})
	rep, err := entryRun(t, g, plan)
	if err != nil {
		t.Fatalf("%v\n%s", err, notesOf(rep))
	}
	polls := len(g.asked) - 1 // the first is the start-of-run probe
	if polls < 3 || polls > 6 || g.asked[1] != "cmd state entry:124372@20.00,60.00,0.00" || rep.Remote.StatePolls != polls {
		t.Fatalf("polls %d state_polls %d %v", polls, rep.Remote.StatePolls, g.asked)
	}
	if g.indexOf("cmd open entry:124371 Magey") < 0 || !hasNote(rep, "cmd state says game object 124372 near 20 60 0 is active") || hasNote(rep, "cannot see") {
		t.Fatalf("%v\n%s", g.orders, notesOf(rep))
	}
	if len(g.appears) != 0 || len(g.walks) != 0 || rep.Remote.RunGMAssists != 0 {
		t.Fatalf("appears %v walks %v", g.appears, g.walks)
	}
}

func TestEntryTargetsWaitNotFoundUntilItSpawns(t *testing.T) {
	g := entryKeystoneGame(t, 0, true)
	seal := g.world[1]
	g.world = g.world[:1] // the Seal is not spawned yet
	g.fakeRemote.onTick = func(*fakeRemote) {
		if len(g.asked) >= 5 && len(g.world) == 1 {
			seal.state = "active"
			g.world = append(g.world, seal)
		}
	}
	plan := remotePlan(Leg{Name: "k", To: Point{20, 0, 0}, Steps: entrySealStep(60)})
	rep, err := entryRun(t, g, plan)
	if err != nil || len(g.asked) < 5 {
		t.Fatalf("%v asked %d\n%s", err, len(g.asked), notesOf(rep))
	}
}

func TestEntryTargetsWaitFailsAtItsTimeoutNamingTheLastAnswer(t *testing.T) {
	g := entryKeystoneGame(t, 3, true)
	g.world[1].state = "ready" // never opens
	plan := remotePlan(Leg{Name: "k", To: Point{20, 0, 0}, Steps: entrySealStep(20)})
	rep, err := entryRun(t, g, plan)
	if err == nil || !strings.Contains(err.Error(), "waited 20 s for game object 124372") || !strings.Contains(err.Error(), "last cmd state answer: state:ready") {
		t.Fatalf("%v", err)
	}
	if n := len(g.asked); n < 8 || n > 14 { // every 2 s for 20 s, plus the probe
		t.Fatalf("%d polls", n)
	}
	// Never spawned: not-found to the end.
	g = entryKeystoneGame(t, 3, true)
	g.world = g.world[:1]
	if _, err := entryRun(t, g, plan); err == nil || !strings.Contains(err.Error(), "last cmd state answer: not-found") {
		t.Fatalf("%v", err)
	}
	_ = rep
}

// entrySealGoneStep is the keystone and a wait for the Seal that may be gone_ok.
func entrySealGoneStep(timeout int, goneOK bool) []Step {
	st := entrySealStep(timeout)
	st[1].WaitFor.GameObject.GoneOK = goneOK
	return st
}

// gone_ok: the object is answered ready, then not-found for GoneConfirm (a script that removes it): done, with the note, long before the
// timeout.
func TestEntryTargetsWaitGoneAfterSeenIsDone(t *testing.T) {
	g := entryKeystoneGame(t, 3, true)
	g.fakeRemote.onTick = func(*fakeRemote) {
		if len(g.asked) >= 4 && len(g.world) == 2 { // the Seal was answered ready a few times, then the script removed it
			g.world = g.world[:1]
		}
	}
	plan := remotePlan(Leg{Name: "k", To: Point{20, 0, 0}, Steps: entrySealGoneStep(60, true)}, Leg{Name: "i", To: Point{20, 20, 0}})
	rep, err := entryRun(t, g, plan)
	if err != nil {
		t.Fatalf("%v\n%s", err, notesOf(rep))
	}
	if !hasNote(rep, "is gone after it was seen: counted as done (gone_ok)") {
		t.Fatalf("%s", notesOf(rep))
	}
}

// A blip (the object answered again between two misses) starts the confirmation over: the misses before it do not add up.
func TestEntryTargetsWaitGoneBlipRestartsTheConfirmation(t *testing.T) {
	polls := func(present func(n int) bool) int {
		g := entryKeystoneGame(t, 3, true)
		seal := g.world[1]
		g.fakeRemote.onTick = func(*fakeRemote) {
			g.world = g.world[:1]
			if present(len(g.asked)) {
				g.world = append(g.world, seal)
			}
		}
		plan := remotePlan(Leg{Name: "k", To: Point{20, 0, 0}, Steps: entrySealGoneStep(60, true)}, Leg{Name: "i", To: Point{20, 20, 0}})
		rep, err := entryRun(t, g, plan)
		if err != nil || !hasNote(rep, "counted as done (gone_ok)") {
			t.Fatalf("%v\n%s", err, notesOf(rep))
		}
		return len(g.asked)
	}
	steady := polls(func(n int) bool { return n < 4 })                   // gone from the fourth poll on
	blip := polls(func(n int) bool { return n < 4 || n == 5 || n == 6 }) // gone at 4, found again at 5-6, gone from 7
	// Without the reset the miss at poll 4 would count on to the first miss after the blip.
	if blip < steady+3 {
		t.Fatalf("steady %d polls, with a blip %d", steady, blip)
	}
}

// A refusal that is not a not-found (the point is out of the bots' reach) is no proof of a gone object, however long it lasts.
func TestEntryTargetsWaitGoneNeedsTheModuleToLookAndFindNothing(t *testing.T) {
	g := entryKeystoneGame(t, 3, true)
	g.fakeRemote.onTick = func(*fakeRemote) {
		if len(g.asked) >= 4 {
			g.world = g.world[:1]
			g.reach = 1 // every poll now answers too-far
		}
	}
	plan := remotePlan(Leg{Name: "k", To: Point{20, 0, 0}, Steps: entrySealGoneStep(20, true)})
	_, err := entryRun(t, g, plan)
	if err == nil || !strings.Contains(err.Error(), "waited 20 s for game object 124372") || !strings.Contains(err.Error(), "too-far") {
		t.Fatalf("%v", err)
	}
}

// Sightings are per target: a Seal seen at one point says nothing of the same entry at another.
func TestEntryTargetsWaitGoneSightingsAreKeyedByTarget(t *testing.T) {
	var l sightings
	here := ObjectRef{Entry: 18895, Near: &Point{-242, 2159, 90}}
	near := ObjectRef{Entry: 18895, Near: &Point{-238, 2159, 90}}
	far := ObjectRef{Entry: 18895, Near: &Point{-100, 2159, 90}}
	other := ObjectRef{Entry: 18896, Near: &Point{-242, 2159, 90}}
	l.add(&here)
	if !l.has(&here) || !l.has(&near) || l.has(&far) || l.has(&other) {
		t.Fatalf("%v", l)
	}
	l.add(&near)
	if len(l) != 1 {
		t.Fatalf("a target within SightYd is one: %v", l)
	}
}

// ... never answered found, it is not gone but not there yet: the wait runs out its timeout.
func TestEntryTargetsWaitNeverSeenIsNotGone(t *testing.T) {
	g := entryKeystoneGame(t, 3, true)
	g.world = g.world[:1]
	plan := remotePlan(Leg{Name: "k", To: Point{20, 0, 0}, Steps: entrySealGoneStep(20, true)})
	_, err := entryRun(t, g, plan)
	if err == nil || !strings.Contains(err.Error(), "waited 20 s for game object 124372") || !strings.Contains(err.Error(), "last cmd state answer: not-found") {
		t.Fatalf("%v", err)
	}
}

// ... and without gone_ok the same removal is a timeout.
func TestEntryTargetsWaitGoneWithoutTheFlagTimesOut(t *testing.T) {
	g := entryKeystoneGame(t, 3, true)
	g.fakeRemote.onTick = func(*fakeRemote) {
		if len(g.asked) >= 4 && len(g.world) == 2 {
			g.world = g.world[:1]
		}
	}
	plan := remotePlan(Leg{Name: "k", To: Point{20, 0, 0}, Steps: entrySealGoneStep(20, false)})
	_, err := entryRun(t, g, plan)
	if err == nil || !strings.Contains(err.Error(), "waited 20 s for game object 124372") {
		t.Fatalf("%v", err)
	}
}

func TestEntryTargetsWaitsForTheAlternativeState(t *testing.T) {
	for in, want := range map[string]string{"active": "active", "ready": "ready", "active_alt": "alternative"} {
		if got := wantedState(in); got != want {
			t.Errorf("%s: %s", in, got)
		}
	}
}

// ---- the capability probe ----

func TestEntryProbeAnswersThatTurnItOn(t *testing.T) {
	for _, code := range []string{"", "not-found", "too-far"} {
		g := entryKeystoneGame(t, 3, true)
		g.stateErr = code
		plan := remotePlan(Leg{Name: "k", To: Point{20, 0, 0}, Steps: entrySealStep(5)[:1]})
		rep, err := entryRun(t, g, plan)
		if err != nil || rep.Remote.EntryTargets != "on" || len(g.appears) != 0 {
			t.Fatalf("%q: %v %q\n%s", code, err, rep.Remote.EntryTargets, notesOf(rep))
		}
	}
}

func TestEntryProbeAnswersThatTurnItOff(t *testing.T) {
	for name, set := range map[string]func(*entryGame){
		"invalid": func(g *entryGame) { g.modern = false },
		"denied":  func(g *entryGame) { g.stateErr = "denied" },
		"silence": func(g *entryGame) { g.silent = true },
	} {
		g := entryKeystoneGame(t, 3, true)
		set(g)
		plan := remotePlan(Leg{Name: "k", To: Point{20, 0, 0}, Steps: entrySealStep(5)[:1]})
		g.onAppear = func(n *nearGame) {
			n.gobjs = []objects.GameObject{{GUID: keystoneGUID, Entry: goKeystone, X: 20, Y: 0, Z: 0, Distance: 3, State: "ready"}}
		}
		rep, err := entryRun(t, g, plan)
		if err != nil || !strings.HasPrefix(rep.Remote.EntryTargets, "off: ") || g.indexOf("cmd open "+tbm.GUIDArg(keystoneGUID)) < 0 || g.indexOf("cmd open entry:") >= 0 {
			t.Fatalf("%s: %v %q %v\n%s", name, err, rep.Remote.EntryTargets, g.orders, notesOf(rep))
		}
		// The old behaviour is kept: the agent was brought up with .appear (a GM assist during the run).
		if len(g.appears) == 0 || rep.Remote.RunGMAssists == 0 {
			t.Fatalf("%s: appears %v %+v", name, g.appears, rep.Remote)
		}
	}
}

func TestEntryOffKeepsTheUncheckedWait(t *testing.T) {
	g := entryKeystoneGame(t, 3, false)
	g.onAppear = func(n *nearGame) {
		n.gobjs = []objects.GameObject{{GUID: keystoneGUID, Entry: goKeystone, X: 20, Y: 0, Z: 0, Distance: 3, State: "ready"}}
	}
	plan := remotePlan(Leg{Name: "k", To: Point{20, 0, 0}, Steps: entrySealStep(60)}, Leg{Name: "i", To: Point{20, 20, 0}})
	rep, err := entryRun(t, g, plan)
	if err != nil || !hasNote(rep, "cannot see game object 124372") || rep.Remote.StatePolls != 0 {
		t.Fatalf("%v\n%s", err, notesOf(rep))
	}
}

func TestEntryProbeOnlyWhenThePlanHasObjectSteps(t *testing.T) {
	g := entryKeystoneGame(t, 3, true)
	if _, err := entryRun(t, g, remotePlan(Leg{Name: "a", To: Point{20, 0, 0}})); err != nil || len(g.asked) != 0 {
		t.Fatalf("%v %v", err, g.asked)
	}
	// A game that gives no orders is not probed either.
	r := newFakeRemote(t)
	if rep, err := runRemote(t, r, remotePlan(Leg{Name: "a", To: Point{20, 0, 0}}), opts(), nil); err != nil || rep.Remote.EntryTargets != "" {
		t.Fatalf("%v %q", err, rep.Remote.EntryTargets)
	}
}

func TestEntryTargetsKeepNearWhenAStepHasNoEntry(t *testing.T) {
	g := entryKeystoneGame(t, 3, true)
	wait := Step{WaitFor: &WaitFor{GameObject: &ObjectState{ObjectRef: ObjectRef{Near: sealAtEntry()}, State: "active"}, TimeoutS: 5}}
	plan := remotePlan(Leg{Name: "k", To: Point{200, 0, 0}, Steps: append(entrySealStep(5)[:1], wait)})
	rep, err := entryRun(t, g, plan)
	_ = err // the Near-only wait is the old, unchecked one; the point is that the agent is still kept near
	if !hasNote(rep, "1 object step(s) of the plan name no entry") || len(g.appears) == 0 {
		t.Fatalf("appears %v\n%s", g.appears, notesOf(rep))
	}
}

// entryHands is a handsGame (the agent can act) over a module that takes objects by entry.
type entryHands struct {
	*handsGame
	eg *entryGame
}

func (g entryHands) Order(ctx context.Context, command string) (tbm.OrderResult, error) {
	return g.eg.Order(ctx, command)
}

// The fallback when the agent never saw the chest: the carrier's open by entry fails twice, the agent walks up and opens it with the GUID
// the module named in its ACTION_TARGET line.
func TestEntryTargetsFallbackUsesTheModulesGUID(t *testing.T) {
	hg := newHandsGame(t, 3)
	hg.gobjs = nil
	hg.chestHas = []ItemCount{{Entry: itMedallion, Count: 1}}
	hg.script["open"] = openFails("failed", 2)
	eg := &entryGame{nearGame: hg.nearGame, modern: true, reach: 150,
		world: []*worldObj{{guid: chestGUID, entry: goChest, at: chestAt, state: "ready"}}}
	g := entryHands{hg, eg}
	plan := remotePlan(Leg{Name: "c", To: Point{20, 0, 0}, Steps: []Step{entryChestStep()}})
	hg.agent = Point{-300, 0, 0}
	cfg := DefaultConfig()
	cfg.PostFight, cfg.AddsWait = 2*time.Second, 4*time.Second
	cfg.Setup = []string{"gm visible off"}
	d := NewRemoteDriver(plan, g, cfg, objOpts(), hg.clock())
	if err := d.Run(context.Background(), 0); err != nil {
		rep, _ := d.Status()
		t.Fatalf("%v\n%s", err, notesOf(rep))
	}
	rep, _ := d.Status()
	s := rep.Remote.ObjectSteps[0]
	if hg.loots != 1 || !s.FallbackUsed || s.Fallback == nil || s.Fallback.Result != "done" || s.TargetGUID != tbm.GUIDArg(chestGUID) {
		t.Fatalf("loots %d %+v", hg.loots, s)
	}
	if hg.indexOf("cmd open entry:123329@30.00,0.00,0.00 Magey") < 0 || hg.bag("Magey")[itMedallion] != 1 {
		t.Fatalf("%v", hg.orders)
	}
}

// A resumed run with entry targets: keep-near is off, but a boss killed before this driver started is in no kill record and no ledger, so
// the agent is brought up once for the boss step and its view of the corpse is the evidence (one counted GM assist).
func TestEntryTargetsResumedBossBringsTheAgentUpOnce(t *testing.T) {
	g := entryChestGame(t, 3, true)
	g.onAppear = func(n *nearGame) { n.units = []objects.Unit{corpseOf(777, Point{62, 3, 0})} }
	plan := remotePlan(Leg{Name: "b", To: Point{20, 0, 0}, Steps: []Step{entryChestStep()}, Boss: &Boss{Entry: 777, Position: Point{60, 0, 0}}})
	rep, err := entryRun(t, g, plan)
	if err != nil {
		t.Fatalf("%v\n%s", err, notesOf(rep))
	}
	if fmt.Sprint(g.appears) != "[Tanky]" || rep.Remote.RunGMAssists != 1 || !hasNote(rep, "its corpse is in the agent's view") || g.indexOf("cmd open entry:") < 0 {
		t.Fatalf("appears %v %+v\n%s", g.appears, rep.Remote, notesOf(rep))
	}
	// No corpse in the agent's view even then: the boss is not counted dead.
	g = entryChestGame(t, 3, true)
	if _, err := entryRun(t, g, plan); err == nil || !strings.Contains(err.Error(), "no living [777]") || len(g.appears) != 1 {
		t.Fatalf("%v appears %v", err, g.appears)
	}
}
