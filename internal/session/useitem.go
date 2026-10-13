package session

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/pjerra/turtle-agent/internal/events"
	"github.com/pjerra/turtle-agent/internal/game"
	"github.com/pjerra/turtle-agent/internal/objects"
	"github.com/pjerra/turtle-agent/internal/opcode"
	"github.com/pjerra/turtle-agent/internal/packet"
	"github.com/pjerra/turtle-agent/internal/spells"
)

// Core cites in this file are tortoise-wow upstream/1181dev 5784fb0.

// mountState follows the player's own mount. Owner loop only.
type mountState struct {
	aura    uint32   // the aura that appeared with the mount display, when exactly one did; dismount cancels it
	cands   []uint32 // the auras that appeared with it when there were several: dismount needs a spell_id
	fail    string   // why the newest cast of the player failed
	failed  uint64   // counts failed casts of the player
	result  *uint32  // the newest SMSG_MOUNTRESULT, nil until one arrives
	seq     uint64   // counts SMSG_MOUNTRESULT and SMSG_DISMOUNTRESULT
	learns  uint64   // counts SMSG_LEARNED_SPELL: a bridle teaches its mount spell and is used up
	learned uint32   // the newest spell SMSG_LEARNED_SPELL named
	wakes   notifier
}

func (s *Session) registerUseItem() {
	s.handlers[opcode.SMSGMountresult] = s.onMountResult
	s.handlers[opcode.SMSGDismountresult] = s.onDismountResult
	s.handlers[opcode.SMSGLearnedSpell] = s.onLearnedSpell
}

// UseItem uses an item from the bags with CMSG_USE_ITEM, as a right-click does: food,
// drink, potions, scrolls, a mount item. A lootable item (a clam, a lockbox) opens with
// CMSG_OPEN_ITEM in 1.12, which is not covered. ref picks the item as for Equip (an
// entry searches the backpack, then the bags). target is the unit it is used on; 0 uses
// it on the player itself, which is also how the client uses an item with no target
// selected. The server answers through spell events (spell_start, aura_applied,
// stand_state when eating sits) or an inventory_error or spell_fail event for a refusal
// (SpellHandler.cpp:37-172). The item is returned as it stood when sent.
func (s *Session) UseItem(ctx context.Context, ref EquipRef, target packet.GUID) (objects.Item, error) {
	if ref.Entry == 0 && ref.Slot == nil {
		return objects.Item{}, errors.New("use_item needs an item_entry or a slot (bag defaults to the backpack, 255)")
	}
	v, err := s.do(ctx, func() (any, error) {
		if err := s.inWorld(); err != nil {
			return nil, err
		}
		it, err := s.pickItem(ref)
		if err != nil {
			return nil, err
		}
		return it, s.conn.WritePacket(opcode.CMSGUseItem, game.BuildUseItem(it.Bag, it.Slot, target))
	})
	if err != nil {
		return objects.Item{}, err
	}
	return v.(objects.Item), nil
}

// MountRef says what mounts the player: an item from the bags (Item) or a spell cast on
// the player (Spell).
type MountRef struct {
	Item  EquipRef
	Spell uint32
}

// MountInfo is the state after mount or dismount.
type MountInfo struct {
	Mounted   bool   `json:"mounted"`
	DisplayID uint32 `json:"display_id,omitempty"`    // UNIT_FIELD_MOUNTDISPLAYID
	Via       string `json:"via,omitempty"`           // item or spell
	Result    string `json:"result,omitempty"`        // the server's mount result when it sent one
	Learned   uint32 `json:"learned_spell,omitempty"` // the spell a bridle taught, which the agent cast
}

// Mounted reports whether the player's mount display is set.
func (s *Session) Mounted() bool { return s.store.Field(s.selfGUID(), objects.UnitMountDisplay) != 0 }

