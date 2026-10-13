package spells

import (
	"bytes"
	"compress/gzip"
	"testing"
)

// The numbers below were read off the core's spell_template and the server's DBCs when the table
// was made (make spellnames); each is the vanilla value, which the Turtle data keeps.
func TestLookup(t *testing.T) {
	cases := []struct {
		id   uint32
		name string
		fn   func(t *testing.T, m Meta)
	}{
		{78, "Heroic Strike", func(t *testing.T, m Meta) {
			// A next-swing rage ability: no cast time, no global cooldown, 5 yd, 15 rage (tenths).
			if m.Rank != 1 || m.Power != PowerRage || m.Cost != 150 || m.CastMS != 0 || m.GCDMS != 0 || m.RangeMax != 5 || !m.Flags.Has(FlagNextSwing|FlagDamage) {
				t.Errorf("%+v", m)
			}
		}},
		{116, "Frostbolt", func(t *testing.T, m Meta) {
			if m.Rank != 1 || m.Level != 4 || m.Power != PowerMana || m.Cost != 25 || m.CastMS != 1500 || m.RangeMax != 30 ||
				m.GCDCategory != 133 || m.GCDMS != 1500 || m.School != 4 || !m.Flags.Has(FlagDamage|FlagAura|FlagMoveInterrupts|FlagInterruptible) {
				t.Errorf("%+v", m)
			}
			// The chill comes first (SPELL_AURA_MOD_DECREASE_SPEED, 33), the damage second.
			if e := m.Effects[0]; e.Effect != 6 || e.Aura != 33 || m.Effects[1].Effect != 2 || m.Effects[1].Base != 17 || m.DurationMS != 5000 {
				t.Errorf("effects %+v", m.Effects)
			}
		}},
		{1953, "Blink", func(t *testing.T, m Meta) {
			// Its 15 s sits in the category (44), not in the spell's own cooldown.
			if m.CooldownMS != 0 || m.Category != 44 || m.CategoryCDMS != 15000 || m.CastMS != 0 || m.GCDMS != 1500 {
				t.Errorf("%+v", m)
			}
		}},
		{2687, "Bloodrage", func(t *testing.T, m Meta) {
			if m.CooldownMS != 60000 || m.GCDMS != 0 || !m.Flags.Has(FlagEnergize) {
				t.Errorf("%+v", m)
			}
		}},
		{355, "Taunt", func(t *testing.T, m Meta) {
			if m.Category != 82 || m.CategoryCDMS != 10000 || !m.Flags.Has(FlagTaunt) {
				t.Errorf("%+v", m)
			}
		}},
		{6552, "Pummel", func(t *testing.T, m Meta) {
			if m.Category != 88 || m.CategoryCDMS != 10000 || m.Cost != 100 || !m.Flags.Has(FlagInterrupt) {
				t.Errorf("%+v", m)
			}
		}},
		{2139, "Counterspell", func(t *testing.T, m Meta) {
			if m.Category != 88 || m.CategoryCDMS != 30000 || m.RangeMax != 30 || !m.Flags.Has(FlagInterrupt) {
				t.Errorf("%+v", m)
			}
		}},
		{75, "Auto Shot", func(t *testing.T, m Meta) {
			if m.RangeMin != 8 || m.RangeMax != 35 || !m.Flags.Has(FlagRanged|FlagAutoRepeat) {
				t.Errorf("%+v", m)
			}
		}},
		{2098, "Eviscerate", func(t *testing.T, m Meta) {
			if m.Power != PowerEnergy || m.Cost != 30 || !m.Flags.Has(FlagComboPoints|FlagDamage) || m.Effects[0].PerComboPoint == 0 {
				t.Errorf("%+v", m)
			}
		}},
		{2457, "Battle Stance", func(t *testing.T, m Meta) {
			if m.Rank != 0 || m.Category != 47 || m.DurationMS != -1 {
				t.Errorf("%+v", m)
			}
		}},
	}
	for _, c := range cases {
		m, ok := Lookup(c.id)
		if !ok || Name(c.id) != c.name || m.ID != c.id {
			t.Errorf("%d: %q %v", c.id, Name(c.id), ok)
			continue
		}
		c.fn(t, m)
	}
	if _, ok := Lookup(4000000); ok {
		t.Error("an id past the table has metadata")
	}
}

// The committed table holds the whole spell database, not a stub.
func TestTableIsWhole(t *testing.T) {
	Lookup(0)
	if n := len(metas); n < 20000 {
		t.Errorf("%d spells in meta.tsv.gz", n)
	}
}

