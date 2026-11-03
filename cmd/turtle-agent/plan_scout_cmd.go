package main

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/pjerra/turtle-agent/internal/dungeon"
	"github.com/pjerra/turtle-agent/internal/navmesh"
	"github.com/pjerra/turtle-agent/internal/planexport"
	"github.com/pjerra/turtle-agent/internal/planscout"
	"github.com/pjerra/turtle-agent/internal/routeedit"
	"github.com/pjerra/turtle-agent/internal/worldspawn"
)

// The scout's exit codes.
const (
	// exitPlanScoutFail: at least one step failed (or warned, with --fail-on warn), or --apply-fixes was refused.
	exitPlanScoutFail = 27
	// exitPlanScoutLiveAborted: the live pass stopped part-way (offline verdicts complete, live ones skipping).
	exitPlanScoutLiveAborted = 28
	// exitLivePhaseRefused: a live phase failed or was refused (no test server, GM mode, module, toolkit, instance).
	exitLivePhaseRefused = 18
	// exitPlanScoutNoNavmesh: the navmesh could not answer, so nothing was checked.
	exitPlanScoutNoNavmesh = 1
)

// planScoutNav is what plan-scout needs from the navmesh; navmesh.Planner is one and the tests inject a fake.
type planScoutNav interface {
	planscout.Nav
	Close()
}

// planScoutOpen opens the navmesh of one mmaps folder; the caller closes it.
var planScoutOpen = func(dir string) planScoutNav {
	return navmesh.NewPlanner(dir)
}

const planScoutUsage = `usage: turtle-agent plan-scout <dungeon> [--dir dungeons] [--sql FILE] [--mmaps DIR] [--spawns DIR]
    [--from LEG] [--until LEG] [--party-level N] [--out DIR] [--json]
    [--fix-out FILE] [--apply-fixes DIR] [--fail-on fail|warn]
    [--live <server>] [--slot N] [--holder NAME] [--slots FILE] [--binary PATH]
    [--walk-budget 5m] [--max-minutes 20] [--instance-cap-wait 20m] [--instance-cap-fail] [--instance-cap-reset]`

// planScoutOptions are the command's flags.
type planScoutOptions struct {
	dir         string
	sqlFile     string
	mmaps       string
	spawns      string
	from, until string
	partyLevel  int
	json        bool
	failOn      string
	// live flags (parsed; the live phase is built by W3 and refuses here).
	live             string
	slot             int
	holder           string
	slots            string
	binary           string
	walkBudget       string
	maxMinutes       int
	instanceCapWait  string
	instanceCapFail  bool
	instanceCapReset bool
}

