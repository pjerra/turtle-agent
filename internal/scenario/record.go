package scenario

import (
	"encoding/json"
	"regexp"
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
	e.Source = redactAll(e.Source, tokens)
	e.Target = redactAll(e.Target, tokens)
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

// redactJSON replaces every token occurrence in raw JSON bytes, recursing
// through strings via redactValue so nested fields are scrubbed too. The
// output keeps the literal <token> text (no \u003c escapes).
func redactJSON(raw json.RawMessage, tokens []string) json.RawMessage {
	if len(tokens) == 0 || len(raw) == 0 {
		return raw
	}
	var v any
	if err := json.Unmarshal(raw, &v); err != nil {
		return json.RawMessage(redactAll(string(raw), tokens))
	}
	var buf strings.Builder
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(redactValue(v, tokens)); err != nil {
		return raw
	}
	return json.RawMessage(strings.TrimSuffix(buf.String(), "\n"))
}

var (
	// B1: RFC3339 times, with optional fractions and zone.
	bNormTime = regexp.MustCompile(`\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}(\.\d+)?(Z|[+-]\d{2}:\d{2})`)
	// B2: clock times.
	bNormClock = regexp.MustCompile(`\b\d{2}:\d{2}:\d{2}\b`)
	// B3: hex guids.
	bNormHexGUID = regexp.MustCompile(`\b0x[0-9A-Fa-f]{16}\b`)
	// B4: long decimal guids (TBM lines).
	bNormDecGUID = regexp.MustCompile(`\b\d{17,20}\b`)
)

// builtinRules is the normaliser's fixed first layer (spec section 5).
var builtinRules = []NormRule{
	{Match: `\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}(\.\d+)?(Z|[+-]\d{2}:\d{2})`, Replace: "<TIME>"},
	{Match: `\b\d{2}:\d{2}:\d{2}\b`, Replace: "<CLOCK>"},
	{Match: `\b0x[0-9A-Fa-f]{16}\b`, Replace: "<GUID>"},
	{Match: `\b\d{17,20}\b`, Replace: "<GUID>"},
}

// normaliseOne applies the builtins then the file rules, in order, to one line.
func normaliseOne(line string, rules []NormRule) string {
	line = bNormTime.ReplaceAllString(line, "<TIME>")
	line = bNormClock.ReplaceAllString(line, "<CLOCK>")
	line = bNormHexGUID.ReplaceAllString(line, "<GUID>")
	line = bNormDecGUID.ReplaceAllString(line, "<GUID>")
	for _, r := range rules {
		if r.re == nil {
			continue
		}
		line = r.re.ReplaceAllString(line, r.Replace)
	}
	return line
}

// renderNorm renders every record's Norm through the normaliser, dropping
// records with empty Norm (structural: seq, times, templates already hide
// the values). scenario norm re-applies this to stored records.
func renderNorm(recs []Record, rules []NormRule) []string {
	out := []string{}
	for _, r := range recs {
		if r.Norm == "" {
			continue
		}
		out = append(out, normaliseOne(r.Norm, rules))
	}
	return out
}

// renderRaw renders every record's Raw, keeping blank lines (the summary's
// leading blanks stay).
func renderRaw(recs []Record) []string {
	out := make([]string, 0, len(recs))
	for _, r := range recs {
		out = append(out, r.Raw)
	}
	return out
}

// RenderNorm is the exported renderNorm for the CLI.
func RenderNorm(recs []Record, rules []NormRule) []string { return renderNorm(recs, rules) }

// RenderRaw is the exported renderRaw for the CLI.
func RenderRaw(recs []Record) []string { return renderRaw(recs) }

// NormaliseOne is the exported normaliseOne for the CLI.
func NormaliseOne(line string, rules []NormRule) string { return normaliseOne(line, rules) }
