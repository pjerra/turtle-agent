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
//   - the feed says its last action was a drink that failed for want of one (action "drink", result F, reason no-item, TB #924),
//
// then reads `.bot inv` again for the count it came to. Without a BotLiner, or with maintenance off on the server, nothing is sent.

// Refill is one `.bot maintenance` sent again during a run.
type Refill struct {
	Bot string `json:"bot"`
	Leg int    `json:"leg"`
	// Why is "low-drinks" (the count was under the limit), "nudge" (the mana did not rise) or "no-item" (the feed said the drink failed
	// for want of one).
	Why string `json:"why"`
	// Before and After are the drinks the bags held before and after (-1: not read). Maintenance is its verdict ("maintained", ...).
	Before      int    `json:"before"`
	After       int    `json:"after"`
	Maintenance string `json:"maintenance"`
}

// restKit is what the rest gates know of the bots' bags.
type restKit struct {
	drinks  map[string]int // the drinks each bot held at its last `.bot inv` read (by lower-case name)
	readLeg map[string]int // the leg of that read (-1: the preflight)
	doneLeg map[string]int // the leg a bot was refilled in
	off     bool           // maintenance is off on the server, or the game cannot send it: no more refills
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
func (d *RemoteDriver) refillWhy(ctx context.Context, bl BotLiner, m tbm.PartyRec, nudged bool) string {
	switch {
	case m.DrinkFailedNoItem():
		return "no-item"
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
		if leg, ok := d.kit.doneLeg[key]; ok && leg == d.curLeg {
			continue
		}
		bl, ok := d.g.(BotLiner)
		if !ok {
			d.kit.off = true
			d.rep.note("refill: skipped, this game cannot send .bot maintenance")
			return done, nil
		}
		why := d.refillWhy(ctx, bl, m, rw.nudged[m.GUID])
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
