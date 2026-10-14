package dungeon

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"slices"
	"strings"
	"time"

	"github.com/pjerra/turtle-agent/internal/events"
	"github.com/pjerra/turtle-agent/internal/packet"
)

// The pull-test scenario (remote mode; dungeon_run pulltest, owner-approved 2026-10-14). One pull group of one leg is pulled `tries` times
// with the same party and each try scored (pulltest_report.go), so that a combat change is A/B-tested in minutes instead of a 40-minute
// dungeon: start at the pack (start.go), then per try gather -> pull/pullback -> kill -> rest, and reset the pack for the next try:
//
//	"equivalent": the next pack of the leg with the same `equiv` label (PullGroup.Equiv), the party walks to its gather spot;
//	"respawn":    the commander's own `.respawn` (SEC_DEVELOPER; Commands.cpp HandleRespawnCommand: with nothing selected it respawns the
//	              creatures and game objects within the map's visibility distance of the commander) after the party walked back to the
//	              gather spot; a GM assist during the run, counted in run_gm_assists and setup_shortcuts;
//	"auto" (the default): equivalent packs first, then respawn;
//	"none": one try.
//
// With neither an equivalent pack left nor the right to respawn, the scenario says "one try per spawn" and stops with the tries it has.
// Everything is bounded (driver rule 3): at most MaxPulltestTries tries, each try runs the ordinary pull (PullAttempts, FightTimeout) and
// rest gate (the gate's cap), the start phase has its waits, and a wipe ends the scenario.

// Values of PulltestSpec.Reset.
const (
	ResetAuto       = "auto"
	ResetEquivalent = "equivalent"
	ResetRespawn    = "respawn"
	ResetNone       = "none"
)

// The tries of a scenario.
const (
	DefaultPulltestTries = 3
	MaxPulltestTries     = 10
	respawnReach         = 80.0 // yards: the commander must stand this close to the pack for `.respawn` (the map's visibility distance is larger)
	respawnSettle        = 6 * time.Second
)

// PulltestSpec is dungeon_run's pulltest.
type PulltestSpec struct {
	Leg       int
	PullGroup int
	Tries     int    // 0: DefaultPulltestTries
	Reset     string // "": auto
}

// Normalized returns the spec with its defaults and the reset lower-cased.
func (s PulltestSpec) Normalized() PulltestSpec {
	if s.Tries == 0 {
		s.Tries = DefaultPulltestTries
	}
	if s.Reset = strings.ToLower(strings.TrimSpace(s.Reset)); s.Reset == "" {
		s.Reset = ResetAuto
	}
	return s
}

// Validate checks the spec against the plan.
func (s PulltestSpec) Validate(p *Plan) error {
	s = s.Normalized()
	if s.Leg < 0 || s.Leg >= len(p.Legs) {
		return fmt.Errorf("pulltest.leg %d: the plan has legs 0-%d", s.Leg, len(p.Legs)-1)
	}
	if n := len(p.Legs[s.Leg].PullGroups); s.PullGroup < 0 || s.PullGroup >= n {
		return fmt.Errorf("pulltest.pull_group %d: leg %d (%s) has %d pull groups (numbers 0-%d)", s.PullGroup, s.Leg, p.Legs[s.Leg].Name, n, n-1)
	}
	if s.Tries < 1 || s.Tries > MaxPulltestTries {
		return fmt.Errorf("pulltest.tries %d: use 1-%d (default %d)", s.Tries, MaxPulltestTries, DefaultPulltestTries)
	}
	if !slices.Contains([]string{ResetAuto, ResetEquivalent, ResetRespawn, ResetNone}, s.Reset) {
		return fmt.Errorf("pulltest.reset %q: use %s, %s, %s or %s", s.Reset, ResetAuto, ResetEquivalent, ResetRespawn, ResetNone)
	}
	if g := p.Legs[s.Leg].PullGroups[s.PullGroup]; g.Ambush {
		return fmt.Errorf("pulltest: pull group %d of leg %d is an ambush (stealthed): it is walked through, not pulled", s.PullGroup, s.Leg)
	}
	return nil
}

