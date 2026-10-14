// Package planexport turns a dungeon plan (dungeons/<name>.json, plan format v1) into the rows of TortoiseBots' run leader
// (ai_dungeon_plan, ai_dungeon_leg, ai_dungeon_step) and writes them as one idempotent world migration.
//
// The mapping is TortoiseBots' docs/guides/dungeon-run-leader.md ("From plan v1 to rows"); the limits are the ones its loader
// enforces (ai/playerbot/DungeonPlanPolicy.h, NormalizePlanStep). The plan file stays the single source: edit it here and export
// again, never the rows by hand.
package planexport

import (
	"fmt"
	"math"
	"regexp"
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
	Plan    string
	Map     uint32
	Source  string
	Comment string
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
	// Snap, when set, is asked for every move and move_direct point: it returns the point on the navmesh the move should aim at and
	// whether it changed the point (a point that is on the mesh stays as the plan has it). A false ok with a note means the point could
	// not be put on the mesh; the note goes to Warnings.
	Snap func(mapID uint32, p dungeon.Point) (snapped dungeon.Point, note string)
	// EndStop, when set, appends a last leg holding one unsupported step (a ritual of Entry at Pos): the run leader pauses on it and
	// names the leg, so the run ends there with a clear reason instead of doing something it cannot.
	EndStop *EndStop
}

// EndStop is the "stop: not supported" step that ends a plan before an event the run leader cannot do yet.
type EndStop struct {
	Leg     string        // the leg's name, shown in the TBM:RUN status line
	Entry   uint32        // the ritual step's entry (a step kind that pauses the run; it must be non-zero)
	Pos     dungeon.Point // where the object stands
	Comment string
}

// Result is a plan's rows and what the exporter had to say about them.
type Result struct {
	Rows     Rows
	Warnings []string
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
	b := &builder{opt: opt, plan: p, name: name}
	b.rows.Plan = PlanRow{Plan: name, Map: p.Map, Source: clip(source, MaxSource), Comment: clip(planComment(p), MaxComment)}
	for li := range p.Legs {
		if err := b.leg(li); err != nil {
			return Result{}, fmt.Errorf("plan %s: leg %d (%s): %w", name, li, p.Legs[li].Name, err)
		}
	}
	if err := b.cutLongHops(); err != nil {
		return Result{}, err
	}
	b.snapMoves()
	if es := opt.EndStop; es != nil {
		if es.Entry == 0 || es.Leg == "" {
			return Result{}, fmt.Errorf("end stop: needs a leg name and a non-zero entry")
		}
		li := len(b.rows.Legs)
		if li >= MaxLegs {
			return Result{}, fmt.Errorf("end stop: no leg number left")
		}
		b.rows.Legs = append(b.rows.Legs, LegRow{Plan: name, Leg: li, Name: clip(es.Leg, MaxLegName), Comment: clip(es.Comment, MaxComment)})
		b.rows.Steps = append(b.rows.Steps, StepRow{Plan: name, Leg: li, Seq: 0, Kind: KindRitual, Entry: es.Entry, X: es.Pos.X, Y: es.Pos.Y, Z: es.Pos.Z, Comment: clip("STOP: not supported. "+es.Comment, MaxComment)})
	}
	if err := Validate(b.rows); err != nil {
		return Result{}, err
	}
	return Result{Rows: b.rows, Warnings: b.warnings}, nil
}

func planComment(p *dungeon.Plan) string {
	c := "plan v1"
	if p.Status != "" {
		c += ", " + p.Status
	}
	return c
}

type builder struct {
	opt      Options
	plan     *dungeon.Plan
	name     string
	rows     Rows
	warnings []string
	leg_     int
	seq      int
	last     dungeon.Point // the last point a step of the leg put the party at
	haveLast bool
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
	b.leg_, b.seq, b.haveLast = li, 0, false
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
	if pt := g.Patrol; pt != nil {
		// The run leader has no patrol column: the note is for the reader (and the owner's log), the pull is the same.
		c += fmt.Sprintf(" (patrol %s: %d points, %.0f s round)", joinEntries(uniq(pt.Entries)), len(pt.Path), pt.PeriodS)
	}
	b.add(StepRow{Kind: KindPull, X: f32(cx), Y: f32(cy), Z: f32(cz), Radius: radius, Flags: flags, Entries: uniq(g.Entries), Comment: c})
	b.last, b.haveLast = dungeon.Point{X: f32(cx), Y: f32(cy), Z: f32(cz)}, true
	return nil
}

