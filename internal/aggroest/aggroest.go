// Package aggroest is the offline port of the TortoiseBots run leader's
// pull aggro-count estimator (ai/playerbot/PullAggroEstimatePolicy.h,
// EstimateAggroRing): before a pull it estimates the set of creatures that
// would join, as a count and a member list with a reason each.
//
// The rule, from the raid evidence (Arlokk terrace: cross-pack wakes at spawn
// pairs 11.0-19.5 yd apart; the walk waking G14 at 21.3 yd + spread; the drag
// collecting joiners along the way back):
//
//  1. the target's pack (GroupID: the caller's union of the creature_groups
//     AGGRO_TOGETHER group and the creature_linking edges);
//  2. proximity: every mob without NoAggro inside its aggro reach
//     (detection minus the party-minus-mob level difference, floored, times
//     the rate) of the party's walk (2 yd samples, + WalkSpreadYd), the
//     tank-only walk (+ TankSpreadYd), the stand (+ StandSpreadYd) and the
//     fight point (+ StandSpreadYd), with sight from the mob to the point;
//  3. ring hops: every member calls every mob of the same faction within
//     RingYd of its own position (sight caller -> candidate, |dz| <= FloorDz,
//     no NoAssist/NoAggro), each call adding the called mob's pack, up to
//     RingHops hops (hop 2+ callers are the ones the previous hop added);
//  4. drag sweep: along the drag polyline (4 yd samples) every mob of the
//     target's faction within RingYd of the sample (sight + floor band)
//     joins, then its pack.
//
// The constants mirror the run-leader ones (kAggroRingYd, kAggroRingHops,
// kAggroFloorDz, the 3/10/1.5 yd spreads); TestConstantsMirrorTheRunLeader
// pins them, and testdata/aggroest_cases.json lists the shared cases.
package aggroest

import (
	"math"

	"github.com/pjerra/turtle-agent/internal/dungeon"
)

// The run-leader constants this package mirrors.
const (
	// RingYd is the call-for-help ring (kAggroRingYd, 20 yd).
	RingYd = 20.0
	// RingHops is how far the ring carries (kAggroRingHops, 2).
	RingHops = 2
	// FloorDz is the ring's vertical band (kAggroFloorDz, 12 yd).
	FloorDz = 12.0
	// WalkSpreadYd widens the party's walk (3 yd).
	WalkSpreadYd = 3.0
	// StandSpreadPartyYd widens the stand and fight point of a party (3 yd).
	StandSpreadPartyYd = 3.0
	// StandSpreadRaidYd widens the stand and fight point of a raid
	// (ai::kSpreadSettledDistance, 10 yd).
	StandSpreadRaidYd = 10.0
	// TankSpreadYd widens the tank-only walk (1.5 yd).
	TankSpreadYd = 1.5
	// MaxPullBodies is the most bodies a plan may size a pull at (kPlanMaxPullBodies, 80).
	MaxPullBodies = 80
	// assistYdFloor is the server's CreatureFamilyAssistanceRadius: the pure
	// function takes max(server, ringYd), so a ring below 10 still reaches 10.
	assistYdFloor = 10.0
	// walkSampleYd samples the walk polylines; dragSampleYd the drag polyline.
	walkSampleYd = 2.0
	dragSampleYd = 4.0
	// WalkSamples is the most samples of a party walk an estimate reads
	// (kAggroWalkSamples): the spacing grows with the path so a long walk
	// still fits, and the walk's end is always kept.
	WalkSamples = 64
)

// MobPoint is a position on the map.
type MobPoint = dungeon.Point

// Mob is one creature the estimate may count.
type Mob struct {
	Guid uint32
	Pos  MobPoint
	// Level is the spawn's level_max; DetectionYd its detection_range.
	Level       int
	DetectionYd float32
	// Faction is the creature_template faction; GroupID the caller's pack id
	// (0: no pack): the union of the AGGRO_TOGETHER group and the aggro
	// creature_linking edges the target's pack rule joins.
	Faction uint32
	GroupID uint32
	// NoAggro (flags_extra NO_AGGRO) never wakes by proximity and never
	// answers a call; NoAssist (flags_extra NO_ASSIST) never answers one.
	// Patroller counts at its current position, like the run leader.
	NoAggro, NoAssist, Patroller bool
}

// Geometry is where the party walks, stands and fights (AggroGeometry).
type Geometry struct {
	// Walk is the party's walk polyline (lead bot to the stand), sampled
	// every walkSampleYd inside Estimate; WalkSpreadYd widens it.
	Walk          []MobPoint
	WalkSpreadYd  float32
	TankWalk      []MobPoint
	TankSpreadYd  float32
	Stand         MobPoint
	StandSpreadYd float32
	HasFight      bool
	Fight         MobPoint
	FightSpreadYd float32
	Drag          []MobPoint
}

