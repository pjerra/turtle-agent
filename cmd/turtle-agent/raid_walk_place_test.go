package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/pjerra/turtle-agent/internal/dungeon"
	"github.com/pjerra/turtle-agent/internal/objects"
	"github.com/pjerra/turtle-agent/internal/packet"
	"github.com/pjerra/turtle-agent/internal/planexport"
	"github.com/pjerra/turtle-agent/internal/raidwalk"
	"github.com/pjerra/turtle-agent/internal/tbm"
)

// ttReplyOf is a one-reply toolkit answer out of a body object.
func ttReplyOf(t *testing.T, body map[string]any) ttResult {
	t.Helper()
	return ttResult{Replies: []json.RawMessage{ttRaw(t, body)}}
}

// ttRaw is one reply of a toolkit answer, as the walk's reader sees it.
func ttRaw(t *testing.T, body map[string]any) json.RawMessage {
	t.Helper()
	raw, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

// ttPointOf reads the x y z of a `place x y z spread=.. of=..` command.
func ttPointOf(t *testing.T, cmd string) (x, y, z float32) {
	t.Helper()
	f := strings.Fields(cmd)
	if len(f) < 4 {
		t.Fatalf("place command %q", cmd)
	}
	var p [3]float32
	for i := range 3 {
		var v float64
		if _, err := fmt.Sscanf(f[1+i], "%g", &v); err != nil {
			t.Fatalf("place command %q field %d: %v", cmd, i, err)
		}
		p[i] = float32(v)
	}
	return p[0], p[1], p[2]
}

// placeHires moves the fake's whole party onto a point (what the toolkit's place does).
func placeHires(f *fixture, x, y, z float32) {
	f.agent.mu.Lock()
	defer f.agent.mu.Unlock()
	if f.agent.pos == nil {
		f.agent.pos = map[string][3]float32{}
	}
	for _, n := range f.agent.party {
		f.agent.pos[n] = [3]float32{x, y, z}
	}
}

// placeToolkit is a scripted Eluna toolkit for the place tests.
type placeToolkit struct {
	t    *testing.T
	f    *fixture
	mu   sync.Mutex // guards fields a test sets while the walk runs (instAfterSignal)
	inst float64    // the instance `q inst` and `q group` report
	// cmdInst, when set, is what `q inst` reports instead (a commander in another copy).
	cmdInst float64
	// afterPlace, when set, makes `q inst` report movedInst once the place has happened.
	afterPlace bool
	movedInst  float64
	// instAfterSignal makes the FIRST `q inst` after the test arms it report otherInst, later ones the normal inst
	// (walk 24 C2: the copy moved under a placed walk at the revive, then held: the appear path runs, the summons'
	// own check passes). armed is set by the test (the revive's `.gm visible off` marks its moment).
	instAfterSignal bool
	otherInst       float64
	instSignalAt    int
	// units is the pre-read's answer for one `q units` command; nil: an empty list.
	units func(cmd string) (units []any, trunc bool)
	// quiet is the answer of the n-th quiet read (nil: always quiet).
	quiet func(n int) bool
	// quietNear is the `near` list a not-quiet read reports (nil: none): the extra clear round's input.
	quietNear []any
	// quietRoamers is the `roamers` list a not-quiet read reports (nil: none). The toolkit sends no position for them:
	// the walk joins them to a `q units` read of its own (item 4).
	quietRoamers []any
	// clearto, when set, answers a `clearto` (n removed, or an error).
	clearto func(cmd string) (int, error)
	// place, when set, replaces the default place handler.
	place func(cmd string) error
	// placeAll says the default place handler moves every hire (the zero value leaves the last one behind).
	placeAll bool

	quietCalls int
	cleartos   []string
	places     []string
}

func (p *placeToolkit) answer(cmd string) (ttResult, error) {
	switch {
	case cmd == "q inst":
		inst := p.inst
		if p.cmdInst != 0 {
			inst = p.cmdInst
		} else if p.afterPlace && p.movedInst != 0 {
			inst = p.movedInst
		}
		p.mu.Lock()
		armed, other := p.instAfterSignal, p.otherInst
		if armed {
			p.instSignalAt++
			if p.instSignalAt > 1 {
				armed = false
			}
		}
		p.mu.Unlock()
		if armed {
			inst = other
		}
		return ttReplyOf(p.t, map[string]any{"map": 309, "inst": inst}), nil
	case strings.HasPrefix(cmd, "q group of="):
		return ttReplyOf(p.t, map[string]any{"map": 309, "inst": p.inst, "members": p.members()}), nil
	case strings.HasPrefix(cmd, "q floor"):
		f := strings.Fields(cmd)
		var z float64
		if len(f) >= 5 {
			fmt.Sscanf(f[4], "%g", &z)
		}
		return ttReplyOf(p.t, map[string]any{"map": 309, "inst": p.inst, "floor": z}), nil
	case strings.HasPrefix(cmd, "q units"):
		var units []any
		trunc := false
		if p.units != nil {
			units, trunc = p.units(cmd)
		}
		return ttReplyOf(p.t, map[string]any{"map": 309, "units": units, "n": len(units), "trunc": trunc}), nil
	case strings.HasPrefix(cmd, "clearto"):
		p.cleartos = append(p.cleartos, cmd)
		n := 0
		if p.clearto != nil {
			var err error
			if n, err = p.clearto(cmd); err != nil {
				return ttResult{}, err
			}
		}
		return ttReplyOf(p.t, map[string]any{"map": 309, "n": n, "removed": []any{}, "skipped": []any{}}), nil
	case strings.HasPrefix(cmd, "quiet"):
		p.quietCalls++
		q := true
		if p.quiet != nil {
			q = p.quiet(p.quietCalls)
		}
		near := []any{}
		if !q && p.quietNear != nil {
			near = p.quietNear
		}
		roamers := []any{}
		if !q && p.quietRoamers != nil {
			roamers = p.quietRoamers
		}
		return ttReplyOf(p.t, map[string]any{"map": 309, "read": 2, "final": true, "quiet": q, "near": near, "roamers": roamers}), nil
	case strings.HasPrefix(cmd, "place "):
		p.places = append(p.places, cmd)
		if p.place != nil {
			if err := p.place(cmd); err != nil {
				return ttResult{}, err
			}
		} else {
			x, y, z := ttPointOf(p.t, cmd)
			placeHires(p.f, x, y, z)
			if !p.placeAll {
				// One hire is left behind: the walk must summon it (or fail).
				p.f.agent.mu.Lock()
				if n := len(p.f.agent.party); n > 0 {
					delete(p.f.agent.pos, p.f.agent.party[n-1])
				}
				p.f.agent.mu.Unlock()
			}
		}
		p.afterPlace = true
		return ttReplyOf(p.t, map[string]any{"map": 309, "read": 2, "final": true, "placed": true, "members": []any{}}), nil
	}
	p.t.Errorf("unexpected toolkit command %q", cmd)
	return ttResult{}, nil
}

// members is the `q group` member list: the fake's party, all on the walk's map in the toolkit's instance.
func (p *placeToolkit) members() []any {
	p.f.agent.mu.Lock()
	defer p.f.agent.mu.Unlock()
	out := make([]any, 0, len(p.f.agent.party))
	for i, n := range p.f.agent.party {
		out = append(out, map[string]any{"g": fmt.Sprintf("0xF130%012d", i), "n": n, "map": 309, "inst": p.inst, "alive": true, "cb": false})
	}
	return out
}

// newPlaceFixture is a raid-walk fixture set for a --place walk of ZG's leg 3, with a plausible toolkit.
func newPlaceFixture(t *testing.T) (*fixture, *placeToolkit) {
	t.Helper()
	f := newFixture(t)
	f.opts.plan = "zul_gurub"
	f.opts.holder = "raid-walk-zul_gurub"
	f.opts.from = "leg high_priest_thekal"
	f.opts.place = true
	f.agent.instMap = 309
	p := &placeToolkit{t: t, f: f, inst: 103, placeAll: true}
	f.agent.ttFn = p.answer
	// The run's own status lines: the placed run walks two steps and stops at its until. A test that needs the raid to
	// die (or the run to go on) replaces this queue.
	n := zgPlaceSteps(t)
	f.agent.startQueue = []queuedEvent{
		{raw: fmt.Sprintf("TBM:RUN|zul_gurub|running|%d/%d|thekal|move|walk|walking", n.start, n.total)},
		{raw: fmt.Sprintf("TBM:RUN|zul_gurub|running|%d/%d|thekal|move|walk|walking", n.start+1, n.total)},
		{raw: fmt.Sprintf("TBM:RUN|zul_gurub|done|%d/%d|thekal|move|walk|stopped before step %d (until boss): the party stays where it stands", n.start+1, n.total, n.until)},
	}
	return f, p
}

// placeStartReply is the run start's answer for a placed walk: the run running at step of steps, with the until label.
func placeStartReply(plan string, step, steps, untilStep int) *commanderReply {
	lines := []string{
		fmt.Sprintf("TBM:RUN|%s|running|%d/%d|thekal|move|walk|walking|until boss (step %d)", plan, step, steps, untilStep),
		fmt.Sprintf("The run starts at step %d of %d (leg high_priest_thekal)", step, steps),
		"TBM:ACTION_ACK|run start|party|1|-",
	}
	return &commanderReply{Reply: tbm.Reply{Kind: tbm.KindAck, OK: true,
		Ack:  &tbm.Ack{Intent: "run start", Scope: "party", Count: 1, Executor: "-"},
		Line: lines[0], Lines: lines}}
}

// orderedAt says every want appears in log, in order, as a prefix of some line (each want matches a later line than the
// one before it).
func orderedAt(t *testing.T, log, want []string) {
	t.Helper()
	i := 0
	for _, line := range log {
		if i < len(want) && strings.HasPrefix(line, want[i]) {
			i++
		}
	}
	if i != len(want) {
		t.Fatalf("the call order misses %q (matched %d of %d)\nlog:\n%s", want[i], i, len(want), strings.Join(log, "\n"))
	}
}

func (f *fixture) agentLog() []string {
	f.agent.mu.Lock()
	defer f.agent.mu.Unlock()
	return append([]string{}, f.agent.log...)
}

// zgPlan is the fixture's plan file.
func zgPlan(t *testing.T) *dungeon.Plan {
	t.Helper()
	p, err := dungeon.Load("../../dungeons/zul_gurub.json")
	if err != nil {
		t.Fatal(err)
	}
	return p
}

// zgPlaceNums are the placed ZG walk's step numbers: the start step, the rows' step count and the until-boss stop.
type zgPlaceNums struct{ start, total, until int }

// zgPlaceSteps derives zgPlaceNums from the plan the fixture loads, through placePlanRows (planexport.Build,
// raidwalk.PlaceStart, raidwalk.PlaceStop: the functions the walk itself uses), so a plan change (#356 grew ZG from 397
// to 401 rows) cannot leave the tests' numbers behind.
func zgPlaceSteps(t *testing.T) zgPlaceNums {
	t.Helper()
	rows, plan := placePlanRows(t)
	return zgPlaceNums{start: plan.Start, total: len(rows.Steps), until: plan.Stop}
}

// zgPlaceReply is placeStartReply for the placed ZG walk: the server starts exactly the placed step.
func zgPlaceReply(t *testing.T) *commanderReply {
	t.Helper()
	n := zgPlaceSteps(t)
	return placeStartReply("zul_gurub", n.start, n.total, n.until)
}

func TestParsePlaceNeedsFrom(t *testing.T) {
	var stderr bytes.Buffer
	if _, code := parseRaidWalkArgs([]string{"onyxias_lair", "--place"}, &stderr); code != exitUsage ||
		!strings.Contains(stderr.String(), "--place needs --from") {
		t.Fatalf("exit %d, stderr %q", code, stderr.String())
	}
	stderr.Reset()
	if _, code := parseRaidWalkArgs([]string{"onyxias_lair", "--place", "--from", "step 5", "--sql", "walk.sql"}, &stderr); code != exitOK {
		t.Fatalf("a placed walk with a start: exit %d, stderr %q", code, stderr.String())
	}
	// --place=off is a plain walk: it needs no --from.
	stderr.Reset()
	if _, code := parseRaidWalkArgs([]string{"onyxias_lair", "--place=off"}, &stderr); code != exitOK {
		t.Fatalf("--place=off: exit %d, stderr %q", code, stderr.String())
	}
}

// The placed start is checked against the deployed rows, so --place requires --sql (review round 1, item 9).
func TestParsePlaceNeedsSQL(t *testing.T) {
	var stderr bytes.Buffer
	if _, code := parseRaidWalkArgs([]string{"onyxias_lair", "--place", "--from", "step 5"}, &stderr); code != exitUsage ||
		!strings.Contains(stderr.String(), "--place needs --sql") {
		t.Fatalf("exit %d, stderr %q", code, stderr.String())
	}
	stderr.Reset()
	if _, code := parseRaidWalkArgs([]string{"onyxias_lair", "--place", "--from", "step 5", "--sql", "walk.sql"}, &stderr); code != exitOK {
		t.Fatalf("with --sql: exit %d, stderr %q", code, stderr.String())
	}
}

func TestParsePlaceRefusesInject(t *testing.T) {
	var stderr bytes.Buffer
	args := []string{"onyxias_lair", "--place", "--from", "step 5", "--inject", "kill-mt"}
	if _, code := parseRaidWalkArgs(args, &stderr); code != exitUsage ||
		!strings.Contains(stderr.String(), "--place and --inject are not combined") {
		t.Fatalf("exit %d, stderr %q", code, stderr.String())
	}
}

func TestParsePlaceServerGuard(t *testing.T) {
	var stderr bytes.Buffer
	for _, server := range []string{"m910q", "tortoise-test2"} {
		stderr.Reset()
		if _, code := parseRaidWalkArgs([]string{"onyxias_lair", "--server", server, "--place", "--from", "step 5", "--sql", "walk.sql"}, &stderr); code != exitOK {
			t.Fatalf("%s: exit %d, stderr %q", server, code, stderr.String())
		}
	}
	stderr.Reset()
	if _, code := parseRaidWalkArgs([]string{"onyxias_lair", "--server", "tortoise", "--place", "--from", "step 5", "--sql", "walk.sql"}, &stderr); code != exitUsage ||
		!strings.Contains(stderr.String(), "test servers only") {
		t.Fatalf("exit %d, stderr %q", code, stderr.String())
	}
}

// The place phase's call order (spec 5, test 12).
func TestPhasePlaceOrder(t *testing.T) {
	f, p := newPlaceFixture(t)
	// The walk's own place plan decides the step: read it from the dry run first, then run for real.
	f.opts.dryRun = true
	if code := f.run(context.Background()); code != exitOK {
		t.Fatalf("dry run exit %d: %s", code, f.stderr.String())
	}
	step := 0
	for _, line := range strings.Split(f.stdout.String(), "\n") {
		if _, rest, ok := strings.Cut(line, "8b place: start step "); ok {
			fmt.Sscanf(rest, "%d", &step)
		}
	}
	if step == 0 {
		t.Fatalf("the dry run named no start step:\n%s", f.stdout.String())
	}
	f.opts.dryRun = false
	zg := zgPlaceSteps(t)
	if step != zg.start {
		t.Fatalf("the dry run starts step %d, the plan's place start is %d", step, zg.start)
	}
	f.agent.startReply = placeStartReply("zul_gurub", step, zg.total, zg.until)
	if code := f.run(context.Background()); code != exitOK {
		t.Fatalf("exit %d: %s\n%s", code, f.stderr.String(), f.stdout.String())
	}
	orderedAt(t, f.agentLog(), []string{
		"gm .instance unbind 309", // phase 5b
		"tt q inst",               // the probe, before anything moves
		"gm .gm on",               // the GM-mode probe, before the first teleport
		"cmdr cmd stay",
		"gm .appear Bot01",
		"tt q inst",
		"tt q group of=Bot01",
		"gm .go xyz ",
		"tt q inst",
		"tt q floor ",
		"tt q units ",
		"tt clearto 309 ",
		"tt quiet 50 ",
		"tt place ",
		"cmdr cmd stay",
		"cmdr cmd follow",
		"cmdr run start zul_gurub from step ",
	})
	if len(p.cleartos) == 0 {
		t.Fatal("no clearto was sent")
	}
	if !strings.HasSuffix(f.agent.startArgs, "|until|boss") {
		t.Fatalf("run start args %q", f.agent.startArgs)
	}
	sum := f.summary()
	if sum.Place == nil {
		t.Fatal("the summary has no place object")
	}
	if sum.Place.Start != step || sum.Place.Stop != zg.until || !sum.Place.Quiet {
		t.Fatalf("place summary %+v", sum.Place)
	}
	if sum.Place.Instance != 103 || sum.Place.Placed != sum.Place.Hires || sum.Place.Hires == 0 {
		t.Fatalf("place summary %+v", sum.Place)
	}
	if len(sum.Place.Circles) != len(p.cleartos) {
		t.Fatalf("place circles %d, cleartos %d", len(sum.Place.Circles), len(p.cleartos))
	}
	if sum.Place.Point[0] == 0 && sum.Place.Point[1] == 0 {
		t.Fatalf("place point %v", sum.Place.Point)
	}
	// The teardown leaves the raid's copy through the portal and drops the commander's bind there (spec 5.1).
	portal := zgPlan(t).Portal
	orderedAt(t, f.agentLog(), []string{
		"cmdr status",
		fmt.Sprintf("gm .go xyz %s %s %s %d", num32(portal.X), num32(portal.Y), num32(portal.Z), portal.Map),
		"gm .instance unbind 309",
	})
}

func TestPlaceInstanceMismatchClearsNothing(t *testing.T) {
	f, p := newPlaceFixture(t)
	p.cmdInst = 104 // the commander is in another copy
	f.agent.startReply = zgPlaceReply(t)
	code := f.run(context.Background())
	if code != exitRaidWalkFailed {
		t.Fatalf("exit %d, want 18: %s", code, f.stderr.String())
	}
	if len(p.cleartos) != 0 || len(p.places) != 0 {
		t.Fatalf("cleared %v and placed %v with the commander in another copy", p.cleartos, p.places)
	}
	if sum := f.summary(); !strings.Contains(sum.EndReason, "the commander is in instance 104, the raid in 103") {
		t.Fatalf("end reason %q", sum.EndReason)
	}
}

func TestPlacePreReadForeignUnitSkipsCircle(t *testing.T) {
	f, p := newPlaceFixture(t)
	p.units = func(string) ([]any, bool) {
		// A unit the row does not own stands in the circle.
		return []any{map[string]any{"g": "0xF1300000000009", "e": 999999, "n": "Stranger", "p": []float32{0, 0, 0}, "dz": 0.5, "d": 1.0}}, false
	}
	f.agent.startReply = zgPlaceReply(t)
	code := f.run(context.Background())
	if code != exitOK {
		t.Fatalf("exit %d: %s", code, f.stderr.String())
	}
	if len(p.cleartos) != 0 {
		t.Fatalf("clearto was sent for a circle holding a foreign unit: %v", p.cleartos)
	}
	sum := f.summary()
	if len(sum.Place.NotCleared) == 0 || !strings.Contains(sum.Place.NotCleared[0], "stands in the circle") {
		t.Fatalf("not_cleared %v", sum.Place.NotCleared)
	}
	if !hasNote(sum, "place: not cleared row") {
		t.Fatalf("notes %v", sum.Notes)
	}
}

func TestPlaceVerifyByFramesSummonsMissingOnce(t *testing.T) {
	f, p := newPlaceFixture(t)
	p.placeAll = false // one hire is left behind: the frames catch it, one summon fixes it
	f.agent.startReply = zgPlaceReply(t)
	code := f.run(context.Background())
	if code != exitOK {
		t.Fatalf("exit %d: %s\n%s", code, f.stderr.String(), f.stdout.String())
	}
	summons := 0
	for _, line := range f.agent.gmLines() {
		if strings.HasPrefix(line, ".summon ") {
			summons++
		}
	}
	if summons != 1 {
		t.Fatalf("%d summons, want one", summons)
	}
	sum := f.summary()
	if len(sum.Place.Summoned) != 1 || sum.Place.Placed != sum.Place.Hires-1 {
		t.Fatalf("place summary %+v", sum.Place)
	}
}

func TestPlaceVerifyFailsWhenStillMissing(t *testing.T) {
	f, p := newPlaceFixture(t)
	p.placeAll = false
	// The summon does not bring the hire to the point: the walk must fail after its second window.
	f.agent.onGM = func(line string) {
		if name, ok := strings.CutPrefix(line, ".summon "); ok {
			f.agent.mu.Lock()
			f.agent.pos[name] = [3]float32{0, 0, 0}
			f.agent.mu.Unlock()
		}
	}
	f.agent.startReply = zgPlaceReply(t)
	code := f.run(context.Background())
	if code != exitRaidWalkFailed {
		t.Fatalf("exit %d, want 18: %s", code, f.stderr.String())
	}
	if sum := f.summary(); !strings.Contains(sum.EndReason, "place: not at the start") {
		t.Fatalf("end reason %q", sum.EndReason)
	}
}

// The walk judges the place by its own frames, never by the toolkit's member lists (which truncate near a full raid).
func TestPlaceIgnoresTruncatedRead2(t *testing.T) {
	f, p := newPlaceFixture(t)
	f.agent.startReply = zgPlaceReply(t)
	f.agent.ttFn = func(cmd string) (ttResult, error) {
		if strings.HasPrefix(cmd, "place ") {
			p.places = append(p.places, cmd)
			x, y, z := ttPointOf(t, cmd)
			placeHires(f, x, y, z)
			p.afterPlace = true
			// 17 of 20 members listed, and the toolkit says it did not place them: the frames decide.
			members := make([]any, 0, 17)
			for i := range 17 {
				members = append(members, map[string]any{"n": fmt.Sprintf("Bot%02d", i+1), "ok": true})
			}
			return ttReplyOf(t, map[string]any{"map": 309, "read": 2, "final": true, "placed": false, "members": members}), nil
		}
		return p.answer(cmd)
	}
	code := f.run(context.Background())
	if code != exitOK {
		t.Fatalf("exit %d: %s\n%s", code, f.stderr.String(), f.stdout.String())
	}
	if sum := f.summary(); sum.Place.Placed != sum.Place.Hires {
		t.Fatalf("place summary %+v", sum.Place)
	}
}

func TestPlaceStartLineMismatchStops(t *testing.T) {
	f, _ := newPlaceFixture(t)
	zg := zgPlaceSteps(t)
	f.agent.startReply = placeStartReply("zul_gurub", zg.start+1, zg.total, zg.until) // the server starts another step
	code := f.run(context.Background())
	if code != exitRaidWalkFailed {
		t.Fatalf("exit %d, want 18: %s", code, f.stderr.String())
	}
	stopped := false
	for _, line := range f.agent.gmLines() {
		if line == ".bot run stop" {
			stopped = true
		}
	}
	if !stopped {
		t.Fatalf("the walk did not stop the run: %v", f.agent.gmLines())
	}
	if sum := f.summary(); !strings.Contains(sum.EndReason, "place: placed for step") {
		t.Fatalf("end reason %q", sum.EndReason)
	}
}

// wipeAfterStart kills the raid as soon as the walk reads a running status line (after the place).
func wipeAfterStart(f *fixture) {
	n := zgPlaceSteps(f.t)
	f.agent.startQueue = []queuedEvent{{raw: fmt.Sprintf("TBM:RUN|zul_gurub|running|%d/%d|thekal|move|walk|walking", n.start, n.total)}}
	f.agent.eventsHook = func(raw string) {
		if !strings.Contains(raw, "|running|") {
			return
		}
		f.agent.mu.Lock()
		for _, n := range f.agent.party {
			f.agent.dead[n] = true
		}
		f.agent.mu.Unlock()
	}
}

// A wipe revives at the place point, never at the plan's first point, and never falls back to the entrance (spec 6).
func TestReviveUsesPlacePointNoEntranceFallback(t *testing.T) {
	f, _ := newPlaceFixture(t)
	f.opts.wipeResume = 0
	f.agent.startReply = zgPlaceReply(t)
	// A hostile unit that follows the commander: the place point is never quiet, so the entrance fallback would run
	// if the placed walk kept it (review round 1, item 5): the test's entrance assertion is what turns red.
	f.agent.unitsFn = func(int) []objects.Unit {
		f.agent.mu.Lock()
		x, y, z := f.agent.x, f.agent.y, f.agent.z
		f.agent.mu.Unlock()
		return []objects.Unit{{Type: "creature", Name: "Warder", Hostile: true, X: x + 10, Y: y, Z: z}}
	}
	wipeAfterStart(f)
	code := f.run(context.Background())
	if code != exitRaidWalkIncident {
		t.Fatalf("exit %d, want 19: %s\n%s", code, f.stderr.String(), f.stdout.String())
	}
	sum := f.summary()
	// Every .go xyz after the run started: the first is the revive's, the last is the teardown's portal move.
	var after []string
	started := false
	for _, line := range f.agentLog() {
		if strings.HasPrefix(line, "cmdr run start ") {
			started = true
			continue
		}
		if started && strings.HasPrefix(line, "gm .go xyz ") {
			after = append(after, strings.TrimPrefix(line, "gm "))
		}
	}
	place := fmt.Sprintf(".go xyz %s %s ", num32(sum.Place.Point[0]), num32(sum.Place.Point[1]))
	if len(after) == 0 || !strings.HasPrefix(after[0], place) {
		t.Fatalf("the revive's .go xyz lines are %q, want the place point %v first", after, sum.Place.Point)
	}
	portal := zgPlan(t).Portal
	if !strings.HasPrefix(after[len(after)-1], fmt.Sprintf(".go xyz %s %s %s %d", num32(portal.X), num32(portal.Y), num32(portal.Z), portal.Map)) {
		t.Fatalf("the teardown's .go xyz is %q, want the portal", after[len(after)-1])
	}
	e := zgPlan(t).Entrance
	entrance := fmt.Sprintf(".go xyz %s %s ", num32(e.X), num32(e.Y))
	for _, line := range f.agent.gmLines() {
		if strings.HasPrefix(line, entrance) {
			t.Fatalf("the placed walk fell back to the entrance: %q", line)
		}
	}
}

// A placed walk whose revive lands over a navmesh hole still revives (walk 24 test2 r3). The navmesh has a hole
// under the place point, so the z guess never settles (ZUnknown for good), and the old near rule never accepts the
// landing; the session's acked teleport (TeleportSeq) is the landing, so the summons leave.
func TestRaidWalkPlacedReviveLandsOverAMeshHole(t *testing.T) {
	f, _ := newPlaceFixture(t)
	f.opts.wipeResume = 0
	f.agent.startReply = zgPlaceReply(t)
	f.agent.meshHole = true // the revive's near go lands over a mesh hole
	wipeAfterStart(f)
	code := f.run(context.Background())
	if code != exitRaidWalkIncident {
		t.Fatalf("exit %d, want 19: %s\n%s", code, f.stderr.String(), f.stdout.String())
	}
	sum := f.summary()
	if strings.Contains(sum.EndReason, "not revived: the commander did not land") {
		t.Fatalf("the revive never landed over the mesh hole: %q", sum.EndReason)
	}
	if n := countPrefix(f.agent.gmLines(), ".summon "); n == 0 {
		t.Fatalf("no summons went out: the revive never reached its summon stage: %q", f.agent.gmLines())
	}
}

// A placed walk whose commander is off the plan's map at the revive (map 0) takes the appear path: the placed
// shortcut only holds while the map does (walk 24 C2). The appear binds the commander to the hires' copy first.
func TestRaidWalkPlacedReviveOffMapAppearsFirst(t *testing.T) {
	f, _ := newPlaceFixture(t)
	f.opts.wipeResume = 0
	f.agent.startReply = zgPlaceReply(t)
	// The commander is off the map at the revive, but the appear (and its .go) must land it back in the raid's copy.
	onGM := f.agent.onGM
	f.agent.onGM = func(line string) {
		if onGM != nil {
			onGM(line)
		}
		if line == ".gm visible off" {
			f.agent.mu.Lock()
			f.agent.status.Map = 0
			f.agent.mu.Unlock()
		}
	}
	wipeAfterStart(f)
	code := f.run(context.Background())
	if code != exitRaidWalkIncident {
		t.Fatalf("exit %d, want 19: %s\n%s", code, f.stderr.String(), f.stdout.String())
	}
	sum := f.summary()
	if strings.Contains(sum.EndReason, "did not land") {
		t.Fatalf("the commander never landed after its appear: %q", sum.EndReason)
	}
	// The appear must precede the revive's .go xyz after the run started.
	appearAt, goAt := -1, -1
	started := false
	for i, line := range f.agentLog() {
		if strings.HasPrefix(line, "cmdr run start ") {
			started = true
			continue
		}
		if !started {
			continue
		}
		if appearAt < 0 && strings.HasPrefix(line, "gm .appear ") {
			appearAt = i
		}
		if goAt < 0 && strings.HasPrefix(line, "gm .go xyz ") {
			goAt = i
		}
	}
	if appearAt < 0 || goAt < 0 || appearAt > goAt {
		t.Fatalf("appear at %d, go at %d (want appear first): %q", appearAt, goAt, f.agentLog())
	}
}

// A placed walk whose commander is in another copy at the revive (map 309, instance 104) takes the appear path too
// (walk 24 C2): the map alone is not enough, the toolkit instance must match the raid's.
func TestRaidWalkPlacedReviveOtherCopyAppearsFirst(t *testing.T) {
	f, p := newPlaceFixture(t)
	f.opts.wipeResume = 0
	f.agent.startReply = zgPlaceReply(t)
	p.otherInst = 104
	// Arm the copy move at the revive's own `.gm visible off` (after the run started): the FIRST `q inst` after it
	// is placeStillJoined's, so the appear path runs, and the later summons check sees the copy held again.
	onGM := f.agent.onGM
	f.agent.onGM = func(line string) {
		if onGM != nil {
			onGM(line)
		}
		if line == ".gm visible off" {
			f.agent.mu.Lock()
			started := f.agent.runStarted
			f.agent.mu.Unlock()
			if started {
				p.mu.Lock()
				p.instAfterSignal = true
				p.mu.Unlock()
			}
		}
	}
	wipeAfterStart(f)
	code := f.run(context.Background())
	if code != exitRaidWalkIncident {
		t.Fatalf("exit %d, want 19: %s\n%s", code, f.stderr.String(), f.stdout.String())
	}
	sum := f.summary()
	if strings.Contains(sum.EndReason, "did not land") {
		t.Fatalf("the commander never landed after its appear: %q", sum.EndReason)
	}
	appearAt, goAt := -1, -1
	started := false
	for i, line := range f.agentLog() {
		if strings.HasPrefix(line, "cmdr run start ") {
			started = true
			continue
		}
		if !started {
			continue
		}
		if appearAt < 0 && strings.HasPrefix(line, "gm .appear ") {
			appearAt = i
		}
		if goAt < 0 && strings.HasPrefix(line, "gm .go xyz ") {
			goAt = i
		}
	}
	if appearAt < 0 || goAt < 0 || appearAt > goAt {
		t.Fatalf("appear at %d, go at %d (want appear first): %q", appearAt, goAt, f.agentLog())
	}
}

func TestReviveInstanceCheckBeforeSummon(t *testing.T) {
	f, p := newPlaceFixture(t)
	f.opts.wipeResume = 0
	f.agent.startReply = zgPlaceReply(t)
	wipeAfterStart(f)
	p.movedInst = 104 // the commander is back in his own copy by the revive
	code := f.run(context.Background())
	if code != exitRaidWalkIncident {
		t.Fatalf("exit %d, want 19: %s", code, f.stderr.String())
	}
	sum := f.summary()
	if !strings.Contains(sum.EndReason, "not revived: the commander is in instance 104") {
		t.Fatalf("end reason %q", sum.EndReason)
	}
	for _, line := range f.agent.gmLines() {
		if strings.HasPrefix(line, ".summon ") {
			t.Fatalf("a summon went out from the wrong copy: %q", line)
		}
	}
}

// The pair form narrows --place to one walk (spec 3, test 18).
func TestPairPlacePerWalk(t *testing.T) {
	var stderr bytes.Buffer
	o, code := parsePairArgs([]string{"--slot", "2,4", "--place", "2=on", "--from", "2=leg high_priest_thekal", "--sql", "walk.sql", "zul_gurub"}, &stderr)
	if code != exitOK {
		t.Fatalf("exit %d: %s", code, stderr.String())
	}
	if len(o.walks) != 2 {
		t.Fatalf("walks %d", len(o.walks))
	}
	if !o.walks[0].opts.place || o.walks[0].opts.from != "leg high_priest_thekal" {
		t.Fatalf("slot 2: place %v from %q", o.walks[0].opts.place, o.walks[0].opts.from)
	}
	if o.walks[1].opts.place || o.walks[1].opts.from != "" {
		t.Fatalf("slot 4: place %v from %q", o.walks[1].opts.place, o.walks[1].opts.from)
	}
	// A bare --place turns it on for every walk; each placed walk needs its own --from.
	stderr.Reset()
	o, code = parsePairArgs([]string{"--slot", "2,4", "--place", "--from", "leg high_priest_thekal", "--sql", "walk.sql", "zul_gurub"}, &stderr)
	if code != exitOK {
		t.Fatalf("bare --place: exit %d: %s", code, stderr.String())
	}
	if !o.walks[0].opts.place || !o.walks[1].opts.place {
		t.Fatalf("bare --place: %v %v", o.walks[0].opts.place, o.walks[1].opts.place)
	}
	stderr.Reset()
	if _, code := parsePairArgs([]string{"--slot", "2,4", "--place", "2=on", "zul_gurub"}, &stderr); code != exitUsage ||
		!strings.Contains(stderr.String(), "--place needs --from") {
		t.Fatalf("a placed half without --from: exit %d, stderr %q", code, stderr.String())
	}
}

func TestDryRunPrintsPlacePlan(t *testing.T) {
	f, _ := newPlaceFixture(t)
	f.opts.dryRun = true
	if code := f.run(context.Background()); code != exitOK {
		t.Fatalf("exit %d: %s", code, f.stderr.String())
	}
	got := f.stdout.String()
	for _, want := range []string{"5b gm: .instance unbind 309", "8b place: start step ", "(leg high_priest_thekal) at ", "8b clear: row "} {
		if !strings.Contains(got, want) {
			t.Fatalf("the dry run misses %q:\n%s", want, got)
		}
	}
	if _, err := os.Stat(filepath.Join(f.out, "summary.json")); err == nil {
		t.Fatal("the dry run wrote a summary")
	}
}

// placePlanRows builds the rows and the place plan the fixture's validate builds (no --sql: the plan JSON's own
// export), so a test can learn the start step, the circles and the AHEAD rows the walk computes.
func placePlanRows(t *testing.T) (planexport.Rows, raidwalk.PlacePlan) {
	t.Helper()
	plan := zgPlan(t)
	res, err := planexport.Build(plan, planexport.Options{})
	if err != nil {
		t.Fatal(err)
	}
	rows := res.Rows
	step, leg, _, _, err := raidwalk.PlaceStart(rows, []string{"from", "leg", "high_priest_thekal"})
	if err != nil {
		t.Fatal(err)
	}
	point, ok := raidwalk.PlacePoint(rows, step)
	if !ok {
		t.Fatal("the start step has no point")
	}
	stop, err := raidwalk.PlaceStop(rows, []string{"until", "boss"}, step)
	if err != nil {
		t.Fatal(err)
	}
	clear := raidwalk.PlaceClear(rows, plan, step, stop)
	if clear.Refusal != "" {
		t.Fatal(clear.Refusal)
	}
	p := raidwalk.PlacePlan{Start: step, Leg: leg, Point: point, Stop: stop,
		Circles: clear.Circles, Notes: clear.Notes, Ahead: clear.Ahead, Behind: clear.Behind,
		Skips: raidwalk.PlaceSkips(plan)}
	return rows, p
}

// The pre-read must not clear a circle that holds a unit standing in an AHEAD row's circle (review round 1, item 5).
func TestPlacePreReadAheadUnitSkipsCircle(t *testing.T) {
	f, p := newPlaceFixture(t)
	rows, plan := placePlanRows(t)
	if len(plan.Circles) == 0 || len(plan.Ahead) == 0 {
		t.Fatal("the fixture plan has no circles or no AHEAD rows")
	}
	entry := plan.Circles[0].Entries[0]
	ahead := plan.Ahead[0]
	p.units = func(string) ([]any, bool) {
		return []any{map[string]any{"g": "0xF1300000000009", "e": entry, "n": "Roamer",
			"p": []float32{ahead.X, ahead.Y, ahead.Z}, "dz": 0.5, "d": 1.0}}, false
	}
	f.agent.startReply = placeStartReply("zul_gurub", plan.Start, len(rows.Steps), plan.Stop)
	if code := f.run(context.Background()); code != exitOK {
		t.Fatalf("exit %d: %s", code, f.stderr.String())
	}
	var found bool
	for _, s := range f.summary().Place.NotCleared {
		if strings.Contains(s, "AHEAD row") {
			found = true
		}
	}
	if !found {
		t.Fatalf("no AHEAD note; not_cleared %v", f.summary().Place.NotCleared)
	}
}

// A truncated pre-read clears nothing in that circle (review round 1, item 5).
func TestPlacePreReadTruncatedSkipsCircle(t *testing.T) {
	f, p := newPlaceFixture(t)
	rows, plan := placePlanRows(t)
	p.units = func(string) ([]any, bool) { return nil, true }
	f.agent.startReply = placeStartReply("zul_gurub", plan.Start, len(rows.Steps), plan.Stop)
	if code := f.run(context.Background()); code != exitOK {
		t.Fatalf("exit %d: %s", code, f.stderr.String())
	}
	if len(p.cleartos) != 0 {
		t.Fatalf("a truncated pre-read still cleared: %v", p.cleartos)
	}
	sum := f.summary()
	if len(sum.Place.NotCleared) != len(plan.Circles) {
		t.Fatalf("not_cleared %v", sum.Place.NotCleared)
	}
	for _, s := range sum.Place.NotCleared {
		if !strings.Contains(s, "trunc") {
			t.Fatalf("not_cleared %v", sum.Place.NotCleared)
		}
	}
}

// A unit beyond the clear's dz window is not the circle's business (review round 1, item 5).
func TestPlacePreReadIgnoresFarDz(t *testing.T) {
	f, p := newPlaceFixture(t)
	rows, plan := placePlanRows(t)
	p.units = func(string) ([]any, bool) {
		return []any{map[string]any{"g": "0xF1300000000009", "e": 999999, "n": "Far",
			"p": []float32{0, 0, 0}, "dz": 12.0, "d": 1.0}}, false
	}
	f.agent.startReply = placeStartReply("zul_gurub", plan.Start, len(rows.Steps), plan.Stop)
	if code := f.run(context.Background()); code != exitOK {
		t.Fatalf("exit %d: %s", code, f.stderr.String())
	}
	if len(p.cleartos) == 0 {
		t.Fatal("no clearto was sent")
	}
	if sum := f.summary(); len(sum.Place.NotCleared) != 0 {
		t.Fatalf("not_cleared %v", sum.Place.NotCleared)
	}
}

// A unit whose entry is in the clearto's skip list cannot block the circle (review round 1, item 2).
func TestPlacePreReadIgnoresSkipEntry(t *testing.T) {
	f, p := newPlaceFixture(t)
	rows, plan := placePlanRows(t)
	if len(plan.Skips) == 0 {
		t.Fatal("the fixture plan has no skip entries")
	}
	p.units = func(string) ([]any, bool) {
		return []any{map[string]any{"g": "0xF1300000000009", "e": plan.Skips[0], "n": "Trigger",
			"p": []float32{0, 0, 0}, "dz": 0.0, "d": 1.0}}, false
	}
	f.agent.startReply = placeStartReply("zul_gurub", plan.Start, len(rows.Steps), plan.Stop)
	if code := f.run(context.Background()); code != exitOK {
		t.Fatalf("exit %d: %s", code, f.stderr.String())
	}
	if len(p.cleartos) == 0 {
		t.Fatal("no clearto was sent")
	}
	if sum := f.summary(); len(sum.Place.NotCleared) != 0 {
		t.Fatalf("not_cleared %v", sum.Place.NotCleared)
	}
}

// The placed-start check reads the step count too: a server with other rows stops the run (review round 1, item 5).
func TestPlaceStartLineCountMismatchStops(t *testing.T) {
	f, _ := newPlaceFixture(t)
	rows, plan := placePlanRows(t)
	f.agent.startReply = placeStartReply("zul_gurub", plan.Start, len(rows.Steps)+2, plan.Stop)
	code := f.run(context.Background())
	if code != exitRaidWalkFailed {
		t.Fatalf("exit %d, want 18: %s", code, f.stderr.String())
	}
	var stopped bool
	for _, line := range f.agent.gmLines() {
		if line == ".bot run stop" {
			stopped = true
		}
	}
	if !stopped {
		t.Fatalf("the walk did not stop the run: %v", f.agent.gmLines())
	}
	if sum := f.summary(); !strings.Contains(sum.EndReason, "place: placed for step") {
		t.Fatalf("end reason %q", sum.EndReason)
	}
}

// The quiet step's one extra clear round runs the same guard as the corridor: a circle holding a unit of another row,
// a unit in an AHEAD circle, or a truncated reply is never cleared; the quiet then fails (review round 1, item 1).
func TestQuietExtraClearRefusesUnsafeCircle(t *testing.T) {
	cases := []struct {
		name      string
		extra     []any
		trunc     bool
		wantInSum string
	}{
		{"unrelated entry", []any{map[string]any{"g": "0xF1300000000009", "e": 999999, "n": "Stranger",
			"p": []float32{0, 0, 0}, "dz": 0.0, "d": 1.0}}, false, "stands in the circle"},
		{"ahead unit", nil, false, "AHEAD row"},
		{"truncated", nil, true, "trunc"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f, p := newPlaceFixture(t)
			rows, plan := placePlanRows(t)
			if len(plan.Circles) == 0 || len(plan.Ahead) == 0 {
				t.Fatal("the fixture plan has no circles or no AHEAD rows")
			}
			owner := plan.Circles[0]
			home := [3]float32{owner.X, owner.Y, owner.Z}
			p.quiet = func(int) bool { return false }
			// The quiet unit stands inside the landing reach (D6): beyond it the walk would leave it alive and place.
			p.quietNear = []any{quietHomedAt(owner.Entries[0], plan.Point.X+10, plan.Point.Y, plan.Point.Z, home)}
			extra := tc.extra
			if tc.name == "ahead unit" {
				ahead := plan.Ahead[0]
				extra = []any{map[string]any{"g": "0xF1300000000009", "e": owner.Entries[0], "n": "Roamer",
					"p": []float32{ahead.X, ahead.Y, ahead.Z}, "dz": 0.0, "d": 1.0}}
			}
			p.units = func(cmd string) ([]any, bool) {
				if f := strings.Fields(cmd); len(f) > 2 && f[2] == "3" { // the extra round's placeExtraRadius
					return extra, tc.trunc
				}
				return nil, false
			}
			f.agent.startReply = placeStartReply("zul_gurub", plan.Start, len(rows.Steps), plan.Stop)
			code := f.run(context.Background())
			if code != exitRaidWalkFailed {
				t.Fatalf("exit %d, want 18: %s", code, f.stderr.String())
			}
			for _, c := range p.cleartos {
				if strings.Contains(c, " 3 dz=") {
					t.Fatalf("the extra clear ran in an unsafe circle: %q", c)
				}
			}
			sum := f.summary()
			if !strings.Contains(sum.EndReason, "not quiet") {
				t.Fatalf("end reason %q", sum.EndReason)
			}
			var noted bool
			for _, n := range sum.Notes {
				if strings.Contains(n, "in the extra round") && strings.Contains(n, tc.wantInSum) {
					noted = true
				}
			}
			if !noted {
				t.Fatalf("no %q note in the extra round: %v", tc.wantInSum, sum.Notes)
			}
		})
	}
}

