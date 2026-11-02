package dashboard

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"
)

// The control log (README "Controls (--control)", D10). Every control writes two JSON lines: a "request" line before the RPC (a write that
// fails stops the control, nothing is sent) and a "result" line after. The log holds who sent it and what the agent answered, never a token.

// controlMaxTail bounds what GET /api/control/<id>/log reads: the last bytes of the file, enough for many lines.
const controlMaxTail = 256 << 10

// controlMaxLines is the most result lines one log request answers (the ?n= bound).
const controlMaxLines = 50

// controlRecord is one line of the log.
type controlRecord struct {
	Phase   string `json:"phase"`             // request or result
	At      string `json:"at"`                // RFC3339, the dashboard's clock
	Who     string `json:"who"`               // the token label
	Via     string `json:"via"`               // token or trusted
	Peer    string `json:"peer"`              // the caller's address as the dashboard saw it
	Agent   string `json:"agent"`             // the agent id (its normalized RPC address)
	Action  string `json:"action"`            // run (tune, order in later slices)
	Verb    string `json:"verb"`              // pause, resume, skip, stop
	Args    any    `json:"args,omitempty"`    // what was sent to the RPC (nil for the run controls)
	RPC     string `json:"rpc,omitempty"`     // the RPC method of the result line
	OK      *bool  `json:"ok,omitempty"`      // the result line: did the agent answer without an error (absent when indeterminate)
	Outcome string `json:"outcome,omitempty"` // the result line: ok, error, or indeterminate (a reply that is not a JSON-RPC 2.0 answer to this request)
	Code    int    `json:"code,omitempty"`    // the agent's RPC error code, when it gave one
	Message string `json:"message,omitempty"` // the result line: the scrubbed, redacted message
}

// controlStderr is where a result line the log cannot hold is written (a test injects a buffer).
var controlStderr io.Writer = os.Stderr

// logLost writes a result line that the control log could not take to controlStderr, so an operator can still read what the agent answered.
func logLost(res controlRecord, cause error) {
	b, _ := json.Marshal(res)
	fmt.Fprintf(controlStderr, "control log write failed (%v); result line: %s\n", cause, b)
}

// controlLog appends to the log file and reads it back. w is the writer (the file; a test injects a failing one).
type controlLog struct {
	w  io.Writer
	f  *os.File
	mu sync.Mutex
}

// openControlLog opens (or makes) the log: its folder 0700, the file 0600, appended to. A failure is fatal to --control (fail closed).
func openControlLog(path string) (*controlLog, error) {
	if path == "" {
		return nil, errors.New("control log: no path")
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return nil, err
	}
	return &controlLog{w: f, f: f}, nil
}

// Close closes the file (the tests' injected writer keeps no file).
func (l *controlLog) Close() {
	if l.f != nil {
		l.f.Close()
	}
}

// append writes one line. It returns the write error so the caller can fail closed.
func (l *controlLog) append(r controlRecord) error {
	b, err := json.Marshal(r)
	if err != nil {
		return err
	}
	b = append(b, '\n')
	l.mu.Lock()
	defer l.mu.Unlock()
	_, err = l.w.Write(b)
	return err
}

// tail returns the last n result lines of one agent, newest last, read by tailing the file (at most controlMaxTail bytes).
func (l *controlLog) tail(agent string, n int) ([]controlRecord, error) {
	l.mu.Lock()
	path := ""
	if l.f != nil {
		path = l.f.Name()
	}
	l.mu.Unlock()
	if path == "" {
		return nil, nil
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	if st, err := f.Stat(); err == nil && st.Size() > controlMaxTail {
		if _, err := f.Seek(st.Size()-controlMaxTail, io.SeekStart); err != nil {
			return nil, err
		}
	}
	data, err := io.ReadAll(f)
	if err != nil {
		return nil, err
	}
	if i := bytes.IndexByte(data, '\n'); i >= 0 && len(data) > controlMaxTail {
		data = data[i+1:] // the first line may be cut by the tail window
	}
	var out []controlRecord
	for _, line := range bytes.Split(data, []byte("\n")) {
		if len(line) == 0 {
			continue
		}
		var r controlRecord
		if json.Unmarshal(line, &r) != nil || r.Phase != "result" || r.Agent != agent {
			continue
		}
		out = append(out, r)
	}
	if len(out) > n {
		out = out[len(out)-n:]
	}
	return out, nil
}
