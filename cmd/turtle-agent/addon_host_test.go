package main

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/TortoiseBots/turtle-agent/internal/opcode"
	"github.com/TortoiseBots/turtle-agent/internal/session"
	"github.com/TortoiseBots/turtle-agent/internal/tbm"
)

// testAddonDir is the real addon for the end-to-end tests: $TBM_ADDON_DIR or the copy
// tools/tbmcontract/fetch.sh keeps. Without it or lua5.1 the tests skip, unless
// TBM_ADDON_HOST_STRICT=1 (make addon-host) makes that a failure.
func testAddonDir(t *testing.T) string {
	t.Helper()
	skip := func(why string) {
		if os.Getenv("TBM_ADDON_HOST_STRICT") == "1" {
			t.Fatal(why)
		}
		t.Skip(why)
	}
	if _, err := exec.LookPath("lua5.1"); err != nil {
		skip("lua5.1 not installed")
	}
	dir := os.Getenv("TBM_ADDON_DIR")
	if dir == "" {
		dir = filepath.Join("..", "..", ".cache", "tbmcontract", "addon-"+tbm.AddonCommit)
	}
	if _, err := os.Stat(filepath.Join(dir, "TortoiseBotsManager.toc")); err != nil {
		skip("no addon checkout (run tools/tbmcontract/fetch.sh or make addon-host)")
	}
	abs, _ := filepath.Abs(dir)
	return abs
}

func (r *agentRig) ok(method, params string) map[string]any {
	r.t.Helper()
	out := r.call(method, params)
	if out["error"] != nil {
		r.t.Fatalf("%s %s: %v", method, params, out["error"])
	}
	res, _ := out["result"].(map[string]any)
	return res
}

