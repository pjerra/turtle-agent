package trace

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// Defaults of the writer.
const (
	DefaultDir        = "traces"
	DefaultMaxBytes   = 32 << 20 // frames stop at this size; events go on to a quarter more; the end and the oracles are always written
	DefaultFrameEvery = time.Second
	DefaultKeep       = 200 // files of generated run ids kept in the folder
	eventReserve      = 4   // events stop at MaxBytes + MaxBytes/eventReserve
	maxMobRecords     = 1024
)

// Options of a Writer.
type Options struct {
	Dir        string        // the folder (DefaultDir)
	RunID      string        // names the file: <Dir>/<RunID>.jsonl
	MaxBytes   int64         // DefaultMaxBytes
	FrameEvery time.Duration // the least time between two frames (DefaultFrameEvery); it triples once half of MaxBytes is used
	Keep       int           // generated-id files kept in Dir (DefaultKeep); a negative value keeps all
	Src        string        // the producer the records name (Producer, "agent")
}

// Writer appends records to the run's file. It is safe for concurrent use. Records are written as they come (no buffer), so a
// crashed run leaves a readable file.
type Writer struct {
	mu        sync.Mutex
	f         *os.File
	path      string
	run       string
	src       string
	max       int64
	every     int64 // ms
	bytes     int64
	frames    int
	events    int
	dropF     int
	dropE     int
	trunc     bool
	noEvents  bool
	lastFrame int64
	haveFrame bool
	err       error
	endT      int64
	parties   map[string]bool
	mobs      map[string]bool
	records   int
}

// Open creates the file (it must not exist) and prunes old generated-id files of the folder.
func Open(o Options) (*Writer, error) {
	if !ValidRunID(o.RunID) {
		return nil, fmt.Errorf("trace: %q is not a run id (letters, digits, . _ - ; at most 64)", o.RunID)
	}
	if o.Dir == "" {
		o.Dir = DefaultDir
	}
	if o.MaxBytes <= 0 {
		o.MaxBytes = DefaultMaxBytes
	}
	if o.FrameEvery <= 0 {
		o.FrameEvery = DefaultFrameEvery
	}
	if o.Keep == 0 {
		o.Keep = DefaultKeep
	}
	if o.Src == "" {
		o.Src = Producer
	}
	if err := os.MkdirAll(o.Dir, 0o755); err != nil {
		return nil, fmt.Errorf("trace: %w", err)
	}
	path := filepath.Join(o.Dir, o.RunID+".jsonl")
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if err != nil {
		return nil, fmt.Errorf("trace: %w", err)
	}
	if o.Keep > 0 {
		Prune(o.Dir, o.Keep, o.RunID)
	}
	return &Writer{f: f, path: path, run: o.RunID, src: o.Src, max: o.MaxBytes, every: o.FrameEvery.Milliseconds(), parties: map[string]bool{}, mobs: map[string]bool{}}, nil
}

// Path is the file's path.
func (w *Writer) Path() string { return w.path }

// IntervalMS is the frame interval the header reports.
func (w *Writer) IntervalMS() int { return int(w.every) }

// MaxBytes is the size limit the header reports.
func (w *Writer) MaxBytes() int64 { return w.max }

// Err is the first write error, if any (a full disk): the run goes on without a trace.
func (w *Writer) Err() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.err
}

// Bytes is the size written so far.
func (w *Writer) Bytes() int64 {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.bytes
}

// Truncated says whether the size limit was hit.
func (w *Writer) Truncated() bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.trunc
}

func (w *Writer) env(kind string, t int64) Envelope {
	return Envelope{V: Version, K: kind, Run: w.run, Src: w.src, T: t}
}

// write appends one record; the caller holds the lock.
func (w *Writer) write(v any) bool {
	if w.f == nil || w.err != nil {
		return false
	}
	b, err := json.Marshal(v)
	if err != nil {
		w.err = err
		return false
	}
	b = append(b, '\n')
	n, err := w.f.Write(b)
	w.bytes += int64(n)
	if err != nil {
		w.err = err
		return false
	}
	w.records++
	return true
}

// Header writes the header (the writer fills the envelope, schema, scope, interval and size limit when they are empty) and a party
// record for each member of h.Party.
func (w *Writer) Header(h Header) {
	w.mu.Lock()
	defer w.mu.Unlock()
	h.Envelope = w.env(KindHeader, 0)
	h.Schema = Schema
	if h.Scope == "" {
		h.Scope = "party"
	}
	h.IntervalMS, h.MaxBytes = int(w.every), w.max
	if h.Caps == nil {
		h.Caps = []string{}
	}
	w.write(h)
	for _, m := range h.Party {
		w.party(0, m)
	}
}

