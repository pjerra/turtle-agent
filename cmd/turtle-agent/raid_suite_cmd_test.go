package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/pjerra/turtle-agent/internal/dungeon"
	"github.com/pjerra/turtle-agent/internal/planexport"
	"github.com/pjerra/turtle-agent/internal/raidwalk"
)

// suiteRig replaces raid-suite's walk and environment with fakes: each walk records its options and writes the
// summary its script returns (none when nil).
type suiteRig struct {
	walks  []raidWalkOptions
	script func(o raidWalkOptions) (int, *raidwalk.Summary)
}

func newSuiteRig(t *testing.T) *suiteRig {
	t.Helper()
	r := &suiteRig{}
	prevWalk, prevEnv := raidSuiteWalk, raidSuiteEnv
	t.Cleanup(func() { raidSuiteWalk, raidSuiteEnv = prevWalk, prevEnv })
	raidSuiteEnv = func(*raidWalkOptions) (walkEnv, walkDeps, error) {
		return walkEnv{sleep: func(context.Context, time.Duration) error { return nil }}, walkDeps{}, nil
	}
	raidSuiteWalk = func(ctx context.Context, o raidWalkOptions, env walkEnv, deps walkDeps, stdout, stderr io.Writer) int {
		r.walks = append(r.walks, o)
		if r.script == nil {
			return 0
		}
		code, sum := r.script(o)
		if sum != nil {
			writeSuiteSummary(t, o.out, sum)
		}
		return code
	}
	return r
}

// suiteSummary is a good onyxias_lair walk: tier 2 from the table, the given until, one step of runS seconds.
func suiteSummary(until string, started time.Time, runS float64) *raidwalk.Summary {
	return &raidwalk.Summary{Format: "tb-raid-walk", Version: 1, Plan: "onyxias_lair", Map: 249, Until: until, Speed: 1.0,
		Started: started, Ended: started.Add(time.Duration(runS) * time.Second), EndReason: "until", ExitCode: 0,
		Roster:       raidwalk.RosterSummary{Tier: 2, TierSource: "table"},
		StepTimeline: []raidwalk.StepSummary{{Step: 1, Leg: "a", Kind: "move", Seconds: runS}}, StepsTotal: 29}
}

func writeSuiteSummary(t *testing.T, out string, sum *raidwalk.Summary) {
	t.Helper()
	if err := os.MkdirAll(out, 0o700); err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(sum)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(out, "summary.json"), raw, 0o600); err != nil {
		t.Fatal(err)
	}
}

// writeSuiteFile writes a tb-raid-suite v1 file of the given raids and returns its path.
func writeSuiteFile(t *testing.T, raids ...raidSuiteEntry) string {
	t.Helper()
	raw, err := json.Marshal(raidSuiteFile{Format: "tb-raid-suite", Version: 1, Raids: raids})
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "suite.json")
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

