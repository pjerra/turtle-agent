package dungeon

import (
	"context"
	"fmt"
	"math"
	"strings"
)

// AgentPositioner is the optional part of a Preparer that tells where the agent's own character stands, and whether its height is a guess
// (session.StatusInfo.ZUnknown). A teleport or a climb leaves z a guess until the server sends a position, and the hire spot then judges
// every floor the commander may be on (see hireSpotProblem).
type AgentPositioner interface {
	AgentPosition() (pos Point, zUnknown bool)
}

const (
	hireSpotRadius  = 15.0 // yd, horizontal, around the plan's portal point
	hireSpotMaxDrop = 5.0  // yd: a commander whose navmesh floor is farther than this below the portal's floor hires nothing
	hireSpotGuess   = 30.0 // yd: how far around a guessed height the floors are looked at, so a bad guess does not fail open as "no navmesh"
)

// hireSpotProblem says why the commander must not hire where it stands ("" when it may). Hires appear at the commander and muster
// there (#266: on 2026-10-20 an Alliance commander hired for a horde plan under the Orgrimmar portal and a Grunt and an imp killed all
// five hires in 41 s), and the module hires the commander's own faction only, so a commander of the other team than the run's gets a party
// that fights for the other side. The portal ground is the navmesh floor at the portal x/y nearest below the trigger centre. A commander's
// height that the server settled picks the floor at its own x/y nearest that height; a guessed one (a far teleport leaves it guessed for
// good: the session lands through SMSG_NEW_WORLD with no floor drop and the core never sends the player its own position, walk 23) picks
// the lowest floor within hireSpotGuess yd of the guess, so a stacked lower level refuses instead of a nearest-floor snap that could be the
// portal's own floor. Only a commander whose floor is well below the portal's refuses.
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
	var floor float32
	stacked := false
	if zUnknown {
		// The guess may be on any floor at the commander's x/y, so its nearest floor could be the portal ground while the commander is a
		// level down. Looking hireSpotGuess yd around the guess (wider than the walk's 12) and judging the lowest floor there keeps a bad
		// guess from passing as "no navmesh", and a stacked lower level from snapping to the portal floor (Codex HIGH, F5 round 5).
		zs, ok := fl.FloorsWithin(ctx, portal.Map, pos.X, pos.Y, pos.Z-hireSpotGuess, pos.Z+hireSpotGuess)
		if !ok {
			e.note("hire: portal ground not checked (no navmesh)")
			return "", nil
		}
		floor = zs[0]
		for _, z := range zs[1:] {
			if z < floor {
				floor = z
			}
		}
		stacked = len(zs) > 1
	} else {
		zs, ok := fl.FloorsAt(ctx, portal.Map, pos.X, pos.Y, pos.Z)
		if !ok {
			e.note("hire: portal ground not checked (no navmesh)")
			return "", nil
		}
		floor, ok = nearestFloor(zs, pos.Z, -math.MaxFloat32, math.MaxFloat32)
		if !ok {
			e.note("hire: portal ground not checked (no navmesh)")
			return "", nil
		}
	}
	if float64(ground)-float64(floor) <= hireSpotMaxDrop {
		return "", nil
	}
	if zUnknown && stacked {
		return fmt.Sprintf("hire: commander may be on a lower floor near the portal: its x/y has a floor %.1f, %.1f yd below the portal ground %.1f, and its own height is a guess; stand on the portal ground first", floor, float64(ground)-float64(floor), ground), nil
	}
	return fmt.Sprintf("the commander stands %.1f yd below the portal ground (its floor %.1f, the portal ground %.1f, %.1f yd across): the hires appear and muster where the commander stands; stand on the portal ground first", float64(ground)-float64(floor), floor, ground, across(pos)), nil
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
