package objects

import (
	"math"
	"testing"

	"github.com/TortoiseBots/turtle-agent/internal/objects/objtest"
	"github.com/TortoiseBots/turtle-agent/internal/packet"
)

func linearMove(g packet.GUID) []byte {
	return packet.NewWriter().PackedGUID(g).F32(1).F32(2).F32(3).U32(9).U8(0). // start, id, normal facing
											U32(0x100).U32(1500).U32(2).F32(11).F32(12).F32(13).U32(0x01020304).Bytes() // flags, duration, count, dest, one packed offset
}

func TestMonsterMoveLinearSmoothAndStop(t *testing.T) {
	m, err := ParseMonsterMove(linearMove(wolf))
	if err != nil || m.GUID != wolf || m.Dest != [3]float32{11, 12, 13} || m.Duration != 1500 || m.Stop {
		t.Fatalf("%+v %v", m, err)
	}
	smooth := packet.NewWriter().PackedGUID(wolf).F32(1).F32(2).F32(3).U32(9).U8(4).F32(0.5).
		U32(splineFlying).U32(800).U32(2).F32(4).F32(4).F32(4).F32(8).F32(8).F32(8).Bytes()
	if m, err := ParseMonsterMove(smooth); err != nil || m.Dest != [3]float32{8, 8, 8} {
		t.Fatalf("%+v %v", m, err)
	}
	stop := packet.NewWriter().PackedGUID(wolf).F32(5).F32(6).F32(7).U32(9).U8(1).Bytes() // MoveSplineInit.cpp:148-153
	if m, err := ParseMonsterMove(stop); err != nil || !m.Stop || m.Dest != [3]float32{5, 6, 7} {
		t.Fatalf("%+v %v", m, err)
	}
}

// The facing part of WriteCommonMonsterMovePart (packet_builder.cpp:52-71): 12 bytes for a
// spot, an 8-byte guid for a target, a 4-byte angle.
func TestMonsterMoveFacingForms(t *testing.T) {
	for _, c := range []struct {
		kind  uint8
		bytes int
	}{{2, 12}, {3, 8}, {4, 4}} {
		w := packet.NewWriter().PackedGUID(wolf).F32(1).F32(2).F32(3).U32(9).U8(c.kind).Append(make([]byte, c.bytes)).
			U32(0x100).U32(700).U32(1).F32(21).F32(22).F32(23)
		m, err := ParseMonsterMove(w.Bytes())
		if err != nil || m.Start != [3]float32{1, 2, 3} || m.ID != 9 || m.Duration != 700 || m.Dest != [3]float32{21, 22, 23} {
			t.Fatalf("facing %d: %+v %v", c.kind, m, err)
		}
	}
	unknown := packet.NewWriter().PackedGUID(wolf).F32(1).F32(2).F32(3).U32(9).U8(5).
		U32(0x100).U32(700).U32(1).F32(21).F32(22).F32(23).Bytes()
	if _, err := ParseMonsterMove(unknown); err == nil {
		t.Fatal("facing type 5 accepted")
	}
}

