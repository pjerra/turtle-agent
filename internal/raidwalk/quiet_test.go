package raidwalk

import (
	"strings"
	"testing"
	"time"

	"github.com/pjerra/turtle-agent/internal/dungeon"
	"github.com/pjerra/turtle-agent/internal/objects"
	"github.com/pjerra/turtle-agent/internal/packet"
)

// quietNow is the read time the quiet tests use.
var quietNow = time.Unix(1700000000, 0)

// quietAt is QuietAt with a fresh read memory: one read, so D-A's two-read idle exemption exempts nothing.
func quietAt(units []objects.Unit, patrols []QuietPatrol, p dungeon.Point) (bool, string) {
	return QuietAt(units, patrols, p, NewQuietIdleReads(), nil, quietNow)
}

// quietHome is QuietAt after the two reads D-A's exemption needs: the same units, quietIdleHold apart at the same spot.
func quietHome(units []objects.Unit, patrols []QuietPatrol, p dungeon.Point) (bool, string) {
	return quietProvenAt(units, patrols, p, nil, atHomeAll(units))
}

// atHomeAll is the home list that proves every given unit at home: a spawn of
// its entry where it stands. Nil units: nil (no plan data, no exemption).
func atHomeAll(units []objects.Unit) QuietHomeSpawns {
	if units == nil {
		return nil
	}
	homes := make(QuietHomeSpawns, 0, len(units))
	for _, u := range units {
		homes = append(homes, QuietHomeSpawn{Entry: u.Entry, At: dungeon.Point{X: u.X, Y: u.Y, Z: u.Z}})
	}
	return homes
}

// quietProvenAt is QuietAt after the two reads D-A's exemption needs, with the
// given plan spawns: the same units, quietIdleHold apart at the same spot.
func quietProvenAt(units []objects.Unit, patrols []QuietPatrol, p dungeon.Point, idle *QuietIdleReads, homes QuietHomeSpawns) (bool, string) {
	if idle == nil {
		idle = NewQuietIdleReads()
	}
	QuietAt(units, patrols, p, idle, homes, quietNow)
	return QuietAt(units, patrols, p, idle, homes, quietNow.Add(quietIdleHold))
}

// quietProven is quietProvenAt with a fresh read memory.
func quietProven(units []objects.Unit, patrols []QuietPatrol, p dungeon.Point, homes QuietHomeSpawns) (bool, string) {
	return quietProvenAt(units, patrols, p, nil, homes)
}

// Codex HIGH (turtle-agent#360): stillness is not proof of home. A paused
// patrol at 31-45 yd passed the old stillness check, and the raid could be
// summoned next to it. The idle exemption now needs a proven home: a plan
// spawn (by spawn guid when the plan names one, else by entry) within 3 yd of
// where the unit stands, and never for a patrol group's unit. No plan spawn
// data: no exemption (the strict 45 yd rule).
func TestQuietAtIdleNeedsProvenHome(t *testing.T) {
	p := dungeon.Point{}
	const entry = 14532
	at := func(x float32) objects.Unit {
		u := hostileAt("Razzashi Venombrood", x, false, false)
		u.Entry = entry
		return u
	}
	spawn := func(x float32, patrol bool) QuietHomeSpawns {
		return QuietHomeSpawns{{Entry: entry, At: dungeon.Point{X: x}, Patrol: patrol}}
	}
	// (a) still and calm at 35 yd, but 8 yd from its plan spawn: blocks.
	if ok, reason := quietProven([]objects.Unit{at(35)}, nil, p, spawn(43, false)); ok {
		t.Fatalf("(a) idle 35 yd out but 8 yd from its spawn: quiet, want blocked (%s)", reason)
	}
	// (b) the same unit within 2 yd of its spawn: exempt after the two reads.
	if ok, reason := quietProven([]objects.Unit{at(35)}, nil, p, spawn(36.5, false)); !ok {
		t.Fatalf("(b) idle 35 yd out at its spawn: not quiet (%s)", reason)
	}
	// One read is still no proof, even at home.
	if ok, _ := QuietAt([]objects.Unit{at(35)}, nil, p, NewQuietIdleReads(), spawn(36.5, false), quietNow); ok {
		t.Fatal("idle at home on one read: quiet, want blocked")
	}
	// (c) a patrol group's unit at its own spawn: never exempt.
	if ok, reason := quietProven([]objects.Unit{at(35)}, nil, p, spawn(35, true)); ok {
		t.Fatalf("(c) a patrol-group unit at its spawn: quiet, want blocked (%s)", reason)
	}
	// (d) no plan spawn data: no exemption.
	if ok, reason := quietProven([]objects.Unit{at(35)}, nil, p, nil); ok {
		t.Fatalf("(d) idle 35 yd out with no plan data: quiet, want blocked (%s)", reason)
	}
}

