package main

import (
	"bytes"
	"testing"
	"time"

	"github.com/pjerra/turtle-agent/internal/game"
	"github.com/pjerra/turtle-agent/internal/game/gametest"
	"github.com/pjerra/turtle-agent/internal/opcode"
	"github.com/pjerra/turtle-agent/internal/packet"
	"github.com/pjerra/turtle-agent/internal/rpc"
)

func (r *agentRig) expectBody(op uint16, want []byte) {
	r.t.Helper()
	p, err := r.srv.Expect(op, 2*time.Second)
	if err != nil {
		r.t.Fatal(err)
	}
	if !bytes.Equal(p.Body, want) {
		r.t.Fatalf("%s: % X, want % X", opcode.Name(op), p.Body, want)
	}
}

func say(text string) []byte { return packet.NewWriter().U32(0).U32(0).CString(text).Bytes() }

func TestAddonSendMethod(t *testing.T) {
	r := newRig(t)
	if out := r.call("addon_send", `{"prefix":"TBM","text":"roster","channel":"party"}`); out["error"] != nil {
		t.Fatalf("%v", out)
	}
	r.expectBody(opcode.CMSGMessagechat, packet.NewWriter().U32(1).U32(game.LangAddon).CString("TBM\troster").Bytes())
	if out := r.call("addon_send", `{"prefix":"TBM","text":"x","channel":"whisper","to":"Gorm"}`); errCode(out) != rpc.CodeGame {
		t.Fatalf("whisper: %v", out)
	}
	if out := r.call("addon_send", `{"prefix":"TBM","text":"x"}`); errCode(out) != rpc.CodeGame {
		t.Fatalf("no channel: %v", out)
	}
}

func TestTBMCommandMethod(t *testing.T) {
	r := newRig(t)
	if out := r.call("tbm", `{"command":"action attack"}`); out["error"] != nil {
		t.Fatalf("%v", out)
	}
	r.expectBody(opcode.CMSGMessagechat, say(".bot action attack"))
	if out := r.call("tbm", `{"command":"action attack","via":"addon"}`); out["error"] != nil {
		t.Fatalf("%v", out)
	}
	r.expectBody(opcode.CMSGMessagechat, packet.NewWriter().U32(1).U32(game.LangAddon).CString("TBM\taction attack").Bytes())
	if out := r.call("tbm", `{"command":"x","via":"pigeon"}`); errCode(out) != rpc.CodeGame {
		t.Fatalf("%v", out)
	}
}

// The handshake sends ".bot help" and ".bot roster" as the addon does on login and
// collects the roster response up to its TBM:TRANSPORT trailer, from system lines (chat
// transport) and TBM addon lines (addon transport) alike.
func TestTBMHandshakeMethod(t *testing.T) {
	r := newRig(t)
	got := make(chan map[string]any, 1)
	go func() { got <- r.call("tbm_handshake", `{"wait_ms":3000}`) }()
	r.expectBody(opcode.CMSGMessagechat, say(".bot help"))
	r.expectBody(opcode.CMSGMessagechat, say(".bot roster"))
	system := func(s string) {
		r.srv.Send(opcode.SMSGMessagechat, gametest.ChatPacket(game.ChatSystem, 0, 0, "", 0, "", s, 0))
	}
	system("TBM:VERSION|2026-10-11-v8")
	system("TBM:ROSTER_BEGIN|1")
	system("TBM:ROSTER|7|Gorm|1|removing|1|-")
	system("TBM:ROSTER_END")
	r.srv.Send(opcode.SMSGMessagechat, gametest.AddonMessage(0x2A, "TBM", "TBM:CAPS|pull-seconds,flee", 0))
	system("Unrelated system line")
	system("TBM:TRANSPORT|party")
	var out map[string]any
	select {
	case out = <-got:
	case <-time.After(3 * time.Second):
		t.Fatal("tbm_handshake did not return")
	}
	res, ok := out["result"].(map[string]any)
	if !ok || res["complete"] != true || res["transport"] != "party" || res["version"] != "2026-10-11-v8" {
		t.Fatalf("%v", out)
	}
	rows := res["roster"].([]any)
	if len(rows) != 1 || rows[0].(map[string]any)["state"] != "removing" || rows[0].(map[string]any)["name"] != "Gorm" {
		t.Fatalf("roster %v", rows)
	}
	if caps := res["caps"].([]any); len(caps) != 2 || caps[1] != "flee" {
		t.Fatalf("caps %v", caps)
	}
	if lines := res["lines"].([]any); len(lines) != 6 {
		t.Fatalf("lines %v", lines)
	}
	if res["since"].(float64) >= res["next"].(float64) {
		t.Fatalf("cursors %v", res)
	}
}

