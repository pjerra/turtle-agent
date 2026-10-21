package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/pjerra/turtle-agent/internal/trace"
	"github.com/pjerra/turtle-agent/internal/tracecheck"
)

// exitTraceFail: trace-score found a failed check.
const exitTraceFail = 11

const traceScoreUsage = "usage: turtle-agent trace-score [--json] [--summary] [--set check=threshold]... <trace.jsonl>...\n" +
	"  scores run traces (README \"Run traces and checks\"); exit 11 when a check failed, 0 otherwise"

type thresholdFlags []string

func (t *thresholdFlags) String() string     { return strings.Join(*t, ",") }
func (t *thresholdFlags) Set(v string) error { *t = append(*t, v); return nil }

// traceScoreCmd reads trace files and scores them again with the current checks: an old trace can be re-scored after a threshold or
// a check changes. It reads nothing but the files.
func traceScoreCmd(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("trace-score", flag.ContinueOnError)
	fs.SetOutput(stderr)
	asJSON := fs.Bool("json", false, "print the verdicts as JSON (one object per trace)")
	summary := fs.Bool("summary", false, "write <run_id>.summary.json beside each trace")
	var sets thresholdFlags
	fs.Var(&sets, "set", "change a threshold: check=value (rest_share, stale_party, walk_stops, threat_off_tank, order_refusals, feed_stalls, stuck, facing, and the spell warnings idle_share, interrupt_cover)")
	if err := fs.Parse(args); err != nil {
		fmt.Fprintln(stderr, traceScoreUsage)
		return exitUsage
	}
	if fs.NArg() == 0 {
		fmt.Fprintln(stderr, traceScoreUsage)
		return exitUsage
	}
	cfg := tracecheck.Defaults()
	for _, s := range sets {
		k, v, ok := strings.Cut(s, "=")
		n, err := strconv.ParseFloat(v, 64)
		if !ok || err != nil {
			fmt.Fprintf(stderr, "--set %q: want check=number\n", s)
			return exitUsage
		}
		if err := cfg.Set(k, n); err != nil {
			fmt.Fprintln(stderr, "--set:", err)
			return exitUsage
		}
	}
	code := exitOK
	for _, path := range fs.Args() {
		f, err := trace.Read(path)
		if err != nil {
			fmt.Fprintf(stderr, "%s: %v\n", path, err)
			return exitUsage
		}
		checks := tracecheck.Score(f, cfg)
		verdict, failed := trace.Verdict(checks)
		if verdict == trace.Fail {
			code = exitTraceFail
		}
		if *summary {
			var size int64
			if st, err := os.Stat(path); err == nil {
				size = st.Size()
			}
			if err := trace.WriteSummary(path, trace.SummaryOf(f, size, checks, time.Now())); err != nil {
				fmt.Fprintf(stderr, "%s: summary: %v\n", path, err)
				return exitUsage
			}
		}
		report := tracecheck.Spells(f, cfg)
		if *asJSON {
			_ = json.NewEncoder(stdout).Encode(map[string]any{"run": f.Header.Run, "file": path, "verdict": verdict, "failed": failed, "checks": checks, "spells": report})
			continue
		}
		fmt.Fprintf(stdout, "%s  %s %s %s  %.0f s  %s\n", f.Header.Run, f.Header.Source, f.Header.Mode, f.Header.Plan, f.Seconds(), strings.ToUpper(verdict))
		for _, c := range checks {
			fmt.Fprintf(stdout, "  %-4s %-16s %s\n", strings.ToUpper(c.Verdict), c.ID, describeCheck(c))
		}
		for _, l := range report.Lines() {
			fmt.Fprintln(stdout, l)
		}
	}
	return code
}

func describeCheck(c trace.Check) string {
	if c.Verdict == trace.NA {
		return "n/a: " + c.Detail
	}
	if c.ID == tracecheck.IDSpells { // report only: the detail is the line, the section below has the tables
		return "report: " + c.Detail
	}
	return fmt.Sprintf("%g %s (limit %g): %s", c.Value, c.Unit, c.Threshold, c.Detail)
}