// QuietHomeSpawns marks the plan's patrol groups: their units are never
// exempt, even standing at the group's own positions. The boss and the
// left-alone spawns count as homes.
func TestQuietHomeSpawnsMarksPatrolGroups(t *testing.T) {
	plan := &dungeon.Plan{Legs: []dungeon.Leg{{
		PullGroups: []dungeon.PullGroup{
			{Entries: []uint32{7}, Positions: []dungeon.Point{{X: 40}}},
			{Entries: []uint32{9}, Positions: []dungeon.Point{{X: 35}}, Patrol: &dungeon.Patrol{Entries: []uint32{9}}},
		},
		Boss:  &dungeon.Boss{Entry: 11, Position: dungeon.Point{X: 36}},
		Leave: []dungeon.Leave{{Entry: 12, Spawn: 77, At: dungeon.Point{X: 38}}},
	}}}
	homes := PlanHomeSpawns(plan)
	byEntry := map[uint32]QuietHomeSpawn{}
	for _, h := range homes {
		byEntry[h.Entry] = h
	}
	if h := byEntry[7]; h.Patrol {
		t.Fatal("a stationary group's spawn is marked patrol")
	}
	if h := byEntry[9]; !h.Patrol {
		t.Fatal("a patrol group's spawn is not marked patrol")
	}
	if h := byEntry[11]; h.Patrol || h.At.X != 36 {
		t.Fatalf("the boss is not a home spawn at its position: %+v", h)
	}
	if h := byEntry[12]; h.Patrol || h.Spawn != 77 || h.At.X != 38 {
		t.Fatalf("a left-alone spawn is not a home spawn with its guid: %+v", h)
	}
	p := dungeon.Point{}
	steady := func(x float32, entry uint32) objects.Unit {
		u := hostileAt("mob", x, false, false)
		u.Entry = entry
		return u
	}
	// End to end: the patrol-group unit at its own position still blocks.
	if ok, reason := quietProven([]objects.Unit{steady(35, 9)}, nil, p, homes); ok {
		t.Fatalf("a patrol-group unit at its position: quiet, want blocked (%s)", reason)
	}
	// The stationary-group unit at its own position is exempt.
	if ok, reason := quietProven([]objects.Unit{steady(40, 7)}, nil, p, homes); !ok {
		t.Fatalf("a stationary-group unit at its position: not quiet (%s)", reason)
	}
	// The boss at his position is exempt too.
	if ok, reason := quietProven([]objects.Unit{steady(36, 11)}, nil, p, homes); !ok {
		t.Fatalf("the boss at his position: not quiet (%s)", reason)
	}
}

// A spawn guid pins the spawn: a unit carrying another spawn's guid is not at
// this one, even with the same entry and within 3 yd of its point.
func TestQuietHomeSpawnMatchesByGuid(t *testing.T) {
	p := dungeon.Point{}
	homes := QuietHomeSpawns{{Entry: 14532, Spawn: 1234, At: dungeon.Point{X: 35}}}
	withGUID := func(low uint32) objects.Unit {
		u := hostileAt("Razzashi Venombrood", 35, false, false)
		u.Entry = 14532
		u.GUID = packet.GUID(uint64(packet.HighUnit)<<48 | uint64(14532)<<24 | uint64(low))
		return u
	}
	if ok, reason := quietProven([]objects.Unit{withGUID(1234)}, nil, p, homes); !ok {
		t.Fatalf("the unit of spawn 1234 at its point: not quiet (%s)", reason)
	}
	if ok, reason := quietProven([]objects.Unit{withGUID(5678)}, nil, p, homes); ok {
		t.Fatalf("the unit of spawn 5678 at spawn 1234's point: quiet, want blocked (%s)", reason)
	}
}

func hostileAt(name string, x float32, moving, dead bool) objects.Unit {
	return objects.Unit{Type: "creature", Name: name, Hostile: true, X: x, Z: 0, Moving: moving, Dead: dead}
}

