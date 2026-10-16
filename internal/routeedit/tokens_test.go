package routeedit

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const (
	tokA = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	tokB = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
)

func tokenFile(t *testing.T, mode os.FileMode, body string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "tokens")
	if err := os.WriteFile(p, []byte(body), mode); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(p, mode); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestLoadTokensGuards(t *testing.T) {
	good := "# owners\nowner " + tokA + "\n\nhypeer " + tokB + "\n"
	if _, err := LoadTokens(tokenFile(t, 0o600, good)); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadTokens(tokenFile(t, 0o400, good)); err != nil {
		t.Errorf("read-only for the owner is fine: %v", err)
	}
	for name, c := range map[string]struct {
		mode os.FileMode
		body string
	}{
		"group readable":  {0o640, good},
		"world readable":  {0o644, good},
		"empty":           {0o600, "# nothing\n"},
		"one field":       {0o600, "owner\n"},
		"three fields":    {0o600, "owner " + tokA + " extra\n"},
		"short token":     {0o600, "owner short\n"},
		"bad label":       {0o600, "../owner " + tokA + "\n"},
		"bad token chars": {0o600, "owner " + strings.Repeat("a", 31) + "!\n"},
		"same label":      {0o600, "owner " + tokA + "\nowner " + tokB + "\n"},
		"same token":      {0o600, "a " + tokA + "\nb " + tokA + "\n"},
	} {
		if _, err := LoadTokens(tokenFile(t, c.mode, c.body)); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	if _, err := LoadTokens(filepath.Join(t.TempDir(), "missing")); err == nil {
		t.Error("a missing file")
	}
	dir := t.TempDir()
	real := tokenFile(t, 0o600, good)
	link := filepath.Join(dir, "link")
	if err := os.Symlink(real, link); err == nil {
		if _, err := LoadTokens(link); err == nil {
			t.Error("a link to a token file")
		}
	}
	if _, err := LoadTokens(dir); err == nil {
		t.Error("a folder")
	}
}

func TestAuthenticate(t *testing.T) {
	tk, err := LoadTokens(tokenFile(t, 0o600, "owner "+tokA+"\nhypeer "+tokB+"\n"))
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 10, 16, 1, 0, 0, 0, time.UTC)
	if who, err := tk.Authenticate("Bearer "+tokB, "1.2.3.4", now); err != nil || who != "hypeer" {
		t.Errorf("%q %v", who, err)
	}
	if who, err := tk.Authenticate("Bearer "+tokA, "1.2.3.4", now); err != nil || who != "owner" {
		t.Errorf("%q %v", who, err)
	}
	for i, h := range []string{"", "Bearer", "Bearer ", tokA, "bearer " + tokA, "Basic " + tokA, "Bearer " + tokA + " ", "Bearer  " + tokA, "Bearer " + tokA[:len(tokA)-1], "Bearer " + tokA + "x", "Bearer " + strings.ToUpper(tokA)} {
		if _, err := tk.Authenticate(h, "10.0.0."+string(rune('1'+i)), now); err != ErrUnauthorized { // one host each: the limit is another test
			t.Errorf("%q: %v", h, err)
		}
	}
}

func TestFailedAttemptsAreLimitedPerHost(t *testing.T) {
	tk, _ := LoadTokens(tokenFile(t, 0o600, "owner "+tokA+"\n"))
	now := time.Date(2026, 10, 16, 1, 0, 0, 0, time.UTC)
	for i := 0; i < maxFails; i++ {
		if _, err := tk.Authenticate("Bearer nope", "9.9.9.9", now.Add(time.Duration(i)*time.Second)); err != ErrUnauthorized {
			t.Fatalf("try %d: %v", i, err)
		}
	}
	// the right token is not even looked at while the host is over the limit
	if _, err := tk.Authenticate("Bearer "+tokA, "9.9.9.9", now.Add(20*time.Second)); err != ErrTooMany {
		t.Errorf("%v", err)
	}
	// another host is not affected
	if _, err := tk.Authenticate("Bearer "+tokA, "8.8.8.8", now.Add(20*time.Second)); err != nil {
		t.Errorf("%v", err)
	}
	// the window passes
	if _, err := tk.Authenticate("Bearer "+tokA, "9.9.9.9", now.Add(failWindow+15*time.Second)); err != nil {
		t.Errorf("%v", err)
	}
}
