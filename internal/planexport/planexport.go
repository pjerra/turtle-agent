// Package planexport turns a dungeon plan (dungeons/<name>.json, plan format v1) into the rows of TortoiseBots' run leader
// (ai_dungeon_plan, ai_dungeon_leg, ai_dungeon_step) and writes them as one idempotent world migration.
//
// The mapping is TortoiseBots' docs/guides/dungeon-run-leader.md ("From plan v1 to rows"); the limits are the ones its loader
// enforces (ai/playerbot/DungeonPlanPolicy.h, NormalizePlanStep). The plan file stays the single source: edit it here and export
// again, never the rows by hand.
package planexport

import (
	"errors"
	"fmt"
	"math"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"github.com/pjerra/turtle-agent/internal/dungeon"
)

// Step kinds of ai_dungeon_step.kind.
const (
	KindMove       = "move"
	KindMoveDirect = "move_direct"
	// KindMoveHopExact is the direct step right after an exact via (FlagExact): a move_direct in every way for a module that knows
	// exact waypoints, and a kind an older module does not know, so its loader skips the row (UnknownKind) and no direct order goes
	// out over the hole from a foot it walks as a plain move (fail closed against a binary rollback).
	KindMoveHopExact = "move_hop_exact"
	KindPull         = "pull"
	KindBoss         = "boss"
	KindLoot         = "loot"
	KindOpen         = "open"
	KindUseItem      = "use_item"
	KindWaitObject   = "wait_object"
	KindWaitClear    = "wait_clear"
	KindRest         = "rest"
	KindRitual       = "ritual"
	KindHold         = "hold"
	// KindGossip is a gossip step: the lead bot talks to creature `entry` and picks option `param` (`.bot cmd gossip <entry> <bot> <param>`).
	KindGossip = "gossip"
)

// Flags of ai_dungeon_step.flags.
const (
	FlagAmbush   = 1
	FlagAlliance = 2
	FlagHorde    = 4
	// FlagDoneData, FlagEncounter: a hold is done when instance data `entry` reads `param`, read as the core's encounter states
	// (DungeonPlanPolicy.h kPlanFlagDoneData, kPlanFlagEncounter).
	FlagDoneData  = 8
	FlagEncounter = 32
	// FlagGoneIsDone marks a loot or open row as done when its object is gone (the module's kPlanFlagGoneIsDone): a `not-found` answer
	// for a loot of no item with this flag ends the step ("nothing of entry N left near the point: done") instead of pausing the run.
	// A chest of spawntimesecs 7200 (Zul'Farrak's Shallow Grave, entry 128403) despawns once looted; the flag matters only for a run
	// resumed after a wipe at one of these rows (a fresh run does not need it). A loader that knows no such flag drops it (unknown flags
	// are ignored on a loot/open), so the rows still load.
	FlagGoneIsDone = 256
	// FlagExact marks a move row as an exact waypoint (the plan's Waypoint.Exact): the run leader counts it done only once the lead bot
	// stands within 1.5 yd of it and has stood still there for 2 s, and only then sends the move_direct after it (TortoiseBots
	// DungeonPlanPolicy.h kPlanFlagExact, DungeonRunPolicy.h ExactWalkTo). The loader refuses it on any other kind.
	FlagExact = 64
	// EncounterDone is the encounter state DONE: a hold's param.
	EncounterDone = 3
)

// The limits of the module's loader (NormalizePlanStep) and of its column widths.
const (
	MaxRadius      = 100   // yards
	MaxTimeoutS    = 600   // seconds
	MaxEntries     = 16    // creature entries of a pull or boss
	MaxKind        = 16    // ai_dungeon_step.kind is a varchar(16): a longer kind fails the INSERT (or is cut)
	MaxPlanName    = 32    // ai_dungeon_plan.plan
	MaxLegName     = 32    // ai_dungeon_leg.name
	MaxPlanList    = 255   // ai_dungeon_plan.cc_priority, cc_always, kill_first (varchar(255))
	MaxSource      = 64    // ai_dungeon_plan.source
	MaxComment     = 255   // every comment column
	MaxLegs        = 255   // ai_dungeon_leg.leg is a tinyint unsigned
	MaxSteps       = 65535 // ai_dungeon_step.seq is a smallint unsigned
	DefaultPullYd  = 15    // the module's pull radius for 0
	PullSlackYd    = 10    // added to the farthest position of a group when the plan gives no radius
	MoveGapWarnYd  = 60    // the module logs consecutive moves farther apart than this
	MoveGapCutYd   = 50    // the longest hop the exporter leaves when it cuts a leg into via points
	DefaultBossRad = 30
	// MaxGossipOption is the highest `param` of a gossip row: the number `.bot cmd gossip` takes counts the menu from 1 (a 1.12 menu holds 15
	// options), while the plan's `option` is the menu's own index counted from 0 (the CMSG_GOSSIP_SELECT_OPTION value): the row's param is option + 1.
	MaxGossipOption = 15
)

// PlanRow is one ai_dungeon_plan row.
type PlanRow struct {
	Plan string
	Map  uint32
	// CCPriority, CCAlways and KillFirst are the plan's unit lists (cc_priority, cc_always, kill_first), written as comma lists like
	// ai_dungeon_step.entries in the ai_dungeon_plan columns of the same names (TortoiseBots #1001, varchar(255), '' for none).
	CCPriority, CCAlways, KillFirst []uint32
	Source                          string
	Comment                         string
}

// LegRow is one ai_dungeon_leg row.
type LegRow struct {
	Plan    string
	Leg     int
	Name    string
	Comment string
}

// StepRow is one ai_dungeon_step row.
type StepRow struct {
	Plan     string
	Leg      int
	Seq      int
	Kind     string
	Entry    uint32
	X, Y, Z  float32
	Radius   float32
	Item     uint32
	Param    uint32
	Flags    uint32
	TimeoutS int
	Entries  []uint32
	Comment  string
}

// Rows are the three tables' rows of one plan.
type Rows struct {
	Plan  PlanRow
	Legs  []LegRow
	Steps []StepRow
}

// Count is the number of rows of the plan in all three tables.
func (r Rows) Count() int { return 1 + len(r.Legs) + len(r.Steps) }

