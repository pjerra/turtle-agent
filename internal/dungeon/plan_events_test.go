package dungeon

import (
	"bytes"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/pjerra/turtle-agent/internal/packet"
)

// The event steps of the Zul'Farrak events design (TortoiseBots #984): area_trigger, wait_for unit and the leg's companions_loot.

const triggerStep = `{"area_trigger":{"id":962,"center":{"x":10,"y":0,"z":0},"radius":10,"stand":{"x":4,"y":0,"z":0},"expect":"unit 7271 hostile"}}`

func eventPlan(steps, legExtra string) string {
	return `{"version":1,"name":"t","map":1,"entrance":{"x":0,"y":0,"z":0},"legs":[{"to":{"x":0,"y":0,"z":0},` + legExtra + `"steps":[` + steps + `]}]}`
}

func TestAreaTriggerStepParsesAndKeepsItsForm(t *testing.T) {
	p := mustPlan(t, eventPlan(triggerStep+`,{"area_trigger":{"id":5,"center":{"x":0,"y":0,"z":0},"radius":3,"stand":{"x":1,"y":0,"z":0},"by":"agent","expect":"unit 1,2 dead","timeout_s":20}}`, ""))
	a, b := p.Legs[0].Steps[0].AreaTrigger, p.Legs[0].Steps[1].AreaTrigger
	// The defaults are filled in: by auto, the usual timeout; the expect is parsed and written the same way.
	if a.ID != 962 || a.By != TriggerAuto || a.TimeoutS != DefaultTimeoutS || a.Radius != 10 || a.Stand != (Point{4, 0, 0}) || a.Expect != "unit 7271 hostile" ||
		a.ExpectUnit == nil || fmt.Sprint(a.ExpectUnit.Entries) != "[7271]" || a.ExpectUnit.State != UnitHostile {
		t.Fatalf("%+v", a)
	}
	if b.By != TriggerAgent || b.TimeoutS != 20 || b.Expect != "unit 1,2 dead" || fmt.Sprint(b.ExpectUnit.Entries) != "[1 2]" {
		t.Fatalf("%+v", b)
	}
	// Written out and read again, the plan is the same (the file form is what Marshal writes).
	out, err := Marshal(p)
	if err != nil {
		t.Fatal(err)
	}
	q, err := Parse("again.json", out)
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	again, _ := Marshal(q)
	if !bytes.Equal(out, again) || !strings.Contains(string(out), `"expect": "unit 7271 hostile"`) || !strings.Contains(string(out), `"center": {"x": 10, "y": 0, "z": 0}`) {
		t.Fatalf("%s", out)
	}
}

