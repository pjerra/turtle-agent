// The route rules judge consecutive export rows as a walked route: the
// navmesh path between them must exist and stay near the straight line
// (path_ratio, no_path), a climb or drop must go gradually (zjump), the leg
// must not zig-zag over its own steps (backtrack), and no hop may outrun the
// run leader's sight (long_hop). The geometry rules (backtrack, long_hop)
// run without mmaps; the path rules need the offline planner.
package planlint

import (
	"fmt"
	"math"

	"github.com/pjerra/turtle-agent/internal/dungeon"
	"github.com/pjerra/turtle-agent/internal/navmesh"
	"github.com/pjerra/turtle-agent/internal/planexport"
)

// The route rule thresholds, in yards.
const (
	// RouteStraightMinYd is the shortest straight hop the ratio rule judges:
	// shorter hops are the cutter's own corners, not detours.
	RouteStraightMinYd = 15
	// RouteRatioWarn is the path-to-straight ratio that warns (a walk twice
	// the crow flies).
	RouteRatioWarn = 1.5
	// RouteRatioError is the ratio that errors (three times around).
	RouteRatioError = 3
	// RouteZJumpDzYd is the climb or drop that needs a gradual mesh path.
	RouteZJumpDzYd = 6
	// RouteBacktrackYd is how near a row must come to a row 3+ steps back to
	// count as a return; RouteBacktrackGap is the minimum step gap; more than
	// RouteBacktrackMax in any RouteBacktrackWindow rows is a zig-zag.
	RouteBacktrackYd     = 10
	RouteBacktrackGap    = 3
	RouteBacktrackMax    = 2
	RouteBacktrackWindow = 10
)

// The route lints' names.
const (
	LintPathRatio = "path_ratio"
	LintNoPath    = "no_path"
	LintZJump     = "zjump"
	LintBacktrack = "backtrack"
	LintLongHop   = "long_hop"
)

// routeHop is one walked hop: consecutive positioned rows of the same leg,
// measured pairwise like the other hop lints (not from drift origins).
// Pull, boss and hold rows have no walk of their own: the party fights from
// the stand, so the hop ends at the positioned move row before them.
func (c *checker) routeHop(i int) (from, to dungeon.Point, ok bool) {
	s := c.steps[i]
	if !positioned(s) || !routeStop(s) {
		return dungeon.Point{}, dungeon.Point{}, false
	}
	prev := c.prev(i)
	if prev < 0 || c.steps[prev].Leg != s.Leg {
		return dungeon.Point{}, dungeon.Point{}, false
	}
	return pointOf(c.steps[prev]), pointOf(s), true
}

// checkRoutePath runs the path rules over one hop: no_path when the mesh has
// no complete path, path_ratio when it wanders, zjump when it climbs or
// drops steeply. Without the offline planner there is no path to judge.
func (c *checker) checkRoutePath(i int, s planexport.StepRow) []Finding {
	pn, ok := c.nav.(PathNav)
	if !ok {
		return nil
	}
	from, to, ok := c.routeHop(i)
	if !ok {
		return nil
	}
	path, err := pn.Plan(c.opt.Map, v3(from), v3(to))
	if err != nil {
		return nil
	}
	straight := dist3(from, to)
	if path.Status != navmesh.Complete || len(path.Points) == 0 {
		f := c.severityFinding(i, s, LintNoPath, fmt.Sprintf("no complete navmesh path from row %d (%.0f yd straight)", c.prev(i)+1, straight), SeverityError)
		f.From = &from
		return []Finding{f}
	}
	var out []Finding
	if straight >= RouteStraightMinYd {
		if ratio := float64(path.Length) / straight; ratio > RouteRatioError || ratio > RouteRatioWarn {
			sev := SeverityWarning
			if ratio > RouteRatioError {
				sev = SeverityError
			}
			f := c.severityFinding(i, s, LintPathRatio, fmt.Sprintf("navmesh path %.0f yd over %.0f yd straight (ratio %.1f)", float64(path.Length), straight, ratio), sev)
			f.From = &from
			out = append(out, f)
		}
	}
	if dz := float64(to.Z - from.Z); dz >= RouteZJumpDzYd || dz <= -RouteZJumpDzYd {
		if steep, far := steepClimb(path, dz); steep {
			f := c.severityFinding(i, s, LintZJump, fmt.Sprintf("%.1f yd %s with no gradual mesh path (%s)", absf(dz), upDown(dz), far), SeverityError)
			f.From = &from
			out = append(out, f)
		}
	}
	return out
}

