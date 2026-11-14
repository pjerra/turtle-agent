// raid_walk_prepullaura.go: the `--prepull-aura` test tool of raid-walk (test servers only, off by default). Before
// each boss row the commander puts the flag's aura lists on every living hire the commander's raid holds with the same
// GM select-then-".aura" call dungeon_run's buff presets use (internal/dungeon's AuraPort and ApplyAuras), so a walk
// tests the preparation a real raid carries into the instance: a flask, elixirs, food and the world buffs, all as
// auras the walk can read back.
//
//	--prepull-aura role=tank:17626,17538 --prepull-aura role=healer:17627,24363 --prepull-aura all:world,23768
//	--prepull-aura dungeons/prepull/zg-preraid.json
//
// The raid is read from the commander's view (prepullRaid), never from the walk's own `party` RPC: that is the
// PLAYER's group, and the walk's player never joins the hired bots' raid (walk 101 shipped a verified boss row for 0
// bots). The view is matched against the walk's hires: a frame member the walk never hired is watched for
// combat but never targeted, and a fire whose lists do not reach every hire the walk made fails instead
// (prepullAuraCoverage). A hire whose aura role the walk cannot tell — a unit row with no class, a bot the module's
// state rows leave out, or a state naming no single role's spec — is a failure too, never an "all"-only bot: the role
// comes PER BOT from the module itself (prepullRole, raidwalk.BotStateAuraRole), not from the class alone and not from
// the roster batch, because a batch like "5h 13d" names no role for a priest, paladin, druid or shaman and a ret
// paladin, a feral druid, an enhancement shaman and a shadow priest share a class with a healer. The source is
// `.bot roster`'s TBM:BOTSTATE rows, whose `specs=` field is the spec strategy kit the module installed on that bot
// (walk 101b aborted on the "rest" batch before this source existed).
//
// The fire is the boss row's own gate (prepullGate): the step before a boss row in WHATEVER phase, as long as the run
// is still before the pull (no pulling/fighting note, no later pull line of the page, no party bot in combat), or the
// boss row itself when the run passed that step too fast to pause, still before the pull (no member in combat and the
// boss's arena read fresh out of combat). Walk 101b: the ZG Thekal plan's step before boss row 255 is a plain move, so
// a rest/gather-only gate never fired and the walk stopped with exit 18. The fire is once per boss row per walk, under
// a run pause that is always resumed. The pause is fail-closed: a refused or unconfirmed pause sends no aura, and the
// commander and every living raid member are re-read for combat right before the first `.aura` and before every
// later one, so a pull that starts mid-batch stops the batch and fails the walk. The raid view leaves the player out,
// so the commander is read through the walk's own status and holds a read of its own. A unit the unit RPC
// cannot read counts as combat too: the walk never assumes an unreadable raid is out of combat. A boss row that
// becomes current without a verified fire fails the walk too: the preparation the run was started to test never
// happened. The auras a bot already carries are not sent again, so a second boss row only tops up what a death or a
// buff strip took. Every fire is read back into prepull-auras.json (bot, role, ids sent/seen/missing). By default
// every requested aura must verify; --prepull-aura-tolerance N is the explicit opt-in that accepts up to N missing
// ids per bot. A missing aura beyond that bound fails the walk (exit 18), because a preparation the walk cannot see
// is not the test the run was started for. A boss row is written to prepull-auras.json only after its auras verified
// within the tolerance; a fire that failed is a separate `attempts` entry with its reason, never a boss row.
//
// --prepull-aura-when start (walk 101: the boss-row fire's ~120 sends gave a bot time to enter combat at Hakkar's row
// 689, and 4 of 4 attempts lost the run there) fires the lists ONCE, after the hire, the gear and the placement and
// before `run start`, so the run's first move already carries them; the flasks and world buffs last 2 h or more. The
// fire is the same fail-closed one (every hire, roles from the module's per-bot state, a fresh combat watch, strict
// verification), it needs no pause because the run is positively confirmed idle (state none or done; anything else,
// refusals included, sends nothing), and it is recorded as start_row instead of a boss row. Because it goes out inside
// the placement's teleport wave, it first waits (60 s, 2 s polls, prepullAuraStartWait) for every hire to be in the
// commander's view and alive and in the agent's unit store: walk 101's ZG Jeklik attempt at plan start row 1 stopped
// 0.16 s after the last `.tele`, on two unit reads of the MT that failed before the store had it. No boss row is
// fired in this mode: at each boss pull the walk only READS which auras are still up on each bot and logs them, so a
// walk shows the coverage the fight started with.
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/pjerra/turtle-agent/internal/dungeon"
	"github.com/pjerra/turtle-agent/internal/objects"
	"github.com/pjerra/turtle-agent/internal/planexport"
	"github.com/pjerra/turtle-agent/internal/raidwalk"
	"github.com/pjerra/turtle-agent/internal/tbm"
)

// The --prepull-aura-when modes: boss (the default) fires the lists before each boss row, start fires them once,
// after the placement and before the run's first move (walk 101's Hakkar row 689 is out of this mode's way).
const (
	prepullAuraWhenBoss  = "boss"
	prepullAuraWhenStart = "start"
)

// The walk's prepull-aura timings. The settle is dungeon_run's own; the gap is shorter than dungeon_run's 300 ms
// because the GM command channel needs no flood margin: the core's anti-chat-flood counter returns at once for any
// account above SEC_PLAYER (MasterPlayerChat.cpp, UpdateSpeakTime: "ignore chat spam protection for GMs in any mode"),
// so `.aura` lines from the developer-level commander are not throttled there. Each send already waits on the
// selection's own server echo (session.GMOn) and the world thread runs the lines one at a time, so 150 ms between two
// auras of one list keeps a boss row's ~20 bots and ~10 auras near half a minute without piling lines up. The verify
// window covers the last auras' arrival.
var (
	prepullAuraGap        = 150 * time.Millisecond
	prepullAuraSettle     = 1500 * time.Millisecond
	prepullAuraViewWait   = 10 * time.Second
	prepullAuraVerifyWait = 5 * time.Second
	prepullAuraVerifyPoll = time.Second
	// The --prepull-aura-when start fire waits for the placement's teleport wave to land (walk 101: four of four
	// Jeklik attempts at row 1 stopped at the fire 0.16 s after the last `.tele`, with "the unit read of Darinn failed
	// twice"). A boss row never needs it: the raid has been in the view for minutes by then.
	prepullStartWaitMax  = 60 * time.Second
	prepullStartWaitPoll = 2 * time.Second
)

// prepullAuraFileName is the fire's evidence in the walk's out dir.
const prepullAuraFileName = "prepull-auras.json"

// prepullAuraFile is prepull-auras.json: the flag set, the tolerance, the merged lists, the boss rows whose fire
// verified, the fires that failed and every bot's line.
type prepullAuraFile struct {
	Flag       []string             `json:"flag"`
	Tolerance  int                  `json:"tolerance"`
	Lists      []prepullAuraList    `json:"lists"`
	BossRows   []int                `json:"boss_rows"`
	StartRow   int                  `json:"start_row,omitempty"`
	Attempts   []prepullAuraAttempt `json:"attempts"`
	Bots       []prepullAuraRecord  `json:"bots"`
	RoleSource string               `json:"role_source,omitempty"` // where each bot's aura role came from
}

