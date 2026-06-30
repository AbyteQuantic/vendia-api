// Spec: specs/094-foto-fiel-fondo-realce/spec.md
//
// Composición FIEL (Opción A): el producto sale de los PÍXELES REALES de la foto del
// tendero, recortados con la máscara de Gemini (services.SegmentProductMask) y pegados
// sobre un fondo de estudio. NUNCA se regenera el producto. Sin dilatar la máscara
// (evita halo); alfa SUAVE = la luminancia de la máscara, así las partes finas (correa)
// quedan atenuadas pero presentes en vez de cortarse en seco.
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

func maskLum(c color.NRGBA) float64 {
	return float64(c.R)*0.299 + float64(c.G)*0.587 + float64(c.B)*0.114
}

// gentleRealce: mejora suave de luz/color sobre el producto (no cambia su identidad,
// no aplica nitidez para no crear borde). Conserva el alfa.
func gentleRealce(img image.Image) *image.NRGBA {
	out := imaging.AdjustContrast(img, 6)
	out = imaging.AdjustBrightness(out, 2)
	out = imaging.AdjustSaturation(out, 4)
	return out
}

// studioBackdrop: fondo de estudio (degradado vertical sutil blanco→gris claro).
func studioBackdrop(side int) *image.NRGBA {
	c := imaging.New(side, side, color.NRGBA{255, 255, 255, 255})
	for y := 0; y < side; y++ {
		t := float64(y) / float64(side)
		t2 := t * t
		r := uint8(255 - t2*18)
		g := uint8(255 - t2*16)
		b := uint8(255 - t2*13)
		for x := 0; x < side; x++ {
			c.SetNRGBA(x, y, color.NRGBA{r, g, b, 255})
		}
	}
	return c
}

func encodeJPEG(img image.Image) ([]byte, error) {
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, img, &jpeg.Options{Quality: 92}); err != nil {
		return nil, fmt.Errorf("no se pudo codificar el resultado: %w", err)
	}
	return buf.Bytes(), nil
}

// ComposeFaithful pega los PÍXELES REALES del producto (recortados con la máscara) sobre
// un fondo de estudio + sombra suave, centrado. maskBytes nil/ inválida → fail-safe:
// solo realce de la foto original (no recorta, no altera). Devuelve JPEG.
func ComposeFaithful(originalBytes, maskBytes []byte) ([]byte, error) {
	src, _, err := image.Decode(bytes.NewReader(originalBytes))
	if err != nil {
		return nil, fmt.Errorf("no se pudo decodificar la foto: %w", err)
	}
	realced := gentleRealce(src)

	if len(maskBytes) == 0 {
		return encodeJPEG(realced) // sin máscara → solo realce (fiel)
	}
	maskImg, err := imaging.Decode(bytes.NewReader(maskBytes))
	if err != nil {
		return encodeJPEG(realced)
	}
	b := realced.Bounds()
	w, h := b.Dx(), b.Dy()
	mask := imaging.Resize(maskImg, w, h, imaging.Linear)

	// Recorte con alfa SUAVE (= luminancia de la máscara). Sin dilatar → sin halo.
	prod := imaging.New(w, h, color.NRGBA{0, 0, 0, 0})
	minX, minY, maxX, maxY := w, h, -1, -1
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			a := maskLum(mask.NRGBAAt(x, y))
			if a <= 8 {
				continue
			}
			sc := realced.NRGBAAt(x, y)
			prod.SetNRGBA(x, y, color.NRGBA{sc.R, sc.G, sc.B, uint8(a)})
			if a > 30 { // bbox por el cuerpo sólido del producto
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
		return encodeJPEG(realced) // máscara vacía → fail-safe
	}

	prodCrop := imaging.Crop(prod, image.Rect(minX, minY, maxX+1, maxY+1))
	bw, bh := maxX-minX+1, maxY-minY+1
	const margin = 0.10
	frac := 1 - 2*margin
	side := int(float64(max(bw, bh)) / frac)
	if side < 1 {
		side = max(bw, bh)
	}

	canvas := studioBackdrop(side)
	offX, offY := (side-bw)/2, (side-bh)/2

	// Sombra de contacto: silueta difuminada, bajo el producto.
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
		image.Pt(offX, offY+int(float64(bh)*0.03)), 0.22)

	// Producto real encima.
	canvas = imaging.Overlay(canvas, prodCrop, image.Pt(offX, offY), 1.0)
	return encodeJPEG(canvas)
}
