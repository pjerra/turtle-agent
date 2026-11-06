package main

// The raid-walk --gm-instance-data test tool (replaces walk 39's hkgmall.sh):
// the commander sends `.debug instancedata <a> <b>` through the walk's own gm
// path (the commander's gm RPC, allow-listed), one event line per command in
// run.log, in order, 3 s apart (plus the 2 s reply wait), once per instance.
// A placed start inside the leg fires BEFORE the run starts; a mid-run reach
// fires under a run pause. One walk never refires a leg (the marker lives in
// its out dir); every new launch, a --reuse-instance restart included, sends.
import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/pjerra/turtle-agent/internal/dungeon"
	"github.com/pjerra/turtle-agent/internal/events"
	"github.com/pjerra/turtle-agent/internal/objects"
	"github.com/pjerra/turtle-agent/internal/planexport"
	"github.com/pjerra/turtle-agent/internal/raidwalk"
	"github.com/pjerra/turtle-agent/internal/tbm"
)

func instDataArgs(extra ...string) []string {
	return append([]string{"zul_gurub", "--place", "--from", "leg hakkar", "--sql", "walk.sql", "--gm-instance-data", "hakkar:1 3;6 3;0 0"}, extra...)
}

func TestParseGMInstanceDataFlags(t *testing.T) {
	var stderr bytes.Buffer
	o, code := parseRaidWalkArgs(instDataArgs("--gm-instance-data", "jeklik:0 0"), &stderr)
	if code != exitOK {
		t.Fatalf("exit %d: %s", code, stderr.String())
	}
	if len(o.gmInstanceData) != 2 || o.gmInstanceData[0] != "hakkar:1 3;6 3;0 0" || o.gmInstanceData[1] != "jeklik:0 0" {
		t.Fatalf("options %+v", o.gmInstanceData)
	}
	// Repeatable with = and across flags; off by default.
	o, code = parseRaidWalkArgs([]string{"zul_gurub", "--place", "--from", "leg hakkar", "--sql", "walk.sql", "--gm-instance-data=hog:2 5"}, &stderr)
	if code != exitOK || len(o.gmInstanceData) != 1 || o.gmInstanceData[0] != "hog:2 5" {
		t.Fatalf("options %+v exit %d", o.gmInstanceData, code)
	}
	o, code = parseRaidWalkArgs([]string{"zul_gurub", "--place", "--from", "leg hakkar", "--sql", "walk.sql"}, &stderr)
	if code != exitOK || len(o.gmInstanceData) != 0 {
		t.Fatalf("default options %+v exit %d", o.gmInstanceData, code)
	}
}

func TestParseGMInstanceDataRefusals(t *testing.T) {
	for _, tc := range []struct {
		name string
		args []string
		want string
	}{
		{"production server", []string{"zul_gurub", "--place", "--from", "leg hakkar", "--sql", "walk.sql", "--server", "tortoise", "--gm-instance-data", "hakkar:1 3"}, "test servers only"},
		{"unplaced flag parses", []string{"zul_gurub", "--from", "leg hakkar", "--sql", "walk.sql", "--gm-instance-data", "hakkar:1 3"}, ""},
		{"no leg", []string{"zul_gurub", "--place", "--from", "leg hakkar", "--sql", "walk.sql", "--gm-instance-data", "1 3"}, "want \"<leg>:"},
		{"bad cmd", []string{"zul_gurub", "--place", "--from", "leg hakkar", "--sql", "walk.sql", "--gm-instance-data", "hakkar:1"}, "not two integers"},
		{"extra word", []string{"zul_gurub", "--place", "--from", "leg hakkar", "--sql", "walk.sql", "--gm-instance-data", "hakkar:1 2 3"}, "not two integers"},
	} {
		var stderr bytes.Buffer
		_, code := parseRaidWalkArgs(tc.args, &stderr)
		if tc.want == "" {
			if code != exitOK {
				t.Errorf("%s: exit %d, want %d (stderr %q)", tc.name, code, exitOK, stderr.String())
			}
			continue
		}
		if code != exitUsage || !strings.Contains(stderr.String(), tc.want) {
			t.Errorf("%s: exit %d, stderr %q (want %q)", tc.name, code, stderr.String(), tc.want)
		}
	}
	// The leg check needs the rows: unknown legs pass the shape check in
	// parseRaidWalkArgs and fail in validate (still exit 2). Pin that the
	// shape check alone accepts them.
	var stderr bytes.Buffer
	if _, code := parseRaidWalkArgs([]string{"zul_gurub", "--place", "--from", "leg high_priest_thekal", "--sql", "walk.sql", "--gm-instance-data", "high_priest_thekal:1 3"}, &stderr); code != exitOK {
		t.Fatalf("thekal shape: exit %d, stderr %q", code, stderr.String())
	}
}

// The placed walk whose start stands in the named leg fires the lines right
// after placement, BEFORE the run starts: through w.gm (the allow-listed
// commander gm RPC), in order, 3 s apart (plus the 2 s reply wait) on the
// fake clock, one event line per command in run.log.
// instDataStartReply is the run start's answer with the plan's full leg
// name in the status lines, as the live server sends
// (walk39 run.log: TBM:RUN|zul_gurub|running|215/733|high_priest_thekal).
// The fixture's own replies shorten it to thekal.
func instDataStartReply(t *testing.T, n zgPlaceNums) *commanderReply {
	t.Helper()
	lines := []string{
		fmt.Sprintf("TBM:RUN|zul_gurub|running|%d/%d|high_priest_thekal|move|walk|walking|until boss (step %d)", n.start, n.total, n.until),
		fmt.Sprintf("The run starts at step %d of %d (leg high_priest_thekal)", n.start, n.total),
		"TBM:ACTION_ACK|run start|party|1|-",
	}
	return &commanderReply{Reply: tbm.Reply{Kind: tbm.KindAck, OK: true,
		Ack:  &tbm.Ack{Intent: "run start", Scope: "party", Count: 1, Executor: "-"},
		Line: lines[0], Lines: lines}}
}

func TestGMInstanceDataFiresAfterPlacement(t *testing.T) {
	f, _ := newPlaceFixture(t)
	// The fixture places at the thekal leg; retarget the walk at the hakkar
	// leg is plan surgery. Drive thekal instead: the flag names the placed leg.
	f.opts.gmInstanceData = stringList{"high_priest_thekal:1 3;6 3;0 0"}
	n := zgPlaceSteps(t)
	f.agent.startReply = instDataStartReply(t, n)
	f.agent.unitsFn = func(int) []objects.Unit { return []objects.Unit{} }
	old := gmInstanceDataGap
	defer func() { gmInstanceDataGap = old }()
	if code := f.run(context.Background()); code != exitOK {
		t.Fatalf("exit %d: %s\n%s", code, f.stderr.String(), f.stdout.String())
	}
	want := []string{".debug instancedata 1 3", ".debug instancedata 6 3", ".debug instancedata 0 0"}
	var got []string
	for _, l := range f.agent.gmLines() {
		if strings.HasPrefix(l, ".debug instancedata") {
			got = append(got, l)
		}
	}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Fatalf("instancedata lines %q, want %q (all gm: %q)", got, want, f.agent.gmLines())
	}
	at := instDataTimes(f, want)
	if len(at) != 3 {
		t.Fatalf("times %v for %q", at, want)
	}
	for i := 1; i < len(at); i++ {
		gap := time.Duration(at[i] - at[i-1])
		if gap != old+gmInstanceDataReplyWait {
			t.Fatalf("gap %d is %v, want the %v gap + the %v reply wait: %v", i, gap, old, gmInstanceDataReplyWait, at)
		}
	}
	by, err := os.ReadFile(filepath.Join(f.out, "run.log"))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"gm-instance-data: high_priest_thekal: .debug instancedata 1 3 -> sent",
		"gm-instance-data: high_priest_thekal: .debug instancedata 6 3 -> sent",
		"gm-instance-data: high_priest_thekal: .debug instancedata 0 0 -> sent",
	} {
		if !strings.Contains(string(by), want) {
			t.Fatalf("run.log lacks %q:\n%s", want, by)
		}
	}
}