// fightingAt is hostileAt for a unit in combat: D-A's idle exemption (not in combat, no target, not moving) leaves it
// alone, so the 45/80 yd rules still hold for it.
func fightingAt(name string, x float32, moving bool) objects.Unit {
	u := hostileAt(name, x, moving, false)
	u.InCombat = true
	return u
}

func TestQuietAt(t *testing.T) {
	p := dungeon.Point{}
	// 40 yd and in combat: D-A's idle exemption does not touch it, so the 45 yd still rule blocks (an idle unit at
	// 40 yd is quiet now: TestQuietAtIgnoresIdleUnitBeyondReach).
	if ok, reason := quietAt([]objects.Unit{fightingAt("Onyxian Warder", 40, false)}, nil, p); ok || !strings.Contains(reason, "40") {
		t.Fatalf("40 yd: quiet %v, reason %q", ok, reason)
	}
	if ok, reason := quietAt([]objects.Unit{hostileAt("Onyxian Warder", 50, false, false)}, nil, p); !ok {
		t.Fatalf("50 yd standing: not quiet (%s)", reason)
	}
	if ok, reason := quietAt([]objects.Unit{hostileAt("Onyxian Warder", 70, true, false)}, nil, p); ok || !strings.Contains(reason, "70") {
		t.Fatalf("70 yd moving: quiet %v, reason %q", ok, reason)
	}
	if ok, reason := quietAt([]objects.Unit{hostileAt("Onyxian Warder", 10, false, true)}, nil, p); !ok {
		t.Fatalf("dead unit: not quiet (%s)", reason)
	}
	if ok, reason := quietAt([]objects.Unit{{Type: "player", Name: "Someone", Hostile: true, X: 10}}, nil, p); !ok {
		t.Fatalf("hostile player: not quiet (%s)", reason)
	}

	patrol := QuietPatrol{ID: "52050", Path: []dungeon.Point{{X: 30, Y: 0, Z: 0}, {X: 30, Y: 30, Z: 0}}}
	if ok, reason := quietAt(nil, []QuietPatrol{patrol}, p); ok || !strings.Contains(reason, "52050") {
		t.Fatalf("patrol near: quiet %v, reason %q", ok, reason)
	}
	patrol.Exempt = true
	if ok, reason := quietAt(nil, []QuietPatrol{patrol}, p); !ok {
		t.Fatalf("exempt patrol: not quiet (%s)", reason)
	}

	// The loop closes from the last point back to the first.
	patrol = QuietPatrol{ID: "52051", Path: []dungeon.Point{{X: 50, Y: -50, Z: 0}, {X: 50, Y: 50, Z: 0}, {X: -50, Y: 50, Z: 0}}}
	if ok, reason := quietAt(nil, []QuietPatrol{patrol}, p); ok {
		t.Fatalf("the closing leg of a loop was not seen: %s", reason)
	}
	patrol.Path = []dungeon.Point{{X: 60, Y: 0, Z: 0}, {X: 60, Y: 30, Z: 0}}
	if ok := mustQuiet(t, patrol); !ok {
		t.Fatal("a patrol 60 yd away was not quiet")
	}
}

// D-A (walk 25): the refusal was a mob standing at its own spawn 44.6 yd from the revive point, not fighting: an idle
// unit beyond the landing reach does not block the revive — once two reads quietIdleHold apart saw it idle at the same
// spot (review round 1, Opus M1: one read is no proof, the mob may start to move next).
func TestQuietAtIgnoresIdleUnitBeyondReach(t *testing.T) {
	p := dungeon.Point{}
	u := hostileAt("Razzashi Venombrood", 44.6, false, false)
	u.Entry = 14532
	if ok, reason := quietAt([]objects.Unit{u}, nil, p); ok {
		t.Fatalf("an idle unit 44.6 yd from the point is quiet on ONE read: %s", reason)
	}
	if ok, reason := quietHome([]objects.Unit{u}, nil, p); !ok {
		t.Fatalf("an idle unit 44.6 yd from the point still blocks after two reads: %s", reason)
	}
}

