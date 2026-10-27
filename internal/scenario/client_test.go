package scenario

import (
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestHTTPCallerSendsTheTokenAndHidesIt pins the client's auth behaviour:
// the bearer token from a 0600 file goes out; a 401 never surfaces the
// token; a loose token file and a non-loopback address fail before any dial.
func TestHTTPCallerSendsTheTokenAndHidesIt(t *testing.T) {
	dir := t.TempDir()
	tokPath := filepath.Join(dir, "rpc-8774.token")
	if err := os.WriteFile(tokPath, []byte("tok-"+strings.Repeat("s", 40)+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	var gotAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": 1, "result": map[string]any{"ok": true}})
	}))
	defer srv.Close()
	addr := strings.TrimPrefix(srv.URL, "http://")
	c := &HTTPCaller{Addr: addr, TokenFile: tokPath, HTTP: srv.Client()}
	raw, err := c.Call(t.Context(), "party", map[string]any{}, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), "ok") {
		t.Fatalf("%s", raw)
	}
	if !strings.HasPrefix(gotAuth, "Bearer tok-") || strings.Contains(gotAuth, "\n") {
		t.Fatalf("auth %q", gotAuth)
	}
	if c.Token() == "" || !strings.HasPrefix(c.Token(), "tok-") {
		t.Fatalf("token %q", c.Token())
	}

	// A 401 error text never contains the token.
	bad := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": 1, "error": map[string]any{"code": -32001, "message": "no"}})
	}))
	defer bad.Close()
	c2 := &HTTPCaller{Addr: strings.TrimPrefix(bad.URL, "http://"), TokenFile: tokPath, HTTP: bad.Client()}
	_, err = c2.Call(t.Context(), "party", map[string]any{}, time.Second)
	if err == nil || strings.Contains(err.Error(), c2.Token()) {
		t.Fatalf("leaked: %v", err)
	}

	// A 0644 token file is refused.
	loose := filepath.Join(dir, "loose.token")
	if err := os.WriteFile(loose, []byte("x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	c3 := &HTTPCaller{Addr: addr, TokenFile: loose, HTTP: srv.Client()}
	if _, err := c3.Call(t.Context(), "party", map[string]any{}, time.Second); err == nil {
		t.Fatal("loose token accepted")
	}

	// A non-loopback address fails before any dial.
	c4 := &HTTPCaller{Addr: "10.0.0.5:8774", TokenFile: tokPath, HTTP: srv.Client()}
	if _, err := c4.Call(t.Context(), "party", map[string]any{}, time.Second); err == nil ||
		!strings.Contains(err.Error(), "not a loopback address") {
		t.Fatalf("got %v", err)
	}
	// A userinfo smuggling a host ("@") fails before any dial: the token
	// must never go to evil.example however Sscanf parses the port.
	c5 := &HTTPCaller{Addr: "127.0.0.1:80@evil.example", TokenFile: tokPath, HTTP: srv.Client()}
	if _, err := c5.Call(t.Context(), "party", map[string]any{}, time.Second); err == nil ||
		!strings.Contains(err.Error(), "not a loopback address") {
		t.Fatalf("got %v", err)
	}
	_ = base64.StdEncoding
}
