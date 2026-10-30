package patrols

import (
	"math"
	"strings"
	"testing"
	"time"

	"github.com/pjerra/turtle-agent/internal/packet"
)

// A 40 yd square walked at 2.5 yd/s: one lap takes 64 s (160 yd).
const (
	squareSide  = 40.0
	squareLapS  = 64
	squareSpeed = 2.5
)

func baseTime() time.Time { return time.Date(2026, 10, 29, 10, 0, 0, 0, time.UTC) }

// squarePoint is the point d yards along the square (0,0) -> (40,0) -> (40,40) -> (0,40) -> (0,0).
func squarePoint(d float64) (float32, float32) {
	d = math.Mod(d, 4*squareSide)
	switch {
	case d < squareSide:
		return float32(d), 0
	case d < 2*squareSide:
		return squareSide, float32(d - squareSide)
	case d < 3*squareSide:
		return float32(3*squareSide - d), squareSide
	default:
		return 0, float32(4*squareSide - d)
	}
}

// squareSamples walks cycles laps of the square with one sample per second. A second named in stops holds the unit at
// that corner for two extra samples (the arrival sample plus two resting ones: 2 s of rest), and time moves on.
func squareSamples(guid packet.GUID, entry uint32, name string, cycles int, stops map[int]bool, dx, dy float32) []Sample {
	base := baseTime()
	t := 0
	var out []Sample
	for lap := 0; lap < cycles; lap++ {
		for sec := 0; sec < squareLapS; sec++ {
			x, y := squarePoint(float64(sec) * squareSpeed)
			x += dx
			y += dy
			stop := stops[sec] && sec%16 == 0
			out = append(out, Sample{T: base.Add(time.Duration(t) * time.Second), GUID: guid, Entry: entry, Name: name, X: x, Y: y, Moving: !stop})
			if stop {
				out = append(out,
					Sample{T: base.Add(time.Duration(t+1) * time.Second), GUID: guid, Entry: entry, Name: name, X: x, Y: y},
					Sample{T: base.Add(time.Duration(t+2) * time.Second), GUID: guid, Entry: entry, Name: name, X: x, Y: y})
				t += 3
			} else {
				t++
			}
		}
	}
	return out
}

func TestAnalyzeSquareLoopPeriod(t *testing.T) {
	guid := packet.GUID(0xF130002F61000001)
	samples := squareSamples(guid, 12129, "Onyxian Warder", 3, nil, 0, 0)

	ps, st, ws, notes := Analyze(samples, Options{})
	if len(ps) != 1 || len(st) != 0 || len(ws) != 0 {
		t.Fatalf("got %d patrols, %d static, %d wanderers, want 1/0/0 (notes %q)", len(ps), len(st), len(ws), notes)
	}
	p := ps[0]
	if math.Abs(float64(p.PeriodS)-64) > 1 {
		t.Errorf("period %g s, want 64 +-1", p.PeriodS)
	}
	if p.CyclesSeen != 3 {
		t.Errorf("cycles %d, want 3", p.CyclesSeen)
	}
	if p.PeriodEstimate {
		t.Error("period_estimate set for a loop seen three times")
	}
	if !p.Complete {
		t.Error("complete false for a closed loop")
	}
	if n := len(p.Path); n < 4 || n > 6 {
		t.Errorf("path has %d points, want 4-6 for a square: %+v", n, p.Path)
	}
	if p.ID != guid.String() {
		t.Errorf("id %q, want the hex guid %q", p.ID, guid.String())
	}
	if len(p.GUIDs) != 1 || p.GUIDs[0] != guid.String() {
		t.Errorf("guids %v, want [%s]", p.GUIDs, guid.String())
	}
	if len(p.Entries) != 1 || p.Entries[0] != 12129 {
		t.Errorf("entries %v, want [12129]", p.Entries)
	}
	if p.Name != "Onyxian Warder" {
		t.Errorf("name %q", p.Name)
	}
	if p.Samples != len(samples) {
		t.Errorf("samples %d, want %d", p.Samples, len(samples))
	}
	wantPhase := baseTime().Add(2 * squareLapS * time.Second).UnixMilli()
	if p.PhaseEpochMS != wantPhase {
		t.Errorf("phase_epoch_ms %d, want the last exact visit of path[0] %d", p.PhaseEpochMS, wantPhase)
	}
}

