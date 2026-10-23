package planexport

// CarrierChests (with DropCompanionsLoot) exports a companions_loot-false leg's loot_object steps as carrier rows: the stand move, the
// loot row with FlagGoneIsDone, and — when the leg has leftover_entries — the sweep pull of those entries at the chest's point. The
// shipped Zul'Farrak plan (the zumrah leg, 31 Shallow Graves 128403) is the carrier.

import (
	"fmt"
	"strings"
	"testing"

	"github.com/pjerra/turtle-agent/internal/dungeon"
)

// zulfarrakWithoutBly builds the shipped plan with the bly leg skipped (as every export of it does: a boss that turns hostile after
// his gossip).
func zulfarrakWithoutBly(t *testing.T, carrier bool) *Result {
	t.Helper()
	p, err := dungeon.Load("../../dungeons/zulfarrak.json")
	if err != nil {
		t.Fatal(err)
	}
	opt := Options{Source: "test", DropCompanionsLoot: true, SkipLegs: []string{"bly"}, CarrierChests: carrier}
	res, err := Build(p, opt)
	if err != nil {
		t.Fatal(err)
	}
	if err := Validate(res.Rows); err != nil {
		t.Fatalf("the rows do not validate: %v", err)
	}
	return &res
}

// legRows returns the rows of the named leg.
func zulfarrakLegRows(t *testing.T, r Rows, name string) []StepRow {
	t.Helper()
	n := -1
	for _, l := range r.Legs {
		if l.Name == name {
			n = l.Leg
		}
	}
	if n < 0 {
		t.Fatalf("no leg row named %s in %+v", name, r.Legs)
	}
	var out []StepRow
	for _, s := range r.Steps {
		if s.Leg == n {
			out = append(out, s)
		}
	}
	if len(out) == 0 {
		t.Fatalf("no step rows of leg %s", name)
	}
	return out
}

