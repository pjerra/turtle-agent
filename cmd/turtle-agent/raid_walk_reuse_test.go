package main

// --reuse-instance (walk 32): a re-place that keeps the raid's current instance. The instance cap made every
// re-place wait 15-20 minutes (the core allows 5 fresh ids per account per hour), and the tool had no way to move the
// party inside the copy it was already in. These tests drive a fake server: the raid is already entered, the ledger
// is at the cap, and the walk must neither enter a fresh copy nor count one.

import (
	"bytes"
	"context"
	"fmt"
	"math"
	"path/filepath"
	"strconv"
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

// Walk 80 (#446): the bind resume was the only placement that never read the quiet. Attempt 3 resumed the Hakkar leg
// at the point the deployed plan numbers step 689 (-11807.651 -1691.3049; the plan JSON's own export carries it as an
// earlier step), 2.4 yd from waypoint 7 of the altar patrols (-11806 -1693, 11359), and the raid was wiped 27 s after
// the summon. The quiet must go out after the GM-clear lines and before the first `.summon`; a not-quiet read is
// answered by GM-killing the unit it named and reading again, and no `.summon` may go out while the area is not quiet.
func TestReuseInstanceBindResumeReadsTheQuietBeforeSummoning(t *testing.T) {
	f, p := newPlaceFixture(t)
	f.opts.reuseInstance = true
	f.opts.gmClear = stringList{"2:2"}
	f.opts.gmClearSpawns = gmClearZGSpawns
	f.agent.entered = false
	f.agent.instanceBinds = []fakeBind{{inst: 107, ttr: "1h 0m"}}
	p.inst = 107
	res, err := planexport.Build(zgPlan(t), planexport.Options{})
	if err != nil {
		t.Fatal(err)
	}
	rows := res.Rows
	// The row the walk started at: the Hakkar leg's boss-approach point of walk 80 (the deployed plan's step 689).
	at := 0
	for i, r := range rows.Steps {
		if math.Abs(float64(r.X)+11807.651) < 0.05 && math.Abs(float64(r.Y)+1691.3049) < 0.05 {
			at = i + 1
			break
		}
	}
	if at == 0 {
		t.Fatal("no row at the Hakkar boss-approach point (-11807.651 -1691.3049): the walk 80 start is gone from the plan")
	}
	start, leg, _, _, err := raidwalk.PlaceStart(rows, []string{"from", "step", strconv.Itoa(at)})
	if err != nil {
		t.Fatal(err)
	}
	f.opts.from = fmt.Sprintf("step %d", at)
	point, ok := raidwalk.PlacePoint(rows, start)
	if !ok {
		t.Fatal("the start step has no point")
	}
	// The patrols' waypoint 7 (movement.tsv, WHY-446): it must stand inside the landing reach of the placement point,
	// or the read would not block.
	if d := placeDist2(-11806, -1693, point.X, point.Y); d > placeReachYd {
		t.Fatalf("the patrol stands %.1f yd from the start point (%v %v %v), outside the %d yd reach", d, point.X, point.Y, point.Z, placeReachYd)
	}
	patrol := packet.GUID(uint64(packet.HighUnit)<<48 | 11359<<24 | 0x11F382)
	// The first quiet read sees the patrol; the retry's read is clean.
	p.quiet = func(n int) bool { return n >= 2 }
	p.quietNear = []any{map[string]any{"g": patrol.String(), "e": 11359, "n": "Hakkari Bloodkeeper",
		"p": []float32{-11806, -1693, 52.1}, "w": 1.0, "d": 2.4}}
	pack, guids := gmClearPack(t, gmClearZGSpawns)
	f.agent.unitsFn = func(int) []objects.Unit { return gmClearView(f, pack, guids, func(packet.GUID) bool { return false }) }
	until, err := raidwalk.PlaceStop(rows, []string{"until", "boss"}, start)
	if err != nil {
		t.Fatal(err)
	}
	f.agent.startReply = placeStartReplyLeg("zul_gurub", leg, start, len(rows.Steps), until)
	// The run's own status lines: the placed start walks two steps and stops at its until, on the Hakkar leg.
	f.agent.startQueue = []queuedEvent{
		{raw: fmt.Sprintf("TBM:RUN|zul_gurub|running|%d/%d|%s|move|walk|walking", start, len(rows.Steps), leg)},
		{raw: fmt.Sprintf("TBM:RUN|zul_gurub|running|%d/%d|%s|move|walk|walking", start+1, len(rows.Steps), leg)},
		{raw: fmt.Sprintf("TBM:RUN|zul_gurub|done|%d/%d|%s|move|walk|stopped before step %d (until boss): the party stays where it stands", start+1, len(rows.Steps), leg, until)},
	}
	if code := f.run(context.Background()); code != exitOK {
		t.Fatalf("exit %d: %s\n%s", code, f.stderr.String(), f.stdout.String())
	}
	log := f.agentLog()
	firstQuiet, lastQuiet, firstSummon, lastClear, patrolKill := -1, -1, -1, -1, -1
	for i, l := range log {
		switch {
		case strings.HasPrefix(l, "tt quiet "):
			if firstQuiet < 0 {
				firstQuiet = i
			}
			lastQuiet = i
		case strings.HasPrefix(l, "gm_kill "):
			if firstQuiet < 0 {
				lastClear = i
			}
			if l == "gm_kill "+patrol.String() {
				patrolKill = i
			}
		case strings.HasPrefix(l, "gm .summon ") && firstSummon < 0:
			firstSummon = i
		}
	}
	if firstQuiet < 0 {
		t.Fatalf("no .tt quiet went out on the bind resume (walk 80's landing fight)\n%s", strings.Join(log, "\n"))
	}
	if lastClear < 0 || lastClear > firstQuiet {
		t.Fatalf("no GM-clear line before the first quiet (last at %d, quiet at %d)\n%s", lastClear, firstQuiet, strings.Join(log, "\n"))
	}
	if patrolKill < 0 || patrolKill < firstQuiet || patrolKill > lastQuiet {
		t.Fatalf("the patrol %s was not GM-killed between the two quiet reads (kill %d, reads %d..%d)\n%s",
			patrol, patrolKill, firstQuiet, lastQuiet, strings.Join(log, "\n"))
	}
	if firstSummon < 0 {
		t.Fatalf("no .summon went out\n%s", strings.Join(log, "\n"))
	}
	if lastQuiet > firstSummon {
		t.Fatalf("a .summon (line %d) went out before the quiet read that answered quiet (line %d): the raid would land in the patrols", firstSummon, lastQuiet)
	}
	sum := f.summary()
	if !hasNote(sum, "GM-killing the units the quiet named and reading it again (try 2 of 4, no `.summon` went out)") {
		t.Fatalf("notes %q, want the quiet retry", sum.Notes)
	}
	if sum.Place == nil || !sum.Place.Quiet {
		t.Fatalf("place summary %+v, want quiet", sum.Place)
	}
}

// Round 1 (LOW): placeReuse (the raid is alive in map, `.appear` opens no copy) reads the quiet at the same point of
// the sequence as placeResume: after the GM-clear lines and before the first `.summon`. A not-quiet read is answered
// by GM-killing the unit it named and reading again; no `.summon` goes out while the area is not quiet.
func TestReuseInstancePlaceReuseReadsTheQuietBeforeSummoning(t *testing.T) {
	f, p := newPlaceFixture(t)
	f.opts.reuseInstance = true
	f.opts.gmClear = stringList{"2:2"}
	f.opts.gmClearSpawns = gmClearZGSpawns
	f.agent.entered = true // the raid is alive in map 309: the placement is the reused copy
	_, plan := placePlanRows(t)
	patrol := packet.GUID(uint64(packet.HighUnit)<<48 | 11359<<24 | 0x11F382)
	// The first quiet read sees the patrol; the retry's read is clean.
	p.quiet = func(n int) bool { return n >= 2 }
	p.quietNear = []any{map[string]any{"g": patrol.String(), "e": 11359, "n": "Hakkari Bloodkeeper",
		"p": []float32{plan.Point.X + 2, plan.Point.Y, plan.Point.Z}, "w": 1.0, "d": 2.0}}
	pack, guids := gmClearPack(t, gmClearZGSpawns)
	f.agent.unitsFn = func(int) []objects.Unit { return gmClearView(f, pack, guids, func(packet.GUID) bool { return false }) }
	f.agent.startReply = zgPlaceReply(t)
	if code := f.run(context.Background()); code != exitOK {
		t.Fatalf("exit %d: %s\n%s", code, f.stderr.String(), f.stdout.String())
	}
	log := f.agentLog()
	firstQuiet, lastQuiet, firstSummon, lastClear, patrolKill, appear := -1, -1, -1, -1, -1, -1
	for i, l := range log {
		switch {
		case strings.HasPrefix(l, "tt quiet "):
			if firstQuiet < 0 {
				firstQuiet = i
			}
			lastQuiet = i
		case strings.HasPrefix(l, "gm_kill "):
			if firstQuiet < 0 {
				lastClear = i
			}
			if l == "gm_kill "+patrol.String() {
				patrolKill = i
			}
		case strings.HasPrefix(l, "gm .summon ") && firstSummon < 0:
			firstSummon = i
		case strings.HasPrefix(l, "gm .appear ") && appear < 0:
			appear = i
		}
	}
	if appear < 0 {
		t.Fatalf("no .appear went out: the reuse path was not taken\n%s", strings.Join(log, "\n"))
	}
	if firstQuiet < 0 {
		t.Fatalf("no .tt quiet went out on the reuse placement\n%s", strings.Join(log, "\n"))
	}
	if lastClear < 0 || lastClear > firstQuiet {
		t.Fatalf("no GM-clear line before the first quiet (last at %d, quiet at %d)\n%s", lastClear, firstQuiet, strings.Join(log, "\n"))
	}
	if patrolKill < 0 || patrolKill < firstQuiet || patrolKill > lastQuiet {
		t.Fatalf("the patrol %s was not GM-killed between the two quiet reads (kill %d, reads %d..%d)\n%s",
			patrol, patrolKill, firstQuiet, lastQuiet, strings.Join(log, "\n"))
	}
	if firstSummon < 0 {
		t.Fatalf("no .summon went out\n%s", strings.Join(log, "\n"))
	}
	if lastQuiet > firstSummon {
		t.Fatalf("a .summon (line %d) went out before the quiet read that answered quiet (line %d)", firstSummon, lastQuiet)
	}
	sum := f.summary()
	if !hasNote(sum, "GM-killing the units the quiet named and reading it again (try 2 of 4, no `.summon` went out)") {
		t.Fatalf("notes %q, want the quiet retry", sum.Notes)
	}
	if sum.Place == nil || !sum.Place.Quiet {
		t.Fatalf("place summary %+v, want quiet", sum.Place)
	}
}
