package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/pjerra/turtle-agent/internal/dungeon"
	"github.com/pjerra/turtle-agent/internal/minimap"
	"github.com/pjerra/turtle-agent/internal/plandiff"
	"github.com/pjerra/turtle-agent/internal/planexport"
)

// exitPlanDiffChanged: plan-diff --fail-on-change found a change other than a comment.
const exitPlanDiffChanged = 25

const planDiffUsage = "usage: turtle-agent plan-diff <old> <new> [--plan NAME] [--json] [--out DIR] [--svg FILE] [--minimaps DIR] [--picture worldmap|minimap|navmesh|none] [--fail-on-change]"

// planInput is one side of the diff: its rows and what had to be said about reading them.
type planInput struct {
	rows planexport.Rows
	note string
}

// planUsageError is a mistake in what the command was asked to compare (exit 2), not an unreadable file (exit 1).
type planUsageError struct{ msg string }

func (e planUsageError) Error() string { return e.msg }

// planDiffCmd compares two exports of one plan and prints what changed (README, "Plan diff").
func planDiffCmd(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("plan-diff", flag.ContinueOnError)
	fs.SetOutput(stderr)
	plan := fs.String("plan", "", "the plan to compare when a migration holds several")
	asJSON := fs.Bool("json", false, "print the tb-plan-diff JSON on stdout instead of the text")
	out := fs.String("out", "", "write diff-<plan>.txt, diff-<plan>.json and diff-<plan>.svg there (refused when a file exists)")
	svgFile := fs.String("svg", "", "also write the SVG to this file")
	minimaps := fs.String("minimaps", "~/turtle-agent-minimaps", "folder with the map pictures: <map>.json/.png (worldmap), <map>.minimap.*, <map>.navmesh.*")
	picture := fs.String("picture", "worldmap", "the picture under the route: worldmap, minimap, navmesh or none")
	failOnChange := fs.Bool("fail-on-change", false, "exit 25 when there is any change other than a comment")
	if err := fs.Parse(splitPlanDiffArgs(fs, args)); err != nil {
		return exitUsage
	}
	if fs.NArg() != 2 {
		fmt.Fprintln(stderr, planDiffUsage)
		return exitUsage
	}
	switch *picture {
	case "worldmap", "minimap", "navmesh", "none":
	default:
		fmt.Fprintf(stderr, "plan-diff: --picture %q: want worldmap, minimap, navmesh or none\n", *picture)
		return exitUsage
	}
	oldIn, err := readPlanInput(fs.Arg(0), *plan)
	if err != nil {
		return planDiffInputError(err, stderr)
	}
	newIn, err := readPlanInput(fs.Arg(1), *plan)
	if err != nil {
		return planDiffInputError(err, stderr)
	}
	if oldIn.rows.Plan.Map != newIn.rows.Plan.Map {
		fmt.Fprintf(stderr, "plan-diff: the plans are on different maps (%d and %d): a route diff is one map\n",
			oldIn.rows.Plan.Map, newIn.rows.Plan.Map)
		return exitUsage
	}
	name := newIn.rows.Plan.Plan
	if !planName.MatchString(name) {
		fmt.Fprintf(stderr, "plan-diff: %q is not a plan name (a-z, 0-9, _ and -), so its output files cannot be named\n", name)
		return exitUsage
	}
	d := plandiff.Rows(oldIn.rows, newIn.rows)
	if oldIn.note != "" {
		d.Notes = append(d.Notes, oldIn.note)
	} else if newIn.note != "" {
		d.Notes = append(d.Notes, newIn.note)
	}
	if oldName := oldIn.rows.Plan.Plan; oldName != name {
		d.Notes = append(d.Notes, fmt.Sprintf("plan name: %s -> %s", oldName, name))
	}

	dirSVG := *out != "" && !(*picture == "none" && *svgFile == "")
	dirSVGPath := filepath.Join(*out, "diff-"+name+".svg")
	var svg []byte
	if dirSVG || *svgFile != "" {
		pic, png, note := loadPlanPicture(*picture, expandHome(*minimaps), d.Map)
		if note != "" {
			d.Notes = append(d.Notes, note)
		}
		if planDiffHas2F(expandHome(*minimaps), d.Map) {
			svg = plandiff.SVG2F(d, oldIn.rows, newIn.rows, pic, png)
		} else {
			svg = plandiff.SVG(d, oldIn.rows, newIn.rows, pic, png)
		}
	}

	var text bytes.Buffer
	d.Text(&text)
	var jsonOut []byte
	if *asJSON || *out != "" {
		jsonOut, err = plandiff.JSON(d)
		if err != nil {
			fmt.Fprintf(stderr, "plan-diff: %v\n", err)
			return 1
		}
	}
	if *asJSON {
		if _, err := stdout.Write(jsonOut); err != nil {
			fmt.Fprintf(stderr, "plan-diff: %v\n", err)
			return 1
		}
	} else if _, err := stdout.Write(text.Bytes()); err != nil {
		fmt.Fprintf(stderr, "plan-diff: %v\n", err)
		return 1
	}

	type diffFile struct {
		path string
		data []byte
	}
	var writes []diffFile
	if *out != "" {
		writes = append(writes,
			diffFile{filepath.Join(*out, "diff-"+name+".txt"), text.Bytes()},
			diffFile{filepath.Join(*out, "diff-"+name+".json"), jsonOut})
		if dirSVG {
			writes = append(writes, diffFile{dirSVGPath, svg})
		}
	}
	if *svgFile != "" {
		writes = append(writes, diffFile{*svgFile, svg})
	}
	targets := make([]string, len(writes))
	for i, w := range writes {
		targets[i] = w.path
	}
	if p := existingFile(targets); p != "" {
		fmt.Fprintf(stderr, "plan-diff: %s exists: refusing to overwrite a diff\n", p)
		return exitUsage
	}
	if *out != "" {
		if err := os.MkdirAll(*out, 0o700); err != nil {
			fmt.Fprintf(stderr, "plan-diff: %v\n", err)
			return 1
		}
	}
	for _, w := range writes {
		if err := writePlanDiffFile(w.path, w.data); err != nil {
			fmt.Fprintf(stderr, "plan-diff: %v\n", err)
			return 1
		}
	}
	if *failOnChange && d.Changed() {
		return exitPlanDiffChanged
	}
	return exitOK
}

