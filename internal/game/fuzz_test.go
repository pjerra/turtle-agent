package game_test

import (
	"testing"

	"github.com/pjerra/turtle-agent/internal/game"
	"github.com/pjerra/turtle-agent/internal/opcode"
	"github.com/pjerra/turtle-agent/internal/testfixture"
)

// One fuzz target per SMSG parser: any bytes must give a value or an error, never a
// panic or a hang. Seeds are the packets of that opcode in the committed captures plus
// the literal captures in realpackets_test.go. `make test` runs only the seeds (and
// testdata/fuzz/<target>); `make fuzz FUZZTIME=30s` fuzzes them all.
func fuzzParse[T any](f *testing.F, parse func([]byte) (T, error), ops ...uint16) {
	f.Helper()
	seeds := testfixture.Bodies(f, ops...)
	for _, op := range ops {
		seeds = append(seeds, realPackets[op]...)
		seeds = append(seeds, builtSeeds[op]...)
	}
	testfixture.FuzzBody(f, seeds, func(b []byte) { _, _ = parse(b) })
}

func FuzzParseGossipMessage(f *testing.F) {
	fuzzParse(f, game.ParseGossipMessage, opcode.SMSGGossipMessage)
}
func FuzzParseListInventory(f *testing.F) {
	fuzzParse(f, game.ParseListInventory, opcode.SMSGListInventory)
}
func FuzzParseBuyFailed(f *testing.F) { fuzzParse(f, game.ParseBuyFailed, opcode.SMSGBuyFailed) }
func FuzzParseBuyItem(f *testing.F)   { fuzzParse(f, game.ParseBuyItem, opcode.SMSGBuyItem) }
func FuzzParseSellItem(f *testing.F)  { fuzzParse(f, game.ParseSellItem, opcode.SMSGSellItem) }
func FuzzParseInventoryChangeFailure(f *testing.F) {
	fuzzParse(f, game.ParseInventoryChangeFailure, opcode.SMSGInventoryChangeFailure)
}
func FuzzParseResultCode(f *testing.F) {
	fuzzParse(f, func(b []byte) (uint32, error) { return game.ParseResultCode(b, "SMSG_MOUNTRESULT") },
		opcode.SMSGMountresult, opcode.SMSGDismountresult)
}
func FuzzParseChannelNotify(f *testing.F) {
	fuzzParse(f, game.ParseChannelNotify, opcode.SMSGChannelNotify)
}
func FuzzParseMessageChat(f *testing.F) { fuzzParse(f, game.ParseMessageChat, opcode.SMSGMessagechat) }
func FuzzParseCString(f *testing.F) {
	fuzzParse(f, game.ParseCString, opcode.SMSGNotification, opcode.SMSGChatPlayerNotFound)
}
func FuzzParseNameQueryResponse(f *testing.F) {
	fuzzParse(f, game.ParseNameQueryResponse, opcode.SMSGNameQueryResponse)
}
func FuzzParseCreatureQueryResponse(f *testing.F) {
	fuzzParse(f, game.ParseCreatureQueryResponse, opcode.SMSGCreatureQueryResponse)
}

// Movement orders and world changes.

func FuzzParseTeleportAck(f *testing.F) {
	fuzzParse(f, game.ParseTeleportAck, opcode.MSGMoveTeleportAck)
}
func FuzzParseNewWorld(f *testing.F) { fuzzParse(f, game.ParseNewWorld, opcode.SMSGNewWorld) }
func FuzzParseTransferPending(f *testing.F) {
	fuzzParse(f, game.ParseTransferPending, opcode.SMSGTransferPending)
}
func FuzzParseMovementChangeSpeed(f *testing.F) {
	fuzzParse(f, func(b []byte) (game.MovementChange, error) { return game.ParseMovementChange(b, true) },
		opcode.SMSGForceRunSpeedChange, opcode.SMSGForceRunBackSpeedChange, opcode.SMSGForceSwimSpeedChange,
		opcode.SMSGForceWalkSpeedChange, opcode.SMSGForceSwimBackSpeedChange, opcode.SMSGForceTurnRateChange)
}
func FuzzParseMovementChangeFlag(f *testing.F) {
	fuzzParse(f, func(b []byte) (game.MovementChange, error) { return game.ParseMovementChange(b, false) },
		opcode.SMSGForceMoveRoot, opcode.SMSGForceMoveUnroot)
}
func FuzzParseKnockBack(f *testing.F) { fuzzParse(f, game.ParseKnockBack, opcode.SMSGMoveKnockBack) }
func FuzzParseAttackStart(f *testing.F) {
	fuzzParse(f, game.ParseAttackStart, opcode.SMSGAttackstart)
}
func FuzzParseAttackStop(f *testing.F) { fuzzParse(f, game.ParseAttackStop, opcode.SMSGAttackstop) }

// Combat.

