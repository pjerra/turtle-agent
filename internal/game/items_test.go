package game_test

import (
	"bytes"
	"testing"

	"github.com/TortoiseBots/turtle-agent/internal/game"
	"github.com/TortoiseBots/turtle-agent/internal/game/gametest"
)

const sword = 0x4000000000000003

func TestAutoEquipAndChangeFailure(t *testing.T) {
	// HandleAutoEquipItemOpcode reads u8 bag, u8 slot (ItemHandler.cpp:159-163).
	if b := game.BuildAutoEquipItem(255, 23); !bytes.Equal(b, []byte{255, 23}) {
		t.Fatalf("% X", b)
	}
	f, err := game.ParseInventoryChangeFailure(gametest.InventoryChangeFailure(1, 40, sword, 0))
	if err != nil || f != (game.InventoryChangeFailure{Result: 1, RequiredLevel: 40, Item: sword}) {
		t.Fatalf("level %+v %v", f, err)
	}
	f, err = game.ParseInventoryChangeFailure(gametest.InventoryChangeFailure(2, 0, sword, 0))
	if err != nil || f != (game.InventoryChangeFailure{Result: 2, Item: sword}) {
		t.Fatalf("skill %+v %v", f, err)
	}
	if f, err := game.ParseInventoryChangeFailure(gametest.InventoryChangeFailure(0, 0, 0, 0)); err != nil || f.Result != 0 {
		t.Fatalf("ok %+v %v", f, err)
	}
	if _, err := game.ParseInventoryChangeFailure(gametest.InventoryChangeFailure(2, 0, sword, 0)[:9]); err == nil {
		t.Fatal("short body")
	}
}
