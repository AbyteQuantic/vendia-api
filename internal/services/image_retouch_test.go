// Spec: specs/094-foto-fiel-fondo-realce/spec.md
package services

import (
	"bytes"
	"context"
	"image"
	"image/color"
	"image/png"
	"testing"

	"github.com/disintegration/imaging"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func cutoutPNG(t *testing.T) []byte {
	t.Helper()
	// Producto: cuadrado rojo opaco centrado; resto transparente (recorte limpio).
	img := imaging.New(16, 16, color.NRGBA{0, 0, 0, 0})
	for y := 4; y < 12; y++ {
		for x := 4; x < 12; x++ {
			img.SetNRGBA(x, y, color.NRGBA{200, 60, 60, 255})
		}
	}
	var b bytes.Buffer
	require.NoError(t, png.Encode(&b, img))
	return b.Bytes()
}

func TestComposeStudioFromCutout_CuadradoConProductoReal(t *testing.T) {
	out, err := ComposeStudioFromCutout(cutoutPNG(t))
	require.NoError(t, err)
	img, _, err := image.Decode(bytes.NewReader(out))
	require.NoError(t, err)

	w, h := img.Bounds().Dx(), img.Bounds().Dy()
	assert.Equal(t, w, h, "el encuadre queda cuadrado")

	// Esquina superior = fondo de estudio (claro).
	cr, cg, cb, _ := img.At(0, 0).RGBA()
	assert.Greater(t, cr>>8, uint32(235))
	assert.Greater(t, cg>>8, uint32(235))
	assert.Greater(t, cb>>8, uint32(235))

	// Centro = producto real (rojo conservado).
	mr, mg, mb, _ := img.At(w/2, h/2).RGBA()
	assert.Greater(t, mr>>8, uint32(140), "producto conserva rojo")
	assert.Less(t, mg>>8, uint32(160))
	assert.Less(t, mb>>8, uint32(160))
}

func TestRemoveBackground_SinKey_Error(t *testing.T) {
	t.Setenv("REMOVEBG_API_KEY", "")
	assert.False(t, RemoveBackgroundConfigured())
	_, err := RemoveBackground(context.Background(), []byte("x"))
	require.Error(t, err, "sin REMOVEBG_API_KEY debe fallar (el worker hace fallback)")
}

