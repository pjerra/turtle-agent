package dungeon

import (
	"context"
	"errors"
	"fmt"
	"math"
	"slices"
	"strings"
	"time"

	"github.com/pjerra/turtle-agent/internal/packet"
	"github.com/pjerra/turtle-agent/internal/tbm"
)

// The agent as the fallback of an object step (remote mode, RemoteOptions.ObjectFallback "agent", the default). When the carrier's
// order fails twice (any ERR, or "failed (spell check ...)"), the agent's own character does what a player at a chest does, with the
// group-mode code (ObjectActor: its own Opening cast, then the loot into its own bags), and hands the item to the carrier with a trade
// (#112). Trade needs both sides visible and within 10 yd: a GM that the setup made invisible (`gm visible off`) is made visible for the
// trade only and invisible again right after, both lines GM assists during the run (run_gm_assists), never in a fight. The item is
// checked in the carrier's bags with `.bot inv` before the step counts as done.
//
// The same fallback serves the combine (the agent uses the Medallion if it holds both parts, then trades the Staff) and the Keystone
// (the agent opens it if it holds the Staff and no later step needs a bot to hold it). A boss's corpse is not covered: the agent is not
// in the party, so the corpse's loot is not its to take.

// AgentHands is the optional part of a RemoteGame that lets the agent's own character act: ObjectActor (the Opening cast, corpse and item
// use), UseObject (right-click), ItemSource (what its bags hold), GM (the visibility lines) and Trade. A game without it has no fallback.
type AgentHands interface {
	ObjectActor
	ItemSource
	UseObject(ctx context.Context, guid packet.GUID) error
	// GM sends a GM line (".gm visible on|off").
	GM(ctx context.Context, text string) error
	// Trade hands items to the player `to` (a character name) in trade range and waits for the trade to end.
	Trade(ctx context.Context, to string, items []ItemCount) (TradeOutcome, error)
}

// TradeOutcome is how a trade ended: Result is "completed" or why not (cancelled, timeout, too_far, busy, rejected, ...).
type TradeOutcome struct {
	Result   string
	Verified bool // completed and the agent's own bags show the items gone
	Detail   string
}

// FallbackReport is what the agent's own character did for an object step.
type FallbackReport struct {
	Used      bool         `json:"used"`
	Reason    string       `json:"reason"` // what the carrier's orders ended in
	Did       string       `json:"did,omitempty"`
	Items     []string     `json:"items"` // "7740 x1" taken or made by the agent
	Trade     *TradeReport `json:"trade,omitempty"`
	GMAssists int          `json:"gm_assists"` // `.gm visible on|off` and `.appear` lines the fallback sent
	Seconds   float64      `json:"seconds"`
	Result    string       `json:"result"` // running, done or failed
	Error     string       `json:"error,omitempty"`
}

// TradeReport is the hand-over to the carrier.
type TradeReport struct {
	Result   string   `json:"result"` // completed, or why not
	Verified bool     `json:"verified"`
	Detail   string   `json:"detail,omitempty"`
	Items    []string `json:"items"`
	Visible  string   `json:"visible,omitempty"` // "on/off around the trade" for a GM that was invisible, else empty
	InBags   bool     `json:"in_bags"`           // `.bot inv <carrier>` showed the item
	Seconds  float64  `json:"seconds"`
}

func (f *FallbackReport) clone() *FallbackReport {
	if f == nil {
		return nil
	}
	c := *f
	c.Items = slices.Clone(f.Items)
	if f.Trade != nil {
		t := *f.Trade
		t.Items = slices.Clone(f.Trade.Items)
		c.Trade = &t
	}
	return &c
}

// fb changes the step's fallback report (made on first use) and marks the fallback as used.
func (o *objRec) fb(f func(*FallbackReport)) {
	o.update(func(s *ObjectStepReport) {
		if s.Fallback == nil {
			s.Fallback = &FallbackReport{Items: []string{}, Result: "running"}
		}
		s.Fallback.Used, s.FallbackUsed = true, true
		f(s.Fallback)
	})
}

// objectFallbackTries is how often the carrier's order is tried before the agent takes the step over.
const objectFallbackTries = 2

