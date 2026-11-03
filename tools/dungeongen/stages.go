// The stage lint: the offline checks of a pull group's scripted pull stages (dungeon.PullGroup.Stages, spec camp-pull-and-aggro-count
// 4.5). Each stage exports as one camp chain (camp move, tag rows, camp pull row scoped to the stage's formations), so rules 4 and 5 are
// CampPullFindings' on the stage rows (the tag spot has a shot and body-pulls nothing, the camp is on the cleared route). The checks here are
// the ones that need the stage list and the world:
//
//  0. structure: every peel guid is a formation leader of the group, every formation of the group is peeled by exactly one stage, and the
//     tag spot stands within the run leader's sight of its stage;
//  1. caster distance: the stage's camp is at least dungeon.CasterCampYd (3D) from every spawn of the plan's caster_entries in the stage's
//     own and the later stages' formations (a caster in range of the camp can plant and hold the fight away from the raid);
//  2. one pack per tag: the aggro-count estimator (internal/aggroest) from the tag spot, with the tank-only walk camp -> tag, the fight at the
//     camp and the drag tag -> camp, returns exactly the stage's formations (earlier stages are dead and left out); any other formation is
//     named with its leader and the reason it joins, and an own formation the tag does not bring is named too;
//  3. camp clear: the camp's fight point wakes no spawn that a later row pulls (or no row does): reach + raid spread + wander.
//
// Sight is "always sees" (the high side, as in pull_size.go).
package main

import (
	"fmt"
	"math"
	"sort"

	"github.com/pjerra/turtle-agent/internal/aggroest"
	"github.com/pjerra/turtle-agent/internal/dungeon"
	"github.com/pjerra/turtle-agent/internal/planexport"
	"github.com/pjerra/turtle-agent/internal/worldspawn"
)

// stageLabel reads the comment plan-export writes on a stage's rows ("pull group 0 stage 1/3 ...") into the group's 0-based index, the stage
// number (1-based) and the stage count.
func stageLabel(label string) (gi, si, k int, ok bool) {
	n, _ := fmt.Sscanf(label, "pull group %d stage %d/%d", &gi, &si, &k)
	return gi, si, k, n == 3
}

// leaderOf is the creature_groups leader of a spawn (its own guid when it is in no group).
func leaderOf(c creature) uint64 {
	if c.GroupLeader != 0 {
		return c.GroupLeader
	}
	return c.GUID
}

// leaderPackIDs groups the spawns by group_leader alone (a nil world: no creature_linking edges are known).
func leaderPackIDs(cs []creature) map[uint64]uint64 {
	out := map[uint64]uint64{}
	for _, c := range cs {
		if c.GroupLeader != 0 {
			out[c.GUID] = c.GroupLeader
		}
	}
	return out
}

// routeOwners maps every spawn to the row of the export that pulls it (routeOwnerOf's rule).
func routeOwners(pts []routePoint, cs []creature) map[uint64]routePoint {
	var owners []routePoint
	for _, q := range pts {
		if q.ownerIdx >= 0 {
			owners = append(owners, q)
		}
	}
	byLeader := map[uint64][]creature{}
	for _, c := range cs {
		if c.GroupLeader != 0 {
			byLeader[c.GroupLeader] = append(byLeader[c.GroupLeader], c)
		}
	}
	out := map[uint64]routePoint{}
	for _, c := range cs {
		if o, ok := routeOwnerOf(owners, byLeader, c); ok {
			out[c.GUID] = o
		}
	}
	return out
}

// stageSpawns are one stage's spawns: its peeled formations, resolved.
type stageSpawns struct {
	leaders map[uint64]bool // the formation leaders (resolved from the peel guids)
	spawns  []creature
	guids   map[uint64]bool
	centre  dungeon.Point
}

