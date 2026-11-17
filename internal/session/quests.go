package session

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/TortoiseBots/turtle-agent/internal/events"
	"github.com/TortoiseBots/turtle-agent/internal/game"
	"github.com/TortoiseBots/turtle-agent/internal/objects"
	"github.com/TortoiseBots/turtle-agent/internal/opcode"
	"github.com/TortoiseBots/turtle-agent/internal/packet"
)

// Core cites in this file are tortoise-wow upstream/1181dev 5784fb0.
//
// The quest dialogue follows the 1.12 client's panels. An NPC with quests answers a hello
// with a gossip menu (quests listed in it), a quest_list, or, for a single quest, straight
// away the panel for it. Then:
//
//	quest not in the log: quest_query -> quest_details -> quest_accept
//	quest in the log:     quest_complete -> quest_request_items -> (items in hand) quest_offer_reward
//	                      -> quest_reward -> quest_rewarded
//
// quest_accept, quest_complete and quest_reward work without the earlier steps too; the core
// checks each request itself.

func (s *Session) registerQuests() {
	s.handlers[opcode.SMSGQuestgiverQuestList] = s.onQuestList
	s.handlers[opcode.SMSGQuestgiverQuestDetails] = s.onQuestDetails
	s.handlers[opcode.SMSGQuestgiverRequestItems] = s.onQuestRequestItems
	s.handlers[opcode.SMSGQuestgiverOfferReward] = s.onQuestOfferReward
	s.handlers[opcode.SMSGQuestgiverQuestComplete] = s.onQuestRewarded
	s.handlers[opcode.SMSGQuestgiverQuestInvalid] = s.onQuestInvalid
	s.handlers[opcode.SMSGQuestgiverQuestFailed] = s.onQuestFailedAtTaker
	s.handlers[opcode.SMSGQuestlogFull] = s.onQuestLogFull
	s.handlers[opcode.SMSGQuestupdateAddKill] = s.onQuestUpdateKill
	s.handlers[opcode.SMSGQuestupdateAddItem] = s.onQuestUpdateItem
	s.handlers[opcode.SMSGQuestupdateComplete] = s.onQuestUpdateID("complete", "SMSG_QUESTUPDATE_COMPLETE")
	s.handlers[opcode.SMSGQuestupdateFailed] = s.onQuestUpdateID("failed", "SMSG_QUESTUPDATE_FAILED")
	s.handlers[opcode.SMSGQuestupdateFailedtimer] = s.onQuestUpdateID("timer_failed", "SMSG_QUESTUPDATE_FAILEDTIMER")
}

type questKey struct {
	npc   packet.GUID
	quest uint32
}

// pendingQuest is an accept or an abandon waiting for the quest log to change.
type pendingQuest struct {
	npc     packet.GUID
	quest   uint32
	expires time.Time
}

// QuestLog lists the quests in the character's log, from its own update fields.
func (s *Session) QuestLog() []objects.QuestSlot {
	return s.store.QuestLog(s.selfGUID())
}

func (s *Session) questSlot(quest uint32) (objects.QuestSlot, bool) {
	for _, q := range s.QuestLog() {
		if q.ID == quest {
			return q, true
		}
	}
	return objects.QuestSlot{}, false
}

// QuestHello talks to a quest giver (CMSG_QUESTGIVER_HELLO). The core answers as for a
// gossip hello; the answer is a gossip, quest_list, quest_details or quest_request_items
// event. The server says nothing when the NPC is out of reach.
func (s *Session) QuestHello(ctx context.Context, npc packet.GUID) error {
	if npc == 0 {
		return errors.New("quest_hello needs the NPC's guid")
	}
	_, err := s.do(ctx, func() (any, error) {
		if err := s.inWorld(); err != nil {
			return nil, err
		}
		s.gossip = nil
		return nil, s.conn.WritePacket(opcode.CMSGQuestgiverHello, game.BuildQuestgiverHello(npc))
	})
	return err
}

// QuestQuery asks the NPC for a quest's panel (CMSG_QUESTGIVER_QUERY_QUEST): a quest_details
// event. The core answers nothing for a quest the NPC neither gives nor takes.
func (s *Session) QuestQuery(ctx context.Context, npc packet.GUID, quest uint32) error {
	if npc == 0 || quest == 0 {
		return errors.New("quest_query needs the NPC's guid and a quest_id")
	}
	return s.send(ctx, opcode.CMSGQuestgiverQueryQuest, game.BuildQuestgiverQueryQuest(npc, quest))
}

