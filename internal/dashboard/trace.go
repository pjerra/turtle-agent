package dashboard

import (
	"bufio"
	"bytes"
	"encoding/json"
	"io"
	"os"
	"regexp"
	"strings"
	"sync"
	"time"
)

// A run trace (tracerun) is a JSON-lines file the agent writes while the run goes. The dashboard reads it from the end
// it has already seen, so a poll costs the new bytes only; the file is only ever opened for reading.
const (
	traceFirstRead = 6 << 20   // a file larger than this is read from its head (header, party) and its last traceTail bytes
	traceTail      = 2 << 20   //
	traceChunk     = 4 << 20   // the most one poll reads
	traceHead      = 256 << 10 // the head read of a large file
	maxOrders      = 10
	maxMobEntries  = 8192
	maxLine        = 1 << 20
	traceIdle      = 10 * time.Minute // a tracker nobody asked for this long is dropped
)

// Order is one order of the run as the trace records it, with the module's answer.
type Order struct {
	At      time.Time `json:"at"`
	Verb    string    `json:"verb,omitempty"`
	Text    string    `json:"text"`
	Via     string    `json:"via,omitempty"`   // cmd (a `commander cmd`) or chat (a `.bot action` line)
	Reply   string    `json:"reply,omitempty"` // ACK, ERR, ... as the module answered; empty for a chat order
	Code    string    `json:"code,omitempty"`
	Message string    `json:"message,omitempty"`
	MS      int64     `json:"ms,omitempty"`
}

// TraceParty is one record of the trace's party list.
type TraceParty struct {
	GUID string `json:"-"`
	Name string `json:"name"`
	Role string `json:"role"`
	Lead bool   `json:"lead"`
}

// TraceSnapshot is what the dashboard knows of a trace file.
type TraceSnapshot struct {
	Path string
	SHA  string
	// RouteEdit is the run's trace_meta.route_edit ("zulfarrak@v0007 sha256=..."): which edit of the route editor the run was started with.
	RouteEdit string
	Plan      string
	RunID     string
	Map       uint32
	Started   time.Time
	Party     []TraceParty
	Orders    []Order // the last maxOrders, oldest first
	Ended     bool
	Size      int64
}

type traceFile struct {
	offset int64
	snap   TraceSnapshot
	party  map[string]TraceParty
	seen   time.Time
}

// Tailer follows trace files incrementally.
type Tailer struct {
	mu    sync.Mutex
	files map[string]*traceFile
}

// NewTailer makes an empty tailer.
func NewTailer() *Tailer { return &Tailer{files: map[string]*traceFile{}} }

// Read brings the state of the trace at path up to date and returns it. The caller has already checked the path.
func (t *Tailer) Read(path string, now time.Time) (TraceSnapshot, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	for p, f := range t.files {
		if now.Sub(f.seen) > traceIdle {
			delete(t.files, p)
		}
	}
	f := t.files[path]
	if f == nil {
		f = &traceFile{party: map[string]TraceParty{}}
		f.snap.Path = path
		t.files[path] = f
	}
	f.seen = now
	fh, err := os.Open(path)
	if err != nil {
		return TraceSnapshot{}, err
	}
	defer fh.Close()
	st, err := fh.Stat()
	if err != nil {
		return TraceSnapshot{}, err
	}
	size := st.Size()
	if size < f.offset { // the file was replaced or cut: start over
		*f = traceFile{party: map[string]TraceParty{}, seen: now}
		f.snap.Path = path
	}
	if f.offset == 0 && size > traceFirstRead {
		f.readRange(fh, 0, traceHead)
		f.offset = size - traceTail
		if _, err := fh.Seek(f.offset, io.SeekStart); err == nil {
			br := bufio.NewReader(fh) // drop the partial first line
			if line, err := br.ReadBytes('\n'); err == nil {
				f.offset += int64(len(line))
			}
		}
	}
	f.readRange(fh, f.offset, traceChunk)
	f.snap.Size = size
	out := f.snap
	out.Orders = append([]Order(nil), f.snap.Orders...)
	out.Party = nil
	for _, p := range f.party {
		out.Party = append(out.Party, p)
	}
	return out, nil
}

