package session

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/pjerra/turtle-agent/internal/opcode"
	"github.com/pjerra/turtle-agent/internal/packet"
)

// CreateSpec says which character login creates when the account's list lacks
// the name: Turtle's CMSG_CHAR_CREATE (race, class, gender) with a default
// look (skin, face, hair, outfit 0) and no challenge mode (challengeMask 0).
type CreateSpec struct {
	Race   uint8
	Class  uint8
	Gender uint8
}

// ParseCreateSpec reads a --create "race,class[,gender]" spec: plain numbers
// (the repo has no race/class name tables). Gender defaults to 0 (male).
func ParseCreateSpec(spec string) (CreateSpec, error) {
	var zero CreateSpec
	parts := strings.Split(spec, ",")
	if len(parts) < 2 || len(parts) > 3 {
		return zero, fmt.Errorf("--create %q: want race,class[,gender]", spec)
	}
	vals := make([]uint8, 3)
	for i, part := range parts {
		v, err := strconv.ParseUint(strings.TrimSpace(part), 10, 8)
		if err != nil {
			return zero, fmt.Errorf("--create %q: %q is not a number: %w", spec, strings.TrimSpace(part), err)
		}
		vals[i] = uint8(v)
	}
	return CreateSpec{Race: vals[0], Class: vals[1], Gender: vals[2]}, nil
}

// createBody builds CMSG_CHAR_CREATE (CharacterHandler.cpp:179-185): name,
// race, class, gender, six look bytes, then Turtle's extra u32 challengeMask.
func (c CreateSpec) createBody(name string) []byte {
	w := packet.NewWriter().CString(name).U8(c.Race).U8(c.Class).U8(c.Gender)
	for range 6 { // skin, face, hair style, hair colour, facial hair, outfit
		w.U8(0)
	}
	w.U32(0) // Turtle's challengeMask (CharacterHandler.cpp:184): no challenge mode
	return w.Bytes()
}

// charCreateResult names an SMSG_CHAR_CREATE result code (SharedDefines.h
// ResponseCodes; the enum runs 0-based from RESPONSE_SUCCESS). Codes outside
// the create/name ranges read as their number.
func charCreateResult(code uint8) string {
	switch code {
	case 46:
		return "success"
	case 47:
		return "error"
	case 48:
		return "failed"
	case 49:
		return "name already in use"
	case 50:
		return "character creation is disabled"
	case 51:
		return "PvP team violation (one faction per realm)"
	case 52:
		return "too many characters on this realm"
	case 53:
		return "too many characters on this account"
	case 54:
		return "server queue"
	case 55:
		return "only existing characters can be created"
	case 69:
		return "name required"
	case 70:
		return "name too short"
	case 71:
		return "name too long"
	case 72:
		return "name has an invalid character"
	case 73:
		return "name mixes languages"
	case 74:
		return "name is profane"
	case 75:
		return "name is reserved"
	case 76:
		return "name has a bad apostrophe"
	case 77:
		return "name has multiple apostrophes"
	case 78:
		return "name has three consecutive letters"
	case 79:
		return "name has a bad space"
	case 80:
		return "name has consecutive spaces"
	case 81:
		return "name failure"
	}
	return fmt.Sprintf("code %d", code)
}

// onCharCreate asks for the character list again after a create, so onCharEnum
// logs the new character in. A failed create is fatal like an unknown
// character: retrying would just send the same name again.
func (s *Session) onCharCreate(body []byte) error {
	if len(body) == 0 {
		return &fatalError{fmt.Errorf("SMSG_CHAR_CREATE: create failed: empty reply")}
	}
	if body[0] != 46 { // CHAR_CREATE_SUCCESS
		return &fatalError{fmt.Errorf("SMSG_CHAR_CREATE: create failed: %s", charCreateResult(body[0]))}
	}
	if err := s.conn.WritePacket(opcode.CMSGCharEnum, nil); err != nil {
		return &fatalError{err}
	}
	return nil
}