// Options are the exporter's choices.
type Options struct {
	// Source is the text of ai_dungeon_plan.source (where the rows came from); empty: "turtle-agent dungeons/<name>.json".
	Source string
	// Cut, when set, cuts a hop longer than MoveGapWarnYd between two moves into via points: it returns the points to walk through
	// (not including the two ends), e.g. a navmesh path. Nil: the long hops stay and are reported in Warnings.
	Cut func(mapID uint32, from, to dungeon.Point) ([]dungeon.Point, error)
	// DropCompanionsLoot lets a plan with a leg whose companions_loot is false be exported all the same, without it: the rows have no
	// column for it (TortoiseBots' run leader has no such setting), so the bots would loot what the leg wants left alone (Zul'Farrak's
	// graveyard chests). Without this option such a plan is refused.
	DropCompanionsLoot bool
	// CarrierChests, with DropCompanionsLoot, exports a companions_loot-false leg's loot_object steps as chest rows the carrier alone
	// takes: a move to its stand, a loot row (entry, near point, the expected item, FlagGoneIsDone), and, when the leg has
	// LeftoverEntries, a pull row of those entries at the chest's near point (radius carrierSweepRadiusYd, "sweep"): the trap a grave
	// (Zul'Farrak's Shallow Graves) is linked to raises its adds on the opener in the same server call, so the loot row can never be
	// safe without the sweep behind it, and one chest a row (no batch) keeps them two at a time. The companions_loot setting itself is
	// still not in the rows: the bots' looting has to be turned off for the leg in the setup, or a module's auto-loot rule has to skip a
	// trapped chest. Without the option the loot_object steps are left out as before. Needs DropCompanionsLoot (the chest would not be
	// in the rows without it anyway).
	CarrierChests bool
	// ApproachHops, with Cut, also cuts the hop to a pull, boss or hold row that is more than the run leader's reach (50 yd) from the
	// anchor before it (the move, or the fight, the party stood at) into move rows along the planned path, the last of them at least
	// 25 yd short of the target. Without it such a hop is left to the run leader's own navmesh hops (at most 8 of 40 yd, stopping 30 yd
	// short), which a plan of long corridors (Wailing Caverns: up to 363 yd) outruns.
	ApproachHops bool
	// GMCrossDirect exports a gm_cross waypoint as a move_direct (a straight walk to the landing point) instead of refusing the plan: the
	// run leader has no GM move. Only right where the gap is a drop the straight line crosses (Wailing Caverns' ramp to Lord Serpentis,
	// 6 yd down over a 3.9 yd gap); the row's comment and a warning say it is not tried live.
	GMCrossDirect bool
	// SkipLegs names legs (by their name in the plan) that are left out of the rows, each with a warning, and the later legs close up, as for
	// an optional leg. For a leg the run leader cannot do while the legs after it can: Zul'Farrak's Sergeant Bly, who turns hostile after his
	// gossip (the run leader has no boss that turns hostile later), stands between the Weegli gossip that opens the End Door and the legs behind
	// that door. An unknown name is an error (a misspelling would otherwise export the leg or cut the plan there).
	SkipLegs []string
	// Route, when set, is called for every hop between two anchors of the rows (move, pull, boss, hold) of a plan with avoid boxes: it
	// returns the via points that keep the run leader's own navmesh path (the server's, which does not know the boxes) out of them,
	// none when that path is already clear. An error means a hop that cannot be made safe: the export fails.
	Route func(mapID uint32, from, to dungeon.Point) ([]dungeon.Point, error)
}

// ErrCompanionsLoot is the refusal of a plan with a leg whose companions_loot is false (see Options.DropCompanionsLoot).
var ErrCompanionsLoot = errors.New("plan not exported")

// ErrNoRunLeaderStep marks a step the run leader has no step for (a gossip, a unit wait, an area trigger by the agent). A plan whose first
// leg has one is refused; a plan that has one in a later leg is exported up to that leg (Result.Cut).
var ErrNoRunLeaderStep = errors.New("a step the run leader cannot do")

type noRunLeaderStep struct{ msg string }

func (e noRunLeaderStep) Error() string        { return e.msg }
func (e noRunLeaderStep) Is(target error) bool { return target == ErrNoRunLeaderStep }

func noStep(format string, a ...any) error { return noRunLeaderStep{fmt.Sprintf(format, a...)} }

// Result is a plan's rows and what the exporter had to say about them.
type Result struct {
	Rows     Rows
	Warnings []string
	// Cut is the note of a plan exported only up to the first leg with a step the run leader cannot do ("" for a whole plan): it names the
	// leg and the step, is in Warnings and ends the plan row's comment.
	Cut string
}

var planNameRe = regexp.MustCompile(`^[a-z0-9_-]{1,32}$`)

// Refusal says why a plan is not exported, or "": a draft plan is a skeleton that has had no mapping walk and must not be put on the
// server as a route to run, unless the caller says allowDraft.
func Refusal(p *dungeon.Plan, allowDraft bool) string {
	if strings.HasPrefix(p.Status, dungeon.StatusDraft) && !allowDraft {
		return fmt.Sprintf("plan %s is a draft (%s): its route has not had its mapping walk; walk it first, or pass --allow-draft", p.Name, p.Status)
	}
	return ""
}

// IsLive reports whether the plan's status says it was checked live.
func IsLive(p *dungeon.Plan) bool { return strings.HasPrefix(p.Status, dungeon.StatusLiveChecked) }

