// raid_walk_inject_test.go: the command side of --inject (spec raid-walk-addons 1.6-1.8, task I2). The fakes are the
// A3 ones extended in raid_walk_cmd_test.go with the target RPC, `.gps`, per-bot positions and the P0 finding 4 lag.
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/pjerra/turtle-agent/internal/dungeon"
	"github.com/pjerra/turtle-agent/internal/events"
	"github.com/pjerra/turtle-agent/internal/packet"
	"github.com/pjerra/turtle-agent/internal/raidwalk"
	"github.com/pjerra/turtle-agent/internal/tbm"
)

// The onyxia plan's rows (planexport.Build of dungeons/onyxias_lair.json): step 1, the lead's stand (step 5) and the
// runaway's Q (step 2): 153.0 yd from the lead, inside runaway's 120-200 yd band.
const (
	injLeadX, injLeadY, injLeadZ = -166.623, -196.003, -66.2619
	injOwnGUID                   = packet.GUID(700)
)

// setupInject gives the fake a commander GUID and the bots' positions: the party stands at the lead's stand, the last
// rest hires 60 yd out (the runaway's victim), the others within 30 yd (the lag's victim).
func (f *fixture) setupInject() {
	f.agent.mu.Lock()
	defer f.agent.mu.Unlock()
	f.agent.ownGUID = injOwnGUID
	f.agent.status.GUID = injOwnGUID
	f.agent.pos = map[string][3]float32{}
	lead := [3]float32{injLeadX, injLeadY, injLeadZ}
	f.agent.pos["Bot01"] = lead
	for i := 2; i <= 30; i++ {
		f.agent.pos[fmt.Sprintf("Bot%02d", i)] = [3]float32{lead[0] + float32(i-1), lead[1], lead[2]}
	}
	for i := 31; i <= 39; i++ {
		f.agent.pos[fmt.Sprintf("Bot%02d", i)] = [3]float32{lead[0] + 60, lead[1], lead[2]}
	}
}

// injectRun queues the run's status lines with one step kind, then the until stop.
func (f *fixture) injectRun(kind string, last int) {
	var q []queuedEvent
	for step := 2; step <= last; step++ {
		q = append(q, queuedEvent{raw: fmt.Sprintf("TBM:RUN|onyxias_lair|running|%d/29|onyxia|%s|fight|fighting", step, kind)})
	}
	q = append(q, queuedEvent{raw: fmt.Sprintf("TBM:RUN|onyxias_lair|done|%d/29|onyxia|%s|fight|stopped before step 29 (until boss): the party stays where it stands", last, kind)})
	f.agent.mu.Lock()
	f.agent.startQueue = q
	f.agent.mu.Unlock()
}

// injFight makes two hostiles fight the party, both on the main tank, at 60 %.
func (f *fixture) injFight() {
	lead := [3]float32{injLeadX, injLeadY, injLeadZ}
	f.agent.mu.Lock()
	f.agent.hostilesFn = func(int) []tbm.HostileRec {
		return []tbm.HostileRec{
			{GUID: 9001, Entry: 12129, X: lead[0] + 5, Y: lead[1], Z: lead[2], HPPct: 60, Target: packet.GUID(1000)},
			{GUID: 9002, Entry: 12129, X: lead[0] + 6, Y: lead[1], Z: lead[2], HPPct: 60, Target: packet.GUID(1000)},
		}
	}
	f.agent.mu.Unlock()
}

// injectJSON reads <out>/inject.json.
func (f *fixture) injectJSON() map[string]any {
	f.t.Helper()
	raw, err := os.ReadFile(filepath.Join(f.out, "inject.json"))
	if err != nil {
		f.t.Fatalf("inject.json: %v", err)
	}
	var v map[string]any
	if err := json.Unmarshal(raw, &v); err != nil {
		f.t.Fatal(err)
	}
	return v
}

