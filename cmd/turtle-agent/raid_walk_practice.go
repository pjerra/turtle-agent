// raid_walk_practice.go: the `raid-walk --practice` loop (owner-approved 2026-11-07, test servers only). After the
// normal placement the run walks to and pulls the boss as usual; from the boss engagement on this loop keeps the
// fight alive, collects mechanic cycles, and resets the boss with the practice GM hands between attempts:
//
//  1. Attempt start: the boss engaged (the walk's own IncidentBossEngaged, or a fresh frame of the mechanic's entry
//     for an entry no plan row names). The boss guid is bound ONCE here, from the engagement: every GM action of the
//     whole practice names that guid, so a trash unit of the same entry is never touched. When the frames name one
//     engaged guid, exactly that guid binds, and a candidate list that does not hold it fails the practice before any
//     GM action; otherwise only a single unit of the entry binds, and several with no engaged one fail it too. The
//     Son set is the guids of the mechanic's RespawnEntries the client holds at that moment (alive or dead).
//  2. Cycles: the commander's spell_go events for the bound boss guid and the mechanic's SpellID (spell_start is
//     ignored, and a cast within EverySec/2 of the last counted one is the same cast); when none is seen by
//     FirstAtS+10 s the run falls back to the boss's own timer (pull + FirstAtS + k*EverySec) and marks the rows
//     detect=timer, but keeps reading events so a late cast is still detected.
//  3. A cycle row is written EvalAfterS after the cast to <out>/practice.jsonl, then the sustain acts: every dead
//     hire is revived and a boss under the floor is restored to its own max. The floor is also checked on every poll,
//     not only at the cycle rows, on the newest reading across every source (the tracked frames, the newest frame's
//     hostile rows and bot targets, the commander's own client). A source reading the bound boss dead ends the
//     practice with its kill ending; death is ONLY the commander's unit saying Dead or at raw health 0, and a live
//     unit with positive raw health clamps to at least 1 % (an integer percent that rounds to 0 is not a kill). A
//     frame row at 0 % is "seen, health unknown", never dead. The attempt ends with "the boss is gone" only when no
//     source has shown it for practiceBossGoneGrace: one slow poll, one missing source or one unresolved GM hand is
//     not a despawn. A commander unit read that fails is an unknown source, not a missing one: the gone timer holds
//     while it fails, and 60 s of it ends the attempt as "commander unreadable".
//  4. Attempt end: CyclesPerAttempt cycles, BerserkS-60 s of attempt, every hire dead, or the boss dying (which ends
//     the whole practice, without a respawn and without crediting a kill).
//  5. Reset: pause the run with the commander's `run pause` (never `.bot run stop`: a stop ends the run and erases
//     its slot); a pause that does not land (a refusal, no ACK, no paused status on this plan within 10 s) ends the
//     practice before any GM action, EXCEPT when the server says the run is not running (paused, done or gone):
//     `no-run` (no slot at all) and `state` (a slot that is not Running) proceed unheld instead. The resume at the
//     end is skipped when the pause found no resumable run; a Paused slot still resumes. Then `.npc evade` the boss,
//     wait up to 30 s for it at 100 % with no target, revive the dead hires and the commander, `.respawn` every
//     dead Son the client still holds (a guid it no longer holds is skipped and noted), rest --practice-rest out of
//     combat, re-check the lease, resend the --gm-instance-data lines, and resume the run at the boss row. The next
//     attempt's clock starts at its re-engagement (up to 60 s); no re-engagement is a failed reset, counted once
//     per reset.
//  6. End: the cycle budget, --practice-max, a boss death, 3 failed resets or 4 attempts; then the normal teardown.
//
// Safety: the loop runs only on a server in placeServers, re-checked before every GM action; the hands are the
// session's gated RPCs (the dialled test-server world_addr), and every target is a guid this walk resolved itself.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/pjerra/turtle-agent/internal/events"
	"github.com/pjerra/turtle-agent/internal/objects"
	"github.com/pjerra/turtle-agent/internal/packet"
	"github.com/pjerra/turtle-agent/internal/raidwalk"
	"github.com/pjerra/turtle-agent/internal/session"
	"github.com/pjerra/turtle-agent/internal/tbm"
)

// practiceMaxAttempts is the most boss attempts one practice run makes (each reset is a new attempt).
const practiceMaxAttempts = 4

// practiceResetWait is how long a reset waits for the boss to show reset: home, at 100 % and with no target.
const practiceResetWait = 30 * time.Second

// practicePauseWait is how long the reset's `run pause` waits for a status that shows the run paused on this plan: an
// ACK alone is no proof the pause landed, and a reset that starts unheld would fight the run's own pulls.
const practicePauseWait = 10 * time.Second

// practiceSampleFresh is how old a boss frame may be and still be read: an older sample says nothing about the boss
// now (a target change, a dip between frames), so the sustain skips it instead of restoring on stale numbers.
const practiceSampleFresh = 3 * time.Second

// practiceFightIDBack is how far before the practice's own start its fight_ids reach: the first pull's fight starts
// when the boss engages, which is a few seconds before the bind (and the dashboard's record may round earlier).
const practiceFightIDBack = 15 * time.Second

// practiceBossGoneGrace is how long the bound boss must be absent from EVERY source the walk has before the attempt
// ends with "the boss is gone": the frames the walk tracks (every bot's view), the newest frame's hostile rows and
// bot targets, and the commander's own client. One slow poll (the event wait can run to 5 s before the frame is even
// fetched) or one source dropping the unit is not a despawn. zgP-hk5p-1-1 ended a practice 23 s in on three such
// polls while every frame in the same window held the boss at 94-99 %.
const practiceBossGoneGrace = 30 * time.Second

