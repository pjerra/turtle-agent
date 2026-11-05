package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/pjerra/turtle-agent/internal/dungeon"
	"github.com/pjerra/turtle-agent/internal/navmesh"
	"github.com/pjerra/turtle-agent/internal/planexport"
	"github.com/pjerra/turtle-agent/internal/planlint"
	"github.com/pjerra/turtle-agent/internal/rpc"
	"github.com/pjerra/turtle-agent/internal/worldspawn"
)

// exitPlanCheck: plan-check found a point the navmesh cannot take (a failing plan is not a usage error).
const exitPlanCheck = 11

const planCheckUsage = "usage: turtle-agent plan-check [--dir dungeons] --mmaps DIR [--from-leg N [--from-pull-group N]] [--spawns DIR] [--floor-aware] [--approach-hops] [--strict] [--json] <plan>... | turtle-agent plan-check --mmaps DIR --sql FILE [--plan NAME] [--spawns DIR] [--strict] [--json]"

// planCheckCmd walks the named plans on the navmesh (README, "Plan check") and prints each failing point. It reads the plan files and
// the mmaps folder and nothing else.
func planCheckCmd(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("plan-check", flag.ContinueOnError)
	fs.SetOutput(stderr)
	dir := fs.String("dir", "dungeons", "the folder with the plan files")
	mmaps := fs.String("mmaps", "", "folder with the server's mmaps (<map>.mmap and <map>*.mmtile); required")
	from := fs.Int("from-leg", 0, "check from this leg on (the legs before it only place the party)")
	fromGroup := fs.Int("from-pull-group", -1, "with --from-leg: check from this pull group (0-based) of that leg on (default: the leg's own walk too)")
	sqlFile := fs.String("sql", "", "lint the rows of this world migration instead of a plan file (the exported rows as the server runs them)")
	planFlag := fs.String("plan", "", "with --sql: the plan in the file (needed when it holds several)")
	spawns := fs.String("spawns", "", "the map's world export for the aggro and patrol lints (tools/dungeongen/export.sh writes it; default: <dir>/../tools/dungeongen/testdata/mapN)")
	floorAware := fs.Bool("floor-aware", false, "build the plan's rows for the lints with the floor-aware cutter (plan-export --floor-aware)")
	approachHops := fs.Bool("approach-hops", false, "build the plan's rows for the lints with the approach hops (plan-export --approach-hops)")
	strict := fs.Bool("strict", false, "make a lint finding fail the check (by default the lints are warnings)")
	asJSON := fs.Bool("json", false, "print the results as JSON")
	if err := fs.Parse(args); err != nil {
		return exitUsage
	}
	given := map[string]bool{}
	fs.Visit(func(f *flag.Flag) { given[f.Name] = true })
	if given["from-pull-group"] && !given["from-leg"] {
		fmt.Fprintln(stderr, "plan-check: --from-pull-group needs --from-leg (a pull group number means nothing without its leg)")
		return exitUsage
	}
	switch {
	case *sqlFile != "" && fs.NArg() > 0:
		fmt.Fprintln(stderr, "plan-check: name the plans or pass --sql, not both")
		return exitUsage
	case *sqlFile == "" && *planFlag != "":
		fmt.Fprintln(stderr, "plan-check: --plan picks a plan in the --sql file; name the plan as the argument otherwise")
		return exitUsage
	case *sqlFile == "" && fs.NArg() == 0, *mmaps == "":
		fmt.Fprintln(stderr, planCheckUsage)
		return exitUsage
	}
	planner := navmesh.NewPlanner(*mmaps)
	defer planner.Close()
	if *sqlFile != "" {
		return planLintCmd(*sqlFile, *planFlag, *spawns, *dir, planner, *strict, *asJSON, stdout, stderr)
	}
	var results []planCheckReport
	failed := false
	for _, n := range fs.Args() {
		if !planName.MatchString(n) {
			fmt.Fprintf(stderr, "plan-check: %q is not a plan name (a-z, 0-9, _ and -)\n", n)
			return exitUsage
		}
		p, err := dungeon.Load(filepath.Join(*dir, n+".json"))
		if err != nil {
			fmt.Fprintf(stderr, "plan-check: %v\n", err)
			return exitUsage
		}
		opt := dungeon.CheckOptions{FromLeg: *from}
		if *fromGroup >= 0 {
			opt.FromPullGroup = fromGroup
		}
		res, err := dungeon.CheckPlanWith(p, planner, opt)
		if err != nil {
			fmt.Fprintf(stderr, "plan-check: %s: %v\n", n, err)
			return exitUsage
		}
		lints, note, err := planLints(p, planner, *dir, *spawns, *floorAware, *approachHops)
		if err != nil {
			fmt.Fprintf(stderr, "plan-check: %s: %v\n", n, err)
			return exitUsage
		}
		rep := planCheckReport{PlanCheck: *res, Lints: lints, LintCounts: planlint.Counts(lints), LintNote: note}
		results = append(results, rep)
		failed = failed || !res.OK() || *strict && len(lints) > 0 || lintErrors(lints) > 0
		if *asJSON {
			continue
		}
		if res.OK() {
			fmt.Fprintf(stdout, "%s: ok, %d points on the map %d navmesh\n", n, res.Points, res.Map)
		} else {
			fmt.Fprintf(stdout, "%s: %d of %d points fail (%d root, %d re-anchored after an earlier failure)\n%s\n", n, len(res.Issues), res.Points, res.RootIssues(), res.Reanchored, res.Summary())
		}
		if len(res.Exempt) > 0 {
			fmt.Fprintf(stdout, "%s: %d hops exempted by the plan (check_skip)\n%s\n", n, len(res.Exempt), res.ExemptSummary())
		}
		if len(res.RunLeader) > 0 {
			fmt.Fprintf(stdout, "%s: %d packs or bosses the TortoiseBots run leader walks toward in hops of its own (give them approach points)\n%s\n", n, len(res.RunLeader), res.RunLeaderSummary())
		}
		if len(res.Warnings) > 0 {
			fmt.Fprintf(stdout, "%s: %d warnings\n%s\n", n, len(res.Warnings), res.WarningSummary())
		}
		printLints(stdout, n, lints, note, *strict)
	}
	if *asJSON {
		_ = json.NewEncoder(stdout).Encode(results)
	}
	if failed {
		return exitPlanCheck
	}
	return exitOK
}

