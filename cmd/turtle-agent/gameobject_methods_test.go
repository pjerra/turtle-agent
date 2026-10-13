package main

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/pjerra/turtle-agent/internal/game"
	"github.com/pjerra/turtle-agent/internal/game/gametest"
	"github.com/pjerra/turtle-agent/internal/objects"
	"github.com/pjerra/turtle-agent/internal/objects/objtest"
	"github.com/pjerra/turtle-agent/internal/opcode"
	"github.com/pjerra/turtle-agent/internal/packet"
	"github.com/pjerra/turtle-agent/internal/rpc"
)

// goCreate sends the create block of a game object and answers its name query.
func (r *agentRig) goCreate(g packet.GUID, entry uint32, name string, x, y, z float32, vals map[uint16]uint32) {
	w := objtest.Header(1).U8(uint8(objects.BlockCreate2)).PackedGUID(g).U8(objects.TypeGameObject)
	objtest.Static(w, x, y, z, 0)
	v := map[uint16]uint32{objects.FieldEntry: entry}
	for k, val := range vals {
		v[k] = val
	}
	objtest.Values(w, 26, v)
	r.srv.Send(opcode.SMSGUpdateObject, w.Bytes())
	r.expect(opcode.CMSGGameobjectQuery)
	resp := packet.NewWriter().U32(entry).U32(v[objects.GOTypeID]).U32(1369).CString(name).U8(0).U8(0).U8(0).U8(0)
	for i := 0; i < 24; i++ {
		resp.U32(0)
	}
	r.srv.Send(opcode.SMSGGameobjectQueryResponse, resp.Bytes())
}

func (r *agentRig) goValues(g packet.GUID, vals map[uint16]uint32) {
	w := objtest.Header(1).U8(uint8(objects.BlockValues)).PackedGUID(g)
	objtest.Values(w, 26, vals)
	r.srv.Send(opcode.SMSGUpdateObject, w.Bytes())
}

func TestGameObjectsMethod(t *testing.T) {
	const door, chest packet.GUID = 0xF11001E5D3003839, 0xF11001E5D4003856
	r := newRig(t) // the character stands at 1, 2, 3
	if out := r.call("gameobjects", "{}"); out["error"] != nil || out["result"] == nil || len(out["result"].([]any)) != 0 {
		t.Fatalf("an empty list is [], not null: %v", out)
	}
	r.goCreate(door, 124371, "Keystone", 4, 6, 3, map[uint16]uint32{objects.GOFlags: 32, objects.GOState: objects.GOStateReady})
	r.goCreate(chest, 3707, "Food Crate", 101, 2, 3, map[uint16]uint32{objects.GOTypeID: 3, objects.GODynFlags: 1})
	list := func(params string) []any {
		t.Helper()
		out := r.eventually("gameobjects", params, func(m map[string]any) bool {
			for _, g := range m["result"].([]any) {
				if g.(map[string]any)["name"] == "" {
					return false
				}
			}
			return true
		})
		return out["result"].([]any)
	}
	all := list("{}")
	if len(all) != 2 {
		t.Fatalf("%v", all)
	}
	d := all[0].(map[string]any)
	if d["guid"] != "0xF11001E5D3003839" || d["entry"] != float64(124371) || d["name"] != "Keystone" || d["type"] != float64(0) || d["type_name"] != "door" ||
		d["state"] != "ready" || d["flags"] != float64(32) || d["locked"] != false || d["usable"] != true || d["distance"] != float64(5) || d["x"] != float64(4) {
		t.Fatalf("%v", d)
	}
	if got := list(`{"within":50}`); len(got) != 1 {
		t.Fatalf("within: %v", got)
	}
	if got := list(`{"radius":50}`); len(got) != 1 {
		t.Fatalf("radius is the same: %v", got)
	}
	if got := list(`{"entry":3707}`); len(got) != 1 || got[0].(map[string]any)["name"] != "Food Crate" {
		t.Fatalf("entry: %v", got)
	}
	for _, typ := range []string{`"door"`, `0`, `"DOOR"`} {
		if got := list(`{"type":` + typ + `}`); len(got) != 1 || got[0].(map[string]any)["name"] != "Keystone" {
			t.Fatalf("type %s: %v", typ, got)
		}
	}
	if got := list(`{"type":"chest","name":"crate"}`); len(got) != 1 {
		t.Fatalf("type and name: %v", got)
	}
	if got := list(`{"limit":1}`); len(got) != 1 {
		t.Fatalf("limit: %v", got)
	}
	for _, bad := range []string{`{"type":"lever"}`, `{"type":99}`, `{"type":true}`, `{"colour":1}`} {
		if out := r.call("gameobjects", bad); errCode(out) != rpc.CodeInvalidParams {
			t.Fatalf("%s: %v", bad, out)
		}
	}
	if units := r.call("units", "{}")["result"].([]any); len(units) != 0 {
		t.Fatalf("game objects are no units: %v", units)
	}
}

