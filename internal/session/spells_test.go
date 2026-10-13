package session

import (
	"testing"
	"time"

	"github.com/pjerra/turtle-agent/internal/capture"
	"github.com/pjerra/turtle-agent/internal/events"
	"github.com/pjerra/turtle-agent/internal/game"
	"github.com/pjerra/turtle-agent/internal/game/gametest"
	"github.com/pjerra/turtle-agent/internal/objects"
	"github.com/pjerra/turtle-agent/internal/opcode"
	"github.com/pjerra/turtle-agent/internal/packet"
	"github.com/pjerra/turtle-agent/internal/testfixture"
)

const (
	fireBlast  = 2136 // 8 s category 19, GCD 1.5 s, 40 mana
	fireball   = 133
	heroic     = 78 // 15 rage, no GCD
	bloodrageS = 2687
)

// spellRow finds one spell of the spells result.
func spellRow(t *testing.T, h *harness, id uint32) SpellRow {
	t.Helper()
	rows, err := h.s.Spells(SpellsFilter{SpellID: id})
	if err != nil || len(rows) != 1 {
		t.Fatalf("spell %d: %d rows, %v", id, len(rows), err)
	}
	return rows[0]
}

// The login of the m910q GM warrior (testdata/captures/m1-login.twcap) gives the spellbook:
// Heroic Strike is rank 1, costs 15 rage (150 in tenths) and is ready, and "affordable" follows the
// rage the player holds.
func TestSpellbookFromRealLoginPacket(t *testing.T) {
	bodies := capture.Bodies(testfixture.Load(t, "m1-login.twcap"), opcode.SMSGInitialSpells)
	h := start(t, Options{NamesWait: 50 * time.Millisecond})
	h.enterWorld()
	h.send(opcode.SMSGUpdateObject, selfCreate(7))
	h.send(opcode.SMSGInitialSpells, bodies[0])
	waitFor(t, func() bool { return h.s.SpellState().Loaded() })
	rows, err := h.s.Spells(SpellsFilter{Name: "heroic"})
	if err != nil || len(rows) != 1 {
		t.Fatalf("%v %v", rows, err)
	}
	hs := rows[0]
	if hs.SpellID != heroic || hs.Name != "Heroic Strike" || hs.Rank != 1 || !hs.Highest || !hs.Meta || hs.Power != "rage" || hs.Cost != 150 ||
		!hs.Ready || hs.RemainingMS != 0 || hs.GCDMS != 0 || hs.RangeMax != 5 || hs.Affordable == nil || *hs.Affordable {
		t.Fatalf("%+v", hs)
	}
	h.send(opcode.SMSGUpdateObject, selfValues(map[uint16]uint32{objects.UnitPower1 + 1: 200}))
	waitFor(t, func() bool { r := spellRow(t, h, heroic); return r.Affordable != nil && *r.Affordable })

	// Passive spells (proficiencies, Block, Dodge) are listed only on request; a ranged name filter finds none.
	all, _ := h.s.Spells(SpellsFilter{})
	withPassive, _ := h.s.Spells(SpellsFilter{Passive: true})
	if len(all) == 0 || len(withPassive) <= len(all) {
		t.Errorf("%d spells, %d with passives", len(all), len(withPassive))
	}
	if r, _ := h.s.Spells(SpellsFilter{Name: "no such spell"}); len(r) != 0 {
		t.Errorf("%+v", r)
	}
	if _, ok := h.s.SpellState().Highest("battle stance"); !ok {
		t.Error("Battle Stance is in the spellbook")
	}
	h.logout()
}

