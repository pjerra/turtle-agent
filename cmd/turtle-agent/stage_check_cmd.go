package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"path/filepath"
	"strconv"

	"github.com/pjerra/turtle-agent/internal/dungeon"
	"github.com/pjerra/turtle-agent/internal/navmesh"
)

// exitStageRefused is the exit code of stage-check when the leg refuses to
// stage (11 and 12 are taken by plan-check and route-oracle: check main.go).
const exitStageRefused = 13

const stageCheckUsage = "usage: turtle-agent stage-check [--dir dungeons] --mmaps DIR [--cleared-through LEG] [--server-steps N] [--json] <plan> <leg>"

// stageCheckCmd checks where dungeon_stage would put the party for the leg,
// offline: the leg's first row (else its first plan point), the checks of
// dungeon.StageLeg, the back-off proposal and the earlier-leg proposal.
func stageCheckCmd(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("stage-check", flag.ContinueOnError)
	fs.SetOutput(stderr)
	dir := fs.String("dir", "dungeons", "the folder with the plan files")
	mmaps := fs.String("mmaps", "", "folder with the server's mmaps (<map>.mmap and <map>*.mmtile); required")
	cleared := fs.String("cleared-through", "", "legs 0..this one count as cleared (their packs dead)")
	serverSteps := fs.Int("server-steps", 0, "the step count .bot run list shows for the plan (picks the exporter option set)")
	asJSON := fs.Bool("json", false, "print the result as JSON")
	if err := fs.Parse(args); err != nil {
		return exitUsage
	}
	if fs.NArg() != 2 || *mmaps == "" {
		fmt.Fprintln(stderr, stageCheckUsage)
		return exitUsage
	}
	name, legArg := fs.Arg(0), fs.Arg(1)
	if !planName.MatchString(name) {
		fmt.Fprintf(stderr, "stage-check: %q is not a plan name (a-z, 0-9, _ and -)\n", name)
		return exitUsage
	}
	plan, err := dungeon.Load(filepath.Join(*dir, name+".json"))
	if err != nil {
		fmt.Fprintf(stderr, "stage-check: %v\n", err)
		return exitUsage
	}
	leg, err := stageLegArg(plan, legArg)
	if err != nil {
		fmt.Fprintf(stderr, "stage-check: %v\n", err)
		return exitUsage
	}
	clearedThrough := -1
	if *cleared != "" {
		clearedThrough, err = stageLegArg(plan, *cleared)
		if err != nil {
			fmt.Fprintf(stderr, "stage-check: cleared-through: %v\n", err)
			return exitUsage
		}
	}
	planner := navmesh.NewPlanner(*mmaps)
	defer planner.Close()
	if boxes := dungeon.NavBoxes(plan.Avoid); len(boxes) > 0 {
		planner.SetAvoid(plan.Map, boxes)
	}
	fr, haveFR, fnote := stageFirstRow(plan, leg, planner, *serverSteps)
	opt := dungeon.StageOptions{ClearedThrough: clearedThrough}
	if haveFR {
		opt.FirstRow = &fr
	}
	res, err := dungeon.StageLeg(plan, leg, planner, opt)
	if err != nil {
		fmt.Fprintf(stderr, "stage-check: %v\n", err)
		return exitUsage
	}
	firstSource := "plan"
	if haveFR {
		firstSource = "rows"
	}
	if *asJSON {
		_ = json.NewEncoder(stdout).Encode(buildStageCheckJSON(plan, res, firstSource, fnote))
	} else {
		printStageCheck(stdout, plan, res, firstSource, fnote)
	}
	if res.OK {
		return exitOK
	}
	return exitStageRefused
}

// stageLegArg resolves a CLI leg (an index or a name).
func stageLegArg(plan *dungeon.Plan, arg string) (int, error) {
	if n, err := strconv.Atoi(arg); err == nil {
		if n < 0 || n >= len(plan.Legs) {
			return 0, fmt.Errorf("leg: the plan has legs %v (numbers 0-%d)", legNames(plan), len(plan.Legs)-1)
		}
		return n, nil
	}
	for i, l := range plan.Legs {
		if l.Name == arg {
			return i, nil
		}
	}
	return 0, fmt.Errorf("leg: the plan has legs %v (numbers 0-%d)", legNames(plan), len(plan.Legs)-1)
}

// stageCheckJSONOut is the --json output: the same fields as the RPC with
// staged false and dry_run true.
type stageCheckJSONOut struct {
	Staged      bool               `json:"staged"`
	DryRun      bool               `json:"dry_run"`
	Plan        string             `json:"plan"`
	Map         uint32             `json:"map"`
	Leg         int                `json:"leg"`
	LegName     string             `json:"leg_name"`
	First       pointJSON          `json:"first"`
	FirstSource string             `json:"first_source"`
	Point       pointJSON          `json:"point"`
	Kind        string             `json:"kind,omitempty"`
	BackYd      float64            `json:"back_yd"`
	Refusal     string             `json:"refusal,omitempty"`
	Proposal    *stageProposalJSON `json:"proposal,omitempty"`
	Warnings    []string           `json:"warnings"`
	Notes       []string           `json:"notes"`
}

func buildStageCheckJSON(plan *dungeon.Plan, res dungeon.StageResult, firstSource, fnote string) stageCheckJSONOut {
	out := stageCheckJSONOut{Plan: plan.Name, Map: res.Map, Leg: res.Leg, LegName: res.LegName,
		First: dungeonPointJSON(res.First), FirstSource: firstSource,
		Point: dungeonPointJSON(res.Point), Kind: res.Kind, BackYd: res.BackYd,
		Refusal: res.Refusal, Warnings: res.Warnings, DryRun: true}
	if fnote != "" {
		out.Notes = append(out.Notes, fnote)
	}
	if out.Warnings == nil {
		out.Warnings = []string{}
	}
	if out.Notes == nil {
		out.Notes = []string{}
	}
	if res.Proposal != nil {
		out.Proposal = &stageProposalJSON{Kind: res.Proposal.Kind, Leg: res.Proposal.Leg,
			LegName: res.Proposal.LegName, Point: dungeonPointJSON(res.Proposal.Point), BackYd: res.Proposal.BackYd}
	}
	return out
}

func printStageCheck(w io.Writer, plan *dungeon.Plan, res dungeon.StageResult, firstSource, fnote string) {
	if res.OK {
		fmt.Fprintf(w, "%s leg %d %s: stage at %.1f %.1f %.1f (%s; first point from %s)\n",
			plan.Name, res.Leg, res.LegName, res.Point.X, res.Point.Y, res.Point.Z, res.Kind, firstSource)
	} else {
		fmt.Fprintf(w, "%s leg %d %s: refused: %s\n", plan.Name, res.Leg, res.LegName, res.Refusal)
	}
	if res.Proposal != nil {
		fmt.Fprintf(w, "proposal: %s leg %d %s at %.1f %.1f %.1f (%.0f yd back)\n",
			res.Proposal.Kind, res.Proposal.Leg, res.Proposal.LegName,
			res.Proposal.Point.X, res.Proposal.Point.Y, res.Proposal.Point.Z, res.Proposal.BackYd)
	}
	if fnote != "" {
		fmt.Fprintf(w, "note: %s\n", fnote)
	}
	for _, n := range res.Warnings {
		fmt.Fprintf(w, "note: %s\n", n)
	}
}
