// dungeongen -camp-search: measures candidate camp and tag points for a pull group (spec camp-pull-and-aggro-count 4.5). It is read-only:
// it prints the candidates with their distances and never edits the plan; the author copies the points they pick into the plan, and the
// lint (CampPullFindings, StageFindings) then checks them like any other.
//
// What a candidate is, per rule:
//
//	camp (grid, Step yd, on the navmesh floor with mmaps)
//	  1. at least dungeon.CasterCampYd from every caster spawn (Plan.CasterEntries) of the stage's own and the later stages' formations
//	     (an unstaged group: its own formations);
//	  3. clear of every spawn that is not the stage's own or dead: farther than its wake reach (aggro + raid spread + wander);
//	     and clear of the stage's own pack too (a camp inside its wake reach, aggro + raid spread, would pull it before the tank tags);
//	  5. on the cleared route: within 10 yd of an earlier row's point or of the segment between two earlier rows (CampPullFindings' rule);
//	tag spot (grid around the stage's units)
//	  4. the shot: at most campShotYd from the nearest peeled unit (with mmaps, a clear navmesh ray to it), and outside every peeled unit's
//	     tank wake reach + 1 yd (no body pull);
//	  2. one pack: the estimator from the tag spot (and, for a pair, from the camp walk, the fight at the camp and the drag back) returns
//	     exactly the stage's formations;
//	pair: the camp at most dungeon.MaxCampDragYd from the tag spot (and, with mmaps, a complete navmesh path of at most MaxCampPathYd).
//
// The pairs are ranked by drag length (the straight distance; the navmesh path length is printed with mmaps). Without mmaps there is no
// floor to snap to: a point takes the height of the nearest cleared-route sample (a camp) or peeled unit (a tag spot), the sight is
// "always sees", and nothing is verified against the navmesh; the output says so.
package main

import (
	"fmt"
	"math"
	"sort"
	"strings"

	"github.com/pjerra/turtle-agent/internal/aggroest"
	"github.com/pjerra/turtle-agent/internal/dungeon"
	"github.com/pjerra/turtle-agent/internal/navmesh"
	"github.com/pjerra/turtle-agent/internal/planexport"
	"github.com/pjerra/turtle-agent/internal/worldspawn"
)

const (
	// campSearchMargin is how far beyond the peeled units the camp grid reaches: the shot (23.5) plus the drag cap (50).
	campSearchMargin = campShotYd + dungeon.MaxCampDragYd + 2
	// campSearchMaxPairs bounds the pairs the full estimate is run on.
	campSearchMaxPairs = 20000
	// campRouteYd is how close the cleared route must pass to a camp (CampPullFindings' 10 yd).
	campRouteYd = 10
	// campFloorDz is how far a navmesh floor may lie from the reference height of a grid point.
	campFloorDz = 8
	// campSpacingYd keeps the listed candidates apart: a camp and a tag within this of a listed pair's are the same candidate.
	campSpacingYd = 5
)

// CampSearchOptions are the search's knobs.
type CampSearchOptions struct {
	Step    float64          // grid step in yards (default 1)
	Top     int              // how many pairs to list (default 5)
	Planner *navmesh.Planner // the map's navmesh (mmaps); nil: no floors, no rays, no paths
	Ignore  map[int]bool     // factions that are friendly (35)
	// PartyLevel replaces the plan's party level (level_band[0], the widest aggro radius) for this search only: the lint keeps judging the
	// plan's own, so a point found at another level may still fail it. 0: the plan's.
	PartyLevel int
}

// CampCandidate is one verified camp and tag spot pair.
type CampCandidate struct {
	Camp, Tag dungeon.Point
	DragYd    float64 // straight camp-to-tag distance
	PathYd    float64 // the navmesh path tag -> camp (0: unknown, no mmaps)
	CasterYd  float64 // the nearest caster spawn of the stage and later stages from the camp (-1: none)
	ClearYd   float64 // the margin by which the camp stays out of the nearest other pack's wake reach
	RouteYd   float64 // the distance from the camp to the cleared route
	PackYd    float64 // the camp's distance to the nearest peeled unit (outside every unit's wake reach)
	ShotYd    float64 // the tag spot's distance to the nearest peeled unit
	WakeYd    float64 // the margin by which the tag spot stays outside the nearest peeled unit's tank wake reach + 1
	Bodies    int     // the estimate at the tag spot with the camp walk and drag
}

