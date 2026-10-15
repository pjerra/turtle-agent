package dungeon

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/pjerra/turtle-agent/internal/objects"
)

// Refusal is TortoiseBots saying no to one of the driver's orders: the TBM:ACTION_ERR|<intent>|<code>|<message> reply to
// a "/p .bot action ..." order (internal/tbm), or a whispered "<mob> is evading" / "<mob> is dead". A whisper names no
// intent (Intent empty): the driver takes it for an answer to the last order it gave.
type Refusal struct {
	Intent  string `json:"intent"`
	Code    string `json:"code"`
	Message string `json:"message"`
}

// RefusalSource is the part of a Game that can tell what the bots refused. It is optional: a Game without it (the
// tests that do not care) simply never sees a refusal, and the driver behaves as before. Refusals returns the
// refusals that arrived since the last call.
type RefusalSource interface {
	Refusals() []Refusal
}

// SelfAuraSource is the optional part of a Game that tells the leader's own auras (the unit list leaves the leader
// out). ok is false when they cannot be told. The buff batch uses it to see a ".aura" that landed on the leader.
type SelfAuraSource interface {
	SelfAuras() (auras []objects.Aura, ok bool)
}

// ItemSource is the optional part of a Game that tells what the leader carries (the backpack and the bags). A Game without
// it never fails a required_item check.
type ItemSource interface {
	Items() []objects.Item
}

// RefusalReport is one kind of refusal in the report, with how often it came in the leg.
type RefusalReport struct {
	Intent  string    `json:"intent"`
	Code    string    `json:"code"`
	Message string    `json:"message"`
	Leg     int       `json:"leg"`
	Count   int       `json:"count"`
	First   time.Time `json:"first"`
}

// maxRefusalKinds bounds the report's refusal list.
const maxRefusalKinds = 50

type refusalClass int

const (
	refOther       refusalClass = iota
	refUnreachable              // the tank cannot get to the mob, or the mob is evading: no point in pulling it again
	refDead                     // the target died before the order arrived
	refNoTarget                 // the order arrived with no (live, hostile) selection
)

// classify sorts a refusal by what the driver does about it.
func classify(r Refusal) refusalClass {
	code, msg := strings.ToLower(r.Code), strings.ToLower(r.Message)
	switch {
	case code == "unreachable" || code == "evading" || code == "evade" || strings.Contains(msg, "evading") || strings.Contains(msg, "evade mode"):
		return refUnreachable
	case code == "dead-target" || code == "dead" || strings.HasSuffix(msg, " is dead"):
		return refDead
	case code == "no-target":
		return refNoTarget
	}
	return refOther
}

// pullIntent says whether the refusal answers a pull order (the pull itself) and not, say, an assist "attack".
func pullIntent(r Refusal) bool { return r.Intent == "pullback" || r.Intent == "pull" }

// drainRefusals reads the new refusals, records them in the report and keeps them for the fight that looks for them.
func (d *Driver) drainRefusals() {
	src, ok := d.g.(RefusalSource)
	if !ok {
		return
	}
	for _, r := range src.Refusals() {
		if r.Intent == "" { // a whisper: it answers the last order given
			r.Intent = d.lastOrder
			if r.Intent == "" {
				r.Intent = "attack"
			}
		}
		d.recordRefusal(r)
		if len(d.pendingRef) < 32 {
			d.pendingRef = append(d.pendingRef, r)
		}
	}
}

// takeRefusals returns (and forgets) the refusals the last sleeps saw.
func (d *Driver) takeRefusals() []Refusal {
	out := d.pendingRef
	d.pendingRef = nil
	return out
}

func (d *Driver) recordRefusal(r Refusal) {
	d.tr.Event("refusal", map[string]any{"intent": r.Intent, "code": r.Code, "message": r.Message, "leg": d.curLeg})
	d.rep.update(func(rep *Report) {
		for i := range rep.Refusals {
			x := &rep.Refusals[i]
			if x.Intent == r.Intent && x.Code == r.Code && x.Message == r.Message && x.Leg == d.curLeg {
				x.Count++
				return
			}
		}
		if len(rep.Refusals) < maxRefusalKinds {
			rep.Refusals = append(rep.Refusals, RefusalReport{Intent: r.Intent, Code: r.Code, Message: r.Message, Leg: d.curLeg, Count: 1, First: d.clk.Now()})
		}
	})
}

// itemState says what the leader's bags tell of an item: has it, known (the list can be trusted: the Game has an ItemSource and
// no item in it waits for its create block), and empty (the list has no item at all: the inventory fields have not arrived, or the
// bags are truly bare). An item that is missing from a known list is missing; an unknown list proves nothing.
func (d *Driver) itemState(entry uint32) (has, known, empty bool) {
	src, ok := d.g.(ItemSource)
	if !ok {
		return false, false, false
	}
	items := src.Items()
	pending := false
	for _, it := range items {
		if it.Entry == entry && it.Count > 0 {
			return true, true, false
		}
		pending = pending || it.Entry == 0
	}
	return false, len(items) > 0 && !pending, len(items) == 0
}