// TestCarrierChestsExportGravesAsRows: with CarrierChests the zumrah leg carries a move/loot/pull triple for every one of the plan's
// 31 Shallow Grave steps, in the plan's order, at the plan's stand and near points, each triple before the trigger move and the boss.
func TestCarrierChestsExportGravesAsRows(t *testing.T) {
	p, err := dungeon.Load("../../dungeons/zulfarrak.json")
	if err != nil {
		t.Fatal(err)
	}
	res := zulfarrakWithoutBly(t, true)
	rows := zulfarrakLegRows(t, res.Rows, "zumrah")
	var graves []dungeon.LootObject
	for _, l := range p.Legs {
		if l.Name == "zumrah" {
			for _, st := range l.Steps {
				if st.LootObject != nil {
					graves = append(graves, *st.LootObject)
				}
			}
		}
	}
	if len(graves) != 31 {
		t.Fatalf("the plan's zumrah leg has %d loot_object steps, want 31", len(graves))
	}
	// The zumrah row: via move, to move, then move loot pull per grave, then the trigger move, then the boss.
	need := 2 + 3*len(graves) + 2
	if len(rows) != need {
		t.Fatalf("the zumrah leg has %d rows, want %d (via, to, 3 per grave, trigger, boss)", len(rows), need)
	}
	gi := 0
	for k := 0; 2+3*k+2 < len(rows); k++ {
		if gi >= len(graves) {
			break
		}
		lo := graves[gi]
		gi++
		m, l, sw := rows[2+3*k], rows[3+3*k], rows[4+3*k]
		if m.Kind != KindMove || !strings.HasPrefix(m.Comment, "stand") {
			t.Fatalf("grave %d: first row is %s %q", gi, m.Kind, m.Comment)
		}
		if float32(lo.Stand.X) != m.X || float32(lo.Stand.Y) != m.Y || float32(lo.Stand.Z) != m.Z {
			t.Fatalf("grave %d: stand is %.2f %.2f %.2f, want the plan's %v", gi, m.X, m.Y, m.Z, *lo.Stand)
		}
		if l.Kind != KindLoot || l.Entry != lo.Entry {
			t.Fatalf("grave %d: second row is %s %d", gi, l.Kind, l.Entry)
		}
		if l.Flags != FlagGoneIsDone {
			t.Fatalf("grave %d: loot row flags %d, want %d (gone is done)", gi, l.Flags, FlagGoneIsDone)
		}
		if l.Item != itemEntry(lo.ExpectItem) {
			t.Fatalf("grave %d: loot item %d", gi, l.Item)
		}
		if float32(lo.Near.X) != l.X || float32(lo.Near.Y) != l.Y || float32(lo.Near.Z) != l.Z {
			t.Fatalf("grave %d: loot point %.2f %.2f %.2f, want the plan's near %v", gi, l.X, l.Y, l.Z, *lo.Near)
		}
		if sw.Kind != KindPull || !strings.HasPrefix(sw.Comment, "sweep") {
			t.Fatalf("grave %d: third row is %s %q", gi, sw.Kind, sw.Comment)
		}
		if sw.Entry != 0 {
			t.Fatalf("grave %d: the sweep's entry column holds %d", gi, sw.Entry)
		}
		if fmt.Sprint(sw.Entries) != "[7286 7276]" {
			t.Fatalf("grave %d: sweep entries %v", gi, sw.Entries)
		}
		if sw.Radius != carrierSweepRadiusYd {
			t.Fatalf("grave %d: sweep radius %.1f", gi, sw.Radius)
		}
		if float32(lo.Near.X) != sw.X || float32(lo.Near.Y) != sw.Y {
			t.Fatalf("grave %d: sweep point %.2f %.2f, want the grave at %.2f %.2f", gi, sw.X, sw.Y, lo.Near.X, lo.Near.Y)
		}
	}
	if gi != len(graves) {
		t.Fatalf("wrote %d grave triples, want %d", gi, len(graves))
	}
	trig := rows[len(rows)-2]
	if trig.Kind != KindMove || !strings.Contains(trig.Comment, "area trigger 962") {
		t.Fatalf("the row before the boss is %+v", trig)
	}
	boss := rows[len(rows)-1]
	if boss.Kind != KindBoss || boss.Entry != 7271 {
		t.Fatalf("the last row is %+v", boss)
	}
	// No 128308 grave anywhere (the trap graves only), and the plan's order is the rows' order.
	for i, s := range rows {
		if s.Entry == 128308 {
			t.Fatalf("row %d loots the 128308 grave (%s)", i, s.Kind)
		}
	}
	back, err := Parse(WriteSQL([]Rows{res.Rows}))
	if err != nil || Equal([]Rows{res.Rows}, back) != nil {
		t.Fatalf("%v", err)
	}
}

// TestCarrierChestsOffKeepsOld: without the option the zumrah leg is as before — no chest of it in the rows, the steps-left-out
// warning — and the whole plan has no FlagGoneIsDone row.
func TestCarrierChestsOffKeepsOld(t *testing.T) {
	res := zulfarrakWithoutBly(t, false)
	rows := zulfarrakLegRows(t, res.Rows, "zumrah")
	kinds := make([]string, len(rows))
	for i, s := range rows {
		kinds[i] = s.Kind
		if s.Entry == 128403 {
			t.Fatalf("row %d (%s) loots a grave without CarrierChests", i, s.Kind)
		}
		if s.Flags&FlagGoneIsDone != 0 {
			t.Fatalf("row %d carries the gone-is-done flag without CarrierChests", i)
		}
	}
	// via, to, the trigger move, the boss: the four rows the leg had.
	if got := strings.Join(kinds, " "); !strings.HasPrefix(got, "move move ") || !strings.HasSuffix(got, " move boss") || len(kinds) > 4 {
		t.Fatalf("the zumrah leg without CarrierChests: %s (%d rows)", got, len(kinds))
	}
	warn := false
	for _, w := range res.Warnings {
		warn = warn || strings.Contains(w, "loot_object steps left out")
	}
	if !warn {
		t.Fatalf("no left-out warning: %v", res.Warnings)
	}
}

