package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"strings"

	"github.com/pjerra/turtle-agent/internal/preflight"
)

const walkPreflightUsage = "usage: turtle-agent walk-preflight [--server NAME]... [--json] <walk-dir>"

// walkPreflightCmd checks a raid-walk directory before it starts: exit 0 all OK, 1 warnings only, 2 any FAIL. It
// writes nothing, so it is safe beside a running walk (README, "Walk preflight").
func walkPreflightCmd(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("walk-preflight", flag.ContinueOnError)
	fs.SetOutput(stderr)
	var servers stringList
	fs.Var(&servers, "server", "check only the commands of this server (repeatable, or a comma list)")
	asJSON := fs.Bool("json", false, "print the report as JSON")
	if err := fs.Parse(args); err != nil {
		return exitUsage
	}
	if fs.NArg() != 1 {
		fmt.Fprintln(stderr, walkPreflightUsage)
		return exitUsage
	}
	var filter []string
	for _, s := range servers {
		for _, part := range strings.Split(s, ",") {
			if part = strings.TrimSpace(part); part != "" {
				filter = append(filter, part)
			}
		}
	}
	rep, err := preflight.Run(ctx, preflight.Options{Dir: fs.Arg(0), Servers: filter})
	if err != nil {
		fmt.Fprintf(stderr, "walk-preflight: %v\n", err)
		return exitUsage
	}
	if *asJSON {
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
