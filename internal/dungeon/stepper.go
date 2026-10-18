package dungeon

import (
	"fmt"
	"math"
	"strings"
	"time"

	"github.com/pjerra/turtle-agent/internal/objects"
	"github.com/pjerra/turtle-agent/internal/packet"
)

// World is what the Stepper needs to see of the game. The driver implements it over a session; tests use a fake.
type World interface {
	Position() Point
	// GameObjects lists the game objects in view, with the State and Distance a gameobjects row carries.
	GameObjects() []objects.GameObject
	// HostilesWithin counts the hostile, living units within radius yards of center.
	HostilesWithin(center Point, radius float32) int
}

// UnitWorld is the optional part of a World that tells whether a unit wait holds (wait_for unit, the expect of an area_trigger): what the
// bots' feed lists and what the agent's own object store holds. A World without it cannot do those steps; the Stepper fails them with that
// reason instead of waiting out their timeout.
type UnitWorld interface {
	UnitHolds(u *UnitWait) bool
}

// EntryWorld is the optional part of a World whose object steps are done by entry: the carrier's order names the object by its entry
// and the plan's point (`cmd open entry:<id>@x,y,z`), and a game-object wait asks `cmd state`, so the agent need not see the object. With
// it a use_object or loot_object step whose object has an entry is returned as an action at once (no walk to bring the object into view;
// the driver's approach does the walking), and a game-object wait with an entry never holds on the agent's own copy: the driver polls.
type EntryWorld interface {
	EntryTargets() bool
}

// PartyWorld is the optional part of a World that knows where the party stands: ResumeAt skips a pull group only when a member
// stands near it (and sees it empty), because a World lists only the hostiles that someone sees.
type PartyWorld interface {
	// PartyNear says whether the leader or a party member stands within radius yards of center.
	PartyNear(center Point, radius float32) bool
}

// Distances the Stepper works with, in yards.
const (
	ArriveRadius = 3.0 // a Walk is skipped when the player already stands this close
	UseRange     = 4.5 // the core ignores a use out of 5 yards (see the README, use_object)
)

// Bounds on one step, so a door that cannot be reached or a walk that never gets close ends the plan instead of
// looping: a use_object step fails after MaxStepWalks walks (each brings the player next to the object, or to its
// stand point) or MaxStepSeconds since it began, whichever comes first. A wait has its own timeout_s.
const (
	MaxStepWalks   = 4
	MaxStepSeconds = 120
)

// ActionKind says what the driver is asked to do.
type ActionKind string

const (
	ActWalk    ActionKind = "walk"         // walk the navmesh to To
	ActPull    ActionKind = "pull"         // pull and kill the group in Entries/Positions
	ActUse     ActionKind = "use_object"   // use game object GUID (it is within UseRange)
	ActLoot    ActionKind = "loot_object"  // open and loot game object GUID (it is within UseRange)
	ActItem    ActionKind = "use_item"     // use the item Item from the bags
	ActRitual  ActionKind = "ritual"       // Participants bots use the summoning ritual Entry at EntryAt together (remote runs)
	ActTrigger ActionKind = "area_trigger" // take the area trigger Trigger (the lead bot walks in, or the agent's own character sends it)
	ActGossip  ActionKind = "gossip"       // talk to Gossip.NPC and pick its option (Gossip carries who talks)
	ActKill    ActionKind = "kill_boss"    // kill Boss, First entries before it
	ActWait    ActionKind = "wait"         // nothing to do yet: look again with Next
	ActDone    ActionKind = "done"         // the plan is finished
	ActFailed  ActionKind = "failed"       // the plan cannot go on: Err says why
)

