// The pull_size lint: the offline twin of the run leader's aggro-count
// estimator (internal/aggroest, a port of TortoiseBots'
// PullAggroEstimatePolicy.h EstimateAggroRing). For every pull row of the
// export it estimates the bodies that would join (a count and guid list) and
// warns when the estimate is over the row's ceiling:
//
//	leg L pull P: N bodies (ceiling M): guids ...
//
// Target: the row's own Creature, the run leader's pull target: among the
// spawns the row owns (its entry in the row's entries and within its radius
// of the row's ownership point, routeOwnerOf's rule), kill_first rank first,
// then the nearest to the party point. A row that owns nothing is a finding
// of its own ("pull N: no target"), never an unrelated spawn's estimate.
// Geometry: the previous row's party point to this row's party point (a
// straight 2 yd sampled segment: the navmesh polyline needs TURTLE_AGENT_MMAPS
// and the offline path there is what the server walks, so the mmaps-free
// straight line is the documented approximation, high side), stand = party
// point, fight = party point (no camp chain in this work: feature A only), no
// drag. Sight is "always sees" (high side, documented): without mmaps there
// is no collision geometry to raycast against.
//
// The ceiling is the row's item (the plan's max_bodies), else the plan's
// pull_ceiling, else the lint default (8 for raids, 6 for parties).
package planlint

import (
	"fmt"
	"sort"
	"strings"

	"github.com/pjerra/turtle-agent/internal/aggroest"
	"github.com/pjerra/turtle-agent/internal/dungeon"
	"github.com/pjerra/turtle-agent/internal/planexport"
	"github.com/pjerra/turtle-agent/internal/worldspawn"
)

// Default ceilings when neither the row nor the plan sizes the pull.
const (
	defaultRaidPullCeiling  = 8
	defaultPartyPullCeiling = 6
)

// PullFinding is one pull row over its ceiling, or a pull row with no target.
type PullFinding struct {
	Leg int // the export's leg number
	// Pull is the pull row's number within its export leg: every pull row of
	// the leg is numbered in route order, so a row's number never moves when
	// a ceiling or a neighbour changes (rows under their ceiling and rows
	// with no target consume a number).
	Pull int
	// NoTarget says the row owns no mob to estimate from (its finding warns
	// "no target" with no bodies); otherwise Bodies is over Ceiling.
	NoTarget bool
	Bodies   int
	Ceiling  int
	Guids    []uint32
	Summary  string
}

// PullCeilingOf is the bodies ceiling of one pull row: the row's item (the
// plan's max_bodies) when the row sizes the pull, else the plan's
// pull_ceiling, else the lint default (8 raids, 6 parties).
func PullCeilingOf(p *dungeon.Plan, item uint32) int {
	if item != 0 {
		return int(item)
	}
	if p.PullCeiling != 0 {
		return p.PullCeiling
	}
	if p.IsRaid() {
		return defaultRaidPullCeiling
	}
	return defaultPartyPullCeiling
}

// PullFilter says which spawns and rows the lint counts: the check flags
// (-ignore-factions, -friendly-factions, -team, -awakened, -scripted). 35 is
// always friendly (as in Check); a row gated to the other team is not pulled
// by this run. Awakened and Scripted are the check's entry sets (checkFile):
// a spawn of those entries counts as a mob whatever its npc_flags or faction.
// Empty: every mob counts, every row is pulled.
type PullFilter struct {
	Ignore   map[int]bool
	Friendly map[int]bool
	Awakened map[uint32]bool
	Scripted map[uint32]bool
	Team     string
}

