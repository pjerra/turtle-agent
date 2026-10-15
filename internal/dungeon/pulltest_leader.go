package dungeon

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"slices"
	"strings"
	"time"

	"github.com/pjerra/turtle-agent/internal/events"
)

// The pull test in leader mode (dungeon_run mode "leader" with pulltest). The same scenario as the commander's (pulltest.go): one pull
// group of one leg is pulled `tries` times with the same party and each try scored, but here the agent's own character is in the party and
// plays with the bot brain, and the score is its own: damage and healing per second and per power, casts the server refused by reason,
// seconds idle in combat, the mobs it took off the tank, its deaths. So `turtle-agent pulltest --compare a.json b.json` can A/B two
// rotations (or a rotation against the Go strategy it replaces) on the same pull.
//
// The party stands in the instance at the pack (like the commander's start_here): the run does not walk the plan to it. Per try the party
// rests (the rest gate), the leader pulls the group as in an ordinary run (doPull: approach, the party's pullback, the fight), the party
// rests again, and the pack comes back for the next try:
//
//	"equivalent": the next pack of the leg with the same `equiv` label (PullGroup.Equiv); the pull approaches it as it does any pack;
//	"respawn":    `.respawn` of the leader (a GM of developer level; with nothing selected it respawns everything dead within the map's
//	              visibility distance of the leader), refused when a plan boss within that distance is not seen alive; counted as a GM
//	              assist (setup_shortcuts, gm_commands);
//	"auto" (the default): equivalent packs first, then respawn;
//	"none": one try.
//
// With neither an equivalent pack left nor the right to respawn, the scenario says "one try per spawn" and stops with the tries it has.
// Everything is bounded: at most MaxPulltestTries tries, each try runs the ordinary pull (PullAttempts, FightTimeout) and rest gate, and a
// wipe ends the scenario.

// CombatTotals is what the event log says the character did since a mark.
type CombatTotals struct {
	Damage, Healing int64
}

// CombatMeter is the optional part of a Game that reads the character's own damage and healing from the event log: Mark is a position in
// the log, Since the totals of the events after it whose source is the character.
type CombatMeter interface {
	CombatMark() uint64
	CombatSince(mark uint64) CombatTotals
}

// StatsBrain is the optional part of a Brain that counts the character's play (brain.Stats): ResetStats opens a window, CharacterStats
// reads it.
type StatsBrain interface {
	ResetStats()
	CharacterStats() CharacterStats
}

// runPulltest is the scenario. It ends the run with nil when it finished (even with fewer tries than asked, or a wipe: those are in the
// result), and with an error when it could not start or was stopped.
func (d *Driver) runPulltest(ctx context.Context) error {
	spec := d.cfg.Pulltest.Normalized()
	if err := spec.Validate(d.plan); err != nil {
		return err
	}
	leg := d.plan.Legs[spec.Leg]
	pg0 := leg.PullGroups[spec.PullGroup]
	pr := &PulltestReport{Version: PulltestVersion, Plan: d.plan.Name, Leg: spec.Leg, LegName: leg.Name, PullGroup: spec.PullGroup,
		Entries: slices.Clone(pg0.Entries), TriesAsked: spec.Tries, Reset: spec.Reset, Mode: ModeLeader, Brain: d.cfg.BrainName,
		Character: d.g.Self().Name, RunID: d.rep.r.RunID, GMCommands: []string{}}
	publish := func() {
		pr.Summarize()
		d.rep.update(func(r *Report) { r.Pulltest = pr.clone() })
	}
	publish()
	if len(d.g.Party()) == 0 {
		return errors.New("the leader has no party: invite the companions first (invite, hire)")
	}
	d.observeInit()
	if err := d.setupGM(ctx); err != nil {
		return err
	}
	if d.g.Map() != d.plan.Map {
		return fmt.Errorf("the leader is on map %d and the plan needs map %d: stand in the instance at the pack (the leader-mode pull test does not walk the plan)", d.g.Map(), d.plan.Map)
	}
	if err := d.startBuffs(ctx, spec.Leg); err != nil {
		return err
	}
	if err := d.startLeg(ctx, spec.Leg, Action{Kind: ActPull}); err != nil {
		return err
	}
	meter, _ := d.g.(CombatMeter)
	sb, _ := d.cfg.Brain.(StatsBrain)
	if d.cfg.Brain == nil {
		d.rep.note("pulltest: no brain plays the character: its numbers are not measured")
	}
	cur := spec.PullGroup
	used := map[int]bool{cur: true}
	how := "first spawn"
	for try := 1; try <= spec.Tries; try++ {
		if err := ctx.Err(); err != nil {
			return err
		}
		t, stop, err := d.ptTry(ctx, spec, leg, cur, try, how, meter, sb)
		if err != nil {
			return err
		}
		pr.Tries = append(pr.Tries, t)
		publish()
		d.tr.Event("pulltest_try", map[string]any{"try": t.Try, "pack": t.PullGroup, "outcome": t.Outcome, "time_to_kill_s": t.TimeToKillS, "deaths": t.Deaths})
		if t.Outcome == TryWipe {
			pr.Stopped = "wipe: the whole party died"
			break
		}
		if stop != "" || try == spec.Tries {
			pr.Stopped = stop
			break
		}
		next, h, stopWhy, err := d.ptResetLeader(ctx, spec, leg, cur, used, pr)
		if err != nil {
			return err
		}
		if stopWhy != "" {
			pr.Stopped = stopWhy
			break
		}
		cur, how = next, h
	}
	publish()
	d.writePulltestFile(pr)
	d.emit(events.KindDungeonStep, fmt.Sprintf("pulltest leg %d pull group %d: %d tries", spec.Leg, spec.PullGroup, len(pr.Tries)),
		map[string]any{"action": "pulltest", "ok": true, "tries": len(pr.Tries), "killed": pr.Summary.Killed})
	return nil
}