// A --reuse-instance restart in the same instance DOES resend. Walk 44 (raid
// hk7m attempt -3) showed the instance data does not survive a wipe: the
// restart's run.log had no gm-instance-data lines and Hakkar cast Aspect of
// Jeklik/Venoxis again. Each launch runs in a fresh out dir, so its own marker
// is empty, and nothing else (no ledger) may suppress the lines. The reuse
// fixture's kept instance is the place toolkit's answer (inst 103).
func TestGMInstanceDataResendsAfterReuseRestart(t *testing.T) {
	ledger := filepath.Join(t.TempDir(), "instance-cap.json")
	n := zgPlaceSteps(t)
	run := func() *fixture {
		f, _ := newPlaceFixture(t)
		f.opts.gmInstanceData = stringList{"high_priest_thekal:1 3"}
		f.opts.reuseInstance = true
		f.env.capLedger = ledger
		f.agent.entered = true
		f.agent.startReply = instDataStartReply(t, n)
		f.agent.unitsFn = func(int) []objects.Unit { return []objects.Unit{} }
		if code := f.run(context.Background()); code != exitOK {
			t.Fatalf("exit %d: %s\n%s", code, f.stderr.String(), f.stdout.String())
		}
		return f
	}
	first := run()
	if n := countPrefix(first.agent.gmLines(), ".debug instancedata"); n != 1 {
		t.Fatalf("first walk: %d lines, want 1: %q", n, first.agent.gmLines())
	}
	second := run()
	if n := countPrefix(second.agent.gmLines(), ".debug instancedata"); n != 1 {
		t.Fatalf("same-instance --reuse-instance restart sent %d lines, want 1: %q", n, second.agent.gmLines())
	}
	by, _ := os.ReadFile(filepath.Join(second.out, "run.log"))
	if !strings.Contains(string(by), "gm-instance-data: high_priest_thekal: .debug instancedata 1 3 -> sent") {
		t.Fatalf("restart run.log lacks the instance-data line:\n%s", by)
	}
	// A third restart resends too.
	third := run()
	if n := countPrefix(third.agent.gmLines(), ".debug instancedata"); n != 1 {
		t.Fatalf("third launch sent %d lines, want 1: %q", n, third.agent.gmLines())
	}
}

// A fresh walk (no --reuse-instance) in the same instance id sends too.
func TestGMInstanceDataFreshWalkSends(t *testing.T) {
	ledger := filepath.Join(t.TempDir(), "instance-cap.json")
	n := zgPlaceSteps(t)
	f, _ := newPlaceFixture(t)
	f.opts.gmInstanceData = stringList{"high_priest_thekal:1 3"}
	f.opts.reuseInstance = true
	f.env.capLedger = ledger
	f.agent.entered = true
	f.agent.startReply = instDataStartReply(t, n)
	f.agent.unitsFn = func(int) []objects.Unit { return []objects.Unit{} }
	if code := f.run(context.Background()); code != exitOK {
		t.Fatalf("exit %d: %s\n%s", code, f.stderr.String(), f.stdout.String())
	}
	if n := countPrefix(f.agent.gmLines(), ".debug instancedata"); n != 1 {
		t.Fatalf("setup walk sent %d, want 1: %q", n, f.agent.gmLines())
	}
	// A fresh walk (no --reuse-instance) in the same instance id sends
	// again.
	g, _ := newPlaceFixture(t)
	g.opts.gmInstanceData = stringList{"high_priest_thekal:1 3"}
	g.env.capLedger = ledger
	g.agent.entered = true
	g.agent.startReply = instDataStartReply(t, n)
	g.agent.unitsFn = func(int) []objects.Unit { return []objects.Unit{} }
	if code := g.run(context.Background()); code != exitOK {
		t.Fatalf("exit %d: %s\n%s", code, g.stderr.String(), g.stdout.String())
	}
	if n := countPrefix(g.agent.gmLines(), ".debug instancedata"); n != 1 {
		t.Fatalf("fresh walk with an old mark sent %d lines, want 1: %q", n, g.agent.gmLines())
	}
}

// A --reuse-instance walk that falls back to a fresh instance sends the
// lines again: the new copy is not the one the data was confirmed for, even
// when the core hands out the same instance id (it reuses ids across
// copies). The first walk fires in 103 (alive reuse); the second finds the raid gone and no bind, enters fresh, and the
// toolkit reports 103 again for the new copy.
func TestGMInstanceDataRefiresAfterReuseFreshFallback(t *testing.T) {
	ledger := filepath.Join(t.TempDir(), "instance-cap.json")
	n := zgPlaceSteps(t)
	first, _ := newPlaceFixture(t)
	first.opts.gmInstanceData = stringList{"high_priest_thekal:1 3"}
	first.opts.reuseInstance = true
	first.env.capLedger = ledger
	first.agent.entered = true
	first.agent.startReply = instDataStartReply(t, n)
	first.agent.unitsFn = func(int) []objects.Unit { return []objects.Unit{} }
	if code := first.run(context.Background()); code != exitOK {
		t.Fatalf("first walk exit %d: %s\n%s", code, first.stderr.String(), first.stdout.String())
	}
	if n := countPrefix(first.agent.gmLines(), ".debug instancedata"); n != 1 {
		t.Fatalf("first walk sent %d lines, want 1: %q", n, first.agent.gmLines())
	}
	// The raid is gone and no bind is left: the walk falls back to a fresh
	// entry. The core reuses the id, so the new copy is 103 again.
	second, _ := newPlaceFixture(t)
	second.opts.gmInstanceData = stringList{"high_priest_thekal:1 3"}
	second.opts.reuseInstance = true
	second.env.capLedger = ledger
	second.agent.entered = false
	second.agent.startReply = instDataStartReply(t, n)
	second.agent.unitsFn = func(int) []objects.Unit { return []objects.Unit{} }
	if code := second.run(context.Background()); code != exitOK {
		t.Fatalf("second walk exit %d: %s\n%s", code, second.stderr.String(), second.stdout.String())
	}
	if n := countPrefix(second.agent.gmLines(), ".debug instancedata"); n != 1 {
		t.Fatalf("fresh fallback sent %d lines, want the new copy's one: %q", n, second.agent.gmLines())
	}
}

// One walk fires its leg once even when the run passes the leg twice, and a
// leg that already fired in the same instance (marker in the out dir) does
// not fire again on a --reuse-instance restart.
func TestGMInstanceDataSentOncePerInstance(t *testing.T) {
	n := zgPlaceSteps(t)
	f, _ := newPlaceFixture(t)
	f.opts.gmInstanceData = stringList{"high_priest_thekal:1 3"}
	f.agent.startReply = instDataStartReply(t, n)
	f.agent.unitsFn = func(int) []objects.Unit { return []objects.Unit{} }
	if code := f.run(context.Background()); code != exitOK {
		t.Fatalf("exit %d: %s\n%s", code, f.stderr.String(), f.stdout.String())
	}
	// The run's own second line names the same leg: no second firing.
	count := 0
	for _, l := range f.agent.gmLines() {
		if strings.HasPrefix(l, ".debug instancedata") {
			count++
		}
	}
	if count != 1 {
		t.Fatalf("%d instancedata lines in one walk, want 1: %q", count, f.agent.gmLines())
	}
	entries, err := os.ReadDir(f.out)
	if err != nil {
		t.Fatal(err)
	}
	var markers []string
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), "gm-instance-data-") {
			markers = append(markers, e.Name())
		}
	}
	if len(markers) != 1 {
		t.Fatalf("markers %q in %s, want one", markers, f.out)
	}
}

// The sender is the walk's own gm path: the allow-list accepts exactly the
// lines the walk sends, so a gm RPC failure breaks the walk loudly instead
// of losing the pull gate silently.
func TestGMInstanceDataUsesTheWalksGMPath(t *testing.T) {
	for _, line := range []string{".debug instancedata 1 3", ".debug instancedata 6 3", ".debug instancedata 0 0"} {
		if !raidwalk.AllowedLine(line, map[string]bool{"Tankbot": true}, 309) {
			t.Fatalf("the allow-list refuses the walk's own line %q", line)
		}
	}
	for _, line := range []string{".debug instancedata 1", ".debug instancedata 1 2 3", ".debug foo 1 3", ".debug instancedata a b", ".debug instancedata 1 3 extra"} {
		if raidwalk.AllowedLine(line, map[string]bool{"Tankbot": true}, 309) {
			t.Fatalf("the allow-list accepts a line the walk never sends %q", line)
		}
	}
}

