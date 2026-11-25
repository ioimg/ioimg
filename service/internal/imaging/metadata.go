//go:build !vips

package imaging

import (
	"fmt"
	"image"
)

// DominantColor returns an approximate average colour as #rrggbb, sampled on a
// coarse grid so large images stay cheap.
func DominantColor(img image.Image) string {
	b := img.Bounds()
	w, h := b.Dx(), b.Dy()
	if w == 0 || h == 0 {
		return "#000000"
	}
	step := max(1, min(w, h)/32)
	var r, g, bl, n uint64
	for y := b.Min.Y; y < b.Max.Y; y += step {
		for x := b.Min.X; x < b.Max.X; x += step {
			r0, g0, b0, a0 := img.At(x, y).RGBA()
			if a0 == 0 {
				continue
			}
			r += uint64(r0 >> 8)
			g += uint64(g0 >> 8)
			bl += uint64(b0 >> 8)
			n++
		}
	}
	if n == 0 {
		return "#000000"
	}
	return fmt.Sprintf("#%02x%02x%02x", r/n, g/n, bl/n)
}
