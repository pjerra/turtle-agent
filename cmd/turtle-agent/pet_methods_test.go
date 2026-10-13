package main

import (
	"context"
	"encoding/binary"
	"testing"
	"time"

	"github.com/pjerra/turtle-agent/internal/game"
	"github.com/pjerra/turtle-agent/internal/game/gametest"
	"github.com/pjerra/turtle-agent/internal/objects"
	"github.com/pjerra/turtle-agent/internal/objects/objtest"
	"github.com/pjerra/turtle-agent/internal/opcode"
	"github.com/pjerra/turtle-agent/internal/packet"
	"github.com/pjerra/turtle-agent/internal/rpc"
	"github.com/pjerra/turtle-agent/internal/session"
)

const (
	rigPet    = packet.GUID(0xF140000000000011)
	rigMob    = packet.GUID(0xF130000000000099)
	rigGrowl  = 2649
	rigPlayer = packet.GUID(0x2A)
)

// callPet creates the pet and tells the player it is theirs, with a bar of Growl (autocast on).
func (r *agentRig) callPet() {
	r.t.Helper()
	w := objtest.Header(1).U8(uint8(objects.BlockCreate2)).PackedGUID(rigPet).U8(objects.TypeUnit)
	objtest.Living(w, 0x20, game.MoveInfo{X: 4, Y: 2, Z: 3}, nil)
	objtest.Values(w, 192, map[uint16]uint32{objects.FieldEntry: 17252, objects.UnitHealth: 300, objects.UnitMaxHealth: 400, objects.UnitLevel: 12,
		objects.UnitBytes0: 2 << 24, objects.UnitPower1 + 2: 80, objects.UnitMaxPower1 + 2: 100, objects.UnitPower5: 700000, objects.UnitMaxPower5: 1000000})
	r.srv.Send(opcode.SMSGUpdateObject, w.Bytes())
	r.srv.Send(opcode.SMSGPetSpells, gametest.PetSpells(rigPet, 2, 1, false, gametest.PetBar(game.PetBarSpell{Spell: rigGrowl, Autocast: true}), nil))
	r.selfValues(map[uint16]uint32{objects.UnitSummon: uint32(uint64(rigPet) & 0xFFFFFFFF), objects.UnitSummon + 1: uint32(uint64(rigPet) >> 32)})
}

func (r *agentRig) pet() map[string]any {
	r.t.Helper()
	return r.call("pet", "{}")["result"].(map[string]any)
}

func TestPetMethods(t *testing.T) {
	r := newRigAt(t, session.Options{NamesWait: 30 * time.Millisecond}, [3]float32{1, 2, 3})
	r.selfValues(nil)
	r.eventually("status", "{}", func(m map[string]any) bool { return m["result"].(map[string]any)["max_health"] == float64(800) })
	if p := r.pet(); p["present"] != false {
		t.Fatalf("%v", p)
	}
	if out := r.call("pet_action", `{"action":"follow"}`); errCode(out) != rpc.CodeGame {
		t.Fatalf("an order without a pet: %v", out)
	}
	r.callPet()
	p := r.eventually("pet", "{}", func(m map[string]any) bool { return m["result"].(map[string]any)["present"] == true })["result"].(map[string]any)
	if p["guid"] != rigPet.String() || p["health"] != float64(300) || p["max_health"] != float64(400) || p["power_type"] != float64(2) ||
		p["happiness"] != float64(700000) || p["happiness_state"] != float64(3) || p["react"] != "aggressive" || p["command"] != "follow" || p["bar"] != true {
		t.Fatalf("%v", p)
	}
	sp := p["spells"].([]any)[0].(map[string]any)
	if sp["spell_id"] != float64(rigGrowl) || sp["name"] != "Growl" || sp["slot"] != float64(3) || sp["autocast"] != true {
		t.Fatalf("%v", sp)
	}
	// pet_action sends CMSG_PET_ACTION: the pet, the button, the target.
	if out := r.call("pet_action", `{"action":"attack","target":"`+rigMob.String()+`"}`); out["error"] != nil {
		t.Fatalf("%v", out)
	}
	pk, err := r.srv.Expect(opcode.CMSGPetAction, 2*time.Second)
	if err != nil || len(pk.Body) != 20 || binary.LittleEndian.Uint64(pk.Body) != uint64(rigPet) || binary.LittleEndian.Uint32(pk.Body[8:]) != 0x07000002 ||
		binary.LittleEndian.Uint64(pk.Body[12:]) != uint64(rigMob) {
		t.Fatalf("% X %v", pk.Body, err)
	}
	if out := r.call("pet_action", `{"action":"defensive"}`); out["error"] != nil || out["result"].(map[string]any)["react"] != "defensive" {
		t.Fatalf("%v", out)
	}
	r.expect(opcode.CMSGPetAction)
	if out := r.call("pet_action", `{"action":"autocast_off","name":"Growl"}`); out["error"] != nil {
		t.Fatalf("%v", out)
	}
	pk, err = r.srv.Expect(opcode.CMSGPetSpellAutocast, 2*time.Second)
	if err != nil || string(pk.Body) != string(game.BuildPetSpellAutocast(rigPet, rigGrowl, false)) {
		t.Fatalf("% X %v", pk.Body, err)
	}
	if out := r.call("pet_action", `{"action":"cast","slot":3,"target":"`+rigMob.String()+`"}`); out["error"] != nil {
		t.Fatalf("%v", out)
	}
	pk, err = r.srv.Expect(opcode.CMSGPetAction, 2*time.Second)
	if err != nil || binary.LittleEndian.Uint32(pk.Body[8:]) != 0x81000A59 {
		t.Fatalf("a cast after autocast went off is the 0x81 button: % X %v", pk.Body, err)
	}
	if out := r.call("pet_action", `{"action":"bark"}`); errCode(out) != rpc.CodeGame {
		t.Fatalf("%v", out)
	}
}