// Without the flag nothing goes out and no marker file appears.
func TestGMInstanceDataOffByDefault(t *testing.T) {
	f, _ := newPlaceFixture(t)
	f.agent.startReply = zgPlaceReply(t)
	f.agent.unitsFn = func(int) []objects.Unit { return []objects.Unit{} }
	if code := f.run(context.Background()); code != exitOK {
		t.Fatalf("exit %d: %s", code, f.stderr.String())
	}
	for _, l := range f.agent.gmLines() {
		if strings.HasPrefix(l, ".debug instancedata") {
			t.Fatalf("a line went out without the flag: %q", l)
		}
	}
	entries, err := os.ReadDir(f.out)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), "gm-instance-data-") {
			t.Fatalf("a marker file without the flag: %s", e.Name())
		}
	}
}

// Unknown leg names are refused at validate (exit 2), like --gm-clear's
// unknown groups. The shape check at parse time cannot know the plan's legs.
func TestGMInstanceDataUnknownLegRefused(t *testing.T) {
	f, _ := newPlaceFixture(t)
	f.opts.gmInstanceData = stringList{"nosuchleg:1 3"}
	f.agent.startReply = zgPlaceReply(t)
	if code := f.run(context.Background()); code != exitUsage {
		t.Fatalf("exit %d, want %d: %s", code, exitUsage, f.stderr.String())
	}
	if strings.Contains(f.stdout.String(), "nosuchleg") {
		t.Fatalf("a refusal printed before the out dir: %q", f.stdout.String())
	}
}

// An interrupt after the FIRST command resends the rest. The sleep between
// the lines fails, the walk ends, and a fresh out dir with --reuse-instance
// sends the whole list again (the run's leg
// line-up is the reuse fixture's kept instance 103).
func TestGMInstanceDataInterruptResendsRest(t *testing.T) {
	ledger := filepath.Join(t.TempDir(), "instance-cap.json")
	n := zgPlaceSteps(t)
	old := gmInstanceDataGap
	gmInstanceDataGap = time.Nanosecond
	defer func() { gmInstanceDataGap = old }()
	interrupted := errors.New("interrupted for the firing test")
	first, _ := newPlaceFixture(t)
	first.opts.gmInstanceData = stringList{"high_priest_thekal:1 3;6 3"}
	first.opts.reuseInstance = true
	first.env.capLedger = ledger
	first.agent.entered = true
	first.agent.startReply = instDataStartReply(t, n)
	first.agent.unitsFn = func(int) []objects.Unit { return []objects.Unit{} }
	first.env.sleep = func(ctx context.Context, d time.Duration) error {
		if d == gmInstanceDataGap {
			return interrupted
		}
		return first.clock.sleep(ctx, d)
	}
	if code := first.run(context.Background()); code != exitRaidWalkFailed {
		t.Fatalf("exit %d, want %d (the gap sleep fails the walk): %s", code, exitRaidWalkFailed, first.stderr.String())
	}
	if n := countPrefix(first.agent.gmLines(), ".debug instancedata"); n != 1 {
		t.Fatalf("interrupted walk sent %d lines, want exactly the first: %q", n, first.agent.gmLines())
	}
	second, _ := newPlaceFixture(t)
	second.opts.gmInstanceData = stringList{"high_priest_thekal:1 3;6 3"}
	second.opts.reuseInstance = true
	second.env.capLedger = ledger
	second.agent.entered = true
	second.agent.startReply = instDataStartReply(t, n)
	second.agent.unitsFn = func(int) []objects.Unit { return []objects.Unit{} }
	if code := second.run(context.Background()); code != exitOK {
		t.Fatalf("exit %d, want 0: %s\n%s", code, second.stderr.String(), second.stdout.String())
	}
	var got []string
	for _, l := range second.agent.gmLines() {
		if strings.HasPrefix(l, ".debug instancedata") {
			got = append(got, l)
		}
	}
	// A fresh out dir has no marker: the whole list goes out again (the
	// first walk confirmed `1 3` in its own out dir only).
	if strings.Join(got, "|") != ".debug instancedata 1 3|.debug instancedata 6 3" {
		t.Fatalf("resumed walk sent %q, want the whole list again", got)
	}
}

// A placed start inside the leg sends the lines BEFORE the run starts, in
// every --until mode including --until none (review item 3): the lines
// precede `run start` in the call order, and the until-none walk carries
// no until clause.
func TestGMInstanceDataPlacedStartSendsBeforeRunStart(t *testing.T) {
	for _, until := range []string{"boss", "none"} {
		f, _ := newPlaceFixture(t)
		f.opts.until = until
		f.opts.gmInstanceData = stringList{"high_priest_thekal:1 3"}
		n := zgPlaceSteps(t)
		f.agent.startReply = instDataStartReply(t, n)
		f.agent.unitsFn = func(int) []objects.Unit { return []objects.Unit{} }
		if code := f.run(context.Background()); code != exitOK {
			t.Fatalf("until %s: exit %d: %s\n%s", until, code, f.stderr.String(), f.stdout.String())
		}
		log := f.agentLog()
		inst, run := -1, -1
		for i, line := range log {
			if strings.HasPrefix(line, "gm .debug instancedata") && inst < 0 {
				inst = i
			}
			if strings.HasPrefix(line, "cmdr run start ") && run < 0 {
				run = i
			}
		}
		if inst < 0 {
			t.Fatalf("until %s: no instance-data line went out: %q", until, log)
		}
		if run < 0 {
			t.Fatalf("until %s: no run start went out: %q", until, log)
		}
		if inst > run {
			t.Fatalf("until %s: the line went out after the run start (gm %d, run %d): %q", until, inst, run, log)
		}
		if until == "none" && strings.Contains(f.agent.startArgs, "until") {
			t.Fatalf("until none: the run start carries an until clause: %q", f.agent.startArgs)
		}
	}
}

// A walk that reaches the leg mid-run holds the next pull until the lines
// are sent (review item 3): the run is paused first, resumed after the
// last line. The fixture places at the thekal leg while the flag names the
// NEXT leg (arlokk), which no pre-start firing touches; the run's own next
// line names arlokk mid-run and fires it there with the hold. (Jeklik-path
// note: the walk stays placed at thekal — the mid line only names the leg,
// it does not move the raid.)
func TestGMInstanceDataMidRunHoldsNextPull(t *testing.T) {
	n := zgPlaceSteps(t)
	f, _ := newPlaceFixture(t)
	f.opts.gmInstanceData = stringList{"high_priestess_arlokk:1 3"}
	f.agent.startReply = instDataStartReply(t, n)
	f.agent.unitsFn = func(int) []objects.Unit { return []objects.Unit{} }
	mid := fmt.Sprintf("TBM:RUN|zul_gurub|running|%d/%d|high_priestess_arlokk|move|rest|resting|until boss (step %d)", n.start+1, n.total, n.until)
	f.agent.mu.Lock()
	last := f.agent.startQueue[len(f.agent.startQueue)-1]
	f.agent.startQueue = append(f.agent.startQueue[:len(f.agent.startQueue)-1], queuedEvent{raw: mid}, last)
	f.agent.mu.Unlock()
	if code := f.run(context.Background()); code != exitOK {
		t.Fatalf("exit %d: %s\n%s", code, f.stderr.String(), f.stdout.String())
	}
	f.agent.mu.Lock()
	verbs := append([]string{}, f.agent.runVerbs...)
	f.agent.mu.Unlock()
	pause, resume := -1, -1
	for i, v := range verbs {
		if v == "pause" && pause < 0 {
			pause = i
		}
		if v == "resume" && resume < 0 {
			resume = i
		}
	}
	if pause < 0 || resume < 0 || resume < pause {
		t.Fatalf("pause %d resume %d in run verbs %q: the mid-run firing must pause and resume", pause, resume, verbs)
	}
	if n := countPrefix(f.agent.gmLines(), ".debug instancedata"); n != 1 {
		t.Fatalf("%d lines, want the mid-run firing's one: %q", n, f.agent.gmLines())
	}
}

