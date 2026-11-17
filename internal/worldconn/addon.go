package worldconn

import (
	"bytes"
	"compress/zlib"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/TortoiseBots/turtle-agent/internal/opcode"
	"github.com/TortoiseBots/turtle-agent/internal/packet"
	"github.com/TortoiseBots/turtle-agent/internal/privfile"
)

// fingerprintAddons are the four addons the anticheat reads its fingerprint from, in the
// order of the four bytes (Anticheat/AddonHandler.cpp:80-86). Their flags byte is the
// fingerprint byte: 0x01 on all four means "no fingerprint yet" and the server makes one up
// (:217-253); a real client stores what the server sends and presents it at every login.
var fingerprintAddons = [4]string{"Blizzard_BindingUI", "Blizzard_InspectUI", "Blizzard_MacroUI", "Blizzard_RaidUI"}

// StockAddons is what a stock 1.12.1 client lists in CMSG_AUTH_SESSION: every Blizzard_*
// addon it ships, in directory order, enabled with the standard modulus CRC.
var StockAddons = []string{
	"Blizzard_AuctionUI", "Blizzard_BattlefieldMinimap", "Blizzard_BindingUI", "Blizzard_CraftUI",
	"Blizzard_GMSurveyUI", "Blizzard_InspectUI", "Blizzard_MacroUI", "Blizzard_RaidUI",
	"Blizzard_TalentUI", "Blizzard_TradeSkillUI", "Blizzard_TrainerUI",
}

const standardModulusCRC = 0x4C1C776D // AddonHandler.cpp:141

// Addons is the addon list one login presents and the fingerprint it carries. A nil
// *Addons means the stock list with no stored fingerprint.
type Addons struct {
	Names       []string // nil: StockAddons
	Fingerprint uint32   // 0: none yet, the server generates one
	// OnFingerprint, if set, is called with the fingerprint the server announces in
	// SMSG_ADDON_INFO when it differs from the one sent.
	OnFingerprint func(uint32)
}

// ValidateAddonNames checks an override list: every fingerprint addon must be in it, or the
// server cannot read a stored fingerprint back and makes a new one at every login.
func ValidateAddonNames(names []string) error {
	have := map[string]bool{}
	for _, n := range names {
		if n == "" || strings.ContainsRune(n, 0) {
			return fmt.Errorf("addon name %q is empty or holds a NUL", n)
		}
		if have[n] {
			return fmt.Errorf("addon %s is listed twice", n)
		}
		have[n] = true
	}
	for _, f := range fingerprintAddons {
		if !have[f] {
			return fmt.Errorf("the list must include %s (the server keeps its fingerprint in that addon's flags)", f)
		}
	}
	return nil
}

// FingerprintValid is the core's rule (AddonHandler.cpp:25-34): no byte may be 0x00-0x02.
func FingerprintValid(fp uint32) bool {
	for i := 0; i < 4; i++ {
		if byte(fp>>(8*i)) <= 0x02 {
			return false
		}
	}
	return true
}

func (a *Addons) names() []string {
	if a == nil || a.Names == nil {
		return StockAddons
	}
	return a.Names
}

// flags is the flags value for a listed addon.
func (a *Addons) flags(name string) uint8 {
	if a != nil && FingerprintValid(a.Fingerprint) {
		for i, f := range fingerprintAddons {
			if name == f {
				return byte(a.Fingerprint >> (8 * i)) // the core reads the uint32 little-endian
			}
		}
	}
	return 0x01
}

// AddonBlock is the tail of CMSG_AUTH_SESSION for the stock list and no fingerprint.
func AddonBlock() []byte { return (*Addons)(nil).Block() }

// Block is the tail of CMSG_AUTH_SESSION: u32 uncompressed size, then a zlib stream of
// name, u8 flags, u32 modulus CRC, u32 URL CRC (AddonHandler.cpp:114-139, :200).
func (a *Addons) Block() []byte {
	plain := packet.NewWriter()
	for _, name := range a.names() {
		plain.CString(name).U8(a.flags(name)).U32(standardModulusCRC).U32(0)
	}
	var z bytes.Buffer
	zw := zlib.NewWriter(&z)
	zw.Write(plain.Bytes())
	zw.Close()
	return packet.NewWriter().U32(uint32(plain.Len())).Append(z.Bytes()).Bytes()
}

// parseAddonInfo reads SMSG_ADDON_INFO (AddonHandler.cpp:212-318), one entry per addon sent,
// in order: u8 2, u8 flags, [u8 sendData, [256 modulus bytes], u32 0] when flags is not 0,
// u8 url flag. It returns the fingerprint the flags of the four fingerprint addons make up.
func (a *Addons) parseAddonInfo(body []byte) (uint32, bool) {
	r := packet.NewReader(body)
	var fp [4]byte
	var seen [4]bool
	for _, name := range a.names() {
		r.U8() // 2
		flags := r.U8()
		if flags != 0 {
			if r.U8() != 0 {
				r.Bytes(256)
			}
			r.U32()
		}
		r.U8() // url
		if r.Err() != nil {
			return 0, false
		}
		for i, f := range fingerprintAddons {
			if name == f {
				fp[i], seen[i] = flags, true
			}
		}
	}
	for _, s := range seen {
		if !s {
			return 0, false
		}
	}
	v := uint32(fp[0]) | uint32(fp[1])<<8 | uint32(fp[2])<<16 | uint32(fp[3])<<24
	return v, FingerprintValid(v)
}

// observe sees every server packet; it learns the fingerprint from SMSG_ADDON_INFO.
func (a *Addons) observe(op uint16, body []byte) {
	if a == nil || op != opcode.SMSGAddonInfo {
		return
	}
	fp, ok := a.parseAddonInfo(body)
	if !ok || fp == a.Fingerprint {
		return
	}
	a.Fingerprint = fp
	if a.OnFingerprint != nil {
		a.OnFingerprint(fp)
	}
}

// Option changes how a connection authenticates.
type Option func(*Conn)

// WithAddons sets the addon list and fingerprint the login presents.
func WithAddons(a *Addons) Option { return func(c *Conn) { c.addons = a } }

// FingerprintFile keeps the fingerprint between runs, as the 1.12 client keeps it in its
// WoW folder. The file holds a hex uint32 and is mode 0600; it is read only if it is a
// regular file only its owner can access, and written only when the fingerprint is new.
type FingerprintFile struct {
	Path string
}

// Load returns the stored fingerprint, or 0 when there is none or it is not valid.
func (f *FingerprintFile) Load() uint32 {
	raw, err := privfile.Read(f.Path, 64)
	if err != nil {
		return 0
	}
	var fp uint32
	if _, err := fmt.Sscanf(strings.TrimSpace(string(raw)), "0x%x", &fp); err != nil || !FingerprintValid(fp) {
		return 0
	}
	return fp
}

// Save stores fp unless a file is already there. The file is complete and private before it
// takes its name, and os.Link fails if the name exists, so two processes saving at once
// cannot overwrite each other: the first wins and the other keeps the winner's value (it
// presents that one from its next login on). A stored file that is not a valid fingerprint
// is replaced.
func (f *FingerprintFile) Save(fp uint32) error {
	dir := filepath.Dir(f.Path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".fingerprint-*") // mode 0600
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := fmt.Fprintf(tmp, "0x%08x\n", fp); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	err = os.Link(tmp.Name(), f.Path)
	if errors.Is(err, fs.ErrExist) {
		if f.Load() != 0 {
			return nil // lost the race to a valid fingerprint
		}
		return os.Rename(tmp.Name(), f.Path) // unreadable or invalid: replace
	}
	return err
}