// QuestAccept accepts a quest from an NPC (CMSG_QUESTGIVER_ACCEPT_QUEST). The core sends no
// packet for it: the quest turns up in the log, which is a quest_accepted event. A refusal is
// a quest_error event (log full, requirements) or only a gossip_complete when the NPC does
// not give that quest.
func (s *Session) QuestAccept(ctx context.Context, npc packet.GUID, quest uint32) error {
	if npc == 0 || quest == 0 {
		return errors.New("quest_accept needs the NPC's guid and a quest_id")
	}
	_, err := s.do(ctx, func() (any, error) {
		if err := s.inWorld(); err != nil {
			return nil, err
		}
		if _, ok := s.questSlot(quest); ok {
			return nil, fmt.Errorf("quest %d is already in the quest log", quest)
		}
		s.noteAccept(npc, quest)
		return nil, s.conn.WritePacket(opcode.CMSGQuestgiverAcceptQuest, game.BuildQuestgiverAcceptQuest(npc, quest))
	})
	return err
}

// QuestComplete starts handing in a quest the character is on (CMSG_QUESTGIVER_COMPLETE_QUEST,
// what clicking the quest in the NPC's list sends). The NPC answers with a quest_request_items
// event, or at once with quest_offer_reward when it asks for no items. When the items are in
// hand the session presses "Complete Quest" (CMSG_QUESTGIVER_REQUEST_REWARD) for you, so the
// quest_offer_reward follows; pick the reward with QuestReward.
func (s *Session) QuestComplete(ctx context.Context, npc packet.GUID, quest uint32) error {
	if npc == 0 || quest == 0 {
		return errors.New("quest_complete needs the NPC's guid and a quest_id")
	}
	_, err := s.do(ctx, func() (any, error) {
		if err := s.inWorld(); err != nil {
			return nil, err
		}
		s.noteComplete(npc, quest)
		return nil, s.conn.WritePacket(opcode.CMSGQuestgiverCompleteQuest, game.BuildQuestgiverCompleteQuest(npc, quest))
	})
	return err
}

// QuestReward takes the reward of a finished quest (CMSG_QUESTGIVER_CHOOSE_REWARD). choice is the
// index into the offer's choices, 0 to 5; it may be left out (nil) when the offer has at
// most one. When a quest_offer_reward for this NPC and quest was seen the choice is checked
// against it. The result is a quest_rewarded event; a refusal makes the core send the offer again.
func (s *Session) QuestReward(ctx context.Context, npc packet.GUID, quest uint32, choice *uint32) error {
	if npc == 0 || quest == 0 {
		return errors.New("quest_reward needs the NPC's guid and a quest_id")
	}
	if choice != nil && *choice >= game.QuestRewardChoices {
		return fmt.Errorf("quest_reward: choice %d is out of range (0-%d)", *choice, game.QuestRewardChoices-1)
	}
	_, err := s.do(ctx, func() (any, error) {
		if err := s.inWorld(); err != nil {
			return nil, err
		}
		pick := uint32(0)
		if choice != nil {
			pick = *choice
		}
		if o, ok := s.npc.offers[questKey{npc, quest}]; ok {
			n := o.choices
			switch {
			case choice == nil && n > 1:
				return nil, fmt.Errorf("quest_reward: the offer has %d reward choices; pass choice (0-%d)", n, n-1)
			case choice != nil && int(pick) >= n:
				return nil, fmt.Errorf("quest_reward: the offer has %d reward choices; choice %d is out of range", n, pick)
			}
		}
		s.noteReward(npc, quest)
		return nil, s.conn.WritePacket(opcode.CMSGQuestgiverChooseReward, game.BuildQuestgiverChooseReward(npc, quest, pick))
	})
	return err
}

