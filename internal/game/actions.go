package game

import (
	"fmt"
	"math"

	"github.com/pjerra/turtle-agent/internal/packet"
)

// BuildSetSelection is CMSG_SET_SELECTION (MiscHandler.cpp:488-493).
func BuildSetSelection(g packet.GUID) []byte { return packet.NewWriter().GUID(g).Bytes() }

// BuildStandStateChange is CMSG_STANDSTATECHANGE (MiscHandler.cpp HandleStandStateChangeOpcode): the u32 stand state to take, 0 to stand.
func BuildStandStateChange(state uint32) []byte { return packet.NewWriter().U32(state).Bytes() }

// BuildAttackSwing is CMSG_ATTACKSWING (CombatHandler.cpp:30-33); CMSG_ATTACKSTOP is empty.
func BuildAttackSwing(g packet.GUID) []byte { return packet.NewWriter().GUID(g).Bytes() }

// Spell target flags (DBCEnums.h:131-148).
const (
	TargetFlagSelf           uint16 = 0x0000
	TargetFlagUnit           uint16 = 0x0002
	TargetFlagItem           uint16 = 0x0010
	TargetFlagSourceLocation uint16 = 0x0020
	TargetFlagDestLocation   uint16 = 0x0040
	TargetFlagObjectUnk      uint16 = 0x0080
	TargetFlagPvPCorpse      uint16 = 0x0200
	TargetFlagObject         uint16 = 0x0800
	TargetFlagTradeItem      uint16 = 0x1000
	TargetFlagString         uint16 = 0x2000
	TargetFlagCorpse         uint16 = 0x8000
)

// BuildCastSpell is CMSG_CAST_SPELL (SpellHandler.cpp:325-328, Spell.cpp:180-226): a zero
// target casts on the caster.
func BuildCastSpell(spell uint32, target packet.GUID) []byte {
	w := packet.NewWriter().U32(spell)
	if target == 0 {
		return w.U16(TargetFlagSelf).Bytes()
	}
	return w.U16(TargetFlagUnit).PackedGUID(target).Bytes()
}

// BuildCastSpellOnItem is CMSG_CAST_SPELL aimed at an item of the caster (TARGET_FLAG_ITEM, Spell.cpp:166-176): a shaman's weapon
// imbue. The item is named by its guid.
func BuildCastSpellOnItem(spell uint32, item packet.GUID) []byte {
	return packet.NewWriter().U32(spell).U16(TargetFlagItem).PackedGUID(item).Bytes()
}

// BuildCastSpellOnObject is CMSG_CAST_SPELL aimed at a game object (TARGET_FLAG_OBJECT, Spell.cpp:198-199): u32 spell, u16 target mask
// 0x0800, the object's guid packed. A locked chest or a node is opened this way with an Opening spell (SPELL_EFFECT_OPEN_LOCK,
// SpellEffects.cpp:1915-1988); CMSG_GAMEOBJ_USE alone only runs a chest's scripts (GameObject.cpp:1493-1514).
func BuildCastSpellOnObject(spell uint32, object packet.GUID) []byte {
	return packet.NewWriter().U32(spell).U16(TargetFlagObject).PackedGUID(object).Bytes()
}

// BuildMovement is a client MSG_MOVE_*: MovementInfo, no guid (MovementHandler.cpp:295-321).
func BuildMovement(m MoveInfo) []byte {
	w := packet.NewWriter()
	WriteMoveInfo(w, m)
	return w.Bytes()
}

// Facing is the orientation from (x0, y0) towards (x1, y1), in [0, 2π).
func Facing(x0, y0, x1, y1 float32) float32 {
	a := math.Atan2(float64(y1-y0), float64(x1-x0))
	if a < 0 {
		a += 2 * math.Pi
	}
	return float32(a)
}

type TeleportAck struct {
	GUID    packet.GUID
	Counter uint32
	Move    MoveInfo
}

// ParseTeleportAck decodes the server's MSG_MOVE_TELEPORT_ACK (MovementPacketSender.cpp:155-158).
func ParseTeleportAck(body []byte) (TeleportAck, error) {
	r := packet.NewReader(body)
	a := TeleportAck{GUID: r.PackedGUID(), Counter: r.U32()}
	a.Move = ReadMoveInfo(r)
	if err := r.Err(); err != nil {
		return TeleportAck{}, fmt.Errorf("MSG_MOVE_TELEPORT_ACK: %w", err)
	}
	return a, nil
}

// BuildTeleportAck is the client's MSG_MOVE_TELEPORT_ACK (MovementHandler.cpp:228-233).
func BuildTeleportAck(g packet.GUID, counter, clientTime uint32) []byte {
	return packet.NewWriter().GUID(g).U32(counter).U32(clientTime).Bytes()
}

type NewWorld struct {
	Map        uint32
	X, Y, Z, O float32
}

// ParseNewWorld decodes SMSG_NEW_WORLD (Player.cpp:2827-2842).
func ParseNewWorld(body []byte) (NewWorld, error) {
	r := packet.NewReader(body)
	n := NewWorld{Map: r.U32(), X: r.F32(), Y: r.F32(), Z: r.F32(), O: r.F32()}
	if err := r.Err(); err != nil {
		return NewWorld{}, fmt.Errorf("SMSG_NEW_WORLD: %w", err)
	}
	return n, nil
}

