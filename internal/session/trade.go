package session

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/pjerra/turtle-agent/internal/events"
	"github.com/pjerra/turtle-agent/internal/game"
	"github.com/pjerra/turtle-agent/internal/objects"
	"github.com/pjerra/turtle-agent/internal/opcode"
	"github.com/pjerra/turtle-agent/internal/packet"
)

// Core cites in this file are tortoise-wow upstream/1181dev 5784fb0 (TradeHandler.cpp).
//
// A trade is a short conversation the core drives with SMSG_TRADE_STATUS. Trade follows it from
// the initiating side: CMSG_INITIATE_TRADE, wait for the other player's CMSG_BEGIN_TRADE to open the
// window (status open), put items and gold in, wait out the core's scam-prevention delay, CMSG_ACCEPT_TRADE,
// and wait for the other side's accept to complete it. What the core tells us:
//   - Only the other player is told of a new trade (status begin); we hear nothing until the window opens.
//   - Our own changes are not echoed as SMSG_TRADE_STATUS_EXTENDED (the core sends that to the other player,
//     TradeData::Update). Each effective change comes back as one status back_to_trade, and a refused item as cancelled.
//   - An accept less than a second after the last change is refused with back_to_trade (the delay is counted in
//     whole seconds, TradeHandler.cpp:297-303), so Trade waits Options.TradeSettle before it accepts.
//   - The whole stack goes in: CMSG_SET_TRADE_ITEM has no count. Trade splits a stack first (CMSG_SPLIT_ITEM) when
//     a part is wanted.

// tradeRange is the farthest the other player may be for Trade to start. The core allows TRADE_DISTANCE (11.11 yd,
// game.TradeDistance) between the two centres, both when it opens the trade and when it completes it; Trade keeps a
// yard in hand because a position in the store is the last one the server sent.
const tradeRange float32 = 10

// tradeSettle is how long Trade waits after its last change before it accepts (Options.TradeSettle, 0: this). The core
// counts the scam-prevention delay in whole seconds from time(), so a second and a margin always clears it.
const tradeSettle = 1100 * time.Millisecond

// Defaults for the waits of one trade (TradeRequest.OpenWait, Wait).
const (
	tradeOpenWait   = 8 * time.Second
	tradeDoneWait   = 12 * time.Second
	tradeSplitWait  = 3 * time.Second
	tradeCancelWait = time.Second
	// tradeMaxAccepts bounds how often Trade accepts again after the core sent back_to_trade, which the other
	// side's changes cause too (a mage bot that adds conjured food to the window, for one).
	tradeMaxAccepts = 4
)

// tradeState follows the player's trade window. Owner loop only.
type tradeState struct {
	active      bool        // Trade is running
	partner     packet.GUID // who it trades with
	open        bool        // status open came
	ended       bool        // a status that closes the window came
	status      game.TradeStatus
	theirAccept bool   // status accepted came since the last back_to_trade
	backs       uint64 // counts back_to_trade
	theirs      *game.TradeView
	incoming    packet.GUID // who last started a trade with us (status begin); the agent does not answer it
	wakes       notifier
}

func (s *Session) registerTrade() {
	s.handlers[opcode.SMSGTradeStatus] = s.onTradeStatus
	s.handlers[opcode.SMSGTradeStatusExtended] = s.onTradeStatusExtended
}

func (s *Session) onTradeStatus(body []byte) error {
	m, err := game.ParseTradeStatus(body)
	if err != nil {
		return err
	}
	t := &s.trade
	src := t.partner
	switch m.Status {
	case game.TradeBegin:
		t.incoming, src = m.Other, m.Other
	case game.TradeOpenWindow:
		t.open = true
	case game.TradeAccept:
		t.theirAccept = true
	case game.TradeBackToTrade:
		t.backs++
		t.theirAccept = false
	default:
		t.ended, t.status, t.open = true, m.Status, false
	}
	d := map[string]any{"status": m.Status.Name(), "code": uint32(m.Status)}
	if t.theirs != nil && (m.Status == game.TradeAccept || m.Status == game.TradeComplete) {
		d["their_gold"], d["their_items"] = t.theirs.Gold, t.theirs.Items
	}
	text := "trade " + m.Status.Name()
	if n, ok := s.cachedName(src); ok && src != 0 {
		text += " with " + n
	}
	s.emit(events.Event{Kind: events.KindTradeStatus, SourceGUID: src, Text: text, Data: d})
	t.wakes.wake()
	return nil
}

