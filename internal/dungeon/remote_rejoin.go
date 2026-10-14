package dungeon

import (
	"context"
	"fmt"
	"math"
	"strings"
	"time"

	"github.com/pjerra/turtle-agent/internal/packet"
	"github.com/pjerra/turtle-agent/internal/tbm"
)

// Bring a bot that spawned far from the party back to it (owner order 2026-10-15: "if a party member dies and corpse-runs and spawns at
// the entrance far away from the group, the commander should teleport the party member to the rest of the group").
//
// A bot that died, was released and got up again (a single death, or the recovery of a wipe) stands at the entrance, or outside the
// instance, and runs a long way back alone. While it does, the party either waits for it at every gather (the hop and pull gathers give up
// after gather_wait_s and then ignore the bot for the leg) or walks on without it. The cheapest mechanism the module already has is the
// commander's `.bot cmd summon <bot>` (owner or GM, TortoiseBots HandleSummon): the bot is teleported, across maps too, to the lead bot
// (3 s, out of combat, refused while the bot is dead or in combat). It only ever names a bot of the bound party, and only a GM account
// sends it (the same rule as keep-near's `.appear`): every one is a GM assist during the run (run_gm_assists, setup_shortcuts) and is
// counted in rejoin_tps.
//
// When it runs: a living bot of the run (not the lead bot, one the run named) farther than rejoin_tp_dist yards from the lead bot, or on
// another map, with the party out of combat for rejoinCalm; 0 turns it off. It runs before each hop of an in-dungeon walk and at each rest gate,
// the two places the run would otherwise go on without the bot. The wait is bounded (rejoinMaxWait for the whole call, rejoinLand for one
// summon to land) and a bot is summoned at most once per rejoin_tp_gap_s; a bot that does not land is left to the gathers.
//
// `cmd summon` makes the commander the bot's master and puts it on follow (hires-3 run 1: a summon with the commander outside the instance
// ejected the party, which is why gathers never use it). So once a summoned bot has landed, one `cmd follow` puts the party back on the
// lead bot at once.

const (
	rezReach = 30.0 // yards: a dead bot's body this near a living caster is left to the caster's resurrection (rez_reach_yd)

	rejoinTPDist  = 60.0 // yards from the lead bot beyond which a living bot is brought back (0: off)
	rejoinGap     = 60 * time.Second
	rejoinCalm    = 4 * time.Second  // the party must have been out of combat this long
	rejoinLand    = 15 * time.Second // how long one summon is waited for (the module takes 3 s; a map change loads)
	rejoinMaxWait = 90 * time.Second // ceiling of one call
)

// rejoinState is the driver's state of the behaviour.
type rejoinState struct {
	last  map[packet.GUID]time.Time // when each bot was last summoned
	noted map[string]bool
}

func (r *rejoinState) once(key string) bool {
	if r.noted == nil {
		r.noted = map[string]bool{}
	}
	if r.noted[key] {
		return false
	}
	r.noted[key] = true
	return true
}

// rejoinBot is a living bot of the run that stands too far from the lead bot.
type rejoinBot struct {
	guid  packet.GUID
	name  string
	dist  float64 // +Inf on another map
	other bool
}

// rejoinFarBots lists the living bots of the run that are farther than yd from the lead bot or on another map. Only a bot the run named
// (RemoteOptions.Bots; without names the bound party) and never the lead bot.
func (d *RemoteDriver) rejoinFarBots(yd float64) []rejoinBot {
	l, ok := d.fr.LeadRec()
	if !ok {
		return nil
	}
	var out []rejoinBot
	for _, m := range d.fr.Party {
		if m.GUID == l.GUID || strings.EqualFold(m.Name, d.opt.Lead) || m.Dead || m.State == "d" {
			continue
		}
		if len(d.opt.Bots) > 0 && !nameIn(d.opt.Bots, m.Name) {
			continue
		}
		if m.Map != l.Map {
			out = append(out, rejoinBot{guid: m.GUID, name: m.Name, dist: math.Inf(1), other: true})
		} else if dd := dist3(partyPoint(m), partyPoint(l)); dd > yd {
			out = append(out, rejoinBot{guid: m.GUID, name: m.Name, dist: dd})
		}
	}
	return out
}

func nameIn(list []string, n string) bool {
	for _, x := range list {
		if strings.EqualFold(x, n) {
			return true
		}
	}
	return false
}

func rejoinText(bs []rejoinBot) string {
	var out []string
	for _, b := range bs {
		if b.other {
			out = append(out, b.name+" on another map")
		} else {
			out = append(out, fmt.Sprintf("%s %.0f yd", b.name, b.dist))
		}
	}
	return strings.Join(out, ", ")
}

