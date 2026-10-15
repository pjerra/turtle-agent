package dungeon

import (
	"context"
	"fmt"
	"strings"
)

// gm_cross: a waypoint the party does not walk to (Waypoint.GMCross). Wailing Caverns' ramp up to Lord Serpentis starts across a 3.9 yd gap
// with no floor, 6 yd lower: a running jump down, a climb nobody can make back up, no navmesh link, so a party that follows the mesh cannot
// get there (tests/walk-wc, 2026-10-15). A GM account moves the party instead, with the machinery the start phase already has
// (start.go: `.appear <lead bot>` when the commander is not near, `.go xyz`, `.bot summon <lead bot>`, the lead bot held where it lands,
// `cmd summon <bot>` for each bot that stands apart): the commander's own character goes to the point, the lead bot and the others are
// brought to it, and the run goes on when every living member stands within GMCrossRadius of the point. Each GM line is booked as a GM
// command of the run, like the start phase's.
//
// What it needs: a remote run whose game can move the commander's own character (StartHands) and an account of developer level
// (capsFor(level).Teleport, `.go xyz`). Run checks that before anything is sent (checkGMCross) when the plan has a gm_cross from the start leg
// on; the step itself checks again. The group driver refuses such a plan (Driver.Run). It is not for a fight: a party in combat is waited
// for, up to startWait, then the step fails.

// checkGMCross refuses a run whose plan has a gm_cross (from leg fromLeg on) that the session cannot do, before any command goes out.
func (d *RemoteDriver) checkGMCross(ctx context.Context, fromLeg int) error {
	if !d.plan.HasGMCross(fromLeg) {
		return nil
	}
	return d.gmCrossCan(ctx)
}

// gmCrossCan says whether the commander may do a GM move: the game moves the commander's character and the account level allows `.go xyz`.
func (d *RemoteDriver) gmCrossCan(ctx context.Context) error {
	h, ok := d.g.(StartHands)
	if !ok {
		return fmt.Errorf("plan %s has a gm_cross waypoint, but this game cannot move the commander's own character (no .go xyz): nothing was sent. Use a remote run with a GM account", d.plan.Name)
	}
	level, known, err := h.AccountLevel(ctx)
	if err != nil {
		return fmt.Errorf("plan %s has a gm_cross waypoint and the account level could not be read: %w; nothing was sent", d.plan.Name, err)
	}
	if !known || !capsFor(level).Teleport {
		return fmt.Errorf("plan %s has a gm_cross waypoint (a GM moves the party across a gap no walk crosses) and needs a GM commander (account level %d, .go xyz); nothing was sent. Use a GM account, or start after the gap with from_leg", d.plan.Name, level)
	}
	return nil
}

// outsideCross lists the living members of the feed that do not stand within GMCrossRadius of at on the plan's map.
func (d *RemoteDriver) outsideCross(at Point) []string {
	var out []string
	for _, m := range d.fr.Party {
		if m.Dead {
			continue
		}
		if m.Map != d.plan.Map || dist3(partyPoint(m), at) > GMCrossRadius {
			out = append(out, m.Name)
		}
	}
	return out
}

// gmCross moves the party to at with GM lines and returns when every living member stands within GMCrossRadius of it.
func (d *RemoteDriver) gmCross(ctx context.Context, at Point) error {
	if err := d.gmCrossCan(ctx); err != nil {
		return err
	}
	h := d.g.(StartHands)
	d.gmCrossing = true // the start phase's notes and GM lines are booked as the crossing's
	defer func() { d.gmCrossing = false }()
	d.look()
	if len(d.outsideCross(at)) == 0 && d.haveFr {
		d.gmCrossNote(fmt.Sprintf("the party is at %.1f %.1f %.1f already: no GM move", at.X, at.Y, at.Z))
		return nil
	}
	began := d.clk.Now()
	// Out of a fight only: the module refuses to summon a bot in combat, and a GM move would drop the tank out of one.
	for d.fr.PartyInCombat() {
		if d.clk.Now().Sub(began) > d.startWait() {
			return fmt.Errorf("gm_cross at %.1f %.1f %.1f: the party is still in combat after %.0f s", at.X, at.Y, at.Z, d.startWait().Seconds())
		}
		if err := d.tick(ctx, d.cfg.Poll); err != nil {
			return err
		}
	}
	if err := d.commanderTo(ctx, h, at); err != nil {
		return fmt.Errorf("gm_cross at %.1f %.1f %.1f: %w", at.X, at.Y, at.Z, err)
	}
	if err := d.bringLead(ctx, h, at); err != nil {
		return fmt.Errorf("gm_cross at %.1f %.1f %.1f: %w", at.X, at.Y, at.Z, err)
	}
	if err := d.gatherParty(ctx, at); err != nil {
		return fmt.Errorf("gm_cross at %.1f %.1f %.1f: %w", at.X, at.Y, at.Z, err)
	}
	// `cmd summon` made the commander the summoned bots' master and put them on follow (as in rejoinFar): the party follows the lead bot again.
	r, err := d.order(ctx, "follow")
	if err != nil {
		return err
	}
	if ref, bad := refusedOf(r); bad {
		d.gmCrossNote(fmt.Sprintf("cmd follow after the move was refused: %s (%s)", ref.Code, ref.Message))
	}
	// Every living member must stand within the radius of the point: the others landed beside the lead bot, which landed beside the commander.
	deadline := d.clk.Now().Add(d.startWait())
	for {
		d.look()
		if d.haveFr && len(d.outsideCross(at)) == 0 {
			break
		}
		if d.clk.Now().After(deadline) {
			return fmt.Errorf("gm_cross at %.1f %.1f %.1f: %s not within %.0f yd of it %.0f s after the move", at.X, at.Y, at.Z, strings.Join(d.outsideCross(at), ", "), GMCrossRadius, d.startWait().Seconds())
		}
		if err := d.tick(ctx, d.cfg.Poll); err != nil {
			return err
		}
	}
	moved := 0
	for _, m := range d.fr.Party {
		if !m.Dead {
			moved++
		}
	}
	d.remote(func(rr *RemoteReport) { rr.GMCrosses++ })
	d.gmCrossNote(fmt.Sprintf("gm_cross at %.1f %.1f %.1f: %d bots moved (%.0f s)", at.X, at.Y, at.Z, moved, d.clk.Now().Sub(began).Seconds()))
	return nil
}

func (d *RemoteDriver) gmCrossNote(text string) { d.startNote(text) }
