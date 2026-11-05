// raid_walk_instancedata.go: the Hakkar priest state of a placed walk (HAKKAR 2.4 T2; spec hakkar-mechanics 3.0).
//
// A living High Priest wears Hakkar Power (24692) on Hakkar, and the run leader's pull gate refuses a Hakkar pull with
// any stack and ends the run after 30 s (`boss gate:`). Walk 38 left Arlokk (script-summoned, no spawn row for
// --gm-clear) and Thekal (a phase-1 `.die` is a fake death: he revives as 14509) undead, so the raid never got to pull.
// Two test-server tools close that hole:
//
//   - --gm-instance-data sets saved instance data by GM command after --gm-clear and before the first boss pull, in the
//     flag order, then `.debug instancedata 0 0` once: only SetData(0, 0) recomputes Hakkar Power
//     (instance_zulgurub.cpp:61-98, boss_hakkar.cpp:276-285). Every line's answer goes into run.log.
//   - when the plan's next boss is Hakkar (14834), the walk reads `.tt q inst` before the raid moves and refuses
//     (exit 18) unless saved fields 0-4 are all 3 (DONE): an instance with a living priest cannot give a real fight.
//     --allow-priests-alive skips the refusal (a deliberate walk-32/33 style repro).
//
// Both are test servers only (the --place allow-list) and both need --place.
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	"github.com/pjerra/turtle-agent/internal/events"
	"github.com/pjerra/turtle-agent/internal/planexport"
	"github.com/pjerra/turtle-agent/internal/raidwalk"
	"github.com/pjerra/turtle-agent/internal/tbm"
)

const (
	// hakkarEntry is Hakkar the Soulflayer (ZG 309), the boss the priest-state refusal guards.
	hakkarEntry = 14834
	// hakkarPriests is how many saved fields the five High Priests own: Arlokk, Jeklik, Venoxis, Mar'li and Thekal
	// (instance_zulgurub.cpp's m_auiEncounter[0..4]).
	hakkarPriests = 5
	// hakkarPriestDone is TYPE_*_DONE: a priest whose saved field is not this one is alive.
	hakkarPriestDone = 3
	// instanceDataReplyWaitMS is how long one .debug instancedata line is given for its answer: the core's handler
	// prints nothing on success (Commands.cpp:17778), so this only catches a refusal line.
	instanceDataReplyWaitMS = 500
)

// instanceDataPair is one --gm-instance-data value: the saved field and the value it is set to.
type instanceDataPair struct {
	Type  uint32
	Value uint32
}

// parseInstanceDataSpecs reads every --gm-instance-data value ("<type>=<value>"), keeping the flag order. Type 0 is
// refused: it is the walk's own refresh line (`.debug instancedata 0 0`), which would silently rewrite field 0.
func parseInstanceDataSpecs(vals []string) ([]instanceDataPair, error) {
	out := make([]instanceDataPair, 0, len(vals))
	for _, v := range vals {
		typ, val, ok := strings.Cut(v, "=")
		if !ok {
			return nil, fmt.Errorf("--gm-instance-data %q: want \"<type>=<value>\", e.g. 1=3", v)
		}
		t, err := strconv.ParseUint(strings.TrimSpace(typ), 10, 32)
		if err != nil {
			return nil, fmt.Errorf("--gm-instance-data %q: the type is not a number", v)
		}
		x, err := strconv.ParseUint(strings.TrimSpace(val), 10, 32)
		if err != nil {
			return nil, fmt.Errorf("--gm-instance-data %q: the value is not a number", v)
		}
		if t == 0 {
			return nil, fmt.Errorf("--gm-instance-data %q: type 0 is the walk's own refresh line (.debug instancedata 0 0); name the field, e.g. 1=3", v)
		}
		out = append(out, instanceDataPair{Type: uint32(t), Value: uint32(x)})
	}
	return out, nil
}

// nextBossRow is the first boss row at or after start (1-based, as the walk's step numbers are): the plan's next boss.
func nextBossRow(rows planexport.Rows, start int) (planexport.StepRow, bool) {
	if start < 1 {
		start = 1
	}
	for i := start - 1; i < len(rows.Steps); i++ {
		if rows.Steps[i].Kind == planexport.KindBoss {
			return rows.Steps[i], true
		}
	}
	return planexport.StepRow{}, false
}

