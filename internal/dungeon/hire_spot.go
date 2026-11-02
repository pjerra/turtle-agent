package dungeon

import (
	"context"
	"fmt"
	"math"
	"strings"
)

// AgentPositioner is the optional part of a Preparer that tells where the agent's own character stands, and whether its height is a guess
// (session.StatusInfo.ZUnknown). A teleport or a climb leaves z a guess until the server sends a position; a guessed height over two floors
// is settled with one short walk before the hire spot is judged (see hireSpotProblem).
type AgentPositioner interface {
	AgentPosition() (pos Point, zUnknown bool)
}

// CommanderWalker is the optional part of a Preparer that walks the commander's own character a short navmesh move (walk_path). The hire
// spot uses it once to settle a guessed height over stacked floors: a navmesh walk that ends on the mesh clears status.z_unknown
// (session tickWalk).
type CommanderWalker interface {
	WalkSelf(ctx context.Context, to Point) (WalkResult, error)
}

const (
	hireSpotRadius  = 15.0 // yd, horizontal, around the plan's portal point
	hireSpotMaxDrop = 5.0  // yd: a commander whose navmesh floor is farther than this below the portal's floor hires nothing
	hireSpotGuess   = 30.0 // yd: how far around a guessed height the floors are looked at, so a bad guess does not fail open as "no navmesh"
)

// hireSpotProblem says why the commander must not hire where it stands ("" when it may). Hires appear at the commander and muster
// there (#266: on 2026-10-20 an Alliance commander hired for a horde plan under the Orgrimmar portal and a Grunt and an imp killed all
// five hires in 41 s), and the module hires the commander's own faction only, so a commander of the other team than the run's gets a party
// that fights for the other side. The portal ground is the navmesh floor at the portal x/y nearest below the trigger centre. A settled
// height (the server knows it) picks the floor at its own x/y nearest that height and refuses when it stands on the wrong floor. A
// guessed height (status.z_unknown: a far teleport leaves it guessed for good, the session lands through SMSG_NEW_WORLD with no floor
// drop and the core never sends the player its own position, walk 23) over two floors more than hireSpotMaxDrop apart is settled first
// with one short navmesh walk of the commander and judged like a settled height; a height still a guess afterwards refuses.
func (e *prepEnv) hireSpotProblem(ctx context.Context) (string, error) {
	agent := ""
	if ts, ok := e.g.(TeamSource); ok {
		agent = ts.Team()
	}
	if e.team != "" && agent != "" && !strings.EqualFold(agent, e.team) {
		return fmt.Sprintf("the run's team is %s but the commander is %s: the module hires the commander's own faction (TortoiseBots HireProvisionService), so every hire would fight for the other side; start the run from a %s commander, or drop team", e.team, agent, e.team), nil
	}
	if e.plan == nil || e.plan.Portal == nil {
		return "", nil
	}
	ap, ok := e.g.(AgentPositioner)
	if !ok {
		e.note("hire spot: the game gives no agent position, the portal ground is not checked")
		return "", nil
	}
	portal := *e.plan.Portal
	if e.g.AgentMap() != portal.Map {
		return "", nil
	}
	across := func(p Point) float64 {
		return math.Hypot(float64(p.X)-float64(portal.X), float64(p.Y)-float64(portal.Y))
	}
	pos, zUnknown := ap.AgentPosition()
	if across(pos) > hireSpotRadius {
		return "", nil
	}
	fl, ok := e.g.(FloorLister)
	if !ok {
		e.note("hire: portal ground not checked (no navmesh)")
		return "", nil
	}
	ground, ok := navmeshFloorBelow(ctx, fl, portal.Map, portal.Point)
	if !ok {
		e.note("hire: portal ground not checked (no navmesh)")
		return "", nil
	}
	if zUnknown {
		// The guess may sit on any floor at the commander's x/y (a far teleport leaves status.z_unknown set for good: walk 23), so the
		// step looks at every floor within hireSpotGuess yd of it. One floor there, or floors within hireSpotMaxDrop of each other
		// (Wailing Caverns' portal has one), is today's behaviour: no walk, no warning, the spot is not judged on the guess.
		zs, ok := fl.FloorsWithin(ctx, portal.Map, pos.X, pos.Y, pos.Z-hireSpotGuess, pos.Z+hireSpotGuess)
		if !ok {
			e.note("hire: portal ground not checked (no navmesh)")
			return "", nil
		}
		floor, low := zs[0], zs[0]
		for _, z := range zs[1:] {
			if z > floor {
				floor = z
			}
			if z < low {
				low = z
			}
		}
		if floor-low <= hireSpotMaxDrop {
			return "", nil
		}
		// Floors stacked more than hireSpotMaxDrop apart: refusing on the guess would refuse correct spots, and hiring on it could hire
		// onto the unreachable lower floor, so the height is settled first with one short navmesh walk of the commander (which clears
		// status.z_unknown when it ends on the mesh), and the settled height is judged like a known one. Still a guess afterwards refuses.
		settled, ok := e.resolveCommanderHeight(ctx, fl, ap, portal.Map, pos)
		if !ok {
			if ctx.Err() != nil {
				return "", ctx.Err()
			}
			return "commander height unknown over two floors; move the commander and retry", nil
		}
		pos = settled
		if across(pos) > hireSpotRadius {
			return "", nil
		}
	}
	return e.hireSpotKnown(ctx, fl, portal, ground, pos, across)
}

