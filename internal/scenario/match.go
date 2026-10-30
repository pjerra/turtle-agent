package scenario

import (
	"regexp"
	"strings"

	"github.com/pjerra/turtle-agent/internal/events"
)

// compileRegexp compiles an already-expanded pattern.
func compileRegexp(pat string) (*regexp.Regexp, error) { return regexp.Compile(pat) }

// sortStrings sorts step description keys without importing sort everywhere.
func sortStrings(s []string) {
	for i := 1; i < len(s); i++ {
		for j := i; j > 0 && s[j] < s[j-1]; j-- {
			s[j], s[j-1] = s[j-1], s[j]
		}
	}
}

// eventLine is the line a matcher reads: system events come raw (the
// stripped text loses "|r" out of fields such as "|removing", and G1's
// TBM:RUN|zulfarrak|running lost a field to plain text); every other kind
// reads text. Same rule as tbm.Line.
func eventLine(e events.Event) string {
	if e.Kind == events.KindSystem {
		if raw, ok := e.Data["raw"].(string); ok {
			return raw
		}
	}
	return e.Text
}

// matcher is a compiled Matcher: the match regexp plus the field filters.
type matcher struct {
	kind, prefix, channel, source string
	re                            *regexp.Regexp
}

// compile expands vars in the match (regex-quoted, so a name with a dot
// cannot match anything) and compiles it.
func (m Matcher) compile(vars map[string]string) (*matcher, error) {
	pat := m.Match
	if pat != "" {
		x, err := expand(pat, vars, true)
		if err != nil {
			return nil, err
		}
		pat = x
	}
	var re *regexp.Regexp
	if pat != "" {
		var err error
		re, err = regexp.Compile(pat)
		if err != nil {
			return nil, err
		}
	}
	return &matcher{kind: m.Kind, prefix: m.Prefix, channel: m.Channel, source: m.Source, re: re}, nil
}

func (m *matcher) matches(e events.Event) bool {
	if m.kind != "" && e.Kind != m.kind {
		return false
	}
	if m.kind == "addon" || m.prefix != "" {
		if e.Kind != events.KindAddon {
			return false
		}
		if m.prefix != "" {
			p, _ := e.Data["prefix"].(string)
			if p != m.prefix {
				return false
			}
		}
	}
	if m.channel != "" {
		var typ string
		if e.Kind == events.KindAddon {
			typ, _ = e.Data["type"].(string)
		} else {
			if d := e.Data; d != nil {
				typ, _ = d["type"].(string)
			}
		}
		if typ != m.channel {
			return false
		}
	}
	if m.source != "" && !strings.EqualFold(e.Source, m.source) {
		return false
	}
	if m.re != nil && !m.re.MatchString(eventLine(e)) {
		return false
	}
	return true
}
