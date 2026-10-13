package session

import (
	"fmt"

	"github.com/pjerra/turtle-agent/internal/events"
	"github.com/pjerra/turtle-agent/internal/game"
	"github.com/pjerra/turtle-agent/internal/opcode"
	"github.com/pjerra/turtle-agent/internal/packet"
)

func (s *Session) registerCombat() {
	s.handlers[opcode.SMSGAttackerstateupdate] = s.onMelee
	s.handlers[opcode.SMSGSpellnonmeleedamagelog] = s.onSpellDamage
	s.handlers[opcode.SMSGPeriodicauralog] = s.onPeriodic
	s.handlers[opcode.SMSGSpellheallog] = s.onHeal
	s.handlers[opcode.SMSGSpellenergizelog] = s.onEnergize
	s.handlers[opcode.SMSGSpellStart] = s.onSpellStart
	s.handlers[opcode.SMSGSpellGo] = s.onSpellGo
	s.handlers[opcode.SMSGCastResult] = s.onCastResult
	s.handlers[opcode.SMSGSpellFailedOther] = s.onSpellFailedOther
	s.handlers[opcode.SMSGSpelllogmiss] = s.onSpellLogMiss
	s.handlers[opcode.SMSGProcresist] = s.onProcResist
	s.handlers[opcode.SMSGPartykilllog] = s.onKill
	s.handlers[opcode.SMSGSpellDelayed] = s.onSpellDelayed
	s.handlers[opcode.SMSGUpdateAuraDuration] = s.onAuraDuration
}

func (s *Session) onMelee(body []byte) error {
	m, err := game.ParseAttackerStateUpdate(body)
	if err != nil {
		return err
	}
	var absorb uint32
	var resist int32
	for _, sub := range m.Subs {
		absorb += sub.Absorb
		resist += sub.Resist
	}
	s.emit(events.Event{Kind: events.KindMelee, SourceGUID: m.Attacker, TargetGUID: m.Victim, Amount: int64(m.Total), Crit: m.Crit(),
		Data: map[string]any{"outcome": m.Outcome(), "hit_info": m.HitInfo, "absorb": absorb, "resist": resist, "blocked": m.Blocked}})
	return nil
}

func (s *Session) onSpellDamage(body []byte) error {
	d, err := game.ParseSpellNonMeleeDamageLog(body)
	if err != nil {
		return err
	}
	s.emit(events.Event{Kind: events.KindSpellDamage, SourceGUID: d.Attacker, TargetGUID: d.Target, SpellID: d.SpellID,
		Amount: int64(d.Damage), Crit: d.Crit(),
		Data: map[string]any{"school": d.School, "absorb": d.Absorb, "resist": d.Resist, "blocked": d.Blocked, "periodic": d.Periodic}})
	return nil
}

// onPeriodic emits one event per tick; energize and leech ticks also name the power type.
func (s *Session) onPeriodic(body []byte) error {
	p, err := game.ParsePeriodicAuraLog(body)
	if err != nil {
		return err
	}
	for _, t := range p.Ticks {
		data := map[string]any{"aura_type": t.AuraType, "tick": t.Kind(), "school": t.School}
		if k := t.Kind(); k == "energize" || k == "leech" {
			data["power"] = t.PowerType
		}
		s.emit(events.Event{Kind: events.KindPeriodic, SourceGUID: p.Caster, TargetGUID: p.Target, SpellID: p.SpellID,
			Amount: int64(t.Amount), Data: data})
	}
	return nil
}

func (s *Session) onHeal(body []byte) error {
	h, err := game.ParseSpellHealLog(body)
	if err != nil {
		return err
	}
	s.emit(events.Event{Kind: events.KindHeal, SourceGUID: h.Caster, TargetGUID: h.Target, SpellID: h.SpellID, Amount: int64(h.Amount), Crit: h.Crit})
	return nil
}

func (s *Session) onEnergize(body []byte) error {
	e, err := game.ParseSpellEnergizeLog(body)
	if err != nil {
		return err
	}
	s.emit(events.Event{Kind: events.KindEnergize, SourceGUID: e.Caster, TargetGUID: e.Target, SpellID: e.SpellID,
		Amount: int64(e.Amount), Data: map[string]any{"power": e.Power}})
	return nil
}

func (s *Session) onSpellStart(body []byte) error {
	st, err := game.ParseSpellStart(body)
	if err != nil {
		return err
	}
	s.store.StartCast(st.Caster, st.SpellID, st.CastTime)
	if st.Caster == s.selfGUID() {
		s.spellState.CastStarted(st.SpellID, st.CastTime, st.Targets.Unit)
	}
	s.emit(events.Event{Kind: events.KindSpellStart, SourceGUID: st.Caster, TargetGUID: st.Targets.Unit, SpellID: st.SpellID,
		Data: map[string]any{"cast_ms": st.CastTime}})
	return nil
}

