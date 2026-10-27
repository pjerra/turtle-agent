package dungeon

// Spec ta-player-follow section 5: the follow mode's named tests, against the
// existing fakeGame (driver_test.go) + fakeDeath (fake_death_test.go) and the
// fake clock. Written before follow.go (see them fail first).

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/pjerra/turtle-agent/internal/objects"
	"github.com/pjerra/turtle-agent/internal/packet"
)

// followFake is a fakeGame wrapped with the agent's own unit row and the melee
// swing the follow mode needs (SelfRow/Attack/StopAttack).
type followFake struct {
	*fakeGame
	row            objects.Unit
	attacks, stops int
}

func (f *followFake) SelfRow() (objects.Unit, bool) { return f.row, true }

func (f *followFake) Attack(ctx context.Context) error {
	f.attacks++
	return nil
}

func (f *followFake) StopAttack(ctx context.Context) error {
	f.stops++
	return nil
}

const followPlan = `{"version":1,"name":"followtest","map":70,"entrance":{"x":0,"y":0,"z":0},"legs":[
 {"name":"a","to":{"x":10,"y":0,"z":0},"pull_groups":[{"entries":[1],"positions":[{"x":30,"y":0,"z":0}]}]}]}`

const followPlanZoned = `{"version":1,"name":"followtest","map":70,"entrance":{"x":0,"y":0,"z":0},"portal":{"map":0,"x":100,"y":0,"z":0},"entrance_trigger":286,"legs":[
 {"name":"a","to":{"x":10,"y":0,"z":0},"pull_groups":[{"entries":[1],"positions":[{"x":30,"y":0,"z":0}]}]}]}`

// followFixture builds the follow loop's world: the agent "Me" on map 70 with
// the lead bot Tank in the party.
func followFixture(t *testing.T, zoned bool) (*followFake, *Plan) {
	t.Helper()
	doc := followPlan
	if zoned {
		doc = followPlanZoned
	}
	p := mustPlan(t, doc)
	g := newFake()
	g.partyInViewNearMax = 0
	g.pos = Point{0, 0, 0}
	g.self = Self{GUID: leader, Name: "Me", Health: 1000, MaxHealth: 1000}
	return &followFake{fakeGame: g}, p
}

// leadAt puts the Tank unit row at at (Type player, in party). Members' rows
// are added directly (partyInViewNearMax is 0, so no synthesized rows).
func leadAt(f *followFake, at Point) {
	for _, u := range f.units {
		if u.GUID == tank {
			u.X, u.Y, u.Z = at.X, at.Y, at.Z
			return
		}
	}
	u := f.add(tank, 0, at)
	u.Type, u.Name, u.Hostile, u.InParty = "player", "Tank", false, true
}

func leadRow(f *followFake) *fakeUnit {
	for _, u := range f.units {
		if u.GUID == tank {
			return u
		}
	}
	return nil
}

func newFollowerForTest(f *followFake, p *Plan, mutateCfg func(*FollowConfig)) *PlayerFollower {
	cfg := FollowConfig{Plan: p, Lead: "Tank", WalkWatch: 2 * time.Millisecond}
	if mutateCfg != nil {
		mutateCfg(&cfg)
	}
	return NewFollower(f, cfg, f.clock())
}

// runTicks drives the loop deterministically: n ticks with FollowTick between.
// A non-empty stop reason is the test's expected outcome, returned.
func runTicks(f *followFake, fr *PlayerFollower, n int) string {
	var reason string
	for range n {
		if reason = fr.tick(); reason != "" {
			return reason
		}
		f.now = f.now.Add(FollowTick)
	}
	return reason
}

func lastNoteHas(st FollowStatus, sub string) bool {
	for i := len(st.Notes) - 1; i >= 0; i-- {
		if strings.Contains(st.Notes[i], sub) {
			return true
		}
	}
	return false
}

// addHostile adds a living hostile creature row at at with an optional target.
func addHostile(f *followFake, guid packet.GUID, at Point, inCombat bool, target packet.GUID) *fakeUnit {
	u := f.add(guid, 7, at)
	u.InCombat, u.Target = inCombat, target
	return u
}

