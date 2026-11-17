package auth

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"strings"
	"time"

	"github.com/TortoiseBots/turtle-agent/internal/packet"
)

// ClientBuild is the only build realmd knows (RealmList.cpp:39); realms show offline
// for any other build (AuthSocket.cpp:1249, :1266-1268).
const ClientBuild uint16 = 7272

const (
	cmdLogonChallenge = 0x00 // AuthCodes.h:31
	cmdLogonProof     = 0x01 // AuthCodes.h:32
	cmdRealmList      = 0x10 // AuthCodes.h:35
	realmFlagOffline  = 0x02 // REALM_FLAG_OFFLINE, shared/Common.h:200
)

// Result codes (AuthCodes.h:60-92).
const (
	ResultSuccess           uint8 = 0x00
	ResultBanned            uint8 = 0x03
	ResultUnknownAccount    uint8 = 0x04 // also the wrong-password answer (AuthSocket.cpp:1016-1017)
	ResultIncorrectPassword uint8 = 0x05 // realmd sends it for an inactive account (AuthSocket.cpp:515)
	ResultAlreadyOnline     uint8 = 0x06
	ResultNoTime            uint8 = 0x07
	ResultDBBusy            uint8 = 0x08 // also IP bans and the 15-accounts-per-IP throttle (AuthSocket.cpp:428-456)
	ResultVersionInvalid    uint8 = 0x09
	ResultVersionUpdate     uint8 = 0x0A
	ResultSuspended         uint8 = 0x0C
	ResultParentControl     uint8 = 0x0F // email not verified or geolocked (AuthSocket.cpp:485, :978)
)

// ErrPINRequired means realmd set security flag 1 (AuthSocket.cpp:652-663); v1 cannot answer a PIN grid.
var ErrPINRequired = errors.New("realmd asks for a PIN: raise ForcePinAccountRank above the account's rank or add an account_twofactor_allowed row for this IP")

// AuthError is a realmd rejection. It never carries the password.
type AuthError struct {
	Stage string // "challenge" or "proof"
	Code  uint8
}

func (e *AuthError) Error() string {
	return fmt.Sprintf("realmd %s rejected: %s (code 0x%02X)", e.Stage, resultText(e.Code), e.Code)
}

func resultText(c uint8) string {
	switch c {
	case ResultBanned:
		return "account banned"
	case ResultUnknownAccount:
		return "unknown account or wrong password"
	case ResultIncorrectPassword:
		return "account inactive" // AuthSocket.cpp:513-516
	case ResultAlreadyOnline:
		return "account already online"
	case ResultNoTime:
		return "account blocked (negative shop balance)"
	case ResultDBBusy:
		return "server busy, IP banned, or too many accounts from this IP (15 per 300 s)"
	case ResultVersionInvalid, ResultVersionUpdate:
		return "client build rejected"
	case ResultSuspended:
		return "account suspended or IP-locked"
	case ResultParentControl:
		return "email not verified or account geolocked"
	}
	return "rejected"
}

type Realm struct {
	ID         int     `json:"id"` // position in the list, from 1: the vanilla list sends no id (AuthSocket.cpp:1277)
	Icon       uint32  `json:"icon"`
	Flags      uint8   `json:"flags"`
	Name       string  `json:"name"`
	Address    string  `json:"address"`
	Population float32 `json:"population"`
	Characters uint8   `json:"characters"`
	Timezone   uint8   `json:"timezone"`
}

func (r Realm) Offline() bool { return r.Flags&realmFlagOffline != 0 }

// Ticket is a successful realmd login. Key is secret and the account name is treated
// as sensitive (ruling P10): fmt prints only the redacted String for every verb.
type Ticket struct {
	Account string
	Key     [40]byte
	Realms  []Realm
}

// KeyBytes is the key in the form the world server uses (see Proof.KeyBytes).
func (t Ticket) KeyBytes() []byte { return asBytes(t.Key[:]) }

func (t Ticket) String() string {
	return fmt.Sprintf("auth.Ticket{account=redacted realms=%d key=redacted}", len(t.Realms))
}

// Format makes every verb (%d and %x included) print String, never the fields.
func (t Ticket) Format(f fmt.State, _ rune) { _, _ = io.WriteString(f, t.String()) }

// Pick returns the realm called name (case-insensitive), or the first realm when name is empty.
func (t Ticket) Pick(name string) (Realm, error) {
	for _, r := range t.Realms {
		if name == "" || strings.EqualFold(r.Name, name) {
			if r.Offline() {
				return Realm{}, fmt.Errorf("realm %q is offline for build %d (flags 0x%02X)", r.Name, ClientBuild, r.Flags)
			}
			return r, nil
		}
	}
	return Realm{}, fmt.Errorf("realm %q is not in the realm list (%d realms)", name, len(t.Realms))
}

