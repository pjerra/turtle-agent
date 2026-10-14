package dungeon

import (
	"fmt"
	"slices"
	"time"

	"github.com/pjerra/turtle-agent/internal/packet"
	"github.com/pjerra/turtle-agent/internal/tbm"
)

// The rest gate's shared parts: the targets a leg asks for, the watch on members whose mana is short, and the per-gate record
// of the report. Driver.restGate (group mode) and RemoteDriver.restGate (remote mode) both use them.

// restTarget is what one gate waits for: health and mana percentages and the cap, from the Config and the leg's RestSpec.
type restTarget struct {
	health     int
	healerMana int
	mana       int
	max        time.Duration
}

// restTargetFor resolves the targets of leg (an index into the plan; out of range: the Config's own).
func restTargetFor(cfg Config, plan *Plan, leg int) restTarget {
	t := restTarget{health: cfg.RestHealthPct, healerMana: cfg.RestHealerManaPct, mana: cfg.RestManaPct, max: cfg.RestTimeout}
	if plan == nil || leg < 0 || leg >= len(plan.Legs) || plan.Legs[leg].Rest == nil {
		return t
	}
	s := plan.Legs[leg].Rest
	if s.Health > 0 {
		t.health = s.Health
	}
	if s.HealerMana > 0 {
		t.healerMana = s.HealerMana
	}
	if s.Mana > 0 {
		t.mana = s.Mana
	}
	if s.MaxS > 0 {
		t.max = time.Duration(s.MaxS) * time.Second
	}
	return t
}

// manaNeed is the mana percent a member must reach: the healer's target for a healer, the other for the rest.
func (t restTarget) manaNeed(healer bool) int {
	if healer {
		return t.healerMana
	}
	return t.mana
}

// restWatch is the state a gate keeps between two looks at the party: the windows on members short of mana, and who has had a
// drink order already (one per member per gate).
type restWatch struct {
	mana   map[packet.GUID]manaWatch
	nudged map[packet.GUID]bool
	tgt    restTarget
	gather bool // remote mode: the followers must be near the lead bot too (a gate before a pull), not only rested
}

func newRestWatch(tgt restTarget) *restWatch {
	return &restWatch{mana: map[packet.GUID]manaWatch{}, nudged: map[packet.GUID]bool{}, tgt: tgt}
}

// shortMana is a member below its mana target.
type shortMana struct {
	GUID packet.GUID
	Name string
	Pct  int
}

func (s shortMana) String() string { return fmt.Sprintf("%s mana %d%%", s.Name, s.Pct) }

// look updates the member's window and says whether it is due a drink order: its mana has not risen at all in DrinkNudge since the
// window began (it began when the member was first seen short, and starts again whenever its mana rises), and it has had none yet.
func (rw *restWatch) look(cfg Config, now time.Time, guid packet.GUID, power uint32) (w manaWatch, nudge bool) {
	w, ok := rw.mana[guid]
	if !ok {
		w = manaWatch{power: power, since: now, base: power, baseAt: now}
	}
	if power > w.base {
		w.base, w.baseAt = power, now
	}
	rw.mana[guid] = w
	return w, !rw.nudged[guid] && cfg.DrinkNudge > 0 && now.Sub(w.baseAt) >= cfg.DrinkNudge && power <= w.base
}

// stalled is the ManaStall rule shared by both drivers: after ManaStall the member is "not drinking" unless it gained
// ManaStallPct of its maximum, which starts the window again.
func (rw *restWatch) stalled(cfg Config, now time.Time, guid packet.GUID, w manaWatch, power, maxPower uint32) bool {
	if now.Sub(w.since) < cfg.ManaStall {
		return false
	}
	if int(power)-int(w.power) >= int(maxPower)*cfg.ManaStallPct/100 {
		w.power, w.since = power, now
		rw.mana[guid] = w
		return false
	}
	return true
}

