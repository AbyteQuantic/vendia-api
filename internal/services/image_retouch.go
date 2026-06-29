// Spec: specs/094-foto-fiel-fondo-realce/spec.md
//
// Realce, recorte de fondo, centrado y sombra NO generativos: operan sobre los
// PÍXELES REALES de la foto del tendero, nunca redibujan el producto. Garantía de
// fidelidad (Spec 094):
//   - applyRealce: filtros de foto (contraste, brillo, nitidez) — embellece sin alterar.
//   - cutoutWithAlpha: usa la máscara (producto vs fondo) para dejar el producto real
//     con transparencia (fondo recortado).
//   - centerProductOnSquare: recorta al contorno, centra en cuadrado blanco con margen
//     y agrega una SOMBRA suave (derivada de la silueta) para mejor aspecto.
// Si no hay máscara, se devuelve solo el realce (fail-safe).
package services

import (
	"bytes"
	"fmt"
	"image"
	"image/color"
	_ "image/jpeg" // decode jpeg
	"image/jpeg"
	_ "image/png" // decode png

	"github.com/disintegration/imaging"
)

// applyRealce mejora cómo se ve la FOTO sin inventar nada: leve contraste, brillo y
// nitidez sobre los píxeles reales. Valores conservadores para no "quemar" la imagen.
func applyRealce(img image.Image) *image.NRGBA {
	out := imaging.AdjustContrast(img, 8)  // +8% contraste
	out = imaging.AdjustBrightness(out, 3) // +3% brillo
	out = imaging.Sharpen(out, 0.8)        // nitidez suave (unsharp)
	return out
}

func maskLuminance(c color.NRGBA) float64 {
	return float64(c.R)*0.299 + float64(c.G)*0.587 + float64(c.B)*0.114
}

// cutoutWithAlpha recorta el fondo: deja el producto real con TRANSPARENCIA (alfa =
// luminancia de la máscara), sin tocar sus colores. Devuelve también la máscara
// reescalada (para hallar el bounding box al centrar).
func cutoutWithAlpha(src image.Image, mask image.Image) (*image.NRGBA, *image.NRGBA) {
	b := src.Bounds()
	w, h := b.Dx(), b.Dy()
	srcN := imaging.Clone(src)
	maskR := imaging.Resize(mask, w, h, imaging.Linear)
	out := imaging.New(w, h, color.NRGBA{0, 0, 0, 0}) // transparente
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			a := maskLuminance(maskR.NRGBAAt(x, y))
			if a <= 5 {
				continue // fondo → transparente
			}
			sc := srcN.NRGBAAt(x, y)
			out.SetNRGBA(x, y, color.NRGBA{sc.R, sc.G, sc.B, uint8(a)})
		}
	}
	return out, maskR
}

// centerProductOnSquare recorta al contorno real del producto (bounding box de la
// máscara), lo centra en un lienzo CUADRADO blanco con margen y le pone una SOMBRA
// suave. Spec 094: solo mueve/encuadra los píxeles reales y agrega una sombra a la
// COMPOSICIÓN — no redibuja ni inventa nada del producto. Devuelve nil si no halla
// producto (para que el caller use el fail-safe).
func centerProductOnSquare(product *image.NRGBA, mask *image.NRGBA, margin float64) *image.NRGBA {
	b := mask.Bounds()
	w, h := b.Dx(), b.Dy()
	minX, minY, maxX, maxY := w, h, -1, -1
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			if maskLuminance(mask.NRGBAAt(x, y)) > 40 {
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
		return nil // máscara vacía
	}

	prodCrop := imaging.Crop(product, image.Rect(minX, minY, maxX+1, maxY+1))
	bw, bh := maxX-minX+1, maxY-minY+1
	frac := 1 - 2*margin
	if frac < 0.5 {
		frac = 0.5
	}
	side := int(float64(max(bw, bh)) / frac)
	if side < 1 {
		side = max(bw, bh)
	}
	canvas := imaging.New(side, side, color.NRGBA{255, 255, 255, 255})
	offX, offY := (side-bw)/2, (side-bh)/2

	// Sombra: silueta del producto en gris oscuro, difuminada y semitransparente,
	// desplazada hacia abajo (sombra de contacto). Deriva de la forma real.
	shadow := imaging.New(bw, bh, color.NRGBA{0, 0, 0, 0})
	for y := 0; y < bh; y++ {
		for x := 0; x < bw; x++ {
			if prodCrop.NRGBAAt(x, y).A > 40 {
				shadow.SetNRGBA(x, y, color.NRGBA{40, 40, 40, 255})
			}
		}
	}
	sigma := float64(side)*0.02 + 1
	shadow = imaging.Blur(shadow, sigma)
	dy := int(float64(side) * 0.04) // hacia abajo

	canvas = imaging.Overlay(canvas, shadow, image.Pt(offX, offY+dy), 0.28)
	canvas = imaging.Overlay(canvas, prodCrop, image.Pt(offX, offY), 1.0)
	return canvas
}

// FaithfulRetouch aplica el pipeline fiel: realce siempre; recorte de fondo + centrado
// + sombra si hay máscara. NUNCA regenera el producto. maskBytes puede ser nil
// (fail-safe → solo realce). Devuelve JPEG (el upload a R2 lo convierte a WebP).
func FaithfulRetouch(originalBytes, maskBytes []byte) ([]byte, error) {
	src, _, err := image.Decode(bytes.NewReader(originalBytes))
	if err != nil {
		return nil, fmt.Errorf("no se pudo decodificar la foto: %w", err)
	}

	realced := applyRealce(src)
	var result image.Image = realced

	if len(maskBytes) > 0 {
		if mask, _, mErr := image.Decode(bytes.NewReader(maskBytes)); mErr == nil {
			cut, maskR := cutoutWithAlpha(realced, mask)
			if centered := centerProductOnSquare(cut, maskR, 0.10); centered != nil {
				result = centered
			}
			// bbox vacío o máscara no decodifica → fail-safe: solo realce.
		}
	}

	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, result, &jpeg.Options{Quality: 90}); err != nil {
		return nil, fmt.Errorf("no se pudo codificar el resultado: %w", err)
	}
	return buf.Bytes(), nil
}
