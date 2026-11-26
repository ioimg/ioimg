package imaging

import (
	"image"
	"image/color"
	"image/draw"
	"math"
	"sync"

	"github.com/ioimg/ioimg/service/pkg/ops"
	xdraw "golang.org/x/image/draw"
	"golang.org/x/image/font"
	"golang.org/x/image/font/gofont/goregular"
	"golang.org/x/image/font/opentype"
	"golang.org/x/image/math/fixed"
)

// applyWatermark composites wm over base according to imgproxy's wm_ semantics:
// scale is relative to the result width, offsets with |v| < 1 are fractions of
// the result, and "re"/"ch" tile the watermark.
func applyWatermark(base, wm image.Image, w ops.Watermark) image.Image {
	if wm == nil || w.Opacity <= 0 {
		return base
	}
	b := base.Bounds()
	bw, bh := b.Dx(), b.Dy()
	if bw <= 0 || bh <= 0 {
		return base
	}

	overlay := wm
	// wms_ is an explicit fit size and wins over wm_'s relative scale.
	if w.Size != nil {
		overlay = fitWatermarkSize(overlay, *w.Size)
	} else if w.Scale > 0 {
		targetW := int(math.Round(w.Scale * float64(bw)))
		if targetW < 1 {
			targetW = 1
		}
		wb := wm.Bounds()
		targetH := int(math.Round(float64(wb.Dy()) * float64(targetW) / float64(wb.Dx())))
		if targetH < 1 {
			targetH = 1
		}
		overlay = resizeTo(wm, targetW, targetH)
	}
	if w.Rotate != 0 {
		overlay = rotateWatermark(overlay, w.Rotate)
	}
	if w.Shadow > 0 {
		overlay = shadowWatermark(overlay, w.Shadow)
	}
	overlay = scaleAlpha(overlay, w.Opacity)
	ob := overlay.Bounds()
	ow, oh := ob.Dx(), ob.Dy()

	dst := image.NewNRGBA(image.Rect(0, 0, bw, bh))
	draw.Draw(dst, dst.Bounds(), base, b.Min, draw.Src)

	place := func(x, y int) {
		r := image.Rect(x, y, x+ow, y+oh)
		draw.DrawMask(dst, r, overlay, ob.Min, nil, image.Point{}, draw.Over)
	}

	if w.Position == ops.WatermarkRepeat || w.Position == ops.WatermarkChessboard {
		stepX, stepY := ow, oh
		if w.XOffset != 0 {
			stepX = offsetPx(w.XOffset, bw)
		}
		if w.YOffset != 0 {
			stepY = offsetPx(w.YOffset, bh)
		}
		if stepX < 1 {
			stepX = ow
		}
		if stepY < 1 {
			stepY = oh
		}
		row := 0
		for y := 0; y < bh; y += stepY {
			shift := 0
			if w.Position == ops.WatermarkChessboard && row%2 == 1 {
				shift = stepX / 2
			}
			for x := -shift; x < bw; x += stepX {
				place(x+offsetPx(w.XOffset, bw), y+offsetPx(w.YOffset, bh))
			}
			row++
		}
		return dst
	}

	x, y := watermarkOrigin(w.Position, bw, bh, ow, oh)
	x += offsetPx(w.XOffset, bw)
	y += offsetPx(w.YOffset, bh)
	place(x, y)
	return dst
}

// offsetPx resolves imgproxy's offset rule: |v| >= 1 is pixels, otherwise it is
// a fraction of the corresponding image dimension.
func offsetPx(v float64, dim int) int {
	if v == 0 {
		return 0
	}
	if math.Abs(v) >= 1 {
		return int(math.Round(v))
	}
	return int(math.Round(v * float64(dim)))
}

func watermarkOrigin(position string, bw, bh, ow, oh int) (int, int) {
	left, right := 0, bw-ow
	top, bottom := 0, bh-oh
	cx, cy := (bw-ow)/2, (bh-oh)/2
	switch position {
	case ops.WatermarkNorth:
		return cx, top
	case ops.WatermarkSouth:
		return cx, bottom
	case ops.WatermarkEast:
		return right, cy
	case ops.WatermarkWest:
		return left, cy
	case ops.WatermarkNorthEast:
		return right, top
	case ops.WatermarkNorthWest:
		return left, top
	case ops.WatermarkSouthEast:
		return right, bottom
	case ops.WatermarkSouthWest:
		return left, bottom
	}
	return cx, cy
}