// RestGate is one rest gate in the report: how long it held the run, how it ended and who was short.
type RestGate struct {
	Leg     int     `json:"leg"`
	Seconds float64 `json:"seconds"`
	// Result is "rested" (everyone at the targets), "stalled" (passed with casters whose mana does not rise), "gave_up" (the cap
	// came with someone short; the run went on) or "failed" (the gate ended the run).
	Result string `json:"result"`
	MaxS   int    `json:"max_s"` // the cap this gate had
	// Short is who was short when the gate ended ("Name mana 29%", "Name health 60%"); empty for a rested gate.
	Short []string `json:"short,omitempty"`
	// DrinkOrders names the members a drink order was sent to (one each), DrinkOrderSent says whether there was any, and
	// PartyRestRefused whether TortoiseBots refused the party rest order (so the orders went to the bots one by one).
	DrinkOrders    []string `json:"drink_orders,omitempty"`
	DrinkOrderSent bool     `json:"drink_order_sent"`
	// DrinkOrderResult is "sent (unverified)" when any order went out: the driver does not read the bot's reply and no live run has
	// shown yet that the bot drinks on it. Absent when none was sent.
	DrinkOrderResult string `json:"drink_order_result,omitempty"`
	PartyRestRefused bool   `json:"party_rest_refused,omitempty"`
}

// maxRestGates bounds the report's gate list.
const maxRestGates = 300

// gateRec builds a RestGate while the gate runs.
type gateRec struct {
	g     RestGate
	begun time.Time
}

func newGateRec(leg int, tgt restTarget, now time.Time) *gateRec {
	return &gateRec{begun: now, g: RestGate{Leg: leg, MaxS: int(tgt.max.Seconds())}}
}

func (r *gateRec) nudged(name string) {
	r.g.DrinkOrderSent, r.g.DrinkOrderResult = true, "sent (unverified)"
	if !slices.Contains(r.g.DrinkOrders, name) {
		r.g.DrinkOrders = append(r.g.DrinkOrders, name)
	}
}

// finish puts the gate in the report and counts its seconds into the leg and the run.
func (r *gateRec) finish(rep *reporter, now time.Time, err error) {
	r.g.Seconds = now.Sub(r.begun).Seconds()
	if r.g.Result == "" {
		if err != nil {
			r.g.Result = "failed"
		} else {
			r.g.Result = "rested"
		}
	}
	rep.update(func(rp *Report) {
		rp.RestSeconds += max(r.g.Seconds, 0)
		if n := len(rp.Legs); n > 0 {
			rp.Legs[n-1].RestSeconds += max(r.g.Seconds, 0)
		}
		if len(rp.RestGates) < maxRestGates {
			rp.RestGates = append(rp.RestGates, r.g)
		}
	})
}

// drinkOrder is the per-bot order that makes one bot drink ("drink") or eat ("eat"): `.bot command <Name> u drink` or `u food`.
// The bare words are no chat command of the module (ParseChatCommand has no "drink" or "eat" trigger, so the bot would ignore
// them); `u <item>` is the owner-or-GM use command, and the item text resolves through FindFoodVisitor (PlayerbotAI.cpp, the
// "drink" and "food" item queries). Whether the bot then sits and drinks is unverified until a live check.
func drinkOrder(name, what string) string {
	if what == "eat" {
		what = "food"
	}
	return ".bot command " + name + " u " + what
}

// replyText is a reply in a few words for a note: its kind and the line that settled it.
func replyText(r tbm.Reply) string {
	if r.Line == "" {
		return r.Kind
	}
	return r.Kind + ": " + r.Line
}

// resume skips what the leader has already passed (see Stepper.ResumeAt), unless the run was asked not to.
func (d *Driver) resume() {
	if !d.cfg.Resume {
		return
	}
	if r, ok := d.st.ResumeAt(); ok {
		d.rep.update(func(rp *Report) { rp.Resumed = &r })
		d.rep.note(r.String())
	}
}
