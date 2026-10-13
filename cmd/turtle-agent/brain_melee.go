package main

import (
	"sync"
	"time"

	"github.com/pjerra/turtle-agent/internal/brain"
	"github.com/pjerra/turtle-agent/internal/events"
	"github.com/pjerra/turtle-agent/internal/packet"
)

// The melee half of the brain's Game (brain.Melee) over the agent's session: what the player's swings did, read off the session's
// event log. It reads the log from where it stood when the brain first asked, so a dodge from before the brain started is not news.
var (
	_ brain.Melee        = (*sessionBrainGame)(nil)
	_ brain.SwingWatcher = (*sessionBrainGame)(nil)
)

// meleeWatch follows the event log for the dodges, the avoided attacks and the auto attack of the player.
type meleeWatch struct {
	mu       sync.Mutex
	log      *events.Log // the log the cursor belongs to; a new session has a new log
	cursor   uint64
	dodged   map[packet.GUID]time.Time
	avoided  time.Time
	swinging packet.GUID
	errs     uint64 // the swings the server refused since the first look
	lastErr  string
}

// dodgeKeep is how long a dodge is remembered: Overpower can use it for five seconds.
const dodgeKeep = 30 * time.Second

// avoidKinds are the outcomes of an attack that its victim avoided.
func avoidedOutcome(s string) bool { return s == "dodge" || s == "parry" || s == "block" }

// sync reads the events that came in since the last call.
func (g *sessionBrainGame) syncMelee() {
	s := g.cur()
	l := s.Log()
	self := s.Status().GUID
	w := &g.melee
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.log != l {
		w.log, w.cursor, w.dodged, w.avoided, w.swinging = l, l.Last(), map[packet.GUID]time.Time{}, time.Time{}, 0
		w.errs, w.lastErr = 0, ""
		// The auto attack the player already had when the brain looked first (an attack order from the RPC, a fight pulled before the
		// brain started) is on: the server does not announce it again, so without this the brain would think it was idle and order it
		// again every tick.
		for since := uint64(0); ; {
			page := l.Read(events.Query{Since: since, Limit: events.MaxLimit, Kinds: []string{events.KindAttackStart, events.KindAttackStop}})
			for _, e := range page.Events {
				if e.SourceGUID != self {
					continue
				}
				if e.Kind == events.KindAttackStart {
					w.swinging = e.TargetGUID
				} else {
					w.swinging = 0
				}
			}
			since = page.Next
			if !page.More {
				break
			}
		}
		return
	}
	for {
		page := l.Read(events.Query{Since: w.cursor, Limit: events.MaxLimit,
			Kinds: []string{events.KindMelee, events.KindSpellMiss, events.KindAttackStart, events.KindAttackStop, events.KindAttackError}})
		w.cursor = page.Next
		for _, e := range page.Events {
			switch e.Kind {
			case events.KindMelee, events.KindSpellMiss:
				outcome, _ := e.Data["outcome"].(string)
				if e.Kind == events.KindSpellMiss {
					outcome, _ = e.Data["miss"].(string)
				}
				if e.SourceGUID == self && outcome == "dodge" {
					w.dodged[e.TargetGUID] = e.Time
				}
				if e.TargetGUID == self && avoidedOutcome(outcome) {
					w.avoided = e.Time
				}
			case events.KindAttackStart:
				if e.SourceGUID == self {
					w.swinging = e.TargetGUID
				}
			case events.KindAttackError:
				w.errs++
				w.lastErr, _ = e.Data["reason"].(string)
			case events.KindAttackStop:
				if e.SourceGUID == self {
					w.swinging = 0
				}
			}
		}
		if !page.More {
			break
		}
	}
	now := time.Now()
	for t, at := range w.dodged {
		if now.Sub(at) > dodgeKeep {
			delete(w.dodged, t)
		}
	}
}

func (g *sessionBrainGame) LastDodge(target packet.GUID) time.Time {
	g.syncMelee()
	g.melee.mu.Lock()
	defer g.melee.mu.Unlock()
	return g.melee.dodged[target]
}

func (g *sessionBrainGame) LastAvoided() time.Time {
	g.syncMelee()
	g.melee.mu.Lock()
	defer g.melee.mu.Unlock()
	return g.melee.avoided
}

func (g *sessionBrainGame) Swinging() packet.GUID {
	g.syncMelee()
	g.melee.mu.Lock()
	defer g.melee.mu.Unlock()
	return g.melee.swinging
}

// SwingErrors is brain.SwingWatcher: how many swings the server refused since the brain first looked, and the last reason.
func (g *sessionBrainGame) SwingErrors() (uint64, string) {
	g.syncMelee()
	g.melee.mu.Lock()
	defer g.melee.mu.Unlock()
	return g.melee.errs, g.melee.lastErr
}
