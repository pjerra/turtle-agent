package dungeon

import (
	"context"
	"fmt"
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
	Note        string  `json:"note,omitempty"`
}

// gravesState is what the batches of a run remember.
type gravesState struct {
	n         int             // batches begun
	size      int             // the ramp's size for the next batch (0: not started)
	lastClean int             // the largest batch that went by without a wipe
	wipes     int             // wipes during batches
	wipedAt   int             // the size that wiped first
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

// batchWanted is how many chests the next batch should take, before the chests that lie together cut it.
func (d *RemoteDriver) batchWanted(a Action) int {
	gb := &d.gb
	wipes := max(gb.wipes, d.wipeRecoverLeg[d.curLeg]) // a wipe earlier in the leg used a recovery slot as much as one in the graves
	switch d.gravesMode() {
	case "fixed":
		if wipes >= 2 {
			return 1
		}
		return d.opt.GravesBatch
	case "ramp":
		r := d.rampOf()
		if gb.size == 0 {
			gb.size = r.Start
		}
		switch {
		case wipes >= 2:
			return 1 // the second wipe of the leg: the rest goes one chest at a time and the last recovery slot is left for the boss
		case wipes == 1:
			return max(gb.lastClean, 1)
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
		if err := d.restGate(ctx, false); err != nil { // the first batch: the rest gate that follows each fight has not run
			return err
		}
	}
	wanted := d.batchWanted(a)
	size := min(wanted, len(a.Section))
	size = max(size, 1)
	pts := make([]Point, 0, size)
	for _, c := range a.Section[:size] {
		pts = append(pts, c.At)
	}
	mode := d.gravesMode()
	// A size of the run's own may reach past the plan's radius: the cluster widens as far as BatchWidenMax and the batch is cut beyond it.
	limit := BatchWidenMax
	if mode == "plan" {
		limit = BatchRadius
	}
	for len(pts) > 1 && BatchSpread(pts) > limit {
		pts = pts[:len(pts)-1]
	}
	size = len(pts)
	gb.n++
	b := &batchRun{
		left: size, began: d.clk.Now(), adds: map[packet.GUID]bool{},
		deaths0: len(d.rep.snapshot().Deaths),
		rec: GraveBatch{Leg: max(d.curLeg, 0), N: gb.n, Mode: mode, Wanted: wanted, Size: size, TankMin: -1, HealMin: -1,
			RadiusYd: BatchSpread(pts), Centre: BatchCentre(pts)},
	}
	if wanted > size {
		b.rec.Note = fmt.Sprintf("wanted %d, %d chests lie within %.0f yd of each other", wanted, size, limit)
	}
	d.rep.note(fmt.Sprintf("graves-batch begin: %d chest(s) around %.0f %.0f %.0f (%s mode, wanted %d, radius %.1f yd)%s", size, b.rec.Centre.X, b.rec.Centre.Y, b.rec.Centre.Z, mode, wanted, b.rec.RadiusYd, ifNote(b.rec.Note)))
	d.batch = b
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

// legAoEOn puts the area spells back on for every bot a batch turned them off for (the module's default state), when the leg ends.
func (d *RemoteDriver) legAoEOn() {
	off := false
	for _, on := range d.gb.aoe {
		off = off || !on
	}
	if !off {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if d.setAoE(ctx, true) {
		d.rep.note("graves-batch: area spells (aoe) back on for every bot after the leg")
	} else {
		d.degrade("area spells (aoe) left off on some bot after the grave batches")
	}
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
	if err := d.batchThreatLead(ctx, b); err != nil {
		return err
	}
	b.rec.AoE = d.setAoE(ctx, true)
	if err := d.fightOutOnly(ctx, a.Label); err != nil {
		return err // a wipe is recorded by batchWiped, which stepFailedRecover calls
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
	if wipe {
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
	text := fmt.Sprintf("graves-batch: n=%d leg=%d mode=%s wanted=%d size=%d opened=%d skipped=%d adds=%d fight_s=%.0f deaths=%d wipe=%s tank_min_pct=%d healer_min_pct=%d healer_mana_end_pct=%d aoe=%s threat_wait_s=%.0f tank_held=%s radius_yd=%.1f",
		r.N, r.Leg, r.Mode, r.Wanted, r.Size, r.Opened, r.Skipped, r.Adds, r.FightS, r.Deaths, yesNo(wipe), r.TankMin, r.HealMin, r.HealMana, onOff(r.AoE), r.ThreatWaitS, yesNo(r.TankHeld), r.RadiusYd)
	if wipe {
		text += fmt.Sprintf(" wiped_at_size=%d next_size=%d", gb.wipedAt, d.batchWantedAfterWipe())
	}
	d.rep.note(text)
	d.remote(func(rr *RemoteReport) { rr.GraveBatches = append(rr.GraveBatches, *r) })
	d.tr.Event("graves_batch", map[string]any{"n": r.N, "leg": r.Leg, "mode": r.Mode, "wanted": r.Wanted, "size": r.Size, "opened": r.Opened,
		"skipped": r.Skipped, "adds": r.Adds, "fight_s": r.FightS, "deaths": r.Deaths, "wipe": wipe, "tank_min_pct": r.TankMin,
		"healer_min_pct": r.HealMin, "healer_mana_end_pct": r.HealMana, "aoe": r.AoE, "threat_wait_s": r.ThreatWaitS, "tank_held": r.TankHeld, "radius_yd": r.RadiusYd})
}

// batchWantedAfterWipe is the size the batches go on with after a wipe (for the note).
func (d *RemoteDriver) batchWantedAfterWipe() int {
	if d.gb.wipes >= 2 || d.wipeRecoverLeg[d.curLeg] >= 2 {
		return 1
	}
	return max(d.gb.lastClean, 1)
}

// batchWiped is called when a wipe ends a step: a batch that is going is recorded with the wipe, and the ramp stops growing.
func (d *RemoteDriver) batchWiped() {
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
