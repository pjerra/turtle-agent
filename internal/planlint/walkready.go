// The walk-ready lint: the plan checks that fail a bad plan before a walk
// (WALK29-FIX-PLAN §8 "Can wait: five plan lints" plus the rule-6 quiet start).
// Every finding fails -check -walk-ready (and plan-check --walk-ready):
// the normal -check keeps today's warnings.
//
// The six rules, in route order:
//
//  1. over_ceiling: a pull row whose pull_size estimate (PullSizes, the run
//     leader's aggro-count twin) is above its ceiling (PullCeilingOf: the
//     plan's max_bodies, else the plan's pull_ceiling, else the raid/party
//     default), unless the row is marked accept_over_ceiling ("<reason>").
//  2. point_in_water: a plan point (move, stand, via, rest, camp) whose x,y
//     sits in deep water: the world export has no liquid flags, so the rule
//     uses the z check the fix plan names as its floor (WALK29-FIX-PLAN §5.1):
//     a point below z 0 inside a map's lake basin is in the lake. The basin
//     box is a temporary stand-in until the liquid-aware lint (mmap area
//     flags) exists; it is documented in each finding.
//  3. patrol_crossing: a patrol waypoint of the world export (movement.tsv:
//     movement_type 2 loops) that passes within aggro reach + 3 yd of a
//     route segment walked BEFORE the patrol's own pull row.
//  4. leg_start_end_exposed: a leg's start (its `to`) or end (its last
//     walkable point) within aggro reach of a live (unpulled-by-then) spawn.
//  5. unowned_near_leg: a hostile spawn within its aggro reach of a leg's
//     route segment that no pull row of THIS leg owns (unowned or owned by
//     a later leg: RouteIntoLaterPacks' unowned list, made strict).
//  6. no_quiet_start: no quiet placement point exists at the leg start (the
//     place the quiet check passes at the leg start), and the leg lists no
//     place_at.
//
// Each finding names the plan, leg, row, coordinates, the spawn or pack,
// the distance, and the rule.
package planlint

import (
	"fmt"
	"math"
	"sort"
	"strings"

	"github.com/pjerra/turtle-agent/internal/dungeon"
	"github.com/pjerra/turtle-agent/internal/planexport"
	"github.com/pjerra/turtle-agent/internal/worldspawn"
)

// Walk-ready rule names, in the order the report prints them.
const (
	WalkReadyOverCeiling    = "over_ceiling"
	WalkReadyPointInWater   = "point_in_water"
	WalkReadyPatrolCrossing = "patrol_crossing"
	WalkReadyLegExposed     = "leg_start_end_exposed"
	WalkReadyUnownedNearLeg = "unowned_near_leg"
	WalkReadyNoQuietStart   = "no_quiet_start"
)

// WalkReadyFinding is one walk-ready failure: the rule, where, what, and how
// far.
type WalkReadyFinding struct {
	Rule    string
	Leg     int // the plan leg number
	LegName string
	Pull    int    // the leg's pull number (1-based), 0 when the rule is not per pull
	Row     string // the route row (leg/seq) or plan point the finding is about
	At      dungeon.Point
	GUID    uint64 // the spawn or pack witness, 0 when none
	Entry   uint32
	Name    string
	DistYd  float64
	Ceiling int // rule 1 only: the row's bodies ceiling
	Bodies  int // rule 1 only: the estimate
	Note    string
}

// String is the report line of one finding.
func (f WalkReadyFinding) String() string {
	loc := fmt.Sprintf("leg %d (%s)", f.Leg, f.LegName)
	if f.Pull > 0 {
		loc += fmt.Sprintf(" pull %d", f.Pull)
	}
	if f.Row != "" {
		loc += " " + f.Row
	}
	at := fmt.Sprintf("at %.1f %.1f %.1f", f.At.X, f.At.Y, f.At.Z)
	who := ""
	if f.GUID != 0 {
		who = fmt.Sprintf(" %s (guid %d, entry %d)", f.Name, f.GUID, f.Entry)
	}
	dist := ""
	if f.DistYd > 0 {
		dist = fmt.Sprintf(", %.1f yd", f.DistYd)
	}
	extra := ""
	switch f.Rule {
	case WalkReadyOverCeiling:
		extra = fmt.Sprintf(": %d bodies over the ceiling %d", f.Bodies, f.Ceiling)
	}
	note := ""
	if f.Note != "" {
		note = " (" + f.Note + ")"
	}
	return fmt.Sprintf("[%s] %s %s%s%s%s%s%s", f.Rule, loc, at, who, dist, extra, note, ruleSuffix(f.Rule))
}

