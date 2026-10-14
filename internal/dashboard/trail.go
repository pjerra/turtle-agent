package dashboard

import "math"

// The trail is where the lead bot has been this run, built from the positions the poller sees (about one per 1.5 s). It is kept in
// memory per agent, thinned as it grows, and sent to the page as a list of [x, y] in the drawing's plane, oldest first.
const (
	// TrailCap is the most points a trail holds.
	TrailCap = 1500
	// trailStep is the least distance (yards) between two kept points.
	trailStep = 3.0
	// trailStraight is how far (yards) the middle of three points may sit off the line through the outer two and still be dropped.
	trailStraight = 0.7
)

// Trail is one agent's walked path.
type Trail struct {
	run string
	pts [][2]float64
}

// Add records a position of the run (any label that changes when a new run starts: its run id or plan). A new run empties the
// trail. A position closer than trailStep to the last is ignored, a point on the straight line between its neighbours is merged into
// the line, and past TrailCap the older half of the trail is thinned to every second point, so that the start of a long run stays.
func (t *Trail) Add(run string, x, y float64) {
	if run != t.run {
		t.run, t.pts = run, nil
	}
	n := len(t.pts)
	if n > 0 && math.Hypot(t.pts[n-1][0]-x, t.pts[n-1][1]-y) < trailStep {
		return
	}
	if n >= 2 && offLine(t.pts[n-2], t.pts[n-1], [2]float64{x, y}) < trailStraight {
		t.pts[n-1] = [2]float64{round1(x), round1(y)}
		return
	}
	t.pts = append(t.pts, [2]float64{round1(x), round1(y)})
	if len(t.pts) > TrailCap {
		half := len(t.pts) / 2
		thinned := make([][2]float64, 0, len(t.pts)-half/2)
		for i := 0; i < half; i += 2 {
			thinned = append(thinned, t.pts[i])
		}
		t.pts = append(thinned, t.pts[half:]...)
	}
}

// Points is a copy of the trail.
func (t *Trail) Points() [][2]float64 {
	if len(t.pts) == 0 {
		return nil
	}
	return append([][2]float64(nil), t.pts...)
}

// offLine is how far b lies from the line through a and c.
func offLine(a, b, c [2]float64) float64 {
	dx, dy := c[0]-a[0], c[1]-a[1]
	l := math.Hypot(dx, dy)
	if l == 0 {
		return math.Hypot(b[0]-a[0], b[1]-a[1])
	}
	return math.Abs(dx*(a[1]-b[1])-(a[0]-b[0])*dy) / l
}