// itemsExempt says whether the run takes the leader past a lock without the key: only when the caller said it is a GM (gm: true,
// GMAssumed), because the core lets a GM through a lock in GM mode (".gm on", GameObject::PlayerCanUse -> IsGameMaster()), which the
// driver cannot see: a GM account that probed as a GM but plays with ".gm off" gets the silent failure like a player.
func (d *Driver) itemsExempt() bool { return d.cfg.GM == GMAssumed }

// missingForUse is the error for an item a use_item step lacks: the item it uses, or the one its use consumes.
func missingForUse(it *ItemRef, where string, use ItemRef) error {
	if it.Entry == use.Entry {
		return fmt.Errorf("the leader does not carry %s%s, the item this step uses", it, where)
	}
	return fmt.Errorf("the leader does not carry %s%s, which using %s needs", it, where, use)
}

func (d *Driver) missingItem(it *ItemRef, where string) error {
	return fmt.Errorf("the leader does not carry %s%s, which opening the object needs (the core ignores the use of a locked object by anyone but a GM in GM mode): put it in the bags first", it, where)
}

// needsItem fails when the item is certainly not in the leader's bags: a known list without it. An unknown list (no ItemSource,
// the inventory not arrived, items still loading) passes; the check before the use looks again.
func (d *Driver) needsItem(it *ItemRef, where string) error {
	return d.needsItemMsg(it, where, d.itemsExempt(), d.missingItem)
}

// needsItemMsg is needsItem with the error and the exemption of the caller.
func (d *Driver) needsItemMsg(it *ItemRef, where string, exempt bool, missing func(it *ItemRef, where string) error) error {
	if it == nil || exempt {
		return nil
	}
	if has, known, _ := d.itemState(it.Entry); known && !has {
		return missing(it, where)
	}
	return nil
}

// checkUseItem is the check right before a use. An empty list is the inventory not having arrived (just after a login or a
// teleport), not proof of a bare bag: it waits up to ItemsWait for the list to fill, and only if it stays empty the item is
// missing. Items that are still loading are not guessed at.
func (d *Driver) checkUseItem(ctx context.Context, it *ItemRef) error {
	return d.checkItem(ctx, it, d.itemsExempt(), d.missingItem)
}

// checkItem is checkUseItem for any item and message: missing builds the error for an item the bags lack (where says how the bags
// looked), exempt skips the check.
func (d *Driver) checkItem(ctx context.Context, it *ItemRef, exempt bool, missing func(it *ItemRef, where string) error) error {
	if it == nil || exempt {
		return nil
	}
	for waited := time.Duration(0); ; waited += d.cfg.Poll {
		has, known, empty := d.itemState(it.Entry)
		switch {
		case has:
			return nil
		case known:
			return missing(it, "")
		case !empty:
			return nil
		case waited >= d.cfg.ItemsWait:
			return missing(it, " (the bags are empty)")
		}
		if err := d.sleep(ctx, d.cfg.Poll); err != nil {
			return err
		}
	}
}

// checkRequiredItems fails the run before it starts when a use_object step from leg fromLeg on needs an item the leader lacks and
// no earlier step of the run brings it: a loot_object or use_item with that expect_item, or a boss whose loot_item it is, is
// where the Staff of Prehistoria comes from.
func (d *Driver) checkRequiredItems(fromLeg int) error {
	made := map[uint32]bool{}
	scan := func(steps []Step, li int, name, list string) error {
		for si, st := range steps {
			where := fmt.Sprintf(" at leg %d %q %s %d", li, name, list, si)
			switch {
			case st.UseObject != nil:
				if it := st.UseObject.RequiresItem; it != nil && !made[it.Entry] {
					if err := d.needsItem(it, where); err != nil {
						return err
					}
				}
			case st.LootObject != nil && st.LootObject.ExpectItem != nil:
				made[st.LootObject.ExpectItem.Entry] = true
			case st.UseItem != nil:
				// A leader that carries the result already (a GM set it up) has no use for the reagent.
				if e := st.UseItem.ExpectItem; e != nil {
					if has, _, _ := d.itemState(e.Entry); has {
						made[e.Entry] = true
						continue
					}
				}
				use := ItemRef{Entry: st.UseItem.Entry, Name: st.UseItem.Name}
				need := func(it *ItemRef, where string) error { return missingForUse(it, where, use) }
				// The item the step uses must be in the bags or made by a step before it (a run from the revelosh leg needs the Medallion).
				if !made[use.Entry] {
					if err := d.needsItemMsg(&use, where, false, need); err != nil {
						return err
					}
				}
				if it := st.UseItem.RequiresItem; it != nil && !made[it.Entry] {
					if err := d.needsItemMsg(it, where, false, need); err != nil {
						return err
					}
				}
				if st.UseItem.ExpectItem != nil {
					made[st.UseItem.ExpectItem.Entry] = true
				}
			}
		}
		return nil
	}
	for li := fromLeg; li < len(d.plan.Legs); li++ {
		l := d.plan.Legs[li]
		if l.Optional != nil {
			continue // Leg.Optional: the leg is skipped, never a failure, when its item is missing
		}
		if err := scan(l.Steps, li, l.Name, "step"); err != nil {
			return err
		}
		if b := l.Boss; b != nil && b.LootItem != nil {
			made[b.LootItem.Entry] = true
		}
		if err := scan(l.AfterBoss, li, l.Name, "after_boss step"); err != nil {
			return err
		}
	}
	return nil
}
