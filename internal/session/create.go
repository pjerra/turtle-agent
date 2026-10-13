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
// (the repo has no race/class name tables). Gender defaults to 0 (male). The
// numbers are checked here, before realmd is contacted, against the sets the
// core accepts: races 1-10 (RACEMASK_ALL_PLAYABLE, Turtle's Goblin 9 and High
// Elf 10 included), classes 1-5, 7-9 and 11 (CLASSMASK_ALL_PLAYABLE: no Death
// Knight 6, no unused 10), gender 0 or 1 (Player::Create). Race and class pairs
// are not checked: which pairs exist comes from the world database
// (playercreateinfo), so the server still refuses an unknown pair.
func ParseCreateSpec(spec string) (CreateSpec, error) {
	var zero CreateSpec
	parts := strings.Split(spec, ",")
	if len(parts) < 2 || len(parts) > 3 {
		return zero, fmt.Errorf("--create %q: want race,class[,gender]", spec)
	}
	vals := []uint64{0, 0, 0} // gender stays 0 (male) when omitted
	for i, part := range parts {
		v, err := strconv.ParseUint(strings.TrimSpace(part), 10, 32)
		if err != nil {
			return zero, fmt.Errorf("--create %q: %q is not a number: %w", spec, strings.TrimSpace(part), err)
		}
		vals[i] = v
	}
	race, class, gender := vals[0], vals[1], vals[2]
	switch {
	case race < 1 || race > 10:
		return zero, fmt.Errorf("--create %q: race %d is not a playable race (valid: 1-10)", spec, race)
	case !playableClass(class):
		return zero, fmt.Errorf("--create %q: class %d is not a playable class (valid: 1-5, 7-9, 11)", spec, class)
	case gender > 1:
		return zero, fmt.Errorf("--create %q: gender %d is not 0 (male) or 1 (female)", spec, gender)
	}
	return CreateSpec{Race: uint8(race), Class: uint8(class), Gender: uint8(gender)}, nil
}

// playableClass reports whether class is one of the classes the core accepts
// (CLASSMASK_ALL_PLAYABLE).
func playableClass(class uint64) bool {
	switch class {
	case 1, 2, 3, 4, 5, 7, 8, 9, 11:
		return true
	}
	return false
}

// createBody builds the CMSG_CHAR_CREATE body that WorldSession::HandleCharCreateOpcode
// (src/game/Handlers/CharacterHandler.cpp) reads: name, race, class, gender, six
// look bytes, then Turtle's extra u32 challengeMask.
func (c CreateSpec) createBody(name string) []byte {
	w := packet.NewWriter().CString(name).U8(c.Race).U8(c.Class).U8(c.Gender)
	for range 6 { // skin, face, hair style, hair colour, facial hair, outfit
		w.U8(0)
	}
	w.U32(0) // Turtle's challengeMask, read last by HandleCharCreateOpcode: no challenge mode
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