func (s *Session) onTradeStatusExtended(body []byte) error {
	v, err := game.ParseTradeStatusExtended(body)
	if err != nil {
		return err
	}
	if v.Theirs {
		s.trade.theirs = &v
		s.trade.wakes.wake()
	}
	return nil
}

// TradeItemReq asks for Count of the item Entry from the bags (0: one).
type TradeItemReq struct {
	Entry uint32 `json:"item"`
	Count uint32 `json:"count,omitempty"`
}

// TradeRequest says what Trade hands over and to whom: To (a guid) or Name (a player in view),
// the items and the gold in copper. OpenWait bounds the wait for the window to open and Wait the wait for
// the other side to accept; 0: 8 s and 12 s.
type TradeRequest struct {
	To       packet.GUID
	Name     string
	Items    []TradeItemReq
	Gold     uint32
	OpenWait time.Duration
	Wait     time.Duration
}

// TradeGave is what went into the window: an item entry and the count.
type TradeGave struct {
	Entry uint32 `json:"item"`
	Count uint32 `json:"count"`
	Split bool   `json:"split,omitempty"` // the count was split off a larger stack first
}

// TradeResult is how a trade ended. Result is completed, or why not: cancelled, timeout, too_far, busy, rejected,
// no_target, wrong_faction, ignored, and the player's own and the target's states (you_dead, target_dead, ...).
type TradeResult struct {
	Result          string           `json:"result"`
	Status          string           `json:"status,omitempty"` // the last SMSG_TRADE_STATUS, by name
	Partner         packet.GUID      `json:"partner,omitempty"`
	PartnerName     string           `json:"partner_name,omitempty"`
	Gave            []TradeGave      `json:"gave"`
	Gold            uint32           `json:"gold,omitempty"` // copper put in the window
	Received        []game.TradeItem `json:"received"`       // what the other side had in the window
	ReceivedGold    uint32           `json:"received_gold,omitempty"`
	PartnerAccepted bool             `json:"partner_accepted"`
	Verified        bool             `json:"verified"` // completed, and the bags and the purse show the items and gold gone
	Detail          string           `json:"detail,omitempty"`
}

// TradeFailed is the error of a trade that did not complete; Result says how far it got.
type TradeFailed struct{ Result TradeResult }

func (e *TradeFailed) Error() string {
	m := "trade " + e.Result.Result
	if e.Result.PartnerName != "" {
		m += " with " + e.Result.PartnerName
	}
	if e.Result.Detail != "" {
		m += ": " + e.Result.Detail
	}
	return m
}

// tradeStep is one window slot's worth of items from the plan.
type tradeStep struct {
	Item  objects.Item // the stack to put in the window: whole, or the part split off Src into Dst
	Src   objects.Item // the stack a part is split from; Item.GUID == 0 until then
	Dst   [2]uint8     // bag and slot a split goes to
	Split bool
	Count uint32 // what goes in the window
}

