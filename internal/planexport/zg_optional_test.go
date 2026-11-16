package planexport

import (
	"bytes"
	"encoding/json"
	"math"
	"slices"
	"strings"
	"testing"

	"github.com/pjerra/turtle-agent/internal/dungeon"
)

func loadZGPlan(t *testing.T) *dungeon.Plan {
	t.Helper()
	p, err := dungeon.Load("../../dungeons/zul_gurub.json")
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func findLegIndex(p *dungeon.Plan, name string) int {
	for i, l := range p.Legs {
		if l.Name == name {
			return i
		}
	}
	return -1
}

// TestZulGurubEdgeOfMadnessLeg checks that zul_gurub.json has the optional edge_of_madness leg
// at the end of legs (after hakkar, so existing leg indices stay unchanged), with optional item 19931,
// use_object 180327 via_item within dungeon.UseRange of the brazier, wait_for the four madness bosses,
// and the boss row.
func TestZulGurubEdgeOfMadnessLeg(t *testing.T) {
	p := loadZGPlan(t)
	arlokk := findLegIndex(p, "high_priestess_arlokk")
	hakkar := findLegIndex(p, "hakkar")
	eom := findLegIndex(p, "edge_of_madness")
	if arlokk != 4 || hakkar < 0 || eom <= hakkar {
		t.Fatalf("leg order: arlokk=%d hakkar=%d edge_of_madness=%d (want arlokk=4 and eom after hakkar)", arlokk, hakkar, eom)
	}
	leg := p.Legs[eom]
	if leg.Optional == nil || leg.Optional.Item != 19931 {
		t.Fatalf("optional = %+v, want item 19931", leg.Optional)
	}
	if len(leg.Steps) != 2 || leg.Steps[0].UseObject == nil {
		t.Fatalf("steps = %+v, want [use_object, wait_for]", leg.Steps)
	}
	u := leg.Steps[0].UseObject
	if u.Entry != 180327 || !u.ViaItem || u.RequiresItem == nil || u.RequiresItem.Entry != 19931 {
		t.Fatalf("use_object = %+v, want entry 180327 via_item with requires_item 19931", u)
	}
	if u.Near == nil || u.Stand == nil {
		t.Fatalf("use_object near=%v stand=%v, want both set", u.Near, u.Stand)
	}
	if d := dist3(*u.Stand, *u.Near); d > dungeon.UseRange {
		t.Fatalf("stand is %.2f yd from brazier, want <= %.2f (dungeon.UseRange)", d, dungeon.UseRange)
	}
	w := leg.Steps[1].WaitFor
	if w == nil || w.Unit == nil || !slices.Equal(w.Unit.Entries, []uint32{15082, 15083, 15084, 15085}) || w.TimeoutS != 40 {
		t.Fatalf("wait_for = %+v, want entries [15082 15083 15084 15085] timeout 40", w)
	}
	if leg.Boss == nil || leg.Boss.Entry != 15082 || !slices.Equal(leg.Boss.AnyOf, []uint32{15082, 15083, 15084, 15085}) ||
		leg.Boss.Position.X != -11901.3 || leg.Boss.Position.Y != -1906.52 || leg.Boss.Position.Z != 65.36 {
		t.Fatalf("boss = %+v, want 15082 with any_of [15082 15083 15084 15085] at (-11901.3, -1906.52, 65.36)", leg.Boss)
	}

	res, err := Build(p, Options{Source: "test", WithOptional: []string{"edge_of_madness"}, BossAnyOf: true})
	if err != nil {
		t.Fatal(err)
	}
	var rowLeg = -1
	for _, l := range res.Rows.Legs {
		if l.Name == "edge_of_madness" {
			rowLeg = l.Leg
		}
	}
	if rowLeg < 0 {
		t.Fatalf("edge_of_madness missing from --with-optional export: %+v", res.Rows.Legs)
	}
	var gotUseOn, gotBoss bool
	for _, s := range res.Rows.Steps {
		if s.Leg != rowLeg {
			continue
		}
		if s.Kind == KindUseOn && s.Entry == 180327 && s.Item == 19931 {
			gotUseOn = true
		}
		if s.Kind == KindBoss && s.Entry == 15082 {
			gotBoss = true
		}
	}
	if !gotUseOn || !gotBoss {
		t.Fatalf("exported edge_of_madness rows: gotUseOn=%v gotBoss=%v", gotUseOn, gotBoss)
	}
}

// TestZulGurubEdgeOfMadnessBossAnyOf is the Edge of Madness row: the four madness bosses are the row's alternatives, the
// row carries FlagBossAnyOf (65536) so a module that does not know the flag must not load it, and 15082 is its canonical
// entry. Without Boss.AnyOf and this export the leg wrote one boss row fixed to 15085 (the boss of the 2026-11-15 event),
// which is wrong once the 14-day rotation turns over.
func TestZulGurubEdgeOfMadnessBossAnyOf(t *testing.T) {
	p := loadZGPlan(t)
	res, err := Build(p, Options{Source: "test", WithOptional: []string{"edge_of_madness"}, BossAnyOf: true})
	if err != nil {
		t.Fatal(err)
	}
	var boss *StepRow
	for i := range res.Rows.Steps {
		if RowBossIs(res.Rows.Steps[i], 15084) {
			boss = &res.Rows.Steps[i]
			break
		}
	}
	if boss == nil {
		t.Fatalf("no boss row of the edge_of_madness leg takes 15084: %+v", res.Rows.Steps)
	}
	if boss.Kind != KindBoss || boss.Entry != 15082 {
		t.Fatalf("boss row = %+v, want kind boss with the canonical entry 15082", boss)
	}
	if boss.Flags&FlagBossAnyOf == 0 {
		t.Fatalf("boss row flags = %d, want FlagBossAnyOf (%d) set: an older module would read entries as kill-first adds", boss.Flags, FlagBossAnyOf)
	}
	if !slices.Equal(boss.Entries, []uint32{15082, 15083, 15084, 15085}) {
		t.Fatalf("boss row entries = %v, want the four madness bosses", boss.Entries)
	}
	if want := "boss; any of 15082/15083/15084/15085"; boss.Comment != want {
		t.Fatalf("boss row comment = %q, want %q", boss.Comment, want)
	}
}

// TestBossAnyOfExportWarning: an any_of boss row exported with the explicit --boss-anyof opt-in also carries the warning
// that it needs TortoiseBots kPlanFlagBossAnyOf, because an older module would wait for all of the row's entries. The
// default export (no --with-optional) has neither the row nor the warning.
func TestBossAnyOfExportWarning(t *testing.T) {
	p := loadZGPlan(t)
	res, err := Build(p, Options{Source: "test", WithOptional: []string{"edge_of_madness"}, BossAnyOf: true})
	if err != nil {
		t.Fatal(err)
	}
	want := "leg 9 (edge_of_madness): boss row any_of needs TortoiseBots kPlanFlagBossAnyOf (65536)"
	var found bool
	for _, w := range res.Warnings {
		if strings.Contains(w, want) {
			found = true
		}
	}
	if !found {
		t.Fatalf("warnings %q, want one with %q", res.Warnings, want)
	}
	def, err := Build(p, Options{Source: "test"})
	if err != nil {
		t.Fatal(err)
	}
	for _, w := range def.Warnings {
		if strings.Contains(w, "kPlanFlagBossAnyOf") {
			t.Fatalf("the default export warned about an any_of row it left out: %q", w)
		}
	}
}

// TestZulGurubGahzrankaLeg checks that zul_gurub.json has the optional gahzranka leg at the end
// of legs (after hakkar, so existing leg indices stay unchanged), with optional item 19974,
// use_item 19974, a dry beach stand near Gahz'ranka's beach, wait_for 15114, and boss row 15114 at
// the beach (-11688.95, -1777.21, 12.59).
func TestZulGurubGahzrankaLeg(t *testing.T) {
	p := loadZGPlan(t)
	outer := findLegIndex(p, "hakkar_outer")
	hakkar := findLegIndex(p, "hakkar")
	gahz := findLegIndex(p, "gahzranka")
	if outer != 7 || hakkar < 0 || gahz <= hakkar {
		t.Fatalf("leg order: hakkar_outer=%d hakkar=%d gahzranka=%d (want outer=7 and gahz after hakkar)", outer, hakkar, gahz)
	}
	leg := p.Legs[gahz]
	if leg.Optional == nil || leg.Optional.Item != 19974 {
		t.Fatalf("optional = %+v, want item 19974", leg.Optional)
	}
	if math.Hypot(float64(leg.To.X)-(-11674), float64(leg.To.Y)-(-1757.9)) > 2 {
		t.Fatalf("stand/to = %+v, want near (-11674, -1757.9)", leg.To)
	}
	if len(leg.Steps) != 2 || leg.Steps[0].UseItem == nil || leg.Steps[0].UseItem.Entry != 19974 {
		t.Fatalf("steps[0] = %+v, want use_item 19974", leg.Steps)
	}
	w := leg.Steps[1].WaitFor
	if w == nil || w.Unit == nil || !slices.Equal(w.Unit.Entries, []uint32{15114}) || w.TimeoutS != 45 {
		t.Fatalf("wait_for = %+v, want entries [15114] timeout 45", w)
	}
	if leg.Boss == nil || leg.Boss.Entry != 15114 || leg.Boss.Position.X != -11688.95 || leg.Boss.Position.Y != -1777.21 || leg.Boss.Position.Z != 12.59 {
		t.Fatalf("boss = %+v, want 15114 at (-11688.95, -1777.21, 12.59)", leg.Boss)
	}

	res, err := Build(p, Options{Source: "test", WithOptional: []string{"gahzranka"}})
	if err != nil {
		t.Fatal(err)
	}
	var rowLeg = -1
	for _, l := range res.Rows.Legs {
		if l.Name == "gahzranka" {
			rowLeg = l.Leg
		}
	}
	if rowLeg < 0 {
		t.Fatalf("gahzranka missing from --with-optional export: %+v", res.Rows.Legs)
	}
	var gotUseItem, gotBoss bool
	for _, s := range res.Rows.Steps {
		if s.Leg != rowLeg {
			continue
		}
		if s.Kind == KindUseItem && s.Entry == 19974 {
			gotUseItem = true
		}
		if s.Kind == KindBoss && s.Entry == 15114 && s.X == -11688.95 && s.Y == -1777.21 && s.Z == 12.59 {
			gotBoss = true
		}
	}
	if !gotUseItem || !gotBoss {
		t.Fatalf("exported gahzranka rows: gotUseItem=%v gotBoss=%v", gotUseItem, gotBoss)
	}
}

// TestPlanexportAnyOfRowNeedsFlag: a boss row of alternatives is written ONLY with the explicit --boss-anyof opt-in.
// An older TortoiseBots module reads the row's entries as kill-first adds and waits for all of them, so an export
// without the flag refuses the plan instead of writing a row an old run leader would misrun. With the flag the row
// carries FlagBossAnyOf and the row's canonical entry.
func TestPlanexportAnyOfRowNeedsFlag(t *testing.T) {
	p := loadZGPlan(t)
	_, err := Build(p, Options{Source: "test", WithOptional: []string{"edge_of_madness"}})
	if err == nil {
		t.Fatal("an any_of boss row was exported without --boss-anyof")
	}
	for _, want := range []string{"--boss-anyof", "kPlanFlagBossAnyOf"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("error %q does not name %q", err, want)
		}
	}
	res, err := Build(p, Options{Source: "test", WithOptional: []string{"edge_of_madness"}, BossAnyOf: true})
	if err != nil {
		t.Fatalf("the opt-in export was refused: %v", err)
	}
	var boss *StepRow
	for i := range res.Rows.Steps {
		if RowBossIs(res.Rows.Steps[i], 15084) {
			boss = &res.Rows.Steps[i]
			break
		}
	}
	if boss == nil || boss.Entry != 15082 || boss.Flags&FlagBossAnyOf == 0 || !slices.Equal(boss.Entries, []uint32{15082, 15083, 15084, 15085}) {
		t.Fatalf("opt-in boss row = %+v, want entry 15082 with FlagBossAnyOf and entries [15082 15083 15084 15085]", boss)
	}
}

