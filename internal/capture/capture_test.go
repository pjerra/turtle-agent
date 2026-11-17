package capture

import (
	"bytes"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/TortoiseBots/turtle-agent/internal/opcode"
	"github.com/TortoiseBots/turtle-agent/internal/packet"
	"github.com/TortoiseBots/turtle-agent/internal/worldconn"
)

func TestRoundTripAndMode(t *testing.T) {
	path := filepath.Join(t.TempDir(), "c.twcap")
	w, err := Create(path)
	if err != nil {
		t.Fatal(err)
	}
	w.Packet(worldconn.ServerToClient, opcode.SMSGCharEnum, []byte{1, 2, 3})
	w.Packet(worldconn.ClientToServer, opcode.CMSGPing, []byte{4})
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	if info, _ := os.Stat(path); info.Mode().Perm() != 0o600 {
		t.Fatalf("mode %v", info.Mode())
	}
	recs, err := ReadFile(path)
	if err != nil || len(recs) != 2 {
		t.Fatalf("%v %v", recs, err)
	}
	if recs[0].Op != opcode.SMSGCharEnum || recs[0].Dir != worldconn.ServerToClient || !bytes.Equal(recs[0].Body, []byte{1, 2, 3}) ||
		recs[1].Dir != worldconn.ClientToServer || recs[1].Redacted {
		t.Fatalf("%+v", recs)
	}
	if got := Bodies(recs, opcode.SMSGCharEnum); len(got) != 1 {
		t.Fatalf("Bodies %v", got)
	}
	if Stats(recs)["SMSG_CHAR_ENUM"] != 1 {
		t.Fatalf("stats %v", Stats(recs))
	}
}

func TestNeverStoresAuthSessionOrWarden(t *testing.T) {
	path := filepath.Join(t.TempDir(), "c.twcap")
	w, _ := Create(path)
	w.Packet(worldconn.ClientToServer, opcode.CMSGAuthSession, []byte("HARNESS\x00digest"))
	w.Packet(worldconn.ServerToClient, opcode.SMSGWardenData, []byte{9, 9, 9})
	w.Close()
	recs, _ := ReadFile(path)
	for _, r := range recs {
		if !r.Redacted || len(r.Body) != 0 {
			t.Fatalf("%s stored: %+v", opcode.Name(r.Op), r)
		}
	}
}

func TestSecretsRedacted(t *testing.T) {
	key := bytes.Repeat([]byte{0xAB, 0xCD}, 20)
	path := filepath.Join(t.TempDir(), "c.twcap")
	w, _ := Create(path, []byte("HARNESS"), []byte("pw-secret"), key)
	w.Packet(worldconn.ServerToClient, opcode.SMSGMessagechat, []byte("say pw-secret now"))
	w.Packet(worldconn.ServerToClient, opcode.SMSGMessagechat, append([]byte{1}, key...))
	w.Packet(worldconn.ServerToClient, opcode.SMSGNameQueryResponse, []byte("Harnessb\x00"))
	if n, red := w.Counts(); n != 3 || red != 2 {
		t.Fatalf("counts %d %d", n, red)
	}
	w.Close()
	recs, _ := ReadFile(path)
	if !recs[0].Redacted || !recs[1].Redacted || recs[2].Redacted {
		t.Fatalf("%+v", recs)
	}
	if f, err := Scan(recs, []byte("Harnessb")); err != nil || len(f) != 1 || f[0].Index != 2 {
		t.Fatalf("scan %v", f)
	}
}

func TestCreateRefusesOverwrite(t *testing.T) {
	path := filepath.Join(t.TempDir(), "c.twcap")
	w, _ := Create(path)
	w.Close()
	if _, err := Create(path); err == nil {
		t.Fatal("overwrote an existing capture")
	}
}

func TestReadFileRejectsGarbage(t *testing.T) {
	path := filepath.Join(t.TempDir(), "bad")
	os.WriteFile(path, []byte("TWCAP1\n\x00\x01"), 0o600)
	if _, err := ReadFile(path); err == nil {
		t.Fatal("truncated record accepted")
	}
}

func TestCreateRejectsShortSecret(t *testing.T) {
	path := filepath.Join(t.TempDir(), "c.twcap")
	if _, err := Create(path, []byte("ab")); err == nil || !strings.Contains(err.Error(), "secret too short to redact safely") {
		t.Fatalf("err %v", err)
	}
	if _, err := os.Stat(path); err == nil {
		t.Fatal("file left behind")
	}
}

func TestScanRejectsShortNeedle(t *testing.T) {
	for _, n := range [][]byte{nil, {}, []byte("ab")} {
		if _, err := Scan([]Record{{Body: []byte("abc")}}, n); err == nil {
			t.Fatalf("needle %q accepted", n)
		}
	}
}

func TestVariantsDeduplicated(t *testing.T) {
	v := Variants([]byte("123"))
	for i := range v {
		for j := i + 1; j < len(v); j++ {
			if bytes.Equal(v[i], v[j]) {
				t.Fatalf("duplicate %q", v[i])
			}
		}
	}
	if len(v) != 4 { // raw, reversed, hex, hex reversed
		t.Fatalf("variants %q", v)
	}
}

