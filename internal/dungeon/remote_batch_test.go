package dungeon

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/pjerra/turtle-agent/internal/objects"
	"github.com/pjerra/turtle-agent/internal/packet"
	"github.com/pjerra/turtle-agent/internal/tbm"
)

// ---- plan: the batch checks of the loader ----

func batchPlanJSON(ids []string, dx float64) string {
	var steps []string
	for i, id := range ids {
		steps = append(steps, fmt.Sprintf(`{"loot_object":{"entry":128403,"spawn":%d,"near":{"x":%g,"y":0,"z":0},"range":2,"batch":%q}}`, 27000+i, 30+dx*float64(i), id))
	}
	return eventPlan(strings.Join(steps, ","), "")
}

func repeatID(id string, n int) []string {
	out := make([]string, n)
	for i := range out {
		out[i] = id
	}
	return out
}

func TestPlanBatchChecks(t *testing.T) {
	ok := append(repeatID("g1", 3), repeatID("g2", 6)...)
	if p := mustPlan(t, batchPlanJSON(ok, 4)); p.Legs[0].Steps[0].LootObject.Batch != "g1" {
		t.Fatalf("%+v", p.Legs[0].Steps[0].LootObject)
	}
	out, _ := Marshal(mustPlan(t, batchPlanJSON(ok, 4)))
	if !strings.Contains(string(out), `"batch": "g2"`) {
		t.Fatalf("the batch id is lost on writing: %s", out)
	}
	for name, c := range map[string]struct {
		ids  []string
		dx   float64
		want string
	}{
		"two chests":       {repeatID("g1", 2), 4, `batch "g1" has 2 chests: a batch has 3 to 6`},
		"seven chests":     {repeatID("g1", 7), 2, `batch "g1" has 7 chests: a batch has 3 to 6`},
		"too wide":         {repeatID("g1", 4), 12, `reaches 18.0 yd from its centre: the chests of a batch lie within 15 yd`},
		"comes back":       {append(append(repeatID("g1", 3), repeatID("g2", 3)...), repeatID("g1", 3)...), 2, `batch "g1" comes back after other steps`},
		"widest allowed":   {repeatID("g1", 4), 10, ""}, // 15 yd from the centre exactly
		"unbatched mix":    {append(repeatID("g1", 3), "", "", ""), 4, ""},
		"no batch at all":  {repeatID("", 5), 60, ""},
		"six within 15 yd": {repeatID("g1", 6), 5.9, ""},
	} {
		_, err := Parse("t.json", []byte(batchPlanJSON(c.ids, c.dx)))
		switch {
		case c.want == "" && err != nil:
			t.Errorf("%s: %v", name, err)
		case c.want != "" && (err == nil || !strings.Contains(err.Error(), c.want)):
			t.Errorf("%s: %v, want %q", name, err, c.want)
		}
	}
}

// The Stepper hands the driver the section: the chests from the step on that have a batch id, with where they lie.
func TestStepperGivesTheBatchSection(t *testing.T) {
	leg, _ := graveLeg(5)
	for i := range leg.Steps {
		leg.Steps[i].LootObject.Batch = "g1"
		if i >= 3 {
			leg.Steps[i].LootObject.Batch = "g2"
		}
	}
	a, _ := batchSection(leg.Steps[1:])
	_, sec := batchSection(leg.Steps[1:])
	if a != "g1" || len(sec) != 4 || sec[0].ID != "g1" || sec[3].ID != "g2" || sec[0].At.X != 40 {
		t.Fatalf("%s %+v", a, sec)
	}
	if id, sec := batchSection([]Step{{UseObject: &UseObject{}}}); id != "" || sec != nil {
		t.Fatalf("%q %v", id, sec)
	}
}

// The graves of the shipped Zul'Farrak plan are in batches of 3 to 6 within 15 yd of their centre.
func TestZulfarrakGravesAreInBatches(t *testing.T) {
	p := loadPlan(t, zulfarrakPlan)
	l := zfLeg(t, p, "zumrah")
	sizes := map[string][]Point{}
	var order []string
	for _, s := range l.Steps {
		if lo := s.LootObject; lo != nil {
			if lo.Batch == "" {
				t.Fatalf("grave %d has no batch", lo.Spawn)
			}
			if _, ok := sizes[lo.Batch]; !ok {
				order = append(order, lo.Batch)
			}
			sizes[lo.Batch] = append(sizes[lo.Batch], *lo.Near)
		}
	}
	total := 0
	for _, id := range order {
		pts := sizes[id]
		total += len(pts)
		if len(pts) < BatchMinSize || len(pts) > BatchMaxSize || BatchSpread(pts) > BatchRadius {
			t.Errorf("batch %s: %d graves, %.1f yd", id, len(pts), BatchSpread(pts))
		}
	}
	if total != 31 || len(order) < 5 {
		t.Fatalf("%d graves in %v", total, order)
	}
}

// ---- the driver ----

// batchGraves is n graves 4 yd apart; ids names the plan batch of each.
func batchGraves(n int, gap float32, ids func(i int) string) (Leg, []*fakeGrave) {
	leg, gs := graveLeg(n)
	for i := range leg.Steps {
		at := Point{30 + gap*float32(i), 0, 0}
		stand := Point{at.X - 3, 0, 0}
		lo := leg.Steps[i].LootObject
		lo.Near, lo.Stand = &at, &stand
		lo.Batch = ids(i)
		gs[i].at = at
	}
	return leg, gs
}

type batchRunOut struct {
	killedAtOpen []int // the hostiles dead at each open
	d            *RemoteDriver
	rep          Report
	err          error
}

// runBatches runs a graves leg with the options; every open brings two hostiles that come at the party, unless wipeAt says that this open (1-based)
// kills the whole party instead.
func runBatches(t *testing.T, g *fakeCarrier, leg Leg, gs []*fakeGrave, o RemoteOptions, wipeAt ...int) batchRunOut {
	t.Helper()
	return runBatchesCfg(t, g, leg, gs, o, nil, wipeAt...)
}

func runBatchesCfg(t *testing.T, g *fakeCarrier, leg Leg, gs []*fakeGrave, o RemoteOptions, mod func(*Config), wipeAt ...int) batchRunOut {
	t.Helper()
	g.graves = map[packet.GUID]*fakeGrave{}
	for _, gv := range gs {
		g.graves[gv.guid] = gv
	}
	opens := 0
	var killed []int
	g.onGrave = func(c *fakeCarrier, gv *fakeGrave) {
		opens++
		killed = append(killed, c.mobsKilled)
		for _, w := range wipeAt {
			if opens == w {
				for _, p := range c.party {
					p.Dead, p.HP = true, 0
				}
				return
			}
		}
		for i := 0; i < 2; i++ {
			m := c.addMob(7286, Point{gv.at.X - 2, float32(i), 0})
			m.pulled, m.pullAt = true, c.now
		}
	}
	cfg := DefaultConfig()
	cfg.PostFight, cfg.AddsWait = 2*time.Second, 4*time.Second
	cfg.RecoverWipe = true
	if mod != nil {
		mod(&cfg)
	}
	d := NewRemoteDriver(remotePlan(leg), g, cfg, o, g.clock())
	err := d.Run(context.Background(), 0)
	rep, ended := d.Status()
	if !ended {
		t.Fatal("not ended")
	}
	return batchRunOut{killed, d, rep, err}
}

func batchSizes(rep Report) []int {
	var out []int
	for _, b := range rep.Remote.GraveBatches {
		out = append(out, b.Size)
	}
	return out
}

