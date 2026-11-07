package game

import (
	"fmt"

	"github.com/pjerra/turtle-agent/internal/packet"
)

// Flight masters: asking for a flight and the server's answer. Enum names cite
// enum ActivateTaxiReplies in src/game/Objects/Player.h:423-438.

// BuildActivateTaxi is CMSG_ACTIVATETAXI: flight master GUID, source node, destination node
// (TaxiHandler.cpp HandleActivateTaxiOpcode).
func BuildActivateTaxi(npc packet.GUID, from, to uint32) []byte {
	return packet.NewWriter().GUID(npc).U32(from).U32(to).Bytes()
}

// BuildActivateTaxiExpress is CMSG_ACTIVATETAXIEXPRESS: flight master GUID, total cost (the
// server recomputes it), node count, then the route's nodes from source to destination.
func BuildActivateTaxiExpress(npc packet.GUID, nodes []uint32) []byte {
	w := packet.NewWriter().GUID(npc).U32(0).U32(uint32(len(nodes)))
	for _, n := range nodes {
		w.U32(n)
	}
	return w.Bytes()
}

// taxiReplies is the ActivateTaxiReplies enum SMSG_ACTIVATETAXIREPLY carries
// (src/game/Objects/Player.h:423-438).
var taxiReplies = map[uint32]string{
	0:  "ok",
	1:  "unspecified_server_error",
	2:  "no_such_path",
	3:  "not_enough_money",
	4:  "too_far_away",
	5:  "no_vendor_nearby",
	6:  "not_visited",
	7:  "player_busy",
	8:  "player_already_mounted",
	9:  "player_shapeshifted",
	10: "player_moving",
	11: "same_node",
	12: "not_standing",
}

// TaxiReplyName names an ActivateTaxiReply code.
func TaxiReplyName(code uint32) string {
	if n, ok := taxiReplies[code]; ok {
		return n
	}
	return fmt.Sprintf("unknown_%d", code)
}

// TaxiReply is SMSG_ACTIVATETAXIREPLY: the flight master's answer, 0 for an accepted
// flight and a reason code otherwise.
type TaxiReply struct {
	Result uint32
}

// ParseActivateTaxiReply decodes SMSG_ACTIVATETAXIREPLY, a single u32 result.
func ParseActivateTaxiReply(body []byte) (TaxiReply, error) {
	r := packet.NewReader(body)
	t := TaxiReply{Result: r.U32()}
	return t, done(r, "SMSG_ACTIVATETAXIREPLY")
}
