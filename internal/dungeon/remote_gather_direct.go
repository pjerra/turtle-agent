package dungeon

import (
	"context"
	"fmt"
	"math"
	"regexp"
	"strings"
	"time"

	"github.com/pjerra/turtle-agent/internal/packet"
)

// Gather before and after every direct step (Uldaman hires-2 run 10, 20:09:42: the pit crossing's second direct step was refused
// `too-far: Hega is not with Jialebaeth ...`; the first step had put the lead bot 4 yd on, the followers were still running up, and the
// module takes a direct step only with every follower within 5 yd of the lead bot's spot (1 yd up or down, in its sight) or a navmesh path
// of at most 40 yd to it: TortoiseBots #948). The careful gather (remote_gather.go) waits for 12 yd and sends nothing; a direct step needs
// the party on the lead bot, so its gather is closer and acts:
//   - every living bot within directTogether yards of the lead bot (horizontally) and directTogetherDZ above or below it;
//   - nothing is sent for the first directFollowAfter (bots trailing the lead bot by a second or two arrive by themselves), then one
//     `cmd follow` (the followers form up on the lead bot, which keeps its spot);
//   - a bot still apart directSummonAfter into the wait gets `cmd summon <bot>`, once: a last resort (a bot that cannot path to the lead
//     bot, or stands stuck on a ledge). It is a bot command, not a GM one (orders.summon, direct_gather_summons; gm_commands stays 0);
//   - the wait ends at directGatherWait whatever the bots do (the step is then sent anyway, and a too-far naming a bot is answered with
//     a regather of that bot: follow at once and summon after directNamedSummonAfter, then the step is retried).
//
// It runs before every send of a direct step and again once the lead bot has arrived, so the next step starts with the party up. After a
// wait that ran out the walk does not wait again for the same step (a bot that cannot be brought would cost the ceiling at every retry).
const (
	directTogether         = 3.0 // yards (horizontal) every living bot must be within of the lead bot
	directTogetherDZ       = 1.5 // yards above or below the lead bot: the module wants the same floor (1 yd) and the feed rounds
	directFollowAfter      = 3 * time.Second
	directSummonAfter      = 9 * time.Second
	directNamedSummonAfter = 2 * time.Second
	directGatherWait       = 25 * time.Second
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
	followAfter, summonAfter := directFollowAfter, directSummonAfter
	if named != "" {
		followAfter, summonAfter = 0, directNamedSummonAfter
	}
	counted, followed := false, false
	summoned := map[packet.GUID]bool{}
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
		if !followed && el >= followAfter && len(away) > 0 {
			followed = true
			if err := d.directFollow(ctx, why, away); err != nil {
				return false, false, err
			}
		}
		if el >= summonAfter {
			for _, b := range away {
				if summoned[b.guid] {
					continue
				}
				summoned[b.guid] = true
				if err := d.directSummon(ctx, why, b); err != nil {
					return false, false, err
				}
			}
		}
		if err := d.tick(ctx, d.cfg.Poll); err != nil {
			return false, false, err
		}
	}
}

// directFollow sends `cmd follow` (the followers form up on the lead bot, which holds its spot). A refusal is a note, not a failure: the
// summon after it is the fallback.
func (d *RemoteDriver) directFollow(ctx context.Context, why string, away []awayBot) error {
	r, err := d.order(ctx, "follow")
	if err != nil {
		return err
	}
	d.remote(func(rr *RemoteReport) { rr.DirectGatherFollows++ })
	if ref, bad := refusedOf(r); bad {
		d.rep.note(fmt.Sprintf("leg %d: cmd follow before %s was refused (%s: %s)", d.curLeg, why, ref.Code, ref.Message))
		return nil
	}
	d.rep.note(fmt.Sprintf("leg %d: cmd follow before %s: the party stands apart from the lead bot (%s)", d.curLeg, why, awayText(away)))
	return nil
}

// directSummon sends `cmd summon <bot>` for a bot that did not come up by itself. The module answering busy or in-combat is a note: the
// wait goes on to its ceiling.
func (d *RemoteDriver) directSummon(ctx context.Context, why string, b awayBot) error {
	r, err := d.order(ctx, "summon", b.name)
	if err != nil {
		return err
	}
	if ref, bad := refusedOf(r); bad {
		d.rep.note(fmt.Sprintf("leg %d: cmd summon %s before %s was refused (%s: %s)", d.curLeg, b.name, why, ref.Code, ref.Message))
		return nil
	}
	d.remote(func(rr *RemoteReport) { rr.DirectGatherSummons++ })
	d.rep.note(fmt.Sprintf("leg %d: cmd summon %s before %s: it stood %.0f yd from the lead bot", d.curLeg, b.name, why, b.dist))
	return nil
}
