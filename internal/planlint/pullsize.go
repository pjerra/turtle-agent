// The pull_size lint, the offline twin of the run leader's aggro-count estimator (internal/aggroest, a port of TortoiseBots'
// PullAggroEstimatePolicy.h EstimateAggroRing): the bodies a pull wakes, and which of them the pull does not own.
//
// It was tools/dungeongen's pull_size.go; it lives here so that plan-check's exported rows and dungeongen judge a pull the
// same way (the scout's joiners lint is this estimate). The geometry is the caller's: dungeongen hands the straight 2 yd
// segment between rows, the scout a straight walk or the offline path. Sight is the caller's Sees (nil: always sees, the high
// side: without mmaps there is no collision geometry to raycast against).
package planlint

import (
	"fmt"
	"math"
	"sort"
	"strings"

	"github.com/pjerra/turtle-agent/internal/aggroest"
	"github.com/pjerra/turtle-agent/internal/dungeon"
	"github.com/pjerra/turtle-agent/internal/planexport"
	"github.com/pjerra/turtle-agent/internal/worldspawn"
)

// Default pack radius and detection range when the export carries none.
const (
	defaultPackRadius   = 25
	defaultDetectionYd  = 20
	defaultRaidCeiling  = 8
	defaultPartyCeiling = 6
)

// PullRow is one positioned row of the route the estimate walks. Point is the row's own point (the ownership point); Party is
// where the party stands (a stand pull's stand, the move row before it); Walk is the segment walked into this row; Entries
// and Radius say which spawns the row pulls; Owns marks a row that pulls (a pull, boss or hold row with entries). Team is the
// plan's gate for the row ("alliance" or "horde").
type PullRow struct {
	Leg, Seq int
	// Kind is the export row's kind: only a pull row is estimated (a boss or hold row pulls nothing).
	Kind    string
	Point   dungeon.Point
	Party   dungeon.Point
	Walk    []dungeon.Point
	Entries []uint32
	Radius  float32
	Item    uint32
	Team    string
	Owns    bool
}

// PullOptions are the estimate's choices.
type PullOptions struct {
	// PartyLevel is the lowest party level (the widest aggro radius).
	PartyLevel int
	// Raid widens the stand spread (10 yd, else 3).
	Raid bool
	// Ceiling is the plan's pull_ceiling (0: the default 8 for a raid, 6 for a party).
	Ceiling int
	// Team is the run's team: a row gated to another team is not pulled and skipped.
	Team string
	// KillFirst ranks the target choice (the plan's kill_first).
	KillFirst []uint32
	// Fight makes the estimate include the row's own point as the fight point (the scout's joiner geometry). Without it the
	// estimate only walks and stands (dungeongen's established counts).
	Fight bool
	// Joiners fills Joiners, JoinerGuids and JoinerPacks: the bodies whose pack is not the target's.
	Joiners bool
	// All reports every pull row's estimate, also under its ceiling (the joiner lint's boundaries).
	All bool
	// Rate is the core's Rate.Creature.Aggro (1 offline).
	Rate float32
	// Sees is the sight test (nil: always sees).
	Sees func(a, b dungeon.Point) bool
}

// PullFinding is one pull row over its ceiling, or a pull row with no target, or the row's estimate when ReportAll is set.
type PullFinding struct {
	// Row is the row's index in the PullRows given.
	Row int
	// Leg is the export's leg number; Pull is the pull row's number within it (pull rows in route order).
	Leg  int
	Pull int
	// NoTarget says the row owns no mob to estimate from.
	NoTarget bool
	Bodies   int
	Ceiling  int
	// Target is the target mob's guid; Members is the estimate (the target first).
	Target  uint32
	Members []aggroest.Member
	Guids   []uint32
	// Joiners is the count of bodies outside the target's pack, with their guids and packs (Joiners option).
	Joiners     int
	JoinerGuids []uint32
	JoinerPacks []JoinerPack
	Summary     string
}

// PullCeilingOf is the bodies ceiling of one pull row: the row's item (the plan's max_bodies) when the row sizes the pull,
// else the plan's pull_ceiling, else the lint default (8 raids, 6 parties).
func PullCeilingOf(planCeiling int, item uint32, raid bool) int {
	if item != 0 {
		return int(item)
	}
	if planCeiling != 0 {
		return planCeiling
	}
	if raid {
		return defaultRaidCeiling
	}
	return defaultPartyCeiling
}

