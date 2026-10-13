package dungeon

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/pjerra/turtle-agent/internal/objects"
	"github.com/pjerra/turtle-agent/internal/packet"
)

// Tests for the fixes of the first live Uldaman run (issue #73): stealthed packs, the rest gate, the revive race,
// packs that aggro on a walk and the setup list.

const ambushPlan = `{"version":1,"name":"t","map":70,"entrance":{"x":0,"y":0,"z":0},"legs":[
 {"name":"a","to":{"x":0,"y":0,"z":0},
  "pull_groups":[{"entries":[10,10],"ambush":true,"positions":[{"x":30,"y":0,"z":0},{"x":34,"y":0,"z":0}]},
                 {"entries":[3],"positions":[{"x":60,"y":0,"z":0}]}]}]}`

func notes(d *Driver) string { return strings.Join(d.Report().Notes, "|") }

// Stealthed members cannot be pulled: the party is walked through the group's positions and, when nothing shows
// itself, the group counts as cleared (noted). Nothing is targeted or pulled for it.
func TestDriverWalksThroughAStealthedGroup(t *testing.T) {
	g := newFake()
	g.add(201, 10, Point{30, 0, 0}).stealth()
	g.add(202, 10, Point{34, 0, 0}).stealth()
	g.add(203, 3, Point{60, 0, 0})
	d := NewDriver(mustPlan(t, ambushPlan), g, Config{}, g.clock())
	if err := d.Run(context.Background(), 0); err != nil {
		t.Fatalf("%v\n%+v", err, d.Report())
	}
	// The lurkers were never targeted; the ordinary group after them was pulled as usual.
	if fmt.Sprint(g.targets) != fmt.Sprint([]packet.GUID{203}) || g.count(".bot action pullback") != 1 || g.count(".bot action follow") != 1 {
		t.Fatalf("targets %v chats %q", g.targets, g.chats)
	}
	if fmt.Sprint(g.walks[:2]) != "[{30 0 0} {34 0 0}]" {
		t.Fatalf("walks %v", g.walks)
	}
	l := d.Report().Legs[0]
	if l.Ambushes != 1 || l.Pulls != 1 || l.PullRetries != 0 || l.WalkAggros != 0 {
		t.Fatalf("%+v", l)
	}
	if n := notes(d); !strings.Contains(n, "stealthed group [10 10] passed with no fight; counted as cleared") {
		t.Fatalf("%s", n)
	}
	// Two pauses of AmbushPause at the two positions.
	if l.Seconds < 12 {
		t.Fatalf("%.0f s", l.Seconds)
	}
}

// A mob that shows itself while the party stands at a position is fought until it is dead; the group is then cleared.
func TestDriverFightsWhatAStealthedGroupRevealsItself(t *testing.T) {
	g := newFake()
	a := g.add(201, 10, Point{30, 0, 0}).stealth()
	g.add(202, 10, Point{34, 0, 0}).stealth()
	g.add(203, 3, Point{60, 0, 0})
	g.onWalk = func(g *fakeGame, to Point) {
		if to == (Point{30, 0, 0}) { // the first one notices the party
			a.Auras, a.InCombat, a.Health, a.Target = nil, true, 60, leader
			a.dieAt = g.now.Add(5 * time.Second)
		}
	}
	d := NewDriver(mustPlan(t, ambushPlan), g, Config{}, g.clock())
	if err := d.Run(context.Background(), 0); err != nil {
		t.Fatalf("%v\n%+v", err, d.Report())
	}
	l := d.Report().Legs[0]
	if l.Ambushes != 1 || l.Pulls != 2 || g.count(".bot action attack") < 1 {
		t.Fatalf("%+v chats %q", l, g.chats)
	}
	// The second lurker never showed itself and is still alive and hidden: the group goes on as passed.
	if n := notes(d); !strings.Contains(n, "stealthed group [10 10]: 1 fights, 1 members still alive in view; went on") {
		t.Fatalf("%s", n)
	}
}

