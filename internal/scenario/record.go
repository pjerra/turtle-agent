package scenario

import (
	"encoding/json"
	"strings"

	"github.com/pjerra/turtle-agent/internal/events"
)

// Record is one transcript line, rendered to raw.txt and norm.txt and stored
// as one JSON object per line in transcript.jsonl.
type Record struct {
	T       string          `json:"t"`
	Step    int             `json:"step,omitempty"`
	Session string          `json:"session,omitempty"`
	Raw     string          `json:"raw"`
	Norm    string          `json:"norm"`
	Event   *events.Event   `json:"event,omitempty"`
	Status  string          `json:"status,omitempty"`
	Got     json.RawMessage `json:"got,omitempty"`
}

// CheckResult is one check outcome for result.json.
type CheckResult struct {
	Step   int             `json:"step"`
	Desc   string          `json:"desc"`
	Status string          `json:"status"`
	Got    json.RawMessage `json:"got,omitempty"`
	Reason string          `json:"reason,omitempty"`
}

// redactAll replaces every token occurrence with <token>. The token is never
// written to any transcript, file or line the runner stores or prints.
func redactAll(s string, tokens []string) string {
	for _, tok := range tokens {
		if tok == "" {
			continue
		}
		s = strings.ReplaceAll(s, tok, "<token>")
	}
	return s
}

// redactEvent scrubs the token from an event's text fields.
func redactEvent(e *events.Event, tokens []string) {
	if len(tokens) == 0 {
		return
	}
	e.Text = redactAll(e.Text, tokens)
	e.Data = redactValue(e.Data, tokens).(map[string]any)
}

func redactValue(v any, tokens []string) any {
	switch t := v.(type) {
	case string:
		return redactAll(t, tokens)
	case map[string]any:
		for k, e := range t {
			t[k] = redactValue(e, tokens)
		}
		return t
	case []any:
		for i, e := range t {
			t[i] = redactValue(e, tokens)
		}
		return t
	default:
		return v
	}
}
