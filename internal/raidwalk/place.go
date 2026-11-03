// place.go: the pure decisions of `raid-walk --place` (spec ~/TortoiseBots-audit/research/raid-walk-place/SPEC.md): the
// staged start step, the until stop, the corridor clear of the legs the raid skips, and the toolkit's skip entries.
// No network, no session: the command computes all of it in validate() and the place phase only runs it.
package raidwalk

import (
	"errors"
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"

	"github.com/pjerra/turtle-agent/internal/dungeon"
	"github.com/pjerra/turtle-agent/internal/planexport"
)

// The place decisions' numbers (spec 4.3).
const (
	// PlaceStartClearYd is how far a staged start must stand from every AHEAD creature row.
	PlaceStartClearYd = 50
	// PlaceCorridorYd and PlaceCorridorZ bound the corridor: a BEHIND creature row farther than PlaceCorridorYd (its own
	// radius taken off) from the route, or more than PlaceCorridorZ above or below it, is not cleared.
	PlaceCorridorYd = 45
	PlaceCorridorZ  = 15
	// PlaceBossGuardYd refuses the placement when a BEHIND boss or hold row comes within it (plus its radius) of the route.
	PlaceBossGuardYd = 30
	// PlaceCircleSlack is added to a BEHIND pull row's radius for its circle, capped at PlaceCircleMax.
	PlaceCircleSlack = 5
	PlaceCircleMax   = 30
	// PlaceAheadSlack is added to an AHEAD creature row's radius for the overlap test.
	PlaceAheadSlack = 3
	// PlaceUnitYd is the radius of a per-unit circle a split row becomes.
	PlaceUnitYd = 4
	// PlaceOwnYd is how far beyond a BEHIND pull row's radius a unit's home may stand and still be owned by that row
	// (the spawn-home ownership test: the same 6 yd the extra clear round allows around a corridor circle).
	PlaceOwnYd = 6
	// PlaceSweepYd and PlaceSweepZ bound the landing sweep (PLACE-W26 M1): a BEHIND pull row whose point stands
	// within PlaceSweepYd 2-D and PlaceSweepZ in z of the start point is cleared even when the corridor never
	// reaches it (a leftover patrol whose loop walks into the landing). 150 yd is the farthest BEHIND patrol
	// spawn whose loop reaches a placed ZG landing; the ZG coverage test is the real guard: raise the constant
	// when it fails.
	PlaceSweepYd = 150
	PlaceSweepZ  = 60
	// PlaceSweepSlack is added to a sweep circle's radius, so a patroller a few seconds into its loop is still
	// inside. Capped at PlaceCircleMax like the corridor's circles.
	PlaceSweepSlack = 10
	// PlaceMaxCircles bounds one walk's clear: more circles than this refuse the placement.
	PlaceMaxCircles = 40
	// PlaceReviveYd is how close a hire's frame must be to the place point to count as placed.
	PlaceReviveYd = 12
	// PlaceMTYd is how close the main tank's frame must be to the start point before `run start`.
	PlaceMTYd = 15
	// PlaceFloorYd is how far the toolkit's floor may be from the start point's z.
	PlaceFloorYd = 3
)

// ClearCircle is one despawn circle of the place clear (spec 4.3, PLACE-W26 M1): a BEHIND creature row's circle, or,
// for a row whose circle overlaps an AHEAD row's, one circle per unit of the plan's pull group (PerUnit,
// PlaceUnitYd). Sweep marks a circle the M1 landing sweep added (a BEHIND pull row near the start point, outside
// the corridor): corridor circles come first, sweep circles after, both in route then step order.
type ClearCircle struct {
	Step    int
	Leg     int
	X, Y, Z float32
	Radius  float32
	Entries []uint32
	PerUnit bool
	Unit    uint32 // the unit's entry (PerUnit only)
	Sweep   bool   // the M1 landing sweep added this circle, not the corridor
}

// ClearPlan is PlaceClear's answer: the circles to despawn in route order, the per-unit circles it dropped and why, the
// AHEAD creature rows' circles (the runtime pre-read's guard), and the refusal (a BEHIND boss or hold row near the
// route; the circles are then nil).
type ClearPlan struct {
	Circles []ClearCircle
	Notes   []string
	Ahead   []AheadCircle
	Behind  []BehindRow
	Refusal string
}

// AheadCircle is one AHEAD creature row's circle (its radius plus PlaceAheadSlack) with the row's entries, so the quiet
// can tell a unit of a later leg (which stays) from a unit of a leg the raid skips (which must go).
type AheadCircle struct {
	Step    int
	X, Y, Z float32
	Radius  float32
	Entries []uint32
}

// BehindRow is one BEHIND pull row: any pull row before the start step, whether or not its circle reaches the corridor.
// A unit is only BEHIND trash when such a row holds its entry and owns its home (PlacePlan.BehindOwns), because entries
// are shared: a later leg's patrol can carry a corridor row's entry.
type BehindRow struct {
	Step    int
	X, Y, Z float32
	Radius  float32
	Entries []uint32
}