func TestMonsterMoveErrors(t *testing.T) {
	full := linearMove(wolf)
	if _, err := ParseMonsterMove(full[:len(full)-5]); err == nil {
		t.Fatal("truncated destination accepted")
	}
	if _, err := ParseMonsterMove(full[:len(full)-4]); err != nil {
		t.Fatalf("destination without the packed offsets: %v", err)
	}
	if _, err := ParseMonsterMove(full[:3]); err == nil {
		t.Fatal("truncated start accepted")
	}
	noPoints := packet.NewWriter().PackedGUID(wolf).F32(1).F32(2).F32(3).U32(9).U8(0).
		U32(splineFlying).U32(800).U32(0).Bytes()
	if _, err := ParseMonsterMove(noPoints); err == nil {
		t.Fatal("smooth path without points accepted")
	}
	tooMany := packet.NewWriter().PackedGUID(wolf).F32(1).F32(2).F32(3).U32(9).U8(0).
		U32(splineFlying).U32(800).U32(1 << 30).F32(4).F32(4).F32(4).Bytes()
	if _, err := ParseMonsterMove(tooMany); err == nil {
		t.Fatal("smooth path with more points than bytes accepted")
	}
	// A linear path's count is not used to parse (destination first, packed offsets
	// after it, packet_builder.cpp:78-112), so a large count is no reason to drop it.
	linear := packet.NewWriter().PackedGUID(wolf).F32(1).F32(2).F32(3).U32(9).U8(0).
		U32(0x100).U32(800).U32(5000).F32(4).F32(5).F32(6).Bytes()
	if m, err := ParseMonsterMove(linear); err != nil || m.Dest != [3]float32{4, 5, 6} {
		t.Fatalf("linear path with a large count: %+v %v", m, err)
	}
	long := packet.NewWriter().PackedGUID(wolf).F32(1).F32(2).F32(3).U32(9).U8(0).U32(splineFlying).U32(800).U32(2000)
	for i := 0; i < 2000; i++ {
		long.F32(float32(i)).F32(0).F32(50)
	}
	if m, err := ParseMonsterMove(long.Bytes()); err != nil || m.Dest != [3]float32{1999, 0, 50} {
		t.Fatalf("long smooth path: %+v %v", m, err)
	}
	shortSmooth := packet.NewWriter().PackedGUID(wolf).F32(1).F32(2).F32(3).U32(9).U8(0).
		U32(splineFlying).U32(800).U32(3).F32(4).F32(4).F32(4).Bytes()
	if _, err := ParseMonsterMove(shortSmooth); err == nil {
		t.Fatal("smooth path shorter than its count accepted")
	}
}

// Review Focus 2: SMSG_COMPRESSED_MOVES bundles several length-prefixed packets.
func TestCompressedMovesSplit(t *testing.T) {
	a := linearMove(wolf)
	b := packet.NewWriter().PackedGUID(wolf).Bytes()
	plain := packet.NewWriter().U8(uint8(len(a) + 2)).U16(0x0DD).Append(a).U8(uint8(len(b) + 2)).U16(0x304).Append(b).Bytes()
	subs, err := SplitCompressedMoves(objtest.Compress(plain))
	if err != nil || len(subs) != 2 || subs[0].Op != 0x0DD || len(subs[0].Body) != len(a) || subs[1].Op != 0x304 {
		t.Fatalf("%+v %v", subs, err)
	}
	if _, err := SplitCompressedMoves(objtest.Compress([]byte{1, 0xDD})); err == nil {
		t.Fatal("size below 2 accepted")
	}
}

func TestCompressedMovesEdges(t *testing.T) {
	// An entry of size 2 is an opcode with an empty body (MovementData::AddPacket,
	// UpdateData.cpp:187-198).
	plain := packet.NewWriter().U8(2).U16(0x0DD).Bytes()
	if subs, err := SplitCompressedMoves(objtest.Compress(plain)); err != nil || len(subs) != 1 || len(subs[0].Body) != 0 {
		t.Fatalf("%+v %v", subs, err)
	}
	if subs, err := SplitCompressedMoves(objtest.Compress(nil)); err != nil || len(subs) != 0 {
		t.Fatalf("empty bundle: %+v %v", subs, err)
	}
	// The second entry claims more bytes than remain: the first is still returned.
	a := linearMove(wolf)
	cut := packet.NewWriter().U8(uint8(len(a) + 2)).U16(0x0DD).Append(a).U8(40).U16(0x0DD).Append(a[:10]).Bytes()
	subs, err := SplitCompressedMoves(objtest.Compress(cut))
	if err == nil || len(subs) != 1 {
		t.Fatalf("%+v %v", subs, err)
	}
	if _, err := SplitCompressedMoves(objtest.Compress(plain)[:6]); err == nil {
		t.Fatal("truncated zlib accepted")
	}
}

