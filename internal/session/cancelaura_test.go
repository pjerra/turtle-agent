package session

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/pjerra/turtle-agent/internal/game"
	"github.com/pjerra/turtle-agent/internal/opcode"
	"github.com/pjerra/turtle-agent/internal/packet"
)

// A druid leaves its shapeshift form with CMSG_CANCEL_AURA, as a right-click on the buff does.
func TestCancelAura(t *testing.T) {
	ctx := context.Background()
	h := start(t, Options{})
	h.enterWorld()
	if err := h.s.CancelAura(ctx, 0); err == nil || !strings.Contains(err.Error(), "needs a spell id") {
		t.Fatalf("no spell: %v", err)
	}
	if err := h.s.CancelAura(ctx, 768); err != nil {
		t.Fatal(err)
	}
	if p := h.expect(opcode.CMSGCancelAura); string(p.Body) != string(game.BuildCancelAura(768)) {
		t.Fatalf("% X", p.Body)
	}
}

// A weapon imbue is a cast at the weapon in the main hand (inventory slot 15), named by its guid.
func TestCastOnMainHand(t *testing.T) {
	ctx := context.Background()
	h := start(t, Options{})
	h.enterWorld()
	if err := h.s.CastOnMainHand(ctx, 0); err == nil || !strings.Contains(err.Error(), "needs a spell id") {
		t.Fatalf("no spell: %v", err)
	}
	if err := h.s.CastOnMainHand(ctx, 8232); err == nil || !strings.Contains(err.Error(), "no weapon in the main hand") {
		t.Fatalf("an empty hand: %v", err)
	}
	if p, err := h.srv.Expect(opcode.CMSGCastSpell, 100*time.Millisecond); err == nil {
		t.Fatalf("sent % X", p.Body)
	}
	h.send(opcode.SMSGUpdateObject, ownInventory(map[uint8]packet.GUID{15: swordGUID, 30: potionGUID},
		map[packet.GUID][2]uint32{swordGUID: {2589, 1}, potionGUID: {118, 5}}))
	waitFor(t, func() bool { _, ok := h.s.store.ItemAt(h.s.selfGUID(), 255, 15); return ok })
	if err := h.s.CastOnMainHand(ctx, 8232); err != nil {
		t.Fatal(err)
	}
	if p := h.expect(opcode.CMSGCastSpell); string(p.Body) != string(game.BuildCastSpellOnItem(8232, swordGUID)) {
		t.Fatalf("% X", p.Body)
	}
}
