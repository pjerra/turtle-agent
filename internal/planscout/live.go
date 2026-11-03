package planscout

import (
	"fmt"
	"math"
	"sort"

	"github.com/pjerra/turtle-agent/internal/aggroest"
	"github.com/pjerra/turtle-agent/internal/dungeon"
	"github.com/pjerra/turtle-agent/internal/navmesh"
	"github.com/pjerra/turtle-agent/internal/objects"
	"github.com/pjerra/turtle-agent/internal/planexport"
	"github.com/pjerra/turtle-agent/internal/planlint"
	"github.com/pjerra/turtle-agent/internal/raidwalk"
	"github.com/pjerra/turtle-agent/internal/worldspawn"
)

// This file is the live half of the scout (spec 5.6): the lists the live pass walks (hops, spots, stops) and MergeLive, which
// merges the GM's server answers onto the offline step records. Nothing here talks to a server; plan_scout_live.go does.

// LiveSpotYd is how far a spot may stand from a stop's first spot and still be read from there (raidwalk.GMClearStopYd).
const LiveSpotYd = raidwalk.GMClearStopYd

// The live checks' names.
const (
	CheckLivePath    = "live_path"
	CheckLiveWalk    = "live_walk"
	CheckLiveAggro   = "live_aggro"
	CheckLiveLOS     = "live_los"
	CheckLiveJoiners = "live_joiners"
)

// ScoutPath is one TBM:SCOUT_PATH answer, reduced to the plan-scout fields.
type ScoutPath struct {
	Verdict   string
	PathType  uint32
	Length    float64
	End       dungeon.Point
	EndOffset float64
	SnappedZ  float64
	StartSwim bool
	EndSwim   bool
}

// Underwater says the answer is the core's straight swim line (type bit 0x80).
func (p ScoutPath) Underwater() bool { return p.PathType&0x80 != 0 }

// NOPATH says the server found no path at all.
func (p ScoutPath) NOPATH() bool { return p.PathType&navmesh.PathNoPath != 0 }

// WalkResult is where a `.bot scout walk` stopped, and the path back to the anchor for an anchor hop.
type WalkResult struct {
	Stop dungeon.Point
	// Back is the SCOUT_PATH answer from the stop back to the anchor (anchor->stand hops only).
	Back *ScoutPath
	// Skipped names why the hop was not walked ("walk budget", ...).
	Skipped string
	// Length is the module's path length at the walk's start (the walk wait's bound).
	Length float64
}

// ScoutCfg is the live server's run-leader rules (TBM:SCOUT_CFG).
type ScoutCfg struct {
	ReactDistance      float64
	RateCreatureAggro  float64
	FamilyAssistRadius float64
}

// LOSKey names one `q los` answer: the step it was asked for and the unit's spawn guid (the low 24 bits of its GUID).
type LOSKey struct {
	Step int
	Guid uint32
}

// LiveData is what the live pass read. Everything is keyed by the 1-based step number (the exported row).
type LiveData struct {
	Cfg   ScoutCfg
	CfgOK bool
	// Paths is the plan hop's answer per step (from the row before it). ExtraPaths are the other origins of 3.2.
	Paths      map[int]ScoutPath
	ExtraPaths map[int][]ScoutPath
	Walks      map[int]WalkResult
	// Units is the `q units` read at that step's spot (stand, anchor or pull row).
	Units map[int][]objects.Unit
	LOS   map[LOSKey]bool
	// Aborted says the live pass stopped part-way; AbortReason says why.
	Aborted     bool
	AbortReason string
	// Notes are the live pass's report notes (leases, instance cap, retries).
	Notes []string
}

// LiveHop is one path query: a hop row from one origin of 3.2.
type LiveHop struct {
	// Step is the hop's target row (1-based); From is the origin point and FromStep its row.
	Step     int
	From     dungeon.Point
	FromStep int
	Point    dungeon.Point
	Kind     string
	Drift    bool
	// Primary is the origin row 3.2 lists first (the plan hop); the others are drift ring points and the anchor.
	Primary bool
	Label   string
	// StandRow says the hop is a stand row of a stand pull; Anchor is its gather anchor then.
	StandRow bool
	Anchor   dungeon.Point
}

// LiveTarget is one unit of a pull's target pack: its spawn guid (the toolkit's db guid) and entry.
type LiveTarget struct {
	Guid  uint32
	Entry uint32
}

