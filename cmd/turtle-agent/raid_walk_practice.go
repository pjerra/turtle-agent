// raid_walk_practice.go: the `raid-walk --practice` loop (owner-approved 2026-11-07, test servers only). After the
// normal placement the run walks to and pulls the boss as usual; from the boss engagement on this loop keeps the
// fight alive, collects mechanic cycles, and resets the boss with the practice GM hands between attempts:
//
//  1. Attempt start: the boss engaged (the walk's own IncidentBossEngaged, or a fresh frame of the mechanic's entry
//     for an entry no plan row names). The boss guid and MaxHealth are recorded, and the Son set: the guids of the
//     mechanic's RespawnEntries the client holds at that moment (alive or dead).
//  2. Cycles: the commander's spell_start/spell_go events for the boss guid and the mechanic's SpellID; when none is
//     seen by FirstAtS+10 s the run falls back to the boss's own timer (pull + FirstAtS + k*EverySec) and marks the
//     rows detect=timer.
//  3. A cycle row is written EvalAfterS after the cast to <out>/practice.jsonl, then the sustain acts: every dead
//     hire is revived and a boss under the floor is restored to its own max.
//  4. Attempt end: CyclesPerAttempt cycles, BerserkS-60 s of attempt, every hire dead, or the boss dying (which ends
//     the whole practice, without a respawn and without crediting a kill).
//  5. Reset: stop the run, `.npc evade` the boss, wait up to 30 s for it at 100 % with no target, revive the dead
//     hires, `.respawn` every dead Son the client still holds (a guid it no longer holds is skipped and noted), rest
//     --practice-rest out of combat, resend the --gm-instance-data lines, and resume the run at the boss row.
//  6. End: the cycle budget, --practice-max, a boss death, 3 failed resets or 4 attempts; then the normal teardown.
//
// Safety: the loop runs only on a server in placeServers, re-checked before every GM action; the hands are the
// session's gated RPCs (the dialled test-server world_addr), and every target is a guid this walk resolved itself.
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/pjerra/turtle-agent/internal/events"
	"github.com/pjerra/turtle-agent/internal/objects"
	"github.com/pjerra/turtle-agent/internal/packet"
	"github.com/pjerra/turtle-agent/internal/raidwalk"
)

// practiceMaxAttempts is the most boss attempts one practice run makes (each reset is a new attempt).
const practiceMaxAttempts = 4

// practiceResetWait is how long a reset waits for the boss to show reset: home, at 100 % and with no target.
const practiceResetWait = 30 * time.Second

// practiceRowsFile is the per-cycle record in the walk's out dir (diag-1b's walk-report joins it to the server's
// siphon rows by time).
const practiceRowsFile = "practice.jsonl"

// practiceState is one practice run's facts.
type practiceState struct {
	mech raidwalk.PracticeMechanic
	// bossGUID and bossMax are the practice boss's guid and the client's MaxHealth for it.
	bossGUID packet.GUID
	bossMax  uint32
	// sons is the Son set recorded at the attempt start (guid -> entry), and sonsKnown its size when recorded.
	sons      map[packet.GUID]uint32
	sonsKnown int

	cycles         int // all cycle rows collected
	attempts       int // attempts started
	attempt        int // the current attempt's number
	cycleInAttempt int // the current attempt's cycle number (1-based)
	// failedResets counts resets in a row that did not see the boss reset.
	failedResets int
	ended        string
	rowsPath     string

	// eventsAt is the practice loop's own events cursor: it reads the cast events independently of pollOnce's
	// system/addon cursor, so neither read hides the other's events.
	eventsAt uint64
	// deadAtLast is the dead set the previous cycle row recorded, for dead_since_last.
	deadAtLast map[string]bool
	// bossHPSet/revived are the current cycle's sustain actions, written into its row.
	revived   []string
	bossHPSet bool
}

