// route-regen rewrites one leg's route from the navmesh: the audit's §4.2 method (research/zg-waypoints/WAYPOINT-AUDIT.md).
//
//	dungeongen route-regen --mmaps DIR --leg 7 [--dry-run] [--spawns DIR] [--dir dungeons] [--out FILE] zul_gurub
//
// It keeps the leg's anchors (its `to`, its gm_cross vias, every pull group's stand or last approach point, the boss and
// its approach), moves an anchor that fails the placement rules to the cheapest passing point within 25 yd, orders the
// pulls by a navmesh path-length matrix that flags one-way pairs (a path more than twice the reverse one) so no uphill
// return is scheduled while a downhill order exists, plans each anchor pair round the packs not pulled yet (aggro + 8), the
// patrol lanes (aggro + 5) and water, string-pulls the path with clearance, pushes each kept corner 3 yd inward, checks the
// points again and spaces them 8-45 yd apart, and writes them as authored approach points (or via points), so the exporter
// has nothing to cut. The pull set does not change: only the order and the route do.
package main

import (
	"flag"
	"fmt"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/pjerra/turtle-agent/internal/dungeon"
	"github.com/pjerra/turtle-agent/internal/navmesh"
	"github.com/pjerra/turtle-agent/internal/planexport"
	"github.com/pjerra/turtle-agent/internal/planlint"
	"github.com/pjerra/turtle-agent/internal/worldspawn"
)

// The limits route-regen works to (the audit's §3 rules and §4.2 steps).
const (
	regenWallYd        = 2.5   // a point's clearance from a mesh edge
	regenStandWallYd   = 4.0   // a stand's (the party gathers there)
	regenAggroPointYd  = 8.0   // a point keeps this far outside an unpulled unit's aggro
	regenAggroSegYd    = 5.0   // a walked segment keeps this far outside it
	regenPatrolYd      = 5.0   // and this far outside a patrol lane
	regenMaxMoveYd     = 25.0  // how far a failing anchor may move
	regenMinHopYd      = 8.0   // merge anything under this (the arrival radius is 3)
	regenMaxHopYd      = 45.0  // split anything over this (under kApproachHopYd 40 + 5)
	regenMaxHopLongYd  = 110.0 // the one hop left when a split is impossible (under kTooFarLegYd 120)
	regenOneWayRatio   = 2.0   // a path over twice its reverse is one-way (the brief's flag)
	regenOneWayMargin  = 1.0   // ignore asymmetries under this many yards
	regenGroundZtolYd  = 0.5   // a point's floor is within this of its z
	regenFloorWindowYd = 12.0  // how far above and below a point Floors is asked
	regenLedgeDropYd   = 3.0   // no floor this much lower within regenLedgeNear
	regenLedgeNearYd   = 4.0   //
	regenCeilLowYd     = 4.5   // no floor this much (to regenCeilHigh) above or below within
	regenCeilHighYd    = 8.0   //   planlint.LedgeNearYd (kStandZTolYd 4.5 by a margin)
	regenCeilNearYd    = 4.5   //
	regenPartYd        = 60    // the party's level: the aggro radius is computed at the plan's band bottom, else this
	regenRayDirs       = 16    //
	regenRayYd         = 6.0   // the clearance rays reach past the widest edge wanted
	regenPushYd        = 3.0   // a kept corner is pushed this far into the walkable side
	regenMaxSegSamples = 2.0   // a segment's clearance is sampled every this many yards
	regenFillerZtolYd  = 1.5   // a split point's floor is within this of the line's z
	regenMaxHopWarn    = 60.0  // the exporter cuts a move over this (MoveGapWarnYd) into rows of its own
)

// The exit statuses of route-regen (0: done, even with a move note for the lead; 2: the command cannot run).
const (
	regenExitOK    = 0
	regenExitUsage = 2
)

const routeRegenUsage = "usage: dungeongen route-regen --mmaps DIR --leg N [--leg N...] [--dry-run] [--dir dungeons] [--spawns DIR] [--out FILE] [--force] <plan>"