func TestFollowWalksToTheTrailBehindTheLead(t *testing.T) {
	f, p := followFixture(t, false)
	leadAt(f, Point{0, 0, 0})
	f.pos = Point{-5, 0, 0}
	var leadXs []float32
	f.onWalk = func(g *fakeGame, to Point) { leadXs = append(leadXs, leadRow(f).X) }
	fr := newFollowerForTest(f, p, nil)
	for x := float32(0); x <= 40; x += 2 {
		leadRow(f).X = x
		if reason := fr.tick(); reason != "" {
			t.Fatalf("tick at lead x %.0f stopped: %s", x, reason)
		}
		f.now = f.now.Add(FollowTick)
	}
	if len(f.walks) == 0 {
		t.Fatalf("no walk; status %+v", fr.Status())
	}
	// Every walk goal lies on the trail 11.5 yd behind the lead where it stood at that tick.
	for i, goal := range f.walks {
		if goal.Y != 0 || goal.X < leadXs[i]-12 || goal.X > leadXs[i]-11 {
			t.Fatalf("walk %d goal %v not 11.5 yd behind the lead (lead x %.1f)", i, goal, leadXs[i])
		}
	}
}

func TestFollowNeverWalksAheadOfTheLead(t *testing.T) {
	f, p := followFixture(t, false)
	fr := newFollowerForTest(f, p, nil)
	for x := float32(0); x <= 40; x += 2 {
		leadAt(f, Point{x, 0, 0})
		if reason := fr.tick(); reason != "" {
			t.Fatalf("trail tick at x %.0f stopped: %s", x, reason)
		}
		f.now = f.now.Add(FollowTick)
	}
	// The player stands 20 yd ahead of the lead, heading +x.
	f.pos = Point{60, 0, 0}
	for range 3 {
		if reason := fr.tick(); reason != "" {
			t.Fatalf("stopped: %s", reason)
		}
		f.now = f.now.Add(FollowTick)
	}
	for _, w := range f.walks {
		if w.X >= 40 {
			t.Fatalf("walk goal ahead of the lead: %v", w)
		}
	}
	if !lastNoteHas(fr.Status(), "stands ahead of the lead bot") {
		t.Fatalf("no ahead note: %+v", fr.Status())
	}
}

func TestFollowStaysStillInTheBand(t *testing.T) {
	f, p := followFixture(t, false)
	fr := newFollowerForTest(f, p, nil)
	for x := float32(0); x <= 12; x += 2 {
		leadAt(f, Point{x, 0, 0})
		if reason := fr.tick(); reason != "" {
			t.Fatalf("trail tick at x %.0f stopped: %s", x, reason)
		}
		f.now = f.now.Add(FollowTick)
	}
	f.pos = Point{0, 0, 0} // 12 yd behind on the trail
	if reason := runTicks(f, fr, 10); reason != "" {
		t.Fatalf("stopped: %s", reason)
	}
	if len(f.walks) != 0 {
		t.Fatalf("walked in the band: %v", f.walks)
	}
	if st := fr.Status(); st.State != "near" {
		t.Fatalf("state %q, want near", st.State)
	}
}

func TestFollowHoldsInAFight(t *testing.T) {
	f, p := followFixture(t, false)
	fr := newFollowerForTest(f, p, nil)
	for x := float32(0); x <= 30; x += 2 {
		leadAt(f, Point{x, 0, 0})
		if reason := fr.tick(); reason != "" {
			t.Fatalf("trail tick at x %.0f stopped: %s", x, reason)
		}
		f.now = f.now.Add(FollowTick)
	}
	u := addHostile(f, 0x20, Point{30, 0, 0}, true, tank)
	u.Target = healer
	f.row.InCombat = false
	leadRow(f).InCombat = false
	// A party member row in combat, 30 yd away.
	m := f.add(healer, 0, Point{30, 0, 0})
	m.Type, m.Name, m.Hostile, m.InParty, m.InCombat = "player", "Heal", false, true, true
	n := len(f.walks)
	if reason := runTicks(f, fr, 3); reason != "" {
		t.Fatalf("stopped: %s", reason)
	}
	if len(f.walks) != n {
		t.Fatalf("walked in a fight: %v", f.walks[n:])
	}
	if st := fr.Status(); st.State != "combat" {
		t.Fatalf("state %q, want combat", st.State)
	}
}

