package session

import (
	"fmt"

	"github.com/pjerra/turtle-agent/internal/packet"
)

// Character is one SMSG_CHAR_ENUM entry (Player::BuildEnumData, Player.cpp:2176-2288).
type Character struct {
	GUID       packet.GUID `json:"guid"`
	Name       string      `json:"name"`
	Race       uint8       `json:"race"`
	Class      uint8       `json:"class"`
	Gender     uint8       `json:"gender"`
	Level      uint8       `json:"level"`
	Zone       uint32      `json:"zone"`
	Map        uint32      `json:"map"`
	X          float32     `json:"x"`
	Y          float32     `json:"y"`
	Z          float32     `json:"z"`
	Guild      uint32      `json:"guild"`
	Flags      uint32      `json:"flags"`
	FirstLogin bool        `json:"first_login"`
}

// CharEnumTailLen is the size of the per-character block after the name: race, class,
// gender, five appearance bytes, level, zone, map, x, y, z, guild, flags, first login,
// pet (12) and 20 equipment slots of 5 bytes. Test rigs outside this package build
// SMSG_CHAR_ENUM with it, so a layout change reaches them too.
const CharEnumTailLen = 150

// ParseCharEnum reads a u8 count (WorldSession::HandleCharEnum writes it) and per character the
// guid, the name and a 150-byte tail (Player::BuildEnumData). Bytes left over mean the
// layout drifted, which is an error rather than a silent misparse.
func ParseCharEnum(body []byte) ([]Character, error) {
	r := packet.NewReader(body)
	n := int(r.U8())
	chars := make([]Character, 0, n)
	for i := 0; i < n; i++ {
		var c Character
		c.GUID, c.Name = r.GUID(), r.CString()
		c.Race, c.Class, c.Gender = r.U8(), r.U8(), r.U8()
		r.Skip(5) // skin, face, hair style, hair colour, facial hair
		c.Level = r.U8()
		c.Zone, c.Map = r.U32(), r.U32()
		c.X, c.Y, c.Z = r.F32(), r.F32(), r.F32()
		c.Guild, c.Flags = r.U32(), r.U32()
		c.FirstLogin = r.U8() != 0
		r.Skip(12)     // pet display id, level, family
		r.Skip(20 * 5) // 20 equipment slots: u32 display id + u8 inventory type
		chars = append(chars, c)
	}
	if err := r.Err(); err != nil {
		return nil, fmt.Errorf("SMSG_CHAR_ENUM: %w", err)
	}
	if r.Remaining() != 0 {
		return nil, fmt.Errorf("SMSG_CHAR_ENUM: %d unread bytes after %d characters (layout drift)", r.Remaining(), n)
	}
	return chars, nil
}