// gmIndex is the first gm line with the prefix (-1 when none).
func (f *fixture) gmIndex(prefix string) int {
	for i, line := range f.agent.gmLines() {
		if strings.HasPrefix(line, prefix) {
			return i
		}
	}
	return -1
}

// targetGUIDs is the fake's target RPC calls.
func (f *fixture) targetGUIDs() []packet.GUID {
	f.agent.mu.Lock()
	defer f.agent.mu.Unlock()
	return append([]packet.GUID{}, f.agent.targetCalls...)
}

func TestRaidWalkInjectDryRunListsFaultLines(t *testing.T) {
	f := newFixture(t)
	f.opts.dryRun = true
	f.opts.injects = []string{"kill-mt", "runaway", "add-pack", "lag"}
	if code := f.run(context.Background()); code != exitOK {
		t.Fatalf("dry run exit %d: %s", code, f.stderr.String())
	}
	got := f.stdout.String()
	want := `faults:
  prepare: .gm visible off; .go xyz <step-1> <map>; wait for the commander's map 30 s; wait for the hires' bot_view positions 20 s
  kill-mt (from step 1): on a pull step, a fight on for 6 s, 2+ hostiles on the party, the MT targeted, their mean health 20-90%, an OT alive
    .die
  runaway (from step 1, dist 150 yd, hold 90 s): on a move step, no fight for 10 s, a rest hire alive and out of combat, a plan point 120-200 yd from the lead bot
    .go xyz <Q> <map>; .summon <victim>; .freeze <victim>; .unfreeze <victim>
  add-pack (from step 1, max 60 yd): on a pull step, a fight on for 3 s, an idle hostile 20-60 yd from the lead bot
    attack <pack-guid> <puller>
  lag (from step 1, secs 30): on a move step, no fight for 10 s, a rest hire within 30 yd of the lead bot
    .freeze <victim>; .unfreeze <victim>
`
	if !strings.HasSuffix(got, want) {
		t.Fatalf("dry-run plan does not end with the fault block:\n%s\nwant suffix:\n%s", got, want)
	}
}

func TestRaidWalkInjectKillMTVerifiesSelectionBeforeDie(t *testing.T) {
	t.Run("status target mismatched", func(t *testing.T) {
		f := newFixture(t)
		f.setupInject()
		f.injFight()
		f.injectRun("pull", 20)
		f.agent.statusTargetMismatch = true
		f.opts.injects = []string{"kill-mt"}
		if code := f.run(context.Background()); code != exitOK {
			t.Fatalf("exit %d: %s", code, f.stderr.String())
		}
		if i := f.gmIndex(".die"); i >= 0 {
			t.Fatalf("`.die` was sent on a mismatched status.target (gm %v)", f.agent.gmLines())
		}
		sum := f.summary()
		if len(sum.Injections) != 1 || sum.Injections[0].Verdict != raidwalk.VerdictInconclusive {
			t.Fatalf("injections %+v, want one inconclusive", sum.Injections)
		}
		if !strings.Contains(sum.Injections[0].Reason, "selection not confirmed") {
			t.Fatalf("reason %q", sum.Injections[0].Reason)
		}
	})

	t.Run("matched", func(t *testing.T) {
		f := newFixture(t)
		f.setupInject()
		f.injFight()
		f.injectRun("pull", 20)
		f.opts.injects = []string{"kill-mt"}
		_ = f.run(context.Background())
		die := f.gmIndex(".die")
		if die < 0 {
			t.Fatalf("no `.die` after a verified selection (gm %v, injections %+v)", f.agent.gmLines(), f.summary().Injections)
		}
		targets := f.targetGUIDs()
		var mt, self int
		for _, g := range targets {
			switch g {
			case packet.GUID(1000):
				mt++
			case injOwnGUID:
				self++
			}
		}
		if mt != 1 || self < 1 {
			t.Fatalf("target RPCs %v, want the MT then self", targets)
		}
		// The self selection comes after `.die` (the injector selects self right after it).
		var after int
		for _, g := range targets[1:] {
			if g == injOwnGUID {
				after++
			}
		}
		if after < 1 {
			t.Fatalf("no self selection after the MT (%v)", targets)
		}
	})
}

