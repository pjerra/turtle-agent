package dungeon

import (
	"bytes"
	"crypto/sha256"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"strings"
	"sync"
	"time"
)

// Live tuning in the remote driver (tune.go has the tunables and the limits). The driver looks at the tunables file, the plan file and
// the `tune_set` requests at two places only: before a leg starts and before a pull group starts (liveStep, called from the run loop
// between two steps). What it finds applies to the NEXT step: nothing changes in the middle of a walk, a fight or a wait. A file that
// does not parse or breaks a limit changes nothing and is reported once; the values and the plan the run has stay.

// tuneBoundaries name the two places.
const (
	boundaryLeg  = "leg_start"
	boundaryPull = "pull_group_start"
)

// maxLiveRecords bounds the tune changes and plan reloads one report keeps.
const maxLiveRecords = 300

// fileStamp is what tells that a file may have changed since it was last read.
type fileStamp struct {
	mod  time.Time
	size int64
}

func stampOf(path string) (fileStamp, error) {
	fi, err := os.Stat(path)
	if err != nil {
		return fileStamp{}, err
	}
	return fileStamp{fi.ModTime(), fi.Size()}, nil
}

// pendingTune is a tune_set request waiting for the next boundary.
type pendingTune struct {
	key   string
	val   float64
	reset bool
}

// liveTune is the driver's live-tuning state. mu guards all of it: tune_set is called from the RPC goroutine, the rest from the run loop.
type liveTune struct {
	mu   sync.Mutex
	base map[string]float64 // the values the driver was made with: what a key falls back to
	cur  map[string]float64 // the values the driver has now (a copy for tune_list: the driver's own fields belong to the run loop)
	file map[string]float64 // the last good tunables file
	rpc  map[string]float64 // the values tune_set put
	pend []pendingTune

	tunePath  string
	tuneStamp fileStamp
	tuneGone  bool
	planPath  string
	planStamp fileStamp
	planHash  [sha256.Size]byte
}

func (d *RemoteDriver) tuneTarget() *tuneTarget {
	return &tuneTarget{cfg: &d.cfg, opt: &d.opt, x: &d.tx}
}

// DefaultTunePath is the tunables file a plan file has when none is named: next to it, <plan>.tune.json.
func DefaultTunePath(planPath string) string {
	if planPath == "" {
		return ""
	}
	return strings.TrimSuffix(planPath, ".json") + ".tune.json"
}

// initLive is called once, from NewRemoteDriver.
func (d *RemoteDriver) initLive() {
	lt := &d.live
	lt.base = readTarget(d.tuneTarget())
	lt.cur = readTarget(d.tuneTarget())
	lt.file, lt.rpc = map[string]float64{}, map[string]float64{}
	if d.opt.NoReload {
		return
	}
	if d.opt.PlanPath != "" {
		if data, err := os.ReadFile(d.opt.PlanPath); err == nil {
			lt.planHash = sha256.Sum256(data)
			lt.planStamp, _ = stampOf(d.opt.PlanPath)
		}
		lt.tunePath = d.opt.TunePath
		if lt.tunePath == "" {
			lt.tunePath = DefaultTunePath(d.opt.PlanPath)
		}
	} else {
		lt.tunePath = d.opt.TunePath
	}
}

// liveBoundary is where a leg or a pull group is about to start: leg is the leg, planned says that the plan may be reloaded here.
type liveBoundary struct {
	leg   int
	kind  string
	group int // a pull group start: the index of the group
	// mid says the leg's start is merged as a pull-group start (the stepper is past the leg's walk); noPlan that the plan is not reloaded
	// at all (the stepper is past the pull groups).
	mid, noPlan bool
}