// ptTry runs and scores one try. A non-empty stop ends the scenario after it (the leader died too often to go on).
func (d *Driver) ptTry(ctx context.Context, spec PulltestSpec, leg Leg, cur, try int, how string, meter CombatMeter, sb StatsBrain) (PulltestTry, string, error) {
	pg := leg.PullGroups[cur]
	t := PulltestTry{Try: try, PullGroup: cur, Reset: how, HealerMinManaPct: -1}
	before := d.rep.snapshot()
	restBefore, deathsBefore, leaderBefore := legRest(before), len(before.Deaths), before.LeaderDeaths
	began := d.clk.Now()
	var mark uint64
	if meter != nil {
		mark = meter.CombatMark()
	}
	if sb != nil {
		sb.ResetStats()
	}
	d.fightKills, d.fightFirst, d.fightLast = 0, time.Time{}, time.Time{}
	a := Action{Kind: ActPull, Leg: spec.Leg, Step: -1, Label: fmt.Sprintf("pulltest leg %d (%s): try %d, pull group %d", spec.Leg, leg.Name, try, cur),
		Entries: pg.Entries, Positions: pg.Positions, Radius: pg.Radius, Skip: leg.Skip}
	err := d.doPull(ctx, a)
	// The fight is over (or failed): read the character's numbers before the rest gate drinks or sits it down.
	var dmg CombatTotals
	if meter != nil {
		dmg = meter.CombatSince(mark)
	}
	var cs *CharacterStats
	if sb != nil {
		s := sb.CharacterStats()
		cs = &s
	}
	kills := d.fightKills
	first, last := d.fightFirst, d.fightLast
	t.Kills = kills
	switch {
	case err == nil && kills > 0:
		t.Outcome = TryKilled
	case err == nil:
		t.Outcome = TryNoPack // the pack was not there to pull: the reset did not bring it back
	case ctxDone(err):
		return t, "", err
	case errors.Is(err, errWipe):
		t.Outcome, t.Error = TryWipe, err.Error()
	default:
		t.Outcome, t.Error = TryFailed, err.Error()
	}
	if !first.IsZero() && !last.Before(first) {
		t.TimeToKillS = round1(last.Sub(first).Seconds())
	}
	stop := ""
	if t.Outcome == TryKilled || t.Outcome == TryFailed {
		// The party rests after the try; the gate's seconds are part of it (the one before the pull ran inside the pull).
		if rerr := d.restGate(ctx); rerr != nil {
			if ctxDone(rerr) {
				return t, "", rerr
			}
			if errors.Is(rerr, errWipe) {
				t.Outcome, t.Error = TryWipe, rerr.Error()
			} else {
				t.Error = strings.TrimSpace(t.Error + " rest: " + rerr.Error())
			}
		}
	}
	after := d.rep.snapshot()
	t.Seconds = round1(d.clk.Now().Sub(began).Seconds())
	t.RestS = round1(legRest(after) - restBefore)
	t.Deaths = len(after.Deaths) - deathsBefore
	for _, x := range after.Deaths[deathsBefore:] {
		t.DeathNames = append(t.DeathNames, x.Name)
	}
	if cs != nil || meter != nil {
		t.Leader = leaderTry(dmg, cs, t.TimeToKillS, after.LeaderDeaths-leaderBefore)
	}
	if after.LeaderDeaths-leaderBefore >= 2 {
		stop = "the leader died twice in one try"
	}
	return t, stop, nil
}