func TestFollowLeashesInAFightBeyond40(t *testing.T) {
	f, p := followFixture(t, false)
	fr := newFollowerForTest(f, p, nil)
	for x := float32(0); x <= 50; x += 2 {
		leadAt(f, Point{x, 0, 0})
		if reason := fr.tick(); reason != "" {
			t.Fatalf("trail tick at x %.0f stopped: %s", x, reason)
		}
		f.now = f.now.Add(FollowTick)
	}
	m := f.add(healer, 0, Point{50, 0, 0})
	m.Type, m.Name, m.Hostile, m.InParty, m.InCombat = "player", "Heal", false, true, true
	f.pos = Point{0, 0, 0} // 50 yd behind the lead
	n := len(f.walks)
	if reason := fr.tick(); reason != "" {
		t.Fatalf("stopped: %s", reason)
	}
	f.now = f.now.Add(FollowTick)
	if len(f.walks) == n {
		t.Fatalf("no leash walk at 50 yd; status %+v", fr.Status())
	}
	last := f.walks[len(f.walks)-1]
	got := dist3(last, Point{leadRow(f).X, 0, 0})
	if got < 24 || got > 26 {
		t.Fatalf("leash goal %.1f yd from the lead, want 25", got)
	}
	if st := fr.Status(); st.State != "combat_leash" {
		t.Fatalf("state %q, want combat_leash", st.State)
	}
}

func TestFollowCancelsAWalkWhenAFightBegins(t *testing.T) {
	f, p := followFixture(t, false)
	fr := newFollowerForTest(f, p, nil)
	for x := float32(0); x <= 30; x += 2 {
		leadAt(f, Point{x, 0, 0})
		if reason := fr.tick(); reason != "" {
			t.Fatalf("trail tick at x %.0f stopped: %s", x, reason)
		}
		f.now = f.now.Add(FollowTick)
	}
	started := make(chan struct{}, 1)
	release := make(chan struct{})
	f.walkHook = func(ctx context.Context, g *fakeGame, to Point) (WalkResult, error) {
		started <- struct{}{}
		select {
		case <-release:
			g.pos = to
			return WalkResult{Result: "arrived"}, nil
		case <-ctx.Done():
			return WalkResult{}, ctx.Err()
		}
	}
	walkDone := make(chan struct{})
	go func() {
		defer close(walkDone)
		_, _, _ = fr.walkTo(context.Background(), Point{18, 0, 0}, "follow")
	}()
	<-started
	f.aggro.Store(true)
	f.aggroGUID = healer
	m := f.add(healer, 0, Point{30, 0, 0})
	m.Type, m.Name, m.Hostile, m.InParty, m.InCombat = "player", "Heal", false, true, true
	close(release)
	<-walkDone
	f.walkHook = nil
	f.aggro.Store(false)
	if st := fr.Status(); st.WalkFails != 0 {
		t.Fatalf("a cancelled walk counted as failed: %+v", st)
	}
}

func TestFollowStopsWhenTheLeadIsLost(t *testing.T) {
	f, p := followFixture(t, false)
	fr := newFollowerForTest(f, p, nil)
	leadAt(f, Point{10, 0, 0})
	if reason := fr.tick(); reason != "" {
		t.Fatalf("stopped: %s", reason)
	}
	f.now = f.now.Add(FollowTick)
	f.units = nil // the lead row is gone
	// lastSeen is the seen tick; the loop stops 30 s after it: 61 ticks of
	// nothing, the last tick setting the stop.
	var reason string
	for range 61 {
		if reason = fr.tick(); reason != "" {
			break
		}
		f.now = f.now.Add(FollowTick)
	}
	if reason == "" || !strings.Contains(reason, "was not in view for 30 s") {
		t.Fatalf("no lost stop at 30 s: %q", reason)
	}
	if st := fr.Status(); !st.Running || st.State != "lost" {
		t.Fatalf("tick returns the stop, Run ends the loop: %+v", st)
	}
}

func TestFollowLeadLeftTheParty(t *testing.T) {
	f, p := followFixture(t, false)
	fr := newFollowerForTest(f, p, nil)
	leadAt(f, Point{10, 0, 0})
	if reason := fr.tick(); reason != "" {
		t.Fatalf("stopped: %s", reason)
	}
	f.now = f.now.Add(FollowTick)
	f.party = f.party[1:] // Tank (party[0]) leaves the party, its unit row stays
	var reason string
	for range 70 {
		if reason = fr.tick(); reason != "" {
			break
		}
		f.now = f.now.Add(FollowTick)
	}
	if reason == "" || !strings.Contains(reason, "left the party") {
		t.Fatalf("no left-party stop: %q", reason)
	}
}

