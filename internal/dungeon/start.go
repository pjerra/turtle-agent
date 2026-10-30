package dungeon

import (
	"context"
	"errors"
	"fmt"
	"math"
	"slices"
	"strings"
	"time"

	"github.com/pjerra/turtle-agent/internal/events"
)

// Starting anywhere (remote mode; dungeon_run from_leg and from_pull_group, owner-approved 2026-10-14 to make combat fixes faster to
// test). A run that does not start at the first leg of a fresh instance needs the party at that point. The commander is a GM and its own
// character is allowed its own GM move, so:
//
//	1. `.appear <lead bot>` when the agent is not on the dungeon's map near the lead bot (a GM enters the lead bot's instance that way; a
//	   `.go xyz` into the map from outside would open an instance of the agent's own);
//	2. `.go xyz <point> <map>`: the agent's own character stands at the start point;
//	3. `.bot summon <lead bot>`: a player command; the lead bot is the point `cmd summon` fetches the others to, so it comes first;
//	4. `cmd summon <bot>` for every other bot of the party, and a wait until all stand together.
//
// No GM command is aimed at another player. Both GM lines (1 and 2) count in gm_commands and are listed in setup_shortcuts. The legs
// before the start are reported "skipped (from_leg)", never as cleared (driver rule 1): their packs are alive, their doors shut and their
// items not collected; a start past something the plan needs (a key, an opened door) fails later at the step that needs it.
//
// The start point of a leg is where the leg before it ended (the entrance for leg 0, as the group driver's startPoint); the start point of
// a pull group is its gather spot: a point gatherStandoff yards short of the group's first position on the navmesh path from the leg's To,
// pushed back along the path until no other pack or boss of the plan stands within gatherSafe yards of it.

const (
	gatherStandoff = 55.0 // yards short of the pack the party is gathered
	gatherSafe     = 30.0 // yards: no other pack or boss of the plan nearer than this to the gather spot (when the path allows)
	startNear      = 20.0 // yards: the party is "at" the start point within this of it (the lead bot), and each bot within startTogether of the lead bot
	startTogether  = 25.0
	startSettle    = 12.0 // yards: the summoned lead bot lands within this of the agent
	holdSlack      = 4.0  // yards: the lead bot "holds" while it stays within this of the point it was told to hold
)

// holdWatch is how long the feed has to show the lead bot holding.
const holdWatch = 4 * time.Second

// The values of StartReport.Kind.
const (
	StartEntrance = "entrance"
	StartLeg      = "leg_start"
	StartGather   = "pull_gather"
)

// StartReport is what a run that starts inside the dungeon did to get there (Report.Start; absent for a run from the first leg's start).
type StartReport struct {
	Leg       int     `json:"leg"`
	LegName   string  `json:"leg_name,omitempty"`
	PullGroup *int    `json:"pull_group,omitempty"` // from_pull_group
	Kind      string  `json:"kind"`                 // entrance, leg_start or pull_gather
	Point     Point   `json:"point"`
	Map       uint32  `json:"map"`
	Method    string  `json:"method"` // "gm" (the commander's own move, then the summons) or "already there"
	Seconds   float64 `json:"seconds"`
	// GMCommands are the GM lines sent, in order: each one is also in the report's setup_shortcuts and counted in remote.gm_commands.
	GMCommands []string `json:"gm_commands"`
	// LeadSummoned says that `.bot summon <lead bot>` (a player command) brought the lead bot; Summoned are the bots `cmd summon` fetched.
	LeadSummoned bool     `json:"lead_summoned"`
	Summoned     []string `json:"summoned,omitempty"`
	// Skipped are the legs (and the leg's pull groups) before the start: "skipped (from_leg)", never cleared.
	Skipped []string `json:"skipped"`
	// Warning is set when the gather spot is nearer than gatherSafe to another pack of the plan.
	Warning string `json:"warning,omitempty"`
}

func (s *StartReport) clone() *StartReport {
	if s == nil {
		return nil
	}
	c := *s
	c.GMCommands = slices.Clone(s.GMCommands)
	c.Summoned = slices.Clone(s.Summoned)
	c.Skipped = slices.Clone(s.Skipped)
	if s.PullGroup != nil {
		g := *s.PullGroup
		c.PullGroup = &g
	}
	return &c
}

// StartHands is the optional part of a RemoteGame that starts a run inside the dungeon: the agent's own character, with the GM move.
type StartHands interface {
	Follower
	// GoXYZ sends `.go xyz x y z map` for the agent's own character and waits for its teleport.
	GoXYZ(ctx context.Context, mapID uint32, at Point) error
	// GPS sends the plain GM line `.gps` for the agent's own character and returns the lines the core answered with, joined by newlines
	// (empty and an error when no reply came).
	GPS(ctx context.Context) (string, error)
}

