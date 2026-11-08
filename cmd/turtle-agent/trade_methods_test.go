package main

import (
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
	"github.com/pjerra/turtle-agent/internal/session"
)

func TestPlayerRef(t *testing.T) {
	for _, tc := range []struct {
		in   string
		guid packet.GUID
		name string
		bad  bool
	}{
		{"Gorm", 0, "Gorm", false},
		{" Deaf ", 0, "Deaf", false}, // letters a-f only: a name, not a guid
		{"0x0000000000000099", 0x99, "", false},
		{"0X99", 0x99, "", false},
		{"42", 0x42, "", false},
		{"0xZZ", 0, "", true},
		{"", 0, "", true},
	} {
		g, n, err := playerRef(tc.in)
		if (err != nil) != tc.bad || g != tc.guid || n != tc.name {
			t.Errorf("%q: %v %q %v", tc.in, g, n, err)
		}
	}
}

// trade over JSON-RPC: it validates, hands a stack and gold to a nearby player, and a trade that fails answers an
// error whose data is the result.
func TestTradeMethod(t *testing.T) {
	const bread packet.GUID = 0x4000000000000003
	r := newRigAt(t, session.Options{TradeSettle: 20 * time.Millisecond}, [3]float32{1, 2, 3})
	r.giveSelf(bread, 4540, map[uint16]uint32{objects.PlayerCoinage: 300})
	w := objtest.Header(1).U8(uint8(objects.BlockCreate2)).PackedGUID(0x99).U8(objects.TypePlayer)
	objtest.Living(w, 0x20, game.MoveInfo{X: 3, Y: 2, Z: 3}, nil)
	objtest.Values(w, 192, map[uint16]uint32{objects.UnitHealth: 500, objects.UnitMaxHealth: 500})
	r.srv.Send(opcode.SMSGUpdateObject, w.Bytes())
	r.srv.Send(opcode.SMSGNameQueryResponse, packet.NewWriter().GUID(0x99).CString("Gorm").U8(0).U32(1).U32(0).U32(1).Bytes())
	r.eventually("units", `{"name":"gorm"}`, func(m map[string]any) bool { return len(m["result"].([]any)) == 1 })

	// Bad requests never reach the server.
	for _, params := range []string{`{}`, `{"to":"Gorm"}`, `{"to":"0xZZ","gold":1}`, `{"to":"Nobody","gold":1}`, `{"to":"Gorm","gold":301}`,
		`{"to":"Gorm","items":[{"item":4540,"count":4}]}`, `{"to":"Gorm","items":[{"item":4540}],"extra":1}`} {
		want := rpc.CodeGame
		if strings.Contains(params, "extra") {
			want = rpc.CodeInvalidParams
		}
		if out := r.call("trade", params); errCode(out) != want {
			t.Fatalf("trade %s: %v", params, out)
		}
	}
	if p, err := r.srv.Expect(opcode.CMSGInitiateTrade, 100*time.Millisecond); err == nil {
		t.Fatalf("initiated % X", p.Body)
	}

	done := r.callAsync("trade", `{"to":"Gorm","items":[{"item":4540,"count":3}],"gold":120}`)
	r.expectBody(opcode.CMSGInitiateTrade, game.BuildInitiateTrade(0x99))
	r.srv.Send(opcode.SMSGTradeStatus, gametest.TradeStatus(game.TradeOpenWindow, 0))
	r.expectBody(opcode.CMSGSetTradeItem, game.BuildSetTradeItem(0, 255, 23))
	r.expectBody(opcode.CMSGSetTradeGold, game.BuildSetTradeGold(120))
	r.expect(opcode.CMSGAcceptTrade)
	r.srv.Send(opcode.SMSGTradeStatus, gametest.TradeStatus(game.TradeComplete, 0))
	res := (<-done)["result"].(map[string]any)
	gave := res["gave"].([]any)
	if res["result"] != "completed" || res["partner_name"] != "Gorm" || res["gold"] != float64(120) || len(gave) != 1 ||
		gave[0].(map[string]any)["item"] != float64(4540) || gave[0].(map[string]any)["count"] != float64(3) || res["received"] == nil {
		t.Fatalf("%v", res)
	}

	// The other side turns the trade down: an error, with the result in data.
	done = r.callAsync("trade", `{"to":"0x0000000000000099","gold":5}`)
	r.expect(opcode.CMSGInitiateTrade)
	r.srv.Send(opcode.SMSGTradeStatus, gametest.TradeStatus(game.TradeBusy, 0))
	out := <-done
	data, _ := out["error"].(map[string]any)["data"].(map[string]any)
	if errCode(out) != rpc.CodeGame || data["result"] != "busy" || data["status"] != "busy" {
		t.Fatalf("%v", out)
	}
	// The trade_status events are in the log.
	ev := r.call("events", `{"since":0,"kinds":["trade_status"]}`)["result"].(map[string]any)["events"].([]any)
	if len(ev) < 3 {
		t.Fatalf("%v", ev)
	}
}

