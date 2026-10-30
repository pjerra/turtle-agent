package dungeon

import (
	"context"
	"fmt"
	"math"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/pjerra/turtle-agent/internal/packet"
	"github.com/pjerra/turtle-agent/internal/tbm"
)

// Every tunable is read by the driver and takes effect at the next boundary and not before: a value written to the tunables file during
// leg 0's first fight is seen by the driver at the start of leg 1 and at no earlier tick.
func TestEveryTunableTakesEffectAtTheNextBoundary(t *testing.T) {
	for _, s := range tuneSpecs {
		t.Run(s.Key, func(t *testing.T) {
			r := newLiveRig(t, nil)
			scratch := r.d.tuneTarget()
			before := s.get(scratch)
			want := s.Max
			if cand := layered(TuneDefaults(), map[string]float64{s.Key: want}, nil); before == s.Max || checkTuneSet(cand) != nil {
				want = s.Min // a rule between two values (pull_standoff within pull_range) rules the other end out
			}
			// What the driver will hold after storing the value (a duration to the millisecond, a yard count as float32).
			tg := &tuneTarget{cfg: new(Config), opt: new(RemoteOptions), x: new(tuneExtra)}
			s.set(tg, want)
			held := s.get(tg)
			var early []float64
			wrote := false
			r.g.onTick = func(g *fakeRemote) {
				if v := s.get(r.d.tuneTarget()); wrote && r.d.curLeg == 0 && g.mobsKilled == 0 && v != before {
					early = append(early, v)
				}
				if !wrote && r.d.curLeg == 0 && fighting(g, 297) {
					wrote = true
					r.write(r.tunePath, fmt.Sprintf(`{%q: %v}`, s.Key, want))
				}
			}
			// The run may go badly with a value at a limit; only the timeline of the value matters here.
			_ = r.d.Run(context.Background(), 0)
			if !wrote {
				t.Skip("the run did not get to the first fight")
			}
			if len(early) > 0 {
				t.Fatalf("%s changed before the boundary: %v", s.Key, early)
			}
			if got := s.get(r.d.tuneTarget()); got != held {
				t.Fatalf("%s is %v after the run, want %v", s.Key, got, held)
			}
			rep := r.d.Report()
			if len(rep.Remote.TuneChanges) != 1 || rep.Remote.TuneChanges[0].Key != s.Key || rep.Remote.TuneChanges[0].Boundary != boundaryLeg {
				t.Fatalf("%+v", rep.Remote.TuneChanges)
			}
		})
	}
}

// tuneRun runs plan on the fake game with a tunables file beside the plan file (read at the first leg start).
func tuneRun(t *testing.T, g *fakeRemote, plan *Plan, tune string, mod func(*RemoteOptions)) (Report, error) {
	t.Helper()
	dir := t.TempDir()
	r := &liveRig{t: t, dir: dir, g: g, plan: plan, planPath: filepath.Join(dir, "rt.json")}
	r.tunePath = DefaultTunePath(r.planPath)
	r.writePlan(plan)
	if tune != "" {
		r.write(r.tunePath, tune)
	}
	o := opts()
	o.PlanPath = r.planPath
	if mod != nil {
		mod(&o)
	}
	cfg := DefaultConfig()
	cfg.PostFight, cfg.AddsWait = 2*time.Second, 4*time.Second
	d := NewRemoteDriver(plan, g, cfg, o, g.clock())
	err := d.Run(context.Background(), 0)
	rep, _ := d.Status()
	return rep, err
}

func strungOut(t *testing.T) (*fakeRemote, *Plan) {
	g := newFakeRemote(t)
	g.aggro = 10
	m := g.addMob(299, Point{40, 0, 0})
	g.party[2].X = -30 // the mage stands 30 yd behind and does not come up
	g.lagUntil = map[packet.GUID]time.Time{gMage: g.now.Add(time.Hour)}
	return g, remotePlan(Leg{Name: "first", To: Point{0, 0, 0}, PullGroups: []PullGroup{{Entries: []uint32{299}, Positions: []Point{m.pos}}}})
}

