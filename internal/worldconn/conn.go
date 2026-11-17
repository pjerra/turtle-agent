// Copyright (c) 2026 agent-wow
// SPDX-License-Identifier: MIT
// Framing skeleton ported from github.com/agent-wow/agent-wow pkg/worldconn/protocol.go
// and pkg/worldconn/conn.go at 5ef4916 (see NOTICE): io.ReadFull framing, serialized
// writes, Dial-then-authenticate. Header layout, cipher, auth session and auth response
// are rewritten for the 1.12 core in ~/tortoise-wow.

// Package worldconn owns one world-server socket: framing, header cipher and the
// authentication handshake. One goroutine may read while others write.
package worldconn

import (
	"context"
	"crypto/rand"
	"crypto/sha1"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"sync"
	"time"

	"github.com/TortoiseBots/turtle-agent/internal/opcode"
	"github.com/TortoiseBots/turtle-agent/internal/packet"
)

// WorldBuild is the only build the world server accepts (game/Database/DBCStores.cpp:121-129).
const WorldBuild uint32 = 5875

// maxClientPacket is the server's limit on a client packet, opcode included (MangosSocketImpl.h:316).
const maxClientPacket = 10240

// writeTimeout bounds one packet write once the session is up, so a peer that stops
// reading cannot block the session's owner loop (and with it the pong timeout) forever.
var writeTimeout = 10 * time.Second

// ErrWrite wraps a failed socket write. The connection is closed after one: a partial
// frame leaves the stream and the header cipher out of step with the server.
var ErrWrite = errors.New("world: write failed")

// SMSG_AUTH_RESPONSE codes: ResponseCodes ordinals (SharedDefines.h:1629-1666).
const (
	AuthOK              uint8 = 0x0C
	AuthFailed          uint8 = 0x0D
	AuthReject          uint8 = 0x0E
	AuthUnavailable     uint8 = 0x10
	AuthSystemError     uint8 = 0x11
	AuthVersionMismatch uint8 = 0x14
	AuthUnknownAccount  uint8 = 0x15
	AuthSessionExpired  uint8 = 0x17
	AuthServerShutdown  uint8 = 0x18
	AuthWaitQueue       uint8 = 0x1B
	AuthBanned          uint8 = 0x1C
	AuthAlreadyOnline   uint8 = 0x1D
)

type AuthError struct{ Code uint8 }

func (e *AuthError) Error() string {
	text := "rejected"
	switch e.Code {
	case AuthFailed:
		text = "session key mismatch (realmd login stale, or K hashed in the wrong form)"
	case AuthUnavailable:
		text = "server is locked to a higher security level"
	case AuthVersionMismatch:
		text = "build rejected (the world server wants 5875)"
	case AuthUnknownAccount:
		text = "account unknown to the world server"
	case AuthBanned:
		text = "account or IP banned"
	case AuthAlreadyOnline:
		text = "account already online"
	case AuthServerShutdown:
		text = "server shutting down"
	}
	return fmt.Sprintf("world auth: %s (code 0x%02X)", text, e.Code)
}

type Packet struct {
	Op   uint16
	Body []byte
}

type Direction uint8

const (
	ServerToClient Direction = 0
	ClientToServer Direction = 1
)

// Tap sees every packet after decryption (capture mode); it must be safe for
// concurrent use because reads and writes happen on different goroutines.
// CMSG_AUTH_SESSION reaches it with a nil body: that body carries the account name
// and a digest of the session key, which never go into captures.
type Tap interface {
	Packet(dir Direction, op uint16, body []byte)
}

type Conn struct {
	nc        net.Conn
	tap       Tap
	crypt     *HeaderCrypt
	early     []Packet // packets that arrived before SMSG_AUTH_RESPONSE
	writeMu   sync.Mutex
	closeOnce sync.Once
	addons    *Addons // nil: stock list, no stored fingerprint
}

// Dial connects to the world server and authenticates; ctx bounds the handshake only.
func Dial(ctx context.Context, addr, account string, key []byte, tap Tap, opts ...Option) (*Conn, error) {
	var d net.Dialer
	nc, err := d.DialContext(ctx, "tcp", addr)
	if err != nil {
		return nil, fmt.Errorf("world: connect %s: %w", addr, err)
	}
	deadline := time.Now().Add(30 * time.Second)
	if dl, ok := ctx.Deadline(); ok {
		deadline = dl
	}
	_ = nc.SetDeadline(deadline)
	stop := context.AfterFunc(ctx, func() { _ = nc.SetDeadline(time.Unix(1, 0)) })
	c, err := Client(nc, account, key, tap, opts...)
	if !stop() && err == nil {
		err = ctx.Err() // cancelled while the handshake finished: the deadline may be in the past
	}
	if err != nil {
		nc.Close()
		if ctx.Err() != nil && !errors.Is(err, ctx.Err()) {
			err = fmt.Errorf("%w (%w)", err, ctx.Err())
		}
		return nil, err
	}
	_ = nc.SetDeadline(time.Time{})
	return c, nil
}