// Waits of the hand-over.
const (
	tradeReach      = 8.0             // yards from the carrier the agent stands for a trade (the session allows 10)
	tradeTries      = 2               // trades attempted before the hand-over fails
	visibleSettle   = 2 * time.Second // after `.gm visible on`, for the server to show the agent to the bot
	agentCombatWait = 60 * time.Second
	gmVisibleOn     = ".gm visible on"
	gmVisibleOff    = ".gm visible off"
	setupVisibleOff = "gm visible off"
)

// agentHands is the agent's hands when the fallback is on and the game has them.
func (d *RemoteDriver) agentHands() (AgentHands, bool) {
	if d.opt.ObjectFallback == ObjectFallbackOff {
		return nil, false
	}
	h, ok := d.g.(AgentHands)
	return h, ok
}

// carryOrFallback is carry with a second try: with the fallback available, a refusal of the order is sent once more before the step is
// given up to the agent. failed says that the order was refused twice (or once with no second try wanted) and the agent may take over; an
// error with failed false (a lost binding, a wipe, no answer, a busy carrier that stayed busy) is the step's as it was.
func (d *RemoteDriver) carryOrFallback(ctx context.Context, rec *objRec, command string, near *Point) (res tbm.OrderResult, failed bool, err error) {
	_, canFall := d.agentHands()
	tries := 1
	if canFall {
		tries = objectFallbackTries
	}
	for try := 1; try <= tries; try++ {
		res, err = d.carry(ctx, rec, command, near)
		code := res.Code()
		rec.update(func(s *ObjectStepReport) { s.CarrierResult = codeOrAck(res) })
		if err == nil {
			return res, false, nil
		}
		if runEnded(ctx, err) || res.Err == nil {
			return res, false, err
		}
		if try < tries {
			d.rep.note(fmt.Sprintf("%s: the carrier's order failed (%s): sending it once more", rec.label(), code))
			if terr := d.tick(ctx, d.cfg.Poll*2); terr != nil {
				return res, false, terr
			}
		}
	}
	return res, canFall, err
}

func codeOrAck(res tbm.OrderResult) string {
	if c := res.Code(); c != "" {
		return c
	}
	return "ack"
}

// fallbackGate waits for the party to be out of combat (the agent opens and trades only then) up to agentCombatWait.
func (d *RemoteDriver) fallbackGate(ctx context.Context, what string) error {
	until := d.clk.Now().Add(agentCombatWait)
	d.look()
	for d.inCombat() {
		if !d.clk.Now().Before(until) {
			return fmt.Errorf("the party was still in combat after %.0f s: the agent does not %s in a fight", agentCombatWait.Seconds(), what)
		}
		if err := d.tick(ctx, d.cfg.Poll*2); err != nil {
			return err
		}
	}
	return nil
}

// runFallback runs body as the agent's fallback of the step and records it. The returned error joins the carrier's (cause may be nil when
// the agent took the step without trying the carrier).
func (d *RemoteDriver) runFallback(ctx context.Context, rec *objRec, what string, cause error, body func(h AgentHands) error) error {
	h, ok := d.agentHands()
	if !ok {
		return errors.Join(cause, errors.New("the agent has no hands for the fallback"))
	}
	began := d.clk.Now()
	reason := "the carrier does not have what the step needs"
	if cause != nil {
		reason = cause.Error()
	}
	rec.fb(func(f *FallbackReport) { f.Reason, f.Did = reason, what })
	d.rep.note(fmt.Sprintf("%s: the agent takes over (%s): %s", rec.label(), what, reason))
	d.tr.Event("object_fallback", map[string]any{"leg": d.curLeg, "step": rec.label(), "what": what, "reason": reason})
	err := body(h)
	secs := d.clk.Now().Sub(began).Seconds()
	rec.fb(func(f *FallbackReport) {
		f.Seconds, f.Result = secs, "done"
		if err != nil {
			f.Result, f.Error = "failed", err.Error()
		}
	})
	d.tr.Event("object_fallback_end", map[string]any{"leg": d.curLeg, "step": rec.label(), "ok": err == nil, "seconds": secs})
	if err != nil {
		if cause != nil && !runEnded(ctx, err) {
			return fmt.Errorf("%w; the agent's fallback failed too: %v", cause, err)
		}
		return err
	}
	return nil
}