// planTrade works out which stacks fill the window for want. A request is met by one whole stack of exactly that
// count, else by splitting it off the smallest stack that has more, else by whole stacks, largest first, with the
// last piece found the same way. free is the empty slots (objects.Store.FreeSlots) the splits use. The window has
// six slots; a plan that needs more is an error, as is one that cannot be met from the bags.
func planTrade(items []objects.Item, free [][2]uint8, want []TradeItemReq) ([]tradeStep, error) {
	need := map[uint32]uint32{}
	var order []uint32
	for _, w := range want {
		if w.Entry == 0 {
			return nil, errors.New("trade: an item needs its entry")
		}
		c := w.Count
		if c == 0 {
			c = 1
		}
		if _, seen := need[w.Entry]; !seen {
			order = append(order, w.Entry)
		}
		need[w.Entry] += c
	}
	var steps []tradeStep
	for _, entry := range order {
		var stacks []objects.Item
		var have uint32
		for _, it := range items {
			if it.Entry == entry && it.Count > 0 {
				stacks = append(stacks, it)
				have += it.Count
			}
		}
		n := need[entry]
		if have < n {
			return nil, fmt.Errorf("trade: have %d of item %d in the bags, %d wanted", have, entry, n)
		}
		sort.Slice(stacks, func(i, j int) bool {
			if stacks[i].Count != stacks[j].Count {
				return stacks[i].Count > stacks[j].Count
			}
			if stacks[i].Bag != stacks[j].Bag {
				return stacks[i].Bag < stacks[j].Bag
			}
			return stacks[i].Slot < stacks[j].Slot
		})
		for n > 0 {
			exact, over := -1, -1 // the stack of exactly n, and the smallest above n (the list runs large to small)
			for i, st := range stacks {
				if st.Count == n {
					exact = i
					break
				}
				if st.Count > n {
					over = i
				}
			}
			switch {
			case exact >= 0:
				steps = append(steps, tradeStep{Item: stacks[exact], Count: n})
				stacks = slices.Delete(stacks, exact, exact+1)
				n = 0
			case over >= 0:
				if n > 255 {
					return nil, fmt.Errorf("trade: cannot split %d off a stack (the split packet counts to 255)", n)
				}
				if len(free) == 0 {
					return nil, fmt.Errorf("trade: no free bag slot to split %d of item %d into", n, entry)
				}
				steps = append(steps, tradeStep{Src: stacks[over], Dst: free[0], Split: true, Count: n})
				free = free[1:]
				stacks = slices.Delete(stacks, over, over+1)
				n = 0
			default: // every stack is smaller: the largest goes whole
				steps = append(steps, tradeStep{Item: stacks[0], Count: stacks[0].Count})
				n -= stacks[0].Count
				stacks = stacks[1:]
			}
		}
	}
	if len(steps) > game.TradeSlotsTraded {
		return nil, fmt.Errorf("trade: that takes %d trade slots and the window has %d: ask for fewer kinds of item or whole stacks", len(steps), game.TradeSlotsTraded)
	}
	return steps, nil
}

// tradeTarget finds the player to trade with: the guid, or a player in view by name (an exact name, else the only
// one that contains it). It checks the range and that the player is alive.
func (s *Session) tradeTarget(g packet.GUID, name string) (objects.Unit, string, error) {
	x, y, z, _ := s.selfPos()
	var u objects.Unit
	switch {
	case g != 0:
		var ok bool
		if u, ok = s.store.Get(g, x, y, z); !ok || !g.IsPlayer() {
			return u, "", fmt.Errorf("trade: %s is not a player in view", g)
		}
		u.GUID = g
	case strings.TrimSpace(name) != "":
		var cands []objects.Unit
		for _, c := range s.Units(objects.Filter{Name: name, Limit: 1 << 20}) {
			if c.Type == "player" {
				cands = append(cands, c)
			}
		}
		for _, c := range cands {
			if strings.EqualFold(c.Name, name) {
				u = c
				break
			}
		}
		if u.GUID == 0 {
			switch len(cands) {
			case 0:
				return u, "", fmt.Errorf("trade: no player named %q in view (walk up to them first)", name)
			case 1:
				u = cands[0]
			default:
				return u, "", fmt.Errorf("trade: %q matches %d players in view; use the full name or a guid", name, len(cands))
			}
		}
	default:
		return u, "", errors.New("trade needs a player: a guid or a name")
	}
	if u.GUID == s.selfGUID() {
		return u, "", errors.New("trade: cannot trade with yourself")
	}
	n, _ := s.cachedName(u.GUID)
	if n == "" {
		n = u.Name
	}
	who := n
	if who == "" {
		who = u.GUID.String()
	}
	switch {
	case u.Distance < 0:
		return u, n, fmt.Errorf("trade: no position known for %s yet", who)
	case u.Distance > tradeRange:
		return u, n, fmt.Errorf("trade: %s is %.1f yd away, past trade range (%.0f yd of the core's %.1f): walk closer first", who, u.Distance, tradeRange, game.TradeDistance)
	case u.Dead:
		return u, n, fmt.Errorf("trade: %s is dead", who)
	}
	return u, n, nil
}

// errStackMoved marks a plan the bags no longer match: the trade is cancelled, not the session.
var errStackMoved = errors.New("a stack moved while the window was open")