// Client authenticates over an already open connection (tests pass a net.Pipe).
func Client(nc net.Conn, account string, key []byte, tap Tap, opts ...Option) (*Conn, error) {
	c := &Conn{nc: nc, tap: tap}
	for _, o := range opts {
		o(c)
	}
	if err := c.authenticate(account, key); err != nil {
		return nil, err
	}
	return c, nil
}

func (c *Conn) authenticate(account string, key []byte) error {
	p, err := c.readRaw()
	if err != nil {
		return fmt.Errorf("world: read auth challenge: %w", err)
	}
	if p.Op != opcode.SMSGAuthChallenge || len(p.Body) != 4 {
		return fmt.Errorf("world: expected a 4-byte SMSG_AUTH_CHALLENGE, got %s with %d bytes", opcode.Name(p.Op), len(p.Body))
	}
	var serverSeed, cs [4]byte
	copy(serverSeed[:], p.Body)
	if _, err := rand.Read(cs[:]); err != nil {
		return err
	}
	body := buildAuthSession(account, binary.LittleEndian.Uint32(cs[:]), serverSeed, key, c.addons)
	if err := c.write(opcode.CMSGAuthSession, body, nil); err != nil {
		return fmt.Errorf("world: send auth session: %w", err)
	}
	c.crypt = NewHeaderCrypt(key) // the server starts its cipher after the digest check (WorldSocket.cpp:341-342)
	return c.readAuthResponse()
}

// BuildAuthSession lays out CMSG_AUTH_SESSION (WorldSocket.cpp:144-149, :287-299).
func BuildAuthSession(account string, clientSeed uint32, serverSeed [4]byte, key []byte) []byte {
	return buildAuthSession(account, clientSeed, serverSeed, key, nil)
}

func buildAuthSession(account string, clientSeed uint32, serverSeed [4]byte, key []byte, addons *Addons) []byte {
	d := sha1.New()
	d.Write([]byte(account))
	d.Write([]byte{0, 0, 0, 0})
	d.Write(binary.LittleEndian.AppendUint32(nil, clientSeed))
	d.Write(serverSeed[:])
	d.Write(key)
	return packet.NewWriter().U32(WorldBuild).U32(0).CString(account).U32(clientSeed).
		Append(d.Sum(nil)).Append(addons.Block()).Bytes()
}

// readAuthResponse waits for SMSG_AUTH_RESPONSE. A rejection is sent before the server
// starts its cipher, so its header is plain (WorldSocket.cpp:158-308); success and queue
// updates are encrypted. Other packets that arrive first (SMSG_ADDON_INFO is sent right
// after the session is queued, WorldSocket.cpp:380-390) are kept for ReadPacket.
// The plain rejection header is checked before the trial decryption: an encrypted
// header equals 00 03 EE 01 one time in 2^32, while a plain one decrypts to opcode
// SMSG_AUTH_RESPONSE for one key in 2^16.
func (c *Conn) readAuthResponse() error {
	for range 1000 {
		var h [4]byte
		if _, err := io.ReadFull(c.nc, h[:]); err != nil {
			return fmt.Errorf("world: read auth response: %w", err)
		}
		if plainSize(h) == 2 && plainOp(h) == 0 { // AUTH_UNAVAILABLE sends an empty packet (WorldSocket.cpp:275-283)
			return &AuthError{Code: AuthUnavailable}
		}
		trial := *c.crypt
		dec := h
		trial.Decrypt(dec[:])
		size, op := plainSize(dec), plainOp(dec)
		switch {
		case plainSize(h) == 3 && plainOp(h) == opcode.SMSGAuthResponse:
			size, op = 3, opcode.SMSGAuthResponse
		case op < opcode.NumMsgTypes:
			*c.crypt = trial
		default:
			return fmt.Errorf("world: undecodable header % X after the auth session", h)
		}
		if size < 2 {
			return fmt.Errorf("world: bad header size %d", size)
		}
		body := make([]byte, size-2)
		if _, err := io.ReadFull(c.nc, body); err != nil {
			return fmt.Errorf("world: read auth response: %w", err)
		}
		if c.tap != nil {
			c.tap.Packet(ServerToClient, op, body)
		}
		c.addons.observe(op, body)
		if op != opcode.SMSGAuthResponse {
			c.early = append(c.early, Packet{Op: op, Body: body})
			continue
		}
		if len(body) == 0 {
			return errors.New("world: empty SMSG_AUTH_RESPONSE")
		}
		switch body[0] {
		case AuthOK: // 10 bytes (World.cpp:367-371) or 1 byte after a queue (WorldSession.cpp:949-952)
			return nil
		case AuthWaitQueue: // 14 bytes (World.cpp:471-477) or 5 (WorldSession.cpp:955-958); keep waiting
			continue
		default:
			return &AuthError{Code: body[0]}
		}
	}
	return errors.New("world: no SMSG_AUTH_RESPONSE within 1000 packets")
}

