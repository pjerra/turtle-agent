package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/pjerra/turtle-agent/internal/opcode"
	"github.com/pjerra/turtle-agent/internal/rpc"
	"github.com/pjerra/turtle-agent/internal/session"
)

const (
	vTank = "0x2A,Tanky,4000,5000,35,100,1,5.0,5.0,5.0,1.50,0,0,0,0,0,n,-"
	vMob  = "0xF130000C5E0012AB,3874,9.0,5.0,5.0,64,0x2A,0,-"
)

// viewFrame is a frame of the feed for the rig: the lead bot Tanky at 5 5 5 and, when mob, one hostile it fights.
func viewFrame(seq int, mob bool) []string {
	pre := fmt.Sprintf("TBM:VIEW|v1|%d|", seq)
	if !mob {
		return []string{pre + "B|0x2A,1,0,0", pre + "P|" + vTank, pre + "E|3"}
	}
	return []string{pre + "B|0x2A,1,1,1", pre + "P|" + vTank, pre + "H|" + vMob, pre + "T|0xF130000C5E0012AB,0x2A:100", pre + "E|5"}
}

func resultOf(t *testing.T, out map[string]any) map[string]any {
	t.Helper()
	if out["error"] != nil {
		t.Fatalf("%v", out)
	}
	return out["result"].(map[string]any)
}

func TestCommanderMethod(t *testing.T) {
	r := newRig(t)
	got := make(chan map[string]any, 1)
	go func() { got <- r.call("commander", `{"op":"bind","lead_bot":"Tanky","bots":["Healy"],"wait_ms":3000}`) }()
	r.expectBody(opcode.CMSGMessagechat, say(".bot commander bind Tanky Healy"))
	r.system("Commander bound.", "TBM:ACTION_ACK|commander bind|party|2|Tanky")
	res := resultOf(t, <-got)
	rep := res["reply"].(map[string]any)
	if res["sent"] != ".bot commander bind Tanky Healy" || rep["kind"] != "ack" || rep["ok"] != true || rep["ack"].(map[string]any)["executor"] != "Tanky" {
		t.Fatalf("%v", res)
	}

	go func() {
		got <- r.call("commander", `{"op":"cmd","verb":"pullback","args":["0xF130000C5E0012AB","10"],"wait_ms":3000}`)
	}()
	r.expectBody(opcode.CMSGMessagechat, say(".bot cmd pullback 0xF130000C5E0012AB 10"))
	r.system("TBM:ACTION_ERR|cmd pullback|no-tank|No tank in the party")
	res = resultOf(t, <-got)
	rep = res["reply"].(map[string]any)
	if rep["kind"] != "err" || rep["err"].(map[string]any)["code"] != "no-tank" {
		t.Fatalf("%v", res)
	}

	go func() { got <- r.call("commander", `{"op":"view","on":false,"wait_ms":3000}`) }()
	r.expectBody(opcode.CMSGMessagechat, say(".bot commander view off"))
	r.system("TBM:ACTION_ACK|commander view|party|2|off")
	if rep := resultOf(t, <-got)["reply"].(map[string]any); rep["kind"] != "ack" {
		t.Fatalf("%v", rep)
	}

	// No answer in the wait: a timeout reply, not an error.
	go func() { got <- r.call("commander", `{"op":"status","wait_ms":200}`) }()
	r.expectBody(opcode.CMSGMessagechat, say(".bot commander status"))
	if rep := resultOf(t, <-got)["reply"].(map[string]any); rep["kind"] != "timeout" {
		t.Fatalf("%v", rep)
	}
}

// A loot, open or use order is answered in two steps: ACTION_PENDING says it runs, the final ACK comes later, and the typed order result
// is returned.
func TestCommanderMethodOrderWaitsThroughPending(t *testing.T) {
	r := newRig(t)
	got := make(chan map[string]any, 1)
	go func() {
		got <- r.call("commander", `{"op":"cmd","verb":"open","args":["0x1F1000000000AAA1","Magey"],"wait_ms":3000}`)
	}()
	r.expectBody(opcode.CMSGMessagechat, say(".bot cmd open 0x1F1000000000AAA1 Magey"))
	r.system("TBM:ACTION_PENDING|cmd open|bot:Magey|14|0x1F1000000000AAA1")
	time.Sleep(100 * time.Millisecond)
	r.system("TBM:ACTION_ACK|cmd open|bot:Magey|1|7740:1,copper:5")
	res := resultOf(t, <-got)
	rep, ord := res["reply"].(map[string]any), res["order"].(map[string]any)
	if rep["kind"] != "ack" || ord["pending"].(map[string]any)["budget_s"] != float64(14) || ord["loot"].(map[string]any)["copper"] != float64(5) {
		t.Fatalf("%v", res)
	}
}