// With nothing of the group in view the plan's flag decides: the party walks to the positions (and does not take the
// pack as already dead).
func TestDriverAmbushFlagAloneWalksThePositions(t *testing.T) {
	g := newFake()
	g.add(203, 3, Point{60, 0, 0})
	d := NewDriver(mustPlan(t, ambushPlan), g, Config{}, g.clock())
	if err := d.Run(context.Background(), 0); err != nil {
		t.Fatalf("%v", err)
	}
	if fmt.Sprint(g.walks[:2]) != "[{30 0 0} {34 0 0}]" || strings.Contains(notes(d), "taken as already dead") {
		t.Fatalf("walks %v notes %s", g.walks, notes(d))
	}
}

// Members in view that carry no stealth aura are pulled as usual, whatever the plan's flag says; in a mixed pack the
// one that can be seen is the target even when a hidden one stands nearer, and a hidden member that does not join
// does not hold the fight open.
func TestDriverPullsTheVisibleMemberOfAMixedPack(t *testing.T) {
	g := newFake()
	g.add(201, 10, Point{30, 0, 0}).stealth() // nearer than the other, hidden
	g.add(202, 10, Point{34, 0, 0})
	g.add(203, 3, Point{60, 0, 0})
	d := NewDriver(mustPlan(t, ambushPlan), g, Config{}, g.clock())
	if err := d.Run(context.Background(), 0); err != nil {
		t.Fatalf("%v\n%+v", err, d.Report())
	}
	if fmt.Sprint(g.targets) != fmt.Sprint([]packet.GUID{202, 203}) {
		t.Fatalf("targets %v", g.targets)
	}
	if l := d.Report().Legs[0]; l.Ambushes != 0 || l.Pulls != 2 || l.PullRetries != 0 {
		t.Fatalf("%+v", l)
	}
	if g.units[0].Dead {
		t.Fatal("the hidden member was not fought")
	}
}

// A pack that was not flagged and turns out stealthed once in view is walked past too (the aura decides).
func TestDriverStealthSeenOnApproachIsWalkedPast(t *testing.T) {
	g := newFake()
	g.add(203, 3, Point{200, 0, 0}).stealth() // beyond the fake's 150 yd view: the approach brings it into view
	p := mustPlan(t, `{"version":1,"name":"t","map":70,"entrance":{"x":0,"y":0,"z":0},"legs":[
	 {"to":{"x":0,"y":0,"z":0},"pull_groups":[{"entries":[3],"positions":[{"x":200,"y":0,"z":0}]}]}]}`)
	d := NewDriver(p, g, Config{}, g.clock())
	if err := d.Run(context.Background(), 0); err != nil {
		t.Fatalf("%v\n%+v", err, d.Report())
	}
	if g.count(".bot action pullback") != 0 || !strings.Contains(notes(d), "passed with no fight") {
		t.Fatalf("chats %q notes %s", g.chats, notes(d))
	}
}

const restPlan = `{"version":1,"name":"t","map":70,"entrance":{"x":0,"y":0,"z":0},"legs":[
 {"to":{"x":0,"y":0,"z":0},"pull_groups":[{"entries":[1],"positions":[{"x":20,"y":0,"z":0}]}]}]}`