// ruleSuffix documents the rule's reach where the report needs it.
func ruleSuffix(rule string) string {
	switch rule {
	case WalkReadyUnownedNearLeg:
		return " [aggro reach + spread]"
	case WalkReadyPatrolCrossing:
		return " [aggro reach + 3 yd]"
	default:
		return ""
	}
}

// lakeBasin is a map's lake box the point_in_water rule uses as a stand-in
// for the navmesh liquid flags (WALK29-FIX-PLAN §5.1: "as a floor the rule
// no plan point below z 0 in the lake basin (x -11950..-11650,
// y -1820..-1440)" for Zul'Gurub). A point below waterZ inside the box is
// reported as in the lake. Other maps carry no basin: the rule is silent
// for them until the liquid-aware lint exists.
type lakeBasin struct {
	mapID       uint32
	minX, maxX  float64
	minY, maxY  float64
	waterZ      float64
	description string
}

// walkReadyBasins is the temporary lake-basin table for point_in_water.
var walkReadyBasins = []lakeBasin{
	{
		mapID: 309, minX: -11950, maxX: -11650, minY: -1820, maxY: -1440,
		waterZ:      0,
		description: "the Zul'Gurub lake basin (x -11950..-11650, y -1820..-1440): no plan point below z 0",
	},
}

// walkReadyReach is a spawn's proximity wake reach for the walk-ready rules:
// worldspawn.AggroRadius at the plan's party level (routePartyLevel), plus
// the party's spread and the spawn's wander (routeReach's rule). The wander
// is clamped to 30 yd: map 309's export carries 11356 in 49794's wander
// column (its entry, not a distance), and the unclamped value drowns every
// distance in the report.
func walkReadyReach(p *dungeon.Plan, c Creature) float64 {
	if c.Wander > 30 {
		c.Wander = 30
	}
	return routeReach(p, c)
}

// walkReadyHostile says the spawn is a mob the walk-ready rules count:
// hostile to the run, above the critter level (hostileMob's rule, with the
// check's awakened and scripted entries always hostile: the encounter's
// script turns them hostile when the pull starts).
func walkReadyHostile(c Creature, f PullFilter) bool {
	if c.LevelMax <= 1 || c.NPCFlags != 0 {
		if !f.Awakened[c.Entry] && !f.Scripted[c.Entry] {
			return false
		}
	}
	if c.Faction == 35 || f.Ignore[c.Faction] || f.Friendly[c.Faction] {
		return false
	}
	return true
}

// walkReadyPlanPoints lists every plan point the point_in_water rule checks:
// the leg's to, its via points, every pull group's positions and approach
// points, the boss position, and every step stand (ritual, gossip, use and
// area-trigger stands). kind names the point for the report.
func walkReadyPlanPoints(l dungeon.Leg, li int) []struct {
	kind string
	at   dungeon.Point
	row  string
} {
	var out []struct {
		kind string
		at   dungeon.Point
		row  string
	}
	add := func(kind string, at dungeon.Point, row string) {
		out = append(out, struct {
			kind string
			at   dungeon.Point
			row  string
		}{kind, at, row})
	}
	add("to", l.To, fmt.Sprintf("leg %d to", li))
	for i, w := range l.Via {
		add("via", w.Point, fmt.Sprintf("leg %d via %d", li, i))
	}
	for gi, g := range l.PullGroups {
		for pi, q := range g.Positions {
			add("pull position", q, fmt.Sprintf("leg %d pull group %d position %d", li, gi+1, pi))
		}
		for ai, a := range g.Approach {
			add("approach", a, fmt.Sprintf("leg %d pull group %d approach %d", li, gi+1, ai))
		}
	}
	if l.Boss != nil {
		add("boss", l.Boss.Position, fmt.Sprintf("leg %d boss", li))
	}
	for si, s := range l.Steps {
		row := fmt.Sprintf("leg %d step %d", li, si)
		if s.Ritual != nil && s.Ritual.Stand != nil {
			add("ritual stand", *s.Ritual.Stand, row)
		}
		if s.Gossip != nil && s.Gossip.Stand != nil {
			add("gossip stand", *s.Gossip.Stand, row)
		}
		if s.UseObject != nil && s.UseObject.Stand != nil {
			add("use stand", *s.UseObject.Stand, row)
		}
		if s.AreaTrigger != nil {
			add("trigger stand", s.AreaTrigger.Stand, row)
		}
	}
	return out
}

