package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/pjerra/turtle-agent/internal/game"
	"github.com/pjerra/turtle-agent/internal/game/gametest"
	"github.com/pjerra/turtle-agent/internal/objects"
	"github.com/pjerra/turtle-agent/internal/opcode"
	"github.com/pjerra/turtle-agent/internal/packet"
	"github.com/pjerra/turtle-agent/internal/rpc"
	"github.com/pjerra/turtle-agent/internal/session"
)

// addonText builds the addon chat body the method must send (a build error would be a
// test bug: the strings are constants).
func addonText(t *testing.T, typ game.ChatType, text string) []byte {
	t.Helper()
	body, err := game.BuildAddonMessage(typ, "TW_LFG", text, "")
	if err != nil {
		t.Fatal(err)
	}
	return body
}

// setClass gives the rig's own character a class row (UNIT_FIELD_BYTES_0) and waits until
// the store has it: the LFT methods read the class to mirror the core's role mask.
func (r *agentRig) setClass(class uint8) {
	r.t.Helper()
	r.selfValues(map[uint16]uint32{objects.UnitBytes0: uint32(class) << 8})
	r.eventually("unit", `{"guid":"0x2A"}`, func(m map[string]any) bool {
		res, _ := m["result"].(map[string]any)
		return res != nil && res["class"] == float64(class)
	})
}

// lfgPhase reads the phase from an lfg_* result.
func lfgPhase(t *testing.T, out map[string]any) string {
	t.Helper()
	res, ok := out["result"].(map[string]any)
	if !ok {
		t.Fatalf("no result: %v", out)
	}
	st, ok := res["state"].(map[string]any)
	if !ok {
		t.Fatalf("no state: %v", out)
	}
	phase, _ := st["phase"].(string)
	return phase
}

// lfgAddon is one S2C line as the core sends it: CHAT_MSG_GUILD in LANG_ADDON from the
// player himself (Player::SendAddonMessage, Player.cpp:25604-25612).
func (r *agentRig) lfgAddon(text string) {
	r.t.Helper()
	r.srv.Send(opcode.SMSGMessagechat, gametest.AddonMessage(0x2A, "TW_LFG", text, 0))
}

// The join sends exactly the addon message Turtle's LFG addon sends on the guild channel,
// and the answer's role check becomes the state.
func TestLFGJoinSendsTheAddonLine(t *testing.T) {
	r := newRig(t)
	r.setClass(1) // a warrior may tank and damage
	res := make(chan map[string]any, 1)
	go func() { res <- r.call("lfg_join", `{"instances":["ragefirechasm"],"roles":"t","wait_ms":3000}`) }()
	r.expectBody(opcode.CMSGMessagechat, addonText(t, game.ChatGuild, "C2S_QUEUE_JOIN;ragefirechasm;t"))
	r.lfgAddon("S2C_ROLECHECK_START;ragefirechasm")
	r.lfgAddon("S2C_UPDATE_QUEUE_STATUS;pending_rolecheck")
	out := <-res
	if out["error"] != nil {
		t.Fatalf("%v", out)
	}
	if got := lfgPhase(t, out); got != "rolecheck" {
		t.Fatalf("phase %q", got)
	}
	result := out["result"].(map[string]any)
	if result["sent"] != "C2S_QUEUE_JOIN;ragefirechasm;t" {
		t.Fatalf("sent %v", result["sent"])
	}
	state := result["state"].(map[string]any)
	if inst, _ := state["instances"].([]any); len(inst) != 1 || inst[0] != "ragefirechasm" {
		t.Fatalf("instances %v", state["instances"])
	}
}

func TestLFGRoleAnswerRefusedOutOfPhase(t *testing.T) {
	r := newRig(t)
	r.setClass(1)
	out := r.call("lfg_role_check_answer", `{"roles":"t","wait_ms":100}`)
	if out["error"] == nil || !strings.Contains(out["error"].(map[string]any)["message"].(string), "rolecheck") {
		t.Fatalf("%v", out)
	}
}

func TestLFGRoleAnswerRefusesClassMask(t *testing.T) {
	r := newRig(t)
	r.setClass(5) // a priest may heal and damage, not tank
	r.lfgAddon("S2C_ROLECHECK_START;ragefirechasm")
	r.eventually("lfg_status", "{}", func(m map[string]any) bool { return lfgPhase(t, m) == "rolecheck" })
	out := r.call("lfg_role_check_answer", `{"roles":"t","wait_ms":200}`)
	if out["error"] == nil {
		t.Fatalf("priest tank answer was accepted: %v", out)
	}
	if msg := out["error"].(map[string]any)["message"].(string); !strings.Contains(msg, `roles "t" are not allowed for a priest (allowed: hd)`) {
		t.Fatalf("message %q", msg)
	}
	res := make(chan map[string]any, 1)
	go func() { res <- r.call("lfg_role_check_answer", `{"roles":"t","force":true,"wait_ms":3000}`) }()
	r.expectBody(opcode.CMSGMessagechat, addonText(t, game.ChatGuild, "C2S_ROLECHECK_RESPONSE;t"))
	r.lfgAddon("S2C_ROLECHECK_INFO;Harnessb;t")
	if out := <-res; out["error"] != nil {
		t.Fatalf("%v", out)
	}
}