// Action is the Stepper's current request. Leg and Step locate it in the plan (Step is -1 outside the steps).
type Action struct {
	Kind  ActionKind
	Leg   int
	Step  int
	Label string // for logs: "leg 2 (ironaya): use_object 124371"

	To        Point               // walk target
	Direct    bool                // walk: a straight-line step (`cmd move ... direct`), not a navmesh path (Waypoint.Direct)
	GMCross   bool                // walk: a GM moves the party to To (Waypoint.GMCross); remote runs only
	Exact     bool                // walk: the waypoint is Exact (Waypoint.Exact): its point must be reached tightly (remote.go tightenTo)
	Entries   []uint32            // pull: the group's entries
	Positions []Point             // pull: where its members stand
	Ambush    bool                // pull: the group is stealthed (PullGroup.Ambush)
	Radius    float32             // pull: PullGroup.Radius, 0 for the default
	Patrol    *Patrol             // pull: PullGroup.Patrol, nil when no patrol comes by
	Skip      []uint32            // pull and boss: entries not to wait for
	Boss      *Boss               // boss
	Object    *objects.GameObject // use
	GUID      packet.GUID         // use (0 when the agent does not see the object: an entry-form action, see Entry)
	// Entry and EntryAt are set when the World names objects by entry (EntryWorld): the order says `entry:<Entry>@EntryAt` and the server
	// finds the object, so Object and GUID are only the agent's own copy, if it sees one (the fallback when the server finds nothing).
	Entry     uint32
	EntryAt   *Point
	SpawnGUID packet.GUID // use_object, loot_object: the exact spawn (ObjectRef.Spawn as a GUID); 0 when the plan gives none
	// Batch is the loot_object step's batch id (LootObject.Batch; "" when it has none) and Section the chests from this step on of the run
	// of consecutive loot_object steps that have a batch, so that the driver can group them into batches of the run's own size.
	Batch        string
	Section      []BatchChest
	Trigger      *AreaTrigger  // area_trigger: the step
	Participants int           // ritual: the number of bots that use the object
	ViaItem      bool          // use: the Requires item is used on the object (UseObject.ViaItem)
	Gossip       *Gossip       // gossip: the step
	Requires     *ItemRef      // use, use_item: the item a non-GM leader needs to carry (UseObject.RequiresItem, UseItem.RequiresItem)
	Item         *ItemRef      // use_item: the item to use
	Expect       *ItemRef      // loot_object, use_item: the item that must be in the bags afterwards
	Remaining    time.Duration // wait: time left before the plan fails
	Err          string        // failed
	// TeamGate is set on a pull or kill of a part gated to the other team that a World with TeamGateLook runs only after a look: the
	// driver looks at the part's positions and skips it only if none of its mobs is there (or hostile). It reads like the skip note,
	// "pull group 1 of 1 ([1]) is for team horde, the leader is alliance".
	TeamGate string
}

type phase int

const (
	phWalk phase = iota
	phPulls
	phSteps
	phBoss
	phAfter // the steps after the boss (Leg.AfterBoss)
	phDone
	phFailed
)

// Stepper walks a Plan: Next returns what to do now and Complete or Fail report the result of the last action.
// It never blocks and sends nothing; all it knows of the game is the World. Not safe for concurrent use.
type Stepper struct {
	plan *Plan
	w    World
	now  func() time.Time

	leg       int
	ph        phase
	idx       int        // pull group or step index; in the walk phase, the next via waypoint (len(Via): the leg's To)
	since     time.Time  // when the current wait started; zero when not waiting
	stepAt    time.Time  // when the current step was first looked at; zero before
	stepWalks int        // walks Complete has seen in the current step
	last      ActionKind // the kind of the action Next returned last
	err       string
	skipped   []string        // what the team gate passed over since Skipped was last called
	teamSeen  map[string]bool // team-gated parts already noted as hostile in view (a note once per part)
}

// NewStepper starts at the first leg. now may be nil (time.Now).
func NewStepper(p *Plan, w World, now func() time.Time) *Stepper {
	if now == nil {
		now = time.Now
	}
	s := &Stepper{plan: p, w: w, now: now}
	s.enterLeg(0)
	return s
}

