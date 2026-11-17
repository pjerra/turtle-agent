package game_test

import (
	"bytes"
	"testing"

	"github.com/TortoiseBots/turtle-agent/internal/game"
	"github.com/TortoiseBots/turtle-agent/internal/game/gametest"
	"github.com/TortoiseBots/turtle-agent/internal/packet"
)

func TestRaidIconNames(t *testing.T) {
	// The bots read the same order (RtiTargetValue.h:20-31): star 0 .. skull 7.
	for i, name := range []string{"star", "circle", "diamond", "triangle", "moon", "square", "cross", "skull"} {
		if id, ok := game.RaidIconByName(name); !ok || id != uint8(i) || game.RaidIconName(uint8(i)) != name {
			t.Errorf("%s: %d %v %q", name, id, ok, game.RaidIconName(uint8(i)))
		}
	}
	if id, ok := game.RaidIconByName("SKULL"); !ok || id != 7 {
		t.Fatal("case-insensitive")
	}
	if _, ok := game.RaidIconByName("clear"); ok {
		t.Fatal("clear is not an icon")
	}
	if game.RaidIconName(8) != "" {
		t.Fatal("icon 8")
	}
}

func TestRaidTargetUpdateLayouts(t *testing.T) {
	// HandleRaidTargetUpdateOpcode reads u8 icon, then a u64 guid unless the icon is 0xFF
	// (GroupHandler.cpp:476-502).
	if b := game.BuildRaidTargetUpdate(7, wolf); !bytes.Equal(b, packet.NewWriter().U8(7).GUID(wolf).Bytes()) {
		t.Fatalf("set % X", b)
	}
	if b := game.BuildRaidTargetUpdate(7, 0); !bytes.Equal(b, packet.NewWriter().U8(7).GUID(0).Bytes()) {
		t.Fatalf("clear % X", b)
	}
	if b := game.BuildRaidTargetRequest(); !bytes.Equal(b, []byte{0xFF}) {
		t.Fatalf("request % X", b)
	}

	u, err := game.ParseRaidTargetUpdate(gametest.RaidTargetDelta(7, wolf))
	if err != nil || u.Full || len(u.Marks) != 1 || u.Marks[0] != (game.RaidMark{Icon: 7, GUID: wolf}) {
		t.Fatalf("delta %+v %v", u, err)
	}
	u, err = game.ParseRaidTargetUpdate(gametest.RaidTargetDelta(2, 0))
	if err != nil || len(u.Marks) != 1 || u.Marks[0] != (game.RaidMark{Icon: 2}) {
		t.Fatalf("delta clear %+v %v", u, err)
	}
	u, err = game.ParseRaidTargetUpdate(gametest.RaidTargetList([8]packet.GUID{0: gorm, 7: wolf}))
	if err != nil || !u.Full || len(u.Marks) != 2 || u.Marks[0] != (game.RaidMark{Icon: 0, GUID: gorm}) || u.Marks[1] != (game.RaidMark{Icon: 7, GUID: wolf}) {
		t.Fatalf("list %+v %v", u, err)
	}
	if u, err = game.ParseRaidTargetUpdate(gametest.RaidTargetList([8]packet.GUID{})); err != nil || !u.Full || len(u.Marks) != 0 {
		t.Fatalf("empty list %+v %v", u, err)
	}
	for name, body := range map[string][]byte{
		"empty":         nil,
		"short delta":   gametest.RaidTargetDelta(7, wolf)[:5],
		"short list":    gametest.RaidTargetList([8]packet.GUID{3: wolf})[:6],
		"icon 8":        gametest.RaidTargetDelta(8, wolf),
		"trailing byte": append(gametest.RaidTargetDelta(7, wolf), 0),
	} {
		if _, err := game.ParseRaidTargetUpdate(body); err == nil {
			t.Errorf("%s: no error", name)
		}
	}
}