// Mount mounts with an item (CMSG_USE_ITEM) or a spell (CMSG_CAST_SPELL on the player) and
// waits up to wait for the mount display. A mount is a cast, so a refusal comes as spell_fail
// or inventory_error events (indoors, in combat, too low a level), or as an SMSG_MOUNTRESULT;
// when none of those comes in time, the cast may still be running and the call errors.
//
// A vanilla mount item is a bridle. Its use is a cast of its own (spell 46499, "Add Mount to
// Collection", about 4 s in agent.cap #4300-#4321) that teaches the mount spell and uses the item
// up: SMSG_LEARNED_SPELL comes with that cast's end, and no mount follows (SpellEffects.cpp:2241,
// Player.cpp:4591). After a learn the agent waits learnQuiet for another, then casts the spell it
// taught when that is a mount spell (spells.IsMount), the wait has not run out and no mount, failure
// or refusal came. Anything else is an error that names the spell, and nothing is cast.
func (s *Session) Mount(ctx context.Context, ref MountRef, wait time.Duration) (MountInfo, error) {
	if ref.Spell == 0 && ref.Item.Entry == 0 && ref.Item.Slot == nil {
		return MountInfo{}, errors.New("mount needs an item_entry (or bag and slot), or a spell_id")
	}
	info := MountInfo{Via: "item"}
	var seq, failed, learns uint64
	_, err := s.do(ctx, func() (any, error) {
		if err := s.inWorld(); err != nil {
			return nil, err
		}
		if s.Mounted() {
			return nil, errors.New("already mounted")
		}
		seq, failed, learns, s.mount.result = s.mount.seq, s.mount.failed, s.mount.learns, nil
		if ref.Spell != 0 {
			info.Via = "spell"
			return nil, s.conn.WritePacket(opcode.CMSGCastSpell, game.BuildCastSpell(ref.Spell, 0))
		}
		it, err := s.pickItem(ref.Item)
		if err != nil {
			return nil, err
		}
		return nil, s.conn.WritePacket(opcode.CMSGUseItem, game.BuildUseItem(it.Bag, it.Slot, 0))
	})
	if err != nil {
		return MountInfo{}, err
	}
	deadline := time.Now().Add(wait)
	bridle := ref.Spell == 0
	err = s.await(ctx, wait, &s.mount.wakes, func() bool {
		return s.mountSettled(seq, failed) || (bridle && s.mount.learns > learns)
	})
	if err != nil && !errors.Is(err, context.DeadlineExceeded) {
		return MountInfo{}, err
	}
	if bridle {
		if err := s.settleLearns(ctx, learns, deadline); err != nil {
			return MountInfo{}, err
		}
		taught, cerr := s.castLearned(ctx, learns, failed, seq, deadline)
		if cerr != nil {
			return info, cerr
		}
		if taught != 0 {
			info.Learned = taught
			err = s.await(ctx, time.Until(deadline), &s.mount.wakes, func() bool { return s.mountSettled(seq, failed) })
		}
	}
	return s.mountInfo(ctx, info, err, "mounted", failed)
}

// mountSettled reports whether a mount call that began at seq and failed has its answer: the
// mount display is set, a cast failed, or the server refused the mount.
func (s *Session) mountSettled(seq, failed uint64) bool {
	return s.Mounted() || s.mount.failed > failed || (s.mount.seq > seq && s.mount.result != nil && *s.mount.result != mountResultOK)
}

// learnQuiet is how long no SMSG_LEARNED_SPELL may come before the learns of one use are counted.
// A second learn can still be in a packet the owner loop has not read: agent.cap #2708 and #2709
// came in one update tick.
const learnQuiet = 250 * time.Millisecond

// settleLearns waits until no SMSG_LEARNED_SPELL has come for learnQuiet, or the deadline has
// passed, so that every spell the use taught is counted. learns is the count when the use began;
// it returns at once when no learn has come yet.
func (s *Session) settleLearns(ctx context.Context, learns uint64, deadline time.Time) error {
	seen, err := s.learnCount(ctx)
	if err != nil || seen == learns {
		return err
	}
	for {
		rem := time.Until(deadline)
		if rem <= 0 {
			return nil
		}
		timer := time.NewTimer(min(learnQuiet, rem))
		select {
		case <-timer.C:
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-s.done:
			timer.Stop()
			return ErrClosed
		}
		n, err := s.learnCount(ctx)
		if err != nil || n == seen {
			return err
		}
		seen = n
	}
}

