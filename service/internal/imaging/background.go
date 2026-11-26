package imaging

import (
	"image"
	"image/color"
	"math"
	"strconv"
	"strings"

	xdraw "golang.org/x/image/draw"

	"github.com/ioimg/ioimg/service/pkg/ops"
)

// backgroundFor resolves the request's background into a concrete RGBA colour.
// An unset background is transparent for padding/extending and white for
// flattening (imgproxy's default). bga_ scales the alpha of either.
func backgroundFor(o ops.Ops, defaultWhite bool) color.NRGBA {
	c := color.NRGBA{A: 0}
	if defaultWhite {
		c = color.NRGBA{R: 255, G: 255, B: 255, A: 255}
	}
	if strings.EqualFold(o.Background, "transparent") {
		c = color.NRGBA{}
	} else if o.Background != "" {
		raw := strings.TrimPrefix(strings.ToLower(o.Background), "#")
		if n, err := strconv.ParseUint(raw, 16, 32); err == nil {
			switch len(raw) {
			case 8:
				c = color.NRGBA{R: uint8(n >> 24), G: uint8(n >> 16), B: uint8(n >> 8), A: uint8(n)}
			case 6:
				c = color.NRGBA{R: uint8(n >> 16), G: uint8(n >> 8), B: uint8(n), A: 255}
			}
		}
	}
	if a := o.BackgroundAlphaMultiplier(); a != 1 {
		c.A = uint8(math.Round(float64(c.A) * a))
	}
	return c
}

// backgroundForJPEG keeps the historical JPEG behaviour: a transparent or
// unset background becomes opaque white so alpha does not turn black.
func backgroundForJPEG(o ops.Ops) color.NRGBA {
	c := backgroundFor(o, true)
	if c.A == 0 {
		return color.NRGBA{R: 255, G: 255, B: 255, A: 255}
	}
	return c
}

// flattenImage composites src over bg. The vips backend mirrors this with
// flattenVips so opaque and semi-transparent backgrounds produce the same
// pixels in both pipelines.
func flattenImage(src image.Image, bg color.NRGBA) *image.NRGBA {
	b := src.Bounds()
	dst := image.NewNRGBA(image.Rect(0, 0, b.Dx(), b.Dy()))
	fillCanvas(dst, bg)
	xdraw.Draw(dst, dst.Bounds(), src, b.Min, xdraw.Over)
	return dst
}

// extendImage embeds src into the requested w*DPR by h*DPR canvas when it is
// smaller, filling the remainder with bg. A missing requested side keeps the
// current size on that axis.
func extendImage(src image.Image, o ops.Ops, bg color.NRGBA) image.Image {
	b := src.Bounds()
	w, h := b.Dx(), b.Dy()
	tw, th := o.EffectiveWidth(), o.EffectiveHeight()
	if o.WidthAuto || tw <= 0 {
		tw = w
	}
	if o.HeightAuto || th <= 0 {
		th = h
	}
	if tw < w {
		tw = w
	}
	if th < h {
		th = h
	}
	if tw == w && th == h {
		return src
	}
	g := o.ExtendGravity
	if g == "" {
		g = ops.GravityCenter
	}
	dx, dy := anchor(w, h, tw, th, g)
	dst := image.NewNRGBA(image.Rect(0, 0, tw, th))
	fillCanvas(dst, bg)
	xdraw.Draw(dst, image.Rect(dx, dy, dx+w, dy+h), src, b.Min, xdraw.Over)
	return dst
}

func fillCanvas(dst *image.NRGBA, c color.NRGBA) {
	xdraw.Draw(dst, dst.Bounds(), &image.Uniform{C: c}, image.Point{}, xdraw.Src)
}
