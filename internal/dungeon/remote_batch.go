package dungeon

import (
	"context"
	"errors"
	"fmt"
	"math"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/pjerra/turtle-agent/internal/packet"
	"github.com/pjerra/turtle-agent/internal/tbm"
)

// Batched chest opening (Zum'rah's graves, owner 2026-10-15: "make them open 3-6 graveyards, then use aoe to kill them", then "test how many
// graveyards they can open before they wipe"). The loot_object steps of a section (consecutive steps with a batch id) are opened in batches:
// the party gathers at the batch's centre, the carrier opens every chest of the batch back to back with no fight between the opens, then the
// party fights out everything that came out, with the bots' area spells on. How many chests a batch has is the plan's (each batch id), or
// the run's own: graves_batch (a fixed size) or graves_ramp (start, step, max: one more chest after each batch without a wipe, until the first
// wipe). Each batch leaves one note that starts with graves-batch: and a GraveBatch record in the report.

// GravesRamp grows the batch size run-time: Start chests in the first batch, Step more after each batch that did not wipe, up to Max
// (0: no limit but the chests that lie together).
type GravesRamp struct {
	Start int `json:"start,omitempty"` // default 2
	Step  int `json:"step,omitempty"`  // default 1
	Max   int `json:"max,omitempty"`   // 0: unlimited
}

// Defaults of GravesRamp.
const (
	GravesRampStart = 2
	GravesRampStep  = 1
)

// GraveBatch is the record of one batch (RemoteReport.GraveBatches; the note "graves-batch: ..." says the same).
type GraveBatch struct {
	Leg      int     `json:"leg"`
	N        int     `json:"n"`    // the batch's number in the run, from 1
	Mode     string  `json:"mode"` // plan, fixed or ramp
	Wanted   int     `json:"wanted"`
	Size     int     `json:"size"`    // the chests the batch took
	Opened   int     `json:"opened"`  // chests opened
	Skipped  int     `json:"skipped"` // chests that were open already or gone
	Adds     int     `json:"adds"`    // distinct hostiles that fought the party in the batch
	FightS   float64 `json:"fight_s"`
	Deaths   int     `json:"deaths"`
	Wipe     bool    `json:"wipe"`
	TankMin  int     `json:"tank_min_pct"`   // lowest health % seen of the tank (-1: no tank known)
	HealMin  int     `json:"healer_min_pct"` // lowest health % seen of the healer (-1: none known)
	HealMana int     `json:"healer_mana_end_pct"`
	AoE      bool    `json:"aoe"` // the area spells were on for the fight
	// ThreatWaitS is how long the area spells waited for the tank's threat lead after the last open, and TankHeld whether it had it.
	ThreatWaitS float64 `json:"threat_wait_s"`
	TankHeld    bool    `json:"tank_held"`
	RadiusYd    float64 `json:"radius_yd"`
	Centre      Point   `json:"centre"`
	// CutByRadius says that the batch is shorter than asked for because the next chests were not within BatchRadius of its centre.
	CutByRadius bool   `json:"cut_by_radius"`
	OpenedBy    string `json:"opened_by"` // carrier or agent (graves_open_by)
	Note        string `json:"note,omitempty"`
}

// gravesState is what the batches of a run remember.
type gravesState struct {
	n         int             // batches begun
	size      int             // the ramp's size for the next batch (0: not started)
	lastClean int             // the largest batch that went by without a wipe
	wipes     int             // wipes during batches
	wipedAt   int             // the size that wiped first
	opened    int             // chests opened in all batches
	skipped   int             // chests skipped (open already, gone, or the agent took nothing)
	cuts      int             // batches cut by the radius
	healMana  int             // the healer's mana % at the end of the last batch that did not wipe (-1: not known)
	maxRadius float64         // the widest batch
	aoe       map[string]bool // each bot's area spells as the driver set them (true: on); absent: not set by a batch
}

// batchRun is the batch that is being opened and fought.
type batchRun struct {
	rec     GraveBatch
	left    int // chests still to take
	began   time.Time
	fightAt time.Time
	adds    map[packet.GUID]bool
	deaths0 int
	stayed  bool // the party was told to stay at the centre
	// agentHome is where the agent stood before it opened the batch's chests (graves_open_by agent); it goes back there after the last one.
	// safeSpot is where the lead bot stood when the batch began, before the party walked to the batch's centre: the agent's home when it came
	// from outside the instance.
	safeSpot *Point
	// sectionLeft is how many chests of the section follow this batch (0: it is the last one).
	sectionLeft int
}

// gravesMode names how the batch size is decided.
func (d *RemoteDriver) gravesMode() string {
	switch {
	case d.opt.GravesRamp != nil:
		return "ramp"
	case d.opt.GravesBatch > 0:
		return "fixed"
	}
	return "plan"
}

// rampOf is the ramp with its defaults.
func (d *RemoteDriver) rampOf() GravesRamp {
	r := *d.opt.GravesRamp
	if r.Start <= 0 {
		r.Start = GravesRampStart
	}
	if r.Step <= 0 {
		r.Step = GravesRampStep
	}
	return r
}

