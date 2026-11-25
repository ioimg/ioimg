package ioimg

import (
	"bytes"
	"fmt"
	"image"
	"image/jpeg"
	"image/png"

	"golang.org/x/image/draw"
)

type Filter uint8

const (
	CatmullRom Filter = iota
	ApproxBiLinear
)

// Transform: decode → resize → encode. Tanpa cache, tanpa I/O.
func Transform(src []byte, o Ops, filter Filter) ([]byte, error) {
	img, _, err := image.Decode(bytes.NewReader(src))
	if err != nil {
		return nil, fmt.Errorf("decode: %w", err)
	}
	img = resize(img, o, filter)
	var buf bytes.Buffer
	switch o.Format {
	case Png:
		if err := png.Encode(&buf, img); err != nil {
			return nil, fmt.Errorf("encode png: %w", err)
		}
	default:
		if err := jpeg.Encode(&buf, img, &jpeg.Options{Quality: o.Quality}); err != nil {
			return nil, fmt.Errorf("encode jpeg: %w", err)
		}
	}
	return buf.Bytes(), nil
}

func resize(src image.Image, o Ops, filter Filter) image.Image {
	sw, sh := src.Bounds().Dx(), src.Bounds().Dy()
	var dw, dh int
	switch {
	case o.W == 0 && o.H == 0:
		return src
	case o.W > 0 && o.H == 0:
		dw = o.W
		dh = max(1, int(float64(sh)*float64(o.W)/float64(sw)+0.5))
	case o.W == 0 && o.H > 0:
		dh = o.H
		dw = max(1, int(float64(sw)*float64(o.H)/float64(sh)+0.5))
	default:
		if o.Fit == Cover {
			// crop tengah ke rasio target lalu skala
			dw, dh = o.W, o.H
			src = cropToRatio(src, o.W, o.H)
			sw, sh = src.Bounds().Dx(), src.Bounds().Dy()
		} else {
			scale := min(float64(o.W)/float64(sw), float64(o.H)/float64(sh))
			dw = max(1, int(float64(sw)*scale+0.5))
			dh = max(1, int(float64(sh)*scale+0.5))
		}
	}
	_ = sh
	dst := image.NewRGBA(image.Rect(0, 0, dw, dh))
	var s draw.Scaler = draw.CatmullRom
	if filter == ApproxBiLinear {
		s = draw.ApproxBiLinear
	}
	s.Scale(dst, dst.Bounds(), src, src.Bounds(), draw.Src, nil)
	return dst
}

func cropToRatio(src image.Image, w, h int) image.Image {
	b := src.Bounds()
	sw, sh := b.Dx(), b.Dy()
	targetRatio := float64(w) / float64(h)
	srcRatio := float64(sw) / float64(sh)
	var cw, ch int
	if srcRatio > targetRatio {
		ch = sh
		cw = int(float64(sh) * targetRatio)
	} else {
		cw = sw
		ch = int(float64(sw) / targetRatio)
	}
	x0 := b.Min.X + (sw-cw)/2
	y0 := b.Min.Y + (sh-ch)/2
	type subImager interface {
		SubImage(r image.Rectangle) image.Image
	}
	if si, ok := src.(subImager); ok {
		return si.SubImage(image.Rect(x0, y0, x0+cw, y0+ch))
	}
	dst := image.NewRGBA(image.Rect(0, 0, cw, ch))
	draw.Draw(dst, dst.Bounds(), src, image.Pt(x0, y0), draw.Src)
	return dst
}
