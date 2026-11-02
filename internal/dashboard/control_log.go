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
	Peer    string `json:"peer,omitempty"`    // the caller's address as the dashboard saw it (left out of a log read by a caller who may not send controls)
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

// openControlLog opens (or makes) the log: its folder must be ours and not writable by others (refused, not fixed), the file must be a
// regular file of ours, not a link, and it is held 0600 (a mode of ours that is not 0600 is set to it). Everything is checked on the open
// descriptor, and every later read goes through that descriptor, never the path. A failure is fatal to --control (fail closed).
func openControlLog(path string) (*controlLog, error) {
	if path == "" {
		return nil, errors.New("control log: no path")
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	dst, err := os.Stat(dir)
	if err != nil {
		return nil, err
	}
	switch {
	case !dst.IsDir():
		return nil, fmt.Errorf("control log folder %s is not a folder", dir)
	case !ownedByUser(dst):
		return nil, fmt.Errorf("control log folder %s belongs to another user", dir)
	case dst.Mode().Perm()&0o022 != 0:
		return nil, fmt.Errorf("control log folder %s is writable by others (mode %04o); make it 0700 or choose another --control-log", dir, dst.Mode().Perm())
	}
	if oNoFollow == 0 {
		// Off unix an open follows a link, so the link is refused before it (on unix O_NOFOLLOW does it in the open itself).
		if st, err := os.Lstat(path); err == nil && st.Mode()&os.ModeSymlink != 0 {
			return nil, fmt.Errorf("control log %s is a symbolic link", path)
		}
	}
	// One open, no check-then-open gap: a link at the path is an error, and a new file is made 0600.
	f, err := os.OpenFile(path, os.O_RDWR|os.O_APPEND|os.O_CREATE|oNoFollow, 0o600)
	if err != nil {
		return nil, openRefusal(path, err)
	}
	fail := func(err error) (*controlLog, error) {
		f.Close()
		return nil, err
	}
	st, err := f.Stat()
	if err != nil {
		return fail(err)
	}
	switch {
	case !st.Mode().IsRegular():
		return fail(fmt.Errorf("control log %s is not a regular file", path))
	case !ownedByUser(st):
		return fail(fmt.Errorf("control log %s belongs to another user", path))
	case st.Mode().Perm() != 0o600:
		if err := f.Chmod(0o600); err != nil {
			return fail(fmt.Errorf("control log %s has mode %04o and cannot be made 0600: %w", path, st.Mode().Perm(), err))
		}
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

// tail returns the last n result lines of one agent, newest last: the last controlMaxTail bytes, read through the descriptor the log was
// opened with (never the path, which may by now name another file). The lock keeps a half-written line out of the read.
func (l *controlLog) tail(agent string, n int) ([]controlRecord, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.f == nil {
		return nil, nil
	}
	st, err := l.f.Stat()
	if err != nil {
		return nil, err
	}
	var start int64
	if st.Size() > controlMaxTail {
		start = st.Size() - controlMaxTail
	}
	data := make([]byte, st.Size()-start)
	m, err := l.f.ReadAt(data, start)
	if err != nil && !errors.Is(err, io.EOF) {
		return nil, err
	}
	data = data[:m]
	if start > 0 {
		if i := bytes.IndexByte(data, '\n'); i >= 0 {
			data = data[i+1:] // the first line may be cut by the tail window
		}
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