// PackIDs maps each spawn guid to its pack id (the smallest member guid), from the world's AGGRO_TOGETHER groups and aggro
// creature_linking edges (worldspawn.Packs); nil world: every spawn its own pack.
func PackIDs(w *worldspawn.World) map[uint64]uint64 {
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

// PullMobs converts the kept spawn export to estimator mobs: hostile, non-critter spawns with their detection range (an export
// 0 predates the column, read as 20 yd, while the estimator itself never wakes a spawn with no detection range), NO_AGGRO and
// NO_ASSIST from flags_extra, patrollers counted where they stand. groups maps spawn guids to pack ids (PackIDs); nil: every
// spawn its own pack. The returned map is the entry of each kept mob guid (the target rank).
func PullMobs(spawns []worldspawn.Spawn, pats map[uint64]bool, groups map[uint64]uint64, keep func(worldspawn.Spawn) bool) ([]aggroest.Mob, map[uint32]uint32) {
	var out []aggroest.Mob
	entries := map[uint32]uint32{}
	for _, c := range spawns {
		if keep != nil && !keep(c) {
			continue
		}
		det := c.DetectionYd
		if det <= 0 {
			det = defaultDetectionYd
		}
		m := aggroest.Mob{
			Guid: uint32(c.GUID), Pos: c.Pos, Level: c.LevelMax, DetectionYd: det,
			Faction: uint32(c.Faction), GroupID: uint32(groups[c.GUID] & 0xffffffff),
			NoAggro: c.FlagsExtra&worldspawn.NoAggro != 0, NoAssist: c.FlagsExtra&worldspawn.NoAssist != 0,
			Patroller: pats[c.GUID],
		}
		out = append(out, m)
		entries[m.Guid] = c.Entry
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Guid < out[j].Guid })
	return out, entries
}

// PullOwners maps each spawn guid to the index of the PullRow that pulls it: the first pull row (in route order) whose
// entries hold its entry and whose radius reaches one of its formation's spawns from the row's point. Only rows with Owns are
// owners. A formation's spawns share the row (group_leader).
func PullOwners(rows []PullRow, spawns []worldspawn.Spawn) map[uint64]int {
	byLeader := map[uint64][]worldspawn.Spawn{}
	for _, c := range spawns {
		if c.GroupLeader != 0 {
			byLeader[c.GroupLeader] = append(byLeader[c.GroupLeader], c)
		}
	}
	out := map[uint64]int{}
	for _, c := range spawns {
		if i, ok := ownerOf(rows, byLeader, c); ok {
			out[c.GUID] = i
		}
	}
	return out
}

func ownerOf(rows []PullRow, byLeader map[uint64][]worldspawn.Spawn, c worldspawn.Spawn) (int, bool) {
	group := byLeader[c.GroupLeader]
	if len(group) == 0 {
		group = []worldspawn.Spawn{c}
	}
	for i, o := range rows {
		if !o.Owns || !hasEntry(o.Entries, c.Entry) {
			continue
		}
		r := float64(o.Radius)
		if r == 0 {
			r = defaultPackRadius
		}
		for _, m := range group {
			dx, dy, dz := float64(o.Point.X-m.Pos.X), float64(o.Point.Y-m.Pos.Y), float64(o.Point.Z-m.Pos.Z)
			if sqrt3(dx, dy, dz) <= r {
				return i, true
			}
		}
	}
	return -1, false
}

