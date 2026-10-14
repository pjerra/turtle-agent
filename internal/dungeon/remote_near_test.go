package dungeon

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/pjerra/turtle-agent/internal/objects"
	"github.com/pjerra/turtle-agent/internal/tbm"
)

// The agent kept near the party (remote_near.go).

// nearGame is a fakeCarrier whose game is also a Follower: the agent's character stands at agent.
type nearGame struct {
	*fakeCarrier
	agent        Point
	agentMap     uint32
	level        int
	known        bool
	appears      []string
	appearErr    []error // errors the next appears answer with
	walks        []Point
	walkErr      error
	probes       int
	onAppear     func(g *nearGame)
	onWalk       func(g *nearGame, to Point)
	blockWalk    bool        // WalkSelf waits for its context to end (a long walk)
	walking      atomic.Bool // a walk is under way
	units        []objects.Unit
	noVisibleOff bool // the setup does not list `gm visible off`
}

func newNearGame(t *testing.T, level int) *nearGame {
	return &nearGame{fakeCarrier: newFakeCarrier(t), level: level, known: true}
}

func (n *nearGame) AgentPlace() (Point, uint32, bool) { return n.agent, n.agentMap, true }

func (n *nearGame) AccountLevel(ctx context.Context) (int, bool, error) {
	n.probes++
	return n.level, n.known, nil
}

func (n *nearGame) Appear(ctx context.Context, player string) error {
	n.appears = append(n.appears, player)
	if len(n.appearErr) > 0 {
		err := n.appearErr[0]
		n.appearErr = n.appearErr[1:]
		return err
	}
	l := n.lead()
	n.agent = Point{l.X, l.Y, l.Z}
	if n.onAppear != nil {
		n.onAppear(n)
	}
	return nil
}

func (n *nearGame) WalkSelf(ctx context.Context, to Point) (WalkResult, error) {
	n.walks = append(n.walks, to)
	if n.blockWalk {
		n.walking.Store(true)
		<-ctx.Done()
		n.walking.Store(false)
		return WalkResult{Result: "partial"}, ctx.Err()
	}
	if n.walkErr != nil {
		return WalkResult{}, n.walkErr
	}
	n.agent = to
	if n.onWalk != nil {
		n.onWalk(n, to)
	}
	return WalkResult{Result: "arrived"}, nil
}

func nearRun(t *testing.T, g *nearGame, plan *Plan) (Report, error) {
	t.Helper()
	g.agent = Point{-300, 0, 0} // far behind the party's start
	cfg := DefaultConfig()
	cfg.PostFight, cfg.AddsWait = 2*time.Second, 4*time.Second
	if !g.noVisibleOff {
		cfg.Setup = []string{"gm visible off"} // what the caller ran before the run
	}
	d := NewRemoteDriver(plan, g, cfg, objOpts(), g.clock())
	err := d.Run(context.Background(), 0)
	rep, ended := d.Status()
	if !ended {
		t.Fatal("not ended")
	}
	return rep, err
}

func TestAgentAppearsAtTheLeadAfterTheLegsWalk(t *testing.T) {
	g := newNearGame(t, 3) // a developer-level GM
	rep, err := nearRun(t, g, remotePlan(Leg{Name: "a", To: Point{200, 0, 0}}, Leg{Name: "b", To: Point{220, 0, 0}}))
	if err != nil {
		t.Fatalf("%v %v", err, rep.Notes)
	}
	// One appear after leg a's walk; leg b's walk of 20 yd leaves the agent within 40 yd.
	if fmt.Sprint(g.appears) != "[Tanky]" || len(g.walks) != 0 || g.probes != 1 {
		t.Fatalf("appears %v walks %v probes %d", g.appears, g.walks, g.probes)
	}
	r := rep.Remote
	if r.RunGMAssists != 1 || r.GMCommands != 0 || fmt.Sprint(rep.SetupShortcuts) != "[gm visible off .appear Tanky (GM assist during the run)]" {
		t.Fatalf("%+v setup %v", r, rep.SetupShortcuts)
	}
	if !strings.Contains(notesOf(rep), "a GM assist during the run (run_gm_assists; not counted in gm_commands)") {
		t.Fatalf("%s", notesOf(rep))
	}
}