// resizeTo scales an image to exactly w×h with a Catmull-Rom kernel.
func resizeTo(src image.Image, w, h int) image.Image {
	sb := src.Bounds()
	if sb.Dx() < 1 || sb.Dy() < 1 {
		return image.NewNRGBA(image.Rect(0, 0, w, h))
	}
	// A watermark is usually far larger than its stamped size (a 2400 px logo at
	// 80 px). A Catmull-Rom pass at that ratio samples the whole kernel support
	// per output pixel — ~70 ms in practice. Pre-shrinking bilinearly to twice
	// the target keeps the final kernel small.
	if sb.Dx() > 4*w {
		mw := 2 * w
		mh := max(1, mw*sb.Dy()/sb.Dx())
		mid := image.NewNRGBA(image.Rect(0, 0, mw, mh))
		xdraw.ApproxBiLinear.Scale(mid, mid.Bounds(), src, sb, xdraw.Src, nil)
		src, sb = mid, mid.Bounds()
	}
	dst := image.NewNRGBA(image.Rect(0, 0, w, h))
	xdraw.CatmullRom.Scale(dst, dst.Bounds(), src, sb, xdraw.Src, nil)
	return dst
}

// scaleAlpha multiplies every alpha by opacity; opaque watermarks would
// otherwise ignore wm opacity entirely.
func scaleAlpha(src image.Image, opacity float64) image.Image {
	if opacity >= 1 {
		return src
	}
	b := src.Bounds()
	dst := image.NewNRGBA(image.Rect(0, 0, b.Dx(), b.Dy()))
	f := opacity
	for y := 0; y < b.Dy(); y++ {
		for x := 0; x < b.Dx(); x++ {
			c := color.NRGBAModel.Convert(src.At(b.Min.X+x, b.Min.Y+y)).(color.NRGBA)
			c.A = uint8(float64(c.A) * f)
			dst.SetNRGBA(x, y, c)
		}
	}
	return dst
}

// fitWatermarkSize applies wms_: fit inside the requested box while preserving
// the aspect ratio, enlarging when the watermark is smaller. A zero side is
// derived from the other side.
func fitWatermarkSize(src image.Image, size ops.Size) image.Image {
	b := src.Bounds()
	sw, sh := b.Dx(), b.Dy()
	if sw < 1 || sh < 1 {
		return src
	}
	tw, th := size.Width, size.Height
	switch {
	case tw == 0 && th == 0:
		return src
	case tw == 0:
		tw = max(1, int(math.Round(float64(sw)*float64(th)/float64(sh))))
		return resizeTo(src, tw, th)
	case th == 0:
		th = max(1, int(math.Round(float64(sh)*float64(tw)/float64(sw))))
		return resizeTo(src, tw, th)
	}
	scale := math.Min(float64(tw)/float64(sw), float64(th)/float64(sh))
	nw := max(1, int(math.Round(float64(sw)*scale)))
	nh := max(1, int(math.Round(float64(sh)*scale)))
	return resizeTo(src, nw, nh)
}

// rotateWatermark rotates the watermark clockwise by degrees with bilinear
// sampling. The canvas grows to the rotated bounding box; transparent pixels
// stay transparent and alpha is interpolated in premultiplied space.
func rotateWatermark(src image.Image, degrees float64) image.Image {
	deg := math.Mod(degrees, 360)
	if deg < 0 {
		deg += 360
	}
	if deg == 0 {
		return src
	}
	img := nrgbaOf(src)
	b := img.Bounds()
	w, h := b.Dx(), b.Dy()
	if w < 1 || h < 1 {
		return src
	}
	rad := deg * math.Pi / 180
	c, s := math.Cos(rad), math.Sin(rad)
	// The epsilon keeps exact quarter turns pixel-exact despite cos(90°) noise.
	nw := int(math.Ceil(math.Abs(float64(w)*c) + math.Abs(float64(h)*s) - 1e-9))
	nh := int(math.Ceil(math.Abs(float64(w)*s) + math.Abs(float64(h)*c) - 1e-9))
	nw, nh = max(1, nw), max(1, nh)
	dst := image.NewNRGBA(image.Rect(0, 0, nw, nh))
	cx, cy := float64(w)/2, float64(h)/2
	ncx, ncy := float64(nw)/2, float64(nh)/2
	for y := 0; y < nh; y++ {
		for x := 0; x < nw; x++ {
			dx := float64(x) + 0.5 - ncx
			dy := float64(y) + 0.5 - ncy
			// Inverse of the clockwise rotation: source = R(-deg) * dest.
			sx := dx*c + dy*s + cx - 0.5 - float64(b.Min.X)
			sy := -dx*s + dy*c + cy - 0.5 - float64(b.Min.Y)
			dst.SetNRGBA(x, y, bilinearNRGBA(img, sx, sy))
		}
	}
	return dst
}