func TestFollowTrailResetsOnAJump(t *testing.T) {
	f, p := followFixture(t, false)
	fr := newFollowerForTest(f, p, nil)
	for x := float32(0); x <= 10; x += 2 {
		leadAt(f, Point{x, 0, 0})
		if reason := fr.tick(); reason != "" {
			t.Fatalf("stopped: %s", reason)
		}
		f.now = f.now.Add(FollowTick)
	}
	leadAt(f, Point{60, 0, 0}) // a 50 yd jump
	if reason := fr.tick(); reason != "" {
		t.Fatalf("stopped: %s", reason)
	}
	st := fr.Status()
	if !lastNoteHas(st, "jumped") {
		t.Fatalf("no jump note: %+v", st)
	}
	if st.TrailYd >= 1 {
		t.Fatalf("trail_yd %.1f after a jump", st.TrailYd)
	}
}

func TestFollowRecoversWithoutAWord(t *testing.T) {
	f, p := followFixture(t, false)
	fr := newFollowerForTest(f, p, nil)
	leadAt(f, Point{30, 0, 0})
	if reason := fr.tick(); reason != "" {
		t.Fatalf("stopped: %s", reason)
	}
	f.now = f.now.Add(FollowTick)
	f.kill()
	f.dead.graveyard, f.dead.graveMap = Point{50, 0, 0}, 70
	f.dead.corpse = Corpse{Found: true, Map: 70, CorpseMap: 70, Pos: Point{25, 0, 0}}
	deadSeen := false
	for range 2000 {
		reason := fr.tick()
		if st := fr.Status(); st.Deaths == 1 && st.State == "waiting" {
			deadSeen = true
		}
		if reason != "" {
			t.Fatalf("stopped during recovery: %s", reason)
		}
		if deadSeen && fr.Status().CorpseRuns == 1 && fr.Status().State == "waiting" {
			break
		}
		f.now = f.now.Add(FollowTick)
	}
	st := fr.Status()
	if st.Deaths != 1 || st.CorpseRuns != 1 {
		t.Fatalf("deaths %d corpse_runs %d: %+v", st.Deaths, st.CorpseRuns, st)
	}
	// Waiting: no walk while the lead is out of view.
	f.units = nil
	n := len(f.walks)
	if reason := runTicks(f, fr, 20); reason != "" {
		t.Fatalf("stopped while waiting: %s", reason)
	}
	if len(f.walks) != n {
		t.Fatalf("walked while waiting after recovery")
	}
	// The lead is in view again: the mode follows behind it.
	leadAt(f, Point{20, 0, 0})
	for range 10 {
		if reason := fr.tick(); reason != "" {
			t.Fatalf("stopped: %s", reason)
		}
		if s := fr.Status().State; s == "near" || s == "walking" {
			break
		}
		f.now = f.now.Add(FollowTick)
	}
	if s := fr.Status().State; s != "near" && s != "walking" {
		t.Fatalf("state %q after the lead is back, want near/walking", s)
	}
	if len(f.chats) != 0 || len(f.gms) != 0 {
		t.Fatalf("sent a chat or GM line: chats %q gms %q", f.chats, f.gms)
	}
}

func TestFollowRecoversWhenTheWholePartyIsDead(t *testing.T) {
	f, p := followFixture(t, false)
	fr := newFollowerForTest(f, p, nil)
	leadAt(f, Point{30, 0, 0})
	if reason := fr.tick(); reason != "" {
		t.Fatalf("stopped: %s", reason)
	}
	f.now = f.now.Add(FollowTick)
	f.kill()
	f.party[0].Dead, f.party[1].Dead = true, true
	f.party[0].Online, f.party[1].Online = true, true
	f.dead.graveyard, f.dead.graveMap = Point{50, 0, 0}, 70
	f.dead.corpse = Corpse{Found: true, Map: 70, CorpseMap: 70, Pos: Point{25, 0, 0}}
	for range 2000 {
		if st := fr.Status(); st.CorpseRuns == 1 {
			return
		}
		if reason := fr.tick(); reason != "" {
			t.Fatalf("stopped with the party dead: %s", reason)
		}
		f.now = f.now.Add(FollowTick)
	}
	t.Fatalf("no recovery with the party dead: %+v", fr.Status())
}

