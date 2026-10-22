package eluna

import (
	"encoding/json"
	"testing"
)

func TestParseLine(t *testing.T) {
	line := `TTJSON {"v":1,"cmd":"q.units","id":"a1","ok":true,"ts":17,"read":2,"final":true,"n":0}`
	r, ok := ParseLine(line)
	if !ok {
		t.Fatalf("not parsed: %s", line)
	}
	if r.V != 1 || r.Cmd != "q.units" || r.ID != "a1" || !r.OK || r.Read != 2 || !r.Final {
		t.Fatalf("reply %+v", r)
	}
	if string(r.Body["cmd"]) != `"q.units"` || string(r.Body["n"]) != "0" {
		t.Fatalf("body %v", r.Body)
	}

	// An error answer keeps its code and message.
	r, ok = ParseLine(`TTJSON {"v":1,"cmd":"q.unit","id":"b2","ok":false,"err":"not_found","msg":"no unit 0xF130000000000001"}`)
	if !ok || r.OK || r.Err != "not_found" || r.Msg != "no unit 0xF130000000000001" {
		t.Fatalf("%+v %v", r, ok)
	}

	for _, bad := range []string{
		"",
		"TTJSON",
		"TTJSON ",
		"TTJSON {",
		`TTJSON {"v":1,"id":"a1"`,
		`TTJSON {"v":1} {"v":1}`,
		`TTJSON [1,2]`,
		`TTJSON null`,
		`TTJSON "text"`,
		`[guild] TTJSON {"v":1,"id":"a1"}`,
		`ttjson {"v":1,"id":"a1"}`,
		`hello`,
	} {
		if _, ok := ParseLine(bad); ok {
			t.Errorf("parsed %q", bad)
		}
	}
}

func TestReplyDone(t *testing.T) {
	for _, c := range []struct {
		r    Reply
		done bool
	}{
		{Reply{OK: true}, true},               // one-shot commands carry no read
		{Reply{Read: 1, Final: false}, false}, // the first read of quiet/place
		{Reply{Read: 2, Final: true}, true},   // the second read
		{Reply{Read: 1, Final: true}, true},   // a short wait: the first read is the last
		{Reply{Read: 0, Final: false}, true},  // no second read announced
		{Reply{Read: 3, Final: true}, true},   //
		{Reply{Read: 2, Final: false}, false}, //
		{Reply{Read: 1, Final: false, OK: true}, false},
	} {
		if got := c.r.Done(); got != c.done {
			t.Errorf("%+v: done %v", c.r, got)
		}
	}
}

func TestHexGUID(t *testing.T) {
	const want = "0xF130000E450000FF"
	if got := HexGUID(0xF130000E450000FF); got != want {
		t.Fatalf("HexGUID %q", got)
	}
	if got := HexGUID(0); got != "0x0000000000000000" {
		t.Fatalf("HexGUID(0) %q", got)
	}
	for _, s := range []string{want, "0xf130000e450000ff", "F130000E450000FF", "0xF130000E450000FF"} {
		g, err := ParseHexGUID(s)
		if err != nil || g != 0xF130000E450000FF {
			t.Fatalf("ParseHexGUID(%q) %X %v", s, g, err)
		}
	}
	if g, err := ParseHexGUID("0x0000000000000000"); err != nil || g != 0 {
		t.Fatalf("zero: %X %v", g, err)
	}
	for _, s := range []string{"", "0x", "xyz", "0x1FFFFFFFFFFFFFFFF", "0x12 34"} {
		if _, err := ParseHexGUID(s); err == nil {
			t.Fatalf("ParseHexGUID(%q) accepted", s)
		}
	}
}

// The named fields come from the body, and the body keeps every field (a caller reads the ones this client does not name).
func TestReplyBodyIsTheWholeObject(t *testing.T) {
	r, ok := ParseLine(`TTJSON {"v":1,"cmd":"q.floor","id":"x","ok":true,"floor":91.67,"top":null,"multi":false}`)
	if !ok {
		t.Fatal("not parsed")
	}
	var f float64
	if err := json.Unmarshal(r.Body["floor"], &f); err != nil || f != 91.67 {
		t.Fatalf("floor: %v %v", f, err)
	}
}