// liveStep is called by the run loop with the stepper's next action. At a leg start or a pull-group start it applies the pending
// changes and returns the action again if the plan changed.
func (d *RemoteDriver) liveStep(a Action) Action {
	var b liveBoundary
	switch {
	case a.Kind == ActDone || a.Kind == ActFailed:
		return a
	case a.Leg != d.curLeg:
		b = liveBoundary{leg: a.Leg, kind: boundaryLeg}
		// A leg whose first points the stepper has already passed (the party stood at its end, a resumed run, a run started at a pull
		// group) is merged like a leg inside its pull groups: what is behind the stepper stays.
		switch _, ph, idx := d.st.Position(); {
		case ph == "pull_groups":
			b.mid, b.group = true, idx
		case ph != "walk":
			b.mid, b.noPlan = true, true
		}
	case a.Kind == ActPull:
		_, _, idx := d.st.Position()
		b = liveBoundary{leg: a.Leg, kind: boundaryPull, group: idx}
	default:
		return a
	}
	d.applyTunables(b)
	if d.reloadPlan(b) {
		if b.kind == boundaryLeg && !b.mid && !d.cfg.Resume {
			d.st.RestartLeg() // nothing of the leg was done: it starts again from its first point, on the new plan (a resumed run keeps what it skipped)
		}
		return d.st.Next()
	}
	return a
}

func layered(base, file, rpc map[string]float64) map[string]float64 {
	out := make(map[string]float64, len(base))
	for k, v := range base {
		out[k] = v
	}
	for k, v := range file {
		out[k] = v
	}
	for k, v := range rpc {
		out[k] = v
	}
	return out
}

func copyVals(m map[string]float64) map[string]float64 { return layered(nil, m, nil) }

// applyTunables reads the file, takes the tune_set requests and sets the driver's values to what the layers say: tune_set over the file
// over what the driver was made with.
func (d *RemoteDriver) applyTunables(b liveBoundary) {
	lt := &d.live
	lt.mu.Lock()
	defer lt.mu.Unlock()
	now := d.clk.Now()
	d.readTuneFile(b, now)
	for _, p := range lt.pend {
		cand := copyVals(lt.rpc)
		if p.reset {
			delete(cand, p.key)
		} else {
			cand[p.key] = p.val
		}
		if err := checkTuneSet(layered(lt.base, lt.file, cand)); err != nil {
			d.tuneRefused(b, now, "rpc", "", fmt.Errorf("%s: %w", p.key, err))
			continue
		}
		lt.rpc = cand
	}
	lt.pend = nil
	want := layered(lt.base, lt.file, lt.rpc)
	t := d.tuneTarget()
	for _, s := range tuneSpecs {
		if want[s.Key] == lt.cur[s.Key] {
			continue // a key nothing touches is not written back (a duration is kept to the millisecond by the tunables)
		}
		old := s.get(t)
		s.set(t, want[s.Key])
		nw := s.get(t)
		lt.cur[s.Key] = nw
		if nw == old {
			continue
		}
		src := "reset"
		if _, ok := lt.rpc[s.Key]; ok {
			src = "rpc"
		} else if _, ok := lt.file[s.Key]; ok {
			src = "file"
		}
		d.tuneChanged(TuneChange{Key: s.Key, Old: old, New: nw, Source: src, At: now, Leg: b.leg, Boundary: b.kind})
	}
}

func (d *RemoteDriver) tuneChanged(c TuneChange) {
	d.remote(func(r *RemoteReport) {
		if len(r.TuneChanges) < maxLiveRecords {
			r.TuneChanges = append(r.TuneChanges, c)
		}
	})
	data := map[string]any{"ok": true, "key": c.Key, "old": c.Old, "new": c.New, "source": c.Source, "at": c.At.UTC().Format(time.RFC3339Nano),
		"leg": c.Leg, "boundary": c.Boundary}
	d.tr.Event("tune_change", data)
	d.rep.note(fmt.Sprintf("tune: %s %v -> %v (%s, before %s of leg %d)", c.Key, c.Old, c.New, c.Source, strings.ReplaceAll(c.Boundary, "_", " "), c.Leg))
}

// tuneRefused records a tunables file or a tune_set request that was not taken.
func (d *RemoteDriver) tuneRefused(b liveBoundary, now time.Time, source, file string, err error) {
	data := map[string]any{"ok": false, "source": source, "error": err.Error(), "at": now.UTC().Format(time.RFC3339Nano), "leg": b.leg, "boundary": b.kind}
	if file != "" {
		data["file"] = file
	}
	d.tr.Event("tune_change", data)
	d.rep.note(fmt.Sprintf("tune: %s refused, the old values stay: %v", source, err))
}

