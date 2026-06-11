// Spec: specs/042-modulo-eventos/spec.md
package services

import (
	"bytes"
	"fmt"
	"image"
	"image/color"
	_ "image/jpeg" // decode JPEG inputs
	"image/png"
	"math"

	xdraw "golang.org/x/image/draw"
	_ "golang.org/x/image/webp" // decode WebP inputs (el logo del negocio es .webp)
)

const (
	// logoMaxDim caps the output so the PNG stays light (like the cleaned
	// signature). A flat-color 512px logo PNG is tens of KB.
	logoMaxDim = 512
	// bgSeedTol: a border pixel within this color distance of the sampled
	// background color is treated as background (flood-fill seed/grow).
	bgSeedTol = 42.0
	// Feather window for the boundary between logo and erased background, so the
	// cut is anti-aliased instead of jagged. Only pixels that touch the erased
	// region AND are this close to the background color get a partial alpha —
	// interior whites (not touching the background) are never feathered.
	bgFeatherInner = 24.0
	bgFeatherOuter = 70.0
)

// RemoveLogoBackground knocks out ONLY the border-connected background of a logo
// (the white card around it) and returns a true-transparent PNG, preserving the
// logo's own colors AND any white that lives INSIDE the mark.
//
// Unlike MakeSignatureTransparent — which flattens everything to one ink color
// and drops EVERY near-white pixel (right for dark ink on white, wrong for a
// colored logo) — this is an edge-connected flood fill: a pixel is erased only
// if it is near the background color AND reachable from the image border through
// other background pixels. Interior whites, enclosed by the logo, are never
// reached, so they stay opaque and keep their color. The result is cropped to
// content and capped at logoMaxDim so the file stays manageable (FR-12, IMG_4099).
func RemoveLogoBackground(data []byte) ([]byte, error) {
	src, _, err := image.Decode(bytes.NewReader(data))
	if err != nil {
		return nil, fmt.Errorf("decodificar logo: %w", err)
	}
	b := src.Bounds()
	w, h := b.Dx(), b.Dy()
	if w == 0 || h == 0 {
		return nil, fmt.Errorf("logo vacío")
	}

	// Normalize to a 0-origin NRGBA grid for easy indexing.
	rgba := image.NewNRGBA(image.Rect(0, 0, w, h))
	xdraw.Copy(rgba, image.Point{}, src, b, xdraw.Src, nil)

	bg := sampleBackgroundColor(rgba, w, h)

	// Flood fill from every border pixel that matches the background color.
	isBG := make([]bool, w*h)
	visited := make([]bool, w*h)
	queue := make([]int, 0, w*h/4+1)
	push := func(x, y int) {
		idx := y*w + x
		if visited[idx] {
			return
		}
		visited[idx] = true
		if pixelIsBackground(rgba, x, y, bg) {
			isBG[idx] = true
			queue = append(queue, idx)
		}
	}
	for x := 0; x < w; x++ {
		push(x, 0)
		push(x, h-1)
	}
	for y := 0; y < h; y++ {
		push(0, y)
		push(w-1, y)
	}
	for len(queue) > 0 {
		idx := queue[len(queue)-1]
		queue = queue[:len(queue)-1]
		x, y := idx%w, idx/w
		if x > 0 {
			push(x-1, y)
		}
		if x < w-1 {
			push(x+1, y)
		}
		if y > 0 {
			push(x, y-1)
		}
		if y < h-1 {
			push(x, y+1)
		}
	}

	// Build output: background → transparent; everything else keeps its color,
	// with the boundary feathered for a clean edge. Track the content bbox.
	out := image.NewNRGBA(image.Rect(0, 0, w, h))
	minX, minY, maxX, maxY := w, h, -1, -1
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			idx := y*w + x
			c := rgba.NRGBAAt(x, y)
			if isBG[idx] || c.A == 0 {
				continue // erased / already transparent
			}
			a := uint8(255)
			if hasBackgroundNeighbor(isBG, x, y, w, h) {
				d := colorDistance(c, bg)
				switch {
				case d <= bgFeatherInner:
					a = 0
				case d < bgFeatherOuter:
					a = uint8((d - bgFeatherInner) / (bgFeatherOuter - bgFeatherInner) * 255)
				}
			}
			if a == 0 {
				continue
			}
			out.SetNRGBA(x, y, color.NRGBA{R: c.R, G: c.G, B: c.B, A: a})
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

	if maxX < minX || maxY < minY {
		return nil, fmt.Errorf("el logo quedó vacío tras quitar el fondo")
	}

	// Crop to content with a small margin, then cap the size.
	const pad = 4
	rect := image.Rect(
		max(0, minX-pad), max(0, minY-pad),
		min(w, maxX+1+pad), min(h, maxY+1+pad),
	)
	cropped := out.SubImage(rect).(*image.NRGBA)
	final := capLogoSize(cropped)

	var buf bytes.Buffer
	if err := png.Encode(&buf, final); err != nil {
		return nil, fmt.Errorf("codificar logo PNG: %w", err)
	}
	return buf.Bytes(), nil
}