// Frostbolt's ranks climb: the rank is the N of "Rank N", never the order of the ids.
func TestRanks(t *testing.T) {
	for id, rank := range map[uint32]uint8{116: 1, 205: 2, 837: 3, 7322: 4, 8406: 5, 10181: 10} {
		if m, ok := Lookup(id); !ok || m.Rank != rank || Name(id) != "Frostbolt" {
			t.Errorf("%d: rank %d (%v), want %d", id, m.Rank, ok, rank)
		}
	}
}

func TestFlagsOf(t *testing.T) {
	cases := []struct {
		name string
		in   FlagInput
		want Flags
	}{
		{"passive", FlagInput{Attributes: 0x40}, FlagPassive},
		{"channel that breaks on movement", FlagInput{AttributesEx: 0x40, ChannelInterruptFlags: 0x08}, FlagChanneled | FlagMoveInterrupts},
		{"damage aura", FlagInput{Effects: [3][2]int64{{6, 3}}}, FlagAura | FlagDamage},
		{"heal over time", FlagInput{Effects: [3][2]int64{{6, 8}}}, FlagAura | FlagHeal},
		{"normalized weapon damage", FlagInput{Effects: [3][2]int64{{121, 0}, {80, 0}}}, FlagDamage},
		{"a periodic aura on a non-aura effect is nothing", FlagInput{Effects: [3][2]int64{{3, 3}}}, 0},
		{"finisher", FlagInput{AttributesEx: 0x100000}, FlagComboPoints},
		{"stealth cooldown on fade", FlagInput{Attributes: 0x02000000}, FlagCooldownOnFade},
		{"dispel and resurrect", FlagInput{Effects: [3][2]int64{{38, 0}, {18, 0}}}, FlagDispel | FlagResurrect},
	}
	for _, c := range cases {
		if got := FlagsOf(c.in); got != c.want {
			t.Errorf("%s: %v, want %v", c.name, got.Names(), c.want.Names())
		}
	}
	if n := (FlagDamage | FlagPassive).Names(); len(n) != 2 || n[0] != "passive" || n[1] != "damage" {
		t.Errorf("names %v", n)
	}
}

func TestParseMeta(t *testing.T) {
	line := func(cols ...string) string {
		out := ""
		for i, c := range cols {
			if i > 0 {
				out += "\t"
			}
			out += c
		}
		return out
	}
	// id rank level base max power cost pct cast rmin rmax cd cat catcd gcdcat gcd school dispel mech dur flags e1 e2 e3 reagents
	row := line("7", "2", "8", "8", "", "0", "30", "", "2500", "5", "30", "6000", "12", "6000", "133", "1500", "2", "1", "", "-1", "4096", "2,,10,1,5,0.5,2", "6,3", "", "2447*2,118*1")
	var gz bytes.Buffer
	zw := gzip.NewWriter(&gz)
	zw.Write([]byte("# a comment\n" + row + "\n" + line("9", "", "", "", "", "", "", "", "", "", "", "", "", "", "", "", "", "", "", "", "", "", "", "", "") + "\n"))
	zw.Close()
	got, err := ParseMeta(&gz)
	if err != nil || len(got) != 2 {
		t.Fatal(err, got)
	}
	m := got[7]
	if m.Rank != 2 || m.Level != 8 || m.Cost != 30 || m.CastMS != 2500 || m.RangeMin != 5 || m.RangeMax != 30 || m.CooldownMS != 6000 || m.Category != 12 ||
		m.GCDMS != 1500 || m.School != 2 || m.Dispel != 1 || m.DurationMS != -1 || m.Flags != FlagDamage {
		t.Errorf("%+v", m)
	}
	if e := m.Effects[0]; e != (Effect{Effect: 2, Base: 10, Dice: 1, Sides: 5, PerLevel: 0.5, PerComboPoint: 2}) {
		t.Errorf("effect 1 %+v", e)
	}
	if e := m.Effects[1]; e.Effect != 6 || e.Aura != 3 {
		t.Errorf("effect 2 %+v", e)
	}
	if len(m.Reagents) != 2 || m.Reagents[0] != (Reagent{Item: 2447, Count: 2}) {
		t.Errorf("reagents %+v", m.Reagents)
	}
	for _, bad := range []string{"1\t2", row + "\textra", "x" + row[1:]} {
		var b bytes.Buffer
		w := gzip.NewWriter(&b)
		w.Write([]byte(bad + "\n"))
		w.Close()
		if _, err := ParseMeta(&b); err == nil {
			t.Errorf("accepted %q", bad)
		}
	}
}
