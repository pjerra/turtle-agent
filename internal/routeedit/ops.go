// Package routeedit is the write side of the dashboard's route editor: the few edits of a plan's route a person may make from the map
// (move a point, insert one, delete one), applied to a copy of the plan by the server, checked on the navmesh, and kept as numbered
// versions outside every repo and every slot's plan folder. The page never sends a plan: it sends ops, and nothing but a via point's or a
// leg's `to` position can change (see Apply).
package routeedit

import (
	"errors"
	"fmt"
	"math"

	"github.com/pjerra/turtle-agent/internal/dungeon"
	"github.com/pjerra/turtle-agent/internal/navmesh"
)

// The kinds of op.
const (
	KindMove   = "move"
	KindInsert = "insert"
	KindDelete = "delete"
)

const (
	// MaxOps bounds the ops of one request.
	MaxOps = 200
	// BoundsMargin is how far (yards) outside the box of the plan's own points an edited point may lie.
	BoundsMargin = 200.0
	// FloorWindow is how far (yards) above and below the old height a dropped point looks for a floor: a drag on a multi-level map stays on
	// the level it started on.
	FloorWindow = 25.0
)

// Op is one edit of one leg. X and Y are game coordinates (the plan file's: the page converts from its drawing); the height is the
// server's to find. Index counts the leg's route points: 0 to len(via)-1 are the via points and len(via) is `to`. A move takes a via
// point or `to`; an insert puts a new via point before Index (len(via): after the last via point, before `to`); a delete takes a via point.
type Op struct {
	Leg   int     `json:"leg"`
	Kind  string  `json:"kind"`
	Index int     `json:"index"`
	X     float64 `json:"x"`
	Y     float64 `json:"y"`
}

// Floorer finds the navmesh surfaces under a point; navmesh.Planner is one.
type Floorer interface {
	Floors(mapID uint32, x, y, lo, hi float32) ([]navmesh.Floor, error)
}

// Change is what an op did, in words and numbers, for the record and the page.
type Change struct {
	Leg     int            `json:"leg"`
	LegName string         `json:"leg_name,omitempty"`
	Op      string         `json:"op"`
	Part    string         `json:"part"` // "via 2" or "to"
	From    *dungeon.Point `json:"from,omitempty"`
	To      *dungeon.Point `json:"to,omitempty"`
	Moved   float64        `json:"moved_yd,omitempty"`
	Floors  int            `json:"floors,omitempty"` // walkable floors in the window; more than one: FloorPicked
	Picked  bool           `json:"floor_picked,omitempty"`
}

// ErrOp is an op that cannot be applied; Index is its place in the request.
type ErrOp struct {
	Index int
	Msg   string
}

func (e *ErrOp) Error() string { return fmt.Sprintf("op %d: %s", e.Index, e.Msg) }

func opErr(i int, format string, a ...any) error {
	return &ErrOp{Index: i, Msg: fmt.Sprintf(format, a...)}
}

// Clone copies a plan through its canonical file form.
func Clone(p *dungeon.Plan) (*dungeon.Plan, error) {
	b, err := dungeon.Marshal(p)
	if err != nil {
		return nil, err
	}
	return dungeon.Parse(p.Name+".json", b)
}