// With no run setting the plan's batches apply: all the chests of a batch are opened back to back (nothing of the first batch is dead at the
// opens of its own chests), then the hostiles are fought, with the area spells off while the chests are opened and on for the fight.
func TestPlanBatchesOpenBackToBackThenFightWithAoE(t *testing.T) {
	g := carrierGame(t)
	leg, gs := batchGraves(8, 4, func(i int) string { return fmt.Sprintf("g%d", i/4+1) })
	o := runBatches(t, g, leg, gs, objOpts())
	if o.err != nil || o.rep.State != "done" {
		t.Fatalf("%v\n%s", o.err, notesOf(o.rep))
	}
	if fmt.Sprint(batchSizes(o.rep)) != "[4 4]" {
		t.Fatalf("%v\n%s", batchSizes(o.rep), notesOf(o.rep))
	}
	b := o.rep.Remote.GraveBatches[0]
	if b.Mode != "plan" || b.Opened != 4 || b.Adds < 1 || b.Adds > 8 || b.Wipe || !b.AoE || b.FightS <= 0 || b.TankMin < 0 || b.HealMin < 0 || b.HealMana < 0 || b.RadiusYd <= 0 {
		t.Fatalf("%+v\n%s", b, notesOf(o.rep))
	}
	if !strings.Contains(notesOf(o.rep), "graves-batch: n=1 leg=0 mode=plan wanted=4 size=4 opened=4 skipped=0 adds=") || !strings.Contains(notesOf(o.rep), "wipe=no") {
		t.Fatalf("%s", notesOf(o.rep))
	}
	// The order of the area spells and the opens: off, 4 opens, on, then again for the second batch.
	var seq []string
	for _, ord := range g.orders {
		switch {
		case strings.HasPrefix(ord, "cmd open "):
			seq = append(seq, "open")
		case strings.HasSuffix(ord, " aoe off"):
			seq = append(seq, "off")
		case strings.HasSuffix(ord, " aoe on"):
			seq = append(seq, "on")
		}
	}
	got := strings.Join(compact(seq), " ")
	if got != "off open on off open on" {
		t.Fatalf("%s\n%v", got, g.orders)
	}
	// Three bots each way, and every bot ends with the area spells on (the module's default).
	if n := strings.Count(strings.Join(g.orders, "\n"), " aoe off"); n != 6 {
		t.Errorf("aoe off orders: %d", n)
	}
	for _, b := range []string{"Tanky", "Healy", "Magey"} {
		last := ""
		for _, ord := range g.orders {
			if strings.HasPrefix(ord, ".bot behavior "+b+" aoe ") {
				last = ord
			}
		}
		if !strings.HasSuffix(last, " aoe on") {
			t.Errorf("%s ends with %q", b, last)
		}
	}
	// No fight between the opens: the driver sends no attack or pull order from the first open of a batch to the fight after its last (the bots
	// defend themselves; the fake's party kills what comes at it on its own).
	first, last := g.indexOf("cmd open"), -1
	for i, ord := range g.orders {
		if strings.HasPrefix(ord, "cmd open ") && i < len(g.orders) && len(o.killedAtOpen) > 0 {
			if last < 0 || strings.Count(strings.Join(g.orders[first:i+1], "\n"), "cmd open ") <= 4 {
				last = i
			}
		}
	}
	for _, ord := range g.orders[first : last+1] {
		if strings.HasPrefix(ord, "cmd attack") || strings.HasPrefix(ord, "cmd pull") {
			t.Errorf("an order between the opens of a batch: %s\n%v", ord, g.orders[first:last+1])
		}
	}
	if g.mobsKilled != 16 {
		t.Errorf("killed %d", g.mobsKilled)
	}
}

func compact(in []string) []string {
	var out []string
	for _, s := range in {
		if len(out) == 0 || out[len(out)-1] != s {
			out = append(out, s)
		}
	}
	return out
}

// graves_batch is a fixed size, whatever the plan's ids; the last batch is what is left.
func TestFixedGravesBatchRegroupsTheSteps(t *testing.T) {
	g := carrierGame(t)
	leg, gs := batchGraves(7, 4, func(i int) string { return "g1" })
	opt := objOpts()
	opt.GravesBatch = 3
	o := runBatches(t, g, leg, gs, opt)
	if o.err != nil || fmt.Sprint(batchSizes(o.rep)) != "[3 3 1]" || o.rep.Remote.GraveBatches[0].Mode != "fixed" {
		t.Fatalf("%v %v\n%s", o.err, batchSizes(o.rep), notesOf(o.rep))
	}
}

// graves_ramp starts at 2 and grows by one after each batch without a wipe, up to the chests there are.
func TestGravesRampGrowsByOnePerCleanBatch(t *testing.T) {
	g := carrierGame(t)
	leg, gs := batchGraves(12, 3, func(i int) string { return "g1" })
	opt := objOpts()
	opt.GravesRamp = &GravesRamp{}
	o := runBatches(t, g, leg, gs, opt)
	if o.err != nil || fmt.Sprint(batchSizes(o.rep)) != "[2 3 4 3]" { // 2 + 3 + 4, then the 3 that are left
		t.Fatalf("%v %v\n%s", o.err, batchSizes(o.rep), notesOf(o.rep))
	}
	if o.rep.Remote.GraveBatches[0].Mode != "ramp" || o.rep.Wipes != 0 {
		t.Fatalf("%+v", o.rep.Remote.GraveBatches[0])
	}
	// A step and a maximum of its own.
	g = carrierGame(t)
	leg, gs = batchGraves(14, 3, func(i int) string { return "g1" })
	opt.GravesRamp = &GravesRamp{Start: 3, Step: 2, Max: 5}
	o = runBatches(t, g, leg, gs, opt)
	if o.err != nil || fmt.Sprint(batchSizes(o.rep)) != "[3 5 5 1]" {
		t.Fatalf("%v %v\n%s", o.err, batchSizes(o.rep), notesOf(o.rep))
	}
}

// After the first wipe in the graves the ramp stops growing, and with the cap of two recoveries the one that is left is the boss's: the rest of
// the graves is one chest at a time. The wipe is recorded with the size that wiped.
func TestGravesRampStopsGrowingAtTheFirstWipe(t *testing.T) {
	g := carrierGame(t)
	leg, gs := batchGraves(16, 3, func(i int) string { return "g1" })
	opt := objOpts()
	opt.GravesRamp = &GravesRamp{}
	o := runBatches(t, g, leg, gs, opt, 8) // the 8th open: the batch of 4 (opens 6-9) wipes
	if o.err != nil || o.rep.State != "done" || o.rep.Wipes != 1 {
		t.Fatalf("%v wipes %d\n%s", o.err, o.rep.Wipes, notesOf(o.rep))
	}
	bs := o.rep.Remote.GraveBatches
	var wiped []bool
	for _, b := range bs {
		wiped = append(wiped, b.Wipe)
	}
	if fmt.Sprint(batchSizes(o.rep)[:4]) != "[2 3 4 1]" || fmt.Sprint(wiped[:4]) != "[false false true false]" {
		t.Fatalf("%v %v\n%s", batchSizes(o.rep), wiped, notesOf(o.rep))
	}
	for _, s := range batchSizes(o.rep)[3:] {
		if s != 1 {
			t.Errorf("after the wipe the graves go one at a time: %v", batchSizes(o.rep))
		}
	}
	if !strings.Contains(notesOf(o.rep), "wipe=yes") || !strings.Contains(notesOf(o.rep), "wiped_at_size=4 next_size=1") || o.d.gb.wipedAt != 4 {
		t.Fatalf("%s", notesOf(o.rep))
	}
	if o.d.wipeRecoverLeg[0] != 1 || o.d.cfg.MaxWipeRecoveries-o.d.wipeRecoverLeg[0] != 1 {
		t.Fatalf("recoveries %v", o.d.wipeRecoverLeg)
	}
}

// With a cap of three recoveries the ramp's first wipe leaves two: the rest runs at the last clean size, singles only after the second wipe.
func TestGravesRampWithALargerCapRunsAtTheLastCleanSize(t *testing.T) {
	g := carrierGame(t)
	leg, gs := batchGraves(16, 3, func(i int) string { return "g1" })
	opt := objOpts()
	opt.GravesRamp = &GravesRamp{}
	o := runBatchesCfg(t, g, leg, gs, opt, func(c *Config) { c.MaxWipeRecoveries = 3 }, 8)
	if o.err != nil || fmt.Sprint(batchSizes(o.rep)[:5]) != "[2 3 4 3 3]" {
		t.Fatalf("%v %v\n%s", o.err, batchSizes(o.rep), notesOf(o.rep))
	}
}

