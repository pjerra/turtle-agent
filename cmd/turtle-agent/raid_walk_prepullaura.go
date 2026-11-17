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
// (prepullAuraCoverage). Before the role reads the fire moves the COMMANDER to the raid when it does not stand on the
// target hire's map within prepullJoinRadius of it (prepullAuraJoinRaid, the revive path's
// `.appear`): the agent's own unit store only holds units in the commander's visibility range and is cleared on a
// map change, while its frame lists the whole raid on any map, so a commander left behind by the last placement read
// no hire at all (walk 105c's start fire on map 0 against hires on 309; walks 105/105b's Jeklik row 49 at 350-364 yd;
// walk 104's row 201 at 298-317 yd). No hire is ever moved: the commander hides itself first (`.gm visible off`, the
// revive's own line) so the appear lands unseen. A hire whose aura
// role the walk cannot tell — a unit row with no class, a bot the module's state rows leave out, or a state naming no
// single role's spec — is a failure too, never an "all"-only bot: the role
// comes PER BOT from the module itself (prepullRole, raidwalk.BotStateAuraRole), not from the class alone and not
// from the roster batch, because a batch like "5h 13d" names no role for a priest, paladin, druid or shaman and a ret
// paladin, a feral druid, an enhancement shaman and a shadow priest share a class with a healer. The source is
// `.bot roster`'s TBM:BOTSTATE rows, whose `specs=` field is the spec strategy kit the module installed on that bot
// (walk 101b aborted on the "rest" batch before this source existed). A boss row's unit read is retried rather than
// failed on one miss: the hire's row is re-read every 2 s for up to 30 s (prepullAuraUnitWait), because walk 105
// stopped a run 24 minutes in when the main tank, standing ahead of the commander, was unseen for two back-to-back
// reads. A hire still unseen after that wait takes the role the module's own roster names for it, without the class
// (prepullRosterRole, raidwalk.BotStateAuraRoleUnclassed), logged as "role from roster (unit unseen)"; the MT/OT
// assignment does not override that row, so an unseen MT with a non-tank kit is not handed the tank list. With no
// roster role either the fire fails closed with the hire and the wait named. The aura's own combat read and read-back
// still decide: an unseen unit is never assumed alive, out of combat or carrying its list.
//
// The fire is the boss row's own gate (prepullGate): the step before a boss row in WHATEVER phase, as long as the run
// is still before the pull (no pulling/fighting note, no later pull line of the page, no party bot in combat), or the
// row where the run passed that step too fast to pause — the boss row itself, or any step beyond it, as walk 113's
// one-tick 421-424 stretch left the pause for boss row 425 on step 426 — still before the pull (no member in combat
// and the boss's arena read fresh out of combat). A boss row that an object or an NPC summons — a use_object (an open or
// open_proof row) or a gossip row — fires on the step before that trigger instead, when only the trigger, waits and
// moves of the approach stand between it and the boss row (raidwalk.PrepullFirePoint), because the trigger starts the
// boss's script at once: walk 104's ZG gong 180526 summoned Arlokk into combat on her own row 346, and her boss row
// 348 found the raid already fighting (exit 18, "combat started"), while a row before the gong is still out of
// combat. Walk 101b: the ZG Thekal plan's step before boss row 255 is a plain move, so
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
//
// --prepull-aura-topup is bounded so it can never hold the poll loop past a boss fire (walk 2m's a4-1 abort: the
// top-up's pause hit its 15 s cap, the pause's ACK came back late after the walk's resume, and while the loop sat in
// the top-up the run passed step 38, the boss fire's row, which then came up unprepared). Three bounds, all in
// prepullAuraTopup: (B1) combat is read BEFORE the pause, so a pull that already started costs no GM line at all and
// no pause is asked for during a fight, with the recheck after the pause kept; (B2) a pull step whose budget is spent
// is capped — every later status of that step reads and sends nothing (prepullTopupSettled); (B3) one call's wall
// budget starts before its reads and bounds every send of it, whether the run is paused by the call, already paused
// or unheld. The read-back records what a bot carries instead of a missing id (`others`), and both the fire's
// tolerance line and the top-up's step line carry a per-id tally, so a per-bot tolerance miss is not misread as "N
// ids missing".
package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
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
	"github.com/pjerra/turtle-agent/internal/session"
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
	// twice"). A boss row never needs it: the raid has been in the view for minutes by then. The frame poll and the
	// unit-store poll after the join each get their own window (walk 105c: the commander outside the instance cannot
	// fill its store until the join).
	prepullStartWaitMax   = 60 * time.Second
	prepullStartStoreWait = 60 * time.Second
	prepullStartWaitPoll  = 2 * time.Second
	// The boss-row unit read wait (walk 105). A hire's unit row can be missing from the agent's own store for a few
	// seconds: at ZG Jeklik boss row 49 the main tank stood ahead of the commander, the two back-to-back unit reads
	// missed it and a run 24 minutes in stopped there, while the store held it seconds later. The role read re-tries
	// every 2 s for up to 30 s; a hire still unseen then takes the role the module's own roster names for it
	// (prepullRosterRole), never a guessed one. The run is paused while the fire waits, so the time is the walk's,
	// not the raid's.
	prepullUnitReadMax  = 30 * time.Second
	prepullUnitReadPoll = 2 * time.Second
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
// Others is what the read-back found the bot carrying INSTEAD of an id it lacks: the aura ids on the unit that are not
// in its list (sorted, at most dungeon.AurasOtherMax), nil when it misses nothing. A missing Well Fed id is usually
// this: the core's spell_group 1001 lets a target hold one food buff, and the bot already holds another (its own
// food), which TA cannot replace. Recording it says which aura it holds instead, so the lead can pick the preset's
// food or ask TortoiseBots.
type prepullAuraRecord struct {
	Bot     string   `json:"bot"`
	Role    string   `json:"role"`
	Specs   string   `json:"specs,omitempty"` // the module's spec strategies the role was read from
	Sent    []uint32 `json:"sent"`
	Seen    []uint32 `json:"seen"`
	Missing []uint32 `json:"missing"`
	Others  []uint32 `json:"others,omitempty"`
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

// prepullTarget is the gate's answer: the boss row a fire prepares, the row its fire stands before (the gate fires on
// the status of the row before fireRow), the trigger row the fire was moved in front of (0: it fires at the boss
// row's own step, or on the boss row itself), whether a fire may go out now, and whether the row's boss is already
// dead (no fire and no failure: the caller marks the row done and the run continues). bossRow 0: nothing to do, no
// failure.
type prepullTarget struct {
	bossRow int
	fireRow int
	trigger int
	kind    string
	fire    bool
	dead    bool
}

// prepullTargetFor is the boss row's firing plan: raidwalk.PrepullFirePoint's answer, which moves a boss row's fire
// back in front of the trigger (a use_object or gossip row) that summons and engages it, when only that trigger, waits
// and moves stand between them. A boss row with no such trigger keeps its own step.
func (w *walk) prepullTargetFor(bossRow int) prepullTarget {
	fireRow, trigger, kind := raidwalk.PrepullFirePoint(w.rows, bossRow)
	return prepullTarget{bossRow: bossRow, fireRow: fireRow, trigger: trigger, kind: kind}
}

// prepullBossRowFiredBy is the boss row whose fire stands on the status of the row before next: the boss row at next
// itself (a plain boss row), or the boss row a trigger at next summons, whose fire raidwalk.PrepullFirePoint moved in
// front of that trigger. 0: no boss row fires there.
func (w *walk) prepullBossRowFiredBy(next int) int {
	for i, s := range w.rows.Steps {
		if s.Kind != planexport.KindBoss {
			continue
		}
		if t := w.prepullTargetFor(i + 1); t.fireRow == next {
			return t.bossRow
		}
	}
	return 0
}

// prepullWakesBossDue is the boss row whose wakes-boss fire is due at a status on step: its fire stands on the row
// before the waking pull, and a run that jumped over that row fires on the FIRST status at or after it (the run then
// stands on the waking pull row or past it), up to the boss row itself. The wakes fire is due while the boss row is
// still ahead: past it the row's own status has already had its chance, and a fire there would prepare nothing.
// 0: no wakes-boss boss row is due.
func (w *walk) prepullWakesBossDue(step int) int {
	for i, s := range w.rows.Steps {
		if s.Kind != planexport.KindBoss {
			continue
		}
		t := w.prepullTargetFor(i + 1)
		if t.kind != raidwalk.PrepullWakesBossKind {
			continue
		}
		if step >= t.fireRow-1 && step <= t.bossRow {
			return t.bossRow
		}
	}
	return 0
}

// prepullWakesBossFires says the pull row is the waking pull row of a boss row whose fire owns it (in boss mode): the
// fire sends the full lists on the first status at or after the row before it, so no top-up duplicates that send.
func (w *walk) prepullWakesBossFires(step int) bool {
	for i, s := range w.rows.Steps {
		if s.Kind != planexport.KindBoss {
			continue
		}
		t := w.prepullTargetFor(i + 1)
		if t.kind == raidwalk.PrepullWakesBossKind && t.fireRow == step {
			return true
		}
	}
	return false
}

// prepullGate is the --prepull-aura firing gate. It returns the boss row to prepare and whether a fire may go out
// now (no boss row: nothing to do, and no failure). The step before a boss row fires in whatever phase, as long as the
// status is still before the pull (prePullOK: no pulling/fighting note, no later pull line of the same page, no party
// bot in combat in a recent frame). A status that is not before the pull defers to the boss row's own status, exactly
// as the rest/gather-only gate did. A boss row that is summoned by a trigger row (a use_object or a gossip) is fired
// on the status of the row before that trigger instead (prepullTargetFor): opening the object or taking the gossip
// starts the boss's script at once, so the boss row's own step is already inside the fight (walk 104: the ZG gong
// summoned Arlokk into combat three rows before her boss row). The trigger row itself never fires: its own status
// shows the script already running, and the moved fire carries a next==trigger row of its own. The boss row itself
// fires only while the pause can still land before the pull: the same pre-pull check AND the shared three-case
// verdict (prepullAuraVerdict) — a boss alive out of combat fires, a dead or already-killed boss is skipped with one
// line and the run continues, and an engaged or missing boss is the caller's blocking failure. The verdict's arena is
// read FRESH from the server (prepullBossArena) rather than from the cached hostile frames bossEngagedNow reads: those
// can still show the boss unengaged while the pull has started and the next view frame has not arrived. A boss row
// that becomes current otherwise is (bossRow, fire=false), the caller's blocking failure: the preparation the run was
// started to test never happened. A failed boss read is an error too (the gate never fires on a read it could not
// make); the caller stops the walk with it, and no aura goes out. A pull row whose comment wakes the boss (a plan's
// wakes_boss) is due on the first status at or after the row before it, whatever the page's later lines say; a run
// already on or past that pull is decided by the fresh arena (prepullAuraWakesVerdict), so a boss dead by then skips
// the row and an engaged one fails closed.
func (w *walk) prepullGate(ctx context.Context, st tbm.RunStatus, later bool) (prepullTarget, error) {
	if st.State != tbm.RunRunning && st.State != tbm.RunPaused {
		return prepullTarget{}, nil
	}
	if st.Kind == planexport.KindBoss && w.prepullWakesBossDue(st.Step) == 0 {
		if _, ok := w.bossRowAt(st.Step); !ok {
			return prepullTarget{}, nil
		}
		t := w.prepullTargetFor(st.Step)
		if w.prepullAuraSent[t.bossRow] {
			// The row was prepared already (normally in front of its trigger): its own status is no failure and needs
			// no read. The caller's sent check returns before it treats fire=false as one.
			return t, nil
		}
		if w.prepullAuraDead[t.bossRow] {
			// The row's boss was already dead when a pause overshot it or stood on it (prepullAuraOvershoot, the boss
			// row's own pause): its status is no failure either, and no second pause attempts a fire that has
			// nothing to prepare.
			return t, nil
		}
		if w.prepullBossKilled(t.bossRow) {
			// The walk's own boss record already holds this row's kill: no read and no pause can change that, so the
			// row is done without one.
			w.prepullAuraDeadLine(t.bossRow, "the boss row became current", "the walk's boss record holds the kill")
			t.dead = true
			return t, nil
		}
		if !w.prePullOK(st, later, w.env.now()) {
			return t, nil
		}
		c, err := w.prepullAuraVerdict(ctx, t.bossRow)
		if err != nil {
			return prepullTarget{}, err
		}
		if c.dead {
			w.prepullAuraDeadLine(t.bossRow, "the boss row became current", c.evidence)
			t.dead = true
			return t, nil
		}
		t.fire = c.fire
		return t, nil
	}
	if bossRow := w.prepullWakesBossDue(st.Step); bossRow != 0 {
		// A wakes-boss pull wakes the boss the moment its pack dies, so the party must already carry the boss row's
		// lists when the pull starts. The fire is due on the first status at or after the row before that pull: a
		// later pull line of the page is not a block (that line is the very pull this fire must beat), and a run that
		// jumped over the row fires where it stands, under the same fresh-arena verdict as the boss row's own
		// overshoot (a dead boss skips the row, an engaged one fails closed).
		if w.prepullAuraSent[bossRow] || w.prepullAuraDead[bossRow] {
			return prepullTarget{}, nil
		}
		t := w.prepullTargetFor(bossRow)
		if st.Step <= t.fireRow-1 {
			if !w.prePullOK(st, false, w.env.now()) {
				return prepullTarget{}, nil
			}
			t.fire = true
			return t, nil
		}
		c, err := w.prepullAuraWakesVerdict(ctx, bossRow)
		if err != nil {
			return prepullTarget{}, err
		}
		switch {
		case c.dead:
			w.prepullAuraDeadLine(bossRow, fmt.Sprintf("the run reached step %d first", st.Step), c.evidence)
			t.dead = true
			return t, nil
		case c.unclear():
			return prepullTarget{}, stopf(exitRaidWalkFailed,
				"prepull-aura: boss row %d: the run reached step %d, at or past the waking pull row (step %d), %s: no auras were sent",
				bossRow, st.Step, t.fireRow, c.why())
		}
		t.fire = true
		return t, nil
	}
	bossRow := w.prepullBossRowFiredBy(st.Step + 1)
	if bossRow == 0 {
		return prepullTarget{}, nil
	}
	if w.prepullAuraDead[bossRow] {
		// The row's boss was already dead when a pause overshot it or stood on it: a later status of the row before
		// its trigger fires nothing either.
		return prepullTarget{}, nil
	}
	if !w.prePullOK(st, later, w.env.now()) {
		// Not before the pull yet, or the run passed this row too fast: nothing goes out now, and a later status of
		// the same page may still fire it. Only the boss row's own status is a failure when it was never prepared.
		return prepullTarget{}, nil
	}
	t := w.prepullTargetFor(bossRow)
	t.fire = true
	return t, nil
}

// prepullAuraSendPending is pollOnce's firing: at the step before a boss row (whatever its phase), before the trigger
// row that summons the boss when there is one (prepullTarget), or where a paused run stands when it passed that step
// too fast to pause (the boss row itself or beyond it), once per boss row, the auras go out under a run pause that
// must hold. A row whose boss is already dead (the shared verdict: a corpse in the fresh arena read, or the walk's
// record of the kill) sends nothing and never pauses: the gate writes one line and the run continues, and a pause that
// finds it dead on the row or past it does the same. A boss row that became current without a verified fire otherwise
// fails the walk (a configured boss row the walk never prepared is not the test the run was started for). A boss row
// whose arena read fails is the gate's error and stops the walk too. When the fire went out before a trigger, one line
// names the boss row and the trigger ("fired before trigger step M (open)"); a boss row already prepared that way
// never fires again on its own row. Whenever the status was running before the pause attempt, the resume is attempted
// afterwards even when the hold or the fire failed: a run left paused stops the raid silently. A status that was
// already paused sends no resume. A row is recorded as done only after the auras verified within the tolerance. It
// reports whether a firing (or the hold of one) happened, so pollOnce breaks its line loop (the next poll re-reads
// from the cursor), like the instance-data firing. Under --prepull-aura-when start nothing is sent here: the fire
// already went out before the run's first move, and each boss row only gets a read of the auras the raid still
// carries (prepullAuraPullRead).
func (w *walk) prepullAuraSendPending(ctx context.Context, st tbm.RunStatus, later bool) (bool, error) {
	if len(w.prepullAuras) == 0 {
		return false, nil
	}
	// --prepull-aura-topup stands before every pull row, boss rows included (a boss pull's hires are read there, which
	// under --prepull-aura-when start is the only read they get): it pauses the run only when it has something to send,
	// and both the call and the pull step are bounded at 15 s (prepullAuraTopup B1-B3), so it cannot hold this loop
	// past a boss fire. It does not fire this line loop (nothing of the run's state changes, so the page's other lines
	// are still worth handling), and the only failure of it that reaches the walk is its resume's, failed twice (the
	// run may then be paused). A boss row the boss-row fire owns is the fire's business, not the top-up's
	// (prepullTopupBossFires).
	if step := w.prepullTopupStep(st, later); step != 0 {
		// The top-up stops the walk for one reason only: its resume failed twice and the run may be paused (a run left
		// paused is a dead raid, as it is for the boss-row fire). Every other failure of it is a line.
		if err := w.prepullAuraTopup(ctx, st, step); err != nil {
			return false, err
		}
	}
	if w.opts.prepullAuraWhen == prepullAuraWhenStart {
		// --prepull-aura-when start fired once before the run's first move: a boss row is never a place to send
		// again. The pull gets the coverage read instead (prepullAuraPullRead), which sends nothing and so never
		// pauses the run, and the once-per-row guard keeps it to one line per boss.
		return false, w.prepullAuraPullRead(ctx, st, later)
	}
	t, err := w.prepullGate(ctx, st, later)
	if err != nil {
		return false, err
	}
	if t.bossRow == 0 {
		return false, nil
	}
	if w.prepullAuraSent[t.bossRow] {
		return false, nil
	}
	if w.prepullAuraDead[t.bossRow] {
		return false, nil
	}
	if t.dead {
		// The row's boss is already dead (the gate's verdict, prepullAuraVerdict): no fire and no failure, the row
		// is done and the run continues.
		w.prepullAuraMarkDead(t.bossRow)
		return false, nil
	}
	if !t.fire {
		return false, stopf(exitRaidWalkFailed,
			"prepull-aura: boss row %d became current with no verified pre-pull application: the auras were never sent", t.bossRow)
	}
	pauseAttempted := st.State == tbm.RunRunning
	goOut, fireErr := w.prepullAuraHold(ctx, t.bossRow, t.fireRow, pauseAttempted,
		t.kind == raidwalk.PrepullWakesBossKind)
	if fireErr == nil && goOut {
		fireErr = w.prepullAuraFire(ctx, t.bossRow, false)
		if fireErr == nil {
			if w.prepullAuraSent == nil {
				w.prepullAuraSent = map[int]bool{}
			}
			w.prepullAuraSent[t.bossRow] = true
			if t.trigger != 0 {
				w.logLine(w.env.now(), fmt.Sprintf("prepull-aura: boss row %d: fired before trigger step %d (%s)",
					t.bossRow, t.trigger, t.kind))
			}
		}
	}
	if fireErr == nil && !goOut {
		// The row's own pause (or the overshot one) found the boss already dead (prepullAuraVerdict): the row is done
		// with no fire, and its own later statuses are no failure. The run is resumed like any other held pause.
		w.prepullAuraMarkDead(t.bossRow)
	}
	if pauseAttempted {
		if _, err := w.commander(ctx, commanderReq{Op: "run", Verb: "resume", WaitMS: 10000}); err != nil {
			if fireErr != nil {
				w.note("prepull-aura: WARN: the run resume after boss row %d also failed (%v)", t.bossRow, err)
				return true, fireErr
			}
			return true, stopf(exitRaidWalkFailed, "prepull-aura: boss row %d: the run resume after the auras failed: %v", t.bossRow, err)
		}
	}
	return true, fireErr
}

// prepullTopupStep is --prepull-aura-topup's gate: the pull row a status stands before. The status's OWN row comes
// first: a pull row whose pull has not started is the pull this status stands before, and the row after it is a later
// pull. Only a row that is not itself a pull row lets the gate look one ahead, at the pull the rest, gather, walk or
// approach leads to — and a status on that pull row itself (the run passed the rest too fast) then still names it. A
// boss row is a pull row here too (the boss pull): its statuses before the pull get the top-up, which is the only thing
// that reads the hires there under --prepull-aura-when start. In boss mode the boss row's own fire owns it
// (prepullTopupBossFires), so the top-up stays out of that row. prePullOK keeps the top-up out of the pull and the
// fight (phase pull/fight, a pulling or fighting note, a later pull line of the page, a party bot in combat in a fresh
// frame); 0 means no pull row to top up before.
//
// Reading the LATER pull from a status on the earlier one is what this row order fixes: adjacent pull rows (a trash
// pull and the next pull, or a boss row and the next pull) had the gate pick st.Step+1 before st.Step, so a pre-pull
// status on the current row recorded the NEXT pull as done — and a bot that died in the current fight was never
// re-buffed before that next pull (test: TestPrepullAuraTopupReadsTheSecondOfTwoAdjacentPulls). Completion is per
// (step, hire) (prepullTopupDone), so the next pull is read again after the current fight.
func (w *walk) prepullTopupStep(st tbm.RunStatus, later bool) int {
	if !w.opts.prepullAuraTopup || len(w.prepullAuras) == 0 {
		return 0
	}
	if st.State != tbm.RunRunning && st.State != tbm.RunPaused {
		return 0
	}
	if !w.prePullOK(st, later, w.env.now()) {
		return 0
	}
	step := 0
	if w.prepullPullRow(w.prepullRowAt(st.Step)) {
		step = st.Step
	} else if w.prepullPullRow(w.prepullRowAt(st.Step + 1)) {
		step = st.Step + 1
	}
	if step == 0 || w.prepullTopupBossFires(st, step) {
		return 0
	}
	return step
}

// prepullPullRow says the row is a pull the top-up stands before: a pull row, or a boss row, whose own pull is its
// row's pull too.
func (w *walk) prepullPullRow(row planexport.StepRow) bool {
	return row.Kind == planexport.KindPull || row.Kind == planexport.KindBoss
}

// prepullTopupBossFires says the boss-row fire owns this pull row's auras. In boss mode (the default) the fire prepares
// every boss row with the full lists and is fail-closed — on the step before the row, at its summoning trigger, or on
// the row itself — so a top-up there would send the same ids a second time: it is skipped whether the fire is still to
// come for that row or already went out (and likewise on a pull row the fire stands on in this same status, a waking
// pull row included). Under --prepull-aura-when start no boss row is ever fired (the lists went out once before the
// run's first move), so the top-up is what reads and tops up the hires at the boss pull.
func (w *walk) prepullTopupBossFires(st tbm.RunStatus, step int) bool {
	if w.opts.prepullAuraWhen == prepullAuraWhenStart {
		return false
	}
	if _, ok := w.bossRowAt(step); ok {
		return true
	}
	if w.prepullWakesBossFires(step) {
		// A waking pull row belongs to the boss fire in both of its due positions: the row before it, and the pull row
		// itself when the run jumped over that row. The fire sends the full lists, so the top-up stays out of it.
		return true
	}
	return step == st.Step+1 && w.prepullBossRowFiredBy(st.Step+1) != 0
}

// prepullRowAt is the plan row at step (1-based), the zero row for a step the plan does not have.
func (w *walk) prepullRowAt(step int) planexport.StepRow {
	if step < 1 || step > len(w.rows.Steps) {
		return planexport.StepRow{}
	}
	return w.rows.Steps[step-1]
}

// prepullTopupHire is one hire's place in one pull step's top-up: read is set once its auras were read at that step
// and the ids it lacked were put back and read back (or nothing was lacking, which is a complete read too), tries counts
// the send attempts of that step and said is set once the walk wrote the reason it was left out, so later statuses of
// the same step retry it without repeating the line. A hire whose read failed, that is dead or that is out of the
// commander's view stays pending. Once tries reaches prepullTopupTryMax with an id still missing (an aura that will not
// stick, ticket T32: 23768 misses on one bot about 1 in 3 fires) the hire is done for the step too: the walk does not
// resend to it on every later pre-pull status.
type prepullTopupHire struct {
	read  bool
	tries int
	said  bool
}

// prepullTopupTryMax is how many times one pull step sends to the same hire. An id the server keeps refusing is the
// bot's loss for that pull, not a reason to repeat the whole batch on every status before it.
const prepullTopupTryMax = 3

// prepullTopupMark is the hire's place in this pull step's top-up, created on first use. Keyed case-insensitively:
// the frame's spelling may differ from the roster's.
func (w *walk) prepullTopupMark(pullStep int, name string) *prepullTopupHire {
	if w.prepullTopupDone == nil {
		w.prepullTopupDone = map[int]map[string]*prepullTopupHire{}
	}
	byStep := w.prepullTopupDone[pullStep]
	if byStep == nil {
		byStep = map[string]*prepullTopupHire{}
		w.prepullTopupDone[pullStep] = byStep
	}
	key := strings.ToLower(name)
	h := byStep[key]
	if h == nil {
		h = &prepullTopupHire{}
		byStep[key] = h
	}
	return h
}

// prepullTopupSettled reports whether every hire of the roster is done for this pull step's top-up: only then does a
// later status of the same step read nothing at all. A step whose 15 s budget is spent counts as done (B2): every later
// status of it reads nothing and sends nothing, because the poll loop must not sit in the top-up past a boss fire.
func (w *walk) prepullTopupSettled(pullStep int, roster []string) bool {
	if w.prepullTopupCapped[pullStep] {
		return true
	}
	for _, name := range roster {
		if h := w.prepullTopupDone[pullStep][strings.ToLower(name)]; h == nil || !h.read {
			return false
		}
	}
	return true
}

// prepullAuraTopupSaid logs the reason a hire is left out of this pull step's top-up, once per hire per step: the
// later statuses of the same step retry that hire without repeating its line.
func (w *walk) prepullAuraTopupSaid(pullStep int, name, format string, a ...any) {
	mark := w.prepullTopupMark(pullStep, name)
	if mark.said {
		return
	}
	mark.said = true
	w.prepullAuraTopupLine(format, a...)
}

// prepullAuraTopupOnce logs one line per pull step for key: a step's stop line names the same raid state on every
// later status of the step (combat, an unheld top-up), so the second line would only repeat the first.
func (w *walk) prepullAuraTopupOnce(pullStep int, key, format string, a ...any) {
	if w.prepullTopupSaid[pullStep][key] {
		return
	}
	if w.prepullTopupSaid == nil {
		w.prepullTopupSaid = map[int]map[string]bool{}
	}
	if w.prepullTopupSaid[pullStep] == nil {
		w.prepullTopupSaid[pullStep] = map[string]bool{}
	}
	w.prepullTopupSaid[pullStep][key] = true
	w.prepullAuraTopupLine(format, a...)
}

// prepullAuraTopupUnheldOnce logs that pullStep's top-up is going out without a run pause, once per step.
func (w *walk) prepullAuraTopupUnheldOnce(pullStep int, format string, a ...any) {
	w.prepullAuraTopupOnce(pullStep, "unheld", format, a...)
}

// prepullTopupCapStep marks pullStep's top-up capped and names it once: its 15 s budget is spent (the run pause's, or
// the whole call's wall budget when the pause was never held), so no later status of the step reads or sends anything
// (B2), and the poll loop is free for the run's own lines. It reports whether this call is the one that capped it.
func (w *walk) prepullTopupCapStep(pullStep int, where string) bool {
	if w.prepullTopupCapped[pullStep] {
		return false
	}
	if w.prepullTopupCapped == nil {
		w.prepullTopupCapped = map[int]bool{}
	}
	w.prepullTopupCapped[pullStep] = true
	w.prepullAuraTopupLine("prepull-aura: %s: the 15s pause budget is spent; no further top-up before this pull", where)
	return true
}

// prepullTopupPauseCap is the top-up's 15 s budget, and it is one budget of two readings (B2/B3): a run pause it holds
// is resumed when it is spent, and one top-up CALL stops sending when it is spent since the call started, before the
// reads, whether the run was held, already paused or never held. Either way the pull step is marked capped, so every
// later status of it reads and sends nothing.
const prepullTopupPauseCap = 15 * time.Second

// prepullTopupReadBackMin is the shortest real-time window a capped call's read-back gets when the budget has nothing
// left: no poll, one read per sent hire, but each read still has to answer. It is a var so a test can make the window
// expire inside the pass.
var prepullTopupReadBackMin = time.Second

// errTopupPauseCap stops ApplyAuras when a top-up's 15 s budget is spent mid-batch.
var errTopupPauseCap = errors.New("prepull-aura: top-up 15s budget spent")

// prepullTopupCtx derives the context one top-up call's RPCs are made with: a real-time bound of the budget left on the
// walk's clock, the clock the deadline was taken on (a fake clock must not become a wall deadline). The budget is the
// context's CAUSE, so prepullTopupCut can tell the top-up's own end from the walk's (a cancelled parent): a call with no
// budget left starts on a cut context, so not one RPC of it can wait.
func (w *walk) prepullTopupCtx(ctx context.Context, deadline time.Time) (context.Context, context.CancelFunc) {
	left := deadline.Sub(w.env.now())
	if left <= 0 {
		tctx, cancel := context.WithCancelCause(ctx)
		cancel(errTopupPauseCap)
		return tctx, func() {}
	}
	return context.WithDeadlineCause(ctx, time.Now().Add(left), errTopupPauseCap)
}

// prepullTopupCut reports errTopupPauseCap when the call's budget ended: its context is done for the budget's own cause,
// or the walk's clock reached the deadline the call took at entry. A read that came back empty then was cut by the
// budget, not answered by a broken store, and the step is capped instead of stopped. A context that is done because the
// WALK ended (its parent was cancelled) is not a budget: the parent's own error is returned, and the caller leaves
// without a budget line, a read-back or a record. Outside a top-up (no deadline) it never reports a cut.
func (w *walk) prepullTopupCut(ctx context.Context) error {
	deadline := w.prepullTopupSendDeadline
	if deadline.IsZero() {
		return nil
	}
	if err := ctx.Err(); err != nil {
		if errors.Is(context.Cause(ctx), errTopupPauseCap) {
			return errTopupPauseCap
		}
		return err
	}
	if !w.env.now().Before(deadline) {
		return errTopupPauseCap
	}
	return nil
}

// prepullTopupMarkSeen records the spell ids observed on name in this run.
func (w *walk) prepullTopupMarkSeen(name string, ids []uint32) {
	if len(ids) == 0 {
		return
	}
	if w.prepullTopupSeen == nil {
		w.prepullTopupSeen = map[string]map[uint32]bool{}
	}
	key := strings.ToLower(name)
	seen := w.prepullTopupSeen[key]
	if seen == nil {
		seen = map[uint32]bool{}
		w.prepullTopupSeen[key] = seen
	}
	for _, id := range ids {
		seen[id] = true
	}
}

// prepullTopupEverSeen reports whether id was ever observed on name in this run.
func (w *walk) prepullTopupEverSeen(name string, id uint32) bool {
	return w.prepullTopupSeen[strings.ToLower(name)][id]
}

// prepullTopupCause names why name is missing its auras: "death" when it died since its last top-up, "expired" when
// every missing id was seen on it before in this run, "never had" when none of the missing ids were ever seen on it,
// and "expired, never had" when the missing ids mix both.
func (w *walk) prepullTopupCause(name string, missing []dungeon.Buff) string {
	key := strings.ToLower(name)
	if w.prepullTopupDied[key] {
		return "death"
	}
	seenCount := 0
	for _, b := range missing {
		if w.prepullTopupEverSeen(name, b.ID) {
			seenCount++
		}
	}
	switch seenCount {
	case 0:
		return "never had"
	case len(missing):
		return "expired"
	default:
		return "expired, never had"
	}
}

// prepullAuraTopup is --prepull-aura-topup: before pull row pullStep, out of combat, it reads every living hire's
// auras and puts back only the ids its role's list is missing, whether a death stripped them, an aura expired, or the
// hire never had it. It reuses the fire's path (prepullRaid, the commander join when the store cannot see the raid, the
// per-bot role from the module's state rows, the combat recheck before every send, the `.aura` kernel and the
// read-back):
//
//   - only the bots that are missing something are sent to, and only their missing ids: a bot that carries its list
//     costs no GM line and no run pause;
//   - combat is read BEFORE anything else (B1): an in-combat raid gets no pause and no send, because a pull that has
//     started must cost no GM line and the pause's own round trip is where a4-1 lost its ACK. The recheck after the
//     pause stays: a pull can start during it;
//   - the call is bounded (B3): ONE budget is taken at entry, before the raid read, and every RPC of the call is made
//     with it and with the time left as its own wait (the pause's wait and the resume's included), so no read can spend
//     it unseen and no RPC can outlast it. Past prepullTopupPauseCap no send of it goes out — the run held by it,
//     "already paused", or unheld alike — and a read the budget cut caps the step instead of reading as a failure. A
//     resume is still always sent once a pause was SENT, whatever its answer, but with its own short context
//     (min(10 s, max(2 s, time left)): a resume that blocks past the deadline caps the call too, so it is the last RPC
//     of it, not one that extends it; a walk cancelled around the pause resumes on a fresh 2 s window instead. A
//     pull step whose budget is spent is capped (B2): prepullTopupSettled answers true for it, so every later status of
//     that pull reads nothing and sends nothing, and one line says so. That is what keeps the poll loop free for the
//     run's own lines (a4-1: the loop sat in the top-up while the run passed the boss fire's row); a capped call also
//     waits for nothing after its last send — one immediate read-back names what is short, the 5 s verify poll is
//     skipped, and a hire a cut batch left half-done is read back and tallied too. A call that is not capped verifies
//     for at most the time its budget has left, and the verify window's own end (a context deadline, or the walk's
//     clock) never skips the record: the tally line and prepull-auras.json are always written. A hire the window ended
//     INSIDE its read is unread, not a hire missing its whole list (its record keeps the earlier read, and it does not
//     count toward the give-up). A walk that was itself cancelled is the one exception: it writes no budget line, takes
//     no read-back and leaves no record, and only the resume goes out;
//   - nothing here fails the walk except the run's own resume. A raid read, a join, a role or a unit read, a send and
//     a read-back that does not land are a run.log line, never a stop: a missed top-up is the run the walk was already
//     going to have, while the boss-row fire stays fail-closed. The resume is the exception because the run's state is
//     not the top-up's to leave broken: it is tried twice in its one window, and a failure that outlives the retry
//     stops the walk (exit 18, "prepull-aura: topup: resume failed twice; the run may be paused"), exactly as the
//     boss-row fire does when its own resume fails.
//
// One run.log line names the pull step and every upgraded bot with the number of ids put back and the cause: "death"
// when the frames saw that hire die since its last top-up, "expired" when the missing ids were seen on it before in
// this run, and "never had" when they were never seen on it. It is written after the read-back and ends with the
// per-id tally ("; missing by id: 22730 x7, 18192 x5"). Completion is tracked PER HIRE for the pull step
// (prepullTopupDone): a hire is done only after its auras were read and the ids it lacked were sent and read back, and
// a dead, unseen or unreadable one stays retryable on the next pre-pull status of the same step, at most one read of
// that hire per status. A hire that still lacks an id after prepullTopupTryMax sends is done for the step too, with one
// line naming the ids: an aura the server will not stick (ticket T32) is not resent on every status before the pull.
// The step is done when every hire of the roster is done, and only then does a later status of the same step read
// nothing. Nothing is carried to the next pull step: an aura that was present at one pull can expire seconds later, so
// only a read at the pull itself says what the hires carry.
func (w *walk) prepullAuraTopup(ctx context.Context, runSt tbm.RunStatus, pullStep int) (stopErr error) {
	roster := w.hireList()
	if w.prepullTopupSettled(pullStep, roster) {
		return
	}
	where := fmt.Sprintf("topup before step %d", pullStep)
	// B3: ONE budget for the whole call, taken before its first read and applied to every RPC of it (the raid read, the
	// commander join, the per-bot roles, the status, every hire's aura read, the combat rechecks, the pause and the
	// sends), so no read can spend it unseen: the poll loop stays free for the run's own lines long enough that a boss
	// row cannot come up while the top-up still reads or writes. Past it this call sends nothing, held, "already paused"
	// or unheld, and the step is capped.
	sendDeadline := w.env.now().Add(prepullTopupPauseCap)
	tctx, cancel := w.prepullTopupCtx(ctx, sendDeadline)
	defer cancel()
	w.prepullTopupSendDeadline = sendDeadline
	defer func() { w.prepullTopupSendDeadline = time.Time{} }()
	// pauseTried is set BEFORE the pause RPC goes out, not after its ACK: a cut pause may have reached the run (the
	// `.bot run pause` line was sent, and the budget cut the wait for its answer), and so may a pause whose answer is an
	// error, a refusal or no ACK. The resume is deferred right there, before the RPC, so every exit after an attempted
	// pause sends it exactly once: a run left paused is worse than a resume against a run that never paused — the resume
	// is idempotent. The boss-row fire resumes the same way (prepullAuraSendPending).
	pauseTried := false
	resumed := false
	// resumeStop is the walk's stop when the resume failed twice: it is carried out by the function's own return (the
	// call has no other stop), so the deferred resume below can set it too.
	var resumeStop error
	// resume puts the run back whatever ended the call. It is still sent once whenever a pause was attempted — an
	// expired context would leave the raid paused silently — but with its own short context (B3): the flat 10 s is one
	// RPC that can outlast the call's budget, and a resume that blocks past it is exactly what held the poll loop after
	// the deadline. min(10 s, max(2 s, time left)) keeps the smallest window a slow ACK can land in. It is idempotent,
	// so the cap, a stop and the function's own return all resume exactly once, and it is tried twice inside that one
	// window: a failure that outlives the retry sets resumeStop, and the walk stops rather than run paused.
	resume := func() {
		if !pauseTried || resumed {
			return
		}
		resumed = true
		wait := 10 * time.Second
		if left := sendDeadline.Sub(w.env.now()); left < wait {
			wait = max(left, 2*time.Second)
		}
		// The walk's own cancellation ends ctx too, and a resume on a done context never goes out: the run is put back
		// on a fresh 2 s window instead (prepullTopupCut tells the walk's end from the budget's), because a run left
		// paused is worse than a resume nobody waited for.
		base := ctx
		if ctx.Err() != nil {
			base, wait = context.WithoutCancel(ctx), 2*time.Second
		}
		rctx, rcancel := context.WithTimeout(base, wait)
		defer rcancel()
		// A resume that errored or was refused is retried ONCE in its own window, the floor included: an answer lost on
		// the way leaves the run paused until the next boss row fires, and the pull it was paused for runs on a raid
		// nobody put back. The retry shares the one window (a second one would double the time the resume may hold the
		// call), so a resume that spent it fails the retry too, and THAT is the failure a WARN cannot cover: the walk
		// stops, as it does when the boss-row fire cannot resume.
		var said string
		for try := range 2 {
			rep, err := w.commander(rctx, commanderReq{Op: "run", Verb: "resume", WaitMS: int(wait / time.Millisecond)})
			switch {
			case err != nil:
				said = fmt.Sprintf("failed: %v", err)
			case rep.Reply.Err != nil:
				said = fmt.Sprintf("was refused: %s", rep.Reply.Err.Message)
			default:
				if try > 0 {
					w.prepullAuraTopupLine("prepull-aura: WARN: %s: the run resume after the top-up %s; the retry put the run back", where, said)
				}
				return
			}
			if try == 0 {
				w.prepullAuraTopupLine("prepull-aura: WARN: %s: the run resume after the top-up %s; retrying once", where, said)
			}
		}
		resumeStop = stopf(exitRaidWalkFailed, "prepull-aura: topup: resume failed twice; the run may be paused")
	}
	capped := false
	// capStep spends the step's budget: resume first (a run left paused stops the raid silently), then name the cap
	// once and stop sending, whether the run was held by this call, already paused or unheld (B2/B3). A capped call
	// also waits for nothing afterwards: its read-back is one immediate read (the verify loop below).
	capStep := func() {
		capped = true
		w.prepullTopupCapStep(pullStep, where)
		resume()
	}
	// ended closes the call for a read its context ended: the top-up's own budget caps the step (the budget line), and
	// the walk's own cancellation (the parent's error, see prepullTopupCut) leaves silently — no budget line, no
	// read-back, no record — the deferred resume still puts the run back.
	ended := func() bool {
		cut := w.prepullTopupCut(tctx)
		if cut == nil {
			return false
		}
		if errors.Is(cut, errTopupPauseCap) {
			capStep()
		}
		return true
	}
	// stop ends the call: a read the budget cut is the step's cap (the budget line), never a read or combat failure; any
	// other failure keeps its own stop line.
	stop := func(err error) {
		if ended() {
			return
		}
		w.prepullAuraTopupStop(where, err)
	}
	raid, err := w.prepullRaid(tctx)
	if err != nil {
		stop(err)
		return
	}
	hired := make(map[string]string, len(roster))
	for _, name := range roster {
		hired[strings.ToLower(name)] = name
	}
	// Only a hire this step has not read yet can need the commander in the raid, so a step whose remaining hires are
	// dead bodies or absent from the frame costs no join and no unit read: their revival or arrival at a later status
	// of the same step retries them.
	pending := false
	for _, m := range raid {
		if m.GUID == 0 || m.Name == "" || m.Dead || m.State == "d" {
			continue
		}
		if name, ok := hired[strings.ToLower(m.Name)]; ok && !w.prepullTopupMark(pullStep, name).read {
			pending = true
			break
		}
	}
	if !pending {
		return
	}
	// The unit store only holds what the commander can see (walk 105c, 105, 104): join the raid before reading it.
	if err := w.prepullAuraJoinRaid(tctx, raid, where); err != nil {
		stop(err)
		return
	}
	// The roles come from the module's own state rows, read once per walk.
	if _, err := w.prepullBotSpecs(tctx); err != nil {
		stop(stopf(exitRaidWalkFailed, "prepull-aura: %s: the per-bot roles could not be read: %v", where, err))
		return
	}
	st, err := w.status(tctx)
	if err != nil {
		stop(stopf(exitRaidWalkFailed, "prepull-aura: %s: the commander's status: %v", where, err))
		return
	}
	if st.Character == "" || st.GUID == 0 {
		stop(stopf(exitRaidWalkFailed,
			"prepull-aura: %s: the status names no commander (character %q, guid %s), so its combat state cannot be read", where, st.Character, st.GUID))
		return
	}
	port := walkAuras{w: w}
	watch := []dungeon.AuraTarget{{GUID: st.GUID, Name: st.Character}}
	type topupTarget struct {
		rec     *prepullAuraRecord
		unit    dungeon.AuraTarget
		want    []dungeon.Buff
		missing []dungeon.Buff
		cause   string
	}
	var targets []topupTarget
	inView := make(map[string]bool, len(roster))
	for _, m := range raid {
		if m.GUID == 0 || m.GUID == st.GUID || m.Name == "" {
			continue
		}
		name, ok := hired[strings.ToLower(m.Name)]
		unit := dungeon.AuraTarget{GUID: m.GUID, Name: m.Name}
		if ok {
			unit.Name = name
			inView[name] = true
		}
		if m.Dead || m.State == "d" {
			// Never for a dead bot: it cannot carry an aura, and its revival is its own step. The hire stays pending:
			// a status that sees it alive before the same pull reads it then.
			continue
		}
		if !ok {
			// A frame member the walk never hired is watched for combat, never buffed (as in the fire).
			watch = append(watch, unit)
			continue
		}
		watch = append(watch, unit)
		mark := w.prepullTopupMark(pullStep, name)
		if mark.read {
			// Read at this pull step already: it stays in the combat watch and costs no read.
			continue
		}
		u, seen := port.ViewUnit(tctx, unit)
		if ended() {
			return
		}
		if seen && (u.Dead || u.Ghost) {
			continue
		}
		var role, why string
		switch {
		case seen && u.Class == 0:
			w.prepullAuraTopupSaid(pullStep, name, "prepull-aura: %s: %s: its unit row carries no class, so its aura role is unknown: skipped", where, name)
			continue
		case seen:
			role, why = w.prepullRole(name, u.Class)
		default:
			role, why = w.prepullRosterRole(name)
		}
		if role == "" {
			w.prepullAuraTopupSaid(pullStep, name, "prepull-aura: %s: %s: %s: skipped", where, name, why)
			continue
		}
		want := w.prepullAuras.For(role)
		if len(want) == 0 {
			w.prepullAuraTopupSaid(pullStep, name, "prepull-aura: %s: %s has no list (role %q): skipped", where, name, role)
			continue
		}
		if !seen {
			// The roster named the role, but only the unit row carries the auras: no read, no top-up.
			w.prepullAuraTopupSaid(pullStep, name, "prepull-aura: %s: %s: the unit read did not answer, so its auras cannot be read: skipped", where, name)
			continue
		}
		missing, ok := dungeon.AurasMissing(tctx, port, unit, want)
		if ended() {
			return
		}
		if !ok {
			w.prepullAuraTopupSaid(pullStep, name, "prepull-aura: %s: %s: the unit read did not answer, so its auras cannot be read: skipped", where, name)
			continue
		}
		w.prepullTopupMarkSeen(name, subtractIDs(raidwalk.PrepullIDs(want), raidwalk.PrepullIDs(missing)))
		if len(missing) == 0 {
			mark.read = true
			delete(w.prepullTopupDied, strings.ToLower(name))
			continue
		}
		cause := w.prepullTopupCause(name, missing)
		rec := w.prepullAuraRecordFor(name, role)
		rec.Specs = w.prepullAuraSpecs[strings.ToLower(name)]
		targets = append(targets, topupTarget{rec: rec, unit: unit, want: want, missing: missing, cause: cause})
	}
	for _, name := range roster {
		if !inView[name] {
			w.prepullAuraTopupSaid(pullStep, name, "prepull-aura: %s: %s is not in the commander's view: skipped", where, name)
		}
	}
	if len(targets) == 0 {
		return
	}
	// B3: the call's budget, spent by the reads: nothing goes out and the step is capped, so the run's poll loop is not
	// held by a top-up that can no longer finish before the pull.
	if !w.env.now().Before(sendDeadline) {
		capStep()
		return
	}
	// The watch and the send kernel's own combat recheck: the commander and every living member, list or no list. Each
	// check is the one immediate read.
	w.prepullAuraWatch, w.prepullAuraWhat = watch, where
	w.prepullAuraCombatWaitEnd = w.env.now()
	defer func() {
		w.prepullAuraWatch, w.prepullAuraWhat = nil, ""
		w.prepullAuraCombatWaitEnd = time.Time{}
	}()
	// clearCombat is one combat recheck of the batch, bounded by the call's budget, and says whether the sends may go
	// on. A read the budget cut caps the step with the send loop's own line: the raid is not known to fight, so the
	// combat stop line would name a cause the walk never saw.
	clearCombat := func() bool {
		err := w.prepullAuraCombatClear(tctx, where)
		if err == nil {
			return true
		}
		if ctx.Err() != nil {
			// The walk itself ended: the read proves nothing, but this is not the top-up's budget either, so neither
			// the budget line nor a stop line is written.
			return false
		}
		if errors.Is(err, errTopupPauseCap) || w.prepullTopupCut(tctx) != nil {
			capStep()
			return false
		}
		w.prepullAuraTopupStopOnce(pullStep, "combat", where, err)
		return false
	}
	// B1: combat is read BEFORE the pause. A pull that already started must cost no GM line at all, and a pause the
	// walk asks for while the raid fights is exactly the late ACK that left a4-1's run paused at the wrong moment: the
	// top-up then sent auras under no pause of its own while the run went on into the boss row.
	if !clearCombat() {
		return
	}
	if runSt.State == tbm.RunRunning {
		// The pause is asked for with the time the budget has left: the flat 10 s wait could outlast the budget on its
		// own, and the poll loop would sit in it.
		pauseWait := 10000
		if left := sendDeadline.Sub(w.env.now()); left < time.Duration(pauseWait)*time.Millisecond {
			pauseWait = max(int(left/time.Millisecond), 1)
		}
		// From here the run may be paused whatever this call returns: a pause cut by the budget sent its line, and its
		// answer never came, and an error or a refusal may still mean the command reached the run. See pauseTried. So
		// every exit from here resumes, whatever the answer says: a resume against a running run is a no-op, a run left
		// paused is a dead raid. The explicit resume() after the sends still runs first, so the run is unpaused before
		// the read-back. A resume that fails twice is the ONE exit here that is not a WARN: the walk stops with
		// resumeStop, whatever else this call decided.
		pauseTried = true
		defer func() {
			resume()
			if resumeStop != nil {
				stopErr = resumeStop
			}
		}()
		if rep, err := w.commander(tctx, commanderReq{Op: "run", Verb: "pause", WaitMS: pauseWait}); err != nil {
			if ended() {
				return
			}
			w.prepullAuraTopupUnheldOnce(pullStep, "prepull-aura: WARN: %s: the run pause failed (%v): the top-up goes out unheld", where, err)
		} else if rep.Reply.Err != nil {
			w.prepullAuraTopupUnheldOnce(pullStep, "prepull-aura: WARN: %s: the run pause was refused (%s): the top-up goes out unheld", where, rep.Reply.Err.Message)
		} else if !rep.Reply.OK && rep.Reply.Kind != tbm.KindAck {
			w.prepullAuraTopupUnheldOnce(pullStep, "prepull-aura: WARN: %s: the run pause was not acknowledged (%s): the top-up goes out unheld", where, rep.Reply.Line)
		} else {
			w.logLine(w.env.now(), fmt.Sprintf("> .bot run pause (prepull-aura, %s)", where))
		}
	}
	// The combat recheck after the pause too: the pause's own round trip is long enough for a pull to start.
	if !clearCombat() {
		return
	}
	cfg := dungeon.AuraCfg{Gap: prepullAuraGap, Settle: prepullAuraSettle, ViewWait: prepullAuraViewWait}
	var parts []string
	var sent []int
	for i := range targets {
		if !w.env.now().Before(sendDeadline) {
			capStep()
			break
		}
		tg := &targets[i]
		// The attempt is counted before the send: a batch that combat stops mid-list leaves this hire's tries at the
		// attempt it did not finish, so a later status of the step still has the budget to put the rest back.
		w.prepullTopupMark(pullStep, tg.rec.Bot).tries++
		// What the read-back and the tally follow is whether this hire got at least one aura of THIS call, and the
		// record cannot say that: SendAura deduplicates into rec.Sent, so a re-sent aura (one an expiry or a death
		// stripped after an earlier pull) grows no slice, and a batch the budget cuts returns before its own end.
		// SendAura's success path counts instead, so the hire is read back and named whatever error ended the batch.
		w.prepullTopupSent = 0
		oc, err := dungeon.ApplyAuras(tctx, port, tg.unit, tg.missing, cfg, w.env.now)
		// The count is what went out, not len(tg.missing): a batch the budget cut sent one of its three ids, and the
		// line saying "+3" would overstate the top-up and disagree with the tally's own missing ids.
		n := w.prepullTopupSent
		w.prepullTopupSent = 0
		if n > 0 {
			parts = append(parts, fmt.Sprintf("%s +%d (%s)", tg.rec.Bot, n, tg.cause))
			sent = append(sent, i)
		}
		if err != nil {
			if errors.Is(err, errTopupPauseCap) {
				// A list the cap cut mid-way may already be partly on the hire: it was recorded as sent above, so the
				// read-back and the tally below name what it got and what it still lacks instead of nothing or
				// everything.
				capStep()
				break
			}
			stop(err)
			break
		}
		if oc != dungeon.AuraTaken {
			w.note("prepull-aura: %s: the first .aura (%s) did not land on the bot", tg.rec.Bot, tg.missing[0].Name)
		}
	}
	resume()
	// A walk that ended takes no read-back and leaves no record: a read its cancellation cut is no evidence about the
	// hires, and the window below is the walk's own. The resume above already put the run back, and this is not the
	// top-up's budget, so the cap's line is not written either.
	if ctx.Err() != nil {
		return
	}
	// A resume can still hold the clock past the deadline (a slow ACK): the call's budget is spent then, so the call
	// counts as capped — the step gets its budget line and every later status of it reads nothing — and its read-back is
	// the ONE immediate read below, never the verify wait.
	if !w.env.now().Before(sendDeadline) {
		capStep()
	}
	// The read-back is evidence, not a send: its reads run with the send deadline lifted (a budget-cut read would report
	// every hire the call sent to as missing its whole list, and the per-hire give-up would fire early), and the pass is
	// bounded by the window the verify loop always had — one immediate read per sent hire, no poll, no sleep. A call
	// with no time left has no window: its deadline is now, so the loop below reads once and leaves.
	rbWait := prepullAuraVerifyWait
	if left := sendDeadline.Sub(w.env.now()); left <= 0 {
		rbWait = 0
	} else if left < rbWait {
		rbWait = left
	}
	w.prepullTopupSendDeadline = time.Time{}
	// The window is the walk's clock — the clock the budget was taken on, so a fake clock does not become a wall
	// deadline — and rbctx is derived FROM that deadline, never created with the window before the deadline is known
	// (r3: the poll then hit its own context before the clock reached the deadline, and the early return skipped the
	// record). A context deadline is the window's own end: the loop leaves it normally and the write below always runs.
	rbDeadline := w.env.now().Add(rbWait)
	// The one immediate read of a capped call still needs a window to answer in: a read made under a context that is
	// already done fails, and an unread hire is recorded as missing its whole list (the budget-cut probe r3 warned of),
	// which also fires the per-hire give-up early. The floor is the verify poll's own second, so a store that does not
	// answer costs the call a second, never a top-up.
	rbctx, rbcancel := context.WithTimeout(ctx, max(rbDeadline.Sub(w.env.now()), prepullTopupReadBackMin))
	defer rbcancel()
	// One read-back of the ids just sent: an aura the store does not show is on disk as missing, so the record says so
	// instead of trusting a send that left a bot short. over is how many sent hires still lack one. unread names the
	// hires the window ended before or inside: a read under a done context fails, and failing it would overwrite what an
	// earlier read of that hire showed with "missing its whole list" (and let the per-hire give-up fire early), so the
	// pass stops there and leaves those hires exactly as they were.
	readBack := func() (over int, unread []string) {
		// The window (or the walk) ended: the reads from here on are no reads of the hires.
		ended := func() bool { return rbctx.Err() != nil || ctx.Err() != nil }
		rest := func(n int) []string {
			out := make([]string, 0, len(sent)-n)
			for _, j := range sent[n:] {
				out = append(out, targets[j].rec.Bot)
			}
			return out
		}
		for n, i := range sent {
			if ended() {
				unread = append(unread, rest(n)...)
				break
			}
			tg := &targets[i]
			missing, others, seen := dungeon.AurasRead(rbctx, port, tg.unit, tg.want)
			if !seen && ended() {
				// The window ended INSIDE the call: AurasRead collapses the call's own error into !seen, so the
				// context is the only evidence there is, and a hire straddling the window is unread — never a hire
				// missing its whole list. Its record keeps what the earlier read showed, it is not counted short and
				// it does not count toward the per-step give-up.
				unread = append(unread, rest(n)...)
				break
			}
			if !seen {
				missing, others = tg.want, nil
			}
			tg.rec.Missing = raidwalk.PrepullIDs(missing)
			tg.rec.Seen = subtractIDs(raidwalk.PrepullIDs(tg.want), tg.rec.Missing)
			tg.rec.Others = nil
			if len(missing) > 0 {
				tg.rec.Others = others
				over++
			}
			w.prepullTopupMarkSeen(tg.rec.Bot, tg.rec.Seen)
		}
		return over, unread
	}
	var unread []string
	// A capped step takes the read-back once and waits no more: its budget is spent, so the poll loop must be free for
	// the run's own lines at the cap itself, not five polls later. The tally line still names what is short.
	if capped || rbWait <= 0 {
		_, unread = readBack()
	} else {
		for {
			var over int
			over, unread = readBack()
			if over == 0 || !w.env.now().Before(rbDeadline) || rbctx.Err() != nil || ctx.Err() != nil {
				break
			}
			if err := w.env.sleep(rbctx, prepullAuraVerifyPoll); err != nil {
				// The window ended between two polls (rbctx is the window): what the last read showed is written below.
				if rbctx.Err() != nil || ctx.Err() != nil {
					break
				}
				w.prepullAuraTopupStop(where, err)
				return
			}
		}
	}
	// The walk ended inside the read-back: it leaves without the record, as it does before the pass (a cancelled walk's
	// reads say nothing about the hires). The resume already went out.
	if ctx.Err() != nil {
		return
	}
	// The step's one line, written after the read-back so it can name what is still short per id: a line read as
	// "26-31 of 39 carry their lists" says nothing about WHICH id the short bots miss, and the per-bot tolerance
	// cannot be judged from it.
	if len(parts) > 0 {
		recs := make([]*prepullAuraRecord, 0, len(sent))
		for _, i := range sent {
			recs = append(recs, targets[i].rec)
		}
		// A hire the window ended before is named as such, never as verified or as short: nothing was read for it since
		// its send, so its record and its per-step place keep what the earlier read said.
		cut := ""
		if len(unread) > 0 {
			cut = "; read-back cut, not read: " + strings.Join(unread, ", ")
		}
		w.prepullAuraTopupLine("prepull-aura: %s: %s%s%s", where, strings.Join(parts, ", "), prepullMissTallySuffix(recs), cut)
	}
	// Only a hire whose ids the read-back shows are all there is done for this pull step: one the store still shows
	// short, or does not answer for, stays pending and is read again at the next pre-pull status of the same step —
	// until prepullTopupTryMax attempts were sent to it. An id that will not stick (the server refuses one aura on one
	// bot; ticket T32) is then given up for this step with one line, instead of resending the batch on every status
	// before the same pull.
	unreadBot := make(map[string]bool, len(unread))
	for _, name := range unread {
		unreadBot[strings.ToLower(name)] = true
	}
	for _, i := range sent {
		tg := &targets[i]
		if unreadBot[strings.ToLower(tg.rec.Bot)] {
			// Not read back: the hire stays exactly as it was for this step (its line above said so), so a later status
			// of the same step reads it rather than this call marking it done off a read it never took.
			continue
		}
		mark := w.prepullTopupMark(pullStep, tg.rec.Bot)
		if len(tg.rec.Missing) == 0 {
			mark.read = true
			delete(w.prepullTopupDied, strings.ToLower(tg.rec.Bot))
			continue
		}
		if mark.tries >= prepullTopupTryMax {
			w.prepullAuraTopupLine("prepull-aura: %s: %s still lacks %s after %d tries; giving up for this step",
				where, tg.rec.Bot, idWords(tg.rec.Missing), mark.tries)
			mark.read = true
			delete(w.prepullTopupDied, strings.ToLower(tg.rec.Bot))
		}
	}
	if err := w.prepullAuraWrite(); err != nil {
		w.note("prepull-aura: WARN: %s could not be written (%v)", prepullAuraFileName, err)
	}
	// The record is written whatever the resume did: the stop itself names the paused run, and prepull-auras.json is
	// the evidence of what the call sent before it.
	return stopErr
}

// prepullAuraTopupStop logs a top-up that did not go out. Unlike the boss-row fire it never stops the walk: the pull
// goes on without the auras, and the line says so.
func (w *walk) prepullAuraTopupStop(where string, err error) {
	w.prepullAuraTopupLine("prepull-aura: WARN: %s; the top-up stopped and the pull goes on", prepullAuraTopupReason(where, err))
}

// prepullAuraTopupStopOnce is prepullAuraTopupStop, written once per pull step for key: the combat a later status of
// the same step reads again is the same state, and its line would only repeat the first.
func (w *walk) prepullAuraTopupStopOnce(pullStep int, key, where string, err error) {
	w.prepullAuraTopupOnce(pullStep, key, "prepull-aura: WARN: %s; the top-up stopped and the pull goes on",
		prepullAuraTopupReason(where, err))
}

// prepullAuraTopupReason is the stop line's reason: the error's own, or one named with the step when it carries none.
func prepullAuraTopupReason(where string, err error) string {
	_, reason := stopInfo(err)
	if !strings.HasPrefix(reason, "prepull-aura: ") {
		reason = fmt.Sprintf("prepull-aura: %s: %v", where, err)
	}
	return reason
}

// prepullAuraTopupLine writes one top-up line to run.log and the summary.
func (w *walk) prepullAuraTopupLine(format string, a ...any) {
	msg := fmt.Sprintf(format, a...)
	w.note("%s", msg)
	w.logLine(w.env.now(), msg)
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
		w.prepullTopupMarkSeen(name, subtractIDs(raidwalk.PrepullIDs(want), raidwalk.PrepullIDs(missing)))
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
// stand on the step before the row the fire stands before (fireRow: the boss row itself, or the trigger row that
// summons it, bossRow); a run that passed that row too fast to pause may stand on fireRow itself or beyond it (walk
// 113: steps 421-424 completed in one tick and the boss row 425's pause landed on step 426). On the boss row itself,
// and past fireRow, the shared three cases of prepullAuraVerdict decide (alive out of combat fires, a dead or
// already-killed boss is skipped with one line and the run continues, an engaged or missing boss fails closed); on the
// trigger row only combat can rule the fire out, because the boss is not summoned yet. The boss's arena is read fresh
// from the server after the pause is confirmed (prepullBossArena), never from the cached frames, and the fire's own
// combat read still gates the commander and every member. A wakes-boss fire (wakes) owns every step from the row
// before its pull up to the boss row, and there the wakes verdict decides after the pause (prepullAuraWakesVerdict):
// a dead boss means no fire and no failure (the row is done), an engaged or missing one fails closed, and anything
// else fires where the run stands. A pause before the step before fireRow, or past the row the fire may stand on, is
// not the hold the fire stands before and fails. The confirmation is a fresh `run status`, the run's own answer rather
// than the push feed's newest line. It returns whether the fire may go out; false means the boss row's boss was
// already dead and the run carries on without auras (no failure).
func (w *walk) prepullAuraHold(ctx context.Context, bossRow, fireRow int, pause, wakes bool) (bool, error) {
	step := bossRow
	if pause {
		rep, err := w.commander(ctx, commanderReq{Op: "run", Verb: "pause", WaitMS: 10000})
		if err != nil {
			return false, stopf(exitRaidWalkFailed, "prepull-aura: boss row %d: the run pause failed, no auras were sent: %v", step, err)
		}
		if rep.Reply.Err != nil {
			return false, stopf(exitRaidWalkFailed, "prepull-aura: boss row %d: the run pause was refused (%s), no auras were sent", step, rep.Reply.Err.Message)
		}
		if !rep.Reply.OK && rep.Reply.Kind != tbm.KindAck {
			return false, stopf(exitRaidWalkFailed, "prepull-aura: boss row %d: the run pause was not acknowledged (%s), no auras were sent", step, rep.Reply.Line)
		}
		w.logLine(w.env.now(), fmt.Sprintf("> .bot run pause (prepull-aura, boss row %d)", step))
	}
	rep, err := w.commander(ctx, commanderReq{Op: "run", Verb: "status", WaitMS: 5000})
	if err != nil {
		return false, stopf(exitRaidWalkFailed, "prepull-aura: boss row %d: the paused run could not be confirmed (%v), no auras were sent", step, err)
	}
	if rep.Reply.Err != nil {
		return false, stopf(exitRaidWalkFailed, "prepull-aura: boss row %d: the run status was refused (%s), no auras were sent", step, rep.Reply.Err.Message)
	}
	run, line, ok := lastRunLine(rep.Reply.Lines)
	if !ok {
		return false, stopf(exitRaidWalkFailed, "prepull-aura: boss row %d: the run status answer has no TBM:RUN line, no auras were sent", step)
	}
	if run.State != tbm.RunPaused {
		return false, stopf(exitRaidWalkFailed, "prepull-aura: boss row %d: the run is not paused (%s), no auras were sent", step, line)
	}
	if w.plan != nil && run.Plan != "" && run.Plan != w.plan.Name {
		return false, stopf(exitRaidWalkFailed, "prepull-aura: boss row %d: the paused run is %s, not %s, no auras were sent", step, run.Plan, w.plan.Name)
	}
	if run.Step < fireRow-1 {
		return false, stopf(exitRaidWalkFailed, "prepull-aura: boss row %d: the run paused at step %d, before the step before it (step %d), no auras were sent",
			step, run.Step, fireRow-1)
	}
	if wakes && run.Step > bossRow {
		return false, stopf(exitRaidWalkFailed, "prepull-aura: boss row %d: the run paused at step %d, past the boss row (step %d), no auras were sent",
			step, run.Step, bossRow)
	}
	if run.Step >= fireRow {
		if wakes {
			// The paused run already stands on the waking pull row or past it: the phase and the page's notes cannot
			// prove anything this late, so the fresh arena decides (the same verdict the gate read before the pause).
			c, err := w.prepullAuraWakesVerdict(ctx, bossRow)
			if err != nil {
				return false, err
			}
			switch {
			case c.dead:
				w.prepullAuraDeadLine(bossRow, fmt.Sprintf("the run paused at step %d", run.Step), c.evidence)
				return false, nil
			case c.unclear():
				return false, stopf(exitRaidWalkFailed,
					"prepull-aura: boss row %d: the run paused at step %d, at or past the waking pull row (step %d), %s, no auras were sent",
					bossRow, run.Step, fireRow, c.why())
			}
			w.note("prepull-aura: boss row %d: the run paused at step %d, at or past the waking pull row (step %d); it fires there with the boss not engaged",
				bossRow, run.Step, fireRow)
			return true, nil
		}
		if run.Step > fireRow {
			return w.prepullAuraOvershoot(ctx, step, fireRow, run.Step)
		}
		// The run reached the fire row before the pause confirmed: it stands on the boss row itself, or on the trigger
		// row that summons the boss. The arena is read fresh here, after the pause was confirmed, because the cached
		// hostile frames can lag the server by a frame and show a boss the commander already pulled. The fire re-reads
		// the commander and every living member for combat before its first aura, so a pull that started anyway still
		// stops the batch there.
		if fireRow != step {
			// The trigger row: the boss is not summoned yet, so an arena without a unit of his is the normal state
			// there and only combat can rule the fire out.
			a, err := w.prepullBossArena(ctx, step)
			if err != nil {
				return false, err
			}
			if a.engaged {
				return false, stopf(exitRaidWalkFailed, "prepull-aura: boss row %d: the run paused on the trigger step %d with the boss engaged, no auras were sent", step, fireRow)
			}
			w.note("prepull-aura: boss row %d: the run reached the trigger step %d before the pause; it paused there with the boss not engaged", step, fireRow)
			return true, nil
		}
		// The boss row itself: the same three cases as the overshot pause (prepullAuraVerdict) — a boss alive out of
		// combat fires, a dead or already-killed boss is skipped with one line and the run continues, and an engaged
		// or missing boss fails closed. A pause on the row must prove the boss's state like every other read.
		c, err := w.prepullAuraVerdict(ctx, step)
		if err != nil {
			return false, err
		}
		if c.dead {
			w.prepullAuraDeadLine(step, fmt.Sprintf("the run paused at step %d", run.Step), c.evidence)
			return false, nil
		}
		if c.unclear() {
			return false, stopf(exitRaidWalkFailed,
				"prepull-aura: boss row %d: the run paused on the boss row, %s, no auras were sent", step, c.why())
		}
		w.note("prepull-aura: boss row %d: the run reached the boss row before the pause; it paused there with the boss not engaged", step)
	}
	return true, nil
}

// prepullAuraOvershoot decides the fire of a pause that landed PAST its fire row (the server completed rows in one
// tick, walk 113: steps 421-424 in one tick, boss row 425's pause confirmed on 426). The three cases are the shared
// verdict (prepullAuraVerdict), the same one the boss row's own pause reads: alive out of combat fires where the run
// stands; a dead or already-killed boss gets no fire, one line, and the run continues; an engaged or missing boss (or
// a failed read) fails closed. It returns whether the fire may go out; false is the dead case only.
func (w *walk) prepullAuraOvershoot(ctx context.Context, step, fireRow, at int) (bool, error) {
	c, err := w.prepullAuraVerdict(ctx, step)
	if err != nil {
		return false, err
	}
	switch {
	case c.dead:
		w.prepullAuraDeadLine(step, fmt.Sprintf("the run paused at step %d", at), c.evidence)
		return false, nil
	case c.unclear():
		return false, stopf(exitRaidWalkFailed,
			"prepull-aura: boss row %d: the run paused at step %d, past the step before it (step %d), %s, no auras were sent",
			step, at, fireRow-1, c.why())
	}
	w.note("prepull-aura: boss row %d: the run paused at step %d, past the step before it (step %d); it fires there with the boss not engaged",
		step, at, fireRow-1)
	return true, nil
}

// prepullAuraVerdict is the one three-case decision of a boss row's pre-pull fire, read by both paths: the boss row's
// own pause (prepullGate before it, prepullAuraHold on the row) and a pause that overshot the row
// (prepullAuraOvershoot).
//  1. fire: the boss is alive and nothing in his arena is in combat — the auras may go out;
//  2. dead: the boss is dead — the fresh arena read lists his corpse, or the walk's own boss record for the row already
//     holds the kill (the run's `boss down` note, a fight record, or the end-of-run credit) — no aura goes out, one
//     line says so, and the run continues: in the live report that raised this, the step-418 pull killed Mandokir
//     before his boss row, and preparing a corpse is not the test the run was started for;
//  3. unclear: the boss's state cannot be proven — a unit of the arena in combat, a unit without a combat state, or no
//     unit of the boss in the read at all (a despawned corpse or a read of the wrong place) — the caller fails closed
//     with no aura, because an unknown boss may be pulling.
//
// A dead boss wins over an engaged arena: the row is moot, and failing the walk over a corpse's neighbours would stop
// a raid the run is still walking through. A failed read is the error, never a verdict.
func (w *walk) prepullAuraVerdict(ctx context.Context, step int) (prepullAuraCase, error) {
	if w.prepullBossKilled(step) {
		return prepullAuraCase{dead: true, evidence: "the walk's boss record holds the kill"}, nil
	}
	a, err := w.prepullBossArena(ctx, step)
	if err != nil {
		return prepullAuraCase{}, err
	}
	switch {
	case a.dead:
		return prepullAuraCase{dead: true, evidence: "the arena read shows the boss dead", arena: a}, nil
	case a.engaged, !a.boss:
		return prepullAuraCase{arena: a}, nil
	}
	return prepullAuraCase{fire: true, arena: a}, nil
}

// prepullAuraWakesVerdict is prepullAuraVerdict's three cases for a wakes-boss fire, judged on the BOSS's own unit: a
// waking pull fights its pack in front of the boss, so an engaged trash add is the pull's normal state and no evidence
// about him. Fire: the boss is alive and his own unit is out of combat. Dead: the walk's record holds the kill or the
// arena read lists his corpse (no fire, one line, the run continues). Unclear: his own unit is engaged or carries no
// combat state, or the read lists no unit of him at all — the caller fails closed, because an unknown boss may be
// waking into the pull. A failed read is the error, never a verdict.
func (w *walk) prepullAuraWakesVerdict(ctx context.Context, step int) (prepullAuraCase, error) {
	if w.prepullBossKilled(step) {
		return prepullAuraCase{dead: true, evidence: "the walk's boss record holds the kill"}, nil
	}
	a, err := w.prepullBossArena(ctx, step)
	if err != nil {
		return prepullAuraCase{}, err
	}
	switch {
	case a.dead:
		return prepullAuraCase{dead: true, evidence: "the arena read shows the boss dead", arena: a}, nil
	case a.bossCombat, !a.boss:
		return prepullAuraCase{arena: a}, nil
	}
	return prepullAuraCase{fire: true, arena: a}, nil
}

// prepullAuraCase is one boss row's three-case verdict: fire, dead (with the evidence the line names) or neither, the
// unclear case the caller fails closed on. arena keeps the read, for the failure line's why and the missing-boss line.
type prepullAuraCase struct {
	fire     bool
	dead     bool
	evidence string
	arena    prepullArena
}

// unclear says the verdict is neither a fire nor a settled dead: the caller fails closed with why().
func (c prepullAuraCase) unclear() bool { return !c.fire && !c.dead }

// why is the clause a fail-closed line names after where the run stood: the engaged arena or the missing boss.
func (c prepullAuraCase) why() string {
	if c.arena.engaged {
		return "with the boss engaged"
	}
	return fmt.Sprintf("the arena read (%s) lists no such boss, so his state cannot be ruled out", c.arena.cmd)
}

// prepullAuraDeadLine is the one line a dead boss row writes: no fire, and the run continues. where names where the
// run stood (the boss row became current, or the paused step), evidence what held the kill. The row is marked dead by
// the caller (prepullAuraMarkDead), so its own statuses neither pause again nor fail as "never prepared".
func (w *walk) prepullAuraDeadLine(step int, where, evidence string) {
	msg := fmt.Sprintf("prepull-aura: boss row %d: boss already dead, no pre-pull fire (%s, %s)", step, where, evidence)
	w.note("%s", msg)
	w.logLine(w.env.now(), msg)
}

// prepullAuraMarkDead records a boss row whose boss was already dead: no fire, and no later status of the row is a
// failure or a second pause.
func (w *walk) prepullAuraMarkDead(step int) {
	if w.prepullAuraDead == nil {
		w.prepullAuraDead = map[int]bool{}
	}
	w.prepullAuraDead[step] = true
}

// prepullBossKilled says the walk's own boss record for the row already holds the kill: the run's `boss down` note
// (observeBossStatus), a commander fight record (creditFightKills) or the end-of-run credit (bossEndStatus). A dead
// boss can also have despawned before the arena read, so the walk's record is the one evidence a corpse row cannot be.
func (w *walk) prepullBossKilled(step int) bool {
	t, ok := w.bossTracks[bossKey{step: step}]
	return ok && t != nil && t.killed
}

// prepullArena is what one fresh arena read says about a boss row's boss: whether a unit of the boss's own family
// (bossUnits: the row's entry and the phase entries that are the boss reborn) is in the reply, whether every such
// unit is dead, whether the arena holds a unit in combat (or one whose combat state the reply does not carry), and
// whether that is the boss's own unit. The boss's body is the evidence the pre-pull cases turn on, so the read keeps it
// apart from the arena's adds: a wakes-boss fire is judged on the boss (the trash pack it is pulling in front of him
// may well be in combat and is no evidence about him), a boss row's own fire on the arena.
type prepullArena struct {
	boss       bool   // a boss unit is in the reply
	dead       bool   // every boss unit in the reply is dead (no live form of him)
	engaged    bool   // a unit is in combat, or its combat state is missing
	bossCombat bool   // the boss's own unit is in combat, or its combat state is missing
	cmd        string // the `q units` line the read made, for the failure lines
}

// prepullBossArena reads the boss row's arena FRESH from the server: `q units 60 <anchor> all`, the same read the
// reuse-instance guard (`placeBossEngaged`) makes, with `cb` the combat state and `dead` the corpse flag. The walk's
// cached hostile frames (`bossEngagedNow`) are not used here: the push feed's statuses are handled before the next view
// frame is fetched, so a cached frame can still show the boss unengaged after the commander has pulled. It fails
// closed: a boss row without a point, a refused or unanswered read, and a truncated reply are errors, never a clean
// read, and the callers send no aura on any of them (ttUnitOf's own InCombat stays tolerant for the other callers; the
// prepull read demands the field). An unproven unit is counted as in combat too, no matter whether it is the boss.
func (w *walk) prepullBossArena(ctx context.Context, step int) (prepullArena, error) {
	row, ok := w.bossRowAt(step)
	if !ok {
		return prepullArena{}, stopf(exitRaidWalkFailed, "prepull-aura: boss row %d is not one of the plan's rows", step)
	}
	if row.X == 0 && row.Y == 0 {
		return prepullArena{}, stopf(exitRaidWalkFailed,
			"prepull-aura: boss row %d has no point to read the boss's combat state at, so combat cannot be ruled out: no auras were sent", step)
	}
	cmd := fmt.Sprintf("q units %d %s %s %s all", placeBossArenaYd, num32(row.X), num32(row.Y), num32(row.Z))
	a := prepullArena{cmd: cmd}
	out, err := w.tt(ctx, cmd)
	if err != nil {
		return prepullArena{}, stopf(exitRaidWalkFailed,
			"prepull-aura: boss row %d: the arena read (%s) failed, so combat cannot be ruled out: no auras were sent: %v", step, cmd, err)
	}
	body, ok := ttLast(out)
	if !ok {
		return prepullArena{}, stopf(exitRaidWalkFailed,
			"prepull-aura: boss row %d: the arena read (%s) gave no reply, so combat cannot be ruled out: no auras were sent", step, cmd)
	}
	if body.flag("trunc") {
		return prepullArena{}, stopf(exitRaidWalkFailed,
			"prepull-aura: boss row %d: the arena read (%s) lists more units than one reply carries, so combat cannot be ruled out: no auras were sent", step, cmd)
	}
	units := w.bossUnits(row.Entry)
	live, dead := false, false
	for _, m := range body.list("units") {
		u := ttUnitOf(m)
		isBoss := inEntries(units, u.Entry)
		if u.Dead {
			// A corpse is no combatant: it neither engages the arena nor proves a live boss. The boss's own corpse is
			// the dead that prepullAuraOvershoot's case 2 reads.
			if isBoss {
				dead = true
			}
			continue
		}
		if isBoss {
			live = true
		}
		if !u.CBKnown {
			// The reply does not say whether this unit is fighting (a degraded or version-skewed toolkit answer still
			// has a valid envelope and no trunc flag). An unproven unit is not an out-of-combat unit.
			if !a.engaged {
				w.note("prepull-aura: boss row %d: %s (%d) carries no combat state in the arena read at %s: counted as in combat", step, u.Name, u.Entry, cmd)
			}
			a.engaged = true
			a.bossCombat = a.bossCombat || isBoss
			continue
		}
		if u.InCombat && !a.engaged {
			w.note("prepull-aura: boss row %d: %s (%d) is in combat in the arena read at %s", step, u.Name, u.Entry, cmd)
			a.engaged = true
		}
		if u.InCombat {
			a.bossCombat = a.bossCombat || isBoss
		}
	}
	a.boss = live || dead
	a.dead = dead && !live
	return a, nil
}

// prepullAuraCombatClear re-reads the commander and every living raid member and refuses the batch while any one of
// them is in combat: a pull that starts after the pause must stop the auras, not receive them after the encounter
// began. The commander's view leaves the player out, so the watch carries it first; a pull the commander started is
// the one a member-only check misses. It fails closed on the read too: a unit the RPC cannot read may be fighting, so
// the batch stops there as well - but only after the unit store was given its window (prepullAuraUnitWait, one shared
// prepullUnitReadMax per fire), because walk 105's main tank stood ahead of the commander with its row a few seconds
// late. A member whose row never arrives still stops the batch. The caller builds the watch from the commander and the
// whole raid, not only the members that carry an aura list: an unhired member can pull too. where names the fire in
// its failure ("boss row 5", or "start (row 1)"), so the walk-101 line reads as the start fire it was.
func (w *walk) prepullAuraCombatClear(ctx context.Context, where string) error {
	// The caller's context carries the call's budget (the top-up derives it once at entry; a fire has none), each unit
	// waits no longer than the time left on the walk's clock, and a unit with no budget left is not read at all. A read
	// the budget cut is no proof of anything, so it never clears the batch: errTopupPauseCap sends the caller down the
	// cap path instead, and that step writes no more auras.
	deadline := w.prepullTopupSendDeadline
	shared := w.prepullAuraCombatWaitEnd
	if !deadline.IsZero() && (shared.IsZero() || deadline.Before(shared)) {
		shared = deadline
	}
	port := walkAuras{w: w}
	for _, t := range w.prepullAuraWatch {
		if err := w.prepullTopupCut(ctx); err != nil {
			return err
		}
		u, seen, _, err := w.prepullAuraUnitWait(ctx, port, where, t, shared)
		if err != nil {
			return err
		}
		if !seen {
			if cut := w.prepullTopupCut(ctx); cut != nil {
				return cut
			}
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

// prepullJoinRadius is how far the commander may stand from the hire it would join before a fire moves it: the
// agent's own unit store holds only units in the visibility range of the commander's character, and the fires read
// every hire through it.
const prepullJoinRadius = 60

// prepullJoinWait bounds the `.appear` a fire sends at a hire (appearAtHire's own landing wait is shorter).
const prepullJoinWait = 30 * time.Second

// prepullAuraJoinRaid puts the COMMANDER at the raid before a fire. ViewUnit reads the agent's own client object store
// (internal/objects/store.go), which only holds units in visibility range of the commander's character and is cleared
// on a map change (onNewWorld -> store.Clear()), while the commander's view (the TBM frame) lists the whole raid on
// any map: walk 105c's start fire (commander on map 0 outside, hires on map 309 after `cmd enter near`), walks
// 105/105b's Jeklik row 49 (raid 350-364 yd away) and walk 104's row 201 (298-317 yd) all read units the store did not
// hold, and row 49 at 146-155 yd worked. The commander's map alone is not enough: it may stand on the plan's map while
// the hires stand on another (walk 105c's start fire), or on the plan's map in a copy of its own. The no-join path
// therefore asks the TARGET hire's map and the distance to that hire, and a commander that fails either joins too.
// The frame is re-read here, because the caller's can predate the placement's `--enter tele` wave and the landing
// check uses the hire's frame position. The appear is the revive path's (appearAtHire: `.appear` binds the GM to the
// hire's copy, then a fresh landing is proven), and the commander is hidden first with the revive's own `.gm visible
// off`, so the appear lands unseen; the walk joins at its own MT when the frame shows it alive, else at the living
// hire nearest the commander. Only the GM commander moves, never a hire. A join that cannot land stops the fire
// before any aura.
func (w *walk) prepullAuraJoinRaid(ctx context.Context, raid []tbm.PartyRec, where string) error {
	st, err := w.status(ctx)
	if err != nil {
		return stopf(exitRaidWalkFailed, "prepull-aura: %s: the commander's status: %v: no aura was sent", where, err)
	}
	// The start fire polls the frame while the placement's teleport wave is still landing, so the rows the caller
	// holds can be a read from before the hires moved: re-read right before the join, and keep the caller's rows only
	// when the fresh read fails (the fire already validated them).
	if fresh, rerr := w.prepullRaid(ctx); rerr == nil {
		raid = fresh
	}
	target, dist, living := w.prepullJoinTarget(st, raid)
	if !living {
		return stopf(exitRaidWalkFailed,
			"prepull-aura: %s: the commander is on map %d with no living hire in its view to join: no aura was sent", where, st.Map)
	}
	// The commander's own map proves nothing: walk 105c's start fire had it on the plan's map and the target hire on
	// 309, and a commander on the plan's map in a copy of its own is just as far from the target. Only the hire the
	// fire would join places the two together, so the no-join path asks that hire's map, not the plan's.
	if st.Map == target.Map && dist <= prepullJoinRadius {
		return nil
	}
	// The revive hides the commander before its own appear; a visible GM landing in the raid is a line nobody asked
	// for, so the prepull join sends the same line first.
	if err := w.gm(ctx, raidwalk.GMVisibleOff); err != nil {
		return stopf(exitRaidWalkFailed, "prepull-aura: %s: %s before the join failed: %v: no aura was sent", where, raidwalk.GMVisibleOff, err)
	}
	// The landing check reads the hire's last frame position from botAt: this fire's own frame is the freshest one.
	if w.botAt == nil {
		w.botAt = map[string]botPos{}
	}
	w.botAt[target.Name] = botPos{guid: target.GUID, x: target.X, y: target.Y, z: target.Z, inCombat: target.InCombat(),
		mapID: target.Map, at: w.env.now()}
	joined, why, err := w.appearAtHire(ctx, target.Name, w.env.now().Add(prepullJoinWait))
	if err != nil {
		return err
	}
	if !joined {
		return stopf(exitRaidWalkFailed, "prepull-aura: %s: the commander could not join the raid at %s: %s: no aura was sent",
			where, target.Name, why)
	}
	w.logLine(w.env.now(), fmt.Sprintf("prepull-aura: %s: commander joined the raid at %s (was %d yd / map %d)",
		where, target.Name, int(math.Round(dist)), st.Map))
	return nil
}

// prepullJoinTarget is the hire the commander joins: its own MT when the frame shows it alive, else the living hire
// nearest the commander (x/y; a cross-map distance is only the log's number). Names are matched case-insensitively,
// like the rest of the fire: the frame's spelling may differ from the roster's. living is false when the frame holds
// no living hire of the walk's.
func (w *walk) prepullJoinTarget(st session.StatusInfo, raid []tbm.PartyRec) (target tbm.PartyRec, dist float64, living bool) {
	var mt tbm.PartyRec
	mtDist := math.Inf(1)
	best := math.Inf(1)
	for _, m := range raid {
		if m.GUID == 0 || m.Name == "" || m.Dead || m.State == "d" {
			continue
		}
		name, ok := w.hireName(m.Name)
		if !ok {
			continue
		}
		// The roster's spelling is what the GM line and the fire's records use (the allow-list matches it exactly).
		m.Name = name
		living = true
		d := math.Hypot(float64(st.X-m.X), float64(st.Y-m.Y))
		if d < best {
			target, best = m, d
		}
		if w.mt != "" && strings.EqualFold(name, w.mt) {
			mt, mtDist = m, d
		}
	}
	if mt.GUID != 0 {
		return mt, mtDist, true
	}
	return target, best, living
}

// hireName returns the walk's own spelling of a hire name, matched case-insensitively: the commander's frame and the
// roster can spell the same bot differently, and the fire's lookups lowercase both sides.
func (w *walk) hireName(name string) (string, bool) {
	for hire := range w.hires {
		if strings.EqualFold(hire, name) {
			return hire, true
		}
	}
	return "", false
}

// prepullAuraStartWait waits for every hire to be in the commander's view and alive, joins the raid once, and only
// then waits for the agent's own unit store to hold every one of them, before the start fire reads the raid's classes
// and roles. Walk 101's ZG Jeklik run (plan start row 1) is why the store is waited for: the placement's `.tele` wave
// and the fire shared a second, the fire went out 0.16 s after the last `.tele`, and the unit read of the MT failed
// twice before the walk spent a single GM line - the commander's frame already named the raid, but the agent's unit
// store had not received the teleported bots yet. Walk 105c is why the join comes between the two: the start fire can
// run with the commander still outside the instance (map 0 against hires on map 309 after `cmd enter near`), and the
// store is cleared on the map change and only fills in the commander's visibility range, so a store poll from outside
// never answers (prepullAuraJoinRaid). The frame poll keeps its 60 s/2 s window, the store poll gets its own 60 s, and
// the success line names the total time, so a slow instance shows its own number. It stays fail-closed: a hire still
// missing after its wait (or a view read that keeps failing) stops the fire with the hire named, never a fire on an
// unproven raid.
func (w *walk) prepullAuraStartWait(ctx context.Context) ([]tbm.PartyRec, error) {
	begin := w.env.now()
	roster := w.hireList()
	var raid []tbm.PartyRec
	var inView map[string]tbm.PartyRec
	var lastErr error
	// (a) The commander's frame: every hire present and alive. Nothing else rides on this loop, so the unit store is
	// not read from wherever the commander happens to stand.
	for {
		r, err := w.prepullRaid(ctx)
		var missing []string
		if err == nil {
			raid = r
			inView = make(map[string]tbm.PartyRec, len(raid))
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
				}
			}
			if len(missing) == 0 {
				break
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
	// (b) Join once, before the store is read: an outside commander's store holds no hire at all.
	if err := w.prepullAuraJoinRaid(ctx, raid, "start"); err != nil {
		return nil, err
	}
	// (c) The agent's unit store: the very next read (the class) goes to it, and after a map change it fills behind
	// the frame (walk 101).
	port := walkAuras{w: w}
	storeBegin := w.env.now()
	for {
		var missing []string
		for _, name := range roster {
			m := inView[strings.ToLower(name)]
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
		waited := w.env.now().Sub(storeBegin).Round(time.Millisecond)
		if !w.env.now().Before(storeBegin.Add(prepullStartStoreWait)) {
			return nil, stopf(exitRaidWalkFailed,
				"prepull-aura: start: after waiting %s, %d of the walk's %d hire(s) are not in the agent's unit store and alive: %s: no aura was sent",
				waited, len(missing), len(roster), strings.Join(missing, "; "))
		}
		if err := w.env.sleep(ctx, prepullStartWaitPoll); err != nil {
			return nil, err
		}
	}
}

// prepullAuraUnitWait reads one hire's unit row, waiting out an agent unit store that has not received it yet: the
// first read is immediate, then one re-read every 2 s, for up to prepullUnitReadMax or the fire's shared budget for
// this phase when that comes first. Walk 105 is why: at ZG Jeklik's boss row 49 the two back-to-back reads of the main
// tank, standing ahead of the commander, both missed it and the fire stopped a run that was 24 minutes in, while the
// store held the row a few seconds later. One run.log line records the wait - the time it took, or that it ran out -
// and only when the first read missed, so a raid the store holds whole costs no line per bot per boss row. The read
// stays fail-closed: the caller decides what an unseen unit means, and the wait is no licence to fire on a unit the
// walk cannot read. waited is the time spent, for the caller's own failure line. A read the call's budget cut is not a
// store that missed the row: the wait returns errTopupPauseCap then, so the top-up caps the step instead of reading it
// as a combat or unit failure.
func (w *walk) prepullAuraUnitWait(ctx context.Context, port walkAuras, where string, t dungeon.AuraTarget, shared time.Time) (u objects.Unit, seen bool, waited time.Duration, err error) {
	begin := w.env.now()
	if u, seen = port.ViewUnit(ctx, t); seen {
		return u, true, 0, nil
	}
	// A read the call's budget cut is not a store that missed the row: the caller caps the step on it, and none of the
	// waits below may spend more of the budget either.
	if err := w.prepullTopupCut(ctx); err != nil {
		return objects.Unit{}, false, 0, err
	}
	deadline := begin.Add(prepullUnitReadMax)
	if !shared.IsZero() && shared.Before(deadline) {
		deadline = shared
	}
	for w.env.now().Before(deadline) {
		poll := prepullUnitReadPoll
		if left := deadline.Sub(w.env.now()); left < poll {
			poll = left
		}
		if err := w.env.sleep(ctx, poll); err != nil {
			if cut := w.prepullTopupCut(ctx); cut != nil {
				return objects.Unit{}, false, 0, cut
			}
			return objects.Unit{}, false, 0, err
		}
		if u, seen = port.ViewUnit(ctx, t); seen {
			break
		}
	}
	waited = w.env.now().Sub(begin).Round(time.Millisecond)
	if seen {
		w.logLine(w.env.now(), fmt.Sprintf("prepull-aura: %s: the unit read of %s took %s (the first read missed it)", where, t.Name, waited))
	} else {
		if err := w.prepullTopupCut(ctx); err != nil {
			return objects.Unit{}, false, waited, err
		}
		w.logLine(w.env.now(), fmt.Sprintf("prepull-aura: %s: the unit read of %s ran out after %s", where, t.Name, waited))
	}
	return u, seen, waited, nil
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
// passed; a hire the unit store misses is waited for first (prepullAuraUnitWait), and one still unseen after that wait
// takes its roster role when the module's state names one (prepullRosterRole). Before any of that the commander joins
// the raid (prepullAuraJoinRaid) when it stands on another map or out of range: the unit store is the commander's own
// view of the world, not the raid's. A failed fire is recorded as an attempt with its reason; only a fire whose auras
// verified within the tolerance appends the boss row (or start_row under --prepull-aura-when start), so the file
// never reports a preparation that did not happen. The file is written on every fire, failures included, so the
// attempt's reason and the missing list are always on disk. step is the boss
// row the fire prepares, or the run's start row when start is true.
func (w *walk) prepullAuraFire(ctx context.Context, step int, start bool) (err error) {
	where := prepullAuraWhere(step, start)
	defer func() {
		if err != nil {
			w.prepullAuraAttempts = append(w.prepullAuraAttempts, prepullAuraAttempt{BossRow: step, Start: start, Reason: err.Error()})
		}
		w.prepullAuraWatch, w.prepullAuraWhat = nil, ""
		w.prepullAuraUnitWaitEnd, w.prepullAuraCombatWaitEnd = time.Time{}, time.Time{}
		if werr := w.prepullAuraWrite(); werr != nil {
			w.note("prepull-aura: WARN: %s could not be written (%v)", prepullAuraFileName, werr)
		}
	}()
	// The start fire is the one fire that runs inside the placement's own teleport wave, so it is the one that waits
	// for the raid to arrive (prepullAuraStartWait): at every boss row the raid has been in the commander's view and
	// the agent's unit store for a while already.
	var raid []tbm.PartyRec
	if start {
		// The start wait joins once itself, after the frame shows the raid and before it reads the unit store.
		raid, err = w.prepullAuraStartWait(ctx)
	} else {
		raid, err = w.prepullRaid(ctx)
		if err == nil {
			// The agent's unit store holds only units in the commander's visibility range: a fire from wherever the
			// last placement left the commander reads no hire at all until it joins the raid.
			err = w.prepullAuraJoinRaid(ctx, raid, where)
		}
	}
	if err != nil {
		return err
	}
	// The fire's own unit-read budgets: the role reads share one window, the combat rechecks another (a store that
	// lags is waited for twice at most, whatever the raid's size).
	w.prepullAuraUnitWaitEnd = w.env.now().Add(prepullUnitReadMax)
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
		u, seen, waited, err := w.prepullAuraUnitWait(ctx, port, where, t, w.prepullAuraUnitWaitEnd)
		if err != nil {
			return err
		}
		if (seen && (u.Dead || u.Ghost)) || (!seen && (m.Dead || m.State == "d")) {
			uncovered[name] = "is dead, so it cannot carry a list"
			continue
		}
		watch = append(watch, t)
		// A role guessed from an unreadable row would put only the "all" lists on the bot while coverage still passed,
		// so an unknown role fails the fire: a hire gets its role's list or the walk stops with it named. The class
		// comes from the unit row alone, so a seen row with no class is still a failure; a hire still unseen after the
		// wait takes the role the module's own roster names for it (prepullRosterRole, logged as such), because the
		// roster names a role without needing the class - walk 105's main tank was unseen for seconds at a boss row.
		// The MT/OT assignment is no shortcut there: the unseen read trusts the roster row alone.
		var role, why string
		switch {
		case seen && u.Class == 0:
			return stopf(exitRaidWalkFailed,
				"prepull-aura: %s: %s is a hire whose unit row carries no class, so its aura role is unknown: no aura was sent", where, name)
		case seen:
			role, why = w.prepullRole(name, u.Class)
		default:
			role, why = w.prepullRosterRole(name)
			if role != "" {
				w.logLine(w.env.now(), fmt.Sprintf("prepull-aura: %s: %s: role from roster (unit unseen): %s", where, name, role))
			}
		}
		if role == "" {
			if !seen {
				return stopf(exitRaidWalkFailed,
					"prepull-aura: %s: the unit read of %s failed after %s and the roster gives no role either: %s, so its class and aura role are unknown: no aura was sent",
					where, name, waited, why)
			}
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
		w.prepullTopupMarkSeen(name, rec.Seen)
		targets = append(targets, prepullAuraTarget{rec: rec, unit: t, want: list, missing: missing})
	}
	w.prepullAuraWatch, w.prepullAuraWhat = watch, where
	w.prepullAuraCombatWaitEnd = w.env.now().Add(prepullUnitReadMax)
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
			missing, others, seen := dungeon.AurasRead(ctx, port, tg.unit, tg.want)
			if !seen {
				missing, others = tg.want, nil
			}
			tg.rec.Missing = raidwalk.PrepullIDs(missing)
			tg.rec.Seen = subtractIDs(raidwalk.PrepullIDs(tg.want), tg.rec.Missing)
			// What the bot carries instead of a missing id: nil whenever it misses nothing, so the file only names
			// substitutes where there is a miss to explain.
			tg.rec.Others = nil
			if len(missing) > 0 {
				tg.rec.Others = others
			}
			w.prepullTopupMarkSeen(tg.rec.Bot, tg.rec.Seen)
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
		recs := make([]*prepullAuraRecord, 0, len(within))
		for _, name := range within {
			recs = append(recs, w.prepullAuraBots[name])
		}
		w.logLine(w.env.now(), fmt.Sprintf("prepull-aura: %s: %d bot(s) carry their lists; %d bot(s) miss auras within the tolerance of %d: %s%s",
			where, len(targets)-len(within), len(within), tol, w.prepullAuraMissSummary(within), prepullMissTallySuffix(recs)))
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

// prepullAuraClear forgets which boss rows fired (and which were found dead), so the rest before the next boss row
// (the one the wipe retries) sends the lists again: a death stripped them. The bots' records keep their sent ids; the
// next fire's read-back rewrites seen and missing. The top-up's per-step completion is forgotten too — the lines it
// already wrote and a budget it already spent included: the pull a recovery retries must be read again hire by hire,
// and the read from before the wipe describes a raid that no longer exists.
func (w *walk) prepullAuraClear() {
	if len(w.prepullAuras) == 0 {
		return
	}
	w.prepullAuraSent = nil
	w.prepullAuraDead = nil
	w.prepullTopupDone = nil
	w.prepullTopupSaid = nil
	w.prepullTopupCapped = nil
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
// with; the walk maps that to the aura list. The walk's own MT and OT are tanks whatever their kit when their unit row
// is readable (they hold the raid's front), but they still need a state row like every hire; unseen, the roster read
// decides (prepullRosterRole). role == "" comes with the reason the walk cannot tell it (no state row for the bot, a
// class no aura role covers, or a kit naming no spec or specs of more than one role); the fire fails closed on that
// reason, never as an "all"-only bot.
func (w *walk) prepullRole(name string, class uint8) (role, why string) {
	specs, ok := w.prepullAuraSpecs[strings.ToLower(name)]
	if !ok {
		return "", "the module's `.bot roster` state names no row for it, so its role cannot be read"
	}
	if w.prepullHoldsFront(name) {
		return raidwalk.PrepullTank, ""
	}
	className := raidwalk.ClassName(class)
	if className == "" {
		return "", fmt.Sprintf("its unit row carries class %d, which no aura role covers", class)
	}
	return raidwalk.BotStateAuraRole(className, specs)
}

// prepullRosterRole is prepullRole for a hire whose unit row the agent's store does not hold, so no class is there to
// key the module's per-class spec table: the role comes from the module's own spec strategies alone
// (raidwalk.BotStateAuraRoleUnclassed, which fails closed on a strategy whose classes split on roles), and a bot
// without a state row has no role. The walk's own MT and OT are no exception: without the class, the roster's row is
// the only role source, so an unseen MT whose row names a non-tank kit is not handed the tank list and one whose row
// names no single role stops the fire like any other hire. It is the fallback of a fire whose unit read ran out
// (prepullAuraUnitWait), never a guess: every failure names its reason.
func (w *walk) prepullRosterRole(name string) (role, why string) {
	specs, ok := w.prepullAuraSpecs[strings.ToLower(name)]
	if !ok {
		return "", "the module's `.bot roster` state names no row for it, so its role cannot be read"
	}
	return raidwalk.BotStateAuraRoleUnclassed(specs)
}

// prepullHoldsFront says whether name is the walk's own main tank or one of its off tanks: they hold the raid's
// front whatever their kit, so their aura role is the tank's.
func (w *walk) prepullHoldsFront(name string) bool {
	if w.mt != "" && strings.EqualFold(name, w.mt) {
		return true
	}
	for _, ot := range w.ots {
		if strings.EqualFold(name, ot) {
			return true
		}
	}
	return false
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

// prepullMissTally sums which ids the given bots miss, as "22730 x7, 18192 x5": the count per id, the largest first
// and ties by id. It is the line's per-ID tally, which counts BOTS per id and so reads the opposite way from
// --prepull-aura-tolerance's per-BOT bound: a walk that stopped "26-31 of 39 carry their lists" had every short bot
// missing one id, well within the tolerance, while the tally showed most of them missing the same food id.
func prepullMissTally(recs []*prepullAuraRecord) string {
	count := map[uint32]int{}
	for _, rec := range recs {
		if rec == nil {
			continue
		}
		for _, id := range rec.Missing {
			count[id]++
		}
	}
	ids := make([]uint32, 0, len(count))
	for id := range count {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool {
		if count[ids[i]] != count[ids[j]] {
			return count[ids[i]] > count[ids[j]]
		}
		return ids[i] < ids[j]
	})
	words := make([]string, len(ids))
	for i, id := range ids {
		words[i] = fmt.Sprintf("%d x%d", id, count[id])
	}
	return strings.Join(words, ", ")
}

// prepullMissTallySuffix is prepullMissTally as the two lines append it: "; missing by id: 22730 x7, 18192 x5", and
// "" when no bot misses anything.
func prepullMissTallySuffix(recs []*prepullAuraRecord) string {
	tally := prepullMissTally(recs)
	if tally == "" {
		return ""
	}
	return "; missing by id: " + tally
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
	// B3: a top-up's 15 s budget, counted from before its reads, bounds every send of that call, held or not.
	if !p.w.prepullTopupSendDeadline.IsZero() && !p.w.env.now().Before(p.w.prepullTopupSendDeadline) {
		return errTopupPauseCap
	}
	if len(p.w.prepullAuraWatch) > 0 {
		if err := p.w.prepullAuraCombatClear(ctx, p.w.prepullAuraWhat); err != nil {
			return err
		}
	}
	// The combat read is a round trip per watched unit, and the last of them can answer after the deadline: the check
	// at the top of this call is not enough, and a send past the cap is exactly the boss-row overrun the cap exists to
	// stop. Past it the step is capped like any other spent budget: this call's error, the caller's line, no send.
	if !p.w.prepullTopupSendDeadline.IsZero() && !p.w.env.now().Before(p.w.prepullTopupSendDeadline) {
		return errTopupPauseCap
	}
	line := fmt.Sprintf(".aura %d", b.ID)
	p.w.logLine(p.w.env.now(), fmt.Sprintf("> %s (gm_aura, selected %s)", line, t.Name))
	params := map[string]any{"guid": t.GUID.String(), "spell_id": b.ID}
	if err := p.w.agent.Call(ctx, "gm_aura", params, nil, 15*time.Second); err != nil {
		return stopf(exitRaidWalkFailed, "prepull-aura: %s on %s: %v", line, t.Name, err)
	}
	// The line went out: a top-up call's caller counts it here, not in the record below (a re-sent id is deduplicated
	// into rec.Sent, so the slice does not grow for it).
	p.w.prepullTopupSent++
	if rec := p.w.prepullAuraBots[t.Name]; rec != nil && !slices.Contains(rec.Sent, b.ID) {
		rec.Sent = append(rec.Sent, b.ID)
	}
	return nil
}

// ViewUnit reads the target's unit row from the agent's store (auras included); false when the store does not hold it.
// A top-up's send budget bounds the read too (B3): the RPC gets at most the time left, and one with none left is not
// attempted at all - a read the budget cut is no row, so its caller reads it as "not clear".
func (p walkAuras) ViewUnit(ctx context.Context, t dungeon.AuraTarget) (objects.Unit, bool) {
	timeout := 10 * time.Second
	if deadline := p.w.prepullTopupSendDeadline; !deadline.IsZero() {
		left := deadline.Sub(p.w.env.now())
		if left <= 0 {
			return objects.Unit{}, false
		}
		timeout = min(timeout, left)
	}
	var u objects.Unit
	if err := p.w.agent.Call(ctx, "unit", map[string]any{"guid": t.GUID.String()}, &u, timeout); err != nil {
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
