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

	"github.com/pjerra/turtle-agent/internal/objects"
	"github.com/pjerra/turtle-agent/internal/packet"
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

// Walk 33: --reuse-instance and --gm-clear are allowed together: the named packs are killed in the kept copy.
func TestReuseInstanceRunsGMClear(t *testing.T) {
	f, _ := newPlaceFixture(t)
	f.opts.reuseInstance = true
	f.opts.gmClear = stringList{"2:2"}
	f.opts.gmClearSpawns = gmClearZGSpawns
	f.agent.entered = true
	pack, guids := gmClearPack(t, gmClearZGSpawns)
	f.agent.unitsFn = func(int) []objects.Unit {
		return gmClearView(f, pack, guids, func(packet.GUID) bool { return false })
	}
	f.agent.startReply = zgPlaceReply(t)
	if code := f.run(context.Background()); code != exitOK {
		t.Fatalf("exit %d: %s\n%s", code, f.stderr.String(), f.stdout.String())
	}
	kills := 0
	for _, l := range f.agentLog() {
		if strings.HasPrefix(l, "gm_kill ") {
			kills++
		}
	}
	if kills == 0 {
		t.Fatal("no gm_kill went out under --reuse-instance --gm-clear")
	}
	if !hasNote(f.summary(), "ASSISTED") {
		t.Fatalf("notes %q, want the ASSISTED mark", f.summary().Notes)
	}
}

// A raid that is not alive in the plan's map refuses the reuse instead of entering a fresh instance.
// Walk 33: a raid that logged out with no live bind is said so, and the walk falls back to a fresh entry (it used to
// refuse with exit 2, leaving the operator to burn an instance by hand).
func TestReuseInstanceFallsBackWhenTheRaidAndBindAreGone(t *testing.T) {
	f, _ := newPlaceFixture(t)
	f.opts.reuseInstance = true
	f.agent.entered = false
	f.agent.startReply = zgPlaceReply(t)
	if code := f.run(context.Background()); code != exitOK {
		t.Fatalf("exit %d: %s", code, f.stderr.String())
	}
	out := f.stderr.String() + f.stdout.String()
	sum := f.summary()
	if !hasNote(sum, "--reuse-instance: the raid is not alive in map 309 (dead: none; not seen: ") {
		t.Fatalf("notes %q, want the not-alive report", sum.Notes)
	}
	if !hasNote(sum, "no live bind for map 309 is left: falling back to a fresh instance") {
		t.Fatalf("notes %q, want the fresh fallback", sum.Notes)
	}
	enters := 0
	for _, l := range f.agentLog() {
		if strings.HasPrefix(l, "cmdr cmd enter") {
			enters++
		}
	}
	if enters != 1 {
		t.Fatalf("%d entry orders, want exactly one for the fresh fallback: %q", enters, out)
	}
}

// Walk 33: the previous party logged out, but the commander's own bind to the map is alive: .go xyz lands in it and
// the fresh hires are summoned in, so no fresh instance is entered or counted.
func TestReuseInstanceResumesTheExistingBind(t *testing.T) {
	f, p := newPlaceFixture(t)
	f.opts.reuseInstance = true
	f.agent.entered = false
	f.agent.instanceBinds = []fakeBind{{inst: 107, ttr: "1h 0m"}}
	p.inst = 107
	f.agent.startReply = zgPlaceReply(t)
	if code := f.run(context.Background()); code != exitOK {
		t.Fatalf("exit %d: %s\n%s", code, f.stderr.String(), f.stdout.String())
	}
	sum := f.summary()
	if sum.Place == nil || sum.Place.Instance != 107 {
		t.Fatalf("place summary %+v, want instance 107", sum.Place)
	}
	if !hasNote(sum, "still bound to map 309 instance 107") ||
		!hasNote(sum, "resumed instance 107 from the commander's existing bind") {
		t.Fatalf("notes %q, want the bind resume", sum.Notes)
	}
	appears, enters := 0, 0
	for _, l := range f.agentLog() {
		if strings.HasPrefix(l, "gm .appear ") {
			appears++
		}
		if strings.HasPrefix(l, "cmdr cmd enter") {
			enters++
		}
		if l == "gm .instance listbinds" {
			// the read itself
		}
	}
	if appears != 0 || enters != 0 {
		t.Fatalf("appear %d, enter %d, want neither on a bind resume", appears, enters)
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
	if o, code := parseRaidWalkArgs(args, &stderr); code != exitOK || !o.reuseInstance || len(o.gmClear) != 1 {
		t.Fatalf("with --gm-clear: exit %d, options %+v, stderr %q (walk 33: the two are allowed together)", code, o, stderr.String())
	}
	stderr.Reset()
	args = []string{"zul_gurub", "--place", "--from", "leg high_priest_thekal", "--sql", "walk.sql", "--reuse-instance"}
	if o, code := parseRaidWalkArgs(args, &stderr); code != exitOK || !o.reuseInstance {
		t.Fatalf("plain reuse: exit %d, options %+v, stderr %q", code, o, stderr.String())
	}
}

// Walk 33: the teardown's `.instance unbind` left --reuse-instance with no bind after a stop, so every walk-super
// restart entered a fresh instance. A restart of the same cmd (--reuse-instance) keeps a live bind for the plan's
// map; a cmd that asks for a fresh instance, and an expired bind, still unbind.
func TestTeardownKeepsTheReuseBind(t *testing.T) {
	cases := []struct {
		name     string
		reuse    bool
		binds    []fakeBind
		wantKeep bool
	}{
		{"reuse keeps a live bind", true, []fakeBind{{inst: 107, ttr: "1h 0m"}}, true},
		{"reuse drops an expired bind", true, []fakeBind{{inst: 107, ttr: "0s"}}, false},
		{"fresh placement unbinds", false, []fakeBind{{inst: 107, ttr: "1h 0m"}}, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f := newFixture(t)
			f.agent.instMap = 309
			f.agent.instanceBinds = c.binds
			f.agent.status.Map = zgPlan(t).Portal.Map
			w := leaveWalk(t, f, f.clock.sleep)
			w.opts.reuseInstance = c.reuse
			w.placeLeave(context.Background())
			unbinds := 0
			for _, l := range f.agent.gmLines() {
				if strings.HasPrefix(l, ".instance unbind") {
					unbinds++
				}
			}
			if c.wantKeep {
				if unbinds != 0 {
					t.Fatalf("the teardown unbound the bind --reuse-instance resumes: %v", f.agent.gmLines())
				}
				if !strings.Contains(strings.Join(w.notes, "\n"), "keeps the commander's bind to map 309 (instance 107, TTR 1h 0m)") {
					t.Fatalf("notes %q, want the kept bind", w.notes)
				}
				return
			}
			if unbinds != 1 {
				t.Fatalf("%d unbinds, want one: %v (notes %q)", unbinds, f.agent.gmLines(), w.notes)
			}
		})
	}
}