// prepullAuraAttempt is one fire that did not verify: the boss row it prepared (or the start row, start=true) and why
// it stopped. A fire that succeeded is a boss row, or start_row, instead, so the two never overlap.
type prepullAuraAttempt struct {
	BossRow int    `json:"boss_row"`
	Start   bool   `json:"start,omitempty"`
	Reason  string `json:"reason"`
}

// prepullAuraList is one role's merged list, as the walk resolved it.
type prepullAuraList struct {
	Role string   `json:"role"`
	IDs  []uint32 `json:"ids"`
}

// prepullAuraRecord is one bot's line: its aura role, every id this walk sent it, and what the last read showed.
type prepullAuraRecord struct {
	Bot     string   `json:"bot"`
	Role    string   `json:"role"`
	Specs   string   `json:"specs,omitempty"` // the module's spec strategies the role was read from
	Sent    []uint32 `json:"sent"`
	Seen    []uint32 `json:"seen"`
	Missing []uint32 `json:"missing"`
}

// prepullAuraTarget is one bot of a fire: its record, its guid, the list it must carry and what it lacked at the send.
type prepullAuraTarget struct {
	rec     *prepullAuraRecord
	unit    dungeon.AuraTarget
	want    []dungeon.Buff
	missing []dungeon.Buff
}

// validatePrepullAura parses the flag values into the walk's lists before any GM line (phase 1: a bad role, an
// unknown preset, spell id 0, a role named twice or an over-full list is an exit-2 refusal).
func (w *walk) validatePrepullAura() error {
	set, err := raidwalk.PlanPrepullAuras(w.opts.prepullAura)
	if err != nil {
		return stopf(exitUsage, "%v", err)
	}
	w.prepullAuras = set
	return nil
}

// prepullGate is the --prepull-aura firing gate. It returns the boss row to prepare and whether a fire may go out
// now (0: nothing to do, and no failure). The step before a boss row fires in whatever phase, as long as the status
// is still before the pull (prePullOK: no pulling/fighting note, no later pull line of the same page, no party bot
// in combat in a recent frame). A status that is not before the pull defers to the boss row's own status, exactly as
// the rest/gather-only gate did. The boss row itself fires only while the pause can still land before the pull: the
// same pre-pull check AND no engaged boss, read FRESH from the server (prepullBossEngaged) rather than from the
// cached hostile frames bossEngagedNow reads: those can still show the boss unengaged while the pull has started and
// the next view frame has not arrived. A boss row that becomes current otherwise is (step, false), the caller's
// blocking failure: the preparation the run was started to test never happened. A failed boss read is an error too
// (the gate never fires on a read it could not make); the caller stops the walk with it, and no aura goes out.
func (w *walk) prepullGate(ctx context.Context, st tbm.RunStatus, later bool) (int, bool, error) {
	if st.State != tbm.RunRunning && st.State != tbm.RunPaused {
		return 0, false, nil
	}
	if st.Kind == planexport.KindBoss {
		if _, ok := w.bossRowAt(st.Step); !ok {
			return 0, false, nil
		}
		if w.prepullAuraSent[st.Step] {
			// The row was prepared already: its own status is no failure and needs no read. The caller's sent check
			// returns before it treats fire=false as one.
			return st.Step, false, nil
		}
		if !w.prePullOK(st, later, w.env.now()) {
			return st.Step, false, nil
		}
		engaged, err := w.prepullBossEngaged(ctx, st.Step)
		if err != nil {
			return 0, false, err
		}
		return st.Step, !engaged, nil
	}
	next := st.Step + 1
	if _, ok := w.bossRowAt(next); !ok {
		return 0, false, nil
	}
	if !w.prePullOK(st, later, w.env.now()) {
		return 0, false, nil
	}
	return next, true, nil
}

// prepullAuraSendPending is pollOnce's firing: at the step before a boss row (whatever its phase) or at the boss row
// itself when the run passed that step too fast to pause, once per boss row, the auras go out under a run pause that
// must hold. A boss row that became current without a verified fire fails the walk (a configured boss row the walk
// never prepared is not the test the run was started for). A boss row whose arena read fails is the gate's error and
// stops the walk too. Whenever the status was running before the pause attempt, the resume is attempted afterwards
// even when the hold or the fire failed: a run left paused stops the raid silently. A status that was already paused
// sends no resume. A row is recorded as done only after the auras verified within the tolerance. It reports whether a
// firing happened, so pollOnce breaks its line loop (the next poll re-reads from the cursor), like the instance-data
// firing. Under --prepull-aura-when start nothing is sent here: the fire already went out before the run's first move,
// and each boss row only gets a read of the auras the raid still carries (prepullAuraPullRead).
func (w *walk) prepullAuraSendPending(ctx context.Context, st tbm.RunStatus, later bool) (bool, error) {
	if len(w.prepullAuras) == 0 {
		return false, nil
	}
	if w.opts.prepullAuraWhen == prepullAuraWhenStart {
		// --prepull-aura-when start fired once before the run's first move: a boss row is never a place to send
		// again. The pull gets the coverage read instead (prepullAuraPullRead), which sends nothing and so never
		// pauses the run, and the once-per-row guard keeps it to one line per boss.
		return false, w.prepullAuraPullRead(ctx, st, later)
	}
	step, fire, err := w.prepullGate(ctx, st, later)
	if err != nil {
		return false, err
	}
	if step == 0 {
		return false, nil
	}
	if w.prepullAuraSent[step] {
		return false, nil
	}
	if !fire {
		return false, stopf(exitRaidWalkFailed,
			"prepull-aura: boss row %d became current with no verified pre-pull application: the auras were never sent", step)
	}
	pauseAttempted := st.State == tbm.RunRunning
	fireErr := w.prepullAuraHold(ctx, step, pauseAttempted)
	if fireErr == nil {
		fireErr = w.prepullAuraFire(ctx, step, false)
		if fireErr == nil {
			if w.prepullAuraSent == nil {
				w.prepullAuraSent = map[int]bool{}
			}
			w.prepullAuraSent[step] = true
		}
	}
	if pauseAttempted {
		if _, err := w.commander(ctx, commanderReq{Op: "run", Verb: "resume", WaitMS: 10000}); err != nil {
			if fireErr != nil {
				w.note("prepull-aura: WARN: the run resume after boss row %d also failed (%v)", step, err)
				return true, fireErr
			}
			return true, stopf(exitRaidWalkFailed, "prepull-aura: boss row %d: the run resume after the auras failed: %v", step, err)
		}
	}
	return true, fireErr
}