// TestRaidWalkInjectSelectionWaitsOutALaggingServer pins P0 finding 4: the server applies a target one command later,
// so the walk waits 2 s, reads `.gps` (an observation only), then proves the identity with `.pinfo`. One attempt
// suffices: the command that follows the lagged target applies it before the identity read.
func TestRaidWalkInjectSelectionWaitsOutALaggingServer(t *testing.T) {
	t.Run("the lag is waited out", func(t *testing.T) {
		f := newFixture(t)
		f.setupInject()
		f.injFight()
		f.injectRun("pull", 20)
		f.agent.lagTargets = true
		f.opts.injects = []string{"kill-mt"}
		_ = f.run(context.Background())
		die := f.gmIndex(".die")
		if die < 0 {
			t.Fatalf("the lagging selection was never confirmed (gm %v)", f.agent.gmLines())
		}
		gps := 0
		for _, line := range f.agent.gmLines()[:die] {
			if line == ".gps" {
				gps++
			}
		}
		if gps != 1 {
			t.Fatalf("%d .gps reads before .die, want 1 (the identity read proves the lagged selection): %v", gps, f.agent.gmLines())
		}
		if pinfo, die := f.gmIndex(".pinfo"), f.gmIndex(".die"); pinfo < 0 || pinfo > die {
			t.Fatalf(".pinfo (%d) is not before .die (%d): %v", pinfo, die, f.agent.gmLines())
		}
		var mtTargets int
		for _, g := range f.targetGUIDs() {
			if g == packet.GUID(1000) {
				mtTargets++
			}
		}
		if mtTargets != 1 {
			t.Fatalf("target RPCs %v, want the MT once", f.targetGUIDs())
		}
		f.agent.mu.Lock()
		firstTarget := f.agent.targetAt[0]
		gpsAt := time.Time{}
		for i, line := range f.agent.gm {
			if line == ".gps" {
				gpsAt = f.agent.gmAt[i]
				break
			}
		}
		f.agent.mu.Unlock()
		if gpsAt.Sub(firstTarget) < 2*time.Second {
			t.Fatalf(".gps came %.2f s after the target, want at least 2 s", gpsAt.Sub(firstTarget).Seconds())
		}
	})

	t.Run("a server that never applies it aborts", func(t *testing.T) {
		f := newFixture(t)
		f.setupInject()
		f.injFight()
		f.injectRun("pull", 20)
		f.agent.noSelect = true
		f.opts.injects = []string{"kill-mt"}
		if code := f.run(context.Background()); code != exitOK {
			t.Fatalf("exit %d: %s", code, f.stderr.String())
		}
		if i := f.gmIndex(".die"); i >= 0 {
			t.Fatalf("`.die` was sent although .pinfo never named the MT (gm %v)", f.agent.gmLines())
		}
		sum := f.summary()
		if len(sum.Injections) != 1 || sum.Injections[0].Verdict != raidwalk.VerdictInconclusive {
			t.Fatalf("injections %+v", sum.Injections)
		}
	})
}