// addonStateUntil polls addon_state until cond holds: the fake server's packets reach the log
// on the session goroutine, a moment after they were sent.
func (r *agentRig) addonStateUntil(what string, cond func(st map[string]any) bool) map[string]any {
	r.t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for {
		st := r.ok("addon_state", `{}`)
		if cond(st) {
			return st
		}
		if time.Now().After(deadline) {
			b, _ := json.MarshalIndent(st, "", " ")
			r.t.Fatalf("addon never showed %s; state:\n%s", what, b)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func switchOf(st map[string]any, key string) map[string]any {
	for _, s := range st["switches"].([]any) {
		if m := s.(map[string]any); m["key"] == key {
			return m
		}
	}
	return nil
}

// addonRig is a rig whose settings point addon_start at the addon checkout dir.
func addonRig(t *testing.T, dir string) *agentRig {
	return newRigWith(t, session.Options{}, [3]float32{1, 2, 3}, methodOptions{AddonDir: dir})
}

func addonStart(r *agentRig, clock string) map[string]any {
	b, _ := json.Marshal(map[string]string{"clock": clock})
	return r.ok("addon_start", string(b))
}

// The issue's done-criterion, offline: hire a bot (the server groups it and reports it in
// the roster's BOTSTATE), the addon shows it; click Fight AoE through the addon's own OnClick,
// the command goes out on the addon channel, the server's ACK turns the lamp off, and the next
// roster agrees.
func TestAddonHostHireAndToggleAoE(t *testing.T) {
	dir := testAddonDir(t)
	r := addonRig(t, dir)
	start := addonStart(r, "manual")
	if sent := start["sent"].([]any); len(sent) != 1 || sent[0].(map[string]any)["text"] != ".bot help" {
		t.Fatalf("handshake %v", start)
	}
	r.expectBody(opcode.CMSGMessagechat, say(".bot help")) // PLAYER_ENTERING_WORLD: the capabilities

	// The hire: the server invites the bot into the player's group.
	r.ok("chat", `{"type":"say","text":".bot hire warrior"}`)
	r.expectBody(opcode.CMSGMessagechat, say(".bot hire warrior"))
	r.groupWith("Gorm")
	r.addonStateUntil("the group", func(st map[string]any) bool { return st["server"].(map[string]any)["transport"] == nil })

	// The addon polls the roster: its 1 s auto poll (the last poll is "never"), and again 3.5 s
	// after entering the world. With no verdict for this group it still uses chat.
	tick := r.ok("addon_tick", `{"ms":3600}`)
	sent := tick["sent"].([]any)
	if len(sent) != 2 {
		t.Fatalf("polls %v", tick)
	}
	for _, s := range sent {
		if s.(map[string]any)["text"] != ".bot roster" || s.(map[string]any)["channel"] != "SAY" {
			t.Fatalf("poll %v", s)
		}
		r.expectBody(opcode.CMSGMessagechat, say(".bot roster"))
	}
	r.system("TBM:VERSION|2026-10-12-v17", "TBM:ROSTER_BEGIN|0", "TBM:ROSTER_END", "TBM:CC_ASSIGN_BEGIN|0", "TBM:CC_ASSIGN_END",
		"TBM:BOTSTATE_BEGIN|1", "TBM:BOTSTATE|Gorm|move=follow,loot=on,aoe=on,autocc=off", "TBM:BOTSTATE_END",
		"TBM:CAPS|pull-seconds,flee,inventory,behavior", "TBM:TRANSPORT|party")
	st := r.addonStateUntil("the hired bot", func(st map[string]any) bool {
		return st["server"].(map[string]any)["transport"] == "PARTY"
	})
	if bots := st["bots"].(map[string]any); bots["Gorm"] == nil || bots["Gorm"].(map[string]any)["aoe"] != "on" {
		t.Fatalf("bots %v", bots)
	}
	if aoe := switchOf(st, "aoe"); aoe["lamp"] != "green" || aoe["state"] != "on" || aoe["text"] != "AoE On" {
		t.Fatalf("aoe %v", aoe)
	}
	for _, line := range st["chat"].([]any) {
		if strings.Contains(line.(map[string]any)["text"].(string), "TBM:") {
			t.Fatalf("a protocol line reached the chat frame: %v", line)
		}
	}
	r.ok("addon_tick", `{"ms":1500}`) // the poll's reconcile window ends

	// The click: /tbm opens the window (which polls), the AoE switch sends "aoe off" from the
	// queue 0.35 s later, both on the addon channel the server advertised.
	click := r.ok("addon_click", `{"button":"Fight.AoE"}`)
	if c := click["clicked"].([]any); len(c) != 2 || c[0] != "/tbm full" || c[1] != "AoE On" {
		t.Fatalf("clicked %v", click)
	}
	r.expectBody(opcode.CMSGMessagechat, r.addonBody("roster"))
	if aoe := switchOf(click["state"].(map[string]any), "aoe"); aoe["enabled"] != false {
		t.Fatalf("the switch is not pending: %v", aoe)
	}
	r.ok("addon_tick", `{"ms":400}`)
	r.expectBody(opcode.CMSGMessagechat, r.addonBody("action aoe off"))
	r.addon("TBM:ACTION_ACK|aoe|party|1|off")
	st = r.addonStateUntil("the ACK", func(st map[string]any) bool { return st["last_ack"] != nil })
	if aoe := switchOf(st, "aoe"); aoe["lamp"] != "grey" || aoe["state"] != "off" || aoe["text"] != "AoE Off" || aoe["enabled"] != true {
		t.Fatalf("aoe after the ACK %v", aoe)
	}
	if a := st["last_ack"].(map[string]any); a["intent"] != "aoe" || a["executor"] != "off" {
		t.Fatalf("ack %v", a)
	}

	// The server's own state says off too: the next roster keeps the lamp off.
	r.addon("TBM:BOTSTATE_BEGIN|1", "TBM:BOTSTATE|Gorm|move=follow,loot=on,aoe=off,autocc=off", "TBM:BOTSTATE_END", "TBM:TRANSPORT|party")
	st = r.addonStateUntil("the new BOTSTATE", func(st map[string]any) bool {
		g, _ := st["bots"].(map[string]any)["Gorm"].(map[string]any)
		return g != nil && g["aoe"] == "off"
	})
	if aoe := switchOf(st, "aoe"); aoe["state"] != "off" {
		t.Fatalf("aoe %v", aoe)
	}
	if errs := st["lua_errors"].([]any); len(errs) != 0 {
		t.Fatalf("lua errors %v", errs)
	}
}

// The real clock: the pump ticks the addon, and a click waits for its reply.
func TestAddonHostRealClockClickWaitsForReply(t *testing.T) {
	dir := testAddonDir(t)
	r := addonRig(t, dir)
	addonStart(r, "")
	r.expectBody(opcode.CMSGMessagechat, say(".bot help"))
	ch := r.callAsync("addon_click", `{"button":"Camp.Rest","wait_ms":5000}`)
	r.expectBody(opcode.CMSGMessagechat, say(".bot roster")) // the window opens
	r.expectBody(opcode.CMSGMessagechat, say(".bot action rest"))
	r.system("TBM:ACTION_ACK|rest|party|2|-")
	res := result(t, ch)
	if res["timed_out"] == true || len(res["reply"].([]any)) != 1 {
		t.Fatalf("%v", res)
	}
	st := res["state"].(map[string]any)
	if a, _ := st["last_ack"].(map[string]any); a == nil || a["intent"] != "rest" {
		t.Fatalf("state %v", st)
	}
	if c := res["clicked"].([]any); c[len(c)-1] != "Rest" {
		t.Fatalf("clicked %v", c)
	}
}

func TestAddonHostClickErrors(t *testing.T) {
	dir := testAddonDir(t)
	r := addonRig(t, dir)
	if out := r.call("addon_state", `{}`); out["error"] == nil {
		t.Fatalf("state before start: %v", out)
	}
	// The caller cannot choose the interpreter or the checkout.
	for _, p := range []string{`{"lua":"/bin/sh"}`, `{"dir":"/tmp"}`} {
		if out := r.call("addon_start", p); out["error"] == nil {
			t.Fatalf("addon_start %s: %v", p, out)
		}
	}
	addonStart(r, "manual")
	if out := r.call("addon_start", `{"clock":"manual"}`); out["error"] == nil {
		t.Fatal("second start")
	}
	// Attack is greyed out without an enemy target.
	out := r.call("addon_click", `{"button":"Fight.Attack"}`)
	if msg, _ := out["error"].(map[string]any)["message"].(string); !strings.Contains(msg, "greyed out") {
		t.Fatalf("%v", out)
	}
	out = r.call("addon_click", `{"tab":"Fight","label":"No such button"}`)
	if msg, _ := out["error"].(map[string]any)["message"].(string); !strings.Contains(msg, "no visible button") {
		t.Fatalf("%v", out)
	}
	// The Raid tab is hidden outside a raid.
	out = r.call("addon_click", `{"button":"Raid.DouseRunes"}`)
	if msg, _ := out["error"].(map[string]any)["message"].(string); !strings.Contains(msg, "not in a raid") {
		t.Fatalf("%v", out)
	}
}

func TestPlanAddonClick(t *testing.T) {
	bag, slot := 0, 3
	for _, c := range []struct {
		p    addonClickParams
		want string
		err  string
	}{
		{p: addonClickParams{Button: "Fight.AoE"}, want: `{"tab":"actions","label":"AoE"}`},
		{p: addonClickParams{Button: "Fight.CCAssign", Mark: "Moon"}, want: `{"tab":"actions","label":"Next","row":"Moon","before":"CC Mark"}`},
		{p: addonClickParams{Button: "Fight.CCAssign"}, err: "name the mark"},
		{p: addonClickParams{Button: "Roster.Login", Name: "Alt"}, want: `{"tab":"roster","label":"Login","select":["Alt"]}`},
		{p: addonClickParams{Button: "Roster.Login"}, err: "rows to tick"},
		{p: addonClickParams{Button: "BotPanel.Behavior.AoESpells", Bot: "Gorm"}, want: `{"tab":"botpanel","label":"AoE spells","bot":"Gorm","view":"Behaviour"}`},
		{p: addonClickParams{Button: "BotPanel.Item.Equip", Bot: "Gorm", Bag: &bag, Slot: &slot}, want: `{"tab":"botpanel","label":"Equip","bot":"Gorm","view":"Bags","item":{"bag":0,"slot":3}}`},
		{p: addonClickParams{Button: "BotPanel.Inventory", Bot: "Gorm"}, want: `{"tab":"botpanel","bot":"Gorm"}`},
		{p: addonClickParams{Button: "BotPanel.Inventory"}, err: "needs bot"},
		{p: addonClickParams{Tab: "Party", Bot: "Gorm", Label: "Tank"}, want: `{"tab":"party","label":"Tank","row":"Gorm"}`},
		{p: addonClickParams{Tab: "Nowhere", Label: "x"}, err: "tab"},
		{p: addonClickParams{}, err: "give a button"},
	} {
		plan, _, err := planAddonClick(c.p)
		if c.err != "" {
			if err == nil || !strings.Contains(err.Error(), c.err) {
				t.Errorf("%+v: err %v, want %q", c.p, err, c.err)
			}
			continue
		}
		b, _ := json.Marshal(plan)
		if err != nil || string(b) != c.want {
			t.Errorf("%+v: %s %v, want %s", c.p, b, err, c.want)
		}
	}
}
