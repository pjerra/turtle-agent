package session

import (
	"context"
	"errors"
	"math"
	"strings"

	"github.com/pjerra/turtle-agent/internal/events"
	"github.com/pjerra/turtle-agent/internal/game"
	"github.com/pjerra/turtle-agent/internal/opcode"
	"github.com/pjerra/turtle-agent/internal/packet"
)

type PartyMember struct {
	Name      string      `json:"name"`
	GUID      packet.GUID `json:"guid"`
	Online    bool        `json:"online"`
	Dead      bool        `json:"dead"`
	Leader    bool        `json:"leader"`
	Assistant bool        `json:"assistant,omitempty"` // a raid assistant
	Subgroup  int         `json:"subgroup,omitempty"`  // raid subgroup 1-8, as the raid frame numbers them; 0 in a party
	Health    uint16      `json:"health"`
	MaxHealth uint16      `json:"max_health"`
	PowerType uint8       `json:"power_type"`
	Power     uint16      `json:"power"`
	MaxPower  uint16      `json:"max_power"`
	Level     uint16      `json:"level"`
	Zone      uint16      `json:"zone"`
}

// PartyInfo is the group seen from the player; Members never includes the player.
type PartyInfo struct {
	InGroup       bool          `json:"in_group"`
	Raid          bool          `json:"raid,omitempty"`      // a raid group, not a party
	Assistant     bool          `json:"assistant,omitempty"` // the player is a raid assistant
	Subgroup      int           `json:"subgroup,omitempty"`  // the player's raid subgroup 1-8; 0 in a party
	Leader        packet.GUID   `json:"leader,omitempty"`
	Members       []PartyMember `json:"members"`
	PendingInvite string        `json:"pending_invite,omitempty"`
	// LootMethod, LootMaster and LootThreshold are the group's loot rules from SMSG_GROUP_LIST: the method by name
	// (free_for_all, round_robin, master_loot, group_loot, need_before_greed), the master looter (master_loot only) and
	// the quality threshold (2 uncommon). Under round_robin, group_loot and need_before_greed an item below the threshold
	// is shown only to the member whose turn it was at the kill until that member closes the window (LootMgr.cpp:922-946).
	LootMethod    string      `json:"loot_method,omitempty"`
	LootMaster    packet.GUID `json:"loot_master,omitempty"`
	LootThreshold uint8       `json:"loot_threshold,omitempty"`
	// Marks are the group's raid icons by name (skull, cross, ...), from MSG_RAID_TARGET_UPDATE.
	Marks map[string]packet.GUID `json:"marks,omitempty"`
}

func (s *Session) registerParty() {
	s.handlers[opcode.SMSGGroupInvite] = s.onGroupInvite
	s.handlers[opcode.SMSGPartyCommandResult] = s.onPartyResult
	s.handlers[opcode.SMSGGroupList] = s.onGroupList
	s.handlers[opcode.SMSGGroupDestroyed] = func([]byte) error { s.clearParty("disbanded"); return nil }
	s.handlers[opcode.SMSGGroupUninvite] = func([]byte) error { s.clearParty("uninvited"); return nil }
	s.handlers[opcode.SMSGGroupSetLeader] = s.onGroupSetLeader
	s.handlers[opcode.SMSGPartyMemberStats] = s.onMemberStats
	s.handlers[opcode.SMSGPartyMemberStatsFull] = s.onMemberStats
}

// Party returns a copy of the group; Members is never nil, so the RPC sends []. The server
// sends SMSG_PARTY_MEMBER_STATS only to members out of range of each other
// (Player::SendUpdateToOutOfRangeGroupMembers, Player.cpp:21101); a client reads a nearby
// member's health from its unit fields. So a member the object store holds reports the
// store's health, max health and level, and any other member keeps the last stats values.
func (s *Session) Party() PartyInfo {
	s.mu.Lock()
	p := s.party
	p.Members = append([]PartyMember{}, s.party.Members...)
	p.Marks = nil
	for i, g := range s.marks {
		if g != 0 {
			if p.Marks == nil {
				p.Marks = map[string]packet.GUID{}
			}
			p.Marks[game.RaidIconName(uint8(i))] = g
		}
	}
	s.mu.Unlock()
	for i := range p.Members {
		m := &p.Members[i]
		u, ok := s.store.Get(m.GUID, 0, 0, 0)
		if !ok || u.MaxHealth == 0 {
			continue
		}
		m.Health, m.MaxHealth = clamp16(u.Health), clamp16(u.MaxHealth)
		if u.Level != 0 {
			m.Level = clamp16(u.Level)
		}
	}
	return p
}

