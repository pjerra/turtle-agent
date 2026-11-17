package session

import (
	"testing"
	"time"

	"github.com/TortoiseBots/turtle-agent/internal/events"
	"github.com/TortoiseBots/turtle-agent/internal/game"
	"github.com/TortoiseBots/turtle-agent/internal/game/gametest"
	"github.com/TortoiseBots/turtle-agent/internal/objects"
	"github.com/TortoiseBots/turtle-agent/internal/opcode"
	"github.com/TortoiseBots/turtle-agent/internal/packet"
)

func kindsOf(l *events.Log) map[string][]events.Event {
	m := map[string][]events.Event{}
	for _, e := range l.Read(events.Query{Limit: events.MaxLimit}).Events {
		m[e.Kind] = append(m[e.Kind], e)
	}
	return m
}

func TestCombatEventsNamesAndPartyFilter(t *testing.T) {
	h := start(t, Options{NamesWait: 100 * time.Millisecond})
	h.enterWorld()
	stranger := packet.GUID(0x40)
	h.send(opcode.SMSGGroupList, gametest.GroupList(me.GUID, gorm))
	wolfName(h)
	h.send(opcode.SMSGAttackerstateupdate, gametest.AttackerStateUpdate(game.HitInfoCritical, gorm.GUID, wolfGUID, 24, 0, 0, 0, 1, 0))
	h.send(opcode.SMSGSpellnonmeleedamagelog, gametest.SpellNonMeleeDamage(gorm.GUID, wolfGUID, 0, 5, 0, 0, 0, false, 0, 0))
	h.send(opcode.SMSGSpellnonmeleedamagelog, gametest.SpellNonMeleeDamage(wolfGUID, stranger, 133, 30, 2, 0, 0, false, 0, 0))
	h.send(opcode.SMSGPeriodicauralog, gametest.PeriodicDamage(wolfGUID, gorm.GUID, 589, game.AuraPeriodicDamage, 6, 5, 0, 0))
	h.send(opcode.SMSGSpellheallog, gametest.SpellHeal(gorm.GUID, me.GUID, 2061, 48, false))
	h.send(opcode.SMSGSpellenergizelog, gametest.SpellEnergize(gorm.GUID, gorm.GUID, 29166, 0, 400))
	h.send(opcode.SMSGSpellStart, gametest.SpellStart(me.GUID, 133, 3000, wolfGUID))
	h.send(opcode.SMSGSpellGo, gametest.SpellGo(me.GUID, 133, []packet.GUID{wolfGUID}, nil, nil, wolfGUID))
	h.send(opcode.SMSGCastResult, gametest.CastResult(133, 0x0B))
	h.send(opcode.SMSGSpellFailedOther, gametest.SpellFailedOther(gorm.GUID, 2061))
	h.send(opcode.SMSGSpelllogmiss, gametest.SpellLogMiss(133, me.GUID, wolfGUID, 2))
	h.send(opcode.SMSGPartykilllog, gametest.PartyKill(gorm.GUID, wolfGUID))
	time.Sleep(300 * time.Millisecond) // the stranger's name query is never answered
	h.logout()
	byKind := kindsOf(h.s.Log())
	m := byKind[events.KindMelee]
	if len(m) != 1 || m[0].Source != "Gorm" || m[0].Target != "Young Wolf" || m[0].Amount != 24 || !m[0].Crit || !m[0].Party || m[0].Data["outcome"] != "normal" {
		t.Fatalf("melee %+v", m)
	}
	sd := byKind[events.KindSpellDamage]
	if len(sd) != 2 || sd[0].Target != "Gorm" || !sd[0].Party || sd[1].SpellID != 133 || sd[1].Party || sd[1].Source != "" {
		t.Fatalf("spell damage %+v", sd)
	}
	if p := byKind[events.KindPeriodic]; len(p) != 1 || p[0].SpellID != 589 || p[0].Data["tick"] != "damage" || p[0].Source != "Gorm" {
		t.Fatalf("periodic %+v", p)
	}
	if hl := byKind[events.KindHeal]; len(hl) != 1 || hl[0].Source != "Harnessb" || hl[0].Target != "Gorm" || hl[0].Amount != 48 {
		t.Fatalf("heal %+v", hl)
	}
	if en := byKind[events.KindEnergize]; len(en) != 1 || en[0].Amount != 400 {
		t.Fatalf("energize %+v", en)
	}
	if ss := byKind[events.KindSpellStart]; len(ss) != 1 || ss[0].Target != "Young Wolf" || ss[0].Data["cast_ms"] != uint32(3000) {
		t.Fatalf("spell start %+v", ss)
	}
	if sg := byKind[events.KindSpellGo]; len(sg) != 1 || sg[0].Target != "Young Wolf" || sg[0].Data["hits"] != 1 {
		t.Fatalf("spell go %+v", sg)
	}
	if sf := byKind[events.KindSpellFail]; len(sf) != 2 || sf[0].Source != "Harnessb" || sf[0].Data["reason"] != uint8(0x0B) || sf[1].Data["reason"] != "interrupted" {
		t.Fatalf("spell fail %+v", sf)
	}
	if sm := byKind[events.KindSpellMiss]; len(sm) != 1 || sm[0].Data["miss"] != "resist" {
		t.Fatalf("spell miss %+v", sm)
	}
	if k := byKind[events.KindKill]; len(k) != 1 || k[0].Source != "Gorm" || k[0].Target != "Young Wolf" {
		t.Fatalf("kill %+v", k)
	}
	party := h.s.Log().Read(events.Query{Kinds: []string{events.KindSpellDamage}, PartyOnly: true}).Events
	if len(party) != 1 || party[0].Target != "Gorm" {
		t.Fatalf("party filter %+v", party)
	}
}