func bilinearNRGBA(src *image.NRGBA, x, y float64) color.NRGBA {
	x0, y0 := int(math.Floor(x)), int(math.Floor(y))
	fx, fy := x-float64(x0), y-float64(y0)
	b := src.Bounds()
	sample := func(px, py int) (r, g, bl, a float64) {
		if px < b.Min.X || py < b.Min.Y || px >= b.Max.X || py >= b.Max.Y {
			return 0, 0, 0, 0
		}
		o := src.PixOffset(px, py)
		alpha := float64(src.Pix[o+3]) / 255
		return float64(src.Pix[o]) / 255 * alpha, float64(src.Pix[o+1]) / 255 * alpha,
			float64(src.Pix[o+2]) / 255 * alpha, alpha
	}
	type corner struct{ r, g, b, a float64 }
	var p [4]corner
	p[0].r, p[0].g, p[0].b, p[0].a = sample(x0, y0)
	p[1].r, p[1].g, p[1].b, p[1].a = sample(x0+1, y0)
	p[2].r, p[2].g, p[2].b, p[2].a = sample(x0, y0+1)
	p[3].r, p[3].g, p[3].b, p[3].a = sample(x0+1, y0+1)
	at := func(i int) float64 {
		wi := 1.0
		if i == 0 || i == 1 {
			wi *= 1 - fx
		} else {
			wi *= fx
		}
		if i < 2 {
			wi *= 1 - fy
		} else {
			wi *= fy
		}
		return wi
	}
	var r, g, bl, a float64
	for i := range p {
		wi := at(i)
		r += p[i].r * wi
		g += p[i].g * wi
		bl += p[i].b * wi
		a += p[i].a * wi
	}
	if a <= 1e-6 {
		return color.NRGBA{}
	}
	clamp := func(v float64) uint8 {
		return uint8(math.Round(math.Max(0, math.Min(1, v)) * 255))
	}
	return color.NRGBA{
		R: clamp(r / a),
		G: clamp(g / a),
		B: clamp(bl / a),
		A: clamp(a),
	}
}

// shadowWatermark adds imgproxy's wmsh_ shadow: the watermark alpha is blurred
// by sigma and the black shadow is offset down-right by sigma/2 (rounded) and
// composited underneath the watermark.
func shadowWatermark(src image.Image, sigma float64) image.Image {
	if sigma <= 0 {
		return src
	}
	// Keep a hostile wmsh_ from allocating an enormous canvas. The practical
	// shadow reach is a few sigma anyway.
	sigma = math.Min(sigma, 64)
	reach := max(1, int(math.Ceil(sigma)))
	offset := int(math.Round(sigma / 2))
	img := nrgbaOf(src)
	b := img.Bounds()
	w, h := b.Dx(), b.Dy()
	if w < 1 || h < 1 {
		return src
	}
	pad := 3 * reach
	cw, ch := w+2*pad+offset, h+2*pad+offset
	mask := image.NewAlpha(image.Rect(0, 0, cw, ch))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			c := color.NRGBAModel.Convert(img.At(b.Min.X+x, b.Min.Y+y)).(color.NRGBA)
			mask.SetAlpha(pad+x, pad+y, color.Alpha{A: c.A})
		}
	}
	// Three box passes approximate a Gaussian sigma blur.
	r := max(1, int(math.Round(sigma)))
	for i := 0; i < 3; i++ {
		mask = boxBlurAlpha(mask, r)
	}
	layer := image.NewNRGBA(image.Rect(0, 0, cw, ch))
	for y := 0; y < ch; y++ {
		for x := 0; x < cw; x++ {
			if x < offset || y < offset {
				continue
			}
			a := mask.AlphaAt(x-offset, y-offset).A
			if a == 0 {
				continue
			}
			o := layer.PixOffset(x, y)
			layer.Pix[o+3] = a // black shadow
		}
	}
	draw.Draw(layer, image.Rect(pad, pad, pad+w, pad+h), img, b.Min, draw.Over)
	return layer
}