// learnCount is how many SMSG_LEARNED_SPELL the session has handled.
func (s *Session) learnCount(ctx context.Context) (uint64, error) {
	v, err := s.do(ctx, func() (any, error) { return s.mount.learns, nil })
	if err != nil {
		return 0, err
	}
	return v.(uint64), nil
}

// TaughtError is a mount by item that stopped because the item taught a spell the agent did not
// cast. Spell is that spell, or 0 when the use taught several (see castLearned).
type TaughtError struct {
	Spell uint32
	msg   string
}

func (e *TaughtError) Error() string { return e.msg }

// castLearned casts the spell a use taught, once the learns have settled (see Mount). It returns
// the spell it cast, or 0 when the use taught none or a mount, failure or refusal came first. What
// it does not cast is a TaughtError that says why: several spells, a spell that is not a mount, or
// a learn that came after the deadline.
func (s *Session) castLearned(ctx context.Context, learns, failed, seq uint64, deadline time.Time) (uint32, error) {
	v, err := s.do(ctx, func() (any, error) {
		n, spell := s.mount.learns-learns, s.mount.learned
		switch {
		case n == 0 || s.mountSettled(seq, failed):
			return uint32(0), nil
		case n > 1:
			return uint32(0), &TaughtError{msg: fmt.Sprintf("not mounted: the item taught %d spells (the newest is %d), so the agent cast none; pass the mount's spell_id", n, spell)}
		case !spells.IsMount(spell):
			return uint32(0), &TaughtError{Spell: spell, msg: fmt.Sprintf("not mounted: the item taught spell %d, which is not a mount spell, so the agent did not cast it", spell)}
		case !time.Now().Before(deadline):
			return uint32(0), &TaughtError{Spell: spell, msg: fmt.Sprintf("not mounted: the item taught spell %d after the wait ran out, so the agent did not cast it; pass its spell_id to mount", spell)}
		}
		return spell, s.conn.WritePacket(opcode.CMSGCastSpell, game.BuildCastSpell(spell, 0))
	})
	if err != nil {
		return 0, err
	}
	return v.(uint32), nil
}

// mountResultOK is UnitMountResult MOUNTRESULT_OK (UnitDefines.h:591).
const mountResultOK uint32 = 10

// Dismount cancels the mount aura with CMSG_CANCEL_AURA (1.12 has no separate dismount
// packet) and waits up to wait for the display to clear. The aura is the one that appeared
// with the mount display, when only one did; spell names it when the mount came before the
// agent saw it (a login while mounted) or several auras came with it. The spell data to tell
// the mount aura (SPELL_AURA_MOUNTED) apart is not available to the agent.
func (s *Session) Dismount(ctx context.Context, spell uint32, wait time.Duration) (MountInfo, error) {
	_, err := s.do(ctx, func() (any, error) {
		if err := s.inWorld(); err != nil {
			return nil, err
		}
		if !s.Mounted() {
			return nil, errors.New("not mounted")
		}
		if spell == 0 {
			spell = s.mount.aura
		}
		switch {
		case spell != 0:
		case len(s.mount.cands) > 0:
			return nil, fmt.Errorf("several auras appeared with the mount (spells %v); pass the mount's spell_id", s.mount.cands)
		default:
			return nil, errors.New("the mount aura is unknown (mounted before the agent saw it); pass its spell_id")
		}
		return nil, s.conn.WritePacket(opcode.CMSGCancelAura, game.BuildCancelAura(spell))
	})
	if err != nil {
		return MountInfo{}, err
	}
	err = s.await(ctx, wait, &s.mount.wakes, func() bool { return !s.Mounted() })
	return s.mountInfo(ctx, MountInfo{}, err, "dismounted", 0)
}

