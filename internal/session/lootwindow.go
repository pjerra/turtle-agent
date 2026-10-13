package session

import (
	"context"
	"errors"
	"fmt"
	"math"
	"time"

	"github.com/pjerra/turtle-agent/internal/events"
	"github.com/pjerra/turtle-agent/internal/game"
	"github.com/pjerra/turtle-agent/internal/objects"
	"github.com/pjerra/turtle-agent/internal/opcode"
	"github.com/pjerra/turtle-agent/internal/packet"
)

// Core cites in this file are tortoise-wow upstream/1181dev 5784fb0.

// notifier wakes whoever waits on it. It belongs to the owner loop: wake and wait are
// called from handlers and from requests run by do.
type notifier struct{ ch chan struct{} }

// wait returns a channel that closes at the next wake.
func (n *notifier) wait() <-chan struct{} {
	if n.ch == nil {
		n.ch = make(chan struct{})
	}
	return n.ch
}

func (n *notifier) wake() {
	if n.ch != nil {
		close(n.ch)
	}
	n.ch = make(chan struct{})
}

// lootState is the loot window the server holds open for the player, and the first answer
// to the CMSG_LOOT that is waiting for one. Owner loop only.
type lootState struct {
	win *game.LootResponse // the open window; nil when none
	// pending says OpenLoot waits for an answer. The first answer after the request is kept in
	// ans, with the window as it stood then: a window with nothing in it is released right
	// behind its response, and the caller may only look after both have arrived.
	pending bool
	ans     lootAnswer
	ansWin  *LootWindow
	// skip is the window a new CMSG_LOOT closes first: the core releases the old loot
	// before it answers the new one (Player.cpp:9304-9305, LootHandler.cpp:463-481), and that release is no answer.
	skip  packet.GUID
	wakes notifier
}

type lootAnswer struct {
	guid     packet.GUID
	refused  bool
	released bool
	err      uint8
}

// LootSlot is one item of a loot window. Slot is what loot_take addresses.
type LootSlot struct {
	Slot           uint8  `json:"slot"`
	Item           uint32 `json:"item_entry"`
	Count          uint32 `json:"count"`
	DisplayID      uint32 `json:"display_id"`
	RandomProperty uint32 `json:"random_property,omitempty"`
	SlotType       string `json:"slot_type"` // allow_loot, roll_ongoing, master, locked, owner; only allow_loot and owner can be taken
}

// LootWindow is what the server shows for a corpse: the copper and the items.
type LootWindow struct {
	GUID  packet.GUID `json:"guid"`
	Type  string      `json:"type"` // corpse, pickpocket, fishing, disenchant
	Money uint32      `json:"money"`
	Items []LootSlot  `json:"items"`
}

func windowOf(r *game.LootResponse) *LootWindow {
	if r == nil {
		return nil
	}
	w := &LootWindow{GUID: r.GUID, Type: game.LootTypeName(r.Type), Money: r.Money, Items: make([]LootSlot, 0, len(r.Items))}
	for _, it := range r.Items {
		w.Items = append(w.Items, LootSlot{Slot: it.Slot, Item: it.Item, Count: it.Count, DisplayID: it.DisplayID,
			RandomProperty: it.RandomProp, SlotType: game.LootSlotTypeName(it.SlotType)})
	}
	return w
}

func (s *Session) registerLootWindow() {
	s.handlers[opcode.SMSGLootResponse] = s.onLootResponse
	s.handlers[opcode.SMSGLootReleaseResponse] = s.onLootRelease
	s.handlers[opcode.SMSGLootRemoved] = s.onLootRemoved
	s.handlers[opcode.SMSGLootClearMoney] = s.onLootClearMoney
	s.handlers[opcode.SMSGLootMoneyNotify] = s.onLootMoneyNotify
}

// LootWindow is the window now open, nil when there is none.
func (s *Session) LootWindow(ctx context.Context) (*LootWindow, error) {
	v, err := s.do(ctx, func() (any, error) {
		if err := s.inWorld(); err != nil {
			return nil, err
		}
		return windowOf(s.loot.win), nil
	})
	if err != nil {
		return nil, err
	}
	return v.(*LootWindow), nil
}

