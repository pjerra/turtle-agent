package testfixture

import (
	"sync"
	"testing"
	"time"

	"github.com/pjerra/turtle-agent/internal/capture"
)

// hangLimit is how long one parse of one input may take. The parsers are linear in the
// input, so a run that outlives this is a hang (or an allocation that never ends).
const hangLimit = 5 * time.Second

// fixtureCaptures are the committed captures that seed the fuzz corpora.
var fixtureCaptures = []string{"m1-login.twcap", "m2-chat.twcap", "m3-party.twcap", "m4-world.twcap", "m5-attack.twcap", "m6-fight.twcap"}

var (
	loadOnce sync.Once
	loaded   []capture.Record
)

// maxSeedsPerOp bounds how many distinct bodies of one opcode seed a corpus.
const maxSeedsPerOp = 12

// Bodies returns up to maxSeedsPerOp distinct server bodies of each opcode in ops from the
// committed captures: the shortest, the longest and an even spread between them.
func Bodies(tb testing.TB, ops ...uint16) [][]byte {
	tb.Helper()
	loadOnce.Do(func() {
		for _, name := range fixtureCaptures {
			loaded = append(loaded, Load(tb, name)...)
		}
	})
	var out [][]byte
	for _, op := range ops {
		seen := map[string]bool{}
		var all [][]byte
		for _, b := range capture.Bodies(loaded, op) {
			if !seen[string(b)] {
				seen[string(b)] = true
				all = append(all, b)
			}
		}
		if len(all) <= maxSeedsPerOp {
			out = append(out, all...)
			continue
		}
		for i := 0; i < maxSeedsPerOp; i++ {
			out = append(out, all[i*(len(all)-1)/(maxSeedsPerOp-1)])
		}
	}
	return out
}

// FuzzBody registers a fuzz target that feeds parse arbitrary bytes. parse may return an
// error but must not panic and must return within hangLimit. The corpus is seeds plus
// whatever lives under testdata/fuzz/<target>; `go test` runs only those.
func FuzzBody(f *testing.F, seeds [][]byte, parse func(body []byte)) {
	f.Helper()
	for _, s := range seeds {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, body []byte) {
		done := make(chan any, 1)
		go func() {
			defer func() { done <- recover() }()
			parse(body)
		}()
		select {
		case p := <-done:
			if p != nil {
				t.Fatalf("panic on % X: %v", body, p)
			}
		case <-time.After(hangLimit):
			t.Fatalf("no result after %s on % X", hangLimit, body)
		}
	})
}
