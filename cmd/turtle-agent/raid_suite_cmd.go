// raid-suite: walk a list of raid plans one after another, each with raid-walk, and judge each walk against the last
// good walk of its plan in the history (spec raid-walk-addons 2). The pure parts (the metrics, the baseline pick, the
// compare rules, the first leg's until) live in internal/raidwalk (S1); this file holds the flags, the walk of each
// raid, the verdicts and the report (format tb-raid-suite-report v1).
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/pjerra/turtle-agent/internal/dungeon"
	"github.com/pjerra/turtle-agent/internal/planexport"
	"github.com/pjerra/turtle-agent/internal/raidwalk"
	"github.com/pjerra/turtle-agent/internal/slots"
)

// exitRaidSuiteRegression: at least one raid regressed or its walk failed (spec 2.1).
const exitRaidSuiteRegression = 24

const raidSuiteUsage = `usage: turtle-agent raid-suite (--suite FILE | --dir DIR) --history DIR [--server m910q] [--slot N]
    [--only p1,p2] [--speed X] [--gap 60s] [--out DIR] [--dash-url URL] [--dash-user U] [--dash-pass-file PATH]
    [--mmaps DIR] [--slots FILE] [--binary PATH] [--compare-only] [--dry-run] [--json]`

// raidSuiteFormat is the suite file's format and version.
const (
	raidSuiteFormat       = "tb-raid-suite"
	raidSuiteReportFormat = "tb-raid-suite-report"
	raidSuiteStamp        = "20060102-150405"
)

// The verdicts of one raid (spec 2.2).
const (
	suiteRefused    = "refused"
	suiteFailed     = "failed"
	suiteNew        = "new"
	suiteRegression = "regression"
	suiteOK         = "ok"
	suiteSkipped    = "skipped"
)

// raidSuiteWalk walks one raid: raidWalkRun in production, a fake in tests.
var raidSuiteWalk = raidWalkRun

// raidSuiteEnv builds the walk environment once per suite (defaultWalkEnv); a test replaces it.
var raidSuiteEnv = defaultWalkEnv

// raidSuiteOptions are the command's flags.
type raidSuiteOptions struct {
	suite        string
	dir          string
	history      string
	server       string
	slot         string // "" leases any free slot
	only         []string
	speed        float64
	gap          time.Duration
	out          string
	dashURL      string
	dashUser     string
	dashPassFile string
	mmaps        string
	slotsPath    string
	binary       string
	compareOnly  bool
	dryRun       bool
	jsonOut      bool
}

// raidSuiteFile is the tb-raid-suite v1 file: its raids in walk order.
type raidSuiteFile struct {
	Format  string           `json:"format"`
	Version int              `json:"version"`
	Raids   []raidSuiteEntry `json:"raids"`
}

// raidSuiteEntry is one raid as the suite file (or --dir) names it.
type raidSuiteEntry struct {
	Plan  string `json:"plan"`
	Dir   string `json:"dir"`
	SQL   string `json:"sql"`
	Hires string `json:"hires"`
}

// raidSuiteCounts is the report's verdict counts.
type raidSuiteCounts struct {
	OK         int `json:"ok"`
	Regression int `json:"regression"`
	New        int `json:"new"`
	Failed     int `json:"failed"`
	Refused    int `json:"refused"`
	Skipped    int `json:"skipped"`
}

// raidSuiteReport is the tb-raid-suite-report v1 file (suite.json, and stdout with --json).
type raidSuiteReport struct {
	Format  string                `json:"format"`
	Version int                   `json:"version"`
	Started time.Time             `json:"started"`
	Ended   time.Time             `json:"ended"`
	Server  string                `json:"server"`
	History string                `json:"history"`
	Raids   []raidSuiteRaidReport `json:"raids"`
	Counts  raidSuiteCounts       `json:"counts"`
}

