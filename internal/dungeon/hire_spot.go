package dungeon

import (
	"fmt"
	"math"
	"strings"
)

// AgentPositioner is the optional part of a Preparer that tells where the agent's own character stands.
type AgentPositioner interface {
	AgentPosition() Point
}

const (
	hireSpotRadius = 15.0 // yd, horizontal, around the plan's portal point
	hireSpotMaxDZ  = 5.0  // yd: farther than this above or below the portal point, inside the radius, is not the portal ground
)

// hireSpotProblem says why the commander must not hire where it stands ("" when it may). Hires appear at the commander and muster
// there (#266: on 2026-10-20 an Alliance commander hired for a horde plan under the Orgrimmar portal, z -19.8 against the portal's
// -10.4; a Grunt and an imp killed all five hires in 41 s), and the module hires the commander's own faction only, so a commander of
// the other team than the run's gets a party that fights for the other side.
func (e *prepEnv) hireSpotProblem() string {
	agent := ""
	if ts, ok := e.g.(TeamSource); ok {
		agent = ts.Team()
	}
	if e.team != "" && agent != "" && !strings.EqualFold(agent, e.team) {
		return fmt.Sprintf("the run's team is %s but the commander is %s: the module hires the commander's own faction (TortoiseBots HireProvisionService), so every hire would fight for the other side; start the run from a %s commander, or drop team", e.team, agent, e.team)
	}
	if e.plan == nil || e.plan.Portal == nil {
		return ""
	}
	ap, ok := e.g.(AgentPositioner)
	if !ok {
		e.note("hire spot: the game gives no agent position, the portal ground is not checked")
		return ""
	}
	portal := *e.plan.Portal
	if e.g.AgentMap() != portal.Map {
		return ""
	}
	pos := ap.AgentPosition()
	d := math.Hypot(float64(pos.X)-float64(portal.X), float64(pos.Y)-float64(portal.Y))
	dz := float64(pos.Z) - float64(portal.Z)
	if d > hireSpotRadius || math.Abs(dz) <= hireSpotMaxDZ {
		return ""
	}
	side := "above"
	if dz < 0 {
		side = "below"
	}
	return fmt.Sprintf("the commander stands %.1f yd %s the portal ground (z %.1f, the portal %.1f, %.1f yd across): the hires appear and muster where the commander stands; stand on the portal ground first", math.Abs(dz), side, pos.Z, portal.Z, d)
}
