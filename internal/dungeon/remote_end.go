package dungeon

import (
	"context"
	"fmt"
	"time"
)

// The end of a run with the party still fighting. A failure (or a stop) used to send `cmd stay` and unbind at once; the bots then stood
// unbound in the middle of the pack that had aggroed them and four of them died (remote Uldaman run 2). So the binding is kept, and the
// fight logic (tank first, attack orders) keeps running, until the party is out of combat; only then do the stay and the unbind go.

// DefaultEndFightWait is how long the end of a run keeps the binding for a fight still on (RemoteOptions.EndFightWait).
const DefaultEndFightWait = 90 * time.Second

// partyFighting says whether the party is in a fight in the newest frame: a hostile that fights it is listed, or a living bot is in
// combat. A party that is all dead is not fighting (nobody is left to command).
func (d *RemoteDriver) partyFighting() bool {
	if !d.haveFr || d.fr.AllDead() {
		return false
	}
	if len(d.fr.EngagedHostiles()) > 0 {
		return true
	}
	for _, p := range d.fr.Party {
		if p.InCombat() {
			return true
		}
	}
	return false
}

// finishFights runs before the stay and the unbind of the run's end, on every way out: it fights on while the party is in combat, for at
// most EndFightWait. It uses its own context, so that a stopped run (a cancelled context) still gets the party out of the fight.
func (d *RemoteDriver) finishFights() {
	if !d.bound || d.opt.EndFightWait < 0 {
		return
	}
	defer func() {
		if r := recover(); r != nil { // the end must go on to the unbind whatever happens here
			d.rep.note(fmt.Sprintf("end of run: the fight loop panicked: %v", r))
		}
	}()
	d.look()
	if d.endedByModule() != nil || !d.partyFighting() {
		return
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(context.Background()), d.opt.EndFightWait+30*time.Second)
	defer cancel()
	began := d.clk.Now()
	deadline := began.Add(d.opt.EndFightWait)
	d.rep.note(fmt.Sprintf("end of run: the party is still in combat; keeping the binding to finish the fight (up to %.0f s)", d.opt.EndFightWait.Seconds()))
	result := ""
	for result == "" {
		switch {
		case !d.partyFighting():
			result = fmt.Sprintf("out of combat after %.0f s", d.clk.Now().Sub(began).Seconds())
		case !d.clk.Now().Before(deadline):
			result = fmt.Sprintf("still in combat after %.0f s; gave up", d.opt.EndFightWait.Seconds())
		default:
			if eng := d.fr.EngagedHostiles(); len(eng) > 0 && !d.stale() {
				d.watchThreat(eng)
				if err := d.tankFirstTick(ctx, eng); err != nil {
					result = "interrupted: " + err.Error()
					continue
				}
				if err := d.assist(ctx, eng); err != nil {
					result = "interrupted: " + err.Error()
					continue
				}
			}
			if err := d.tick(ctx, d.cfg.Poll); err != nil {
				result = "interrupted: " + err.Error()
			}
		}
	}
	d.remote(func(r *RemoteReport) { r.EndFight = result })
	d.rep.note("end of run: fight " + result)
}
