package dungeon

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/pjerra/turtle-agent/internal/trace"
	"github.com/pjerra/turtle-agent/internal/tracerun"
)

// liveRig is a remote run over three legs (the second has two pull groups) whose plan and tunables files sit in a temp folder.
type liveRig struct {
	t        *testing.T
	g        *fakeRemote
	d        *RemoteDriver
	dir      string
	planPath string
	tunePath string
	plan     *Plan // the plan as written at the start
	stamp    int
}

func livePlan() *Plan {
	return remotePlan(
		Leg{Name: "a", To: Point{50, 0, 0}, PullGroups: []PullGroup{{Entries: []uint32{297}, Positions: []Point{{65, 5, 0}}}}},
		Leg{Name: "b", To: Point{150, 0, 0}, PullGroups: []PullGroup{
			{Entries: []uint32{299}, Positions: []Point{{170, 5, 0}}},
			{Entries: []uint32{298}, Positions: []Point{{190, 5, 0}}},
		}},
		Leg{Name: "c", To: Point{250, 0, 0}, PullGroups: []PullGroup{{Entries: []uint32{296}, Positions: []Point{{270, 5, 0}}}}},
	)
}

func newLiveRig(t *testing.T, mod func(*RemoteOptions)) *liveRig {
	t.Helper()
	r := &liveRig{t: t, dir: t.TempDir(), g: newFakeRemote(t), plan: livePlan()}
	r.g.addMob(297, Point{65, 5, 0})
	r.g.addMob(299, Point{170, 5, 0})
	r.g.addMob(298, Point{190, 5, 0})
	r.g.addMob(296, Point{270, 5, 0})
	r.planPath = filepath.Join(r.dir, "rt.json")
	r.tunePath = DefaultTunePath(r.planPath)
	r.writePlan(r.plan)
	plan, err := Load(r.planPath)
	if err != nil {
		t.Fatal(err)
	}
	o := opts()
	o.PlanPath = r.planPath
	if mod != nil {
		mod(&o)
	}
	cfg := DefaultConfig()
	cfg.PostFight, cfg.AddsWait = 2*time.Second, 4*time.Second
	cfg.Trace = tracerun.Config{On: true, Dir: r.dir, RunID: "r-live-1"}
	r.d = NewRemoteDriver(plan, r.g, cfg, o, r.g.clock())
	return r
}

// write puts data in path with a modification time of its own, so that two writes in a row never look alike to the stamp.
func (r *liveRig) write(path, data string) {
	r.t.Helper()
	if err := os.WriteFile(path, []byte(data), 0o644); err != nil {
		r.t.Fatal(err)
	}
	r.stamp++
	at := time.Unix(1_500_000_000+int64(r.stamp)*10, 0)
	if err := os.Chtimes(path, at, at); err != nil {
		r.t.Fatal(err)
	}
}

func (r *liveRig) writePlan(p *Plan) {
	r.t.Helper()
	b, err := Marshal(p)
	if err != nil {
		r.t.Fatal(err)
	}
	r.write(r.planPath, string(b))
}

func (r *liveRig) run() Report {
	r.t.Helper()
	err := r.d.Run(context.Background(), 0)
	rep, ended := r.d.Status()
	if !ended {
		r.t.Fatal("the report does not say the run ended")
	}
	if err != nil || rep.State != "done" {
		r.t.Fatalf("%v %s %v", err, rep.State, rep.Notes)
	}
	return rep
}

func (r *liveRig) events(ev string) []trace.Event {
	r.t.Helper()
	f, err := trace.Read(filepath.Join(r.dir, "r-live-1.jsonl"))
	if err != nil {
		r.t.Fatal(err)
	}
	var out []trace.Event
	for _, e := range f.Events {
		if e.Ev == ev {
			out = append(out, e)
		}
	}
	return out
}

// mobOf finds the live mob of an entry (a killed mob leaves the fake's list).
func mobOf(g *fakeRemote, entry uint32) *rmob {
	for _, m := range g.mobs {
		if m.entry == entry {
			return m
		}
	}
	return nil
}

// fighting says that the mob of the entry is in a fight and alive.
func fighting(g *fakeRemote, entry uint32) bool {
	m := mobOf(g, entry)
	return m != nil && m.hp < 100 && m.hp > 0
}

