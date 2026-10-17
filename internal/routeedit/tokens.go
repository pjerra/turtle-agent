package routeedit

import (
	"bufio"
	"crypto/subtle"
	"errors"
	"fmt"
	"os"
	"regexp"
	"strings"
	"sync"
	"time"
)

// A token file has one line per person: `label token`. The label is what a version records as `who`.
var (
	labelRe = regexp.MustCompile(`^[A-Za-z0-9._-]{1,32}$`)
	tokenRe = regexp.MustCompile(`^[A-Za-z0-9._~+/=-]{32,256}$`)
)

type tokenEntry struct{ label, token string }

// Tokens are the people who may edit.
type Tokens struct {
	list []tokenEntry

	mu    sync.Mutex
	fails map[string][]time.Time // remote host -> the times of its recent failures
}

// LoadTokens reads the file at path. It must be a regular file (not a link), owned by the user running the dashboard, and readable by nobody
// else (mode 0600 or tighter); a line that is not `label token`, a short token, a repeated label or token is an error, and so is a file with
// no token in it. Blank lines and lines starting with # are skipped.
func LoadTokens(path string) (*Tokens, error) {
	f, err := os.OpenFile(path, os.O_RDONLY|oNoFollow, 0)
	if err != nil {
		return nil, fmt.Errorf("token file: %w", err)
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return nil, err
	}
	if !st.Mode().IsRegular() {
		return nil, fmt.Errorf("token file %s is not a regular file", path)
	}
	if st.Mode().Perm()&0o077 != 0 {
		return nil, fmt.Errorf("token file %s is readable by others (mode %04o): chmod 600 it", path, st.Mode().Perm())
	}
	if !ownedByUser(st) {
		return nil, fmt.Errorf("token file %s belongs to another user", path)
	}
	t := &Tokens{fails: map[string][]time.Time{}}
	seenLabel, seenToken := map[string]bool{}, map[string]bool{}
	sc := bufio.NewScanner(f)
	for n := 1; sc.Scan(); n++ {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		parts := strings.Fields(line)
		if len(parts) != 2 || !labelRe.MatchString(parts[0]) {
			return nil, fmt.Errorf("token file line %d: want `label token` (label of letters, digits, . _ -)", n)
		}
		if !tokenRe.MatchString(parts[1]) {
			return nil, fmt.Errorf("token file line %d: the token must be 32 to 256 characters of letters, digits and . _ ~ + / = -", n)
		}
		if seenLabel[parts[0]] || seenToken[parts[1]] {
			return nil, fmt.Errorf("token file line %d: a label or token is repeated", n)
		}
		seenLabel[parts[0]], seenToken[parts[1]] = true, true
		t.list = append(t.list, tokenEntry{parts[0], parts[1]})
	}
	if err := sc.Err(); err != nil {
		return nil, err
	}
	if len(t.list) == 0 {
		return nil, errors.New("token file has no token")
	}
	return t, nil
}

// Has reports whether the file has a token with this label.
func (t *Tokens) Has(label string) bool {
	for _, e := range t.list {
		if e.label == label {
			return true
		}
	}
	return false
}

// Failure limit: a host that fails this many times in the window is answered 429 without its token being looked at.
const (
	maxFails   = 10
	failWindow = time.Minute
)

// ErrTooMany is returned for a host that failed too often.
var ErrTooMany = errors.New("too many failed attempts: wait a minute")

// ErrUnauthorized is returned for a missing or wrong token.
var ErrUnauthorized = errors.New("missing or wrong token")

// Authenticate checks an Authorization header (`Bearer <token>`) and returns the label of the token's owner. The token is compared with every
// token of the file in constant time, so the time taken says nothing of how close a guess was. host is the caller's address, for the limit.
func (t *Tokens) Authenticate(header, host string, now time.Time) (string, error) {
	t.mu.Lock()
	recent := t.fails[host][:0:0]
	for _, at := range t.fails[host] {
		if now.Sub(at) < failWindow {
			recent = append(recent, at)
		}
	}
	t.fails[host] = recent
	blocked := len(recent) >= maxFails
	t.mu.Unlock()
	if blocked {
		return "", ErrTooMany
	}
	label, ok := "", false
	if tok, found := strings.CutPrefix(header, "Bearer "); found {
		for _, e := range t.list {
			if subtle.ConstantTimeCompare([]byte(tok), []byte(e.token)) == 1 {
				label, ok = e.label, true // no early exit: every token is compared
			}
		}
	}
	if !ok {
		t.mu.Lock()
		if len(t.fails) > 1024 {
			t.fails = map[string][]time.Time{}
		}
		t.fails[host] = append(t.fails[host], now)
		t.mu.Unlock()
		return "", ErrUnauthorized
	}
	return label, nil
}