// Apply applies ops, in order, to a copy of base and returns the copy and what each op did. base is not changed. The only parts an op can
// change are the position of a via point or of a leg's `to`, the insertion of a new plain via point and the deletion of a via point; a
// direct or check_skip via point is locked (the navmesh cannot judge it, so a person edits it in the file). Every new position is checked to
// be finite and near the plan's own points, and is put on the floor of the navmesh nearest the old height (see snap).
func Apply(base *dungeon.Plan, ops []Op, nav Floorer) (*dungeon.Plan, []Change, error) {
	if len(ops) == 0 {
		return nil, nil, errors.New("no ops")
	}
	if len(ops) > MaxOps {
		return nil, nil, fmt.Errorf("%d ops (at most %d in one request)", len(ops), MaxOps)
	}
	if nav == nil {
		return nil, nil, errors.New("no navmesh to put the points on the floor")
	}
	p, err := Clone(base)
	if err != nil {
		return nil, nil, err
	}
	box := planBox(p)
	var changes []Change
	for i, op := range ops {
		if op.Leg < 0 || op.Leg >= len(p.Legs) {
			return nil, nil, opErr(i, "leg %d: the plan has %d legs", op.Leg, len(p.Legs))
		}
		leg := &p.Legs[op.Leg]
		ch := Change{Leg: op.Leg, LegName: leg.Name, Op: op.Kind}
		switch op.Kind {
		case KindMove, KindInsert:
			if err := box.check(op.X, op.Y); err != nil {
				return nil, nil, opErr(i, "%v", err)
			}
		case KindDelete:
		default:
			return nil, nil, opErr(i, "unknown kind %q (move, insert or delete)", op.Kind)
		}
		n := len(leg.Via)
		switch op.Kind {
		case KindMove:
			if op.Index < 0 || op.Index > n {
				return nil, nil, opErr(i, "leg %d has via points 0-%d and `to` (index %d)", op.Leg, n-1, n)
			}
			old := leg.To
			ch.Part = "to"
			if op.Index < n {
				w := leg.Via[op.Index]
				if err := locked(w); err != nil {
					return nil, nil, opErr(i, "via %d of leg %d is %v", op.Index, op.Leg, err)
				}
				old, ch.Part = w.Point, fmt.Sprintf("via %d", op.Index)
			}
			pt, floors, picked, err := snap(nav, p.Map, op.X, op.Y, float64(old.Z))
			if err != nil {
				return nil, nil, opErr(i, "%s of leg %d: %v", ch.Part, op.Leg, err)
			}
			if op.Index < n {
				leg.Via[op.Index].Point = pt
			} else {
				leg.To = pt
			}
			ch.From, ch.To, ch.Floors, ch.Picked = &old, &pt, floors, picked
			ch.Moved = round1(math.Hypot(float64(pt.X-old.X), float64(pt.Y-old.Y)))
		case KindInsert:
			if op.Index < 0 || op.Index > n {
				return nil, nil, opErr(i, "leg %d has room for a via point at 0-%d (index %d)", op.Leg, n, op.Index)
			}
			prev, next := neighbours(p, op.Leg, op.Index)
			pt, floors, picked, err := snap(nav, p.Map, op.X, op.Y, heightOnSegment(prev, next, op.X, op.Y))
			if err != nil {
				return nil, nil, opErr(i, "new via point of leg %d: %v", op.Leg, err)
			}
			leg.Via = append(leg.Via, dungeon.Waypoint{})
			copy(leg.Via[op.Index+1:], leg.Via[op.Index:])
			leg.Via[op.Index] = dungeon.Waypoint{Point: pt}
			ch.Part, ch.To, ch.Floors, ch.Picked = fmt.Sprintf("via %d", op.Index), &pt, floors, picked
		case KindDelete:
			if op.Index < 0 || op.Index >= n {
				return nil, nil, opErr(i, "leg %d has via points 0-%d (a leg needs its `to`: it cannot be deleted)", op.Leg, n-1)
			}
			w := leg.Via[op.Index]
			if err := locked(w); err != nil {
				return nil, nil, opErr(i, "via %d of leg %d is %v", op.Index, op.Leg, err)
			}
			old := w.Point
			leg.Via = append(leg.Via[:op.Index], leg.Via[op.Index+1:]...)
			ch.Part, ch.From = fmt.Sprintf("via %d", op.Index), &old
		}
		changes = append(changes, ch)
	}
	// the edited plan must still be a plan the file loader takes (a direct step needs a via point before it)
	b, err := dungeon.Marshal(p)
	if err != nil {
		return nil, nil, err
	}
	checked, err := dungeon.Parse(p.Name+".json", b)
	if err != nil {
		return nil, nil, fmt.Errorf("the edited plan is not a valid plan: %w", err)
	}
	return checked, changes, nil
}

// locked says why a via point cannot be edited from the map, or nil.
func locked(w dungeon.Waypoint) error {
	switch {
	case w.GMCross:
		return errors.New("a gm_cross point (a GM moves the party there): locked, edit it in the plan file")
	case w.Direct:
		return errors.New("a direct (straight-line) step: locked, edit it in the plan file")
	case w.CheckSkip != "":
		return errors.New("exempt from the plan check (check_skip): locked, edit it in the plan file")
	}
	return nil
}