func TestAreaTriggerStepErrors(t *testing.T) {
	trig := func(f string) string { return `{"area_trigger":` + f + `}` }
	const c, s = `"center":{"x":10,"y":0,"z":0},`, `"stand":{"x":4,"y":0,"z":0},`
	for name, tc := range map[string]struct{ step, want string }{
		"no id":            {trig(`{` + c + `"radius":10,` + s + `"expect":"unit 1 seen"}`), "steps[0].area_trigger.id: required"},
		"no centre":        {trig(`{"id":1,"radius":10,` + s + `"expect":"unit 1 seen"}`), "area_trigger.center: required"},
		"no stand":         {trig(`{"id":1,` + c + `"radius":10,"expect":"unit 1 seen"}`), "area_trigger.stand: required"},
		"no radius":        {trig(`{"id":1,` + c + s + `"expect":"unit 1 seen"}`), "area_trigger.radius: required"},
		"huge radius":      {trig(`{"id":1,` + c + `"radius":9999,` + s + `"expect":"unit 1 seen"}`), "area_trigger.radius: 9999 yards is outside"},
		"stand outside":    {trig(`{"id":1,` + c + `"radius":3,` + s + `"expect":"unit 1 seen"}`), "area_trigger.stand: is 6.0 yd from the centre, outside the radius 3"},
		"bad by":           {trig(`{"id":1,` + c + `"radius":10,` + s + `"by":"lead","expect":"unit 1 seen"}`), `area_trigger.by: "lead" is none of auto, bot, agent`},
		"no expect":        {trig(`{"id":1,` + c + `"radius":10,"stand":{"x":4,"y":0,"z":0}}`), "area_trigger.expect: required"},
		"expect grammar":   {trig(`{"id":1,` + c + `"radius":10,` + s + `"expect":"7271 hostile"}`), `area_trigger.expect: "7271 hostile": write it as "unit <entry>`},
		"expect state":     {trig(`{"id":1,` + c + `"radius":10,` + s + `"expect":"unit 7271 angry"}`), `"angry" is none of seen, hostile, dead, gone`},
		"expect entry":     {trig(`{"id":1,` + c + `"radius":10,` + s + `"expect":"unit 0 seen"}`), `"0" is no creature entry`},
		"timeout":          {trig(`{"id":1,` + c + `"radius":10,` + s + `"expect":"unit 1 seen","timeout_s":601}`), "area_trigger.timeout_s: 601 s is outside 1-600"},
		"unknown field":    {trig(`{"id":1,"colour":1}`), `unknown field "colour"`},
		"two step kinds":   {`{"area_trigger":{},"use_object":{"entry":1}}`, "has more than one of"},
		"trigger + loot":   {`{"loot_object":{"entry":1},"area_trigger":{}}`, "has more than one of"},
		"wait unit empty":  {`{"wait_for":{"unit":{}}}`, "wait_for.unit.entries: needs at least one entry"},
		"wait unit state":  {`{"wait_for":{"unit":{"entries":[1],"state":"asleep"}}}`, `wait_for.unit.state: "asleep" is none of seen, hostile, dead, gone`},
		"wait unit radius": {`{"wait_for":{"unit":{"entries":[1],"state":"seen","radius":9}}}`, "wait_for.unit.radius: only means something with near"},
		"wait unit big":    {`{"wait_for":{"unit":{"entries":[1],"state":"seen","near":{"x":0,"y":0,"z":0},"radius":9999}}}`, "wait_for.unit.radius: 9999 yards is outside"},
		"wait unit + gone": {`{"wait_for":{"unit":{"entries":[1],"state":"seen"},"hostiles_gone":{"radius":5}}}`, "has unit and another condition"},
	} {
		_, err := Parse("t.json", []byte(eventPlan(tc.step, "")))
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: want %q, got %v", name, tc.want, err)
		}
	}
}

func TestWaitForUnitParsesAndKeepsItsForm(t *testing.T) {
	p := mustPlan(t, eventPlan(`{"wait_for":{"unit":{"entries":[7271,7285],"near":{"x":1,"y":2,"z":3},"radius":40,"state":"dead"},"timeout_s":90,"fail_text":"he did not die"}},{"wait_for":{"unit":{"entries":[7],"state":"seen"}}}`, ""))
	w, v := p.Legs[0].Steps[0].WaitFor, p.Legs[0].Steps[1].WaitFor
	if u := w.Unit; w.TimeoutS != 90 || fmt.Sprint(u.Entries) != "[7271 7285]" || *u.Near != (Point{1, 2, 3}) || u.Radius != 40 || u.State != UnitDead {
		t.Fatalf("%+v %+v", w, w.Unit)
	}
	if u := v.Unit; v.TimeoutS != DefaultTimeoutS || u.Near != nil || u.radius() != DefaultUnitRadius {
		t.Fatalf("%+v %+v", v, v.Unit)
	}
	if got := describeWait(w); got != "unit 7271,7285 dead within 40 yd of 1 2 3" {
		t.Errorf("%q", got)
	}
	out, err := Marshal(p)
	if err != nil {
		t.Fatal(err)
	}
	q, err := Parse("again.json", out)
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	if again, _ := Marshal(q); !bytes.Equal(out, again) || !strings.Contains(string(out), `"entries": [7271, 7285]`) {
		t.Fatalf("%s", out)
	}
}

