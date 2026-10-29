// raid_walk_pair_test.go: the two-raid mode (spec raid-walk-addons 4.2). The walks themselves run with the A3 fakes
// (walkFakeAgent/fakeClock), so no slot, lease or network is touched.
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/pjerra/turtle-agent/internal/slots"
)

// pairFakeRun replaces the walk run and records its options; it answers with code and prints one line.
func pairFakeRun(t *testing.T, code int) (*[]raidWalkOptions, *[]time.Duration) {
	t.Helper()
	var mu sync.Mutex
	opts := []raidWalkOptions{}
	sleeps := []time.Duration{}
	oldRun, oldEnv, oldSleep := raidWalkPairRun, raidWalkPairEnv, raidWalkPairSleep
	raidWalkPairEnv = func(o *raidWalkOptions) (walkEnv, walkDeps, error) { return walkEnv{}, walkDeps{}, nil }
	raidWalkPairSleep = func(ctx context.Context, d time.Duration) error {
		mu.Lock()
		sleeps = append(sleeps, d)
		mu.Unlock()
		return nil
	}
	raidWalkPairRun = func(ctx context.Context, o raidWalkOptions, env walkEnv, deps walkDeps, stdout, stderr io.Writer) int {
		mu.Lock()
		opts = append(opts, o)
		mu.Unlock()
		fmt.Fprintf(stdout, "walk %s\n", o.plan)
		return code
	}
	t.Cleanup(func() { raidWalkPairRun, raidWalkPairEnv, raidWalkPairSleep = oldRun, oldEnv, oldSleep })
	return &opts, &sleeps
}

func TestRaidWalkPairRefusesSlot3(t *testing.T) {
	envCalled := false
	oldEnv := raidWalkPairEnv
	raidWalkPairEnv = func(o *raidWalkOptions) (walkEnv, walkDeps, error) {
		envCalled = true
		return walkEnv{}, walkDeps{}, nil
	}
	t.Cleanup(func() { raidWalkPairEnv = oldEnv })
	var stdout, stderr bytes.Buffer
	if code := raidWalkCmd(context.Background(), []string{"onyxias_lair", "--slot", "1,3"}, &stdout, &stderr); code != exitSlotRefused {
		t.Fatalf("--slot 1,3: exit %d, want %d (stderr %q)", code, exitSlotRefused, stderr.String())
	}
	if !strings.Contains(stderr.String(), "never uses slot 3") {
		t.Fatalf("the refusal does not name the rule: %q", stderr.String())
	}
	if envCalled {
		t.Fatal("slot 3 built a walk environment (a lease could follow)")
	}
	if _, err := os.Stat(filepath.Join(".", "raid-walk-pair-")); err == nil {
		t.Fatal("slot 3 made an out dir")
	}
}

func TestRaidWalkPairSlotListShape(t *testing.T) {
	for _, list := range []string{"1,1", "1,2,4", "1,", ",2", "1,2,3"} {
		var stdout, stderr bytes.Buffer
		if code := raidWalkCmd(context.Background(), []string{"--slot", list, "onyxias_lair"}, &stdout, &stderr); code != exitUsage {
			t.Fatalf("--slot %q: exit %d, want %d (stderr %q)", list, code, exitUsage, stderr.String())
		}
		if stderr.Len() == 0 {
			t.Fatalf("--slot %q: no message", list)
		}
	}
}

