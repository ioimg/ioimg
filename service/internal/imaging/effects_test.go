package imaging

import (
	"image"
	"image/color"
	"testing"

	"github.com/ioimg/ioimg/service/pkg/ops"
)

func solidNRGBA(r, g, b, a uint8, w, h int) *image.NRGBA {
	img := image.NewNRGBA(image.Rect(0, 0, w, h))
	for y := range h {
		for x := range w {
			img.Set(x, y, color.NRGBA{R: r, G: g, B: b, A: a})
		}
	}
	return img
}

func pixel(t *testing.T, img image.Image, x, y int) color.NRGBA {
	t.Helper()
	r, g, b, a := img.At(x, y).RGBA()
	return color.NRGBA{R: uint8(r >> 8), G: uint8(g >> 8), B: uint8(b >> 8), A: uint8(a >> 8)}
}

func TestDuotoneMapsLuminance(t *testing.T) {
	src := solidNRGBA(255, 255, 255, 255, 2, 2)
	o, err := ops.Parse("w_2,dt_1:000000:ff0000", nil)
	if err != nil {
		t.Fatal(err)
	}
	// White is the light end of the palette: color2.
	if got := pixel(t, applyColorEffects(src, o), 0, 0); got.R != 255 || got.G != 0 || got.B != 0 {
		t.Fatalf("white duotone = %+v, want red", got)
	}

	dark := solidNRGBA(0, 0, 0, 255, 2, 2)
	// Black is the dark end: color1.
	if got := pixel(t, applyColorEffects(dark, o), 0, 0); got.R != 0 || got.G != 0 || got.B != 0 {
		t.Fatalf("black duotone = %+v, want black", got)
	}
}

func TestDuotoneIntensityBlends(t *testing.T) {
	src := solidNRGBA(255, 255, 255, 255, 1, 1)
	o, _ := ops.Parse("w_1,dt_0.5:000000:000000", nil)
	// Half-way between white and the black palette entry.
	if got := pixel(t, applyColorEffects(src, o), 0, 0); got.R < 126 || got.R > 129 {
		t.Fatalf("half intensity = %d, want ~128", got.R)
	}
}

func TestColorizeAndKeepAlpha(t *testing.T) {
	src := solidNRGBA(10, 20, 30, 0x80, 1, 1)

	full, _ := ops.Parse("w_1,col_1:ff0000:0", nil)
	got := pixel(t, applyColorEffects(src, full), 0, 0)
	if got.R != 255 || got.G != 0 || got.B != 0 || got.A != 255 {
		t.Fatalf("col_1 without keep_alpha = %+v, want opaque red", got)
	}

	keep, _ := ops.Parse("w_1,col_1:ff0000:1", nil)
	got = pixel(t, applyColorEffects(src, keep), 0, 0)
	if got.A != 0x80 {
		t.Fatalf("keep_alpha = %d, want 128", got.A)
	}

	half, _ := ops.Parse("w_1,col_0.5:000000:0", nil)
	got = pixel(t, applyColorEffects(src, half), 0, 0)
	if got.R < 4 || got.R > 6 || got.G < 9 || got.G > 11 {
		t.Fatalf("col_0.5 = %+v, want ~5,10,15", got)
	}
}

func TestColorEffectsNoop(t *testing.T) {
	src := solidNRGBA(1, 2, 3, 255, 1, 1)
	o, _ := ops.Parse("w_1,dt_0:000000:ffffff,col_0:ff0000:0", nil)
	if got := applyColorEffects(src, o); got != src {
		t.Fatal("expected the original image to be returned untouched")
	}
}
