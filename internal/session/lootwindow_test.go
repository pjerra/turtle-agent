package session

import (
	"context"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/TortoiseBots/turtle-agent/internal/events"
	"github.com/TortoiseBots/turtle-agent/internal/game"
	"github.com/TortoiseBots/turtle-agent/internal/game/gametest"
	"github.com/TortoiseBots/turtle-agent/internal/objects"
	"github.com/TortoiseBots/turtle-agent/internal/opcode"
)

type openResult struct {
	w   *LootWindow
	err error
}

// openLoot runs OpenLoot beside the scripted server and returns once the CMSG_LOOT is in.
func openLoot(t *testing.T, h *harness, name string) chan openResult {
	t.Helper()
	done := make(chan openResult, 1)
	go func() {
		w, err := h.s.OpenLoot(context.Background(), wolfGUID, name, 2*time.Second)
		done <- openResult{w, err}
	}()
	if p := h.expect(opcode.CMSGLoot); string(p.Body) != string(game.BuildLoot(wolfGUID)) {
		t.Fatalf("% X", p.Body)
	}
	return done
}

func lootRig(t *testing.T) *harness {
	t.Helper()
	h := start(t, Options{})
	h.enterWorld()
	wolfNearby(h)
	return h
}

func TestLootOpenTakeAllAndClose(t *testing.T) {
	ctx := context.Background()
	h := lootRig(t)
	if _, err := h.s.TakeLoot(ctx, LootTake{All: true}, time.Second); err == nil || !strings.Contains(err.Error(), "no loot window") {
		t.Fatalf("take with no window: %v", err)
	}
	if err := h.s.CloseLoot(ctx, time.Second); err == nil || !strings.Contains(err.Error(), "no loot window") {
		t.Fatalf("close with no window: %v", err)
	}
	if w, err := h.s.LootWindow(ctx); err != nil || w != nil {
		t.Fatalf("%+v %v", w, err)
	}

	done := openLoot(t, h, "")
	h.send(opcode.SMSGLootResponse, gametest.LootResponse(wolfGUID, game.LootCorpse, 253,
		gametest.LootSlot{Slot: 0, Item: 2589, Count: 2, Display: 7},
		gametest.LootSlot{Slot: 1, Item: 118, Count: 1, SlotType: game.LootSlotRolling},
		gametest.LootSlot{Slot: 2, Item: 4540, Count: 1, Prop: 9}))
	r := <-done
	want := &LootWindow{GUID: wolfGUID, Type: "corpse", Money: 253, Items: []LootSlot{
		{Slot: 0, Item: 2589, Count: 2, DisplayID: 7, SlotType: "allow_loot"},
		{Slot: 1, Item: 118, Count: 1, SlotType: "roll_ongoing"},
		{Slot: 2, Item: 4540, Count: 1, RandomProperty: 9, SlotType: "allow_loot"}}}
	if r.err != nil || !reflect.DeepEqual(r.w, want) {
		t.Fatalf("%+v %v", r.w, r.err)
	}
	if w, _ := h.s.LootWindow(ctx); !reflect.DeepEqual(w, want) {
		t.Fatalf("window %+v", w)
	}

	// A slot under a running roll cannot be taken; an unknown slot is not there.
	one, two, nine := uint8(1), uint8(2), uint8(9)
	if _, err := h.s.TakeLoot(ctx, LootTake{Slot: &one}, time.Second); err == nil || !strings.Contains(err.Error(), "roll_ongoing") {
		t.Fatalf("rolling slot: %v", err)
	}
	if _, err := h.s.TakeLoot(ctx, LootTake{Slot: &nine}, time.Second); err == nil || !strings.Contains(err.Error(), "not in the loot window") {
		t.Fatalf("unknown slot: %v", err)
	}
	if _, err := h.s.TakeLoot(ctx, LootTake{}, time.Second); err == nil {
		t.Fatal("take nothing")
	}

	// One slot: the server removes it.
	taken := make(chan LootTaken, 1)
	go func() {
		r, err := h.s.TakeLoot(ctx, LootTake{Slot: &two}, 2*time.Second)
		if err != nil {
			t.Error(err)
		}
		taken <- r
	}()
	if p := h.expect(opcode.CMSGAutostoreLootItem); string(p.Body) != string(game.BuildAutoStoreLootItem(2)) {
		t.Fatalf("% X", p.Body)
	}
	h.send(opcode.SMSGLootRemoved, gametest.LootRemoved(2))
	if r := <-taken; !reflect.DeepEqual(r.Taken, []int{2}) || len(r.Left) != 0 || r.Window == nil || len(r.Window.Items) != 2 || r.Money != 0 {
		t.Fatalf("%+v", r)
	}

	// All: slot 0 and the money; slot 1 is skipped, so the window stays open.
	go func() {
		r, err := h.s.TakeLoot(ctx, LootTake{All: true}, 2*time.Second)
		if err != nil {
			t.Error(err)
		}
		taken <- r
	}()
	if p := h.expect(opcode.CMSGAutostoreLootItem); string(p.Body) != string(game.BuildAutoStoreLootItem(0)) {
		t.Fatalf("% X", p.Body)
	}
	if p := h.expect(opcode.CMSGLootMoney); len(p.Body) != 0 {
		t.Fatalf("% X", p.Body)
	}
	h.send(opcode.SMSGLootRemoved, gametest.LootRemoved(0))
	h.send(opcode.SMSGLootClearMoney, nil)
	r2 := <-taken
	if !reflect.DeepEqual(r2.Taken, []int{0}) || !reflect.DeepEqual(r2.Skipped, []int{1}) || r2.Money != 253 ||
		r2.Window == nil || len(r2.Window.Items) != 1 || r2.Window.Money != 0 {
		t.Fatalf("%+v", r2)
	}

	// Close: CMSG_LOOT_RELEASE, then the server's release response.
	closed := make(chan error, 1)
	go func() { closed <- h.s.CloseLoot(ctx, 2*time.Second) }()
	if p := h.expect(opcode.CMSGLootRelease); string(p.Body) != string(game.BuildLootRelease(wolfGUID)) {
		t.Fatalf("% X", p.Body)
	}
	h.send(opcode.SMSGLootReleaseResponse, gametest.LootRelease(wolfGUID))
	if err := <-closed; err != nil {
		t.Fatal(err)
	}
	if w, _ := h.s.LootWindow(ctx); w != nil {
		t.Fatalf("still open %+v", w)
	}
	h.send(opcode.SMSGLootMoneyNotify, []byte{0x10, 0x27, 0, 0})
	h.logout()
	l := h.s.Log()
	if o := ofKind(l, events.KindLootOpen); len(o) != 1 || o[0].SourceGUID != wolfGUID || o[0].Amount != 253 || len(o[0].Data["items"].([]map[string]any)) != 3 {
		t.Fatalf("open %+v", o)
	}
	if k := ofKind(l, events.KindLootTaken); len(k) != 2 || k[0].Data["item_entry"] != uint32(4540) || k[1].Data["slot"] != uint8(0) {
		t.Fatalf("taken %+v", k)
	}
	if m := ofKind(l, events.KindLootMoney); len(m) != 1 || m[0].Amount != 253 {
		t.Fatalf("money %+v", m)
	}
	if c := ofKind(l, events.KindLootClosed); len(c) != 1 {
		t.Fatalf("closed %+v", c)
	}
	if s := ofKind(l, events.KindLootMoneyShare); len(s) != 1 || s[0].Amount != 10000 {
		t.Fatalf("share %+v", s)
	}
}

