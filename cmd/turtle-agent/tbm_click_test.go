package main

import (
	"strings"
	"testing"
	"time"

	"github.com/pjerra/turtle-agent/internal/game"
	"github.com/pjerra/turtle-agent/internal/game/gametest"
	"github.com/pjerra/turtle-agent/internal/opcode"
	"github.com/pjerra/turtle-agent/internal/packet"
	"github.com/pjerra/turtle-agent/internal/rpc"
	"github.com/pjerra/turtle-agent/internal/tbm"
)

func (r *agentRig) system(lines ...string) {
	for _, s := range lines {
		r.srv.Send(opcode.SMSGMessagechat, gametest.ChatPacket(game.ChatSystem, 0, 0, "", 0, "", s, 0))
	}
}

func (r *agentRig) addon(lines ...string) {
	for _, s := range lines {
		r.srv.Send(opcode.SMSGMessagechat, gametest.AddonMessage(0x2A, "TBM", s, 0))
	}
}

func (r *agentRig) addonBody(text string) []byte {
	return packet.NewWriter().U32(1).U32(game.LangAddon).CString("TBM\t" + text).Bytes()
}

func (r *agentRig) groupWith(names ...string) {
	var ms []game.GroupMember
	for i, n := range names {
		ms = append(ms, game.GroupMember{Name: n, GUID: packet.GUID(0x30 + i), Status: game.MemberOnline})
	}
	r.srv.Send(opcode.SMSGGroupList, gametest.GroupList(0x2A, ms...))
	r.expect(opcode.CMSGRequestPartyMemberStats)
}

func result(t *testing.T, ch chan map[string]any) map[string]any {
	t.Helper()
	select {
	case out := <-ch:
		if out["error"] != nil {
			t.Fatalf("%v", out)
		}
		return out["result"].(map[string]any)
	case <-time.After(5 * time.Second):
		t.Fatal("no answer")
		return nil
	}
}

// noRosterThrottle lets a test poll the roster as often as it likes.
func noRosterThrottle(t *testing.T) {
	old := tbm.RosterThrottle
	tbm.RosterThrottle = 0
	t.Cleanup(func() { tbm.RosterThrottle = old })
}

func TestTBMButtonsMethod(t *testing.T) {
	r := newRig(t)
	res := r.call("tbm_buttons", `{}`)["result"].(map[string]any)
	if res["addon_commit"] == "" || len(res["buttons"].([]any)) < 40 || res["send_delay_ms"] != float64(350) {
		t.Fatalf("%v", res)
	}
	camp := r.call("tbm_buttons", `{"tab":"camp"}`)["result"].(map[string]any)["buttons"].([]any)
	for _, b := range camp {
		if b.(map[string]any)["tab"] != "Camp" {
			t.Fatalf("%v", b)
		}
	}
	if len(camp) < 8 {
		t.Fatalf("camp buttons %d", len(camp))
	}
}

// Not grouped: the click goes out as ".bot" chat and the ACK is read from system messages.
func TestTBMClickUngroupedUsesChat(t *testing.T) {
	r := newRig(t)
	ch := r.callAsync("tbm_click", `{"button":"Camp.Rest","wait_ms":3000}`)
	r.expectBody(opcode.CMSGMessagechat, say(".bot action rest"))
	r.addon("TBM:ACTION_ACK|rest|party|1|-") // an addon line is not the reply to a chat request
	r.system("TBM:ACTION_ACK|sell|party|1|-", "TBM:ACTION_ACK|rest|party|2|-")
	res := result(t, ch)
	sent := res["sent"].(map[string]any)
	reply := res["reply"].(map[string]any)
	if sent["transport"] != "chat" || sent["text"] != ".bot action rest" || res["scope"] != "party" || res["refreshed"] != nil {
		t.Fatalf("%v", res)
	}
	if reply["kind"] != "ack" || reply["ok"] != true || reply["ack"].(map[string]any)["count"] != float64(2) {
		t.Fatalf("%v", reply)
	}
	if lines := reply["lines"].([]any); len(lines) != 2 {
		t.Fatalf("lines %v", lines)
	}
}

