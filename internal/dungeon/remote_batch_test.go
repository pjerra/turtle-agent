package dungeon

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

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
	l := p.Legs[len(p.Legs)-1]
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
	if total != 28 || len(order) < 5 {
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