// prepullAuraStart is the --prepull-aura-when start fire: once per walk, right after the placement and before
// `run start`, so the run's first move already carries the lists (a flask and the world buffs last 2 h or more, so a
// fight that starts later keeps them). It sends nothing in the default boss mode, and never a second time. The run is
// not started yet, so nothing moves and no pause is needed. The fire goes out only on a POSITIVELY confirmed idle run:
// a `run status` reply that parsed a TBM:RUN line in state none or done. A refused reply, an answer without a status
// line, and any running state (whatever plan it names) are all blocking failures, because a batch sent while the raid
// walks is not the preparation before the first move: walk 101's Hakkar row 689 is exactly the run that walked into
// combat while the auras were going out. A paused run is not proof either (the walk never paused one before the start
// fire, so it cannot show the pause as its own); it fails closed too. Every other fail-closed rule of the boss-row fire
// stays (every hire, roles from the module's per-bot state, a fresh combat watch over the whole raid, strict
// verification, zero or partial coverage fails), and a failed fire stops the walk: a run whose start preparation did
// not verify is not the test the walk was started for. The verified fire lands in prepull-auras.json as start_row.
func (w *walk) prepullAuraStart(ctx context.Context) error {
	if len(w.prepullAuras) == 0 || w.opts.prepullAuraWhen != prepullAuraWhenStart || w.prepullAuraStarted {
		return nil
	}
	w.prepullAuraStarted = true
	rep, err := w.commander(ctx, commanderReq{Op: "run", Verb: "status", WaitMS: 5000})
	if err != nil {
		return stopf(exitRaidWalkFailed, "prepull-aura: start: the run status: %v, so the run's state cannot be ruled out: no aura was sent", err)
	}
	if rep.Reply.Err != nil {
		return stopf(exitRaidWalkFailed,
			"prepull-aura: start: the run status was refused (%s), so the run's state cannot be ruled out: no aura was sent", rep.Reply.Err.Message)
	}
	run, line, ok := lastRunLine(rep.Reply.Lines)
	if !ok {
		return stopf(exitRaidWalkFailed,
			"prepull-aura: start: the run status answer has no TBM:RUN line, so the run's state cannot be ruled out: no aura was sent")
	}
	switch run.State {
	case tbm.RunNone, tbm.RunDone:
		// Positively no run: the only states a start fire may go out in.
	case tbm.RunRunning:
		return stopf(exitRaidWalkFailed,
			"prepull-aura: start: a run is already running (%s), so the lists cannot go out before the first move: no aura was sent", line)
	default:
		return stopf(exitRaidWalkFailed,
			"prepull-aura: start: the run is %s (%s), which is not a confirmed idle state, so the lists cannot go out before the first move: no aura was sent",
			run.State, line)
	}
	step := w.prepullAuraRunStartRow()
	w.logLine(w.env.now(), fmt.Sprintf("prepull-aura: start: the lists go out before the run starts (row %d)", step))
	return w.prepullAuraFire(ctx, step, true)
}

// prepullAuraRunStartRow is the row the run is about to start at: a --place walk's staged start (PlaceStart rewrote
// --from to it), else the --from step, else the plan's first row. It is what start_row records, so the file names the
// pull whose preparation the start fire is.
func (w *walk) prepullAuraRunStartRow() int {
	if n, err := raidwalk.FromStartStep(w.rows, w.fromWords); err == nil && n >= 1 && n <= len(w.rows.Steps) {
		return n
	}
	if w.startStep >= 1 && w.startStep <= len(w.rows.Steps) {
		return w.startStep
	}
	return 1
}

// prepullAuraPullRead is the --prepull-aura-when start read at the pull: on a status that has the boss row itself
// current AND the run in the pull phase (the run is at the boss, awaiting the pull) or the fight-start phase (its task
// has begun), it reads which auras every hire of the raid still carries and logs them (prepullAuraCoverageRead). No
// aura is sent and the run is never paused: the fire went out before the run's first move, and this shows the coverage
// the pull actually starts with. A status of the step BEFORE the boss row is not the pull and reads nothing (walk
// 101b's rest status could be a minute of walking away from the pull); neither is a boss row in any other phase. A row
// is read once in the pull phase and refreshed once at the fight start (task) phase, so the last line is the coverage
// the fight itself began with; the phase is in the line. The phase is recorded only AFTER a successful read, so a
// failed read (a refused view, a unit store hiccup) is retried at the next qualifying status instead of the row
// counting as read. A read that keeps failing is logged as a WARN each time: the auras are already on the raid, so a
// read cannot turn a good run into a failed one.
func (w *walk) prepullAuraPullRead(ctx context.Context, st tbm.RunStatus, later bool) error {
	if st.State != tbm.RunRunning && st.State != tbm.RunPaused {
		return nil
	}
	if st.Kind != planexport.KindBoss {
		return nil
	}
	if _, ok := w.bossRowAt(st.Step); !ok {
		return nil
	}
	if st.Phase != "pull" && st.Phase != "task" {
		return nil
	}
	step, phase := st.Step, st.Phase
	if w.prepullAuraRead[step] == phase {
		return nil
	}
	if err := w.prepullAuraCoverageRead(ctx, step, phase); err != nil {
		w.note("prepull-aura: WARN: boss row %d: the coverage read at the pull failed: %v", step, err)
		w.logLine(w.env.now(), fmt.Sprintf("prepull-aura: WARN: boss row %d: the coverage read at the pull failed: %v", step, err))
		return nil
	}
	if w.prepullAuraRead == nil {
		w.prepullAuraRead = map[int]string{}
	}
	w.prepullAuraRead[step] = phase
	return nil
}

// prepullAuraCoverageRead reads every hire's auras and logs how many of its list it still carries, with the missing
// ids: the evidence of what the raid brought into the fight under --prepull-aura-when start. The lists are the ones
// the start fire resolved (each bot's record keeps its aura role), so the read names the same ids the fire sent. A
// hire the commander's view no longer holds, one with no start record and one the unit read cannot see are named in
// the line rather than dropped: a coverage line that silently skipped a bot would read as full coverage.
func (w *walk) prepullAuraCoverageRead(ctx context.Context, step int, phase string) error {
	raid, err := w.prepullRaid(ctx)
	if err != nil {
		return err
	}
	port := walkAuras{w: w}
	inRaid := make(map[string]tbm.PartyRec, len(raid))
	for _, m := range raid {
		inRaid[strings.ToLower(m.Name)] = m
	}
	var parts []string
	whole := 0
	roster := w.hireList()
	for _, name := range roster {
		m, ok := inRaid[strings.ToLower(name)]
		if !ok {
			parts = append(parts, name+" not in the commander's raid")
			continue
		}
		rec := w.prepullAuraBots[name]
		if rec == nil || rec.Role == "" {
			parts = append(parts, name+" has no start-fire record")
			continue
		}
		want := w.prepullAuras.For(rec.Role)
		if len(want) == 0 {
			parts = append(parts, fmt.Sprintf("%s has no list (role %q)", name, rec.Role))
			continue
		}
		missing, seen := dungeon.AurasMissing(ctx, port, dungeon.AuraTarget{GUID: m.GUID, Name: name}, want)
		if !seen {
			parts = append(parts, name+" unreadable")
			continue
		}
		if len(missing) == 0 {
			whole++
			parts = append(parts, fmt.Sprintf("%s %d/%d", name, len(want), len(want)))
			continue
		}
		parts = append(parts, fmt.Sprintf("%s %d/%d (missing %s)", name, len(want)-len(missing), len(want), idWords(raidwalk.PrepullIDs(missing))))
	}
	msg := fmt.Sprintf("prepull-aura: boss row %d: %d of %d hire(s) carry every requested aura at the pull (phase %s): %s",
		step, whole, len(roster), phase, strings.Join(parts, "; "))
	w.note("%s", msg)
	w.logLine(w.env.now(), msg)
	return nil
}

