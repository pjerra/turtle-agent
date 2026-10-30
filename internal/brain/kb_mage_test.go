package brain

import (
	"slices"
	"testing"
	"time"

	"github.com/pjerra/turtle-agent/internal/packet"
	"github.com/pjerra/turtle-agent/internal/spells"
)

// The knowledge-base rotations of the mage, the warlock and the hunter (tasks K2a-K2d, this file's mage specs): each recipe's
// play test. kbScene and kbPlays are shared with kb_warlock_test.go and kb_hunter_test.go.

// kbScene is the play test's world: a level-60 character of the class with its power full, the spells known, and a warrior tank
// holding one mob — 25 yards off for a ranged spec, 4 yards (in reach) for a melee one — on the KB preset with the KB rotation.
// tune adjusts the scene (a mage's buffs, for instance).
func kbScene(t *testing.T, class uint8, preset, rotation string, known []uint32, melee bool, tune func(*fakeGame)) *rig {
	t.Helper()
	g := newFake(known...)
	g.self.Class, g.self.Level = class, 60
	g.self.PowerType, g.self.Power, g.self.MaxPower = powerMana, manaFull, manaFull
	g.self.Health, g.self.MaxHealth = 1000, 1000
	mf := &meleeFake{posFake: &posFake{fakeGame: g}, dodged: map[packet.GUID]time.Time{}}
	cfg := DefaultConfig()
	cfg.Now = g.clk.Now
	b, err := New(mf, cfg, Params{Strategies: []string{preset}, Rotation: rotation})
	if err != nil {
		t.Fatal(err)
	}
	r := &rig{t: t, g: g, b: b}
	if melee {
		tankFights(g, 7, 4, 0)
	} else {
		tankFights(g, 22, 25, 0)
	}
	if tune != nil {
		tune(g)
	}
	return r
}

// kbPlays runs the play test of one KB spec: the first action is the file's first entry that can run (pinned by the author
// from the KB row, in the scene above), and one of the spec's core spells is cast within the first three actions (the "within
// 3 s of actions" of the task brief: the band-high cooldown the KB lists first may take the opening action). A tick before the
// first action ("-") is the damage dealer's opening delay holding the hostile entries, not the file.
func kbPlays(t *testing.T, r *rig, first string, core ...string) []string {
	t.Helper()
	got := actions(r, 100*time.Millisecond, 2200*time.Millisecond, 1600*time.Millisecond, 1600*time.Millisecond)
	i := 0
	for i < len(got) && got[i] == "-" {
		i++
	}
	if i == len(got) || got[i] != first {
		t.Fatalf("first action %q, want %q (the file's first entry that can run): %v (idle %q)", at(got, i), first, got, r.idle())
	}
	want := map[uint32]bool{}
	for _, name := range core {
		for _, id := range spells.IDsByName(name) {
			want[id] = true
		}
	}
	casts := spellSeq(r.g)
	ok := false
	for j := 0; j < len(casts) && j < 3; j++ {
		if want[casts[j]] {
			ok = true
			break
		}
	}
	if !ok {
		t.Fatalf("no core spell of %v among the first three casts %v (actions %v)", core, casts, got)
	}
	return got
}

// at is got[i], "-" past the end (the failure message of a scene that never acted).
func at(got []string, i int) string {
	if i < len(got) {
		return got[i]
	}
	return "-"
}

// The mage KB play tests' spellbooks: the rotation's spells and the preset's upkeep, at their highest rank of level 60.
var (
	kbArcaneSpells = []uint32{5143, 5144, 5145, 51949, 51950, 51933, 51934, 2136, 2137, 1449, 1467, 133, 143, 116, 837,
		168, 7300, 12042, 12043, 12051}
	kbFireSpells = []uint32{133, 143, 145, 2948, 8444, 2136, 2137, 11366, 12505, 11129, 11113, 13018, 2120, 2121,
		1449, 1467, 116, 837, 168, 7300}
	kbFrostSpells = []uint32{116, 837, 205, 120, 8492, 122, 865, 11426, 13031, 12472, 10, 6141, 1449, 1467,
		52516, 52501, 168, 7300}
)

// TestKBArcaneMagePlays: at full mana the KB's first cooldown row (Arcane Power, band high) opens, and the Rupture inside it
// follows — the first core spell. Arcane Missiles takes over after the Rupture debuff is up.
func TestKBArcaneMagePlays(t *testing.T) {
	r := kbScene(t, ClassMage, "arcane_mage", "kb_arcane_mage", kbArcaneSpells, false, buffUp)
	got := kbPlays(t, r, "arcane_power", "Arcane Missiles", "Arcane Rupture")
	if got[2] != "arcane_rupture" {
		t.Fatalf("the Rupture did not follow Arcane Power: %v", got)
	}
}

// TestKBFireMagePlays: no cooldown of the KB's list is up at the pull, so the first entry that can run is Scorch (Fire
// Vulnerability below five stacks on a mob above 70%), and a core spell is on the first cast.
func TestKBFireMagePlays(t *testing.T) {
	r := kbScene(t, ClassMage, "fire_mage", "kb_fire_mage", kbFireSpells, false, buffUp)
	kbPlays(t, r, "scorch", "Fireball", "Scorch", "Fire Blast")
}

// TestKBFrostMagePlays: Ice Barrier is the KB's first cooldown (band high) and goes up at the pull; Frostbolt, the filler,
// follows. Icicles waits for a Flash Freeze proc, which the scene does not have.
func TestKBFrostMagePlays(t *testing.T) {
	r := kbScene(t, ClassMage, "frost_mage", "kb_frost_mage", kbFrostSpells, false, buffUp)
	got := kbPlays(t, r, "ice_barrier_cooldown", "Frostbolt", "Icicles", "Ice Barrier")
	if got[1] != "frostbolt" {
		t.Fatalf("the filler did not follow Ice Barrier: %v", got)
	}
	for _, a := range got {
		if a == "icicles" {
			t.Fatalf("Icicles was cast without a Flash Freeze proc: %v", got)
		}
	}
}

// TestKBMageRotationsReplaceTheFrostList pins what a KB preset is made of: the preset's own strategies, with mage_frost replaced
// by the KB rotation (and nothing else changed).
func TestKBMageRotationsReplaceTheFrostList(t *testing.T) {
	for _, tc := range []struct{ preset, rotation string }{
		{"arcane_mage", "kb_arcane_mage"}, {"fire_mage", "kb_fire_mage"}, {"frost_mage", "kb_frost_mage"},
	} {
		rot, err := RotationNamed(tc.rotation)
		if err != nil {
			t.Fatal(err)
		}
		if rot.KB == nil || rot.KB.Preset != tc.preset {
			t.Errorf("%s: the rotation names preset %v, want %s", tc.rotation, rot.KB, tc.preset)
		}
		strats, _, err := ResolveFor([]string{tc.preset}, ResolveOpts{Rotation: tc.rotation})
		if err != nil {
			t.Fatalf("%s: %v", tc.preset, err)
		}
		var names []string
		for _, s := range strats {
			names = append(names, s.Name)
		}
		if !slices.Contains(names, "rot:"+tc.rotation) || slices.Contains(names, "mage_frost") {
			t.Errorf("%s plays %v, want rot:%s and no mage_frost", tc.preset, names, tc.rotation)
		}
	}
}