// Respawner is the optional part of a RemoteGame that sends the commander's own `.respawn`.
type Respawner interface {
	// Respawn sends `.respawn` with nothing selected: the core then respawns the creatures and game objects around the commander.
	Respawn(ctx context.Context) error
}

// ptSampler reads every new feed frame of a try (look calls it) for the numbers the frames hold: mobs off the tank, adds, healer mana.
type ptSampler struct {
	g       PullGroup
	radius  float32
	healer  string
	last    time.Time
	pack    map[packet.GUID]bool // each hostile seen: of the pack (true) or not, decided when it was first seen
	adds    map[packet.GUID]uint32
	off     map[packet.GUID]float64 // seconds a mob was engaged with the party and not on the tank
	minMana int                     // 101: none seen
	kills   int                     // units a won pull of the try killed
	pullAt  time.Time               // the first pull order that went through
	killAt  time.Time               // the last kill of the fight
}

func newSampler(g PullGroup, radius float32, healer string) *ptSampler {
	return &ptSampler{g: g, radius: radius, healer: healer, pack: map[packet.GUID]bool{}, adds: map[packet.GUID]uint32{}, off: map[packet.GUID]float64{}, minMana: 101}
}

// ptObserve is called by look for each new frame.
func (d *RemoteDriver) ptObserve() {
	s := d.pts
	if s == nil {
		return
	}
	now := d.clk.Now()
	dt := 0.0
	if !s.last.IsZero() {
		dt = math.Min(now.Sub(s.last).Seconds(), 3) // a gap in the feed is not time spent off the tank
	}
	s.last = now
	for _, h := range d.fr.Hostiles {
		if _, seen := s.pack[h.GUID]; !seen && h.HPPct > 0 {
			s.pack[h.GUID] = slices.Contains(s.g.Entries, h.Entry) && nearAny(hostilePoint(h), s.g.Positions, float64(s.radius)+20)
		}
	}
	for _, h := range d.fr.EngagedHostiles() {
		if !s.pack[h.GUID] {
			s.adds[h.GUID] = h.Entry
		}
		if d.tank == 0 || h.CrowdControlled() {
			continue
		}
		holder, ok := d.fr.ThreatHolder(h.GUID)
		if (ok && holder != d.tank) || (!ok && h.Target != 0 && h.Target != d.tank) {
			s.off[h.GUID] += dt
		}
	}
	for _, m := range d.fr.Party {
		if m.Dead || m.PowerType != 0 || m.MaxPower == 0 {
			continue
		}
		if s.healer != "" && !strings.EqualFold(m.Name, s.healer) {
			continue
		}
		s.minMana = min(s.minMana, m.PowerPct())
	}
}

func nearAny(p Point, ps []Point, r float64) bool {
	for _, q := range ps {
		if dist3(p, q) <= r {
			return true
		}
	}
	return false
}

// ptPulled marks the first pull order of a try that went through.
func (d *RemoteDriver) ptPulled() {
	if d.pts != nil && d.pts.pullAt.IsZero() {
		d.pts.pullAt = d.clk.Now()
	}
}

// ptKilled marks the end of a won fight: the last such mark of a try is its kill time.
func (d *RemoteDriver) ptKilled() {
	if d.pts != nil {
		d.pts.killAt = d.clk.Now()
	}
}