// readTuneFile reads the tunables file when its time or size changed. A bad file is reported once (its stamp is kept) and changes nothing.
func (d *RemoteDriver) readTuneFile(b liveBoundary, now time.Time) {
	lt := &d.live
	if lt.tunePath == "" || d.opt.NoReload {
		return
	}
	st, err := stampOf(lt.tunePath)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		if (lt.tuneStamp != fileStamp{}) && !lt.tuneGone {
			lt.tuneGone = true
			d.tuneRefused(b, now, "file", lt.tunePath, errors.New("the file is gone"))
		}
		return
	case err != nil:
		if st != lt.tuneStamp {
			lt.tuneStamp = st
			d.tuneRefused(b, now, "file", lt.tunePath, err)
		}
		return
	case st == lt.tuneStamp && !lt.tuneGone:
		return
	}
	lt.tuneStamp, lt.tuneGone = st, false
	data, err := os.ReadFile(lt.tunePath)
	var vals map[string]float64
	if err == nil {
		vals, err = ParseTuneFile(data)
	}
	if err == nil {
		err = checkTuneSet(layered(lt.base, vals, lt.rpc))
	}
	if err != nil {
		d.tuneRefused(b, now, "file", lt.tunePath, err)
		return
	}
	lt.file = vals
}

// TuneAck is the answer to a tune_set.
type TuneAck struct {
	Key     string  `json:"key"`
	Old     float64 `json:"old"` // the value the driver has now
	New     float64 `json:"new"` // the value it takes at the next boundary
	Applies string  `json:"applies"`
}

// TuneSet asks for a one-off change from outside the run (the tune_set RPC): value is a number, or "default" to put the key back to
// what the tunables file (or the driver) has. It is checked now against the same limits as a file, and taken, with the same events,
// at the next leg start or pull-group start. It may be called while the run goes on.
func (d *RemoteDriver) TuneSet(key string, value any) (TuneAck, error) {
	s, ok := tuneIndex[key]
	if !ok {
		return TuneAck{}, tuneErr("unknown tunable %q (tune_list lists them)", key)
	}
	lt := &d.live
	lt.mu.Lock()
	defer lt.mu.Unlock()
	p := pendingTune{key: key}
	switch v := value.(type) {
	case float64:
		p.val = v
	case string:
		if v != "default" && v != "reset" {
			return TuneAck{}, tuneErr("%s: value must be a number or \"default\"", key)
		}
		p.reset = true
	case nil:
		p.reset = true
	default:
		return TuneAck{}, tuneErr("%s: value must be a number or \"default\"", key)
	}
	// The rpc layer as it will be once the earlier requests are in, for the check.
	rpc := copyVals(lt.rpc)
	for _, q := range lt.pend {
		if q.reset {
			delete(rpc, q.key)
		} else {
			rpc[q.key] = q.val
		}
	}
	if p.reset {
		delete(rpc, key)
	} else {
		if err := s.check(p.val); err != nil {
			return TuneAck{}, &TuneError{Msg: err.Error()}
		}
		rpc[key] = p.val
	}
	want := layered(lt.base, lt.file, rpc)
	if err := checkTuneSet(want); err != nil {
		return TuneAck{}, &TuneError{Msg: err.Error()}
	}
	lt.pend = append(lt.pend, p)
	return TuneAck{Key: key, Old: lt.cur[key], New: want[key], Applies: "at the next leg start or pull-group start"}, nil
}

// TuneInfo is one line of tune_list.
type TuneInfo struct {
	Key     string   `json:"key"`
	Unit    string   `json:"unit"`
	Min     float64  `json:"min"`
	Max     float64  `json:"max"`
	Default float64  `json:"default"`
	Base    float64  `json:"base"`  // what the driver was made with
	Value   float64  `json:"value"` // what it has now
	Source  string   `json:"source"`
	Queued  *float64 `json:"queued,omitempty"` // a tune_set waiting for the next boundary
	Doc     string   `json:"doc"`
}