func TestAgentThatIsNotAGMWalksBehindTheParty(t *testing.T) {
	g := newNearGame(t, 0)
	rep, err := nearRun(t, g, remotePlan(Leg{Name: "a", To: Point{200, 0, 0}}))
	if err != nil {
		t.Fatalf("%v %v", err, rep.Notes)
	}
	if len(g.appears) != 0 || len(g.walks) != 1 || fmt.Sprint(rep.SetupShortcuts) != "[gm visible off]" {
		t.Fatalf("appears %v walks %v setup %v", g.appears, g.walks, rep.SetupShortcuts)
	}
	// 30 yd short of the lead bot, on the line from the agent.
	if w := g.walks[0]; w.X < 169 || w.X > 171 || rep.Remote.AgentWalks != 1 || rep.Remote.GMCommands != 0 {
		t.Fatalf("%v %+v", w, rep.Remote)
	}
}

func TestAgentWithAnUnknownLevelIsNoGM(t *testing.T) {
	g := newNearGame(t, 3)
	g.known = false
	if _, err := nearRun(t, g, remotePlan(Leg{Name: "a", To: Point{200, 0, 0}})); err != nil || len(g.appears) != 0 || len(g.walks) != 1 {
		t.Fatalf("%v appears %v walks %v", err, g.appears, g.walks)
	}
}

func TestWalkingAgentKeepsOutOfAPack(t *testing.T) {
	g := newNearGame(t, 0)
	g.addMob(299, Point{150, 10, 0}) // idle, on the agent's way and near the stand point; the leg skips it (the walk does not pull it)
	rep, err := nearRun(t, g, remotePlan(Leg{Name: "a", To: Point{200, 0, 0}, Skip: []uint32{299}}))
	if err != nil || len(g.walks) != 0 || !strings.Contains(notesOf(rep), "the agent stays where it is") {
		t.Fatalf("%v walks %v\n%s", err, g.walks, notesOf(rep))
	}
}

func TestWalkingAgentStopsWhenAFightBegins(t *testing.T) {
	g := newNearGame(t, 0)
	g.blockWalk = true
	g.onTick = func(f *fakeRemote) {
		if g.walking.Load() {
			f.party[0].State = "c" // a fight begins while the agent walks
		}
	}
	rep, err := nearRun(t, g, remotePlan(Leg{Name: "a", To: Point{200, 0, 0}}))
	if err != nil || len(g.walks) != 1 || !strings.Contains(notesOf(rep), "a fight began: the agent stopped walking") {
		t.Fatalf("%v walks %v\n%s", err, g.walks, notesOf(rep))
	}
}

func TestNoFollowerNoMovement(t *testing.T) {
	g := carrierGame(t) // a game that is no Follower: the run is as before
	if _, err := runCarrier(t, g, remotePlan(Leg{Name: "a", To: Point{200, 0, 0}}), objOpts()); err != nil {
		t.Fatal(err)
	}
}

// An object step whose chest the agent cannot see: the agent is brought up (once) and the step goes on.
func TestObjectStepBringsTheAgentUp(t *testing.T) {
	g := newNearGame(t, 3)
	g.chestLoot = []tbm.ItemCount{{Entry: itMedallion, Count: 1}}
	g.appearErr = []error{errors.New("not now")} // the walk's own appear fails; the object step's retry works
	g.onAppear = func(n *nearGame) { n.gobjs = []objects.GameObject{chestObj()} }
	plan := remotePlan(Leg{Name: "c", To: Point{200, 0, 0}, Steps: []Step{lootChestStep()}})
	rep, err := nearRun(t, g, plan)
	if err != nil {
		t.Fatalf("%v\n%s", err, notesOf(rep))
	}
	if len(g.appears) != 2 || g.indexOf("cmd open") < 0 || rep.Remote.RunGMAssists != 1 {
		t.Fatalf("appears %v %v %+v", g.appears, g.orders, rep.Remote)
	}
}

func (n *nearGame) Units() []objects.Unit { return n.units }

func corpseOf(entry uint32, at Point) objects.Unit {
	return objects.Unit{GUID: 0xF130000309000099, Type: "creature", Entry: entry, X: at.X, Y: at.Y, Z: at.Z, PosAgeMS: 100, MaxHealth: 5000, Dead: true}
}

func nearBossPlan() *Plan {
	return remotePlan(Leg{Name: "b", To: Point{20, 0, 0}, Boss: &Boss{Entry: 777, Position: Point{60, 0, 0}}})
}

