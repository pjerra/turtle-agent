package tbdash

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
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