// LegStart is where a run from leg i begins: the entrance for leg 0, else where the leg before ended (its boss, else its To).
func (p *Plan) LegStart(i int) (Point, string) {
	if i <= 0 || i > len(p.Legs) {
		return p.Entrance, StartEntrance
	}
	l := p.Legs[i-1]
	if l.Boss != nil {
		return l.Boss.Position, StartLeg
	}
	return l.To, StartLeg
}

// GatherSpot is the point the party is gathered at for the pull group `group` of leg `leg`. path plans a path between two points of the
// map (nil: the straight line). The second result is a warning when no candidate stands gatherSafe yards clear of the plan's other packs.
func (p *Plan) GatherSpot(leg, group int, path func(from, to Point) []Point) (Point, string) {
	l := p.Legs[leg]
	g := l.PullGroups[group]
	target := g.Positions[0]
	anchor := l.To
	if dist3(anchor, target) <= gatherStandoff {
		return anchor, p.gatherWarning(leg, group, anchor)
	}
	var best Point
	bestClear := -1.0
	for _, standoff := range []float32{gatherStandoff, gatherStandoff + 15, gatherStandoff + 30} {
		pts := []Point{anchor, target}
		if path != nil {
			if got := path(anchor, target); len(got) >= 2 {
				pts = got
			}
		}
		spot, ok := StandoffPoint(pts, standoff)
		if !ok {
			spot = anchor
		}
		clear := p.nearestOther(leg, group, spot)
		if clear >= gatherSafe {
			return spot, ""
		}
		if clear > bestClear {
			best, bestClear = spot, clear
		}
	}
	return best, p.gatherWarning(leg, group, best)
}

// nearestOther is the distance from at to the nearest pack position or boss of the plan that is not the group itself.
func (p *Plan) nearestOther(leg, group int, at Point) float64 {
	best := math.Inf(1)
	for li, l := range p.Legs {
		for gi, g := range l.PullGroups {
			if li == leg && gi == group {
				continue
			}
			for _, pos := range g.Positions {
				best = math.Min(best, dist3(at, pos))
			}
		}
		if l.Boss != nil {
			best = math.Min(best, dist3(at, l.Boss.Position))
		}
	}
	return best
}

func (p *Plan) gatherWarning(leg, group int, at Point) string {
	if n := p.nearestOther(leg, group, at); n < gatherSafe {
		return fmt.Sprintf("the gather spot %.0f %.0f %.0f is %.0f yd from another pack or boss of the plan (a safe spot needs %.0f)", at.X, at.Y, at.Z, n, gatherSafe)
	}
	return ""
}

// earlierPack is the nearest pack or boss of a step the run skips, as nearestEarlier finds it.
type earlierPack struct {
	dist  float64
	leg   int
	group int    // the pull group; -1 for the leg's boss
	entry uint32 // the boss's entry
}

// String names it in a refusal or a warning.
func (e earlierPack) String() string {
	if e.group < 0 {
		return fmt.Sprintf("the boss of leg %d (entry %d)", e.leg, e.entry)
	}
	return PullGroupRef(e.group, 0) + fmt.Sprintf(" of leg %d", e.leg)
}

// nearestEarlier is nearestOther limited to the steps a start at pull group `group` of leg `leg` skips: every pull group
// of the legs before leg, the groups of leg before group, and the bosses of the legs before leg (a from_leg start is
// group 0 of its leg, so the boss of the leg before it is in). A pack of a later step is the run's own fight, not a
// danger at the start point. dist is +Inf when there is none.
func (p *Plan) nearestEarlier(leg, group int, at Point) earlierPack {
	best := earlierPack{dist: math.Inf(1)}
	for li, l := range p.Legs {
		if li > leg {
			break
		}
		for gi, g := range l.PullGroups {
			if li == leg && gi >= group {
				continue
			}
			for _, pos := range g.Positions {
				if d := dist3(at, pos); d < best.dist {
					best = earlierPack{dist: d, leg: li, group: gi}
				}
			}
		}
		if li < leg && l.Boss != nil {
			if d := dist3(at, l.Boss.Position); d < best.dist {
				best = earlierPack{dist: d, leg: li, group: -1, entry: l.Boss.Entry}
			}
		}
	}
	return best
}

// startNeeded says whether the run starts inside the dungeon: from a leg past the first, or at a pull group.
func (d *RemoteDriver) startNeeded(fromLeg int) bool {
	return fromLeg > 0 || d.opt.FromPullGroup != nil
}

// startGroup is the pull group the run starts at, or -1.
func (d *RemoteDriver) startGroup() int {
	if d.opt.FromPullGroup == nil {
		return -1
	}
	return *d.opt.FromPullGroup
}