func FuzzParseAttackerStateUpdate(f *testing.F) {
	fuzzParse(f, game.ParseAttackerStateUpdate, opcode.SMSGAttackerstateupdate)
}
func FuzzParseSpellNonMeleeDamageLog(f *testing.F) {
	fuzzParse(f, game.ParseSpellNonMeleeDamageLog, opcode.SMSGSpellnonmeleedamagelog)
}
func FuzzParsePeriodicAuraLog(f *testing.F) {
	fuzzParse(f, game.ParsePeriodicAuraLog, opcode.SMSGPeriodicauralog)
}
func FuzzParseSpellHealLog(f *testing.F) {
	fuzzParse(f, game.ParseSpellHealLog, opcode.SMSGSpellheallog)
}
func FuzzParseSpellEnergizeLog(f *testing.F) {
	fuzzParse(f, game.ParseSpellEnergizeLog, opcode.SMSGSpellenergizelog)
}
func FuzzParseSpellStart(f *testing.F) { fuzzParse(f, game.ParseSpellStart, opcode.SMSGSpellStart) }
func FuzzParseSpellGo(f *testing.F)    { fuzzParse(f, game.ParseSpellGo, opcode.SMSGSpellGo) }
func FuzzParseCastResult(f *testing.F) { fuzzParse(f, game.ParseCastResult, opcode.SMSGCastResult) }
func FuzzParseSpellFailedOther(f *testing.F) {
	fuzzParse(f, game.ParseSpellFailedOther, opcode.SMSGSpellFailedOther)
}
func FuzzParseSpellLogMiss(f *testing.F) {
	fuzzParse(f, game.ParseSpellLogMiss, opcode.SMSGSpelllogmiss)
}
func FuzzParseProcResist(f *testing.F) { fuzzParse(f, game.ParseProcResist, opcode.SMSGProcresist) }
func FuzzParsePartyKillLog(f *testing.F) {
	fuzzParse(f, game.ParsePartyKillLog, opcode.SMSGPartykilllog)
}

// Group.

func FuzzParsePartyCommandResult(f *testing.F) {
	fuzzParse(f, game.ParsePartyCommandResult, opcode.SMSGPartyCommandResult)
}
func FuzzParseGroupList(f *testing.F) { fuzzParse(f, game.ParseGroupList, opcode.SMSGGroupList) }
func FuzzParsePartyMemberStats(f *testing.F) {
	fuzzParse(f, game.ParsePartyMemberStats, opcode.SMSGPartyMemberStats, opcode.SMSGPartyMemberStatsFull)
}
func FuzzParseRaidTargetUpdate(f *testing.F) {
	fuzzParse(f, game.ParseRaidTargetUpdate, opcode.MSGRaidTargetUpdate)
}

// Loot.

func FuzzParseLootStartRoll(f *testing.F) {
	fuzzParse(f, game.ParseLootStartRoll, opcode.SMSGLootStartRoll)
}
func FuzzParseLootRoll(f *testing.F)    { fuzzParse(f, game.ParseLootRoll, opcode.SMSGLootRoll) }
func FuzzParseLootRollWon(f *testing.F) { fuzzParse(f, game.ParseLootRollWon, opcode.SMSGLootRollWon) }
func FuzzParseLootAllPassed(f *testing.F) {
	fuzzParse(f, game.ParseLootAllPassed, opcode.SMSGLootAllPassed)
}
func FuzzParseLootResponse(f *testing.F) {
	fuzzParse(f, game.ParseLootResponse, opcode.SMSGLootResponse)
}
func FuzzParseLootRelease(f *testing.F) {
	fuzzParse(f, game.ParseLootRelease, opcode.SMSGLootReleaseResponse)
}
func FuzzParseLootRemoved(f *testing.F) { fuzzParse(f, game.ParseLootRemoved, opcode.SMSGLootRemoved) }
func FuzzParseLootMoneyNotify(f *testing.F) {
	fuzzParse(f, game.ParseLootMoneyNotify, opcode.SMSGLootMoneyNotify)
}

// Quests and trainers.

func FuzzParseQuestList(f *testing.F) {
	fuzzParse(f, game.ParseQuestList, opcode.SMSGQuestgiverQuestList)
}
func FuzzParseQuestDetails(f *testing.F) {
	fuzzParse(f, game.ParseQuestDetails, opcode.SMSGQuestgiverQuestDetails)
}
func FuzzParseQuestRequestItems(f *testing.F) {
	fuzzParse(f, game.ParseQuestRequestItems, opcode.SMSGQuestgiverRequestItems)
}
func FuzzParseQuestOfferReward(f *testing.F) {
	fuzzParse(f, game.ParseQuestOfferReward, opcode.SMSGQuestgiverOfferReward)
}
func FuzzParseQuestComplete(f *testing.F) {
	fuzzParse(f, game.ParseQuestComplete, opcode.SMSGQuestgiverQuestComplete)
}
func FuzzParseQuestInvalid(f *testing.F) {
	fuzzParse(f, game.ParseQuestInvalid, opcode.SMSGQuestgiverQuestInvalid)
}
func FuzzParseQuestFailedAtTaker(f *testing.F) {
	fuzzParse(f, game.ParseQuestFailedAtTaker, opcode.SMSGQuestgiverQuestFailed)
}
func FuzzParseQuestID(f *testing.F) {
	fuzzParse(f, func(b []byte) (uint32, error) { return game.ParseQuestID(b, "SMSG_QUESTUPDATE_COMPLETE") },
		opcode.SMSGQuestupdateComplete, opcode.SMSGQuestupdateFailed, opcode.SMSGQuestupdateFailedtimer)
}
func FuzzParseQuestUpdateItem(f *testing.F) {
	fuzzParse(f, game.ParseQuestUpdateItem, opcode.SMSGQuestupdateAddItem)
}
func FuzzParseQuestUpdateKill(f *testing.F) {
	fuzzParse(f, game.ParseQuestUpdateKill, opcode.SMSGQuestupdateAddKill)
}
func FuzzParseTrainerList(f *testing.F) { fuzzParse(f, game.ParseTrainerList, opcode.SMSGTrainerList) }
func FuzzParseTrainerSucceeded(f *testing.F) {
	fuzzParse(f, game.ParseTrainerSucceeded, opcode.SMSGTrainerBuySucceeded)
}
func FuzzParseTrainerFailed(f *testing.F) {
	fuzzParse(f, game.ParseTrainerFailed, opcode.SMSGTrainerBuyFailed)
}
