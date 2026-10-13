package brain

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/pjerra/turtle-agent/internal/events"
	"github.com/pjerra/turtle-agent/internal/objects"
)

// A test strategy: actions that only record that they ran.
type log struct{ ran []string }

func (l *log) act(name string, pre, alt []string) *Action {
	return &Action{Name: name, Pre: pre, Alt: alt, Do: func(ctx context.Context, c *Ctx) error {
		l.ran = append(l.ran, name)
		return nil
	}}
}

func always(string) Trigger { return Trigger{Name: "always", Active: func(*Ctx) bool { return true }} }

// engine builds a brain on strategies of the test's own (no base strategy, so nothing is busy).
func engine(t *testing.T, g *fakeGame, ss ...*Strategy) *rig {
	t.Helper()
	cfg := DefaultConfig()
	cfg.Now = g.clk.Now
	b, err := assemble(g, cfg, Params{}, ss, []string{"test"})
	if err != nil {
		t.Fatal(err)
	}
	return &rig{t: t, g: g, b: b}
}

func TestRelevanceOrderPicksTheHighestPossible(t *testing.T) {
	g, l := newFake(spFrostbolt), &log{}
	s := &Strategy{Name: "s", Actions: []*Action{l.act("low", nil, nil), l.act("mid", nil, nil), l.act("high", nil, nil)},
		Rules: []Rule{
			{Trigger: Trigger{Name: "a", Active: func(*Ctx) bool { return true }}, Do: []Next{{"low", RelDefault}, {"high", RelHigh}}},
			{Trigger: Trigger{Name: "b", Active: func(*Ctx) bool { return true }}, Do: []Next{{"mid", RelNormal}}},
			{Trigger: Trigger{Name: "off", Active: func(*Ctx) bool { return false }}, Do: []Next{{"low", RelRaid}}},
		}}
	r := engine(t, g, s)
	d := r.tick(time.Second)
	if d == nil || d.Action != "high" || d.Trigger != "a" || d.Rel != RelHigh || d.Result != ResultOK {
		t.Fatalf("decision %+v", d)
	}
	if len(l.ran) != 1 {
		t.Fatalf("one action per tick, ran %v", l.ran)
	}
	ev := g.events(events.KindBrainDecision)
	if len(ev) != 1 || ev[0].Data["action"] != "high" || ev[0].Data["result"] != "ok" || ev[0].Data["trigger"] != "a" || ev[0].Data["relevance"] != RelHigh {
		t.Fatalf("event %+v", ev)
	}
}

func TestDefaultsRunAtTheirBand(t *testing.T) {
	g, l := newFake(spFrostbolt), &log{}
	s := &Strategy{Name: "s", Actions: []*Action{l.act("idle_thing", nil, nil), l.act("urgent", nil, nil)},
		Default: []Next{{"idle_thing", RelIdle}},
		Rules:   []Rule{{Trigger: Trigger{Name: "u", Active: func(c *Ctx) bool { return c.Tick > 1 }}, Do: []Next{{"urgent", RelInterrupt}}}}}
	r := engine(t, g, s)
	if d := r.tick(time.Second); d.Action != "idle_thing" || d.Trigger != "default" {
		t.Fatalf("%+v", d)
	}
	if d := r.tick(time.Second); d.Action != "urgent" {
		t.Fatalf("%+v", d)
	}
}

func TestMultiplierVetoAndDamp(t *testing.T) {
	g, l := newFake(spFrostbolt), &log{}
	veto := true
	damp := float32(1)
	s := &Strategy{Name: "s", Actions: []*Action{l.act("big", nil, nil), l.act("small", nil, nil)},
		Rules: []Rule{{Trigger: always(""), Do: []Next{{"big", RelHigh}, {"small", RelDefault}}}},
		Mult: []Multiplier{{Name: "no_big", F: func(c *Ctx, a *Action, rel float32) float32 {
			if a.Name == "big" && veto {
				return 0
			}
			if a.Name == "big" {
				return damp
			}
			return 1
		}}}}
	r := engine(t, g, s)
	if d := r.tick(time.Second); d.Action != "small" {
		t.Fatalf("the vetoed action must not run: %+v", d)
	}
	// A damped relevance can fall below another action's.
	veto, damp = false, 0.1
	if d := r.tick(time.Second); d.Action != "small" {
		t.Fatalf("damped 20 -> 2 must lose to 5: %+v", d)
	}
	damp = 1
	if d := r.tick(time.Second); d.Action != "big" {
		t.Fatalf("%+v", d)
	}
}