// A resumed run: the boss died before this driver started, so no ledger and no frame knows it. Its corpse is in the agent's own store,
// which the keep-near before the boss step brought up to the party.
func TestResumedBossCorpseInTheAgentsStorePassesTheBossStep(t *testing.T) {
	g := newNearGame(t, 3)
	g.appearErr = []error{errors.New("not now")} // the walk's appear fails; the boss step's own keep-near works
	g.onAppear = func(n *nearGame) { n.units = []objects.Unit{corpseOf(777, Point{62, 3, 0})} }
	rep, err := nearRun(t, g, nearBossPlan())
	if err != nil || len(g.appears) != 2 || !strings.Contains(notesOf(rep), "its corpse is in the agent's view") {
		t.Fatalf("%v appears %v\n%s", err, g.appears, notesOf(rep))
	}
}

func TestBossStepWithoutACorpseOrLedgerStillFails(t *testing.T) {
	// No corpse at all.
	g := newNearGame(t, 3)
	if _, err := nearRun(t, g, nearBossPlan()); err == nil || !strings.Contains(err.Error(), "no living [777]") {
		t.Fatalf("%v", err)
	}
	// A corpse of another entry, a living unit, and a corpse far from the boss's area do not count.
	g = newNearGame(t, 3)
	alive := corpseOf(777, Point{60, 0, 0})
	alive.Dead, alive.Health = false, 5000
	g.units = []objects.Unit{corpseOf(778, Point{60, 0, 0}), alive, corpseOf(777, Point{900, 900, 0})}
	if _, err := nearRun(t, g, nearBossPlan()); err == nil || !strings.Contains(err.Error(), "no living [777]") {
		t.Fatalf("%v", err)
	}
}

func TestNoAppearWithoutGMVisibleOffInTheSetup(t *testing.T) {
	g := newNearGame(t, 3)
	g.noVisibleOff = true
	rep, err := nearRun(t, g, remotePlan(Leg{Name: "a", To: Point{200, 0, 0}}))
	if err != nil || len(g.appears) != 0 || rep.Remote.RunGMAssists != 0 || !strings.Contains(notesOf(rep), `does not list "gm visible off"`) {
		t.Fatalf("%v appears %v\n%s", err, g.appears, notesOf(rep))
	}
	// The setup spelled with a dot and capitals still counts.
	g = newNearGame(t, 3)
	d := NewRemoteDriver(remotePlan(Leg{Name: "a", To: Point{1, 0, 0}}), g, Config{Setup: []string{" .GM  Visible Off "}}.withDefaults(), opts(), g.clock())
	if !d.setupHas("gm visible off") {
		t.Fatal("not read")
	}
}

func TestNoAppearInACombat(t *testing.T) {
	g := newNearGame(t, 3)
	g.onTick = func(f *fakeRemote) {
		if f.lead().X >= 200 && f.combatUntil.IsZero() {
			f.combatUntil = f.now.Add(3 * time.Second) // a fight as the party arrives
			f.party[0].State = "c"
		}
	}
	rep, err := nearRun(t, g, remotePlan(Leg{Name: "a", To: Point{200, 0, 0}}))
	if err != nil || len(g.appears) != 0 || rep.Remote.RunGMAssists != 0 {
		t.Fatalf("%v appears %v\n%s", err, g.appears, notesOf(rep))
	}
}

// A step without positions takes no corpse as proof.
func TestBossCorpseNeedsTheStepsPositions(t *testing.T) {
	g := newNearGame(t, 3)
	g.units = []objects.Unit{corpseOf(777, Point{60, 0, 0})}
	d := NewRemoteDriver(nearBossPlan(), g, DefaultConfig(), opts(), g.clock())
	if d.bossCorpseSeen(fightSpec{pick: []uint32{777}}) {
		t.Fatal("a corpse counted for a step with no positions")
	}
	if !d.bossCorpseSeen(fightSpec{pick: []uint32{777}, anchors: []Point{{60, 0, 0}}}) {
		t.Fatal("the corpse at the boss position did not count")
	}
}

// ---- the agent brought into view of the step's own object ----

// sealAt is the Seal of Khaz'Mul 60 yd on from the Keystone, where the lead bot stands: out of the agent's view until it has walked toward it.
var sealAt = Point{20, 60, 0}

func sealObj(state string) objects.GameObject {
	return objects.GameObject{GUID: 0x1F1000000000AAA3, Entry: goSeal, X: sealAt.X, Y: sealAt.Y, Z: sealAt.Z, Distance: 40, State: state}
}

