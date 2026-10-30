package main

import (
	"sync"

	"github.com/pjerra/turtle-agent/internal/rpc"
)

// The movement modes that walk the agent's character by themselves: a dungeon run (any mode) and the player follow.
const (
	moveOwnerRun    = "dungeon_run"
	moveOwnerFollow = "follow"
)

// moveMode is the one owner of the agent's movement mode, shared by dungeon_run and follow_start. acquire checks and claims in one
// step under mu, so two calls racing each other cannot both start. The owner's worker goroutine releases it once it has exited: a
// stop that gave up waiting for the worker leaves the mode owned ("ending"), and the other mode is refused until the worker is gone.
// A nil *moveMode owns nothing and refuses nothing (a runner built bare in a test).
type moveMode struct {
	mu     sync.Mutex
	owner  string
	ending bool
}

// refusal is the error acquire(owner) would return now when the OTHER mode owns the movement, without claiming anything (a
// cheap early answer; acquire decides). The owner's own busy state is its caller's to answer (dungeon_run: "a dungeon run is going").
func (m *moveMode) refusal(owner string) error {
	if m == nil {
		return nil
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.owner == owner {
		return nil
	}
	return m.refusalLocked(owner)
}

func (m *moveMode) refusalLocked(owner string) error {
	switch {
	case m.owner == "":
		return nil
	case m.owner == moveOwnerFollow && m.ending:
		return &rpc.Error{Code: rpc.CodeGame, Message: "a player follow is still ending (follow_stop did not see it end): try again in a moment"}
	case m.owner == moveOwnerFollow && owner == moveOwnerFollow:
		return &rpc.Error{Code: rpc.CodeGame, Message: "the previous player follow is still ending: try again in a moment"}
	case m.owner == moveOwnerFollow:
		return &rpc.Error{Code: rpc.CodeGame, Message: "a player follow is going: follow_stop it first"}
	case owner == moveOwnerRun:
		return &rpc.Error{Code: rpc.CodeGame, Message: "the previous dungeon run is still ending: try again in a moment"}
	default:
		return &rpc.Error{Code: rpc.CodeGame, Message: "a dungeon run is going: it moves the character itself (dungeon_stop it first)"}
	}
}

// acquire makes owner the mode's owner, or returns why not.
func (m *moveMode) acquire(owner string) error {
	if m == nil {
		return nil
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := m.refusalLocked(owner); err != nil {
		return err
	}
	m.owner, m.ending = owner, false
	return nil
}

// markEnding says the owner was told to stop but its worker has not exited yet.
func (m *moveMode) markEnding(owner string) {
	if m == nil {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.owner == owner {
		m.ending = true
	}
}

// release frees the mode; only its worker calls it, once it has exited.
func (m *moveMode) release(owner string) {
	if m == nil {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.owner == owner {
		m.owner, m.ending = "", false
	}
}