// agentReach brings the agent's character within `within` yards of target: a GM that the setup made invisible is `.appear`ed to appearTo
// (a GM assist), then the agent walks to stand. Out of a fight only. A game that cannot move the agent is taken to stand near already.
func (d *RemoteDriver) agentReach(ctx context.Context, what string, target Point, stand *Point, within float64, appearTo string) error {
	f, ok := d.g.(Follower)
	if !ok {
		return nil
	}
	appeared, walked := false, false
	for {
		d.look()
		at, m, here := f.AgentPlace()
		if !here {
			return errors.New("the agent's character is not in the world")
		}
		dist := math.Inf(1)
		if l, ok := d.fr.LeadRec(); ok && l.Map == m {
			dist = dist3(at, target)
		}
		if dist <= within {
			return nil
		}
		switch {
		case !appeared && d.agentGM(ctx, f) && d.setupHas(setupVisibleOff) && !d.inCombat():
			appeared = true
			if _, err := d.appear(ctx, f, appearTo, dist); err != nil {
				return err
			}
		case !walked && !d.inCombat():
			walked = true
			d.remote(func(r *RemoteReport) { r.AgentWalks++ })
			to := d.standoffFrom(ctx, at, target, 2)
			if stand != nil {
				to = *stand
			}
			d.rep.note(fmt.Sprintf("agent fallback: the agent is %.1f yd from %s; walking to %.1f %.1f %.1f", dist, what, to.X, to.Y, to.Z))
			if _, err := d.agentWalk(ctx, f, to, 0); err != nil {
				return err
			}
		default:
			return fmt.Errorf("the agent stands %.1f yd from %s (it needs %.1f)", dist, what, within)
		}
	}
}

// gmAssist counts a GM line the fallback sent during the run, like keep-near's `.appear`.
func (d *RemoteDriver) gmAssist(rec *objRec, cmd, why string) {
	d.rep.update(func(r *Report) { r.SetupShortcuts = append(r.SetupShortcuts, cmd+" (GM assist during the run)") })
	d.remote(func(r *RemoteReport) { r.RunGMAssists++ })
	rec.fb(func(f *FallbackReport) { f.GMAssists++ })
	d.rep.note(fmt.Sprintf("%s: %s %s: a GM assist during the run (run_gm_assists; not counted in gm_commands)", rec.label(), cmd, why))
}

// invisibleGM says whether the agent is a GM that the setup made invisible: it needs `.gm visible on` for a trade.
func (d *RemoteDriver) invisibleGM(ctx context.Context) bool {
	f, ok := d.g.(Follower)
	return ok && d.agentGM(ctx, f) && d.setupHas(setupVisibleOff)
}

// handOver trades the items to the carrier and checks `.bot inv` for the first of them (check, the item the step expects).
func (d *RemoteDriver) handOver(ctx context.Context, h AgentHands, rec *objRec, carrier string, items []ItemCount, check *ItemRef) (err error) {
	began := d.clk.Now()
	tr := &TradeReport{Items: []string{}, Result: "not started"}
	for _, it := range items {
		tr.Items = append(tr.Items, fmt.Sprintf("%d x%d", it.Entry, it.Count))
	}
	save := func() {
		tr.Seconds = d.clk.Now().Sub(began).Seconds()
		c := *tr
		c.Items = slices.Clone(tr.Items)
		rec.fb(func(f *FallbackReport) { f.Trade = &c })
		d.tr.Event("object_trade", map[string]any{"leg": d.curLeg, "carrier": carrier, "result": c.Result, "verified": c.Verified, "in_bags": c.InBags, "visible": c.Visible, "items": c.Items})
	}
	defer save()
	if err := d.fallbackGate(ctx, "trade"); err != nil {
		return err
	}
	if m, ok := d.memberByName(carrier); ok {
		if err := d.agentReach(ctx, carrier, partyPoint(m), nil, tradeReach, carrier); err != nil {
			return fmt.Errorf("trade with %s: %w", carrier, err)
		}
	}
	if d.invisibleGM(ctx) {
		if err := d.fallbackGate(ctx, "go visible"); err != nil { // the walk to the carrier took time: look at the fight again
			return err
		}
		d.gmAssist(rec, gmVisibleOn, "(for the trade)")
		tr.Visible = "on before the trade, off after"
		if gerr := h.GM(ctx, gmVisibleOn); gerr != nil {
			tr.Result = "visible on failed"
			return fmt.Errorf("%s: %w", gmVisibleOn, gerr)
		}
		defer func() {
			// Whatever the trade did, the GM goes invisible again; the line is sent with a context the run's end does not cut.
			octx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 15*time.Second)
			defer cancel()
			d.gmAssist(rec, gmVisibleOff, "(after the trade)")
			if gerr := h.GM(octx, gmVisibleOff); gerr != nil {
				tr.Visible += "; " + gmVisibleOff + " failed: " + gerr.Error()
				d.rep.note(fmt.Sprintf("%s: %s failed: %v; the agent may still be visible", rec.label(), gmVisibleOff, gerr))
			}
		}()
		if err := d.tick(ctx, visibleSettle); err != nil {
			return err
		}
	}
	for try := 1; try <= tradeTries; try++ {
		out, terr := h.Trade(ctx, carrier, items)
		tr.Result, tr.Verified, tr.Detail = out.Result, out.Verified, out.Detail
		if terr != nil {
			tr.Result = "error"
			tr.Detail = terr.Error()
			if runEnded(ctx, terr) || try == tradeTries {
				return fmt.Errorf("trade with %s: %w", carrier, terr)
			}
		} else if out.Result == "completed" {
			break
		} else if try == tradeTries {
			return fmt.Errorf("trade with %s ended %s (%s)", carrier, out.Result, out.Detail)
		}
		d.rep.note(fmt.Sprintf("%s: trade with %s ended %s %s: trying again", rec.label(), carrier, tr.Result, tr.Detail))
		if err := d.tick(ctx, d.cfg.Poll*2); err != nil {
			return err
		}
	}
	if check == nil {
		return nil
	}
	ok, err := d.waitForItem(ctx, carrier, check)
	if err != nil {
		return err
	}
	tr.InBags = ok
	if !ok {
		return fmt.Errorf("the trade with %s completed but %s is not in its bags (.bot inv %s)", carrier, check, carrier)
	}
	return nil
}

