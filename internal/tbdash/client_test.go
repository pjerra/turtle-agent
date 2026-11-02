package tbdash

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// fakePassword is a made-up value: the tests prove it never reaches an error text.
const fakePassword = "not-a-real-dashboard-password"

func writePassFile(t *testing.T, mode os.FileMode) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "tbdash.pass")
	if err := os.WriteFile(path, []byte(fakePassword+"\n"), 0o600); err != nil {
		t.Fatalf("writing the fake password file: %v", err)
	}
	if err := os.Chmod(path, mode); err != nil {
		t.Fatalf("chmod %04o: %v", mode, err)
	}
	return path
}

func newClient(t *testing.T, base string, mode os.FileMode) *Client {
	t.Helper()
	c, err := New(base, "TADASH", writePassFile(t, mode))
	if err != nil {
		t.Fatalf("New(%q): %v", base, err)
	}
	return c
}

// captureDiagnostics redirects the standard logger and os.Stderr while a call runs, so a test can
// prove that no diagnostic path prints the password. The returned stop function restores both and
// returns everything written.
func captureDiagnostics(t *testing.T) func() string {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	oldStderr, oldLog := os.Stderr, log.Writer()
	os.Stderr = w
	log.SetOutput(w)
	return func() string {
		log.SetOutput(oldLog)
		os.Stderr = oldStderr
		if err := w.Close(); err != nil {
			t.Errorf("closing the capture pipe: %v", err)
		}
		written, err := io.ReadAll(r)
		if err != nil {
			t.Errorf("reading the capture pipe: %v", err)
		}
		r.Close()
		return string(written)
	}
}

func TestLoginReadsPasswordFileAndKeepsCookie(t *testing.T) {
	const token = "session-token-from-the-daemon"
	var logins int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/auth/login":
			if r.Method != http.MethodPost {
				t.Errorf("login method %s, want POST", r.Method)
			}
			var req struct {
				Username string `json:"username"`
				Password string `json:"password"`
			}
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				t.Errorf("login body: %v", err)
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			if req.Username != "TADASH" || req.Password != fakePassword {
				t.Errorf("login sent user %q, want TADASH and the password file's value (the trailing newline trimmed)", req.Username)
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			atomic.AddInt32(&logins, 1)
			http.SetCookie(w, &http.Cookie{Name: "tbdash_session", Value: token, Path: "/"})
			fmt.Fprint(w, `{"success":true}`)
		case "/api/v1/commanders":
			c, err := r.Cookie("tbdash_session")
			if err != nil || c.Value != token {
				w.WriteHeader(http.StatusUnauthorized)
				fmt.Fprint(w, `{"error":"Unauthorized: GM session required"}`)
				return
			}
			fmt.Fprint(w, `{"commanders":[]}`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	c := newClient(t, srv.URL, 0o600)
	if err := c.Login(context.Background()); err != nil {
		t.Fatalf("Login: %v", err)
	}
	got, err := c.GetJSON(context.Background(), "/api/v1/commanders", nil)
	if err != nil {
		t.Fatalf("GetJSON after login: %v", err)
	}
	if string(got) != `{"commanders":[]}` {
		t.Errorf("GetJSON = %s", got)
	}
	if n := atomic.LoadInt32(&logins); n != 1 {
		t.Errorf("%d logins, want 1", n)
	}
}

func TestPasswordFileWithLooseModeIsRefused(t *testing.T) {
	var requests int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&requests, 1)
		fmt.Fprint(w, `{"success":true}`)
	}))
	defer srv.Close()

	c := newClient(t, srv.URL, 0o644)
	err := c.Login(context.Background())
	if err == nil {
		t.Fatal("Login accepted a 0644 password file")
	}
	if !strings.Contains(err.Error(), "0644") {
		t.Errorf("error %q does not name the file's mode", err)
	}
	if strings.Contains(err.Error(), fakePassword) {
		t.Errorf("error %q leaks the password", err)
	}
	if n := atomic.LoadInt32(&requests); n != 0 {
		t.Errorf("%d requests reached the daemon, want 0", n)
	}
}