// Before the first teleport the commander must be in GM mode (review round 1, item 12).
func TestPlaceRequiresGMMode(t *testing.T) {
	f, _ := newPlaceFixture(t)
	rows, plan := placePlanRows(t)
	f.agent.noGMOn = true
	f.agent.startReply = placeStartReply("zul_gurub", plan.Start, len(rows.Steps), plan.Stop)
	code := f.run(context.Background())
	if code != exitRaidWalkFailed {
		t.Fatalf("exit %d, want 18: %s", code, f.stderr.String())
	}
	for _, line := range f.agent.gmLines() {
		if strings.HasPrefix(line, ".appear ") {
			t.Fatalf("the commander appeared before the GM check: %q", line)
		}
	}
	if sum := f.summary(); !strings.Contains(sum.EndReason, "not in GM mode") {
		t.Fatalf("end reason %q", sum.EndReason)
	}
}

// The longest-line check (review round 1, item 11): a plan whose skip list pushes a clearto over the toolkit's cap is
// caught before any GM command.
func TestPlaceTTMaxOverflow(t *testing.T) {
	skip := make([]uint32, raidwalk.MaxSkipEntries)
	for i := range skip {
		skip[i] = uint32(4000000000 + i)
	}
	p := &raidwalk.PlacePlan{
		Point:   dungeon.Point{X: -11920.5, Y: -1857.6, Z: 61.34},
		Skips:   skip,
		Circles: []raidwalk.ClearCircle{{X: -11920.5, Y: -1857.6, Z: 61.34, Radius: 22}},
	}
	long, n := placeTTMax(p, 309)
	if n != len(long) || n <= raidwalk.AllowedTTMax {
		t.Fatalf("longest line is %d chars (over %v): %q", n, n > raidwalk.AllowedTTMax, long)
	}
}