// prepullAuraHold puts the run on hold for the batch and confirms it. A refused, unacknowledged or unconfirmed pause
// is a blocking failure: no aura goes out on a run the walk cannot show is paused before the pull. The pause must
// stand on the step before the boss row; the one exception is a run that passed that step too fast to pause: then it
// may stand on the boss row itself, and only while the boss is not engaged — the boss's arena is read fresh from the
// server after the pause is confirmed (prepullBossEngaged), never from the cached frames, and the fire's own combat
// read still gates the commander and every member. A pause past the boss row is mid-pull and fails. The confirmation
// is a fresh `run status`, the run's own answer rather than the push feed's newest line.
func (w *walk) prepullAuraHold(ctx context.Context, step int, pause bool) error {
	if pause {
		rep, err := w.commander(ctx, commanderReq{Op: "run", Verb: "pause", WaitMS: 10000})
		if err != nil {
			return stopf(exitRaidWalkFailed, "prepull-aura: boss row %d: the run pause failed, no auras were sent: %v", step, err)
		}
		if rep.Reply.Err != nil {
			return stopf(exitRaidWalkFailed, "prepull-aura: boss row %d: the run pause was refused (%s), no auras were sent", step, rep.Reply.Err.Message)
		}
		if !rep.Reply.OK && rep.Reply.Kind != tbm.KindAck {
			return stopf(exitRaidWalkFailed, "prepull-aura: boss row %d: the run pause was not acknowledged (%s), no auras were sent", step, rep.Reply.Line)
		}
		w.logLine(w.env.now(), fmt.Sprintf("> .bot run pause (prepull-aura, boss row %d)", step))
	}
	rep, err := w.commander(ctx, commanderReq{Op: "run", Verb: "status", WaitMS: 5000})
	if err != nil {
		return stopf(exitRaidWalkFailed, "prepull-aura: boss row %d: the paused run could not be confirmed (%v), no auras were sent", step, err)
	}
	if rep.Reply.Err != nil {
		return stopf(exitRaidWalkFailed, "prepull-aura: boss row %d: the run status was refused (%s), no auras were sent", step, rep.Reply.Err.Message)
	}
	run, line, ok := lastRunLine(rep.Reply.Lines)
	if !ok {
		return stopf(exitRaidWalkFailed, "prepull-aura: boss row %d: the run status answer has no TBM:RUN line, no auras were sent", step)
	}
	if run.State != tbm.RunPaused {
		return stopf(exitRaidWalkFailed, "prepull-aura: boss row %d: the run is not paused (%s), no auras were sent", step, line)
	}
	if w.plan != nil && run.Plan != "" && run.Plan != w.plan.Name {
		return stopf(exitRaidWalkFailed, "prepull-aura: boss row %d: the paused run is %s, not %s, no auras were sent", step, run.Plan, w.plan.Name)
	}
	if run.Step != step-1 && run.Step != step {
		return stopf(exitRaidWalkFailed, "prepull-aura: boss row %d: the run paused at step %d, past the step before it (step %d), no auras were sent",
			step, run.Step, step-1)
	}
	if run.Step == step {
		// The run passed the step before the boss too fast for the pause. A pause on the boss row is still before the
		// pull only while the boss is not engaged: the arena is read fresh here, after the pause was confirmed, because
		// the cached hostile frames can lag the server by a frame and show a boss the commander already pulled. The fire
		// re-reads the commander and every living member for combat before its first aura, so a pull that started anyway
		// still stops the batch there.
		engaged, err := w.prepullBossEngaged(ctx, step)
		if err != nil {
			return err
		}
		if engaged {
			return stopf(exitRaidWalkFailed, "prepull-aura: boss row %d: the run paused on the boss row with the boss engaged, no auras were sent", step)
		}
		w.note("prepull-aura: boss row %d: the run reached the boss row before the pause; it paused there with the boss not engaged", step)
	}
	return nil
}

// prepullBossEngaged reads the boss row's arena FRESH from the server and reports whether any unit there is in
// combat: `q units 60 <anchor> all`, the same read the reuse-instance guard (`placeBossEngaged`) makes, with `cb`
// the combat state. The walk's cached hostile frames (`bossEngagedNow`) are not used here: the push feed's statuses
// are handled before the next view frame is fetched, so a cached frame can still show the boss unengaged after the
// commander has pulled. It fails closed: a boss row without a point, a refused or unanswered read, a truncated reply,
// and a unit row without a well-formed `cb` are all errors or counted as combat, never a clean read, and the caller
// sends no aura on any of them (ttUnitOf's own InCombat stays tolerant for the other callers; the prepull read
// demands the field).
func (w *walk) prepullBossEngaged(ctx context.Context, step int) (bool, error) {
	row, ok := w.bossRowAt(step)
	if !ok {
		return false, stopf(exitRaidWalkFailed, "prepull-aura: boss row %d is not one of the plan's rows", step)
	}
	if row.X == 0 && row.Y == 0 {
		return false, stopf(exitRaidWalkFailed,
			"prepull-aura: boss row %d has no point to read the boss's combat state at, so combat cannot be ruled out: no auras were sent", step)
	}
	cmd := fmt.Sprintf("q units %d %s %s %s all", placeBossArenaYd, num32(row.X), num32(row.Y), num32(row.Z))
	out, err := w.tt(ctx, cmd)
	if err != nil {
		return false, stopf(exitRaidWalkFailed,
			"prepull-aura: boss row %d: the arena read (%s) failed, so combat cannot be ruled out: no auras were sent: %v", step, cmd, err)
	}
	body, ok := ttLast(out)
	if !ok {
		return false, stopf(exitRaidWalkFailed,
			"prepull-aura: boss row %d: the arena read (%s) gave no reply, so combat cannot be ruled out: no auras were sent", step, cmd)
	}
	if body.flag("trunc") {
		return false, stopf(exitRaidWalkFailed,
			"prepull-aura: boss row %d: the arena read (%s) lists more units than one reply carries, so combat cannot be ruled out: no auras were sent", step, cmd)
	}
	for _, m := range body.list("units") {
		u := ttUnitOf(m)
		if !u.CBKnown {
			// The reply does not say whether this unit is fighting (a degraded or version-skewed toolkit answer still
			// has a valid envelope and no trunc flag). An unproven unit is not an out-of-combat unit.
			w.note("prepull-aura: boss row %d: %s (%d) carries no combat state in the arena read at %s: counted as in combat", step, u.Name, u.Entry, cmd)
			return true, nil
		}
		if u.InCombat && !u.Dead {
			w.note("prepull-aura: boss row %d: %s (%d) is in combat in the arena read at %s", step, u.Name, u.Entry, cmd)
			return true, nil
		}
	}
	return false, nil
}

// prepullAuraCombatClear re-reads the commander and every living raid member and refuses the batch while any one of
// them is in combat: a pull that starts after the pause must stop the auras, not receive them after the encounter
// began. The commander's view leaves the player out, so the watch carries it first; a pull the commander started is
// the one a member-only check misses. It fails closed on the read too: a unit the RPC cannot read may be fighting, so
// the batch stops there as well. The caller builds the watch from the commander and the whole raid, not only
// the members that carry an aura list: an unhired member can pull too. where names the fire in its failure
// ("boss row 5", or "start (row 1)"), so the walk-101 line reads as the start fire it was.
func (w *walk) prepullAuraCombatClear(ctx context.Context, where string) error {
	port := walkAuras{w: w}
	for _, t := range w.prepullAuraWatch {
		u, seen := port.ViewUnit(ctx, t)
		if !seen {
			return stopf(exitRaidWalkFailed,
				"prepull-aura: %s: the unit read of %s failed, so combat cannot be ruled out: no further auras were sent", where, t.Name)
		}
		if u.Dead || u.Ghost {
			continue
		}
		if u.InCombat {
			return stopf(exitRaidWalkFailed,
				"prepull-aura: %s: combat started (%s is in combat): no further auras were sent", where, t.Name)
		}
	}
	return nil
}

