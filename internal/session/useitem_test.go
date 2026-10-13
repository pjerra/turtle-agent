package session

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/pjerra/turtle-agent/internal/events"
	"github.com/pjerra/turtle-agent/internal/game"
	"github.com/pjerra/turtle-agent/internal/game/gametest"
	"github.com/pjerra/turtle-agent/internal/objects"
	"github.com/pjerra/turtle-agent/internal/objects/objtest"
	"github.com/pjerra/turtle-agent/internal/opcode"
	"github.com/pjerra/turtle-agent/internal/packet"
)

const (
	bagGUID   = packet.GUID(0x4000000000000010)
	breadGUID = packet.GUID(0x4000000000000011)
)

// selfValues is a values update for the player itself (PLAYER_END fields, so the inventory
// words fit in the mask).
func selfValues(vals map[uint16]uint32) []byte {
	w := objtest.Header(1).U8(uint8(objects.BlockValues)).PackedGUID(me.GUID)
	objtest.Values(w, playerEnd, vals)
	return w.Bytes()
}

// bagWithBread puts a bag in inventory slot 19 holding bread at its slot 2.
func bagWithBread(h *harness) {
	h.t.Helper()
	bread, bag := uint64(breadGUID), uint64(bagGUID)
	w := objtest.Header(3)
	w.U8(uint8(objects.BlockCreate2)).PackedGUID(breadGUID).U8(objects.TypeItem).U8(0x10).U32(1)
	objtest.Values(w, 0x30, map[uint16]uint32{objects.FieldEntry: 4540, objects.ItemStackCount: 4})
	w.U8(uint8(objects.BlockCreate2)).PackedGUID(bagGUID).U8(objects.TypeContainer).U8(0x10).U32(1)
	objtest.Values(w, 0x80, map[uint16]uint32{objects.FieldEntry: 4500, objects.ItemStackCount: 1, objects.ContainerNumSlots: 6,
		objects.ContainerSlot1 + 4: uint32(bread), objects.ContainerSlot1 + 5: uint32(bread >> 32)})
	w.U8(uint8(objects.BlockValues)).PackedGUID(me.GUID)
	objtest.Values(w, playerEnd, map[uint16]uint32{objects.PlayerInvSlotHead + 38: uint32(bag), objects.PlayerInvSlotHead + 39: uint32(bag >> 32)})
	h.send(opcode.SMSGUpdateObject, w.Bytes())
	waitFor(h.t, func() bool { return len(h.s.Items()) == len(h.s.Inventory())+1 })
}

func TestUseItemFromBackpackAndBags(t *testing.T) {
	ctx := context.Background()
	h := start(t, Options{})
	h.enterWorld()
	h.send(opcode.SMSGUpdateObject, ownInventory(map[uint8]packet.GUID{30: potionGUID}, map[packet.GUID][2]uint32{potionGUID: {118, 5}}))
	waitFor(t, func() bool { return len(h.s.Inventory()) == 1 })
	bagWithBread(h)

	// A potion on the player: right-click with nothing selected.
	if it, err := h.s.UseItem(ctx, EquipRef{Entry: 118}, 0); err != nil || it.Slot != 30 {
		t.Fatalf("%+v %v", it, err)
	}
	if p := h.expect(opcode.CMSGUseItem); string(p.Body) != string(game.BuildUseItem(255, 30, 0)) {
		t.Fatalf("% X", p.Body)
	}
	// Bread from a bag, by entry, on a unit.
	if it, err := h.s.UseItem(ctx, EquipRef{Entry: 4540}, wolfGUID); err != nil || it.Bag != 19 || it.Slot != 2 || it.Count != 4 {
		t.Fatalf("%+v %v", it, err)
	}
	if p := h.expect(opcode.CMSGUseItem); string(p.Body) != string(game.BuildUseItem(19, 2, wolfGUID)) {
		t.Fatalf("% X", p.Body)
	}
	// By bag and slot.
	bag, slot := uint8(19), uint8(2)
	if _, err := h.s.UseItem(ctx, EquipRef{Bag: &bag, Slot: &slot}, 0); err != nil {
		t.Fatal(err)
	}
	h.expect(opcode.CMSGUseItem)
	if _, err := h.s.UseItem(ctx, EquipRef{}, 0); err == nil || !strings.Contains(err.Error(), "item_entry or a slot") {
		t.Fatalf("no item: %v", err)
	}
	if _, err := h.s.UseItem(ctx, EquipRef{Entry: 9999}, 0); err == nil || !strings.Contains(err.Error(), "not in the backpack or the bags") {
		t.Fatalf("missing: %v", err)
	}
	if p, err := h.srv.Expect(opcode.CMSGUseItem, 100*time.Millisecond); err == nil {
		t.Fatalf("sent % X", p.Body)
	}
	// The server's refusal is an inventory_error event, the existing path.
	h.send(opcode.SMSGInventoryChangeFailure, gametest.InventoryChangeFailure(2, 0, potionGUID, 0))
	waitFor(t, func() bool { return len(ofKind(h.s.Log(), events.KindInventoryError)) == 1 })
	h.logout()
}