// writeRosterTier writes a tb-raid-roster v1 file that carries tier, for a raid the table leaves unknown.
func writeRosterTier(t *testing.T, tier int) string {
	t.Helper()
	raw := `{"format":"tb-raid-roster","version":1,"level":60,"tier":` + strconv.Itoa(tier) +
		`,"batches":[{"role":"mt","hire":"1 warrior:tank"},{"role":"rest","hire":"10h 27d"}]}`
	path := filepath.Join(t.TempDir(), "roster.json")
	if err := os.WriteFile(path, []byte(raw), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func readSuiteReport(t *testing.T, out string) raidSuiteReport {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(out, "suite.json"))
	if err != nil {
		t.Fatalf("suite.json: %v", err)
	}
	var rep raidSuiteReport
	if err := json.Unmarshal(raw, &rep); err != nil {
		t.Fatalf("suite.json: %v", err)
	}
	return rep
}

func walkedPlans(walks []raidWalkOptions) []string {
	var out []string
	for _, w := range walks {
		out = append(out, w.plan)
	}
	return out
}

const suiteDir = "../../dungeons"

func TestRaidSuiteRefusesUnknownTier(t *testing.T) {
	rig := newSuiteRig(t)
	rig.script = func(o raidWalkOptions) (int, *raidwalk.Summary) { return 0, suiteSummary(o.until, time.Now(), 300) }
	suite := writeSuiteFile(t, raidSuiteEntry{Plan: "onyxias_lair", Dir: suiteDir}, raidSuiteEntry{Plan: "molten_core", Dir: suiteDir})
	out := filepath.Join(t.TempDir(), "suite-out")
	var stdout, stderr bytes.Buffer
	code := raidSuiteCmd(context.Background(), []string{"--suite", suite, "--history", t.TempDir(), "--out", out}, &stdout, &stderr)
	if code != exitOK {
		t.Fatalf("exit %d, want 0; stderr %q", code, stderr.String())
	}
	if got := walkedPlans(rig.walks); len(got) != 1 || got[0] != "onyxias_lair" {
		t.Fatalf("walked %v, want only onyxias_lair (molten_core has no sourced tier)", got)
	}
	rep := readSuiteReport(t, out)
	if rep.Raids[1].Verdict != "refused" || !strings.Contains(rep.Raids[1].Reason, "molten_core") {
		t.Fatalf("molten_core verdict %q reason %q, want refused naming the raid", rep.Raids[1].Verdict, rep.Raids[1].Reason)
	}
}

func TestRaidSuiteUsesFirstLegUntil(t *testing.T) {
	rig := newSuiteRig(t)
	rig.script = func(o raidWalkOptions) (int, *raidwalk.Summary) { return 0, suiteSummary(o.until, time.Now(), 300) }
	suite := writeSuiteFile(t, raidSuiteEntry{Plan: "onyxias_lair", Dir: suiteDir})
	plan, err := dungeon.Load(filepath.Join(suiteDir, "onyxias_lair.json"))
	if err != nil {
		t.Fatal(err)
	}
	res, err := planexport.Build(plan, planexport.Options{})
	if err != nil {
		t.Fatal(err)
	}
	want, err := raidwalk.FirstLegUntil(res.Rows)
	if err != nil {
		t.Fatal(err)
	}
	out := filepath.Join(t.TempDir(), "suite-out")
	var stdout, stderr bytes.Buffer
	if code := raidSuiteCmd(context.Background(), []string{"--suite", suite, "--history", t.TempDir(), "--out", out}, &stdout, &stderr); code != exitOK {
		t.Fatalf("exit %d, want 0; stderr %q", code, stderr.String())
	}
	if len(rig.walks) != 1 || rig.walks[0].until != want {
		t.Fatalf("walk until %q, want the first leg's %q", rig.walks[0].until, want)
	}
}

func TestRaidSuiteRegressionExit24(t *testing.T) {
	rig := newSuiteRig(t)
	rig.script = func(o raidWalkOptions) (int, *raidwalk.Summary) { return 0, suiteSummary(o.until, time.Now(), 412) }
	suite := writeSuiteFile(t, raidSuiteEntry{Plan: "onyxias_lair", Dir: suiteDir})
	hist := t.TempDir()
	old := time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)
	writeSuiteSummary(t, filepath.Join(hist, "onyxias_lair", "20250101-000000"), suiteSummary("boss", old, 301))
	out := filepath.Join(t.TempDir(), "suite-out")
	var stdout, stderr bytes.Buffer
	code := raidSuiteCmd(context.Background(), []string{"--suite", suite, "--history", hist, "--out", out}, &stdout, &stderr)
	if code != exitRaidSuiteRegression {
		t.Fatalf("exit %d, want 24; stderr %q", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "REGRESSION time 412s vs 301s (+37%)") {
		t.Fatalf("stdout %q has no REGRESSION line for the 412 s run", stdout.String())
	}
	rep := readSuiteReport(t, out)
	if rep.Raids[0].Verdict != "regression" || len(rep.Raids[0].Regressions) != 1 || rep.Raids[0].Regressions[0].Metric != "time" {
		t.Fatalf("verdict %q regressions %+v, want one time regression", rep.Raids[0].Verdict, rep.Raids[0].Regressions)
	}
	if _, err := os.Stat(filepath.Join(out, "suite.txt")); err != nil {
		t.Fatalf("suite.txt: %v", err)
	}
}

func TestRaidSuiteNewBaseline(t *testing.T) {
	rig := newSuiteRig(t)
	rig.script = func(o raidWalkOptions) (int, *raidwalk.Summary) { return 0, suiteSummary(o.until, time.Now(), 300) }
	suite := writeSuiteFile(t, raidSuiteEntry{Plan: "onyxias_lair", Dir: suiteDir})
	out := filepath.Join(t.TempDir(), "suite-out")
	var stdout, stderr bytes.Buffer
	if code := raidSuiteCmd(context.Background(), []string{"--suite", suite, "--history", t.TempDir(), "--out", out}, &stdout, &stderr); code != exitOK {
		t.Fatalf("exit %d, want 0 for a first walk; stderr %q", code, stderr.String())
	}
	rep := readSuiteReport(t, out)
	if rep.Raids[0].Verdict != "new" || rep.Counts.New != 1 || rep.Raids[0].Baseline != nil {
		t.Fatalf("verdict %q counts %+v, want new with no baseline", rep.Raids[0].Verdict, rep.Counts)
	}
}

func TestRaidSuiteRefusesSlot3AndSlotPair(t *testing.T) {
	cases := []struct {
		slot string
		code int
	}{{"3", exitSlotRefused}, {"1,2", exitUsage}}
	for _, c := range cases {
		rig := newSuiteRig(t)
		rig.script = func(o raidWalkOptions) (int, *raidwalk.Summary) { return 0, suiteSummary(o.until, time.Now(), 300) }
		suite := writeSuiteFile(t, raidSuiteEntry{Plan: "onyxias_lair", Dir: suiteDir})
		out := filepath.Join(t.TempDir(), "suite-out")
		var stdout, stderr bytes.Buffer
		code := raidSuiteCmd(context.Background(), []string{"--suite", suite, "--history", t.TempDir(), "--out", out, "--slot", c.slot}, &stdout, &stderr)
		if code != c.code {
			t.Fatalf("--slot %s: exit %d, want %d; stderr %q", c.slot, code, c.code, stderr.String())
		}
		if len(rig.walks) != 0 {
			t.Fatalf("--slot %s: walked %v before refusing", c.slot, walkedPlans(rig.walks))
		}
		if _, err := os.Stat(out); !os.IsNotExist(err) {
			t.Fatalf("--slot %s: the out dir was created before refusing", c.slot)
		}
	}
}

func TestRaidSuiteInterruptSkipsRest(t *testing.T) {
	rig := newSuiteRig(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	rig.script = func(o raidWalkOptions) (int, *raidwalk.Summary) {
		cancel() // the operator's Ctrl-C lands while the first walk runs
		return 0, suiteSummary(o.until, time.Now(), 300)
	}
	roster := writeRosterTier(t, 2)
	suite := writeSuiteFile(t, raidSuiteEntry{Plan: "onyxias_lair", Dir: suiteDir},
		raidSuiteEntry{Plan: "molten_core", Dir: suiteDir, Hires: roster})
	out := filepath.Join(t.TempDir(), "suite-out")
	var stdout, stderr bytes.Buffer
	if code := raidSuiteCmd(ctx, []string{"--suite", suite, "--history", t.TempDir(), "--out", out}, &stdout, &stderr); code != exitOK {
		t.Fatalf("exit %d, want 0; stderr %q", code, stderr.String())
	}
	if got := walkedPlans(rig.walks); len(got) != 1 {
		t.Fatalf("walked %v, want only the first raid", got)
	}
	rep := readSuiteReport(t, out)
	if len(rep.Raids) != 2 || rep.Raids[1].Verdict != "skipped" || rep.Raids[1].Reason != "interrupted" {
		t.Fatalf("raids %+v, want the second skipped: interrupted", rep.Raids)
	}
}

func TestRaidSuiteCompareOnlyWalksNothing(t *testing.T) {
	rig := newSuiteRig(t)
	hist := t.TempDir()
	writeSuiteSummary(t, filepath.Join(hist, "onyxias_lair", "20250101-000000"),
		suiteSummary("boss", time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC), 301))
	writeSuiteSummary(t, filepath.Join(hist, "onyxias_lair", "20250201-000000"),
		suiteSummary("boss", time.Date(2025, 2, 1, 0, 0, 0, 0, time.UTC), 412))
	roster := writeRosterTier(t, 2)
	suite := writeSuiteFile(t, raidSuiteEntry{Plan: "onyxias_lair", Dir: suiteDir},
		raidSuiteEntry{Plan: "molten_core", Dir: suiteDir, Hires: roster})
	out := filepath.Join(t.TempDir(), "suite-out")
	var stdout, stderr bytes.Buffer
	code := raidSuiteCmd(context.Background(), []string{"--suite", suite, "--history", hist, "--out", out, "--compare-only"}, &stdout, &stderr)
	if code != exitRaidSuiteRegression {
		t.Fatalf("exit %d, want 24; stderr %q", code, stderr.String())
	}
	if len(rig.walks) != 0 {
		t.Fatalf("--compare-only walked %v", walkedPlans(rig.walks))
	}
	rep := readSuiteReport(t, out)
	if rep.Raids[0].Verdict != "regression" || rep.Raids[1].Verdict != "skipped" {
		t.Fatalf("verdicts %q and %q, want regression and skipped (no walk in the history)", rep.Raids[0].Verdict, rep.Raids[1].Verdict)
	}
}

func TestRaidSuiteDryRun(t *testing.T) {
	rig := newSuiteRig(t)
	suite := writeSuiteFile(t, raidSuiteEntry{Plan: "onyxias_lair", Dir: suiteDir}, raidSuiteEntry{Plan: "molten_core", Dir: suiteDir})
	hist := t.TempDir()
	var stdout, stderr bytes.Buffer
	if code := raidSuiteCmd(context.Background(), []string{"--suite", suite, "--history", hist, "--dry-run"}, &stdout, &stderr); code != exitOK {
		t.Fatalf("exit %d, want 0; stderr %q", code, stderr.String())
	}
	if len(rig.walks) != 0 {
		t.Fatalf("--dry-run walked %v", walkedPlans(rig.walks))
	}
	stamp := regexp.MustCompile(`[0-9]{8}-[0-9]{6}`)
	got := strings.ReplaceAll(stamp.ReplaceAllString(stdout.String(), "<stamp>"), hist, "<history>")
	want := "onyxias_lair tier 2 (table) until boss out <history>/onyxias_lair/<stamp>\n" +
		"molten_core: refused: raid molten_core has no minimum hire tier in internal/raidwalk/tiers.go: pass --tier N (1-3), the kit a player holds when the raid is next\n"
	if got != want {
		t.Fatalf("dry run stdout:\n%s\nwant:\n%s", got, want)
	}
}