// After each line the walk logs the server's reply in the event line
// (`-> <reply>`, or `-> sent` when the window passes silently: the core's
// debug handler prints nothing on success, so silence is the success path).
// A refusal line (`There is no such command`, `Incorrect syntax.`, ...) is
// logged `-> <refusal> (REFUSED)`, NOT recorded, and retried on the next
// status; neither ever fails the walk.
func TestGMInstanceDataLogsServerReply(t *testing.T) {
	n := zgPlaceSteps(t)
	t.Run("unrelated line confirms", func(t *testing.T) {
		f, _ := newPlaceFixture(t)
		f.opts.gmInstanceData = stringList{"high_priest_thekal:1 3"}
		f.agent.startReply = instDataStartReply(t, n)
		f.agent.unitsFn = func(int) []objects.Unit { return []objects.Unit{} }
		// A non-refusal system line on the wire is the command's confirmation.
		f.agent.onGM = func(line string) {
			if strings.HasPrefix(line, ".debug instancedata") {
				f.agent.push("Some unrelated system chatter", "")
			}
		}
		if code := f.run(context.Background()); code != exitOK {
			t.Fatalf("exit %d: %s\n%s", code, f.stderr.String(), f.stdout.String())
		}
		by, _ := os.ReadFile(filepath.Join(f.out, "run.log"))
		if !strings.Contains(string(by), "gm-instance-data: high_priest_thekal: .debug instancedata 1 3 -> Some unrelated system chatter") {
			t.Fatalf("run.log lacks the reply line:\n%s", by)
		}
	})
	t.Run("silence confirms", func(t *testing.T) {
		f, _ := newPlaceFixture(t)
		f.opts.gmInstanceData = stringList{"high_priest_thekal:1 3"}
		f.agent.startReply = instDataStartReply(t, n)
		f.agent.unitsFn = func(int) []objects.Unit { return []objects.Unit{} }
		if code := f.run(context.Background()); code != exitOK {
			t.Fatalf("exit %d: %s\n%s", code, f.stderr.String(), f.stdout.String())
		}
		by, _ := os.ReadFile(filepath.Join(f.out, "run.log"))
		if !strings.Contains(string(by), "gm-instance-data: high_priest_thekal: .debug instancedata 1 3 -> sent") {
			t.Fatalf("run.log lacks the silence-confirms line:\n%s", by)
		}
	})
}

// instDataOnyxiaReply is a run start answer for the unplaced onyxia
// fixture: the run's own leg name is "onyxia" (defaultRun's lines).
// instDataOnyxiaRestQueue replaces the fixture's default statuses by two
// walking statuses, an out-of-combat rest and the end: the mid-run sends only
// fire at the rest.
func instDataOnyxiaRestQueue(f *fixture, plan string) {
	st := func(state string, step int, phase, note string) queuedEvent {
		return queuedEvent{raw: fmt.Sprintf("TBM:RUN|%s|%s|%d/29|onyxia|move|%s|%s", plan, state, step, phase, note)}
	}
	f.agent.mu.Lock()
	f.agent.startQueue = []queuedEvent{
		st("running", 3, "walk", "walking"),
		st("running", 4, "walk", "walking"),
		st("running", 5, "rest", "resting"),
		st("running", 6, "walk", "walking"),
		st("done", 6, "walk", "stopped before step 29 (until boss): the party stays where it stands"),
	}
	f.agent.mu.Unlock()
}

func instDataOnyxiaReply() *commanderReply {
	line := "TBM:RUN|onyxias_lair|running|2/29|onyxia|move|walk|walking|until boss (step 29)"
	return &commanderReply{Reply: tbm.Reply{Kind: tbm.KindAck, OK: true,
		Ack:  &tbm.Ack{Intent: "run start", Scope: "party", Count: 1, Executor: "-"},
		Line: line, Lines: []string{line, "TBM:ACTION_ACK|run start|party|1|-"}}}
}

// Without --place the flag still fires: the instance id is read the way
// the cap ledger reads it at entry (review item 5). The unplaced onyxia
// fixture answers `q inst` through its tt fake; the run's own leg (onyxia)
// fires once per entry instance. When the read fails, the walk WARNs once
// and falls back to the per-out-dir marker only.
func TestGMInstanceDataWithoutPlace(t *testing.T) {
	t.Run("entry instance", func(t *testing.T) {
		f := newFixture(t)
		f.opts.gmInstanceData = stringList{"onyxia:1 3"}
		f.agent.ttFn = func(cmd string) (ttResult, error) {
			if cmd == "q inst" {
				return ttReplyOf(t, map[string]any{"map": 249, "inst": 77}), nil
			}
			return ttResult{}, nil
		}
		f.agent.startReply = instDataOnyxiaReply()
		f.defaultRun("onyxias_lair")
		instDataOnyxiaRestQueue(f, "onyxias_lair")
		if code := f.run(context.Background()); code != exitOK {
			t.Fatalf("exit %d: %s\n%s", code, f.stderr.String(), f.stdout.String())
		}
		if n := countPrefix(f.agent.gmLines(), ".debug instancedata"); n != 1 {
			t.Fatalf("%d lines, want the entry instance's one: %q", n, f.agent.gmLines())
		}
	})
	t.Run("unreadable falls back", func(t *testing.T) {
		f := newFixture(t)
		f.opts.gmInstanceData = stringList{"onyxia:1 3"}
		f.agent.ttFn = func(cmd string) (ttResult, error) {
			return ttResult{}, errors.New("toolkit down")
		}
		f.agent.startReply = instDataOnyxiaReply()
		f.defaultRun("onyxias_lair")
		instDataOnyxiaRestQueue(f, "onyxias_lair")
		if code := f.run(context.Background()); code != exitOK {
			t.Fatalf("exit %d: %s\n%s", code, f.stderr.String(), f.stdout.String())
		}
		if n := countPrefix(f.agent.gmLines(), ".debug instancedata"); n != 1 {
			t.Fatalf("%d lines, want the marker-only one: %q", n, f.agent.gmLines())
		}
		if !strings.Contains(strings.Join(f.summary().Notes, "\n"), "WARN") {
			t.Fatalf("notes %q, want the WARN", f.summary().Notes)
		}
	})
}

// instDataTimes is when each want line went out through the fake's gm RPC.
func instDataTimes(f *fixture, want []string) []int64 {
	f.agent.mu.Lock()
	defer f.agent.mu.Unlock()
	var out []int64
	j := 0
	for i, l := range f.agent.gm {
		if j < len(want) && l == want[j] {
			out = append(out, f.agent.gmAt[i].UnixNano())
			j++
		}
	}
	return out
}

// Item 2: a refused command is NOT recorded, and after 3 unconfirmed tries
// the walk logs `NOT confirmed after 3 tries` and continues (never failing).
// Driven at the fire level: each pass sends the command, the fake answers
// `Incorrect syntax.` (the core's ExecuteCommand failure text,
// Commands.cpp:1679 LANG_CMD_SYNTAX), and nothing is recorded until the
// third pass logs the final line.
func TestGMInstanceDataRefusalRetriesThenGivesUp(t *testing.T) {
	f := newFixture(t)
	if err := os.MkdirAll(f.out, 0o700); err != nil {
		t.Fatal(err)
	}
	logF, err := os.Create(filepath.Join(f.out, "run.log"))
	if err != nil {
		t.Fatal(err)
	}
	defer logF.Close()
	f.agent.onGM = func(line string) {
		if strings.HasPrefix(line, ".debug instancedata") {
			f.agent.push("Incorrect syntax.", "")
		}
	}
	w := &walk{plan: zgPlan(t), agent: f.agent, outDir: f.out, logF: logF,
		env: walkEnv{now: f.clock.now, sleep: f.clock.sleep}}
	spec := raidwalk.GMInstanceSpec{Leg: "hakkar", Cmds: [][2]int{{1, 3}}}
	ctx := context.Background()
	for try := 1; try <= 3; try++ {
		if err := w.gmInstanceDataFire(ctx, "77", spec); err != nil {
			t.Fatalf("try %d: %v", try, err)
		}
	}
	if n := countPrefix(f.agent.gmLines(), ".debug instancedata"); n != 3 {
		t.Fatalf("%d sends, want 3 tries: %q", n, f.agent.gmLines())
	}
	by, _ := os.ReadFile(filepath.Join(f.out, "run.log"))
	if !strings.Contains(string(by), "gm-instance-data: hakkar: .debug instancedata 1 3 NOT confirmed after 3 tries") {
		t.Fatalf("run.log lacks the NOT-confirmed line:\n%s", by)
	}
	if !strings.Contains(string(by), "(REFUSED)") {
		t.Fatalf("run.log lacks the REFUSED mark:\n%s", by)
	}
	entries, err := os.ReadDir(f.out)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), "gm-instance-data-") {
			t.Fatalf("a refused command was recorded in %s", e.Name())
		}
	}
	// A fourth pass sends nothing more for the command (tried out), and the
	// walk still does not fail.
	if err := w.gmInstanceDataFire(ctx, "77", spec); err != nil {
		t.Fatalf("fourth pass: %v", err)
	}
	if n := countPrefix(f.agent.gmLines(), ".debug instancedata"); n != 3 {
		t.Fatalf("%d sends after the fourth pass, want still 3: %q", n, f.agent.gmLines())
	}
}