func TestAnalyzeWaitsAtCorners(t *testing.T) {
	guid := packet.GUID(0xF130002F61000002)
	samples := squareSamples(guid, 12129, "Onyxian Warder", 1, map[int]bool{16: true, 32: true}, 0, 0)

	ps, _, _, _ := Analyze(samples, Options{})
	if len(ps) != 1 {
		t.Fatalf("got %d patrols, want 1", len(ps))
	}
	p := ps[0]
	for _, c := range []struct{ x, y float32 }{{40, 0}, {40, 40}} {
		want := 2000
		if got := waitAt(t, p, c.x, c.y); got != want {
			t.Errorf("waits at (%g,%g) = %d ms, want %d", c.x, c.y, got, want)
		}
	}
	for i, pnt := range p.Path {
		if (pnt.X == 40 && (pnt.Y == 0 || pnt.Y == 40)) || (pnt.X == 0 && (pnt.Y == 0 || pnt.Y == 40)) {
			continue
		}
		if p.WaitsMS[i] != 0 {
			t.Errorf("point %d (%g,%g) has waits %d, want 0", i, pnt.X, pnt.Y, p.WaitsMS[i])
		}
	}
}

// waitAt returns the wait recorded for the path point nearest (x,y).
func waitAt(t *testing.T, p Patrol, x, y float32) int {
	t.Helper()
	best, bestI := math.MaxFloat64, -1
	for i, pnt := range p.Path {
		d := math.Hypot(float64(pnt.X-x), float64(pnt.Y-y))
		if d < best {
			best, bestI = d, i
		}
	}
	if bestI < 0 || best > 2 {
		t.Fatalf("no path point near (%g,%g): %+v", x, y, p.Path)
	}
	return p.WaitsMS[bestI]
}

func TestAnalyzeFormationMerged(t *testing.T) {
	a := packet.GUID(0xF130002F6100000A)
	b := packet.GUID(0xF130002F61000003)
	c := packet.GUID(0xF130002F61000007)
	var samples []Sample
	samples = append(samples, squareSamples(a, 100, "Lead", 2, nil, 0, 0)...)
	samples = append(samples, squareSamples(b, 101, "Second", 2, nil, 3, 0)...)
	samples = append(samples, squareSamples(c, 102, "Third", 2, nil, 0, 3)...)

	ps, st, ws, _ := Analyze(samples, Options{})
	if len(ps) != 1 || len(st) != 0 || len(ws) != 0 {
		t.Fatalf("got %d patrols, %d static, %d wanderers, want one patrol of three", len(ps), len(st), len(ws))
	}
	p := ps[0]
	if len(p.GUIDs) != 3 {
		t.Fatalf("guids %v, want three", p.GUIDs)
	}
	if p.GUIDs[0] != b.String() || p.ID != b.String() {
		t.Errorf("id %q, guids %v: want the smallest guid %s first and as the id", p.ID, p.GUIDs, b)
	}
	want := []uint32{101, 102, 100} // in guid order: b, c, a
	for i, e := range want {
		if p.Entries[i] != e {
			t.Fatalf("entries %v, want %v", p.Entries, want)
		}
	}
	if p.Name != "Second" {
		t.Errorf("name %q, want the lead member's %q", p.Name, "Second")
	}
}

func TestAnalyzePassingUnitNotMerged(t *testing.T) {
	a := packet.GUID(0xF130002F6100000A)
	b := packet.GUID(0xF130002F6100000B)
	lead := squareSamples(a, 100, "Lead", 3, nil, 0, 0)
	passing := make([]Sample, len(lead))
	copy(passing, lead)
	for i := range passing {
		passing[i].GUID, passing[i].Entry, passing[i].Name = b, 101, "Passing"
		if i <= 116 { // within 8 yd of the lead for 117 of 193 common samples (60%)
			passing[i].X += 3
			continue
		}
		passing[i].X, passing[i].Y = 500, 500
	}
	samples := append(lead, passing...)

	ps, _, _, _ := Analyze(samples, Options{})
	if len(ps) != 2 {
		t.Fatalf("got %d patrols, want 2: a unit near another for 60%% of the samples is not a formation member", len(ps))
	}
}

