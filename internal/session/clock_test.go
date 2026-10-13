package session

import (
	"math"
	"math/rand"
	"testing"
	"time"

	"github.com/pjerra/turtle-agent/internal/worldconn"
)

// driftHost is a host whose monotonic clock runs fast and whose wall clock is stepped back a
// second whenever it has gained one on real time, as a WSL2 laptop did in live run 3 (wall
// stepped back about 1.04 s every ~30 s, monotonic not; the server saw the agent 3.1% fast).
type driftHost struct {
	real, mono, wall time.Duration
	rate             float64 // monotonic (and wall, between steps) seconds per real second
}

func (h *driftHost) advance(d time.Duration) {
	h.real += d
	h.mono += time.Duration(float64(d) * h.rate)
	h.wall += time.Duration(float64(d) * h.rate)
	if h.wall-h.real >= time.Second {
		h.wall -= 1040 * time.Millisecond
	}
}

// desync is Movement.cpp:452-462: the client counted past the server's time since the
// first movement by more than max(5 s, 500 ms a minute).
func desync(clientDiff, serverDiff float64) bool {
	return clientDiff > serverDiff+max(5000, serverDiff*500/60000)
}

// session is an hour's play: walks of 10-150 s with a heartbeat every 500 ms and a 100 ms
// loop tick reading the clock (peekClock for clampZ), and idle stretches of 2-90 s where the
// brain steps every 100 ms and now and then turns to face. sample gets every packet's client
// time and real time; moving says whether the packet ends a step of a walk under way.
func longSession(t *testing.T, h *driftHost, read func(moving bool) uint32, sample func(ctime uint32, moving bool)) {
	t.Helper()
	rng := rand.New(rand.NewSource(3))
	for h.real < time.Hour {
		walk := time.Duration(10+rng.Intn(140)) * time.Second
		sample(read(true), false) // start forward: from where the stop left it, no step to bound
		for el := time.Duration(0); el < walk; {
			for range 5 { // the loop ticks between two heartbeats
				h.advance(100 * time.Millisecond)
				el += 100 * time.Millisecond
				_ = read(true)
			}
			sample(read(true), true) // heartbeat
		}
		sample(read(true), true) // stop: still a moving packet's gap from the last heartbeat
		idle := time.Duration(2+rng.Intn(88)) * time.Second
		for el := time.Duration(0); el < idle; el += 100 * time.Millisecond {
			h.advance(100 * time.Millisecond)
			c := read(false)
			if rng.Intn(40) == 0 {
				sample(c, false) // set facing
			}
		}
	}
}

// The movement client time keeps the server's pace over a long session on a host whose
// monotonic clock runs 3.8% fast: never back, within a few seconds of real time, no CLOCK_DESYNC,
// and while moving no packet gap shorter than the walk's own monotonic time allows a step for.
func TestMoveClockTracksRealTime(t *testing.T) {
	h := &driftHost{rate: 1.038}
	var c moveClock
	var last uint32
	var first, prevMono time.Duration
	var firstC uint32
	var prevC uint32
	started, prevMoving := false, false
	worst := 0.0
	read := func(moving bool) uint32 {
		slew := clockSlewIdle
		if moving {
			slew = clockSlewMoving
		}
		c = c.at(h.mono, h.wall, slew)
		v := max(uint32(c.ms)+1, last)
		if v < last {
			t.Fatalf("client time ran back from %d to %d", last, v)
		}
		last = v
		return v
	}
	sample := func(ctime uint32, moving bool) {
		if !started {
			started, first, firstC = true, h.real, ctime
		} else {
			serverDiff := float64(h.real-first) / float64(time.Millisecond)
			clientDiff := float64(ctime - firstC)
			if desync(clientDiff, serverDiff) {
				t.Fatalf("CLOCK_DESYNC at %s: client %0.f ms, server %0.f ms", h.real, clientDiff, serverDiff)
			}
			if off := math.Abs(clientDiff - serverDiff); h.real > 2*time.Minute && off > worst {
				worst = off
			}
			if moving && prevMoving {
				monoGap := float64(h.mono-prevMono) / float64(time.Millisecond)
				if gap := float64(ctime - prevC); gap < monoGap*(1-clockSlewMoving)-1 {
					t.Fatalf("at %s a moving gap of %.0f ms client time for %.0f ms walked: steps would outrun it", h.real, gap, monoGap)
				}
			}
		}
		prevC, prevMono, prevMoving = ctime, h.mono, moving
	}
	longSession(t, h, read, sample)
	// While moving the client time gains (3.8% - clockSlewMoving) of the walk, at most 2.7 s on
	// a 150 s walk, plus the wall clock's own second; standing still takes it back.
	if bound := (h.rate-1-clockSlewMoving)*150_000 + 1100; worst > bound {
		t.Errorf("client time strayed %.0f ms from real time (bound %.0f)", worst, bound)
	}
	t.Logf("hour on a 3.8%% fast monotonic clock: worst offset %.0f ms after the first two minutes", worst)
}