// gather_wait_s shortens the wait for a bot that does not come; gather_near widens what counts as gathered.
func TestTunedGatherWaitAndNear(t *testing.T) {
	g, plan := strungOut(t)
	base, err := tuneRun(t, g, plan, "", nil)
	if err != nil || base.Remote.GatherTimeouts < 1 {
		t.Fatalf("%v %+v", err, base.Remote)
	}
	g, plan = strungOut(t)
	short, err := tuneRun(t, g, plan, `{"gather_wait_s": 3}`, nil)
	if err != nil || short.Remote.GatherTimeouts < 1 || short.Remote.GatherWaitS > 3.5*float64(short.Remote.GatherTimeouts) || short.Remote.GatherWaitS >= base.Remote.GatherWaitS {
		t.Fatalf("%v waited %.1f s (default %.1f s) %+v", err, short.Remote.GatherWaitS, base.Remote.GatherWaitS, short.Remote)
	}
	g, plan = strungOut(t)
	wide, err := tuneRun(t, g, plan, `{"gather_near": 40}`, nil)
	if err != nil || wide.Remote.Gathers != 0 || wide.Remote.GatherTimeouts != 0 {
		t.Fatalf("%v %+v", err, wide.Remote)
	}
}

func hopLengths(g *fakeRemote) (longest float64) {
	for i := range g.moveOrderDest {
		longest = math.Max(longest, dist3(g.moveOrderPos[i], g.moveOrderDest[i]))
	}
	return
}

// careful_hop sets the longest single move of a walk inside the dungeon.
func TestTunedCarefulHop(t *testing.T) {
	plan := func() *Plan {
		return remotePlan(Leg{Name: "first", To: Point{120, 0, 0}, PullGroups: []PullGroup{{Entries: []uint32{299}, Positions: []Point{{125, 0, 0}}}}})
	}
	g := newFakeRemote(t)
	g.addMob(299, Point{125, 0, 0})
	if _, err := tuneRun(t, g, plan(), "", nil); err != nil {
		t.Fatal(err)
	}
	base := hopLengths(g)
	g = newFakeRemote(t)
	g.addMob(299, Point{125, 0, 0})
	if _, err := tuneRun(t, g, plan(), `{"careful_hop": 10}`, nil); err != nil {
		t.Fatal(err)
	}
	if got := hopLengths(g); got > 10.5 || base <= 20 {
		t.Fatalf("longest hop %.1f yd tuned to 10, %.1f untuned", got, base)
	}
}

// aggro_guard and ahead_max_dz say which hostile near the way is pulled before the hop; max_ahead_pulls bounds the pulls of one walk.
func TestTunedAheadPulls(t *testing.T) {
	walk := func() *Plan { return remotePlan(Leg{Name: "first", To: Point{120, 0, 0}}) }
	run := func(tune string, mobs ...Point) (Report, *fakeRemote, error) {
		g := newFakeRemote(t)
		for _, p := range mobs {
			g.addMob(77, p)
		}
		rep, err := tuneRun(t, g, walk(), tune, nil)
		return rep, g, err
	}
	if rep, _, err := run("", Point{60, 8, 0}); err != nil || rep.Remote.AheadPulls != 1 {
		t.Fatalf("untuned: %v %+v", err, rep.Remote)
	}
	if rep, _, err := run(`{"aggro_guard": 8, "gather_clear": 10}`, Point{60, 20, 0}); err != nil || rep.Remote.AheadPulls != 0 {
		t.Fatalf("a mob 20 yd off the way with aggro_guard 8 and gather_clear 10: %v %+v", err, rep.Remote)
	}
	if rep, _, err := run("", Point{60, 20, 0}); err != nil || rep.Remote.AheadPulls != 1 {
		t.Fatalf("a mob 20 yd off the way, untuned: %v %+v", err, rep.Remote)
	}
	if rep, _, err := run(`{"ahead_max_dz": 2}`, Point{60, 0, -5}); err != nil || rep.Remote.AheadPulls != 0 {
		t.Fatalf("a mob 5 yd below with ahead_max_dz 2: %v %+v", err, rep.Remote)
	}
	if rep, _, err := run("", Point{60, 0, -5}); err != nil || rep.Remote.AheadPulls != 1 {
		t.Fatalf("a mob 5 yd below, untuned: %v %+v", err, rep.Remote)
	}
	_, _, err := run(`{"max_ahead_pulls": 1}`, Point{50, 8, 0}, Point{80, 8, 0})
	if err == nil || !strings.Contains(err.Error(), "hostiles near the way were pulled") {
		t.Fatalf("max_ahead_pulls 1 with two packs on the way: %v", err)
	}
	if rep, _, err := run("", Point{50, 8, 0}, Point{80, 8, 0}); err != nil || rep.Remote.AheadPulls != 2 {
		t.Fatalf("two packs untuned: %v %+v", err, rep.Remote)
	}
}

