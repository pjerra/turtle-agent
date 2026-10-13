package session

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/pjerra/turtle-agent/internal/opcode"
	"github.com/pjerra/turtle-agent/internal/packet"
)

// The create body is CMSG_CHAR_CREATE (WorldSession::HandleCharCreateOpcode in
// CharacterHandler.cpp): name, race, class, gender, six default look bytes,
// Turtle's u32 challengeMask 0.
func TestCreateBodyLayout(t *testing.T) {
	c := CreateSpec{Race: 1, Class: 2, Gender: 1}
	got := c.createBody("Hirequest")
	r := packet.NewReader(got)
	if name := r.CString(); name != "Hirequest" {
		t.Fatalf("name %q", name)
	}
	if race, class, gender := r.U8(), r.U8(), r.U8(); race != 1 || class != 2 || gender != 1 {
		t.Fatalf("race/class/gender %d/%d/%d", race, class, gender)
	}
	for range 6 {
		if v := r.U8(); v != 0 {
			t.Fatalf("look byte %d", v)
		}
	}
	if mask := r.U32(); mask != 0 {
		t.Fatalf("challengeMask %d", mask)
	}
	if err := r.Err(); err != nil || r.Remaining() != 0 {
		t.Fatalf("err %v remaining %d", err, r.Remaining())
	}
}

func TestParseCreateSpec(t *testing.T) {
	c, err := ParseCreateSpec("1,2")
	if err != nil || c != (CreateSpec{Race: 1, Class: 2, Gender: 0}) {
		t.Fatalf("%+v %v", c, err)
	}
	c, err = ParseCreateSpec(" 4 , 7 , 1 ")
	if err != nil || c != (CreateSpec{Race: 4, Class: 7, Gender: 1}) {
		t.Fatalf("%+v %v", c, err)
	}
	c, err = ParseCreateSpec("10,11,1") // Turtle's High Elf, Druid
	if err != nil || c != (CreateSpec{Race: 10, Class: 11, Gender: 1}) {
		t.Fatalf("%+v %v", c, err)
	}
	for _, bad := range []struct{ spec, want string }{
		{"", "want race,class[,gender]"},
		{"1", "want race,class[,gender]"},
		{"1,2,3,4", "want race,class[,gender]"},
		{"human,warrior", "is not a number"},
		{"1,x", "is not a number"},
		{"0,1", "race 0 is not a playable race"},
		{"11,1", "race 11 is not a playable race"},
		{"300,2", "race 300 is not a playable race"},
		{"1,6", "class 6 is not a playable class"},
		{"1,10", "class 10 is not a playable class"},
		{"1,2,2", "gender 2 is not 0 (male) or 1 (female)"},
		{"1,2,256", "gender 256 is not 0 (male) or 1 (female)"},
	} {
		if _, err := ParseCreateSpec(bad.spec); err == nil || !strings.Contains(err.Error(), bad.want) {
			t.Errorf("%q: err = %v, want it to mention %q", bad.spec, err, bad.want)
		}
	}
}

// Every number is accepted or refused as the core's playable sets say: races
// 1-10, classes 1-5, 7-9 and 11, genders 0 and 1.
func TestParseCreateSpecRangesMatchCore(t *testing.T) {
	classes := map[int]bool{1: true, 2: true, 3: true, 4: true, 5: true, 7: true, 8: true, 9: true, 11: true}
	for n := 0; n <= 255; n++ {
		if _, err := ParseCreateSpec(fmt.Sprintf("%d,1", n)); (err == nil) != (n >= 1 && n <= 10) {
			t.Errorf("race %d: err = %v", n, err)
		}
		if _, err := ParseCreateSpec(fmt.Sprintf("1,%d", n)); (err == nil) != classes[n] {
			t.Errorf("class %d: err = %v", n, err)
		}
		if _, err := ParseCreateSpec(fmt.Sprintf("1,1,%d", n)); (err == nil) != (n <= 1) {
			t.Errorf("gender %d: err = %v", n, err)
		}
	}
}

