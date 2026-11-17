package game_test

import (
	"bytes"
	"math"
	"testing"

	"github.com/TortoiseBots/turtle-agent/internal/game"
	"github.com/TortoiseBots/turtle-agent/internal/game/gametest"
	"github.com/TortoiseBots/turtle-agent/internal/packet"
)

const self = packet.GUID(0x2A)

// SMSG_MOVE_KNOCK_BACK is packed guid, counter, cos, sin, xy speed, z speed
// (MovementPacketSender.cpp:198-205); the z speed is the server's -verticalSpeed
// (Unit.cpp:10674), so a throw upwards arrives negative.
func TestKnockBackParse(t *testing.T) {
	kb, err := game.ParseKnockBack(gametest.KnockBack(self, 9, 0.6, -0.8, 12, -9.5))
	if err != nil {
		t.Fatal(err)
	}
	want := game.KnockBack{GUID: self, Counter: 9, Cos: 0.6, Sin: -0.8, XYSpeed: 12, ZSpeed: -9.5}
	if kb != want {
		t.Fatalf("%+v", kb)
	}
	body := gametest.KnockBack(self, 9, 0.6, -0.8, 12, -9.5)
	if _, err := game.ParseKnockBack(body[:len(body)-1]); err == nil {
		t.Fatal("truncated knockback accepted")
	}
}

// CMSG_MOVE_KNOCK_BACK_ACK is u64 guid, counter, MovementInfo (MovementHandler.cpp:743-750).
// The server keeps the pending change only when the ack carries MoveJumping and jump values
// within 0.01 of the order (Unit.cpp:7479-7495): the builder copies them from the order.
func TestKnockBackAckCarriesTheOrdersJump(t *testing.T) {
	kb := game.KnockBack{GUID: self, Counter: 9, Cos: 0.6, Sin: -0.8, XYSpeed: 12, ZSpeed: -9.5}
	b := game.BuildKnockBackAck(kb, game.MoveInfo{Flags: game.MoveSafeFall, Time: 77, X: 1, Y: 2, Z: 3, O: 4})
	r := packet.NewReader(b)
	if r.GUID() != self || r.U32() != 9 {
		t.Fatal("ack header")
	}
	mi := game.ReadMoveInfo(r)
	if r.Remaining() != 0 || r.Err() != nil {
		t.Fatalf("ack tail: %d left, %v", r.Remaining(), r.Err())
	}
	if mi.Flags != game.MoveSafeFall|game.MoveJumping || mi.Time != 77 || mi.X != 1 || mi.Z != 3 {
		t.Fatalf("%+v", mi)
	}
	for _, d := range []float32{mi.JumpCos - kb.Cos, mi.JumpSin - kb.Sin, mi.JumpXYSpeed - kb.XYSpeed, mi.JumpZSpeed - kb.ZSpeed} {
		if math.Abs(float64(d)) > 0.01 {
			t.Fatalf("jump values %+v off the order %+v", mi, kb)
		}
	}
}

// The water-walk, hover and feather-fall orders are packed guid + counter
// (MovementPacketSender.cpp:273-276); their acks end in a u32 apply after MovementInfo
// (MovementHandler.cpp:549-558).
func TestToggleOrderAndAck(t *testing.T) {
	c, err := game.ParseMovementChange(gametest.MovementOrder(self, 11), false)
	if err != nil || c.GUID != self || c.Counter != 11 {
		t.Fatalf("%+v %v", c, err)
	}
	m := game.MoveInfo{Flags: game.MoveWaterWalking, Time: 5, X: 1}
	for _, apply := range []bool{true, false} {
		b := game.BuildMovementToggleAck(self, 11, m, apply)
		want := packet.NewWriter().GUID(self).U32(11)
		game.WriteMoveInfo(want, m)
		v := uint32(0)
		if apply {
			v = 1
		}
		if !bytes.Equal(b, want.U32(v).Bytes()) {
			t.Fatalf("apply %v: % X", apply, b)
		}
	}
}

// CMSG_CANCEL_CAST is the spell id alone (SpellHandler.cpp:411-414); 0 cancels whatever
// is being cast (Object.cpp:5428-5440).
func TestCancelCast(t *testing.T) {
	if b := game.BuildCancelCast(133); !bytes.Equal(b, packet.NewWriter().U32(133).Bytes()) {
		t.Fatalf("% X", b)
	}
}