// LiveSpot is one point the live pass teleports to and reads.
type LiveSpot struct {
	Step  int
	Kind  string // "stand", "anchor", "pull", "stand_candidate"
	Point dungeon.Point
	// LOSPoint is where `q los` is asked from: the spot itself, or the pull's stand for a pull row.
	LOSPoint dungeon.Point
	// TargetGuids is the pull's target pack with its entries (pull row spots only): the toolkit's `q los` needs a
	// full creature guid (entry and counter).
	TargetGuids []LiveTarget
}

// LiveHops lists every hop origin of the reported steps, in route order. A row without a positioned row before it is
// no hop (the scout enters at it).
func LiveHops(rep *Report, in Input, nav Nav) []LiveHop {
	if rep == nil || nav == nil {
		return nil
	}
	steps := in.Rows.Steps
	var out []LiveHop
	for _, st := range rep.Steps {
		idx := st.Step - 1
		if idx < 0 || idx >= len(steps) {
			continue
		}
		prev := planlint.PrevPositioned(steps, idx)
		if prev < 0 {
			continue
		}
		standRow := liveStandRow(steps, idx)
		anchor := dungeon.Point{}
		if standRow {
			anchor = dungeon.Point{X: steps[prev].X, Y: steps[prev].Y, Z: steps[prev].Z}
		}
		for k, o := range planlint.HopOrigins(in.Rows, nav, idx) {
			out = append(out, LiveHop{Step: st.Step, From: o.Point, FromStep: o.Step, Point: pointOf(steps[idx]),
				Kind: st.Kind, Drift: o.Drift, Primary: k == 0, Label: o.Label, StandRow: standRow, Anchor: anchor})
		}
	}
	return out
}

// liveStandRow says row i is the stand row of a stand pull: a move right before a stand-pull row.
func liveStandRow(steps []planexport.StepRow, i int) bool {
	if i < 0 || i >= len(steps) || !moveKindOf(steps[i].Kind) {
		return false
	}
	for k := i + 1; k < len(steps); k++ {
		if !positioned(steps[k]) {
			continue
		}
		return (steps[k].Kind == planexport.KindPull || steps[k].Kind == planexport.KindBoss) && steps[k].Flags&planexport.FlagStandPull != 0
	}
	return false
}

func moveKindOf(kind string) bool {
	switch kind {
	case planexport.KindMove, planexport.KindMoveDirect, planexport.KindMoveHopExact:
		return true
	}
	return false
}

func positioned(s planexport.StepRow) bool { return s.X != 0 || s.Y != 0 || s.Z != 0 }

func pointOf(s planexport.StepRow) dungeon.Point { return dungeon.Point{X: s.X, Y: s.Y, Z: s.Z} }

// LiveSpots lists the spots the live pass reads: every stand and gather anchor, every pull or boss row, and the stand
// candidates the offline fixes proposed (a move_stand fix's stand).
func LiveSpots(rep *Report, in Input, w *worldspawn.World, extra []Fix) []LiveSpot {
	if rep == nil {
		return nil
	}
	steps := in.Rows.Steps
	gather := planlint.GatherPoints(steps)
	var rows []planlint.PullRow
	var owned map[uint64]int
	if w != nil {
		rows = planlint.PullRowsOf(steps)
		owned = planlint.PullOwners(rows, w.Spawns)
	}
	var out []LiveSpot
	seen := map[string]bool{}
	add := func(s LiveSpot) {
		key := fmt.Sprintf("%d|%s|%.0f|%.0f|%.0f", s.Step, s.Kind, s.Point.X, s.Point.Y, s.Point.Z)
		if seen[key] {
			return
		}
		seen[key] = true
		if s.LOSPoint == (dungeon.Point{}) {
			s.LOSPoint = s.Point
		}
		out = append(out, s)
	}
	for _, st := range rep.Steps {
		idx := st.Step - 1
		if idx < 0 || idx >= len(steps) {
			continue
		}
		row := steps[idx]
		switch row.Kind {
		case planexport.KindPull, planexport.KindBoss, planexport.KindHold:
		default:
			continue
		}
		prev := planlint.PrevPositioned(steps, idx)
		stand := prev >= 0 && gather[prev] && moveKindOf(steps[prev].Kind)
		if stand {
			add(LiveSpot{Step: prev + 1, Kind: "stand", Point: pointOf(steps[prev]), LOSPoint: pointOf(steps[prev])})
		}
		if row.Kind != planexport.KindPull && row.Kind != planexport.KindBoss {
			continue
		}
		losPoint := pointOf(row)
		if stand {
			losPoint = pointOf(steps[prev])
		}
		add(LiveSpot{Step: st.Step, Kind: "pull", Point: pointOf(row), LOSPoint: losPoint,
			TargetGuids: liveTargetPack(planKillFirst(in), rows, owned, w, idx)})
		if stand && row.Flags&planexport.FlagStandPull != 0 {
			if anchor := planlint.PrevPositioned(steps, prev); anchor >= 0 {
				add(LiveSpot{Step: anchor + 1, Kind: "anchor", Point: pointOf(steps[anchor]), LOSPoint: pointOf(steps[anchor])})
			}
		}
	}
	for _, f := range extra {
		if f.Stand == nil {
			continue
		}
		add(LiveSpot{Step: f.Step, Kind: "stand_candidate", Point: dungeon.Point{X: f.Stand.X, Y: f.Stand.Y, Z: f.Stand.Z}})
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Step != out[j].Step {
			return out[i].Step < out[j].Step
		}
		return out[i].Kind < out[j].Kind
	})
	return out
}

