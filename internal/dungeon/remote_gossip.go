package dungeon

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/pjerra/turtle-agent/internal/objects"
	"github.com/pjerra/turtle-agent/internal/tbm"
)

// The gossip step in remote mode (Gossip in plan.go). `by` says who talks:
//
//	agent    the agent's own character: it is brought up to the party (keepNearOpt: `.appear <lead bot>` for a GM that the setup made invisible,
//	         else a walk), walks to the plan's stand or to a spot short of the NPC (never through a fight), and uses GossipHello and
//	         GossipSelect (gossip.go). It stays where it ends: the step does not walk it back.
//	carrier, tank
//	         the bot gets the order `cmd gossip <npc entry> <bot> <option>` (TortoiseBots #1012), after the party has been walked to the NPC
//	         like for a chest (approachObject); its ACK ends the step. A module that does not know the verb (an unknown-verb refusal, or no
//	         answer at all to the order) makes the agent take the step once, noted and counted as the step's fallback.
//
// The option is the index, or the text (the words of the line, the module matches them as a case-insensitive substring: the order's words
// must be commander arguments, which the plan loader checks). What the NPC does next (a door opens, a unit turns hostile) is checked by the
// wait_for that follows.

// gossipWalkMax is the ceiling of the agent's walk to the NPC.
const gossipWalkMax = 90 * time.Second

// doGossip is the gossip step.
func (d *RemoteDriver) doGossip(ctx context.Context, a Action) error {
	g := a.Gossip
	by := g.Talker()
	who := "agent"
	var bot tbm.PartyRec
	if by != GossipByAgent {
		var err error
		if bot, err = d.gossipBot(by); err != nil {
			return fmt.Errorf("%s: %w", a.Label, err)
		}
		who = bot.Name
	}
	rec := d.newObjRecAs(a.Leg, a.Label, "gossip", who, false)
	var err error
	if by == GossipByAgent {
		err = d.gossipByAgent(ctx, a)
	} else {
		err = d.gossipByBot(ctx, a, rec, bot)
	}
	if err != nil {
		rec.finish("failed", err, "")
		return fmt.Errorf("%s: %w", a.Label, err)
	}
	rec.finish("done", nil, "")
	return nil
}

// gossipBot is the bot that talks for by carrier or tank.
func (d *RemoteDriver) gossipBot(by string) (tbm.PartyRec, error) {
	if by == GossipByCarrier {
		return d.carrierBot()
	}
	d.look()
	var m tbm.PartyRec
	ok := false
	switch {
	case strings.TrimSpace(d.opt.Tank) != "":
		m, ok = d.memberByName(d.opt.Tank)
	case d.tank != 0:
		m, ok = d.fr.Member(d.tank)
	}
	switch {
	case !ok:
		return m, errors.New("by tank: no tank is known (the run's tank option names none, and no pull has shown which bot holds threat yet)")
	case m.Dead:
		return m, fmt.Errorf("the tank %s is dead", m.Name)
	}
	return m, nil
}

// gossipOrder is the order of a bot's talk: `cmd gossip <npc entry> <bot> <option>`. The order counts the menu from 1 (TortoiseBots
// GossipOrderPolicy.h refuses 0), while the plan's option is the menu's own index from 0, so the order carries option + 1.
func gossipOrder(g *Gossip, bot string) string {
	opt := g.Text
	if g.Option != nil {
		opt = strconv.FormatUint(uint64(*g.Option)+1, 10)
	}
	return fmt.Sprintf("cmd gossip %d %s %s", g.NPC, bot, opt)
}

// noGossipVerb says whether the answer to the order means the module does not know it: an unknown-verb refusal, or no answer at all (not
// even ACTION_PENDING), which is what a module without the verb gives (as for `cmd enter`).
func noGossipVerb(res tbm.OrderResult) bool {
	if res.Err != nil {
		return unknownVerb(res.Err.Code)
	}
	return res.Kind == tbm.KindTimeout && res.Pending == nil
}

