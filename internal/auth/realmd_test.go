package auth

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/TortoiseBots/turtle-agent/internal/packet"
)

type fakeOpts struct {
	challengeCode uint8
	pin           bool
	rejects       *int // proofs to reject before accepting
	realms        []Realm
}

var testRealms = []Realm{{Icon: 1, Name: "Tortoise", Address: "127.0.0.1:8090", Population: 0.5, Characters: 2}}

// serveRealmd plays one realmd connection the way AuthSocket.cpp does.
func serveRealmd(t *testing.T, c net.Conn, f *fakeRealm, o fakeOpts) {
	defer c.Close()
	head := make([]byte, 4)
	if _, err := io.ReadFull(c, head); err != nil {
		return
	}
	body := make([]byte, binary.LittleEndian.Uint16(head[2:4]))
	if _, err := io.ReadFull(c, body); err != nil {
		return
	}
	if string(body[0:4]) != "WoW\x00" || body[4] != 1 || body[5] != 18 || body[6] != 1 ||
		binary.LittleEndian.Uint16(body[7:9]) != ClientBuild ||
		string(body[9:13]) != "68x\x00" || string(body[13:17]) != "niW\x00" {
		t.Errorf("bad challenge fields % X", body[:17])
	}
	if user := string(body[30 : 30+int(body[29])]); user != f.user {
		t.Errorf("user %q, want %q", user, f.user)
	}
	if o.challengeCode != 0 {
		c.Write([]byte{0, 0, o.challengeCode})
		return
	}
	w := packet.NewWriter().U8(0).U8(0).U8(0).Append(bnBytes(f.bigB, 32)).U8(1).U8(7).U8(32).
		Append(bnBytes(srpN, 32)).Append(bnBytes(f.s, 0)).Append(make([]byte, 16))
	if o.pin {
		w.U8(1).U32(1234).Append(make([]byte, 16))
		c.Write(w.Bytes())
		return
	}
	c.Write(w.U8(0).Bytes())
	proof := make([]byte, 75)
	if _, err := io.ReadFull(c, proof); err != nil {
		return
	}
	var a [32]byte
	var m1 [20]byte
	copy(a[:], proof[1:33])
	copy(m1[:], proof[33:53])
	ok, m2, _ := f.verify(a, m1)
	if !ok || (o.rejects != nil && *o.rejects > 0) {
		if o.rejects != nil && *o.rejects > 0 {
			*o.rejects--
		}
		c.Write([]byte{cmdLogonProof, ResultUnknownAccount})
		return
	}
	c.Write(packet.NewWriter().U8(cmdLogonProof).U8(0).Append(m2).U32(0).Bytes())
	req := make([]byte, 5)
	if _, err := io.ReadFull(c, req); err != nil || req[0] != cmdRealmList {
		t.Errorf("realm list request % X err %v", req, err)
		return
	}
	list := packet.NewWriter().U32(0).U8(uint8(len(o.realms)))
	for _, r := range o.realms {
		list.U32(r.Icon).U8(r.Flags).CString(r.Name).CString(r.Address).F32(r.Population).U8(r.Characters).U8(r.Timezone).U8(0)
	}
	list.U16(2)
	c.Write(packet.NewWriter().U8(cmdRealmList).U16(uint16(list.Len())).Append(list.Bytes()).Bytes())
}

// startRealmd serves connections one after another and counts them.
func startRealmd(t *testing.T, f *fakeRealm, o fakeOpts) (addr string, conns *atomic.Int32) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	n := new(atomic.Int32)
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			n.Add(1)
			serveRealmd(t, c, f, o)
		}
	}()
	return ln.Addr().String(), n
}

func ctx5s(t *testing.T) context.Context {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	t.Cleanup(cancel)
	return ctx
}

func TestLoginSuccess(t *testing.T) {
	f := newFakeRealm("harness", "pw", bigFrom(11), bigFrom(12))
	addr, _ := startRealmd(t, f, fakeOpts{realms: testRealms})
	tk, err := Login(ctx5s(t), addr, "Harness", "pw")
	if err != nil {
		t.Fatal(err)
	}
	if tk.Account != "HARNESS" || len(tk.Realms) != 1 || tk.Realms[0].Name != "Tortoise" ||
		tk.Realms[0].ID != 1 || tk.Realms[0].Address != "127.0.0.1:8090" || tk.Realms[0].Characters != 2 {
		t.Fatalf("ticket %s realms %+v", tk, tk.Realms)
	}
	if n := len(tk.KeyBytes()); n != 39 && n != 40 {
		t.Fatalf("key bytes %d", n)
	}
	if !strings.Contains(tk.String(), "redacted") {
		t.Fatalf("String leaks: %s", tk)
	}
}

func TestLoginRetriesOnceAfterProofReject(t *testing.T) {
	f := newFakeRealm("harness", "pw", bigFrom(21), bigFrom(22))
	one := 1
	addr, conns := startRealmd(t, f, fakeOpts{rejects: &one, realms: testRealms})
	if _, err := Login(ctx5s(t), addr, "harness", "pw"); err != nil {
		t.Fatal(err)
	}
	if conns.Load() != 2 {
		t.Fatalf("%d connections, want 2", conns.Load())
	}
}

func TestLoginWrongPassword(t *testing.T) {
	f := newFakeRealm("harness", "right-one", bigFrom(31), bigFrom(32))
	addr, conns := startRealmd(t, f, fakeOpts{realms: testRealms})
	_, err := Login(ctx5s(t), addr, "harness", "wrong-one")
	var ae *AuthError
	if !errors.As(err, &ae) || ae.Stage != "proof" || ae.Code != ResultUnknownAccount {
		t.Fatalf("err = %v", err)
	}
	if strings.Contains(err.Error(), "wrong-one") || conns.Load() != 2 {
		t.Fatalf("err %q after %d connections", err, conns.Load())
	}
}

