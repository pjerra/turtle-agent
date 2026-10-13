// Package dungeon holds the dungeon plan format of the dungeon leader (issue #73): a JSON file per dungeon
// (dungeons/<name>.json) that lists, in order, where to walk, which groups to pull, which doors to open and which
// boss to kill; a validating loader; and a pure Stepper that walks a plan against a fake or real World.
//
// The package does no networking. The driver that turns the Stepper's actions into RPC calls is a separate piece.
package dungeon

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"slices"
	"strings"
)

// Version is the plan format this package reads and writes.
const Version = 1

// Limits the loader enforces, so a typo cannot ask for an hour-long wait or a 3-mile radius.
const (
	DefaultTimeoutS    = 60
	MaxTimeoutS        = 600
	DefaultObjectRange = 8   // yards around Near an object is looked for
	MaxRadius          = 300 // yards
)

// Point is a position in the map's coordinates.
type Point struct {
	X float32 `json:"x"`
	Y float32 `json:"y"`
	Z float32 `json:"z"`
}

// Plan is a loaded, validated dungeon plan.
type Plan struct {
	Version  int    `json:"version"`
	Name     string `json:"name"`
	Map      uint32 `json:"map"`
	Entrance Point  `json:"entrance"`
	// EntranceTrigger is the id of the area trigger of the dungeon's entrance portal (areatrigger_teleport in the world
	// database; Uldaman 286). A ghost whose corpse lies inside sends it at the portal to be taken in and revived (the client
	// does the same on stepping into it). Without it a corpse run into the dungeon is refused; the rest of the run does not need it.
	EntranceTrigger uint32   `json:"entrance_trigger,omitempty"`
	Notes           []string `json:"notes,omitempty"`
	Legs            []Leg    `json:"legs"`
}

// Leg is one stretch of the dungeon. The driver walks to To, clears PullGroups in order, runs Steps in order, then
// walks to the Boss and kills it. Every part is optional but a leg must hold at least one of them.
type Leg struct {
	Name       string      `json:"name,omitempty"`
	To         Point       `json:"to"`
	PullGroups []PullGroup `json:"pull_groups,omitempty"`
	// Skip lists creature entries the leg never waits for: rares, escorts, friendly NPCs, and adds that die with the boss.
	Skip  []uint32 `json:"skip,omitempty"`
	Steps []Step   `json:"steps,omitempty"`
	Boss  *Boss    `json:"boss,omitempty"`
	Notes []string `json:"notes,omitempty"`
}

// PullGroup is one pack: the entries it holds and where its members stand (one position each, or fewer when they
// stand together; the first is the one to pull).
type PullGroup struct {
	Entries   []uint32 `json:"entries"`
	Positions []Point  `json:"positions"`
	// Ambush marks a pack whose members are stealthed (tools/dungeongen sets it from the spawn's stealth aura): a
	// companion never attacks a stealthed unit, so `.bot action pullback` draws no fight. The driver does not pull
	// such a group: it walks the party through the group's positions and fights whatever shows itself (see
	// Driver.sneakPast). A member in view without a stealth aura is pulled as usual; the flag only decides what
	// happens when none is in view.
	Ambush bool `json:"ambush,omitempty"`
	// Radius is how far from one of Positions a unit of the group's entries may stand and still be a member (yards;
	// 0: the driver's PackRadius, 25). Packs of the same entry that stand closer to each other than that, such as the
	// two rows of Stonevault Ambushers at the Uldaman Keystone, need a smaller one so that each group takes its own.
	Radius float32 `json:"radius,omitempty"`
}

// Boss is the leg's boss. First lists the entries of the encounter to kill before the boss itself, in order (an add
// that heals it, the first of a group of brothers); the boss entry is killed last and the leg is done when it dies.
type Boss struct {
	Entry    uint32   `json:"entry"`
	Position Point    `json:"position"`
	First    []uint32 `json:"first,omitempty"`
}

