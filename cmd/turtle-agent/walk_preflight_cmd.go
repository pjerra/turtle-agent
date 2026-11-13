package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"strings"

	"github.com/pjerra/turtle-agent/internal/preflight"
)

const walkPreflightUsage = "usage: turtle-agent walk-preflight [--server NAME]... [--binary PATH] [--json] <walk-dir>"

// walkPreflightCmd checks a raid-walk directory before it starts: exit 0 all OK, 1 warnings only, 2 any FAIL. It
// writes nothing, so it is safe beside a running walk (README, "Walk preflight").
func walkPreflightCmd(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	opts, asJSON, code := parsePreflightArgs(args, stderr)
	if code != exitOK {
		return code
	}
	rep, err := preflight.Run(ctx, opts)
	if err != nil {
		fmt.Fprintf(stderr, "walk-preflight: %v\n", err)
		return exitUsage
	}
	if asJSON {
		if err := rep.WriteJSON(stdout); err != nil {
			fmt.Fprintf(stderr, "walk-preflight: %v\n", err)
			return exitUsage
		}
		return rep.ExitCode()
	}
	for _, r := range rep.Results {
		line := fmt.Sprintf("%-4s %-16s", r.Status, r.Check)
		if r.Subject != "" {
			line += " " + r.Subject + ":"
		}
		line += " " + r.Detail
		if r.Fix != "" {
			line += " -- fix: " + r.Fix
		}
		fmt.Fprintln(stdout, line)
	}
	warns, fails := rep.Counts()
	fmt.Fprintf(stdout, "%d check line(s): %d warning(s), %d failure(s)\n", len(rep.Results), warns, fails)
	return rep.ExitCode()
}

// parsePreflightArgs reads walk-preflight's flags: the walk directory, the server filter, --binary and --json.
// --binary is left empty when it is not given: preflight.Run then checks this executable, the binary the autopilot
// launches without its own --binary.
func parsePreflightArgs(args []string, stderr io.Writer) (preflight.Options, bool, int) {
	fs := flag.NewFlagSet("walk-preflight", flag.ContinueOnError)
	fs.SetOutput(stderr)
	var servers stringList
	fs.Var(&servers, "server", "check only the commands of this server (repeatable, or a comma list)")
	binary := fs.String("binary", "", "the turtle-agent to check when a target names none (default: this executable, as raid-autopilot launches)")
	asJSON := fs.Bool("json", false, "print the report as JSON")
	if err := fs.Parse(args); err != nil {
		return preflight.Options{}, false, exitUsage
	}
	if fs.NArg() != 1 {
		fmt.Fprintln(stderr, walkPreflightUsage)
		return preflight.Options{}, false, exitUsage
	}
	var filter []string
	for _, s := range servers {
		for _, part := range strings.Split(s, ",") {
			if part = strings.TrimSpace(part); part != "" {
				filter = append(filter, part)
			}
		}
	}
	return preflight.Options{Dir: fs.Arg(0), Servers: filter, Binary: *binary}, *asJSON, exitOK
}