// TestZulGurubDefaultExportUnchangedByOptionalLegs verifies that without --with-optional,
// the exported zul_gurub rows are byte-identical to exporting the plan without the optional legs.
func TestZulGurubDefaultExportUnchangedByOptionalLegs(t *testing.T) {
	p := loadZGPlan(t)
	withOptInPlan, err := Build(p, Options{Source: "test"})
	if err != nil {
		t.Fatal(err)
	}
	for _, l := range withOptInPlan.Rows.Legs {
		if l.Name == "edge_of_madness" || l.Name == "gahzranka" {
			t.Fatalf("optional leg %q appeared in default export", l.Name)
		}
	}

	stripped := *p
	stripped.Legs = make([]dungeon.Leg, 0, len(p.Legs)-2)
	for _, l := range p.Legs {
		if l.Name != "edge_of_madness" && l.Name != "gahzranka" {
			stripped.Legs = append(stripped.Legs, l)
		}
	}
	withoutOptInPlan, err := Build(&stripped, Options{Source: "test"})
	if err != nil {
		t.Fatal(err)
	}

	gotBytes, err := json.Marshal(withOptInPlan.Rows)
	if err != nil {
		t.Fatal(err)
	}
	wantBytes, err := json.Marshal(withoutOptInPlan.Rows)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(gotBytes, wantBytes) {
		t.Fatalf("default export differs with optional legs present:\ngot:  %s\nwant: %s", gotBytes, wantBytes)
	}
}
