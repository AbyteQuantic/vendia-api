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

// straightenElongated endereza el producto cuando su silueta es claramente
// alargada (llavero + correa, colgante, herramienta): calcula el eje
// principal de la máscara vía momentos de imagen y gira `prod` (con su
// alfa, sin invertar ningún pixel — es una rotación geométrica de píxeles
// reales, no generativa) para que ese eje quede vertical, como se espera
// en una foto de catálogo. Luego orienta la parte más "pesada" (la figura
// decorativa) hacia ABAJO, colgando, y el enganche/anillo más liviano
// hacia arriba — la convención con la que se presentan llaveros/colgantes
// en fotos de producto reales.
//
// Deliberadamente NO se aplica a siluetas poco alargadas (botellas, cajas,
// bolsas vistas de frente, la mayoría del catálogo): ahí el eje principal
// de una PCA de 2 líneas no es un dato confiable — podría "enderezar" en
// una dirección arbitraria y voltear el producto de forma rara. El umbral
// de elongación (relación entre el eje mayor y el menor de la silueta)
// filtra ese caso: solo actúa cuando la forma es inequívocamente alargada.
//
// minX/minY/maxX/maxY de ENTRADA acotan la región a inspeccionar
// (optimización, no cambian el resultado si no hay rotación). Devuelve el
// `prod` (rotado o el mismo) y su bbox actualizado.
func straightenElongated(prod *image.NRGBA, minX, minY, maxX, maxY int) (*image.NRGBA, int, int, int, int) {
	var sumW, sumX, sumY, sumXX, sumYY, sumXY float64
	for y := minY; y <= maxY; y++ {
		for x := minX; x <= maxX; x++ {
			a := float64(prod.NRGBAAt(x, y).A)
			if a <= 8 {
				continue
			}
			fx, fy := float64(x), float64(y)
			sumW += a
			sumX += a * fx
			sumY += a * fy
			sumXX += a * fx * fx
			sumYY += a * fy * fy
			sumXY += a * fx * fy
		}
	}
	if sumW <= 0 {
		return prod, minX, minY, maxX, maxY
	}

	cx, cy := sumX/sumW, sumY/sumW
	mu20 := sumXX/sumW - cx*cx
	mu02 := sumYY/sumW - cy*cy
	mu11 := sumXY/sumW - cx*cy

	common := math.Sqrt((mu20-mu02)*(mu20-mu02) + 4*mu11*mu11)
	lambdaMajor := (mu20 + mu02 + common) / 2
	lambdaMinor := (mu20 + mu02 - common) / 2
	if lambdaMinor <= 1e-6 || lambdaMajor <= 1e-6 {
		return prod, minX, minY, maxX, maxY // silueta degenerada — no rotar
	}

	const elongationThreshold = 1.6 // solo siluetas claramente alargadas
	if math.Sqrt(lambdaMajor/lambdaMinor) < elongationThreshold {
		return prod, minX, minY, maxX, maxY
	}

	angleDeg := 0.5 * math.Atan2(2*mu11, mu20-mu02) * 180 / math.Pi
	rotateDeg := angleDeg - 90
	if rotateDeg > 90 {
		rotateDeg -= 180
	} else if rotateDeg < -90 {
		rotateDeg += 180
	}
	if math.Abs(rotateDeg) < 3 {
		return prod, minX, minY, maxX, maxY // ya casi vertical, no vale la pena
	}

	rotated := imaging.Rotate(prod, rotateDeg, color.NRGBA{0, 0, 0, 0})
	nMinX, nMinY, nMaxX, nMaxY, ok := opaqueBounds(rotated)
	if !ok {
		return prod, minX, minY, maxX, maxY // rotación degeneró la máscara → fail-safe
	}

	// Convención de catálogo: la parte más pesada cuelga abajo, el
	// enganche liviano queda arriba. Si quedó al revés, voltear 180°.
	if massAbove(rotated, nMinX, nMinY, nMaxX, nMaxY) {
		rotated = imaging.Rotate180(rotated)
		nMinX, nMinY, nMaxX, nMaxY, ok = opaqueBounds(rotated)
		if !ok {
			return prod, minX, minY, maxX, maxY
		}
	}

	return rotated, nMinX, nMinY, nMaxX, nMaxY
}

// opaqueBounds devuelve el bounding box de los píxeles con alfa>8 en img.
// ok=false si no hay ninguno.
func opaqueBounds(img *image.NRGBA) (minX, minY, maxX, maxY int, ok bool) {
	b := img.Bounds()
	w, h := b.Dx(), b.Dy()
	minX, minY, maxX, maxY = w, h, -1, -1
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
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

// massAbove compara cuánta "masa" (suma de alfa) del producto cae en la
// mitad superior vs inferior de su propio bbox — true si hay más arriba
// (la figura pesada quedó mal orientada, hay que voltear 180°).
func massAbove(img *image.NRGBA, minX, minY, maxX, maxY int) bool {
	midY := (minY + maxY) / 2
	var top, bottom float64
	for y := minY; y <= maxY; y++ {
		for x := minX; x <= maxX; x++ {
			a := float64(img.NRGBAAt(x, y).A)
			if a <= 8 {
				continue
			}
			if y <= midY {
				top += a
			} else {
				bottom += a
			}
		}
	}
	return top > bottom
}

// ComposeFaithful pega los PÍXELES REALES del producto (recortados con la máscara) sobre
// un fondo de estudio + sombra suave, centrado. maskBytes nil/ inválida → fail-safe:
// solo realce de la foto original (no recorta, no altera). Devuelve JPEG. Usada por
// "Quitar fondo" — realce suave, cero riesgo de alterar el producto.
func ComposeFaithful(originalBytes, maskBytes []byte) ([]byte, error) {
	return composeFaithful(originalBytes, maskBytes, gentleRealce)
}

// ComposeFaithfulEnhanced es igual a ComposeFaithful pero con strongRealce: más
// contraste/brillo/saturación/nitidez para "Mejorar con IA". Reemplaza el uso de
// GeminiService.EnhancePhoto (generativo) en el flujo por-defecto de ese botón: Gemini
// solo aporta la máscara (SegmentProductMask, nunca dibuja el producto) y el "mejorado"
// es 100% filtros de píxeles deterministas sobre la foto real — así "Mejorar con IA" no
// puede reinterpretar/alucinar el producto (el bug reportado: un llavero de goma azul
// salía como un llavero metálico plateado de otro personaje).
func ComposeFaithfulEnhanced(originalBytes, maskBytes []byte) ([]byte, error) {
	return composeFaithful(originalBytes, maskBytes, strongRealce)
}

func composeFaithful(originalBytes, maskBytes []byte, realce func(image.Image) *image.NRGBA) ([]byte, error) {
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

	// Enderezar SOLO siluetas claramente alargadas (llavero+correa,
	// colgante) — un tendero suele fotografiarlas colgando en diagonal, y
	// eso salta a la vista sobre un fondo de estudio limpio. Productos
	// compactos (botellas, cajas, bolsas) no se tocan: ver comentario de
	// straightenElongated.
	prod, minX, minY, maxX, maxY = straightenElongated(prod, minX, minY, maxX, maxY)

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
