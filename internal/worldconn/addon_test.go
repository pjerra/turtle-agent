package worldconn_test

import (
	"bytes"
	"compress/zlib"
	"io"
	"net"
	"os"
	"path/filepath"
	"slices"
	"sync"
	"testing"

	"github.com/pjerra/turtle-agent/internal/opcode"
	"github.com/pjerra/turtle-agent/internal/packet"
	"github.com/pjerra/turtle-agent/internal/worldconn"
	"github.com/pjerra/turtle-agent/internal/worldconn/worldtest"
)

type addonEntry struct {
	name   string
	flags  uint8
	modCRC uint32
	urlCRC uint32
}

// decodeBlock reads an addon block the way the core does (AddonHandler.cpp:114-139).
func decodeBlock(t *testing.T, block []byte) []addonEntry {
	t.Helper()
	r := packet.NewReader(block)
	size := int(r.U32())
	zr, err := zlib.NewReader(bytes.NewReader(r.Bytes(r.Remaining())))
	if err != nil {
		t.Fatal(err)
	}
	plain, err := io.ReadAll(zr)
	if err != nil || len(plain) != size {
		t.Fatalf("inflate: %d bytes, declared %d, err %v", len(plain), size, err)
	}
	var out []addonEntry
	pr := packet.NewReader(plain)
	for pr.Remaining() > 0 {
		out = append(out, addonEntry{name: pr.CString(), flags: pr.U8(), modCRC: pr.U32(), urlCRC: pr.U32()})
	}
	if pr.Err() != nil {
		t.Fatal(pr.Err())
	}
	return out
}

func TestStockBlockIsTheClientsFullBlizzardSet(t *testing.T) {
	got := decodeBlock(t, worldconn.AddonBlock())
	if len(got) != 11 {
		t.Fatalf("%d addons, want the 11 stock Blizzard_* ones", len(got))
	}
	for i, e := range got {
		if e.name != worldconn.StockAddons[i] || e.flags != 1 || e.modCRC != 0x4C1C776D || e.urlCRC != 0 {
			t.Fatalf("entry %d = %+v", i, e)
		}
	}
}

func TestBlockCarriesStoredFingerprintInTheFourAddonFlags(t *testing.T) {
	a := &worldconn.Addons{Fingerprint: 0x7A6B5C4D}
	want := map[string]uint8{"Blizzard_BindingUI": 0x4D, "Blizzard_InspectUI": 0x5C, "Blizzard_MacroUI": 0x6B, "Blizzard_RaidUI": 0x7A}
	for _, e := range decodeBlock(t, a.Block()) {
		w, ok := want[e.name]
		if !ok {
			w = 1
		}
		if e.flags != w {
			t.Fatalf("%s flags %#x, want %#x", e.name, e.flags, w)
		}
	}
	// An invalid stored value (a byte <= 2) is never sent: the server would call it invalid.
	for _, e := range decodeBlock(t, (&worldconn.Addons{Fingerprint: 0x7A020304}).Block()) {
		if e.flags != 1 {
			t.Fatalf("%s sent %#x for an invalid fingerprint", e.name, e.flags)
		}
	}
}

func TestBlockIsStableAcrossBuilds(t *testing.T) {
	a := &worldconn.Addons{Fingerprint: 0x7A6B5C4D}
	if !bytes.Equal(a.Block(), a.Block()) {
		t.Fatal("two builds of the same list differ")
	}
}

func TestValidateAddonNames(t *testing.T) {
	good := append([]string{"Blizzard_AuctionUI"}, "Blizzard_BindingUI", "Blizzard_InspectUI", "Blizzard_MacroUI", "Blizzard_RaidUI")
	cases := []struct {
		name  string
		names []string
		ok    bool
	}{
		{"stock", worldconn.StockAddons, true},
		{"minimal", good, true},
		{"missing fingerprint addon", []string{"Blizzard_BindingUI", "Blizzard_InspectUI", "Blizzard_MacroUI"}, false},
		{"duplicate", append(append([]string{}, good...), "Blizzard_RaidUI"), false},
		{"empty name", append(append([]string{}, good...), ""), false},
	}
	for _, c := range cases {
		if err := worldconn.ValidateAddonNames(c.names); (err == nil) != c.ok {
			t.Errorf("%s: err %v", c.name, err)
		}
	}
}

// addonInfo builds SMSG_ADDON_INFO as the core does for the given list (AddonHandler.cpp:262-318).
func addonInfo(names []string, fp uint32, announce bool) []byte {
	fpAddons := []string{"Blizzard_BindingUI", "Blizzard_InspectUI", "Blizzard_MacroUI", "Blizzard_RaidUI"}
	w := packet.NewWriter()
	for _, n := range names {
		w.U8(2)
		written := false
		for i, f := range fpAddons {
			if n == f && announce {
				w.U8(byte(fp >> (8 * i))).U8(1).Append(make([]byte, 256)).U32(0)
				written = true
			}
		}
		if !written {
			w.U8(1).U8(0).U32(0) // flags 1, standard CRC: no data, then the "unknown" u32
		}
		w.U8(0)
	}
	return w.Bytes()
}

func TestFingerprintIsLearnedFromAddonInfo(t *testing.T) {
	var got []uint32
	a := &worldconn.Addons{OnFingerprint: func(fp uint32) { got = append(got, fp) }}
	body := addonInfo(worldconn.StockAddons, 0x7A6B5C4D, true)
	c, _, err := pipeAuthAddons(t, a, worldtest.Options{Early: []worldconn.Packet{{Op: opcode.SMSGAddonInfo, Body: body}}})
	if err != nil {
		t.Fatal(err)
	}
	_ = c
	if len(got) != 1 || got[0] != 0x7A6B5C4D || a.Fingerprint != 0x7A6B5C4D {
		t.Fatalf("learned %#x, a.Fingerprint %#x", got, a.Fingerprint)
	}
}