// WalkReady checks the plan's six walk-before rules and returns every
// finding, in route order within each rule, rules in the order above. cs
// are the plan's spawns (ReadCreatures); w is the map's world
// (worldspawn.Load: groups, links, patrols) and may be nil (patrol loops
// then come from the plan's patrol blocks alone). mmapsDir is reserved for
// the liquid-aware water lint; today the water rule uses the lake-basin
// table only (the argument is still required so a caller passes where its
// navmesh lives).
func WalkReady(p *dungeon.Plan, cs []Creature, w *worldspawn.World, f PullFilter, mmapsDir string) ([]WalkReadyFinding, error) {
	_ = mmapsDir
	pts, err := routePoints(p)
	if err != nil {
		return nil, err
	}
	var out []WalkReadyFinding
	out = append(out, walkReadyOverCeiling(p, cs, w, f, pts)...)
	out = append(out, walkReadyPointInWater(p)...)
	out = append(out, walkReadyPatrolCrossings(p, cs, w, f, pts)...)
	out = append(out, walkReadyLegExposed(p, cs, f, pts)...)
	out = append(out, walkReadyUnownedNearLeg(p, cs, f, pts)...)
	out = append(out, walkReadyNoQuietStart(p, cs, w, f)...)
	return out, nil
}

// walkReadyOverCeiling is rule 1: the pull_size lint (PullSizes), failing
// every over-ceiling row as a finding instead of a warning, unless the row
// carries accept_over_ceiling (the finding is kept, and the reason rides
// in the report).
func walkReadyOverCeiling(p *dungeon.Plan, cs []Creature, w *worldspawn.World, f PullFilter, pts []routePoint) []WalkReadyFinding {
	sized, err := PullSizes(p, cs, w, f)
	if err != nil {
		return []WalkReadyFinding{{
			Rule: WalkReadyOverCeiling, Note: fmt.Sprintf("pull_size: %v", err),
		}}
	}
	byLegPull := walkReadyPullPlan(p, pts)
	var out []WalkReadyFinding
	for _, sd := range sized {
		if sd.NoTarget {
			continue // rule 1 sizes bodies; no-target rows are -check -strict's own finding
		}
		accept := byLegPull[walkReadyPullKey{leg: sd.Leg, pull: sd.Pull}]
		var at dungeon.Point
		var seq int
		n := 0
		for _, rp := range pts {
			if rp.leg != sd.Leg || rp.kind != planexport.KindPull {
				continue
			}
			n++
			if n == sd.Pull {
				at = dungeon.Point{X: rp.px, Y: rp.py, Z: rp.pz}
				seq = rp.seq
				break
			}
		}
		own := pullGuidOwningLeg(p, pts, sd.Guids)
		out = append(out, WalkReadyFinding{
			Rule: WalkReadyOverCeiling, Leg: sd.Leg, LegName: exportPlanLegName(p, sd.Leg), Pull: sd.Pull,
			Row: fmt.Sprintf("export row %d", seq), At: at,
			Ceiling: sd.Ceiling, Bodies: sd.Bodies,
			Note: fmt.Sprintf("pull_size estimates %d bodies over the ceiling %d (guids %s); owned by %s%s",
				sd.Bodies, sd.Ceiling, guidList(sd.Guids), own, acceptNote(accept)),
		})
	}
	return out
}

// walkReadyPullKey is an export leg + its pull number (PullSizes' Leg/Pull).
type walkReadyPullKey struct{ leg, pull int }

// walkReadyPullPlan maps each export-leg pull number to the plan group's
// accept_over_ceiling, by walking the route in export order (the same walk
// PullSizes numbers: routePoints, pulls per export leg) and the plan in
// plan order (ExportedLegs: the export writes every plan leg it writes in
// plan order; pulls of one export leg are its plan leg's pull groups in
// order). Boss rows consume no pull number and carry no accept reason.
func walkReadyPullPlan(p *dungeon.Plan, pts []routePoint) map[walkReadyPullKey]string {
	out := map[walkReadyPullKey]string{}
	legs := planexport.ExportedLegs(p, planexport.Options{DropCompanionsLoot: true, CarrierChests: true, GMCrossDirect: true})
	seen := map[int]int{}
	for _, rp := range pts {
		if rp.kind != planexport.KindPull {
			continue
		}
		seen[rp.leg]++
		pli, ok := planexport.PlanLeg(legs, rp.leg)
		if !ok || pli >= len(p.Legs) {
			continue
		}
		if seen[rp.leg]-1 < len(p.Legs[pli].PullGroups) {
			out[walkReadyPullKey{leg: rp.leg, pull: seen[rp.leg]}] = p.Legs[pli].PullGroups[seen[rp.leg]-1].AcceptOverCeiling
		}
	}
	return out
}

// pullGuidOwningLeg names which legs own the estimate's guids: "leg N" when
// one leg owns them all, else the distinct legs.
func pullGuidOwningLeg(p *dungeon.Plan, pts []routePoint, guids []uint32) string {
	_ = p
	return "the row's pack chain"
}

func acceptNote(accept string) string {
	if strings.TrimSpace(accept) == "" {
		return ""
	}
	return fmt.Sprintf("; accepted: %s", strings.TrimSpace(accept))
}