func TestEverythingVetoedIsIdleWithTheReason(t *testing.T) {
	g, l := newFake(spFrostbolt), &log{}
	s := &Strategy{Name: "s", Actions: []*Action{l.act("a", nil, nil)},
		Rules: []Rule{{Trigger: always(""), Do: []Next{{"a", RelHigh}}}},
		Mult:  []Multiplier{{Name: "wait_for_tank", F: func(*Ctx, *Action, float32) float32 { return 0 }}}}
	r := engine(t, g, s)
	if d := r.tick(time.Second); d != nil || len(l.ran) != 0 {
		t.Fatalf("ran %v %v", d, l.ran)
	}
	if r.idle() != "vetoed:wait_for_tank" {
		t.Fatalf("idle %q", r.idle())
	}
	ev := g.events(events.KindBrainDecision)
	if len(ev) != 1 || ev[0].Data["result"] != "vetoed" || ev[0].Data["why"] != "wait_for_tank" || ev[0].Data["action"] != "a" || ev[0].Data["relevance"] != RelHigh {
		t.Fatalf("decision event %+v", ev)
	}
	if idle := g.events(events.KindBrainIdle); len(idle) != 1 || idle[0].Data["reason"] != "vetoed:wait_for_tank" {
		t.Fatalf("idle event %+v", idle)
	}
}

func TestRefusalsAreReportedOnceThenThrottled(t *testing.T) {
	g := newFake(spFrostbolt)
	s := &Strategy{Name: "s", Actions: []*Action{{Name: "a", Possible: func(*Ctx) bool { return false }, Do: func(context.Context, *Ctx) error { return nil }}},
		Rules: []Rule{{Trigger: always(""), Do: []Next{{"a", RelNormal}}}}}
	r := engine(t, g, s)
	for i := 0; i < 20; i++ { // 4 s of ticks
		r.tick(200 * time.Millisecond)
	}
	if n := len(g.events(events.KindBrainDecision)); n != 2 { // at 0.2 s and again after DecEvery (3 s)
		t.Fatalf("%d decision events for one steady refusal", n)
	}
	if n := len(g.events(events.KindBrainIdle)); n != 1 {
		t.Fatalf("%d idle events (IdleEvery is 10 s)", n)
	}
	ev := g.events(events.KindBrainDecision)[0]
	if ev.Data["result"] != "not_possible" || ev.Data["why"] != "condition" {
		t.Fatalf("%+v", ev)
	}
	r.tick(11 * time.Second)
	if n := len(g.events(events.KindBrainIdle)); n != 2 {
		t.Fatalf("the idle reason must be repeated after IdleEvery: %d", n)
	}
}

func TestNotUsefulFallsThroughWithoutNoise(t *testing.T) {
	g, l := newFake(spFrostbolt), &log{}
	useless := l.act("useless", nil, nil)
	useless.Useful = func(*Ctx) bool { return false }
	s := &Strategy{Name: "s", Actions: []*Action{useless, l.act("next", nil, nil)},
		Rules: []Rule{{Trigger: always(""), Do: []Next{{"useless", RelHigh}, {"next", RelNormal}}}}}
	r := engine(t, g, s)
	if d := r.tick(time.Second); d.Action != "next" {
		t.Fatalf("%+v", d)
	}
	if n := len(g.events(events.KindBrainDecision)); n != 1 {
		t.Fatalf("a useless action is not a decision: %d events", n)
	}
}

func TestPrerequisitesRunBeforeTheActionAndAlternativesInsteadOfIt(t *testing.T) {
	g, l := newFake(spFrostbolt), &log{}
	ready := false
	main := l.act("main", []string{"prep"}, []string{"plan_b"})
	main.Possible = func(*Ctx) bool { return ready }
	prepOK := true
	prep := l.act("prep", nil, nil)
	prep.Possible = func(*Ctx) bool { return prepOK }
	planB := l.act("plan_b", nil, nil)
	s := &Strategy{Name: "s", Actions: []*Action{main, prep, planB}, Rules: []Rule{{Trigger: always(""), Do: []Next{{"main", RelNormal}}}}}
	r := engine(t, g, s)

	if d := r.tick(time.Second); d == nil || d.Action != "prep" || d.Via != "pre" || d.Rel <= RelNormal {
		t.Fatalf("the prerequisite goes first: %+v", d)
	}
	prepOK = false
	if d := r.tick(time.Second); d == nil || d.Action != "plan_b" || d.Via != "alt" {
		t.Fatalf("then the alternative: %+v", d)
	}
	ready = true
	if d := r.tick(time.Second); d == nil || d.Action != "main" || d.Via != "" {
		t.Fatalf("the action itself once it is possible: %+v", d)
	}
	if got := strings.Join(l.ran, ","); got != "prep,plan_b,main" {
		t.Fatal(got)
	}
	ev := g.events(events.KindBrainDecision)
	if ev[0].Data["via"] != "pre" || ev[1].Data["via"] != "alt" {
		t.Fatalf("events %+v", ev)
	}
}

