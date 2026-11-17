// Package game holds pure codecs for 1.12 gameplay packets: no state and no I/O.
package game

import (
	"errors"
	"fmt"
	"regexp"
	"strings"

	"github.com/TortoiseBots/turtle-agent/internal/packet"
)

// ChatType is ChatMsg (SharedDefines.h:1358-1455).
type ChatType uint8

const (
	ChatSay                ChatType = 0x00
	ChatParty              ChatType = 0x01
	ChatRaid               ChatType = 0x02
	ChatGuild              ChatType = 0x03
	ChatOfficer            ChatType = 0x04
	ChatYell               ChatType = 0x05
	ChatWhisper            ChatType = 0x06
	ChatWhisperInform      ChatType = 0x07
	ChatEmote              ChatType = 0x08
	ChatTextEmote          ChatType = 0x09
	ChatSystem             ChatType = 0x0A
	ChatMonsterSay         ChatType = 0x0B
	ChatMonsterYell        ChatType = 0x0C
	ChatMonsterEmote       ChatType = 0x0D
	ChatChannel            ChatType = 0x0E
	ChatMonsterWhisper     ChatType = 0x1A
	ChatRaidLeader         ChatType = 0x57
	ChatRaidWarning        ChatType = 0x58
	ChatRaidBossWhisper    ChatType = 0x59
	ChatRaidBossEmote      ChatType = 0x5A
	ChatBattleground       ChatType = 0x5C
	ChatBattlegroundLeader ChatType = 0x5D
	ChatHardcore           ChatType = 0x5E // Turtle (SharedDefines.h:1455)
)

// LangUniversal (SharedDefines.h:206) needs no language skill (ObjectMgr.cpp:93).
const LangUniversal uint32 = 0

var chatNames = map[ChatType]string{
	ChatSay: "say", ChatParty: "party", ChatRaid: "raid", ChatGuild: "guild", ChatOfficer: "officer",
	ChatYell: "yell", ChatWhisper: "whisper", ChatWhisperInform: "whisper_inform", ChatEmote: "emote",
	ChatTextEmote: "text_emote", ChatSystem: "system", ChatMonsterSay: "monster_say", ChatMonsterYell: "monster_yell",
	ChatMonsterEmote: "monster_emote", ChatChannel: "channel", ChatMonsterWhisper: "monster_whisper",
	ChatRaidLeader: "raid_leader", ChatRaidWarning: "raid_warning", ChatRaidBossWhisper: "raid_boss_whisper",
	ChatRaidBossEmote: "raid_boss_emote", ChatBattleground: "battleground", ChatBattlegroundLeader: "battleground_leader",
	ChatHardcore: "hardcore",
}

// sendable are the types HandleMessagechatOpcode reads as [target or channel,] message
// (ChatHandler.cpp:264-307 at 187af78).
var sendable = map[ChatType]bool{
	ChatSay: true, ChatParty: true, ChatRaid: true, ChatGuild: true, ChatOfficer: true, ChatYell: true,
	ChatWhisper: true, ChatEmote: true, ChatRaidLeader: true, ChatRaidWarning: true, ChatBattleground: true,
	ChatBattlegroundLeader: true, ChatHardcore: true, ChatChannel: true,
}

func (t ChatType) String() string {
	if n, ok := chatNames[t]; ok {
		return n
	}
	return fmt.Sprintf("chat_0x%02X", uint8(t))
}

func ChatTypeByName(name string) (ChatType, error) {
	name = strings.ToLower(name)
	for t, n := range chatNames {
		if n == name && sendable[t] {
			return t, nil
		}
	}
	return 0, fmt.Errorf("chat type %q cannot be sent; use say, party, raid, guild, officer, yell, whisper, emote, raid_leader, raid_warning, battleground, battleground_leader, hardcore or channel", name)
}

// BuildMessageChat lays out CMSG_MESSAGECHAT (ChatHandler.cpp:176-307 at 187af78); to is
// the whisper target or, for ChatChannel, the channel name.
func BuildMessageChat(t ChatType, to, text string) ([]byte, error) {
	if text == "" {
		return nil, errors.New("chat: empty message")
	}
	if len(text) > 255 {
		return nil, fmt.Errorf("chat: message is %d bytes; the client limit is 255", len(text))
	}
	if !sendable[t] {
		return nil, fmt.Errorf("chat: type %s cannot be sent", t)
	}
	w := packet.NewWriter().U32(uint32(t)).U32(LangUniversal)
	if t == ChatWhisper {
		if to == "" {
			return nil, errors.New("chat: a whisper needs a target name")
		}
		w.CString(to)
	}
	if t == ChatChannel {
		if to == "" {
			return nil, errors.New("chat: a channel message needs the channel name in to")
		}
		w.CString(to)
	}
	return w.CString(text).Bytes(), nil
}

type ChatMessage struct {
	Type       ChatType
	Lang       uint32
	Sender     packet.GUID
	SenderName string // only monster and boss messages carry a name
	Target     packet.GUID
	Channel    string
	Raw        string // as sent, colour codes included
	Text       string // StripColors(Raw)
	Tag        uint8  // CHAT_TAG_* (Chat.h:85-88)
}

// ParseMessageChat decodes ChatHandler::BuildChatPacket (Chat.cpp:2251-2314).
func ParseMessageChat(body []byte) (ChatMessage, error) {
	r := packet.NewReader(body)
	var m ChatMessage
	m.Type, m.Lang = ChatType(r.U8()), r.U32()
	switch m.Type {
	case ChatMonsterWhisper, ChatRaidBossWhisper, ChatRaidBossEmote, ChatMonsterEmote:
		r.U32()
		m.SenderName, m.Target = r.CString(), r.GUID()
	case ChatSay, ChatParty, ChatYell:
		m.Sender = r.GUID()
		r.GUID()
	case ChatMonsterSay, ChatMonsterYell:
		m.Sender = r.GUID()
		r.U32()
		m.SenderName, m.Target = r.CString(), r.GUID()
	case ChatChannel:
		m.Channel = r.CString()
		r.U32()
		m.Sender = r.GUID()
	default:
		m.Sender = r.GUID()
	}
	if r.Err() == nil && r.Remaining() > 0 {
		r.U32()
		m.Raw, m.Tag = r.CString(), r.U8()
	}
	if err := r.Err(); err != nil {
		return ChatMessage{}, fmt.Errorf("SMSG_MESSAGECHAT: %w", err)
	}
	m.Text = StripColors(m.Raw)
	return m, nil
}

var (
	colourCode = regexp.MustCompile(`\|c[0-9A-Fa-f]{8}|\|r`)
	linkCode   = regexp.MustCompile(`\|H[^|]*\|h(.*?)\|h`)
)

// StripColors removes |cAARRGGBB and |r codes and unwraps |H…|h[text]|h links.
func StripColors(s string) string {
	return colourCode.ReplaceAllString(linkCode.ReplaceAllString(s, "$1"), "")
}

// ParseCString reads a body that is one string (SMSG_NOTIFICATION, SMSG_CHAT_PLAYER_NOT_FOUND).
func ParseCString(body []byte) (string, error) {
	r := packet.NewReader(body)
	s := r.CString()
	return s, r.Err()
}