func TestPasswordNeverInErrors(t *testing.T) {
	t.Run("login", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			// A server that echoes the credential back in its error body.
			w.WriteHeader(http.StatusInternalServerError)
			fmt.Fprintf(w, `{"error":"cannot log in with %s"}`, fakePassword)
		}))
		defer srv.Close()
		c := newClient(t, srv.URL, 0o600)
		stop := captureDiagnostics(t)
		err := c.Login(context.Background())
		diagnostics := stop()
		if err == nil {
			t.Fatal("Login succeeded against a 500")
		}
		if !strings.Contains(err.Error(), "500") {
			t.Errorf("error %q does not report the status", err)
		}
		if strings.Contains(err.Error(), fakePassword) {
			t.Fatalf("error %q leaks the password", err)
		}
		if strings.Contains(diagnostics, fakePassword) {
			t.Fatalf("diagnostics %q leak the password", diagnostics)
		}
	})

	t.Run("get", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path == "/api/v1/auth/login" {
				http.SetCookie(w, &http.Cookie{Name: "s", Value: "v", Path: "/"})
				fmt.Fprint(w, `{"success":true}`)
				return
			}
			w.WriteHeader(http.StatusInternalServerError)
			fmt.Fprintf(w, `{"error":"boom %s"}`, fakePassword)
		}))
		defer srv.Close()
		c := newClient(t, srv.URL, 0o600)
		stop := captureDiagnostics(t)
		if err := c.Login(context.Background()); err != nil {
			stop()
			t.Fatalf("Login: %v", err)
		}
		_, err := c.Commanders(context.Background())
		diagnostics := stop()
		if err == nil {
			t.Fatal("Commanders succeeded against a 500")
		}
		if strings.Contains(err.Error(), fakePassword) {
			t.Fatalf("error %q leaks the password", err)
		}
		if strings.Contains(diagnostics, fakePassword) {
			t.Fatalf("diagnostics %q leak the password", diagnostics)
		}
	})
}

func TestReLoginOnceOn401(t *testing.T) {
	var logins, gets int32
	var reject atomic.Bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/auth/login":
			atomic.AddInt32(&logins, 1)
			http.SetCookie(w, &http.Cookie{Name: "s", Value: "v", Path: "/"})
			fmt.Fprint(w, `{"success":true}`)
		case "/api/v1/commanders":
			n := atomic.AddInt32(&gets, 1)
			if reject.Load() || n == 1 {
				w.WriteHeader(http.StatusUnauthorized)
				fmt.Fprint(w, `{"error":"Unauthorized: GM session required"}`)
				return
			}
			fmt.Fprint(w, `{"commanders":[]}`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	c := newClient(t, srv.URL, 0o600)
	if _, err := c.Commanders(context.Background()); err != nil {
		t.Fatalf("Commanders after the 401: %v", err)
	}
	if n := atomic.LoadInt32(&logins); n != 1 {
		t.Fatalf("%d logins after the first 401, want 1", n)
	}
	if n := atomic.LoadInt32(&gets); n != 2 {
		t.Fatalf("%d GETs after the first 401, want 2 (one retry)", n)
	}

	reject.Store(true)
	_, err := c.Commanders(context.Background())
	var httpErr *HTTPError
	if !errors.As(err, &httpErr) || httpErr.Code != http.StatusUnauthorized {
		t.Fatalf("second 401 gave %v, want *HTTPError 401", err)
	}
	if n := atomic.LoadInt32(&logins); n != 2 {
		t.Errorf("%d logins, want 2 (one per 401, no loop)", n)
	}
	if n := atomic.LoadInt32(&gets); n != 4 {
		t.Errorf("%d GETs, want 4 (one retry, no loop)", n)
	}
}

func TestFightKeyIsPathEscaped(t *testing.T) {
	const key = "Glacienne\x001793219890\x004-5"
	const want = "/api/v1/combat/fight/Glacienne%001793219890%004-5"
	var seen string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v1/auth/login" {
			http.SetCookie(w, &http.Cookie{Name: "s", Value: "v", Path: "/"})
			fmt.Fprint(w, `{"success":true}`)
			return
		}
		seen = r.URL.EscapedPath()
		fmt.Fprint(w, `{"id":"4-5"}`)
	}))
	defer srv.Close()

	c := newClient(t, srv.URL, 0o600)
	if err := c.Login(context.Background()); err != nil {
		t.Fatalf("Login: %v", err)
	}
	if _, err := c.Fight(context.Background(), key); err != nil {
		t.Fatalf("Fight: %v", err)
	}
	if seen != want {
		t.Fatalf("request path %q, want %q", seen, want)
	}
}