// BehindOwns says whether a BEHIND pull row that holds the entry owns a home: the point lies within the row's radius
// plus PlaceOwnYd, 2D, within placeAheadDz of the row's height (the z rule the AHEAD ownership test uses).
func (p PlacePlan) BehindOwns(entry uint32, x, y, z float32) (int, bool) {
	for _, b := range p.Behind {
		if !holdsEntry(b.Entries, entry) {
			continue
		}
		if math.Abs(float64(b.Z)-float64(z)) > placeAheadDz {
			continue
		}
		if dist2f(x, y, b.X, b.Y) <= float64(b.Radius)+PlaceOwnYd {
			return b.Step, true
		}
	}
	return 0, false
}

// AheadIn says whether a point lies in an AHEAD creature row's circle (spec 5 step 6).
func (p PlacePlan) AheadIn(x, y, z float32) (int, bool) {
	for _, a := range p.Ahead {
		if math.Abs(float64(a.Z)-float64(z)) > placeAheadDz {
			continue
		}
		if dist2f(x, y, a.X, a.Y) <= float64(a.Radius)+PlaceAheadSlack {
			return a.Step, true
		}
	}
	return 0, false
}

// AheadOwns says whether a unit of an AHEAD row stands in that row's own area: its entry is one of the row's and the
// point lies within the row's radius plus PlaceAheadSlack, within placeAheadDz (the same test aheadInside makes). The
// quiet uses it to name the row a unit beyond the landing reach belongs to.
func (p PlacePlan) AheadOwns(entry uint32, x, y, z float32) (int, bool) {
	for _, a := range p.Ahead {
		if !holdsEntry(a.Entries, entry) {
			continue
		}
		if math.Abs(float64(a.Z)-float64(z)) > placeAheadDz {
			continue
		}
		if dist2f(x, y, a.X, a.Y) <= float64(a.Radius)+PlaceAheadSlack {
			return a.Step, true
		}
	}
	return 0, false
}

// holdsEntry says a row's entry list holds an entry.
func holdsEntry(entries []uint32, e uint32) bool {
	for _, x := range entries {
		if x == e {
			return true
		}
	}
	return false
}

// PlacePlan is the place phase's plan (spec 4): the start step and its point, the until stop the corridor ends at, the
// circles, the notes and the toolkit's skip entries.
type PlacePlan struct {
	Start   int
	Leg     string // the leg the start came from ("" for --from "step N")
	Point   dungeon.Point
	Stop    int
	Circles []ClearCircle
	Notes   []string
	Ahead   []AheadCircle
	Behind  []BehindRow
	Skips   []uint32
}

// creatureRow says a row holds a unit.
func creatureRow(s planexport.StepRow) bool {
	switch s.Kind {
	case planexport.KindPull, planexport.KindBoss, planexport.KindHold:
		return true
	}
	return false
}

// moveRow says a row is a walk the raid can be placed on.
func moveRow(s planexport.StepRow) bool {
	switch s.Kind {
	case planexport.KindMove, planexport.KindMoveDirect, planexport.KindMoveHopExact:
		return true
	}
	return false
}

// placedRow says a row carries a point (the same rule as firstStepPoint).
func placedRow(s planexport.StepRow) bool { return s.X != 0 || s.Y != 0 || s.Z != 0 }

// legName is the leg as the messages name it: its name, else its number.
func legName(l planexport.LegRow) string {
	if l.Name != "" {
		return l.Name
	}
	return strconv.Itoa(l.Leg)
}

// findLeg finds a leg by its row number or, case-insensitively, by its name (spec 4.2).
func findLeg(rows planexport.Rows, word string) (int, string, error) {
	if n, err := strconv.Atoi(word); err == nil {
		for _, l := range rows.Legs {
			if l.Leg == n {
				return l.Leg, legName(l), nil
			}
		}
		return 0, "", fmt.Errorf("the plan has no leg %d", n)
	}
	for _, l := range rows.Legs {
		if strings.EqualFold(l.Name, word) {
			return l.Leg, legName(l), nil
		}
	}
	return 0, "", fmt.Errorf("the plan has no leg %s", word)
}

// legSpan is the first and last step index of a leg (-1, -1 when the leg has no step).
func legSpan(rows planexport.Rows, leg int) (int, int) {
	lo, hi := -1, -1
	for i, s := range rows.Steps {
		if s.Leg != leg {
			continue
		}
		if lo < 0 {
			lo = i
		}
		hi = i
	}
	return lo, hi
}

// aheadCreatureWithin says whether a creature row after i stands within yd (2D) of row i.
func aheadCreatureWithin(rows planexport.Rows, i int, yd float64) bool {
	p := rows.Steps[i]
	for j := i + 1; j < len(rows.Steps); j++ {
		s := rows.Steps[j]
		if !creatureRow(s) || !placedRow(s) {
			continue
		}
		if dist2f(p.X, p.Y, s.X, s.Y) <= yd {
			return true
		}
	}
	return false
}

// eventStep says whether row i lies in a scripted event the run leader would move the start back out of (spec 4.2): the
// row is a hold, or a move leading into one. The step returned is the hold's.
func eventStep(rows planexport.Rows, i int) (int, bool) {
	if rows.Steps[i].Kind == planexport.KindHold {
		return i + 1, true
	}
	if moveRow(rows.Steps[i]) && i+1 < len(rows.Steps) && rows.Steps[i+1].Kind == planexport.KindHold {
		return i + 2, true
	}
	return 0, false
}

