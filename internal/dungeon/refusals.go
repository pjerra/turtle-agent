package dungeon

import (
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

// leaderHasItem says whether the leader carries the item. known is false when that cannot be told: the Game has no
// ItemSource, or the bags hold items whose create block has not arrived yet (entry 0) and the item is not among the rest.
func (d *Driver) leaderHasItem(entry uint32) (has, known bool) {
	src, ok := d.g.(ItemSource)
	if !ok {
		return false, false
	}
	pending := false
	for _, it := range src.Items() {
		if it.Entry == entry && it.Count > 0 {
			return true, true
		}
		pending = pending || it.Entry == 0
	}
	return false, !pending
}

// needsItem fails when the step's required item is certainly not in the leader's bags. A GM passes the object's lock, so a GM
// leader is never failed. where names the place in the message ("leg 'keystone' step 0" for the check before the run).
func (d *Driver) needsItem(it *ItemRef, where string) error {
	if it == nil || d.gm {
		return nil
	}
	if has, known := d.leaderHasItem(it.Entry); known && !has {
		if where != "" {
			where = " " + where
		}
		return fmt.Errorf("the leader does not carry %s, which opening the object%s needs (the core ignores the use of a locked object by a non-GM): put it in the bags first", it, where)
	}
	return nil
}

// checkRequiredItems fails the run before it starts when a use_object step from leg fromLeg on needs an item the leader lacks.
func (d *Driver) checkRequiredItems(fromLeg int) error {
	for li := fromLeg; li < len(d.plan.Legs); li++ {
		l := d.plan.Legs[li]
		for si, st := range l.Steps {
			if st.UseObject == nil {
				continue
			}
			if err := d.needsItem(st.UseObject.RequiresItem, fmt.Sprintf("(leg %d %q step %d)", li, l.Name, si)); err != nil {
				return err
			}
		}
	}
	return nil
}