// Build maps a plan to rows. It refuses what the module's loader would skip: a name that is not a plan name, a radius over 100, a
// timeout over 600, more than 16 entries, a step without the entry its kind needs.
func Build(p *dungeon.Plan, opt Options) (Result, error) {
	name := strings.ToLower(strings.TrimSpace(p.Name))
	if !planNameRe.MatchString(name) {
		return Result{}, fmt.Errorf("plan name %q: use a-z, 0-9, _ and - (at most %d characters)", p.Name, MaxPlanName)
	}
	if len(p.Legs) == 0 || len(p.Legs) > MaxLegs {
		return Result{}, fmt.Errorf("plan %s: %d legs (1-%d)", name, len(p.Legs), MaxLegs)
	}
	source := opt.Source
	if source == "" {
		source = "turtle-agent dungeons/" + name + ".json"
	}
	for _, l := range []struct {
		col string
		es  []uint32
	}{{"cc_priority", p.CCPriority}, {"cc_always", p.CCAlways}, {"kill_first", p.KillFirst}} {
		if n := len(entriesText(l.es)); n > MaxPlanList {
			return Result{}, fmt.Errorf("plan %s: %s is %d characters as a comma list (ai_dungeon_plan.%s holds %d)", name, l.col, n, l.col, MaxPlanList)
		}
	}
	for _, n := range opt.SkipLegs {
		if !slices.ContainsFunc(p.Legs, func(l dungeon.Leg) bool { return l.Name == n }) {
			return Result{}, fmt.Errorf("plan %s: skip-legs names %q, which is no leg of the plan", name, n)
		}
	}
	b := &builder{opt: opt, plan: p, name: name}
	b.rows.Plan = PlanRow{Plan: name, Map: p.Map, CCPriority: slices.Clone(p.CCPriority), CCAlways: slices.Clone(p.CCAlways), KillFirst: slices.Clone(p.KillFirst), Source: clip(source, MaxSource), Comment: clip(planComment(p), MaxComment)}
	cut, cutLeg := "", 0
	for li := range p.Legs {
		nl, ns, nw := len(b.rows.Legs), len(b.rows.Steps), len(b.warnings)
		if err := b.leg(li); err != nil {
			if li > 0 && errors.Is(err, ErrNoRunLeaderStep) {
				// The legs before it are a plan the run leader can run: export them, and say where and why the rest was left off.
				b.rows.Legs, b.rows.Steps, b.warnings = b.rows.Legs[:nl], b.rows.Steps[:ns], b.warnings[:nw]
				// The rows number the legs that were written: an optional leg left out earlier takes no number, so the note counts rows, not plan legs.
				n := len(b.rows.Legs)
				cutLeg = li
				cut = fmt.Sprintf("plan %s: exported legs 0-%d (%s) only; cut before leg %d (%s), which has a step the run leader cannot do: %v; that leg and the %d after it are not in the rows", name, n-1, b.rows.Legs[n-1].Name, n, p.Legs[li].Name, err, len(p.Legs)-li-1)
				break
			}
			return Result{}, fmt.Errorf("plan %s: leg %d (%s): %w", name, li, p.Legs[li].Name, err)
		}
	}
	if cut != "" {
		b.warnings = append(b.warnings, cut)
		b.rows.Plan.Comment = clip(planComment(p)+"; cut before leg "+strconv.Itoa(len(b.rows.Legs))+" ("+p.Legs[cutLeg].Name+"): a step the run leader cannot do", MaxComment)
	}
	if err := b.cutLongHops(); err != nil {
		return Result{}, err
	}
	if err := b.routeAroundAvoid(); err != nil {
		return Result{}, err
	}
	for _, w := range p.RunLeaderReach(0) {
		if w.Leg < cutLeg || cut == "" {
			b.warnings = append(b.warnings, w.String())
		}
	}
	upto := len(p.Legs)
	if cut != "" {
		upto = cutLeg
	}
	b.warnings = append(b.warnings, unexpressed(p, opt, upto)...)
	if err := Validate(b.rows); err != nil {
		return Result{}, err
	}
	return Result{Rows: b.rows, Warnings: b.warnings, Cut: cut}, nil
}

func planComment(p *dungeon.Plan) string {
	c := "plan v1"
	if p.Status != "" {
		c += ", " + p.Status
	}
	return c
}

type builder struct {
	opt         Options
	plan        *dungeon.Plan
	name        string
	rows        Rows
	warnings    []string
	leg_        int // the plan's leg being built
	outLeg      int // the row number of the next leg written (an optional leg left out takes none)
	seq         int
	skippedLoot int           // loot_object steps of a companions_loot false leg left out of the leg being built
	last        dungeon.Point // the last point a step of the leg put the party at
	haveLast    bool
}

func (b *builder) add(s StepRow) {
	s.Plan, s.Leg, s.Seq = b.name, b.outLeg, b.seq
	b.seq++
	b.rows.Steps = append(b.rows.Steps, s)
}

func (b *builder) move(p dungeon.Point, direct bool, comment string) {
	kind := KindMove
	if direct {
		kind = KindMoveDirect
	}
	b.add(StepRow{Kind: kind, X: p.X, Y: p.Y, Z: p.Z, Comment: comment})
	b.last, b.haveLast = p, true
}

