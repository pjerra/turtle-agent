package main

// The raid-walk --gm-instance-data test tool (replaces walk 39's hkgmall.sh):
// the commander sends `.debug instancedata <a> <b>` through the walk's own gm
// path (the commander's gm RPC, allow-listed), one event line per command, in
// order, 3 s apart, once per instance, when the walk first starts the named
// leg. A placed start inside the leg fires right after placement. A resumed
// walk in the same instance never refires: the markers live in the out dir.
import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/pjerra/turtle-agent/internal/objects"
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
		{"no place", []string{"zul_gurub", "--gm-instance-data", "hakkar:1 3"}, "needs --place"},
		{"no leg", []string{"zul_gurub", "--place", "--from", "leg hakkar", "--sql", "walk.sql", "--gm-instance-data", "1 3"}, "want \"<leg>:"},
		{"bad cmd", []string{"zul_gurub", "--place", "--from", "leg hakkar", "--sql", "walk.sql", "--gm-instance-data", "hakkar:1"}, "not two integers"},
		{"extra word", []string{"zul_gurub", "--place", "--from", "leg hakkar", "--sql", "walk.sql", "--gm-instance-data", "hakkar:1 2 3"}, "not two integers"},
	} {
		var stderr bytes.Buffer
		if _, code := parseRaidWalkArgs(tc.args, &stderr); code != exitUsage || !strings.Contains(stderr.String(), tc.want) {
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
// after placement, before the run goes on: through w.gm (the allow-listed
// commander gm RPC), in order, 3 s apart on the fake clock, one event line
// per command in run.log.
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
		if gap := at[i] - at[i-1]; gap != old.Nanoseconds() {
			t.Fatalf("gap %d is %v, want %v: %v", i, gap, old.Nanoseconds(), at)
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

// A resumed walk in the same instance does not refire: a --reuse-instance
// restart runs in a fresh out dir, so the out dir's marker cannot tell it,
// and the ledger (which outlives the attempt) carries the mark. The reuse
// fixture's kept instance is the place toolkit's answer (inst 103).
func TestGMInstanceDataNoRefireAfterReuseRestart(t *testing.T) {
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
	if n := countPrefix(second.agent.gmLines(), ".debug instancedata"); n != 0 {
		t.Fatalf("resumed walk in the same instance resent: %q", second.agent.gmLines())
	}
	if by, _ := os.ReadFile(filepath.Join(second.out, "run.log")); strings.Contains(string(by), ".debug instancedata") {
		t.Fatalf("resumed walk logged a line it never sent:\n%s", by)
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
