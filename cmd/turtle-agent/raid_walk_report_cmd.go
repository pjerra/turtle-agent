package main

import (
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/pjerra/turtle-agent/internal/raidwalk"
)

const raidWalkReportUsage = `usage: turtle-agent raid-walk report <dir> [--spell ID[,ID]...] [--window 5s] [--clock-offset 1h]
    [--log FILE] [--fights FILE] [--md FILE] [--json FILE] [--out DIR]
  reads what a raid walk saved in <dir> (combat-fight-*.json or fights-full.json, and run.log) and writes the per-pull
  table: step, fight, engaged units, deaths, contact before the pull order, neighbours that joined mid-fight, stand-pull
  waits, pauses/stops/resumes, and the deaths within --window of each cast of the watched --spell ids.
  --clock-offset is how far the run log's clock is ahead of UTC (the servers run UTC+1: 1h); the report prints UTC.
  Markdown goes to stdout unless --md/--json/--out name files (--out DIR writes report.md and report.json; --json - prints the JSON).
  It reads files only: no server, no login, no settings or token file.`

// raidWalkReportCmd is `turtle-agent raid-walk report <dir>`.
func raidWalkReportCmd(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("raid-walk report", flag.ContinueOnError)
	fs.SetOutput(stderr)
	var spells stringList
	fs.Var(&spells, "spell", "an enemy spell id to list the casts of (repeatable, or a comma list)")
	window := fs.Duration("window", raidwalk.DefaultCastWindow, "a death this soon after a watched cast counts as its death")
	offset := fs.Duration("clock-offset", 0, "the run log's clock is UTC plus this (the servers: 1h)")
	logPath := fs.String("log", "", "the run log (default <dir>/run.log)")
	fightsPath := fs.String("fights", "", "a file of many fights (id -> fight, or a list) in place of <dir>/combat-fight-*.json")
	mdPath := fs.String("md", "", "write the Markdown here")
	jsonPath := fs.String("json", "", "write the JSON here (- : print it instead of the Markdown)")
	outDir := fs.String("out", "", "write report.md and report.json into this folder")
	fs.Usage = func() { fmt.Fprintln(stderr, raidWalkReportUsage) }
	pos, rest := splitPositionals(fs, args, 1)
	if err := fs.Parse(rest); err != nil {
		return exitUsage
	}
	if len(pos) != 1 || fs.NArg() != 0 {
		fmt.Fprintln(stderr, raidWalkReportUsage)
		return exitUsage
	}
	dir := pos[0]
	if *window <= 0 {
		fmt.Fprintln(stderr, "raid-walk report: --window must be positive")
		return exitUsage
	}
	ids, err := parseSpellIDs(spells)
	if err != nil {
		fmt.Fprintf(stderr, "raid-walk report: %v\n", err)
		return exitUsage
	}

	fights, err := readReportFights(dir, *fightsPath)
	if err != nil {
		fmt.Fprintf(stderr, "raid-walk report: %v\n", err)
		return exitRaidWalkFailed
	}
	var lines []raidwalk.LogLine
	lp := *logPath
	if lp == "" {
		lp = filepath.Join(dir, "run.log")
	}
	var notes []string
	if f, err := os.Open(lp); err == nil {
		lines, err = raidwalk.ParseRunLog(f)
		f.Close()
		if err != nil {
			fmt.Fprintf(stderr, "raid-walk report: %s: %v\n", lp, err)
			return exitRaidWalkFailed
		}
	} else if *logPath != "" {
		fmt.Fprintf(stderr, "raid-walk report: %v\n", err)
		return exitRaidWalkFailed
	}
	if len(fights) > 0 {
		first := fights[0].Start
		for _, f := range fights {
			first = min(first, f.Start)
		}
		raidwalk.ResolveLog(lines, *offset, time.Unix(int64(first), 0).UTC())
	}
	rep := raidwalk.BuildReport(fights, lines, raidwalk.ReportOptions{Spells: ids, Window: *window, ClockOffset: *offset})
	rep.Source = dir
	rep.Notes = append(rep.Notes, notes...)
	if len(fights) == 0 {
		rep.Notes = append(rep.Notes, "no fights found")
	}

	md := rep.Markdown()
	js, err := rep.JSON()
	if err != nil {
		fmt.Fprintf(stderr, "raid-walk report: %v\n", err)
		return exitRaidWalkFailed
	}
	wrote := false
	write := func(path string, b []byte) bool {
		if err := os.WriteFile(path, b, 0o644); err != nil {
			fmt.Fprintf(stderr, "raid-walk report: %v\n", err)
			return false
		}
		wrote = true
		return true
	}
	if *outDir != "" {
		if err := os.MkdirAll(*outDir, 0o755); err != nil {
			fmt.Fprintf(stderr, "raid-walk report: %v\n", err)
			return exitRaidWalkFailed
		}
		if !write(filepath.Join(*outDir, "report.md"), []byte(md)) || !write(filepath.Join(*outDir, "report.json"), js) {
			return exitRaidWalkFailed
		}
	}
	if *mdPath != "" && !write(*mdPath, []byte(md)) {
		return exitRaidWalkFailed
	}
	switch *jsonPath {
	case "":
	case "-":
		_, _ = stdout.Write(js)
		return exitOK
	default:
		if !write(*jsonPath, js) {
			return exitRaidWalkFailed
		}
	}
	if !wrote {
		_, _ = io.WriteString(stdout, md)
	}
	return exitOK
}

// parseSpellIDs reads the --spell values: ids, repeated or comma separated.
func parseSpellIDs(vals []string) ([]uint32, error) {
	var out []uint32
	seen := map[uint32]bool{}
	for _, v := range vals {
		for _, f := range strings.Split(v, ",") {
			f = strings.TrimSpace(f)
			if f == "" {
				continue
			}
			n, err := strconv.ParseUint(f, 10, 32)
			if err != nil || n == 0 {
				return nil, fmt.Errorf("--spell %q is not a spell id", f)
			}
			if !seen[uint32(n)] {
				seen[uint32(n)] = true
				out = append(out, uint32(n))
			}
		}
	}
	return out, nil
}

// readReportFights reads the fights of a walk folder: --fights, else every combat-fight-*.json, else fights-full.json.
func readReportFights(dir, fightsPath string) ([]raidwalk.ReportFight, error) {
	if fightsPath != "" {
		raw, err := os.ReadFile(fightsPath)
		if err != nil {
			return nil, err
		}
		fs, err := raidwalk.ParseFightSet(raw)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", fightsPath, err)
		}
		return fs, nil
	}
	files, err := filepath.Glob(filepath.Join(dir, "combat-fight-*.json"))
	if err != nil {
		return nil, err
	}
	sort.Strings(files)
	var out []raidwalk.ReportFight
	for _, p := range files {
		raw, err := os.ReadFile(p)
		if err != nil {
			return nil, err
		}
		f, err := raidwalk.ParseFight(raw)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", p, err)
		}
		out = append(out, f)
	}
	if len(out) > 0 {
		return out, nil
	}
	raw, err := os.ReadFile(filepath.Join(dir, "fights-full.json"))
	if err != nil {
		if os.IsNotExist(err) {
			return nil, fmt.Errorf("%s has no combat-fight-*.json and no fights-full.json (pass --fights FILE)", dir)
		}
		return nil, err
	}
	fs, err := raidwalk.ParseFightSet(raw)
	if err != nil {
		return nil, fmt.Errorf("fights-full.json: %w", err)
	}
	return fs, nil
}