func TestCommanderMethodRefusals(t *testing.T) {
	r := newRig(t)
	for _, c := range []string{
		`{"op":"dance"}`, `{"op":"bind"}`, `{"op":"view"}`, `{"op":"cmd"}`, `{"op":"cmd","verb":"dance"}`,
		`{"op":"cmd","verb":"attack","args":["0x1","; .gm on"]}`, `{"op":"cmd","verb":"attack","args":["0x1\n.gm on"]}`,
		`{"op":"bind","lead_bot":"Tanky; say hi"}`, `{"op":"cmd","verb":"attack","args":[".gm"]}`,
	} {
		if out := r.call("commander", c); errCode(out) != rpc.CodeInvalidParams {
			t.Errorf("%s: %v", c, out)
		}
	}
	// Nothing was sent for any of them.
	if _, err := r.srv.Expect(opcode.CMSGMessagechat, 200*time.Millisecond); err == nil {
		t.Fatal("a refused call was sent")
	}
}

func TestBotViewMethod(t *testing.T) {
	r := newRig(t)
	res := resultOf(t, r.call("bot_view", `{}`))
	if res["have"] != false || res["frame"] != nil {
		t.Fatalf("%v", res)
	}
	r.system(viewFrame(1, true)...)
	var n float64
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		res = resultOf(t, r.call("bot_view", `{}`))
		if res["have"] == true {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if res["have"] != true {
		t.Fatalf("no frame: %v", res)
	}
	fr := res["frame"].(map[string]any)
	n = res["n"].(float64)
	if fr["seq"].(float64) != 1 || len(fr["party"].([]any)) != 1 || len(fr["hostiles"].([]any)) != 1 || len(fr["threats"].([]any)) != 1 || fr["lead"] != "0x000000000000002A" {
		t.Fatalf("%v", fr)
	}
	if res["stats"].(map[string]any)["frames"].(float64) != 1 {
		t.Fatalf("%v", res["stats"])
	}
	// Wait for a newer frame.
	got := make(chan map[string]any, 1)
	go func() { got <- r.call("bot_view", fmt.Sprintf(`{"after_n":%d,"wait_ms":3000}`, int(n))) }()
	time.Sleep(50 * time.Millisecond)
	r.system(viewFrame(2, false)...)
	res = resultOf(t, <-got)
	if res["frame"].(map[string]any)["seq"].(float64) != 2 {
		t.Fatalf("%v", res)
	}
	// The throttled event is in the log: the first frame, and the second (hostiles gone) is inside the minimum gap.
	ev := resultOf(t, r.call("events", `{"since":0,"kinds":["bot_view_frame"]}`))["events"].([]any)
	if len(ev) == 0 || ev[0].(map[string]any)["data"].(map[string]any)["seq"].(float64) != 1 {
		t.Fatalf("%v", ev)
	}
}

func TestDungeonRunRemoteRefusals(t *testing.T) {
	r := dungeonRig(t)
	for _, c := range []struct{ params, want string }{
		{`{"name":"rig","mode":"fly"}`, `mode must be "group" or "remote"`},
		{`{"name":"rig","lead_bot":"Tanky"}`, `go with mode "remote"`},
		{`{"name":"rig","mode":"remote"}`, "needs lead_bot"},
		{`{"name":"rig","mode":"remote","lead_bot":"Tank y"}`, "needs lead_bot"},
		{`{"name":"rig","mode":"remote","lead_bot":"Tanky","bots":["A","B","C","D","E"]}`, "at most 4"},
		{`{"name":"rig","mode":"remote","lead_bot":"Tanky","bots":["Healy","healy"]}`, "listed twice"},
		{`{"name":"rig","mode":"remote","lead_bot":"Tanky","tank":"x;y"}`, "tank:"},
		{`{"name":"rig","mode":"remote","lead_bot":"Tanky","healer":"x;y"}`, "healer:"},
		{`{"name":"rig","healer":"Healy"}`, `go with mode "remote"`},
		{`{"name":"rig","team":"horde"}`, `go with mode "remote"`},
		{`{"name":"rig","on_end":"stay"}`, `go with mode "remote"`},
		{`{"name":"rig","carrier":"Magey"}`, `go with mode "remote"`},
		{`{"name":"rig","mode":"remote","lead_bot":"Tanky","carrier":"Ma gey"}`, "carrier:"},
		{`{"name":"rig","mode":"remote","lead_bot":"Tanky","loot_restore":"ffa"}`, "loot_restore:"},
		{`{"name":"rig","mode":"remote","lead_bot":"Tanky","object_fallback":"maybe"}`, "object_fallback:"},
		{`{"name":"rig","object_fallback":"agent"}`, `go with mode "remote"`},
		{`{"name":"rig","mode":"remote","lead_bot":"Tanky","team":"orcs"}`, "team:"},
		{`{"name":"rig","mode":"remote","lead_bot":"Tanky","preflight":"maybe"}`, "preflight:"},
		{`{"name":"rig","mode":"remote","lead_bot":"Tanky","on_end":"dance"}`, "on_end:"},
		{`{"name":"rig","mode":"remote","lead_bot":"Tanky","enter":"fly"}`, "enter:"},
		{`{"name":"rig","mode":"group","enter":"gm"}`, "go with mode"},
		{`{"name":"rig","mode":"remote","lead_bot":"Tanky","cc":[{"entry":1,"mark":"banana"}]}`, "cc: each rule"},
		{`{"name":"rig","mode":"remote","lead_bot":"Tanky","teleport":true}`, "teleport is a GM command"},
		{`{"name":"rig","mode":"remote","lead_bot":"Tanky","buffs":"world"}`, "buffs are GM commands"},
		{`{"name":"rig","mode":"remote","lead_bot":"Tanky","brain":"frost_mage"}`, "brain plays the agent's own character"},
		{`{"name":"rig","mode":"remote","lead_bot":"Tanky","gm":true}`, "no GM command"},
		// Starting anywhere and the pull test (README "Start anywhere", "Pull test").
		{`{"name":"rig","from_pull_group":0}`, `go with mode "remote"`},
		{`{"name":"rig","start_here":true}`, `go with mode "remote"`},
		{`{"name":"rig","pulltest":{"leg":0,"pull_group":0}}`, `go with mode "remote"`},
		{`{"name":"rig","mode":"remote","lead_bot":"Tanky","from_pull_group":3}`, "from_pull_group 3: leg 0 (first) has 1 pull groups"},
		{`{"name":"rig","mode":"remote","lead_bot":"Tanky","from_leg":"second","from_pull_group":-1}`, "from_pull_group -1: leg 1 (second)"},
		{`{"name":"rig","have_items":[7733]}`, `go with mode "remote"`},
		{`{"name":"rig","mode":"remote","lead_bot":"Tanky","have_items":[0]}`, "have_items"},
		{`{"name":"rig","mode":"remote","lead_bot":"Tanky","have_items":[1,2,3,4,5,6,7,8,9,10,11,12,13,14,15,16,17,18,19,20,21]}`, "have_items"},
		{`{"name":"rig","mode":"remote","lead_bot":"Tanky","from_pull_group":0,"start_here":true}`, "exclude each other"},
		{`{"name":"rig","mode":"remote","lead_bot":"Tanky","from_pull_group":0,"resume":true}`, "exclude each other"},
		{`{"name":"rig","mode":"remote","lead_bot":"Tanky","pulltest":{"pull_group":0}}`, "needs leg"},
		{`{"name":"rig","mode":"remote","lead_bot":"Tanky","pulltest":{"leg":0}}`, "needs leg"},
		{`{"name":"rig","mode":"remote","lead_bot":"Tanky","pulltest":{"leg":"nowhere","pull_group":0}}`, "pulltest.leg: the plan has legs"},
		{`{"name":"rig","mode":"remote","lead_bot":"Tanky","pulltest":{"leg":0,"pull_group":0},"from_leg":1}`, "names its own start"},
		{`{"name":"rig","mode":"remote","lead_bot":"Tanky","pulltest":{"leg":0,"pull_group":0},"from_pull_group":0}`, "names its own start"},
		{`{"name":"rig","mode":"remote","lead_bot":"Tanky","pulltest":{"leg":0,"pull_group":0,"tries":11}}`, "use 1-10"},
		{`{"name":"rig","mode":"remote","lead_bot":"Tanky","pulltest":{"leg":0,"pull_group":0,"tries":-1}}`, "use 1-10"},
		{`{"name":"rig","mode":"remote","lead_bot":"Tanky","pulltest":{"leg":0,"pull_group":0,"reset":"fast"}}`, "pulltest.reset"},
		{`{"name":"rig","mode":"remote","lead_bot":"Tanky","pulltest":{"leg":"second","pull_group":4}}`, "has 1 pull groups"},
	} {
		out := r.call("dungeon_run", c.params)
		if errCode(out) != rpc.CodeInvalidParams || !strings.Contains(out["error"].(map[string]any)["message"].(string), c.want) {
			t.Errorf("%s: %v", c.params, out["error"])
		}
	}
}

// A remote run end to end over the session: it binds, switches the view on, reads a frame, finds the plan done where the lead bot
// stands, and unbinds. The agent sends chat lines only; no GM command.
func TestDungeonRunRemoteEndToEnd(t *testing.T) {
	dir := t.TempDir()
	plan := `{"version":1,"name":"rem","map":0,"entrance":{"x":5,"y":5,"z":5},"legs":[{"name":"here","to":{"x":5,"y":5,"z":5},"steps":[{"wait_for":{"hostiles_gone":{"radius":30},"timeout_s":10}}]}]}`
	if err := os.WriteFile(filepath.Join(dir, "rem.json"), []byte(plan), 0o600); err != nil {
		t.Fatal(err)
	}
	r := newRigWith(t, session.Options{}, [3]float32{1, 2, 3}, methodOptions{DungeonsDir: dir})
	start := resultOf(t, r.call("dungeon_run", `{"name":"rem","mode":"remote","lead_bot":"Tanky","bots":["Healy"]}`))
	if start["mode"] != "remote" || start["lead_bot"] != "Tanky" || start["leader_gm"] != "false" || start["started"] != true {
		t.Fatalf("%v", start)
	}
	r.expectBody(opcode.CMSGMessagechat, say(".bot commander bind Tanky Healy"))
	r.system("TBM:ACTION_ACK|commander bind|party|2|Tanky")
	r.expectBody(opcode.CMSGMessagechat, say(".bot commander view on"))
	r.system("TBM:ACTION_ACK|commander view|party|2|on")
	r.system(viewFrame(1, false)...)
	// The run turns corpse looting on for the bound bots first (a run that ended in a crash may have left it off), then the pre-run kit
	// check: maintenance, then the bags (plain `.bot` lines, answered by system messages).
	r.expectBody(opcode.CMSGMessagechat, say(".bot behavior Tanky loot on"))
	r.system("TBM:ACTION_ACK|behavior loot|bot:Tanky|1|on")
	r.expectBody(opcode.CMSGMessagechat, say(".bot maintenance Tanky"))
	r.system("Maintained Tanky (consumables, reagents, ammo, repair).")
	// The talents read-back: the one `.bot command` line the agent sends, answered here as a system line.
	r.expectBody(opcode.CMSGMessagechat, say(".bot command Tanky talents"))
	r.system("My current talent spec is: Protection (0/5/31) Link: 05-31")
	r.expectBody(opcode.CMSGMessagechat, say(".bot inv Tanky"))
	r.system("TBM:INV_BEGIN|Tanky|0", "TBM:INV_EQ|16|2000|10|10", "TBM:INV_END|Tanky|10|16")
	// The end of the run: the bots are stood still, then the binding is ended.
	r.expectBody(opcode.CMSGMessagechat, say(".bot cmd stay"))
	r.system("TBM:ACTION_ACK|cmd stay|party|2|Tanky")
	r.expectBody(opcode.CMSGMessagechat, say(".bot commander unbind"))
	r.system("TBM:ACTION_ACK|commander unbind|party|2|0")
	var st map[string]any
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		st = resultOf(t, r.call("dungeon_status", `{}`))
		if st["state"] == "done" {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if st["state"] != "done" {
		t.Fatalf("%v", st)
	}
	rem := st["report"].(map[string]any)["remote"].(map[string]any)
	if rem["mode"] != "remote" || rem["bound"] != true || rem["unbound"] != true || rem["frames_received"].(float64) != 1 || rem["gm_commands"].(float64) != 0 {
		t.Fatalf("%v", rem)
	}
	if rem["end_stay"] != "ack" {
		t.Fatalf("end_stay: %v", rem)
	}
	if pf, _ := rem["preflight"].(map[string]any); pf == nil || pf["bots"].([]any)[0].(map[string]any)["maintenance"] != "maintained" || pf["bots"].([]any)[0].(map[string]any)["talents"] != "0/5/31" {
		t.Fatalf("preflight: %v", rem["preflight"])
	}
}

// A remote run whose lead bot is outside the plan's map enters it: with enter "gm" the agent teleports the lead bot with the one GM line
// `.tele name`, sends nothing else of the sort, waits for the feed to show the bot on the plan's map, and goes on. The agent is not in a
// group and stays outside.
func TestDungeonRunRemoteEntersByTele(t *testing.T) {
	dir := t.TempDir()
	plan := `{"version":1,"name":"rem","map":70,"entrance":{"x":5,"y":5,"z":5},"entrance_tele":"ulda","legs":[{"name":"here","to":{"x":5,"y":5,"z":5},"steps":[{"wait_for":{"hostiles_gone":{"radius":30},"timeout_s":10}}]}]}`
	if err := os.WriteFile(filepath.Join(dir, "rem.json"), []byte(plan), 0o600); err != nil {
		t.Fatal(err)
	}
	r := newRigWith(t, session.Options{}, [3]float32{1, 2, 3}, methodOptions{DungeonsDir: dir})
	resultOf(t, r.call("dungeon_run", `{"name":"rem","mode":"remote","lead_bot":"Tanky","enter":"gm","preflight":"off"}`))
	r.expectBody(opcode.CMSGMessagechat, say(".bot commander bind Tanky"))
	r.system("TBM:ACTION_ACK|commander bind|party|2|Tanky")
	r.expectBody(opcode.CMSGMessagechat, say(".bot commander view on"))
	r.system("TBM:ACTION_ACK|commander view|party|2|on")
	r.system(viewFrame(1, false)...) // outside: map 0
	r.expectBody(opcode.CMSGMessagechat, say(".tele name Tanky ulda"))
	inside := viewFrame(2, false)
	inside[1] = strings.Replace(inside[1], ",1.50,0,0,", ",1.50,70,0,", 1)
	r.system(inside...)
	r.expectBody(opcode.CMSGMessagechat, say(".bot behavior Tanky loot on"))
	r.system("TBM:ACTION_ACK|behavior loot|bot:Tanky|1|on")
	r.expectBody(opcode.CMSGMessagechat, say(".bot cmd stay"))
	r.system("TBM:ACTION_ACK|cmd stay|party|1|Tanky")
	r.expectBody(opcode.CMSGMessagechat, say(".bot commander unbind"))
	r.system("TBM:ACTION_ACK|commander unbind|party|1|0")
	var st map[string]any
	deadline := time.Now().Add(8 * time.Second)
	for time.Now().Before(deadline) {
		st = resultOf(t, r.call("dungeon_status", `{}`))
		if st["state"] == "done" {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if st["state"] != "done" {
		t.Fatalf("%v", st)
	}
	rem := st["report"].(map[string]any)["remote"].(map[string]any)
	en, _ := rem["enter"].(map[string]any)
	if en == nil || en["method"] != "gm" || en["tele"] != "ulda" || en["entries"].(float64) != 1 || en["agent_left_group"] != false || rem["gm_commands"].(float64) != 1 {
		t.Fatalf("enter: %v / %v", en, rem["gm_commands"])
	}
}

// The prepare block over the session: the roster is read, the offline bot is logged in with `.bot add`, the roster is read again, the
// account is probed, `.gm visible off` goes out, and only then the bind. Every line is the one the whitelist allows; the report has the
// steps with their seconds, the GM line is a setup shortcut and the run's own gm_commands stays 0.
func TestDungeonRunRemotePrepareOverTheSession(t *testing.T) {
	dir := t.TempDir()
	plan := `{"version":1,"name":"rem","map":0,"entrance":{"x":5,"y":5,"z":5},"legs":[{"name":"here","to":{"x":5,"y":5,"z":5},"steps":[{"wait_for":{"hostiles_gone":{"radius":30},"timeout_s":10}}]}]}`
	if err := os.WriteFile(filepath.Join(dir, "rem.json"), []byte(plan), 0o600); err != nil {
		t.Fatal(err)
	}
	r := newRigWith(t, session.Options{}, [3]float32{1, 2, 3}, methodOptions{DungeonsDir: dir})
	resultOf(t, r.call("dungeon_run", `{"name":"rem","mode":"remote","lead_bot":"Tanky","preflight":"off","prepare":{"online":true,"gm_setup":[".gm visible off"]}}`))
	r.expectBody(opcode.CMSGMessagechat, say(".bot roster"))
	r.system("TBM:ROSTER_BEGIN|1", "TBM:ROSTER|42|Tanky|1|offline|0|-", "TBM:ROSTER_END")
	r.expectBody(opcode.CMSGMessagechat, say(".bot add Tanky"))
	r.system("Bot Tanky queued for login; it will follow Glacienne after entering the world.")
	r.expectBody(opcode.CMSGMessagechat, say(".bot roster"))
	r.system("TBM:ROSTER_BEGIN|1", "TBM:ROSTER|42|Tanky|1|online|0|Uldaman", "TBM:ROSTER_END")
	r.expectBody(opcode.CMSGMessagechat, say(".account"))
	r.system("Your account level is: 3")
	r.expectBody(opcode.CMSGMessagechat, say(".gm visible off"))
	r.expectBody(opcode.CMSGMessagechat, say(".bot commander bind Tanky"))
	r.system("TBM:ACTION_ACK|commander bind|party|1|Tanky")
	r.expectBody(opcode.CMSGMessagechat, say(".bot commander view on"))
	r.system("TBM:ACTION_ACK|commander view|party|1|on")
	r.system(viewFrame(1, false)...)
	r.expectBody(opcode.CMSGMessagechat, say(".bot behavior Tanky loot on"))
	r.system("TBM:ACTION_ACK|behavior loot|bot:Tanky|1|on")
	r.expectBody(opcode.CMSGMessagechat, say(".bot cmd stay"))
	r.system("TBM:ACTION_ACK|cmd stay|party|1|Tanky")
	r.expectBody(opcode.CMSGMessagechat, say(".bot commander unbind"))
	r.system("TBM:ACTION_ACK|commander unbind|party|1|0")
	var st map[string]any
	deadline := time.Now().Add(8 * time.Second)
	for time.Now().Before(deadline) {
		st = resultOf(t, r.call("dungeon_status", `{}`))
		if st["state"] == "done" {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if st["state"] != "done" {
		t.Fatalf("%v", st)
	}
	rep := st["report"].(map[string]any)
	pr, _ := rep["prepare"].(map[string]any)
	steps, _ := pr["steps"].([]any)
	if len(steps) != 2 || steps[0].(map[string]any)["name"] != "online" || steps[1].(map[string]any)["name"] != "gm_setup" || pr["gm_commands"].(float64) != 1 {
		t.Fatalf("prepare: %v", pr)
	}
	if _, ok := rep["setup_s"].(float64); !ok || rep["remote"].(map[string]any)["gm_commands"].(float64) != 0 {
		t.Fatalf("setup_s / gm_commands: %v %v", rep["setup_s"], rep["remote"])
	}
	if sc, _ := rep["setup_shortcuts"].([]any); len(sc) != 1 || sc[0] != "gm visible off" {
		t.Fatalf("setup_shortcuts: %v", rep["setup_shortcuts"])
	}
}

// What a prepare block may hold is checked when the run starts, before anything is sent.
func TestDungeonRunPrepareRefusals(t *testing.T) {
	r := dungeonRig(t)
	for _, c := range []struct{ params, want string }{
		{`{"name":"rig","prepare":{"online":true}}`, `go with mode "remote"`},
		{`{"name":"rig","mode":"group","prepare":{}}`, `go with mode "remote"`},
		{`{"name":"rig","mode":"remote","lead_bot":"Tanky","prepare":{"gm_setup":["gm on"]}}`, "not allowed"},
		{`{"name":"rig","mode":"remote","lead_bot":"Tanky","prepare":{"gm_setup":["instance unbind all"]}}`, "not allowed"},
		{`{"name":"rig","mode":"remote","lead_bot":"Tanky","prepare":{"gm_setup":["gm visible off","gm visible on","gm visible off","gm visible on","gm visible off"]}}`, "at most"},
		{`{"name":"rig","mode":"remote","lead_bot":"Tanky","prepare":{"online":true,"online_wait_s":999}}`, "online_wait_s"},
		{`{"name":"rig","mode":"remote","lead_bot":"Tanky","prepare":{"fresh":true,"unbind":"all"}}`, "unbind"},
	} {
		out := r.call("dungeon_run", c.params)
		if errCode(out) != rpc.CodeInvalidParams || !strings.Contains(out["error"].(map[string]any)["message"].(string), c.want) {
			t.Errorf("%s: %v", c.params, out["error"])
		}
	}
}

// A run that fails still unbinds.
func TestDungeonRunRemoteUnbindsWhenTheBindIsAnswered(t *testing.T) {
	dir := t.TempDir()
	plan := `{"version":1,"name":"rem","map":70,"entrance":{"x":5,"y":5,"z":5},"legs":[{"name":"here","to":{"x":5,"y":5,"z":5},"steps":[{"wait_for":{"hostiles_gone":{"radius":30},"timeout_s":10}}]}]}`
	if err := os.WriteFile(filepath.Join(dir, "rem.json"), []byte(plan), 0o600); err != nil {
		t.Fatal(err)
	}
	r := newRigWith(t, session.Options{}, [3]float32{1, 2, 3}, methodOptions{DungeonsDir: dir})
	resultOf(t, r.call("dungeon_run", `{"name":"rem","mode":"remote","lead_bot":"Tanky","enter":"off"}`))
	r.expectBody(opcode.CMSGMessagechat, say(".bot commander bind Tanky"))
	r.system("TBM:ACTION_ACK|commander bind|party|2|Tanky")
	r.expectBody(opcode.CMSGMessagechat, say(".bot commander view on"))
	r.system("TBM:ACTION_ACK|commander view|party|2|on")
	r.system(viewFrame(1, false)...) // the lead bot is on map 0, the plan needs 70
	r.expectBody(opcode.CMSGMessagechat, say(".bot cmd stay"))
	r.system("TBM:ACTION_ACK|cmd stay|party|1|Tanky")
	r.expectBody(opcode.CMSGMessagechat, say(".bot commander unbind"))
	r.system("TBM:ACTION_ACK|commander unbind|party|2|0")
	var st map[string]any
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		st = resultOf(t, r.call("dungeon_status", `{}`))
		if st["state"] == "failed" {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	rep := st["report"].(map[string]any)
	if st["state"] != "failed" || !strings.Contains(rep["error"].(string), "map 70") || rep["remote"].(map[string]any)["unbound"] != true {
		t.Fatalf("%v", st)
	}
}

// TBM:VIEW lines go to the feed and not into the event log, so a long run does not push other events out; the ACKs stay.
func TestViewLinesStayOutOfTheEventLog(t *testing.T) {
	r := newRig(t)
	for seq := 1; seq <= 5; seq++ {
		r.system(viewFrame(seq, true)...)
	}
	r.system("TBM:ACTION_ACK|commander view|party|1|on", "plain system line")
	var res map[string]any
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		res = resultOf(t, r.call("bot_view", `{}`))
		if res["view_lines"].(float64) >= 25 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if res["view_lines"].(float64) != 25 || res["stats"].(map[string]any)["frames"].(float64) != 5 {
		t.Fatalf("%v", res)
	}
	// The ACK and the plain line are in the log (poll until they arrive), no VIEW line is.
	var evs []any
	for time.Now().Before(deadline.Add(2 * time.Second)) {
		evs = resultOf(t, r.call("events", `{"since":0,"kinds":["system"]}`))["events"].([]any)
		if len(evs) >= 2 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if len(evs) != 2 {
		t.Fatalf("%d system events: %v", len(evs), evs)
	}
	for _, e := range evs {
		if strings.Contains(e.(map[string]any)["text"].(string), "TBM:VIEW") {
			t.Fatalf("a VIEW line is in the log: %v", e)
		}
	}
	// Debug sampling: every 2nd frame is logged (frames 1, 3, 5 of the next five: lines of frames counted from now on).
	resultOf(t, r.call("bot_view", `{"log_every":2}`))
	for seq := 6; seq <= 9; seq++ {
		r.system(viewFrame(seq, false)...)
	}
	deadline = time.Now().Add(3 * time.Second)
	n := 0
	for time.Now().Before(deadline) {
		n = 0
		for _, e := range resultOf(t, r.call("events", `{"since":0,"kinds":["system"]}`))["events"].([]any) {
			if strings.Contains(e.(map[string]any)["text"].(string), "TBM:VIEW") {
				n++
			}
		}
		if n >= 6 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if n != 6 { // two of the four frames, three lines each
		t.Fatalf("%d VIEW lines logged with log_every 2", n)
	}
	if out := r.call("bot_view", `{"log_every":-1}`); errCode(out) != rpc.CodeInvalidParams {
		t.Fatalf("%v", out)
	}
}

// A run that starts inside the dungeon (from_pull_group) needs a GM commander: with a player's account it fails after reading the account
// level and before any GM line, and still ends the binding. The result of dungeon_run names the pull group.
func TestDungeonRunRemoteStartNeedsAGM(t *testing.T) {
	dir := t.TempDir()
	plan := `{"version":1,"name":"rem","map":0,"entrance":{"x":5,"y":5,"z":5},"legs":[{"name":"packs","to":{"x":5,"y":5,"z":5},"pull_groups":[{"entries":[299],"positions":[{"x":300,"y":5,"z":5}]}]}]}`
	if err := os.WriteFile(filepath.Join(dir, "rem.json"), []byte(plan), 0o600); err != nil {
		t.Fatal(err)
	}
	r := newRigWith(t, session.Options{}, [3]float32{1, 2, 3}, methodOptions{DungeonsDir: dir})
	start := resultOf(t, r.call("dungeon_run", `{"name":"rem","mode":"remote","lead_bot":"Tanky","preflight":"off","from_pull_group":0}`))
	if start["from_leg"] != float64(0) || start["from_pull_group"] != float64(0) {
		t.Fatalf("%v", start)
	}
	r.expectBody(opcode.CMSGMessagechat, say(".bot commander bind Tanky"))
	r.system("TBM:ACTION_ACK|commander bind|party|1|Tanky")
	r.expectBody(opcode.CMSGMessagechat, say(".bot commander view on"))
	r.system("TBM:ACTION_ACK|commander view|party|1|on")
	r.system(viewFrame(1, false)...)
	r.expectBody(opcode.CMSGMessagechat, say(".bot behavior Tanky loot on"))
	r.system("TBM:ACTION_ACK|behavior loot|bot:Tanky|1|on")
	r.expectBody(opcode.CMSGMessagechat, say(".account"))
	r.system("Your account level is: 0")
	r.expectBody(opcode.CMSGMessagechat, say(".bot cmd stay"))
	r.system("TBM:ACTION_ACK|cmd stay|party|1|Tanky")
	r.expectBody(opcode.CMSGMessagechat, say(".bot commander unbind"))
	r.system("TBM:ACTION_ACK|commander unbind|party|1|0")
	var st map[string]any
	deadline := time.Now().Add(8 * time.Second)
	for time.Now().Before(deadline) {
		st = resultOf(t, r.call("dungeon_status", `{}`))
		if st["state"] == "failed" {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	rep, _ := st["report"].(map[string]any)
	if st["state"] != "failed" || !strings.Contains(rep["error"].(string), "needs a GM commander") {
		t.Fatalf("%v", st)
	}
	rem := rep["remote"].(map[string]any)
	sr, _ := rep["start"].(map[string]any)
	if rem["gm_commands"].(float64) != 0 || rem["unbound"] != true || sr == nil || sr["kind"] != "pull_gather" || sr["pull_group"] != float64(0) || len(sr["gm_commands"].([]any)) != 0 {
		t.Fatalf("remote %v start %v", rem, sr)
	}
}