// No module answer: the wait runs out and the result says the response is incomplete.
func TestTBMHandshakeTimesOut(t *testing.T) {
	r := newRig(t)
	got := make(chan map[string]any, 1)
	go func() { got <- r.call("tbm_handshake", `{"wait_ms":200,"via":"addon"}`) }()
	r.expectBody(opcode.CMSGMessagechat, packet.NewWriter().U32(1).U32(game.LangAddon).CString("TBM\thelp").Bytes())
	r.expectBody(opcode.CMSGMessagechat, packet.NewWriter().U32(1).U32(game.LangAddon).CString("TBM\troster").Bytes())
	select {
	case out := <-got:
		if res := out["result"].(map[string]any); res["complete"] != false || len(res["lines"].([]any)) != 0 {
			t.Fatalf("%v", out)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("tbm_handshake did not time out")
	}
}

func TestChannelMethods(t *testing.T) {
	r := newRig(t)
	if out := r.call("join_channel", `{"name":"tbtest","password":"pw"}`); out["error"] != nil {
		t.Fatalf("%v", out)
	}
	r.expectBody(opcode.CMSGJoinChannel, packet.NewWriter().CString("tbtest").CString("pw").Bytes())
	if out := r.call("chat", `{"type":"channel","to":"tbtest","text":"hi"}`); out["error"] != nil {
		t.Fatalf("%v", out)
	}
	r.expectBody(opcode.CMSGMessagechat, packet.NewWriter().U32(0x0E).U32(0).CString("tbtest").CString("hi").Bytes())
	if out := r.call("leave_channel", `{"name":"tbtest"}`); out["error"] != nil {
		t.Fatalf("%v", out)
	}
	r.expectBody(opcode.CMSGLeaveChannel, packet.NewWriter().CString("tbtest").Bytes())
	if out := r.call("join_channel", `{}`); errCode(out) != rpc.CodeGame {
		t.Fatalf("%v", out)
	}
}

func TestGossipMethods(t *testing.T) {
	r := newRig(t)
	const npc = "0xF130001F4A000123"
	got := make(chan map[string]any, 1)
	go func() { got <- r.call("gossip_hello", `{"guid":"`+npc+`","wait_ms":2000}`) }()
	r.expectBody(opcode.CMSGGossipHello, packet.NewWriter().GUID(0xF130001F4A000123).Bytes())
	r.srv.Send(opcode.SMSGGossipMessage, gametest.GossipMessage(0xF130001F4A000123, 907,
		[]game.GossipOption{{Text: "Hire a companion"}, {Icon: 6, Coded: true, Text: "Enter a code"}}, nil))
	r.expect(opcode.CMSGCreatureQuery)
	r.srv.Send(opcode.SMSGCreatureQueryResponse, packet.NewWriter().U32(0x1F4A).CString("Bot Recruiter").U8(0).U8(0).U8(0).CString("").
		U32(0).U32(7).U32(0).U32(0).U32(0).U32(0).U32(1).U8(0).U8(0).Bytes())
	out := <-got
	ev, ok := out["result"].(map[string]any)["event"].(map[string]any)
	if !ok || ev["kind"] != "gossip" || ev["source"] != "Bot Recruiter" {
		t.Fatalf("%v", out)
	}
	opts := ev["data"].(map[string]any)["options"].([]any)
	if len(opts) != 2 || opts[1].(map[string]any)["coded"] != true || opts[0].(map[string]any)["text"] != "Hire a companion" {
		t.Fatalf("%v", opts)
	}
	go func() { got <- r.call("gossip_select", `{"guid":"`+npc+`","option":1,"code":"42","wait_ms":2000}`) }()
	r.expectBody(opcode.CMSGGossipSelectOption, packet.NewWriter().GUID(0xF130001F4A000123).U32(1).CString("42").Bytes())
	r.srv.Send(opcode.SMSGGossipComplete, nil)
	if ev, ok := (<-got)["result"].(map[string]any)["event"].(map[string]any); !ok || ev["kind"] != "gossip_complete" {
		t.Fatalf("%v", ev)
	}
	if out := r.call("gossip_select", `{"guid":"`+npc+`","option":0}`); errCode(out) != rpc.CodeGame {
		t.Fatalf("select after complete: %v", out)
	}
	// No wait: the result has the cursor and no event.
	out = r.call("gossip_hello", `{"guid":"`+npc+`"}`)
	if res := out["result"].(map[string]any); res["event"] != nil || res["since"] == nil {
		t.Fatalf("%v", out)
	}
	r.expect(opcode.CMSGGossipHello)
}

// Without wait_ms the result is only the cursor; with it, the flight master's answer
// rides along, and a 1-node route is a game error with nothing sent.
func TestTaxiMethod(t *testing.T) {
	r := newRig(t)
	const master = "0xF1300008C1000042"
	ev := resultEvent(t, r, "taxi", `{"guid":"`+master+`","nodes":[2,6],"wait_ms":2000}`, func() {
		r.expectBody(opcode.CMSGActivatetaxi, game.BuildActivateTaxi(0xF1300008C1000042, 2, 6))
		r.srv.Send(opcode.SMSGActivatetaxireply, packet.NewWriter().U32(3).Bytes())
	})
	if d := ev["data"].(map[string]any); ev["kind"] != "taxi_reply" || d["reason"] != "not_enough_money" || d["result"] != float64(3) {
		t.Fatalf("%v", ev)
	}
	out := r.call("taxi", `{"guid":"`+master+`","nodes":[2,5,6]}`)
	if res := out["result"].(map[string]any); out["error"] != nil || res["event"] != nil || res["since"] == nil {
		t.Fatalf("%v", out)
	}
	r.expectBody(opcode.CMSGActivatetaxiexpress, game.BuildActivateTaxiExpress(0xF1300008C1000042, []uint32{2, 5, 6}))
	if out := r.call("taxi", `{"guid":"`+master+`","nodes":[2]}`); errCode(out) != rpc.CodeGame {
		t.Fatalf("single node: %v", out)
	}
}
