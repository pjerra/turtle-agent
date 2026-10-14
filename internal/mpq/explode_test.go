package mpq

import (
	"bytes"
	"testing"
)

// bitWriter writes the LSB-first stream of a PKWARE DCL file.
type bitWriter struct {
	out  []byte
	cur  byte
	nbit uint
}

func (w *bitWriter) bit(b int) {
	w.cur |= byte(b&1) << w.nbit
	if w.nbit++; w.nbit == 8 {
		w.out = append(w.out, w.cur)
		w.cur, w.nbit = 0, 0
	}
}

func (w *bitWriter) bits(v int, n uint) {
	for i := uint(0); i < n; i++ {
		w.bit(v >> i)
	}
}

func (w *bitWriter) flush() []byte {
	if w.nbit > 0 {
		w.out = append(w.out, w.cur)
	}
	return w.out
}

// code finds the bits (as the decoder reads them) of symbol sym in h by search: the decoder inverts each bit.
func code(t *testing.T, h *huffman, sym int) []int {
	t.Helper()
	for n := 1; n <= maxBits; n++ {
		for v := 0; v < 1<<n; v++ {
			var bs []int
			for i := 0; i < n; i++ {
				bs = append(bs, v>>i&1)
			}
			r := &bitReader{in: pack(bs)}
			if got, err := r.decode(h); err == nil && got == sym && int(r.pos)*8-int(r.cnt) == n {
				return bs
			}
		}
	}
	t.Fatalf("no code for symbol %d", sym)
	return nil
}

func pack(bs []int) []byte {
	w := &bitWriter{}
	for _, b := range bs {
		w.bit(b)
	}
	return append(w.flush(), 0, 0, 0)
}

func TestExplodeTablesAreCompleteCodes(t *testing.T) {
	// init would have panicked otherwise; check the counts that follow from the tables too.
	for name, h := range map[string]*huffman{"length": lenCode, "distance": distCode} {
		n := 0
		for _, c := range h.count[1:] {
			n += c
		}
		if want := map[string]int{"length": 16, "distance": 64}[name]; n != want {
			t.Errorf("%s code has %d symbols, want %d", name, n, want)
		}
	}
}

func TestExplodeLiteralsAndMatch(t *testing.T) {
	w := &bitWriter{}
	emit := func(bs []int) {
		for _, b := range bs {
			w.bit(b)
		}
	}
	for _, c := range []byte("abcabc") {
		w.bit(0)
		w.bits(int(c), 8)
	}
	// a match of length 6 (lenBase[4], no extra bits) at distance 3, dictionary 4 bits.
	sym := 4 // lenBase[4] = 6
	w.bit(1)
	emit(code(t, lenCode, sym))
	w.bits(0, uint(lenExtra[sym]))
	dist := 3 - 1
	emit(code(t, distCode, dist>>4))
	w.bits(dist&15, 4)
	// end marker: length symbol 15 with 8 extra bits = 255 (264 + 255 = 519)
	w.bit(1)
	emit(code(t, lenCode, 15))
	w.bits(255, 8)
	stream := append([]byte{0, 4}, w.flush()...)
	got, err := explode(stream, 100)
	if err != nil {
		t.Fatal(err)
	}
	if want := []byte("abcabcabcabc"); !bytes.Equal(got, want) {
		t.Errorf("explode = %q, want %q", got, want)
	}
}

func TestExplodeRejectsBadStreams(t *testing.T) {
	for name, in := range map[string][]byte{
		"empty":        nil,
		"ascii mode":   {1, 4, 0},
		"bad dict":     {0, 9, 0},
		"truncated":    {0, 4, 0xff},
		"far distance": {0, 4, 0x03, 0x00, 0x00},
	} {
		if _, err := explode(in, 10); err == nil {
			t.Errorf("%s: no error", name)
		}
	}
}