// checkStart refuses a start the plan does not have and a start past an item the plan needs from a leg it skips. It
// runs before anything is sent; so does the unsafe-start gate (checkStartSafety).
func (d *RemoteDriver) checkStart(fromLeg int) error {
	if d.opt.FromPullGroup != nil {
		g := d.startGroup()
		if fromLeg < 0 || fromLeg >= len(d.plan.Legs) {
			return fmt.Errorf("from_pull_group: leg %d is not in the plan (legs 0-%d)", fromLeg, len(d.plan.Legs)-1)
		}
		if n := len(d.plan.Legs[fromLeg].PullGroups); g < 0 || g >= n {
			return fmt.Errorf("from_pull_group %d: leg %d (%s) has %d pull groups (from_pull_group 0-%d; dungeon_status numbers them 1-%d)", g, fromLeg, d.plan.Legs[fromLeg].Name, n, n-1, n)
		}
	}
	if d.opt.Pulltest != nil {
		return nil
	}
	return d.plan.SkippedNeeds(fromLeg, d.opt.HaveItems)
}

// checkStartSafety is the unsafe-start gate (#193, zf-g9-6cd0b13: a from_leg 8 start in a fresh instance gathered the
// party 21 yd from a live pack and it died there twice). A from_leg, from_pull_group or pulltest start whose start point
// is under gatherSafe yards from a pack or boss of a step the run skips is refused when the instance is known fresh
// (freshStart: every skipped pack is alive), unless force_start. Otherwise it only warns in the report's notes: the
// party may have fought its way there. Run calls it before the prepare step, the bind and the enter phase, so a refused
// start has sent nothing: no teleport, no unbind, no bind, no instance entry, no loot change, no GM line. The point is
// the one startAnywhere moves the party to: the gather spot on the same navmesh path (planPath, after markAvoid), or the
// leg's start (LegStart: where the boss of the leg before it stands, when it has one). start_here moves nobody, so it
// is not gated.
func (d *RemoteDriver) checkStartSafety(ctx context.Context, fromLeg int) error {
	var (
		at   Point
		what string
		near earlierPack
	)
	switch {
	case d.opt.Pulltest != nil:
		spec := d.opt.Pulltest.Normalized()
		if spec.Validate(d.plan) != nil {
			return nil // runPulltest refuses it
		}
		at, _ = d.plan.GatherSpot(spec.Leg, spec.PullGroup, d.planPath(ctx))
		what = fmt.Sprintf("pulltest: the gather spot %.0f %.0f %.0f", at.X, at.Y, at.Z)
		near = d.plan.nearestEarlier(spec.Leg, spec.PullGroup, at)
	case d.opt.StartHere:
		return nil
	case d.opt.FromPullGroup != nil:
		at, _ = d.plan.GatherSpot(fromLeg, d.startGroup(), d.planPath(ctx))
		what = fmt.Sprintf("from_pull_group %d: the gather spot %.0f %.0f %.0f", d.startGroup(), at.X, at.Y, at.Z)
		near = d.plan.nearestEarlier(fromLeg, d.startGroup(), at)
	case fromLeg > 0:
		at, _ = d.plan.LegStart(fromLeg)
		what = fmt.Sprintf("from_leg %d: the start point %.0f %.0f %.0f", fromLeg, at.X, at.Y, at.Z)
		near = d.plan.nearestEarlier(fromLeg, 0, at)
	default:
		return nil
	}
	if near.dist >= gatherSafe {
		return nil
	}
	msg := fmt.Sprintf("%s is %.0f yd from %s (a safe spot needs %.0f)", what, near.dist, near, gatherSafe)
	if !d.freshStart() {
		d.rep.note("start: warning: " + msg + "; the instance is not known fresh, so it may be dead: starting there")
		return nil
	}
	if d.opt.ForceStart {
		d.rep.note("start: warning: " + msg + ", alive in a fresh instance; force_start: starting there anyway")
		return nil
	}
	if near.group < 0 && d.opt.FromPullGroup == nil && d.opt.Pulltest == nil && near.leg == fromLeg-1 {
		// The usual case of a fresh from_leg: LegStart is where that boss stands.
		return fmt.Errorf("from_leg %d: the boss of leg %d is alive at the start point %.0f %.0f %.0f in a fresh instance (entry %d, %.0f yd away; a safe spot needs %.0f). Start at a pull group of leg %d (from_pull_group), or in a cleared or kept instance (prepare.fresh false, or keep_on_fail and resume), or pass force_start to start there anyway",
			fromLeg, near.leg, at.X, at.Y, at.Z, near.entry, near.dist, gatherSafe, fromLeg)
	}
	return fmt.Errorf("%s, alive in a fresh instance: pass force_start to start there anyway", msg)
}

