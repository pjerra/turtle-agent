// Package objects parses update-object packets and keeps the units around the player.
package objects

import (
	"bytes"
	"compress/zlib"
	"fmt"
	"io"

	"github.com/TortoiseBots/turtle-agent/internal/game"
	"github.com/TortoiseBots/turtle-agent/internal/packet"
)

// BlockType is ObjectUpdateType (UpdateData.h:32-40).
type BlockType uint8

const (
	BlockValues     BlockType = 0
	BlockMovement   BlockType = 1
	BlockCreate     BlockType = 2
	BlockCreate2    BlockType = 3
	BlockOutOfRange BlockType = 4
	BlockNear       BlockType = 5
)

// Update flags (UpdateData.h:43-53).
const (
	updateFlagSelf           = 0x01
	updateFlagTransport      = 0x02
	updateFlagMeleeAttacking = 0x04
	updateFlagHighGUID       = 0x08
	updateFlagAll            = 0x10
	updateFlagLiving         = 0x20
	updateFlagHasPosition    = 0x40
)

// Spline flags (Movement/spline/MoveSplineFlag.h:36-78).
const (
	splineFlying      = 0x00000200 // Catmull-Rom path (Mask_CatmullRom = Flying, :77)
	splineFinalPoint  = 0x00010000
	splineFinalTarget = 0x00020000
	splineFinalAngle  = 0x00040000
)

// maxInflated bounds the declared size; the core splits update packets near 32 KB
// (MAX_UNCOMPRESSED_PACKET_SIZE, UpdateData.cpp:33 and :54-68).
const maxInflated = 8 << 20

// Spline is PacketBuilder::WriteCreate's spline (packet_builder.cpp:154-180).
type Spline struct {
	Flags                    uint32
	FinalPoint               [3]float32
	FinalTarget              packet.GUID
	FinalAngle               float32
	TimePassed, Duration, ID uint32
	Points                   [][3]float32
	Dest                     [3]float32
}

// Movement is Object::BuildMovementUpdate read back (Object.cpp:415-513).
type Movement struct {
	UpdateFlags uint8
	Living      bool
	Info        game.MoveInfo // living units only
	Speeds      [6]float32    // walk, run, run back, swim, swim back, turn rate
	Spline      *Spline
	HasPosition bool
	X, Y, Z, O  float32
	Victim      packet.GUID
}

// Block is one update block. Values holds every set mask bit, known field or not.
type Block struct {
	Type    BlockType
	GUID    packet.GUID
	ObjType uint8 // TypeID (ObjectGuid.h:35-42), create blocks only
	Move    *Movement
	Values  map[uint16]uint32
	GUIDs   []packet.GUID // out-of-range and near-objects blocks
}

// Inflate reads a u32 uncompressed size and a zlib stream (UpdateData.cpp:121-135, the
// core compresses with libdeflate_zlib_compress at :76-80). The stream must inflate to
// exactly the declared size and end with a good checksum. Memory follows the data
// actually inflated, never the declared size.
func Inflate(body []byte) ([]byte, error) {
	r := packet.NewReader(body)
	size := r.U32()
	if err := r.Err(); err != nil {
		return nil, fmt.Errorf("inflate: %w", err)
	}
	if size > maxInflated {
		return nil, fmt.Errorf("inflate: declared size %d is too large", size)
	}
	zr, err := zlib.NewReader(bytes.NewReader(body[4:]))
	if err != nil {
		return nil, fmt.Errorf("inflate: %w", err)
	}
	defer zr.Close()
	// Ruling P3: reading one byte past the declared size must hit a clean end of
	// stream, which is also where the zlib reader checks the adler32 checksum.
	var out bytes.Buffer
	n, err := io.Copy(&out, io.LimitReader(zr, int64(size)+1))
	if err != nil {
		return nil, fmt.Errorf("inflate: %w", err)
	}
	if n != int64(size) {
		return nil, fmt.Errorf("inflate: stream holds %d bytes, declared %d", n, size)
	}
	return out.Bytes(), nil
}

// ParseCompressedUpdate decodes SMSG_COMPRESSED_UPDATE_OBJECT (UpdateData.cpp:121-136).
func ParseCompressedUpdate(body []byte) ([]Block, error) {
	plain, err := Inflate(body)
	if err != nil {
		return nil, fmt.Errorf("SMSG_COMPRESSED_UPDATE_OBJECT: %w", err)
	}
	return ParseUpdate(plain)
}

// ParseUpdate decodes SMSG_UPDATE_OBJECT: u32 block count, u8 has transport, blocks
// (UpdateData.cpp:97-117). On error it returns the blocks parsed so far.
func ParseUpdate(body []byte) ([]Block, error) {
	blocks, _, err := parseUpdate(body)
	return blocks, err
}

// parseUpdate is ParseUpdate that also reports how many bytes follow the last block.
func parseUpdate(body []byte) ([]Block, int, error) {
	r := packet.NewReader(body)
	count := r.U32()
	r.U8() // has transport
	if err := r.Err(); err != nil {
		return nil, r.Remaining(), fmt.Errorf("update header: %w", err)
	}
	if count > uint32(r.Remaining()) { // every block is at least one byte
		return nil, r.Remaining(), fmt.Errorf("update header: %d blocks in %d bytes", count, r.Remaining())
	}
	blocks := make([]Block, 0, count)
	for i := uint32(0); i < count; i++ {
		b, err := readBlock(r)
		if err != nil {
			return blocks, r.Remaining(), fmt.Errorf("update block %d of %d (type %d): %w", i+1, count, b.Type, err)
		}
		blocks = append(blocks, b)
	}
	return blocks, r.Remaining(), nil
}

