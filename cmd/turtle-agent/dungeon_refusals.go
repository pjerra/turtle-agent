package main

import (
	"regexp"
	"strings"
	"sync"

	"github.com/pjerra/turtle-agent/internal/dungeon"
	"github.com/pjerra/turtle-agent/internal/events"
	"github.com/pjerra/turtle-agent/internal/objects"
	"github.com/pjerra/turtle-agent/internal/tbm"
)

// refusalCursor reads the refusals of TortoiseBots out of the event log for a dungeon run: the TBM:ACTION_ERR replies
// to the run's "/p .bot action ..." orders, and the whisper a bot sends when it will not attack ("<mob> is evading",
// "<mob> is dead", "<mob> is too far away"; BotCommands/AttackAction in the module, not yet seen live in a capture).
type refusalCursor struct {
	mu  sync.Mutex
	seq uint64
}

// mark starts reading after the newest event now.
func (c *refusalCursor) mark(l *events.Log) {
	c.mu.Lock()
	c.seq = l.Last()
	c.mu.Unlock()
}

// next returns the refusals logged since the last call.
func (c *refusalCursor) next(l *events.Log, fromParty func(name string) bool) []dungeon.Refusal {
	c.mu.Lock()
	defer c.mu.Unlock()
	var out []dungeon.Refusal
	for {
		p := l.Read(events.Query{Since: c.seq, Kinds: []string{events.KindSystem, events.KindAddon, events.KindWhisper}, Limit: 500})
		out = append(out, refusalsIn(p.Events, fromParty)...)
		c.seq = p.Next
		if !p.More {
			return out
		}
	}
}

var whisperRefusal = regexp.MustCompile(`^(.{1,60}) is (evading|dead|too far away)\.?$`)

// refusalsIn picks the refusals out of events. A whisper counts only from a member of the party (fromParty), and names
// no intent: the driver matches it to the order it gave last.
func refusalsIn(evs []events.Event, fromParty func(name string) bool) []dungeon.Refusal {
	var out []dungeon.Refusal
	for _, e := range evs {
		line, ok := tbm.Line(e)
		if !ok && e.Kind == events.KindAddon && e.Data["prefix"] == tbm.Prefix {
			line, ok = "TBM:"+e.Text, true // the module's addon channel carries the line without its "TBM:"
		}
		if ok {
			if r, ok := tbm.ParseErr(line); ok {
				out = append(out, dungeon.Refusal{Intent: r.Intent, Code: r.Code, Message: r.Message})
			}
			continue
		}
		if e.Kind != events.KindWhisper || fromParty == nil || !fromParty(e.Source) {
			continue
		}
		if m := whisperRefusal.FindStringSubmatch(strings.TrimSpace(e.Text)); m != nil {
			code := map[string]string{"evading": "evading", "dead": "dead-target", "too far away": "too-far"}[m[2]]
			out = append(out, dungeon.Refusal{Code: code, Message: strings.TrimSpace(e.Text)})
		}
	}
	return out
}

// Refusals implements dungeon.RefusalSource.
func (g *sessionGame) Refusals() []dungeon.Refusal {
	s := g.cur()
	return g.refusals.next(s.Log(), func(name string) bool {
		for _, m := range s.Party().Members {
			if strings.EqualFold(m.Name, name) {
				return true
			}
		}
		return false
	})
}

// SelfAuras implements dungeon.SelfAuraSource: the leader's own auras from its object in the store.
func (g *sessionGame) SelfAuras() ([]objects.Aura, bool) {
	s := g.cur()
	u, err := s.Unit(s.Status().GUID, "")
	if err != nil {
		return nil, false
	}
	return u.Auras, true
}

// Items implements dungeon.ItemSource: the backpack and the bags of the leader.
func (g *sessionGame) Items() []objects.Item { return g.cur().Items() }