// gormNearby puts the player Gorm (guid 0x99) three yards from the rig's character.
func (r *agentRig) gormNearby() {
	w := objtest.Header(1).U8(uint8(objects.BlockCreate2)).PackedGUID(0x99).U8(objects.TypePlayer)
	objtest.Living(w, 0x20, game.MoveInfo{X: 3, Y: 2, Z: 3}, nil)
	objtest.Values(w, 192, map[uint16]uint32{objects.UnitHealth: 500, objects.UnitMaxHealth: 500})
	r.srv.Send(opcode.SMSGUpdateObject, w.Bytes())
	r.srv.Send(opcode.SMSGNameQueryResponse, packet.NewWriter().GUID(0x99).CString("Gorm").U8(0).U32(1).U32(0).U32(1).Bytes())
	r.eventually("units", `{"name":"gorm"}`, func(m map[string]any) bool { return len(m["result"].([]any)) == 1 })
}

func TestTradeStepMethodsParams(t *testing.T) {
	r := newRig(t)
	for _, tc := range []struct {
		method, params string
		code           int
		msg            string
	}{
		{"trade_set_item", `{}`, rpc.CodeInvalidParams, "trade_slot"},
		{"trade_set_item", `{"trade_slot":7,"item_entry":1420}`, rpc.CodeInvalidParams, "trade_slot 0-6"},
		{"trade_set_item", `{"trade_slot":300,"item_entry":1420}`, rpc.CodeInvalidParams, ""},
		{"trade_set_item", `{"trade_slot":6}`, rpc.CodeGame, "needs an item_entry or a slot"},
		{"trade_set_item", `{"trade_slot":6,"item_entry":1420}`, rpc.CodeGame, "no trade window is open"},
		{"cast", `{"spell_id":7418,"trade_slot":6,"target":"0x1"}`, rpc.CodeInvalidParams, "not both"},
		{"cast", `{"spell_id":7418,"trade_slot":7}`, rpc.CodeInvalidParams, "0-6"},
		{"cast", `{"spell_id":7418,"trade_slot":6}`, rpc.CodeGame, "no trade window is open"},
		{"trade_expect", `{}`, rpc.CodeGame, `"from"`},
		{"trade_open", `{}`, rpc.CodeGame, `"to"`},
		{"trade_open", `{"to":"Gorm","extra":1}`, rpc.CodeInvalidParams, ""},
		{"trade_accept", `{}`, rpc.CodeGame, "no trade window is open"},
	} {
		out := r.call(tc.method, tc.params)
		msg, _ := out["error"].(map[string]any)["message"].(string)
		if errCode(out) != tc.code || !strings.Contains(msg, tc.msg) {
			t.Errorf("%s %s: %v", tc.method, tc.params, out)
		}
	}
	for _, op := range []uint16{opcode.CMSGInitiateTrade, opcode.CMSGSetTradeItem, opcode.CMSGCastSpell, opcode.CMSGAcceptTrade} {
		if p, err := r.srv.Expect(op, 50*time.Millisecond); err == nil {
			t.Errorf("%s was sent: % X", opcode.Name(op), p.Body)
		}
	}
	// trade_status and trade_cancel never fail, and the lists are arrays.
	st := r.call("trade_status", `{}`)["result"].(map[string]any)
	if st["active"] != false || st["their_items"] == nil || st["my_items"] == nil {
		t.Fatalf("%v", st)
	}
	if out := r.call("trade_cancel", `{}`); out["error"] != nil {
		t.Fatalf("%v", out)
	}
}

