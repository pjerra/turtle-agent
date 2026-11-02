// decisions_cmd.go: `turtle-agent decisions pull` fetches decision captures from the dashboard
// into <out>/decisions. It reuses the walk's pull logic (pullDecisionsInto) so the standalone
// command and the walk pull the same files. The password never reaches an error text or stdout.
package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"time"

	"github.com/pjerra/turtle-agent/internal/tbdash"
)

const decisionsPullUsage = `usage: turtle-agent decisions pull --dash-url URL --dash-user U --dash-pass-file FILE --commander C [--since T] [--until T] --out DIR [--max-mib N]`

// decisionsPullOptions are the pull's flags.
type decisionsPullOptions struct {
	dashURL  string
	dashUser string
	passFile string
	command  string
	since    string
	until    string
	out      string
	maxMiB   int
}

// decisionsCmd is the `turtle-agent decisions` entry.
func decisionsCmd(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 || args[0] != "pull" {
		fmt.Fprintln(stderr, decisionsPullUsage)
		return exitUsage
	}
	o, code := parseDecisionsPullArgs(args[1:], stderr)
	if code != exitOK {
		return code
	}
	since, until, ok := parseDecisionsWindow(o, stderr)
	if !ok {
		return exitUsage
	}
	c, err := openDecisionsClient(o.dashURL, o.dashUser, o.passFile)
	if err != nil {
		fmt.Fprintf(stderr, "decisions: %v\n", err)
		return exitRaidWalkFailed
	}
	if err := c.Login(ctx); err != nil {
		fmt.Fprintf(stderr, "decisions: %v\n", sanitizeDecisionsErr(err))
		return exitRaidWalkFailed
	}
	maxBytes := int64(o.maxMiB) * 1024 * 1024
	pullCtx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()
	res, notes := pullDecisionsInto(pullCtx, c, o.command, since, until, o.out, maxBytes)
	for _, n := range notes {
		fmt.Fprintf(stderr, "decisions: %s\n", n)
	}
	fmt.Fprintf(stdout, "decisions: pulled %d captures (%d bytes) into %s/decisions\n", len(res.Captures), res.Bytes, o.out)
	if res.BudgetHit {
		fmt.Fprintf(stderr, "decisions: budget stopped the pull at %d MiB\n", o.maxMiB)
	}
	fmt.Fprintf(stdout, "decisions: %s\n", res.Note)
	return exitOK
}

func parseDecisionsPullArgs(args []string, stderr io.Writer) (decisionsPullOptions, int) {
	var o decisionsPullOptions
	fs := flag.NewFlagSet("decisions-pull", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.Usage = func() { fmt.Fprintln(stderr, decisionsPullUsage) }
	fs.StringVar(&o.dashURL, "dash-url", "", "the dashboard URL")
	fs.StringVar(&o.dashUser, "dash-user", "TADASH", "the dashboard login")
	fs.StringVar(&o.passFile, "dash-pass-file", "", "the dashboard password file")
	fs.StringVar(&o.command, "commander", "", "the commander to pull")
	fs.StringVar(&o.since, "since", "", "RFC3339 start of the window")
	fs.StringVar(&o.until, "until", "", "RFC3339 end of the window")
	fs.StringVar(&o.out, "out", "", "the output dir")
	fs.IntVar(&o.maxMiB, "max-mib", decisionsBudgetDefaultMiB, "the pull budget in MiB")
	if err := fs.Parse(args); err != nil {
		return o, exitUsage
	}
	if fs.NArg() != 0 {
		fmt.Fprintf(stderr, "decisions: unexpected argument %q\n", fs.Arg(0))
		fmt.Fprintln(stderr, decisionsPullUsage)
		return o, exitUsage
	}
	var missing []string
	if o.dashURL == "" {
		missing = append(missing, "--dash-url")
	}
	if o.passFile == "" {
		missing = append(missing, "--dash-pass-file")
	}
	if o.command == "" {
		missing = append(missing, "--commander")
	}
	if o.out == "" {
		missing = append(missing, "--out")
	}
	if len(missing) > 0 {
		fmt.Fprintf(stderr, "decisions: missing %s\n", joinWords(missing))
		fmt.Fprintln(stderr, decisionsPullUsage)
		return o, exitUsage
	}
	if o.maxMiB <= 0 {
		fmt.Fprintf(stderr, "decisions: --max-mib must be above 0\n")
		return o, exitUsage
	}
	return o, exitOK
}

// openDecisionsClient builds the dashboard client; a var so tests hand in a fake.
var openDecisionsClient = func(base, user, passFile string) (decisionsPuller, error) {
	return tbdash.New(base, user, passFile)
}

// sanitizeDecisionsErr drops nothing sensitive: the dashboard client never carries the password
// into an error, so this is the identity. It exists so a future message cannot leak it by accident.
func sanitizeDecisionsErr(err error) error { return err }

func parseDecisionsWindow(o decisionsPullOptions, stderr io.Writer) (time.Time, time.Time, bool) {
	var since, until time.Time
	if o.since != "" {
		t, err := time.Parse(time.RFC3339, o.since)
		if err != nil {
			fmt.Fprintf(stderr, "decisions: --since wants RFC3339, not %q\n", o.since)
			return since, until, false
		}
		since = t
	}
	if o.until != "" {
		t, err := time.Parse(time.RFC3339, o.until)
		if err != nil {
			fmt.Fprintf(stderr, "decisions: --until wants RFC3339, not %q\n", o.until)
			return since, until, false
		}
		until = t
	}
	return since, until, true
}

func joinWords(words []string) string {
	out := ""
	for i, word := range words {
		if i > 0 {
			out += ", "
		}
		out += word
	}
	return out
}
