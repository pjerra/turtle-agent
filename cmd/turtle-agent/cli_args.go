// Shared argument parsing for the lease, slot setup and teardown
// subcommands: every flag works before, between or after the positionals,
// so every documented flag order parses to the same options
// (cli_args_test.go pins this: restoring the old leading-only split
// breaks it).
package main

import (
	"flag"
	"fmt"
	"io"
	"strings"
)

// isFlagArg reports whether s looks like a flag (as opposed to a
// positional or a lone "-").
func isFlagArg(s string) bool {
	return len(s) > 1 && s[0] == '-' && s != "-"
}

// isBoolFlag reports whether name is defined on fs with fs.Bool: such a
// flag takes no value, so the arg after one is a positional, not its value.
func isBoolFlag(fs *flag.FlagSet, name string) bool {
	f := fs.Lookup(name)
	if f == nil {
		return false
	}
	b, ok := f.Value.(interface{ IsBoolFlag() bool })
	return ok && b.IsBoolFlag()
}

// splitPositionals partitions args into positionals (in order) and flag
// args so flags work before, between or after positionals with the stdlib
// flag package (which stops parsing at the first positional). A boolean
// flag takes no value; any other flag takes its value from the next arg or
// from --flag=value. Unknown flags pass through untouched so fs.Parse still
// reports them. `--` ends flag parsing: everything after it is positional.
// At most n positionals are collected; extras stay in the flag args so
// fs.Parse leaves them in fs.Args() and the caller refuses them as before.
func splitPositionals(fs *flag.FlagSet, args []string, n int) (pos []string, rest []string) {
	addPos := func(s string) {
		if len(pos) < n {
			pos = append(pos, s)
		} else {
			rest = append(rest, s)
		}
	}
	for i := 0; i < len(args); i++ {
		s := args[i]
		if s == "--" {
			for _, p := range args[i+1:] {
				addPos(p)
			}
			break
		}
		if !isFlagArg(s) {
			addPos(s)
			continue
		}
		rest = append(rest, s)
		name := strings.TrimLeft(s, "-")
		if strings.IndexByte(name, '=') < 0 && !isBoolFlag(fs, name) {
			if i+1 < len(args) {
				i++
				rest = append(rest, args[i])
			}
		}
	}
	return pos, rest
}

// leaseAcquireOptions is the parsed `lease acquire` command line.
type leaseAcquireOptions struct {
	server    string
	slotID    string
	any       bool
	holder    string
	ttlMin    int
	slotsPath string
}

