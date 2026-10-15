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
// What it needs: a remote run whose game can move the commander's own character (StartHands) and a GM account that may `.go xyz`
// (capsFor(level).Teleport: level 1 and up, the probe the keep-near and start phases use: `.account`; it does not read the group driver's
// leader_gm or gm_caps report fields, which a remote run leaves false). Run checks that before anything is sent (checkGMCross) when the plan has a gm_cross from the start leg
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

// crossExpected are the names of the party the crossing must move: the lead bot and the bots the run named. A run that named none expects
// as many members as its bound party had at the start (wantParty), whoever they are.
func (d *RemoteDriver) crossExpected() []string {
	return append([]string{d.opt.Lead}, d.opt.Bots...)
}

// crossUnresolved lists what keeps the party from being at `at`: an expected member that the newest frame does not list (it is not known
// to be there), a living member on another map or farther than GMCrossRadius from the point, and, when the run named no bots, a frame
// with fewer members than the party the run began with. A dead member is not waited for. Empty: every expected living member stands there.
func (d *RemoteDriver) crossUnresolved(at Point) []string {
	var out []string
	for _, name := range d.crossExpected() {
		m, ok := d.memberByName(name)
		switch {
		case !ok:
			out = append(out, name+" (not in the frame)")
		case m.Dead:
		case m.Map != d.plan.Map || dist3(partyPoint(m), at) > GMCrossRadius:
			out = append(out, name)
		}
	}
	if len(d.opt.Bots) == 0 {
		for _, m := range d.fr.Party {
			if !m.Dead && !strings.EqualFold(m.Name, d.opt.Lead) && (m.Map != d.plan.Map || dist3(partyPoint(m), at) > GMCrossRadius) {
				out = append(out, m.Name)
			}
		}
		if len(d.fr.Party) < d.wantParty {
			out = append(out, fmt.Sprintf("%d of %d members in the frame", len(d.fr.Party), d.wantParty))
		}
	}
	return out
}

// freshFrame waits for a frame newer than frame number `after` (the feed publishes about every second): a verdict about where the party
// stands is taken from a frame that was published after the orders it judges, never from the one in hand. It fails after startWait.
func (d *RemoteDriver) freshFrame(ctx context.Context, after uint64, what string) error {
	deadline := d.clk.Now().Add(d.startWait())
	for {
		d.look()
		if d.haveFr && !d.stale() && d.n > after {
			return nil
		}
		if d.clk.Now().After(deadline) {
			return fmt.Errorf("%s: no fresh frame of the party came in %.0f s", what, d.startWait().Seconds())
		}
		if err := d.tick(ctx, d.cfg.Poll); err != nil {
			return err
		}
	}
}

// summonMissing summons by name every expected bot that the newest frame does not list (a frame that omits a bot says nothing about where it
// is; summonRest does the same for the entry): gatherParty only fetches the members it can see.
func (d *RemoteDriver) summonMissing(ctx context.Context) error {
	for _, name := range d.crossExpected()[1:] {
		if _, ok := d.memberByName(name); ok {
			continue
		}
		if err := d.startSummon(ctx, name); err != nil {
			return err
		}
	}
	return nil
}

// followParty sends `cmd follow` (the party follows the lead bot again after the summons) and insists: up to three orders, SummonGap apart.
// A bot that keeps following the commander is left behind when the lead bot walks on, so a follow that is refused three times fails the
// crossing.
func (d *RemoteDriver) followParty(ctx context.Context) error {
	var last string
	for try := 0; try < 3; try++ {
		if try > 0 {
			if err := d.tick(ctx, d.opt.SummonGap); err != nil {
				return err
			}
		}
		r, err := d.order(ctx, "follow")
		if err != nil {
			return err
		}
		ref, bad := refusedOf(r)
		if !bad {
			return nil
		}
		last = ref.Code + ": " + ref.Message
		d.gmCrossNote(fmt.Sprintf("cmd follow after the move was refused (try %d): %s", try+1, last))
	}
	return fmt.Errorf("cmd follow was refused three times (%s): the summoned bots would keep following the commander and be left behind", last)
}

// gmCross moves the party to at with GM lines and returns when every living expected member (the lead bot and the named bots) is listed by a
// fresh frame and stands within GMCrossRadius of it.
func (d *RemoteDriver) gmCross(ctx context.Context, at Point) error {
	if err := d.gmCrossCan(ctx); err != nil {
		return err
	}
	h := d.g.(StartHands)
	d.gmCrossing = true // the start phase's notes and GM lines are booked as the crossing's
	defer func() { d.gmCrossing = false }()
	fail := func(err error) error { return fmt.Errorf("gm_cross at %.1f %.1f %.1f: %w", at.X, at.Y, at.Z, err) }
	d.look()
	if err := d.freshFrame(ctx, d.n, fmt.Sprintf("gm_cross at %.1f %.1f %.1f", at.X, at.Y, at.Z)); err != nil {
		return err
	}
	if len(d.crossUnresolved(at)) == 0 {
		d.gmCrossNote(fmt.Sprintf("the party is at %.1f %.1f %.1f already: no GM move", at.X, at.Y, at.Z))
		return nil
	}
	began := d.clk.Now()
	// Out of a fight only: the module refuses to summon a bot in combat, and a GM move would drop the tank out of one.
	for d.fr.PartyInCombat() {
		if d.clk.Now().Sub(began) > d.startWait() {
			return fail(fmt.Errorf("the party is still in combat after %.0f s", d.startWait().Seconds()))
		}
		if err := d.tick(ctx, d.cfg.Poll); err != nil {
			return err
		}
	}
	if err := d.commanderTo(ctx, h, at); err != nil {
		return fail(err)
	}
	if err := d.bringLead(ctx, h, at); err != nil {
		return fail(err)
	}
	if err := d.summonMissing(ctx); err != nil {
		return fail(err)
	}
	if err := d.gatherParty(ctx, at); err != nil {
		return fail(err)
	}
	// `cmd summon` made the commander the summoned bots' master and put them on follow (as in rejoinFar): the party follows the lead bot again.
	if err := d.followParty(ctx); err != nil {
		return fail(err)
	}
	// Every expected living member must be listed and stand within the radius: judged from frames published after the follow order.
	sent := d.n
	deadline := d.clk.Now().Add(d.startWait())
	for {
		if err := d.freshFrame(ctx, sent, fmt.Sprintf("gm_cross at %.1f %.1f %.1f", at.X, at.Y, at.Z)); err != nil {
			return err
		}
		left := d.crossUnresolved(at)
		if len(left) == 0 {
			break
		}
		if d.clk.Now().After(deadline) {
			return fail(fmt.Errorf("%s not within %.0f yd of it %.0f s after the move", strings.Join(left, ", "), GMCrossRadius, d.startWait().Seconds()))
		}
		sent = d.n
	}
	moved := 0
	for _, name := range d.crossExpected() {
		if m, ok := d.memberByName(name); ok && !m.Dead {
			moved++
		}
	}
	d.remote(func(rr *RemoteReport) { rr.GMCrosses++ })
	d.gmCrossNote(fmt.Sprintf("gm_cross at %.1f %.1f %.1f: %d bots moved (%.0f s)", at.X, at.Y, at.Z, moved, d.clk.Now().Sub(began).Seconds()))
	return nil
}

func (d *RemoteDriver) gmCrossNote(text string) { d.startNote(text) }