func boxBlurAlpha(src *image.Alpha, r int) *image.Alpha {
	w, h := src.Bounds().Dx(), src.Bounds().Dy()
	tmp := image.NewAlpha(image.Rect(0, 0, w, h))
	dst := image.NewAlpha(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		var sum, count int
		for x := -r; x <= r; x++ {
			if x >= 0 && x < w {
				sum += int(src.AlphaAt(x, y).A)
				count++
			}
		}
		for x := 0; x < w; x++ {
			if count > 0 {
				tmp.SetAlpha(x, y, color.Alpha{A: uint8(sum / count)})
			}
			if nx := x - r; nx >= 0 && nx < w {
				sum -= int(src.AlphaAt(nx, y).A)
				count--
			}
			if nx := x + r + 1; nx >= 0 && nx < w {
				sum += int(src.AlphaAt(nx, y).A)
				count++
			}
		}
	}
	for x := 0; x < w; x++ {
		var sum, count int
		for y := -r; y <= r; y++ {
			if y >= 0 && y < h {
				sum += int(tmp.AlphaAt(x, y).A)
				count++
			}
		}
		for y := 0; y < h; y++ {
			if count > 0 {
				dst.SetAlpha(x, y, color.Alpha{A: uint8(sum / count)})
			}
			if ny := y - r; ny >= 0 && ny < h {
				sum -= int(tmp.AlphaAt(x, ny).A)
				count--
			}
			if ny := y + r + 1; ny >= 0 && ny < h {
				sum += int(tmp.AlphaAt(x, ny).A)
				count++
			}
		}
	}
	return dst
}

var (
	watermarkTextCache sync.Map // map[string]image.Image
	watermarkFontOnce  sync.Once
	watermarkFont      *opentype.Font
)

// RenderWatermarkText renders wmt_ text to a transparent image using the
// embedded Go Regular font at 16 pt, black. Rendered text is memoised by its
// exact content. Pango markup is not interpreted: the text is drawn literally.
func RenderWatermarkText(text string) image.Image {
	if v, ok := watermarkTextCache.Load(text); ok {
		return v.(image.Image)
	}
	img := renderWatermarkText(text)
	actual, _ := watermarkTextCache.LoadOrStore(text, img)
	return actual.(image.Image)
}

func renderWatermarkText(text string) image.Image {
	watermarkFontOnce.Do(func() {
		watermarkFont, _ = opentype.Parse(goregular.TTF)
	})
	if watermarkFont == nil {
		return image.NewNRGBA(image.Rect(0, 0, 1, 1))
	}
	face, err := opentype.NewFace(watermarkFont, &opentype.FaceOptions{
		Size: 16, DPI: 72, Hinting: font.HintingFull,
	})
	if err != nil {
		return image.NewNRGBA(image.Rect(0, 0, 1, 1))
	}
	defer face.Close()

	w := int(math.Ceil(float64(font.MeasureString(face, text)) / 64))
	metrics := face.Metrics()
	ascent, descent := metrics.Ascent.Ceil(), metrics.Descent.Ceil()
	h := ascent + descent
	if w < 1 || h < 1 {
		return image.NewNRGBA(image.Rect(0, 0, 1, 1))
	}
	dst := image.NewNRGBA(image.Rect(0, 0, w, h))
	d := font.Drawer{
		Dst:  dst,
		Src:  image.NewUniform(color.Black),
		Face: face,
		Dot:  fixed.P(0, ascent),
	}
	d.DrawString(text)
	return dst
}
