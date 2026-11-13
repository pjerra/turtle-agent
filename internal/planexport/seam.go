package planexport

import (
	"math"

	"github.com/pjerra/turtle-agent/internal/dungeon"
)

// SeamCellYd is one cell of the server's mmaps grid. The core builds a 533.3333 yd grid tile of 100 cells a side, and the
// server's PathFinder answers NOPATH for a hop whose goal stands exactly on a cell line: the tile-local coordinate is an
// integer there and the lookup lands off the polygon (route audit 2026-11-12, finding 3: 14 rows in 11 plans, every one with
// x or y an exact multiple of this cell).
const SeamCellYd = 533.3333333 / 100

// SeamNudgeYd is how far OffSeam moves a cut point off a cell line: enough to name the cell on either side, and small enough
// that the row still stands on the walk it cuts.
const SeamNudgeYd = 0.5

// seamTol is how near a cell line counts as on it, in cells: a float32 plan coordinate keeps ~1e-4 yd (426.66653 for the
// cell line at 80 cells), far inside this.
const seamTol = 1e-3

// seamOffYd is how far a seam coordinate must move for the nudge to be worth a row: under it the hop runs along the line, and
// a point off the line on one axis only is fine (plan-probe 2026-11-12: maraudon_purple step 1216 cleared with its x moved
// and its y left on the line).
const seamOffYd = 0.02

// OnSeam says whether v stands on a cell line of the server's mmaps grid.
func OnSeam(v float32) bool {
	q := float64(v) / SeamCellYd
	return math.Abs(q-math.Round(q)) < seamTol
}

// OffSeam returns p moved SeamNudgeYd back along the hop from `from` when its x or its y stands on a cell line: the server
// answers a hop to such a point NOPATH (route audit 2026-11-12, finding 3; plan-probe cleared 13 of the 14 listed rows with
// this move, naxxramas step 637 stayed NOPATH: a steep ledge and not a seam). The move is toward the point the hop comes
// from, so the cut point stays on the walk it cuts a corner of. A point off the lines keeps its coordinates, and so does one
// whose seam axis the hop does not cross: moving it would rewrite the row and leave the seam as it was.
func OffSeam(from, p dungeon.Point) dungeon.Point {
	sx, sy := OnSeam(p.X), OnSeam(p.Y)
	if !sx && !sy {
		return p
	}
	dx, dy := float64(p.X-from.X), float64(p.Y-from.Y)
	d := math.Hypot(dx, dy)
	if d == 0 {
		if sx {
			p.X -= SeamNudgeYd
		}
		if sy {
			p.Y -= SeamNudgeYd
		}
		return p
	}
	mx, my := SeamNudgeYd*dx/d, SeamNudgeYd*dy/d
	if !(sx && math.Abs(mx) >= seamOffYd) && !(sy && math.Abs(my) >= seamOffYd) {
		return p
	}
	p.X = float32(float64(p.X) - mx)
	p.Y = float32(float64(p.Y) - my)
	return p
}
