package raidwalk

import (
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/pjerra/turtle-agent/internal/dungeon"
	"github.com/pjerra/turtle-agent/internal/objects"
	"github.com/pjerra/turtle-agent/internal/packet"
	"github.com/pjerra/turtle-agent/internal/planexport"
	"github.com/pjerra/turtle-agent/internal/trashcheck"
	"github.com/pjerra/turtle-agent/internal/worldspawn"
)

// The --gm-clear test tool (opt-in, test servers only): before the raid moves, the commander GM-kills every live unit of the named
// pull groups (or of a leg's boss) so a later leg can be tested while a trash wipe is still being fixed. A walk that used it is
// ASSISTED: its summary and report say so, and it is never a real clear.

// GMClearBoss is the group word of a --gm-clear-boss entry.
const GMClearBoss = "boss"

// GMClearSpec is one --gm-clear or --gm-clear-boss value: a leg (its row number or name) and either pull group numbers (1-based, in
// route order among the leg's pull and hold rows) or the leg's boss.
type GMClearSpec struct {
	Leg    string
	Groups []int
	Boss   bool
}

// ParseGMClear reads one --gm-clear value, "<leg>:<group>[,<group>...]". The leg is a row number or a name; a group is a number from 1.
func ParseGMClear(v string) (GMClearSpec, error) {
	leg, groups, ok := strings.Cut(v, ":")
	leg, groups = strings.TrimSpace(leg), strings.TrimSpace(groups)
	if !ok || leg == "" || groups == "" {
		return GMClearSpec{}, fmt.Errorf("--gm-clear %q: want \"<leg>:<group>[,<group>...]\" (a leg number or name and pull group numbers from 1)", v)
	}
	spec := GMClearSpec{Leg: leg}
	seen := map[int]bool{}
	for _, g := range strings.Split(groups, ",") {
		n, err := strconv.Atoi(strings.TrimSpace(g))
		if err != nil || n < 1 {
			return GMClearSpec{}, fmt.Errorf("--gm-clear %q: %q is not a pull group number (1 or more)", v, g)
		}
		if seen[n] {
			return GMClearSpec{}, fmt.Errorf("--gm-clear %q: group %d is named twice", v, n)
		}
		seen[n] = true
		spec.Groups = append(spec.Groups, n)
	}
	return spec, nil
}

// ParseGMClearBoss reads one --gm-clear-boss value: a leg number or name.
func ParseGMClearBoss(v string) (GMClearSpec, error) {
	leg := strings.TrimSpace(v)
	if leg == "" || strings.ContainsAny(leg, ":,") {
		return GMClearSpec{}, fmt.Errorf("--gm-clear-boss %q: want a leg number or name", v)
	}
	return GMClearSpec{Leg: leg, Boss: true}, nil
}

// GMClearEntry is one unit the tool tried to kill: the summary's gm_clear list and the report's table. Killed says the unit's frames
// showed it dead; Tries counts the kill lines sent (2 at most).
type GMClearEntry struct {
	Leg    string `json:"leg"`
	Group  string `json:"group"`
	Entry  uint32 `json:"entry"`
	GUID   string `json:"guid"`
	Killed bool   `json:"killed"`
	Tries  int    `json:"tries"`
}

// GMClearLabel is the "<leg>:<group>" of a run.log line and a summary entry.
func GMClearLabel(leg, group string) string { return leg + ":" + group }

// GMClearLogLine is the run.log line of one confirmed kill.
func GMClearLogLine(e GMClearEntry) string {
	return fmt.Sprintf("gm-clear: killed %d %s (%s)", e.Entry, e.GUID, GMClearLabel(e.Leg, e.Group))
}

// GMClearPack is one pack to clear: every member of the pack as the world data defines it (a creature_groups AGGRO_TOGETHER group, or
// the chain of aggro links, both directions), whether or not the member stands near the pull row.
type GMClearPack struct {
	Leg      string // the leg as the messages name it (its name, else its number)
	Group    string // the pull group number, or GMClearBoss
	Step     int    // the 1-based step of the pull/boss row the pack was found from
	ID       uint64 // the pack's smallest member spawn guid
	Centroid dungeon.Point
	Radius   float32
	// Members maps a member's spawn guid (the low part of its world guid) to its entry.
	Members map[uint64]uint32
}

