package game_test

import (
	"bytes"
	"testing"

	"github.com/pjerra/turtle-agent/internal/game"
	"github.com/pjerra/turtle-agent/internal/game/gametest"
	"github.com/pjerra/turtle-agent/internal/packet"
)

// Pet packet layouts, byte for byte, as the core reads and writes them (tortoise-wow 5d79486; cites in internal/game/pet.go).

const petG = packet.GUID(0xF140000000000007)

func TestPetActionLayout(t *testing.T) {
	// CMSG_PET_ACTION: u64 pet, u32 button, u64 target (Handlers/PetHandler.cpp:35-46). Attack is command 2, type 7.
	got := game.BuildPetAction(petG, game.PetButton(game.PetCmdAttack, game.PetActCommand), 0xF130000000000099)
	want := []byte{
		0x07, 0, 0, 0, 0, 0, 0x40, 0xF1, // pet guid
		0x02, 0, 0, 0x07, // button: action 2, type 0x07 in the top byte
		0x99, 0, 0, 0, 0, 0, 0x30, 0xF1, // target
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("% x\nwant % x", got, want)
	}
	if len(got) != 20 {
		t.Fatalf("the core reads 20 bytes, the packet has %d", len(got))
	}
	// A spell button keeps its autocast type (0xC1 on, 0x81 off) in the top byte.
	b := game.PetButton(2649, game.PetActEnabled)
	if b != 0xC1000A59 || game.PetButtonAction(b) != 2649 || game.PetButtonType(b) != 0xC1 {
		t.Fatalf("%#x", b)
	}
	// Follow, stay and the reactions are buttons of their own types (CommandStates, ReactStates; Objects/UnitDefines.h:486-498).
	if game.PetButton(game.PetCmdFollow, game.PetActCommand) != 0x07000001 || game.PetButton(game.PetCmdStay, game.PetActCommand) != 0x07000000 ||
		game.PetButton(game.PetReactDefensive, game.PetActReaction) != 0x06000001 || game.PetButton(game.PetReactPassive, game.PetActReaction) != 0x06000000 ||
		game.PetButton(game.PetReactAggressive, game.PetActReaction) != 0x06000002 {
		t.Fatal("a command or reaction button changed")
	}
}

func TestPetAutocastLayout(t *testing.T) {
	// CMSG_PET_SPELL_AUTOCAST: u64 pet, u32 spell, u8 state (Handlers/PetHandler.cpp:558-592).
	want := []byte{0x07, 0, 0, 0, 0, 0, 0x40, 0xF1, 0x59, 0x0A, 0, 0, 0}
	if got := game.BuildPetSpellAutocast(petG, 2649, false); !bytes.Equal(got, want) {
		t.Fatalf("% x\nwant % x", got, want)
	}
	want[12] = 1
	if got := game.BuildPetSpellAutocast(petG, 2649, true); !bytes.Equal(got, want) {
		t.Fatalf("% x\nwant % x", got, want)
	}
}