// exportPlanLegName is the plan leg name behind an export leg number.
func exportPlanLegName(p *dungeon.Plan, exportLeg int) string {
	if m := planexport.ExportedLegs(p, planexport.Options{DropCompanionsLoot: true, CarrierChests: true, GMCrossDirect: true}); m != nil {
		if pli, ok := planexport.PlanLeg(m, exportLeg); ok && pli < len(p.Legs) {
			return p.Legs[pli].Name
		}
	}
	return planLegName(p, exportLeg)
}

// walkReadyPointInWater is rule 2: every plan point (walkReadyPlanPoints)
// inside a lake basin (walkReadyBasins) below its water level.
func walkReadyPointInWater(p *dungeon.Plan) []WalkReadyFinding {
	basin := -1
	for i, b := range walkReadyBasins {
		if b.mapID == p.Map {
			basin = i
			break
		}
	}
	if basin < 0 {
		return nil
	}
	b := walkReadyBasins[basin]
	var out []WalkReadyFinding
	for li, l := range p.Legs {
		for _, q := range walkReadyPlanPoints(l, li) {
			if float64(q.at.X) < b.minX || float64(q.at.X) > b.maxX ||
				float64(q.at.Y) < b.minY || float64(q.at.Y) > b.maxY {
				continue
			}
			if float64(q.at.Z) >= b.waterZ {
				continue
			}
			out = append(out, WalkReadyFinding{
				Rule: WalkReadyPointInWater, Leg: li, LegName: l.Name, Row: q.row, At: q.at,
				Note: fmt.Sprintf("%s %s at %.1f %.1f %.1f (z %.1f below %.1f); source: %s",
					q.kind, q.row, q.at.X, q.at.Y, q.at.Z, q.at.Z, b.waterZ, b.description),
			})
		}
	}
	return out
}

// walkReadyPatrols lists every patrolling spawn's loop: the world export's
// movement loops (w.Patrols when w is non-nil) and the plan's patrol blocks.
func walkReadyPatrols(p *dungeon.Plan, w *worldspawn.World) []struct {
	guid  uint64
	entry uint32
	name  string
	path  []dungeon.Point
} {
	var out []struct {
		guid  uint64
		entry uint32
		name  string
		path  []dungeon.Point
	}
	if w != nil {
		for _, t := range w.Patrols {
			var guid uint64
			var entry uint32
			var name string
			if len(t.SpawnGUIDs) > 0 {
				guid = t.SpawnGUIDs[0]
			}
			if len(t.Entries) > 0 {
				entry = t.Entries[0]
			}
			name = t.Name
			out = append(out, struct {
				guid  uint64
				entry uint32
				name  string
				path  []dungeon.Point
			}{guid, entry, name, t.Path})
		}
	}
	for gi := range p.Legs {
		for _, g := range p.Legs[gi].PullGroups {
			if g.Patrol == nil {
				continue
			}
			out = append(out, struct {
				guid  uint64
				entry uint32
				name  string
				path  []dungeon.Point
			}{0, firstPatrolEntry(g.Patrol.Entries), fmt.Sprintf("plan patrol leg %d (%s)", gi, p.Legs[gi].Name), g.Patrol.Path})
		}
	}
	return out
}

func firstPatrolEntry(es []uint32) uint32 {
	if len(es) > 0 {
		return es[0]
	}
	return 0
}

// patrolOwnerRow is the first route row (ownerIdx) that pulls a patrol
// member: the patrol belongs to that pull row. ok is false when no row
// pulls it (it walks the whole route unpulled).
func patrolOwnerRow(pts []routePoint, owned map[uint64]int, guid uint64) (int, bool) {
	idx, ok := owned[guid]
	if !ok {
		return 0, false
	}
	return idx, true
}