// Grouped with no verdict: a roster poll over chat first, then the click on the addon channel
// the module advertised; the ACK arrives as an addon message.
func TestTBMClickGroupedUsesAddonChannel(t *testing.T) {
	noRosterThrottle(t)
	r := newRig(t)
	r.groupWith("Gorm")
	ch := r.callAsync("tbm_click", `{"button":"Fight.AoE","wait_ms":3000,"follow_up":false}`)
	r.expectBody(opcode.CMSGMessagechat, say(".bot roster"))
	r.system("TBM:ROSTER_BEGIN|1", "TBM:ROSTER|7|Gorm|1|online|1|-", "TBM:ROSTER_END", "TBM:BOTSTATE_BEGIN|1",
		"TBM:BOTSTATE|Gorm|move=follow,aoe=on", "TBM:BOTSTATE_END", "TBM:CAPS|pull-seconds", "TBM:TRANSPORT|party")
	r.expectBody(opcode.CMSGMessagechat, r.addonBody("action aoe off")) // on -> off
	r.system("TBM:ACTION_ACK|aoe|party|1|off")                          // wrong transport: ignored
	r.addon("TBM:ACTION_ACK|aoe|party|1|off")
	res := result(t, ch)
	sent := res["sent"].(map[string]any)
	if sent["transport"] != "addon" || sent["text"] != "action aoe off" || sent["channel"] != "PARTY" || sent["prefix"] != "TBM" ||
		res["refreshed"] != true {
		t.Fatalf("%v", res)
	}
	if reply := res["reply"].(map[string]any); reply["kind"] != "ack" || reply["ack"].(map[string]any)["executor"] != "off" {
		t.Fatalf("%v", reply)
	}

	// The ACK said the switch is off now, so the next click flips from that without a new roster
	// (and a poll this soon would break the addon's 5 s throttle).
	ch = r.callAsync("tbm_click", `{"button":"Fight.AoE","wait_ms":3000,"follow_up":false}`)
	r.expectBody(opcode.CMSGMessagechat, r.addonBody("action aoe on"))
	r.addon("TBM:ACTION_ACK|aoe|party|1|on")
	if res := result(t, ch); res["reply"].(map[string]any)["kind"] != "ack" {
		t.Fatalf("%v", res)
	}
}

// A battleground group (the module says none), or a group that changed since the verdict,
// keeps the chat transport.
func TestTBMClickBattlegroundGroupAndGroupChange(t *testing.T) {
	noRosterThrottle(t)
	r := newRig(t)
	r.groupWith("Gorm")
	ch := r.callAsync("tbm_click", `{"button":"Camp.Rest","wait_ms":2000}`)
	r.expectBody(opcode.CMSGMessagechat, say(".bot roster"))
	r.system("TBM:TRANSPORT|none")
	r.expectBody(opcode.CMSGMessagechat, say(".bot action rest"))
	r.system("TBM:ACTION_ACK|rest|party|1|-")
	if res := result(t, ch); res["sent"].(map[string]any)["transport"] != "chat" {
		t.Fatalf("%v", res)
	}

	r = newRig(t)
	r.groupWith("Gorm")
	ch = r.callAsync("tbm_handshake", `{"wait_ms":2000}`)
	r.expectBody(opcode.CMSGMessagechat, say(".bot help"))
	r.expectBody(opcode.CMSGMessagechat, say(".bot roster"))
	r.system("TBM:TRANSPORT|party")
	result(t, ch)
	ch = r.callAsync("tbm_click", `{"button":"Camp.Rest","wait_ms":2000,"follow_up":false}`)
	r.expectBody(opcode.CMSGMessagechat, r.addonBody("action rest"))
	r.addon("TBM:ACTION_ACK|rest|party|1|-")
	if res := result(t, ch); res["sent"].(map[string]any)["transport"] != "addon" || res["refreshed"] != nil {
		t.Fatalf("the handshake verdict was not used: %v", res)
	}
	r.groupWith("Gorm", "Lia") // PARTY_MEMBERS_CHANGED: the verdict is void until the next roster
	ch = r.callAsync("tbm_click", `{"button":"Camp.Rest","wait_ms":2000,"follow_up":false,"transport":"chat"}`)
	r.expectBody(opcode.CMSGMessagechat, say(".bot action rest"))
	r.system("TBM:ACTION_ACK|rest|party|2|-")
	if res := result(t, ch); res["transport_reason"] != "forced by the caller" {
		t.Fatalf("%v", res)
	}
	ch = r.callAsync("tbm_click", `{"button":"Camp.Rest","wait_ms":2000,"follow_up":false}`)
	r.expectBody(opcode.CMSGMessagechat, say(".bot roster")) // stale verdict: poll over chat, as the addon would
	r.system("TBM:TRANSPORT|party")
	r.expectBody(opcode.CMSGMessagechat, r.addonBody("action rest"))
	r.addon("TBM:ACTION_ACK|rest|party|2|-")
	if res := result(t, ch); res["sent"].(map[string]any)["transport"] != "addon" || res["refreshed"] != true {
		t.Fatalf("%v", res)
	}
}