// TestQuietAtIdleNeedsTwoReads (review round 1, Opus M1): D-A's exemption is not an idle-ness test but a
// idle-at-home one — the same unit must be seen idle twice, quietIdleHold apart, within quietIdleMoveYd of the same
// position. A unit idle on read 1 and moving on read 2 was never idle at home, so read 1 does not exempt it.
func TestQuietAtIdleNeedsTwoReads(t *testing.T) {
	p := dungeon.Point{}
	u := hostileAt("Razzashi Venombrood", 44.6, false, false)
	homes := atHomeAll([]objects.Unit{u})
	idle := NewQuietIdleReads()
	if ok, _ := QuietAt([]objects.Unit{u}, nil, p, idle, homes, quietNow); ok {
		t.Fatal("idle on read 1: quiet, want blocked (no earlier read saw it idle)")
	}
	moving := hostileAt("Razzashi Venombrood", 44.6, true, false)
	if ok, _ := QuietAt([]objects.Unit{moving}, nil, p, idle, homes, quietNow.Add(quietIdleHold)); ok {
		t.Fatal("idle on read 1, moving on read 2: quiet, want blocked")
	}
	// Read 2 did not see it idle, so read 3 starts the pair over: a single idle read again.
	if ok, _ := QuietAt([]objects.Unit{u}, nil, p, idle, homes, quietNow.Add(2*quietIdleHold)); ok {
		t.Fatal("an idle read after a moving one: quiet, want blocked (the moving read forgot the pair)")
	}
	// Read 4, quietIdleHold after read 3 at the same spot: the pair is complete.
	if ok, reason := QuietAt([]objects.Unit{u}, nil, p, idle, homes, quietNow.Add(3*quietIdleHold)); !ok {
		t.Fatalf("two idle reads quietIdleHold apart at the same spot: not quiet (%s)", reason)
	}

	// The two reads must be quietIdleHold apart: a second read too soon is no pair.
	soon := NewQuietIdleReads()
	QuietAt([]objects.Unit{u}, nil, p, soon, homes, quietNow)
	if ok, _ := QuietAt([]objects.Unit{u}, nil, p, soon, homes, quietNow.Add(quietIdleHold-time.Second)); ok {
		t.Fatal("two idle reads 4 s apart: quiet, want blocked")
	}

	// And within quietIdleMoveYd of the SAME position: a unit idle at two spots is a wanderer, not at home.
	near := NewQuietIdleReads()
	QuietAt([]objects.Unit{u}, nil, p, near, homes, quietNow)
	shifted := hostileAt("Razzashi Venombrood", 44.9, false, false)
	if ok, reason := QuietAt([]objects.Unit{shifted}, nil, p, near, atHomeAll([]objects.Unit{shifted}), quietNow.Add(quietIdleHold)); !ok {
		t.Fatalf("idle 0.3 yd apart: not quiet (%s)", reason)
	}
	far := NewQuietIdleReads()
	QuietAt([]objects.Unit{u}, nil, p, far, homes, quietNow)
	moved := hostileAt("Razzashi Venombrood", 43.0, false, false)
	if ok, _ := QuietAt([]objects.Unit{moved}, nil, p, far, atHomeAll([]objects.Unit{moved}), quietNow.Add(quietIdleHold)); ok {
		t.Fatal("idle at two spots over 1 yd apart: quiet, want blocked")
	}
	// The same spot twice again, quietIdleHold later: the pair completes there.
	if ok, reason := QuietAt([]objects.Unit{moved}, nil, p, far, atHomeAll([]objects.Unit{moved}), quietNow.Add(2*quietIdleHold)); !ok {
		t.Fatalf("idle at the same spot on two reads: not quiet (%s)", reason)
	}
}