// The window closes by itself once the last item and the money are gone, as the client does.
func TestLootClosesWhenEmpty(t *testing.T) {
	ctx := context.Background()
	h := lootRig(t)
	done := openLoot(t, h, "")
	h.send(opcode.SMSGLootResponse, gametest.LootResponse(wolfGUID, game.LootCorpse, 5, gametest.LootSlot{Item: 2589, Count: 1}))
	if r := <-done; r.err != nil {
		t.Fatal(r.err)
	}
	taken := make(chan LootTaken, 1)
	go func() {
		r, err := h.s.TakeLoot(ctx, LootTake{All: true}, 2*time.Second)
		if err != nil {
			t.Error(err)
		}
		taken <- r
	}()
	h.expect(opcode.CMSGAutostoreLootItem)
	h.expect(opcode.CMSGLootMoney)
	h.send(opcode.SMSGLootRemoved, gametest.LootRemoved(0))
	h.send(opcode.SMSGLootClearMoney, nil)
	if r := <-taken; !reflect.DeepEqual(r.Taken, []int{0}) || r.Window != nil {
		t.Fatalf("%+v", r)
	}
	if p := h.expect(opcode.CMSGLootRelease); string(p.Body) != string(game.BuildLootRelease(wolfGUID)) {
		t.Fatalf("% X", p.Body)
	}
	h.send(opcode.SMSGLootReleaseResponse, gametest.LootRelease(wolfGUID))
	waitFor(t, func() bool { w, _ := h.s.LootWindow(ctx); return w == nil })

	// A corpse with nothing on it opens and is released at once.
	done = openLoot(t, h, "")
	h.send(opcode.SMSGLootResponse, gametest.LootResponse(wolfGUID, game.LootCorpse, 0))
	h.send(opcode.SMSGLootReleaseResponse, gametest.LootRelease(wolfGUID)) // right behind it: the window is gone by the time the caller looks
	if r := <-done; r.err != nil || r.w == nil || len(r.w.Items) != 0 {
		t.Fatalf("%+v %v", r.w, r.err)
	}
	h.expect(opcode.CMSGLootRelease)
	h.logout()
}

