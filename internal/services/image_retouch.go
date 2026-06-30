// Spec: specs/094-foto-fiel-fondo-realce/spec.md
//
// Composición de estudio sobre el RECORTE REAL del producto (PNG con transparencia que
// devuelve remove.bg). Todo opera sobre los PÍXELES REALES — nunca re-dibuja el
// producto. La escena (fondo degradado + sombra + reflejo) va alrededor.
package services

import (
	"bytes"
	"fmt"
	"image"
	"image/color"
	_ "image/jpeg"
	"image/jpeg"
	_ "image/png"

	"github.com/disintegration/imaging"
)

// studioBackdrop: fondo de estudio (degradado vertical sutil blanco→gris claro).
func studioBackdrop(side int) *image.NRGBA {
	c := imaging.New(side, side, color.NRGBA{255, 255, 255, 255})
	for y := 0; y < side; y++ {
		t := float64(y) / float64(side)
		t2 := t * t
		r := uint8(255 - t2*19)
		g := uint8(255 - t2*17)
		b := uint8(255 - t2*14)
		for x := 0; x < side; x++ {
			c.SetNRGBA(x, y, color.NRGBA{r, g, b, 255})
		}
	}
	return c
}

// makeReflection: reflejo (espejo vertical) del producto que se desvanece — look catálogo.
func makeReflection(prod *image.NRGBA, maxAlpha, heightFrac float64) *image.NRGBA {
	flipped := imaging.FlipV(prod)
	b := flipped.Bounds()
	w, h := b.Dx(), b.Dy()
	rh := int(float64(h) * heightFrac)
	if rh < 1 {
		rh = 1
	}
	out := imaging.New(w, rh, color.NRGBA{0, 0, 0, 0})
	for y := 0; y < rh; y++ {
		fade := maxAlpha * (1 - float64(y)/float64(rh))
		for x := 0; x < w; x++ {
			pc := flipped.NRGBAAt(x, y)
			if pc.A == 0 {
				continue
			}
			out.SetNRGBA(x, y, color.NRGBA{pc.R, pc.G, pc.B, uint8(float64(pc.A) * fade)})
		}
	}
	return out
}

// realceCutout: realce SUAVE sobre el recorte (sin nitidez, para no crear halo en el
// borde con alfa). Mejora luz/color sin cambiar el producto. Conserva el alfa.
func realceCutout(img image.Image) *image.NRGBA {
	out := imaging.AdjustContrast(img, 6)
	out = imaging.AdjustBrightness(out, 2)
	out = imaging.AdjustSaturation(out, 4)
	return out
}

// ComposeStudioFromCutout toma el PNG recortado (producto real con transparencia) y lo
// monta en una escena de estudio: fondo + sombra de contacto + reflejo, centrado y
// cuadrado. Devuelve JPEG (R2 lo pasa a WebP). El producto NO se altera.
func ComposeStudioFromCutout(cutoutPNG []byte) ([]byte, error) {
	decoded, err := imaging.Decode(bytes.NewReader(cutoutPNG))
	if err != nil {
		return nil, fmt.Errorf("no se pudo decodificar el recorte: %w", err)
	}
	prod := realceCutout(decoded)

	// Bounding box por el ALFA real (recorte limpio de remove.bg).
	b := prod.Bounds()
	w, h := b.Dx(), b.Dy()
	minX, minY, maxX, maxY := w, h, -1, -1
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			if prod.NRGBAAt(x, y).A > 20 {
				if x < minX {
					minX = x
				}
				if y < minY {
					minY = y
				}
				if x > maxX {
					maxX = x
				}
				if y > maxY {
					maxY = y
				}
			}
		}
	}
	if maxX < minX || maxY < minY {
		return nil, fmt.Errorf("recorte vacío")
	}

	prodCrop := imaging.Crop(prod, image.Rect(minX, minY, maxX+1, maxY+1))
	bw, bh := maxX-minX+1, maxY-minY+1

	reflH := int(float64(bh) * 0.45)
	gap := int(float64(bh) * 0.015)
	if gap < 1 {
		gap = 1
	}
	contentH := bh + gap + reflH
	const margin = 0.12
	frac := 1 - 2*margin
	side := int(float64(max(bw, contentH)) / frac)
	if side < 1 {
		side = max(bw, contentH)
	}

	canvas := studioBackdrop(side)
	offX := (side - bw) / 2
	top := (side - contentH) / 2
	if top < 0 {
		top = 0
	}

	// Sombra de contacto (silueta difuminada) bajo el producto.
	shadow := imaging.New(bw, bh, color.NRGBA{0, 0, 0, 0})
	for y := 0; y < bh; y++ {
		for x := 0; x < bw; x++ {
			if prodCrop.NRGBAAt(x, y).A > 40 {
				shadow.SetNRGBA(x, y, color.NRGBA{30, 30, 35, 255})
			}
		}
	}
	shadow = imaging.Blur(shadow, float64(side)*0.018+1)
	canvas = imaging.Overlay(canvas, shadow,
		image.Pt(offX, top+int(float64(bh)*0.03)), 0.22)

	// Reflejo sutil debajo.
	refl := makeReflection(prodCrop, 0.22, 0.45)
	canvas = imaging.Overlay(canvas, refl, image.Pt(offX, top+bh+gap), 1.0)

	// Producto real encima.
	canvas = imaging.Overlay(canvas, prodCrop, image.Pt(offX, top), 1.0)

	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, canvas, &jpeg.Options{Quality: 92}); err != nil {
		return nil, fmt.Errorf("no se pudo codificar el resultado: %w", err)
	}
	return buf.Bytes(), nil
}

// RealceOnly es el FALLBACK cuando no hay recorte (sin REMOVEBG_API_KEY o si la API
// falla): mejora luz/color/nitidez de la foto completa, conservando el fondo original.
// Nunca altera ni recorta el producto. Devuelve JPEG.
func RealceOnly(originalBytes []byte) ([]byte, error) {
	src, _, err := image.Decode(bytes.NewReader(originalBytes))
	if err != nil {
		return nil, fmt.Errorf("no se pudo decodificar la foto: %w", err)
	}
	out := imaging.AdjustContrast(src, 8)
	out = imaging.AdjustBrightness(out, 3)
	out = imaging.AdjustSaturation(out, 5)
	out = imaging.Sharpen(out, 0.8)
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, out, &jpeg.Options{Quality: 92}); err != nil {
		return nil, fmt.Errorf("no se pudo codificar el resultado: %w", err)
	}
	return buf.Bytes(), nil
}