func (w *Writer) party(t int64, m Member) {
	if m.G == "" || w.parties[m.G] {
		return
	}
	w.parties[m.G] = true
	w.write(PartyRecord{Envelope: w.env(KindParty, t), Member: m})
}

// Party writes a party record the first time a member is seen.
func (w *Writer) Party(t int64, m Member) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.party(t, m)
}

// Mob writes a mobs record the first time a hostile is seen (at most 1024 a run).
func (w *Writer) Mob(t int64, m MobInfo) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if m.G == "" || w.mobs[m.G] || len(w.mobs) >= maxMobRecords || w.bytes >= w.max {
		return
	}
	w.mobs[m.G] = true
	w.write(MobRecord{Envelope: w.env(KindMobs, t), MobInfo: m})
}

// Frame writes a frame unless the sampling or the size limit leaves it out; it reports whether it was written. f.T is set by the caller.
func (w *Writer) Frame(f Frame) bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	f.Envelope = w.env(KindFrame, f.T)
	if f.Bots == nil {
		f.Bots = []Bot{}
	}
	if f.Mobs == nil {
		f.Mobs = []Mob{}
	}
	every := w.every
	if w.bytes >= w.max/2 {
		every *= 3
	}
	if w.haveFrame && f.T-w.lastFrame < every*8/10 && f.T >= w.lastFrame {
		return false // sampling, not a loss: not counted as dropped
	}
	if w.bytes >= w.max {
		w.limit("frames")
		w.dropF++
		return false
	}
	if w.write(f) {
		w.frames++
		w.lastFrame, w.haveFrame = f.T, true
		return true
	}
	return false
}

// limit notes the first crossing of a size limit in the trace itself; the caller holds the lock.
func (w *Writer) limit(what string) {
	w.trunc = true
	if what == "events" {
		if w.noEvents {
			return
		}
		w.noEvents = true
	} else if w.bytes < w.max || w.dropF > 0 {
		return
	}
	w.write(Event{Envelope: w.env(KindEvent, w.lastFrame), Ev: "trace_limit", D: map[string]any{"dropping": what, "max_bytes": w.max, "bytes": w.bytes}})
}

// Event writes an event at t (ms); once the size limit is reached events go on for a quarter more, then are dropped.
func (w *Writer) Event(t int64, ev string, d map[string]any) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.bytes >= w.max+w.max/eventReserve {
		w.limit("events")
		w.dropE++
		return
	}
	if w.write(Event{Envelope: w.env(KindEvent, t), Ev: ev, D: d}) {
		w.events++
	}
}

// End writes the closing record: the writer fills the envelope and adds its counts (frames, events, party, mobs, records, bytes,
// dropped, truncated) to e.Counters.
func (w *Writer) End(e End) {
	w.mu.Lock()
	defer w.mu.Unlock()
	e.Envelope = w.env(KindEnd, e.T)
	w.endT = e.T
	c := map[string]any{}
	for k, v := range e.Counters {
		c[k] = v
	}
	c["frames"], c["events"], c["party"], c["mobs"] = w.frames, w.events, len(w.parties), len(w.mobs)
	c["dropped"], c["dropped_frames"], c["dropped_events"], c["truncated"] = w.dropF+w.dropE, w.dropF, w.dropE, w.trunc
	c["records"], c["bytes"] = w.records+1, w.bytes
	e.Counters = c
	w.write(e)
}

// Oracles appends the verdicts.
func (w *Writer) Oracles(cs []Check) {
	w.mu.Lock()
	defer w.mu.Unlock()
	for _, c := range cs {
		w.write(Oracle{Envelope: w.env(KindOracle, w.endT), Check: c})
	}
}

// Close closes the file.
func (w *Writer) Close() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.f == nil {
		return nil
	}
	err := w.f.Close()
	w.f = nil
	return err
}

// ---- summary ----

// Scorer names this scorer in a summary.
const Scorer = "turtle-agent tracecheck v1"