func TestPetSpellsRoundTripAndSize(t *testing.T) {
	bar := gametest.PetBar(game.PetBarSpell{Spell: 2649, Autocast: true}, game.PetBarSpell{Spell: 3716})
	body := gametest.PetSpells(petG, 1, 1, false, bar, []uint32{game.PetButton(2649, game.PetActEnabled)}, game.PetCooldown{SpellID: 2649, MS: 5000})
	// 8 guid + 4 + 4 state bytes + 10 buttons + 1 + 1 spell + 1 + 1 cooldown of 12 bytes (Player::PetSpellInitialize).
	if want := 8 + 4 + 4 + 40 + 1 + 4 + 1 + 12; len(body) != want {
		t.Fatalf("%d bytes, want %d", len(body), want)
	}
	p, err := game.ParsePetSpells(body)
	if err != nil {
		t.Fatal(err)
	}
	if p.Pet != petG || p.Cleared || p.React != 1 || p.Command != 1 || !p.Enabled || p.Bar != bar {
		t.Fatalf("%+v", p)
	}
	if len(p.Spells) != 1 || game.PetButtonAction(p.Spells[0]) != 2649 || len(p.Cooldowns) != 1 || p.Cooldowns[0] != (game.PetCooldown{SpellID: 2649, MS: 5000}) {
		t.Fatalf("%+v", p)
	}
	// The default bar: attack, follow, stay, the spells, aggressive, defensive, passive (CharmInfo::InitPetActionBar).
	if bar[0] != 0x07000002 || bar[1] != 0x07000001 || bar[2] != 0x07000000 || bar[3] != 0xC1000A59 || bar[4] != 0x81000E84 ||
		bar[5] != 0x81000000 || bar[7] != 0x06000002 || bar[8] != 0x06000001 || bar[9] != 0x06000000 {
		t.Fatalf("%#x", bar)
	}
	// The flag byte 0x08 is a bar that is off (Pet::IsEnabled).
	off, err := game.ParsePetSpells(gametest.PetSpells(petG, 0, 1, true, bar, nil))
	if err != nil || off.Enabled || off.React != 0 || len(off.Spells) != 0 {
		t.Fatalf("%+v %v", off, err)
	}
	// A demon: the bar carries the spells and the list is empty (the charm layout, Player::CharmSpellInitialize, has the same widths).
	if _, err := game.ParsePetSpells(gametest.PetSpells(petG, 1, 1, false, bar, nil)); err != nil {
		t.Fatal(err)
	}
}

func TestPetSpellsClearedAndShort(t *testing.T) {
	p, err := game.ParsePetSpells(gametest.PetSpellsCleared())
	if err != nil || !p.Cleared || p.Pet != 0 {
		t.Fatalf("%+v %v", p, err)
	}
	full := gametest.PetSpells(petG, 1, 1, false, gametest.PetBar(), nil)
	for _, n := range []int{0, 7, 9, 20, len(full) - 1} {
		if n == 0 {
			continue
		}
		if _, err := game.ParsePetSpells(full[:n]); err == nil {
			t.Fatalf("a body cut at %d must be an error", n)
		}
	}
	if _, err := game.ParsePetSpells(nil); err == nil {
		t.Fatal("an empty body must be an error")
	}
}

func TestPetModeCastFailedAndFeedback(t *testing.T) {
	m, err := game.ParsePetMode(gametest.PetMode(petG, 2, 0, true))
	if err != nil || m.Pet != petG || m.React != 2 || m.Command != 0 || m.Enabled {
		t.Fatalf("%+v %v", m, err)
	}
	if b := gametest.PetMode(petG, 1, 1, false); len(b) != 12 {
		t.Fatalf("SMSG_PET_MODE is 12 bytes (Pet::SetEnabled reserves 12), got %d", len(b))
	}
	if _, err := game.ParsePetMode([]byte{1, 2, 3}); err == nil {
		t.Fatal("short")
	}
	f, err := game.ParsePetCastFailed(gametest.PetCastFailed(2649, 60))
	if err != nil || f.SpellID != 2649 || f.Name() != "not_ready" {
		t.Fatalf("%+v %v", f, err)
	}
	if _, err := game.ParsePetCastFailed([]byte{1, 0}); err == nil {
		t.Fatal("short")
	}
	for msg, want := range map[uint8]string{1: "pet_dead", 2: "nothing_to_attack", 3: "cant_attack_target", 4: "no_path", 9: "feedback_9"} {
		got, err := game.ParsePetActionFeedback(gametest.PetActionFeedback(msg))
		if err != nil || got != msg || game.PetFeedbackName(got) != want {
			t.Fatalf("%d: %d %q %v", msg, got, game.PetFeedbackName(got), err)
		}
	}
	if _, err := game.ParsePetActionFeedback(nil); err == nil {
		t.Fatal("empty")
	}
}

func TestPetHappinessStates(t *testing.T) {
	for v, want := range map[uint32]uint8{0: game.PetUnhappy, 332999: game.PetUnhappy, 333000: game.PetContent, 665999: game.PetContent, 666000: game.PetHappy, 1000000: game.PetHappy} {
		if got := game.PetHappinessState(v); got != want {
			t.Errorf("happiness %d: state %d, want %d", v, got, want)
		}
	}
}