func sealStep() []Step {
	return []Step{
		{UseObject: &UseObject{ObjectRef: ObjectRef{Entry: goKeystone}, RequiresItem: &ItemRef{Entry: itStaff, Name: "Staff of Prehistoria"}}},
		{WaitFor: &WaitFor{GameObject: &ObjectState{ObjectRef: ObjectRef{Entry: goSeal, Near: &sealAt}, State: "active"}, TimeoutS: 60}},
	}
}

func nearKeystoneGame(t *testing.T, level int) *nearGame {
	g := newNearGame(t, level)
	g.gobjs = []objects.GameObject{{GUID: keystoneGUID, Entry: goKeystone, X: 20, Y: 0, Z: 0, Distance: 3, State: "ready"}}
	g.bag("Magey")[itStaff] = 1
	return g
}

// uldaman-remote-4: the lead bot stood 41 yd from the Seal the wait names, the agent beside it saw nothing. The agent is walked toward the
// Seal before the wait, sees it, the wait is checked (the Seal opens once the Keystone is used), and the next leg's walk, which the
// Stepper hands out in the very Next that passes the wait, is no object failure.
func TestWaitForAnObjectOutOfViewBringsTheAgentIntoView(t *testing.T) {
	for _, level := range []int{3, 0} { // a GM and a player
		g := nearKeystoneGame(t, level)
		var near atomic.Bool // the walk toward the Seal ended (the walk runs on its own goroutine: the store is changed on the driver's)
		g.onWalk = func(n *nearGame, to Point) {
			if to.Y > 30 { // the walk toward the Seal, not the one behind the party
				near.Store(true)
			}
		}
		g.fakeRemote.onTick = func(*fakeRemote) {
			if near.Load() && len(g.gobjs) == 1 {
				g.gobjs = append(g.gobjs, sealObj("ready"))
			}
			for i := range g.gobjs { // the Seal opens once the Keystone was used
				if g.gobjs[i].Entry == goSeal && len(g.ordersOf("cmd open")) > 0 {
					g.gobjs[i].State = "active"
				}
			}
		}
		plan := remotePlan(Leg{Name: "k", To: Point{20, 0, 0}, Steps: sealStep()}, Leg{Name: "i", To: Point{20, 20, 0}})
		rep, err := nearRun(t, g, plan)
		if err != nil {
			t.Fatalf("level %d: %v\n%s", level, err, notesOf(rep))
		}
		last := g.walks[len(g.walks)-1]
		if last.Y < 39 || last.Y > 43 || strings.Contains(notesOf(rep), "cannot see") || !strings.Contains(notesOf(rep), "is in the agent's view after") {
			t.Fatalf("level %d: walks %v\n%s", level, g.walks, notesOf(rep))
		}
	}
}

// An object that never comes into view: the agent looked and walked, and the failure says how far from it the agent stood. A wait goes
// on instead of failing, unchecked and with the distance in the note.
func TestObjectNeverInViewFailsWithTheDistance(t *testing.T) {
	g := newNearGame(t, 3)
	chestFar := Step{LootObject: &LootObject{ObjectRef: ObjectRef{Entry: goChest, Near: &sealAt}}}
	rep, err := nearRun(t, g, remotePlan(Leg{Name: "c", To: Point{20, 0, 0}, Steps: []Step{chestFar}}))
	if err == nil || !strings.Contains(err.Error(), "within view of game object 123329 near 20 60 0, the agent stands 20 yd from it") || len(g.walks) != 1 {
		t.Fatalf("%v walks %v\n%s", err, g.walks, notesOf(rep))
	}
	if g.indexOf("cmd open") >= 0 {
		t.Fatalf("an order went out: %v", g.orders)
	}
	// The wait for the Seal: unchecked, and the note says where the agent stood.
	g = nearKeystoneGame(t, 3)
	rep, err = nearRun(t, g, remotePlan(Leg{Name: "k", To: Point{20, 0, 0}, Steps: sealStep()}, Leg{Name: "i", To: Point{20, 20, 0}}))
	if err != nil || !strings.Contains(notesOf(rep), "cannot see game object 124372 near 20 60 0 (the agent stands 20 yd from it)") {
		t.Fatalf("%v\n%s", err, notesOf(rep))
	}
}

// The agent does not walk past a hostile the bots see; the object stays out of view and the step fails as before.
func TestAgentDoesNotWalkTowardAnObjectPastAHostile(t *testing.T) {
	g := newNearGame(t, 3)
	g.addMob(299, Point{20, 45, 0}) // idle, near the stand point
	chestFar := Step{LootObject: &LootObject{ObjectRef: ObjectRef{Entry: goChest, Near: &sealAt}}}
	rep, err := nearRun(t, g, remotePlan(Leg{Name: "c", To: Point{20, 0, 0}, Steps: []Step{chestFar}}))
	if err == nil || len(g.walks) != 0 || !strings.Contains(notesOf(rep), "the agent stays where it is") {
		t.Fatalf("%v walks %v\n%s", err, g.walks, notesOf(rep))
	}
}

