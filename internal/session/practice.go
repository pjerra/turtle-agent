// practice.go: the GM hands of `raid-walk --practice` (the owner-approved boss-mechanic practice tool, 2026-11-07).
// Every line is built by PracticeGMLine, the one allow-list of the closed set: `.die` (the existing gm_kill), `.npc
// evade`, `.modify hp <n> <n>`, `.respawn` (the selected form only) and `.revive <name>`. A line the set does not
// build is refused before it reaches the session. Each hand runs only on an agent authorized for gm_kill (the
// dialled host:port is the configured world_addr of a test server in the slots table), and each acts on a guid the
// caller resolved itself: evade, hp and respawn select-then-confirm through GMOn, revive names a character.
package session

import (
	"context"
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"github.com/pjerra/turtle-agent/internal/packet"
)

// The kinds PracticeGMLine builds. Nothing else is a practice GM line.
const (
	PracticeGMKill    = "kill"
	PracticeGMEvade   = "evade"
	PracticeGMSetHP   = "set_hp"
	PracticeGMRespawn = "respawn"
	PracticeGMRevive  = "revive"
)

// ErrGMRefused is the practice hands' refusal on an agent not authorized for gm_kill: its dialled host:port is not the
// configured world_addr of a test server in the slots table.
var ErrGMRefused = fmt.Errorf("gm: refused: this agent is not a slot of a test server (m910q, tortoise-test2)")

// practiceNameRe is the character-name shape `.revive` takes (the walk's own botName rule).
var practiceNameRe = regexp.MustCompile(`^[A-Za-z]{2,12}$`)

// PracticeGMLine builds one of the practice GM lines, or refuses every other text. arg is the line's one argument:
// the health for set_hp (decimal, the caller passes the unit's own max for both values), the character name for
// revive, and nothing for kill, evade and respawn.
//
// The core mechanics (cited in internal/raidwalk/practice.go): `.die` kills the selected unit; `.npc evade` leaves
// combat, walks home and resets it (full HP and timers); `.modify hp <hp> <max>` sets max then current health;
// `.respawn` with a selected dead creature respawns that one only (without a selection it respawns every dead
// creature in range, which would bring back cleared trash); `.revive <name>` resurrects the named player at full
// health.
func PracticeGMLine(kind, arg string) (string, error) {
	switch kind {
	case PracticeGMKill:
		if arg != "" {
			return "", fmt.Errorf("practice gm: %s takes no argument, got %q", kind, arg)
		}
		return GMKillLine, nil
	case PracticeGMEvade:
		if arg != "" {
			return "", fmt.Errorf("practice gm: %s takes no argument, got %q", kind, arg)
		}
		return ".npc evade", nil
	case PracticeGMSetHP:
		n, err := strconv.ParseUint(arg, 10, 32)
		if err != nil || n == 0 {
			return "", fmt.Errorf("practice gm: %s wants a positive health, not %q", kind, arg)
		}
		return fmt.Sprintf(".modify hp %d %d", n, n), nil
	case PracticeGMRespawn:
		if arg != "" {
			return "", fmt.Errorf("practice gm: %s takes no argument, got %q", kind, arg)
		}
		return ".respawn", nil
	case PracticeGMRevive:
		if !practiceNameRe.MatchString(arg) {
			return "", fmt.Errorf("practice gm: %s wants a character name, not %q", kind, arg)
		}
		return ".revive " + arg, nil
	}
	return "", fmt.Errorf("practice gm: %q is not in the closed set (kill, evade, set_hp, respawn, revive)", kind)
}

// gmRefuse is the authorization gate every practice hand shares with GMKill.
func (s *Session) gmRefuse() error {
	if !s.opts.AllowGMKill {
		return ErrGMRefused
	}
	return nil
}

// GMEvade sends `.npc evade` on the selected creature g through GMOn.
func (s *Session) GMEvade(ctx context.Context, g packet.GUID) error {
	if err := s.gmRefuse(); err != nil {
		return err
	}
	if !g.IsCreature() {
		return fmt.Errorf("gm evade: %s is not a creature guid", g)
	}
	line, err := PracticeGMLine(PracticeGMEvade, "")
	if err != nil {
		return err
	}
	return s.GMOn(ctx, g, line)
}

// GMSetHP sets the creature g's max and current health to hp through GMOn. hp must be above 0 and no higher than the
// unit's MaxHealth as the client holds it: restoring a boss is a full heal, never a smaller maximum than its own.
func (s *Session) GMSetHP(ctx context.Context, g packet.GUID, hp uint32) error {
	if err := s.gmRefuse(); err != nil {
		return err
	}
	if !g.IsCreature() {
		return fmt.Errorf("gm set hp: %s is not a creature guid", g)
	}
	u, err := s.Unit(g, "")
	if err != nil {
		return fmt.Errorf("gm set hp: %w", err)
	}
	if hp == 0 || u.MaxHealth == 0 || hp > u.MaxHealth {
		return fmt.Errorf("gm set hp: %d is not above 0 and at most the unit's max %d", hp, u.MaxHealth)
	}
	line, err := PracticeGMLine(PracticeGMSetHP, strconv.FormatUint(uint64(hp), 10))
	if err != nil {
		return err
	}
	return s.GMOn(ctx, g, line)
}

// GMRespawn respawns the creature g through GMOn, only after the client shows it dead: an alive unit, or one the
// client no longer holds, is refused (the unselected `.respawn` would respawn every dead creature in range).
func (s *Session) GMRespawn(ctx context.Context, g packet.GUID) error {
	if err := s.gmRefuse(); err != nil {
		return err
	}
	if !g.IsCreature() {
		return fmt.Errorf("gm respawn: %s is not a creature guid", g)
	}
	u, err := s.Unit(g, "")
	if err != nil {
		return fmt.Errorf("gm respawn: %w", err)
	}
	if !u.Dead {
		return fmt.Errorf("gm respawn: %s is alive", g)
	}
	line, err := PracticeGMLine(PracticeGMRespawn, "")
	if err != nil {
		return err
	}
	return s.GMOn(ctx, g, line)
}

// GMRevive resurrects the named character through the plain GM path (no selection): `.revive <name>`.
func (s *Session) GMRevive(ctx context.Context, name string) error {
	if err := s.gmRefuse(); err != nil {
		return err
	}
	line, err := PracticeGMLine(PracticeGMRevive, strings.TrimSpace(name))
	if err != nil {
		return err
	}
	return s.GM(ctx, line)
}
