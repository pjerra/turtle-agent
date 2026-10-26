package planexport

import (
	"strings"
	"testing"

	"github.com/pjerra/turtle-agent/internal/dungeon"
)

// Spec section E: leg-level "needs" parse and validate: item required, mode
// valid, pick/object/how only with door, pick 0-300, object required with door;
// both "optional" and a needs entry for the same item is a plan error.
func TestNeedsParseAndValidate(t *testing.T) {
	const base = `{"version":1,"name":"nd","status":"live-checked (test)","map":209,"entrance":{"x":0,"y":0,"z":0},"legs":[
 {"name":"a","to":{"x":10,"y":0,"z":0},"boss":{"entry":7000,"position":{"x":12,"y":0,"z":0}}},
 REPLACE]}`
	good := `{"name":"b","to":{"x":20,"y":0,"z":0},"needs":[{"item":11000,"name":"Shadowforge Key","mode":"door","pick":250,"object":170560,"how":"Dark Iron Legacy quest (Searing Gorge/BRD)"}],"boss":{"entry":7001,"position":{"x":22,"y":0,"z":0}}}`
	p := parsePlan(t, strings.Replace(base, "REPLACE", good, 1))
	if len(p.Legs[1].Needs) != 1 || p.Legs[1].Needs[0].Item != 11000 || p.Legs[1].Needs[0].Mode != dungeon.NeedDoor || p.Legs[1].Needs[0].Pick != 250 || p.Legs[1].Needs[0].Object != 170560 || p.Legs[1].Needs[0].Name != "Shadowforge Key" || p.Legs[1].Needs[0].How != "Dark Iron Legacy quest (Searing Gorge/BRD)" {
		t.Fatalf("needs %+v", p.Legs[1].Needs)
	}

	for _, tc := range []struct {
		name string
		leg  string
		want string
	}{
		{"bad mode", `{"name":"b","to":{"x":20,"y":0,"z":0},"needs":[{"item":11000,"mode":"key","object":170560}],"boss":{"entry":7001,"position":{"x":22,"y":0,"z":0}}}`, "mode"},
		{"pick on optional", `{"name":"b","to":{"x":20,"y":0,"z":0},"needs":[{"item":9240,"mode":"optional","pick":100}],"boss":{"entry":7001,"position":{"x":22,"y":0,"z":0}}}`, "pick"},
		{"object without door", `{"name":"b","to":{"x":20,"y":0,"z":0},"needs":[{"item":9240,"mode":"optional","object":170560}],"boss":{"entry":7001,"position":{"x":22,"y":0,"z":0}}}`, "object"},
		{"door without object", `{"name":"b","to":{"x":20,"y":0,"z":0},"needs":[{"item":11000,"mode":"door"}],"boss":{"entry":7001,"position":{"x":22,"y":0,"z":0}}}`, "object"},
		{"both forms", `{"name":"b","to":{"x":20,"y":0,"z":0},"optional":{"item":9240,"name":"Mallet"},"needs":[{"item":9240,"mode":"optional"}],"steps":[{"use_object":{"entry":141832,"near":{"x":21,"y":0,"z":0},"stand":{"x":20,"y":0,"z":0},"requires_item":{"entry":9240}}}],"boss":{"entry":7001,"position":{"x":22,"y":0,"z":0}}}`, "both"},
	} {
		_, err := dungeon.Parse("test.json", []byte(strings.Replace(base, "REPLACE", tc.leg, 1)))
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Fatalf("%s: err %v, want %q", tc.name, err, tc.want)
		}
	}
}

// Spec section E: every needs entry becomes an ai_dungeon_leg_need row with the
// EXPORTED leg number (after left-out legs close up), carrying mode, pick,
// object, name and how; the DELETE comes before the INSERT.
func TestNeedsExportedWithDelete(t *testing.T) {
	const js = `{"version":1,"name":"nd","status":"live-checked (test)","map":209,"entrance":{"x":0,"y":0,"z":0},"legs":[
 {"name":"a","to":{"x":10,"y":0,"z":0},"needs":[{"item":7146,"name":"The Scarlet Key","mode":"door","pick":175,"object":104591,"how":"SM Library chest (Arcanist Doan)"}],"boss":{"entry":7000,"position":{"x":12,"y":0,"z":0}}},
 {"name":"gong","to":{"x":15,"y":0,"z":0},"optional":{"item":9240,"name":"Mallet"},"steps":[{"use_object":{"entry":141832,"near":{"x":16,"y":0,"z":0},"stand":{"x":15,"y":0,"z":0},"requires_item":{"entry":9240}}}]},
 {"name":"b","to":{"x":20,"y":0,"z":0},"needs":[{"item":11000,"name":"Shadowforge Key","mode":"door","pick":250,"object":170560,"how":"Dark Iron Legacy quest (Searing Gorge/BRD)"}],"boss":{"entry":7001,"position":{"x":22,"y":0,"z":0}}}]}`
	p := parsePlan(t, js)
	// The gong leg is left out by skip-legs (review round 2: WithoutOptional now writes no need rows at all, for a module
	// without the table; TestWithoutOptionalWritesNothingForTheNeedTable).
	res, err := Build(p, Options{Source: "test", SkipLegs: []string{"gong"}})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Rows.Legs) != 2 || res.Rows.Legs[1].Name != "b" {
		t.Fatalf("legs %+v", res.Rows.Legs)
	}
	if len(res.Rows.Needs) != 2 {
		t.Fatalf("%d need rows, want 2: %+v", len(res.Rows.Needs), res.Rows.Needs)
	}
	if res.Rows.Needs[0].Leg != 0 || res.Rows.Needs[0].Item != 7146 || res.Rows.Needs[0].Mode != 2 || res.Rows.Needs[0].Pick != 175 || res.Rows.Needs[0].Object != 104591 || res.Rows.Needs[0].Name != "The Scarlet Key" || res.Rows.Needs[0].How != "SM Library chest (Arcanist Doan)" {
		t.Fatalf("need row 0 %+v", res.Rows.Needs[0])
	}
	if res.Rows.Needs[1].Leg != 1 || res.Rows.Needs[1].Item != 11000 || res.Rows.Needs[1].Mode != 2 || res.Rows.Needs[1].Pick != 250 || res.Rows.Needs[1].Object != 170560 || res.Rows.Needs[1].How == "" {
		t.Fatalf("need row 1 (want the exported number after the left-out leg) %+v", res.Rows.Needs[1])
	}
	sql := WriteSQL([]Rows{res.Rows})
	del := strings.Index(sql, "DELETE FROM `ai_dungeon_leg_need` WHERE `plan` = 'nd';")
	ins := strings.Index(sql, "INSERT INTO `ai_dungeon_leg_need`")
	legs := strings.Index(sql, "INSERT INTO `ai_dungeon_leg`")
	if del < 0 || ins < 0 || !(del < legs && legs < ins) {
		t.Fatalf("DELETE/INSERT order wrong:\n%s", sql)
	}
	if !strings.Contains(sql, "(`plan`, `leg`, `item`, `mode`, `pick`, `object`, `name`, `how`)") {
		t.Fatalf("need INSERT lacks the object/how columns:\n%s", sql)
	}
	back, err := Parse(sql)
	if err != nil {
		t.Fatal(err)
	}
	if err := Equal([]Rows{res.Rows}, back); err != nil {
		t.Fatal(err)
	}
}
