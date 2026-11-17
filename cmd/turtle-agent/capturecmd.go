package main

import (
	"bufio"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/TortoiseBots/turtle-agent/internal/capture"
	"github.com/TortoiseBots/turtle-agent/internal/config"
	"github.com/TortoiseBots/turtle-agent/internal/events"
	"github.com/TortoiseBots/turtle-agent/internal/opcode"
	"github.com/TortoiseBots/turtle-agent/internal/session"
)

// scrubCmd fails when a capture contains an account name (upper case, as on the wire)
// or a password from the settings file, in any form capture.Variants lists. Run it on
// every fixture before committing. A secret too short to prove anything is an error.
func scrubCmd(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("scrub-check", flag.ContinueOnError)
	fs.SetOutput(stderr)
	cfgPath := fs.String("config", config.DefaultPath(), "settings file whose accounts and passwords must not appear")
	if err := fs.Parse(args); err != nil {
		return exitUsage
	}
	if fs.NArg() == 0 {
		fmt.Fprintln(stderr, "usage: turtle-agent scrub-check [--config FILE] <capture>...")
		return exitUsage
	}
	st, err := config.Load(*cfgPath)
	if err != nil {
		fmt.Fprintln(stderr, "config:", err)
		return exitUsage
	}
	// Three needles per account, in this order: Finding.Needle%3 == 0 is the account name.
	names := make([]string, 0, len(st.Accounts))
	for name := range st.Accounts {
		names = append(names, name)
	}
	sort.Strings(names)
	var needles [][]byte
	for _, name := range names {
		pw := st.Accounts[name]
		needles = append(needles, []byte(strings.ToUpper(name)), []byte(pw), []byte(strings.ToUpper(pw)))
	}
	if len(needles) == 0 {
		fmt.Fprintln(stderr, "scrub-check: the settings file has no accounts, so there is nothing to check against")
		return exitUsage
	}
	code := exitOK
	for _, path := range fs.Args() {
		recs, err := capture.ReadFile(path)
		if err != nil {
			fmt.Fprintln(stderr, err)
			return exitUsage
		}
		found, err := capture.Scan(recs, needles...)
		if err != nil {
			// The error names a needle index, never the secret.
			fmt.Fprintln(stderr, "scrub-check:", err)
			return exitUsage
		}
		for _, f := range found {
			what := "a password"
			if f.Needle%3 == 0 {
				what = "an account name"
			}
			fmt.Fprintf(stdout, "%s: record %d (%s) contains %s from the settings file\n", path, f.Index, opcode.Name(f.Op), what)
		}
		if len(found) > 0 {
			code = exitScrub
			continue
		}
		fmt.Fprintf(stdout, "%s: clean, %d records\n", path, len(recs))
	}
	return code
}

func statsCmd(args []string, stdout, stderr io.Writer) int {
	if len(args) != 1 {
		fmt.Fprintln(stderr, "usage: turtle-agent capture-stats <capture>")
		return exitUsage
	}
	recs, err := capture.ReadFile(args[0])
	if err != nil {
		fmt.Fprintln(stderr, err)
		return exitUsage
	}
	st := capture.Stats(recs)
	names := make([]string, 0, len(st))
	for n := range st {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		fmt.Fprintf(stdout, "%-36s %d\n", n, st[n])
	}
	fmt.Fprintf(stdout, "%-36s %d\n", "TOTAL", len(recs))
	return exitOK
}

// captureCmd runs the capture subcommands.
func captureCmd(args []string, stdout, stderr io.Writer) int {
	if len(args) > 0 && args[0] == "events" {
		return captureEventsCmd(args[1:], stdout, stderr)
	}
	fmt.Fprintln(stderr, captureEventsUsage)
	return exitUsage
}

const captureEventsUsage = "usage: turtle-agent capture events [--from RFC3339] [--to RFC3339] [--kinds a,b] [--out FILE [--force]] <capture>"