// runPulltest is the run in place of the plan's loop when dungeon_run has a pulltest.
func (d *RemoteDriver) runPulltest(ctx context.Context) (err error) {
	spec := d.opt.Pulltest.Normalized()
	leg := d.plan.Legs[spec.Leg]
	pg := leg.PullGroups[spec.PullGroup]
	if pg.Team != "" && d.team != "" && !strings.EqualFold(pg.Team, d.team) {
		return fmt.Errorf("pulltest: pull group %d of leg %d is for team %s, the party is %s", spec.PullGroup, spec.Leg, pg.Team, d.team)
	}
	healer := d.opt.Healer
	pr := &PulltestReport{Version: PulltestVersion, Plan: d.plan.Name, Leg: spec.Leg, LegName: leg.Name, PullGroup: spec.PullGroup, Entries: slices.Clone(pg.Entries),
		TriesAsked: spec.Tries, Reset: spec.Reset, Healer: healer, Tank: d.opt.Tank, RunID: d.tr.ID(), Tries: []PulltestTry{}, GMCommands: []string{}}
	if pr.Healer == "" {
		pr.Healer = "all mana users"
	}
	publish := func() {
		pr.Summarize()
		d.rep.update(func(r *Report) { r.Pulltest = pr.clone() })
	}
	defer func() {
		if d.tank != 0 && pr.Tank == "" {
			if m, ok := d.fr.Member(d.tank); ok {
				pr.Tank = m.Name
			}
		}
		publish()
		d.writePulltestFile(pr)
	}()
	publish()
	d.rep.note(fmt.Sprintf("pulltest: leg %d%s pull group %d %v, %d tries, reset %s", spec.Leg, nameSuffix(leg.Name), spec.PullGroup, pg.Entries, spec.Tries, spec.Reset))
	first := d.ptAction(spec.Leg, spec.PullGroup, 1)
	if err := d.startLeg(ctx, spec.Leg, first); err != nil {
		return err
	}
	d.wd.progress("pulltest: leg started")
	used := map[int]bool{spec.PullGroup: true}
	cur := spec.PullGroup
	for try := 1; try <= spec.Tries; try++ {
		how := "first spawn"
		if try > 1 {
			next, h, stop, err := d.ptReset(ctx, spec, pr, cur, used)
			if err != nil {
				if runEnded(ctx, err) {
					return err
				}
				pr.Stopped = "reset failed: " + err.Error()
				d.rep.note("pulltest: " + pr.Stopped)
				break
			}
			if stop != "" {
				pr.Stopped = stop
				d.rep.note("pulltest: " + stop)
				break
			}
			cur, how = next, h
		}
		res, err := d.ptTry(ctx, spec, cur, try, how)
		pr.Tries = append(pr.Tries, res)
		publish()
		d.wd.progress(fmt.Sprintf("pulltest try %d: %s", try, res.Outcome))
		d.emit(events.KindDungeonStep, fmt.Sprintf("pulltest try %d: %s in %.0f s, %d deaths", try, res.Outcome, res.TimeToKillS, res.Deaths), map[string]any{"action": "pulltest", "ok": res.Outcome == TryKilled, "try": try, "outcome": res.Outcome, "mode": "remote"})
		if err != nil {
			return err
		}
		if res.Outcome != TryKilled {
			pr.Stopped = fmt.Sprintf("try %d: %s", try, res.Outcome)
			if res.Outcome == TryNoPack {
				pr.Stopped += ": the pack was not found after the reset (nothing was killed); the try is not scored"
			}
			break
		}
	}
	return nil
}

// ptAction is the pull action of a group, as the Stepper would return it.
func (d *RemoteDriver) ptAction(leg, group, try int) Action {
	l := d.plan.Legs[leg]
	g := l.PullGroups[group]
	return Action{Kind: ActPull, Leg: leg, Step: -1, Entries: g.Entries, Positions: g.Positions, Ambush: g.Ambush, Radius: g.Radius, Skip: l.Skip,
		Label: fmt.Sprintf("leg %d%s: pulltest try %d, pull group %d of %d", leg, nameSuffix(l.Name), try, group+1, len(l.PullGroups))}
}