// The whole step trade over JSON-RPC, as the Eluna smoke drives it: expect, begin, set the item in slot 6, cast the
// enchant at it, accept, complete; every client packet body is checked.
func TestTradeStepMethodsFlow(t *testing.T) {
	const bracers packet.GUID = 0x4000000000000041
	r := newRigAt(t, session.Options{TradeSettle: 20 * time.Millisecond}, [3]float32{1, 2, 3})
	r.giveSelf(bracers, 1420, nil)
	r.gormNearby()

	expect := r.callAsync("trade_expect", `{"from":"Gorm","wait_ms":5000}`)
	r.eventually("trade_status", `{}`, func(m map[string]any) bool { return m["result"].(map[string]any)["armed_from"] == "0x0000000000000099" })
	r.srv.Send(opcode.SMSGTradeStatus, gametest.TradeStatus(game.TradeBegin, 0x99))
	r.expectBody(opcode.CMSGBeginTrade, nil)
	r.srv.Send(opcode.SMSGTradeStatus, gametest.TradeStatus(game.TradeOpenWindow, 0))
	st := (<-expect)["result"].(map[string]any)
	if st["open"] != true || st["step"] != true || st["partner"] != "0x0000000000000099" || st["partner_name"] != "Gorm" {
		t.Fatalf("trade_expect: %v", st)
	}

	set := r.callAsync("trade_set_item", `{"trade_slot":6,"item_entry":1420}`)
	r.expectBody(opcode.CMSGSetTradeItem, []byte{0x06, 0xFF, 0x17})
	r.srv.Send(opcode.SMSGTradeStatusExtended, gametest.TradeExtended(false, 0, gametest.TradeSide{Slot: 6, Entry: 1420, Count: 1}))
	r.srv.Send(opcode.SMSGTradeStatus, gametest.TradeStatus(game.TradeBackToTrade, 0))
	got := (<-set)["result"].(map[string]any)
	if got["trade_slot"] != float64(6) || got["bag"] != float64(255) || got["slot"] != float64(23) || got["item_entry"] != float64(1420) || got["guid"] != "0x4000000000000041" {
		t.Fatalf("trade_set_item: %v", got)
	}

	if out := r.call("cast", `{"spell_id":7418,"trade_slot":6}`); out["error"] != nil {
		t.Fatalf("%v", out)
	}
	r.expectBody(opcode.CMSGCastSpell, []byte{0xFA, 0x1C, 0x00, 0x00, 0x00, 0x10, 0x01, 0x06})
	r.srv.Send(opcode.SMSGTradeStatusExtended, gametest.TradeExtendedSpell(false, 0, 7418))
	r.srv.Send(opcode.SMSGTradeStatusExtended, gametest.TradeExtended(true, 0, gametest.TradeSide{Slot: 6, Entry: 1420, Count: 1}))
	out := r.eventually("trade_status", `{}`, func(m map[string]any) bool { return m["result"].(map[string]any)["my_spell"] == float64(7418) })
	st = out["result"].(map[string]any)
	if items := st["their_items"].([]any); len(items) != 1 || items[0].(map[string]any)["slot"] != float64(6) {
		t.Fatalf("trade_status: %v", st)
	}

	acc := r.callAsync("trade_accept", `{"wait_ms":5000}`)
	r.expectBody(opcode.CMSGAcceptTrade, game.BuildAcceptTrade())
	r.srv.Send(opcode.SMSGTradeStatus, gametest.TradeStatus(game.TradeComplete, 0))
	res := (<-acc)["result"].(map[string]any)
	if res["result"] != "completed" || res["partner_name"] != "Gorm" {
		t.Fatalf("trade_accept: %v", res)
	}
	if st := r.call("trade_status", `{}`)["result"].(map[string]any); st["active"] != false {
		t.Fatalf("%v", st)
	}
	if p, err := r.srv.Expect(opcode.CMSGCancelTrade, 100*time.Millisecond); err == nil {
		t.Fatalf("cancel after a complete trade % X", p.Body)
	}
}
