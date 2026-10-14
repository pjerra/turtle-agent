package main

import (
	"flag"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/pjerra/turtle-agent/internal/brain"
)

const rotationUsage = "usage: turtle-agent rotation list\n" +
	"       turtle-agent rotation show [--level N] <name|file.json>\n" +
	"       turtle-agent rotation check <file.json>...\n" +
	"  list: the built-in rotations (README \"Rotations as data\"); show: a rotation's priority list, with the rank cast at level N;\n" +
	"  check: read rotation files and check them against the spell data (names, levels, auras, conditions). It reads nothing but the files."

// rotationCmd lists, shows and checks rotation files. It sends nothing and logs nothing in.
func rotationCmd(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprintln(stderr, rotationUsage)
		return exitUsage
	}
	switch args[0] {
	case "list":
		for _, n := range brain.RotationNames() {
			r, _ := brain.RotationNamed(n)
			fmt.Fprintf(stdout, "%-18s %-8s %-11s replaces %s\n", r.Name, r.Class, r.Spec, strings.Join(r.Replaces, ", "))
		}
		return exitOK
	case "show":
		fs := flag.NewFlagSet("rotation show", flag.ContinueOnError)
		fs.SetOutput(stderr)
		level := fs.Int("level", 0, "show the rank cast at this character level")
		if err := fs.Parse(args[1:]); err != nil || fs.NArg() != 1 {
			fmt.Fprintln(stderr, rotationUsage)
			return exitUsage
		}
		r, err := loadRotationArg(fs.Arg(0))
		if err != nil {
			fmt.Fprintln(stderr, err)
			return exitUsage
		}
		fmt.Fprint(stdout, r.Describe(*level))
		return exitOK
	case "check":
		if len(args) < 2 {
			fmt.Fprintln(stderr, rotationUsage)
			return exitUsage
		}
		bad := 0
		for _, path := range args[1:] {
			data, err := os.ReadFile(path)
			if err == nil {
				_, err = brain.ParseRotation(data)
			}
			if err != nil {
				fmt.Fprintf(stderr, "%s: %v\n", path, err)
				bad++
				continue
			}
			fmt.Fprintf(stdout, "%s: ok\n", path)
		}
		if bad > 0 {
			return exitUsage
		}
		return exitOK
	}
	fmt.Fprintln(stderr, rotationUsage)
	return exitUsage
}

// loadRotationArg is a built-in rotation by name, or a file when the argument ends in .json (read, not registered).
func loadRotationArg(arg string) (*brain.Rotation, error) {
	if !strings.HasSuffix(arg, ".json") {
		return brain.RotationNamed(arg)
	}
	data, err := os.ReadFile(arg)
	if err != nil {
		return nil, err
	}
	return brain.ParseRotation(data)
}
