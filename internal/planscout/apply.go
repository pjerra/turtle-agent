package planscout

import (
	"fmt"
	"sort"

	"github.com/pjerra/turtle-agent/internal/dungeon"
	"github.com/pjerra/turtle-agent/internal/planexport"
	"github.com/pjerra/turtle-agent/internal/routeedit"
)

// ApplyFixes applies the applicable fixes to a copy of the plan (routeedit.Clone), then checks the copy: a fix set that adds a
// plan-check issue is refused as a whole, with nothing returned. The base plan is never changed.
func ApplyFixes(p *dungeon.Plan, fixes []Fix, nav dungeon.Navmesh) (*dungeon.Plan, routeedit.CheckResult, error) {
	edited, err := routeedit.Clone(p)
	if err != nil {
		return nil, routeedit.CheckResult{}, err
	}
	var todo []Fix
	for _, f := range fixes {
		if f.Apply {
			todo = append(todo, f)
		}
	}
	// Highest step first: an insertion at a low index never shifts the element a later fix names.
	sort.SliceStable(todo, func(i, j int) bool { return todo[i].Step > todo[j].Step })
	for _, f := range todo {
		if err := applyFix(edited, f); err != nil {
			return nil, routeedit.CheckResult{}, fmt.Errorf("fix %d (step %d, %s): %w", f.ID, f.Step, f.Kind, err)
		}
	}
	res := routeedit.Evaluate(p, edited, nav)
	if !res.Checked {
		return nil, res, fmt.Errorf("the plan check of the edited plan could not run: %s", res.Skipped)
	}
	if res.NewIssues > 0 {
		return nil, res, fmt.Errorf("the fixes add %d plan-check issue(s): %s", res.NewIssues, res.Refusal())
	}
	return edited, res, nil
}

func applyFix(p *dungeon.Plan, f Fix) error {
	if f.Ref == nil {
		return fmt.Errorf("no plan element to edit")
	}
	ref := *f.Ref
	if ref.Leg < 0 || ref.Leg >= len(p.Legs) {
		return fmt.Errorf("leg %d is outside the plan", ref.Leg)
	}
	switch f.Kind {
	case FixSnapPoint:
		if f.Point == nil {
			return fmt.Errorf("no point")
		}
		return setPoint(p, ref, *f.Point)
	case FixMoveStand:
		if f.Stand == nil {
			return fmt.Errorf("no stand")
		}
		return setStand(p, ref, dungeon.Point{X: f.Stand.X, Y: f.Stand.Y, Z: f.Stand.Z})
	case FixInsertVia, FixDryDetour:
		if f.Point != nil {
			return setPoint(p, ref, *f.Point)
		}
		if len(f.Vias) == 0 {
			return fmt.Errorf("no vias")
		}
		return insertVias(p, ref, f.Vias)
	}
	return fmt.Errorf("fix kind %q is not applied", f.Kind)
}

// element is where a fix writes: the via list it inserts into, or the point it moves.
func insertVias(p *dungeon.Plan, ref planexport.RowOrigin, vias []dungeon.Point) error {
	leg := &p.Legs[ref.Leg]
	add := func(points []dungeon.Point) []dungeon.Point {
		out := make([]dungeon.Point, 0, len(points)+len(vias))
		out = append(out, points...)
		out = append(out, vias...)
		return out
	}
	switch ref.Part {
	case "to":
		for _, v := range vias {
			leg.Via = append(leg.Via, dungeon.Waypoint{Point: v})
		}
		return nil
	case "via":
		if ref.Index < 0 || ref.Index > len(leg.Via) {
			return fmt.Errorf("via index %d is outside the leg", ref.Index)
		}
		var out []dungeon.Waypoint
		out = append(out, leg.Via[:ref.Index]...)
		for _, v := range vias {
			out = append(out, dungeon.Waypoint{Point: v})
		}
		out = append(out, leg.Via[ref.Index:]...)
		leg.Via = out
		return nil
	case "approach":
		if ref.Group < 0 || ref.Group >= len(leg.PullGroups) {
			return fmt.Errorf("pull group %d is outside the leg", ref.Group)
		}
		ap := leg.PullGroups[ref.Group].Approach
		if ref.Index < 0 || ref.Index > len(ap) {
			return fmt.Errorf("approach index %d is outside the group", ref.Index)
		}
		var out []dungeon.Point
		out = append(out, ap[:ref.Index]...)
		out = append(out, vias...)
		out = append(out, ap[ref.Index:]...)
		leg.PullGroups[ref.Group].Approach = out
		return nil
	case "pull", "hold":
		if ref.Group < 0 || ref.Group >= len(leg.PullGroups) {
			return fmt.Errorf("pull group %d is outside the leg", ref.Group)
		}
		leg.PullGroups[ref.Group].Approach = add(leg.PullGroups[ref.Group].Approach)
		return nil
	case "boss":
		if leg.Boss == nil {
			return fmt.Errorf("the leg has no boss")
		}
		leg.Boss.Approach = add(leg.Boss.Approach)
		return nil
	}
	return fmt.Errorf("a %q row has no plan element to insert vias into", ref.Part)
}