func TestTBMClickErrors(t *testing.T) {
	r := newRig(t)
	if out := r.call("tbm_click", `{"button":"Fight.Nope"}`); errCode(out) != rpc.CodeGame {
		t.Fatalf("%v", out)
	}
	if out := r.call("tbm_click", `{"button":"Fight.Attack"}`); errCode(out) != rpc.CodeGame {
		t.Fatalf("no enemy target must refuse: %v", out)
	}
	if out := r.call("tbm_click", `{"button":"Fight.Stay","transport":"pigeon"}`); errCode(out) != rpc.CodeGame {
		t.Fatalf("%v", out)
	}
	if out := r.call("tbm_click", `{"button":"Roster.Login"}`); errCode(out) != rpc.CodeGame {
		t.Fatalf("missing name must refuse: %v", out)
	}
	// force sends it, and the server's ERR is the reply.
	ch := r.callAsync("tbm_click", `{"button":"Fight.Attack","force":true,"wait_ms":2000}`)
	r.expectBody(opcode.CMSGMessagechat, say(".bot action attack"))
	r.system("TBM:ACTION_ERR|attack|no-target|Select a live non-bot target first.")
	reply := result(t, ch)["reply"].(map[string]any)
	if reply["kind"] != "err" || reply["ok"] != false || reply["err"].(map[string]any)["code"] != "no-target" {
		t.Fatalf("%v", reply)
	}
}

// No answer: the click still goes out and the reply says timeout with the lines seen.
func TestTBMClickTimeout(t *testing.T) {
	r := newRig(t)
	ch := r.callAsync("tbm_click", `{"button":"Fight.Stay","wait_ms":200}`)
	r.expectBody(opcode.CMSGMessagechat, say(".bot action stay"))
	reply := result(t, ch)["reply"].(map[string]any)
	if reply["kind"] != "timeout" || reply["ok"] != false || len(reply["lines"].([]any)) != 0 {
		t.Fatalf("%v", reply)
	}
}

// A lifecycle button is answered by a text line, then the addon's roster poll follows.
func TestTBMClickFollowUpRoster(t *testing.T) {
	r := newRig(t)
	ch := r.callAsync("tbm_click", `{"button":"Roster.Summon","args":{"name":"Gorm"},"wait_ms":3000}`)
	r.expectBody(opcode.CMSGMessagechat, say(".bot summon Gorm"))
	r.system("Summoning Gorm to a safe position near you (3s); it will follow on arrival.")
	r.expectBody(opcode.CMSGMessagechat, say(".bot roster"))
	r.system("TBM:ROSTER_BEGIN|1", "TBM:ROSTER|7|Gorm|1|online|1|-", "TBM:ROSTER_END", "TBM:TRANSPORT|none")
	res := result(t, ch)
	if res["reply"].(map[string]any)["kind"] != "text" || res["scope"] != nil {
		t.Fatalf("%v", res)
	}
	fu := res["follow_up"].(map[string]any)
	if fu["complete"] != true || len(fu["roster"].([]any)) != 1 {
		t.Fatalf("%v", fu)
	}
}

