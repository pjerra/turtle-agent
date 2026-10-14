// Package mpq is a small read-only reader for the MPQ v1 archives of the 1.12 / Tortoise 1.18.1 client: hash and block
// tables, encrypted tables and files, sector-compressed files (zlib, bzip2, PKWARE implode) and single-unit files.
// Audio-only methods (Huffman, ADPCM) are refused. A Set stacks archives, the way the client loads its patches: a later
// archive overrides an earlier one.
package mpq

import (
	"bytes"
	"compress/bzip2"
	"compress/zlib"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
)

const (
	flagImplode    = 0x00000100
	flagCompress   = 0x00000200
	flagEncrypted  = 0x00010000
	flagFixKey     = 0x00020000
	flagPatch      = 0x00100000
	flagSingleUnit = 0x01000000
	flagDelete     = 0x02000000
	flagSectorCRC  = 0x04000000
	flagExists     = 0x80000000

	hashFree    = 0xFFFFFFFF
	hashDeleted = 0xFFFFFFFE
)

// ErrNotFound means no archive of the set holds the file (or the newest one marks it deleted).
var ErrNotFound = errors.New("mpq: file not found")

var cryptTable = buildCryptTable()

func buildCryptTable() [0x500]uint32 {
	var t [0x500]uint32
	seed := uint32(0x00100001)
	for i := 0; i < 0x100; i++ {
		idx := i
		for k := 0; k < 5; k++ {
			seed = (seed*125 + 3) % 0x2AAAAB
			t1 := (seed & 0xFFFF) << 16
			seed = (seed*125 + 3) % 0x2AAAAB
			t2 := seed & 0xFFFF
			t[idx] = t1 | t2
			idx += 0x100
		}
	}
	return t
}

// hashString is the MPQ name hash of type kind (0 table index, 1 and 2 name check, 3 file key).
func hashString(s string, kind uint32) uint32 {
	seed1, seed2 := uint32(0x7FED7FED), uint32(0xEEEEEEEE)
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c >= 'a' && c <= 'z' {
			c -= 'a' - 'A'
		}
		if c == '/' {
			c = '\\'
		}
		seed1 = cryptTable[kind*0x100+uint32(c)] ^ (seed1 + seed2)
		seed2 = uint32(c) + seed1 + seed2 + (seed2 << 5) + 3
	}
	return seed1
}

func decryptBlock(data []uint32, key uint32) {
	seed := uint32(0xEEEEEEEE)
	for i := range data {
		seed += cryptTable[0x400+(key&0xFF)]
		ch := data[i] ^ (key + seed)
		data[i] = ch
		key = ((^key << 0x15) + 0x11111111) | (key >> 0x0B)
		seed = ch + seed + (seed << 5) + 3
	}
}

type hashEntry struct {
	nameA, nameB uint32
	locale       uint16
	platform     uint16
	block        uint32
}

type blockEntry struct {
	pos, csize, size, flags uint32
}

// Archive is one open MPQ file.
type Archive struct {
	Path       string
	f          *os.File
	base       int64
	sectorSize uint32
	hash       []hashEntry
	blocks     []blockEntry
}