// ParseTransferPending returns the destination map (Player.cpp:2784-2791).
func ParseTransferPending(body []byte) (uint32, error) {
	r := packet.NewReader(body)
	m := r.U32()
	if err := r.Err(); err != nil {
		return 0, fmt.Errorf("SMSG_TRANSFER_PENDING: %w", err)
	}
	return m, nil
}

type MovementChange struct {
	GUID    packet.GUID
	Counter uint32
	Speed   float32 // speed changes only
}

// ParseMovementChange decodes SMSG_FORCE_*_SPEED_CHANGE (hasSpeed) and SMSG_FORCE_MOVE_(UN)ROOT
// (MovementPacketSender.cpp:57-66, :259-276).
func ParseMovementChange(body []byte, hasSpeed bool) (MovementChange, error) {
	r := packet.NewReader(body)
	c := MovementChange{GUID: r.PackedGUID(), Counter: r.U32()}
	if hasSpeed {
		c.Speed = r.F32()
	}
	if err := r.Err(); err != nil {
		return MovementChange{}, fmt.Errorf("movement change: %w", err)
	}
	return c, nil
}

// BuildMovementChangeAck acknowledges a forced change: u64 guid, u32 counter, MovementInfo,
// and the new speed for speed changes (MovementHandler.cpp:430-443, :657-668). The server
// matches the echoed speed within 0.01 (Unit.cpp:7497-7523). A nil speed gives the root
// and unroot acks. Knockbacks use BuildKnockBackAck, the water-walk, hover and feather-fall
// toggles BuildMovementToggleAck.
func BuildMovementChangeAck(g packet.GUID, counter uint32, m MoveInfo, speed *float32) []byte {
	w := packet.NewWriter().GUID(g).U32(counter)
	WriteMoveInfo(w, m)
	if speed != nil {
		w.F32(*speed)
	}
	return w.Bytes()
}

// KnockBack is SMSG_MOVE_KNOCK_BACK (MovementPacketSender.cpp:198-205). ZSpeed is the
// server's -verticalSpeed (Unit.cpp:10674): negative throws the player up.
type KnockBack struct {
	GUID                      packet.GUID
	Counter                   uint32
	Cos, Sin, XYSpeed, ZSpeed float32
}

func ParseKnockBack(body []byte) (KnockBack, error) {
	r := packet.NewReader(body)
	k := KnockBack{GUID: r.PackedGUID(), Counter: r.U32(), Cos: r.F32(), Sin: r.F32(), XYSpeed: r.F32(), ZSpeed: r.F32()}
	if err := r.Err(); err != nil {
		return KnockBack{}, fmt.Errorf("SMSG_MOVE_KNOCK_BACK: %w", err)
	}
	return k, nil
}

// BuildKnockBackAck is CMSG_MOVE_KNOCK_BACK_ACK: u64 guid, u32 counter, MovementInfo
// (MovementHandler.cpp:743-750). The server keeps the change only when the MovementInfo
// carries MoveJumping and the order's four values within 0.01 (Unit.cpp:7479-7495), so
// they are copied from k over whatever m holds.
func BuildKnockBackAck(k KnockBack, m MoveInfo) []byte {
	m.Flags |= MoveJumping
	m.JumpZSpeed, m.JumpCos, m.JumpSin, m.JumpXYSpeed = k.ZSpeed, k.Cos, k.Sin, k.XYSpeed
	w := packet.NewWriter().GUID(k.GUID).U32(k.Counter)
	WriteMoveInfo(w, m)
	return w.Bytes()
}

// BuildMovementToggleAck is CMSG_MOVE_WATER_WALK_ACK, CMSG_MOVE_HOVER_ACK or
// CMSG_MOVE_FEATHER_FALL_ACK: u64 guid, u32 counter, MovementInfo, then a u32 apply that
// must match the order (MovementHandler.cpp:549-558, Unit.cpp:7437-7449).
func BuildMovementToggleAck(g packet.GUID, counter uint32, m MoveInfo, apply bool) []byte {
	w := packet.NewWriter().GUID(g).U32(counter)
	WriteMoveInfo(w, m)
	if apply {
		return w.U32(1).Bytes()
	}
	return w.U32(0).Bytes()
}

// BuildCancelCast is CMSG_CANCEL_CAST (SpellHandler.cpp:411-414); spell 0 cancels any
// cast, channel or auto-repeat in progress (Object.cpp:5428-5440).
func BuildCancelCast(spell uint32) []byte { return packet.NewWriter().U32(spell).Bytes() }

type AttackPair struct {
	Attacker, Victim packet.GUID
}

// ParseAttackStart decodes SMSG_ATTACKSTART (Unit.cpp:2704-2712).
func ParseAttackStart(body []byte) (AttackPair, error) {
	r := packet.NewReader(body)
	a := AttackPair{Attacker: r.GUID(), Victim: r.GUID()}
	if err := r.Err(); err != nil {
		return AttackPair{}, fmt.Errorf("SMSG_ATTACKSTART: %w", err)
	}
	return a, nil
}

// ParseAttackStop decodes SMSG_ATTACKSTOP (Unit.cpp:2714-2725).
func ParseAttackStop(body []byte) (AttackPair, error) {
	r := packet.NewReader(body)
	a := AttackPair{Attacker: r.PackedGUID(), Victim: r.PackedGUID()}
	r.U32()
	if err := r.Err(); err != nil {
		return AttackPair{}, fmt.Errorf("SMSG_ATTACKSTOP: %w", err)
	}
	return a, nil
}
