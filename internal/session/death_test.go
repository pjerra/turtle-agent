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
	"github.com/pjerra/turtle-agent/internal/opcode"
	"github.com/pjerra/turtle-agent/internal/packet"
)

// Death and the corpse run: the packets a 1.12 client sends and reads (internal/game/death.go).

const (
	healerGUID = packet.GUID(0x30)
	corpseGUID = packet.GUID(0xF1560000000000AA)
)

// selfWith creates the player's own object with the given values over a living base.
func selfWith(vals map[uint16]uint32) []byte {
	v := map[uint16]uint32{objects.UnitHealth: 100, objects.UnitMaxHealth: 100, objects.UnitLevel: 60}
	for k, x := range vals {
		v[k] = x
	}
	return unitCreate(me.GUID, objects.TypePlayer, me.X, me.Y, me.Z, v)
}

func deathRig(t *testing.T) *harness {
	t.Helper()
	h := start(t, Options{NamesWait: 50 * time.Millisecond}) // the resurrection event names its caster, whom nobody answers for
	h.enterWorld()
	h.send(opcode.SMSGUpdateObject, selfWith(nil))
	waitFor(t, func() bool { return h.s.Status().MaxHealth == 100 })
	return h
}

func TestDeathStateFollowsTheUpdateFields(t *testing.T) {
	h := deathRig(t)
	if d := h.s.Death(); d.Dead || d.Ghost || d.Resurrect != nil {
		t.Fatalf("%+v", d)
	}
	h.send(opcode.SMSGUpdateObject, valuesUpdate(me.GUID, map[uint16]uint32{objects.UnitHealth: 0}))
	waitFor(t, func() bool { return h.s.Status().Dead })
	if st := h.s.Status(); !st.Dead || st.Ghost {
		t.Fatalf("%+v", st)
	}
	// The release: health 1 and the ghost flag (Player::BuildPlayerRepop, Player.cpp:5758-5786).
	h.send(opcode.SMSGUpdateObject, valuesUpdate(me.GUID, map[uint16]uint32{objects.UnitHealth: 1, objects.PlayerFlags: objects.PlayerFlagGhost}))
	waitFor(t, func() bool { return h.s.Status().Ghost })
	if st := h.s.Status(); st.Dead || !st.Ghost {
		t.Fatalf("%+v", st)
	}
	if d := h.s.Death(); !d.Ghost || d.Dead {
		t.Fatalf("%+v", d)
	}
}

func TestAResurrectionOfferIsAcceptedOnce(t *testing.T) {
	ctx := context.Background()
	h := deathRig(t)
	if _, err := h.s.AcceptResurrect(ctx); err == nil || !strings.Contains(err.Error(), "alive") {
		t.Fatalf("a live player has nothing to accept: %v", err)
	}
	h.send(opcode.SMSGUpdateObject, valuesUpdate(me.GUID, map[uint16]uint32{objects.UnitHealth: 0}))
	waitFor(t, func() bool { return h.s.Status().Dead })
	if _, err := h.s.AcceptResurrect(ctx); err == nil || !strings.Contains(err.Error(), "no resurrection is on offer") {
		t.Fatalf("%v", err)
	}
	h.send(opcode.SMSGResurrectRequest, gametest.ResurrectRequest(healerGUID, "", false, true))
	waitFor(t, func() bool { return h.s.Death().Resurrect != nil })
	if d := h.s.Death(); d.Resurrect.Caster != healerGUID || d.Resurrect.Sickness || !d.Resurrect.Timer {
		t.Fatalf("%+v", d.Resurrect)
	}
	waitFor(t, func() bool { return len(ofKind(h.s.Log(), events.KindResurrectRequest)) == 1 })
	req := ofKind(h.s.Log(), events.KindResurrectRequest)
	if len(req) != 1 || req[0].SourceGUID != healerGUID || req[0].Data["sickness"] != false {
		t.Fatalf("%+v", req)
	}
	got, err := h.s.AcceptResurrect(ctx)
	if err != nil || got.Caster != healerGUID {
		t.Fatalf("%+v %v", got, err)
	}
	// CMSG_RESURRECT_RESPONSE: the resurrector's guid and 1 (MiscHandler.cpp:740-766).
	p := h.expect(opcode.CMSGResurrectResponse)
	r := packet.NewReader(p.Body)
	if g, accept := r.GUID(), r.U8(); g != healerGUID || accept != 1 || r.Remaining() != 0 {
		t.Fatalf("% X", p.Body)
	}
	// Answered: the offer is gone, and a second answer is refused here instead of being ignored by the server.
	if _, err := h.s.AcceptResurrect(ctx); err == nil {
		t.Fatal("a second accept")
	}
	// The server revives the player; a leftover offer means nothing then.
	h.send(opcode.SMSGResurrectRequest, gametest.ResurrectRequest(healerGUID, "", false, true))
	h.send(opcode.SMSGUpdateObject, valuesUpdate(me.GUID, map[uint16]uint32{objects.UnitHealth: 50}))
	waitFor(t, func() bool { return !h.s.Status().Dead })
	if d := h.s.Death(); d.Resurrect != nil || d.Dead {
		t.Fatalf("%+v", d)
	}
}