// The rest gate does not start while the party fights: it waits for the fight to end (helping with the attack order),
// sends the rest order after it, and its timeout counts from the end of the fight.
func TestRestGateWaitsForTheFightToEnd(t *testing.T) {
	g := newFake()
	g.add(201, 1, Point{20, 0, 0})
	m := g.add(99, 99, Point{10, 0, 0})
	m.InCombat, m.Target, m.Health = true, leader, 50
	m.dieAt = g.now.Add(30 * time.Second)
	g.party[0].Health = 700 // 77%: never rested, so the gate runs into its timeout
	d := NewDriver(mustPlan(t, restPlan), g, Config{RestTimeout: 20 * time.Second}, g.clock())
	if err := d.Run(context.Background(), 0); err != nil {
		t.Fatalf("%v\n%+v", err, d.Report())
	}
	if !strings.Contains(notes(d), "rest gate gave up after 20 s") {
		t.Fatalf("%s", notes(d))
	}
	// The first gate lasted the 30 s fight plus the 20 s timeout, not 20 s from the start.
	if rs := d.Report().Legs[0].RestSeconds; rs < 50 {
		t.Fatalf("rest %.0f s", rs)
	}
	attack, rest := -1, -1
	for i, c := range g.chats {
		switch {
		case c == ".bot action attack" && attack < 0:
			attack = i
		case c == ".bot action rest" && rest < 0:
			rest = i
		}
	}
	if attack < 0 || rest < 0 || attack > rest {
		t.Fatalf("attack at %d, rest at %d: %q", attack, rest, g.chats)
	}
	if g.targets[0] != 99 {
		t.Fatalf("targets %v", g.targets)
	}
}

// A fight that never ends fails the gate after FightTimeout instead of waiting for ever.
func TestRestGateGivesUpOnAFightThatNeverEnds(t *testing.T) {
	g := newFake()
	g.add(201, 1, Point{20, 0, 0})
	m := g.add(99, 99, Point{10, 0, 0})
	m.InCombat, m.Target = true, leader
	d := NewDriver(mustPlan(t, restPlan), g, Config{FightTimeout: 40 * time.Second}, g.clock())
	err := d.Run(context.Background(), 0)
	if err == nil || !strings.Contains(err.Error(), "still in combat after 40 s at a rest gate") {
		t.Fatalf("%v", err)
	}
}

// A caster whose mana does not rise (the priest who never drank) holds the gate for ManaStall, not the whole timeout.
func TestRestGatePassesACasterWhoDoesNotDrink(t *testing.T) {
	g := newFake()
	g.add(201, 1, Point{20, 0, 0})
	g.party[1].Power = 200 // 50% of 400, below the 75% threshold, and flat
	d := NewDriver(mustPlan(t, restPlan), g, Config{}, g.clock())
	if err := d.Run(context.Background(), 0); err != nil {
		t.Fatalf("%v\n%+v", err, d.Report())
	}
	rs := d.Report().Legs[0].RestSeconds // two gates (leg start and the pull): 30 s each, not 150
	if rs < 60 || rs > 80 || !strings.Contains(notes(d), "rest gate passed with Heal not drinking (mana 50%); mana did not rise in 30 s") || strings.Contains(notes(d), "gave up") {
		t.Fatalf("rest %.0f s notes %s", rs, notes(d))
	}

	// A trickle of regeneration (2.5% in 30 s) is not drinking either.
	g = newFake()
	g.add(201, 1, Point{20, 0, 0})
	g.party[1].Power = 200
	g.onTick = func(g *fakeGame) {
		if g.resting {
			g.party[1].Power++
		}
	}
	d = NewDriver(mustPlan(t, restPlan), g, Config{}, g.clock())
	if err := d.Run(context.Background(), 0); err != nil {
		t.Fatalf("%v", err)
	}
	if !strings.Contains(notes(d), "not drinking") {
		t.Fatalf("%s", notes(d))
	}

	// The tolerance is for mana only: a hurt member still holds the gate to its timeout, and the note names both.
	g = newFake()
	g.add(201, 1, Point{20, 0, 0})
	g.party[1].Power = 200
	g.party[0].Health = 700
	d = NewDriver(mustPlan(t, restPlan), g, Config{RestTimeout: 60 * time.Second}, g.clock())
	if err := d.Run(context.Background(), 0); err != nil {
		t.Fatalf("%v", err)
	}
	if n := notes(d); !strings.Contains(n, "rest gate gave up after 60 s ([Tank health 77% Heal not drinking (mana 50%)])") {
		t.Fatalf("%s", n)
	}
}

