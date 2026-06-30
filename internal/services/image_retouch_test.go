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

func TestComposeFaithful_SinMascara_FailsafeRealce(t *testing.T) {
	src := imaging.New(20, 20, color.NRGBA{120, 130, 140, 255})
	out, err := ComposeFaithful(pngOf(t, src), nil)
	require.NoError(t, err)
	img, format, err := image.Decode(bytes.NewReader(out))
	require.NoError(t, err)
	assert.Equal(t, "jpeg", format)
	assert.Equal(t, 20, img.Bounds().Dx(), "sin máscara conserva la foto original (realce)")
}
