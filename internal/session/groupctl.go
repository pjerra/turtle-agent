package session

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/pjerra/turtle-agent/internal/game"
	"github.com/pjerra/turtle-agent/internal/opcode"
	"github.com/pjerra/turtle-agent/internal/packet"
)

// Core cites in this file are tortoise-wow upstream/1181dev 5784fb0.
//
// The server ignores a group command it refuses (wrong rank, not a raid, full subgroup)
// without a reply, so each call below checks the rank it can see, sends the packet, and
// waits for the SMSG_GROUP_LIST that shows the change, bounded by Options.MarkWait.

// groupPoll is how often a call re-reads the group while it waits for the server's list.
const groupPoll = 10 * time.Millisecond

// partyView is the group as the session holds it, without the object store overlay of Party.
func (s *Session) partyView() (p PartyInfo, self packet.GUID, selfName string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	p = s.party
	p.Members = append([]PartyMember{}, s.party.Members...)
	return p, s.self.GUID, s.self.Name
}

// groupMember is a member of the group, the player included.
type groupMember struct {
	GUID      packet.GUID
	Name      string
	Subgroup  int // 1-based; 0 in a party
	Assistant bool
	Online    bool
}

// findMember resolves the member a call names by guid or by name (case-insensitive). The
// player counts as a member: it can hold an assistant flag or sit in a subgroup.
func findMember(p PartyInfo, self packet.GUID, selfName string, g packet.GUID, name string) (groupMember, error) {
	if g == 0 && strings.TrimSpace(name) == "" {
		return groupMember{}, errors.New("needs a group member's guid or name")
	}
	if g == self || (g == 0 && strings.EqualFold(name, selfName)) {
		return groupMember{GUID: self, Name: selfName, Subgroup: p.Subgroup, Assistant: p.Assistant, Online: true}, nil
	}
	for _, m := range p.Members {
		if (g != 0 && m.GUID == g) || (g == 0 && strings.EqualFold(m.Name, name)) {
			return groupMember{GUID: m.GUID, Name: m.Name, Subgroup: m.Subgroup, Assistant: m.Assistant, Online: m.Online}, nil
		}
	}
	if g != 0 {
		return groupMember{}, fmt.Errorf("guid %s is not in your group", g)
	}
	return groupMember{}, fmt.Errorf("%q is not in your group", name)
}

// groupCommand runs check on the session goroutine against the group, and when it returns
// a packet sends it; a nil packet from check means there is nothing to send, since the
// group already has the wanted shape. Then it waits for done to hold on the group list.
func (s *Session) groupCommand(ctx context.Context, what string,
	check func(p PartyInfo, self packet.GUID, selfName string) (send func() error, done func(PartyInfo) bool, err error)) error {
	v, err := s.do(ctx, func() (any, error) {
		if err := s.inWorld(); err != nil {
			return nil, err
		}
		p, self, selfName := s.partyView()
		if !p.InGroup {
			return nil, fmt.Errorf("%s needs a group", what)
		}
		send, done, err := check(p, self, selfName)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", what, err)
		}
		if send != nil {
			if err := send(); err != nil {
				return nil, err
			}
		}
		return done, nil
	})
	if err != nil {
		return err
	}
	return s.awaitGroup(ctx, what, v.(func(PartyInfo) bool))
}

// awaitGroup polls the group until done holds; the server's group list updates it.
func (s *Session) awaitGroup(ctx context.Context, what string, done func(PartyInfo) bool) error {
	deadline := time.NewTimer(s.opts.MarkWait)
	defer deadline.Stop()
	tick := time.NewTicker(groupPoll)
	defer tick.Stop()
	for {
		p, _, _ := s.partyView()
		if done(p) {
			return nil
		}
		select {
		case <-tick.C:
		case <-s.done:
			return ErrClosed
		case <-ctx.Done():
			return ctx.Err()
		case <-deadline.C:
			return fmt.Errorf("%s: no SMSG_GROUP_LIST showing it within %s: the server ignores a group command it refuses (check your rank and the group; in a battleground the core refuses removing members)", what, s.opts.MarkWait)
		}
	}
}

func (s *Session) write(op uint16, body []byte) func() error {
	return func() error { return s.conn.WritePacket(op, body) }
}

// needLeader is the error for a command only the group leader may send.
func needLeader(p PartyInfo, self packet.GUID) error {
	if p.Leader != self {
		return errors.New("needs the group leader")
	}
	return nil
}

