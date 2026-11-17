package game_test

import (
	"bytes"
	"testing"

	"github.com/TortoiseBots/turtle-agent/internal/game"
	"github.com/TortoiseBots/turtle-agent/internal/game/gametest"
	"github.com/TortoiseBots/turtle-agent/internal/packet"
)

func TestLootRollChoices(t *testing.T) {
	// RollVote (Group.h:72-79): pass 0, need 1, greed 2.
	for name, want := range map[string]uint8{"pass": 0, "need": 1, "greed": 2, "Greed": 2} {
		if v, ok := game.RollChoiceByName(name); !ok || v != want {
			t.Errorf("%s: %d %v", name, v, ok)
		}
	}
	if game.RollChoiceName(2) != "greed" || game.RollChoiceName(3) != "unknown(3)" {
		t.Fatal("RollChoiceName")
	}
	if _, ok := game.RollChoiceByName("disenchant"); ok {
		t.Fatal("disenchant is not a 1.12 choice")
	}
	// HandleLootRoll reads u64 looted guid, u32 item slot, u8 roll type (GroupHandler.cpp:405-425).
	if b := game.BuildLootRoll(wolf, 2, game.RollGreed); !bytes.Equal(b, packet.NewWriter().GUID(wolf).U32(2).U8(2).Bytes()) {
		t.Fatalf("% X", b)
	}
}

func TestLootStartRollAndEnd(t *testing.T) {
	s, err := game.ParseLootStartRoll(gametest.LootStartRoll(wolf, 1, 2589, 7, 60000))
	if err != nil || s != (game.LootStartRoll{Looted: wolf, Slot: 1, Item: 2589, RandomProp: 7, Countdown: 60000}) {
		t.Fatalf("%+v %v", s, err)
	}
	w, err := game.ParseLootRollWon(gametest.LootRollWon(wolf, 1, 2589, 7, gorm, 87, 2))
	if err != nil || w != (game.LootRollWon{Looted: wolf, Slot: 1, Item: 2589, RandomProp: 7, Winner: gorm, Number: 87, Type: 2}) || w.Choice() != "greed" {
		t.Fatalf("%+v %v", w, err)
	}
	// A lone looter wins at once with 100 and need (Group.cpp:1091-1095).
	if w, _ := game.ParseLootRollWon(gametest.LootRollWon(wolf, 1, 2589, 0, gorm, 100, 1)); w.Choice() != "need" {
		t.Fatalf("lone looter %+v", w)
	}
	p, err := game.ParseLootAllPassed(gametest.LootAllPassed(wolf, 3, 4537, 9))
	if err != nil || p != (game.LootAllPassed{Looted: wolf, Slot: 3, Item: 4537, RandomProp: 9}) {
		t.Fatalf("%+v %v", p, err)
	}
	for name, fn := range map[string]func([]byte) error{
		"start":  func(b []byte) error { _, err := game.ParseLootStartRoll(b); return err },
		"won":    func(b []byte) error { _, err := game.ParseLootRollWon(b); return err },
		"passed": func(b []byte) error { _, err := game.ParseLootAllPassed(b); return err },
		"roll":   func(b []byte) error { _, err := game.ParseLootRoll(b); return err },
	} {
		if fn(make([]byte, 7)) == nil {
			t.Errorf("%s: short body accepted", name)
		}
	}
}

// The core reports a vote and, when the roll ends, the dice in the same packet
// (Group.cpp:954-973, :1160-1163, :1231-1234).
func TestLootRollVotesAndDice(t *testing.T) {
	for _, c := range []struct {
		number, typ uint8
		choice      string
		rolled      bool
	}{
		{0, 0, "need", false},
		{128, 2, "greed", false},
		{128, 128, "pass", false},
		{57, 1, "need", true},
		{3, 2, "greed", true},
	} {
		r, err := game.ParseLootRoll(gametest.LootRoll(wolf, 1, gorm, 2589, 7, c.number, c.typ))
		if err != nil || r.Looted != wolf || r.Slot != 1 || r.Player != gorm || r.Item != 2589 || r.RandomProp != 7 {
			t.Fatalf("%+v %v", r, err)
		}
		choice, rolled := r.Vote()
		if choice != c.choice || rolled != c.rolled {
			t.Errorf("(%d, %d): %s %v, want %s %v", c.number, c.typ, choice, rolled, c.choice, c.rolled)
		}
	}
}
