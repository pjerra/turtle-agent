package objects

import (
	"bytes"
	"compress/zlib"
	"errors"
	"testing"

	"github.com/TortoiseBots/turtle-agent/internal/game"
	"github.com/TortoiseBots/turtle-agent/internal/objects/objtest"
	"github.com/TortoiseBots/turtle-agent/internal/packet"
)

const wolf = packet.GUID(0xF13000012B000001)

func TestCreateLivingUnitBlock(t *testing.T) {
	w := objtest.Header(1).U8(uint8(BlockCreate2)).PackedGUID(wolf).U8(3)
	objtest.Living(w, 0x20|0x10, game.MoveInfo{X: 10, Y: 20, Z: 30, O: 1}, nil)
	objtest.Values(w, 188, map[uint16]uint32{UnitHealth: 100, UnitMaxHealth: 120, UnitLevel: 5, FieldEntry: 299})
	blocks, err := ParseUpdate(w.Bytes())
	if err != nil || len(blocks) != 1 {
		t.Fatalf("%v %v", blocks, err)
	}
	b := blocks[0]
	if b.Type != BlockCreate2 || b.GUID != wolf || b.ObjType != 3 || !b.Move.Living || b.Move.X != 10 ||
		b.Move.Speeds[1] != 2 || b.Values[UnitHealth] != 100 || b.Values[FieldEntry] != 299 {
		t.Fatalf("%+v %+v", b, b.Move)
	}
}

// Review Focus 2: several blocks inside one compressed packet.
func TestCompressedUpdateMultiBlock(t *testing.T) {
	w := objtest.Header(3)
	w.U8(uint8(BlockOutOfRange)).U32(2).PackedGUID(0xF130000000000009).PackedGUID(0x31)
	w.U8(uint8(BlockCreate)).PackedGUID(0x2A).U8(4)
	objtest.Living(w, 0x01|0x20|0x10, game.MoveInfo{X: -8913, Y: -136, Z: 82}, nil)
	objtest.Values(w, 1300, map[uint16]uint32{UnitHealth: 1000, UnitMaxHealth: 1000})
	w.U8(uint8(BlockValues)).PackedGUID(wolf)
	objtest.Values(w, 188, map[uint16]uint32{UnitHealth: 40})
	blocks, err := ParseCompressedUpdate(objtest.Compress(w.Bytes()))
	if err != nil || len(blocks) != 3 {
		t.Fatalf("%v %v", blocks, err)
	}
	if len(blocks[0].GUIDs) != 2 || blocks[1].GUID != 0x2A || blocks[1].Move.UpdateFlags&0x01 == 0 || blocks[2].Values[UnitHealth] != 40 {
		t.Fatalf("%+v", blocks)
	}
	if blocks[0].GUIDs[0] != 0xF130000000000009 || blocks[0].GUIDs[1] != 0x31 {
		t.Fatalf("out-of-range guids %v", blocks[0].GUIDs)
	}
}

// Review Focus 2: a moving creature's create block carries spline data before its values.
func TestSplineEnabledLivingBlock(t *testing.T) {
	w := objtest.Header(1).U8(uint8(BlockCreate)).PackedGUID(wolf).U8(3)
	objtest.Living(w, 0x20, game.MoveInfo{Flags: game.MoveForward | game.MoveSplineEnabled, X: 1}, func(w *packet.Writer) {
		w.U32(splineFinalTarget).U64(0x2A)                 // final target (packet_builder.cpp:166-167)
		w.U32(500).U32(2000).U32(77)                       // time passed, duration, id
		w.U32(2).F32(1).F32(1).F32(1).F32(2).F32(2).F32(2) // two nodes
		w.F32(5).F32(6).F32(7)                             // final destination
	})
	objtest.Values(w, 188, map[uint16]uint32{UnitHealth: 55})
	blocks, err := ParseUpdate(w.Bytes())
	if err != nil {
		t.Fatal(err)
	}
	s := blocks[0].Move.Spline
	if s == nil || s.FinalTarget != 0x2A || s.Duration != 2000 || len(s.Points) != 2 || s.Dest != [3]float32{5, 6, 7} || blocks[0].Values[UnitHealth] != 55 {
		t.Fatalf("%+v %+v", s, blocks[0].Values)
	}
}

