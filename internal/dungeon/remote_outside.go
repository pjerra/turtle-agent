package dungeon

import (
	"context"
	"fmt"
	"strings"
	"time"
)

// A party released outside the instance (zf-r5, Velratha wipe: the tank died last, was spirit-healed at the Tanaris graveyard on map 1 and the
// others followed it out through the exit; two resumes then failed "unreachable" because the lead bot, on map 1, had no path to a point of
// map 209, until a GM summoned the bots to the door by hand).
//
// With the agent's own character inside the instance, bringPartyIn takes the lead bot in with `.summon`. In a `cmd enter` run (TortoiseBots
// #939) the agent stays OUTSIDE, at the door, so the way back is the way in: every bot that is off the plan's map is brought to the commander
// with the module's `cmd summon <bot>` (a GM assist; the commander is at the door, so the bots land there), the party is put on follow, and
// the run's entry method brings it in again (`cmd enter` / the walk / the tele), as the first entry did. The walk back from the instance's
// entrance to the leg is the leg's own.

// outsideSummonWait bounds the wait for the summoned bots to stand with the commander.
const outsideSummonWait = 45 * time.Second

// reenterFromOutside brings a party that stands outside the plan's map back into it from the commander's place outside. It needs a GM
// account (cmd summon of bots that are far or on another map, like the rejoin) and the run's entry game.
func (d *RemoteDriver) reenterFromOutside(ctx context.Context, f Follower) error {
	e, ok := d.g.(Enterer)
	if !ok {
		return fmt.Errorf("recovery: the party stands outside the dungeon (map %d) and this game cannot enter a dungeon again", d.plan.Map)
	}
	defer d.wd.hold()()
	began := d.clk.Now()
	at, am, _ := f.AgentPlace()
	var off []string
	for _, m := range d.offMapMembers() {
		off = append(off, m.Name)
	}
	d.enterNote(fmt.Sprintf("recovery: %s outside the dungeon, the commander is on map %d at %.0f %.0f: cmd summon to the commander, then in again", strings.Join(off, ", "), am, at.X, at.Y))
	for i, name := range off {
		if i > 0 {
			if err := d.tick(ctx, d.opt.SummonGap); err != nil {
				return err
			}
		}
		if err := d.summon(ctx, name); err != nil {
			return err
		}
		cmd := "cmd summon " + name
		d.rep.update(func(r *Report) {
			r.SetupShortcuts = append(r.SetupShortcuts, cmd+" (GM assist during the run: recovery outside the dungeon)")
		})
		d.remote(func(r *RemoteReport) {
			r.RunGMAssists++
			r.RejoinTPs++
		})
	}
	// Wait for them to stand with the commander (on its map, within portalTogether of it when the portal is where it stands).
	until := d.clk.Now().Add(outsideSummonWait)
	for {
		d.look()
		var apart []string
		for _, name := range off {
			m, ok := d.memberByName(name)
			if !ok || m.Dead || m.Map != am {
				apart = append(apart, name)
			}
		}
		if len(apart) == 0 {
			break
		}
		if !d.clk.Now().Before(until) {
			return fmt.Errorf("recovery: %s did not land with the commander on map %d %.0f s after cmd summon: the party cannot be brought back in", strings.Join(apart, ", "), am, outsideSummonWait.Seconds())
		}
		if err := d.tick(ctx, d.cfg.Poll); err != nil {
			return err
		}
	}
	// `cmd summon` made the commander the bots' master and put them on follow of it: the party follows the lead bot again before the entry.
	if err := d.followAfterSummons(ctx); err != nil {
		return err
	}
	method, err := d.entryMethod(ctx, e)
	if err != nil {
		return err
	}
	switch method {
	case EnterGM:
		err = d.enterByTele(ctx, e, began)
	case EnterCmd:
		err = d.enterByCmd(ctx, began)
	default:
		err = d.enterByWalk(ctx, began)
	}
	if err != nil {
		return err
	}
	if err := d.summonRest(ctx, began); err != nil {
		return err
	}
	if err := d.verifyInside(ctx, began); err != nil {
		return err
	}
	d.enterNote(fmt.Sprintf("recovery: every bot is on map %d again, %.0f s after the party was found outside", d.plan.Map, d.clk.Now().Sub(began).Seconds()))
	return nil
}
