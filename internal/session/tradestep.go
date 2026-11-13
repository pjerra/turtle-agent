package session

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/pjerra/turtle-agent/internal/game"
	"github.com/pjerra/turtle-agent/internal/objects"
	"github.com/pjerra/turtle-agent/internal/opcode"
	"github.com/pjerra/turtle-agent/internal/packet"
)

// Step trades. Trade (trade.go) opens a window, fills it, accepts and closes it in one call. A step trade is the same
// conversation driven one call at a time, so a test can do what Trade cannot: open the window and leave it open, take
// an incoming request (TradeExpect), put an item in any of the seven slots (TradeSetItem, slot 6 is the "will not be
// traded" slot), cast at the partner's item (CastOnTradeSlot, CMSG_CAST_SPELL with TARGET_FLAG_TRADE_ITEM) and accept
// (TradeAccept). The window stays open until it completes, the partner or the core closes it, or TradeCancel.
//
// Core cites are tortoise-wow TradeHandler.cpp and Spell.cpp as in trade.go.

// Defaults for the waits of a step trade.
const (
	tradeExpectWait  = 15 * time.Second
	tradeSetItemWait = 3 * time.Second
)

// errStepInFlight is the answer to a mutating step call made while another one runs.
var errStepInFlight = errors.New("trade: another trade step is in flight")

// beginStep marks a mutating step call as running and returns its token; owner loop only. A second call is refused at once.
func (s *Session) beginStep() (*struct{}, error) {
	t := &s.trade
	if t.inflight != nil {
		return nil, errStepInFlight
	}
	t.inflight = new(struct{})
	return t.inflight, nil
}

// endStep ends the mark of the call that holds tok; a fresh trade state has dropped it already.
func (s *Session) endStep(tok *struct{}) {
	if tok == nil {
		return
	}
	bg, cancel := context.WithTimeout(context.Background(), 2*tradeCancelWait)
	defer cancel()
	_, _ = s.do(bg, func() (any, error) {
		if s.trade.inflight == tok {
			s.trade.inflight = nil
		}
		return nil, nil
	})
}

// stepReleased reports whether the step trade was released (cancelled) under a waiting call. Owner loop only.
func (s *Session) stepReleased() bool { return !s.trade.active && !s.trade.ended }

// stepCancelled is the result of a waiting call whose trade was cancelled.
func (s *Session) stepCancelled(res TradeResult) *TradeFailed {
	r, _ := s.tradeSnapshot(res)
	r.Result, r.Detail = "cancelled", "the trade was cancelled (trade_cancel)"
	return &TradeFailed{r}
}

// errNoStepWindow is the answer to a step call made with no step trade open.
var errNoStepWindow = errors.New("no trade window is open: trade_open or trade_expect first")

// TradeStepState is the step trade as trade_status shows it: both sides of the window (SMSG_TRADE_STATUS_EXTENDED,
// trader_state 1 is the partner's side and 0 our own), the last status and an armed accept.
type TradeStepState struct {
	Active      bool             `json:"active"` // a trade exists (a one-shot trade too)
	Step        bool             `json:"step"`   // it is a step trade
	Open        bool             `json:"open"`   // the window is open
	Partner     packet.GUID      `json:"partner,omitempty"`
	PartnerName string           `json:"partner_name,omitempty"`
	Status      string           `json:"status,omitempty"` // the last SMSG_TRADE_STATUS, by name
	TheirItems  []game.TradeItem `json:"their_items"`
	TheirGold   uint32           `json:"their_gold"`
	TheirSpell  uint32           `json:"their_spell,omitempty"`
	MyItems     []game.TradeItem `json:"my_items"`
	MyGold      uint32           `json:"my_gold"`
	MySpell     uint32           `json:"my_spell,omitempty"` // the enchant our own trade-slot cast has pending
	TheirAccept bool             `json:"their_accept"`
	ArmedFrom   string           `json:"armed_from,omitempty"` // trade_expect waits for a request from this guid or name
}