// The three final-facing forms of PacketBuilder::WriteCreate (packet_builder.cpp:164-169),
// checked in the core's order (angle, then target, then point), and the no-facing form.
func TestSplineFinalFacingForms(t *testing.T) {
	cases := []struct {
		name   string
		facing func(*packet.Writer)
		check  func(*Spline) bool
	}{
		{"angle", func(w *packet.Writer) { w.U32(splineFinalAngle).F32(1.5) },
			func(s *Spline) bool { return s.FinalAngle == 1.5 }},
		{"point", func(w *packet.Writer) { w.U32(splineFinalPoint).F32(7).F32(8).F32(9) },
			func(s *Spline) bool { return s.FinalPoint == [3]float32{7, 8, 9} }},
		{"angle wins over point", func(w *packet.Writer) { w.U32(splineFinalAngle | splineFinalPoint).F32(2.5) },
			func(s *Spline) bool { return s.FinalAngle == 2.5 && s.FinalPoint == [3]float32{} }},
		{"none", func(w *packet.Writer) { w.U32(splineFlying) },
			func(s *Spline) bool { return s.Flags == splineFlying }},
	}
	for _, c := range cases {
		w := objtest.Header(1).U8(uint8(BlockCreate)).PackedGUID(wolf).U8(3)
		objtest.Living(w, 0x20, game.MoveInfo{Flags: game.MoveSplineEnabled}, func(w *packet.Writer) {
			c.facing(w)
			w.U32(0).U32(1000).U32(3).U32(1).F32(1).F32(2).F32(3).F32(4).F32(5).F32(6)
		})
		objtest.Values(w, 188, map[uint16]uint32{UnitHealth: 9})
		blocks, err := ParseUpdate(w.Bytes())
		if err != nil {
			t.Fatalf("%s: %v", c.name, err)
		}
		s := blocks[0].Move.Spline
		if s == nil || !c.check(s) || s.ID != 3 || s.Dest != [3]float32{4, 5, 6} || blocks[0].Values[UnitHealth] != 9 {
			t.Fatalf("%s: %+v %+v", c.name, s, blocks[0].Values)
		}
	}
}

// A node count is bounded by the bytes left, so a lying count fails before any loop.
func TestSplineNodeCountBounded(t *testing.T) {
	w := objtest.Header(1).U8(uint8(BlockCreate)).PackedGUID(wolf).U8(3)
	objtest.Living(w, 0x20, game.MoveInfo{Flags: game.MoveSplineEnabled}, func(w *packet.Writer) {
		w.U32(0).U32(0).U32(0).U32(0).U32(1 << 30)
	})
	if _, err := ParseUpdate(w.Bytes()); err == nil {
		t.Fatal("spline with more nodes than bytes accepted")
	}
}

// Taxi flights create long Catmull-Rom splines (WaypointMovementGenerator.cpp:364-372);
// a create block carrying one must parse, here with 2000 nodes inside a compressed update.
func TestLongTaxiSpline(t *testing.T) {
	const nodes = 2000
	w := objtest.Header(2).U8(uint8(BlockCreate2)).PackedGUID(0x2A).U8(TypePlayer)
	objtest.Living(w, 0x01|0x20|0x10, game.MoveInfo{Flags: game.MoveForward | game.MoveSplineEnabled}, func(w *packet.Writer) {
		w.U32(splineFlying).U32(0).U32(600000).U32(12).U32(nodes)
		for i := 0; i < nodes; i++ {
			w.F32(float32(i)).F32(float32(-i)).F32(100)
		}
		w.F32(1999).F32(-1999).F32(100)
	})
	objtest.Values(w, 1300, map[uint16]uint32{UnitHealth: 60})
	w.U8(uint8(BlockValues)).PackedGUID(wolf)
	objtest.Values(w, 188, map[uint16]uint32{UnitHealth: 1})
	blocks, err := ParseCompressedUpdate(objtest.Compress(w.Bytes()))
	if err != nil || len(blocks) != 2 {
		t.Fatalf("%d blocks, %v", len(blocks), err)
	}
	s := blocks[0].Move.Spline
	if s == nil || len(s.Points) != nodes || s.Points[nodes-1] != [3]float32{1999, -1999, 100} || s.Duration != 600000 ||
		blocks[0].Values[UnitHealth] != 60 || blocks[1].Values[UnitHealth] != 1 {
		t.Fatalf("%+v", blocks)
	}
}