func TestRaidWalkPairPrefixedFlags(t *testing.T) {
	opts, _ := pairFakeRun(t, exitOK)
	out := filepath.Join(t.TempDir(), "pair")
	var stdout, stderr bytes.Buffer
	code := raidWalkPairCmd(context.Background(), []string{
		"--slot", "1,2", "--out", out, "--sql", "1=a.sql", "--sql", "2=b.sql",
		"--hires", "r.json", "--inject", "2=lag", "onyxias_lair",
	}, &stdout, &stderr)
	if code != exitOK {
		t.Fatalf("exit %d: %s", code, stderr.String())
	}
	got := *opts
	if len(got) != 2 {
		t.Fatalf("%d walks ran, want 2 (%+v)", len(got), got)
	}
	sort.Slice(got, func(i, j int) bool { return got[i].slot < got[j].slot })
	if got[0].slot != "1" || got[0].sql != "a.sql" || len(got[0].injects) != 0 {
		t.Fatalf("walk 1 %+v, want slot 1, sql a.sql, no inject", got[0])
	}
	if got[1].slot != "2" || got[1].sql != "b.sql" || strings.Join(got[1].injects, ",") != "lag" {
		t.Fatalf("walk 2 %+v, want slot 2, sql b.sql, inject lag", got[1])
	}
	for _, o := range got {
		if o.hires != "r.json" {
			t.Fatalf("walk %s hires %q, want the unprefixed r.json", o.slot, o.hires)
		}
	}

	// A global flag refuses a slot prefix.
	stdout.Reset()
	stderr.Reset()
	code = raidWalkPairCmd(context.Background(), []string{
		"--slot", "1,2", "--out", filepath.Join(t.TempDir(), "pair2"), "--speed", "1=2.0", "onyxias_lair",
	}, &stdout, &stderr)
	if code != exitUsage {
		t.Fatalf("--speed 1=2.0: exit %d, want %d (stderr %q)", code, exitUsage, stderr.String())
	}
	if !strings.Contains(stderr.String(), "global") {
		t.Fatalf("the refusal does not say the flag is global: %q", stderr.String())
	}
}

func TestRaidWalkPairSeparateDirsAndHolders(t *testing.T) {
	opts, sleeps := pairFakeRun(t, exitOK)
	out := filepath.Join(t.TempDir(), "pair")
	var stdout, stderr bytes.Buffer
	if code := raidWalkPairCmd(context.Background(), []string{"--slot", "1,2", "--out", out, "onyxias_lair"}, &stdout, &stderr); code != exitOK {
		t.Fatalf("exit %d: %s", code, stderr.String())
	}
	got := *opts
	if len(got) != 2 {
		t.Fatalf("%d walks ran, want 2", len(got))
	}
	sort.Slice(got, func(i, j int) bool { return got[i].slot < got[j].slot })
	wantOut := []string{filepath.Join(out, "s1-onyxias_lair"), filepath.Join(out, "s2-onyxias_lair")}
	wantHolder := []string{"raid-walk-onyxias_lair-s1", "raid-walk-onyxias_lair-s2"}
	for i, o := range got {
		if o.out != wantOut[i] {
			t.Fatalf("walk %s out %q, want %q (one shared out dir would be %q)", o.slot, o.out, wantOut[i], out)
		}
		if o.holder != wantHolder[i] {
			t.Fatalf("walk %s holder %q, want %q (the same holder for both would collide)", o.slot, o.holder, wantHolder[i])
		}
	}
	if len(*sleeps) != 1 || (*sleeps)[0] != 30*time.Second {
		t.Fatalf("stagger sleeps %v, want one 30 s sleep", *sleeps)
	}
	if !strings.Contains(stdout.String(), "[s1 onyxias_lair] walk onyxias_lair") ||
		!strings.Contains(stdout.String(), "[s2 onyxias_lair] walk onyxias_lair") {
		t.Fatalf("the walks' output carries no slot prefix:\n%s", stdout.String())
	}
}

func TestRaidWalkPairWorstExit(t *testing.T) {
	cases := []struct{ a, b, want int }{
		{exitOK, exitRaidWalkInjectFailed, exitRaidWalkInjectFailed},
		{exitRaidWalkIncident, exitRaidWalkInjectFailed, exitRaidWalkIncident},
		{exitRaidWalkFailed, exitOK, exitRaidWalkFailed},
		{exitSlotRefused, exitOK, exitSlotRefused},
		{exitSlotRefused, exitUsage, exitUsage},
		{exitOK, exitOK, exitOK},
	}
	for _, c := range cases {
		if got := pairWorstExit(c.a, c.b); got != c.want {
			t.Fatalf("pairWorstExit(%d, %d) = %d, want %d", c.a, c.b, got, c.want)
		}
	}
}

