package main

import (
	"context"
	"fmt"
	"path/filepath"
	"sync"
	"time"

	"github.com/pjerra/turtle-agent/internal/dungeon"
	"github.com/pjerra/turtle-agent/internal/navmesh"
	"github.com/pjerra/turtle-agent/internal/objects"
	"github.com/pjerra/turtle-agent/internal/planexport"
	"github.com/pjerra/turtle-agent/internal/rpc"
	"github.com/pjerra/turtle-agent/internal/tbm"
)

// stageParams are dungeon_stage's params.
type stageParams struct {
	Name           string    `json:"name"`
	Leg            *legParam `json:"leg"`
	ClearedThrough *legParam `json:"cleared_through,omitempty"`
	UseSafePoint   bool      `json:"use_safe_point"`
	Force          bool      `json:"force"`
	DryRun         bool      `json:"dry_run"`
	Start          bool      `json:"start"`
	Resume         bool      `json:"resume"`
	Speed          float64   `json:"speed"`
	QuietWaitS     *int      `json:"quiet_wait_s,omitempty"`
}

// stagePartyJSON is one staged bot in the result.
type stagePartyJSON struct {
	Name    string  `json:"name"`
	X       float64 `json:"x"`
	Y       float64 `json:"y"`
	Z       float64 `json:"z"`
	Dist    float64 `json:"dist"`
	Arrived bool    `json:"arrived"`
}

// stageRunJSON is the start or resume the staging ended with.
type stageRunJSON struct {
	Sent      string `json:"sent"`
	StartStep int    `json:"start_step,omitempty"`
	Steps     int    `json:"steps,omitempty"`
	Leg       string `json:"leg,omitempty"`
	Line      string `json:"line,omitempty"`
}

// stageProposalJSON is a leg to stage at instead.
type stageProposalJSON struct {
	Kind    string    `json:"kind"`
	Leg     int       `json:"leg"`
	LegName string    `json:"leg_name"`
	Point   pointJSON `json:"point"`
	BackYd  float64   `json:"back_yd"`
}

// pointJSON is a point in the result.
type pointJSON struct {
	X float64 `json:"x"`
	Y float64 `json:"y"`
	Z float64 `json:"z"`
}

// stageResult is dungeon_stage's answer.
type stageResult struct {
	Staged      bool               `json:"staged"`
	Started     bool               `json:"started"`
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
	FloorZ      *float64           `json:"floor_z,omitempty"`
	Refusal     string             `json:"refusal"`
	Proposal    *stageProposalJSON `json:"proposal,omitempty"`
	Warnings    []string           `json:"warnings"`
	Notes       []string           `json:"notes"`
	GMCommands  []string           `json:"gm_commands"`
	Party       []stagePartyJSON   `json:"party"`
	Run         *stageRunJSON      `json:"run,omitempty"`
	Seconds     float64            `json:"seconds"`
}

func dungeonPointJSON(p dungeon.Point) pointJSON {
	return pointJSON{X: float64(p.X), Y: float64(p.Y), Z: float64(p.Z)}
}

func stageResultJSON(rep dungeon.StageReport, plan string) stageResult {
	out := stageResult{
		Staged: rep.Staged, Started: rep.Started, DryRun: rep.DryRun,
		Plan: plan, Map: rep.Map, Leg: rep.Leg, LegName: rep.LegName,
		First: dungeonPointJSON(rep.First), FirstSource: rep.FirstSource,
		Point: dungeonPointJSON(rep.Point), Kind: rep.Kind, BackYd: rep.BackYd,
		FloorZ: rep.FloorZ, Refusal: rep.Refusal,
		Warnings: rep.Warnings, Notes: rep.Notes, GMCommands: rep.GMCommands,
		Seconds: rep.Seconds,
	}
	if out.Warnings == nil {
		out.Warnings = []string{}
	}
	if out.Notes == nil {
		out.Notes = []string{}
	}
	if out.GMCommands == nil {
		out.GMCommands = []string{}
	}
	if rep.Proposal != nil {
		out.Proposal = &stageProposalJSON{Kind: rep.Proposal.Kind, Leg: rep.Proposal.Leg,
			LegName: rep.Proposal.LegName, Point: dungeonPointJSON(rep.Proposal.Point), BackYd: rep.Proposal.BackYd}
	}
	for _, m := range rep.Party {
		out.Party = append(out.Party, stagePartyJSON{Name: m.Name, X: m.X, Y: m.Y, Z: m.Z, Dist: m.Dist, Arrived: m.Arrived})
	}
	if out.Party == nil {
		out.Party = []stagePartyJSON{}
	}
	if rep.Run != nil {
		out.Run = &stageRunJSON{Sent: rep.Run.Sent, StartStep: rep.Run.StartStep, Steps: rep.Run.Steps, Leg: rep.Run.Leg, Line: rep.Run.Line}
	}
	return out
}

