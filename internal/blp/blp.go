// Package blp decodes the BLP2 textures of the 1.12 client (palettized, DXT1, DXT3, DXT5 and raw BGRA) into images.
package blp

import (
	"encoding/binary"
	"errors"
	"fmt"
	"image"
	"image/color"
)

const headerSize = 148 + 1024

// Decode returns the first (largest) mip level of a BLP2 file.
func Decode(data []byte) (*image.NRGBA, error) {
	if len(data) < headerSize || string(data[:4]) != "BLP2" {
		return nil, errors.New("blp: not a BLP2 file")
	}
	le := binary.LittleEndian
	if le.Uint32(data[4:]) != 1 {
		return nil, errors.New("blp: JPEG-compressed BLP is not supported")
	}
	enc, alphaDepth, alphaEnc := data[8], data[9], data[10]
	w, h := int(le.Uint32(data[12:])), int(le.Uint32(data[16:]))
	off, size := int(le.Uint32(data[20:])), int(le.Uint32(data[84:]))
	if w <= 0 || h <= 0 || w > 8192 || h > 8192 {
		return nil, fmt.Errorf("blp: implausible size %dx%d", w, h)
	}
	if off < 0 || size < 0 || off > len(data) || off+size > len(data) {
		return nil, errors.New("blp: mip 0 lies outside the file")
	}
	px := data[off : off+size]
	img := image.NewNRGBA(image.Rect(0, 0, w, h))
	switch enc {
	case 1:
		return img, decodePalette(img, data[148:headerSize], px, alphaDepth)
	case 2:
		switch alphaEnc {
		case 0:
			return img, decodeDXT(img, px, 1)
		case 1:
			return img, decodeDXT(img, px, 3)
		case 7:
			return img, decodeDXT(img, px, 5)
		}
		return nil, fmt.Errorf("blp: DXT alpha encoding %d", alphaEnc)
	case 3:
		if len(px) < w*h*4 {
			return nil, errors.New("blp: short BGRA data")
		}
		for i := 0; i < w*h; i++ {
			img.Pix[i*4], img.Pix[i*4+1], img.Pix[i*4+2], img.Pix[i*4+3] = px[i*4+2], px[i*4+1], px[i*4], px[i*4+3]
		}
		return img, nil
	}
	return nil, fmt.Errorf("blp: encoding %d", enc)
}

func decodePalette(img *image.NRGBA, pal, px []byte, alphaDepth byte) error {
	w, h := img.Rect.Dx(), img.Rect.Dy()
	n := w * h
	if len(px) < n {
		return errors.New("blp: short palette data")
	}
	alpha := px[n:]
	for i := 0; i < n; i++ {
		p := pal[int(px[i])*4:]
		a := byte(255)
		switch alphaDepth {
		case 1:
			if i/8 < len(alpha) {
				a = (alpha[i/8] >> (uint(i) % 8) & 1) * 255
			}
		case 4:
			if i/2 < len(alpha) {
				a = alpha[i/2] >> (uint(i) % 2 * 4) & 15 * 17
			}
		case 8:
			if i < len(alpha) {
				a = alpha[i]
			}
		}
		img.Pix[i*4], img.Pix[i*4+1], img.Pix[i*4+2], img.Pix[i*4+3] = p[2], p[1], p[0], a
	}
	return nil
}

func rgb565(v uint16) color.NRGBA {
	r, g, b := uint32(v>>11)&31, uint32(v>>5)&63, uint32(v)&31
	return color.NRGBA{uint8(r<<3 | r>>2), uint8(g<<2 | g>>4), uint8(b<<3 | b>>2), 255}
}

func decodeDXT(img *image.NRGBA, px []byte, kind int) error {
	w, h := img.Rect.Dx(), img.Rect.Dy()
	bw, bh := (w+3)/4, (h+3)/4
	bs := 8
	if kind != 1 {
		bs = 16
	}
	if len(px) < bw*bh*bs {
		return errors.New("blp: short DXT data")
	}
	le := binary.LittleEndian
	for by := 0; by < bh; by++ {
		for bx := 0; bx < bw; bx++ {
			blk := px[(by*bw+bx)*bs:]
			var alpha [16]uint8
			for i := range alpha {
				alpha[i] = 255
			}
			col := blk
			switch kind {
			case 3:
				for i := 0; i < 16; i++ {
					alpha[i] = blk[i/2] >> (uint(i) % 2 * 4) & 15 * 17
				}
				col = blk[8:]
			case 5:
				a0, a1 := blk[0], blk[1]
				var tab [8]uint8
				tab[0], tab[1] = a0, a1
				if a0 > a1 {
					for i := 1; i < 7; i++ {
						tab[i+1] = uint8((int(a0)*(7-i) + int(a1)*i) / 7)
					}
				} else {
					for i := 1; i < 5; i++ {
						tab[i+1] = uint8((int(a0)*(5-i) + int(a1)*i) / 5)
					}
					tab[6], tab[7] = 0, 255
				}
				bits := uint64(0)
				for i := 0; i < 6; i++ {
					bits |= uint64(blk[2+i]) << (8 * uint(i))
				}
				for i := 0; i < 16; i++ {
					alpha[i] = tab[bits>>(3*uint(i))&7]
				}
				col = blk[8:]
			}
			c0v, c1v := le.Uint16(col), le.Uint16(col[2:])
			var pal [4]color.NRGBA
			pal[0], pal[1] = rgb565(c0v), rgb565(c1v)
			if c0v > c1v || kind != 1 {
				ch := func(a, b uint8, wa, wb int) uint8 { return uint8((int(a)*wa + int(b)*wb) / 3) }
				pal[2] = color.NRGBA{ch(pal[0].R, pal[1].R, 2, 1), ch(pal[0].G, pal[1].G, 2, 1), ch(pal[0].B, pal[1].B, 2, 1), 255}
				pal[3] = color.NRGBA{ch(pal[0].R, pal[1].R, 1, 2), ch(pal[0].G, pal[1].G, 1, 2), ch(pal[0].B, pal[1].B, 1, 2), 255}
			} else {
				pal[2] = color.NRGBA{uint8((int(pal[0].R) + int(pal[1].R)) / 2), uint8((int(pal[0].G) + int(pal[1].G)) / 2), uint8((int(pal[0].B) + int(pal[1].B)) / 2), 255}
				pal[3] = color.NRGBA{0, 0, 0, 0}
			}
			idx := le.Uint32(col[4:])
			for i := 0; i < 16; i++ {
				x, y := bx*4+i%4, by*4+i/4
				if x >= w || y >= h {
					continue
				}
				c := pal[idx>>(2*uint(i))&3]
				if kind != 1 {
					c.A = alpha[i]
				}
				img.SetNRGBA(x, y, c)
			}
		}
	}
	return nil
}