// practiceRow is one line of practice.jsonl.
type practiceRow struct {
	Attempt       int    `json:"attempt"`
	Cycle         int    `json:"cycle"`
	TotalCycle    int    `json:"total_cycle"`
	CastAt        string `json:"cast_at"`
	Detect        string `json:"detect"`
	Alive         int    `json:"alive"`
	DeadSinceLast int    `json:"dead_since_last"`
	BossHPPct     int    `json:"boss_hp_pct"`
	SonsAlive     int    `json:"sons_alive"`
	SonsKnown     int    `json:"sons_known"`
	Sustain       struct {
		Revived   []string `json:"revived"`
		BossHPSet bool     `json:"boss_hp_set"`
	} `json:"sustain"`
}

// practiceBossEngaged is the frame fallback for an entry no plan boss row names: a fresh hostile row of the entry,
// alive and hurt or holding a party target (the walk's own attempt-start judgement).
func (w *walk) practiceBossEngaged(entry uint32) bool {
	now := w.env.now()
	for _, h := range w.hostiles {
		if h.entry != entry || h.hppct <= 0 {
			continue
		}
		if now.Sub(h.at) > 30*time.Second {
			continue
		}
		if h.hppct < 100 || (h.target != 0 && w.partyG[h.target]) {
			return true
		}
	}
	return false
}

// practiceIncidentEntry is the boss engagement incident the practice loop owns, if this poll carried one.
func (w *walk) practiceIncidentEntry(incs []raidwalk.Incident, entry uint32) (int, bool) {
	for _, inc := range incs {
		if v, ok := inc.(raidwalk.IncidentBossEngaged); ok && v.Entry == entry {
			return v.Step, true
		}
	}
	return 0, false
}

// runPractice is the whole practice loop, from the first boss engagement to its end. It returns nil when the
// practice finished (the walk's end reason says how) and a *walkStop for an abnormal stop.
func (w *walk) runPractice(ctx context.Context, entry uint32, step int) error {
	mech, ok := raidwalk.PracticeMechanicFor(entry)
	if !ok {
		mech = raidwalk.PracticeWindowMechanic(entry, w.opts.practiceWindow)
	}
	p := &practiceState{mech: mech, rowsPath: filepath.Join(w.outDir, practiceRowsFile), eventsAt: w.eventsAt, deadAtLast: map[string]bool{}}
	w.practice = p
	bossStep := w.bossStepOf(entry)
	if step > 0 {
		bossStep = step
	}
	deadline := w.env.now().Add(w.opts.practiceMax)
	w.logLine(w.env.now(), fmt.Sprintf("practice: %s (%d), %d cycles wanted, max %s", mech.Name, mech.Entry, w.opts.practiceCycles, durWords(w.opts.practiceMax)))
	w.note("practice: %s (%d): %d cycles wanted over at most %d attempts, %s of wall time; sustain %s", mech.Name, mech.Entry,
		w.opts.practiceCycles, practiceMaxAttempts, durWords(w.opts.practiceMax), map[bool]string{true: "on", false: "off"}[w.opts.practiceSustain])
	w.recordSons(ctx)

	for p.attempts < practiceMaxAttempts {
		if w.cyclesAtTarget() {
			p.ended = "cycles"
			break
		}
		if !w.env.now().Before(deadline) {
			p.ended = "max time"
			break
		}
		p.attempts++
		p.attempt = p.attempts
		if err := w.practiceAttempt(ctx, p, bossStep, deadline); err != nil {
			return err
		}
		if p.ended != "" {
			break
		}
		if w.cyclesAtTarget() {
			p.ended = "cycles"
			break
		}
		if !w.env.now().Before(deadline) {
			p.ended = "max time"
			break
		}
		if p.attempts >= practiceMaxAttempts {
			p.ended = "4 attempts"
			break
		}
		resetOK, err := w.practiceReset(ctx, p, bossStep)
		if err != nil {
			return err
		}
		if p.ended != "" {
			break
		}
		if resetOK {
			p.failedResets = 0
		} else {
			p.failedResets++
			if p.failedResets >= 3 {
				p.ended = "3 failed resets"
				break
			}
		}
	}
	if p.ended == "" {
		p.ended = "cycles"
	}
	summary := fmt.Sprintf("practice: %d cycles in %d attempts, ended: %s", p.cycles, p.attempts, p.ended)
	w.logLine(w.env.now(), summary)
	w.note("%s", summary)
	w.endReason = "practice: " + p.ended
	if p.ended == "cycles" || p.ended == "boss killed during practice" {
		return nil
	}
	// The abnormal ends reuse the phase-failure code: the practice did not collect what it was asked to, and the
	// summary carries the reason.
	return stopf(exitRaidWalkFailed, "practice stopped: %s", p.ended)
}