// With --create the session sends CMSG_CHAR_CREATE once for the missing name,
// re-asks the list on success and logs the new character in.
func TestCreateMissingCharacterThenLogin(t *testing.T) {
	spec, err := ParseCreateSpec("1,1")
	if err != nil {
		t.Fatal(err)
	}
	h := start(t, Options{Character: "Hirequest", Create: &spec})
	h.expect(opcode.CMSGCharEnum)
	h.send(opcode.SMSGCharEnum, charEnumBody(Character{GUID: 0x2B, Name: "Alt"}))
	p := h.expect(opcode.CMSGCharCreate)
	r := packet.NewReader(p.Body)
	if name := r.CString(); name != "Hirequest" {
		t.Fatalf("create name %q", name)
	}
	if race, class, gender := r.U8(), r.U8(), r.U8(); race != 1 || class != 1 || gender != 0 {
		t.Fatalf("create race/class/gender %d/%d/%d", race, class, gender)
	}
	h.send(opcode.SMSGCharCreate, []byte{46}) // CHAR_CREATE_SUCCESS
	h.expect(opcode.CMSGCharEnum)
	newChar := Character{GUID: 0x2C, Name: "Hirequest", Race: 1, Class: 1}
	h.send(opcode.SMSGCharEnum, charEnumBody(Character{GUID: 0x2B, Name: "Alt"}, newChar))
	if p := h.expect(opcode.CMSGPlayerLogin); packet.NewReader(p.Body).GUID() != newChar.GUID {
		t.Fatalf("logged in % X", p.Body)
	}
	h.send(opcode.SMSGLoginVerifyWorld, packet.NewWriter().U32(0).F32(0).F32(0).F32(0).F32(0).Bytes())
	select {
	case <-h.s.Ready():
	case <-time.After(2 * time.Second):
		t.Fatal("not ready after the create")
	}
}

// A failed create is fatal and names the cause: no retry, no second create.
func TestCreateFailureEndsSession(t *testing.T) {
	spec, err := ParseCreateSpec("1,1")
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		code uint8
		want string
	}{
		{49, "name already in use"},
		{75, "reserved"},
		{52, "too many characters on this realm"},
		{0, "code 0"},
	} {
		h := start(t, Options{Character: "Hirequest", Create: &spec})
		h.expect(opcode.CMSGCharEnum)
		h.send(opcode.SMSGCharEnum, charEnumBody(Character{GUID: 0x2B, Name: "Alt"}))
		h.expect(opcode.CMSGCharCreate)
		h.send(opcode.SMSGCharCreate, []byte{c.code})
		select {
		case err := <-h.run:
			if err == nil || !strings.Contains(err.Error(), c.want) {
				t.Fatalf("code %d: err = %v", c.code, err)
			}
		case <-time.After(2 * time.Second):
			t.Fatalf("code %d: session did not end", c.code)
		}
		if n := len(h.srv.Skipped()); n != 0 {
			t.Fatalf("code %d: %d unexpected client packets", c.code, n)
		}
	}
}

// An empty SMSG_CHAR_CREATE reply is fatal, not a silent stall.
func TestCreateEmptyReplyEndsSession(t *testing.T) {
	spec, err := ParseCreateSpec("1,1")
	if err != nil {
		t.Fatal(err)
	}
	h := start(t, Options{Character: "Hirequest", Create: &spec})
	h.expect(opcode.CMSGCharEnum)
	h.send(opcode.SMSGCharEnum, charEnumBody(Character{GUID: 0x2B, Name: "Alt"}))
	h.expect(opcode.CMSGCharCreate)
	h.send(opcode.SMSGCharCreate, nil)
	select {
	case err := <-h.run:
		if err == nil || !strings.Contains(err.Error(), "empty reply") {
			t.Fatalf("err = %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("session did not end")
	}
}