// CampSearchResult is what a search found, with the counts of what it looked at.
type CampSearchResult struct {
	Plan               string
	Leg, Group, Stage  int // the arguments (leg 0-based plan leg, group 1-based, stage 1-based, 0: not staged)
	LegName            string
	Units              []creature
	Staged             bool
	Mode               string
	Camps, Tags, Pairs int // candidate camps, candidate tag spots, pairs the estimate ran on
	PairsTruncated     bool
	Candidates         []CampCandidate
	Casters            int // caster spawns the camp must keep away from
	PartyLevel         int
	PartyLevelOverride bool
	// Notes are findings about the pack itself, not about a grid point.
	Notes []string
	// Rejected counts, per rule, the grid points dropped by it (a point is counted at the first rule it fails).
	Rejected map[string]int
}

// campFloors snaps grid points to the navmesh.
type campFloors struct {
	pl  *navmesh.Planner
	mid uint32
}

// z gives the height of the walkable floor of (x, y) nearest zref within campFloorDz; without a planner, zref itself.
func (f campFloors) z(x, y, zref float32) (float32, bool) {
	if f.pl == nil {
		return zref, true
	}
	fl, err := f.pl.Floors(f.mid, x, y, zref-campFloorDz, zref+campFloorDz)
	if err != nil {
		return 0, false
	}
	best, found := float32(0), false
	for _, c := range fl {
		if c.Flags&navmesh.NavGround == 0 || c.Flags&(navmesh.NavMagma|navmesh.NavSlime|navmesh.NavSteep) != 0 {
			continue
		}
		if !found || abs32(c.Z-zref) < abs32(best-zref) {
			best, found = c.Z, true
		}
	}
	return best, found
}

func abs32(v float32) float32 {
	if v < 0 {
		return -v
	}
	return v
}

