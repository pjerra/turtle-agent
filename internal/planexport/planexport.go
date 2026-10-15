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
	KindPull       = "pull"
	KindBoss       = "boss"
	KindLoot       = "loot"
	KindOpen       = "open"
	KindUseItem    = "use_item"
	KindWaitObject = "wait_object"
	KindWaitClear  = "wait_clear"
	KindRest       = "rest"
	KindRitual     = "ritual"
)

// Flags of ai_dungeon_step.flags.
const (
	FlagAmbush   = 1
	FlagAlliance = 2
	FlagHorde    = 4
)

// The limits of the module's loader (NormalizePlanStep) and of its column widths.
const (
	MaxRadius      = 100   // yards
	MaxTimeoutS    = 600   // seconds
	MaxEntries     = 16    // creature entries of a pull or boss
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
	b := &builder{opt: opt, plan: p, name: name}
	b.rows.Plan = PlanRow{Plan: name, Map: p.Map, CCPriority: slices.Clone(p.CCPriority), CCAlways: slices.Clone(p.CCAlways), KillFirst: slices.Clone(p.KillFirst), Source: clip(source, MaxSource), Comment: clip(planComment(p), MaxComment)}
	cut := ""
	for li := range p.Legs {
		nl, ns, nw := len(b.rows.Legs), len(b.rows.Steps), len(b.warnings)
		if err := b.leg(li); err != nil {
			if li > 0 && errors.Is(err, ErrNoRunLeaderStep) {
				// The legs before it are a plan the run leader can run: export them, and say where and why the rest was left off.
				b.rows.Legs, b.rows.Steps, b.warnings = b.rows.Legs[:nl], b.rows.Steps[:ns], b.warnings[:nw]
				cut = fmt.Sprintf("plan %s: exported legs 0-%d (%s) only; cut before leg %d (%s), which has a step the run leader cannot do: %v; that leg and the %d after it are not in the rows", name, li-1, p.Legs[li-1].Name, li, p.Legs[li].Name, err, len(p.Legs)-li-1)
				break
			}
			return Result{}, fmt.Errorf("plan %s: leg %d (%s): %w", name, li, p.Legs[li].Name, err)
		}
	}
	if cut != "" {
		b.warnings = append(b.warnings, cut)
		b.rows.Plan.Comment = clip(planComment(p)+"; cut before leg "+strconv.Itoa(len(b.rows.Legs))+" ("+p.Legs[len(b.rows.Legs)].Name+"): a step the run leader cannot do", MaxComment)
	}
	if err := b.cutLongHops(); err != nil {
		return Result{}, err
	}
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
	leg_        int
	seq         int
	skippedLoot int           // loot_object steps of a companions_loot false leg left out of the leg being built
	last        dungeon.Point // the last point a step of the leg put the party at
	haveLast    bool
}

func (b *builder) add(s StepRow) {
	s.Plan, s.Leg, s.Seq = b.name, b.leg_, b.seq
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
	b.leg_, b.seq, b.haveLast, b.skippedLoot = li, 0, false, 0
	if l.CompanionsLoot != nil && !*l.CompanionsLoot && !b.opt.DropCompanionsLoot {
		return fmt.Errorf("companions_loot is false and the run leader's rows cannot carry it (TortoiseBots has no such column): its bots would loot what this leg wants left alone; export is refused (pass DropCompanionsLoot / --drop-companions-loot to export without it): %w", ErrCompanionsLoot)
	}
	b.rows.Legs = append(b.rows.Legs, LegRow{Plan: b.name, Leg: li, Name: clip(l.Name, MaxLegName)})
	// The leg in the order the driver does it: the via waypoints, the To, the pull groups, the steps, the boss, the steps after it.
	for _, w := range l.Via {
		b.move(w.Point, w.Direct, "via")
	}
	b.move(l.To, false, "to")
	for gi, g := range l.PullGroups {
		if err := b.pull(gi, g); err != nil {
			return fmt.Errorf("pull_groups[%d]: %w", gi, err)
		}
	}
	for si, st := range l.Steps {
		// A leg whose companions_loot is false (only exported with DropCompanionsLoot) has its chests opened by the carrier bot alone: with the
		// leg's looting setting dropped those steps would send bots at them, so they are left out, with a warning.
		if st.LootObject != nil && l.CompanionsLoot != nil && !*l.CompanionsLoot {
			b.skippedLoot++
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
	b.add(StepRow{Kind: KindPull, X: f32(cx), Y: f32(cy), Z: f32(cz), Radius: radius, Flags: flags, Entries: uniq(g.Entries), Comment: c})
	b.last, b.haveLast = dungeon.Point{X: f32(cx), Y: f32(cy), Z: f32(cz)}, true
	return nil
}

func (b *builder) boss(bs *dungeon.Boss) error {
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

func (b *builder) step(st dungeon.Step) error {
	switch {
	case st.UseObject != nil:
		u := st.UseObject
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
		b.add(StepRow{Kind: KindUseItem, Entry: u.Entry, Item: itemEntry(u.ExpectItem), Comment: clip("use "+nameOr(u.Name, fmt.Sprintf("item %d", u.Entry))+madeNote(u.ExpectItem), MaxComment)})
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
		b.add(StepRow{Kind: KindRitual, Entry: r.Entry, X: r.Position.X, Y: r.Position.Y, Z: r.Position.Z, Comment: fmt.Sprintf("ritual of %d", r.Participants)})
	case st.Gossip != nil:
		g := st.Gossip
		return noStep("gossip (NPC %d, %s): the run leader's plan has no gossip step yet, so a plan with one cannot be exported until it has", g.NPC, g.OptionText())
	default:
		return fmt.Errorf("an empty step")
	}
	return nil
}

func (b *builder) wait(w *dungeon.WaitFor) error {
	timeout := w.TimeoutS
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
		b.add(StepRow{Kind: KindWaitObject, Entry: g.Entry, X: x, Y: y, Z: z, Param: uint32(param), TimeoutS: timeout, Comment: clip(waitComment("wait for object "+strconv.Itoa(int(g.Entry))+" "+g.State, w), MaxComment)})
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
	for _, s := range r.Steps {
		where := fmt.Sprintf("plan %s leg %d step %d (%s)", s.Plan, s.Leg, s.Seq, s.Kind)
		bad := func(format string, a ...any) error { return fmt.Errorf("%s: %s", where, fmt.Sprintf(format, a...)) }
		if s.Radius < 0 || s.Radius > MaxRadius {
			return bad("radius %.1f is outside 0-%d", s.Radius, MaxRadius)
		}
		if s.TimeoutS < 0 || s.TimeoutS > MaxTimeoutS {
			return bad("timeout_s %d is outside 0-%d", s.TimeoutS, MaxTimeoutS)
		}
		if len(s.Entries) > MaxEntries {
			return bad("%d entries (at most %d)", len(s.Entries), MaxEntries)
		}
		switch s.Kind {
		case KindMove, KindMoveDirect, KindRest:
		case KindPull:
			if len(s.Entries) == 0 {
				return bad("a pull needs entries")
			}
		case KindBoss, KindLoot, KindOpen, KindUseItem, KindWaitObject, KindRitual:
			if s.Entry == 0 {
				return bad("needs an entry")
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
		if s.Kind == KindMove || s.Kind == KindMoveDirect {
			if prev != nil {
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
