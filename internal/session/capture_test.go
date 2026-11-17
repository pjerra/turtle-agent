package session

import (
	"testing"

	"github.com/TortoiseBots/turtle-agent/internal/capture"
	"github.com/TortoiseBots/turtle-agent/internal/opcode"
	"github.com/TortoiseBots/turtle-agent/internal/packet"
	"github.com/TortoiseBots/turtle-agent/internal/testfixture"
)

// fixture loads a capture recorded on m910q by a milestone's live step (ruling P11:
// the shared testfixture.Load does the work).
func fixture(t *testing.T, name string) []capture.Record {
	t.Helper()
	return testfixture.Load(t, name)
}

// liveCharacter is the test account's character on m910q, a clone of the GM character Harnessb.
const liveCharacter = "Tagentone"

// Pins the SMSG_CHAR_ENUM layout: the source gives a 150-byte tail after the name
// (Player.cpp:2198-2285; the spike guessed 151). It must parse with no bytes left.
func TestM1CharEnumFromCapture(t *testing.T) {
	bodies := capture.Bodies(fixture(t, "m1-login.twcap"), opcode.SMSGCharEnum)
	if len(bodies) != 1 {
		t.Fatalf("%d SMSG_CHAR_ENUM records", len(bodies))
	}
	chars, err := ParseCharEnum(bodies[0])
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range chars {
		if c.Name == liveCharacter && c.GUID.IsPlayer() && c.Level > 0 {
			return
		}
	}
	t.Fatalf("%s not found in %+v", liveCharacter, chars)
}

func TestM1HandshakeShapesFromCapture(t *testing.T) {
	recs := fixture(t, "m1-login.twcap")
	if b := capture.Bodies(recs, opcode.SMSGAuthChallenge); len(b) != 1 || len(b[0]) != 4 {
		t.Fatalf("auth challenge %v", b)
	}
	if b := capture.Bodies(recs, opcode.SMSGAuthResponse); len(b) == 0 || b[len(b)-1][0] != 0x0C || (len(b[len(b)-1]) != 10 && len(b[len(b)-1]) != 1) {
		t.Fatalf("auth response %v", b)
	}
	b := capture.Bodies(recs, opcode.SMSGLoginVerifyWorld)
	if len(b) != 1 || len(b[0]) != 20 {
		t.Fatalf("verify world %v", b)
	}
	r := packet.NewReader(b[0])
	r.U32()
	if x := r.F32(); x == 0 || r.Err() != nil {
		t.Fatalf("verify world position %v %v", x, r.Err())
	}
}