// A plan whose skip union is over the toolkit's cap is refused in validate, before any GM command (review round 1,
// item 11).
func TestPlaceRefusesOverlongSkipList(t *testing.T) {
	f, _ := newPlaceFixture(t)
	raw, err := os.ReadFile("../../dungeons/zul_gurub.json")
	if err != nil {
		t.Fatal(err)
	}
	var doc map[string]any
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	legs, _ := doc["legs"].([]any)
	skip := make([]any, 20)
	for i := range skip {
		skip[i] = 90000 + i
	}
	for _, l := range legs {
		if m, ok := l.(map[string]any); ok {
			m["skip"] = skip
		}
	}
	out, err := json.Marshal(doc)
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "zul_gurub.json"), out, 0o644); err != nil {
		t.Fatal(err)
	}
	f.opts.dir = dir
	code := f.run(context.Background())
	if code != exitUsage {
		t.Fatalf("exit %d, want %d: %s", code, exitUsage, f.stderr.String())
	}
	if !strings.Contains(f.stderr.String(), "skip lists hold") {
		t.Fatalf("stderr %q", f.stderr.String())
	}
	if len(f.agent.gmLines()) != 0 {
		t.Fatalf("a GM line went out before the refusal: %v", f.agent.gmLines())
	}
}

// A plain --from (no --place, no boss-done) goes to the server exactly as before: a step number past the plan's own
// export is not resolved against the rows, so it is not refused (review round 1, item 3).
func TestPlainFromStepNotResolvedAgainstRows(t *testing.T) {
	f := newFixture(t)
	f.opts.from = "step 500"
	f.opts.until = "none"
	f.opts.dryRun = true
	if code := f.run(context.Background()); code != exitOK {
		t.Fatalf("exit %d: %s", code, f.stderr.String())
	}
	if !strings.Contains(f.stdout.String(), "from step 500") {
		t.Fatalf("the dry run does not carry the from clause:\n%s", f.stdout.String())
	}
}

// quietUnitAt is a quiet read's `near` entry for a unit standing at a point.
func quietUnitAt(entry uint32, x, y, z float32) map[string]any {
	return map[string]any{"g": "0xF1300000000009", "e": entry, "n": "Roamer", "p": []float32{x, y, z}, "w": 1.0, "d": 3.0}
}

// quietHomedAt is a quiet read's `near` entry for a unit standing at a point whose home (its spawn point) the reply
// carries: the extra round owns it by that home (a reply without one gives no owner: fail closed).
func quietHomedAt(entry uint32, x, y, z float32, home [3]float32) map[string]any {
	return map[string]any{"g": "0xF1300000000009", "e": entry, "n": "Roamer", "p": []float32{x, y, z}, "w": 1.0, "d": 3.0,
		"home": []float32{home[0], home[1], home[2]}}
}

// extraClearCount is the number of extra-round cleartos (radius 3) among the toolkit lines.
func extraClearCount(p *placeToolkit) int {
	n := 0
	for _, c := range p.cleartos {
		if strings.Contains(c, " 3 dz=") {
			n++
		}
	}
	return n
}

// A safe extra round really sends its clearto, and the quiet is read again after it (review round 2, MEDIUM-A (a)).
func TestQuietExtraClearSendsSafeClearto(t *testing.T) {
	f, p := newPlaceFixture(t)
	rows, plan := placePlanRows(t)
	owner := plan.Circles[0]
	home := [3]float32{owner.X, owner.Y, owner.Z}
	p.quiet = func(n int) bool { return n >= 2 }
	p.quietNear = []any{quietHomedAt(owner.Entries[0], owner.X, owner.Y, owner.Z, home)}
	f.agent.startReply = placeStartReply("zul_gurub", plan.Start, len(rows.Steps), plan.Stop)
	if code := f.run(context.Background()); code != exitOK {
		t.Fatalf("exit %d: %s", code, f.stderr.String())
	}
	if n := extraClearCount(p); n != 1 {
		t.Fatalf("%d extra cleartos, want 1: %v", n, p.cleartos)
	}
	want := fmt.Sprintf("clearto 309 %s %s %s 3 dz=8 all=1", num32(owner.X), num32(owner.Y), num32(owner.Z))
	var found bool
	for _, c := range p.cleartos {
		if strings.HasPrefix(c, want) {
			found = true
		}
	}
	if !found {
		t.Fatalf("no %q among %v", want, p.cleartos)
	}
	if p.quietCalls != 2 {
		t.Fatalf("%d quiet reads, want 2 (one before the extra clear, one after)", p.quietCalls)
	}
	// The re-read comes after the extra clear.
	log := f.agentLog()
	extraAt, lastQuiet := -1, -1
	for i, l := range log {
		if strings.HasPrefix(l, "tt clearto ") && strings.Contains(l, " 3 dz=") {
			extraAt = i
		}
		if strings.HasPrefix(l, "tt quiet ") {
			lastQuiet = i
		}
	}
	if extraAt < 0 || lastQuiet < extraAt {
		t.Fatalf("the quiet was not read again after the extra clear (clear at %d, last quiet at %d)", extraAt, lastQuiet)
	}
}

// A unit no BEHIND row owns is never cleared by the extra round (review round 2, MEDIUM-A (b)); inside the landing
// reach it blocks the placement, and the refusal says why (item 3: beyond that reach it never blocks).
func TestQuietExtraClearRefusesUnownedUnit(t *testing.T) {
	f, p := newPlaceFixture(t)
	rows, plan := placePlanRows(t)
	p.quiet = func(int) bool { return false }
	p.quietNear = []any{quietUnitAt(999999, plan.Point.X+10, plan.Point.Y, plan.Point.Z)}
	f.agent.startReply = placeStartReply("zul_gurub", plan.Start, len(rows.Steps), plan.Stop)
	if code := f.run(context.Background()); code != exitRaidWalkFailed {
		t.Fatalf("exit %d, want 18: %s", code, f.stderr.String())
	}
	if n := extraClearCount(p); n != 0 {
		t.Fatalf("the extra round cleared a unit no BEHIND row owns: %v", p.cleartos)
	}
	if !hasNote(f.summary(), "no BEHIND row owns the unit") {
		t.Fatalf("notes %v", f.summary().Notes)
	}
	// The same unit 40 yd out from a circle no row owns is not the circle's business: it never blocked the quiet.
	f2, p2 := newPlaceFixture(t)
	p2.quiet = func(int) bool { return false }
	p2.quietNear = []any{quietUnitAt(999999, plan.Point.X+40, plan.Point.Y, plan.Point.Z)}
	f2.agent.startReply = placeStartReply("zul_gurub", plan.Start, len(rows.Steps), plan.Stop)
	if code := f2.run(context.Background()); code != exitOK {
		t.Fatalf("exit %d, want 0 for an unowned unit 40 yd out: %s", code, f2.summary().EndReason)
	}
}

