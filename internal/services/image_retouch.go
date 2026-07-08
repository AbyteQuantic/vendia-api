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
	"image/jpeg"
	_ "image/jpeg"
	_ "image/png"
	"math"

	"github.com/disintegration/imaging"
)

// opaqueBounds devuelve el bounding box de los píxeles con alfa>8 en img.
// ok=false si no hay ninguno (máscara vacía).
func opaqueBounds(img *image.NRGBA) (minX, minY, maxX, maxY int, ok bool) {
	b := img.Bounds()
	minX, minY, maxX, maxY = b.Max.X, b.Max.Y, b.Min.X-1, b.Min.Y-1
	for y := b.Min.Y; y < b.Max.Y; y++ {
		for x := b.Min.X; x < b.Max.X; x++ {
			if img.NRGBAAt(x, y).A <= 8 {
				continue
			}
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
	return minX, minY, maxX, maxY, maxX >= minX && maxY >= minY
}

func maskLum(c color.NRGBA) float64 {
	return float64(c.R)*0.299 + float64(c.G)*0.587 + float64(c.B)*0.114
}

// gentleRealce: mejora suave de luz/color sobre el producto (no cambia su identidad,
// no aplica nitidez para no crear borde). Conserva el alfa. Usada por "Quitar fondo".
func gentleRealce(img image.Image) *image.NRGBA {
	out := imaging.AdjustContrast(img, 6)
	out = imaging.AdjustBrightness(out, 2)
	out = imaging.AdjustSaturation(out, 4)
	return out
}

// strongRealce: mejora más agresiva de luz/color/nitidez para "Mejorar con IA".
// Sigue siendo un filtro de PÍXELES (determinista, no generativo) — nunca puede
// cambiar la identidad del producto, solo su presentación fotográfica. El blur
// suave antes de afilar evita que el sharpen amplifique el grano/ruido de la
// foto (denoise-then-sharpen); contraste/brillo/saturación/gamma más marcados
// corrigen fotos subexpuestas o con balance de blanco pobre — el caso típico de
// una foto de celular en interior con mala luz.
func strongRealce(img image.Image) *image.NRGBA {
	out := imaging.Blur(img, 0.4)
	out = imaging.Sharpen(out, 1.2)
	out = imaging.AdjustContrast(out, 12)
	out = imaging.AdjustBrightness(out, 3)
	out = imaging.AdjustSaturation(out, 10)
	out = imaging.AdjustGamma(out, 1.05)
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
// solo realce de la foto original (no recorta, no altera). Devuelve JPEG. Usada por
// "Quitar fondo" — realce suave, cero riesgo de alterar el producto.
//
// rotationDeg gira el producto (píxeles reales, determinista — nunca lo regenera)
// para presentarlo derecho/nivelado como en una foto de catálogo. Positivo =
// antihorario, negativo = horario (misma convención que imaging.Rotate y que
// GeminiService.EstimateUprightRotation, que es quien calcula este valor —
// NUNCA geometría rígida local: un producto con varias piezas que cuelgan de
// forma independiente del mismo punto, ej. un llavero con correa Y figura, no
// tiene un único eje rígido "correcto"; Gemini entiende semánticamente qué es
// "derecho" para ESE producto). |rotationDeg|<2 omite la rotación (no vale la
// pena el desenfoque de interpolación para una corrección imperceptible).
func ComposeFaithful(originalBytes, maskBytes []byte, rotationDeg float64) ([]byte, error) {
	return composeFaithful(originalBytes, maskBytes, rotationDeg, gentleRealce)
}

// ComposeFaithfulEnhanced es igual a ComposeFaithful pero con strongRealce: más
// contraste/brillo/saturación/nitidez para "Mejorar con IA". Reemplaza el uso de
// GeminiService.EnhancePhoto (generativo) en el flujo por-defecto de ese botón: Gemini
// solo aporta la máscara (SegmentProductMask, nunca dibuja el producto) y el "mejorado"
// es 100% filtros de píxeles deterministas sobre la foto real — así "Mejorar con IA" no
// puede reinterpretar/alucinar el producto (el bug reportado: un llavero de goma azul
// salía como un llavero metálico plateado de otro personaje).
func ComposeFaithfulEnhanced(originalBytes, maskBytes []byte, rotationDeg float64) ([]byte, error) {
	return composeFaithful(originalBytes, maskBytes, rotationDeg, strongRealce)
}

func composeFaithful(originalBytes, maskBytes []byte, rotationDeg float64, realce func(image.Image) *image.NRGBA) ([]byte, error) {
	src, _, err := image.Decode(bytes.NewReader(originalBytes))
	if err != nil {
		return nil, fmt.Errorf("no se pudo decodificar la foto: %w", err)
	}
	realced := realce(src)

	if len(maskBytes) == 0 {
		return encodeJPEG(realced) // sin máscara → solo realce (fiel)
	}
	maskImg, err := imaging.Decode(bytes.NewReader(maskBytes))
	if err != nil {
		return encodeJPEG(realced)
	}
	b := realced.Bounds()
	w, h := b.Dx(), b.Dy()
	// CatmullRom (cúbico nítido) escala la máscara con menos escalones que Linear.
	mask := imaging.Resize(maskImg, w, h, imaging.CatmullRom)
	// Feather anti-alias: un blur MUY pequeño suaviza los dientes del borde
	// (staircase) convirtiéndolos en una rampa de 1 px. Es SEGURO para partes
	// finas: el blur las ESPARCE (no las erosiona), así una correa/cadena queda
	// más suave pero presente. El umbral a<=8 de más abajo recorta la rampa más
	// tenue que agrega el blur, así que no reaparece halo. Radio escalado al
	// tamaño y acotado [0.5, 1.2] px para no engordar el recorte.
	blurR := float64(w+h) / 2 * 0.0009
	if blurR < 0.5 {
		blurR = 0.5
	} else if blurR > 1.2 {
		blurR = 1.2
	}
	mask = imaging.Blur(mask, blurR)

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
			// El bbox usa el MISMO umbral que la inclusión de arriba (a<=8
			// se descarta, todo lo demás cuenta). Antes usaba a>30 ("cuerpo
			// sólido") mientras el pegado usaba a>8 — cualquier parte
			// delgada del producto (correa, cadena, cordón) con alfa entre
			// 9 y 30 SÍ se pintaba en `prod` pero el recorte final
			// (`imaging.Crop` más abajo, acotado a este bbox) la dejaba
			// fuera del marco: el pixel existía pero jamás llegaba al
			// canvas. Bug real reportado: la correa "Stitch" del llavero
			// quedaba cortada en el resultado. Mismo umbral en ambos lados
			// = lo que se pinta es lo que se incluye en el recorte.
			if a > 8 {
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

	// Rotación determinista (píxeles reales, sin regenerar nada) al ángulo
	// que Gemini estimó como "derecho" para este producto — ver comentario
	// en ComposeFaithful. Recalcula el bbox sobre la imagen ya rotada; si
	// la rotación deja la máscara vacía (no debería pasar, pero es una
	// transformación geométrica sobre datos externos) se ignora la
	// rotación y se sigue con el bbox original sin rotar.
	if math.Abs(rotationDeg) >= 2 {
		rotatedProd := imaging.Rotate(prod, rotationDeg, color.NRGBA{0, 0, 0, 0})
		if rMinX, rMinY, rMaxX, rMaxY, ok := opaqueBounds(rotatedProd); ok {
			prod, minX, minY, maxX, maxY = rotatedProd, rMinX, rMinY, rMaxX, rMaxY
		}
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
