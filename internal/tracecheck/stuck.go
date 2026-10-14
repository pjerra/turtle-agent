package tracecheck

import (
	"math"
	"sort"

	"github.com/pjerra/turtle-agent/internal/trace"
)

// Stuck detection (the "stuck" check, and the stuck and unstuck events the recorder writes while a run goes): a member that should be
// moving and whose position has not changed for StuckSeconds. A member should be moving when a walk of the plan is going and it is
// alive and out of combat: the lead (it walks) and every other member more than followDist yards from the lead (it follows). The
// positions are the ones for a per-dungeon stuck-spot map.

const (
	// stuckMoved is how far (yd) a member must move to count as having moved.
	stuckMoved = 1.0
	// followDist is how far from the lead a member must be to be expected to follow.
	followDist = 10.0
)

// StuckEvent is the start ("stuck", after the limit) or the end ("unstuck", the member moved again or stopped being expected to
// move) of a stall.
type StuckEvent struct {
	Ev       string
	T        int64
	G        string // the member's guid
	X, Y, Z  float32
	Seconds  float64 // how long the member had stood still at this point of the trace
	Lead     bool
	DistLead float64 // yards to the lead; 0 for the lead
}

// Data are the event's fields as the trace writes them: a is the member's guid; name is added by the caller that knows it.
func (e StuckEvent) Data(name string) map[string]any {
	d := map[string]any{"a": e.G, "x": e.X, "y": e.Y, "z": e.Z, "seconds": math.Round(e.Seconds*10) / 10, "lead": e.Lead, "dist_lead": math.Round(e.DistLead*10) / 10}
	if name != "" {
		d["name"] = name
	}
	return d
}

type stuckTrack struct {
	x, y, z float32
	since   int64 // -1: not counting (not expected to move)
	fired   bool
}

// StuckDetector reads frames in order. SetWalking says whether a walk of the plan is going.
type StuckDetector struct {
	limit   int64 // ms
	walking bool
	tr      map[string]*stuckTrack
}

// NewStuckDetector makes a detector that fires after seconds of standing still.
func NewStuckDetector(seconds float64) *StuckDetector {
	return &StuckDetector{limit: int64(seconds * 1000), tr: map[string]*stuckTrack{}}
}

// SetWalking says whether a walk of the plan is going.
func (d *StuckDetector) SetWalking(on bool) { d.walking = on }

// Feed takes the next frame and returns the stalls it starts or ends.
func (d *StuckDetector) Feed(f trace.Frame) []StuckEvent {
	var lead *trace.Bot
	for i := range f.Bots {
		if f.Bots[i].Lead && !f.Bots[i].NoPos {
			lead = &f.Bots[i]
		}
	}
	var out []StuckEvent
	for _, m := range f.Bots {
		tk := d.tr[m.G]
		dl := 0.0
		if lead != nil && !m.Lead {
			dl = math.Hypot(float64(m.X-lead.X), float64(m.Y-lead.Y))
		}
		expected := d.walking && !m.NoPos && m.Alive && !m.Combat && (m.Lead || (lead != nil && dl > followDist))
		moved := tk != nil && math.Hypot(float64(m.X-tk.x), float64(m.Y-tk.y)) > stuckMoved
		if tk == nil || moved || !expected {
			if tk != nil && tk.fired {
				out = append(out, StuckEvent{Ev: "unstuck", T: f.T, G: m.G, X: tk.x, Y: tk.y, Z: tk.z, Seconds: float64(f.T-tk.since) / 1000, Lead: m.Lead, DistLead: dl})
			}
			d.tr[m.G] = &stuckTrack{x: m.X, y: m.Y, z: m.Z, since: f.T}
			if !expected {
				d.tr[m.G].since = -1
			}
			continue
		}
		if tk.since < 0 {
			tk.since = f.T
			continue
		}
		if !tk.fired && f.T-tk.since >= d.limit {
			tk.fired = true
			out = append(out, StuckEvent{Ev: "stuck", T: f.T, G: m.G, X: tk.x, Y: tk.y, Z: tk.z, Seconds: float64(f.T-tk.since) / 1000, Lead: m.Lead, DistLead: dl})
		}
	}
	return out
}

// DetectStuck runs the detector over a trace: the walks of the plan come from its walk_start and walk_end events.
func DetectStuck(f *trace.File, seconds float64) []StuckEvent {
	type step struct {
		t     int64
		on    bool
		frame *trace.Frame
	}
	var steps []step
	for _, e := range f.Events {
		switch e.Ev {
		case "walk_start":
			steps = append(steps, step{t: e.T, on: true})
		case "walk_end":
			steps = append(steps, step{t: e.T})
		}
	}
	for i := range f.Frames {
		steps = append(steps, step{t: f.Frames[i].T, frame: &f.Frames[i]})
	}
	sort.SliceStable(steps, func(i, j int) bool { return steps[i].t < steps[j].t })
	d := NewStuckDetector(seconds)
	var out []StuckEvent
	for _, s := range steps {
		if s.frame == nil {
			d.SetWalking(s.on)
			continue
		}
		out = append(out, d.Feed(*s.frame)...)
	}
	return out
}