// LiveStops groups the spots greedily in route order: a spot within LiveSpotYd (2D) of an earlier stop's spot is read
// from that stop (raidwalk's stop rule).
func LiveStops(spots []LiveSpot) [][]LiveSpot {
	var groups [][]LiveSpot
	for _, s := range spots {
		placed := false
		for gi := range groups {
			for _, at := range groups[gi] {
				if dist2D(s.Point, at.Point) <= LiveSpotYd {
					groups[gi] = append(groups[gi], s)
					placed = true
					break
				}
			}
			if placed {
				break
			}
		}
		if !placed {
			groups = append(groups, []LiveSpot{s})
		}
	}
	return groups
}

// LiveSpotRadius is the `q units` radius of a spot read: the widest reach plus the stand spread plus 5 yd, at most 60.
func LiveSpotRadius(w *worldspawn.World, opt Options) float64 {
	reach := 20.0
	if w != nil {
		for _, sp := range w.Spawns {
			if !sp.Hostile() || sp.LevelMax <= 1 {
				continue
			}
			if r := float64(worldspawn.AggroRadius(sp, liveLevel(opt, nil), liveRate(nil))); r > reach {
				reach = r
			}
		}
	}
	r := reach + liveSpread(opt) + StandPullMarginYd
	return math.Min(r, 60)
}

// liveLevel is the party level the aggro radius is computed at.
func liveLevel(opt Options, in *Input) int {
	if opt.PartyLevel != 0 {
		return opt.PartyLevel
	}
	if in != nil && in.Plan != nil {
		if _, hi := in.Plan.Band(); hi != 0 {
			return hi
		}
	}
	return 60
}

// liveRate is the core's Rate.Creature.Aggro (1 offline or when the module gave none).
func liveRate(cfg *ScoutCfg) float32 {
	if cfg != nil && cfg.RateCreatureAggro > 0 {
		return float32(cfg.RateCreatureAggro)
	}
	return 1
}

// liveSpread is the stand spread: 10 yd raid, 3 yd party.
func liveSpread(opt Options) float64 {
	if opt.Raid {
		return aggroest.StandSpreadRaidYd
	}
	return aggroest.StandSpreadPartyYd
}

// liveTargetPack is the pull's target pack: the target mob's GroupID members (planlint's target rule).
func liveTargetPack(killFirst []uint32, rows []planlint.PullRow, owned map[uint64]int, w *worldspawn.World, idx int) []LiveTarget {
	if w == nil || idx < 0 || idx >= len(rows) {
		return nil
	}
	mobs, entries := planlint.PullMobs(w.Spawns, livePats(w), planlint.PackIDs(w), liveKeep)
	target := planlint.PullTarget(killFirst, mobs, entries, idx, rows[idx], owned)
	if target < 0 {
		return nil
	}
	pack := mobs[target].GroupID
	var out []LiveTarget
	for _, m := range mobs {
		if m.GroupID == pack {
			out = append(out, LiveTarget{Guid: m.Guid, Entry: entries[m.Guid]})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Guid < out[j].Guid })
	return out
}

func livePats(w *worldspawn.World) map[uint64]bool {
	pats := map[uint64]bool{}
	if w != nil {
		for _, pt := range w.Patrols {
			for _, g := range pt.SpawnGUIDs {
				pats[g] = true
			}
		}
	}
	return pats
}

func liveKeep(sp worldspawn.Spawn) bool { return sp.Hostile() && sp.LevelMax > 1 }

