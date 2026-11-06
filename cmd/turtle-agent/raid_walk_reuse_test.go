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
	"github.com/pjerra/turtle-agent/internal/planexport"
	"github.com/pjerra/turtle-agent/internal/raidwalk"
	"github.com/pjerra/turtle-agent/internal/tbm"
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

// Walk 44 hk7m-3 / walk 46 hk4m-3: the raid wiped at Hakkar, the autopilot relaunched into the same instance, the
// summoned hires landed beside the live boss and died (19 of 20), and the placement burned all four tries there
// while the survivors fought him with no run. When a try finds half the party dead the walk stops at once with exit 29
// and a reason that asks for a fresh instance: no second summon wave, no "try 2 of 4".
func TestReuseGatherStopsAtOnceWhenMostOfThePartyIsDead(t *testing.T) {
	f, p := newPlaceFixture(t)
	f.opts.reuseInstance = true
	f.opts.noPlaceRetreat = false
	f.agent.entered = false
	f.agent.instanceBinds = []fakeBind{{inst: 107, ttr: "1h 0m"}}
	p.inst = 107
	f.agent.onGM = func(line string) {
		if name, ok := strings.CutPrefix(line, ".summon "); ok {
			f.agent.mu.Lock()
			if !strings.HasSuffix(name, "01") { // all but one die at the boss
				f.agent.dead[name] = true
			}
			f.agent.mu.Unlock()
		}
	}
	f.agent.startReply = zgPlaceReply(t)
	if code := f.run(context.Background()); code != exitRaidWalkPlaceFailed {
		t.Fatalf("exit %d, want 29: %s\n%s", code, f.stderr.String(), f.stdout.String())
	}
	sum := f.summary()
	if !strings.Contains(sum.EndReason, "fresh instance needed") || !strings.Contains(sum.EndReason, "party dead") {
		t.Fatalf("end reason %q, want the party-dead / fresh-instance reason", sum.EndReason)
	}
	if strings.Contains(sum.EndReason, "tries in the same instance") || hasNote(sum, "try 2 of") {
		t.Fatalf("the placement retried in the dead instance: %q %q", sum.EndReason, sum.Notes)
	}
}

// The same stop when the boss of the start leg is fighting the party (walk 46: Hakkar at 90 % targeting a hire).
func TestReuseGatherStopsAtOnceWhenTheBossIsEngaged(t *testing.T) {
	f, p := newPlaceFixture(t)
	f.opts.reuseInstance = true
	f.opts.noPlaceRetreat = false
	f.agent.entered = false
	f.agent.instanceBinds = []fakeBind{{inst: 107, ttr: "1h 0m"}}
	p.inst = 107
	f.agent.combatAllFn = func(int) bool { return true }
	rows, _ := placePlanRows(t)
	var boss uint32
	for _, r := range rows.Steps {
		if r.Kind == planexport.KindBoss && r.Leg == rows.Steps[zgPlaceSteps(t).start-1].Leg {
			boss = r.Entry
		}
	}
	if boss == 0 {
		t.Fatal("the start leg has no boss row")
	}
	f.agent.hostilesFn = func(int) []tbm.HostileRec {
		return []tbm.HostileRec{{GUID: 0xF130002C5800CAFA, Entry: boss, X: -12228.3, Y: -1680.7, Z: 126.7,
			HPPct: 90, Target: packet.GUID(1000)}}
	}
	f.agent.startReply = zgPlaceReply(t)
	if code := f.run(context.Background()); code != exitRaidWalkPlaceFailed {
		t.Fatalf("exit %d, want 29: %s\n%s", code, f.stderr.String(), f.stdout.String())
	}
	sum := f.summary()
	if !strings.Contains(sum.EndReason, "fresh instance needed") || !strings.Contains(sum.EndReason, "boss engaged") {
		t.Fatalf("end reason %q, want the boss-engaged / fresh-instance reason", sum.EndReason)
	}
	if hasNote(sum, "try 2 of") {
		t.Fatalf("the placement retried beside the boss: %q", sum.Notes)
	}
}

// Round 1 (LOW): a boss with no target that is only healing (an evading boss under 100 %) is not engaged with the
// party: the placement keeps its ordinary tries.
func TestReuseGatherIgnoresABossWithNoTarget(t *testing.T) {
	f, p := newPlaceFixture(t)
	f.opts.reuseInstance = true
	f.opts.noPlaceRetreat = false
	f.agent.entered = false
	f.agent.instanceBinds = []fakeBind{{inst: 107, ttr: "1h 0m"}}
	p.inst = 107
	f.agent.combatAllFn = func(int) bool { return true }
	rows, _ := placePlanRows(t)
	var boss uint32
	for _, r := range rows.Steps {
		if r.Kind == planexport.KindBoss && r.Leg == rows.Steps[zgPlaceSteps(t).start-1].Leg {
			boss = r.Entry
		}
	}
	f.agent.hostilesFn = func(int) []tbm.HostileRec {
		return []tbm.HostileRec{{GUID: 0xF130002C5800CAFA, Entry: boss, X: -12228.3, Y: -1680.7, Z: 126.7, HPPct: 90}}
	}
	f.agent.startReply = zgPlaceReply(t)
	if code := f.run(context.Background()); code != exitRaidWalkPlaceFailed {
		t.Fatalf("exit %d, want 29: %s", code, f.stderr.String())
	}
	if reason := f.summary().EndReason; strings.Contains(reason, "fresh instance needed") || !strings.Contains(reason, "4 tries in the same instance") {
		t.Fatalf("end reason %q, want the ordinary 4-try failure: the boss has no target", reason)
	}
}