func TestRaidWalkInjectRunawayLineOrder(t *testing.T) {
	f := newFixture(t)
	f.setupInject()
	f.defaultRun("onyxias_lair")
	f.opts.injects = []string{"runaway"}
	const victim = "Bot39"
	f.agent.onGM = func(line string) {
		if line != ".freeze "+victim {
			return
		}
		f.agent.mu.Lock()
		lead := f.agent.pos["Bot01"]
		f.agent.pos[victim] = lead
		note := fmt.Sprintf("TBM:RUN|onyxias_lair|running|20/29|onyxia|move|walk|%s is 152.3 yd from the lead bot (8 s out of reach): summoned", victim)
		f.agent.lines = append([]queuedEvent{{raw: note}}, f.agent.lines...)
		f.agent.mu.Unlock()
	}
	if code := f.run(context.Background()); code != exitOK {
		t.Fatalf("exit %d: %s\n%s", code, f.stderr.String(), f.stdout.String())
	}
	order := []string{
		".gm visible off",
		".go xyz 49.8206 -114.607 -24.3112 249",
		".go xyz -52.97 -96.88 -38.64 249",
		".summon " + victim,
		".freeze " + victim,
		".gps",
		".unfreeze " + victim,
	}
	at := -1
	for _, want := range order {
		next := -1
		for i := at + 1; i < len(f.agent.gmLines()); i++ {
			if f.agent.gmLines()[i] == want {
				next = i
				break
			}
		}
		if next < 0 {
			t.Fatalf("%q is not after %d in the gm lines:\n%v", want, at, f.agent.gmLines())
		}
		at = next
	}
	sum := f.summary()
	if len(sum.Injections) != 1 || sum.Injections[0].Verdict != raidwalk.VerdictPass {
		t.Fatalf("injections %+v, want one pass", sum.Injections)
	}
	inj := f.injectJSON()
	if inj["format"] != "tb-raid-inject" || inj["version"] != float64(1) || inj["plan"] != "onyxias_lair" {
		t.Fatalf("inject.json header %v", inj)
	}
	faults, _ := inj["faults"].([]any)
	if len(faults) != 1 {
		t.Fatalf("inject.json faults %v", inj["faults"])
	}
	if got := faults[0].(map[string]any)["verdict"]; got != raidwalk.VerdictPass {
		t.Fatalf("inject.json verdict %v", got)
	}
}

func TestRaidWalkInjectCleanupOnInterrupt(t *testing.T) {
	f := newFixture(t)
	f.setupInject()
	f.defaultRun("onyxias_lair")
	f.opts.injects = []string{"runaway"}
	const victim = "Bot39"
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	f.agent.onGM = func(line string) {
		if line == ".freeze "+victim {
			cancel()
		}
	}
	if code := f.run(ctx); code != exitRaidWalkFailed {
		t.Fatalf("exit %d, want %d: %s", code, exitRaidWalkFailed, f.stderr.String())
	}
	freeze, unfreeze, stop := f.gmIndex(".freeze "+victim), f.gmIndex(".unfreeze "+victim), f.gmIndex(".bot run stop")
	if freeze < 0 || unfreeze < 0 {
		t.Fatalf("freeze %d unfreeze %d in %v", freeze, unfreeze, f.agent.gmLines())
	}
	if unfreeze > stop {
		t.Fatalf(".unfreeze (%d) does not come before .bot run stop (%d)", unfreeze, stop)
	}
	if len(f.rec.teardown) == 0 {
		t.Fatalf("no teardown after the interrupt")
	}
}

func TestRaidWalkInjectFailSetsExit23(t *testing.T) {
	f := newFixture(t)
	f.setupInject()
	f.injFight()
	f.injectRun("pull", 20)
	f.opts.injects = []string{"kill-mt"}
	code := f.run(context.Background())
	if code != exitRaidWalkInjectFailed {
		t.Fatalf("exit %d, want %d: %s", code, exitRaidWalkInjectFailed, f.stderr.String())
	}
	sum := f.summary()
	if len(sum.Injections) != 1 || sum.Injections[0].Verdict != raidwalk.VerdictFail {
		t.Fatalf("injections %+v, want one fail", sum.Injections)
	}
	if sum.ExitCode != exitRaidWalkInjectFailed {
		t.Fatalf("summary exit %d", sum.ExitCode)
	}
}