// Params tunes one estimate.
type Params struct {
	// PartyLevel is the lowest party level (the widest aggro radius).
	PartyLevel int
	// Rate is the core's Rate.Creature.Aggro.
	Rate   float32
	RingYd float32
	// RingHops caps the call-for-help chain.
	RingHops int
	FloorDz  float32
	// Sees reports sight from a to b (mob to point, caller to candidate);
	// nil means always sees (high side, what the mmaps-free lint uses).
	Sees func(a, b MobPoint) bool
}

// Why says how a member joined the estimate (AggroWhy).
type Why int

const (
	WhyTarget Why = iota
	WhyPack
	WhyWalk
	WhyTankWalk
	WhyStand
	WhyFight
	WhyRing1
	WhyRing2
	WhyDrag
)

// String names the reason.
func (w Why) String() string {
	switch w {
	case WhyTarget:
		return "Target"
	case WhyPack:
		return "Pack"
	case WhyWalk:
		return "Walk"
	case WhyTankWalk:
		return "TankWalk"
	case WhyStand:
		return "Stand"
	case WhyFight:
		return "Fight"
	case WhyRing1:
		return "Ring1"
	case WhyRing2:
		return "Ring2"
	case WhyDrag:
		return "Drag"
	}
	return "?"
}

// Member is one counted creature, in insertion order (the target first).
type Member struct {
	Index int
	Guid  uint32
	Why   Why
}

// Result is the estimate: the bodies that would join and why each comes.
type Result struct {
	Bodies  int
	Members []Member
}

// PullCeilingBodies is the bodies ceiling: the plan row's max_bodies when the
// row sizes the pull, else the config joiner ceiling + 1 (bodies include the
// target), else 0 (no ceiling).
func PullCeilingBodies(planBodies, configJoiners uint32) uint32 {
	switch {
	case planBodies != 0:
		return planBodies
	case configJoiners != 0:
		return configJoiners + 1
	default:
		return 0
	}
}

// Estimate runs the ring estimator for mobs[target] (index, not guid).
func Estimate(mobs []Mob, target int, g Geometry, p Params) Result {
	if target < 0 || target >= len(mobs) {
		return Result{}
	}
	sees := p.Sees
	if sees == nil {
		sees = func(a, b MobPoint) bool { return true }
	}
	ring := p.RingYd
	if ring < assistYdFloor {
		ring = assistYdFloor
	}
	in := make([]bool, len(mobs))
	var members []Member
	add := func(idx int, why Why) {
		if in[idx] {
			return
		}
		in[idx] = true
		members = append(members, Member{Index: idx, Guid: mobs[idx].Guid, Why: why})
	}
	// closePack adds the whole pack of idx (rule 1 closure).
	closePack := func(idx int) {
		id := mobs[idx].GroupID
		if id == 0 {
			return
		}
		for i := range mobs {
			if mobs[i].GroupID == id {
				add(i, WhyPack)
			}
		}
	}

	add(target, WhyTarget)
	closePack(target)

	// Proximity: the walk, the tank-only walk, the stand, the fight point.
	walk := samplePolyline(g.Walk, walkSampleYd)
	tank := samplePolyline(g.TankWalk, walkSampleYd)
	for i := range mobs {
		if in[i] || mobs[i].NoAggro {
			continue
		}
		reach := aggroReach(mobs[i], p.PartyLevel, p.Rate)
		switch {
		case nearAny(mobs[i].Pos, walk, reach+g.WalkSpreadYd, sees):
			add(i, WhyWalk)
		case nearAny(mobs[i].Pos, tank, reach+g.TankSpreadYd, sees):
			add(i, WhyTankWalk)
		case dist3(mobs[i].Pos, g.Stand) <= reach+g.StandSpreadYd && sees(mobs[i].Pos, g.Stand):
			add(i, WhyStand)
		case g.HasFight && dist3(mobs[i].Pos, g.Fight) <= reach+g.FightSpreadYd && sees(mobs[i].Pos, g.Fight):
			add(i, WhyFight)
		}
	}
	for _, m := range members {
		closePack(m.Index)
	}

	// Ring hops from every member: hop 1 callers are the whole set so far,
	// later hops the ones the previous hop added, the pack members its
	// closure dragged in included (Arlokk: G12 calls G15, and G15's pack
	// mate is the only bridge on).
	callers := append([]int(nil), memberIndexes(members)...)
	for h := 1; h <= p.RingHops && len(callers) > 0; h++ {
		why := WhyRing1
		if h >= 2 {
			why = WhyRing2
		}
		before := len(members)
		for _, ci := range callers {
			for i := range mobs {
				if in[i] || mobs[i].NoAggro || mobs[i].NoAssist {
					continue
				}
				if mobs[i].Faction != mobs[ci].Faction {
					continue
				}
				if dist3(mobs[i].Pos, mobs[ci].Pos) > ring {
					continue
				}
				if abs32(mobs[i].Pos.Z-mobs[ci].Pos.Z) > p.FloorDz {
					continue
				}
				if !sees(mobs[ci].Pos, mobs[i].Pos) {
					continue
				}
				add(i, why)
				closePack(i)
			}
		}
		callers = memberIndexes(members[before:])
	}

	// Drag sweep: along the drag polyline every mob of the target's faction
	// within the ring of the moved centre joins (sight + floor band).
	if len(g.Drag) > 0 {
		for _, s := range samplePolyline(g.Drag, dragSampleYd) {
			for i := range mobs {
				if in[i] || mobs[i].NoAggro || mobs[i].NoAssist {
					continue
				}
				if mobs[i].Faction != mobs[target].Faction {
					continue
				}
				if dist3(mobs[i].Pos, s) > ring {
					continue
				}
				if abs32(mobs[i].Pos.Z-s.Z) > p.FloorDz {
					continue
				}
				if !sees(mobs[i].Pos, s) {
					continue
				}
				add(i, WhyDrag)
				closePack(i)
			}
		}
	}

	return Result{Bodies: len(members), Members: members}
}