// freshStart says whether the run knows its instance is fresh, so that every pack and boss of a skipped step is alive at
// the start. Fresh: prepare.fresh (the bots' instances are unbound before the bind), or a hired party (prepare.party
// "hire", which Validate refuses together with fresh because a new hire has no saved instance). Not fresh: a resume
// (cfg.Resume), and a hire without rehire that takes over the binding an earlier run kept (HireMemory: keep_on_fail or
// a pause left the party in that run's instance). A hire that reuses a matching hire outside a kept binding still counts
// as fresh: that bot may hold a saved instance, but which hires are reused is known only at the hire step, after this
// gate, and a wrong refusal costs a force_start where a missed one cost zf-g9-6cd0b13 two wipes.
func (d *RemoteDriver) freshStart() bool {
	if d.cfg.Resume {
		return false
	}
	if d.opt.Prepare.Fresh {
		return true
	}
	if !d.opt.Prepare.HiredParty() {
		return false
	}
	rehire := d.opt.Prepare.Hire != nil && d.opt.Prepare.Hire.Rehire
	return rehire || d.opt.HireMemory == nil
}

// SkippedNeeds fails when a step of the legs from `from` on needs an item that only a leg before `from` produces (a boss's loot_item, a
// loot_object's or a use_item's expect_item): those legs are skipped, nothing of theirs happened, and the step would wait out its timeout
// for the item (Uldaman leg 2's keystone needs the Staff of Prehistoria that leg 1's use_item makes, then a 60 s wait). haveItems are
// entries the caller says the party already carries; an item no leg of the plan produces is not checked.
func (p *Plan) SkippedNeeds(from int, haveItems []uint32) error {
	if from <= 0 || from > len(p.Legs) {
		return nil
	}
	made := func(li int, e uint32) bool {
		l := p.Legs[li]
		if l.Boss != nil && l.Boss.LootItem != nil && l.Boss.LootItem.Entry == e {
			return true
		}
		for _, st := range append(slices.Clone(l.Steps), l.AfterBoss...) {
			switch {
			case st.LootObject != nil && st.LootObject.ExpectItem != nil && st.LootObject.ExpectItem.Entry == e:
				return true
			case st.UseItem != nil && st.UseItem.ExpectItem != nil && st.UseItem.ExpectItem.Entry == e:
				return true
			}
		}
		return false
	}
	for li := from; li < len(p.Legs); li++ {
		l := p.Legs[li]
		if l.Optional != nil {
			continue // run only when its item is in the bags (Leg.Optional); a start without it skips the leg
		}
		for _, group := range [][]Step{l.Steps, l.AfterBoss} {
			for si, st := range group {
				var needs []ItemRef
				switch {
				case st.UseObject != nil && st.UseObject.RequiresItem != nil:
					needs = append(needs, *st.UseObject.RequiresItem)
				case st.UseItem != nil:
					needs = append(needs, ItemRef{Entry: st.UseItem.Entry, Name: st.UseItem.Name})
					if st.UseItem.RequiresItem != nil {
						needs = append(needs, *st.UseItem.RequiresItem)
					}
				}
			item:
				for _, n := range needs {
					if slices.Contains(haveItems, n.Entry) {
						continue
					}
					// Made in a leg that runs (from `from` up to this one) before the step: fine. The same leg's boss or earlier steps count;
					// a producer later in the same leg or after it does not make this step's item, but the plan's own order is its author's.
					for lj := from; lj <= li; lj++ {
						if made(lj, n.Entry) {
							continue item
						}
					}
					for lj := 0; lj < from; lj++ {
						if made(lj, n.Entry) {
							return fmt.Errorf("leg %d%s step %d needs %s from skipped leg %d%s: nothing before it in this run makes it. Start at leg %d, or pass have_items [%d] if the party already carries it",
								li, nameSuffix(l.Name), si, n, lj, nameSuffix(p.Legs[lj].Name), lj, n.Entry)
						}
					}
				}
			}
		}
	}
	return nil
}

// planPath is the path function of GatherSpot over the game's planner, if it has one.
func (d *RemoteDriver) planPath(ctx context.Context) func(from, to Point) []Point {
	pl, ok := d.g.(PathPlanner)
	if !ok {
		return nil
	}
	return func(from, to Point) []Point {
		pts, err := pl.PlanPath(ctx, d.plan.Map, from, to)
		if err != nil {
			return nil
		}
		return pts
	}
}