// The same session counted on the monotonic clock alone, as the agent did before: the
// simulated host does make the anticheat fire, so the test above proves something.
func TestMonotonicClockAloneDesyncs(t *testing.T) {
	h := &driftHost{rate: 1.038}
	var first time.Duration
	var firstC uint32
	started, flagged := false, false
	read := func(bool) uint32 { return uint32(h.mono/time.Millisecond) + 1 }
	sample := func(ctime uint32, _ bool) {
		if !started {
			started, first, firstC = true, h.real, ctime
			return
		}
		if desync(float64(ctime-firstC), float64(h.real-first)/float64(time.Millisecond)) {
			flagged = true
		}
	}
	longSession(t, h, read, sample)
	if !flagged {
		t.Fatal("the drifting host never tripped CLOCK_DESYNC: the simulation proves nothing")
	}
}

// A wall clock set by an hour (by hand, a resume) is followed from where it now is: the client
// time neither jumps nor stalls.
func TestMoveClockWallStep(t *testing.T) {
	var c moveClock
	c = c.at(10*time.Second, 10*time.Second, clockSlewIdle)
	for _, step := range []time.Duration{time.Hour, -2 * time.Hour} {
		before := c.ms
		c = c.at(c.mono+time.Second, c.mono+time.Second+step, clockSlewIdle)
		if d := c.ms - before; d < 999 || d > 1001 {
			t.Fatalf("wall step %s: client time moved %.0f ms in a second", step, d)
		}
		before = c.ms
		c = c.at(c.mono+time.Second, c.mono+step, clockSlewIdle) // wall reading only lags
		if d := c.ms - before; d < 0 || d > 1001 {
			t.Fatalf("after wall step %s: client time moved %.0f ms in a second", step, d)
		}
	}
}

// On a healthy host (or a replay, whose times carry no monotonic reading) the client time is
// the milliseconds since the start plus one, as before, and a walk reads the moving slew.
func TestClockAtHealthyHost(t *testing.T) {
	s := New(worldconn.NewOffline(), Options{Character: "x"})
	start := time.Unix(1_800_000_000, 0)
	s.start = start
	for _, ms := range []int64{0, 1, 499, 500, 61_234, 3_600_000} {
		now := start.Add(time.Duration(ms) * time.Millisecond)
		if p := s.peekClock(now); p != uint32(ms)+1 {
			t.Fatalf("peekClock at %d ms = %d", ms, p)
		}
		if got := s.clockAt(now); got != uint32(ms)+1 {
			t.Fatalf("clockAt at %d ms = %d, want %d", ms, got, ms+1)
		}
	}
	if got := s.clockAt(start); got != 3_600_001 {
		t.Fatalf("clockAt ran back to %d", got)
	}
	if _, _, slew := s.clockReading(start); slew != clockSlewIdle {
		t.Fatalf("standing still: slew %v", slew)
	}
	s.walk = &walkState{}
	if _, _, slew := s.clockReading(start); slew != clockSlewMoving {
		t.Fatalf("walking: slew %v", slew)
	}
}
