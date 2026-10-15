package planexport

import (
	"fmt"
	"strings"
	"testing"

	"github.com/pjerra/turtle-agent/internal/dungeon"
)

func parsePlan(t *testing.T, js string) *dungeon.Plan {
	t.Helper()
	p, err := dungeon.Parse("test.json", []byte(js))
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func build(t *testing.T, p *dungeon.Plan) Rows {
	t.Helper()
	res, err := Build(p, Options{Source: "test"})
	if err != nil {
		t.Fatal(err)
	}
	return res.Rows
}

func kinds(r Rows, leg int) []string {
	var out []string
	for _, s := range r.Steps {
		if s.Leg == leg {
			out = append(out, s.Kind)
		}
	}
	return out
}

const small = `{"version":1,"name":"Tiny_Cave","status":"live-checked (test)","map":70,"entrance":{"x":0,"y":0,"z":0},"legs":[
 {"name":"first","via":[{"x":1,"y":0,"z":0},{"x":10,"y":0,"z":0,"direct":true}],"to":{"x":20,"y":0,"z":0},
  "pull_groups":[
   {"entries":[100,101,100],"positions":[{"x":30,"y":0,"z":0},{"x":40,"y":0,"z":0}],"team":"horde"},
   {"entries":[102],"positions":[{"x":50,"y":10,"z":0}],"ambush":true,"radius":12.5,"team":"alliance"}],
  "steps":[
   {"use_object":{"entry":900,"near":{"x":60,"y":0,"z":0},"stand":{"x":58,"y":0,"z":0},"requires_item":{"entry":7733,"name":"Staff"}}},
   {"wait_for":{"gameobject":{"entry":901,"near":{"x":61,"y":0,"z":0},"state":"active_alt"},"timeout_s":90}},
   {"wait_for":{"hostiles_gone":{"radius":20}}},
   {"wait_for":{"hostiles_gone":{"radius":8,"center":{"x":5,"y":5,"z":5}},"timeout_s":30}},
   {"loot_object":{"entry":902,"near":{"x":62,"y":0,"z":0},"expect_item":{"entry":7740,"name":"Gni'kiv Medallion"}}},
   {"use_item":{"entry":7740,"name":"Gni'kiv Medallion","expect_item":{"entry":7733}}},
   {"ritual":{"entry":903,"position":{"x":63,"y":0,"z":0},"participants":3}}],
  "boss":{"entry":6906,"position":{"x":70,"y":0,"z":0},"first":[6907,6908],"team":"horde","loot_item":{"entry":5555,"name":"Prize"}},
  "after_boss":[{"loot_object":{"entry":904,"near":{"x":71,"y":0,"z":0}}}]},
 {"name":"second","to":{"x":80,"y":0,"z":0},"boss":{"entry":7000,"position":{"x":90,"y":0,"z":0}}}]}`

func TestMappingFollowsTheGuide(t *testing.T) {
	r := build(t, parsePlan(t, small))
	if r.Plan.Plan != "tiny_cave" || r.Plan.Map != 70 || r.Plan.Source != "test" {
		t.Fatalf("plan row %+v", r.Plan)
	}
	if len(r.Legs) != 2 || r.Legs[0].Name != "first" || r.Legs[1].Leg != 1 {
		t.Fatalf("legs %+v", r.Legs)
	}
	want := []string{"move", "move_direct", "move", "pull", "pull", "move", "open", "wait_object", "wait_clear", "wait_clear", "loot", "use_item", "ritual", "boss", "loot", "loot"}
	if got := kinds(r, 0); strings.Join(got, " ") != strings.Join(want, " ") {
		t.Fatalf("leg 0 kinds\n got %v\nwant %v", got, want)
	}
	if got := kinds(r, 1); strings.Join(got, " ") != "move boss" {
		t.Fatalf("leg 1 kinds %v", got)
	}
	for i, s := range r.Steps { // seq counts from 0 in each leg, without gaps
		if s.Leg == 0 && s.Seq != i {
			t.Fatalf("step %d has seq %d", i, s.Seq)
		}
	}
	at := func(i int) StepRow { return r.Steps[i] }
	// the via waypoints, then To
	if at(1).Kind != KindMoveDirect || at(1).X != 10 || at(2).X != 20 {
		t.Fatalf("via/to %+v %+v", at(1), at(2))
	}
	// pull 0: centre = mean of the positions, radius = farthest + 10 yd, deduplicated entries, horde = 4
	p0 := at(3)
	if p0.X != 35 || p0.Radius != 15 || p0.Flags != FlagHorde || entriesText(p0.Entries) != "100,101" {
		t.Fatalf("pull 0 %+v", p0)
	}
	// pull 1: ambush + alliance, the plan's own radius
	p1 := at(4)
	if p1.Flags != FlagAmbush|FlagAlliance || p1.Radius != 12.5 || p1.X != 50 {
		t.Fatalf("pull 1 %+v", p1)
	}
	// use_object: a move to stand, then open with the lock's item
	if m := at(5); m.Kind != KindMove || m.X != 58 {
		t.Fatalf("stand move %+v", m)
	}
	if o := at(6); o.Entry != 900 || o.X != 60 || o.Item != 7733 {
		t.Fatalf("open %+v", o)
	}
	if w := at(7); w.Entry != 901 || w.Param != 2 || w.TimeoutS != 90 {
		t.Fatalf("wait_object %+v", w)
	}
	// a hostiles_gone wait without a centre is about the last point the leg put the party at (the stand spot)
	if w := at(8); w.Radius != 20 || w.X != 58 || w.TimeoutS != dungeon.DefaultTimeoutS {
		t.Fatalf("wait_clear without a centre %+v", w)
	}
	if w := at(9); w.X != 5 || w.Z != 5 || w.Radius != 8 || w.TimeoutS != 30 {
		t.Fatalf("wait_clear %+v", w)
	}
	if l := at(10); l.Kind != KindLoot || l.Entry != 902 || l.Item != 7740 || !strings.Contains(l.Comment, "Gni'kiv") {
		t.Fatalf("loot %+v", l)
	}
	if u := at(11); u.Entry != 7740 || u.Item != 7733 {
		t.Fatalf("use_item %+v", u)
	}
	if rt := at(12); rt.Kind != KindRitual || rt.Entry != 903 || rt.X != 63 {
		t.Fatalf("ritual %+v", rt)
	}
	// the boss: entries = first, team gate; its loot_item is a loot row of the boss entry right after it; after_boss steps follow
	if b := at(13); b.Entry != 6906 || entriesText(b.Entries) != "6907,6908" || b.Flags != FlagHorde || b.Radius != 0 {
		t.Fatalf("boss %+v", b)
	}
	if l := at(14); l.Kind != KindLoot || l.Entry != 6906 || l.Item != 5555 {
		t.Fatalf("boss loot %+v", l)
	}
	if l := at(15); l.Entry != 904 || l.Item != 0 {
		t.Fatalf("after_boss %+v", l)
	}
}

func TestSQLRoundTrip(t *testing.T) {
	r := build(t, parsePlan(t, small))
	r2 := build(t, parsePlan(t, strings.Replace(small, "Tiny_Cave", "other", 1)))
	sql := WriteSQL([]Rows{r, r2})
	for _, want := range []string{"DELETE FROM `ai_dungeon_step` WHERE `plan` = 'tiny_cave';", "INSERT INTO `ai_dungeon_plan`", "Gni''kiv Medallion"} {
		if !strings.Contains(sql, want) {
			t.Fatalf("migration lacks %q", want)
		}
	}
	back, err := Parse(sql)
	if err != nil {
		t.Fatal(err)
	}
	if err := Equal([]Rows{r, r2}, back); err != nil {
		t.Fatal(err)
	}
	// idempotent: each plan's deletes come before its inserts
	if strings.Index(sql, "DELETE FROM `ai_dungeon_plan` WHERE `plan` = 'other'") > strings.Index(sql, "INSERT INTO `ai_dungeon_plan` (`plan`, `map`, `cc_priority`, `cc_always`, `kill_first`, `source`, `comment`) VALUES\n('other'") {
		t.Fatal("delete after insert")
	}
}

func TestQuoting(t *testing.T) {
	r := Rows{Plan: PlanRow{Plan: "q", Map: 1, Source: `it's a \ test`, Comment: "a, b (c)"}, Legs: []LegRow{{Plan: "q", Leg: 0, Name: "x"}},
		Steps: []StepRow{{Plan: "q", Kind: KindRest, Comment: `back\slash 'quote', and (parens)`}}}
	back, err := Parse(WriteSQL([]Rows{r}))
	if err != nil {
		t.Fatal(err)
	}
	if err := Equal([]Rows{r}, back); err != nil {
		t.Fatal(err)
	}
}

func TestDraftIsRefused(t *testing.T) {
	p := parsePlan(t, strings.Replace(small, `"live-checked (test)"`, `"draft (not mapped, not live-checked)"`, 1))
	if Refusal(p, false) == "" || Refusal(p, true) != "" {
		t.Fatal("a draft is refused without allow-draft only")
	}
	if IsLive(p) {
		t.Fatal("a draft is not live")
	}
}

func TestLimitsAreRefused(t *testing.T) {
	cases := map[string]string{
		"radius":  strings.Replace(small, `"radius":12.5`, `"radius":150`, 1),
		"timeout": strings.Replace(small, `"timeout_s":90`, `"timeout_s":601`, 1),
	}
	for name, js := range cases {
		p, err := dungeon.Parse("t.json", []byte(js))
		if err != nil {
			continue // the plan loader's own limit (300 yd, 600 s) came first
		}
		if _, err := Build(p, Options{}); err == nil {
			t.Errorf("%s over the module's limit was exported", name)
		}
	}
	p := parsePlan(t, small)
	p.Name = "Bad Name"
	if _, err := Build(p, Options{}); err == nil {
		t.Error("a name with a space was exported")
	}
}

func TestLongHopsAreListedOrCut(t *testing.T) {
	js := `{"version":1,"name":"far","status":"live-checked","map":70,"entrance":{"x":0,"y":0,"z":0},"legs":[
	 {"name":"a","to":{"x":0,"y":0,"z":0},"boss":{"entry":1,"position":{"x":5,"y":0,"z":0}}},
	 {"name":"b","to":{"x":200,"y":0,"z":0},"boss":{"entry":2,"position":{"x":205,"y":0,"z":0}}}]}`
	p := parsePlan(t, js)
	res, err := Build(p, Options{})
	if err != nil || len(res.Warnings) != 1 || !strings.Contains(res.Warnings[0], "leg 1 step 0") {
		t.Fatalf("%v %v", res.Warnings, err)
	}
	cut := func(_ uint32, from, to dungeon.Point) ([]dungeon.Point, error) {
		return []dungeon.Point{{X: 50}, {X: 100}, {X: 150}}, nil
	}
	res, err = Build(p, Options{Cut: cut})
	if err != nil || len(res.Warnings) != 0 {
		t.Fatalf("%v %v", res.Warnings, err)
	}
	if got := kinds(res.Rows, 1); strings.Join(got, " ") != "move move move move boss" {
		t.Fatalf("leg 1 %v", got)
	}
	for i, s := range res.Rows.Steps {
		if s.Leg == 1 && s.Seq != i-2 { // leg 0 holds 2 rows
			t.Fatalf("step %d seq %d", i, s.Seq)
		}
	}
}

// TestUldaman exports the live-checked Uldaman plan and reads the migration back.
func TestUldaman(t *testing.T) {
	p, err := dungeon.Load("../../dungeons/uldaman.json")
	if err != nil {
		t.Fatal(err)
	}
	if !IsLive(p) || Refusal(p, false) != "" {
		t.Fatalf("uldaman status %q", p.Status)
	}
	res, err := Build(p, Options{Source: "turtle-agent test dungeons/uldaman.json"})
	if err != nil {
		t.Fatal(err)
	}
	r := res.Rows
	pulls, bosses, rituals, legsWithBoss := 0, 0, 0, 0
	for _, l := range p.Legs {
		pulls += len(l.PullGroups)
		if l.Boss != nil {
			legsWithBoss++
		}
	}
	for _, s := range r.Steps {
		switch s.Kind {
		case KindPull:
			pulls--
		case KindBoss:
			bosses++
		case KindRitual:
			rituals++
		}
	}
	if pulls != 0 || bosses != legsWithBoss || rituals != 2 || len(r.Legs) != len(p.Legs) || r.Plan.Plan != "uldaman" || r.Plan.Map != 70 {
		t.Fatalf("pulls left %d, bosses %d (want %d), rituals %d, legs %d", pulls, bosses, legsWithBoss, rituals, len(r.Legs))
	}
	back, err := Parse(WriteSQL([]Rows{r}))
	if err != nil {
		t.Fatal(err)
	}
	if err := Equal([]Rows{r}, back); err != nil {
		t.Fatal(err)
	}
	if len(back) != 1 || back[0].Count() != r.Count() {
		t.Fatalf("read back %d plans", len(back))
	}
	t.Logf("uldaman: %d legs, %d steps, %d rows, %d warnings", len(r.Legs), len(r.Steps), r.Count(), len(res.Warnings))
}

// TestZulfarrakExportCarriesTheUnitLists: the plan row has cc_priority, cc_always and kill_first (TortoiseBots #1001's columns on
// ai_dungeon_plan), as comma lists like `entries`, in the column order of that migration, and they read back.
func TestZulfarrakExportCarriesTheUnitLists(t *testing.T) {
	p, err := dungeon.Load("../../dungeons/zulfarrak.json")
	if err != nil {
		t.Fatal(err)
	}
	res, err := Build(p, Options{Source: "test"})
	if err != nil {
		t.Fatal(err)
	}
	r := res.Rows
	if len(r.Plan.CCPriority) == 0 || len(r.Plan.CCAlways) == 0 || len(r.Plan.KillFirst) == 0 {
		t.Fatalf("plan row %+v: all three lists must be set", r.Plan)
	}
	sql := WriteSQL([]Rows{r})
	want := "INSERT INTO `ai_dungeon_plan` (`plan`, `map`, `cc_priority`, `cc_always`, `kill_first`, `source`, `comment`) VALUES\n('zulfarrak', 209, '5650,7247,7246,5648', '5650,7246', '8179,5650,7247', 'test', "
	if !strings.Contains(sql, want) {
		t.Fatalf("the migration lacks %q", want)
	}
	back, err := Parse(sql)
	if err != nil {
		t.Fatal(err)
	}
	if err := Equal([]Rows{r}, back); err != nil {
		t.Fatal(err)
	}
	if fmt.Sprint(back[0].Plan.KillFirst) != "[8179 5650 7247]" {
		t.Fatalf("read back %+v", back[0].Plan)
	}
}

func TestPlanListTooLongIsRefused(t *testing.T) {
	p := parsePlan(t, small)
	for i := 0; i < 60; i++ {
		p.KillFirst = append(p.KillFirst, 10000+uint32(i))
	}
	if _, err := Build(p, Options{}); err == nil || !strings.Contains(err.Error(), "kill_first") {
		t.Fatalf("a kill_first list over 255 characters must be refused: %v", err)
	}
}