// A second wipe in the graves is not recovered (the last slot of the leg is the boss's): the run stops with a note that says so, and the
// slot is unused.
func TestSecondGraveWipeIsNotRecoveredToKeepTheBossSlot(t *testing.T) {
	g := carrierGame(t)
	leg, gs := batchGraves(30, 3, func(i int) string { return "g1" })
	opt := objOpts()
	opt.GravesRamp = &GravesRamp{}
	o := runBatches(t, g, leg, gs, opt, 7, 10) // a wipe at the 7th open (recovered), another at the 10th
	if o.err == nil || o.rep.State != "failed" || !strings.Contains(o.err.Error(), "wipe in the graves: not recovered") {
		t.Fatalf("%v %s\n%s", o.err, o.rep.State, notesOf(o.rep))
	}
	if o.d.wipeRecoverLeg[0] != 1 || o.rep.Wipes != 2 || o.d.cfg.MaxWipeRecoveries-o.d.wipeRecoverLeg[0] != 1 || !strings.Contains(notesOf(o.rep), "the last slot is kept for the boss") {
		t.Fatalf("recoveries %v wipes %d\n%s", o.d.wipeRecoverLeg, o.rep.Wipes, notesOf(o.rep))
	}
	if n := len(g.revived); n != 3 { // the first wipe's revives only
		t.Errorf("revived %v", g.revived)
	}
}

// The full sequence: a grave wipe (recovered), the graves one at a time, then a wipe at the boss, which still has its slot, and the leg is done.
func TestGraveWipeThenBossWipeStillRecovers(t *testing.T) {
	g := carrierGame(t)
	leg, gs := batchGraves(8, 3, func(i int) string { return "g1" })
	leg.Boss = &Boss{Entry: 9, Position: Point{300, 0, 0}}
	boss := g.addMob(9, Point{300, 0, 0})
	bossWiped := false
	g.onTick = func(f *fakeRemote) {
		if !bossWiped && boss.hp < 100 && !f.party[0].Dead {
			bossWiped = true
			boss.hp, boss.engaged, boss.pulled, boss.attacked, boss.target = 100, false, false, false, 0 // the boss won this round and is back at full health, at his spot
			for _, p := range f.party {
				p.Dead, p.HP = true, 0
			}
		}
	}
	opt := objOpts()
	opt.GravesRamp = &GravesRamp{}
	o := runBatches(t, g, leg, gs, opt, 4)
	if o.err != nil || o.rep.State != "done" || !bossWiped {
		t.Fatalf("%v %s boss wiped %v\n%s", o.err, o.rep.State, bossWiped, notesOf(o.rep))
	}
	if o.d.wipeRecoverLeg[0] != 2 || o.rep.Wipes != 2 {
		t.Fatalf("recoveries %v wipes %d\n%s", o.d.wipeRecoverLeg, o.rep.Wipes, notesOf(o.rep))
	}
}

// A wipe earlier in the leg (a pull) used a recovery slot as much as one in the graves: the ramp does not start growing past what is safe.
func TestGravesRampCountsTheLegsEarlierWipes(t *testing.T) {
	g := carrierGame(t)
	leg, gs := batchGraves(9, 3, func(i int) string { return "g1" })
	opt := objOpts()
	opt.GravesRamp = &GravesRamp{}
	g.graves = map[packet.GUID]*fakeGrave{}
	cfg := DefaultConfig()
	cfg.RecoverWipe = true
	d := NewRemoteDriver(remotePlan(leg), g, cfg, opt, g.clock())
	d.curLeg = 0
	d.cfg.MaxWipeRecoveries = 4
	d.wipeRecoverLeg = map[int]int{0: 1}
	d.gb.lastClean = 3
	a := Action{Batch: "g1", Section: []BatchChest{{ID: "g1"}, {ID: "g1"}, {ID: "g1"}, {ID: "g1"}, {ID: "g1"}}}
	if got := d.batchWanted(a); got != 3 {
		t.Fatalf("after one recovered wipe the size is the last clean one: %d", got)
	}
	d.wipeRecoverLeg[0] = 3
	if got := d.batchWanted(a); got != 1 {
		t.Fatalf("with only the boss's slot left: %d", got)
	}
	_ = gs
}

// The loader's 15 yd holds for a size of the run's own: the batch is cut where the next chests are not within 15 yd of its centre, and says so.
func TestRunBatchSizeIsCutByTheRadius(t *testing.T) {
	g := carrierGame(t)
	leg, gs := batchGraves(8, 10, func(i int) string { return "g1" }) // 10 yd apart: 4 chests are 15 yd from their centre, 5 are 20
	opt := objOpts()
	opt.GravesBatch = 8
	o := runBatches(t, g, leg, gs, opt)
	if o.err != nil {
		t.Fatalf("%v\n%s", o.err, notesOf(o.rep))
	}
	b := o.rep.Remote.GraveBatches[0]
	if b.Wanted != 8 || b.Size != 4 || !b.CutByRadius || b.RadiusYd > BatchRadius+0.01 || !strings.Contains(b.Note, "wanted 8, only 4 chests lie within 15 yd") {
		t.Fatalf("%+v\n%s", b, notesOf(o.rep))
	}
	if fmt.Sprint(batchSizes(o.rep)) != "[4 4]" || !strings.Contains(notesOf(o.rep), "cut_by_radius=yes") {
		t.Fatalf("%v\n%s", batchSizes(o.rep), notesOf(o.rep))
	}
	// Running out of chests is not a cut by radius.
	g = carrierGame(t)
	leg, gs = batchGraves(3, 3, func(i int) string { return "g1" })
	opt.GravesBatch = 6
	o = runBatches(t, g, leg, gs, opt)
	if b := o.rep.Remote.GraveBatches[0]; b.Size != 3 || b.CutByRadius {
		t.Fatalf("%+v", b)
	}
}

// The party stays at the batch centre: one cmd stay after the gather, no walk or stay per chest, and cmd follow only after the last open.
func TestBatchPartyStaysAtTheCentreAndOnlyTheCarrierWalks(t *testing.T) {
	g := carrierGame(t)
	leg, gs := batchGraves(4, 4, func(i int) string { return "g1" })
	o := runBatches(t, g, leg, gs, objOpts())
	if o.err != nil {
		t.Fatalf("%v\n%s", o.err, notesOf(o.rep))
	}
	firstOpen, lastOpen := g.indexOf("cmd open"), -1
	for i, ord := range g.orders {
		if strings.HasPrefix(ord, "cmd open ") {
			lastOpen = i
		}
	}
	stays := 0
	for _, ord := range g.orders[firstOpen:lastOpen] {
		if ord == "cmd stay" || ord == "cmd follow" || strings.HasPrefix(ord, "cmd move") {
			t.Errorf("an order between the opens of a batch: %q\n%v", ord, g.orders[firstOpen:lastOpen])
		}
	}
	for _, ord := range g.orders[:firstOpen] {
		if ord == "cmd stay" {
			stays++
		}
	}
	if stays == 0 {
		t.Errorf("the party was not held at the centre before the opens: %v", g.orders[:firstOpen])
	}
	if g.indexOf("cmd follow") < lastOpen {
		t.Errorf("cmd follow before the last open: %v", g.orders)
	}
}