// planCheckReport is one plan's check and the lints of its exported rows. The embedded PlanCheck keeps the JSON of a plain
// plan-check unchanged; lints and lint_counts are added.
type planCheckReport struct {
	dungeon.PlanCheck
	Lints      []planlint.Finding `json:"lints,omitempty"`
	LintCounts map[string]int     `json:"lint_counts,omitempty"`
	LintNote   string             `json:"lint_note,omitempty"`
}

// planLintReport is the lints of a migration's rows (plan-check --sql).
type planLintReport struct {
	Plan       string             `json:"plan"`
	Map        uint32             `json:"map"`
	Steps      int                `json:"steps"`
	Lints      []planlint.Finding `json:"lints"`
	LintCounts map[string]int     `json:"lint_counts"`
	Note       string             `json:"note,omitempty"`
}

// lintOrder is the order the lints are counted and printed in.
var lintOrder = []string{planlint.LintWater, planlint.LintOffFloor, planlint.LintEdge, planlint.LintLedge,
	planlint.LintShortHop, planlint.LintAggro, planlint.LintBossAggro, planlint.LintPatrol, planlint.LintReach, planlint.LintHop,
	planlint.LintArrival, planlint.LintClimb, planlint.LintJoiners}

// lintErrors counts the findings the lints call errors: they fail plan-check without --strict.
func lintErrors(fs []planlint.Finding) int {
	n := 0
	for _, f := range fs {
		if f.Severity == planlint.SeverityError {
			n++
		}
	}
	return n
}

func lintCountsText(c map[string]int) string {
	parts := make([]string, 0, len(c))
	for _, k := range lintOrder {
		if c[k] > 0 {
			parts = append(parts, fmt.Sprintf("%s %d", k, c[k]))
		}
	}
	if len(parts) == 0 {
		return "none"
	}
	return strings.Join(parts, ", ")
}

// planLints builds the plan's rows as plan-export writes them and lints them. A plan the exporter refuses is not a check
// failure: the note says why the lints did not run.
func planLints(p *dungeon.Plan, planner *navmesh.Planner, dir, spawnDir string, floorAware, approachHops bool) ([]planlint.Finding, string, error) {
	planner.SetAvoid(p.Map, dungeon.NavBoxes(p.Avoid))
	rows, err := planexport.Build(p, planexport.Options{Source: "plan-check", Cut: cutWith(planner, floorAware), ApproachHops: approachHops})
	if err != nil {
		return nil, fmt.Sprintf("lints skipped: the exporter refused the plan: %v", err), nil
	}
	return lintRows(rows.Rows, planner, dir, spawnDir, p)
}