// planScoutCmd checks a plan's exported rows offline (README, "Plan scout"): the placement lints, the four scout rules and
// the joiner estimate, a report and a fix file. Only the offline pass is in this build.
func planScoutCmd(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("plan-scout", flag.ContinueOnError)
	fs.SetOutput(stderr)
	o := planScoutOptions{}
	var out, fixOut, applyTo string
	fs.StringVar(&o.dir, "dir", "dungeons", "the folder with the plan files")
	fs.StringVar(&o.sqlFile, "sql", "", "check a deployed world migration's rows instead of the plan file's export")
	fs.StringVar(&o.mmaps, "mmaps", defaultMmapsDir(), "folder with the server's mmaps (<map>.mmap and <map>*.mmtile)")
	fs.StringVar(&o.spawns, "spawns", "", "the map's world export (default: <dir>/../tools/dungeongen/testdata/mapN)")
	fs.StringVar(&o.from, "from", "", "first leg to report (an exported leg number or a leg name)")
	fs.StringVar(&o.until, "until", "", "last leg to report (inclusive)")
	fs.IntVar(&o.partyLevel, "party-level", 0, "the party's level for the aggro radius (default: the plan's band high end)")
	fs.StringVar(&out, "out", "", "folder for scout-<plan>.json and scout-<plan>.txt (default ./plan-scout-<plan>-<stamp>)")
	fs.BoolVar(&o.json, "json", false, "print the report as JSON")
	fs.StringVar(&fixOut, "fix-out", "", "write the fix file (tb-scout-fix v1) here, also with no fixes")
	fs.StringVar(&applyTo, "apply-fixes", "", "write the patched plan copy and its diff to this folder (must not exist or be empty)")
	fs.StringVar(&o.failOn, "fail-on", "fail", "fail or warn: which step verdict fails the run")
	fs.StringVar(&o.live, "live", "", "run the live pass on this test server (not in this build)")
	fs.IntVar(&o.slot, "slot", 0, "the slot to use (3 is never allowed)")
	fs.StringVar(&o.holder, "holder", "", "the lease holder's name (default plan-scout-<plan>)")
	fs.StringVar(&o.slots, "slots", "", "the slots file")
	fs.StringVar(&o.binary, "binary", "", "the server binary path")
	fs.StringVar(&o.walkBudget, "walk-budget", "5m", "live walk budget")
	fs.IntVar(&o.maxMinutes, "max-minutes", 20, "live time ceiling in minutes")
	fs.StringVar(&o.instanceCapWait, "instance-cap-wait", "20m", "how long to wait for a free instance place")
	fs.BoolVar(&o.instanceCapFail, "instance-cap-fail", false, "fail when the instance ledger is at its cap")
	fs.BoolVar(&o.instanceCapReset, "instance-cap-reset", false, "reset the instance ledger entry")
	flags, pos, err := splitFlagArgs(fs, args)
	if err != nil {
		return exitUsage
	}
	if err := fs.Parse(flags); err != nil {
		return exitUsage
	}
	fail := func(format string, a ...any) int {
		fmt.Fprintf(stderr, "plan-scout: "+format+"\n", a...)
		return exitUsage
	}
	if len(pos) != 1 {
		fmt.Fprintln(stderr, planScoutUsage)
		return fail("name one plan")
	}
	name := pos[0]
	if !planName.MatchString(name) {
		return fail("%q is not a plan name (a-z, 0-9, _ and -)", name)
	}
	if o.failOn != "fail" && o.failOn != "warn" {
		return fail("--fail-on takes fail or warn")
	}
	if o.slot == 3 {
		fmt.Fprintln(stderr, "plan-scout: slot 3 is never used")
		return exitSlotRefused
	}
	if o.live != "" {
		fmt.Fprintf(stderr, "plan-scout: the live phase is not in this build (W3 builds it): run without --live\n")
		return exitLivePhaseRefused
	}

	planner := planScoutOpen(o.mmaps)
	defer planner.Close()

	// The rows: the plan's export, or a migration's rows.
	var in planscout.Input
	planPath := filepath.Join(o.dir, name+".json")
	p, perr := dungeon.Load(planPath)
	if perr != nil && o.sqlFile == "" {
		return fail("%v", perr)
	}
	in.Plan = p
	source := planPath
	if o.sqlFile != "" {
		b, err := os.ReadFile(o.sqlFile)
		if err != nil {
			return fail("%v", err)
		}
		all, notes, err := planexport.ParseMigration(string(b))
		if err != nil {
			return fail("%s: %v", o.sqlFile, err)
		}
		var rows []planexport.Rows
		for _, r := range all {
			if r.Plan.Plan == name {
				rows = append(rows, r)
			}
		}
		if len(rows) == 0 {
			return fail("%s holds no plan %q", o.sqlFile, name)
		}
		if len(rows) > 1 {
			return fail("%s holds %d plans named %q", o.sqlFile, len(rows), name)
		}
		in.Rows = rows[0]
		source = o.sqlFile
		_ = notes
	} else {
		if p == nil {
			return fail("%v", perr)
		}
		res, err := planexport.Build(p, planexport.Options{Source: "plan-scout", Cut: cutWith(planner, false)})
		if err != nil {
			return fail("the exporter refused the plan: %v", err)
		}
		in.Rows, in.Origins = res.Rows, res.Origins
	}

	from, err := legNumber(in, o.from)
	if err != nil {
		return fail("%v", err)
	}
	until, err := legNumber(in, o.until)
	if err != nil {
		return fail("%v", err)
	}

	// One query tells whether the navmesh can answer at all: that is exit 1, not a report of every row off the mesh.
	if len(in.Rows.Steps) > 0 {
		s := in.Rows.Steps[0]
		if _, err := planner.ServerPath(in.Rows.Plan.Map, navmesh.Vec3{s.X, s.Y, s.Z}, navmesh.Vec3{s.X, s.Y, s.Z}); err != nil {
			if reason, unavailable := planCheckUnavailable(err); unavailable {
				fmt.Fprintf(stderr, "plan-scout: %s\n", reason)
			} else {
				fmt.Fprintf(stderr, "plan-scout: %v\n", err)
			}
			return exitPlanScoutNoNavmesh
		}
	}

	// The world export: explicit --spawns must read; the default one is optional (the aggro and joiner lints then skip).
	spawnDir := o.spawns
	if spawnDir == "" {
		spawnDir = filepath.Join(o.dir, "..", "tools", "dungeongen", "testdata", fmt.Sprintf("map%d", in.Rows.Plan.Map))
	}
	var world *worldspawn.World
	wnote := ""
	if _, err := os.Stat(filepath.Join(spawnDir, "creatures.tsv")); err != nil {
		if o.spawns != "" {
			return fail("no world export in %s", spawnDir)
		}
		wnote = fmt.Sprintf("aggro, patrol and joiner lints skipped: no world export in %s (pass --spawns DIR)", spawnDir)
	} else {
		w, notes, err := worldspawn.Load(spawnDir, in.Rows.Plan.Map)
		if err != nil {
			if o.spawns != "" {
				return fail("%v", err)
			}
			wnote = fmt.Sprintf("aggro, patrol and joiner lints skipped: %v", err)
		} else {
			world = w
			if len(notes) > 0 {
				wnote = strings.Join(notes, "; ")
			}
		}
	}

	rep, err := planscout.Offline(in, planner, world, planscout.Options{Filter: o.from != "" || o.until != "", FromLeg: from, UntilLeg: until, PartyLevel: o.partyLevel})
	if err != nil {
		return fail("%v", err)
	}
	rep.Source, rep.Mmaps = source, o.mmaps
	if from < 0 {
		rep.From = 0
	}
	if until < 0 {
		rep.Until = 0
	}
	if wnote != "" {
		rep.Notes = append(rep.Notes, wnote)
	}

	// The fix file (always when asked, also with no fixes).
	var fixes []planscout.Fix
	if os.Getenv("SCOUT_NO_FIXES") == "" {
		fixes = planscout.ProposeFixes(rep, in, planner, world)
	}
	rowsSHA := scoutRowsSHA(in.Rows)
	if fixOut != "" {
		ff := planscout.FixFile{Format: planscout.FixFormat, Version: planscout.FixVersion, Plan: rep.Plan, Map: rep.Map,
			RowsSHA256: rowsSHA, Scout: "scout-" + rep.Plan + ".json", Fixes: fixes}
		if ff.Fixes == nil {
			ff.Fixes = []planscout.Fix{}
		}
		if err := writeJSON(fixOut, ff); err != nil {
			return fail("--fix-out: %v", err)
		}
	}

	// The report files.
	outDir := out
	if outDir == "" {
		outDir = fmt.Sprintf("./plan-scout-%s-%s", rep.Plan, time.Now().Format("20060102-150405"))
	}
	if err := os.MkdirAll(outDir, 0o755); err != nil {
		return fail("%v", err)
	}
	jsonFile := filepath.Join(outDir, "scout-"+rep.Plan+".json")
	textFile := filepath.Join(outDir, "scout-"+rep.Plan+".txt")
	if err := writeJSON(jsonFile, rep); err != nil {
		return fail("%v", err)
	}
	var tb strings.Builder
	rep.Text(&tb)
	if err := os.WriteFile(textFile, []byte(tb.String()), 0o644); err != nil {
		return fail("%v", err)
	}
	if o.json {
		_ = json.NewEncoder(stdout).Encode(rep)
	} else {
		io.WriteString(stdout, tb.String())
	}
	fmt.Fprintf(stdout, "report: %s\n", outDir)

	// --apply-fixes: a patched copy of the plan and its diff, never in place.
	applyRefused := false
	if applyTo != "" {
		switch {
		case in.Plan == nil:
			return fail("--apply-fixes needs the plan file (%s): a migration's rows have no plan refs", planPath)
		case !emptyDir(applyTo):
			return fail("%s exists and is not empty", applyTo)
		case underDungeons(applyTo):
			return fail("%s is inside a dungeons/ folder (or a slot's plan folder): the plan file is never written there", applyTo)
		}
		if o.sqlFile != "" {
			exp, err := planexport.Build(in.Plan, planexport.Options{Source: "plan-scout", Cut: cutWith(planner, false)})
			if err != nil {
				return fail("%v", err)
			}
			if h := scoutRowsSHA(exp.Rows); h != rowsSHA {
				return fail("the rows in %s do not equal the plan's export (sha256 %s vs %s): nothing to apply fixes to", o.sqlFile, rowsSHA, h)
			}
		}
		edited, res, err := planscout.ApplyFixes(in.Plan, fixes, planner)
		if err != nil {
			applyRefused = true
			rep.Notes = append(rep.Notes, "apply-fixes refused: "+err.Error())
			fmt.Fprintf(stderr, "plan-scout: --apply-fixes refused: %v\n", err)
			_ = res
		} else {
			if err := os.MkdirAll(applyTo, 0o755); err != nil {
				return fail("%v", err)
			}
			nb, err := dungeon.Marshal(edited)
			if err != nil {
				return fail("%v", err)
			}
			old, _ := os.ReadFile(planPath)
			if err := os.WriteFile(filepath.Join(applyTo, rep.Plan+".json"), nb, 0o644); err != nil {
				return fail("%v", err)
			}
			diff := routeedit.UnifiedDiff(planPath, filepath.Join(applyTo, rep.Plan+".json"), string(old), string(nb))
			if err := os.WriteFile(filepath.Join(applyTo, rep.Plan+".diff"), []byte(diff), 0o644); err != nil {
				return fail("%v", err)
			}
			rep.Notes = append(rep.Notes, fmt.Sprintf("apply-fixes: %d fix(es) applied to a copy in %s (the plan check found no new issue)", countApplied(fixes), applyTo))
		}
		// Rewrite the report with the apply note.
		if err := writeJSON(jsonFile, rep); err != nil {
			return fail("%v", err)
		}
		tb.Reset()
		rep.Text(&tb)
		_ = os.WriteFile(textFile, []byte(tb.String()), 0o644)
	}

	switch {
	case applyRefused, rep.Counts.Fail > 0, o.failOn == "warn" && rep.Counts.Warn > 0:
		return exitPlanScoutFail
	}
	return exitOK
}

