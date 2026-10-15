// Package tracerun is the recorder a run (a dungeon run, a brain session) writes its trace with: it owns the run id, opens the
// trace file at the start, takes the events and frames the run hands it, and at the end writes the closing record, scores the
// trace with tracecheck, appends the verdicts and writes the <run_id>.summary.json beside it.
//
// A Recorder is nil-safe after New: every method does nothing on a recorder with no file (tracing off, or the file could not be
// opened; ID still answers, because a run has an id with or without a trace).
package tracerun

import (
	"context"
	"fmt"
	"path/filepath"
	"runtime/debug"
	"sync"
	"time"

	"github.com/pjerra/turtle-agent/internal/trace"
	"github.com/pjerra/turtle-agent/internal/tracecheck"
)

// Config is how a run is traced. The zero value writes no file (the run still has an id); the agent's RPC methods turn On by default.
type Config struct {
	RunID      string        // the caller's run id; empty: generated (r-<utc yyyymmddThhmmss>-<4 hex>)
	On         bool          // write a trace file
	Dir        string        // the trace folder (trace.DefaultDir)
	MaxBytes   int64         // trace.DefaultMaxBytes
	FrameEvery time.Duration // trace.DefaultFrameEvery
	Keep       int           // generated-id traces kept in Dir (trace.DefaultKeep); negative keeps all
	Meta       map[string]string
	Agent      trace.Agent // Version is the caller's; SHA defaults to the build's VCS revision
	// Background asks the run to sample frames on a goroutine of its own, so that a long blocked call (a walk) still records. Only
	// for a run on the real clock.
	Background bool
	Thresholds *tracecheck.Config // nil: tracecheck.Defaults
}

// Info is what a finished run reports about its trace (Report.Trace).
type Info struct {
	Path      string        `json:"path,omitempty"`
	Summary   string        `json:"summary,omitempty"`
	Frames    int           `json:"frames"`
	Events    int           `json:"events"`
	Bytes     int64         `json:"bytes"`
	Truncated bool          `json:"truncated,omitempty"`
	Error     string        `json:"error,omitempty"` // why there is no (or no complete) trace
	Verdict   string        `json:"verdict,omitempty"`
	Failed    []string      `json:"failed,omitempty"`
	Checks    []trace.Check `json:"checks,omitempty"`
}

// live holds the recorders with a file open, by run id, so that another part of the agent (the brain, which a dungeon run starts)
// can add its events to the trace of the run it belongs to.
var (
	liveMu sync.Mutex
	live   = map[string]*Recorder{}
)

// Find returns the recorder with a file open for the run id, or nil.
func Find(id string) *Recorder {
	liveMu.Lock()
	defer liveMu.Unlock()
	return live[id]
}

// Recorder writes one run's trace.
type Recorder struct {
	id     string
	cfg    Config
	source string
	mode   string
	now    func() time.Time

	mu     sync.Mutex
	w      *trace.Writer
	start  time.Time
	hdr    trace.Header
	last   time.Time // the last frame taken (Due)
	have   bool
	errTxt string
	done   bool
	stuck  *tracecheck.StuckDetector // writes the stuck and unstuck events as frames come
	names  map[string]string         // guid -> name, for the events
	combat map[string]bool           // guid -> in combat at the last frame
}

// New makes the recorder of a run; it opens nothing until Begin. An empty run id is generated. A run id that is not valid is refused:
// the error says so, and the recorder returned is one with a generated id and no trace file (Finish reports the reason), so the run can
// still go on.
func New(cfg Config, source, mode string, now func() time.Time) (*Recorder, error) {
	if now == nil {
		now = time.Now
	}
	id := cfg.RunID
	var refused error
	switch {
	case id == "":
		id = trace.NewRunID(now())
	case !trace.ValidRunID(id):
		refused = fmt.Errorf("run_id %q is not valid (letters, digits, . _ - ; at most 64): no trace", id)
		id, cfg.On = trace.NewRunID(now()), false
	}
	if cfg.Agent.SHA == "" {
		cfg.Agent.SHA = buildSHA()
	}
	secs := tracecheck.Defaults().StuckSeconds
	if cfg.Thresholds != nil {
		secs = cfg.Thresholds.StuckSeconds
	}
	r := &Recorder{id: id, cfg: cfg, source: source, mode: mode, now: now, stuck: tracecheck.NewStuckDetector(secs), names: map[string]string{}, combat: map[string]bool{}}
	if refused != nil {
		r.errTxt = refused.Error()
	}
	return r, refused
}

