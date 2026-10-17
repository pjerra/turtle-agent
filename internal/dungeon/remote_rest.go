package dungeon

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/pjerra/turtle-agent/internal/tbm"
)

// Drink refills at a rest gate (remote mode). The preflight sends `.bot maintenance <bot>` once, and TortoiseBots buys only 6-7 drinks
// a bot: a long dungeon runs dry. At a gate where a mana user is short of mana, the driver sends `.bot maintenance <bot>` again, at
// most once per bot per leg, when
//   - the bot's drink count (the last `.bot inv` read; read afresh once per bot per leg) is under Config.RefillBelow, or
//   - its drink nudge fired (the mana did not rise at all for DrinkNudge), or
//   - the feed says its last action was a drink that failed for want of one (action "drink", result F, reason no-item, TB #924), or a
//     drink that failed for any reason (TortoiseBots reports item-action failures as `drink,F,-`, no reason) in two different frames
//     within DrinkFailWindow,
//
// then reads `.bot inv` again for the count it came to. Without a BotLiner, or with maintenance off on the server, nothing is sent.

// Refill is one `.bot maintenance` sent again during a run.
type Refill struct {
	Bot string `json:"bot"`
	Leg int    `json:"leg"`
	// Why is "low-drinks" (the count was under the limit), "nudge" (the mana did not rise), "no-item" (the feed said the drink failed
	// for want of one) or "drink-failed" (it failed in two frames within DrinkFailWindow, no reason given).
	Why string `json:"why"`
	// Before and After are the drinks the bags held before and after (-1: not read). Maintenance is its verdict ("maintained", ...).
	Before      int    `json:"before"`
	After       int    `json:"after"`
	Maintenance string `json:"maintenance"`
}

// DrinkFailWindow is the time in which a drink that fails in two different frames counts as a bot that cannot drink.
const DrinkFailWindow = 10 * time.Second

// restKit is what the rest gates know of the bots' bags.
type restKit struct {
	drinks  map[string]int // the drinks each bot held at its last `.bot inv` read (by lower-case name)
	readLeg map[string]int // the leg of that read (-1: the preflight)
	doneLeg map[string]int // the leg a bot was refilled in
	off     bool           // maintenance is off on the server, or the game cannot send it: no more refills
	failAt  map[string][]failSight
}

// failSight is one frame in which a bot's last action was a failed drink.
type failSight struct {
	frame uint64
	at    time.Time
}

// drinkFailing notes the frame (n) in which m's last action is a failed drink and says whether it has been one in another frame within
// DrinkFailWindow before. A bot whose last action is anything else forgets its sightings.
func (k *restKit) drinkFailing(m tbm.PartyRec, n uint64, now time.Time) bool {
	k.init()
	key := strings.ToLower(m.Name)
	if !m.DrinkFailed() {
		delete(k.failAt, key)
		return false
	}
	var keep []failSight
	for _, f := range k.failAt[key] {
		if now.Sub(f.at) <= DrinkFailWindow {
			keep = append(keep, f)
		}
	}
	if len(keep) == 0 || keep[len(keep)-1].frame != n {
		keep = append(keep, failSight{n, now})
	}
	if k.failAt == nil {
		k.failAt = map[string][]failSight{}
	}
	k.failAt[key] = keep
	return len(keep) >= 2
}

func (k *restKit) init() {
	if k.drinks == nil {
		k.drinks, k.readLeg, k.doneLeg = map[string]int{}, map[string]int{}, map[string]int{}
	}
}

func (k *restKit) set(name string, n, leg int) {
	k.init()
	name = strings.ToLower(name)
	k.drinks[name], k.readLeg[name] = n, leg
}

// readDrinks reads `.bot inv <bot>` and the drinks it holds; ok is false when no inventory came.
func (d *RemoteDriver) readDrinks(ctx context.Context, bl BotLiner, name string) (int, bool) {
	lines, settled, err := bl.BotLines(ctx, "inv "+name, 8*time.Second, func(line string) bool {
		return strings.HasPrefix(line, "TBM:INV_END|") || strings.HasPrefix(line, "TBM:ACTION_ERR|inv|")
	})
	inv := tbm.ParseInventory(lines)
	if err != nil || !settled || inv.Err != nil || !inv.Complete {
		return 0, false
	}
	n := invCount(inv, drinkSet)
	d.kit.set(name, n, d.curLeg)
	return n, true
}

// refillWhy says why the short caster m is due a refill, or "" when it is not. It reads the bot's bags when its count is older than
// the leg (once per bot per leg).
func (d *RemoteDriver) refillWhy(ctx context.Context, bl BotLiner, m tbm.PartyRec, nudged, failing bool) string {
	switch {
	case m.DrinkFailedNoItem():
		return "no-item"
	case failing:
		return "drink-failed"
	case nudged:
		return "nudge"
	}
	key := strings.ToLower(m.Name)
	if leg, ok := d.kit.readLeg[key]; !ok || leg != d.curLeg {
		if _, ok := d.readDrinks(ctx, bl, m.Name); !ok {
			return ""
		}
	}
	if d.kit.drinks[key] < d.cfg.RefillBelow {
		return "low-drinks"
	}
	return ""
}

