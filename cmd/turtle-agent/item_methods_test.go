package main

import (
	"testing"

	"github.com/TortoiseBots/turtle-agent/internal/game"
	"github.com/TortoiseBots/turtle-agent/internal/game/gametest"
	"github.com/TortoiseBots/turtle-agent/internal/objects"
	"github.com/TortoiseBots/turtle-agent/internal/objects/objtest"
	"github.com/TortoiseBots/turtle-agent/internal/opcode"
	"github.com/TortoiseBots/turtle-agent/internal/packet"
	"github.com/TortoiseBots/turtle-agent/internal/rpc"
)

// callAsync starts an RPC that waits on the server and returns a channel for its answer.
func (r *agentRig) callAsync(method, params string) chan map[string]any {
	ch := make(chan map[string]any, 1)
	go func() { ch <- r.call(method, params) }()
	return ch
}

// giveSelf sends the player's create block with a stack of potions in the backpack slot 23.
func (r *agentRig) giveSelf(item packet.GUID, entry uint32, extra map[uint16]uint32) {
	w := objtest.Header(2).U8(uint8(objects.BlockCreate2)).PackedGUID(item).U8(objects.TypeItem).U8(0x10).U32(1)
	objtest.Values(w, 0x30, map[uint16]uint32{objects.FieldEntry: entry, objects.ItemStackCount: 3})
	w.U8(uint8(objects.BlockCreate2)).PackedGUID(0x2A).U8(objects.TypePlayer)
	objtest.Living(w, 0x21, game.MoveInfo{X: 1, Y: 2, Z: 3}, nil)
	vals := map[uint16]uint32{objects.PlayerInvSlotHead + 46: uint32(item & 0xFFFFFFFF), objects.PlayerInvSlotHead + 47: uint32(item >> 32)}
	for k, v := range extra {
		vals[k] = v
	}
	objtest.Values(w, 0x502, vals)
	r.srv.Send(opcode.SMSGUpdateObject, w.Bytes())
	r.eventually("inventory", "{}", func(m map[string]any) bool { return len(m["result"].([]any)) == 1 })
}

func TestUseItemMethod(t *testing.T) {
	r := newRig(t)
	r.giveSelf(0x4000000000000003, 4540, nil)
	for _, c := range []struct {
		params string
		want   []byte
	}{
		{`{"item_entry":4540}`, game.BuildUseItem(255, 23, 0)},
		{`{"bag":255,"slot":23}`, game.BuildUseItem(255, 23, 0)},
		{`{"slot":23,"target":"0xF13000012B000001"}`, game.BuildUseItem(255, 23, 0xF13000012B000001)},
	} {
		out := r.call("use_item", c.params)
		if res, ok := out["result"].(map[string]any); !ok || res["item_entry"] != float64(4540) || res["slot"] != float64(23) {
			t.Fatalf("use_item %s: %v", c.params, out)
		}
		r.expectBody(opcode.CMSGUseItem, c.want)
	}
	for _, params := range []string{`{}`, `{"item_entry":1}`, `{"slot":24}`} {
		if out := r.call("use_item", params); errCode(out) != rpc.CodeGame {
			t.Fatalf("use_item %s: %v", params, out)
		}
	}
	if out := r.call("use_item", `{"item_entry":4540,"unknown":1}`); errCode(out) != rpc.CodeInvalidParams {
		t.Fatalf("unknown field: %v", out)
	}
	// inventory with bags lists the same backpack item (no bags carried).
	if out := r.call("inventory", `{"bags":true}`); len(out["result"].([]any)) != 1 {
		t.Fatalf("%v", out)
	}
}