func TestReleaseSpiritSendsAnEmptyRepop(t *testing.T) {
	ctx := context.Background()
	h := deathRig(t)
	if err := h.s.ReleaseSpirit(ctx); err == nil || !strings.Contains(err.Error(), "alive") {
		t.Fatalf("%v", err)
	}
	h.send(opcode.SMSGUpdateObject, valuesUpdate(me.GUID, map[uint16]uint32{objects.UnitHealth: 0}))
	waitFor(t, func() bool { return h.s.Status().Dead })
	if err := h.s.ReleaseSpirit(ctx); err != nil {
		t.Fatal(err)
	}
	if p := h.expect(opcode.CMSGRepopRequest); len(p.Body) != 0 {
		t.Fatalf("CMSG_REPOP_REQUEST has no body: % X", p.Body)
	}
	h.send(opcode.SMSGUpdateObject, valuesUpdate(me.GUID, map[uint16]uint32{objects.UnitHealth: 1, objects.PlayerFlags: objects.PlayerFlagGhost}))
	waitFor(t, func() bool { return h.s.Status().Ghost })
	if err := h.s.ReleaseSpirit(ctx); err == nil || !strings.Contains(err.Error(), "already released") {
		t.Fatalf("%v", err)
	}
}

func TestCorpseQueryAnswersAndTheReclaimWaitsOutTheDelay(t *testing.T) {
	ctx := context.Background()
	h := deathRig(t)
	h.send(opcode.SMSGUpdateObject, valuesUpdate(me.GUID, map[uint16]uint32{objects.UnitHealth: 1, objects.PlayerFlags: objects.PlayerFlagGhost}))
	waitFor(t, func() bool { return h.s.Status().Ghost })

	// A corpse object of the player's: its guid goes in the reclaim.
	h.send(opcode.SMSGUpdateObject, unitCreate(corpseGUID, objects.TypeCorpse, me.X, me.Y, me.Z, map[uint16]uint32{
		objects.CorpseOwner: uint32(me.GUID), objects.CorpseOwner + 1: uint32(uint64(me.GUID) >> 32)}))
	waitFor(t, func() bool { return h.s.store.OwnCorpse(me.GUID) == corpseGUID })

	// The delay after the release: 30 s (Player.cpp:22237-22239).
	h.send(opcode.SMSGCorpseReclaimDelay, gametest.CorpseReclaimDelay(30000))
	waitFor(t, func() bool { return h.s.Death().ReclaimInMS > 25000 })
	if d := h.s.Death(); d.ReclaimInMS > 30000 {
		t.Fatalf("%+v", d)
	}
	if err := h.s.ReclaimCorpse(ctx); err == nil || !strings.Contains(err.Error(), "reclaim delay") {
		t.Fatalf("%v", err)
	}

	type res struct {
		c   game.Corpse
		err error
	}
	got := make(chan res, 1)
	go func() { c, err := h.s.CorpseQuery(ctx); got <- res{c, err} }()
	if p := h.expect(opcode.MSGCorpseQuery); len(p.Body) != 0 {
		t.Fatalf("MSG_CORPSE_QUERY from the client has no body: % X", p.Body)
	}
	h.send(opcode.MSGCorpseQuery, gametest.CorpseQuery(0, -6053.7, -2954.6, 213.7, 70))
	r := <-got
	if r.err != nil || !r.c.Found || r.c.Map != 0 || r.c.CorpseMap != 70 || !r.c.Inside() {
		t.Fatalf("%+v %v", r.c, r.err)
	}
	go func() { c, err := h.s.CorpseQuery(ctx); got <- res{c, err} }()
	h.expect(opcode.MSGCorpseQuery)
	h.send(opcode.MSGCorpseQuery, gametest.NoCorpse())
	if r := <-got; r.err != nil || r.c.Found {
		t.Fatalf("%+v %v", r.c, r.err)
	}
}

