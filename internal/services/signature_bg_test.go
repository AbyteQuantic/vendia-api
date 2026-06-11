// Spec: specs/042-modulo-eventos/spec.md
package services

import (
	"bytes"
	"image"
	"image/color"
	"image/png"
	"testing"
)

// buildSignatureSample paints a black stroke on a white card, the exact shape
// Gemini returns (opaque white background, dark ink), so we can assert the
// background-removal pass.
func buildSignatureSample() []byte {
	img := image.NewRGBA(image.Rect(0, 0, 20, 20))
	for y := 0; y < 20; y++ {
		for x := 0; x < 20; x++ {
			img.Set(x, y, color.RGBA{R: 255, G: 255, B: 255, A: 255}) // white card
		}
	}
	// A solid dark diagonal "stroke".
	for i := 2; i < 18; i++ {
		img.Set(i, i, color.RGBA{R: 10, G: 10, B: 30, A: 255})
	}
	var buf bytes.Buffer
	_ = png.Encode(&buf, img)
	return buf.Bytes()
}

func TestMakeSignatureTransparent_BackgroundBecomesTransparent(t *testing.T) {
	out, err := MakeSignatureTransparent(buildSignatureSample())
	if err != nil {
		t.Fatalf("MakeSignatureTransparent: %v", err)
	}
	decoded, err := png.Decode(bytes.NewReader(out))
	if err != nil {
		t.Fatalf("decode result: %v", err)
	}

	// A white-background pixel must be fully transparent.
	_, _, _, aBg := decoded.At(0, 0).RGBA()
	if aBg != 0 {
		t.Errorf("background pixel alpha = %d, want 0 (transparent)", aBg>>8)
	}

	// A pixel on the dark stroke must stay opaque.
	_, _, _, aInk := decoded.At(10, 10).RGBA()
	if aInk>>8 < 250 {
		t.Errorf("ink pixel alpha = %d, want ~255 (opaque)", aInk>>8)
	}
}

func TestMakeSignatureTransparent_RejectsGarbage(t *testing.T) {
	if _, err := MakeSignatureTransparent([]byte("not an image")); err == nil {
		t.Error("expected error decoding non-image bytes, got nil")
	}
}