// Step is one thing to do between the pulls and the boss: exactly one of UseObject and WaitFor is set.
type Step struct {
	UseObject *UseObject `json:"use_object,omitempty"`
	WaitFor   *WaitFor   `json:"wait_for,omitempty"`
}

// ObjectRef picks a game object: by entry, by position, or both (the nearest object of that entry to Near).
type ObjectRef struct {
	Entry uint32  `json:"entry,omitempty"`
	Near  *Point  `json:"near,omitempty"`
	Range float32 `json:"range,omitempty"` // yards around Near; DefaultObjectRange when 0
}

// UseObject uses a game object (right-click). Stand is where to stand to do it; without it the driver walks next
// to the object.
type UseObject struct {
	ObjectRef
	Stand *Point `json:"stand,omitempty"`
	// RequiresItem is the item a non-GM leader must carry for the use to do anything (the object's lock names it: the
	// Uldaman Keystone needs the Staff of Prehistoria). Without it the core ignores the use silently, so the driver checks the
	// leader's bags before the run and before the step and fails with this item's name. Only a leader the caller says is a GM
	// (gm: true) is not checked: the core lets a GM through a lock in GM mode only.
	RequiresItem *ItemRef `json:"requires_item,omitempty"`
}

// ItemRef names an item: its entry and, for the messages, its name.
type ItemRef struct {
	Entry uint32 `json:"entry"`
	Name  string `json:"name,omitempty"`
}

// String is "Staff of Prehistoria (item 7733)", or "item 7733" without a name.
func (i ItemRef) String() string {
	if i.Name == "" {
		return fmt.Sprintf("item %d", i.Entry)
	}
	return fmt.Sprintf("%s (item %d)", i.Name, i.Entry)
}

// WaitFor waits for a condition, up to TimeoutS seconds, and fails the plan when it does not come. Exactly one of
// GameObject and HostilesGone is set.
type WaitFor struct {
	GameObject   *ObjectState  `json:"gameobject,omitempty"`
	HostilesGone *HostilesGone `json:"hostiles_gone,omitempty"`
	TimeoutS     int           `json:"timeout_s,omitempty"`
}

// ObjectState waits for a game object to be in a state: ready (a door closed), active (open or used) or active_alt.
type ObjectState struct {
	ObjectRef
	State string `json:"state"`
}

// HostilesGone waits until no hostile unit is within Radius yards of Center (the player when nil).
type HostilesGone struct {
	Radius float32 `json:"radius"`
	Center *Point  `json:"center,omitempty"`
}

// ObjectStates are the values of ObjectState.State.
var ObjectStates = []string{"ready", "active", "active_alt"}

// raw layer: what the JSON file says before it is checked. Pointers tell a missing field from a zero.
type rawPoint struct {
	X, Y, Z *float32
}

type rawPlan struct {
	Version  *int              `json:"version"`
	Name     string            `json:"name"`
	Map      *uint32           `json:"map"`
	Entrance *rawPoint         `json:"entrance"`
	Trigger  uint32            `json:"entrance_trigger"`
	Notes    []string          `json:"notes"`
	Legs     []json.RawMessage `json:"legs"`
}

type rawLeg struct {
	Name       string            `json:"name"`
	To         *rawPoint         `json:"to"`
	PullGroups []rawPullGroup    `json:"pull_groups"`
	Skip       []uint32          `json:"skip"`
	Steps      []json.RawMessage `json:"steps"`
	Boss       *rawBoss          `json:"boss"`
	Notes      []string          `json:"notes"`
}

type rawPullGroup struct {
	Entries   []uint32   `json:"entries"`
	Positions []rawPoint `json:"positions"`
	Ambush    bool       `json:"ambush"`
	Radius    float32    `json:"radius"`
}

type rawBoss struct {
	Entry    uint32    `json:"entry"`
	Position *rawPoint `json:"position"`
	First    []uint32  `json:"first"`
}

