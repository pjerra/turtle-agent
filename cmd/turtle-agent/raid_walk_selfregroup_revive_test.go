package main

import (
	"context"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/pjerra/turtle-agent/internal/objects"
	"github.com/pjerra/turtle-agent/internal/tbm"
)

// TestRaidWalkSelfRegroupReviveWaitsTenSecondsAfterRelease (CRASH-2026-11-03): TB releases the party at its
// "regrouping by itself" line, and no `.revive` may go out within selfRegroupReviveWait of that line. The
// released bots' graveyard repop fires after the release; a revive before it sends a living bot far away.
func TestRaidWalkSelfRegroupReviveWaitsTenSecondsAfterRelease(t *testing.T) {
	var releaseAt time.Time
	type revive struct {
		name string
		at   time.Time
	}
	var revives []revive
	_, party := selfRegroupWipeWith(t, nil, func(f *fixture) {
		// No hostile near the revive point: the walk reaches its revive straight after the release, as it did live
		// (the crash's release and revive were 170 ms apart). With a quiet-check wait the 10 s would pass anyway.
		f.agent.unitsFn = func(n int) []objects.Unit { return []objects.Unit{} }
		prevEvents := f.agent.eventsHook
		f.agent.eventsHook = func(served string) {
			if prevEvents != nil {
				prevEvents(served)
			}
			if strings.Contains(served, "regrouping by itself") && releaseAt.IsZero() {
				releaseAt = f.clock.now()
			}
		}
		prevGM := f.agent.onGM
		f.agent.onGM = func(line string) {
			if prevGM != nil {
				prevGM(line)
			}
			if name, ok := strings.CutPrefix(line, ".revive "); ok {
				revives = append(revives, revive{name: name, at: f.clock.now()})
			}
		}
	})
	if releaseAt.IsZero() {
		t.Fatal("the regroup line was never served: no release to time the revive from")
	}
	if len(revives) != len(party) {
		t.Fatalf("%d revives, want %d (every hire is still dead at the release)", len(revives), len(party))
	}
	// The requirement is the literal 10 s, not the constant the code uses: a code change to the constant must not move the test.
	const want = 10 * time.Second
	for _, r := range revives {
		if d := r.at.Sub(releaseAt); d < want {
			t.Fatalf(".revive %s %s after the release, want at least %s", r.name, d, want)
		}
	}
}

// TestRaidWalkSelfRegroupReviveSkipsTheLiving (CRASH-2026-11-03): the hires TB has already brought up are never
// `.revive`d, only the still-dead ones are, and one line counts them.
func TestRaidWalkSelfRegroupReviveSkipsTheLiving(t *testing.T) {
	const keep = 5
	f, party := selfRegroupWipeWith(t, func(p []string) []string { return p[:keep] }, nil)
	gm := f.agent.gmLines()
	for _, l := range gm {
		for _, n := range party[:keep] {
			if l == ".revive "+n {
				t.Fatalf("%s was revived though TB had already brought it up: %q", n, gm)
			}
		}
	}
	if got, want := countPrefix(gm, ".revive "), len(party)-keep; got != want {
		t.Fatalf("%d revives, want %d (the dead hires only)", got, want)
	}
	sum := f.summary()
	want := fmt.Sprintf("revive after the regroup: %d hire(s) still dead (revived), %d alive (TB brought them back: not revived), 0 not in the frame (not revived)", len(party)-keep, keep)
	if !hasNote(sum, want) {
		t.Fatalf("notes %q, want the count line %q", sum.Notes, want)
	}
}

// TestStillDeadSplitsTheHiresByTheFrame: the split behind the revive filter keeps hire order and counts each hire once.
func TestStillDeadSplitsTheHiresByTheFrame(t *testing.T) {
	hires := []string{"Tank", "Healer", "Mage", "Rogue"}
	party := []tbm.PartyRec{
		{Name: "Tank", Dead: true},
		{Name: "Healer"},
		{Name: "Mage", State: "d"},
	}
	dead, alive, unread := stillDead(hires, party)
	if want := []string{"Tank", "Mage"}; !reflect.DeepEqual(dead, want) {
		t.Fatalf("dead %q, want %q", dead, want)
	}
	if alive != 1 || unread != 1 {
		t.Fatalf("alive %d unread %d, want 1 and 1 (Healer is up, Rogue is not in the frame)", alive, unread)
	}
}

// TestWaitPastReleaseSleepsToTheTenthSecond: the wait is measured from the regroup line the walk read, however long the
// checks between the line and the revive took. It is the test that pins the 10 s itself (the integration test above
// passes on the old race too, because the fixture's own pre-revive path already takes minutes).
func TestWaitPastReleaseSleepsToTheTenthSecond(t *testing.T) {
	base := time.Date(2026, 11, 3, 15, 57, 4, 0, time.UTC)
	for _, c := range []struct {
		name      string
		regroupAt time.Time
		now       time.Time
		want      time.Duration
	}{
		{"just released: the full 10 s", base, base, 10 * time.Second},
		{"4 s after the release: 6 s more", base, base.Add(4 * time.Second), 6 * time.Second},
		{"past the 10 s already: no wait", base, base.Add(12 * time.Second), 0},
		{"no release time read: the full 10 s", time.Time{}, base, 10 * time.Second},
	} {
		t.Run(c.name, func(t *testing.T) {
			now := c.now
			var slept time.Duration
			w := &walk{
				env: walkEnv{
					now: func() time.Time { return now },
					sleep: func(_ context.Context, d time.Duration) error {
						slept += d
						now = now.Add(d)
						return nil
					},
				},
				regroupAt: c.regroupAt,
			}
			if err := w.waitPastRelease(context.Background(), time.Time{}); err != nil {
				t.Fatalf("waitPastRelease: %v", err)
			}
			if slept != c.want {
				t.Fatalf("slept %s, want %s", slept, c.want)
			}
		})
	}
}