// A destination of +Inf or 1e10 used to hang the session goroutine in linearPath's unwrap
// loop (float32 p-512 == p). Such a move is now rejected, bounded, and returns at once.
func TestMonsterMoveOutOfWorldRejected(t *testing.T) {
	for _, bad := range []float32{float32(math.Inf(1)), float32(math.Inf(-1)), float32(math.NaN()), 1e10, -3e38} {
		w := packet.NewWriter().PackedGUID(wolf).F32(1).F32(2).F32(3).U32(9).U8(0).
			U32(0x100).U32(1500).U32(2).F32(bad).F32(12).F32(13).U32(0x01020304)
		if _, err := ParseMonsterMove(w.Bytes()); err == nil {
			t.Errorf("linear destination %v accepted", bad)
		}
		s := packet.NewWriter().PackedGUID(wolf).F32(bad).F32(2).F32(3).U32(9).U8(0).
			U32(0x100).U32(1500).U32(1).F32(1).F32(1).F32(1)
		if _, err := ParseMonsterMove(s.Bytes()); err == nil {
			t.Errorf("start %v accepted", bad)
		}
		sm := packet.NewWriter().PackedGUID(wolf).F32(1).F32(2).F32(3).U32(9).U8(0).
			U32(splineFlying).U32(800).U32(1).F32(1).F32(bad).F32(1)
		if _, err := ParseMonsterMove(sm.Bytes()); err == nil {
			t.Errorf("smooth point %v accepted", bad)
		}
	}
}

// The unwrap picks the wrap nearest the previous point, in one step for any distance.
func TestLinearPathUnwrap(t *testing.T) {
	// offset 0xFF in x is -1 -> 0.25 yd past dest; a dest 5000 yd from start still lands within span/2 of it.
	path, err := linearPath(packet.NewReader(packet.NewWriter().U32(0x000003FF).Bytes()), [3]float32{0, 0, 0}, [3]float32{5000, 0, 0}, 2)
	if err != nil || len(path) != 3 {
		t.Fatalf("%v", path)
	}
	if d := path[1][0] - path[0][0]; d > 256 || d < -256 {
		t.Fatalf("unwrapped point %v not within half a span of the start", path[1])
	}
}

func FuzzParseMonsterMove(f *testing.F) {
	f.Add(linearMove(wolf))
	f.Add(packet.NewWriter().PackedGUID(wolf).F32(5).F32(6).F32(7).U32(9).U8(1).Bytes())
	f.Add(packet.NewWriter().PackedGUID(wolf).F32(1).F32(2).F32(3).U32(9).U8(4).F32(0.5).
		U32(splineFlying).U32(800).U32(2).F32(4).F32(4).F32(4).F32(8).F32(8).F32(8).Bytes())
	f.Add(packet.NewWriter().PackedGUID(wolf).F32(1).F32(2).F32(3).U32(9).U8(0).
		U32(0x100).U32(1500).U32(2).F32(float32(math.Inf(1))).F32(12).F32(13).U32(0x01020304).Bytes())
	f.Fuzz(func(t *testing.T, b []byte) {
		m, err := ParseMonsterMove(b)
		if err != nil {
			return
		}
		for _, p := range m.Path {
			if checkInWorld(p) != nil {
				t.Fatalf("accepted a path point out of the world: %v", p)
			}
		}
	})
}

// Each rebuilt point can sit up to 256 yd from the one before, so a crafted run of offsets
// walks the path out of the world even with the start and destination in range.
func TestMonsterMoveWalkedOutOfWorld(t *testing.T) {
	const n = 999
	w := packet.NewWriter().PackedGUID(wolf).F32(0).F32(0).F32(0).U32(9).U8(0).
		U32(0x100).U32(1500).U32(n + 1).F32(0).F32(0).F32(0)
	x := float32(0) // the x the parser rebuilds so far
	for i := 0; i < n; i++ {
		// pick the offset whose point is 255 yd past the previous one (mod the 512 yd span)
		off := int32(-(x + 255) * 4)
		off = (off&0x7FF ^ 0x400) - 0x400 // wrap to 11 signed bits
		w.U32(uint32(off) & 0x7FF)
		x += 255
	}
	if _, err := ParseMonsterMove(w.Bytes()); err == nil {
		t.Fatal("path walked out of the world accepted")
	}
}