// ptTry is one try: the ordinary pull of the group (gather at the rest gate, pullback, fight), then the rest gate the next pull would wait
// at, scored from the feed and the report.
func (d *RemoteDriver) ptTry(ctx context.Context, spec PulltestSpec, group, try int, how string) (PulltestTry, error) {
	a := d.ptAction(spec.Leg, group, try)
	g := d.plan.Legs[spec.Leg].PullGroups[group]
	radius := g.Radius
	if radius == 0 {
		radius = d.cfg.PackRadius
	}
	if try > 1 {
		d.forgetMobs()
	}
	d.pts = newSampler(g, radius, d.opt.Healer)
	s := d.pts
	before := d.rep.snapshot()
	began := d.clk.Now()
	d.rep.setCurrent(a.Label)
	d.wd.setStep(a.Label)
	leave := d.wd.enter(wdPhaseName(a.Kind), a.Label, d.wdRecoverStep(a.Kind))
	err := d.doPull(ctx, a)
	if err == nil {
		err = d.restGate(ctx, true) // the gate before the next pull: its seconds are the try's rest
	}
	leave()
	d.pts = nil
	after := d.rep.snapshot()

	t := PulltestTry{Try: try, PullGroup: group, Reset: how, Kills: s.kills, HealerMinManaPct: -1, Seconds: round1(d.clk.Now().Sub(began).Seconds())}
	for _, x := range after.Deaths[len(before.Deaths):] {
		t.Deaths++
		t.DeathNames = append(t.DeathNames, x.Name)
	}
	if after.Remote != nil && before.Remote != nil {
		t.ThreatOverrides = after.Remote.ThreatOverrides - before.Remote.ThreatOverrides
	}
	t.RestS = round1(after.RestSeconds - before.RestSeconds)
	if !s.pullAt.IsZero() && !s.killAt.IsZero() && !s.killAt.Before(s.pullAt) {
		t.TimeToKillS = round1(s.killAt.Sub(s.pullAt).Seconds())
	}
	for _, secs := range s.off {
		if secs > 0 {
			t.OffTankMobs++
			t.OffTankS += secs
			t.MaxOffTankS = math.Max(t.MaxOffTankS, secs)
		}
	}
	t.OffTankS, t.MaxOffTankS = round1(t.OffTankS), round1(t.MaxOffTankS)
	for _, e := range s.adds {
		t.Adds++
		t.AddEntries = append(t.AddEntries, e)
	}
	slices.Sort(t.AddEntries)
	if s.minMana <= 100 {
		t.HealerMinManaPct = s.minMana
	}
	switch {
	case err != nil && errors.Is(err, errWipe):
		t.Outcome, t.Error = TryWipe, err.Error()
		d.rep.update(func(r *Report) { r.Wipes++ })
	case err != nil:
		t.Outcome, t.Error = TryFailed, err.Error()
	case t.Kills == 0:
		t.Outcome = TryNoPack
	default:
		t.Outcome = TryKilled
	}
	return t, err
}

// forgetMobs drops what the run holds per mob GUID and per place before a new try. A creature that respawns keeps its GUID (it is the
// spawn's), so the first life's engagement time, opener, give-ups and kill would stand in the second life's way: a stale engagement time
// would send the party in at once as a threat override, and a killed GUID is never counted twice.
func (d *RemoteDriver) forgetMobs() {
	clear(d.engagedAt)
	clear(d.attacked)
	clear(d.heldNoted)
	clear(d.waitNoted)
	clear(d.warned)
	clear(d.ccDone)
	clear(d.skipped)
	clear(d.killedAt)
	d.killedGUID = nil
	d.ledger = nil
	d.tf.heldAt = nil
	d.tf.init()
	d.tf.sentGUID, d.tf.focus = 0, 0
}

// canRespawn says whether the commander may send `.respawn`: the game can, and the account is a developer.
func (d *RemoteDriver) canRespawn(ctx context.Context) (Respawner, StartHands, string) {
	rs, ok := d.g.(Respawner)
	h, ok2 := d.g.(StartHands)
	if !ok || !ok2 {
		return nil, nil, "this game cannot send .respawn"
	}
	level, known, err := h.AccountLevel(ctx)
	if err != nil || !known {
		return nil, nil, "the account level is unknown"
	}
	if level < DeveloperLevel {
		return nil, nil, fmt.Sprintf(".respawn needs account level %d, the commander's is %d", DeveloperLevel, level)
	}
	return rs, h, ""
}