func (b *builder) leg(li int) error {
	l := b.plan.Legs[li]
	if slices.Contains(b.opt.SkipLegs, l.Name) {
		b.warnings = append(b.warnings, fmt.Sprintf("leg %d (%s): left out of the export by the exporter's skip-legs option (a leg the run leader cannot do, or does not need): the later legs close up", li, l.Name))
		return nil
	}
	if l.Optional != nil {
		// The run leader's bots carry no such item and its rows have no way to give one (give_items is the agent's GM setup), so the leg would
		// end the run at its first use of the object: the leg is left out and the later legs close up.
		b.warnings = append(b.warnings, fmt.Sprintf("leg %d (%s): optional (needs %s, which the run leader's bots do not carry): left out of the export", li, l.Name, l.Optional.Ref()))
		return nil
	}
	b.leg_, b.seq, b.haveLast, b.skippedLoot = li, 0, false, 0
	if l.CompanionsLoot != nil && !*l.CompanionsLoot && !b.opt.DropCompanionsLoot {
		return fmt.Errorf("companions_loot is false and the run leader's rows cannot carry it (TortoiseBots has no such column): its bots would loot what this leg wants left alone; export is refused (pass DropCompanionsLoot / --drop-companions-loot to export without it): %w", ErrCompanionsLoot)
	}
	b.rows.Legs = append(b.rows.Legs, LegRow{Plan: b.name, Leg: b.outLeg, Name: clip(l.Name, MaxLegName)})
	defer func() { b.outLeg++ }()
	// The leg in the order the driver does it: the via waypoints, the To, the pull groups, the steps, the boss, the steps after it.
	for vi, w := range l.Via {
		if w.GMCross && b.opt.GMCrossDirect {
			b.warnings = append(b.warnings, fmt.Sprintf("leg %d (%s): via %.1f %.1f %.1f is a gm_cross; exported as a move_direct (a straight walk to the landing point), not tried live: the run leader has no GM move", li, l.Name, w.X, w.Y, w.Z))
			b.move(w.Point, true, "via (gm_cross in the plan; move_direct, not tried live)")
			continue
		}
		if w.GMCross {
			return fmt.Errorf("via %.1f %.1f %.1f is a gm_cross: a GM moves the party there in a remote run, and the TortoiseBots run leader has no such step", w.X, w.Y, w.Z)
		}
		if w.Exact {
			// The plan allows exact only on a plain via whose next via is a direct step (dungeon.Parse refuses any other), so this row is
			// always followed by its move_direct; Validate checks the rows keep it so.
			b.add(StepRow{Kind: KindMove, X: w.X, Y: w.Y, Z: w.Z, Flags: FlagExact, Comment: "via (exact: the direct step's launch point)"})
			b.last, b.haveLast = w.Point, true
			continue
		}
		if w.Direct && vi > 0 && l.Via[vi-1].Exact {
			b.add(StepRow{Kind: KindMoveHopExact, X: w.X, Y: w.Y, Z: w.Z, Comment: "via (direct from the exact point before it)"})
			b.last, b.haveLast = w.Point, true
			continue
		}
		b.move(w.Point, w.Direct, "via")
	}
	b.move(l.To, false, "to")
	for gi, g := range l.PullGroups {
		if err := b.pull(gi, g); err != nil {
			return fmt.Errorf("pull_groups[%d]: %w", gi, err)
		}
	}
	for si, st := range l.Steps {
		// A leg whose companions_loot is false: with DropCompanionsLoot alone the chest steps would send bots at chests the leg wants left
		// alone unless the setup mutes them, so they are left out, with a warning. With CarrierChests each chest becomes rows the carrier
		// alone runs: the stand move, the loot (flag 256, gone is done), and the leg's leftover entries as the sweep pull at its point
		// (the chest's linked trap raises its adds before the order's ACK, so the sweep is part of the chest, and one chest a row keeps
		// the adds two at a time).
		if st.LootObject != nil && l.CompanionsLoot != nil && !*l.CompanionsLoot && !b.opt.CarrierChests {
			b.skippedLoot++
			continue
		}
		if st.LootObject != nil && l.CompanionsLoot != nil && !*l.CompanionsLoot && b.opt.CarrierChests {
			if st.LootObject.Stand == nil {
				// The chest rows walk the party to a stand before the loot (the run leader takes rows from where the party stands); a chest
				// without one would be a two-row standless loot, which no row could make safe: the loot would be taken from wherever the
				// last row left the party, possibly out of the search range or in the next pack.
				return fmt.Errorf("steps[%d]: loot_object %d has no stand: give the chest a stand point (the carrier's rows walk the party there before the loot)", si, st.LootObject.Entry)
			}
			b.carrierChest(l, st.LootObject)
			continue
		}
		if err := b.step(st); err != nil {
			return fmt.Errorf("steps[%d]: %w", si, err)
		}
	}
	if l.Boss != nil {
		if err := b.boss(l.Boss); err != nil {
			return fmt.Errorf("boss: %w", err)
		}
	}
	for si, st := range l.AfterBoss {
		if err := b.step(st); err != nil {
			return fmt.Errorf("after_boss[%d]: %w", si, err)
		}
	}
	if b.skippedLoot > 0 {
		b.warnings = append(b.warnings, fmt.Sprintf("leg %d (%s): %d loot_object steps left out: the leg's companions_loot false (looting off for every bot, only the carrier opens these) cannot be exported", li, l.Name, b.skippedLoot))
	}
	if b.seq > MaxSteps {
		return fmt.Errorf("%d steps (at most %d)", b.seq, MaxSteps)
	}
	return nil
}

// teamFlags is the flags bit of a team gate.
func teamFlags(team string) uint32 {
	switch team {
	case dungeon.TeamAlliance:
		return FlagAlliance
	case dungeon.TeamHorde:
		return FlagHorde
	}
	return 0
}

// uniq lists the entries once each, in first-seen order: a pull is every living unit of its entries, so a repeat adds nothing.
func uniq(es []uint32) []uint32 {
	var out []uint32
	for _, e := range es {
		dup := false
		for _, o := range out {
			dup = dup || o == e
		}
		if !dup {
			out = append(out, e)
		}
	}
	return out
}

func (b *builder) pull(gi int, g dungeon.PullGroup) error {
	if len(g.Positions) == 0 {
		return fmt.Errorf("no position")
	}
	var cx, cy, cz float64
	for _, p := range g.Positions {
		cx, cy, cz = cx+float64(p.X), cy+float64(p.Y), cz+float64(p.Z)
	}
	n := float64(len(g.Positions))
	cx, cy, cz = cx/n, cy/n, cz/n
	radius := g.Radius
	if radius <= 0 {
		far := 0.0
		for _, p := range g.Positions {
			far = math.Max(far, math.Sqrt(sq(float64(p.X)-cx)+sq(float64(p.Y)-cy)+sq(float64(p.Z)-cz)))
		}
		radius = ceil1(far + PullSlackYd)
	}
	flags := teamFlags(g.Team)
	if g.Ambush {
		flags |= FlagAmbush
	}
	c := fmt.Sprintf("pull group %d", gi)
	for _, a := range g.Approach {
		b.move(a, false, c+" approach")
	}
	if g.Hold != nil {
		b.hold(g.Hold, uniq(g.Entries), c+" hold")
		return nil
	}
	b.add(StepRow{Kind: KindPull, X: f32(cx), Y: f32(cy), Z: f32(cz), Radius: radius, Flags: flags, Entries: uniq(g.Entries), Comment: c})
	b.last, b.haveLast = dungeon.Point{X: f32(cx), Y: f32(cy), Z: f32(cz)}, true
	return nil
}

// hold writes a scripted event's hold row (dungeon.Hold): done when instance data h.Data reads DONE, read as encounter states. A move row
// straight before it to the same spot is left out: the hold walks there itself, and a ritual followed straight by its hold keeps the
// event's first fight (it starts with the ritual's cast) in the hold.
func (b *builder) hold(h *dungeon.Hold, entries []uint32, comment string) {
	if n := len(b.rows.Steps); n > 0 {
		prev := b.rows.Steps[n-1]
		if prev.Leg == b.outLeg && prev.Kind == KindMove && dist3(dungeon.Point{X: prev.X, Y: prev.Y, Z: prev.Z}, h.Spot) <= 1 {
			b.rows.Steps = b.rows.Steps[:n-1]
			b.seq--
		}
	}
	param, flags := uint32(EncounterDone), uint32(FlagDoneData|FlagEncounter)
	done := fmt.Sprintf("%d (DONE)", EncounterDone)
	if h.Value != nil {
		// A script value (Zul'Farrak's pyramid phase), not an encounter state: flag 8 alone, so the run leader reads no FAIL or reset
		// into it and its loader takes a param above 4 (DungeonPlanPolicy.h NormalizePlanStep).
		param, flags = *h.Value, FlagDoneData
		done = fmt.Sprintf("%d (a script value, not an encounter state)", *h.Value)
	}
	b.add(StepRow{Kind: KindHold, Entry: h.Data, X: h.Spot.X, Y: h.Spot.Y, Z: h.Spot.Z, Radius: h.Radius, Param: param,
		Flags: flags, TimeoutS: h.TimeoutS, Entries: entries,
		Comment: clip(fmt.Sprintf("%s: the event's mobs are fought on the spot until instance data %d reads %s", comment, h.Data, done), MaxComment)})
	b.last, b.haveLast = h.Spot, true
}

