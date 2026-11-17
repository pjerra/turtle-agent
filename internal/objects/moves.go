package objects

import (
	"fmt"
	"math"

	"github.com/TortoiseBots/turtle-agent/internal/packet"
)

// SubPacket is one packet bundled in SMSG_COMPRESSED_MOVES.
type SubPacket struct {
	Op   uint16
	Body []byte
}

// SplitCompressedMoves decodes SMSG_COMPRESSED_MOVES: inflate, then entries of u8 size
// (opcode + body), u16 opcode, body (UpdateData.cpp:187-217).
func SplitCompressedMoves(body []byte) ([]SubPacket, error) {
	plain, err := Inflate(body)
	if err != nil {
		return nil, fmt.Errorf("SMSG_COMPRESSED_MOVES: %w", err)
	}
	r := packet.NewReader(plain)
	var out []SubPacket
	for r.Remaining() > 0 {
		size := int(r.U8())
		if size < 2 {
			return out, fmt.Errorf("SMSG_COMPRESSED_MOVES: entry size %d", size)
		}
		op := r.U16()
		b := r.Bytes(size - 2)
		if err := r.Err(); err != nil {
			return out, fmt.Errorf("SMSG_COMPRESSED_MOVES: %w", err)
		}
		out = append(out, SubPacket{Op: op, Body: b})
	}
	return out, nil
}

// MonsterMove is the part of SMSG_MONSTER_MOVE the agent uses.
type MonsterMove struct {
	GUID     packet.GUID
	Start    [3]float32
	ID       uint32
	Stop     bool
	Flags    uint32 // MoveSplineFlag, 0 for a stop
	Dest     [3]float32
	Duration uint32
	// Path is the whole route from Start to Dest, both included: a smooth path's points,
	// or a linear path's points rebuilt from their packed offsets. A stop has none.
	Path [][3]float32
	// FinalAngle is the facing at the end when HasFinalAngle (MonsterMoveFacingAngle).
	FinalAngle    float32
	HasFinalAngle bool
}

func vec(r *packet.Reader) [3]float32 { return [3]float32{r.F32(), r.F32(), r.F32()} }

// maxWorld bounds a coordinate the agent will accept from the network: map coordinates stay
// within about 17066 yards, so anything beyond this (or NaN, or an infinity) is not a position.
const maxWorld = 100000

func checkInWorld(v [3]float32) error {
	for _, c := range v {
		if !(c >= -maxWorld && c <= maxWorld) { // false for NaN too
			return fmt.Errorf("coordinate %v outside the world", c)
		}
	}
	return nil
}

// ParseMonsterMove decodes SMSG_MONSTER_MOVE (MoveSplineInit.cpp:134-156, packet_builder.cpp:45-152).
func ParseMonsterMove(body []byte) (MonsterMove, error) {
	r := packet.NewReader(body)
	var m MonsterMove
	m.GUID = r.PackedGUID()
	m.Start = vec(r)
	if err := checkInWorld(m.Start); err != nil && r.Err() == nil {
		return m, fmt.Errorf("SMSG_MONSTER_MOVE: start: %w", err)
	}
	m.ID = r.U32()
	switch t := r.U8(); t { // MonsterMoveType (packet_builder.cpp:36-43)
	case 0: // normal
	case 1: // stop
		m.Stop, m.Dest = true, m.Start
		if err := r.Err(); err != nil {
			return m, fmt.Errorf("SMSG_MONSTER_MOVE: %w", err)
		}
		return m, nil
	case 2: // facing spot
		r.Skip(12)
	case 3: // facing target
		r.Skip(8)
	case 4: // facing angle
		m.FinalAngle, m.HasFinalAngle = r.F32(), true
	default:
		if r.Err() == nil {
			return m, fmt.Errorf("SMSG_MONSTER_MOVE: facing type %d", t)
		}
	}
	m.Flags = r.U32()
	m.Duration = r.U32()
	count := r.U32()
	if m.Flags&splineFlying != 0 && r.Err() == nil { // smooth path: count points, the last is the destination (:115-127)
		if count == 0 || uint64(count)*12 > uint64(r.Remaining()) {
			return m, fmt.Errorf("SMSG_MONSTER_MOVE: smooth path of %d points in %d bytes", count, r.Remaining())
		}
		m.Path = append(make([][3]float32, 0, count+1), m.Start)
		for i := uint32(0); i < count; i++ {
			m.Path = append(m.Path, vec(r))
		}
		for _, p := range m.Path {
			if err := checkInWorld(p); err != nil && r.Err() == nil {
				return m, fmt.Errorf("SMSG_MONSTER_MOVE: path: %w", err)
			}
		}
		m.Dest = m.Path[len(m.Path)-1]
		if err := r.Err(); err != nil {
			return m, fmt.Errorf("SMSG_MONSTER_MOVE: %w", err)
		}
		return m, nil
	}
	m.Dest = vec(r) // linear path: destination first, packed offsets follow (:78-112)
	if err := r.Err(); err != nil {
		return m, fmt.Errorf("SMSG_MONSTER_MOVE: %w", err)
	}
	if err := checkInWorld(m.Dest); err != nil {
		return m, fmt.Errorf("SMSG_MONSTER_MOVE: destination: %w", err)
	}
	path, err := linearPath(r, m.Start, m.Dest, count)
	if err != nil {
		return m, fmt.Errorf("SMSG_MONSTER_MOVE: %w", err)
	}
	m.Path = path
	return m, nil
}

// linearPath rebuilds a linear path from the packed offsets after the destination:
// WriteLinearPath writes destination - point for the points from the first one on, each
// as 11, 11 and 10 signed bits of quarter yards (packet_builder.cpp:78-112,
// ByteBuffer::appendPackXYZ). A linear spline is laid out as a Catmull-Rom one (the
// initializers table, spline.cpp:49-55: a virtual point before the first, a copy of the
// last after it), so last_idx is the path's point count - 1 and the core writes no offsets
// for a two-point path (last_idx 1, packet_builder.cpp:94) and two for a three-point one.
// The count is therefore not trusted: at most count-1 offsets are read, and only the
// bytes that are there.
// An offset only spans 512 yards across (256 up and down) and wraps beyond that, so each
// axis takes the wrap that puts the point nearest the one before it.
func linearPath(r *packet.Reader, start, dest [3]float32, count uint32) ([][3]float32, error) {
	path := [][3]float32{start}
	for i := uint32(1); i < count && r.Remaining() >= 4; i++ {
		v := r.U32()
		off := [3]int32{int32(v<<21) >> 21, int32(v<<10) >> 21, int32(v) >> 22}
		prev := path[len(path)-1]
		var p [3]float32
		for k, span := range [3]float32{512, 512, 256} {
			p[k] = dest[k] - float32(off[k])*0.25
			// One step, not a loop: dest and prev are in range, so this is a few wraps at most.
			p[k] -= span * float32(math.Round(float64((p[k]-prev[k])/span)))
		}
		if err := checkInWorld(p); err != nil { // each point may sit 256 yd from the one before
			return nil, fmt.Errorf("path point %d: %w", len(path), err)
		}
		path = append(path, p)
	}
	return append(path, dest), nil
}

func dist3(a, b [3]float32) float64 {
	dx, dy, dz := float64(a[0]-b[0]), float64(a[1]-b[1]), float64(a[2]-b[2])
	return math.Sqrt(dx*dx + dy*dy + dz*dz)
}
