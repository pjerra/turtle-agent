package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/pjerra/turtle-agent/internal/rpc"
)

// rpcTestAgent starts the REAL internal/rpc server (not a fake handler) with
// token auth and records each request's Content-Type. Mutation: serving a
// hand-rolled handler instead would still answer calls but would not enforce
// the 415/401 behaviour the tests below pin.
func rpcCLITestAgent(t *testing.T, token string, seenCT *string) *httptest.Server {
	t.Helper()
	srv := rpc.NewServer()
	srv.SetToken(token)
	srv.Register("status", rpc.Typed(func(ctx context.Context, _ struct{}) (map[string]string, error) {
		return map[string]string{"state": "in_world"}, nil
	}))
	srv.Register("add", rpc.Typed(func(ctx context.Context, p struct {
		A int `json:"a"`
		B int `json:"b"`
	}) (int, error) {
		return p.A + p.B, nil
	}))
	srv.Register("join", rpc.Typed(func(ctx context.Context, p []string) (string, error) {
		return strings.Join(p, "-"), nil
	}))
	srv.Register("fail", rpc.Typed(func(ctx context.Context, _ struct{}) (string, error) {
		return "", &rpc.Error{Code: -32000, Message: "boom " + token}
	}))
	srv.Register("leak", rpc.Typed(func(ctx context.Context, _ struct{}) (string, error) {
		return "holder " + token, nil
	}))
	h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if seenCT != nil {
			*seenCT = r.Header.Get("Content-Type")
		}
		srv.Handler().ServeHTTP(w, r)
	})
	ts := httptest.NewServer(h)
	t.Cleanup(ts.Close)
	return ts
}

// rpcTestPort returns the loopback port a test server listens on.
func rpcCLITestPort(t *testing.T, ts *httptest.Server) int {
	t.Helper()
	u, err := url.Parse(ts.URL)
	if err != nil {
		t.Fatal(err)
	}
	_, portS, err := net.SplitHostPort(u.Host)
	if err != nil {
		t.Fatal(err)
	}
	var port int
	if _, err := fmt.Sscanf(portS, "%d", &port); err != nil {
		t.Fatal(err)
	}
	return port
}

// rpcTestDir writes a slot dir like slot setup does: slot.json plus the
// token file derived from the validated port. Mutation: reading
// manifest.TokenFile instead of the derived path passes every test here
// except TestRPCRefusesOutsideTokenPath.
func rpcCLITestDir(t *testing.T, port int, token string) string {
	t.Helper()
	dir := t.TempDir()
	m := teardownSlotManifest{
		Server:    "m910q",
		Slot:      "1",
		Holder:    "w-a",
		LeaseID:   "lease-1",
		Account:   "TESTACC",
		Character: "Gbmage",
		Port:      port,
		Dir:       dir,
		Binary:    filepath.Join(dir, "turtle-agent"),
		Settings:  filepath.Join(dir, "settings.json"),
		PIDFile:   filepath.Join(dir, "agent.pid"),
		TokenFile: filepath.Join(dir, fmt.Sprintf("rpc-%d.token", port)),
		Created:   "2026-10-28T00:00:00Z",
	}
	raw, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "slot.json"), raw, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, fmt.Sprintf("rpc-%d.token", port)), []byte(token+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	return dir
}

func rpcCLITestToken(t *testing.T) string {
	t.Helper()
	token, err := rpc.NewToken()
	if err != nil {
		t.Fatal(err)
	}
	return token
}

// A call returns the result, pretty-printed, with no token anywhere.
// Mutation: dropping the Authorization header gets a bare 401 (no result).
func TestRPCCallReturnsResult(t *testing.T) {
	token := rpcCLITestToken(t)
	var ct string
	ts := rpcCLITestAgent(t, token, &ct)
	dir := rpcCLITestDir(t, rpcCLITestPort(t, ts), token)
	code, out, errOut := runCLI("rpc", "--timeout", "5s", dir, "status")
	if code != exitOK {
		t.Fatalf("code %d stdout %q stderr %q", code, out, errOut)
	}
	var got map[string]string
	if err := json.Unmarshal([]byte(out), &got); err != nil || got["state"] != "in_world" {
		t.Fatalf("result %q: %v", out, err)
	}
	if !strings.Contains(out, "\n") {
		t.Fatalf("want pretty JSON, got %q", out)
	}
	if strings.Contains(out+errOut, token) {
		t.Fatal("token in output")
	}
	// The call above succeeded against a server that refuses requests
	// without Content-Type, so the recorded header proves the client sets
	// it. Mutation: dropping req.Header.Set("Content-Type", ...) fails here.
	if ct != "application/json" {
		t.Fatalf("Content-Type %q", ct)
	}
}

