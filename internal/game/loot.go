package game

import (
	"fmt"
	"strings"

	"github.com/TortoiseBots/turtle-agent/internal/packet"
)

// Core cites in this file are tortoise-wow upstream/1181dev 5784fb0.

// Roll choices are RollVote (Group.h:72-79), the values CMSG_LOOT_ROLL carries.
const (
	RollPass  uint8 = 0
	RollNeed  uint8 = 1
	RollGreed uint8 = 2
)

var rollChoices = [...]string{RollPass: "pass", RollNeed: "need", RollGreed: "greed"}

// RollChoiceByName maps pass, need or greed (case-insensitive) to its RollVote.
func RollChoiceByName(name string) (uint8, bool) {
	for i, n := range rollChoices {
		if strings.EqualFold(n, name) {
			return uint8(i), true
		}
	}
	return 0, false
}

// RollChoiceName is pass, need or greed.
func RollChoiceName(v uint8) string {
	if int(v) < len(rollChoices) {
		return rollChoices[v]
	}
	return fmt.Sprintf("unknown(%d)", v)
}

// BuildLootRoll is CMSG_LOOT_ROLL: u64 looted object, u32 loot slot, u8 RollVote
// (GroupHandler.cpp:405-425).
func BuildLootRoll(looted packet.GUID, slot uint32, choice uint8) []byte {
	return packet.NewWriter().GUID(looted).U32(slot).U8(choice).Bytes()
}

// LootStartRoll is SMSG_LOOT_START_ROLL (Group.cpp:751-772); Countdown is in ms.
type LootStartRoll struct {
	Looted                   packet.GUID
	Slot, Item               uint32
	RandomSuffix, RandomProp uint32
	Countdown                uint32
}

func ParseLootStartRoll(body []byte) (LootStartRoll, error) {
	r := packet.NewReader(body)
	s := LootStartRoll{Looted: r.GUID(), Slot: r.U32(), Item: r.U32(), RandomSuffix: r.U32(), RandomProp: r.U32(), Countdown: r.U32()}
	return s, done(r, "SMSG_LOOT_START_ROLL")
}

// LootRoll is SMSG_LOOT_ROLL (Group.cpp:774-795): Player's vote, or when the roll ends
// Player's dice.
type LootRoll struct {
	Looted                   packet.GUID
	Slot                     uint32
	Player                   packet.GUID
	Item                     uint32
	RandomSuffix, RandomProp uint32
	Number, Type             uint8
}

func ParseLootRoll(body []byte) (LootRoll, error) {
	r := packet.NewReader(body)
	l := LootRoll{Looted: r.GUID(), Slot: r.U32(), Player: r.GUID(), Item: r.U32(), RandomSuffix: r.U32(), RandomProp: r.U32(),
		Number: r.U8(), Type: r.U8()}
	return l, done(r, "SMSG_LOOT_ROLL")
}

// Vote reads the (number, type) pairs the core sends: a vote is need (0, 0), greed
// (128, 2) or pass (128, 128) (Group.cpp:954-973); the dice at the end are (1..100,
// ROLL_NEED) or (1..100, ROLL_GREED) (Group.cpp:1160-1163, :1231-1234). rolled says Number
// is the dice.
func (l LootRoll) Vote() (choice string, rolled bool) {
	switch {
	case l.Type == 128:
		return "pass", false
	case l.Type == 0:
		return "need", false
	case l.Type == RollGreed && l.Number == 128:
		return "greed", false
	case l.Type == RollNeed:
		return "need", true
	case l.Type == RollGreed:
		return "greed", true
	}
	return fmt.Sprintf("unknown(%d)", l.Type), false
}

// LootRollWon is SMSG_LOOT_ROLL_WON (Group.cpp:797-818).
type LootRollWon struct {
	Looted                   packet.GUID
	Slot, Item               uint32
	RandomSuffix, RandomProp uint32
	Winner                   packet.GUID
	Number, Type             uint8
}

func ParseLootRollWon(body []byte) (LootRollWon, error) {
	r := packet.NewReader(body)
	w := LootRollWon{Looted: r.GUID(), Slot: r.U32(), Item: r.U32(), RandomSuffix: r.U32(), RandomProp: r.U32(),
		Winner: r.GUID(), Number: r.U8(), Type: r.U8()}
	return w, done(r, "SMSG_LOOT_ROLL_WON")
}

// Choice is what the winner rolled: need or greed.
func (w LootRollWon) Choice() string { return RollChoiceName(w.Type) }

// LootAllPassed is SMSG_LOOT_ALL_PASSED (Group.cpp:820-838), whose random property comes
// before the suffix.
type LootAllPassed struct {
	Looted                   packet.GUID
	Slot, Item               uint32
	RandomProp, RandomSuffix uint32
}

func ParseLootAllPassed(body []byte) (LootAllPassed, error) {
	r := packet.NewReader(body)
	p := LootAllPassed{Looted: r.GUID(), Slot: r.U32(), Item: r.U32(), RandomProp: r.U32(), RandomSuffix: r.U32()}
	return p, done(r, "SMSG_LOOT_ALL_PASSED")
}

// done reports a short body, or bytes left over, as an error naming the packet.
func done(r *packet.Reader, name string) error {
	if err := r.Err(); err != nil {
		return fmt.Errorf("%s: %w", name, err)
	}
	if n := r.Remaining(); n != 0 {
		return fmt.Errorf("%s: %d bytes unread", name, n)
	}
	return nil
}
