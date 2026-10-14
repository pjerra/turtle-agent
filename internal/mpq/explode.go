package mpq

import (
	"errors"
)

// PKWARE DCL "implode" decoder (binary mode), after the public-domain structure of zlib's blast.c.

const maxBits = 13

type huffman struct {
	count  [maxBits + 1]int
	symbol [256]int
}

var (
	lenLen   = []byte{2, 35, 36, 53, 38, 23}
	distLen  = []byte{2, 20, 53, 230, 247, 151, 248}
	lenBase  = [16]int{3, 2, 4, 5, 6, 7, 8, 9, 10, 12, 16, 24, 40, 72, 136, 264}
	lenExtra = [16]int{0, 0, 0, 0, 0, 0, 0, 0, 1, 2, 3, 4, 5, 6, 7, 8}

	lenCode, distCode = mustConstruct(lenLen), mustConstruct(distLen)
)

// construct builds a canonical Huffman decoder from run-length-coded code lengths; the second result is the number of
// unused codes (0 for a complete code).
func construct(rep []byte) (*huffman, int) {
	h := &huffman{}
	var length [256]int
	sym := 0
	for _, r := range rep {
		l := int(r & 15)
		for left := int(r>>4) + 1; left > 0; left-- {
			length[sym] = l
			sym++
		}
	}
	n := sym
	for s := 0; s < n; s++ {
		h.count[length[s]]++
	}
	left := 1
	for l := 1; l <= maxBits; l++ {
		left <<= 1
		left -= h.count[l]
		if left < 0 {
			return h, left
		}
	}
	var offs [maxBits + 2]int
	for l := 1; l < maxBits; l++ {
		offs[l+1] = offs[l] + h.count[l]
	}
	for s := 0; s < n; s++ {
		if length[s] != 0 {
			h.symbol[offs[length[s]]] = s
			offs[length[s]]++
		}
	}
	return h, left
}

func mustConstruct(rep []byte) *huffman {
	h, left := construct(rep)
	if left != 0 {
		panic("mpq: PKWARE table is not a complete code")
	}
	return h
}

type bitReader struct {
	in  []byte
	pos int
	buf uint32
	cnt uint
}

var errShort = errors.New("truncated implode stream")

func (r *bitReader) bits(need uint) (int, error) {
	val := r.buf
	for r.cnt < need {
		if r.pos >= len(r.in) {
			return 0, errShort
		}
		val |= uint32(r.in[r.pos]) << r.cnt
		r.pos++
		r.cnt += 8
	}
	r.buf = val >> need
	r.cnt -= need
	return int(val & (1<<need - 1)), nil
}

func (r *bitReader) decode(h *huffman) (int, error) {
	code, first, index := 0, 0, 0
	for l := 1; l <= maxBits; l++ {
		b, err := r.bits(1)
		if err != nil {
			return 0, err
		}
		code |= b ^ 1
		count := h.count[l]
		if code < first+count {
			return h.symbol[index+(code-first)], nil
		}
		index += count
		first = (first + count) << 1
		code <<= 1
	}
	return 0, errors.New("bad implode code")
}

// explode decompresses a PKWARE DCL stream (binary mode only: Blizzard's archives do not use ASCII mode).
func explode(in []byte, want int) ([]byte, error) {
	if len(in) < 2 {
		return nil, errShort
	}
	if in[0] != 0 {
		return nil, errors.New("ASCII-mode implode is not supported")
	}
	dict := uint(in[1])
	if dict < 4 || dict > 6 {
		return nil, errors.New("bad implode dictionary size")
	}
	r := &bitReader{in: in, pos: 2}
	out := make([]byte, 0, want)
	for {
		isMatch, err := r.bits(1)
		if err != nil {
			return nil, err
		}
		if isMatch == 0 {
			lit, err := r.bits(8)
			if err != nil {
				return nil, err
			}
			out = append(out, byte(lit))
		} else {
			sym, err := r.decode(lenCode)
			if err != nil {
				return nil, err
			}
			extra, err := r.bits(uint(lenExtra[sym]))
			if err != nil {
				return nil, err
			}
			n := lenBase[sym] + extra
			if n == 519 {
				break
			}
			shift := dict
			if n == 2 {
				shift = 2
			}
			ds, err := r.decode(distCode)
			if err != nil {
				return nil, err
			}
			low, err := r.bits(shift)
			if err != nil {
				return nil, err
			}
			dist := ds<<shift + low + 1
			if dist > len(out) {
				return nil, errors.New("implode distance too far back")
			}
			for ; n > 0; n-- {
				out = append(out, out[len(out)-dist])
			}
		}
		if len(out) > want+520 {
			return nil, errors.New("implode output too long")
		}
	}
	if len(out) > want {
		out = out[:want]
	}
	return out, nil
}
