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

// waitUnit polls the session until it holds the creature g: the create block is applied on the owner goroutine, and a
// read straight after h.send races it.
func (h *harness) waitUnit(ctx context.Context, g packet.GUID) objects.Unit {
	h.t.Helper()
	for deadline := time.Now().Add(5 * time.Second); ; time.Sleep(5 * time.Millisecond) {
		u, err := h.s.Unit(g, "")
		if err == nil {
			return u
		}
		if time.Now().After(deadline) {
			h.t.Fatalf("the session did not hold unit %s within 5s: %v", g, err)
		}
	}
}

// TestGMSetHPRestoresOwnMax: restoring a boss is a full heal to its OWN MaxHealth. The hand takes no health argument,
// reads the unit the client holds, and sends `.modify hp <max> <max>`: nothing can lower the unit's maximum.
func TestGMSetHPRestoresOwnMax(t *testing.T) {
	ctx := context.Background()
	h := start(t, Options{AllowGMKill: true})
	h.enterWorld()
	h.send(opcode.SMSGUpdateObject, unitCreate(me.GUID, objects.TypePlayer, me.X, me.Y, me.Z,
		map[uint16]uint32{objects.UnitHealth: 800, objects.UnitMaxHealth: 800}))
	h.waitSelfCreated(ctx)
	g := practiceCreature()
	// A hurt boss: 50 of 100.
	h.send(opcode.SMSGUpdateObject, unitCreate(g, objects.TypeUnit, me.X+5, me.Y, me.Z,
		map[uint16]uint32{objects.FieldEntry: 14834, objects.UnitHealth: 50, objects.UnitMaxHealth: 100}))
	u := h.waitUnit(ctx, g)
	if u.MaxHealth != 100 || u.Health != 50 {
		t.Fatalf("the creature reads %d/%d, want 50/100", u.Health, u.MaxHealth)
	}
	line := make(chan string, 1)
	stop := make(chan struct{})
	done := make(chan struct{})
	go func() {
		defer close(done)
		inForce := packet.GUID(0)
		for {
			select {
			case <-stop:
				return
			default:
			}
			p, err := h.srv.Next(50 * time.Millisecond)
			if err != nil {
				continue
			}
			switch p.Op {
			case opcode.CMSGSetSelection:
				sg := packet.NewReader(p.Body).GUID()
				if sg != inForce {
					inForce = sg
					h.send(opcode.SMSGUpdateObject, targetEcho(sg))
				}
			case opcode.CMSGMessagechat:
				line <- string(p.Body)
			}
		}
	}()
	if err := h.s.GMSetHP(ctx, g); err != nil {
		close(stop)
		<-done
		t.Fatal(err)
	}
	var text string
	select {
	case text = <-line:
	case <-time.After(5 * time.Second):
		close(stop)
		<-done
		t.Fatal("the server did not read the .modify hp line within 5s")
	}
	close(stop)
	<-done
	if !strings.Contains(text, ".modify hp 100 100") {
		t.Fatalf("the line %q does not restore the unit's own max 100 for both values", text)
	}
}

// TestGMSetHPRefusesUnheldUnit: a unit the client does not hold (or one with no max) is refused before any selection:
// the max the line would carry is never guessed.
func TestGMSetHPRefusesUnheldUnit(t *testing.T) {
	ctx := context.Background()
	h := start(t, Options{AllowGMKill: true})
	h.enterWorld()
	h.send(opcode.SMSGUpdateObject, unitCreate(me.GUID, objects.TypePlayer, me.X, me.Y, me.Z,
		map[uint16]uint32{objects.UnitHealth: 800, objects.UnitMaxHealth: 800}))
	h.waitSelfCreated(ctx)
	g := practiceCreature()
	if err := h.s.GMSetHP(ctx, g); err == nil {
		t.Fatal("GMSetHP accepted a unit the client does not hold")
	}
	h.send(opcode.SMSGUpdateObject, unitCreate(g, objects.TypeUnit, me.X+5, me.Y, me.Z,
		map[uint16]uint32{objects.FieldEntry: 14834, objects.UnitHealth: 0, objects.UnitMaxHealth: 0}))
	h.waitUnit(ctx, g)
	if err := h.s.GMSetHP(ctx, g); err == nil || !strings.Contains(err.Error(), "no max health") {
		t.Fatalf("GMSetHP on a unit with no max: %v", err)
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
	h.waitUnit(ctx, g)
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
		"GMSetHP":   h.s.GMSetHP(ctx, g),
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