// Codex round 3: the two reads must be consecutive evidence. A unit missing from a read, a read too long ago, or a
// failed read in between starts the pair over.
func TestQuietAtIdlePairNeedsFreshConsecutiveReads(t *testing.T) {
	p := dungeon.Point{}
	u := hostileAt("Razzashi Venombrood", 44.6, false, false)
	homes := atHomeAll([]objects.Unit{u})

	gone := NewQuietIdleReads()
	QuietAt([]objects.Unit{u}, nil, p, gone, homes, quietNow)
	QuietAt(nil, nil, p, gone, homes, quietNow.Add(quietIdleHold)) // a complete read without the unit
	if ok, _ := QuietAt([]objects.Unit{u}, nil, p, gone, homes, quietNow.Add(2*quietIdleHold)); ok {
		t.Fatal("idle, absent, idle: quiet, want blocked (the absent read forgot the first sighting)")
	}

	dead := NewQuietIdleReads()
	QuietAt([]objects.Unit{u}, nil, p, dead, homes, quietNow)
	corpse := u
	corpse.Dead = true
	QuietAt([]objects.Unit{corpse}, nil, p, dead, homes, quietNow.Add(quietIdleHold))
	if ok, _ := QuietAt([]objects.Unit{u}, nil, p, dead, homes, quietNow.Add(2*quietIdleHold)); ok {
		t.Fatal("idle, dead, idle again: quiet, want blocked")
	}

	old := NewQuietIdleReads()
	QuietAt([]objects.Unit{u}, nil, p, old, homes, quietNow)
	if ok, _ := QuietAt([]objects.Unit{u}, nil, p, old, homes, quietNow.Add(quietIdleMaxGap+time.Second)); ok {
		t.Fatal("two idle reads over quietIdleMaxGap apart: quiet, want blocked")
	}

	failed := NewQuietIdleReads()
	QuietAt([]objects.Unit{u}, nil, p, failed, homes, quietNow)
	failed.Reset() // what quietOnce does on a failed units read
	if ok, _ := QuietAt([]objects.Unit{u}, nil, p, failed, homes, quietNow.Add(quietIdleHold)); ok {
		t.Fatal("idle, failed read, idle: quiet, want blocked")
	}
}

// An idle unit inside the landing reach still blocks, however many reads saw it idle: the revived raid lands right
// there. The cut itself (30 yd since review round 1, L1) counts as inside, and just beyond it needs the two reads.
func TestQuietAtIdleWithinReachBlocks(t *testing.T) {
	p := dungeon.Point{}
	for _, d := range []float32{10, 29.9, 30} {
		ok, reason := quietHome([]objects.Unit{hostileAt("Razzashi Venombrood", d, false, false)}, nil, p)
		if ok {
			t.Fatalf("an idle unit at %g yd: quiet, want blocked", d)
		}
		if !strings.Contains(reason, "yd from the revive point") {
			t.Fatalf("an idle unit at %g yd: reason %q", d, reason)
		}
	}
	if ok, reason := quietHome([]objects.Unit{hostileAt("Razzashi Venombrood", 30.1, false, false)}, nil, p); !ok {
		t.Fatalf("an idle unit at 30.1 yd is not quiet after two reads: %s", reason)
	}
}

// A unit in combat, or one holding a target, is not idle: D-A leaves it to the old 45/80 yd rules.
func TestQuietAtInCombatOrTargetingStillBlocks(t *testing.T) {
	p := dungeon.Point{}
	if ok, reason := quietAt([]objects.Unit{fightingAt("Onyxian Warder", 40, false)}, nil, p); ok {
		t.Fatalf("an in-combat unit 40 yd from the point: quiet (%s)", reason)
	}
	targeting := hostileAt("Onyxian Warder", 40, false, false)
	targeting.Target = 1000
	if ok, reason := quietAt([]objects.Unit{targeting}, nil, p); ok {
		t.Fatalf("a targeting unit 40 yd from the point: quiet (%s)", reason)
	}
	// Two blockers: the reason names the nearest one (QuietBlockers' order; the summary's own and the run.log line's
	// use of the whole list is checked in the walk command's own tests).
	close := fightingAt("Razzashi Skitterer", 30, false)
	if ok, reason := quietAt([]objects.Unit{targeting, close}, nil, p); ok || !strings.Contains(reason, "Razzashi Skitterer") {
		t.Fatalf("two blockers: quiet %v, reason %q, want the nearest named", ok, reason)
	}
}

// A moving unit is not idle either: it keeps the 80 yd moving rule (the Onyxian Warder patrol case).
func TestQuietAtIdleMovingStillBlocks(t *testing.T) {
	p := dungeon.Point{}
	if ok, reason := quietAt([]objects.Unit{hostileAt("Onyxian Warder", 60, true, false)}, nil, p); ok {
		t.Fatalf("a moving unit 60 yd from the point: quiet (%s)", reason)
	}
}

// critterAt is a level-1 critter of the place phase's max-HP band: the units read's stand-in for the frame/toolkit's
// critter flag (a Snake 2914 in the walk 27 evidence).
func critterAt(name string, x float32, moving bool) objects.Unit {
	u := hostileAt(name, x, moving, false)
	u.Level = 1
	u.MaxHealth = 8
	return u
}