// lintRows lints already built rows (an export's steps). p is the plan behind them (nil for a migration: the row's own
// pull rules stand in).
func lintRows(rows planexport.Rows, planner *navmesh.Planner, dir, spawnDir string, p *dungeon.Plan) ([]planlint.Finding, string, error) {
	world, note := worldForLints(dir, spawnDir, rows.Plan.Map)
	opt := planlint.Options{Map: rows.Plan.Map, Level: 60, World: world}
	if p != nil {
		opt.Raid, opt.KillFirst, opt.PullCeiling = p.IsRaid(), p.KillFirst, p.PullCeiling
	}
	fs, err := planlint.Lint(rows, planner, opt)
	if err != nil {
		return nil, note, err
	}
	return fs, note, nil
}

// worldForLints loads the map's world export for the aggro and patrol lints; nil (with the reason) when it is not there.
func worldForLints(dir, spawnDir string, mapID uint32) (*worldspawn.World, string) {
	d := spawnDir
	if d == "" {
		d = filepath.Join(dir, "..", "tools", "dungeongen", "testdata", fmt.Sprintf("map%d", mapID))
	}
	if _, err := os.Stat(filepath.Join(d, "creatures.tsv")); err != nil {
		return nil, fmt.Sprintf("aggro and patrol lints skipped: no world export in %s (pass --spawns DIR: tools/dungeongen/export.sh writes it)", d)
	}
	w, notes, err := worldspawn.Load(d, mapID)
	if err != nil {
		return nil, fmt.Sprintf("aggro and patrol lints skipped: %v", err)
	}
	return w, strings.Join(notes, "; ")
}

// printLints prints a plan's lint findings with the counts per lint.
func printLints(w io.Writer, name string, fs []planlint.Finding, note string, strict bool) {
	if note != "" {
		fmt.Fprintf(w, "%s: lints: %s\n", name, note)
	}
	if len(fs) == 0 {
		return
	}
	sev := "warnings"
	if n := lintErrors(fs); n > 0 && !strict {
		sev = fmt.Sprintf("findings (%d fail the check)", n)
	} else if strict {
		sev = "failures (--strict)"
	}
	fmt.Fprintf(w, "%s: %d lint %s (%s)\n%s\n", name, len(fs), sev, lintCountsText(planlint.Counts(fs)), planlint.Summary(fs))
}

// planLintCmd lints the rows of one migration (plan-check --sql), the exported rows as the server runs them.
func planLintCmd(sqlFile, planName, spawns, dir string, planner *navmesh.Planner, strict, asJSON bool, stdout, stderr io.Writer) int {
	b, err := os.ReadFile(sqlFile)
	if err != nil {
		fmt.Fprintf(stderr, "plan-check: %v\n", err)
		return exitUsage
	}
	all, notes, err := planexport.ParseMigration(string(b))
	if err != nil {
		fmt.Fprintf(stderr, "plan-check: %s: %v\n", sqlFile, err)
		return exitUsage
	}
	var rows []planexport.Rows
	for _, r := range all {
		if planName == "" || r.Plan.Plan == planName {
			rows = append(rows, r)
		}
	}
	switch {
	case len(rows) == 0 && planName != "":
		fmt.Fprintf(stderr, "plan-check: %s holds no plan %q\n", sqlFile, planName)
		return exitUsage
	case len(rows) == 0:
		fmt.Fprintf(stderr, "plan-check: %s holds no plan rows\n", sqlFile)
		return exitUsage
	case len(rows) > 1:
		names := make([]string, len(rows))
		for i, r := range rows {
			names[i] = r.Plan.Plan
		}
		fmt.Fprintf(stderr, "plan-check: %s holds %d plans (%s): pass --plan NAME\n", sqlFile, len(rows), strings.Join(names, ", "))
		return exitUsage
	}
	r := rows[0]
	// One query tells whether the navmesh can answer at all (no cgo, no files for the map): that is a usage error, not a report
	// of every row as off the mesh.
	if p, ok := firstPoint(r); ok {
		if _, err := planner.ServerPath(r.Plan.Map, vecNav(p), vecNav(p)); err != nil {
			if reason, unavailable := planCheckUnavailable(err); unavailable {
				fmt.Fprintf(stderr, "plan-check: %s\n", reason)
			} else {
				fmt.Fprintf(stderr, "plan-check: %v\n", err)
			}
			return exitUsage
		}
	}
	fs, note, err := lintRows(r, planner, dir, spawns, nil)
	if err != nil {
		fmt.Fprintf(stderr, "plan-check: %v\n", err)
		return exitUsage
	}
	if len(notes) > 0 {
		note = strings.TrimSpace(strings.TrimSpace(note) + "; " + strings.Join(notes, "; "))
	}
	rep := planLintReport{Plan: r.Plan.Plan, Map: r.Plan.Map, Steps: len(r.Steps), Lints: fs, LintCounts: planlint.Counts(fs), Note: note}
	if asJSON {
		_ = json.NewEncoder(stdout).Encode(rep)
	} else {
		fmt.Fprintf(stdout, "%s: %d rows on map %d\n", rep.Plan, rep.Steps, rep.Map)
		printLints(stdout, rep.Plan, fs, note, strict)
	}
	if (strict && len(fs) > 0) || lintErrors(fs) > 0 {
		return exitPlanCheck
	}
	return exitOK
}