// PlanGMClear expands the specs into the packs to kill, in spec order. Each group is the leg's n-th pull (or hold) row; the boss is the
// leg's boss row. The packs a row clears are trashcheck.ClearedPacks: the pack model the pull estimator and the trash check use, so a
// pack's linked members come with it. A pack found by two rows is listed once, under the first. An error names the spec that has no
// leg, no such group, or no spawn in the world export.
func PlanGMClear(rows planexport.Rows, world *worldspawn.World, specs []GMClearSpec) ([]GMClearPack, error) {
	var out []GMClearPack
	seen := map[uint64]bool{}
	for _, sp := range specs {
		leg, name, err := findLeg(rows, sp.Leg)
		if err != nil {
			return nil, fmt.Errorf("--gm-clear: %v", err)
		}
		lo, hi := legSpan(rows, leg)
		if lo < 0 {
			return nil, fmt.Errorf("--gm-clear: leg %s has no step", name)
		}
		var pulls, bosses []int
		for i := lo; i <= hi; i++ {
			if rows.Steps[i].Leg != leg {
				continue
			}
			switch rows.Steps[i].Kind {
			case planexport.KindPull, planexport.KindHold:
				pulls = append(pulls, i)
			case planexport.KindBoss:
				bosses = append(bosses, i)
			}
		}
		type target struct {
			row   int
			group string
		}
		var targets []target
		if sp.Boss {
			if len(bosses) == 0 {
				return nil, fmt.Errorf("--gm-clear-boss: leg %s has no boss row", name)
			}
			for _, i := range bosses {
				targets = append(targets, target{i, GMClearBoss})
			}
		}
		for _, g := range sp.Groups {
			if g > len(pulls) {
				return nil, fmt.Errorf("--gm-clear: leg %s has %d pull groups, not %d", name, len(pulls), g)
			}
			targets = append(targets, target{pulls[g-1], strconv.Itoa(g)})
		}
		for _, t := range targets {
			row := rows.Steps[t.row]
			// The step number is the row's place in (leg, seq) order plus one, as the server numbers it; the rows slice is in that order.
			packs := trashcheck.ClearedPacks(world, row)
			if len(packs) == 0 {
				return nil, fmt.Errorf("--gm-clear: leg %s group %s (step %d): the world export holds no spawn of its entries near the row", name, t.group, t.row+1)
			}
			for _, p := range packs {
				if seen[p.ID] {
					continue
				}
				seen[p.ID] = true
				members := map[uint64]uint32{}
				for _, g := range p.Members {
					for _, s := range world.Spawns {
						if s.GUID == g {
							members[g] = s.Entry
							break
						}
					}
				}
				out = append(out, GMClearPack{Leg: name, Group: t.group, Step: t.row + 1, ID: p.ID,
					Centroid: p.Centroid, Radius: p.Radius, Members: members})
			}
		}
	}
	return out, nil
}

// SpawnGUID is the DB spawn guid of a creature's world guid: its low 24 bits (ObjectGuid.h: high, entry, counter), the number the world
// export lists as the spawn's guid.
func SpawnGUID(g packet.GUID) uint64 { return uint64(g) & 0x00FFFFFF }

// LiveMembers picks, from the units a read shows, the live ones that are members of the pack: a creature whose spawn guid and entry both
// match a member. A dead unit is left out (a corpse needs no second kill), and the order is by guid.
func (p GMClearPack) LiveMembers(units []objects.Unit) []objects.Unit {
	var out []objects.Unit
	for _, u := range units {
		if !u.GUID.IsCreature() || u.Dead {
			continue
		}
		if e, ok := p.Members[SpawnGUID(u.GUID)]; ok && e == u.GUID.Entry() {
			out = append(out, u)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].GUID < out[j].GUID })
	return out
}

// GMClearLegs lists the distinct legs the packs came from, for the summary's ASSISTED marks.
func GMClearLegs(packs []GMClearPack) []string {
	var out []string
	seen := map[string]bool{}
	for _, p := range packs {
		if !seen[p.Leg] {
			seen[p.Leg] = true
			out = append(out, p.Leg)
		}
	}
	return out
}

// GMClearCounts is how many entries were killed and how many would not die.
func GMClearCounts(es []GMClearEntry) (killed, failed int) {
	for _, e := range es {
		if e.Killed {
			killed++
		} else {
			failed++
		}
	}
	return killed, failed
}

// GMClearSummaryLine is the one ASSISTED line of summary.txt and the report.
func GMClearSummaryLine(es []GMClearEntry, legs []string) string {
	killed, failed := GMClearCounts(es)
	line := fmt.Sprintf("ASSISTED: --gm-clear killed %d units by GM command (%s); this is not a real clear", killed, strings.Join(legs, ", "))
	if failed > 0 {
		line += fmt.Sprintf("; %d would not die", failed)
	}
	return line
}