// cyclesAtTarget says the cycle budget is met.
func (w *walk) cyclesAtTarget() bool {
	return w.practice != nil && w.opts.practiceCycles > 0 && w.practice.cycles >= w.opts.practiceCycles
}

// recordSons records the Son set: every unit of the mechanic's RespawnEntries the client holds now.
func (w *walk) recordSons(ctx context.Context) {
	p := w.practice
	if p == nil || len(p.mech.RespawnEntries) == 0 {
		return
	}
	p.sons = map[packet.GUID]uint32{}
	us, err := w.unitsAt(ctx, 0)
	if err != nil {
		w.note("practice: the Son set could not be read (%v): a reset respawns none", err)
		return
	}
	want := map[uint32]bool{}
	for _, e := range p.mech.RespawnEntries {
		want[e] = true
	}
	for _, u := range us {
		if want[u.Entry] {
			p.sons[u.GUID] = u.Entry
		}
	}
	p.sonsKnown = len(p.sons)
	w.logLine(w.env.now(), fmt.Sprintf("practice: %d Son(s) of %d held at the attempt start", p.sonsKnown, len(p.sons)))
}

// practiceAttempt runs one attempt: the cycle rows and the sustain until an attempt end.
func (w *walk) practiceAttempt(ctx context.Context, p *practiceState, bossStep int, deadline time.Time) error {
	started := w.env.now()
	cyclesAtStart := p.cycles
	// The boss's guid and max at the attempt start: from the client's unit list, else the frames' guid and the
	// unit RPC. A missing max means no hp restore this attempt.
	p.bossGUID, p.bossMax = w.practiceBoss(ctx)
	p.cycleInAttempt = 0
	p.deadAtLast = w.practiceDeadSet()
	seenEvent := false
	useTimer := p.mech.IsWindow()
	var rowDue, rowCast time.Time
	rowDetect := ""
	timerK := 0
	if useTimer {
		rowCast = started.Add(time.Duration(p.mech.FirstAtS) * time.Second)
		rowDue = rowCast.Add(time.Duration(p.mech.EvalAfterS) * time.Second)
		rowDetect = map[bool]string{true: "window", false: "timer"}[p.mech.IsWindow()]
	}

	for {
		now := w.env.now()
		if !now.Before(deadline) {
			p.ended = "max time"
			return nil
		}
		if w.practiceBossDead(ctx, p) {
			p.ended = "boss killed during practice"
			return nil
		}
		// The cast events are read on their own cursor, before pollOnce consumes the system lines.
		if !useTimer && rowDue.IsZero() {
			evs, err := w.practiceSpellEvents(ctx, p)
			if err != nil {
				return err
			}
			for _, e := range evs {
				if !w.practiceCastOf(p, e) {
					continue
				}
				seenEvent = true
				cast := e.Time
				if cast.IsZero() {
					cast = now
				}
				rowCast, rowDue, rowDetect = cast, cast.Add(time.Duration(p.mech.EvalAfterS)*time.Second), "event"
				break
			}
			if !seenEvent && !now.Before(started.Add((time.Duration(p.mech.FirstAtS)+10)*time.Second)) {
				// No cast seen by FirstAtS+10 s: the boss's own timer is the schedule from here on.
				useTimer = true
				timerK = 0
				w.note("practice: no %s cast seen %d s into the attempt: falling back to the boss timer", p.mech.Spell, p.mech.FirstAtS+10)
			}
		}
		if useTimer && rowDue.IsZero() {
			for {
				cast := started.Add(time.Duration(p.mech.FirstAtS+timerK*p.mech.EverySec) * time.Second)
				if now.Before(cast) {
					break
				}
				timerK++
				rowCast, rowDue = cast, cast.Add(time.Duration(p.mech.EvalAfterS)*time.Second)
				rowDetect = map[bool]string{true: "window", false: "timer"}[p.mech.IsWindow()]
				break
			}
		}
		if !rowDue.IsZero() && !now.Before(rowDue) {
			if err := w.practiceCycleRow(ctx, p, rowCast, rowDetect, bossStep); err != nil {
				return err
			}
			rowDue = time.Time{}
			if p.ended != "" {
				return nil
			}
		}
		if w.cyclesAtTarget() {
			return nil
		}
		if cyclesThis := p.cycles - cyclesAtStart; cyclesThis >= p.mech.CyclesPerAttempt {
			return nil
		}
		if p.mech.BerserkS > 0 && now.Sub(started) >= time.Duration(p.mech.BerserkS-60)*time.Second {
			return nil
		}
		if w.practiceAllHiresDead() {
			return nil
		}
		incs, err := w.pollOnce(ctx, false)
		if err != nil {
			return err
		}
		for _, inc := range incs {
			if v, ok := inc.(raidwalk.IncidentStall); ok {
				p.ended = fmt.Sprintf("stalled at step %d", v.Step)
				return nil
			}
		}
		if err := w.env.sleep(ctx, 2*time.Second); err != nil {
			return stopf(exitRaidWalkFailed, "practice: interrupted")
		}
	}
}