// prepullRaid reads the raid the run's commander leads from the commander's view. The walk's own `party` RPC is the
// PLAYER's group, and a TurtleBots raid is the hired bots' own raid: the player that drives the walk stands outside
// it, so every raid seat is a hire (docs/okf/raids/roster-format.md). Walk 101: the slot answered `in_group:false`
// with no members while the run leader's view held the 20 bots, so the fire saw 0 bots and still recorded the boss
// row. The view's party records are the run leader's raid (guid, name, health, dead state, map), which is where the fire
// looks for the walk's hires: a frame member the walk never hired is watched for combat but never targeted, and a hire
// the frame lacks fails the coverage check. It fails closed: a failed read, no frame, a truncated frame and a frame
// without a party bot are errors, never an empty fire.
func (w *walk) prepullRaid(ctx context.Context) ([]tbm.PartyRec, error) {
	_, frame, err := w.view(ctx, 0, 0)
	if err != nil {
		return nil, stopf(exitRaidWalkFailed, "prepull-aura: the commander view read failed, so the raid cannot be read and no aura was sent: %v", err)
	}
	if frame == nil {
		return nil, stopf(exitRaidWalkFailed, "prepull-aura: the commander view holds no frame, so the raid cannot be read and no aura was sent (is a commander bound to the run's raid?)")
	}
	if frame.Truncated {
		return nil, stopf(exitRaidWalkFailed, "prepull-aura: the commander view's frame is truncated, so the raid cannot be read whole and no aura was sent")
	}
	if len(frame.Party) == 0 {
		return nil, stopf(exitRaidWalkFailed, "prepull-aura: the commander view shows no party bot, so no aura was sent (is a commander bound to the run's raid?)")
	}
	return append([]tbm.PartyRec{}, frame.Party...), nil
}

// prepullAuraStartWait waits for every hire to be in the commander's view and alive, and in the agent's own unit
// store, before the start fire reads the raid's classes and roles. Walk 101's ZG Jeklik run (plan start row 1) is why:
// the placement's `.tele` wave and the fire shared a second, the fire went out 0.16 s after the last `.tele`, and the
// unit read of the MT failed twice before the walk spent a single GM line - the commander's frame already named the
// raid, but the agent's unit store had not received the teleported bots yet. The boss rows never hit this: the raid
// has been in both stores for minutes when the run reaches a boss. The wait polls every 2 s for up to 60 s and logs
// how long it took, so a slow instance shows its own number. It stays fail-closed: a hire still missing after the
// wait (or a view read that keeps failing) stops the fire with the hire named, never a fire on an unproven raid. The
// unit store is part of the wait because it is the store the very next read (the class) goes to, and it filled later
// than the frame in walk 101.
func (w *walk) prepullAuraStartWait(ctx context.Context) ([]tbm.PartyRec, error) {
	begin := w.env.now()
	roster := w.hireList()
	port := walkAuras{w: w}
	var lastErr error
	for {
		raid, err := w.prepullRaid(ctx)
		var missing []string
		if err == nil {
			inView := make(map[string]tbm.PartyRec, len(raid))
			for _, m := range raid {
				if m.GUID != 0 && m.Name != "" {
					inView[strings.ToLower(m.Name)] = m
				}
			}
			for _, name := range roster {
				m, ok := inView[strings.ToLower(name)]
				if !ok {
					missing = append(missing, name+" is not in the commander's view")
					continue
				}
				if m.Dead || m.State == "d" {
					missing = append(missing, name+" is dead in the commander's view")
					continue
				}
				u, seen := port.ViewUnit(ctx, dungeon.AuraTarget{GUID: m.GUID, Name: name})
				switch {
				case !seen:
					missing = append(missing, name+" is not in the agent's unit store")
				case u.Dead || u.Ghost:
					missing = append(missing, name+" is dead in the agent's unit store")
				}
			}
			if len(missing) == 0 {
				w.logLine(w.env.now(), fmt.Sprintf("prepull-aura: start: the raid's %d hire(s) are in the commander's view and alive after %s",
					len(roster), w.env.now().Sub(begin).Round(time.Millisecond)))
				return raid, nil
			}
			lastErr = nil
		} else {
			lastErr = err
		}
		waited := w.env.now().Sub(begin).Round(time.Millisecond)
		if !w.env.now().Before(begin.Add(prepullStartWaitMax)) {
			if lastErr != nil {
				return nil, stopf(exitRaidWalkFailed,
					"prepull-aura: start: after waiting %s for the raid to appear, the commander view still cannot be read, so no aura was sent: %v", waited, lastErr)
			}
			return nil, stopf(exitRaidWalkFailed,
				"prepull-aura: start: after waiting %s, %d of the walk's %d hire(s) never appeared in the commander's view and alive: %s: no aura was sent",
				waited, len(missing), len(roster), strings.Join(missing, "; "))
		}
		if err := w.env.sleep(ctx, prepullStartWaitPoll); err != nil {
			return nil, err
		}
	}
}

// prepullAuraCoverage fails the fire unless every bot the walk hired received a list: the walk tests the preparation
// a real raid carries in, so a hire the lists do not reach is a broken flag set (a role word the classes never
// produce, or no "all" list) or a raid that lost bots, never a fire to record. Walk 101 wrote prepull-auras.json with
// boss_rows [691] and 0 bots; a boss row is only ever recorded for a preparation that reached the walk's roster. The
// names and the reasons go into the failure, so the file's attempt says which bot and why. where names the fire
// ("boss row 5", or "start (row 1)"), so a start failure is never read as a boss row's.
func (w *walk) prepullAuraCoverage(where string, bots int, targets []prepullAuraTarget, uncovered map[string]string) error {
	if len(targets) == 0 {
		return stopf(exitRaidWalkFailed, "prepull-aura: %s: the commander view shows %d bot(s) and not one of them received a list: no aura was sent and no row was recorded", where, bots)
	}
	covered := make(map[string]bool, len(targets))
	for _, t := range targets {
		covered[t.rec.Bot] = true
	}
	roster := w.hireList()
	var short []string
	for _, name := range roster {
		if covered[name] {
			continue
		}
		why := uncovered[name]
		if why == "" {
			why = "is not in the commander view"
		}
		short = append(short, name+" "+why)
	}
	if len(short) == 0 {
		return nil
	}
	return stopf(exitRaidWalkFailed, "prepull-aura: %s: %d of the walk's %d hire(s) received no list (%s): the rest did not get a full preparation, so no row was recorded",
		where, len(short), len(roster), strings.Join(short, "; "))
}

// prepullAuraWhere is the fire's own name for what it prepares: a boss row, or the run's start under
// --prepull-aura-when start (start_row). Every failure line opens with it.
func prepullAuraWhere(step int, start bool) string {
	if start {
		return fmt.Sprintf("start (row %d)", step)
	}
	return fmt.Sprintf("boss row %d", step)
}

