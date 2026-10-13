package session

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/pjerra/turtle-agent/internal/events"
	"github.com/pjerra/turtle-agent/internal/game"
	"github.com/pjerra/turtle-agent/internal/game/gametest"
	"github.com/pjerra/turtle-agent/internal/objects"
	"github.com/pjerra/turtle-agent/internal/opcode"
	"github.com/pjerra/turtle-agent/internal/packet"
)

// The pet: the summon field of the player, the pet's unit row, SMSG_PET_SPELLS and the orders (internal/game/pet.go; core cites there).

const (
	petGUID  = packet.GUID(0xF140000000000007)
	spGrowl  = 2649 // Growl rank 1
	spBite   = 17253
	spCower  = 1742
	petEntry = 17252 // a creature entry; the name query is never answered
)

func petRig(t *testing.T) *harness {
	t.Helper()
	h := start(t, Options{NamesWait: 30 * time.Millisecond})
	h.enterWorld()
	h.send(opcode.SMSGUpdateObject, selfWith(nil))
	waitFor(t, func() bool { return h.s.Status().MaxHealth == 100 })
	return h
}

// petVals are the unit fields of a hunter's pet: focus (power type 2), happiness in POWER5 (Pet.cpp:429-430), loyalty in byte 1 of BYTES_1.
func petVals(hp, happiness uint32) map[uint16]uint32 {
	return map[uint16]uint32{objects.FieldEntry: petEntry, objects.UnitHealth: hp, objects.UnitMaxHealth: 400, objects.UnitLevel: 20,
		objects.UnitBytes0: 2 << 24, objects.UnitPower1 + 2: 90, objects.UnitMaxPower1 + 2: 100,
		objects.UnitPower5: happiness, objects.UnitMaxPower5: 1000000, objects.UnitBytes1: 3 << 8, objects.UnitFlags: objects.UnitFlagPlayerControlled}
}

func summonField(g packet.GUID) map[uint16]uint32 {
	return map[uint16]uint32{objects.UnitSummon: uint32(g), objects.UnitSummon + 1: uint32(g >> 32)}
}

// callPet creates the pet's object, sends its bar (the core sends the bar with the pet; the field comes with the next update block) and
// then the player's summon field.
func callPet(h *harness, vals map[uint16]uint32) {
	h.t.Helper()
	h.send(opcode.SMSGUpdateObject, unitCreate(petGUID, objects.TypeUnit, me.X+3, me.Y, me.Z, vals))
	bar := gametest.PetBar(game.PetBarSpell{Spell: spGrowl, Autocast: true}, game.PetBarSpell{Spell: spBite, Autocast: true})
	h.send(opcode.SMSGPetSpells, gametest.PetSpells(petGUID, 1, 1, false, bar, nil))
	h.send(opcode.SMSGUpdateObject, valuesUpdate(me.GUID, summonField(petGUID)))
	waitFor(h.t, func() bool { return h.s.Pet().Present })
}

func TestThePetIsTheUnitTheSummonFieldNames(t *testing.T) {
	h := petRig(t)
	if p := h.s.Pet(); p.Present || p.Bar {
		t.Fatalf("no pet yet: %+v", p)
	}
	callPet(h, petVals(400, 500000))
	waitFor(t, func() bool { return len(ofKind(h.s.Log(), events.KindPetSummoned)) == 1 })
	p := h.s.Pet()
	if !p.Present || p.GUID != petGUID || p.Entry != petEntry || p.Dead || p.Health != 400 || p.MaxHealth != 400 || p.Level != 20 {
		t.Fatalf("%+v", p)
	}
	if p.PowerType != 2 || p.Power != 90 || p.MaxPower != 100 {
		t.Fatalf("a hunter's pet plays on focus: %+v", p)
	}
	if p.Happiness != 500000 || p.HappinessState != game.PetContent || p.Loyalty != 3 {
		t.Fatalf("happiness is POWER5, loyalty byte 1 of BYTES_1: %+v", p)
	}
	if !p.Bar || !p.Enabled || p.React != "defensive" || p.Command != "follow" {
		t.Fatalf("%+v", p)
	}
	if len(p.Spells) != 2 || p.Spells[0].SpellID != spGrowl || p.Spells[0].Slot != 3 || !p.Spells[0].Autocast || p.Spells[0].Name != "Growl" || p.Spells[1].Slot != 4 {
		t.Fatalf("%+v", p.Spells)
	}
	ev := ofKind(h.s.Log(), events.KindPetSummoned)
	if ev[0].SourceGUID != petGUID || ev[0].Data["entry"] != uint32(petEntry) {
		t.Fatalf("%+v", ev)
	}
	// The pet is also the unit the store names as the player's: the owner lookup the party filter uses and the pet lookup agree.
	if h.s.store.PetOf(me.GUID) != petGUID {
		t.Fatal("PetOf")
	}
	// Another player's pet is not ours.
	other := packet.GUID(0xF140000000000009)
	h.send(opcode.SMSGUpdateObject, unitCreate(other, objects.TypeUnit, me.X+5, me.Y, me.Z, petVals(300, 0)))
	if p := h.s.Pet(); p.GUID != petGUID {
		t.Fatalf("%+v", p)
	}
}