func TestReclaimCorpseSendsTheCorpseGuidOnceTheDelayIsOver(t *testing.T) {
	ctx := context.Background()
	h := deathRig(t)
	if err := h.s.ReclaimCorpse(ctx); err == nil || !strings.Contains(err.Error(), "alive") {
		t.Fatalf("%v", err)
	}
	h.send(opcode.SMSGUpdateObject, valuesUpdate(me.GUID, map[uint16]uint32{objects.UnitHealth: 0}))
	waitFor(t, func() bool { return h.s.Status().Dead })
	if err := h.s.ReclaimCorpse(ctx); err == nil || !strings.Contains(err.Error(), "not released") {
		t.Fatalf("%v", err)
	}
	h.send(opcode.SMSGUpdateObject, valuesUpdate(me.GUID, map[uint16]uint32{objects.UnitHealth: 1, objects.PlayerFlags: objects.PlayerFlagGhost}))
	waitFor(t, func() bool { return h.s.Status().Ghost })
	h.send(opcode.SMSGUpdateObject, unitCreate(corpseGUID, objects.TypeCorpse, me.X, me.Y, me.Z, map[uint16]uint32{
		objects.CorpseOwner: uint32(me.GUID), objects.CorpseOwner + 1: uint32(uint64(me.GUID) >> 32)}))
	waitFor(t, func() bool { return h.s.store.OwnCorpse(me.GUID) == corpseGUID })
	// A delay that has run out (the clock is the wall clock; 50 ms).
	h.send(opcode.SMSGCorpseReclaimDelay, gametest.CorpseReclaimDelay(50))
	waitFor(t, func() bool { return len(ofKind(h.s.Log(), events.KindCorpseReclaimDelay)) == 1 })
	time.Sleep(80 * time.Millisecond)
	if err := h.s.ReclaimCorpse(ctx); err != nil {
		t.Fatal(err)
	}
	p := h.expect(opcode.CMSGReclaimCorpse)
	if r := packet.NewReader(p.Body); r.GUID() != corpseGUID || r.Remaining() != 0 {
		t.Fatalf("% X", p.Body)
	}
}

func TestAreaTriggerSendsTheTriggerId(t *testing.T) {
	ctx := context.Background()
	h := deathRig(t)
	if err := h.s.AreaTrigger(ctx, 0); err == nil {
		t.Fatal("trigger 0")
	}
	if err := h.s.AreaTrigger(ctx, 286); err != nil {
		t.Fatal(err)
	}
	p := h.expect(opcode.CMSGAreatrigger)
	if r := packet.NewReader(p.Body); r.U32() != 286 || r.Remaining() != 0 {
		t.Fatalf("% X", p.Body)
	}
}
