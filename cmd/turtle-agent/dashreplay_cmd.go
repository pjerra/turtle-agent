package main

import (
	"bufio"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/pjerra/turtle-agent/internal/dashreplay"
)

// exitReplayRefused: dashboard-replay will not send to a target that is not a
// loopback address (and --i-know did not vouch for it).
const exitReplayRefused = 17

const dashReplayUsage = `usage:
  turtle-agent dashboard-replay <recording-dir|file.jsonl> [--to host:port] [--speed N] [--max-gap D] [--loop] [--dry-run] [--i-know]
  turtle-agent dashboard-replay convert <api-dump-dir> [--out <dir>]
  feeds a recording to a TortoiseBots observability daemon's UDP port (README "Dashboard replay"): <dir>/replay.jsonl,
  or the API dumps in a folder that has none, converted in memory. A replay's session is newer than a running
  module's, so a replay overwrites what a live daemon shows until the module restarts: point it at a test daemon.
  It logs in to nothing and reads no settings or token file.`

// dashboardReplayCmd replays a dashboard recording into a daemon's UDP port,
// or converts API dumps to a replay file.
func dashboardReplayCmd(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	if len(args) > 0 && args[0] == "convert" {
		return dashReplayConvertCmd(args[1:], stdout, stderr)
	}
	return dashReplayPlayCmd(ctx, args, stdout, stderr)
}

func dashReplayPlayCmd(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("dashboard-replay", flag.ContinueOnError)
	fs.SetOutput(stderr)
	to := fs.String("to", dashreplay.DefaultTarget, "the observability daemon's UDP address")
	speed := fs.Float64("speed", 1, "play the recording this many times faster than recorded (> 0)")
	maxGap := fs.Duration("max-gap", 10*time.Second, "longest pause between two datagrams (kept under the daemon's 12 s commanders staleness)")
	loop := fs.Bool("loop", false, "start over with a newer session after the last datagram")
	dryRun := fs.Bool("dry-run", false, "check the target and print the schedule; send nothing")
	iKnow := fs.Bool("i-know", false, "allow a non-loopback target (a test dashboard): a production daemon must not take replays")
	pos, rest := splitPositionals(fs, args, 1)
	if err := fs.Parse(rest); err != nil {
		fmt.Fprintln(stderr, dashReplayUsage)
		return exitUsage
	}
	if len(pos) != 1 || fs.NArg() != 0 {
		fmt.Fprintln(stderr, dashReplayUsage)
		return exitUsage
	}
	if *speed <= 0 {
		fmt.Fprintln(stderr, "dashboard-replay: --speed must be > 0")
		return exitUsage
	}
	if *maxGap <= 0 {
		fmt.Fprintln(stderr, "dashboard-replay: --max-gap must be > 0")
		return exitUsage
	}
	// The target is checked first: a replay that must not go out is refused
	// before anything is read.
	warn, err := dashreplay.CheckTarget(*to, *iKnow)
	if err != nil {
		fmt.Fprintln(stderr, err)
		var refused *dashreplay.TargetRefusedError
		if errors.As(err, &refused) {
			return exitReplayRefused
		}
		return exitUsage
	}
	if warn != "" {
		fmt.Fprintln(stderr, warn)
	}
	lines, converted, err := loadReplay(pos[0])
	if err != nil {
		fmt.Fprintln(stderr, "dashboard-replay:", err)
		return exitUsage
	}
	if converted {
		fmt.Fprintf(stderr, "converted from API dumps: %d datagrams\n", len(lines))
	}
	if *dryRun {
		total, byType := dashreplay.Plan(lines, *speed, *maxGap)
		fmt.Fprintf(stdout, "%d datagrams over %.1f s at speed %g: %s\n", len(lines), total.Seconds(), *speed, dashreplay.TypeSummary(byType))
		return exitOK
	}
	opts := dashreplay.Options{To: *to, Speed: *speed, MaxGap: *maxGap, Loop: *loop, IKnow: *iKnow}
	stats, err := dashreplay.Play(ctx, lines, opts, stdout)
	if err != nil && !errors.Is(err, context.Canceled) {
		fmt.Fprintln(stderr, "dashboard-replay:", err)
		return exitUsage
	}
	fmt.Fprintf(stdout, "sent %d datagrams (%s) in %d loop(s), failed %d, skipped %d\n",
		stats.Sent, dashreplay.TypeSummary(stats.ByType), stats.Loops, stats.Failed, stats.Skipped)
	return exitOK
}

// loadReplay reads path/replay.jsonl or the file path, or converts the API
// dumps in path when the folder has none (converted reports which happened).
func loadReplay(path string) (lines []dashreplay.Line, converted bool, err error) {
	info, err := os.Stat(path)
	if err != nil {
		return nil, false, err
	}
	if !info.IsDir() {
		lines, err := readReplayFile(path)
		return lines, false, err
	}
	file := filepath.Join(path, "replay.jsonl")
	if _, err := os.Stat(file); err == nil {
		lines, err := readReplayFile(file)
		return lines, false, err
	}
	_, lines, err = dashreplay.ConvertDumps(path)
	if err != nil {
		return nil, false, err
	}
	return lines, true, nil
}

func readReplayFile(path string) ([]dashreplay.Line, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	_, lines, err := dashreplay.Read(f)
	if err != nil {
		return nil, fmt.Errorf("%s: %v", path, err)
	}
	return lines, nil
}

func dashReplayConvertCmd(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("dashboard-replay convert", flag.ContinueOnError)
	fs.SetOutput(stderr)
	out := fs.String("out", "", "folder for replay.jsonl (default: the dump folder)")
	pos, rest := splitPositionals(fs, args, 1)
	if err := fs.Parse(rest); err != nil || len(pos) != 1 || fs.NArg() != 0 {
		fmt.Fprintln(stderr, dashReplayUsage)
		return exitUsage
	}
	header, lines, err := dashreplay.ConvertDumps(pos[0])
	if err != nil {
		fmt.Fprintln(stderr, "dashboard-replay convert:", err)
		return exitUsage
	}
	dir := *out
	if dir == "" {
		dir = pos[0]
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		fmt.Fprintln(stderr, "dashboard-replay convert:", err)
		return exitUsage
	}
	path := filepath.Join(dir, "replay.jsonl")
	if err := writeReplayFile(path, header, lines); err != nil {
		fmt.Fprintln(stderr, "dashboard-replay convert:", err)
		return exitUsage
	}
	fmt.Fprintf(stdout, "wrote %s: %d datagrams (%s)\n", path, len(lines), dashreplay.TypeSummary(header.ByType))
	return exitOK
}

// writeReplayFile writes a replay file mode 0600 through a temp file in the
// same folder: a reader never sees half a file, and an older file's looser
// mode never survives the rewrite.
func writeReplayFile(path string, h dashreplay.Header, lines []dashreplay.Line) error {
	tmp := path + ".tmp"
	f, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	fail := func(err error) error {
		f.Close()
		os.Remove(tmp)
		return err
	}
	// O_TRUNC on an existing temp file keeps that file's mode.
	if err := f.Chmod(0o600); err != nil {
		return fail(err)
	}
	w := bufio.NewWriter(f)
	if err := dashreplay.Write(w, h, lines); err != nil {
		return fail(err)
	}
	if err := w.Flush(); err != nil {
		return fail(err)
	}
	if err := f.Close(); err != nil {
		os.Remove(tmp)
		return err
	}
	return os.Rename(tmp, path)
}
