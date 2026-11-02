package dashboard

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

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
	return newFakeWriteAgentOn(t, nil)
}

// newFakeWriteAgentOn starts the fake on ln, or on a fresh loopback listener when ln is nil.
func newFakeWriteAgentOn(t *testing.T, ln net.Listener) *fakeWriteAgent {
	t.Helper()
	f := &fakeWriteAgent{results: map[string]string{}, errs: map[string]string{}, gates: map[string]chan struct{}{}}
	h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
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
	})
	if ln == nil {
		f.srv = httptest.NewServer(h)
	} else {
		f.srv = &httptest.Server{Listener: ln, Config: &http.Server{Handler: h}}
		f.srv.Start()
	}
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
	t          *testing.T
	p          *Poller
	ctl        *Controller
	fake       *fakeWriteAgent
	agent      Agent
	agentToken string
	logP       string
	token      string
	srv        *httptest.Server
}

// watch adds a second fake agent to the agents the dashboard watches.
func (r *controlRig) watch(f *fakeWriteAgent) Agent {
	r.t.Helper()
	u, _ := url.Parse(f.srv.URL)
	id := u.Port()
	must(r.t, os.WriteFile(filepath.Join(r.p.opt.ConfigDir, "rpc-"+id+".token"), []byte(r.agentToken+"\n"), 0o600))
	a := Agent{ID: u.Host, Addr: u.Host, TokenPath: filepath.Join(r.p.opt.ConfigDir, "rpc-"+id+".token")}
	r.p.mu.Lock()
	r.p.known = append(r.p.known, a)
	r.p.mu.Unlock()
	return a
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
	a := Agent{ID: u.Host, Addr: u.Host, TokenPath: filepath.Join(cfg, "rpc-"+id+".token")}
	p.mu.Lock()
	p.known = []Agent{a}
	p.mu.Unlock()
	r := &controlRig{t: t, p: p, ctl: ctl, fake: f, agent: a, agentToken: tok, logP: logP, token: controlToken}
	r.srv = httptest.NewServer(HandlerFull(p, "", nil, nil, nil, ctl))
	t.Cleanup(r.srv.Close)
	return r
}

