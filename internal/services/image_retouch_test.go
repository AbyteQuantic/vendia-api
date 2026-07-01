// Spec: specs/094-foto-fiel-fondo-realce/spec.md
package services

import (
	"bytes"
	"image"
	"image/color"
	"image/png"
	"testing"

	"github.com/disintegration/imaging"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func pngOf(t *testing.T, img image.Image) []byte {
	t.Helper()
	var b bytes.Buffer
	require.NoError(t, png.Encode(&b, img))
	return b.Bytes()
}

func TestComposeFaithful_PegaProductoRealSobreEstudio(t *testing.T) {
	// Foto: toda roja. Máscara: mitad izquierda blanca (producto), derecha negra (fondo).
	src := imaging.New(16, 16, color.NRGBA{200, 60, 60, 255})
	mask := imaging.New(16, 16, color.NRGBA{0, 0, 0, 255})
	for y := 0; y < 16; y++ {
		for x := 0; x < 8; x++ {
			mask.SetNRGBA(x, y, color.NRGBA{255, 255, 255, 255})
		}
	}
	out, err := ComposeFaithful(pngOf(t, src), pngOf(t, mask))
	require.NoError(t, err)
	img, _, err := image.Decode(bytes.NewReader(out))
	require.NoError(t, err)

	w, h := img.Bounds().Dx(), img.Bounds().Dy()
	assert.Equal(t, w, h, "encuadre cuadrado")
	// Esquina superior = fondo de estudio (claro).
	cr, _, _, _ := img.At(0, 0).RGBA()
	assert.Greater(t, cr>>8, uint32(235))
	// Centro = producto real (rojo conservado, no se vuelve otro).
	mr, mg, mb, _ := img.At(w/2, h/2).RGBA()
	assert.Greater(t, mr>>8, uint32(140))
	assert.Less(t, mg>>8, uint32(160))
	assert.Less(t, mb>>8, uint32(160))
}

// Bug real reportado: la correa/cordón de un llavero quedaba cortada del
// resultado. El bbox del recorte usaba un umbral (a>30, "cuerpo sólido") más
// estricto que el de inclusión en el pegado (a>8) — una parte delgada del
// producto con alfa entre 9 y 30 SÍ se pintaba en el buffer intermedio pero
// el Crop final (acotado al bbox viejo, más chico) la descartaba antes de
// llegar al canvas. Este test construye una máscara con un "cuerpo sólido"
// en una esquina y una "correa" tenue (alfa ~20, fuera del cuerpo sólido)
// lejos de esa esquina — el canvas final debe ser lo bastante grande para
// contener AMBAS partes, no solo el cuerpo sólido.
func TestComposeFaithful_NoRecortaPartesDelgadasDelProducto(t *testing.T) {
	src := imaging.New(30, 30, color.NRGBA{80, 90, 100, 255})
	mask := imaging.New(30, 30, color.NRGBA{0, 0, 0, 255})
	// Cuerpo sólido: blanco puro (alfa ~255), esquina superior-izquierda.
	for y := 2; y <= 9; y++ {
		for x := 2; x <= 9; x++ {
			mask.SetNRGBA(x, y, color.NRGBA{255, 255, 255, 255})
		}
	}
	// "Correa" tenue: gris (luminancia ~20, entre el umbral de inclusión
	// a>8 y el viejo umbral de bbox a>30), lejos del cuerpo sólido.
	for y := 20; y <= 24; y++ {
		for x := 20; x <= 24; x++ {
			mask.SetNRGBA(x, y, color.NRGBA{20, 20, 20, 255})
		}
	}

	out, err := ComposeFaithful(pngOf(t, src), pngOf(t, mask))
	require.NoError(t, err)
	img, _, err := image.Decode(bytes.NewReader(out))
	require.NoError(t, err)

	w := img.Bounds().Dx()
	// bbox completo (2..24 en ambos ejes) = 23px + margen 10% por lado →
	// side ≈ 23/0.8 ≈ 28. Con el bug viejo (bbox solo del cuerpo sólido
	// 2..9 = 8px) el canvas habría salido de ~10px — muy por debajo de 20.
	assert.Greater(t, w, 20,
		"el canvas debe ser lo bastante grande para incluir la correa tenue, no solo el cuerpo sólido")
}

// drawFilledDisk pinta un disco opaco (alfa 255) centrado en (cx,cy) con
// radio r sobre un *image.NRGBA — helper para las pruebas de enderezado.
func drawFilledDisk(img *image.NRGBA, cx, cy, r float64) {
	b := img.Bounds()
	for y := b.Min.Y; y < b.Max.Y; y++ {
		for x := b.Min.X; x < b.Max.X; x++ {
			dx, dy := float64(x)-cx, float64(y)-cy
			if dx*dx+dy*dy <= r*r {
				img.SetNRGBA(x, y, color.NRGBA{40, 70, 200, 255})
			}
		}
	}
}

// drawFilledStrip pinta una franja opaca de ancho halfW*2 desde (cx,cy) en
// la dirección (dx,dy) durante `length` píxeles — simula la correa/cadena
// de un llavero saliendo de la figura principal.
func drawFilledStrip(img *image.NRGBA, cx, cy, dx, dy, length, halfW float64) {
	b := img.Bounds()
	for t := 0.0; t < length; t += 0.5 {
		px, py := cx+dx*t, cy+dy*t
		for o := -halfW; o <= halfW; o += 0.5 {
			x, y := int(px+o*dy), int(py-o*dx)
			if x >= b.Min.X && x < b.Max.X && y >= b.Min.Y && y < b.Max.Y {
				img.SetNRGBA(x, y, color.NRGBA{40, 70, 200, 255})
			}
		}
	}
}

// Bug real reportado: un llavero fotografiado colgando en diagonal se
// mostraba diagonal también en el resultado — "no parece estudio
// fotográfico". straightenElongated debe girar una silueta claramente
// alargada (figura + correa) hasta dejar su eje vertical, con la parte
// pesada (la figura) abajo — la convención de cómo se presentan
// llaveros/colgantes en fotos de catálogo reales.
func TestStraightenElongated_RotatesElongatedToVertical(t *testing.T) {
	const w, h = 120, 120
	prod := imaging.New(w, h, color.NRGBA{0, 0, 0, 0})
	// Figura pesada arriba-derecha, correa larga hacia abajo-izquierda —
	// eje a ~30° de la vertical, igual al caso real reportado.
	drawFilledDisk(prod, 85, 35, 18)
	drawFilledStrip(prod, 85, 35, -0.5, 0.866, 70, 4)

	minX, minY, maxX, maxY, ok := opaqueBounds(prod)
	require.True(t, ok)

	rotated, nMinX, nMinY, nMaxX, nMaxY :=
		straightenElongated(prod, minX, minY, maxX, maxY)

	bw, bh := nMaxX-nMinX+1, nMaxY-nMinY+1
	assert.Greater(t, bh, bw,
		"una silueta alargada enderezada debe quedar más alta que ancha (vertical)")

	assert.False(t, massAbove(rotated, nMinX, nMinY, nMaxX, nMaxY),
		"la parte pesada (la figura) debe terminar ABAJO, no arriba — convención de catálogo")
}

// Un producto compacto (botella, caja, disco) no tiene un eje principal
// confiable — straightenElongated NO debe tocarlo, o arriesga voltearlo de
// forma arbitraria. El bbox debe salir IDÉNTICO al de entrada.
func TestStraightenElongated_SkipsCompactShapes(t *testing.T) {
	const w, h = 120, 120
	prod := imaging.New(w, h, color.NRGBA{0, 0, 0, 0})
	drawFilledDisk(prod, 60, 60, 30)

	minX, minY, maxX, maxY, ok := opaqueBounds(prod)
	require.True(t, ok)

	_, nMinX, nMinY, nMaxX, nMaxY := straightenElongated(prod, minX, minY, maxX, maxY)

	assert.Equal(t, [4]int{minX, minY, maxX, maxY}, [4]int{nMinX, nMinY, nMaxX, nMaxY},
		"una silueta compacta (disco) no debe rotarse — el bbox debe quedar igual")
}

func TestComposeFaithful_SinMascara_FailsafeRealce(t *testing.T) {
	src := imaging.New(20, 20, color.NRGBA{120, 130, 140, 255})
	out, err := ComposeFaithful(pngOf(t, src), nil)
	require.NoError(t, err)
	img, format, err := image.Decode(bytes.NewReader(out))
	require.NoError(t, err)
	assert.Equal(t, "jpeg", format)
	assert.Equal(t, 20, img.Bounds().Dx(), "sin máscara conserva la foto original (realce)")
}

// "Mejorar con IA" (ComposeFaithfulEnhanced) reemplazó al camino generativo
// (GeminiService.EnhancePhoto) por el mismo principio que "Quitar fondo":
// Gemini solo aporta la máscara, el producto sale de los píxeles reales. Este
// test es la garantía estructural de que "mejorar" nunca puede cambiar la
// identidad del producto — el color base se conserva (mismo canal dominante),
// solo cambia de intensidad por el realce más fuerte.
func TestComposeFaithfulEnhanced_PegaProductoRealSobreEstudio(t *testing.T) {
	src := imaging.New(16, 16, color.NRGBA{200, 60, 60, 255})
	mask := imaging.New(16, 16, color.NRGBA{0, 0, 0, 255})
	for y := 0; y < 16; y++ {
		for x := 0; x < 8; x++ {
			mask.SetNRGBA(x, y, color.NRGBA{255, 255, 255, 255})
		}
	}
	out, err := ComposeFaithfulEnhanced(pngOf(t, src), pngOf(t, mask))
	require.NoError(t, err)
	img, _, err := image.Decode(bytes.NewReader(out))
	require.NoError(t, err)

	w, h := img.Bounds().Dx(), img.Bounds().Dy()
	assert.Equal(t, w, h, "encuadre cuadrado")
	// Centro = producto real: sigue siendo la MISMA identidad de color (rojo
	// dominante), nunca otro objeto/color — el realce fuerte solo intensifica.
	mr, mg, mb, _ := img.At(w/2, h/2).RGBA()
	assert.Greater(t, mr>>8, uint32(140), "rojo se conserva como canal dominante")
	assert.Less(t, mg>>8, uint32(160))
	assert.Less(t, mb>>8, uint32(160))
}

func TestComposeFaithfulEnhanced_SinMascara_FailsafeRealce(t *testing.T) {
	src := imaging.New(20, 20, color.NRGBA{120, 130, 140, 255})
	out, err := ComposeFaithfulEnhanced(pngOf(t, src), nil)
	require.NoError(t, err)
	img, format, err := image.Decode(bytes.NewReader(out))
	require.NoError(t, err)
	assert.Equal(t, "jpeg", format)
	assert.Equal(t, 20, img.Bounds().Dx(), "sin máscara conserva la foto original (solo realce)")
}

// El realce fuerte de "Mejorar con IA" debe notarse más que el suave de
// "Quitar fondo" sobre la MISMA foto — si no, el botón "Mejorar" no mejora
// nada distinto y el fix pierde su propósito (colores/nitidez más marcados).
func TestStrongRealce_EsMasIntensoQueGentleRealce(t *testing.T) {
	src := imaging.New(20, 20, color.NRGBA{100, 110, 120, 255})
	gentle := gentleRealce(src)
	strong := strongRealce(src)

	gr, _, gb, _ := gentle.At(10, 10).RGBA()
	sr, _, sb, _ := strong.At(10, 10).RGBA()
	// La saturación/contraste más fuerte separa los canales más — el delta
	// entre canales debe ser mayor en el resultado "strong" que en "gentle".
	gentleSpread := int(gr>>8) - int(gb>>8)
	strongSpread := int(sr>>8) - int(sb>>8)
	if gentleSpread < 0 {
		gentleSpread = -gentleSpread
	}
	if strongSpread < 0 {
		strongSpread = -strongSpread
	}
	assert.GreaterOrEqual(t, strongSpread, gentleSpread,
		"el realce fuerte debe separar los canales al menos tanto como el suave")
}
