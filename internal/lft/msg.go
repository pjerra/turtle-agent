// Package lft is Turtle's group finder (LFT) as an addon conversation: the message texts a
// real 1.12 client's LFG addon sends and the state the server's answers describe.
//
// Turtle's LFT is not a set of opcodes. The client writes a line prefixed "TW_LFG\t" into
// the guild chat channel; the core's LFTManager::HandleAddonMessage reads it (LFTMgr.cpp:
// 41-90), and the server answers with Player::SendAddonMessage as CHAT_MSG_GUILD from the
// receiving player himself. The fields are ';'-separated with empty fields kept. Listing and
// signup payloads are JSON and are not escaped for the field format, so a description may
// hold ';' and only the leading fields may be split (LFTUserGroups.cpp:616-632).
//
// The package is pure: it builds texts, parses them and folds them into a State. It does not
// talk to a session; cmd/turtle-agent's lfg methods carry the lines.
package lft

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
)

// Prefix is the addon message prefix Turtle's LFG addon uses (LFTMgr.cpp:12).
const Prefix = "TW_LFG"

// maxText is the longest text after the prefix. The addon message is "TW_LFG\t" + text;
// 6+1+248 = 255 bytes, the 1.12 chat message limit.
const maxText = 248

var instancePattern = regexp.MustCompile(`^[a-z0-9]{1,32}$`)

// QueueJoin builds the body of C2S_QUEUE_JOIN. Instances are the core's LFT codes, at most
// ten, deduplicated in order; roles is normalised to the subset of t, h, d in that order.
// The core masks both again (LFTQeueue.cpp:88-95) and refuses an empty mask as "invalid".
func QueueJoin(instances []string, roles string) (string, error) {
	if len(instances) < 1 || len(instances) > 10 {
		return "", fmt.Errorf("%d instances, want 1 to 10", len(instances))
	}
	seen := make(map[string]bool, len(instances))
	list := make([]string, 0, len(instances))
	for _, instance := range instances {
		if !instancePattern.MatchString(instance) {
			return "", fmt.Errorf("instance %q is not 1 to 32 lowercase letters or digits", instance)
		}
		if seen[instance] {
			continue
		}
		seen[instance] = true
		list = append(list, instance)
	}
	normalised := normalizeRoles(roles)
	if normalised == "" {
		return "", fmt.Errorf("roles %q name none of t, h, d", roles)
	}
	text := "C2S_QUEUE_JOIN;" + strings.Join(list, ":") + ";" + normalised
	if len(text) > maxText {
		return "", fmt.Errorf("join text is %d bytes, the limit is %d", len(text), maxText)
	}
	return text, nil
}

// QueueLeave builds C2S_QUEUE_LEAVE. The core treats it as the only decline there is: it
// cancels a role check or an offer, or leaves the queue (LFTQeueue.cpp HandleQueueLeave).
func QueueLeave() string { return "C2S_QUEUE_LEAVE" }

// QueueStatus builds C2S_GET_QUEUE_STATUS. The core answers nothing at all when the player
// is idle (SendQueueStatus, LFTQeueue.cpp:184-209).
func QueueStatus() string { return "C2S_GET_QUEUE_STATUS" }

// RolecheckResponse builds C2S_ROLECHECK_RESPONSE;roles. The core masks the roles with the
// class's allowed roles and silently ignores a mask of 0 (HandleRolecheckResponse,
// LFTQeueue.cpp:112-124), so a wrong answer only times the check out after 90 s.
func RolecheckResponse(roles string) string {
	return "C2S_ROLECHECK_RESPONSE;" + normalizeRoles(roles)
}

// OfferAccept builds C2S_OFFER_ACCEPT. Declining is QueueLeave: the protocol has no
// decline message (LFTQeueue.cpp HandleOfferAccept, CancelOffer).
func OfferAccept() string { return "C2S_OFFER_ACCEPT" }

// GroupsList builds C2S_GET_GROUPS_LIST.
func GroupsList() string { return "C2S_GET_GROUPS_LIST" }

// GroupDetails builds C2S_GET_GROUP_DETAILS;<id>.
func GroupDetails(id uint32) string { return fmt.Sprintf("C2S_GET_GROUP_DETAILS;%d", id) }

// ListingRole is the role field a member of a listed group confirms with:
// C2C_ROLECHECK_RESPONSE;<t|h|d|0>, where 0 or empty removes the row
// (LFTUserGroups.cpp ParseConfirmedRoleIndex, 118-132). Anything else means 0.
func ListingRole(role string) string {
	switch strings.ToLower(strings.TrimSpace(role)) {
	case "t", "h", "d":
		return strings.ToLower(strings.TrimSpace(role))
	}
	return "0"
}

func normalizeRoles(roles string) string {
	mask := Mask(roles)
	var b strings.Builder
	if mask&1 != 0 {
		b.WriteByte('t')
	}
	if mask&2 != 0 {
		b.WriteByte('h')
	}
	if mask&4 != 0 {
		b.WriteByte('d')
	}
	return b.String()
}

// Msg is one addon line after the prefix.
type Msg struct {
	Kind string
	// Fields are the leading ';'-separated fields after Kind. For S2C_GROUPS_LIST_UPDATE the
	// whole remainder is one field ("start", "end" or the listing JSON). For
	// S2C_UPDATE_GROUP they are the listing id and its second field ("start", "end",
	// "invalid" or a role index 1-3), and the JSON payload is in JSON.
	Fields []string
	// JSON is the listing or signup object when the line carries one.
	JSON json.RawMessage
	Raw  string
}

// Parse splits one addon line. It never fails: an unknown kind keeps Kind and Raw, and the
// caller decides what to do with it.
func Parse(text string) Msg {
	m := Msg{Raw: text}
	semi := strings.IndexByte(text, ';')
	if semi < 0 {
		m.Kind = text
		return m
	}
	m.Kind = text[:semi]
	rest := text[semi+1:]
	switch m.Kind {
	case "S2C_GROUPS_LIST_UPDATE":
		m.Fields = []string{rest}
		if isJSONObject(rest) {
			m.JSON = json.RawMessage(rest)
		}
	case "S2C_UPDATE_GROUP":
		m.Fields, m.JSON = splitGroupUpdate(rest)
	default:
		m.Fields = strings.Split(rest, ";")
	}
	return m
}

func splitGroupUpdate(rest string) ([]string, json.RawMessage) {
	first := strings.IndexByte(rest, ';')
	if first < 0 {
		return []string{rest}, nil
	}
	id, tail := rest[:first], rest[first+1:]
	second := strings.IndexByte(tail, ';')
	if second < 0 {
		return []string{id, tail}, nil
	}
	payload := tail[second+1:]
	fields := []string{id, tail[:second]}
	if isJSONObject(payload) {
		return fields, json.RawMessage(payload)
	}
	return fields, nil
}

func isJSONObject(s string) bool {
	s = strings.TrimSpace(s)
	return strings.HasPrefix(s, "{")
}