// refill sends `.bot maintenance` again to the casters of st that are short of mana and due it, and reads their bags afterwards.
// It returns who was refilled; the gate then orders the party to drink again.
func (d *RemoteDriver) refill(ctx context.Context, rw *restWatch, rec *gateRec) ([]string, error) {
	d.kit.init()
	if d.cfg.RefillBelow < 0 || d.kit.off {
		return nil, nil
	}
	var done []string
	for _, m := range d.fr.Party {
		if m.Dead || !m.UsesMana() {
			continue
		}
		healer := d.opt.Healer == "" || strings.EqualFold(d.opt.Healer, m.Name)
		if m.PowerPct() >= rw.tgt.manaNeed(healer) {
			continue
		}
		key := strings.ToLower(m.Name)
		failing := d.kit.drinkFailing(m, d.n, d.clk.Now())
		if leg, ok := d.kit.doneLeg[key]; ok && leg == d.curLeg {
			continue
		}
		bl, ok := d.g.(BotLiner)
		if !ok {
			d.kit.off = true
			d.rep.note("refill: skipped, this game cannot send .bot maintenance")
			return done, nil
		}
		why := d.refillWhy(ctx, bl, m, rw.nudged[m.GUID], failing)
		if why == "" {
			continue
		}
		if err := ctx.Err(); err != nil {
			return done, err
		}
		before := -1
		if n, ok := d.kit.drinks[key]; ok {
			before = n
		}
		d.kit.doneLeg[key] = d.curLeg
		verdict := d.maintain(ctx, bl, m.Name)
		after := -1
		if verdict == "maintained" {
			if n, ok := d.readDrinks(ctx, bl, m.Name); ok {
				after = n
			}
		}
		rf := Refill{Bot: m.Name, Leg: max(d.curLeg, 0), Why: why, Before: before, After: after, Maintenance: verdict}
		d.remote(func(r *RemoteReport) { r.Refills = append(r.Refills, rf) })
		rec.g.Refills = append(rec.g.Refills, m.Name)
		d.tr.Event("refill", map[string]any{"leg": rf.Leg, "bot": rf.Bot, "why": why, "before": before, "after": after, "maintenance": verdict})
		d.rep.note(fmt.Sprintf("leg %d: %s refilled (%s): drinks %s -> %s, maintenance %s", rf.Leg, m.Name, why, countText(before), countText(after), verdict))
		if verdict == "disabled" {
			d.kit.off = true
			d.rep.note("refill: maintenance is off on the server, no more refills")
			return done, nil
		}
		if verdict == "maintained" {
			done = append(done, m.Name)
		}
	}
	return done, nil
}

func countText(n int) string {
	if n < 0 {
		return "?"
	}
	return fmt.Sprint(n)
}

// walkAhead is the walking-rest rule for the gate that opens a leg whose first step is next: ok when next is a walk of at least the
// rule's distance from the lead bot. A leg that does not walk first, or a plan or Config that turns it off, holds the gate as before.
func (d *RemoteDriver) walkAhead(next Action) *walkRest {
	if next.Kind != ActWalk {
		return nil
	}
	w, ok := walkRestFor(d.cfg, d.plan, d.curLeg)
	if !ok {
		return nil
	}
	lead, have := d.leadPos()
	if !have || dist3(lead, next.To) < float64(w.yd) {
		return nil
	}
	return &w
}

// pullFloorShort lists what is under the pull floors: the tank's health under Config.RestPullTankPct and the healer's mana under
// Config.RestPullHealerManaPct, each floor held down to the gate's own target (a leg whose rest.* lowers a target lowers the floor with it).
// The tank is the named or learned one, else the lead bot; the healer is the bot RemoteOptions.Healer names (without one only the
// casters' own mana target applies, and no floor is checked). A dead member is the gate's other business.
func (d *RemoteDriver) pullFloorShort(tgt restTarget) []string {
	var out []string
	tank, ok := d.memberByGUID(d.tank)
	if d.tank == 0 || !ok {
		tank, ok = d.fr.LeadRec()
	}
	if ok && !tank.Dead {
		if h := tank.HPPct(); h < min(d.cfg.RestPullTankPct, tgt.health) {
			out = append(out, fmt.Sprintf("%s health %d%%", tank.Name, h))
		}
	}
	if d.opt.Healer != "" {
		if m, ok := d.memberByName(d.opt.Healer); ok && !m.Dead && m.UsesMana() {
			if p := m.PowerPct(); p < min(d.cfg.RestPullHealerManaPct, tgt.healerMana) {
				out = append(out, fmt.Sprintf("%s mana %d%%", m.Name, p))
			}
		}
	}
	return out
}
