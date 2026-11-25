package imaging

import (
	"image"
	"image/color"

	xdraw "golang.org/x/image/draw"
	"image/draw"
	"math"

	"github.com/ioimg/ioimg/service/pkg/ops"
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
	if w.Scale > 0 {
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
