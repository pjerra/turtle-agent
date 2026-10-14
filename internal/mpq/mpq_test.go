package mpq

import (
	"bytes"
	"errors"
	"math/rand"
	"os"
	"path/filepath"
	"testing"
)

func sample(n int, seed int64) []byte {
	r := rand.New(rand.NewSource(seed))
	b := make([]byte, n)
	for i := range b {
		b[i] = byte(r.Intn(7)) // compressible
	}
	return b
}

func writeArchive(t *testing.T, dir, name string, files []File) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), Build(files), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestReadRoundTrip(t *testing.T) {
	dir := t.TempDir()
	big := sample(3*buildSector+123, 1)
	files := []File{
		{Name: `Textures\Minimap\md5translate.trs`, Data: []byte("dir: x\n")},
		{Name: `World\big.bin`, Data: big},
		{Name: `World\raw.bin`, Data: sample(5000, 2), Raw: true},
		{Name: `World\enc.bin`, Data: sample(9000, 3), Encrypt: true},
		{Name: `World\encraw.bin`, Data: sample(4099, 4), Encrypt: true, Raw: true},
		{Name: `World\empty.bin`},
	}
	writeArchive(t, dir, "a.MPQ", files)
	a, err := Open(filepath.Join(dir, "a.MPQ"))
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	for _, f := range files {
		got, err := a.Read(f.Name)
		if err != nil {
			t.Fatalf("%s: %v", f.Name, err)
		}
		if !bytes.Equal(got, f.Data) {
			t.Errorf("%s: %d bytes read, want %d (equal=%v)", f.Name, len(got), len(f.Data), bytes.Equal(got, f.Data))
		}
	}
	// names are case and slash insensitive
	if _, err := a.Read(`world/BIG.bin`); err != nil {
		t.Errorf("case/slash-insensitive lookup: %v", err)
	}
	if _, err := a.Read(`World\nothere.bin`); !errors.Is(err, ErrNotFound) {
		t.Errorf("missing file: err = %v, want ErrNotFound", err)
	}
}

func TestSetLaterArchiveWins(t *testing.T) {
	dir := t.TempDir()
	writeArchive(t, dir, "base.MPQ", []File{{Name: "a.txt", Data: []byte("old")}, {Name: "only-base.txt", Data: []byte("b")}})
	writeArchive(t, dir, "patch.MPQ", []File{{Name: "a.txt", Data: []byte("new")}})
	s, err := OpenSet(dir, []string{"base.MPQ", "missing.MPQ", "patch.MPQ"})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if b, _ := s.Read("a.txt"); string(b) != "new" {
		t.Errorf("a.txt = %q, want the patch's", b)
	}
	if b, _ := s.Read("only-base.txt"); string(b) != "b" {
		t.Errorf("only-base.txt = %q", b)
	}
	if len(s.Archives()) != 2 {
		t.Errorf("archives = %v; the missing one is skipped", s.Archives())
	}
	if _, err := OpenSet(dir, []string{"nope.MPQ"}); err == nil {
		t.Error("a set of nothing opens")
	}
}

func TestOpenRejectsGarbage(t *testing.T) {
	p := filepath.Join(t.TempDir(), "x.MPQ")
	os.WriteFile(p, bytes.Repeat([]byte{7}, 4096), 0o644)
	if _, err := Open(p); err == nil {
		t.Error("garbage opened")
	}
}

func TestCryptTableKnownValues(t *testing.T) {
	// The first entries of StormLib's table.
	want := []uint32{0x55C636E2, 0x02BE0170, 0x584B71D4, 0x2984F00E}
	for i, w := range want {
		if cryptTable[i] != w {
			t.Errorf("cryptTable[%d] = %#x, want %#x", i, cryptTable[i], w)
		}
	}
	// Known hash of the table names.
	if got := hashString("(hash table)", 3); got != 0xC3AF3770 {
		t.Errorf("hash of (hash table) = %#x, want 0xC3AF3770", got)
	}
	if got := hashString("(block table)", 3); got != 0xEC83B3A3 {
		t.Errorf("hash of (block table) = %#x, want 0xEC83B3A3", got)
	}
}

func TestEncryptDecrypt(t *testing.T) {
	in := []uint32{1, 2, 3, 0xdeadbeef, 5}
	w := append([]uint32(nil), in...)
	encryptBlock(w, 0x1234)
	decryptBlock(w, 0x1234)
	for i := range in {
		if w[i] != in[i] {
			t.Fatalf("round trip differs at %d", i)
		}
	}
}
