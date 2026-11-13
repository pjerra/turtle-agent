// raid_walk_prepullaura.go: the `--prepull-aura` test tool of raid-walk (test servers only, off by default). At the
// pre-pull rest before each boss row the commander puts the flag's aura lists on every living party member with the
// same GM select-then-".aura" call dungeon_run's buff presets use (internal/dungeon's AuraPort and ApplyAuras), so a
// walk tests the preparation a real raid carries into the instance: a flask, elixirs, food and the world buffs, all
// as auras the walk can read back.
//
//	--prepull-aura role=tank:17626,17538 --prepull-aura role=healer:17627,24363 --prepull-aura all:world,23768
//
// The fire is the boss-row resend's gate (preBossGate): a rest or gather of the step before a boss row, out of combat,
// once per boss row per walk, under a run pause that is always resumed. The auras a bot already carries are not sent
// again, so a second boss row only tops up what a death or a buff strip took. Every fire is read back into
// prepull-auras.json (bot, role, ids sent/seen); a bot missing more than prepullAuraMissMax ids fails the walk
// (exit 18), because a preparation the walk cannot see is not the test the run was started for.
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/pjerra/turtle-agent/internal/dungeon"
	"github.com/pjerra/turtle-agent/internal/objects"
	"github.com/pjerra/turtle-agent/internal/raidwalk"
	"github.com/pjerra/turtle-agent/internal/tbm"
)

// The walk's prepull-aura timings. The gap and the settle are dungeon_run's own buff timings, so one boss row's ~20
// bots and ~10 auras cost about a minute of the run pause; the verify window covers the last auras' arrival.
var (
	prepullAuraGap        = 300 * time.Millisecond
	prepullAuraSettle     = 1500 * time.Millisecond
	prepullAuraViewWait   = 10 * time.Second
	prepullAuraVerifyWait = 5 * time.Second
	prepullAuraVerifyPoll = time.Second
)

// prepullAuraMissMax is the most ids one bot may miss after a fire: three or more fail the walk (exit 18).
const prepullAuraMissMax = 2

// prepullAuraFileName is the fire's evidence in the walk's out dir.
const prepullAuraFileName = "prepull-auras.json"

// prepullAuraFile is prepull-auras.json: the flag set, the merged lists and every bot's line.
type prepullAuraFile struct {
	Flag     []string            `json:"flag"`
	Lists    []prepullAuraList   `json:"lists"`
	BossRows []int               `json:"boss_rows"`
	Bots     []prepullAuraRecord `json:"bots"`
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
	Sent    []uint32 `json:"sent"`
	Seen    []uint32 `json:"seen"`
	Missing []uint32 `json:"missing"`
}

