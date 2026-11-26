//go:build !vips

package imaging

import (
	"bytes"
	"image"
	"image/draw"
	"image/gif"

	"github.com/ioimg/ioimg/service/pkg/ops"
)

// decodeGIF selects and stacks GIF frames. image/gif is the only multi-frame
// decoder in the pure-Go build, so pg_/pgs_ are honoured here and other
// animated formats keep their first-frame behaviour (see docs/IMGPARITY.md).
func decodeGIF(data []byte, o ops.Ops) (image.Image, Info, error) {
	g, err := gif.DecodeAll(bytes.NewReader(data))
	if err != nil || len(g.Image) == 0 {
		return nil, Info{}, ErrDecode
	}
	w, h := g.Config.Width, g.Config.Height
	if w <= 0 || h <= 0 {
		return nil, Info{}, ErrDecode
	}

	page, pages := o.PageIndex(), o.PageCount()
	if page >= len(g.Image) {
		// A page beyond the end falls back to the last available frame.
		page = len(g.Image) - 1
	}
	if pages > len(g.Image)-page {
		// Never fail when the source has fewer frames than requested.
		pages = len(g.Image) - page
	}

	// Coalesce the animation frame by frame, keeping only the requested pages.
	// GIF disposal rules make earlier frames part of a later frame's pixels, so
	// composition has to start at frame 0 even when Page is large.
	canvas := image.NewRGBA(image.Rect(0, 0, w, h))
	var restore *image.RGBA
	selected := make([]*image.RGBA, 0, pages)
	for i, frame := range g.Image {
		if i > 0 {
			switch g.Disposal[i-1] {
			case gif.DisposalBackground:
				draw.Draw(canvas, g.Image[i-1].Bounds(), image.Transparent, image.Point{}, draw.Src)
			case gif.DisposalPrevious:
				if restore != nil {
					canvas = restore
				}
			}
		}
		if i < len(g.Disposal) && g.Disposal[i] == gif.DisposalPrevious {
			restore = cloneRGBA(canvas)
		}
		b := frame.Bounds()
		draw.Draw(canvas, b, frame, b.Min, draw.Over)
		if i >= page && i < page+pages {
			selected = append(selected, cloneRGBA(canvas))
		}
	}

	outH := 0
	for _, frame := range selected {
		outH += frame.Bounds().Dy()
	}
	if w > MaxSide || outH > MaxSide || w*outH > MaxPixels {
		return nil, Info{}, ErrDimension
	}

	out := selected[0]
	if len(selected) > 1 {
		out = image.NewRGBA(image.Rect(0, 0, w, outH))
		y := 0
		for _, frame := range selected {
			fh := frame.Bounds().Dy()
			draw.Draw(out, image.Rect(0, y, w, y+fh), frame, frame.Bounds().Min, draw.Src)
			y += fh
		}
	}

	return out, Info{
		Width:    out.Bounds().Dx(),
		Height:   out.Bounds().Dy(),
		Format:   "gif",
		HasAlpha: hasAlpha(out),
		Animated: len(g.Image) > 1 && !o.DisableAnimation,
	}, nil
}

func cloneRGBA(src *image.RGBA) *image.RGBA {
	dst := image.NewRGBA(src.Bounds())
	copy(dst.Pix, src.Pix)
	return dst
}
