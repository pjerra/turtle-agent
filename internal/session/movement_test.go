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
	"github.com/TortoiseBots/turtle-agent/internal/opcode"
	"github.com/TortoiseBots/turtle-agent/internal/packet"
	"github.com/TortoiseBots/turtle-agent/internal/worldconn"
)

func readMove(body []byte) game.MoveInfo { return game.ReadMoveInfo(packet.NewReader(body)) }

// ackMove reads an ack: u64 guid, u32 counter, MovementInfo, and what follows.
func ackMove(t *testing.T, p worldconn.Packet, counter uint32) (game.MoveInfo, *packet.Reader) {
	t.Helper()
	r := packet.NewReader(p.Body)
	if g, c := r.GUID(), r.U32(); g != me.GUID || c != counter {
		t.Fatalf("%s: guid %s counter %d, want %d", opcode.Name(p.Op), g, c, counter)
	}
	return game.ReadMoveInfo(r), r
}

// stepWithin checks one step against the anticheat's bound for the movement before it
// (Movement::GetMaxAllowedDist, Movement.cpp:849-890, checked at :574-595), without the
// server's 0.5-yard margin: a step that passes here passes there with room to spare.
func stepWithin(t *testing.T, i int, a, b game.MoveInfo, run float64) {
	t.Helper()
	if b.Time < a.Time || b.Time == 0 {
		t.Fatalf("step %d: client time %d after %d", i, b.Time, a.Time)
	}
	dt := float64(b.Time-a.Time) / 1000
	var dxy, dz float64
	switch {
	case a.Flags&game.MoveMaskMoving == 0:
	case a.Flags&(game.MoveJumping|game.MoveFallingFar) != 0:
		term := game.TerminalVelocity
		if a.Flags&game.MoveSafeFall != 0 { // Movement.cpp:875-881
			term = game.TerminalSafeFall
		}
		dxy, dz = float64(a.JumpXYSpeed)*dt, term*dt
	default:
		dxy, dz = run*dt, run*dt
	}
	const eps = 0.01
	if d := math.Hypot(float64(b.X-a.X), float64(b.Y-a.Y)); d > dxy+eps {
		t.Fatalf("step %d: %.3f yd across in %.3f s, bound %.3f (%+v -> %+v)", i, d, dt, dxy, a, b)
	}
	if d := math.Abs(float64(b.Z - a.Z)); d > dz+eps {
		t.Fatalf("step %d: %.3f yd up or down in %.3f s, bound %.3f: OVERSPEED_Z (%+v -> %+v)", i, d, dt, dz, a, b)
	}
}

