package main

import (
	"testing"
	"time"

	"github.com/pjerra/turtle-agent/internal/dungeon"
	"github.com/pjerra/turtle-agent/internal/raidwalk"
	"github.com/pjerra/turtle-agent/internal/tbm"
)

// TestRaidWalkAnyOfBossCountsAsEngaged: the Edge of Madness leg's boss row lists the four madness bosses as alternatives
// (boss.any_of; the event makes exactly one active), and the row's canonical entry is 15082. A frame with a live 15084
// — the boss of a later event, not the row's own entry — is that row's boss engaged: the boss gates see it (bossSeenAt,
// bossRows), the attempt starts on it, and the kill evidence counts it (bossUnits). Before Boss.AnyOf and the rows-side
// rule, the walk read only the row's entry and this boss was invisible to every gate.
func TestRaidWalkAnyOfBossCountsAsEngaged(t *testing.T) {
	p, err := dungeon.Load("../../dungeons/zul_gurub.json")
	if err != nil {
		t.Fatal(err)
	}
	var boss *dungeon.Boss
	for i := range p.Legs {
		if p.Legs[i].Name == "edge_of_madness" {
			boss = p.Legs[i].Boss
		}
	}
	if boss == nil || boss.Entry != 15082 || len(boss.AnyOf) != 4 {
		t.Fatalf("the plan's edge_of_madness boss = %+v, want entry 15082 with 4 any_of alternatives", boss)
	}
	now := time.Date(2026, 11, 16, 20, 0, 0, 0, time.UTC)
	w := &walk{
		plan:          p,
		env:           walkEnv{now: func() time.Time { return now }},
		bossRule:      raidwalk.NewBossRows(p),
		bossRows:      map[uint32]hostileState{},
		hostiles:      map[string]hostileState{},
		partyG:        map[uint64]bool{},
		botAt:         map[string]botPos{},
		bossAttemptAt: map[uint32]time.Time{},
		bossAdds:      map[uint32]bool{},
	}
	w.trackFrame(now, &tbm.ViewFrame{Hostiles: []tbm.HostileRec{{
		GUID: 9, Entry: 15084, X: boss.Position.X, Y: boss.Position.Y, Z: boss.Position.Z, HPPct: 60,
	}}})
	row, ok := w.bossRows[15084]
	if !ok || row.hppct != 60 {
		t.Fatalf("bossRows = %+v, want the live 15084: it is a unit of the any_of boss row", w.bossRows)
	}
	if w.bossSeenAt.IsZero() {
		t.Fatal("bossSeenAt was not set by the 15084 frame")
	}
	if !w.bossEngagedNow() {
		t.Fatal("bossEngagedNow() = false, want true: a live alternative of the boss row is the boss")
	}
	if at := w.bossAttemptAt[15084]; at.IsZero() {
		t.Fatal("bossAttemptAt[15084] is zero, want the attempt started by the live 15084")
	}
	if !w.bossUnit(15084) {
		t.Fatal("bossUnit(15084) = false, want true")
	}
	units := w.bossUnits(15082)
	found := false
	for _, e := range units {
		if e == 15084 {
			found = true
		}
	}
	if !found {
		t.Fatalf("bossUnits(15082) = %v, want the row's units including 15084: its corpse is the row's kill", units)
	}
}