func TestPINRequired(t *testing.T) {
	f := newFakeRealm("harness", "pw", bigFrom(41), bigFrom(42))
	addr, _ := startRealmd(t, f, fakeOpts{pin: true})
	if _, err := Login(ctx5s(t), addr, "harness", "pw"); !errors.Is(err, ErrPINRequired) {
		t.Fatalf("err = %v", err)
	}
}

func TestChallengeRejected(t *testing.T) {
	f := newFakeRealm("harness", "pw", bigFrom(51), bigFrom(52))
	addr, _ := startRealmd(t, f, fakeOpts{challengeCode: ResultDBBusy})
	_, err := Login(ctx5s(t), addr, "harness", "pw")
	var ae *AuthError
	if !errors.As(err, &ae) || ae.Stage != "challenge" || !strings.Contains(err.Error(), "15 per 300 s") {
		t.Fatalf("err = %v", err)
	}
}

func TestLogonChallengeLayout(t *testing.T) {
	b := BuildLogonChallenge("HARNESS")
	if len(b) != 4+30+7 || binary.LittleEndian.Uint16(b[2:4]) != 37 || b[33] != 7 || string(b[34:]) != "HARNESS" {
		t.Fatalf("challenge % X", b)
	}
}

func TestParseRealmListAndPick(t *testing.T) {
	list := packet.NewWriter().U32(0).U8(2).
		U32(1).U8(realmFlagOffline).CString("Old").CString("10.0.0.1:8085").F32(0).U8(0).U8(1).U8(0).
		U32(1).U8(0).CString("Tortoise").CString("192.0.2.10:8090").F32(1).U8(3).U8(1).U8(0).
		U16(2)
	realms, err := ParseRealmList(list.Bytes())
	if err != nil || len(realms) != 2 || realms[1].ID != 2 || realms[1].Characters != 3 {
		t.Fatalf("%+v %v", realms, err)
	}
	tk := &Ticket{Realms: realms}
	if _, err := tk.Pick(""); err == nil {
		t.Fatal("first realm is offline but Pick accepted it")
	}
	if r, err := tk.Pick("tortoise"); err != nil || r.Address != "192.0.2.10:8090" {
		t.Fatalf("%+v %v", r, err)
	}
}

// scriptedConn replays a canned realmd answer and records what the client sent.
type scriptedConn struct {
	io.Reader
	sent []byte
}

func (s *scriptedConn) Write(p []byte) (int, error) {
	s.sent = append(s.sent, p...)
	return len(p), nil
}

// TestHandshakeSendsUppercasedName pins the T4 ruling: realmd hashes the challenge
// name as sent (AuthSocket.cpp:395, :862) and ComputeProof hashes ToUpper(user).
func TestHandshakeSendsUppercasedName(t *testing.T) {
	rw := &scriptedConn{Reader: strings.NewReader("\x00\x00\x04")}
	_, err := handshake(rw, "Harness", "pw", nil)
	var ae *AuthError
	if !errors.As(err, &ae) || ae.Stage != "challenge" || ae.Code != ResultUnknownAccount {
		t.Fatalf("err = %v", err)
	}
	if len(rw.sent) < 34 || string(rw.sent[34:]) != "HARNESS" {
		t.Fatalf("challenge on the wire % X", rw.sent)
	}
}

// TestChallengeShortSaltIsAClearError: a DB salt with a zero high byte goes out as
// 31 bytes (AuthSocket.cpp:616); the 1.12 client reads 32, so we fail with a hint.
func TestChallengeShortSaltIsAClearError(t *testing.T) {
	f := newFakeRealm("harness", "pw", bigFrom(61), bigFrom(62))
	reply := packet.NewWriter().U8(0).U8(0).U8(0).Append(bnBytes(f.bigB, 32)).U8(1).U8(7).U8(32).
		Append(bnBytes(srpN, 32)).Append(make([]byte, 31)).Append(make([]byte, 16)).U8(0)
	_, err := handshake(&scriptedConn{Reader: bytes.NewReader(reply.Bytes())}, "harness", "pw", nil)
	if err == nil || !strings.Contains(err.Error(), "salt shorter than 32 bytes") {
		t.Fatalf("err = %v", err)
	}
}

func TestTicketFormattingRedacts(t *testing.T) {
	tk := Ticket{Account: "SECRETNAME", Realms: testRealms}
	for i := range tk.Key {
		tk.Key[i] = 0xAB
	}
	for _, verb := range []string{"%v", "%+v", "%#v", "%s", "%d", "%x", "%q"} {
		for _, v := range []any{tk, &tk} {
			out := fmt.Sprintf(verb, v)
			if strings.Contains(out, "SECRETNAME") || strings.Contains(strings.ToLower(out), "abab") ||
				strings.Contains(out, "171") || !strings.Contains(out, "redacted") {
				t.Errorf("%s of %T leaks: %s", verb, v, out)
			}
		}
	}
}

func TestLoginHonoursCancel(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	go func() { // accept and never answer
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			t.Cleanup(func() { c.Close() })
		}
	}()
	ctx, cancel := context.WithCancel(context.Background())
	time.AfterFunc(100*time.Millisecond, cancel)
	start := time.Now()
	_, err = Login(ctx, ln.Addr().String(), "harness", "pw")
	if !errors.Is(err, context.Canceled) || time.Since(start) > 5*time.Second {
		t.Fatalf("err = %v after %s", err, time.Since(start))
	}
}