// routeRegenCmd is `dungeongen route-regen`: one leg's route rewritten from the navmesh (§4.2 of the ZG waypoint audit).
func routeRegenCmd(args []string) int {
	fs := flag.NewFlagSet("dungeongen route-regen", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	dir := fs.String("dir", "dungeons", "the folder with the plan files")
	mmaps := fs.String("mmaps", "", "folder with the server's mmaps (<map>.mmap and <map>*.mmtile); required")
	spawns := fs.String("spawns", "", "the map's world export for the aggro and patrol checks (default: tools/dungeongen/testdata/mapN)")
	out := fs.String("out", "", "the plan file to write (default: the plan file named)")
	force := fs.Bool("force", false, "with --out: replace an existing file")
	dryRun := fs.Bool("dry-run", false, "print the diff and write nothing")
	legs := legFlag{}
	fs.Var(&legs, "leg", "the plan leg to regenerate (repeat for several legs)")
	if err := fs.Parse(args); err != nil {
		return regenExitUsage
	}
	fail := func(format string, a ...any) int {
		fmt.Fprintf(os.Stderr, "route-regen: "+format+"\n", a...)
		return regenExitUsage
	}
	switch {
	case fs.NArg() != 1:
		fmt.Fprintln(os.Stderr, routeRegenUsage)
		return fail("name one plan")
	case *mmaps == "":
		fmt.Fprintln(os.Stderr, routeRegenUsage)
		return fail("--mmaps is required")
	case len(legs) == 0:
		fmt.Fprintln(os.Stderr, routeRegenUsage)
		return fail("--leg is required")
	}
	name := fs.Arg(0)
	path := name
	if !strings.HasSuffix(path, ".json") {
		path = filepath.Join(*dir, name+".json")
		name = strings.TrimSuffix(filepath.Base(path), ".json")
	}
	p, err := dungeon.Load(path)
	if err != nil {
		return fail("%v", err)
	}
	for _, n := range legs {
		if n < 0 || n >= len(p.Legs) {
			return fail("leg %d: %s has %d legs", n, name, len(p.Legs))
		}
	}
	b0, err := dungeon.Marshal(p)
	if err != nil {
		return fail("%s: %v", name, err)
	}
	before := map[int]*dungeon.Plan{}
	for _, n := range legs {
		c, err := dungeon.Parse(name+".json", b0)
		if err != nil {
			return fail("%s: %v", name, err)
		}
		before[n] = c
	}
	spawnDir := *spawns
	if spawnDir == "" {
		spawnDir = filepath.Join(*dir, "..", "tools", "dungeongen", "testdata", fmt.Sprintf("map%d", p.Map))
	}
	world, notes, err := worldspawn.Load(spawnDir, p.Map)
	if err != nil {
		return fail("the world export in %s: %v (pass --spawns DIR)", spawnDir, err)
	}
	if len(notes) > 0 {
		fmt.Fprintf(os.Stdout, "%s: world export: %s\n", name, strings.Join(notes, "; "))
	}
	planner := navmesh.NewPlanner(*mmaps)
	defer planner.Close()
	if err := probeNavmesh(planner, p.Map); err != nil {
		return fail("%v", err)
	}
	planner.SetAvoid(p.Map, dungeon.NavBoxes(p.Avoid))
	r := &regen{mapID: p.Map, nav: planner, mmaps: *mmaps, world: world, plan: p}
	var reports []string
	for _, n := range legs {
		rep, err := r.regenLeg(n)
		if err != nil {
			return fail("leg %d (%s): %v", n, p.Legs[n].Name, err)
		}
		reports = append(reports, rep)
	}
	if *dryRun {
		fmt.Fprintf(os.Stdout, "%s: dry run, nothing written\n%s\n", name, strings.Join(reports, "\n"))
		return regenExitOK
	}
	target := path
	if *out != "" {
		target = *out
		if _, err := os.Stat(target); err == nil && !*force {
			return fail("%s exists (pass --force to replace it)", target)
		}
	}
	b, err := dungeon.Marshal(p)
	if err != nil {
		return fail("%s: %v", name, err)
	}
	if err := os.WriteFile(target, b, 0o644); err != nil {
		return fail("%v", err)
	}
	fmt.Fprintf(os.Stdout, "%s: wrote %s\n%s\n", name, target, strings.Join(reports, "\n"))
	return regenExitOK
}

// legFlag collects --leg (it may be given once per leg).
type legFlag []int

func (l *legFlag) String() string { return fmt.Sprint([]int(*l)) }

func (l *legFlag) Set(s string) error {
	var n int
	if _, err := fmt.Sscanf(s, "%d", &n); err != nil {
		return fmt.Errorf("%q is not a leg number", s)
	}
	*l = append(*l, n)
	return nil
}

// probeNavmesh makes sure the planner can read the map's mmap and one tile, so a missing folder fails before any work.
func probeNavmesh(pl *navmesh.Planner, mapID uint32) error {
	if _, err := pl.Floors(mapID, 0, 0, -10000, 10000); err != nil {
		return fmt.Errorf("the navmesh: %w", err)
	}
	return nil
}

// regen is one plan's route regeneration.
type regen struct {
	mapID uint32
	nav   *navmesh.Planner
	mmaps string
	world *worldspawn.World
	plan  *dungeon.Plan
	// lanesForOrder are the patrol loops order weighs (model builds them for the leg under regeneration), and orderGroups the
	// leg's groups in the order of order()'s indices.
	lanesForOrder  []regenLane
	orderGroups    []*regenGroup
	lanesSpawns    []regenSpawn
	pulledForOrder map[int]bool
	// must is the extra pull order learned from a stand a later pack's aggro blocks (owner before group), and deps the next
	// round's additions.
	must map[int]map[int]bool
	deps map[int]int
	// ownerLeg says which leg pulls a spawn's owner, and leg the leg under regeneration: a path is not held back by a pack a
	// later leg pulls (the audit's leg 7 and 8 packs stand in each other's corridors; the plan owner orders the legs).
	ownerLeg map[int]int
	leg      int
}

// regenReachErr is a route a later pack's aggro blocks: the pack (owner) is pulled first by the retry.
type regenReachErr struct {
	owner int
	msg   string
}

func (e *regenReachErr) Error() string { return e.msg }

// regenSpawn is a world spawn with the reach it wakes at (the aggro radius plus a random mover's wander) and the pull group
// that owns it (the first one whose entries hold its entry and whose positions reach it; -1 when no row pulls it).
type regenSpawn struct {
	sp    worldspawn.Spawn
	reach float64
	owner int
}

// regenLane is one patrol loop with the reach of its leader and the pull group that owns the leader (-1: no row).
type regenLane struct {
	path  []dungeon.Point
	reach float64
	owner int
}

// regenGroup is one pull group of the plan; a group is owned across the whole run (a leg's entry can stand in the aggro of a
// pack an earlier leg already pulled).
type regenGroup struct {
	id        int // its id in the run's order (plan order, leg by leg)
	idx       int // its index in its leg's pull_groups as written
	leg       int
	g         *dungeon.PullGroup
	positions []dungeon.Point
	centre    dungeon.Point
}

// regenBossID is the owner id of a leg's boss encounter: negative, so it never collides with a pull group's id.
func regenBossID(leg int) int { return -1000 - leg }

// regenModel is the plan's packs and patrols with the ownership the leg's checks need.
type regenModel struct {
	local      []*regenGroup // this leg's groups, in written order
	all        []*regenGroup // every leg's groups, plan order
	spawns     []regenSpawn
	lanes      []regenLane
	pulledBase map[int]bool // the groups of the legs before this one (pulled when the leg starts)
}

// regenLeg regenerates one leg in place and returns its diff summary. A stand a later pack's aggro blocks teaches the order
// that the pack is pulled first; the leg is regenerated once more with it (at most three times).
func (r *regen) regenLeg(legIdx int) (string, error) {
	rep, err := r.regenLegOnce(legIdx)
	if err != nil && r.deps != nil {
		// A stand a later pack blocks is a pull for that pack first; the leg is regenerated once more with it.
		if r.must == nil {
			r.must = map[int]map[int]bool{}
		}
		added := false
		for g, owner := range r.deps {
			if r.must[g] == nil {
				r.must[g] = map[int]bool{}
			}
			if !r.must[g][owner] {
				r.must[g][owner] = true
				added = true
			}
		}
		if added {
			r.deps = nil
			return r.regenLegOnce(legIdx)
		}
	}
	return rep, err
}

func (r *regen) regenLegOnce(legIdx int) (string, error) {
	leg := &r.plan.Legs[legIdx]
	if len(leg.PullGroups) == 0 && leg.Boss == nil {
		return "", fmt.Errorf("no pull groups and no boss: nothing to order")
	}
	model := r.model(legIdx)
	groups, spawns, lanes := model.local, model.spawns, model.lanes
	r.lanesForOrder = lanes
	r.orderGroups = groups
	r.lanesSpawns = spawns
	r.pulledForOrder = model.pulledBase

	// The leg's entry: its first via point (the walk in from the leg before), else its `to`.
	entry := leg.To
	var pinnedVia []dungeon.Waypoint
	for i, w := range leg.Via {
		if w.Direct || w.GMCross || w.CheckSkip != "" {
			pinnedVia = append(pinnedVia, w)
			continue
		}
		if i == 0 {
			entry = w.Point
		}
	}
	// The leg starts where the leg before it ended (the exporter measures its first hop from that row): a first via far from
	// it makes the exporter cut a long hop of its own, and its split rows are the water and off-floor ones.
	if legIdx > 0 && len(pinnedVia) == 0 {
		if prevEnd, ok := regenLegEnd(&r.plan.Legs[legIdx-1]); ok && regenDist3(prevEnd, entry) > 60 {
			entry = prevEnd
		}
	}

	// A pinned via (gm_cross, direct or check_skip) is a lesson the navmesh cannot judge: keep the leg's via as written then.

	// Snapshot the old route's walk for the diff.
	oldLong := r.longHops(legIdx, nil)
	oldWalk, oldWater, oldPoints := r.walkLen(legIdx)
	oldLints := r.lintLeg(legIdx)

	// 1. Anchors: every group's stand (its last approach point, or one found for it) and the boss's stand.
	stands := make([]dungeon.Point, len(groups))
	for i, gr := range groups {
		stands[i] = regenLastApproach(gr.g)
	}
	var bossStand dungeon.Point
	hasBossStand := false
	if leg.Boss != nil {
		bossStand = regenLastApproachBoss(leg.Boss)
		hasBossStand = true
	}

	// 1b. The leg's entry is an anchor too: a via point off the mesh (a walk-log frame) moves to the floor near it.
	moveNotes := []string{}
	if len(pinnedVia) == 0 {
		var next *dungeon.Point
		moved, note, err := r.placeAnchor("via", entry, nil, next, regenCopySet(model.pulledBase), -1, false, lanes, spawns)
		if err != nil {
			return "", fmt.Errorf("the leg's entry point: %w", err)
		}
		if note != "" {
			moveNotes = append(moveNotes, note)
		}
		entry = moved
	}

	// 2. Order the pulls by the path-length matrix between their pack centres.
	targets := make([]dungeon.Point, len(groups))
	for i, gr := range groups {
		targets[i] = gr.centre
	}
	endPt := leg.To
	hasEnd := false
	if legIdx+1 < len(r.plan.Legs) {
		// Where the next leg starts walking: its first via, else its `to`. The leg has to end near it.
		next := &r.plan.Legs[legIdx+1]
		endPt, hasEnd = next.To, true
		if len(next.Via) > 0 {
			endPt = next.Via[0].Point
		}
	}
	order, oneWays := r.order(targets, leg.To, endPt, hasEnd)

	// 2b. The leg's `to` is an anchor: it moves when it is off the mesh or fails the rules. It stands near the first pack it
	// leads to, so that pack's units do not count against it.
	if len(order) > 0 {
		self := -1
		if regenDist3(leg.To, groups[order[0]].centre) <= 40 {
			self = groups[order[0]].id
		}
		var next *dungeon.Point
		if stands[order[0]] != (dungeon.Point{}) {
			n := stands[order[0]]
			next = &n
		}
		moved, note, err := r.placeAnchor("to", leg.To, &entry, next, regenCopySet(model.pulledBase), self, false, lanes, spawns)
		if err != nil {
			return "", fmt.Errorf("the leg's `to` point: %w", err)
		}
		if note != "" {
			moveNotes = append(moveNotes, note)
		}
		leg.To = moved
	}

	// 3. Move an anchor that fails the rules to the cheapest passing point within 25 yd, in the new order.
	seq := make([]dungeon.Point, 0, len(order)+1) // the stands in walking order
	seq = append(seq, entry)
	pulled := regenCopySet(model.pulledBase)
	var bossPoint dungeon.Point
	if leg.Boss != nil {
		bossPoint = leg.Boss.Position
	}
	for k, gi := range order {
		gr := groups[gi]
		stand := stands[gi]
		if stand == (dungeon.Point{}) {
			var err error
			stand, err = r.estimateStand(gr.centre, seq[len(seq)-1], pulled, gr.id, lanes, spawns)
			if err != nil {
				// The plan needs no approach for a group: the run leader approaches a pack it sees from its last move
				// point itself. A pack with no stand it can stand at keeps that, and the group is pulled in passing.
				moveNotes = append(moveNotes, fmt.Sprintf("pull group %d (%v) has no stand (%v): pulled in passing, no approach", gi+1, gr.g.Entries, err))
				stands[gi] = dungeon.Point{}
				pulled[gr.id] = true
				continue
			}
		}
		var next *dungeon.Point
		if k+1 < len(order) {
			n := stands[order[k+1]]
			if n == (dungeon.Point{}) {
				n = groups[order[k+1]].centre
			}
			next = &n
		} else if leg.Boss != nil {
			next = &bossPoint
		}
		prev := &seq[len(seq)-1]
		moved, note, err := r.placeAnchor("stand", stand, prev, next, pulled, gr.id, true, lanes, spawns)
		if err != nil {
			return "", fmt.Errorf("pull group %d (%v): %w", gi+1, gr.g.Entries, err)
		}
		if note != "" {
			moveNotes = append(moveNotes, note)
		}
		stands[gi] = moved
		seq = append(seq, moved)
		pulled[gr.id] = true
	}
	if leg.Boss != nil {
		if !hasBossStand {
			var err error
			bossStand, err = r.estimateStand(bossPoint, seq[len(seq)-1], pulled, regenBossID(legIdx), lanes, spawns)
			if err != nil {
				return "", fmt.Errorf("the boss has no stand: %w", err)
			}
		}
		prev := &seq[len(seq)-1]
		moved, note, err := r.placeAnchor("boss", bossStand, prev, &bossPoint, pulled, regenBossID(legIdx), true, lanes, spawns)
		if err != nil {
			return "", fmt.Errorf("the boss: %w", err)
		}
		if note != "" {
			moveNotes = append(moveNotes, note)
		}
		bossStand = moved
	}

	// 4. Route: the leg's via from its entry to `to`, then each group's approach in the new order, then the boss's.
	if len(pinnedVia) == 0 && entry != leg.To {
		viaSelf := -1
		if len(order) > 0 && regenDist3(leg.To, groups[order[0]].centre) <= 45 {
			viaSelf = groups[order[0]].id // the walk ends at the first pack it pulls
		}
		via, err := r.route(entry, leg.To, regenCopySet(model.pulledBase), viaSelf, 0, lanes, spawns)
		if err != nil {
			return "", fmt.Errorf("the leg's walk from %.1f %.1f %.1f to its `to`: %w", entry.X, entry.Y, entry.Z, err)
		}
		leg.Via = nil
		if entry != leg.To {
			leg.Via = append(leg.Via, dungeon.Waypoint{Point: entry})
			if len(via) > 0 && via[len(via)-1] == leg.To {
				via = via[:len(via)-1] // `to` is its own walk after the via points
			}
			for _, q := range via {
				leg.Via = append(leg.Via, dungeon.Waypoint{Point: q})
			}
		}
	}
	newGroups := make([]dungeon.PullGroup, len(order))
	pulled = regenCopySet(model.pulledBase)
	prev := leg.To
	minAggro, minWall := math.Inf(1), math.Inf(1)
	totalPts := 0
	for k, gi := range order {
		gr := groups[gi]
		stand := stands[gi]
		if stand == (dungeon.Point{}) {
			g := *gr.g
			g.Approach = nil
			newGroups[k] = g
			pulled[gr.id] = true
			continue
		}
		viaPts, err := r.route(prev, stand, pulled, gr.id, dungeon.MaxApproach, lanes, spawns)
		if err != nil {
			// The walk to the stand cannot be kept clear: the group keeps its written approach when that passes the required
			// rules (water, off-floor, aggro), else it is pulled in passing and the lead is told.
			g := *gr.g
			if r.oldApproachOK(&g, pulled, gr.id, lanes, spawns) {
				moveNotes = append(moveNotes, fmt.Sprintf("pull group %d (%v): no clear route to its stand %.1f %.1f %.1f (%v): the written approach kept",
					gi+1, gr.g.Entries, stand.X, stand.Y, stand.Z, err))
			} else {
				moveNotes = append(moveNotes, fmt.Sprintf("pull group %d (%v): no clear route to its stand %.1f %.1f %.1f (%v): pulled in passing, no approach",
					gi+1, gr.g.Entries, stand.X, stand.Y, stand.Z, err))
				g.Approach = nil
			}
			newGroups[k] = g
			pulled[gr.id] = true
			continue
		}
		approach := viaPts // route ends at the stand
		if len(approach) == 0 || approach[len(approach)-1] != stand {
			approach = append(approach, stand)
		}
		if len(approach) > dungeon.MaxApproach {
			return "", fmt.Errorf("the route to pull group %d needs %d approach points (at most %d)", gi+1, len(approach), dungeon.MaxApproach)
		}
		g := *gr.g
		g.Approach = approach
		newGroups[k] = g
		totalPts += len(approach)
		for _, p := range approach {
			if a, w, _, ok := r.margins(p, pulled, gr.id, lanes, spawns); ok {
				minAggro = math.Min(minAggro, a)
				minWall = math.Min(minWall, w)
			}
		}
		pulled[gr.id] = true
		prev = stand
	}
	leg.PullGroups = newGroups
	if leg.Boss != nil {
		viaPts, err := r.route(prev, bossStand, pulled, regenBossID(legIdx), dungeon.MaxApproach, lanes, spawns)
		if err != nil {
			return "", fmt.Errorf("the route to the boss: %w", err)
		}
		approach := viaPts
		if len(approach) == 0 || approach[len(approach)-1] != bossStand {
			approach = append(approach, bossStand)
		}
		if len(approach) > dungeon.MaxApproach {
			return "", fmt.Errorf("the route to the boss needs %d approach points (at most %d)", len(approach), dungeon.MaxApproach)
		}
		leg.Boss.Approach = approach
		totalPts += len(approach)
		for _, p := range approach {
			if a, w, _, ok := r.margins(p, pulled, regenBossID(legIdx), lanes, spawns); ok {
				minAggro = math.Min(minAggro, a)
				minWall = math.Min(minWall, w)
			}
		}
	}

	// 5. The note: what the route is and the margins it keeps.
	rev := regenRev()
	note := fmt.Sprintf("ROUTE-REGEN %s (2026-11-03, audit §4.2): %d points, minimum aggro margin %.1f yd, minimum wall %.1f yd; "+
		"pull order regen from the navmesh path-length matrix (%d one-way pairs flagged); every point authored, no exporter cut",
		rev, totalPts, math.Min(minAggro, 99), math.Min(minWall, 99), len(oneWays))
	leg.Notes = append(leg.Notes, note)

	// 6. The diff.
	newWalk, newWater, newPoints := r.walkLen(legIdx)
	newLints := r.lintLeg(legIdx)
	var b strings.Builder
	fmt.Fprintf(&b, "leg %d %s: walk %.1f -> %.1f yd (%.2fx), anchors %d -> %d, water rows %d -> %d, hops over %g yd %d -> %d\n",
		legIdx, leg.Name, oldWalk, newWalk, newWalk/max(oldWalk, 1), oldPoints, newPoints, oldWater, newWater,
		regenMaxHopYd, oldLong, r.longHops(legIdx, nil))
	fmt.Fprintf(&b, "  lints (authored rows): %s -> %s\n", regenLintText(oldLints), regenLintText(newLints))
	if len(oneWays) > 0 {
		fmt.Fprintf(&b, "  one-way pairs: %s\n", strings.Join(oneWays, "; "))
	}
	for _, n := range moveNotes {
		fmt.Fprintf(&b, "  %s\n", n)
	}
	return strings.TrimRight(b.String(), "\n"), nil
}

// regenLegEnd is where a leg's last written row stands: its boss, else its last group's stand (or pack centre).
func regenLegEnd(l *dungeon.Leg) (dungeon.Point, bool) {
	if l.Boss != nil {
		return l.Boss.Position, true
	}
	if n := len(l.PullGroups); n > 0 {
		g := &l.PullGroups[n-1]
		if p := regenLastApproach(g); p != (dungeon.Point{}) {
			return p, true
		}
		return regenCentre(g.Positions), true
	}
	for i := len(l.Via) - 1; i >= 0; i-- {
		return l.Via[i].Point, true
	}
	return dungeon.Point{}, false
}

// regenCentre is the mean of a group's positions.
func regenCentre(ps []dungeon.Point) dungeon.Point {
	if len(ps) == 0 {
		return dungeon.Point{}
	}
	var c dungeon.Point
	for _, p := range ps {
		c.X += p.X
		c.Y += p.Y
		c.Z += p.Z
	}
	n := float32(len(ps))
	return dungeon.Point{X: c.X / n, Y: c.Y / n, Z: c.Z / n}
}

func regenLastApproach(g *dungeon.PullGroup) dungeon.Point {
	if len(g.Approach) == 0 {
		return dungeon.Point{}
	}
	return g.Approach[len(g.Approach)-1]
}

func regenLastApproachBoss(b *dungeon.Boss) dungeon.Point {
	if len(b.Approach) == 0 {
		return dungeon.Point{}
	}
	return b.Approach[len(b.Approach)-1]
}

// regenRev is the checkout's short commit, "+local" when it has uncommitted changes.
func regenRev() string {
	out, err := runGit("rev-parse", "--short", "HEAD")
	if err != nil {
		return "local"
	}
	rev := strings.TrimSpace(out)
	if dirty, err := runGit("status", "--porcelain"); err == nil && strings.TrimSpace(dirty) != "" {
		rev += "+local"
	}
	return rev
}

// spawns reads the world export into the spawns route-regen checks against: the leg's skip entries are dropped (fish, triggers,
// friendly NPCs), and each spawn's reach is its aggro radius at the party level plus a random mover's wander.
func (r *regen) spawns(leg *dungeon.Leg) []regenSpawn {
	skip := map[uint32]bool{}
	for _, e := range leg.Skip {
		skip[e] = true
	}
	out := make([]regenSpawn, 0, len(r.world.Spawns))
	for _, s := range r.world.Spawns {
		if !s.Hostile() || skip[s.Entry] || s.LevelMax <= 0 || s.LevelMax < regenPartYd-30 {
			continue // ambient critters (ZG's frogs and the like) are nothing a raid pulls
		}
		reach := float64(worldspawn.AggroRadius(s, regenPartYd, 1))
		if s.MovementType == 1 && s.Wander < 50 {
			reach += float64(s.Wander)
		}
		out = append(out, regenSpawn{sp: s, reach: reach, owner: -1})
	}
	return out
}

// regenBossEntry says the entry belongs to the boss encounter (the boss itself, its First mobs or its phase entries).
func regenBossEntry(b *dungeon.Boss, e uint32) bool {
	if b.Entry == e {
		return true
	}
	for _, x := range b.First {
		if x == e {
			return true
		}
	}
	for _, x := range b.PhaseEntries {
		if x == e {
			return true
		}
	}
	return false
}

func regenHasEntry(es []uint32, e uint32) bool {
	for _, x := range es {
		if x == e {
			return true
		}
	}
	return false
}

// lanes builds the patrol loops, each owned by the pull group that owns its leader (none: the loop counts always), and keeps
// a loop's reach as its leader's.
func (r *regen) lanes(leg *dungeon.Leg, spawns []regenSpawn) []regenLane {
	byGUID := map[uint64]*regenSpawn{}
	for i := range spawns {
		byGUID[spawns[i].sp.GUID] = &spawns[i]
	}
	out := make([]regenLane, 0, len(r.world.Patrols))
	for _, p := range r.world.Patrols {
		if len(p.Path) < 2 {
			continue
		}
		l := regenLane{path: p.Path, owner: -1}
		if len(p.SpawnGUIDs) > 0 {
			if s, ok := byGUID[p.SpawnGUIDs[0]]; ok {
				l.owner = s.owner
				l.reach = s.reach
			}
		}
		if l.reach == 0 {
			// A leadered spawn the leg skips (a fish in the lake) is no lane the route must keep from.
			if len(p.Entries) > 0 && regenHasEntry(leg.Skip, p.Entries[0]) {
				continue
			}
			l.reach = 20
		}
		out = append(out, l)
	}
	return out
}

// model builds the plan's groups, spawns and lanes with their owners (a spawn belongs to the first pull group, in plan order,
// whose entries hold its entry and whose positions reach it; a boss's encounter owns its own entries within 40 yd; -1: no row).
func (r *regen) model(legIdx int) *regenModel {
	leg := &r.plan.Legs[legIdx]
	m := &regenModel{pulledBase: map[int]bool{}}
	r.ownerLeg = map[int]int{}
	r.leg = legIdx
	id := 0
	for li := range r.plan.Legs {
		for gi := range r.plan.Legs[li].PullGroups {
			g := &r.plan.Legs[li].PullGroups[gi]
			gr := &regenGroup{id: id, idx: gi, leg: li, g: g, positions: g.Positions, centre: regenCentre(g.Positions)}
			id++
			m.all = append(m.all, gr)
			r.ownerLeg[gr.id] = li
			if li == legIdx {
				m.local = append(m.local, gr)
			} else if li < legIdx {
				m.pulledBase[gr.id] = true
			}
			if li < legIdx && r.plan.Legs[li].Boss != nil {
				m.pulledBase[regenBossID(li)] = true
			}
		}
	}
	m.spawns = r.spawns(leg)
	for i := range m.spawns {
		s := &m.spawns[i]
		owned := false
		for li := range r.plan.Legs {
			b := r.plan.Legs[li].Boss
			if b != nil && regenBossEntry(b, s.sp.Entry) && regenDist3(s.sp.Pos, b.Position) <= 40 {
				s.owner = regenBossID(li)
				owned = true
				break
			}
		}
		if owned {
			continue
		}
		for _, gr := range m.all {
			if !regenHasEntry(gr.g.Entries, s.sp.Entry) {
				continue
			}
			reach := float64(max(gr.g.Radius, 3)) + 3
			for _, p := range gr.positions {
				if regenDist3(s.sp.Pos, p) <= reach {
					s.owner = gr.id
					owned = true
					break
				}
			}
			if owned {
				break
			}
		}
	}
	m.lanes = r.lanes(leg, m.spawns)
	return m
}

// margins is a point's aggro margin (distance to the nearest qualifying unit minus its reach) and its wall clearance, with
// the groups already pulled and the pull's own target (self) left out. ok is false when the mesh cannot answer.
func (r *regen) margins(p dungeon.Point, pulled map[int]bool, self int, lanes []regenLane, spawns []regenSpawn) (float64, float64, float64, bool) {
	aggro := math.Inf(1)
	for _, s := range spawns {
		if (self != -1 && s.owner == self) || (s.owner != -1 && pulled[s.owner]) {
			continue
		}
		if m := regenDist3(p, s.sp.Pos) - s.reach; m < aggro {
			aggro = m
		}
	}
	patrol := math.Inf(1)
	for _, l := range lanes {
		if (self != -1 && l.owner == self) || (l.owner != -1 && pulled[l.owner]) {
			continue
		}
		if m := regenLoopDist(p, l.path) - l.reach; m < patrol {
			patrol = m
		}
	}
	wall, ok := r.clearance(p)
	return aggro, wall, patrol, ok
}

// clearance is the nearest mesh edge around p: the shortest of regenRayDirs rays, and whether the mesh answered.
func (r *regen) clearance(p dungeon.Point) (float64, bool) {
	from := navmesh.Vec3{p.X, p.Y, p.Z}
	min := math.Inf(1)
	for k := 0; k < regenRayDirs; k++ {
		a := 2 * math.Pi * float64(k) / regenRayDirs
		to := navmesh.Vec3{p.X + float32(regenRayYd*math.Cos(a)), p.Y + float32(regenRayYd*math.Sin(a)), p.Z}
		ray, err := r.nav.RaycastNoWater(r.mapID, from, to)
		if err != nil || !ray.Known {
			return 0, false
		}
		d := float64(regenRayYd)
		if !ray.Clear {
			d = float64(ray.Hit)
		}
		if d < min {
			min = d
		}
	}
	return min, true
}

// plane says the point stands on plain ground: its nearest floor is ground, not water, steep, lava or slime, within
// regenGroundZtolYd of its z.
func (r *regen) plane(p dungeon.Point) (navmesh.Floor, bool, error) {
	fl, err := r.nav.Floors(r.mapID, p.X, p.Y, p.Z-regenFloorWindowYd, p.Z+regenFloorWindowYd)
	if err != nil {
		return navmesh.Floor{}, false, err
	}
	best, ok := regenNearestFloor(fl, p.Z)
	if !ok || best.Flags&navmesh.NavGround == 0 || best.Flags&(navmesh.NavWater|navmesh.NavSteep|navmesh.NavMagma|navmesh.NavSlime) != 0 {
		return navmesh.Floor{}, false, nil
	}
	if math.Abs(float64(best.Z-p.Z)) > regenGroundZtolYd {
		return navmesh.Floor{}, false, nil
	}
	return best, true, nil
}

// ledges lists the floors within regenLedgeNearYd that break the ledge rule: more than regenLedgeDropYd below, or
// regenCeilLowYd-regenCeilHighYd above or below.
func (r *regen) ledges(p dungeon.Point) (bool, error) {
	for k := 0; k < 8; k++ {
		a := 2 * math.Pi * float64(k) / 8
		x, y := p.X+float32(regenLedgeNearYd*math.Cos(a)), p.Y+float32(regenLedgeNearYd*math.Sin(a))
		fl, err := r.nav.Floors(r.mapID, x, y, p.Z-regenCeilHighYd-1, p.Z+regenCeilHighYd+1)
		if err != nil {
			return false, err
		}
		for _, f := range fl {
			d := f.Z - p.Z
			if float64(d) < -regenCeilHighYd && float64(d) < -regenLedgeDropYd {
				return true, nil
			}
			if d >= regenCeilLowYd && d <= regenCeilHighYd {
				return true, nil
			}
			if d <= -regenCeilLowYd && d >= -regenCeilHighYd {
				return true, nil
			}
		}
	}
	return false, nil
}

// checkPoint says the point passes the placement rules (floor, water, edge, ledge, aggro + 8, patrol + 5).
func (r *regen) checkPoint(p dungeon.Point, stand bool, pulled map[int]bool, self int, lanes []regenLane, spawns []regenSpawn) (bool, string, error) {
	if _, ok, err := r.plane(p); err != nil {
		return false, "", err
	} else if !ok {
		return false, "not on plain ground (water, a steep slope or off the floor)", nil
	}
	wall, known := r.clearance(p)
	if !known {
		return false, "the navmesh cannot answer at the point", nil
	}
	want := regenWallYd
	if stand {
		want = regenStandWallYd
	}
	if wall < want {
		return false, fmt.Sprintf("a wall is %.1f yd away (the rule is %g yd)", wall, want), nil
	}
	ledge, err := r.ledges(p)
	if err != nil {
		return false, "", err
	}
	if ledge {
		return false, "a floor over 3 yd below or 4.5-8 yd above or below within 4.5 yd (a ledge)", nil
	}
	aggro, _, patrol, _ := r.margins(p, pulled, self, lanes, spawns)
	if aggro < regenAggroPointYd {
		return false, fmt.Sprintf("%.1f yd outside an unpulled unit's aggro (the point rule is %g)", aggro, regenAggroPointYd), nil
	}
	if patrol < regenPatrolYd {
		// A lane of a pack this leg pulls is a pull order matter (§3: "or a pull row for the patroller before the point is
		// used"): the order puts that pack first where it can; a lane no row pulls is a hard stop.
		owner := r.worstLaneOwner(p, pulled, self, lanes)
		if owner == -1 {
			return false, fmt.Sprintf("%.1f yd outside a patrol lane no row pulls (the rule is %g)", patrol, regenPatrolYd), nil
		}
	}
	return true, "", nil
}

// ownerNear is the owner of an unpulled pull group with a spawn within 45 yd of p (the pack a stand there would stand in).
func (r *regen) ownerNear(p dungeon.Point, pulled map[int]bool, self int, spawns []regenSpawn) (int, bool) {
	best := -1
	bestD := 45.0
	for _, sp := range spawns {
		if sp.owner < 0 || (self != -1 && sp.owner == self) || pulled[sp.owner] || r.laterOwner(sp.owner) {
			continue
		}
		if d := regenDist3(p, sp.sp.Pos); d < bestD {
			best, bestD = sp.owner, d
		}
	}
	return best, best >= 0
}

// oldApproachOK says a group's written approach still passes the required rules: every point dry plain ground, the stand out
// of every other unpulled unit's aggro + 8.
func (r *regen) oldApproachOK(g *dungeon.PullGroup, pulled map[int]bool, self int, lanes []regenLane, spawns []regenSpawn) bool {
	if len(g.Approach) == 0 {
		return false
	}
	p0 := dungeon.Point{}
	prev := p0
	for i, p := range g.Approach {
		if ok, _, err := r.routePointOK(p); err != nil || !ok {
			return false
		}
		if i > 0 && regenDist3(prev, p) > regenMaxHopWarn {
			return false // the exporter would cut the hop, and its split rows are the water and off-floor ones
		}
		prev = p
		if i == len(g.Approach)-1 {
			if ok, _, err := r.checkStandLoose(p, pulled, self, lanes, spawns); err != nil || !ok {
				return false
			}
		}
	}
	return true
}

// checkStandLoose is the last resort for a stand of a pack in a corridor too narrow for the wall or ledge rule: plain
// ground, out of every other unpulled unit's aggro + 8, and no lane that no row pulls.
func (r *regen) checkStandLoose(p dungeon.Point, pulled map[int]bool, self int, lanes []regenLane, spawns []regenSpawn) (bool, string, error) {
	if _, ok, err := r.plane(p); err != nil {
		return false, "", err
	} else if !ok {
		return false, "not on plain ground (water, a steep slope or off the floor)", nil
	}
	aggro, _, patrol, _ := r.margins(p, pulled, self, lanes, spawns)
	if aggro < regenAggroPointYd {
		return false, fmt.Sprintf("%.1f yd outside an unpulled unit's aggro (the point rule is %g)", aggro, regenAggroPointYd), nil
	}
	if patrol < regenPatrolYd && r.worstLaneOwner(p, pulled, self, lanes) == -1 {
		return false, fmt.Sprintf("%.1f yd outside a patrol lane no row pulls (the rule is %g)", patrol, regenPatrolYd), nil
	}
	return true, "", nil
}

// worstLaneOwner is the owner of the lane a point is worst inside (-1: no row pulls it), -2 when no lane is close.
func (r *regen) worstLaneOwner(p dungeon.Point, pulled map[int]bool, self int, lanes []regenLane) int {
	worst := -2
	worstM := regenPatrolYd
	for _, l := range lanes {
		if (self != -1 && l.owner == self) || (l.owner != -1 && pulled[l.owner]) {
			continue
		}
		if m := regenLoopDist(p, l.path) - l.reach; m < worstM {
			worst, worstM = l.owner, m
		}
	}
	return worst
}

// routePointOK says a route point (not a stand) is dry plain ground with the mesh able to answer. The wall and ledge
// preferences are the push's job: a descent along a ledge has no point within 2.5 yd of a wall or free of a drop, and an
// edge or ledge row there is a warning, not one of the brief's failures (water, off-floor, aggro).
func (r *regen) routePointOK(p dungeon.Point) (bool, string, error) {
	if _, ok, err := r.plane(p); err != nil {
		return false, "", err
	} else if !ok {
		return false, "not on plain ground (water, a steep slope or off the floor)", nil
	}
	if _, known := r.clearance(p); !known {
		return false, "the navmesh cannot answer at the point", nil
	}
	return true, "", nil
}

// placeAnchor keeps an anchor that passes, else moves it to the cheapest passing point within regenMaxMoveYd that has complete
// paths to its neighbours. It returns the point and a note when it moved (or gave up).
func (r *regen) placeAnchor(kind string, a dungeon.Point, prev, next *dungeon.Point, pulled map[int]bool, self int, stand bool,
	lanes []regenLane, spawns []regenSpawn) (dungeon.Point, string, error) {
	if ok, why, err := r.checkPoint(a, stand, pulled, self, lanes, spawns); err != nil {
		return a, "", err
	} else if ok {
		return a, "", nil
	} else {
		why0 := why
		best, found, err := r.searchAnchor(a, prev, next, pulled, self, stand, lanes, spawns)
		if err != nil {
			return a, "", err
		}
		if !found {
			return a, fmt.Sprintf("%s %.1f %.1f %.1f fails (%s) and no point within %g yd passes: for the lead", kind, a.X, a.Y, a.Z, why0, regenMaxMoveYd), nil
		}
		moved := regenDist3(a, best)
		return best, fmt.Sprintf("%s %.1f %.1f %.1f -> %.1f %.1f %.1f (%.1f yd; was: %s)", kind,
			a.X, a.Y, a.Z, best.X, best.Y, best.Z, moved, why0), nil
	}
}

// searchAnchor looks for the cheapest passing point near a: 3 yd rings, 16 directions, within regenMaxMoveYd, complete paths to
// both neighbours. Cost = displacement + 0.3 x extra path length - margins.
func (r *regen) searchAnchor(a dungeon.Point, prev, next *dungeon.Point, pulled map[int]bool, self int, stand bool,
	lanes []regenLane, spawns []regenSpawn) (dungeon.Point, bool, error) {
	zs := []float32{a.Z}
	if prev != nil && math.Abs(float64(prev.Z-a.Z)) < 12 {
		zs = append(zs, prev.Z)
	}
	if next != nil && math.Abs(float64(next.Z-a.Z)) < 12 {
		zs = append(zs, next.Z)
	}
	// A point over a drop or off a ledge has no floor at its own z: try the floors a body could land on there too.
	if fl, err := r.nav.Floors(r.mapID, a.X, a.Y, a.Z-25, a.Z+25); err == nil {
		for _, f := range fl {
			if math.Abs(float64(f.Z-a.Z)) > 25 {
				continue
			}
			zs = append(zs, f.Z)
		}
		if len(fl) == 0 {
			for _, dz := range []float32{-5, 5, -10, 10, -15, 15, -20, 20} {
				zs = append(zs, a.Z+dz)
			}
		}
	}
	var base float64
	if prev != nil && next != nil {
		if l, ok := r.pathLen(*prev, a); ok {
			base += l
		}
		if l, ok := r.pathLen(a, *next); ok {
			base += l
		}
	}
	bestCost := math.Inf(1)
	var best dungeon.Point
	found := false
	for rad := 0.0; rad <= regenMaxMoveYd+0.01; rad += 3 {
		for k := 0; k < 16; k++ {
			ang := 2 * math.Pi * float64(k) / 16
			for _, z := range zs {
				q := navmesh.Vec3{a.X + float32(rad*math.Cos(ang)), a.Y + float32(rad*math.Sin(ang)), z}
				c, ok, err := r.snap(q)
				if err != nil {
					return dungeon.Point{}, false, err
				}
				if !ok {
					continue
				}
				d := regenDist3(a, c)
				if d > regenMaxMoveYd {
					continue
				}
				if ok, _, err := r.checkPoint(c, stand, pulled, self, lanes, spawns); err != nil {
					return dungeon.Point{}, false, err
				} else if !ok {
					continue
				}
				extra := 0.0
				keep := true
				if prev != nil {
					l, ok := r.pathLen(*prev, c)
					if !ok {
						keep = false
					}
					extra += l
				}
				if next != nil {
					l, ok := r.pathLen(c, *next)
					if !ok {
						keep = false
					}
					extra += l
				}
				if !keep {
					continue
				}
				aggro, wall, patrol, _ := r.margins(c, pulled, self, lanes, spawns)
				cost := d + 0.3*(extra-base) - 0.25*math.Min(aggro, 12) - 0.25*math.Min(patrol, 12) - 0.3*math.Min(wall, 8)
				if cost < bestCost {
					bestCost, best, found = cost, c, true
				}
			}
		}
		if found && rad >= 9 {
			break
		}
	}
	return best, found, nil
}

// snap puts a point on the mesh: Nearest where it can, else the nearest plain ground within 20 yd of the point's z at the same
// x and y (a walk-log frame off a ledge has no walkable polygon within Nearest's 6 yd of it).
func (r *regen) snap(q navmesh.Vec3) (dungeon.Point, bool, error) {
	if n, ok, err := r.nav.Nearest(r.mapID, q); err != nil || ok {
		return dungeon.Point{X: n[0], Y: n[1], Z: n[2]}, ok, err
	}
	fl, err := r.nav.Floors(r.mapID, q[0], q[1], q[2]-20, q[2]+20)
	if err != nil {
		return dungeon.Point{}, false, err
	}
	for _, f := range fl {
		if f.Flags&navmesh.NavGround != 0 && f.Flags&(navmesh.NavWater|navmesh.NavSteep|navmesh.NavMagma|navmesh.NavSlime) == 0 {
			return dungeon.Point{X: q[0], Y: q[1], Z: f.Z}, true, nil
		}
	}
	return dungeon.Point{}, false, nil
}

// estimateStand finds a stand for a group that has none: 20-30 yd from its nearest unit, out of every other unpulled unit's
// aggro + 8 and every lane + 5, on plain ground with a stand's clearance, with a clear ray to the pack's centre.
func (r *regen) estimateStand(centre, from dungeon.Point, pulled map[int]bool, self int, lanes []regenLane, spawns []regenSpawn) (dungeon.Point, error) {
	var why string
	for _, mode := range []int{2, 1, 0} { // stand clearance, point clearance, then floor and aggro alone
		bestCost := math.Inf(1)
		var best dungeon.Point
		for rad := 0.0; rad <= 30; rad += 3 {
			for k := 0; k < 16; k++ {
				ang := 2 * math.Pi * float64(k) / 16
				q := navmesh.Vec3{centre.X + float32(rad*math.Cos(ang)), centre.Y + float32(rad*math.Sin(ang)), centre.Z}
				c, ok, err := r.snap(q)
				if err != nil {
					return dungeon.Point{}, err
				}
				if !ok {
					continue
				}
				var pass bool
				var w string
				var cerr error
				if mode == 2 {
					pass, w, cerr = r.checkPoint(c, true, pulled, self, lanes, spawns)
				} else if mode == 1 {
					pass, w, cerr = r.checkPoint(c, false, pulled, self, lanes, spawns)
				} else {
					pass, w, cerr = r.checkStandLoose(c, pulled, self, lanes, spawns)
				}
				if cerr != nil {
					return dungeon.Point{}, cerr
				} else if !pass {
					why = w
					continue
				}
				if ray, err := r.nav.RaycastNoWater(r.mapID, navmesh.Vec3{c.X, c.Y, c.Z}, navmesh.Vec3{centre.X, centre.Y, centre.Z}); err != nil || !ray.Clear {
					continue
				}
				l, ok := r.pathLen(from, c)
				if !ok {
					continue
				}
				cost := math.Abs(rad-22) + 0.3*(l-regenDist3(from, c))
				if cost < bestCost {
					bestCost, best = cost, c
				}
			}
			if bestCost <= 3 {
				break
			}
		}
		if bestCost < math.Inf(1) {
			return best, nil
		}
	}
	return dungeon.Point{}, fmt.Errorf("no stand within 30 yd of %.1f %.1f %.1f passes (%s)", centre.X, centre.Y, centre.Z, why)
}

// pathLen is a complete navmesh path's length over plain ground (water left out), ok false when there is no complete path.
func (r *regen) pathLen(from, to dungeon.Point) (float64, bool) {
	path, err := r.nav.PlanNoWater(r.mapID, navmesh.Vec3{from.X, from.Y, from.Z}, navmesh.Vec3{to.X, to.Y, to.Z})
	if err != nil || path.Status != navmesh.Complete {
		return 0, false
	}
	return float64(path.Length), true
}

// route plans one hop (an anchor pair) round the packs not pulled yet, the patrol lanes and water, and returns the authored
// points between from and to (not including from; the last is to). The plan walks the plain mesh; where its shortest path
// comes within aggro + 5 of an unpulled unit or patrol + 5 of a lane, an evasive point outside that reach is put in and the
// path replanned from it (SetAvoid's 25 % polygon rule marks whole corridors on this map: the goal's polygon itself is
// partly inside a box, so the path is warped instead). Water is a plain-mesh fallback: a crossing stays one hop between dry
// banks. maxPts bounds the points when the caller writes a pull group's or boss's approach (at most dungeon.MaxApproach, the
// stand included); 0 leaves it open (the leg's via).
func (r *regen) route(from, to dungeon.Point, pulled map[int]bool, self, maxPts int, lanes []regenLane, spawns []regenSpawn) ([]dungeon.Point, error) {
	anchors := []dungeon.Point{from}
	var legs []navmesh.Path
	var wet []bool
	for iter := 0; ; iter++ {
		cur := anchors[len(anchors)-1]
		path, isWet, err := r.planHop(cur, to)
		if err != nil {
			return nil, err
		}
		bad, at, off, need := r.firstViolation(path.Points, pulled, self, lanes, spawns)
		if !bad {
			legs = append(legs, path)
			wet = append(wet, isWet)
			break
		}
		if iter >= 6 {
			msg := fmt.Sprintf("the walk from %.1f %.1f %.1f to %.1f %.1f %.1f still comes inside a reach at %.1f %.1f %.1f after 6 detours (%s)",
				from.X, from.Y, from.Z, to.X, to.Y, to.Z, at.X, at.Y, at.Z, r.offenderAt(at, pulled, self, lanes, spawns))
			if owner := r.ownerAt(at, pulled, self, spawns); owner >= 0 {
				return nil, &regenReachErr{owner: owner, msg: msg}
			}
			return nil, fmt.Errorf("%s", msg)
		}
		d, ok := r.evasive(at, off, need, cur, to, pulled, self, lanes, spawns)
		if !ok {
			return nil, fmt.Errorf("no detour point outside the reach %.1f yd at %.1f %.1f (%.1f yd short)", need, off.X, off.Y, at)
		}
		seg, segWet, err := r.planHop(cur, d)
		if err != nil {
			return nil, fmt.Errorf("the detour to %.1f %.1f %.1f: %w", d.X, d.Y, d.Z, err)
		}
		legs = append(legs, seg)
		wet = append(wet, segWet)
		anchors = append(anchors, d)
	}
	// Join the legs, string-pull each with clearance (a water crossing keeps its hop), push the kept corners off the wall.
	var pts []dungeon.Point
	for i, path := range legs {
		kept := r.stringPull(path.Points, anchors[i], wet[i], pulled, self, lanes, spawns)
		for _, p := range kept[1:] {
			pts = append(pts, p)
		}
	}
	pts = append(pts, to)
	// Two adjacent points from different legs (or either side of a snap) can lose sight of each other: drop one of them and
	// let the merged hop take its chance (the final check keeps a hop over 45 yd to the reach rules).
	for changed := true; changed; {
		changed = false
		for i := 1; i < len(pts); i++ {
			a, b := pts[i-1], pts[i]
			if regenDist3(a, b) > regenMaxHopYd {
				continue
			}
			if r.canSkipSeg(navmesh.Vec3{a.X, a.Y, a.Z}, navmesh.Vec3{b.X, b.Y, b.Z}, false, pulled, self, lanes, spawns) {
				continue
			}
			if i < len(pts)-1 {
				pts = append(pts[:i], pts[i+1:]...)
			} else if i-1 > 0 {
				pts = append(pts[:i-1], pts[i:]...)
			} else {
				continue
			}
			changed = true
			break
		}
	}
	// A Detour corner can stand over water or off the floor (the mesh's own detail): put it on the nearest dry point,
	// and drop it when there is none (the hop keeps its reach check).
	var dry []dungeon.Point
	for i, p := range pts {
		if i == len(pts)-1 {
			dry = append(dry, p)
			continue
		}
		if ok, _, err := r.routePointOK(p); err != nil {
			return nil, err
		} else if ok {
			dry = append(dry, p)
			continue
		}
		q, ok := r.dryNear(p, pulled, self, lanes, spawns)
		if !ok {
			continue // no dry point near it: the hop over it is left to the spacing check
		}
		// A move across a wall would break the walk: keep the corner only if both neighbours still see it.
		conn := true
		if len(dry) > 0 && !r.canSkipSeg(navmesh.Vec3{dry[len(dry)-1].X, dry[len(dry)-1].Y, dry[len(dry)-1].Z}, navmesh.Vec3{q.X, q.Y, q.Z}, false, pulled, self, lanes, spawns) {
			conn = false
		}
		if n := pts[i+1]; !r.canSkipSeg(navmesh.Vec3{q.X, q.Y, q.Z}, navmesh.Vec3{n.X, n.Y, n.Z}, false, pulled, self, lanes, spawns) {
			conn = false
		}
		if conn {
			dry = append(dry, q)
		}
	}
	pts = dry
	// Space the points 8-45 yd apart, splitting only when there is room for the point (maxPts).
	var spaced []dungeon.Point
	prev := from
	for i, p := range pts {
		if i < len(pts)-1 && regenDist3(prev, p) < regenMinHopYd {
			continue
		}
		d := regenDist3(prev, p)
		if d > regenMaxHopYd && (maxPts == 0 || len(spaced)+1+1 <= maxPts) {
			if part, ok := r.split(prev, p); ok {
				pass, _, err := r.routePointOK(part)
				va, vq, vb := navmesh.Vec3{prev.X, prev.Y, prev.Z}, navmesh.Vec3{part.X, part.Y, part.Z}, navmesh.Vec3{p.X, p.Y, p.Z}
				if err == nil && pass {
					if regenDist3(prev, p) > regenMaxHopWarn || regenWetLine(r, prev, p) {
						// A crossing split stands on the bank: the water between the banks is the walk itself.
						if regenDist3(prev, part) <= regenMaxHopLongYd && regenDist3(part, p) <= regenMaxHopLongYd &&
							r.reachWhy(prev, part, pulled, self, lanes, spawns) == "sample? none" &&
							r.reachWhy(part, p, pulled, self, lanes, spawns) == "sample? none" {
							spaced = append(spaced, part)
						}
					} else if r.canSkipSeg(va, vq, false, pulled, self, lanes, spawns) && r.canSkipSeg(vq, vb, false, pulled, self, lanes, spawns) {
						spaced = append(spaced, part)
					}
				}
			}
		}
		spaced = append(spaced, p)
		prev = p
	}
	// The last point is the anchor itself; drop everything that would sit on top of it.
	for len(spaced) > 1 && regenDist3(spaced[len(spaced)-2], to) < regenMinHopYd {
		spaced = append(spaced[:len(spaced)-2], spaced[len(spaced)-1:]...)
	}
	if len(spaced) == 0 || spaced[len(spaced)-1] != to {
		spaced = append(spaced, to)
	}
	// Reduce to maxPts by dropping interior points the rest of the route can skip.
	for maxPts > 0 && len(spaced) > maxPts {
		dropped := false
		for i := len(spaced) - 2; i >= 0 && len(spaced) > maxPts; i-- {
			best := -1
			for j := i - 1; j >= -1; j-- {
				a := from
				if j >= 0 {
					a = spaced[j]
				}
				if regenDist3(a, spaced[i+1]) <= regenMaxHopLongYd && r.reachWhy(a, spaced[i+1], pulled, self, lanes, spawns) == "sample? none" {
					best = i
					break
				}
			}
			if best >= 0 {
				spaced = append(spaced[:best], spaced[best+1:]...)
				dropped = true
			}
		}
		if !dropped {
			break
		}
	}
	if maxPts > 0 && len(spaced) > maxPts {
		return nil, fmt.Errorf("%d points needed (at most %d) from %.1f %.1f %.1f: %v", len(spaced), maxPts, from.X, from.Y, from.Z, spaced)
	}
	// Two written points can still lose sight of each other after a split or a snap: drop one of them (the final check
	// keeps a hop over 45 yd to the reach rules).
	for changed := true; changed; {
		changed = false
		chain := append([]dungeon.Point{from}, spaced...)
		for i := 1; i < len(chain); i++ {
			a, b := chain[i-1], chain[i]
			_ = a
			_ = b
			if regenDist3(a, b) > regenMaxHopWarn {
				continue
			}
			if r.canSkipSeg(navmesh.Vec3{a.X, a.Y, a.Z}, navmesh.Vec3{b.X, b.Y, b.Z}, false, pulled, self, lanes, spawns) {
				continue
			}
			// i is the index in chain: 1 is spaced[0].
			if i < len(chain)-1 {
				spaced = append(spaced[:i-1], spaced[i:]...)
			} else if i-1 >= 1 {
				spaced = append(spaced[:i-2], spaced[i-1:]...)
			} else {
				continue
			}
			changed = true
			break
		}
	}
	// Check every point, and every hop's clearance, again.
	prev = from
	for idx, p := range spaced {
		var ok bool
		var why string
		var err error
		if p == to && maxPts > 0 {
			ok, why, err = r.checkPoint(p, true, pulled, self, lanes, spawns)
		} else {
			ok, why, err = r.routePointOK(p)
		}
		if err != nil {
			return nil, err
		} else if !ok {
			return nil, fmt.Errorf("the route point %.1f %.1f %.1f (index %d of %d, from %.1f %.1f %.1f) fails: %s", p.X, p.Y, p.Z, idx, len(spaced), from.X, from.Y, from.Z, why)
		}
		long := regenDist3(prev, p) > regenMaxHopYd
		if long {
			// A descent the split cannot cut: one hop, and its samples still keep every reach.
			if r.reachWhy(prev, p, pulled, self, lanes, spawns) != "sample? none" {
				msg := fmt.Sprintf("the %.0f yd hop from %.1f %.1f %.1f to %.1f %.1f %.1f crosses a reach: %s",
					regenDist3(prev, p), prev.X, prev.Y, prev.Z, p.X, p.Y, p.Z, r.reachWhy(prev, p, pulled, self, lanes, spawns))
				if owner := r.ownerOnHop(prev, p, pulled, self, spawns); owner >= 0 {
					return nil, &regenReachErr{owner: owner, msg: msg}
				}
				return nil, fmt.Errorf("%s", msg)
			}
			prev = p
			continue
		}
		if !r.hopClear(prev, p, pulled, self, lanes, spawns) {
			va, vb := navmesh.Vec3{prev.X, prev.Y, prev.Z}, navmesh.Vec3{p.X, p.Y, p.Z}
			rayDry, _ := r.nav.RaycastNoWater(r.mapID, va, vb)
			rayWet, _ := r.nav.Raycast(r.mapID, va, vb)
			return nil, fmt.Errorf("the hop %.0f yd from %.1f %.1f %.1f to %.1f %.1f %.1f is not clear (%s; ground ray clear %v hit %.1f; plain ray clear %v hit %.1f); points %v",
				regenDist3(prev, p), prev.X, prev.Y, prev.Z, p.X, p.Y, p.Z, r.reachWhy(prev, p, pulled, self, lanes, spawns), rayDry.Clear, rayDry.Hit, rayWet.Clear, rayWet.Hit, spaced)
		}
		prev = p
	}
	return spaced, nil
}

// planHop plans a leg of a route: plain ground first (water left out), else the plain mesh (a crossing is then one hop).
func (r *regen) planHop(from, to dungeon.Point) (navmesh.Path, bool, error) {
	a, b := navmesh.Vec3{from.X, from.Y, from.Z}, navmesh.Vec3{to.X, to.Y, to.Z}
	if path, err := r.nav.PlanNoWater(r.mapID, a, b); err != nil {
		return navmesh.Path{}, false, err
	} else if path.Status == navmesh.Complete {
		return path, false, nil
	}
	path, err := r.nav.Plan(r.mapID, a, b)
	if err != nil {
		return navmesh.Path{}, false, err
	}
	if path.Status != navmesh.Complete {
		return navmesh.Path{}, false, fmt.Errorf("no complete navmesh path from %.1f %.1f %.1f to %.1f %.1f %.1f (%s)", from.X, from.Y, from.Z, to.X, to.Y, to.Z, path.Reason)
	}
	return path, true, nil
}

// firstViolation samples a path every regenMaxSegSamples yards and returns the first sample inside an unpulled unit's aggro + 5
// (or a lane's reach + 5): the sample, the offender's point, and how much of the reach it is short by.
func (r *regen) firstViolation(pts []navmesh.Vec3, pulled map[int]bool, self int, lanes []regenLane, spawns []regenSpawn) (bool, dungeon.Point, dungeon.Point, float64) {
	for i := 1; i < len(pts); i++ {
		a := dungeon.Point{X: pts[i-1][0], Y: pts[i-1][1], Z: pts[i-1][2]}
		b := dungeon.Point{X: pts[i][0], Y: pts[i][1], Z: pts[i][2]}
		n := int(math.Ceil(regenDist3(a, b) / regenMaxSegSamples))
		if n < 1 {
			n = 1
		}
		for k := 0; k <= n; k++ {
			f := float32(k) / float32(n)
			p := dungeon.Point{X: a.X + (b.X-a.X)*f, Y: a.Y + (b.Y-a.Y)*f, Z: a.Z + (b.Z-a.Z)*f}
			if off, need, ok := r.insideReach(p, pulled, self, lanes, spawns); ok {
				return true, p, off, need
			}
		}
	}
	return false, dungeon.Point{}, dungeon.Point{}, 0
}

// insideReach says p is inside an unpulled unit's aggro + 5 or a lane's reach + 5, with the offender's nearest point and the
// yards the sample is short of the reach.
func (r *regen) insideReach(p dungeon.Point, pulled map[int]bool, self int, lanes []regenLane, spawns []regenSpawn) (dungeon.Point, float64, bool) {
	var off dungeon.Point
	need := 0.0
	found := false
	for _, s := range spawns {
		if (self != -1 && s.owner == self) || (s.owner != -1 && pulled[s.owner]) || r.laterOwner(s.owner) {
			continue
		}
		d := regenDist3(p, s.sp.Pos) - s.reach
		if d < regenAggroSegYd && (!found || regenAggroSegYd-d > need) {
			off, need, found = s.sp.Pos, regenAggroSegYd-d, true
		}
	}
	for _, l := range lanes {
		if (self != -1 && l.owner == self) || (l.owner != -1 && pulled[l.owner]) || r.laterOwner(l.owner) {
			continue
		}
		q := regenLoopNearest(p, l.path)
		d := regenDist3(p, q) - l.reach
		if d < regenPatrolYd && (!found || regenPatrolYd-d > need) {
			off, need, found = q, regenPatrolYd-d, true
		}
	}
	return off, need, found
}

// evasive is a point outside the offender's reach at p: pushed out radially past the reach, with a fan either side of the
// radial line, the one nearest the walk on to to, on plain ground with a point's clearance.
func (r *regen) evasive(p, off dungeon.Point, need float64, cur, to dungeon.Point, pulled map[int]bool, self int, lanes []regenLane, spawns []regenSpawn) (dungeon.Point, bool) {
	dx, dy := float64(p.X-off.X), float64(p.Y-off.Y)
	if math.Hypot(dx, dy) < 0.5 {
		dx, dy = float64(to.X-p.X), float64(to.Y-p.Y)
	}
	base := math.Atan2(dy, dx)
	// The direction the walk is going: a detour must make progress along it, or the same point would be added again.
	fx, fy := float64(to.X-cur.X), float64(to.Y-cur.Y)
	fl := math.Hypot(fx, fy)
	if fl > 0 {
		fx, fy = fx/fl, fy/fl
	}
	along := func(c dungeon.Point) float64 {
		return float64(c.X-cur.X)*fx + float64(c.Y-cur.Y)*fy
	}
	pAlong := along(p)
	var best dungeon.Point
	bestCost := math.Inf(1)
	bestAny := math.Inf(-1)
	var anyPoint dungeon.Point
	haveAny := false
	for _, extra := range []float64{4, 8, 13} {
		rad := regenDist3(p, off) + need + extra
		for _, turn := range []float64{0, 0.4, -0.4, 0.8, -0.8, 1.2, -1.2, 1.7, -1.7, 2.2, -2.2} {
			a := base + turn
			q := navmesh.Vec3{off.X + float32(rad*math.Cos(a)), off.Y + float32(rad*math.Sin(a)), p.Z}
			c, ok, err := r.snap(q)
			if err != nil || !ok {
				continue
			}
			if regenDist3(p, c) > 60 {
				continue
			}
			if ok, _, err := r.routePointOK(c); err != nil || !ok {
				continue
			}
			if _, _, bad := r.insideReach(c, pulled, self, lanes, spawns); bad {
				continue
			}
			if a := along(c); a > bestAny {
				anyPoint, bestAny, haveAny = c, a, true
			}
			if along(c) <= pAlong+1 {
				continue
			}
			cost := regenDist3(p, c) + 0.2*regenDist3(c, to) + 8*math.Abs(turn)
			if cost < bestCost {
				best, bestCost = c, cost
			}
		}
	}
	if bestCost < math.Inf(1) {
		return best, true
	}
	if haveAny {
		return anyPoint, true
	}
	return dungeon.Point{}, false
}

// stringPull drops the corners of a planned leg a straight walk can skip: the segment is clear (a water leg's ray may cross a
// lake), no sample comes inside a reach, and the kept corners are pushed off the wall.
func (r *regen) stringPull(pts []navmesh.Vec3, anchor dungeon.Point, wet bool, pulled map[int]bool, self int, lanes []regenLane, spawns []regenSpawn) []dungeon.Point {
	if len(pts) == 0 {
		return nil
	}
	point := func(v navmesh.Vec3) dungeon.Point { return dungeon.Point{X: v[0], Y: v[1], Z: v[2]} }
	var kept []int
	i := 0
	for i < len(pts)-1 {
		j := len(pts) - 1
		for j > i+1 && !r.canSkipSeg(pts[i], pts[j], wet, pulled, self, lanes, spawns) {
			j--
		}
		kept = append(kept, i)
		i = j
	}
	kept = append(kept, len(pts)-1)
	var out []dungeon.Point
	out = append(out, anchor)
	// Detour's path starts on the floor nearest the anchor, not on the anchor: that point is a waypoint of its own when the
	// two differ, or the hop from the anchor to the first corner could cut a wall the floor point stands clear of.
	if start := point(pts[0]); regenDist3(anchor, start) > 0.1 {
		out = append(out, start)
	}
	for k := 1; k < len(kept)-1; k++ {
		idx := kept[k]
		a, b, c := pts[kept[k-1]], pts[idx], pts[kept[k+1]]
		if regenTurn(point(a), point(b), point(c)) < 20 && r.canSkipSeg(a, c, wet, pulled, self, lanes, spawns) {
			continue // a turn a straight walk can skip
		}
		// The push is off for now: a corner moved 3 yd off the wall can lose sight of a neighbour the mesh corner saw, and
		// every such chain has to be re-planned. The mesh corner keeps the walk the navmesh gave.
		pushed := point(b)
		va, vc := a, c
		vp := navmesh.Vec3{pushed.X, pushed.Y, pushed.Z}
		prevOut := out[len(out)-1]
		vo := navmesh.Vec3{prevOut.X, prevOut.Y, prevOut.Z}
		if pushed != point(b) {
			if !r.canSkipSeg(va, vp, wet, pulled, self, lanes, spawns) || !r.canSkipSeg(vp, vc, wet, pulled, self, lanes, spawns) {
				pushed, vp = point(b), navmesh.Vec3{b[0], b[1], b[2]} // the push broke sight: the mesh corner stands
			}
		}
		if !r.canSkipSeg(vo, vp, wet, pulled, self, lanes, spawns) {
			pushed, vp = point(b), va
			if !r.canSkipSeg(vo, vp, wet, pulled, self, lanes, spawns) {
				out = append(out, pushed)
				continue
			}
		}
		out = append(out, pushed)
	}
	out = append(out, point(pts[len(pts)-1]))
	return out
}

// canSkipSeg is a string-pull's test of a straight walk: a clear ray (the ground-only ray, or any walkable ray for a water
// leg), and no sample inside a reach.
func (r *regen) canSkipSeg(a, b navmesh.Vec3, wet bool, pulled map[int]bool, self int, lanes []regenLane, spawns []regenSpawn) bool {
	var ray navmesh.Ray
	var err error
	if wet {
		ray, err = r.nav.Raycast(r.mapID, a, b)
	} else {
		ray, err = r.nav.RaycastNoWater(r.mapID, a, b)
	}
	if err != nil || !ray.Known || !ray.Clear {
		return false
	}
	from := dungeon.Point{X: a[0], Y: a[1], Z: a[2]}
	to := dungeon.Point{X: b[0], Y: b[1], Z: b[2]}
	n := int(math.Ceil(regenDist3(from, to) / regenMaxSegSamples))
	if n < 1 {
		n = 1
	}
	for k := 0; k <= n; k++ {
		f := float32(k) / float32(n)
		p := dungeon.Point{X: from.X + (to.X-from.X)*f, Y: from.Y + (to.Y-from.Y)*f, Z: from.Z + (to.Z-from.Z)*f}
		if _, _, bad := r.insideReach(p, pulled, self, lanes, spawns); bad {
			return false
		}
	}
	return true
}

// laterOwner says the owner belongs to a leg after the one under regeneration.
func (r *regen) laterOwner(owner int) bool {
	if owner < 0 {
		return false
	}
	leg, ok := r.ownerLeg[owner]
	return ok && leg > r.leg
}

// ownerOnHop is the first unpulled spawn owner a straight hop comes inside aggro + 5 of.
func (r *regen) ownerOnHop(a, b dungeon.Point, pulled map[int]bool, self int, spawns []regenSpawn) int {
	n := int(math.Ceil(regenDist3(a, b) / regenMaxSegSamples))
	if n < 1 {
		n = 1
	}
	for k := 0; k <= n; k++ {
		f := float32(k) / float32(n)
		p := dungeon.Point{X: a.X + (b.X-a.X)*f, Y: a.Y + (b.Y-a.Y)*f, Z: a.Z + (b.Z-a.Z)*f}
		if owner := r.ownerAt(p, pulled, self, spawns); owner >= 0 {
			return owner
		}
	}
	return -1
}

// ownerAt is the owner of the spawn holding p inside its aggro + 5 (-1: none or a lane).
func (r *regen) ownerAt(p dungeon.Point, pulled map[int]bool, self int, spawns []regenSpawn) int {
	for _, sp := range spawns {
		if (self != -1 && sp.owner == self) || (sp.owner != -1 && pulled[sp.owner]) || r.laterOwner(sp.owner) {
			continue
		}
		if regenDist3(p, sp.sp.Pos)-sp.reach < regenAggroSegYd {
			return sp.owner
		}
	}
	return -1
}

// offenderAt names what holds a sample inside a reach.
func (r *regen) offenderAt(p dungeon.Point, pulled map[int]bool, self int, lanes []regenLane, spawns []regenSpawn) string {
	for _, sp := range spawns {
		if (self != -1 && sp.owner == self) || (sp.owner != -1 && pulled[sp.owner]) || r.laterOwner(sp.owner) {
			continue
		}
		if d := regenDist3(p, sp.sp.Pos) - sp.reach; d < regenAggroSegYd {
			return fmt.Sprintf("spawn %d %s owner %d margin %.1f", sp.sp.Entry, sp.sp.Name, sp.owner, d)
		}
	}
	for li, l := range lanes {
		if (self != -1 && l.owner == self) || (l.owner != -1 && pulled[l.owner]) || r.laterOwner(l.owner) {
			continue
		}
		if d := regenLoopDist(p, l.path) - l.reach; d < regenPatrolYd {
			return fmt.Sprintf("lane %d owner %d margin %.1f", li, l.owner, d)
		}
	}
	return "none"
}

// reachWhy names the first sample inside a reach on the hop a-b, for a route error.
func (r *regen) reachWhy(a, b dungeon.Point, pulled map[int]bool, self int, lanes []regenLane, spawns []regenSpawn) string {
	n := int(math.Ceil(regenDist3(a, b) / regenMaxSegSamples))
	if n < 1 {
		n = 1
	}
	for k := 0; k <= n; k++ {
		f := float32(k) / float32(n)
		p := dungeon.Point{X: a.X + (b.X-a.X)*f, Y: a.Y + (b.Y-a.Y)*f, Z: a.Z + (b.Z-a.Z)*f}
		for _, sp := range spawns {
			if (self != -1 && sp.owner == self) || (sp.owner != -1 && pulled[sp.owner]) || r.laterOwner(sp.owner) {
				continue
			}
			if d := regenDist3(p, sp.sp.Pos) - sp.reach; d < regenAggroSegYd {
				return fmt.Sprintf("spawn %d %s owner %d margin %.1f at %.1f %.1f", sp.sp.Entry, sp.sp.Name, sp.owner, d, p.X, p.Y)
			}
		}
		for li, l := range lanes {
			if (self != -1 && l.owner == self) || (l.owner != -1 && pulled[l.owner]) || r.laterOwner(l.owner) {
				continue
			}
			if d := regenLoopDist(p, l.path) - l.reach; d < regenPatrolYd {
				return fmt.Sprintf("lane %d owner %d margin %.1f at %.1f %.1f", li, l.owner, d, p.X, p.Y)
			}
		}
	}
	return "sample? none"
}

// dryNear puts a route point on plain ground near it (within 9 yd), out of a reach: rings of 3 yd, 16 directions.
func (r *regen) dryNear(p dungeon.Point, pulled map[int]bool, self int, lanes []regenLane, spawns []regenSpawn) (dungeon.Point, bool) {
	for rad := 3.0; rad <= 9; rad += 3 {
		for k := 0; k < 16; k++ {
			a := 2 * math.Pi * float64(k) / 16
			q := navmesh.Vec3{p.X + float32(rad*math.Cos(a)), p.Y + float32(rad*math.Sin(a)), p.Z}
			c, ok, err := r.snap(q)
			if err != nil || !ok {
				continue
			}
			if ok, _, err := r.routePointOK(c); err != nil || !ok {
				continue
			}
			if _, _, bad := r.insideReach(c, pulled, self, lanes, spawns); bad {
				continue
			}
			return c, true
		}
	}
	return dungeon.Point{}, false
}

// hopClear says a written hop keeps the clearance rules (a water crossing is left to the plain-mesh check, which the plan check
// lints as a water row).
func (r *regen) hopClear(a, b dungeon.Point, pulled map[int]bool, self int, lanes []regenLane, spawns []regenSpawn) bool {
	va, vb := navmesh.Vec3{a.X, a.Y, a.Z}, navmesh.Vec3{b.X, b.Y, b.Z}
	if r.canSkipSeg(va, vb, false, pulled, self, lanes, spawns) {
		return true
	}
	// A water crossing: the plain mesh joins the banks and no reach stands on the way.
	ray, err := r.nav.Raycast(r.mapID, va, vb)
	if err != nil || !ray.Known || !ray.Clear {
		return false
	}
	n := int(math.Ceil(regenDist3(a, b) / regenMaxSegSamples))
	if n < 1 {
		n = 1
	}
	for k := 0; k <= n; k++ {
		f := float32(k) / float32(n)
		p := dungeon.Point{X: a.X + (b.X-a.X)*f, Y: a.Y + (b.Y-a.Y)*f, Z: a.Z + (b.Z-a.Z)*f}
		if _, _, bad := r.insideReach(p, pulled, self, lanes, spawns); bad {
			return false
		}
	}
	return true
}

// pushCorner moves a kept path corner regenPushYd into the walkable side of its bisector, snaps it to plain ground and checks
// it; the original is kept when no push passes and improves the clearance.
func (r *regen) pushCorner(p dungeon.Point, prev, next navmesh.Vec3, pulled map[int]bool, self int,
	lanes []regenLane, spawns []regenSpawn) dungeon.Point {
	in := [2]float64{float64(prev[0] - p.X), float64(prev[1] - p.Y)}
	out := [2]float64{float64(next[0] - p.X), float64(next[1] - p.Y)}
	for i := range in {
		if n := math.Hypot(in[0], in[1]); n > 0 {
			in[i] /= n
		}
		if n := math.Hypot(out[0], out[1]); n > 0 {
			out[i] /= n
		}
	}
	bx, by := in[0]+out[0], in[1]+out[1]
	if n := math.Hypot(bx, by); n > 1e-6 {
		bx, by = bx/n, by/n
	} else {
		return p
	}
	base, ok := r.clearance(p)
	if !ok {
		return p
	}
	best, bestWall := p, base
	for _, rad := range []float64{regenPushYd, regenPushYd / 2, 4} {
		q := navmesh.Vec3{p.X + float32(rad*bx), p.Y + float32(rad*by), p.Z}
		c, ok, err := r.snap(q)
		if err != nil || !ok {
			continue
		}
		if math.Abs(float64(c.Z-p.Z)) > 3 || regenDist3(p, c) > 2*rad+1 {
			continue
		}
		if ok, _, _ := r.routePointOK(c); !ok {
			continue
		}
		if wall, ok := r.clearance(c); ok && wall > bestWall+0.2 {
			best, bestWall = c, wall
		}
	}
	return best
}

// split finds a point of the path between a and b at regenMaxHopYd; it must be plain ground within regenFillerZtolYd of the
// line's z at that place.
func (r *regen) split(a, b dungeon.Point) (dungeon.Point, bool) {
	path, err := r.nav.PlanNoWater(r.mapID, navmesh.Vec3{a.X, a.Y, a.Z}, navmesh.Vec3{b.X, b.Y, b.Z})
	if err != nil || path.Status != navmesh.Complete {
		path, err = r.nav.Plan(r.mapID, navmesh.Vec3{a.X, a.Y, a.Z}, navmesh.Vec3{b.X, b.Y, b.Z})
	}
	if err != nil || len(path.Points) < 2 {
		return dungeon.Point{}, false
	}
	want := regenMaxHopYd - 5
	if d := regenDist3(a, b); d > 2*want {
		want = d / 2 // long hops are split in the middle, so both halves stay under the limit
	}
	walked := 0.0
	prev := path.Points[0]
	for _, v := range path.Points[1:] {
		seg := navmesh.Vec3{v[0] - prev[0], v[1] - prev[1], v[2] - prev[2]}
		l := math.Sqrt(float64(seg[0]*seg[0] + seg[1]*seg[1] + seg[2]*seg[2]))
		if walked+l >= want {
			t := float32((want - walked) / l)
			q := navmesh.Vec3{prev[0] + seg[0]*t, prev[1] + seg[1]*t, prev[2] + seg[2]*t}
			cands := []navmesh.Vec3{q}
			if c, ok, err := r.nav.Nearest(r.mapID, q); err == nil && ok {
				cands = append(cands, c)
			}
			if c, ok, err := r.snap(q); err == nil && ok {
				cands = append(cands, navmesh.Vec3{c.X, c.Y, c.Z})
			}
			for _, cand := range cands {
				c := dungeon.Point{X: cand[0], Y: cand[1], Z: cand[2]}
				if ok, _, err := r.routePointOK(c); err == nil && ok {
					return c, true
				}
			}
			// A crossing: the split must stand on the bank, which the floor says plainly (no water, no bed).
			for _, v := range path.Points[1:] {
				c := dungeon.Point{X: v[0], Y: v[1], Z: v[2]}
				if ok, _, err := r.routePointOK(c); err == nil && ok {
					if d := regenDist3(a, c); d >= 8 && d <= regenMaxHopLongYd {
						return c, true
					}
				}
			}
		}
		walked += l
		prev = v
	}
	return dungeon.Point{}, false
}

// order orders the pull groups of a leg: start is walked first, and no pair is walked uphill (a path over twice its reverse)
// while a downhill order exists. It returns the new order (group indices) and the one-way pairs it flagged.
func (r *regen) order(targets []dungeon.Point, start dungeon.Point, end dungeon.Point, hasEnd bool) ([]int, []string) {
	n := len(targets)
	// The end (where the next leg starts) is a fixed last stop: the leg has to finish near it, or the next leg's first hop
	// is a long one the exporter cuts.
	stop := n
	total := n
	if hasEnd {
		total = n + 1
	}
	l := make([][]float64, total)
	for i := range l {
		l[i] = make([]float64, total)
	}
	at := func(i int) dungeon.Point {
		if hasEnd && i == stop {
			return end
		}
		return targets[i]
	}
	for i := range l {
		for j := range l[i] {
			if i == j {
				continue
			}
			if d, ok := r.pathLen(at(i), at(j)); ok {
				l[i][j] = d
			} else {
				l[i][j] = math.Inf(1)
			}
		}
	}
	from := make([]float64, total)
	for i := 0; i < total; i++ {
		if d, ok := r.pathLen(start, at(i)); ok {
			from[i] = d
		} else {
			from[i] = math.Inf(1)
		}
	}
	// A patrol lane that reaches a pack's stand ring is a pull for that patroller before the pack is pulled (§3: "or a pull row
	// for the patroller before the point is used").
	before := map[int]map[int]bool{}
	localOf := map[int]int{}
	for i, gr := range r.orderGroups {
		localOf[gr.id] = i
	}
	if hasEnd {
		if before[stop] == nil {
			before[stop] = map[int]bool{}
		}
		for i := 0; i < n; i++ {
			before[stop][i] = true
		}
	}
	for g, owners := range r.must {
		gi, ok := localOf[g]
		if !ok {
			continue
		}
		for owner := range owners {
			oi, ok := localOf[owner]
			if !ok {
				continue
			}
			if before[gi] == nil {
				before[gi] = map[int]bool{}
			}
			before[gi][oi] = true
		}
	}
	for _, l := range r.lanesForOrder {
		if l.owner < 0 {
			continue
		}
		ownerLocal := -1
		for i, gr := range r.orderGroups {
			if gr.id == l.owner {
				ownerLocal = i
				break
			}
		}
		if ownerLocal < 0 {
			continue
		}
		for g, target := range targets {
			if g == ownerLocal || regenLoopDist(target, l.path) >= l.reach+regenPatrolYd+regenMaxMoveYd-5 {
				continue
			}
			if before[g] == nil {
				before[g] = map[int]bool{}
			}
			before[g][ownerLocal] = true
		}
	}
	oneWays := regenOneWays(l)
	orig := make([]int, total)
	for i := range orig {
		orig[i] = i
	}
	cons, _ := regenOrder(l, from, before)
	cands := [][]int{regenNaiveOrder(l, from), orig, cons}
	bestOrder := orig
	bestCost := math.Inf(1)
	for _, cand := range cands {
		if len(cand) != total {
			continue // regenOrder's cycle breaker can stop early; a short order is no order
		}
		cand = regenTwoOpt(l, from, cand, before)
		cand = regenOrOpt(l, from, cand, before)
		cost := regenTourCost(l, from, cand)
		// An uphill hop is what the order exists to avoid: it costs more than any length.
		for i := 0; i+1 < len(cand); i++ {
			if uphill(l, cand[i], cand[i+1]) {
				cost += 1e6
			}
		}
		if cost < bestCost {
			bestOrder, bestCost = cand, cost
		}
	}
	order := bestOrder
	out := make([]int, 0, len(order))
	for _, v := range order {
		if hasEnd && v == stop {
			continue
		}
		out = append(out, v)
	}
	return out, oneWays
}

// regenOrder is order's pure part: l[i][j] is the path length between stops i and j (Infs for no path), from[i] the path from
// the start. A pair whose path is over regenOneWayRatio times its reverse is one-way, and the uphill direction is never
// scheduled while the downhill one is free.
func regenOrder(l [][]float64, from []float64, must map[int]map[int]bool) ([]int, []string) {
	n := len(l)
	if n == 0 {
		return nil, nil
	}
	// Constraints: uphill(i, j) means i -> j costs much more than j -> i, so j must be walked before i.
	before := make([]map[int]bool, n) // before[i]: the stops that must come before i
	hard := make([]map[int]bool, n)   // what the run learned (a pack in the way): kept when a cycle is broken
	for i := range before {
		before[i] = map[int]bool{}
		hard[i] = map[int]bool{}
		for j := range must[i] {
			if j < 0 || j >= n {
				continue
			}
			before[i][j] = true
			hard[i][j] = true
		}
	}
	var oneWays []string
	for i := 0; i < n; i++ {
		for j := 0; j < n; j++ {
			if i == j {
				continue
			}
			if !uphill(l, i, j) {
				continue
			}
			before[i][j] = true
			if i < j {
				oneWays = append(oneWays, fmt.Sprintf("%d->%d %.0f yd vs %.0f yd", i, j, l[i][j], l[j][i]))
			}
		}
	}
	done := make([]bool, n)
	order := make([]int, 0, n)
	for len(order) < n {
		costOf := func(i int) float64 {
			if len(order) == 0 {
				return from[i]
			}
			return l[order[len(order)-1]][i]
		}
		pick := func(edges []map[int]bool) (int, float64) {
			best, bestCost := -1, math.Inf(1)
			for i := 0; i < n; i++ {
				if done[i] {
					continue
				}
				blocked := false
				for b := range edges[i] {
					if !done[b] {
						blocked = true
						break
					}
				}
				if blocked {
					continue
				}
				if c := costOf(i); c < bestCost {
					best, bestCost = i, c
				}
			}
			return best, bestCost
		}
		best, bestCost := pick(before)
		if best < 0 {
			// A cycle: first drop the one-way edges only (the learned "clear this pack first" order is kept), and only then
			// the learned ones too.
			best, bestCost = pick(hard)
			if best < 0 {
				for i := 0; i < n && best < 0; i++ {
					if !done[i] {
						best, bestCost = i, 1
					}
				}
			}
			if best < 0 {
				break
			}
			for b := range before[best] {
				if !hard[best][b] {
					before[best][b] = false
				}
			}
			_ = bestCost
		}
		done[best] = true
		order = append(order, best)
	}
	return order, oneWays
}

// regenOneWays lists the pairs whose path is over regenOneWayRatio times its reverse.
func regenOneWays(l [][]float64) []string {
	var out []string
	for i := range l {
		for j := i + 1; j < len(l); j++ {
			if uphill(l, i, j) {
				out = append(out, fmt.Sprintf("%d->%d %.0f yd vs %.0f yd", i, j, l[i][j], l[j][i]))
			} else if uphill(l, j, i) {
				out = append(out, fmt.Sprintf("%d->%d %.0f yd vs %.0f yd", j, i, l[j][i], l[i][j]))
			}
		}
	}
	return out
}

// regenTourCost is the order's walked length from the start, one-way or not.
func regenTourCost(l [][]float64, from []float64, order []int) float64 {
	total := 0.0
	for i, v := range order {
		if i == 0 {
			total += from[v]
		} else {
			total += l[order[i-1]][v]
		}
	}
	return total
}

// regenOrOpt relocates one stop to a better place in the order (2-opt alone cannot undo a zigzag on a one-way matrix).
func regenOrOpt(l [][]float64, from []float64, order []int, must map[int]map[int]bool) []int {
	ok := func(o []int) bool {
		pos := make([]int, len(o))
		for k, v := range o {
			pos[v] = k
		}
		for i, bs := range must {
			for j := range bs {
				if j < 0 || j >= len(pos) || i < 0 || i >= len(pos) {
					continue
				}
				if pos[j] > pos[i] {
					return false
				}
			}
		}
		return true
	}
	best := regenTourCost(l, from, order)
	for pass := 0; pass < 4; pass++ {
		improved := false
		for i := range order {
			for j := range order {
				if i == j {
					continue
				}
				rest := make([]int, 0, len(order)-1)
				for k, v := range order {
					if k != i {
						rest = append(rest, v)
					}
				}
				j2 := j
				if j > i {
					j2 = j - 1
				}
				if j2 < 0 || j2 > len(rest) {
					continue
				}
				cand := make([]int, 0, len(order))
				cand = append(cand, rest[:j2]...)
				cand = append(cand, order[i])
				cand = append(cand, rest[j2:]...)
				if !ok(cand) {
					continue
				}
				if c := regenTourCost(l, from, cand); c < best-0.5 {
					order, best, improved = cand, c, true
				}
			}
		}
		if !improved {
			break
		}
	}
	return order
}

// regenNaiveOrder is the nearest-neighbour walk from the start.
func regenNaiveOrder(l [][]float64, from []float64) []int {
	n := len(l)
	done := make([]bool, n)
	var out []int
	for len(out) < n {
		best, bestCost := -1, math.Inf(1)
		for i := 0; i < n; i++ {
			if done[i] {
				continue
			}
			cost := from[i]
			if len(out) > 0 {
				cost = l[out[len(out)-1]][i]
			}
			if cost < bestCost {
				best, bestCost = i, cost
			}
		}
		if best < 0 {
			break
		}
		done[best] = true
		out = append(out, best)
	}
	return out
}

// regenTwoOpt shortens an order with 2-opt (a segment walked backwards), never breaking a must-before pair: the path-length
// matrix is one-way, so the whole tour's cost is recomputed.
func regenTwoOpt(l [][]float64, from []float64, order []int, must map[int]map[int]bool) []int {
	cost := func(o []int) float64 {
		total := 0.0
		prev := -1
		for _, i := range o {
			if prev < 0 {
				total += from[i]
			} else {
				total += l[prev][i]
			}
			prev = i
		}
		return total
	}
	ok := func(o []int) bool {
		pos := make([]int, len(o))
		for k, v := range o {
			pos[v] = k
		}
		for i, bs := range must {
			for j := range bs {
				if pos[j] > pos[i] {
					return false
				}
			}
		}
		return true
	}
	best := cost(order)
	for pass := 0; pass < 8; pass++ {
		improved := false
		for i := 1; i < len(order)-1; i++ {
			for j := i + 1; j < len(order); j++ {
				cand := make([]int, len(order))
				copy(cand, order)
				for a, b := i, j; a < b; a, b = a+1, b-1 {
					cand[a], cand[b] = cand[b], cand[a]
				}
				if !ok(cand) {
					continue
				}
				if c := cost(cand); c < best-0.5 {
					order, best, improved = cand, c, true
				}
			}
		}
		if !improved {
			break
		}
	}
	return order
}

// uphill says i -> j costs over regenOneWayRatio times its reverse (and more than regenOneWayMargin yards more), a walk that
// would have to come back the long way.
func uphill(l [][]float64, i, j int) bool {
	a, b := l[i][j], l[j][i]
	if math.IsInf(a, 1) || math.IsInf(b, 1) {
		return false
	}
	return a > regenOneWayRatio*b && a-b > regenOneWayMargin
}

// walkLen measures a leg's walked route: dir_style waypoint, `to`, every group's approach, the boss's approach and the boss, each
// hop's navmesh path length. It also counts the positioned anchors and the ones whose floor is water.
func (r *regen) walkLen(legIdx int) (total float64, water, points int) {
	leg := &r.plan.Legs[legIdx]
	var seq []dungeon.Point
	for _, w := range leg.Via {
		seq = append(seq, w.Point)
	}
	seq = append(seq, leg.To)
	for i := range leg.PullGroups {
		seq = append(seq, leg.PullGroups[i].Approach...)
	}
	if leg.Boss != nil {
		seq = append(seq, leg.Boss.Approach...)
		seq = append(seq, leg.Boss.Position)
	}
	points = len(seq)
	for i, p := range seq {
		if f, err := r.nav.Floors(r.mapID, p.X, p.Y, p.Z-regenFloorWindowYd, p.Z+regenFloorWindowYd); err == nil {
			if best, ok := regenNearestFloor(f, p.Z); ok && best.Flags&navmesh.NavWater != 0 {
				water++
			}
		}
		if i == 0 {
			continue
		}
		if d, ok := r.pathLen(seq[i-1], p); ok {
			total += d
		} else {
			total += math.Sqrt(regenDist3Sq(seq[i-1], p))
		}
	}
	return total, water, points
}

// longHops counts hops over regenMaxHopYd in a leg's written route (old is the plan before the regeneration when given).
func (r *regen) longHops(legIdx int, old *dungeon.Plan) int {
	leg := &r.plan.Legs[legIdx]
	if old != nil {
		leg = &old.Legs[legIdx]
	}
	var seq []dungeon.Point
	for _, w := range leg.Via {
		seq = append(seq, w.Point)
	}
	seq = append(seq, leg.To)
	for i := range leg.PullGroups {
		seq = append(seq, leg.PullGroups[i].Approach...)
	}
	if leg.Boss != nil {
		seq = append(seq, leg.Boss.Approach...)
		seq = append(seq, leg.Boss.Position)
	}
	n := 0
	for i := 1; i < len(seq); i++ {
		if regenDist3(seq[i-1], seq[i]) > regenMaxHopYd {
			n++
		}
	}
	return n
}

// lintLeg runs the audit's placement lints on the leg's authored rows (the plan export without a cut) and returns the counts of
// the lints the brief checks (water, off-floor, aggro); nil when the rows cannot be built.
func (r *regen) lintLeg(legIdx int) map[string]int {
	res, err := planexport.Build(r.plan, planexport.Options{Source: "route-regen"})
	if err != nil {
		return nil
	}
	lm := res.Rows.LegMap(r.plan)
	rowLeg, ok := planexport.LegNumber(lm, legIdx)
	if !ok {
		return nil
	}
	fs, err := planlint.Lint(res.Rows, r.nav, planlint.Options{Map: r.mapID, Level: regenPartYd, World: r.world})
	if err != nil {
		return nil
	}
	c := map[string]int{}
	for _, f := range fs {
		if f.Leg == rowLeg {
			c[f.Lint]++
		}
	}
	return c
}

// regenCopySet copies a pulled-groups set (a segment's route starts from the legs before it).
func regenCopySet(m map[int]bool) map[int]bool {
	out := make(map[int]bool, len(m))
	for k, v := range m {
		out[k] = v
	}
	return out
}

func regenLintText(c map[string]int) string {
	if c == nil {
		return "not run"
	}
	keys := []string{planlint.LintWater, planlint.LintOffFloor, planlint.LintAggro, planlint.LintPatrol, planlint.LintEdge, planlint.LintLedge, planlint.LintShortHop}
	var parts []string
	for _, k := range keys {
		if c[k] > 0 {
			parts = append(parts, fmt.Sprintf("%s %d", k, c[k]))
		}
	}
	if len(parts) == 0 {
		return "none"
	}
	return strings.Join(parts, ", ")
}

func regenNearestFloor(fl []navmesh.Floor, z float32) (navmesh.Floor, bool) {
	var best navmesh.Floor
	bestD := float32(math.Inf(1))
	for _, f := range fl {
		if d := float32(math.Abs(float64(f.Z - z))); d < bestD {
			best, bestD = f, d
		}
	}
	return best, bestD < float32(math.Inf(1))
}

// regenLoopDist is the 3D distance from p to the closed patrol loop.
func regenLoopDist(p dungeon.Point, path []dungeon.Point) float64 {
	min := math.Inf(1)
	for i := range path {
		if d := regenSegDist(p, path[i], path[(i+1)%len(path)]); d < min {
			min = d
		}
	}
	return min
}

// regenLoopNearest is the patrol loop's point nearest p.
func regenLoopNearest(p dungeon.Point, path []dungeon.Point) dungeon.Point {
	best := path[0]
	bestD := math.Inf(1)
	for i := range path {
		q, d := regenSegNearest(p, path[i], path[(i+1)%len(path)])
		if d < bestD {
			best, bestD = q, d
		}
	}
	return best
}

// regenSegNearest is the point of the segment a-b nearest p.
func regenSegNearest(p, a, b dungeon.Point) (dungeon.Point, float64) {
	abx, aby, abz := float64(b.X-a.X), float64(b.Y-a.Y), float64(b.Z-a.Z)
	l := abx*abx + aby*aby + abz*abz
	t := 0.0
	if l > 0 {
		t = (float64(p.X-a.X)*abx + float64(p.Y-a.Y)*aby + float64(p.Z-a.Z)*abz) / l
		t = math.Max(0, math.Min(1, t))
	}
	q := dungeon.Point{X: a.X + float32(t*abx), Y: a.Y + float32(t*aby), Z: a.Z + float32(t*abz)}
	return q, regenDist3(p, q)
}

// regenWetLine says the straight line a-b crosses water: some sample's nearest floor is water or there is no ground within
// regenGroundZtolYd of it while a water floor is within 1.5 yd.
func regenWetLine(r *regen, a, b dungeon.Point) bool {
	n := int(math.Ceil(regenDist3(a, b) / regenMaxSegSamples))
	if n < 1 {
		return false
	}
	for k := 0; k <= n; k++ {
		f := float32(k) / float32(n)
		p := dungeon.Point{X: a.X + (b.X-a.X)*f, Y: a.Y + (b.Y-a.Y)*f, Z: a.Z + (b.Z-a.Z)*f}
		fl, err := r.nav.Floors(r.mapID, p.X, p.Y, p.Z-2, p.Z+2)
		if err != nil {
			return false
		}
		wet := false
		ground := false
		for _, floor := range fl {
			if floor.Flags&navmesh.NavWater != 0 && math.Abs(float64(floor.Z-p.Z)) <= 1.5 {
				wet = true
			}
			if floor.Flags&navmesh.NavGround != 0 && floor.Flags&(navmesh.NavWater|navmesh.NavSteep|navmesh.NavMagma|navmesh.NavSlime) == 0 && math.Abs(float64(floor.Z-p.Z)) <= regenGroundZtolYd {
				ground = true
			}
		}
		if wet && !ground {
			return true
		}
	}
	return false
}

func regenSegDist(p, a, b dungeon.Point) float64 {
	abx, aby, abz := float64(b.X-a.X), float64(b.Y-a.Y), float64(b.Z-a.Z)
	l := abx*abx + aby*aby + abz*abz
	t := 0.0
	if l > 0 {
		t = (float64(p.X-a.X)*abx + float64(p.Y-a.Y)*aby + float64(p.Z-a.Z)*abz) / l
		t = math.Max(0, math.Min(1, t))
	}
	return math.Sqrt(math.Pow(float64(p.X-a.X)-t*abx, 2) + math.Pow(float64(p.Y-a.Y)-t*aby, 2) + math.Pow(float64(p.Z-a.Z)-t*abz, 2))
}

func regenDist3(a, b dungeon.Point) float64 { return math.Sqrt(regenDist3Sq(a, b)) }

func regenDist3Sq(a, b dungeon.Point) float64 {
	dx, dy, dz := float64(a.X-b.X), float64(a.Y-b.Y), float64(a.Z-b.Z)
	return dx*dx + dy*dy + dz*dz
}

// regenTurn is the 3D change of direction at b in degrees (0 straight on).
func regenTurn(a, b, c dungeon.Point) float64 {
	u := [3]float64{float64(a.X - b.X), float64(a.Y - b.Y), float64(a.Z - b.Z)}
	v := [3]float64{float64(c.X - b.X), float64(c.Y - b.Y), float64(c.Z - b.Z)}
	nu := math.Sqrt(u[0]*u[0] + u[1]*u[1] + u[2]*u[2])
	nv := math.Sqrt(v[0]*v[0] + v[1]*v[1] + v[2]*v[2])
	if nu == 0 || nv == 0 {
		return 0
	}
	cos := (u[0]*v[0] + u[1]*v[1] + u[2]*v[2]) / (nu * nv)
	cos = math.Max(-1, math.Min(1, cos))
	return math.Acos(cos) * 180 / math.Pi
}

// runGit runs git in the working directory and returns its standard output.
func runGit(args ...string) (string, error) {
	out, err := exec.Command("git", args...).Output()
	return string(out), err
}
