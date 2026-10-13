package dungeon

import (
	"context"
	"testing"

	"github.com/pjerra/turtle-agent/internal/events"
)

// The end event goes out before the report says the run ended, whatever way it ended: a reader that sees "done", "failed" or
// "stopped" finds the event in the log, and until then the report reads "running" (not ended). The event carries the final state.
func TestRunEndEventComesBeforeTheEndState(t *testing.T) {
	for _, tc := range []struct {
		name  string
		setup func(g *fakeGame, cancel context.CancelFunc)
		state string
		kind  string
	}{
		{"done", func(g *fakeGame, cancel context.CancelFunc) {}, "done", events.KindDungeonDone},
		{"failed", func(g *fakeGame, cancel context.CancelFunc) { g.party = nil }, "failed", events.KindDungeonFailed},
		{"stopped", func(g *fakeGame, cancel context.CancelFunc) {
			g.onChat = func(g *fakeGame, text string) {
				if text == ".bot action pullback" {
					cancel()
				}
			}
		}, "stopped", events.KindDungeonFailed},
	} {
		t.Run(tc.name, func(t *testing.T) {
			g, p := runFixture(t)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			tc.setup(g, cancel)
			d := NewDriver(p, g, Config{}, g.clock())
			seen := 0
			g.onEmit = func(kind string, data map[string]any) {
				if kind != events.KindDungeonDone && kind != events.KindDungeonFailed {
					return
				}
				seen++
				rep, ended := d.Status()
				if ended || rep.State != "running" || rep.Ended != nil {
					t.Errorf("%s is emitted after the report ended: %s ended %v", kind, rep.State, ended)
				}
				if kind != tc.kind || data["state"] != tc.state {
					t.Errorf("event %s with state %v, want %s with %s", kind, data["state"], tc.kind, tc.state)
				}
			}
			_ = d.Run(ctx, 0)
			rep, ended := d.Status()
			if seen != 1 || !ended || rep.State != tc.state || rep.Ended == nil {
				t.Fatalf("events %d ended %v %+v", seen, ended, rep)
			}
			if got := g.events[len(g.events)-1].data["seconds"]; got != rep.Seconds {
				t.Errorf("the event says %v s, the report %v s", got, rep.Seconds)
			}
		})
	}
}