// Eating sets the stand state to sit and applies a food aura: both reach the event log.
func TestEatingShowsAuraAndSit(t *testing.T) {
	h := start(t, Options{})
	h.enterWorld()
	h.send(opcode.SMSGUpdateObject, ownInventory(nil, nil))
	waitFor(t, func() bool { return h.s.Store().Field(me.GUID, objects.UnitHealth) == 100 })
	h.send(opcode.SMSGUpdateObject, selfValues(map[uint16]uint32{objects.UnitBytes1: 1, objects.UnitAura + 2: 433}))
	h.send(opcode.SMSGUpdateObject, selfValues(map[uint16]uint32{objects.UnitBytes1: 0, objects.UnitAura + 2: 0}))
	h.logout()
	st := ofKind(h.s.Log(), events.KindStandState)
	if len(st) != 2 || st[0].Data["state"] != "sit" || st[0].Data["was"] != "stand" || st[1].Data["state"] != "stand" || st[0].SourceGUID != me.GUID {
		t.Fatalf("%+v", st)
	}
	if a := ofKind(h.s.Log(), events.KindAuraApplied); len(a) != 1 || a[0].SpellID != 433 {
		t.Fatalf("%+v", a)
	}
}

// mountUp makes the server mount the player: the display field and the mount aura arrive in one update.
func mountUp(h *harness, display, spell uint32) {
	h.send(opcode.SMSGUpdateObject, selfValues(map[uint16]uint32{objects.UnitMountDisplay: display, objects.UnitAura + 5: spell}))
}