// placeInstanceData is place step 6c: the --gm-instance-data lines (each with its answer logged), then the Hakkar
// priest refusal. It runs after --gm-clear/--gm-clear-boss and before the corridor's quiet and the raid's move, so
// nothing has pulled a boss yet. It is a no-op for a plan whose next boss is not Hakkar and a walk with no flags.
func (w *walk) placeInstanceData(ctx context.Context) error {
	pairs, err := parseInstanceDataSpecs(w.opts.gmInstanceData)
	if err != nil {
		return stopf(exitUsage, "%v", err)
	}
	for _, p := range pairs {
		if err := w.instanceDataLine(ctx, p.Type, p.Value); err != nil {
			return err
		}
	}
	if len(pairs) > 0 {
		// The refresh: SetData(0, 0) is what makes the core recompute Hakkar Power's stacks from the priest fields
		// (boss_hakkar.cpp:276-285). Without it the walk's field writes leave Hakkar wearing the old stacks.
		if err := w.instanceDataLine(ctx, 0, 0); err != nil {
			return err
		}
		w.note("instance-data: %d flag(s) and the 0 0 refresh sent by GM command (test tool; the walk is ASSISTED)", len(pairs))
	}
	return w.checkHakkarPriests(ctx)
}

// instanceDataLine sends one `.debug instancedata <type> <value>` and logs every system line the server answers with
// (the handler is silent on success; a refusal, if any, is what this shows).
func (w *walk) instanceDataLine(ctx context.Context, typ, value uint32) error {
	line := fmt.Sprintf(".debug instancedata %d %d", typ, value)
	if err := w.gm(ctx, line); err != nil {
		return err
	}
	evs, err := w.pumpEvents(ctx, instanceDataReplyWaitMS)
	if err != nil {
		return err
	}
	for _, e := range evs {
		if tl, ok := tbm.Line(e); ok {
			w.logLine(w.env.now(), tl)
			continue
		}
		if e.Kind != events.KindSystem || e.Text == "" {
			continue
		}
		text := raidwalk.CleanLine(e.Text)
		w.logLine(w.env.now(), fmt.Sprintf("%s: %s", line, text))
	}
	return nil
}

// checkHakkarPriests is the spec hakkar-mechanics 3.0 refusal (never implemented before): when the plan's next boss is
// Hakkar, the instance's saved fields 0-4 must all be DONE. The check is a read of the toolkit's own state (`.tt q
// inst`), so it costs no GM line and shows the same numbers an operator checks by hand.
func (w *walk) checkHakkarPriests(ctx context.Context) error {
	if w.place == nil {
		return nil
	}
	boss, ok := nextBossRow(w.rows, w.place.Start)
	if !ok || boss.Entry != hakkarEntry {
		return nil
	}
	if w.opts.allowPriestsAlive {
		w.note("hakkar: --allow-priests-alive: the five High Priests' state is not checked (the walk-32/33 repro; Hakkar may wear Hakkar Power when the raid pulls)")
		return nil
	}
	fields, ok, err := w.ttInstSaved(ctx)
	if err != nil {
		return err
	}
	if !ok {
		return stopf(exitRaidWalkFailed, "hakkar: the instance data is unreadable (.tt q inst named no saved fields): cannot prove the five High Priests are DONE")
	}
	alive := false
	for i := range hakkarPriests {
		if i >= len(fields) || fields[i] != hakkarPriestDone {
			alive = true
		}
	}
	if alive {
		return stopf(exitRaidWalkFailed, "hakkar: priests alive: fields %s: Hakkar would wear Hakkar Power and the pull gate would end the run; kill the priests first, or pass --allow-priests-alive for a deliberate repro", savedWords(fields))
	}
	w.note("hakkar: the five High Priests are DONE in this instance (saved %s)", savedWords(fields))
	return nil
}

// ttInstSaved reads the instance's saved-data words from `q inst` (the toolkit's `saved` field). ok is false when the
// reply carries none: the walk then cannot tell which priests are alive.
func (w *walk) ttInstSaved(ctx context.Context) ([]int, bool, error) {
	out, err := w.tt(ctx, "q inst")
	if err != nil {
		return nil, false, stopf(exitRaidWalkFailed, "hakkar: q inst: %v", err)
	}
	body, ok := ttLast(out)
	if !ok {
		return nil, false, nil
	}
	raw, ok := body.raw["saved"]
	if !ok {
		return nil, false, nil
	}
	var text string
	if err := json.Unmarshal(raw, &text); err == nil {
		words := strings.Fields(text)
		fields := make([]int, 0, len(words))
		for _, word := range words {
			n, err := strconv.Atoi(word)
			if err != nil {
				return nil, false, nil
			}
			fields = append(fields, n)
		}
		return fields, true, nil
	}
	var nums []int
	if err := json.Unmarshal(raw, &nums); err == nil {
		return nums, true, nil
	}
	return nil, false, nil
}

// savedWords is the saved field list as one space-separated word list ("3 3 3 3 0"), the form .tt q inst prints.
func savedWords(fields []int) string {
	words := make([]string, 0, len(fields))
	for _, f := range fields {
		words = append(words, strconv.Itoa(f))
	}
	return strings.Join(words, " ")
}