func TestRaidWalkInjectWipeKeepsExit19(t *testing.T) {
	f := newFixture(t)
	// No recoveries left: a fault wipe ends the walk exactly as without the fault (decision 3). With
	// --wipe-resume left the wipe would recover and the run would go on.
	f.opts.wipeResume = 0
	f.setupInject()
	f.injFight()
	f.injectRun("pull", 20)
	f.opts.injects = []string{"kill-mt"}
	f.agent.onGM = func(line string) {
		if line != ".die" {
			return
		}
		f.agent.mu.Lock()
		for _, n := range f.agent.party {
			f.agent.dead[n] = true
		}
		f.agent.mu.Unlock()
	}
	code := f.run(context.Background())
	if code != exitRaidWalkIncident {
		t.Fatalf("exit %d, want %d: %s", code, exitRaidWalkIncident, f.stderr.String())
	}
	sum := f.summary()
	if sum.ExitCode != exitRaidWalkIncident {
		t.Fatalf("summary exit %d", sum.ExitCode)
	}
	if len(sum.Injections) != 1 || sum.Injections[0].Verdict != raidwalk.VerdictFail {
		t.Fatalf("injections %+v, want one fail", sum.Injections)
	}
}

// TestRaidWalkInjectNotReadySkipsAll: without bot_view party positions there is nothing to measure, so every fault is
// inconclusive and the walk sends no selected-target line (P0 finding 1: the commander's own units are not the test).
func TestRaidWalkInjectNotReadySkipsAll(t *testing.T) {
	f := newFixture(t)
	f.setupInject()
	f.defaultRun("onyxias_lair")
	f.opts.injects = []string{"runaway"}
	f.agent.onGM = func(line string) {
		if line == ".gm visible off" {
			f.agent.mu.Lock()
			f.agent.noPartyFrames = true
			f.agent.mu.Unlock()
		}
	}
	if code := f.run(context.Background()); code != exitOK {
		t.Fatalf("exit %d: %s", code, f.stderr.String())
	}
	for _, line := range f.agent.gmLines() {
		if line == ".die" || strings.HasPrefix(line, ".freeze ") || strings.HasPrefix(line, ".unfreeze ") ||
			strings.HasPrefix(line, ".summon ") {
			t.Fatalf("a selected-target line went out without bot positions: %q", line)
		}
	}
	if targets := f.targetGUIDs(); len(targets) != 0 {
		t.Fatalf("target RPCs %v", targets)
	}
	sum := f.summary()
	if len(sum.Injections) != 1 || sum.Injections[0].Verdict != raidwalk.VerdictInconclusive {
		t.Fatalf("injections %+v, want one inconclusive", sum.Injections)
	}
	if !strings.Contains(sum.Injections[0].Reason, "bot_view") {
		t.Fatalf("reason %q", sum.Injections[0].Reason)
	}
}

func TestRaidWalkInjectEveryLinePassesAnAllowList(t *testing.T) {
	f := newFixture(t)
	f.setupInject()
	f.defaultRun("onyxias_lair")
	f.opts.injects = []string{"runaway"}
	f.agent.injectCtx = captureInjectorCtx(t)
	const victim = "Bot39"
	f.agent.onGM = func(line string) {
		if line != ".freeze "+victim {
			return
		}
		f.agent.mu.Lock()
		lead := f.agent.pos["Bot01"]
		f.agent.pos[victim] = lead
		note := fmt.Sprintf("TBM:RUN|onyxias_lair|running|20/29|onyxia|move|walk|%s is 152.3 yd from the lead bot (8 s out of reach): summoned", victim)
		f.agent.lines = append([]queuedEvent{{raw: note}}, f.agent.lines...)
		f.agent.mu.Unlock()
	}
	if code := f.run(context.Background()); code != exitOK {
		t.Fatalf("exit %d: %s", code, f.stderr.String())
	}
	hires := map[string]bool{}
	for i := 1; i <= 39; i++ {
		hires[fmt.Sprintf("Bot%02d", i)] = true
	}
	ctxs, haveCtx := f.agent.gmContexts()
	for i, line := range f.agent.gmLines() {
		if isInjectorLine(line) && i < len(ctxs) && haveCtx[i] {
			// The injector's line: the context it was really sent with, not a union of plausible ones.
			if !raidwalk.AllowedInjectLine(line, ctxs[i]) {
				t.Fatalf("gm line %q (send %d) fails the fault allow-list at its own context %+v", line, i, ctxs[i])
			}
			continue
		}
		if !raidwalk.AllowedLine(line, hires) {
			t.Fatalf("gm line %q (send %d) passes no allow-list", line, i)
		}
	}
}