// TradeSetResult is what TradeSetItem put in the window.
type TradeSetResult struct {
	TradeSlot uint8       `json:"trade_slot"`
	Bag       uint8       `json:"bag"`
	Slot      uint8       `json:"slot"`
	GUID      packet.GUID `json:"guid"`
	ItemEntry uint32      `json:"item_entry"`
	// Unchanged is true when the slot already held that item: nothing was sent and nothing changed.
	Unchanged bool `json:"unchanged,omitempty"`
}

// activeTrade reports whether a trade is running that a new one must not replace. A step trade whose window has closed
// is not (its state stays until the next call, or the next request, releases it). Owner loop only.
func (s *Session) activeTrade() bool {
	t := &s.trade
	return t.active && !(t.step && t.ended)
}

// armLive reports whether a trade_expect arm is waiting for its request. Owner loop only.
func (s *Session) armLive() bool {
	t := &s.trade
	return !t.active && !t.acceptUntil.IsZero() && s.now().Before(t.acceptUntil)
}

// busyErr is the refusal of a call that would start or arm a trade while one is running or a trade_expect is waiting: a
// new state would wipe the arm. Owner loop only.
func (s *Session) busyErr() error {
	switch {
	case s.activeTrade():
		return errors.New("trade: another trade is running")
	case s.armLive():
		return errors.New("trade: a trade_expect is waiting for a request (trade_cancel ends it)")
	}
	return nil
}

// armedFor reports whether an incoming trade request from g is the one trade_expect armed the accept for. Owner loop only.
func (s *Session) armedFor(g packet.GUID) bool {
	t := &s.trade
	if t.acceptUntil.IsZero() || !s.now().Before(t.acceptUntil) {
		return false
	}
	if t.acceptFrom != 0 && t.acceptFrom == g {
		return true
	}
	if t.acceptName != "" {
		n, ok := s.cachedName(g)
		return ok && strings.EqualFold(n, t.acceptName)
	}
	return false
}

// disarm drops an armed accept. Owner loop only.
func (t *tradeState) disarm() { t.acceptFrom, t.acceptName, t.acceptUntil = 0, "", time.Time{} }

// stepWindow checks that a step trade has its window open. Owner loop only.
func (s *Session) stepWindow() error {
	t := &s.trade
	switch {
	case t.active && t.step && t.open && !t.ended:
		return nil
	case t.active && !t.step:
		return errors.New("a one-shot trade is running; the step calls need a trade from trade_open or trade_expect")
	}
	return errNoStepWindow
}

// stepBase is the empty result of the step trade now running, with its partner.
func (s *Session) stepBase() TradeResult {
	res := TradeResult{Gave: []TradeGave{}, Received: []game.TradeItem{}}
	v, err := s.do(context.Background(), func() (any, error) {
		n, _ := s.cachedName(s.trade.partner)
		return [2]any{s.trade.partner, n}, nil
	})
	if err == nil {
		p := v.([2]any)
		res.Partner, res.PartnerName = p[0].(packet.GUID), p[1].(string)
	}
	return res
}

// stepFailed ends a step trade whose window closed (or never opened): the result says why from the closing status, and the
// state is released.
func (s *Session) stepFailed(res TradeResult, result, detail string) *TradeFailed {
	r, ended := s.tradeSnapshot(res)
	s.releaseTrade()
	if !ended {
		r.Result, r.Detail = result, detail
	}
	return &TradeFailed{r}
}

// cleanStaleIncoming cancels a declined incoming trade that may still be closing on the core, which would drop our request,
// and waits for the core's closing status. Trade does the same before it starts.
func (s *Session) cleanStaleIncoming(ctx context.Context) error {
	v, err := s.do(ctx, func() (any, error) {
		if s.trade.active || s.trade.incoming == 0 {
			return false, nil
		}
		return true, s.conn.WritePacket(opcode.CMSGCancelTrade, game.BuildCancelTrade())
	})
	if err != nil {
		return err
	}
	if v.(bool) {
		_ = s.tradeWait(ctx, tradeCancelWait, func() bool { return s.trade.incoming == 0 })
	}
	return nil
}