// turtle-agent#379: walk 27 C restart 2 was refused because a moving Snake 2914 at 72 yd and an idle Mad Servant off
// its spawn were the last blockers, so all_idle was false. A critter never counts, at any distance and in any state;
// a real hostile off its spawn still blocks (the home-proof rule is unchanged).
func TestQuietAtIgnoresCritters(t *testing.T) {
	p := dungeon.Point{}
	// The evidence's 72 yd moving critter: inside the 80 yd moving rule, so it would block if it counted.
	if ok, reason := quietAt([]objects.Unit{critterAt("Snake", 72, true)}, nil, p); !ok {
		t.Fatalf("a moving critter 72 yd out: not quiet (%s)", reason)
	}
	// With an idle unit at its home also in the read: the unit is exempt after its two reads, and the critter never
	// joins the blocker list.
	atHome := hostileAt("Razzashi Venombrood", 44.6, false, false)
	atHome.Entry = 14532
	ok, reason := quietProven([]objects.Unit{critterAt("Snake", 72, true), atHome}, nil, p,
		atHomeAll([]objects.Unit{atHome}))
	if !ok {
		t.Fatalf("a moving critter 72 yd out with an idle unit at home: not quiet (%s)", reason)
	}
	// Inside the 45 yd still radius too: no two reads and no home are needed, because a critter never blocks.
	if ok, reason := quietAt([]objects.Unit{critterAt("Snake", 20, true)}, nil, p); !ok {
		t.Fatalf("a moving critter 20 yd out: not quiet (%s)", reason)
	}
	// A real hostile off its spawn is not a critter by its level, however still it is: the home-proof rule still
	// blocks it, as walk 27's idle Mad Servant did.
	servant := hostileAt("Mad Servant", 16.8, false, false)
	servant.Entry = 15111
	servant.Level = 61
	servant.MaxHealth = 4000
	homes := QuietHomeSpawns{{Entry: 15111, At: dungeon.Point{X: 29}}}
	if ok, reason := quietProven([]objects.Unit{servant}, nil, p, homes); ok {
		t.Fatalf("a level-61 hostile off its spawn: quiet (%s)", reason)
	}
}

// turtle-agent#379, round 2: the quiet fallback only trusts an absolute max health. A percent read carries MaxHealth
// 100 (the store passes health through as sent), which sits at the band top: a level-1 unit read at 100 max HP is
// ambiguous with "60 %", so it still blocks.
func TestQuietAtPercentLevel1StillBlocks(t *testing.T) {
	p := dungeon.Point{}
	// A level-1 unit whose max health is a percent read: inside no absolute band, so the moving rule still holds.
	percent := hostileAt("Snake", 72, true, false)
	percent.Level = 1
	percent.Health = 60
	percent.MaxHealth = 100
	if ok, reason := quietAt([]objects.Unit{percent}, nil, p); ok {
		t.Fatalf("a level-1 percent-health unit 72 yd out: quiet (%s)", reason)
	}
}

func mustQuiet(t *testing.T, p QuietPatrol) bool {
	t.Helper()
	ok, reason := quietAt(nil, []QuietPatrol{p}, dungeon.Point{})
	if !ok && reason == "" {
		t.Fatal("not quiet without a reason")
	}
	return ok
}

// TestZulGurubCubPatrolBlocksTheRevivePoint (turtle-agent#392): the placed Arlokk walk revives at leg 4's via point,
// which the Zulian Cub patrol of the Last pull group of the Thekal leg walks within 5.7 yd of. The plan's patrol
// block makes the quiet check refuse that point by path, the reason naming the patrol and its distance (the 392 run
// saw only the cub's paused position, 32.6 yd away, and waited 568 s on a point the patrol walks through).
func TestZulGurubCubPatrolBlocksTheRevivePoint(t *testing.T) {
	plan, err := dungeon.Load("../../dungeons/zul_gurub.json")
	if err != nil {
		t.Fatal(err)
	}
	patrols := PlanQuietPatrols(plan)
	if len(patrols) == 0 {
		t.Fatal("the plan yields no patrol: the cub block is missing")
	}
	rev := dungeon.Point{X: -11712, Y: -1889.8667, Z: 52.825405}
	ok, reason := QuietAt(nil, patrols, rev, NewQuietIdleReads(), nil, quietNow)
	if ok {
		t.Fatalf("the revive point is quiet: the cub patrol's path does not block")
	}
	if !strings.Contains(reason, "51344") || !strings.Contains(reason, "passes") {
		t.Fatalf("reason %q, want the cub patrol's path reason", reason)
	}
}