func TestLegCompanionsLootParses(t *testing.T) {
	p := mustPlan(t, eventPlan(`{"wait_for":{"hostiles_gone":{"radius":5}}}`, `"companions_loot":false,`))
	if c := p.Legs[0].CompanionsLoot; c == nil || *c {
		t.Fatalf("%v", c)
	}
	out, _ := Marshal(p)
	if !strings.Contains(string(out), `"companions_loot": false`) {
		t.Fatalf("%s", out)
	}
	if q := mustPlan(t, eventPlan(`{"wait_for":{"hostiles_gone":{"radius":5}}}`, "")); q.Legs[0].CompanionsLoot != nil {
		t.Fatal("absent stays absent")
	}
}

func TestUnitWaitHolds(t *testing.T) {
	at := Point{100, 0, 0}
	alive := unitSight{guid: 1, entry: 7271, at: at}
	hostile := unitSight{guid: 1, entry: 7271, at: at, hostile: true}
	corpse := unitSight{guid: 1, entry: 7271, at: at, dead: true}
	other := unitSight{guid: 2, entry: 5, at: at, hostile: true}
	far := unitSight{guid: 3, entry: 7271, at: Point{500, 0, 0}, hostile: true}
	wait := func(state string) *UnitWait { return &UnitWait{Entries: []uint32{7271}, State: state} }
	for _, tc := range []struct {
		name   string
		state  string
		sights []unitSight
		known  bool
		want   bool
	}{
		{"seen: friendly", UnitSeen, []unitSight{alive}, false, true},
		{"seen: other entry", UnitSeen, []unitSight{other}, false, false},
		{"seen: corpse", UnitSeen, []unitSight{corpse}, false, false},
		{"hostile: friendly is not", UnitHostile, []unitSight{alive}, false, false},
		{"hostile: yes", UnitHostile, []unitSight{hostile, other}, false, true},
		{"hostile: other entry", UnitHostile, []unitSight{other}, false, false},
		{"dead: corpse", UnitDead, []unitSight{corpse}, false, true},
		{"dead: known", UnitDead, nil, true, true},
		{"dead: alive wins", UnitDead, []unitSight{corpse, alive}, true, false},
		{"dead: nothing", UnitDead, nil, false, false},
		{"gone: nothing in view", UnitGone, nil, false, true},
		{"gone: a corpse is not a living unit", UnitGone, []unitSight{corpse}, false, true},
		{"gone: alive", UnitGone, []unitSight{alive}, false, false},
	} {
		if got := wait(tc.state).holds(tc.sights, tc.known); got != tc.want {
			t.Errorf("%s: %v", tc.name, got)
		}
	}
	// Near narrows to a radius (the default without one); a unit outside is not counted.
	near := &UnitWait{Entries: []uint32{7271}, State: UnitHostile, Near: &Point{0, 0, 0}, Radius: 60}
	if near.holds([]unitSight{hostile}, false) || !(&UnitWait{Entries: []uint32{7271}, State: UnitHostile, Near: &at, Radius: 5}).holds([]unitSight{hostile}, false) {
		t.Error("near and radius")
	}
	if (&UnitWait{Entries: []uint32{7271}, State: UnitSeen, Near: &Point{0, 0, 0}}).holds([]unitSight{alive}, false) { // 100 yd away, default 50
		t.Error("the default radius")
	}
	if !(&UnitWait{Entries: []uint32{7271}, State: UnitGone, Near: &Point{0, 0, 0}, Radius: 60}).holds([]unitSight{far}, false) {
		t.Error("a unit beyond the radius is gone")
	}
}

// unitWorld is a fakeWorld that also tells unit waits.
type unitWorld struct {
	fakeWorld
	sights []unitSight
}

func (w *unitWorld) UnitHolds(u *UnitWait) bool { return u.holds(w.sights, false) }

func TestStepperTakesTheAreaTriggerStep(t *testing.T) {
	p := mustPlan(t, eventPlan(triggerStep, ""))
	w := &unitWorld{}
	s := NewStepper(p, w, nil)
	a := s.Next() // the player stands at the leg's To: no walk first
	if a.Kind != ActTrigger || a.Trigger == nil || a.Trigger.ID != 962 || a.Step != 0 || !strings.Contains(a.Label, "area trigger 962 (by auto, expect unit 7271 hostile)") {
		t.Fatalf("%+v", a)
	}
	s.Complete()
	if a = s.Next(); a.Kind != ActDone {
		t.Fatalf("after the step: %+v", a)
	}
}

