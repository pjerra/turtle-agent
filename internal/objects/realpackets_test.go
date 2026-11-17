package objects

import (
	"encoding/hex"
	"testing"
	"time"

	"github.com/TortoiseBots/turtle-agent/internal/packet"
)

// Packets from the live run of #61 (m910q, TortoiseBots build 17), tests/ta61/agent.cap, in
// the order the server sent them when the agent ate Food (spell 1129 in aura slot 1): the
// duration (#4099), an item create that is no part of the aura (#4101), then the compressed
// update that puts the spell in the slot (#4102). #N is the record index in agent.cap. The
// player is 0x207 (the only player in these updates).
const realPlayer packet.GUID = 0x207

var (
	// #4099 SMSG_UPDATE_AURA_DURATION: u8 slot 1, u32 30000 ms.
	realFoodDuration = mustHex("0130750000")
	// #4101 SMSG_UPDATE_OBJECT: the item create for the food.
	realFoodItem = mustHex("01000000000287a94b0440011001000000025f41010000000000a94b04000000" +
		"004003000000f71100000000803f070200000702000001000000ffffffff")
	// #4102 SMSG_COMPRESSED_UPDATE_OBJECT: the player's slot 1 becomes 1129 (Food).
	realFoodAura = mustHex("d10000007801636200016676264d308381114434288048161031b840260b0343" +
		"280303838d2e0303e33b410686f695de2c0e4c0c0e6059b0e301fef2050a")
)

func mustHex(s string) []byte {
	b, err := hex.DecodeString(s)
	if err != nil {
		panic(err)
	}
	return b
}

// The server sends the duration before the aura's update (SpellAuras.cpp:6896-6909). It has
// to wait through the unrelated item create and then count down once the aura is in the slot.
func TestRealOrderFoodKeepsDuration(t *testing.T) {
	s, c := newClockStore()
	s.Apply([]Block{createBlock(realPlayer, TypePlayer, 0, 0, map[uint16]uint32{UnitHealth: 100, UnitMaxHealth: 100})})
	r := packet.NewReader(realFoodDuration)
	slot, ms := r.U8(), r.I32()
	if err := r.Err(); err != nil || r.Remaining() != 0 || slot != 1 || ms != 30000 {
		t.Fatalf("duration packet: slot %d, %d ms, %d bytes left, %v", slot, ms, r.Remaining(), err)
	}
	s.AuraDuration(realPlayer, int(slot), ms)
	item, err := ParseUpdate(realFoodItem)
	if err != nil {
		t.Fatal(err)
	}
	s.Apply(item)
	aura, err := ParseCompressedUpdate(realFoodAura)
	if err != nil {
		t.Fatal(err)
	}
	s.Apply(aura)
	c.add(5 * time.Second)
	u, _ := s.Get(realPlayer, 0, 0, 0)
	if len(u.Auras) != 1 || u.Auras[0].Slot != 1 || u.Auras[0].SpellID != 1129 || u.Auras[0].Name != "Food" ||
		u.Auras[0].RemainingMS == nil || *u.Auras[0].RemainingMS != 25000 {
		t.Fatalf("%+v", u.Auras)
	}
}
