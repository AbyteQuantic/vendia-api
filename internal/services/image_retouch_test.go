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

func pngBytes(t *testing.T, img image.Image) []byte {
	t.Helper()
	var b bytes.Buffer
	require.NoError(t, png.Encode(&b, img))
	return b.Bytes()
}

func TestFaithfulRetouch_RealceOnly_PreservaTamano(t *testing.T) {
	src := imaging.New(16, 16, color.NRGBA{200, 60, 60, 255})
	out, err := FaithfulRetouch(pngBytes(t, src), nil)
	require.NoError(t, err)
	img, _, err := image.Decode(bytes.NewReader(out))
	require.NoError(t, err)
	assert.Equal(t, 16, img.Bounds().Dx())
	assert.Equal(t, 16, img.Bounds().Dy())
}

func TestFaithfulRetouch_Composite_FondoBlanco_ProductoReal(t *testing.T) {
	// Imagen toda roja; máscara: mitad izquierda blanca (producto), derecha negra (fondo).
	src := imaging.New(16, 16, color.NRGBA{200, 60, 60, 255})
	mask := imaging.New(16, 16, color.NRGBA{0, 0, 0, 255})
	for y := 0; y < 16; y++ {
		for x := 0; x < 8; x++ {
			mask.SetNRGBA(x, y, color.NRGBA{255, 255, 255, 255})
		}
	}

	out, err := FaithfulRetouch(pngBytes(t, src), pngBytes(t, mask))
	require.NoError(t, err)
	img, _, err := image.Decode(bytes.NewReader(out))
	require.NoError(t, err)

	// Tras centrar: el resultado es CUADRADO.
	bw, bh := img.Bounds().Dx(), img.Bounds().Dy()
	assert.Equal(t, bw, bh, "el encuadre queda cuadrado")

	// Esquina = margen blanco.
	cr, cg, cb, _ := img.At(0, 0).RGBA()
	assert.Greater(t, cr>>8, uint32(240))
	assert.Greater(t, cg>>8, uint32(240))
	assert.Greater(t, cb>>8, uint32(240))

	// Centro = producto real (rojo conservado, no borrado ni alterado).
	mr, mg, mb, _ := img.At(bw/2, bh/2).RGBA()
	assert.Greater(t, mr>>8, uint32(150), "producto conserva rojo en el centro")
	assert.Less(t, mg>>8, uint32(150))
	assert.Less(t, mb>>8, uint32(150))
}