func (b *builder) boss(bs *dungeon.Boss) error {
	for _, a := range bs.Approach {
		b.move(a, false, "boss approach")
	}
	if bs.Hold != nil {
		b.hold(bs.Hold, uniq(append(append([]uint32{bs.Entry}, bs.First...), bs.Hold.Entries...)), "boss hold")
		return nil
	}
	b.add(StepRow{Kind: KindBoss, Entry: bs.Entry, X: bs.Position.X, Y: bs.Position.Y, Z: bs.Position.Z, Flags: teamFlags(bs.Team), Entries: uniq(bs.First), Comment: "boss"})
	b.last, b.haveLast = bs.Position, true
	if bs.LootItem != nil {
		b.add(StepRow{Kind: KindLoot, Entry: bs.Entry, X: bs.Position.X, Y: bs.Position.Y, Z: bs.Position.Z, Item: bs.LootItem.Entry, Comment: clip("boss loot: "+bs.LootItem.String(), MaxComment)})
	}
	return nil
}

func refPoint(r dungeon.ObjectRef) (x, y, z float32) {
	if r.Near != nil {
		return r.Near.X, r.Near.Y, r.Near.Z
	}
	return 0, 0, 0
}

func itemEntry(i *dungeon.ItemRef) uint32 {
	if i == nil {
		return 0
	}
	return i.Entry
}

// carrierSweepRadiusYd is the sweep pull's radius at a carrier chest: the trap's adds rise on the chest's point (Zul'Farrak's Shallow
// Grave trap 128972 spawns at the grave itself), so the ones that engage at once are fought by the fight block and an idle stray of
// this chest's pair is within a few yards of it; one grave's adds only, not the next grave's (the nearest stands 3.9 yd off).
const carrierSweepRadiusYd = 15

// carrierChest writes a companions_loot-false leg's chest (Options.CarrierChests): the move to its stand, the carrier's loot row with
// FlagGoneIsDone (a looted chest of spawntimesecs 7200 despawns, so a resumed run takes the gone chest as done instead of pausing on
// it), and — when the leg has leftover_entries — the sweep pull (the chest's linked trap raises its adds in the same server call the
// loot ACK answers, and round 3's eleven leftovers aggroed the next walk; the loot row is never safe without the sweep). batch is
// ignored: one chest a row keeps the adds two at a time (batches of 3+ wiped twice in round 4); the `loot_object batch` warning stays.
func (b *builder) carrierChest(l dungeon.Leg, lo *dungeon.LootObject) {
	if lo.Stand != nil {
		b.move(*lo.Stand, false, "stand")
	}
	x, y, z := refPoint(lo.ObjectRef)
	b.add(StepRow{Kind: KindLoot, Entry: lo.Entry, X: x, Y: y, Z: z, Item: itemEntry(lo.ExpectItem), Flags: FlagGoneIsDone,
		Comment: clip(itemComment("loot", lo.ExpectItem), MaxComment)})
	if len(l.LeftoverEntries) > 0 {
		b.add(StepRow{Kind: KindPull, X: x, Y: y, Z: z, Radius: carrierSweepRadiusYd, Entries: uniq(l.LeftoverEntries),
			Comment: clip("sweep: what the chest raised", MaxComment)})
	}
}

func (b *builder) step(st dungeon.Step) error {
	switch {
	case st.UseObject != nil:
		u := st.UseObject
		if u.ViaItem {
			return fmt.Errorf("use_object %d with via_item (the item is used on the object): the run leader's open row cannot carry it", u.Entry)
		}
		if u.Stand != nil {
			b.move(*u.Stand, false, "stand")
		}
		x, y, z := refPoint(u.ObjectRef)
		b.add(StepRow{Kind: KindOpen, Entry: u.Entry, X: x, Y: y, Z: z, Item: itemEntry(u.RequiresItem), Comment: itemComment("open", u.RequiresItem)})
	case st.LootObject != nil:
		l := st.LootObject
		x, y, z := refPoint(l.ObjectRef)
		b.add(StepRow{Kind: KindLoot, Entry: l.Entry, X: x, Y: y, Z: z, Item: itemEntry(l.ExpectItem), Comment: itemComment("loot", l.ExpectItem)})
	case st.UseItem != nil:
		u := st.UseItem
		b.add(StepRow{Kind: KindUseItem, Entry: u.Entry, Item: itemEntry(u.ExpectItem), Param: itemEntry(u.RequiresItem), Comment: clip("use "+nameOr(u.Name, fmt.Sprintf("item %d", u.Entry))+madeNote(u.ExpectItem), MaxComment)})
	case st.WaitFor != nil:
		return b.wait(st.WaitFor)
	case st.AreaTrigger != nil:
		// The run leader has no area trigger step. A lead bot that walks into the zone takes the trigger itself when the module lists it
		// (AiPlayerbot.BotAreaTriggers, TortoiseBots #1002), which is the plan's "auto" and "bot": a move to the stand point. The agent's own
		// character ("agent") and the expect check exist only in a driven run.
		t := st.AreaTrigger
		if t.By == dungeon.TriggerAgent {
			return noStep("area_trigger %d: by \"agent\" has no run leader step (the leader is a bot: use \"auto\" or \"bot\")", t.ID)
		}
		b.move(t.Stand, false, clip(fmt.Sprintf("area trigger %d: the bot walks into the zone; needs AiPlayerbot.BotAreaTriggers = %d (TortoiseBots #1002); expect %s", t.ID, t.ID, t.Expect), MaxComment))
	case st.Ritual != nil:
		r := st.Ritual
		// param: the participants, the bots the run leader sends to use the object (gameobject_template data0 of a summoning ritual).
		b.add(StepRow{Kind: KindRitual, Entry: r.Entry, X: r.Position.X, Y: r.Position.Y, Z: r.Position.Z, Param: uint32(r.Participants), Comment: fmt.Sprintf("ritual of %d", r.Participants)})
	case st.Gossip != nil:
		g := st.Gossip
		switch {
		case g.Talker() == dungeon.GossipByAgent:
			return noStep("gossip (NPC %d, %s): by \"agent\" is the agent's own character talking, which a bound party has none of (use by \"tank\" or \"carrier\": the lead bot talks)", g.NPC, g.OptionText())
		case g.Option == nil:
			return noStep("gossip (NPC %d, %s): names its option by text, and the run leader's row carries the option number (the menu's index, \"option\")", g.NPC, g.OptionText())
		case *g.Option+1 > MaxGossipOption:
			return fmt.Errorf("gossip (NPC %d, %s): the run leader's row counts the menu from 1 and holds at most %d (a menu holds %d options)", g.NPC, g.OptionText(), MaxGossipOption, MaxGossipOption)
		}
		// x y z: where the NPC stands (the run leader walks the lead bot there when it is farther than 30 yd, and the order searches within 40 yd
		// of the bot). The row's param is the option the way `.bot cmd gossip` counts it, from 1: the plan's option (the menu index) + 1.
		b.add(StepRow{Kind: KindGossip, Entry: g.NPC, X: g.Near.X, Y: g.Near.Y, Z: g.Near.Z, Param: *g.Option + 1,
			Comment: clip(gossipComment(g), MaxComment)})
	default:
		return fmt.Errorf("an empty step")
	}
	return nil
}

