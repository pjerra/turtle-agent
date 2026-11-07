package session

import (
	"context"
	"errors"
	"fmt"
	"math"
	"time"

	"github.com/pjerra/turtle-agent/internal/events"
	"github.com/pjerra/turtle-agent/internal/game"
	"github.com/pjerra/turtle-agent/internal/opcode"
	"github.com/pjerra/turtle-agent/internal/packet"
)

// registerSocial installs the chat channel and gossip handlers.
func (s *Session) registerSocial() {
	s.handlers[opcode.SMSGChannelNotify] = s.onChannelNotify
	s.handlers[opcode.SMSGGossipMessage] = s.onGossipMessage
	s.handlers[opcode.SMSGGossipComplete] = s.onGossipComplete
	s.handlers[opcode.SMSGActivatetaxireply] = s.onActivateTaxiReply
}

// JoinChannel joins a chat channel (CMSG_JOIN_CHANNEL); the server answers with
// SMSG_CHANNEL_NOTIFY, logged as channel_notify events.
func (s *Session) JoinChannel(ctx context.Context, name, password string) error {
	body, err := game.BuildJoinChannel(name, password)
	if err != nil {
		return err
	}
	return s.send(ctx, opcode.CMSGJoinChannel, body)
}

// LeaveChannel leaves a chat channel (CMSG_LEAVE_CHANNEL).
func (s *Session) LeaveChannel(ctx context.Context, name string) error {
	body, err := game.BuildLeaveChannel(name)
	if err != nil {
		return err
	}
	return s.send(ctx, opcode.CMSGLeaveChannel, body)
}

// onChannelNotify logs a channel notice. The player it is about is the source; a kick, ban
// or unban has the target as target and whoever did it as source.
func (s *Session) onChannelNotify(body []byte) error {
	n, err := game.ParseChannelNotify(body)
	if err != nil {
		return err
	}
	e := events.Event{Kind: events.KindChannelNotify, SourceGUID: n.Player, Text: n.Text(),
		Data: map[string]any{"notice": n.Notice.String(), "channel": n.Channel}}
	if n.Source != 0 {
		e.SourceGUID, e.TargetGUID = n.Source, n.Player
	}
	if n.Name != "" {
		e.Data["name"] = n.Name
	}
	switch n.Notice {
	case game.ChannelYouJoinedNotice:
		e.Data["flags"] = n.Flags
	case game.ChannelModeChangeNotice:
		e.Data["old_flags"], e.Data["new_flags"] = n.OldFlags, n.NewFlags
	}
	s.emit(e)
	return nil
}

// GossipHello talks to an NPC (CMSG_GOSSIP_HELLO). The menu comes back as a gossip event;
// the server answers nothing when the NPC is out of reach (NPCHandler.cpp:375-380 at 187af78).
func (s *Session) GossipHello(ctx context.Context, npc packet.GUID) error {
	if npc == 0 {
		return errors.New("gossip: no NPC guid")
	}
	_, err := s.do(ctx, func() (any, error) {
		if err := s.inWorld(); err != nil {
			return nil, err
		}
		// The old menu is gone once we talk to someone else, answer or not.
		s.gossip = nil
		return nil, s.conn.WritePacket(opcode.CMSGGossipHello, game.BuildGossipHello(npc))
	})
	return err
}

// GossipSelect picks an option of the open menu (CMSG_GOSSIP_SELECT_OPTION). Like the
// client it only clicks what the last SMSG_GOSSIP_MESSAGE showed, and sends code only, and
// always, for a coded option, because the core reads the code by its own copy of the menu
// (NPCHandler.cpp:405-420 at 187af78).
func (s *Session) GossipSelect(ctx context.Context, npc packet.GUID, option uint32, code string) error {
	_, err := s.do(ctx, func() (any, error) {
		if err := s.inWorld(); err != nil {
			return nil, err
		}
		if s.gossip == nil {
			return nil, errors.New("gossip: no menu is open; call gossip_hello first")
		}
		if s.gossip.NPC != npc {
			return nil, fmt.Errorf("gossip: the open menu is from %s, not %s", s.gossip.NPC, npc)
		}
		o, ok := s.gossip.Option(option)
		if !ok {
			return nil, fmt.Errorf("gossip: the menu has no option %d (%d options)", option, len(s.gossip.Options))
		}
		if code != "" && !o.Coded {
			return nil, fmt.Errorf("gossip: option %d takes no code", option)
		}
		return nil, s.conn.WritePacket(opcode.CMSGGossipSelectOption, game.BuildGossipSelectOption(npc, option, code, o.Coded))
	})
	return err
}

