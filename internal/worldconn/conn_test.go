package worldconn_test

import (
	"bytes"
	"compress/zlib"
	"context"
	"crypto/sha1"
	"encoding/binary"
	"errors"
	"io"
	"net"
	"sync"
	"testing"
	"time"

	"github.com/TortoiseBots/turtle-agent/internal/opcode"
	"github.com/TortoiseBots/turtle-agent/internal/packet"
	"github.com/TortoiseBots/turtle-agent/internal/worldconn"
	"github.com/TortoiseBots/turtle-agent/internal/worldconn/worldtest"
)

var key40 = bytes.Repeat([]byte{0x11, 0x22, 0x33, 0x44}, 10)

func pipeAuth(t *testing.T, key []byte, o worldtest.Options) (*worldconn.Conn, *worldtest.Server, error) {
	t.Helper()
	return pipeAuthTap(t, key, o, nil)
}

func pipeAuthTap(t *testing.T, key []byte, o worldtest.Options, tap worldconn.Tap) (*worldconn.Conn, *worldtest.Server, error) {
	t.Helper()
	cli, srv := net.Pipe()
	t.Cleanup(func() { cli.Close(); srv.Close() })
	type res struct {
		s   *worldtest.Server
		err error
	}
	ch := make(chan res, 1)
	go func() { s, err := worldtest.Accept(srv, key, o); ch <- res{s, err} }()
	c, err := worldconn.Client(cli, "HARNESS", key, tap)
	if err != nil {
		cli.Close() // a client that stopped mid-packet must not leave the server blocked in Write
	}
	r := <-ch
	if r.err != nil {
		t.Fatalf("server: %v", r.err)
	}
	return c, r.s, err
}

func TestAuthSessionLayout(t *testing.T) {
	seed := [4]byte{9, 8, 7, 6}
	body := worldconn.BuildAuthSession("HARNESS", 0x01020304, seed, key40)
	r := packet.NewReader(body)
	if r.U32() != worldconn.WorldBuild || r.U32() != 0 || r.CString() != "HARNESS" || r.U32() != 0x01020304 {
		t.Fatal("header fields wrong")
	}
	h := sha1.New()
	h.Write([]byte("HARNESS"))
	h.Write([]byte{0, 0, 0, 0})
	h.Write([]byte{4, 3, 2, 1})
	h.Write(seed[:])
	h.Write(key40)
	if !bytes.Equal(r.Bytes(20), h.Sum(nil)) {
		t.Fatal("digest differs from WorldSocket.cpp:287-299")
	}
	size := r.U32()
	zr, err := zlib.NewReader(bytes.NewReader(r.Bytes(r.Remaining())))
	if err != nil {
		t.Fatal(err)
	}
	plain, _ := io.ReadAll(zr)
	if uint32(len(plain)) != size {
		t.Fatalf("declared %d bytes, inflated %d", size, len(plain))
	}
	pr := packet.NewReader(plain)
	var names []string
	for pr.Remaining() > 0 {
		names = append(names, pr.CString())
		if pr.U8() != 0x01 || pr.U32() != 0x4C1C776D || pr.U32() != 0 {
			t.Fatal("addon entry fields wrong")
		}
	}
	if len(names) != 11 || names[0] != "Blizzard_AuctionUI" || names[10] != "Blizzard_TrainerUI" || pr.Err() != nil {
		t.Fatalf("addons %v err %v", names, pr.Err())
	}
}

func TestClientAuthenticatesAndFrames(t *testing.T) {
	early := []worldconn.Packet{{Op: opcode.SMSGAddonInfo, Body: []byte{2, 1, 0, 0, 0, 0, 0, 0}}}
	c, srv, err := pipeAuth(t, key40, worldtest.Options{Early: early})
	if err != nil {
		t.Fatal(err)
	}
	if srv.Build != 5875 || srv.Account != "HARNESS" || len(srv.AddonBlock) < 8 {
		t.Fatalf("server saw build %d account %q addon %d bytes", srv.Build, srv.Account, len(srv.AddonBlock))
	}
	p, err := c.ReadPacket()
	if err != nil || p.Op != opcode.SMSGAddonInfo {
		t.Fatalf("first packet %s %v", opcode.Name(p.Op), err)
	}
	go srv.Send(opcode.SMSGPong, []byte{7, 0, 0, 0})
	if p, err = c.ReadPacket(); err != nil || p.Op != opcode.SMSGPong || binary.LittleEndian.Uint32(p.Body) != 7 {
		t.Fatalf("pong %v %v", p, err)
	}
	if err := c.WritePacket(opcode.CMSGPing, []byte{1, 0, 0, 0, 0, 0, 0, 0}); err != nil {
		t.Fatal(err)
	}
	if p, err := srv.Expect(opcode.CMSGPing, time.Second); err != nil || p.Body[0] != 1 {
		t.Fatalf("ping %v %v", p, err)
	}
}

