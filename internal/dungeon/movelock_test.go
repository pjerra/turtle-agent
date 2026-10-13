package dungeon

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/pjerra/turtle-agent/internal/events"
)

// Every call that moves the leader runs under one hold of the brain's move lock, and no hold stands while the Driver does anything else.
func TestDriverHoldsTheMoveLockAroundEveryWalk(t *testing.T) {
	g, p := runFixture(t)
	br := &fakeBrain{}
	g.moveHeld = func() int { return br.held }
	heldAtSleep := 0
	g.onTick = func(g *fakeGame) {
		// A wait under the lock is the settle of a use_object (see TestUseAndArrivalWaitsHoldTheLock), nothing else.
		if br.held > 0 && !strings.HasPrefix(g.moveLog[len(g.moveLog)-1], "use") {
			heldAtSleep++
		}
	}
	d := NewDriver(p, g, Config{Brain: br}, g.clock())
	if err := d.Run(context.Background(), 0); err != nil {
		t.Fatalf("%v\n%+v", err, d.Report())
	}
	if len(g.moveLog) == 0 || len(g.moveLog) != len(g.walks)+len(g.teleports)+len(g.used) {
		t.Fatalf("moves %v walks %v", g.moveLog, g.walks)
	}
	for _, m := range g.moveLog {
		if !strings.HasSuffix(m, ":1") {
			t.Fatalf("a movement without exactly one hold: %v", g.moveLog)
		}
	}
	if br.held != 0 || br.maxHeld != 1 || br.holds != len(g.moveLog) {
		t.Fatalf("held %d max %d holds %d moves %d", br.held, br.maxHeld, br.holds, len(g.moveLog))
	}
	if heldAtSleep != 0 {
		t.Fatalf("the lock stood across a wait (%d)", heldAtSleep)
	}
}

// The teleport to a leg's start is a movement too.
func TestTeleportRunsUnderTheMoveLock(t *testing.T) {
	g, p := runFixture(t)
	g.pos = Point{500, 0, 0}
	g.self.Health = g.self.MaxHealth
	br := &fakeBrain{}
	g.moveHeld = func() int { return br.held }
	d := NewDriver(p, g, Config{Brain: br, Teleport: true, GM: GMAssumed}, g.clock())
	if err := d.Run(context.Background(), 0); err != nil {
		t.Fatalf("%v\n%+v", err, d.Report())
	}
	if len(g.teleports) == 0 || g.moveLog[0] != "teleport:1" {
		t.Fatalf("teleports %v moves %v", g.teleports, g.moveLog)
	}
}

// While a pack is fought the Driver holds nothing, so the brain may reposition; the walk that the fight cut short is made again under a
// new hold.
func TestBrainIsFreeToMoveDuringAFightBetweenDriverWalks(t *testing.T) {
	g := newFake()
	g.add(90, 90, Point{20, 0, 0})
	g.aggroGUID = 90
	p := mustPlan(t, `{"version":1,"name":"t","map":70,"entrance":{"x":0,"y":0,"z":0},"legs":[
	 {"to":{"x":80,"y":0,"z":0},"pull_groups":[{"entries":[1],"positions":[{"x":100,"y":0,"z":0}]}]}]}`)
	g.add(201, 1, Point{100, 0, 0})
	br := &fakeBrain{}
	g.moveHeld = func() int { return br.held }
	blocked := 0
	g.walkHook = func(ctx context.Context, g *fakeGame, to Point) (WalkResult, error) {
		if blocked == 0 && to == (Point{80, 0, 0}) {
			blocked++
			g.aggro.Store(true)
			<-ctx.Done()
			return WalkResult{Result: "partial", Reason: "cancelled"}, ctx.Err()
		}
		g.pos = to
		return WalkResult{Result: "arrived"}, nil
	}
	heldInFight := -1
	g.onChat = func(g *fakeGame, text string) {
		if text == ".bot action attack" {
			if heldInFight < 0 {
				heldInFight = br.held
			}
			for _, u := range g.units {
				if u.GUID == 90 && u.dieAt.IsZero() && !u.Dead {
					u.dieAt = g.now.Add(6 * time.Second)
				}
			}
		}
	}
	fightSleeps, heldAtSleep := 0, 0
	g.onTick = func(g *fakeGame) { fightSleeps++; heldAtSleep += br.held }
	d := NewDriver(p, g, Config{Brain: br, WalkWatch: 2 * time.Millisecond}, g.clock())
	if err := d.Run(context.Background(), 0); err != nil {
		t.Fatalf("%v\n%+v", err, d.Report())
	}
	if heldInFight != 0 || fightSleeps == 0 || heldAtSleep != 0 {
		t.Fatalf("the lock stood in the fight: at the order %d, over %d waits %d", heldInFight, fightSleeps, heldAtSleep)
	}
	if br.maxHeld != 1 || br.holds < 2 || br.held != 0 {
		t.Fatalf("held %d max %d holds %d", br.held, br.maxHeld, br.holds)
	}
}