// look_standoff sets how far short of a pack the lead bot stops to look for it: at 10 yd it is inside a 18 yd aggro range.
func TestTunedLookStandoff(t *testing.T) {
	look := func(tune string) (*rmob, Report) {
		g := newFakeRemote(t)
		g.aggro = 18
		g.sight = 25
		m := g.addMob(299, Point{100, 0, 0})
		plan := remotePlan(Leg{Name: "first", To: Point{0, 0, 0}, PullGroups: []PullGroup{{Entries: []uint32{299}, Positions: []Point{m.pos}}}})
		rep, err := tuneRun(t, g, plan, tune, nil)
		if err != nil {
			t.Fatalf("%v\n%s", err, notesOf(rep))
		}
		return m, rep
	}
	if m, _ := look(""); m.aggroed {
		t.Fatal("the pack aggroed with the default stand-off")
	}
	if m, _ := look(`{"look_standoff": 10}`); !m.aggroed {
		t.Fatal("a look at 10 yd did not come within the pack's 18 yd aggro range")
	}
}

// party_down_dead sets how many dead bots stop the leg.
func TestTunedPartyDownDead(t *testing.T) {
	run := func(tune string) (Report, error) {
		g := newFakeRemote(t)
		for i, n := range []string{"Roguey", "Huntey"} {
			g.party = append(g.party, &tbm.PartyRec{GUID: gMage + 1 + packet.GUID(i), Name: n, HP: 1000, MaxHP: 1000, Power: 100, MaxPower: 100, PowerType: 3, State: "n"})
		}
		g.killRate = 1
		m := g.addMob(299, Point{40, 0, 0})
		died := false
		g.onTick = func(g *fakeRemote) {
			if m.engaged && !died && g.now.Sub(m.pullAt) > 6*time.Second {
				died = true
				for _, p := range g.party[1:3] { // two dead of five
					p.Dead, p.HP = true, 0
				}
			}
		}
		plan := remotePlan(Leg{Name: "first", To: Point{0, 0, 0}, PullGroups: []PullGroup{{Entries: []uint32{299}, Positions: []Point{{40, 0, 0}}}}})
		return tuneRun(t, g, plan, tune, func(o *RemoteOptions) { o.Bots = append(o.Bots, "Roguey", "Huntey"); o.RezWait = -1 })
	}
	if rep, _ := run(""); rep.PartyDowns != 0 {
		t.Fatalf("two dead stopped the leg by default: %d", rep.PartyDowns)
	}
	if rep, err := run(`{"party_down_dead": 2}`); rep.PartyDowns != 1 || err == nil || !strings.Contains(err.Error(), "wipe") {
		t.Fatalf("party_down_dead 2: %v party downs %d", err, rep.PartyDowns)
	}
}