// ptReset brings a pack back for the next try (see the top of this file): the group to pull, how it came back, and a reason to stop when
// nothing can (stop). An error is a reset that failed (a walk, the commander's move, .respawn).
func (d *RemoteDriver) ptReset(ctx context.Context, spec PulltestSpec, pr *PulltestReport, cur int, used map[int]bool) (next int, how, stop string, err error) {
	leg := d.plan.Legs[spec.Leg]
	if spec.Reset == ResetNone {
		return 0, "", "one try per spawn: reset is none", nil
	}
	if spec.Reset == ResetAuto || spec.Reset == ResetEquivalent {
		label := leg.PullGroups[spec.PullGroup].Equiv
		for gi, g := range leg.PullGroups {
			if used[gi] || label == "" || g.Equiv != label || g.Ambush {
				continue
			}
			spot, warn := d.plan.GatherSpot(spec.Leg, gi, d.planPath(ctx))
			if warn != "" {
				d.rep.note("pulltest: warning: " + warn)
			}
			d.rep.note(fmt.Sprintf("pulltest: next try on the equivalent pack %d (equiv %q): the party walks to %.0f %.0f %.0f", gi, label, spot.X, spot.Y, spot.Z))
			if err := d.doWalk(ctx, spot); err != nil {
				return 0, "", "", fmt.Errorf("walk to the equivalent pack %d: %w", gi, err)
			}
			used[gi] = true
			return gi, "equivalent pack", "", nil
		}
		if spec.Reset == ResetEquivalent {
			return 0, "", "one try per spawn: no equivalent pack left (equiv " + fmt.Sprintf("%q", label) + ")", nil
		}
	}
	rs, h, why := d.canRespawn(ctx)
	if rs == nil {
		return 0, "", "one try per spawn: " + why, nil
	}
	pack := leg.PullGroups[cur].Positions[0]
	spot, _ := d.plan.GatherSpot(spec.Leg, cur, d.planPath(ctx))
	d.look()
	if lead, ok := d.leadPos(); ok && dist3(lead, spot) > startNear {
		if err := d.doWalk(ctx, spot); err != nil {
			return 0, "", "", fmt.Errorf("walk back to the gather spot: %w", err)
		}
	}
	if p, m, ok := h.AgentPlace(); !ok || m != d.plan.Map || dist3(p, pack) > respawnReach {
		if _, err := d.keepNearOpt(ctx, true); err != nil {
			return 0, "", "", err
		}
		if p, m, ok := h.AgentPlace(); !ok || m != d.plan.Map || dist3(p, pack) > respawnReach {
			return 0, "", fmt.Sprintf("one try per spawn: the commander is not within %.0f yd of the pack (.respawn reaches the creatures around the commander)", respawnReach), nil
		}
	}
	if err := rs.Respawn(ctx); err != nil {
		return 0, "", "", fmt.Errorf(".respawn: %w", err)
	}
	line := ".respawn"
	d.rep.update(func(r *Report) {
		r.SetupShortcuts = append(r.SetupShortcuts, line+" (GM assist during the run: pulltest reset)")
	})
	d.remote(func(r *RemoteReport) { r.RunGMAssists++ })
	d.tr.Event("gm_shortcut", map[string]any{"kind": "assist", "detail": line})
	pr.GMCommands = append(pr.GMCommands, line)
	d.rep.note("pulltest: .respawn sent by the commander for the next try (a GM assist during the run: run_gm_assists)")
	deadline := d.clk.Now().Add(respawnSettle)
	for d.clk.Now().Before(deadline) {
		if err := d.tick(ctx, d.cfg.Poll); err != nil {
			return 0, "", "", err
		}
	}
	return cur, "respawn", "", nil
}

// writePulltestFile writes the result beside the run's trace: <trace path without .jsonl>.pulltest.json. A run without a trace file writes
// none (the result is in the report).
func (d *RemoteDriver) writePulltestFile(pr *PulltestReport) {
	path := d.tr.Path()
	if path == "" || !d.tr.On() {
		return
	}
	path = strings.TrimSuffix(path, ".jsonl") + ".pulltest.json"
	b, err := json.MarshalIndent(pr, "", "  ")
	if err == nil {
		err = os.WriteFile(path, append(b, '\n'), 0o644)
	}
	if err != nil {
		d.rep.note("pulltest: result file not written: " + err.Error())
		return
	}
	pr.File = path
	d.rep.update(func(r *Report) { r.Pulltest = pr.clone() })
}