// agentCount is how many of the item the agent's own bags hold.
func agentCount(h AgentHands, entry uint32) uint32 {
	var n uint32
	for _, it := range h.Items() {
		if it.Entry == entry {
			n += it.Count
		}
	}
	return n
}

// fallbackLoot is a loot_object step done by the agent: it opens the chest (twice at most, like the group driver) and hands the
// expected item to the carrier.
func (d *RemoteDriver) fallbackLoot(ctx context.Context, a Action, carrier string, rec *objRec, cause error) error {
	return d.runFallback(ctx, rec, "loot_object", cause, func(h AgentHands) error {
		if err := d.fallbackGate(ctx, "open the object"); err != nil {
			return err
		}
		if at := objectPoint(a); at != nil {
			stand := d.standOf(a)
			if stand == nil {
				s := d.standFor(ctx, *at)
				stand = &s
			}
			if err := d.agentReach(ctx, "the object", *at, stand, UseRange-0.5, carrier); err != nil {
				return err
			}
		}
		guid, err := d.agentGUID(ctx, a, rec)
		if err != nil {
			return err
		}
		var got Looted
		for try := 0; try < 2; try++ {
			if got, err = h.LootObject(ctx, guid); err == nil {
				break
			}
			if ctx.Err() != nil {
				return ctx.Err()
			}
			if terr := d.tick(ctx, time.Second); terr != nil {
				return terr
			}
		}
		if err != nil {
			return fmt.Errorf("the agent's own Opening cast at game object %s: %w", guid, err)
		}
		d.rep.note(fmt.Sprintf("%s: the agent took %s", rec.label(), describeLooted(got)))
		var hand []ItemCount
		for _, it := range got.Items {
			label := fmt.Sprintf("%d x%d", it.Entry, it.Count)
			rec.fb(func(f *FallbackReport) { f.Items = append(f.Items, label) })
			rec.gained(label)
			if a.Expect != nil && it.Entry == a.Expect.Entry {
				hand = append(hand, it)
			}
		}
		if got.Money > 0 {
			rec.gained(fmt.Sprintf("copper %d", got.Money))
		}
		if a.Expect == nil {
			return nil // the step wants no item: nothing to hand over
		}
		if len(hand) == 0 {
			if n := agentCount(h, a.Expect.Entry); n > 0 {
				hand = []ItemCount{{Entry: a.Expect.Entry, Count: n}} // taken earlier and still in the agent's bags
			} else {
				return fmt.Errorf("the agent opened the object (%s) but %s is not in its bags", describeLooted(got), a.Expect)
			}
		}
		return d.handOver(ctx, h, rec, carrier, hand, a.Expect)
	})
}