// raidSuiteRaidReport is one raid of the report.
type raidSuiteRaidReport struct {
	Plan            string                 `json:"plan"`
	Map             uint32                 `json:"map"`
	Tier            int                    `json:"tier"`
	TierSource      string                 `json:"tier_source"`
	Until           string                 `json:"until"`
	Verdict         string                 `json:"verdict"`
	Reason          string                 `json:"reason"`
	Out             string                 `json:"out"`
	ExitCode        *int                   `json:"exit_code"`
	Baseline        *raidSuiteBaseline     `json:"baseline"`
	Metrics         *raidwalk.SuiteMetrics `json:"metrics"`
	BaselineMetrics *raidwalk.SuiteMetrics `json:"baseline_metrics"`
	Regressions     []raidwalk.Regression  `json:"regressions"`
	Info            []string               `json:"info"`
	StepDeltas      []raidwalk.StepDelta   `json:"step_deltas"`
	Notes           []string               `json:"notes"`
}

// raidSuiteBaseline is the walk a raid was compared with.
type raidSuiteBaseline struct {
	Out      string    `json:"out"`
	Started  time.Time `json:"started"`
	ExitCode int       `json:"exit_code"`
}

// raidSuitePrep is what a raid needs before it is walked: its plan, the tier (a refusal when none is sourced) and the
// until the walk stops at (the first leg, spec 2.2).
type raidSuitePrep struct {
	plan   *dungeon.Plan
	tier   int
	source string
	until  string
}

// raidSuite is one run of the command.
type raidSuite struct {
	o       raidSuiteOptions
	history string
	out     string
	env     walkEnv
	deps    walkDeps
	stdout  io.Writer
	stderr  io.Writer
}

// raidSuiteCmd is the command line entry: flags, the raids, one walk (or comparison) each, the report.
func raidSuiteCmd(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	o, code := parseRaidSuiteArgs(args, stderr)
	if code != exitOK {
		return code
	}
	// The owner rule, before anything else: never slot 3, for every server (spec 2.1).
	if o.slot == "3" {
		fmt.Fprintln(stderr, "raid-suite never uses slot 3 (owner rule)")
		return exitSlotRefused
	}
	entries, code := raidSuiteEntries(o, stderr)
	if code != exitOK {
		return code
	}
	s := &raidSuite{o: o, history: slots.ExpandTilde(o.history), stdout: stdout, stderr: stderr}
	if o.dryRun {
		for _, e := range entries {
			fmt.Fprintln(stdout, s.dryLine(e))
		}
		return exitOK
	}
	if !o.compareOnly {
		if !slotsSupported() {
			return slotsUnsupported("raid-suite", stderr)
		}
		env, deps, err := raidSuiteEnv(&raidWalkOptions{slotsPath: o.slotsPath})
		if err != nil {
			fmt.Fprintf(stderr, "raid-suite: %v\n", err)
			return exitUsage
		}
		s.env, s.deps = env, deps
	}
	out := slots.ExpandTilde(o.out)
	if out == "" {
		out = "raid-suite-" + time.Now().Format(raidSuiteStamp)
	}
	if _, err := os.Lstat(out); err == nil {
		fmt.Fprintf(stderr, "raid-suite: --out %s exists: pick a new dir\n", out)
		return exitUsage
	} else if !os.IsNotExist(err) {
		fmt.Fprintf(stderr, "raid-suite: --out %s: %v\n", out, err)
		return exitUsage
	}
	if err := os.MkdirAll(out, 0o700); err != nil {
		fmt.Fprintf(stderr, "raid-suite: out dir: %v\n", err)
		return exitUsage
	}
	s.out = out

	rep := raidSuiteReport{Format: raidSuiteReportFormat, Version: 1, Started: time.Now(), Server: o.server,
		History: s.history, Raids: []raidSuiteRaidReport{}}
	for i, e := range entries {
		r, walked := s.raid(ctx, e)
		rep.Raids = append(rep.Raids, r)
		// The gap is between two raids: after a walk, when another raid follows and the run was not interrupted.
		if walked && i < len(entries)-1 && ctx.Err() == nil {
			_ = s.env.sleep(ctx, o.gap)
		}
	}
	rep.Ended = time.Now()
	rep.Counts = countSuite(rep.Raids)
	text := raidSuiteText(rep)
	if err := s.writeReport(rep, text); err != nil {
		fmt.Fprintf(stderr, "raid-suite: report: %v\n", err)
		return exitRaidWalkFailed
	}
	if o.jsonOut {
		raw, err := json.MarshalIndent(rep, "", "  ")
		if err != nil {
			fmt.Fprintf(stderr, "raid-suite: report: %v\n", err)
			return exitRaidWalkFailed
		}
		fmt.Fprintln(stdout, string(raw))
	} else {
		fmt.Fprint(stdout, text)
	}
	return raidSuiteExit(rep)
}

