package session

import (
	"bytes"
	"strings"

	"github.com/TortoiseBots/turtle-agent/internal/events"
)

// redactedMark replaces a secret in event text and names.
const redactedMark = "<redacted>"

// redactor scrubs secrets the server echoes back (the account name in a .pinfo reply,
// ruling P10) out of events and diagnostics, ASCII case-insensitive. Account names match
// on whole words only: no letter or digit may adjoin them, so a character named after
// its account (account HARNESS, character Harnessb) keeps its name. Passwords, long and
// random, match anywhere.
type redactor struct{ needles []needle }

type needle struct {
	b        []byte // lowered
	anywhere bool   // match as a plain substring, not only as a whole word
}

func isWordByte(c byte) bool {
	return 'a' <= c && c <= 'z' || 'A' <= c && c <= 'Z' || '0' <= c && c <= '9'
}

func isHex(c byte) bool { return '0' <= c && c <= '9' || 'a' <= c && c <= 'f' }

// afterColor reports whether l[:i] ends in a WoW colour escape, |c plus 8 hex digits
// (l is lowered): the text after it starts a new word.
func afterColor(l []byte, i int) bool {
	if i < 10 || l[i-10] != '|' || l[i-9] != 'c' {
		return false
	}
	for _, c := range l[i-8 : i] {
		if !isHex(c) {
			return false
		}
	}
	return true
}

// afterEscape reports whether l[:i] ends in a colour reset |r, a link text marker |h or
// a newline |n (l is lowered, so |R, |H and |N too): raw chat lines and addon text keep these.
func afterEscape(l []byte, i int) bool {
	return i >= 2 && l[i-2] == '|' && (l[i-1] == 'r' || l[i-1] == 'h' || l[i-1] == 'n')
}

// wholeWord reports whether l[i:i+n] has no letter or digit on either side; a colour
// escape, colour reset or link text marker right before it counts as a boundary.
func wholeWord(l []byte, i, n int) bool {
	left := i == 0 || !isWordByte(l[i-1]) || afterColor(l, i) || afterEscape(l, i)
	return left && (i+n == len(l) || !isWordByte(l[i+n]))
}

// index returns the first match of n in l at or after from, or -1.
func index(l []byte, n needle, from int) int {
	for from <= len(l)-len(n.b) {
		j := bytes.Index(l[from:], n.b)
		if j < 0 {
			return -1
		}
		if n.anywhere || wholeWord(l, from+j, len(n.b)) {
			return from + j
		}
		from += j + 1
	}
	return -1
}

// newRedactor matches words (account names) as whole words and anywhere (passwords) as
// plain substrings.
func newRedactor(words, anywhere []string) redactor {
	var r redactor
	add := func(s string, any bool) {
		if s == "" {
			return
		}
		n := asciiLower([]byte(s))
		for i, have := range r.needles {
			if bytes.Equal(have.b, n) {
				r.needles[i].anywhere = have.anywhere || any
				return
			}
		}
		r.needles = append(r.needles, needle{b: n, anywhere: any})
	}
	for _, s := range words {
		add(s, false)
	}
	for _, s := range anywhere {
		add(s, true)
	}
	return r
}

// asciiLower lowers A-Z only, so byte offsets match the input.
func asciiLower(b []byte) []byte {
	out := make([]byte, len(b))
	for i, c := range b {
		if 'A' <= c && c <= 'Z' {
			c += 'a' - 'A'
		}
		out[i] = c
	}
	return out
}

// contains reports whether b holds any secret, as any substring: it guards the hex dump,
// where a false alarm only costs the dump.
func (r redactor) contains(b []byte) bool {
	if len(r.needles) == 0 {
		return false
	}
	l := asciiLower(b)
	for _, n := range r.needles {
		if bytes.Contains(l, n.b) {
			return true
		}
	}
	return false
}

// scrub replaces every run of bytes covered by a secret with redactedMark;
// overlapping or adjacent matches merge into one mark.
func (r redactor) scrub(s string) string {
	if len(r.needles) == 0 || s == "" {
		return s
	}
	l := asciiLower([]byte(s))
	var hit []bool
	for _, n := range r.needles {
		for i := index(l, n, 0); i >= 0; i = index(l, n, i+1) {
			if hit == nil {
				hit = make([]bool, len(l))
			}
			for k := i; k < i+len(n.b); k++ {
				hit[k] = true
			}
		}
	}
	if hit == nil {
		return s
	}
	var b strings.Builder
	for i := 0; i < len(s); {
		if !hit[i] {
			b.WriteByte(s[i])
			i++
			continue
		}
		b.WriteString(redactedMark)
		for i < len(s) && hit[i] {
			i++
		}
	}
	return b.String()
}

func (r redactor) scrubValue(v any) any {
	switch t := v.(type) {
	case string:
		return r.scrub(t)
	case []string:
		out := make([]string, len(t))
		for i, s := range t {
			out[i] = r.scrub(s)
		}
		return out
	case map[string]any:
		for k, x := range t {
			t[k] = r.scrubValue(x)
		}
		return t
	case []map[string]any: // gossip options and quests
		for _, m := range t {
			r.scrubValue(m)
		}
		return t
	case []any:
		for i, x := range t {
			t[i] = r.scrubValue(x)
		}
		return t
	}
	return v
}

// scrubEvent scrubs the text, the names and the string data of e.
func (r redactor) scrubEvent(e *events.Event) {
	if len(r.needles) == 0 {
		return
	}
	e.Text, e.Source, e.Target = r.scrub(e.Text), r.scrub(e.Source), r.scrub(e.Target)
	for k, v := range e.Data {
		e.Data[k] = r.scrubValue(v)
	}
}