// prepullAuraTarget is one bot of a fire: its record, its guid, and the list it must carry.
type prepullAuraTarget struct {
	rec  *prepullAuraRecord
	unit dungeon.AuraTarget
	want []dungeon.Buff
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

// prepullAuraSendPending is pollOnce's firing: at the pre-pull rest or gather before a boss row, once per boss row,
// the auras go out under a run pause. Whenever the status was running before the pause attempt, the resume is
// attempted afterwards even when the fire failed: a run left paused stops the raid silently. A status that was
// already paused sends no resume. It reports whether a firing happened, so pollOnce breaks its line loop (the next
// poll re-reads from the cursor), like the instance-data firing.
func (w *walk) prepullAuraSendPending(ctx context.Context, st tbm.RunStatus, later bool) (bool, error) {
	if len(w.prepullAuras) == 0 {
		return false, nil
	}
	step, fire := w.preBossGate(st, later)
	if step == 0 {
		return false, nil
	}
	if w.prepullAuraSent == nil {
		w.prepullAuraSent = map[int]bool{}
	}
	if w.prepullAuraSent[step] {
		return false, nil
	}
	w.prepullAuraSent[step] = true
	if !fire {
		w.note("prepull-aura: boss row %d became current with no out-of-combat rest before it: the auras were not sent", step)
		w.logLine(w.env.now(), fmt.Sprintf("prepull-aura: boss row %d: no pre-pull rest, the auras were not sent", step))
		return false, nil
	}
	pauseAttempted := st.State == tbm.RunRunning
	if pauseAttempted {
		if _, err := w.commander(ctx, commanderReq{Op: "run", Verb: "pause", WaitMS: 10000}); err != nil {
			w.note("prepull-aura: WARN: the run pause before boss row %d was refused (%v): the auras go out unheld", step, err)
		}
	}
	fireErr := w.prepullAuraFire(ctx, step)
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

// prepullAuraFire puts the flag's lists on every living party member, reads them back, writes prepull-auras.json and
// fails the walk when a bot misses more than prepullAuraMissMax ids.
func (w *walk) prepullAuraFire(ctx context.Context, step int) error {
	party, err := w.party(ctx)
	if err != nil {
		return stopf(exitRaidWalkFailed, "prepull-aura: party: %v", err)
	}
	port := walkAuras{w: w}
	cfg := dungeon.AuraCfg{Gap: prepullAuraGap, Settle: prepullAuraSettle, ViewWait: prepullAuraViewWait}
	if w.prepullAuraRows == nil || !slices.Contains(w.prepullAuraRows, step) {
		w.prepullAuraRows = append(w.prepullAuraRows, step)
	}
	var targets []prepullAuraTarget
	for _, m := range party.Members {
		if !m.Online || m.Dead || m.GUID == 0 {
			continue
		}
		t := dungeon.AuraTarget{GUID: m.GUID, Name: m.Name}
		u, seen := port.ViewUnit(ctx, t)
		if seen && (u.Dead || u.Ghost) {
			continue
		}
		role := w.prepullRole(m.Name, u.Class)
		list := w.prepullAuras.For(role)
		if len(list) == 0 {
			continue
		}
		rec := w.prepullAuraRecordFor(m.Name, role)
		missing := list
		if seen {
			missing, _ = dungeon.AurasMissing(ctx, port, t, list)
		}
		if len(missing) > 0 {
			if oc, err := dungeon.ApplyAuras(ctx, port, t, missing, cfg, w.env.now); err != nil {
				return err
			} else if oc != dungeon.AuraTaken {
				w.note("prepull-aura: %s: the first .aura (%s) did not land on the bot", m.Name, missing[0].Name)
			}
			for _, b := range missing {
				if !slices.Contains(rec.Sent, b.ID) {
					rec.Sent = append(rec.Sent, b.ID)
				}
			}
		}
		targets = append(targets, prepullAuraTarget{rec: rec, unit: t, want: list})
	}
	// One read-back with a short retry window: the auras sent last need a moment to show in the unit store, and a
	// bot the store does not hold tells nothing (an unproven aura counts as missing).
	var short []string
	for deadline := w.env.now().Add(prepullAuraVerifyWait); ; {
		short = short[:0]
		for i := range targets {
			tg := &targets[i]
			missing, seen := dungeon.AurasMissing(ctx, port, tg.unit, tg.want)
			if !seen {
				missing = tg.want
			}
			tg.rec.Missing = raidwalk.PrepullIDs(missing)
			tg.rec.Seen = subtractIDs(raidwalk.PrepullIDs(tg.want), tg.rec.Missing)
			if len(missing) > prepullAuraMissMax {
				short = append(short, tg.rec.Bot)
			}
		}
		if len(short) == 0 || !w.env.now().Before(deadline) {
			break
		}
		if err := w.env.sleep(ctx, prepullAuraVerifyPoll); err != nil {
			return err
		}
	}
	if err := w.prepullAuraWrite(); err != nil {
		w.note("prepull-aura: WARN: %s could not be written (%v)", prepullAuraFileName, err)
	}
	if len(short) > 0 {
		return stopf(exitRaidWalkFailed, "prepull-aura: boss row %d: %d bot(s) miss more than %d auras (%s): %s",
			step, len(short), prepullAuraMissMax, w.prepullAuraMissSummary(short), prepullAuraFileName)
	}
	w.logLine(w.env.now(), fmt.Sprintf("prepull-aura: boss row %d: %d bot(s) carry their lists", step, len(targets)))
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

// prepullRole is the aura role a bot plays: the walk's own tanks (mt and ot) are "tank", and every other bot takes its
// class's role (raidwalk.ClassAuraRole). "" means only the "all" lists apply.
func (w *walk) prepullRole(name string, class uint8) string {
	if w.mt != "" && strings.EqualFold(name, w.mt) {
		return raidwalk.PrepullTank
	}
	for _, ot := range w.ots {
		if strings.EqualFold(name, ot) {
			return raidwalk.PrepullTank
		}
	}
	return raidwalk.ClassAuraRole(class)
}

// prepullAuraMissSummary names the bots over the miss bound and what each of them lacks.
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
	f := prepullAuraFile{Flag: w.opts.prepullAura, BossRows: w.prepullAuraRows,
		Lists: []prepullAuraList{}, Bots: []prepullAuraRecord{}}
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
// show that selection and sends the line only while it stands (session.GMAura). A refused or failed call breaks the
// walk: a preparation the walk could not send is not a preparation it may report.
func (p walkAuras) SendAura(ctx context.Context, t dungeon.AuraTarget, b dungeon.Buff) error {
	line := fmt.Sprintf(".aura %d", b.ID)
	p.w.logLine(p.w.env.now(), fmt.Sprintf("> %s (gm_aura, selected %s)", line, t.Name))
	params := map[string]any{"guid": t.GUID.String(), "spell_id": b.ID}
	if err := p.w.agent.Call(ctx, "gm_aura", params, nil, 15*time.Second); err != nil {
		return stopf(exitRaidWalkFailed, "prepull-aura: %s on %s: %v", line, t.Name, err)
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