// stageFirstRow is the row helper of 3.2 (it owns the exporter, reusing
// observePlanRows): the first row of the leg in the server's rows when they
// can be reproduced, else the leg's first plan point with a note saying why.
func stageFirstRow(plan *dungeon.Plan, leg int, planner *navmesh.Planner, serverSteps int) (dungeon.Point, bool, string) {
	if plan.Legs[leg].Optional != nil {
		return dungeon.Point{}, false, fmt.Sprintf("leg %s is optional: the exporter of this build leaves it out of the rows; the leg's first plan point is used", plan.Legs[leg].Name)
	}
	rowLeg, ok := planexport.LegNumber(plan, leg)
	if !ok {
		return dungeon.Point{}, false, fmt.Sprintf("leg %s has no rows here; the leg's first plan point is used", plan.Legs[leg].Name)
	}
	cands, _ := observePlanRows(plan, planner)
	var kept []planexport.Rows
	for _, c := range cands {
		if serverSteps > 0 && c.StepCount() != serverSteps {
			continue
		}
		kept = append(kept, c)
	}
	var firsts []dungeon.Point
	for _, k := range kept {
		for _, st := range k.Steps {
			if st.Leg == rowLeg && (st.Kind == planexport.KindMove || st.Kind == planexport.KindMoveDirect || st.Kind == planexport.KindMoveHopExact) {
				firsts = append(firsts, dungeon.Point{X: st.X, Y: st.Y, Z: st.Z})
				break
			}
		}
	}
	if len(firsts) == 0 {
		if serverSteps > 0 {
			return dungeon.Point{}, false, fmt.Sprintf("the server's %d steps match no exporter option set of the plan file: the leg's first plan point is used", serverSteps)
		}
		return dungeon.Point{}, false, fmt.Sprintf("leg %s has no rows here; the leg's first plan point is used", plan.Legs[leg].Name)
	}
	for _, f := range firsts[1:] {
		if pointDist(f, firsts[0]) > 1 {
			return dungeon.Point{}, false, "the exporter option sets put the leg's first row at different points: the leg's first plan point is used (pass the server's step count)"
		}
	}
	return firsts[0], true, ""
}

// registerStageMethods registers dungeon_stage. One mutex TryLock: a second
// call while one runs is refused; runner.running() refuses while a
// dungeon_run goes.
func registerStageMethods(srv *rpc.Server, cmdr *commanderService, runner *dungeonRunner, planner *navmesh.Planner) {
	var mu sync.Mutex
	srv.Register("dungeon_stage", rpc.Typed(func(ctx context.Context, p stageParams) (stageResult, error) {
		if !mu.TryLock() {
			return stageResult{}, &rpc.Error{Code: rpc.CodeGame, Message: "a dungeon_stage is going"}
		}
		defer mu.Unlock()
		if runner.running() {
			return stageResult{}, &rpc.Error{Code: rpc.CodeGame, Message: "a dungeon_run is going: dungeon_stop it first"}
		}
		return stageRun(ctx, cmdr, runner, planner, p)
	}))
}