// PullSizes estimates every pull row of the plan's export rows and returns
// the rows over their ceiling, in route order. cs are the plan's spawns
// (ReadCreatures); w is the map's world (worldspawn.Load: groups, links,
// patrols) and may be nil (then every spawn is its own pack). A row gated to
// a team other than f.Team is not pulled by this run and skipped (it consumes
// no pull number); a pull row with no target of its own is a finding of its
// own ("no target"), never an unrelated spawn's estimate.
func PullSizes(p *dungeon.Plan, cs []Creature, w *worldspawn.World, f PullFilter) ([]PullFinding, error) {
	pts, err := routePoints(p)
	if err != nil {
		return nil, err
	}
	// No eligible mobs is not an early exit: every pull row then finds no
	// target below and gets its "no target" finding.
	mobs, entries := pullMobs(p, cs, packIDs(w), f)
	spread := float32(aggroest.WalkSpreadYd)
	stand := float32(aggroest.StandSpreadPartyYd)
	if p.IsRaid() {
		stand = float32(aggroest.StandSpreadRaidYd)
	}
	var out []PullFinding
	pullIdx := map[int]int{}     // export leg -> pulls so far
	owned := pullOwners(pts, cs) // guid -> row index, routeOwnerOf's rule
	for i, rp := range pts {
		if rp.kind != planexport.KindPull {
			continue
		}
		if gatedOut(rp.team, f.Team) {
			continue
		}
		pullIdx[rp.leg]++
		n := pullIdx[rp.leg]
		target := pullTarget(p, mobs, entries, rp, owned)
		if target < 0 {
			out = append(out, PullFinding{
				Leg: rp.leg, Pull: n, NoTarget: true, Ceiling: PullCeilingOf(p, rp.item),
				Summary: fmt.Sprintf("leg %d pull %d: no target", rp.leg, n),
			})
			continue
		}
		var walk []dungeon.Point
		if i > 0 {
			walk = []dungeon.Point{{X: pts[i-1].px, Y: pts[i-1].py, Z: pts[i-1].pz}, {X: rp.px, Y: rp.py, Z: rp.pz}}
		}
		g := aggroest.Geometry{
			Walk: walk, WalkSpreadYd: spread,
			Stand:         dungeon.Point{X: rp.px, Y: rp.py, Z: rp.pz},
			StandSpreadYd: stand,
		}
		res := aggroest.Estimate(mobs, target, g, aggroest.Params{
			PartyLevel: routePartyLevel(p), Rate: routeAggroRate,
			RingYd: aggroest.RingYd, RingHops: aggroest.RingHops, FloorDz: aggroest.FloorDz,
		})
		ceiling := PullCeilingOf(p, rp.item)
		if res.Bodies <= ceiling {
			continue
		}
		guids := make([]uint32, 0, len(res.Members))
		for _, m := range res.Members {
			guids = append(guids, m.Guid)
		}
		out = append(out, PullFinding{
			Leg: rp.leg, Pull: n, Bodies: res.Bodies, Ceiling: ceiling, Guids: guids,
			Summary: fmt.Sprintf("leg %d pull %d: %d bodies (ceiling %d): %s", rp.leg, n, res.Bodies, ceiling, guidList(guids)),
		})
	}
	return out, nil
}

// packIDs maps each spawn guid to its pack id (the smallest member guid),
// from the world's AGGRO_TOGETHER groups and aggro creature_linking edges
// (worldspawn.Packs); nil world: every spawn its own pack.
func packIDs(w *worldspawn.World) map[uint64]uint64 {
	if w == nil {
		return nil
	}
	out := map[uint64]uint64{}
	for _, pk := range w.Packs(worldspawn.PackOptions{}) {
		for _, m := range pk.Members {
			out[m] = pk.ID
		}
	}
	return out
}