// sampleBackgroundColor estimates the card color from the four corners, so the
// fill works for white, cream or light-gray backgrounds (not only pure white).
func sampleBackgroundColor(img *image.NRGBA, w, h int) color.NRGBA {
	var sr, sg, sb, n uint64
	sample := func(x, y int) {
		c := img.NRGBAAt(x, y)
		if c.A == 0 {
			return
		}
		sr += uint64(c.R)
		sg += uint64(c.G)
		sb += uint64(c.B)
		n++
	}
	const k = 3 // a 3×3 patch in each corner
	for dy := 0; dy < k && dy < h; dy++ {
		for dx := 0; dx < k && dx < w; dx++ {
			sample(dx, dy)
			sample(w-1-dx, dy)
			sample(dx, h-1-dy)
			sample(w-1-dx, h-1-dy)
		}
	}
	if n == 0 {
		return color.NRGBA{R: 255, G: 255, B: 255, A: 255}
	}
	return color.NRGBA{R: uint8(sr / n), G: uint8(sg / n), B: uint8(sb / n), A: 255}
}

func pixelIsBackground(img *image.NRGBA, x, y int, bg color.NRGBA) bool {
	c := img.NRGBAAt(x, y)
	if c.A == 0 {
		return true // already-transparent pixels count as background
	}
	return colorDistance(c, bg) <= bgSeedTol
}

func colorDistance(a, b color.NRGBA) float64 {
	dr := float64(a.R) - float64(b.R)
	dg := float64(a.G) - float64(b.G)
	db := float64(a.B) - float64(b.B)
	return math.Sqrt(dr*dr + dg*dg + db*db)
}

func hasBackgroundNeighbor(isBG []bool, x, y, w, h int) bool {
	if x > 0 && isBG[y*w+x-1] {
		return true
	}
	if x < w-1 && isBG[y*w+x+1] {
		return true
	}
	if y > 0 && isBG[(y-1)*w+x] {
		return true
	}
	if y < h-1 && isBG[(y+1)*w+x] {
		return true
	}
	return false
}

// capLogoSize re-origins the cropped image to (0,0) and, if it exceeds
// logoMaxDim on either side, downscales it with high-quality resampling so the
// PNG stays light.
func capLogoSize(img *image.NRGBA) *image.NRGBA {
	b := img.Bounds()
	w, h := b.Dx(), b.Dy()
	if w <= logoMaxDim && h <= logoMaxDim {
		dst := image.NewNRGBA(image.Rect(0, 0, w, h))
		xdraw.Copy(dst, image.Point{}, img, b, xdraw.Src, nil)
		return dst
	}
	scale := float64(logoMaxDim) / float64(max(w, h))
	nw, nh := max(1, int(float64(w)*scale)), max(1, int(float64(h)*scale))
	dst := image.NewNRGBA(image.Rect(0, 0, nw, nh))
	xdraw.CatmullRom.Scale(dst, dst.Bounds(), img, b, xdraw.Over, nil)
	return dst
}