// startPoint is the point the run starts at and what it is.
func (d *RemoteDriver) startPoint(ctx context.Context, fromLeg int) (Point, string, string) {
	if g := d.startGroup(); g >= 0 {
		at, warn := d.plan.GatherSpot(fromLeg, g, d.planPath(ctx))
		return at, StartGather, warn
	}
	at, kind := d.plan.LegStart(fromLeg)
	return at, kind, ""
}

// startNote records a step of the start phase as a note and a dungeon_step event.
func (d *RemoteDriver) startNote(text string) {
	prefix, action := "start: ", "start"
	if d.gmCrossing {
		prefix, action = "gm_cross: ", "gm_cross"
	}
	d.rep.note(prefix + text)
	d.emit(events.KindDungeonStep, prefix+text, map[string]any{"action": action, "ok": true, "mode": "remote"})
}

// startGM books a GM line of the start phase: a setup shortcut, a GM command of the run, and a line of the start report.
func (d *RemoteDriver) startGM(line string) {
	crossing := d.gmCrossing
	d.rep.update(func(r *Report) {
		if crossing {
			r.SetupShortcuts = append(r.SetupShortcuts, line+" (GM crossing)")
			return
		}
		r.SetupShortcuts = append(r.SetupShortcuts, line+" (GM start point)")
		if r.Start != nil {
			r.Start.GMCommands = append(r.Start.GMCommands, line)
		}
	})
	d.remote(func(r *RemoteReport) { r.GMCommands++ })
	d.tr.Event("gm_shortcut", map[string]any{"kind": "setup", "detail": line})
}

func (d *RemoteDriver) startReport(f func(*StartReport)) {
	if d.gmCrossing {
		return // a crossing is no start: the start block is the run's own
	}
	d.rep.update(func(r *Report) {
		if r.Start != nil {
			f(r.Start)
		}
	})
}

// skippedLegs puts the legs before the start in the report as skipped, and returns their texts.
func (d *RemoteDriver) skippedLegs(fromLeg int) []string {
	var out []string
	for i := 0; i < fromLeg && i < len(d.plan.Legs); i++ {
		name := d.plan.Legs[i].Name
		text := fmt.Sprintf("leg %d%s: skipped (from_leg)", i, nameSuffix(name))
		out = append(out, text)
		d.rep.update(func(r *Report) {
			r.Legs = append(r.Legs, LegReport{Leg: i, Name: name, Started: d.clk.Now(), Result: SkippedFromLeg})
		})
	}
	if g := d.startGroup(); g > 0 {
		out = append(out, fmt.Sprintf("leg %d%s: pull groups 0-%d skipped (from_pull_group)", fromLeg, nameSuffix(d.plan.Legs[fromLeg].Name), g-1))
	}
	if g := d.startGroup(); g >= 0 {
		out = append(out, fmt.Sprintf("leg %d%s: the walk to its To skipped (from_pull_group)", fromLeg, nameSuffix(d.plan.Legs[fromLeg].Name)))
	}
	return out
}

func nameSuffix(name string) string {
	if name == "" {
		return ""
	}
	return " (" + name + ")"
}

// SkippedFromLeg is the Result of a leg before the start.
const SkippedFromLeg = "skipped (from_leg)"