// NewStepperAt is NewStepper starting at leg (0 <= leg <= len(plan.Legs); the last is a plan that is done).
func NewStepperAt(p *Plan, w World, now func() time.Time, leg int) *Stepper {
	s := NewStepper(p, w, now)
	s.enterLeg(max(0, min(leg, len(p.Legs))))
	return s
}

// NewStepperAtGroup starts inside leg: at its pull group `group` (0 <= group < len(PullGroups)), with the walk to the leg's To and the
// pull groups before `group` skipped. The caller has brought the party to the group's gather spot and reports what was skipped.
func NewStepperAtGroup(p *Plan, w World, now func() time.Time, leg, group int) *Stepper {
	s := NewStepperAt(p, w, now, leg)
	if leg >= 0 && leg < len(p.Legs) && group >= 0 && group < len(p.Legs[leg].PullGroups) {
		s.ph, s.idx = phPulls, group
	}
	return s
}

func (s *Stepper) enterLeg(i int) {
	s.leg, s.idx, s.since = i, 0, time.Time{}
	s.stepAt, s.stepWalks = time.Time{}, 0
	if i >= len(s.plan.Legs) {
		s.ph = phDone
		return
	}
	s.ph = phWalk
}

// RestartLeg begins the current leg again from its first point: for a leg whose plan was reloaded before anything of it was done.
func (s *Stepper) RestartLeg() { s.enterLeg(s.leg) }

// SkipLeg gives up the current leg and goes on at the next one: the driver's way past an optional leg whose item is missing. It does nothing
// on a plan that is done or failed.
func (s *Stepper) SkipLeg() {
	if s.ph != phDone && s.ph != phFailed {
		s.enterLeg(s.leg + 1)
	}
}

// Position reports where the stepper is, for logs and the driver's status: the leg, the phase name and the index in it.
func (s *Stepper) Position() (leg int, phase string, index int) {
	return s.leg, [...]string{"walk", "pull_groups", "steps", "boss", "after_boss", "done", "failed"}[s.ph], s.idx
}

// Complete reports that the action Next last returned (walk, pull, use_object or kill_boss) is done. After a walk
// that a use_object step asked for, the step is looked at again; after a wait it does nothing.
func (s *Stepper) Complete() {
	switch s.ph {
	case phWalk:
		if s.idx < len(s.plan.Legs[s.leg].Via) {
			s.idx++ // a via waypoint is done; the next one, or To, follows
			break
		}
		s.ph, s.idx = phPulls, 0
	case phPulls:
		s.idx++
	case phSteps, phAfter:
		// A walk inside a step only brings the player to the object: the step itself is still to do.
		switch s.last {
		case ActUse, ActLoot, ActItem, ActRitual, ActTrigger, ActGossip:
			s.nextStep()
		case ActWalk:
			s.stepWalks++
		}
	case phBoss:
		s.ph, s.idx = phAfter, 0
	}
}

// SkipStep gives up the step the stepper is on and goes to the next: the driver's way past a wait it cannot check (a game object the
// agent does not see). It does nothing outside a leg's steps.
func (s *Stepper) SkipStep() {
	if s.ph == phSteps || s.ph == phAfter {
		s.nextStep()
	}
}

func (s *Stepper) nextStep() {
	s.idx++
	s.since, s.stepAt, s.stepWalks = time.Time{}, time.Time{}, 0
}

// Fail ends the plan: the driver could not do the action (the group wiped it, the path is blocked). Next then keeps
// returning ActFailed with the reason.
func (s *Stepper) Fail(reason string) {
	if s.ph != phDone && s.ph != phFailed {
		s.ph, s.err = phFailed, reason
	}
}

