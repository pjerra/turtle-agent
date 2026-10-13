package session

import (
	"context"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/pjerra/turtle-agent/internal/game"
	"github.com/pjerra/turtle-agent/internal/game/gametest"
	"github.com/pjerra/turtle-agent/internal/objects"
	"github.com/pjerra/turtle-agent/internal/opcode"
	"github.com/pjerra/turtle-agent/internal/packet"
)

const chestGUID packet.GUID = 0xF11001E1C1000001 // Baelog's Chest, entry 123329

func chestNearby(h *harness, dist float32) {
	h.t.Helper()
	h.send(opcode.SMSGUpdateObject, goCreate(chestGUID, me.X+dist, me.Y, me.Z, map[uint16]uint32{
		objects.FieldEntry: 123329, objects.GOTypeID: uint32(objects.GOTypeChest), objects.GOFlags: 0, objects.GOState: objects.GOStateReady}))
	waitFor(h.t, func() bool { return len(h.s.GameObjects(objects.GOFilter{Entry: 123329})) == 1 })
}

// A chest is opened by an Opening cast at it; the window it answers with is looted like a corpse's, and the window closes when empty.
func TestLootObjectCastsOpeningThenLoots(t *testing.T) {
	ctx := context.Background()
	h := start(t, Options{})
	h.enterWorld()
	chestNearby(h, 3)

	type res struct {
		r   LootedObject
		err error
	}
	done := make(chan res, 1)
	go func() {
		r, err := h.s.LootObject(ctx, chestGUID, 0, 2*time.Second, 2*time.Second)
		done <- res{r, err}
	}()
	// The cast is spell 6477, target mask 0x0800, the chest's guid packed (the layout is pinned in internal/game).
	if p := h.expect(opcode.CMSGCastSpell); string(p.Body) != string(game.BuildCastSpellOnObject(OpeningSpell, chestGUID)) {
		t.Fatalf("cast % X", p.Body)
	}
	// The server answers the finished cast with the window; a chest's loot goes out as the pickpocket type (Player.cpp:9614-9637).
	h.send(opcode.SMSGLootResponse, gametest.LootResponse(chestGUID, game.LootPickpocket, 0,
		gametest.LootSlot{Slot: 0, Item: 7740, Count: 1, Display: 7366, SlotType: game.LootSlotAllow}))
	if p := h.expect(opcode.CMSGAutostoreLootItem); string(p.Body) != string(game.BuildAutoStoreLootItem(0)) {
		t.Fatalf("take % X", p.Body)
	}
	h.send(opcode.SMSGLootRemoved, gametest.LootRemoved(0))
	// The emptied window is released at once, as the client closes the frame.
	if p := h.expect(opcode.CMSGLootRelease); string(p.Body) != string(game.BuildLootRelease(chestGUID)) {
		t.Fatalf("release % X", p.Body)
	}
	h.send(opcode.SMSGLootReleaseResponse, gametest.LootRelease(chestGUID))
	r := <-done
	if r.err != nil || len(r.r.Taken) != 1 || r.r.Taken[0].Item != 7740 || len(r.r.Left) != 0 || r.r.Window.GUID != chestGUID {
		t.Fatalf("%+v %v", r.r, r.err)
	}
	if w, _ := h.s.LootWindow(ctx); w != nil {
		t.Fatalf("still open %+v", w)
	}
}