// QuestAbandon drops a quest from the log (CMSG_QUESTLOG_REMOVE_QUEST). The quest leaving the
// log is a quest_abandoned event; the core ignores the request silently when it cannot take
// back the quest's starting items.
func (s *Session) QuestAbandon(ctx context.Context, quest uint32) error {
	if quest == 0 {
		return errors.New("quest_abandon needs a quest_id")
	}
	_, err := s.do(ctx, func() (any, error) {
		if err := s.inWorld(); err != nil {
			return nil, err
		}
		q, ok := s.questSlot(quest)
		if !ok {
			return nil, fmt.Errorf("quest %d is not in the quest log (see quest_log)", quest)
		}
		s.noteAbandon(quest)
		return nil, s.conn.WritePacket(opcode.CMSGQuestlogRemoveQuest, game.BuildQuestlogRemoveQuest(q.Slot))
	})
	return err
}

func (s *Session) noteAccept(npc packet.GUID, quest uint32) {
	s.prunePending(s.now())
	s.npc.accepts = append(s.npc.accepts, pendingQuest{npc: npc, quest: quest, expires: s.now().Add(npcWait)})
}

func (s *Session) noteComplete(npc packet.GUID, quest uint32) {
	s.npc.completes[questKey{npc, quest}] = s.now().Add(npcWait)
}

func (s *Session) noteReward(npc packet.GUID, quest uint32) {
	s.npc.rewarding[quest] = rewardAsk{npc, s.now().Add(npcWait)}
}

func (s *Session) noteAbandon(quest uint32) {
	s.prunePending(s.now())
	s.npc.abandons = append(s.npc.abandons, pendingQuest{quest: quest, expires: s.now().Add(npcWait)})
}

// checkQuestLog turns the log changes the pending accepts and abandons wait for into events.
func (s *Session) checkQuestLog() {
	log := s.QuestLog()
	has := func(id uint32) (objects.QuestSlot, bool) {
		for _, q := range log {
			if q.ID == id {
				return q, true
			}
		}
		return objects.QuestSlot{}, false
	}
	kept := s.npc.accepts[:0]
	for _, p := range s.npc.accepts {
		if q, ok := has(p.quest); ok {
			s.emit(events.Event{Kind: events.KindQuestAccepted, SourceGUID: p.npc, Data: map[string]any{"quest_id": p.quest, "slot": q.Slot, "complete": q.Complete}})
		} else {
			kept = append(kept, p)
		}
	}
	s.npc.accepts = kept
	kept = s.npc.abandons[:0]
	for _, p := range s.npc.abandons {
		if _, ok := has(p.quest); !ok {
			s.emit(events.Event{Kind: events.KindQuestAbandoned, Data: map[string]any{"quest_id": p.quest}})
		} else {
			kept = append(kept, p)
		}
	}
	s.npc.abandons = kept
}

func questItems(items []game.QuestItem) []map[string]any {
	out := make([]map[string]any, len(items))
	for i, it := range items {
		out[i] = map[string]any{"item_entry": it.Entry, "count": it.Count}
	}
	return out
}

func (s *Session) onQuestList(body []byte) error {
	l, err := game.ParseQuestList(body)
	if err != nil {
		return err
	}
	quests := make([]map[string]any, len(l.Quests))
	for i, q := range l.Quests {
		quests[i] = map[string]any{"id": q.ID, "icon": q.Icon, "level": q.Level, "title": q.Title}
	}
	s.emit(events.Event{Kind: events.KindQuestList, SourceGUID: l.NPC, Data: map[string]any{"greeting": l.Greeting, "quests": quests}})
	return nil
}

func (s *Session) onQuestDetails(body []byte) error {
	d, err := game.ParseQuestDetails(body)
	if err != nil {
		return err
	}
	s.emit(events.Event{Kind: events.KindQuestDetails, SourceGUID: d.NPC, Data: map[string]any{
		"quest_id": d.Quest, "title": d.Title, "details": d.Details, "objectives": d.Objective, "can_accept": d.AutoAccept,
		"choices": questItems(d.Choices), "rewards": questItems(d.Rewards), "money": d.Money, "reward_spell": d.RewardSpell}})
	return nil
}