// groupFormations is the set of formation leaders a pull group holds: every spawn with an entry of the group within its radius (the pack
// radius when it gives none) of one of its positions, by its formation's leader.
func groupFormations(g dungeon.PullGroup, cs []creature, eligible func(creature) bool) map[uint64]bool {
	r := float64(g.Radius)
	if r == 0 {
		r = defaultPackRadius
	}
	out := map[uint64]bool{}
	for _, c := range cs {
		if !eligible(c) || !containsEntry(g.Entries, c.Entry) {
			continue
		}
		for _, q := range g.Positions {
			if dist(q, c.Pos) <= r {
				out[leaderOf(c)] = true
				break
			}
		}
	}
	return out
}

func containsEntry(es []uint32, e uint32) bool {
	for _, x := range es {
		if x == e {
			return true
		}
	}
	return false
}

// StageFindings lints every staged pull group of the plan (see the file comment). cs are the plan's spawns (readCreatures); w is the map's
// world (packs: groups and links) and may be nil (then the formations are the group_leader groups alone). One line per violation, naming
// the leg, the pull group and the stage; a peel guid that is no spawn, or an export the plan-export refuses, is an error.
func StageFindings(p *dungeon.Plan, cs []creature, w *worldspawn.World, ignore map[int]bool) ([]string, error) {
	staged := false
	for _, l := range p.Legs {
		for _, g := range l.PullGroups {
			staged = staged || len(g.Stages) > 0
		}
	}
	if !staged {
		return nil, nil
	}
	byGUID := map[uint64]creature{}
	byLeader := map[uint64][]creature{}
	for _, c := range cs {
		byGUID[c.GUID] = c
		byLeader[leaderOf(c)] = append(byLeader[leaderOf(c)], c)
	}
	eligible := func(c creature) bool {
		return c.NPCFlags == 0 && !ignore[c.Faction] && c.LevelMax > 1
	}
	var out []string
	resolved := map[[3]int]*stageSpawns{} // leg, group, stage -> spawns
	for li, l := range p.Legs {
		for gi, g := range l.PullGroups {
			where := func(si int) string {
				return fmt.Sprintf("%s legs[%d] (%s) pull_groups[%d] stage %d/%d", p.Name, li, l.Name, gi, si+1, len(g.Stages))
			}
			if len(g.Stages) == 0 {
				continue
			}
			held := groupFormations(g, cs, eligible)
			peeledBy := map[uint64]int{}
			for si, st := range g.Stages {
				ss := &stageSpawns{leaders: map[uint64]bool{}, guids: map[uint64]bool{}}
				resolved[[3]int{li, gi, si}] = ss
				for _, id := range st.Peel {
					c, ok := byGUID[uint64(id)]
					if !ok {
						return out, fmt.Errorf("%s: peel %d is no spawn of the world export", where(si), id)
					}
					lead := leaderOf(c)
					if lead != c.GUID {
						out = append(out, fmt.Sprintf("%s: peel %d is not a formation leader (the formation is led by %d): peel the leader", where(si), id, lead))
					}
					if !held[lead] {
						out = append(out, fmt.Sprintf("%s: peel %d (formation led by %d) is not in the group (no spawn of its entries within %g yd of its positions)", where(si), id, lead, groupRadius(g)))
					}
					if at, dup := peeledBy[lead]; dup && at != si {
						out = append(out, fmt.Sprintf("%s: the formation led by %d is also peeled by stage %d: a formation is pulled by one stage only", where(si), lead, at+1))
					}
					peeledBy[lead] = si
					ss.leaders[lead] = true
					for _, m := range byLeader[lead] {
						if !ss.guids[m.GUID] {
							ss.guids[m.GUID] = true
							ss.spawns = append(ss.spawns, m)
						}
					}
				}
				var cx, cy, cz float64
				for _, m := range ss.spawns {
					cx, cy, cz = cx+float64(m.Pos.X), cy+float64(m.Pos.Y), cz+float64(m.Pos.Z)
				}
				if n := float64(len(ss.spawns)); n > 0 {
					ss.centre = dungeon.Point{X: float32(cx / n), Y: float32(cy / n), Z: float32(cz / n)}
					if len(st.Tag) > 0 {
						if d := dist(st.Tag[len(st.Tag)-1], ss.centre); d > dungeon.RunLeaderSeeYd {
							out = append(out, fmt.Sprintf("%s: the tag spot is %.1f yd from the centre of the peeled formations: the run leader sees %d yd at most", where(si), d, dungeon.RunLeaderSeeYd))
						}
					}
				}
			}
			var unheld []uint64
			for lead := range held {
				if _, ok := peeledBy[lead]; !ok {
					unheld = append(unheld, lead)
				}
			}
			sort.Slice(unheld, func(i, j int) bool { return unheld[i] < unheld[j] })
			for _, lead := range unheld {
				out = append(out, fmt.Sprintf("%s legs[%d] (%s) pull_groups[%d]: the formation led by %d (%s, %d spawns) is peeled by no stage", p.Name, li, l.Name, gi, lead, byGUID[lead].Name, len(byLeader[lead])))
			}
		}
	}

	pts, err := routePoints(p, cs)
	if err != nil {
		return out, err
	}
	ids := leaderPackIDs(cs)
	if w != nil {
		ids = packIDs(w)
	}
	mobs, _ := pullMobs(p, cs, ids)
	owners := routeOwners(pts, cs)
	spread := float32(aggroest.StandSpreadPartyYd)
	if p.IsRaid() {
		spread = float32(aggroest.StandSpreadRaidYd)
	}
	for li, l := range p.Legs {
		legSkip := map[uint32]bool{}
		for _, e := range l.Skip {
			legSkip[e] = true
		}
		exportLeg, _ := planexport.LegNumber(p, li)
		for gi, g := range l.PullGroups {
			for si, st := range g.Stages {
				ss := resolved[[3]int{li, gi, si}]
				where := fmt.Sprintf("%s legs[%d] (%s) pull_groups[%d] stage %d/%d", p.Name, li, l.Name, gi, si+1, len(g.Stages))
				if len(ss.spawns) == 0 || len(st.Tag) == 0 {
					continue
				}
				// Rule 1: caster distance from the camp, own and later stages.
				for sj := si; sj < len(g.Stages); sj++ {
					for _, m := range resolved[[3]int{li, gi, sj}].spawns {
						if !containsEntry(p.CasterEntries, m.Entry) {
							continue
						}
						if d := dist(st.Camp, m.Pos); d < dungeon.CasterCampYd {
							out = append(out, fmt.Sprintf("%s: camp is %.1f yd from %s (guid %d, entry %d) of stage %d: at least %d yd (a caster there may plant and hold the fight away from the raid)",
								where, d, m.Name, m.GUID, m.Entry, sj+1, dungeon.CasterCampYd))
						}
					}
				}
				// Rule 2: one pack per tag.
				dead := map[uint64]bool{}
				for sj := 0; sj < si; sj++ {
					for gd := range resolved[[3]int{li, gi, sj}].guids {
						dead[gd] = true
					}
				}
				var live []aggroest.Mob
				for _, m := range mobs {
					if !dead[uint64(m.Guid)] {
						live = append(live, m)
					}
				}
				tagSpot := st.Tag[len(st.Tag)-1]
				target, best := -1, math.MaxFloat64
				for i, m := range live {
					if !ss.guids[uint64(m.Guid)] {
						continue
					}
					if d := dist(tagSpot, m.Pos); d < best {
						target, best = i, d
					}
				}
				if target < 0 {
					out = append(out, fmt.Sprintf("%s: no eligible spawn among the peeled formations to tag", where))
				} else {
					walk := append([]dungeon.Point{st.Camp}, st.Tag...)
					var drag []dungeon.Point
					for j := len(st.Tag) - 1; j >= 0; j-- {
						drag = append(drag, st.Tag[j])
					}
					drag = append(drag, st.Camp)
					res := aggroest.Estimate(live, target, aggroest.Geometry{
						TankWalk: walk, TankSpreadYd: aggroest.TankSpreadYd,
						Stand: tagSpot, StandSpreadYd: aggroest.TankSpreadYd,
						HasFight: true, Fight: st.Camp, FightSpreadYd: spread,
						Drag: drag,
					}, aggroest.Params{
						PartyLevel: routePartyLevel(p), Rate: routeAggroRate,
						RingYd: aggroest.RingYd, RingHops: aggroest.RingHops, FloorDz: aggroest.FloorDz,
					})
					came := map[uint64]bool{}
					foreign := map[uint64]aggroest.Member{}
					var order []uint64
					for _, m := range res.Members {
						came[uint64(m.Guid)] = true
						if ss.guids[uint64(m.Guid)] {
							continue
						}
						lead := leaderOf(byGUID[uint64(m.Guid)])
						if _, seen := foreign[lead]; !seen {
							foreign[lead] = m
							order = append(order, lead)
						}
					}
					for _, lead := range order {
						m := foreign[lead]
						c := byGUID[uint64(m.Guid)]
						out = append(out, fmt.Sprintf("%s: tag spot wakes %s (guid %d, entry %d, formation led by %d) via %s: a stage pulls its own formations only", where, c.Name, c.GUID, c.Entry, lead, m.Why))
					}
					for lead := range ss.leaders {
						var missing []uint64
						for _, m := range byLeader[lead] {
							if _, alive := mobIndex(live, m.GUID); alive && !came[m.GUID] {
								missing = append(missing, m.GUID)
							}
						}
						if len(missing) > 0 {
							out = append(out, fmt.Sprintf("%s: the formation led by %d is not woken by the tag (%d of its %d spawns would stay: guids %s): the stage's pull would wait for units that never come", where, lead, len(missing), len(byLeader[lead]), guidList64(missing)))
						}
					}
				}
				// Rule 3: the camp wakes no later pack.
				rowIdx := -1
				for _, q := range pts {
					if g0, s0, _, ok := stageLabel(q.label); ok && q.campPull && q.leg == exportLeg && g0 == gi && s0 == si+1 {
						rowIdx = q.idx
					}
				}
				if rowIdx < 0 {
					continue
				}
				for _, c := range cs {
					if !eligible(c) || c.FlagsExtra&worldspawn.NoAggro != 0 || legSkip[c.Entry] {
						continue
					}
					o, owned := owners[c.GUID]
					if owned && o.idx <= rowIdx {
						continue
					}
					reach := routeReach(p, c)
					if reach <= 0 {
						continue
					}
					if d := dist(st.Camp, c.Pos); d < reach {
						by := "no row"
						if owned {
							by = fmt.Sprintf("leg %d row %d", o.leg, o.seq)
						}
						out = append(out, fmt.Sprintf("%s: camp wakes %s (guid %d, entry %d) pulled by %s: %.1f yd away, reach %.1f", where, c.Name, c.GUID, c.Entry, by, d, reach))
					}
				}
			}
		}
	}
	sort.Strings(out)
	return out, nil
}

func groupRadius(g dungeon.PullGroup) float64 {
	if g.Radius == 0 {
		return defaultPackRadius
	}
	return float64(g.Radius)
}

func mobIndex(mobs []aggroest.Mob, guid uint64) (int, bool) {
	for i, m := range mobs {
		if uint64(m.Guid) == guid {
			return i, true
		}
	}
	return -1, false
}

func guidList64(gs []uint64) string {
	var u []uint32
	for _, g := range gs {
		u = append(u, uint32(g))
	}
	sort.Slice(u, func(i, j int) bool { return u[i] < u[j] })
	return guidList(u)
}