// MergeLive merges the live answers onto the offline step records (spec 3.4). A live check that could not run is skip and
// the offline verdict stands. live_aggro is kept beside aggro_stand; live_joiners replaces joiners.
func MergeLive(rep *Report, in Input, w *worldspawn.World, live LiveData, opt Options) {
	if rep == nil {
		return
	}
	steps := in.Rows.Steps
	level := liveLevel(opt, &in)
	var cfg *ScoutCfg
	if live.CfgOK {
		cfg = &live.Cfg
	}
	rate := liveRate(cfg)
	spread := liveSpread(opt)
	gather := planlint.GatherPoints(steps)
	var rows []planlint.PullRow
	var owned map[uint64]int
	if w != nil {
		rows = planlint.PullRowsOf(steps)
		owned = planlint.PullOwners(rows, w.Spawns)
	}
	approachEnd := approachEnds(in.Origins)
	// The pack for a live joiner estimate, rebuilt once per world.
	var mobs []aggroest.Mob
	var entries map[uint32]uint32
	if w != nil {
		mobs, entries = planlint.PullMobs(w.Spawns, livePats(w), planlint.PackIDs(w), liveKeep)
	}
	for si := range rep.Steps {
		st := &rep.Steps[si]
		idx := st.Step - 1
		if idx < 0 || idx >= len(steps) {
			continue
		}
		row := steps[idx]
		prev := planlint.PrevPositioned(steps, idx)
		special := gather[idx] || isAnchorRow(steps, gather, idx) || approachEnd[st.Step]
		if prev >= 0 {
			if p, ok := live.Paths[st.Step]; ok {
				addLive(st, livePathCheck(st, p, special))
			} else {
				addLive(st, Check{Name: CheckLivePath, Source: SourceLive, Verdict: VerdictSkip,
					Reason: "no server answer for this hop" + abortedSuffix(live)})
			}
		}
		if wk, ok := live.Walks[st.Step]; ok {
			anchorHop := liveStandRow(steps, idx) && prev >= 0
			anchor := dungeon.Point{}
			if anchorHop {
				anchor = pointOf(steps[prev])
			}
			addLive(st, liveWalkCheck(st, wk, anchorHop, anchor))
		} else if live.Aborted && liveWalkWanted(st) {
			addLive(st, Check{Name: CheckLiveWalk, Source: SourceLive, Verdict: VerdictSkip,
				Reason: "not walked" + abortedSuffix(live)})
		}
		if gather[idx] || isAnchorRow(steps, gather, idx) {
			switch {
			case w == nil:
				addLive(st, Check{Name: CheckLiveAggro, Source: SourceLive, Verdict: VerdictSkip,
					Reason: "no world export: the live units cannot be joined to their spawns"})
			default:
				pullIdx := livePullFor(steps, idx)
				if units, ok := live.Units[st.Step]; ok {
					addLive(st, liveAggroCheck(st, units, w, owned, pullIdx, level, rate, spread, live))
				} else {
					addLive(st, Check{Name: CheckLiveAggro, Source: SourceLive, Verdict: VerdictSkip,
						Reason: "no live unit read at this spot" + abortedSuffix(live)})
				}
			}
		}
		if row.Kind == planexport.KindPull || row.Kind == planexport.KindBoss {
			var targets []uint32
			for _, t := range liveTargetPack(liveKillFirst(in, opt), rows, owned, w, idx) {
				targets = append(targets, t.Guid)
			}
			addLive(st, liveLOSCheck(st, targets, live))
			if w != nil && len(mobs) > 0 {
				if units, ok := live.Units[st.Step]; ok {
					ceiling := planlint.PullCeilingOf(planCeiling(in), row.Item, opt.Raid)
					if c := liveJoinersCheck(st, row, idx, rows, owned, mobs, entries, units, ceiling, spread, level, rate,
						liveKillFirst(in, opt), live); c.Name != "" {
						removeCheck(st, planlint.LintJoiners)
						addLive(st, c)
					}
				}
			}
		}
	}
	rep.Notes = append(rep.Notes, live.Notes...)
	recount(rep)
}

// liveWalkWanted says the walk pass ranks this step (its checks are in its list, or the live path failed).
func liveWalkWanted(st *StepResult) bool {
	for _, c := range st.Checks {
		switch c.Name {
		case planlint.LintClimb, planlint.LintWater, planlint.LintArrival, "drop", CheckLivePath:
			if c.Verdict == VerdictFail || c.Verdict == VerdictWarn {
				return true
			}
		}
	}
	return false
}