// parseLeaseAcquireArgs parses lease acquire flags in any position (before,
// between or after <server>). Failures are reported to stderr; the result
// is exitOK with the options, or exitUsage.
func parseLeaseAcquireArgs(args []string, stderr io.Writer) (leaseAcquireOptions, int) {
	var o leaseAcquireOptions
	fs := flag.NewFlagSet("lease acquire", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.StringVar(&o.slotID, "slot", "", "slot id to take")
	fs.BoolVar(&o.any, "any", false, "take the first free slot of the server")
	fs.StringVar(&o.holder, "holder", "", "lease holder name")
	fs.IntVar(&o.ttlMin, "ttl", 180, "lease length in minutes (1-720)")
	fs.StringVar(&o.slotsPath, "slots", "", "slot table file")
	pos, rest := splitPositionals(fs, args, 1)
	if err := fs.Parse(rest); err != nil {
		return o, exitUsage
	}
	if len(pos) != 1 || fs.NArg() != 0 {
		fmt.Fprintln(stderr, leaseUsage)
		return o, exitUsage
	}
	o.server = pos[0]
	return o, exitOK
}

// leaseRenewOptions is the parsed `lease renew` command line.
type leaseRenewOptions struct {
	server    string
	slotID    string
	holder    string
	leaseID   string
	ttlMin    int
	slotsPath string
}

// parseLeaseRenewArgs parses lease renew flags in any position (before,
// between or after <server> <slot>). Failures are reported to stderr; the
// result is exitOK with the options, or exitUsage.
func parseLeaseRenewArgs(args []string, stderr io.Writer) (leaseRenewOptions, int) {
	var o leaseRenewOptions
	fs := flag.NewFlagSet("lease renew", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.StringVar(&o.holder, "holder", "", "lease holder name")
	fs.StringVar(&o.leaseID, "lease-id", "", "lease id from acquire (holder alone is not enough)")
	fs.IntVar(&o.ttlMin, "ttl", 180, "lease length in minutes (1-720)")
	fs.StringVar(&o.slotsPath, "slots", "", "slot table file")
	pos, rest := splitPositionals(fs, args, 2)
	if err := fs.Parse(rest); err != nil {
		return o, exitUsage
	}
	if len(pos) != 2 || fs.NArg() != 0 {
		fmt.Fprintln(stderr, leaseUsage)
		return o, exitUsage
	}
	o.server, o.slotID = pos[0], pos[1]
	return o, exitOK
}

// leaseReleaseOptions is the parsed `lease release` command line.
type leaseReleaseOptions struct {
	server    string
	slotID    string
	holder    string
	leaseID   string
	force     bool
	slotsPath string
}

// parseLeaseReleaseArgs parses lease release flags in any position (before,
// between or after <server> <slot>). Failures are reported to stderr; the
// result is exitOK with the options, or exitUsage.
func parseLeaseReleaseArgs(args []string, stderr io.Writer) (leaseReleaseOptions, int) {
	var o leaseReleaseOptions
	fs := flag.NewFlagSet("lease release", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.StringVar(&o.holder, "holder", "", "lease holder name")
	fs.StringVar(&o.leaseID, "lease-id", "", "lease id from acquire (holder alone is not enough; not needed with --force)")
	fs.BoolVar(&o.force, "force", false, "release another holder's or a live lease")
	fs.StringVar(&o.slotsPath, "slots", "", "slot table file")
	pos, rest := splitPositionals(fs, args, 2)
	if err := fs.Parse(rest); err != nil {
		return o, exitUsage
	}
	if len(pos) != 2 || fs.NArg() != 0 {
		fmt.Fprintln(stderr, leaseUsage)
		return o, exitUsage
	}
	o.server, o.slotID = pos[0], pos[1]
	return o, exitOK
}

// leaseListOptions is the parsed `lease list` command line.
type leaseListOptions struct {
	server    string
	asJSON    bool
	slotsPath string
}

// parseLeaseListArgs parses lease list flags in any position (before or
// after the optional <server>). Failures are reported to stderr; the result
// is exitOK with the options, or exitUsage.
func parseLeaseListArgs(args []string, stderr io.Writer) (leaseListOptions, int) {
	var o leaseListOptions
	fs := flag.NewFlagSet("lease list", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.BoolVar(&o.asJSON, "json", false, "print the slot states as JSON")
	fs.StringVar(&o.slotsPath, "slots", "", "slot table file")
	pos, rest := splitPositionals(fs, args, 1)
	if err := fs.Parse(rest); err != nil {
		return o, exitUsage
	}
	if len(pos) > 1 || fs.NArg() != 0 {
		fmt.Fprintln(stderr, leaseUsage)
		return o, exitUsage
	}
	if len(pos) == 1 {
		o.server = pos[0]
	}
	return o, exitOK
}

// slotSetupOptions is the parsed `slot setup` command line.
type slotSetupOptions struct {
	server      string
	slot        string
	holder      string
	dir         string
	binary      string
	dungeonsDir string
	character   string
	slotsPath   string
}

// parseSlotSetupArgs parses slot setup flags in any position (before,
// between or after <server> <slot>). Failures are reported to stderr; the
// result is exitOK with the options, or exitUsage.
func parseSlotSetupArgs(args []string, stderr io.Writer) (slotSetupOptions, int) {
	var o slotSetupOptions
	fs := flag.NewFlagSet("slot setup", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.StringVar(&o.holder, "holder", "", "lease holder name")
	fs.StringVar(&o.dir, "dir", "", "client dir (default ~/ta-<holder>)")
	fs.StringVar(&o.binary, "binary", "", "turtle-agent binary to install (default os.Executable())")
	fs.StringVar(&o.dungeonsDir, "dungeons-dir", "", "dungeons dir (default <dir>/dungeons)")
	fs.StringVar(&o.character, "character", "", "character to play (default the slot's)")
	fs.StringVar(&o.slotsPath, "slots", "", "slot table file")
	pos, rest := splitPositionals(fs, args, 2)
	if err := fs.Parse(rest); err != nil {
		return o, exitUsage
	}
	if len(pos) != 2 || fs.NArg() != 0 || o.holder == "" {
		fmt.Fprintln(stderr, slotSetupUsage)
		return o, exitUsage
	}
	o.server, o.slot = pos[0], pos[1]
	return o, exitOK
}

// teardownOptions is the parsed `teardown` command line.
type teardownOptions struct {
	dir           string
	keepLease     bool
	releaseAnyway bool
	gm            string
	dryRun        bool
	asJSON        bool
	slotsPath     string
}

// parseTeardownArgs parses teardown flags in any position (before or after
// <dir>). Failures are reported to stderr; the result is exitOK with the
// options, or exitUsage.
func parseTeardownArgs(args []string, stderr io.Writer) (teardownOptions, int) {
	var o teardownOptions
	fs := flag.NewFlagSet("teardown", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.BoolVar(&o.keepLease, "keep-lease", false, "keep the slot lease")
	fs.BoolVar(&o.releaseAnyway, "release-anyway", false, "release the lease even when game-side steps failed")
	fs.StringVar(&o.gm, "gm", "off", "gm mode at the end: off, on or keep")
	fs.BoolVar(&o.dryRun, "dry-run", false, "ask the agent for a plan, change nothing")
	fs.BoolVar(&o.asJSON, "json", false, "print the result as JSON")
	fs.StringVar(&o.slotsPath, "slots", "", "slot table file")
	pos, rest := splitPositionals(fs, args, 1)
	if err := fs.Parse(rest); err != nil {
		return o, exitUsage
	}
	if len(pos) != 1 || fs.NArg() != 0 || (o.gm != "off" && o.gm != "on" && o.gm != "keep") {
		fmt.Fprintln(stderr, teardownUsage)
		return o, exitUsage
	}
	o.dir = pos[0]
	return o, exitOK
}
