package game

import "github.com/TortoiseBots/turtle-agent/internal/packet"

// Movement flags (Objects/Object.h:210-240).
const (
	MoveForward         uint32 = 0x00000001
	MoveBackward        uint32 = 0x00000002
	MoveStrafeLeft      uint32 = 0x00000004
	MoveStrafeRight     uint32 = 0x00000008
	MovePitchUp         uint32 = 0x00000040
	MovePitchDown       uint32 = 0x00000080
	MoveWalkMode        uint32 = 0x00000100
	MoveRoot            uint32 = 0x00001000
	MoveJumping         uint32 = 0x00002000
	MoveFallingFar      uint32 = 0x00004000
	MoveSwimming        uint32 = 0x00200000
	MoveSplineEnabled   uint32 = 0x00400000
	MoveOnTransport     uint32 = 0x02000000
	MoveSplineElevation uint32 = 0x04000000
	MoveWaterWalking    uint32 = 0x10000000
	MoveSafeFall        uint32 = 0x20000000 // feather fall
	MoveHover           uint32 = 0x40000000

	// MoveMaskMoving is MOVEFLAG_MASK_MOVING (Object.h:242-245): the flags under which the
	// anticheat lets the position change between two packets.
	MoveMaskMoving = MoveForward | MoveBackward | MoveStrafeLeft | MoveStrafeRight | MovePitchUp | MovePitchDown |
		MoveJumping | MoveFallingFar | MoveSplineElevation
)

// MoveInfo is MovementInfo (Object.cpp:64-106 read, :146-180 write).
type MoveInfo struct {
	Flags                                     uint32
	Time                                      uint32
	X, Y, Z, O                                float32
	Transport                                 packet.GUID
	TX, TY, TZ, TO                            float32
	Pitch                                     float32
	FallTime                                  uint32
	JumpZSpeed, JumpCos, JumpSin, JumpXYSpeed float32
	SplineElevation                           float32
}

// ReadMoveInfo reads MovementInfo; a short read sticks in r.Err().
func ReadMoveInfo(r *packet.Reader) MoveInfo {
	var m MoveInfo
	m.Flags, m.Time = r.U32(), r.U32()
	m.X, m.Y, m.Z, m.O = r.F32(), r.F32(), r.F32(), r.F32()
	if m.Flags&MoveOnTransport != 0 {
		m.Transport = r.GUID()
		m.TX, m.TY, m.TZ, m.TO = r.F32(), r.F32(), r.F32(), r.F32()
	}
	if m.Flags&MoveSwimming != 0 {
		m.Pitch = r.F32()
	}
	m.FallTime = r.U32()
	if m.Flags&MoveJumping != 0 {
		m.JumpZSpeed, m.JumpCos, m.JumpSin, m.JumpXYSpeed = r.F32(), r.F32(), r.F32(), r.F32()
	}
	if m.Flags&MoveSplineElevation != 0 {
		m.SplineElevation = r.F32()
	}
	return m
}

// WriteMoveInfo writes MovementInfo in the order MovementInfo::Write uses.
func WriteMoveInfo(w *packet.Writer, m MoveInfo) {
	w.U32(m.Flags).U32(m.Time).F32(m.X).F32(m.Y).F32(m.Z).F32(m.O)
	if m.Flags&MoveOnTransport != 0 {
		w.GUID(m.Transport).F32(m.TX).F32(m.TY).F32(m.TZ).F32(m.TO)
	}
	if m.Flags&MoveSwimming != 0 {
		w.F32(m.Pitch)
	}
	w.U32(m.FallTime)
	if m.Flags&MoveJumping != 0 {
		w.F32(m.JumpZSpeed).F32(m.JumpCos).F32(m.JumpSin).F32(m.JumpXYSpeed)
	}
	if m.Flags&MoveSplineElevation != 0 {
		w.F32(m.SplineElevation)
	}
}