// PlaceStart is the start step of a placed walk (spec 4.2): --from "step N" as given (a positioned move row, or a
// pull row at its own stand point — the nearest move row before it when the pull row has no point), or
// --from "leg L" staged at the highest move row before the leg's first creature row that has no AHEAD creature row
// within PlaceStartClearYd. The FIRST leg needs no placement: it is a door start (door is true, step 1), and the
// caller skips the place phase and logs `place: leg X is the first leg; starting from the door`. When the leg holds
// no quiet row, the LAST quiet move row before that point is used, searching backward through the earlier legs (walk
// 26c: Jeklik's leg starts with a pull); only when no quiet row stands anywhere before the first pull is the
// placement refused. A fallback start with a boss or hold row between it and the target leg's first row is refused
// too: the raid would re-fight it on its way to the target leg. The second answer names the leg the start came from
// ("" for an explicit step), the third the requested leg's name when the start fell back to an earlier leg (""
// otherwise): the caller logs `place: leg X has no quiet row; placing at step N (leg Y)`.
func PlaceStart(rows planexport.Rows, from []string) (step int, leg, fallbackFrom string, door bool, err error) {
	if len(from) != 3 || from[0] != "from" {
		return 0, "", "", false, errors.New("place: the start needs the words of --from (ParseFrom)")
	}
	switch from[1] {
	case "step":
		n, err := strconv.Atoi(from[2])
		if err != nil || n < 1 || n > len(rows.Steps) {
			return 0, "", "", false, fmt.Errorf("--place --from step %s: the rows have %d steps", from[2], len(rows.Steps))
		}
		s := rows.Steps[n-1]
		if !moveRow(s) {
			// A pull row is a stand the raid may be placed on: its own point is the pull's stand (walk 32: steps 237
			// and 560 were refused, and the operator had to find 238/559/575 by hand). A pull row without a point
			// falls back to the nearest move row before it. Anything else still refuses.
			if s.Kind != planexport.KindPull {
				return 0, "", "", false, fmt.Errorf("--place --from step %d: step %d is a %s row; place at a move row before it", n, n, s.Kind)
			}
			if placedRow(s) {
				return n, "", "", false, nil
			}
			for i := n - 2; i >= 0; i-- {
				m := rows.Steps[i]
				if moveRow(m) && placedRow(m) {
					return i + 1, "", "", false, nil
				}
			}
			return 0, "", "", false, fmt.Errorf("--place --from step %d: step %d is a pull row without a point and no move row stands before it", n, n)
		}
		if !placedRow(s) {
			return 0, "", "", false, fmt.Errorf("--place --from step %d: step %d is a %s row; place at a move row before it", n, n, s.Kind)
		}
		if k, ok := eventStep(rows, n-1); ok {
			return 0, "", "", false, fmt.Errorf("--place --from step %d: the start would fall into the event at step %d", n, k)
		}
		return n, "", "", false, nil
	case "leg":
		leg, name, err := findLeg(rows, from[2])
		if err != nil {
			return 0, "", "", false, fmt.Errorf("--place --from leg %s: %v", from[2], err)
		}
		// The first leg is a door start: the walk begins at step 1 from the instance entrance exactly as a
		// non-placed run does, so no quiet row is needed and the backward search below never runs for it.
		if len(rows.Steps) > 0 && rows.Steps[0].Leg == leg {
			return 1, name, "", true, nil
		}
		lo, hi := legSpan(rows, leg)
		first := -1
		for i := lo; i >= 0 && i <= hi; i++ {
			if creatureRow(rows.Steps[i]) {
				first = i
				break
			}
		}
		if first < 0 {
			return 0, "", "", false, fmt.Errorf("--place --from leg %s: the leg has no pull, boss or hold row", name)
		}
		for i := first - 1; i >= lo && i >= 0; i-- {
			s := rows.Steps[i]
			if !moveRow(s) || !placedRow(s) {
				continue
			}
			if aheadCreatureWithin(rows, i, PlaceStartClearYd) {
				continue
			}
			if k, ok := eventStep(rows, i); ok {
				return 0, "", "", false, fmt.Errorf("--place --from leg %s: the start would fall into the event at step %d", name, k)
			}
			return i + 1, name, "", false, nil
		}
		// No quiet row in the leg itself: fall back to the LAST quiet move row before that point, searching
		// backward through the earlier legs. An event row refuses like in the leg itself (the server would move the
		// start back out of it), it is not skipped.
		for i := lo - 1; i >= 0; i-- {
			s := rows.Steps[i]
			if !moveRow(s) || !placedRow(s) {
				continue
			}
			if aheadCreatureWithin(rows, i, PlaceStartClearYd) {
				continue
			}
			if k, ok := eventStep(rows, i); ok {
				return 0, "", "", false, fmt.Errorf("--place --from leg %s: the start would fall into the event at step %d", name, k)
			}
			// The raid walks from the fallback start to the target leg: a boss or hold row between the two would be
			// re-fought, and a boss until would resolve to it. Refuse with the old exit-2 error, naming why.
			for j := i + 1; j < lo; j++ {
				if bs := rows.Steps[j]; bs.Kind == planexport.KindBoss || bs.Kind == planexport.KindHold {
					return 0, "", "", false, fmt.Errorf("--place --from leg %s: leg %s has no quiet move row before its first pull: pass --from \"step N\" (the fallback start at step %d (leg %s) stands before the %s row at step %d (leg %s); the raid would re-fight it)", name, name, i+1, startLegName(rows, s.Leg), bs.Kind, j+1, startLegName(rows, bs.Leg))
				}
			}
			return i + 1, startLegName(rows, s.Leg), name, false, nil
		}
		return 0, "", "", false, fmt.Errorf("--place --from leg %s: leg %s has no quiet move row before its first pull: pass --from \"step N\"", name, name)
	}
	return 0, "", "", false, fmt.Errorf("--place: --from wants \"leg L\" or \"step N\", not %q", strings.Join(from, " "))
}