func (s *Session) onGossipMessage(body []byte) error {
	m, err := game.ParseGossipMessage(body)
	if err != nil {
		return err
	}
	s.gossip = &m
	// Fresh maps for the event: Data is never shared with session state.
	opts := make([]map[string]any, len(m.Options))
	for i, o := range m.Options {
		opts[i] = map[string]any{"index": o.Index, "icon": o.Icon, "coded": o.Coded, "text": o.Text}
	}
	quests := make([]map[string]any, len(m.Quests))
	for i, q := range m.Quests {
		quests[i] = map[string]any{"id": q.ID, "icon": q.Icon, "level": q.Level, "title": q.Title}
	}
	s.emit(events.Event{Kind: events.KindGossip, SourceGUID: m.NPC,
		Data: map[string]any{"text_id": m.TextID, "options": opts, "quests": quests}})
	return nil
}

func (s *Session) onGossipComplete([]byte) error {
	var npc packet.GUID
	if s.gossip != nil {
		npc = s.gossip.NPC
	}
	s.gossip = nil
	s.emit(events.Event{Kind: events.KindGossipComplete, SourceGUID: npc})
	return nil
}

// taxiWait is how long a taxi request waits for the flight master's answer before the
// session takes another one: some refusals get no reply at all (the flight master out of
// reach, an unknown node, disabled movement), so the timeout is what frees those.
const taxiWait = 10 * time.Second

// maxTaxiNodes caps a taxi route: real routes are a handful of nodes, and 64 keep the
// body far below the packet limit.
const maxTaxiNodes = 64

// ActivateTaxi asks the flight master npc for a flight along nodes (source first): two nodes
// send CMSG_ACTIVATETAXI, more send CMSG_ACTIVATETAXIEXPRESS, as the client does. The
// server's answer comes back as a taxi_reply event. A reply names no request, so one call
// waits for its answer before the next goes out.
func (s *Session) ActivateTaxi(ctx context.Context, npc packet.GUID, nodes []uint32) error {
	if npc == 0 {
		return errors.New("taxi: no flight master guid")
	}
	if len(nodes) < 2 {
		return errors.New("taxi: need at least a source and a destination node")
	}
	if len(nodes) > maxTaxiNodes {
		return fmt.Errorf("taxi: too many nodes (max %d)", maxTaxiNodes)
	}
	var op uint16
	var body []byte
	if len(nodes) == 2 {
		op, body = opcode.CMSGActivatetaxi, game.BuildActivateTaxi(npc, nodes[0], nodes[1])
	} else {
		op, body = opcode.CMSGActivatetaxiexpress, game.BuildActivateTaxiExpress(npc, nodes)
	}
	_, err := s.do(ctx, func() (any, error) {
		if err := s.inWorld(); err != nil {
			return nil, err
		}
		now := s.now()
		if now.Before(s.taxiPendingUntil) {
			return nil, fmt.Errorf("taxi: previous request still unanswered (retry after %d s)",
				int(math.Ceil(s.taxiPendingUntil.Sub(now).Seconds())))
		}
		// The window starts only once the request is out: a failed write leaves it
		// untouched, so the next call is not refused for a flight never asked for.
		if err := s.conn.WritePacket(op, body); err != nil {
			return nil, err
		}
		s.taxiPendingUntil = now.Add(taxiWait)
		return nil, nil
	})
	return err
}

// onActivateTaxiReply logs the flight master's answer: 0 means the flight was accepted
// and shows up as movement instead; anything else names the refusal. The reply clears the
// pending request, so the session takes the next one.
func (s *Session) onActivateTaxiReply(body []byte) error {
	t, err := game.ParseActivateTaxiReply(body)
	if err != nil {
		return err
	}
	s.taxiPendingUntil = time.Time{}
	s.emit(events.Event{Kind: events.KindTaxiReply,
		Text: "taxi reply: " + game.TaxiReplyName(t.Result),
		Data: map[string]any{"result": t.Result, "reason": game.TaxiReplyName(t.Result)}})
	return nil
}
