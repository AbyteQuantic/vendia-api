// Spec: specs/042-modulo-eventos/spec.md
package services

import (
	"bytes"
	"image"
	"image/color"
	"image/png"
	"testing"
)

// buildLogoFixture paints a white card with a solid red square in the middle and
// a small WHITE hole inside that square. It exercises the three things that
// matter: a border background to erase, colored content to keep, and an interior
// white that must survive (the whole point vs. MakeSignatureTransparent).
func buildLogoFixture() []byte {
	const size = 24
	img := image.NewNRGBA(image.Rect(0, 0, size, size))
	white := color.NRGBA{R: 255, G: 255, B: 255, A: 255}
	red := color.NRGBA{R: 220, G: 30, B: 30, A: 255}

	for y := 0; y < size; y++ {
		for x := 0; x < size; x++ {
			img.SetNRGBA(x, y, white) // white card everywhere
		}
	}
	// Red square 8..15.
	for y := 8; y <= 15; y++ {
		for x := 8; x <= 15; x++ {
			img.SetNRGBA(x, y, red)
		}
	}
	// Interior white hole 11..12, fully enclosed by red.
	for y := 11; y <= 12; y++ {
		for x := 11; x <= 12; x++ {
			img.SetNRGBA(x, y, white)
		}
	}

	var buf bytes.Buffer
	_ = png.Encode(&buf, img)
	return buf.Bytes()
}

func TestRemoveLogoBackground_KeepsColorsAndInteriorWhite(t *testing.T) {
	out, err := RemoveLogoBackground(buildLogoFixture())
	if err != nil {
		t.Fatalf("RemoveLogoBackground: %v", err)
	}

	decoded, err := png.Decode(bytes.NewReader(out))
	if err != nil {
		t.Fatalf("decode result: %v", err)
	}
	b := decoded.Bounds()

	// Corner must be transparent: the border background was erased.
	if _, _, _, a := decoded.At(b.Min.X, b.Min.Y).RGBA(); a != 0 {
		t.Errorf("esperaba esquina transparente, alpha=%d", a>>8)
	}

	var foundRed, foundInteriorWhite bool
	for y := b.Min.Y; y < b.Max.Y; y++ {
		for x := b.Min.X; x < b.Max.X; x++ {
			r, g, bl, a := decoded.At(x, y).RGBA()
			if a>>8 < 200 {
				continue // skip transparent / feathered edge pixels
			}
			r8, g8, b8 := r>>8, g>>8, bl>>8
			if r8 > 150 && g8 < 100 && b8 < 100 {
				foundRed = true // the logo's red survived with its hue
			}
			if r8 > 220 && g8 > 220 && b8 > 220 {
				foundInteriorWhite = true // the enclosed white survived
			}
		}
	}
	if !foundRed {
		t.Error("se perdió el color del logo (rojo) tras quitar el fondo")
	}
	if !foundInteriorWhite {
		t.Error("se borró el blanco INTERIOR del logo (debía preservarse)")
	}
}

func TestRemoveLogoBackground_AllWhiteFails(t *testing.T) {
	img := image.NewNRGBA(image.Rect(0, 0, 16, 16))
	for y := 0; y < 16; y++ {
		for x := 0; x < 16; x++ {
			img.SetNRGBA(x, y, color.NRGBA{R: 255, G: 255, B: 255, A: 255})
		}
	}
	var buf bytes.Buffer
	_ = png.Encode(&buf, img)

	if _, err := RemoveLogoBackground(buf.Bytes()); err == nil {
		t.Error("una imagen toda blanca debería fallar (no queda logo)")
	}
}

func TestRemoveLogoBackground_RejectsGarbage(t *testing.T) {
	if _, err := RemoveLogoBackground([]byte("not an image")); err == nil {
		t.Error("esperaba error al decodificar bytes inválidos")
	}
}