// Summary is the small file beside the trace, <run_id>.summary.json, in the shape the server's summary has (the fields up to
// trace_complete and bad_lines), so a dashboard lists runs of both producers: enough to list runs and their verdicts without parsing
// the trace. It is written when the run starts (status running) and again, atomically, when it ends and is scored. The fields from
// source on are the agent's own.
type Summary struct {
	V             int            `json:"v"`
	Schema        string         `json:"schema"`
	Run           string         `json:"run"`
	Src           string         `json:"src"`
	Status        string         `json:"status"` // running, complete or aborted (a write error)
	EndReason     string         `json:"end_reason,omitempty"`
	Scope         string         `json:"scope"`
	ScopeName     string         `json:"scope_name,omitempty"`
	Requester     string         `json:"requester,omitempty"`
	AgentVersion  string         `json:"agent_version,omitempty"`
	AgentSHA      string         `json:"agent_sha,omitempty"`
	TraceFile     string         `json:"trace_file"`
	StartUnix     int64          `json:"start_unix"`
	EndUnix       int64          `json:"end_unix,omitempty"`
	DurationMS    int64          `json:"duration_ms"`
	IntervalMS    int            `json:"interval_ms"`
	Map           uint32         `json:"map"`
	Instance      uint32         `json:"instance"`
	Counters      map[string]any `json:"counters"`
	Oracles       []Check        `json:"oracles"`
	Verdict       string         `json:"verdict,omitempty"`
	ScoredUnix    int64          `json:"scored_unix,omitempty"`
	Scorer        string         `json:"scorer,omitempty"`
	TraceComplete bool           `json:"trace_complete"`
	BadLines      int            `json:"bad_lines"`

	Source    string            `json:"source,omitempty"` // dungeon_run, brain
	Mode      string            `json:"mode,omitempty"`   // group or remote
	Plan      string            `json:"plan,omitempty"`
	Result    string            `json:"result,omitempty"` // done, failed or stopped
	Error     string            `json:"error,omitempty"`
	Party     []Member          `json:"party,omitempty"`
	Meta      map[string]string `json:"meta,omitempty"`
	Failed    []string          `json:"failed,omitempty"` // ids of the checks that failed
	Bytes     int64             `json:"bytes"`
	Truncated bool              `json:"truncated"`
}

// SummaryPath is where the summary of the trace at tracePath goes.
func SummaryPath(tracePath string) string {
	return strings.TrimSuffix(tracePath, ".jsonl") + ".summary.json"
}

// Verdict folds checks into one: fail if any failed, na if none passed, else pass.
func Verdict(cs []Check) (verdict string, failed []string) {
	passed := 0
	for _, c := range cs {
		switch c.Verdict {
		case Fail:
			failed = append(failed, c.ID)
		case Pass:
			passed++
		}
	}
	switch {
	case len(failed) > 0:
		return Fail, failed
	case passed == 0:
		return NA, nil
	}
	return Pass, nil
}

// SummaryOf builds the summary of a trace read back; a file with no end record is a run still going (or one that crashed). checks may
// be nil (not scored yet). size is the file's size.
func SummaryOf(f *File, size int64, checks []Check, scored time.Time) Summary {
	h := f.Header
	s := Summary{V: Version, Schema: Schema, Run: h.Run, Src: h.Src, Status: "running", Scope: h.Scope, ScopeName: h.ScopeName, Requester: h.Requester,
		AgentVersion: h.AgentVersion, AgentSHA: h.AgentSHA, TraceFile: filepath.Base(f.Path), StartUnix: h.StartUnix, IntervalMS: h.IntervalMS, Map: h.Map,
		Instance: h.Instance, Counters: map[string]any{}, Oracles: []Check{}, Source: h.Source, Mode: h.Mode, Plan: h.Plan, Party: f.Party, Meta: h.Meta,
		Bytes: size, BadLines: f.Skipped}
	if s.Src == "" {
		s.Src = Producer
	}
	// A driver that learns the instance from the feed (remote and observe runs) writes it as an instance event: the header may have none.
	if s.Instance == 0 {
		for _, e := range f.Events {
			if e.Ev != "instance" {
				continue
			}
			switch v := e.D["instance"].(type) {
			case float64:
				s.Instance = uint32(v)
			case int:
				s.Instance = uint32(v)
			case uint32:
				s.Instance = v
			}
			if s.Instance > 0 {
				break
			}
		}
	}
	if e := f.End; e != nil {
		s.Status, s.EndReason, s.Result, s.Error = "complete", e.Reason, e.Reason, e.Error
		s.DurationMS = int64(e.Seconds * 1000)
		s.EndUnix = e.Ended.Unix()
		s.Counters = e.Counters
		s.Truncated, _ = e.Counters["truncated"].(bool)
		s.TraceComplete = true
	} else {
		s.DurationMS = int64(f.Seconds() * 1000)
	}
	if checks != nil {
		s.Oracles = checks
		s.Verdict, s.Failed = Verdict(checks)
		s.ScoredUnix, s.Scorer = scored.Unix(), Scorer
	}
	return s
}

// WriteSummary writes s next to the trace (path is the trace's), through a temporary file and a rename, so that a reader sees a
// whole file.
func WriteSummary(tracePath string, s Summary) error {
	b, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	dst := SummaryPath(tracePath)
	tmp := dst + ".tmp"
	if err := os.WriteFile(tmp, append(b, '\n'), 0o644); err != nil {
		return err
	}
	if err := os.Rename(tmp, dst); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return nil
}