// Login runs challenge, proof and realm list. A proof rejected with 0x04 is retried
// once on a fresh connection (spec: the rare unpadded-BigNumber mismatch); a real
// wrong password therefore costs two of the server's WrongPass.MaxCount attempts.
func Login(ctx context.Context, addr, account, password string) (*Ticket, error) {
	t, err := loginOnce(ctx, addr, account, password)
	var ae *AuthError
	if errors.As(err, &ae) && ae.Stage == "proof" && ae.Code == ResultUnknownAccount {
		t, err = loginOnce(ctx, addr, account, password)
	}
	return t, err
}

func loginOnce(ctx context.Context, addr, account, password string) (*Ticket, error) {
	var d net.Dialer
	conn, err := d.DialContext(ctx, "tcp", addr)
	if err != nil {
		return nil, fmt.Errorf("realmd: connect %s: %w", addr, err)
	}
	defer conn.Close()
	deadline := time.Now().Add(30 * time.Second)
	if dl, ok := ctx.Deadline(); ok {
		deadline = dl
	}
	_ = conn.SetDeadline(deadline)
	stop := context.AfterFunc(ctx, func() { _ = conn.SetDeadline(time.Unix(1, 0)) })
	defer stop()
	t, err := handshake(conn, account, password, rand.Reader)
	if err != nil && ctx.Err() != nil {
		return nil, fmt.Errorf("%w (%w)", err, ctx.Err())
	}
	return t, err
}

func handshake(rw io.ReadWriter, account, password string, rng io.Reader) (*Ticket, error) {
	user := strings.ToUpper(account)
	if user == "" || len(user) > 16 {
		return nil, errors.New("realmd: account name must be 1-16 characters")
	}
	if _, err := rw.Write(BuildLogonChallenge(user)); err != nil {
		return nil, fmt.Errorf("realmd: send challenge: %w", err)
	}
	c, err := readChallenge(rw)
	if err != nil {
		return nil, err
	}
	p, err := newProofFrom(rng, user, password, c)
	if err != nil {
		return nil, err
	}
	if _, err := rw.Write(BuildLogonProof(p)); err != nil {
		return nil, fmt.Errorf("realmd: send proof: %w", err)
	}
	if err := readProof(rw, p); err != nil {
		return nil, err
	}
	realms, err := requestRealms(rw)
	if err != nil {
		return nil, err
	}
	return &Ticket{Account: user, Key: p.K, Realms: realms}, nil
}

// BuildLogonChallenge lays out sAuthLogonChallenge_C (AuthSocket.cpp:90-107); size counts
// the bytes after the 4-byte header: 30 fixed bytes plus the name.
func BuildLogonChallenge(user string) []byte {
	w := packet.NewWriter().U8(cmdLogonChallenge).U8(0x03).U16(uint16(30 + len(user)))
	w.Append([]byte("WoW\x00")).U8(1).U8(18).U8(1).U16(ClientBuild)
	w.Append([]byte("68x\x00")) // 'x86' (AuthSocket.h:134), checked by VerifyVersion (AuthSocket.cpp:1533)
	w.Append([]byte("niW\x00")) // 'Win' (AuthSocket.h:131)
	w.Append([]byte("SUne"))    // 'enUS' reversed; realmd reverses it back (AuthSocket.cpp:669-671)
	w.U32(0)                    // timezone bias
	w.U32(0x0100007F)           // ip, informational only
	w.U8(uint8(len(user))).Append([]byte(user))
	return w.Bytes()
}

// BuildLogonProof lays out the 75-byte sAuthLogonProof_C_1_11 (AuthSocket.cpp:109-121).
// The version hash for build 7272 is all zeros (RealmList.cpp:39), so VerifyVersion
// passes without a CRC (AuthSocket.cpp:1552-1553).
func BuildLogonProof(p Proof) []byte {
	return packet.NewWriter().U8(cmdLogonProof).Append(p.A[:]).Append(p.M1[:]).
		Append(make([]byte, 20)).U8(0).U8(0).Bytes()
}

