// Package objtest writes update-object payloads the way the core does, for tests only.
package objtest

import (
	"bytes"
	"compress/zlib"
	"sort"

	"github.com/pjerra/turtle-agent/internal/game"
	"github.com/pjerra/turtle-agent/internal/packet"
)

// Header starts an SMSG_UPDATE_OBJECT body: u32 block count, u8 has transport 0
// (Objects/UpdateData.cpp:104-105).
func Header(count uint32) *packet.Writer { return packet.NewWriter().U32(count).U8(0) }

// Living writes Object::BuildMovementUpdate for a living unit (Objects/Object.cpp:415-449,
// :474-484): update flags, the movement info, speeds 1..6, the spline written by spline
// (nil for none), then the high-guid and all words when flags carry 0x08 and 0x10.
func Living(w *packet.Writer, flags uint8, mi game.MoveInfo, spline func(*packet.Writer)) {
	w.U8(flags)
	game.WriteMoveInfo(w, mi)
	for i := 0; i < 6; i++ {
		w.F32(float32(i + 1))
	}
	if spline != nil {
		spline(w)
	}
	if flags&0x08 != 0 {
		w.U32(0)
	}
	if flags&0x10 != 0 {
		w.U32(1)
	}
}

// Static writes Object::BuildMovementUpdate for a game object or other stationary object
// (Objects/Object.cpp:452-471, :474-484): update flags HAS_POSITION and ALL (0x50), the
// position and facing, and the all word.
func Static(w *packet.Writer, x, y, z, o float32) {
	w.U8(0x50).F32(x).F32(y).F32(z).F32(o).U32(1)
}

// Values writes Object::BuildValuesUpdate's mask and values (Objects/Object.cpp:553-556)
// for an object of count fields: u8 mask block count, the u32 mask words, then one u32
// per set bit in index order.
func Values(w *packet.Writer, count int, vals map[uint16]uint32) {
	blocks := (count + 31) / 32
	masks := make([]uint32, blocks)
	var idx []int
	for i := range vals {
		masks[i/32] |= 1 << (i % 32)
		idx = append(idx, int(i))
	}
	sort.Ints(idx)
	w.U8(uint8(blocks))
	for _, m := range masks {
		w.U32(m)
	}
	for _, i := range idx {
		w.U32(vals[uint16(i)])
	}
}

// Compress frames plain the way SMSG_COMPRESSED_UPDATE_OBJECT and SMSG_COMPRESSED_MOVES
// carry it: u32 uncompressed size, then a zlib stream (Objects/UpdateData.cpp:121-135, :200-217).
func Compress(plain []byte) []byte {
	var z bytes.Buffer
	zw := zlib.NewWriter(&z)
	zw.Write(plain)
	zw.Close()
	return packet.NewWriter().U32(uint32(len(plain))).Append(z.Bytes()).Bytes()
}
