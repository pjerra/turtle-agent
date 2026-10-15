package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/pjerra/turtle-agent/internal/opcode"
	"github.com/pjerra/turtle-agent/internal/session"
)

const observePlanJSON = `{"version":1,"name":"rem","map":0,"entrance":{"x":5,"y":5,"z":5},"legs":[{"name":"here","to":{"x":5,"y":5,"z":5},"boss":{"entry":99,"position":{"x":6,"y":5,"z":5}}},{"name":"there","to":{"x":9,"y":5,"z":5},"boss":{"entry":98,"position":{"x":10,"y":5,"z":5}}}]}`

func observeRig(t *testing.T) *agentRig {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "rem.json"), []byte(observePlanJSON), 0o600); err != nil {
		t.Fatal(err)
	}
	return newRigWith(t, session.Options{}, [3]float32{1, 2, 3}, methodOptions{DungeonsDir: dir})
}

// serverUp answers the probe, the bind and the first frame of an observed run, up to the point where the run leader is asked to start.
func serverUp(t *testing.T, r *agentRig) {
	t.Helper()
	r.expectBody(opcode.CMSGMessagechat, say(".bot run list"))
	r.system("Dungeon plans: rem (map 0, 3 steps)", "TBM:ACTION_ACK|run list|plans|1|-")
	r.expectBody(opcode.CMSGMessagechat, say(".bot commander bind Tanky Healy"))
	r.system("TBM:ACTION_ACK|commander bind|party|2|Tanky")
	r.expectBody(opcode.CMSGMessagechat, say(".bot commander view on"))
	r.system("TBM:ACTION_ACK|commander view|party|2|on")
	r.system(viewFrame(1, false)...)
	r.expectBody(opcode.CMSGMessagechat, say(".bot behavior Tanky loot on"))
	r.system("TBM:ACTION_ACK|behavior loot|bot:Tanky|1|on")
	r.expectBody(opcode.CMSGMessagechat, say(".bot run start rem"))
}

// An observed run end to end over the protocol: the agent prepares and binds as in commander mode, starts the run leader and then sends
// nothing but the run's own pause and resume (asked through the RPC) until the leader says done; the card data is in the report.
func TestDungeonRunObserveEndToEnd(t *testing.T) {
	r := observeRig(t)
	start := resultOf(t, r.call("dungeon_run", `{"name":"rem","force":true,"mode":"observe","lead_bot":"Tanky","bots":["Healy"],"preflight":"off"}`))
	if start["mode"] != "observe" || start["run_mode"] != "observe" || start["started"] != true || start["lead_bot"] != "Tanky" {
		t.Fatalf("%v", start)
	}
	serverUp(t, r)
	r.system("TBM:RUN|rem|running|1/3|here|move|walk|walking to 5 5 5", "TBM:ACTION_ACK|run start|party|3|rem")

	// a person asks for a pause through the RPC: the next look sends it
	resultOf(t, r.call("dungeon_pause", `{}`))
	r.expectBody(opcode.CMSGMessagechat, say(".bot run pause"))
	r.system("TBM:RUN|rem|paused|1/3|here|move|walk|paused by .bot run pause", "TBM:ACTION_ACK|run pause|party|1|rem")
	st := r.eventually("dungeon_status", `{}`, func(o map[string]any) bool {
		ob := observeOf(o)
		return ob != nil && ob["state"] == "paused"
	})
	if ob := observeOf(st); ob["leg"] != "here" || ob["kind"] != "move" || ob["step"].(float64) != 1 || ob["steps"].(float64) != 3 || ob["text"] == "" {
		t.Fatalf("%v", ob)
	}
	resultOf(t, r.call("dungeon_resume", `{}`))
	r.expectBody(opcode.CMSGMessagechat, say(".bot run resume"))
	r.system("TBM:RUN|rem|running|1/3|here|move|walk|resumed", "TBM:ACTION_ACK|run resume|party|1|rem")
	r.system("TBM:RUN|rem|running|2/3|there|move|walk|walking")
	time.Sleep(1200 * time.Millisecond)
	r.system("TBM:RUN|rem|done|3/3|there|move|walk|plan done")

	// the end of the run: the bots are stood still and the binding is let go. No run stop: the run leader ended it.
	r.expectBody(opcode.CMSGMessagechat, say(".bot cmd stay"))
	r.system("TBM:ACTION_ACK|cmd stay|party|2|Tanky")
	r.expectBody(opcode.CMSGMessagechat, say(".bot commander unbind"))
	r.system("TBM:ACTION_ACK|commander unbind|party|2|0")
	var done map[string]any
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		done = resultOf(t, r.call("dungeon_status", `{}`))
		if done["state"] == "done" {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if done["state"] != "done" {
		t.Fatalf("%v", done)
	}
	rep := done["report"].(map[string]any)
	rem := rep["remote"].(map[string]any)
	ob := rem["observe"].(map[string]any)
	if rem["mode"] != "observe" || ob["started"] != true || ob["ended"] != "done" || ob["state"] != "done" || ob["pauses"].(float64) != 1 || len(ob["controls"].([]any)) != 2 {
		t.Fatalf("%v", rem)
	}
	if legs := rep["legs"].([]any); len(legs) != 2 || legs[0].(map[string]any)["name"] != "here" || legs[1].(map[string]any)["name"] != "there" {
		t.Fatalf("legs %v", rep["legs"])
	}
	// resume and skip need a run; a pause with no run going changes nothing (the commander pause's rule)
	for _, m := range []string{"dungeon_resume", "dungeon_skip"} {
		if out := r.call(m, `{}`); errCode(out) == 0 {
			t.Fatalf("%s with no run going: %v", m, out)
		}
	}
	if out := r.call("dungeon_pause", `{}`); errCode(out) != 0 {
		t.Fatalf("a pause with no run going: %v", out)
	}
}

func observeOf(status map[string]any) map[string]any {
	res, ok := status["result"].(map[string]any)
	if !ok {
		res = status
	}
	rep, _ := res["report"].(map[string]any)
	rem, _ := rep["remote"].(map[string]any)
	ob, _ := rem["observe"].(map[string]any)
	return ob
}

// A server that does not know `.bot run` is refused before the bots are bound.
func TestDungeonRunObserveRefusesAServerWithoutTheRunLeader(t *testing.T) {
	r := observeRig(t)
	resultOf(t, r.call("dungeon_run", `{"name":"rem","force":true,"mode":"observe","lead_bot":"Tanky","bots":["Healy"],"preflight":"off"}`))
	r.expectBody(opcode.CMSGMessagechat, say(".bot run list"))
	// an older module answers nothing at all (or an "unknown command" line)
	r.system("There is no such command.")
	var st map[string]any
	deadline := time.Now().Add(8 * time.Second)
	for time.Now().Before(deadline) {
		st = resultOf(t, r.call("dungeon_status", `{}`))
		if st["state"] == "failed" {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if st["state"] != "failed" {
		t.Fatalf("%v", st)
	}
	msg := st["report"].(map[string]any)["error"].(string)
	if !strings.Contains(msg, "needs the TortoiseBots run leader") {
		t.Fatalf("%s", msg)
	}
}