// prepullAuraFire puts the flag's lists on every living hire of the commander's raid, reads them back, writes
// prepull-auras.json and fails the walk when a bot misses more auras than --prepull-aura-tolerance (0 by default:
// every requested aura must verify) or when the raid's lists do not reach every bot the walk hired (a fire that
// covered fewer bots than the roster is no preparation either, and it is never recorded as one). A frame member the
// walk never hired is not a target: it is watched for combat like a hire and logged, so a misbound or stale commander
// can neither receive GM auras nor pull unnoticed. A hire whose aura role the walk cannot tell fails the fire before
// any aura (a class the unit read cannot give, no row for it in the module's TBM:BOTSTATE snapshot, or a row whose kit
// names no spec or specs of more than one role), because only the "all" lists would reach it while coverage still
// passed. A failed fire is recorded as an
// attempt with its reason; only a fire whose auras verified within the tolerance appends the boss row (or start_row
// under --prepull-aura-when start), so the file never reports a preparation that did not happen. The file is written
// on every fire, failures included, so the attempt's reason and the missing list are always on disk. step is the boss
// row the fire prepares, or the run's start row when start is true.
func (w *walk) prepullAuraFire(ctx context.Context, step int, start bool) (err error) {
	where := prepullAuraWhere(step, start)
	defer func() {
		if err != nil {
			w.prepullAuraAttempts = append(w.prepullAuraAttempts, prepullAuraAttempt{BossRow: step, Start: start, Reason: err.Error()})
		}
		w.prepullAuraWatch, w.prepullAuraWhat = nil, ""
		if werr := w.prepullAuraWrite(); werr != nil {
			w.note("prepull-aura: WARN: %s could not be written (%v)", prepullAuraFileName, werr)
		}
	}()
	// The start fire is the one fire that runs inside the placement's own teleport wave, so it is the one that waits
	// for the raid to arrive (prepullAuraStartWait): at every boss row the raid has been in the commander's view and
	// the agent's unit store for a while already.
	var raid []tbm.PartyRec
	if start {
		raid, err = w.prepullAuraStartWait(ctx)
	} else {
		raid, err = w.prepullRaid(ctx)
	}
	if err != nil {
		return err
	}
	port := walkAuras{w: w}
	cfg := dungeon.AuraCfg{Gap: prepullAuraGap, Settle: prepullAuraSettle, ViewWait: prepullAuraViewWait}
	// The combat watch is the commander and every living member of its raid, list or no list: a pull may come from
	// the commander (which the raid view leaves out) or from a bot the flag does not cover. A unit the unit read
	// cannot confirm is living stays in the watch, and its unreadable row fails the recheck: an unproven raid is not
	// an out-of-combat raid.
	st, err := w.status(ctx)
	if err != nil {
		return stopf(exitRaidWalkFailed, "prepull-aura: the commander's status: %v", err)
	}
	if st.Character == "" || st.GUID == 0 {
		return stopf(exitRaidWalkFailed, "prepull-aura: the status names no commander (character %q, guid %s), so its combat state cannot be read", st.Character, st.GUID)
	}
	watch := []dungeon.AuraTarget{{GUID: st.GUID, Name: st.Character}}
	// The fire targets only the walk's hires: the commander frame can hold a bot someone else invited, and a frame
	// name the walk never hired is not its to buff with GM commands. Hires are matched case-insensitively, and the
	// roster's own spelling is what the records and the failure name.
	roster := w.hireList()
	hired := make(map[string]string, len(roster))
	for _, name := range roster {
		hired[strings.ToLower(name)] = name
	}
	// The per-bot role source, read before any role: the module's own TBM:BOTSTATE rows. A refused or incomplete
	// snapshot fails the fire here, before any aura, with the read's reason (never an "all"-only bot).
	if _, err := w.prepullBotSpecs(ctx); err != nil {
		return stopf(exitRaidWalkFailed, "prepull-aura: %s: the per-bot roles could not be read: %v: no aura was sent", where, err)
	}
	var targets []prepullAuraTarget
	// uncovered names every hired bot that gets no list and why, for the coverage check after the combat read.
	uncovered := map[string]string{}
	for _, m := range raid {
		if m.GUID == 0 || m.GUID == st.GUID || m.Name == "" {
			continue
		}
		name, ok := hired[strings.ToLower(m.Name)]
		if !ok {
			// A frame member the walk never hired is not a target: a misbound or stale commander must not receive GM
			// auras. It is still a member of the raid the fire runs inside, so it goes into the combat watch like
			// every other living member, and an unreadable row or combat there stops the batch before any aura.
			watch = append(watch, dungeon.AuraTarget{GUID: m.GUID, Name: m.Name})
			msg := fmt.Sprintf("prepull-aura: %s: %s is in the commander's raid but not one of the walk's hires: watched, not targeted", where, m.Name)
			w.note("%s", msg)
			w.logLine(w.env.now(), msg)
			continue
		}
		t := dungeon.AuraTarget{GUID: m.GUID, Name: name}
		u, seen := port.ViewUnit(ctx, t)
		if !seen {
			// One retry: a unit store that missed the bot for one call must not decide its role from a zero class.
			u, seen = port.ViewUnit(ctx, t)
		}
		if (seen && (u.Dead || u.Ghost)) || (!seen && (m.Dead || m.State == "d")) {
			uncovered[name] = "is dead, so it cannot carry a list"
			continue
		}
		watch = append(watch, t)
		// A role guessed from an unreadable row would put only the "all" lists on the bot while coverage still passed,
		// so an unknown class fails the fire: a hire gets its role's list or the walk stops with it named.
		if !seen {
			return stopf(exitRaidWalkFailed,
				"prepull-aura: %s: the unit read of %s failed twice, so its class and aura role are unknown: no aura was sent", where, name)
		}
		if u.Class == 0 {
			return stopf(exitRaidWalkFailed,
				"prepull-aura: %s: %s is a hire whose unit row carries no class, so its aura role is unknown: no aura was sent", where, name)
		}
		role, why := w.prepullRole(name, u.Class)
		if role == "" {
			return stopf(exitRaidWalkFailed,
				"prepull-aura: %s: %s: %s, so its aura role is unknown and only an \"all\" list would reach it: no aura was sent", where, name, why)
		}
		list := w.prepullAuras.For(role)
		if len(list) == 0 {
			uncovered[name] = fmt.Sprintf("has no list (role %q, and no \"all\" list covers it)", role)
			continue
		}
		rec := w.prepullAuraRecordFor(name, role)
		rec.Specs = w.prepullAuraSpecs[strings.ToLower(name)]
		missing := list
		if seen {
			missing, _ = dungeon.AurasMissing(ctx, port, t, list)
		}
		// Until the read-back below proves otherwise (or the batch aborts early), the auras that did not land are
		// what the bot lacks: the file never claims a preparation the walk has not seen.
		rec.Missing = raidwalk.PrepullIDs(missing)
		rec.Seen = subtractIDs(raidwalk.PrepullIDs(list), rec.Missing)
		targets = append(targets, prepullAuraTarget{rec: rec, unit: t, want: list, missing: missing})
	}
	w.prepullAuraWatch, w.prepullAuraWhat = watch, where
	if err := w.prepullAuraCombatClear(ctx, where); err != nil {
		return err
	}
	if err := w.prepullAuraCoverage(where, len(raid), targets, uncovered); err != nil {
		return err
	}
	for i := range targets {
		tg := &targets[i]
		if len(tg.missing) == 0 {
			continue
		}
		if oc, err := dungeon.ApplyAuras(ctx, port, tg.unit, tg.missing, cfg, w.env.now); err != nil {
			return err
		} else if oc != dungeon.AuraTaken {
			w.note("prepull-aura: %s: the first .aura (%s) did not land on the bot", tg.rec.Bot, tg.missing[0].Name)
		}
	}
	// One read-back with a short retry window: the auras sent last need a moment to show in the unit store, and a
	// bot the store does not hold tells nothing (an unproven aura counts as missing).
	tol := w.opts.prepullAuraTolerance
	var over, within []string
	for deadline := w.env.now().Add(prepullAuraVerifyWait); ; {
		over, within = over[:0], within[:0]
		for i := range targets {
			tg := &targets[i]
			missing, seen := dungeon.AurasMissing(ctx, port, tg.unit, tg.want)
			if !seen {
				missing = tg.want
			}
			tg.rec.Missing = raidwalk.PrepullIDs(missing)
			tg.rec.Seen = subtractIDs(raidwalk.PrepullIDs(tg.want), tg.rec.Missing)
			switch {
			case len(missing) > tol:
				over = append(over, tg.rec.Bot)
			case len(missing) > 0:
				within = append(within, tg.rec.Bot)
			}
		}
		if len(over) == 0 || !w.env.now().Before(deadline) {
			break
		}
		if err := w.env.sleep(ctx, prepullAuraVerifyPoll); err != nil {
			return err
		}
	}
	if len(over) > 0 {
		return stopf(exitRaidWalkFailed, "prepull-aura: %s: %d bot(s) miss auras beyond the tolerance of %d (%s): %s",
			where, len(over), tol, w.prepullAuraMissSummary(over), prepullAuraFileName)
	}
	if len(within) > 0 {
		w.logLine(w.env.now(), fmt.Sprintf("prepull-aura: %s: %d bot(s) carry their lists; %d bot(s) miss auras within the tolerance of %d: %s",
			where, len(targets)-len(within), len(within), tol, w.prepullAuraMissSummary(within)))
	} else {
		w.logLine(w.env.now(), fmt.Sprintf("prepull-aura: %s: %d bot(s) carry their lists", where, len(targets)))
	}
	// The fire is a verified preparation now, and only now: every failure above left it out (its attempt carries the
	// reason), so a consumer of the file reads boss_rows, and start_row, as applications that happened.
	if start {
		w.prepullAuraStartRow = step
		return nil
	}
	if w.prepullAuraRows == nil || !slices.Contains(w.prepullAuraRows, step) {
		w.prepullAuraRows = append(w.prepullAuraRows, step)
	}
	return nil
}