func TestRaidWalkPairTwoRealWalks(t *testing.T) {
	fa, fb := newFixture(t), newFixture(t)
	fa.defaultRun("onyxias_lair")
	fb.defaultRun("onyxias_lair")
	oldEnv, oldSleep := raidWalkPairEnv, raidWalkPairSleep
	raidWalkPairEnv = func(o *raidWalkOptions) (walkEnv, walkDeps, error) {
		switch o.slot {
		case "1":
			return fa.env, fa.deps, nil
		case "2":
			return fb.env, fb.deps, nil
		}
		return walkEnv{}, walkDeps{}, fmt.Errorf("unexpected slot %q", o.slot)
	}
	raidWalkPairSleep = func(ctx context.Context, d time.Duration) error { return nil }
	t.Cleanup(func() { raidWalkPairEnv, raidWalkPairSleep = oldEnv, oldSleep })

	out := filepath.Join(t.TempDir(), "pair")
	var stdout, stderr bytes.Buffer
	code := raidWalkPairCmd(context.Background(), []string{
		"--slot", "1,2", "--out", out, "--dir", "../../dungeons", "onyxias_lair",
	}, &stdout, &stderr)
	if code != exitOK {
		t.Fatalf("exit %d: %s\n%s", code, stderr.String(), stdout.String())
	}
	for _, sub := range []string{"s1-onyxias_lair", "s2-onyxias_lair"} {
		raw, err := os.ReadFile(filepath.Join(out, sub, "summary.json"))
		if err != nil {
			t.Fatalf("%s: %v", sub, err)
		}
		var s struct {
			ExitCode     int    `json:"exit_code"`
			EndReason    string `json:"end_reason"`
			StepsReached int    `json:"steps_reached"`
		}
		if err := json.Unmarshal(raw, &s); err != nil {
			t.Fatal(err)
		}
		if s.ExitCode != exitOK || s.EndReason != "until" || s.StepsReached != 28 {
			t.Fatalf("%s summary %+v", sub, s)
		}
	}
	raw, err := os.ReadFile(filepath.Join(out, "pair.json"))
	if err != nil {
		t.Fatal(err)
	}
	var p struct {
		Format              string  `json:"format"`
		Version             int     `json:"version"`
		OverlapS            float64 `json:"overlap_s"`
		DashboardBothListed *bool   `json:"dashboard_both_listed"`
		ExitCode            int     `json:"exit_code"`
		Walks               []struct {
			Slot         string `json:"slot"`
			Plan         string `json:"plan"`
			Out          string `json:"out"`
			ExitCode     int    `json:"exit_code"`
			EndReason    string `json:"end_reason"`
			StepsReached int    `json:"steps_reached"`
			Tab          string `json:"tab"`
		} `json:"walks"`
	}
	if err := json.Unmarshal(raw, &p); err != nil {
		t.Fatal(err)
	}
	if p.Format != "tb-raid-walk-pair" || p.Version != 1 || len(p.Walks) != 2 || p.ExitCode != exitOK {
		t.Fatalf("pair.json %+v", p)
	}
	for i, w := range p.Walks {
		if w.Slot != fmt.Sprint(i+1) || w.Plan != "onyxias_lair" || w.ExitCode != exitOK || w.EndReason != "until" || w.StepsReached != 28 {
			t.Fatalf("walk %d %+v", i, w)
		}
		if w.Tab != "#/commanders/Commander" {
			t.Fatalf("walk %d tab %q", i, w.Tab)
		}
	}
	if p.OverlapS <= 0 {
		t.Fatalf("overlap_s %v, want the two runs' overlap", p.OverlapS)
	}
	if p.DashboardBothListed != nil {
		t.Fatalf("dashboard_both_listed %v, want null with the dashboard off", *p.DashboardBothListed)
	}
	if _, err := os.Stat(filepath.Join(out, "pair.txt")); err != nil {
		t.Fatalf("pair.txt: %v", err)
	}
}