func joinEntries(es []uint32) string {
	var p []string
	for _, e := range es {
		p = append(p, strconv.FormatUint(uint64(e), 10))
	}
	return strings.Join(p, ",")
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
	case st.Ritual != nil:
		r := st.Ritual
		b.add(StepRow{Kind: KindRitual, Entry: r.Entry, X: r.Position.X, Y: r.Position.Y, Z: r.Position.Z, Comment: fmt.Sprintf("ritual of %d", r.Participants)})
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

// ApproachStopYd is how far from a pull or boss the approach via points stop: the run leader walks the last stretch itself (it
// gathers the party and pulls from there), so the points get it within pulling distance but not onto the pack.
const ApproachStopYd = 30

// positioned reports whether a step puts the party at a point of its own (a walk, a pull's centre, a boss), which the next step's
// hop is measured from.
func positioned(k string) bool {
	return k == KindMove || k == KindMoveDirect || k == KindPull || k == KindBoss
}

// cutLongHops finds steps that are more than MoveGapWarnYd from the step before them that put the party somewhere: two moves, or a
// move, pull or boss after any of those (across legs too: a leg starts where the one before it ended, at its last pull or boss, not at
// its last move). With Options.Cut set it puts the points the cutter returns between them (as move rows, renumbering the leg's
// steps): a pull or boss gets the points of the path up to ApproachStopYd short of it, so the run leader's own approach (a straight line
// that can leave the mesh) has little left to do. Without Cut it lists them in Warnings.
func (b *builder) cutLongHops() error {
	var out []StepRow
	var prev *StepRow
	renumber := map[int]int{} // leg -> next seq
	for i := range b.rows.Steps {
		s := b.rows.Steps[i]
		if positioned(s.Kind) {
			if prev != nil {
				a, c := dungeon.Point{X: prev.X, Y: prev.Y, Z: prev.Z}, dungeon.Point{X: s.X, Y: s.Y, Z: s.Z}
				if d := dist3(a, c); d > MoveGapWarnYd {
					if b.opt.Cut == nil {
						b.warnings = append(b.warnings, fmt.Sprintf("leg %d step %d: the %s is %.0f yd from the step before it (the module logs hops over %d yd); export with --mmaps to cut it into via points", s.Leg, s.Seq, s.Kind, d, MoveGapWarnYd))
					} else {
						via, err := b.opt.Cut(b.plan.Map, a, c)
						if err != nil {
							b.warnings = append(b.warnings, fmt.Sprintf("leg %d step %d: the %s is %.0f yd from the step before it and could not be cut: %v", s.Leg, s.Seq, s.Kind, d, err))
						}
						for _, v := range via {
							if (s.Kind == KindPull || s.Kind == KindBoss) && dist3(v, c) < ApproachStopYd {
								continue
							}
							out = append(out, StepRow{Plan: s.Plan, Leg: s.Leg, Kind: KindMove, X: v.X, Y: v.Y, Z: v.Z, Comment: "path"})
						}
					}
				}
			}
			out = append(out, s)
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

// snapMoves puts every move and move_direct point on the navmesh (Options.Snap): a point off the mesh is a point the lead bot's walk
// stops short of or refuses, so the plan gives the mesh's own point instead.
func (b *builder) snapMoves() {
	if b.opt.Snap == nil {
		return
	}
	for i := range b.rows.Steps {
		s := &b.rows.Steps[i]
		if s.Kind != KindMove && s.Kind != KindMoveDirect {
			continue
		}
		p, note := b.opt.Snap(b.plan.Map, dungeon.Point{X: s.X, Y: s.Y, Z: s.Z})
		if note != "" {
			b.warnings = append(b.warnings, fmt.Sprintf("leg %d step %d (%s): %s", s.Leg, s.Seq, s.Kind, note))
		}
		s.X, s.Y, s.Z = p.X, p.Y, p.Z
	}
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