// splitFlagArgs splits the command line into flags and the positional arguments: the plan name may stand anywhere (the
// usage shows it first, and the flag package stops at the first non-flag word).
func splitFlagArgs(fs *flag.FlagSet, args []string) (flags, pos []string, err error) {
	for i := 0; i < len(args); i++ {
		a := args[i]
		if a == "--" {
			pos = append(pos, args[i+1:]...)
			break
		}
		if len(a) == 0 || a[0] != '-' || a == "-" {
			pos = append(pos, a)
			continue
		}
		flags = append(flags, a)
		name := strings.TrimLeft(a, "-")
		if j := strings.IndexByte(name, '='); j >= 0 {
			continue
		}
		f := fs.Lookup(name)
		if f == nil {
			continue // flag.Parse reports the unknown flag
		}
		if b, ok := f.Value.(interface{ IsBoolFlag() bool }); ok && b.IsBoolFlag() {
			continue
		}
		if i+1 < len(args) {
			i++
			flags = append(flags, args[i])
		}
	}
	return flags, pos, nil
}

func countApplied(fixes []planscout.Fix) int {
	n := 0
	for _, f := range fixes {
		if f.Apply {
			n++
		}
	}
	return n
}

// legNumber reads --from/--until: a leg number (the exported leg, 0-based as the rows carry it) or a plan leg name.
func legNumber(in planscout.Input, v string) (int, error) {
	if v == "" {
		return -1, nil
	}
	if n, err := strconv.Atoi(v); err == nil {
		if n < 0 {
			return 0, fmt.Errorf("leg %d: numbers start at 0", n)
		}
		return n, nil
	}
	if in.Plan == nil {
		return 0, fmt.Errorf("leg name %q: --from/--until take a number without the plan file", v)
	}
	for li, l := range in.Plan.Legs {
		if l.Name != v {
			continue
		}
		n, ok := in.Rows.LegNumber(in.Plan, li)
		if !ok {
			return 0, fmt.Errorf("leg %q is not in the export", v)
		}
		return n, nil
	}
	return 0, fmt.Errorf("no leg named %q in the plan", v)
}

