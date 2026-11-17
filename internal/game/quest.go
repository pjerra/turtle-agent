package game

import (
	"fmt"

	"github.com/TortoiseBots/turtle-agent/internal/packet"
)

// Core cites in this file are tortoise-wow upstream/1181dev 5784fb0.

// BuildQuestgiverHello is CMSG_QUESTGIVER_HELLO: the NPC's GUID (QuestHandler.cpp:83-111).
// The core answers like CMSG_GOSSIP_HELLO: a gossip menu, a quest list, or the one quest.
func BuildQuestgiverHello(npc packet.GUID) []byte { return packet.NewWriter().GUID(npc).Bytes() }

// BuildQuestgiverQueryQuest is CMSG_QUESTGIVER_QUERY_QUEST: NPC GUID and quest id, what the
// client sends when a quest the player is not on is clicked (QuestHandler.cpp:201-219).
func BuildQuestgiverQueryQuest(npc packet.GUID, quest uint32) []byte {
	return packet.NewWriter().GUID(npc).U32(quest).Bytes()
}

// BuildQuestgiverAcceptQuest is CMSG_QUESTGIVER_ACCEPT_QUEST: NPC GUID and quest id
// (QuestHandler.cpp:113-199).
func BuildQuestgiverAcceptQuest(npc packet.GUID, quest uint32) []byte {
	return packet.NewWriter().GUID(npc).U32(quest).Bytes()
}

// BuildQuestgiverCompleteQuest is CMSG_QUESTGIVER_COMPLETE_QUEST: NPC GUID and quest id,
// what the client sends when a quest it is on is clicked (QuestHandler.cpp:581-601).
func BuildQuestgiverCompleteQuest(npc packet.GUID, quest uint32) []byte {
	return packet.NewWriter().GUID(npc).U32(quest).Bytes()
}

// BuildQuestgiverRequestReward is CMSG_QUESTGIVER_REQUEST_REWARD: NPC GUID and quest id,
// the "Complete Quest" button of the request-items panel (QuestHandler.cpp:451-483).
func BuildQuestgiverRequestReward(npc packet.GUID, quest uint32) []byte {
	return packet.NewWriter().GUID(npc).U32(quest).Bytes()
}

// QuestRewardChoices is QUEST_REWARD_CHOICES_COUNT: the core drops a choice at or above it
// (QuestHandler.cpp:403-408).
const QuestRewardChoices = 6

// BuildQuestgiverChooseReward is CMSG_QUESTGIVER_CHOOSE_REWARD: NPC GUID, quest id and the
// index of the chosen reward item (QuestHandler.cpp:400-449).
func BuildQuestgiverChooseReward(npc packet.GUID, quest, choice uint32) []byte {
	return packet.NewWriter().GUID(npc).U32(quest).U32(choice).Bytes()
}

// BuildQuestlogRemoveQuest is CMSG_QUESTLOG_REMOVE_QUEST: the quest log slot, 0-19
// (QuestHandler.cpp:505-531).
func BuildQuestlogRemoveQuest(slot uint8) []byte { return []byte{slot} }

// QuestLogSize is MAX_QUEST_LOG_SIZE (QuestDef.h:35).
const QuestLogSize = 20

// QuestListEntry is one quest of SMSG_QUESTGIVER_QUEST_LIST, like GossipQuest.
type QuestListEntry = GossipQuest

// QuestList is SMSG_QUESTGIVER_QUEST_LIST (PlayerMenu::SendQuestGiverQuestList,
// GossipDef.cpp:432-492): what an NPC with several quests and no gossip options answers.
type QuestList struct {
	NPC      packet.GUID
	Greeting string
	Quests   []QuestListEntry
}

func ParseQuestList(body []byte) (QuestList, error) {
	r := packet.NewReader(body)
	l := QuestList{NPC: r.GUID(), Greeting: r.CString()}
	r.U32() // emote delay
	r.U32() // emote
	for i, n := uint8(0), r.U8(); i < n && r.Err() == nil; i++ {
		q := QuestListEntry{ID: r.U32(), Icon: r.U32(), Level: r.U32(), Title: r.CString()}
		if r.Err() == nil {
			l.Quests = append(l.Quests, q)
		}
	}
	return l, done(r, "SMSG_QUESTGIVER_QUEST_LIST")
}

