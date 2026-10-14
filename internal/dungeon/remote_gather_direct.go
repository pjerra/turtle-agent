package dungeon

import (
	"context"
	"fmt"
	"math"
	"regexp"
	"strings"
	"time"
)

// Gather before and after every direct step (Uldaman hires-2 run 10, 20:09:42: the pit crossing's second direct step was refused
// `too-far: Hega is not with Jialebaeth ...`; the first step had put the lead bot 4 yd on, the followers were still running up, and the
// module takes a direct step only with every follower within 5 yd of the lead bot's spot (1 yd up or down, in its sight) or a navmesh path
// of at most 40 yd to it: TortoiseBots #948). The careful gather (remote_gather.go) waits for 12 yd and sends nothing; a direct step needs
// the party on the lead bot, so its gather is closer and acts:
//   - every living bot within directTogether yards of the lead bot (horizontally) and directTogetherDZ above or below it. 5 yd is what the
//     module takes for a straight join; `cmd follow` holds the bots 5-7 yd behind the lead bot, so a stricter bar was never met by it;
//   - nothing is sent for the first directMoveAfter (bots trailing the lead bot by a second or two arrive by themselves), then one
//     `cmd move <the lead bot's x y z>` (the party walks to the lead bot's spot), sent once more at directMoveAgainAfter for a bot still
//     apart. Never `cmd summon`: it set the bots' follow to the commander, who stood outside the instance, and 15 s later the whole party
//     was ejected to Northshire (Uldaman hires-3, run 1);
//   - once the party stands on the lead bot after such a move, one `cmd stay` holds everybody on that spot (nobody wanders off);
//   - the wait ends at directGatherWait whatever the bots do (the step is then sent anyway, and a too-far naming a bot is answered with
//     a regather: the move at once and again after directNamedMoveAgainAfter, then the step is retried).
//
// It runs before every send of a direct step and again once the lead bot has arrived, so the next step starts with the party up. After a
// wait that ran out the walk does not wait again for the same step (a bot that cannot be brought would cost the ceiling at every retry).
const (
	directTogether            = 5.0 // yards (horizontal) every living bot must be within of the lead bot (the module's straight-join limit)
	directTogetherDZ          = 1.5 // yards above or below the lead bot: the module wants the same floor (1 yd) and the feed rounds
	directMoveAfter           = 3 * time.Second
	directMoveAgainAfter      = 9 * time.Second
	directNamedMoveAgainAfter = 2 * time.Second
	directGatherWait          = 25 * time.Second
)

// farBotRe reads the bot out of the module's too-far answer to a direct step: "Hega is not with Jialebaeth (no navmesh path ...".
var farBotRe = regexp.MustCompile(`^(\S+) is not with `)

// farBotName is the bot a direct step's too-far refusal names, or "".
func farBotName(message string) string {
	if m := farBotRe.FindStringSubmatch(strings.TrimSpace(message)); m != nil {
		return m[1]
	}
	return ""
}

// directApart lists the living bots on the lead bot's map that are not on the lead bot for a direct step.
func (d *RemoteDriver) directApart() []awayBot {
	l, ok := d.fr.LeadRec()
	if !ok {
		return nil
	}
	lp := partyPoint(l)
	var out []awayBot
	for _, m := range d.fr.Party {
		if m.GUID == l.GUID || m.Dead || m.Map != l.Map {
			continue
		}
		p := partyPoint(m)
		if hdist(p, lp) <= directTogether && math.Abs(float64(p.Z-lp.Z)) <= directTogetherDZ {
			continue
		}
		out = append(out, awayBot{guid: m.GUID, name: m.Name, dist: dist3(p, lp)})
	}
	return out
}

// gatherDirect waits for the whole party to stand on the lead bot (see above), at most directGatherWait. named is a bot a too-far
// refusal named: it is brought at once. fight is true when a fight began (the caller's combat branch owns that); timedOut when the wait
// ran out (a note says who was still away) and the step goes on.
func (d *RemoteDriver) gatherDirect(ctx context.Context, why, named string) (fight, timedOut bool, err error) {
	if d.opt.GatherWait < 0 {
		return false, false, nil
	}
	began := d.clk.Now()
	moveAfter, againAfter := directMoveAfter, directMoveAgainAfter
	if named != "" {
		moveAfter, againAfter = 0, directNamedMoveAgainAfter
	}
	counted, moves := false, 0
	done := func() {
		if counted {
			el := d.clk.Now().Sub(began).Seconds()
			d.remote(func(r *RemoteReport) { r.DirectGatherWaitS += el })
		}
	}
	if named != "" {
		// The frame in hand is from before the refusal; the bot it names is judged by a fresh one.
		if err := d.tick(ctx, d.cfg.Poll); err != nil {
			return false, false, err
		}
	}
	for {
		if len(d.fr.EngagedHostiles()) > 0 {
			done()
			return true, false, nil
		}
		away := d.directApart()
		if !d.stale() && len(away) == 0 {
			done()
			if moves > 0 {
				if err := d.directStay(ctx, why); err != nil {
					return false, false, err
				}
			}
			return false, false, nil
		}
		if !counted {
			counted = true
			d.remote(func(r *RemoteReport) { r.DirectGathers++ })
		}
		el := d.clk.Now().Sub(began)
		if el >= directGatherWait {
			done()
			d.remote(func(r *RemoteReport) { r.DirectGatherTimeouts++ })
			d.rep.note(fmt.Sprintf("leg %d: the party did not stand on the lead bot in %.0f s before %s (%s); going on", d.curLeg, directGatherWait.Seconds(), why, awayText(away)))
			return false, true, nil
		}
		if len(away) > 0 && ((moves == 0 && el >= moveAfter) || (moves == 1 && el >= againAfter)) {
			moves++
			if err := d.directMove(ctx, why, away); err != nil {
				return false, false, err
			}
		}
		if err := d.tick(ctx, d.cfg.Poll); err != nil {
			return false, false, err
		}
	}
}

// directMove sends `cmd move <the lead bot's spot>`: the party walks to it. A refusal is a note, not a failure (the lead bot itself may be
// told it is already there).
func (d *RemoteDriver) directMove(ctx context.Context, why string, away []awayBot) error {
	here, ok := d.leadPos()
	if !ok {
		return nil
	}
	if err := d.moveGap(ctx); err != nil {
		return err
	}
	r, err := d.order(ctx, "move", moveArgs(here)...)
	if err != nil {
		return err
	}
	d.remote(func(rr *RemoteReport) { rr.DirectGatherMoves++ })
	if ref, bad := refusedOf(r); bad {
		d.rep.note(fmt.Sprintf("leg %d: cmd move to the lead bot before %s was refused (%s: %s)", d.curLeg, why, ref.Code, ref.Message))
		return nil
	}
	d.rep.note(fmt.Sprintf("leg %d: cmd move to the lead bot's spot %.1f %.1f %.1f before %s: the party stands apart from it (%s)", d.curLeg, here.X, here.Y, here.Z, why, awayText(away)))
	return nil
}

// directStay sends `cmd stay` once the party stands on the lead bot after a move: nobody wanders off before the step.
func (d *RemoteDriver) directStay(ctx context.Context, why string) error {
	r, err := d.order(ctx, "stay")
	if err != nil {
		return err
	}
	if ref, bad := refusedOf(r); bad {
		d.rep.note(fmt.Sprintf("leg %d: cmd stay before %s was refused (%s: %s)", d.curLeg, why, ref.Code, ref.Message))
		return nil
	}
	d.remote(func(rr *RemoteReport) { rr.DirectGatherStays++ })
	return nil
}
