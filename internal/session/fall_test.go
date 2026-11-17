package session

import (
	"context"
	"math"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/TortoiseBots/turtle-agent/internal/events"
	"github.com/TortoiseBots/turtle-agent/internal/game"
	"github.com/TortoiseBots/turtle-agent/internal/game/gametest"
	"github.com/TortoiseBots/turtle-agent/internal/navmesh"
	"github.com/TortoiseBots/turtle-agent/internal/opcode"
	"github.com/TortoiseBots/turtle-agent/internal/packet"
)

// ground is a fake terrain: the floor heights over (x, y), any order.
type ground func(x, y float32) []float32

func (g ground) Floors(_ uint32, x, y, lo, hi float32) ([]navmesh.Floor, error) {
	var out []navmesh.Floor
	for _, z := range g(x, y) {
		if z >= lo && z <= hi {
			out = append(out, navmesh.Floor{Z: z, Flags: navmesh.NavGround})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Z > out[j].Z })
	return out, nil
}

// groundPlanner is a Planner that knows the ground and plans nothing.
type groundPlanner struct{ ground }

func (groundPlanner) Plan(uint32, navmesh.Vec3, navmesh.Vec3) (navmesh.Path, error) {
	return navmesh.Path{Status: navmesh.NoPath}, nil
}

func flat(z float32) ground { return func(float32, float32) []float32 { return []float32{z} } }

func flight(g ground, from [3]float32, kb game.KnockBack) *fallState {
	f := &fallState{kb: kb, began: time.Unix(0, 0), startZ: from[2], floor: func(x, y, lo, hi float32) ([]navmesh.Floor, error) {
		return g.Floors(0, x, y, lo, hi)
	}}
	f.arc(from, f.began, float64(kb.ZSpeed), game.TerminalVelocity, false)
	return f
}

func (f *fallState) seconds() float64 { return f.land.Sub(f.start).Seconds() }

// The arc against the terrain: it lands where it meets the floor, sooner up a slope and
// later down one than on flat ground, slides down a wall it runs into, and without a floor
// comes back to the height it began at, a guess.
func TestFlightLandsOnTheFloor(t *testing.T) {
	north := game.KnockBack{Cos: 0, Sin: 1, XYSpeed: 8, ZSpeed: -9.5}
	flatT := game.FallLandTime(-9.5, game.TerminalVelocity)
	cases := []struct {
		name   string
		g      ground
		kb     game.KnockBack
		t      func(float64) bool // flight time
		y, z   float32            // landing, relative to the start; NaN: not checked
		floor  bool
		stopXY bool
	}{
		{"flat", flat(0), north, func(s float64) bool { return math.Abs(s-flatT) < 0.002 }, float32(8 * flatT), 0, true, false},
		{"uphill", func(x, y float32) []float32 { return []float32{y / 2} }, north,
			func(s float64) bool { return s < flatT-0.1 }, float32(math.NaN()), float32(math.NaN()), true, false},
		{"downhill", func(x, y float32) []float32 { return []float32{-y / 2} }, north,
			func(s float64) bool { return s > flatT+0.1 }, float32(math.NaN()), float32(math.NaN()), true, false},
		{"20 yd drop from rest", flat(-20), game.KnockBack{Cos: 1}, func(s float64) bool {
			return math.Abs(s-game.FallTimeTo(20, 0, game.TerminalVelocity)) < 0.002
		}, 0, -20, true, false},
		{"cliff 20 yd up from y 3", func(x, y float32) []float32 {
			if y >= 3 {
				return []float32{20}
			}
			return []float32{0}
		}, north, func(s float64) bool { return math.Abs(s-flatT) < 0.06 }, float32(math.NaN()), 0, true, true},
		{"no floor", func(float32, float32) []float32 { return nil }, north,
			func(s float64) bool { return math.Abs(s-flatT) < 0.002 }, float32(8 * flatT), float32(math.NaN()), false, false},
	}
	for _, c := range cases {
		f := flight(c.g, [3]float32{0, 0, 0}, c.kb)
		got := f.landPos
		if !c.t(f.seconds()) || f.onFloor != c.floor || (f.stopXY < 10) != c.stopXY {
			t.Errorf("%s: lands after %.3f s on floor %v, stopped across %v (%.2f s), at %v", c.name, f.seconds(), f.onFloor, f.stopXY < 10, f.stopXY, got)
			continue
		}
		if !math.IsNaN(float64(c.y)) && math.Abs(float64(got[1]-c.y)) > 0.02 {
			t.Errorf("%s: lands at y %.3f, want %.3f", c.name, got[1], c.y)
		}
		if !math.IsNaN(float64(c.z)) && got[2] != c.z {
			t.Errorf("%s: lands at z %.3f, want %.3f", c.name, got[2], c.z)
		}
		if fl := c.g(got[0], got[1]); c.floor && (len(fl) != 1 || fl[0] != got[2]) {
			t.Errorf("%s: landing %v is not on the floor %v", c.name, got, fl)
		}
		if c.stopXY && got[1] >= 3 {
			t.Errorf("%s: went through the wall to y %.2f", c.name, got[1])
		}
	}
}