// A unit near a circle the corridor skipped is never cleared by the extra round (review round 2, MEDIUM-A (c)). Inside
// the landing reach the leftover fails the placement (the old case); beyond it the walk leaves the unit alive with a
// note (D6, F4) — row 94's circle stands about 100 yd from the start.
func TestQuietExtraClearRefusesUnitNearSkippedCircle(t *testing.T) {
	for _, tc := range []struct {
		name   string
		target func(t *testing.T, plan raidwalk.PlacePlan) raidwalk.ClearCircle
		want   int
	}{
		{"inside the reach", func(_ *testing.T, plan raidwalk.PlacePlan) raidwalk.ClearCircle { return plan.Circles[0] }, exitRaidWalkFailed},
		{"beyond the reach", placeRow94Circle, exitOK},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f, p := newPlaceFixture(t)
			rows, plan := placePlanRows(t)
			target := tc.target(t, plan)
			p.units = func(cmd string) ([]any, bool) {
				if fl := strings.Fields(cmd); len(fl) > 2 && fl[2] == "3" { // the extra round's read: nothing there
					return nil, false
				}
				if fl := strings.Fields(cmd); len(fl) == 7 && fl[3] == num32(target.X) && fl[4] == num32(target.Y) {
					// A unit the row does not own stands in the circle: the corridor skips it.
					return []any{map[string]any{"g": "0xF1300000000008", "e": 999999, "n": "Stranger",
						"p": []float32{target.X, target.Y, target.Z}, "dz": 0.5, "d": 1.0}}, false
				}
				return nil, false
			}
			home := [3]float32{target.X, target.Y, target.Z}
			p.quiet = func(int) bool { return false }
			p.quietNear = []any{quietHomedAt(target.Entries[0], target.X, target.Y, target.Z, home)}
			f.agent.startReply = placeStartReply("zul_gurub", plan.Start, len(rows.Steps), plan.Stop)
			if code := f.run(context.Background()); code != tc.want {
				t.Fatalf("exit %d, want %d: %s", code, tc.want, f.summary().EndReason)
			}
			if n := extraClearCount(p); n != 0 {
				t.Fatalf("the extra round cleared next to a skipped circle: %v", p.cleartos)
			}
			if !hasNote(f.summary(), "skipped circle of row") {
				t.Fatalf("notes %v", f.summary().Notes)
			}
			if tc.want == exitOK {
				if !hasNote(f.summary(), "leaves") || !hasNote(f.summary(), "alive") {
					t.Fatalf("the walk did not leave the unit alive: %v", f.summary().Notes)
				}
				return
			}
			if len(p.places) != 0 {
				t.Fatalf("the walk placed with a unit in the reach: %v", p.places)
			}
		})
	}
}

// placeCircleNear is the plan's clear circle nearest a point (fatal when none is within a yard).
func placeCircleNear(t *testing.T, plan raidwalk.PlacePlan, x, y, z float32) raidwalk.ClearCircle {
	t.Helper()
	best, bestD := raidwalk.ClearCircle{}, 1e9
	for _, c := range plan.Circles {
		if d := placeDist3(c.X, c.Y, c.Z, x, y, z); d < bestD {
			best, bestD = c, d
		}
	}
	if bestD > 1 {
		t.Fatalf("the fixture plan has no clear circle at %.2f %.2f %.2f (the nearest is %.1f yd off)", x, y, z, bestD)
	}
	return best
}

// placeAheadNear is the plan's AHEAD circle nearest a point (fatal when none is within a yard).
func placeAheadNear(t *testing.T, plan raidwalk.PlacePlan, x, y, z float32) raidwalk.AheadCircle {
	t.Helper()
	best, bestD := raidwalk.AheadCircle{}, 1e9
	for _, a := range plan.Ahead {
		if d := placeDist3(a.X, a.Y, a.Z, x, y, z); d < bestD {
			best, bestD = a, d
		}
	}
	if bestD > 1 {
		t.Fatalf("the fixture plan has no AHEAD circle at %.2f %.2f %.2f (the nearest is %.1f yd off)", x, y, z, bestD)
	}
	return best
}

// placeAheadB is leg 3's first pull (the deployed rows' step 234: 15111 + 11340 at -11873.62 -1891.77 64.10; the plan
// JSON's own export numbers it 110).
func placeAheadB(t *testing.T, plan raidwalk.PlacePlan) (raidwalk.AheadCircle, [3]float32) {
	t.Helper()
	b := placeAheadNear(t, plan, -11873.62, -1891.77, 64.1)
	return b, [3]float32{b.X, b.Y, b.Z}
}

// placeRow96Circle is the deployed row 96's circle (15111 at -11965.97 -1842.25 55.98), the one the snake 49037 stood in.
func placeRow96Circle(t *testing.T, plan raidwalk.PlacePlan) raidwalk.ClearCircle {
	t.Helper()
	return placeCircleNear(t, plan, -11965.97, -1842.25, 55.98)
}

// placeRow94Circle is the deployed row 94's circle (15111 at -11945.70 -1841.13 56.11), the E patroller's home.
func placeRow94Circle(t *testing.T, plan raidwalk.PlacePlan) raidwalk.ClearCircle {
	t.Helper()
	return placeCircleNear(t, plan, -11945.70, -1841.13, 56.11)
}

// placeInsideWithoutAhead is a point inside a clear circle but outside every AHEAD row's area: where a unit the circle's
// pre-read may list stands. The test fails when the fixture leaves none.
func placeInsideWithoutAhead(t *testing.T, plan raidwalk.PlacePlan, c raidwalk.ClearCircle) [3]float32 {
	t.Helper()
	for _, d := range []float64{0, 3, 6, 9, 12, 15} {
		if d >= float64(c.Radius) {
			break
		}
		for k := range 16 {
			a := float64(k) * math.Pi / 8
			x := c.X + float32(d*math.Cos(a))
			y := c.Y + float32(d*math.Sin(a))
			if _, in := plan.AheadIn(x, y, c.Z); in {
				continue
			}
			return [3]float32{x, y, c.Z}
		}
	}
	t.Fatalf("the fixture plan leaves no point inside the circle of row %d outside every AHEAD row", c.Step)
	return [3]float32{}
}

// placeFarHome is a point 30-200 yd from a circle, at its height, outside every AHEAD circle and every BEHIND pull
// row's area: where a later leg's patrol spawns. The test fails when the fixture leaves none.
func placeFarHome(t *testing.T, plan raidwalk.PlacePlan, c raidwalk.ClearCircle) [3]float32 {
	t.Helper()
	for _, d := range []float64{30, 35, 40, 45, 50, 60, 80, 100, 150, 200} {
		for k := range 32 {
			a := float64(k) * math.Pi / 16
			x := c.X + float32(d*math.Cos(a))
			y := c.Y + float32(d*math.Sin(a))
			if _, in := plan.AheadIn(x, y, c.Z); in {
				continue
			}
			if placeInAnyBehindRow(plan, x, y, c.Z) {
				continue
			}
			return [3]float32{x, y, c.Z}
		}
	}
	t.Fatalf("the fixture plan leaves no home outside every BEHIND row and AHEAD circle within 200 yd of the circle of row %d", c.Step)
	return [3]float32{}
}

// placeOwnedHome is the point of a BEHIND pull row that holds the entry, outside every corridor circle's radius + 6 yd
// (so the extra round's owner check finds none) and outside every AHEAD circle: the home that makes a unit BEHIND trash
// by positive ownership. The test fails when the fixture has no such row.
func placeOwnedHome(t *testing.T, plan raidwalk.PlacePlan, entry uint32) [3]float32 {
	t.Helper()
	for _, b := range plan.Behind {
		if !inEntries(b.Entries, entry) {
			continue
		}
		var circled bool
		for _, c := range plan.Circles {
			if placeDist3(b.X, b.Y, b.Z, c.X, c.Y, c.Z) <= float64(c.Radius)+placeRogueYd {
				circled = true
				break
			}
		}
		if circled {
			continue
		}
		if _, in := plan.AheadIn(b.X, b.Y, b.Z); in {
			continue
		}
		return [3]float32{b.X, b.Y, b.Z}
	}
	t.Fatalf("the fixture plan has no BEHIND pull row of entry %d outside every corridor circle and AHEAD row", entry)
	return [3]float32{}
}

// placeInAnyBehindRow says the point lies in a BEHIND pull row's area, whatever the entry: the test-side "outside every
// BEHIND row" check. BehindOwns applies the same row area (radius + 6 yd, its z window) once the row holds the entry,
// and the row always holds the entry it is asked about.
func placeInAnyBehindRow(plan raidwalk.PlacePlan, x, y, z float32) bool {
	for _, b := range plan.Behind {
		if len(b.Entries) == 0 {
			continue
		}
		if _, in := plan.BehindOwns(b.Entries[0], x, y, z); in {
			return true
		}
	}
	return false
}

// quietUnitHome is a quiet read's `near` entry with the unit's home (its spawn point) and its vitals.
func quietUnitHome(guid string, entry uint32, x, y, z float32, home [3]float32) map[string]any {
	return map[string]any{"g": guid, "e": entry, "n": "Mad Servant", "p": []float32{x, y, z}, "d": 3.0,
		"hpv": []float64{4000, 4000}, "rk": 0, "db": 49062, "home": []float32{home[0], home[1], home[2]}}
}

// quietRoamAt is a quiet read's `roamers` entry: the toolkit sends g, e, why, d and moved, and no position.
func quietRoamAt(guid string, entry uint32, why string, d, moved float32) map[string]any {
	return map[string]any{"g": guid, "e": entry, "why": why, "d": d, "moved": moved}
}

// joinUnitAt is one unit of the quiet's join read (`q units` around the start point): where the roamer stands, where its
// home is, and its vitals.
func joinUnitAt(guid string, entry uint32, x, y, z float32, home [3]float32, maxHP float64, rank int) map[string]any {
	return map[string]any{"g": guid, "e": entry, "n": "Mad Servant", "p": []float32{x, y, z}, "dz": 0.0, "d": 0.0,
		"hpv": []float64{maxHP, maxHP}, "rk": rank, "db": 49062, "home": []float32{home[0], home[1], home[2]}}
}

// joinHook answers the quiet's join read (radius placeRoamScopeYd) with units, and every other `q units` (the corridor's
// pre-reads and the extra round's) with nothing.
func joinHook(units []any) func(string) ([]any, bool) {
	return func(cmd string) ([]any, bool) {
		if fl := strings.Fields(cmd); len(fl) > 2 && fl[2] == fmt.Sprintf("%d", placeRoamScopeYd) {
			return units, false
		}
		return nil, false
	}
}

// The walk judges the quiet itself (item 3): three 15111 wanderers the toolkit counts as roamers, whose home lies in
// leg 3's first pull 54-61 yd out, never block the placement (today: exit 18 "not quiet").
func TestPlaceQuietIgnoresAheadWanderersBeyondReach(t *testing.T) {
	f, p := newPlaceFixture(t)
	rows, plan := placePlanRows(t)
	b, home := placeAheadB(t, plan)
	var roamers, units []any
	for i, d := range []float32{54, 57.5, 61} {
		guid := fmt.Sprintf("0xF130000000100%d", i+1)
		roamers = append(roamers, quietRoamAt(guid, 15111, "moved", d, float32(i)+3))
		units = append(units, joinUnitAt(guid, 15111, plan.Point.X+d, plan.Point.Y, plan.Point.Z, home, 4000, 0))
	}
	p.quiet = func(int) bool { return false }
	p.quietRoamers = roamers
	p.units = joinHook(units)
	f.agent.startReply = placeStartReply("zul_gurub", plan.Start, len(rows.Steps), plan.Stop)
	if code := f.run(context.Background()); code != exitOK {
		t.Fatalf("exit %d, want 0 (AHEAD wanderers %d yd out block): %s", code, b.Step, f.summary().EndReason)
	}
	if len(p.places) != 1 {
		t.Fatalf("%d place commands, want one", len(p.places))
	}
	if n := extraClearCount(p); n != 0 {
		t.Fatalf("the extra round ran for AHEAD-owned wanderers: %v", p.cleartos)
	}
	if !f.summary().Place.Quiet {
		t.Fatalf("the place summary does not report the quiet")
	}
}

// An AHEAD-owned unit inside the landing reach blocks the quiet (item 3, rule a); the refusal names it and its row.
func TestPlaceQuietBlocksAheadUnitWithinReach(t *testing.T) {
	f, p := newPlaceFixture(t)
	rows, plan := placePlanRows(t)
	_, home := placeAheadB(t, plan)
	p.quiet = func(int) bool { return false }
	p.quietNear = []any{quietUnitHome("0xF1300000002001", 15111, plan.Point.X+18, plan.Point.Y, plan.Point.Z, home)}
	f.agent.startReply = placeStartReply("zul_gurub", plan.Start, len(rows.Steps), plan.Stop)
	if code := f.run(context.Background()); code != exitRaidWalkFailed {
		t.Fatalf("exit %d, want 18: %s", code, f.summary().EndReason)
	}
	if len(p.places) != 0 {
		t.Fatalf("the walk placed with an AHEAD unit at 18 yd: %v", p.places)
	}
	if sum := f.summary(); !strings.Contains(sum.EndReason, "not quiet") || !strings.Contains(sum.EndReason, "15111") {
		t.Fatalf("end reason %q", sum.EndReason)
	}
	if !hasNote(f.summary(), "its home is in AHEAD row") {
		t.Fatalf("notes %v", f.summary().Notes)
	}
}

// A critter (rank 0, at most 100 max HP) never blocks a circle's clear (item 2): row 96 is cleared with the snake in it
// (today: "not cleared row 96: unit 2914 Snake stands in the circle").
func TestPlacePreReadIgnoresCritter(t *testing.T) {
	f, p := newPlaceFixture(t)
	rows, plan := placePlanRows(t)
	row96 := placeRow96Circle(t, plan)
	if len(row96.Entries) != 1 || row96.Entries[0] != 15111 {
		t.Fatalf("the fixture's row-96 circle holds %v, want [15111]", row96.Entries)
	}
	at := []float32{row96.X, row96.Y, row96.Z}
	p.units = func(cmd string) ([]any, bool) {
		fl := strings.Fields(cmd)
		if len(fl) != 7 || fl[3] != num32(row96.X) || fl[4] != num32(row96.Y) {
			return nil, false
		}
		units := []any{}
		for i := range 3 {
			units = append(units, map[string]any{"g": fmt.Sprintf("0xF130000000300%d", i+1), "e": 15111, "n": "Mad Servant",
				"p": at, "dz": 0.5, "d": 1.0, "hpv": []float64{4000, 4000}})
		}
		return append(units, map[string]any{"g": "0xF1300000003004", "e": 2914, "n": "Snake", "p": at, "dz": 0.5, "d": 1.0,
			"hpv": []float64{8, 8}, "rk": 0}), false
	}
	f.agent.startReply = placeStartReply("zul_gurub", plan.Start, len(rows.Steps), plan.Stop)
	if code := f.run(context.Background()); code != exitOK {
		t.Fatalf("exit %d: %s", code, f.summary().EndReason)
	}
	want := fmt.Sprintf("clearto 309 %s %s %s %s dz=8 all=1", num32(row96.X), num32(row96.Y), num32(row96.Z), num32(row96.Radius))
	var found bool
	for _, c := range p.cleartos {
		if strings.HasPrefix(c, want) {
			found = true
		}
	}
	if !found {
		t.Fatalf("row 96 was not cleared: cleartos %v, not_cleared %v", p.cleartos, f.summary().Place.NotCleared)
	}
	for _, s := range f.summary().Place.NotCleared {
		if strings.Contains(s, "2914") {
			t.Fatalf("the critter still blocked a circle: %v", s)
		}
	}
}

// The extra round owns a unit by its home (its spawn point) and clears it where it stands (item 1): a 15111 25 yd out
// from row 94's circle, whose home is that circle, is cleared at its current position.
func TestQuietExtraClearOwnsByHome(t *testing.T) {
	f, p := newPlaceFixture(t)
	rows, plan := placePlanRows(t)
	row94 := placeRow94Circle(t, plan)
	if len(row94.Entries) != 1 || row94.Entries[0] != 15111 {
		t.Fatalf("the fixture's row-94 circle holds %v, want [15111]", row94.Entries)
	}
	home := [3]float32{row94.X, row94.Y, row94.Z}
	at := [3]float32{row94.X + 25, row94.Y, row94.Z}
	if d := placeDist3(at[0], at[1], at[2], home[0], home[1], home[2]); d <= float64(row94.Radius)+placeRogueYd {
		t.Fatalf("the fixture unit is %.1f yd from its circle: it must stand beyond radius + %d", d, placeRogueYd)
	}
	p.quiet = func(n int) bool { return n >= 2 }
	p.quietNear = []any{quietUnitHome("0xF1300000004001", 15111, at[0], at[1], at[2], home)}
	f.agent.startReply = placeStartReply("zul_gurub", plan.Start, len(rows.Steps), plan.Stop)
	if code := f.run(context.Background()); code != exitOK {
		t.Fatalf("exit %d: %s", code, f.summary().EndReason)
	}
	if n := extraClearCount(p); n != 1 {
		t.Fatalf("%d extra cleartos, want one: %v", n, p.cleartos)
	}
	want := fmt.Sprintf("clearto 309 %s %s %s 3 dz=8 all=1", num32(at[0]), num32(at[1]), num32(at[2]))
	var found bool
	for _, c := range p.cleartos {
		if strings.HasPrefix(c, want) {
			found = true
		}
	}
	if !found {
		t.Fatalf("the extra clear did not run at the unit's own position: %v", p.cleartos)
	}
	if p.quietCalls != 2 {
		t.Fatalf("%d quiet reads, want 2 (one before the extra clear, one after)", p.quietCalls)
	}
}

// A reply without a home gives the unit no owner (fail closed): the extra round never clears it, and the note says the
// home was missing, so the live run can tell the two apart.
func TestPlaceQuietMissingHomeIsNotOwned(t *testing.T) {
	f, p := newPlaceFixture(t)
	rows, plan := placePlanRows(t)
	row94 := placeRow94Circle(t, plan)
	p.quiet = func(n int) bool { return n >= 2 }
	p.quietNear = []any{quietUnitAt(15111, row94.X, row94.Y, row94.Z)} // no home in the reply
	f.agent.startReply = placeStartReply("zul_gurub", plan.Start, len(rows.Steps), plan.Stop)
	if code := f.run(context.Background()); code != exitOK {
		t.Fatalf("exit %d: %s", code, f.summary().EndReason)
	}
	if n := extraClearCount(p); n != 0 {
		t.Fatalf("%d extra cleartos for a unit with no home, want none (no owner without a spawn): %v", n, p.cleartos)
	}
	if !hasNote(f.summary(), "home none") {
		t.Fatalf("the note does not say the home was missing: %v", f.summary().Notes)
	}
}

