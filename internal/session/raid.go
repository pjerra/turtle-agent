package session

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/TortoiseBots/turtle-agent/internal/events"
	"github.com/TortoiseBots/turtle-agent/internal/game"
	"github.com/TortoiseBots/turtle-agent/internal/opcode"
	"github.com/TortoiseBots/turtle-agent/internal/packet"
)

// Core cites in this file are tortoise-wow upstream/1181dev 5784fb0.

func (s *Session) registerRaid() {
	s.handlers[opcode.MSGRaidTargetUpdate] = s.onRaidTargetUpdate
}

// markWaiter waits for the group's icon to hold guid (0: cleared).
type markWaiter struct {
	icon uint8
	guid packet.GUID
	done chan struct{}
}

type MarkResult struct {
	Icon string      `json:"icon"` // the icon set or cleared
	GUID packet.GUID `json:"guid"`
}

// Mark sets a raid icon (skull, cross, square, moon, triangle, diamond, circle, star) on a
// unit given by guid or name, or with icon "clear" takes the unit's icon off, as the 1.12
// client's SetRaidTarget does. It waits for the server's MSG_RAID_TARGET_UPDATE echo: the
// server keeps icons per group and takes them from the leader or an assistant only, and
// ignores anyone else without a reply (GroupHandler.cpp:476-502).
func (s *Session) Mark(ctx context.Context, g packet.GUID, name, icon string) (MarkResult, error) {
	clearing := strings.EqualFold(icon, "clear")
	id, ok := game.RaidIconByName(icon)
	if !clearing && !ok {
		return MarkResult{}, fmt.Errorf("unknown raid icon %q: use skull, cross, square, moon, triangle, diamond, circle, star or clear", icon)
	}
	g, err := s.pickUnit("mark", g, name)
	if err != nil {
		return MarkResult{}, err
	}
	v, err := s.do(ctx, func() (any, error) {
		if err := s.inWorld(); err != nil {
			return nil, err
		}
		s.mu.Lock()
		party, marks, self := s.party, s.marks, s.self.GUID
		s.mu.Unlock()
		if !party.InGroup {
			return nil, errors.New("mark needs a group: the server keeps raid icons per group")
		}
		if party.Leader != self && !party.Assistant {
			return nil, errors.New("mark needs the group leader or an assistant: the server ignores anyone else (GroupHandler.cpp:494-496)")
		}
		want := g
		if clearing {
			found := false
			for i, m := range marks {
				if m == g {
					id, found = uint8(i), true
					break
				}
			}
			if !found {
				return nil, fmt.Errorf("unit %s has no raid icon to clear", g)
			}
			want = 0
		}
		if err := s.conn.WritePacket(opcode.MSGRaidTargetUpdate, game.BuildRaidTargetUpdate(id, want)); err != nil {
			return nil, err
		}
		w := markWaiter{icon: id, guid: want, done: make(chan struct{})}
		s.markWaiters = append(s.markWaiters, w)
		return w.done, nil
	})
	if err != nil {
		return MarkResult{}, err
	}
	done := v.(chan struct{})
	res := MarkResult{Icon: game.RaidIconName(id), GUID: g}
	timer := time.NewTimer(s.opts.MarkWait)
	defer timer.Stop()
	select {
	case <-done:
		return res, nil
	case <-s.done:
		return MarkResult{}, ErrClosed
	case <-ctx.Done():
		s.dropMarkWaiter(done)
		return MarkResult{}, ctx.Err()
	case <-timer.C:
		s.dropMarkWaiter(done)
		return MarkResult{}, fmt.Errorf("no MSG_RAID_TARGET_UPDATE echo within %s: only the group leader or an assistant may set raid icons", s.opts.MarkWait)
	}
}

func (s *Session) dropMarkWaiter(done chan struct{}) {
	_, _ = s.do(context.Background(), func() (any, error) {
		for i, w := range s.markWaiters {
			if w.done == done {
				s.markWaiters = append(s.markWaiters[:i], s.markWaiters[i+1:]...)
				break
			}
		}
		return nil, nil
	})
}

// onRaidTargetUpdate applies a delta or a full list and logs what changed: a raid_mark
// "cleared" for a unit that lost an icon, "set" for one that got it. The full list the
// server repeats after every group list of a party (Group.cpp:1383-1448) logs nothing new.
func (s *Session) onRaidTargetUpdate(body []byte) error {
	u, err := game.ParseRaidTargetUpdate(body)
	if err != nil {
		return err
	}
	s.mu.Lock()
	old := s.marks
	next := old
	if u.Full {
		next = [game.RaidIconCount]packet.GUID{}
	}
	for _, m := range u.Marks {
		next[m.Icon] = m.GUID
	}
	s.marks = next
	s.mu.Unlock()
	for i := range next {
		if old[i] == next[i] {
			continue
		}
		icon := uint8(i)
		if old[i] != 0 {
			s.emit(events.Event{Kind: events.KindRaidMark, TargetGUID: old[i],
				Data: map[string]any{"action": "cleared", "icon": game.RaidIconName(icon), "icon_id": icon}})
		}
		if next[i] != 0 {
			s.emit(events.Event{Kind: events.KindRaidMark, TargetGUID: next[i],
				Data: map[string]any{"action": "set", "icon": game.RaidIconName(icon), "icon_id": icon}})
		}
	}
	kept := s.markWaiters[:0]
	for _, w := range s.markWaiters {
		if next[w.icon] == w.guid {
			close(w.done)
		} else {
			kept = append(kept, w)
		}
	}
	s.markWaiters = kept
	return nil
}