// startLegName is the leg a fallback start row belongs to, as the messages name it.
func startLegName(rows planexport.Rows, leg int) string {
	for _, l := range rows.Legs {
		if l.Leg == leg {
			return legName(l)
		}
	}
	return strconv.Itoa(leg)
}

// PlaceUntilBase is the step a boss or boss-done until is resolved from when the start fell back to an earlier leg:
// the TARGET leg's first row. Resolving from the fallback start would answer the earlier leg's boss, which the raid
// meets on its way to the target leg (PlaceStart refuses the fallback when a boss or hold row lies between, but the
// base stays the target leg's regardless).
func PlaceUntilBase(rows planexport.Rows, targetLeg string, start int) int {
	if targetLeg == "" {
		return start
	}
	leg, _, err := findLeg(rows, targetLeg)
	if err != nil {
		return start
	}
	lo, _ := legSpan(rows, leg)
	if lo < 0 {
		return start
	}
	return lo + 1
}

// FromStartStep is the step a --from clause starts the run leader at without --place (spec 5.2): N for "step N", the
// leg's first row for "leg L", and 1 with no clause. It is the step `--until boss-done` is resolved from.
func FromStartStep(rows planexport.Rows, from []string) (int, error) {
	if len(from) == 0 {
		return 1, nil
	}
	if len(from) != 3 || from[0] != "from" {
		return 0, errors.New("place: the start needs the words of --from (ParseFrom)")
	}
	switch from[1] {
	case "step":
		n, err := strconv.Atoi(from[2])
		if err != nil || n < 1 || n > len(rows.Steps) {
			return 0, fmt.Errorf("--from step %s: the rows have %d steps", from[2], len(rows.Steps))
		}
		return n, nil
	case "leg":
		leg, name, err := findLeg(rows, from[2])
		if err != nil {
			return 0, fmt.Errorf("--from leg %s: %v", from[2], err)
		}
		lo, _ := legSpan(rows, leg)
		if lo < 0 {
			return 0, fmt.Errorf("--from leg %s: the leg has no row", name)
		}
		return lo + 1, nil
	}
	return 0, fmt.Errorf("--from wants \"leg L\" or \"step N\", not %q", strings.Join(from, " "))
}

// PlaceStop is the row the corridor's route ends at (spec 4.3): "until step N" is N, "until leg L" the leg's first row,
// "until boss" the first boss row at or after the start, and no clause the last row. A stop before the start step is
// refused: the corridor would be empty and the placed run would end at once (review round 1, item 8).
func PlaceStop(rows planexport.Rows, until []string, start int) (int, error) {
	stop, err := placeStopRow(rows, until, start)
	if err != nil {
		return 0, err
	}
	if stop < start {
		return 0, fmt.Errorf("place: the until clause stops at step %d, before the start step %d", stop, start)
	}
	return stop, nil
}

// placeStopRow reads the until clause (PlaceStop's own work, before the "before the start" refusal).
func placeStopRow(rows planexport.Rows, until []string, start int) (int, error) {
	if len(until) == 0 {
		return len(rows.Steps), nil
	}
	if len(until) != 2 && len(until) != 3 {
		return 0, fmt.Errorf("place: the until clause is %q", strings.Join(until, " "))
	}
	if until[0] != "until" {
		return 0, fmt.Errorf("place: the until clause is %q", strings.Join(until, " "))
	}
	switch until[1] {
	case "step":
		n, err := strconv.Atoi(until[2])
		if err != nil || n < 1 || n > len(rows.Steps) {
			return 0, fmt.Errorf("--until step %s: the rows have %d steps", until[2], len(rows.Steps))
		}
		return n, nil
	case "leg":
		leg, name, err := findLeg(rows, until[2])
		if err != nil {
			return 0, fmt.Errorf("--until leg %s: %v", until[2], err)
		}
		lo, _ := legSpan(rows, leg)
		if lo < 0 {
			return 0, fmt.Errorf("--until leg %s: the leg has no row", name)
		}
		return lo + 1, nil
	case "boss":
		for i := start - 1; i >= 0 && i < len(rows.Steps); i++ {
			if rows.Steps[i].Kind == planexport.KindBoss {
				return i + 1, nil
			}
		}
		return 0, errors.New("--place: no boss row at or after the start step")
	}
	return 0, fmt.Errorf("place: the until clause is %q", strings.Join(until, " "))
}