// T23 deferred item / review M3: a knockback is acked with MoveJumping and the order's jump
// values, the flight sends heartbeats along the ballistic arc and lands with
// MSG_MOVE_FALL_LAND where the arc meets the starting height; every step stays inside the
// anticheat bound. The landing z is a guess until the server sends a position.
func TestKnockBackAckFlightAndLanding(t *testing.T) {
	ctx := context.Background()
	h := start(t, Options{})
	h.enterWorld()
	h.send(opcode.SMSGMoveKnockBack, gametest.KnockBack(me.GUID, 9, 0, 1, 8, -9.5)) // north, 8 yd/s, up 9.5
	ack, r := ackMove(t, h.expect(opcode.CMSGMoveKnockBackAck), 9)
	if r.Remaining() != 0 || ack.Flags != game.MoveJumping || ack.X != me.X || ack.Y != me.Y || ack.Z != me.Z || ack.Time == 0 {
		t.Fatalf("ack %+v (%d left)", ack, r.Remaining())
	}
	if ack.JumpCos != 0 || ack.JumpSin != 1 || ack.JumpXYSpeed != 8 || ack.JumpZSpeed != -9.5 || ack.FallTime != 0 {
		t.Fatalf("ack jump %+v", ack)
	}
	if st := h.s.Status(); !st.Falling {
		t.Fatalf("status during the flight %+v", st)
	}
	if err := h.s.WalkTo(ctx, me.X+1, me.Y, me.Z); err == nil || !strings.Contains(err.Error(), "knockback") {
		t.Fatalf("walk in the air: %v", err)
	}
	land, err := h.srv.Expect(opcode.MSGMoveFallLand, 3*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	steps := []game.MoveInfo{ack}
	for _, p := range h.srv.Skipped() {
		if p.Op == opcode.MSGMoveHeartbeat {
			mi := readMove(p.Body)
			// Up to the apex (9.5 / g = 0.49 s) MoveJumping alone, then MoveFallingFar too.
			down := float64(mi.FallTime)/1000 > 9.5/game.Gravity
			if mi.Flags&^game.MoveFallingFar != game.MoveJumping || (mi.Flags&game.MoveFallingFar != 0) != down ||
				mi.JumpXYSpeed != 8 || mi.FallTime == 0 || mi.Z <= me.Z || mi.Y <= me.Y {
				t.Fatalf("flight heartbeat %+v", mi)
			}
			steps = append(steps, mi)
		}
	}
	lm := readMove(land.Body)
	steps = append(steps, lm)
	if len(steps) < 3 {
		t.Fatalf("no heartbeat during a %.2f s flight", game.FallLandTime(-9.5, game.TerminalVelocity))
	}
	for i := 1; i < len(steps); i++ {
		stepWithin(t, i, steps[i-1], steps[i], defaultRunSpeed)
	}
	flight := game.FallLandTime(-9.5, game.TerminalVelocity)
	if lm.Flags != 0 || lm.Z != me.Z || math.Abs(float64(lm.Y-me.Y)-8*flight) > 0.01 || lm.X != me.X ||
		math.Abs(float64(lm.FallTime)-flight*1000) > 1 {
		t.Fatalf("landing %+v, want y %.3f after %.0f ms", lm, me.Y+float32(8*flight), flight*1000)
	}
	waitFor(t, func() bool { st := h.s.Status(); return !st.Falling && st.Y == lm.Y })
	if st := h.s.Status(); !st.ZUnknown {
		t.Fatalf("landing z is a guess: %+v", st)
	}
	// The server's next position for the player, a spline on it, settles the z.
	h.send(opcode.SMSGMonsterMove, selfSpline([3]float32{lm.X, lm.Y, lm.Z}, [3]float32{lm.X, lm.Y, me.Z - 2.5}, 1))
	waitFor(t, func() bool { st := h.s.Status(); return st.Z == me.Z-2.5 && !st.ZUnknown })
	time.Sleep(20 * time.Millisecond) // the spline has run
	if err := h.s.WalkTo(ctx, lm.X+2, lm.Y, me.Z-2.5); err != nil {
		t.Fatal(err)
	}
	if mi := readMove(h.expect(opcode.MSGMoveStartForward).Body); mi.Z != me.Z-2.5 || mi.Y != lm.Y {
		t.Fatalf("walk starts at %+v", mi)
	}
	h.expect(opcode.MSGMoveStop)
	h.logout()
	var states []string
	for _, e := range ofKind(h.s.Log(), events.KindSession) {
		if s := e.Data["state"]; s == "knocked_back" || s == "landed" {
			states = append(states, s.(string))
		}
	}
	if strings.Join(states, ",") != "knocked_back,landed" {
		t.Fatalf("events %v", states)
	}
}

// A knockback mid-walk throws the player from where the server last had it, the walk's
// last heartbeat, not from where the walk has got to since; the walk ends.
func TestKnockBackMidWalk(t *testing.T) {
	ctx := context.Background()
	h := start(t, Options{})
	h.enterWorld()
	walked := make(chan error, 1)
	go func() { walked <- h.s.WalkTo(ctx, me.X+20, me.Y, me.Z) }()
	h.expect(opcode.MSGMoveStartForward)
	beat := readMove(h.expect(opcode.MSGMoveHeartbeat).Body)
	time.Sleep(200 * time.Millisecond)
	h.send(opcode.SMSGMoveKnockBack, gametest.KnockBack(me.GUID, 3, -1, 0, 5, -4))
	ack, _ := ackMove(t, h.expect(opcode.CMSGMoveKnockBackAck), 3)
	if ack.X != beat.X || ack.Y != beat.Y || ack.Flags != game.MoveJumping || ack.Time < beat.Time {
		t.Fatalf("ack %+v after heartbeat %+v", ack, beat)
	}
	if err := <-walked; err == nil || !strings.Contains(err.Error(), "knocked back") {
		t.Fatalf("walk: %v", err)
	}
	lm := readMove(h.expect(opcode.MSGMoveFallLand).Body)
	if lm.X >= ack.X || lm.Z != ack.Z {
		t.Fatalf("landing %+v after ack %+v", lm, ack)
	}
	h.logout()
}

// A knockback for another unit (a pet the server never lets this client move) is not acked.
func TestKnockBackForOthersIgnored(t *testing.T) {
	h := start(t, Options{})
	h.enterWorld()
	h.send(opcode.SMSGMoveKnockBack, gametest.KnockBack(wolfGUID, 3, 1, 0, 5, -4))
	h.logout()
	for _, p := range h.srv.Skipped() {
		if p.Op == opcode.CMSGMoveKnockBackAck {
			t.Fatal("acked another unit's knockback")
		}
	}
}

// selfSpline is SMSG_MONSTER_MOVE for the player, a fear or charge ending at dest after
// ms milliseconds (MoveSplineInit.cpp:134-156, packet_builder.cpp:45-152).
func selfSpline(from, dest [3]float32, ms uint32) []byte {
	return packet.NewWriter().PackedGUID(me.GUID).F32(from[0]).F32(from[1]).F32(from[2]).U32(1).U8(0).U32(0x100).U32(ms).U32(1).
		F32(dest[0]).F32(dest[1]).F32(dest[2]).Bytes()
}

// Item 4: a walk that climbs steeply is timed on its 3D length, so no step changes height
// faster than the run speed the anticheat allows (OVERSPEED_Z, Movement.cpp:593-595); the
// first step leaves from exactly where the server has the player.
func TestSteepWalkStaysInsideTheZBound(t *testing.T) {
	ctx := context.Background()
	h := start(t, Options{})
	h.enterWorld()
	walked := make(chan error, 1)
	go func() { walked <- h.s.WalkTo(ctx, me.X+2, me.Y, me.Z+15) }()
	stop, err := h.srv.Expect(opcode.MSGMoveStop, 5*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if err := <-walked; err != nil {
		t.Fatal(err)
	}
	steps := []game.MoveInfo{{X: me.X, Y: me.Y, Z: me.Z}} // the login position, standing
	for _, p := range h.srv.Skipped() {
		if p.Op == opcode.MSGMoveStartForward || p.Op == opcode.MSGMoveHeartbeat {
			steps = append(steps, readMove(p.Body))
		}
	}
	steps = append(steps, readMove(stop.Body))
	if len(steps) < 6 {
		t.Fatalf("%d steps for a 2.2 s walk", len(steps))
	}
	steps[0].Time = steps[1].Time
	for i := 1; i < len(steps); i++ {
		stepWithin(t, i, steps[i-1], steps[i], defaultRunSpeed)
	}
	if st := h.s.Status(); st.Z != me.Z+15 || !st.ZUnknown {
		t.Fatalf("a walk that climbed leaves z a guess: %+v", st)
	}
	h.logout()
}

// Review M13: after a GM teleport the z may be in the air and stays a guess; the core sends
// no movement block for the player, so the next server spline on it (a fear or charge)
// settles it, and the next walk starts at the spline's end.
func TestTeleportZSettlesOnTheNextServerPosition(t *testing.T) {
	ctx := context.Background()
	h := start(t, Options{})
	h.enterWorld()
	if st := h.s.Status(); st.ZUnknown || st.Falling {
		t.Fatalf("at login %+v", st)
	}
	done := make(chan error, 1)
	go func() { done <- h.s.Teleport(ctx, 1, 2, 30, -1) }()
	h.expect(opcode.CMSGMessagechat)
	w := packet.NewWriter().PackedGUID(me.GUID).U32(7)
	game.WriteMoveInfo(w, game.MoveInfo{X: 1, Y: 2, Z: 30})
	h.send(opcode.MSGMoveTeleportAck, w.Bytes())
	h.expect(opcode.MSGMoveTeleportAck)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if st := h.s.Status(); st.Z != 30 || !st.ZUnknown {
		t.Fatalf("after .go xyz %+v", st)
	}
	h.send(opcode.SMSGMonsterMove, selfSpline([3]float32{1, 2, 30}, [3]float32{4, 2, 24.5}, 50))
	waitFor(t, func() bool { st := h.s.Status(); return st.Z == 24.5 && st.X == 4 && !st.ZUnknown })
	time.Sleep(80 * time.Millisecond) // the spline has run
	if err := h.s.WalkTo(ctx, 6, 2, 24.5); err != nil {
		t.Fatal(err)
	}
	if mi := readMove(h.expect(opcode.MSGMoveStartForward).Body); mi.Z != 24.5 || mi.X != 4 {
		t.Fatalf("walk starts at %+v", mi)
	}
	h.expect(opcode.MSGMoveStop)
	h.logout()
}

// T24 minor: a walk slower than the RPC client's 35 s wait is refused up front.
func TestSlowWalkRefused(t *testing.T) {
	ctx := context.Background()
	h := start(t, Options{})
	h.enterWorld()
	h.send(opcode.SMSGForceRunSpeedChange, gametest.SpeedChange(me.GUID, 1, 1))
	h.expect(opcode.CMSGForceRunSpeedChangeAck)
	if err := h.s.WalkTo(ctx, me.X+35, me.Y, me.Z); err == nil || !strings.Contains(err.Error(), "would take 35 s") {
		t.Fatalf("slow walk: %v", err)
	}
	h.logout()
}

// T23 deferred item: the water-walk, hover and feather-fall orders are acked with their
// own counter, MovementInfo and the u32 apply (MovementHandler.cpp:543-558); the flag stays
// in later movement until the removal order, and feather fall caps a knockback's descent.
func TestToggleOrdersAcked(t *testing.T) {
	ctx := context.Background()
	h := start(t, Options{})
	h.enterWorld()
	cases := []struct {
		order, ack uint16
		flag       uint32
		apply      bool
	}{
		{opcode.SMSGMoveWaterWalk, opcode.CMSGMoveWaterWalkAck, game.MoveWaterWalking, true},
		{opcode.SMSGMoveSetHover, opcode.CMSGMoveHoverAck, game.MoveHover, true},
		{opcode.SMSGMoveFeatherFall, opcode.CMSGMoveFeatherFallAck, game.MoveSafeFall, true},
		{opcode.SMSGMoveLandWalk, opcode.CMSGMoveWaterWalkAck, game.MoveWaterWalking, false},
		{opcode.SMSGMoveUnsetHover, opcode.CMSGMoveHoverAck, game.MoveHover, false},
	}
	want := uint32(0)
	var lastTime uint32
	for i, c := range cases {
		h.send(c.order, gametest.MovementOrder(me.GUID, uint32(20+i)))
		mi, r := ackMove(t, h.expect(c.ack), uint32(20+i))
		if c.apply {
			want |= c.flag
		} else {
			want &^= c.flag
		}
		apply := r.U32()
		if r.Err() != nil || r.Remaining() != 0 || (apply == 1) != c.apply || apply > 1 {
			t.Fatalf("%s: apply %d, err %v, %d left", opcode.Name(c.ack), apply, r.Err(), r.Remaining())
		}
		if mi.Flags != want || mi.X != me.X || mi.Time == 0 || mi.Time < lastTime {
			t.Fatalf("%s: %+v, want flags %#x", opcode.Name(c.ack), mi, want)
		}
		lastTime = mi.Time
	}
	// Feather fall is still on: it rides along in a walk and caps the knockback's fall.
	if err := h.s.WalkTo(ctx, me.X+1, me.Y, me.Z); err != nil {
		t.Fatal(err)
	}
	if mi := readMove(h.expect(opcode.MSGMoveStartForward).Body); mi.Flags != game.MoveForward|game.MoveSafeFall {
		t.Fatalf("walk flags %#x", mi.Flags)
	}
	h.expect(opcode.MSGMoveStop)
	h.send(opcode.SMSGMoveKnockBack, gametest.KnockBack(me.GUID, 30, 1, 0, 2, -12))
	ack, _ := ackMove(t, h.expect(opcode.CMSGMoveKnockBackAck), 30)
	if ack.Flags != game.MoveJumping|game.MoveSafeFall {
		t.Fatalf("knockback ack flags %#x", ack.Flags)
	}
	land, err := h.srv.Expect(opcode.MSGMoveFallLand, 5*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	// Under feather fall the arc also rises no faster than 7 yd/s (Movement.cpp:874-881).
	if lm, want := readMove(land.Body), game.FallLandTime(-7, game.TerminalSafeFall); math.Abs(float64(lm.FallTime)-want*1000) > 1 {
		t.Fatalf("feather-fall landing after %d ms, want %.0f", lm.FallTime, want*1000)
	}
	h.send(opcode.SMSGMoveNormalFall, gametest.MovementOrder(me.GUID, 31))
	if mi, r := ackMove(t, h.expect(opcode.CMSGMoveFeatherFallAck), 31); mi.Flags != 0 || r.U32() != 0 {
		t.Fatalf("normal fall ack %+v", mi)
	}
	// An order for another unit is not acked.
	h.send(opcode.SMSGMoveSetHover, gametest.MovementOrder(wolfGUID, 32))
	h.logout()
	for _, p := range h.srv.Skipped() {
		if p.Op == opcode.CMSGMoveHoverAck {
			t.Fatal("acked another unit's hover")
		}
	}
}

// Review fix 1: feather fall acked mid-flight. From the ack on the server bounds the fall by
// 7 yd/s (Movement.cpp:874-881), so the arc restarts from where it is with the speed it has,
// capped at 7, and every later beat stays inside that bound.
func TestFeatherFallMidFlight(t *testing.T) {
	h := start(t, Options{})
	h.enterWorld()
	h.send(opcode.SMSGMoveKnockBack, gametest.KnockBack(me.GUID, 40, 1, 0, 3, -15))
	ack, _ := ackMove(t, h.expect(opcode.CMSGMoveKnockBackAck), 40)
	time.Sleep(time.Second - 5*time.Millisecond)
	h.send(opcode.SMSGMoveFeatherFall, gametest.MovementOrder(me.GUID, 41))
	ff := h.expect(opcode.CMSGMoveFeatherFallAck)
	land, err := h.srv.Expect(opcode.MSGMoveFallLand, 5*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	fmi, _ := ackMove(t, ff, 41)
	if fmi.Flags != game.MoveJumping|game.MoveFallingFar|game.MoveSafeFall || fmi.Z <= me.Z { // past the apex (0.78 s)
		t.Fatalf("feather-fall ack in the air %+v", fmi)
	}
	steps := []game.MoveInfo{ack, fmi}
	for _, p := range h.srv.Skipped() {
		if p.Op == opcode.MSGMoveHeartbeat {
			steps = append(steps, readMove(p.Body))
		}
	}
	sort.Slice(steps, func(i, j int) bool { return steps[i].Time < steps[j].Time })
	lm := readMove(land.Body)
	steps = append(steps, lm)
	if len(steps) < 5 {
		t.Fatalf("%d steps", len(steps))
	}
	for i := 1; i < len(steps); i++ {
		stepWithin(t, i, steps[i-1], steps[i], defaultRunSpeed)
	}
	if lm.Z != me.Z || lm.Flags != game.MoveSafeFall {
		t.Fatalf("landing %+v", lm)
	}
	h.logout()
}

// Review fix 3: an aura already on at login (or after a far teleport) shows only as a flag in
// the player's create block (Object.cpp:436-441); the toggles are seeded from it, as the run
// speed is, so later movement carries them before any order arrives.
func TestTogglesSeededFromCreateBlock(t *testing.T) {
	ctx := context.Background()
	h := start(t, Options{})
	h.enterWorld()
	h.send(opcode.SMSGUpdateObject, selfCreateFlags(7, game.MoveSafeFall|game.MoveHover|game.MoveWalkMode))
	waitFor(t, func() bool { return h.s.Status().MaxHealth == 100 })
	if err := h.s.WalkTo(ctx, me.X+1, me.Y, me.Z); err != nil {
		t.Fatal(err)
	}
	if mi := readMove(h.expect(opcode.MSGMoveStartForward).Body); mi.Flags != game.MoveForward|game.MoveSafeFall|game.MoveHover {
		t.Fatalf("walk flags %#x", mi.Flags)
	}
	h.expect(opcode.MSGMoveStop)
	h.logout()
}