func TestQueueThenShortOK(t *testing.T) {
	if _, _, err := pipeAuth(t, key40, worldtest.Options{Queue: true, ShortOK: true}); err != nil {
		t.Fatal(err)
	}
}

func TestPlainTextRejection(t *testing.T) {
	_, _, err := pipeAuth(t, key40, worldtest.Options{Reject: worldconn.AuthUnknownAccount})
	var ae *worldconn.AuthError
	if !errors.As(err, &ae) || ae.Code != worldconn.AuthUnknownAccount {
		t.Fatalf("err = %v", err)
	}
}

// Review Focus 1: K with a zero top byte is 39 bytes on the server; digest and cipher use that.
func TestAuthWith39ByteKey(t *testing.T) {
	key := key40[:39]
	c, srv, err := pipeAuth(t, key, worldtest.Options{})
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 50; i++ {
		if err := c.WritePacket(opcode.CMSGPing, []byte{byte(i), 0, 0, 0, 0, 0, 0, 0}); err != nil {
			t.Fatal(err)
		}
		if p, err := srv.Expect(opcode.CMSGPing, time.Second); err != nil || p.Body[0] != byte(i) {
			t.Fatalf("ping %d: %v %v", i, p, err)
		}
	}
}

func TestWriteRefusesBadOpcodeAndSize(t *testing.T) {
	c, _, err := pipeAuth(t, key40, worldtest.Options{})
	if err != nil {
		t.Fatal(err)
	}
	if err := c.WritePacket(opcode.NumMsgTypes, nil); err == nil {
		t.Fatal("opcode NUM_MSG_TYPES accepted")
	}
	if err := c.WritePacket(opcode.CMSGMessagechat, make([]byte, 10237)); err == nil {
		t.Fatal("oversized packet accepted")
	}
}

type recordTap struct {
	mu   sync.Mutex
	seen []worldconn.Packet
	dirs []worldconn.Direction
}

func (r *recordTap) Packet(dir worldconn.Direction, op uint16, body []byte) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.seen = append(r.seen, worldconn.Packet{Op: op, Body: bytes.Clone(body)})
	r.dirs = append(r.dirs, dir)
}

// Ruling P10 / T5 minor 2: the account name never reaches a capture; CMSG_AUTH_SESSION
// is tapped with its body withheld.
func TestTapSeesHandshakeWithoutAccount(t *testing.T) {
	tap := &recordTap{}
	if _, _, err := pipeAuthTap(t, key40, worldtest.Options{}, tap); err != nil {
		t.Fatal(err)
	}
	tap.mu.Lock()
	defer tap.mu.Unlock()
	var ops []uint16
	for i, p := range tap.seen {
		ops = append(ops, p.Op)
		if bytes.Contains(p.Body, []byte("HARNESS")) {
			t.Fatalf("tap saw the account in %s", opcode.Name(p.Op))
		}
		if p.Op == opcode.CMSGAuthSession && (len(p.Body) != 0 || tap.dirs[i] != worldconn.ClientToServer) {
			t.Fatalf("CMSG_AUTH_SESSION tapped with %d body bytes, dir %d", len(p.Body), tap.dirs[i])
		}
	}
	want := []uint16{opcode.SMSGAuthChallenge, opcode.CMSGAuthSession, opcode.SMSGAuthResponse}
	if len(ops) != len(want) || ops[0] != want[0] || ops[1] != want[1] || ops[2] != want[2] {
		t.Fatalf("tapped ops %v", ops)
	}
}

// A cancelled ctx aborts a handshake whose server never answers (same contract as auth.Login).
func TestDialHonoursCancel(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	go func() {
		if nc, err := ln.Accept(); err == nil {
			defer nc.Close()
			_, _ = io.Copy(io.Discard, nc) // never sends SMSG_AUTH_CHALLENGE
		}
	}()
	ctx, cancel := context.WithCancel(context.Background())
	time.AfterFunc(50*time.Millisecond, cancel)
	start := time.Now()
	_, err = worldconn.Dial(ctx, ln.Addr().String(), "HARNESS", key40, nil)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
	if d := time.Since(start); d > 5*time.Second {
		t.Fatalf("Dial took %s after cancel", d)
	}
}

// A key whose first trial decryption turns the plain rejection header 00 03 EE 01 into
// size 2, opcode SMSG_AUTH_RESPONSE: the plain header must win (1 key in 2^16 does this).
func TestPlainRejectionWithCollidingKey(t *testing.T) {
	key := append([]byte{0x00, 0x01, 0x05, 0x12}, key40[4:]...)
	_, _, err := pipeAuth(t, key, worldtest.Options{Reject: worldconn.AuthBanned})
	var ae *worldconn.AuthError
	if !errors.As(err, &ae) || ae.Code != worldconn.AuthBanned {
		t.Fatalf("err = %v", err)
	}
}