func (s *Stepper) label(format string, args ...any) string {
	l := s.plan.Legs[s.leg]
	name := fmt.Sprintf("leg %d", s.leg)
	if l.Name != "" {
		name += " (" + l.Name + ")"
	}
	msg := fmt.Sprintf(format, args...)
	if s.ph == phAfter { // a step of the list after the boss: its index is that list's
		msg = strings.Replace(msg, "step ", "after_boss step ", 1)
	}
	return name + ": " + msg
}

// Next returns the action to do now. It returns the same action until it is completed, and ActWait while a step
// waits for the world to change; call it again after a moment. Conditions the world already meets are passed
// without an action: a leg whose To the player stands at needs no walk, a wait whose condition holds is over.
func (s *Stepper) Next() Action {
	a := s.next()
	s.last = a.Kind
	return a
}

func (s *Stepper) next() Action {
	for {
		switch s.ph {
		case phDone:
			return Action{Kind: ActDone, Leg: len(s.plan.Legs), Step: -1, Label: "plan " + s.plan.Name + " done"}
		case phFailed:
			return Action{Kind: ActFailed, Leg: s.leg, Step: -1, Err: s.err, Label: "plan " + s.plan.Name + " failed: " + s.err}
		}
		l := &s.plan.Legs[s.leg]
		switch s.ph {
		case phWalk:
			if s.idx < len(l.Via) {
				w := l.Via[s.idx]
				// A waypoint with Exact is always returned, whatever the leader's distance — even standing on it: the remote driver must run its
				// tighten, which is the only place that wants the proof (fresh frames, 2 s of standing at one anchor) before the direct step after
				// this point goes out; the tighten itself skips the movement that is not needed. Every other via point (and the direct step's own
				// point) is passed when the leader stands within ArriveRadius of it.
				if !w.Exact && dist3(s.w.Position(), w.Point) <= ArriveRadius {
					s.idx++
					continue
				}
				kind := "walk"
				if w.Direct {
					kind = "direct step"
				}
				if w.GMCross {
					kind = "gm_cross"
				}
				return Action{Kind: ActWalk, Leg: s.leg, Step: -1, To: w.Point, Direct: w.Direct, GMCross: w.GMCross, Exact: w.Exact, Label: s.label("%s to %.1f %.1f %.1f", kind, w.X, w.Y, w.Z)}
			}
			if dist3(s.w.Position(), l.To) <= ArriveRadius {
				s.Complete()
				continue
			}
			return Action{Kind: ActWalk, Leg: s.leg, Step: -1, To: l.To, Label: s.label("walk to %.1f %.1f %.1f", l.To.X, l.To.Y, l.To.Z)}
		case phPulls:
			if s.idx >= len(l.PullGroups) {
				s.ph, s.idx = phSteps, 0
				continue
			}
			g := l.PullGroups[s.idx]
			gate := ""
			if !s.teamOK(g.Team) && !s.hostileOverride(fmt.Sprintf("g%d/%d", s.leg, s.idx), g.Entries, g.Team, s.label("pull group %d of %d (%v)", s.idx+1, len(l.PullGroups), g.Entries)) {
				note := s.label("pull group %d of %d (%v) is for team %s, the leader is %s", s.idx+1, len(l.PullGroups), g.Entries, g.Team, s.team())
				if !s.looksFirst() {
					s.skipped = append(s.skipped, note+": skipped")
					s.idx++
					continue
				}
				gate = note
			}
			return Action{Kind: ActPull, Leg: s.leg, Step: -1, Entries: g.Entries, Positions: g.Positions, Ambush: g.Ambush, Radius: g.Radius, Patrol: g.Patrol, Skip: l.Skip, TeamGate: gate,
				Label: s.label("pull group %d of %d", s.idx+1, len(l.PullGroups))}
		case phSteps, phAfter:
			steps := l.Steps
			if s.ph == phAfter {
				steps = l.AfterBoss
			}
			if s.idx >= len(steps) {
				if s.ph == phAfter {
					s.enterLeg(s.leg + 1)
				} else {
					s.ph = phBoss
				}
				continue
			}
			a, advance := s.step(&steps[s.idx])
			if advance {
				s.nextStep()
				continue
			}
			if a.Kind == ActLoot {
				a.Batch, a.Section = batchSection(steps[s.idx:])
			}
			return a
		case phBoss:
			if l.Boss == nil {
				s.ph, s.idx = phAfter, 0
				continue
			}
			gate := ""
			if !s.teamOK(l.Boss.Team) && !s.hostileOverride(fmt.Sprintf("b%d", s.leg), append([]uint32{l.Boss.Entry}, l.Boss.First...), l.Boss.Team, s.label("boss %d", l.Boss.Entry)) {
				note := s.label("boss %d is for team %s, the leader is %s", l.Boss.Entry, l.Boss.Team, s.team())
				if !s.looksFirst() {
					s.skipped = append(s.skipped, note+": skipped")
					s.ph, s.idx = phAfter, 0
					continue
				}
				gate = note
			}
			return Action{Kind: ActKill, Leg: s.leg, Step: -1, Boss: l.Boss, Skip: l.Skip, TeamGate: gate, Label: s.label("kill boss %d", l.Boss.Entry)}
		}
	}
}