// startAnywhere brings the party to the start point (see the top of this file) and fills the report's start block. It sends nothing
// before it has checked that the commander may: a player's account fails the run here, with no command sent.
func (d *RemoteDriver) startAnywhere(ctx context.Context, fromLeg int) error {
	if !d.startNeeded(fromLeg) {
		return nil
	}
	if err := d.checkStart(fromLeg); err != nil {
		return err
	}
	began := d.clk.Now()
	at, kind, warn := d.startPoint(ctx, fromLeg)
	sr := &StartReport{Leg: fromLeg, LegName: d.plan.Legs[fromLeg].Name, Kind: kind, Point: at, Map: d.plan.Map, Method: "gm", GMCommands: []string{}, Skipped: []string{}, Warning: warn}
	if g := d.startGroup(); g >= 0 {
		sr.PullGroup = &g
	}
	sr.Skipped = d.skippedLegs(fromLeg)
	d.rep.update(func(r *Report) { r.Start = sr.clone() })
	what := fmt.Sprintf("leg %d%s start", fromLeg, nameSuffix(sr.LegName))
	if g := d.startGroup(); g >= 0 {
		what = fmt.Sprintf("leg %d%s pull group %d (gather spot)", fromLeg, nameSuffix(sr.LegName), g)
	}
	d.startNote(fmt.Sprintf("%s at %.1f %.1f %.1f on map %d; %s", what, at.X, at.Y, at.Z, d.plan.Map, strings.Join(sr.Skipped, "; ")))
	if warn != "" {
		d.rep.note("start: warning: " + warn)
	}
	if d.opt.StartHere {
		d.startReport(func(s *StartReport) { s.Method, s.Seconds = "here", d.clk.Now().Sub(began).Seconds() })
		d.startNote("start_here: no move, the run starts where the lead bot stands; the legs before it are skipped, not counted as cleared")
		return nil
	}
	if d.partyAt(at) {
		d.startReport(func(s *StartReport) { s.Method, s.Seconds = "already there", d.clk.Now().Sub(began).Seconds() })
		d.startNote("the party is at the start point already: no GM move")
		return nil
	}
	if d.startGroup() < 0 && d.partyInLeg(fromLeg) {
		// A resume into a leg whose party already stands in the leg's own ground (the Zum'rah room after the trigger, 1.9 min lost live when
		// it was sent back to the courtyard at the previous leg's end): it stays, and the leg starts from where it is.
		d.startReport(func(s *StartReport) { s.Method, s.Seconds = "already in the leg", d.clk.Now().Sub(began).Seconds() })
		d.startNote(fmt.Sprintf("the party is in leg %d's own ground already (within %.0f yd of its points): no GM move", fromLeg, legRoomYd))
		return nil
	}
	h, ok := d.g.(StartHands)
	if !ok {
		return fmt.Errorf("start at %s: this game cannot move the commander's own character (no .go xyz)", what)
	}
	level, known, err := h.AccountLevel(ctx)
	if err != nil {
		return fmt.Errorf("start at %s: the account level could not be read: %w", what, err)
	}
	if !known || !capsFor(level).Teleport {
		return fmt.Errorf("start at %s needs a GM commander (account level %d, .go xyz); nothing was sent. Start from leg 0 and walk, or use a GM account", what, level)
	}
	if err := d.commanderTo(ctx, h, at); err != nil {
		return fmt.Errorf("start at %s: %w", what, err)
	}
	if err := d.bringLead(ctx, h, at); err != nil {
		return fmt.Errorf("start at %s: %w", what, err)
	}
	if err := d.gatherParty(ctx, at); err != nil {
		return fmt.Errorf("start at %s: %w", what, err)
	}
	d.startReport(func(s *StartReport) { s.Seconds = d.clk.Now().Sub(began).Seconds() })
	d.startNote(fmt.Sprintf("the party is at %.1f %.1f %.1f (%.0f s, %d GM lines)", at.X, at.Y, at.Z, d.clk.Now().Sub(began).Seconds(), len(d.rep.snapshot().Start.GMCommands)))
	return nil
}

// partyAt says whether the lead bot is within startNear of at and every living bot within startTogether of the lead bot.
func (d *RemoteDriver) partyAt(at Point) bool {
	d.look()
	lead, ok := d.leadPos()
	if !ok || dist3(lead, at) > startNear {
		return false
	}
	return len(d.apart(lead)) == 0
}

// legRoomYd is how near one of a leg's points (its To, via points, step stands and objects, boss) the lead bot must stand for the party to count as
// in that leg's ground.
const legRoomYd = 60.0

// partyInLeg says whether the lead bot stands within legRoomYd of one of the leg's points and every living bot is with it.
func (d *RemoteDriver) partyInLeg(leg int) bool {
	d.look()
	lead, ok := d.leadPos()
	if !ok || leg < 0 || leg >= len(d.plan.Legs) {
		return false
	}
	l := d.plan.Legs[leg]
	pts := []Point{l.To}
	for _, w := range l.Via {
		pts = append(pts, w.Point)
	}
	for _, steps := range [][]Step{l.Steps, l.AfterBoss} {
		for _, st := range steps {
			switch {
			case st.LootObject != nil && st.LootObject.Stand != nil:
				pts = append(pts, *st.LootObject.Stand)
			case st.UseObject != nil && st.UseObject.Stand != nil:
				pts = append(pts, *st.UseObject.Stand)
			case st.AreaTrigger != nil:
				pts = append(pts, st.AreaTrigger.Stand)
			}
		}
	}
	for _, g := range l.PullGroups {
		pts = append(pts, g.Positions...) // a party mid-leg stands by one of its packs, not by its `to`
	}
	if l.Boss != nil {
		pts = append(pts, l.Boss.Position)
	}
	near := false
	for _, p := range pts {
		if dist3(lead, p) <= legRoomYd {
			near = true
		}
	}
	// A resume (Config.Resume) takes the party for inside the leg when it stands within ResumeMaxOff of the leg's route, as the stepper's
	// ResumeAt does: rfc-live-2 resumed leg 4 with the party 68 yd from the leg's `to` and in the middle of it, and the start sent it back
	// to the leg's first point (127 s, a new summon) before the stepper could skip anything.
	if !near && d.cfg.Resume {
		route := []Point{legStart(d.plan, leg), l.To}
		for _, g := range l.PullGroups {
			if len(g.Positions) > 0 {
				route = append(route, g.Positions[0])
			}
		}
		if l.Boss != nil {
			route = append(route, l.Boss.Position)
		}
		for i := 0; i+1 < len(route); i++ {
			if dd, _ := segDist(lead, route[i], route[i+1]); dd <= ResumeMaxOff {
				near = true
				break
			}
		}
	}
	return near && len(d.apart(lead)) == 0
}

