package dungeon

import (
	"context"
	"fmt"
	"math"
	"strings"
	"time"
)

// AgentPositioner is the optional part of a Preparer that tells where the agent's own character stands, and whether z is the server's
// own height (false: a guess until the server sends a position, session.StatusInfo.ZUnknown).
type AgentPositioner interface {
	AgentPosition() (Point, bool)
}

const (
	hireSpotRadius = 15.0             // yd, horizontal, around the plan's portal point
	hireSpotMaxDZ  = 5.0              // yd: farther than this above or below the portal point, inside the radius, is not the portal ground
	hireSpotZWait  = 10 * time.Second // how long the hire waits near the portal for the server's own height
	hireSpotZPoll  = 250 * time.Millisecond
)

// hireSpotProblem says why the commander must not hire where it stands ("" when it may). Hires appear at the commander and muster
// there (#266: on 2026-10-20 an Alliance commander hired for a horde plan under the Orgrimmar portal, z -19.8 against the portal's
// -10.4; a Grunt and an imp killed all five hires in 41 s), and the module hires the commander's own faction only, so a commander of
// the other team than the run's gets a party that fights for the other side.
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
	pos, zKnown := ap.AgentPosition()
	if across(pos) > hireSpotRadius {
		return "", nil
	}
	if !zKnown {
		// A guessed height cannot decide the drop under the portal (#266): wait for the server's own position, then judge on it.
		e.note(fmt.Sprintf("hire spot: the commander's height is unknown near the portal, waiting up to %.0f s for the server's position", hireSpotZWait.Seconds()))
		var werr error
		pos, zKnown, werr = e.waitAgentZ(ctx, ap, portal.Point)
		if werr != nil {
			// A stop during the wait ends the hire as stopped, never as a failed spot: the cancellation goes back unchanged, so the
			// run's classifier (ctxDone) reads it as a stop.
			return "", werr
		}
		if across(pos) > hireSpotRadius {
			return "", nil
		}
		if !zKnown {
			return "hire: commander height unknown near the portal", nil
		}
	}
	dz := float64(pos.Z) - float64(portal.Z)
	if math.Abs(dz) <= hireSpotMaxDZ {
		return "", nil
	}
	side := "above"
	if dz < 0 {
		side = "below"
	}
	return fmt.Sprintf("the commander stands %.1f yd %s the portal ground (z %.1f, the portal %.1f, %.1f yd across): the hires appear and muster where the commander stands; stand on the portal ground first", math.Abs(dz), side, pos.Z, portal.Z, across(pos)), nil
}

// waitAgentZ waits for the game to report the commander's own height: up to hireSpotZWait, polled every hireSpotZPoll, and ends early
// when the commander leaves the portal's horizontal radius (there the height decides nothing). It returns the last position, whether
// the height was the server's own, and the wait's error: a stop during the wait is the caller's stop, never a height verdict.
func (e *prepEnv) waitAgentZ(ctx context.Context, ap AgentPositioner, portal Point) (Point, bool, error) {
	deadline := e.now().Add(hireSpotZWait)
	pos, known := ap.AgentPosition()
	for !known && e.now().Before(deadline) {
		if math.Hypot(float64(pos.X)-float64(portal.X), float64(pos.Y)-float64(portal.Y)) > hireSpotRadius {
			break
		}
		if err := e.sleep(ctx, hireSpotZPoll); err != nil {
			return pos, known, err
		}
		pos, known = ap.AgentPosition()
	}
	return pos, known, nil
}