type rawStep struct {
	UseObject *rawUse  `json:"use_object"`
	WaitFor   *rawWait `json:"wait_for"`
}

type rawRef struct {
	Entry uint32    `json:"entry"`
	Near  *rawPoint `json:"near"`
	Range *float32  `json:"range"`
}

type rawUse struct {
	rawRef
	Stand        *rawPoint `json:"stand"`
	RequiresItem *rawItem  `json:"requires_item"`
}

type rawItem struct {
	Entry uint32 `json:"entry"`
	Name  string `json:"name"`
}

type rawWait struct {
	GameObject   *rawObjState `json:"gameobject"`
	HostilesGone *rawHostiles `json:"hostiles_gone"`
	TimeoutS     *int         `json:"timeout_s"`
}

type rawObjState struct {
	rawRef
	State string `json:"state"`
}

type rawHostiles struct {
	Radius *float32  `json:"radius"`
	Center *rawPoint `json:"center"`
}

// checker collects the errors of one file, each with the path of the field.
type checker struct {
	file string
	errs []error
}

func (c *checker) fail(path, format string, args ...any) {
	c.errs = append(c.errs, fmt.Errorf("%s: %s: %s", c.file, path, fmt.Sprintf(format, args...)))
}

// Load reads and validates the plan file at path.
func Load(path string) (*Plan, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	return Parse(path, data)
}

// Parse validates a plan; name is used in the error messages. The error lists every problem found, one per line,
// each with the path of the field (legs[2].steps[0].use_object.entry) so the file can be fixed in one pass.
func Parse(name string, data []byte) (*Plan, error) {
	c := &checker{file: name}
	var rp rawPlan
	if err := strict(data, &rp); err != nil {
		if !strings.HasPrefix(err.Error(), "json: unknown field ") {
			return nil, fmt.Errorf("%s: %w", name, err)
		}
		// An unknown field stops the strict decoder where it stands: report it, then read the file again
		// without the check so the other mistakes are listed with it.
		c.errs = append(c.errs, fmt.Errorf("%s: %w", name, err))
		rp = rawPlan{}
		if err := json.Unmarshal(data, &rp); err != nil {
			return nil, errors.Join(c.errs...)
		}
	}
	p := &Plan{Name: rp.Name, Notes: rp.Notes}
	switch {
	case rp.Version == nil:
		c.fail("version", "required (this build reads version %d)", Version)
	case *rp.Version != Version:
		c.fail("version", "%d is not supported (this build reads version %d)", *rp.Version, Version)
	default:
		p.Version = *rp.Version
	}
	if strings.TrimSpace(p.Name) == "" {
		c.fail("name", "required")
	}
	if rp.Map == nil {
		c.fail("map", "required (the instance map id)")
	} else {
		p.Map = *rp.Map
	}
	p.Entrance = c.point("entrance", rp.Entrance)
	p.EntranceTrigger = rp.Trigger
	if len(rp.Legs) == 0 {
		c.fail("legs", "needs at least one leg")
	}
	for i, rl := range rp.Legs {
		path := fmt.Sprintf("legs[%d]", i)
		var l rawLeg
		if err := strict(rl, &l); err != nil {
			c.fail(path, "%v", err)
			continue
		}
		p.Legs = append(p.Legs, c.leg(path, &l))
	}
	if len(c.errs) > 0 {
		return nil, errors.Join(c.errs...)
	}
	return p, nil
}

// strict decodes JSON that must hold exactly one value of v's shape and no unknown field.
func strict(data []byte, v any) error {
	d := json.NewDecoder(bytes.NewReader(data))
	d.DisallowUnknownFields()
	if err := d.Decode(v); err != nil {
		return err
	}
	if d.More() {
		return errors.New("more than one JSON value")
	}
	return nil
}