func TestLFGProposalAcceptAndDecline(t *testing.T) {
	r := newRig(t)
	r.setClass(1)
	offer := func() {
		r.lfgAddon("S2C_OFFER_NEW;ragefirechasm;t")
		r.eventually("lfg_status", "{}", func(m map[string]any) bool { return lfgPhase(t, m) == "offer" })
	}
	offer()
	res := make(chan map[string]any, 1)
	go func() { res <- r.call("lfg_proposal_answer", `{"accept":true,"wait_ms":3000}`) }()
	r.expectBody(opcode.CMSGMessagechat, addonText(t, game.ChatGuild, "C2S_OFFER_ACCEPT"))
	r.lfgAddon("S2C_OFFER_COMPLETE")
	out := <-res
	if out["error"] != nil || lfgPhase(t, out) != "grouped" || out["result"].(map[string]any)["sent"] != "C2S_OFFER_ACCEPT" {
		t.Fatalf("%v", out)
	}

	offer() // the core offered again (a new listing this time)
	res = make(chan map[string]any, 1)
	go func() { res <- r.call("lfg_proposal_answer", `{"accept":false,"wait_ms":3000}`) }()
	r.expectBody(opcode.CMSGMessagechat, addonText(t, game.ChatGuild, "C2S_QUEUE_LEAVE"))
	r.lfgAddon("S2C_QUEUE_LEFT;Harnessb")
	out = <-res
	if out["error"] != nil || out["result"].(map[string]any)["sent"] != "C2S_QUEUE_LEAVE" || lfgPhase(t, out) != "left" {
		t.Fatalf("%v", out)
	}
}

// A guild-mate can forge the same line; only the line the server sends from the player
// himself counts (decision 2 of the spec).
func TestLFGSpoofedLineIgnored(t *testing.T) {
	r := newRig(t)
	r.setClass(1)
	r.srv.Send(opcode.SMSGMessagechat, gametest.AddonMessage(0x99, "TW_LFG", "S2C_OFFER_NEW;ragefirechasm;t", 0))
	// The session names an unknown sender before it logs the line.
	r.expect(opcode.CMSGNameQuery)
	r.srv.Send(opcode.SMSGNameQueryResponse, packet.NewWriter().GUID(0x99).CString("Gorm").U8(0).U32(1).U32(0).U32(1).Bytes())
	// Wait until the forged line is in the client's log, so the check is about the guard
	// and not about timing.
	r.eventually("events", `{"since":0,"kinds":["addon"],"wait_ms":1000}`, func(m map[string]any) bool {
		page, _ := m["result"].(map[string]any)
		ev, _ := page["events"].([]any)
		return len(ev) > 0
	})
	out := r.call("lfg_status", "{}")
	if got := lfgPhase(t, out); got != "idle" {
		t.Fatalf("a spoofed offer changed the phase to %q", got)
	}
}

func TestLFGAutoAnswers(t *testing.T) {
	r := newRig(t)
	r.setClass(1)
	out := r.call("lfg_auto", `{"roles":"d","accept":true,"delay_ms":0}`)
	if out["error"] != nil {
		t.Fatalf("%v", out)
	}
	if auto := out["result"].(map[string]any)["auto"].(map[string]any); auto["on"] != true || auto["delay_ms"] != float64(0) {
		t.Fatalf("auto %v", auto)
	}
	r.lfgAddon("S2C_ROLECHECK_START;ragefirechasm")
	r.expectBody(opcode.CMSGMessagechat, addonText(t, game.ChatGuild, "C2S_ROLECHECK_RESPONSE;d"))
	r.lfgAddon("S2C_OFFER_NEW;ragefirechasm;d")
	r.expectBody(opcode.CMSGMessagechat, addonText(t, game.ChatGuild, "C2S_OFFER_ACCEPT"))
	if out := r.call("lfg_auto", `{"off":true}`); out["error"] != nil {
		t.Fatalf("%v", out)
	}
	r.lfgAddon("S2C_ROLECHECK_START;ragefirechasm")
	if p, err := r.srv.Expect(opcode.CMSGMessagechat, 1*time.Second); err == nil {
		t.Fatalf("the stopped auto answered: % X", p.Body)
	}
}