// abortedSuffix says the live pass stopped, with its reason.
func abortedSuffix(live LiveData) string {
	if !live.Aborted {
		return ""
	}
	if live.AbortReason == "" {
		return ": the live pass stopped"
	}
	return ": the live pass stopped: " + live.AbortReason
}

// addLive appends a live check and worsens the step's verdict.
func addLive(st *StepResult, c Check) {
	st.Checks = append(st.Checks, c)
	st.Verdict = Worst(st.Verdict, c.Verdict)
}

func removeCheck(st *StepResult, name string) {
	out := st.Checks[:0]
	for _, c := range st.Checks {
		if c.Name != name || c.Source != SourceOffline {
			out = append(out, c)
		}
	}
	st.Checks = out
}

// recount recomputes the report's counts and re-sorts the steps' checks by name.
func recount(rep *Report) {
	rep.Counts = Counts{}
	for i := range rep.Steps {
		st := &rep.Steps[i]
		sort.SliceStable(st.Checks, func(a, b int) bool { return st.Checks[a].Name < st.Checks[b].Name })
		st.Verdict = VerdictOK
		for _, c := range st.Checks {
			st.Verdict = Worst(st.Verdict, c.Verdict)
		}
		switch st.Verdict {
		case VerdictFail:
			rep.Counts.Fail++
		case VerdictWarn:
			rep.Counts.Warn++
		case VerdictSkip:
			rep.Counts.Skip++
		default:
			rep.Counts.OK++
		}
	}
}

// approachEnds marks the last approach row of each (leg, group): a stand there in water is a stand pull from water.
func approachEnds(origins []planexport.RowOrigin) map[int]bool {
	last := map[[2]int]int{}
	out := map[int]bool{}
	for i, o := range origins {
		if o.Part != "approach" {
			continue
		}
		key := [2]int{o.Leg, o.Group}
		if idx, ok := last[key]; !ok || o.Index > origins[idx].Index {
			last[key] = i
		}
	}
	for _, i := range last {
		out[i+1] = true
	}
	return out
}

// isAnchorRow says row i is the gather anchor before a stand row.
func isAnchorRow(steps []planexport.StepRow, gather []bool, i int) bool {
	next := i + 1
	if next >= len(steps) || !positioned(steps[next]) || !gather[next] || !moveKindOf(steps[next].Kind) {
		return false
	}
	return liveStandRow(steps, next)
}

// livePullFor is the pull row a stand or anchor serves: the first pull, boss or hold row after it, or -1.
func livePullFor(steps []planexport.StepRow, i int) int {
	for k := i + 1; k < len(steps); k++ {
		if !positioned(steps[k]) {
			continue
		}
		switch steps[k].Kind {
		case planexport.KindPull, planexport.KindBoss, planexport.KindHold:
			return k
		}
		return -1
	}
	return -1
}

// livePathCheck judges one server path answer (spec 3.4).
func livePathCheck(st *StepResult, p ScoutPath, special bool) Check {
	c := Check{Name: CheckLivePath, Source: SourceLive, Verdict: VerdictOK,
		Data: map[string]float64{"type": float64(p.PathType), "length": p.Length, "end_offset": p.EndOffset}}
	where := "the point"
	if special {
		where = "a stand or gather anchor"
	}
	switch {
	case p.Verdict != "ok":
		c.Verdict = VerdictFail
		c.Reason = fmt.Sprintf("the server answers %s: the path ends %.1f yd from the point (type 0x%x)", p.Verdict, p.EndOffset, p.PathType)
	case p.Underwater() && special:
		c.Verdict = VerdictFail
		c.Reason = fmt.Sprintf("the server path is the swim line (type 0x%x) to %s: both ends are swimmable", p.PathType, where)
	case p.EndSwim && special:
		c.Verdict = VerdictFail
		c.Reason = fmt.Sprintf("the point is swimmable (end_swim, type 0x%x) and it is %s", p.PathType, where)
	case p.Underwater():
		c.Verdict = VerdictWarn
		c.Reason = fmt.Sprintf("the server path is the swim line (type 0x%x) on a plain move", p.PathType)
	default:
		if off := offlineCheck(st, planlint.LintReach); off != nil && off.Verdict != VerdictOK {
			c.Verdict = VerdictWarn
			c.Reason = fmt.Sprintf("offline %s said %q, the server path is ok (type 0x%x, %.1f yd): mmaps drift", off.Name, off.Reason, p.PathType, p.Length)
			break
		}
		c.Reason = fmt.Sprintf("the server path is ok (type 0x%x, %.1f yd, ends %.1f yd from the point)", p.PathType, p.Length, p.EndOffset)
	}
	return c
}