// practiceCommanderUnreadable is how long the commander's unit read may keep failing before the attempt ends: while
// that source is unknown the gone timer holds (its silence is not absence), and 60 s of it is its own reason,
// "commander unreadable", never "the boss is gone".
const practiceCommanderUnreadable = 60 * time.Second

// practiceReengageWait is how long an attempt that follows a reset waits for the pulled boss to fight again (below
// 100 %, a party target, or the walk's IncidentBossEngaged) before its clock starts. No re-engagement in the wait is
// a failed reset.
const practiceReengageWait = 60 * time.Second

// practiceRowsFile is the per-cycle record in the walk's out dir (diag-1b's walk-report joins it to the server's
// siphon rows by time).
const practiceRowsFile = "practice.jsonl"

// practiceState is one practice run's facts.
type practiceState struct {
	mech raidwalk.PracticeMechanic
	// bossGUID and bossMax are the practice boss's guid and the client's MaxHealth for it, bound once at the practice
	// start: every GM action names this guid and no other.
	bossGUID packet.GUID
	bossMax  uint32
	// sons is the Son set recorded at the attempt start (guid -> entry), and sonsKnown its size when recorded.
	sons      map[packet.GUID]uint32
	sonsKnown int

	cycles         int // all cycle rows collected
	attempts       int // attempts started
	attempt        int // the current attempt's number
	cycleInAttempt int // the current attempt's cycle number (1-based)
	// failedResets counts resets in a row that did not see the boss reset, or whose pull did not re-engage within
	// practiceReengageWait.
	failedResets int
	ended        string
	rowsPath     string
	// startedAt is when the practice loop started: the summary's fight_ids are the dashboard fights that started at
	// or after it.
	startedAt time.Time

	// eventsAt is the practice loop's own events cursor: it reads the cast events independently of pollOnce's
	// system/addon cursor, so neither read hides the other's events.
	eventsAt uint64
	// lastCast is the last cycle's cast time: a cast within EverySec/2 of it is the same cast (a spell_start and its
	// spell_go, or the event of a cast the timer already counted), never a second cycle.
	lastCast time.Time
	// awaitingReengage says the next attempt follows a reset and its clock starts only when the boss re-engages.
	awaitingReengage bool
	// lastResetOK is the reset's own verdict (the boss showed home, 100 % and no target): only a reset that landed
	// plus a pull that fights again clears the failed-reset streak.
	lastResetOK bool
	// bossSeenAt is when the newest source last showed the bound boss (alive or dead): the attempt ends only when
	// every source has missed it for practiceBossGoneGrace. It starts at the attempt's own clock.
	bossSeenAt time.Time
	// commanderUnknownSince is when the commander's unit read first failed and has failed ever since: zero while it
	// reads. While it is set the gone timer holds (an unreadable source is not a missing one), and after
	// practiceCommanderUnreadable the attempt ends with "commander unreadable".
	commanderUnknownSince time.Time
	// pending holds the tail of an events page the attempt did not consume (it stops at the first counted cast): the
	// next read serves them before it asks for a new page, so no cast of the page is lost.
	pending []events.Event
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
	// The boss guid is bound once, from the engagement, before any GM action: every reset and every hp restore names
	// it and no other unit. An entry the client holds several units of with no single engaged one is refused here.
	guid, max, err := w.practiceBossBind(ctx)
	if err != nil {
		p.ended = "boss not bound"
		return err
	}
	p.bossGUID, p.bossMax = guid, max
	p.startedAt = w.env.now()
	p.bossSeenAt = p.startedAt
	deadline := p.startedAt.Add(w.opts.practiceMax)
	w.logLine(p.startedAt, fmt.Sprintf("practice: %s (%d) guid %s, %d cycles wanted, max %s", mech.Name, mech.Entry, guid, w.opts.practiceCycles, durWords(w.opts.practiceMax)))
	w.note("practice: %s (%d): %d cycles wanted over at most %d attempts, %s of wall time; sustain %s", mech.Name, mech.Entry,
		w.opts.practiceCycles, practiceMaxAttempts, durWords(w.opts.practiceMax), map[bool]string{true: "on", false: "off"}[w.opts.practiceSustain])
	// fights-run.json marks the practice run for the fight analysers (they skip the practice fights): the flag is on
	// every rewrite from here (saveFights writes the same body).
	_ = w.writeOut("fights-run.json", fightsRunJSON(w.started, w.fightIDsAtStart, true))
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
		if p.failedResets >= 3 {
			// The attempt that followed the reset never saw the boss re-engage: a failed reset, the third in a row.
			p.ended = "3 failed resets"
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
		p.awaitingReengage = true
		p.lastResetOK = resetOK
		if !resetOK {
			// The boss did not show reset in practiceResetWait. The next attempt's re-engagement decides whether
			// the streak breaks: a pull that fights again after a reset that DID land clears it (practiceAttempt).
			// The count is once per reset: a non-re-engagement after this one adds nothing (practiceAttempt only
			// counts a reset that landed).
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
	if p.ended == "the boss is gone" {
		// Every source missed the bound boss for practiceBossGoneGrace, or one read it dead: no attempt can go on.
		return stopf(exitRaidWalkFailed, "practice: the boss is gone")
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
	// An attempt after a reset is clocked from the re-pull, not from the reset: the boss's Reset() timer starts when
	// the fight does, so the rows must not count time the boss stood idle. No re-engagement in the wait is a failed
	// reset (the reset did not bring the fight back), counted toward the 3-failed-resets end.
	if p.awaitingReengage {
		p.awaitingReengage = false
		ok, err := w.practiceAwaitReengage(ctx, p, deadline)
		if err != nil {
			return err
		}
		if !ok {
			if p.ended != "" {
				return nil
			}
			// One failed reset counts once: a reset that did not land was already counted by runPractice, so this
			// pull adds nothing then. A reset that DID land but whose pull never fought again is counted here.
			if p.lastResetOK {
				p.failedResets++
			}
			if p.failedResets >= 3 {
				p.ended = "3 failed resets"
			}
			return nil
		}
		if p.lastResetOK {
			p.failedResets = 0
		}
	}
	started := w.env.now()
	cyclesAtStart := p.cycles
	p.cycleInAttempt = 0
	p.bossSeenAt = started
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
		// The cast events are read on their own cursor, before pollOnce consumes the system lines. They are read
		// after the timer fallback too: a late spell_go is still the boss's own cast, and the EverySec/2 rule below
		// keeps a cast the timer already counted from counting twice.
		if rowDue.IsZero() {
			evs, err := w.practiceSpellEvents(ctx, p)
			if err != nil {
				return err
			}
			for i, e := range evs {
				if !w.practiceCastOf(p, e) {
					continue
				}
				cast := e.Time
				if cast.IsZero() {
					cast = now
				}
				// A page from before this attempt's clock (or a re-served old event) is not this pull's cast.
				if cast.Before(started) || w.practiceCastCounted(p, cast) {
					continue
				}
				seenEvent = true
				rowCast, rowDue, rowDetect = cast, cast.Add(time.Duration(p.mech.EvalAfterS)*time.Second), "event"
				useTimer = false
				timerK = 0
				// The rest of the page is kept for the next read: the loop stops at this cast, and dropping the
				// tail would lose a cast the page already served.
				if i+1 < len(evs) {
					p.pending = append(p.pending, evs[i+1:]...)
				}
				break
			}
			// The timer fallback is only for an attempt that never saw a cast: after one event the schedule is the
			// boss's own casts, and a gap between them is no reason to invent cycles.
			if !seenEvent && !useTimer && rowDue.IsZero() && !now.Before(started.Add((time.Duration(p.mech.FirstAtS)+10)*time.Second)) {
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
		// The floor is checked on every poll, not only at the cycle rows: a boss that dips between rows is restored
		// before the next mechanic, not after it. Every poll is classified; three misses in a row end the attempt.
		done, err := w.practiceSustainBoss(ctx, p)
		if err != nil {
			return err
		}
		if done {
			return nil
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

// practiceAwaitReengage waits (bounded at practiceReengageWait, and by the attempt's deadline) for the boss to fight
// again after a reset: a fresh frame of the bound guid below 100 % or holding a party target, or the walk's
// IncidentBossEngaged of its entry. It only polls the walk (system events and frames) while it waits; the practice's
// own cast cursor is not read here, so a cast made during the wait is before the next attempt's clock and is not
// counted (the attempt's rows start from its re-engagement).
func (w *walk) practiceAwaitReengage(ctx context.Context, p *practiceState, deadline time.Time) (bool, error) {
	end := w.env.now().Add(practiceReengageWait)
	if deadline.Before(end) {
		end = deadline
	}
	for {
		if w.practiceBossEngagedGUID(p) {
			return true, nil
		}
		incs, err := w.pollOnce(ctx, false)
		if err != nil {
			return false, err
		}
		if _, ok := w.practiceIncidentEntry(incs, p.mech.Entry); ok {
			return true, nil
		}
		for _, inc := range incs {
			if v, ok := inc.(raidwalk.IncidentStall); ok {
				p.ended = fmt.Sprintf("stalled at step %d", v.Step)
				return false, nil
			}
		}
		if w.practiceBossEngagedGUID(p) {
			return true, nil
		}
		now := w.env.now()
		if !now.Before(end) {
			w.note("practice: the boss did not re-engage within %s of the reset: a failed reset", durWords(practiceReengageWait))
			return false, nil
		}
		if err := w.env.sleep(ctx, 2*time.Second); err != nil {
			return false, stopf(exitRaidWalkFailed, "practice: interrupted")
		}
	}
}

// practiceBossEngagedGUID says the bound boss is fighting in a fresh frame: hurt, or holding a party member.
func (w *walk) practiceBossEngagedGUID(p *practiceState) bool {
	if p.bossGUID == 0 {
		return false
	}
	h, ok := w.hostiles[p.bossGUID.String()]
	if !ok || h.hppct <= 0 || w.env.now().Sub(h.at) > 30*time.Second {
		return false
	}
	return h.hppct < 100 || (h.target != 0 && w.partyG[h.target])
}

// practiceCastCounted says a cast within EverySec/2 of the last counted one is that same cast, never a new cycle (a
// spell_start and its spell_go, or a spell_go the timer row already counted).
func (w *walk) practiceCastCounted(p *practiceState, cast time.Time) bool {
	if p.lastCast.IsZero() {
		return false
	}
	half := time.Duration(p.mech.EverySec) * time.Second / 2
	return cast.Sub(p.lastCast) < half && cast.After(p.lastCast.Add(-half))
}

// practiceHostileRow finds one guid's row in the tracked hostiles. The frames key the map by GUID.String()
// ("0x%016X", a creature's 0xF130/0xF140 high part included); a key written in another case is the same unit, so the
// lookup falls back to a case-insensitive compare instead of reading it as absent.
func practiceHostileRow(m map[string]hostileState, g packet.GUID) (hostileState, bool) {
	key := g.String()
	if h, ok := m[key]; ok {
		return h, true
	}
	for k, h := range m {
		if strings.EqualFold(k, key) {
			return h, true
		}
	}
	return hostileState{}, false
}

// practiceBossSight is the bound boss's newest reading across every source the walk already has:
//
//   - the tracked frames (w.hostiles): the bots' views as the module aggregates them, keyed by the frame guid. The map
//     is never pruned, so a row's at is when it was last seen, and an old row is not a sighting now;
//   - the newest frame itself (w.viewFrame) at w.viewAt or its own At: its hostile rows and every bot's target. The
//     tracker can lag the very poll that read the frame (the poll reads events first), so the frame is its own
//     evidence, not only the map it fed;
//   - the commander's own client (units, hostiles only), read now: the commander sees the boss even when a frame
//     drops it.
//
// hp is the health percent of the newest source that has one: sources are ranked commander > the frame's hostile row
// > a party target, and at equal timestamps the higher rank wins. hp is -1 when the newest evidence names the unit
// without a reading (a bot's target, or a frame row at 0 %: frames round a live boss under 1 % to 0, so a frame's
// percent is never a death reading); an unknown health may refresh "seen" but never replaces a known one from the same
// moment. at is when that evidence was seen; ok is false when no readable source names the guid at all. A sight of hp
// 0 is a dead reading, and ONLY the commander's own client can give one: the unit says Dead or its raw Health is 0. A
// live unit with positive raw health clamps to at least 1 % (an integer percent that rounds to 0 is not a kill).
// commanderErr is the commander source's own status: a failed units read is an unknown source, never evidence that
// the boss left.
func (w *walk) practiceBossSight(ctx context.Context, p *practiceState) (hp int, at time.Time, ok bool, commanderErr error) {
	if p == nil || p.bossGUID == 0 {
		return 0, time.Time{}, false, nil
	}
	// Source priority, for observations of the same moment: the commander's own client is the most direct, the
	// frame's hostile row next, the tracker's copy of a frame row after it, and a party target (the unit is named,
	// its health is not) last.
	const (
		sightPrioParty = iota
		sightPrioTracked
		sightPrioFrame
		sightPrioCommander
	)
	bestPrio := 0
	consider := func(h int, t time.Time, prio int) {
		switch {
		case !ok:
			hp, at, ok, bestPrio = h, t, true, prio
		case t.After(at):
			// Newer evidence wins on time, even when it names the unit without a health reading.
			hp, at, bestPrio = h, t, prio
		case t.Equal(at) && prio > bestPrio:
			// Same moment: the more direct source wins, but an unknown health (hp < 0) never replaces a known one
			// from that same moment. A known reading includes hp 0, the commander's death.
			bestPrio = prio
			if h >= 0 || hp < 0 {
				hp = h
			}
		}
	}
	// A frame names the unit and its percent: 0 is "seen, health unknown", never dead.
	frameHP := func(pct int) int {
		if pct <= 0 {
			return -1
		}
		return pct
	}
	if h, found := practiceHostileRow(w.hostiles, p.bossGUID); found {
		consider(frameHP(h.hppct), h.at, sightPrioTracked)
	}
	if fr := w.viewFrame; fr != nil {
		frameAt := w.viewAt
		if !fr.At.IsZero() {
			frameAt = fr.At
		}
		for _, h := range fr.Hostiles {
			if h.GUID == p.bossGUID { // parsed guids: the frame's case and 0xF130 high part compare exactly
				consider(frameHP(h.HPPct), frameAt, sightPrioFrame)
			}
		}
		for _, b := range fr.Party {
			if b.Target == p.bossGUID {
				consider(-1, frameAt, sightPrioParty)
			}
		}
	}
	us, err := w.unitsAt(ctx, 0)
	if err != nil {
		commanderErr = err
	} else {
		for _, u := range us {
			if u.GUID != p.bossGUID {
				continue
			}
			h := -1 // listed without a health pool: present, but no reading to restore on
			switch {
			case u.Dead || u.Health == 0:
				// Death is the unit's own dead flag or raw health 0, never a percentage that rounded down.
				h = 0
			case u.MaxHealth > 0:
				h = int(uint64(u.Health) * 100 / uint64(u.MaxHealth))
				if h < 1 {
					h = 1 // live with positive raw health: at least 1 %, never a dead reading
				}
			}
			consider(h, w.env.now(), sightPrioCommander)
		}
	}
	return hp, at, ok, commanderErr
}

// practiceSustainBoss keeps the bound boss alive between cycle rows and decides when it is really gone: a fresh
// reading under the floor is restored to the boss's own max; the attempt ends only when a source reads the boss dead
// (hp 0, the practice's kill ending) or NO source has shown it for practiceBossGoneGrace. One poll without a fresh
// sample, one slow poll and one source dropping the unit are frame gaps, never a despawn: a gm_set_hp the client cannot
// resolve is noted and the poll goes on (the frames still show the boss), it does not count toward gone.
//
// The commander's own unit read is a source of its own: while it fails the source is unknown, not absent, so the gone
// timer holds (its last observation stays the attempt's last sighting) and only practiceCommanderUnreadable of
// failures ends the attempt, with the distinct reason "commander unreadable".
func (w *walk) practiceSustainBoss(ctx context.Context, p *practiceState) (bool, error) {
	if !w.opts.practiceSustain || p == nil || p.bossGUID == 0 || p.bossMax == 0 {
		return false, nil
	}
	now := w.env.now()
	if p.bossSeenAt.IsZero() {
		p.bossSeenAt = now
	}
	hp, at, seen, commanderErr := w.practiceBossSight(ctx, p)
	if seen && at.After(p.bossSeenAt) {
		p.bossSeenAt = at
	}
	if seen && hp == 0 && now.Sub(at) <= practiceBossGoneGrace {
		// A source reads the bound boss dead: the practice's own kill ending (practiceBossDead's), never "gone": no
		// respawn, no kill credit, exit 0, and the summary says the boss died.
		p.ended = "boss killed during practice"
		w.note("practice: the bound boss reads dead in a source: ending the practice")
		return true, nil
	}
	if commanderErr != nil {
		if p.commanderUnknownSince.IsZero() {
			p.commanderUnknownSince = now
			w.note("practice: the commander's units are unreadable (%v): its view is unknown, not a miss, so the boss-gone timer holds", commanderErr)
		}
		if now.Sub(p.commanderUnknownSince) >= practiceCommanderUnreadable {
			p.ended = "commander unreadable"
			w.note("practice: the commander's units have been unreadable for %s (%v): ending the attempt without calling the boss gone",
				durWords(now.Sub(p.commanderUnknownSince)), commanderErr)
			return true, nil
		}
	} else {
		p.commanderUnknownSince = time.Time{}
	}
	if seen && hp > 0 && hp < w.opts.practiceBossFloor && now.Sub(at) <= practiceSampleFresh {
		if err := w.practiceGMSetHP(ctx, p.bossGUID); err != nil {
			if !errors.Is(err, session.ErrUnitNotInView) {
				return false, err
			}
			w.note("practice: the client does not hold the boss for the hp restore (%v): the frames still show it, so it is not gone", err)
		} else {
			w.logLine(now, fmt.Sprintf("practice: boss at %d%% between rows: restored to its max %d", hp, p.bossMax))
		}
	}
	if !p.commanderUnknownSince.IsZero() {
		// One source cannot vouch for the boss: its silence is not every source missing it.
		return false, nil
	}
	if now.Sub(p.bossSeenAt) < practiceBossGoneGrace {
		return false, nil
	}
	p.ended = "the boss is gone"
	w.note("practice: the boss is gone: every source missed it for %s (last reading %s): ending the attempt",
		durWords(practiceBossGoneGrace), durWords(now.Sub(p.bossSeenAt)))
	return true, nil
}

// practiceSpellEvents reads the cast events since the practice cursor. A page tail kept by the attempt (it stops at
// the first counted cast) is served first, so no event of an already-read page is lost.
func (w *walk) practiceSpellEvents(ctx context.Context, p *practiceState) ([]events.Event, error) {
	if len(p.pending) > 0 {
		out := p.pending
		p.pending = nil
		return out, nil
	}
	var page events.Page
	params := map[string]any{"since": p.eventsAt, "kinds": []string{events.KindSpellStart, events.KindSpellGo}}
	if err := w.agent.Call(ctx, "events", params, &page, 15*time.Second); err != nil {
		return nil, stopf(exitRaidWalkFailed, "practice: events: %v", err)
	}
	p.eventsAt = page.Next
	return page.Events, nil
}

// practiceCastOf says the event is the mechanic's cast by the bound boss: only spell_go counts (a spell_start and its
// spell_go are one cast).
func (w *walk) practiceCastOf(p *practiceState, e events.Event) bool {
	if e.Kind != events.KindSpellGo || e.SpellID != p.mech.SpellID || p.bossGUID == 0 {
		return false
	}
	return e.SourceGUID == p.bossGUID
}

// practiceCycleRow writes the cycle's row and runs the sustain.
func (w *walk) practiceCycleRow(ctx context.Context, p *practiceState, cast time.Time, detect string, bossStep int) error {
	p.revived = nil
	p.bossHPSet = false
	p.lastCast = cast
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
		if hp, at, ok, _ := w.practiceBossSight(ctx, p); p.bossGUID != 0 && p.bossMax > 0 && ok && hp > 0 && hp < w.opts.practiceBossFloor && now.Sub(at) <= practiceSampleFresh {
			if err := w.practiceGMSetHP(ctx, p.bossGUID); err != nil {
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

// practiceBossBind resolves the practice boss's guid ONCE, at the practice start, and its MaxHealth: the client's unit
// list holds the candidates, and when an engaged unit is known (hurt, or holding a party target) that guid IS the
// boss, whatever else the entry lists. A candidate list that does not hold the engaged guid is refused before any GM
// action; only without an engaged guid does a single candidate of the entry bind. The practice never guesses which
// creature to reset (a trash unit of the same entry must never be touched).
func (w *walk) practiceBossBind(ctx context.Context) (packet.GUID, uint32, error) {
	p := w.practice
	engaged, engagedOK := w.practiceEngagedGUID(p.mech.Entry)
	us, err := w.unitsAt(ctx, 0)
	if err != nil {
		return 0, 0, stopf(exitRaidWalkFailed, "practice: units: %v", err)
	}
	var cands []objects.Unit
	for _, u := range us {
		if u.Entry == p.mech.Entry {
			cands = append(cands, u)
		}
	}
	if len(cands) == 0 {
		if engagedOK {
			// The unit RPC did not list the entry (a cut list): the one engaged frame names the unit.
			return engaged, w.practiceUnitMax(ctx, engaged), nil
		}
		return 0, 0, stopf(exitRaidWalkFailed, "practice: no unit of entry %d is in view: nothing to bind", p.mech.Entry)
	}
	if engagedOK {
		// The frames name one engaged unit: bind exactly that guid. A candidate list without it (a single different
		// unit of the entry) is a refusal, never a reason to bind the other creature.
		for _, u := range cands {
			if u.GUID == engaged {
				if u.MaxHealth == 0 {
					return u.GUID, w.practiceUnitMax(ctx, u.GUID), nil
				}
				return u.GUID, u.MaxHealth, nil
			}
		}
		return 0, 0, stopf(exitRaidWalkFailed, "practice: the engaged unit %s is not among the client's %d units of entry %d: cannot bind the boss", engaged, len(cands), p.mech.Entry)
	}
	if len(cands) > 1 {
		return 0, 0, stopf(exitRaidWalkFailed, "practice: %d units of entry %d and no single engaged one: cannot tell the boss from the trash", len(cands), p.mech.Entry)
	}
	u := cands[0]
	if u.MaxHealth == 0 {
		return u.GUID, w.practiceUnitMax(ctx, u.GUID), nil
	}
	return u.GUID, u.MaxHealth, nil
}

// practiceEngagedGUID is the one engaged unit's guid of entry in fresh frames (hurt, or holding a party target); ok is
// false when none or several are engaged (several is ambiguous, never guessed).
func (w *walk) practiceEngagedGUID(entry uint32) (packet.GUID, bool) {
	now := w.env.now()
	var found packet.GUID
	n := 0
	for g, h := range w.hostiles {
		if h.entry != entry || h.hppct <= 0 || now.Sub(h.at) > 30*time.Second {
			continue
		}
		if h.hppct >= 100 && !(h.target != 0 && w.partyG[h.target]) {
			continue
		}
		guid, err := packet.ParseGUID(g)
		if err != nil || guid == 0 {
			continue
		}
		found = guid
		n++
	}
	return found, n == 1
}

// practiceUnitMax reads one unit's MaxHealth through the unit RPC (0 when the read fails).
func (w *walk) practiceUnitMax(ctx context.Context, guid packet.GUID) uint32 {
	var u objects.Unit
	if err := w.agent.Call(ctx, "unit", map[string]string{"guid": guid.String()}, &u, 10*time.Second); err == nil {
		return u.MaxHealth
	}
	return 0
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

// practiceBossDead says the BOUND boss died: a unit the commander's own client shows under that guid as Dead or at raw
// health 0. A frame row at 0 % is "seen, health unknown" (frames round a live boss under 1 % to 0), never a death; a
// dead trash unit of the same entry is never the boss.
func (w *walk) practiceBossDead(ctx context.Context, p *practiceState) bool {
	if p.bossGUID == 0 {
		return false
	}
	us, err := w.unitsAt(ctx, 0)
	if err != nil {
		return false
	}
	for _, u := range us {
		if u.GUID == p.bossGUID && (u.Dead || u.Health == 0) {
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

// errRunGone marks the reset pause's no-slot answer: the server's `run pause` ACTION_ERR has code `no-run`, "No run
// is going (.bot run start [plan])." (TortoiseBots commands/Commander.cpp RunVerbAs, upstream/dev f7368050
// :15381). The run slot is gone: the reset proceeds without a held run and the resume is skipped. Transport, poll
// and interrupt errors, and refusals under any other code, never match.
var errRunGone = errors.New("no run is going")

// errRunNotRunning marks the reset pause's not-running-slot answer: the server's `run pause` ACTION_ERR has code
// `state`, "The run is not running." (:15466-15468, the pause verb's only `state` refusal). That code means the
// slot exists but is not Running: Paused (door hold, system or player pause) or Done. practiceReset reads the run
// status to tell them apart: a Paused run resumes afterwards, a Done (or no) slot skips the resume, and any other
// answer refuses the reset.
var errRunNotRunning = errors.New("run is not running")

// runState is what the `run status` read after a `state` pause refusal says about the slot. Only the two continuous
// answers go on unheld: one worth resuming into, and one with nothing left to resume. Everything else (Running after
// the server called the run not running, an unrecognised state, a lineless answer, a refused or failed read) is
// unknown, and the reset is refused rather than run unheld.
type runSlotState int

const (
	// runStateEnded: Done or no slot. The reset proceeds and skips the resume.
	runStateEnded runSlotState = iota
	// runStatePaused: a paused slot the reset resumes afterwards.
	runStatePaused
	// runStateUnknown: Running, an unrecognised state, or a read that did not answer.
	runStateUnknown
)

// String names the state for the notes.
func (s runSlotState) String() string {
	switch s {
	case runStatePaused:
		return "paused"
	case runStateEnded:
		return "ended"
	}
	return "unknown"
}

// practicePauseRefusal classifies the server's `run pause` ACTION_ERR for the reset: the code is the whole signal,
// never the message text.
func practicePauseRefusal(err *tbm.Err) error {
	if err == nil {
		return nil
	}
	switch err.Code {
	case "no-run":
		return errRunGone
	case "state":
		return errRunNotRunning
	}
	return nil
}

// practiceReset pauses the run, resets the boss and the Sons, rests, resends the instance data and resumes the run at
// the boss row. ok is false when the boss did not show reset (home, 100 %, no target) in practiceResetWait.
func (w *walk) practiceReset(ctx context.Context, p *practiceState, bossStep int) (bool, error) {
	// A boss that died during the previous attempt is never respawned: the death ends the practice.
	if w.practiceBossDead(ctx, p) {
		p.ended = "boss killed during practice"
		return true, nil
	}
	// `run pause` holds the run with its binding kept. Never `.bot run stop`: a stop ENDS the run and erases its
	// slot, so the resume after the reset would find no run going. A pause that did not land (a refusal, no ACK,
	// no paused status in practicePauseWait) ends the practice HERE, EXCEPT when the server says the run is not
	// running: `no-run` (no slot at all) or `state` (a slot that is not Running: Paused or Done). The wipe rule
	// may have stopped it (the practice saw this at ~10.5 min in), or it ended or was stopped from under the
	// reset. Then the reset proceeds without a held run, but only when the run status itself says Paused (the
	// resume follows) or Done/no slot (the resume is skipped): a Running or unreadable status is no answer, so
	// the reset is refused instead of run unheld. skipResume is decided per reset below and never leaks into the
	// next one.
	skipResume := false
	if err := w.pauseRun(ctx); err != nil {
		switch {
		case errors.Is(err, errRunGone):
			// No run slot: nothing to hold and nothing to resume into.
			w.note("practice: the run is not running (no run going) before the reset pause: continuing unheld")
			w.logLine(w.env.now(), "practice: no run is going, the reset proceeds without a pause")
			skipResume = true
		case errors.Is(err, errRunNotRunning):
			// A slot that is not Running: Paused resumes afterwards, Done or no slot skips the resume, and any
			// other answer refuses the reset. The status read is the server's own answer through the commander
			// RPC, not the push feed's newest line.
			state, why := w.practiceRunState(ctx)
			switch state {
			case runStatePaused:
				w.note("practice: the run is not running (%s: %s) before the reset pause: continuing unheld", state, why)
				w.logLine(w.env.now(), "practice: the run is paused, the reset proceeds and resumes it afterwards")
			case runStateEnded:
				skipResume = true
				w.note("practice: the run is not running (%s: %s) before the reset pause: continuing unheld, no resume", state, why)
				w.logLine(w.env.now(), "practice: the run has ended, the reset proceeds without a pause or a resume")
			default:
				w.note("practice: the run is not running but the status read is %s (%s): refusing the reset", state, why)
				p.ended = "run pause refused"
				return false, stopf(exitRaidWalkFailed, "practice: the run pause before the reset failed: %v", err)
			}
		default:
			p.ended = "run pause refused"
			return false, stopf(exitRaidWalkFailed, "practice: the run pause before the reset failed: %v", err)
		}
	}
	if err := w.practiceGMEvade(ctx, p.bossGUID); err != nil {
		return false, err
	}
	resetOK := w.practiceBossReset(ctx, p)
	// Revive the dead hires (the ones the frames show) and the commander if it died, then every dead Son the client
	// still holds.
	deadHires := w.practiceDeadSet()
	for _, name := range w.hireList() {
		if !deadHires[name] {
			continue
		}
		if err := w.practiceGMRevive(ctx, name); err != nil {
			return false, err
		}
	}
	if w.practiceCommanderDead(ctx) {
		if err := w.practiceGMRevive(ctx, w.manifest.Character); err != nil {
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
	// existing per-leg send). A lost lease ends the walk before those GM lines: another holder may own the slot now.
	if w.leaseLost.Load() {
		return false, stopf(exitRaidWalkFailed, "practice: lease lost")
	}
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
	// A pause that found no resumable run leaves no slot to resume into: the GM reset stands alone, and the next
	// attempt re-engages the boss from the world state it finds.
	if skipResume {
		w.logLine(now, "practice: reset done; no run to resume (not running at the pause)")
		return resetOK, nil
	}
	if err := w.resumeRun(ctx, bossStep, now, w.opts.practiceRest); err != nil {
		// resumeRun's absolute-deadline failure is a *recoveryTimeout, which the wipe paths turn into exit 19 ("wipe
		// at step N"). During practice that is the wrong ending: the run did not come back after the reset, a practice
		// failure (18) with its own reason.
		var timeout *recoveryTimeout
		if errors.As(err, &timeout) {
			p.ended = "resume timed out"
			return false, stopf(exitRaidWalkFailed, "practice: the run did not resume after the reset within %s", durWords(timeout.dur))
		}
		return false, err
	}
	w.logLine(now, "practice: reset done; the run resumes at the boss row")
	return resetOK, nil
}

// practiceRunState reads one `run status` after a `state` pause refusal and classifies the slot. why names what the
// answer showed, for the notes: the TBM:RUN line, or why there was none. A Paused slot is worth resuming; Done or no
// slot has nothing to resume into; Running, an unrecognised state, and a refused, failed or lineless read are all
// unknown, so the caller refuses the reset rather than run it unheld.
func (w *walk) practiceRunState(ctx context.Context) (runSlotState, string) {
	rep, err := w.commander(ctx, commanderReq{Op: "run", Verb: "status", WaitMS: 5000})
	switch {
	case err != nil:
		return runStateUnknown, fmt.Sprintf("run status unreadable (%v)", err)
	case rep.Reply.Err != nil:
		return runStateUnknown, fmt.Sprintf("run status refused (%s)", rep.Reply.Err.Message)
	default:
		st, line, ok := lastRunLine(rep.Reply.Lines)
		if !ok {
			return runStateUnknown, "run status answer has no TBM:RUN line"
		}
		switch st.State {
		case tbm.RunPaused:
			return runStatePaused, line
		case tbm.RunDone, tbm.RunNone:
			return runStateEnded, line
		}
		return runStateUnknown, line
	}
}

// pauseRun holds the run with the commander's `run pause` (the binding is kept, and a later `run resume` finds it).
// It is what the reset and the gm-instance-data firing use; `.bot run stop` is never a pause. The ACK is checked the
// way resumeRun checks its own (an ERR or a non-ACK reply is a refusal), and only a status received AFTER the request
// that shows this plan paused makes the pause a success: an ACK alone leaves the reset unheld.
func (w *walk) pauseRun(ctx context.Context) error {
	seq0 := w.statusSeq
	rep, err := w.commander(ctx, commanderReq{Op: "run", Verb: "pause", WaitMS: 10000})
	if err != nil {
		return err
	}
	if rep.Reply.Err != nil {
		if sentinel := practicePauseRefusal(rep.Reply.Err); sentinel != nil {
			return fmt.Errorf("run pause refused (%s): %w", rep.Reply.Err.Message, sentinel)
		}
		return fmt.Errorf("run pause: %s", rep.Reply.Err.Message)
	}
	if !rep.Reply.OK && rep.Reply.Kind != tbm.KindAck {
		return fmt.Errorf("run pause: no ACK (%s)", rep.Reply.Line)
	}
	w.logLine(w.env.now(), "> .bot run pause")
	deadline := w.env.now().Add(practicePauseWait)
	for {
		if w.statusSeq > seq0 && w.haveStatus && w.lastStatus.State == tbm.RunPaused && w.lastStatus.Plan == w.plan.Name {
			return nil
		}
		if !w.env.now().Before(deadline) {
			return fmt.Errorf("no paused status for %s in %s", w.plan.Name, durWords(practicePauseWait))
		}
		if _, err := w.pollOnce(ctx, false); err != nil {
			return err
		}
		if err := w.env.sleep(ctx, 2*time.Second); err != nil {
			return stopf(exitRaidWalkFailed, "practice: interrupted")
		}
	}
}

// practiceCommanderDead reads the commander's own status for a 0 health (a wipe that took the commander too).
func (w *walk) practiceCommanderDead(ctx context.Context) bool {
	st, err := w.status(ctx)
	if err != nil {
		return false
	}
	return st.MaxHealth > 0 && st.Health == 0
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
	// A lost lease ends the walk before any GM action: another holder may own the slot now.
	if w.leaseLost.Load() {
		return stopf(exitRaidWalkFailed, "practice: lease lost")
	}
	if err := w.practiceServerOK(); err != nil {
		return err
	}
	w.logLine(w.env.now(), fmt.Sprintf("> %s %v", method, params))
	if err := w.agent.Call(ctx, method, params, nil, 20*time.Second); err != nil {
		return stopfCause(exitRaidWalkFailed, err, "practice: %s: %v", method, err)
	}
	return nil
}

func (w *walk) practiceGMEvade(ctx context.Context, guid packet.GUID) error {
	if guid == 0 {
		return stopf(exitRaidWalkFailed, "practice: no boss guid to evade")
	}
	return w.practiceGM(ctx, "gm_evade", map[string]any{"guid": guid.String()})
}

// practiceGMSetHP restores the bound boss to its own MaxHealth. The RPC takes the guid only: the session reads the
// unit's MaxHealth itself and sends `.modify hp <max> <max>`, so no caller can lower a boss's maximum.
func (w *walk) practiceGMSetHP(ctx context.Context, guid packet.GUID) error {
	if guid == 0 {
		return stopf(exitRaidWalkFailed, "practice: no boss guid for the hp restore")
	}
	return w.practiceGM(ctx, "gm_set_hp", map[string]any{"guid": guid.String()})
}

func (w *walk) practiceGMRespawn(ctx context.Context, guid packet.GUID) error {
	return w.practiceGM(ctx, "gm_respawn", map[string]any{"guid": guid.String()})
}

func (w *walk) practiceGMRevive(ctx context.Context, name string) error {
	return w.practiceGM(ctx, "gm_revive", map[string]any{"name": name})
}

// practiceSummary is the summary's practice object: nil only without --practice. A --practice walk whose loop never
// started still carries the object (ended "never engaged"), so `practice` and `assisted: true` are always present and
// BuildSummary always clears boss_kill(s).
func (w *walk) practiceSummary() *raidwalk.PracticeSummary {
	if w.opts.practiceEntry == 0 {
		return nil
	}
	if w.practice == nil {
		return &raidwalk.PracticeSummary{
			Entry: w.opts.practiceEntry, Ended: "never engaged", Rows: practiceRowsFile, FightIDs: []string{},
		}
	}
	return &raidwalk.PracticeSummary{
		Entry: w.practice.mech.Entry, Attempts: w.practice.attempts, Cycles: w.practice.cycles,
		Ended: w.practice.ended, Rows: practiceRowsFile, FightIDs: w.practiceFightIDs(),
	}
}

// practiceFightIDs is the dashboard fights that started at or after the practice's own start (back to the first
// pull's engagement, practiceFightIDBack before the bind), sorted. The tools skip them (they are the practice
// attempts, never a real raid fight).
func (w *walk) practiceFightIDs() []string {
	p := w.practice
	if p == nil || p.startedAt.IsZero() {
		return []string{}
	}
	cutoff := p.startedAt.Add(-practiceFightIDBack).Unix()
	ids := make([]string, 0, len(w.fightRows))
	for _, r := range w.fightRows {
		if r.ID == "" || r.Start == 0 {
			continue
		}
		if r.Start >= cutoff {
			ids = append(ids, r.ID)
		}
	}
	sort.Strings(ids)
	return ids
}