// TuneList lists the tunables with their limits and values.
func (d *RemoteDriver) TuneList() []TuneInfo {
	lt := &d.live
	lt.mu.Lock()
	defer lt.mu.Unlock()
	def := TuneDefaults()
	queued := map[string]*float64{}
	for _, p := range lt.pend {
		if p.reset {
			queued[p.key] = nil
			continue
		}
		v := p.val
		queued[p.key] = &v
	}
	out := make([]TuneInfo, 0, len(tuneSpecs))
	for _, s := range tuneSpecs {
		src := "base"
		if _, ok := lt.rpc[s.Key]; ok {
			src = "rpc"
		} else if _, ok := lt.file[s.Key]; ok {
			src = "file"
		}
		out = append(out, TuneInfo{Key: s.Key, Unit: s.Unit, Min: s.Min, Max: s.Max, Default: def[s.Key], Base: lt.base[s.Key], Value: lt.cur[s.Key],
			Source: src, Queued: queued[s.Key], Doc: s.Doc})
	}
	return out
}

// reloadPlan reads the plan file again when it changed and takes over what a reload may change (mergeReload). It reports whether the
// plan the run holds changed. A file that does not parse, or has another shape, changes nothing and is reported once.
func (d *RemoteDriver) reloadPlan(b liveBoundary) bool {
	lt := &d.live
	path := d.opt.PlanPath
	if path == "" || d.opt.NoReload || b.noPlan {
		return false
	}
	lt.mu.Lock()
	defer lt.mu.Unlock()
	st, err := stampOf(path)
	if err != nil || st == lt.planStamp {
		return false
	}
	lt.planStamp = st
	data, err := os.ReadFile(path)
	if err != nil {
		d.planRefused(b, path, err)
		return false
	}
	h := sha256.Sum256(data)
	if bytes.Equal(h[:], lt.planHash[:]) {
		return false
	}
	lt.planHash = h
	next, err := Parse(path, data)
	if err != nil {
		d.planRefused(b, path, err)
		return false
	}
	started := b.kind == boundaryPull || b.mid
	merged, ignored, err := mergeReload(d.plan, next, b.leg, started, b.group)
	if err != nil {
		d.planRefused(b, path, err)
		return false
	}
	ops := planDiff(d.plan, merged)
	if len(ops) == 0 && len(ignored) == 0 {
		return false
	}
	d.plan.Notes = merged.Notes
	copy(d.plan.Legs, merged.Legs)
	rec := PlanReload{OK: true, At: d.clk.Now(), Leg: b.leg, Boundary: b.kind, File: path, Ops: ops, Ignored: ignored}
	d.remote(func(r *RemoteReport) {
		if len(r.PlanReloads) < maxLiveRecords {
			r.PlanReloads = append(r.PlanReloads, rec)
		}
	})
	data2 := map[string]any{"ok": true, "file": path, "at": rec.At.UTC().Format(time.RFC3339Nano), "leg": b.leg, "boundary": b.kind, "ops": ops}
	if len(ignored) > 0 {
		data2["ignored"] = ignored
	}
	d.tr.Event("plan_reload", data2)
	d.rep.note(fmt.Sprintf("plan reload before %s of leg %d: %d change(s) taken%s", strings.ReplaceAll(b.kind, "_", " "), b.leg, len(ops), ignoredText(ignored)))
	return len(ops) > 0
}

func ignoredText(ignored []string) string {
	if len(ignored) == 0 {
		return ""
	}
	if len(ignored) > 6 {
		return fmt.Sprintf(", %d not taken (%s, ...): the run's legs done and groups cleared stay as they were", len(ignored), strings.Join(ignored[:6], ", "))
	}
	return fmt.Sprintf(", not taken: %s (the run's legs done and groups cleared stay as they were)", strings.Join(ignored, ", "))
}

func (d *RemoteDriver) planRefused(b liveBoundary, path string, err error) {
	rec := PlanReload{OK: false, At: d.clk.Now(), Leg: b.leg, Boundary: b.kind, File: path, Error: err.Error()}
	d.remote(func(r *RemoteReport) {
		if len(r.PlanReloads) < maxLiveRecords {
			r.PlanReloads = append(r.PlanReloads, rec)
		}
	})
	d.tr.Event("plan_reload", map[string]any{"ok": false, "file": path, "at": rec.At.UTC().Format(time.RFC3339Nano), "leg": b.leg, "boundary": b.kind, "error": err.Error()})
	d.rep.note(fmt.Sprintf("plan reload refused, the run's plan stays: %v", firstLine(err.Error())))
}

func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i] + " (...)"
	}
	return s
}