// offlineCheck finds one of the step's offline checks by name.
func offlineCheck(st *StepResult, name string) *Check {
	for i := range st.Checks {
		if st.Checks[i].Name == name && st.Checks[i].Source == SourceOffline {
			return &st.Checks[i]
		}
	}
	return nil
}

// liveWalkCheck judges where a real walk stopped (spec 3.4), and for an anchor hop CheckAnchor's rule.
func liveWalkCheck(st *StepResult, wk WalkResult, anchorHop bool, anchor dungeon.Point) Check {
	c := Check{Name: CheckLiveWalk, Source: SourceLive, Verdict: VerdictOK}
	if wk.Skipped != "" {
		c.Verdict = VerdictSkip
		c.Reason = wk.Skipped
		return c
	}
	point := dungeon.Point{X: st.X, Y: st.Y, Z: st.Z}
	d := dist3(point, wk.Stop)
	c.Data = map[string]float64{"stop_yd": d}
	switch {
	case d > StalledArriveYd:
		c.Verdict = VerdictFail
		c.Reason = fmt.Sprintf("the walk never arrives: stops %.1f yd short (the stall rule is %d yd)", d, StalledArriveYd)
	case d > ArriveYd:
		c.Verdict = VerdictWarn
		c.Reason = fmt.Sprintf("the walk stops %.1f yd short (it arrives only by the stall rule, %d yd)", d, ArriveYd)
	default:
		c.Reason = fmt.Sprintf("the walk stops %.1f yd from the point (kArriveYd %d)", d, ArriveYd)
	}
	if !anchorHop {
		return c
	}
	dz := math.Abs(float64(wk.Stop.Z - anchor.Z))
	c.Data["anchor_dz"] = dz
	backBad := ""
	if wk.Back != nil {
		switch {
		case wk.Back.NOPATH():
			backBad = "the path back to the anchor is NOPATH"
		case wk.Back.Verdict != "ok":
			backBad = fmt.Sprintf("the path back to the anchor answers %s", wk.Back.Verdict)
		case wk.Back.EndOffset > MaxMoveEndOffset:
			backBad = fmt.Sprintf("the path back to the anchor ends %.1f yd off it", wk.Back.EndOffset)
		}
	}
	switch {
	case dz > StragglerDzYd && backBad != "":
		c.Verdict = VerdictFail
		c.Reason = fmt.Sprintf("CheckAnchor Off: the stop is %.1f yd %s the anchor and %s: the gathering-back loop", dz,
			above(wk.Stop.Z, anchor.Z), backBad)
		return c
	case dz > StragglerDzYd:
		c.Verdict = VerdictFail
		c.Reason = fmt.Sprintf("CheckAnchor Off: the stop is %.1f yd %s the anchor row (kStragglerDzYd %d)", dz,
			above(wk.Stop.Z, anchor.Z), StragglerDzYd)
		return c
	case backBad != "":
		c.Verdict = VerdictFail
		c.Reason = "CheckAnchor Off: " + backBad
		return c
	}
	return c
}

// above says where z stands against at.
func above(z, at float32) string {
	if z > at {
		return "above"
	}
	if z < at {
		return "below"
	}
	return "level with"
}