// isInjectorLine says whether a gm line can only come from the fault injector once the watch runs (the walk sends
// none of these after Prepare; .bot run stop is the walk's own).
func isInjectorLine(line string) bool {
	f := strings.Fields(line)
	if len(f) == 0 {
		return false
	}
	switch f[0] {
	case ".gm", ".gps", ".pinfo", ".die", ".go", ".summon", ".freeze", ".unfreeze":
		return true
	}
	return false
}

// captureInjectorCtx wraps the real injector and returns its allow-list context at the moment a gm line goes out; there
// is no context before Prepare, when only the walk sends lines.
func captureInjectorCtx(t *testing.T) func() (raidwalk.InjectCtx, bool) {
	t.Helper()
	var mu sync.Mutex
	var inj *raidInjector
	old := newWalkInjector
	newWalkInjector = func(w *walk, specs []string) (walkInjector, error) {
		v, err := newRaidInjector(w, specs)
		if r, ok := v.(*raidInjector); ok {
			mu.Lock()
			inj = r
			mu.Unlock()
		}
		return v, err
	}
	t.Cleanup(func() { newWalkInjector = old })
	return func() (raidwalk.InjectCtx, bool) {
		mu.Lock()
		defer mu.Unlock()
		if inj == nil || inj.in == nil {
			return raidwalk.InjectCtx{}, false
		}
		return inj.in.Ctx(), true
	}
}

// TestRaidInjectPageReadKeepsTheWholePage: the `.gps`/`.pinfo` readers must process the WHOLE events page, not return
// at their own reply line: page.Next has already advanced past the rest, so a TBM:RUN note behind the reply (an F4
// summon, a pause, the run's done) would be lost for good - neither in run.log nor in the notes the injector drains.
func TestRaidInjectPageReadKeepsTheWholePage(t *testing.T) {
	f := newFixture(t)
	logPath := filepath.Join(t.TempDir(), "run.log")
	logF, err := os.Create(logPath)
	if err != nil {
		t.Fatal(err)
	}
	defer logF.Close()
	const reply = `Human Warrior |cffffffff|Hplayer:Bot03|h[Bot03]|h|r (guid: 1002) Account: acct (id: 1) GMLevel: 3`
	const note = "Bot39 is 152.3 yd from the lead bot (8 s out of reach): summoned"
	runLine := "TBM:RUN|onyxias_lair|running|6/29|lair|move|walk|" + note
	w := &walk{plan: &dungeon.Plan{Name: "onyxias_lair", Map: 249}, env: walkEnv{now: f.clock.now}, logF: logF}
	r := &raidInjector{w: w}
	got := r.readEventsPage(events.Page{
		Next: 2,
		Events: []events.Event{
			{Seq: 1, Kind: events.KindSystem, Text: reply},
			{Seq: 2, Kind: events.KindSystem, Text: runLine, Data: map[string]any{"raw": runLine}},
		},
	})
	if !got.haveID || got.name != "Bot03" || got.counter != 1002 {
		t.Fatalf("identity %+v, want Bot03 (guid counter 1002)", got)
	}
	if len(w.newNotes) != 1 || w.newNotes[0] != note {
		t.Fatalf("newNotes %v, want the page's note %q", w.newNotes, note)
	}
	if !w.haveStatus || w.lastStatus.Step != 6 || w.lastStatus.Note != note {
		t.Fatalf("lastStatus %+v (have %v), want step 6 with the note", w.lastStatus, w.haveStatus)
	}
	raw, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), runLine) {
		t.Fatalf("run.log lacks the page's RUN line:\n%s", raw)
	}
}