// practiceSpellEvents reads the cast events since the practice cursor.
func (w *walk) practiceSpellEvents(ctx context.Context, p *practiceState) ([]events.Event, error) {
	var page events.Page
	params := map[string]any{"since": p.eventsAt, "kinds": []string{events.KindSpellStart, events.KindSpellGo}}
	if err := w.agent.Call(ctx, "events", params, &page, 15*time.Second); err != nil {
		return nil, stopf(exitRaidWalkFailed, "practice: events: %v", err)
	}
	p.eventsAt = page.Next
	return page.Events, nil
}

// practiceCastOf says the event is the mechanic's cast by the practice boss.
func (w *walk) practiceCastOf(p *practiceState, e events.Event) bool {
	if e.SpellID != p.mech.SpellID {
		return false
	}
	if p.bossGUID != 0 && e.SourceGUID != p.bossGUID {
		return false
	}
	return p.bossGUID != 0 || e.SourceGUID.Entry() == p.mech.Entry
}

// practiceCycleRow writes the cycle's row and runs the sustain.
func (w *walk) practiceCycleRow(ctx context.Context, p *practiceState, cast time.Time, detect string, bossStep int) error {
	p.revived = nil
	p.bossHPSet = false
	now := w.env.now()
	dead := w.practiceDeadSet()
	row := practiceRow{
		Attempt:    p.attempt,
		Cycle:      p.cycleInAttempt + 1,
		TotalCycle: p.cycles + 1,
		CastAt:     cast.Format(time.RFC3339),
		Detect:     detect,
		Alive:      len(w.hireList()) - len(dead),
		BossHPPct:  w.practiceBossHP(),
		SonsAlive:  w.practiceSonsAlive(),
		SonsKnown:  p.sonsKnown,
	}
	for name := range dead {
		if !p.deadAtLast[name] {
			row.DeadSinceLast++
		}
	}
	if w.opts.practiceSustain {
		for _, name := range w.hireList() {
			if !dead[name] {
				continue
			}
			if err := w.practiceGMRevive(ctx, name); err != nil {
				return err
			}
			p.revived = append(p.revived, name)
		}
		if p.bossGUID != 0 && p.bossMax > 0 && row.BossHPPct < w.opts.practiceBossFloor {
			if err := w.practiceGMSetHP(ctx, p.bossGUID, p.bossMax); err != nil {
				return err
			}
			p.bossHPSet = true
		}
	}
	row.Sustain.Revived = append([]string{}, p.revived...)
	row.Sustain.BossHPSet = p.bossHPSet
	p.cycles++
	p.cycleInAttempt++
	p.deadAtLast = dead
	if err := w.appendPracticeRow(p, row); err != nil {
		return stopf(exitRaidWalkFailed, "practice: %v", err)
	}
	w.logLine(now, fmt.Sprintf("practice: cycle %d/%d (attempt %d, %s): hp %d%%, sons %d/%d, alive %d, dead since last %d, revived %d, hp set %v",
		p.cycles, w.opts.practiceCycles, p.attempt, detect, row.BossHPPct, row.SonsAlive, row.SonsKnown, row.Alive, row.DeadSinceLast, len(p.revived), p.bossHPSet))
	return nil
}

