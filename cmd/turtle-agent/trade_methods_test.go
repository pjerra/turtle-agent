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