// PullSizes estimates every pull row (rows whose Owns is set, not gated to another team) and returns one finding per row that
// is over its ceiling or owns no target, in route order. The ceiling is the row's item, the plan's pull_ceiling, else 8 for a
// raid and 6 for a party.
func PullSizes(rows []PullRow, spawns []worldspawn.Spawn, w *worldspawn.World, keep func(worldspawn.Spawn) bool, opt PullOptions) []PullFinding {
	pats := map[uint64]bool{}
	if w != nil {
		for _, pt := range w.Patrols {
			for _, g := range pt.SpawnGUIDs {
				pats[g] = true
			}
		}
	}
	mobs, entries := PullMobs(spawns, pats, PackIDs(w), keep)
	rate := opt.Rate
	if rate == 0 {
		rate = 1
	}
	spread := float32(aggroest.WalkSpreadYd)
	stand := float32(aggroest.StandSpreadPartyYd)
	if opt.Raid {
		stand = float32(aggroest.StandSpreadRaidYd)
	}
	owned := PullOwners(rows, spawns)
	var out []PullFinding
	pullIdx := map[int]int{} // export leg -> pulls so far
	for i, rp := range rows {
		if rp.Kind != planexport.KindPull || !rp.Owns || gatedOut(rp.Team, opt.Team) {
			continue
		}
		pullIdx[rp.Leg]++
		n := pullIdx[rp.Leg]
		ceiling := PullCeilingOf(opt.Ceiling, rp.Item, opt.Raid)
		target := PullTarget(opt.KillFirst, mobs, entries, i, rp, owned)
		if target < 0 {
			out = append(out, PullFinding{Row: i, Leg: rp.Leg, Pull: n, NoTarget: true, Ceiling: ceiling,
				Summary: fmt.Sprintf("leg %d pull %d: no target", rp.Leg, n)})
			continue
		}
		g := aggroest.Geometry{
			Walk: rp.Walk, WalkSpreadYd: spread,
			Stand: rp.Party, StandSpreadYd: stand,
		}
		if opt.Fight {
			g.HasFight, g.Fight, g.FightSpreadYd = true, rp.Point, stand
		}
		res := aggroest.Estimate(mobs, target, g, aggroest.Params{
			PartyLevel: opt.PartyLevel, Rate: rate,
			RingYd: aggroest.RingYd, RingHops: aggroest.RingHops, FloorDz: aggroest.FloorDz,
			Sees: opt.Sees,
		})
		if res.Bodies <= ceiling && !opt.All {
			continue
		}
		guids := make([]uint32, 0, len(res.Members))
		for _, m := range res.Members {
			guids = append(guids, m.Guid)
		}
		f := PullFinding{
			Row: i, Leg: rp.Leg, Pull: n, Bodies: res.Bodies, Ceiling: ceiling, Target: mobs[target].Guid,
			Members: res.Members, Guids: guids,
			Summary: fmt.Sprintf("leg %d pull %d: %d bodies (ceiling %d): %s", rp.Leg, n, res.Bodies, ceiling, guidList(guids)),
		}
		if opt.Joiners {
			f.Joiners, f.JoinerGuids, f.JoinerPacks = joinerBodies(mobs, target, res.Members)
		}
		out = append(out, f)
	}
	return out
}

// pullTarget is the row's own creature for the estimate: among the mobs the row owns, kill_first rank first (the run leader
// fires from its stand, and the plan's kill_first pins the target while a formation walks), then the nearest to the party
// point. -1 when the row owns nothing: the caller reports "no target" instead of estimating an unrelated spawn.
func PullTarget(killFirst []uint32, mobs []aggroest.Mob, entries map[uint32]uint32, row int, rp PullRow, owned map[uint64]int) int {
	rank := map[uint32]int{}
	for i, e := range killFirst {
		if _, ok := rank[e]; !ok {
			rank[e] = i
		}
	}
	best, bestListed, bestRank := -1, false, 0
	var bestDist float64
	for i, m := range mobs {
		idx, ok := owned[uint64(m.Guid)]
		if !ok || idx != row {
			continue
		}
		dx, dy, dz := float64(m.Pos.X-rp.Party.X), float64(m.Pos.Y-rp.Party.Y), float64(m.Pos.Z-rp.Party.Z)
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

// joinerBodies splits an estimate's members into the ones outside the target's pack: the count, their guids and their packs.
func joinerBodies(mobs []aggroest.Mob, target int, members []aggroest.Member) (int, []uint32, []JoinerPack) {
	pack := mobs[target].GroupID
	var guids []uint32
	byPack := map[uint64]*JoinerPack{}
	var order []uint64
	for _, m := range members {
		if m.Guid == mobs[target].Guid || mobs[m.Index].GroupID == pack {
			continue
		}
		guids = append(guids, m.Guid)
		id := uint64(mobs[m.Index].GroupID)
		p, ok := byPack[id]
		if !ok {
			p = &JoinerPack{ID: id}
			byPack[id] = p
			order = append(order, id)
		}
		p.Guids = append(p.Guids, m.Guid)
	}
	packs := make([]JoinerPack, 0, len(order))
	for _, id := range order {
		p := byPack[id]
		p.Bodies = len(p.Guids)
		sort.Slice(p.Guids, func(i, j int) bool { return p.Guids[i] < p.Guids[j] })
		packs = append(packs, *p)
	}
	sort.Slice(guids, func(i, j int) bool { return guids[i] < guids[j] })
	return len(guids), guids, packs
}

// gatedOut says a row gated to another team is not pulled by this run.
func gatedOut(gate, team string) bool { return gate != "" && team != "" && gate != team }

func guidList(guids []uint32) string {
	var parts []string
	for _, g := range guids {
		parts = append(parts, fmt.Sprintf("%d", g))
	}
	return strings.Join(parts, ", ")
}

func sqrt3(dx, dy, dz float64) float64 {
	return math.Sqrt(dx*dx + dy*dy + dz*dz)
}