// TestProcResistAndPowerTicks covers the Task 26 consumer ruling: a resisted proc reads as
// a spell miss, and energize and leech ticks name their power type.
func TestProcResistAndPowerTicks(t *testing.T) {
	h := start(t, Options{NamesWait: 100 * time.Millisecond})
	h.enterWorld()
	h.send(opcode.SMSGGroupList, gametest.GroupList(me.GUID, gorm))
	wolfName(h)
	h.send(opcode.SMSGProcresist, gametest.ProcResist(me.GUID, wolfGUID, 20007))
	h.send(opcode.SMSGPeriodicauralog, gametest.PeriodicEnergize(gorm.GUID, gorm.GUID, 25, game.AuraPeriodicEnergize, 3, 30))
	h.send(opcode.SMSGPeriodicauralog, gametest.PeriodicLeech(gorm.GUID, wolfGUID, 5138, 0, 42, 1.5))
	h.logout()
	byKind := kindsOf(h.s.Log())
	sm := byKind[events.KindSpellMiss]
	if len(sm) != 1 || sm[0].Source != "Harnessb" || sm[0].Target != "Young Wolf" || sm[0].SpellID != 20007 ||
		sm[0].Data["miss"] != "resist" || sm[0].Data["proc"] != true || !sm[0].Party {
		t.Fatalf("proc resist %+v", sm)
	}
	p := byKind[events.KindPeriodic]
	if len(p) != 2 || p[0].Data["tick"] != "energize" || p[0].Data["power"] != uint32(3) || p[0].Amount != 30 ||
		p[1].Data["tick"] != "leech" || p[1].Data["power"] != uint32(0) || p[1].Source != "Young Wolf" || p[1].Amount != 42 {
		t.Fatalf("power ticks %+v", p)
	}
}

// TestPetEventsArePartyEvents: a hunter pet's swings and the hits it takes carry the party
// flag through UNIT_FIELD_CREATEDBY; a stranger's pet stays out.
func TestPetEventsArePartyEvents(t *testing.T) {
	h := start(t, Options{NamesWait: 100 * time.Millisecond})
	h.enterWorld()
	wolfName(h)
	const pet, strangerPet = packet.GUID(0xF140000001000001), packet.GUID(0xF140000002000002)
	owned := func(owner packet.GUID) map[uint16]uint32 {
		return map[uint16]uint32{objects.UnitHealth: 100, objects.UnitMaxHealth: 100, objects.UnitCreatedBy: uint32(owner), objects.UnitCreatedBy + 1: uint32(owner >> 32)}
	}
	h.send(opcode.SMSGUpdateObject, unitCreate(pet, objects.TypeUnit, me.X+2, me.Y, me.Z, owned(me.GUID)))
	h.send(opcode.SMSGUpdateObject, unitCreate(strangerPet, objects.TypeUnit, me.X+4, me.Y, me.Z, owned(0x40)))
	h.send(opcode.SMSGAttackerstateupdate, gametest.AttackerStateUpdate(0, pet, wolfGUID, 12, 0, 0, 0, 1, 0))
	h.send(opcode.SMSGAttackerstateupdate, gametest.AttackerStateUpdate(0, wolfGUID, pet, 7, 0, 0, 0, 1, 0))
	h.send(opcode.SMSGAttackerstateupdate, gametest.AttackerStateUpdate(0, strangerPet, wolfGUID, 9, 0, 0, 0, 1, 0))
	h.logout()
	m := kindsOf(h.s.Log())[events.KindMelee]
	if len(m) != 3 || !m[0].Party || m[0].Amount != 12 || !m[1].Party || m[1].Amount != 7 || m[2].Party {
		t.Fatalf("pet melee %+v", m)
	}
}