// The area spells are put back on before the unbind whatever way the run ends while a batch has them off.
func TestAoEIsBackOnBeforeTheUnbindWhateverEndsTheRun(t *testing.T) {
	check := func(t *testing.T, name string, g *fakeCarrier) {
		t.Helper()
		var lastAoE = map[string]string{}
		unbind := -1
		for i, ord := range g.orders {
			if strings.HasPrefix(ord, ".bot behavior ") && strings.Contains(ord, " aoe ") {
				lastAoE[strings.Fields(ord)[2]] = ord
			}
			if ord == "commander unbind" {
				unbind = i
			}
		}
		if len(lastAoE) != 3 || unbind < 0 {
			t.Fatalf("%s: aoe orders %v, unbind at %d\n%v", name, lastAoE, unbind, g.orders)
		}
		for bot, ord := range lastAoE {
			if !strings.HasSuffix(ord, " aoe on") {
				t.Errorf("%s: %s ends with %q", name, bot, ord)
			}
		}
		for i, ord := range g.orders {
			if i > unbind && strings.Contains(ord, " aoe ") {
				t.Errorf("%s: an aoe order after the unbind: %q", name, ord)
			}
		}
	}
	setup := func() (*fakeCarrier, Leg, []*fakeGrave) {
		g := carrierGame(t)
		leg, gs := batchGraves(6, 3, func(i int) string { return "g1" })
		return g, leg, gs
	}
	t.Run("the open fails", func(t *testing.T) {
		g, leg, gs := setup()
		fail := errOrder("cmd open x Magey", "invalid")
		g.script = map[string][]tbm.OrderResult{"open": {fail, fail, fail, fail}}
		opt := objOpts()
		opt.ObjectFallback = ObjectFallbackOff
		o := runBatches(t, g, leg, gs, opt)
		if o.err == nil {
			t.Fatalf("the run did not fail\n%s", notesOf(o.rep))
		}
		check(t, "failure", g)
	})
	t.Run("the run is cancelled", func(t *testing.T) {
		g, leg, gs := setup()
		ctx, cancel := context.WithCancel(context.Background())
		g.graves = map[packet.GUID]*fakeGrave{}
		for _, gv := range gs {
			g.graves[gv.guid] = gv
		}
		g.onGrave = func(c *fakeCarrier, gv *fakeGrave) { cancel() }
		d := NewRemoteDriver(remotePlan(leg), g, DefaultConfig(), objOpts(), g.clock())
		if err := d.Run(ctx, 0); err == nil {
			t.Fatal("the run was not cancelled")
		}
		check(t, "cancel", g)
	})
	t.Run("a panic", func(t *testing.T) {
		g, leg, gs := setup()
		g.graves = map[packet.GUID]*fakeGrave{}
		for _, gv := range gs {
			g.graves[gv.guid] = gv
		}
		g.onGrave = func(c *fakeCarrier, gv *fakeGrave) { panic("boom") }
		d := NewRemoteDriver(remotePlan(leg), g, DefaultConfig(), objOpts(), g.clock())
		err := d.Run(context.Background(), 0)
		if err == nil || !strings.Contains(err.Error(), "internal error: boom") {
			t.Fatalf("%v", err)
		}
		check(t, "panic", g)
	})
	t.Run("a wipe nobody recovers", func(t *testing.T) {
		g, leg, gs := setup()
		g.graves = map[packet.GUID]*fakeGrave{}
		for _, gv := range gs {
			g.graves[gv.guid] = gv
		}
		g.onGrave = func(c *fakeCarrier, gv *fakeGrave) {
			for _, p := range c.party {
				p.Dead, p.HP = true, 0
			}
		}
		d := NewRemoteDriver(remotePlan(leg), g, DefaultConfig(), objOpts(), g.clock()) // recover_wipe off
		if err := d.Run(context.Background(), 0); err == nil {
			t.Fatal("the wipe did not end the run")
		}
		check(t, "wipe", g)
	})
}

// A chest that is open already is skipped and counted as skipped, not opened.
func TestBatchCountsSkippedChests(t *testing.T) {
	g := carrierGame(t)
	leg, gs := batchGraves(4, 4, func(i int) string { return "g1" })
	gs[1].state = "active"
	o := runBatches(t, g, leg, gs, objOpts())
	if o.err != nil || len(o.rep.Remote.GraveBatches) != 1 {
		t.Fatalf("%v\n%s", o.err, notesOf(o.rep))
	}
	if b := o.rep.Remote.GraveBatches[0]; b.Opened != 3 || b.Skipped != 1 || b.Size != 4 {
		t.Fatalf("%+v", b)
	}
}

// graves_batch and graves_ramp are refused in observe mode and together (the rpc layer): see cmd/turtle-agent.

// ---- leftover summons ----

func TestLeftoverEntriesParseAndKeepTheirForm(t *testing.T) {
	p := mustPlan(t, eventPlan(`{"loot_object":{"entry":128403,"near":{"x":1,"y":2,"z":3}}}`, `"leftover_entries":[7286,7276],`))
	if fmt.Sprint(p.Legs[0].LeftoverEntries) != "[7286 7276]" {
		t.Fatalf("%v", p.Legs[0].LeftoverEntries)
	}
	out, _ := Marshal(p)
	if !strings.Contains(string(out), `"leftover_entries"`) {
		t.Fatalf("%s", out)
	}
	if _, err := Parse("t.json", []byte(eventPlan(`{"loot_object":{"entry":128403,"near":{"x":1,"y":2,"z":3}}}`, `"leftover_entries":[0],`))); err == nil || !strings.Contains(err.Error(), "leftover_entries[0]") {
		t.Fatalf("%v", err)
	}
	zf := loadPlan(t, zulfarrakPlan)
	if l := zfLeg(t, zf, "zumrah"); fmt.Sprint(l.LeftoverEntries) != "[7286 7276]" {
		t.Fatalf("the Zum'rah leg's leftover_entries: %v", l.LeftoverEntries)
	}
}

// Idle summons left near the next batch (the leftovers of earlier graves) are pulled and fought before the batch's first open.
func TestIdleLeftoversNearTheBatchAreFoughtBeforeTheOpen(t *testing.T) {
	g := carrierGame(t)
	leg, gs := batchGraves(6, 3, func(i int) string { return fmt.Sprintf("g%d", i/3+1) })
	leg.LeftoverEntries = []uint32{7286, 7276}
	for i := 0; i < 3; i++ {
		g.addMob(7286, Point{40 + float32(i), 30, 0}) // idle, 100% hp, not pulled; off the walk, so that the walk's own pulls leave it
	}
	o := runBatches(t, g, leg, gs, objOpts())
	if o.err != nil || o.rep.State != "done" {
		t.Fatalf("%v\n%s", o.err, notesOf(o.rep))
	}
	if len(o.killedAtOpen) == 0 || o.killedAtOpen[0] < 3 {
		t.Fatalf("hostiles dead at the first open: %v\n%s", o.killedAtOpen, notesOf(o.rep))
	}
	if !strings.Contains(notesOf(o.rep), "leftover sweep before the batch: 3 idle summon(s)") || o.rep.Remote.LeftoverPulls < 1 {
		t.Fatalf("%s", notesOf(o.rep))
	}
}

// Summons a batch's fight-out leaves idle are fought after the batch, before the rest gate, so that the next batch's approach does not pull them all.
func TestIdleLeftoversAfterABatchAreSweptBeforeTheNextBatch(t *testing.T) {
	g := carrierGame(t)
	leg, gs := batchGraves(6, 3, func(i int) string { return fmt.Sprintf("g%d", i/3+1) })
	leg.LeftoverEntries = []uint32{7286}
	g.graves = map[packet.GUID]*fakeGrave{}
	for _, gv := range gs {
		g.graves[gv.guid] = gv
	}
	opens := 0
	g.onGrave = func(c *fakeCarrier, gv *fakeGrave) {
		opens++
		c.addMob(7286, Point{gv.at.X + 4, 30, 0}) // a summon that stays where it is, idle
	}
	cfg := DefaultConfig()
	cfg.PostFight, cfg.AddsWait = 2*time.Second, 4*time.Second
	d := NewRemoteDriver(remotePlan(leg), g, cfg, objOpts(), g.clock())
	err := d.Run(context.Background(), 0)
	rep, _ := d.Status()
	if err != nil || rep.State != "done" {
		t.Fatalf("%v\n%s", err, notesOf(rep))
	}
	if len(g.mobs) != 0 || g.mobsKilled != 6 {
		t.Fatalf("mobs left %d, killed %d\n%s", len(g.mobs), g.mobsKilled, notesOf(rep))
	}
	if !strings.Contains(notesOf(rep), "leftover sweep after the batch") {
		t.Fatalf("%s", notesOf(rep))
	}
}

