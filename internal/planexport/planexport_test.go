package planexport

import (
	"errors"
	"fmt"
	"slices"
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
   {"use_item":{"entry":7740,"name":"Gni'kiv Medallion","requires_item":{"entry":7741},"expect_item":{"entry":7733}}},
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
	if u := at(11); u.Entry != 7740 || u.Item != 7733 || u.Param != 7741 {
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
	pulls, bosses, rituals, holds, legsWithBoss := 0, 0, 0, 0, 0
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
			if s.Param != 3 {
				t.Errorf("ritual %d: param %d, want its 3 participants", s.Entry, s.Param)
			}
		case KindHold:
			holds++
		}
	}
	// The Stone Keepers' group and Archaedas are the run leader's two event holds (TortoiseBots #1041): instance data 1 and 2, DONE,
	// flags 40, the rows that migration set by hand.
	if pulls != 1 || bosses != legsWithBoss-1 || holds != 2 || rituals != 2 || len(r.Legs) != len(p.Legs) || r.Plan.Plan != "uldaman" || r.Plan.Map != 70 {
		t.Fatalf("pulls left %d (want the keepers' 1), bosses %d (want %d), holds %d, rituals %d, legs %d", pulls, bosses, legsWithBoss-1, holds, rituals, len(r.Legs))
	}
	var got []string
	for _, s := range r.Steps {
		if s.Kind == KindHold || (s.Kind == KindWaitObject && s.Leg == 11) {
			got = append(got, fmt.Sprintf("%d/%d %s %d %g %g %g r%g p%d f%d t%d %v", s.Leg, s.Seq, s.Kind, s.Entry, s.X, s.Y, s.Z, s.Radius, s.Param, s.Flags, s.TimeoutS, s.Entries))
		}
	}
	if want := []string{
		"11/0 hold 1 101.5 271.2 -26.5 r30 p3 f40 t600 [4857]",
		"11/1 wait_object 124367 137.7 288.1 -26.4 r0 p1 f0 t60 []",
		"13/4 hold 2 94 267.8 -52 r45 p3 f40 t600 [2748 7309 7077 7076 10120]",
	}; strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Errorf("event rows:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
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
	p := zulfarrakThroughZumrah(t)
	res, err := Build(p, Options{Source: "test", DropCompanionsLoot: true})
	if err != nil {
		t.Fatal(err)
	}
	r := res.Rows
	if len(r.Plan.CCPriority) == 0 || len(r.Plan.CCAlways) == 0 || len(r.Plan.KillFirst) == 0 {
		t.Fatalf("plan row %+v: all three lists must be set", r.Plan)
	}
	sql := WriteSQL([]Rows{r})
	want := "INSERT INTO `ai_dungeon_plan` (`plan`, `map`, `cc_priority`, `cc_always`, `kill_first`, `source`, `comment`) VALUES\n('zulfarrak', 209, '7246,5648', '7246', '7785,8179,5650,7247', 'test', "
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
	if fmt.Sprint(back[0].Plan.KillFirst) != "[7785 8179 5650 7247]" {
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

// An area_trigger step becomes a move to its stand point: a lead bot that walks into the zone takes the trigger itself when the module
// lists it (TortoiseBots #1002). The agent's own character and unit waits have no run leader step, and are refused with the reason.
func TestAreaTriggerBecomesAMoveToTheStand(t *testing.T) {
	const trig = `{"area_trigger":{"id":962,"center":{"x":50,"y":0,"z":0},"radius":10,"stand":{"x":44,"y":0,"z":0},"by":"%s","expect":"unit 7271 hostile"}}`
	plan := func(by string) *dungeon.Plan {
		return parsePlan(t, `{"version":1,"name":"zf","status":"live-checked","map":209,"entrance":{"x":0,"y":0,"z":0},"legs":[{"to":{"x":20,"y":0,"z":0},"companions_loot":false,"steps":[`+
			strings.Replace(trig, "%s", by, 1)+`],"boss":{"entry":7271,"position":{"x":52,"y":0,"z":0},"tactics":{"kill_first":[7785]}}}]}`)
	}
	for _, by := range []string{"auto", "bot"} {
		r := buildDropLoot(t, plan(by))
		if got := strings.Join(kinds(r, 0), " "); got != "move move boss" {
			t.Fatalf("%s: %s", by, got)
		}
		m := r.Steps[1]
		if m.X != 44 || m.Y != 0 || !strings.Contains(m.Comment, "area trigger 962: the bot walks into the zone; needs AiPlayerbot.BotAreaTriggers = 962 (TortoiseBots #1002)") || !strings.Contains(m.Comment, "unit 7271 hostile") {
			t.Fatalf("%s: %+v", by, m)
		}
	}
	if _, err := Build(plan("agent"), Options{Source: "test", DropCompanionsLoot: true}); err == nil || !strings.Contains(err.Error(), `area_trigger 962: by "agent" has no run leader step`) {
		t.Fatalf("%v", err)
	}
	wait := parsePlan(t, `{"version":1,"name":"w","status":"live-checked","map":209,"entrance":{"x":0,"y":0,"z":0},"legs":[{"to":{"x":20,"y":0,"z":0},"steps":[{"wait_for":{"unit":{"entries":[7271],"state":"dead"}}}]}]}`)
	if _, err := Build(wait, Options{Source: "test"}); err == nil || !strings.Contains(err.Error(), "wait_for.unit has no run leader step (unit 7271 dead)") {
		t.Fatalf("%v", err)
	}
}

// zulfarrakThroughZumrah is the shipped Zul'Farrak plan cut after the zumrah leg: the legs after it (the stairs event, Bly, the End Door,
// Ukorz) use the gossip step and unit waits, which the run leader has no step for.
func zulfarrakThroughZumrah(t *testing.T) *dungeon.Plan {
	t.Helper()
	p, err := dungeon.Load("../../dungeons/zulfarrak.json")
	if err != nil {
		t.Fatal(err)
	}
	for i, l := range p.Legs {
		if l.Name == "zumrah" {
			p.Legs = p.Legs[:i+1]
			return p
		}
	}
	t.Fatal("the plan has no zumrah leg")
	return nil
}

// The whole shipped Zul'Farrak plan is exported up to the zumrah leg: the legs after it (the stairs event) use a unit wait and the gossip
// step, which the run leader has no step for, so the export is cut before the first of them, with a note that names the leg and the step.
func TestZulfarrakIsCutAfterZumrah(t *testing.T) {
	p, err := dungeon.Load("../../dungeons/zulfarrak.json")
	if err != nil {
		t.Fatal(err)
	}
	res, err := Build(p, Options{Source: "test", DropCompanionsLoot: true})
	if err != nil {
		t.Fatal(err)
	}
	whole := buildWhole(t, zulfarrakThroughZumrah(t))
	if len(res.Rows.Legs) != 11 || res.Rows.Legs[10].Name != "zumrah" || len(res.Rows.Steps) != len(whole.Rows.Steps) || res.Rows.Count() != whole.Rows.Count() {
		t.Fatalf("%d legs, %d steps (the plan through zumrah: %d)", len(res.Rows.Legs), len(res.Rows.Steps), len(whole.Rows.Steps))
	}
	for _, want := range []string{"exported legs 0-10 (zumrah) only", "cut before leg 11 (stairs)", "after_boss[1]: wait_for.unit has no run leader step (unit 7607 seen)", "5 after it"} {
		if !strings.Contains(res.Cut, want) {
			t.Errorf("the cut note lacks %q: %s", want, res.Cut)
		}
	}
	if !slices.Contains(res.Warnings, res.Cut) || !strings.Contains(res.Rows.Plan.Comment, "cut before leg 11 (stairs)") {
		t.Errorf("warnings %v, plan comment %q", res.Warnings, res.Rows.Plan.Comment)
	}
	back, err := Parse(WriteSQL([]Rows{res.Rows}))
	if err != nil || Equal([]Rows{res.Rows}, back) != nil {
		t.Fatalf("%v", err)
	}
}

// buildWhole builds a plan that has to export whole.
func buildWhole(t *testing.T, p *dungeon.Plan) Result {
	t.Helper()
	res, err := Build(p, Options{Source: "test", DropCompanionsLoot: true})
	if err != nil {
		t.Fatal(err)
	}
	if res.Cut != "" {
		t.Fatalf("cut: %s", res.Cut)
	}
	return res
}

// The cut: a step the run leader cannot do in a later leg ends the export before that leg (its rows, and the warnings it made, are gone); in
// the first leg there is nothing to export, and the plan is refused.
func TestCutBeforeTheFirstLegWithAStepTheRunLeaderCannotDo(t *testing.T) {
	const walk = `{"name":"a","to":{"x":1,"y":0,"z":0},"pull_groups":[{"entries":[1],"positions":[{"x":5,"y":0,"z":0}]}]}`
	const talk = `{"name":"%s","to":{"x":10,"y":0,"z":0},"steps":[{"gossip":{"npc":7607,"near":{"x":12,"y":0,"z":0},"text":"blow","by":"tank"}}],"boss":{"entry":2,"position":{"x":14,"y":0,"z":0}}}`
	plan := func(legs ...string) *dungeon.Plan {
		return parsePlan(t, `{"version":1,"name":"cut","status":"live-checked (test)","map":209,"entrance":{"x":0,"y":0,"z":0},"legs":[`+strings.Join(legs, ",")+`]}`)
	}
	res, err := Build(plan(walk, strings.Replace(talk, "%s", "b", 1), walk), Options{Source: "test"})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Rows.Legs) != 1 || res.Rows.Legs[0].Name != "a" || res.Cut == "" || !strings.Contains(res.Cut, "cut before leg 1 (b)") || !strings.Contains(res.Cut, "gossip (NPC 7607") || !strings.Contains(res.Cut, "1 after it") {
		t.Fatalf("%d legs, cut %q", len(res.Rows.Legs), res.Cut)
	}
	for _, st := range res.Rows.Steps {
		if st.Leg != 0 {
			t.Errorf("a row of leg %d is left: %+v", st.Leg, st)
		}
	}
	if _, err := Build(plan(strings.Replace(talk, "%s", "a", 1), walk), Options{Source: "test"}); err == nil || !errors.Is(err, ErrNoRunLeaderStep) || !strings.Contains(err.Error(), "leg 0 (a)") {
		t.Fatalf("a first leg that cannot be exported: %v", err)
	}
}

// The shipped Zul'Farrak plan, with the courtyard and Zum'rah legs, still exports: the area trigger is the walk into the zone.
func TestZulfarrakWithZumrahExports(t *testing.T) {
	p := zulfarrakThroughZumrah(t)
	res, err := Build(p, Options{Source: "turtle-agent test dungeons/zulfarrak.json", DropCompanionsLoot: true})
	if err != nil {
		t.Fatal(err)
	}
	// The optional gahzrilla leg is left out of the run leader's rows (its bots carry no Mallet), so the rows have one leg fewer and the later
	// legs close up: the last leg is the plan's last, one number lower.
	if len(res.Rows.Legs) != len(p.Legs)-1 {
		t.Fatalf("%d rows legs for %d plan legs", len(res.Rows.Legs), len(p.Legs))
	}
	last := len(res.Rows.Legs) - 1
	if p.Legs[len(p.Legs)-1].Name != "zumrah" || res.Rows.Legs[last].Name != "zumrah" {
		t.Fatalf("%s", p.Legs[len(p.Legs)-1].Name)
	}
	for _, l := range res.Rows.Legs {
		if l.Name == "gahzrilla" {
			t.Errorf("the optional leg is in the rows: %+v", l)
		}
	}
	for _, s := range res.Rows.Steps {
		if s.Entry == 7273 || s.Entry == 141832 {
			t.Errorf("a step of the optional leg is in the rows: %+v", s)
		}
	}
	// via, to, the walk into the zone, the boss: the grave steps (the carrier bot's loot_object) are left out of a leg whose companions_loot is false.
	if got := strings.Join(kinds(res.Rows, last), " "); got != "move move move boss" {
		t.Fatalf("%s", got)
	}
	if len(res.Warnings) == 0 || !strings.Contains(strings.Join(res.Warnings, "\n"), "31 loot_object steps left out") {
		t.Fatalf("warnings %v", res.Warnings)
	}
	if w := strings.Join(res.Warnings, "\n"); !strings.Contains(w, "leg 9 (gahzrilla): optional (needs Mallet of Zul'Farrak (item 9240)") {
		t.Fatalf("no warning for the optional leg: %v", res.Warnings)
	}
	back, err := Parse(WriteSQL([]Rows{res.Rows}))
	if err != nil || Equal([]Rows{res.Rows}, back) != nil {
		t.Fatalf("%v", err)
	}
}

func buildDropLoot(t *testing.T, p *dungeon.Plan) Rows {
	t.Helper()
	res, err := Build(p, Options{Source: "test", DropCompanionsLoot: true})
	if err != nil {
		t.Fatal(err)
	}
	return res.Rows
}

// A leg whose companions_loot is false cannot be exported: the run leader's rows cannot carry it, and its bots would loot the leg's chests.
// DropCompanionsLoot says to export without it; the shipped Zul'Farrak plan has such a leg.
func TestCompanionsLootFalseLegIsRefused(t *testing.T) {
	p := parsePlan(t, `{"version":1,"name":"zf","status":"live-checked","map":209,"entrance":{"x":0,"y":0,"z":0},"legs":[{"name":"grave","to":{"x":20,"y":0,"z":0},"companions_loot":false,"boss":{"entry":7271,"position":{"x":52,"y":0,"z":0}}}]}`)
	_, err := Build(p, Options{Source: "test"})
	if err == nil || !strings.Contains(err.Error(), "leg 0 (grave): companions_loot is false and the run leader's rows cannot carry it") {
		t.Fatalf("%v", err)
	}
	if _, err := Build(p, Options{Source: "test", DropCompanionsLoot: true}); err != nil {
		t.Fatal(err)
	}
	z, err := dungeon.Load("../../dungeons/zulfarrak.json")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Build(z, Options{Source: "test"}); err == nil || !strings.Contains(err.Error(), "(zumrah): companions_loot is false") {
		t.Fatalf("the shipped Zul'Farrak plan: %v", err)
	}
}

// An optional leg (it needs an item no bot of the run leader carries) is left out of the rows with a warning, and the legs after it take its
// numbers; every row's leg number is one of the legs written.
func TestOptionalLegIsLeftOut(t *testing.T) {
	p := parsePlan(t, `{"version":1,"name":"opt","status":"live-checked (test)","map":209,"entrance":{"x":0,"y":0,"z":0},"legs":[
 {"name":"a","to":{"x":10,"y":0,"z":0},"boss":{"entry":1,"position":{"x":20,"y":0,"z":0}}},
 {"name":"gong","to":{"x":30,"y":0,"z":0},"optional":{"item":9240,"name":"Mallet"},"steps":[{"use_object":{"entry":141832,"near":{"x":31,"y":0,"z":0},"requires_item":{"entry":9240,"name":"Mallet"}}}],"boss":{"entry":7273,"position":{"x":40,"y":0,"z":0}}},
 {"name":"c","to":{"x":50,"y":0,"z":0},"boss":{"entry":3,"position":{"x":60,"y":0,"z":0}}}]}`)
	res, err := Build(p, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Rows.Legs) != 2 || res.Rows.Legs[0].Name != "a" || res.Rows.Legs[1].Name != "c" || res.Rows.Legs[1].Leg != 1 {
		t.Fatalf("legs %+v", res.Rows.Legs)
	}
	for _, s := range res.Rows.Steps {
		if s.Leg > 1 || s.Entry == 7273 || s.Entry == 141832 {
			t.Errorf("row %+v belongs to the optional leg or to a leg that is not written", s)
		}
	}
	if got := strings.Join(kinds(res.Rows, 1), " "); got != "move boss" {
		t.Errorf("leg 1: %s", got)
	}
	if len(res.Warnings) != 1 || !strings.Contains(res.Warnings[0], "leg 1 (gong): optional (needs Mallet (item 9240)") {
		t.Errorf("warnings %v", res.Warnings)
	}
	back, err := Parse(WriteSQL([]Rows{res.Rows}))
	if err != nil || Equal([]Rows{res.Rows}, back) != nil {
		t.Fatalf("%v", err)
	}
}

// A use_object whose item is used ON the object (via_item) has no row in the run leader's tables: the export is refused, naming the step.
func TestViaItemUseObjectIsRefused(t *testing.T) {
	p := parsePlan(t, `{"version":1,"name":"vi","status":"live-checked (test)","map":209,"entrance":{"x":0,"y":0,"z":0},"legs":[
 {"name":"gong","to":{"x":30,"y":0,"z":0},"steps":[{"use_object":{"entry":141832,"near":{"x":31,"y":0,"z":0},"stand":{"x":32,"y":0,"z":0},"requires_item":{"entry":9240},"via_item":true}}]}]}`)
	if _, err := Build(p, Options{}); err == nil || !strings.Contains(err.Error(), "via_item") {
		t.Fatalf("%v", err)
	}
}

// A gm_cross waypoint is a GM move of a remote run: the TortoiseBots run leader has no such step, so the export is refused.
func TestGMCrossIsRefused(t *testing.T) {
	p := parsePlan(t, strings.Replace(small, `{"x":10,"y":0,"z":0,"direct":true}`, `{"x":10,"y":0,"z":0,"direct":true,"gm_cross":true}`, 1))
	if _, err := Build(p, Options{}); err == nil || !strings.Contains(err.Error(), "gm_cross") {
		t.Fatalf("%v", err)
	}
}

// TestGMCrossDirect: with GMCrossDirect a gm_cross waypoint is a move_direct row whose comment says it is not tried live, and a warning.
func TestGMCrossDirect(t *testing.T) {
	p := parsePlan(t, strings.Replace(small, `{"x":10,"y":0,"z":0,"direct":true}`, `{"x":10,"y":0,"z":0,"direct":true,"gm_cross":true}`, 1))
	res, err := Build(p, Options{Source: "test", GMCrossDirect: true})
	if err != nil {
		t.Fatal(err)
	}
	var got *StepRow
	for i, s := range res.Rows.Steps {
		if s.Leg == 0 && s.X == 10 {
			got = &res.Rows.Steps[i]
		}
	}
	if got == nil || got.Kind != KindMoveDirect || !strings.Contains(got.Comment, "gm_cross") {
		t.Fatalf("%+v", got)
	}
	if w := strings.Join(res.Warnings, "\n"); !strings.Contains(w, "gm_cross") || !strings.Contains(w, "not tried live") {
		t.Fatalf("warnings: %q", w)
	}
}

// TestApproachHops: with ApproachHops a pull more than 50 yd from the anchor before it gets the cutter's points as move rows before it,
// without the ones within 25 yd of the pack; the hop after a fight is measured from the fight; without the option nothing is cut.
func TestApproachHops(t *testing.T) {
	const js = `{"version":1,"name":"far","status":"live-checked (test)","map":43,"entrance":{"x":0,"y":0,"z":0},
 "legs":[{"name":"a","to":{"x":10,"y":0,"z":0},"pull_groups":[{"entries":[100],"positions":[{"x":100,"y":0,"z":0}]},{"entries":[101],"positions":[{"x":120,"y":0,"z":0}]}]}]}`
	cut := func(_ uint32, a, b dungeon.Point) ([]dungeon.Point, error) {
		var out []dungeon.Point
		for x := a.X + 40; x < b.X; x += 40 {
			out = append(out, dungeon.Point{X: x})
		}
		return out, nil
	}
	res, err := Build(parsePlan(t, js), Options{Source: "test", Cut: cut, ApproachHops: true})
	if err != nil {
		t.Fatal(err)
	}
	// 10 -> 100: points at 50 and 90; 90 is within 25 yd of the pack at 100 and goes; 100 -> 120 is 20 yd, nothing to cut.
	if got := strings.Join(kinds(res.Rows, 0), " "); got != "move move pull pull" {
		t.Fatalf("%s", got)
	}
	if s := res.Rows.Steps[1]; s.X != 50 || s.Comment != "approach path" {
		t.Fatalf("%+v", s)
	}
	res, err = Build(parsePlan(t, js), Options{Source: "test", Cut: cut})
	if err != nil || strings.Join(kinds(res.Rows, 0), " ") != "move pull pull" {
		t.Fatalf("%v %v", err, kinds(res.Rows, 0))
	}
}

// TestRouteAroundAvoid: a plan with an avoid box asks Route for every hop between anchors (the entrance, moves, pulls) and puts the
// points it returns in as move rows; an error from Route fails the export; a plan without a box never calls it.
func TestRouteAroundAvoid(t *testing.T) {
	const js = `{"version":1,"name":"lava","status":"live-checked (test)","map":389,"entrance":{"x":0,"y":0,"z":0},
 "avoid":[{"name":"basin","min":{"x":20,"y":-5,"z":-9},"max":{"x":30,"y":5,"z":-1},"why":"lava"}],
 "legs":[{"name":"a","to":{"x":10,"y":0,"z":0},"pull_groups":[{"entries":[100],"positions":[{"x":40,"y":20,"z":0}]}]}]}`
	var calls [][2]dungeon.Point
	route := func(_ uint32, a, b dungeon.Point) ([]dungeon.Point, error) {
		calls = append(calls, [2]dungeon.Point{a, b})
		if b.X == 40 {
			return []dungeon.Point{{X: 25, Y: 20, Z: 0}}, nil
		}
		return nil, nil
	}
	res, err := Build(parsePlan(t, js), Options{Source: "test", Route: route})
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(kinds(res.Rows, 0), " "); got != "move move pull" {
		t.Fatalf("%s", got)
	}
	if len(calls) != 2 || calls[1][0].X != 10 {
		t.Fatalf("calls %+v", calls)
	}
	if s := res.Rows.Steps[1]; s.X != 25 || s.Seq != 1 || !strings.Contains(s.Comment, "avoid box") {
		t.Fatalf("%+v", s)
	}
	if w := strings.Join(res.Warnings, "\n"); !strings.Contains(w, "1 via points added") {
		t.Fatalf("warnings: %q", w)
	}
	if _, err := Build(parsePlan(t, js), Options{Source: "test", Route: func(uint32, dungeon.Point, dungeon.Point) ([]dungeon.Point, error) {
		return nil, errors.New("no safe path")
	}}); err == nil || !strings.Contains(err.Error(), "no safe path") {
		t.Fatalf("%v", err)
	}
	n := 0
	if _, err := Build(parsePlan(t, strings.Replace(js, `"avoid":[{"name":"basin","min":{"x":20,"y":-5,"z":-9},"max":{"x":30,"y":5,"z":-1},"why":"lava"}],`, ``, 1)), Options{Source: "test", Route: func(uint32, dungeon.Point, dungeon.Point) ([]dungeon.Point, error) { n++; return nil, nil }}); err != nil || n != 0 {
		t.Fatalf("%v, %d calls", err, n)
	}
}

// TestApproachAndHold: a pull group's and a boss's approach points are move rows before their row; a hold is a hold row in place of the
// pull or boss (instance data, DONE, flags 40, the event's entries), the move straight before it to its spot is left out, the game object
// wait straight after it waits at most 60 s, and a ritual's row carries its participants.
func TestApproachAndHold(t *testing.T) {
	js := `{"version":1,"name":"events","status":"live-checked","map":70,"entrance":{"x":0,"y":0,"z":0},"legs":[
	 {"name":"a","to":{"x":10,"y":0,"z":0},
	  "pull_groups":[{"entries":[5],"positions":[{"x":50,"y":0,"z":0}],"approach":[{"x":20,"y":0,"z":0},{"x":25,"y":1,"z":0}]}],
	  "steps":[{"ritual":{"entry":130511,"position":{"x":30,"y":0,"z":0},"participants":3,"stand":{"x":29,"y":0,"z":0}}}]},
	 {"name":"b","to":{"x":40,"y":0,"z":0},
	  "pull_groups":[{"entries":[4857,4857],"positions":[{"x":41,"y":2,"z":0},{"x":45,"y":0,"z":0}],"radius":30,
	   "hold":{"instance_data":1,"spot":{"x":40,"y":0,"z":0},"radius":30,"timeout_s":600}}],
	  "steps":[{"wait_for":{"gameobject":{"entry":124367,"near":{"x":60,"y":0,"z":0},"state":"active"},"timeout_s":600}}]},
	 {"name":"c","to":{"x":60,"y":0,"z":0},
	  "boss":{"entry":2748,"position":{"x":70,"y":0,"z":0},"approach":[{"x":62,"y":0,"z":0}],
	   "hold":{"instance_data":2,"spot":{"x":66,"y":0,"z":0},"radius":45,"timeout_s":600,"entries":[7309,7077,7076,10120]}}}]}`
	r := build(t, parsePlan(t, js))
	if got := strings.Join(kinds(r, 0), " "); got != "move move move pull ritual" {
		t.Fatalf("leg 0 %s", got)
	}
	if s := r.Steps[1]; s.X != 20 || s.Comment != "pull group 0 approach" {
		t.Fatalf("approach row %+v", s)
	}
	if s := r.Steps[4]; s.Kind != KindRitual || s.Param != 3 {
		t.Fatalf("ritual row %+v", s)
	}
	if got := strings.Join(kinds(r, 1), " "); got != "hold wait_object" {
		t.Fatalf("leg 1 %s (the move to the hold's own spot is left out)", got)
	}
	var hold, wait, boss StepRow
	for _, s := range r.Steps {
		switch {
		case s.Leg == 1 && s.Kind == KindHold:
			hold = s
		case s.Leg == 1 && s.Kind == KindWaitObject:
			wait = s
		case s.Leg == 2 && s.Kind == KindHold:
			boss = s
		}
	}
	if hold.Seq != 0 || hold.Entry != 1 || hold.Param != 3 || hold.Flags != 40 || hold.TimeoutS != 600 || hold.Radius != 30 || fmt.Sprint(hold.Entries) != "[4857]" || hold.X != 40 {
		t.Fatalf("keepers hold %+v", hold)
	}
	if wait.TimeoutS != 60 || wait.Seq != 1 {
		t.Fatalf("door wait after the hold %+v", wait)
	}
	if got := strings.Join(kinds(r, 2), " "); got != "move move hold" {
		t.Fatalf("leg 2 %s", got)
	}
	if boss.Entry != 2 || boss.Flags != 40 || boss.Radius != 45 || fmt.Sprint(boss.Entries) != "[2748 7309 7077 7076 10120]" || boss.X != 66 {
		t.Fatalf("boss hold %+v", boss)
	}
	back, err := Parse(WriteSQL([]Rows{r}))
	if err != nil {
		t.Fatal(err)
	}
	if err := Equal([]Rows{r}, back); err != nil {
		t.Fatal(err)
	}
}

// TestLongHopStartsAtTheLastFight: the hop to a leg's first move is measured from the boss (or pack) the party fought last, not from the
// last move before that fight: Uldaman's export of dd05724 walked the party from the scorpid pit to the Sentinel's hall and back because
// leg 6's `to` was measured from leg 5's ramp top.
func TestLongHopStartsAtTheLastFight(t *testing.T) {
	js := `{"version":1,"name":"back","status":"live-checked","map":70,"entrance":{"x":0,"y":0,"z":0},"legs":[
	 {"name":"a","to":{"x":0,"y":0,"z":0},"pull_groups":[{"entries":[3],"positions":[{"x":40,"y":0,"z":0}],"approach":[{"x":30,"y":0,"z":0}]}],
	  "boss":{"entry":1,"position":{"x":100,"y":0,"z":0},"approach":[{"x":60,"y":0,"z":0}]}},
	 {"name":"b","to":{"x":110,"y":0,"z":0},"boss":{"entry":2,"position":{"x":120,"y":0,"z":0}}}]}`
	res, err := Build(parsePlan(t, js), Options{Cut: func(_ uint32, from, to dungeon.Point) ([]dungeon.Point, error) {
		t.Fatalf("cut from %v to %v: the boss at 100 is 10 yd from the next move", from, to)
		return nil, nil
	}})
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(kinds(res.Rows, 1), " "); got != "move boss" {
		t.Fatalf("leg 1 %s", got)
	}
}

// The run leader numbers the rows it loaded: an optional leg takes no leg number and its legs close up, and a pull group starts at its
// first row (its approach move, else its pull row). An observer's from_leg / from_pull_group map through these.
func TestLegNumberAndGroupStep(t *testing.T) {
	const groups = `{"version":1,"name":"Groups","status":"live-checked (test)","map":70,"entrance":{"x":0,"y":0,"z":0},"legs":[
	 {"name":"first","to":{"x":10,"y":0,"z":0},"pull_groups":[
	  {"entries":[100],"positions":[{"x":20,"y":0,"z":0}],"approach":[{"x":15,"y":0,"z":0}]},
	  {"entries":[101],"positions":[{"x":25,"y":0,"z":0}]}]},
	 {"name":"gong","to":{"x":0,"y":0,"z":0},"optional":{"item":9240,"name":"Mallet"},"steps":[{"use_object":{"entry":900,"near":{"x":2,"y":0,"z":0},"requires_item":{"entry":9240}}}],"boss":{"entry":7,"position":{"x":5,"y":0,"z":0}}},
	 {"name":"third","to":{"x":30,"y":0,"z":0},"pull_groups":[{"entries":[102],"positions":[{"x":40,"y":0,"z":0}]}]}]}`
	p := parsePlan(t, groups)
	r := build(t, p)
	if r.StepCount() != 6 || len(r.Steps) != 6 {
		t.Fatalf("%d steps: %+v", r.StepCount(), r.Steps)
	}
	for leg, want := range map[int]int{0: 0, 2: 1} {
		if got, ok := r.LegNumber(p, leg); !ok || got != want {
			t.Errorf("leg %d: number %d ok %v, want %d", leg, got, ok, want)
		}
	}
	if _, ok := r.LegNumber(p, 1); ok {
		t.Error("the optional leg took a number")
	}
	for _, c := range []struct{ leg, group, want int }{
		{0, 0, 2}, // the move to the leg (1), then the group's approach move
		{0, 1, 4}, // the second group's pull row
		{2, 0, 6}, // second leg written, its pull row after the leg's move
	} {
		got, ok := r.GroupStep(p, c.leg, c.group)
		if !ok || got != c.want {
			t.Errorf("leg %d group %d: step %d ok %v, want %d", c.leg, c.group, got, ok, c.want)
		}
	}
	if _, ok := r.GroupStep(p, 1, 0); ok {
		t.Error("the optional leg has no group step")
	}
	if _, ok := r.GroupStep(p, 0, 2); ok {
		t.Error("a group the leg does not have has a step")
	}
}