// appendPracticeRow appends one JSON line to practice.jsonl (0600, append).
func (w *walk) appendPracticeRow(p *practiceState, row practiceRow) error {
	raw, err := json.Marshal(row)
	if err != nil {
		return err
	}
	f, err := os.OpenFile(p.rowsPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return err
	}
	if _, err := f.Write(append(raw, '\n')); err != nil {
		f.Close()
		return err
	}
	return f.Close()
}

// practiceDeadSet is the hire set the frames show dead now.
func (w *walk) practiceDeadSet() map[string]bool {
	out := map[string]bool{}
	now := w.env.now()
	for _, name := range w.hireList() {
		pos, ok := w.botAt[name]
		if ok && now.Sub(pos.at) <= 30*time.Second && pos.dead {
			out[name] = true
		}
	}
	return out
}

// practiceAllHiresDead says every hire the walk hired is dead in the frames.
func (w *walk) practiceAllHiresDead() bool {
	names := w.hireList()
	if len(names) == 0 {
		return false
	}
	dead := w.practiceDeadSet()
	if len(dead) != len(names) {
		return false
	}
	for _, n := range names {
		if !dead[n] {
			return false
		}
	}
	return true
}

// practiceBoss is the boss's guid and MaxHealth as the client holds them.
func (w *walk) practiceBoss(ctx context.Context) (packet.GUID, uint32) {
	p := w.practice
	us, err := w.unitsAt(ctx, 0)
	if err == nil {
		for _, u := range us {
			if u.Entry == p.mech.Entry && u.MaxHealth > 0 {
				return u.GUID, u.MaxHealth
			}
		}
	}
	// Fall back to the frame guid and the unit RPC (a served unit carries its max even when the hostile list is cut).
	for g, h := range w.hostiles {
		if h.entry != p.mech.Entry || h.hppct <= 0 {
			continue
		}
		guid, err := packet.ParseGUID(g)
		if err != nil || guid == 0 {
			continue
		}
		var u objects.Unit
		if err := w.agent.Call(ctx, "unit", map[string]string{"guid": guid.String()}, &u, 10*time.Second); err == nil && u.MaxHealth > 0 {
			return guid, u.MaxHealth
		}
		return guid, 0
	}
	return 0, 0
}

// practiceBossHP is the boss's health percent in the newest frame (0 when the frames do not hold it).
func (w *walk) practiceBossHP() int {
	p := w.practice
	if p == nil || p.bossGUID == 0 {
		return 0
	}
	if h, ok := w.hostiles[p.bossGUID.String()]; ok {
		return h.hppct
	}
	return 0
}