// steepClimb says the path gains the hop's dz faster than 1:1 anywhere: the
// steepest corner-to-corner slope. A path the planner cannot lay out (no
// points) counts as steep: there is no gradual climb to walk. far names the
// evidence.
func steepClimb(path navmesh.Path, dz float64) (bool, string) {
	if len(path.Points) < 2 {
		return true, "the path has no corners"
	}
	worst := 0.0
	for k := 1; k < len(path.Points); k++ {
		a, b := point3(path.Points[k-1]), point3(path.Points[k])
		run := dist2(a, b)
		rise := absf(float64(b.Z - a.Z))
		if run == 0 {
			if rise > 0 {
				return true, fmt.Sprintf("corner %d climbs %.1f yd standing still", k, rise)
			}
			continue
		}
		if s := rise / run; s > worst {
			worst = s
		}
	}
	if worst > 1 {
		return true, fmt.Sprintf("steepest mesh slope %.1f:1", worst)
	}
	return false, ""
}

// checkRouteGeometry runs the mmaps-free rules over one hop: long_hop past
// the leader's sight, and backtrack's zig-zag count (one finding per leg,
// attached to the row that completes it).
func (c *checker) checkRouteGeometry(i int, s planexport.StepRow) []Finding {
	from, to, ok := c.routeHop(i)
	if !ok {
		return nil
	}
	var out []Finding
	if d := dist3(from, to); d > SeeYd {
		f := c.severityFinding(i, s, LintLongHop, fmt.Sprintf("%.0f yd straight hop, past the leader's %d yd sight", d, SeeYd), SeverityWarning)
		f.From = &from
		out = append(out, f)
	}
	if n := backtrackReturns(c.steps, i); n > RouteBacktrackMax {
		f := c.severityFinding(i, s, LintBacktrack, fmt.Sprintf("%d returns within %d yd of a row %d+ steps back in %d rows (zig-zag)", n, RouteBacktrackYd, RouteBacktrackGap, RouteBacktrackWindow), SeverityWarning)
		out = append(out, f)
	}
	return out
}

// backtrackReturns counts, in the RouteBacktrackWindow rows ending at i (same
// leg), the rows that stand within RouteBacktrackYd of a row of the same leg
// at least RouteBacktrackGap steps earlier. Only rows the party walks to
// count; a leg boundary ends the window.
func backtrackReturns(steps []planexport.StepRow, i int) int {
	leg := steps[i].Leg
	start := i - RouteBacktrackWindow + 1
	if start < 0 {
		start = 0
	}
	n := 0
	for k := start; k <= i; k++ {
		if steps[k].Leg != leg || !positioned(steps[k]) || !routeStop(steps[k]) {
			continue
		}
		for j := start; j <= k-RouteBacktrackGap; j++ {
			if steps[j].Leg != leg || !positioned(steps[j]) || !routeStop(steps[j]) {
				continue
			}
			if dist3(pointOf(steps[j]), pointOf(steps[k])) <= RouteBacktrackYd {
				n++
				break
			}
		}
	}
	return n
}

// routeStop says the party walks to the row's own point: a move, or a pull
// or boss it walks to (a stand pull is fought from its stand, whose stand
// row carries the walk instead).
func routeStop(s planexport.StepRow) bool {
	if moveKind(s.Kind) {
		return true
	}
	return walkedTo(s)
}

func dist2(a, b dungeon.Point) float64 {
	return math.Hypot(float64(a.X-b.X), float64(a.Y-b.Y))
}

func absf(f float64) float64 {
	if f < 0 {
		return -f
	}
	return f
}

func upDown(dz float64) string {
	if dz > 0 {
		return "up"
	}
	return "down"
}