func TestChainsMayNotLoop(t *testing.T) {
	g := newFake(spFrostbolt)
	never := func(*Ctx) bool { return false }
	a := &Action{Name: "a", Possible: never, Pre: []string{"b"}, Do: func(context.Context, *Ctx) error { return nil }}
	b := &Action{Name: "b", Possible: never, Alt: []string{"a"}, Do: func(context.Context, *Ctx) error { return nil }}
	r := engine(t, g, &Strategy{Name: "s", Actions: []*Action{a, b}, Rules: []Rule{{Trigger: always(""), Do: []Next{{"a", RelNormal}}}}})
	if d := r.tick(time.Second); d != nil { // returns instead of recursing forever
		t.Fatalf("%+v", d)
	}
}

func TestAVetoedChainMemberIsSkipped(t *testing.T) {
	g, l := newFake(spFrostbolt), &log{}
	main := l.act("main", nil, []string{"banned", "ok"})
	main.Possible = func(*Ctx) bool { return false }
	s := &Strategy{Name: "s", Actions: []*Action{main, l.act("banned", nil, nil), l.act("ok", nil, nil)},
		Rules: []Rule{{Trigger: always(""), Do: []Next{{"main", RelNormal}}}},
		Mult: []Multiplier{{Name: "ban", F: func(c *Ctx, a *Action, rel float32) float32 {
			if a.Name == "banned" {
				return 0
			}
			return 1
		}}}}
	r := engine(t, g, s)
	if d := r.tick(time.Second); d == nil || d.Action != "ok" {
		t.Fatalf("%+v", d)
	}
}

func TestAGameErrorIsReportedAndHoldsTheSpell(t *testing.T) {
	g := newFake(spFrostbolt)
	g.castErr = errors.New("session closed")
	g.self.PowerType = 0
	s := &Strategy{Name: "s", Actions: []*Action{{Name: "bolt", Spell: "Frostbolt"}}, Rules: []Rule{{Trigger: always(""), Do: []Next{{"bolt", RelNormal}}}}}
	r := engine(t, g, s)
	d := r.tick(time.Second)
	if d == nil || d.Result != ResultError || d.Error != "session closed" || d.Spell != spFrostbolt {
		t.Fatalf("%+v", d)
	}
	if ev := g.events(events.KindBrainDecision); len(ev) != 1 || ev[0].Data["result"] != "error" || ev[0].Data["error"] != "session closed" || ev[0].SpellID != spFrostbolt {
		t.Fatalf("event %+v", ev)
	}
	// The spell is held for ErrorHold, so the brain does not retry on the next tick.
	g.castErr = nil
	if d := r.tick(200 * time.Millisecond); d != nil || !strings.Contains(r.idle(), "held:error") {
		t.Fatalf("%+v idle %q", d, r.idle())
	}
	if d := r.tick(1500 * time.Millisecond); d == nil || d.Result != ResultOK {
		t.Fatalf("after the hold: %+v", d)
	}
}

func TestACastSettlesBeforeTheNextOne(t *testing.T) {
	g := newFake(spFireBlast, spFrostNova)
	s := &Strategy{Name: "s", Actions: []*Action{{Name: "a", Spell: "Fire Blast"}, {Name: "b", Spell: "Frost Nova"}},
		Rules: []Rule{{Trigger: always(""), Do: []Next{{"a", RelNormal}, {"b", RelDefault}}}}}
	b := baseStrategy()
	cfg := DefaultConfig()
	cfg.Now = g.clk.Now
	br, err := assemble(g, cfg, Params{}, []*Strategy{b, s}, []string{"base", "s"})
	if err != nil {
		t.Fatal(err)
	}
	r := &rig{t: t, g: g, b: br}
	if d := r.tick(time.Second); d == nil || d.Action != "a" {
		t.Fatalf("%+v", d)
	}
	// 100 ms later the GCD still runs and the cast has not settled: nothing is sent.
	if d := r.tick(100 * time.Millisecond); d != nil || r.idle() != "vetoed:busy" {
		t.Fatalf("%+v idle %q", d, r.idle())
	}
	// The GCD ended: the other spell goes.
	if d := r.tick(1500 * time.Millisecond); d == nil || d.Action != "b" {
		t.Fatalf("%+v", d)
	}
}

