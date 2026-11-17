package game

import (
	"errors"
	"fmt"
	"strings"

	"github.com/TortoiseBots/turtle-agent/internal/packet"
)

// Chat channels. Line numbers cite the core at 187af78, the checkout deployed on the test
// server; ChannelHandler.cpp is the same at 5d79486.

// BuildJoinChannel is CMSG_JOIN_CHANNEL: name, then password (ChannelHandler.cpp:30-49).
// The server answers a name that does not start with a letter with invalid_name
// (ChannelHandler.cpp:38-45).
func BuildJoinChannel(name, password string) ([]byte, error) {
	if err := channelName(name); err != nil {
		return nil, err
	}
	if strings.Contains(password, "\x00") {
		return nil, errors.New("channel: NUL byte in the password")
	}
	return packet.NewWriter().CString(name).CString(password).Bytes(), nil
}

// BuildLeaveChannel is CMSG_LEAVE_CHANNEL: the name only (ChannelHandler.cpp:74-90).
func BuildLeaveChannel(name string) ([]byte, error) {
	if err := channelName(name); err != nil {
		return nil, err
	}
	return packet.NewWriter().CString(name).Bytes(), nil
}

func channelName(name string) error {
	if name == "" {
		return errors.New("channel: empty name")
	}
	if strings.Contains(name, "\x00") {
		return errors.New("channel: NUL byte in the name")
	}
	return nil
}

// ChannelNotice is ChatNotify (Channel.h:35-71).
type ChannelNotice uint8

const (
	ChannelJoinedNotice      ChannelNotice = 0x00
	ChannelLeftNotice        ChannelNotice = 0x01
	ChannelYouJoinedNotice   ChannelNotice = 0x02
	ChannelYouLeftNotice     ChannelNotice = 0x03
	ChannelNotMemberNotice   ChannelNotice = 0x05
	ChannelModeChangeNotice  ChannelNotice = 0x0C
	ChannelInvalidNameNotice ChannelNotice = 0x1B
)

var channelNotices = [...]string{
	"joined", "left", "you_joined", "you_left", "wrong_password", "not_member", "not_moderator",
	"password_changed", "owner_changed", "player_not_found", "not_owner", "channel_owner", "mode_change",
	"announcements_on", "announcements_off", "moderation_on", "moderation_off", "muted", "player_kicked",
	"banned", "player_banned", "player_unbanned", "player_not_banned", "player_already_member", "invite",
	"invite_wrong_faction", "wrong_faction", "invalid_name", "not_moderated", "player_invited",
	"player_invite_banned", "throttled",
}

func (n ChannelNotice) String() string {
	if int(n) < len(channelNotices) {
		return channelNotices[n]
	}
	return fmt.Sprintf("notice_0x%02X", uint8(n))
}

// ChannelNotify is SMSG_CHANNEL_NOTIFY.
type ChannelNotify struct {
	Notice  ChannelNotice
	Channel string
	// Player is the player the notice is about: who joined, left, changed the password
	// or owner, switched announcements or moderation, changed mode, is already a member,
	// invited you; for a kick, ban or unban, the target.
	Player packet.GUID
	Source packet.GUID // kick, ban, unban: who did it
	Name   string      // player not found, channel owner, not banned, invited, invite banned
	Flags  uint32      // you_joined: the channel flags (Channel.h:102-111)
	// mode_change: the player's channel flags before and after (MEMBER_FLAG_*).
	OldFlags, NewFlags uint8
}

// ParseChannelNotify decodes SMSG_CHANNEL_NOTIFY as Channel::Make* writes it
// (Channel.cpp:883-1087; invalid name: ChannelHandler.cpp:41-44). An unknown notice keeps
// its header only.
func ParseChannelNotify(body []byte) (ChannelNotify, error) {
	r := packet.NewReader(body)
	n := ChannelNotify{Notice: ChannelNotice(r.U8()), Channel: r.CString()}
	switch n.Notice {
	case 0x00, 0x01, 0x07, 0x08, 0x0D, 0x0E, 0x0F, 0x10, 0x17, 0x18:
		n.Player = r.GUID()
	case 0x02:
		n.Flags = r.U32()
		r.U32() // a number the client appends to the name; always 0 (Channel.cpp:906)
	case 0x09, 0x0B, 0x16, 0x1D, 0x1E:
		n.Name = r.CString()
	case 0x0C:
		n.Player, n.OldFlags, n.NewFlags = r.GUID(), r.U8(), r.U8()
	case 0x12, 0x14, 0x15:
		n.Player, n.Source = r.GUID(), r.GUID()
	}
	if err := r.Err(); err != nil {
		return ChannelNotify{}, fmt.Errorf("SMSG_CHANNEL_NOTIFY %s: %w", n.Notice, err)
	}
	return n, nil
}

// channelTexts are the client lines from Channel.h:37-70 for notices whose text needs only
// the channel name (%[1]s) and the Name field (%[2]s).
var channelTexts = map[ChannelNotice]string{
	0x02: "Joined Channel: [%[1]s]", 0x03: "Left Channel: [%[1]s]", 0x04: "Wrong password for %[1]s.",
	0x05: "Not on channel %[1]s.", 0x06: "Not a moderator of %[1]s.", 0x09: "[%[1]s] Player %[2]s was not found.",
	0x0A: "[%[1]s] You are not the channel owner.", 0x0B: "[%[1]s] Channel owner is %[2]s.",
	0x11: "[%[1]s] You do not have permission to speak.", 0x13: "[%[1]s] You are banned from that channel.",
	0x16: "[%[1]s] Player %[2]s is not banned.", 0x19: "Target is in the wrong alliance for %[1]s.",
	0x1A: "Wrong alliance for %[1]s.", 0x1B: "Invalid channel name", 0x1C: "%[1]s is not moderated",
	0x1D: "[%[1]s] You invited %[2]s to join the channel", 0x1E: "[%[1]s] %[2]s has been banned.",
	0x1F: "[%[1]s] The number of messages that can be sent to this channel is limited, please wait to send another message.",
}

// Text is the client's line for the notice, or "" when it names a player by GUID.
func (n ChannelNotify) Text() string {
	f, ok := channelTexts[n.Notice]
	if !ok {
		return ""
	}
	if n.Notice == ChannelInvalidNameNotice {
		return f
	}
	return fmt.Sprintf(f, n.Channel, n.Name)
}