// step evaluates one step. advance is true when it is already satisfied (a wait that holds). A use_object step
// that would walk once more than MaxStepWalks times, or after MaxStepSeconds, fails instead.
func (s *Stepper) step(st *Step) (a Action, advance bool) {
	if s.stepAt.IsZero() {
		s.stepAt = s.now()
	}
	a, advance = s.stepOnce(st)
	obj, _ := objectOf(st)
	if a.Kind != ActWalk || obj == nil {
		return a, advance
	}
	ref := describeRef(obj)
	if s.stepWalks >= MaxStepWalks {
		s.Fail(s.label("step %d: ", s.idx) + fmt.Sprintf("%s is still out of reach after %d walks", ref, s.stepWalks))
	} else if el := s.now().Sub(s.stepAt); el > MaxStepSeconds*time.Second {
		s.Fail(s.label("step %d: ", s.idx) + fmt.Sprintf("%s is not used after %.0f s", ref, el.Seconds()))
	} else {
		return a, false
	}
	return Action{Kind: ActFailed, Leg: s.leg, Step: s.idx, Err: s.err, Label: s.err}, false
}

func (s *Stepper) stepOnce(st *Step) (a Action, advance bool) {
	base := Action{Leg: s.leg, Step: s.idx}
	fail := func(format string, args ...any) (Action, bool) {
		s.Fail(s.label("step %d: ", s.idx) + fmt.Sprintf(format, args...))
		return Action{Kind: ActFailed, Leg: s.leg, Step: s.idx, Err: s.err, Label: s.err}, false
	}
	switch {
	case st.UseItem != nil:
		u := st.UseItem
		base.Kind, base.Item, base.Requires, base.Expect = ActItem, &ItemRef{Entry: u.Entry, Name: u.Name}, u.RequiresItem, u.ExpectItem
		base.Label = s.label("step %d: use item %s", s.idx, base.Item)
		return base, false
	case st.Gossip != nil:
		g := st.Gossip
		base.Kind, base.Gossip = ActGossip, g
		base.Label = s.label("step %d: gossip with NPC %d (%s, by %s)", s.idx, g.NPC, g.OptionText(), g.Talker())
		return base, false
	case st.Ritual != nil:
		r := st.Ritual
		at := r.Position
		base.Kind, base.Entry, base.EntryAt, base.Participants = ActRitual, r.Entry, &at, r.Participants
		base.Label = s.label("step %d: ritual at game object %d with %d participants", s.idx, r.Entry, r.Participants)
		return base, false
	case st.AreaTrigger != nil:
		t := st.AreaTrigger
		base.Kind, base.Trigger = ActTrigger, t
		base.Label = s.label("step %d: area trigger %d (by %s, expect %s)", s.idx, t.ID, t.By, t.Expect)
		return base, false
	case st.UseObject != nil || st.LootObject != nil:
		ref, stand := objectOf(st)
		kind, verb := ActUse, "use"
		if st.LootObject != nil {
			kind, verb = ActLoot, "loot"
		}
		if ew, isEntry := s.w.(EntryWorld); isEntry && ref.Entry != 0 && ew.EntryTargets() {
			base.Kind, base.Entry = kind, ref.Entry
			if ref.Spawn != 0 {
				base.SpawnGUID = packet.GUID(uint64(packet.HighGameObject)<<48 | uint64(ref.Entry)<<24 | uint64(ref.Spawn))
			}
			switch {
			case ref.Near != nil:
				base.EntryAt = ref.Near
			case stand != nil:
				base.EntryAt = stand
			}
			if o, seen := s.find(ref); seen && o.Distance >= 0 {
				base.Object, base.GUID = &o, o.GUID
				if base.EntryAt == nil {
					base.EntryAt = &Point{o.X, o.Y, o.Z}
				}
			}
			if st.UseObject != nil {
				base.Requires, base.ViaItem = st.UseObject.RequiresItem, st.UseObject.ViaItem
			} else {
				base.Expect = st.LootObject.ExpectItem
			}
			base.Label = s.label("step %d: %s %s", s.idx, verb, describeRef(ref))
			return base, false
		}
		o, ok := s.find(ref)
		if !ok || o.Distance < 0 {
			// Not in view yet: the objects load as the player nears them, so go to where it stands.
			target, have := Point{}, false
			switch {
			case stand != nil:
				target, have = *stand, true
			case ref.Near != nil:
				target, have = *ref.Near, true
			}
			if have && dist3(s.w.Position(), target) > ArriveRadius {
				base.Kind, base.To = ActWalk, target
				base.Label = s.label("step %d: walk to %.1f %.1f %.1f to bring %s into view", s.idx, target.X, target.Y, target.Z, describeRef(ref))
				return base, false
			}
			return fail("%s is not in view", describeRef(ref))
		}
		if stand != nil && dist3(s.w.Position(), *stand) > ArriveRadius && o.Distance > UseRange {
			base.Kind, base.To = ActWalk, *stand
			base.Label = s.label("step %d: walk to the stand point for %s", s.idx, describeRef(ref))
			return base, false
		}
		if o.Distance > UseRange {
			base.Kind, base.To = ActWalk, Point{o.X, o.Y, o.Z}
			base.Label = s.label("step %d: walk next to %s (%.1f yd away)", s.idx, describeRef(ref), o.Distance)
			return base, false
		}
		base.Kind, base.Object, base.GUID = kind, &o, o.GUID
		if st.UseObject != nil {
			base.Requires, base.ViaItem = st.UseObject.RequiresItem, st.UseObject.ViaItem
		} else {
			base.Expect = st.LootObject.ExpectItem
		}
		base.Label = s.label("step %d: %s %s", s.idx, verb, describeRef(ref))
		return base, false
	case st.WaitFor != nil:
		w := st.WaitFor
		if _, ok := s.w.(UnitWorld); w.Unit != nil && !ok {
			return fail("wait for %s: this driver does not see units", describeWait(w))
		}
		if s.since.IsZero() {
			s.since = s.now()
		}
		if s.holds(w) {
			return base, true
		}
		timeout := time.Duration(w.TimeoutS) * time.Second
		left := timeout - s.now().Sub(s.since)
		if left <= 0 {
			if w.FailText != "" {
				return fail("waited %d s for %s: %s", w.TimeoutS, describeWait(w), w.FailText)
			}
			return fail("waited %d s for %s", w.TimeoutS, describeWait(w))
		}
		base.Kind, base.Remaining = ActWait, left
		base.Label = s.label("step %d: wait for %s (%.0f s left)", s.idx, describeWait(w), left.Seconds())
		return base, false
	}
	return fail("is empty")
}