// PlacePoint is the point of a step (false when the row carries none).
func PlacePoint(rows planexport.Rows, step int) (dungeon.Point, bool) {
	if step < 1 || step > len(rows.Steps) {
		return dungeon.Point{}, false
	}
	s := rows.Steps[step-1]
	if !placedRow(s) {
		return dungeon.Point{}, false
	}
	return dungeon.Point{X: s.X, Y: s.Y, Z: s.Z}, true
}

// routePoints is the route of the corridor: the positioned rows from start to stop, in order (spec 4.3).
func routePoints(rows planexport.Rows, start, stop int) []dungeon.Point {
	if start < 1 {
		start = 1
	}
	if stop > len(rows.Steps) {
		stop = len(rows.Steps)
	}
	var out []dungeon.Point
	for i := start - 1; i < stop; i++ {
		s := rows.Steps[i]
		if !placedRow(s) {
			continue
		}
		out = append(out, dungeon.Point{X: s.X, Y: s.Y, Z: s.Z})
	}
	return out
}

// routeDistance is the 2D distance from a point to the route polyline, and the route's z at the closest point.
func routeDistance(route []dungeon.Point, x, y float32) (d float64, z float64) {
	if len(route) == 0 {
		return math.Inf(1), 0
	}
	if len(route) == 1 {
		p := route[0]
		return dist2f(x, y, p.X, p.Y), float64(p.Z)
	}
	best := math.Inf(1)
	for i := 0; i+1 < len(route); i++ {
		a, b := route[i], route[i+1]
		ax, ay := float64(a.X), float64(a.Y)
		dx, dy := float64(b.X)-ax, float64(b.Y)-ay
		t := 0.0
		if l2 := dx*dx + dy*dy; l2 > 0 {
			t = ((float64(x)-ax)*dx + (float64(y)-ay)*dy) / l2
			if t < 0 {
				t = 0
			} else if t > 1 {
				t = 1
			}
		}
		px, py := ax+t*dx, ay+t*dy
		d := math.Hypot(float64(x)-px, float64(y)-py)
		if d < best {
			best = d
			z = float64(a.Z) + t*(float64(b.Z)-float64(a.Z))
		}
	}
	return best, z
}

// routeSteps is the step numbers of the positioned rows from start to stop, in order (the same filter as routePoints).
func routeSteps(rows planexport.Rows, start, stop int) []int {
	if start < 1 {
		start = 1
	}
	if stop > len(rows.Steps) {
		stop = len(rows.Steps)
	}
	var out []int
	for i := start - 1; i < stop; i++ {
		if !placedRow(rows.Steps[i]) {
			continue
		}
		out = append(out, i+1)
	}
	return out
}

// startPointGuard says whether the start point itself stands within the boss guard of a BEHIND boss or hold row (the
// test PlaceBossGuard and PlaceClear make for a row of an EARLIER leg than the start): the 2-D distance minus the row's
// radius <= PlaceBossGuardYd and dz <= PlaceCorridorZ. The distance is returned for the refusal text.
func startPointGuard(rows planexport.Rows, start int, s planexport.StepRow) (d float64, near bool) {
	sp := rows.Steps[start-1]
	d = dist2f(sp.X, sp.Y, s.X, s.Y)
	return d, d-float64(s.Radius) <= PlaceBossGuardYd && math.Abs(float64(s.Z)-float64(sp.Z)) <= PlaceCorridorZ
}

// earlierLegGuard is the PlaceClear guard for a BEHIND boss or hold row of an EARLIER leg than the start: a placement
// teleports the raid to the start point, so the route that leads to the start leg (and the start leg's own walk) is not
// tested; only the start point itself and the segments that end in a later leg are (PlaceBossGuard reports the step to
// stop before those). Rows of the start leg itself keep the whole route.
func earlierLegGuard(rows planexport.Rows, start, stop int, s planexport.StepRow) (d float64, near bool) {
	if d, near = startPointGuard(rows, start, s); near {
		return d, true
	}
	startLeg := rows.Steps[start-1].Leg
	steps := routeSteps(rows, start, stop)
	for i := 0; i+1 < len(steps); i++ {
		a, b := rows.Steps[steps[i]-1], rows.Steps[steps[i+1]-1]
		if b.Leg == startLeg {
			continue
		}
		seg := []dungeon.Point{{X: a.X, Y: a.Y, Z: a.Z}, {X: b.X, Y: b.Y, Z: b.Z}}
		dd, z := routeDistance(seg, s.X, s.Y)
		if dd-float64(s.Radius) <= PlaceBossGuardYd && math.Abs(float64(s.Z)-z) <= PlaceCorridorZ {
			return dd, true
		}
	}
	return d, false
}