// Open reads the tables of the archive at path.
func Open(path string) (*Archive, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	a := &Archive{Path: path, f: f}
	if err := a.readTables(); err != nil {
		f.Close()
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return a, nil
}

// Close closes the file.
func (a *Archive) Close() error { return a.f.Close() }

func (a *Archive) readTables() error {
	var hdr [32]byte
	// The header sits at a 512-byte boundary (normally offset 0).
	found := false
	for off := int64(0); off < 1<<20; off += 512 {
		if _, err := a.f.ReadAt(hdr[:], off); err != nil {
			break
		}
		if string(hdr[:4]) == "MPQ\x1a" {
			a.base, found = off, true
			break
		}
	}
	if !found {
		return errors.New("no MPQ header")
	}
	le := binary.LittleEndian
	hdrSize := le.Uint32(hdr[4:])
	if hdrSize < 32 {
		return errors.New("bad header size")
	}
	version := le.Uint16(hdr[12:])
	if version > 1 {
		return fmt.Errorf("MPQ format version %d is not supported", version)
	}
	shift := uint(le.Uint16(hdr[14:]))
	a.sectorSize = 512 << shift
	hashPos, blockPos := int64(le.Uint32(hdr[16:])), int64(le.Uint32(hdr[20:]))
	hashN, blockN := le.Uint32(hdr[24:]), le.Uint32(hdr[28:])
	if version == 1 && hdrSize >= 44 {
		var ext [12]byte
		if _, err := a.f.ReadAt(ext[:], a.base+32); err == nil {
			hashPos |= int64(le.Uint16(ext[8:])) << 32
			blockPos |= int64(le.Uint16(ext[10:])) << 32
		}
	}
	if hashN == 0 || hashN&(hashN-1) != 0 || hashN > 1<<22 || blockN > 1<<22 {
		return errors.New("bad table sizes")
	}
	raw, err := a.readU32s(a.base+hashPos, hashN*4, hashString("(hash table)", 3))
	if err != nil {
		return err
	}
	a.hash = make([]hashEntry, hashN)
	for i := range a.hash {
		r := raw[i*4:]
		a.hash[i] = hashEntry{r[0], r[1], uint16(r[2]), uint16(r[2] >> 16), r[3]}
	}
	raw, err = a.readU32s(a.base+blockPos, blockN*4, hashString("(block table)", 3))
	if err != nil {
		return err
	}
	a.blocks = make([]blockEntry, blockN)
	for i := range a.blocks {
		r := raw[i*4:]
		a.blocks[i] = blockEntry{r[0], r[1], r[2], r[3]}
	}
	return nil
}

func (a *Archive) readU32s(off int64, n uint32, key uint32) ([]uint32, error) {
	buf := make([]byte, n*4)
	if _, err := a.f.ReadAt(buf, off); err != nil {
		return nil, err
	}
	out := make([]uint32, n)
	for i := range out {
		out[i] = binary.LittleEndian.Uint32(buf[i*4:])
	}
	if key != 0 {
		decryptBlock(out, key)
	}
	return out, nil
}

func (a *Archive) find(name string) (blockEntry, bool) {
	mask := uint32(len(a.hash) - 1)
	start := hashString(name, 0) & mask
	nA, nB := hashString(name, 1), hashString(name, 2)
	best, have := blockEntry{}, false
	for i := uint32(0); i <= mask; i++ {
		e := a.hash[(start+i)&mask]
		if e.block == hashFree {
			break
		}
		if e.block == hashDeleted || e.nameA != nA || e.nameB != nB || int(e.block) >= len(a.blocks) {
			continue
		}
		b := a.blocks[e.block]
		if !have || e.locale == 0 {
			best, have = b, true
		}
		if e.locale == 0 {
			break
		}
	}
	return best, have
}

// Has reports whether the archive lists name (a delete marker counts as listed; see Read).
func (a *Archive) Has(name string) bool { _, ok := a.find(name); return ok }

// Read returns the file called name. ErrNotFound if the archive has none or marks it deleted.
func (a *Archive) Read(name string) ([]byte, error) {
	b, ok := a.find(name)
	if !ok || b.flags&flagExists == 0 || b.flags&flagDelete != 0 {
		return nil, ErrNotFound
	}
	if b.flags&flagPatch != 0 {
		return nil, fmt.Errorf("mpq: %s is a binary patch file (not supported)", name)
	}
	return a.readFile(name, b)
}

func baseName(name string) string {
	if i := strings.LastIndexAny(name, "\\/"); i >= 0 {
		return name[i+1:]
	}
	return name
}

func (a *Archive) readFile(name string, b blockEntry) ([]byte, error) {
	if b.size == 0 {
		return nil, nil
	}
	if b.csize > 1<<30 || b.size > 1<<30 {
		return nil, errors.New("mpq: implausible file size")
	}
	raw := make([]byte, b.csize)
	if _, err := a.f.ReadAt(raw, a.base+int64(b.pos)); err != nil {
		return nil, fmt.Errorf("mpq: %s: %w", name, err)
	}
	key := uint32(0)
	if b.flags&flagEncrypted != 0 {
		key = hashString(baseName(name), 3)
		if b.flags&flagFixKey != 0 {
			key = (key + b.pos) ^ b.size
		}
	}
	compressed := b.flags&(flagCompress|flagImplode) != 0
	if b.flags&flagSingleUnit != 0 {
		if key != 0 {
			decryptBytes(raw, key)
		}
		if !compressed || b.csize >= b.size {
			return raw[:min(len(raw), int(b.size))], nil
		}
		return a.sector(raw, int(b.size), b.flags)
	}
	n := int((b.size + a.sectorSize - 1) / a.sectorSize)
	tabLen := n + 1
	if b.flags&flagSectorCRC != 0 {
		tabLen++
	}
	if !compressed {
		// Stored sectors back to back, each possibly encrypted.
		out := make([]byte, 0, b.size)
		for i := 0; i < n; i++ {
			lo := i * int(a.sectorSize)
			hi := min(lo+int(a.sectorSize), int(b.size))
			if hi > len(raw) {
				return nil, errors.New("mpq: truncated file")
			}
			s := append([]byte(nil), raw[lo:hi]...)
			if key != 0 {
				decryptBytes(s, key+uint32(i))
			}
			out = append(out, s...)
		}
		return out, nil
	}
	if len(raw) < tabLen*4 {
		return nil, errors.New("mpq: truncated sector table")
	}
	offs := make([]uint32, tabLen)
	for i := range offs {
		offs[i] = binary.LittleEndian.Uint32(raw[i*4:])
	}
	if key != 0 {
		decryptBlock(offs, key-1)
	}
	out := make([]byte, 0, b.size)
	for i := 0; i < n; i++ {
		lo, hi := int(offs[i]), int(offs[i+1])
		if lo > hi || hi > len(raw) {
			return nil, fmt.Errorf("mpq: %s: bad sector table", name)
		}
		s := append([]byte(nil), raw[lo:hi]...)
		if key != 0 {
			decryptBytes(s, key+uint32(i))
		}
		want := min(int(a.sectorSize), int(b.size)-i*int(a.sectorSize))
		if len(s) < want {
			d, err := a.sector(s, want, b.flags)
			if err != nil {
				return nil, fmt.Errorf("mpq: %s sector %d: %w", name, i, err)
			}
			s = d
		}
		out = append(out, s...)
	}
	return out, nil
}

func decryptBytes(b []byte, key uint32) {
	n := len(b) / 4
	w := make([]uint32, n)
	for i := range w {
		w[i] = binary.LittleEndian.Uint32(b[i*4:])
	}
	decryptBlock(w, key)
	for i := range w {
		binary.LittleEndian.PutUint32(b[i*4:], w[i])
	}
}

// sector decompresses one compressed sector (or single unit) to want bytes.
func (a *Archive) sector(s []byte, want int, flags uint32) ([]byte, error) {
	if flags&flagImplode != 0 && flags&flagCompress == 0 {
		return explode(s, want)
	}
	if len(s) == 0 {
		return nil, errors.New("empty sector")
	}
	mask := s[0]
	data := s[1:]
	var err error
	// Methods are undone in the reverse of the order the writer applied them.
	for _, step := range []struct {
		bit  byte
		name string
		fn   func([]byte, int) ([]byte, error)
	}{{0x10, "bzip2", unBzip2}, {0x08, "pkware", explode}, {0x02, "zlib", unZlib}} {
		if mask&step.bit != 0 {
			if data, err = step.fn(data, want); err != nil {
				return nil, fmt.Errorf("%s: %w", step.name, err)
			}
			mask &^= step.bit
		}
	}
	if mask != 0 {
		return nil, fmt.Errorf("unsupported compression 0x%02x", mask)
	}
	return data, nil
}

func unZlib(b []byte, want int) ([]byte, error) {
	r, err := zlib.NewReader(bytes.NewReader(b))
	if err != nil {
		return nil, err
	}
	defer r.Close()
	return readUpTo(r, want)
}

func unBzip2(b []byte, want int) ([]byte, error) {
	return readUpTo(bzip2.NewReader(bytes.NewReader(b)), want)
}

func readUpTo(r io.Reader, want int) ([]byte, error) {
	out := make([]byte, want)
	n, err := io.ReadFull(r, out)
	if err != nil && !(errors.Is(err, io.ErrUnexpectedEOF) && n > 0) {
		return nil, err
	}
	return out[:n], nil
}

// Set is a stack of archives; the later ones win.
type Set struct {
	archives []*Archive
}

// OpenSet opens the named files of dir in order (earliest first). A missing optional file is skipped.
func OpenSet(dir string, names []string) (*Set, error) {
	s := &Set{}
	for _, n := range names {
		p := dir + string(os.PathSeparator) + n
		a, err := Open(p)
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				continue
			}
			s.Close()
			return nil, err
		}
		s.archives = append(s.archives, a)
	}
	if len(s.archives) == 0 {
		return nil, fmt.Errorf("mpq: none of the archives exist in %s", dir)
	}
	return s, nil
}

// Close closes every archive.
func (s *Set) Close() {
	for _, a := range s.archives {
		a.Close()
	}
}

// Archives lists the paths of the open archives, earliest first.
func (s *Set) Archives() []string {
	var out []string
	for _, a := range s.archives {
		out = append(out, a.Path)
	}
	return out
}

// Read returns the newest version of name. A file whose newest listing is a delete marker is not found.
func (s *Set) Read(name string) ([]byte, error) {
	for i := len(s.archives) - 1; i >= 0; i-- {
		a := s.archives[i]
		b, ok := a.find(name)
		if !ok {
			continue
		}
		if b.flags&flagDelete != 0 || b.flags&flagExists == 0 {
			return nil, ErrNotFound
		}
		return a.Read(name)
	}
	return nil, ErrNotFound
}