// objectOf is the game object a use_object or loot_object step works on, and where to stand for it; nil for the other steps.
func objectOf(st *Step) (*ObjectRef, *Point) {
	switch {
	case st.UseObject != nil:
		return &st.UseObject.ObjectRef, st.UseObject.Stand
	case st.LootObject != nil:
		return &st.LootObject.ObjectRef, st.LootObject.Stand
	}
	return nil, nil
}

// TeamWorld is the optional part of a World that tells the leader's team ("alliance", "horde", "" when unknown). Without it, or
// when unknown, the team gate (Boss.Team, PullGroup.Team) passes everything.
type TeamWorld interface {
	Team() string
}

func (s *Stepper) team() string {
	if tw, ok := s.w.(TeamWorld); ok {
		return tw.Team()
	}
	return ""
}

// teamOK reports whether something gated to team is for the leader.
func (s *Stepper) teamOK(team string) bool {
	t := s.team()
	return team == "" || t == "" || t == team
}

// HostileWorld is the optional part of a World that says whether a unit of one of entries is hostile to the party: listed hostile in
// the feed now (alive), or seen hostile earlier in the run. The team gate is data-driven with it: a part gated to the other team runs
// anyway when its mobs turn out hostile (the Uldaman Lost Dwarves, "horde-only" in the plan, aggroed on the Alliance party of remote
// run 2). A World without it keeps the plan's team gate as written.
type HostileWorld interface {
	HostileSeen(entries []uint32) bool
}