// PlaceBossGuard walks the route from start to stop segment by segment against every BEHIND boss or hold row (the same
// test as PlaceClear's guard: route distance minus the row's radius <= PlaceBossGuardYd and dz <= PlaceCorridorZ).
// A placed run is a fresh copy, so those bosses are alive. For the first segment a -> b that comes within the guard:
//   - b in the start step's own leg (or a == start): refusal, the message PlaceClear gives today;
//   - b in a later leg: until = b (`until step b` stops before b, so the run never walks a -> b), routeEnd = a (the
//     corridor's route end), and a note naming the row, its leg, the distance and the two steps. When b is the stop
//     itself, until == stop: the caller must still shorten, so hit says a segment came within the guard at all.
//
// With no segment within the guard: until = routeEnd = stop, no note, hit false.
func PlaceBossGuard(rows planexport.Rows, start, stop int) (until, routeEnd int, note, refusal string, hit bool) {
	until, routeEnd = stop, stop
	if start < 1 {
		return until, routeEnd, "", "", false
	}
	steps := routeSteps(rows, start, stop)
	if len(steps) < 2 {
		return until, routeEnd, "", "", false
	}
	startLeg := rows.Steps[start-1].Leg
	// A BEHIND boss or hold row of an EARLIER leg refuses the placement only when the start point itself stands within
	// the guard of it (the raid is teleported there, so the walk that led there does not matter). The segments of the
	// start leg's own route are not tested against it (see earlierLegGuard); the segments of later legs still are.
	for j := 0; j < start-1 && j < len(rows.Steps); j++ {
		s := rows.Steps[j]
		if (s.Kind == planexport.KindBoss || s.Kind == planexport.KindHold) && placedRow(s) && s.Leg != startLeg {
			if d, near := startPointGuard(rows, start, s); near {
				return stop, stop, "", fmt.Sprintf("boss/event row %d of leg %d is within %.0f yd of the route: start later or walk from the door", j+1, s.Leg, d), true
			}
		}
	}
	for i := 0; i+1 < len(steps); i++ {
		a, b := rows.Steps[steps[i]-1], rows.Steps[steps[i+1]-1]
		seg := []dungeon.Point{{X: a.X, Y: a.Y, Z: a.Z}, {X: b.X, Y: b.Y, Z: b.Z}}
		for j := 0; j < start-1 && j < len(rows.Steps); j++ {
			s := rows.Steps[j]
			if !creatureRow(s) || !placedRow(s) {
				continue
			}
			if s.Kind != planexport.KindBoss && s.Kind != planexport.KindHold {
				continue
			}
			if s.Leg != startLeg && b.Leg == startLeg {
				continue // an earlier leg's row against the start leg's own route: the start point was tested above
			}
			d, z := routeDistance(seg, s.X, s.Y)
			if d-float64(s.Radius) > PlaceBossGuardYd || math.Abs(float64(s.Z)-z) > PlaceCorridorZ {
				continue
			}
			if steps[i] == start || b.Leg == startLeg {
				return stop, stop, "", fmt.Sprintf("boss/event row %d of leg %d is within %.0f yd of the route: start later or walk from the door", j+1, s.Leg, d), true
			}
			kind := "boss"
			if s.Kind == planexport.KindHold {
				kind = "hold"
			}
			note = fmt.Sprintf("the run stops before step %d (until step %d): the route from step %d to %d passes %.0f yd from BEHIND %s row %d of leg %d, which a placed run leaves alive",
				steps[i+1], steps[i+1], steps[i], steps[i+1], d, kind, j+1, s.Leg)
			return steps[i+1], steps[i], note, "", true
		}
	}
	return until, routeEnd, "", "", false
}

// circleRadius is a BEHIND pull row's circle radius (spec 4.3).
func circleRadius(row float32) float32 {
	r := row + PlaceCircleSlack
	if r > PlaceCircleMax {
		r = PlaceCircleMax
	}
	return r
}

// unitPos is one member position of a plan pull group.
type unitPos struct {
	Entry   uint32
	X, Y, Z float32
}

// planLegOf is the plan leg a row's leg belongs to. The rows' leg numbers are renumbered when an optional leg is left
// out of the export (planexport's outLeg counts only the legs it writes), so the plan leg is found by the rows leg's
// NAME, never by the number; the number is used only when the rows carry no leg row (a synthetic fixture) or its name
// is empty (review round 1, item 4).
func planLegOf(plan *dungeon.Plan, rows planexport.Rows, leg int) *dungeon.Leg {
	if plan == nil {
		return nil
	}
	name, found := "", false
	for _, l := range rows.Legs {
		if l.Leg == leg {
			name, found = l.Name, true
			break
		}
	}
	if found && name != "" {
		for i := range plan.Legs {
			if plan.Legs[i].Name == name {
				return &plan.Legs[i]
			}
		}
		return nil
	}
	if leg < 0 || leg >= len(plan.Legs) {
		return nil
	}
	return &plan.Legs[leg]
}