// A slot the server does not remove (a full bag) is reported left; the wait ends at its limit.
func TestLootTakeLeftWhenBagsFull(t *testing.T) {
	ctx := context.Background()
	h := lootRig(t)
	done := openLoot(t, h, "")
	h.send(opcode.SMSGLootResponse, gametest.LootResponse(wolfGUID, game.LootCorpse, 0, gametest.LootSlot{Slot: 4, Item: 2589, Count: 1}))
	<-done
	slot := uint8(4)
	r, err := h.s.TakeLoot(ctx, LootTake{Slot: &slot}, 100*time.Millisecond)
	if err != nil || len(r.Taken) != 0 || !reflect.DeepEqual(r.Left, []int{4}) || r.Window == nil {
		t.Fatalf("%+v %v", r, err)
	}
	h.expect(opcode.CMSGAutostoreLootItem)
	h.logout()
}

func TestLootRefusedAndNotFound(t *testing.T) {
	ctx := context.Background()
	h := lootRig(t)
	done := openLoot(t, h, "")
	h.send(opcode.SMSGLootResponse, gametest.LootRefused(wolfGUID, 4))
	if r := <-done; r.err == nil || !strings.Contains(r.err.Error(), "too far away") || !strings.Contains(r.err.Error(), "too_far") {
		t.Fatalf("%v", r.err)
	}
	// The core answers a corpse it cannot open with a release.
	done = openLoot(t, h, "")
	h.send(opcode.SMSGLootReleaseResponse, gametest.LootRelease(wolfGUID))
	if r := <-done; r.err == nil || !strings.Contains(r.err.Error(), "closed the loot at once") {
		t.Fatalf("%v", r.err)
	}
	// No answer at all.
	if _, err := h.s.OpenLoot(ctx, wolfGUID, "", 100*time.Millisecond); err == nil || !strings.Contains(err.Error(), "within") {
		t.Fatalf("%v", err)
	}
	h.expect(opcode.CMSGLoot)
	if _, err := h.s.OpenLoot(ctx, 0, "", time.Second); err == nil || !strings.Contains(err.Error(), "guid or a name") {
		t.Fatalf("%v", err)
	}
	if _, err := h.s.OpenLoot(ctx, 0, "dragon", time.Second); err == nil || !strings.Contains(err.Error(), "no lootable dead unit") {
		t.Fatalf("%v", err)
	}
	h.logout()
	if e := ofKind(h.s.Log(), events.KindLootError); len(e) != 1 || e[0].Data["reason"] != "too_far" {
		t.Fatalf("%+v", e)
	}
}