func TestTBMSendMethod(t *testing.T) {
	r := newRig(t)
	ch := r.callAsync("tbm_send", `{"command":".bot action stop","wait_ms":2000}`)
	r.expectBody(opcode.CMSGMessagechat, say(".bot action stop"))
	r.system("TBM:ACTION_ACK|stop|party|1|-")
	res := result(t, ch)
	if res["reply"].(map[string]any)["kind"] != "ack" || res["sent"].(map[string]any)["text"] != ".bot action stop" {
		t.Fatalf("%v", res)
	}
	if out := r.call("tbm_send", `{"command":""}`); errCode(out) != rpc.CodeGame {
		t.Fatalf("%v", out)
	}
}

// One request at a time: a second click waits for the first one's reply, so the first ACK
// cannot settle the second click.
func TestTBMClicksAreSerialized(t *testing.T) {
	r := newRig(t)
	a := r.callAsync("tbm_click", `{"button":"Fight.Stay","wait_ms":3000}`)
	r.expectBody(opcode.CMSGMessagechat, say(".bot action stay"))
	b := r.callAsync("tbm_click", `{"button":"Fight.Stay","wait_ms":3000}`)
	if _, err := r.srv.Expect(opcode.CMSGMessagechat, 700*time.Millisecond); err == nil {
		t.Fatal("the second click was sent while the first was still waiting")
	}
	r.system("TBM:ACTION_ACK|stay|party|1|-")
	if res := result(t, a); res["reply"].(map[string]any)["ack"].(map[string]any)["count"] != float64(1) {
		t.Fatalf("%v", res)
	}
	r.expectBody(opcode.CMSGMessagechat, say(".bot action stay"))
	r.system("TBM:ACTION_ACK|stay|party|2|-")
	if res := result(t, b); res["reply"].(map[string]any)["ack"].(map[string]any)["count"] != float64(2) {
		t.Fatalf("%v", res)
	}
}

// The ACK of a click that timed out arrives late: before the next click it is drained, and
// after the next click is sent it is passed over, so it never answers the wrong click.
func TestTBMLateAckOfATimedOutClick(t *testing.T) {
	r := newRig(t)
	ch := r.callAsync("tbm_click", `{"button":"Fight.Stay","wait_ms":200}`)
	r.expectBody(opcode.CMSGMessagechat, say(".bot action stay"))
	if reply := result(t, ch)["reply"].(map[string]any); reply["kind"] != "timeout" {
		t.Fatalf("%v", reply)
	}
	// Late, before the next click. The session logs the line on its own goroutine after the pipe took it, so wait until the log has
	// it: the next click's drain must find it there, as it does for an ACK that really was late.
	lastSeq := r.s.Log().Last()
	r.system("TBM:ACTION_ACK|stay|party|1|-")
	waitUntil(t, func() bool { return r.s.Log().Last() > lastSeq })
	ch = r.callAsync("tbm_click", `{"button":"Fight.Stay","wait_ms":3000}`)
	r.expectBody(opcode.CMSGMessagechat, say(".bot action stay"))
	r.system("TBM:ACTION_ACK|stay|party|2|-")
	if res := result(t, ch); res["reply"].(map[string]any)["ack"].(map[string]any)["count"] != float64(2) {
		t.Fatalf("%v", res)
	}

	// Late after the next click went out: the first ACK of the intent belongs to the timed-out one.
	ch = r.callAsync("tbm_click", `{"button":"Fight.Follow","wait_ms":200}`)
	r.expectBody(opcode.CMSGMessagechat, say(".bot action follow"))
	result(t, ch)
	ch = r.callAsync("tbm_click", `{"button":"Fight.Follow","wait_ms":3000}`)
	r.expectBody(opcode.CMSGMessagechat, say(".bot action follow"))
	r.system("TBM:ACTION_ACK|follow|party|1|-", "TBM:ACTION_ACK|follow|party|2|-")
	if res := result(t, ch); res["reply"].(map[string]any)["ack"].(map[string]any)["count"] != float64(2) {
		t.Fatalf("%v", res)
	}
}