// A caster who drinks (mana climbing 20% per look) is waited for, not let pass.
func TestRestGateWaitsForACasterWhoDrinks(t *testing.T) {
	g := newFake()
	g.add(201, 1, Point{20, 0, 0})
	g.healOnRest = true
	g.party[1].Power = 100 // 25%
	d := NewDriver(mustPlan(t, restPlan), g, Config{}, g.clock())
	if err := d.Run(context.Background(), 0); err != nil {
		t.Fatalf("%v", err)
	}
	if strings.Contains(notes(d), "not drinking") || g.party[1].Power < 300 {
		t.Fatalf("power %d notes %s", g.party[1].Power, notes(d))
	}
}

// After a GM revive the dead flag is polled for a few seconds: a party list that follows 3 s later is not judged
// still dead, and no second revive goes out.
func TestReviveWaitsForThePartyListToFollow(t *testing.T) {
	g := newFake()
	g.add(201, 1, Point{20, 0, 0})
	g.party[1].Dead = true
	g.reviveDelay = 3 * time.Second
	d := NewDriver(mustPlan(t, restPlan), g, Config{Shortcuts: true}, g.clock())
	if err := d.Run(context.Background(), 0); err != nil {
		t.Fatalf("%v\n%+v", err, d.Report())
	}
	if n := countOf(g.gms, ".revive Heal"); n != 1 || len(d.Report().Shortcuts) != 1 {
		t.Fatalf("gms %q shortcuts %+v", g.gms, d.Report().Shortcuts)
	}
}

// A member that stays dead gets a second revive after the wait, and then the run fails.
func TestReviveGivesUpAfterTwoRevives(t *testing.T) {
	g := newFake()
	g.add(201, 1, Point{20, 0, 0})
	g.party[1].Dead = true
	g.reviveDelay = -1
	d := NewDriver(mustPlan(t, restPlan), g, Config{Shortcuts: true}, g.clock())
	err := d.Run(context.Background(), 0)
	if err == nil || !strings.Contains(err.Error(), "Heal is still dead after two GM revives") || countOf(g.gms, ".revive Heal") != 2 {
		t.Fatalf("%v gms %q", err, g.gms)
	}
	if s := d.Report().Shortcuts; len(s) != 2 || s[1].At.Sub(s[0].At) < 5*time.Second {
		t.Fatalf("the second revive came too soon: %+v", s)
	}
}

func countOf(in []string, s string) int {
	n := 0
	for _, c := range in {
		if c == s {
			n++
		}
	}
	return n
}

// The walk of a use_object step is not preceded by a rest gate: a pack that is on the party when it is due is fought
// first (the fake starts the fight as the gate looks at the party), then the walk goes on.
const stepPlan = `{"version":1,"name":"t","map":70,"entrance":{"x":0,"y":0,"z":0},"legs":[
 {"name":"a","to":{"x":0,"y":0,"z":0},"steps":[{"use_object":{"entry":124371}}]}]}`

func stepFixture(t *testing.T) (*fakeGame, *fakeUnit, *Plan) {
	g := newFake()
	m := g.add(99, 99, Point{5, 0, 0})
	g.objs = append(g.objs, objects.GameObject{GUID: keystone, Entry: 124371, X: 20, State: "ready"})
	g.afterGate = func(g *fakeGame) {
		m.InCombat, m.Target, m.Health = true, leader, 50
		m.dieAt = g.now.Add(8 * time.Second)
	}
	return g, m, mustPlan(t, stepPlan)
}

func TestWalkFightsAPackThatIsAlreadyOnTheParty(t *testing.T) {
	g, m, p := stepFixture(t)
	d := NewDriver(p, g, Config{}, g.clock())
	if err := d.Run(context.Background(), 0); err != nil {
		t.Fatalf("%v\n%+v", err, d.Report())
	}
	l := d.Report().Legs[0]
	if l.WalkAggros != 1 || l.Pulls != 1 || g.count(".bot action attack") < 1 || len(g.walks) != 1 || g.walks[0] != (Point{20, 0, 0}) || !m.Dead || len(g.used) != 1 {
		t.Fatalf("%+v walks %v chats %q", l, g.walks, g.chats)
	}
	if !strings.Contains(notes(d), "leg 0: mob99 aggroed during a walk") {
		t.Fatalf("%s", notes(d))
	}
}

