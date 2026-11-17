package game

import (
	"bytes"
	"fmt"

	"github.com/TortoiseBots/turtle-agent/internal/packet"
)

// Core cites in this file are tortoise-wow upstream/1181dev 5784fb0.

// BuildTrainerList is CMSG_TRAINER_LIST: the trainer's GUID (NPCHandler.cpp:108-115).
func BuildTrainerList(trainer packet.GUID) []byte { return packet.NewWriter().GUID(trainer).Bytes() }

// BuildTrainerBuySpell is CMSG_TRAINER_BUY_SPELL: trainer GUID and the spell id as the list
// shows it (NPCHandler.cpp:282-290).
func BuildTrainerBuySpell(trainer packet.GUID, spell uint32) []byte {
	return packet.NewWriter().GUID(trainer).U32(spell).Bytes()
}

// Trainer spell states (Player.h:194-197); the server never sends the custom "disabled" one.
const (
	TrainerGreen uint8 = 0 // can be learned
	TrainerRed   uint8 = 1 // not yet: level, skill, money or a missing earlier rank
	TrainerGray  uint8 = 2 // already known
)

// TrainerStateName is learnable, unavailable or known.
func TrainerStateName(state uint8) string {
	switch state {
	case TrainerGreen:
		return "learnable"
	case TrainerRed:
		return "unavailable"
	case TrainerGray:
		return "known"
	}
	return fmt.Sprintf("unknown(%d)", state)
}

// TrainerSpell is one line of SMSG_TRAINER_LIST, 38 bytes (SendTrainerSpellHelper,
// NPCHandler.cpp:117-159).
type TrainerSpell struct {
	Spell         uint32 `json:"spell_id"`
	State         string `json:"state"` // learnable, unavailable or known
	Cost          uint32 `json:"cost"`  // copper, after the reputation discount
	Level         uint8  `json:"level"` // level required
	ReqSkill      uint32 `json:"req_skill,omitempty"`
	ReqSkillValue uint32 `json:"req_skill_value,omitempty"`
	ReqSpell1     uint32 `json:"req_spell_1,omitempty"` // earlier rank or other spell needed first
	ReqSpell2     uint32 `json:"req_spell_2,omitempty"`
	// PrimaryProfession is set for the first rank of a primary profession, which takes one
	// of the character's profession points.
	PrimaryProfession bool `json:"primary_profession,omitempty"`
}

// TrainerList is SMSG_TRAINER_LIST (NPCHandler.cpp:161-263). Type is TRAINER_TYPE_* (0 class,
// 1 mounts, 2 tradeskills, 3 pets, SharedDefines.h:954-957). Declared is the spell count the
// header claims; it exceeds len(Spells) when the core counted a line it did not write.
type TrainerList struct {
	Trainer  packet.GUID
	Type     uint32
	Declared uint32
	Spells   []TrainerSpell
	Greeting string
}

// trainerSpellSize is the length of one line (NPCHandler.cpp:208 reserves 38 bytes each).
const trainerSpellSize = 38

// ParseTrainerList decodes the list from its tail. SendTrainerSpellHelper returns without
// writing a line when it knows neither the spell's required level nor its trigger spell
// (NPCHandler.cpp:121-126), yet SendTrainerList counts that line (:229-236), so the header can
// claim more spells than the body holds. The greeting is the last string of the body and
// has no NUL inside, so the lines are what lies between the header and it.
func ParseTrainerList(body []byte) (TrainerList, error) {
	r := packet.NewReader(body)
	l := TrainerList{Trainer: r.GUID(), Type: r.U32(), Declared: r.U32()}
	rest := r.Bytes(r.Remaining())
	if err := r.Err(); err != nil {
		return TrainerList{}, fmt.Errorf("SMSG_TRAINER_LIST: %w", err)
	}
	if len(rest) == 0 || rest[len(rest)-1] != 0 {
		return TrainerList{}, fmt.Errorf("SMSG_TRAINER_LIST: greeting is not terminated")
	}
	cut := bytes.LastIndexByte(rest[:len(rest)-1], 0) + 1 // 0 when there is no line at all
	l.Greeting = string(rest[cut : len(rest)-1])
	lines := rest[:cut]
	if len(lines)%trainerSpellSize != 0 || uint32(len(lines)/trainerSpellSize) > l.Declared {
		return TrainerList{}, fmt.Errorf("SMSG_TRAINER_LIST: %d bytes of spells for %d declared (%d bytes each)", len(lines), l.Declared, trainerSpellSize)
	}
	lr := packet.NewReader(lines)
	for lr.Remaining() > 0 {
		s := TrainerSpell{Spell: lr.U32(), State: TrainerStateName(lr.U8()), Cost: lr.U32()}
		lr.U32() // can learn a primary profession (this player has a free point)
		s.PrimaryProfession = lr.U32() != 0
		s.Level = lr.U8()
		s.ReqSkill, s.ReqSkillValue = lr.U32(), lr.U32()
		s.ReqSpell1, s.ReqSpell2 = lr.U32(), lr.U32()
		lr.U32() // always 0
		l.Spells = append(l.Spells, s)
	}
	return l, lr.Err()
}

// TrainerSucceeded is SMSG_TRAINER_BUY_SUCCEEDED (NPCHandler.cpp:264-270).
type TrainerSucceeded struct {
	Trainer packet.GUID
	Spell   uint32
}

func ParseTrainerSucceeded(body []byte) (TrainerSucceeded, error) {
	r := packet.NewReader(body)
	s := TrainerSucceeded{Trainer: r.GUID(), Spell: r.U32()}
	return s, done(r, "SMSG_TRAINER_BUY_SUCCEEDED")
}

// TrainerFailed is SMSG_TRAINER_BUY_FAILED: the spell id of the request and a TRAIN_FAIL_*
// code (NPCHandler.cpp:272-280, SharedDefines.h:1294-1296).
type TrainerFailed struct {
	Trainer packet.GUID
	Spell   uint32
	Code    uint32
}

func ParseTrainerFailed(body []byte) (TrainerFailed, error) {
	r := packet.NewReader(body)
	f := TrainerFailed{Trainer: r.GUID(), Spell: r.U32(), Code: r.U32()}
	return f, done(r, "SMSG_TRAINER_BUY_FAILED")
}

// TrainerFailName is unavailable, not_enough_money or not_enough_skill.
func TrainerFailName(code uint32) string {
	switch code {
	case 0:
		return "unavailable"
	case 1:
		return "not_enough_money"
	case 2:
		return "not_enough_skill"
	}
	return fmt.Sprintf("unknown(%d)", code)
}