// TradeOpen starts a step trade with the player to (a guid) or name (a player in view) and returns once the window is
// open, which it leaves open. The checks and the request are those of Trade (range, alive, not yourself). When the
// window does not open within openWait (0: 8 s) or a status closes it, the trade is cancelled and the error is a *TradeFailed.
func (s *Session) TradeOpen(ctx context.Context, to packet.GUID, name string, openWait time.Duration) (TradeStepState, error) {
	if openWait <= 0 {
		openWait = tradeOpenWait
	}
	if err := s.cleanStaleIncoming(ctx); err != nil {
		return TradeStepState{}, err
	}
	res := TradeResult{Gave: []TradeGave{}, Received: []game.TradeItem{}}
	if _, err := s.do(ctx, func() (any, error) {
		if err := s.inWorld(); err != nil {
			return nil, err
		}
		if err := s.busyErr(); err != nil {
			return nil, err
		}
		u, n, err := s.tradeTarget(to, name)
		if err != nil {
			return nil, err
		}
		res.Partner, res.PartnerName = u.GUID, n
		s.trade = tradeState{active: true, step: true, partner: u.GUID, wakes: s.trade.wakes}
		if err := s.conn.WritePacket(opcode.CMSGInitiateTrade, game.BuildInitiateTrade(u.GUID)); err != nil {
			s.trade.active, s.trade.step = false, false
			return nil, err
		}
		return nil, nil
	}); err != nil {
		return TradeStepState{}, err
	}
	err := s.tradeWait(ctx, openWait, func() bool { return s.trade.open || s.trade.ended })
	if errors.Is(err, context.DeadlineExceeded) && ctx.Err() == nil {
		return TradeStepState{}, s.stepFailed(res, "timeout", fmt.Sprintf("the window did not open within %s: nobody answered the request (a TortoiseBots bot answers its master and its party members)", openWait))
	} else if err != nil {
		s.releaseTrade()
		return TradeStepState{}, err
	}
	if r, ended := s.tradeSnapshot(res); ended {
		return TradeStepState{}, s.stepFailed(res, r.Result, r.Detail)
	}
	return s.TradeStatus(), nil
}

// TradeExpect arms the accept of one incoming trade request, from the player from (a guid) or name, for wait (0: 15 s), and
// returns once the window is open. Every other request is declined, as when nothing is armed. The name is resolved to a
// guid when the player is in view; otherwise it is matched against the cached name when the request arrives. Nothing is
// sent until the request comes.
func (s *Session) TradeExpect(ctx context.Context, from packet.GUID, name string, wait time.Duration) (TradeStepState, error) {
	name = strings.TrimSpace(name)
	if from == 0 && name == "" {
		return TradeStepState{}, errors.New("trade_expect needs a player: a guid or a name")
	}
	if wait <= 0 {
		wait = tradeExpectWait
	}
	who := name
	if who == "" {
		who = from.String()
	}
	if err := s.cleanStaleIncoming(ctx); err != nil {
		return TradeStepState{}, err
	}
	if _, err := s.do(ctx, func() (any, error) {
		if err := s.inWorld(); err != nil {
			return nil, err
		}
		if err := s.busyErr(); err != nil {
			return nil, err
		}
		g, n := from, name
		if g == 0 {
			// The arm takes a full name only: a part of a name never matches the cached name of the request, so it would
			// arm for nobody. No one in view by that name arms on the cached name when the player shows up.
			cands := s.playerCandidates(n)
			for _, c := range cands {
				if strings.EqualFold(c.Name, n) {
					g, n = c.GUID, ""
				}
			}
			if g == 0 && len(cands) > 0 {
				return nil, fmt.Errorf("trade: no player named %q in view (trade_expect needs the full name; %d in view contain it)", n, len(cands))
			}
		}
		if g == s.selfGUID() {
			return nil, errors.New("trade: cannot trade with yourself")
		}
		s.trade = tradeState{acceptFrom: g, acceptName: n, acceptUntil: s.now().Add(wait), incoming: s.trade.incoming, wakes: s.trade.wakes}
		return nil, nil
	}); err != nil {
		return TradeStepState{}, err
	}
	err := s.tradeWait(ctx, wait, func() bool { return s.trade.open || (s.trade.active && s.trade.ended) })
	if err != nil {
		// Drop the arm; a request that was accepted but whose window never opened is cancelled.
		bg, cancel := context.WithTimeout(context.Background(), 2*tradeCancelWait)
		defer cancel()
		v, _ := s.do(bg, func() (any, error) { s.trade.disarm(); return s.trade.active, nil })
		if active, _ := v.(bool); active {
			s.releaseTrade()
		}
		if errors.Is(err, context.DeadlineExceeded) && ctx.Err() == nil {
			return TradeStepState{}, fmt.Errorf("no trade request from %s within %s", who, wait)
		}
		return TradeStepState{}, err
	}
	if r, ended := s.tradeSnapshot(s.stepBase()); ended {
		return TradeStepState{}, s.stepFailed(r, r.Result, r.Detail)
	}
	return s.TradeStatus(), nil
}

