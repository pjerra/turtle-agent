package main

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/pjerra/turtle-agent/internal/dungeon"
	"github.com/pjerra/turtle-agent/internal/packet"
	"github.com/pjerra/turtle-agent/internal/planexport"
	"github.com/pjerra/turtle-agent/internal/raidwalk"
	"github.com/pjerra/turtle-agent/internal/tbm"
)

// TestTrackFrameAnyOfAlternativeStoresCanonical: the frames show the Edge of Madness boss as 15084, one of the row's
// any_of alternatives (the event makes exactly one active), while the row's canonical entry is 15082. bossGUIDs keys
// the guid to the ROW's entry, because every reader compares it with the row's entry (bossGUIDFor, bossFormsFor,
// bossKilled's guid rule): before the fix it stored 15084, so those readers missed the guid and a morphed alternative
// lost the rule. The corpse of the active alternative is still the row's kill.
func TestTrackFrameAnyOfAlternativeStoresCanonical(t *testing.T) {
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
	if boss == nil || boss.Entry != 15082 {
		t.Fatalf("the plan's edge_of_madness boss = %+v, want entry 15082", boss)
	}
	now := time.Date(2026, 11, 16, 20, 0, 0, 0, time.UTC)
	w := &walk{
		plan:          p,
		env:           walkEnv{now: func() time.Time { return now }},
		mt:            "tank",
		bossRule:      raidwalk.NewBossRows(p),
		bossRows:      map[uint32]hostileState{},
		hostiles:      map[string]hostileState{},
		partyG:        map[uint64]bool{},
		botAt:         map[string]botPos{},
		bossAttemptAt: map[uint32]time.Time{},
		bossAdds:      map[uint32]bool{},
	}
	const guid = packet.GUID(9)
	w.trackFrame(now, &tbm.ViewFrame{Hostiles: []tbm.HostileRec{{
		GUID: guid, Entry: 15084, X: boss.Position.X, Y: boss.Position.Y, Z: boss.Position.Z, HPPct: 60,
	}}})
	if got := w.bossGUIDs[guid.String()]; got != 15082 {
		t.Fatalf("bossGUIDs[%s] = %d, want the row's canonical entry 15082 (the frames read the active alternative 15084)", guid, got)
	}
	// The same guid, now the active alternative's corpse, is the row's kill. The party row keeps a hire alive: a wipe
	// is no kill.
	now = now.Add(20 * time.Second)
	w.trackFrame(now, &tbm.ViewFrame{
		Party: []tbm.PartyRec{{Name: "tank", GUID: 1000}},
		Hostiles: []tbm.HostileRec{{
			GUID: guid, Entry: 15084, X: boss.Position.X, Y: boss.Position.Y, Z: boss.Position.Z, HPPct: 0,
		}},
	})
	ok, ev := w.bossKilled(context.Background(), 15082, now.Add(time.Second))
	if !ok {
		t.Fatalf("bossKilled(15082) = false (%s), want true: the active alternative 15084's corpse is the row's kill", ev)
	}
}

// TestBossKillSummaryActiveEntryThroughPhase: the row's guid was seen as the active any_of alternative 15084 and then
// morphed into a phase entry 99999 with no tracker latch. The summary's active_entry is the alternative the form chain
// STARTED from: reading only the last form (99999 maps to itself, not to the row's 15082) dropped it.
func TestBossKillSummaryActiveEntryThroughPhase(t *testing.T) {
	p, err := dungeon.Load("../../dungeons/zul_gurub.json")
	if err != nil {
		t.Fatal(err)
	}
	res, err := planexport.Build(p, planexport.Options{Source: "test", WithOptional: []string{"edge_of_madness"}, BossAnyOf: true})
	if err != nil {
		t.Fatal(err)
	}
	step := 0
	for i, s := range res.Rows.Steps {
		if s.Kind == planexport.KindBoss && s.Entry == 15082 {
			step = i + 1
		}
	}
	if step == 0 {
		t.Fatalf("no boss row 15082 in the export: %+v", res.Rows.Steps)
	}
	w := &walk{
		rows:       res.Rows,
		bossTracks: map[bossKey]*bossStepTrack{},
		bossGUIDs:  map[string]uint32{},
		bossForms:  map[string][]uint32{},
	}
	g := packet.GUID(0xF13000390700C09E).String()
	w.bossGUIDs[g] = 15082
	w.bossForms[g] = []uint32{15084, 99999}
	tk := w.bossTrackAt(step, 15082)
	tk.guid = g
	if k := w.bossKillSummaryFor(tk); k.ActiveEntry != 15084 {
		t.Fatalf("summary active_entry = %d (phase %d entry %d), want the chain's first alternative 15084", k.ActiveEntry, k.Phase, k.PhaseEntry)
	}
}

// TestFinishAnyOfTrackUsesCanonicalEntry: the tracker's one-shot latch reports the entry it saw engaged, which for an
// any_of row is the active alternative (15084). finish's block creates the track from it (finish, via bossStepOf and
// bossTrackAt), so the summary must say boss 15082 — the row's canonical entry, the one the statuses and the
// autopilot's goal name — and keep the alternative as active_entry.
func TestFinishAnyOfTrackUsesCanonicalEntry(t *testing.T) {
	p, err := dungeon.Load("../../dungeons/zul_gurub.json")
	if err != nil {
		t.Fatal(err)
	}
	// edge_of_madness is an optional leg: the default export leaves it out, so the rows are built with it in.
	res, err := planexport.Build(p, planexport.Options{Source: "test", WithOptional: []string{"edge_of_madness"}, BossAnyOf: true})
	if err != nil {
		t.Fatal(err)
	}
	step := 0
	for i, s := range res.Rows.Steps {
		if s.Kind == planexport.KindBoss && s.Entry == 15082 {
			step = i + 1
		}
	}
	if step == 0 {
		t.Fatalf("no boss row 15082 in the export: %+v", res.Rows.Steps)
	}
	w := &walk{
		rows:       res.Rows,
		bossTracks: map[bossKey]*bossStepTrack{},
		bossGUIDs:  map[string]uint32{},
		bossForms:  map[string][]uint32{},
	}
	if s := w.bossStepOf(15084); s != step {
		t.Fatalf("bossStepOf(15084) = %d, want the row's step %d", s, step)
	}
	w.bossTrackAt(step, 15084).engaged = true
	if len(w.bossTracks) != 1 {
		t.Fatalf("%d tracks, want one record for the boss step: %+v", len(w.bossTracks), w.bossTracks)
	}
	tk, ok := w.bossTracks[bossKey{step: step}]
	if !ok {
		t.Fatalf("no track keyed by step %d: %+v", step, w.bossTracks)
	}
	if tk.entry != 15082 {
		t.Fatalf("track entry = %d, want the canonical 15082", tk.entry)
	}
	k := w.bossKillSummary()
	if k == nil || k.Entry != 15082 || k.Step != step || k.ActiveEntry != 15084 {
		t.Fatalf("summary boss_kill = %+v, want entry 15082 at step %d, active_entry 15084", k, step)
	}
	out, err := json.Marshal(k)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(out), `"entry":15082`) || !strings.Contains(string(out), `"active_entry":15084`) {
		t.Fatalf("summary JSON %s, want entry 15082 and active_entry 15084", out)
	}
}