// A `quiet w=20` answers twice, and the walk judges read 1 as well (item 3): a unit that stood inside the landing reach
// when the first read was taken blocks the placement even though the final read (20 s later) no longer lists it.
func TestPlaceQuietJudgesReadOne(t *testing.T) {
	f, p := newPlaceFixture(t)
	rows, plan := placePlanRows(t)
	f.agent.ttFn = func(cmd string) (ttResult, error) {
		if strings.HasPrefix(cmd, "quiet") {
			p.quietCalls++
			near1 := map[string]any{"g": "0xF1300000006001", "e": 999999, "n": "Stranger", "d": 10.0,
				"p": []float32{plan.Point.X + 10, plan.Point.Y, plan.Point.Z}}
			return ttResult{Replies: []json.RawMessage{
				ttRaw(t, map[string]any{"map": 309, "read": 1, "final": false, "quiet_now": false, "near": []any{near1}}),
				ttRaw(t, map[string]any{"map": 309, "read": 2, "final": true, "quiet": false, "near": []any{}, "roamers": []any{}}),
			}}, nil
		}
		return p.answer(cmd)
	}
	f.agent.startReply = placeStartReply("zul_gurub", plan.Start, len(rows.Steps), plan.Stop)
	if code := f.run(context.Background()); code != exitRaidWalkFailed {
		t.Fatalf("exit %d, want 18: %s", code, f.summary().EndReason)
	}
	if len(p.places) != 0 {
		t.Fatalf("the walk placed although read 1 listed a unit at 10 yd: %v", p.places)
	}
	if sum := f.summary(); !strings.Contains(sum.EndReason, "not quiet") || !strings.Contains(sum.EndReason, "999999") {
		t.Fatalf("end reason %q", sum.EndReason)
	}
}

// Item 5: when the only blockers are moving patrols (here a Berserker at 18 yd that the toolkit lists as a roamer) the
// quiet is read again, at most twice, and the placement then fails.
func TestPlaceQuietRereadsMovingPatrolsTwiceThenFails(t *testing.T) {
	f, p := newPlaceFixture(t)
	rows, plan := placePlanRows(t)
	_, home := placeAheadB(t, plan)
	const guid = "0xF1300000005001"
	at := [3]float32{plan.Point.X + 18, plan.Point.Y, plan.Point.Z}
	p.quiet = func(int) bool { return false }
	p.quietNear = []any{quietUnitHome(guid, 11352, at[0], at[1], at[2], home)}
	p.quietRoamers = []any{quietRoamAt(guid, 11352, "moved", 18.0, 4.2)}
	p.units = joinHook([]any{joinUnitAt(guid, 11352, at[0], at[1], at[2], home, 3000, 0)})
	f.agent.startReply = placeStartReply("zul_gurub", plan.Start, len(rows.Steps), plan.Stop)
	if code := f.run(context.Background()); code != exitRaidWalkFailed {
		t.Fatalf("exit %d, want 18: %s", code, f.summary().EndReason)
	}
	if want := 1 + placeQuietRereads; p.quietCalls != want {
		t.Fatalf("%d quiet reads, want %d", p.quietCalls, want)
	}
	if len(p.places) != 0 {
		t.Fatalf("the walk placed with a patrol in the landing reach: %v", p.places)
	}
	if sum := f.summary(); !strings.Contains(sum.EndReason, "11352") {
		t.Fatalf("end reason %q", sum.EndReason)
	}
	if !hasNote(f.summary(), "reading it again") {
		t.Fatalf("notes %v", f.summary().Notes)
	}
}

// F1 (D1): a roamer the cut join read missed and that stands beyond the landing reach is ignored, with a note
// (walk 24 m910q-marli: three roamers at 65.6 / 67.4 / 77.2 yd, none of them near the raid's landing).
func TestPlaceQuietIgnoresMissingRoamerBeyondReach(t *testing.T) {
	f, p := newPlaceFixture(t)
	rows, plan := placePlanRows(t)
	entries := []uint32{11338, 14532, 14881}
	guids := []string{"0xF1300000009001", "0xF1300000009002", "0xF1300000009003"}
	var roamers []any
	for i, d := range []float32{65.6, 67.4, 77.2} {
		roamers = append(roamers, quietRoamAt(guids[i], entries[i], "moved", d, float32(i)+3.2))
	}
	p.quiet = func(int) bool { return false }
	p.quietRoamers = roamers
	p.units = func(cmd string) ([]any, bool) {
		if fl := strings.Fields(cmd); len(fl) > 2 && fl[2] == fmt.Sprintf("%d", placeRoamScopeYd) {
			return []any{
				joinUnitAt("0xF1300000009101", 11338, plan.Point.X+90, plan.Point.Y, plan.Point.Z, [3]float32{plan.Point.X + 90, plan.Point.Y, plan.Point.Z}, 4000, 0),
				joinUnitAt("0xF1300000009102", 14532, plan.Point.X+92, plan.Point.Y, plan.Point.Z, [3]float32{plan.Point.X + 92, plan.Point.Y, plan.Point.Z}, 4000, 0),
			}, true
		}
		return nil, false
	}
	f.agent.startReply = placeStartReply("zul_gurub", plan.Start, len(rows.Steps), plan.Stop)
	if code := f.run(context.Background()); code != exitOK {
		t.Fatalf("exit %d, want 0: %s", code, f.summary().EndReason)
	}
	if len(p.places) != 1 {
		t.Fatalf("%d place commands, want one", len(p.places))
	}
	if !hasNote(f.summary(), "the cut join read missed it") {
		t.Fatalf("notes %v", f.summary().Notes)
	}
}

// F1 (D1, D4): a roamer the cut join read missed that stands inside the landing reach blocks (fail closed), and it
// counts as a mover for the re-read rule: the walk makes its two re-reads before it fails.
func TestPlaceQuietBlocksMissingRoamerWithinReach(t *testing.T) {
	f, p := newPlaceFixture(t)
	rows, plan := placePlanRows(t)
	const guid = "0xF1300000009201"
	p.quiet = func(int) bool { return false }
	p.quietRoamers = []any{quietRoamAt(guid, 11338, "moved", 18.0, 3.0)}
	p.units = func(cmd string) ([]any, bool) {
		if fl := strings.Fields(cmd); len(fl) > 2 && fl[2] == fmt.Sprintf("%d", placeRoamScopeYd) {
			return nil, true
		}
		return nil, false
	}
	f.agent.startReply = placeStartReply("zul_gurub", plan.Start, len(rows.Steps), plan.Stop)
	if code := f.run(context.Background()); code != exitRaidWalkFailed {
		t.Fatalf("exit %d, want 18: %s", code, f.summary().EndReason)
	}
	if want := 1 + placeQuietRereads; p.quietCalls != want {
		t.Fatalf("%d quiet reads, want %d", p.quietCalls, want)
	}
	if sum := f.summary(); !strings.Contains(sum.EndReason, "11338") {
		t.Fatalf("end reason %q", sum.EndReason)
	}
}

// F1 (D2): a roamer the cut join read missed but that the quiet's own near list carries is judged by that entry: the
// walk does not refuse it as missing (walk 24 test2-ven: the 11368 roamer at 28.2 yd).
func TestPlaceQuietFindsRoamerInNearList(t *testing.T) {
	f, p := newPlaceFixture(t)
	rows, plan := placePlanRows(t)
	_, home := placeAheadB(t, plan)
	const guid = "0xF1300000009301"
	at := [3]float32{plan.Point.X + 28.2, plan.Point.Y, plan.Point.Z}
	p.quiet = func(int) bool { return false }
	p.quietNear = []any{quietUnitHome(guid, 15111, at[0], at[1], at[2], home)}
	p.quietRoamers = []any{quietRoamAt(guid, 15111, "moved", 28.2, 2.5)}
	p.units = func(cmd string) ([]any, bool) {
		if fl := strings.Fields(cmd); len(fl) > 2 && fl[2] == fmt.Sprintf("%d", placeRoamScopeYd) {
			return []any{joinUnitAt("0xF1300000009302", 11338, plan.Point.X+90, plan.Point.Y, plan.Point.Z, [3]float32{plan.Point.X + 90, plan.Point.Y, plan.Point.Z}, 4000, 0)}, true
		}
		return nil, false
	}
	f.agent.startReply = placeStartReply("zul_gurub", plan.Start, len(rows.Steps), plan.Stop)
	if code := f.run(context.Background()); code != exitOK {
		t.Fatalf("exit %d, want 0: %s", code, f.summary().EndReason)
	}
	for _, n := range f.summary().Notes {
		if strings.Contains(n, "is not in the join read") {
			t.Fatalf("the walk refused a roamer it found in the near list: %q", n)
		}
		if strings.Contains(n, "missed it") {
			t.Fatalf("the walk treated a roamer its near list carries as missing: %q", n)
		}
	}
}

// The join read is fresher than the quiet's own near list: a roamer the quiet lists 40 yd out that the join read
// finds 10 yd out blocks the placement at its 10 yd position (the judge kept the stale near-list position and placed).
func TestPlaceQuietJoinsFresherRoamerPosition(t *testing.T) {
	f, p := newPlaceFixture(t)
	rows, plan := placePlanRows(t)
	const guid = "0xF1300000009401"
	home := placeFarHome(t, plan, plan.Circles[0])
	p.quiet = func(int) bool { return false }
	p.quietNear = []any{quietUnitHome(guid, 999999, plan.Point.X+40, plan.Point.Y, plan.Point.Z, home)}
	p.quietRoamers = []any{quietRoamAt(guid, 999999, "moved", 40.0, 3.0)}
	p.units = joinHook([]any{joinUnitAt(guid, 999999, plan.Point.X+10, plan.Point.Y, plan.Point.Z, home, 4000, 0)})
	f.agent.startReply = placeStartReply("zul_gurub", plan.Start, len(rows.Steps), plan.Stop)
	if code := f.run(context.Background()); code != exitRaidWalkFailed {
		t.Fatalf("exit %d, want 18: %s", code, f.summary().EndReason)
	}
	if len(p.places) != 0 {
		t.Fatalf("the walk placed with a roamer 10 yd out: %v", p.places)
	}
	if sum := f.summary(); !strings.Contains(sum.EndReason, "999999") || !strings.Contains(sum.EndReason, "10.0 yd") {
		t.Fatalf("end reason %q", sum.EndReason)
	}
	if !hasNote(f.summary(), "10.0 yd") {
		t.Fatalf("notes %v", f.summary().Notes)
	}
}

// F2 (D3): when the quiet reply cut its near list (len(near) < n_near) and the farthest unit it still lists stands
// inside the landing reach, the placement is refused at once: the units the cut dropped may stand where the raid lands.
func TestPlaceQuietCutNearListInsideReachRefuses(t *testing.T) {
	f, p := newPlaceFixture(t)
	rows, plan := placePlanRows(t)
	f.agent.ttFn = func(cmd string) (ttResult, error) {
		if strings.HasPrefix(cmd, "quiet") {
			var near []any
			for i, d := range []float32{10.0, 11.0, 12.0} {
				near = append(near, map[string]any{"g": fmt.Sprintf("0xF130000000A%03d", i+1), "e": uint32(999000 + i),
					"n": "Roamer", "p": []float32{plan.Point.X + d, plan.Point.Y, plan.Point.Z}, "d": d})
			}
			return ttReplyOf(t, map[string]any{"map": 309, "read": 2, "final": true, "quiet": false, "near": near, "n_near": 30, "roamers": []any{}}), nil
		}
		return p.answer(cmd)
	}
	f.agent.startReply = placeStartReply("zul_gurub", plan.Start, len(rows.Steps), plan.Stop)
	if code := f.run(context.Background()); code != exitRaidWalkFailed {
		t.Fatalf("exit %d, want 18: %s", code, f.summary().EndReason)
	}
	if len(p.places) != 0 {
		t.Fatalf("the walk placed with a near list cut inside the reach: %v", p.places)
	}
	if sum := f.summary(); !strings.Contains(sum.EndReason, "near list was cut at 12.0 yd") {
		t.Fatalf("end reason %q", sum.EndReason)
	}
}

// F2 (D3): a cut near list whose farthest listed unit stands beyond the landing reach is judged as before (guards
// against F2 refusing too much).
func TestPlaceQuietCutNearListBeyondReachIsJudged(t *testing.T) {
	f, p := newPlaceFixture(t)
	rows, plan := placePlanRows(t)
	f.agent.ttFn = func(cmd string) (ttResult, error) {
		if strings.HasPrefix(cmd, "quiet") {
			var near []any
			for i, d := range []float32{29.0, 30.0, 31.0} {
				near = append(near, map[string]any{"g": fmt.Sprintf("0xF130000000B%03d", i+1), "e": uint32(999000 + i),
					"n": "Roamer", "p": []float32{plan.Point.X + d, plan.Point.Y, plan.Point.Z}, "d": d})
			}
			return ttReplyOf(t, map[string]any{"map": 309, "read": 2, "final": true, "quiet": false, "near": near, "n_near": 30, "roamers": []any{}}), nil
		}
		return p.answer(cmd)
	}
	f.agent.startReply = placeStartReply("zul_gurub", plan.Start, len(rows.Steps), plan.Stop)
	if code := f.run(context.Background()); code != exitOK {
		t.Fatalf("exit %d, want 0: %s", code, f.summary().EndReason)
	}
}

// L2 (F2): the reach's own limit is inside. A cut near list whose farthest listed unit stands at exactly 25 yd refuses
// the placement; at 25.1 yd it is judged as before (the unit is beyond where the raid lands).
func TestPlaceQuietCutNearListAtTheReach(t *testing.T) {
	cut := func(t *testing.T, d float32) *placeQuietRead {
		t.Helper()
		body, ok := ttLast(ttReplyOf(t, map[string]any{"n_near": 30, "near": []any{
			map[string]any{"g": "0xF130000000C001", "e": 999001, "n": "Roamer", "p": []float32{5, 0, 0}, "d": 5.0},
			map[string]any{"g": "0xF130000000C002", "e": 999002, "n": "Roamer", "p": []float32{d, 0, 0}, "d": d},
		}}))
		if !ok {
			t.Fatal("the reply has no body")
		}
		r := &placeQuietRead{}
		r.nearCutOf(body, nearUnitsOf(body))
		return r
	}
	if r := cut(t, placeReachYd); !r.NearCut || r.NearCutYd != placeReachYd {
		t.Fatalf("a cut list at exactly %v yd is not refused: %+v", placeReachYd, r)
	}
	if r := cut(t, placeReachYd+0.1); r.NearCut {
		t.Fatalf("a cut list at %v yd is refused: %+v", placeReachYd+0.1, r)
	}
}

// placeCircleWithEntry is the plan's first clear circle whose entries hold an entry.
func placeCircleWithEntry(t *testing.T, plan raidwalk.PlacePlan, entry uint32) raidwalk.ClearCircle {
	t.Helper()
	for _, c := range plan.Circles {
		if inEntries(c.Entries, entry) {
			return c
		}
	}
	t.Fatalf("the fixture plan has no clear circle holding %d", entry)
	return raidwalk.ClearCircle{}
}

// F3 (D5): the pre-read does not refuse a circle for BEHIND trash whose entry belongs to another corridor row and whose
// home lies outside every AHEAD row (walk 24 test2-arlokk: row 264 skipped for the Axe Thrower 11350 of rows 266/268).
func TestPlacePreReadClearsTrashOfAnotherCorridorRow(t *testing.T) {
	f, p := newPlaceFixture(t)
	rows, plan := placePlanRows(t)
	row94 := placeRow94Circle(t, plan)
	owner := placeCircleWithEntry(t, plan, 11371)
	home := [3]float32{owner.X, owner.Y, owner.Z}
	at := []float32{row94.X, row94.Y, row94.Z}
	p.units = func(cmd string) ([]any, bool) {
		if fl := strings.Fields(cmd); len(fl) == 7 && fl[3] == num32(row94.X) && fl[4] == num32(row94.Y) {
			return []any{map[string]any{"g": "0xF130000000C001", "e": 11371, "n": "Trash of another row",
				"p": at, "dz": 0.5, "d": 1.0, "hpv": []float64{4000, 4000}, "rk": 0, "db": 49001,
				"home": []float32{home[0], home[1], home[2]}}}, false
		}
		return nil, false
	}
	f.agent.startReply = placeStartReply("zul_gurub", plan.Start, len(rows.Steps), plan.Stop)
	if code := f.run(context.Background()); code != exitOK {
		t.Fatalf("exit %d: %s", code, f.summary().EndReason)
	}
	want := fmt.Sprintf("clearto 309 %s %s %s %s dz=8 all=1", num32(row94.X), num32(row94.Y), num32(row94.Z), num32(row94.Radius))
	var found bool
	for _, c := range p.cleartos {
		if strings.HasPrefix(c, want) {
			found = true
		}
	}
	if !found {
		t.Fatalf("row 94 was not cleared: cleartos %v, not_cleared %v", p.cleartos, f.summary().Place.NotCleared)
	}
	for _, s := range f.summary().Place.NotCleared {
		if strings.Contains(s, fmt.Sprintf("row %d", row94.Step)) {
			t.Fatalf("row %d still refused: %v", row94.Step, s)
		}
	}
}