// splitPlanDiffArgs takes the flags out of args before the flag set sees them, so the documented form
// `plan-diff <old> <new> [flags]` works too (the flag package stops at the first positional).
func splitPlanDiffArgs(fs *flag.FlagSet, args []string) []string {
	var flags, pos []string
	for i := 0; i < len(args); i++ {
		a := args[i]
		if a == "--" {
			pos = append(pos, args[i+1:]...)
			break
		}
		if len(a) < 2 || a[0] != '-' {
			pos = append(pos, a)
			continue
		}
		flags = append(flags, a)
		name := strings.TrimLeft(a, "-")
		if strings.Contains(name, "=") {
			continue
		}
		f := fs.Lookup(name)
		if f == nil {
			continue
		}
		if b, ok := f.Value.(interface{ IsBoolFlag() bool }); ok && b.IsBoolFlag() {
			continue
		}
		if i+1 < len(args) {
			i++
			flags = append(flags, args[i])
		}
	}
	return append(flags, pos...)
}

func planDiffInputError(err error, stderr io.Writer) int {
	var ue planUsageError
	if errors.As(err, &ue) {
		fmt.Fprintf(stderr, "plan-diff: %s\n", ue.msg)
		return exitUsage
	}
	fmt.Fprintf(stderr, "plan-diff: %v\n", err)
	return 1
}

