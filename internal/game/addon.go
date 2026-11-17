package game

import (
	"errors"
	"fmt"
	"strings"

	"github.com/TortoiseBots/turtle-agent/internal/packet"
)

// Addon messages are ordinary CMSG_MESSAGECHAT / SMSG_MESSAGECHAT packets in LANG_ADDON
// whose message is prefix + "\t" + text; the 1.12 client raises CHAT_MSG_ADDON for them.
// Line numbers cite the core at 187af78, the checkout deployed on the test server.

// LangAddon is LANG_ADDON (SharedDefines.h:220).
const LangAddon uint32 = 0xFFFFFFFF

// addonTypes are the chat types IsLanguageAllowedForChatType accepts in LANG_ADDON
// (ChatHandler.cpp:98-125); any other type is dropped (ChatHandler.cpp:61-62).
var addonTypes = map[ChatType]bool{
	ChatParty: true, ChatGuild: true, ChatOfficer: true, ChatRaid: true, ChatRaidLeader: true,
	ChatRaidWarning: true, ChatBattleground: true, ChatBattlegroundLeader: true, ChatChannel: true,
}

// AddonChannelByName maps an addon channel name (party, raid, guild, officer, raid_leader,
// raid_warning, battleground, battleground_leader, channel) to its chat type.
func AddonChannelByName(name string) (ChatType, error) {
	name = strings.ToLower(name)
	if name == "whisper" {
		return 0, errors.New("addon whisper: the core drops LANG_ADDON whispers (ChatHandler.cpp:98-125, :283-307); " +
			"1.12 has no addon whisper, use party, raid, guild or channel")
	}
	for t := range addonTypes {
		if t.String() == name {
			return t, nil
		}
	}
	return 0, fmt.Errorf("addon channel %q cannot be sent; use party, raid, guild, officer, raid_leader, raid_warning, battleground, battleground_leader or channel", name)
}

// BuildAddonMessage lays out CMSG_MESSAGECHAT for an addon message the way the client's
// SendAddonMessage sends it: LANG_ADDON, then the channel name for CHAT_MSG_CHANNEL, then
// prefix + "\t" + text (ChatHandler.cpp:176-183, :264-307). The server drops the whole
// addon channel when AddonChannel is off in its config (ChatHandler.cpp:206-210).
func BuildAddonMessage(t ChatType, prefix, text, channel string) ([]byte, error) {
	if !addonTypes[t] {
		return nil, fmt.Errorf("addon: the core drops LANG_ADDON on %s (ChatHandler.cpp:98-125)", t)
	}
	if prefix == "" {
		return nil, errors.New("addon: empty prefix")
	}
	if strings.Contains(prefix, "\t") {
		return nil, errors.New("addon: the prefix holds a tab, which separates it from the text")
	}
	msg := prefix + "\t" + text
	if strings.Contains(msg, "\x00") {
		return nil, errors.New("addon: NUL byte in the prefix or text")
	}
	if len(msg) > 255 {
		return nil, fmt.Errorf("addon: prefix, tab and text are %d bytes; the client limit is 255", len(msg))
	}
	w := packet.NewWriter().U32(uint32(t)).U32(LangAddon)
	if t == ChatChannel {
		if channel == "" {
			return nil, errors.New("addon: a channel message needs a channel name")
		}
		w.CString(channel)
	}
	return w.CString(msg).Bytes(), nil
}

// SplitAddon splits a LANG_ADDON message at its first tab into prefix and text. ok is
// false when there is no tab: the core also moves say lines holding "MR:" or "TR:" into
// LANG_ADDON (ChatHandler.cpp:358-360), and those come back whole as text.
func SplitAddon(raw string) (prefix, text string, ok bool) {
	i := strings.IndexByte(raw, '\t')
	if i < 0 {
		return "", raw, false
	}
	return raw[:i], raw[i+1:], true
}