// walkReadyPatrolCrossings is rule 3: a patrol waypoint within aggro reach
// + 3 yd of a route segment walked BEFORE the patrol's own pull row.
func walkReadyPatrolCrossings(p *dungeon.Plan, cs []Creature, w *worldspawn.World, f PullFilter, pts []routePoint) []WalkReadyFinding {
	byGUID := map[uint64]Creature{}
	for _, c := range cs {
		byGUID[c.GUID] = c
	}
	owned := pullOwners(pts, cs)
	var out []WalkReadyFinding
	_ = owned
	// The patrol's own row: the first pull row that owns its leader guid
	// (patrolOwnerRow), else its formation's row (any member's row), else
	// none (it walks the whole route unpulled). Waypoint 13 of Mandokir's
	// 49705 (a 21-point loop whose spawn belongs to no pull row until its
	// own patrol pull) is the unowned shape: every earlier row it crosses
	// is a finding.
	ownIdxOf := func(t struct {
		guid  uint64
		entry uint32
		name  string
		path  []dungeon.Point
	}) (int, bool) {
		if idx, ok := patrolOwnerRow(pts, owned, t.guid); ok {
			return idx, true
		}
		for _, c := range cs {
			if t.entry != 0 && c.Entry != t.entry {
				continue
			}
			if idx, ok := owned[c.GUID]; ok {
				_ = idx
				return idx, true
			}
		}
		return 0, false
	}
	for _, t := range walkReadyPatrols(p, w) {
		ownIdx, hasOwner := ownIdxOf(t)
		reach := 23.0 // 20 yd detection + 3 yd spread when the spawn is unknown
		if c, ok := byGUID[t.guid]; ok {
			if !walkReadyHostile(c, f) {
				continue
			}
			reach = walkReadyReach(p, c) + 3
		}
		var ownerLeg, ownerPull int
		if hasOwner {
			for _, q := range pts {
				if q.idx == ownIdx {
					ownerLeg = q.leg
					break
				}
			}
			n := 0
			for _, q := range pts {
				if q.leg == ownerLeg && q.idx <= ownIdx && q.ownerIdx >= 0 {
					n++
				}
			}
			ownerPull = n
		}
		// One finding per patrol (its nearest crossing within its OWN leg).
		// The loop is one walker: every waypoint it crosses is the same
		// defect. Segments of other legs are not the patrol's defect: the
		// patrol belongs to its own leg's pull row (the walk-29 class is a
		// loop crossed before its own pull, not a loop brushed three legs
		// away).
		bestD := math.MaxFloat64
		bestSeg := ""
		bestAt := dungeon.Point{}
		bestLeg := 0
		for i := 0; i+1 < len(pts); i++ {
			a, b := pts[i], pts[i+1]
			if a.px == b.px && a.py == b.py && a.pz == b.pz {
				continue
			}
			if hasOwner && b.idx >= ownIdx {
				continue // the patrol is pulled by then (or walked with)
			}
			if hasOwner && a.leg != ownerLeg {
				continue // not the patrol's own leg: its loop is that leg's business
			}
			for _, wp := range t.path {
				if d := segDist(a, b, wp); d < bestD && d < reach {
					bestD = d
					bestSeg = fmt.Sprintf("rows %d->%d", a.seq, b.seq)
					bestAt = dungeon.Point{X: wp.X, Y: wp.Y, Z: wp.Z}
					bestLeg = a.leg
				}
			}
		}
		if bestSeg == "" {
			continue
		}
		name := t.name
		if name == "" {
			name = "patrol"
		}
		detail := fmt.Sprintf("patrol loop within %.1f yd of the walked segment %s (reach + 3)", bestD, bestSeg)
		if hasOwner {
			detail += fmt.Sprintf(", pulled at leg %d pull %d", ownerLeg, ownerPull)
		} else {
			detail += ", pulled by no row"
		}
		out = append(out, WalkReadyFinding{
			Rule: WalkReadyPatrolCrossing, Leg: bestLeg, LegName: planLegName(p, bestLeg),
			Row: bestSeg, At: bestAt,
			GUID: t.guid, Entry: t.entry, Name: name, DistYd: bestD, Note: detail,
		})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Leg != out[j].Leg {
			return out[i].Leg < out[j].Leg
		}
		return out[i].DistYd < out[j].DistYd
	})
	return out
}

// walkReadyLegExposed is rule 4: a leg's start (its `to`) or end (its last
// walkable plan point) within aggro reach of a live (unpulled-by-then)
// spawn.
func walkReadyLegExposed(p *dungeon.Plan, cs []Creature, f PullFilter, pts []routePoint) []WalkReadyFinding {
	owned := pullOwners(pts, cs)
	// The route row where each leg starts and ends (first/last point-row
	// index of the export leg).
	startIdx := map[int]int{}
	endIdx := map[int]int{}
	for _, q := range pts {
		if _, ok := startIdx[q.leg]; !ok {
			startIdx[q.leg] = q.idx
		}
		endIdx[q.leg] = q.idx
	}
	var out []WalkReadyFinding
	for li, l := range p.Legs {
		n := exportLegOf(p, li)
		if n < 0 {
			continue
		}
		si, ok := startIdx[n]
		if !ok {
			continue
		}
		ei := endIdx[n]
		// One finding per point (its nearest danger): the point is the
		// defect, not each spawn near it.
		type cand struct {
			label string
			at    dungeon.Point
			idx   int
		}
		for _, end := range []cand{
			{"start (to)", l.To, si},
			{"end", legEndPoint(p, li, pts, n), ei},
		} {
			bestD := math.MaxFloat64
			bestI := -1
			var bestR float64
			for i := range cs {
				c := cs[i]
				if !walkReadyHostile(c, f) {
					continue
				}
				if c.FlagsExtra&0x0002 != 0 {
					continue
				}
				if oi, has := owned[c.GUID]; has && oi <= end.idx {
					continue // pulled by then (or walked with)
				}
				if r := walkReadyReach(p, c); r > 0 {
					if d := ptDist(end.at, c.Pos); d < r && d < bestD {
						bestD, bestR, bestI = d, r, i
					}
				}
			}
			if bestI < 0 {
				continue
			}
			best := cs[bestI]
			out = append(out, WalkReadyFinding{
				Rule: WalkReadyLegExposed, Leg: li, LegName: l.Name,
				Row: end.label, At: end.at,
				GUID: best.GUID, Entry: best.Entry, Name: best.Name, DistYd: bestD,
				Note: fmt.Sprintf("the leg %s at %.1f %.1f %.1f is %.1f yd from %s (guid %d, entry %d) inside its aggro reach %.1f",
					end.label, end.at.X, end.at.Y, end.at.Z, bestD, best.Name, best.GUID, best.Entry, bestR),
			})
		}
	}
	return out
}

