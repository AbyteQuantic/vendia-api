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

	// Lado derecho (fondo) → blanco.
	rr, rg, rb, _ := img.At(13, 8).RGBA()
	assert.Greater(t, rr>>8, uint32(240))
	assert.Greater(t, rg>>8, uint32(240))
	assert.Greater(t, rb>>8, uint32(240))

	// Lado izquierdo (producto) → conserva el rojo real (R alto, G/B bajos),
	// NO se vuelve blanco → el producto no se borra ni se altera.
	lr, lg, lb, _ := img.At(3, 8).RGBA()
	assert.Greater(t, lr>>8, uint32(150), "producto conserva rojo")
	assert.Less(t, lg>>8, uint32(150))
	assert.Less(t, lb>>8, uint32(150))
}