// gossipByBot orders the bot to talk and reads its answer.
func (d *RemoteDriver) gossipByBot(ctx context.Context, a Action, rec *objRec, bot tbm.PartyRec) error {
	g := a.Gossip
	if _, ok := d.orderGame(); !ok {
		return errors.New("this game cannot send player orders (cmd gossip)")
	}
	// An NPC the agent's own view shows dead is not worth an order.
	if us, ok := d.g.(UnitSource); ok {
		if u, st := findGossipNPC(us.Units(), g); st == gossipDead {
			return fmt.Errorf("%s (NPC %d) is dead: it cannot talk", g.npcName(u), g.NPC)
		}
	}
	near := g.Near
	stayed, err := d.approachObject(ctx, rec, bot.Name, &near, g.Stand)
	if err != nil {
		return err
	}
	defer d.resumeFollow(ctx, stayed)
	command := gossipOrder(g, bot.Name)
	until := d.clk.Now().Add(g.Retry())
	for {
		res, oerr := d.carry(ctx, rec, command, &near)
		if oerr == nil {
			d.rep.note(fmt.Sprintf("%s: %s talked to NPC %d (%s): %s", a.Label, bot.Name, g.NPC, g.OptionText(), res.Describe()))
			return d.waitRejoin(ctx, bot.Name)
		}
		if runEnded(ctx, oerr) {
			return oerr
		}
		if noGossipVerb(res) {
			reason := fmt.Sprintf("the module has no `cmd gossip` (%s)", res.Describe())
			d.rep.note(fmt.Sprintf("%s: %s: the agent talks to the NPC itself (TortoiseBots #1012 is not in this module)", a.Label, reason))
			d.tr.Event("gossip_fallback", map[string]any{"leg": a.Leg, "bot": bot.Name, "reason": reason})
			rec.fb(func(f *FallbackReport) { f.Reason, f.Did = reason, "gossip by the agent" })
			if ferr := d.gossipByAgent(ctx, a); ferr != nil {
				rec.fb(func(f *FallbackReport) { f.Result, f.Error = "failed", ferr.Error() })
				return fmt.Errorf("%w; the agent's fallback failed too: %v", oerr, ferr)
			}
			rec.fb(func(f *FallbackReport) { f.Result = "done" })
			return d.waitRejoin(ctx, bot.Name)
		}
		// Any other refusal: the phase may lag one update behind the event that adds the option, so it is sent again for retry_s.
		if !d.clk.Now().Before(until) {
			if g.FailText != "" {
				oerr = fmt.Errorf("%w: %s", oerr, g.FailText)
			}
			return fmt.Errorf("%w (after %.0f s)", oerr, g.Retry().Seconds())
		}
		d.rep.note(fmt.Sprintf("%s: %s: sending it again", a.Label, oerr))
		if terr := d.tick(ctx, gossipPoll); terr != nil {
			return terr
		}
	}
}

// gossipByAgent is the agent's own character talking.
func (d *RemoteDriver) gossipByAgent(ctx context.Context, a Action) error {
	g := a.Gossip
	tk, okT := d.g.(GossipTalker)
	f, okF := d.g.(Follower)
	us, okU := d.g.(UnitSource)
	if !okT || !okF || !okU {
		return errors.New("the agent has no hands for a gossip step in this game")
	}
	if err := d.fallbackGate(ctx, "talk to an NPC"); err != nil {
		return err
	}
	units := func() (objects.Unit, gossipStatus) { return findGossipNPC(us.Units(), g) }
	if _, err := d.keepNearOpt(ctx, true); err != nil {
		return err
	}
	// An NPC out of view is brought into view first: the objects load as the player nears them.
	if _, st := units(); st == gossipMissing {
		to := g.Near
		if g.Stand != nil {
			to = *g.Stand
		}
		if at, _, here := f.AgentPlace(); here && dist3(at, to) > ArriveRadius {
			if ok, err := d.agentWalk(ctx, f, to, gossipWalkMax); err != nil {
				return err
			} else if !ok {
				return fmt.Errorf("the agent could not walk to %.1f %.1f %.1f to bring NPC %d into view", to.X, to.Y, to.Z, g.NPC)
			}
		}
		if err := gossipSettle(ctx, gossipViewLooks, d.tick, func() gossipStatus { _, st := units(); return st }); err != nil {
			return err
		}
	}
	if u, st := units(); st == gossipSeen {
		at, _, here := f.AgentPlace()
		if !here {
			return errors.New("the agent's own character is not in the world")
		}
		path := func(from, to Point) []Point {
			if pl, ok := d.g.(PathPlanner); ok {
				if pts, err := pl.PlanPath(ctx, d.plan.Map, from, to); err == nil {
					return pts
				}
			}
			return nil
		}
		if spot, walk := gossipSpot(g, at, Point{u.X, u.Y, u.Z}, path); walk {
			ok, err := d.agentWalk(ctx, f, spot, gossipWalkMax)
			if err != nil {
				return err
			}
			if !ok {
				return fmt.Errorf("the agent could not walk to %.1f %.1f %.1f next to %s", spot.X, spot.Y, spot.Z, g.npcName(u))
			}
		}
	}
	t := &gossipTalk{g: g, tk: tk, now: d.clk.Now, pause: d.tick, note: d.rep.note, npc: units}
	return t.run(ctx)
}