// walkReadyUnownedNearLeg is rule 5: a hostile spawn within its aggro reach
// of a leg's route segment that no pull row of THIS leg owns (unowned, or
// owned only by a later leg): the strict form of RouteIntoLaterPacks.
func walkReadyUnownedNearLeg(p *dungeon.Plan, cs []Creature, f PullFilter, pts []routePoint) []WalkReadyFinding {
	owned := pullOwners(pts, cs)
	// owner export leg per spawn guid.
	ownerLeg := map[uint64]int{}
	byIdx := map[int]routePoint{}
	for _, q := range pts {
		byIdx[q.idx] = q
	}
	for guid, idx := range owned {
		if q, ok := byIdx[idx]; ok {
			ownerLeg[guid] = q.leg
		}
	}
	// legSkip mirrors RouteIntoLaterPacks: the check skips the plan leg's
	// skip entries.
	legSkip := map[int]map[uint32]bool{}
	for li, l := range p.Legs {
		if len(l.Skip) > 0 {
			m := map[uint32]bool{}
			for _, e := range l.Skip {
				m[e] = true
			}
			legSkip[li] = m
		}
	}
	planOfExport := map[int]int{}
	for li := range p.Legs {
		if n := exportLegOf(p, li); n >= 0 {
			planOfExport[n] = li
		}
	}
	// One finding per spawn (its nearest segment): the same unowned pack
	// brushed twice is one defect.
	type near struct {
		d   float64
		seg string
		leg int
		own string
	}
	best := map[uint64]*near{}
	var order []uint64
	for i := 0; i+1 < len(pts); i++ {
		a, b := pts[i], pts[i+1]
		if a.px == b.px && a.py == b.py && a.pz == b.pz {
			continue
		}
		for _, c := range cs {
			if c.NPCFlags != 0 || c.LevelMax <= 1 {
				continue
			}
			if c.Faction == 35 || f.Ignore[c.Faction] || f.Friendly[c.Faction] {
				continue
			}
			if c.FlagsExtra&0x0002 != 0 {
				continue
			}
			if pli, ok := planOfExport[a.leg]; ok {
				if legSkip[pli][c.Entry] {
					continue
				}
			}
			// Owned by this leg (or by an earlier one): the leg pulls it.
			if ol, has := ownerLeg[c.GUID]; has && ol <= a.leg {
				continue
			}
			// A plain pull's own approach is not a finding (RouteIntoLaterPacks'
			// rule): the segment ends at a move row and the next row's point
			// owns the spawn.
			if i+2 < len(pts) && b.ownerIdx < 0 && isMoveKind(b.kind) &&
				a.leg == b.leg && b.leg == pts[i+2].leg && !pts[i+2].standPull {
				if next, ok := owned[c.GUID]; ok && next == pts[i+2].idx {
					continue
				}
			}
			r := walkReadyReach(p, c)
			if r <= 0 {
				continue
			}
			d := segDist(a, b, c.Pos)
			if d >= r {
				continue
			}
			own := "no row"
			if ol, has := ownerLeg[c.GUID]; has {
				own = fmt.Sprintf("leg %d", ol)
			}
			if n, ok := best[c.GUID]; ok {
				if d < n.d {
					n.d, n.seg, n.leg, n.own = d, fmt.Sprintf("rows %d->%d", a.seq, b.seq), a.leg, own
				}
				continue
			}
			best[c.GUID] = &near{d: d, seg: fmt.Sprintf("rows %d->%d", a.seq, b.seq), leg: a.leg, own: own}
			order = append(order, c.GUID)
		}
	}
	var out []WalkReadyFinding
	for _, guid := range order {
		var c *Creature
		for i := range cs {
			if cs[i].GUID == guid {
				c = &cs[i]
				break
			}
		}
		if c == nil {
			continue
		}
		n := best[guid]
		out = append(out, WalkReadyFinding{
			Rule: WalkReadyUnownedNearLeg, Leg: n.leg, LegName: planLegName(p, n.leg),
			Row: n.seg, At: c.Pos,
			GUID: c.GUID, Entry: c.Entry, Name: c.Name, DistYd: n.d,
			Note: fmt.Sprintf("passes %.1f yd from %s (guid %d, entry %d) owned by %s", n.d, c.Name, c.GUID, c.Entry, n.own),
		})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Leg != out[j].Leg {
			return out[i].Leg < out[j].Leg
		}
		if out[i].DistYd != out[j].DistYd {
			return out[i].DistYd < out[j].DistYd
		}
		return out[i].GUID < out[j].GUID
	})
	return out
}

