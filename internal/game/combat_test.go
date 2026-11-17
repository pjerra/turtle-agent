package game_test

import (
	"testing"

	"github.com/TortoiseBots/turtle-agent/internal/game"
	"github.com/TortoiseBots/turtle-agent/internal/game/gametest"
	"github.com/TortoiseBots/turtle-agent/internal/packet"
)

const (
	gorm = packet.GUID(0x30)
	wolf = packet.GUID(0xF13000012B000001)
)

func TestMelee(t *testing.T) {
	m, err := game.ParseAttackerStateUpdate(gametest.AttackerStateUpdate(game.HitInfoCritical|0x02, gorm, wolf, 24, 0, 3, 0, 1, 0))
	if err != nil || m.Attacker != gorm || m.Victim != wolf || m.Total != 24 || !m.Crit() || m.Miss() || len(m.Subs) != 1 || m.Subs[0].Absorb != 3 || m.Outcome() != "normal" {
		t.Fatalf("%+v %v", m, err)
	}
	m, _ = game.ParseAttackerStateUpdate(gametest.AttackerStateUpdate(game.HitInfoMiss, gorm, wolf, 0, 0, 0, 0, 0, 0))
	if !m.Miss() || m.Outcome() != "miss" {
		t.Fatalf("%+v", m)
	}
	m, _ = game.ParseAttackerStateUpdate(gametest.AttackerStateUpdate(0, wolf, gorm, 0, 0, 0, 0, 2, 0))
	if m.Outcome() != "dodge" {
		t.Fatalf("%+v", m)
	}
}

func TestSpellDamageHealEnergize(t *testing.T) {
	d, err := game.ParseSpellNonMeleeDamageLog(gametest.SpellNonMeleeDamage(wolf, gorm, 133, 36, 2, 0, 0, false, 0, game.SpellHitCrit))
	if err != nil || d.SpellID != 133 || d.Damage != 36 || d.School != 2 || !d.Crit() || d.Target != wolf {
		t.Fatalf("%+v %v", d, err)
	}
	h, err := game.ParseSpellHealLog(gametest.SpellHeal(gorm, gorm, 2061, 48, true))
	if err != nil || h.Amount != 48 || !h.Crit || h.Caster != gorm {
		t.Fatalf("%+v %v", h, err)
	}
	e, err := game.ParseSpellEnergizeLog(gametest.SpellEnergize(gorm, gorm, 29166, 0, 400))
	if err != nil || e.Amount != 400 || e.Power != 0 {
		t.Fatalf("%+v %v", e, err)
	}
}

func TestPeriodicKinds(t *testing.T) {
	for _, c := range []struct {
		body []byte
		kind string
		amt  uint32
	}{
		{gametest.PeriodicDamage(wolf, gorm, 589, game.AuraPeriodicDamage, 5, 5, 0, 0), "damage", 5},
		{gametest.PeriodicDamage(wolf, gorm, 589, game.AuraPeriodicDamagePercent, 7, 0, 0, 0), "damage", 7},
		{gametest.PeriodicHeal(gorm, gorm, 139, game.AuraPeriodicHeal, 20), "heal", 20},
		{gametest.PeriodicEnergize(gorm, gorm, 25, game.AuraPeriodicEnergize, 0, 30), "energize", 30},
	} {
		p, err := game.ParsePeriodicAuraLog(c.body)
		if err != nil || len(p.Ticks) != 1 || p.Ticks[0].Kind() != c.kind || p.Ticks[0].Amount != c.amt {
			t.Fatalf("%s: %+v %v", c.kind, p, err)
		}
	}
	leech := gametest.PeriodicLeech(wolf, gorm, 5138, 0, 42, 1.5)
	p, err := game.ParsePeriodicAuraLog(leech)
	if err != nil || len(p.Ticks) != 1 || p.Ticks[0].Kind() != "leech" || p.Ticks[0].Amount != 42 || p.Ticks[0].Multiplier != 1.5 {
		t.Fatalf("leech: %+v %v", p, err)
	}
	if _, err := game.ParsePeriodicAuraLog(leech[:len(leech)-1]); err == nil {
		t.Fatal("leech tick without its multiplier accepted")
	}
	bad := packet.NewWriter().PackedGUID(wolf).PackedGUID(gorm).U32(1).U32(1).U32(99).Bytes()
	if _, err := game.ParsePeriodicAuraLog(bad); err == nil {
		t.Fatal("unknown aura type accepted")
	}
}

func TestSpellStartGoAndMisses(t *testing.T) {
	s, err := game.ParseSpellStart(gametest.SpellStart(gorm, 133, 3000, wolf))
	if err != nil || s.Caster != gorm || s.SpellID != 133 || s.CastTime != 3000 || s.Targets.Unit != wolf {
		t.Fatalf("%+v %v", s, err)
	}
	g, err := game.ParseSpellGo(gametest.SpellGo(gorm, 133, []packet.GUID{wolf}, []packet.GUID{0xF130000000000099}, []uint8{game.SpellMissReflect}, wolf))
	if err != nil || len(g.Hits) != 1 || g.Hits[0] != wolf || len(g.Misses) != 1 || g.Misses[0].Reason != 11 || g.Targets.Unit != wolf {
		t.Fatalf("%+v %v", g, err)
	}
	m, err := game.ParseSpellLogMiss(gametest.SpellLogMiss(133, gorm, wolf, 2))
	if err != nil || m.Caster != gorm || len(m.Misses) != 1 || game.SpellMissName(m.Misses[0].Reason) != "resist" {
		t.Fatalf("%+v %v", m, err)
	}
}