func readChallenge(r io.Reader) (Challenge, error) {
	var head [3]byte
	if _, err := io.ReadFull(r, head[:]); err != nil {
		return Challenge{}, fmt.Errorf("realmd: read challenge: %w", err)
	}
	if head[0] != cmdLogonChallenge {
		return Challenge{}, fmt.Errorf("realmd: unexpected command 0x%02X", head[0])
	}
	if head[2] != ResultSuccess {
		return Challenge{}, &AuthError{Stage: "challenge", Code: head[2]}
	}
	var c Challenge
	g, n := []byte{}, []byte{}
	var version [16]byte
	var flags [1]byte
	err := readAll(r, c.B[:])
	if err == nil {
		g, err = readLenPrefixed(r)
	}
	if err == nil {
		n, err = readLenPrefixed(r)
	}
	if err != nil {
		return Challenge{}, fmt.Errorf("realmd: read challenge: %w", err)
	}
	// The salt goes out as s.AsByteArray() (AuthSocket.cpp:616): 31 bytes when the stored
	// salt's high byte is zero. The 1.12 client reads 32, and so do we (ruling T4 concern 2).
	c.Salt = make([]byte, 32)
	if err := readAll(r, c.Salt, version[:], flags[:]); err != nil {
		return Challenge{}, fmt.Errorf("realmd: read challenge: %w (a server salt shorter than 32 bytes, AuthSocket.cpp:616, is unsupported, as with the real 1.12 client)", err)
	}
	nLE := toLE32(srpN)
	if !bytes.Equal(g, []byte{7}) || !bytes.Equal(n, nLE[:]) {
		return Challenge{}, errors.New("realmd: unexpected SRP group")
	}
	if flags[0] != 0 {
		return Challenge{}, ErrPINRequired
	}
	return c, nil
}

func readProof(r io.Reader, p Proof) error {
	var head [2]byte
	if _, err := io.ReadFull(r, head[:]); err != nil {
		return fmt.Errorf("realmd: read proof reply: %w", err)
	}
	if head[0] == cmdLogonChallenge { // "no patch" answer: cmd, 0, code (AuthSocket.cpp:750-755)
		var code [1]byte
		if _, err := io.ReadFull(r, code[:]); err != nil {
			return fmt.Errorf("realmd: read proof reply: %w", err)
		}
		return &AuthError{Stage: "proof", Code: code[0]}
	}
	if head[0] != cmdLogonProof {
		return fmt.Errorf("realmd: unexpected command 0x%02X after proof", head[0])
	}
	if head[1] != ResultSuccess {
		return &AuthError{Stage: "proof", Code: head[1]}
	}
	var rest [24]byte // M2[20] + uint32
	if _, err := io.ReadFull(r, rest[:]); err != nil {
		return fmt.Errorf("realmd: read proof reply: %w", err)
	}
	if !bytes.Equal(rest[:20], p.M2[:]) {
		return errors.New("realmd: server proof mismatch")
	}
	return nil
}

func requestRealms(rw io.ReadWriter) ([]Realm, error) {
	if _, err := rw.Write([]byte{cmdRealmList, 0, 0, 0, 0}); err != nil {
		return nil, fmt.Errorf("realmd: request realm list: %w", err)
	}
	var head [3]byte
	if _, err := io.ReadFull(rw, head[:]); err != nil {
		return nil, fmt.Errorf("realmd: read realm list: %w", err)
	}
	if head[0] != cmdRealmList {
		return nil, fmt.Errorf("realmd: unexpected command 0x%02X for realm list", head[0])
	}
	body := make([]byte, binary.LittleEndian.Uint16(head[1:3]))
	if _, err := io.ReadFull(rw, body); err != nil {
		return nil, fmt.Errorf("realmd: read realm list: %w", err)
	}
	return ParseRealmList(body)
}

// ParseRealmList decodes AuthSocket::LoadRealmlist (AuthSocket.cpp:1230-1281).
func ParseRealmList(body []byte) ([]Realm, error) {
	r := packet.NewReader(body)
	r.U32()
	n := int(r.U8())
	realms := make([]Realm, 0, n)
	for i := 0; i < n; i++ {
		var rl Realm
		rl.ID = i + 1
		rl.Icon, rl.Flags = r.U32(), r.U8()
		rl.Name, rl.Address = r.CString(), r.CString()
		rl.Population, rl.Characters, rl.Timezone = r.F32(), r.U8(), r.U8()
		r.U8()
		realms = append(realms, rl)
	}
	r.U16()
	if err := r.Err(); err != nil {
		return nil, fmt.Errorf("realmd: realm list: %w", err)
	}
	return realms, nil
}

func readAll(r io.Reader, bufs ...[]byte) error {
	for _, b := range bufs {
		if _, err := io.ReadFull(r, b); err != nil {
			return err
		}
	}
	return nil
}

func readLenPrefixed(r io.Reader) ([]byte, error) {
	var n [1]byte
	if _, err := io.ReadFull(r, n[:]); err != nil {
		return nil, err
	}
	b := make([]byte, n[0])
	_, err := io.ReadFull(r, b)
	return b, err
}
