package gametest

import "github.com/TortoiseBots/turtle-agent/internal/packet"

// Vendor, trainer and quest packets written the way the core does. Core cites in this file
// are tortoise-wow upstream/1181dev 5784fb0.

// VendorLine is one line of a vendor's list as the core writes it.
type VendorLine struct {
	Entry, Display, Stock, Price, Durability, BuyCount uint32
}

// ListInventory is WorldSession::SendListInventory (ItemHandler.cpp:861-1003): the lines are
// numbered from 1 in the order sent. No lines is the "vendor has no inventory" form: count
// 0 and a result byte of 0 (ItemHandler.cpp:907-914).
func ListInventory(vendor packet.GUID, lines ...VendorLine) []byte {
	w := packet.NewWriter().GUID(vendor).U8(uint8(len(lines)))
	if len(lines) == 0 {
		return w.U8(0).Bytes()
	}
	for i, l := range lines {
		w.U32(uint32(i + 1)).U32(l.Entry).U32(l.Display).U32(l.Stock).U32(l.Price).U32(l.Durability).U32(l.BuyCount)
	}
	return w.Bytes()
}

// BuyItem is the SMSG_BUY_ITEM of Player::BuyItemFromVendor (Player.cpp:20490-20496).
func BuyItem(vendor packet.GUID, slot, stock, count uint32) []byte {
	return packet.NewWriter().GUID(vendor).U32(slot).U32(stock).U32(count).Bytes()
}

// BuyFailed is Player::SendBuyError (Player.cpp:13589-13599).
func BuyFailed(vendor packet.GUID, item uint32, code uint8) []byte {
	return packet.NewWriter().GUID(vendor).U32(item).U8(code).Bytes()
}

// SellItem is Player::SendSellError (Player.cpp:13601-13611).
func SellItem(vendor, item packet.GUID, code uint8) []byte {
	return packet.NewWriter().GUID(vendor).GUID(item).U8(code).Bytes()
}

// TrainerLine is one trainer spell as SendTrainerSpellHelper writes it
// (NPCHandler.cpp:117-159).
type TrainerLine struct {
	Spell                 uint32
	State                 uint8
	Cost                  uint32
	PrimaryProf           bool
	Level                 uint8
	ReqSkill, ReqSkillVal uint32
	Req1, Req2            uint32
}

// TrainerList is WorldSession::SendTrainerList (NPCHandler.cpp:161-263).
func TrainerList(trainer packet.GUID, trainerType uint32, greeting string, lines ...TrainerLine) []byte {
	w := packet.NewWriter().GUID(trainer).U32(trainerType).U32(uint32(len(lines)))
	for _, l := range lines {
		prof := uint32(0)
		if l.PrimaryProf {
			prof = 1
		}
		w.U32(l.Spell).U8(l.State).U32(l.Cost).U32(prof).U32(prof).U8(l.Level).U32(l.ReqSkill).U32(l.ReqSkillVal)
		w.U32(l.Req1).U32(l.Req2).U32(0)
	}
	return w.CString(greeting).Bytes()
}

// TrainerBuySucceeded is SendTrainingSuccess (NPCHandler.cpp:265-271).
func TrainerBuySucceeded(trainer packet.GUID, spell uint32) []byte {
	return packet.NewWriter().GUID(trainer).U32(spell).Bytes()
}

// TrainerBuyFailed is SendTrainingFailure (NPCHandler.cpp:273-280).
func TrainerBuyFailed(trainer packet.GUID, spell, code uint32) []byte {
	return packet.NewWriter().GUID(trainer).U32(spell).U32(code).Bytes()
}

// QuestListEntry is a quest of SMSG_QUESTGIVER_QUEST_LIST.
type QuestListEntry struct {
	ID, Icon, Level uint32
	Title           string
}

// QuestGiverQuestList is PlayerMenu::SendQuestGiverQuestList (GossipDef.cpp:432-492).
func QuestGiverQuestList(npc packet.GUID, greeting string, quests ...QuestListEntry) []byte {
	w := packet.NewWriter().GUID(npc).CString(greeting).U32(0).U32(0).U8(uint8(len(quests)))
	for _, q := range quests {
		w.U32(q.ID).U32(q.Icon).U32(q.Level).CString(q.Title)
	}
	return w.Bytes()
}

// QuestReward is an item with its count; the core follows each with its display id.
type QuestReward struct{ Entry, Count, Display uint32 }

func questRewards(w *packet.Writer, items []QuestReward) {
	w.U32(uint32(len(items)))
	for _, it := range items {
		w.U32(it.Entry).U32(it.Count).U32(it.Display)
	}
}

// QuestGiverQuestDetails is SendQuestGiverQuestDetails (GossipDef.cpp:504-635), with the
// four emote pairs the core always sends.
func QuestGiverQuestDetails(npc packet.GUID, quest uint32, title, details, objectives string, accept bool, choices, rewards []QuestReward, money, spell uint32) []byte {
	w := packet.NewWriter().GUID(npc).U32(quest).CString(title).CString(details).CString(objectives)
	if accept {
		w.U32(1)
	} else {
		w.U32(0)
	}
	questRewards(w, choices)
	questRewards(w, rewards)
	w.U32(money).U32(spell).U32(4)
	for i := 0; i < 4; i++ {
		w.U32(0).U32(0)
	}
	return w.Bytes()
}

// QuestGiverRequestItems is SendQuestGiverRequestItems (GossipDef.cpp:751-846).
func QuestGiverRequestItems(npc packet.GUID, quest uint32, title, text string, money uint32, items []QuestReward, completable bool) []byte {
	w := packet.NewWriter().GUID(npc).U32(quest).CString(title).CString(text).U32(0).U32(0).U32(1).U32(money)
	questRewards(w, items)
	flags := uint32(0)
	if completable {
		flags = 3
	}
	return w.U32(2).U32(flags).U32(4).U32(8).Bytes()
}

// QuestGiverOfferReward is SendQuestGiverOfferReward (GossipDef.cpp:637-749) with one
// emote.
func QuestGiverOfferReward(npc packet.GUID, quest uint32, title, text string, choices, rewards []QuestReward, money, spell uint32) []byte {
	w := packet.NewWriter().GUID(npc).U32(quest).CString(title).CString(text).U32(1).U32(1).U32(0).U32(1)
	questRewards(w, choices)
	questRewards(w, rewards)
	return w.U32(money).U32(0).U32(spell).Bytes()
}

// QuestGiverQuestComplete is Player::SendQuestReward (Player.cpp:16382-16412).
func QuestGiverQuestComplete(quest, xp, money uint32, rewards ...QuestReward) []byte {
	w := packet.NewWriter().U32(quest).U32(3).U32(xp).U32(money).U32(uint32(len(rewards)))
	for _, r := range rewards {
		w.U32(r.Entry).U32(r.Count)
	}
	return w.Bytes()
}

// QuestUpdateAddKill is Player::SendQuestUpdateAddCreatureOrGo (Player.cpp:16510-16535).
func QuestUpdateAddKill(quest, entry, count, required uint32, victim packet.GUID) []byte {
	return packet.NewWriter().U32(quest).U32(entry).U32(count).U32(required).GUID(victim).Bytes()
}