// practiceBossDead says the boss died during the practice: a 0 % row in this attempt or a unit the client shows dead.
func (w *walk) practiceBossDead(ctx context.Context, p *practiceState) bool {
	if w.bossFramesDead(p.mech.Entry) {
		return true
	}
	us, err := w.unitsAt(ctx, 0)
	if err != nil {
		return false
	}
	for _, u := range us {
		if u.Entry == p.mech.Entry && u.Dead {
			return true
		}
	}
	return false
}

// practiceSonsAlive counts the Son set's living units in the newest frames.
func (w *walk) practiceSonsAlive() int {
	p := w.practice
	n := 0
	for guid := range p.sons {
		if h, ok := w.hostiles[guid.String()]; ok && h.hppct > 0 {
			n++
		}
	}
	return n
}

// practiceReset stops the run, resets the boss and the Sons, rests, resends the instance data and resumes the run at
// the boss row. ok is false when the boss did not show reset (home, 100 %, no target) in practiceResetWait.
func (w *walk) practiceReset(ctx context.Context, p *practiceState, bossStep int) (bool, error) {
	// A boss that died during the previous attempt is never respawned: the death ends the practice.
	if w.practiceBossDead(ctx, p) {
		p.ended = "boss killed during practice"
		return true, nil
	}
	if err := w.stopRun(ctx, ""); err != nil {
		return false, err
	}
	if err := w.practiceGMEvade(ctx, p.bossGUID); err != nil {
		return false, err
	}
	resetOK := w.practiceBossReset(ctx, p)
	// Revive the dead hires (the ones the frames show), then every dead Son the client still holds.
	deadHires := w.practiceDeadSet()
	for _, name := range w.hireList() {
		if !deadHires[name] {
			continue
		}
		if err := w.practiceGMRevive(ctx, name); err != nil {
			return false, err
		}
	}
	if err := w.practiceRespawnSons(ctx, p); err != nil {
		return false, err
	}
	if err := w.practiceRest(ctx, w.opts.practiceRest); err != nil {
		return false, err
	}
	// The instance data a boss Reset() overwrites is sent again, as after a wipe (gmInstanceDataClear then the
	// existing per-leg send).
	w.gmInstanceDataClear()
	if len(w.gmInstanceData) > 0 {
		inst := w.gmInstanceDataInst(ctx)
		leg := raidwalk.PlaceLegName(w.rows, bossStep)
		if leg != "" {
			if err := w.gmInstanceDataStart(ctx, inst, leg); err != nil {
				return false, err
			}
		}
	}
	now := w.env.now()
	if err := w.resumeRun(ctx, bossStep, now, w.opts.practiceRest); err != nil {
		return false, err
	}
	w.runStopped = false
	w.logLine(now, "practice: reset done; the run resumes at the boss row")
	return resetOK, nil
}

// practiceBossReset polls the frames until the boss is home, at 100 % and with no target (or the wait runs out).
func (w *walk) practiceBossReset(ctx context.Context, p *practiceState) bool {
	deadline := w.env.now().Add(practiceResetWait)
	for {
		if !w.env.now().Before(deadline) {
			w.note("practice: WARN: the boss did not show reset within %s", durWords(practiceResetWait))
			return false
		}
		if _, err := w.pollOnce(ctx, false); err != nil {
			_ = err // the reset still reports its own verdict; the caller's next poll fails loudly
			return false
		}
		h, ok := w.hostiles[p.bossGUID.String()]
		if ok && h.hppct >= 100 && h.target == 0 && !strings.Contains(h.flags, "e") {
			return true
		}
		if err := w.env.sleep(ctx, 2*time.Second); err != nil {
			return false
		}
	}
}

// practiceRespawnSons respawns every dead Son of the recorded set; a guid the client no longer holds is skipped and
// noted. The list is read once per reset.
func (w *walk) practiceRespawnSons(ctx context.Context, p *practiceState) error {
	if len(p.sons) == 0 {
		return nil
	}
	us, err := w.unitsAt(ctx, 0)
	if err != nil {
		w.note("practice: the Son set could not be re-read (%v): no Son respawned", err)
		return nil
	}
	held := map[packet.GUID]objects.Unit{}
	for _, u := range us {
		held[u.GUID] = u
	}
	for guid, entry := range p.sons {
		u, ok := held[guid]
		if !ok {
			w.note("practice: Son %d %s is no longer held by the client: skipped", entry, guid)
			continue
		}
		if !u.Dead {
			continue
		}
		if err := w.practiceGMRespawn(ctx, guid); err != nil {
			return err
		}
	}
	return nil
}