// lfgListings keeps the list and one listing's detail, and a listed group's role answer
// goes out on the party channel with C2C_ROLECHECK_RESPONSE.
func TestLFGListings(t *testing.T) {
	r := newRig(t)
	r.setClass(1)
	got := make(chan map[string]any, 1)
	go func() { got <- r.call("lfg_listings", `{"refresh":true,"wait_ms":3000}`) }()
	r.expectBody(opcode.CMSGMessagechat, addonText(t, game.ChatGuild, "C2S_GET_GROUPS_LIST"))
	r.lfgAddon("S2C_GROUPS_LIST_UPDATE;start")
	r.lfgAddon(`S2C_GROUPS_LIST_UPDATE;{"id":7,"creator":"Gorm","class":"WARRIOR","level":60,"title":"RFC tank","description":"a;b","category":1,"limit":[1,1,3],"numConfirmed":[0,0,0]}`)
	r.lfgAddon("S2C_GROUPS_LIST_UPDATE;end")
	out := <-got
	if out["error"] != nil {
		t.Fatalf("%v", out)
	}
	listings := out["result"].(map[string]any)["state"].(map[string]any)["listings"].(map[string]any)
	if listing := listings["7"].(map[string]any); listing["title"] != "RFC tank" || listing["description"] != "a;b" {
		t.Fatalf("listing %v", listing)
	}

	got = make(chan map[string]any, 1)
	go func() { got <- r.call("lfg_listings", `{"id":7,"wait_ms":3000}`) }()
	r.expectBody(opcode.CMSGMessagechat, addonText(t, game.ChatGuild, "C2S_GET_GROUP_DETAILS;7"))
	r.lfgAddon(`S2C_UPDATE_GROUP;7;start;{"id":7,"creator":"Gorm","class":"WARRIOR","level":60,"title":"RFC tank","description":"","category":1,"limit":[1,1,3],"numConfirmed":[0,0,0]}`)
	r.lfgAddon(`S2C_UPDATE_GROUP;7;1;{"name":"Gorm","class":"WARRIOR","level":60,"confirmed":true}`)
	r.lfgAddon("S2C_UPDATE_GROUP;7;end")
	out = <-got
	if out["error"] != nil {
		t.Fatalf("%v", out)
	}
	signups := out["result"].(map[string]any)["state"].(map[string]any)["listings"].(map[string]any)["7"].(map[string]any)["signups"].([]any)
	if len(signups) != 1 || signups[0].(map[string]any)["role"] != "t" {
		t.Fatalf("signups %v", signups)
	}

	got = make(chan map[string]any, 1)
	go func() { got <- r.call("lfg_listing_role", `{"role":"t","wait_ms":3000}`) }()
	r.expectBody(opcode.CMSGMessagechat, addonText(t, game.ChatParty, "C2C_ROLECHECK_RESPONSE;t"))
	r.lfgAddon("S2C_GROUPS_LIST_UPDATE;end")
	if out := <-got; out["error"] != nil || out["result"].(map[string]any)["sent"] != "C2C_ROLECHECK_RESPONSE;t" {
		t.Fatalf("%v", out)
	}
}

// A new session behind the same RPC address starts from an empty group-finder state and
// cursor (decision 4).
func TestLFGStateResetsOnNewSession(t *testing.T) {
	r1 := newRig(t)
	r2 := newRig(t)
	var mu sync.Mutex
	cur := r1.s
	srv := rpc.NewServer()
	srv.DisableAuth()
	stop := registerLFGMethods(srv, func() *session.Session { mu.Lock(); defer mu.Unlock(); return cur })
	t.Cleanup(stop)
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)

	call := func(method, params string) map[string]any {
		t.Helper()
		body := `{"jsonrpc":"2.0","id":1,"method":"` + method + `","params":` + params + `}`
		resp, err := http.Post(ts.URL+"/rpc", "application/json", strings.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		var out map[string]any
		if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
			t.Fatal(err)
		}
		return out
	}

	r1.lfgAddon("S2C_ROLECHECK_START;ragefirechasm")
	r1.eventually("events", `{"since":0,"kinds":["addon"],"wait_ms":1000}`, func(m map[string]any) bool {
		page, _ := m["result"].(map[string]any)
		ev, _ := page["events"].([]any)
		return len(ev) > 0
	})
	if out := call("lfg_status", "{}"); lfgPhase(t, out) != "rolecheck" {
		t.Fatalf("first session %v", out)
	}
	mu.Lock()
	cur = r2.s
	mu.Unlock()
	if out := call("lfg_status", "{}"); lfgPhase(t, out) != "idle" {
		t.Fatalf("new session kept the old state: %v", out)
	}
}