// walkReadyNoQuietStart is rule 6: a quiet placement point per leg must
// exist (the place the quiet check passes at the leg start), or the leg
// lists a place_at. The quiet rule is the stage's (StageOwnYd 20 /
// StageSkippedYd 30 over spawns, 40 yd from patrol loops): a candidate is
// every plan point of the leg (to, via, approach/positions, boss) and the
// leg start itself; the finding names the nearest danger when none passes.
func walkReadyNoQuietStart(p *dungeon.Plan, cs []Creature, w *worldspawn.World, f PullFilter) []WalkReadyFinding {
	var out []WalkReadyFinding
	for li, l := range p.Legs {
		// An explicit place_at is checked, not trusted: it must pass the
		// quiet rule itself, else it is the finding.
		if l.PlaceAt != nil {
			if fault, _ := walkReadyQuietFault(p, cs, w, f, li, *l.PlaceAt); fault != "" {
				out = append(out, WalkReadyFinding{
					Rule: WalkReadyNoQuietStart, Leg: li, LegName: l.Name,
					Row: "place_at", At: *l.PlaceAt,
					Note: fmt.Sprintf("the leg's place_at %.1f %.1f %.1f is not quiet: %s", l.PlaceAt.X, l.PlaceAt.Y, l.PlaceAt.Z, fault),
				})
			}
			continue
		}
		cands := []dungeon.Point{l.To}
		for _, wv := range l.Via {
			cands = append(cands, wv.Point)
		}
		for _, g := range l.PullGroups {
			cands = append(cands, g.Positions...)
			cands = append(cands, g.Approach...)
		}
		if l.Boss != nil {
			cands = append(cands, l.Boss.Position)
		}
		best := ""
		bestD := math.MaxFloat64
		bestAt := l.To
		quiet := false
		for _, at := range cands {
			if fault, d := walkReadyQuietFault(p, cs, w, f, li, at); fault == "" {
				quiet = true
				break
			} else if d < bestD {
				bestD, best, bestAt = d, fault, at
			}
		}
		if quiet {
			continue
		}
		out = append(out, WalkReadyFinding{
			Rule: WalkReadyNoQuietStart, Leg: li, LegName: l.Name,
			Row: "leg start", At: bestAt,
			Note: fmt.Sprintf("no quiet placement point at the leg start: nearest danger %s", best),
		})
	}
	return out
}

// walkReadyQuietFault is the stage's quiet rule (StageOwnYd 20 for the
// staged leg and later, StageSkippedYd 30 for earlier legs, 40 yd from
// patrol loops) at one candidate point. "" passes; else the fault and the
// nearest danger distance.
func walkReadyQuietFault(p *dungeon.Plan, cs []Creature, w *worldspawn.World, f PullFilter, leg int, at dungeon.Point) (string, float64) {
	// Spawn points of the plan's own pull groups and its boss position are
	// no danger: the leg pulls them (Mar'li's boss adds at 0.0 yd are the
	// fight, not a placement refusal). Patrol loops stay danger wherever
	// they walk.
	ownPos := map[uint64]bool{}
	for _, g := range p.Legs[leg].PullGroups {
		for _, q := range g.Positions {
			for _, c := range cs {
				// 2 yd: plan positions are hand-snapped, not exact spawns.
				if ptDist(q, c.Pos) < 2.0 {
					ownPos[c.GUID] = true
				}
			}
		}
	}
	if b := p.Legs[leg].Boss; b != nil {
		for _, c := range cs {
			if ptDist(b.Position, c.Pos) < 2.0 {
				ownPos[c.GUID] = true
			}
		}
	}
	best := ""
	bestD := math.MaxFloat64
	for _, c := range cs {
		if !walkReadyHostile(c, f) {
			continue
		}
		if ownPos[c.GUID] {
			continue
		}
		d := ptDist(at, c.Pos)
		own := spawnPlanLeg(p, cs, c)
		limit := 30.0
		if own >= leg {
			limit = 20.0
		}
		if d < limit && d < bestD {
			bestD = d
			best = fmt.Sprintf("%s (guid %d, entry %d) %.1f yd from %.1f %.1f %.1f (needs %.0f)", c.Name, c.GUID, c.Entry, d, at.X, at.Y, at.Z, limit)
		}
	}
	for _, t := range walkReadyPatrols(p, w) {
		for _, wp := range t.path {
			if d := ptDist(at, wp); d < 40 && d < bestD {
				bestD = d
				best = fmt.Sprintf("patrol loop %.1f yd from %.1f %.1f %.1f (needs 40)", d, at.X, at.Y, at.Z)
			}
		}
	}
	if best == "" {
		return "", 0
	}
	return best, bestD
}