// prepullAuraClear forgets which boss rows fired, so the rest before the next boss row (the one the wipe retries)
// sends the lists again: a death stripped them. The bots' records keep their sent ids; the next fire's read-back
// rewrites seen and missing.
func (w *walk) prepullAuraClear() {
	if len(w.prepullAuras) == 0 {
		return
	}
	w.prepullAuraSent = nil
}

// prepullAuraRecordFor is the bot's accumulating record, created on its first fire.
func (w *walk) prepullAuraRecordFor(name, role string) *prepullAuraRecord {
	if w.prepullAuraBots == nil {
		w.prepullAuraBots = map[string]*prepullAuraRecord{}
	}
	rec, ok := w.prepullAuraBots[name]
	if !ok {
		rec = &prepullAuraRecord{Bot: name, Role: role, Sent: []uint32{}, Seen: []uint32{}, Missing: []uint32{}}
		w.prepullAuraBots[name] = rec
	}
	if rec.Role == "" {
		rec.Role = role
	}
	return rec
}

// prepullRole is the aura role a bot plays, PER BOT from the module's own state rows: the `specs=` field of its
// `TBM:BOTSTATE` line (prepullBotSpecs), read by the walk's `.bot roster` and mapped by raidwalk.BotStateAuraRole. The
// roster batch is not the source: a mixed batch ("5h 13d") names no role for a priest, a paladin, a druid or a shaman,
// and that batch is what walk 101b aborted on ("Whiteguard: the batch that hired it (rest) names no single role for a
// priest"). The module installs each hire's kit from its own role and spec, so its row says which spec the bot fights
// with; the walk maps that to the aura list. The walk's own MT and OT are tanks whatever their kit (they hold the
// raid's front), but they still need a state row like every hire. role == "" comes with the reason the walk cannot
// tell it (no state row for the bot, a class no aura role covers, or a kit naming no spec or specs of more than one
// role); the fire fails closed on that reason, never as an "all"-only bot.
func (w *walk) prepullRole(name string, class uint8) (role, why string) {
	specs, ok := w.prepullAuraSpecs[strings.ToLower(name)]
	if !ok {
		return "", "the module's `.bot roster` state names no row for it, so its role cannot be read"
	}
	if w.mt != "" && strings.EqualFold(name, w.mt) {
		return raidwalk.PrepullTank, ""
	}
	for _, ot := range w.ots {
		if strings.EqualFold(name, ot) {
			return raidwalk.PrepullTank, ""
		}
	}
	className := raidwalk.ClassName(class)
	if className == "" {
		return "", fmt.Sprintf("its unit row carries class %d, which no aura role covers", class)
	}
	return raidwalk.BotStateAuraRole(className, specs)
}

// prepullRoleSource names where prepullRole reads each bot's role, as prepull-auras.json records it.
const prepullRoleSource = ".bot roster TBM:BOTSTATE specs (the module's own per-bot spec strategy kit)"

// prepullBotStateTimeout bounds the `.bot roster` read (a bounded console snapshot, not a run poll).
const prepullBotStateTimeout = 15 * time.Second

// prepullBotSpecs reads each controllable bot's spec strategies from the module itself: `.bot roster` answers with a
// TBM:BOTSTATE row per live bot it can control ("TBM:BOTSTATE|<bot>|move=...,...,specs=<strategy>+<strategy>"), the same
// rows the addon's Party tab shows. For the walk's GM that set is the hired raid: the bots' master is the walker, and an
// ungrouped player's snapshot falls back to the bots it controls, so the bound raid is still listed. This is the
// per-bot role source: the reply rows name a bot, never only role counts, so a mixed batch is no obstacle.
//
// The snapshot is read whole or not at all: a refused roster (TBM:ROSTER_ERROR), no TBM:BOTSTATE_BEGIN, no END, or a
// BEGIN count the rows do not match is an error. A row without a specs field is kept with "" so the caller can fail on
// that bot by name. The read is cached for the walk: roles do not change mid-walk, and a later fire must not pay for a
// second snapshot.
func (w *walk) prepullBotSpecs(ctx context.Context) (map[string]string, error) {
	if w.prepullAuraSpecs != nil {
		return w.prepullAuraSpecs, nil
	}
	if err := w.gm(ctx, ".bot roster"); err != nil {
		return nil, err
	}
	deadline := w.env.now().Add(prepullBotStateTimeout)
	specs := map[string]string{}
	want, began, ended, refused := 0, false, false, ""
	// The attempt bound keeps a clock that does not move (a test fake, a stalled read) from spinning on a snapshot
	// that never ends; each pump waits up to a second, so the bound and the deadline agree.
	for tries := 0; !ended && tries < int(prepullBotStateTimeout/time.Second) && w.env.now().Before(deadline); tries++ {
		lines, err := w.pump(ctx, 1000)
		if err != nil {
			return nil, err
		}
		for _, line := range lines {
			switch {
			case strings.HasPrefix(line, "TBM:ROSTER_ERROR|"):
				refused = line
			case strings.HasPrefix(line, "TBM:BOTSTATE_BEGIN|"):
				n, err := strconv.Atoi(strings.TrimSpace(strings.TrimPrefix(line, "TBM:BOTSTATE_BEGIN|")))
				if err != nil {
					return nil, fmt.Errorf("the roster's TBM:BOTSTATE_BEGIN line is not a count: %s", line)
				}
				want, began = n, true
			case line == "TBM:BOTSTATE_END":
				ended = true
			case strings.HasPrefix(line, "TBM:BOTSTATE|"):
				name, rowSpecs, ok := parsePrepullBotState(line)
				if !ok {
					continue
				}
				specs[strings.ToLower(name)] = rowSpecs
			}
		}
	}
	if refused != "" {
		return nil, fmt.Errorf("the module refused `.bot roster`: %s", refused)
	}
	if !began || !ended {
		return nil, fmt.Errorf("no complete TBM:BOTSTATE snapshot in %s: began %v, ended %v", prepullBotStateTimeout, began, ended)
	}
	if len(specs) != want {
		return nil, fmt.Errorf("the roster announced %d bot state row(s) and sent %d: the snapshot is incomplete", want, len(specs))
	}
	w.prepullAuraSpecs, w.prepullAuraRoleSource = specs, prepullRoleSource
	return specs, nil
}