// parseUpdate reports the bytes left after the last block, which the capture test
// requires to be zero for every captured packet.
func TestParseUpdateReportsLeftover(t *testing.T) {
	w := objtest.Header(1).U8(uint8(BlockValues)).PackedGUID(wolf)
	objtest.Values(w, 188, map[uint16]uint32{UnitHealth: 3})
	if blocks, left, err := parseUpdate(w.Bytes()); err != nil || left != 0 || len(blocks) != 1 {
		t.Fatalf("%+v %d %v", blocks, left, err)
	}
	w.U8(0xEE).U8(0xFF)
	if blocks, left, err := parseUpdate(w.Bytes()); err != nil || left != 2 || len(blocks) != 1 {
		t.Fatalf("%+v %d %v", blocks, left, err)
	}
}

func TestTruncatedCompressedUpdate(t *testing.T) {
	good := objtest.Compress(objtest.Header(0).Bytes())
	if _, err := ParseCompressedUpdate(good[:len(good)-3]); err == nil {
		t.Fatal("truncated zlib accepted")
	}
	lying := packet.NewWriter().U32(500).Append(good[4:]).Bytes() // declares more than it holds
	if _, err := ParseCompressedUpdate(lying); err == nil {
		t.Fatal("short inflate accepted")
	}
	if _, err := ParseCompressedUpdate(packet.NewWriter().U32(1 << 30).Bytes()); err == nil {
		t.Fatal("huge declared size accepted")
	}
}

// Ruling P3: the stream must end exactly at the declared size with a good checksum.
func TestInflateRejectsSizeMismatchAndBadChecksum(t *testing.T) {
	plain := objtest.Header(0).Bytes()
	good := objtest.Compress(plain)
	if out, err := Inflate(good); err != nil || !bytes.Equal(out, plain) {
		t.Fatalf("%x %v", out, err)
	}
	short := packet.NewWriter().U32(uint32(len(plain) - 1)).Append(good[4:]).Bytes() // declares less than it holds
	if _, err := Inflate(short); err == nil {
		t.Fatal("trailing inflated data accepted")
	}
	bad := append([]byte(nil), good...)
	bad[len(bad)-1] ^= 0xFF // adler32 checksum
	if _, err := Inflate(bad); err == nil {
		t.Fatal("bad checksum accepted")
	}
	if _, err := Inflate([]byte{5, 0, 0}); err == nil {
		t.Fatal("short size accepted")
	}
	if _, err := Inflate(packet.NewWriter().U32(5).Append([]byte{1, 2, 3}).Bytes()); err == nil {
		t.Fatal("garbage zlib header accepted")
	}
	if _, err := ParseCompressedUpdate(objtest.Compress(nil)); err == nil {
		t.Fatal("empty update payload accepted")
	}
}

// A real compressed update holds many blocks; inflate copes with sizes past one zlib window.
func TestCompressedUpdateManyBlocks(t *testing.T) {
	const n = 300
	w := objtest.Header(n)
	for i := 0; i < n; i++ {
		w.U8(uint8(BlockValues)).PackedGUID(packet.GUID(0xF130000000000000 | uint64(i+1)))
		objtest.Values(w, 188, map[uint16]uint32{UnitHealth: uint32(i), UnitFlags: UnitFlagInCombat})
	}
	var z bytes.Buffer
	zw, _ := zlib.NewWriterLevel(&z, zlib.BestSpeed)
	zw.Write(w.Bytes())
	zw.Close()
	body := packet.NewWriter().U32(uint32(w.Len())).Append(z.Bytes()).Bytes()
	blocks, err := ParseCompressedUpdate(body)
	if err != nil || len(blocks) != n || blocks[n-1].Values[UnitHealth] != n-1 || blocks[0].Values[UnitFlags] != UnitFlagInCombat {
		t.Fatalf("%d blocks, %v", len(blocks), err)
	}
}