// OpenLoot opens the loot of a dead creature (CMSG_LOOT), given by guid or by name (the
// nearest dead lootable unit whose name contains it), and waits up to wait for the server's window. The core checks the reach itself (LootHandler.cpp:414-440,
// Player.cpp:9286-9305) and answers a refusal with a reason, which this returns as an
// error. A window with nothing in it is closed again at once, as the client does.
func (s *Session) OpenLoot(ctx context.Context, g packet.GUID, name string, wait time.Duration) (*LootWindow, error) {
	g, err := s.pickCorpse(g, name)
	if err != nil {
		return nil, err
	}
	_, err = s.do(ctx, func() (any, error) {
		if err := s.inWorld(); err != nil {
			return nil, err
		}
		s.loot.skip = 0
		if s.loot.win != nil {
			s.loot.skip = s.loot.win.GUID
		}
		s.loot.pending, s.loot.ans, s.loot.ansWin = true, lootAnswer{}, nil
		return nil, s.conn.WritePacket(opcode.CMSGLoot, game.BuildLoot(g))
	})
	if err != nil {
		return nil, err
	}
	return s.awaitLootAnswer(ctx, wait, nil)
}

// awaitLootAnswer waits for the answer to the loot request that was just sent (pending is set) and turns a refusal or a
// release into an error. With castFailed set, it also ends when a cast of the player fails (the Opening spell of a chest),
// and the error names the reason. castFailed is the count of failed casts before the request.
func (s *Session) awaitLootAnswer(ctx context.Context, wait time.Duration, castFailed *uint64) (*LootWindow, error) {
	var a lootAnswer
	var win *LootWindow
	var failReason string
	var failCode uint8
	if err := s.awaitLoot(ctx, wait, func() bool {
		if s.loot.pending {
			if castFailed != nil && s.mount.failed > *castFailed {
				failReason, failCode = s.mount.fail, s.mount.failCode
				return true
			}
			return false
		}
		a, win = s.loot.ans, s.loot.ansWin
		return true
	}); err != nil {
		s.do(context.WithoutCancel(ctx), func() (any, error) { s.loot.pending = false; return nil, nil })
		return nil, fmt.Errorf("no loot window from the server within %s: %w", wait, err)
	}
	if failReason != "" {
		s.do(context.WithoutCancel(ctx), func() (any, error) { s.loot.pending = false; return nil, nil })
		return nil, &CastFailedError{Reason: failReason, Code: failCode}
	}
	switch {
	case a.refused:
		return nil, fmt.Errorf("loot refused: %s (%s)", game.LootErrorText(a.err), game.LootErrorName(a.err))
	case a.released:
		return nil, errors.New("the server closed the loot at once: nothing to loot there, out of reach, or already looted")
	}
	return win, nil
}

// pickCorpse returns g, or the nearest dead unit with loot (UNIT_DYNFLAG_LOOTABLE) whose
// name contains name.
func (s *Session) pickCorpse(g packet.GUID, name string) (packet.GUID, error) {
	if g != 0 {
		return g, nil
	}
	if name == "" {
		return 0, errors.New("loot needs a guid or a name")
	}
	for _, u := range s.Units(objects.Filter{Name: name, Limit: math.MaxInt}) {
		if u.Dead && s.store.Field(u.GUID, objects.UnitDynamicFlags)&objects.DynFlagLootable != 0 {
			return u.GUID, nil
		}
	}
	return 0, fmt.Errorf("no lootable dead unit named %q nearby", name)
}

// awaitLoot runs ready on the owner loop until it is true, for at most wait. The error
// says the wait ran out, or the call was cancelled.
func (s *Session) awaitLoot(ctx context.Context, wait time.Duration, ready func() bool) error {
	return s.await(ctx, wait, &s.loot.wakes, ready)
}