// OpenObject alone leaves the window open for loot_take, and a spell id of the caller's replaces the default.
func TestOpenObjectOtherSpellKeepsTheWindow(t *testing.T) {
	ctx := context.Background()
	h := start(t, Options{})
	h.enterWorld()
	chestNearby(h, 2)
	type res struct {
		w   *LootWindow
		err error
	}
	done := make(chan res, 1)
	go func() {
		w, err := h.s.OpenObject(ctx, chestGUID, 3365, 2*time.Second)
		done <- res{w, err}
	}()
	if p := h.expect(opcode.CMSGCastSpell); string(p.Body) != string(game.BuildCastSpellOnObject(3365, chestGUID)) {
		t.Fatalf("cast % X", p.Body)
	}
	h.send(opcode.SMSGLootResponse, gametest.LootResponse(chestGUID, game.LootPickpocket, 12,
		gametest.LootSlot{Slot: 0, Item: 7740, Count: 1}))
	r := <-done
	want := &LootWindow{GUID: chestGUID, Type: "pickpocket", Money: 12, Items: []LootSlot{{Slot: 0, Item: 7740, Count: 1, SlotType: "allow_loot"}}}
	if r.err != nil || !reflect.DeepEqual(r.w, want) {
		t.Fatalf("%+v %v", r.w, r.err)
	}
	if w, _ := h.s.LootWindow(ctx); !reflect.DeepEqual(w, want) {
		t.Fatalf("window %+v", w)
	}
}

// A failed cast ends the wait with the reason, a refusal is its own error, and a server that says nothing runs out the wait.
func TestOpenObjectFailures(t *testing.T) {
	ctx := context.Background()
	h := start(t, Options{})
	h.enterWorld()

	if _, err := h.s.OpenObject(ctx, 0, 0, time.Second); err == nil || !strings.Contains(err.Error(), "needs a guid") {
		t.Fatalf("no guid: %v", err)
	}
	if _, err := h.s.OpenObject(ctx, chestGUID, 0, time.Second); err == nil || !strings.Contains(err.Error(), "not in view") {
		t.Fatalf("not in view: %v", err)
	}
	h.send(opcode.SMSGUpdateObject, goCreate(doorGUID, me.X+3, me.Y, me.Z, map[uint16]uint32{
		objects.FieldEntry: 124371, objects.GOTypeID: uint32(objects.GOTypeDoor), objects.GOFlags: 32, objects.GOState: objects.GOStateReady}))
	waitFor(t, func() bool { return len(h.s.GameObjects(objects.GOFilter{Entry: 124371})) == 1 })
	if _, err := h.s.OpenObject(ctx, doorGUID, 0, time.Second); err == nil || !strings.Contains(err.Error(), "not a chest") {
		t.Fatalf("a door: %v", err)
	}
	h.noPacket(opcode.CMSGCastSpell)

	chestNearby(h, 3)
	run := func(wait time.Duration) chan error {
		c := make(chan error, 1)
		go func() { _, err := h.s.OpenObject(ctx, chestGUID, 0, wait); c <- err }()
		h.expect(opcode.CMSGCastSpell)
		return c
	}
	// SPELL_FAILED_LOW_CASTLEVEL (the lock asks for more skill): SMSG_CAST_RESULT with the failure.
	c := run(2 * time.Second)
	h.send(opcode.SMSGCastResult, gametest.CastResult(OpeningSpell, 0x2B))
	if err := <-c; err == nil || !strings.Contains(err.Error(), "the cast failed") {
		t.Fatalf("cast failure: %v", err)
	}
	// A loot refusal.
	c = run(2 * time.Second)
	h.send(opcode.SMSGLootResponse, gametest.LootRefused(chestGUID, 1))
	if err := <-c; err == nil || !strings.Contains(err.Error(), "loot refused") {
		t.Fatalf("refusal: %v", err)
	}
	// Silence (an object the core ignores).
	if err := <-run(300 * time.Millisecond); err == nil || !strings.Contains(err.Error(), "no loot window") {
		t.Fatalf("silence: %v", err)
	}
}

func TestTeamOfRace(t *testing.T) {
	for race, want := range map[uint8]string{1: "alliance", 3: "alliance", 4: "alliance", 7: "alliance", 10: "alliance",
		2: "horde", 5: "horde", 6: "horde", 8: "horde", 9: "horde", 0: "", 11: ""} {
		if got := Team(race); got != want {
			t.Errorf("race %d: %q, want %q", race, got, want)
		}
	}
}