// F3 (D5): the same unit stays a refusal when its home lies in an AHEAD row's area (pins the AHEAD exclusion).
func TestPlacePreReadKeepsForeignUnitWithAheadHome(t *testing.T) {
	f, p := newPlaceFixture(t)
	rows, plan := placePlanRows(t)
	row94 := placeRow94Circle(t, plan)
	_, aheadHome := placeAheadB(t, plan)
	at := []float32{row94.X, row94.Y, row94.Z}
	p.units = func(cmd string) ([]any, bool) {
		if fl := strings.Fields(cmd); len(fl) == 7 && fl[3] == num32(row94.X) && fl[4] == num32(row94.Y) {
			return []any{map[string]any{"g": "0xF130000000C101", "e": 11371, "n": "Wanderer of a later leg",
				"p": at, "dz": 0.5, "d": 1.0, "hpv": []float64{4000, 4000}, "rk": 0, "db": 49002,
				"home": []float32{aheadHome[0], aheadHome[1], aheadHome[2]}}}, false
		}
		return nil, false
	}
	f.agent.startReply = placeStartReply("zul_gurub", plan.Start, len(rows.Steps), plan.Stop)
	if code := f.run(context.Background()); code != exitOK {
		t.Fatalf("exit %d: %s", code, f.summary().EndReason)
	}
	var found bool
	for _, s := range f.summary().Place.NotCleared {
		if strings.Contains(s, fmt.Sprintf("row %d", row94.Step)) && strings.Contains(s, "stands in the circle") {
			found = true
		}
	}
	if !found {
		t.Fatalf("row %d was not refused for an AHEAD-home unit: not_cleared %v", row94.Step, f.summary().Place.NotCleared)
	}
}

// Review round 3 (positive ownership): a unit that carries a corridor row's entry is not trash unless a BEHIND pull
// row owns its home. A later leg's patrol carries an entry another of the corridor's circles clears and stands in the
// first circle, but its home lies outside every BEHIND row's area and every AHEAD circle: the pre-read must refuse the
// circle, and no clearto may go out (the entry-only rule despawned the patrol).
func TestPlacePreReadSkipsLaterLegPatrolSharingAnEntry(t *testing.T) {
	f, p := newPlaceFixture(t)
	rows, plan := placePlanRows(t)
	c := plan.Circles[0]
	var entry uint32 // a corridor entry another circle owns, not one of this circle's
	for _, other := range plan.Circles {
		if len(other.Entries) > 0 && !inEntries(c.Entries, other.Entries[0]) {
			entry = other.Entries[0]
			break
		}
	}
	if entry == 0 {
		t.Fatal("the fixture's corridor circles share every entry")
	}
	pos := placeInsideWithoutAhead(t, plan, c)
	home := placeFarHome(t, plan, c)
	if placeInAnyBehindRow(plan, home[0], home[1], home[2]) {
		t.Fatalf("the fixture's home %.2f %.2f %.2f lies in a BEHIND row", home[0], home[1], home[2])
	}
	p.units = func(cmd string) ([]any, bool) {
		if fl := strings.Fields(cmd); len(fl) == 7 && fl[3] == num32(c.X) && fl[4] == num32(c.Y) {
			return []any{map[string]any{"g": "0xF130000000E001", "e": entry, "n": "Patrol of a later leg",
				"p": []float32{pos[0], pos[1], pos[2]}, "dz": 0.5, "d": 1.0, "hpv": []float64{4000, 4000}, "rk": 0,
				"db": 49002, "home": []float32{home[0], home[1], home[2]}}}, false
		}
		return nil, false
	}
	f.agent.startReply = placeStartReply("zul_gurub", plan.Start, len(rows.Steps), plan.Stop)
	if code := f.run(context.Background()); code != exitOK {
		t.Fatalf("exit %d: %s", code, f.summary().EndReason)
	}
	want := fmt.Sprintf("clearto 309 %s %s %s %s dz=8", num32(c.X), num32(c.Y), num32(c.Z), num32(c.Radius))
	for _, cl := range p.cleartos {
		if strings.HasPrefix(cl, want) {
			t.Fatalf("the pre-read cleared the circle of row %d with a later leg's patrol in it: %q", c.Step, cl)
		}
	}
	var found bool
	for _, s := range f.summary().Place.NotCleared {
		if strings.HasPrefix(s, fmt.Sprintf("row %d: unit %d", c.Step, entry)) && strings.Contains(s, "stands in the circle") {
			found = true
		}
	}
	if !found {
		t.Fatalf("row %d was not refused for the patrol: not_cleared %v", c.Step, f.summary().Place.NotCleared)
	}
	if hasNote(f.summary(), "trash of a skipped leg") {
		t.Fatalf("the walk took the patrol as trash: %v", f.summary().Notes)
	}
}

// F3 (D5, review round 3): placeBehindTrash. All four conditions hold: the reply carried a home, a BEHIND pull row
// holds the entry AND owns the home (its radius + 6 yd, any pull row before the start, not only the corridor's
// circles), the unit is not boss evidence where it stands or at its home, and its home lies in no AHEAD row's area.
func TestPlaceBehindTrash(t *testing.T) {
	plan := &dungeon.Plan{Legs: []dungeon.Leg{{
		Name: "one",
		Boss: &dungeon.Boss{Entry: 8, Position: dungeon.Point{X: 200, Y: 0, Z: 0}},
	}}}
	w := &walk{
		place: &raidwalk.PlacePlan{
			// Entry 8 is also a BEHIND row's, so the "entry 8 (boss)" case exercises the BossRows check (not the
			// ownership check).
			Behind: []raidwalk.BehindRow{{Step: 90, X: 0, Y: 0, Z: 0, Radius: 10, Entries: []uint32{7, 8}}},
			Ahead:  []raidwalk.AheadCircle{{Step: 200, X: 100, Y: 0, Z: 0, Radius: 5, Entries: []uint32{9}}},
		},
		bossRule: raidwalk.NewBossRows(plan),
	}
	hom := func(entry uint32, home [3]float32) ttUnit {
		return ttUnit{Entry: entry, X: 0, Y: 0, Home: home, HomeOK: true}
	}
	if !w.placeBehindTrash(hom(7, [3]float32{0, 0, 0})) {
		t.Fatal("entry 7 held by a BEHIND row with a home in its area is not trash")
	}
	// The row's radius + 6 yd is the edge of its ownership, and the entry alone is not enough.
	if !w.placeBehindTrash(hom(7, [3]float32{16, 0, 0})) {
		t.Fatal("a home at the row's radius + 6 yd is not owned")
	}
	if w.placeBehindTrash(hom(7, [3]float32{17, 0, 0})) {
		t.Fatal("a home beyond the row's radius + 6 yd is trash")
	}
	if w.placeBehindTrash(ttUnit{Entry: 7}) {
		t.Fatal("a unit with no home is trash")
	}
	if w.placeBehindTrash(hom(6, [3]float32{0, 0, 0})) {
		t.Fatal("an entry no BEHIND row holds is trash")
	}
	if w.placeBehindTrash(hom(7, [3]float32{100, 0, 0})) {
		t.Fatal("a unit whose home lies in an AHEAD row is trash")
	}
	if w.placeBehindTrash(hom(8, [3]float32{0, 0, 0})) {
		t.Fatal("a boss entry is trash")
	}
}

// F3 (D5): the quiet's extra round takes BEHIND trash within the quiet radius even when no corridor circle owns its
// home; beyond that radius it is never taken (the quiet's own reach).
func TestQuietExtraClearTakesTrashWithinQuietRadius(t *testing.T) {
	t.Run("within the quiet radius", func(t *testing.T) {
		f, p := newPlaceFixture(t)
		rows, plan := placePlanRows(t)
		// The home is a BEHIND pull row's point that owns entry 15111 outside every corridor circle: positive ownership
		// (review round 3), so the extra round takes it as trash rather than as a circle's own unit.
		home := placeOwnedHome(t, plan, 15111)
		p.quiet = func(n int) bool { return n >= 2 }
		p.quietNear = []any{quietUnitHome("0xF130000000D001", 15111, plan.Point.X+10, plan.Point.Y, plan.Point.Z, home)}
		f.agent.startReply = placeStartReply("zul_gurub", plan.Start, len(rows.Steps), plan.Stop)
		if code := f.run(context.Background()); code != exitOK {
			t.Fatalf("exit %d: %s", code, f.summary().EndReason)
		}
		if n := extraClearCount(p); n != 1 {
			t.Fatalf("%d extra cleartos, want 1: %v", n, p.cleartos)
		}
		if !hasNote(f.summary(), "trash of a skipped leg") {
			t.Fatalf("notes %v", f.summary().Notes)
		}
	})
	t.Run("beyond the quiet radius", func(t *testing.T) {
		f, p := newPlaceFixture(t)
		rows, plan := placePlanRows(t)
		const guid = "0xF130000000D002"
		at := [3]float32{plan.Point.X + 60, plan.Point.Y, plan.Point.Z}
		home := [3]float32{plan.Point.X + 65, plan.Point.Y, plan.Point.Z}
		p.quiet = func(int) bool { return false }
		p.quietRoamers = []any{quietRoamAt(guid, 15111, "moved", 60, 3)}
		p.units = joinHook([]any{joinUnitAt(guid, 15111, at[0], at[1], at[2], home, 4000, 0)})
		f.agent.startReply = placeStartReply("zul_gurub", plan.Start, len(rows.Steps), plan.Stop)
		if code := f.run(context.Background()); code != exitOK {
			t.Fatalf("exit %d: %s", code, f.summary().EndReason)
		}
		if n := extraClearCount(p); n != 0 {
			t.Fatalf("the extra round took a unit 60 yd out, beyond the quiet's %d yd radius: %v", placeQuietR, p.cleartos)
		}
	})
}

// Review round 3 (positive ownership, the walk-24 Venoxis case): a bat whose home lies in a BEHIND Jeklik pull row's
// area is still BEHIND trash even though no corridor circle holds its entry, and the extra round clears it. The bat
// stands 10 yd from the start; its home is a Jeklik bat row (entry 11368 at row 13 of the deployed rows: -11983.13
// -1471.61 79.97, 8.8 yd), which the corridor never clears.
func TestQuietExtraClearTakesABatOwnedByABehindJeklikRow(t *testing.T) {
	f, p := newPlaceFixture(t)
	rows, plan := placePlanRows(t)
	for _, c := range plan.Circles {
		if inEntries(c.Entries, 11368) {
			t.Fatalf("the fixture's corridor holds entry 11368 in row %d: the test needs a bat row outside it", c.Step)
		}
	}
	home := placeOwnedHome(t, plan, 11368)
	p.quiet = func(n int) bool { return n >= 2 }
	p.quietNear = []any{quietUnitHome("0xF130000000B001", 11368, plan.Point.X+10, plan.Point.Y, plan.Point.Z, home)}
	f.agent.startReply = placeStartReply("zul_gurub", plan.Start, len(rows.Steps), plan.Stop)
	if code := f.run(context.Background()); code != exitOK {
		t.Fatalf("exit %d: %s (notes %v)", code, f.summary().EndReason, f.summary().Notes)
	}
	if n := extraClearCount(p); n != 1 {
		t.Fatalf("%d extra cleartos, want 1: %v", n, p.cleartos)
	}
	if !hasNote(f.summary(), "trash of a skipped leg") {
		t.Fatalf("notes %v", f.summary().Notes)
	}
}

// F4 (D6): a unit the extra rounds cannot take and that stands beyond the landing reach is left alive, with a note
// (walk 24 test2-arlokk: the owned Tiger at 46.5 yd standing at its home).
func TestPlaceQuietLeavesOwnedUnitBeyondReachAlive(t *testing.T) {
	f, p := newPlaceFixture(t)
	rows, plan := placePlanRows(t)
	owner := placeRow94Circle(t, plan)
	home := [3]float32{owner.X, owner.Y, owner.Z}
	p.quiet = func(int) bool { return false }
	p.quietNear = []any{quietUnitHome("0xF130000000F001", 15111, plan.Point.X+46, plan.Point.Y, plan.Point.Z, home)}
	p.units = func(cmd string) ([]any, bool) {
		if fl := strings.Fields(cmd); len(fl) > 2 && fl[2] == "3" { // the extra round's read: a stranger, so it cannot take
			return []any{map[string]any{"g": "0xF130000000F002", "e": 999999, "n": "Stranger",
				"p": []float32{0, 0, 0}, "dz": 0.0, "d": 1.0}}, false
		}
		return nil, false
	}
	f.agent.startReply = placeStartReply("zul_gurub", plan.Start, len(rows.Steps), plan.Stop)
	if code := f.run(context.Background()); code != exitOK {
		t.Fatalf("exit %d, want 0: %s", code, f.summary().EndReason)
	}
	if len(p.places) != 1 {
		t.Fatalf("%d place commands, want one", len(p.places))
	}
	if !hasNote(f.summary(), "leaves") || !hasNote(f.summary(), "alive") {
		t.Fatalf("the walk did not leave the unit alive: %v", f.summary().Notes)
	}
}

// F4 (D6): the quiet gets two extra clear rounds. Read 1 lists an owned unit A, read 2 an owned unit B, read 3 is
// quiet: both A and B are cleared.
func TestPlaceQuietTwoExtraRounds(t *testing.T) {
	f, p := newPlaceFixture(t)
	rows, plan := placePlanRows(t)
	owner := placeRow94Circle(t, plan)
	home := [3]float32{owner.X, owner.Y, owner.Z}
	nearFor := map[int]any{
		1: quietUnitHome("0xF130000000F101", 15111, plan.Point.X+10, plan.Point.Y, plan.Point.Z, home),
		2: quietUnitHome("0xF130000000F102", 15111, plan.Point.X+12, plan.Point.Y, plan.Point.Z, home),
	}
	f.agent.ttFn = func(cmd string) (ttResult, error) {
		if strings.HasPrefix(cmd, "quiet") {
			p.quietCalls++
			q := p.quietCalls >= 3
			near := []any{}
			if !q {
				near = []any{nearFor[p.quietCalls]}
			}
			return ttReplyOf(t, map[string]any{"map": 309, "read": 2, "final": true, "quiet": q, "near": near, "roamers": []any{}}), nil
		}
		return p.answer(cmd)
	}
	f.agent.startReply = placeStartReply("zul_gurub", plan.Start, len(rows.Steps), plan.Stop)
	if code := f.run(context.Background()); code != exitOK {
		t.Fatalf("exit %d, want 0: %s", code, f.summary().EndReason)
	}
	if n := extraClearCount(p); n != 2 {
		t.Fatalf("%d extra cleartos, want 2: %v", n, p.cleartos)
	}
	if p.quietCalls != 3 {
		t.Fatalf("%d quiet reads, want 3", p.quietCalls)
	}
}

// F4 (D6): a third owned unit inside the landing reach blocks once the two rounds are spent (walk 24 m910q-ven).
func TestPlaceQuietThirdOwnedUnitInReachBlocks(t *testing.T) {
	f, p := newPlaceFixture(t)
	rows, plan := placePlanRows(t)
	owner := placeRow94Circle(t, plan)
	home := [3]float32{owner.X, owner.Y, owner.Z}
	c := quietUnitHome("0xF130000000F201", 15111, plan.Point.X+10, plan.Point.Y, plan.Point.Z, home)
	nearFor := map[int]any{
		1: quietUnitHome("0xF130000000F202", 15111, plan.Point.X+12, plan.Point.Y, plan.Point.Z, home),
		2: quietUnitHome("0xF130000000F203", 15111, plan.Point.X+14, plan.Point.Y, plan.Point.Z, home),
		3: c, 4: c, 5: c,
	}
	f.agent.ttFn = func(cmd string) (ttResult, error) {
		if strings.HasPrefix(cmd, "quiet") {
			p.quietCalls++
			near := []any{}
			if u, ok := nearFor[p.quietCalls]; ok {
				near = []any{u}
			}
			return ttReplyOf(t, map[string]any{"map": 309, "read": 2, "final": true, "quiet": false, "near": near, "roamers": []any{}}), nil
		}
		return p.answer(cmd)
	}
	f.agent.startReply = placeStartReply("zul_gurub", plan.Start, len(rows.Steps), plan.Stop)
	if code := f.run(context.Background()); code != exitRaidWalkFailed {
		t.Fatalf("exit %d, want 18: %s", code, f.summary().EndReason)
	}
	if n := extraClearCount(p); n != 2 {
		t.Fatalf("%d extra cleartos, want 2: %v", n, p.cleartos)
	}
	if sum := f.summary(); !strings.Contains(sum.EndReason, "15111") {
		t.Fatalf("end reason %q", sum.EndReason)
	}
}

// F5 (D8): a placed `--until none` run from Marli shortens its until to stop before the segment that passes Venoxis,
// and the run start carries the shortened clause (walk 24 m910q-reach: the restart refused with "boss/event row 185").
func TestPlaceShortenedUntilReachesRunStart(t *testing.T) {
	f, _ := newPlaceFixture(t)
	f.opts.from = "leg high_priestess_marli"
	f.opts.until = "none"
	plan := zgPlan(t)
	res, err := planexport.Build(plan, planexport.Options{})
	if err != nil {
		t.Fatal(err)
	}
	rows := res.Rows
	start, _, err := raidwalk.PlaceStart(rows, []string{"from", "leg", "high_priestess_marli"})
	if err != nil {
		t.Fatal(err)
	}
	until, _, _, refusal, hit := raidwalk.PlaceBossGuard(rows, start, len(rows.Steps))
	if refusal != "" {
		t.Fatalf("the guard refused: %q", refusal)
	}
	if !hit {
		t.Fatal("the guard did not report the segment")
	}
	if until == len(rows.Steps) {
		t.Fatal("the guard did not shorten the until")
	}
	label := fmt.Sprintf("until step %d", until)
	status := fmt.Sprintf("TBM:RUN|zul_gurub|running|%d/%d|marli|move|walk|walking|%s (step %d)", start, len(rows.Steps), label, until)
	f.agent.startReply = &commanderReply{Reply: tbm.Reply{Kind: tbm.KindAck, OK: true,
		Ack:  &tbm.Ack{Intent: "run start", Scope: "party", Count: 1, Executor: "-"},
		Line: status,
		Lines: []string{
			status,
			fmt.Sprintf("The run starts at step %d of %d (leg high_priestess_marli)", start, len(rows.Steps)),
			"TBM:ACTION_ACK|run start|party|1|-",
		}}}
	f.agent.startQueue = []queuedEvent{
		{raw: status},
		{raw: fmt.Sprintf("TBM:RUN|zul_gurub|done|%d/%d|marli|move|walk|stopped before step %d (%s): the party stays where it stands", start, len(rows.Steps), until, label)},
	}
	code := f.run(context.Background())
	if code != exitOK {
		t.Fatalf("exit %d: %s\n%s", code, f.stderr.String(), f.stdout.String())
	}
	if want := fmt.Sprintf("until|step|%d", until); !strings.HasSuffix(f.agent.startArgs, want) {
		t.Fatalf("run start args %q, want the suffix %q", f.agent.startArgs, want)
	}
	if !hasNote(f.summary(), "the run stops before step") {
		t.Fatalf("notes %v", f.summary().Notes)
	}
}