func (s *Session) onSpellGo(body []byte) error {
	g, err := game.ParseSpellGo(body)
	if err != nil {
		return err
	}
	s.store.EndCast(g.Caster, g.SpellID)
	if g.Caster == s.selfGUID() {
		if started, ok := s.spellState.CastWent(g.SpellID); ok {
			s.emitCooldownStart(started)
		}
	}
	target := g.Targets.Unit
	if target == 0 && len(g.Hits) > 0 {
		target = g.Hits[0]
	}
	s.emit(events.Event{Kind: events.KindSpellGo, SourceGUID: g.Caster, TargetGUID: target, SpellID: g.SpellID,
		Data: map[string]any{"hits": len(g.Hits), "misses": len(g.Misses)}})
	return nil
}

func (s *Session) onCastResult(body []byte) error {
	c, err := game.ParseCastResult(body)
	if err != nil || !c.Failed {
		return err
	}
	s.store.EndCast(s.selfGUID(), c.SpellID)
	reason := s.spellState.CastFailed(c.SpellID, c.Reason)
	s.emit(events.Event{Kind: events.KindSpellFail, SourceGUID: s.selfGUID(), SpellID: c.SpellID, Data: map[string]any{"reason": c.Reason, "reason_name": reason}})
	s.noteCastFailed(fmt.Sprintf("SpellCastResult %d", c.Reason))
	return nil
}

func (s *Session) onSpellFailedOther(body []byte) error {
	c, err := game.ParseSpellFailedOther(body)
	if err != nil {
		return err
	}
	s.store.EndCast(c.Caster, c.SpellID)
	s.emit(events.Event{Kind: events.KindSpellFail, SourceGUID: c.Caster, SpellID: c.SpellID, Data: map[string]any{"reason": "interrupted", "reason_name": "interrupted"}})
	if c.Caster == s.selfGUID() {
		s.spellState.CastFailed(c.SpellID, game.CastFailedInterrupted)
		s.noteCastFailed("interrupted")
	}
	return nil
}

// onSpellDelayed is pushback on the player's own cast: guid, u32 ms (Spell.cpp:7673-7678).
func (s *Session) onSpellDelayed(body []byte) error {
	r := packet.NewReader(body)
	g, ms := r.GUID(), r.U32()
	if err := r.Err(); err != nil {
		return fmt.Errorf("SMSG_SPELL_DELAYED: %w", err)
	}
	s.store.DelayCast(g, ms)
	if g == s.selfGUID() {
		s.spellState.CastDelayed(ms)
	}
	return nil
}

// onAuraDuration is the time left on one of the player's own auras: u8 slot, u32 ms, -1
// for none (SpellAuraHolder::UpdateAuraDuration, SpellAuras.cpp:7576-7588).
func (s *Session) onAuraDuration(body []byte) error {
	r := packet.NewReader(body)
	slot, ms := r.U8(), r.I32()
	if err := r.Err(); err != nil {
		return fmt.Errorf("SMSG_UPDATE_AURA_DURATION: %w", err)
	}
	s.store.AuraDuration(s.selfGUID(), int(slot), ms)
	return nil
}

func (s *Session) onSpellLogMiss(body []byte) error {
	m, err := game.ParseSpellLogMiss(body)
	if err != nil {
		return err
	}
	for _, miss := range m.Misses {
		s.emit(events.Event{Kind: events.KindSpellMiss, SourceGUID: m.Caster, TargetGUID: miss.Target, SpellID: m.SpellID,
			Data: map[string]any{"miss": game.SpellMissName(miss.Reason)}})
	}
	return nil
}

// onProcResist reports a resisted proc as a spell miss, the way the 1.12 combat log words
// both ("Your X was resisted by Y"); proc tells the two apart.
func (s *Session) onProcResist(body []byte) error {
	p, err := game.ParseProcResist(body)
	if err != nil {
		return err
	}
	s.emit(events.Event{Kind: events.KindSpellMiss, SourceGUID: p.Caster, TargetGUID: p.Target, SpellID: p.SpellID,
		Data: map[string]any{"miss": "resist", "proc": true}})
	return nil
}

func (s *Session) onKill(body []byte) error {
	k, err := game.ParsePartyKillLog(body)
	if err != nil {
		return err
	}
	s.emit(events.Event{Kind: events.KindKill, SourceGUID: k.Killer, TargetGUID: k.Victim})
	return nil
}