func TestRaidWalkPairPanicCleansBothWalks(t *testing.T) {
	var mu sync.Mutex
	var releases, teardowns []string
	oldRun, oldEnv, oldSleep := raidWalkPairRun, raidWalkPairEnv, raidWalkPairSleep
	raidWalkPairEnv = func(o *raidWalkOptions) (walkEnv, walkDeps, error) {
		return walkEnv{
			acquire: func(server, slot string, any bool, holder string, ttl time.Duration) (slots.Acquired, error) {
				return slots.Acquired{Lease: slots.Lease{Server: server, Slot: slot, Holder: holder, LeaseID: "id-" + slot}}, nil
			},
			release: func(server, slot, holder, leaseID string, force bool) (slots.Lease, error) {
				mu.Lock()
				releases = append(releases, leaseID)
				mu.Unlock()
				return slots.Lease{Server: server, Slot: slot, Holder: holder, LeaseID: leaseID}, nil
			},
			teardown: func(args []string, stdout, stderr io.Writer) int {
				mu.Lock()
				teardowns = append(teardowns, args[len(args)-1])
				mu.Unlock()
				return exitOK
			},
		}, walkDeps{}, nil
	}
	raidWalkPairSleep = func(ctx context.Context, d time.Duration) error { return nil }
	raidWalkPairRun = func(ctx context.Context, o raidWalkOptions, env walkEnv, deps walkDeps, stdout, stderr io.Writer) int {
		if _, err := env.acquire(o.server, o.slot, false, o.holder, time.Minute); err != nil {
			t.Errorf("walk %s: acquire: %v", o.slot, err)
		}
		if o.slot == "2" {
			panic("boom")
		}
		select {
		case <-ctx.Done():
		case <-time.After(5 * time.Second):
			t.Error("walk 1 was not cancelled when walk 2 panicked")
		}
		return exitRaidWalkFailed
	}
	t.Cleanup(func() { raidWalkPairRun, raidWalkPairEnv, raidWalkPairSleep = oldRun, oldEnv, oldSleep })

	out := filepath.Join(t.TempDir(), "pair")
	var stdout, stderr bytes.Buffer
	if code := raidWalkPairCmd(context.Background(), []string{"--slot", "1,2", "--out", out, "onyxias_lair"}, &stdout, &stderr); code != exitRaidWalkFailed {
		t.Fatalf("exit %d, want %d\n%s%s", code, exitRaidWalkFailed, stdout.String(), stderr.String())
	}
	mu.Lock()
	rels, teard := append([]string(nil), releases...), append([]string(nil), teardowns...)
	mu.Unlock()
	sort.Strings(rels)
	if strings.Join(rels, ",") != "id-1,id-2" {
		t.Fatalf("leases released %v, want id-1 and id-2 (a panicked walk left its lease held)", rels)
	}
	wantTeard := []string{
		filepath.Join(out, "s1-onyxias_lair", "slot"),
		filepath.Join(out, "s2-onyxias_lair", "slot"),
	}
	sort.Strings(teard)
	sort.Strings(wantTeard)
	if strings.Join(teard, ",") != strings.Join(wantTeard, ",") {
		t.Fatalf("teardowns %v, want %v (both slots)", teard, wantTeard)
	}
	raw, err := os.ReadFile(filepath.Join(out, "pair.json"))
	if err != nil {
		t.Fatalf("pair.json after a panic: %v", err)
	}
	var p struct {
		ExitCode int `json:"exit_code"`
		Walks    []struct {
			Slot      string `json:"slot"`
			ExitCode  int    `json:"exit_code"`
			EndReason string `json:"end_reason"`
		} `json:"walks"`
	}
	if err := json.Unmarshal(raw, &p); err != nil {
		t.Fatal(err)
	}
	if p.ExitCode != exitRaidWalkFailed || len(p.Walks) != 2 {
		t.Fatalf("pair.json %+v, want both walks and exit %d", p, exitRaidWalkFailed)
	}
	found := false
	for _, w := range p.Walks {
		if w.Slot == "2" {
			found = true
			if w.ExitCode != exitRaidWalkFailed || !strings.Contains(w.EndReason, "panic: boom") {
				t.Fatalf("walk 2 %+v, want a failed walk with the panic reason", w)
			}
		}
	}
	if !found {
		t.Fatal("pair.json has no walk on slot 2")
	}
	text, err := os.ReadFile(filepath.Join(out, "pair.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(text), fmt.Sprintf("pair: exit %d", exitRaidWalkFailed)) {
		t.Fatalf("pair.txt:\n%s", text)
	}
}

func TestRaidWalkPairAlphanumericSlotsInReport(t *testing.T) {
	opts, _ := pairFakeRun(t, exitOK)
	out := filepath.Join(t.TempDir(), "pair")
	var stdout, stderr bytes.Buffer
	if code := raidWalkPairCmd(context.Background(), []string{"--slot", "A,B", "--out", out, "onyxias_lair"}, &stdout, &stderr); code != exitOK {
		t.Fatalf("exit %d: %s", code, stderr.String())
	}
	got := *opts
	if len(got) != 2 {
		t.Fatalf("%d walks ran, want 2", len(got))
	}
	sort.Slice(got, func(i, j int) bool { return got[i].slot < got[j].slot })
	if got[0].slot != "A" || got[1].slot != "B" {
		t.Fatalf("walks ran on slots %q, %q, want A, B", got[0].slot, got[1].slot)
	}
	raw, err := os.ReadFile(filepath.Join(out, "pair.json"))
	if err != nil {
		t.Fatal(err)
	}
	var p struct {
		Walks []struct {
			Slot string `json:"slot"`
		} `json:"walks"`
	}
	if err := json.Unmarshal(raw, &p); err != nil {
		t.Fatalf("pair.json: %v", err)
	}
	if len(p.Walks) != 2 || p.Walks[0].Slot != "A" || p.Walks[1].Slot != "B" {
		t.Fatalf("pair.json slots %+v, want A then B", p.Walks)
	}
	text, err := os.ReadFile(filepath.Join(out, "pair.txt"))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"sA onyxias_lair:", "sB onyxias_lair:"} {
		if !strings.Contains(string(text), want) {
			t.Fatalf("pair.txt has no %q:\n%s", want, text)
		}
	}
}

func TestPrefixWriterKeepsLinesWhole(t *testing.T) {
	var buf bytes.Buffer
	w1 := prefixW(&buf, "[1] ")
	w2 := prefixW(&buf, "[2] ")
	var wg sync.WaitGroup
	for _, w := range []*linePrefixWriter{w1, w2} {
		wg.Add(1)
		go func(w *linePrefixWriter) {
			defer wg.Done()
			for i := range 1000 {
				if _, err := fmt.Fprintf(w, "line %d\n", i); err != nil {
					t.Errorf("write: %v", err)
					return
				}
			}
		}(w)
	}
	wg.Wait()
	w1.Flush()
	w2.Flush()
	text := buf.String()
	lines := strings.Split(strings.TrimSuffix(text, "\n"), "\n")
	if len(lines) != 2000 {
		t.Fatalf("%d lines, want 2000 (a lost or merged line)", len(lines))
	}
	for i, line := range lines {
		n := strings.Count(line, "[1] ") + strings.Count(line, "[2] ")
		if n != 1 || (!strings.HasPrefix(line, "[1] line ") && !strings.HasPrefix(line, "[2] line ")) {
			t.Fatalf("line %d %q: %d prefixes, want exactly one at the start", i, line, n)
		}
	}
}
