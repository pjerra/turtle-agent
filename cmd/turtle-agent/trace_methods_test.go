package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/pjerra/turtle-agent/internal/brain"
	"github.com/pjerra/turtle-agent/internal/dungeon"
	"github.com/pjerra/turtle-agent/internal/rpc"
	"github.com/pjerra/turtle-agent/internal/session"
	"github.com/pjerra/turtle-agent/internal/trace"
	"github.com/pjerra/turtle-agent/internal/tracerun"
)

func traceRig(t *testing.T) (*agentRig, string) {
	t.Helper()
	plans, traces := t.TempDir(), t.TempDir()
	if err := os.WriteFile(filepath.Join(plans, "rig.json"), []byte(rigPlan), 0o600); err != nil {
		t.Fatal(err)
	}
	r := newRigWith(t, session.Options{}, [3]float32{1, 2, 3}, methodOptions{DungeonsDir: plans, TracesDir: traces, AccountProbeWindow: 50 * time.Millisecond})
	return r, traces
}

func TestDungeonRunTraceParametersAreChecked(t *testing.T) {
	r, _ := traceRig(t)
	meta := map[string]string{}
	for i := 0; i < 17; i++ {
		meta[strings.Repeat("k", i+1)] = "v"
	}
	big, _ := json.Marshal(meta)
	for _, c := range []struct{ params, want string }{
		{`{"name":"rig","run_id":"../x"}`, "run_id must be letters"},
		{`{"name":"rig","run_id":"a b"}`, "run_id must be letters"},
		{`{"name":"rig","trace_meta":` + string(big) + `}`, "at most 16"},
		{`{"name":"rig","trace_meta":{"":"x"}}`, "trace_meta: keys and values"},
		{`{"name":"rig","trace":"yes"}`, "trace"},
		{`{"name":"rig","run_meta":{"model":"a b"}}`, "run_meta.model"},
		{`{"name":"rig","run_meta":{"run":"a:b"}}`, "no colon"},
		{`{"name":"rig","run_meta":{"server_trace":true}}`, "server_trace"},
	} {
		out := r.call("dungeon_run", c.params)
		msg, _ := out["error"].(map[string]any)["message"].(string)
		if errCode(out) != rpc.CodeInvalidParams || !strings.Contains(msg, c.want) {
			t.Errorf("%s: %v", c.params, out)
		}
	}
}

// A run (here one that fails at once: no party) has the caller's run id in the result, the report, the events and the trace, and a
// summary beside the trace.
func TestDungeonRunWritesATraceUnderTheRunID(t *testing.T) {
	r, dir := traceRig(t)
	res := r.call("dungeon_run", `{"name":"rig","run_id":"r-cmd-1","trace_meta":{"tb_sha":"abc"}}`)["result"].(map[string]any)
	want := filepath.Join(dir, "r-cmd-1.jsonl")
	if res["run_id"] != "r-cmd-1" || res["trace_file"] != want {
		t.Fatalf("%v", res)
	}
	st := r.eventually("dungeon_status", "{}", func(m map[string]any) bool {
		ds := m["result"].(map[string]any)
		return ds["state"] == "failed" && ds["running"] == false
	})["result"].(map[string]any)
	rep := st["report"].(map[string]any)
	tr, _ := rep["trace"].(map[string]any)
	if rep["run_id"] != "r-cmd-1" || tr == nil || tr["path"] != want || tr["verdict"] == nil {
		t.Fatalf("%v", rep)
	}
	evs := r.call("events", `{"since":0,"kinds":["dungeon_failed"]}`)["result"].(map[string]any)["events"].([]any)
	if len(evs) != 1 || evs[0].(map[string]any)["data"].(map[string]any)["run_id"] != "r-cmd-1" {
		t.Fatalf("%v", evs)
	}
	f, err := trace.Read(want)
	if err != nil {
		t.Fatal(err)
	}
	if f.Header.Run != "r-cmd-1" || f.Header.Plan != "rig" || f.Header.Meta["tb_sha"] != "abc" || f.Header.AgentVersion != version || f.End == nil || f.End.Reason != "failed" {
		t.Fatalf("%+v", f.Header)
	}
	if _, err := os.Stat(filepath.Join(dir, "r-cmd-1.summary.json")); err != nil {
		t.Fatal(err)
	}
	// The same id again: the file exists, the run goes on without a trace and says why.
	r.call("dungeon_run", `{"name":"rig","run_id":"r-cmd-1"}`)
	st = r.eventually("dungeon_status", "{}", func(m map[string]any) bool {
		ds := m["result"].(map[string]any)
		rp, _ := ds["report"].(map[string]any)
		return ds["running"] == false && rp != nil && rp["run_id"] == "r-cmd-1" && rp["trace"] != nil && rp["trace"].(map[string]any)["error"] != nil
	})["result"].(map[string]any)
	if e := st["report"].(map[string]any)["trace"].(map[string]any)["error"].(string); !strings.Contains(e, "exist") {
		t.Fatalf("%s", e)
	}
}

