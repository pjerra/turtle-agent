package main

import (
	"fmt"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/pjerra/turtle-agent/internal/brain"
	"github.com/pjerra/turtle-agent/internal/events"
	"github.com/pjerra/turtle-agent/internal/rpc"
	"github.com/pjerra/turtle-agent/internal/trace"
	"github.com/pjerra/turtle-agent/internal/tracerun"
)

// tracesDirEnv names the folder run traces go to when the settings do not say (traces_dir); "traces" under the working directory
// when neither does.
const tracesDirEnv = "TURTLE_AGENT_TRACES_DIR"

// maxTraceMeta bounds the free-form header text a caller may add.
const (
	maxTraceMeta     = 16
	maxTraceMetaText = 200
)

func tracesDir(setting string) string {
	switch {
	case setting != "":
		return setting
	case os.Getenv(tracesDirEnv) != "":
		return os.Getenv(tracesDirEnv)
	}
	return trace.DefaultDir
}

// traceConfig reads dungeon_run's run_id, trace and trace_meta into the run's trace configuration. The trace is on unless trace is
// false; the run id is the caller's or a generated one.
func (r *dungeonRunner) traceConfig(p dungeonRunParams) (tracerun.Config, error) {
	bad := func(format string, a ...any) error {
		return &rpc.Error{Code: rpc.CodeInvalidParams, Message: fmt.Sprintf(format, a...)}
	}
	cfg := tracerun.Config{On: p.Trace == nil || *p.Trace, Dir: tracesDir(r.tracesDir), Agent: trace.Agent{Version: version}, Background: true}
	switch {
	case p.RunID == "":
		cfg.RunID = trace.NewRunID(time.Now())
	case !trace.ValidRunID(p.RunID):
		return cfg, bad("run_id must be letters, digits, dot, underscore or dash, at most 64 characters, starting with a letter or digit")
	default:
		cfg.RunID = p.RunID
	}
	if len(p.TraceMeta) > maxTraceMeta {
		return cfg, bad("trace_meta: at most %d entries", maxTraceMeta)
	}
	for k, v := range p.TraceMeta {
		if k == "" || len(k) > maxTraceMetaText || len(v) > maxTraceMetaText {
			return cfg, bad("trace_meta: keys and values need 1-%d characters", maxTraceMetaText)
		}
	}
	cfg.Meta = p.TraceMeta
	if p.RunMeta != nil {
		if msg := p.RunMeta.Check(); msg != "" {
			return cfg, bad("%s", msg)
		}
	}
	return cfg, nil
}

// brainTrace is the trace of a brain session started by hand (brain_start). The brain's events carry the session's run id
// (data.run_id); a session that a dungeon run started adds its events to the run's trace instead (tracerun.Find).
type brainTrace struct {
	dir string

	mu       sync.Mutex
	rec      *tracerun.Recorder // the standalone session's recorder, nil when none
	counters map[string]float64
}

// begin opens the trace of a brain session that brain_start has just started.
func (b *brainTrace) begin(runID string, p brainStartParams, self string, selfGUID uint64, st brain.Status) {
	cfg := tracerun.Config{On: p.Trace == nil || *p.Trace, Dir: tracesDir(b.dir), RunID: runID, Meta: p.TraceMeta, Agent: trace.Agent{Version: version}}
	rec, _ := tracerun.New(cfg, "brain", "", time.Now) // runID was checked by brain_start; an invalid one would only leave the session untraced
	opts := map[string]any{"strategies": st.Strategies, "role": st.Role, "no_move": st.NoMove}
	if p.Rotation != "" {
		if label, err := brainLabel("", p.Rotation); err == nil {
			opts["rotation"] = strings.TrimPrefix(label, "+rot:")
		}
	}
	if st.Tank != "" {
		opts["tank"] = st.Tank
	}
	rec.Begin(trace.Header{Lead: self, Tank: st.Tank, Party: []trace.Member{{G: fmt.Sprintf("0x%x", selfGUID), Name: self, Lead: true, Role: brainRole(st.Role)}}, Options: opts, Caps: []string{}})
	rec.Event("brain_state", map[string]any{"state": "started", "strategies": st.Strategies, "role": st.Role})
	b.mu.Lock()
	b.rec, b.counters = rec, map[string]float64{}
	b.mu.Unlock()
}

// finish ends the standalone session's trace if it is still open: the brain stopped before begin ran, so its "stopped" event found no
// recorder to finish.
func (b *brainTrace) finish(runID string) {
	b.mu.Lock()
	rec, counters := b.rec, b.counters
	if rec == nil || rec.ID() != runID {
		b.mu.Unlock()
		return
	}
	b.rec = nil
	b.mu.Unlock()
	rec.Event("brain_state", map[string]any{"state": "stopped"})
	rec.Finish("stopped", "", counters)
}

// event is called with every event the brain emits.
func (b *brainTrace) event(e events.Event) {
	id, _ := e.Data["run_id"].(string)
	if id == "" || (e.Kind != events.KindBrainDecision && e.Kind != events.KindBrainState) {
		return
	}
	rec := tracerun.Find(id)
	if rec == nil {
		return
	}
	if e.Kind == events.KindBrainState {
		state, _ := e.Data["state"].(string)
		bs := map[string]any{"state": state, "strategies": e.Data["strategies"], "role": e.Data["role"]}
		if enc, ok := e.Data["encounter"]; ok {
			bs["encounter"] = enc
		}
		rec.Event("brain_state", bs)
		b.mu.Lock()
		own := b.rec == rec
		if own && state == "stopped" {
			b.rec = nil
		}
		counters := b.counters
		b.mu.Unlock()
		if own && state == "stopped" {
			rec.Finish("stopped", "", counters)
		}
		return
	}
	d := map[string]any{"action": e.Text, "spell_id": e.SpellID}
	for _, k := range []string{"trigger", "relevance", "result", "why", "via", "error"} {
		if v, ok := e.Data[k]; ok {
			d[k] = v
		}
	}
	if e.TargetGUID != 0 {
		d["target"] = e.TargetGUID.String()
	}
	b.mu.Lock()
	if b.rec == rec {
		b.counters["decisions"]++
		if r, _ := e.Data["result"].(string); r == "ok" {
			b.counters["acted"]++
		}
	}
	b.mu.Unlock()
	rec.Event("brain", d)
}

// brainRole is the role in the server's words: tank, heal or dps.
func brainRole(r string) string {
	if r == "healer" {
		return "heal"
	}
	return r
}