// recoverySlotsLeft is how many wipe recoveries the leg has left under its cap.
func (d *RemoteDriver) recoverySlotsLeft() int {
	return d.cfg.MaxWipeRecoveries - d.wipeRecoverLeg[d.curLeg]
}

// slotReserved says that the leg has only the recovery slot left that the boss (Zum'rah) keeps: a wipe in a grave batch is not recovered
// then (stepFailedRecover), and the graves are opened one at a time.
func (d *RemoteDriver) slotReserved() bool {
	return d.cfg.MaxWipeRecoveries >= 2 && d.recoverySlotsLeft() <= 1
}

// batchWanted is how many chests the next batch should take, before the chests that lie together cut it.
func (d *RemoteDriver) batchWanted(a Action) int {
	gb := &d.gb
	if d.slotReserved() {
		return 1 // a wipe in the graves from here on is not recovered, to keep the last slot for the boss: one chest at a time
	}
	wipes := max(gb.wipes, d.wipeRecoverLeg[d.curLeg]) // a wipe earlier in the leg used a recovery slot as much as one in the graves
	switch d.gravesMode() {
	case "fixed":
		return d.opt.GravesBatch
	case "ramp":
		r := d.rampOf()
		if gb.size == 0 {
			gb.size = r.Start
		}
		if wipes >= 1 {
			return max(gb.lastClean, 1) // no growth after a wipe: the last size that went by clean
		}
		if r.Max > 0 {
			return min(gb.size, r.Max)
		}
		return gb.size
	}
	n := 0
	for _, c := range a.Section {
		if c.ID != a.Batch {
			break
		}
		n++
	}
	return n
}

// batchBegin starts a batch at a loot_object step that no batch holds yet: it fights out what is going on, picks the size, walks the party to
// the batch's centre, gathers it, puts the area spells on and notes it.
func (d *RemoteDriver) batchBegin(ctx context.Context, a Action) error {
	if err := d.fightEngagedOut(ctx, a.Label); err != nil {
		return err
	}
	gb := &d.gb
	if gb.n == 0 {
		gb.healMana = -1
		if err := d.restGate(ctx, false); err != nil { // the first batch: the rest gate that follows each fight has not run
			return err
		}
	} else if gb.healMana >= 0 && gb.healMana < batchManaLowPct {
		// The healer ended the last batch nearly out of mana (zf-r4-s4: three batches at 0 %). The rest gate after the fight gives up on a
		// healer-only mana wait after healerManaCap, once per leg, and then no later gate waits for it: this gate gets its cap back and waits
		// for the healer before the next chests are opened.
		d.rep.note(fmt.Sprintf("graves-batch: the healer ended the last batch at %d%% mana (under %d%%): mana gate before the next batch", gb.healMana, batchManaLowPct))
		d.healerCapped, d.floorCapped = 0, 0
		if err := d.restGate(ctx, false); err != nil {
			return err
		}
	}
	if err := d.healerManaWait(ctx); err != nil {
		return err
	}
	wanted := d.batchWanted(a)
	size := min(wanted, len(a.Section))
	size = max(size, 1)
	pts := make([]Point, 0, size)
	for _, c := range a.Section[:size] {
		pts = append(pts, c.At)
	}
	mode := d.gravesMode()
	// The loader's radius holds for a size of the run's own as well: the batch is cut where the next chests are not within BatchRadius of its
	// centre, and the batch says so (cut_by_radius).
	want := len(pts)
	radius := BatchRadius
	if mode != "plan" {
		radius = RunBatchRadius // a size of the run's own is held tight (zf-r5: a batch of 3 spread 12.3 yd wiped)
	}
	for len(pts) > 1 && BatchSpread(pts) > radius {
		pts = pts[:len(pts)-1]
	}
	size = len(pts)
	cut := size < want
	gb.n++
	b := &batchRun{
		left: size, sectionLeft: len(a.Section) - size, began: d.clk.Now(), adds: map[packet.GUID]bool{},
		deaths0: len(d.rep.snapshot().Deaths),
		rec: GraveBatch{Leg: max(d.curLeg, 0), N: gb.n, Mode: mode, Wanted: wanted, Size: size, TankMin: -1, HealMin: -1,
			RadiusYd: BatchSpread(pts), Centre: BatchCentre(pts), OpenedBy: d.gravesOpenBy()},
	}
	b.rec.CutByRadius = cut
	if cut {
		b.rec.Note = fmt.Sprintf("wanted %d, only %d chests lie within %.0f yd of their centre", wanted, size, radius)
	}
	d.rep.note(fmt.Sprintf("graves-batch begin: %d chest(s) around %.0f %.0f %.0f (%s mode, wanted %d, radius %.1f yd)%s", size, b.rec.Centre.X, b.rec.Centre.Y, b.rec.Centre.Z, mode, wanted, b.rec.RadiusYd, ifNote(b.rec.Note)))
	if err := d.sweepLeftovers(ctx, "before the batch", b.rec.Centre); err != nil {
		return err
	}
	d.batch = b
	if p, ok := d.leadPos(); ok {
		b.safeSpot = &p
	}
	d.setAoE(ctx, false) // the opens go with the area spells off: the tank takes the summons first, the spells come on after its threat lead
	if err := d.doWalk(ctx, b.rec.Centre); err != nil {
		if runEnded(ctx, err) {
			return err // a wipe on the walk is recorded by batchWiped
		}
		d.rep.note(fmt.Sprintf("graves-batch: the party could not be walked to the batch's centre: %v; the first chest's approach brings it", err))
	}
	if err := d.gather(ctx, "the grave batch"); err != nil {
		return err
	}
	// The party is held at the centre while the carrier opens the chests (the carrier's own order brings it to each chest and back).
	if r, err := d.order(ctx, "stay"); err != nil {
		return err
	} else if ref, bad := refusedOf(r); bad {
		d.rep.note(fmt.Sprintf("graves-batch: cmd stay was refused: %s (%s)", ref.Code, ref.Message))
	} else {
		b.stayed = true
	}
	return nil
}