// QuestItem is a reward or required item with its count.
type QuestItem struct {
	Entry uint32 `json:"item_entry"`
	Count uint32 `json:"count"`
}

func readQuestItems(r *packet.Reader) []QuestItem {
	var out []QuestItem
	for i, n := uint32(0), r.U32(); i < n && r.Err() == nil; i++ {
		it := QuestItem{Entry: r.U32(), Count: r.U32()}
		r.U32() // display id
		if r.Err() == nil {
			out = append(out, it)
		}
	}
	return out
}

// QuestDetails is SMSG_QUESTGIVER_QUEST_DETAILS (PlayerMenu::SendQuestGiverQuestDetails,
// GossipDef.cpp:504-635): the panel with the Accept button. A quest with hidden rewards
// sends none.
type QuestDetails struct {
	NPC                       packet.GUID
	Quest                     uint32
	Title, Details, Objective string
	AutoAccept                bool // the "auto finish" word, 1 when the server shows the Accept button
	Choices, Rewards          []QuestItem
	Money                     int32 // GetRewOrReqMoney is signed: a negative sum is what the player pays
	RewardSpell               uint32
}

func ParseQuestDetails(body []byte) (QuestDetails, error) {
	r := packet.NewReader(body)
	d := QuestDetails{NPC: r.GUID(), Quest: r.U32(), Title: r.CString(), Details: r.CString(), Objective: r.CString()}
	d.AutoAccept = r.U32() != 0
	d.Choices = readQuestItems(r)
	d.Rewards = readQuestItems(r)
	d.Money = r.I32()
	d.RewardSpell = r.U32()
	for i, n := uint32(0), r.U32(); i < n && r.Err() == nil; i++ {
		r.U32() // emote
		r.U32() // delay
	}
	return d, done(r, "SMSG_QUESTGIVER_QUEST_DETAILS")
}

// QuestRequestItems is SMSG_QUESTGIVER_REQUEST_ITEMS (GossipDef.cpp:751-846): what the NPC
// asks for, and whether the player has it (Completable, flags1 = 3). The core sends the
// offer-reward packet instead when the quest needs no items and is done.
type QuestRequestItems struct {
	NPC         packet.GUID
	Quest       uint32
	Title, Text string
	Money       uint32 // copper the player must hand over
	Items       []QuestItem
	Completable bool
}

func ParseQuestRequestItems(body []byte) (QuestRequestItems, error) {
	r := packet.NewReader(body)
	q := QuestRequestItems{NPC: r.GUID(), Quest: r.U32(), Title: r.CString(), Text: r.CString()}
	r.U32() // emote delay
	r.U32() // emote
	r.U32() // close on cancel
	q.Money = r.U32()
	q.Items = readQuestItems(r)
	r.U32() // 0x02
	q.Completable = r.U32() == 0x03
	r.U32() // 0x04
	r.U32() // 0x08
	return q, done(r, "SMSG_QUESTGIVER_REQUEST_ITEMS")
}

// QuestOfferReward is SMSG_QUESTGIVER_OFFER_REWARD (GossipDef.cpp:637-749): the panel with
// the Complete button, where one of Choices is picked.
type QuestOfferReward struct {
	NPC              packet.GUID
	Quest            uint32
	Title, Text      string
	Choices, Rewards []QuestItem
	Money            int32 // signed, as in the details
	RewardSpell      uint32
}

func ParseQuestOfferReward(body []byte) (QuestOfferReward, error) {
	r := packet.NewReader(body)
	q := QuestOfferReward{NPC: r.GUID(), Quest: r.U32(), Title: r.CString(), Text: r.CString()}
	r.U32() // enable next
	for i, n := uint32(0), r.U32(); i < n && r.Err() == nil; i++ {
		r.U32() // emote delay
		r.U32() // emote
	}
	q.Choices = readQuestItems(r)
	q.Rewards = readQuestItems(r)
	q.Money = r.I32()
	r.U32() // unused
	q.RewardSpell = r.U32()
	return q, done(r, "SMSG_QUESTGIVER_OFFER_REWARD")
}