// apart names the living members of the feed that stand more than startTogether yards from p (or on another map).
func (d *RemoteDriver) apart(p Point) []string {
	var out []string
	for _, m := range d.fr.Party {
		if m.Dead {
			continue
		}
		if m.Map != d.plan.Map || dist3(partyPoint(m), p) > startTogether {
			out = append(out, m.Name)
		}
	}
	return out
}

// startWait is the ceiling of each wait of the start phase.
func (d *RemoteDriver) startWait() time.Duration { return d.opt.StartWait }

// commanderTo puts the agent's own character at the start point: `.appear <lead bot>` first when it is not on the map near the lead bot,
// then `.go xyz`. Each wait is bounded by startWait.
func (d *RemoteDriver) commanderTo(ctx context.Context, h StartHands, at Point) error {
	d.look()
	lead, haveLead := d.leadPos()
	here, m, in := h.AgentPlace()
	if !in {
		return errors.New("the agent's character is not in the world")
	}
	if m != d.plan.Map || !haveLead || dist3(here, lead) > keepNearFar {
		if err := h.Appear(ctx, d.opt.Lead); err != nil {
			return fmt.Errorf(".appear %s: %w", d.opt.Lead, err)
		}
		d.startGM(".appear " + d.opt.Lead)
		d.startNote(".appear " + d.opt.Lead + " (the commander enters the lead bot's instance)")
		deadline := d.clk.Now().Add(d.startWait())
		for {
			if err := d.tick(ctx, d.cfg.Poll); err != nil {
				return err
			}
			if p, mm, ok := h.AgentPlace(); ok && mm == d.plan.Map {
				if l, ok := d.leadPos(); ok && dist3(p, l) <= 2*keepNearFar {
					break
				}
			}
			if d.clk.Now().After(deadline) {
				return fmt.Errorf("the commander is not in the lead bot's instance %.0f s after .appear %s", d.startWait().Seconds(), d.opt.Lead)
			}
		}
	}
	if err := h.GoXYZ(ctx, d.plan.Map, at); err != nil {
		return fmt.Errorf(".go xyz: %w", err)
	}
	d.startGM(fmt.Sprintf(".go xyz %.2f %.2f %.2f %d", at.X, at.Y, at.Z, d.plan.Map))
	d.startNote(fmt.Sprintf(".go xyz %.1f %.1f %.1f %d", at.X, at.Y, at.Z, d.plan.Map))
	if p, mm, ok := h.AgentPlace(); !ok || mm != d.plan.Map || hdist(p, at) > startNear {
		return fmt.Errorf("the commander is not at the start point after .go xyz (at %.0f %.0f on map %d)", p.X, p.Y, mm)
	}
	return d.checkStartZ(ctx, h, at)
}

// bringLead summons the lead bot to the commander with `.bot summon <lead bot>`, a player command of the commander's own account (the
// point `cmd summon` fetches the others to is the lead bot, so it comes first). Up to three tries, each waiting up to a third of startWait.
func (d *RemoteDriver) bringLead(ctx context.Context, h StartHands, at Point) error {
	d.look()
	if lead, ok := d.leadPos(); ok && dist3(lead, at) <= startNear {
		return nil // the lead bot is there already: only the others come
	}
	bl, ok := d.g.(BotLiner)
	if !ok {
		return errors.New("this game cannot send .bot summon: the lead bot cannot be brought to the commander")
	}
	var last string
	for try := 1; try <= 3; try++ {
		lines, _, err := bl.BotLines(ctx, "summon "+d.opt.Lead, 5*time.Second, func(line string) bool { return line != "" })
		if err != nil {
			return fmt.Errorf(".bot summon %s: %w", d.opt.Lead, err)
		}
		last = strings.Join(lines, " / ")
		d.startNote(fmt.Sprintf(".bot summon %s (try %d): %s", d.opt.Lead, try, last))
		deadline := d.clk.Now().Add(d.startWait() / 3)
		for {
			if err := d.tick(ctx, d.cfg.Poll); err != nil {
				return err
			}
			if p, _, ok := h.AgentPlace(); ok {
				if lead, ok := d.leadPos(); ok && hdist(lead, p) <= startSettle {
					d.startReport(func(s *StartReport) { s.LeadSummoned = true })
					return d.holdLead(ctx)
				}
			}
			if d.clk.Now().After(deadline) {
				break
			}
		}
		if err := d.tick(ctx, d.opt.SummonGap); err != nil {
			return err
		}
	}
	return fmt.Errorf("the lead bot %s did not come to the commander after three .bot summon (last answer: %s)", d.opt.Lead, last)
}