// Promote makes a group member the leader (CMSG_GROUP_SET_LEADER, GroupHandler.cpp:334-352).
// Only the leader may; the server also needs the target online.
func (s *Session) Promote(ctx context.Context, g packet.GUID, name string) error {
	return s.groupCommand(ctx, "promote", func(p PartyInfo, self packet.GUID, selfName string) (func() error, func(PartyInfo) bool, error) {
		if err := needLeader(p, self); err != nil {
			return nil, nil, err
		}
		m, err := findMember(p, self, selfName, g, name)
		if err != nil {
			return nil, nil, err
		}
		if !m.Online { // the core returns early without a Player object (GroupHandler.cpp:343-345)
			return nil, nil, fmt.Errorf("%s is offline", m.Name)
		}
		done := func(p PartyInfo) bool { return p.Leader == m.GUID }
		if m.GUID == self {
			return nil, done, nil // already the leader
		}
		return s.write(opcode.CMSGGroupSetLeader, game.BuildGroupSetLeader(m.GUID)), done, nil
	})
}

// Assistant sets or, with clear, takes away a raid member's assistant flag
// (CMSG_GROUP_ASSISTANT_LEADER). Only the leader of a raid may; the leader can flag itself.
func (s *Session) Assistant(ctx context.Context, g packet.GUID, name string, clear bool) error {
	return s.groupCommand(ctx, "assistant", func(p PartyInfo, self packet.GUID, selfName string) (func() error, func(PartyInfo) bool, error) {
		if err := needLeader(p, self); err != nil {
			return nil, nil, err
		}
		if !p.Raid {
			return nil, nil, errors.New("needs a raid: convert_raid first")
		}
		m, err := findMember(p, self, selfName, g, name)
		if err != nil {
			return nil, nil, err
		}
		done := func(p PartyInfo) bool {
			cur, err := findMember(p, self, selfName, m.GUID, "")
			return err == nil && cur.Assistant == !clear
		}
		if m.Assistant == !clear {
			return nil, done, nil
		}
		return s.write(opcode.CMSGGroupAssistantLeader, game.BuildGroupAssistant(m.GUID, !clear)), done, nil
	})
}

// ConvertRaid turns the leader's party into a raid (CMSG_GROUP_RAID_CONVERT,
// GroupHandler.cpp:505-522). It needs another member to be in the group.
func (s *Session) ConvertRaid(ctx context.Context) error {
	return s.groupCommand(ctx, "convert_raid", func(p PartyInfo, self packet.GUID, _ string) (func() error, func(PartyInfo) bool, error) {
		if err := needLeader(p, self); err != nil {
			return nil, nil, err
		}
		done := func(p PartyInfo) bool { return p.Raid }
		if p.Raid {
			return nil, done, nil
		}
		return s.write(opcode.CMSGGroupRaidConvert, game.BuildGroupRaidConvert()), done, nil
	})
}

