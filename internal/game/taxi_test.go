package game_test

import (
	"bytes"
	"testing"

	"github.com/pjerra/turtle-agent/internal/game"
	"github.com/pjerra/turtle-agent/internal/packet"
)

// A direct flight carries the flight master and its two nodes; a multi-hop one carries
// cost 0 (the server recomputes it), the node count and the route, source first.
func TestBuildActivateTaxi(t *testing.T) {
	const master packet.GUID = 0x0102030405060708
	direct := []byte{0x08, 0x07, 0x06, 0x05, 0x04, 0x03, 0x02, 0x01,
		0x02, 0x00, 0x00, 0x00, 0x06, 0x00, 0x00, 0x00}
	if b := game.BuildActivateTaxi(master, 2, 6); !bytes.Equal(b, direct) {
		t.Fatalf("direct % X", b)
	}
	express := []byte{0x08, 0x07, 0x06, 0x05, 0x04, 0x03, 0x02, 0x01,
		0x00, 0x00, 0x00, 0x00, 0x03, 0x00, 0x00, 0x00,
		0x02, 0x00, 0x00, 0x00, 0x05, 0x00, 0x00, 0x00, 0x06, 0x00, 0x00, 0x00}
	if b := game.BuildActivateTaxiExpress(master, []uint32{2, 5, 6}); !bytes.Equal(b, express) {
		t.Fatalf("express % X", b)
	}
}

func TestTaxiReplyName(t *testing.T) {
	for code, want := range map[uint32]string{0: "ok", 2: "no_such_path", 3: "not_enough_money", 12: "not_standing"} {
		if got := game.TaxiReplyName(code); got != want {
			t.Fatalf("code %d: %q, want %q", code, got, want)
		}
	}
	if got := game.TaxiReplyName(99); got != "unknown_99" {
		t.Fatalf("code 99: %q", got)
	}
	if r, err := game.ParseActivateTaxiReply(packet.NewWriter().U32(3).Bytes()); err != nil || r.Result != 3 {
		t.Fatalf("reply %+v %v", r, err)
	}
	if _, err := game.ParseActivateTaxiReply(packet.NewWriter().U32(3).U32(4).Bytes()); err == nil {
		t.Fatal("trailing bytes accepted")
	}
	// A short body (0-3 bytes) is an error, not a zero result.
	for _, body := range [][]byte{nil, {0x03}, {0x03, 0x00}, {0x03, 0x00, 0x00}} {
		if _, err := game.ParseActivateTaxiReply(body); err == nil {
			t.Fatalf("%d bytes accepted", len(body))
		}
	}
}