// rejoinFar brings the far bots to the lead bot with `cmd summon` and waits, within the bounds above, for them to land. It returns at
// once when the behaviour is off, the run is outside the dungeon, the agent is no GM, nobody is far, or the party fights (the caller's fight
// branch owns that; it is called again after). Its error is the run's.
func (d *RemoteDriver) rejoinFar(ctx context.Context) error {
	yd := float64(d.tx.RejoinTPDist)
	if yd <= 0 || d.curLeg < 0 || d.outside != nil {
		return nil
	}
	d.look()
	if !d.haveFr || d.stale() || d.inCombat() || len(d.rejoinFarBots(yd)) == 0 {
		return nil
	}
	f, ok := d.g.(Follower)
	if !ok || !d.agentGM(ctx, f) {
		if d.rejoin.once("nogm") {
			d.rep.note("rejoin: a bot is far from the lead bot, but only a GM account brings it back (cmd summon is a GM assist during the run); the gathers wait for it as before")
		}
		return nil
	}
	began := d.clk.Now()
	calmFrom := began
	if d.rejoin.last == nil {
		d.rejoin.last = map[packet.GUID]time.Time{}
	}
	sent := map[packet.GUID]time.Time{} // summons of this call that have not landed yet
	landed := false
	for {
		d.look()
		far := d.rejoinFarBots(yd)
		now := d.clk.Now()
		// A summoned bot that now stands near the lead bot has landed.
		for g, at := range sent {
			if !farHas(far, g) {
				if m, ok := d.fr.Member(g); ok && !m.Dead {
					d.rep.note(fmt.Sprintf("leg %d: rejoin: %s is back at the lead bot %.0f s after the summon", d.curLeg, m.Name, now.Sub(at).Seconds()))
					landed = true
				}
				delete(sent, g)
			}
		}
		if len(far) == 0 || d.inCombat() || d.stale() {
			break
		}
		if now.Sub(began) > rejoinMaxWait {
			if d.rejoin.once(fmt.Sprintf("max-%d", d.curLeg)) {
				d.rep.note(fmt.Sprintf("leg %d: rejoin: %s still far from the lead bot after %.0f s; going on", d.curLeg, rejoinText(far), rejoinMaxWait.Seconds()))
			}
			break
		}
		if now.Sub(calmFrom) < rejoinCalm {
			if err := d.tick(ctx, d.cfg.Poll); err != nil {
				return err
			}
			continue
		}
		pending := false
		for _, b := range far {
			if at, ok := sent[b.guid]; ok {
				if now.Sub(at) < rejoinLand {
					pending = true
				}
				continue
			}
			if last, ok := d.rejoin.last[b.guid]; ok && now.Sub(last) < d.tx.RejoinGap {
				continue
			}
			if d.rejoinSummon(ctx, b) {
				sent[b.guid] = d.clk.Now()
				pending = true
			}
		}
		if !pending {
			break // every far bot is in its cooldown, was refused, or did not land: the gathers take over
		}
		if err := d.tick(ctx, d.cfg.Poll); err != nil {
			return err
		}
	}
	if landed {
		// The summon made the commander the bot's master and put it on follow: the party follows the lead bot again.
		r, err := d.order(ctx, "follow")
		if err != nil {
			return err
		}
		if ref, bad := refusedOf(r); bad {
			d.rep.note(fmt.Sprintf("leg %d: rejoin: cmd follow after the summon was refused: %s (%s)", d.curLeg, ref.Code, ref.Message))
		}
	}
	return nil
}

func farHas(far []rejoinBot, g packet.GUID) bool {
	for _, b := range far {
		if b.guid == g {
			return true
		}
	}
	return false
}

// rejoinSummon sends `cmd summon <bot>` for one far bot and reports whether the module took it. The counters, the trace event and the
// setup_shortcuts line are the GM assist's.
func (d *RemoteDriver) rejoinSummon(ctx context.Context, b rejoinBot) bool {
	d.rejoin.last[b.guid] = d.clk.Now()
	r, err := d.order(ctx, "summon", b.name)
	cmd := "cmd summon " + b.name
	if err != nil {
		d.rep.note(fmt.Sprintf("leg %d: rejoin: %s failed: %v", d.curLeg, cmd, err))
		return false
	}
	ev := map[string]any{"leg": d.curLeg, "bot": b.name, "reply": r.Kind, "other_map": b.other}
	if !b.other {
		ev["dist"] = math.Round(b.dist)
	}
	if ref, bad := refusedOf(r); bad {
		ev["code"] = ref.Code
		d.tr.Event("rejoin_tp", ev)
		d.rep.note(fmt.Sprintf("leg %d: rejoin: %s was refused: %s (%s); %s", d.curLeg, cmd, ref.Code, ref.Message, rejoinText([]rejoinBot{b})))
		return false
	}
	if r.Kind != tbm.KindAck {
		ev["code"] = "no-ack"
		d.tr.Event("rejoin_tp", ev)
		d.rep.note(fmt.Sprintf("leg %d: rejoin: %s was not acknowledged (%s)", d.curLeg, cmd, r.Kind))
		return false
	}
	d.tr.Event("rejoin_tp", ev)
	d.rep.update(func(rp *Report) { rp.SetupShortcuts = append(rp.SetupShortcuts, cmd+" (GM assist during the run)") })
	d.remote(func(rr *RemoteReport) {
		rr.RunGMAssists++
		rr.RejoinTPs++
	})
	d.rep.note(fmt.Sprintf("leg %d: rejoin: %s is far from the lead bot (%s); %s, a GM assist during the run (rejoin_tps, run_gm_assists; not counted in gm_commands)", d.curLeg, b.name, rejoinText([]rejoinBot{b}), cmd))
	return true
}