// A walk that is pulled into a fight that does not end fails the run instead of waiting for ever.
func TestWalkGivesUpOnAFightThatNeverEnds(t *testing.T) {
	g, m, p := stepFixture(t)
	m.dieAt = time.Time{}
	g.afterGate = func(g *fakeGame) {
		m.InCombat, m.Target = true, leader
	}
	d := NewDriver(p, g, Config{FightTimeout: 30 * time.Second}, g.clock())
	err := d.Run(context.Background(), 0)
	if err == nil || !strings.Contains(err.Error(), "the fight on the way lasted more than 30 s") {
		t.Fatalf("%v", err)
	}
}

// A pack that aggros during a long walk stops it (the walk's context is cancelled), is fought until combat ends, and
// the same walk is then made again. It counts as an extra pull.
func TestWalkIsStoppedByAggroFoughtAndResumed(t *testing.T) {
	g := newFake()
	g.add(90, 90, Point{20, 0, 0})
	g.aggroGUID = 90
	p := mustPlan(t, `{"version":1,"name":"t","map":70,"entrance":{"x":0,"y":0,"z":0},"legs":[
	 {"to":{"x":80,"y":0,"z":0},"pull_groups":[{"entries":[1],"positions":[{"x":100,"y":0,"z":0}]}]}]}`)
	g.add(201, 1, Point{100, 0, 0})
	blocked := 0
	g.walkHook = func(ctx context.Context, g *fakeGame, to Point) (WalkResult, error) {
		if blocked == 0 && to == (Point{80, 0, 0}) {
			blocked++
			g.aggro.Store(true) // the pack notices the party half way
			<-ctx.Done()
			return WalkResult{Result: "partial", Reason: "cancelled"}, ctx.Err()
		}
		g.pos = to
		return WalkResult{Result: "arrived"}, nil
	}
	g.onChat = func(g *fakeGame, text string) { // the party kills the pack when told to attack
		if text == ".bot action attack" {
			for _, u := range g.units {
				if u.GUID == 90 && u.dieAt.IsZero() && !u.Dead {
					u.dieAt = g.now.Add(6 * time.Second)
				}
			}
		}
	}
	d := NewDriver(p, g, Config{WalkWatch: 2 * time.Millisecond}, g.clock())
	if err := d.Run(context.Background(), 0); err != nil {
		t.Fatalf("%v\n%+v", err, d.Report())
	}
	l := d.Report().Legs[0]
	if l.WalkAggros != 1 || l.Pulls != 2 || g.walks[0] != (Point{80, 0, 0}) || g.walks[1] != (Point{80, 0, 0}) || g.count(".bot action attack") < 1 {
		t.Fatalf("%+v walks %v chats %q", l, g.walks, g.chats)
	}
	if g.targets[0] != 90 || !g.units[0].Dead {
		t.Fatalf("targets %v", g.targets)
	}
	// The cancelled walk was not counted as a failed try.
	if strings.Contains(notes(d), "failed") {
		t.Fatalf("%s", notes(d))
	}
}