// captureEventsCmd decodes a capture into the events the `events` RPC returned while it
// was recorded, one JSON event per line: the capture's server packets go through the
// session's own handlers (session.Replay), with seq and time following the capture.
func captureEventsCmd(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("capture events", flag.ContinueOnError)
	fs.SetOutput(stderr)
	from := fs.String("from", "", "only events at or after this time (RFC 3339)")
	to := fs.String("to", "", "only events at or before this time (RFC 3339)")
	kinds := fs.String("kinds", "", "only these event kinds, comma separated")
	out := fs.String("out", "", "write the events to this file (mode 0600, never overwrites unless --force) instead of stdout")
	force := fs.Bool("force", false, "let --out replace an existing file (never a capture)")
	// Flags may follow the capture path: parse again after each positional argument.
	var files []string
	for rest := args; ; {
		if err := fs.Parse(rest); err != nil {
			return exitUsage
		}
		if fs.NArg() == 0 {
			break
		}
		files = append(files, fs.Arg(0))
		rest = fs.Args()[1:]
	}
	if len(files) != 1 {
		fmt.Fprintln(stderr, captureEventsUsage)
		return exitUsage
	}
	var tFrom, tTo time.Time
	var err error
	if *from != "" {
		if tFrom, err = time.Parse(time.RFC3339, *from); err != nil {
			fmt.Fprintln(stderr, "capture events: --from:", err)
			return exitUsage
		}
	}
	if *to != "" {
		if tTo, err = time.Parse(time.RFC3339, *to); err != nil {
			fmt.Fprintln(stderr, "capture events: --to:", err)
			return exitUsage
		}
	}
	want := map[string]bool{}
	for _, k := range strings.Split(*kinds, ",") {
		if k = strings.TrimSpace(k); k != "" {
			want[k] = true
		}
	}
	// A capture of an agent that was killed ends in a cut-off record: decode what is there.
	recs, err := capture.ReadFilePartial(files[0])
	if err != nil {
		if len(recs) == 0 {
			fmt.Fprintln(stderr, err)
			return exitUsage
		}
		fmt.Fprintf(stderr, "capture events: warning: %v; decoding the %d records before it\n", err, len(recs))
	}
	log, rep, err := session.Replay(recs, session.Options{})
	if err != nil {
		fmt.Fprintln(stderr, "capture events:", err)
		return exitUsage
	}
	w := stdout
	if *out != "" {
		f, err := openEventsOut(*out, files[0], *force)
		if err != nil {
			fmt.Fprintln(stderr, "capture events:", err)
			return exitUsage
		}
		defer f.Close()
		w = f
	}
	bw := bufio.NewWriter(w)
	enc := json.NewEncoder(bw)
	enc.SetEscapeHTML(false)
	written, byKind := 0, map[string]int{}
	for since := uint64(0); ; {
		page := log.Read(events.Query{Since: since, Limit: events.MaxLimit})
		for _, e := range page.Events {
			if (len(want) > 0 && !want[e.Kind]) || (!tFrom.IsZero() && e.Time.Before(tFrom)) || (!tTo.IsZero() && e.Time.After(tTo)) {
				continue
			}
			if err := enc.Encode(e); err != nil {
				fmt.Fprintln(stderr, "capture events:", err)
				return exitUsage
			}
			written++
			byKind[e.Kind]++
		}
		since = page.Next
		if !page.More {
			break
		}
	}
	if err := bw.Flush(); err != nil {
		fmt.Fprintln(stderr, "capture events:", err)
		return exitUsage
	}
	fmt.Fprintf(stderr, "capture events: %d of %d events for %s, from %d records (%d redacted, %d server packets without a handler)\n",
		written, log.Last(), rep.Character, rep.Records, rep.Redacted, sumCounts(rep.Unknown))
	kinds2 := make([]string, 0, len(byKind))
	for k, n := range byKind {
		kinds2 = append(kinds2, fmt.Sprintf("%s=%d", k, n))
	}
	sort.Strings(kinds2)
	fmt.Fprintf(stderr, "capture events: kinds: %s\n", strings.Join(kinds2, " "))
	if rep.Ended != "" {
		fmt.Fprintln(stderr, "capture events: replay stopped early:", rep.Ended)
	}
	return exitOK
}

// openEventsOut creates the --out file, mode 0600. It never touches the input capture or any
// other capture file, and replaces an existing file only with --force.
func openEventsOut(path, input string, force bool) (*os.File, error) {
	if a, err := os.Stat(path); err == nil {
		if b, err := os.Stat(input); err == nil && os.SameFile(a, b) {
			return nil, errors.New("--out is the capture itself")
		}
		if capture.IsCaptureFile(path) {
			return nil, fmt.Errorf("--out %s is a capture file; it is never overwritten", path)
		}
		if !force {
			return nil, fmt.Errorf("--out %s exists (--force replaces it)", path)
		}
	}
	flags := os.O_WRONLY | os.O_CREATE | os.O_EXCL
	if force {
		flags = os.O_WRONLY | os.O_CREATE | os.O_TRUNC
	}
	f, err := os.OpenFile(path, flags, 0o600)
	if err != nil {
		return nil, err
	}
	if err := f.Chmod(0o600); err != nil { // an existing file keeps its mode through O_TRUNC
		f.Close()
		return nil, err
	}
	return f, nil
}

func sumCounts(m map[string]int) (n int) {
	for _, v := range m {
		n += v
	}
	return n
}