// await is the wait loop behind loot_take, loot_close and mount: it asks ready on the owner
// loop, and sleeps on n between asks.
func (s *Session) await(ctx context.Context, wait time.Duration, n *notifier, ready func() bool) error {
	timer := time.NewTimer(wait)
	defer timer.Stop()
	for {
		v, err := s.do(ctx, func() (any, error) {
			if ready() {
				return nil, nil
			}
			return n.wait(), nil
		})
		if err != nil {
			return err
		}
		if v == nil {
			return nil
		}
		select {
		case <-v.(<-chan struct{}):
		case <-timer.C:
			return context.DeadlineExceeded
		case <-s.done:
			return ErrClosed
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}

// LootTake says what loot_take takes: one Slot, or All the items the player may take, plus
// the money (also alone, with Money).
type LootTake struct {
	Slot  *uint8
	All   bool
	Money bool
}

// LootTaken is the outcome of a take. Taken are the slots the server removed; Left the
// requested slots still in the window when the wait ended (a full bag refuses with an
// inventory_error event). Skipped are slots the player may not take (a running roll, the
// master looter's items).
type LootTaken struct {
	Taken   []int       `json:"taken"` // ints: a []uint8 would marshal as base64
	Left    []int       `json:"left,omitempty"`
	Skipped []int       `json:"skipped,omitempty"`
	Money   uint32      `json:"money,omitempty"` // copper asked for
	Window  *LootWindow `json:"window"`          // what remains; nil once the window closed
}

// TakeLoot takes items out of the open window with CMSG_AUTOSTORE_LOOT_ITEM and the money
// with CMSG_LOOT_MONEY, then waits up to wait for the server to remove them. The window
// closes by itself once empty.
func (s *Session) TakeLoot(ctx context.Context, take LootTake, wait time.Duration) (LootTaken, error) {
	if take.Slot == nil && !take.All && !take.Money {
		return LootTaken{}, errors.New("loot_take needs a slot, all, or money")
	}
	var res LootTaken
	var want []uint8
	var guid packet.GUID
	_, err := s.do(ctx, func() (any, error) {
		if err := s.inWorld(); err != nil {
			return nil, err
		}
		w := s.loot.win
		if w == nil {
			return nil, errors.New("no loot window is open; call loot first")
		}
		guid = w.GUID
		switch {
		case take.Slot != nil:
			var found *game.LootItem
			for i := range w.Items {
				if w.Items[i].Slot == *take.Slot {
					found = &w.Items[i]
				}
			}
			if found == nil {
				return nil, fmt.Errorf("slot %d is not in the loot window", *take.Slot)
			}
			if !game.LootTakeable(found.SlotType) {
				return nil, fmt.Errorf("slot %d cannot be taken (%s)", *take.Slot, game.LootSlotTypeName(found.SlotType))
			}
			want = append(want, found.Slot)
		case take.All:
			for _, it := range w.Items {
				if game.LootTakeable(it.SlotType) {
					want = append(want, it.Slot)
				} else {
					res.Skipped = append(res.Skipped, int(it.Slot))
				}
			}
		}
		money := w.Money > 0 && (take.Money || take.All)
		if len(want) == 0 && !money {
			return nil, errors.New("nothing to take in the loot window")
		}
		for _, slot := range want {
			if err := s.conn.WritePacket(opcode.CMSGAutostoreLootItem, game.BuildAutoStoreLootItem(slot)); err != nil {
				return nil, err
			}
		}
		if money {
			res.Money = w.Money
			return nil, s.conn.WritePacket(opcode.CMSGLootMoney, nil)
		}
		return nil, nil
	})
	if err != nil {
		return LootTaken{}, err
	}
	// Done when every asked slot is gone and the money with it, or the window closed.
	settled := func() bool {
		w := s.loot.win
		if w == nil || w.GUID != guid {
			return true
		}
		return len(missing(w, want)) == 0 && (res.Money == 0 || w.Money == 0)
	}
	werr := s.awaitLoot(ctx, wait, settled)
	if werr != nil && !errors.Is(werr, context.DeadlineExceeded) {
		return LootTaken{}, werr
	}
	_, err = s.do(ctx, func() (any, error) {
		w := s.loot.win
		if w != nil && w.GUID != guid {
			w = nil
		}
		left := missing(w, want)
		res.Taken = []int{}
		for _, slot := range want {
			if contains(left, slot) {
				res.Left = append(res.Left, int(slot))
			} else {
				res.Taken = append(res.Taken, int(slot))
			}
		}
		if w != nil && (len(w.Items) > 0 || w.Money > 0) {
			res.Window = windowOf(w)
		}
		return nil, nil
	})
	return res, err
}

// missing is the slots of want still in w.
func missing(w *game.LootResponse, want []uint8) []uint8 {
	var left []uint8
	if w == nil {
		return left
	}
	for _, slot := range want {
		for _, it := range w.Items {
			if it.Slot == slot {
				left = append(left, slot)
				break
			}
		}
	}
	return left
}

func contains(xs []uint8, x uint8) bool {
	for _, v := range xs {
		if v == x {
			return true
		}
	}
	return false
}

// CloseLoot closes the open window (CMSG_LOOT_RELEASE) and waits up to wait for the
// server's SMSG_LOOT_RELEASE_RESPONSE.
func (s *Session) CloseLoot(ctx context.Context, wait time.Duration) error {
	_, err := s.do(ctx, func() (any, error) {
		if err := s.inWorld(); err != nil {
			return nil, err
		}
		if s.loot.win == nil {
			return nil, errors.New("no loot window is open")
		}
		return nil, s.conn.WritePacket(opcode.CMSGLootRelease, game.BuildLootRelease(s.loot.win.GUID))
	})
	if err != nil {
		return err
	}
	if err := s.awaitLoot(ctx, wait, func() bool { return s.loot.win == nil }); err != nil {
		return fmt.Errorf("the server did not confirm the loot closed within %s: %w", wait, err)
	}
	return nil
}

// answer keeps the first answer after an OpenLoot request.
func (l *lootState) answer(a lootAnswer, win *LootWindow) {
	if l.pending {
		l.pending, l.ans, l.ansWin = false, a, win
	}
}

func (s *Session) onLootResponse(body []byte) error {
	r, err := game.ParseLootResponse(body)
	if err != nil {
		return err
	}
	l := &s.loot
	l.skip = 0
	if r.Refused {
		l.answer(lootAnswer{guid: r.GUID, refused: true, err: r.Err}, nil)
		s.emit(events.Event{Kind: events.KindLootError, SourceGUID: r.GUID, Text: "loot refused: " + game.LootErrorText(r.Err),
			Data: map[string]any{"reason": game.LootErrorName(r.Err), "code": r.Err}})
		l.wakes.wake()
		return nil
	}
	l.win = &r
	l.answer(lootAnswer{guid: r.GUID}, windowOf(l.win))
	s.emit(events.Event{Kind: events.KindLootOpen, SourceGUID: r.GUID, Amount: int64(r.Money), Data: lootOpenData(&r)})
	s.closeIfEmpty()
	l.wakes.wake()
	return nil
}

// lootOpenData is the data of a loot_open event; each call builds fresh maps.
func lootOpenData(r *game.LootResponse) map[string]any {
	items := make([]map[string]any, len(r.Items))
	for i, it := range r.Items {
		d := map[string]any{"slot": it.Slot, "item_entry": it.Item, "count": it.Count, "slot_type": game.LootSlotTypeName(it.SlotType)}
		if it.RandomProp != 0 {
			d["random_property"] = it.RandomProp
		}
		items[i] = d
	}
	return map[string]any{"type": game.LootTypeName(r.Type), "money": r.Money, "items": items}
}

// closeIfEmpty releases a window with nothing left in it, as the client closes the frame.
func (s *Session) closeIfEmpty() {
	w := s.loot.win
	if w == nil || len(w.Items) > 0 || w.Money > 0 {
		return
	}
	if err := s.conn.WritePacket(opcode.CMSGLootRelease, game.BuildLootRelease(w.GUID)); err != nil {
		fmt.Fprintf(s.logger, "loot release: %v\n", err)
	}
}

func (s *Session) onLootRelease(body []byte) error {
	g, err := game.ParseLootRelease(body)
	if err != nil {
		return err
	}
	l := &s.loot
	if l.skip == g {
		l.skip = 0
	} else {
		l.answer(lootAnswer{guid: g, released: true}, nil)
	}
	if l.win != nil && l.win.GUID == g {
		l.win = nil
	}
	s.emit(events.Event{Kind: events.KindLootClosed, SourceGUID: g})
	l.wakes.wake()
	return nil
}

func (s *Session) onLootRemoved(body []byte) error {
	slot, err := game.ParseLootRemoved(body)
	if err != nil {
		return err
	}
	w := s.loot.win
	if w == nil {
		return nil
	}
	for i, it := range w.Items {
		if it.Slot == slot {
			w.Items = append(w.Items[:i:i], w.Items[i+1:]...)
			s.emit(events.Event{Kind: events.KindLootTaken, SourceGUID: w.GUID, Amount: int64(it.Count),
				Data: map[string]any{"slot": slot, "item_entry": it.Item, "count": it.Count}})
			break
		}
	}
	s.closeIfEmpty()
	s.loot.wakes.wake()
	return nil
}

func (s *Session) onLootClearMoney([]byte) error {
	w := s.loot.win
	if w == nil {
		return nil
	}
	s.emit(events.Event{Kind: events.KindLootMoney, SourceGUID: w.GUID, Amount: int64(w.Money)})
	w.Money = 0
	s.closeIfEmpty()
	s.loot.wakes.wake()
	return nil
}

func (s *Session) onLootMoneyNotify(body []byte) error {
	m, err := game.ParseLootMoneyNotify(body)
	if err != nil {
		return err
	}
	s.emit(events.Event{Kind: events.KindLootMoneyShare, Amount: int64(m)})
	return nil
}
