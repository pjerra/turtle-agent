package scenario

import (
	"strings"
	"testing"
	"time"

	"github.com/pjerra/turtle-agent/internal/events"
)

// TestNormaliseBuiltins pins the four built-in placeholders: RFC3339 times,
// clock times, hex guids and long decimal guids.
func TestNormaliseBuiltins(t *testing.T) {
	lines := []string{"at 2026-10-27T12:00:00Z and 00:20:37 guid 0xf130001611013ea0 and 17370386142314129924 done"}
	got := renderNorm([]Record{{T: "event", Raw: "x", Norm: strings.Join(lines, "\n")}}, nil)
	if len(got) != 1 {
		t.Fatalf("%v", got)
	}
	if strings.Contains(got[0], "2026-10-27") || strings.Contains(got[0], "00:20:37") ||
		strings.Contains(got[0], "0xf130001611013ea0") || strings.Contains(got[0], "17370386142314129924") {
		t.Fatalf("not normalised: %q", got[0])
	}
	for _, want := range []string{"<TIME>", "<CLOCK>", "<GUID>"} {
		if !strings.Contains(got[0], want) {
			t.Fatalf("missing %s: %q", want, got[0])
		}
	}
}

// TestNormaliseFileRulesInOrder pins that file rules apply after the builtins
// and in file order.
func TestNormaliseFileRulesInOrder(t *testing.T) {
	mk := func(match, replace string) NormRule {
		r := NormRule{Match: match, Replace: replace}
		if err := r.compile(); err != nil {
			t.Fatal(err)
		}
		return r
	}
	recs := []Record{{T: "event", Raw: "x", Norm: `"id":2 then SECOND`}}
	got := renderNorm(recs, []NormRule{mk("SECOND", "FIRST"), mk("FIRST", "1")})
	if len(got) != 1 || got[0] != `"id":<N> then 1` && got[0] != `"id":2 then 1` {
		// builtin rules do not touch ids; file rules run in order:
		// SECOND->FIRST, then FIRST->1.
		t.Fatalf("%q", got)
	}
	if got[0] != `"id":2 then 1` {
		t.Fatalf("%q", got)
	}
}

func TestNormIsStableAcrossIDsAndTimes(t *testing.T) {
	// Minimal harness for the before/after use case: two events differing in
	// seq, time and id, normalised by the LFT file's rules.
	rules := []NormRule{
		{Match: `"id":\d+`, Replace: `"id":<ID>`},
		{Match: `(S2C_UPDATE_GROUP;)\d+`, Replace: `${1}<ID>`},
	}
	for i := range rules {
		if err := rules[i].compile(); err != nil {
			t.Fatal(err)
		}
	}
	mk := func(seq uint64, tm time.Time, id string) Record {
		e := &events.Event{Kind: "addon", Text: "x"}
		raw := "    [A] " + strings.Repeat("s", int(seq)) + "\t" + tm.Format(time.RFC3339Nano) + "\taddon\t" + id
		norm := "    [A] addon\t" + id
		return Record{T: "event", Raw: raw, Norm: norm, Event: e}
	}
	a := renderNorm([]Record{
		mk(3, time.Date(2026, 10, 27, 12, 0, 0, 0, time.UTC), `S2C_UPDATE_GROUP;2;start;{"id":2}`),
	}, rules)
	b := renderNorm([]Record{
		mk(9, time.Date(2026, 10, 28, 1, 2, 3, 0, time.UTC), `S2C_UPDATE_GROUP;7;start;{"id":7}`),
	}, rules)
	if len(a) != 1 || len(b) != 1 || a[0] != b[0] {
		t.Fatalf("%q vs %q", a, b)
	}
	if !strings.Contains(a[0], "<ID>") {
		t.Fatalf("%q", a[0])
	}
}