func TestLootMethods(t *testing.T) {
	const wolf packet.GUID = 0xF13000012B000001
	r := newRig(t)
	if out := r.call("loot", "{}"); out["error"] != nil || out["result"] != nil {
		t.Fatalf("no window: %v", out)
	}
	open := r.callAsync("loot", `{"guid":"0xF13000012B000001","wait_ms":3000}`)
	r.expectBody(opcode.CMSGLoot, game.BuildLoot(wolf))
	r.srv.Send(opcode.SMSGLootResponse, gametest.LootResponse(wolf, game.LootCorpse, 12, gametest.LootSlot{Slot: 0, Item: 2589, Count: 1}, gametest.LootSlot{Slot: 1, Item: 118, Count: 2}))
	res := (<-open)["result"].(map[string]any)
	items := res["items"].([]any)
	if res["guid"] != "0xF13000012B000001" || res["type"] != "corpse" || res["money"] != float64(12) || len(items) != 2 ||
		items[0].(map[string]any)["item_entry"] != float64(2589) || items[1].(map[string]any)["slot_type"] != "allow_loot" {
		t.Fatalf("%v", res)
	}
	if out := r.call("loot", "{}"); out["result"].(map[string]any)["money"] != float64(12) {
		t.Fatalf("window: %v", out)
	}

	take := r.callAsync("loot_take", `{"slot":1,"wait_ms":3000}`)
	r.expectBody(opcode.CMSGAutostoreLootItem, []byte{1})
	r.srv.Send(opcode.SMSGLootRemoved, gametest.LootRemoved(1))
	got := (<-take)["result"].(map[string]any)
	if taken := got["taken"].([]any); len(taken) != 1 || taken[0] != float64(1) || got["window"] == nil {
		t.Fatalf("%v", got)
	}
	if out := r.call("loot_take", `{}`); errCode(out) != rpc.CodeGame {
		t.Fatalf("nothing asked: %v", out)
	}

	shut := r.callAsync("loot_close", `{"wait_ms":3000}`)
	r.expectBody(opcode.CMSGLootRelease, game.BuildLootRelease(wolf))
	r.srv.Send(opcode.SMSGLootReleaseResponse, gametest.LootRelease(wolf))
	if out := <-shut; out["error"] != nil {
		t.Fatalf("%v", out)
	}
	if out := r.call("loot_close", `{}`); errCode(out) != rpc.CodeGame {
		t.Fatalf("closed twice: %v", out)
	}
	// A refusal arrives as a game error carrying the reason.
	refused := r.callAsync("loot", `{"guid":"0xF13000012B000001","wait_ms":3000}`)
	r.expect(opcode.CMSGLoot)
	r.srv.Send(opcode.SMSGLootResponse, gametest.LootRefused(wolf, 0))
	if out := <-refused; errCode(out) != rpc.CodeGame {
		t.Fatalf("%v", out)
	}
}

// A bridle that taught a spell which is no mount: the error names the spell as data.learned_spell,
// and the agent casts nothing (the real learn bytes, agent.cap #2708).
func TestMountNamesABridleSpellItDidNotCast(t *testing.T) {
	r := newRig(t)
	r.giveSelf(0x4000000000000003, 5655, nil)
	up := r.callAsync("mount", `{"item_entry":5655,"wait_ms":3000}`)
	r.expectBody(opcode.CMSGUseItem, game.BuildUseItem(255, 23, 0))
	r.srv.Send(opcode.SMSGLearnedSpell, []byte{0x4f, 0x09, 0x00, 0x00}) // spell 2383
	out := <-up
	e, _ := out["error"].(map[string]any)
	data, _ := e["data"].(map[string]any)
	if errCode(out) != rpc.CodeGame || data["learned_spell"] != float64(2383) {
		t.Fatalf("%v", out)
	}
}

func TestMountMethods(t *testing.T) {
	r := newRig(t)
	r.giveSelf(0x4000000000000003, 13317, nil)
	set := func(vals map[uint16]uint32) {
		w := objtest.Header(1).U8(uint8(objects.BlockValues)).PackedGUID(0x2A)
		objtest.Values(w, 0x502, vals)
		r.srv.Send(opcode.SMSGUpdateObject, w.Bytes())
	}
	up := r.callAsync("mount", `{"item_entry":13317,"wait_ms":3000}`)
	r.expectBody(opcode.CMSGUseItem, game.BuildUseItem(255, 23, 0))
	set(map[uint16]uint32{objects.UnitMountDisplay: 14337, objects.UnitAura + 1: 458})
	if res := (<-up)["result"].(map[string]any); res["mounted"] != true || res["display_id"] != float64(14337) || res["via"] != "item" {
		t.Fatalf("%v", res)
	}
	down := r.callAsync("dismount", `{"wait_ms":3000}`)
	r.expectBody(opcode.CMSGCancelAura, game.BuildCancelAura(458))
	set(map[uint16]uint32{objects.UnitMountDisplay: 0, objects.UnitAura + 1: 0})
	if res := (<-down)["result"].(map[string]any); res["mounted"] != false {
		t.Fatalf("%v", res)
	}
	if out := r.call("dismount", `{}`); errCode(out) != rpc.CodeGame {
		t.Fatalf("on foot: %v", out)
	}
	if out := r.call("mount", `{}`); errCode(out) != rpc.CodeGame {
		t.Fatalf("nothing to mount with: %v", out)
	}
	spell := r.callAsync("mount", `{"spell_id":458,"wait_ms":3000}`)
	r.expectBody(opcode.CMSGCastSpell, game.BuildCastSpell(458, 0))
	set(map[uint16]uint32{objects.UnitMountDisplay: 14337})
	if res := (<-spell)["result"].(map[string]any); res["mounted"] != true || res["via"] != "spell" {
		t.Fatalf("%v", res)
	}
}
