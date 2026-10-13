package game_test

import (
	"testing"

	"github.com/pjerra/turtle-agent/internal/capture"
	"github.com/pjerra/turtle-agent/internal/game"
	"github.com/pjerra/turtle-agent/internal/game/gametest"
	"github.com/pjerra/turtle-agent/internal/opcode"
	"github.com/pjerra/turtle-agent/internal/packet"
	"github.com/pjerra/turtle-agent/internal/testfixture"
)

// The login of a level-1 GM warrior on the m910q test server (testdata/captures/m1-login.twcap,
// record SMSG_INITIAL_SPELLS, 213 bytes): 52 spells, no cooldowns. It holds Heroic Strike (78)
// and Battle Stance (2457), and spell ids above 45000 that only Turtle has.
func TestInitialSpellsFromCapture(t *testing.T) {
	bodies := capture.Bodies(testfixture.Load(t, "m1-login.twcap"), opcode.SMSGInitialSpells)
	if len(bodies) != 1 || len(bodies[0]) != 213 {
		t.Fatalf("%d SMSG_INITIAL_SPELLS records", len(bodies))
	}
	in, err := game.ParseInitialSpells(bodies[0])
	if err != nil {
		t.Fatal(err)
	}
	if len(in.Spells) != 52 || len(in.Cooldowns) != 0 {
		t.Fatalf("%d spells, %d cooldowns", len(in.Spells), len(in.Cooldowns))
	}
	have := map[uint32]bool{}
	for _, id := range in.Spells {
		have[id] = true
	}
	for _, id := range []uint32{7, 78, 2457, 46001, 56047} {
		if !have[id] {
			t.Errorf("spell %d missing", id)
		}
	}
	// The builder writes what the core writes: the same bytes come back.
	if got := gametest.InitialSpells(in.Spells, nil); string(got) != string(bodies[0]) {
		t.Errorf("the builder differs from the capture:\n% X\n% X", got, bodies[0])
	}
}

func TestInitialSpellsCooldowns(t *testing.T) {
	in, err := game.ParseInitialSpells(gametest.InitialSpells([]uint32{116, 133}, []game.InitialCooldown{
		{Spell: 133, Category: 4, Cooldown: 7500, CategoryCooldown: 7000},
		{Spell: 5, Item: 6948, Cooldown: 1, CategoryCooldown: game.InfiniteCooldown},
	}))
	if err != nil || len(in.Spells) != 2 || len(in.Cooldowns) != 2 {
		t.Fatalf("%+v %v", in, err)
	}
	if c := in.Cooldowns[0]; c.Spell != 133 || c.Category != 4 || c.Cooldown != 7500 || c.CategoryCooldown != 7000 {
		t.Errorf("%+v", c)
	}
	if c := in.Cooldowns[1]; c.Item != 6948 || c.CategoryCooldown != game.InfiniteCooldown {
		t.Errorf("%+v", c)
	}
	// A count the bytes cannot hold is refused, not looped over.
	bad := packet.NewWriter().U8(0).U16(60000).Bytes()
	if _, err := game.ParseInitialSpells(bad); err == nil {
		t.Error("an absurd spell count was accepted")
	}
	if _, err := game.ParseInitialSpells(gametest.InitialSpells([]uint32{1}, nil)[:5]); err == nil {
		t.Error("a cut packet was accepted")
	}
}

func TestSpellbookChanges(t *testing.T) {
	if id, err := game.ParseRemovedSpell(gametest.RemovedSpell(8401)); err != nil || id != 8401 {
		t.Errorf("removed %d %v", id, err)
	}
	if o, n, err := game.ParseSupercededSpell(gametest.SupercededSpell(116, 205)); err != nil || o != 116 || n != 205 {
		t.Errorf("superceded %d %d %v", o, n, err)
	}
	if _, err := game.ParseRemovedSpell([]byte{1}); err == nil {
		t.Error("a short removed-spell packet was accepted")
	}
	if _, _, err := game.ParseSupercededSpell([]byte{1, 0, 2}); err == nil {
		t.Error("a short superceded packet was accepted")
	}
}

