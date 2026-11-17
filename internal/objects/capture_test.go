package objects

import (
	"testing"

	"github.com/TortoiseBots/turtle-agent/internal/capture"
	"github.com/TortoiseBots/turtle-agent/internal/opcode"
	"github.com/TortoiseBots/turtle-agent/internal/packet"
	"github.com/TortoiseBots/turtle-agent/internal/testfixture"
	"github.com/TortoiseBots/turtle-agent/internal/worldconn"
)

// Review Focus 2 and 3, pinned by captures: every update packet the server sent parses
// completely, compressed ones included, and so do the compressed moves. The fixtures are
// the login (milestone 1) and the units around the player with two bots (milestone 4).
func TestCapturedUpdatesParse(t *testing.T) {
	compressed, blocks := 0, 0
	for _, name := range []string{"m1-login.twcap", "m4-world.twcap"} {
		recs := testfixture.Load(t, name)
		for _, body := range capture.Bodies(recs, opcode.SMSGUpdateObject) {
			b, left, err := parseUpdate(body)
			if err != nil || left != 0 {
				t.Fatalf("%s SMSG_UPDATE_OBJECT: %v, %d bytes left after %d blocks\n%x", name, err, left, len(b), body)
			}
			blocks += len(b)
		}
		for _, body := range capture.Bodies(recs, opcode.SMSGCompressedUpdateObject) {
			plain, err := Inflate(body)
			if err != nil {
				t.Fatalf("%s SMSG_COMPRESSED_UPDATE_OBJECT: %v\n%x", name, err, body)
			}
			b, left, err := parseUpdate(plain)
			if err != nil || left != 0 {
				t.Fatalf("%s SMSG_COMPRESSED_UPDATE_OBJECT: %v, %d bytes left after %d blocks\n%x", name, err, left, len(b), plain)
			}
			compressed++
			blocks += len(b)
		}
		for _, body := range capture.Bodies(recs, opcode.SMSGCompressedMoves) {
			subs, err := SplitCompressedMoves(body)
			if err != nil {
				t.Fatalf("%s SMSG_COMPRESSED_MOVES: %v\n%x", name, err, body)
			}
			for _, sp := range subs {
				if sp.Op == opcode.SMSGMonsterMove {
					if _, err := ParseMonsterMove(sp.Body); err != nil {
						t.Fatalf("%s compressed monster move: %v\n%x", name, err, sp.Body)
					}
				}
			}
		}
		for _, body := range capture.Bodies(recs, opcode.SMSGMonsterMove) {
			if _, err := ParseMonsterMove(body); err != nil {
				t.Fatalf("%s SMSG_MONSTER_MOVE: %v\n%x", name, err, body)
			}
		}
	}
	if compressed == 0 || blocks < 10 {
		t.Fatalf("only %d compressed updates and %d blocks in the fixtures", compressed, blocks)
	}
}

// The m4 run met a Voidwalker Minion (entry 8996) summoned by Caliph Scorpidsting. Its
// HIGHGUID_PET GUID carries pet number 326 where a unit carries its entry, and the agent
// of that run asked CMSG_CREATURE_QUERY for 326. Replaying the server's updates, the store
// knows the entry from OBJECT_FIELD_ENTRY, and the GUID itself claims none.
func TestM4PetEntryFromCapture(t *testing.T) {
	const minion = packet.GUID(0xF1400001460000D0)
	recs := testfixture.Load(t, "m4-world.twcap")
	st := NewStore()
	askedWrong := false
	for _, r := range recs {
		body := r.Body
		switch {
		case r.Dir == worldconn.ClientToServer && r.Op == opcode.CMSGCreatureQuery:
			q := packet.NewReader(body)
			askedWrong = askedWrong || (q.U32() == 326 && q.GUID() == minion)
			continue
		case r.Dir != worldconn.ServerToClient || r.Redacted:
			continue
		case r.Op == opcode.SMSGCompressedUpdateObject:
			plain, err := Inflate(body)
			if err != nil {
				t.Fatal(err)
			}
			body = plain
		case r.Op != opcode.SMSGUpdateObject:
			continue
		}
		b, err := ParseUpdate(body)
		if err != nil {
			t.Fatal(err)
		}
		st.Apply(b)
	}
	if !askedWrong {
		t.Fatal("the capture no longer holds the old query for entry 326")
	}
	if minion.Entry() != 0 || st.Entry(minion) != 8996 {
		t.Fatalf("minion entry: GUID %d, store %d; want 0 and 8996", minion.Entry(), st.Entry(minion))
	}
}