// A change made during leg 0's walk is taken at leg 1's start and not before; a change made during the first pull applies at the second
// group's start and not before. Both are in the report and in the trace, with old and new.
func TestLiveTuneAppliesAtTheBoundaryOnly(t *testing.T) {
	r := newLiveRig(t, nil)
	type sample struct {
		leg       int
		moveLeg   float32
		pullRange float32
		packAlive bool // the first pack of leg b is alive
	}
	var samples []sample
	wrote1, wrote2 := false, false
	r.g.onTick = func(g *fakeRemote) {
		leg := r.d.curLeg
		if leg == 0 && g.mobsKilled > 0 {
			leg = -1 // leg 0 is over: the next boundary may be taking the edit
		}
		samples = append(samples, sample{leg, r.d.opt.MoveLeg, r.d.cfg.PullRange, mobOf(g, 299) != nil})
		switch {
		case !wrote1 && r.d.curLeg == 0 && fighting(g, 297):
			wrote1 = true
			r.write(r.tunePath, `{"_why": "the walks are too long", "move_leg": 60, "pull_range": 40}`)
		case !wrote2 && r.d.curLeg == 1 && fighting(g, 299):
			wrote2 = true
			r.write(r.tunePath, `{"move_leg": 60, "pull_range": 39}`)
		}
	}
	rep := r.run()
	if !wrote1 || !wrote2 {
		t.Fatal("the files were not written")
	}
	for _, s := range samples {
		if s.leg == 0 && (s.moveLeg != 120 || s.pullRange != 45) {
			t.Fatalf("the first edit took effect in leg 0: %+v", s)
		}
		if s.leg == 1 && s.packAlive && s.pullRange != 40 {
			t.Fatalf("the second edit took effect before the second group: %+v", s)
		}
	}
	got := rep.Remote.TuneChanges
	if len(got) != 3 {
		t.Fatalf("%+v", got)
	}
	want := []TuneChange{
		{Key: "move_leg", Old: 120, New: 60, Source: "file", Leg: 1, Boundary: boundaryLeg},
		{Key: "pull_range", Old: 45, New: 40, Source: "file", Leg: 1, Boundary: boundaryLeg},
		{Key: "pull_range", Old: 40, New: 39, Source: "file", Leg: 1, Boundary: boundaryPull},
	}
	for i, w := range want {
		g := got[i]
		g.At = time.Time{}
		if g != w {
			t.Fatalf("change %d: %+v want %+v", i, g, w)
		}
		if got[i].At.IsZero() {
			t.Fatalf("change %d has no time", i)
		}
	}
	if r.d.cfg.PullRange != 39 || r.d.opt.MoveLeg != 60 {
		t.Fatalf("end values %v %v", r.d.cfg.PullRange, r.d.opt.MoveLeg)
	}
	evs := r.events("tune_change")
	if len(evs) != 3 || evs[2].D["key"] != "pull_range" || evs[2].D["old"] != 40.0 || evs[2].D["new"] != 39.0 || evs[2].D["boundary"] != boundaryPull || evs[2].D["source"] != "file" {
		t.Fatalf("%+v", evs)
	}
	if _, err := time.Parse(time.RFC3339Nano, evs[0].D["at"].(string)); err != nil {
		t.Fatal(err)
	}
	// The trace turns into the patch: the net of each key, as it would be written to the file.
	f, err := trace.Read(filepath.Join(r.dir, "r-live-1.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	diff := TuneDiff(f)
	b, _ := json.Marshal(diff.TunePatch)
	if string(b) != `[{"op":"add","path":"/move_leg","value":60},{"op":"add","path":"/pull_range","value":39}]` || len(diff.PlanPatch) != 0 {
		t.Fatalf("%s %+v", b, diff)
	}
}

// A file that does not parse, names an unknown key, breaks a limit or breaks a rule between two values changes nothing; the reason is
// in the report and the trace once; a good file after it is taken.
func TestLiveTuneBadFileKeepsTheOldValues(t *testing.T) {
	for name, content := range map[string]string{
		"not json":     `{"pull_range": 40`,
		"unknown key":  `{"pul_range": 40}`,
		"over a limit": `{"pull_range": 500}`,
		"not a number": `{"pull_range": "far"}`,
		"half good":    `{"move_leg": 60, "pull_range": 5}`,
		"rule":         `{"pull_standoff": 60}`,
	} {
		t.Run(name, func(t *testing.T) {
			r := newLiveRig(t, nil)
			wrote, fixed := false, false
			var seen []float32
			r.g.onTick = func(g *fakeRemote) {
				seen = append(seen, r.d.cfg.PullRange)
				switch {
				case !wrote && r.d.curLeg == 0 && fighting(g, 297):
					wrote = true
					r.write(r.tunePath, content)
				case !fixed && r.d.curLeg == 1 && fighting(g, 299):
					fixed = true
					r.write(r.tunePath, `{"pull_range": 42}`)
				}
			}
			rep := r.run()
			if got := rep.Remote.TuneChanges; len(got) != 1 || got[0].Key != "pull_range" || got[0].New != 42 || got[0].Boundary != boundaryPull {
				t.Fatalf("the bad file changed something, or the good one after it was not taken: %+v", got)
			}
			if slices.Contains(seen, 40) || slices.Contains(seen, 500) || r.d.opt.MoveLeg != 120 {
				t.Fatalf("a value of the bad file got in: %v move_leg %v", seen, r.d.opt.MoveLeg)
			}
			bad := 0
			for _, e := range r.events("tune_change") {
				if e.D["ok"] == false {
					bad++
					if e.D["source"] != "file" || e.D["error"] == "" || e.D["file"] != r.tunePath {
						t.Fatalf("%+v", e.D)
					}
				}
			}
			if bad != 1 {
				t.Fatalf("%d refusals in the trace, want one (a bad file is reported once)", bad)
			}
			if !strings.Contains(strings.Join(rep.Notes, "\n"), "the old values stay") {
				t.Fatalf("notes: %v", rep.Notes)
			}
		})
	}
}

// A tunables file that goes away is no reason to drop the values: the run keeps them and says so.
func TestLiveTuneFileGoneKeepsValues(t *testing.T) {
	r := newLiveRig(t, nil)
	r.write(r.tunePath, `{"assist_every_s": 20}`)
	gone := false
	r.g.onTick = func(g *fakeRemote) {
		if !gone && r.d.curLeg == 1 && g.lead().X > 120 {
			gone = true
			_ = os.Remove(r.tunePath)
		}
	}
	rep := r.run()
	if r.d.cfg.AssistEvery != 20*time.Second || len(rep.Remote.TuneChanges) != 1 {
		t.Fatalf("%v %+v", r.d.cfg.AssistEvery, rep.Remote.TuneChanges)
	}
	if !strings.Contains(strings.Join(rep.Notes, "\n"), "the file is gone") {
		t.Fatalf("%v", rep.Notes)
	}
}

// A key taken out of the file goes back to the value the driver was made with, as a "reset".
func TestLiveTuneKeyRemovedFromFileResets(t *testing.T) {
	r := newLiveRig(t, nil)
	r.write(r.tunePath, `{"assist_every_s": 20}`)
	done := false
	r.g.onTick = func(g *fakeRemote) {
		if !done && r.d.curLeg == 1 && g.lead().X > 120 {
			done = true
			r.write(r.tunePath, `{}`)
		}
	}
	rep := r.run()
	got := rep.Remote.TuneChanges
	if len(got) != 2 || got[1].Source != "reset" || got[1].New != 10 || got[1].Old != 20 || r.d.cfg.AssistEvery != 10*time.Second {
		t.Fatalf("%+v", got)
	}
	var f *trace.File
	f, _ = trace.Read(filepath.Join(r.dir, "r-live-1.jsonl"))
	// 10 -> 20 -> 10: nothing to patch.
	if p := TuneDiff(f).TunePatch; len(p) != 0 {
		t.Fatalf("%+v", p)
	}
}

// tune_set: checked at once against the limits, taken at the next boundary with the same events, "default" takes it back. It may be
// called from another goroutine while the run goes on (the race detector looks).
func TestLiveTuneSetRPC(t *testing.T) {
	r := newLiveRig(t, nil)
	var acks []TuneAck
	var errs []error
	var wg sync.WaitGroup
	step := 0
	r.g.onTick = func(g *fakeRemote) {
		switch {
		case step == 0 && r.d.curLeg == 0 && fighting(g, 297):
			step++
			_, e1 := r.d.TuneSet("pull_range", 500.0)
			_, e2 := r.d.TuneSet("no_such_key", 1.0)
			_, e3 := r.d.TuneSet("pull_attempts", 2.5)
			_, e4 := r.d.TuneSet("pull_standoff", 60.0) // more than pull_range
			_, e5 := r.d.TuneSet("pull_range", "far")
			errs = append(errs, e1, e2, e3, e4, e5)
			a, err := r.d.TuneSet("pull_range", 40.0)
			acks, errs = append(acks, a), append(errs, err)
			wg.Add(1)
			go func() { // the run worker's call, from another goroutine
				defer wg.Done()
				for range 20 {
					r.d.TuneList()
				}
			}()
		case step == 1 && r.d.curLeg == 1 && fighting(g, 299):
			step++
			a, err := r.d.TuneSet("pull_range", "default")
			acks, errs = append(acks, a), append(errs, err)
		}
	}
	rep := r.run()
	wg.Wait()
	for i := range 5 {
		var te *TuneError
		if !errors.As(errs[i], &te) {
			t.Fatalf("request %d: %v", i, errs[i])
		}
	}
	if errs[5] != nil || errs[6] != nil || acks[0].Old != 45 || acks[0].New != 40 || acks[1].New != 45 || acks[1].Old != 40 {
		t.Fatalf("%+v %v", acks, errs)
	}
	got := rep.Remote.TuneChanges
	if len(got) != 2 || got[0].Source != "rpc" || got[0].New != 40 || got[0].Boundary != boundaryLeg || got[1].Source != "reset" || got[1].New != 45 || got[1].Boundary != boundaryPull {
		t.Fatalf("%+v", got)
	}
	if r.d.cfg.PullAttempts != 3 || r.d.cfg.PullStandoff != 38 {
		t.Fatalf("a refused request got in: %+v", r.d.cfg)
	}
	// A request that is queued shows in the list until the boundary takes it.
	r2 := newLiveRig(t, nil)
	if _, err := r2.d.TuneSet("move_leg", 80.0); err != nil {
		t.Fatal(err)
	}
	for _, in := range r2.d.TuneList() {
		if in.Key == "move_leg" && (in.Queued == nil || *in.Queued != 80 || in.Value != 120) {
			t.Fatalf("%+v", in)
		}
	}
}

// The tunables file wins over the base, tune_set over the file; the file's later change does not undo a tune_set.
func TestLiveTuneLayers(t *testing.T) {
	r := newLiveRig(t, nil)
	r.write(r.tunePath, `{"pull_range": 40}`)
	step := 0
	r.g.onTick = func(g *fakeRemote) {
		switch {
		case step == 0 && r.d.curLeg == 0 && fighting(g, 297):
			step++
			if _, err := r.d.TuneSet("pull_range", 39.0); err != nil {
				t.Error(err)
			}
		case step == 1 && r.d.curLeg == 1 && fighting(g, 299):
			step++
			r.write(r.tunePath, `{"pull_range": 41}`)
		}
	}
	rep := r.run()
	got := rep.Remote.TuneChanges
	if len(got) != 2 || got[0].Source != "file" || got[0].New != 40 || got[1].Source != "rpc" || got[1].Old != 40 || got[1].New != 39 || r.d.cfg.PullRange != 39 {
		t.Fatalf("%+v %v", got, r.d.cfg.PullRange)
	}
}

// Every tunable: its default is the driver's, the default lies inside its limits, the limits are enforced at both ends, and no wait
// can be made longer than half an hour (rule 3).
func TestTuneLimits(t *testing.T) {
	def := TuneDefaults()
	cfg, opt, x := DefaultConfig().withDefaults(), DefaultRemoteOptions().withDefaults(), defaultTuneExtra()
	tg := &tuneTarget{cfg: &cfg, opt: &opt, x: &x}
	if len(tuneSpecs) != len(tuneIndex) {
		t.Fatal("a key is listed twice")
	}
	if err := checkTuneSet(def); err != nil {
		t.Fatalf("the defaults break a rule: %v", err)
	}
	for _, s := range tuneSpecs {
		if d := def[s.Key]; d < s.Min || d > s.Max {
			t.Errorf("%s: default %v outside %v..%v", s.Key, d, s.Min, s.Max)
		}
		if s.Min > s.Max || s.Doc == "" {
			t.Errorf("%s: limits %v..%v doc %q", s.Key, s.Min, s.Max, s.Doc)
		}
		if s.Unit == "s" && s.Max > 1800 {
			t.Errorf("%s: a wait of up to %v s", s.Key, s.Max)
		}
		for _, v := range []float64{s.Min, s.Max} {
			if err := s.check(v); err != nil {
				t.Errorf("%s: %v refused: %v", s.Key, v, err)
			}
			s.set(tg, v)
			if got := s.get(tg); got != v {
				t.Errorf("%s: set %v, read %v", s.Key, v, got)
			}
		}
		for _, v := range []float64{s.Min - 1, s.Max + 1} {
			if err := s.check(v); err == nil {
				t.Errorf("%s: %v accepted", s.Key, v)
			}
		}
		if s.whole() && s.check(s.Min+0.5) == nil {
			t.Errorf("%s: a fraction accepted", s.Key)
		}
		if _, err := ParseTuneFile([]byte(`{"` + s.Key + `": ` + jsonNumber(s.Max+1) + `}`)); err == nil {
			t.Errorf("%s: a file over the limit accepted", s.Key)
		}
	}
	// The look stops short of the 25 yd of the clear evidence, whatever the file says.
	if tuneIndex["look_standoff"].Max >= ClearSeeRadius {
		t.Fatal("look_standoff can reach the evidence radius")
	}
	if _, err := ParseTuneFile([]byte(`{"_note": "x", "pull_range": 44, "gather_wait_s": 20.5}`)); err != nil {
		t.Fatal(err)
	}
}

func jsonNumber(v float64) string { b, _ := json.Marshal(v); return string(b) }

// The thresholds that were constants start at those constants.
func TestTuneExtraDefaultsAreTheConstants(t *testing.T) {
	x := defaultTuneExtra()
	if x.LookStandoff != LookStandoff || x.LookArrive != lookArrive || x.GatherNear != GatherNear || x.AggroGuard != AggroGuard || x.MaxAheadPulls != maxAheadPulls ||
		x.PartyDownDead != partyDownDead || x.GatherProgress != gatherProgress || x.AheadMaxDZ != aheadMaxDZ || x.RespawnSettle != respawnSettle || x.AttackHoldYd != attackHoldYd || x.TankBusyRounds != maxTankBusyRounds || x.AddOrders != maxAddOrders || x.FallbackTries != objectFallbackTries {
		t.Fatalf("%+v", x)
	}
}

func editedPlan(t *testing.T, mod func(*Plan)) *Plan {
	t.Helper()
	p := livePlan()
	mod(p)
	return p
}

// Plan reload at a leg start: everything of the legs not started changes (points, groups, radius, notes), the leg that was done does not,
// and the stepper begins the new leg on the new plan. Nothing changes before the boundary.
func TestLivePlanReloadAtLegStart(t *testing.T) {
	r := newLiveRig(t, nil)
	done := false
	var legC []float32
	r.g.onTick = func(g *fakeRemote) {
		if r.d.curLeg == 0 && g.mobsKilled == 0 {
			legC = append(legC, r.d.plan.Legs[2].To.X)
		}
		if !done && r.d.curLeg == 0 && fighting(g, 297) {
			done = true
			r.writePlan(editedPlan(t, func(p *Plan) {
				p.Legs[0].To = Point{55, 0, 0} // the leg being walked: not taken
				p.Legs[1].To = Point{160, 0, 0}
				p.Legs[1].PullGroups[1].Radius = 18
				p.Legs[1].Notes = []string{"the second pack patrols"}
				p.Legs[2].To = Point{260, 0, 0}
			}))
		}
	}
	rep := r.run()
	for _, x := range legC {
		if x != 250 {
			t.Fatalf("the plan changed in the middle of leg 0: %v", legC)
		}
	}
	pl := rep.Remote.PlanReloads
	if len(pl) != 1 || !pl[0].OK || pl[0].Boundary != boundaryLeg || pl[0].Leg != 1 {
		t.Fatalf("%+v", pl)
	}
	paths := []string{}
	for _, op := range pl[0].Ops {
		paths = append(paths, op.Path)
	}
	for _, w := range []string{"/legs/1/to/x", "/legs/1/pull_groups/1/radius", "/legs/1/notes", "/legs/2/to/x"} {
		if !slices.Contains(paths, w) {
			t.Fatalf("no op for %s: %v", w, paths)
		}
	}
	if !slices.Contains(pl[0].Ignored, "/legs/0/to") {
		t.Fatalf("ignored %v", pl[0].Ignored)
	}
	if r.d.plan.Legs[0].To.X != 50 || r.d.plan.Legs[1].To.X != 160 || r.d.plan.Legs[2].To.X != 260 || r.d.plan.Legs[1].PullGroups[1].Radius != 18 {
		t.Fatalf("%+v", r.d.plan.Legs)
	}
	if len(r.g.ordersOf("cmd move 260.00")) == 0 {
		t.Fatalf("leg 2 was not walked to its new point: %v", r.g.ordersOf("cmd move"))
	}
	if len(r.g.ordersOf("cmd move 160.00")) == 0 {
		t.Fatalf("leg 1 was not walked to its new point: %v", r.g.ordersOf("cmd move"))
	}
	evs := r.events("plan_reload")
	if len(evs) != 1 || evs[0].D["ok"] != true || evs[0].D["boundary"] != boundaryLeg {
		t.Fatalf("%+v", evs)
	}
	// And tune-diff gives the operations back, without the old values.
	f, _ := trace.Read(filepath.Join(r.dir, "r-live-1.jsonl"))
	diff := TuneDiff(f)
	if len(diff.PlanPatch) != len(pl[0].Ops) || len(diff.TunePatch) != 0 {
		t.Fatalf("%+v", diff)
	}
	for _, op := range diff.PlanPatch {
		if op.Old != nil {
			t.Fatalf("the patch has an old value: %+v", op)
		}
	}
}

// Plan reload at a pull-group start: the groups the leg cleared stay as they were (rule 1), the groups not started and what follows take
// the file.
func TestLivePlanReloadAtPullGroupStart(t *testing.T) {
	r := newLiveRig(t, nil)
	done := false
	r.g.onTick = func(g *fakeRemote) {
		if !done && r.d.curLeg == 1 && fighting(g, 299) {
			done = true
			r.writePlan(editedPlan(t, func(p *Plan) {
				p.Legs[1].PullGroups[0].Positions = []Point{{171, 5, 0}} // the group being fought: not taken
				p.Legs[1].PullGroups[0].Entries = []uint32{299, 300}
				p.Legs[1].PullGroups[1].Radius = 22
				p.Legs[1].To = Point{151, 0, 0} // the walk is done
				p.Legs[2].To = Point{240, 0, 0}
			}))
		}
	}
	rep := r.run()
	pl := rep.Remote.PlanReloads
	if len(pl) != 1 || pl[0].Boundary != boundaryPull || pl[0].Leg != 1 {
		t.Fatalf("%+v", pl)
	}
	l := r.d.plan.Legs[1]
	if l.PullGroups[0].Positions[0].X != 170 || len(l.PullGroups[0].Entries) != 1 || l.To.X != 150 || l.PullGroups[1].Radius != 22 || r.d.plan.Legs[2].To.X != 240 {
		t.Fatalf("%+v", r.d.plan.Legs)
	}
	for _, w := range []string{"/legs/1/pull_groups/0", "/legs/1/to"} {
		if !slices.Contains(pl[0].Ignored, w) {
			t.Fatalf("ignored %v lacks %s", pl[0].Ignored, w)
		}
	}
	if rep.Legs[1].Pulls != 2 || r.g.mobsKilled != 4 {
		t.Fatalf("%+v", rep.Legs)
	}
}

// A plan file that does not parse, has another shape or another name changes nothing; the run goes on with its own plan and says why.
func TestLivePlanReloadRefusals(t *testing.T) {
	cases := map[string]func(r *liveRig){
		"truncated": func(r *liveRig) { r.write(r.planPath, `{"version": 1, "name": "rt", "map": 0, "legs": [`) },
		"invalid": func(r *liveRig) {
			p := livePlan()
			p.Legs[1].PullGroups[0].Entries = nil
			b, _ := Marshal(p)
			r.write(r.planPath, string(b))
		},
		"fewer legs": func(r *liveRig) {
			p := livePlan()
			p.Legs = p.Legs[:2]
			r.writePlan(p)
		},
		"other name": func(r *liveRig) {
			p := livePlan()
			p.Name = "other"
			r.writePlan(p)
		},
		"missing": func(r *liveRig) { _ = os.Remove(r.planPath) },
	}
	for name, edit := range cases {
		t.Run(name, func(t *testing.T) {
			r := newLiveRig(t, nil)
			done := false
			r.g.onTick = func(g *fakeRemote) {
				if !done && r.d.curLeg == 0 && fighting(g, 297) {
					done = true
					edit(r)
				}
			}
			rep := r.run()
			if !reflectEqualPlans(r.d.plan, livePlan()) {
				t.Fatalf("the plan changed: %+v", r.d.plan.Legs)
			}
			if name == "missing" {
				return // a file that is not there is not looked at again
			}
			pl := rep.Remote.PlanReloads
			if len(pl) != 1 || pl[0].OK || pl[0].Error == "" {
				t.Fatalf("%+v", pl)
			}
			evs := r.events("plan_reload")
			if len(evs) != 1 || evs[0].D["ok"] != false || evs[0].D["error"] == "" {
				t.Fatalf("%+v", evs)
			}
			f, _ := trace.Read(filepath.Join(r.dir, "r-live-1.jsonl"))
			if d := TuneDiff(f); len(d.PlanPatch) != 0 || len(d.Refused) != 1 {
				t.Fatalf("%+v", d)
			}
		})
	}
}

func reflectEqualPlans(a, b *Plan) bool {
	x, _ := json.Marshal(a)
	y, _ := json.Marshal(b)
	return string(x) == string(y)
}

// NoReload leaves the files alone (tune_set still works); with no plan path the run reads nothing.
func TestLiveReloadOff(t *testing.T) {
	r := newLiveRig(t, func(o *RemoteOptions) { o.NoReload = true })
	r.write(r.tunePath, `{"pull_range": 40}`)
	done := false
	r.g.onTick = func(g *fakeRemote) {
		if !done && r.d.curLeg == 0 && fighting(g, 297) {
			done = true
			r.writePlan(editedPlan(t, func(p *Plan) { p.Legs[2].To = Point{240, 0, 0} }))
			if _, err := r.d.TuneSet("assist_every_s", 12.0); err != nil {
				t.Error(err)
			}
		}
	}
	rep := r.run()
	if r.d.cfg.PullRange != 45 || r.d.plan.Legs[2].To.X != 250 || len(rep.Remote.PlanReloads) != 0 {
		t.Fatalf("%v %+v", r.d.cfg.PullRange, rep.Remote.PlanReloads)
	}
	if got := rep.Remote.TuneChanges; len(got) != 1 || got[0].Key != "assist_every_s" || got[0].Source != "rpc" {
		t.Fatalf("%+v", got)
	}
}

// The reload merge in isolation: legs done and groups cleared stay, the rest takes the file; the paths that differ are the ignored ones.
func TestMergeReload(t *testing.T) {
	cur, next := livePlan(), livePlan()
	next.Legs[0].To = Point{1, 0, 0}
	next.Legs[1].PullGroups[0].Radius = 9
	next.Legs[1].PullGroups[1].Radius = 11
	next.Legs[1].PullGroups = append(next.Legs[1].PullGroups, PullGroup{Entries: []uint32{7}, Positions: []Point{{1, 2, 3}}})
	next.Legs[2].Notes = []string{"n"}
	m, ign, err := mergeReload(cur, next, 1, true, 1)
	if err != nil {
		t.Fatal(err)
	}
	if m.Legs[0].To.X != 50 || m.Legs[1].PullGroups[0].Radius != 0 || m.Legs[1].PullGroups[1].Radius != 11 || len(m.Legs[1].PullGroups) != 3 || len(m.Legs[2].Notes) != 1 {
		t.Fatalf("%+v", m.Legs)
	}
	if !slices.Equal(ign, []string{"/legs/0/to", "/legs/1/pull_groups/0"}) {
		t.Fatalf("%v", ign)
	}
	if cur.Legs[1].PullGroups[1].Radius != 0 || len(cur.Legs[1].PullGroups) != 2 {
		t.Fatal("the merge changed the run's own plan")
	}
	// Fewer groups than the ones cleared: the cleared ones stay, the rest goes.
	next = livePlan()
	next.Legs[1].PullGroups = nil
	m, _, err = mergeReload(cur, next, 1, true, 1)
	if err != nil || len(m.Legs[1].PullGroups) != 1 {
		t.Fatalf("%v %+v", err, m.Legs[1].PullGroups)
	}
	if _, _, err := mergeReload(cur, &Plan{Name: "rt", Legs: next.Legs[:1]}, 0, false, 0); err == nil {
		t.Fatal("a plan with fewer legs was merged")
	}
}

func TestJSONDiffArrays(t *testing.T) {
	var a, b any
	_ = json.Unmarshal([]byte(`{"x": [1, 2, 3], "y": {"k": 1}, "z": 1}`), &a)
	_ = json.Unmarshal([]byte(`{"x": [1, 9], "y": {"k": 1, "n": 2}, "w": 3}`), &b)
	var ops []PatchOp
	jsonDiff(nil, a, b, &ops)
	got, _ := json.Marshal(ops)
	want := `[{"op":"add","path":"/w","value":3},{"op":"replace","path":"/x/1","value":9,"old":2},{"op":"remove","path":"/x/2","old":3},{"op":"add","path":"/y/n","value":2},{"op":"remove","path":"/z","old":1}]`
	if string(got) != want {
		t.Fatalf("%s\nwant %s", got, want)
	}
}

// The README table lists every tunable with its unit, limits and default, as the code has them.
func TestReadmeListsEveryTunable(t *testing.T) {
	b, err := os.ReadFile(filepath.Join("..", "..", "README.md"))
	if err != nil {
		t.Fatal(err)
	}
	def := TuneDefaults()
	for _, s := range tuneSpecs {
		prefix := fmt.Sprintf("| `%s` | %s | %v to %v | %v |", s.Key, s.Unit, s.Min, s.Max, def[s.Key])
		if !strings.Contains(string(b), prefix) {
			t.Errorf("README has no row %q", prefix)
		}
	}
}

// The tunables file counts from the start of the run, before the enter phase and the preflight: the first look of the bind already has
// its values, and the change is a run_start one.
func TestLiveTuneFileIsReadBeforeLegZero(t *testing.T) {
	r := newLiveRig(t, nil)
	r.write(r.tunePath, `{"move_leg": 60}`)
	var first []float32
	r.g.onTick = func(g *fakeRemote) {
		if len(first) == 0 {
			first = append(first, r.d.opt.MoveLeg)
		}
	}
	rep := r.run()
	if len(first) != 1 || first[0] != 60 {
		t.Fatalf("the first tick saw move_leg %v", first)
	}
	if got := rep.Remote.TuneChanges; len(got) != 1 || got[0].Key != "move_leg" || got[0].Boundary != boundaryRun || got[0].Leg != 0 {
		t.Fatalf("%+v", got)
	}
	if evs := r.events("tune_change"); len(evs) != 1 || evs[0].D["boundary"] != boundaryRun {
		t.Fatalf("%+v", evs)
	}
}

func TestValidateTuneFile(t *testing.T) {
	for in, ok := range map[string]bool{
		`{}`: true, `{"pull_range": 40, "_x": 1}`: true, `{"pull_range": 5}`: false, `{"nope": 1}`: false, `{`: false,
		`{"pull_standoff": 60}`: false, `{"rest_min_health_pct": 19}`: false, `{"rest_min_health_pct": 20}`: true,
	} {
		if err := ValidateTuneFile([]byte(in)); (err == nil) != ok {
			t.Errorf("%s: %v", in, err)
		}
	}
}
