package raidwalk

import (
	"fmt"
	"math"
	"slices"
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

// The Reason of a GMClearEntry that is not a confirmed kill, or a member that was dead before the tool got to it.
const (
	GMClearNotSeen     = "not seen"     // no frame showed this member alive or dead: it was never in the commander's view
	GMClearAlive       = "alive"        // the member was seen alive and the kill lines did not bring a death
	GMClearAlreadyDead = "already dead" // the member's frames showed it dead before any line was sent (Killed true, Tries 0)
	// GMClearDespawned marks a member the walk's own corridor clear had already despawned in this instance before the tool
	// ran (Killed true, Tries 0). The client's object store does not drop a despawned unit, so the frames still show it
	// alive; the server no longer has it, and every `.die` on its guid is a no-op (walk 35: 52 members of the Jin'do hall
	// groups, ~25 min of fruitless retries).
	GMClearDespawned = "despawned by the corridor clear"
	// GMClearGaveUp marks a member the tool never got to because the clear's time budget ran out (Killed false, Tries 0).
	GMClearGaveUp = "not tried: the gm-clear budget ran out"
)

// GMClearEntry is one expected pack member: the summary's gm_clear list and the report's table. Killed says the unit is gone from the
// instance: its frames showed it dead, or the walk's corridor clear despawned it. Tries counts the kill lines sent (gmClearTries at
// most). A member that is not Killed has a Reason (GMClearNotSeen, GMClearAlive, GMClearGaveUp) and leaves the clear PARTIAL. GUID is
// the unit's world guid, or "spawn <n>" (the export's spawn guid) for a member never seen.
type GMClearEntry struct {
	Leg    string `json:"leg"`
	Group  string `json:"group"`
	Entry  uint32 `json:"entry"`
	GUID   string `json:"guid"`
	Killed bool   `json:"killed"`
	Tries  int    `json:"tries"`
	Reason string `json:"reason,omitempty"`
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
	// Members maps a member's spawn guid (the low part of its world guid) to its entry; Positions maps it to its spawn point.
	Members   map[uint64]uint32
	Positions map[uint64]dungeon.Point
}

// GMClearStopYd is how far a member may stand from a visit stop and still be inside the commander's view and read radius there.
const GMClearStopYd = 100

// GMPatrolReachYd is how far a patrol loop (or a spawn of a patrol entry) may stand from a pull row's point and still be
// that row's patroller. A patroller walks its loop: its spawn may lie outside the row's radius while the loop comes by the
// pull. --gm-clear clears it too, so a cleared leg does not wait at the stand for a patrol that is already dead.
const GMPatrolReachYd = 30

// Stops are the points the commander visits to see every member: a member within GMClearStopYd (2D) of an earlier stop is covered by
// it, else it gets a stop of its own, taken in spawn-guid order so the list is the same every time. A pack that fits one view has one
// stop (its first member's spawn); a pack wider than the view has several, each at a member.
func (p GMClearPack) Stops() []dungeon.Point {
	guids := make([]uint64, 0, len(p.Members))
	for g := range p.Members {
		guids = append(guids, g)
	}
	sort.Slice(guids, func(i, j int) bool { return guids[i] < guids[j] })
	var stops []dungeon.Point
	for _, g := range guids {
		pt := p.Positions[g]
		covered := false
		for _, s := range stops {
			dx, dy := float64(pt.X-s.X), float64(pt.Y-s.Y)
			if dx*dx+dy*dy <= GMClearStopYd*GMClearStopYd {
				covered = true
				break
			}
		}
		if !covered {
			stops = append(stops, pt)
		}
	}
	return stops
}

// SpawnGUIDs are the members' spawn guids, sorted.
func (p GMClearPack) SpawnGUIDs() []uint64 {
	out := make([]uint64, 0, len(p.Members))
	for g := range p.Members {
		out = append(out, g)
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}

// PlanGMClear expands the specs into the packs to kill, in spec order. Each group is the leg's n-th pull (or hold) row; the boss is the
// leg's boss row. The packs a row clears are trashcheck.ClearedPacks: the pack model the pull estimator and the trash check use, so a
// pack's linked members come with it. A pack found by two rows is listed once, under the first. An error names the spec that has no
// leg or no such group. A group whose row clears no pack at all (its units are a boss's, an event's or another non-hostile NPC's, so
// the export holds no hostile spawn of them near the row) is skipped with a warning and the other groups go on: a boss group is never
// cleared by a trash group's spec.
func PlanGMClear(rows planexport.Rows, world *worldspawn.World, specs []GMClearSpec) ([]GMClearPack, []string, error) {
	var out []GMClearPack
	var warnings []string
	seen := map[uint64]bool{}
	for _, sp := range specs {
		leg, name, err := findLeg(rows, sp.Leg)
		if err != nil {
			return nil, nil, fmt.Errorf("--gm-clear: %v", err)
		}
		lo, hi := legSpan(rows, leg)
		if lo < 0 {
			return nil, nil, fmt.Errorf("--gm-clear: leg %s has no step", name)
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
				return nil, nil, fmt.Errorf("--gm-clear-boss: leg %s has no boss row", name)
			}
			for _, i := range bosses {
				targets = append(targets, target{i, GMClearBoss})
			}
		}
		for _, g := range sp.Groups {
			if g > len(pulls) {
				return nil, nil, fmt.Errorf("--gm-clear: leg %s has %d pull groups, not %d", name, len(pulls), g)
			}
			targets = append(targets, target{pulls[g-1], strconv.Itoa(g)})
		}
		for _, t := range targets {
			row := rows.Steps[t.row]
			// The step number is the row's place in (leg, seq) order plus one, as the server numbers it; the rows slice is in that order.
			n := 0
			for _, p := range trashcheck.ClearedPacks(world, row) {
				if seen[p.ID] {
					continue
				}
				seen[p.ID] = true
				members, positions := gmMemberMaps(world, p.Members)
				out = append(out, GMClearPack{Leg: name, Group: t.group, Step: t.row + 1, ID: p.ID,
					Centroid: p.Centroid, Radius: p.Radius, Members: members, Positions: positions})
				n++
			}
			// The row's patrols: a patroller's spawn may stand outside the row's radius, but its loop comes by the pull.
			patrols, pw, err := gmPatrolPacks(world, row, name, t.group, t.row+1, seen)
			if err != nil {
				return nil, nil, err
			}
			warnings = append(warnings, pw...)
			out = append(out, patrols...)
			n += len(patrols)
			if n == 0 {
				warnings = append(warnings, fmt.Sprintf("gm-clear: WARN: skipped leg %s group %s (step %d): the world export holds no spawn of its entries near the row; the walk goes on without it", name, t.group, t.row+1))
			}
		}
	}
	return out, warnings, nil
}

// gmMemberMaps reads a pack's members: spawn guid -> entry and spawn point.
func gmMemberMaps(world *worldspawn.World, guids []uint64) (map[uint64]uint32, map[uint64]dungeon.Point) {
	members := map[uint64]uint32{}
	positions := map[uint64]dungeon.Point{}
	for _, g := range guids {
		for _, s := range world.Spawns {
			if s.GUID == g {
				members[g] = s.Entry
				positions[g] = s.Pos
				break
			}
		}
	}
	return members, positions
}

// gmPatrolPacks is the world patrol a pull row waits for: the one the plan's patrol names, never one that merely shares
// its entry. StepRow.PatrolSource is the plan's source string (dungeon.Patrol.Source); when it names a creature_movement
// guid the patrol is the world loop of that guid (a spawn's own loop, or a template loop of the same guid), and when it
// names none the patrol is the world loop whose path is the plan's, point for point. The row's entry and radius play no
// part: several patrols share an entry (ZG's 11352 walks at Jeklik's stair, at Arlokk's and at Mandokir's), so an entry
// and distance match cleared another leg's patrol. A row whose plan names no patrol it can find is a warning, not a
// failure: its own packs are still cleared and the walk goes on.
func gmPatrolPacks(world *worldspawn.World, row planexport.StepRow, leg, group string, step int, seen map[uint64]bool) ([]GMClearPack, []string, error) {
	if len(row.PatrolEntries) == 0 && row.PatrolSource == "" && len(row.PatrolPath) == 0 {
		return nil, nil, nil
	}
	if world == nil {
		return nil, nil, fmt.Errorf("gm-clear: leg %s group %s (step %d) names a patrol (%q) but there is no world export", leg, group, step, row.PatrolSource)
	}
	guid, hasGUID := gmPatrolGUID(row.PatrolSource)
	// The anchor: the world loop the plan's source names, or (when it names no guid) the loop that is the plan's path.
	anchor := -1
	for i := range world.Patrols {
		p := &world.Patrols[i]
		if len(p.Path) == 0 {
			continue
		}
		if hasGUID {
			if slices.Contains(p.SpawnGUIDs, guid) {
				anchor = i
				break
			}
			if g, ok := gmPatrolGUID(p.Source); ok && g == guid {
				anchor = i
				break
			}
			continue
		}
		if len(row.PatrolPath) > 0 && gmSamePath(p.Path, row.PatrolPath) {
			anchor = i
			break
		}
	}
	var out []GMClearPack
	var warnings []string
	if anchor < 0 {
		what := fmt.Sprintf("source %q", row.PatrolSource)
		if !hasGUID {
			what = fmt.Sprintf("%d waypoints", len(row.PatrolPath))
		}
		warnings = append(warnings, fmt.Sprintf("gm-clear: WARN: leg %s group %s (step %d): no world patrol matches the plan's patrol (%s, entries %v); it is not cleared",
			leg, group, step, what, row.PatrolEntries))
		return out, warnings, nil
	}
	// The anchor's loop is the loop its formation walks: every world patrol on the same path walks it too (the formation's
	// followers carry their own movement rows in some exports, or are merged into the leader's patrol in others).
	for i := range world.Patrols {
		p := &world.Patrols[i]
		if len(p.Path) == 0 || !gmSamePath(p.Path, world.Patrols[anchor].Path) {
			continue
		}
		guids := append([]uint64(nil), p.SpawnGUIDs...)
		members, positions := gmMemberMaps(world, guids)
		if len(members) == 0 {
			continue
		}
		id := uint64(math.MaxUint64)
		for _, g := range guids {
			if _, ok := members[g]; ok && g < id {
				id = g
			}
		}
		if seen[id] {
			continue
		}
		seen[id] = true
		out = append(out, GMClearPack{Leg: leg, Group: group, Step: step, ID: id,
			Centroid: p.Path[0], Radius: float32(row.Radius), Members: members, Positions: positions})
	}
	return out, warnings, nil
}

// gmPatrolGUID reads the creature_movement (or spawn) guid a plan patrol's source names: the first number in the string
// ("creature_movement 91493 (27 points...)", "creature_movement guid 2574092", "creature_movement of spawn 155626",
// "spawn 88074", or the bare guid). A template loop's source names its entry instead, not a spawn: ok is false then.
func gmPatrolGUID(source string) (uint64, bool) {
	s := strings.ToLower(source)
	if strings.Contains(s, "creature_movement_template") {
		return 0, false
	}
	i := strings.IndexFunc(s, func(r rune) bool { return r >= '0' && r <= '9' })
	if i < 0 {
		return 0, false
	}
	j := i
	for j < len(s) && s[j] >= '0' && s[j] <= '9' {
		j++
	}
	n, err := strconv.ParseUint(s[i:j], 10, 64)
	if err != nil {
		return 0, false
	}
	return n, true
}

// gmSamePath says two patrol loops are the same loop: as many points, each within gmSamePathYd of the other's (the plan's
// points are the export's, so this is a rounding tolerance, not a similarity test).
func gmSamePath(a, b []dungeon.Point) bool {
	if len(a) != len(b) || len(a) == 0 {
		return false
	}
	for i := range a {
		dx, dy, dz := float64(a[i].X-b[i].X), float64(a[i].Y-b[i].Y), float64(a[i].Z-b[i].Z)
		if math.Sqrt(dx*dx+dy*dy+dz*dz) > gmSamePathYd {
			return false
		}
	}
	return true
}

// gmSamePathYd is how far apart two points of a "same path" pair may lie: the plan writes the export's numbers, rounded.
const gmSamePathYd = 0.05

// SpawnGUID is the DB spawn guid of a creature's world guid: its low 24 bits (ObjectGuid.h: high, entry, counter), the number the world
// export lists as the spawn's guid.
func SpawnGUID(g packet.GUID) uint64 { return uint64(g) & 0x00FFFFFF }

// SeenMembers picks, from the units a read shows, the members of the pack: a creature whose spawn guid and entry both match a member,
// dead or alive (the caller tells them apart). The order is by guid.
func (p GMClearPack) SeenMembers(units []objects.Unit) []objects.Unit {
	var out []objects.Unit
	for _, u := range units {
		if !u.GUID.IsCreature() {
			continue
		}
		if e, ok := p.Members[SpawnGUID(u.GUID)]; ok && e == u.GUID.Entry() {
			out = append(out, u)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].GUID < out[j].GUID })
	return out
}

