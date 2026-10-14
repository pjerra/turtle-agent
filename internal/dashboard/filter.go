// Package dashboard is the read-only live dashboard of the running agents (README "Live dashboard"): it polls each
// agent's RPC for status, never sends a command, and serves one page that shows every commander's run and map.
package dashboard

import (
	"bytes"
	"encoding/json"
	"regexp"
	"strings"
)

// secretKey matches the names of fields the dashboard never serialises: accounts, passwords, tokens and the like. The
// cards are built from an allow-list of fields, so this is the second lock: everything that leaves the server (cards,
// the raw debug panel) is scrubbed with it once more.
var secretKey = regexp.MustCompile(`(?i)pass|token|account|secret|credential|auth|login|cookie|session_?key|srp`)

// SecretKey says whether a JSON field name is one the dashboard drops.
func SecretKey(name string) bool { return secretKey.MatchString(name) }

// minRedact is the shortest secret value that is searched for in strings: a shorter one would blank ordinary text.
const minRedact = 8

const redacted = "[redacted]"

// Scrub returns raw as JSON without any object member whose name looks like a secret (at any depth) and with every
// string that contains one of secrets replaced. Invalid JSON is an error: nothing is passed through unchecked.
func Scrub(raw []byte, secrets ...string) ([]byte, error) {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var v any
	if err := dec.Decode(&v); err != nil {
		return nil, err
	}
	return json.Marshal(scrub(v, secrets))
}

func scrub(v any, secrets []string) any {
	switch t := v.(type) {
	case map[string]any:
		out := make(map[string]any, len(t))
		for k, x := range t {
			if SecretKey(k) {
				continue
			}
			out[k] = scrub(x, secrets)
		}
		return out
	case []any:
		out := make([]any, len(t))
		for i, x := range t {
			out[i] = scrub(x, secrets)
		}
		return out
	case string:
		for _, s := range secrets {
			if len(s) >= minRedact && strings.Contains(t, s) {
				return redacted
			}
		}
	}
	return v
}
