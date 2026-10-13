package session

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/pjerra/turtle-agent/internal/events"
	"github.com/pjerra/turtle-agent/internal/game"
	"github.com/pjerra/turtle-agent/internal/objects"
	"github.com/pjerra/turtle-agent/internal/objects/objtest"
	"github.com/pjerra/turtle-agent/internal/opcode"
	"github.com/pjerra/turtle-agent/internal/packet"
	"github.com/pjerra/turtle-agent/internal/testfixture"
)

const (
	doorGUID  packet.GUID = 0xF11001E5D3003839 // entry 124371
	crateGUID packet.GUID = 0xF110000E7B008203 // entry 3707
)

// goCreate is one SMSG_UPDATE_OBJECT holding a game object's create block.
func goCreate(g packet.GUID, x, y, z float32, vals map[uint16]uint32) []byte {
	w := objtest.Header(1).U8(uint8(objects.BlockCreate2)).PackedGUID(g).U8(objects.TypeGameObject)
	objtest.Static(w, x, y, z, 0)
	objtest.Values(w, 26, vals)
	return w.Bytes()
}

func goQueryResponse(entry uint32, typ uint32, name string) []byte {
	w := packet.NewWriter().U32(entry).U32(typ).U32(1369).CString(name).U8(0).U8(0).U8(0).U8(0)
	for i := 0; i < 24; i++ {
		w.U32(0)
	}
	return w.Bytes()
}

// A game object's create block asks its name; the answer names it in the list, the filters and the
// events that follow, and the door's use goes out as the full guid.
func TestGameObjectsNamesEventsAndUse(t *testing.T) {
	ctx := context.Background()
	h := start(t, Options{})
	h.enterWorld()
	h.send(opcode.SMSGUpdateObject, goCreate(doorGUID, me.X+3, me.Y+4, me.Z, map[uint16]uint32{
		objects.FieldEntry: 124371, objects.GOTypeID: 0, objects.GOFlags: 32, objects.GOState: objects.GOStateReady}))
	q := h.expect(opcode.CMSGGameobjectQuery)
	if r := packet.NewReader(q.Body); r.U32() != 124371 || r.GUID() != doorGUID {
		t.Fatalf("query % X", q.Body)
	}
	h.noPacket(opcode.CMSGCreatureQuery) // a game object entry is no creature entry
	h.send(opcode.SMSGGameobjectQueryResponse, goQueryResponse(124371, 0, "Keystone"))
	waitFor(t, func() bool { return len(h.s.GameObjects(objects.GOFilter{Name: "keystone"})) == 1 })
	gs := h.s.GameObjects(objects.GOFilter{})
	if len(gs) != 1 || gs[0].Name != "Keystone" || gs[0].Distance != 5 || gs[0].TypeName != "door" || gs[0].State != "ready" || !gs[0].Usable {
		t.Fatalf("%+v", gs)
	}
	if got := h.s.GameObjects(objects.GOFilter{Name: "lever"}); len(got) != 0 {
		t.Fatalf("%+v", got)
	}
	if len(h.s.Units(objects.Filter{})) != 0 {
		t.Fatal("a game object is no unit")
	}

	// Using it: the packet is the guid; an object that is not in view is refused before anything is sent.
	o, err := h.s.UseObject(ctx, doorGUID)
	if err != nil || o.Name != "Keystone" || o.GUID != doorGUID {
		t.Fatalf("%+v %v", o, err)
	}
	if p := h.expect(opcode.CMSGGameobjUse); string(p.Body) != string(game.BuildGameObjectUse(doorGUID)) {
		t.Fatalf("% X", p.Body)
	}
	if _, err := h.s.UseObject(ctx, crateGUID); err == nil || !strings.Contains(err.Error(), "not in view") {
		t.Fatalf("an unseen object: %v", err)
	}
	if _, err := h.s.UseObject(ctx, 0); err == nil {
		t.Fatal("no guid accepted")
	}
	h.noPacket(opcode.CMSGGameobjUse)

	// The server's answer to the use: flags and state change on the object, as events with its name.
	h.send(opcode.SMSGUpdateObject, valuesUpdate(doorGUID, map[uint16]uint32{objects.GOFlags: 5, objects.GOState: objects.GOStateActive}))
	waitFor(t, func() bool { return len(ofKind(h.s.Log(), events.KindGameObject)) == 2 })
	evs := ofKind(h.s.Log(), events.KindGameObject)
	flags, state := evs[0], evs[1]
	if flags.Source != "Keystone" || flags.SourceGUID != doorGUID || flags.Data["change"] != "flags" || flags.Data["old"] != uint32(32) ||
		flags.Data["new"] != uint32(5) || flags.Data["locked"] != false || flags.Data["entry"] != uint32(124371) || flags.Data["type"] != "door" {
		t.Fatalf("%+v", flags)
	}
	if state.Data["change"] != "state" || state.Data["old"] != "ready" || state.Data["new"] != "active" {
		t.Fatalf("%+v", state)
	}
	if g := h.s.GameObjects(objects.GOFilter{}); g[0].State != "active" || g[0].Usable {
		t.Fatalf("%+v", g[0])
	}

	// Animation packets and a destroy.
	h.send(opcode.SMSGGameobjectCustomAnim, packet.NewWriter().GUID(doorGUID).U32(1).Bytes())
	h.send(opcode.SMSGGameobjectResetState, packet.NewWriter().GUID(doorGUID).Bytes())
	h.send(opcode.SMSGDestroyObject, packet.NewWriter().GUID(doorGUID).Bytes())
	waitFor(t, func() bool { return len(ofKind(h.s.Log(), events.KindGameObject)) == 5 })
	evs = ofKind(h.s.Log(), events.KindGameObject)
	if evs[2].Data["change"] != "animation" || evs[2].Data["anim"] != uint32(1) || evs[3].Data["change"] != "reset" || evs[4].Data["change"] != "destroyed" || evs[4].Source != "Keystone" {
		t.Fatalf("%+v", evs[2:])
	}
	if len(h.s.GameObjects(objects.GOFilter{})) != 0 {
		t.Fatal("a destroyed object is still listed")
	}
	h.logout()
}

