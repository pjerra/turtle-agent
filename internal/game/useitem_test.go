package game_test

import (
	"bytes"
	"testing"

	"github.com/TortoiseBots/turtle-agent/internal/game"
	"github.com/TortoiseBots/turtle-agent/internal/game/gametest"
	"github.com/TortoiseBots/turtle-agent/internal/packet"
)

// readUseItem decodes CMSG_USE_ITEM the way HandleUseItemOpcode and SpellCastTargets::read
// do (SpellHandler.cpp:37-42, :134; Spell.cpp:180-228) and reports it did not run off the end.
func readUseItem(t *testing.T, body []byte) (bag, slot uint8, mask uint16, unit packet.GUID) {
	t.Helper()
	r := packet.NewReader(body)
	bag, slot = r.U8(), r.U8()
	r.U8() // spell_count
	mask = r.U16()
	if mask&game.TargetFlagUnit != 0 {
		unit = r.PackedGUID()
	}
	if err := r.Err(); err != nil || r.Remaining() != 0 {
		t.Fatalf("% X: %v, %d bytes left", body, err, r.Remaining())
	}
	return
}

func TestBuildUseItem(t *testing.T) {
	// On the player: bag, slot, spell count, mask 0 (TARGET_FLAG_SELF reads nothing more).
	if b := game.BuildUseItem(255, 24, 0); !bytes.Equal(b, []byte{255, 24, 0, 0, 0}) {
		t.Fatalf("self % X", b)
	}
	bag, slot, mask, unit := readUseItem(t, game.BuildUseItem(19, 3, 0x4000000000001234))
	if bag != 19 || slot != 3 || mask != game.TargetFlagUnit || unit != 0x4000000000001234 {
		t.Fatalf("unit target: %d %d %#x %s", bag, slot, mask, unit)
	}
}

func TestCancelAuraAndMountResults(t *testing.T) {
	if b := game.BuildCancelAura(458); !bytes.Equal(b, []byte{0xCA, 0x01, 0, 0}) {
		t.Fatalf("% X", b)
	}
	v, err := game.ParseResultCode(gametest.ResultCode(10), "SMSG_MOUNTRESULT")
	if err != nil || game.MountResultName(v) != "ok" {
		t.Fatalf("%d %v", v, err)
	}
	if _, err := game.ParseResultCode([]byte{1, 0}, "SMSG_MOUNTRESULT"); err == nil {
		t.Fatal("short body")
	}
	if game.MountResultName(6) != "looting" || game.DismountResultName(1) != "not_mounted" || game.StandStateName(1) != "sit" || game.StandStateName(0) != "stand" {
		t.Fatal("names")
	}
}
