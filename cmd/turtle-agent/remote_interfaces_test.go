package main

import "github.com/pjerra/turtle-agent/internal/dungeon"

// The commander driver wraps the agent's game in remoteGame, and the remote driver finds its optional abilities by asserting these
// interfaces on it (d.g.(X)): a missing method is not a compile error and silently turns a step into "no hands" (live, 2026-10-15: the
// area_trigger fallback failed with "the agent has no hands" because remoteGame had no AreaTrigger). Every optional interface the remote
// driver asserts is listed here, so a new one that remoteGame lacks fails the build.
var (
	_ dungeon.RemoteGame       = remoteGame{}
	_ dungeon.AgentHands       = remoteGame{}
	_ dungeon.AreaTriggerGame  = remoteGame{}
	_ dungeon.BotLiner         = remoteGame{}
	_ dungeon.Enterer          = remoteGame{}
	_ dungeon.EntryLedger      = remoteGame{}
	_ dungeon.FloorSnapper     = remoteGame{}
	_ dungeon.Follower         = remoteGame{}
	_ dungeon.HireGame         = remoteGame{}
	_ dungeon.BotReviver       = remoteGame{}
	_ dungeon.ItemSource       = remoteGame{}
	_ dungeon.OrderGame        = remoteGame{}
	_ dungeon.PathPlanner      = remoteGame{}
	_ dungeon.Preparer         = remoteGame{}
	_ dungeon.RunLeaderWatcher = remoteGame{}
	_ dungeon.StartHands       = remoteGame{}
	_ dungeon.TeamSource       = remoteGame{}
	_ dungeon.UnitSource       = remoteGame{}
)