// A leg without leftover_entries sweeps nothing; a summon of another entry is left alone.
func TestLeftoverSweepOnlyTakesTheLegsEntries(t *testing.T) {
	g := carrierGame(t)
	leg, gs := batchGraves(3, 3, func(i int) string { return "g1" })
	m := g.addMob(299, Point{40, 30, 0})
	o := runBatches(t, g, leg, gs, objOpts())
	if o.err != nil || strings.Contains(notesOf(o.rep), "leftover sweep") || m.hp <= 0 {
		t.Fatalf("%v\n%s", o.err, notesOf(o.rep))
	}
	g = carrierGame(t)
	leg, gs = batchGraves(3, 3, func(i int) string { return "g1" })
	leg.LeftoverEntries = []uint32{7286}
	m = g.addMob(299, Point{40, 30, 0})
	o = runBatches(t, g, leg, gs, objOpts())
	if o.err != nil || strings.Contains(notesOf(o.rep), "pulling") || m.hp <= 0 {
		t.Fatalf("%v\n%s", o.err, notesOf(o.rep))
	}
}

// The sweep is bounded: a leftover the party cannot pull is tried once, the sweep says so and the run goes on.
func TestLeftoverSweepIsBounded(t *testing.T) {
	g := carrierGame(t)
	leg, gs := batchGraves(3, 3, func(i int) string { return "g1" })
	leg.LeftoverEntries = []uint32{7286}
	for i := 0; i < 12; i++ {
		g.addMob(7286, Point{40 + float32(i), 30 + float32(i), 0})
	}
	g.script = map[string][]tbm.OrderResult{"pullback": repeatOrder(errOrder("cmd pullback x", "unreachable"), 100)}
	o := runBatches(t, g, leg, gs, objOpts())
	if o.rep.Remote.LeftoverPulls < 1 || o.rep.Remote.LeftoverPulls > 2*leftoverRounds {
		t.Fatalf("pulls %d\n%s", o.rep.Remote.LeftoverPulls, notesOf(o.rep))
	}
	if !strings.Contains(notesOf(o.rep), "leftover sweep before the batch") {
		t.Fatalf("%s", notesOf(o.rep))
	}
}

func repeatOrder(r tbm.OrderResult, n int) []tbm.OrderResult {
	out := make([]tbm.OrderResult, n)
	for i := range out {
		out[i] = r
	}
	return out
}

// Idle leftovers near the trigger's stand are fought before the trigger is sent (and so before Zum'rah wakes).
func TestIdleLeftoversAreFoughtBeforeTheAreaTrigger(t *testing.T) {
	g := newTriggerGame(t, 3)
	g.moduleTakes = true
	z := g.addMob(7286, Point{104, 20, 0}) // idle, within 35 yd of the stand point 104 0 0, off the walk to the leg's To 80 0 0
	leg := zumrahLeg("auto", 15)
	leg.LeftoverEntries = []uint32{7286, 7276}
	rep, err := runTrigger(t, g, leg)
	if err != nil || rep.State != "done" {
		t.Fatalf("%v\n%s", err, notesOf(rep))
	}
	if z.hp > 0 || !strings.Contains(notesOf(rep), "leftover sweep before the trigger") || rep.Remote.LeftoverPulls < 1 {
		t.Fatalf("zombie hp %v\n%s", z.hp, notesOf(rep))
	}
	// And the trigger came after: Zum'rah was killed last (two hostiles died in all).
	if g.mobsKilled != 2 {
		t.Errorf("killed %d", g.mobsKilled)
	}
}

// The leftover selector takes hostile units only: not friendly (f), not neutral (n), not non-attackable (kind u) or any other bystander; the
// legacy empty reaction (an older module) and h or - count as hostile.
func TestLeftoverSelectorTakesOnlyHostileAttackableUnits(t *testing.T) {
	g := carrierGame(t)
	d := NewRemoteDriver(remotePlan(Leg{Name: "a", To: Point{0, 0, 0}}), g, DefaultConfig(), objOpts(), g.clock())
	mk := func(n int, reaction, kind string) tbm.HostileRec {
		return tbm.HostileRec{GUID: packet.GUID(0xF130001C76000000 + uint64(n)), Entry: 7286, X: 10, Y: 0, HPPct: 100, Reaction: reaction, Kind: kind, Flags: "-"}
	}
	d.fr = tbm.ViewFrame{Hostiles: []tbm.HostileRec{mk(1, "f", ""), mk(2, "n", ""), mk(3, "h", "u"), mk(4, "", ""), mk(5, "h", ""), mk(6, "-", ""), mk(7, "h", "k"), mk(8, "h", "g")}}
	var got []int
	for _, h := range d.idleLeftovers([]uint32{7286}, Point{0, 0, 0}, map[packet.GUID]bool{}) {
		got = append(got, int(h.GUID&0xFF))
	}
	slices.Sort(got)
	if fmt.Sprint(got) != "[4 5 6]" {
		t.Fatalf("selected %v: want the legacy empty, h and - reactions only", got)
	}
	// A unit that is evading, dead or too far is not taken either.
	far, dead, evading := mk(9, "h", ""), mk(10, "h", ""), mk(11, "h", "")
	far.X, dead.HPPct, evading.Flags = 80, 0, "e"
	d.fr = tbm.ViewFrame{Hostiles: []tbm.HostileRec{far, dead, evading}}
	if got := d.idleLeftovers([]uint32{7286}, Point{0, 0, 0}, map[packet.GUID]bool{}); len(got) != 0 {
		t.Fatalf("%+v", got)
	}
}

// A wipe in a leftover sweep is a wipe in the graves: with the leg's first recovery already used it is not recovered, so the last slot stays for
// the boss. Before a batch, and before the trigger.
func TestWipeInALeftoverSweepKeepsTheBossSlot(t *testing.T) {
	wipeOnFight := func(f *fakeRemote) {
		for _, m := range f.mobs {
			if m.engaged || m.pulled {
				for _, p := range f.party {
					p.Dead, p.HP = true, 0
				}
				return
			}
		}
	}
	t.Run("before a batch", func(t *testing.T) {
		g := carrierGame(t)
		leg, gs := batchGraves(3, 3, func(i int) string { return "g1" })
		leg.LeftoverEntries = []uint32{7286}
		g.addMob(7286, Point{40, 30, 0})
		g.onTick = wipeOnFight
		g.graves = map[packet.GUID]*fakeGrave{}
		for _, gv := range gs {
			g.graves[gv.guid] = gv
		}
		cfg := DefaultConfig()
		cfg.RecoverWipe = true
		d := NewRemoteDriver(remotePlan(leg), g, cfg, objOpts(), g.clock())
		d.wipeRecoverLeg = map[int]int{0: 1} // the leg's first recovery is used already
		err := d.Run(context.Background(), 0)
		rep, _ := d.Status()
		if err == nil || rep.State != "failed" || !strings.Contains(err.Error(), "wipe in the graves: not recovered") || d.wipeRecoverLeg[0] != 1 || len(g.revived) != 0 {
			t.Fatalf("%v %s recoveries %v revived %v\n%s", err, rep.State, d.wipeRecoverLeg, g.revived, notesOf(rep))
		}
	})
	t.Run("before the trigger", func(t *testing.T) {
		g := newTriggerGame(t, 3)
		g.moduleTakes = true
		g.addMob(7286, Point{104, 20, 0})
		g.onTick = func(f *fakeRemote) {
			wipeOnFight(f)
			l := f.lead()
			if g.moduleTakes && !g.fired && !l.Dead && dist3(Point{l.X, l.Y, l.Z}, g.center) <= float64(g.radius) {
				g.fire()
			}
		}
		leg := zumrahLeg("auto", 15)
		leg.LeftoverEntries = []uint32{7286}
		cfg := DefaultConfig()
		cfg.PostFight, cfg.AddsWait = 2*time.Second, 4*time.Second
		cfg.RecoverWipe = true
		cfg.Setup = []string{"gm visible off"}
		d := NewRemoteDriver(remotePlan(leg), g, cfg, objOpts(), g.clock())
		d.wipeRecoverLeg = map[int]int{0: 1}
		err := d.Run(context.Background(), 0)
		rep, _ := d.Status()
		if err == nil || rep.State != "failed" || !strings.Contains(err.Error(), "wipe in the graves: not recovered") || d.wipeRecoverLeg[0] != 1 || len(g.revived) != 0 {
			t.Fatalf("%v %s recoveries %v revived %v\n%s", err, rep.State, d.wipeRecoverLeg, g.revived, notesOf(rep))
		}
	})
	// With a slot to spare the same wipe is recovered like any other.
	t.Run("a spare slot recovers", func(t *testing.T) {
		g := carrierGame(t)
		leg, gs := batchGraves(3, 3, func(i int) string { return "g1" })
		leg.LeftoverEntries = []uint32{7286}
		g.addMob(7286, Point{40, 30, 0})
		once := false
		g.onTick = func(f *fakeRemote) {
			if !once {
				for _, m := range f.mobs {
					if m.engaged || m.pulled {
						once = true
						wipeOnFight(f)
						return
					}
				}
			}
		}
		o := runBatches(t, g, leg, gs, objOpts())
		if o.err != nil || o.d.wipeRecoverLeg[0] != 1 {
			t.Fatalf("%v recoveries %v\n%s", o.err, o.d.wipeRecoverLeg, notesOf(o.rep))
		}
	})
}

