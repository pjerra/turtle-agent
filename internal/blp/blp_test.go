package blp

import (
	"encoding/binary"
	"image/color"
	"testing"
)

// file builds a BLP2 with one mip level of w x h.
func file(enc, alphaDepth, alphaEnc byte, w, h int, pal []byte, px []byte) []byte {
	b := make([]byte, headerSize)
	copy(b, "BLP2")
	binary.LittleEndian.PutUint32(b[4:], 1)
	b[8], b[9], b[10], b[11] = enc, alphaDepth, alphaEnc, 0
	binary.LittleEndian.PutUint32(b[12:], uint32(w))
	binary.LittleEndian.PutUint32(b[16:], uint32(h))
	binary.LittleEndian.PutUint32(b[20:], headerSize)
	binary.LittleEndian.PutUint32(b[84:], uint32(len(px)))
	copy(b[148:], pal)
	return append(b, px...)
}

func at(t *testing.T, data []byte, x, y int) color.NRGBA {
	t.Helper()
	img, err := Decode(data)
	if err != nil {
		t.Fatal(err)
	}
	return img.NRGBAAt(x, y)
}

func TestPalette(t *testing.T) {
	pal := make([]byte, 1024)
	copy(pal[4:], []byte{10, 20, 30, 255}) // entry 1, stored B G R A
	data := file(1, 8, 0, 2, 1, pal, []byte{0, 1, 0, 77})
	img, err := Decode(data)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := img.NRGBAAt(1, 0), (color.NRGBA{30, 20, 10, 77}); got != want {
		t.Errorf("pixel = %v, want %v (BGR palette, 8-bit alpha)", got, want)
	}
	// 1-bit alpha: pixel 0 set, pixel 1 clear
	img, _ = Decode(file(1, 1, 0, 2, 1, pal, []byte{1, 1, 0b01}))
	if a, b := img.NRGBAAt(0, 0).A, img.NRGBAAt(1, 0).A; a != 255 || b != 0 {
		t.Errorf("1-bit alpha = %d %d, want 255 0", a, b)
	}
	// 4-bit alpha
	img, _ = Decode(file(1, 4, 0, 2, 1, pal, []byte{1, 1, 0xA5}))
	if a, b := img.NRGBAAt(0, 0).A, img.NRGBAAt(1, 0).A; a != 5*17 || b != 10*17 {
		t.Errorf("4-bit alpha = %d %d, want 85 170", a, b)
	}
}

func TestDXT1(t *testing.T) {
	// colour0 = pure red (565 0xF800), colour1 = pure blue (0x001F); c0 > c1 so four colours; indices: row 0 = 0,1,2,3.
	blk := []byte{0x00, 0xF8, 0x1F, 0x00, 0b11100100, 0, 0, 0}
	data := file(2, 0, 0, 4, 4, nil, blk)
	if got := at(t, data, 0, 0); got != (color.NRGBA{255, 0, 0, 255}) {
		t.Errorf("index 0 = %v, want red", got)
	}
	if got := at(t, data, 1, 0); got != (color.NRGBA{0, 0, 255, 255}) {
		t.Errorf("index 1 = %v, want blue", got)
	}
	if got := at(t, data, 2, 0); got != (color.NRGBA{170, 0, 85, 255}) {
		t.Errorf("index 2 = %v, want 2/3 red + 1/3 blue", got)
	}
	if got := at(t, data, 3, 0); got != (color.NRGBA{85, 0, 170, 255}) {
		t.Errorf("index 3 = %v, want 1/3 red + 2/3 blue", got)
	}
	// c0 <= c1: three colours and transparent black as index 3
	blk = []byte{0x1F, 0x00, 0x00, 0xF8, 0b11100100, 0, 0, 0}
	data = file(2, 0, 0, 4, 4, nil, blk)
	if got := at(t, data, 2, 0); got != (color.NRGBA{127, 0, 127, 255}) {
		t.Errorf("midpoint = %v", got)
	}
	if got := at(t, data, 3, 0); got.A != 0 {
		t.Errorf("index 3 of a 3-colour block = %v, want transparent", got)
	}
}

func TestDXT3Alpha(t *testing.T) {
	blk := append([]byte{0x21, 0x00, 0, 0, 0, 0, 0, 0}, 0x00, 0xF8, 0x00, 0xF8, 0, 0, 0, 0) // alpha nibbles 1, 2, 0...; colour red
	data := file(2, 8, 1, 4, 4, nil, blk)
	if a := at(t, data, 0, 0).A; a != 17 {
		t.Errorf("alpha of pixel 0 = %d, want 17", a)
	}
	if a := at(t, data, 1, 0).A; a != 34 {
		t.Errorf("alpha of pixel 1 = %d, want 34", a)
	}
}

func TestDXT5Alpha(t *testing.T) {
	// alpha0 = 255, alpha1 = 0 (alpha0 > alpha1: 8 interpolated values); indices: pixel 0 -> 0, pixel 1 -> 1, pixel 2 -> 2.
	idx := uint64(0) | 1<<3 | 2<<6
	blk := []byte{255, 0}
	for i := 0; i < 6; i++ {
		blk = append(blk, byte(idx>>(8*uint(i))))
	}
	blk = append(blk, 0x00, 0xF8, 0x00, 0xF8, 0, 0, 0, 0)
	data := file(2, 8, 7, 4, 4, nil, blk)
	for x, want := range []uint8{255, 0, 218} { // 6/7*255 = 218
		if got := at(t, data, x, 0).A; got != want {
			t.Errorf("alpha of pixel %d = %d, want %d", x, got, want)
		}
	}
}

func TestBGRA(t *testing.T) {
	data := file(3, 8, 0, 1, 1, nil, []byte{1, 2, 3, 4})
	if got := at(t, data, 0, 0); got != (color.NRGBA{3, 2, 1, 4}) {
		t.Errorf("pixel = %v", got)
	}
}

func TestBadFiles(t *testing.T) {
	good := file(1, 0, 0, 2, 2, make([]byte, 1024), []byte{0, 0, 0, 0})
	for name, b := range map[string][]byte{
		"short":      good[:100],
		"magic":      append([]byte("BLP1"), good[4:]...),
		"truncated":  good[:len(good)-2],
		"empty mips": file(1, 0, 0, 2, 2, nil, nil),
	} {
		if _, err := Decode(b); err == nil {
			t.Errorf("%s: no error", name)
		}
	}
}