func TestKnownFingerprintIsNotReannounced(t *testing.T) {
	calls := 0
	a := &worldconn.Addons{Fingerprint: 0x7A6B5C4D, OnFingerprint: func(uint32) { calls++ }}
	body := addonInfo(worldconn.StockAddons, 0x7A6B5C4D, true) // the core echoes the flags back
	if _, _, err := pipeAuthAddons(t, a, worldtest.Options{Early: []worldconn.Packet{{Op: opcode.SMSGAddonInfo, Body: body}}}); err != nil {
		t.Fatal(err)
	}
	if calls != 0 {
		t.Fatalf("OnFingerprint called %d times for an unchanged fingerprint", calls)
	}
}

func TestAddonInfoWithoutFingerprintIsIgnored(t *testing.T) {
	calls := 0
	a := &worldconn.Addons{OnFingerprint: func(uint32) { calls++ }}
	for _, body := range [][]byte{nil, {2}, addonInfo(worldconn.StockAddons, 0, false), addonInfo(worldconn.StockAddons, 0x01020304, true)} {
		if _, _, err := pipeAuthAddons(t, a, worldtest.Options{Early: []worldconn.Packet{{Op: opcode.SMSGAddonInfo, Body: body}}}); err != nil {
			t.Fatal(err)
		}
	}
	if calls != 0 || a.Fingerprint != 0 {
		t.Fatalf("calls %d fingerprint %#x", calls, a.Fingerprint)
	}
}

func pipeAuthAddons(t *testing.T, a *worldconn.Addons, o worldtest.Options) (*worldconn.Conn, *worldtest.Server, error) {
	t.Helper()
	cli, srv := net.Pipe()
	t.Cleanup(func() { cli.Close(); srv.Close() })
	type res struct {
		s   *worldtest.Server
		err error
	}
	ch := make(chan res, 1)
	go func() { s, err := worldtest.Accept(srv, key40, o); ch <- res{s, err} }()
	c, err := worldconn.Client(cli, "HARNESS", key40, nil, worldconn.WithAddons(a))
	if err != nil {
		cli.Close()
	}
	r := <-ch
	if r.err != nil {
		t.Fatalf("server: %v", r.err)
	}
	return c, r.s, err
}

func TestFingerprintFileRoundTrip(t *testing.T) {
	f := &worldconn.FingerprintFile{Path: filepath.Join(t.TempDir(), "sub", "addon-fingerprint")}
	if f.Load() != 0 {
		t.Fatal("empty file loaded a fingerprint")
	}
	if err := f.Save(0x7A6B5C4D); err != nil {
		t.Fatal(err)
	}
	if got := f.Load(); got != 0x7A6B5C4D {
		t.Fatalf("loaded %#x", got)
	}
	if info, _ := os.Stat(f.Path); info.Mode().Perm() != 0o600 {
		t.Fatalf("mode %v", info.Mode())
	}
	if err := f.Save(0x11223344); err != nil || f.Load() != 0x7A6B5C4D {
		t.Fatalf("second save replaced the stored fingerprint: %#x err %v", f.Load(), err)
	}
	os.WriteFile(f.Path, []byte("0x01020304\n"), 0o600)
	if f.Load() != 0 {
		t.Fatal("invalid stored fingerprint was accepted")
	}
	os.WriteFile(f.Path, []byte("garbage"), 0o600)
	if f.Load() != 0 {
		t.Fatal("garbage was accepted")
	}
}

func TestFingerprintFileRefusesSymlinkAndLooseMode(t *testing.T) {
	dir := t.TempDir()
	real := filepath.Join(dir, "real")
	os.WriteFile(real, []byte("0x7a6b5c4d\n"), 0o600)
	link := filepath.Join(dir, "link")
	if err := os.Symlink(real, link); err != nil {
		t.Skip(err)
	}
	if (&worldconn.FingerprintFile{Path: link}).Load() != 0 {
		t.Fatal("followed a symlink")
	}
	os.Chmod(real, 0o644)
	if (&worldconn.FingerprintFile{Path: real}).Load() != 0 {
		t.Fatal("read a group/world-readable file")
	}
	os.Chmod(real, 0o600)
	if (&worldconn.FingerprintFile{Path: real}).Load() != 0x7A6B5C4D {
		t.Fatal("did not read a private file")
	}
}

// Concurrent first logins: exactly one value is stored, and it is one of those offered.
func TestFingerprintFileConcurrentSaveKeepsFirst(t *testing.T) {
	path := filepath.Join(t.TempDir(), "fp")
	vals := []uint32{0x7A6B5C4D, 0x11223344, 0x55667788, 0x99AABBCC}
	var wg sync.WaitGroup
	for _, v := range vals {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := (&worldconn.FingerprintFile{Path: path}).Save(v); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	got := (&worldconn.FingerprintFile{Path: path}).Load()
	if !slices.Contains(vals, got) {
		t.Fatalf("stored %#x", got)
	}
	if err := (&worldconn.FingerprintFile{Path: path}).Save(0x01010101 + 0x02020202); err != nil || (&worldconn.FingerprintFile{Path: path}).Load() != got {
		t.Fatalf("later save changed %#x", got)
	}
	entries, _ := os.ReadDir(filepath.Dir(path))
	if len(entries) != 1 {
		t.Fatalf("temp files left: %v", entries)
	}
}