func (s *Session) onQuestRequestItems(body []byte) error {
	q, err := game.ParseQuestRequestItems(body)
	if err != nil {
		return err
	}
	s.emit(events.Event{Kind: events.KindQuestRequestItems, SourceGUID: q.NPC, Data: map[string]any{
		"quest_id": q.Quest, "title": q.Title, "text": q.Text, "money": q.Money, "items": questItems(q.Items), "completable": q.Completable}})
	// A quest_complete asked for the whole hand-in: press "Complete Quest" once the items are in hand.
	k := questKey{q.NPC, q.Quest}
	if until, ok := s.npc.completes[k]; ok {
		delete(s.npc.completes, k)
		if q.Completable && s.now().Before(until) {
			return s.conn.WritePacket(opcode.CMSGQuestgiverRequestReward, game.BuildQuestgiverRequestReward(q.NPC, q.Quest))
		}
	}
	return nil
}

func (s *Session) onQuestOfferReward(body []byte) error {
	q, err := game.ParseQuestOfferReward(body)
	if err != nil {
		return err
	}
	k := questKey{q.NPC, q.Quest}
	delete(s.npc.completes, k)
	s.npc.offers[k] = offer{len(q.Choices), s.now().Add(offerKeep)}
	s.emit(events.Event{Kind: events.KindQuestOfferReward, SourceGUID: q.NPC, Data: map[string]any{
		"quest_id": q.Quest, "title": q.Title, "text": q.Text, "choices": questItems(q.Choices), "rewards": questItems(q.Rewards),
		"money": q.Money, "reward_spell": q.RewardSpell}})
	return nil
}

func (s *Session) onQuestRewarded(body []byte) error {
	c, err := game.ParseQuestComplete(body)
	if err != nil {
		return err
	}
	// The packet does not name the NPC: it is the one the reward was asked of.
	npc := s.npc.rewarding[c.Quest].npc
	delete(s.npc.rewarding, c.Quest)
	delete(s.npc.offers, questKey{npc, c.Quest})
	s.emit(events.Event{Kind: events.KindQuestRewarded, SourceGUID: npc, Data: map[string]any{
		"quest_id": c.Quest, "xp": c.XP, "money": c.Money, "rewards": questItems(c.Rewards)}})
	return nil
}

func (s *Session) onQuestInvalid(body []byte) error {
	reason, err := game.ParseQuestInvalid(body)
	if err != nil {
		return err
	}
	name := game.QuestInvalidName(reason)
	s.emit(events.Event{Kind: events.KindQuestError, Text: "quest refused: " + name, Data: map[string]any{"error": "invalid", "code": reason, "reason": name}})
	return nil
}

func (s *Session) onQuestFailedAtTaker(body []byte) error {
	f, err := game.ParseQuestFailedAtTaker(body)
	if err != nil {
		return err
	}
	name := game.QuestInvalidName(f.Reason)
	s.emit(events.Event{Kind: events.KindQuestError, Text: "quest hand-in failed: " + name,
		Data: map[string]any{"error": "failed_at_giver", "quest_id": f.Quest, "code": f.Reason, "reason": name}})
	return nil
}

func (s *Session) onQuestLogFull([]byte) error {
	s.emit(events.Event{Kind: events.KindQuestError, Text: "quest log full", Data: map[string]any{"error": "log_full"}})
	return nil
}

func (s *Session) onQuestUpdateKill(body []byte) error {
	u, err := game.ParseQuestUpdateKill(body)
	if err != nil {
		return err
	}
	s.emit(events.Event{Kind: events.KindQuestUpdate, TargetGUID: u.Victim, Data: map[string]any{
		"update": "kill", "quest_id": u.Quest, "entry": u.Entry, "count": u.Count, "required": u.Required}})
	return nil
}

func (s *Session) onQuestUpdateItem(body []byte) error {
	u, err := game.ParseQuestUpdateItem(body)
	if err != nil {
		return err
	}
	// The packet names the item and the amount just added (not the total held), not the quest.
	s.emit(events.Event{Kind: events.KindQuestUpdate, Data: map[string]any{"update": "item", "item_entry": u.Item, "added": u.Added}})
	return nil
}

func (s *Session) onQuestUpdateID(update, name string) handler {
	return func(body []byte) error {
		id, err := game.ParseQuestID(body, name)
		if err != nil {
			return err
		}
		s.emit(events.Event{Kind: events.KindQuestUpdate, Data: map[string]any{"update": update, "quest_id": id}})
		return nil
	}
}
