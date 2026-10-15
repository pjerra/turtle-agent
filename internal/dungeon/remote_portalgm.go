package dungeon

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/pjerra/turtle-agent/internal/tbm"
)

// The GM move of the enter phase. The hires spawn at the commander, wherever it stands: the first Ragefire start had the party in Tanaris and
// the portal in Orgrimmar's Cleft of Shadow, and the walk ended with "the lead bot was refused 8 times on the way to the portal (unreachable:
// The lead bot has no walkable path to that point)". So the walk asks once, before it spends its tries:
//
//   - the first move of the walk is refused "unreachable" (nothing was accepted before it), or the agent's own navmesh gives a path of more
//     than portalLongWalk yards to the portal: with a GM account (`.go xyz` needs level 1) the commander goes to the portal point with
//     `.go xyz`, the lead bot is brought to it with `.bot summon <lead>` (a player command of the commander's own account) and held there, the
//     others with `cmd summon <bot>` (a GM assist, counted in run_gm_assists), until every bot of the run stands within portalTogether yards
//     of the portal; the walk then goes on from there (a few yards) and `cmd enter` is sent as before;
//   - without a GM account an "unreachable" first move fails the phase at once, with the reason, instead of after the walk's eight refusals.
//
// The commander stays outside the instance (the portal is on the outer map) and ungrouped; the same map is not required: `.go xyz` and the
// summons cross maps. Only one GM move is made per run: a lead bot that is still refused at the portal fails the phase.

const (
	portalLongWalk    = 300.0 // yards of navmesh path beyond which a GM account moves the party to the portal instead of walking it
	portalTogether    = 15.0  // yards: every bot of the run within this of the portal ends the move
	portalGMLabel     = " (GM move to the portal)"
	portalGMNoPath    = "the lead bot has no walkable path to the portal"
	portalGMAdvice    = "hire the party near it or use a GM account"
	portalMoveTries   = 2 // rounds of `cmd summon` for the bots still apart
	portalFollowTries = 3 // asks for the ACK of `cmd follow`
)

// portalPathLength is the length of the agent's own navmesh path from the lead bot to the portal (0 when the lead bot is not on the portal's
// map, the game plans no paths or no path was found: no answer, not "unreachable").
func (d *RemoteDriver) portalPathLength(ctx context.Context, pt *Portal) float64 {
	pl, ok := d.g.(PathPlanner)
	if !ok {
		return 0
	}
	l, ok := d.fr.LeadRec()
	if !ok || l.Map != pt.Map {
		return 0
	}
	pts, err := pl.PlanPath(ctx, pt.Map, partyPoint(l), pt.Point)
	if err != nil || len(pts) < 2 {
		return 0
	}
	var n float64
	for i := 1; i < len(pts); i++ {
		n += dist3(pts[i-1], pts[i])
	}
	return n
}

// portalGMHands returns the commander's hands when the account may `.go xyz` (level 1); why says what is missing otherwise.
func (d *RemoteDriver) portalGMHands(ctx context.Context) (h StartHands, why string) {
	h, ok := d.g.(StartHands)
	if !ok {
		return nil, "this game cannot move the commander's own character"
	}
	level, known, err := h.AccountLevel(ctx)
	switch {
	case err != nil:
		return nil, "the account level could not be read (" + err.Error() + ")"
	case !known:
		return nil, "the server did not answer .account"
	case !capsFor(level).Teleport:
		return nil, fmt.Sprintf("the account is a player's (level %d, .go xyz needs %d)", level, ObserverLevel)
	}
	return h, ""
}

// portalNoPath is the failure of an unreachable portal without a GM account (or one that cannot move the party).
func (d *RemoteDriver) portalNoPath(ref Refusal, why string) error {
	return fmt.Errorf("enter: %s (%s: %s); %s (%s)", portalGMNoPath, ref.Code, ref.Message, portalGMAdvice, why)
}

// portalGMBook books a GM line of the move: a setup shortcut, a GM command of the run.
func (d *RemoteDriver) portalGMBook(line string) {
	d.rep.update(func(r *Report) { r.SetupShortcuts = append(r.SetupShortcuts, line+portalGMLabel) })
	d.remote(func(r *RemoteReport) { r.GMCommands++ })
	d.tr.Event("gm_shortcut", map[string]any{"kind": "setup", "detail": line})
}