// TradeSetItem puts an item of the bags into trade slot tradeSlot (0-6; slot 6 is the one that is not traded, where an
// enchant is cast at the partner's item) with CMSG_SET_TRADE_ITEM. The core answers an effective change with
// back_to_trade to the setter (TradeData::SetItem calls SetAccepted(false), Player.cpp:529-541 and 589-599; the setter gets
// no view of its own side, Update(true) goes to the partner only), so the call returns on the first back_to_trade that
// arrives after this call's send, within 3 s. A back_to_trade has no sender: a change the partner makes in the same
// instant can be taken for the answer. The core ignores a set of the item already in the slot without any reply, so a slot
// the agent itself last set to that item (entry, bag, slot, count and guid; the record ends with the trade) is answered as
// unchanged without sending. A refused item (a soulbound one) closes the window: the error is a *TradeFailed.
func (s *Session) TradeSetItem(ctx context.Context, tradeSlot uint8, ref EquipRef) (TradeSetResult, error) {
	if tradeSlot > game.TradeSlotCount-1 {
		return TradeSetResult{}, errors.New("trade_set_item needs trade_slot 0-6")
	}
	if ref.Entry == 0 && ref.Slot == nil {
		return TradeSetResult{}, errors.New("trade_set_item needs an item_entry or a slot (bag defaults to the backpack, 255)")
	}
	var backs uint64
	var unchanged bool
	var tok *struct{}
	defer func() { s.endStep(tok) }()
	v, err := s.do(ctx, func() (any, error) {
		if err := s.inWorld(); err != nil {
			return nil, err
		}
		if err := s.stepWindow(); err != nil {
			return nil, err
		}
		if s.trade.inflight != nil {
			return nil, errStepInFlight
		}
		it, err := s.pickItem(ref)
		if err != nil {
			return nil, err
		}
		if rec, ok := s.trade.sets[tradeSlot]; ok && rec == (tradeSet{GUID: it.GUID, Entry: it.Entry, Bag: it.Bag, Slot: it.Slot, Count: it.Count}) {
			unchanged = true
			return it, nil
		}
		tok, _ = s.beginStep()
		backs = s.trade.backs // the count is taken at the send, on the owner loop: only a later back_to_trade answers it
		return it, s.conn.WritePacket(opcode.CMSGSetTradeItem, game.BuildSetTradeItem(tradeSlot, it.Bag, it.Slot))
	})
	if err != nil {
		return TradeSetResult{}, err
	}
	it := v.(objects.Item)
	out := TradeSetResult{TradeSlot: tradeSlot, Bag: it.Bag, Slot: it.Slot, GUID: it.GUID, ItemEntry: it.Entry}
	if unchanged {
		out.Unchanged = true
		return out, nil
	}
	err = s.tradeWait(ctx, tradeSetItemWait, func() bool { return s.trade.ended || !s.trade.active || s.trade.backs > backs })
	if r, ended := s.tradeSnapshot(s.stepBase()); ended {
		return TradeSetResult{}, s.stepFailed(r, r.Result, r.Detail)
	}
	if v, _ := s.do(context.Background(), func() (any, error) { return s.stepReleased(), nil }); v == true {
		return TradeSetResult{}, s.stepCancelled(s.stepBase())
	}
	if errors.Is(err, context.DeadlineExceeded) && ctx.Err() == nil {
		return TradeSetResult{}, fmt.Errorf("trade_set_item: no back_to_trade within %s of the set: the core did not take the item (see inventory_error events)", tradeSetItemWait)
	} else if err != nil {
		return TradeSetResult{}, err
	}
	_, _ = s.do(ctx, func() (any, error) {
		if t := &s.trade; t.active && t.step && !t.ended {
			if t.sets == nil {
				t.sets = map[uint8]tradeSet{}
			}
			t.sets[tradeSlot] = tradeSet{GUID: it.GUID, Entry: it.Entry, Bag: it.Bag, Slot: it.Slot, Count: it.Count}
		}
		return nil, nil
	})
	return out, nil
}