// The way to the object is checked all along, not at its corners: a mob in the middle of a long straight (the corridor 0..160 yd) is
// 2 yd from the line. Hostiles of the agent's own store count too, not only the feed's.
func TestAgentDoesNotWalkPastAMobInTheMiddleOfALongWay(t *testing.T) {
	far := Point{20, 160, 0}
	chestFar := Step{LootObject: &LootObject{ObjectRef: ObjectRef{Entry: goChest, Near: &far}}}
	plan := func() *Plan { return remotePlan(Leg{Name: "c", To: Point{20, 0, 0}, Steps: []Step{chestFar}}) }
	for _, level := range []int{3, 0} {
		// In the bots' feed.
		g := newNearGame(t, level)
		g.addMob(299, Point{22, 80, 0})
		rep, err := nearRun(t, g, plan())
		if err == nil || strings.Contains(notesOf(rep), "walks to 2") || !strings.Contains(notesOf(rep), "the agent stays where it is") {
			t.Fatalf("level %d feed: %v walks %v\n%s", level, err, g.walks, notesOf(rep))
		}
		for _, w := range g.walks {
			if w.Y > 30 {
				t.Fatalf("level %d feed: walked to %v", level, w)
			}
		}
		// Only in the agent's store.
		g = newNearGame(t, level)
		g.units = []objects.Unit{{GUID: 0xF130000309000077, Type: "creature", Entry: 299, Name: "Stonevault Ambusher", X: 22, Y: 80, PosAgeMS: 100, Health: 900, MaxHealth: 900, Hostile: true}}
		rep, err = nearRun(t, g, plan())
		if err == nil || !strings.Contains(notesOf(rep), "Stonevault Ambusher (in the agent's view) stands near its way") {
			t.Fatalf("level %d store: %v walks %v\n%s", level, err, g.walks, notesOf(rep))
		}
		for _, w := range g.walks {
			if w.Y > 30 {
				t.Fatalf("level %d store: walked to %v", level, w)
			}
		}
		// A dead one, a friendly one and a player do not block.
		g = newNearGame(t, level)
		g.units = []objects.Unit{
			{GUID: 0xF130000309000078, Type: "creature", Entry: 299, X: 22, Y: 80, PosAgeMS: 100, Dead: true, Hostile: true},
			{GUID: 0xF130000309000079, Type: "creature", Entry: 300, X: 22, Y: 90, PosAgeMS: 100, Health: 9, MaxHealth: 9},
			{GUID: 0xF13000030900007A, Type: "player", X: 22, Y: 100, PosAgeMS: 100, Health: 9, MaxHealth: 9, Hostile: true},
		}
		if _, err := nearRun(t, g, plan()); err == nil || len(g.walks) == 0 || g.walks[len(g.walks)-1].Y < 130 {
			t.Fatalf("level %d harmless units: %v walks %v", level, err, g.walks)
		}
	}
}

// A wait passed unchecked: when the party's next walk fails, the error names the object.
func TestWalkFailureNamesTheWaitPassedUnchecked(t *testing.T) {
	g := nearKeystoneGame(t, 3) // the Seal never shows
	g.fakeRemote.onTick = func(f *fakeRemote) {
		if len(g.ordersOf("cmd open")) > 0 {
			f.ignoreMoves = 100 // the door stayed shut
		}
	}
	_, err := nearRun(t, g, remotePlan(Leg{Name: "k", To: Point{20, 0, 0}, Steps: sealStep()}, Leg{Name: "i", To: Point{20, 20, 0}}))
	if err == nil || !strings.Contains(err.Error(), "gained no ground") || !strings.Contains(err.Error(), "the earlier wait for game object 124372 near 20 60 0 passed unchecked") {
		t.Fatalf("%v", err)
	}
	// A walk that works clears it: no wait, no mention.
	g = nearKeystoneGame(t, 3)
	if _, err := nearRun(t, g, remotePlan(Leg{Name: "k", To: Point{20, 0, 0}, Steps: sealStep()}, Leg{Name: "i", To: Point{20, 20, 0}})); err != nil {
		t.Fatal(err)
	}
}