func TestFollowAcceptsAResurrection(t *testing.T) {
	f, p := followFixture(t, false)
	fr := newFollowerForTest(f, p, nil)
	leadAt(f, Point{30, 0, 0})
	if reason := fr.tick(); reason != "" {
		t.Fatalf("stopped: %s", reason)
	}
	f.now = f.now.Add(FollowTick)
	f.kill()
	f.dead.offerAt = f.now.Add(20 * time.Second)
	f.dead.offerLater = &Offer{Name: "Heal"}
	for range 2000 {
		if st := fr.Status(); st.Resurrections == 1 {
			break
		}
		if reason := fr.tick(); reason != "" {
			t.Fatalf("stopped: %s", reason)
		}
		f.now = f.now.Add(FollowTick)
	}
	st := fr.Status()
	if st.Resurrections != 1 || st.Deaths != 1 {
		t.Fatalf("resurrections %d deaths %d: %+v", st.Resurrections, st.Deaths, st)
	}
	if f.dead.releases != 0 {
		t.Fatalf("released although a resurrection was offered")
	}
}

func TestFollowWaitsAfterBackDoesNotWalk(t *testing.T) {
	f, p := followFixture(t, false)
	fr := newFollowerForTest(f, p, nil)
	leadAt(f, Point{30, 0, 0})
	if reason := fr.tick(); reason != "" {
		t.Fatalf("stopped: %s", reason)
	}
	f.now = f.now.Add(FollowTick)
	f.kill()
	f.dead.graveyard, f.dead.graveMap = Point{50, 0, 0}, 70
	f.dead.corpse = Corpse{Found: true, Map: 70, CorpseMap: 70, Pos: Point{25, 0, 0}}
	for range 2000 {
		if st := fr.Status(); st.CorpseRuns == 1 {
			break
		}
		if reason := fr.tick(); reason != "" {
			t.Fatalf("stopped: %s", reason)
		}
		f.now = f.now.Add(FollowTick)
	}
	f.units = nil
	n := len(f.walks)
	if reason := runTicks(f, fr, 20); reason != "" {
		t.Fatalf("stopped: %s", reason)
	}
	if len(f.walks) != n {
		t.Fatalf("walked in the 10 s after recovery with the lead out of view")
	}
}

type zoneFake struct {
	*followFake
	zoneTo uint32
	zoneAt Point
}

func (z *zoneFake) AreaTrigger(ctx context.Context, id uint32) error {
	if err := z.followFake.fakeGame.AreaTrigger(ctx, id); err != nil {
		return err
	}
	if z.zoneTo != 0 {
		z.mapID, z.pos = z.zoneTo, z.zoneAt
	}
	return nil
}

func TestFollowZonesInAfterTheLead(t *testing.T) {
	f, p := followFixture(t, true)
	z := &zoneFake{followFake: f, zoneTo: 70, zoneAt: Point{95, 0, 0}}
	fr := NewFollower(z, FollowConfig{Plan: p, Lead: "Tank", WalkWatch: 2 * time.Millisecond}, f.clock())
	f.mapID, f.pos = 0, Point{90, 0, 0}
	leadAt(f, Point{95, 0, 0}) // the lead last seen 10 yd from the portal
	if reason := fr.tick(); reason != "" {
		t.Fatalf("stopped: %s", reason)
	}
	f.now = f.now.Add(FollowTick)
	f.units = nil // the lead went in
	for range 500 {
		if st := fr.Status(); st.ZoneIns == 1 {
			break
		}
		if reason := fr.tick(); reason != "" {
			t.Fatalf("stopped while zoning: %s", reason)
		}
		f.now = f.now.Add(FollowTick)
	}
	st := fr.Status()
	if st.ZoneIns != 1 {
		t.Fatalf("no zone-in: %+v", st)
	}
	if !lastNoteHas(st, "zoned into map 70") {
		t.Fatalf("no zoned note: %+v", st)
	}
	if len(f.dead.triggerIDs) == 0 || f.dead.triggerIDs[len(f.dead.triggerIDs)-1] != 286 {
		t.Fatalf("trigger ids %v", f.dead.triggerIDs)
	}
}