// pullMobs converts the spawn export to estimator mobs: hostile, non-critter
// spawns (pullHostile: the route lint's filter, with the check's ignore and
// friendly factions, and the awakened and scripted entries always in) with their detection range (the lint's own explicit
// mapping: an export 0 predates the column, read as 20 yd, while the
// estimator itself never wakes a spawn with no detection range),
// NO_AGGRO/NO_ASSIST from flags_extra, patrollers at their current position.
// groups maps spawn guids to pack ids (packIDs); nil: every spawn its own
// pack. entries maps each kept mob guid to its Creature entry, for the
// kill_first target rank.
func pullMobs(p *dungeon.Plan, cs []Creature, groups map[uint64]uint64, f PullFilter) ([]aggroest.Mob, map[uint32]uint32) {
	var out []aggroest.Mob
	entries := map[uint32]uint32{}
	_ = p
	for _, c := range cs {
		if !pullHostile(c, f) {
			continue
		}
		det := float32(c.DetectionRange)
		if det <= 0 {
			det = 20
		}
		m := aggroest.Mob{
			Guid: uint32(c.GUID), Pos: c.Pos, Level: c.LevelMax, DetectionYd: det,
			Faction: uint32(c.Faction), GroupID: uint32(groups[c.GUID] & 0xffffffff),
			NoAggro: c.FlagsExtra&worldspawn.NoAggro != 0, NoAssist: c.FlagsExtra&worldspawn.NoAssist != 0,
			Patroller: c.Patrol,
		}
		out = append(out, m)
		entries[m.Guid] = c.Entry
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Guid < out[j].Guid })
	return out, entries
}

// pullHostile is hostileMob for the pull lint, with the check's awakened and
// scripted entries always hostile: the encounter's script turns them hostile
// when the pull starts, so their npc_flags (a scripted gossip NPC) and their
// faction (35, the stoned Stone Keepers, which the default -ignore-factions
// also drops) are no reason to leave them out of the count. The critter rule
// still applies.
func pullHostile(c Creature, f PullFilter) bool {
	if f.Awakened[c.Entry] || f.Scripted[c.Entry] {
		return c.LevelMax > 1
	}
	return hostileMob(c, f.Ignore, f.Friendly)
}

// pullOwners maps each spawn guid to the export row that pulls it, by
// routeOwnerOf's rule (the row's entry and radius at the row's ownership
// point, formations shared by group_leader). Only pull/boss/hold rows with
// entries own spawns (routePoints sets ownerIdx for those).
func pullOwners(pts []routePoint, cs []Creature) map[uint64]int {
	var owners []routePoint
	for _, q := range pts {
		if q.ownerIdx >= 0 {
			owners = append(owners, q)
		}
	}
	byLeader := map[uint64][]Creature{}
	for _, c := range cs {
		if c.GroupLeader != 0 {
			byLeader[c.GroupLeader] = append(byLeader[c.GroupLeader], c)
		}
	}
	out := map[uint64]int{}
	for _, c := range cs {
		if o, ok := routeOwnerOf(owners, byLeader, c); ok {
			out[c.GUID] = o.idx
		}
	}
	return out
}

// pullTarget is the row's own Creature for the estimate: among the mobs the
// row owns, kill_first rank first (the run leader fires from its stand, and
// the plan's kill_first bat rider pins the target while the formation walks),
// then the nearest to the party point. -1 when the row owns nothing: the
// caller reports "no target" instead of estimating an unrelated spawn.
func pullTarget(p *dungeon.Plan, mobs []aggroest.Mob, entries map[uint32]uint32, rp routePoint, owned map[uint64]int) int {
	rank := map[uint32]int{}
	for i, e := range p.KillFirst {
		if _, ok := rank[e]; !ok {
			rank[e] = i
		}
	}
	best, bestListed, bestRank := -1, false, 0
	var bestDist float64
	for i, m := range mobs {
		idx, ok := owned[uint64(m.Guid)]
		if !ok || idx != rp.idx {
			continue
		}
		dx, dy, dz := float64(m.Pos.X-rp.px), float64(m.Pos.Y-rp.py), float64(m.Pos.Z-rp.pz)
		d := dx*dx + dy*dy + dz*dz
		r, listed := rank[entries[m.Guid]]
		switch {
		case best < 0:
		case listed != bestListed:
			if bestListed {
				continue
			}
		case listed && r != bestRank:
			if r > bestRank {
				continue
			}
		default:
			if d >= bestDist {
				continue
			}
		}
		best, bestListed, bestRank, bestDist = i, listed, r, d
	}
	return best
}

func guidList(guids []uint32) string {
	var parts []string
	for _, g := range guids {
		parts = append(parts, fmt.Sprintf("%d", g))
	}
	return strings.Join(parts, ", ")
}