// Trade hands items and gold to another player in trade range and reports the outcome. It plans from the
// bags first and changes nothing when the request cannot be met (items missing, no free slot to split into, more
// than six stacks, more gold than the purse holds, the player too far). Then it opens the trade, splits stacks,
// fills the window, accepts after Options.TradeSettle, and waits for the other side's accept. A trade that does not
// complete is cancelled with CMSG_CANCEL_TRADE and returned as a *TradeFailed with the result. A split stack that
// stayed behind is not merged back. Every status is also logged as a trade_status event.
func (s *Session) Trade(ctx context.Context, req TradeRequest) (TradeResult, error) {
	if len(req.Items) == 0 && req.Gold == 0 {
		return TradeResult{}, errors.New("trade needs items or gold to hand over")
	}
	if req.OpenWait <= 0 {
		req.OpenWait = tradeOpenWait
	}
	if req.Wait <= 0 {
		req.Wait = tradeDoneWait
	}
	res := TradeResult{Gave: []TradeGave{}, Received: []game.TradeItem{}}
	var before uint32
	v, err := s.do(ctx, func() (any, error) {
		if err := s.inWorld(); err != nil {
			return nil, err
		}
		if s.trade.active {
			return nil, errors.New("trade: another trade is running")
		}
		u, name, err := s.tradeTarget(req.To, req.Name)
		if err != nil {
			return nil, err
		}
		self := s.selfGUID()
		before = s.store.Field(self, objects.PlayerCoinage)
		if req.Gold > before {
			return nil, fmt.Errorf("trade: %d copper wanted, the purse holds %d", req.Gold, before)
		}
		plan, err := planTrade(s.Items(), s.store.FreeSlots(self), req.Items)
		if err != nil {
			return nil, err
		}
		res.Partner, res.PartnerName = u.GUID, name
		s.trade = tradeState{active: true, partner: u.GUID, wakes: s.trade.wakes}
		if err := s.conn.WritePacket(opcode.CMSGInitiateTrade, game.BuildInitiateTrade(u.GUID)); err != nil {
			s.trade.active = false
			return nil, err
		}
		return plan, nil
	})
	if err != nil {
		return res, err
	}
	steps := v.([]tradeStep)
	defer s.releaseTrade()

	// finish ends a trade that did not complete the way it should: a status that closed the window
	// first (the other side cancelled) says why; otherwise result and detail do, and the window is cancelled.
	finish := func(result, detail string) (TradeResult, error) {
		r, ended := s.tradeSnapshot(res)
		if ended && r.Result == "completed" {
			s.verifyTrade(ctx, &r, steps, before, req.Gold)
			return r, nil
		}
		s.releaseTrade()
		if !ended {
			r.Result, r.Detail = result, detail
		}
		return r, &TradeFailed{r}
	}
	// stopped is the answer to an error from a wait or a send that is not about the trade: the context or the session.
	stopped := func(err error) bool {
		return ctx.Err() != nil || errors.Is(err, ErrClosed) || errors.Is(err, ErrNotInWorld)
	}

	// The other player's client answers with CMSG_BEGIN_TRADE; a refusal comes as a status instead.
	err = s.tradeWait(ctx, req.OpenWait, func() bool { return s.trade.open || s.trade.ended })
	if errors.Is(err, context.DeadlineExceeded) && ctx.Err() == nil {
		return finish("timeout", fmt.Sprintf("the window did not open within %s: nobody answered the request (a TortoiseBots bot answers its master and its party members)", req.OpenWait))
	} else if err != nil {
		return res, err
	}
	if r, ended := s.tradeSnapshot(res); ended {
		return finish(r.Result, r.Detail)
	}

	// Split first, so the window gets the right stacks; a split stack stays in the bags if the trade fails.
	for i := range steps {
		if !steps[i].Split {
			continue
		}
		if err := s.splitStack(ctx, &steps[i]); err != nil {
			if stopped(err) {
				return res, err
			}
			return finish("cancelled", err.Error())
		}
	}

	// Fill the window. A step's stack must still be where the plan found it.
	_, err = s.do(ctx, func() (any, error) {
		self := s.selfGUID()
		for i, st := range steps {
			it, ok := s.store.ItemAt(self, st.Item.Bag, st.Item.Slot)
			if !ok || it.GUID != st.Item.GUID || it.Count != st.Count {
				return nil, fmt.Errorf("item %d: %w", st.Item.Entry, errStackMoved)
			}
			if err := s.conn.WritePacket(opcode.CMSGSetTradeItem, game.BuildSetTradeItem(uint8(i), it.Bag, it.Slot)); err != nil {
				return nil, err
			}
		}
		if req.Gold > 0 {
			return nil, s.conn.WritePacket(opcode.CMSGSetTradeGold, game.BuildSetTradeGold(req.Gold))
		}
		return nil, nil
	})
	if errors.Is(err, errStackMoved) {
		return finish("cancelled", err.Error())
	} else if err != nil {
		return res, err
	}
	for _, st := range steps {
		res.Gave = append(res.Gave, TradeGave{Entry: st.Item.Entry, Count: st.Count, Split: st.Split})
	}
	res.Gold = req.Gold

	// Accept once the scam-prevention delay has passed, and again when the core sends back_to_trade.
	deadline := time.Now().Add(req.Wait)
	for attempt := 1; ; attempt++ {
		err := s.tradeWait(ctx, s.opts.TradeSettle, func() bool { return s.trade.ended })
		if err != nil && !errors.Is(err, context.DeadlineExceeded) || ctx.Err() != nil {
			return res, err
		}
		var backs uint64
		var sent bool
		if _, err := s.do(ctx, func() (any, error) {
			if s.trade.ended {
				return nil, nil
			}
			backs, sent = s.trade.backs, true
			return nil, s.conn.WritePacket(opcode.CMSGAcceptTrade, game.BuildAcceptTrade())
		}); err != nil {
			return res, err
		}
		if !sent {
			return finish("cancelled", "")
		}
		err = s.tradeWait(ctx, time.Until(deadline), func() bool { return s.trade.ended || s.trade.backs > backs })
		if errors.Is(err, context.DeadlineExceeded) && ctx.Err() == nil {
			return finish("timeout", fmt.Sprintf("the other side did not accept within %s (a bot that refuses a trade says why in a whisper: see chat_log)", req.Wait))
		} else if err != nil {
			return res, err
		}
		if r, ended := s.tradeSnapshot(res); ended {
			return finish(r.Result, r.Detail)
		}
		if attempt >= tradeMaxAccepts {
			return finish("cancelled", fmt.Sprintf("the core sent the window back to trade %d times after the accept (the other side keeps changing it, or refuses it)", attempt))
		}
	}
}

