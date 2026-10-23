package dungeon

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/pjerra/turtle-agent/internal/eluna"
)

// The targeted start placement of an observed run (settings eluna_toolkit): instead of leaving the party wherever it stood and
// checking the distance to the start step, the agent puts it there with the Eluna test toolkit, after proving the place is the
// step's own floor and that nothing stands near. The toolkit is TEST-server-only Lua (TortoiseBots tools/eluna/test-toolkit); a
// server without it answers nothing, and the run is refused rather than started untested. Off unless the setting is on.

// ElunaToolkit is the optional part of a RemoteGame that speaks the toolkit's `.tt` commands (cmd/turtle-agent implements it over
// eluna.Client). The points are on the plan's map.
type ElunaToolkit interface {
	// TTFloor is `.tt q floor`: the floor a caller's z searches at a point.
	TTFloor(ctx context.Context, p Point) (eluna.FloorResult, error)
	// TTQuiet is `.tt quiet`: whether the area stays clear for wait, and what stands near or roams.
	TTQuiet(ctx context.Context, p Point, radius float32, wait time.Duration) (eluna.QuietResult, error)
	// TTPlace is `.tt place ... of=<of>`: the party in a ring of spread yards around the point, verified. of names the
	// player whose group is placed (a commander bound with `.bot commander bind` is not in the bots' group); empty: the
	// caller's own group.
	TTPlace(ctx context.Context, p Point, spread float32, of string) (eluna.PlaceResult, error)
}

const (
	// elunaRise is how far above the start step's point the agent's character stands for the floor query: the toolkit searches the
	// floor downward from the caller, so a point slightly above the step finds it, and a point under terrain does not.
	elunaRise = 2
	// elunaSettle is the wait after `.go xyz`, before the toolkit is asked (the teleport has to land).
	elunaSettle = 2 * time.Second
	// elunaFloorOff is how far the step's floor may lie from the plan's z: farther is another level (a deck, a roof) and the place
	// is not the step's.
	elunaFloorOff = 3
	// elunaQuietRadius and elunaQuietWait are the area's size and how long it must stay clear: a patrol that walks through in 20 s
	// is not a pack waiting at the step.
	elunaQuietRadius = 50
	elunaQuietWait   = 20 * time.Second
	// elunaSpread is the ring's radius: the bots stand within a few yards of the step, not on it.
	elunaSpread = 3
	// elunaDriftYd is the distance from its ring spot past which a placed member is noted as drifted: the toolkit's own old
	// rule (5 yd) is what bots break while walking a few steps after the teleport, on the floor all the same.
	elunaDriftYd = 5
)

