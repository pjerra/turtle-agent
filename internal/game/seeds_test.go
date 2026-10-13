package game_test

import (
	"github.com/pjerra/turtle-agent/internal/game"
	"github.com/pjerra/turtle-agent/internal/game/gametest"
	"github.com/pjerra/turtle-agent/internal/opcode"
	"github.com/pjerra/turtle-agent/internal/packet"
)

// builtSeeds are well-formed packets from the gametest builders for the opcodes that no
// committed capture or realPackets entry covers, so that the fuzzers start inside the parsers
// rather than at the first length check. They are not captures: the builders and the parsers
// share an author. Names are made up.
var builtSeeds = func() map[uint16][][]byte {
	const (
		npc  packet.GUID = 0xF130000012340001
		unit packet.GUID = 0xF130000099990002
		plr  packet.GUID = 0x0000000000000102
	)
	quest := []gametest.QuestReward{{Entry: 1001, Count: 2, Display: 3}}
	return map[uint16][][]byte{
		opcode.SMSGResurrectRequest: {
			gametest.ResurrectRequest(plr, "", false, true),
			gametest.ResurrectRequest(npc, "Spirit Healer", true, true),
		},
		opcode.SMSGCorpseReclaimDelay: {gametest.CorpseReclaimDelay(30000)},
		opcode.MSGCorpseQuery: {
			gametest.CorpseQuery(0, -6053.7, -2954.6, 213.7, 70),
			gametest.NoCorpse(),
		},
		opcode.SMSGListInventory: {
			gametest.ListInventory(npc),
			gametest.ListInventory(npc, gametest.VendorLine{Entry: 159, Display: 18, Stock: 0xFFFFFFFF, Price: 25, BuyCount: 1}),
		},
		opcode.SMSGBuyItem:   {gametest.BuyItem(npc, 1, 5, 2)},
		opcode.SMSGBuyFailed: {gametest.BuyFailed(npc, 159, 2)},
		opcode.SMSGSellItem:  {gametest.SellItem(npc, 0x4000000000000005, 1)},
		opcode.SMSGTrainerList: {
			gametest.TrainerList(npc, 0, "Greetings", gametest.TrainerLine{Spell: 2457, State: 0, Cost: 100, Level: 4}),
			gametest.TrainerList(npc, 2, ""),
		},
		opcode.SMSGTrainerBuySucceeded: {gametest.TrainerBuySucceeded(npc, 2457)},
		opcode.SMSGTrainerBuyFailed:    {gametest.TrainerBuyFailed(npc, 2457, 1)},
		opcode.SMSGQuestgiverQuestList: {
			gametest.QuestGiverQuestList(npc, "Hello", gametest.QuestListEntry{ID: 783, Icon: 2, Level: 1, Title: "A Threat Within"}),
		},
		opcode.SMSGQuestgiverQuestDetails: {
			gametest.QuestGiverQuestDetails(npc, 783, "A Threat Within", "Details", "Objectives", true, quest, quest, 100, 0),
		},
		opcode.SMSGQuestgiverRequestItems: {
			gametest.QuestGiverRequestItems(npc, 783, "A Threat Within", "Bring it", 0, quest, true),
		},
		opcode.SMSGQuestgiverOfferReward: {
			gametest.QuestGiverOfferReward(npc, 783, "A Threat Within", "Well done", quest, quest, 100, 0),
		},
		opcode.SMSGQuestgiverQuestComplete: {gametest.QuestGiverQuestComplete(783, 250, 100, quest...)},
		opcode.SMSGQuestupdateAddKill:      {gametest.QuestUpdateAddKill(783, 6, 3, 8, unit)},
		opcode.SMSGQuestupdateComplete:     {{0x0F, 0x03, 0x00, 0x00}},
		opcode.SMSGLootResponse: {
			gametest.LootResponse(unit, 1, 55),
			gametest.LootResponse(unit, 1, 0, gametest.LootSlot{Slot: 0, Item: 2589, Count: 2, Display: 7}),
			gametest.LootRefused(unit, 1),
		},
		opcode.SMSGLootReleaseResponse: {gametest.LootRelease(unit)},
		opcode.SMSGLootRemoved:         {gametest.LootRemoved(2)},
		opcode.SMSGLootStartRoll:       {gametest.LootStartRoll(unit, 0, 2589, 0, 60000)},
		opcode.SMSGLootRoll:            {gametest.LootRoll(unit, 0, plr, 2589, 0, 87, 1)},
		opcode.SMSGLootRollWon:         {gametest.LootRollWon(unit, 0, 2589, 0, plr, 87, 1)},
		opcode.SMSGLootAllPassed:       {gametest.LootAllPassed(unit, 0, 2589, 0)},
		opcode.MSGRaidTargetUpdate: {
			gametest.RaidTargetDelta(7, unit),
			gametest.RaidTargetList([8]packet.GUID{1: unit, 6: npc}),
		},
		opcode.SMSGMoveKnockBack:          {gametest.KnockBack(plr, 3, 0.7, -0.7, 10, -10)},
		opcode.SMSGForceMoveRoot:          {gametest.MovementOrder(plr, 4)},
		opcode.SMSGForceRunSpeedChange:    {gametest.SpeedChange(plr, 2, 8.5)},
		opcode.SMSGInventoryChangeFailure: {gametest.InventoryChangeFailure(0, 0, 0, 0), gametest.InventoryChangeFailure(1, 12, 5, 6)},
		opcode.SMSGMountresult:            {gametest.ResultCode(2)},
		opcode.SMSGGroupList:              {gametest.GroupList(plr, game.GroupMember{Name: "Alice", GUID: plr})},
		opcode.SMSGGossipMessage: {
			gametest.GossipMessage(npc, 1, []game.GossipOption{{Index: 0, Icon: 1, Text: "Browse"}, {Index: 1, Icon: 0, Coded: true, Text: "Code"}},
				[]game.GossipQuest{{ID: 783, Icon: 2, Level: 1, Title: "A Threat Within"}}),
		},
		opcode.SMSGAttackerstateupdate:    {gametest.AttackerStateUpdate(0x2, plr, unit, 12, 0, 0, 0, 1, 0)},
		opcode.SMSGSpellStart:             {gametest.SpellStart(plr, 133, 2000, unit)},
		opcode.SMSGSpellGo:                {gametest.SpellGo(plr, 133, []packet.GUID{unit}, []packet.GUID{npc}, []uint8{11}, unit)},
		opcode.SMSGCastResult:             {gametest.CastResult(133, -1), gametest.CastResult(133, 7)},
		opcode.SMSGSpellFailedOther:       {gametest.SpellFailedOther(plr, 133)},
		opcode.SMSGSpelllogmiss:           {gametest.SpellLogMiss(133, plr, unit, 1)},
		opcode.SMSGProcresist:             {gametest.ProcResist(plr, unit, 133)},
		opcode.SMSGPartykilllog:           {gametest.PartyKill(plr, unit)},
		opcode.SMSGSpellheallog:           {gametest.SpellHeal(unit, plr, 2050, 100, true)},
		opcode.SMSGSpellenergizelog:       {gametest.SpellEnergize(unit, plr, 5420, 0, 30)},
		opcode.SMSGPeriodicauralog:        {gametest.PeriodicDamage(unit, plr, 172, 3, 9, 32, 0, 0), gametest.PeriodicHeal(unit, plr, 774, 8, 12)},
		opcode.SMSGSpellnonmeleedamagelog: {gametest.SpellNonMeleeDamage(unit, plr, 133, 40, 4, 0, 0, false, 0, 0)},
		opcode.SMSGChannelNotify:          {gametest.ChannelNotice(1, "General"), gametest.ChannelYouJoined("General", 0)},
		opcode.SMSGInitialSpells: {
			gametest.InitialSpells([]uint32{78, 2457}, nil),
			gametest.InitialSpells([]uint32{116}, []game.InitialCooldown{{Spell: 116, Category: 4, Cooldown: 3000, CategoryCooldown: 3000}, {Spell: 5, Item: 6, Cooldown: 1, CategoryCooldown: game.InfiniteCooldown}}),
		},
		opcode.SMSGRemovedSpell:    {gametest.RemovedSpell(78)},
		opcode.SMSGSupercededSpell: {gametest.SupercededSpell(116, 205)},
		opcode.SMSGSpellCooldown:   {gametest.SpellCooldown(plr, game.SpellCooldown{Spell: 116, MS: 8000}, game.SpellCooldown{Spell: 133, MS: 8000}), gametest.SpellCooldown(plr)},
		opcode.SMSGCooldownEvent:   {gametest.CooldownEvent(1856, plr)},
		opcode.SMSGClearCooldown:   {gametest.CooldownEvent(11129, plr)},
		opcode.SMSGCooldownCheat:   {gametest.CooldownCheat(plr)},
		opcode.MSGChannelStart:     {gametest.ChannelStart(5143, 3000)},
		opcode.MSGChannelUpdate:    {gametest.ChannelUpdate(1500), gametest.ChannelUpdate(0)},
	}
}()