// ---- opens that did not open (zf-r4-s5) ----

// A grave the server says is not on the map ("No game object with that GUID is on the bot's map") while the carrier is alive on the plan's map
// is gone: it is skipped and counted as skipped, not as opened, and no agent fallback is tried for it.
func TestInvalidTargetOnAGraveIsGoneNotOpened(t *testing.T) {
	g := carrierGame(t)
	leg, gs := batchGraves(3, 3, func(i int) string { return "g1" })
	g.script = map[string][]tbm.OrderResult{"open": {errOrder("cmd open", "invalid-target"), errOrder("cmd open", "invalid-target")}}
	o := runBatches(t, g, leg, gs, objOpts())
	if o.err != nil || len(o.rep.Remote.GraveBatches) != 1 {
		t.Fatalf("%v\n%s", o.err, notesOf(o.rep))
	}
	b := o.rep.Remote.GraveBatches[0]
	if b.Opened != 1 || b.Skipped != 2 || b.Size != 3 { // the two refused ones are gone, the third opens
		t.Fatalf("%+v\n%s", b, notesOf(o.rep))
	}
	if !strings.Contains(notesOf(o.rep), "it is gone (opened and despawned, or not spawned); skipped, not counted as opened") || strings.Contains(notesOf(o.rep), "the agent takes over") {
		t.Fatalf("%s", notesOf(o.rep))
	}
}

// An invalid-target while the carrier is not on the plan's map (a ghost outside) is not "gone": the usual fallback applies.
func TestInvalidTargetWhileTheCarrierIsOutsideIsNotGone(t *testing.T) {
	g := carrierGame(t)
	leg, gs := batchGraves(3, 3, func(i int) string { return "g1" })
	g.script = map[string][]tbm.OrderResult{"open": {errOrder("cmd open", "invalid-target"), errOrder("cmd open", "invalid-target")}}
	g.memberMap = map[packet.GUID]uint32{gMage: 1} // Magey, the carrier, is on map 1
	o := runBatches(t, g, leg, gs, objOpts())
	if strings.Contains(notesOf(o.rep), "it is gone") {
		t.Fatalf("%s", notesOf(o.rep))
	}
}

// The agent's fallback opens a grave and takes nothing: the grave is not counted as opened.
func TestAgentFallbackThatTakesNothingIsNotAnOpen(t *testing.T) {
	g := newHandsGame(t, 3)
	g.emptyChest = true
	leg, gs := batchGraves(3, 3, func(i int) string { return "g1" })
	g.script["open"] = openFails("failed", 2)
	g.gobjs = []objects.GameObject{{GUID: gs[0].guid, Entry: 128403, X: gs[0].at.X, Y: gs[0].at.Y, Z: gs[0].at.Z, Distance: 10, State: "ready"}}
	g.graves = map[packet.GUID]*fakeGrave{}
	for _, gv := range gs {
		g.graves[gv.guid] = gv
	}
	rep, err := handsRun(t, g, remotePlan(leg), nil)
	if err != nil || len(rep.Remote.GraveBatches) != 1 {
		t.Fatalf("%v\n%s", err, notesOf(rep))
	}
	if b := rep.Remote.GraveBatches[0]; b.Opened != 2 || b.Skipped != 1 {
		t.Fatalf("%+v\n%s", b, notesOf(rep))
	}
	if !strings.Contains(notesOf(rep), "the agent took nothing from the object") {
		t.Fatalf("%s", notesOf(rep))
	}
}

// Every leftover sweep leaves a note, also when it found nothing, and is counted.
func TestLeftoverSweepNotesAlsoWhenNothingIsIdle(t *testing.T) {
	g := carrierGame(t)
	leg, gs := batchGraves(6, 3, func(i int) string { return fmt.Sprintf("g%d", i/3+1) })
	leg.LeftoverEntries = []uint32{7286, 7276}
	o := runBatches(t, g, leg, gs, objOpts())
	if o.err != nil || !strings.Contains(notesOf(o.rep), "leftover sweep before the batch: no idle summon of [7286 7276]") || !strings.Contains(notesOf(o.rep), "leftover sweep after the batch: no idle summon") {
		t.Fatalf("%v\n%s", o.err, notesOf(o.rep))
	}
	if o.rep.Remote.LeftoverSweeps != 4 { // before and after each of the two batches
		t.Fatalf("sweeps %d", o.rep.Remote.LeftoverSweeps)
	}
}

// One summary note ends the graves.
func TestGravesSummaryNoteEndsTheSection(t *testing.T) {
	g := carrierGame(t)
	leg, gs := batchGraves(16, 3, func(i int) string { return "g1" })
	opt := objOpts()
	opt.GravesRamp = &GravesRamp{}
	o := runBatches(t, g, leg, gs, opt, 8) // a wipe in the batch of 4
	if o.err != nil {
		t.Fatalf("%v\n%s", o.err, notesOf(o.rep))
	}
	n := notesOf(o.rep)
	if strings.Count(n, "graves-summary:") != 1 || !strings.Contains(n, "mode=ramp") || !strings.Contains(n, "max_clean_size=3") || !strings.Contains(n, "wiped_at_size=4") || !strings.Contains(n, "wipes=1") {
		t.Fatalf("%s", n)
	}
	// It comes after the last batch's note.
	if strings.Index(n, "graves-summary:") < strings.LastIndex(n, "graves-batch: n=") {
		t.Fatalf("%s", n)
	}
}

// ---- graves_open_by agent ----

// agentHandsGame is a handsGame whose agent can `.go xyz`.
type agentHandsGame struct {
	*handsGame
	teles   []Point
	goCalls int
	goFail  map[int]error // the nth GoXYZ call (from 1) fails with this error and does not move the agent
	goCtx   []error       // ctx.Err() of each GoXYZ call that moved the agent
}

func (g *agentHandsGame) GoXYZ(ctx context.Context, mapID uint32, at Point) error {
	g.goCalls++
	if err := g.goFail[g.goCalls]; err != nil {
		return err
	}
	g.teles = append(g.teles, at)
	g.goCtx = append(g.goCtx, ctx.Err())
	g.agent = at
	g.agentMap = mapID
	g.log = append(g.log, fmt.Sprintf("go %.0f", at.X))
	return nil
}

func agentGravesRun(t *testing.T, g *agentHandsGame, leg Leg, gs []*fakeGrave, level int, setup []string) (*RemoteDriver, Report, error) {
	t.Helper()
	return agentGravesRunCtx(t, context.Background(), g, leg, gs, level, setup)
}

func agentGravesRunCtx(t *testing.T, ctx context.Context, g *agentHandsGame, leg Leg, gs []*fakeGrave, level int, setup []string) (*RemoteDriver, Report, error) {
	t.Helper()
	g.level = level
	plan := remotePlan(leg)
	g.agentMap = plan.Map
	g.agent = Point{-300, 0, 0}
	g.graves = map[packet.GUID]*fakeGrave{}
	for _, gv := range gs {
		g.graves[gv.guid] = gv
	}
	cfg := DefaultConfig()
	cfg.PostFight, cfg.AddsWait = 2*time.Second, 4*time.Second
	cfg.RecoverWipe = true
	cfg.Setup = setup
	o := objOpts()
	o.GravesOpenBy = GravesOpenAgent
	d := NewRemoteDriver(plan, g, cfg, o, g.clock())
	err := d.Run(ctx, 0)
	rep, _ := d.Status()
	return d, rep, err
}