// practiceRest rests out of combat for d: every hire the frames show must be alive and out of combat for the whole
// window. The wait is bounded at twice the rest.
func (w *walk) practiceRest(ctx context.Context, d time.Duration) error {
	if d <= 0 {
		return nil
	}
	end := w.env.now().Add(2 * d)
	var quietSince time.Time
	for {
		now := w.env.now()
		if !now.Before(end) {
			w.note("practice: WARN: the raid did not rest out of combat within %s", durWords(2*d))
			return nil
		}
		if _, err := w.pollOnce(ctx, false); err != nil {
			return err
		}
		quiet := true
		for _, name := range w.hireList() {
			pos, ok := w.botAt[name]
			if !ok || now.Sub(pos.at) > 15*time.Second || pos.dead || pos.inCombat {
				quiet = false
				break
			}
		}
		if quiet {
			if quietSince.IsZero() {
				quietSince = now
			}
			if now.Sub(quietSince) >= d {
				return nil
			}
		} else {
			quietSince = time.Time{}
		}
		if err := w.env.sleep(ctx, 2*time.Second); err != nil {
			return stopf(exitRaidWalkFailed, "practice: interrupted")
		}
	}
}

// practiceServerOK re-checks the test-server rule before every GM action.
func (w *walk) practiceServerOK() error {
	if !placeServers[w.opts.server] {
		return stopf(exitRaidWalkFailed, "practice: %s is not a test server (%s): GM actions refused", w.opts.server, strings.Join(placeServerList(), ", "))
	}
	return nil
}

// practiceGM is one practice hand's RPC.
func (w *walk) practiceGM(ctx context.Context, method string, params any) error {
	if err := w.practiceServerOK(); err != nil {
		return err
	}
	w.logLine(w.env.now(), fmt.Sprintf("> %s %v", method, params))
	if err := w.agent.Call(ctx, method, params, nil, 20*time.Second); err != nil {
		return stopf(exitRaidWalkFailed, "practice: %s: %v", method, err)
	}
	return nil
}

func (w *walk) practiceGMEvade(ctx context.Context, guid packet.GUID) error {
	if guid == 0 {
		return stopf(exitRaidWalkFailed, "practice: no boss guid to evade")
	}
	return w.practiceGM(ctx, "gm_evade", map[string]any{"guid": guid.String()})
}

func (w *walk) practiceGMSetHP(ctx context.Context, guid packet.GUID, hp uint32) error {
	if guid == 0 || hp == 0 {
		return stopf(exitRaidWalkFailed, "practice: no boss guid or max health for the hp restore")
	}
	return w.practiceGM(ctx, "gm_set_hp", map[string]any{"guid": guid.String(), "hp": hp})
}

func (w *walk) practiceGMRespawn(ctx context.Context, guid packet.GUID) error {
	return w.practiceGM(ctx, "gm_respawn", map[string]any{"guid": guid.String()})
}

func (w *walk) practiceGMRevive(ctx context.Context, name string) error {
	return w.practiceGM(ctx, "gm_revive", map[string]any{"name": name})
}

// practiceSummary is the summary's practice object (nil without --practice).
func (w *walk) practiceSummary() *raidwalk.PracticeSummary {
	if w.practice == nil {
		return nil
	}
	return &raidwalk.PracticeSummary{
		Entry: w.practice.mech.Entry, Attempts: w.practice.attempts, Cycles: w.practice.cycles,
		Ended: w.practice.ended, Rows: practiceRowsFile,
	}
}
