package dungeon

import (
	"strings"
	"time"

	"github.com/pjerra/turtle-agent/internal/objects"
)

// Refusal is TortoiseBots saying no to one of the driver's orders: the TBM:ACTION_ERR|<intent>|<code>|<message> reply to
// a "/p .bot action ..." order (internal/tbm), or a whispered "<mob> is evading" / "<mob> is dead" for an attack.
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