// placeLastSegmentRows is the H1 fixture (F5, D8) as rows of plan zul_gurub: leg 0's boss at (20,150), leg 1's moves
// along +y at (0,10)..(0,100), and leg 2's last row at (20,140), 10 yd from the boss. From step 5 the guard's hit is
// on the final segment (11 -> 12), so until stays the stop and only hit tells the caller to shorten.
// The last row is a move, or with bossLast the plan's boss at the same point (so `--until boss-done` resolves to
// `none`: the boss is the plan's last step).
func placeLastSegmentRows(bossLast bool) planexport.Rows {
	const name = "zul_gurub"
	rows := planexport.Rows{Plan: planexport.PlanRow{Plan: name, Map: 309}, Legs: []planexport.LegRow{
		{Plan: name, Leg: 0, Name: "one"}, {Plan: name, Leg: 1, Name: "two"}, {Plan: name, Leg: 2, Name: "three"},
	}}
	add := func(s planexport.StepRow) {
		s.Plan, s.Seq = name, len(rows.Steps)+1
		rows.Steps = append(rows.Steps, s)
	}
	add(planexport.StepRow{Kind: planexport.KindBoss, Leg: 0, X: 20, Y: 150, Entries: []uint32{12345}}) // step 1
	for i := range 10 {                                                                                 // steps 2..11
		add(planexport.StepRow{Kind: planexport.KindMove, Leg: 1, Y: float32(10 + i*10)})
	}
	last := planexport.KindMove
	if bossLast {
		last = planexport.KindBoss
	}
	add(planexport.StepRow{Kind: last, Leg: 2, X: 20, Y: 140, Entries: []uint32{12345}}) // step 12: the last row
	return rows
}

// placeSQLFile writes rows as a migration file for the walk's --sql (planexport's own writer and reader).
func placeSQLFile(t *testing.T, rows planexport.Rows) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "walk.sql")
	if err := os.WriteFile(path, []byte(planexport.WriteSQL([]planexport.Rows{rows})), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

// H1 (D8): with `--until none` the guard's hit is on the last segment, where until already equals the stop. The walk
// must shorten to `until step 12` all the same: the run never walks 11 -> 12 past the BEHIND boss of leg 0.
func TestPlaceShortensAnUntilNoneHitOnTheLastSegment(t *testing.T) {
	f, _ := newPlaceFixture(t)
	rows := placeLastSegmentRows(false)
	f.opts.dryRun = true
	f.opts.from, f.opts.until, f.opts.sql = "step 5", "none", placeSQLFile(t, rows)
	if code := f.run(context.Background()); code != exitOK {
		t.Fatalf("exit %d: %s", code, f.stderr.String())
	}
	out := f.stdout.String()
	for _, want := range []string{
		"until none (the place shortens it to until step 12)",
		"8b place: start step 5 at 0 40 0; until step 12;",
		"9 commander: run start zul_gurub from step 5 until step 12",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("the dry run misses %q:\n%s", want, out)
		}
	}
}

// H1 (D8): a boss-done run whose boss is the plan's last step resolves to `none`, and the guard's hit on the last
// segment would stop the run before that boss: the placement must be refused (exit 2), not shortened past the boss.
func TestPlaceRefusesAShortenedBossDoneOnTheLastSegment(t *testing.T) {
	f, _ := newPlaceFixture(t)
	rows := placeLastSegmentRows(true)
	f.opts.dryRun = true
	f.opts.from, f.opts.until, f.opts.sql = "step 5", "boss-done", placeSQLFile(t, rows)
	if code := f.run(context.Background()); code != exitUsage {
		t.Fatalf("exit %d, want %d: %s", code, exitUsage, f.stderr.String())
	}
	if !strings.Contains(f.stderr.String(), "that is before the boss-done step 12") {
		t.Fatalf("stderr %q", f.stderr.String())
	}
}