// Test 14: two agents on the same port, different loopback hosts, keep distinct ids, and each control reaches its own RPC server.
func TestControlDistinctAgentsOnSamePort(t *testing.T) {
	ln1, err := net.Listen("tcp", "127.0.0.1:0")
	must(t, err)
	_, port, err := net.SplitHostPort(ln1.Addr().String())
	must(t, err)
	ln2, err := net.Listen("tcp", net.JoinHostPort("127.0.0.2", port))
	if err != nil {
		ln1.Close()
		t.Skipf("127.0.0.2:%s cannot be bound here: %v", port, err)
	}
	first, second := newFakeWriteAgentOn(t, ln1), newFakeWriteAgentOn(t, ln2)

	root := t.TempDir()
	cfg := filepath.Join(root, "config")
	must(t, os.MkdirAll(cfg, 0o755))
	tok := "agent-" + strings.Repeat("b", 24)
	must(t, os.WriteFile(filepath.Join(cfg, "rpc-"+port+".token"), []byte(tok+"\n"), 0o600))
	// auto discovery is where an id comes from: the same port on two loopback hosts
	must(t, os.WriteFile(filepath.Join(cfg, "settings-a.json"), []byte(`{"rpc":"127.0.0.1:`+port+`"}`), 0o600))
	must(t, os.WriteFile(filepath.Join(cfg, "settings-b.json"), []byte(`{"rpc":"127.0.0.2:`+port+`"}`), 0o600))
	agents, err := DiscoverAgents(cfg, "auto")
	must(t, err)
	if len(agents) != 2 {
		t.Fatalf("discovered %+v, want two agents", agents)
	}
	want := [2]string{"127.0.0.1:" + port, "127.0.0.2:" + port}
	for i := range want {
		if agents[i].ID != want[i] || agents[i].ID != agents[i].Addr {
			t.Fatalf("agent %d is %+v, want id and addr %s", i, agents[i], want[i])
		}
	}
	if agents[0].ID == agents[1].ID {
		t.Fatalf("two agents on port %s share the id %q", port, agents[0].ID)
	}

	tokens := filepath.Join(root, "control-tokens")
	must(t, os.WriteFile(tokens, []byte("owner "+controlToken+"\n"), 0o600))
	tk, err := routeedit.LoadTokens(tokens)
	must(t, err)
	p := NewPoller(Options{ConfigDir: cfg, Agents: "auto"})
	ctl, err := NewController(p, ControlConfig{Tokens: tk, LogPath: filepath.Join(root, "state", "control.jsonl")})
	must(t, err)
	p.mu.Lock()
	p.known = agents
	p.mu.Unlock()
	srv := httptest.NewServer(HandlerFull(p, "", nil, nil, nil, ctl))
	defer srv.Close()

	fakes := []*fakeWriteAgent{first, second}
	for i, f := range fakes {
		f.set("dungeon_status", `{"running":true,"state":"running","report":{"state":"running","remote":{"mode":"observe"}}}`)
		f.set("dungeon_pause", fmt.Sprintf(`{"ok":true,"server":%d}`, i+1))
	}
	post := func(id string) reply {
		b, _ := json.Marshal(map[string]any{"verb": "pause", "confirm": true})
		req, err := http.NewRequest(http.MethodPost, srv.URL+"/api/control/"+id+"/run", bytes.NewReader(b))
		must(t, err)
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", "Bearer "+controlToken)
		resp, err := http.DefaultClient.Do(req)
		must(t, err)
		defer resp.Body.Close()
		body, _ := io.ReadAll(resp.Body)
		return reply{resp.StatusCode, string(body)}
	}
	for i, a := range agents {
		if rec := post(a.ID); rec.code != http.StatusOK || !strings.Contains(rec.body, fmt.Sprintf(`"server":%d`, i+1)) {
			t.Fatalf("a pause for %s reached the wrong server: %d %s", a.ID, rec.code, rec.body)
		}
		for j, f := range fakes {
			wantCalls := []string{}
			if j <= i {
				wantCalls = []string{"dungeon_status", "dungeon_pause"}
			}
			if got := f.methods(); strings.Join(got, ",") != strings.Join(wantCalls, ",") {
				t.Fatalf("after the control for %s, fake %d saw %v, want %v", a.ID, j+1, got, wantCalls)
			}
		}
	}
}

