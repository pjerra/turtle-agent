// Chat, addon, channel and gossip packets written the way the core does. Line numbers
// cite the core at 187af78, the checkout deployed on the test server.
package gametest

import (
	"github.com/TortoiseBots/turtle-agent/internal/game"
	"github.com/TortoiseBots/turtle-agent/internal/packet"
)

// chatTagGM is CHAT_TAG_GM (Chat.h:90).
const chatTagGM = 3

// ChatPacket is ChatHandler::BuildChatPacket (Chat.cpp:2254-2314). name is written only for
// monster and boss types, target only where the type carries one, channel only for
// CHAT_MSG_CHANNEL (its player rank is written as 0). An empty final message ends the
// packet after the sender, as the core does.
func ChatPacket(t game.ChatType, lang uint32, sender packet.GUID, name string, target packet.GUID, channel, msg string, tag uint8) []byte {
	if tag == chatTagGM && lang != game.LangAddon { // Chat.cpp:2260-2263
		msg = "|c1049e6ff" + msg + "|r"
	}
	w := packet.NewWriter().U8(uint8(t)).U32(lang)
	switch t {
	case game.ChatMonsterWhisper, game.ChatRaidBossWhisper, game.ChatRaidBossEmote, game.ChatMonsterEmote:
		w.U32(uint32(len(name) + 1)).CString(name).GUID(target)
	case game.ChatSay, game.ChatParty, game.ChatYell:
		w.GUID(sender).GUID(sender)
	case game.ChatMonsterSay, game.ChatMonsterYell:
		w.GUID(sender).U32(uint32(len(name) + 1)).CString(name).GUID(target)
	case game.ChatChannel:
		w.CString(channel).U32(0).GUID(sender)
	default:
		w.GUID(sender)
	}
	if msg == "" { // Chat.cpp:2311-2312
		return w.Bytes()
	}
	return w.U32(uint32(len(msg) + 1)).CString(msg).U8(tag).Bytes()
}

// AddonMessage is Player::SendAddonMessage (Player.cpp:25603-25611): CHAT_MSG_GUILD in
// LANG_ADDON from the player himself, the way the TortoiseBots module answers its addon.
func AddonMessage(self packet.GUID, prefix, text string, tag uint8) []byte {
	return ChatPacket(game.ChatGuild, game.LangAddon, self, "", 0, "", prefix+"\t"+text, tag)
}

// PartyAddonMessage is a group member's addon message as CHAT_MSG_PARTY relays it to the
// group (ChatHandler.cpp:664-689).
func PartyAddonMessage(sender packet.GUID, prefix, text string) []byte {
	return ChatPacket(game.ChatParty, game.LangAddon, sender, "", 0, "", prefix+"\t"+text, 0)
}

// ChannelNotice is Channel::MakeNotifyPacket alone (Channel.cpp:883-888): the notices that
// carry nothing but the channel name (you left, wrong password, not member, muted, …).
func ChannelNotice(notice uint8, channel string) []byte {
	return packet.NewWriter().U8(notice).CString(channel).Bytes()
}

// ChannelYouJoined is Channel::MakeYouJoined (Channel.cpp:902-907).
func ChannelYouJoined(channel string, flags uint32) []byte {
	return packet.NewWriter().U8(0x02).CString(channel).U32(flags).U32(0).Bytes()
}

// ChannelPlayer is a notice about one player: joined, left, password or owner changed,
// announcements or moderation switched, already member, invite (Channel.cpp:890-900,
// :937-947, :979-1001, :1040-1050).
func ChannelPlayer(notice uint8, channel string, player packet.GUID) []byte {
	return packet.NewWriter().U8(notice).CString(channel).GUID(player).Bytes()
}

// ChannelName is a notice naming a player by name: player not found, channel owner, not
// banned, invited, invite banned (Channel.cpp:949-969, :1034-1038, :1072-1082).
func ChannelName(notice uint8, channel, name string) []byte {
	return packet.NewWriter().U8(notice).CString(channel).CString(name).Bytes()
}

// ChannelModeChange is Channel::MakeModeChange (Channel.cpp:971-977).
func ChannelModeChange(channel string, player packet.GUID, oldFlags, newFlags uint8) []byte {
	return packet.NewWriter().U8(0x0C).CString(channel).GUID(player).U8(oldFlags).U8(newFlags).Bytes()
}

// ChannelPlayerBy is a kick, ban or unban of target by source (Channel.cpp:1008-1013,
// :1020-1032).
func ChannelPlayerBy(notice uint8, channel string, target, source packet.GUID) []byte {
	return packet.NewWriter().U8(notice).CString(channel).GUID(target).GUID(source).Bytes()
}

// GossipMessage is PlayerMenu::SendGossipMenu (GossipDef.cpp:156-221): the options are
// numbered by their position, as the core numbers them.
func GossipMessage(npc packet.GUID, textID uint32, options []game.GossipOption, quests []game.GossipQuest) []byte {
	w := packet.NewWriter().GUID(npc).U32(textID).U32(uint32(len(options)))
	for i, o := range options {
		coded := uint8(0)
		if o.Coded {
			coded = 1
		}
		w.U32(uint32(i)).U8(o.Icon).U8(coded).CString(o.Text)
	}
	w.U32(uint32(len(quests)))
	for _, q := range quests {
		w.U32(q.ID).U32(q.Icon).U32(q.Level).CString(q.Title)
	}
	return w.Bytes()
}
