// Colour effects shared by both processor builds: imgproxy's duotone and
// colorize. They are pure per-pixel colour math, so a single implementation
// keeps the pure-Go and libvips paths comparable.
package imaging

import (
	"image"
	"math"

	"github.com/ioimg/ioimg/service/pkg/ops"
)

// applyColorEffects applies colorize first, then duotone, then the gradient
// overlay. Colorize runs before duotone so the duotone palette sees the
// colorized luminance; the gradient sits on top because it is a vignette.
// Everything is a no-op at intensity/opacity 0.
func applyColorEffects(src image.Image, o ops.Ops) image.Image {
	col := o.Colorize
	dt := o.Duotone
	gr := o.Gradient
	if col.Opacity <= 0 && dt.Intensity <= 0 && gr.IsZero() {
		return src
	}

	img := nrgbaOf(src)
	b := img.Bounds()
	w, h := b.Dx(), b.Dy()
	dst := image.NewNRGBA(image.Rect(0, 0, w, h))

	// Duotone palette: luminance -> linear blend between the two colours.
	var palette [256][3]float64
	if dt.Intensity > 0 {
		for i := range palette {
			t := float64(i) / 255
			palette[i][0] = (1-t)*float64(dt.Color1.R) + t*float64(dt.Color2.R)
			palette[i][1] = (1-t)*float64(dt.Color1.G) + t*float64(dt.Color2.G)
			palette[i][2] = (1-t)*float64(dt.Color1.B) + t*float64(dt.Color2.B)
		}
	}

	op := col.Opacity
	keepAlpha := !(op > 0 && !col.KeepAlpha)

	// Gradient geometry, precomputed once: project each pixel onto the direction
	// axis and normalise the projection to [0,1] over the image box.
	var gdx, gdy, gpmin, gspan float64
	if !gr.IsZero() {
		rad := gr.Angle() * math.Pi / 180
		gdx, gdy = -math.Sin(rad), math.Cos(rad)
		// Projections of the four corners cover the whole box.
		p00 := 0*gdx + 0*gdy
		p10 := float64(w-1)*gdx + 0*gdy
		p01 := 0*gdx + float64(h-1)*gdy
		p11 := float64(w-1)*gdx + float64(h-1)*gdy
		gpmin = math.Min(math.Min(p00, p10), math.Min(p01, p11))
		gpmax := math.Max(math.Max(p00, p10), math.Max(p01, p11))
		gspan = gpmax - gpmin
	}
	for y := range h {
		for x := range w {
			so := img.PixOffset(x, y)
			do := dst.PixOffset(x, y)

			r := float64(img.Pix[so])
			g := float64(img.Pix[so+1])
			blue := float64(img.Pix[so+2])

			if op > 0 {
				r = r*(1-op) + float64(col.Color.R)*op
				g = g*(1-op) + float64(col.Color.G)*op
				blue = blue*(1-op) + float64(col.Color.B)*op
			}
			if dt.Intensity > 0 {
				lum := int(0.299*r + 0.587*g + 0.114*blue + 0.5)
				if lum > 255 {
					lum = 255
				}
				p := palette[lum]
				r = r*(1-dt.Intensity) + p[0]*dt.Intensity
				g = g*(1-dt.Intensity) + p[1]*dt.Intensity
				blue = blue*(1-dt.Intensity) + p[2]*dt.Intensity
			}

			if !gr.IsZero() {
				t := 0.0
				if gspan != 0 {
					t = (float64(x)*gdx + float64(y)*gdy - gpmin) / gspan
				}
				ramp := (t - gr.Start) / (gr.Stop - gr.Start)
				if ramp < 0 {
					ramp = 0
				} else if ramp > 1 {
					ramp = 1
				}
				a := ramp * gr.Opacity
				r = r*(1-a) + float64(gr.Color.R)*a
				g = g*(1-a) + float64(gr.Color.G)*a
				blue = blue*(1-a) + float64(gr.Color.B)*a
			}

			dst.Pix[do] = clamp8(r)
			dst.Pix[do+1] = clamp8(g)
			dst.Pix[do+2] = clamp8(blue)
			if keepAlpha {
				dst.Pix[do+3] = img.Pix[so+3]
			} else {
				dst.Pix[do+3] = 0xff
			}
		}
	}
	return dst
}

// nrgbaOf converts any image to *image.NRGBA, reusing it when already NRGBA.
// Unlike image/draw it keeps the alpha channel as-is, which colorize's
// keep_alpha depends on.
func nrgbaOf(src image.Image) *image.NRGBA {
	if n, ok := src.(*image.NRGBA); ok {
		return n
	}
	b := src.Bounds()
	dst := image.NewNRGBA(image.Rect(0, 0, b.Dx(), b.Dy()))
	for y := range dst.Bounds().Dy() {
		for x := range dst.Bounds().Dx() {
			c := src.At(b.Min.X+x, b.Min.Y+y)
			do := dst.PixOffset(x, y)
			r, g, bl, a := c.RGBA()
			if a == 0 {
				continue
			}
			// Un-premultiply so NRGBA stays straightforward.
			dst.Pix[do] = uint8(uint32(r) * 0xffff / a >> 8)
			dst.Pix[do+1] = uint8(uint32(g) * 0xffff / a >> 8)
			dst.Pix[do+2] = uint8(uint32(bl) * 0xffff / a >> 8)
			dst.Pix[do+3] = uint8(a >> 8)
		}
	}
	return dst
}

func clamp8(v float64) uint8 {
	switch {
	case v <= 0:
		return 0
	case v >= 255:
		return 255
	default:
		return uint8(v + 0.5)
	}
}