// L5 (F5): a shortened until is printed next to the one the user asked for, in the dry run's header and in the
// summary, so the log never says `until none` for a run that walked `until step 12`.
func TestPlaceShortenedUntilIsPrinted(t *testing.T) {
	rows := placeLastSegmentRows(false)
	const want = "until none (the place shortens it to until step 12)"
	f, _ := newPlaceFixture(t)
	f.opts.dryRun = true
	f.opts.from, f.opts.until, f.opts.sql = "step 5", "none", placeSQLFile(t, rows)
	if code := f.run(context.Background()); code != exitOK {
		t.Fatalf("dry run exit %d: %s", code, f.stderr.String())
	}
	if out := f.stdout.String(); !strings.Contains(out, want) {
		t.Fatalf("the dry run's header misses %q:\n%s", want, out)
	}

	f2, _ := newPlaceFixture(t)
	f2.opts.from, f2.opts.until, f2.opts.sql = "step 5", "none", placeSQLFile(t, rows)
	status := "TBM:RUN|zul_gurub|running|5/12|thekal|move|walk|walking|until step 12 (step 12)"
	f2.agent.startReply = &commanderReply{Reply: tbm.Reply{Kind: tbm.KindAck, OK: true,
		Ack:  &tbm.Ack{Intent: "run start", Scope: "party", Count: 1, Executor: "-"},
		Line: status,
		Lines: []string{
			status,
			"The run starts at step 5 of 12 (leg one)",
			"TBM:ACTION_ACK|run start|party|1|-",
		}}}
	f2.agent.startQueue = []queuedEvent{
		{raw: status},
		{raw: "TBM:RUN|zul_gurub|done|5/12|thekal|move|walk|stopped before step 12 (until step 12): the party stays where it stands"},
	}
	if code := f2.run(context.Background()); code != exitOK {
		t.Fatalf("exit %d: %s\n%s", code, f2.stderr.String(), f2.stdout.String())
	}
	text, err := os.ReadFile(filepath.Join(f2.out, "summary.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(text), want) {
		t.Fatalf("the summary misses %q:\n%s", want, text)
	}
	if sum := f2.summary(); sum.UntilEffective != "until step 12" || sum.Until != "none" {
		t.Fatalf("summary.json until %q, until_effective %q", sum.Until, sum.UntilEffective)
	}
}

// A unit is owned only by a BEHIND circle whose area holds its home (review round 2, LOW-1): the same entry, far away,
// is not, and a reply without a home gives no owner even where the unit stands (fail closed).
func TestPlaceRogueOwnerNeedsNearCircle(t *testing.T) {
	w := &walk{place: &raidwalk.PlacePlan{Circles: []raidwalk.ClearCircle{
		{Step: 90, X: 0, Y: 0, Z: 0, Radius: 10, Entries: []uint32{7}},
	}}}
	homed := func(entry uint32, x float32) ttUnit {
		return ttUnit{Entry: entry, X: x, Home: [3]float32{x, 0, 0}, HomeOK: true}
	}
	if c, ok := w.placeRogueOwner(homed(7, 10+placeRogueYd-1)); !ok || c.Step != 90 {
		t.Fatalf("a unit inside radius + %d yd is not owned: %+v %v", placeRogueYd, c, ok)
	}
	if _, ok := w.placeRogueOwner(homed(7, 200)); ok {
		t.Fatal("a unit far from the only circle that has its entry is owned")
	}
	if _, ok := w.placeRogueOwner(homed(8, 0)); ok {
		t.Fatal("a unit with an entry no circle holds is owned")
	}
	if _, ok := w.placeRogueOwner(ttUnit{Entry: 7, X: 0}); ok {
		t.Fatal("a unit with no home is owned")
	}
}

// placeGM reads system and addon events, so no TBM line is skipped unlogged (review round 2, LOW-2).
func TestPlaceGMReadsAddonEvents(t *testing.T) {
	f, _ := newPlaceFixture(t)
	rows, plan := placePlanRows(t)
	f.agent.startReply = placeStartReply("zul_gurub", plan.Start, len(rows.Steps), plan.Stop)
	if code := f.run(context.Background()); code != exitOK {
		t.Fatalf("exit %d: %s", code, f.stderr.String())
	}
	f.agent.mu.Lock()
	kinds := strings.Join(f.agent.gmOnKinds, ",")
	f.agent.mu.Unlock()
	if kinds != "system,addon" {
		t.Fatalf("the GM-mode probe read event kinds %q, want system,addon", kinds)
	}
}

// leaveWalk is a walk with just what placeLeave needs, over the fixture's fake agent and clock.
func leaveWalk(t *testing.T, f *fixture, sleep func(context.Context, time.Duration) error) *walk {
	t.Helper()
	return &walk{plan: zgPlan(t), agent: f.agent, env: walkEnv{now: f.clock.now, sleep: sleep}}
}

// The teardown's unbind waits for the portal map to land (review round 2, LOW-3).
func TestPlaceLeaveUnbindsAfterPortalMapLands(t *testing.T) {
	f := newFixture(t)
	portal := zgPlan(t).Portal
	sleeps := 0
	w := leaveWalk(t, f, func(ctx context.Context, d time.Duration) error {
		sleeps++
		if sleeps == 3 {
			f.agent.mu.Lock()
			f.agent.status.Map = portal.Map
			f.agent.mu.Unlock()
		}
		return f.clock.sleep(ctx, d)
	})
	mapAtUnbind := uint32(0)
	f.agent.onGM = func(line string) {
		f.agent.mu.Lock()
		defer f.agent.mu.Unlock()
		switch {
		case strings.HasPrefix(line, ".go xyz "):
			f.agent.status.Map = 309 // the landing is slow: the status still shows the raid's copy
		case strings.HasPrefix(line, ".instance unbind"):
			mapAtUnbind = f.agent.status.Map
		}
	}
	w.placeLeave(context.Background())
	if mapAtUnbind != portal.Map {
		t.Fatalf("the unbind went out with the commander on map %d, not the portal's %d (slept %d times)", mapAtUnbind, portal.Map, sleeps)
	}
	if sleeps < 3 {
		t.Fatalf("slept %d times: the wait did not poll", sleeps)
	}
}

// The unbind has a context of its own: a wait that used up the caller's context does not cancel it (review round 2,
// LOW-3).
func TestPlaceLeaveUnbindHasOwnContext(t *testing.T) {
	f := newFixture(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	w := leaveWalk(t, f, f.clock.sleep)
	f.agent.onGM = func(line string) {
		if strings.HasPrefix(line, ".go xyz ") {
			cancel() // the landing took the whole context
		}
	}
	w.placeLeave(ctx)
	lines := f.agent.gmLines()
	if len(lines) == 0 || lines[len(lines)-1] != ".instance unbind 309" {
		t.Fatalf("the unbind did not go out: %v", lines)
	}
	f.agent.mu.Lock()
	err := f.agent.gmCtxErr[len(f.agent.gmCtxErr)-1]
	f.agent.mu.Unlock()
	if err != nil {
		t.Fatalf("the unbind was sent on a dead context: %v", err)
	}
}

// The server's start text is a note only for a placed walk (review round 2, LOW-3; round 1, item 10).
func TestStartTextNoteOnlyForPlacedWalk(t *testing.T) {
	t.Run("plain walk", func(t *testing.T) {
		f := newFixture(t)
		rep := startAt("onyxias_lair", 1)
		rep.Reply.Lines = append([]string{rep.Reply.Lines[0], "The run starts at step 1 of 29 (leg lair)"}, rep.Reply.Lines[1:]...)
		f.agent.startReply = rep
		f.defaultRun("onyxias_lair")
		if code := f.run(context.Background()); code != exitOK {
			t.Fatalf("exit %d: %s", code, f.stderr.String())
		}
		for _, n := range f.summary().Notes {
			if strings.Contains(n, "run starts at step") {
				t.Fatalf("a plain walk noted the server's start text: %q", n)
			}
		}
	})
	t.Run("placed walk", func(t *testing.T) {
		f, _ := newPlaceFixture(t)
		rows, plan := placePlanRows(t)
		f.agent.startReply = placeStartReply("zul_gurub", plan.Start, len(rows.Steps), plan.Stop)
		if code := f.run(context.Background()); code != exitOK {
			t.Fatalf("exit %d: %s", code, f.stderr.String())
		}
		if !hasNote(f.summary(), "place: The run starts at step") {
			t.Fatalf("notes %v", f.summary().Notes)
		}
	})
}

// H1: --place --from leg high_priestess_jeklik (ZG leg 0) is a door start: no error, no placement, the run
// starts at step 1 from the entrance exactly as a non-placed run does, and the notes say so.
func TestPlaceFirstLegStartsFromDoor(t *testing.T) {
	f := newFixture(t)
	f.opts.plan = "zul_gurub"
	f.opts.holder = "raid-walk-zul_gurub"
	f.opts.from = "leg high_priestess_jeklik"
	f.opts.place = true
	f.opts.dryRun = true
	if code := f.run(context.Background()); code != exitOK {
		t.Fatalf("exit %d: %s", code, f.stderr.String())
	}
	got := f.stdout.String()
	// The dry run shows the entrance path, not the place phase: a door start keeps the leg clause, plans
	// no .go and no 8b, and runs from the leg exactly as a non-placed run does.
	for _, want := range []string{"5 gm: .go xyz", "8 commander: cmd enter near", "  9 commander: run start zul_gurub from leg high_priestess_jeklik until boss"} {
		if !strings.Contains(got, want) {
			t.Fatalf("the dry run misses %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "8b place:") {
		t.Fatalf("the door start plans a place phase:\n%s", got)
	}
}

// L2: the fallback note is written when the start falls back to an earlier leg. The testdata migration is
// shaped so leg venoxis starts with a pull: the walk places at the jeklik door move, and the boss-done
// until resolves from the venoxis leg's first row to the venoxis boss (step 4, the last row: clause "none",
// so the run start carries no until).
func TestPlaceFallbackNoteWritten(t *testing.T) {
	f, _ := newPlaceFixture(t)
	f.opts.from = "leg high_priest_venoxis"
	f.opts.until = "boss-done"
	f.opts.sql = "testdata/zg-place-fallback-20000000.sql"
	rows, _, err := readRaidMigration(f.opts.sql, "zul_gurub")
	if err != nil {
		t.Fatal(err)
	}
	if len(rows.Steps) != 4 {
		t.Fatalf("the fallback fixture has %d steps", len(rows.Steps))
	}
	step, leg, fallback, door, err := raidwalk.PlaceStart(rows, []string{"from", "leg", "high_priest_venoxis"})
	if err != nil || door {
		t.Fatal(err)
	}
	if step != 1 || leg != "high_priestess_jeklik" || fallback != "high_priest_venoxis" {
		t.Fatalf("start %d (%q fallback %q), want 1 (jeklik)", step, leg, fallback)
	}
	// The walk runs for real (dry runs print no notes): the start queue walks two steps from the
	// fallback start, and the toolkit answers everything quiet.
	f.agent.startQueue = []queuedEvent{
		{raw: "TBM:RUN|zul_gurub|running|1/4|jeklik|move|walk|walking"},
		{raw: "TBM:RUN|zul_gurub|running|2/4|jeklik|pull|walk|walking"},
		{raw: "TBM:RUN|zul_gurub|done|2/4|jeklik|pull|walk|done"},
	}
	f.agent.startReply = placeStartReply("zul_gurub", step, len(rows.Steps), len(rows.Steps))
	if code := f.run(context.Background()); code != exitOK {
		t.Fatalf("exit %d: %s", code, f.stderr.String())
	}
	if !hasNote(f.summary(), "place: leg high_priest_venoxis has no quiet row; placing at step 1 (leg high_priestess_jeklik)") {
		t.Fatalf("notes %v", f.summary().Notes)
	}
	if sum := f.summary(); sum.Place == nil || sum.Place.Start != 1 {
		t.Fatalf("place summary %+v, want start 1", sum.Place)
	}
}

// The longest-line check counts the extra clear round's lines (review round 2, LOW-4).
func TestPlaceTTMaxCountsExtraRound(t *testing.T) {
	p := &raidwalk.PlacePlan{
		Point:   dungeon.Point{X: 1, Y: 2, Z: 3},
		Circles: []raidwalk.ClearCircle{{X: 1, Y: 2, Z: 3, Radius: 5}},
	}
	long, _ := placeTTMax(p, 309)
	if !strings.HasPrefix(long, "clearto 309 -0.123456789 ") || !strings.Contains(long, " 3 dz=") {
		t.Fatalf("the longest line is %q, not the extra round's clearto", long)
	}
}

// M1 (PLACE-W26 §3): with the deployed 1097-step rows, the Mar'li landing sweep takes BEHIND pull rows 122, 128
// and 130 (today: 0 circles, so the leftover patrol 51962 walks into the landing). The other starts sweep their
// own leftovers: Venoxis from step 52 takes row 50, Thekal takes 87/105, Arlokk takes 255/257/272.
func TestPlaceClearZGSweepsLeftoverPatrols(t *testing.T) {
	rows, _, err := readRaidMigration("testdata/zg-20261101093000.sql", "zul_gurub")
	if err != nil {
		t.Fatal(err)
	}
	plan := zgPlan(t)
	stops := map[string][]string{
		"high_priestess_marli":  {"until", "step", "216"},
		"high_priest_venoxis":   {"until", "step", "186"},
		"high_priest_thekal":    {"until", "boss"},
		"high_priestess_arlokk": {"until", "boss"},
	}
	froms := map[string][]string{
		"high_priestess_marli":  {"from", "leg", "high_priestess_marli"},
		"high_priest_venoxis":   {"from", "step", "52"},
		"high_priest_thekal":    {"from", "leg", "high_priest_thekal"},
		"high_priestess_arlokk": {"from", "leg", "high_priestess_arlokk"},
	}
	// The implementer pins what the code gives: the spec's §3 list was measured by hand. Mar'li matches
	// exactly (122, 128, 130). Thekal takes 87/105 (85 is out of range, the rest are corridor circles);
	// Arlokk takes 255/257/272 (270 is a corridor circle); Venoxis from step 52 takes row 50 plus whatever
	// rows the corridor already covers (the test only requires row 50 among the sweeps).
	want := map[string][]int{
		"high_priestess_marli":  {122, 128, 130},
		"high_priest_venoxis":   {50},
		"high_priest_thekal":    {87, 105},
		"high_priestess_arlokk": {255, 257, 272},
	}
	for _, leg := range []string{"high_priestess_marli", "high_priest_venoxis", "high_priest_thekal", "high_priestess_arlokk"} {
		start, _, err := raidwalk.PlaceStart(rows, froms[leg])
		if err != nil {
			t.Fatalf("%s: %v", leg, err)
		}
		stop, err := raidwalk.PlaceStop(rows, stops[leg], start)
		if err != nil {
			t.Fatalf("%s: %v", leg, err)
		}
		until, routeEnd, _, refusal, _ := raidwalk.PlaceBossGuard(rows, start, stop)
		if refusal != "" {
			t.Fatalf("%s: %s", leg, refusal)
		}
		cp := raidwalk.PlaceClear(rows, plan, start, routeEnd)
		if cp.Refusal != "" {
			t.Fatalf("%s: refused: %s", leg, cp.Refusal)
		}
		var sweeps []int
		seen := map[int]bool{}
		for _, c := range cp.Circles {
			if c.Step == start {
				t.Fatalf("%s: the start step %d has a sweep circle", leg, start)
			}
			if !c.Sweep {
				continue
			}
			sweeps = append(sweeps, c.Step)
			seen[c.Step] = true
		}
		for _, c := range cp.Circles {
			if seen[c.Step] && !c.Sweep {
				t.Fatalf("%s: row %d is both a corridor and a sweep circle", leg, c.Step)
			}
		}
		for i := 1; i < len(sweeps); i++ {
			if sweeps[i-1] >= sweeps[i] {
				t.Fatalf("%s: sweep circles out of step order: %v", leg, sweeps)
			}
		}
		for _, s := range want[leg] {
			var found bool
			for _, got := range sweeps {
				if got == s {
					found = true
				}
			}
			if !found {
				t.Fatalf("%s: sweep steps %v, want %v (stop %d, route end %d)", leg, sweeps, want[leg], until, routeEnd)
			}
		}
	}
}

// M1 (PLACE-W26 §3, TestPlaceClearZGSweepsLeftoverPatrols' guard): every ZG patrol whose closed loop comes within
// 45 yd 2-D (reach 25 + detection 20) and 10 yd in z of a placed start, and whose entry a BEHIND pull row owns by
// home, must be among PlaceClear's circles. Fails today for Mar'li (rows 122 and 128).
func TestZGPlacedStartsCoverEveryLeftoverPatrol(t *testing.T) {
	rows, _, err := readRaidMigration("testdata/zg-20261101093000.sql", "zul_gurub")
	if err != nil {
		t.Fatal(err)
	}
	plan := zgPlan(t)
	spawns, moves, err := zgLeftoverPatrols(t)
	if err != nil {
		t.Fatal(err)
	}
	starts := map[string]int{}
	for _, leg := range []string{"high_priestess_marli", "high_priest_thekal", "high_priestess_arlokk"} {
		start, _, err := raidwalk.PlaceStart(rows, []string{"from", "leg", leg})
		if err != nil {
			t.Fatal(err)
		}
		starts[leg] = start
	}
	start52, _, err := raidwalk.PlaceStart(rows, []string{"from", "step", "52"})
	if err != nil {
		t.Fatal(err)
	}
	starts["high_priest_venoxis"] = start52
	mkPlan := func(start int, stopWords []string) raidwalk.PlacePlan {
		t.Helper()
		stop, err := raidwalk.PlaceStop(rows, stopWords, start)
		if err != nil {
			t.Fatal(err)
		}
		_, routeEnd, _, refusal, _ := raidwalk.PlaceBossGuard(rows, start, stop)
		if refusal != "" {
			t.Fatal(refusal)
		}
		cp := raidwalk.PlaceClear(rows, plan, start, routeEnd)
		if cp.Refusal != "" {
			t.Fatal(cp.Refusal)
		}
		pt, ok := raidwalk.PlacePoint(rows, start)
		if !ok {
			t.Fatalf("step %d has no point", start)
		}
		return raidwalk.PlacePlan{Start: start, Point: pt,
			Circles: cp.Circles, Ahead: cp.Ahead, Behind: cp.Behind, Skips: raidwalk.PlaceSkips(plan)}
	}
	plans := map[string]raidwalk.PlacePlan{
		"high_priestess_marli":  mkPlan(starts["high_priestess_marli"], []string{"until", "step", "216"}),
		"high_priest_venoxis":   mkPlan(starts["high_priest_venoxis"], []string{"until", "step", "186"}),
		"high_priest_thekal":    mkPlan(starts["high_priest_thekal"], []string{"until", "boss"}),
		"high_priestess_arlokk": mkPlan(starts["high_priestess_arlokk"], []string{"until", "boss"}),
	}
	for _, leg := range []string{"high_priestess_marli", "high_priest_venoxis", "high_priest_thekal", "high_priestess_arlokk"} {
		p := plans[leg]
		owned := map[int]bool{}
		for _, c := range p.Circles {
			owned[c.Step] = true
		}
		var missing []string
		for guid, pts := range moves {
			sp := spawns[guid]
			nearest, dz := zgLoopNearest(pts, p.Point)
			if nearest > 45 || dz > 10 {
				continue
			}
			step, ok := p.BehindOwns(sp.entry, sp.x, sp.y, sp.z)
			if !ok {
				continue
			}
			if !owned[step] {
				missing = append(missing, fmt.Sprintf("guid %d entry %d (step %d) reaches %.1f yd, dz %.1f", guid, sp.entry, step, nearest, dz))
			}
		}
		if len(missing) != 0 {
			t.Fatalf("%s (start %d): the clear misses %d leftover patrol(s):\n%s", leg, p.Start, len(missing), strings.Join(missing, "\n"))
		}
	}
}

// zgSpawn is one creature row of the world spawn table.
type zgSpawn struct {
	entry   uint32
	x, y, z float32
}

// zgLeftoverPatrols reads the world spawn and movement tables: the spawn point of every creature with movement rows,
// and its closed loop (the spawn table's point plus every movement point).
func zgLeftoverPatrols(t *testing.T) (map[int]zgSpawn, map[int][][3]float32, error) {
	t.Helper()
	spawns := map[int]zgSpawn{}
	for _, line := range readTSVLines(t, "../../tools/dungeongen/testdata/map309/creatures.tsv") {
		guid := atoiZGT(t, line[0])
		spawns[guid] = zgSpawn{entry: uint32(atoiZGT(t, line[1])), x: atofZGT(t, line[8]), y: atofZGT(t, line[9]), z: atofZGT(t, line[10])}
	}
	moves := map[int][][3]float32{}
	for _, line := range readTSVLines(t, "../../tools/dungeongen/testdata/map309/movement.tsv") {
		guid := atoiZGT(t, line[0])
		sp, ok := spawns[guid]
		if !ok {
			continue
		}
		moves[guid] = append(moves[guid], [3]float32{sp.x, sp.y, sp.z})
		moves[guid] = append(moves[guid], [3]float32{atofZGT(t, line[2]), atofZGT(t, line[3]), atofZGT(t, line[4])})
	}
	return spawns, moves, nil
}

// readTSVLines reads a tab-separated fixture file, skipping its header line.
func readTSVLines(t *testing.T, path string) [][]string {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var out [][]string
	for i, line := range strings.Split(string(raw), "\n") {
		if i == 0 || strings.TrimSpace(line) == "" {
			continue
		}
		out = append(out, strings.Split(line, "\t"))
	}
	return out
}

// atoiZGT parses a TSV integer field.
func atoiZGT(t *testing.T, s string) int {
	t.Helper()
	var v int
	if _, err := fmt.Sscanf(strings.TrimSpace(s), "%d", &v); err != nil {
		t.Fatalf("bad int %q: %v", s, err)
	}
	return v
}

// atofZGT parses a TSV float field.
func atofZGT(t *testing.T, s string) float32 {
	t.Helper()
	var v float64
	if _, err := fmt.Sscanf(strings.TrimSpace(s), "%g", &v); err != nil {
		t.Fatalf("bad float %q: %v", s, err)
	}
	return float32(v)
}

// zgLoopNearest is the closest approach of a patrol's closed loop to a point: the minimum 2-D distance and the z
// distance at that point. Waypoints cut corners, so this understates the loop's reach; the M1 sweep radius (150 yd
// against the 143-yd case) carries the margin.
func zgLoopNearest(pts [][3]float32, p dungeon.Point) (nearest, dz float64) {
	nearest, dz = math.Inf(1), 0
	for _, q := range pts {
		d := math.Hypot(float64(q[0]-p.X), float64(q[1]-p.Y))
		if d < nearest {
			nearest, dz = d, math.Abs(float64(q[2]-p.Z))
		}
	}
	return nearest, dz
}

// M1b (PLACE-W26 §3): the landing read is the net for BEHIND trash the quiet's floor window missed. The quiet is
// clean; the landing read (q units 45) lists a leg-3 servant at 36 yd, dz -13, whose home a BEHIND row holding its
// entry owns. Expect a clearto at its position before place, and the "landing read takes" note.
func TestPlaceLandingReadClearsBehindTrashBelowTheFloor(t *testing.T) {
	f, p := newPlaceFixture(t)
	rows, plan := placePlanRows(t)
	owner := placeRow94Circle(t, plan)
	home := [3]float32{owner.X, owner.Y, owner.Z}
	at := [3]float32{plan.Point.X + 36, plan.Point.Y, plan.Point.Z - 13}
	p.units = func(cmd string) ([]any, bool) {
		fl := strings.Fields(cmd)
		if len(fl) > 2 && fl[2] == "45" && fl[3] == num32(plan.Point.X) && fl[4] == num32(plan.Point.Y) {
			return []any{map[string]any{"g": "0xF130000000C152", "e": owner.Entries[0], "n": "Servant",
				"p": []float32{at[0], at[1], at[2]}, "dz": -13.0, "d": 36.0,
				"hpv": []float64{4000, 4000}, "rk": 1, "db": 51962,
				"home": []float32{home[0], home[1], home[2]}}}, false
		}
		return nil, false
	}
	f.agent.startReply = placeStartReply("zul_gurub", plan.Start, len(rows.Steps), plan.Stop)
	if code := f.run(context.Background()); code != exitOK {
		t.Fatalf("exit %d: %s", code, f.stderr.String())
	}
	want := fmt.Sprintf("clearto 309 %s %s %s 3 dz=8 all=1", num32(at[0]), num32(at[1]), num32(at[2]))
	var found bool
	for _, c := range p.cleartos {
		if strings.HasPrefix(c, want) {
			found = true
		}
	}
	if !found {
		t.Fatalf("the landing read did not clear the servant at its position: %v", p.cleartos)
	}
	if !hasNote(f.summary(), fmt.Sprintf("the landing read takes %d", owner.Entries[0])) {
		t.Fatalf("notes %v", f.summary().Notes)
	}
}

// M1b: the same unit with its home in an AHEAD row's circle is what a door run meets too: no clearto, place sent.
func TestPlaceLandingReadLeavesAheadBelowTheFloor(t *testing.T) {
	f, p := newPlaceFixture(t)
	rows, plan := placePlanRows(t)
	_, aheadHome := placeAheadB(t, plan)
	owner := placeRow94Circle(t, plan)
	// Near the landing (inside the read) but homed in an AHEAD row's circle, like a door run's own patrol.
	at := [3]float32{plan.Point.X + 36, plan.Point.Y, plan.Point.Z - 13}
	p.units = func(cmd string) ([]any, bool) {
		fl := strings.Fields(cmd)
		if len(fl) > 2 && fl[2] == "45" && fl[3] == num32(plan.Point.X) && fl[4] == num32(plan.Point.Y) {
			return []any{map[string]any{"g": "0xF130000000C153", "e": owner.Entries[0], "n": "Servant",
				"p": []float32{at[0], at[1], at[2]}, "dz": -13.0, "d": 36.0,
				"hpv": []float64{4000, 4000}, "rk": 1, "db": 51963,
				"home": []float32{aheadHome[0], aheadHome[1], aheadHome[2]}}}, false
		}
		return nil, false
	}
	f.agent.startReply = placeStartReply("zul_gurub", plan.Start, len(rows.Steps), plan.Stop)
	if code := f.run(context.Background()); code != exitOK {
		t.Fatalf("exit %d: %s", code, f.stderr.String())
	}
	if n := extraClearCount(p); n != 0 {
		t.Fatalf("the landing read cleared an AHEAD unit: %v", p.cleartos)
	}
	// The landing read leaves any other unit with no note (an AHEAD unit off the floor is what a door run meets
	// too): without the ownership check the extra clear notes the refusal.
	if hasNote(f.summary(), "db 51963") {
		t.Fatalf("the landing read noted the AHEAD unit: %v", f.summary().Notes)
	}
	if len(p.places) != 1 {
		t.Fatalf("the walk did not place: %v", p.places)
	}
}

// M1b: a BEHIND unit at dz -3 (inside the quiet's window) is the quiet's own judgement, not the landing read's:
// no clearto from the landing read.
func TestPlaceLandingReadSkipsUnitsTheQuietJudged(t *testing.T) {
	f, p := newPlaceFixture(t)
	rows, plan := placePlanRows(t)
	owner := placeRow94Circle(t, plan)
	home := [3]float32{owner.X, owner.Y, owner.Z}
	// 10 yd from the landing (inside the read), 3 yd below the floor (inside the quiet's window).
	at := [3]float32{plan.Point.X + 10, plan.Point.Y, plan.Point.Z - 3}
	var landingReads int
	p.units = func(cmd string) ([]any, bool) {
		fl := strings.Fields(cmd)
		if len(fl) > 2 && fl[2] == "3" { // the landing read's extra round fires: the quiet judged the unit too
			return []any{map[string]any{"g": "0xF130000000C154", "e": owner.Entries[0], "n": "Servant",
				"p": []float32{at[0], at[1], at[2]}, "dz": -3.0, "d": 1.0,
				"hpv": []float64{4000, 4000}, "rk": 1, "db": 51964,
				"home": []float32{home[0], home[1], home[2]}}}, false
		}
		if len(fl) > 2 && fl[2] == "45" && fl[3] == num32(plan.Point.X) && fl[4] == num32(plan.Point.Y) {
			landingReads++
			return []any{map[string]any{"g": "0xF130000000C154", "e": owner.Entries[0], "n": "Servant",
				"p": []float32{at[0], at[1], at[2]}, "dz": -3.0, "d": 36.0,
				"hpv": []float64{4000, 4000}, "rk": 1, "db": 51964,
				"home": []float32{home[0], home[1], home[2]}}}, false
		}
		return nil, false
	}
	f.agent.startReply = placeStartReply("zul_gurub", plan.Start, len(rows.Steps), plan.Stop)
	if code := f.run(context.Background()); code != exitOK {
		t.Fatalf("exit %d: %s", code, f.stderr.String())
	}
	if landingReads == 0 {
		t.Fatal("the landing read never ran")
	}
	if n := extraClearCount(p); n != 0 {
		t.Fatalf("the landing read cleared a unit the quiet judged: %v", p.cleartos)
	}
	if len(p.places) != 1 {
		t.Fatalf("the walk did not place: %v", p.places)
	}
}

// M2 (PLACE-W26 §3): when the frames show hires fighting after the place, the end reason names the enemy: entry,
// spawn guid low and position, from a hostile record targeting a hire.
func TestPlaceCombatCheckNamesTheEnemy(t *testing.T) {
	f, _ := newPlaceFixture(t)
	f.agent.startReply = zgPlaceReply(t)
	f.agent.combatAllFn = func(int) bool { return true }
	f.agent.hostilesFn = func(int) []tbm.HostileRec {
		return []tbm.HostileRec{{GUID: 0xF130002C5800CAFA, Entry: 11352, X: -12228.3, Y: -1680.7, Z: 126.7,
			HPPct: 90, Target: packet.GUID(1000)}}
	}
	code := f.run(context.Background())
	if code != exitRaidWalkFailed {
		t.Fatalf("exit %d, want 18: %s", code, f.stderr.String())
	}
	if reason := f.summary().EndReason; !strings.Contains(reason, "against 11352 (db 51962)") {
		t.Fatalf("end reason %q, want the enemy named", reason)
	}
}

// M2 (PLACE-W26 §3): placeView saves frames, so a placed walk that fails at the combat check leaves frames.jsonl
// non-empty (walk 26: 0 bytes, the summary could only guess).
func TestPlaceViewSavesFrames(t *testing.T) {
	f, _ := newPlaceFixture(t)
	f.agent.startReply = zgPlaceReply(t)
	f.agent.combatAllFn = func(int) bool { return true }
	f.agent.hostilesFn = func(int) []tbm.HostileRec {
		return []tbm.HostileRec{{GUID: 0xF130002C5800CAFA, Entry: 11352, X: -12228.3, Y: -1680.7, Z: 126.7,
			HPPct: 90, Target: packet.GUID(1000)}}
	}
	code := f.run(context.Background())
	if code != exitRaidWalkFailed {
		t.Fatalf("exit %d, want 18: %s", code, f.stderr.String())
	}
	raw, err := os.ReadFile(filepath.Join(f.out, "frames.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	if len(raw) == 0 {
		t.Fatal("frames.jsonl is empty after a placed walk that fought")
	}
}
