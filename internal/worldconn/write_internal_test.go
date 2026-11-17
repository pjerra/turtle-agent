package worldconn

import (
	"bytes"
	"errors"
	"net"
	"os"
	"testing"
	"time"

	"github.com/TortoiseBots/turtle-agent/internal/opcode"
)

// Review M2: a peer that stops reading cannot block a write past writeTimeout; the failed
// write closes the connection and wraps ErrWrite (review M1: the RPC maps it to -32002).
func TestWriteDeadlineClosesTheConnection(t *testing.T) {
	old := writeTimeout
	writeTimeout = 100 * time.Millisecond
	t.Cleanup(func() { writeTimeout = old })
	cli, peer := net.Pipe() // peer never reads
	t.Cleanup(func() { cli.Close(); peer.Close() })
	c := &Conn{nc: cli, crypt: NewHeaderCrypt(bytes.Repeat([]byte{7}, 40))}
	start := time.Now()
	err := c.WritePacket(opcode.CMSGPing, make([]byte, 8))
	if !errors.Is(err, ErrWrite) || !errors.Is(err, os.ErrDeadlineExceeded) || time.Since(start) > 2*time.Second {
		t.Fatalf("err %v after %s", err, time.Since(start))
	}
	if err := c.WritePacket(opcode.CMSGPing, make([]byte, 8)); !errors.Is(err, ErrWrite) {
		t.Fatalf("second write: %v", err)
	}
	if _, err := cli.Read(make([]byte, 1)); err == nil {
		t.Fatal("connection still open after a failed write")
	}
}