// vecNav is a point as the navmesh takes it.
func vecNav(p dungeon.Point) navmesh.Vec3 { return navmesh.Vec3{p.X, p.Y, p.Z} }

// planCheckUnavailable says the plan check was not run, and why, when the navmesh cannot answer: a build without cgo, or no files for the map.
// Any other error is the check's own.
func planCheckUnavailable(err error) (string, bool) {
	var missing *navmesh.DataMissingError
	switch {
	case errors.Is(err, navmesh.ErrNoEngine), errors.As(err, &missing):
		return err.Error(), true
	}
	return "", false
}

// planCheckParams is plan_check's: the plan's name and, optionally, the leg to check from (as dungeon_run's from_leg).
type planCheckParams struct {
	Name    string    `json:"name"`
	FromLeg *legParam `json:"from_leg,omitempty"`
	// FromPullGroup (0-based; needs from_leg) checks from that pull group of the leg on.
	FromPullGroup *int `json:"from_pull_group,omitempty"`
}

// planCheckResult is plan_check's answer: the check, or the reason it could not run (Checked false).
type planCheckResult struct {
	dungeon.PlanCheck
	Checked bool   `json:"checked"`
	Skipped string `json:"skipped,omitempty"`
}

// maxRefusedIssues is how many failing points a refusal names; plan_check lists all of them.
const maxRefusedIssues = 12

// checkOptions is where the run starts and (when tuned: the commander driver) its tuned look: look_standoff and look_arrive of the tunables file it reads (the one named, else
// the login's --tune, else <plan>.tune.json beside the plan; a missing or bad file leaves the defaults, the run itself reports a bad one).
func (r *dungeonRunner) checkOptions(planPath, tune string, tuned bool, from int, group *int) dungeon.CheckOptions {
	opt := dungeon.CheckOptions{FromLeg: from, FromPullGroup: group}
	if !tuned { // only the commander driver reads a tunables file
		return opt
	}
	path, err := r.tuneFile(dungeonRunParams{Tune: tune}, planPath)
	if err != nil {
		return opt
	}
	if path == "" {
		path = dungeon.DefaultTunePath(planPath)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return opt
	}
	if vals, err := dungeon.ParseTuneFile(data); err == nil {
		opt.LookStandoff, opt.LookArrive = float32(vals["look_standoff"]), float32(vals["look_arrive"])
	}
	return opt
}

// check runs the plan check on a scratch planner (its own tile cache: a run going at the same time keeps its tiles) over the same mmaps_dir
// the run plans on. A nil planner or missing navmesh files leave the plan unchecked, with the reason in skipped.
func (r *dungeonRunner) check(plan *dungeon.Plan, opt dungeon.CheckOptions) (planCheckResult, error) {
	if err := opt.Validate(plan); err != nil {
		return planCheckResult{}, err
	}
	var nav dungeon.Navmesh
	switch {
	case r.checkNav != nil:
		n, closeNav, err := r.checkNav()
		if err != nil {
			return planCheckResult{}, err
		}
		defer closeNav()
		nav = n
	case r.remote != nil && r.remote.planner != nil:
		pl := r.remote.planner.Scratch()
		defer pl.Close()
		nav = pl
	default:
		return planCheckResult{Skipped: `no navmesh planner (set "mmaps_dir" in the settings file)`}, nil
	}
	res, err := dungeon.CheckPlanWith(plan, nav, opt)
	if why, skip := planCheckUnavailable(err); skip {
		return planCheckResult{Skipped: why}, nil
	}
	if err != nil {
		return planCheckResult{}, err
	}
	return planCheckResult{PlanCheck: *res, Checked: true}, nil
}