// CampSearch measures the camp and tag candidates of pull group `group` (1-based) of plan leg `leg` (0-based). For a staged group `stage`
// (1-based, 0 = 1) names the stage whose formations are peeled and the earlier stages are dead; for any other group the whole group is
// the pack and `stage` must be 0.
func CampSearch(p *dungeon.Plan, cs []creature, w *worldspawn.World, leg, group, stage int, opt CampSearchOptions) (*CampSearchResult, error) {
	if opt.PartyLevel > 0 {
		q := *p
		q.LevelBand = []int{opt.PartyLevel, opt.PartyLevel}
		p = &q
	}
	if opt.Step <= 0 {
		opt.Step = 1
	}
	if opt.Top <= 0 {
		opt.Top = 5
	}
	if opt.Ignore == nil {
		opt.Ignore = map[int]bool{35: true}
	}
	if leg < 0 || leg >= len(p.Legs) {
		return nil, fmt.Errorf("leg %d: the plan has legs 0-%d", leg, len(p.Legs)-1)
	}
	l := p.Legs[leg]
	if group < 1 || group > len(l.PullGroups) {
		return nil, fmt.Errorf("pull group %d: leg %d (%s) has pull groups 1-%d", group, leg, l.Name, len(l.PullGroups))
	}
	g := l.PullGroups[group-1]
	res := &CampSearchResult{Plan: p.Name, Leg: leg, Group: group, LegName: l.Name, Staged: len(g.Stages) > 0, Rejected: map[string]int{}}
	if res.Staged {
		if stage == 0 {
			stage = 1
		}
		if stage < 1 || stage > len(g.Stages) {
			return nil, fmt.Errorf("stage %d: pull group %d has stages 1-%d", stage, group, len(g.Stages))
		}
		res.Stage = stage
	} else if stage != 0 {
		return nil, fmt.Errorf("stage %d: pull group %d has no stages", stage, group)
	}
	eligible := func(c creature) bool { return c.NPCFlags == 0 && !opt.Ignore[c.Faction] && c.LevelMax > 1 }
	byGUID := map[uint64]creature{}
	byLeader := map[uint64][]creature{}
	for _, c := range cs {
		byGUID[c.GUID] = c
		byLeader[leaderOf(c)] = append(byLeader[leaderOf(c)], c)
	}

	// The stage's own spawns, the dead (earlier stages) and the later stages'.
	own, later := map[uint64]bool{}, map[uint64]bool{}
	dead := map[uint64]bool{}
	addFormations := func(into map[uint64]bool, leaders map[uint64]bool) {
		for lead := range leaders {
			for _, m := range byLeader[lead] {
				into[m.GUID] = true
			}
		}
	}
	if res.Staged {
		for si, st := range g.Stages {
			leaders := map[uint64]bool{}
			for _, id := range st.Peel {
				c, ok := byGUID[uint64(id)]
				if !ok {
					return nil, fmt.Errorf("stages[%d]: peel %d is no spawn of the world export", si, id)
				}
				leaders[leaderOf(c)] = true
			}
			switch {
			case si+1 == stage:
				addFormations(own, leaders)
			case si+1 < stage:
				addFormations(dead, leaders)
			default:
				addFormations(later, leaders)
			}
		}
	} else {
		addFormations(own, groupFormations(g, cs, eligible))
	}
	if len(own) == 0 {
		return nil, fmt.Errorf("pull group %d of leg %d (%s) holds no spawn: nothing to pull", group, leg, l.Name)
	}
	var units []creature
	for _, c := range cs {
		if own[c.GUID] {
			units = append(units, c)
		}
	}
	res.Units = units
	var casters []creature
	for _, c := range cs {
		if (own[c.GUID] || later[c.GUID]) && containsEntry(p.CasterEntries, c.Entry) {
			casters = append(casters, c)
		}
	}
	res.Casters = len(casters)

	// The cleared route: the rows before the group's chain, from the export.
	pts, err := routePoints(p, cs)
	if err != nil {
		return nil, err
	}
	owners := routeOwners(pts, cs)
	rowIdx := -1
	exportLeg, _ := planexport.LegNumber(p, leg)
	for _, q := range pts {
		if q.leg != exportLeg || q.ownerIdx < 0 {
			continue
		}
		if res.Staged {
			if g0, s0, _, ok := stageLabel(q.label); ok && q.campPull && g0 == group-1 && s0 == stage {
				rowIdx = q.idx
			}
		} else if o, ok := owners[units[0].GUID]; ok && o.idx == q.idx {
			rowIdx = q.idx
		}
	}
	if rowIdx < 0 {
		return nil, fmt.Errorf("pull group %d of leg %d (%s): no pull row of the export owns its spawns (is the leg optional or skipped?)", group, leg, l.Name)
	}
	upto := rowIdx
	prefix := fmt.Sprintf("pull group %d", group-1)
	for j := rowIdx - 1; j >= 0; j-- {
		if pts[j].leg != pts[rowIdx].leg || pts[j].kind != planexport.KindMove || !strings.HasPrefix(pts[j].label, prefix) {
			break
		}
		upto = j
	}
	// Cleared-route samples (rows' party points and 2 yd steps between consecutive rows), kept near the pack.
	var minX, maxX, minY, maxY = math.MaxFloat64, -math.MaxFloat64, math.MaxFloat64, -math.MaxFloat64
	for _, u := range units {
		minX, maxX = math.Min(minX, float64(u.Pos.X)), math.Max(maxX, float64(u.Pos.X))
		minY, maxY = math.Min(minY, float64(u.Pos.Y)), math.Max(maxY, float64(u.Pos.Y))
	}
	minX, maxX, minY, maxY = minX-campSearchMargin, maxX+campSearchMargin, minY-campSearchMargin, maxY+campSearchMargin
	inBox := func(q dungeon.Point) bool {
		return float64(q.X) >= minX && float64(q.X) <= maxX && float64(q.Y) >= minY && float64(q.Y) <= maxY
	}
	var route []dungeon.Point
	for j := 0; j < upto; j++ {
		q := dungeon.Point{X: pts[j].px, Y: pts[j].py, Z: pts[j].pz}
		if inBox(q) {
			route = append(route, q)
		}
		if j+1 < upto {
			r := dungeon.Point{X: pts[j+1].px, Y: pts[j+1].py, Z: pts[j+1].pz}
			n := int(math.Ceil(dist(q, r) / routeAggroStep))
			for s := 1; s < n; s++ {
				t := float64(s) / float64(n)
				m := dungeon.Point{X: q.X + float32(t)*(r.X-q.X), Y: q.Y + float32(t)*(r.Y-q.Y), Z: q.Z + float32(t)*(r.Z-q.Z)}
				if inBox(m) {
					route = append(route, m)
				}
			}
		}
	}
	if len(route) == 0 {
		return nil, fmt.Errorf("pull group %d of leg %d (%s): no cleared route within %.0f yd of the pack (rows before the group)", group, leg, l.Name, campSearchMargin)
	}

	floors := campFloors{pl: opt.Planner, mid: p.Map}
	res.PartyLevel = routePartyLevel(p)
	res.PartyLevelOverride = opt.PartyLevel > 0
	res.Mode = "no mmaps: heights come from the nearest cleared-route sample (camps) or peeled unit (tag spots); nothing is checked against the navmesh; sight is always"
	if opt.Planner != nil {
		res.Mode = "mmaps: points snapped to navmesh ground; the shot needs a clear navmesh ray; the drag needs a complete path of at most 70 yd; sight for the estimate is always"
	}

	// Mobs for the estimate: everything but the dead.
	ids := leaderPackIDs(cs)
	if w != nil {
		ids = packIDs(w)
	}
	allMobs, _ := pullMobs(p, cs, ids)
	var mobs []aggroest.Mob
	for _, m := range allMobs {
		if !dead[uint64(m.Guid)] {
			mobs = append(mobs, m)
		}
	}
	params := aggroest.Params{PartyLevel: routePartyLevel(p), Rate: routeAggroRate, RingYd: aggroest.RingYd, RingHops: aggroest.RingHops, FloorDz: aggroest.FloorDz}
	spread := float32(aggroest.StandSpreadPartyYd)
	if p.IsRaid() {
		spread = float32(aggroest.StandSpreadRaidYd)
	}
	var ownMobs []int
	for i, m := range mobs {
		if own[uint64(m.Guid)] {
			ownMobs = append(ownMobs, i)
		}
	}
	if len(ownMobs) == 0 {
		return nil, fmt.Errorf("pull group %d of leg %d (%s): none of its spawns wakes by proximity or is eligible", group, leg, l.Name)
	}
	// verdict runs the estimate from a tag spot (with the camp walk and drag when a camp is given) and says whether exactly the stage's
	// spawns come, with the body count.
	verdict := func(tag dungeon.Point, camp *dungeon.Point) (bool, int) {
		target, best := -1, math.MaxFloat64
		for _, i := range ownMobs {
			if d := dist(tag, mobs[i].Pos); d < best {
				target, best = i, d
			}
		}
		geom := aggroest.Geometry{TankWalk: []dungeon.Point{tag}, TankSpreadYd: aggroest.TankSpreadYd, Stand: tag, StandSpreadYd: aggroest.TankSpreadYd}
		if camp != nil {
			geom.TankWalk = []dungeon.Point{*camp, tag}
			geom.HasFight, geom.Fight, geom.FightSpreadYd = true, *camp, spread
			geom.Drag = []dungeon.Point{tag, *camp}
		}
		r := aggroest.Estimate(mobs, target, geom, params)
		came := map[uint32]bool{}
		for _, m := range r.Members {
			came[m.Guid] = true
			if !own[uint64(m.Guid)] {
				return false, r.Bodies
			}
		}
		for _, i := range ownMobs {
			if !came[mobs[i].Guid] {
				return false, r.Bodies
			}
		}
		return true, r.Bodies
	}

	// The wake reach of every other spawn at the camp (rule 3), and the units' tank wake reach (rule 4).
	type other struct {
		c     creature
		reach float64
	}
	var others []other
	for _, c := range cs {
		if !eligible(c) || c.FlagsExtra&worldspawn.NoAggro != 0 || own[c.GUID] || dead[c.GUID] {
			continue
		}
		if o, ok := owners[c.GUID]; ok && o.idx <= rowIdx {
			continue
		}
		if r := routeReach(p, c); r > 0 {
			others = append(others, other{c, r})
		}
	}
	unitWake := make([]float64, len(units)) // the raid's wake reach of each unit at the camp (aggro + raid spread + wander)
	for i, u := range units {
		unitWake[i] = routeReach(p, u)
	}
	unitReach := make([]float64, len(units))
	minWake := math.MaxFloat64
	for i, u := range units {
		unitReach[i] = campTankReach(p, u)
		minWake = math.Min(minWake, unitReach[i]+1)
	}
	if minWake > campShotYd {
		res.Notes = append(res.Notes, fmt.Sprintf("no tag spot can exist: every unit's tank wake reach + 1 yd is at least %.1f yd, past the %.1f yd shot (party level %d against level %d: detection range minus the level difference, plus the tank's %.1f yd spread)",
			minWake, campShotYd, routePartyLevel(p), units[0].LevelMax, aggroest.TankSpreadYd))
	}

	// Candidate camps.
	type campPt struct {
		pt                                 dungeon.Point
		casterYd, clearYd, routeYd, packYd float64
	}
	var campPts []campPt
	for x := minX; x <= maxX; x += opt.Step {
		for y := minY; y <= maxY; y += opt.Step {
			// the nearest route sample in the plane gives the reference height
			bi, bd := -1, math.MaxFloat64
			for i, q := range route {
				if d := math.Hypot(float64(q.X)-x, float64(q.Y)-y); d < bd {
					bi, bd = i, d
				}
			}
			if bi < 0 || bd > campRouteYd {
				continue
			}
			z, ok := floors.z(float32(x), float32(y), route[bi].Z)
			if !ok {
				res.Rejected["camp: no navmesh ground"]++
				continue
			}
			pt := dungeon.Point{X: float32(x), Y: float32(y), Z: z}
			rYd := math.MaxFloat64
			for _, q := range route {
				if d := dist(pt, q); d < rYd {
					rYd = d
				}
			}
			if rYd > campRouteYd {
				res.Rejected["camp: rule 5, over 10 yd from the cleared route"]++
				continue
			}
			cYd := -1.0
			okc := true
			for _, c := range casters {
				d := dist(pt, c.Pos)
				if d < dungeon.CasterCampYd {
					okc = false
					break
				}
				if cYd < 0 || d < cYd {
					cYd = d
				}
			}
			if !okc {
				res.Rejected["camp: rule 1, a caster within 40 yd"]++
				continue
			}
			clr := math.MaxFloat64
			for _, o := range others {
				if m := dist(pt, o.c.Pos) - o.reach; m < clr {
					clr = m
				}
			}
			if clr < 0 {
				res.Rejected["camp: rule 3, wakes another pack"]++
				continue
			}
			pk, wakes := math.MaxFloat64, false
			for i, u := range units {
				d := dist(pt, u.Pos)
				if d < pk {
					pk = d
				}
				wakes = wakes || d < unitWake[i]
			}
			if wakes {
				res.Rejected["camp: rule 3, the raid would wake the stage's own pack"]++
				continue
			}
			campPts = append(campPts, campPt{pt, cYd, clr, rYd, pk})
		}
	}
	res.Camps = len(campPts)

	// Candidate tag spots: in the shot of the nearest unit, outside every unit's tank wake reach + 1, and waking the stage alone.
	type tagPt struct {
		pt             dungeon.Point
		shotYd, wakeYd float64
	}
	var tagPts []tagPt
	for x := minX; x <= maxX; x += opt.Step {
		for y := minY; y <= maxY; y += opt.Step {
			ni, nd := -1, math.MaxFloat64
			for i, u := range units {
				if d := math.Hypot(float64(u.Pos.X)-x, float64(u.Pos.Y)-y); d < nd {
					ni, nd = i, d
				}
			}
			if ni < 0 || nd > campShotYd+campFloorDz {
				continue
			}
			z, ok := floors.z(float32(x), float32(y), units[ni].Pos.Z)
			if !ok {
				res.Rejected["tag: no navmesh ground"]++
				continue
			}
			pt := dungeon.Point{X: float32(x), Y: float32(y), Z: z}
			shot, wake := math.MaxFloat64, math.MaxFloat64
			nearest := -1
			for i, u := range units {
				d := dist(pt, u.Pos)
				if d < shot {
					shot, nearest = d, i
				}
				if m := d - (unitReach[i] + 1); m < wake {
					wake = m
				}
			}
			if shot > campShotYd {
				res.Rejected["tag: rule 4, past the 23.5 yd shot"]++
				continue
			}
			if wake < 0 {
				res.Rejected["tag: rule 4, inside a unit's wake reach + 1 yd"]++
				continue
			}
			if opt.Planner != nil {
				ray, err := opt.Planner.Raycast(p.Map, navmesh.Vec3{pt.X, pt.Y, pt.Z}, navmesh.Vec3{units[nearest].Pos.X, units[nearest].Pos.Y, units[nearest].Pos.Z})
				if err == nil && ray.Known && !ray.Clear {
					res.Rejected["tag: rule 4, no clear ray to the nearest unit"]++
					continue
				}
			}
			if ok, _ := verdict(pt, nil); !ok {
				res.Rejected["tag: rule 2, wakes more or less than the stage"]++
				continue
			}
			tagPts = append(tagPts, tagPt{pt, shot, wake})
		}
	}
	res.Tags = len(tagPts)

	// Pairs, shortest drag first.
	type pair struct {
		c, t int
		d    float64
	}
	var pairs []pair
	for ci, c := range campPts {
		for ti, t := range tagPts {
			if d := dist(c.pt, t.pt); d <= dungeon.MaxCampDragYd {
				pairs = append(pairs, pair{ci, ti, d})
			}
		}
	}
	sort.Slice(pairs, func(i, j int) bool {
		if pairs[i].d != pairs[j].d {
			return pairs[i].d < pairs[j].d
		}
		if campPts[pairs[i].c].pt != campPts[pairs[j].c].pt {
			a, b := campPts[pairs[i].c].pt, campPts[pairs[j].c].pt
			return a.X < b.X || a.X == b.X && a.Y < b.Y
		}
		a, b := tagPts[pairs[i].t].pt, tagPts[pairs[j].t].pt
		return a.X < b.X || a.X == b.X && a.Y < b.Y
	})
	for _, pr := range pairs {
		if len(res.Candidates) >= opt.Top {
			break
		}
		if res.Pairs >= campSearchMaxPairs {
			res.PairsTruncated = true
			break
		}
		c, t := campPts[pr.c], tagPts[pr.t]
		near := false
		for _, k := range res.Candidates {
			near = near || dist(k.Camp, c.pt) < campSpacingYd && dist(k.Tag, t.pt) < campSpacingYd
		}
		if near {
			continue
		}
		res.Pairs++
		camp := c.pt
		ok, bodies := verdict(t.pt, &camp)
		if !ok {
			continue
		}
		path := 0.0
		if opt.Planner != nil {
			pth, err := opt.Planner.Plan(p.Map, navmesh.Vec3{t.pt.X, t.pt.Y, t.pt.Z}, navmesh.Vec3{c.pt.X, c.pt.Y, c.pt.Z})
			if err != nil || pth.Status != navmesh.Complete || pth.Length > dungeon.MaxCampPathYd {
				continue
			}
			path = float64(pth.Length)
		}
		res.Candidates = append(res.Candidates, CampCandidate{Camp: c.pt, Tag: t.pt, DragYd: pr.d, PathYd: path, CasterYd: c.casterYd,
			ClearYd: c.clearYd, RouteYd: c.routeYd, PackYd: c.packYd, ShotYd: t.shotYd, WakeYd: t.wakeYd, Bodies: bodies})
	}
	return res, nil
}