func TestSetupShortcutsAreInTheReport(t *testing.T) {
	g, p := runFixture(t)
	d := NewDriver(p, g, Config{Setup: []string{"go entrance", "levelup -15"}}, g.clock())
	if r := d.Report(); fmt.Sprint(r.SetupShortcuts) != "[go entrance levelup -15]" {
		t.Fatalf("%+v", r.SetupShortcuts)
	}
	if err := d.Run(context.Background(), 0); err != nil {
		t.Fatal(err)
	}
	rep := d.Report()
	if fmt.Sprint(rep.SetupShortcuts) != "[go entrance levelup -15]" || len(rep.Shortcuts) != 0 {
		t.Fatalf("%+v", rep)
	}
	last := g.events[len(g.events)-1]
	if last.data["setup_shortcuts"] != 2 {
		t.Fatalf("%+v", last)
	}
	// None given: an empty list, not null.
	g, p = runFixture(t)
	if r := NewDriver(p, g, Config{}, g.clock()).Report(); r.SetupShortcuts == nil || len(r.SetupShortcuts) != 0 {
		t.Fatalf("%#v", r.SetupShortcuts)
	}
}

func TestStealthed(t *testing.T) {
	g := newFake()
	if Stealthed(g.add(1, 1, Point{}).Unit) {
		t.Error("no aura")
	}
	if !Stealthed(g.add(2, 1, Point{}).stealth().Unit) {
		t.Error("5916")
	}
	// Decided by spell id: detection auras (common on ordinary spawns) and look-alike names are no stealth.
	u := g.add(3, 1, Point{})
	for _, a := range []objects.Aura{{SpellID: 8279, Name: "Stealth Detection"}, {SpellID: 18950, Name: "Invisibility and Stealth Detection"},
		{SpellID: 23330, Name: "No Stealth Detection"}, {SpellID: 6151, Name: "Sneaky"}, {SpellID: 7165, Name: "Battle Stance"}} {
		u.Auras = []objects.Aura{a}
		if Stealthed(u.Unit) {
			t.Errorf("%s (%d) is not stealth", a.Name, a.SpellID)
		}
	}
	u.Auras = []objects.Aura{{SpellID: 22766}} // Sneak, no name known
	if !Stealthed(u.Unit) {
		t.Error("sneak by id")
	}
}

// A pack with Stealth Detection is an ordinary pack: it is pulled, not walked past.
func TestDriverPullsAPackWithAStealthDetectionAura(t *testing.T) {
	g := newFake()
	for _, e := range []packet.GUID{201, 202} {
		u := g.add(e, 10, Point{30 + float32(e-201)*4, 0, 0})
		u.Auras = []objects.Aura{{SpellID: 8279, Name: "Stealth Detection"}}
	}
	g.add(203, 3, Point{60, 0, 0})
	d := NewDriver(mustPlan(t, ambushPlan), g, Config{}, g.clock())
	if err := d.Run(context.Background(), 0); err != nil {
		t.Fatalf("%v\n%+v", err, d.Report())
	}
	if l := d.Report().Legs[0]; l.Ambushes != 0 || l.Pulls != 2 || fmt.Sprint(g.targets) != fmt.Sprint([]packet.GUID{201, 203}) {
		t.Fatalf("%+v targets %v", l, g.targets)
	}
}

// A boss that carries a detection aura is fought: it does not count as untouched and "dead" before the pull.
func TestDriverFightsABossWithAStealthDetectionAura(t *testing.T) {
	g := newFake()
	b := g.add(307, 7, Point{40, 0, 0})
	b.Auras = []objects.Aura{{SpellID: 18950, Name: "Invisibility and Stealth Detection"}}
	p := mustPlan(t, `{"version":1,"name":"t","map":70,"entrance":{"x":0,"y":0,"z":0},"legs":[
	 {"name":"boss","to":{"x":0,"y":0,"z":0},"boss":{"entry":7,"position":{"x":40,"y":0,"z":0}}}]}`)
	d := NewDriver(p, g, Config{}, g.clock())
	if err := d.Run(context.Background(), 0); err != nil {
		t.Fatalf("%v\n%+v", err, d.Report())
	}
	if !b.Dead || g.count(".bot action pullback") != 1 || d.Report().Legs[0].Pulls != 1 {
		t.Fatalf("dead %v chats %q %+v", b.Dead, g.chats, d.Report().Legs[0])
	}
	// Even a boss that really is stealthed is fought: only a pull group walks past stealth.
	g = newFake()
	b = g.add(307, 7, Point{40, 0, 0}).stealth()
	b.Hostile = true
	d = NewDriver(p, g, Config{PullAttempts: 1}, g.clock())
	err := d.Run(context.Background(), 0)
	if g.count(".bot action pullback") != 1 || err == nil {
		t.Fatalf("a stealthed boss is still pulled once (the fake gives no fight): chats %q err %v", g.chats, err)
	}
}

