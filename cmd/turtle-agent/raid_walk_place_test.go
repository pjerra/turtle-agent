package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/pjerra/turtle-agent/internal/dungeon"
	"github.com/pjerra/turtle-agent/internal/objects"
	"github.com/pjerra/turtle-agent/internal/planexport"
	"github.com/pjerra/turtle-agent/internal/raidwalk"
	"github.com/pjerra/turtle-agent/internal/tbm"
)

// ttReplyOf is a one-reply toolkit answer out of a body object.
func ttReplyOf(t *testing.T, body map[string]any) ttResult {
	t.Helper()
	raw, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	return ttResult{Replies: []json.RawMessage{raw}}
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
	inst float64 // the instance `q inst` and `q group` report
	// cmdInst, when set, is what `q inst` reports instead (a commander in another copy).
	cmdInst float64
	// afterPlace, when set, makes `q inst` report movedInst once the place has happened.
	afterPlace bool
	movedInst  float64
	// units is the pre-read's answer for one `q units` command; nil: an empty list.
	units func(cmd string) (units []any, trunc bool)
	// quiet is the answer of the n-th quiet read (nil: always quiet).
	quiet func(n int) bool
	// quietNear is the `near` list a not-quiet read reports (nil: none): the extra clear round's input.
	quietNear []any
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
		return ttReplyOf(p.t, map[string]any{"map": 309, "read": 2, "final": true, "quiet": q, "near": near, "roamers": []any{}}), nil
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
	f.agent.startQueue = []queuedEvent{
		{raw: "TBM:RUN|zul_gurub|running|105/397|thekal|move|walk|walking"},
		{raw: "TBM:RUN|zul_gurub|running|106/397|thekal|move|walk|walking"},
		{raw: "TBM:RUN|zul_gurub|done|106/397|thekal|move|walk|stopped before step 124 (until boss): the party stays where it stands"},
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
	f.agent.startReply = placeStartReply("zul_gurub", step, 397, 124)
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
	if sum.Place.Start != step || sum.Place.Stop != 124 || !sum.Place.Quiet {
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
	f.agent.startReply = placeStartReply("zul_gurub", 105, 397, 124)
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
	f.agent.startReply = placeStartReply("zul_gurub", 105, 397, 124)
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
	f.agent.startReply = placeStartReply("zul_gurub", 105, 397, 124)
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
	f.agent.startReply = placeStartReply("zul_gurub", 105, 397, 124)
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
	f.agent.startReply = placeStartReply("zul_gurub", 105, 397, 124)
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
	f.agent.startReply = placeStartReply("zul_gurub", 230, 397, 124) // the server starts another step
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
	f.agent.startQueue = []queuedEvent{{raw: "TBM:RUN|zul_gurub|running|105/397|thekal|move|walk|walking"}}
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
	f.agent.startReply = placeStartReply("zul_gurub", 105, 397, 124)
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

func TestReviveInstanceCheckBeforeSummon(t *testing.T) {
	f, p := newPlaceFixture(t)
	f.opts.wipeResume = 0
	f.agent.startReply = placeStartReply("zul_gurub", 105, 397, 124)
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
	step, leg, err := raidwalk.PlaceStart(rows, []string{"from", "leg", "high_priest_thekal"})
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
		Circles: clear.Circles, Notes: clear.Notes, Ahead: clear.Ahead, Skips: raidwalk.PlaceSkips(plan)}
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
			p.quiet = func(int) bool { return false }
			p.quietNear = []any{map[string]any{"g": "0xF1300000000009", "e": owner.Entries[0], "n": "Roamer",
				"p": []float32{owner.X, owner.Y, owner.Z}, "w": 1.0, "d": 3.0}}
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
