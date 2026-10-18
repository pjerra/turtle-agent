package main

import (
	"fmt"
	"math"
	"sort"

	"github.com/pjerra/turtle-agent/internal/dungeon"
)

// PackAggroMargin is the yards added to a pull group's radius before another mob counts as inside the pack (a mob that stands that near
// the group's area aggroes with it).
const PackAggroMargin = 3

// defaultPackRadius is the driver's PackRadius (dungeon.DefaultConfig), the radius of a group that gives none.
const defaultPackRadius = 25

// PackOverlaps lists the mobs of the creature export that stand within a pull group's radius plus PackAggroMargin of one of its
// positions without being listed in that group: a mob of another pull group, a boss, or a spawn the plan forgot, which the pull would
// drag in unplanned (Wailing Caverns: a Druid standing on Lady Anacondra's spawn, a pack 3 yd from her). A mob is listed when its entry is
// in the group's entries (or its patrol's) and the group lists at least as many of that entry as stand in range; the leg's skip list
// (critters, rares, friendly NPCs) and mobs that are not hostile (ignored factions such as 35, npc_flags, level 1 critters) are left out.
// Distances are 3-D.
func PackOverlaps(p *dungeon.Plan, cs []creature, ignore map[int]bool) []string {
	var out []string
	for i, l := range p.Legs {
		skip := map[uint32]bool{}
		for _, e := range l.Skip {
			skip[e] = true
		}
		for j, g := range l.PullGroups {
			r := float64(g.Radius)
			if r == 0 {
				r = defaultPackRadius
			}
			want := map[uint32]int{}
			for _, e := range g.Entries {
				want[e]++
			}
			if g.Patrol != nil {
				for _, e := range g.Patrol.Entries {
					want[e] += 100 // a patrol's members are listed by the patrol, however many stand on its spawn rows
				}
			}
			stand := map[uint32][]creature{}
			for _, c := range cs {
				if skip[c.Entry] || ignore[c.Faction] || c.NPCFlags != 0 || c.LevelMax <= 1 {
					continue
				}
				for _, q := range g.Positions {
					if packDist(c.Pos, q) <= r+PackAggroMargin {
						stand[c.Entry] = append(stand[c.Entry], c)
						break
					}
				}
			}
			var entries []uint32
			for e := range stand {
				entries = append(entries, e)
			}
			sort.Slice(entries, func(a, b int) bool { return entries[a] < entries[b] })
			for _, e := range entries {
				if n := len(stand[e]); n > want[e] {
					c := stand[e][0]
					out = append(out, fmt.Sprintf("legs[%d] (%s) pull_groups[%d] (radius %.0f + %d): %d x %s (%d) stand in range, the group lists %d; first at %.1f %.1f %.1f", i, l.Name, j+1, r, PackAggroMargin, n, c.Name, e, want[e], c.Pos.X, c.Pos.Y, c.Pos.Z))
				}
			}
		}
	}
	return out
}

func packDist(a, b dungeon.Point) float64 {
	dx, dy, dz := float64(a.X-b.X), float64(a.Y-b.Y), float64(a.Z-b.Z)
	return math.Sqrt(dx*dx + dy*dy + dz*dz)
}