// TestCarrierChestsNeedsDropCompanionsLoot: without DropCompanionsLoot the plan is refused all the same; a loader that knows no
// gone-is-done flag (a loot/open row only) would refuse the flagged rows, so Validate refuses the flag on any other kind.
func TestCarrierChestsNeedsDropCompanionsLoot(t *testing.T) {
	p, err := dungeon.Load("../../dungeons/zulfarrak.json")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Build(p, Options{Source: "test", CarrierChests: true, SkipLegs: []string{"bly"}}); err == nil || !strings.Contains(err.Error(), "--drop-companions-loot") {
		t.Fatalf("CarrierChests without DropCompanionsLoot: %v", err)
	}
	r := Rows{Plan: PlanRow{Plan: "zf"}, Steps: []StepRow{{Kind: KindMove}}}
	r.Steps[0].Flags = FlagGoneIsDone
	if err := Validate(r); err == nil || !strings.Contains(err.Error(), "gone-is-done flag is for a loot or open row") {
		t.Fatalf("the flag on a move row: %v", err)
	}
	r.Steps[0] = StepRow{Kind: KindPull, Entries: []uint32{7286}, Flags: FlagGoneIsDone}
	if err := Validate(r); err == nil || !strings.Contains(err.Error(), "gone-is-done flag is for a loot or open row") {
		t.Fatalf("the flag on a pull row: %v", err)
	}
}

// TestCarrierChestsWarnings: with CarrierChests the companions_loot warning names the setup (the trapped-chest rule a module would
// add) instead of dropping, and the leg's leftover_entries are no longer warned unexpressed (every chest's sweep pull carries them).
// The loot_object batch warning stays: one chest a row, no batches.
func TestCarrierChestsWarnings(t *testing.T) {
	res := zulfarrakWithoutBly(t, true)
	text := strings.Join(res.Warnings, "\n")
	if !strings.Contains(text, "not carried: turn the bots' looting off for this leg (setup), or a module with the trapped-chest rule") {
		t.Fatalf("no setup warning: %v", res.Warnings)
	}
	for _, want := range []string{
		"looting off for every bot",
		"party loot method free-for-all",
		"Executioner's Key 8444 on the lead bot",
		"at least 16 free bag slots on the lead bot",
		"a fresh instance",
		"flag 256 needs a TortoiseBots module with the gone-is-done step flag",
		"pauses on a despawned grave after a wipe (then a .bot run skip skips the row)",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("the setup warning lacks %q: %v", want, res.Warnings)
		}
	}
	if strings.Contains(text, "dropped (--drop-companions-loot): the bots loot as they like") {
		t.Fatalf("the old dropped text under CarrierChests: %v", res.Warnings)
	}
	if strings.Contains(text, "leftover_entries") {
		t.Fatalf("the leftovers are swept by the chest rows, not unexpressed: %v", res.Warnings)
	}
	if !strings.Contains(text, "loot_object batch") {
		t.Fatalf("the batch warning is kept: %v", res.Warnings)
	}
	// Without CarrierChests the old texts come back.
	without := zulfarrakWithoutBly(t, false)
	old := strings.Join(without.Warnings, "\n")
	if !strings.Contains(old, "dropped (--drop-companions-loot): the bots loot as they like") || strings.Contains(old, "not carried: turn") {
		t.Fatalf("the old texts under DropCompanionsLoot alone: %v", without.Warnings)
	}
}

// TestCarrierChestsRefusesAStandlessChest: with CarrierChests a loot_object with no stand has no row form (the chest rows walk the
// party to a stand before the loot) and is refused, naming the step's entry.
func TestCarrierChestsRefusesAStandlessChest(t *testing.T) {
	p := parsePlan(t, `{"version":1,"name":"fc","status":"live-checked","map":209,"entrance":{"x":0,"y":0,"z":0},"legs":[{"name":"grave","to":{"x":20,"y":0,"z":0},"companions_loot":false,"steps":[{"loot_object":{"entry":128403,"near":{"x":10,"y":0,"z":0}}}]}]}`)
	_, err := Build(p, Options{Source: "test", DropCompanionsLoot: true, CarrierChests: true})
	if err == nil || !strings.Contains(err.Error(), "leg 0 (grave): steps[0]: loot_object 128403 has no stand") {
		t.Fatalf("a chest without a stand: %v", err)
	}
	// Without CarrierChests the step is left out as before, not refused.
	if _, err := Build(p, Options{Source: "test", DropCompanionsLoot: true}); err != nil {
		t.Fatalf("without CarrierChests: %v", err)
	}
}