// neighbours are the points either side of a via point inserted at index: the previous via point (or the leg's start), and the via point now
// at index (or `to`).
func neighbours(p *dungeon.Plan, leg, index int) (prev, next dungeon.Point) {
	l := p.Legs[leg]
	prev, _ = p.LegStart(leg)
	if index > 0 {
		prev = l.Via[index-1].Point
	}
	next = l.To
	if index < len(l.Via) {
		next = l.Via[index].Point
	}
	return prev, next
}

// heightOnSegment is the height of the segment prev-next under the point (x, y): where along it the point projects, clamped to its ends.
func heightOnSegment(prev, next dungeon.Point, x, y float64) float64 {
	dx, dy := float64(next.X-prev.X), float64(next.Y-prev.Y)
	t := 0.0
	if l2 := dx*dx + dy*dy; l2 > 0 {
		t = math.Max(0, math.Min(1, ((x-float64(prev.X))*dx+(y-float64(prev.Y))*dy)/l2))
	}
	return float64(prev.Z) + t*float64(next.Z-prev.Z)
}

// snap puts (x, y) on the navmesh floor nearest z within FloorWindow. floors counts the walkable floors in the window; picked is true
// when there was more than one, so a person is shown that the server chose.
func snap(nav Floorer, mapID uint32, x, y, z float64) (pt dungeon.Point, floors int, picked bool, err error) {
	fl, err := nav.Floors(mapID, float32(x), float32(y), float32(z-FloorWindow), float32(z+FloorWindow))
	if err != nil {
		return pt, 0, false, fmt.Errorf("no floor: %w", err)
	}
	best, bestD := float32(0), math.Inf(1)
	for _, f := range fl {
		if f.Flags&navmesh.NavGround == 0 || f.Flags&navmesh.ExcludeFlags != 0 {
			continue // water, lava, slime and slopes too steep to stand on are not floors for a route
		}
		floors++
		if d := math.Abs(float64(f.Z) - z); d < bestD {
			best, bestD = f.Z, d
		}
	}
	if floors == 0 {
		return pt, 0, false, fmt.Errorf("off the mesh: no walkable floor within %.0f yd of height %.1f at %.1f %.1f", FloorWindow, z, x, y)
	}
	return dungeon.Point{X: float32(x), Y: float32(y), Z: best}, floors, floors > 1, nil
}

func round1(v float64) float64 { return math.Round(v*10) / 10 }

// bbox is the box of the points of a plan, widened by BoundsMargin.
type bbox struct{ x0, y0, x1, y1 float64 }

func (b bbox) check(x, y float64) error {
	if math.IsNaN(x) || math.IsInf(x, 0) || math.IsNaN(y) || math.IsInf(y, 0) {
		return errors.New("the position is not a finite number")
	}
	if x < b.x0 || x > b.x1 || y < b.y0 || y > b.y1 {
		return fmt.Errorf("%.1f %.1f is more than %.0f yd outside the plan's own points", x, y, BoundsMargin)
	}
	return nil
}

func planBox(p *dungeon.Plan) bbox {
	b := bbox{math.Inf(1), math.Inf(1), math.Inf(-1), math.Inf(-1)}
	add := func(q dungeon.Point) {
		x, y := float64(q.X), float64(q.Y)
		b.x0, b.y0, b.x1, b.y1 = math.Min(b.x0, x), math.Min(b.y0, y), math.Max(b.x1, x), math.Max(b.y1, y)
	}
	add(p.Entrance)
	for _, l := range p.Legs {
		add(l.To)
		for _, w := range l.Via {
			add(w.Point)
		}
		for _, g := range l.PullGroups {
			for _, q := range g.Positions {
				add(q)
			}
			if g.Patrol != nil {
				for _, q := range g.Patrol.Path {
					add(q)
				}
			}
		}
		if l.Boss != nil {
			add(l.Boss.Position)
		}
	}
	b.x0, b.y0, b.x1, b.y1 = b.x0-BoundsMargin, b.y0-BoundsMargin, b.x1+BoundsMargin, b.y1+BoundsMargin
	return b
}