// TeamGateLook is the optional part of a World that wants the team gate to look before it skips: a part gated to the other team whose
// mobs were never listed hostile is returned as an action with Action.TeamGate set, and the driver walks to its positions to look
// (the feed lists only what the bots see, so "not in the feed" is not "not there"). A World without it skips such a part at once.
type TeamGateLook interface {
	TeamGateLook() bool
}

func (s *Stepper) looksFirst() bool {
	tl, ok := s.w.(TeamGateLook)
	return ok && tl.TeamGateLook()
}

// hostileOverride says whether a part gated to another team is fought anyway because its mobs are hostile to the party, and notes it
// once. key names the part; what is its label for the note.
func (s *Stepper) hostileOverride(key string, entries []uint32, team, what string) bool {
	hw, ok := s.w.(HostileWorld)
	if !ok || !hw.HostileSeen(entries) {
		return false
	}
	if s.teamSeen == nil {
		s.teamSeen = map[string]bool{}
	}
	if !s.teamSeen[key] {
		s.teamSeen[key] = true
		s.skipped = append(s.skipped, fmt.Sprintf("%s is for team %s, the leader is %s, but its mobs are hostile to the party: fought", what, team, s.team()))
	}
	return true
}

// Skipped returns what the team gate passed over since the last call, one line each, for the report.
func (s *Stepper) Skipped() []string {
	out := s.skipped
	s.skipped = nil
	return out
}

func (s *Stepper) holds(w *WaitFor) bool {
	switch {
	case w.GameObject != nil:
		if ew, isEntry := s.w.(EntryWorld); isEntry && w.GameObject.Entry != 0 && ew.EntryTargets() {
			return false // the driver asks the module (cmd state): the agent's copy of the object proves nothing
		}
		o, ok := s.find(&w.GameObject.ObjectRef)
		return ok && o.State == w.GameObject.State
	case w.HostilesGone != nil:
		c := s.w.Position()
		if w.HostilesGone.Center != nil {
			c = *w.HostilesGone.Center
		}
		return s.w.HostilesWithin(c, w.HostilesGone.Radius) == 0
	case w.Unit != nil:
		uw, ok := s.w.(UnitWorld)
		return ok && uw.UnitHolds(w.Unit)
	}
	return false
}