// Review Focus 3: mask bits past every known field are read and kept, not misparsed.
func TestValuesBeyondKnownFields(t *testing.T) {
	w := objtest.Header(1).U8(uint8(BlockValues)).PackedGUID(0x2A)
	objtest.Values(w, 1500, map[uint16]uint32{UnitHealth: 7, 1490: 0xABCD})
	blocks, err := ParseUpdate(w.Bytes())
	if err != nil || blocks[0].Values[1490] != 0xABCD || blocks[0].Values[UnitHealth] != 7 {
		t.Fatalf("%+v %v", blocks, err)
	}
}

// Partial masks: a values block carries only the bits that changed, so the map holds
// exactly those indices, and an empty mask is a valid block with no values.
func TestPartialValueMasks(t *testing.T) {
	w := objtest.Header(3)
	w.U8(uint8(BlockValues)).PackedGUID(wolf)
	objtest.Values(w, 188, map[uint16]uint32{UnitTarget: 0x2A, UnitTarget + 1: 0, UnitAura + AuraSlots - 1: 133, UnitNPCFlags: 2})
	w.U8(uint8(BlockValues)).PackedGUID(wolf)
	objtest.Values(w, 188, nil)
	w.U8(uint8(BlockValues)).PackedGUID(wolf).U8(0) // zero mask blocks
	blocks, err := ParseUpdate(w.Bytes())
	if err != nil || len(blocks) != 3 {
		t.Fatalf("%+v %v", blocks, err)
	}
	v := blocks[0].Values
	if len(v) != 4 || v[UnitTarget] != 0x2A || v[UnitTarget+1] != 0 || v[UnitAura+AuraSlots-1] != 133 || v[UnitNPCFlags] != 2 {
		t.Fatalf("%v", v)
	}
	if _, ok := v[UnitHealth]; ok {
		t.Fatal("unset field reported")
	}
	if len(blocks[1].Values) != 0 || len(blocks[2].Values) != 0 {
		t.Fatalf("%v %v", blocks[1].Values, blocks[2].Values)
	}
}

// A mask that promises more values than the packet holds is an error, and the blocks
// before it are still returned.
func TestTruncatedValuesKeepEarlierBlocks(t *testing.T) {
	w := objtest.Header(2)
	w.U8(uint8(BlockValues)).PackedGUID(wolf)
	objtest.Values(w, 188, map[uint16]uint32{UnitHealth: 1})
	w.U8(uint8(BlockValues)).PackedGUID(0x2A)
	objtest.Values(w, 188, map[uint16]uint32{UnitHealth: 2, UnitLevel: 3})
	body := w.Bytes()[:w.Len()-2]
	blocks, err := ParseUpdate(body)
	if err == nil || !errors.Is(err, packet.ErrShort) || len(blocks) != 1 || blocks[0].Values[UnitHealth] != 1 {
		t.Fatalf("%+v %v", blocks, err)
	}
}

func TestMovementBlockAndGameObjectPosition(t *testing.T) {
	w := objtest.Header(2).U8(uint8(BlockMovement)).GUID(wolf) // full guid (Object.cpp:268-269)
	objtest.Living(w, 0x20, game.MoveInfo{X: 3}, nil)
	w.U8(uint8(BlockCreate)).PackedGUID(0xF110000000000042).U8(5)
	w.U8(0x40 | 0x08 | 0x10).F32(1).F32(2).F32(3).F32(4).U32(0).U32(1) // has position, high guid, all
	objtest.Values(w, 26, map[uint16]uint32{FieldEntry: 1731})
	blocks, err := ParseUpdate(w.Bytes())
	if err != nil || blocks[0].GUID != wolf || blocks[0].Move.X != 3 || !blocks[1].Move.HasPosition || blocks[1].Move.Z != 3 {
		t.Fatalf("%+v %v", blocks, err)
	}
}