// Item 2: a confirmed command IS recorded: the marker holds it and the run
// continues. (Silence is the success path; covered by FiresAfterPlacement.)
// Here an explicit non-refusal line confirms.
func TestGMInstanceDataConfirmedIsRecorded(t *testing.T) {
	n := zgPlaceSteps(t)
	f, _ := newPlaceFixture(t)
	f.opts.gmInstanceData = stringList{"high_priest_thekal:1 3"}
	f.agent.startReply = instDataStartReply(t, n)
	f.agent.unitsFn = func(int) []objects.Unit { return []objects.Unit{} }
	f.agent.onGM = func(line string) {
		if strings.HasPrefix(line, ".debug instancedata") {
			f.agent.push("command ok", "")
		}
	}
	if code := f.run(context.Background()); code != exitOK {
		t.Fatalf("exit %d: %s\n%s", code, f.stderr.String(), f.stdout.String())
	}
	entries, err := os.ReadDir(f.out)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, e := range entries {
		if !strings.HasPrefix(e.Name(), "gm-instance-data-") {
			continue
		}
		found = true
		raw, err := os.ReadFile(filepath.Join(f.out, e.Name()))
		if err != nil {
			t.Fatal(err)
		}
		if strings.TrimSpace(string(raw)) != ".debug instancedata 1 3" {
			t.Fatalf("marker %q holds %q", e.Name(), raw)
		}
	}
	if !found {
		t.Fatal("no marker file for a confirmed command")
	}
}

// Item 3: when the status was running before the pause attempt, the walk
// always sends resume afterwards — even when the pause call errored.
func TestGMInstanceDataResumesAfterFailedPause(t *testing.T) {
	n := zgPlaceSteps(t)
	f, _ := newPlaceFixture(t)
	f.opts.gmInstanceData = stringList{"high_priestess_arlokk:1 3"}
	f.agent.startReply = instDataStartReply(t, n)
	f.agent.unitsFn = func(int) []objects.Unit { return []objects.Unit{} }
	mid := fmt.Sprintf("TBM:RUN|zul_gurub|running|%d/%d|high_priestess_arlokk|move|rest|resting|until boss (step %d)", n.start+1, n.total, n.until)
	f.agent.mu.Lock()
	last := f.agent.startQueue[len(f.agent.startQueue)-1]
	f.agent.startQueue = append(f.agent.startQueue[:len(f.agent.startQueue)-1], queuedEvent{raw: mid}, last)
	pauseFails := true
	f.agent.pauseErr = &pauseFails
	f.agent.mu.Unlock()
	if code := f.run(context.Background()); code != exitOK {
		t.Fatalf("exit %d: %s\n%s", code, f.stderr.String(), f.stdout.String())
	}
	f.agent.mu.Lock()
	verbs := append([]string{}, f.agent.runVerbs...)
	f.agent.mu.Unlock()
	pause, resume := -1, -1
	for i, v := range verbs {
		if v == "pause" && pause < 0 {
			pause = i
		}
		if v == "resume" && resume < 0 {
			resume = i
		}
	}
	if pause < 0 || resume < 0 || resume < pause {
		t.Fatalf("pause %d resume %d in %q: resume must follow even a failed pause", pause, resume, verbs)
	}
	if n := countPrefix(f.agent.gmLines(), ".debug instancedata"); n != 1 {
		t.Fatalf("%d lines, want the mid-run firing's one: %q", n, f.agent.gmLines())
	}
}

// Item 4: after a firing, pollOnce rewinds the cursor to the firing line
// (the way waitTBMMatch rewinds to its matched line) and the next poll
// re-reads the unfired tail in order. The reply reader only advances the
// cursor and scans for the refusal — it never feeds or adopts — so no
// status after the firing ever processes past an adopted newer one. The
// page holds [mid, X] while Y and done wait behind it: the old reader
// adopted Y and done during the firing and the loop then adopted the older
// X past them; now X, Y and done adopt in order. Run.log's running steps
// never decrease after the firing.
func TestGMInstanceDataFiringBreaksPollLoop(t *testing.T) {
	n := zgPlaceSteps(t)
	f, _ := newPlaceFixture(t)
	f.opts.gmInstanceData = stringList{"high_priestess_arlokk:1 3"}
	f.agent.startReply = instDataStartReply(t, n)
	f.agent.unitsFn = func(int) []objects.Unit { return []objects.Unit{} }
	f.agent.pageSize = 2
	run := func(step int) queuedEvent {
		return queuedEvent{raw: fmt.Sprintf("TBM:RUN|zul_gurub|running|%d/%d|high_priestess_arlokk|move|rest|resting|until boss (step %d)", step, n.total, n.until)}
	}
	f.agent.mu.Lock()
	last := f.agent.startQueue[len(f.agent.startQueue)-1]
	f.agent.startQueue = append(f.agent.startQueue[:len(f.agent.startQueue)-1], run(n.start+1), run(n.start+2), run(n.start+3), last)
	f.agent.mu.Unlock()
	if code := f.run(context.Background()); code != exitOK {
		t.Fatalf("exit %d: %s\n%s", code, f.stderr.String(), f.stdout.String())
	}
	if n := countPrefix(f.agent.gmLines(), ".debug instancedata"); n != 1 {
		t.Fatalf("%d lines, want the mid-run firing's one: %q", n, f.agent.gmLines())
	}
	by, _ := os.ReadFile(filepath.Join(f.out, "run.log"))
	var steps []int
	for _, line := range strings.Split(string(by), "\n") {
		if len(line) > 13 {
			line = line[13:]
		}
		st, ok := raidwalk.ParseRun(line)
		if !ok || st.Plan != "zul_gurub" || st.State != tbm.RunRunning {
			continue
		}
		steps = append(steps, st.Step)
	}
	if len(steps) < 4 {
		t.Fatalf("run.log running steps %v: want the start lines plus mid, X and Y", steps)
	}
	for i := 1; i < len(steps); i++ {
		if steps[i] < steps[i-1] {
			t.Fatalf("run.log running steps regressed %v: the lines after a firing processed past it", steps)
		}
	}
}

// Item 5: `--from step N` on a pull row inside the leg fires BEFORE the run
// starts, on the leg PlaceLegName resolves for the step. The placed walk
// stands at the step's own point; the gate lines precede `run start`.
func TestGMInstanceDataFromStepFiresBeforeRunStart(t *testing.T) {
	rows := zgRows(t)
	step := 0
	for i, s := range rows.Steps {
		if s.Kind != planexport.KindPull {
			continue
		}
		if _, ok := raidwalk.PlacePoint(rows, i+1); !ok {
			continue
		}
		if leg := raidwalk.PlaceLegName(rows, i+1); strings.EqualFold(leg, "high_priest_thekal") {
			step = i + 1
			break
		}
	}
	if step == 0 {
		t.Fatal("no placeable thekal pull row in the ZG rows")
	}
	f, _ := newPlaceFixture(t)
	f.opts.from = fmt.Sprintf("step %d", step)
	f.opts.gmInstanceData = stringList{"high_priest_thekal:1 3"}
	n := zgPlaceSteps(t)
	f.agent.startReply = placeStartReply("zul_gurub", step, n.total, n.until)
	f.agent.unitsFn = func(int) []objects.Unit { return []objects.Unit{} }
	if code := f.run(context.Background()); code != exitOK {
		t.Fatalf("exit %d: %s\n%s", code, f.stderr.String(), f.stdout.String())
	}
	log := f.agentLog()
	inst, run := -1, -1
	for i, line := range log {
		if strings.HasPrefix(line, "gm .debug instancedata") && inst < 0 {
			inst = i
		}
		if strings.HasPrefix(line, "cmdr run start ") && run < 0 {
			run = i
		}
	}
	if inst < 0 {
		t.Fatalf("no instance-data line went out: %q", log)
	}
	if run < 0 {
		t.Fatalf("no run start went out: %q", log)
	}
	if inst > run {
		t.Fatalf("the line went out after the run start (gm %d, run %d): %q", inst, run, log)
	}
	if n := countPrefix(f.agent.gmLines(), ".debug instancedata"); n != 1 {
		t.Fatalf("%d lines, want the from-step firing's one: %q", n, f.agent.gmLines())
	}
}

