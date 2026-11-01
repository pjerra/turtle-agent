package dashboard

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/pjerra/turtle-agent/internal/routeedit"
)

// Slice 1 of #183: the run controls (README "Controls (--control)"). The tests drive the real handler with a fake agent that answers
// JSON-RPC, and read the control log back.

const controlToken = "ctl-token-for-the-tests-0123456789abcdef"

// rpcCall is one JSON-RPC request the fake agent saw.
type rpcCall struct {
	Method string
	Params json.RawMessage
	Auth   string
}

// fakeWriteAgent is an httptest JSON-RPC server that records every call and answers per a scripted map. It is the write counterpart of
// fakeAgent (which must keep refusing parameters).
type fakeWriteAgent struct {
	mu      sync.Mutex
	calls   []rpcCall
	results map[string]string        // method -> raw JSON result
	errs    map[string]string        // method -> error message
	gates   map[string]chan struct{} // method -> held until closed (for the in-flight test)
	srv     *httptest.Server
}

func newFakeWriteAgent(t *testing.T) *fakeWriteAgent {
	t.Helper()
	f := &fakeWriteAgent{results: map[string]string{}, errs: map[string]string{}, gates: map[string]chan struct{}{}}
	f.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Method string          `json:"method"`
			ID     int             `json:"id"`
			Params json.RawMessage `json:"params"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		f.mu.Lock()
		f.calls = append(f.calls, rpcCall{req.Method, req.Params, r.Header.Get("Authorization")})
		gate, res, errMsg := f.gates[req.Method], f.results[req.Method], f.errs[req.Method]
		f.mu.Unlock()
		if gate != nil {
			<-gate
		}
		switch {
		case errMsg != "":
			fmt.Fprintf(w, `{"jsonrpc":"2.0","id":1,"error":{"code":-32000,"message":%s}}`, jsonStr(errMsg))
		case res == "":
			w.Write([]byte(`{"jsonrpc":"2.0","id":1,"error":{"code":-32601,"message":"Method not found"}}`))
		default:
			fmt.Fprintf(w, `{"jsonrpc":"2.0","id":1,"result":%s}`, res)
		}
	}))
	t.Cleanup(f.srv.Close)
	return f
}

func jsonStr(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}

func (f *fakeWriteAgent) set(method, result string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.results[method] = result
}

func (f *fakeWriteAgent) fail(method, message string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.errs[method] = message
}

func (f *fakeWriteAgent) hold(method string) chan struct{} {
	f.mu.Lock()
	defer f.mu.Unlock()
	ch := make(chan struct{})
	f.gates[method] = ch
	return ch
}

func (f *fakeWriteAgent) methods() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]string, len(f.calls))
	for i, c := range f.calls {
		out[i] = c.Method
	}
	return out
}

func (f *fakeWriteAgent) paramsOf(method string) (json.RawMessage, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, c := range f.calls {
		if c.Method == method {
			return c.Params, true
		}
	}
	return nil, false
}

func (f *fakeWriteAgent) sawWrite() bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, c := range f.calls {
		if controlWriteMethods[c.Method] {
			return true
		}
	}
	return false
}

// controlRig is one dashboard with the controls on, watching the fake agent.
type controlRig struct {
	t     *testing.T
	p     *Poller
	ctl   *Controller
	fake  *fakeWriteAgent
	agent Agent
	logP  string
	token string
	srv   *httptest.Server
}

func newControlRig(t *testing.T) *controlRig {
	t.Helper()
	root := t.TempDir()
	cfg := filepath.Join(root, "config")
	must(t, os.MkdirAll(cfg, 0o755))
	f := newFakeWriteAgent(t)
	u, _ := url.Parse(f.srv.URL)
	id := u.Port()
	tok := "agent-" + strings.Repeat("a", 24)
	must(t, os.WriteFile(filepath.Join(cfg, "rpc-"+id+".token"), []byte(tok+"\n"), 0o600))
	tokens := filepath.Join(root, "control-tokens")
	must(t, os.WriteFile(tokens, []byte("owner "+controlToken+"\n"), 0o600))
	tk, err := routeedit.LoadTokens(tokens)
	must(t, err)
	logP := filepath.Join(root, "state", "control.jsonl")
	p := NewPoller(Options{ConfigDir: cfg, Agents: "127.0.0.1:" + id})
	ctl, err := NewController(p, ControlConfig{Tokens: tk, LogPath: logP})
	must(t, err)
	a := Agent{ID: id, Addr: u.Host, TokenPath: filepath.Join(cfg, "rpc-"+id+".token")}
	p.mu.Lock()
	p.known = []Agent{a}
	p.mu.Unlock()
	r := &controlRig{t: t, p: p, ctl: ctl, fake: f, agent: a, logP: logP, token: controlToken}
	r.srv = httptest.NewServer(HandlerFull(p, "", nil, nil, nil, ctl))
	t.Cleanup(r.srv.Close)
	return r
}

func (r *controlRig) do(method, path string, body []byte, hdr map[string]string) reply {
	r.t.Helper()
	req, err := http.NewRequest(method, r.srv.URL+path, bytes.NewReader(body))
	must(r.t, err)
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	resp, err := http.DefaultClient.Do(req)
	must(r.t, err)
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return reply{resp.StatusCode, string(b)}
}

func (r *controlRig) post(path string, req map[string]any, token string, extra map[string]string) reply {
	r.t.Helper()
	b, _ := json.Marshal(req)
	h := map[string]string{"Content-Type": "application/json"}
	if token != "" {
		h["Authorization"] = "Bearer " + token
	}
	for k, v := range extra {
		h[k] = v
	}
	return r.do(http.MethodPost, path, b, h)
}

func (r *controlRig) logRecords() []map[string]any {
	r.t.Helper()
	b, err := os.ReadFile(r.logP)
	if err != nil {
		return nil
	}
	var out []map[string]any
	for _, line := range strings.Split(strings.TrimSpace(string(b)), "\n") {
		if line == "" {
			continue
		}
		var m map[string]any
		must(r.t, json.Unmarshal([]byte(line), &m))
		out = append(out, m)
	}
	return out
}



// Test 1: an observe run's pause goes straight through, and the log has a request line before the RPC and a result line after.
func TestControlRunPauseObserveSendsDungeonPause(t *testing.T) {
	r := newControlRig(t)
	r.fake.set("dungeon_status", `{"running":true,"state":"running","report":{"state":"running","remote":{"mode":"observe"}}}`)
	r.fake.set("dungeon_pause", `{"ok":true}`)
	rec := r.post("/api/control/"+r.agent.ID+"/run", map[string]any{"verb": "pause"}, r.token, nil)
	if rec.code != http.StatusOK {
		t.Fatalf("pause: %d %s", rec.code, rec.body)
	}
	calls := r.fake.methods()
	if len(calls) != 2 || calls[0] != "dungeon_status" || calls[1] != "dungeon_pause" {
		t.Fatalf("the fake saw %v, want [dungeon_status dungeon_pause]", calls)
	}
	if p, ok := r.fake.paramsOf("dungeon_pause"); ok && len(p) > 0 && string(p) != "null" {
		t.Errorf("dungeon_pause took parameters: %s", p)
	}
	lines := r.logRecords()
	if len(lines) != 2 || lines[0]["phase"] != "request" || lines[1]["phase"] != "result" {
		t.Fatalf("the log is %v", lines)
	}
	if lines[0]["who"] != "owner" || lines[1]["who"] != "owner" || lines[1]["ok"] != true {
		t.Errorf("the log lines say %v %v", lines[0], lines[1])
	}
}