// QuestComplete is SMSG_QUESTGIVER_QUEST_COMPLETE, sent when the reward was handed over
// (Player::SendQuestReward, Player.cpp:16382-16412). The core writes 3 after the quest id.
type QuestComplete struct {
	Quest   uint32
	XP      uint32
	Money   int32 // signed, as in the details: the core writes GetRewOrReqMoney
	Rewards []QuestItem
}

func ParseQuestComplete(body []byte) (QuestComplete, error) {
	r := packet.NewReader(body)
	c := QuestComplete{Quest: r.U32()}
	r.U32() // 3
	c.XP, c.Money = r.U32(), r.I32()
	for i, n := uint32(0), r.U32(); i < n && r.Err() == nil; i++ {
		it := QuestItem{Entry: r.U32(), Count: r.U32()}
		if r.Err() == nil {
			c.Rewards = append(c.Rewards, it)
		}
	}
	return c, done(r, "SMSG_QUESTGIVER_QUEST_COMPLETE")
}

// QuestInvalidName names the reason of SMSG_QUESTGIVER_QUEST_INVALID (QuestDef.h:46-58;
// Player::SendCanTakeQuestResponse, Player.cpp:16449-16455).
func QuestInvalidName(reason uint32) string {
	switch reason {
	case 0:
		return "requirements_not_met"
	case 1:
		return "low_level"
	case 2:
		return "requirements_not_met"
	case 4, 50:
		return "inventory_full"
	case 6:
		return "wrong_race"
	case 12:
		return "only_one_timed"
	case 13:
		return "already_on_quest"
	case 17:
		return "duplicate_item"
	case 20:
		return "missing_items"
	case 22:
		return "not_enough_money"
	}
	return fmt.Sprintf("unknown(%d)", reason)
}

// ParseQuestInvalid is SMSG_QUESTGIVER_QUEST_INVALID: one u32 reason.
func ParseQuestInvalid(body []byte) (uint32, error) {
	r := packet.NewReader(body)
	v := r.U32()
	return v, done(r, "SMSG_QUESTGIVER_QUEST_INVALID")
}

// QuestFailedAtTaker is SMSG_QUESTGIVER_QUEST_FAILED: the quest a hand-in failed for and
// the reason (Player::SendQuestFailedAtTaker, Player.cpp:16415-16425).
type QuestFailedAtTaker struct{ Quest, Reason uint32 }

func ParseQuestFailedAtTaker(body []byte) (QuestFailedAtTaker, error) {
	r := packet.NewReader(body)
	f := QuestFailedAtTaker{Quest: r.U32(), Reason: r.U32()}
	return f, done(r, "SMSG_QUESTGIVER_QUEST_FAILED")
}

// ParseQuestID reads the single quest id of SMSG_QUESTUPDATE_COMPLETE, _FAILED and
// _FAILEDTIMER (Player.cpp:16371-16446).
func ParseQuestID(body []byte, name string) (uint32, error) {
	r := packet.NewReader(body)
	v := r.U32()
	return v, done(r, name)
}

// QuestUpdateItem is SMSG_QUESTUPDATE_ADD_ITEM: the required item and the amount just
// added, not the total held (Player::SendQuestUpdateAddItem sends newItemCount -
// curItemCount, Player.cpp:15946, 16496-16509). It does not name the quest.
type QuestUpdateItem struct{ Item, Added uint32 }

func ParseQuestUpdateItem(body []byte) (QuestUpdateItem, error) {
	r := packet.NewReader(body)
	u := QuestUpdateItem{Item: r.U32(), Added: r.U32()}
	return u, done(r, "SMSG_QUESTUPDATE_ADD_ITEM")
}

// QuestUpdateKill is SMSG_QUESTUPDATE_ADD_KILL: quest, the creature entry (a game object
// entry has bit 31 set), the count now and the count required, and the victim
// (Player::SendQuestUpdateAddCreatureOrGo, Player.cpp:16510-16535).
type QuestUpdateKill struct {
	Quest, Entry, Count, Required uint32
	Victim                        packet.GUID
}

func ParseQuestUpdateKill(body []byte) (QuestUpdateKill, error) {
	r := packet.NewReader(body)
	u := QuestUpdateKill{Quest: r.U32(), Entry: r.U32(), Count: r.U32(), Required: r.U32(), Victim: r.GUID()}
	return u, done(r, "SMSG_QUESTUPDATE_ADD_KILL")
}