func TestLearnSupersedeAndRemoveSpells(t *testing.T) {
	h := start(t, Options{NamesWait: 50 * time.Millisecond})
	h.enterWorld()
	h.send(opcode.SMSGInitialSpells, gametest.InitialSpells([]uint32{116}, nil))
	h.send(opcode.SMSGLearnedSpell, gametest.LearnedSpell(205))
	h.send(opcode.SMSGSupercededSpell, gametest.SupercededSpell(116, 205))
	h.send(opcode.SMSGLearnedSpell, gametest.LearnedSpell(837))
	h.send(opcode.SMSGSupercededSpell, gametest.SupercededSpell(205, 837))
	waitFor(t, func() bool { return len(h.s.SpellState().Ranks("Frostbolt")) == 1 && h.s.SpellState().Known(837) })
	rows, _ := h.s.Spells(SpellsFilter{Name: "frostbolt"})
	if len(rows) != 1 || rows[0].SpellID != 837 || rows[0].Rank != 3 || !rows[0].Highest || rows[0].CastMS != 2200 || rows[0].Cost != 50 {
		t.Fatalf("%+v", rows)
	}
	h.send(opcode.SMSGRemovedSpell, gametest.RemovedSpell(837))
	waitFor(t, func() bool { return len(ofKind(h.s.Log(), events.KindSpellRemoved)) == 1 })
	if _, ok := h.s.SpellState().Highest("Frostbolt"); ok {
		t.Error("a removed spell is still known")
	}
	sup := ofKind(h.s.Log(), events.KindSpellSuperseded)
	if len(sup) != 2 || sup[0].SpellID != 116 || sup[0].Data["by"] != uint32(205) || sup[1].SpellID != 205 {
		t.Fatalf("%+v", sup)
	}
	if l := ofKind(h.s.Log(), events.KindSpellLearned); len(l) != 2 {
		t.Fatalf("%+v", l)
	}
	h.logout()
}

// A cast, as the server shows it to its caster: SMSG_SPELL_START (the global cooldown starts), SMSG_SPELL_GO
// (the cooldown starts), and later a refusal.
func TestCastCooldownsAndFailures(t *testing.T) {
	h := start(t, Options{NamesWait: 50 * time.Millisecond})
	h.enterWorld()
	h.send(opcode.SMSGInitialSpells, gametest.InitialSpells([]uint32{fireBlast, fireball, heroic}, nil))
	waitFor(t, func() bool { return h.s.SpellState().Loaded() })
	if cs, err := h.s.CastState(); err != nil || cs.Cast.State != "idle" || cs.GCD.Active {
		t.Fatalf("%+v %v", cs, err)
	}

	// Someone else's cast changes nothing.
	h.send(opcode.SMSGSpellStart, gametest.SpellStart(gorm.GUID, fireball, 3000, wolfGUID))
	h.send(opcode.SMSGSpellGo, gametest.SpellGo(gorm.GUID, fireBlast, nil, nil, nil, wolfGUID))
	h.send(opcode.SMSGSpellFailedOther, gametest.SpellFailedOther(gorm.GUID, fireball))

	h.send(opcode.SMSGSpellStart, gametest.SpellStart(me.GUID, fireBlast, 0, wolfGUID))
	waitFor(t, func() bool { return h.s.Status().GCDMS > 0 })
	if g := h.s.Status().GCDMS; g > 1500 {
		t.Errorf("GCD %d ms", g)
	}
	if r := spellRow(t, h, fireball); r.Ready || r.Why != "gcd" || r.RemainingMS <= 0 || r.RemainingMS > 1500 {
		t.Errorf("Fireball during the GCD: %+v", r)
	}
	if r := spellRow(t, h, heroic); !r.Ready {
		t.Errorf("Heroic Strike ignores the GCD: %+v", r)
	}
	h.send(opcode.SMSGSpellGo, gametest.SpellGo(me.GUID, fireBlast, []packet.GUID{wolfGUID}, nil, nil, wolfGUID))
	waitFor(t, func() bool { return len(ofKind(h.s.Log(), events.KindCooldownStart)) == 1 })
	cd := ofKind(h.s.Log(), events.KindCooldownStart)[0]
	if cd.SpellID != fireBlast || cd.Data["ms"] != uint32(8000) || cd.Data["category"] != uint32(19) || cd.Data["source"] != "cast" || cd.SourceGUID != me.GUID {
		t.Fatalf("%+v", cd)
	}
	fb := spellRow(t, h, fireBlast)
	if fb.Ready || fb.Why != "cooldown" || fb.RemainingMS < 7000 || fb.RemainingMS > 8000 || fb.CooldownMS != 0 || fb.CategoryCDMS != 8000 {
		t.Errorf("%+v", fb)
	}
	cs, _ := h.s.CastState()
	if cs.LastCast == nil || cs.LastCast.SpellID != fireBlast || cs.Cast.State != "idle" {
		t.Errorf("%+v", cs)
	}

	// A cast with a cast time is "casting" until its go, and pushback moves its end.
	h.send(opcode.SMSGSpellStart, gametest.SpellStart(me.GUID, fireball, 3000, wolfGUID))
	h.send(opcode.SMSGSpellDelayed, packet.NewWriter().GUID(me.GUID).U32(500).Bytes())
	waitFor(t, func() bool { cs, _ := h.s.CastState(); return cs.Cast.State == "casting" && *cs.Cast.EndsInMS > 3000 })
	cs, _ = h.s.CastState()
	if cs.Cast.SpellID != fireball || cs.Cast.Name != "Fireball" || cs.Cast.Target != wolfGUID {
		t.Errorf("%+v", cs.Cast)
	}

	// The server breaks it with an interrupt: the GCD it started is given back.
	h.send(opcode.SMSGSpellFailedOther, gametest.SpellFailedOther(me.GUID, fireball))
	waitFor(t, func() bool { cs, _ := h.s.CastState(); return cs.Cast.State == "idle" && cs.LastFailure != nil })
	cs, _ = h.s.CastState()
	if cs.LastFailure.SpellID != fireball || cs.LastFailure.ReasonName != "interrupted" || cs.GCD.Active {
		t.Errorf("%+v", cs)
	}

	// A refusal carries SpellCastResult; both its number and its name reach the event.
	h.send(opcode.SMSGCastResult, gametest.CastResult(fireball, 89))
	waitFor(t, func() bool { return len(ofKind(h.s.Log(), events.KindSpellFail)) == 3 })
	fails := ofKind(h.s.Log(), events.KindSpellFail)
	if f := fails[2]; f.SpellID != fireball || f.Data["reason"] != uint8(89) || f.Data["reason_name"] != "out_of_range" {
		t.Errorf("%+v", f)
	}
	if f := fails[1]; f.Data["reason"] != "interrupted" || f.Data["reason_name"] != "interrupted" {
		t.Errorf("%+v", f)
	}
	cs, _ = h.s.CastState()
	if cs.LastFailure.ReasonName != "out_of_range" || cs.Failures != 2 { // our interrupt and this refusal; the other caster's is not ours
		t.Errorf("%+v", cs)
	}
	// "Not ready" from the server holds the spell back although the numbers say it is ready.
	h.send(opcode.SMSGCastResult, gametest.CastResult(heroic, 60))
	waitFor(t, func() bool { return !spellRow(t, h, heroic).Ready })
	if r := spellRow(t, h, heroic); r.Why != "server_refused" || r.RemainingMS <= 0 || r.RemainingMS > 1000 {
		t.Errorf("%+v", r)
	}

	// SMSG_CLEAR_COOLDOWN (a GM's .cooldown, a reset ability) ends the cooldown.
	h.send(opcode.SMSGClearCooldown, gametest.CooldownEvent(fireBlast, me.GUID))
	waitFor(t, func() bool { return spellRow(t, h, fireBlast).Ready })
	if c := ofKind(h.s.Log(), events.KindCooldownClear); len(c) != 1 || c[0].SpellID != fireBlast {
		t.Errorf("%+v", c)
	}
	h.logout()
}