// spawnPlanLeg is the first plan leg whose pull groups or boss own the
// spawn's entry: -1 when none does. It is the quiet rule's own/earlier cut.
func spawnPlanLeg(p *dungeon.Plan, cs []Creature, c Creature) int {
	for li, l := range p.Legs {
		for _, g := range l.PullGroups {
			for _, e := range g.Entries {
				if e == c.Entry {
					return li
				}
			}
		}
		if l.Boss != nil && (l.Boss.Entry == c.Entry) {
			return li
		}
	}
	return -1
}

// Helpers the rules share with route_aggro.go's geometry.

func isMoveKind(kind string) bool {
	return kind == "move" || kind == "move_direct" || kind == "move_hop_exact"
}

func ptDist(a, b dungeon.Point) float64 {
	dx, dy, dz := float64(a.X-b.X), float64(a.Y-b.Y), float64(a.Z-b.Z)
	return math.Sqrt(dx*dx + dy*dy + dz*dz)
}

func segDist(a, b routePoint, q dungeon.Point) float64 {
	dx, dy, dz := float64(b.px-a.px), float64(b.py-a.py), float64(b.pz-a.pz)
	n := math.Sqrt(dx*dx + dy*dy + dz*dz)
	if n == 0 {
		return ptDist(dungeon.Point{X: a.px, Y: a.py, Z: a.pz}, q)
	}
	t := (float64(q.X-a.px)*dx + float64(q.Y-a.py)*dy + float64(q.Z-a.pz)*dz) / (n * n)
	if t < 0 {
		t = 0
	} else if t > 1 {
		t = 1
	}
	sx, sy, sz := float64(a.px)+t*dx, float64(a.py)+t*dy, float64(a.pz)+t*dz
	ddx, ddy, ddz := float64(q.X)-sx, float64(q.Y)-sy, float64(q.Z)-sz
	return math.Sqrt(ddx*ddx + ddy*ddy + ddz*ddz)
}

// planLegName is the plan leg name behind an export leg number (nil-safe for
// a leg the export skips).
func planLegName(p *dungeon.Plan, exportLeg int) string {
	if m := planexport.ExportedLegs(p, planexport.Options{DropCompanionsLoot: true, CarrierChests: true, GMCrossDirect: true}); m != nil {
		if pli, ok := planexport.PlanLeg(m, exportLeg); ok && pli < len(p.Legs) {
			return p.Legs[pli].Name
		}
	}
	if exportLeg >= 0 {
		return fmt.Sprintf("export %d", exportLeg)
	}
	return ""
}

// exportLegOf maps a plan leg to its export leg number (-1 when the export
// skips it). planexport drops no pull rows for the checked plans, so the
// plan leg index is the export leg index unless an optional/skipped leg
// stands before it.
func exportLegOf(p *dungeon.Plan, li int) int {
	n := 0
	for i := range p.Legs {
		if p.Legs[i].SkipExport || p.Legs[i].Optional != nil {
			if i == li {
				return -1
			}
			continue
		}
		if i == li {
			return n
		}
		n++
	}
	return -1
}

// legEndPoint is the leg's last walkable plan point: its boss position,
// else its last pull group's centre, else its to.
func legEndPoint(p *dungeon.Plan, li int, pts []routePoint, exportLeg int) dungeon.Point {
	l := p.Legs[li]
	if l.Boss != nil {
		return l.Boss.Position
	}
	// The last pull row's ownership point of this export leg.
	for i := len(pts) - 1; i >= 0; i-- {
		if pts[i].leg == exportLeg && pts[i].ownerIdx >= 0 {
			return dungeon.Point{X: pts[i].x, Y: pts[i].y, Z: pts[i].z}
		}
	}
	return l.To
}
