package game_test

import (
	"bytes"
	"reflect"
	"testing"

	"github.com/TortoiseBots/turtle-agent/internal/game"
	"github.com/TortoiseBots/turtle-agent/internal/game/gametest"
)

const corpse = 0xF130000123000001

func TestLootRequests(t *testing.T) {
	// CMSG_LOOT and CMSG_LOOT_RELEASE carry a u64; CMSG_AUTOSTORE_LOOT_ITEM a u8 slot
	// (LootHandler.cpp:41-51, :414-420, :452-462).
	b := game.BuildLoot(corpse)
	if len(b) != 8 || b[0] != 1 || b[7] != 0xF1 || !bytes.Equal(b, game.BuildLootRelease(corpse)) {
		t.Fatalf("% X", b)
	}
	if b := game.BuildAutoStoreLootItem(3); !bytes.Equal(b, []byte{3}) {
		t.Fatalf("% X", b)
	}
}

func TestParseLootResponse(t *testing.T) {
	body := gametest.LootResponse(corpse, game.LootCorpse, 253,
		gametest.LootSlot{Slot: 0, Item: 2589, Count: 2, Display: 7, SlotType: game.LootSlotAllow},
		gametest.LootSlot{Slot: 3, Item: 118, Count: 1, Prop: 5, SlotType: game.LootSlotRolling})
	l, err := game.ParseLootResponse(body)
	want := game.LootResponse{GUID: corpse, Type: 1, Money: 253, Items: []game.LootItem{
		{Slot: 0, Item: 2589, Count: 2, DisplayID: 7},
		{Slot: 3, Item: 118, Count: 1, RandomProp: 5, SlotType: 1}}}
	if err != nil || !reflect.DeepEqual(l, want) {
		t.Fatalf("%+v %v", l, err)
	}
	if game.LootTakeable(l.Items[1].SlotType) || !game.LootTakeable(l.Items[0].SlotType) {
		t.Fatal("takeable")
	}
	// An empty window is the money and a zero count.
	if l, err := game.ParseLootResponse(gametest.LootResponse(corpse, game.LootCorpse, 0)); err != nil || len(l.Items) != 0 || l.Refused {
		t.Fatalf("empty %+v %v", l, err)
	}
	// A refusal is the guid, a zero type and the LootError (Player.cpp:9278-9284).
	l, err = game.ParseLootResponse(gametest.LootRefused(corpse, 4))
	if err != nil || !l.Refused || l.Err != 4 || game.LootErrorName(l.Err) != "too_far" {
		t.Fatalf("refused %+v %v", l, err)
	}
	if _, err := game.ParseLootResponse(body[:len(body)-1]); err == nil {
		t.Fatal("short body")
	}
	if _, err := game.ParseLootResponse(append(body, 0)); err == nil {
		t.Fatal("trailing byte")
	}
}

// One item, written out byte by byte from Player::SendLoot and operator<<(LootView) (guid,
// type 1, money 253, one item: slot 0, item 2589, count 1, display 7, suffix 0, property 0,
// slot type 0), so the builder in gametest is not the only witness of the layout.
func TestParseLootResponseHex(t *testing.T) {
	body := []byte{
		0x01, 0x00, 0x00, 0x2B, 0x01, 0x00, 0x30, 0xF1, // guid 0xF13000012B000001
		0x01,                   // LOOT_CORPSE
		0xFD, 0x00, 0x00, 0x00, // money
		0x01,                   // item count
		0x00,                   // slot
		0x1D, 0x0A, 0x00, 0x00, // item 2589
		0x01, 0x00, 0x00, 0x00, // count
		0x07, 0x00, 0x00, 0x00, // display id
		0x00, 0x00, 0x00, 0x00, // random suffix
		0x00, 0x00, 0x00, 0x00, // random property
		0x00, // slot type
	}
	l, err := game.ParseLootResponse(body)
	want := game.LootResponse{GUID: 0xF13000012B000001, Type: 1, Money: 253, Items: []game.LootItem{{Item: 2589, Count: 1, DisplayID: 7}}}
	if err != nil || !reflect.DeepEqual(l, want) {
		t.Fatalf("%+v %v", l, err)
	}
	if !bytes.Equal(gametest.LootResponse(want.GUID, 1, 253, gametest.LootSlot{Item: 2589, Count: 1, Display: 7}), body) {
		t.Fatal("gametest.LootResponse differs from the hex fixture")
	}
	if game.LootErrorName(15) != "already_pickpocketed" || game.LootErrorName(16) != "not_while_shapeshifted" {
		t.Fatal("loot error names")
	}
}

func TestParseLootNotices(t *testing.T) {
	if g, err := game.ParseLootRelease(gametest.LootRelease(corpse)); err != nil || g != corpse {
		t.Fatalf("%s %v", g, err)
	}
	if s, err := game.ParseLootRemoved(gametest.LootRemoved(5)); err != nil || s != 5 {
		t.Fatalf("%d %v", s, err)
	}
	if m, err := game.ParseLootMoneyNotify([]byte{0x10, 0x27, 0, 0}); err != nil || m != 10000 {
		t.Fatalf("%d %v", m, err)
	}
	if _, err := game.ParseLootRemoved(nil); err == nil {
		t.Fatal("empty SMSG_LOOT_REMOVED")
	}
}