func TestCastResultFailedOtherAndKill(t *testing.T) {
	r, err := game.ParseCastResult(gametest.CastResult(133, -1))
	if err != nil || r.Failed {
		t.Fatalf("%+v %v", r, err)
	}
	r, err = game.ParseCastResult(gametest.CastResult(133, 0x0B))
	if err != nil || !r.Failed || r.Reason != 0x0B {
		t.Fatalf("%+v %v", r, err)
	}
	o, err := game.ParseSpellFailedOther(gametest.SpellFailedOther(gorm, 133))
	if err != nil || o.Caster != gorm || o.SpellID != 133 {
		t.Fatalf("%+v %v", o, err)
	}
	k, err := game.ParsePartyKillLog(gametest.PartyKill(gorm, wolf))
	if err != nil || k != (game.Kill{Killer: gorm, Victim: wolf}) {
		t.Fatalf("%+v %v", k, err)
	}
}

// A ranged cast at a location: every SpellCastTargets::write branch (Spell.cpp:228-273), then the
// ammo pair CAST_FLAG_AMMO adds (Spell.cpp:4643-4644, :4684-4685, :4754-4755).
func TestSpellTargetsAndAmmo(t *testing.T) {
	const item = packet.GUID(0x4000000000000123)
	mask := game.TargetFlagUnit | game.TargetFlagItem | game.TargetFlagSourceLocation | game.TargetFlagDestLocation | game.TargetFlagString
	head := packet.NewWriter().PackedGUID(gorm).PackedGUID(gorm).U32(75).U16(0x0002 | game.CastFlagAmmo).U32(0).
		U16(mask).PackedGUID(wolf).PackedGUID(item).F32(1).F32(2).F32(3).F32(4).F32(5).F32(6).CString("x")
	s, err := game.ParseSpellStart(append(append([]byte(nil), head.Bytes()...), packet.NewWriter().U32(5996).U32(24).Bytes()...))
	want := game.SpellTargets{Mask: mask, Unit: wolf, Item: item, Src: [3]float32{1, 2, 3}, Dst: [3]float32{4, 5, 6}, Str: "x"}
	if err != nil || s.Targets != want {
		t.Fatalf("%+v %v", s, err)
	}
	if _, err := game.ParseSpellStart(head.Bytes()); err == nil {
		t.Fatal("SMSG_SPELL_START with CAST_FLAG_AMMO but no ammo accepted")
	}
	g := packet.NewWriter().PackedGUID(gorm).PackedGUID(gorm).U32(75).U16(0x0100 | game.CastFlagAmmo).U8(0).U8(0).U16(game.TargetFlagUnit).PackedGUID(wolf)
	if _, err := game.ParseSpellGo(g.Bytes()); err == nil {
		t.Fatal("SMSG_SPELL_GO with CAST_FLAG_AMMO but no ammo accepted")
	}
	if sg, err := game.ParseSpellGo(g.U32(5996).U32(24).Bytes()); err != nil || sg.Targets.Unit != wolf {
		t.Fatalf("%+v %v", sg, err)
	}
}

func TestProcResist(t *testing.T) {
	p, err := game.ParseProcResist(gametest.ProcResist(gorm, wolf, 16257))
	if err != nil || p != (game.ProcResist{Caster: gorm, Target: wolf, SpellID: 16257}) {
		t.Fatalf("%+v %v", p, err)
	}
}

// SMSG_SPELLLOGMISS's count is uncapped in the core (Spell.cpp:4956): a real long list parses, a
// count the remaining bytes cannot hold is rejected before the loop.
func TestSpellLogMissCountBoundedByBytes(t *testing.T) {
	w := packet.NewWriter().U32(133).GUID(gorm).U8(0).U32(100)
	for i := 0; i < 100; i++ {
		w.GUID(wolf + packet.GUID(i)).U8(2)
	}
	if m, err := game.ParseSpellLogMiss(w.Bytes()); err != nil || len(m.Misses) != 100 || m.Misses[99].Target != wolf+99 {
		t.Fatalf("100 targets: %d %v", len(m.Misses), err)
	}
	hostile := packet.NewWriter().U32(133).GUID(gorm).U8(0).U32(0xFFFFFFFF).GUID(wolf).U8(2).Bytes()
	if _, err := game.ParseSpellLogMiss(hostile); err == nil {
		t.Fatal("count 0xFFFFFFFF in 9 bytes accepted")
	}
	extra := packet.NewWriter().U32(133).GUID(gorm).U8(1).U32(2).GUID(wolf).U8(2).F32(0).F32(0).GUID(wolf).U8(2).Bytes()
	if _, err := game.ParseSpellLogMiss(extra); err == nil {
		t.Fatal("two extra-float targets in 26 bytes accepted")
	}
}
