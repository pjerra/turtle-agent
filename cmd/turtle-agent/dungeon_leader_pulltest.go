package main

import (
	"context"
	"fmt"
	"time"

	"github.com/pjerra/turtle-agent/internal/brain"
	"github.com/pjerra/turtle-agent/internal/dungeon"
	"github.com/pjerra/turtle-agent/internal/events"
)

// The halves of the leader-mode pull test (README "Pull test in leader mode") that need the session: the character's own `.respawn`, and
// its damage and healing from the event log; and the brain's counters, converted for the driver.

// Respawn sends the character's own ".respawn" with nothing selected (dungeon.Respawner), as the commander's does: the selection is cleared
// first and the server is waited for to show it cleared, else the line would act on that one unit alone.
func (g *sessionGame) Respawn(ctx context.Context) error {
	s := g.cur()
	if err := s.ClearTarget(ctx); err != nil {
		return err
	}
	timer := time.NewTimer(gmSelectConfirmWait)
	defer timer.Stop()
	tick := time.NewTicker(25 * time.Millisecond)
	defer tick.Stop()
	for {
		if sel, ok := s.ServerSelection(); ok && sel == 0 {
			break
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-timer.C:
			sel, _ := s.ServerSelection()
			return fmt.Errorf("the server did not show the selection cleared within %s (it shows %v): not sending .respawn", gmSelectConfirmWait, sel)
		case <-tick.C:
		}
	}
	return s.GM(ctx, ".respawn")
}

// CombatMark is the position of the event log now (dungeon.CombatMeter).
func (g *sessionGame) CombatMark() uint64 { return g.cur().Log().Last() }

// CombatSince adds up what the character did since the mark: the amount of its melee hits and spell damage, and of its heals (the heal
// log's amount, overhealing included).
func (g *sessionGame) CombatSince(mark uint64) dungeon.CombatTotals {
	s := g.cur()
	self := s.Status().GUID
	var out dungeon.CombatTotals
	for since := mark; ; {
		page := s.Log().Read(events.Query{Since: since, Kinds: []string{events.KindMelee, events.KindSpellDamage, events.KindHeal}, Limit: events.MaxLimit})
		for _, e := range page.Events {
			if e.SourceGUID != self {
				continue
			}
			if e.Kind == events.KindHeal {
				out.Healing += e.Amount
			} else {
				out.Damage += e.Amount
			}
		}
		if !page.More || page.Next <= since {
			return out
		}
		since = page.Next
	}
}

// ResetStats and CharacterStats make the driver's hold on the brain a dungeon.StatsBrain.
func (b *driverBrain) ResetStats() { b.ctl.ResetStats() }

func (b *driverBrain) CharacterStats() dungeon.CharacterStats {
	s := b.ctl.Stats()
	return characterStats(s)
}

func characterStats(s brain.Stats) dungeon.CharacterStats {
	return dungeon.CharacterStats{Actions: s.Actions, Errors: s.Errors, CastFailed: s.CastFailed, CombatS: s.CombatS, IdleCombatS: s.IdleCombatS,
		MobOnMeS: s.MobOnMeS, MobsOnMe: s.MobsOnMe, PowerSpent: s.PowerSpent, MinPowerPct: s.MinPowerPct}
}