// unaccept withdraws our accept (CMSG_UNACCEPT_TRADE) from a window that is still open; best effort, on its own context so
// that a cancelled call still takes its accept back.
func (s *Session) unaccept() {
	bg, cancel := context.WithTimeout(context.Background(), 2*tradeCancelWait)
	defer cancel()
	_, _ = s.do(bg, func() (any, error) {
		if t := &s.trade; t.active && !t.ended && s.State() == StatusInWorld {
			return nil, s.conn.WritePacket(opcode.CMSGUnacceptTrade, game.BuildUnacceptTrade())
		}
		return nil, nil
	})
}

// TradeAccept accepts the open step trade after Options.TradeSettle and waits up to wait (0: 12 s) for the partner's accept
// to complete it. Unlike Trade it does not accept again by itself: a back_to_trade after the accept means the window
// changed (the core un-accepts both sides), and the result is "changed" with the window in trade_status so the caller sees
// what changed and decides. A completed trade, or one the window closed on, is done. When the partner does not accept in
// time the accept is withdrawn (CMSG_UNACCEPT_TRADE) and the window stays open: the error is a *TradeFailed with result
// timeout, and trade_accept again or trade_cancel decides.
func (s *Session) TradeAccept(ctx context.Context, wait time.Duration) (TradeResult, error) {
	if wait <= 0 {
		wait = tradeDoneWait
	}
	var tok *struct{}
	defer func() { s.endStep(tok) }()
	if _, err := s.do(ctx, func() (any, error) {
		if err := s.inWorld(); err != nil {
			return nil, err
		}
		if err := s.stepWindow(); err != nil {
			return nil, err
		}
		var err error
		tok, err = s.beginStep()
		return nil, err
	}); err != nil {
		return TradeResult{}, err
	}
	res := s.stepBase()
	deadline := time.Now().Add(wait)
	// ended turns a closed window into the result of a trade that did not complete (or did).
	ended := func() (TradeResult, error) {
		r, _ := s.tradeSnapshot(res)
		s.releaseTrade()
		if r.Result == "completed" {
			return r, nil
		}
		return r, &TradeFailed{r}
	}
	if err := s.tradeWait(ctx, s.opts.TradeSettle, func() bool { return s.trade.ended || !s.trade.active }); err != nil && !errors.Is(err, context.DeadlineExceeded) || ctx.Err() != nil {
		return res, err
	}
	var backs uint64
	var sent bool
	if _, err := s.do(ctx, func() (any, error) {
		if s.trade.ended || !s.trade.active {
			return nil, nil
		}
		backs, sent = s.trade.backs, true
		return nil, s.conn.WritePacket(opcode.CMSGAcceptTrade, game.BuildAcceptTrade())
	}); err != nil {
		return res, err
	}
	if !sent {
		if v, _ := s.do(context.Background(), func() (any, error) { return s.stepReleased(), nil }); v == true {
			return res, s.stepCancelled(res)
		}
		return ended()
	}
	err := s.tradeWait(ctx, time.Until(deadline), func() bool { return s.trade.ended || !s.trade.active || s.trade.backs > backs })
	if errors.Is(err, context.DeadlineExceeded) && ctx.Err() == nil {
		s.unaccept()
		r, _ := s.tradeSnapshot(res)
		r.Result = "timeout"
		r.Detail = fmt.Sprintf("the other side did not accept within %s; our accept was withdrawn and the window is still open (a bot that refuses a trade says why in a whisper: see chat_log)", wait)
		return r, &TradeFailed{r}
	} else if err != nil {
		s.unaccept()
		return res, err
	}
	if _, closed := s.tradeSnapshot(res); closed {
		return ended()
	}
	if v, _ := s.do(context.Background(), func() (any, error) { return s.stepReleased(), nil }); v == true {
		return res, s.stepCancelled(res)
	}
	r, _ := s.tradeSnapshot(res)
	st := s.TradeStatus()
	r.Result, r.TradeStatus = "changed", &st
	r.Detail = "the window changed after the accept (back_to_trade): both sides are un-accepted; read trade_status, then trade_accept again or trade_cancel"
	return r, nil
}