// The brain's view of the pet is the session's, and its orders are the same packets.
func TestSessionBrainGameDrivesThePet(t *testing.T) {
	r := newRigAt(t, session.Options{NamesWait: 30 * time.Millisecond}, [3]float32{1, 2, 3})
	r.selfValues(nil)
	r.eventually("status", "{}", func(m map[string]any) bool { return m["result"].(map[string]any)["max_health"] == float64(800) })
	g := &sessionBrainGame{cur: func() *session.Session { return r.s }}
	if _, ok := g.Pet(); ok {
		t.Fatal("a pet before it was called")
	}
	r.callPet()
	r.eventually("pet", "{}", func(m map[string]any) bool { return m["result"].(map[string]any)["present"] == true })
	st, ok := g.Pet()
	if !ok || st.GUID != rigPet || st.Dead || st.Health != 300 || st.Happiness != 3 || st.React != "aggressive" || st.Command != "follow" || !st.BarKnown || !st.Enabled ||
		len(st.Spells) != 1 || st.Spells[0].ID != rigGrowl || !st.Spells[0].Autocast || !st.Spells[0].OnBar || st.Spells[0].Name != "Growl" {
		t.Fatalf("%+v", st)
	}
	ctx := context.Background()
	order := func(f func() error, want uint32) {
		t.Helper()
		done := make(chan error, 1)
		go func() { done <- f() }()
		pk, err := r.srv.Expect(opcode.CMSGPetAction, 2*time.Second)
		if err != nil || binary.LittleEndian.Uint32(pk.Body[8:]) != want {
			t.Fatalf("% X %v", pk.Body, err)
		}
		if err := <-done; err != nil {
			t.Fatal(err)
		}
	}
	order(func() error { return g.PetAttack(ctx, rigMob) }, 0x07000002)
	order(func() error { return g.PetFollow(ctx) }, 0x07000001)
	order(func() error { return g.PetReact(ctx, "defensive") }, 0x06000001)
	done := make(chan error, 1)
	go func() { done <- g.PetAutocast(ctx, rigGrowl, false) }()
	if pk, err := r.srv.Expect(opcode.CMSGPetSpellAutocast, 2*time.Second); err != nil || pk.Body[12] != 0 {
		t.Fatalf("% X %v", pk.Body, err)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if st, _ := g.Pet(); st.React != "defensive" || st.Spells[0].Autocast {
		t.Fatalf("the copy follows the orders: %+v", st)
	}
}