// leaderTry scores the character from the combat log's totals and the brain's counters (either may be missing).
func leaderTry(c CombatTotals, s *CharacterStats, ttk float64, leaderDeaths int) *LeaderTry {
	l := &LeaderTry{DamageDone: c.Damage, HealingDone: c.Healing, LeaderDeaths: leaderDeaths, MinPowerPct: -1}
	if ttk > 0 {
		l.DPS, l.HPS = round1(float64(c.Damage)/ttk), round1(float64(c.Healing)/ttk)
	}
	if s == nil {
		return l
	}
	l.PowerSpent, l.ActionCounts, l.CastsFailedReasons = s.PowerSpent, s.Actions, s.CastFailed
	for _, n := range s.Actions {
		l.Actions += n
	}
	for _, n := range s.CastFailed {
		l.CastsFailed += n
	}
	l.Errors, l.CombatS, l.IdleCombatS, l.MobOnMeS, l.MobsOnMe, l.MinPowerPct = s.Errors, s.CombatS, s.IdleCombatS, s.MobOnMeS, s.MobsOnMe, s.MinPowerPct
	if s.CombatS > 0 {
		l.IdlePct = round1(s.IdleCombatS / s.CombatS * 100)
	}
	if s.PowerSpent > 0 {
		l.DamagePerPower, l.HealingPerPower = round2(float64(c.Damage)/float64(s.PowerSpent)), round2(float64(c.Healing)/float64(s.PowerSpent))
	}
	if len(l.ActionCounts) == 0 {
		l.ActionCounts = nil
	}
	if len(l.CastsFailedReasons) == 0 {
		l.CastsFailedReasons = nil
	}
	return l
}

// legRest is the rest-gate seconds of the report's newest leg.
func legRest(r Report) float64 {
	if n := len(r.Legs); n > 0 {
		return r.Legs[n-1].RestSeconds
	}
	return 0
}

// ptResetLeader brings a pack back for the next try (see the top of this file): the group to pull, how it came back, and a reason to
// stop when nothing can (stop). An error is a reset that failed (a walk, a wait).
func (d *Driver) ptResetLeader(ctx context.Context, spec PulltestSpec, leg Leg, cur int, used map[int]bool, pr *PulltestReport) (next int, how, stop string, err error) {
	if spec.Reset == ResetNone {
		return 0, "", "one try per spawn: reset is none", nil
	}
	if spec.Reset == ResetAuto || spec.Reset == ResetEquivalent {
		label := leg.PullGroups[spec.PullGroup].Equiv
		for gi, g := range leg.PullGroups {
			if used[gi] || label == "" || g.Equiv != label || g.Ambush {
				continue
			}
			used[gi] = true
			d.rep.note(fmt.Sprintf("pulltest: next try on the equivalent pack %d (equiv %q)", gi, label))
			return gi, "equivalent pack", "", nil
		}
		if spec.Reset == ResetEquivalent {
			return 0, "", "one try per spawn: no equivalent pack left (equiv " + fmt.Sprintf("%q", label) + ")", nil
		}
	}
	rs, ok := d.g.(Respawner)
	switch {
	case !ok:
		return 0, "", "one try per spawn: this game cannot send .respawn", nil
	case !d.caps.Revive:
		return 0, "", fmt.Sprintf("one try per spawn: .respawn needs account level %d, the leader's is %d", DeveloperLevel, d.level), nil
	}
	pack := leg.PullGroups[cur].Positions[0]
	at := d.g.Position()
	if dist3(at, pack) > respawnReach {
		return 0, "", fmt.Sprintf("one try per spawn: the leader is not within %.0f yd of the pack (.respawn reaches the creatures around the leader)", respawnReach), nil
	}
	// `.respawn` revives everything dead or despawned in range: other corpses, a boss the party killed, looted chests. A boss of the plan in
	// range that is not seen alive would come back with it.
	if boss := d.respawnWouldReviveLeader(at); boss != "" {
		return 0, "", "one try per spawn: respawn would revive " + boss + " (it reaches everything dead within " + fmt.Sprintf("%.0f", respawnVisibility) + " yd of the leader); use equivalent packs (the plan's equiv label) or reset none", nil
	}
	if err := rs.Respawn(ctx); err != nil {
		return 0, "", "", fmt.Errorf(".respawn: %w", err)
	}
	line := ".respawn"
	d.shortcut("assist", d.curLeg, line+" (GM assist during the run: pulltest reset)")
	d.rep.update(func(r *Report) {
		r.SetupShortcuts = append(r.SetupShortcuts, line+" (GM assist during the run: pulltest reset)")
	})
	pr.GMCommands = append(pr.GMCommands, line)
	d.rep.note("pulltest: .respawn sent by the leader for the next try (a GM assist during the run)")
	if err := d.sleep(ctx, respawnSettle); err != nil {
		return 0, "", "", err
	}
	return cur, "respawn", "", nil
}

// respawnWouldReviveLeader names a boss of the plan within respawnVisibility of at that is not in view alive ("" when none).
func (d *Driver) respawnWouldReviveLeader(at Point) string {
	units := d.g.Units()
	for li, l := range d.plan.Legs {
		b := l.Boss
		if b == nil || dist3(at, b.Position) > respawnVisibility {
			continue
		}
		alive := false
		for _, u := range units {
			alive = alive || (u.Entry == b.Entry && !u.Dead && u.Health > 0)
		}
		if !alive {
			return fmt.Sprintf("boss %d of leg %d%s", b.Entry, li, nameSuffix(l.Name))
		}
	}
	return ""
}

// writePulltestFile writes the result beside the run's trace: <trace path without .jsonl>.pulltest.json. A run without a trace file writes
// none (the result is in the report).
func (d *Driver) writePulltestFile(pr *PulltestReport) {
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
