// Spec: specs/094-foto-fiel-fondo-realce/spec.md
//
// Realce y recorte de fondo NO generativos: operan sobre los PÍXELES REALES de la
// foto del tendero, nunca redibujan el producto. Garantía de fidelidad (Spec 094):
//   - applyRealce: filtros de foto (contraste, brillo, nitidez) — embellece sin alterar.
//   - compositeOnWhite: usa una máscara (producto vs fondo) para dejar el producto
//     real sobre blanco. Si no hay máscara, se devuelve solo el realce (fail-safe).
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

// compositeOnWhite coloca el producto (donde la máscara es clara) sobre fondo blanco.
// mask se reescala al tamaño de src; su luminancia funciona como alfa del producto.
func compositeOnWhite(src image.Image, mask image.Image) *image.NRGBA {
	b := src.Bounds()
	w, h := b.Dx(), b.Dy()
	srcN := imaging.Clone(src)
	maskR := imaging.Resize(mask, w, h, imaging.Linear)
	out := imaging.New(w, h, color.NRGBA{255, 255, 255, 255}) // lienzo blanco
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			mc := maskR.NRGBAAt(x, y)
			// Luminancia de la máscara como alfa [0..1].
			a := float64(mc.R)*0.299 + float64(mc.G)*0.587 + float64(mc.B)*0.114
			alpha := a / 255.0
			if alpha <= 0.02 {
				continue // fondo → queda blanco
			}
			sc := srcN.NRGBAAt(x, y)
			if alpha >= 0.98 {
				out.SetNRGBA(x, y, color.NRGBA{sc.R, sc.G, sc.B, 255})
				continue
			}
			// Borde: mezcla producto real con blanco (anti-alias).
			blend := func(c uint8) uint8 {
				return uint8(float64(c)*alpha + 255*(1-alpha))
			}
			out.SetNRGBA(x, y, color.NRGBA{blend(sc.R), blend(sc.G), blend(sc.B), 255})
		}
	}
	return out
}

// FaithfulRetouch aplica el pipeline fiel: realce siempre; recorte de fondo si hay
// máscara. NUNCA regenera el producto. maskBytes puede ser nil (fail-safe → solo realce).
// Devuelve JPEG (el upload a R2 lo convierte a WebP aguas abajo).
func FaithfulRetouch(originalBytes, maskBytes []byte) ([]byte, error) {
	src, _, err := image.Decode(bytes.NewReader(originalBytes))
	if err != nil {
		return nil, fmt.Errorf("no se pudo decodificar la foto: %w", err)
	}

	var result image.Image = applyRealce(src)

	if len(maskBytes) > 0 {
		if mask, _, mErr := image.Decode(bytes.NewReader(maskBytes)); mErr == nil {
			// Componer el producto realzado sobre blanco usando la máscara.
			result = compositeOnWhite(result, mask)
		}
		// Si la máscara no decodifica → fail-safe: queda solo el realce.
	}

	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, result, &jpeg.Options{Quality: 90}); err != nil {
		return nil, fmt.Errorf("no se pudo codificar el resultado: %w", err)
	}
	return buf.Bytes(), nil
}