// Item 6: a `q inst` reading of 0 is not cached as "unreadable": the next
// firing retries the read, and the walk still fires once the id arrives.
func TestGMInstanceDataZeroInstRetries(t *testing.T) {
	f := newFixture(t)
	f.opts.gmInstanceData = stringList{"onyxia:1 3"}
	reads := 0
	f.agent.ttFn = func(cmd string) (ttResult, error) {
		if cmd == "q inst" {
			reads++
			if reads == 1 {
				return ttReplyOf(t, map[string]any{"map": 249, "inst": 0}), nil
			}
			return ttReplyOf(t, map[string]any{"map": 249, "inst": 77}), nil
		}
		return ttResult{}, nil
	}
	f.agent.startReply = instDataOnyxiaReply()
	f.defaultRun("onyxias_lair")
	instDataOnyxiaRestQueue(f, "onyxias_lair")
	if code := f.run(context.Background()); code != exitOK {
		t.Fatalf("exit %d: %s\n%s", code, f.stderr.String(), f.stdout.String())
	}
	if reads < 2 {
		t.Fatalf("q inst read %d time(s), want a retry after the 0 read", reads)
	}
	if n := countPrefix(f.agent.gmLines(), ".debug instancedata"); n != 1 {
		t.Fatalf("%d lines, want the one firing after the retry: %q", n, f.agent.gmLines())
	}
	if !strings.Contains(strings.Join(f.summary().Notes, "\n"), "WARN") {
		t.Fatalf("notes %q, want the zero-read WARN", f.summary().Notes)
	}
}

// A wipe recovered inside ONE running walk revives the raid in place. The
// boss scripts may have rewritten the priest data meanwhile, so the recovery
// clears the walk's markers and the next status that names the leg resends
// the lines under their own pause and resume: exactly one resend per
// recovery, on top of the placement's first send.
func TestGMInstanceDataResendsAfterWipeRecovery(t *testing.T) {
	n := zgPlaceSteps(t)
	f, _ := newPlaceFixture(t)
	f.opts.gmInstanceData = stringList{"high_priest_thekal:1 3"}
	f.opts.wipeResume = 1
	f.agent.startReply = instDataStartReply(t, n)
	f.agent.unitsFn = func(int) []objects.Unit { return []objects.Unit{} }
	// The run's own status lines name the plan's full leg, as the live server
	// does. One running line before the wipe; the run goes on after the raid
	// was revived.
	leg := func(state string, step int, note string) string {
		return fmt.Sprintf("TBM:RUN|zul_gurub|%s|%d/%d|high_priest_thekal|move|walk|%s", state, step, n.total, note)
	}
	f.agent.startQueue = []queuedEvent{{raw: leg("running", n.start, "walking")}}
	// The raid dies once the run has started, and the run goes on once the
	// recovery resumed it.
	f.agent.onStart = func() {
		f.agent.mu.Lock()
		for _, name := range f.agent.party {
			f.agent.dead[name] = true
		}
		f.agent.mu.Unlock()
	}
	f.agent.onResume = func() {
		f.agent.mu.Lock()
		defer f.agent.mu.Unlock()
		f.agent.lines = append(f.agent.lines,
			queuedEvent{raw: leg("running", n.start+1, "resumed")},
			queuedEvent{raw: leg("running", n.start+1, "walking")},
			queuedEvent{raw: strings.Replace(leg("running", n.start+1, "resting"), "|move|walk|", "|move|rest|", 1)},
			queuedEvent{raw: leg("done", n.start+1, fmt.Sprintf("stopped before step %d (until boss): the party stays where it stands", n.until))})
	}
	if code := f.run(context.Background()); code != exitOK {
		t.Fatalf("exit %d: %s\n%s", code, f.stderr.String(), f.stdout.String())
	}
	if sum := f.summary(); sum.WipeResumes != 1 {
		t.Fatalf("wipe_resumes %d, want 1 (end %q): %s", sum.WipeResumes, sum.EndReason, strings.Join(sum.Notes[len(sum.Notes)-8:], " | "))
	}
	if got := countPrefix(f.agent.gmLines(), ".debug instancedata"); got != 2 {
		t.Fatalf("%d instancedata lines, want 2 (placement + one resend after the recovery): %q", got, f.agent.gmLines())
	}
	// The resend runs under its own pause and resume (after the recovery's own resume).
	if got := strings.Join(f.agent.runVerbs, " "); got != "start resume pause resume" {
		t.Fatalf("run verbs %q, want the recovery's resume then the resend's pause and resume", got)
	}
}

// instDataThekalStep is a placeable thekal pull row of the ZG rows, the
// "resume from step N" start of an autopilot restart.
func instDataThekalStep(t *testing.T) int {
	t.Helper()
	rows := zgRows(t)
	for i, s := range rows.Steps {
		if s.Kind != planexport.KindPull {
			continue
		}
		if _, ok := raidwalk.PlacePoint(rows, i+1); !ok {
			continue
		}
		if strings.EqualFold(raidwalk.PlaceLegName(rows, i+1), "high_priest_thekal") {
			return i + 1
		}
	}
	t.Fatal("no placeable thekal pull row in the ZG rows")
	return 0
}

// A --reuse-instance launch that resumes from a step (the autopilot's
// `--from step N --place --reuse-instance`, no leg words) sends the lines
// exactly once, before `run start`, in both reuse modes: the raid still
// inside (alive) and the logged-out raid resumed from the commander's bind.
func TestGMInstanceDataReuseResumeFromStepSendsOnceBeforeRunStart(t *testing.T) {
	for _, mode := range []string{"alive", "bind"} {
		t.Run(mode, func(t *testing.T) {
			step := instDataThekalStep(t)
			f, p := newPlaceFixture(t)
			f.opts.from = fmt.Sprintf("step %d", step)
			f.opts.reuseInstance = true
			f.opts.gmInstanceData = stringList{"high_priest_thekal:1 3"}
			if mode == "alive" {
				f.agent.entered = true
			} else {
				f.agent.entered = false
				f.agent.instanceBinds = []fakeBind{{inst: 107, ttr: "1h 0m"}}
				p.inst = 107
			}
			n := zgPlaceSteps(t)
			f.agent.startReply = placeStartReply("zul_gurub", step, n.total, n.until)
			f.agent.unitsFn = func(int) []objects.Unit { return []objects.Unit{} }
			if code := f.run(context.Background()); code != exitOK {
				t.Fatalf("exit %d: %s\n%s", code, f.stderr.String(), f.stdout.String())
			}
			inst, run := -1, -1
			for i, line := range f.agentLog() {
				if strings.HasPrefix(line, "gm .debug instancedata") && inst < 0 {
					inst = i
				}
				if strings.HasPrefix(line, "cmdr run start ") && run < 0 {
					run = i
				}
			}
			if inst < 0 || run < 0 || inst > run {
				t.Fatalf("instance-data at %d, run start at %d (want both, data first): %q", inst, run, f.agentLog())
			}
			if n := countPrefix(f.agent.gmLines(), ".debug instancedata"); n != 1 {
				t.Fatalf("%d lines, want exactly 1: %q", n, f.agent.gmLines())
			}
		})
	}
}

// instDataThekalBossStep is the step of the ZG thekal leg's boss row.
func instDataThekalBossStep(t *testing.T) int {
	t.Helper()
	rows := zgRows(t)
	for i, s := range rows.Steps {
		if s.Kind == planexport.KindBoss && strings.EqualFold(raidwalk.PlaceLegName(rows, i+1), "high_priest_thekal") {
			return i + 1
		}
	}
	t.Fatal("no thekal boss row in the ZG rows")
	return 0
}

// bossRowFixture is the thekal-leg walk the boss-row resend tests drive: the
// launch send goes out before the run starts, then the queued statuses play.
func bossRowFixture(t *testing.T, pageSize int, build func(st func(state string, step int, kind, phase, note string) queuedEvent, boss int, n zgPlaceNums) []queuedEvent) *fixture {
	t.Helper()
	n := zgPlaceSteps(t)
	boss := instDataThekalBossStep(t)
	f, _ := newPlaceFixture(t)
	f.opts.gmInstanceData = stringList{"high_priest_thekal:1 3"}
	f.agent.startReply = instDataStartReply(t, n)
	f.agent.unitsFn = func(int) []objects.Unit { return []objects.Unit{} }
	st := func(state string, step int, kind, phase, note string) queuedEvent {
		return queuedEvent{raw: fmt.Sprintf("TBM:RUN|zul_gurub|%s|%d/%d|high_priest_thekal|%s|%s|%s", state, step, n.total, kind, phase, note)}
	}
	f.agent.startQueue = build(st, boss, n)
	f.agent.pageSize = pageSize
	if code := f.run(context.Background()); code != exitOK {
		t.Fatalf("exit %d: %s\n%s", code, f.stderr.String(), f.stdout.String())
	}
	return f
}