func TestStepperWaitsForAUnit(t *testing.T) {
	p := mustPlan(t, eventPlan(`{"wait_for":{"unit":{"entries":[7271],"state":"hostile"},"timeout_s":5,"fail_text":"the trigger did not run"}}`, ""))
	w := &unitWorld{}
	clk := &fakeClock{t: time.Unix(0, 0)}
	s := NewStepper(p, w, clk.now)
	if a := s.Next(); a.Kind != ActWait || a.Remaining != 5*time.Second || !strings.Contains(a.Label, "wait for unit 7271 hostile") {
		t.Fatalf("%+v", a)
	}
	clk.t = clk.t.Add(2 * time.Second)
	w.sights = []unitSight{{guid: packet.GUID(9), entry: 7271, hostile: true}}
	if a := s.Next(); a.Kind != ActDone {
		t.Fatalf("it holds: %+v", a)
	}
	// Timing out names the wait and the plan's reason.
	s = NewStepper(p, &unitWorld{}, clk.now)
	s.Next()
	clk.t = clk.t.Add(6 * time.Second)
	a := s.Next()
	if a.Kind != ActFailed || !strings.Contains(a.Err, "waited 5 s for unit 7271 hostile: the trigger did not run") {
		t.Fatalf("%+v", a)
	}
	// A world that cannot see units fails the step at once, with that reason, instead of waiting out the timeout.
	s = NewStepper(p, &fakeWorld{}, clk.now)
	if a := s.Next(); a.Kind != ActFailed || !strings.Contains(a.Err, "this driver does not see units") {
		t.Fatalf("%+v", a)
	}
}

// The plan check wants the stand inside the zone and the boss within reach of it.
func TestPlanCheckAreaTrigger(t *testing.T) {
	zone := func(stand Point) Leg {
		return Leg{Name: "z", To: Point{0, 0, 0},
			Steps: []Step{{AreaTrigger: &AreaTrigger{ID: 962, Center: Point{10, 0, 0}, Radius: 10, Stand: stand, By: TriggerAuto, Expect: "unit 7 hostile", ExpectUnit: &UnitWait{Entries: []uint32{7}, State: UnitHostile}, TimeoutS: 15}}},
			Boss:  &Boss{Entry: 7, Position: Point{15, 0, 0}}}
	}
	res := checkLegs(t, fakeNav{}, 0, zone(Point{4, 0, 0}))
	if !res.OK() {
		t.Fatalf("%s", res.Summary())
	}
	// The stand is checked on the mesh like the other stands: it counts as a point and is walked to.
	res = checkLegs(t, fakeNav{off: map[Point]bool{{4, 0, 0}: true}}, 0, zone(Point{4, 0, 0}))
	if got, want := issueReasons(res.Issues), "0/steps[0].area_trigger.stand/0/off the mesh"; got != want {
		t.Errorf("%q, want %q", got, want)
	}
	// Outside the radius: nobody who stands there takes the trigger.
	res = checkLegs(t, fakeNav{}, 0, zone(Point{-2, 0, 0}))
	if got, want := issueReasons(res.Issues), "0/steps[0].area_trigger.stand/0/outside the zone"; got != want || !strings.Contains(res.Summary(), "12.0 yd from the centre") {
		t.Errorf("%q, want %q: %s", got, want, res.Summary())
	}
	// A boss more than TriggerMaxStandToBoss from the stand: the trigger would start a fight the party is not at.
	far := zone(Point{4, 0, 0})
	far.Boss.Position = Point{60, 0, 0}
	res = checkLegs(t, fakeNav{}, 0, far)
	if got, want := issueReasons(res.Issues), "0/steps[0].area_trigger.stand/0/too far"; got != want || !strings.Contains(res.Summary(), "boss 7 stands 56.0 yd from the stand (at most 30)") {
		t.Errorf("%q, want %q: %s", got, want, res.Summary())
	}
	// A leg with the trigger and no boss has nothing to measure.
	nb := zone(Point{4, 0, 0})
	nb.Boss = nil
	if res = checkLegs(t, fakeNav{}, 0, nb); !res.OK() {
		t.Fatalf("%s", res.Summary())
	}
}