func TestScanFindsEveryForm(t *testing.T) {
	key := make([]byte, 39)
	for i := range key {
		key[i] = byte(i*7 + 1)
	}
	rev := make([]byte, len(key))
	for i, b := range key {
		rev[len(key)-1-i] = b
	}
	key40 := append(append([]byte{}, key...), 0)
	cases := map[string][]byte{
		"raw key":       append([]byte{9}, key...),
		"reversed key":  rev,
		"lower hex key": []byte(hex.EncodeToString(key)),
		"upper hex key": []byte(strings.ToUpper(hex.EncodeToString(key))),
		"39 in 40":      key40,
		"account lower": []byte("say harness"),
		"account upper": []byte("HARNESS says"),
		"password":      []byte("pw: hunter2x"),
	}
	needles := [][]byte{key, []byte("Harness"), []byte("hunter2x")}
	for name, body := range cases {
		f, err := Scan([]Record{{Body: body}}, needles...)
		if err != nil || len(f) == 0 {
			t.Errorf("%s not found: %v %v", name, f, err)
		}
	}
	if f, _ := Scan([]Record{{Body: []byte("Harnessb nothing")}}, []byte("xyzzy")); len(f) != 0 {
		t.Fatalf("false positive %v", f)
	}
}

func TestCreateRedactsHexAndLowerCase(t *testing.T) {
	key := bytes.Repeat([]byte{0x12, 0xAB}, 20)
	path := filepath.Join(t.TempDir(), "c.twcap")
	w, err := Create(path, []byte("HARNESS"), key)
	if err != nil {
		t.Fatal(err)
	}
	w.Packet(worldconn.ServerToClient, opcode.SMSGMessagechat, []byte(hex.EncodeToString(key)))
	w.Packet(worldconn.ServerToClient, opcode.SMSGMessagechat, []byte("hi harness"))
	w.Close()
	recs, _ := ReadFile(path)
	if !recs[0].Redacted || !recs[1].Redacted {
		t.Fatalf("%+v", recs)
	}
}

func TestPacketAfterCloseIsReported(t *testing.T) {
	w, _ := Create(filepath.Join(t.TempDir(), "c.twcap"))
	w.Close()
	w.Packet(worldconn.ServerToClient, opcode.SMSGCharEnum, []byte{1})
	if err := w.Close(); err == nil {
		t.Fatal("packet after Close went unnoticed")
	}
}

// A capture whose writer died mid-record: ReadFile refuses it, ReadFilePartial returns what
// came before the cut together with the error.
func TestReadFilePartialKeepsRecordsBeforeTheCut(t *testing.T) {
	path := filepath.Join(t.TempDir(), "c.twcap")
	w, err := Create(path)
	if err != nil {
		t.Fatal(err)
	}
	w.Packet(worldconn.ServerToClient, opcode.SMSGCharEnum, []byte{1, 2, 3})
	w.Packet(worldconn.ServerToClient, opcode.SMSGPong, []byte{4, 5, 6, 7})
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	raw, _ := os.ReadFile(path)
	if err := os.WriteFile(path, raw[:len(raw)-2], 0o600); err != nil {
		t.Fatal(err)
	}
	if recs, err := ReadFile(path); err == nil || recs != nil {
		t.Fatalf("ReadFile: %v %v", recs, err)
	}
	recs, err := ReadFilePartial(path)
	if err == nil || len(recs) != 1 || recs[0].Op != opcode.SMSGCharEnum {
		t.Fatalf("%+v %v", recs, err)
	}
}

// A record the writer cannot have written is corruption, not a cut: the records before it are
// kept and the error says where.
func TestReadFilePartialRejectsCorruptRecords(t *testing.T) {
	rec := func(flags uint8, ts int64, op uint16) []byte {
		return packet.NewWriter().U8(flags).U64(uint64(ts)).U16(op).U32(1).U8(9).Bytes()
	}
	base := time.Date(2026, 10, 12, 20, 0, 0, 0, time.UTC).UnixNano()
	good := rec(0, base, opcode.SMSGPong)
	for _, tc := range []struct {
		name string
		bad  []byte
	}{
		{"direction", rec(2, base, opcode.SMSGPong)},
		{"opcode", rec(0, base, opcode.NumMsgTypes)},
		{"time runs back", rec(0, base-int64(2*time.Minute), opcode.SMSGPong)},
	} {
		path := filepath.Join(t.TempDir(), "c.twcap")
		raw := append(append(append([]byte(magic), good...), tc.bad...), good...)
		if err := os.WriteFile(path, raw, 0o600); err != nil {
			t.Fatal(err)
		}
		recs, err := ReadFilePartial(path)
		if len(recs) != 1 || err == nil || !strings.Contains(err.Error(), "corrupt at record 1") {
			t.Errorf("%s: %d records, %v", tc.name, len(recs), err)
		}
	}
	// A redacted record (bit 0x80 over a valid direction) and a small step back are fine.
	path := filepath.Join(t.TempDir(), "c.twcap")
	raw := append(append(append([]byte(magic), good...), rec(0x81, base-int64(time.Second), opcode.CMSGPing)...), good...)
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	if recs, err := ReadFilePartial(path); err != nil || len(recs) != 3 || !recs[1].Redacted {
		t.Fatalf("%d records, %v", len(recs), err)
	}
}