func (b *builder) wait(w *dungeon.WaitFor) error {
	timeout := w.TimeoutS
	if w.Hold != nil {
		// The wait's run leader form (dungeon.Hold on a wait_for): the event's hold row in place of the wait, which the run leader may not
		// be able to run at all (a unit wait). The agent's driver still runs the wait as written.
		b.hold(w.Hold, uniq(w.Hold.Entries), "wait hold")
		return nil
	}
	switch {
	case w.GameObject != nil:
		g := w.GameObject
		param := -1
		for i, s := range dungeon.ObjectStates {
			if s == g.State {
				param = i
			}
		}
		if param < 0 {
			return fmt.Errorf("wait_for.gameobject: state %q is none of %v", g.State, dungeon.ObjectStates)
		}
		x, y, z := refPoint(g.ObjectRef)
		base := "wait for object " + strconv.Itoa(int(g.Entry)) + " " + g.State
		if n := len(b.rows.Steps); n > 0 && b.rows.Steps[n-1].Leg == b.outLeg && b.rows.Steps[n-1].Kind == KindHold && (timeout == 0 || timeout > dungeon.HoldDoorWaitS) {
			// The hold before it ended with the event DONE, which is what opens the door: no fight time to wait for.
			timeout = dungeon.HoldDoorWaitS
			base += fmt.Sprintf(" (at most %d s: the hold before it ends with the event done)", timeout)
		}
		b.add(StepRow{Kind: KindWaitObject, Entry: g.Entry, X: x, Y: y, Z: z, Param: uint32(param), TimeoutS: timeout, Comment: clip(waitComment(base, w), MaxComment)})
	case w.HostilesGone != nil:
		h := w.HostilesGone
		// A wait with no centre is about the party's own spot: the last point the leg put it at.
		c := dungeon.Point{}
		switch {
		case h.Center != nil:
			c = *h.Center
		case b.haveLast:
			c = b.last
		default:
			c = b.plan.Legs[b.leg_].To
		}
		b.add(StepRow{Kind: KindWaitClear, X: c.X, Y: c.Y, Z: c.Z, Radius: h.Radius, TimeoutS: timeout, Comment: clip(waitComment("wait until no hostile", w), MaxComment)})
	case w.Unit != nil:
		return noStep("wait_for.unit has no run leader step (%s)", w.Unit.String())
	default:
		return fmt.Errorf("wait_for: neither gameobject nor hostiles_gone")
	}
	return nil
}

// gossipComment is the row's comment: the plan's option and the plan's failure text.
func gossipComment(g *dungeon.Gossip) string {
	c := fmt.Sprintf("talk to %d, menu index %d (cmd gossip option %d)", g.NPC, *g.Option, *g.Option+1)
	if g.FailText != "" {
		c += ": " + g.FailText
	}
	return c
}

func waitComment(base string, w *dungeon.WaitFor) string {
	if w.FailText != "" {
		return base + ": " + w.FailText
	}
	return base
}

func itemComment(verb string, i *dungeon.ItemRef) string {
	if i == nil {
		return verb
	}
	return verb + " (" + i.String() + ")"
}

func madeNote(i *dungeon.ItemRef) string {
	if i == nil {
		return ""
	}
	return " -> " + i.String()
}