// readRange reads whole lines from off, at most limit bytes, into the state. When it reads from the tracked offset it
// advances it past the last complete line.
func (f *traceFile) readRange(fh *os.File, off, limit int64) {
	if _, err := fh.Seek(off, io.SeekStart); err != nil {
		return
	}
	data, _ := io.ReadAll(io.LimitReader(fh, limit))
	last := bytes.LastIndexByte(data, '\n')
	if last < 0 {
		return
	}
	data = data[:last+1]
	for len(data) > 0 {
		i := bytes.IndexByte(data, '\n')
		line := data[:i]
		data = data[i+1:]
		if len(line) > 0 && len(line) <= maxLine {
			f.line(line)
		}
	}
	if off == f.offset {
		f.offset += int64(last + 1)
	}
}

type traceEnv struct {
	K  string `json:"k"`
	Ev string `json:"ev"`
	T  int64  `json:"t"`
}

func (f *traceFile) line(b []byte) {
	// A frame is the bulk of the file and the dashboard has no use for it: only its kind is looked at.
	if bytes.HasPrefix(b, []byte(`{"v":1,"k":"frame"`)) || bytes.HasPrefix(b, []byte(`{"v":1,"k":"mobs"`)) {
		return
	}
	var env traceEnv
	if json.Unmarshal(b, &env) != nil {
		return
	}
	switch env.K {
	case "header":
		var h struct {
			SHA   string `json:"agent_sha"`
			Plan  string `json:"plan"`
			Run   string `json:"run"`
			Map   uint32 `json:"map"`
			Start string `json:"start"`
			Meta  struct {
				SHA       string `json:"agent_sha"`
				RouteEdit string `json:"route_edit"`
			} `json:"meta"`
		}
		if json.Unmarshal(b, &h) != nil {
			return
		}
		f.snap.SHA, f.snap.Plan, f.snap.RunID, f.snap.Map = shortSHA(h.SHA), h.Plan, h.Run, h.Map
		if f.snap.SHA == "" {
			f.snap.SHA = shortSHA(h.Meta.SHA)
		}
		f.snap.RouteEdit = cleanRouteEdit(h.Meta.RouteEdit)
		f.snap.Started, _ = time.Parse(time.RFC3339Nano, h.Start)
	case "party":
		var p struct {
			G    string `json:"g"`
			Name string `json:"name"`
			Role string `json:"role"`
			Lead bool   `json:"lead"`
		}
		if json.Unmarshal(b, &p) == nil && p.G != "" {
			f.party[p.G] = TraceParty{GUID: p.G, Name: p.Name, Role: p.Role, Lead: p.Lead}
		}
	case "end":
		f.snap.Ended = true
	case "event":
		f.event(env, b)
	}
}

func (f *traceFile) event(env traceEnv, b []byte) {
	at := f.snap.Started.Add(time.Duration(env.T) * time.Millisecond)
	switch env.Ev {
	case "order":
		var o struct {
			Verb  string `json:"verb"`
			Text  string `json:"text"`
			Via   string `json:"via"`
			Reply string `json:"reply"`
			Code  string `json:"code"`
			Msg   string `json:"message"`
			MS    int64  `json:"ms"`
			Err   string `json:"error"`
		}
		if json.Unmarshal(b, &o) != nil {
			return
		}
		ord := Order{At: at, Verb: o.Verb, Text: o.Text, Via: o.Via, Reply: strings.ToUpper(o.Reply), Code: o.Code, Message: o.Msg, MS: o.MS}
		if o.Err != "" {
			ord.Reply, ord.Message = "ERR", o.Err
		}
		f.snap.Orders = append(f.snap.Orders, ord)
		if n := len(f.snap.Orders); n > maxOrders {
			f.snap.Orders = append([]Order(nil), f.snap.Orders[n-maxOrders:]...)
		}
	}
}

func shortSHA(s string) string {
	if len(s) > 12 {
		return s[:12]
	}
	return s
}

// routeEditTag is the shape of a run's route_edit tag: <plan>@v0007 sha256=<64 hex>.
var routeEditTag = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,39}@v\d{4} sha256=[0-9a-f]{64}$`)

// cleanRouteEdit keeps a run's route_edit tag when it has the shape `turtle-agent route-edits export` prints, else nothing: the text is the
// caller's, and the page shows it.
func cleanRouteEdit(s string) string {
	if routeEditTag.MatchString(s) {
		return s
	}
	return ""
}