// moveToPortalByGM moves the commander to the portal, brings the lead bot and the others to it, and waits until all stand together there.
// Every configured bot (the lead bot and RemoteOptions.Bots) counts as apart until the feed shows it alive within portalTogether yards of the
// portal on its map: one that is missing from the feed, dead or on another map is summoned (a dead one revived first, with the run's own
// revive) and waited for, and the phase fails before PartyMoved and `cmd enter` if it does not come.
func (d *RemoteDriver) moveToPortalByGM(ctx context.Context, h StartHands, pt *Portal, reason string) error {
	began := d.clk.Now()
	d.enterNote(fmt.Sprintf("%s: moving the party to the portal at %.1f %.1f %.1f on map %d by GM (.go xyz, .bot summon, cmd summon)", reason, pt.X, pt.Y, pt.Z, pt.Map))
	if err := h.GoXYZ(ctx, pt.Map, pt.Point); err != nil {
		return fmt.Errorf(".go xyz: %w", err)
	}
	d.portalGMBook(fmt.Sprintf(".go xyz %.2f %.2f %.2f %d", pt.X, pt.Y, pt.Z, pt.Map))
	if p, m, ok := h.AgentPlace(); !ok || m != pt.Map || hdist(p, pt.Point) > startNear {
		return fmt.Errorf("the commander is not at the portal after .go xyz (at %.0f %.0f on map %d)", p.X, p.Y, m)
	}
	// A dead bot cannot be summoned ("cannot be summoned" while dead): the run's revive first.
	if err := d.reviveBeforeMove(ctx); err != nil {
		return err
	}
	d.look()
	leadBrought := false
	if l, ok := d.fr.LeadRec(); !ok || l.Map != pt.Map || dist3(partyPoint(l), pt.Point) > portalTogether {
		if err := d.bringLead(ctx, h, pt.Point); err != nil {
			return err
		}
		leadBrought = true
	}
	summoned := false
	for round := 1; round <= portalMoveTries; round++ {
		if err := d.reviveBeforeMove(ctx); err != nil {
			return err
		}
		d.look()
		apart := d.apartFromPortal(pt)
		if len(apart) == 0 {
			break
		}
		for i, name := range apart {
			if strings.EqualFold(name, d.opt.Lead) {
				continue
			}
			if i > 0 {
				if err := d.tick(ctx, d.opt.SummonGap); err != nil {
					return err
				}
			}
			if err := d.startSummon(ctx, name); err != nil {
				return err
			}
			summoned = true
			d.remote(func(r *RemoteReport) { r.RunGMAssists++ })
			d.rep.update(func(r *Report) {
				r.SetupShortcuts = append(r.SetupShortcuts, "cmd summon "+name+" (GM assist: party to the portal)")
			})
		}
		deadline := d.clk.Now().Add(d.startWait())
		for len(d.apartFromPortal(pt)) > 0 && d.clk.Now().Before(deadline) {
			if err := d.tick(ctx, d.cfg.Poll); err != nil {
				return err
			}
		}
	}
	if apart := d.apartFromPortal(pt); len(apart) > 0 {
		return fmt.Errorf("the party did not gather at the portal: %s not seen alive within %.0f yd of it after %d summons", strings.Join(apart, ", "), portalTogether, portalMoveTries)
	}
	if summoned || leadBrought {
		if err := d.followAfterSummons(ctx); err != nil {
			return err
		}
	}
	n := len(d.namedBots())
	msg := fmt.Sprintf("party moved to the portal by GM, %d bots (%.0f s)", n, d.clk.Now().Sub(began).Seconds())
	d.enterReport(func(r *EnterReport) { r.PartyMoved = msg })
	d.enterNote(msg)
	return nil
}

// reviveBeforeMove revives the dead among the configured bots with `cmd revive <bot>` (the lead bot first; up to three orders per bot, 20 s apart)
// and waits for them to show alive; it fails the phase when one stays dead. The run's reviveDead is not used: it ends by bringing the party into
// the dungeon (bringPartyIn), and here the whole party is outside by design.
func (d *RemoteDriver) reviveBeforeMove(ctx context.Context) error {
	d.look()
	deadNames := func() []string {
		var out []string
		for _, n := range append([]string{d.opt.Lead}, d.namedBots()...) {
			if m, ok := d.memberByName(n); ok && m.Dead {
				out = append(out, n)
			}
		}
		return out
	}
	dead := deadNames()
	if len(dead) == 0 {
		return nil
	}
	d.enterNote("dead before the entry: " + strings.Join(dead, ", ") + "; reviving")
	last := map[string]time.Time{}
	tries := map[string]int{}
	deadline := d.clk.Now().Add(d.cfg.WipeRecoverTimeout)
	for {
		d.look()
		dead = deadNames()
		if len(dead) == 0 {
			return nil
		}
		if !d.clk.Now().Before(deadline) {
			return fmt.Errorf("enter: %s still dead %.0f s after the revive orders", strings.Join(dead, ", "), d.cfg.WipeRecoverTimeout.Seconds())
		}
		for _, n := range dead {
			if at, ok := last[n]; ok && d.clk.Now().Sub(at) < 20*time.Second {
				continue
			}
			if tries[n] >= 3 {
				return fmt.Errorf("enter: %s is still dead after %d revive orders", n, tries[n])
			}
			tries[n]++
			last[n] = d.clk.Now()
			d.remote(func(r *RemoteReport) { r.Revives++ })
			r, err := d.order(ctx, "revive", n)
			if err != nil {
				return err
			}
			if ref, bad := refusedOf(r); bad && ref.Code != "not-dead" {
				return fmt.Errorf("enter: %s is dead and the revive order was refused: %s (%s)", n, ref.Code, ref.Message)
			}
		}
		if err := d.tick(ctx, d.cfg.Poll*3); err != nil {
			return err
		}
	}
}