func bossRowDone(st func(string, int, string, string, string) queuedEvent, boss int, n zgPlaceNums) queuedEvent {
	return st("done", boss, "boss", "pull", fmt.Sprintf("stopped before step %d (until boss): the party stays where it stands", n.until))
}

// gmInstanceDataPrePull is the boss-row resend's gate: the status is before
// the pull (phase begin/gather/walk/rest, no pulling or fighting note), no later
// line of the same page shows the pull, and no live bot of the party is in
// combat in a frame no older than gmInstanceDataFrameFresh.
func TestGMInstanceDataPrePullGate(t *testing.T) {
	now := time.Date(2026, 11, 6, 12, 0, 0, 0, time.UTC)
	status := func(phase, note string) tbm.RunStatus {
		return tbm.RunStatus{State: tbm.RunRunning, Step: 700, Kind: "boss", Phase: phase, Note: note}
	}
	cases := []struct {
		name  string
		st    tbm.RunStatus
		later bool
		bots  map[string]botPos
		want  bool
	}{
		{"begin", status("begin", "starting"), false, nil, true},
		{"gather", status("gather", "gathering"), false, nil, true},
		{"rest", status("rest", "resting"), false, nil, true},
		{"walk", status("walk", "walking"), false, nil, true},
		{"pull phase", status("pull", "pulling"), false, nil, false},
		{"hold phase", status("hold", "holding"), false, nil, false},
		{"fighting note at begin", status("begin", "fighting"), false, nil, false},
		{"pulling note at walk", status("walk", "pulling Hakkar"), false, nil, false},
		{"later pull line", status("begin", "starting"), true, nil, false},
		{"bot in combat", status("begin", "starting"), false, map[string]botPos{"A": {inCombat: true, at: now.Add(-3 * time.Second)}}, false},
		{"dead bot flagged combat", status("begin", "starting"), false, map[string]botPos{"A": {inCombat: true, dead: true, at: now.Add(-3 * time.Second)}}, true},
		{"stale combat frame", status("begin", "starting"), false, map[string]botPos{"A": {inCombat: true, at: now.Add(-time.Minute)}}, true},
		{"quiet bots", status("begin", "starting"), false, map[string]botPos{"A": {at: now.Add(-3 * time.Second)}}, true},
	}
	for _, c := range cases {
		w := &walk{botAt: c.bots}
		if got := w.gmInstanceDataPrePull(c.st, c.later, now); got != c.want {
			t.Errorf("%s: pre-pull %v, want %v", c.name, got, c.want)
		}
	}
}

// A later line of the same event page that shows the pull or fight of the same
// step blocks the resend, even when the first line looks safe.
func TestGMInstanceDataLaterPullInPage(t *testing.T) {
	line := func(step int, phase, note string) events.Event {
		return events.Event{Kind: events.KindSystem, Text: fmt.Sprintf("TBM:RUN|zul_gurub|running|%d/733|high_priest_thekal|boss|%s|%s", step, phase, note)}
	}
	w := &walk{plan: &dungeon.Plan{Name: "zul_gurub"}}
	if w.gmInstanceDataLaterPull([]events.Event{line(700, "begin", "starting"), line(701, "pull", "pulling")}, 700) {
		t.Fatal("another step's pull must not block")
	}
	if !w.gmInstanceDataLaterPull([]events.Event{line(700, "begin", "starting"), line(700, "pull", "pulling")}, 700) {
		t.Fatal("a later pull line of the same step must block")
	}
	if !w.gmInstanceDataLaterPull([]events.Event{line(700, "begin", "fighting")}, 700) {
		t.Fatal("a later fighting note of the same step must block")
	}
	if w.gmInstanceDataLaterPull([]events.Event{line(700, "begin", "starting")}, 700) {
		t.Fatal("a quiet later line must not block")
	}
}

// The step before the boss row reaches an out-of-combat rest: the resend goes
// out there (well before the boss pull) and the boss row does not resend again.
func TestGMInstanceDataResendsAtTheRestBeforeTheBossRow(t *testing.T) {
	f := bossRowFixture(t, 1, func(st func(string, int, string, string, string) queuedEvent, boss int, n zgPlaceNums) []queuedEvent {
		return []queuedEvent{
			st("running", boss-1, "move", "walk", "walking"),
			st("running", boss-1, "move", "rest", "resting"),
			st("running", boss, "boss", "pull", "pulling"),
			bossRowDone(st, boss, n),
		}
	})
	if got := countPrefix(f.agent.gmLines(), ".debug instancedata"); got != 2 {
		t.Fatalf("%d instancedata lines, want the launch send + one resend at the rest: %q", got, f.agent.gmLines())
	}
	// The rest handled the boss row: it does not warn when the row arrives.
	if hasNote(f.summary(), "no out-of-combat rest before boss row") {
		t.Fatalf("notes %q: the boss row was handled at the rest, it must not warn", f.summary().Notes)
	}
	if got := strings.Join(f.agent.runVerbs, " "); got != "start pause resume" {
		t.Fatalf("run verbs %q, want one pause and resume", got)
	}
}

// No out-of-combat rest came before the boss row: a quiet begin status is the
// LAST line of its page and the pull arrives on the next page, which no
// look-ahead can see. The boss row never sends: one WARN, no pause.
func TestGMInstanceDataBossRowWithoutRestWarnsAndSkips(t *testing.T) {
	for _, tc := range []struct {
		name  string
		build func(st func(string, int, string, string, string) queuedEvent, boss int, n zgPlaceNums) []queuedEvent
	}{
		{"begin last in page, pull on the next", func(st func(string, int, string, string, string) queuedEvent, boss int, n zgPlaceNums) []queuedEvent {
			return []queuedEvent{
				st("running", boss-1, "move", "walk", "walking"),
				st("running", boss, "boss", "begin", "starting the boss row"),
				st("running", boss, "boss", "pull", "pulling"),
				bossRowDone(st, boss, n),
			}
		}},
		{"already pulling", func(st func(string, int, string, string, string) queuedEvent, boss int, n zgPlaceNums) []queuedEvent {
			return []queuedEvent{
				st("running", boss, "boss", "pull", "pulling"),
				bossRowDone(st, boss, n),
			}
		}},
		{"fight carried over at begin", func(st func(string, int, string, string, string) queuedEvent, boss int, n zgPlaceNums) []queuedEvent {
			return []queuedEvent{
				st("running", boss, "boss", "begin", "fighting"),
				bossRowDone(st, boss, n),
			}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := bossRowFixture(t, 1, tc.build)
			if got := countPrefix(f.agent.gmLines(), ".debug instancedata"); got != 1 {
				t.Fatalf("%d instancedata lines, want only the launch send: %q", got, f.agent.gmLines())
			}
			if got := strings.Join(f.agent.runVerbs, " "); got != "start" {
				t.Fatalf("run verbs %q, want no pause", got)
			}
			warns := 0
			for _, n := range f.summary().Notes {
				if strings.Contains(n, "gm-instance-data: no out-of-combat rest before boss row") &&
					strings.Contains(n, "resend skipped") {
					warns++
				}
			}
			if warns != 1 {
				t.Fatalf("%d skip WARNs, want exactly 1: %q", warns, f.summary().Notes)
			}
		})
	}
}

// The rest resend is once per boss row per walk: a second rest of the step
// before the same boss row sends nothing more.
func TestGMInstanceDataRestResendOncePerBossRow(t *testing.T) {
	f := bossRowFixture(t, 1, func(st func(string, int, string, string, string) queuedEvent, boss int, n zgPlaceNums) []queuedEvent {
		return []queuedEvent{
			st("running", boss-1, "move", "rest", "resting"),
			st("running", boss-1, "move", "walk", "walking"),
			st("running", boss-1, "move", "rest", "resting again"),
			bossRowDone(st, boss, n),
		}
	})
	if got := countPrefix(f.agent.gmLines(), ".debug instancedata"); got != 2 {
		t.Fatalf("%d instancedata lines, want the launch send + one rest resend: %q", got, f.agent.gmLines())
	}
}

// instDataLegBossStep is the step of a leg's boss row in the ZG rows.
func instDataLegBossStep(t *testing.T, leg string) int {
	t.Helper()
	rows := zgRows(t)
	for i, s := range rows.Steps {
		if s.Kind == planexport.KindBoss && strings.EqualFold(raidwalk.PlaceLegName(rows, i+1), leg) {
			return i + 1
		}
	}
	t.Fatalf("no %s boss row in the ZG rows", leg)
	return 0
}

