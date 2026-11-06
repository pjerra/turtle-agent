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
	mid := fmt.Sprintf("TBM:RUN|zul_gurub|running|%d/%d|high_priestess_arlokk|move|walk|walking|until boss (step %d)", n.start+1, n.total, n.until)
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
	mid := fmt.Sprintf("TBM:RUN|zul_gurub|running|%d/%d|high_priestess_arlokk|move|walk|walking|until boss (step %d)", n.start+1, n.total, n.until)
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
		return queuedEvent{raw: fmt.Sprintf("TBM:RUN|zul_gurub|running|%d/%d|high_priestess_arlokk|move|walk|walking|until boss (step %d)", step, n.total, n.until)}
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
			queuedEvent{raw: leg("running", n.start+1, "walking")},
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