// graves_open_by agent: the agent's own GM character opens every chest of a batch (`.go xyz` to each, its Opening cast), with no order to a
// carrier, notes one GM assist per open, goes back out of the fight spot after the last, and the party fights afterwards.
func TestAgentOpensAllChestsOfABatchWithoutCarrierOrders(t *testing.T) {
	g := &agentHandsGame{handsGame: newHandsGame(t, 3)}
	leg, gs := batchGraves(3, 3, func(i int) string { return "g1" })
	for i := 0; i < 3; i++ {
		g.addMob(7286, Point{40 + float32(i), 30, 0})
	}
	g.onLoot = func(h *handsGame) {
		h.addMob(7286, Point{36, 5, 0}) // what the grave brings
	}
	_, rep, err := agentGravesRun(t, g, leg, gs, 3, []string{"gm visible off"})
	if err != nil || rep.State != "done" {
		t.Fatalf("%v\n%s", err, notesOf(rep))
	}
	if n := len(g.ordersOf("cmd open")); n != 0 {
		t.Fatalf("%d cmd open orders in agent mode: %v", n, g.orders)
	}
	if g.loots != 3 || rep.Remote.AgentOpens != 3 {
		t.Fatalf("loots %d agent opens %d\n%s", g.loots, rep.Remote.AgentOpens, notesOf(rep))
	}
	if len(rep.Remote.GraveBatches) != 1 {
		t.Fatalf("%+v", rep.Remote.GraveBatches)
	}
	b := rep.Remote.GraveBatches[0]
	if b.Opened != 3 || b.Skipped != 0 || b.OpenedBy != "agent" || !strings.Contains(notesOf(rep), "open_by=agent") {
		t.Fatalf("%+v", b)
	}
	// One `.go xyz` per open, and one back out of the fight spot after the last, each a GM assist in setup_shortcuts and run_gm_assists.
	goes := 0
	for _, c := range rep.SetupShortcuts {
		if strings.HasPrefix(c, ".go xyz") && strings.HasSuffix(c, "(GM assist during the run)") {
			goes++
		}
	}
	if goes != 4 || len(g.teles) != 4 {
		t.Fatalf("go xyz %d / %d: %v", goes, len(g.teles), rep.SetupShortcuts)
	}
	if strings.Count(notesOf(rep), "a GM assist during the run (run_gm_assists; not counted in gm_commands)") < 4 {
		t.Fatalf("%s", notesOf(rep))
	}
	// The opens come before the fight: the three loots happen before the log shows anything else but go xyz, and the last `.go` is the way back.
	last := g.log[len(g.log)-1]
	if !strings.HasPrefix(last, "go") {
		// the last log entry may be a later GM line; the way back must still come after the last loot
		lastLoot, lastGo := -1, -1
		for i, e := range g.log {
			if e == "loot" {
				lastLoot = i
			}
			if strings.HasPrefix(e, "go") {
				lastGo = i
			}
		}
		if lastGo < lastLoot {
			t.Fatalf("no .go xyz after the last loot: %v", g.log)
		}
	}
}

// graves_open_by agent is refused up front, before anything is sent, without a GM account, and without "gm visible off" in the setup.
func TestAgentOpenIsRefusedWithoutGMOrInvisibility(t *testing.T) {
	leg, gs := batchGraves(3, 3, func(i int) string { return "g1" })
	g := &agentHandsGame{handsGame: newHandsGame(t, 0)}
	d, _, err := agentGravesRun(t, g, leg, gs, 0, nil)
	if err == nil || !strings.Contains(err.Error(), "needs a GM account") || len(g.orders) != 0 || d == nil {
		t.Fatalf("%v orders %v", err, g.orders)
	}
	g = &agentHandsGame{handsGame: newHandsGame(t, 3)}
	_, _, err = agentGravesRun(t, g, leg, gs, 3, nil)
	if err == nil || !strings.Contains(err.Error(), `"gm visible off"`) || len(g.orders) != 0 {
		t.Fatalf("%v orders %v", err, g.orders)
	}
	// A game without the agent's hands is refused too.
	cg := carrierGame(t)
	cg.graves = map[packet.GUID]*fakeGrave{}
	o := objOpts()
	o.GravesOpenBy = GravesOpenAgent
	dd := NewRemoteDriver(remotePlan(leg), cg, DefaultConfig(), o, cg.clock())
	if err := dd.Run(context.Background(), 0); err == nil || !strings.Contains(err.Error(), "no hands") || len(cg.orders) != 0 {
		t.Fatalf("%v orders %v", err, cg.orders)
	}
	// The default (carrier) needs none of it.
	g = &agentHandsGame{handsGame: newHandsGame(t, 0)}
	if err := (&RemoteDriver{plan: remotePlan(leg), g: g, opt: objOpts()}).checkGravesOpenBy(context.Background()); err != nil {
		t.Fatalf("%v", err)
	}
}

// ---- a pack out of view (zf-r4-s4) ----

// shortPathFake plans a path that ends a few yards after its start whatever the goal (a pack off the mesh: the path stops short, and the
// look point is the lead bot's own spot).
type shortPathFake struct{ *fakeRemote }

func (g shortPathFake) PlanPath(_ context.Context, _ uint32, from, to Point) ([]Point, error) {
	return []Point{from, lerp(from, to, 3/dist3(from, to))}, nil
}

// A pack the approach could not get a look point for (the path ends short of it, the lead bot far away) is walked toward, straight at it, before
// it is given up on: the run does not fail "could not get a view of the pack" with no order sent.
func TestPackOutOfViewIsWalkedTowardBeforeGivingUp(t *testing.T) {
	g := shortPathFake{newFakeRemote(t)}
	at := Point{91, 0, 0}
	plan := remotePlan(Leg{Name: "a", To: Point{0, 0, 0}, PullGroups: []PullGroup{{Entries: []uint32{299}, Positions: []Point{at}}}})
	d := NewRemoteDriver(plan, g, DefaultConfig(), opts(), g.clock())
	ctx := context.Background()
	if err := d.begin(ctx, 0); err != nil {
		t.Fatal(err)
	}
	d.curLeg = 0
	sp := fightSpec{pick: []uint32{299}, track: []uint32{299}, anchors: []Point{at}}
	cleared, why, err := d.confirmGone(ctx, sp, newClearing())
	if err != nil || !cleared {
		t.Fatalf("cleared %v (%s) %v\n%s", cleared, why, err, strings.Join(d.Report().Notes, "\n"))
	}
	if l := g.lead(); dist3(Point{l.X, l.Y, l.Z}, at) > float64(ClearSeeRadius) {
		t.Fatalf("the lead bot stands %.0f yd from the pack", dist3(Point{l.X, l.Y, l.Z}, at))
	}
	if !strings.Contains(strings.Join(d.Report().Notes, "\n"), "walking toward it") {
		t.Fatalf("%v", d.Report().Notes)
	}
}

// A healer that ends a batch nearly out of mana gets a mana gate before the next batch, with the rest gate's healer cap given back (the cap
// used once in a leg switches the healer's mana wait off for the later gates: zf-r4-s4, three batches at 0 % mana).
func TestHealerLowOnManaGetsAGateBeforeTheNextBatch(t *testing.T) {
	g := carrierGame(t)
	g.party[1].Power = 100 // Healy at 10 % mana, and it never rises
	leg, gs := batchGraves(6, 3, func(i int) string { return fmt.Sprintf("g%d", i/3+1) })
	o := runBatches(t, g, leg, gs, objOpts())
	if o.err != nil || o.rep.State != "done" {
		t.Fatalf("%v\n%s", o.err, notesOf(o.rep))
	}
	n := notesOf(o.rep)
	if strings.Count(n, "mana gate before the next batch") != 1 {
		t.Fatalf("%s", n)
	}
	// The gate ran between the two batches: its own outcome note stands between the gate note and the second batch's begin.
	i := strings.Index(n, "mana gate before the next batch")
	j := strings.Index(n[i:], "graves-batch begin")
	if i < 0 || j < 0 || !strings.Contains(n[i:i+j], "rest gate") {
		t.Fatalf("no rest gate between the batches\n%s", n)
	}
}

