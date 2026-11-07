package session

// The practice GM hands' session-level safety: the max-health ceiling of gm_set_hp, the dead-only rule of gm_respawn,
// and the authorization gate all five share with GMKill. Every refusal must send no packet at all.

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/pjerra/turtle-agent/internal/objects"
	"github.com/pjerra/turtle-agent/internal/opcode"
	"github.com/pjerra/turtle-agent/internal/packet"
)

// practiceCreature is a creature guid (0xF130...), the only shape the hands that select take.
func practiceCreature() packet.GUID {
	return packet.GUID(uint64(packet.HighUnit)<<48 | 12118<<24 | 4242)
}

// noGMOut checks that no selection or chat packet went out after a refusal.
func noGMOut(t *testing.T, h *harness) {
	t.Helper()
	for {
		p, err := h.srv.Next(200 * time.Millisecond)
		if err != nil {
			return
		}
		if p.Op == opcode.CMSGSetSelection || p.Op == opcode.CMSGMessagechat {
			t.Fatalf("a packet went out: %v (% X)", p.Op, p.Body)
		}
	}
}

// TestGMSetHPRefusesOverMax: restoring a boss is a full heal. A value above the client's MaxHealth (or 0) is refused
// before the selection, so no line can lower a boss's maximum.
func TestGMSetHPRefusesOverMax(t *testing.T) {
	ctx := context.Background()
	h := start(t, Options{AllowGMKill: true})
	h.enterWorld()
	h.send(opcode.SMSGUpdateObject, unitCreate(me.GUID, objects.TypePlayer, me.X, me.Y, me.Z,
		map[uint16]uint32{objects.UnitHealth: 800, objects.UnitMaxHealth: 800}))
	h.waitSelfCreated(ctx)
	g := practiceCreature()
	h.send(opcode.SMSGUpdateObject, unitCreate(g, objects.TypeUnit, me.X+5, me.Y, me.Z,
		map[uint16]uint32{objects.FieldEntry: 14834, objects.UnitHealth: 100, objects.UnitMaxHealth: 100}))
	if _, err := h.s.Unit(g, ""); err != nil {
		t.Fatalf("the creature is not in view: %v", err)
	}
	for _, hp := range []uint32{0, 101, 1000} {
		if err := h.s.GMSetHP(ctx, g, hp); err == nil {
			t.Fatalf("GMSetHP accepted %d over the unit's max 100", hp)
		} else if !strings.Contains(err.Error(), "max 100") {
			t.Fatalf("the refusal does not name the max: %v", err)
		}
	}
	noGMOut(t, h)
}

// TestGMRespawnRefusesLiveUnit: `.respawn` is only ever the selected-dead form. A live unit is refused, so the call
// can never fall through to the unselected form that respawns every dead creature in range.
func TestGMRespawnRefusesLiveUnit(t *testing.T) {
	ctx := context.Background()
	h := start(t, Options{AllowGMKill: true})
	h.enterWorld()
	h.send(opcode.SMSGUpdateObject, unitCreate(me.GUID, objects.TypePlayer, me.X, me.Y, me.Z,
		map[uint16]uint32{objects.UnitHealth: 800, objects.UnitMaxHealth: 800}))
	h.waitSelfCreated(ctx)
	g := practiceCreature()
	h.send(opcode.SMSGUpdateObject, unitCreate(g, objects.TypeUnit, me.X+5, me.Y, me.Z,
		map[uint16]uint32{objects.FieldEntry: 11357, objects.UnitHealth: 50, objects.UnitMaxHealth: 50}))
	if err := h.s.GMRespawn(ctx, g); err == nil || !strings.Contains(err.Error(), "is alive") {
		t.Fatalf("GMRespawn on a live unit: %v, want the alive refusal", err)
	}
	noGMOut(t, h)
}

// TestPracticeHandsRefusedWithoutAuthorize: an agent without AllowGMKill (it is set only from gmKillAuthorize's
// dialled-address check) refuses every practice hand and its own GMKill before selecting, reading or sending.
func TestPracticeHandsRefusedWithoutAuthorize(t *testing.T) {
	ctx := context.Background()
	h := start(t, Options{})
	h.enterWorld()
	g := practiceCreature()
	for name, err := range map[string]error{
		"GMKill":    h.s.GMKill(ctx, g),
		"GMEvade":   h.s.GMEvade(ctx, g),
		"GMSetHP":   h.s.GMSetHP(ctx, g, 100),
		"GMRespawn": h.s.GMRespawn(ctx, g),
		"GMRevive":  h.s.GMRevive(ctx, "Bottle"),
	} {
		if !errors.Is(err, ErrGMRefused) && !errors.Is(err, ErrGMKillRefused) {
			t.Fatalf("%s: %v, want the authorization refusal", name, err)
		}
	}
	noGMOut(t, h)
}

// GMRevive takes a character name and nothing else.
func TestGMReviveNameShape(t *testing.T) {
	ctx := context.Background()
	h := start(t, Options{AllowGMKill: true})
	h.enterWorld()
	for _, name := range []string{"", "B", "Bot 09", "Bottle;.server shutdown", "Bot_09"} {
		if err := h.s.GMRevive(ctx, name); err == nil {
			t.Fatalf("GMRevive accepted %q", name)
		}
	}
}