// A brain step that does not stop in time fails the walk like a refused one (tried again, then the run fails) rather than hanging the run.
func TestWalkWhoseHoldFailsIsTriedAgainAndThenFails(t *testing.T) {
	g, p := runFixture(t)
	br := &fakeBrain{holdErr: errors.New("busy")}
	d := NewDriver(p, g, Config{Brain: br, WalkAttempts: 2}, g.clock())
	err := d.Run(context.Background(), 0)
	if err == nil || !strings.Contains(err.Error(), "failed after 2 tries") || !strings.Contains(err.Error(), "did not stop") {
		t.Fatalf("%v", err)
	}
	if len(g.walks) != 0 {
		t.Fatalf("walked without the lock: %v", g.walks)
	}
}

// The ghost's walk and the release and the portal hold the lock as well.
func TestCorpseRunHoldsTheMoveLock(t *testing.T) {
	g, p := runFixture(t)
	var once bool
	killLeaderInFight(g, &once, true)
	g.dead.graveyard, g.dead.graveMap = Point{50, 0, 0}, 70
	g.dead.corpse = Corpse{Found: true, Map: 70, CorpseMap: 70, Pos: Point{20, 5, 0}}
	br := &fakeBrain{}
	g.moveHeld = func() int { return br.held }
	d := NewDriver(p, g, Config{Brain: br}, g.clock())
	if err := d.Run(context.Background(), 0); err != nil {
		t.Fatalf("%v\n%+v", err, d.Report())
	}
	if g.dead.releases == 0 || !strings.Contains(strings.Join(g.moveLog, ","), "release:1") {
		t.Fatalf("release not held: %v", g.moveLog)
	}
	ghostWalks := 0
	for i, m := range g.moveLog {
		if !strings.HasSuffix(m, ":1") {
			t.Fatalf("%v (entry %d)", g.moveLog, i)
		}
		if m == "walk:1" {
			ghostWalks++
		}
	}
	if ghostWalks < 2 || br.held != 0 {
		t.Fatalf("walks %d held %d: %v", ghostWalks, br.held, g.moveLog)
	}
}

func TestPortalRunsUnderTheMoveLock(t *testing.T) {
	g, p := runFixture(t)
	p.EntranceTrigger = 286
	var once bool
	killLeaderInFight(g, &once, true)
	g.dead.graveyard, g.dead.graveMap = Point{900, 0, 0}, 1
	g.dead.corpse = Corpse{Found: true, Map: 1, CorpseMap: 70, Pos: Point{1000, 0, 0}, Inside: true}
	g.dead.portalOK, g.dead.portalTo = true, Point{0, 0, 0}
	br := &fakeBrain{}
	g.moveHeld = func() int { return br.held }
	d := NewDriver(p, g, Config{Brain: br}, g.clock())
	if err := d.Run(context.Background(), 0); err != nil {
		t.Fatalf("%v\n%+v", err, d.Report())
	}
	if g.dead.triggers == 0 || !strings.Contains(strings.Join(g.moveLog, ","), "portal:1") {
		t.Fatalf("triggers %d moves %v notes %v", g.dead.triggers, g.moveLog, d.Report().Notes)
	}
}