// liveAggroCheck judges the live hostiles within reach + spread of a stand or anchor (spec 3.4).
func liveAggroCheck(st *StepResult, units []objects.Unit, w *worldspawn.World, owned map[uint64]int, pullIdx int,
	level int, rate float32, spread float64, live LiveData) Check {
	c := Check{Name: CheckLiveAggro, Source: SourceLive, Verdict: VerdictOK}
	spawns := map[uint64]worldspawn.Spawn{}
	if w != nil {
		for _, sp := range w.Spawns {
			spawns[sp.GUID] = sp
		}
	}
	point := dungeon.Point{X: st.X, Y: st.Y, Z: st.Z}
	var inSight, outSight []string
	nearest, haveNear := math.Inf(1), false
	worstReach := 0.0
	for _, u := range units {
		if u.Dead {
			continue
		}
		low := uint32(u.GUID) & 0xffffff
		var reach float64
		hostile := u.Hostile && u.Level > 1
		if sp, ok := spawns[uint64(low)]; ok {
			if !liveKeep(sp) {
				continue
			}
			hostile = true
			reach = float64(worldspawn.AggroRadius(sp, level, rate))
		}
		if !hostile || reach <= 0 {
			continue
		}
		if o, ok := owned[uint64(low)]; ok && o <= pullIdx {
			continue // this pull, or a pack an earlier row already owns
		}
		d := dist3(point, dungeon.Point{X: u.X, Y: u.Y, Z: u.Z})
		if d > reach+spread {
			continue
		}
		if reach > worstReach {
			worstReach = reach
		}
		if d < nearest {
			nearest, haveNear = d, true
		}
		los, ok := live.LOS[LOSKey{Step: st.Step, Guid: low}]
		if !ok {
			los = true // the high side: no answer means sight is assumed
		}
		name := u.Name
		if name == "" {
			name = fmt.Sprintf("guid %d", low)
		}
		if los {
			inSight = append(inSight, fmt.Sprintf("%s at %.1f yd", name, d))
		} else {
			outSight = append(outSight, fmt.Sprintf("%s at %.1f yd", name, d))
		}
	}
	c.Data = map[string]float64{"units": float64(len(inSight) + len(outSight)), "reach": worstReach}
	if haveNear {
		c.Data["nearest_yd"] = nearest
	}
	switch {
	case len(inSight) > 0:
		c.Verdict = VerdictFail
		c.Reason = fmt.Sprintf("%d hostile unit(s) within reach + %.0f yd, in sight: %s", len(inSight), spread, joinNames(inSight))
	case len(outSight) > 0:
		c.Verdict = VerdictWarn
		c.Reason = fmt.Sprintf("%d hostile unit(s) within reach + %.0f yd, out of sight (q los false): %s", len(outSight), spread, joinNames(outSight))
	default:
		c.Reason = fmt.Sprintf("no hostile unit not owned by this pull within reach + %.0f yd (%.0f yd read)", spread, worstReach)
	}
	return c
}

// liveLOSCheck asks whether the stand sees any unit of the target pack (spec 3.4).
func liveLOSCheck(st *StepResult, targets []uint32, live LiveData) Check {
	c := Check{Name: CheckLiveLOS, Source: SourceLive, Verdict: VerdictOK}
	if len(targets) == 0 {
		c.Verdict = VerdictSkip
		c.Reason = "no target pack in the world export for this pull"
		return c
	}
	answered, in := 0, 0
	for _, g := range targets {
		los, ok := live.LOS[LOSKey{Step: st.Step, Guid: g}]
		if !ok {
			continue
		}
		answered++
		if los {
			in++
		}
	}
	c.Data = map[string]float64{"targets": float64(len(targets)), "answered": float64(answered), "in_sight": float64(in)}
	switch {
	case answered == 0:
		c.Verdict = VerdictSkip
		c.Reason = "no q los answer for the target pack" + abortedSuffix(live)
	case in > 0:
		c.Reason = fmt.Sprintf("%d of %d target pack unit(s) in sight from the stand", in, len(targets))
	default:
		c.Verdict = VerdictWarn
		c.Reason = fmt.Sprintf("the pull shot waits for sight: none of the %d target pack units is in sight from the stand", len(targets))
	}
	return c
}