func TestMountAndDismount(t *testing.T) {
	ctx := context.Background()
	h := start(t, Options{})
	h.enterWorld()
	h.send(opcode.SMSGUpdateObject, ownInventory(map[uint8]packet.GUID{25: swordGUID}, map[packet.GUID][2]uint32{swordGUID: {13317, 1}}))
	waitFor(t, func() bool { return len(h.s.Inventory()) == 1 })

	if _, err := h.s.Mount(ctx, MountRef{}, time.Second); err == nil || !strings.Contains(err.Error(), "item_entry") {
		t.Fatalf("nothing: %v", err)
	}
	if _, err := h.s.Dismount(ctx, 0, time.Second); err == nil || !strings.Contains(err.Error(), "not mounted") {
		t.Fatalf("dismount on foot: %v", err)
	}

	// Mount with the item: use_item, then the server's mount display and aura.
	done := make(chan MountInfo, 1)
	go func() {
		info, err := h.s.Mount(ctx, MountRef{Item: EquipRef{Entry: 13317}}, 3*time.Second)
		if err != nil {
			t.Error(err)
		}
		done <- info
	}()
	if p := h.expect(opcode.CMSGUseItem); string(p.Body) != string(game.BuildUseItem(255, 25, 0)) {
		t.Fatalf("% X", p.Body)
	}
	h.send(opcode.SMSGMountresult, gametest.ResultCode(10))
	mountUp(h, 14337, 458)
	if info := <-done; !info.Mounted || info.DisplayID != 14337 || info.Via != "item" || info.Result != "ok" {
		t.Fatalf("%+v", info)
	}
	if !h.s.Mounted() {
		t.Fatal("not mounted")
	}
	if _, err := h.s.Mount(ctx, MountRef{Spell: 458}, time.Second); err == nil || !strings.Contains(err.Error(), "already mounted") {
		t.Fatalf("again: %v", err)
	}

	// Dismount cancels the aura that came with the display.
	go func() {
		info, err := h.s.Dismount(ctx, 0, 3*time.Second)
		if err != nil {
			t.Error(err)
		}
		done <- info
	}()
	if p := h.expect(opcode.CMSGCancelAura); string(p.Body) != string(game.BuildCancelAura(458)) {
		t.Fatalf("% X", p.Body)
	}
	h.send(opcode.SMSGUpdateObject, selfValues(map[uint16]uint32{objects.UnitMountDisplay: 0, objects.UnitAura + 5: 0}))
	if info := <-done; info.Mounted {
		t.Fatalf("%+v", info)
	}

	// A mount by spell goes out as a cast on the player; the server refuses it.
	go func() {
		_, err := h.s.Mount(ctx, MountRef{Spell: 458}, 3*time.Second)
		done <- MountInfo{Result: errText(err)}
	}()
	if p := h.expect(opcode.CMSGCastSpell); string(p.Body) != string(game.BuildCastSpell(458, 0)) {
		t.Fatalf("% X", p.Body)
	}
	h.send(opcode.SMSGMountresult, gametest.ResultCode(6))
	if info := <-done; !strings.Contains(info.Result, "looting") {
		t.Fatalf("refused: %q", info.Result)
	}
	// Nothing comes back: the call says so after the wait.
	if _, err := h.s.Mount(ctx, MountRef{Spell: 458}, 100*time.Millisecond); err == nil || !strings.Contains(err.Error(), "not mounted") {
		t.Fatalf("timeout: %v", err)
	}
	h.logout()
	m := ofKind(h.s.Log(), events.KindMount)
	if len(m) != 2 || m[0].Data["mounted"] != true || m[0].Data["display_id"] != uint32(14337) || m[1].Data["mounted"] != false {
		t.Fatalf("%+v", m)
	}
	if r := ofKind(h.s.Log(), events.KindMountResult); len(r) != 2 || r[1].Data["result"] != "looting" {
		t.Fatalf("%+v", r)
	}
}

