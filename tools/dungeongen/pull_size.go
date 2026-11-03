// The pull_size lint: the offline twin of the run leader's aggro-count estimator (internal/aggroest). The estimate itself and
// its rules live in internal/planlint (PullSizes), so plan-check's exported rows and this lint judge a pull the same way:
// the joiner bodies of a pull row equal what the scout's joiners lint counts for it.
//
//	leg L pull P: N bodies (ceiling M): guids ...
//
// Target: the run leader's pull target: among the spawns the row owns (its entry in the row's entries and within its radius of
// the row's point, formations shared by group_leader), kill_first rank first, then the nearest to the party point. A row that
// owns nothing is a finding of its own ("pull N: no target").
// Geometry: the previous row's party point to this row's party point (a straight 2 yd sampled segment: the navmesh polyline
// needs TURTLE_AGENT_MMAPS and the offline path there is what the server walks, so the mmaps-free straight line is the
// documented approximation, high side), stand = party point, fight = the row point, no drag. Sight is "always sees" (high
// side, documented): without mmaps there is no collision geometry to raycast against.
//
// The ceiling is the row's item (the plan's max_bodies), else the plan's pull_ceiling, else the lint default (8 for raids, 6
// for parties).
package main

import (
	"github.com/pjerra/turtle-agent/internal/aggroest"
	"github.com/pjerra/turtle-agent/internal/dungeon"
	"github.com/pjerra/turtle-agent/internal/planlint"
	"github.com/pjerra/turtle-agent/internal/worldspawn"
)

// PullFinding is one pull row over its ceiling, or a pull row with no target.
type PullFinding = planlint.PullFinding

// PullFilter says which spawns and rows the lint counts: the check flags (-ignore-factions, -friendly-factions, -team,
// -awakened, -scripted). 35 is always friendly (as in Check); a row gated to the other team is not pulled by this run.
// Awakened and Scripted are the check's entry sets (checkFile): a spawn of those entries counts as a mob whatever its
// npc_flags or faction. Empty: every mob counts, every row is pulled.
type PullFilter struct {
	Ignore   map[int]bool
	Friendly map[int]bool
	Awakened map[uint32]bool
	Scripted map[uint32]bool
	Team     string
}

// PullSizes estimates every pull row of the plan's export rows and returns the rows over their ceiling, in route order.
// cs are the plan's spawns (readCreatures); w is the map's world (worldspawn.Load: groups, links, patrols) and may be nil
// (then every spawn is its own pack).
func PullSizes(p *dungeon.Plan, cs []creature, w *worldspawn.World, f PullFilter) ([]PullFinding, error) {
	pts, err := routePoints(p)
	if err != nil {
		return nil, err
	}
	spawns := dungeonSpawns(cs)
	return planlint.PullSizes(pullRows(pts), spawns, w, pullKeep(f), planlint.PullOptions{
		PartyLevel: routePartyLevel(p), Raid: p.IsRaid(), Ceiling: p.PullCeiling, Team: f.Team,
		KillFirst: p.KillFirst,
	}), nil
}

// pullRows converts the route's points to the estimator's rows.
func pullRows(pts []routePoint) []planlint.PullRow {
	out := make([]planlint.PullRow, len(pts))
	for i, rp := range pts {
		row := planlint.PullRow{
			Leg: rp.leg, Seq: rp.seq, Kind: rp.kind,
			Point:   dungeon.Point{X: rp.x, Y: rp.y, Z: rp.z},
			Party:   dungeon.Point{X: rp.px, Y: rp.py, Z: rp.pz},
			Entries: rp.entries, Radius: rp.radius, Item: rp.item, Team: rp.team,
			Owns: rp.ownerIdx >= 0,
		}
		if i > 0 {
			row.Walk = []dungeon.Point{{X: pts[i-1].px, Y: pts[i-1].py, Z: pts[i-1].pz}, row.Party}
		}
		out[i] = row
	}
	return out
}

// dungeonspawns converts the plan's spawn export to the world's spawn type (the estimator reads it).
func dungeonSpawns(cs []creature) []worldspawn.Spawn {
	out := make([]worldspawn.Spawn, 0, len(cs))
	for _, c := range cs {
		out = append(out, worldspawn.Spawn{
			GUID: c.GUID, Entry: c.Entry, Name: c.Name, LevelMax: c.LevelMax, Rank: c.Rank,
			Faction: c.Faction, NPCFlags: c.NPCFlags, Pos: c.Pos, Wander: c.Wander,
			DetectionYd: float32(c.DetectionRange), FlagsExtra: c.FlagsExtra, GroupLeader: c.GroupLeader,
		})
	}
	return out
}

// pullKeep is the estimator's mob filter: pullHostile.
func pullKeep(f PullFilter) func(worldspawn.Spawn) bool {
	return func(s worldspawn.Spawn) bool { return pullHostile(s, f) }
}

// pullMobs converts the spawn export to estimator mobs (see planlint.PullMobs).
func pullMobs(p *dungeon.Plan, cs []creature, groups map[uint64]uint64, f PullFilter) ([]aggroest.Mob, map[uint32]uint32) {
	_ = p
	pats := map[uint64]bool{}
	for _, c := range cs {
		if c.Patrol {
			pats[c.GUID] = true
		}
	}
	return planlint.PullMobs(dungeonSpawns(cs), pats, groups, pullKeep(f))
}

// pullOwners maps each spawn guid to the export row index that pulls it (planlint.PullOwners).
func pullOwners(pts []routePoint, cs []creature) map[uint64]int {
	return planlint.PullOwners(pullRows(pts), dungeonSpawns(cs))
}

// pullTarget is the row's own creature for the estimate (planlint.PullTarget).
func pullTarget(p *dungeon.Plan, mobs []aggroest.Mob, entries map[uint32]uint32, rp routePoint, owned map[uint64]int) int {
	row := planlint.PullRow{
		Point: dungeon.Point{X: rp.x, Y: rp.y, Z: rp.z},
		Party: dungeon.Point{X: rp.px, Y: rp.py, Z: rp.pz},
	}
	return planlint.PullTarget(p.KillFirst, mobs, entries, rp.ownerIdx, row, owned)
}

// packIDs maps each spawn guid to its pack id (the smallest member guid); nil world: every spawn its own pack.
func packIDs(w *worldspawn.World) map[uint64]uint64 { return planlint.PackIDs(w) }

// pullHostile is hostileMob for the pull lint, with the check's awakened and scripted entries always hostile: the
// encounter's script turns them hostile when the pull starts, so their npc_flags (a scripted gossip NPC) and their faction
// (35, the stoned Stone Keepers, which the default -ignore-factions also drops) are no reason to leave them out of the
// count. The critter rule still applies.
func pullHostile(c worldspawn.Spawn, f PullFilter) bool {
	if f.Awakened[c.Entry] || f.Scripted[c.Entry] {
		return c.LevelMax > 1
	}
	if c.NPCFlags != 0 || c.LevelMax <= 1 {
		return false
	}
	if c.Faction == 35 || f.Ignore[c.Faction] || f.Friendly[c.Faction] {
		return false
	}
	return true
}
