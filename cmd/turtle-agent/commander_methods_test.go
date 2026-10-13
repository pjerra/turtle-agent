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
		{`{"name":"rig","mode":"remote","lead_bot":"Tanky","cc":[{"entry":1,"mark":"banana"}]}`, "cc: each rule"},
		{`{"name":"rig","mode":"remote","lead_bot":"Tanky","teleport":true}`, "teleport is a GM command"},
		{`{"name":"rig","mode":"remote","lead_bot":"Tanky","buffs":"world"}`, "buffs are GM commands"},
		{`{"name":"rig","mode":"remote","lead_bot":"Tanky","brain":"frost_mage"}`, "brain plays the agent's own character"},
		{`{"name":"rig","mode":"remote","lead_bot":"Tanky","gm":true}`, "no GM command"},
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
}

// A run that fails still unbinds.
func TestDungeonRunRemoteUnbindsWhenTheBindIsAnswered(t *testing.T) {
	dir := t.TempDir()
	plan := `{"version":1,"name":"rem","map":70,"entrance":{"x":5,"y":5,"z":5},"legs":[{"name":"here","to":{"x":5,"y":5,"z":5},"steps":[{"wait_for":{"hostiles_gone":{"radius":30},"timeout_s":10}}]}]}`
	if err := os.WriteFile(filepath.Join(dir, "rem.json"), []byte(plan), 0o600); err != nil {
		t.Fatal(err)
	}
	r := newRigWith(t, session.Options{}, [3]float32{1, 2, 3}, methodOptions{DungeonsDir: dir})
	resultOf(t, r.call("dungeon_run", `{"name":"rem","mode":"remote","lead_bot":"Tanky"}`))
	r.expectBody(opcode.CMSGMessagechat, say(".bot commander bind Tanky"))
	r.system("TBM:ACTION_ACK|commander bind|party|2|Tanky")
	r.expectBody(opcode.CMSGMessagechat, say(".bot commander view on"))
	r.system("TBM:ACTION_ACK|commander view|party|2|on")
	r.system(viewFrame(1, false)...) // the lead bot is on map 0, the plan needs 70
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
