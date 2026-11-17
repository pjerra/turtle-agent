package gametest

import "github.com/TortoiseBots/turtle-agent/internal/packet"

// KnockBack is MovementPacketSender::SendKnockBackToController (MovementPacketSender.cpp:198-205).
// speedZ is what the server writes: -verticalSpeed (Unit.cpp:10674), negative for a throw up.
func KnockBack(g packet.GUID, counter uint32, vcos, vsin, speedXY, speedZ float32) []byte {
	return packet.NewWriter().PackedGUID(g).U32(counter).F32(vcos).F32(vsin).F32(speedXY).F32(speedZ).Bytes()
}

// MovementOrder is SendMovementFlagChangeToController (MovementPacketSender.cpp:259-276):
// the root, water-walk, hover and feather-fall orders and their removals.
func MovementOrder(g packet.GUID, counter uint32) []byte {
	return packet.NewWriter().PackedGUID(g).U32(counter).Bytes()
}

// SpeedChange is SendSpeedChangeToController (MovementPacketSender.cpp:57-66).
func SpeedChange(g packet.GUID, counter uint32, speed float32) []byte {
	return packet.NewWriter().PackedGUID(g).U32(counter).F32(speed).Bytes()
}
