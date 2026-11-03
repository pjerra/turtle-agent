package main

// --reuse-instance (walk 32): a re-place that keeps the raid's current instance. The instance cap made every
// re-place wait 15-20 minutes (the core allows 5 fresh ids per account per hour), and the tool had no way to move the
// party inside the copy it was already in. These tests drive a fake server: the raid is already entered, the ledger
// is at the cap, and the walk must neither enter a fresh copy nor count one.

import (
	"bytes"
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/pjerra/turtle-agent/internal/raidwalk"
)

// reuseCapLedger fills a ledger to the warn threshold at now and returns its path.
func reuseCapLedger(t *testing.T, now time.Time) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), raidwalk.InstanceCapFile)
	if _, err := raidwalk.UpdateCapLedger(path, func(l *raidwalk.CapLedger) {
		for i := range raidwalk.InstanceCapWarnAt {
			l.Record("m910q", raidwalk.CapEntry{At: now.Add(-time.Duration(i+1) * time.Minute)}, now)
		}
	}); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestReuseInstanceKeepsTheInstance(t *testing.T) {
	f, _ := newPlaceFixture(t)
	f.opts.reuseInstance = true
	f.agent.entered = true // the previous run's entry put the raid inside; the frames show it on map 309
	now := time.Date(2026, 11, 3, 12, 0, 0, 0, time.UTC)
	f.env.capLedger = reuseCapLedger(t, now)
	_, plan := placePlanRows(t)
	f.agent.startReply = zgPlaceReply(t)
	if code := f.run(context.Background()); code != exitOK {
		t.Fatalf("exit %d, want 0: %s\n%s", code, f.stderr.String(), f.stdout.String())
	}
	// No fresh entry order: the raid was already inside.
	for _, l := range f.agentLog() {
		if strings.HasPrefix(l, "cmdr cmd enter") {
			t.Fatalf("the walk sent an entry order under --reuse-instance: %q", l)
		}
	}
	// The cap is untouched: no wait, and no ledger entry.
	if strings.Contains(f.stderr.String(), "instance cap") {
		t.Fatalf("the cap check ran under --reuse-instance: %q", f.stderr.String())
	}
	l, err := raidwalk.LoadCapLedger(f.env.capLedger)
	if err != nil {
		t.Fatal(err)
	}
	if n := l.Count("m910q", now); n != raidwalk.InstanceCapWarnAt {
		t.Fatalf("ledger count %d, want %d (a reuse records no fresh instance)", n, raidwalk.InstanceCapWarnAt)
	}
	// Every hire was teleported to the start point with the GM path.
	f.agent.mu.Lock()
	party := append([]string{}, f.agent.party...)
	pos := map[string][3]float32{}
	for k, v := range f.agent.pos {
		pos[k] = v
	}
	f.agent.mu.Unlock()
	if len(party) == 0 {
		t.Fatal("no hires")
	}
	summoned := 0
	for _, l := range f.agentLog() {
		if strings.HasPrefix(l, "gm .summon ") {
			summoned++
		}
	}
	if summoned < len(party) {
		t.Fatalf("%d summons for %d hires, want every hire teleported", summoned, len(party))
	}
	for _, name := range party {
		p, ok := pos[name]
		if !ok || dist3(p, [3]float32{plan.Point.X, plan.Point.Y, plan.Point.Z + 2}) > 5 {
			t.Errorf("%s stands at %v, want the start point %v", name, p, plan.Point)
		}
	}
	sum := f.summary()
	if !hasNote(sum, "keeping the raid's instance") || !hasNote(sum, "teleported") {
		t.Fatalf("notes %q, want the reuse notes", sum.Notes)
	}
	if sum.Place == nil || sum.Place.Instance == 0 {
		t.Fatalf("place summary %+v, want the kept instance", sum.Place)
	}
}

// A raid that is not alive in the plan's map refuses the reuse instead of entering a fresh instance.
func TestReuseInstanceRefusesARaidThatIsNotThere(t *testing.T) {
	f, _ := newPlaceFixture(t)
	f.opts.reuseInstance = true
	f.agent.entered = true
	f.agent.dead["Bot01"] = true
	f.agent.startReply = zgPlaceReply(t)
	if code := f.run(context.Background()); code != exitUsage {
		t.Fatalf("exit %d, want %d (usage): %s", code, exitUsage, f.stderr.String())
	}
	out := f.stderr.String() + f.stdout.String()
	if !strings.Contains(out, "--reuse-instance: the raid is not alive in map 309") {
		t.Fatalf("output %q, want the not-alive refusal", out)
	}
	if !strings.Contains(out, "dead: Bot01") {
		t.Fatalf("output %q, want the dead hire named", out)
	}
	for _, l := range f.agentLog() {
		if strings.HasPrefix(l, "cmdr cmd enter") {
			t.Fatalf("an entry order went out for a refused reuse: %q", l)
		}
	}
}

func TestReuseInstanceRefusals(t *testing.T) {
	var stderr bytes.Buffer
	if _, code := parseRaidWalkArgs([]string{"zul_gurub", "--reuse-instance"}, &stderr); code != exitUsage ||
		!strings.Contains(stderr.String(), "needs --place") {
		t.Fatalf("without --place: exit %d, stderr %q", code, stderr.String())
	}
	stderr.Reset()
	args := []string{"zul_gurub", "--place", "--from", "leg high_priest_thekal", "--sql", "walk.sql",
		"--reuse-instance", "--gm-clear", "2:1"}
	if _, code := parseRaidWalkArgs(args, &stderr); code != exitUsage || !strings.Contains(stderr.String(), "not combined") {
		t.Fatalf("with --gm-clear: exit %d, stderr %q", code, stderr.String())
	}
	stderr.Reset()
	args = []string{"zul_gurub", "--place", "--from", "leg high_priest_thekal", "--sql", "walk.sql", "--reuse-instance"}
	if o, code := parseRaidWalkArgs(args, &stderr); code != exitOK || !o.reuseInstance {
		t.Fatalf("plain reuse: exit %d, options %+v, stderr %q", code, o, stderr.String())
	}
}
