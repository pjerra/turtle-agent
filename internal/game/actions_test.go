package game

import (
	"bytes"
	"math"
	"strings"
	"testing"

	"github.com/pjerra/turtle-agent/internal/packet"
)

const wolfGUID = packet.GUID(0xF13000012B000001)

func TestSelectionAttackAndCastLayouts(t *testing.T) {
	if !bytes.Equal(BuildSetSelection(wolfGUID), packet.NewWriter().GUID(wolfGUID).Bytes()) ||
		!bytes.Equal(BuildAttackSwing(wolfGUID), packet.NewWriter().GUID(wolfGUID).Bytes()) {
		t.Fatal("selection or swing")
	}
	if b := BuildCastSpell(133, wolfGUID); !bytes.Equal(b, packet.NewWriter().U32(133).U16(TargetFlagUnit).PackedGUID(wolfGUID).Bytes()) {
		t.Fatalf("unit cast % X", b)
	}
	if b := BuildCastSpell(2061, 0); !bytes.Equal(b, packet.NewWriter().U32(2061).U16(TargetFlagSelf).Bytes()) {
		t.Fatalf("self cast % X", b)
	}
}

func TestCastSpellOnItemLayout(t *testing.T) {
	const sword = packet.GUID(0x4000000000000003)
	if b := BuildCastSpellOnItem(8232, sword); !bytes.Equal(b, packet.NewWriter().U32(8232).U16(0x0010).PackedGUID(sword).Bytes()) {
		t.Fatalf("item cast % X", b)
	}
}

// A chest is opened by a cast at it: spell, target mask 0x0800 (TARGET_FLAG_OBJECT), the object's guid packed. The bytes are written
// out so a change of layout shows (Baelog's Chest, entry 123329 = 0x01E1C1, counter 1; spell 6477 = 0x194D).
func TestCastSpellOnObjectLayout(t *testing.T) {
	const chest = packet.GUID(0xF11001E1C1000001)
	want := []byte{0x4D, 0x19, 0x00, 0x00, 0x00, 0x08, 0xF9, 0x01, 0xC1, 0xE1, 0x01, 0x10, 0xF1}
	if b := BuildCastSpellOnObject(6477, chest); !bytes.Equal(b, want) {
		t.Fatalf("object cast % X, want % X", b, want)
	}
}

func TestFacing(t *testing.T) {
	for _, c := range []struct{ x, y, want float32 }{{1, 0, 0}, {0, 1, math.Pi / 2}, {-1, 0, math.Pi}, {0, -1, 3 * math.Pi / 2}} {
		if got := Facing(0, 0, c.x, c.y); math.Abs(float64(got-c.want)) > 1e-5 {
			t.Errorf("to (%v,%v): %v want %v", c.x, c.y, got, c.want)
		}
	}
}

func TestTeleportAndNewWorld(t *testing.T) {
	w := packet.NewWriter().PackedGUID(0x2A).U32(7)
	WriteMoveInfo(w, MoveInfo{X: 1, Y: 2, Z: 3, O: 4})
	ack, err := ParseTeleportAck(w.Bytes())
	if err != nil || ack.GUID != 0x2A || ack.Counter != 7 || ack.Move.Z != 3 {
		t.Fatalf("%+v %v", ack, err)
	}
	if b := BuildTeleportAck(0x2A, 7, 99); !bytes.Equal(b, packet.NewWriter().GUID(0x2A).U32(7).U32(99).Bytes()) {
		t.Fatalf("% X", b)
	}
	nw, err := ParseNewWorld(packet.NewWriter().U32(1).F32(10).F32(20).F32(30).F32(0.5).Bytes())
	if err != nil || nw != (NewWorld{Map: 1, X: 10, Y: 20, Z: 30, O: 0.5}) {
		t.Fatalf("%+v %v", nw, err)
	}
	if m, err := ParseTransferPending(packet.NewWriter().U32(1).Bytes()); err != nil || m != 1 {
		t.Fatalf("%v %v", m, err)
	}
}

func TestMovementChangeAndAck(t *testing.T) {
	c, err := ParseMovementChange(packet.NewWriter().PackedGUID(0x2A).U32(3).F32(14).Bytes(), true)
	if err != nil || c.Counter != 3 || c.Speed != 14 {
		t.Fatalf("%+v %v", c, err)
	}
	speed := float32(14)
	b := BuildMovementChangeAck(0x2A, 3, MoveInfo{X: 1}, &speed)
	want := packet.NewWriter().GUID(0x2A).U32(3)
	WriteMoveInfo(want, MoveInfo{X: 1})
	if !bytes.Equal(b, want.F32(14).Bytes()) {
		t.Fatalf("% X", b)
	}
	if c, err := ParseMovementChange(packet.NewWriter().PackedGUID(0x2A).U32(4).Bytes(), false); err != nil || c.Counter != 4 {
		t.Fatalf("root %+v %v", c, err)
	}
}

func TestAttackStartStop(t *testing.T) {
	a, err := ParseAttackStart(packet.NewWriter().GUID(0x2A).GUID(wolfGUID).Bytes())
	if err != nil || a != (AttackPair{0x2A, wolfGUID}) {
		t.Fatalf("%+v %v", a, err)
	}
	a, err = ParseAttackStop(packet.NewWriter().PackedGUID(wolfGUID).PackedGUID(0x2A).U32(0).Bytes())
	if err != nil || a != (AttackPair{wolfGUID, 0x2A}) {
		t.Fatalf("%+v %v", a, err)
	}
}

// A short body names the packet it came from.
func TestShortActionPacketsNameThePacket(t *testing.T) {
	if _, err := ParseTransferPending(nil); err == nil || !strings.Contains(err.Error(), "SMSG_TRANSFER_PENDING") {
		t.Fatalf("transfer pending: %v", err)
	}
	if _, err := ParseAttackStart([]byte{1}); err == nil || !strings.Contains(err.Error(), "SMSG_ATTACKSTART") {
		t.Fatalf("attack start: %v", err)
	}
	if _, err := ParseAttackStop([]byte{1}); err == nil || !strings.Contains(err.Error(), "SMSG_ATTACKSTOP") {
		t.Fatalf("attack stop: %v", err)
	}
}

func TestBuildStandStateChange(t *testing.T) {
	if got := BuildStandStateChange(0); !bytes.Equal(got, []byte{0, 0, 0, 0}) {
		t.Fatalf("% x", got)
	}
	if got := BuildStandStateChange(1); !bytes.Equal(got, []byte{1, 0, 0, 0}) {
		t.Fatalf("% x", got)
	}
}
