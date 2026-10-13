package session

import (
	"errors"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/pjerra/turtle-agent/internal/events"
	"github.com/pjerra/turtle-agent/internal/packet"
	"github.com/pjerra/turtle-agent/internal/worldconn"
)

// A reader that sees the session ended finds the events that say so in the log: the session event, and the events that were waiting
// for a name before it. The state is set last (it used to be set first, with the log still behind it).
func TestSessionEndsWithItsEventsBeforeItsState(t *testing.T) {
	const waiting = 2000
	for _, tc := range []struct {
		name  string
		end   func(s *Session)
		state Status
	}{
		{"disconnect", func(s *Session) { _ = s.endLogout(errors.New("lost"), LogoutLost) }, StatusDisconnected},
		{"closed at logout", func(s *Session) { _ = s.closedAtLogout(errors.New("eof")) }, StatusLoggedOut},
		{"logout complete", func(s *Session) { _ = s.onLogoutComplete(nil) }, StatusLoggedOut},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := New(worldconn.NewOffline(), Options{Character: "x", NamesWait: time.Hour})
			s.setState(StatusInWorld)
			for i := range waiting { // events whose player names are unknown pile up behind each other
				s.emit(events.Event{Kind: events.KindChat, SourceGUID: packet.GUID(1000 + i)})
			}
			if got := s.Log().Last(); got != 0 {
				t.Fatalf("%d events were not held back for their names", got)
			}
			var bad string
			var wg sync.WaitGroup
			wg.Add(1)
			go func() {
				defer wg.Done()
				for {
					if s.State() == tc.state {
						if got := s.Log().Last(); got < waiting+1 {
							bad = "the state was visible with only " + strconv.FormatUint(got, 10) + " of " + strconv.Itoa(waiting+1) + " events in the log"
						}
						return
					}
				}
			}()
			tc.end(s)
			wg.Wait()
			if bad != "" {
				t.Fatal(bad)
			}
		})
	}
}