func stageRun(ctx context.Context, cmdr *commanderService, runner *dungeonRunner, planner *navmesh.Planner, p stageParams) (stageResult, error) {
	t0 := time.Now()
	bad := func(format string, a ...any) (stageResult, error) {
		return stageResult{}, &rpc.Error{Code: rpc.CodeInvalidParams, Message: fmt.Sprintf(format, a...)}
	}
	if !planName.MatchString(p.Name) {
		return bad(`name must be a plan file name such as "zulfarrak" (a-z, 0-9, _ and -)`)
	}
	plan, err := dungeon.Load(filepath.Join(runner.dir, p.Name+".json"))
	if err != nil {
		return stageResult{}, &rpc.Error{Code: rpc.CodeInvalidParams, Message: "dungeon plan: " + err.Error()}
	}
	if p.Leg == nil {
		return bad("leg is required (a leg number from 0 or a leg name)")
	}
	leg := legIndex(plan, p.Leg)
	if leg < 0 {
		return bad("leg: the plan has legs %v (numbers 0-%d)", legNames(plan), len(plan.Legs)-1)
	}
	cleared := -1
	if p.ClearedThrough != nil {
		if cleared = legIndex(plan, p.ClearedThrough); cleared < 0 {
			return bad("leg: the plan has legs %v (numbers 0-%d)", legNames(plan), len(plan.Legs)-1)
		}
		if cleared >= leg {
			return bad("cleared_through %d must be before the staged leg %d", cleared, leg)
		}
	}
	if p.Start && p.Resume {
		return bad("start and resume together: pass one")
	}
	if p.Speed != 0 && !p.Start {
		return bad("speed without start: speed appends to the run start line only")
	}
	if p.Speed != 0 && (p.Speed < 1.0 || p.Speed > 3.0) {
		return bad("speed %.1f: 1.0 to 3.0", p.Speed)
	}
	quietWait := 20 * time.Second
	if p.QuietWaitS != nil {
		if *p.QuietWaitS < 0 || *p.QuietWaitS > 60 {
			return bad("quiet_wait_s %d: 0 to 60", *p.QuietWaitS)
		}
		quietWait = time.Duration(*p.QuietWaitS) * time.Second
	}
	speed := 0.0
	if p.Start {
		speed = float64(int(p.Speed*10+0.5)) / 10
	}
	if planner == nil {
		rep := dungeon.StageReport{StageResult: dungeon.StageResult{Leg: leg, LegName: plan.Legs[leg].Name, Map: plan.Map,
			Refusal: "no navmesh planner (set \"mmaps_dir\" in the settings file): staging needs the server's path query"}}
		out := stageResultJSON(rep, plan.Name)
		out.Seconds = time.Since(t0).Seconds()
		return out, nil
	}
	nav := planner.Scratch()
	defer nav.Close()
	if boxes := dungeon.NavBoxes(plan.Avoid); len(boxes) > 0 {
		nav.SetAvoid(plan.Map, boxes)
	}
	fr := func(serverSteps int) (dungeon.Point, bool, string) {
		return stageFirstRow(plan, leg, planner, serverSteps)
	}
	st := &dungeon.Stager{G: remoteGame{c: cmdr}, Now: time.Now, Sleep: ctxSleep, Poll: 500 * time.Millisecond}
	rep, err := st.Stage(ctx, dungeon.StageRequest{
		Plan: plan, Leg: leg,
		Opt:       dungeon.StageOptions{ClearedThrough: cleared, UseSafePoint: p.UseSafePoint, Force: p.Force},
		Nav:       nav,
		FirstRow:  fr,
		DryRun:    p.DryRun,
		Start:     p.Start,
		Resume:    p.Resume,
		Speed:     speed,
		QuietWait: quietWait,
	})
	if err != nil {
		return stageResult{}, rpcErr(err)
	}
	out := stageResultJSON(rep, plan.Name)
	out.Seconds = time.Since(t0).Seconds()
	return out, nil
}

// ctxSleep sleeps d unless the context ends first.
func ctxSleep(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

// CommandWait implements dungeon.StageGame: ".bot <command>" with its own wait.
func (g remoteGame) CommandWait(ctx context.Context, command string, wait time.Duration) (tbm.Reply, error) {
	return g.c.cmdr.DoWait(ctx, command, wait)
}

// StageUnits implements dungeon.StageGame: the units in view mapped over.
func (g remoteGame) StageUnits() []dungeon.StageUnit {
	st := g.c.cur().Status()
	out := make([]dungeon.StageUnit, 0, 64)
	for _, u := range g.c.cur().Units(objects.Filter{Limit: 500}) {
		out = append(out, dungeon.StageUnit{
			Name: u.Name, Entry: u.Entry, Player: u.Type == "player",
			At:      dungeon.Point{X: u.X, Y: u.Y, Z: u.Z},
			Map:     st.Map,
			Hostile: u.Hostile, Dead: u.Dead, Ghost: u.Ghost,
			InCombat: u.InCombat, Moving: u.Moving,
		})
	}
	return out
}

var _ dungeon.StageGame = remoteGame{}