// A healer that is low on mana and does not drink is waited for like before (the whole gate, then the run goes on with
// a note); a healer at 30% or more, and any other caster, is let pass after 30 s.
func TestRestGateWaitsOutALowHealer(t *testing.T) {
	g := newFake()
	g.add(201, 1, Point{20, 0, 0})
	g.party[1].Power = 100 // the priest at 25%, flat
	d := NewDriver(mustPlan(t, restPlan), g, Config{RestTimeout: 60 * time.Second}, g.clock())
	if err := d.Run(context.Background(), 0); err != nil {
		t.Fatalf("%v\n%+v", err, d.Report())
	}
	if n := notes(d); !strings.Contains(n, "rest gate gave up after 60 s ([Heal mana 25%])") || strings.Contains(n, "not drinking") {
		t.Fatalf("%s", n)
	}
	if rs := d.Report().Legs[0].RestSeconds; rs < 120 {
		t.Fatalf("two full gates expected: %.0f s", rs)
	}

	// At 30% the healer passes after the stall (the 30% edge counts).
	g = newFake()
	g.add(201, 1, Point{20, 0, 0})
	g.party[1].Power = 120
	d = NewDriver(mustPlan(t, restPlan), g, Config{}, g.clock())
	if err := d.Run(context.Background(), 0); err != nil {
		t.Fatalf("%v", err)
	}
	if !strings.Contains(notes(d), "Heal not drinking (mana 30%)") {
		t.Fatalf("%s", notes(d))
	}

	// A mage at 25% that does not drink passes; the healer is at full mana.
	g = newFake()
	g.add(201, 1, Point{20, 0, 0})
	const mage packet.GUID = 0x13
	g.party = append(g.party, Member{Name: "Mage", GUID: mage, Online: true, Health: 500, MaxHealth: 500, PowerType: 0, Power: 100, MaxPower: 400})
	g.partyClass[mage] = 8
	d = NewDriver(mustPlan(t, restPlan), g, Config{}, g.clock())
	if err := d.Run(context.Background(), 0); err != nil {
		t.Fatalf("%v", err)
	}
	if n := notes(d); !strings.Contains(n, "Mage not drinking (mana 25%)") || strings.Contains(n, "gave up") {
		t.Fatalf("%s", n)
	}
}

// An attacker that flickers in and out of the 60 yd line (or an evade-bugged mob that keeps its combat flag) resets
// the gate's fight and rest clocks again and again: the gate as a whole is capped, with a clear error.
func TestRestGateIsCappedOverall(t *testing.T) {
	g := newFake()
	g.add(201, 1, Point{20, 0, 0})
	m := g.add(99, 99, Point{10, 0, 0})
	m.Target = leader
	g.party[0].Health = 700 // never rested
	flick := false
	g.onTick = func(g *fakeGame) { // in combat on every other look
		flick = !flick
		m.InCombat = flick
	}
	d := NewDriver(mustPlan(t, restPlan), g, Config{RestTimeout: 20 * time.Second, FightTimeout: 40 * time.Second}, g.clock())
	err := d.Run(context.Background(), 0)
	if err == nil || !strings.Contains(err.Error(), "the rest gate has gone on for more than 100 s (fights keep interrupting it)") {
		t.Fatalf("%v", err)
	}
	if rs := d.Report().Legs[0].RestSeconds; rs < 100 || rs > 110 {
		t.Fatalf("%.0f s", rs)
	}
}