// groupPositions is the plan's member positions of the row's pull group (spec 4.3): among the groups of the row's leg
// whose entries meet the row's, the one whose positions stand nearest the row (a leg has several groups of the same
// trash entries), each position with the entry of its index.
func groupPositions(plan *dungeon.Plan, rows planexport.Rows, s planexport.StepRow) []unitPos {
	leg := planLegOf(plan, rows, s.Leg)
	if leg == nil {
		return nil
	}
	var bestG *dungeon.PullGroup
	bestD := 0.0
	for i := range leg.PullGroups {
		g := leg.PullGroups[i]
		if len(g.Positions) == 0 || !entriesMeet(g.Entries, s.Entries) {
			continue
		}
		var mx, my float32
		for _, p := range g.Positions {
			mx += p.X
			my += p.Y
		}
		n := float32(len(g.Positions))
		if d := dist2f(s.X, s.Y, mx/n, my/n); bestG == nil || d < bestD {
			bestD, bestG = d, &g
		}
	}
	if bestG == nil {
		return nil
	}
	out := make([]unitPos, 0, len(bestG.Positions))
	for i, p := range bestG.Positions {
		e := uint32(0)
		if i < len(bestG.Entries) {
			e = bestG.Entries[i]
		} else if len(bestG.Entries) > 0 {
			e = bestG.Entries[0]
		}
		out = append(out, unitPos{Entry: e, X: p.X, Y: p.Y, Z: p.Z})
	}
	return out
}

// entriesMeet says whether two entry lists share one entry.
func entriesMeet(a, b []uint32) bool {
	for _, x := range a {
		for _, y := range b {
			if x == y {
				return true
			}
		}
	}
	return false
}

// aheadRow is one creature row at or after the start, for the overlap test.
type aheadRow struct {
	step int
	s    planexport.StepRow
}

// aheadHit is the AHEAD row whose circle (radius + PlaceAheadSlack, within placeAheadDz) overlaps a circle (0: none).
func aheadHit(ahead []aheadRow, x, y, z, r float32) (int, bool) {
	for _, a := range ahead {
		if math.Abs(float64(a.s.Z)-float64(z)) > placeAheadDz {
			continue
		}
		if dist2f(x, y, a.s.X, a.s.Y) <= float64(r)+float64(a.s.Radius)+PlaceAheadSlack {
			return a.step, true
		}
	}
	return 0, false
}

// aheadInside is the AHEAD row a point lies in (its circle is radius + PlaceAheadSlack): a unit of a split row that
// stands in one is not cleared at all (spec 4.3).
func aheadInside(ahead []aheadRow, x, y, z float32) (int, bool) {
	for _, a := range ahead {
		if math.Abs(float64(a.s.Z)-float64(z)) > placeAheadDz {
			continue
		}
		if dist2f(x, y, a.s.X, a.s.Y) <= float64(a.s.Radius)+PlaceAheadSlack {
			return a.step, true
		}
	}
	return 0, false
}

// placeAheadDz is the vertical window of the overlap test (spec 4.3).
const placeAheadDz = 8

// PlaceClear is the corridor clear plus the M1 landing sweep (spec 4.3, PLACE-W26 §3). One circle per BEHIND pull
// row whose circle comes within PlaceCorridorYd of the route from start to stop and within PlaceCorridorZ of the
// route's height; then one circle per BEHIND pull row near the start point (within PlaceSweepYd 2-D and PlaceSweepZ
// in z) that the corridor did not already take. Boss and hold rows are never swept, and the sweep adds no refusal
// (the boss guard stays route-based). A row whose circle overlaps an AHEAD creature row's is split into per-unit
// circles (PlaceUnitYd), and a unit that still lies inside the AHEAD circle is dropped and named. A BEHIND boss or
// hold row within PlaceBossGuardYd of the route refuses the placement: no `boss` flag is ever sent.
func PlaceClear(rows planexport.Rows, plan *dungeon.Plan, start, stop int) ClearPlan {
	var out ClearPlan
	route := routePoints(rows, start, stop)
	if len(route) == 0 {
		return out
	}
	// Every BEHIND pull row, whether or not its circle reaches the corridor: the spawn-home ownership test reads the
	// row's own area and entries, not only the corridor's circles.
	for i := 0; i < start-1 && i < len(rows.Steps); i++ {
		s := rows.Steps[i]
		if s.Kind != planexport.KindPull || !placedRow(s) {
			continue
		}
		out.Behind = append(out.Behind, BehindRow{Step: i + 1, X: s.X, Y: s.Y, Z: s.Z, Radius: s.Radius,
			Entries: append([]uint32{}, s.Entries...)})
	}
	// The AHEAD set is every creature row from the start step to the LAST row of the plan, not to the until stop: a
	// BEHIND circle may overlap a pack of a later leg that the run reaches after the stop, and the split must see it
	// (review round 1, item 6).
	var ahead []aheadRow
	for i := start - 1; i < len(rows.Steps); i++ {
		if creatureRow(rows.Steps[i]) && placedRow(rows.Steps[i]) {
			ahead = append(ahead, aheadRow{step: i + 1, s: rows.Steps[i]})
			out.Ahead = append(out.Ahead, AheadCircle{Step: i + 1, X: rows.Steps[i].X, Y: rows.Steps[i].Y, Z: rows.Steps[i].Z,
				Radius: rows.Steps[i].Radius, Entries: append([]uint32{}, rows.Steps[i].Entries...)})
		}
	}
	circled := map[int]bool{} // steps already the source of a circle: the sweep never doubles a corridor row
	for i := 0; i < start-1 && i < len(rows.Steps); i++ {
		s := rows.Steps[i]
		if !creatureRow(s) || !placedRow(s) {
			continue
		}
		d, z := routeDistance(route, s.X, s.Y)
		dz := math.Abs(float64(s.Z) - z)
		if s.Kind == planexport.KindBoss || s.Kind == planexport.KindHold {
			near := d-float64(s.Radius) <= PlaceBossGuardYd && dz <= PlaceCorridorZ
			if s.Leg != rows.Steps[start-1].Leg {
				// An earlier leg's row: only the start point and the later legs' segments count (earlierLegGuard).
				d, near = earlierLegGuard(rows, start, stop, s)
			}
			if near {
				out.Refusal = fmt.Sprintf("boss/event row %d of leg %d is within %.0f yd of the route: start later or walk from the door", i+1, s.Leg, d)
				out.Circles, out.Notes = nil, nil
				return out
			}
			continue
		}
		if d-float64(s.Radius) > PlaceCorridorYd || dz > PlaceCorridorZ {
			continue
		}
		behindCircles(&out, plan, rows, ahead, i, s, circleRadius(s.Radius), false)
		circled[i+1] = true
	}
	// M1 landing sweep: BEHIND pull rows near the start point, ascending by step. The corridor's way, with sweep
	// slack on the radius and Sweep set. The start point is rows.Steps[start-1].
	if start-1 >= 0 && start-1 < len(rows.Steps) {
		sp := rows.Steps[start-1]
		for i := 0; i < start-1 && i < len(rows.Steps); i++ {
			s := rows.Steps[i]
			if s.Kind != planexport.KindPull || !placedRow(s) || circled[i+1] {
				continue
			}
			if dist2f(s.X, s.Y, sp.X, sp.Y) > PlaceSweepYd || math.Abs(float64(s.Z)-float64(sp.Z)) > PlaceSweepZ {
				continue
			}
			r := s.Radius + PlaceCircleSlack + PlaceSweepSlack
			if r > PlaceCircleMax {
				r = PlaceCircleMax
			}
			behindCircles(&out, plan, rows, ahead, i, s, r, true)
		}
	}
	if len(out.Circles) > PlaceMaxCircles {
		out.Refusal = fmt.Sprintf("the route passes %d packs of earlier legs: use --until to shorten it", len(out.Circles))
		out.Circles = nil
	}
	return out
}

