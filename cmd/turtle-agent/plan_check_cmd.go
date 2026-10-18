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
	"github.com/pjerra/turtle-agent/internal/rpc"
)

// exitPlanCheck: plan-check found a point the navmesh cannot take (a failing plan is not a usage error).
const exitPlanCheck = 11

const planCheckUsage = "usage: turtle-agent plan-check [--dir dungeons] --mmaps DIR [--from-leg N [--from-pull-group N]] [--json] <plan>..."

// planCheckCmd walks the named plans on the navmesh (README, "Plan check") and prints each failing point. It reads the plan files and
// the mmaps folder and nothing else.
func planCheckCmd(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("plan-check", flag.ContinueOnError)
	fs.SetOutput(stderr)
	dir := fs.String("dir", "dungeons", "the folder with the plan files")
	mmaps := fs.String("mmaps", "", "folder with the server's mmaps (<map>.mmap and <map>*.mmtile); required")
	from := fs.Int("from-leg", 0, "check from this leg on (the legs before it only place the party)")
	fromGroup := fs.Int("from-pull-group", -1, "with --from-leg: check from this pull group (0-based) of that leg on (default: the leg's own walk too)")
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
	if fs.NArg() == 0 || *mmaps == "" {
		fmt.Fprintln(stderr, planCheckUsage)
		return exitUsage
	}
	planner := navmesh.NewPlanner(*mmaps)
	defer planner.Close()
	var results []*dungeon.PlanCheck
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
		results = append(results, res)
		failed = failed || !res.OK()
		if *asJSON {
			continue
		}
		if res.OK() {
			fmt.Fprintf(stdout, "%s: ok, %d points on the map %d navmesh\n", n, res.Points, res.Map)
		} else {
			fmt.Fprintf(stdout, "%s: %d of %d points fail\n%s\n", n, len(res.Issues), res.Points, res.Summary())
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
	}
	if *asJSON {
		_ = json.NewEncoder(stdout).Encode(results)
	}
	if failed {
		return exitPlanCheck
	}
	return exitOK
}

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
	return finding(fmt.Sprintf("failed, %d of %d points fail:\n%s%s", len(res.Issues), res.Points, shown.Summary(), more), true)
}