// loot by name picks the nearest dead unit, not a living one of that name.
func TestLootByName(t *testing.T) {
	h := lootRig(t)
	if _, err := h.s.OpenLoot(context.Background(), 0, "young wolf", 50*time.Millisecond); err == nil || !strings.Contains(err.Error(), "no lootable dead unit") {
		t.Fatalf("a living wolf: %v", err)
	}
	h.send(opcode.SMSGUpdateObject, valuesUpdate(wolfGUID, map[uint16]uint32{objects.UnitHealth: 0}))
	waitFor(t, func() bool { us := h.s.Units(objects.Filter{Name: "young wolf"}); return len(us) == 1 && us[0].Dead })
	// Dead but nothing to loot (no UNIT_DYNFLAG_LOOTABLE): not picked.
	if _, err := h.s.OpenLoot(context.Background(), 0, "young wolf", 50*time.Millisecond); err == nil || !strings.Contains(err.Error(), "no lootable dead unit") {
		t.Fatalf("an empty corpse: %v", err)
	}
	h.send(opcode.SMSGUpdateObject, valuesUpdate(wolfGUID, map[uint16]uint32{objects.UnitDynamicFlags: objects.DynFlagLootable}))
	waitFor(t, func() bool { return h.s.Store().Field(wolfGUID, objects.UnitDynamicFlags) == 1 })
	done := make(chan openResult, 1)
	go func() {
		w, err := h.s.OpenLoot(context.Background(), 0, "young wolf", 2*time.Second)
		done <- openResult{w, err}
	}()
	if p := h.expect(opcode.CMSGLoot); string(p.Body) != string(game.BuildLoot(wolfGUID)) {
		t.Fatalf("% X", p.Body)
	}
	h.send(opcode.SMSGLootResponse, gametest.LootResponse(wolfGUID, game.LootCorpse, 0))
	if r := <-done; r.err != nil || r.w.GUID != wolfGUID {
		t.Fatalf("%+v %v", r.w, r.err)
	}
	h.expect(opcode.CMSGLootRelease)
	h.logout()
}

// Opening a corpse while another window is open: the core releases the old one first, and
// that release is not the answer to the new open.
func TestLootReopenSkipsTheOldRelease(t *testing.T) {
	ctx := context.Background()
	h := lootRig(t)
	items := gametest.LootSlot{Slot: 0, Item: 2589, Count: 1}
	done := openLoot(t, h, "")
	h.send(opcode.SMSGLootResponse, gametest.LootResponse(wolfGUID, game.LootCorpse, 0, items))
	if r := <-done; r.err != nil {
		t.Fatal(r.err)
	}
	done = openLoot(t, h, "")
	h.send(opcode.SMSGLootReleaseResponse, gametest.LootRelease(wolfGUID))
	select {
	case r := <-done:
		t.Fatalf("the old release answered the open: %+v %v", r.w, r.err)
	case <-time.After(100 * time.Millisecond):
	}
	h.send(opcode.SMSGLootResponse, gametest.LootResponse(wolfGUID, game.LootCorpse, 7, items))
	if r := <-done; r.err != nil || r.w.Money != 7 {
		t.Fatalf("%+v %v", r.w, r.err)
	}
	if w, _ := h.s.LootWindow(ctx); w == nil || w.Money != 7 {
		t.Fatalf("%+v", w)
	}
	h.logout()
}