// The lock stays on across a use_object and its settle wait, and across the wait for a teleport's arrival, so that a brain step
// cannot carry the leader away between the movement and what depends on where it stands.
func TestUseAndArrivalWaitsHoldTheLock(t *testing.T) {
	g, p := runFixture(t)
	g.pos = Point{500, 0, 0}
	br := &fakeBrain{}
	g.moveHeld = func() int { return br.held }
	heldIn := map[string]int{}
	g.onTick = func(g *fakeGame) {
		if br.held > 0 && len(g.moveLog) > 0 {
			heldIn[strings.SplitN(g.moveLog[len(g.moveLog)-1], ":", 2)[0]]++
		}
	}
	d := NewDriver(p, g, Config{Brain: br, Teleport: true, GM: GMAssumed}, g.clock())
	if err := d.Run(context.Background(), 0); err != nil {
		t.Fatalf("%v\n%+v", err, d.Report())
	}
	if len(g.used) == 0 || heldIn["use"] == 0 || heldIn["teleport"] == 0 {
		t.Fatalf("used %v, waits under the lock: %v, moves %v", g.used, heldIn, g.moveLog)
	}
	for _, m := range g.moveLog {
		if !strings.HasSuffix(m, ":1") {
			t.Fatalf("%v", g.moveLog)
		}
	}
	if heldIn["walk"] != 0 || br.held != 0 {
		t.Fatalf("a wait under a walk's hold, or a hold left: %v %d", heldIn, br.held)
	}
}

// The wait for the ghost after the release is under the lock too.
func TestReleaseWaitHoldsTheLock(t *testing.T) {
	g, p := runFixture(t)
	var once bool
	killLeaderInFight(g, &once, true)
	g.dead.graveyard, g.dead.graveMap = Point{50, 0, 0}, 70
	g.dead.corpse = Corpse{Found: true, Map: 70, CorpseMap: 70, Pos: Point{20, 5, 0}}
	g.dead.reclaimWait = 10 * time.Second
	br := &fakeBrain{}
	g.moveHeld = func() int { return br.held }
	underRelease := 0
	prev := g.onTick
	g.onTick = func(g *fakeGame) {
		prev(g)
		if br.held > 0 && len(g.moveLog) > 0 && strings.HasPrefix(g.moveLog[len(g.moveLog)-1], "release") {
			underRelease++
		}
	}
	d := NewDriver(p, g, Config{Brain: br}, g.clock())
	if err := d.Run(context.Background(), 0); err != nil {
		t.Fatalf("%v\n%+v", err, d.Report())
	}
	if underRelease == 0 || br.held != 0 {
		t.Fatalf("waits under the release's hold: %d held %d", underRelease, br.held)
	}
}

// A leader that dies while a hold waits (the use's settle) is looked at only after the hold is let go: its recovery, a corpse run and a
// rest gate, does not run under the lock.
func TestLeaderDyingDuringTheUseSettleIsRecoveredWithoutTheLock(t *testing.T) {
	g, p := runFixture(t)
	g.dead.graveyard, g.dead.graveMap = Point{50, 0, 0}, 70
	g.dead.corpse = Corpse{Found: true, Map: 70, CorpseMap: 70, Pos: Point{20, 5, 0}}
	br := &fakeBrain{}
	killed := false
	g.onTick = func(g *fakeGame) {
		if !killed && len(g.used) > 0 && br.held > 0 {
			killed = true
			g.kill()
			g.party[1].Online = false // no healer: it goes to the corpse run
		}
	}
	heldAtRecovery := -1
	g.onEmit = func(kind string, data map[string]any) {
		if kind == events.KindDungeonLeader && data["phase"] == "died" && heldAtRecovery < 0 {
			heldAtRecovery = br.held
		}
	}
	d := NewDriver(p, g, Config{Brain: br}, g.clock())
	if err := d.Run(context.Background(), 0); err != nil {
		t.Fatalf("%v\n%+v", err, d.Report())
	}
	if !killed || heldAtRecovery != 0 || br.held != 0 {
		t.Fatalf("killed %v, holds standing when the recovery began: %d (end %d)", killed, heldAtRecovery, br.held)
	}
	if d.Report().LeaderDeaths != 1 {
		t.Fatalf("%+v", d.Report())
	}
}