// The trailing optional parts of BuildMovementUpdate in the core's order
// (Object.cpp:474-512): high guid, all, melee victim packed guid, transport time.
func TestMovementTrailingParts(t *testing.T) {
	w := objtest.Header(2)
	w.U8(uint8(BlockCreate)).PackedGUID(wolf).U8(3)
	w.U8(0x20 | 0x10 | 0x04) // living, all, melee attacking
	game.WriteMoveInfo(w, game.MoveInfo{X: 1})
	for i := 0; i < 6; i++ {
		w.F32(1)
	}
	w.U32(1).PackedGUID(0x2A) // all, victim
	objtest.Values(w, 188, map[uint16]uint32{UnitHealth: 5})
	// A transport game object: has position, transport, melee with an empty victim.
	w.U8(uint8(BlockCreate)).PackedGUID(0xF120000000000007).U8(5)
	w.U8(0x40 | 0x02 | 0x08 | 0x04).F32(1).F32(2).F32(3).F32(4).U32(0).U8(0).U32(123456)
	objtest.Values(w, 26, map[uint16]uint32{FieldEntry: 20808})
	blocks, err := ParseUpdate(w.Bytes())
	if err != nil || len(blocks) != 2 {
		t.Fatalf("%+v %v", blocks, err)
	}
	if blocks[0].Move.Victim != 0x2A || blocks[0].Values[UnitHealth] != 5 {
		t.Fatalf("%+v %+v", blocks[0].Move, blocks[0].Values)
	}
	if !blocks[1].Move.HasPosition || blocks[1].Move.O != 4 || blocks[1].Move.Victim != 0 || blocks[1].Values[FieldEntry] != 20808 {
		t.Fatalf("%+v %+v", blocks[1].Move, blocks[1].Values)
	}
}

// Out-of-range blocks: an update packet that holds only one (UpdateData::Send with no
// other data, UpdateData.cpp:146-153), a near-objects block, and a bad guid count.
func TestOutOfRangeBlocks(t *testing.T) {
	w := objtest.Header(2).U8(uint8(BlockOutOfRange)).U32(3).PackedGUID(wolf).PackedGUID(0x2A).PackedGUID(0)
	w.U8(uint8(BlockNear)).U32(1).PackedGUID(0x31)
	blocks, err := ParseUpdate(w.Bytes())
	if err != nil || len(blocks) != 2 {
		t.Fatalf("%+v %v", blocks, err)
	}
	if g := blocks[0].GUIDs; len(g) != 3 || g[0] != wolf || g[1] != 0x2A || g[2] != 0 || blocks[0].Move != nil || blocks[0].Values != nil {
		t.Fatalf("%+v", blocks[0])
	}
	if blocks[1].Type != BlockNear || len(blocks[1].GUIDs) != 1 || blocks[1].GUIDs[0] != 0x31 {
		t.Fatalf("%+v", blocks[1])
	}
	empty := objtest.Header(1).U8(uint8(BlockOutOfRange)).U32(0).Bytes()
	if blocks, err := ParseUpdate(empty); err != nil || len(blocks) != 1 || len(blocks[0].GUIDs) != 0 {
		t.Fatalf("%+v %v", blocks, err)
	}
	short := objtest.Header(1).U8(uint8(BlockOutOfRange)).U32(3).PackedGUID(wolf).Bytes()
	if _, err := ParseUpdate(short); err == nil {
		t.Fatal("guid list shorter than its count accepted")
	}
	huge := objtest.Header(1).U8(uint8(BlockOutOfRange)).U32(1 << 30).PackedGUID(wolf).Bytes()
	if _, err := ParseUpdate(huge); err == nil {
		t.Fatal("huge guid count accepted")
	}
}

func TestUpdateHeaderErrors(t *testing.T) {
	if _, err := ParseUpdate([]byte{1, 0, 0}); err == nil {
		t.Fatal("short header accepted")
	}
	if _, err := ParseUpdate(objtest.Header(1 << 30).U8(uint8(BlockOutOfRange)).U32(0).Bytes()); err == nil {
		t.Fatal("huge block count accepted")
	}
	if _, err := ParseUpdate(objtest.Header(2).U8(uint8(BlockOutOfRange)).U32(0).Bytes()); err == nil {
		t.Fatal("missing second block accepted")
	}
	if blocks, err := ParseUpdate(packet.NewWriter().U32(0).U8(1).Bytes()); err != nil || len(blocks) != 0 {
		t.Fatalf("has-transport header: %v %v", blocks, err)
	}
}

func TestUnknownBlockTypeFails(t *testing.T) {
	if _, err := ParseUpdate(objtest.Header(1).U8(9).Bytes()); err == nil {
		t.Fatal("block type 9 accepted")
	}
}