func TestPetDeathReviveAndDismissAreEvents(t *testing.T) {
	h := petRig(t)
	callPet(h, petVals(400, 900000))
	h.send(opcode.SMSGUpdateObject, valuesUpdate(petGUID, map[uint16]uint32{objects.UnitHealth: 0}))
	waitFor(t, func() bool { return len(ofKind(h.s.Log(), events.KindPetDied)) == 1 })
	if p := h.s.Pet(); !p.Dead || !p.Present {
		t.Fatalf("a dead pet stays until it is revived or gone: %+v", p)
	}
	h.send(opcode.SMSGUpdateObject, valuesUpdate(petGUID, map[uint16]uint32{objects.UnitHealth: 200}))
	waitFor(t, func() bool { return len(ofKind(h.s.Log(), events.KindPetRevived)) == 1 })
	if p := h.s.Pet(); p.Dead {
		t.Fatalf("%+v", p)
	}
	// Unit::SetPet(nullptr) clears the field; the core also sends the empty bar (Player::RemovePetActionBar).
	h.send(opcode.SMSGPetSpells, gametest.PetSpellsCleared())
	h.send(opcode.SMSGUpdateObject, valuesUpdate(me.GUID, summonField(0)))
	waitFor(t, func() bool { return len(ofKind(h.s.Log(), events.KindPetDismissed)) == 1 })
	if p := h.s.Pet(); p.Present || p.Bar {
		t.Fatalf("%+v", p)
	}
	d := ofKind(h.s.Log(), events.KindPetDismissed)[0]
	if d.SourceGUID != petGUID || d.Data["was_dead"] != false {
		t.Fatalf("%+v", d)
	}
	if n := len(ofKind(h.s.Log(), events.KindPetSummoned)); n != 1 {
		t.Fatalf("%d summoned events", n)
	}
	// The same pet comes back dead (a hunter calls a pet that died): summoned and died together.
	h.send(opcode.SMSGUpdateObject, valuesUpdate(petGUID, map[uint16]uint32{objects.UnitHealth: 0}))
	h.send(opcode.SMSGUpdateObject, valuesUpdate(me.GUID, summonField(petGUID)))
	waitFor(t, func() bool { return len(ofKind(h.s.Log(), events.KindPetDied)) == 2 })
	if n := len(ofKind(h.s.Log(), events.KindPetSummoned)); n != 2 {
		t.Fatalf("%d summoned events", n)
	}
	// A destroyed pet object with the field still set: the unit is gone from the store, the pet reads as absent.
	h.send(opcode.SMSGDestroyObject, packet.NewWriter().GUID(petGUID).Bytes())
	waitFor(t, func() bool { return !h.s.Pet().Present })
}

func TestPetOrdersAreTheBarsButtons(t *testing.T) {
	ctx := context.Background()
	h := petRig(t)
	callPet(h, petVals(400, 900000))
	order := func(o PetOrder) (packet.GUID, uint32, packet.GUID) {
		t.Helper()
		done := asyncErr(func() error { return h.s.PetAction(ctx, o) })
		p := h.expect(opcode.CMSGPetAction)
		finish(t, done)
		if len(p.Body) != 20 {
			t.Fatalf("CMSG_PET_ACTION is 20 bytes (PetHandler.cpp:35-46): % X", p.Body)
		}
		r := packet.NewReader(p.Body)
		return r.GUID(), r.U32(), r.GUID()
	}
	mob := packet.GUID(0xF130000000001234)
	if g, b, tg := order(PetOrder{Action: "attack", Target: mob}); g != petGUID || b != 0x07000002 || tg != mob {
		t.Fatalf("attack: %v %#x %v", g, b, tg)
	}
	// Without a target the player's own target is the pet's.
	h.send(opcode.SMSGUpdateObject, unitCreate(mob, objects.TypeUnit, me.X+9, me.Y, me.Z, map[uint16]uint32{objects.UnitHealth: 50, objects.UnitMaxHealth: 50}))
	waitFor(t, func() bool { return len(h.s.Units(objects.Filter{})) == 2 })
	tdone := asyncErr(func() error { _, err := h.s.Target(ctx, mob, ""); return err })
	h.expect(opcode.CMSGSetSelection)
	finish(t, tdone)
	if _, b, tg := order(PetOrder{Action: "attack"}); b != 0x07000002 || tg != mob {
		t.Fatalf("attack the selected unit: %#x %v", b, tg)
	}
	if _, b, _ := order(PetOrder{Action: "stay"}); b != 0x07000000 || h.s.Pet().Command != "stay" {
		t.Fatalf("stay: %#x %+v", b, h.s.Pet())
	}
	if _, b, tg := order(PetOrder{Action: "follow"}); b != 0x07000001 || tg != 0 || h.s.Pet().Command != "follow" {
		t.Fatalf("follow: %#x %v", b, tg)
	}
	for act, want := range map[string]struct {
		button uint32
		react  string
	}{"passive": {0x06000000, "passive"}, "aggressive": {0x06000002, "aggressive"}, "defensive": {0x06000001, "defensive"}} {
		if _, b, _ := order(PetOrder{Action: act}); b != want.button || h.s.Pet().React != want.react {
			t.Fatalf("%s: %#x %+v", act, b, h.s.Pet())
		}
	}
	// A cast is the spell's button with the autocast type the bar has: 0xC1 while autocast is on, 0x81 after it was switched off.
	if _, b, tg := order(PetOrder{Action: "cast", Name: "growl", Target: mob}); b != 0xC1000A59 || tg != mob {
		t.Fatalf("cast growl: %#x %v", b, tg)
	}
	zero := 4
	if _, b, _ := order(PetOrder{Action: "cast", Slot: &zero}); b != 0xC1000000|spBite {
		t.Fatalf("cast slot 4: %#x", b)
	}
	// CMSG_PET_SPELL_AUTOCAST: pet, spell, state (PetHandler.cpp:558-592); the copy of the bar follows.
	done := asyncErr(func() error { return h.s.PetAction(ctx, PetOrder{Action: "autocast_off", SpellID: spGrowl}) })
	p := h.expect(opcode.CMSGPetSpellAutocast)
	finish(t, done)
	if string(p.Body) != string(game.BuildPetSpellAutocast(petGUID, spGrowl, false)) {
		t.Fatalf("% X", p.Body)
	}
	if sp := h.s.Pet().Spells; sp[0].Autocast || !sp[1].Autocast {
		t.Fatalf("%+v", sp)
	}
	if _, b, _ := order(PetOrder{Action: "cast", SpellID: spGrowl, Target: mob}); b != 0x81000A59 {
		t.Fatalf("cast after autocast off: %#x", b)
	}
}