func buildSHA() string {
	bi, ok := debug.ReadBuildInfo()
	if !ok {
		return ""
	}
	sha, dirty := "", ""
	for _, s := range bi.Settings {
		switch s.Key {
		case "vcs.revision":
			sha = s.Value
		case "vcs.modified":
			if s.Value == "true" {
				dirty = "-dirty"
			}
		}
	}
	if sha == "" {
		return ""
	}
	return sha + dirty
}

// ID is the run id.
func (r *Recorder) ID() string {
	if r == nil {
		return ""
	}
	return r.id
}

// On says whether the recorder has a file open.
func (r *Recorder) On() bool {
	if r == nil {
		return false
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.w != nil && !r.done
}

// Path is the trace file's path: where it will be written before Begin, empty when the run is not traced (or the file could not be
// opened).
func (r *Recorder) Path() string {
	if r == nil || !r.cfg.On {
		return ""
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.w != nil {
		return r.w.Path()
	}
	if r.errTxt != "" {
		return ""
	}
	dir := r.cfg.Dir
	if dir == "" {
		dir = trace.DefaultDir
	}
	return filepath.Join(dir, r.id+".jsonl")
}

// Background says whether the run should sample on a goroutine of its own.
func (r *Recorder) Background() bool { return r != nil && r.cfg.Background }

// Begin opens the file and writes the header and a party record for each member of h.Party (the recorder fills the scope, the agent
// build, the start time, source and mode; the caller gives plan, lead, caps, options). A file that cannot be opened leaves the recorder
// off with the reason in Info.Error; the run goes on.
func (r *Recorder) Begin(h trace.Header) {
	if r == nil || !r.cfg.On {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.w != nil || r.done {
		return
	}
	w, err := trace.Open(trace.Options{Dir: r.cfg.Dir, RunID: r.id, MaxBytes: r.cfg.MaxBytes, FrameEvery: r.cfg.FrameEvery, Keep: r.cfg.Keep})
	if err != nil {
		r.errTxt = err.Error()
		return
	}
	r.w, r.start = w, r.now()
	liveMu.Lock()
	live[r.id] = r
	liveMu.Unlock()
	h.Source, h.Mode, h.Start, h.StartUnix = r.source, r.mode, r.start.UTC(), r.start.Unix()
	h.AgentVersion, h.AgentSHA = r.cfg.Agent.Version, r.cfg.Agent.SHA
	h.ScopeName = h.Lead
	if h.Requester == "" && r.mode == "group" {
		h.Requester = h.Lead // the agent's own character leads
	}
	if h.Meta == nil {
		h.Meta = r.cfg.Meta
	}
	r.hdr = h
	for _, m := range h.Party {
		r.names[m.G] = m.Name
	}
	w.Header(h)
	r.startSummaryLocked()
}

// Party records a party member the first time it is seen (a member that joins later, or one the header did not know).
func (r *Recorder) Party(m trace.Member) {
	if r == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.w == nil || r.done || m.G == "" {
		return
	}
	r.names[m.G] = m.Name
	r.w.Party(r.ms(), m)
}

// ms is the time since the start, in ms.
func (r *Recorder) ms() int64 { return r.now().Sub(r.start).Milliseconds() }

// Event records an event now.
func (r *Recorder) Event(ev string, d map[string]any) {
	if r == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.w == nil || r.done {
		return
	}
	r.w.Event(r.ms(), ev, d)
	switch ev {
	case "walk_start":
		r.stuck.SetWalking(true)
	case "walk_end":
		r.stuck.SetWalking(false)
	}
}

// Due says whether a frame is due: it is true once per frame interval, so a caller can skip the work of building one.
func (r *Recorder) Due() bool {
	if r == nil {
		return false
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.w == nil || r.done {
		return false
	}
	every := r.cfg.FrameEvery
	if every <= 0 {
		every = trace.DefaultFrameEvery
	}
	return !r.have || r.now().Sub(r.last) >= every*8/10
}

// Frame records a frame now (the recorder sets t). It also writes the mobs records of hostiles seen for the first time, the
// combat_on and combat_off events of members whose combat state changed, and the stuck and unstuck events.
func (r *Recorder) Frame(f trace.Frame) {
	if r == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.w == nil || r.done {
		return
	}
	f.T = r.ms()
	if r.w.Frame(f) {
		r.last, r.have = r.now(), true
	}
	for _, m := range f.Mobs {
		r.w.Mob(f.T, trace.MobInfo{G: m.G, Entry: m.Entry, Name: m.Name, Level: m.Level})
	}
	for _, b := range f.Bots {
		if was, ok := r.combat[b.G]; ok && was != b.Combat {
			ev := "combat_on"
			if !b.Combat {
				ev = "combat_off"
			}
			r.w.Event(f.T, ev, map[string]any{"a": b.G, "name": r.names[b.G]})
		}
		r.combat[b.G] = b.Combat
	}
	for _, e := range r.stuck.Feed(f) {
		r.w.Event(e.T, e.Ev, e.Data(r.names[e.G]))
	}
}

// Sampler calls take every frame interval on its own goroutine until ctx ends (for a run on the real clock). The returned func
// stops it and waits.
func (r *Recorder) Sampler(ctx context.Context, take func()) (stop func()) {
	if r == nil || !r.cfg.Background {
		return func() {}
	}
	every := r.cfg.FrameEvery
	if every <= 0 {
		every = trace.DefaultFrameEvery
	}
	ctx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() {
		defer close(done)
		t := time.NewTicker(every)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				take()
			}
		}
	}()
	return func() { cancel(); <-done }
}

// Finish ends the trace: the closing record, the scoring, the verdicts, the summary. counters are the run's totals for the end
// record and the summary. It is called once; a second call returns the first one's Info. The file is parsed once, outside the
// recorder's lock (a trace can be tens of MiB): the end record is written under the lock, which also stops further records.
func (r *Recorder) Finish(state, errText string, counters map[string]float64) *Info {
	if r == nil {
		return nil
	}
	r.mu.Lock()
	if r.w == nil {
		r.mu.Unlock()
		return &Info{Error: r.errTxt}
	}
	if r.done {
		r.mu.Unlock()
		return &Info{Path: r.w.Path()}
	}
	r.done = true
	liveMu.Lock()
	if live[r.id] == r {
		delete(live, r.id)
	}
	liveMu.Unlock()
	end := r.now()
	secs := end.Sub(r.start).Seconds()
	cs := map[string]any{}
	for k, v := range counters {
		cs[k] = v
	}
	r.w.End(trace.End{Envelope: trace.Envelope{T: r.ms()}, Reason: state, Error: errText, Seconds: secs, Ended: end.UTC(), Counters: cs})
	w, thresholds := r.w, r.cfg.Thresholds
	r.mu.Unlock()

	info := &Info{Path: w.Path(), Summary: trace.SummaryPath(w.Path())}
	var checks []trace.Check
	f, err := trace.Read(w.Path())
	if err != nil {
		info.Error = "scoring: " + err.Error()
	} else {
		cfg := tracecheck.Defaults()
		if thresholds != nil {
			cfg = *thresholds
		}
		checks = tracecheck.Score(f, cfg)
		w.Oracles(checks)
		if f.End != nil {
			info.Frames, info.Events = int(f.End.Counter("frames")), int(f.End.Counter("events"))
		}
	}
	if err := w.Err(); err != nil {
		info.Error = "writing: " + err.Error()
	}
	info.Bytes, info.Truncated = w.Bytes(), w.Truncated()
	_ = w.Close()
	info.Verdict, info.Failed = trace.Verdict(checks)
	info.Checks = checks
	if f != nil {
		if err := trace.WriteSummary(w.Path(), trace.SummaryOf(f, info.Bytes, checks, r.now())); err != nil && info.Error == "" {
			info.Error = "summary: " + err.Error()
		}
	}
	r.mu.Lock()
	if info.Error == "" {
		info.Error = r.errTxt
	}
	r.mu.Unlock()
	return info
}

// startSummaryLocked writes the summary file of a run that has just begun (status running).
func (r *Recorder) startSummaryLocked() {
	f, err := trace.Read(r.w.Path())
	if err != nil {
		if r.errTxt == "" {
			r.errTxt = "summary: " + err.Error()
		}
		return
	}
	if err := trace.WriteSummary(r.w.Path(), trace.SummaryOf(f, r.w.Bytes(), nil, r.now())); err != nil && r.errTxt == "" {
		r.errTxt = fmt.Sprintf("summary: %v", err)
	}
}
