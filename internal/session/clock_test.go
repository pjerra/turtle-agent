package session

import (
	"math"
	"math/rand"
	"testing"
	"time"

	"github.com/pjerra/turtle-agent/internal/game"
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
// monotonic clock runs 3.8% fast: never back, within a few seconds of real time, no CLOCK_DESYNC.
// A walk is timed on the client time (walkTime), so the clock reads the idle slew while walking too.
func TestMoveClockTracksRealTime(t *testing.T) {
	h := &driftHost{rate: 1.038}
	var c moveClock
	var last uint32
	var first time.Duration
	var firstC uint32
	started := false
	worst := 0.0
	read := func(moving bool) uint32 {
		sl := slewIdle
		if moving {
			sl = slewWalking
		}
		c = c.at(h.mono, h.wall, sl)
		v := max(uint32(c.ms)+1, last)
		if v < last {
			t.Fatalf("client time ran back from %d to %d", last, v)
		}
		last = v
		return v
	}
	sample := func(ctime uint32, _ bool) {
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
		}
	}
	longSession(t, h, read, sample)
	// The wall clock steps back by about a second every half minute and the client time follows
	// it at up to half the monotonic pace, so it strays about a second (measured 999 ms).
	if bound := 1500.0; worst > bound {
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
	c = c.at(10*time.Second, 10*time.Second, slewIdle)
	for _, step := range []time.Duration{time.Hour, -2 * time.Hour} {
		before := c.ms
		c = c.at(c.mono+time.Second, c.mono+time.Second+step, slewIdle)
		if d := c.ms - before; d < 999 || d > 1001 {
			t.Fatalf("wall step %s: client time moved %.0f ms in a second", step, d)
		}
		before = c.ms
		c = c.at(c.mono+time.Second, c.mono+step, slewIdle) // wall reading only lags
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
	if _, _, slew := s.clockReading(start); slew != slewIdle {
		t.Fatalf("standing still: slew %v", slew)
	}
	s.walk = &walkState{}
	s.last.flags = game.MoveForward
	if _, _, slew := s.clockReading(start); slew != slewWalking {
		t.Fatalf("walking: slew %v (slowing as freely as idle, speeding up by 2%% at most)", slew)
	}
	s.fall = &fallState{}
	if _, _, slew := s.clockReading(start); slew != slewFlight {
		t.Fatalf("in a fall: slew %v", slew)
	}
}

// One continuous walk of six minutes on a host whose monotonic clock runs 3.8% fast, as the
// agent makes between two far legs of a dungeon: the server's anticheat must not see the client
// clock more than max(5 s, 500 ms a minute) ahead, and no step may be longer than the run
// speed times the client-time gap it carries (Movement.cpp:574-595). The walk timed on the
// monotonic clock with the small moving slew (before) gains 1.8% of 360 s = 6.5 s and desyncs.
func TestLongContinuousWalkOnAFastHost(t *testing.T) {
	const (
		speed   = float32(7)
		walkFor = 6 * time.Minute
	)
	run := func(t *testing.T, onClientTime bool) (desynced bool) {
		h := &driftHost{rate: 1.038}
		start := time.Unix(1_800_000_000, 0)
		slew := slewFlight // the small slew the walk used to hold the clock to
		if onClientTime {
			slew = slewWalking
		}
		var c moveClock
		var lastC uint32
		clock := func() uint32 {
			c = c.at(h.mono, h.wall, slew)
			lastC = max(uint32(c.ms)+1, lastC)
			return lastC
		}
		axis := func() time.Time { // what the walk is read at
			if onClientTime {
				return walkTime(start, c.at(h.mono, h.wall, slew))
			}
			return start.Add(h.mono)
		}
		w := newWalk([][3]float32{{0, 0, 0}, {float32(float64(speed) * 3600), 0, 0}}, speed, start, make(chan error, 1))
		w.start = axis()
		firstC := clock()
		first := h.real
		prevC, prevX := firstC, float32(0)
		worstStep := 0.0
		for h.real < walkFor {
			for range 5 { // the loop ticks between two heartbeats, the clock read at each
				h.advance(100 * time.Millisecond)
				_ = clock()
			}
			ct := clock()
			x := w.at(axis())[0]
			if over := float64(x-prevX) - float64(speed)*float64(ct-prevC)/1000; over > worstStep {
				worstStep = over
			}
			if over := float64(x-prevX) - float64(speed)*float64(ct-prevC)/1000; over > stepMargin {
				t.Fatalf("at %s a step of %.2f yd in %d ms is %.2f yd over what the server allows", h.real, x-prevX, ct-prevC, over)
			}
			if desync(float64(ct-firstC), float64(h.real-first)/float64(time.Millisecond)) {
				desynced = true
			}
			prevC, prevX = ct, x
		}
		t.Logf("client %.1f s for %.1f s real, worst step %.3f yd over the bound", float64(prevC-firstC)/1000, float64(h.real-first)/float64(time.Second), worstStep)
		return desynced
	}
	t.Run("walk on the client time", func(t *testing.T) {
		if run(t, true) {
			t.Fatal("CLOCK_DESYNC on a six-minute walk")
		}
	})
	t.Run("walk on the monotonic clock (before)", func(t *testing.T) {
		if !run(t, false) {
			t.Fatal("the old timing never tripped CLOCK_DESYNC: the simulation proves nothing")
		}
	})
}

// A wall clock that is ahead of the client time (stepped forward, a suspend the monotonic clock did not count, a slow monotonic clock)
// must not make a walk's client time run fast: it would put two heartbeats, sent 500 ms of real time apart, more than 1000 ms apart in
// client time after a loop stall of 700 ms (SKIPPED_HEARTBEATS, Movement.cpp:600-604), and the leader would walk at 1.5 times its speed.
func TestWalkingClientTimeCannotRunFastToCatchUp(t *testing.T) {
	const (
		speed = 7.0
		stall = 700 * time.Millisecond // an owner-loop gap: at 1.5 times real time it is 1050 ms of client time
	)
	run := func(sl slewLimits) (worstGap float64, ratio float64) {
		c := moveClock{wallOff: 20 * time.Second} // the wall clock is 20 s ahead of the client time
		var mono time.Duration
		var lastBeat time.Duration
		var lastC float64
		for mono < 120*time.Second {
			step := 100 * time.Millisecond
			if mono == 30*time.Second || mono == 90*time.Second {
				step = stall
				lastBeat, lastC = mono, c.ms // a heartbeat has just gone out when the loop stalls
			}
			mono += step
			c = c.at(mono, mono, sl) // a healthy host: the wall clock runs at the monotonic pace
			if mono-lastBeat >= 500*time.Millisecond {
				if gap := c.ms - lastC; gap > worstGap {
					worstGap = gap
				}
				lastBeat, lastC = mono, c.ms
			}
		}
		return worstGap, c.ms / float64(mono/time.Millisecond)
	}
	gap, ratio := run(slewWalking)
	if gap > 1000 || ratio > 1+clockSlewWalkFast+1e-9 {
		t.Fatalf("walking: worst heartbeat gap %.0f ms, client time %.3f times real", gap, ratio)
	}
	if want := speed * ratio; want > speed*(1+clockSlewWalkFast)+1e-9 {
		t.Fatalf("the walk would go at %.2f yd/s, run speed %v", want, speed)
	}
	// The same walk at the idle slew shows the simulation catches the problem.
	if gap, _ := run(slewIdle); gap <= 1000 {
		t.Fatalf("at the idle slew the gap was %.0f ms: the simulation proves nothing", gap)
	}
	t.Logf("walking 20 s behind the wall clock: worst heartbeat gap %.0f ms, client time %.3f times real", gap, ratio)
}

// Slowing down is as free as standing still: a fast monotonic clock keeps the client time at the pace of the wall clock while walking.
func TestWalkingClientTimeSlowsDownFreely(t *testing.T) {
	c := moveClock{}
	var mono, wall time.Duration
	for range 6000 { // 10 minutes of 100 ms ticks on a host whose monotonic clock runs 3.8% fast
		mono += 100 * time.Millisecond
		wall += time.Duration(96339100) // 100 ms / 1.038
		c = c.at(mono, wall, slewWalking)
	}
	if off := c.ms - float64(wall/time.Millisecond); off > 200 || off < -200 {
		t.Fatalf("client time %.0f ms against the wall clock's %d ms", c.ms, wall/time.Millisecond)
	}
}
