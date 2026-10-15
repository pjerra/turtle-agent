package dungeon

import (
	"context"
	"fmt"

	"github.com/pjerra/turtle-agent/internal/objects"
)

// doGossip is the gossip step of the group driver: the leader is the agent's own character, so it is the one that talks, whatever `by`
// says (a note says so for carrier and tank). It walks to the plan's stand, or to a spot short of the NPC, and talks under the brain's move
// lock, like a use_object: a brain step must not carry the leader out of the NPC's reach between the hello and the select.
func (d *Driver) doGossip(ctx context.Context, a Action) error {
	g := a.Gossip
	tk, ok := d.g.(GossipTalker)
	if !ok {
		return fmt.Errorf("%s: this game cannot talk to an NPC (no gossip hands)", a.Label)
	}
	if g.Talker() != GossipByAgent {
		d.rep.note(fmt.Sprintf("%s: by %s: the agent leads this run and talks to the NPC itself", a.Label, g.Talker()))
	}
	find := func() gossipStatus { _, st := findGossipNPC(d.g.Units(), g); return st }
	// An NPC out of view is brought into view first: the objects load as the player nears them.
	if find() == gossipMissing {
		to := g.Near
		if g.Stand != nil {
			to = *g.Stand
		}
		if dist3(d.g.Position(), to) > ArriveRadius {
			if err := d.tracedWalk(ctx, to); err != nil {
				return err
			}
		}
		if err := gossipSettle(ctx, gossipViewLooks, d.sleep, find); err != nil {
			return err
		}
	}
	if u, st := findGossipNPC(d.g.Units(), g); st == gossipSeen {
		path := func(from, to Point) []Point {
			pts, err := d.g.PathPoints(ctx, to)
			if err != nil {
				return nil
			}
			return pts
		}
		if spot, walk := gossipSpot(g, d.g.Position(), Point{u.X, u.Y, u.Z}, path); walk {
			if err := d.tracedWalk(ctx, spot); err != nil {
				return err
			}
		}
	}
	t := &gossipTalk{
		g: g, tk: tk, now: d.clk.Now, pause: d.sleepHeld, note: d.rep.note,
		npc: func() (objects.Unit, gossipStatus) { return findGossipNPC(d.g.Units(), g) },
	}
	err := d.holdingMove(ctx, func() error { return t.run(ctx) })
	if cerr := d.check(ctx); cerr != nil {
		return cerr
	}
	if err != nil {
		return fmt.Errorf("%s: %w", a.Label, err)
	}
	return nil
}