// An animation packet for an object the store never held reports neither entry nor type: not "door", not 0.
func TestGameObjectEventOfUnheldObject(t *testing.T) {
	h := start(t, Options{NamesWait: 100 * time.Millisecond})
	h.enterWorld()
	h.send(opcode.SMSGGameobjectDespawnAnim, packet.NewWriter().GUID(crateGUID).Bytes())
	waitFor(t, func() bool { return len(ofKind(h.s.Log(), events.KindGameObject)) == 1 })
	ghost := ofKind(h.s.Log(), events.KindGameObject)[0]
	if _, ok := ghost.Data["entry"]; ok || ghost.Data["change"] != "despawn" {
		t.Fatalf("%+v", ghost)
	}
	if _, ok := ghost.Data["type"]; ok {
		t.Fatalf("%+v", ghost)
	}
	h.logout()
}

// An entry the server does not know is named after the entry, so the event does not wait for ever; a
// creature and a game object of the same entry keep their own names and queries.
func TestGameObjectAndCreatureOfOneEntry(t *testing.T) {
	h := start(t, Options{})
	h.enterWorld()
	h.send(opcode.SMSGUpdateObject, unitCreate(wolfGUID, objects.TypeUnit, me.X+1, me.Y, me.Z, map[uint16]uint32{objects.FieldEntry: 299, objects.UnitHealth: 5, objects.UnitMaxHealth: 5}))
	h.expect(opcode.CMSGCreatureQuery)
	h.send(opcode.SMSGUpdateObject, goCreate(0xF110000299008001, me.X+2, me.Y, me.Z, map[uint16]uint32{objects.FieldEntry: 299, objects.GOTypeID: 3}))
	if q := h.expect(opcode.CMSGGameobjectQuery); packet.NewReader(q.Body).U32() != 299 {
		t.Fatalf("% X", q.Body)
	}
	wolfName(h)
	h.send(opcode.SMSGGameobjectQueryResponse, packet.NewWriter().U32(299|0x80000000).Bytes())
	waitFor(t, func() bool { return len(h.s.GameObjects(objects.GOFilter{Name: "gameobject 299"})) == 1 })
	waitFor(t, func() bool { us := h.s.Units(objects.Filter{}); return len(us) == 1 && us[0].Name == "Young Wolf" })
	h.logout()
}

// The agent's own run in Uldaman, replayed through the session: the names come from the captured
// query responses and the door's use arrives as two game object events.
func TestReplayUldamanGameObjects(t *testing.T) {
	recs := testfixture.Load(t, "m7-gameobjects.twcap")
	log, rep, err := Replay(recs, Options{NamesWait: 100 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	if rep.Character != "Harnessc" {
		t.Fatalf("%+v", rep)
	}
	var keystone []events.Event
	for _, e := range ofKind(log, events.KindGameObject) {
		if e.SourceGUID == doorGUID {
			keystone = append(keystone, e)
		}
	}
	if len(keystone) < 2 {
		t.Fatalf("%d events for the Keystone door", len(keystone))
	}
	if e := keystone[0]; e.Source != "Keystone" || e.Data["change"] != "flags" || e.Data["old"] != uint32(32) || e.Data["new"] != uint32(5) {
		t.Fatalf("%+v", e)
	}
	if e := keystone[1]; e.Data["change"] != "state" || e.Data["old"] != "ready" || e.Data["new"] != "active" {
		t.Fatalf("%+v", e)
	}
	// Every game object the run named has its name: the capture holds the answers.
	for _, e := range ofKind(log, events.KindGameObject) {
		if e.Source == "" {
			t.Fatalf("an unnamed game object event: %+v", e)
		}
	}
}