func TestBusyMultiplierLetsAnInterruptThrough(t *testing.T) {
	g, l := newFake(spFrostbolt), &log{}
	g.slow = true
	g.st.CastStarted(spFrostbolt, 2600, 0) // a Frostbolt is being cast
	s := &Strategy{Name: "s", Actions: []*Action{l.act("filler", nil, nil), l.act("kick", nil, nil)},
		Rules: []Rule{{Trigger: always(""), Do: []Next{{"filler", RelNormal}, {"kick", RelInterrupt}}}}}
	br, _ := assemble(g, func() Config { c := DefaultConfig(); c.Now = g.clk.Now; return c }(), Params{}, []*Strategy{baseStrategy(), s}, nil)
	r := &rig{t: t, g: g, b: br}
	if d := r.tick(100 * time.Millisecond); d == nil || d.Action != "kick" {
		t.Fatalf("%+v", d)
	}
	if d := r.tick(100 * time.Millisecond); d == nil || d.Action != "kick" || len(l.ran) != 2 {
		t.Fatalf("the filler must not run while casting: %+v %v", d, l.ran)
	}
}

func TestValueCachesForItsTTL(t *testing.T) {
	g := newFake(spFrostbolt)
	n := 0
	tick := &Value[int]{Name: "per_tick", Calc: func(*Ctx) int { n++; return n }}
	slow := &Value[int]{Name: "slow", TTL: time.Second, Calc: func(*Ctx) int { n += 100; return n }}
	var gotTick, gotSlow []int
	s := &Strategy{Name: "s", Before: func(c *Ctx) {
		a, b := tick.Get(c), tick.Get(c) // twice in one tick: computed once
		if a != b {
			t.Errorf("per-tick value changed inside a tick: %d %d", a, b)
		}
		gotTick, gotSlow = append(gotTick, a), append(gotSlow, slow.Get(c))
	}}
	r := engine(t, g, s)
	r.tick(0)
	r.tick(400 * time.Millisecond)
	r.tick(400 * time.Millisecond)
	r.tick(400 * time.Millisecond) // 1.2 s since the first: the slow value is recomputed
	if gotSlow[0] != gotSlow[1] || gotSlow[1] != gotSlow[2] || gotSlow[3] == gotSlow[2] {
		t.Fatalf("slow %v", gotSlow)
	}
	if gotTick[0] == gotTick[1] {
		t.Fatalf("per-tick values must be fresh each tick: %v", gotTick)
	}
}

func TestUnknownActionNamesAreRefused(t *testing.T) {
	g := newFake()
	cfg := DefaultConfig()
	_, err := assemble(g, cfg, Params{}, []*Strategy{{Name: "s", Rules: []Rule{{Trigger: always(""), Do: []Next{{"ghost", 1}}}},
		Actions: []*Action{{Name: "a", Pre: []string{"phantom"}}}}}, []string{"s"})
	if err == nil || !strings.Contains(err.Error(), "s/always -> ghost") || !strings.Contains(err.Error(), "action a pre -> phantom") {
		t.Fatalf("%v", err)
	}
}

func TestAPanicInsideATickIsContained(t *testing.T) {
	g := newFake(spFrostbolt)
	boom := &Action{Name: "boom", Do: func(context.Context, *Ctx) error { panic("kaboom") }}
	r := engine(t, g, &Strategy{Name: "s", Actions: []*Action{boom}, Rules: []Rule{{Trigger: always(""), Do: []Next{{"boom", RelNormal}}}}})
	if d := r.tick(time.Second); d != nil || r.idle() != "panic" {
		t.Fatalf("%+v %q", d, r.idle())
	}
}