func TestDecisionsListRoute(t *testing.T) {
	var query string
	ended := "2026-10-29T20:05:00Z"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v1/auth/login" {
			http.SetCookie(w, &http.Cookie{Name: "s", Value: "v", Path: "/"})
			fmt.Fprint(w, `{"success":true}`)
			return
		}
		if r.URL.Path != "/api/v1/decisions" {
			http.NotFound(w, r)
			return
		}
		query = r.URL.RawQuery
		fmt.Fprintf(w, `{"captures":[{"id":"20261029T200000Z-Commander-onyxias_lair","commander":"Commander","plan":"onyxias_lair","map":1,"started":"2026-10-29T20:00:00Z","ended":%q,"end_reason":"done","segments":[{"file":"seg-0000.jsonl","first_seq":1,"last_seq":10,"records":10,"bytes":100,"deleted":false}]}]}`, ended)
	}))
	defer srv.Close()
	c := newClient(t, srv.URL, 0o600)
	if err := c.Login(context.Background()); err != nil {
		t.Fatalf("Login: %v", err)
	}
	since := time.Date(2026, 10, 29, 19, 59, 50, 0, time.UTC)
	until := time.Date(2026, 10, 29, 20, 10, 0, 0, time.UTC)
	caps, err := c.Decisions(context.Background(), "Commander", since, until)
	if err != nil {
		t.Fatalf("Decisions: %v", err)
	}
	if len(caps) != 1 || caps[0].Ended == nil {
		t.Fatalf("captures %+v, want one ended capture", caps)
	}
	want := "commander=Commander&since=" + url.QueryEscape("2026-10-29T19:59:50Z") + "&until=" + url.QueryEscape("2026-10-29T20:10:00Z")
	if query != want {
		t.Fatalf("query %q, want %q", query, want)
	}
	// Null ended parses to nil.
	srv2 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"captures":[{"id":"20261029T200000Z-Commander-onyxias_lair","commander":"Commander","plan":"p","map":1,"started":"2026-10-29T20:00:00Z","ended":null,"segments":[]}]}`)
	}))
	defer srv2.Close()
	c2 := newClient(t, srv2.URL, 0o600)
	if err := c2.Login(context.Background()); err != nil {
		t.Fatalf("Login: %v", err)
	}
	caps2, err := c2.Decisions(context.Background(), "", time.Time{}, time.Time{})
	if err != nil {
		t.Fatalf("Decisions: %v", err)
	}
	if len(caps2) != 1 || caps2[0].Ended != nil {
		t.Fatalf("captures %+v, want one unended capture", caps2)
	}
}

func TestDecisionFileValidationAndLimits(t *testing.T) {
	var hits int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v1/auth/login" {
			http.SetCookie(w, &http.Cookie{Name: "s", Value: "v", Path: "/"})
			fmt.Fprint(w, `{"success":true}`)
			return
		}
		hits++
		body := "0123456789"
		w.Header().Set("Content-Type", "application/x-ndjson")
		w.Header().Set("Content-Length", fmt.Sprint(len(body)))
		fmt.Fprint(w, body)
	}))
	defer srv.Close()
	c := newClient(t, srv.URL, 0o600)
	if err := c.Login(context.Background()); err != nil {
		t.Fatalf("Login: %v", err)
	}
	id := "20261029T200000Z-Commander-onyxias_lair"
	var dst bytes.Buffer
	n, err := c.DecisionFile(context.Background(), id, "index.json", &dst, 1024)
	if err != nil || n != 10 {
		t.Fatalf("DecisionFile = %d, %v, want 10, nil", n, err)
	}
	if _, err := c.DecisionFile(context.Background(), "../evil", "index.json", &dst, 1024); err == nil {
		t.Fatal("bad id: no error, want client-side refusal with no request")
	}
	if _, err := c.DecisionFile(context.Background(), id, "evil.json", &dst, 1024); err == nil {
		t.Fatal("bad file: no error, want client-side refusal with no request")
	}
	if hits != 1 {
		t.Fatalf("%d server hits, want 1 (validation happens before any request)", hits)
	}
	var small bytes.Buffer
	if _, err := c.DecisionFile(context.Background(), id, "seg-0000.jsonl", &small, 4); err == nil {
		t.Fatal("over-max file: no error, want a limit error")
	}
}