// followAfterSummons puts the party on follow of the lead bot and requires the module's ACK. `.bot summon` and `cmd summon` made the
// commander the bots' master and put them on follow of it; with the commander outside the instance they are pulled out again after the entry
// (hires-3 run 1), so a follow that is not confirmed fails the phase before `cmd enter`. A busy, cancelled or in-combat answer, or none, is
// asked again (portalFollowTries times, SummonGap apart); any other refusal fails at once.
func (d *RemoteDriver) followAfterSummons(ctx context.Context) error {
	var last string
	for try := 1; try <= portalFollowTries; try++ {
		if try > 1 {
			if err := d.tick(ctx, d.opt.SummonGap); err != nil {
				return err
			}
		}
		r, err := d.order(ctx, "follow")
		if err != nil {
			return err
		}
		if r.Kind == tbm.KindAck {
			if try > 1 {
				d.enterNote(fmt.Sprintf("cmd follow after the summons was acknowledged at try %d", try))
			}
			return nil
		}
		if ref, bad := refusedOf(r); bad {
			last = ref.Code + ": " + ref.Message
			if !slices.Contains([]string{"busy", "cancelled", "in-combat", "tank-busy"}, ref.Code) {
				break
			}
		} else {
			last = "no ACK (" + replyText(r) + ")"
		}
	}
	return fmt.Errorf("enter: cmd follow after the summons was not confirmed (%s): the bots would follow the commander, who stays outside the instance, and be pulled out after the entry; nothing was sent through the portal", last)
}

// apartFromPortal names the lead bot and every configured bot that the feed does not show alive within portalTogether yards of the portal on
// its map: a bot missing from the feed, dead or on another map counts as apart.
func (d *RemoteDriver) apartFromPortal(pt *Portal) []string {
	var out []string
	for _, n := range append([]string{d.opt.Lead}, d.namedBots()...) {
		m, ok := d.memberByName(n)
		if !ok || m.Dead || m.Map != pt.Map || dist3(partyPoint(m), pt.Point) > portalTogether {
			out = append(out, n)
		}
	}
	return out
}

// portalLong is the preflight of the walk: with a path of more than portalLongWalk yards a GM account moves the party at once. Without a GM
// account the walk goes on as it was (a long walk is slow, not impossible).
func (d *RemoteDriver) portalLong(ctx context.Context, pt *Portal) (moved bool, err error) {
	n := d.portalPathLength(ctx, pt)
	if n <= portalLongWalk {
		return false, nil
	}
	h, why := d.portalGMHands(ctx)
	if h == nil {
		d.enterNote(fmt.Sprintf("the path to the portal is %.0f yd; walking it (%s)", n, why))
		return false, nil
	}
	if err := d.moveToPortalByGM(ctx, h, pt, fmt.Sprintf("the path to the portal is %.0f yd (more than %.0f)", n, portalLongWalk)); err != nil {
		return false, fmt.Errorf("enter: %w", err)
	}
	return true, nil
}

// portalUnreachable is the answer to a first move refused "unreachable": a GM account moves the party, anyone else fails the phase now.
func (d *RemoteDriver) portalUnreachable(ctx context.Context, pt *Portal, ref Refusal, alreadyMoved bool) error {
	if alreadyMoved {
		return d.portalNoPath(ref, "the party was moved to the portal by GM and is still refused there")
	}
	h, why := d.portalGMHands(ctx)
	if h == nil {
		return d.portalNoPath(ref, why)
	}
	if err := d.moveToPortalByGM(ctx, h, pt, fmt.Sprintf("the lead bot is refused (%s: %s)", ref.Code, ref.Message)); err != nil {
		return fmt.Errorf("enter: %w", err)
	}
	return nil
}