func (c *checker) point(path string, p *rawPoint) Point {
	if p == nil {
		c.fail(path, "required ({\"x\":..,\"y\":..,\"z\":..})")
		return Point{}
	}
	var out Point
	for _, f := range []struct {
		name string
		in   *float32
		out  *float32
	}{{"x", p.X, &out.X}, {"y", p.Y, &out.Y}, {"z", p.Z, &out.Z}} {
		switch {
		case f.in == nil:
			c.fail(path+"."+f.name, "required")
		case math.IsNaN(float64(*f.in)) || math.IsInf(float64(*f.in), 0) || math.Abs(float64(*f.in)) > 20000:
			c.fail(path+"."+f.name, "%v is not a map coordinate", *f.in)
		default:
			*f.out = *f.in
		}
	}
	return out
}

func (c *checker) entries(path string, es []uint32, need bool) {
	if need && len(es) == 0 {
		c.fail(path, "needs at least one entry")
	}
	for i, e := range es {
		if e == 0 {
			c.fail(fmt.Sprintf("%s[%d]", path, i), "0 is no creature entry")
		}
	}
}

func (c *checker) leg(path string, r *rawLeg) Leg {
	l := Leg{Name: r.Name, Skip: r.Skip, Notes: r.Notes}
	l.To = c.point(path+".to", r.To)
	for i, g := range r.PullGroups {
		gp := fmt.Sprintf("%s.pull_groups[%d]", path, i)
		pg := PullGroup{Entries: g.Entries, Ambush: g.Ambush, Radius: g.Radius}
		if g.Radius < 0 || g.Radius > 60 {
			c.fail(gp+".radius", "%g: use 0 (the default) or 1-60 yards", g.Radius)
		}
		c.entries(gp+".entries", g.Entries, true)
		if len(g.Positions) == 0 {
			c.fail(gp+".positions", "needs at least one position")
		}
		if len(g.Positions) > len(g.Entries) && len(g.Entries) > 0 {
			c.fail(gp+".positions", "%d positions for %d entries: one per member at most", len(g.Positions), len(g.Entries))
		}
		for j := range g.Positions {
			pg.Positions = append(pg.Positions, c.point(fmt.Sprintf("%s.positions[%d]", gp, j), &g.Positions[j]))
		}
		l.PullGroups = append(l.PullGroups, pg)
	}
	c.entries(path+".skip", r.Skip, false)
	for i, e := range r.Skip {
		for j, g := range r.PullGroups {
			if slices.Contains(g.Entries, e) {
				c.fail(fmt.Sprintf("%s.skip[%d]", path, i), "%d is also in pull_groups[%d].entries: the leg would pull it and never wait for it", e, j)
			}
		}
		if r.Boss != nil && slices.Contains(r.Boss.First, e) {
			c.fail(fmt.Sprintf("%s.skip[%d]", path, i), "%d is also in boss.first: the leg would kill it and never wait for it", e)
		}
	}
	if r.Boss != nil {
		bp := path + ".boss"
		b := Boss{Entry: r.Boss.Entry, First: r.Boss.First}
		if b.Entry == 0 {
			c.fail(bp+".entry", "required")
		}
		b.Position = c.point(bp+".position", r.Boss.Position)
		c.entries(bp+".first", b.First, false)
		for i, e := range b.First {
			if e == b.Entry && e != 0 {
				c.fail(fmt.Sprintf("%s.first[%d]", bp, i), "the boss (%d) is killed last; leave it out of first", e)
			}
		}
		for i, e := range l.Skip {
			if e == b.Entry && e != 0 {
				c.fail(fmt.Sprintf("%s.skip[%d]", path, i), "skips the boss (%d)", e)
			}
		}
		l.Boss = &b
	}
	for i, rs := range r.Steps {
		sp := fmt.Sprintf("%s.steps[%d]", path, i)
		var s rawStep
		if err := strict(rs, &s); err != nil {
			c.fail(sp, "%v", err)
			continue
		}
		l.Steps = append(l.Steps, c.step(sp, &s))
	}
	if len(l.PullGroups) == 0 && len(l.Steps) == 0 && l.Boss == nil {
		c.fail(path, "has no pull_groups, steps or boss: it would only walk to \"to\"; fold it into the next leg or add one")
	}
	return l
}