// tradeWait waits up to d for ready on the owner loop; context.DeadlineExceeded is the timeout.
func (s *Session) tradeWait(ctx context.Context, d time.Duration, ready func() bool) error {
	return s.await(ctx, max(d, time.Millisecond), &s.trade.wakes, ready)
}

// tradeSnapshot copies what the trade has learned into res: the other side's items, gold and accept. When a
// status that closes the window has come (ended) it also sets the result, status and detail from it.
func (s *Session) tradeSnapshot(res TradeResult) (TradeResult, bool) {
	v, err := s.do(context.Background(), func() (any, error) {
		t := &s.trade
		r := res
		if t.theirs != nil {
			r.Received, r.ReceivedGold = t.theirs.Items, t.theirs.Gold
		}
		r.PartnerAccepted = t.theirAccept
		if t.ended {
			r.Status = t.status.Name()
			r.Result, r.Detail = tradeResultName(t.status), tradeDetail(t.status)
		}
		return [2]any{r, t.ended}, nil
	})
	if err != nil {
		return res, false
	}
	p := v.([2]any)
	return p[0].(TradeResult), p[1].(bool)
}

// tradeResultName is the Result a closing status stands for.
func tradeResultName(st game.TradeStatus) string {
	switch st {
	case game.TradeComplete:
		return "completed"
	case game.TradeCancelled, game.TradeCloseWindow, game.TradeUnknown13:
		return "cancelled"
	case game.TradeBusy2:
		return "busy"
	case game.TradeTargetTooFar:
		return "too_far"
	}
	return st.Name()
}

func tradeDetail(st game.TradeStatus) string {
	switch st {
	case game.TradeCancelled, game.TradeCloseWindow, game.TradeUnknown13:
		return "the window was closed: the other side cancelled, or the core refused an item (a soulbound item cannot be traded)"
	case game.TradeTargetTooFar:
		return fmt.Sprintf("out of trade range (%.2f yd)", game.TradeDistance)
	case game.TradeBusy, game.TradeBusy2:
		return "the other player has a trade open already"
	case game.TradeIgnoreYou:
		return "the other player ignores trades from you"
	}
	return ""
}