// elunaPlace is the start placement of an observed run, right before checkStartPlace: the agent moves to the start step's point,
// proves the floor under it and that the area is clear, holds the bots, puts them at the step and releases them (the run leader
// starts at once). Any refused step ends the run there: an error starting "observe mode: eluna place:" names the step. It does
// nothing unless ElunaPlace is on and the run starts partway at a known point.
func (d *RemoteDriver) elunaPlace(ctx context.Context) error {
	if !d.opt.ElunaPlace || d.start == nil || d.start.Words == "" || !d.start.HavePoint {
		return nil
	}
	tk, okToolkit := d.g.(ElunaToolkit)
	hands, okHands := d.g.(StartHands)
	if !okToolkit || !okHands {
		return errors.New("observe mode: eluna place: eluna_toolkit is on but this game has no Eluna client")
	}
	p := d.start.Point

	// 1. The agent stands above the step: the floor query searches from the caller's z.
	if err := hands.GoXYZ(ctx, d.plan.Map, Point{X: p.X, Y: p.Y, Z: p.Z + elunaRise}); err != nil {
		return fmt.Errorf("observe mode: eluna place: .go xyz to the start step: %w", err)
	}
	if err := d.clk.Sleep(ctx, elunaSettle); err != nil {
		return err
	}

	// 2. The place is the step's own floor, not another level's.
	f, err := tk.TTFloor(ctx, p)
	if err != nil {
		return fmt.Errorf("observe mode: eluna place: q floor: %w", err)
	}
	if f.Floor == nil {
		return fmt.Errorf("observe mode: eluna place: no floor at the start step %.2f %.2f %.2f", p.X, p.Y, p.Z)
	}
	if off := absDiff(*f.Floor, p.Z); off > elunaFloorOff {
		return fmt.Errorf("observe mode: eluna place: the floor at the start step is %.2f, %.2f yd from its z %.2f (more than %d: another level)", *f.Floor, off, p.Z, elunaFloorOff)
	}

	// 3. Nothing waits at the step. There is no clearto here (the owner's decision): a pack in the way refuses the run.
	q, err := tk.TTQuiet(ctx, p, elunaQuietRadius, elunaQuietWait)
	if err != nil {
		return fmt.Errorf("observe mode: eluna place: quiet: %w", err)
	}
	if !q.Quiet {
		msg := "the area is not quiet: " + describeQuiet(q)
		d.rep.note("eluna place: " + msg)
		return errors.New("observe mode: eluna place: " + msg)
	}

	// 4. The bots hold where they are, so that they do not walk the ring off again while it is placed.
	if _, err := d.g.Command(ctx, "action stay"); err != nil {
		return fmt.Errorf("observe mode: eluna place: action stay: %w", err)
	}

	// 5. The party is put at the step, the toolkit's second read proving it. The group is the lead bot's: a bound
	// commander is not in it, so `of=` names the lead bot whenever a binding stands.
	of := ""
	if d.bound {
		of = d.opt.Lead
	}
	pl, err := tk.TTPlace(ctx, p, elunaSpread, of)
	if err != nil {
		return fmt.Errorf("observe mode: eluna place: place: %w", err)
	}
	// The floor is the test: the bots teleport and then drift a few yards, still on the floor, so a member's distance
	// from its ring spot is only noted. A missing member (why) or a member off the floor fails the placement.
	if !pl.OnFloor() {
		return fmt.Errorf("observe mode: eluna place: place did not verify: %s", describePlaced(pl))
	}
	drifted := describeDrift(pl.Members)

	// 6. Follow is released right before the run leader starts and walks. Nothing waits after the place: a bound party
	// with no run going wanders off within about a minute, so the start follows at once.
	if _, err := d.g.Command(ctx, "action follow"); err != nil {
		return fmt.Errorf("observe mode: eluna place: action follow: %w", err)
	}
	note := fmt.Sprintf("eluna place: quiet, %d placed at %.1f %.1f %.1f", len(pl.Members), p.X, p.Y, p.Z)
	if drifted != "" {
		note += " (drifted: " + drifted + ")"
	}
	d.rep.note(note)
	return nil
}

// absDiff is |a-b|.
func absDiff(a, b float32) float32 {
	if a < b {
		return b - a
	}
	return a - b
}

// describeQuiet lists what keeps an area from being quiet: the units near it and the ones that roam, each with its entry and distance.
func describeQuiet(q eluna.QuietResult) string {
	var parts []string
	for _, u := range q.Near {
		parts = append(parts, fmt.Sprintf("%d %s %.0f yd", u.Entry, nonEmpty(u.Name, u.GUID), u.Dist))
	}
	for _, r := range q.Roamers {
		parts = append(parts, fmt.Sprintf("%d %s (%s, %.0f yd)", r.Entry, r.GUID, nonEmpty(r.Why, "roaming"), r.Dist))
	}
	if len(parts) == 0 {
		return "the toolkit listed nothing (a poll saw movement)"
	}
	return strings.Join(parts, ", ")
}

// describePlaced lists the members the place's second read did not accept: the ones it could not find and the ones off the
// floor (|dz| beyond PlaceDzTolerance). The distance `d` is not a reason: the start path judges by the floor.
func describePlaced(pl eluna.PlaceResult) string {
	var parts []string
	for _, m := range pl.Members {
		switch {
		case m.Why != "":
			parts = append(parts, fmt.Sprintf("%s %s", nonEmpty(m.Name, m.GUID), m.Why))
		case absDiff(m.Dz, 0) > eluna.PlaceDzTolerance:
			parts = append(parts, fmt.Sprintf("%s %.1f yd off the floor, %.1f yd from its spot", nonEmpty(m.Name, m.GUID), m.Dz, m.Dist))
		}
	}
	if len(parts) == 0 {
		return "no member verified"
	}
	return strings.Join(parts, ", ")
}

// describeDrift lists the placed members that stand more than elunaDriftYd from their ring spot: still on the floor, so a
// note, not a refusal.
func describeDrift(ms []eluna.Placed) string {
	var parts []string
	for _, m := range ms {
		if m.Dist > elunaDriftYd {
			parts = append(parts, fmt.Sprintf("%s %.1f yd", nonEmpty(m.Name, m.GUID), m.Dist))
		}
	}
	return strings.Join(parts, ", ")
}