func TestFollowDoesNotZoneWhenTheLeadVanishedFarFromThePortal(t *testing.T) {
	f, p := followFixture(t, true)
	z := &zoneFake{followFake: f, zoneTo: 70, zoneAt: Point{95, 0, 0}}
	fr := NewFollower(z, FollowConfig{Plan: p, Lead: "Tank", WalkWatch: 2 * time.Millisecond}, f.clock())
	f.mapID, f.pos = 0, Point{90, 0, 0}
	leadAt(f, Point{220, 0, 0}) // the lead last seen 120 yd from the portal
	if reason := fr.tick(); reason != "" {
		t.Fatalf("stopped: %s", reason)
	}
	f.now = f.now.Add(FollowTick)
	f.units = nil
	reason := runTicks(f, fr, 70)
	if reason == "" || !strings.Contains(reason, "was not in view") {
		t.Fatalf("no lost stop: %q", reason)
	}
	if len(f.dead.triggerIDs) != 0 {
		t.Fatalf("zoned although the lead vanished far from the portal: %v", f.dead.triggerIDs)
	}
}

func TestFollowZoneTriggerGivesUp(t *testing.T) {
	f, p := followFixture(t, true)
	z := &zoneFake{followFake: f} // zoneTo 0: the trigger never changes the map
	fr := NewFollower(z, FollowConfig{Plan: p, Lead: "Tank", WalkWatch: 2 * time.Millisecond}, f.clock())
	f.mapID, f.pos = 0, Point{95, 0, 0}
	leadAt(f, Point{100, 0, 0})
	if reason := fr.tick(); reason != "" {
		t.Fatalf("stopped: %s", reason)
	}
	f.now = f.now.Add(FollowTick)
	f.units = nil
	var reason string
	for range 2000 {
		if reason = fr.tick(); reason != "" {
			break
		}
		f.now = f.now.Add(FollowTick)
	}
	if reason == "" || !strings.Contains(reason, "did not take the player into map 70 (3 tries)") {
		t.Fatalf("no trigger stop: %q", reason)
	}
}

func TestFollowFallbackHoldsForAPackOnTheWay(t *testing.T) {
	f, p := followFixture(t, false)
	fr := newFollowerForTest(f, p, nil)
	leadAt(f, Point{60, 0, 0})                      // seen once: no trail yet
	addHostile(f, 0x20, Point{15, 10, 0}, false, 0) // 10 yd off the way, 46 yd from the lead
	if reason := fr.tick(); reason != "" {
		t.Fatalf("stopped: %s", reason)
	}
	f.now = f.now.Add(FollowTick)
	if reason := runTicks(f, fr, 3); reason != "" {
		t.Fatalf("stopped: %s", reason)
	}
	if s := fr.Status().State; s != "holding" {
		t.Fatalf("state %q, want holding", s)
	}
	if len(f.walks) != 0 {
		t.Fatalf("walked past a pack: %v", f.walks)
	}
	reason := runTicks(f, fr, 121)
	if reason == "" || !strings.Contains(reason, "stayed blocked") {
		t.Fatalf("no blocked stop after 60 s: %q", reason)
	}
}

func TestFollowAssistAttacksTheTanksTarget(t *testing.T) {
	f, p := followFixture(t, false)
	fr := newFollowerForTest(f, p, func(c *FollowConfig) { c.Assist = true })
	leadAt(f, Point{30, 0, 0})
	mob := addHostile(f, 0x20, Point{33, 0, 0}, true, tank)
	leadRow(f).Target = mob.GUID
	f.pos = Point{20, 0, 0}
	if reason := runTicks(f, fr, 3); reason != "" {
		t.Fatalf("stopped: %s", reason)
	}
	if len(f.targets) == 0 || f.targets[len(f.targets)-1] != mob.GUID {
		t.Fatalf("no target on the tank's target: %v", f.targets)
	}
	if f.attacks != 1 {
		t.Fatalf("attacks %d, want 1", f.attacks)
	}
	mob.Dead, mob.InCombat = true, false
	leadRow(f).Target = 0
	if reason := runTicks(f, fr, 3); reason != "" {
		t.Fatalf("stopped: %s", reason)
	}
	if f.stops != 1 {
		t.Fatalf("stops %d, want 1", f.stops)
	}
}

