// Spec: specs/094-foto-fiel-fondo-realce/spec.md
//
// Realce, recorte de fondo, centrado y sombra NO generativos: operan sobre los
// PÍXELES REALES de la foto del tendero, nunca redibujan el producto. Garantía de
// fidelidad (Spec 094):
//   - applyRealce: filtros de foto (contraste, brillo, nitidez) — embellece sin alterar.
//   - cleanMask: rellena huecos pequeños y dilata levemente la máscara para NO cortar
//     bordes ni dejar "puntos" recortados del producto (morfología sobre la máscara,
//     no sobre el producto).
//   - cutoutWithAlpha: deja el producto real con transparencia (fondo recortado).
//   - centerProductOnSquare: recorta al contorno, centra en cuadrado blanco con margen
//     y agrega una SOMBRA suave para mejor aspecto.
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
	"math"

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

// fillSmallHoles rellena SOLO los huecos interiores PEQUEÑOS (los "puntos" que el
// modelo dejó como fondo dentro del producto), dejando intactos los huecos grandes
// reales (p. ej. el centro de una argolla). No crece el contorno → no genera halo.
// maxArea = tamaño máximo de hueco a rellenar (en píxeles).
func fillSmallHoles(g []bool, w, h, maxArea int) []bool {
	reach := make([]bool, w*h) // fondo conectado al borde
	stack := make([]int, 0, w*h/4)
	push := func(i int) {
		if !g[i] && !reach[i] {
			reach[i] = true
			stack = append(stack, i)
		}
	}
	for x := 0; x < w; x++ {
		push(x)
		push((h-1)*w + x)
	}
	for y := 0; y < h; y++ {
		push(y * w)
		push(y*w + w - 1)
	}
	neigh := func(i int) [4]int {
		x, y := i%w, i/w
		n := [4]int{-1, -1, -1, -1}
		if x > 0 {
			n[0] = i - 1
		}
		if x < w-1 {
			n[1] = i + 1
		}
		if y > 0 {
			n[2] = i - w
		}
		if y < h-1 {
			n[3] = i + w
		}
		return n
	}
	for len(stack) > 0 {
		i := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		for _, ni := range neigh(i) {
			if ni >= 0 {
				push(ni)
			}
		}
	}

	out := make([]bool, w*h)
	copy(out, g)
	seen := make([]bool, w*h)
	// Recorrer componentes de hueco (fondo NO alcanzable) y rellenar los pequeños.
	for start := 0; start < w*h; start++ {
		if g[start] || reach[start] || seen[start] {
			continue
		}
		comp := []int{start}
		seen[start] = true
		for qi := 0; qi < len(comp); qi++ {
			for _, ni := range neigh(comp[qi]) {
				if ni >= 0 && !g[ni] && !reach[ni] && !seen[ni] {
					seen[ni] = true
					comp = append(comp, ni)
				}
			}
		}
		if len(comp) <= maxArea { // hueco pequeño → rellenar
			for _, c := range comp {
				out[c] = true
			}
		}
	}
	return out
}

// cleanMask limpia la máscara reescalada: binariza y rellena los huecos PEQUEÑOS
// (sin dilatar → sin halo), luego un leve desenfoque para bordes suaves. Spec 094:
// solo opera sobre la MÁSCARA, jamás sobre los píxeles del producto.
func cleanMask(mask *image.NRGBA) *image.NRGBA {
	b := mask.Bounds()
	w, h := b.Dx(), b.Dy()
	g := make([]bool, w*h)
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			g[y*w+x] = maskLuminance(mask.NRGBAAt(x, y)) > 40
		}
	}
	maxHole := int(math.Round(float64(w*h) * 0.004)) // ~0.4% del área = "punto pequeño"
	g = fillSmallHoles(g, w, h, maxHole)

	out := imaging.New(w, h, color.NRGBA{0, 0, 0, 255})
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			if g[y*w+x] {
				out.SetNRGBA(x, y, color.NRGBA{255, 255, 255, 255})
			}
		}
	}
	return imaging.Blur(out, 0.6) // bordes suaves, sin engordar el contorno
}

// cutoutWithAlpha recorta el fondo: deja el producto real con TRANSPARENCIA (alfa =
// luminancia de la máscara ya limpia), sin tocar sus colores. mask debe ser del mismo
// tamaño que src.
func cutoutWithAlpha(src image.Image, mask *image.NRGBA) *image.NRGBA {
	b := src.Bounds()
	w, h := b.Dx(), b.Dy()
	srcN := imaging.Clone(src)
	out := imaging.New(w, h, color.NRGBA{0, 0, 0, 0})
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			a := maskLuminance(mask.NRGBAAt(x, y))
			if a <= 5 {
				continue // fondo → transparente
			}
			sc := srcN.NRGBAAt(x, y)
			out.SetNRGBA(x, y, color.NRGBA{sc.R, sc.G, sc.B, uint8(a)})
		}
	}
	return out
}

// centerProductOnSquare recorta al contorno real del producto (bounding box de la
// máscara), lo centra en un lienzo CUADRADO blanco con margen y le pone una SOMBRA
// suave. Devuelve nil si no halla producto (el caller usa el fail-safe).
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
	shadow = imaging.Blur(shadow, float64(side)*0.02+1)
	dy := int(float64(side) * 0.04) // hacia abajo

	canvas = imaging.Overlay(canvas, shadow, image.Pt(offX, offY+dy), 0.28)
	canvas = imaging.Overlay(canvas, prodCrop, image.Pt(offX, offY), 1.0)
	return canvas
}

// FaithfulRetouch aplica el pipeline fiel: realce siempre; limpieza de máscara +
// recorte + centrado + sombra si hay máscara. NUNCA regenera el producto. maskBytes
// puede ser nil (fail-safe → solo realce). Devuelve JPEG (R2 lo pasa a WebP).
func FaithfulRetouch(originalBytes, maskBytes []byte) ([]byte, error) {
	src, _, err := image.Decode(bytes.NewReader(originalBytes))
	if err != nil {
		return nil, fmt.Errorf("no se pudo decodificar la foto: %w", err)
	}

	realced := applyRealce(src)
	var result image.Image = realced

	if len(maskBytes) > 0 {
		if mask, _, mErr := image.Decode(bytes.NewReader(maskBytes)); mErr == nil {
			b := realced.Bounds()
			maskR := imaging.Resize(mask, b.Dx(), b.Dy(), imaging.Linear)
			cleaned := cleanMask(maskR) // completar puntos + no cortar bordes
			cut := cutoutWithAlpha(realced, cleaned)
			if centered := centerProductOnSquare(cut, cleaned, 0.12); centered != nil {
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