func TestNothingHappensUntilTheSpellbookAndPlayerAreKnown(t *testing.T) {
	g, l := newFake(), &log{}
	r := engine(t, g, &Strategy{Name: "s", Actions: []*Action{l.act("a", nil, nil)}, Rules: []Rule{{Trigger: always(""), Do: []Next{{"a", RelNormal}}}}})
	g.selfOK = false
	if d := r.tick(time.Second); d != nil || r.idle() != "not_ready" {
		t.Fatalf("%+v %q", d, r.idle())
	}
	g.selfOK = true
	g.self.Health = 0 // a ghost or a corpse does nothing
	if d := r.tick(time.Second); d != nil || r.idle() != "dead" {
		t.Fatalf("%+v %q", d, r.idle())
	}
	g.self.Health = 800
	if d := r.tick(time.Second); d == nil {
		t.Fatal("alive and loaded: it acts")
	}
	if len(l.ran) != 1 {
		t.Fatal(l.ran)
	}
}

func TestStatusShowsStrategiesDecisionsAndHolds(t *testing.T) {
	g := newFake(spFireBlast)
	ctx := context.Background()
	ctl := NewController(ctx, g, func() Config {
		c := DefaultConfig()
		c.Now = g.clk.Now
		c.NewTicker = func(time.Duration) (<-chan time.Time, func()) { return make(chan time.Time), func() {} }
		return c
	}())
	st, err := ctl.Start(Params{Strategies: []string{"frost_mage"}})
	if err != nil || !st.Running || len(st.Strategies) != 1+len(presets["frost_mage"]) || st.Strategies[0] != "base" || st.Strategies[1] != "combat_dps" {
		t.Fatalf("%+v %v", st, err)
	}
	defer ctl.Stop()
	// Drive the brain by hand: a melee mob on the leader, a Fire Blast, then a refusal.
	ctl.mu.Lock()
	b := ctl.b
	ctl.mu.Unlock()
	g.addUnit(mob(mobA, selfG), 3, 0)
	g.clk.advance(time.Second)
	if d := b.Tick(ctx); d == nil || d.Action != "fire_blast" {
		t.Fatalf("%+v", d)
	}
	g.st.CastRefused(spFireBlast, codeOf(t, "line_of_sight"))
	g.clk.advance(2 * time.Second)
	b.Tick(ctx)
	st = ctl.Status()
	if len(st.Last) != 2 || st.Last[1].Action != "fire_blast" || st.Last[1].Result != ResultOK || st.Last[1].AgoMS != 2000 || st.Actions != 1 || st.Ticks != 2 {
		t.Fatalf("%+v", st)
	}
	if st.Last[0].Result != ResultNotPossible || st.Idle == "" {
		t.Fatalf("newest first, with the refusal: %+v", st)
	}
	if len(st.Holds) != 1 || st.Holds[0].SpellID != spFireBlast || st.Holds[0].Reason != "line_of_sight" || st.Holds[0].Name != "Fire Blast" {
		t.Fatalf("holds %+v", st.Holds)
	}
}

func TestAFailedActionThatIsNoCastIsHeldToo(t *testing.T) {
	g := newFake(spFrostbolt)
	fails := true
	n := 0
	a := &Action{Name: "use", Do: func(context.Context, *Ctx) error {
		n++
		if fails {
			return errors.New("no such item")
		}
		return nil
	}}
	r := engine(t, g, &Strategy{Name: "s", Actions: []*Action{a}, Rules: []Rule{{Trigger: always(""), Do: []Next{{"use", RelNormal}}}}})
	if d := r.tick(time.Second); d == nil || d.Result != ResultError {
		t.Fatalf("%+v", d)
	}
	for i := 0; i < 4; i++ {
		r.tick(200 * time.Millisecond)
	}
	if n != 1 {
		t.Fatalf("%d calls inside the hold", n)
	}
	fails = false
	if d := r.tick(time.Second); d == nil || d.Result != ResultOK || n != 2 {
		t.Fatalf("%+v %d", d, n)
	}
}

// A ghost has 1 health, not 0: the brain still does nothing, no cast and no potion during the corpse run.
func TestAGhostDoesNothing(t *testing.T) {
	g, l := newFake(), &log{}
	r := engine(t, g, &Strategy{Name: "s", Actions: []*Action{l.act("a", nil, nil)}, Rules: []Rule{{Trigger: always(""), Do: []Next{{"a", RelNormal}}}}})
	g.setSelf(func(u *objects.Unit) { u.Health = 1; u.Ghost = true })
	if d := r.tick(time.Second); d != nil || r.idle() != "dead" || len(l.ran) != 0 {
		t.Fatalf("%+v %q %v", d, r.idle(), l.ran)
	}
	g.setSelf(func(u *objects.Unit) { u.Health = 400; u.Ghost = false })
	if d := r.tick(time.Second); d == nil {
		t.Fatal("alive again: it acts")
	}
}