// RaidMove puts a raid member in subgroup (1-8, as the raid frame numbers them). The leader
// or an assistant may; the server ignores a move into a full subgroup (five members), so
// then a non-empty swapWith names the member who trades places instead
// (CMSG_GROUP_SWAP_SUB_GROUP).
func (s *Session) RaidMove(ctx context.Context, g packet.GUID, name string, subgroup int, swapWith string) error {
	if subgroup < 1 || subgroup > game.RaidSubgroups {
		return fmt.Errorf("raid_move: subgroup %d: use 1 to %d", subgroup, game.RaidSubgroups)
	}
	return s.groupCommand(ctx, "raid_move", func(p PartyInfo, self packet.GUID, selfName string) (func() error, func(PartyInfo) bool, error) {
		if p.Leader != self && !p.Assistant {
			return nil, nil, errors.New("needs the group leader or an assistant")
		}
		if !p.Raid {
			return nil, nil, errors.New("needs a raid: convert_raid first")
		}
		m, err := findMember(p, self, selfName, g, name)
		if err != nil {
			return nil, nil, err
		}
		inSubgroup := func(p PartyInfo, guid packet.GUID, want int) bool {
			cur, err := findMember(p, self, selfName, guid, "")
			return err == nil && cur.Subgroup == want
		}
		if swapWith != "" {
			o, err := findMember(p, self, selfName, 0, swapWith)
			if err != nil {
				return nil, nil, err
			}
			if o.GUID == m.GUID {
				return nil, nil, errors.New("cannot swap a member with itself")
			}
			// swap trades the two members' subgroups; subgroup names the one m ends in
			if m.Subgroup == o.Subgroup || o.Subgroup != subgroup {
				return nil, nil, fmt.Errorf("%s is in subgroup %d: swap_with must name a member of subgroup %d, in another subgroup than %s",
					o.Name, o.Subgroup, subgroup, m.Name)
			}
			from := m.Subgroup
			done := func(p PartyInfo) bool { return inSubgroup(p, m.GUID, subgroup) && inSubgroup(p, o.GUID, from) }
			return s.write(opcode.CMSGGroupSwapSubGroup, game.BuildGroupSwapSubGroup(m.Name, o.Name)), done, nil
		}
		done := func(p PartyInfo) bool { return inSubgroup(p, m.GUID, subgroup) }
		if m.Subgroup == subgroup {
			return nil, done, nil
		}
		n := 0
		if p.Subgroup == subgroup {
			n++
		}
		for _, o := range p.Members {
			if o.Subgroup == subgroup {
				n++
			}
		}
		if n >= game.GroupSize {
			return nil, nil, fmt.Errorf("subgroup %d is full (%d members): pass swap_with to trade places with one of them", subgroup, n)
		}
		return s.write(opcode.CMSGGroupChangeSubGroup, game.BuildGroupChangeSubGroup(m.Name, uint8(subgroup-1))), done, nil
	})
}

// Disband ends the group for everyone. The 1.12 client has no such packet: leave_group
// only takes the player out, and the group lives on with a new leader. So the leader
// removes every other member (CMSG_GROUP_UNINVITE_GUID), and the server disbands the
// group when it is down to two (Group::RemoveMember).
func (s *Session) Disband(ctx context.Context) error {
	return s.groupCommand(ctx, "disband", func(p PartyInfo, self packet.GUID, _ string) (func() error, func(PartyInfo) bool, error) {
		if err := needLeader(p, self); err != nil {
			return nil, nil, err
		}
		members := p.Members // a snapshot: someone who joins meanwhile stays
		send := func() error {
			for _, m := range members {
				if err := s.conn.WritePacket(opcode.CMSGGroupUninviteGuid, game.BuildGroupUninviteGUID(m.GUID)); err != nil {
					return err
				}
			}
			return nil
		}
		return send, func(p PartyInfo) bool { return !p.InGroup }, nil
	})
}

// SetLootMethod sets the group's loot method by name (game.LootMethodNames), its master looter (master_loot only: a
// member's guid or name; the player when both are empty) and its threshold (an item quality 1-5; 0 keeps the group's).
// Only the leader may (CMSG_LOOT_METHOD, GroupHandler.cpp:372-403); the call waits for the SMSG_GROUP_LIST that shows it.
func (s *Session) SetLootMethod(ctx context.Context, method string, master packet.GUID, masterName string, threshold uint8) error {
	m, ok := game.LootMethodByName(method)
	if !ok {
		return fmt.Errorf("loot_method: unknown method %q (one of %s)", method, strings.Join(game.LootMethodNames(), ", "))
	}
	if threshold > 5 {
		return fmt.Errorf("loot_method: threshold %d: an item quality 1 (common) to 5 (legendary)", threshold)
	}
	return s.groupCommand(ctx, "loot_method", func(p PartyInfo, self packet.GUID, selfName string) (func() error, func(PartyInfo) bool, error) {
		if err := needLeader(p, self); err != nil {
			return nil, nil, err
		}
		th := threshold
		if th == 0 {
			th = p.LootThreshold
		}
		if th < 1 { // the server ignores a threshold below common (GroupHandler.cpp:383-384)
			th = 2
		}
		var looter packet.GUID
		if m == game.LootMaster {
			if master == 0 && masterName == "" {
				looter = self
			} else {
				mem, err := findMember(p, self, selfName, master, masterName)
				if err != nil {
					return nil, nil, err
				}
				looter = mem.GUID
			}
		}
		done := func(p PartyInfo) bool {
			return p.LootMethod == method && p.LootThreshold == th && (m != game.LootMaster || p.LootMaster == looter)
		}
		if done(p) {
			return nil, done, nil
		}
		return s.write(opcode.CMSGLootMethod, game.BuildLootMethod(m, looter, th)), done, nil
	})
}