func errText(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

// A player who was mounted before the agent saw the aura must name it.
func TestDismountNeedsTheAuraWhenUnknown(t *testing.T) {
	ctx := context.Background()
	h := start(t, Options{})
	h.enterWorld()
	h.send(opcode.SMSGUpdateObject, ownInventory(nil, nil))
	waitFor(t, func() bool { return h.s.Store().Field(me.GUID, objects.UnitHealth) == 100 })
	// The mount display arrives alone, with no aura in the same update.
	h.send(opcode.SMSGUpdateObject, selfValues(map[uint16]uint32{objects.UnitMountDisplay: 14337}))
	waitFor(t, h.s.Mounted)
	if _, err := h.s.Dismount(ctx, 0, time.Second); err == nil || !strings.Contains(err.Error(), "spell_id") {
		t.Fatalf("%v", err)
	}
	done := make(chan error, 1)
	go func() {
		_, err := h.s.Dismount(ctx, 458, 2*time.Second)
		done <- err
	}()
	if p := h.expect(opcode.CMSGCancelAura); string(p.Body) != string(game.BuildCancelAura(458)) {
		t.Fatalf("% X", p.Body)
	}
	h.send(opcode.SMSGUpdateObject, selfValues(map[uint16]uint32{objects.UnitMountDisplay: 0}))
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	h.logout()
}

// Several auras arriving with the mount display: dismount will not guess which one to cancel.
func TestDismountWithSeveralNewAuras(t *testing.T) {
	ctx := context.Background()
	h := start(t, Options{})
	h.enterWorld()
	h.send(opcode.SMSGUpdateObject, ownInventory(nil, nil))
	waitFor(t, func() bool { return h.s.Store().Field(me.GUID, objects.UnitHealth) == 100 })
	h.send(opcode.SMSGUpdateObject, selfValues(map[uint16]uint32{objects.UnitMountDisplay: 14337, objects.UnitAura + 5: 458, objects.UnitAura + 6: 2000}))
	waitFor(t, h.s.Mounted)
	if _, err := h.s.Dismount(ctx, 0, time.Second); err == nil || !strings.Contains(err.Error(), "several auras") || !strings.Contains(err.Error(), "2000") {
		t.Fatalf("%v", err)
	}
	if p, err := h.srv.Expect(opcode.CMSGCancelAura, 100*time.Millisecond); err == nil {
		t.Fatalf("sent % X", p.Body)
	}
	done := make(chan error, 1)
	go func() { _, err := h.s.Dismount(ctx, 458, 2*time.Second); done <- err }()
	h.expect(opcode.CMSGCancelAura)
	h.send(opcode.SMSGUpdateObject, selfValues(map[uint16]uint32{objects.UnitMountDisplay: 0}))
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	h.logout()
}

// The player's own failed cast ends a mount at once, with the server's reason.
func TestMountEndsOnCastFailure(t *testing.T) {
	h := start(t, Options{})
	h.enterWorld()
	h.send(opcode.SMSGUpdateObject, ownInventory(nil, nil))
	waitFor(t, func() bool { return h.s.Store().Field(me.GUID, objects.UnitHealth) == 100 })
	done := make(chan error, 1)
	began := time.Now()
	go func() {
		_, err := h.s.Mount(context.Background(), MountRef{Spell: 458}, 10*time.Second)
		done <- err
	}()
	h.expect(opcode.CMSGCastSpell)
	h.send(opcode.SMSGCastResult, packet.NewWriter().U32(458).U8(2).U8(66).Bytes())
	err := <-done
	if err == nil || !strings.Contains(err.Error(), "SpellCastResult 66") || time.Since(began) > 5*time.Second {
		t.Fatalf("%v after %s", err, time.Since(began))
	}
	h.logout()
}

// Bridle packets from the live run of #61 (tests/ta61/agent.cap; #N is the record index).
var (
	// #4321 SMSG_LEARNED_SPELL: the mount spell 6648, taught when the use's cast (46499) ended.
	learned6648 = []byte{0xf8, 0x19, 0x00, 0x00}
	// #2708 SMSG_LEARNED_SPELL: spell 2383 (Find Herbs), which is no mount.
	learned2383 = []byte{0x4f, 0x09, 0x00, 0x00}
	// #2709 SMSG_LEARNED_SPELL: spell 2366 (Herb Gathering), in the same update tick as #2708.
	learned2366 = []byte{0x3e, 0x09, 0x00, 0x00}
)

// A vanilla mount item is a bridle: its use teaches the mount spell and uses the item up, with no
// cast after it. The agent casts the spell it taught, mounts by it and reports it as learned_spell.
func TestMountCastsTheSpellABridleTeaches(t *testing.T) {
	ctx := context.Background()
	h := start(t, Options{})
	h.enterWorld()
	h.send(opcode.SMSGUpdateObject, ownInventory(map[uint8]packet.GUID{25: swordGUID}, map[packet.GUID][2]uint32{swordGUID: {5655, 1}}))
	waitFor(t, func() bool { return len(h.s.Inventory()) == 1 })
	done := make(chan MountInfo, 1)
	go func() {
		info, err := h.s.Mount(ctx, MountRef{Item: EquipRef{Entry: 5655}}, 3*time.Second)
		if err != nil {
			t.Error(err)
		}
		done <- info
	}()
	h.expect(opcode.CMSGUseItem)
	h.send(opcode.SMSGLearnedSpell, learned6648)
	if p := h.expect(opcode.CMSGCastSpell); string(p.Body) != string(game.BuildCastSpell(6648, 0)) {
		t.Fatalf("% X", p.Body)
	}
	h.send(opcode.SMSGMountresult, gametest.ResultCode(10))
	mountUp(h, 2405, 6648)
	if info := <-done; !info.Mounted || info.DisplayID != 2405 || info.Via != "item" || info.Learned != 6648 || info.Result != "ok" {
		t.Fatalf("%+v", info)
	}
	// spell_learned goes through emit, which can queue it behind an event waiting for a name, so Mount
	// returning does not mean it is in the log yet.
	waitFor(t, func() bool { return len(ofKind(h.s.Log(), events.KindSpellLearned)) == 1 })
	if l := ofKind(h.s.Log(), events.KindSpellLearned); len(l) != 1 || l[0].SpellID != 6648 {
		t.Fatalf("%+v", l)
	}
	h.logout()
}

// The taught spell's cast can be refused like any cast: the call says what was taught and why.
func TestMountReportsARefusedBridleSpell(t *testing.T) {
	ctx := context.Background()
	h := start(t, Options{})
	h.enterWorld()
	h.send(opcode.SMSGUpdateObject, ownInventory(map[uint8]packet.GUID{25: swordGUID}, map[packet.GUID][2]uint32{swordGUID: {5655, 1}}))
	waitFor(t, func() bool { return len(h.s.Inventory()) == 1 })
	done := make(chan error, 1)
	go func() {
		_, err := h.s.Mount(ctx, MountRef{Item: EquipRef{Entry: 5655}}, 3*time.Second)
		done <- err
	}()
	h.expect(opcode.CMSGUseItem)
	h.send(opcode.SMSGLearnedSpell, learned6648)
	h.expect(opcode.CMSGCastSpell)
	h.send(opcode.SMSGCastResult, packet.NewWriter().U32(6648).U8(2).U8(66).Bytes())
	if err := <-done; err == nil || !strings.Contains(err.Error(), "taught spell 6648") || !strings.Contains(err.Error(), "SpellCastResult 66") {
		t.Fatalf("%v", err)
	}
	h.logout()
}

// bridleUse starts a mount by the bridle 5655 in the backpack and waits for its use to be sent.
func bridleUse(t *testing.T, h *harness) <-chan error {
	t.Helper()
	h.enterWorld()
	h.send(opcode.SMSGUpdateObject, ownInventory(map[uint8]packet.GUID{25: swordGUID}, map[packet.GUID][2]uint32{swordGUID: {5655, 1}}))
	waitFor(t, func() bool { return len(h.s.Inventory()) == 1 })
	done := make(chan error, 1)
	go func() {
		_, err := h.s.Mount(context.Background(), MountRef{Item: EquipRef{Entry: 5655}}, 3*time.Second)
		done <- err
	}()
	h.expect(opcode.CMSGUseItem)
	return done
}

// notSent fails the test when the client sends op within a short wait.
func (h *harness) notSent(op uint16) {
	h.t.Helper()
	if p, err := h.srv.Expect(op, 300*time.Millisecond); err == nil {
		h.t.Fatalf("sent %s: % X", opcode.Name(op), p.Body)
	}
}

// A use that teaches two spells leaves the choice to the caller: the agent casts neither. The
// second learn comes 50 ms after the first and is still counted (the quiet window).
func TestBridleTeachingTwoSpellsCastsNothing(t *testing.T) {
	h := start(t, Options{})
	done := bridleUse(t, h)
	h.send(opcode.SMSGLearnedSpell, learned6648)
	time.Sleep(50 * time.Millisecond)
	h.send(opcode.SMSGLearnedSpell, learned2366)
	if err := <-done; err == nil || !strings.Contains(err.Error(), "taught 2 spells") || !strings.Contains(err.Error(), "2366") {
		t.Fatalf("%v", err)
	}
	h.notSent(opcode.CMSGCastSpell)
	h.logout()
}

// A bridle that taught a spell which is not a mount is not cast: the call names the spell.
func TestBridleTeachingANonMountCastsNothing(t *testing.T) {
	h := start(t, Options{})
	done := bridleUse(t, h)
	h.send(opcode.SMSGLearnedSpell, learned2383)
	err := <-done
	if te, ok := err.(*TaughtError); !ok || te.Spell != 2383 || !strings.Contains(err.Error(), "not a mount spell") {
		t.Fatalf("%v", err)
	}
	h.notSent(opcode.CMSGCastSpell)
	h.logout()
}

// A learn that comes after the wait ran out is not cast: the call has already answered.
func TestLearnAfterTheWaitIsNotCast(t *testing.T) {
	h := start(t, Options{})
	h.enterWorld()
	h.send(opcode.SMSGLearnedSpell, learned6648)
	waitFor(t, func() bool { return len(ofKind(h.s.Log(), events.KindSpellLearned)) == 1 })
	if _, err := h.s.castLearned(context.Background(), 0, 0, 0, time.Now().Add(-time.Second)); err == nil || !strings.Contains(err.Error(), "wait ran out") {
		t.Fatalf("%v", err)
	}
	h.notSent(opcode.CMSGCastSpell)
	h.logout()
}