// A missing Content-Type would be refused: the real server answers 415.
func TestRPCMissingContentTypeRefused(t *testing.T) {
	token := rpcCLITestToken(t)
	ts := rpcCLITestAgent(t, token, nil)
	req, err := http.NewRequest("POST", ts.URL+"/rpc", strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"status"}`))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnsupportedMediaType {
		t.Fatalf("status %d, want 415", resp.StatusCode)
	}
}

// --raw prints the result compactly.
func TestRPCRawIsCompact(t *testing.T) {
	token := rpcCLITestToken(t)
	ts := rpcCLITestAgent(t, token, nil)
	dir := rpcCLITestDir(t, rpcCLITestPort(t, ts), token)
	code, out, errOut := runCLI("rpc", "--raw", dir, "status")
	if code != exitOK {
		t.Fatalf("code %d stdout %q stderr %q", code, out, errOut)
	}
	if strings.Contains(strings.TrimSpace(out), "\n") {
		t.Fatalf("want one line, got %q", out)
	}
	if strings.Contains(out+errOut, token) {
		t.Fatal("token in output")
	}
}

// Params forms: object, array, "-" from stdin, omitted (status above).
// Mutation: sending params as a query string or form breaks add/join.
func TestRPCParamsForms(t *testing.T) {
	token := rpcCLITestToken(t)
	ts := rpcCLITestAgent(t, token, nil)
	dir := rpcCLITestDir(t, rpcCLITestPort(t, ts), token)
	if code, out, errOut := runCLI("rpc", dir, "add", `{"a":2,"b":3}`); code != exitOK || strings.TrimSpace(out) != "5" {
		t.Fatalf("object: code %d stdout %q stderr %q", code, out, errOut)
	}
	if code, out, errOut := runCLI("rpc", dir, "join", `["a","b"]`); code != exitOK || strings.TrimSpace(out) != `"a-b"` {
		t.Fatalf("array: code %d stdout %q stderr %q", code, out, errOut)
	}
	oldStdin := rpcStdin
	rpcStdin = strings.NewReader(`{"a":20,"b":22}`)
	defer func() { rpcStdin = oldStdin }()
	if code, out, errOut := runCLI("rpc", dir, "add", "-"); code != exitOK || strings.TrimSpace(out) != "42" {
		t.Fatalf("stdin: code %d stdout %q stderr %q", code, out, errOut)
	}
}

// A JSON-RPC error prints `error: <code> <message>` and exits 16, with the
// token redacted even when the server's error text contains it.
// Mutation: printing the raw message (no rpcScrub) fails the absence checks.
func TestRPCErrorExitAndRedaction(t *testing.T) {
	token := rpcCLITestToken(t)
	ts := rpcCLITestAgent(t, token, nil)
	dir := rpcCLITestDir(t, rpcCLITestPort(t, ts), token)
	code, out, errOut := runCLI("rpc", dir, "fail")
	if code != exitRPCError {
		t.Fatalf("code %d stdout %q stderr %q, want %d", code, out, errOut, exitRPCError)
	}
	if !strings.Contains(errOut, "error: -32000 boom") {
		t.Fatalf("stderr %q", errOut)
	}
	if !strings.Contains(errOut, "<token>") {
		t.Fatalf("no redaction marker in %q", errOut)
	}
	if strings.Contains(out+errOut, token) {
		t.Fatal("token in output")
	}
}

// A result echoing the token is redacted too, including with --raw.
// Mutation: scrubbing only stderr fails here.
func TestRPCRedactsTokenInResult(t *testing.T) {
	token := rpcCLITestToken(t)
	ts := rpcCLITestAgent(t, token, nil)
	dir := rpcCLITestDir(t, rpcCLITestPort(t, ts), token)
	for _, extra := range [][]string{{}, {"--raw"}} {
		args := append([]string{"rpc", dir, "leak"}, extra...)
		code, out, errOut := runCLI(args...)
		if code != exitOK {
			t.Fatalf("%v: code %d stdout %q stderr %q", extra, code, out, errOut)
		}
		if strings.Contains(out+errOut, token) {
			t.Fatalf("%v: token in output %q %q", extra, out, errOut)
		}
		if !strings.Contains(out, "<token>") {
			t.Fatalf("%v: no redaction marker in %q", extra, out)
		}
	}
}

// Agent not running: exit 15, the token never shown.
// Mutation: exiting 0/2 here, or printing the raw dial error with the
// token-bearing URL, fails.
func TestRPCUnreachable(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := ln.Addr().(*net.TCPAddr).Port
	ln.Close()
	token := rpcCLITestToken(t)
	dir := rpcCLITestDir(t, port, token)
	code, out, errOut := runCLI("rpc", "--timeout", "2s", dir, "status")
	if code != exitSlotRefused {
		t.Fatalf("code %d stdout %q stderr %q, want %d", code, out, errOut, exitSlotRefused)
	}
	if !strings.Contains(errOut, "cannot reach") {
		t.Fatalf("stderr %q", errOut)
	}
	if strings.Contains(out+errOut, token) {
		t.Fatal("token in output")
	}
}

// A slot.json pointing the token outside the dir is refused before anything
// is read. Mutation: deriving nothing from the manifest (using its
// token_file string) reads the outside file instead of failing.
func TestRPCRefusesOutsideTokenPath(t *testing.T) {
	token := rpcCLITestToken(t)
	ts := rpcCLITestAgent(t, token, nil)
	dir := rpcCLITestDir(t, rpcCLITestPort(t, ts), token)
	outside := filepath.Join(t.TempDir(), "precious.token")
	if err := os.WriteFile(outside, []byte("precious"), 0o600); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join(dir, "slot.json"))
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatal(err)
	}
	m["token_file"] = outside
	tampered, _ := json.Marshal(m)
	if err := os.WriteFile(filepath.Join(dir, "slot.json"), tampered, 0o600); err != nil {
		t.Fatal(err)
	}
	code, _, errOut := runCLI("rpc", dir, "status")
	if code != exitUsage {
		t.Fatalf("code %d stderr %q, want %d", code, errOut, exitUsage)
	}
	if !strings.Contains(errOut, "outside") {
		t.Fatalf("no outside-dir refusal: %q", errOut)
	}
	if kept, _ := os.ReadFile(outside); string(kept) != "precious" {
		t.Fatal("outside file touched")
	}
	if _, err := os.Stat(filepath.Join(dir, "slot.json")); err != nil {
		t.Fatalf("slot.json touched: %v", err)
	}
}

// Flags work before and after <dir> (splitPositionals, as teardown pins).
// Mutation: restoring leading-only parsing fails the first call.
func TestRPCFlagOrder(t *testing.T) {
	token := rpcCLITestToken(t)
	ts := rpcCLITestAgent(t, token, nil)
	dir := rpcCLITestDir(t, rpcCLITestPort(t, ts), token)
	aCode, aOut, _ := runCLI("rpc", "--timeout", "5s", "--raw", dir, "status")
	bCode, bOut, _ := runCLI("rpc", dir, "status", "--timeout", "5s", "--raw")
	if aCode != exitOK || bCode != exitOK {
		t.Fatalf("codes %d %d", aCode, bCode)
	}
	if aOut != bOut {
		t.Fatalf("flag order changed output: %q vs %q", aOut, bOut)
	}
}

// Usage errors exit 2: no method, extra positional, bad params, bad timeout,
// missing dir.
func TestRPCUsage(t *testing.T) {
	token := rpcCLITestToken(t)
	ts := rpcCLITestAgent(t, token, nil)
	dir := rpcCLITestDir(t, rpcCLITestPort(t, ts), token)
	for name, args := range map[string][]string{
		"no method":        {"rpc", dir},
		"no dir":           {"rpc"},
		"extra positional": {"rpc", dir, "status", "{}", "x"},
		"scalar params":    {"rpc", dir, "add", "42"},
		"bad params JSON":  {"rpc", dir, "add", `{"a":`},
		"bad timeout":      {"rpc", "--timeout", "bogus", dir, "status"},
		"missing dir":      {"rpc", filepath.Join(dir, "no-such-dir"), "status"},
	} {
		if code, _, _ := runCLI(args...); code != exitUsage {
			t.Errorf("%s: code %d, want %d", name, code, exitUsage)
		}
	}
}

// Every bad envelope exits 15 with the bad-reply line, never 0: the body
// must be an object with "jsonrpc":"2.0", "id" 1 and exactly one of a
// present "result" or a well-shaped "error" (numeric code, string message);
// a non-200 status counts too. The replies come from an in-process HTTP
// server, so the client never touches the real agent here. Mutation:
// accepting the old unmarshal-only envelope (no id/version/exactly-one
// check, no status check, errors.As-free "cannot reach" for everything)
// lets one of these bodies exit 0 or print the wrong line.
func TestRPCBadRepliesExit15(t *testing.T) {
	token := rpcCLITestToken(t)
	for name, tc := range map[string]struct {
		status int
		body   string
	}{
		"empty object":               {200, `{}`},
		"null":                       {200, `null`},
		"array":                      {200, `[]`},
		"wrong id":                   {200, `{"jsonrpc":"2.0","id":2,"result":{"state":"in_world"}}`},
		"missing id":                 {200, `{"jsonrpc":"2.0","result":{"state":"in_world"}}`},
		"missing version":            {200, `{"id":1,"result":{"state":"in_world"}}`},
		"bad version":                {200, `{"jsonrpc":"1.0","id":1,"result":{"state":"in_world"}}`},
		"both":                       {200, `{"jsonrpc":"2.0","id":1,"result":{},"error":{"code":1,"message":"x"}}`},
		"neither":                    {200, `{"jsonrpc":"2.0","id":1}`},
		"null result and null error": {200, `{"jsonrpc":"2.0","id":1,"result":null,"error":null}`},
		"error without code":         {200, `{"jsonrpc":"2.0","id":1,"error":{"message":"x"}}`},
		"error string code":          {200, `{"jsonrpc":"2.0","id":1,"error":{"code":"1","message":"x"}}`},
		"error without message":      {200, `{"jsonrpc":"2.0","id":1,"error":{"code":1}}`},
		"error not an object":        {200, `{"jsonrpc":"2.0","id":1,"error":"boom"}`},
		"not JSON":                   {200, `not json`},
		"teapot":                     {http.StatusTeapot, `{"jsonrpc":"2.0","id":1,"result":{}}`},
	} {
		ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Header.Get("Authorization") != "Bearer "+token {
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(tc.status)
			fmt.Fprint(w, tc.body)
		}))
		t.Cleanup(ts.Close)
		dir := rpcCLITestDir(t, rpcCLITestPort(t, ts), token)
		code, out, errOut := runCLI("rpc", "--timeout", "5s", dir, "status")
		if code != exitSlotRefused {
			t.Errorf("%s: code %d stdout %q stderr %q, want %d", name, code, out, errOut, exitSlotRefused)
		}
		if !strings.Contains(errOut, "rpc: bad reply from the agent:") {
			t.Errorf("%s: stderr %q has no bad-reply line", name, errOut)
		}
		if out != "" {
			t.Errorf("%s: stdout %q, want empty", name, out)
		}
		if strings.Contains(out+errOut, token) {
			t.Errorf("%s: token in output", name)
		}
	}
}

// A non-agent token file is refused before any request, with the fixed
// line and exit 15, and the content is never printed; a valid token the
// server echoes is still redacted. Mutation: skipping the shape check
// (using the raw file as the bearer) sends the quotes/backslash/markup
// to the server or prints them; scrubbing only and printing the content
// leaks the file.
func TestRPCRefusesNonAgentToken(t *testing.T) {
	token := rpcCLITestToken(t)
	ts := rpcCLITestAgent(t, token, nil)
	port := rpcCLITestPort(t, ts)
	bad := map[string]string{
		"quotes":        `"0123456789012345678901234567890123456789ab"`,
		"backslash":     `0123456789012345678901234567890123456789a\`,
		"markup":        `012345678901234567890123456789012345678<ab`,
		"inner newline": `0123456789012345678901234567890123456789a` + "\n" + `x`,
		"too short":     `short`,
		"too long":      token + "x",
	}
	for name, content := range bad {
		dir := rpcCLITestDir(t, port, token)
		if err := os.WriteFile(filepath.Join(dir, fmt.Sprintf("rpc-%d.token", port)), []byte(content+"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		code, out, errOut := runCLI("rpc", "--timeout", "5s", dir, "status")
		if code != exitSlotRefused {
			t.Errorf("%s: code %d stdout %q stderr %q, want %d", name, code, out, errOut, exitSlotRefused)
		}
		if !strings.Contains(errOut, "rpc: the token file is not an agent token") {
			t.Errorf("%s: stderr %q has no not-an-agent-token line", name, errOut)
		}
		for _, line := range strings.Split(out+"\n"+errOut, "\n") {
			if got := strings.TrimSpace(content); got != "" && strings.Contains(line, got) {
				t.Errorf("%s: token file content in output %q %q", name, out, errOut)
				break
			}
		}
	}
	// A valid echoed token is still redacted on stdout.
	dir := rpcCLITestDir(t, port, token)
	code, out, errOut := runCLI("rpc", "--raw", dir, "leak")
	if code != exitOK {
		t.Fatalf("valid token: code %d stdout %q stderr %q", code, out, errOut)
	}
	if strings.Contains(out+errOut, token) {
		t.Fatal("valid token in output")
	}
	if !strings.Contains(out, "<token>") {
		t.Fatalf("no redaction marker in %q", out)
	}
}