func TestAnalyzeFigureEightCrossesItsStartMidLoop(t *testing.T) {
	guid := packet.GUID(0xF130002F6100000C)
	base := baseTime()
	var samples []Sample
	// A Lissajous figure eight of period 80 s through the start at 0, 40 and 80 s. The recording stops at 50 s, so the only
	// sighting back at the start is the 40 s crossing: it is not a loop, and taking it as one (no next-sample confirmation) would
	// report a 40 s round.
	for s := 0; s <= 50; s++ {
		u := 2 * math.Pi * float64(s%80) / 80
		x, y := 20*math.Sin(u), 20*math.Sin(2*u)
		samples = append(samples, Sample{T: base.Add(time.Duration(s) * time.Second), GUID: guid, Entry: 3, Name: "Eight", X: float32(x), Y: float32(y), Moving: true})
	}
	ps, _, _, notes := Analyze(samples, Options{})
	if len(ps) != 1 {
		t.Fatalf("got %d patrols, want 1", len(ps))
	}
	if ps[0].Complete {
		t.Error("complete true: the 40 s crossing of the start is not a loop")
	}
	if math.Abs(float64(ps[0].PeriodS)-50) > 0.1 {
		t.Errorf("period %g s, want the recording's 50 s span, not the 40 s crossing", ps[0].PeriodS)
	}
	if len(notes) != 1 || !strings.Contains(notes[0], "seen leaving the area or a loop longer than the recording") {
		t.Errorf("notes %q, want the incomplete-loop note", notes)
	}
}

func TestAnalyzeStaticAndWanderer(t *testing.T) {
	base := baseTime()
	still := packet.GUID(0xF130002F6100000D)
	drift := packet.GUID(0xF130002F6100000E)
	var samples []Sample
	for i := 0; i <= 120; i++ {
		j := 0.5 * math.Sin(float64(i))
		samples = append(samples, Sample{T: base.Add(time.Duration(i) * time.Second), GUID: still, Entry: 1, Name: "Still", X: float32(10 + j), Y: float32(20 - j)})
	}
	for i := 0; i <= 60; i++ { // a slow walk out of the area: 0.31 yd/s leaves 3 yd per 10 s, so it never loops
		samples = append(samples, Sample{T: base.Add(time.Duration(i) * time.Second), GUID: drift, Entry: 2, Name: "Drift", X: float32(0.31 * float64(i)), Moving: true})
	}

	ps, st, ws, notes := Analyze(samples, Options{})
	if len(ps) != 0 || len(notes) != 0 {
		t.Fatalf("got %d patrols, notes %q, want none", len(ps), notes)
	}
	if len(st) != 1 {
		t.Fatalf("got %d static, want 1", len(st))
	}
	if st[0].GUID != still.String() || st[0].Entry != 1 {
		t.Errorf("static %+v", st[0])
	}
	if math.Hypot(float64(st[0].X-10), float64(st[0].Y-20)) > 0.5 {
		t.Errorf("static position (%g,%g), want the median near (10,20)", st[0].X, st[0].Y)
	}
	if len(ws) != 1 {
		t.Fatalf("got %d wanderers, want 1", len(ws))
	}
	if ws[0].GUID != drift.String() || ws[0].Entry != 2 {
		t.Errorf("wanderer %+v", ws[0])
	}
	if ws[0].Radius < 3 || ws[0].Radius > 15 {
		t.Errorf("wanderer radius %g, want the 3-15 yd extent of the spiral", ws[0].Radius)
	}
}

func TestAnalyzeIncompleteLoop(t *testing.T) {
	guid := packet.GUID(0xF130002F6100000F)
	base := baseTime()
	var samples []Sample
	for i := 0; i <= 40; i++ {
		x, y := squarePoint(float64(i) * squareSpeed)
		samples = append(samples, Sample{T: base.Add(time.Duration(i) * time.Second), GUID: guid, Entry: 4, Name: "Wanderer", X: x, Y: y, Moving: true})
	}
	for i := 41; i <= 60; i++ { // leaves the area and keeps walking
		samples = append(samples, Sample{T: base.Add(time.Duration(i) * time.Second), GUID: guid, Entry: 4, Name: "Wanderer", X: float32(500 + 0.5*float64(i-40)), Y: 500, Moving: true})
	}

	ps, _, ws, notes := Analyze(samples, Options{})
	if len(ps) != 1 || len(ws) != 0 {
		t.Fatalf("got %d patrols, %d wanderers, want the mover only", len(ps), len(ws))
	}
	p := ps[0]
	if p.Complete {
		t.Error("complete true without a loop")
	}
	if !p.PeriodEstimate {
		t.Error("period_estimate false without a loop")
	}
	if math.Abs(float64(p.PeriodS)-60) > 0.1 {
		t.Errorf("period %g s, want the recording's 60 s span", p.PeriodS)
	}
	if len(notes) != 1 || !strings.Contains(notes[0], "seen leaving the area or a loop longer than the recording") {
		t.Errorf("notes %q, want the incomplete-loop note", notes)
	}
}