// The boss row is the FIRST status that names the leg and nothing was sent for
// it yet (the walk was placed in another leg): the initial send must not fire
// at the boss row, where the pause lands mid-pull. One skip note, no pause, no
// line. The lines go out at the next out-of-combat rest of that leg, and not at
// the walking status in between.
func TestGMInstanceDataFirstLegStatusAtBossRowSendsNothingUntilARest(t *testing.T) {
	n := zgPlaceSteps(t)
	boss := instDataLegBossStep(t, "high_priestess_arlokk")
	st := func(step int, kind, phase, note string) queuedEvent {
		return queuedEvent{raw: fmt.Sprintf("TBM:RUN|zul_gurub|running|%d/%d|high_priestess_arlokk|%s|%s|%s", step, n.total, kind, phase, note)}
	}
	for _, withRest := range []bool{false, true} {
		name := "no rest"
		if withRest {
			name = "later rest"
		}
		t.Run(name, func(t *testing.T) {
			f, _ := newPlaceFixture(t)
			f.opts.gmInstanceData = stringList{"high_priestess_arlokk:1 3"}
			f.agent.startReply = instDataStartReply(t, n)
			f.agent.unitsFn = func(int) []objects.Unit { return []objects.Unit{} }
			q := []queuedEvent{
				st(boss, "boss", "begin", "starting the boss row"),
				st(boss+1, "move", "walk", "walking"),
			}
			if withRest {
				q = append(q, st(boss+1, "move", "rest", "resting"))
			}
			q = append(q, queuedEvent{raw: fmt.Sprintf("TBM:RUN|zul_gurub|done|%d/%d|high_priestess_arlokk|move|walk|stopped before step %d (until boss): the party stays where it stands", boss+1, n.total, n.until)})
			f.agent.startQueue = q
			if code := f.run(context.Background()); code != exitOK {
				t.Fatalf("exit %d: %s\n%s", code, f.stderr.String(), f.stdout.String())
			}
			got := countPrefix(f.agent.gmLines(), ".debug instancedata")
			verbs := strings.Join(f.agent.runVerbs, " ")
			if withRest {
				if got != 1 || verbs != "start pause resume" {
					t.Fatalf("%d lines, run verbs %q, want the rest's one send under one pause", got, verbs)
				}
			} else if got != 0 || verbs != "start" {
				t.Fatalf("%d lines, run verbs %q, want nothing sent and no pause", got, verbs)
			}
			warns := 0
			for _, note := range f.summary().Notes {
				if strings.Contains(note, "gm-instance-data: no out-of-combat rest before boss row") {
					warns++
				}
			}
			if warns != 1 {
				t.Fatalf("%d skip notes, want 1: %q", warns, f.summary().Notes)
			}
		})
	}
}

// Allowlist: a pending instance-data send fires ONLY at a rest or gather status
// that passes the pre-pull gate (or as the launch send before `run start`). At
// any other status, a boss row, a pull, a fight, a walking status, an unknown
// phase, it is deferred and never fires. The walk is placed in the thekal leg
// and the flag names the arlokk leg, so no launch send happens and the first
// arlokk status finds the send pending.
func TestGMInstanceDataFiresOnlyAtAnOutOfCombatRest(t *testing.T) {
	n := zgPlaceSteps(t)
	boss := instDataLegBossStep(t, "high_priestess_arlokk")
	cases := []struct {
		name              string
		step              int
		kind, phase, note string
		wantLines         int
	}{
		{"walking", boss - 2, "move", "walk", "walking", 0},
		{"begin", boss - 2, "move", "begin", "starting", 0},
		{"boss row begin", boss, "boss", "begin", "starting the boss row", 0},
		{"boss row rest", boss, "boss", "rest", "resting", 1},
		{"pull", boss - 2, "pull", "pull", "pulling", 0},
		{"fight", boss - 2, "pull", "pull", "fighting", 0},
		{"hold", boss - 2, "hold", "hold", "holding", 0},
		{"task", boss - 2, "move", "task", "task", 0},
		{"wait", boss - 2, "move", "wait", "waiting", 0},
		{"unknown phase", boss - 2, "move", "somethingnew", "walking", 0},
		{"empty phase", boss - 2, "move", "", "walking", 0},
		{"rest with a fight note", boss - 2, "move", "rest", "fighting", 0},
		{"rest", boss - 2, "move", "rest", "resting", 1},
		{"gather", boss - 2, "move", "gather", "gathering", 1},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f, _ := newPlaceFixture(t)
			f.opts.gmInstanceData = stringList{"high_priestess_arlokk:1 3"}
			f.agent.startReply = instDataStartReply(t, n)
			f.agent.unitsFn = func(int) []objects.Unit { return []objects.Unit{} }
			f.agent.startQueue = []queuedEvent{
				{raw: fmt.Sprintf("TBM:RUN|zul_gurub|running|%d/%d|high_priestess_arlokk|%s|%s|%s", c.step, n.total, c.kind, c.phase, c.note)},
				{raw: fmt.Sprintf("TBM:RUN|zul_gurub|done|%d/%d|high_priestess_arlokk|move|walk|stopped before step %d (until boss): the party stays where it stands", c.step, n.total, n.until)},
			}
			if code := f.run(context.Background()); code != exitOK {
				t.Fatalf("exit %d: %s\n%s", code, f.stderr.String(), f.stdout.String())
			}
			if got := countPrefix(f.agent.gmLines(), ".debug instancedata"); got != c.wantLines {
				t.Fatalf("%d lines, want %d: %q", got, c.wantLines, f.agent.gmLines())
			}
			wantVerbs := "start"
			if c.wantLines > 0 {
				wantVerbs = "start pause resume"
			}
			if got := strings.Join(f.agent.runVerbs, " "); got != wantVerbs {
				t.Fatalf("run verbs %q, want %q", got, wantVerbs)
			}
		})
	}
}

// Codex's bypass: `q inst` answers 0 on the first reads, so nothing could fire
// (and nothing was held); when the id arrives at a plain walking status the send
// must still wait for a rest.
func TestGMInstanceDataZeroInstThenWalkingWaitsForARest(t *testing.T) {
	for _, withRest := range []bool{false, true} {
		name := "no rest"
		if withRest {
			name = "rest"
		}
		t.Run(name, func(t *testing.T) {
			f := newFixture(t)
			f.opts.gmInstanceData = stringList{"onyxia:1 3"}
			reads := 0
			f.agent.ttFn = func(cmd string) (ttResult, error) {
				if cmd == "q inst" {
					reads++
					if reads <= 2 {
						return ttReplyOf(t, map[string]any{"map": 249, "inst": 0}), nil
					}
					return ttReplyOf(t, map[string]any{"map": 249, "inst": 77}), nil
				}
				return ttResult{}, nil
			}
			f.agent.startReply = instDataOnyxiaReply()
			st := func(state, phase, note string) queuedEvent {
				return queuedEvent{raw: fmt.Sprintf("TBM:RUN|onyxias_lair|%s|3/29|onyxia|move|%s|%s", state, phase, note)}
			}
			q := []queuedEvent{
				st("running", "walk", "walking"),
				st("running", "walk", "walking"),
				st("running", "walk", "walking"),
			}
			if withRest {
				q = append(q, st("running", "rest", "resting"))
			}
			q = append(q, st("done", "walk", "stopped before step 29 (until boss): the party stays where it stands"))
			f.defaultRun("onyxias_lair")
			f.agent.mu.Lock()
			f.agent.startQueue = q
			f.agent.mu.Unlock()
			if code := f.run(context.Background()); code != exitOK {
				t.Fatalf("exit %d: %s\n%s", code, f.stderr.String(), f.stdout.String())
			}
			if reads < 3 {
				t.Fatalf("q inst read %d time(s), want the id read at the walking statuses", reads)
			}
			want, verbs := 0, "start"
			if withRest {
				want, verbs = 1, "start pause resume"
			}
			if n := countPrefix(f.agent.gmLines(), ".debug instancedata"); n != want {
				t.Fatalf("%d lines, want %d: %q", n, want, f.agent.gmLines())
			}
			if got := strings.Join(f.agent.runVerbs, " "); got != verbs {
				t.Fatalf("run verbs %q, want %q", got, verbs)
			}
		})
	}
}
