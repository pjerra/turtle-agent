package main

import (
	"fmt"
	"sort"
	"strings"

	"github.com/pjerra/turtle-agent/internal/dungeon"
)

// npcFlagNames names the UNIT_NPC_FLAG bits a mob that is not a mob carries (Unit.h, 1.12).
var npcFlagNames = []struct {
	bit  uint32
	name string
}{{0x1, "gossip"}, {0x2, "questgiver"}, {0x10, "trainer"}, {0x80, "vendor"}, {0x1000, "flightmaster"}, {0x4000, "innkeeper"},
	{0x20000, "banker"}, {0x40000, "petition"}, {0x80000, "tabard"}, {0x100000, "battlemaster"}, {0x200000, "auctioneer"}, {0x400000, "stablemaster"}, {0x800000, "repair"}}

func describeFlags(f uint32) string {
	var out []string
	for _, n := range npcFlagNames {
		if f&n.bit != 0 {
			out = append(out, n.name)
		}
	}
	if len(out) == 0 {
		return fmt.Sprint(f)
	}
	return fmt.Sprintf("%d (%s)", f, strings.Join(out, ", "))
}

// CheckOptions are the flags of the check.
type CheckOptions struct {
	// Team is the team of the leader the plan is run with ("alliance" or "horde"); something gated to the other team is not
	// looked at, as the driver skips it. Empty checks everything.
	Team string
	// Friendly lists the factions known to be friendly to the leader's team. dungeongen cannot tell by itself: the friend or foe
	// relation of a faction is in FactionTemplate.dbc (a faction template's friend and enemy masks and groups), which the world
	// database export does not carry. 35 (friendly to all) is always in it.
	Friendly map[int]bool
}

// Check lints a plan against the creature export (creatures.tsv): a boss or pack member that is no mob, or that is friendly to the leader's
// team, would make the driver wait for a kill that never starts (the Alliance and Uldaman's Baelog, Eric and Olaf, faction 122,
// questgivers). It returns one warning per entry.
//
// What it can and cannot know. npc_flags other than 0 mark an NPC (questgiver, vendor, trainer, gossip) that a plan does not fight:
// that is certain (unless the part is gated to a team: then the author meant that team to fight it). A faction is friendly only when it is in Friendly or is 35; for any other faction the check says nothing about the
// leader's team, but notes when the faction is shared with creatures that carry npc_flags (a questgiver's faction is usually friendly
// to some team). Hostility toward the other team cannot be told at all without FactionTemplate.dbc.
func Check(p *dungeon.Plan, cs []creature, o CheckOptions) []string {
	byEntry := map[uint32]creature{}
	npcFaction := map[int][]string{} // faction -> names of creatures with npc_flags that use it
	for _, c := range cs {
		if _, ok := byEntry[c.Entry]; !ok {
			byEntry[c.Entry] = c
		}
	}
	entries := make([]uint32, 0, len(byEntry))
	for e := range byEntry {
		entries = append(entries, e)
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i] < entries[j] })
	for _, e := range entries {
		if c := byEntry[e]; c.NPCFlags != 0 {
			npcFaction[c.Faction] = append(npcFaction[c.Faction], c.Name)
		}
	}
	friendly := func(f int) bool { return f == 35 || o.Friendly[f] }
	var out []string
	seen := map[string]bool{}
	warn := func(where string, entry uint32, gate string) {
		c, ok := byEntry[entry]
		if !ok {
			return // not in the export: nothing to say
		}
		var why []string
		// A part gated to a team is one the author meant that team to fight (the Lost Dwarves are questgivers the Horde kills), so the
		// flags are not repeated for it; whether it is friendly to that team is still looked at.
		if c.NPCFlags != 0 && gate == "" {
			why = append(why, fmt.Sprintf("npc_flags %s: it is an NPC, not a mob", describeFlags(c.NPCFlags)))
		}
		if friendly(c.Faction) {
			why = append(why, fmt.Sprintf("faction %d is friendly to the %s", c.Faction, teamOrLeader(o.Team)))
		} else if names := npcFaction[c.Faction]; c.NPCFlags == 0 && len(names) > 0 {
			why = append(why, fmt.Sprintf("faction %d is also used by NPCs with npc_flags (%s): it may be friendly to a team (unknown without FactionTemplate.dbc)", c.Faction, firstNames(names, 3)))
		}
		if len(why) == 0 {
			return
		}
		key := where + fmt.Sprint(entry)
		if seen[key] {
			return
		}
		seen[key] = true
		out = append(out, fmt.Sprintf("%s: %s (%d): %s", where, c.Name, entry, strings.Join(why, "; ")))
	}
	for i, l := range p.Legs {
		name := fmt.Sprintf("legs[%d] (%s)", i, l.Name)
		for j, g := range l.PullGroups {
			if gatedOut(g.Team, o.Team) {
				continue
			}
			for _, e := range g.Entries {
				warn(fmt.Sprintf("%s.pull_groups[%d]", name, j), e, g.Team)
			}
		}
		if b := l.Boss; b != nil && !gatedOut(b.Team, o.Team) {
			warn(name+".boss", b.Entry, b.Team)
			for _, e := range b.First {
				warn(name+".boss.first", e, b.Team)
			}
		}
	}
	return out
}

func gatedOut(gate, team string) bool { return gate != "" && team != "" && gate != team }

func teamOrLeader(team string) string {
	if team == "" {
		return "leader's team"
	}
	return team
}

func firstNames(names []string, n int) string {
	if len(names) > n {
		return strings.Join(names[:n], ", ") + ", ..."
	}
	return strings.Join(names, ", ")
}

// bossWarnings is what Generate puts in a leg's notes for its boss: an NPC (npc_flags) or a faction the caller named as friendly.
func bossWarnings(c creature, friendly map[int]bool) []string {
	var out []string
	if c.NPCFlags != 0 {
		out = append(out, fmt.Sprintf("WARNING: boss %d (%s) has npc_flags %s: it is an NPC, not a mob. A leader it is friendly to cannot kill it; gate the boss with \"team\" or drop it.", c.Entry, c.Name, describeFlags(c.NPCFlags)))
	}
	if friendly[c.Faction] {
		out = append(out, fmt.Sprintf("WARNING: boss %d (%s) has faction %d, which -friendly-factions names as friendly to the leader's team.", c.Entry, c.Name, c.Faction))
	}
	return out
}