// behindCircles appends the clear's circles for one BEHIND row i (spec 4.3, PLACE-W26 M1): the row's circle at radius
// r, or, when it overlaps an AHEAD row's, one per-unit circle (PlaceUnitYd) per member of the plan's pull group, with
// the same notes. Sweep marks circles the M1 landing sweep added.
func behindCircles(out *ClearPlan, plan *dungeon.Plan, rows planexport.Rows, ahead []aheadRow, i int, s planexport.StepRow, r float32, sweep bool) {
	if hit, ok := aheadHit(ahead, s.X, s.Y, s.Z, r); ok {
		units := groupPositions(plan, rows, s)
		if len(units) == 0 {
			out.Notes = append(out.Notes, fmt.Sprintf("not cleared: row %d has no pull group positions and its circle overlaps AHEAD row %d", i+1, hit))
			return
		}
		for _, u := range units {
			if k, inside := aheadInside(ahead, u.X, u.Y, u.Z); inside {
				out.Notes = append(out.Notes, fmt.Sprintf("not cleared: unit %d at %.0f %.0f %.0f lies in AHEAD row %d", u.Entry, u.X, u.Y, u.Z, k))
				continue
			}
			out.Circles = append(out.Circles, ClearCircle{Step: i + 1, Leg: s.Leg, X: u.X, Y: u.Y, Z: u.Z,
				Radius: PlaceUnitYd, Entries: []uint32{u.Entry}, PerUnit: true, Unit: u.Entry, Sweep: sweep})
		}
		return
	}
	out.Circles = append(out.Circles, ClearCircle{Step: i + 1, Leg: s.Leg, X: s.X, Y: s.Y, Z: s.Z,
		Radius: r, Entries: append([]uint32{}, s.Entries...), Sweep: sweep})
}

// PlaceSkips is the union of every leg's skip list, sorted (spec 4.3): the entries the toolkit's quiet and clearto
// never count and never touch.
func PlaceSkips(plan *dungeon.Plan) []uint32 {
	if plan == nil {
		return nil
	}
	seen := map[uint32]bool{}
	var out []uint32
	for _, l := range plan.Legs {
		for _, e := range l.Skip {
			if seen[e] {
				continue
			}
			seen[e] = true
			out = append(out, e)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}

// SkipWords is a skip list as the toolkit's `skip=` argument ("" for none).
func SkipWords(ids []uint32) string {
	if len(ids) == 0 {
		return ""
	}
	parts := make([]string, len(ids))
	for i, e := range ids {
		parts[i] = strconv.FormatUint(uint64(e), 10)
	}
	return strings.Join(parts, ",")
}

// dist2f is the 2D distance between two points.
func dist2f(ax, ay, bx, by float32) float64 {
	dx, dy := float64(ax-bx), float64(ay-by)
	return math.Sqrt(dx*dx + dy*dy)
}