// The fall follows the core's spline physics: gravity 19.2911, terminal speed 60.148, or
// 7 under feather fall (util.cpp:25-29, :59-83).
func TestFallPhysics(t *testing.T) {
	g := game.Gravity
	// A throw up at 9.5 yd/s is back at its start height after 2*9.5/g.
	land := game.FallLandTime(-9.5, game.TerminalVelocity)
	if math.Abs(land-2*9.5/g) > 1e-9 {
		t.Fatalf("land %v", land)
	}
	if d := game.FallDrop(land, -9.5, game.TerminalVelocity); math.Abs(d) > 1e-9 {
		t.Fatalf("drop at landing %v", d)
	}
	if apex := game.FallDrop(9.5/g, -9.5, game.TerminalVelocity); math.Abs(apex+9.5*9.5/(2*g)) > 1e-9 {
		t.Fatalf("apex %v", apex)
	}
	// Free fall from rest matches computeFallElevation(t) (util.cpp:85-99) both sides of
	// the terminal time.
	for _, tt := range []float64{0.5, 2, game.TerminalVelocity / g, 5} {
		want := tt * tt * g / 2
		if tt > game.TerminalVelocity/g {
			term := game.TerminalVelocity / g
			want = game.TerminalVelocity*(tt-term) + game.TerminalVelocity*game.TerminalVelocity/(2*g)
		}
		if d := game.FallDrop(tt, 0, game.TerminalVelocity); math.Abs(d-want) > 1e-6 {
			t.Fatalf("free fall %v s: %v want %v", tt, d, want)
		}
	}
	// Feather fall caps the downward speed at 7: the way down from the apex is longer.
	up, safe := game.FallLandTime(-9.5, game.TerminalVelocity), game.FallLandTime(-9.5, game.TerminalSafeFall)
	if safe <= up {
		t.Fatalf("feather fall lands at %v, normal at %v", safe, up)
	}
	if d := game.FallDrop(safe, -9.5, game.TerminalSafeFall); math.Abs(d) > 1e-9 {
		t.Fatalf("feather fall drop at landing %v", d)
	}
	// No speed ever passes the cap on the way down: a step's drop is at most cap*dt.
	for tt := 0.0; tt < safe; tt += 0.05 {
		if step := game.FallDrop(tt+0.05, -9.5, game.TerminalSafeFall) - game.FallDrop(tt, -9.5, game.TerminalSafeFall); step > game.TerminalSafeFall*0.05+1e-9 {
			t.Fatalf("step at %v drops %v", tt, step)
		}
	}
	// Thrown down or level: it lands at once.
	if game.FallLandTime(0, game.TerminalVelocity) != 0 || game.FallLandTime(3, game.TerminalVelocity) != 0 {
		t.Fatal("a fall that starts downwards has no flight")
	}
}

// FallSpeed and FallTimeTo restart a fall mid-air: the speed reached, and when a body with
// that speed has dropped a given distance (FallDrop's inverse).
func TestFallRestart(t *testing.T) {
	for _, c := range []struct{ v0, term, drop float64 }{
		{-15, game.TerminalVelocity, 0}, {-15, game.TerminalVelocity, 4}, {4.3, game.TerminalSafeFall, 5.35},
		{0, game.TerminalVelocity, 200}, {20, game.TerminalSafeFall, 10}, {-7, game.TerminalSafeFall, 0},
	} {
		tt := game.FallTimeTo(c.drop, c.v0, c.term)
		if d := game.FallDrop(tt, c.v0, c.term); math.Abs(d-c.drop) > 1e-6 {
			t.Fatalf("%+v: after %v s dropped %v", c, tt, d)
		}
	}
	if v := game.FallSpeed(1, -15, game.TerminalVelocity); math.Abs(v-(game.Gravity-15)) > 1e-9 {
		t.Fatalf("speed %v", v)
	}
	if v := game.FallSpeed(10, 0, game.TerminalSafeFall); v != game.TerminalSafeFall {
		t.Fatalf("capped speed %v", v)
	}
	if game.FallTimeTo(-1, 3, game.TerminalVelocity) != 0 {
		t.Fatal("a target above a falling body is reached at once")
	}
}
