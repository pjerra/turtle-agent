package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/pjerra/turtle-agent/internal/raidwalk"
)

// TestAutoCommandArgs pins the managed flags: the target's own flags, the goal's until, the attempt's out, and the
// restart's --from/--place/--reuse-instance. Nothing unsafe is ever added.
func TestAutoCommandArgs(t *testing.T) {
	target := raidwalk.AutoTarget{
		ID: "a", Server: "tortoise-test2", Slot: "1", Holder: "zg-a",
		Goal: raidwalk.AutoGoal{Kind: "boss", Boss: 14515},
		Args: []string{"zul_gurub", "--dir", "/plans", "--hires", "/roster.json"},
	}
	args := autoCommandArgs("/bin/ta", target, "/walk/a-1", "step 405", true)
	got := strings.Join(args, " ")
	want := "raid-walk zul_gurub --dir /plans --hires /roster.json --server tortoise-test2 --slot 1 --holder zg-a --until boss-done --out /walk/a-1 --from step 405 --place --reuse-instance"
	if got != want {
		t.Fatalf("args:\n%s\nwant:\n%s", got, want)
	}
	if strings.Contains(got, "--instance-cap-unsafe") {
		t.Fatal("unsafe flag added")
	}
	// A first placement has no --from, no --place and no reuse.
	args = autoCommandArgs("/bin/ta", target, "/walk/a-1", "", false)
	got = strings.Join(args, " ")
	if strings.Contains(got, "--from") || strings.Contains(got, "--place") || strings.Contains(got, "--reuse-instance") {
		t.Fatalf("first placement has managed restart flags: %s", got)
	}
}

func TestAutoLastStepAndReason(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "run.log")
	os.WriteFile(path, []byte("19:46:05.696 TBM:RUN|zul_gurub|running|51/761|jeklik|boss|begin|fighting\n"+
		"19:46:56.999 TBM:RUN|zul_gurub|running|51/761|jeklik|boss|begin|fighting: 11368 is polymorphed\n"), 0o600)
	if n := autoLastStep(path); n != 51 {
		t.Fatalf("step = %d, want 51", n)
	}
	if r := autoLastReason(path); r != "fighting: 11368 is polymorphed" {
		t.Fatalf("reason = %q", r)
	}
	if n := autoLastStep(filepath.Join(dir, "none.log")); n != 0 {
		t.Fatalf("missing log step = %d", n)
	}
}

// TestAutoFrameSignature counts a boss-HP change and an empty hostile list as progress, but a repeated frame as not.
func TestAutoFrameSignature(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "frames.jsonl")
	os.WriteFile(path, []byte(`{"t":1,"hostiles":[{"guid":"0xA","entry":14515,"hp_pct":80}]}`+"\n"), 0o600)
	r := &autoRun{}
	if sig := autoFrameSignature(path, r); sig != "0xA=14515:80" {
		t.Fatalf("signature = %q", sig)
	}
	// The same frame again: no new bytes, no signature.
	if sig := autoFrameSignature(path, r); sig != "" {
		t.Fatalf("unchanged file returned %q", sig)
	}
	f, _ := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o600)
	f.WriteString(`{"t":2,"hostiles":[{"guid":"0xA","entry":14515,"hp_pct":40}]}` + "\n")
	f.Close()
	if sig := autoFrameSignature(path, r); sig != "0xA=14515:40" {
		t.Fatalf("changed hp signature = %q", sig)
	}
	f, _ = os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o600)
	f.WriteString(`{"t":3,"hostiles":[]}` + "\n")
	f.Close()
	if sig := autoFrameSignature(path, r); sig != "none" {
		t.Fatalf("empty hostiles signature = %q", sig)
	}
}

// TestAutoCapFreeTime pins the instance-cap wait as "not a stall": the line's earliest free time is the floor.
func TestAutoCapFreeTime(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "run.log")
	os.WriteFile(path, []byte("10:00:00.000 instance cap: waiting 16m5s (earliest free time 2026-11-03T12:34:56Z) for the oldest of 4 to age out of the hour\n"), 0o600)
	at := autoCapFreeTime(path)
	if at.IsZero() || at.Format("2006-01-02T15:04:05Z") != "2026-11-03T12:34:56Z" {
		t.Fatalf("free time = %s", at)
	}
}

// TestAutoInitSeq continues the out numbering after an earlier autopilot's dirs (the walk refuses an existing --out).
func TestAutoInitSeq(t *testing.T) {
	dir := t.TempDir()
	for _, name := range []string{"a-1", "a-2.out", "a-10", "b-3", "other-9"} {
		if err := os.MkdirAll(filepath.Join(dir, name), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	s := &autoSupervisor{o: autoOptions{walkDir: dir}, states: []*autoState{{id: "a"}, {id: "b"}}}
	s.initSeq()
	if s.states[0].seq != 10 || s.states[1].seq != 3 {
		t.Fatalf("seq a=%d b=%d, want 10 and 3", s.states[0].seq, s.states[1].seq)
	}
}

func TestAutoSummaryReadsTheSingularBossKill(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "summary.json")
	os.WriteFile(path, []byte(`{"exit_code":0,"steps_reached":51,"steps_total":761,"end_reason":"until","boss_kill":{"entry":14517,"killed":true},"boss_kills":[{"entry":14517,"killed":true}]}`), 0o600)
	sum, err := autoReadSummary(path)
	if err != nil {
		t.Fatal(err)
	}
	if sum.BossKill == nil || !sum.BossKill.Killed || sum.BossKill.Entry != 14517 {
		t.Fatalf("boss_kill = %+v", sum.BossKill)
	}
}
