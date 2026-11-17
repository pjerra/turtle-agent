package main

import (
	"io"
	"reflect"
	"slices"
	"testing"
	"time"

	"github.com/pjerra/turtle-agent/internal/dungeon"
	"github.com/pjerra/turtle-agent/internal/raidwalk"
	"github.com/pjerra/turtle-agent/internal/tbm"
)

// allPlanRunLegs is a run of every leg of a plan these tests build or load: no leg is left out, so the walk's rules
// read the whole plan. A walk's own run legs come from the rows it walks (raidwalk.PlanRunLegs).
func allPlanRunLegs(p *dungeon.Plan) raidwalk.RunLegs {
	run := raidwalk.RunLegs{}
	if p != nil {
		for i := range p.Legs {
			run[i] = true
		}
	}
	return run
}

// allLegsBossRule is the boss rule over every leg of a plan these tests build.
func allLegsBossRule(p *dungeon.Plan) *raidwalk.BossRows {
	return raidwalk.NewBossRows(p, allPlanRunLegs(p))
}

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
		bossRule:      allLegsBossRule(p),
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

// TestDefaultZulGurubWalkIgnoresOptionalLegBosses: a default Zul'Gurub walk (no --with-optional) reads the plan as
// origin/dev did. zul_gurub.json still names the optional edge_of_madness (15082-15085) and gahzranka (15114) legs,
// but a default export leaves them out of the rows, so the run does not walk them: neither is a boss row (the gates,
// the one-shot latch and the injector's filter must not read it) and neither stands a quiet home spawn (the revive's
// idle exemption must not trust it). Both rules equal a plan with the optional legs cut, which is what dev read.
func TestDefaultZulGurubWalkIgnoresOptionalLegBosses(t *testing.T) {
	w := &walk{bossSet: map[uint32]bool{}, stdout: io.Discard, stderr: io.Discard,
		opts: raidWalkOptions{dir: "../../dungeons", plan: "zul_gurub", until: "none"}}
	if err := w.validate(); err != nil {
		t.Fatal(err)
	}
	p := w.plan
	optional := 0
	for li, l := range p.Legs {
		if l.Optional == nil {
			continue
		}
		optional++
		if w.runLegs[li] {
			t.Errorf("leg %d (%s) is optional and a default export leaves it out, but the walk runs it", li, l.Name)
		}
	}
	if optional != 2 {
		t.Fatalf("zul_gurub has %d optional legs, want 2 (edge_of_madness, gahzranka)", optional)
	}
	for _, e := range []uint32{15082, 15083, 15084, 15085, 15114} {
		if w.bossRule.Listed(e) {
			t.Errorf("%d listed as a boss row, want none: its leg is not in the run", e)
		}
		if w.bossRule.Counts(e, 0, 0) {
			t.Errorf("Counts(%d, 0, 0) = true, want false: the unit gate must not latch an optional leg's boss", e)
		}
		if w.bossRule.Owns(e) {
			t.Errorf("Owns(%d) = true, want false: the guid rule must not key an optional leg's boss", e)
		}
	}
	// The plan with the optional legs cut: what the rules read before the run scope existed.
	cut := *p
	cut.Legs = nil
	for _, l := range p.Legs {
		if l.Optional == nil {
			cut.Legs = append(cut.Legs, l)
		}
	}
	cutRun := allPlanRunLegs(&cut)
	if got, want := raidwalk.PlanHomeSpawns(p, w.runLegs), raidwalk.PlanHomeSpawns(&cut, cutRun); !reflect.DeepEqual(got, want) {
		t.Fatalf("home spawns %+v, want the plan without its optional legs (%+v)", got, want)
	}
	cutRule := raidwalk.NewBossRows(&cut, cutRun)
	if got, want := w.bossRule.Ambiguous(), cutRule.Ambiguous(); !slices.Equal(got, want) {
		t.Errorf("ambiguous %v, want %v", got, want)
	}
	entries := map[uint32]bool{}
	for _, l := range append(append([]dungeon.Leg(nil), p.Legs...), cut.Legs...) {
		for _, g := range l.PullGroups {
			for _, e := range g.Entries {
				entries[e] = true
			}
		}
		for _, lv := range l.Leave {
			entries[lv.Entry] = true
		}
		if b := l.Boss; b != nil {
			for _, e := range append(append(append([]uint32{b.Entry}, b.AnyOf...), b.First...), b.PhaseEntries...) {
				entries[e] = true
			}
			if b.Hold != nil {
				for _, e := range b.Hold.Entries {
					entries[e] = true
				}
			}
		}
	}
	for e := range entries {
		if got, want := w.bossRule.Listed(e), cutRule.Listed(e); got != want {
			t.Errorf("Listed(%d) = %v, want %v (the plan without its optional legs)", e, got, want)
		}
		if got, want := w.bossRule.Owns(e), cutRule.Owns(e); got != want {
			t.Errorf("Owns(%d) = %v, want %v (the plan without its optional legs)", e, got, want)
		}
		if got, want := w.bossRule.Counts(e, 0, 0), cutRule.Counts(e, 0, 0); got != want {
			t.Errorf("Counts(%d, 0, 0) = %v, want %v (the plan without its optional legs)", e, got, want)
		}
	}
}
