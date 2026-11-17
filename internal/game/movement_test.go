package game

import (
	"encoding/hex"
	"testing"

	"github.com/TortoiseBots/turtle-agent/internal/packet"
)

func TestMoveInfoRoundTrip(t *testing.T) {
	plain := MoveInfo{Flags: MoveForward, Time: 1234, X: 1, Y: 2, Z: 3, O: 0.5, FallTime: 0}
	w := packet.NewWriter()
	WriteMoveInfo(w, plain)
	if w.Len() != 28 { // flags, time, 4 floats, fall time (Object.cpp:146-166)
		t.Fatalf("plain move info is %d bytes", w.Len())
	}
	if got := ReadMoveInfo(packet.NewReader(w.Bytes())); got != plain {
		t.Fatalf("%+v", got)
	}
	full := MoveInfo{Flags: MoveOnTransport | MoveSwimming | MoveJumping | MoveSplineElevation, X: 1, Transport: 0x1FC0000000000005,
		TX: 1, TY: 2, TZ: 3, TO: 4, Pitch: 0.1, FallTime: 9, JumpZSpeed: 1, JumpCos: 2, JumpSin: 3, JumpXYSpeed: 4, SplineElevation: 5}
	w = packet.NewWriter()
	WriteMoveInfo(w, full)
	r := packet.NewReader(w.Bytes())
	if got := ReadMoveInfo(r); got != full || r.Remaining() != 0 {
		t.Fatalf("%+v remaining %d", got, r.Remaining())
	}
}

// The order of the optional parts, written by hand from MovementInfo::Write
// (Object.cpp:146-180): transport guid + 4 floats, pitch, fall time, jump, elevation.
func TestMoveInfoFieldOrder(t *testing.T) {
	flags := MoveOnTransport | MoveSwimming | MoveJumping | MoveSplineElevation
	body := packet.NewWriter().U32(flags).U32(77).F32(1).F32(2).F32(3).F32(4).
		U64(0x1FC0000000000005).F32(5).F32(6).F32(7).F32(8). // transport (:155-162)
		F32(9).                                              // pitch (:163-164)
		U32(10).                                             // fall time (:166)
		F32(11).F32(12).F32(13).F32(14).                     // jump (:168-174)
		F32(15).Bytes()                                      // spline elevation (:176-179)
	r := packet.NewReader(body)
	m := ReadMoveInfo(r)
	want := MoveInfo{Flags: flags, Time: 77, X: 1, Y: 2, Z: 3, O: 4, Transport: 0x1FC0000000000005, TX: 5, TY: 6, TZ: 7, TO: 8,
		Pitch: 9, FallTime: 10, JumpZSpeed: 11, JumpCos: 12, JumpSin: 13, JumpXYSpeed: 14, SplineElevation: 15}
	if m != want || r.Remaining() != 0 || r.Err() != nil {
		t.Fatalf("%+v remaining %d err %v", m, r.Remaining(), r.Err())
	}
	// Flags without an optional part (forward, walk, root, falling far, spline enabled)
	// add no bytes.
	w := packet.NewWriter()
	WriteMoveInfo(w, MoveInfo{Flags: MoveForward | MoveBackward | MoveWalkMode | MoveRoot | MoveFallingFar | MoveSplineEnabled})
	if w.Len() != 28 {
		t.Fatalf("flags without optional parts give %d bytes", w.Len())
	}
}

func TestMoveInfoTruncated(t *testing.T) {
	w := packet.NewWriter()
	WriteMoveInfo(w, MoveInfo{Flags: MoveJumping})
	r := packet.NewReader(w.Bytes()[:w.Len()-1])
	ReadMoveInfo(r)
	if r.Err() == nil {
		t.Fatal("truncated jump data accepted")
	}
}

// Golden bytes (struct.pack '<IIffffIffff' / '<IIffffI'): a heartbeat on the way down
// carries MoveJumping|MoveFallingFar, the fall time and the four jump floats; the landing
// carries no flags and the fall time, and no jump floats.
func TestFallPacketsGoldenBytes(t *testing.T) {
	for _, c := range []struct {
		name string
		m    MoveInfo
		want string
	}{
		{"falling heartbeat", MoveInfo{Flags: MoveJumping | MoveFallingFar, Time: 1234, X: 1.5, Y: -2.25, Z: 30, O: 0.5,
			FallTime: 750, JumpZSpeed: -9.5, JumpCos: 0, JumpSin: 1, JumpXYSpeed: 8},
			"00600000d20400000000c03f000010c00000f0410000003fee020000000018c1000000000000803f00000041"},
		{"fall land", MoveInfo{Time: 2000, X: 1.5, Y: 5, Z: 10, O: 0.5, FallTime: 1440},
			"00000000d00700000000c03f0000a040000020410000003fa0050000"},
	} {
		if got := hex.EncodeToString(BuildMovement(c.m)); got != c.want {
			t.Errorf("%s:\n got %s\nwant %s", c.name, got, c.want)
		}
	}
}
