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

func TestWatermarkRotateKeepsCanvasAndRotatesPixels(t *testing.T) {
	base := solid(100, 100, color.NRGBA{A: 255})
	wm := image.NewNRGBA(image.Rect(0, 0, 20, 10))
	// A red square in the source top-left: clockwise rotation moves it to the
	// top-right of the rotated watermark.
	for y := 0; y < 5; y++ {
		for x := 0; x < 5; x++ {
			wm.SetNRGBA(x, y, color.NRGBA{R: 255, A: 255})
		}
	}
	rotated := rotateWatermark(wm, 90)
	if b := rotated.Bounds(); b.Dx() != 10 || b.Dy() != 20 {
		t.Fatalf("rotated bounds %v, want 10x20", b)
	}
	redIn := func(x0, x1, y0, y1 int) bool {
		for y := y0; y < y1; y++ {
			for x := x0; x < x1; x++ {
				if got := color.NRGBAModel.Convert(rotated.At(x, y)).(color.NRGBA); got.R > 150 && got.A > 150 {
					return true
				}
			}
		}
		return false
	}
	if !redIn(5, 10, 0, 10) || redIn(0, 5, 0, 10) {
		t.Fatal("top-left ink did not rotate clockwise to the top-right")
	}

	out := applyWatermark(base, wm, ops.Watermark{Opacity: 1, Position: ops.WatermarkCenter, Rotate: 90})
	if b := out.Bounds(); b.Dx() != 100 || b.Dy() != 100 {
		t.Fatalf("rotate changed the result canvas: %v", b)
	}
	composited := false
	for y := 0; y < 100 && !composited; y++ {
		for x := 0; x < 100; x++ {
			if got := color.NRGBAModel.Convert(out.At(x, y)).(color.NRGBA); got.R > 200 {
				composited = true
				break
			}
		}
	}
	if !composited {
		t.Fatal("rotated watermark not composited")
	}
}

func TestWatermarkSizeFitAndEnlarge(t *testing.T) {
	base := solid(200, 200, color.NRGBA{R: 255, G: 255, B: 255, A: 255})
	wm := solid(10, 20, color.NRGBA{R: 255, A: 255})

	// Fit 10x20 into 100x100 => 50x100, centred.
	out := applyWatermark(base, wm, ops.Watermark{
		Opacity: 1, Position: ops.WatermarkCenter,
		Size: &ops.Size{Width: 100, Height: 100},
	})
	if got := color.NRGBAModel.Convert(out.At(100, 100)).(color.NRGBA); got.R < 200 || got.G > 10 {
		t.Fatalf("fit watermark missing at centre: %+v", got)
	}
	if got := color.NRGBAModel.Convert(out.At(100, 40)).(color.NRGBA); got.G < 200 {
		t.Fatalf("fit watermark exceeded its height: %+v", got)
	}
	if got := color.NRGBAModel.Convert(out.At(70, 100)).(color.NRGBA); got.G < 200 {
		t.Fatalf("fit watermark exceeded its width: %+v", got)
	}

	// A small watermark is enlarged to the requested size, not left native.
	small := solid(10, 10, color.NRGBA{R: 255, A: 255})
	out = applyWatermark(base, small, ops.Watermark{
		Opacity: 1, Position: ops.WatermarkCenter,
		Size: &ops.Size{Width: 40, Height: 40},
	})
	if got := color.NRGBAModel.Convert(out.At(85, 85)).(color.NRGBA); got.R < 200 || got.G > 10 {
		t.Fatalf("wms did not enlarge: %+v", got)
	}
}

func TestWatermarkShadowAddsPixelsOutsideGlyphBox(t *testing.T) {
	base := solid(120, 120, color.NRGBA{R: 255, G: 255, B: 255, A: 255})
	wm := solid(10, 10, color.NRGBA{R: 0, G: 0, B: 0, A: 255})
	out := applyWatermark(base, wm, ops.Watermark{Opacity: 1, Position: ops.WatermarkCenter, Shadow: 4})
	if b := out.Bounds(); b.Dx() != 120 || b.Dy() != 120 {
		t.Fatalf("shadow changed the result canvas: %v", b)
	}
	if got := color.NRGBAModel.Convert(out.At(60, 60)).(color.NRGBA); got.R > 20 {
		t.Fatalf("watermark glyph was darkened/overwritten: %+v", got)
	}
	found := false
	for y := 0; y < 120 && !found; y++ {
		for x := 0; x < 120; x++ {
			// Nominal centred 10x10 glyph box is 55..65.
			if x >= 54 && x < 66 && y >= 54 && y < 66 {
				continue
			}
			if got := color.NRGBAModel.Convert(out.At(x, y)).(color.NRGBA); got.R < 245 {
				found = true
				break
			}
		}
	}
	if !found {
		t.Fatal("shadow produced no pixels outside the glyph box")
	}
}

func TestWatermarkTextRendersAndComposes(t *testing.T) {
	txt := RenderWatermarkText("IOimg")
	b := txt.Bounds()
	if b.Dx() < 2 || b.Dy() < 2 {
		t.Fatalf("text image is empty: %v", b)
	}
	ink := false
	for y := 0; y < b.Dy() && !ink; y++ {
		for x := 0; x < b.Dx(); x++ {
			if _, _, _, a := txt.At(x, y).RGBA(); a > 0x4000 {
				ink = true
				break
			}
		}
	}
	if !ink {
		t.Fatal("rendered text has no ink")
	}
	if RenderWatermarkText("IOimg") != txt {
		t.Fatal("text watermark is not memoised")
	}

	base := solid(160, 80, color.NRGBA{R: 255, G: 255, B: 255, A: 255})
	countDark := func(img image.Image, x0, x1, y0, y1 int) int {
		n := 0
		for y := y0; y < y1; y++ {
			for x := x0; x < x1; x++ {
				if got := color.NRGBAModel.Convert(img.At(x, y)).(color.NRGBA); got.R < 100 {
					n++
				}
			}
		}
		return n
	}
	nw := applyWatermark(base, txt, ops.Watermark{Opacity: 1, Position: ops.WatermarkNorthWest})
	se := applyWatermark(base, txt, ops.Watermark{Opacity: 1, Position: ops.WatermarkSouthEast})
	if b := nw.Bounds(); b.Dx() != 160 || b.Dy() != 80 {
		t.Fatalf("text watermark changed the canvas: %v", b)
	}
	if countDark(nw, 0, 160, 0, 80/2) == 0 {
		t.Fatal("north-west text watermark not drawn")
	}
	if countDark(se, 0, 160, 80/2, 80) == 0 {
		t.Fatal("south-east text watermark not drawn")
	}
	if countDark(nw, 0, 160, 80/2, 80) != 0 {
		t.Fatal("north-west text watermark leaked to the bottom half")
	}
}