// fallbackOpen is a use_object step done by the agent (the Keystone): it right-clicks the object with the key item in its bags.
func (d *RemoteDriver) fallbackOpen(ctx context.Context, a Action, carrier string, rec *objRec, cause error) error {
	return d.runFallback(ctx, rec, "use_object", cause, func(h AgentHands) error {
		if err := d.fallbackGate(ctx, "open the object"); err != nil {
			return err
		}
		if at := objectPoint(a); at != nil {
			stand := d.standOf(a)
			if stand == nil {
				s := d.standFor(ctx, *at)
				stand = &s
			}
			if err := d.agentReach(ctx, "the object", *at, stand, UseRange-0.5, carrier); err != nil {
				return err
			}
		}
		guid, err := d.agentGUID(ctx, a, rec)
		if err != nil {
			return err
		}
		for try := 0; try < 2; try++ {
			if err = h.UseObject(ctx, guid); err == nil {
				break
			}
			if ctx.Err() != nil {
				return ctx.Err()
			}
			if terr := d.tick(ctx, time.Second); terr != nil {
				return terr
			}
		}
		if err != nil {
			return fmt.Errorf("the agent's own use of game object %s: %w", guid, err)
		}
		return d.tick(ctx, d.cfg.UseSettle)
	})
}

// fallbackCombine is a use_item step done by the agent: it uses the item (it holds it and the item the use consumes) and hands the
// product to the carrier.
func (d *RemoteDriver) fallbackCombine(ctx context.Context, a Action, carrier string, rec *objRec, cause error) error {
	return d.runFallback(ctx, rec, "use_item", cause, func(h AgentHands) error {
		if err := d.fallbackGate(ctx, "use the item"); err != nil {
			return err
		}
		if err := h.UseItem(ctx, a.Item.Entry); err != nil {
			return fmt.Errorf("the agent's own use of %s: %w", a.Item, err)
		}
		if err := d.tick(ctx, d.cfg.UseSettle); err != nil {
			return err
		}
		if a.Expect == nil {
			return nil
		}
		for waited := time.Duration(0); agentCount(h, a.Expect.Entry) == 0; waited += d.cfg.Poll {
			if waited >= itemArriveMax {
				return fmt.Errorf("the agent used %s but %s is not in its bags after %.0f s", a.Item, a.Expect, itemArriveMax.Seconds())
			}
			if err := d.tick(ctx, d.cfg.Poll); err != nil {
				return err
			}
		}
		rec.fb(func(f *FallbackReport) { f.Items = append(f.Items, fmt.Sprintf("%d x1", a.Expect.Entry)) })
		return d.handOver(ctx, h, rec, carrier, []ItemCount{{Entry: a.Expect.Entry, Count: 1}}, a.Expect)
	})
}

// agentHolds says whether the fallback is on and the agent's bags hold every one of the items (nil entries are ignored).
func (d *RemoteDriver) agentHolds(items ...*ItemRef) bool {
	h, ok := d.agentHands()
	if !ok {
		return false
	}
	have := false
	for _, it := range items {
		if it == nil {
			continue
		}
		have = true
		if agentCount(h, it.Entry) == 0 {
			return false
		}
	}
	return have
}

// laterNeeds says whether a step after the current one uses the item or needs it in a bot's bags (a use_item with it, or a use_object
// that requires it): the agent keeps an item only when no bot has to hold it afterwards.
func (d *RemoteDriver) laterNeeds(a Action, entry uint32) bool {
	needs := func(st Step) bool {
		switch {
		case st.UseItem != nil:
			return st.UseItem.Entry == entry || (st.UseItem.RequiresItem != nil && st.UseItem.RequiresItem.Entry == entry)
		case st.UseObject != nil:
			return st.UseObject.RequiresItem != nil && st.UseObject.RequiresItem.Entry == entry
		}
		return false
	}
	afterBoss := strings.Contains(a.Label, "after_boss step")
	for li := max(a.Leg, 0); li < len(d.plan.Legs); li++ {
		l := d.plan.Legs[li]
		steps := slices.Clone(l.Steps)
		steps = append(steps, l.AfterBoss...)
		from := 0
		if li == a.Leg {
			from = a.Step + 1
			if afterBoss {
				from += len(l.Steps)
			}
		}
		for i := from; i < len(steps); i++ {
			if needs(steps[i]) {
				return true
			}
		}
	}
	return false
}