// TradeCancel closes a step trade (CMSG_CANCEL_TRADE, then up to a second for the core's closing status), drops an armed
// accept and returns the last status by name. It is safe to call twice.
func (s *Session) TradeCancel(ctx context.Context) (string, error) {
	if _, err := s.do(ctx, func() (any, error) {
		if s.trade.active && !s.trade.step {
			return nil, errors.New("a one-shot trade is running; it ends by itself")
		}
		s.trade.disarm()
		return nil, nil
	}); err != nil {
		return "", err
	}
	s.releaseTrade()
	v, err := s.do(ctx, func() (any, error) { return s.trade.last, nil })
	if err != nil {
		return "", err
	}
	return v.(string), nil
}

// TradeStatus is a snapshot of the trade; it never fails (a session that is not running shows no trade).
func (s *Session) TradeStatus() TradeStepState {
	st := TradeStepState{TheirItems: []game.TradeItem{}, MyItems: []game.TradeItem{}}
	v, err := s.do(context.Background(), func() (any, error) {
		t := &s.trade
		r := st
		r.Active, r.Step, r.Open, r.Partner, r.Status, r.TheirAccept = t.active, t.step, t.open, t.partner, t.last, t.theirAccept
		if t.partner != 0 {
			r.PartnerName, _ = s.cachedName(t.partner)
		}
		if t.theirs != nil {
			r.TheirItems, r.TheirGold, r.TheirSpell = t.theirs.Items, t.theirs.Gold, t.theirs.Spell
		}
		if t.mine != nil {
			r.MyItems, r.MyGold, r.MySpell = t.mine.Items, t.mine.Gold, t.mine.Spell
		}
		if !t.acceptUntil.IsZero() && s.now().Before(t.acceptUntil) {
			if t.acceptFrom != 0 {
				r.ArmedFrom = t.acceptFrom.String()
			} else {
				r.ArmedFrom = t.acceptName
			}
		}
		return r, nil
	})
	if err != nil {
		return st
	}
	return v.(TradeStepState)
}

// CastOnTradeSlot casts spell at the partner's item in trade slot slot (CMSG_CAST_SPELL with TARGET_FLAG_TRADE_ITEM, see
// game.BuildCastSpellOnTradeItem): an enchant for the item the partner put in slot 6, which the core applies when the trade
// completes. It needs an open step trade window (a one-shot trade is refused). Nothing else is checked here: slots 0-5 are sent too, and the core answers
// them with a spell failure (ITEM_NOT_READY).
func (s *Session) CastOnTradeSlot(ctx context.Context, spell uint32, slot uint8) error {
	if spell == 0 {
		return errors.New("cast needs a spell id")
	}
	if slot > game.TradeSlotCount-1 {
		return errors.New("cast needs a trade_slot 0-6")
	}
	_, err := s.do(ctx, func() (any, error) {
		if err := s.inWorld(); err != nil {
			return nil, err
		}
		if err := s.stepWindow(); err != nil {
			return nil, err
		}
		if s.trade.inflight != nil {
			return nil, errStepInFlight
		}
		return nil, s.conn.WritePacket(opcode.CMSGCastSpell, game.BuildCastSpellOnTradeItem(spell, slot))
	})
	return err
}