// releaseTrade closes a trade that is still open with CMSG_CANCEL_TRADE, so the other player is not left in
// a window, waits briefly for the core's reply, and lets the next trade start. It is safe to call twice.
func (s *Session) releaseTrade() {
	bg, cancel := context.WithTimeout(context.Background(), 2*tradeCancelWait)
	defer cancel()
	v, err := s.do(bg, func() (any, error) {
		t := &s.trade
		if !t.active || t.ended {
			return false, nil
		}
		if s.State() == StatusInWorld {
			_ = s.conn.WritePacket(opcode.CMSGCancelTrade, game.BuildCancelTrade())
		}
		return true, nil
	})
	if err == nil && v.(bool) {
		_ = s.tradeWait(bg, tradeCancelWait, func() bool { return s.trade.ended })
	}
	_, _ = s.do(bg, func() (any, error) { s.trade.active = false; return nil, nil })
}

// splitStack splits st.Count off st.Src into st.Dst with CMSG_SPLIT_ITEM and waits for the new stack to show up.
func (s *Session) splitStack(ctx context.Context, st *tradeStep) error {
	self := s.selfGUID()
	_, err := s.do(ctx, func() (any, error) {
		src, ok := s.store.ItemAt(self, st.Src.Bag, st.Src.Slot)
		if !ok || src.GUID != st.Src.GUID || src.Count != st.Src.Count {
			return nil, fmt.Errorf("the stack of item %d moved before it could be split", st.Src.Entry)
		}
		if _, taken := s.store.ItemAt(self, st.Dst[0], st.Dst[1]); taken {
			return nil, fmt.Errorf("bag %d slot %d is no longer free for the split", st.Dst[0], st.Dst[1])
		}
		return nil, s.conn.WritePacket(opcode.CMSGSplitItem, game.BuildSplitItem(st.Src.Bag, st.Src.Slot, st.Dst[0], st.Dst[1], uint8(st.Count)))
	})
	if err != nil {
		return err
	}
	var got objects.Item
	ok := pollFor(ctx, tradeSplitWait, s.done, func() bool {
		it, ok := s.store.ItemAt(self, st.Dst[0], st.Dst[1])
		got = it
		return ok && it.Entry == st.Src.Entry && it.Count == st.Count
	})
	if !ok {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return fmt.Errorf("the split of %d from item %d did not show in bag %d slot %d within %s (an inventory_error event says why)", st.Count, st.Src.Entry, st.Dst[0], st.Dst[1], tradeSplitWait)
	}
	st.Item = got
	return nil
}

// verifyTrade checks, after a completed trade, that the bags and the purse show what went out. The updates follow the
// status; Verified stays false when they do not come in time.
func (s *Session) verifyTrade(ctx context.Context, r *TradeResult, steps []tradeStep, before, gold uint32) {
	self := s.selfGUID()
	wait := max(2*s.opts.TradeSettle, 100*time.Millisecond) // 2.2 s by default
	r.Verified = pollFor(ctx, wait, s.done, func() bool {
		for _, st := range steps {
			if it, ok := s.store.ItemAt(self, st.Item.Bag, st.Item.Slot); ok && it.GUID == st.Item.GUID {
				return false
			}
		}
		return gold == 0 || s.store.Field(self, objects.PlayerCoinage) <= before-gold
	})
	if !r.Verified && r.Detail == "" {
		r.Detail = "completed, but the bags or the purse did not show the change within " + wait.String()
	}
}

// pollFor polls cond every few ms until it holds (true) or d, ctx or the session ends (false).
func pollFor(ctx context.Context, d time.Duration, done <-chan struct{}, cond func() bool) bool {
	timer := time.NewTimer(d)
	defer timer.Stop()
	tick := time.NewTicker(5 * time.Millisecond)
	defer tick.Stop()
	for {
		if cond() {
			return true
		}
		select {
		case <-tick.C:
		case <-timer.C:
			return cond()
		case <-done:
			return false
		case <-ctx.Done():
			return false
		}
	}
}