// readPlanInput reads one side: a .sql migration of the plan rows, or a plan JSON built to rows.
func readPlanInput(path, want string) (planInput, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return planInput{}, err
	}
	switch {
	case strings.HasSuffix(strings.ToLower(path), ".sql"):
		rows, _, err := planexport.ParseMigration(string(data))
		if err != nil {
			return planInput{}, fmt.Errorf("%s: %w", path, err)
		}
		if want != "" {
			for _, r := range rows {
				if r.Plan.Plan == want {
					return planInput{rows: r}, nil
				}
			}
			return planInput{}, planUsageError{fmt.Sprintf("%s holds no plan %q", path, want)}
		}
		switch len(rows) {
		case 0:
			return planInput{}, fmt.Errorf("%s holds no plan rows", path)
		case 1:
			return planInput{rows: rows[0]}, nil
		}
		names := make([]string, len(rows))
		for i, r := range rows {
			names[i] = r.Plan.Plan
		}
		return planInput{}, planUsageError{fmt.Sprintf("%s holds %d plans (%s): pass --plan", path, len(rows), strings.Join(names, ", "))}
	case strings.HasSuffix(strings.ToLower(path), ".json"):
		p, err := dungeon.Load(path)
		if err != nil {
			return planInput{}, err
		}
		if want != "" && p.Name != want {
			return planInput{}, planUsageError{fmt.Sprintf("%s is plan %q, not %q", path, p.Name, want)}
		}
		res, err := planexport.Build(p, planexport.Options{})
		if err != nil {
			return planInput{}, fmt.Errorf("%s: %w", path, err)
		}
		return planInput{rows: res.Rows, note: "rows built from the plan JSON (no navmesh cut)"}, nil
	default:
		return planInput{}, fmt.Errorf("%s: want a .sql migration or a .json plan", path)
	}
}

// loadPlanPicture reads the map picture of the chosen source. A missing picture is nil and a note (a blank background).
func loadPlanPicture(picture, dir string, id uint32) (*minimap.Meta, []byte, string) {
	if picture == "none" {
		return nil, nil, ""
	}
	base := fmt.Sprintf("%d", id)
	switch picture {
	case "minimap":
		base += ".minimap"
	case "navmesh":
		base += ".navmesh"
	}
	metaPath, pngPath := filepath.Join(dir, base+".json"), filepath.Join(dir, base+".png")
	var m minimap.Meta
	data, err := os.ReadFile(metaPath)
	if err != nil {
		return nil, nil, fmt.Sprintf("no %s picture for map %d in %s (%s: %v): blank background", picture, id, dir, filepath.Base(metaPath), err)
	}
	if err := json.Unmarshal(data, &m); err != nil {
		return nil, nil, fmt.Sprintf("no %s picture for map %d in %s (%s: %v): blank background", picture, id, dir, filepath.Base(metaPath), err)
	}
	if m.Width <= 0 || m.Height <= 0 || m.PxPerYard <= 0 {
		return nil, nil, fmt.Sprintf("no %s picture for map %d in %s (%s has no size): blank background", picture, id, dir, filepath.Base(metaPath))
	}
	png, err := os.ReadFile(pngPath)
	if err != nil {
		return nil, nil, fmt.Sprintf("no %s picture for map %d in %s (%s: %v): blank background", picture, id, dir, filepath.Base(pngPath), err)
	}
	return &m, png, ""
}

// planDiffHas2F reports whether the minimaps folder has the second floor picture of the map.
func planDiffHas2F(dir string, id uint32) bool {
	st, err := os.Stat(filepath.Join(dir, fmt.Sprintf("%d.worldmap-2f.json", id)))
	return err == nil && !st.IsDir()
}

// existingFile names the first path that exists (a diff never overwrites one).
func existingFile(paths []string) string {
	for _, p := range paths {
		if _, err := os.Lstat(p); err == nil {
			return p
		}
	}
	return ""
}

// writePlanDiffFile creates the file (0600), refusing an existing one.
func writePlanDiffFile(path string, data []byte) error {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	if _, err := f.Write(data); err != nil {
		f.Close()
		os.Remove(path)
		return err
	}
	if err := f.Close(); err != nil {
		os.Remove(path)
		return err
	}
	return nil
}