func plainSize(h [4]byte) int  { return int(binary.BigEndian.Uint16(h[0:2])) }
func plainOp(h [4]byte) uint16 { return binary.LittleEndian.Uint16(h[2:4]) }

// ReadPacket returns the next server packet. Only one goroutine may read.
func (c *Conn) ReadPacket() (Packet, error) {
	if len(c.early) > 0 {
		p := c.early[0]
		c.early = c.early[1:]
		return p, nil
	}
	return c.readRaw()
}

// readRaw reads one server packet: u16 big-endian size (opcode included), u16 opcode
// (MangosSocket.h:33-37, MangosSocketImpl.h:504-513).
func (c *Conn) readRaw() (Packet, error) {
	var h [4]byte
	if _, err := io.ReadFull(c.nc, h[:]); err != nil {
		return Packet{}, err
	}
	if c.crypt != nil {
		c.crypt.Decrypt(h[:])
	}
	size, op := plainSize(h), plainOp(h)
	if size < 2 {
		return Packet{}, fmt.Errorf("world: bad header size %d", size)
	}
	body := make([]byte, size-2)
	if _, err := io.ReadFull(c.nc, body); err != nil {
		return Packet{}, err
	}
	if c.tap != nil {
		c.tap.Packet(ServerToClient, op, body)
	}
	c.addons.observe(op, body)
	return Packet{Op: op, Body: body}, nil
}

// WritePacket frames one client packet: u16 big-endian size (body + 4), u32 opcode,
// first 6 bytes encrypted (MangosSocket.h:39-43, MangosSocketImpl.h:307-331).
func (c *Conn) WritePacket(op uint16, body []byte) error {
	return c.write(op, body, body)
}

// write sends op with body and shows the tap tapBody (nil for CMSG_AUTH_SESSION).
func (c *Conn) write(op uint16, body, tapBody []byte) error {
	if op >= opcode.NumMsgTypes {
		return fmt.Errorf("world: opcode 0x%03X is at or past NUM_MSG_TYPES; the server would close the socket (WorldSocket.cpp:55-59)", op)
	}
	if len(body)+4 > maxClientPacket {
		return fmt.Errorf("world: %s body is %d bytes; the server limit is %d", opcode.Name(op), len(body), maxClientPacket-4)
	}
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	buf := make([]byte, 6, 6+len(body))
	binary.BigEndian.PutUint16(buf[0:2], uint16(len(body)+4))
	binary.LittleEndian.PutUint32(buf[2:6], uint32(op))
	if c.crypt != nil {
		c.crypt.Encrypt(buf[:6])
	}
	buf = append(buf, body...)
	if c.tap != nil {
		c.tap.Packet(ClientToServer, op, tapBody)
	}
	if c.crypt != nil { // the handshake runs under Dial's own deadline
		_ = c.nc.SetWriteDeadline(time.Now().Add(writeTimeout))
	}
	if _, err := c.nc.Write(buf); err != nil {
		c.Close()
		return fmt.Errorf("%w: %s: %w", ErrWrite, opcode.Name(op), err)
	}
	return nil
}

func (c *Conn) Close() error {
	var err error
	c.closeOnce.Do(func() { err = c.nc.Close() })
	return err
}

// NewOffline returns a Conn that has no server: writes are accepted and dropped, reads
// report EOF. A replay of a capture uses it to run the session's handlers, which answer
// packets, without a socket.
func NewOffline() *Conn { return &Conn{nc: offlineConn{}} }

type offlineConn struct{}

func (offlineConn) Read([]byte) (int, error)         { return 0, io.EOF }
func (offlineConn) Write(b []byte) (int, error)      { return len(b), nil }
func (offlineConn) Close() error                     { return nil }
func (offlineConn) LocalAddr() net.Addr              { return offlineAddr{} }
func (offlineConn) RemoteAddr() net.Addr             { return offlineAddr{} }
func (offlineConn) SetDeadline(time.Time) error      { return nil }
func (offlineConn) SetReadDeadline(time.Time) error  { return nil }
func (offlineConn) SetWriteDeadline(time.Time) error { return nil }

type offlineAddr struct{}

func (offlineAddr) Network() string { return "offline" }
func (offlineAddr) String() string  { return "offline" }