func ifNote(s string) string {
	if s == "" {
		return ""
	}
	return ": " + s
}

// setAoE puts the bots' area spells on or off (`.bot behavior <bot> aoe on|off`; on by default in TortoiseBots, and persisted: it survives a
// relog, so endLeg puts it back on, legAoEOn). It sends to the bots whose state the driver does not know to be the wanted one. It reports
// whether every bot is in the wanted state.
func (d *RemoteDriver) setAoE(ctx context.Context, on bool) bool {
	og, ok := d.orderGame()
	if !ok {
		d.rep.note("graves-batch: this game cannot send behavior orders: the bots' area spells are left as they are")
		return false
	}
	if d.gb.aoe == nil {
		d.gb.aoe = map[string]bool{}
	}
	all := true
	for _, m := range d.fr.Party {
		if have, known := d.gb.aoe[m.Name]; known && have == on {
			continue
		}
		if r, err := og.Behavior(ctx, m.Name, "aoe", on); err != nil || r.Kind != tbm.KindAck {
			all = false
			d.rep.note(fmt.Sprintf("graves-batch: could not turn %s's area spells %s: %s", m.Name, onOff(on), behaviorWhy(r, err)))
			continue
		}
		d.gb.aoe[m.Name] = on
	}
	return all
}

// legAoEOn puts the area spells back on for every bot a batch turned them off for (the module's default state, and the switch is persisted: a
// bot left off stays off across relogs). It runs when the leg ends and, with a bounded context of its own, as a defer of the run before the
// unbind, whatever way the run ends (a failure, a cancel, a panic, a wipe nobody recovers from): the orders need the binding.
func (d *RemoteDriver) legAoEOn() {
	var off []string
	for name, on := range d.gb.aoe {
		if !on {
			off = append(off, name)
		}
	}
	if len(off) == 0 {
		return
	}
	sort.Strings(off)
	og, ok := d.orderGame()
	if !ok {
		d.degrade(fmt.Sprintf("area spells (aoe) left off on %s: this game cannot send behavior orders", strings.Join(off, ", ")))
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	var left []string
	for _, n := range off {
		var okOn bool
		for try := 0; try < 2 && !okOn; try++ {
			r, err := og.Behavior(ctx, n, "aoe", true)
			okOn = err == nil && r.Kind == tbm.KindAck
		}
		if okOn {
			d.gb.aoe[n] = true
		} else {
			left = append(left, n)
			delete(d.gb.aoe, n) // given up: the run's last restore (after the unbind) does not try again
		}
	}
	if len(left) > 0 {
		d.degrade(fmt.Sprintf("area spells (aoe) left off on %s after the grave batches", strings.Join(left, ", ")))
		return
	}
	d.rep.note("graves-batch: area spells (aoe) back on for " + strings.Join(off, ", "))
}

// batchThreatLead is the pause between the last open and the area spells: the tank gets its threat lead on what the graves brought (live,
// round 3: the healer held top threat on a mob 343 times, and area spells make it worse). It ends when the tank is at the top of the threat
// of at least 4 in 5 of the engaged hostiles, or after ThreatWait, or when nothing fights the party.
func (d *RemoteDriver) batchThreatLead(ctx context.Context, b *batchRun) error {
	began := d.clk.Now()
	for {
		d.look()
		eng := d.fr.EngagedHostiles()
		if len(eng) == 0 {
			return nil
		}
		if tk, ok := d.tankRec(); ok {
			held := 0
			for _, h := range eng {
				if who, ok := d.fr.ThreatHolder(h.GUID); ok && who == tk.GUID {
					held++
				}
			}
			if held*5 >= len(eng)*4 {
				b.rec.TankHeld = true
				b.rec.ThreatWaitS = d.clk.Now().Sub(began).Seconds()
				return nil
			}
		}
		if d.clk.Now().Sub(began) >= d.opt.ThreatWait {
			b.rec.ThreatWaitS = d.clk.Now().Sub(began).Seconds()
			return nil
		}
		if err := d.tick(ctx, d.cfg.Poll); err != nil {
			return err
		}
	}
}

// batchStepDone counts one chest of the batch (opened, or skipped for being open already) and, after the last one, fights out what came
// out, records the batch and lets the ramp grow.
func (d *RemoteDriver) batchStepDone(ctx context.Context, a Action, skipped bool) error {
	b := d.batch
	if b == nil {
		return nil
	}
	if skipped {
		b.rec.Skipped++
	} else {
		b.rec.Opened++
	}
	b.left--
	if b.left > 0 {
		return nil
	}
	d.agentRestore(ctx, "after the batch's last chest")
	if b.stayed {
		d.resumeFollow(ctx, true) // the fight is with the party free to move: `cmd follow` is the bots' own mode
		b.stayed = false
	}
	if err := d.batchThreatLead(ctx, b); err != nil {
		return err
	}
	b.rec.AoE = d.setAoE(ctx, true)
	if err := d.fightOutOnly(ctx, a.Label); err != nil {
		return err // a wipe is recorded by batchWiped, which stepFailedRecover calls
	}
	if err := d.sweepLeftovers(ctx, "after the batch", b.rec.Centre); err != nil {
		return err
	}
	d.batchFinish(false)
	return d.restGate(ctx, false)
}

// batchFinish records the batch and moves the ramp.
func (d *RemoteDriver) batchFinish(wipe bool) {
	b, gb := d.batch, &d.gb
	if b == nil {
		return
	}
	d.batch = nil
	d.sampleBatchFrame(b)
	r := &b.rec
	r.Wipe = wipe
	r.Adds = len(b.adds)
	r.Deaths = len(d.rep.snapshot().Deaths) - b.deaths0
	if !b.fightAt.IsZero() {
		r.FightS = d.clk.Now().Sub(b.fightAt).Seconds()
	}
	if p, ok := d.healerRec(); ok && p.MaxPower > 0 && p.PowerType == 0 {
		r.HealMana = int(uint64(p.Power) * 100 / uint64(p.MaxPower))
	} else {
		r.HealMana = -1
	}
	gb.opened += r.Opened
	gb.skipped += r.Skipped
	gb.maxRadius = max(gb.maxRadius, r.RadiusYd)
	if r.CutByRadius {
		gb.cuts++
	}
	gb.healMana = r.HealMana
	if wipe {
		gb.healMana = -1 // the revived bots are not at the mana the batch ended with
		gb.wipes++
		if gb.wipes == 1 {
			gb.wipedAt = r.Size
		}
	} else if r.Size > gb.lastClean {
		gb.lastClean = r.Size
	}
	if d.gravesMode() == "ramp" && !wipe && gb.wipes == 0 {
		gb.size = r.Size + d.rampOf().Step
	}
	text := fmt.Sprintf("graves-batch: n=%d leg=%d mode=%s wanted=%d size=%d opened=%d skipped=%d adds=%d fight_s=%.0f deaths=%d wipe=%s tank_min_pct=%d healer_min_pct=%d healer_mana_end_pct=%d aoe=%s threat_wait_s=%.0f tank_held=%s cut_by_radius=%s open_by=%s radius_yd=%.1f",
		r.N, r.Leg, r.Mode, r.Wanted, r.Size, r.Opened, r.Skipped, r.Adds, r.FightS, r.Deaths, yesNo(wipe), r.TankMin, r.HealMin, r.HealMana, onOff(r.AoE), r.ThreatWaitS, yesNo(r.TankHeld), yesNo(r.CutByRadius), r.OpenedBy, r.RadiusYd)
	if wipe {
		text += fmt.Sprintf(" wiped_at_size=%d next_size=%d", gb.wipedAt, d.batchWantedAfterWipe())
	}
	d.rep.note(text)
	d.remote(func(rr *RemoteReport) { rr.GraveBatches = append(rr.GraveBatches, *r) })
	if !wipe && b.sectionLeft <= 0 {
		d.gravesSummary()
	}
	d.tr.Event("graves_batch", map[string]any{"n": r.N, "leg": r.Leg, "mode": r.Mode, "wanted": r.Wanted, "size": r.Size, "opened": r.Opened,
		"skipped": r.Skipped, "adds": r.Adds, "fight_s": r.FightS, "deaths": r.Deaths, "wipe": wipe, "tank_min_pct": r.TankMin,
		"healer_min_pct": r.HealMin, "healer_mana_end_pct": r.HealMana, "aoe": r.AoE, "threat_wait_s": r.ThreatWaitS, "tank_held": r.TankHeld, "cut_by_radius": r.CutByRadius, "open_by": r.OpenedBy, "radius_yd": r.RadiusYd})
}

// batchWantedAfterWipe is the size the batches go on with after a wipe (for the note).
func (d *RemoteDriver) batchWantedAfterWipe() int {
	if d.cfg.MaxWipeRecoveries >= 2 && d.cfg.MaxWipeRecoveries-(d.wipeRecoverLeg[d.curLeg]+1) <= 1 {
		return 1 // this wipe will use a slot (stepFailedRecover counts it after the note) and one is left for the boss
	}
	return max(d.gb.lastClean, 1)
}

// batchWiped is called when a wipe ends a step: a batch that is going is recorded with the wipe, and the ramp stops growing.
func (d *RemoteDriver) batchWiped() {
	d.agentRestore(context.Background(), "after a wipe")
	if d.batch != nil {
		d.batchFinish(true)
	}
}

func yesNo(b bool) string {
	if b {
		return "yes"
	}
	return "no"
}

func onOff(b bool) string {
	if b {
		return "on"
	}
	return "off"
}

// healerRec is the healer's record in the last frame.
func (d *RemoteDriver) healerRec() (tbm.PartyRec, bool) {
	if d.opt.Healer == "" {
		return tbm.PartyRec{}, false
	}
	return d.memberByName(d.opt.Healer)
}

// tankRec is the tank's record in the last frame.
func (d *RemoteDriver) tankRec() (tbm.PartyRec, bool) {
	if d.opt.Tank != "" {
		return d.memberByName(d.opt.Tank)
	}
	for _, p := range d.fr.Party {
		if d.tank != 0 && p.GUID == d.tank {
			return p, true
		}
	}
	return tbm.PartyRec{}, false
}

func hpPct(p tbm.PartyRec) int {
	if p.Dead {
		return 0
	}
	if p.MaxHP == 0 {
		return 100
	}
	return int(uint64(p.HP) * 100 / uint64(p.MaxHP))
}

// sampleBatch is called at each new frame: while a batch goes it counts the hostiles that fight the party, the lowest health of the tank and
// the healer, and when the fight began.
func (d *RemoteDriver) sampleBatch() {
	if d.batch != nil {
		d.sampleBatchFrame(d.batch)
	}
}

func (d *RemoteDriver) sampleBatchFrame(b *batchRun) {
	if !d.haveFr {
		return
	}
	if eng := d.fr.EngagedHostiles(); len(eng) > 0 {
		if b.fightAt.IsZero() {
			b.fightAt = d.clk.Now()
		}
		for _, h := range eng {
			b.adds[h.GUID] = true
		}
	}
	if p, ok := d.tankRec(); ok {
		if v := hpPct(p); b.rec.TankMin < 0 || v < b.rec.TankMin {
			b.rec.TankMin = v
		}
	}
	if p, ok := d.healerRec(); ok {
		if v := hpPct(p); b.rec.HealMin < 0 || v < b.rec.HealMin {
			b.rec.HealMin = v
		}
	}
}

// batchOpening says that chests of a batch are still to be opened: what comes at the party meanwhile is not fought out by the driver (a walk
// between two chests goes on, the bots defend themselves); the fight is after the last chest.
func (d *RemoteDriver) batchOpening() bool {
	return d.batch != nil && d.batch.left > 0
}

// RunBatchRadius is how far from the batch's centre a chest may lie in a batch whose size is the run's own (graves_batch, graves_ramp):
// tighter than the plan loader's BatchRadius, so that the summons stack and the area spells hit them all (zf-r5, Zum'rah: a batch of 3 spread
// 12.3 yd wiped the party).
const RunBatchRadius = 8.0

// healerManaWaitCap is the longest the party waits before one batch for the healer's mana to reach Config.GravesHealerManaPct.
const healerManaWaitCap = 300 * time.Second

// healerManaWait holds the next grave batch while the healer's mana is under Config.GravesHealerManaPct (default 60): the party is told to
// rest (drink) and a fight that breaks out is fought out first. The rest gate before it gives up on a healer-only mana wait after
// healerManaCap, or at its own timeout, and passes a healer at 10 % (zf-r5: batch 3 began with the healer at 10 % and it ended at 0 %).
// It gives up after healerManaWaitCap and says so.
func (d *RemoteDriver) healerManaWait(ctx context.Context) error {
	want := d.cfg.GravesHealerManaPct
	if want <= 0 {
		return nil
	}
	pct := func() (int, bool) {
		p, ok := d.healerRec()
		if !ok || p.Dead || p.MaxPower == 0 || p.PowerType != 0 {
			return 0, false
		}
		return int(uint64(p.Power) * 100 / uint64(p.MaxPower)), true
	}
	began := d.clk.Now()
	var lastRest time.Time
	noted := false
	for {
		d.look()
		cur, ok := pct()
		if !ok || cur >= want {
			if noted {
				d.rep.note(fmt.Sprintf("graves-batch: the healer is at %d%% mana after %.0f s: the batch goes on", cur, d.clk.Now().Sub(began).Seconds()))
			}
			return nil
		}
		if d.clk.Now().Sub(began) > healerManaWaitCap {
			d.rep.note(fmt.Sprintf("graves-batch: the healer is still at %d%% mana (under %d%%) after %.0f s: the batch goes on without it", cur, want, healerManaWaitCap.Seconds()))
			return nil
		}
		if len(d.fr.EngagedHostiles()) > 0 {
			if err := d.restGate(ctx, false); err != nil { // fights out what came, revives, rests
				return err
			}
			continue
		}
		if !noted {
			noted = true
			d.rep.note(fmt.Sprintf("graves-batch: the healer is at %d%% mana (under %d%%): the next batch waits for it (cmd rest)", cur, want))
		}
		if lastRest.IsZero() || d.clk.Now().Sub(lastRest) > 45*time.Second {
			lastRest = d.clk.Now()
			if _, err := d.order(ctx, "rest"); err != nil {
				return err
			}
		}
		if err := d.tick(ctx, d.cfg.RestPoll); err != nil {
			return err
		}
	}
}

// batchManaLowPct is the healer's mana (percent) under which the next batch waits for a mana gate.
const batchManaLowPct = 20

// leftoverRounds bounds sweepLeftovers: each round is the pull and fight of one idle summon with what stands around it.
const leftoverRounds = 8

// idleLeftovers are the living hostiles of the entries that do not fight the party and stand within LeftoverRadius of centre, nearest first.
func (d *RemoteDriver) idleLeftovers(entries []uint32, centre Point, tried map[packet.GUID]bool) []tbm.HostileRec {
	engaged := map[packet.GUID]bool{}
	for _, h := range d.fr.EngagedHostiles() {
		engaged[h.GUID] = true
	}
	var out []tbm.HostileRec
	for _, h := range d.fr.Hostiles {
		if h.HPPct <= 0 || engaged[h.GUID] || tried[h.GUID] || !slices.Contains(entries, h.Entry) || !h.Hostile() || h.Bystander() || h.Evading() {
			continue
		}
		if math.Hypot(float64(h.X-centre.X), float64(h.Y-centre.Y)) <= LeftoverRadius {
			out = append(out, h)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		return dist3(hostilePoint(out[i]), centre) < dist3(hostilePoint(out[j]), centre)
	})
	return out
}

// sweepLeftovers fights the summons the leg leaves standing idle (Leg.LeftoverEntries) within LeftoverRadius of centre: the nearest is pulled
// to the gathered party and fought with what stands around it, and again until none is left, at most leftoverRounds times; what is left is
// noted. It does nothing in a leg without leftover_entries. when says for the notes where in the run it is (before the batch, after the batch,
// before the trigger).
func (d *RemoteDriver) sweepLeftovers(ctx context.Context, when string, centre Point) error {
	if d.curLeg < 0 || d.curLeg >= len(d.plan.Legs) {
		return nil
	}
	entries := d.plan.Legs[d.curLeg].LeftoverEntries
	if len(entries) == 0 {
		return nil
	}
	tried := map[packet.GUID]bool{}
	d.remote(func(r *RemoteReport) { r.LeftoverSweeps++ })
	for round := 0; round < leftoverRounds; round++ {
		d.look()
		idle := d.idleLeftovers(entries, centre, tried)
		if len(idle) == 0 {
			if round == 0 {
				d.rep.note(fmt.Sprintf("graves-batch: leftover sweep %s: no idle summon of %v within %.0f yd of %.0f %.0f %.0f", when, entries, LeftoverRadius, centre.X, centre.Y, centre.Z))
			}
			if round > 0 {
				if left := d.idleLeftovers(entries, centre, map[packet.GUID]bool{}); len(left) > 0 {
					d.rep.note(fmt.Sprintf("graves-batch: leftover sweep %s: %d idle summon(s) could not be pulled and stay within %.0f yd of %.0f %.0f %.0f; going on", when, len(left), LeftoverRadius, centre.X, centre.Y, centre.Z))
				} else {
					d.rep.note(fmt.Sprintf("graves-batch: leftover sweep %s: no idle summon is left within %.0f yd of %.0f %.0f %.0f", when, LeftoverRadius, centre.X, centre.Y, centre.Z))
				}
			}
			return nil
		}
		h := idle[0]
		tried[h.GUID] = true
		d.remote(func(r *RemoteReport) { r.LeftoverPulls++ })
		why := fmt.Sprintf("graves-batch: leftover sweep %s: %d idle summon(s) of %v within %.0f yd of %.0f %.0f %.0f; pulling %s at %.0f %.0f %.0f (round %d of %d)",
			when, len(idle), entries, LeftoverRadius, centre.X, centre.Y, centre.Z, d.hostileName(h), h.X, h.Y, h.Z, round+1, leftoverRounds)
		if err := d.pullAheadNote(ctx, h, why); err != nil {
			return d.sweepErr(err)
		}
		if err := d.afterFight(ctx); err != nil {
			return d.sweepErr(err)
		}
	}
	d.look()
	if left := d.idleLeftovers(entries, centre, map[packet.GUID]bool{}); len(left) > 0 {
		d.rep.note(fmt.Sprintf("graves-batch: leftover sweep %s: %d idle summon(s) are still within %.0f yd of %.0f %.0f %.0f after %d rounds; going on", when, len(left), LeftoverRadius, centre.X, centre.Y, centre.Z, leftoverRounds))
	}
	return nil
}

// sweepErr marks a wipe that ended a leftover sweep (the sweeps before a batch, after one and before the trigger run outside any batch): the
// step fails with it, and stepFailedRecover then treats it as a wipe in the graves, which does not take the leg's last recovery slot.
func (d *RemoteDriver) sweepErr(err error) error {
	if errors.Is(err, errWipe) {
		d.sweepWiped = true
	}
	return err
}

// gravesSummary notes, once at the end of the graves, how the batches went: how many, the largest size that went by clean, the size that wiped
// first, the widest radius, the chests opened and skipped, and how the batch size was decided.
func (d *RemoteDriver) gravesSummary() {
	gb := &d.gb
	wiped := "none"
	if gb.wipes > 0 {
		wiped = fmt.Sprint(gb.wipedAt)
	}
	text := fmt.Sprintf("graves-summary: leg=%d mode=%s batches=%d opened=%d skipped=%d max_clean_size=%d wipes=%d wiped_at_size=%s cut_by_radius=%d max_radius_yd=%.1f",
		max(d.curLeg, 0), d.gravesMode(), gb.n, gb.opened, gb.skipped, gb.lastClean, gb.wipes, wiped, gb.cuts, gb.maxRadius)
	d.rep.note(text)
	d.tr.Event("graves_summary", map[string]any{"leg": max(d.curLeg, 0), "mode": d.gravesMode(), "batches": gb.n, "opened": gb.opened, "skipped": gb.skipped,
		"max_clean_size": gb.lastClean, "wipes": gb.wipes, "wiped_at_size": gb.wipedAt, "cut_by_radius": gb.cuts, "max_radius_yd": gb.maxRadius})
}

// ---- graves_open_by agent ----

// GravesOpenCarrier and GravesOpenAgent are the values of RemoteOptions.GravesOpenBy.
const (
	GravesOpenCarrier = "carrier"
	GravesOpenAgent   = "agent"
)

// gravesOpenBy is who opens the chests of a batch.
func (d *RemoteDriver) gravesOpenBy() string {
	if d.opt.GravesOpenBy == GravesOpenAgent {
		return GravesOpenAgent
	}
	return GravesOpenCarrier
}

// planHasBatches says whether a loot_object step of the plan has a batch id.
func (d *RemoteDriver) planHasBatches() bool {
	for _, l := range d.plan.Legs {
		for _, st := range l.Steps {
			if st.LootObject != nil && st.LootObject.Batch != "" {
				return true
			}
		}
	}
	return false
}

// checkGravesOpenBy refuses a run that asks for the agent to open the graves (graves_open_by agent) when it cannot: the game has no hands
// for the agent, its account is no GM, or the setup does not list "gm visible off" (the summons would take a visible GM for a target). The
// carrier's order is refused by the module while the carrier is in combat ("The bot is in combat and stopped"), which is why the agent
// opens: it is a GM and the summons ignore it while it is invisible.
func (d *RemoteDriver) checkGravesOpenBy(ctx context.Context) error {
	if d.gravesOpenBy() != GravesOpenAgent || !d.planHasBatches() {
		return nil
	}
	f, ok := d.g.(Follower)
	_, hands := d.g.(AgentHands)
	_, tele := d.g.(StartHands)
	switch {
	case !ok || !hands || !tele:
		return errors.New("graves_open_by agent: this game gives the agent no hands (its own Opening cast, .go xyz)")
	case !d.agentGM(ctx, f):
		return errors.New("graves_open_by agent needs a GM account: the agent's character is no GM, or its level is unknown (it moves with .go xyz and opens the graves itself); use graves_open_by carrier")
	case !d.setupHas(setupVisibleOff):
		return errors.New(`graves_open_by agent needs the agent invisible: the setup does not list "gm visible off" (pass it in dungeon_run setup after running it), so the summons would take the agent for a target`)
	}
	return nil
}

// gmAssistGo records a `.go xyz` the agent's opens sent: a GM assist during the run, in setup_shortcuts and run_gm_assists.
func (d *RemoteDriver) gmAssistGo(label string, at Point) {
	cmd := fmt.Sprintf(".go xyz %.2f %.2f %.2f %d", at.X, at.Y, at.Z, d.plan.Map)
	d.rep.update(func(r *Report) { r.SetupShortcuts = append(r.SetupShortcuts, cmd+" (GM assist during the run)") })
	d.remote(func(r *RemoteReport) { r.RunGMAssists++ })
	d.rep.note(fmt.Sprintf("%s: %s: a GM assist during the run (run_gm_assists; not counted in gm_commands)", label, cmd))
}

// stepLootByAgent opens one chest of a batch with the agent's own character (graves_open_by agent): a chest that is open already or gone is
// skipped; otherwise the agent is brought into the instance (the first chest of a batch: `.appear` to the lead bot), teleported to the chest's
// stand point with `.go xyz` (a GM assist, noted for each open), casts its Opening spell at the exact spawn and takes what is in it. No order
// goes to a carrier, and the party's fight is not waited for: the point is to open while the summons fight. A cast that takes nothing is
// not counted as opened.
func (d *RemoteDriver) stepLootByAgent(ctx context.Context, a Action, rec *objRec) (note string, err error) {
	if skip, why, _ := d.spawnOpened(ctx, a); skip {
		d.rep.note(fmt.Sprintf("%s: %s: skipped", a.Label, why))
		return why, nil
	}
	f, _ := d.g.(Follower)
	sh, _ := d.g.(StartHands)
	h, _ := d.g.(AgentHands)
	if f == nil || sh == nil || h == nil {
		return "", errors.New("graves_open_by agent: this game gives the agent no hands")
	}
	b := d.batch
	if b != nil && d.agentAway == nil {
		// Home is where the agent stands before the `.appear` when that is on the plan's map (the instance); when it started outside, it is the
		// party's safe spot, where the lead bot stood when the batch began (before the walk to the batch's centre, the fight spot).
		pre, preMap, preHere := f.AgentPlace()
		if _, err := d.keepNearOpt(ctx, true); err != nil { // the agent into the instance, next to the party
			return "", err
		}
		at, m, here := f.AgentPlace()
		if !here || m != d.plan.Map {
			return "", fmt.Errorf("%s: the agent's character is not on map %d (in the instance) after `.appear`: it cannot `.go xyz` to the graves", a.Label, d.plan.Map)
		}
		home, why := agentHome{at: at, known: true}, "next to the party after `.appear`"
		switch {
		case preHere && preMap == d.plan.Map:
			home, why = agentHome{at: pre, known: true}, "where it stood before the batch"
		case b.safeSpot != nil:
			home, why = agentHome{at: *b.safeSpot, known: true}, "the party's safe spot (where the lead bot stood when the batch began)"
		}
		d.agentAway = &home
		d.rep.note(fmt.Sprintf("graves-batch: the agent's home for this batch is %.0f %.0f %.0f, %s", home.at.X, home.at.Y, home.at.Z, why))
	}
	if d.agentAway != nil {
		// Every way out of this step that is not a success puts the agent back at home, with a context that a cancel does not end: a failed
		// `.go xyz`, a failed cast, a wipe or a cancel must not leave the GM standing at the grave. A success leaves it where it is for the next
		// chest; the batch's last chest (batchStepDone), a wipe (batchWiped) and the end of the run (run) bring it back.
		defer func() {
			if err != nil {
				d.agentRestore(ctx, "after a failed open")
			}
		}()
	}
	to := d.standOf(a)
	if to == nil {
		to = objectPoint(a)
	}
	if to == nil {
		return "", fmt.Errorf("%s: no stand point or position for the grave", a.Label)
	}
	if err := sh.GoXYZ(ctx, d.plan.Map, *to); err != nil {
		return "", fmt.Errorf("%s: .go xyz to the grave failed: %w", a.Label, err)
	}
	d.gmAssistGo(a.Label, *to)
	var got Looted
	var lerr error
	for try := 0; try < 3; try++ {
		if got, lerr = h.LootObject(ctx, a.SpawnGUID); lerr == nil {
			break
		}
		if ctx.Err() != nil {
			return "", ctx.Err()
		}
		if terr := d.tick(ctx, time.Second); terr != nil {
			return "", terr
		}
	}
	if lerr != nil {
		return "", fmt.Errorf("%s: the agent's own Opening cast at %s: %w", a.Label, tbm.GUIDArg(a.SpawnGUID), lerr)
	}
	d.remote(func(r *RemoteReport) { r.AgentOpens++ })
	d.rep.note(fmt.Sprintf("%s: the agent opened the grave and took %s", a.Label, describeLooted(got)))
	for _, it := range got.Items {
		rec.gained(fmt.Sprintf("%d x%d", it.Entry, it.Count))
	}
	if got.Money > 0 {
		rec.gained(fmt.Sprintf("copper %d", got.Money))
	}
	if len(got.Items) == 0 && got.Money == 0 {
		d.rep.note(fmt.Sprintf("%s: the agent took nothing from the object %s; not counted as opened", a.Label, tbm.GUIDArg(a.SpawnGUID)))
		return "the agent took nothing", nil
	}
	return "", nil
}

// agentRestore sends the agent back to its home (`.go xyz`, a GM assist) once, whatever way the batch or the run ends: it runs with its own
// bounded context (context.WithoutCancel of ctx, 30 s) so that a cancel or a stop does not leave the invisible GM standing at an active grave.
// A restore that fails is a degraded run, with where the agent was left. It does nothing when the agent is at home.
func (d *RemoteDriver) agentRestore(ctx context.Context, when string) {
	h := d.agentAway
	if h == nil {
		return
	}
	d.agentAway = nil
	sh, _ := d.g.(StartHands)
	if !h.known || sh == nil {
		return
	}
	rctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
	defer cancel()
	if err := sh.GoXYZ(rctx, d.plan.Map, h.at); err != nil {
		d.rep.note(fmt.Sprintf("graves-batch: .go xyz back to the agent's home %s failed: %v", when, err))
		d.degrade(fmt.Sprintf("agent left away from its home %.0f %.0f %.0f after the grave opens (%s)", h.at.X, h.at.Y, h.at.Z, err))
		return
	}
	d.gmAssistGo("graves-batch", h.at)
}