// parsePrepullBotState reads one TBM:BOTSTATE row: the bot's name and its `specs=` field ("" when the row names none).
// The fields are comma-joined key=value pairs, so a row without `|` or without a name is not a state row.
func parsePrepullBotState(line string) (name, specs string, ok bool) {
	rest, ok := strings.CutPrefix(line, "TBM:BOTSTATE|")
	if !ok {
		return "", "", false
	}
	name, fields, ok := strings.Cut(rest, "|")
	if !ok || strings.TrimSpace(name) == "" {
		return "", "", false
	}
	for _, f := range strings.Split(fields, ",") {
		if v, ok := strings.CutPrefix(f, "specs="); ok {
			return name, v, true
		}
	}
	return name, "", true
}

// prepullAuraMissSummary names bots and what each of them lacks.
func (w *walk) prepullAuraMissSummary(bots []string) string {
	var parts []string
	for _, name := range bots {
		rec := w.prepullAuraBots[name]
		if rec == nil {
			parts = append(parts, name)
			continue
		}
		parts = append(parts, fmt.Sprintf("%s (missing %s)", name, idWords(rec.Missing)))
	}
	return strings.Join(parts, "; ")
}

// prepullAuraWrite rewrites prepull-auras.json with the walk's whole prepull state.
func (w *walk) prepullAuraWrite() error {
	f := prepullAuraFile{Flag: w.opts.prepullAura, Tolerance: w.opts.prepullAuraTolerance, BossRows: w.prepullAuraRows,
		StartRow: w.prepullAuraStartRow, Attempts: append([]prepullAuraAttempt{}, w.prepullAuraAttempts...),
		Lists: []prepullAuraList{}, Bots: []prepullAuraRecord{}, RoleSource: w.prepullAuraRoleSource}
	for _, role := range raidwalk.PrepullRoles {
		if ids := raidwalk.PrepullIDs(w.prepullAuras.For(role)); len(ids) > 0 {
			f.Lists = append(f.Lists, prepullAuraList{Role: role, IDs: ids})
		}
	}
	names := make([]string, 0, len(w.prepullAuraBots))
	for name := range w.prepullAuraBots {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		f.Bots = append(f.Bots, *w.prepullAuraBots[name])
	}
	data, err := json.MarshalIndent(f, "", " ")
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(w.outDir, prepullAuraFileName), append(data, '\n'), 0600)
}

// subtractIDs is want without the ids of missing, in want's order.
func subtractIDs(want, missing []uint32) []uint32 {
	out := make([]uint32, 0, len(want))
	for _, id := range want {
		if !slices.Contains(missing, id) {
			out = append(out, id)
		}
	}
	return out
}

// prepullNames writes an aura list for the dry run ("Elixir of the Mongoose (17538), ...").
func prepullNames(list []dungeon.Buff) string {
	words := make([]string, len(list))
	for i, b := range list {
		words[i] = b.String()
	}
	return strings.Join(words, ", ")
}

// idWords writes ids for a note ("17538, 11405").
func idWords(ids []uint32) string {
	words := make([]string, len(ids))
	for i, id := range ids {
		words[i] = fmt.Sprintf("%d", id)
	}
	return strings.Join(words, ", ")
}

// walkAuras is the walk's dungeon.AuraPort: the agent's gm_aura RPC (a confirmed select then ".aura <id>"), the unit
// RPC for the target's auras and the commander's own, the walk's clock and its notes.
type walkAuras struct{ w *walk }

// SendAura puts one buff on the target with the gm_aura RPC, which selects the bot by guid, waits for the server to
// show that selection and sends the line only while it stands (session.GMAura). It first re-reads the party: a pull
// that started after the pause stops the batch before this line goes out. A refused or failed call breaks the walk: a
// preparation the walk could not send is not a preparation it may report.
func (p walkAuras) SendAura(ctx context.Context, t dungeon.AuraTarget, b dungeon.Buff) error {
	if len(p.w.prepullAuraWatch) > 0 {
		if err := p.w.prepullAuraCombatClear(ctx, p.w.prepullAuraWhat); err != nil {
			return err
		}
	}
	line := fmt.Sprintf(".aura %d", b.ID)
	p.w.logLine(p.w.env.now(), fmt.Sprintf("> %s (gm_aura, selected %s)", line, t.Name))
	params := map[string]any{"guid": t.GUID.String(), "spell_id": b.ID}
	if err := p.w.agent.Call(ctx, "gm_aura", params, nil, 15*time.Second); err != nil {
		return stopf(exitRaidWalkFailed, "prepull-aura: %s on %s: %v", line, t.Name, err)
	}
	if rec := p.w.prepullAuraBots[t.Name]; rec != nil && !slices.Contains(rec.Sent, b.ID) {
		rec.Sent = append(rec.Sent, b.ID)
	}
	return nil
}

// ViewUnit reads the target's unit row from the agent's store (auras included); false when the store does not hold it.
func (p walkAuras) ViewUnit(ctx context.Context, t dungeon.AuraTarget) (objects.Unit, bool) {
	var u objects.Unit
	if err := p.w.agent.Call(ctx, "unit", map[string]any{"guid": t.GUID.String()}, &u, 10*time.Second); err != nil {
		return objects.Unit{}, false
	}
	if u.GUID != t.GUID {
		return objects.Unit{}, false
	}
	return u, true
}

// CasterAuras is the commander's own auras: an aura that lands on it is a miss (the selection did not hold, or the
// account is below developer level and the core puts every aura on the sender).
func (p walkAuras) CasterAuras(ctx context.Context) ([]objects.Aura, bool) {
	st, err := p.w.status(ctx)
	if err != nil || st.Character == "" {
		return nil, false
	}
	u, seen := p.ViewUnit(ctx, dungeon.AuraTarget{GUID: st.GUID, Name: st.Character})
	if !seen {
		return nil, false
	}
	return u.Auras, true
}

func (p walkAuras) Sleep(ctx context.Context, d time.Duration) error { return p.w.env.sleep(ctx, d) }

func (p walkAuras) Note(msg string) {
	p.w.note("prepull-aura: %s", msg)
	p.w.logLine(p.w.env.now(), "prepull-aura: "+msg)
}