// A floor overhead (a bridge) is no wall while there is ground under the arc.
func TestFlightPassesUnderABridge(t *testing.T) {
	bridge := func(x, y float32) []float32 {
		if y >= 2 && y <= 4 {
			return []float32{0, 6}
		}
		return []float32{0}
	}
	f := flight(bridge, [3]float32{0, 0, 0}, game.KnockBack{Sin: 1, XYSpeed: 20, ZSpeed: -4})
	if f.stopXY < 10 || !f.onFloor || f.landPos[2] != 0 || f.landPos[1] < 6 {
		t.Fatalf("landed at %v after %.2f s, stopped across at %.2f", f.landPos, f.seconds(), f.stopXY)
	}
}

// Review M13 / #23: a GM teleport into the air falls to the navmesh floor under it. The fall
// starts with a heartbeat where the teleport put the player (the server keeps its flags
// across a teleport and checks the first packet after one), carries MoveJumping and, on the
// way down, MoveFallingFar, and lands with MSG_MOVE_FALL_LAND and the time fallen; every
// step stays inside the anticheat bound and the landing z is known.
func TestTeleportFallsToTheFloor(t *testing.T) {
	ctx := context.Background()
	h := start(t, Options{Planner: groundPlanner{flat(10)}})
	h.enterWorld()
	done := make(chan error, 1)
	go func() { done <- h.s.Teleport(ctx, 1, 2, 30, -1) }()
	h.expect(opcode.CMSGMessagechat)
	w := packet.NewWriter().PackedGUID(me.GUID).U32(7)
	game.WriteMoveInfo(w, game.MoveInfo{X: 1, Y: 2, Z: 30, O: 0.5})
	h.send(opcode.MSGMoveTeleportAck, w.Bytes())
	h.expect(opcode.MSGMoveTeleportAck)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	first := readMove(h.expect(opcode.MSGMoveHeartbeat).Body)
	if first.Flags != game.MoveJumping || first.X != 1 || first.Y != 2 || first.Z != 30 || first.FallTime != 0 ||
		first.JumpXYSpeed != 0 || first.JumpZSpeed != 0 {
		t.Fatalf("fall start %+v", first)
	}
	if st := h.s.Status(); !st.Falling {
		t.Fatalf("status in the air %+v", st)
	}
	land, err := h.srv.Expect(opcode.MSGMoveFallLand, 3*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	standing := first
	standing.Flags = 0
	steps := []game.MoveInfo{standing, first}
	for _, p := range h.srv.Skipped() {
		if p.Op == opcode.MSGMoveHeartbeat {
			mi := readMove(p.Body)
			if mi.Flags != game.MoveJumping|game.MoveFallingFar || mi.X != 1 || mi.Y != 2 || mi.Z >= 30 || mi.Z <= 10 {
				t.Fatalf("fall heartbeat %+v", mi)
			}
			steps = append(steps, mi)
		}
	}
	lm := readMove(land.Body)
	steps = append(steps, lm)
	for i := 1; i < len(steps); i++ {
		stepWithin(t, i, steps[i-1], steps[i], defaultRunSpeed)
	}
	want := game.FallTimeTo(20, 0, game.TerminalVelocity) * 1000
	if len(steps) < 5 || lm.Flags != 0 || lm.Z != 10 || lm.X != 1 || lm.Y != 2 || math.Abs(float64(lm.FallTime)-want) > 1 {
		t.Fatalf("landing %+v after %d steps, want z 10 after %.0f ms", lm, len(steps), want)
	}
	waitFor(t, func() bool { st := h.s.Status(); return !st.Falling && st.Z == 10 && !st.ZUnknown })
	if err := h.s.WalkTo(ctx, 3, 2, 10); err != nil {
		t.Fatal(err)
	}
	if mi := readMove(h.expect(opcode.MSGMoveStartForward).Body); mi.Z != 10 || mi.X != 1 {
		t.Fatalf("walk starts at %+v", mi)
	}
	h.expect(opcode.MSGMoveStop)
	h.logout()
	var states []string
	for _, e := range ofKind(h.s.Log(), events.KindSession) {
		if s := e.Data["state"]; s == "falling" || s == "landed" {
			states = append(states, s.(string))
			if s == "landed" && (e.Data["floor"] != "navmesh" || e.Data["z_unknown"] != false) {
				t.Fatalf("landed event %v", e.Data)
			}
		}
	}
	if len(states) != 2 || states[0] != "falling" || states[1] != "landed" {
		t.Fatalf("events %v", states)
	}
}

// A teleport onto the floor needs no fall and knows its z; with no floor under the point
// (or no navmesh) the z stays a guess, as before.
func TestTeleportOntoTheFloorOrNone(t *testing.T) {
	for _, c := range []struct {
		name     string
		opts     Options
		zUnknown bool
	}{
		{"on the floor", Options{Planner: groundPlanner{flat(29.98)}}, false},
		{"just above the floor", Options{Planner: groundPlanner{flat(30.3)}}, false},
		{"no floor", Options{Planner: groundPlanner{func(float32, float32) []float32 { return nil }}}, true},
		{"floor far above", Options{Planner: groundPlanner{flat(40)}}, true},
		{"no navmesh", Options{}, true},
	} {
		t.Run(c.name, func(t *testing.T) {
			h := start(t, c.opts)
			h.enterWorld()
			done := make(chan error, 1)
			go func() { done <- h.s.Teleport(context.Background(), 1, 2, 30, -1) }()
			h.expect(opcode.CMSGMessagechat)
			w := packet.NewWriter().PackedGUID(me.GUID).U32(7)
			game.WriteMoveInfo(w, game.MoveInfo{X: 1, Y: 2, Z: 30})
			h.send(opcode.MSGMoveTeleportAck, w.Bytes())
			h.expect(opcode.MSGMoveTeleportAck)
			if err := <-done; err != nil {
				t.Fatal(err)
			}
			time.Sleep(50 * time.Millisecond)
			if st := h.s.Status(); st.Z != 30 || st.ZUnknown != c.zUnknown || st.Falling {
				t.Fatalf("after .go xyz %+v", st)
			}
			h.logout()
			for _, p := range h.srv.Skipped() {
				if p.Op == opcode.MSGMoveHeartbeat || p.Op == opcode.MSGMoveFallLand {
					t.Fatalf("moved: %s", opcode.Name(p.Op))
				}
			}
		})
	}
}

// #23: a knockback up a slope lands on the slope, sooner and higher than on flat ground;
// the steps stay inside the bound, the landing settles z, and its fall time is the flight's.
func TestKnockBackLandsOnASlope(t *testing.T) {
	slope := func(x, y float32) []float32 { return []float32{me.Z + (y-me.Y)/2} }
	h := start(t, Options{Planner: groundPlanner{slope}})
	h.enterWorld()
	h.send(opcode.SMSGMoveKnockBack, gametest.KnockBack(me.GUID, 5, 0, 1, 8, -9.5))
	ack, _ := ackMove(t, h.expect(opcode.CMSGMoveKnockBackAck), 5)
	land, err := h.srv.Expect(opcode.MSGMoveFallLand, 3*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	steps := []game.MoveInfo{ack}
	for _, p := range h.srv.Skipped() {
		if p.Op == opcode.MSGMoveHeartbeat {
			steps = append(steps, readMove(p.Body))
		}
	}
	lm := readMove(land.Body)
	steps = append(steps, lm)
	for i := 1; i < len(steps); i++ {
		stepWithin(t, i, steps[i-1], steps[i], defaultRunSpeed)
	}
	flatT := game.FallLandTime(-9.5, game.TerminalVelocity)
	onSlope := me.Z + (lm.Y-me.Y)/2
	if lm.Flags != 0 || lm.Z <= me.Z || math.Abs(float64(lm.Z-onSlope)) > 0.01 || float64(lm.FallTime) > flatT*1000-100 {
		t.Fatalf("landing %+v, slope there at %.3f, flat flight %.0f ms", lm, onSlope, flatT*1000)
	}
	waitFor(t, func() bool { st := h.s.Status(); return !st.Falling && st.Z == lm.Z && !st.ZUnknown })
	h.logout()
}

// A knockback that goes up into a bridge from below bumps its head: it stops going across,
// drops from rest under the bridge and lands on the ground, never on the bridge's top.
func TestFlightBumpsIntoACeiling(t *testing.T) {
	bridge := func(x, y float32) []float32 {
		if y >= 2 && y <= 12 {
			return []float32{0, 3}
		}
		return []float32{0}
	}
	f := flight(bridge, [3]float32{0, 0, 0}, game.KnockBack{Sin: 1, XYSpeed: 8, ZSpeed: -15})
	if !f.onFloor || f.landPos[2] != 0 || f.bumpT > 1 || f.stopXY > f.bumpT {
		t.Fatalf("landed at %v after %.2f s, bump at %.2f, stopped across at %.2f", f.landPos, f.seconds(), f.bumpT, f.stopXY)
	}
	for tt := 0.0; tt < f.seconds(); tt += 0.01 {
		if p := f.pos(tt); p[1] >= 2 && p[2] > 3 {
			t.Fatalf("through the bridge at %.2f s: %v", tt, p)
		}
	}
	// Without a floor under it, a surface above is a wall even on the way up: no bump.
	cliff := func(x, y float32) []float32 {
		if y >= 3 {
			return []float32{20}
		}
		return []float32{0}
	}
	if f := flight(cliff, [3]float32{0, 0, 0}, game.KnockBack{Sin: 1, XYSpeed: 8, ZSpeed: -9.5}); f.bumpT < 10 || f.stopXY > 1 {
		t.Fatalf("cliff: bump at %.2f, stopped across at %.2f", f.bumpT, f.stopXY)
	}
}

// Live (m910q, 2026-10-12): a summon left the player 0.55 yd under the mesh of a 45° slope,
// and the arc took the slope for a ceiling and fell to a guess. The ground a flight starts
// from may be up to startSlack above it; a trace that finds nothing leaves no bump behind.
func TestFlightStartsUnderTheMeshOfASlope(t *testing.T) {
	slope := func(x, y float32) []float32 { return []float32{0.55 + x, -30} } // rises along +x, a cave far below
	f := flight(slope, [3]float32{0, 0, 0}, game.KnockBack{Cos: -1, XYSpeed: 10, ZSpeed: -10})
	if !f.onFloor || f.bumpT < 10 || f.landPos[0] >= 0 || math.Abs(float64(f.landPos[2]-(0.55+f.landPos[0]))) > 0.01 {
		t.Fatalf("downhill: landed at %v (floor %v) after %.2f s, bump at %.2f", f.landPos, f.onFloor, f.seconds(), f.bumpT)
	}
	none := func(float32, float32) []float32 { return []float32{5} } // only a floor far overhead
	f = flight(none, [3]float32{0, 0, 0}, game.KnockBack{Cos: 1, XYSpeed: 10, ZSpeed: -10})
	if f.onFloor || f.bumpT < 10 || math.Abs(float64(f.landPos[2])) > 0.01 {
		t.Fatalf("no floor: landed at %v (floor %v), bump at %.2f", f.landPos, f.onFloor, f.bumpT)
	}
}

// stalledPlanner is a navmesh whose every query waits stall (or until release closes), as
// one behind a path_to reading tiles from a slow disk. Its floor is 5 yd under the player.
type stalledPlanner struct {
	stall   time.Duration
	release chan struct{}
}

func (p stalledPlanner) wait() {
	select {
	case <-p.release:
	case <-time.After(p.stall):
	}
}

func (p stalledPlanner) Floors(_ uint32, x, y, lo, hi float32) ([]navmesh.Floor, error) {
	p.wait()
	return flat(me.Z-5).Floors(0, x, y, lo, hi)
}

func (p stalledPlanner) Plan(uint32, navmesh.Vec3, navmesh.Vec3) (navmesh.Path, error) {
	p.wait()
	return navmesh.Path{Status: navmesh.NoPath}, nil
}

// #42: a planner that stalls for 3 s holds back nothing of the session loop. A teleport is
// acked at once and the floor search under it waits off the loop; a knockback is acked at
// once, its flight keeps its heartbeats inside the anticheat's 1 s and lands on the guess
// when the trace does not answer in time; another order is acked and pings keep their
// cadence meanwhile. The late answers change nothing.
func TestStalledPlannerKeepsTheLoopOnTime(t *testing.T) {
	stall := stalledPlanner{stall: 3 * time.Second, release: make(chan struct{})}
	h := start(t, Options{Planner: stall, PingInterval: 250 * time.Millisecond})
	h.enterWorld()
	quick := func(what string, since time.Time) {
		t.Helper()
		if d := time.Since(since); d > 200*time.Millisecond {
			t.Fatalf("%s after %v", what, d)
		}
	}
	done := make(chan error, 1)
	go func() { done <- h.s.Teleport(context.Background(), 1, 2, 30, -1) }()
	h.expect(opcode.CMSGMessagechat)
	w := packet.NewWriter().PackedGUID(me.GUID).U32(7)
	game.WriteMoveInfo(w, game.MoveInfo{X: 1, Y: 2, Z: 30})
	sent := time.Now()
	h.send(opcode.MSGMoveTeleportAck, w.Bytes())
	h.expect(opcode.MSGMoveTeleportAck)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	quick("teleport ack", sent)

	sent = time.Now()
	h.send(opcode.SMSGMoveKnockBack, gametest.KnockBack(me.GUID, 5, 0, 1, 8, -15)) // 1.5 s flight
	ack, _ := ackMove(t, h.expect(opcode.CMSGMoveKnockBackAck), 5)
	quick("knockback ack", sent)
	sent = time.Now()
	h.send(opcode.SMSGForceRunSpeedChange, gametest.SpeedChange(me.GUID, 6, 7))
	h.expect(opcode.CMSGForceRunSpeedChangeAck)
	quick("speed ack", sent)
	last := time.Now()
	for time.Since(sent) < time.Second {
		h.expect(opcode.CMSGPing)
		if d := time.Since(last); d > 450*time.Millisecond {
			t.Fatalf("a ping %v after the one before", d)
		}
		last = time.Now()
	}

	land, err := h.srv.Expect(opcode.MSGMoveFallLand, 3*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	steps := []game.MoveInfo{ack}
	for _, p := range h.srv.Skipped() {
		if p.Op == opcode.MSGMoveHeartbeat {
			steps = append(steps, readMove(p.Body))
		}
	}
	lm := readMove(land.Body)
	steps = append(steps, lm)
	if len(steps) < 4 {
		t.Fatalf("%d steps in a 1.5 s flight", len(steps))
	}
	for i := 1; i < len(steps); i++ {
		if gap := steps[i].Time - steps[i-1].Time; gap > 700 {
			t.Fatalf("step %d: %d ms after the one before", i, gap)
		}
	}
	// Late: straight up and down where the arc began, on ground the server knows.
	if lm.X != ack.X || lm.Y != ack.Y || lm.Z != ack.Z {
		t.Fatalf("landing %+v, want it back at the knockback's start %+v", lm, ack)
	}
	for i := 1; i < len(steps); i++ {
		stepWithin(t, i, steps[i-1], steps[i], defaultRunSpeed)
	}
	close(stall.release) // the stalled queries answer now, too late
	time.Sleep(100 * time.Millisecond)
	if st := h.s.Status(); st.Falling || st.Z != lm.Z || st.Y != lm.Y || !st.ZUnknown {
		t.Fatalf("after the late answers %+v", st)
	}
	h.logout()
	for _, p := range h.srv.Skipped() {
		if p.Op == opcode.MSGMoveFallLand {
			t.Fatal("a second landing")
		}
	}
	for _, e := range ofKind(h.s.Log(), events.KindSession) {
		if e.Data["state"] == "landed" && (e.Data["floor"] != "guess" || e.Data["floor_late"] != true) {
			t.Fatalf("landed event %v", e.Data)
		}
		if e.Data["state"] == "falling" {
			t.Fatalf("the teleport's late floor search started a fall: %v", e.Data)
		}
	}
}

// A teleport or a second knockback that ends a flight still waiting for its trace does not
// lose the flight's knocked_back event: it is emitted, with the guess landing, before what
// replaced it.
func TestFlightReplacedWhileTracing(t *testing.T) {
	tp := packet.NewWriter().PackedGUID(me.GUID).U32(7)
	game.WriteMoveInfo(tp, game.MoveInfo{X: 1, Y: 2, Z: 30})
	for _, c := range []struct {
		name string
		op   uint16
		body []byte
		ack  uint16
		want string
	}{
		{"teleport", opcode.MSGMoveTeleportAck, tp.Bytes(), opcode.MSGMoveTeleportAck, "knocked_back,teleported"},
		{"knockback", opcode.SMSGMoveKnockBack, gametest.KnockBack(me.GUID, 6, 1, 0, 5, -4), opcode.CMSGMoveKnockBackAck,
			"knocked_back,knocked_back"},
	} {
		t.Run(c.name, func(t *testing.T) {
			stall := stalledPlanner{stall: 3 * time.Second, release: make(chan struct{})}
			defer close(stall.release)
			h := start(t, Options{Planner: stall})
			h.enterWorld()
			h.send(opcode.SMSGMoveKnockBack, gametest.KnockBack(me.GUID, 5, 0, 1, 8, -15))
			h.expect(opcode.CMSGMoveKnockBackAck)
			h.send(c.op, c.body)
			h.expect(c.ack)
			waitFor(t, func() bool {
				var got []string
				for _, e := range ofKind(h.s.Log(), events.KindSession) {
					if st := e.Data["state"]; st == "knocked_back" || st == "teleported" {
						got = append(got, st.(string))
					}
				}
				return strings.Join(got, ",") == c.want
			})
		})
	}
}