// LiveMembers is SeenMembers without the dead: a corpse needs no second kill.
func (p GMClearPack) LiveMembers(units []objects.Unit) []objects.Unit {
	var out []objects.Unit
	for _, u := range p.SeenMembers(units) {
		if !u.Dead {
			out = append(out, u)
		}
	}
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

// GMClearCounts is how many members are confirmed dead (killed by a line, Tries above 0) and how many are left (not seen, or alive).
// A member that was already dead when first seen is in neither count.
func GMClearCounts(es []GMClearEntry) (killed, left int) {
	for _, e := range es {
		switch {
		case !e.Killed:
			left++
		case e.Tries > 0:
			killed++
		}
	}
	return killed, left
}

// GMClearPartial says a member of an expected pack was not confirmed dead: the clear is PARTIAL.
func GMClearPartial(es []GMClearEntry) bool {
	_, left := GMClearCounts(es)
	return left > 0
}

// GMClearUncleared is the "<leg>:<group>" labels of the packs with a member left (alive or not seen), sorted and with a
// leg's consecutive numeric groups compressed into a range ("high_priestess_arlokk:12-17"). It is what the ASSISTED
// line and the walk's WARN name: a group that is still alive after its retries never fails the placement, it is listed.
func GMClearUncleared(es []GMClearEntry) []string {
	byLeg := map[string]map[string]bool{}
	for _, e := range es {
		if e.Killed {
			continue
		}
		if byLeg[e.Leg] == nil {
			byLeg[e.Leg] = map[string]bool{}
		}
		byLeg[e.Leg][e.Group] = true
	}
	legs := make([]string, 0, len(byLeg))
	for leg := range byLeg {
		legs = append(legs, leg)
	}
	sort.Strings(legs)
	var out []string
	for _, leg := range legs {
		var nums []int
		var words []string
		for g := range byLeg[leg] {
			if n, err := strconv.Atoi(g); err == nil {
				nums = append(nums, n)
				continue
			}
			words = append(words, g)
		}
		sort.Ints(nums)
		sort.Strings(words)
		var parts []string
		for i := 0; i < len(nums); {
			j := i
			for j+1 < len(nums) && nums[j+1] == nums[j]+1 {
				j++
			}
			if j > i {
				parts = append(parts, fmt.Sprintf("%d-%d", nums[i], nums[j]))
			} else {
				parts = append(parts, strconv.Itoa(nums[i]))
			}
			i = j + 1
		}
		parts = append(parts, words...)
		out = append(out, leg+":"+strings.Join(parts, ","))
	}
	return out
}

// GMClearSummaryLine is the one ASSISTED line of summary.txt and the report; a PARTIAL clear says how many members are left and which
// groups were not cleared. A member the tool's budget left untried is counted apart from the ones its lines could not kill.
func GMClearSummaryLine(es []GMClearEntry, legs []string) string {
	killed, left := GMClearCounts(es)
	line := fmt.Sprintf("ASSISTED: --gm-clear killed %d units by GM command (%s); this is not a real clear", killed, strings.Join(legs, ", "))
	if left > 0 {
		var notSeen, alive, untried int
		for _, e := range es {
			if e.Killed {
				continue
			}
			switch e.Reason {
			case GMClearNotSeen:
				notSeen++
			case GMClearGaveUp:
				untried++
			default:
				alive++
			}
		}
		line += fmt.Sprintf("; PARTIAL: %d members left (%d not seen, %d alive", left, notSeen, alive)
		if untried > 0 {
			line += fmt.Sprintf(", %d not tried", untried)
		}
		line += ")"
		if groups := GMClearUncleared(es); len(groups) > 0 {
			line += "; groups not cleared: " + strings.Join(groups, ", ")
		}
	}
	return line
}