func TestUseObjectMethod(t *testing.T) {
	const door, far, lever packet.GUID = 0xF11001E5D3003839, 0xF11001E5D4003856, 0xF11001E5D5003857
	r := newRig(t)
	r.goCreate(door, 124371, "Keystone", 4, 6, 3, map[uint16]uint32{objects.GOFlags: 32, objects.GOState: objects.GOStateReady})
	r.goCreate(far, 124372, "Seal of Khaz'Mul", 80, 6, 3, map[uint16]uint32{objects.GOFlags: 32})
	r.goCreate(lever, 124373, "Ancient Vault", 4, 5, 3, map[uint16]uint32{objects.GOFlags: objects.GOFlagInUse | 32})

	// The door answers with a flags and a state change; the reply carries the first and the cursor to read the rest.
	use := r.callAsync("use_object", `{"guid":"0xF11001E5D3003839","wait_ms":3000}`)
	r.expectBody(opcode.CMSGGameobjUse, game.BuildGameObjectUse(door))
	r.goValues(door, map[uint16]uint32{objects.GOFlags: 5, objects.GOState: objects.GOStateActive})
	res := (<-use)["result"].(map[string]any)
	ev := res["event"].(map[string]any)
	if res["outcome"] != "gameobject" || ev["kind"] != "gameobject" || ev["source"] != "Keystone" || ev["data"].(map[string]any)["change"] != "flags" ||
		res["object"].(map[string]any)["name"] != "Keystone" || res["hint"] != nil {
		t.Fatalf("%v", res)
	}
	page := r.call("events", `{"since":`+fmt.Sprint(res["since"])+`,"kinds":["gameobject"]}`)["result"].(map[string]any)["events"].([]any)
	if len(page) != 2 || page[1].(map[string]any)["data"].(map[string]any)["new"] != "active" {
		t.Fatalf("the cursor reads both changes: %v", page)
	}

	// A chest-like silence: out of reach, nothing comes back, and the hint says why.
	res = r.call("use_object", `{"guid":"0xF11001E5D4003856","wait_ms":200}`)["result"].(map[string]any)
	r.expect(opcode.CMSGGameobjUse)
	if res["outcome"] != "none" || res["event"] != nil || res["hint"] == nil {
		t.Fatalf("%v", res)
	}
	if h := res["hint"].(string); !strings.Contains(h, "yards away") {
		t.Fatalf("hint %q", h)
	}
	res = r.call("use_object", `{"guid":"0xF11001E5D5003857","wait_ms":200}`)["result"].(map[string]any)
	r.expect(opcode.CMSGGameobjUse)
	if h, _ := res["hint"].(string); res["outcome"] != "none" || !strings.Contains(h, "unusable") {
		t.Fatalf("%v", res)
	}

	// What another object or NPC does is no answer; a menu of the object itself is.
	use = r.callAsync("use_object", `{"guid":"0xF11001E5D3003839","wait_ms":3000}`)
	r.expect(opcode.CMSGGameobjUse)
	r.srv.Send(opcode.SMSGGossipMessage, gametest.GossipMessage(far, 1, nil, nil))
	time.Sleep(150 * time.Millisecond)
	r.srv.Send(opcode.SMSGGossipMessage, gametest.GossipMessage(door, 2, []game.GossipOption{{Index: 0, Icon: 0, Text: "Use the keystone"}}, nil))
	res = (<-use)["result"].(map[string]any)
	if ev := res["event"].(map[string]any); res["outcome"] != "gossip" || ev["source_guid"] != "0xF11001E5D3003839" || ev["data"].(map[string]any)["text_id"] != float64(2) {
		t.Fatalf("%v", res)
	}
	// A refusal to loot, and a spell of the player's, answer as well; a spell of someone else does not.
	use = r.callAsync("use_object", `{"guid":"0xF11001E5D3003839","wait_ms":3000}`)
	r.expect(opcode.CMSGGameobjUse)
	r.srv.Send(opcode.SMSGLootResponse, gametest.LootRefused(door, 0))
	if res = (<-use)["result"].(map[string]any); res["outcome"] != "loot_error" {
		t.Fatalf("%v", res)
	}

	// Errors: no guid, not in view.
	for _, params := range []string{`{}`, `{"guid":"0xF11001E5D9003858"}`} {
		if out := r.call("use_object", params); errCode(out) != rpc.CodeGame {
			t.Fatalf("%s: %v", params, out)
		}
	}
	if out := r.call("use_object", `{"guid":"0x1","extra":1}`); errCode(out) != rpc.CodeInvalidParams {
		t.Fatalf("%v", out)
	}
}