// holdLead stops the lead bot following the commander. `.bot summon` makes the commander the bot's master and puts the bot on follow
// (TortoiseBots HandleSummon, PlayerConvenience: SetBotFollow), so the tank would chase the GM instead of holding the start point. A
// `cmd move` to the point it landed on is the module's hold-this-spot order (the same one a walk ends with); `cmd stay` is the fallback. The
// feed then has to show the lead bot within holdSlack yards of that point for holdWatch: up to three orders, then the run fails.
func (d *RemoteDriver) holdLead(ctx context.Context) error {
	d.look()
	landed, ok := d.leadPos()
	if !ok {
		return errors.New("the lead bot is not in the feed after .bot summon")
	}
	var last string
	for try := 1; try <= 3; try++ {
		verb, args := "move", moveArgs(landed)
		if try == 3 {
			verb, args = "stay", nil
		}
		r, err := d.order(ctx, verb, args...)
		if err != nil {
			return err
		}
		if ref, bad := refusedOf(r); bad {
			last = fmt.Sprintf("cmd %s was refused: %s (%s)", verb, ref.Code, ref.Message)
			if err := d.tick(ctx, d.opt.SummonGap); err != nil {
				return err
			}
			continue
		}
		held := true
		for end := d.clk.Now().Add(holdWatch); d.clk.Now().Before(end); {
			if err := d.tick(ctx, d.cfg.Poll); err != nil {
				return err
			}
			if p, ok := d.leadPos(); ok && dist3(p, landed) > holdSlack {
				held, last = false, fmt.Sprintf("after cmd %s the lead bot was %.0f yd from where it landed", verb, dist3(p, landed))
				landed = p
				break
			}
		}
		if held {
			d.startNote(fmt.Sprintf("the lead bot holds at %.1f %.1f %.1f (cmd %s), not following the commander", landed.X, landed.Y, landed.Z, verb))
			return nil
		}
	}
	return fmt.Errorf("the lead bot %s does not hold still after .bot summon (it follows the commander): %s", d.opt.Lead, last)
}

// gatherParty fetches every living bot that is not with the lead bot with `cmd summon <bot>` and waits for all to stand together. A bot
// still apart after startWait is summoned once more and waited for another startWait: two waits, then the run fails naming it.
func (d *RemoteDriver) gatherParty(ctx context.Context, at Point) error {
	for round := 1; round <= 2; round++ {
		d.look()
		lead, ok := d.leadPos()
		if !ok {
			return errors.New("the lead bot is not in the feed")
		}
		missing := d.apart(lead)
		if len(missing) == 0 {
			return nil
		}
		for i, name := range missing {
			if strings.EqualFold(name, d.opt.Lead) {
				continue
			}
			if i > 0 {
				if err := d.tick(ctx, d.opt.SummonGap); err != nil {
					return err
				}
			}
			if err := d.startSummon(ctx, name); err != nil {
				return err
			}
			d.startReport(func(s *StartReport) { s.Summoned = append(s.Summoned, name) })
		}
		deadline := d.clk.Now().Add(d.startWait())
		for {
			if err := d.tick(ctx, d.cfg.Poll); err != nil {
				return err
			}
			if lead, ok := d.leadPos(); ok && len(d.apart(lead)) == 0 {
				return nil
			}
			if d.clk.Now().After(deadline) {
				break
			}
		}
	}
	lead, _ := d.leadPos()
	return fmt.Errorf("the party did not gather at the lead bot: %s still apart after two summons", strings.Join(d.apart(lead), ", "))
}

// startSummon sends `cmd summon <bot>`, again after SummonGap when the module is busy; three tries.
func (d *RemoteDriver) startSummon(ctx context.Context, name string) error {
	var last string
	for try := 0; try < 3; try++ {
		if try > 0 {
			if err := d.tick(ctx, d.opt.SummonGap); err != nil {
				return err
			}
		}
		r, err := d.order(ctx, "summon", name)
		if err != nil {
			return err
		}
		ref, bad := refusedOf(r)
		if !bad {
			d.startNote("cmd summon " + name)
			return nil
		}
		last = ref.Code + ": " + ref.Message
		if slices.Contains([]string{"denied", "no-bot", "invalid"}, ref.Code) {
			break
		}
	}
	return fmt.Errorf("cmd summon %s was refused (%s)", name, last)
}