func readBlock(r *packet.Reader) (Block, error) {
	b := Block{Type: BlockType(r.U8())}
	var err error
	switch b.Type {
	case BlockValues:
		b.GUID = r.PackedGUID() // Object.cpp:382-383
		b.Values, err = readValues(r)
	case BlockMovement:
		b.GUID = r.GUID() // Object.cpp:268-269
		b.Move, err = readMovement(r)
	case BlockCreate, BlockCreate2:
		b.GUID, b.ObjType = r.PackedGUID(), r.U8() // Object.cpp:298-300
		if b.Move, err = readMovement(r); err == nil {
			b.Values, err = readValues(r)
		}
	case BlockOutOfRange, BlockNear:
		n := r.U32()                   // UpdateData.cpp:109-114
		if n > uint32(r.Remaining()) { // a packed guid is at least one byte
			return b, fmt.Errorf("%d guids in %d bytes", n, r.Remaining())
		}
		for j := uint32(0); j < n && r.Err() == nil; j++ {
			b.GUIDs = append(b.GUIDs, r.PackedGUID())
		}
	default:
		return b, fmt.Errorf("unknown block type %d", b.Type)
	}
	if err != nil {
		return b, err
	}
	return b, r.Err()
}

// readMovement is Object::BuildMovementUpdate read back (Object.cpp:415-513).
func readMovement(r *packet.Reader) (*Movement, error) {
	m := &Movement{UpdateFlags: r.U8()}
	if m.UpdateFlags&updateFlagLiving != 0 {
		m.Living, m.HasPosition = true, true
		m.Info = game.ReadMoveInfo(r)
		m.X, m.Y, m.Z, m.O = m.Info.X, m.Info.Y, m.Info.Z, m.Info.O
		for i := range m.Speeds {
			m.Speeds[i] = r.F32()
		}
		// Object.cpp:443-444. WriteCreate writes nothing for an uninitialized spline
		// (packet_builder.cpp:158); the source never sets the flag without one for a
		// creature (MoveSplineInit.cpp:95-130), so the spline is read whenever it is set.
		if m.Info.Flags&game.MoveSplineEnabled != 0 {
			s, err := readSpline(r)
			if err != nil {
				return m, err
			}
			m.Spline = s
		}
	} else if m.UpdateFlags&updateFlagHasPosition != 0 {
		// A transport sends its stationary position here, also 4 floats (Object.cpp:452-471).
		m.HasPosition = true
		m.X, m.Y, m.Z, m.O = r.F32(), r.F32(), r.F32(), r.F32()
	}
	if m.UpdateFlags&updateFlagHighGUID != 0 {
		r.U32()
	}
	if m.UpdateFlags&updateFlagAll != 0 {
		r.U32()
	}
	if m.UpdateFlags&updateFlagMeleeAttacking != 0 {
		m.Victim = r.PackedGUID()
	}
	if m.UpdateFlags&updateFlagTransport != 0 {
		r.U32() // path progress or ms time
	}
	return m, r.Err()
}

// readSpline is PacketBuilder::WriteCreate read back (packet_builder.cpp:154-180).
func readSpline(r *packet.Reader) (*Spline, error) {
	s := &Spline{Flags: r.U32()}
	switch {
	case s.Flags&splineFinalAngle != 0:
		s.FinalAngle = r.F32()
	case s.Flags&splineFinalTarget != 0:
		s.FinalTarget = r.GUID()
	case s.Flags&splineFinalPoint != 0:
		s.FinalPoint = [3]float32{r.F32(), r.F32(), r.F32()}
	}
	s.TimePassed, s.Duration, s.ID = r.U32(), r.U32(), r.U32()
	// Taxi flights send long Catmull-Rom paths (WaypointMovementGenerator.cpp:364-372), so
	// the count is bounded by the bytes left, not by a constant.
	n := r.U32()
	if uint64(n)*12 > uint64(r.Remaining()) {
		return s, fmt.Errorf("spline with %d nodes in %d bytes", n, r.Remaining())
	}
	for i := uint32(0); i < n && r.Err() == nil; i++ {
		s.Points = append(s.Points, [3]float32{r.F32(), r.F32(), r.F32()})
	}
	s.Dest = [3]float32{r.F32(), r.F32(), r.F32()}
	return s, r.Err()
}

// readValues reads the mask and the set values in index order (Object.cpp:553-556).
// Indices past the fields this client knows are kept; the store ignores them.
func readValues(r *packet.Reader) (map[uint16]uint32, error) {
	blocks := int(r.U8())
	masks := make([]uint32, blocks)
	for i := range masks {
		masks[i] = r.U32()
	}
	vals := make(map[uint16]uint32)
	for i, m := range masks {
		for bit := 0; bit < 32; bit++ {
			if m&(1<<bit) != 0 {
				vals[uint16(i*32+bit)] = r.U32()
			}
		}
	}
	return vals, r.Err()
}
