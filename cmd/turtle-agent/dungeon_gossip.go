package main

import (
	"context"
	"time"

	"github.com/pjerra/turtle-agent/internal/dungeon"
	"github.com/pjerra/turtle-agent/internal/events"
	"github.com/pjerra/turtle-agent/internal/packet"
)

// The agent's own gossip (dungeon.GossipTalker): the same session calls as the gossip_hello and gossip_select methods, each with the wait for
// the server's answer in the event log.

func (g *sessionGame) GossipHello(ctx context.Context, npc packet.GUID, wait time.Duration) (dungeon.GossipAnswer, error) {
	s := g.cur()
	log := s.Log()
	since := log.Last()
	if err := s.GossipHello(ctx, npc); err != nil {
		return dungeon.GossipAnswer{}, err
	}
	return gossipAnswerOf(nextGossip(ctx, log, since, int(wait.Milliseconds()))), nil
}

func (g *sessionGame) GossipSelect(ctx context.Context, npc packet.GUID, option uint32, wait time.Duration) (dungeon.GossipAnswer, error) {
	s := g.cur()
	log := s.Log()
	since := log.Last()
	if err := s.GossipSelect(ctx, npc, option, ""); err != nil {
		return dungeon.GossipAnswer{}, err
	}
	return gossipAnswerOf(nextGossip(ctx, log, since, int(wait.Milliseconds()))), nil
}

// GossipHello and GossipSelect: the remote driver's agent talks with the same calls (the agent's own character is not in the group).
func (g remoteGame) GossipHello(ctx context.Context, npc packet.GUID, wait time.Duration) (dungeon.GossipAnswer, error) {
	return g.hands().GossipHello(ctx, npc, wait)
}

func (g remoteGame) GossipSelect(ctx context.Context, npc packet.GUID, option uint32, wait time.Duration) (dungeon.GossipAnswer, error) {
	return g.hands().GossipSelect(ctx, npc, option, wait)
}

// gossipAnswerOf reads the first answer event: the menu, the close, another panel, or (nil) nothing.
func gossipAnswerOf(ev *events.Event) dungeon.GossipAnswer {
	switch {
	case ev == nil:
		return dungeon.GossipAnswer{Kind: dungeon.GossipAnswerNone}
	case ev.Kind == events.KindGossipComplete:
		return dungeon.GossipAnswer{Kind: dungeon.GossipAnswerComplete}
	case ev.Kind != events.KindGossip:
		return dungeon.GossipAnswer{Kind: dungeon.GossipAnswerOther, Other: ev.Kind}
	}
	ans := dungeon.GossipAnswer{Kind: dungeon.GossipAnswerMenu}
	if id, ok := eventUint(ev.Data["text_id"]); ok {
		ans.TextID = uint32(id)
	}
	var opts []map[string]any
	switch l := ev.Data["options"].(type) {
	case []map[string]any:
		opts = l
	case []any: // after a JSON round trip
		for _, o := range l {
			if m, ok := o.(map[string]any); ok {
				opts = append(opts, m)
			}
		}
	}
	for _, o := range opts {
		idx, _ := eventUint(o["index"])
		text, _ := o["text"].(string)
		coded, _ := o["coded"].(bool)
		ans.Options = append(ans.Options, dungeon.GossipLine{Index: uint32(idx), Text: text, Coded: coded})
	}
	return ans
}

// eventUint reads a number of an event's data, which holds the session's own integer types (or float64 after a JSON round trip).
func eventUint(v any) (uint64, bool) {
	switch n := v.(type) {
	case uint32:
		return uint64(n), true
	case uint64:
		return n, true
	case uint8:
		return uint64(n), true
	case int:
		return uint64(n), n >= 0
	case float64:
		return uint64(n), n >= 0
	}
	return 0, false
}

var (
	_ dungeon.GossipTalker = (*sessionGame)(nil)
	_ dungeon.GossipTalker = remoteGame{}
)
