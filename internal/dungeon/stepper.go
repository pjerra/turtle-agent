package dungeon

import (
	"fmt"
	"math"
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
	ActWalk   ActionKind = "walk"       // walk the navmesh to To
	ActPull   ActionKind = "pull"       // pull and kill the group in Entries/Positions
	ActUse    ActionKind = "use_object" // use game object GUID (it is within UseRange)
	ActKill   ActionKind = "kill_boss"  // kill Boss, First entries before it
	ActWait   ActionKind = "wait"       // nothing to do yet: look again with Next
	ActDone   ActionKind = "done"       // the plan is finished
	ActFailed ActionKind = "failed"     // the plan cannot go on: Err says why
)

// Action is the Stepper's current request. Leg and Step locate it in the plan (Step is -1 outside the steps).
type Action struct {
	Kind  ActionKind
	Leg   int
	Step  int
	Label string // for logs: "leg 2 (ironaya): use_object 124371"

	To        Point               // walk target
	Entries   []uint32            // pull: the group's entries
	Positions []Point             // pull: where its members stand
	Ambush    bool                // pull: the group is stealthed (PullGroup.Ambush)
	Radius    float32             // pull: PullGroup.Radius, 0 for the default
	Skip      []uint32            // pull and boss: entries not to wait for
	Boss      *Boss               // boss
	Object    *objects.GameObject // use
	GUID      packet.GUID         // use
	Requires  *ItemRef            // use: the item a non-GM leader needs to carry (UseObject.RequiresItem)
	Remaining time.Duration       // wait: time left before the plan fails
	Err       string              // failed
}

type phase int

const (
	phWalk phase = iota
	phPulls
	phSteps
	phBoss
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
	idx       int        // pull group or step index
	since     time.Time  // when the current wait started; zero when not waiting
	stepAt    time.Time  // when the current step was first looked at; zero before
	stepWalks int        // walks Complete has seen in the current step
	last      ActionKind // the kind of the action Next returned last
	err       string
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

func (s *Stepper) enterLeg(i int) {
	s.leg, s.idx, s.since = i, 0, time.Time{}
	s.stepAt, s.stepWalks = time.Time{}, 0
	if i >= len(s.plan.Legs) {
		s.ph = phDone
		return
	}
	s.ph = phWalk
}

// Position reports where the stepper is, for logs and the driver's status: the leg, the phase name and the index in it.
func (s *Stepper) Position() (leg int, phase string, index int) {
	return s.leg, [...]string{"walk", "pull_groups", "steps", "boss", "done", "failed"}[s.ph], s.idx
}

// Complete reports that the action Next last returned (walk, pull, use_object or kill_boss) is done. After a walk
// that a use_object step asked for, the step is looked at again; after a wait it does nothing.
func (s *Stepper) Complete() {
	switch s.ph {
	case phWalk:
		s.ph, s.idx = phPulls, 0
	case phPulls:
		s.idx++
	case phSteps:
		// A walk inside a step only brings the player to the object: the step itself is still to do.
		switch s.last {
		case ActUse:
			s.nextStep()
		case ActWalk:
			s.stepWalks++
		}
	case phBoss:
		s.enterLeg(s.leg + 1)
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
	return name + ": " + fmt.Sprintf(format, args...)
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
			return Action{Kind: ActPull, Leg: s.leg, Step: -1, Entries: g.Entries, Positions: g.Positions, Ambush: g.Ambush, Radius: g.Radius, Skip: l.Skip,
				Label: s.label("pull group %d of %d", s.idx+1, len(l.PullGroups))}
		case phSteps:
			if s.idx >= len(l.Steps) {
				s.ph = phBoss
				continue
			}
			a, advance := s.step(&l.Steps[s.idx])
			if advance {
				s.nextStep()
				continue
			}
			return a
		case phBoss:
			if l.Boss == nil {
				s.enterLeg(s.leg + 1)
				continue
			}
			return Action{Kind: ActKill, Leg: s.leg, Step: -1, Boss: l.Boss, Skip: l.Skip, Label: s.label("kill boss %d", l.Boss.Entry)}
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
	if a.Kind != ActWalk || st.UseObject == nil {
		return a, advance
	}
	ref := describeRef(&st.UseObject.ObjectRef)
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
	case st.UseObject != nil:
		u := st.UseObject
		o, ok := s.find(&u.ObjectRef)
		if !ok || o.Distance < 0 {
			// Not in view yet: the objects load as the player nears them, so go to where it stands.
			target, have := Point{}, false
			switch {
			case u.Stand != nil:
				target, have = *u.Stand, true
			case u.Near != nil:
				target, have = *u.Near, true
			}
			if have && dist3(s.w.Position(), target) > ArriveRadius {
				base.Kind, base.To = ActWalk, target
				base.Label = s.label("step %d: walk to %.1f %.1f %.1f to bring %s into view", s.idx, target.X, target.Y, target.Z, describeRef(&u.ObjectRef))
				return base, false
			}
			return fail("%s is not in view", describeRef(&u.ObjectRef))
		}
		if u.Stand != nil && dist3(s.w.Position(), *u.Stand) > ArriveRadius && o.Distance > UseRange {
			base.Kind, base.To = ActWalk, *u.Stand
			base.Label = s.label("step %d: walk to the stand point for %s", s.idx, describeRef(&u.ObjectRef))
			return base, false
		}
		if o.Distance > UseRange {
			base.Kind, base.To = ActWalk, Point{o.X, o.Y, o.Z}
			base.Label = s.label("step %d: walk next to %s (%.1f yd away)", s.idx, describeRef(&u.ObjectRef), o.Distance)
			return base, false
		}
		base.Kind, base.Object, base.GUID, base.Requires = ActUse, &o, o.GUID, u.RequiresItem
		base.Label = s.label("step %d: use %s", s.idx, describeRef(&u.ObjectRef))
		return base, false
	case st.WaitFor != nil:
		w := st.WaitFor
		if s.since.IsZero() {
			s.since = s.now()
		}
		if s.holds(w) {
			return base, true
		}
		timeout := time.Duration(w.TimeoutS) * time.Second
		left := timeout - s.now().Sub(s.since)
		if left <= 0 {
			return fail("waited %d s for %s", w.TimeoutS, describeWait(w))
		}
		base.Kind, base.Remaining = ActWait, left
		base.Label = s.label("step %d: wait for %s (%.0f s left)", s.idx, describeWait(w), left.Seconds())
		return base, false
	}
	return fail("is empty")
}

func (s *Stepper) holds(w *WaitFor) bool {
	switch {
	case w.GameObject != nil:
		o, ok := s.find(&w.GameObject.ObjectRef)
		return ok && o.State == w.GameObject.State
	case w.HostilesGone != nil:
		c := s.w.Position()
		if w.HostilesGone.Center != nil {
			c = *w.HostilesGone.Center
		}
		return s.w.HostilesWithin(c, w.HostilesGone.Radius) == 0
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
	}
	return "nothing"
}