// A leg whose looting is off for every bot cannot also have the leader loot its boss for an item: refused when the plan loads.
func TestLegCompanionsLootFalseRefusesABossLootItem(t *testing.T) {
	const bossLeg = `{"version":1,"name":"t","map":1,"entrance":{"x":0,"y":0,"z":0},"legs":[{"to":{"x":0,"y":0,"z":0},"companions_loot":false,"boss":{"entry":4,"position":{"x":1,"y":0,"z":0},"loot_item":{"entry":9,"name":"x"}}}]}`
	if _, err := Parse("t.json", []byte(bossLeg)); err == nil || !strings.Contains(err.Error(), "legs[0].boss.loot_item: the leg has companions_loot false") {
		t.Fatalf("%v", err)
	}
	// The boss's own companions_loot false (the fight only) is the way for a boss whose item the plan takes.
	ok := strings.Replace(strings.Replace(bossLeg, `"companions_loot":false,"boss"`, `"boss"`, 1), `"loot_item"`, `"companions_loot":false,"loot_item"`, 1)
	if _, err := Parse("t.json", []byte(ok)); err != nil {
		t.Fatal(err)
	}
}

// The stand of a loot_object step (a chest) must be within the use range of the object's position: the plan check says so. A use_object (a gong,
// a door) is used from farther and is not measured.
func TestPlanCheckObjectStandWithinUseRange(t *testing.T) {
	step := func(stand Point) Leg {
		near := Point{10, 0, 0}
		return Leg{Name: "g", To: Point{0, 0, 0}, Steps: []Step{{LootObject: &LootObject{ObjectRef: ObjectRef{Entry: 128403, Near: &near, Range: 2}, Stand: &stand}}, {UseObject: &UseObject{ObjectRef: ObjectRef{Entry: 5, Near: &near}, Stand: &stand}}}}
	}
	if res := checkLegs(t, fakeNav{}, 0, step(Point{7, 0, 0})); !res.OK() {
		t.Fatalf("%s", res.Summary())
	}
	res := checkLegs(t, fakeNav{}, 0, step(Point{4, 0, 0}))
	if got, want := issueReasons(res.Issues), "0/steps[0].loot_object.stand/0/too far"; got != want || !strings.Contains(res.Summary(), "the stand is 6.0 yd from game object 128403 at 10.0 0.0 0.0: the use range is 4.5 yd") {
		t.Fatalf("%q: %s", got, res.Summary())
	}
}

// A kill_first entry is never crowd-controlled: the loader refuses one that is also in cc_priority or cc_always.
func TestPlanRefusesAKillFirstEntryInTheCCLists(t *testing.T) {
	const tmpl = `{"version":1,"name":"t","map":1,"entrance":{"x":0,"y":0,"z":0},"cc_priority":%s,"cc_always":%s,"kill_first":[5650,7247],"legs":[{"to":{"x":0,"y":0,"z":0},"steps":[{"wait_for":{"hostiles_gone":{"radius":5}}}]}]}`
	for _, c := range [][2]string{{"[7246,7247]", "[]"}, {"[7246]", "[5650]"}} {
		if _, err := Parse("t.json", []byte(fmt.Sprintf(tmpl, c[0], c[1]))); err == nil || !strings.Contains(err.Error(), "is also in cc_priority or cc_always") {
			t.Errorf("%v: %v", c, err)
		}
	}
	if _, err := Parse("t.json", []byte(fmt.Sprintf(tmpl, "[7246]", "[7246]"))); err != nil {
		t.Fatal(err)
	}
}

