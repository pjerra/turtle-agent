package mpq

import (
	"bytes"
	"compress/zlib"
	"encoding/binary"
	"sort"
)

// File is one file of an archive Build writes.
type File struct {
	Name string
	Data []byte
	// Raw stores the sectors uncompressed; else every sector is zlib-compressed (the usual case in the client's archives).
	Raw bool
	// Encrypt encrypts the sectors with the file key.
	Encrypt bool
}

const buildSector = 4096 // 512 << 3

// Build writes a small MPQ v1 archive holding files, for tests and fixtures of code that reads the client's archives. The
// hash table is the next power of two over twice the file count.
func Build(files []File) []byte {
	var body bytes.Buffer
	body.Write(make([]byte, 32)) // header, filled in last
	blocks := make([]blockEntry, len(files))
	for i, f := range files {
		blocks[i] = blockEntry{pos: uint32(body.Len()), size: uint32(len(f.Data)), flags: flagExists}
		key := uint32(0)
		if f.Encrypt {
			blocks[i].flags |= flagEncrypted
			key = hashString(baseName(f.Name), 3)
		}
		n := (len(f.Data) + buildSector - 1) / buildSector
		var sectors [][]byte
		for k := 0; k < n; k++ {
			s := f.Data[k*buildSector : min((k+1)*buildSector, len(f.Data))]
			if !f.Raw {
				var z bytes.Buffer
				z.WriteByte(0x02)
				w := zlib.NewWriter(&z)
				w.Write(s)
				w.Close()
				if z.Len() < len(s) {
					s = z.Bytes()
				}
			}
			sectors = append(sectors, append([]byte(nil), s...))
		}
		var data bytes.Buffer
		if !f.Raw {
			blocks[i].flags |= flagCompress
			offs := make([]uint32, n+1)
			off := uint32((n + 1) * 4)
			for k, s := range sectors {
				offs[k] = off
				off += uint32(len(s))
			}
			offs[n] = off
			if f.Encrypt {
				encryptBlock(offs, key-1)
			}
			for _, o := range offs {
				binary.Write(&data, binary.LittleEndian, o)
			}
		}
		for k, s := range sectors {
			if f.Encrypt {
				encryptBytes(s, key+uint32(k)) // whole words only; a short tail stays plain, as in StormLib
			}
			data.Write(s)
		}
		blocks[i].csize = uint32(data.Len())
		body.Write(data.Bytes())
	}
	hashPos := body.Len()
	hn := 1
	for hn < 2*len(files) || hn < 4 {
		hn <<= 1
	}
	hash := make([]hashEntry, hn)
	for i := range hash {
		hash[i] = hashEntry{block: hashFree, nameA: 0xFFFFFFFF, nameB: 0xFFFFFFFF, locale: 0xFFFF, platform: 0xFFFF}
	}
	order := make([]int, len(files))
	for i := range order {
		order[i] = i
	}
	sort.Ints(order)
	for _, i := range order {
		name := files[i].Name
		slot := hashString(name, 0) & uint32(hn-1)
		for hash[slot].block != hashFree {
			slot = (slot + 1) & uint32(hn-1)
		}
		hash[slot] = hashEntry{nameA: hashString(name, 1), nameB: hashString(name, 2), block: uint32(i)}
	}
	hw := make([]uint32, hn*4)
	for i, h := range hash {
		hw[i*4], hw[i*4+1], hw[i*4+2], hw[i*4+3] = h.nameA, h.nameB, uint32(h.locale)|uint32(h.platform)<<16, h.block
	}
	encryptBlock(hw, hashString("(hash table)", 3))
	for _, w := range hw {
		binary.Write(&body, binary.LittleEndian, w)
	}
	blockPos := body.Len()
	bw := make([]uint32, len(blocks)*4)
	for i, b := range blocks {
		bw[i*4], bw[i*4+1], bw[i*4+2], bw[i*4+3] = b.pos, b.csize, b.size, b.flags
	}
	encryptBlock(bw, hashString("(block table)", 3))
	for _, w := range bw {
		binary.Write(&body, binary.LittleEndian, w)
	}
	out := body.Bytes()
	copy(out, "MPQ\x1a")
	binary.LittleEndian.PutUint32(out[4:], 32)
	binary.LittleEndian.PutUint32(out[8:], uint32(len(out)))
	binary.LittleEndian.PutUint16(out[12:], 0)
	binary.LittleEndian.PutUint16(out[14:], 3)
	binary.LittleEndian.PutUint32(out[16:], uint32(hashPos))
	binary.LittleEndian.PutUint32(out[20:], uint32(blockPos))
	binary.LittleEndian.PutUint32(out[24:], uint32(hn))
	binary.LittleEndian.PutUint32(out[28:], uint32(len(blocks)))
	return out
}

func encryptBlock(data []uint32, key uint32) {
	seed := uint32(0xEEEEEEEE)
	for i := range data {
		seed += cryptTable[0x400+(key&0xFF)]
		ch := data[i]
		data[i] = ch ^ (key + seed)
		key = ((^key << 0x15) + 0x11111111) | (key >> 0x0B)
		seed = ch + seed + (seed << 5) + 3
	}
}

func encryptBytes(b []byte, key uint32) {
	w := make([]uint32, len(b)/4)
	for i := range w {
		w[i] = binary.LittleEndian.Uint32(b[i*4:])
	}
	encryptBlock(w, key)
	for i := range w {
		binary.LittleEndian.PutUint32(b[i*4:], w[i])
	}
}
