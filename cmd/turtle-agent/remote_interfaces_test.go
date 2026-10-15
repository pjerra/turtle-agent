package main

import (
	"testing"

	"github.com/pjerra/turtle-agent/internal/dungeon"
)

// The commander driver wraps the agent's game in remoteGame, and the remote driver finds its optional abilities by asserting these
// interfaces on it (d.g.(X)): a missing method is not a compile error and silently turns a step into "no hands" (live, 2026-10-15: the
// area_trigger fallback failed with "the agent has no hands" because remoteGame had no AreaTrigger). Every optional interface the remote
// driver asserts is listed here, so a new one that remoteGame lacks fails the build.
var (
	_ dungeon.RemoteGame       = remoteGame{}
	_ dungeon.AgentHands       = remoteGame{}
	_ dungeon.AreaTriggerGame  = remoteGame{}
	_ dungeon.BotLiner         = remoteGame{}
	_ dungeon.BotLinesAller    = remoteGame{}
	_ dungeon.Enterer          = remoteGame{}
	_ dungeon.EntryLedger      = remoteGame{}
	_ dungeon.FloorSnapper     = remoteGame{}
	_ dungeon.Follower         = remoteGame{}
	_ dungeon.HireGame         = remoteGame{}
	_ dungeon.BotSummoner      = remoteGame{}
	_ dungeon.BotReviver       = remoteGame{}
	_ dungeon.ItemGiver        = remoteGame{}
	_ dungeon.ItemSource       = remoteGame{}
	_ dungeon.ObjectActor      = remoteGame{}
	_ dungeon.OrderGame        = remoteGame{}
	_ dungeon.PathPlanner      = remoteGame{}
	_ dungeon.Preparer         = remoteGame{}
	_ dungeon.Respawner        = remoteGame{}
	_ dungeon.RunLeaderWatcher = remoteGame{}
	_ dungeon.StartHands       = remoteGame{}
	_ dungeon.TeamSource       = remoteGame{}
	_ dungeon.UnitSource       = remoteGame{}
)

// The optional interfaces the dungeon package asserts that remoteGame does NOT implement, and why that is right. Every `d.g.(X)` of the package
// was listed by the function that makes it: the ones on a *RemoteDriver are all in the list above. The ones below are asserted only by the
// group driver (*Driver, built from sessionGame in dungeon_methods.go, never from remoteGame) or on a brain, so a commander run never reaches
// them. The test fails when remoteGame gains one of them, so that it moves into the list above and a remote use of it is not forgotten.
func TestGroupOnlyInterfacesAreNotOnRemoteGame(t *testing.T) {
	var g any = remoteGame{}
	for name, has := range map[string]bool{
		"RefusalSource":  isA[dungeon.RefusalSource](g),  // Driver.drainRefusals: the leader's own server refusals
		"MarkSource":     isA[dungeon.MarkSource](g),     // Driver.ccdUnit: the agent's own raid marks
		"LootRuler":      isA[dungeon.LootRuler](g),      // Driver.openBossLoot, checkLootRules: the leader's loot rules
		"SelfAuraSource": isA[dungeon.SelfAuraSource](g), // Driver.leaderAuraIDs, noteSickness: the leader's own auras
		"CombatMeter":    isA[dungeon.CombatMeter](g),    // Driver.runPulltest (leader mode)
		"StatsBrain":     isA[dungeon.StatsBrain](g),     // asserted on the bot brain, not on the game
		"RezBrain":       isA[dungeon.RezBrain](g),       // asserted on the bot brain, not on the game
	} {
		if has {
			t.Errorf("remoteGame implements dungeon.%s: add it to the compile-time list above and check where the remote driver would use it", name)
		}
	}
}

func isA[T any](v any) bool { _, ok := v.(T); return ok }