// A cooldown the server sets (a school lockout) ends with a cooldown_end event; one for a pet is not ours.
func TestServerCooldownsEndWithAnEvent(t *testing.T) {
	h := start(t, Options{NamesWait: 50 * time.Millisecond})
	h.enterWorld()
	h.send(opcode.SMSGInitialSpells, gametest.InitialSpells([]uint32{fireball, bloodrageS}, nil))
	h.send(opcode.SMSGSpellCooldown, gametest.SpellCooldown(0xF14000000000AAAA, game.SpellCooldown{Spell: fireball, MS: 60000}))
	h.send(opcode.SMSGSpellCooldown, gametest.SpellCooldown(me.GUID, game.SpellCooldown{Spell: fireball, MS: 250}))
	waitFor(t, func() bool { return len(ofKind(h.s.Log(), events.KindCooldownStart)) == 1 })
	if c := ofKind(h.s.Log(), events.KindCooldownStart)[0]; c.SpellID != fireball || c.Data["ms"] != uint32(250) || c.Data["source"] != "server" {
		t.Fatalf("%+v", c)
	}
	if r := spellRow(t, h, fireball); r.Ready || r.Why != "cooldown" {
		t.Errorf("%+v", r)
	}
	waitFor(t, func() bool { return len(ofKind(h.s.Log(), events.KindCooldownEnd)) == 1 })
	if c := ofKind(h.s.Log(), events.KindCooldownEnd)[0]; c.SpellID != fireball || c.SourceGUID != me.GUID {
		t.Errorf("%+v", c)
	}
	if !spellRow(t, h, fireball).Ready {
		t.Error("the lockout ended but the spell is not ready")
	}

	// SMSG_COOLDOWN_EVENT starts the cooldown from the spell's data, for the player only.
	h.send(opcode.SMSGCooldownEvent, gametest.CooldownEvent(bloodrageS, 0xF14000000000AAAA))
	h.send(opcode.SMSGCooldownEvent, gametest.CooldownEvent(bloodrageS, me.GUID))
	waitFor(t, func() bool { return len(ofKind(h.s.Log(), events.KindCooldownStart)) == 2 })
	if c := ofKind(h.s.Log(), events.KindCooldownStart)[1]; c.SpellID != bloodrageS || c.Data["ms"] != uint32(60000) || c.Data["source"] != "event" {
		t.Errorf("%+v", c)
	}
	// SMSG_COOLDOWN_CHEAT clears everything.
	h.send(opcode.SMSGCooldownCheat, gametest.CooldownCheat(me.GUID))
	waitFor(t, func() bool { return spellRow(t, h, bloodrageS).Ready })
	if c := ofKind(h.s.Log(), events.KindCooldownClear); len(c) != 1 || c[0].Data["all"] != true || c[0].SpellID != 0 {
		t.Errorf("%+v", c)
	}
	h.logout()
}