// planCheck is the plan_check method.
func (r *dungeonRunner) planCheck(p planCheckParams) (planCheckResult, error) {
	if !planName.MatchString(p.Name) {
		return planCheckResult{}, &rpc.Error{Code: rpc.CodeInvalidParams, Message: `name must be a plan file name such as "zulfarrak" (a-z, 0-9, _ and -)`}
	}
	planPath := filepath.Join(r.dir, p.Name+".json")
	plan, err := dungeon.Load(planPath)
	if err != nil {
		return planCheckResult{}, &rpc.Error{Code: rpc.CodeInvalidParams, Message: "dungeon plan: " + err.Error()}
	}
	if p.FromPullGroup != nil && p.FromLeg == nil {
		return planCheckResult{}, &rpc.Error{Code: rpc.CodeInvalidParams, Message: "from_pull_group needs from_leg (a pull group number means nothing without its leg)"}
	}
	from := 0
	if p.FromLeg != nil {
		if from = legIndex(plan, p.FromLeg); from < 0 {
			return planCheckResult{}, &rpc.Error{Code: rpc.CodeInvalidParams, Message: fmt.Sprintf("from_leg: the plan has legs %v (numbers 0-%d)", legNames(plan), len(plan.Legs)-1)}
		}
	}
	res, err := r.check(plan, r.checkOptions(planPath, "", true, from, p.FromPullGroup))
	if errors.Is(err, dungeon.ErrCheckStart) {
		return planCheckResult{}, &rpc.Error{Code: rpc.CodeInvalidParams, Message: err.Error()}
	}
	if err != nil {
		return planCheckResult{}, &rpc.Error{Code: rpc.CodeGame, Message: "plan check: " + err.Error()}
	}
	if res.Issues == nil {
		res.Issues = []dungeon.PlanIssue{}
	}
	if res.Warnings == nil {
		res.Warnings = []dungeon.PlanIssue{}
	}
	if res.Exempt == nil {
		res.Exempt = []dungeon.PlanIssue{}
	}
	if res.RunLeader == nil {
		res.RunLeader = []dungeon.PlanIssue{}
	}
	return res, nil
}

// checkForRun is dungeon_run's plan check. A live-checked plan is refused when it has a failing point or cannot be checked (no planner, no
// navmesh files, an error in the check) unless force is set, which runs it and says so; a plan of any other status (draft, mapped) is run
// anyway, with the findings in the returned line. The line goes in the result's plan_check.
func (r *dungeonRunner) checkForRun(plan *dungeon.Plan, opt dungeon.CheckOptions, force bool) (string, error) {
	live := strings.HasPrefix(plan.Status, dungeon.StatusLiveChecked)
	status := "no status"
	if f := strings.Fields(plan.Status); len(f) > 0 {
		status = f[0]
	}
	// finding is the line of a check that did not pass: forced past, run anyway (not live-checked), or refused.
	finding := func(what string, fixable bool) (string, error) {
		switch {
		case force:
			return "forced past plan check " + what, nil
		case !live:
			return fmt.Sprintf("warning (the plan is %s, not live-checked, so it runs): plan check %s", status, what), nil
		}
		hint := "pass force to run anyway"
		if fixable {
			hint = "fix the plan, or " + hint
		}
		return "", &rpc.Error{Code: rpc.CodeInvalidParams, Message: "plan check " + what + "\n" + hint}
	}
	res, err := r.check(plan, opt)
	switch {
	case errors.Is(err, dungeon.ErrCheckStart):
		return "", &rpc.Error{Code: rpc.CodeInvalidParams, Message: err.Error()}
	case err != nil:
		return finding("could not run: error in the check: "+err.Error(), false)
	case !res.Checked:
		return finding("could not run: "+res.Skipped, false)
	case res.OK():
		return fmt.Sprintf("passed: %d points on the navmesh, %d warnings, %d hops exempted by the plan (plan_check lists them)", res.Points, len(res.Warnings), len(res.Exempt)), nil
	}
	shown := res.PlanCheck
	if len(shown.Issues) > maxRefusedIssues {
		shown.Issues = shown.Issues[:maxRefusedIssues]
	}
	more := ""
	if n := len(res.Issues) - len(shown.Issues); n > 0 {
		more = fmt.Sprintf("\n... and %d more (plan_check lists all)", n)
	}
	return finding(fmt.Sprintf("failed, %d of %d points fail (%d root, %d re-anchored after an earlier failure):\n%s%s", len(res.Issues), res.Points, res.RootIssues(), res.Reanchored, shown.Summary(), more), true)
}