// Format prints the result for a person: what was searched, the counts, and the candidates with their distances and the plan JSON of each.
func (r *CampSearchResult) Format() string {
	var b strings.Builder
	what := fmt.Sprintf("pull group %d (pull_groups[%d])", r.Group, r.Group-1)
	if r.Staged {
		what += fmt.Sprintf(" stage %d", r.Stage)
	}
	fmt.Fprintf(&b, "camp-search %s leg %d (%s) %s\n", r.Plan, r.Leg, r.LegName, what)
	fmt.Fprintf(&b, "mode: %s\n", r.Mode)
	if r.PartyLevelOverride {
		fmt.Fprintf(&b, "party level: %d (-camp-party-level; the lint judges the plan's own level_band[0], so a point found here may still fail it)\n", r.PartyLevel)
	} else {
		fmt.Fprintf(&b, "party level: %d (the plan's level_band[0])\n", r.PartyLevel)
	}
	fmt.Fprintf(&b, "pack: %d spawns", len(r.Units))
	for i, u := range r.Units {
		if i == 6 {
			fmt.Fprintf(&b, ", ...")
			break
		}
		fmt.Fprintf(&b, "%s%s (guid %d, entry %d) at %.1f %.1f %.1f", map[bool]string{true: "; ", false: ": "}[i > 0], u.Name, u.GUID, u.Entry, u.Pos.X, u.Pos.Y, u.Pos.Z)
	}
	fmt.Fprintf(&b, "\ncaster spawns the camp keeps %d yd from: %d\n", dungeon.CasterCampYd, r.Casters)
	fmt.Fprintf(&b, "candidate camps (rules 1, 3, 5): %d; candidate tag spots (rules 4, 2): %d; pairs estimated: %d%s\n", r.Camps, r.Tags, r.Pairs,
		map[bool]string{true: " (stopped at the pair limit)", false: ""}[r.PairsTruncated])
	for _, n := range r.Notes {
		b.WriteString("note: " + n + "\n")
	}
	var reasons []string
	for k, n := range r.Rejected {
		reasons = append(reasons, fmt.Sprintf("  %s: %d", k, n))
	}
	sort.Strings(reasons)
	if len(reasons) > 0 {
		b.WriteString("grid points dropped, by the first rule they fail:\n" + strings.Join(reasons, "\n") + "\n")
	}
	if len(r.Candidates) == 0 {
		b.WriteString("no candidate: no camp and tag spot pair passes every rule\n")
		return b.String()
	}
	b.WriteString("rank  drag  path  | camp x y z | caster nearest  clear margin  route  pack | tag x y z | shot  wake margin  bodies\n")
	for i, c := range r.Candidates {
		caster := "none"
		if c.CasterYd >= 0 {
			caster = fmt.Sprintf("%.1f", c.CasterYd)
		}
		path := "n/a"
		if c.PathYd > 0 {
			path = fmt.Sprintf("%.1f", c.PathYd)
		}
		fmt.Fprintf(&b, "%d  %.1f  %s  | %.1f %.1f %.1f | %s  %.1f  %.1f  %.1f | %.1f %.1f %.1f | %.1f  %.1f  %d\n", i+1, c.DragYd, path,
			c.Camp.X, c.Camp.Y, c.Camp.Z, caster, c.ClearYd, c.RouteYd, c.PackYd, c.Tag.X, c.Tag.Y, c.Tag.Z, c.ShotYd, c.WakeYd, c.Bodies)
	}
	b.WriteString("plan JSON of each (camp, tag spot):\n")
	for i, c := range r.Candidates {
		fmt.Fprintf(&b, "%d  \"camp\": {\"x\": %.2f, \"y\": %.2f, \"z\": %.2f}, \"tag\": [{\"x\": %.2f, \"y\": %.2f, \"z\": %.2f}]\n", i+1,
			c.Camp.X, c.Camp.Y, c.Camp.Z, c.Tag.X, c.Tag.Y, c.Tag.Z)
	}
	return b.String()
}