func nameOr(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

// Validate checks rows the way the module's loader would, so that a row it would skip is a refusal here.
func Validate(r Rows) error {
	for i, s := range r.Steps {
		where := fmt.Sprintf("plan %s leg %d step %d (%s)", s.Plan, s.Leg, s.Seq, s.Kind)
		bad := func(format string, a ...any) error { return fmt.Errorf("%s: %s", where, fmt.Sprintf(format, a...)) }
		if s.Radius < 0 || s.Radius > MaxRadius {
			return bad("radius %.1f is outside 0-%d", s.Radius, MaxRadius)
		}
		if len(s.Kind) > MaxKind {
			return bad("the kind is longer than ai_dungeon_step.kind holds (%d)", MaxKind)
		}
		if s.TimeoutS < 0 || s.TimeoutS > MaxTimeoutS {
			return bad("timeout_s %d is outside 0-%d", s.TimeoutS, MaxTimeoutS)
		}
		if len(s.Entries) > MaxEntries {
			return bad("%d entries (at most %d)", len(s.Entries), MaxEntries)
		}
		switch s.Kind {
		case KindMove, KindMoveDirect, KindMoveHopExact, KindRest:
		case KindPull:
			if len(s.Entries) == 0 {
				return bad("a pull needs entries")
			}
		case KindBoss, KindLoot, KindOpen, KindUseItem, KindWaitObject, KindRitual:
			if s.Entry == 0 {
				return bad("needs an entry")
			}
		case KindGossip:
			if s.Entry == 0 {
				return bad("needs an entry")
			}
			// The loader's rule (DungeonPlanPolicy.h NormalizePlanStep): the option counts from 1, `.bot cmd gossip` refuses 0; no flag is read.
			if s.Param < 1 || s.Param > MaxGossipOption {
				return bad("param %d is outside 1-%d (the option, counted from 1)", s.Param, MaxGossipOption)
			}
			if s.Flags != 0 {
				return bad("a gossip carries no flags")
			}
		case KindHold:
			if s.Entry == 0 || s.Flags&FlagDoneData == 0 {
				return bad("a hold needs its instance data (entry) and done flag")
			}
			// An encounter state is 0-4; a plain script value (flag 8 without 32) may be any number (the loader's rule).
			if s.Flags&FlagEncounter != 0 && s.Param > 4 {
				return bad("param %d is no encounter state (0-4)", s.Param)
			}
		case KindWaitClear:
			if s.Radius <= 0 {
				return bad("a wait_clear needs a radius")
			}
		default:
			return bad("unknown kind")
		}
		if s.Kind == KindWaitObject && s.Param > 2 {
			return bad("param %d is above 2", s.Param)
		}
		if s.Flags&FlagGoneIsDone != 0 {
			// The exporter's own rule (ours, not the module's): the module reads the flag off a loot/open row only, so the exporter refuses
			// it on any other kind; a module that knows no such step flag yet ignores unknown flags on a loot/open and pauses on a gone
			// object, so nothing breaks either way.
			if s.Kind != KindLoot && s.Kind != KindOpen {
				return bad("the gone-is-done flag is for a loot or open row: the exporter refuses it on a %s (a module without the step flag ignores it there)", s.Kind)
			}
		}
		if s.Flags&FlagExact != 0 {
			if s.Kind != KindMove {
				return bad("the exact flag is for a move row (the loader refuses it on a %s)", s.Kind)
			}
			if i+1 >= len(r.Steps) || r.Steps[i+1].Plan != s.Plan || r.Steps[i+1].Leg != s.Leg || r.Steps[i+1].Kind != KindMoveHopExact {
				return bad("an exact move must be followed by the move_hop_exact it launches (a row came between them, or there is none)")
			}
		}
		if s.Kind == KindMoveHopExact {
			if i == 0 || r.Steps[i-1].Plan != s.Plan || r.Steps[i-1].Leg != s.Leg || r.Steps[i-1].Kind != KindMove || r.Steps[i-1].Flags&FlagExact == 0 {
				return bad("a move_hop_exact must follow the exact move (flags %d) it is launched from", FlagExact)
			}
			if s.Flags != 0 {
				return bad("a move_hop_exact carries no flags")
			}
		}
		if len(s.Comment) > MaxComment {
			return bad("comment longer than %d", MaxComment)
		}
	}
	return nil
}

// cutLongHops finds consecutive moves of the plan that are more than MoveGapWarnYd apart. With Options.Cut set it puts the points the
// cutter returns between them (as move rows, renumbering the leg's steps); without, it lists them in Warnings.
func (b *builder) cutLongHops() error {
	var out []StepRow
	var prev *StepRow
	renumber := map[int]int{} // leg -> next seq
	for i := range b.rows.Steps {
		s := b.rows.Steps[i]
		if b.opt.ApproachHops && b.opt.Cut != nil && prev != nil && (s.Kind == KindPull || s.Kind == KindBoss || s.Kind == KindHold) {
			a, c := dungeon.Point{X: prev.X, Y: prev.Y, Z: prev.Z}, dungeon.Point{X: s.X, Y: s.Y, Z: s.Z}
			if d := dist3(a, c); d > dungeon.RunLeaderReachYd {
				via, err := b.opt.Cut(b.plan.Map, a, c)
				if err != nil {
					b.warnings = append(b.warnings, fmt.Sprintf("leg %d step %d: the %s is %.0f yd from the anchor before it and the approach could not be cut: %v", s.Leg, s.Seq, s.Kind, d, err))
				}
				for len(via) > 0 && dist3(via[len(via)-1], c) < approachStandoffYd {
					via = via[:len(via)-1]
				}
				for _, v := range via {
					out = append(out, StepRow{Plan: s.Plan, Leg: s.Leg, Kind: KindMove, X: v.X, Y: v.Y, Z: v.Z, Comment: "approach path"})
				}
			}
		}
		if (s.Kind == KindPull || s.Kind == KindBoss || s.Kind == KindHold) && prev != nil {
			// The party fought there (a pull or boss it saw from its move point, a hold on its spot): the next move's hop is measured
			// from it, not from the move before the fight (a leg's first move after a boss would otherwise be walked back from the
			// leg before's last move and forth again: Uldaman's legs 6 and 7, export of dd05724).
			cp := StepRow{Plan: s.Plan, Leg: s.Leg, Kind: s.Kind, X: s.X, Y: s.Y, Z: s.Z}
			prev = &cp
		}
		if s.Kind == KindMove || s.Kind == KindMoveDirect || s.Kind == KindMoveHopExact {
			// The hop after an exact move is indivisible: no cut point may come between the exact move and it (the run leader launches
			// it from that move only; Validate refuses rows that separate them). It is still the anchor for the next hop.
			if prev != nil && s.Kind != KindMoveHopExact {
				a, c := dungeon.Point{X: prev.X, Y: prev.Y, Z: prev.Z}, dungeon.Point{X: s.X, Y: s.Y, Z: s.Z}
				if d := dist3(a, c); d > MoveGapWarnYd {
					if b.opt.Cut == nil {
						b.warnings = append(b.warnings, fmt.Sprintf("leg %d step %d: the move is %.0f yd from the one before it (the module logs hops over %d yd); export with --mmaps to cut it into via points", s.Leg, s.Seq, d, MoveGapWarnYd))
					} else {
						via, err := b.opt.Cut(b.plan.Map, a, c)
						if err != nil {
							b.warnings = append(b.warnings, fmt.Sprintf("leg %d step %d: the move is %.0f yd from the one before it and could not be cut: %v", s.Leg, s.Seq, d, err))
						}
						for _, v := range via {
							out = append(out, StepRow{Plan: s.Plan, Leg: s.Leg, Kind: KindMove, X: v.X, Y: v.Y, Z: v.Z, Comment: "path"})
						}
					}
				}
			}
			cp := s
			out = append(out, cp)
			prev = &out[len(out)-1]
			continue
		}
		out = append(out, s)
	}
	for i := range out {
		out[i].Seq = renumber[out[i].Leg]
		renumber[out[i].Leg]++
	}
	b.rows.Steps = out
	return nil
}

// routeAroundAvoid puts via points between two anchors (move, pull, boss, hold rows, in the order of the plan, from the entrance) whose
// run leader path would cross an avoid box of the plan. The run leader walks to a pull or boss in hops of the lead bot's own navmesh
// path, which does not know the boxes (ai_dungeon_step has no column for one): only the rows can keep it out of the lava. Route returns
// the points; a hop it cannot make safe fails the export.
func (b *builder) routeAroundAvoid() error {
	if b.opt.Route == nil || len(b.plan.Avoid) == 0 {
		return nil
	}
	var out []StepRow
	prev := b.plan.Entrance
	added := map[int]int{}
	for _, s := range b.rows.Steps {
		switch s.Kind {
		case KindMove, KindMoveDirect, KindMoveHopExact, KindPull, KindBoss, KindHold:
		default:
			out = append(out, s)
			continue
		}
		to := dungeon.Point{X: s.X, Y: s.Y, Z: s.Z}
		if s.Kind != KindMoveDirect && s.Kind != KindMoveHopExact {
			via, err := b.opt.Route(b.plan.Map, prev, to)
			if err != nil {
				return fmt.Errorf("leg %d step %d (%s): %w", s.Leg, s.Seq, s.Kind, err)
			}
			for _, v := range via {
				out = append(out, StepRow{Plan: s.Plan, Leg: s.Leg, Kind: KindMove, X: v.X, Y: v.Y, Z: v.Z, Comment: "path (keeps out of an avoid box)"})
			}
			added[s.Leg] += len(via)
		}
		out = append(out, s)
		prev = to
	}
	renumber := map[int]int{}
	for i := range out {
		out[i].Seq = renumber[out[i].Leg]
		renumber[out[i].Leg]++
	}
	b.rows.Steps = out
	legs := make([]int, 0, len(added))
	for l, n := range added {
		if n > 0 {
			legs = append(legs, l)
		}
	}
	slices.Sort(legs)
	for _, l := range legs {
		b.warnings = append(b.warnings, fmt.Sprintf("leg %d: %d via points added so that the run leader's own path keeps out of the plan's avoid boxes (the run leader does not know them)", l, added[l]))
	}
	return nil
}

// approachStandoffYd is how near the pack a cut approach path's last point may be (the party is not walked onto the pack).
const approachStandoffYd = 25

func dist3(a, b dungeon.Point) float64 {
	return math.Sqrt(sq(float64(a.X-b.X)) + sq(float64(a.Y-b.Y)) + sq(float64(a.Z-b.Z)))
}

func sq(v float64) float64  { return v * v }
func f32(v float64) float32 { return float32(math.Round(v*100) / 100) }
func ceil1(v float64) float32 {
	return float32(math.Ceil(v*10) / 10)
}

func clip(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return strings.ToValidUTF8(s[:n], "")
}

// StepCount is the number of ai_dungeon_step rows: the step count the run leader lists for this plan (`.bot run list`).
func (r Rows) StepCount() int { return len(r.Steps) }

// LegNumber is the ai_dungeon_leg.leg number the export gives the plan's leg leg. An optional leg is left out and the legs after it
// close up, so this is not the plan's own index. ok is false for a leg outside the plan and for an optional leg (which has no rows).
func LegNumber(p *dungeon.Plan, leg int) (int, bool) {
	if leg < 0 || leg >= len(p.Legs) || p.Legs[leg].Optional != nil {
		return 0, false
	}
	row := 0
	for i := 0; i < leg; i++ {
		if p.Legs[i].Optional == nil {
			row++
		}
	}
	return row, true
}

// LegNumber is LegNumber(p, leg), and false also when these rows were cut before that leg (it has no rows at all).
func (r Rows) LegNumber(p *dungeon.Plan, leg int) (int, bool) {
	row, ok := LegNumber(p, leg)
	if !ok || row >= len(r.Legs) {
		return 0, false
	}
	return row, true
}

// PlanLeg is the plan leg index of the exported row leg rowLeg (false when no leg has that number: a cut plan, or a leg outside it).
func PlanLeg(p *dungeon.Plan, rowLeg int) (int, bool) {
	for i := range p.Legs {
		if n, ok := LegNumber(p, i); ok && n == rowLeg {
			return i, true
		}
	}
	return 0, false
}

// StepPoint is the point of the rows' step step (1-based), and false when the rows have no such step. A pull or boss step's point is
// where the party fights; a move's is where it walks. The observer's place check compares the party with it.
func (r Rows) StepPoint(step int) (dungeon.Point, bool) {
	if step < 1 || step > len(r.Steps) {
		return dungeon.Point{}, false
	}
	s := r.Steps[step-1]
	return dungeon.Point{X: s.X, Y: s.Y, Z: s.Z}, true
}

// GroupStep is the 1-based step number, as the run leader counts its own plan, of the plan leg's pull group group: its first row (the
// group's approach move, else its pull or hold row). ok is false when the rows have no such group (an optional leg, a cut plan, a group
// number the leg does not have).
func (r Rows) GroupStep(p *dungeon.Plan, leg, group int) (int, bool) {
	if leg < 0 || leg >= len(p.Legs) || group < 0 || group >= len(p.Legs[leg].PullGroups) {
		return 0, false
	}
	rowLeg, ok := r.LegNumber(p, leg)
	if !ok {
		return 0, false
	}
	// The exporter puts this comment on the group's own rows (pull, hold and approach moves): "pull group <n>" or "pull group <n> ...".
	prefix := "pull group " + strconv.Itoa(group)
	marker := -1
	for i, s := range r.Steps {
		if s.Leg != rowLeg {
			continue
		}
		if s.Comment == prefix || strings.HasPrefix(s.Comment, prefix+" ") {
			marker = i
			break
		}
	}
	if marker < 0 {
		return 0, false
	}
	// cutLongHops and routeAroundAvoid put the walk to the group in front of its first row: "approach path" (the hop to a pack farther
	// than the run leader's reach), "path" (a long move cut into via points) and "path (keeps out of an avoid box)". The group starts
	// after the previous anchor (a pull, boss or hold, or a move that is not one of those walk rows), so walk back over them.
	i := marker
	for i > 0 {
		prev := r.Steps[i-1]
		if prev.Leg != rowLeg || !walkRow(prev) {
			break
		}
		i--
	}
	return i + 1, true
}

// walkRow says whether a row was put in only to walk: a cut hop or an avoid-box route, not a point of the plan.
func walkRow(s StepRow) bool {
	switch s.Comment {
	case "approach path", "path", "path (keeps out of an avoid box)":
		return s.Kind == KindMove
	}
	return false
}