// A run started with run_meta carries it in the report and in the trace header beside trace_meta.
func TestDungeonRunWritesRunMeta(t *testing.T) {
	r, dir := traceRig(t)
	r.call("dungeon_run", `{"name":"rig","run_id":"r-meta-1","trace_meta":{"tb_sha":"abc"},"run_meta":{"model":"haiku","effort":"max","run":"zf-ab-2-s5"}}`)
	st := r.eventually("dungeon_status", "{}", func(m map[string]any) bool {
		ds := m["result"].(map[string]any)
		return ds["state"] == "failed" && ds["running"] == false
	})["result"].(map[string]any)
	rep := st["report"].(map[string]any)
	rm, _ := rep["run_meta"].(map[string]any)
	if rm["model"] != "haiku" || rm["effort"] != "max" || rm["run"] != "zf-ab-2-s5" {
		t.Fatalf("report run_meta %v", rm)
	}
	f, err := trace.Read(filepath.Join(dir, "r-meta-1.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	if f.Header.Meta["tb_sha"] != "abc" || f.Header.Meta["run_model"] != "haiku" || f.Header.Meta["run_effort"] != "max" || f.Header.Meta["run_label"] != "zf-ab-2-s5" {
		t.Fatalf("header meta %v", f.Header.Meta)
	}
}

// Without run_meta in the call the slot's settings default applies; the call's own run_meta wins over it.
func TestDungeonRunTakesRunMetaFromTheSlot(t *testing.T) {
	mk := func(def string) (*agentRig, string) {
		plans, traces := t.TempDir(), t.TempDir()
		if err := os.WriteFile(filepath.Join(plans, "rig.json"), []byte(rigPlan), 0o600); err != nil {
			t.Fatal(err)
		}
		mo := methodOptions{DungeonsDir: plans, TracesDir: traces, AccountProbeWindow: 50 * time.Millisecond}
		if def != "" {
			mo.RunMeta = dungeon.RunMeta{Model: "slot-model", Effort: "low", Run: def}
		}
		return newRigWith(t, session.Options{}, [3]float32{1, 2, 3}, mo), traces
	}
	metaOf := func(r *agentRig, params string) map[string]any {
		r.call("dungeon_run", params)
		st := r.eventually("dungeon_status", "{}", func(m map[string]any) bool {
			ds := m["result"].(map[string]any)
			return ds["running"] == false
		})["result"].(map[string]any)
		return st["report"].(map[string]any)["run_meta"].(map[string]any)
	}
	r, _ := mk("slot-run")
	if rm := metaOf(r, `{"name":"rig","run_id":"r-slot-1"}`); rm["model"] != "slot-model" || rm["run"] != "slot-run" {
		t.Fatalf("slot default not applied: %v", rm)
	}
	r2, _ := mk("slot-run")
	if rm := metaOf(r2, `{"name":"rig","run_id":"r-slot-2","run_meta":{"model":"call-model"}}`); rm["model"] != "call-model" || rm["effort"] != nil || rm["run"] != nil {
		t.Fatalf("call meta did not win: %v", rm)
	}
	r2b, _ := mk("slot-run")
	if rm := metaOf(r2b, `{"name":"rig","run_id":"r-slot-2b","run_meta":{}}`); rm["model"] != "slot-model" || rm["run"] != "slot-run" {
		t.Fatalf("empty run_meta switched the slot default off: %v", rm)
	}
	r3, _ := mk("")
	r3.call("dungeon_run", `{"name":"rig","run_id":"r-slot-3"}`)
	st := r3.eventually("dungeon_status", "{}", func(m map[string]any) bool {
		ds := m["result"].(map[string]any)
		return ds["running"] == false
	})["result"].(map[string]any)
	if rm, ok := st["report"].(map[string]any)["run_meta"]; ok && rm != nil {
		t.Fatalf("run_meta without a default: %v", rm)
	}
}
func TestDungeonRunTraceFalseWritesNothing(t *testing.T) {
	r, dir := traceRig(t)
	res := r.call("dungeon_run", `{"name":"rig","trace":false}`)["result"].(map[string]any)
	id, _ := res["run_id"].(string)
	if !strings.HasPrefix(id, "r-") || res["trace_file"] != nil {
		t.Fatalf("%v", res)
	}
	r.eventually("dungeon_status", "{}", func(m map[string]any) bool { return m["result"].(map[string]any)["state"] == "failed" })
	if ents, _ := os.ReadDir(dir); len(ents) != 0 {
		t.Fatalf("%v", ents)
	}
}

// A brain started by hand has a run id (the caller's, or one made): in its status, its events and its own trace.
func TestBrainSessionHasARunIDAndATrace(t *testing.T) {
	_, dir := traceRig(t)
	r2, dir2 := traceRig(t)
	_ = dir
	st := r2.call("brain_start", `{"strategies":["frost_mage"],"run_id":"r-brain-1"}`)["result"].(map[string]any)
	if st["run_id"] != "r-brain-1" {
		t.Fatalf("%v", st)
	}
	if out := r2.call("brain_status", "{}")["result"].(map[string]any); out["run_id"] != "r-brain-1" {
		t.Fatalf("%v", out)
	}
	r2.eventually("brain_status", "{}", func(m map[string]any) bool { return m["result"].(map[string]any)["idle"] == "not_ready" })
	r2.call("brain_stop", "{}")
	evs := r2.call("events", `{"since":0,"kinds":["brain_state","brain_idle"]}`)["result"].(map[string]any)["events"].([]any)
	if len(evs) < 2 {
		t.Fatalf("%v", evs)
	}
	for _, e := range evs {
		if e.(map[string]any)["data"].(map[string]any)["run_id"] != "r-brain-1" {
			t.Fatalf("%v", e)
		}
	}
	f, err := trace.Read(filepath.Join(dir2, "r-brain-1.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	if f.Header.Source != "brain" || f.Header.Run != "r-brain-1" || f.End == nil || f.End.Reason != "stopped" || len(f.Oracles) != 10 || len(f.Party) != 1 || !f.Party[0].Lead {
		t.Fatalf("%+v", f.Header)
	}
	var states []string
	for _, e := range f.Events {
		if e.Ev == "brain_state" {
			states = append(states, e.D["state"].(string))
		}
	}
	if strings.Join(states, ",") != "started,stopped" {
		t.Fatalf("%v", states)
	}
	// A made-up id is valid and the same in the status and the file name.
	if out := r2.call("brain_start", `{"strategies":["frost_mage"],"run_id":"a/b"}`); errCode(out) != rpc.CodeInvalidParams {
		t.Fatalf("%v", out)
	}
	st = r2.call("brain_start", `{"strategies":["frost_mage"],"trace":false}`)["result"].(map[string]any)
	id, _ := st["run_id"].(string)
	r2.call("brain_stop", "{}")
	if _, err := os.Stat(filepath.Join(dir2, id+".jsonl")); !os.IsNotExist(err) {
		t.Fatalf("trace false: %v", err)
	}
}

func writeTestTrace(t *testing.T, dir, id string, deaths int) string {
	t.Helper()
	w, err := trace.Open(trace.Options{Dir: dir, RunID: id})
	if err != nil {
		t.Fatal(err)
	}
	w.Header(trace.Header{Source: "dungeon_run", Mode: "group", Plan: "uldaman", Start: time.Unix(1_000_000, 0).UTC(), StartUnix: 1_000_000, Party: []trace.Member{{G: "0x11", Name: "Mage"}}})
	for i := 0; i < deaths; i++ {
		w.Event(int64(1000*(i+1)), "death", map[string]any{"name": "Mage"})
	}
	w.Event(80_000, "rest", map[string]any{"seconds": 50.0})
	w.End(trace.End{Envelope: trace.Envelope{T: 100_000}, Ended: time.Unix(1_000_100, 0).UTC(), Reason: "done", Seconds: 100})
	w.Close()
	return w.Path()
}

func TestTraceScoreCommand(t *testing.T) {
	dir := t.TempDir()
	clean := writeTestTrace(t, dir, "r-clean", 0)
	dead := writeTestTrace(t, dir, "r-dead", 1)
	score := func(args ...string) (int, string, string) {
		var out, errOut bytes.Buffer
		code := run(context.Background(), append([]string{"trace-score"}, args...), &out, &errOut)
		return code, out.String(), errOut.String()
	}
	// 50 s of 100 s in rest gates is over the 40 % limit.
	code, out, _ := score(clean)
	if code != exitTraceFail || !strings.Contains(out, "FAIL rest_share") || !strings.Contains(out, "r-clean") || !strings.Contains(out, "n/a: ") {
		t.Fatalf("%d\n%s", code, out)
	}
	if code, out, _ := score("--set", "rest_share=60", clean); code != exitOK || !strings.Contains(out, "PASS rest_share") {
		t.Fatalf("%d\n%s", code, out)
	}
	code, out, _ = score("--json", "--set", "rest_share=60", clean, dead)
	if code != exitTraceFail {
		t.Fatalf("%d\n%s", code, out)
	}
	lines := strings.Split(strings.TrimSpace(out), "\n")
	if len(lines) != 2 {
		t.Fatalf("%s", out)
	}
	var v struct {
		Run     string
		Verdict string
		Failed  []string
		Checks  []trace.Check
	}
	if err := json.Unmarshal([]byte(lines[1]), &v); err != nil || v.Run != "r-dead" || v.Verdict != "fail" || len(v.Failed) != 1 || v.Failed[0] != "deaths" || len(v.Checks) != 10 {
		t.Fatalf("%v %+v", err, v)
	}
	// --summary writes the summary beside the trace.
	if code, _, _ := score("--summary", "--set", "rest_share=60", clean); code != exitOK {
		t.Fatalf("%d", code)
	}
	b, err := os.ReadFile(filepath.Join(dir, "r-clean.summary.json"))
	var s trace.Summary
	if err != nil || json.Unmarshal(b, &s) != nil || s.Run != "r-clean" || s.Status != "complete" || s.Verdict != "pass" || len(s.Oracles) != 10 || s.Bytes == 0 || s.Src != "agent" || s.Scorer == "" {
		t.Fatalf("%v %s", err, b)
	}
	// Usage errors.
	for _, args := range [][]string{{}, {"--set", "rest_share", clean}, {"--set", "deaths=1", clean}, {filepath.Join(dir, "missing.jsonl")}} {
		if code, _, errOut := score(args...); code != exitUsage || errOut == "" {
			t.Errorf("%v: %d %q", args, code, errOut)
		}
	}
}

// A brain that stopped before its recorder existed has no "stopped" event left: finish ends the trace.
func TestBrainTraceFinishEndsASessionThatStoppedEarly(t *testing.T) {
	dir := t.TempDir()
	bt := &brainTrace{dir: dir}
	bt.begin("r-early", brainStartParams{}, "Mage", 0x2a, brain.Status{Strategies: []string{"frost_mage"}, Role: "dps"})
	if tracerun.Find("r-early") == nil {
		t.Fatal("not open")
	}
	bt.finish("r-other") // another session's id: nothing happens
	if tracerun.Find("r-early") == nil {
		t.Fatal("ended by another id")
	}
	bt.finish("r-early")
	if tracerun.Find("r-early") != nil {
		t.Fatal("still listed as live")
	}
	f, err := trace.Read(filepath.Join(dir, "r-early.jsonl"))
	if err != nil || f.End == nil || f.End.Reason != "stopped" || len(f.Oracles) != 10 || f.Party[0].G != "0x2a" {
		t.Fatalf("%v %+v", err, f)
	}
	bt.finish("r-early") // a second call is harmless
}
