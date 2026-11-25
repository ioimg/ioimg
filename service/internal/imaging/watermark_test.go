//go:build !vips

package imaging

import (
	"image"
	"image/color"
	"testing"

	"github.com/ioimg/ioimg/service/pkg/ops"
)

func solid(w, h int, c color.NRGBA) image.Image {
	img := image.NewNRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			img.SetNRGBA(x, y, c)
		}
	}
	return img
}

func TestApplyWatermarkPositions(t *testing.T) {
	base := solid(100, 100, color.NRGBA{R: 0, G: 0, B: 0, A: 255})
	wm := solid(20, 10, color.NRGBA{R: 255, G: 0, B: 0, A: 255})

	cases := []struct {
		pos  string
		x, y int
	}{
		{ops.WatermarkCenter, 41, 46},
		{ops.WatermarkNorthWest, 1, 1},
		{ops.WatermarkSouthEast, 80, 90},
	}
	for _, c := range cases {
		out := applyWatermark(base, wm, ops.Watermark{Opacity: 1, Position: c.pos})
		got := color.NRGBAModel.Convert(out.At(c.x, c.y)).(color.NRGBA)
		if got.R < 200 {
			t.Errorf("%s: pixel at %d,%d not covered: %+v", c.pos, c.x, c.y, got)
		}
	}
	// Offsets are absolute when |v| >= 1.
	out := applyWatermark(base, wm, ops.Watermark{Opacity: 1, Position: ops.WatermarkNorthWest, XOffset: 5, YOffset: 7})
	if got := color.NRGBAModel.Convert(out.At(6, 8)).(color.NRGBA); got.R < 200 {
		t.Errorf("absolute offset not applied: %+v", got)
	}
	// Fractional offsets are relative to the result size.
	out = applyWatermark(base, wm, ops.Watermark{Opacity: 1, Position: ops.WatermarkNorthWest, XOffset: 0.5})
	if got := color.NRGBAModel.Convert(out.At(51, 1)).(color.NRGBA); got.R < 200 {
		t.Errorf("relative offset not applied: %+v", got)
	}
}

func TestApplyWatermarkScaleAndOpacity(t *testing.T) {
	base := solid(200, 100, color.NRGBA{A: 255})
	wm := solid(10, 10, color.NRGBA{R: 255, A: 255})

	// scale 0.5 must cover half the width: 100×100 at the centre.
	out := applyWatermark(base, wm, ops.Watermark{Opacity: 1, Position: ops.WatermarkCenter, Scale: 0.5})
	if got := color.NRGBAModel.Convert(out.At(100, 50)).(color.NRGBA); got.R < 200 {
		t.Errorf("scaled watermark not centred: %+v", got)
	}
	if got := color.NRGBAModel.Convert(out.At(5, 5)).(color.NRGBA); got.R > 10 {
		t.Errorf("watermark leaked outside its box: %+v", got)
	}

	// opacity 0.5 over black must land near half intensity, not full.
	out = applyWatermark(base, wm, ops.Watermark{Opacity: 0.5, Position: ops.WatermarkCenter})
	got := color.NRGBAModel.Convert(out.At(100, 50)).(color.NRGBA)
	if got.R < 110 || got.R > 145 {
		t.Fatalf("opacity 0.5 produced %d, want ~128", got.R)
	}
}

func TestApplyWatermarkRepeat(t *testing.T) {
	base := solid(60, 30, color.NRGBA{A: 255})
	wm := solid(5, 5, color.NRGBA{R: 255, A: 255})
	out := applyWatermark(base, wm, ops.Watermark{Opacity: 1, Position: ops.WatermarkRepeat})
	for _, p := range [][2]int{{2, 2}, {32, 2}, {57, 27}} {
		if got := color.NRGBAModel.Convert(out.At(p[0], p[1])).(color.NRGBA); got.R < 200 {
			t.Errorf("tile missing at %v: %+v", p, got)
		}
	}
}
