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

func castFailCode(t *testing.T, name string) int {
	t.Helper()
	for c := 0; c < 256; c++ {
		if game.CastFailName(uint8(c)) == name {
			return c
		}
	}
	t.Fatalf("no cast failure named %s", name)
	return 0
}

func bookWith(t *testing.T, spellIDs ...uint32) *harness {
	t.Helper()
	h := start(t, Options{})
	h.enterWorld()
	h.send(opcode.SMSGInitialSpells, gametest.InitialSpells(spellIDs, nil))
	waitFor(t, func() bool { return h.s.spellState.Loaded() })
	chestNearby(h, 3)
	return h
}

// With the spell book in, only the Opening spells the character knows are cast: no 6477 when the book lacks it.
func TestOpenObjectCastsAKnownOpeningSpell(t *testing.T) {
	h := bookWith(t, 6603, 3365)
	done := make(chan error, 1)
	go func() { _, err := h.s.OpenObject(context.Background(), chestGUID, 0, 2*time.Second); done <- err }()
	if p := h.expect(opcode.CMSGCastSpell); string(p.Body) != string(game.BuildCastSpellOnObject(3365, chestGUID)) {
		t.Fatalf("cast % X", p.Body)
	}
	h.send(opcode.SMSGLootResponse, gametest.LootResponse(chestGUID, game.LootPickpocket, 0, gametest.LootSlot{Slot: 0, Item: 7740, Count: 1}))
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

// A spell that is not in the book is refused here, at once, instead of being dropped by the core after the whole wait; so is a book with
// no Opening spell at all.
func TestOpenObjectChecksTheSpellBook(t *testing.T) {
	h := bookWith(t, 6603)
	if _, err := h.s.OpenObject(context.Background(), chestGUID, 0, time.Second); err == nil || !strings.Contains(err.Error(), "knows none of the Opening spells") {
		t.Fatalf("%v", err)
	}
	if _, err := h.s.OpenObject(context.Background(), chestGUID, 6477, time.Second); err == nil || !strings.Contains(err.Error(), "spell 6477 is not in the spell book") {
		t.Fatalf("%v", err)
	}
	h.noPacket(opcode.CMSGCastSpell)
}

// A lock the first spell cannot open (bad targets) is tried with the next Opening spell the character knows; when all are refused the error
// names each and the reason by name.
func TestOpenObjectTriesTheOtherOpeningSpells(t *testing.T) {
	bad := castFailCode(t, "bad_targets")
	h := bookWith(t, 6477, 3365, 6247)
	done := make(chan error, 1)
	go func() { _, err := h.s.OpenObject(context.Background(), chestGUID, 0, 2*time.Second); done <- err }()
	for _, sp := range []uint32{6477, 3365} {
		if p := h.expect(opcode.CMSGCastSpell); string(p.Body) != string(game.BuildCastSpellOnObject(sp, chestGUID)) {
			t.Fatalf("cast % X, want spell %d", p.Body, sp)
		}
		h.send(opcode.SMSGCastResult, gametest.CastResult(sp, bad))
	}
	if p := h.expect(opcode.CMSGCastSpell); string(p.Body) != string(game.BuildCastSpellOnObject(6247, chestGUID)) {
		t.Fatalf("cast % X", p.Body)
	}
	h.send(opcode.SMSGLootResponse, gametest.LootResponse(chestGUID, game.LootPickpocket, 0, gametest.LootSlot{Slot: 0, Item: 7740, Count: 1}))
	if err := <-done; err != nil {
		t.Fatalf("the third spell opens it: %v", err)
	}

	// All refused.
	go func() { _, err := h.s.OpenObject(context.Background(), chestGUID, 0, 2*time.Second); done <- err }()
	for _, sp := range []uint32{6477, 3365, 6247} {
		h.expect(opcode.CMSGCastSpell)
		h.send(opcode.SMSGCastResult, gametest.CastResult(sp, bad))
	}
	err := <-done
	if err == nil || !strings.Contains(err.Error(), "bad_targets") || !strings.Contains(err.Error(), "spell 6477: bad_targets") || !strings.Contains(err.Error(), "SpellCastResult") {
		t.Fatalf("%v", err)
	}
	// Another refusal (out of range) is not retried with another spell.
	go func() { _, err := h.s.OpenObject(context.Background(), chestGUID, 0, 2*time.Second); done <- err }()
	h.expect(opcode.CMSGCastSpell)
	h.send(opcode.SMSGCastResult, gametest.CastResult(6477, castFailCode(t, "out_of_range")))
	if err := <-done; err == nil || !strings.Contains(err.Error(), "out_of_range") {
		t.Fatalf("%v", err)
	}
	h.noPacket(opcode.CMSGCastSpell)
}
