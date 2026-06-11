// Spec: specs/042-modulo-eventos/spec.md
package services

import (
	"bytes"
	"fmt"
	"image"
	"image/color"
	_ "image/jpeg" // decode JPEG inputs
	"image/png"
)

// MakeSignatureTransparent turns a dark-ink-on-white-background signature into a
// true transparent PNG. Gemini cannot emit an alpha channel — it always returns
// the cleaned signature on a solid white card — so we knock the background out
// here: near-white pixels become fully transparent, dark strokes stay opaque,
// and the in-between (anti-aliased) edges get a smooth alpha ramp. The result is
// a PNG with real transparency that composites cleanly on any certificate
// background, no blend tricks needed (FR-12, screenshot IMG_4096).
//
// To avoid the classic white halo at stroke edges, every visible pixel is
// painted with the signature's dominant ink color and only its alpha varies.
func MakeSignatureTransparent(data []byte) ([]byte, error) {
	src, _, err := image.Decode(bytes.NewReader(data))
	if err != nil {
		return nil, fmt.Errorf("decodificar firma: %w", err)
	}

	b := src.Bounds()
	ink := dominantInkColor(src)

	// Alpha ramp: pixels lighter than whiteAt vanish, darker than inkAt are
	// solid, the rest interpolate. Tuned for ink on a white/cream card.
	const whiteAt = 238.0
	const inkAt = 110.0

	out := image.NewNRGBA(b)
	for y := b.Min.Y; y < b.Max.Y; y++ {
		for x := b.Min.X; x < b.Max.X; x++ {
			lum := luminance(src.At(x, y))
			var a float64
			switch {
			case lum >= whiteAt:
				a = 0
			case lum <= inkAt:
				a = 255
			default:
				a = (whiteAt - lum) / (whiteAt - inkAt) * 255
			}
			if a <= 0 {
				continue // leave fully transparent
			}
			out.SetNRGBA(x, y, color.NRGBA{R: ink.R, G: ink.G, B: ink.B, A: uint8(a)})
		}
	}

	var buf bytes.Buffer
	if err := png.Encode(&buf, out); err != nil {
		return nil, fmt.Errorf("codificar firma PNG: %w", err)
	}
	return buf.Bytes(), nil
}

// luminance returns the perceived brightness (0..255) of a pixel.
func luminance(c color.Color) float64 {
	r, g, b, _ := c.RGBA() // 16-bit
	rf := float64(r>>8)
	gf := float64(g>>8)
	bf := float64(b>>8)
	return 0.299*rf + 0.587*gf + 0.114*bf
}

// dominantInkColor averages the dark pixels (the strokes) so blue or black pens
// keep their hue. Falls back to near-black when nothing dark is found.
func dominantInkColor(img image.Image) color.NRGBA {
	b := img.Bounds()
	var sr, sg, sb, n uint64
	for y := b.Min.Y; y < b.Max.Y; y++ {
		for x := b.Min.X; x < b.Max.X; x++ {
			if luminance(img.At(x, y)) > 120 {
				continue
			}
			r, g, bl, _ := img.At(x, y).RGBA()
			sr += uint64(r >> 8)
			sg += uint64(g >> 8)
			sb += uint64(bl >> 8)
			n++
		}
	}
	if n == 0 {
		return color.NRGBA{R: 20, G: 20, B: 20, A: 255}
	}
	return color.NRGBA{
		R: uint8(sr / n),
		G: uint8(sg / n),
		B: uint8(sb / n),
		A: 255,
	}
}