// A healer with mana is not waited for.
func TestHealerWithManaGetsNoExtraGate(t *testing.T) {
	g := carrierGame(t)
	leg, gs := batchGraves(6, 3, func(i int) string { return fmt.Sprintf("g%d", i/3+1) })
	o := runBatches(t, g, leg, gs, objOpts())
	if o.err != nil || strings.Contains(notesOf(o.rep), "mana gate before the next batch") {
		t.Fatalf("%v\n%s", o.err, notesOf(o.rep))
	}
}

// ---- the agent goes home whatever way an open ends ----

var agentHomeSpot = Point{-300, 0, 0} // where agentGravesRun starts the agent, on the plan's map: the home of a batch

// A failed `.go xyz` to the grave, a failed Opening cast, a cancel and a wipe in the middle of a batch each end with the agent moved back to
// where it stood before the batch.
func TestAgentIsBroughtHomeWhenAnOpenFails(t *testing.T) {
	boom := errors.New("boom")
	cases := map[string]func(t *testing.T) (*agentHandsGame, *RemoteDriver, Report, error){
		"go xyz fails": func(t *testing.T) (*agentHandsGame, *RemoteDriver, Report, error) {
			g := &agentHandsGame{handsGame: newHandsGame(t, 3)}
			leg, gs := batchGraves(3, 3, func(i int) string { return "g1" })
			g.goFail = map[int]error{1: boom} // the first move to a grave; the way back is call 2
			d, rep, err := agentGravesRun(t, g, leg, gs, 3, []string{"gm visible off"})
			return g, d, rep, err
		},
		"the cast fails": func(t *testing.T) (*agentHandsGame, *RemoteDriver, Report, error) {
			g := &agentHandsGame{handsGame: newHandsGame(t, 3)}
			g.lootErrs = []error{boom, boom, boom}
			leg, gs := batchGraves(3, 3, func(i int) string { return "g1" })
			d, rep, err := agentGravesRun(t, g, leg, gs, 3, []string{"gm visible off"})
			return g, d, rep, err
		},
		"a cancel mid-batch": func(t *testing.T) (*agentHandsGame, *RemoteDriver, Report, error) {
			g := &agentHandsGame{handsGame: newHandsGame(t, 3)}
			ctx, cancel := context.WithCancel(context.Background())
			n := 0
			g.onLoot = func(h *handsGame) {
				if n++; n == 2 { // the second chest's cast is cut by the cancel
					cancel()
					h.lootErrs = []error{context.Canceled, context.Canceled, context.Canceled}
				}
			}
			leg, gs := batchGraves(3, 3, func(i int) string { return "g1" })
			d, rep, err := agentGravesRunCtx(t, ctx, g, leg, gs, 3, []string{"gm visible off"})
			return g, d, rep, err
		},
		"a wipe mid-batch": func(t *testing.T) (*agentHandsGame, *RemoteDriver, Report, error) {
			g := &agentHandsGame{handsGame: newHandsGame(t, 3)}
			n := 0
			g.onLoot = func(h *handsGame) {
				if n++; n == 2 {
					for _, p := range h.party {
						p.Dead, p.HP = true, 0
					}
					h.lootErrs = []error{boom, boom, boom}
				}
			}
			leg, gs := batchGraves(3, 3, func(i int) string { return "g1" })
			d, rep, err := agentGravesRun(t, g, leg, gs, 3, []string{"gm visible off"})
			return g, d, rep, err
		},
	}
	for name, run := range cases {
		t.Run(name, func(t *testing.T) {
			g, d, rep, err := run(t)
			_ = err
			if len(g.teles) == 0 {
				t.Fatalf("the agent never moved\n%s", notesOf(rep))
			}
			if last := g.teles[len(g.teles)-1]; last != agentHomeSpot {
				t.Fatalf("the agent ends at %v, not home %v (moves %v)\n%s", last, agentHomeSpot, g.teles, notesOf(rep))
			}
			if g.agent != agentHomeSpot {
				t.Fatalf("the agent stands at %v", g.agent)
			}
			if d.agentAway != nil {
				t.Fatalf("the driver still thinks the agent is away: %+v", d.agentAway)
			}
			// The way back never runs on a cancelled context.
			for i, e := range g.goCtx {
				if e != nil && g.teles[i] == agentHomeSpot {
					t.Fatalf("the way home ran with a cancelled context: %v", e)
				}
			}
			// The first move home comes right away: after the one chest's move that worked (none when the move itself failed).
			first := slices.Index(g.teles, agentHomeSpot)
			if first < 0 || first > 2 {
				t.Fatalf("the first way home is move %d: %v", first, g.teles)
			}
		})
	}
}

// A way home that fails too is a degraded run, with where the agent was left.
func TestAgentHomeThatFailsMakesTheRunDegraded(t *testing.T) {
	boom := errors.New("boom")
	g := &agentHandsGame{handsGame: newHandsGame(t, 3)}
	g.lootErrs = []error{boom, boom, boom}
	g.goFail = map[int]error{2: boom} // the move to the grave works, the way home does not
	leg, gs := batchGraves(3, 3, func(i int) string { return "g1" })
	_, rep, _ := agentGravesRun(t, g, leg, gs, 3, []string{"gm visible off"})
	if !strings.Contains(strings.Join(rep.Degraded, "|"), "agent left away from its home") {
		t.Fatalf("degraded %v\n%s", rep.Degraded, notesOf(rep))
	}
}

// Home is the spot the agent stood at before the batch when that is on the plan's map; when the agent started outside the instance it is the
// party's safe spot, where the lead bot stood when the batch began (not the batch's centre, the fight spot).
func TestAgentHomeIsThePreBatchSpot(t *testing.T) {
	g := &agentHandsGame{handsGame: newHandsGame(t, 3)}
	leg, gs := batchGraves(3, 3, func(i int) string { return "g1" })
	_, rep, err := agentGravesRun(t, g, leg, gs, 3, []string{"gm visible off"})
	if err != nil || len(g.teles) == 0 || g.teles[len(g.teles)-1] != agentHomeSpot || !strings.Contains(notesOf(rep), "where it stood before the batch") {
		t.Fatalf("%v teles %v\n%s", err, g.teles, notesOf(rep))
	}
	// From outside the instance (another map): the lead bot's spot at the batch's start.
	g = &agentHandsGame{handsGame: newHandsGame(t, 3)}
	leg, gs = batchGraves(3, 3, func(i int) string { return "g1" })
	g.onAppear = func(n *nearGame) { n.agentMap = 70 } // placeholder, replaced below
	g.onAppear = nil
	plan := remotePlan(leg)
	g.level = 3
	g.agent, g.agentMap = Point{-300, 0, 0}, 1 // outside
	g.graves = map[packet.GUID]*fakeGrave{}
	for _, gv := range gs {
		g.graves[gv.guid] = gv
	}
	g.onAppear = func(n *nearGame) { n.agentMap = plan.Map } // the `.appear` puts it in the instance
	cfg := DefaultConfig()
	cfg.PostFight, cfg.AddsWait = 2*time.Second, 4*time.Second
	cfg.Setup = []string{"gm visible off"}
	o := objOpts()
	o.GravesOpenBy = GravesOpenAgent
	d := NewRemoteDriver(plan, g, cfg, o, g.clock())
	if err := d.Run(context.Background(), 0); err != nil {
		t.Fatal(err)
	}
	rep2, _ := d.Status()
	last := g.teles[len(g.teles)-1]
	if !strings.Contains(notesOf(rep2), "the party's safe spot") || last.X >= 30 || last.X < 15 || dist3(last, Point{33, 0, 0}) < 5 {
		t.Fatalf("home %v (the batch centre is 33 0 0)\n%s", last, notesOf(rep2))
	}
}