// rowsSHA256 pins the rows a fix file was made for.
func scoutRowsSHA(r planexport.Rows) string {
	return fmt.Sprintf("%x", sha256.Sum256([]byte(planexport.WriteSQL([]planexport.Rows{r}))))
}

// writeJSON writes v as indented JSON, creating the file.
func writeJSON(path string, v any) error {
	var b strings.Builder
	enc := json.NewEncoder(&b)
	enc.SetIndent("", "  ")
	if err := enc.Encode(v); err != nil {
		return err
	}
	return os.WriteFile(path, []byte(b.String()), 0o644)
}

// emptyDir says the path does not exist, or is an empty directory.
func emptyDir(path string) bool {
	st, err := os.Stat(path)
	if errors.Is(err, os.ErrNotExist) {
		return true
	}
	if err != nil || !st.IsDir() {
		return false
	}
	ents, err := os.ReadDir(path)
	return err == nil && len(ents) == 0
}

// underDungeons says the path is inside a dungeons/ folder or a slot's plan folder.
func underDungeons(path string) bool {
	abs, err := filepath.Abs(path)
	if err != nil {
		return true
	}
	for _, part := range strings.Split(abs, string(filepath.Separator)) {
		if part == "dungeons" || part == "slots" {
			return true
		}
	}
	return false
}

// defaultMmapsDir is ~/turtle-agent-mmaps.
func defaultMmapsDir() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return "turtle-agent-mmaps"
	}
	return filepath.Join(home, "turtle-agent-mmaps")
}