func TestFollowAssistOffWhileTheBrainRuns(t *testing.T) {
	f, p := followFixture(t, false)
	br := &fakeBrain{running: true}
	fr := newFollowerForTest(f, p, func(c *FollowConfig) {
		c.Assist = true
		c.BrainRunning = func() bool { return br.running }
	})
	leadAt(f, Point{30, 0, 0})
	mob := addHostile(f, 0x20, Point{33, 0, 0}, true, tank)
	leadRow(f).Target = mob.GUID
	if reason := runTicks(f, fr, 3); reason != "" {
		t.Fatalf("stopped: %s", reason)
	}
	if len(f.targets) != 0 || f.attacks != 0 {
		t.Fatalf("assisted while the brain runs: targets %v attacks %d", f.targets, f.attacks)
	}
	if !lastNoteHas(fr.Status(), "the brain runs") {
		t.Fatalf("no brain note: %+v", fr.Status())
	}
}

func TestFollowCountsSecondsOver60(t *testing.T) {
	f, p := followFixture(t, false)
	fr := newFollowerForTest(f, p, nil)
	leadAt(f, Point{70, 0, 0}) // the lead 70 yd out: every walk ends partial, the mode never arrives
	f.walkHook = func(ctx context.Context, g *fakeGame, to Point) (WalkResult, error) {
		return WalkResult{Result: "partial", Reason: "blocked", DistLeft: 60}, nil
	}
	if reason := fr.tick(); reason != "" {
		t.Fatalf("stopped: %s", reason)
	}
	f.now = f.now.Add(FollowTick)
	var reason string
	for range 2000 {
		if reason = fr.tick(); reason != "" {
			break
		}
		f.now = f.now.Add(FollowTick)
	}
	if reason == "" || !strings.Contains(reason, "cannot reach the lead bot") {
		t.Fatalf("no cannot-reach stop: %q", reason)
	}
	if st := fr.Status(); st.SecondsOver60 <= 0 {
		t.Fatalf("seconds_over_60 not counted: %+v", st)
	}
}

func TestFollowSitsUntil45(t *testing.T) {
	f, p := followFixture(t, false)
	fr := newFollowerForTest(f, p, nil)
	leadAt(f, Point{30, 0, 0})
	f.row.StandState = "sit"
	if reason := runTicks(f, fr, 3); reason != "" {
		t.Fatalf("stopped: %s", reason)
	}
	if len(f.walks) != 0 {
		t.Fatalf("walked while sitting with the lead at 30 yd")
	}
	if st := fr.Status(); st.State != "sitting" {
		t.Fatalf("state %q, want sitting", st.State)
	}
	leadAt(f, Point{50, 0, 0})
	if reason := runTicks(f, fr, 5); reason != "" {
		t.Fatalf("stopped: %s", reason)
	}
	if len(f.walks) == 0 {
		t.Fatalf("no walk with the lead at 50 yd while sitting")
	}
}

func TestFollowSetLeadSwitches(t *testing.T) {
	f, p := followFixture(t, false)
	fr := newFollowerForTest(f, p, nil)
	leadAt(f, Point{10, 0, 0})
	if reason := fr.tick(); reason != "" {
		t.Fatalf("stopped: %s", reason)
	}
	f.now = f.now.Add(FollowTick)
	fr.SetLead("Heal")
	if reason := fr.tick(); reason != "" {
		t.Fatalf("stopped: %s", reason)
	}
	st := fr.Status()
	if st.Lead != "Heal" {
		t.Fatalf("lead %q, want Heal", st.Lead)
	}
	if !lastNoteHas(st, "the lead bot is now Heal (was Tank)") {
		t.Fatalf("no switch note: %+v", st)
	}
	if st.TrailYd != 0 {
		t.Fatalf("trail_yd %.1f after a switch", st.TrailYd)
	}
}

func TestFollowWalksUnderTheMoveLock(t *testing.T) {
	f, p := followFixture(t, false)
	br := &fakeBrain{}
	f.moveHeld = func() int { return br.held }
	fr := newFollowerForTest(f, p, func(c *FollowConfig) { c.Brain = br })
	for x := float32(0); x <= 40; x += 2 {
		leadAt(f, Point{x, 0, 0})
		if reason := fr.tick(); reason != "" {
			t.Fatalf("tick at x %.0f stopped: %s", x, reason)
		}
		f.now = f.now.Add(FollowTick)
	}
	if len(f.walks) == 0 {
		t.Fatalf("no walk; status %+v", fr.Status())
	}
	for _, m := range f.moveLog {
		if m != "walk:1" {
			t.Fatalf("a walk without exactly one hold: %v", f.moveLog)
		}
	}
	if br.holds != len(f.moveLog) {
		t.Fatalf("holds %d moves %d", br.holds, len(f.moveLog))
	}
}
