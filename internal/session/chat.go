package session

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/pjerra/turtle-agent/internal/events"
	"github.com/pjerra/turtle-agent/internal/game"
	"github.com/pjerra/turtle-agent/internal/opcode"
	"github.com/pjerra/turtle-agent/internal/packet"
)

func (s *Session) registerChat() {
	s.handlers[opcode.SMSGMessagechat] = s.onMessageChat
	s.handlers[opcode.SMSGNotification] = s.onNotification
	s.handlers[opcode.SMSGChatPlayerNotFound] = s.onPlayerNotFound
	s.handlers[opcode.SMSGNameQueryResponse] = s.onNameQueryResponse
	s.handlers[opcode.SMSGCreatureQueryResponse] = s.onCreatureQueryResponse
	s.handlers[opcode.SMSGGameobjectQueryResponse] = s.onGameObjectQueryResponse
}

// chatKind maps a chat type to its event kind. For whisper_inform the server puts the
// recipient's GUID in the sender field (MasterPlayerChat.cpp:58), so such an event's
// Source is the recipient, as the client shows it and headless.py expects.
func chatKind(t game.ChatType) string {
	switch t {
	case game.ChatWhisper, game.ChatWhisperInform, game.ChatMonsterWhisper, game.ChatRaidBossWhisper:
		return events.KindWhisper
	case game.ChatSystem:
		return events.KindSystem
	}
	return events.KindChat
}

func (s *Session) onMessageChat(body []byte) error {
	m, err := game.ParseMessageChat(body)
	if err != nil {
		return err
	}
	data := map[string]any{"type": m.Type.String()}
	if m.Channel != "" {
		data["channel"] = m.Channel
	}
	if m.Tag != 0 {
		data["tag"] = m.Tag
	}
	if m.Lang == game.LangAddon {
		// The client raises CHAT_MSG_ADDON, not chat, for these. The text stays raw: addon
		// protocols such as TBM separate fields with "|".
		prefix, text, _ := game.SplitAddon(m.Raw)
		data["prefix"] = prefix
		s.emit(events.Event{Kind: events.KindAddon, SourceGUID: m.Sender, Text: text, Data: data})
		return nil
	}
	if m.Raw != m.Text {
		// StripColors also eats "|r" and "|c…" out of "|"-separated protocol lines such as
		// "TBM:ROSTER|…|removing|…"; the addon reads the raw line.
		data["raw"] = m.Raw
	}
	s.emit(events.Event{Kind: chatKind(m.Type), Source: m.SenderName, SourceGUID: m.Sender,
		TargetGUID: m.Target, Text: m.Text, Data: data})
	return nil
}

func (s *Session) onNotification(body []byte) error {
	text, err := game.ParseCString(body)
	if err != nil {
		return err
	}
	s.emit(events.Event{Kind: events.KindSystem, Text: game.StripColors(text), Data: map[string]any{"type": "notification"}})
	return nil
}

func (s *Session) onPlayerNotFound(body []byte) error {
	name, err := game.ParseCString(body)
	if err != nil {
		return err
	}
	s.emit(events.Event{Kind: events.KindSystem, Text: fmt.Sprintf("No player named '%s' is currently playing.", name),
		Data: map[string]any{"type": "player_not_found", "name": name}})
	return nil
}

// Chat sends one chat line; typ is a sendable chat type name ("say", "party", "whisper", …).
func (s *Session) Chat(ctx context.Context, typ, text, to string) error {
	t, err := game.ChatTypeByName(typ)
	if err != nil {
		return err
	}
	body, err := game.BuildMessageChat(t, to, text)
	if err != nil {
		return err
	}
	return s.send(ctx, opcode.CMSGMessagechat, body)
}

// AddonSend sends prefix and text as an addon message on channel (party, raid, guild,
// officer, raid_leader, raid_warning, battleground, battleground_leader, or channel with
// to naming the chat channel).
func (s *Session) AddonSend(ctx context.Context, channel, prefix, text, to string) error {
	t, err := game.AddonChannelByName(channel)
	if err != nil {
		return err
	}
	body, err := game.BuildAddonMessage(t, prefix, text, to)
	if err != nil {
		return err
	}
	return s.send(ctx, opcode.CMSGMessagechat, body)
}

// GM sends a GM or .bot command as say chat, adding the leading "." when it is missing.
// The server runs "."-prefixed say as a command instead of broadcasting it
// (ChatHandler.cpp:83-96), but says aloud a lone "." and anything starting with ".."
// (Chat.cpp:1769-1779), so GM refuses those. An account below GM level without
// PlayerCommands also says it aloud (Chat.cpp:1773-1774); that is server configuration.
func (s *Session) GM(ctx context.Context, text string) error {
	text, err := gmLine(text)
	if err != nil {
		return err
	}
	return s.Chat(ctx, "say", text, "")
}

// gmLine checks a GM command and adds the leading ".".
func gmLine(text string) (string, error) {
	text = strings.TrimSpace(text)
	if !strings.HasPrefix(text, ".") {
		text = "." + text
	}
	if strings.TrimSpace(text[1:]) == "" {
		return "", errors.New("gm: empty command")
	}
	if strings.HasPrefix(text, "..") {
		return "", fmt.Errorf("gm: %q starts with \"..\", which the server says aloud instead of running", text)
	}
	return text, nil
}

// GMOn selects the unit g and sends the GM command text as ONE operation of the session's owner goroutine: no other
// request (a target call, the addon host's click) can change the selection between the two packets. Use it for a
// command that acts on the selected unit (".aura <id>", ".revive"), which the core runs on whatever is selected
// when it arrives (Chat.cpp:2329-2340).
func (s *Session) GMOn(ctx context.Context, g packet.GUID, text string) error {
	if g == 0 {
		return errors.New("gm: no unit to select")
	}
	text, err := gmLine(text)
	if err != nil {
		return err
	}
	say, err := game.ChatTypeByName("say")
	if err != nil {
		return err
	}
	body, err := game.BuildMessageChat(say, "", text)
	if err != nil {
		return err
	}
	_, err = s.do(ctx, func() (any, error) {
		if err := s.inWorld(); err != nil {
			return nil, err
		}
		if err := s.conn.WritePacket(opcode.CMSGSetSelection, game.BuildSetSelection(g)); err != nil {
			return nil, err
		}
		s.mu.Lock()
		s.target = g
		s.mu.Unlock()
		return nil, s.conn.WritePacket(opcode.CMSGMessagechat, body)
	})
	return err
}

// ClearTarget clears the selection, as a client does when the player drops the target (CMSG_SET_SELECTION with a zero GUID). A command that
// acts on the selected unit when there is one and on the surroundings when there is none (".respawn", Commands.cpp HandleRespawnCommand)
// needs it first, and the caller waits for ServerSelection to read zero before the line is sent: the core handles the selection on the map
// thread and a chat line on the world thread.
func (s *Session) ClearTarget(ctx context.Context) error {
	_, err := s.do(ctx, func() (any, error) {
		if err := s.inWorld(); err != nil {
			return nil, err
		}
		if err := s.conn.WritePacket(opcode.CMSGSetSelection, game.BuildSetSelection(0)); err != nil {
			return nil, err
		}
		s.mu.Lock()
		s.target = 0
		s.mu.Unlock()
		return nil, nil
	})
	return err
}
