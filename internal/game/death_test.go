package game_test

import (
	"bytes"
	"testing"

	"github.com/pjerra/turtle-agent/internal/game"
	"github.com/pjerra/turtle-agent/internal/game/gametest"
)

func TestResurrectRequestRoundTrip(t *testing.T) {
	for _, c := range []struct {
		name            string
		sickness, timer bool
		caster          uint64
	}{
		{"", false, true, 0x30},
		{"Spirit Healer", true, true, 0xF130000000001234},
		{"", false, false, 0x31},
	} {
		got, err := game.ParseResurrectRequest(gametest.ResurrectRequest(0x30, c.name, c.sickness, c.timer))
		if err != nil || got.Name != c.name || got.Sickness != c.sickness || got.Timer != c.timer || got.Caster != 0x30 {
			t.Fatalf("%+v %v", got, err)
		}
	}
	if _, err := game.ParseResurrectRequest([]byte{1, 2, 3}); err == nil {
		t.Fatal("a short packet must be an error")
	}
	// A name of eight bytes: the layout is 8 + 4 + len + 1 + 1 + 1 bytes (Spell.cpp:5163).
	if b := gametest.ResurrectRequest(1, "Tholfred", true, true); len(b) != 8+4+8+1+1+1 {
		t.Fatalf("%d bytes", len(b))
	}
}

func TestBuildersMatchTheClientsPackets(t *testing.T) {
	if b := game.BuildRepopRequest(); len(b) != 0 {
		t.Fatalf("CMSG_REPOP_REQUEST has no body: % X", b)
	}
	if b := game.BuildCorpseQuery(); len(b) != 0 {
		t.Fatalf("MSG_CORPSE_QUERY from the client has no body: % X", b)
	}
	if b := game.BuildResurrectResponse(0x1122334455667788, true); !bytes.Equal(b, []byte{0x88, 0x77, 0x66, 0x55, 0x44, 0x33, 0x22, 0x11, 1}) {
		t.Fatalf("% X", b)
	}
	if b := game.BuildResurrectResponse(5, false); b[8] != 0 || len(b) != 9 {
		t.Fatalf("% X", b)
	}
	if b := game.BuildReclaimCorpse(0xAB); len(b) != 8 || b[0] != 0xAB {
		t.Fatalf("% X", b)
	}
	if b := game.BuildAreaTrigger(286); !bytes.Equal(b, []byte{0x1E, 0x01, 0, 0}) {
		t.Fatalf("% X", b)
	}
}

func TestCorpseQueryAndDelay(t *testing.T) {
	c, err := game.ParseCorpseQuery(gametest.CorpseQuery(0, -6053.7, -2954.6, 213.7, 70))
	if err != nil || !c.Found || c.Map != 0 || c.CorpseMap != 70 || c.X != -6053.7 || !c.Inside() {
		t.Fatalf("%+v %v", c, err)
	}
	c, err = game.ParseCorpseQuery(gametest.CorpseQuery(1, 10, 20, 30, 1))
	if err != nil || !c.Found || c.Inside() {
		t.Fatalf("a corpse on the same map: %+v %v", c, err)
	}
	if c, err = game.ParseCorpseQuery(gametest.NoCorpse()); err != nil || c.Found {
		t.Fatalf("%+v %v", c, err)
	}
	if _, err = game.ParseCorpseQuery([]byte{1, 0, 0}); err == nil {
		t.Fatal("a truncated answer must be an error")
	}
	if _, err = game.ParseCorpseQuery(nil); err == nil {
		t.Fatal("an empty body must be an error")
	}
	if ms, err := game.ParseCorpseReclaimDelay(gametest.CorpseReclaimDelay(30000)); err != nil || ms != 30000 {
		t.Fatalf("%d %v", ms, err)
	}
	if _, err := game.ParseCorpseReclaimDelay([]byte{1}); err == nil {
		t.Fatal("a short delay must be an error")
	}
}
