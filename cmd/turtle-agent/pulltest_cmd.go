package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"

	"github.com/pjerra/turtle-agent/internal/dungeon"
)

const pulltestUsage = "usage: turtle-agent pulltest [--json] <result.json>\n" +
	"       turtle-agent pulltest --compare <a.json> <b.json>\n" +
	"  prints a pull-test result (dungeon_run pulltest, README \"Pull test\") as a table, or the change from a to b.\n" +
	"  A result is the <run id>.pulltest.json file beside the trace, or a dungeon_status answer saved as JSON."

// pulltestCmd reads pull-test results and prints them. It reads nothing but the files.
func pulltestCmd(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("pulltest", flag.ContinueOnError)
	fs.SetOutput(stderr)
	compare := fs.Bool("compare", false, "print the change from the first result to the second")
	asJSON := fs.Bool("json", false, "print the result (or, with --compare, the two summaries) as JSON")
	if err := fs.Parse(args); err != nil {
		fmt.Fprintln(stderr, pulltestUsage)
		return exitUsage
	}
	want := 1
	if *compare {
		want = 2
	}
	if fs.NArg() != want {
		fmt.Fprintln(stderr, pulltestUsage)
		return exitUsage
	}
	var rs []*dungeon.PulltestReport
	for _, path := range fs.Args() {
		data, err := os.ReadFile(path)
		if err != nil {
			fmt.Fprintf(stderr, "%s: %v\n", path, err)
			return exitUsage
		}
		r, err := dungeon.LoadPulltest(data)
		if err != nil {
			fmt.Fprintf(stderr, "%s: %v\n", path, err)
			return exitUsage
		}
		rs = append(rs, r)
	}
	switch {
	case *compare && *asJSON:
		_ = json.NewEncoder(stdout).Encode(map[string]any{"a": rs[0].Summary, "b": rs[1].Summary, "a_scenario": rs[0].Scenario(), "b_scenario": rs[1].Scenario()})
	case *compare:
		fmt.Fprint(stdout, dungeon.ComparePulltest(rs[0], rs[1]))
	case *asJSON:
		_ = json.NewEncoder(stdout).Encode(rs[0])
	default:
		fmt.Fprint(stdout, dungeon.FormatPulltest(rs[0]))
	}
	return exitOK
}