// Raid status is one ACK per bot with no end marker: the reply keeps them all and closes on a
// quiet gap, well before wait_ms.
func TestTBMClickMultiAck(t *testing.T) {
	r := newRig(t)
	start := time.Now()
	ch := r.callAsync("tbm_click", `{"button":"Raid.CheckBots","wait_ms":10000}`)
	r.expectBody(opcode.CMSGMessagechat, say(".bot action raid status"))
	r.system("TBM:ACTION_ACK|raid status|bot:Gorm|1|molten core", "TBM:ACTION_ACK|raid status|bot:Lia|1|outdoor")
	reply := result(t, ch)["reply"].(map[string]any)
	if acks := reply["acks"].([]any); reply["kind"] != "ack" || len(acks) != 2 || len(reply["lines"].([]any)) != 2 {
		t.Fatalf("%v", reply)
	}
	if d := time.Since(start); d > 5*time.Second {
		t.Fatalf("waited %v", d)
	}
}

// Without a verdict a grouped click would poll the roster first, but the addon does not poll
// again within 5 s of the last roster request.
func TestTBMClickRespectsTheRosterThrottle(t *testing.T) {
	r := newRig(t)
	r.groupWith("Gorm")
	ch := r.callAsync("tbm_handshake", `{"wait_ms":2000}`)
	r.expectBody(opcode.CMSGMessagechat, say(".bot help"))
	r.expectBody(opcode.CMSGMessagechat, say(".bot roster"))
	r.system("TBM:TRANSPORT|none")
	result(t, ch)
	r.groupWith("Gorm", "Lia") // the verdict is void now
	ch = r.callAsync("tbm_click", `{"button":"Camp.Rest","wait_ms":2000,"follow_up":false}`)
	r.expectBody(opcode.CMSGMessagechat, say(".bot action rest")) // no roster in between
	r.system("TBM:ACTION_ACK|rest|party|2|-")
	res := result(t, ch)
	if res["refreshed"] != nil || res["sent"].(map[string]any)["transport"] != "chat" || !strings.Contains(res["transport_reason"].(string), "throttle") {
		t.Fatalf("%v", res)
	}
}

// Flee needs the module's flee capability, as the addon greys it out without it.
func TestTBMClickFleeNeedsItsCapability(t *testing.T) {
	r := newRig(t)
	ch := r.callAsync("tbm_click", `{"button":"Fight.Flee","wait_ms":2000}`)
	r.expectBody(opcode.CMSGMessagechat, say(".bot roster")) // no capabilities known yet: poll
	r.system("TBM:CAPS|pull-seconds", "TBM:TRANSPORT|none")
	select {
	case out := <-ch:
		if errCode(out) != rpc.CodeGame {
			t.Fatalf("%v", out)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("no answer")
	}
	noRosterThrottle(t)
	ch = r.callAsync("tbm_click", `{"button":"Fight.Flee","wait_ms":2000}`)
	r.expectBody(opcode.CMSGMessagechat, say(".bot roster"))
	r.system("TBM:CAPS|pull-seconds,flee", "TBM:TRANSPORT|none")
	r.expectBody(opcode.CMSGMessagechat, say(".bot action flee"))
	r.system("TBM:ACTION_ACK|flee|party|1|-")
	if res := result(t, ch); res["reply"].(map[string]any)["kind"] != "ack" {
		t.Fatalf("%v", res)
	}
}