// find resolves a reference against the objects in view: those of the entry (when given) within Range of Near (when
// given), the nearest to Near, or to the player when there is no Near.
func (s *Stepper) find(r *ObjectRef) (objects.GameObject, bool) {
	rng := r.Range
	if rng == 0 {
		rng = DefaultObjectRange
	}
	var best objects.GameObject
	bestD, found := math.MaxFloat64, false
	for _, o := range s.w.GameObjects() {
		if r.Entry != 0 && o.Entry != r.Entry {
			continue
		}
		var d float64
		if r.Near != nil {
			d = dist3(Point{o.X, o.Y, o.Z}, *r.Near)
			if o.Distance < 0 || d > float64(rng) {
				continue
			}
		} else {
			if o.Distance < 0 {
				continue
			}
			d = float64(o.Distance)
		}
		if d < bestD || (d == bestD && o.GUID < best.GUID) {
			best, bestD, found = o, d, true
		}
	}
	return best, found
}

// hdist is the horizontal distance between two points.
func hdist(a, b Point) float64 {
	return math.Hypot(float64(a.X-b.X), float64(a.Y-b.Y))
}

// DirectStepOK says whether a straight step from a to b is within the limits TortoiseBots allows for `cmd move ... direct`.
func DirectStepOK(a, b Point) bool {
	return hdist(a, b) <= DirectMaxDistance && math.Abs(float64(b.Z-a.Z)) <= DirectMaxVertical
}

func dist3(a, b Point) float64 {
	return math.Sqrt(math.Pow(float64(a.X-b.X), 2) + math.Pow(float64(a.Y-b.Y), 2) + math.Pow(float64(a.Z-b.Z), 2))
}

func describeRef(r *ObjectRef) string {
	switch {
	case r.Entry != 0 && r.Near != nil:
		return fmt.Sprintf("game object %d near %.0f %.0f %.0f", r.Entry, r.Near.X, r.Near.Y, r.Near.Z)
	case r.Entry != 0:
		return fmt.Sprintf("game object %d", r.Entry)
	case r.Near != nil:
		return fmt.Sprintf("the game object near %.0f %.0f %.0f", r.Near.X, r.Near.Y, r.Near.Z)
	}
	return "a game object"
}

func describeWait(w *WaitFor) string {
	switch {
	case w.GameObject != nil:
		return describeRef(&w.GameObject.ObjectRef) + " to be " + w.GameObject.State
	case w.HostilesGone != nil:
		return fmt.Sprintf("no hostiles within %.0f yd", w.HostilesGone.Radius)
	case w.Unit != nil:
		return w.Unit.describe()
	}
	return "nothing"
}

// describe is the unit wait for a message: "unit 7271 hostile", with "within 40 yd of x y z" when it is narrowed.
func (u UnitWait) describe() string {
	s := u.String()
	if u.Near != nil {
		s += fmt.Sprintf(" within %.0f yd of %.0f %.0f %.0f", u.radius(), u.Near.X, u.Near.Y, u.Near.Z)
	}
	return s
}

// radius is the radius round Near: Radius, or DefaultUnitRadius.
func (u UnitWait) radius() float32 {
	if u.Radius > 0 {
		return u.Radius
	}
	return DefaultUnitRadius
}

// BatchChest is one chest of a Section: the plan's batch id of its step and where it lies.
type BatchChest struct {
	ID string
	At Point
}

// batchSection reads the section that starts at steps[0]: the run of loot_object steps with a batch id (any id) from there on.
func batchSection(steps []Step) (string, []BatchChest) {
	if len(steps) == 0 || steps[0].LootObject == nil || steps[0].LootObject.Batch == "" {
		return "", nil
	}
	var out []BatchChest
	for _, st := range steps {
		l := st.LootObject
		if l == nil || l.Batch == "" {
			break
		}
		p, _ := batchPoint(l)
		out = append(out, BatchChest{ID: l.Batch, At: p})
	}
	return steps[0].LootObject.Batch, out
}