func (r *controlRig) do(method, path string, body []byte, hdr map[string]string) reply {
	r.t.Helper()
	req, err := http.NewRequest(method, r.srv.URL+path, bytes.NewReader(body))
	must(r.t, err)
	for k, v := range hdr {
		if k == "Host" { // a Host header is carried on the request itself, not in the header map
			req.Host = v
			continue
		}
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
	rec := r.post("/api/control/"+r.agent.ID+"/run", map[string]any{"verb": "pause", "confirm": true}, r.token, nil)
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

// Test 2: pausing a commander (remote) run ENDS it, so it needs a confirm; an observe pause does not.
func TestControlRemotePauseNeedsConfirm(t *testing.T) {
	r := newControlRig(t)
	r.fake.set("dungeon_status", `{"running":true,"state":"running","report":{"state":"running","remote":{"mode":"remote"}}}`)
	r.fake.set("dungeon_pause", `{"ok":true}`)
	if rec := r.post("/api/control/"+r.agent.ID+"/run", map[string]any{"verb": "pause"}, r.token, nil); rec.code != http.StatusConflict {
		t.Fatalf("pause without a confirm: %d %s", rec.code, rec.body)
	}
	if r.fake.sawWrite() {
		t.Fatal("dungeon_pause was sent without a confirm")
	}
	if rec := r.post("/api/control/"+r.agent.ID+"/run", map[string]any{"verb": "pause", "confirm": true}, r.token, nil); rec.code != http.StatusOK {
		t.Fatalf("pause with a confirm: %d %s", rec.code, rec.body)
	}
	if !r.fake.sawWrite() {
		t.Fatal("dungeon_pause was not sent with a confirm")
	}
}

// Test 3: stop ends the run and unbinds, so it always needs a confirm, running or idle.
func TestControlStopNeedsConfirm(t *testing.T) {
	for _, running := range []bool{true, false} {
		r := newControlRig(t)
		r.fake.set("dungeon_status", fmt.Sprintf(`{"running":%v,"state":"idle","report":{"state":"idle","remote":{"mode":"observe"}}}`, running))
		r.fake.set("dungeon_stop", `{"ok":true}`)
		if rec := r.post("/api/control/"+r.agent.ID+"/run", map[string]any{"verb": "stop"}, r.token, nil); rec.code != http.StatusConflict {
			t.Errorf("running=%v: stop without a confirm: %d %s", running, rec.code, rec.body)
		}
		if r.fake.sawWrite() {
			t.Fatalf("running=%v: dungeon_stop was sent without a confirm", running)
		}
		if rec := r.post("/api/control/"+r.agent.ID+"/run", map[string]any{"verb": "stop", "confirm": true}, r.token, nil); rec.code != http.StatusOK {
			t.Errorf("running=%v: stop with a confirm: %d %s", running, rec.code, rec.body)
		}
	}
}

// Test 3b: a pause always needs a confirm. The status read can be idle (or an observe run) and the run can start before the RPC lands;
// dungeon_pause carries no expected state, so a stale read must never let an unconfirmed pause reach the agent.
func TestControlPauseNeedsConfirmEvenWhenStatusLooksIdle(t *testing.T) {
	statuses := map[string]string{
		"idle":    `{"running":false,"state":"idle","report":{"state":"idle"}}`,
		"observe": `{"running":true,"state":"running","report":{"state":"running","remote":{"mode":"observe"}}}`,
	}
	for name, status := range statuses {
		r := newControlRig(t)
		r.fake.set("dungeon_status", status)
		r.fake.set("dungeon_pause", `{"ok":true}`)
		if rec := r.post("/api/control/"+r.agent.ID+"/run", map[string]any{"verb": "pause"}, r.token, nil); rec.code != http.StatusConflict {
			t.Errorf("%s: an unconfirmed pause: %d %s, want 409", name, rec.code, rec.body)
		}
		if calls := r.fake.methods(); len(calls) != 1 || calls[0] != "dungeon_status" {
			t.Errorf("%s: the fake saw %v, want only the fresh dungeon_status", name, calls)
		}
		if rec := r.post("/api/control/"+r.agent.ID+"/run", map[string]any{"verb": "pause", "confirm": true}, r.token, nil); rec.code != http.StatusOK {
			t.Errorf("%s: a confirmed pause: %d %s", name, rec.code, rec.body)
		}
	}
}

// Test 4: everything the controls refuse, and none of it reaches the agent.
func TestControlRefusals(t *testing.T) {
	idPath := func(r *controlRig) string { return "/api/control/" + r.agent.ID + "/run" }
	big := `{"verb":"pause","x":"` + strings.Repeat("a", 5<<10) + `"}`
	cases := map[string]func(t *testing.T, r *controlRig) (int, bool){
		"no token": func(t *testing.T, r *controlRig) (int, bool) {
			return r.post(idPath(r), map[string]any{"verb": "pause"}, "", nil).code, true
		},
		"wrong token": func(t *testing.T, r *controlRig) (int, bool) {
			return r.post(idPath(r), map[string]any{"verb": "pause"}, "nope-"+strings.Repeat("z", 40), nil).code, true
		},
		"the route-edit token": func(t *testing.T, r *controlRig) (int, bool) {
			return r.post(idPath(r), map[string]any{"verb": "pause"}, editToken, nil).code, true
		},
		"eleven wrong tries in a minute": func(t *testing.T, r *controlRig) (int, bool) {
			code := 0
			for i := 0; i < 11; i++ {
				code = r.post(idPath(r), map[string]any{"verb": "pause"}, "wrong-"+strings.Repeat("q", 40), nil).code
			}
			return code, true
		},
		"text/plain": func(t *testing.T, r *controlRig) (int, bool) {
			return r.do(http.MethodPost, idPath(r), []byte(`{"verb":"pause"}`), map[string]string{"Content-Type": "text/plain", "Authorization": "Bearer " + r.token}).code, true
		},
		"a 5 KiB body": func(t *testing.T, r *controlRig) (int, bool) {
			return r.do(http.MethodPost, idPath(r), []byte(big), map[string]string{"Content-Type": "application/json", "Authorization": "Bearer " + r.token}).code, true
		},
		"an unknown field": func(t *testing.T, r *controlRig) (int, bool) {
			return r.post(idPath(r), map[string]any{"verb": "pause", "bogus": 1}, r.token, nil).code, true
		},
		"an unknown verb": func(t *testing.T, r *controlRig) (int, bool) {
			return r.post(idPath(r), map[string]any{"verb": "frobnicate"}, r.token, nil).code, true
		},
		"an id that is not a number": func(t *testing.T, r *controlRig) (int, bool) {
			return r.post("/api/control/abc/run", map[string]any{"verb": "pause"}, r.token, nil).code, true
		},
		"an agent not watched": func(t *testing.T, r *controlRig) (int, bool) {
			return r.post("/api/control/65535/run", map[string]any{"verb": "pause"}, r.token, nil).code, true
		},
		"a cross-site Origin": func(t *testing.T, r *controlRig) (int, bool) {
			return r.post(idPath(r), map[string]any{"verb": "pause"}, r.token, map[string]string{"Origin": "http://evil.example"}).code, true
		},
		"a cross-site Sec-Fetch-Site": func(t *testing.T, r *controlRig) (int, bool) {
			return r.post(idPath(r), map[string]any{"verb": "pause"}, r.token, map[string]string{"Sec-Fetch-Site": "cross-site"}).code, true
		},
		"a Host the guard does not know": func(t *testing.T, r *controlRig) (int, bool) {
			return r.post(idPath(r), map[string]any{"verb": "pause"}, r.token, map[string]string{"Host": "evil.example"}).code, true
		},
		"a PUT": func(t *testing.T, r *controlRig) (int, bool) {
			return r.do(http.MethodPut, idPath(r), []byte(`{"verb":"pause"}`), map[string]string{"Content-Type": "application/json", "Authorization": "Bearer " + r.token}).code, true
		},
	}
	want := map[string]int{
		"no token": 401, "wrong token": 401, "the route-edit token": 401, "eleven wrong tries in a minute": 429,
		"text/plain": 415, "a 5 KiB body": 413, "an unknown field": 400, "an unknown verb": 400,
		"an id that is not a number": 404, "an agent not watched": 404, "a cross-site Origin": 403,
		"a cross-site Sec-Fetch-Site": 403, "a Host the guard does not know": 403, "a PUT": 405,
	}
	for name, f := range cases {
		r := newControlRig(t)
		r.fake.set("dungeon_status", `{"running":true,"state":"running","report":{"state":"running","remote":{"mode":"observe"}}}`)
		r.fake.set("dungeon_pause", `{"ok":true}`)
		code, _ := f(t, r)
		if code != want[name] {
			t.Errorf("%s: %d, want %d", name, code, want[name])
		}
		if r.fake.sawWrite() {
			t.Errorf("%s: a write reached the agent", name)
		}
	}
}

// Test 5: without the controller the controls do not exist and the guard stays as it was.
func TestControlOffByDefault(t *testing.T) {
	p := NewPoller(Options{ConfigDir: t.TempDir()})
	srv := httptest.NewServer(HandlerWithEditor(p, "", nil, nil, nil))
	defer srv.Close()
	req, _ := http.NewRequest(http.MethodPost, srv.URL+"/api/control/8765/run", strings.NewReader(`{"verb":"pause"}`))
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	must(t, err)
	resp.Body.Close()
	if resp.StatusCode != http.StatusMethodNotAllowed {
		t.Errorf("POST with the controls off: %d, want 405", resp.StatusCode)
	}
	resp2, err := http.Get(srv.URL + "/api/control/status")
	must(t, err)
	resp2.Body.Close()
	if resp2.StatusCode != http.StatusNotFound {
		t.Errorf("GET status with the controls off: %d, want 404", resp2.StatusCode)
	}
}

// Test 6: the write client may call the four run controls and nothing else.
func TestControlClientAllowlist(t *testing.T) {
	f := newFakeWriteAgent(t)
	cl := NewControlClient(Agent{ID: "1", Addr: f.srv.URL[len("http://"):]}, time.Second)
	ctx := context.Background()
	bad := []struct {
		name   string
		method string
		params any
	}{
		{"dungeon_run", "dungeon_run", nil},
		{"commander bind", "commander", map[string]any{"op": "bind"}},
		{"commander summon", "commander", map[string]any{"op": "cmd", "verb": "summon"}},
		{"tt", "tt", nil},
		{"eval", "eval", nil},
		{"dungeon_pause with params", "dungeon_pause", map[string]any{"x": 1}},
	}
	for _, c := range bad {
		if _, err := cl.Write(ctx, c.method, c.params); err != ErrNotAllowed {
			t.Errorf("%s: %v, want ErrNotAllowed", c.name, err)
		}
	}
	for _, m := range []string{"status", "chat", "save"} {
		if _, err := cl.Read(ctx, m); err != ErrNotAllowed {
			t.Errorf("Read(%q): %v, want ErrNotAllowed", m, err)
		}
	}
	if got := f.methods(); len(got) != 0 {
		t.Errorf("the fake saw %v, want nothing", got)
	}
	// and the run controls are allowed
	f.set("dungeon_pause", `{"ok":true}`)
	f.set("dungeon_status", `{"running":false}`)
	if _, err := cl.Write(ctx, "dungeon_pause", nil); err != nil {
		t.Errorf("dungeon_pause: %v", err)
	}
	if _, err := cl.Read(ctx, "dungeon_status"); err != nil {
		t.Errorf("dungeon_status: %v", err)
	}
}

// Test 7: the log is opened at start-up and a write that fails stops the control.
func TestControlLogFailClosed(t *testing.T) {
	root := t.TempDir()
	tk, err := routeedit.LoadTokens(writeTokens(t, root))
	must(t, err)
	locked := filepath.Join(root, "locked")
	must(t, os.Mkdir(locked, 0o500))
	t.Cleanup(func() { os.Chmod(locked, 0o700) })
	if _, err := NewController(NewPoller(Options{ConfigDir: root}), ControlConfig{Tokens: tk, LogPath: filepath.Join(locked, "control.jsonl")}); err == nil {
		t.Fatal("the controller started with a log it cannot open")
	}
	r := newControlRig(t)
	r.fake.set("dungeon_status", `{"running":true,"state":"running","report":{"state":"running","remote":{"mode":"observe"}}}`)
	r.fake.set("dungeon_pause", `{"ok":true}`)
	r.ctl.log.w = failingWriter{}
	if rec := r.post("/api/control/"+r.agent.ID+"/run", map[string]any{"verb": "pause", "confirm": true}, r.token, nil); rec.code != http.StatusInternalServerError {
		t.Fatalf("a control whose request line cannot be written: %d %s", rec.code, rec.body)
	}
	if r.fake.sawWrite() {
		t.Fatal("the RPC was sent although the request line could not be written")
	}
}

type failingWriter struct{}

func (failingWriter) Write([]byte) (int, error) { return 0, errors.New("disk full") }

func writeTokens(t *testing.T, root string) string {
	t.Helper()
	p := filepath.Join(root, "control-tokens")
	must(t, os.WriteFile(p, []byte("owner "+controlToken+"\n"), 0o600))
	return p
}

// Test 8: a trusted local caller needs no token but must carry the control nonce (its own, not the editor's).
func TestControlTrustLocal(t *testing.T) {
	r := newControlRig(t)
	must(t, r.ctl.EnableTrustLocal("owner", nil))
	r.ctl.trust.owner = func(netip.AddrPort) (uint32, bool) { return uint32(os.Getuid()), true }
	nonce := r.ctl.trust.nonce
	r.fake.set("dungeon_status", `{"running":true,"state":"running","report":{"state":"running","remote":{"mode":"observe"}}}`)
	r.fake.set("dungeon_pause", `{"ok":true}`)
	path := "/api/control/" + r.agent.ID + "/run"
	if rec := r.post(path, map[string]any{"verb": "pause", "confirm": true}, "", map[string]string{controlNonceHeader: nonce}); rec.code != http.StatusOK {
		t.Fatalf("a trusted pause: %d %s", rec.code, rec.body)
	}
	recs := r.logRecords()
	if len(recs) != 2 || recs[0]["via"] != "trusted" || recs[0]["who"] != "owner" {
		t.Fatalf("the log says %v", recs)
	}
	if rec := r.post(path, map[string]any{"verb": "pause"}, "", nil); rec.code != http.StatusUnauthorized {
		t.Errorf("no nonce: %d, want 401", rec.code)
	}
	other := newTrustRig(t, true) // the route editor's own nonce must not open the controls
	if rec := r.post(path, map[string]any{"verb": "pause"}, "", map[string]string{nonceHeader: other.ed.trust.nonce}); rec.code != http.StatusUnauthorized {
		t.Errorf("the editor's nonce: %d, want 401", rec.code)
	}
	if rec := r.post(path, map[string]any{"verb": "pause"}, "", map[string]string{controlNonceHeader: nonce, "X-Forwarded-For": "198.51.100.7"}); rec.code != http.StatusUnauthorized {
		t.Errorf("a proxy header: %d, want 401", rec.code)
	}
}

// Test 9: one control in flight per agent, and another agent is not blocked by it.
func TestControlOneInFlight(t *testing.T) {
	r := newControlRig(t)
	r.fake.set("dungeon_status", `{"running":true,"state":"running","report":{"state":"running","remote":{"mode":"observe"}}}`)
	r.fake.set("dungeon_pause", `{"ok":true}`)
	gate := r.fake.hold("dungeon_pause")
	done := make(chan reply, 1)
	go func() {
		done <- r.post("/api/control/"+r.agent.ID+"/run", map[string]any{"verb": "pause", "confirm": true}, r.token, nil)
	}()
	deadline := time.Now().Add(5 * time.Second)
	for {
		if _, ok := r.fake.paramsOf("dungeon_pause"); ok {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the first control never reached the agent")
		}
		time.Sleep(5 * time.Millisecond)
	}
	if rec := r.post("/api/control/"+r.agent.ID+"/run", map[string]any{"verb": "pause"}, r.token, nil); rec.code != http.StatusConflict {
		t.Errorf("a second control for the same agent: %d, want 409", rec.code)
	}
	other := newFakeWriteAgent(t)
	otherAgent := r.watch(other)
	other.set("dungeon_status", `{"running":true,"state":"running","report":{"state":"running","remote":{"mode":"observe"}}}`)
	other.set("dungeon_pause", `{"ok":true}`)
	if rec := r.post("/api/control/"+otherAgent.ID+"/run", map[string]any{"verb": "pause", "confirm": true}, r.token, nil); rec.code != http.StatusOK {
		t.Errorf("a control for another agent: %d, want 200", rec.code)
	}
	close(gate) // let the first control finish
	if got := <-done; got.code != http.StatusOK {
		t.Errorf("the blocked control finished with %d: %s", got.code, got.body)
	}
}

// Test 10: the agent's token is never in the reply nor the log.
func TestControlScrubsAgentToken(t *testing.T) {
	r := newControlRig(t)
	r.fake.set("dungeon_status", `{"running":true,"state":"running","report":{"state":"running","remote":{"mode":"observe"}}}`)
	r.fake.fail("dungeon_pause", "the request carried "+r.agentToken)
	rec := r.post("/api/control/"+r.agent.ID+"/run", map[string]any{"verb": "pause", "confirm": true}, r.token, nil)
	if rec.code != http.StatusBadGateway {
		t.Fatalf("an agent error: %d %s", rec.code, rec.body)
	}
	if strings.Contains(rec.body, r.agentToken) {
		t.Errorf("the reply carries the agent token: %s", rec.body)
	}
	if b, err := os.ReadFile(r.logP); err != nil {
		t.Fatal(err)
	} else if strings.Contains(string(b), r.agentToken) {
		t.Errorf("the log carries the agent token: %s", b)
	} else if !strings.Contains(string(b), "[redacted]") {
		t.Errorf("the log does not redact: %s", b)
	}
	// a successful answer that echoes the token is scrubbed too
	r2 := newControlRig(t)
	r2.fake.set("dungeon_status", `{"running":true,"state":"running","report":{"state":"running","remote":{"mode":"observe"}}}`)
	r2.fake.set("dungeon_pause", `{"note":"the token is `+r2.agentToken+`"}`)
	rec2 := r2.post("/api/control/"+r2.agent.ID+"/run", map[string]any{"verb": "pause", "confirm": true}, r2.token, nil)
	if rec2.code != http.StatusOK {
		t.Fatalf("a pause whose answer echoes the token: %d %s", rec2.code, rec2.body)
	}
	if strings.Contains(rec2.body, r2.agentToken) {
		t.Errorf("the reply carries the agent token: %s", rec2.body)
	}
}

// Test 11: the log endpoint answers one agent's last result lines only.
func TestControlLogEndpoint(t *testing.T) {
	r := newControlRig(t)
	r.fake.set("dungeon_status", `{"running":true,"state":"running","report":{"state":"running","remote":{"mode":"observe"}}}`)
	r.fake.set("dungeon_pause", `{"ok":true}`)
	path := "/api/control/" + r.agent.ID + "/run"
	for i := 0; i < 3; i++ {
		if rec := r.post(path, map[string]any{"verb": "pause", "confirm": true}, r.token, nil); rec.code != http.StatusOK {
			t.Fatalf("pause %d: %d %s", i, rec.code, rec.body)
		}
	}
	other := newFakeWriteAgent(t)
	oa := r.watch(other)
	other.set("dungeon_status", `{"running":true,"state":"running","report":{"state":"running","remote":{"mode":"observe"}}}`)
	other.set("dungeon_pause", `{"ok":true}`)
	if rec := r.post("/api/control/"+oa.ID+"/run", map[string]any{"verb": "pause", "confirm": true}, r.token, nil); rec.code != http.StatusOK {
		t.Fatalf("other pause: %d %s", rec.code, rec.body)
	}
	rec := r.do(http.MethodGet, "/api/control/"+r.agent.ID+"/log?n=2", nil, nil)
	if rec.code != http.StatusOK {
		t.Fatalf("log: %d %s", rec.code, rec.body)
	}
	var got struct {
		Lines []map[string]any `json:"lines"`
	}
	must(t, json.Unmarshal([]byte(rec.body), &got))
	if len(got.Lines) != 2 {
		t.Fatalf("the log answered %d lines: %s", len(got.Lines), rec.body)
	}
	for _, l := range got.Lines {
		if l["agent"] != r.agent.ID || l["phase"] != "result" || l["ok"] != true {
			t.Errorf("a line is %v", l)
		}
	}
}

// Test 13: the run controls' button matrix, confirm texts and log lines (controls.js, run under node).
func TestControlButtons(t *testing.T) { runCheck(t, "controls_check.js", "controls.js") }