func TestChannelState(t *testing.T) {
	h := start(t, Options{NamesWait: 50 * time.Millisecond})
	h.enterWorld()
	h.send(opcode.MSGChannelStart, gametest.ChannelStart(5143, 3000))
	waitFor(t, func() bool { cs, _ := h.s.CastState(); return cs.Cast.State == "channelling" })
	if cs, _ := h.s.CastState(); cs.Cast.SpellID != 5143 || cs.Cast.CastMS != 3000 {
		t.Errorf("%+v", cs.Cast)
	}
	h.send(opcode.MSGChannelUpdate, gametest.ChannelUpdate(0))
	waitFor(t, func() bool { cs, _ := h.s.CastState(); return cs.Cast.State == "idle" })
	st, en := ofKind(h.s.Log(), events.KindChannelStart), ofKind(h.s.Log(), events.KindChannelEnd)
	if len(st) != 1 || st[0].SpellID != 5143 || st[0].Data["ms"] != uint32(3000) || len(en) != 1 || en[0].SpellID != 5143 {
		t.Errorf("%+v %+v", st, en)
	}
	h.logout()
}

// Combo points are byte 1 of PLAYER_FIELD_BYTES and the guid in PLAYER_FIELD_COMBO_TARGET, on the
// player's own object (Player.cpp:20942-20954).
func TestComboPoints(t *testing.T) {
	h := start(t, Options{NamesWait: 50 * time.Millisecond})
	h.enterWorld()
	h.send(opcode.SMSGUpdateObject, selfCreate(7))
	waitFor(t, func() bool { return h.s.Status().MaxHealth == 100 })
	if st := h.s.Status(); st.ComboPoints != 0 || st.ComboTarget != 0 {
		t.Fatalf("%+v", st)
	}
	g := uint64(wolfGUID)
	h.send(opcode.SMSGUpdateObject, selfValues(map[uint16]uint32{objects.PlayerFieldBytes: 3 << 8, objects.PlayerComboTarget: uint32(g), objects.PlayerComboTarget + 1: uint32(g >> 32)}))
	waitFor(t, func() bool { return h.s.Status().ComboPoints == 3 })
	if st := h.s.Status(); st.ComboTarget != wolfGUID {
		t.Errorf("%+v", st)
	}
	if pts, target := h.s.ComboPoints(); pts != 3 || target != wolfGUID {
		t.Errorf("%d %v", pts, target)
	}
	u, err := h.s.Unit(me.GUID, "")
	if err != nil || u.ComboPoints != 3 || u.ComboTarget != wolfGUID {
		t.Errorf("%+v %v", u, err)
	}
	if cs, _ := h.s.CastState(); cs.ComboPoints != 3 || cs.ComboTarget != wolfGUID {
		t.Errorf("%+v", cs)
	}
	// An honor-rank byte next to the points does not read as a combo point.
	h.send(opcode.SMSGUpdateObject, selfValues(map[uint16]uint32{objects.PlayerFieldBytes: 5<<24 | 4<<8}))
	waitFor(t, func() bool { return h.s.Status().ComboPoints == 4 })
	// A finisher spends them: the points go, the target field keeps its stale guid, and the agent leaves it out.
	h.send(opcode.SMSGUpdateObject, selfValues(map[uint16]uint32{objects.PlayerFieldBytes: 0}))
	waitFor(t, func() bool { return h.s.Status().ComboPoints == 0 })
	if st := h.s.Status(); st.ComboTarget != 0 {
		t.Errorf("%+v", st)
	}
	waitFor(t, func() bool { return len(ofKind(h.s.Log(), events.KindComboPoints)) == 3 }) // the first waits for the target's name
	ev := ofKind(h.s.Log(), events.KindComboPoints)
	if len(ev) != 3 || ev[0].Data["points"] != uint32(3) || ev[0].Data["was"] != uint32(0) || ev[0].TargetGUID != wolfGUID ||
		ev[1].Data["points"] != uint32(4) || ev[2].Data["points"] != uint32(0) || ev[2].Data["was"] != uint32(4) {
		t.Errorf("%+v", ev)
	}
	h.logout()
}