// A grave's spawn guid parses, keeps its form and needs its entry.
func TestObjectSpawnGuidParses(t *testing.T) {
	p := mustPlan(t, eventPlan(`{"loot_object":{"entry":128403,"spawn":27134,"near":{"x":1,"y":2,"z":3},"range":2}}`, ""))
	if lo := p.Legs[0].Steps[0].LootObject; lo.Spawn != 27134 {
		t.Fatalf("%+v", lo)
	}
	out, _ := Marshal(p)
	if !strings.Contains(string(out), `"spawn": 27134`) {
		t.Fatalf("%s", out)
	}
	if _, err := Parse("t.json", []byte(eventPlan(`{"loot_object":{"spawn":5,"near":{"x":1,"y":2,"z":3}}}`, ""))); err == nil || !strings.Contains(err.Error(), "loot_object.spawn: needs the entry as well") {
		t.Fatalf("%v", err)
	}
}

// A pack that stands near but whose way is a long detour (another layer) leaves the tank no firing position: the plan check warns (it never
// fails a plan: the drafts of other dungeons have many).
func TestPlanCheckPullDetour(t *testing.T) {
	leg := Leg{Name: "p", To: Point{0, 0, 0}, PullGroups: []PullGroup{{Entries: []uint32{1}, Positions: []Point{{30, 0, 0}}}}}
	if res := checkLegs(t, fakeNav{}, 0, leg); !res.OK() || len(res.Warnings) != 0 {
		t.Fatalf("%s", res.Summary())
	}
	// 30 yd in a line, 120 yd by the path: a detour (more than twice the line plus 25 yd).
	res := checkLegs(t, fakeNav{detour: map[Point]float32{{30, 0, 0}: 120}}, 0, leg)
	if got, want := issueReasons(res.Warnings), "0/pull group look/1/detour"; got != want || !res.OK() || !strings.Contains(res.WarningSummary(), "the pack is 30 yd away but the way to it is 120 yd") {
		t.Fatalf("%q: ok %v %s", got, res.OK(), res.WarningSummary())
	}
	// 30 yd in a line, 70 yd by the path: still fine (under 2 x 30 + 25).
	if res := checkLegs(t, fakeNav{detour: map[Point]float32{{30, 0, 0}: 80}}, 0, leg); !res.OK() || len(res.Warnings) != 0 {
		t.Fatalf("%s", res.Summary())
	}
	// A pack farther than the near limit is not judged.
	far := Leg{Name: "p", To: Point{0, 0, 0}, PullGroups: []PullGroup{{Entries: []uint32{1}, Positions: []Point{{60, 0, 0}}}}}
	if res := checkLegs(t, fakeNav{detour: map[Point]float32{{60, 0, 0}: 400}}, 0, far); !res.OK() || len(res.Warnings) != 0 {
		t.Fatalf("%s", res.Summary())
	}
}

// A run that starts in a leg whose trigger was taken already (Zum'rah hostile, or dead) goes to the trigger step at once: no walk, no pulls, no
// grave steps before it.
func TestStepperSkipsToAFiredTrigger(t *testing.T) {
	steps := `{"loot_object":{"entry":128403,"near":{"x":5,"y":0,"z":0},"stand":{"x":3,"y":0,"z":0}}},` + triggerStep
	p := mustPlan(t, `{"version":1,"name":"t","map":1,"entrance":{"x":0,"y":0,"z":0},"legs":[{"to":{"x":50,"y":0,"z":0},"steps":[`+steps+`]}]}`)
	w := &unitWorld{}
	s := NewStepper(p, w, nil)
	if _, ok := s.SkipToFiredTrigger(); ok {
		t.Fatal("nothing fired yet")
	}
	w.sights = []unitSight{{guid: 5, entry: 7271, hostile: true}}
	step, ok := s.SkipToFiredTrigger()
	if !ok || step != 1 {
		t.Fatalf("%d %v", step, ok)
	}
	if a := s.Next(); a.Kind != ActTrigger || a.Step != 1 {
		t.Fatalf("%+v", a)
	}
	// Dead counts too (the world sees a corpse).
	s = NewStepper(p, w, nil)
	w.sights = []unitSight{{guid: 5, entry: 7271, dead: true}}
	if step, ok := s.SkipToFiredTrigger(); !ok || step != 1 {
		t.Fatalf("dead: %d %v", step, ok)
	}
}