func (c *checker) ref(path string, r *rawRef) ObjectRef {
	o := ObjectRef{Entry: r.Entry}
	if r.Near != nil {
		p := c.point(path+".near", r.Near)
		o.Near = &p
	}
	if r.Entry == 0 && r.Near == nil {
		c.fail(path, "needs an entry, a near position, or both")
	}
	if r.Range != nil {
		if r.Near == nil {
			c.fail(path+".range", "only means something with near")
		}
		if *r.Range <= 0 || *r.Range > MaxRadius {
			c.fail(path+".range", "%v yards is outside 0-%d", *r.Range, MaxRadius)
		} else {
			o.Range = *r.Range
		}
	}
	return o
}

func (c *checker) step(path string, r *rawStep) Step {
	var s Step
	switch {
	case r.UseObject != nil && r.WaitFor != nil:
		c.fail(path, "has both use_object and wait_for: one step does one thing")
	case r.UseObject == nil && r.WaitFor == nil:
		c.fail(path, "needs use_object or wait_for")
	case r.UseObject != nil:
		u := &UseObject{ObjectRef: c.ref(path+".use_object", &r.UseObject.rawRef)}
		if r.UseObject.Stand != nil {
			p := c.point(path+".use_object.stand", r.UseObject.Stand)
			u.Stand = &p
		}
		if ri := r.UseObject.RequiresItem; ri != nil {
			if ri.Entry == 0 {
				c.fail(path+".use_object.requires_item.entry", "required (the item entry)")
			}
			u.RequiresItem = &ItemRef{Entry: ri.Entry, Name: ri.Name}
		}
		s.UseObject = u
	default:
		s.WaitFor = c.wait(path+".wait_for", r.WaitFor)
	}
	return s
}

func (c *checker) wait(path string, r *rawWait) *WaitFor {
	w := &WaitFor{TimeoutS: DefaultTimeoutS}
	if r.TimeoutS != nil {
		if *r.TimeoutS <= 0 || *r.TimeoutS > MaxTimeoutS {
			c.fail(path+".timeout_s", "%d s is outside 1-%d", *r.TimeoutS, MaxTimeoutS)
		} else {
			w.TimeoutS = *r.TimeoutS
		}
	}
	switch {
	case r.GameObject != nil && r.HostilesGone != nil:
		c.fail(path, "has both gameobject and hostiles_gone: one wait waits for one thing")
	case r.GameObject == nil && r.HostilesGone == nil:
		c.fail(path, "needs gameobject or hostiles_gone")
	case r.GameObject != nil:
		gp := path + ".gameobject"
		g := &ObjectState{ObjectRef: c.ref(gp, &r.GameObject.rawRef), State: r.GameObject.State}
		if !validState(g.State) {
			c.fail(gp+".state", "%q is none of %s", g.State, strings.Join(ObjectStates, ", "))
		}
		w.GameObject = g
	default:
		hp := path + ".hostiles_gone"
		h := &HostilesGone{}
		switch {
		case r.HostilesGone.Radius == nil:
			c.fail(hp+".radius", "required")
		case *r.HostilesGone.Radius <= 0 || *r.HostilesGone.Radius > MaxRadius:
			c.fail(hp+".radius", "%v yards is outside 0-%d", *r.HostilesGone.Radius, MaxRadius)
		default:
			h.Radius = *r.HostilesGone.Radius
		}
		if r.HostilesGone.Center != nil {
			p := c.point(hp+".center", r.HostilesGone.Center)
			h.Center = &p
		}
		w.HostilesGone = h
	}
	return w
}

func validState(s string) bool {
	for _, v := range ObjectStates {
		if v == s {
			return true
		}
	}
	return false
}