// liveJoinersCheck recomputes the pull's joiner estimate with live positions and live sight (spec 3.4).
func liveJoinersCheck(st *StepResult, row planexport.StepRow, idx int, rows []planlint.PullRow, owned map[uint64]int,
	mobs []aggroest.Mob, entries map[uint32]uint32, units []objects.Unit, ceiling int, spread float64, level int,
	rate float32, killFirst []uint32, live LiveData) Check {
	c := Check{Name: CheckLiveJoiners, Source: SourceLive, Verdict: VerdictOK}
	at := map[uint32]dungeon.Point{}
	for _, u := range units {
		if u.Dead {
			continue
		}
		at[uint32(u.GUID)&0xffffff] = dungeon.Point{X: u.X, Y: u.Y, Z: u.Z}
	}
	lmobs := make([]aggroest.Mob, len(mobs))
	copy(lmobs, mobs)
	moved := 0
	for i := range lmobs {
		if p, ok := at[lmobs[i].Guid]; ok {
			lmobs[i].Pos = p
			moved++
		}
	}
	target := planlint.PullTarget(killFirst, lmobs, entries, idx, rows[idx], owned)
	if target < 0 {
		return Check{}
	}
	sees := func(a, b dungeon.Point) bool {
		for i := range lmobs {
			m := &lmobs[i]
			hit := nearPoint(m.Pos, a) || nearPoint(m.Pos, b)
			if !hit {
				continue
			}
			if los, ok := live.LOS[LOSKey{Step: st.Step, Guid: m.Guid}]; ok {
				return los
			}
		}
		return true // no live answer: the high side
	}
	g := aggroest.Geometry{Walk: rows[idx].Walk, WalkSpreadYd: float32(aggroest.WalkSpreadYd),
		Stand: rows[idx].Party, StandSpreadYd: float32(spread), HasFight: true, Fight: rows[idx].Point, FightSpreadYd: float32(spread)}
	res := aggroest.Estimate(lmobs, target, g, aggroest.Params{PartyLevel: level, Rate: rate,
		RingYd: aggroest.RingYd, RingHops: aggroest.RingHops, FloorDz: aggroest.FloorDz, Sees: sees})
	pack := lmobs[target].GroupID
	var guids []uint32
	byPack := map[uint64]*planlint.JoinerPack{}
	var order []uint64
	for _, m := range res.Members {
		if m.Guid == lmobs[target].Guid || lmobs[m.Index].GroupID == pack {
			continue
		}
		guids = append(guids, m.Guid)
		id := uint64(lmobs[m.Index].GroupID)
		p, ok := byPack[id]
		if !ok {
			p = &planlint.JoinerPack{ID: id}
			byPack[id] = p
			order = append(order, id)
		}
		p.Guids = append(p.Guids, m.Guid)
	}
	packs := make([]planlint.JoinerPack, 0, len(order))
	for _, id := range order {
		p := byPack[id]
		p.Bodies = len(p.Guids)
		sort.Slice(p.Guids, func(i, j int) bool { return p.Guids[i] < p.Guids[j] })
		packs = append(packs, *p)
	}
	sort.Slice(guids, func(i, j int) bool { return guids[i] < guids[j] })
	c.Data = map[string]float64{"bodies": float64(res.Bodies), "joiners": float64(len(guids)), "moved_units": float64(moved)}
	c.Bodies, c.Guids, c.Packs = res.Bodies, guids, packs
	overCeiling := ceiling > 0 && res.Bodies > ceiling
	switch {
	case len(guids) >= JoinerFailBodies || overCeiling:
		c.Verdict = VerdictFail
	case len(guids) >= JoinerWarnBodies:
		c.Verdict = VerdictWarn
	}
	if len(guids) == 0 && !overCeiling {
		c.Reason = fmt.Sprintf("%d bodies, none outside the target pack (live positions and sight)", res.Bodies)
		return c
	}
	c.Reason = fmt.Sprintf("%d bodies outside the target pack (live positions, live sight): %s", len(guids), guidText(guids))
	if overCeiling {
		c.Reason += fmt.Sprintf("; %d bodies over the ceiling %d", res.Bodies, ceiling)
	}
	return c
}

// planKillFirst is the plan's kill_first order (the live target rule).
func planKillFirst(in Input) []uint32 {
	if in.Plan != nil {
		return in.Plan.KillFirst
	}
	return nil
}

// liveKillFirst prefers the caller's Options and falls back to the plan.
func liveKillFirst(in Input, opt Options) []uint32 {
	if len(opt.KillFirst) > 0 {
		return opt.KillFirst
	}
	return planKillFirst(in)
}

// planCeiling is the plan's pull ceiling (0: the lint's default).
func planCeiling(in Input) int {
	if in.Plan != nil {
		return in.Plan.PullCeiling
	}
	return 0
}

// nearPoint says two points are the same spot (a live unit's position against an estimator coordinate).
func nearPoint(a, b dungeon.Point) bool {
	return math.Abs(float64(a.X-b.X)) < 0.05 && math.Abs(float64(a.Y-b.Y)) < 0.05 && math.Abs(float64(a.Z-b.Z)) < 0.05
}

func joinNames(ss []string) string {
	out := ""
	for i, s := range ss {
		if i > 0 {
			out += ", "
		}
		out += s
	}
	return out
}

func guidText(gs []uint32) string {
	out := ""
	for i, g := range gs {
		if i > 0 {
			out += ", "
		}
		out += fmt.Sprint(g)
	}
	if out == "" {
		return "none"
	}
	return out
}

func dist2D(a, b dungeon.Point) float64 {
	dx, dy := float64(a.X-b.X), float64(a.Y-b.Y)
	return math.Sqrt(dx*dx + dy*dy)
}