// mountInfo fills in the state after a wait. A wait that ran out is an error saying the
// player did not reach want; failed is the failed-cast count when a mount began.
func (s *Session) mountInfo(ctx context.Context, info MountInfo, waitErr error, want string, failed uint64) (MountInfo, error) {
	if waitErr != nil && !errors.Is(waitErr, context.DeadlineExceeded) {
		return MountInfo{}, waitErr
	}
	var failReason string
	v, _ := s.do(ctx, func() (any, error) {
		if want == "mounted" && s.mount.failed > failed {
			failReason = s.mount.fail
		}
		info.DisplayID = s.store.Field(s.selfGUID(), objects.UnitMountDisplay)
		info.Mounted = info.DisplayID != 0
		if want == "mounted" && s.mount.result != nil {
			info.Result = game.MountResultName(*s.mount.result)
		}
		return info, nil
	})
	if v != nil {
		info = v.(MountInfo)
	}
	if (want == "mounted") != info.Mounted {
		reason := "the cast may still be running or was refused; read the spell_fail and inventory_error events"
		if info.Result != "" && info.Result != "ok" {
			reason = "the server answered " + info.Result
		}
		if failReason != "" {
			reason = "the cast failed: " + failReason
		}
		if info.Learned != 0 {
			reason = fmt.Sprintf("the item taught spell %d, which was cast: %s", info.Learned, reason)
		}
		return info, fmt.Errorf("not %s: %s", want, reason)
	}
	return info, nil
}

// noteCastFailed records a failed cast of the player and wakes a mount that waits on it.
func (s *Session) noteCastFailed(reason string) {
	s.mount.fail = reason
	s.mount.failed++
	s.mount.wakes.wake()
	s.loot.wakes.wake() // an Opening cast at a chest waits on the loot window and on this
}

func (s *Session) onMountResult(body []byte) error {
	v, err := game.ParseResultCode(body, "SMSG_MOUNTRESULT")
	if err != nil {
		return err
	}
	s.mount.result = &v
	s.mount.seq++
	s.emit(events.Event{Kind: events.KindMountResult, SourceGUID: s.selfGUID(),
		Data: map[string]any{"result": game.MountResultName(v), "code": v, "ok": v == mountResultOK}})
	s.mount.wakes.wake()
	return nil
}

func (s *Session) onDismountResult(body []byte) error {
	v, err := game.ParseResultCode(body, "SMSG_DISMOUNTRESULT")
	if err != nil {
		return err
	}
	s.mount.seq++
	s.emit(events.Event{Kind: events.KindMountResult, SourceGUID: s.selfGUID(),
		Data: map[string]any{"result": game.DismountResultName(v), "code": v, "ok": v == 3, "dismount": true}})
	s.mount.wakes.wake()
	return nil
}

// onLearnedSpell is SMSG_LEARNED_SPELL: the spell the player learned, a u32 (Player.cpp:4591-4593).
// It comes for a trainer, a quest or an item's learn effect; a bridle's mount is cast from here.
func (s *Session) onLearnedSpell(body []byte) error {
	r := packet.NewReader(body)
	spell := r.U32()
	if err := r.Err(); err != nil {
		return fmt.Errorf("SMSG_LEARNED_SPELL: %w", err)
	}
	s.mount.learns++
	s.mount.learned = spell
	s.spellState.Learn(spell)
	s.emit(events.Event{Kind: events.KindSpellLearned, SourceGUID: s.selfGUID(), SpellID: spell})
	s.mount.wakes.wake()
	return nil
}

// selfChanges handles what the player's own update fields report: a mount, a stand state.
// auras are the positive auras that appeared in the same update as the mount display.
func (s *Session) selfChanges(c objects.Change, auras []uint32) {
	switch c.Kind {
	case objects.ChangeMount:
		mounted := c.New != 0
		s.mount.aura, s.mount.cands = 0, nil
		if mounted && len(auras) == 1 {
			s.mount.aura = auras[0]
		} else if mounted {
			s.mount.cands = auras
		}
		s.emit(events.Event{Kind: events.KindMount, SourceGUID: c.GUID, Data: map[string]any{"mounted": mounted, "display_id": c.New}})
		s.mount.wakes.wake()
	case objects.ChangeCombo:
		s.emit(events.Event{Kind: events.KindComboPoints, SourceGUID: c.GUID, TargetGUID: s.comboTarget(),
			Data: map[string]any{"points": c.New, "was": c.Old}})
	case objects.ChangeStand:
		s.emit(events.Event{Kind: events.KindStandState, SourceGUID: c.GUID,
			Data: map[string]any{"state": game.StandStateName(c.New), "was": game.StandStateName(c.Old)}})
	}
}