// parseRaidSuiteArgs reads the flags and checks their shape (no file or network access).
func parseRaidSuiteArgs(args []string, stderr io.Writer) (raidSuiteOptions, int) {
	o := raidSuiteOptions{}
	fs := flag.NewFlagSet("raid-suite", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.Usage = func() { fmt.Fprintln(stderr, raidSuiteUsage) }
	speed := "1.0"
	only := ""
	fs.StringVar(&o.suite, "suite", "", "the tb-raid-suite v1 file: its raids, in order")
	fs.StringVar(&o.dir, "dir", "", "a folder of plan files: every raid plan in it, by plan name")
	fs.StringVar(&o.history, "history", "", "the history dir: each walk lives at <history>/<plan>/<stamp>/")
	fs.StringVar(&o.server, "server", "m910q", "the slot server")
	fs.StringVar(&o.slot, "slot", "", "the slot id (default: any free slot); 3 is refused")
	fs.StringVar(&only, "only", "", "plan names to walk, comma separated (default: every raid)")
	fs.StringVar(&speed, "speed", "1.0", "the run's test speed, 1.0 to 3.0")
	fs.DurationVar(&o.gap, "gap", 60*time.Second, "the pause between two raids, 0 to 10m")
	fs.StringVar(&o.out, "out", "", "the report dir (default ./raid-suite-<stamp>, refused if it exists)")
	fs.StringVar(&o.dashURL, "dash-url", "", "the dashboard URL (or TBDASH_URL)")
	fs.StringVar(&o.dashUser, "dash-user", "", "the dashboard login (or TBDASH_USER; default TADASH)")
	fs.StringVar(&o.dashPassFile, "dash-pass-file", "", "the dashboard password file (or TBDASH_PASS_FILE)")
	fs.StringVar(&o.mmaps, "mmaps", "", "the navmesh dir for slot setup")
	fs.StringVar(&o.slotsPath, "slots", "", "the slots file (default ~/.config/turtle-agent/slots.json)")
	fs.StringVar(&o.binary, "binary", "", "the agent binary slot setup copies (default: this executable)")
	fs.BoolVar(&o.compareOnly, "compare-only", false, "walk nothing: compare each plan's newest walk in the history with the last good one before it")
	fs.BoolVar(&o.dryRun, "dry-run", false, "print each raid's tier and until, send nothing")
	fs.BoolVar(&o.jsonOut, "json", false, "print the report as JSON on stdout (suite.json is written either way)")
	if err := fs.Parse(args); err != nil {
		return o, exitUsage
	}
	if fs.NArg() != 0 {
		fmt.Fprintln(stderr, raidSuiteUsage)
		return o, exitUsage
	}
	if strings.Contains(o.slot, ",") {
		fmt.Fprintln(stderr, "raid-suite walks one raid at a time (--slot A,B is raid-walk's two-raid form)")
		return o, exitUsage
	}
	if (o.suite == "") == (o.dir == "") {
		fmt.Fprintln(stderr, "raid-suite needs --suite FILE or --dir DIR (one of them)")
		return o, exitUsage
	}
	if o.history == "" {
		fmt.Fprintln(stderr, "raid-suite needs --history DIR")
		return o, exitUsage
	}
	if !raidWalkSpeedRe.MatchString(speed) {
		fmt.Fprintf(stderr, "raid-suite: --speed wants 1.0 to 3.0 with one decimal, not %q\n", speed)
		return o, exitUsage
	}
	v, err := strconv.ParseFloat(speed, 64)
	if err != nil || v < 1.0 || v > 3.0 {
		fmt.Fprintf(stderr, "raid-suite: --speed wants 1.0 to 3.0 with one decimal, not %q\n", speed)
		return o, exitUsage
	}
	o.speed = v
	if o.gap < 0 || o.gap > 10*time.Minute {
		fmt.Fprintf(stderr, "raid-suite: --gap wants 0 to 10m, not %s\n", o.gap)
		return o, exitUsage
	}
	if only != "" {
		for _, name := range strings.Split(only, ",") {
			if !raidWalkPlanRe.MatchString(name) {
				fmt.Fprintf(stderr, "raid-suite: --only: %q is not a plan name\n", name)
				return o, exitUsage
			}
			o.only = append(o.only, name)
		}
	}
	return o, exitOK
}

// raidSuiteEntries reads the raids: the suite file's list in file order, or the raid plans of --dir sorted by plan
// name (a file that does not load as a raid plan is left out). --only filters them; a plan listed twice is refused,
// since each plan has one history dir.
func raidSuiteEntries(o raidSuiteOptions, stderr io.Writer) ([]raidSuiteEntry, int) {
	var raids []raidSuiteEntry
	if o.suite != "" {
		raw, err := os.ReadFile(slots.ExpandTilde(o.suite))
		if err != nil {
			fmt.Fprintf(stderr, "raid-suite: %v\n", err)
			return nil, exitUsage
		}
		var f raidSuiteFile
		if err := json.Unmarshal(raw, &f); err != nil {
			fmt.Fprintf(stderr, "raid-suite: %s: %v\n", o.suite, err)
			return nil, exitUsage
		}
		if f.Format != raidSuiteFormat || f.Version != 1 {
			fmt.Fprintf(stderr, "raid-suite: %s: format %q version %d, want %s version 1\n", o.suite, f.Format, f.Version, raidSuiteFormat)
			return nil, exitUsage
		}
		raids = f.Raids
		for _, e := range raids {
			if !raidWalkPlanRe.MatchString(e.Plan) {
				fmt.Fprintf(stderr, "raid-suite: %q is not a plan name\n", e.Plan)
				return nil, exitUsage
			}
			if e.Dir == "" {
				fmt.Fprintf(stderr, "raid-suite: raid %s has no dir (its plan JSON is <dir>/%s.json)\n", e.Plan, e.Plan)
				return nil, exitUsage
			}
		}
	} else {
		dir := slots.ExpandTilde(o.dir)
		files, err := filepath.Glob(filepath.Join(dir, "*.json"))
		if err != nil {
			fmt.Fprintf(stderr, "raid-suite: --dir %s: %v\n", dir, err)
			return nil, exitUsage
		}
		for _, f := range files {
			p, err := dungeon.Load(f)
			if err != nil || !p.IsRaid() || p.Raid == nil {
				continue
			}
			raids = append(raids, raidSuiteEntry{Plan: p.Name, Dir: dir})
		}
		sort.SliceStable(raids, func(i, j int) bool { return raids[i].Plan < raids[j].Plan })
		if len(raids) == 0 {
			fmt.Fprintf(stderr, "raid-suite: %s holds no raid plan\n", dir)
			return nil, exitUsage
		}
	}
	seen := map[string]bool{}
	for _, e := range raids {
		if seen[e.Plan] {
			fmt.Fprintf(stderr, "raid-suite: raid %s is listed twice: one history dir per plan\n", e.Plan)
			return nil, exitUsage
		}
		seen[e.Plan] = true
	}
	if len(o.only) == 0 {
		return raids, exitOK
	}
	keep := map[string]bool{}
	for _, name := range o.only {
		keep[name] = true
	}
	var out []raidSuiteEntry
	for _, e := range raids {
		if keep[e.Plan] {
			out = append(out, e)
		}
	}
	for _, name := range o.only {
		if !seen[name] {
			fmt.Fprintf(stderr, "raid-suite: --only %s: no such raid in the suite\n", name)
			return nil, exitUsage
		}
	}
	return out, exitOK
}

// raidSuitePrepare is steps 1-3 of a raid: its plan and rows, its tier (refused when the table and the roster leave it
// unknown), and the until the walk stops at (the first leg). An error refuses the raid.
func raidSuitePrepare(e raidSuiteEntry) (raidSuitePrep, error) {
	if !raidWalkPlanRe.MatchString(e.Plan) {
		return raidSuitePrep{}, fmt.Errorf("%q is not a plan name", e.Plan)
	}
	p, err := dungeon.Load(filepath.Join(slots.ExpandTilde(e.Dir), e.Plan+".json"))
	if err != nil {
		return raidSuitePrep{}, err
	}
	if !p.IsRaid() || p.Raid == nil {
		return raidSuitePrep{}, fmt.Errorf("plan %s is not a raid plan", e.Plan)
	}
	rosterTier := 0
	if e.Hires != "" {
		raw, err := os.ReadFile(slots.ExpandTilde(e.Hires))
		if err != nil {
			return raidSuitePrep{}, err
		}
		r, err := raidwalk.ParseRoster(raw)
		if err != nil {
			return raidSuitePrep{}, err
		}
		rosterTier = r.Tier
	}
	tier, source, err := raidwalk.ResolveTier(p.Name, rosterTier, 0, 0)
	if err != nil {
		return raidSuitePrep{}, err
	}
	var rows planexport.Rows
	if e.SQL != "" {
		rows, _, err = readRaidMigration(slots.ExpandTilde(e.SQL), p.Name)
		if err != nil {
			return raidSuitePrep{}, err
		}
	} else {
		res, err := planexport.Build(p, planexport.Options{})
		if err != nil {
			return raidSuitePrep{}, fmt.Errorf("plan %s: %v", p.Name, err)
		}
		rows = res.Rows
	}
	until, err := raidwalk.FirstLegUntil(rows)
	if err != nil {
		return raidSuitePrep{}, err
	}
	return raidSuitePrep{plan: p, tier: tier, source: source, until: until}, nil
}

// dryLine is one raid of --dry-run: its tier, source and until, and the walk dir it would get.
func (s *raidSuite) dryLine(e raidSuiteEntry) string {
	prep, err := raidSuitePrepare(e)
	if err != nil {
		return fmt.Sprintf("%s: refused: %v", e.Plan, err)
	}
	dir := filepath.Join(s.history, e.Plan, time.Now().Format(raidSuiteStamp))
	return fmt.Sprintf("%s tier %d (%s) until %s out %s", e.Plan, prep.tier, prep.source, prep.until, dir)
}

// raid walks one raid (or compares it with the history in --compare-only) and returns its report line. The bool is
// whether a walk ran (the gap follows it).
func (s *raidSuite) raid(ctx context.Context, e raidSuiteEntry) (raidSuiteRaidReport, bool) {
	rep := raidSuiteRaidReport{Plan: e.Plan, Info: []string{}, StepDeltas: []raidwalk.StepDelta{},
		Regressions: []raidwalk.Regression{}, Notes: []string{}}
	prep, err := raidSuitePrepare(e)
	if err != nil {
		rep.Verdict, rep.Reason = suiteRefused, err.Error()
		return rep, false
	}
	rep.Map, rep.Tier, rep.TierSource, rep.Until = prep.plan.Map, prep.tier, prep.source, prep.until
	if s.o.compareOnly {
		s.compareHistory(&rep)
		return rep, false
	}
	if ctx.Err() != nil {
		rep.Verdict, rep.Reason = suiteSkipped, "interrupted"
		return rep, false
	}
	walked := s.walk(ctx, e, prep, &rep)
	return rep, walked
}

// walk runs raid-walk for one raid into <history>/<plan>/<stamp> and judges the summary it wrote.
func (s *raidSuite) walk(ctx context.Context, e raidSuiteEntry, prep raidSuitePrep, rep *raidSuiteRaidReport) bool {
	out := filepath.Join(s.history, e.Plan, time.Now().Format(raidSuiteStamp))
	rep.Out = out
	wo, code := parseRaidWalkArgs(s.walkArgs(e, prep, out), s.stderr)
	if code != exitOK {
		rep.Verdict, rep.Reason = suiteRefused, "raid-walk's flags do not take this raid (see the usage above)"
		return false
	}
	exit := raidSuiteWalk(ctx, wo, s.env, s.deps, s.stderr, s.stderr)
	rep.ExitCode = &exit
	cur, err := readRaidSuiteSummary(out)
	if err != nil {
		rep.Verdict, rep.Reason = suiteFailed, "no summary: "+err.Error()
		return true
	}
	if exit != exitOK {
		verdictOf(rep, cur, "", nil, exit)
		return true
	}
	hist, notes, err := raidwalk.ReadHistory(s.history, e.Plan)
	rep.Notes = append(rep.Notes, notes...)
	if err != nil {
		rep.Verdict, rep.Reason = suiteFailed, err.Error()
		return true
	}
	verdictOf(rep, cur, filepath.Join(out, "summary.json"), hist, exit)
	return true
}

// walkArgs is the raid-walk command line of one raid. raid-walk's own flag parse gives every default, so the suite
// cannot drift from the walk; the suite sets only what a raid needs.
func (s *raidSuite) walkArgs(e raidSuiteEntry, prep raidSuitePrep, out string) []string {
	// The plan is the one positional argument, so it goes last (flag parsing stops at the first non-flag).
	args := []string{"--server", s.o.server, "--holder", "raid-suite-" + e.Plan,
		"--dir", slots.ExpandTilde(e.Dir), "--until", prep.until,
		"--speed", strconv.FormatFloat(s.o.speed, 'f', 1, 64), "--out", out}
	for _, kv := range [][2]string{
		{"--slot", s.o.slot}, {"--sql", e.SQL}, {"--hires", e.Hires},
		{"--dash-url", s.o.dashURL}, {"--dash-user", s.o.dashUser}, {"--dash-pass-file", s.o.dashPassFile},
		{"--mmaps", s.o.mmaps}, {"--slots", s.o.slotsPath}, {"--binary", s.o.binary},
	} {
		if kv[1] != "" {
			args = append(args, kv[0], kv[1])
		}
	}
	return append(args, e.Plan)
}

// compareHistory is --compare-only for one raid: its newest walk in the history against the last good walk before it.
func (s *raidSuite) compareHistory(rep *raidSuiteRaidReport) {
	hist, notes, err := raidwalk.ReadHistory(s.history, rep.Plan)
	rep.Notes = append(rep.Notes, notes...)
	if err != nil {
		rep.Verdict, rep.Reason = suiteFailed, err.Error()
		return
	}
	if len(hist) == 0 {
		rep.Verdict, rep.Reason = suiteSkipped, "no walk in the history"
		return
	}
	last := hist[len(hist)-1]
	exit := last.S.ExitCode
	rep.Out = filepath.Dir(last.Path)
	rep.ExitCode = &exit
	verdictOf(rep, last.S, last.Path, hist[:len(hist)-1], exit)
}

// verdictOf judges a raid by its walk cur (at curPath, exit code exit): failed when it did not exit 0; else compared
// with the last good walk of older (regression or ok), or new when there is none.
func verdictOf(rep *raidSuiteRaidReport, cur *raidwalk.Summary, curPath string, older []raidwalk.SummaryFile, exit int) {
	m := raidwalk.MetricsOf(cur)
	rep.Metrics = &m
	if exit != exitOK {
		rep.Verdict, rep.Reason = suiteFailed, cur.EndReason
		return
	}
	base := raidwalk.LastGood(older, cur, curPath)
	if base == nil {
		rep.Verdict = suiteNew
		return
	}
	c := raidwalk.Compare(base.S, cur)
	bm := c.Base
	rep.Baseline = &raidSuiteBaseline{Out: filepath.Dir(base.Path), Started: base.S.Started, ExitCode: base.S.ExitCode}
	rep.BaselineMetrics = &bm
	rep.Regressions = append(rep.Regressions, c.Regressions...)
	rep.Info = append(rep.Info, c.Info...)
	rep.StepDeltas = append(rep.StepDeltas, c.StepDeltas...)
	rep.Notes = append(rep.Notes, c.Notes...)
	rep.Verdict = suiteOK
	if len(c.Regressions) > 0 {
		rep.Verdict = suiteRegression
		rep.Reason = strings.Join(regressionWords(c.Regressions), ", ")
	}
}

// regressionWords names each regression with its two values: "time 412s vs 301s (+37%)", "pauses 2 vs 0".
func regressionWords(rs []raidwalk.Regression) []string {
	var out []string
	for _, r := range rs {
		switch r.Metric {
		case "time":
			pct := ""
			if r.Base > 0 {
				pct = fmt.Sprintf(" (%+.0f%%)", (r.New-r.Base)/r.Base*100)
			}
			out = append(out, fmt.Sprintf("time %.0fs vs %.0fs%s", r.New, r.Base, pct))
		case "end":
			out = append(out, r.Rule)
		default:
			out = append(out, fmt.Sprintf("%s %.0f vs %.0f", r.Metric, r.New, r.Base))
		}
	}
	return out
}

// readRaidSuiteSummary reads the summary.json a walk wrote into its out dir.
func readRaidSuiteSummary(out string) (*raidwalk.Summary, error) {
	raw, err := os.ReadFile(filepath.Join(out, "summary.json"))
	if err != nil {
		return nil, err
	}
	var s raidwalk.Summary
	if err := json.Unmarshal(raw, &s); err != nil {
		return nil, err
	}
	return &s, nil
}

// textLine is one raid's line of suite.txt and stdout.
func (r raidSuiteRaidReport) textLine() string {
	switch r.Verdict {
	case suiteRefused, suiteSkipped:
		return fmt.Sprintf("%s: %s: %s", r.Plan, r.Verdict, r.Reason)
	}
	var body string
	switch r.Verdict {
	case suiteRegression:
		body = "REGRESSION " + r.Reason
	case suiteFailed:
		body = "failed"
		if r.ExitCode != nil {
			body = fmt.Sprintf("failed (exit %d)", *r.ExitCode)
		}
		body += ": " + r.Reason
	default:
		body = r.Verdict
	}
	line := fmt.Sprintf("%s t%d until %s: %s", r.Plan, r.Tier, r.Until, body)
	if r.Out != "" {
		line += "; out " + r.Out
	}
	return line
}

// raidSuiteText is suite.txt: one line per raid (its notes and info indented under it), then the counts.
func raidSuiteText(rep raidSuiteReport) string {
	var b strings.Builder
	for _, r := range rep.Raids {
		b.WriteString(r.textLine() + "\n")
		for _, s := range r.Info {
			fmt.Fprintf(&b, "  info: %s\n", s)
		}
		for _, n := range r.Notes {
			fmt.Fprintf(&b, "  note: %s\n", n)
		}
	}
	c := rep.Counts
	fmt.Fprintf(&b, "counts: ok %d, regression %d, new %d, failed %d, refused %d, skipped %d\n",
		c.OK, c.Regression, c.New, c.Failed, c.Refused, c.Skipped)
	return b.String()
}

// countSuite counts the verdicts.
func countSuite(raids []raidSuiteRaidReport) raidSuiteCounts {
	var c raidSuiteCounts
	for _, r := range raids {
		switch r.Verdict {
		case suiteOK:
			c.OK++
		case suiteRegression:
			c.Regression++
		case suiteNew:
			c.New++
		case suiteFailed:
			c.Failed++
		case suiteRefused:
			c.Refused++
		case suiteSkipped:
			c.Skipped++
		}
	}
	return c
}

// raidSuiteExit is 24 when any raid regressed or failed, else 0 (spec 2.1).
func raidSuiteExit(rep raidSuiteReport) int {
	if rep.Counts.Regression > 0 || rep.Counts.Failed > 0 {
		return exitRaidSuiteRegression
	}
	return exitOK
}

// writeReport writes suite.json and suite.txt into the out dir (0600).
func (s *raidSuite) writeReport(rep raidSuiteReport, text string) error {
	raw, err := json.MarshalIndent(rep, "", "  ")
	if err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(s.out, "suite.json"), append(raw, '\n'), 0o600); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(s.out, "suite.txt"), []byte(text), 0o600)
}