func TestSpellMethodsNeedTheWorld(t *testing.T) {
	h := start(t, Options{NamesWait: 50 * time.Millisecond})
	if _, err := h.s.Spells(SpellsFilter{}); err == nil {
		t.Error("spells answered before login")
	}
	if _, err := h.s.CastState(); err == nil {
		t.Error("cast_state answered before login")
	}
	h.enterWorld()
	if rows, err := h.s.Spells(SpellsFilter{}); err != nil || rows == nil || len(rows) != 0 {
		t.Errorf("an empty spellbook must be an empty list: %#v %v", rows, err)
	}
	h.logout()
}

// A fight's worth of the real server's casts (testdata/captures/m6-fight.twcap) flows through the
// state without a parse error, and its refusals reach cast_state with a name.
func TestSpellStateFromCapturedFight(t *testing.T) {
	recs := testfixture.Load(t, "m5-attack.twcap")
	log, rep, err := Replay(recs, Options{NamesWait: 50 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	if rep.Handled == 0 {
		t.Fatal("nothing was handled")
	}
	fails := ofKind(log, events.KindSpellFail)
	var refused int
	for _, f := range fails {
		if f.Data["reason"] == uint8(25) && f.Data["reason_name"] == "equipped_item_class" && f.SpellID == heroic {
			refused++
		}
	}
	if refused != 2 {
		t.Errorf("%d refusals in %+v", refused, fails)
	}
	if n := len(ofKind(log, events.KindParseError)); n != 0 {
		t.Errorf("%d parse errors", n)
	}
}

// Sending a spell again while it casts is refused as spell_in_progress; the cast and its GCD go on.
func TestResendingACastKeepsTheRunningOne(t *testing.T) {
	h := start(t, Options{NamesWait: 50 * time.Millisecond})
	h.enterWorld()
	h.send(opcode.SMSGInitialSpells, gametest.InitialSpells([]uint32{fireball, fireBlast}, nil))
	h.send(opcode.SMSGSpellStart, gametest.SpellStart(me.GUID, fireball, 3000, wolfGUID))
	waitFor(t, func() bool { cs, _ := h.s.CastState(); return cs.Cast.State == "casting" })
	h.send(opcode.SMSGCastResult, gametest.CastResult(fireball, int(game.CastFailedInProgress)))
	waitFor(t, func() bool { cs, _ := h.s.CastState(); return cs.LastFailure != nil })
	cs, _ := h.s.CastState()
	if cs.Cast.State != "casting" || !cs.GCD.Active || cs.LastFailure.ReasonName != "spell_in_progress" {
		t.Fatalf("%+v", cs)
	}
	if r := spellRow(t, h, fireBlast); r.Ready || r.Why != "gcd" {
		t.Errorf("%+v", r)
	}
	// Only the interrupt broadcast ends it.
	h.send(opcode.SMSGSpellFailedOther, gametest.SpellFailedOther(me.GUID, fireball))
	waitFor(t, func() bool { cs, _ := h.s.CastState(); return cs.Cast.State == "idle" })
	h.logout()
}