// setPoint moves the plan element a row names to pt.
func setPoint(p *dungeon.Plan, ref planexport.RowOrigin, pt dungeon.Point) error {
	leg := &p.Legs[ref.Leg]
	switch ref.Part {
	case "via":
		if ref.Index < 0 || ref.Index >= len(leg.Via) {
			return fmt.Errorf("via index %d is outside the leg", ref.Index)
		}
		if leg.Via[ref.Index].Direct || leg.Via[ref.Index].Exact || leg.Via[ref.Index].CheckSkip != "" {
			return fmt.Errorf("via %d is locked (direct, exact or check_skip): the navmesh cannot judge it", ref.Index)
		}
		leg.Via[ref.Index].Point = pt
		return nil
	case "to":
		leg.To = pt
		return nil
	case "approach":
		if ref.Group >= 0 && ref.Group < len(leg.PullGroups) {
			if ref.Index < 0 || ref.Index >= len(leg.PullGroups[ref.Group].Approach) {
				return fmt.Errorf("approach index %d is outside the group", ref.Index)
			}
			leg.PullGroups[ref.Group].Approach[ref.Index] = pt
			return nil
		}
		if leg.Boss != nil && ref.Index >= 0 && ref.Index < len(leg.Boss.Approach) {
			leg.Boss.Approach[ref.Index] = pt
			return nil
		}
		return fmt.Errorf("no approach point %d to move", ref.Index)
	case "pull":
		if ref.Group < 0 || ref.Group >= len(leg.PullGroups) {
			return fmt.Errorf("pull group %d is outside the leg", ref.Group)
		}
		g := &leg.PullGroups[ref.Group]
		if n := len(g.Approach); n > 0 {
			g.Approach[n-1] = pt
			return nil
		}
		if len(g.Positions) == 0 {
			return fmt.Errorf("pull group %d has no position", ref.Group)
		}
		c := centre(g.Positions)
		dx, dy, dz := pt.X-c.X, pt.Y-c.Y, pt.Z-c.Z
		for i := range g.Positions {
			g.Positions[i].X += dx
			g.Positions[i].Y += dy
			g.Positions[i].Z += dz
		}
		return nil
	case "hold":
		if ref.Group < 0 || ref.Group >= len(leg.PullGroups) || leg.PullGroups[ref.Group].Hold == nil {
			return fmt.Errorf("no hold to move")
		}
		leg.PullGroups[ref.Group].Hold.Spot = pt
		return nil
	case "boss":
		if leg.Boss == nil {
			return fmt.Errorf("the leg has no boss")
		}
		if leg.Boss.StandPull && len(leg.Boss.Approach) > 0 {
			leg.Boss.Approach[len(leg.Boss.Approach)-1] = pt
			return nil
		}
		leg.Boss.Position = pt
		return nil
	case "step":
		// A step's stand (the row a step walks the party to).
		for _, steps := range [][]dungeon.Step{leg.Steps, leg.AfterBoss} {
			for si := range steps {
				st := &steps[si]
				switch {
				case st.UseObject != nil && st.UseObject.Stand != nil:
					*st.UseObject.Stand = pt
					return nil
				case st.LootObject != nil && st.LootObject.Stand != nil:
					*st.LootObject.Stand = pt
					return nil
				case st.AreaTrigger != nil:
					st.AreaTrigger.Stand = pt
					return nil
				}
			}
		}
		return fmt.Errorf("the step has no stand point to move")
	}
	return fmt.Errorf("a %q row has no plan element to move", ref.Part)
}

func setStand(p *dungeon.Plan, ref planexport.RowOrigin, pt dungeon.Point) error {
	leg := &p.Legs[ref.Leg]
	group := ref.Group
	if group < 0 {
		// A boss row.
		if leg.Boss == nil {
			return fmt.Errorf("the leg has no boss")
		}
		if n := len(leg.Boss.Approach); n > 0 {
			leg.Boss.Approach[n-1] = pt
		} else {
			leg.Boss.Approach = []dungeon.Point{pt}
		}
		return nil
	}
	if group >= len(leg.PullGroups) {
		return fmt.Errorf("pull group %d is outside the leg", group)
	}
	g := &leg.PullGroups[group]
	if n := len(g.Approach); n > 0 {
		g.Approach[n-1] = pt
	} else {
		g.Approach = []dungeon.Point{pt}
	}
	return nil
}

func centre(ps []dungeon.Point) dungeon.Point {
	var c dungeon.Point
	for _, p := range ps {
		c.X += p.X
		c.Y += p.Y
		c.Z += p.Z
	}
	n := float32(len(ps))
	return dungeon.Point{X: c.X / n, Y: c.Y / n, Z: c.Z / n}
}