// clamp16 fits a unit field into the party stats' 16-bit width.
func clamp16(v uint32) uint16 {
	if v > math.MaxUint16 {
		return math.MaxUint16
	}
	return uint16(v)
}

func (s *Session) inParty(g packet.GUID) bool {
	if g == 0 {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if g != 0 && g == s.self.GUID {
		return true
	}
	for _, m := range s.party.Members {
		if m.GUID == g {
			return true
		}
	}
	return false
}

// partyInvolved reports whether g is the player, a party member, or a unit charmed,
// summoned or created by one of them (a pet, totem or mind-controlled mob). It costs one
// store lookup when g is not a member itself.
func (s *Session) partyInvolved(g packet.GUID) bool {
	if s.inParty(g) {
		return true
	}
	if g == 0 {
		return false
	}
	for _, owner := range s.store.Owners(g) {
		if s.inParty(owner) {
			return true
		}
	}
	return false
}

func (s *Session) Invite(ctx context.Context, name string) error {
	if strings.TrimSpace(name) == "" {
		return errors.New("invite needs a character name")
	}
	return s.send(ctx, opcode.CMSGGroupInvite, game.BuildGroupInvite(name))
}

func (s *Session) AcceptInvite(ctx context.Context) error {
	_, err := s.do(ctx, func() (any, error) {
		if err := s.inWorld(); err != nil {
			return nil, err
		}
		s.mu.Lock()
		pending := s.party.PendingInvite
		s.mu.Unlock()
		if pending == "" {
			return nil, errors.New("no pending group invite")
		}
		if err := s.conn.WritePacket(opcode.CMSGGroupAccept, nil); err != nil {
			return nil, err
		}
		s.mu.Lock()
		s.party.PendingInvite = ""
		s.mu.Unlock()
		return nil, nil
	})
	return err
}

// LeaveGroup sends CMSG_GROUP_DISBAND, which removes the player from the group (GroupHandler.cpp:354-370).
func (s *Session) LeaveGroup(ctx context.Context) error {
	_, err := s.do(ctx, func() (any, error) {
		if err := s.inWorld(); err != nil {
			return nil, err
		}
		if !s.Party().InGroup {
			return nil, errors.New("not in a group")
		}
		return nil, s.conn.WritePacket(opcode.CMSGGroupDisband, nil)
	})
	return err
}

func (s *Session) onGroupInvite(body []byte) error {
	from, err := game.ParseCString(body)
	if err != nil {
		return err
	}
	s.mu.Lock()
	s.party.PendingInvite = from
	s.mu.Unlock()
	s.emit(events.Event{Kind: events.KindParty, Source: from, Text: from + " invites you to a group.",
		Data: map[string]any{"event": "invite", "from": from}})
	return nil
}

func (s *Session) onPartyResult(body []byte) error {
	res, err := game.ParsePartyCommandResult(body)
	if err != nil {
		return err
	}
	s.emit(events.Event{Kind: events.KindParty, Text: res.Text(),
		Data: map[string]any{"event": "result", "operation": res.Op, "member": res.Member, "result": res.Result}})
	return nil
}

func (s *Session) onGroupList(body []byte) error {
	g, err := game.ParseGroupList(body)
	if err != nil {
		return err
	}
	s.mu.Lock()
	old := map[packet.GUID]PartyMember{}
	for _, m := range s.party.Members {
		old[m.GUID] = m
	}
	wasRaid := s.party.Raid
	next := PartyInfo{InGroup: len(g.Members) > 0, Leader: g.Leader, PendingInvite: s.party.PendingInvite}
	next.Raid = next.InGroup && g.Type == game.GroupRaid
	if next.InGroup {
		next.LootMethod, next.LootMaster, next.LootThreshold = game.LootMethodName(g.LootMethod), g.Looter, g.LootThreshold
	}
	next.Assistant = next.InGroup && g.OwnFlags&game.GroupAssistant != 0
	if next.Raid { // a party's list carries subgroup 0 for everyone
		next.Subgroup = int(game.FlagsSubgroup(g.OwnFlags)) + 1
	}
	var joined []PartyMember
	for _, gm := range g.Members {
		m, known := old[gm.GUID]
		m.Name, m.GUID, m.Leader = gm.Name, gm.GUID, gm.GUID == g.Leader
		m.Online, m.Dead = gm.Status&game.MemberOnline != 0, gm.Status&(game.MemberDead|game.MemberGhost) != 0
		m.Assistant, m.Subgroup = next.Raid && gm.Flags&game.GroupAssistant != 0, 0
		if next.Raid {
			m.Subgroup = int(game.FlagsSubgroup(gm.Flags)) + 1
		}
		next.Members = append(next.Members, m)
		delete(old, gm.GUID)
		if !known {
			joined = append(joined, m)
		}
	}
	s.party = next
	if !next.InGroup { // out of the group: its icons are gone with it
		s.marks = [game.RaidIconCount]packet.GUID{}
	}
	s.mu.Unlock()
	// A raid's group lists come without the icon list, which the server resends to parties
	// only (Group.cpp:1385-1403, upstream/1181dev 5784fb0), so ask for it once on joining a
	// raid or converting to one (GroupHandler.cpp:491-492).
	if next.Raid && !wasRaid {
		_ = s.conn.WritePacket(opcode.MSGRaidTargetUpdate, game.BuildRaidTargetRequest())
	}
	for _, m := range joined {
		s.names.setPlayer(m.GUID, m.Name)
		// Ask at once, so health and level arrive without waiting for a change (GroupHandler.cpp:823-827).
		_ = s.conn.WritePacket(opcode.CMSGRequestPartyMemberStats, game.BuildRequestPartyMemberStats(m.GUID))
		s.emit(events.Event{Kind: events.KindParty, Source: m.Name, SourceGUID: m.GUID, Data: map[string]any{"event": "joined"}})
	}
	for _, m := range old {
		s.emit(events.Event{Kind: events.KindParty, Source: m.Name, SourceGUID: m.GUID, Data: map[string]any{"event": "left"}})
	}
	return nil
}

func (s *Session) clearParty(why string) {
	s.mu.Lock()
	s.party = PartyInfo{PendingInvite: s.party.PendingInvite}
	s.marks = [game.RaidIconCount]packet.GUID{}
	s.mu.Unlock()
	s.emit(events.Event{Kind: events.KindParty, Data: map[string]any{"event": why}})
}

func (s *Session) onGroupSetLeader(body []byte) error {
	name, err := game.ParseCString(body)
	if err != nil {
		return err
	}
	s.mu.Lock()
	for i := range s.party.Members {
		s.party.Members[i].Leader = strings.EqualFold(s.party.Members[i].Name, name)
		if s.party.Members[i].Leader {
			s.party.Leader = s.party.Members[i].GUID
		}
	}
	if strings.EqualFold(s.self.Name, name) {
		s.party.Leader = s.self.GUID
	}
	s.mu.Unlock()
	s.emit(events.Event{Kind: events.KindParty, Data: map[string]any{"event": "leader", "name": name}})
	return nil
}

// apply merges the fields st's mask carries into m.
func (m *PartyMember) apply(st game.MemberStats) {
	if st.Mask&game.GroupUpdateStatus != 0 {
		m.Online, m.Dead = st.Status&game.MemberOnline != 0, st.Status&(game.MemberDead|game.MemberGhost) != 0
	}
	if st.Mask&game.GroupUpdateCurHP != 0 {
		m.Health = st.HP
	}
	if st.Mask&game.GroupUpdateMaxHP != 0 {
		m.MaxHealth = st.MaxHP
	}
	if st.Mask&game.GroupUpdatePowerType != 0 {
		m.PowerType = st.PowerType
	}
	if st.Mask&game.GroupUpdateCurPower != 0 {
		m.Power = st.Power
	}
	if st.Mask&game.GroupUpdateMaxPower != 0 {
		m.MaxPower = st.MaxPower
	}
	if st.Mask&game.GroupUpdateLevel != 0 {
		m.Level = st.Level
	}
	if st.Mask&game.GroupUpdateZone != 0 {
		m.Zone = st.Zone
	}
}

// onMemberStats merges an update into the member and emits the merged state (ruling P4):
// a partial mask must not report the fields it leaves out as 0 or false.
func (s *Session) onMemberStats(body []byte) error {
	st, err := game.ParsePartyMemberStats(body)
	if err != nil {
		return err
	}
	s.mu.Lock()
	idx := -1
	for i := range s.party.Members {
		if s.party.Members[i].GUID == st.GUID {
			idx = i
			break
		}
	}
	var merged PartyMember // a GUID outside the group reports only what the packet carries
	if idx >= 0 {
		merged = s.party.Members[idx]
	}
	merged.apply(st)
	if idx >= 0 {
		s.party.Members[idx] = merged
	}
	s.mu.Unlock()
	s.emit(events.Event{Kind: events.KindPartyStats, Source: merged.Name, SourceGUID: st.GUID,
		Data: map[string]any{"mask": st.Mask, "health": merged.Health, "max_health": merged.MaxHealth,
			"online": merged.Online, "dead": merged.Dead}})
	return nil
}