func TestPetOrderRefusals(t *testing.T) {
	ctx := context.Background()
	h := petRig(t)
	if err := h.s.PetAction(ctx, PetOrder{Action: "attack"}); !errors.Is(err, ErrNoPet) {
		t.Fatalf("without a pet: %v", err)
	}
	callPet(h, petVals(400, 900000))
	for _, c := range []struct {
		o    PetOrder
		want string
	}{
		{PetOrder{Action: "attack"}, "no target"},
		{PetOrder{Action: "dance"}, "unknown action"},
		{PetOrder{Action: "cast", Name: "Firebolt"}, "no such spell"},
		{PetOrder{Action: "cast"}, "no such spell"},
		{PetOrder{Action: "autocast_on", SpellID: 99}, "no such spell"},
	} {
		if err := h.s.PetAction(ctx, c.o); err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%+v: %v, want %q", c.o, err, c.want)
		}
	}
	// SMSG_PET_MODE with the disabled flag (the player mounted): the bar is off and no order goes out.
	h.send(opcode.SMSGPetMode, gametest.PetMode(petGUID, 1, 1, true))
	waitFor(t, func() bool { return !h.s.Pet().Enabled })
	if err := h.s.PetAction(ctx, PetOrder{Action: "follow"}); err == nil || !strings.Contains(err.Error(), "bar is off") {
		t.Fatalf("%v", err)
	}
	h.send(opcode.SMSGPetMode, gametest.PetMode(petGUID, 2, 0, false))
	waitFor(t, func() bool { p := h.s.Pet(); return p.Enabled && p.React == "aggressive" && p.Command == "stay" })
	// A dead pet takes no orders.
	h.send(opcode.SMSGUpdateObject, valuesUpdate(petGUID, map[uint16]uint32{objects.UnitHealth: 0}))
	waitFor(t, func() bool { return h.s.Pet().Dead })
	if err := h.s.PetAction(ctx, PetOrder{Action: "follow"}); err == nil || !strings.Contains(err.Error(), "dead") {
		t.Fatalf("%v", err)
	}
	h.noPacket(opcode.CMSGPetAction)
}

func TestPetRefusalsAreEvents(t *testing.T) {
	h := petRig(t)
	callPet(h, petVals(400, 900000))
	h.send(opcode.SMSGPetCastFailed, gametest.PetCastFailed(spGrowl, 60))
	h.send(opcode.SMSGPetActionFeedback, gametest.PetActionFeedback(3))
	waitFor(t, func() bool { return len(ofKind(h.s.Log(), events.KindPetError)) == 2 })
	ev := ofKind(h.s.Log(), events.KindPetError)
	if ev[0].SpellID != spGrowl || ev[0].Data["reason"] != "not_ready" || ev[1].Data["feedback"] != "cant_attack_target" || ev[0].SourceGUID != petGUID {
		t.Fatalf("%+v", ev)
	}
	if p := h.s.Pet(); p.Refused != "not_ready" {
		t.Fatalf("%+v", p)
	}
}