func TestCooldownPackets(t *testing.T) {
	me := packet.GUID(0x2A)
	c, err := game.ParseSpellCooldown(gametest.SpellCooldown(me, game.SpellCooldown{Spell: 116, MS: 8000}, game.SpellCooldown{Spell: 133, MS: 8000}))
	if err != nil || c.Unit != me || len(c.Cooldowns) != 2 || c.Cooldowns[1] != (game.SpellCooldown{Spell: 133, MS: 8000}) {
		t.Fatalf("%+v %v", c, err)
	}
	if c, err := game.ParseSpellCooldown(gametest.SpellCooldown(me)); err != nil || len(c.Cooldowns) != 0 {
		t.Errorf("empty list: %+v %v", c, err)
	}
	if _, err := game.ParseSpellCooldown(append(gametest.SpellCooldown(me, game.SpellCooldown{Spell: 1, MS: 2}), 7)); err == nil {
		t.Error("stray bytes were accepted")
	}
	e, err := game.ParseCooldownEvent(gametest.CooldownEvent(1856, me))
	if err != nil || e.Spell != 1856 || e.Unit != me {
		t.Fatalf("event %+v %v", e, err)
	}
	e, err = game.ParseClearCooldown(gametest.CooldownEvent(11129, me))
	if err != nil || e.Spell != 11129 || e.Unit != me {
		t.Fatalf("clear %+v %v", e, err)
	}
	if g, err := game.ParseCooldownCheat(gametest.CooldownCheat(me)); err != nil || g != me {
		t.Errorf("cheat %v %v", g, err)
	}
	if _, err := game.ParseCooldownEvent([]byte{1, 2, 3}); err == nil {
		t.Error("a short cooldown event was accepted")
	}
}

func TestChannelPackets(t *testing.T) {
	if s, ms, err := game.ParseChannelStart(gametest.ChannelStart(5143, 3000)); err != nil || s != 5143 || ms != 3000 {
		t.Errorf("start %d %d %v", s, ms, err)
	}
	if ms, err := game.ParseChannelUpdate(gametest.ChannelUpdate(1500)); err != nil || ms != 1500 {
		t.Errorf("update %d %v", ms, err)
	}
	if _, _, err := game.ParseChannelStart([]byte{1, 2, 3, 4}); err == nil {
		t.Error("a short channel start was accepted")
	}
}

// The cast results the live fight captures hold (testdata/captures/m5-attack.twcap, m6-fight.twcap):
// Heroic Strike (78) refused with SPELL_FAILED_EQUIPPED_ITEM_CLASS, which carries three u32 more,
// and a success.
func TestCastResultFromCaptures(t *testing.T) {
	var refused, ok int
	for _, name := range []string{"m5-attack.twcap", "m6-fight.twcap"} {
		for _, b := range capture.Bodies(testfixture.Load(t, name), opcode.SMSGCastResult) {
			c, err := game.ParseCastResult(b)
			if err != nil {
				t.Fatal(err)
			}
			if c.Failed {
				refused++
				if c.SpellID != 78 || c.Reason != 25 || game.CastFailName(c.Reason) != "equipped_item_class" {
					t.Errorf("%+v %s", c, game.CastFailName(c.Reason))
				}
			} else {
				ok++
			}
		}
	}
	if refused != 2 || ok != 1 {
		t.Errorf("%d refused, %d ok", refused, ok)
	}
}

func TestCastFailNames(t *testing.T) {
	for code, want := range map[uint8]string{0: "affecting_combat", 10: "bad_targets", 35: "interrupted", 46: "moving", 60: "not_ready", 77: "no_power", 89: "out_of_range", 145: "unknown", 200: "failed_200"} {
		if got := game.CastFailName(code); got != want {
			t.Errorf("%d: %q, want %q", code, got, want)
		}
	}
	if game.CastFailedInterrupted != 35 || game.CastFailedNotReady != 60 {
		t.Error("the named codes moved")
	}
}
