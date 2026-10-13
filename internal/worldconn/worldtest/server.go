// Package worldtest is a scripted world server for tests: the server side of
// WorldSocket.cpp's handshake (challenge, digest check, cipher start, auth response)
// and of MangosSocket framing. Tests only; it never listens on a port.
package worldtest

import (
	"bytes"
	"crypto/sha1"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"sync"
	"time"

	"github.com/pjerra/turtle-agent/internal/opcode"
	"github.com/pjerra/turtle-agent/internal/packet"
	"github.com/pjerra/turtle-agent/internal/worldconn"
)

type Options struct {
	Seed    [4]byte            // zero: DE AD BE EF
	Reject  uint8              // non-zero: answer with this code in a plain header
	Queue   bool               // send a 14-byte AUTH_WAIT_QUEUE first
	ShortOK bool               // 1-byte AUTH_OK instead of 10 bytes
	Early   []worldconn.Packet // sent after the cipher starts, before AUTH_OK
}

type Server struct {
	Account    string
	ClientSeed uint32
	Build      uint32
	AddonBlock []byte

	nc      net.Conn
	crypt   *worldconn.HeaderCrypt
	in      chan worldconn.Packet
	writeMu sync.Mutex
	mu      sync.Mutex
	skipped []worldconn.Packet
}

func Accept(nc net.Conn, key []byte, o Options) (*Server, error) {
	s := &Server{nc: nc, in: make(chan worldconn.Packet, 1024)}
	if o.Seed == ([4]byte{}) {
		o.Seed = [4]byte{0xDE, 0xAD, 0xBE, 0xEF}
	}
	if err := s.sendPlain(opcode.SMSGAuthChallenge, o.Seed[:]); err != nil {
		return nil, err
	}
	p, err := s.recvPlain()
	if err != nil {
		return nil, err
	}
	if p.Op != opcode.CMSGAuthSession {
		return nil, fmt.Errorf("worldtest: got %s, want CMSG_AUTH_SESSION", opcode.Name(p.Op))
	}
	r := packet.NewReader(p.Body)
	s.Build = r.U32()
	r.U32()
	s.Account = r.CString()
	s.ClientSeed = r.U32()
	digest := r.Bytes(20)
	s.AddonBlock = r.Bytes(r.Remaining())
	if err := r.Err(); err != nil {
		return nil, err
	}
	want := sha1.New()
	want.Write([]byte(s.Account))
	want.Write([]byte{0, 0, 0, 0})
	want.Write(binary.LittleEndian.AppendUint32(nil, s.ClientSeed))
	want.Write(o.Seed[:])
	want.Write(key)
	if !bytes.Equal(want.Sum(nil), digest) {
		_ = s.sendPlain(opcode.SMSGAuthResponse, []byte{worldconn.AuthFailed})
		return nil, errors.New("worldtest: digest mismatch")
	}
	if o.Reject != 0 {
		return s, s.sendPlain(opcode.SMSGAuthResponse, []byte{o.Reject})
	}
	s.crypt = worldconn.NewHeaderCrypt(key)
	for _, e := range o.Early {
		if err := s.Send(e.Op, e.Body); err != nil {
			return nil, err
		}
	}
	if o.Queue {
		q := packet.NewWriter().U8(worldconn.AuthWaitQueue).U32(0).U8(0).U32(0).U32(3) // World.cpp:471-477
		if err := s.Send(opcode.SMSGAuthResponse, q.Bytes()); err != nil {
			return nil, err
		}
	}
	ok := packet.NewWriter().U8(worldconn.AuthOK)
	if !o.ShortOK {
		ok.U32(0).U8(0).U32(0) // World.cpp:367-371
	}
	if err := s.Send(opcode.SMSGAuthResponse, ok.Bytes()); err != nil {
		return nil, err
	}
	go s.readLoop()
	return s, nil
}

func (s *Server) sendPlain(op uint16, body []byte) error {
	h := binary.BigEndian.AppendUint16(nil, uint16(len(body)+2))
	h = binary.LittleEndian.AppendUint16(h, op)
	_, err := s.nc.Write(append(h, body...))
	return err
}

func (s *Server) recvPlain() (worldconn.Packet, error) {
	h := make([]byte, 6)
	if _, err := io.ReadFull(s.nc, h); err != nil {
		return worldconn.Packet{}, err
	}
	return s.readBody(h)
}

// readBody reads the body a decoded 6-byte client header announces; the server rejects
// sizes below 4 (MangosSocketImpl.h:316).
func (s *Server) readBody(h []byte) (worldconn.Packet, error) {
	size := int(binary.BigEndian.Uint16(h))
	if size < 4 {
		return worldconn.Packet{}, fmt.Errorf("worldtest: client header size %d", size)
	}
	body := make([]byte, size-4)
	_, err := io.ReadFull(s.nc, body)
	return worldconn.Packet{Op: uint16(binary.LittleEndian.Uint32(h[2:])), Body: body}, err
}

// Send writes one server packet with an encrypted 4-byte header (AuthCrypt::EncryptSend).
func (s *Server) Send(op uint16, body []byte) error {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	h := binary.BigEndian.AppendUint16(nil, uint16(len(body)+2))
	h = binary.LittleEndian.AppendUint16(h, op)
	s.crypt.Encrypt(h)
	_, err := s.nc.Write(append(h, body...))
	return err
}

func (s *Server) readLoop() {
	defer close(s.in)
	for {
		h := make([]byte, 6)
		if _, err := io.ReadFull(s.nc, h); err != nil {
			return
		}
		s.crypt.Decrypt(h) // AuthCrypt::DecryptRecv
		p, err := s.readBody(h)
		if err != nil {
			return
		}
		s.in <- p
	}
}

// Expect returns the next client packet with opcode op; packets with other opcodes
// are kept in Skipped.
func (s *Server) Expect(op uint16, timeout time.Duration) (worldconn.Packet, error) {
	deadline := time.After(timeout)
	for {
		select {
		case p, ok := <-s.in:
			if !ok {
				return worldconn.Packet{}, errors.New("worldtest: client connection closed")
			}
			if p.Op == op {
				return p, nil
			}
			s.mu.Lock()
			s.skipped = append(s.skipped, p)
			s.mu.Unlock()
		case <-deadline:
			return worldconn.Packet{}, fmt.Errorf("worldtest: no %s within %s", opcode.Name(op), timeout)
		}
	}
}

// Next returns the next client packet whatever its opcode, in the order it was sent.
func (s *Server) Next(timeout time.Duration) (worldconn.Packet, error) {
	select {
	case p, ok := <-s.in:
		if !ok {
			return worldconn.Packet{}, errors.New("worldtest: client connection closed")
		}
		return p, nil
	case <-time.After(timeout):
		return worldconn.Packet{}, fmt.Errorf("worldtest: no packet within %s", timeout)
	}
}

func (s *Server) Skipped() []worldconn.Packet {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]worldconn.Packet(nil), s.skipped...)
}

func (s *Server) Close() error { return s.nc.Close() }