// hireSpotKnown says why a commander at a settled height must not hire where it stands ("" when it may): the navmesh floor at its own
// x/y nearest its height must be the portal ground within hireSpotMaxDrop.
func (e *prepEnv) hireSpotKnown(ctx context.Context, fl FloorLister, portal Portal, ground float32, pos Point, across func(Point) float64) (string, error) {
	zs, ok := fl.FloorsAt(ctx, portal.Map, pos.X, pos.Y, pos.Z)
	if !ok {
		e.note("hire: portal ground not checked (no navmesh)")
		return "", nil
	}
	floor, ok := nearestFloor(zs, pos.Z, -math.MaxFloat32, math.MaxFloat32)
	if !ok {
		e.note("hire: portal ground not checked (no navmesh)")
		return "", nil
	}
	if float64(ground)-float64(floor) <= hireSpotMaxDrop {
		return "", nil
	}
	return fmt.Sprintf("the commander stands %.1f yd below the portal ground (its floor %.1f, the portal ground %.1f, %.1f yd across): the hires appear and muster where the commander stands; stand on the portal ground first", float64(ground)-float64(floor), floor, ground, across(pos)), nil
}

// hireSpotResolveStep is how far (yd) the settle walk moves the commander: far enough to end on the mesh, near enough to stay by the
// portal (the spot only judges a commander within hireSpotRadius of it).
const hireSpotResolveStep = 3.0

// resolveCommanderHeight settles a guessed commander height with one short navmesh walk: a walk that ends on the mesh clears
// status.z_unknown (session tickWalk). It reports the commander's position afterwards, false when the height is still a guess (no
// walker, no mesh point for a target, or the walk did not settle it).
func (e *prepEnv) resolveCommanderHeight(ctx context.Context, fl FloorLister, ap AgentPositioner, mapID uint32, pos Point) (Point, bool) {
	w, ok := e.g.(CommanderWalker)
	if !ok {
		return pos, false
	}
	var to Point
	found := false
	for _, d := range [][2]float32{{hireSpotResolveStep, 0}, {-hireSpotResolveStep, 0}, {0, hireSpotResolveStep}, {0, -hireSpotResolveStep}} {
		q := Point{X: pos.X + d[0], Y: pos.Y + d[1], Z: pos.Z}
		zs, ok := fl.FloorsAt(ctx, mapID, q.X, q.Y, pos.Z)
		if !ok || len(zs) == 0 {
			continue
		}
		f, ok := nearestFloor(zs, pos.Z, -math.MaxFloat32, math.MaxFloat32)
		if !ok {
			continue
		}
		to = Point{X: q.X, Y: q.Y, Z: f}
		found = true
		break
	}
	if !found {
		return pos, false
	}
	e.note(fmt.Sprintf("hire: the commander's height is a guess over two floors, so it walks to %.1f %.1f %.1f to settle it", to.X, to.Y, to.Z))
	_, _ = w.WalkSelf(ctx, to)
	after, unknown := ap.AgentPosition()
	if unknown {
		return pos, false
	}
	return after, true
}

// navmeshFloorBelow is the floor at p's x/y nearest below p's height: the ground a body at p would rest on, never a deck above the
// trigger. The trigger centre may sit a little above the mesh (a door or a platform is no navmesh surface), so a floor up to half a yard
// above it still counts. false when the mesh lists no such floor (no planner, no tile, or the trigger is under the mesh).
func navmeshFloorBelow(ctx context.Context, fl FloorLister, mapID uint32, p Point) (float32, bool) {
	zs, ok := fl.FloorsAt(ctx, mapID, p.X, p.Y, p.Z)
	if !ok {
		return 0, false
	}
	for _, z := range zs { // highest first
		if float64(z) <= float64(p.Z)+0.5 {
			return z, true
		}
	}
	return 0, false
}