func memberIndexes(ms []Member) []int {
	out := make([]int, 0, len(ms))
	for _, m := range ms {
		out = append(out, m.Index)
	}
	return out
}

// aggroReach is Creature::GetAttackDistance for one spawn: the detection range
// minus the party-minus-creature level difference (floored at -25 levels),
// never below min(detection, 5 yd), times the aggro rate. A spawn with no
// detection range never wakes (worldspawn.AggroRadius: 0 for it, and the NO_AGGRO
// and no-detection guards there); the pull_size lint keeps its own explicit
// mapping (export 0 means the draft predates the column, read as 20 yd).
func aggroReach(m Mob, partyLevel int, rate float32) float32 {
	if m.DetectionYd < 1 {
		return 0
	}
	det := m.DetectionYd
	diff := partyLevel - m.Level
	if diff < -25 {
		diff = -25
	}
	reach := det - float32(diff)
	floor := det
	if floor > 5 {
		floor = 5
	}
	if reach < floor {
		reach = floor
	}
	return reach * rate
}

// nearAny says the mob is within reach of a sample it sees.
func nearAny(pos MobPoint, samples []MobPoint, reach float32, sees func(a, b MobPoint) bool) bool {
	for _, s := range samples {
		if dist3(pos, s) <= reach && sees(pos, s) {
			return true
		}
	}
	return false
}

// sampleWalk resamples a polyline about every step yards: the spacing grows
// with the path so the whole walk fits maxSamples, and the path's end is
// always kept (a 150 yd walk at 2 yd would otherwise stop at 126 yd and read
// past its end). A single point stays one sample. Mirrors the run leader's
// SampleAggroWalk (kAggroWalkSamples).
func sampleWalk(path []MobPoint, step float32, maxSamples int) []MobPoint {
	if len(path) == 0 || maxSamples == 0 {
		return nil
	}
	out := []MobPoint{path[0]}
	var total float32
	for i := 1; i < len(path); i++ {
		total += dist3(path[i-1], path[i])
	}
	if total < 0.0001 || maxSamples < 2 {
		return out
	}
	spacing := step
	if grow := total / float32(maxSamples-1); grow > spacing {
		spacing = grow
	}
	var travelled float32
	next := spacing
	for i := 1; i < len(path); i++ {
		from, to := path[i-1], path[i]
		seg := dist3(from, to)
		if seg < 0.0001 {
			continue
		}
		end := travelled + seg
		for next < end-0.0001 && len(out)+1 < maxSamples {
			t := (next - travelled) / seg
			out = append(out, MobPoint{
				X: from.X + t*(to.X-from.X),
				Y: from.Y + t*(to.Y-from.Y),
				Z: from.Z + t*(to.Z-from.Z),
			})
			next += spacing
		}
		travelled = end
	}
	return append(out, path[len(path)-1])
}

// samplePolyline resamples a polyline for one estimate: at most WalkSamples
// points (the run leader's budget), endpoints kept.
func samplePolyline(pts []MobPoint, step float32) []MobPoint {
	return sampleWalk(pts, step, WalkSamples)
}
func dist3(a, b MobPoint) float32 {
	dx, dy, dz := a.X-b.X, a.Y-b.Y, a.Z-b.Z
	return float32(math.Sqrt(float64(dx*dx + dy*dy + dz*dz)))
}

func abs32(v float32) float32 {
	if v < 0 {
		return -v
	}
	return v
}
