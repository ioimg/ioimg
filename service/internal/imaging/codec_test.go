//go:build !vips

package imaging

import (
	"bytes"
	"image"
	"image/color"
	"image/png"
	"testing"

	"github.com/ioimg/ioimg/service/pkg/ops"
)

func gradientSource() image.Image {
	img := image.NewNRGBA(image.Rect(0, 0, 8, 4))
	for y := 0; y < 4; y++ {
		for x := 0; x < 8; x++ {
			img.SetNRGBA(x, y, color.NRGBA{R: 255, G: 255, B: 255, A: 255})
		}
	}
	return img
}

func TestGradientDown(t *testing.T) {
	o, err := ops.Parse("w_100,gr_1:000000:down", nil)
	if err != nil {
		t.Fatal(err)
	}
	out := nrgbaOf(applyColorEffects(gradientSource(), o))

	// Top row is transparent overlay (still white), bottom row is opaque black.
	if c := out.NRGBAAt(0, 0); c.R < 250 {
		t.Errorf("top row should stay white, got %v", c)
	}
	if c := out.NRGBAAt(0, 3); c.R > 5 {
		t.Errorf("bottom row should be black, got %v", c)
	}
	// Monotonic ramp down the column.
	prev := 255
	for y := 0; y < 4; y++ {
		v := int(out.NRGBAAt(4, y).R)
		if v > prev {
			t.Fatalf("ramp not monotonic at y=%d: %d > %d", y, v, prev)
		}
		prev = v
	}
}

func TestGradientDirections(t *testing.T) {
	cases := map[string][2][2]int{
		// direction: {{corner that stays white}, {corner that goes black}}
		"down":  {{0, 0}, {0, 3}},
		"up":    {{0, 3}, {0, 0}},
		"right": {{0, 0}, {7, 0}},
		"left":  {{7, 0}, {0, 0}},
		"90":    {{7, 0}, {0, 0}}, // 90° clockwise == right→left
	}
	for dir, c := range cases {
		o, err := ops.Parse("w_100,gr_1:000000:"+dir, nil)
		if err != nil {
			t.Fatalf("%s: %v", dir, err)
		}
		out := nrgbaOf(applyColorEffects(gradientSource(), o))
		if v := out.NRGBAAt(c[0][0], c[0][1]).R; v < 250 {
			t.Errorf("%s: transparent corner should stay white, got %d", dir, v)
		}
		if v := out.NRGBAAt(c[1][0], c[1][1]).R; v > 5 {
			t.Errorf("%s: opaque corner should be black, got %d", dir, v)
		}
	}
}

func TestGradientStartStop(t *testing.T) {
	o, err := ops.Parse("w_100,gr_1:000000:down:0.5:1", nil)
	if err != nil {
		t.Fatal(err)
	}
	out := nrgbaOf(applyColorEffects(gradientSource(), o))
	// Above start the overlay is fully transparent.
	if v := out.NRGBAAt(0, 0).R; v != 255 {
		t.Errorf("before start should be untouched, got %d", v)
	}
}

func TestGradientOpacityAndAlpha(t *testing.T) {
	o, err := ops.Parse("w_100,gr_0.5:ff0000:down", nil)
	if err != nil {
		t.Fatal(err)
	}
	out := nrgbaOf(applyColorEffects(gradientSource(), o))
	bottom := out.NRGBAAt(0, 3)
	if bottom.R < 250 || bottom.G > 130 || bottom.B > 130 {
		t.Errorf("half-opacity red over white should be pink, got %v", bottom)
	}
	// The gradient never touches alpha: a transparent source stays transparent.
	src := image.NewNRGBA(image.Rect(0, 0, 2, 2)) // all zero, alpha 0
	o2, err := ops.Parse("w_100,gr_1:000000", nil)
	if err != nil {
		t.Fatal(err)
	}
	if a := nrgbaOf(applyColorEffects(src, o2)).NRGBAAt(0, 1).A; a != 0 {
		t.Errorf("gradient changed alpha: %d", a)
	}
}

func TestPNGEncoderQuantize(t *testing.T) {
	src := gradientSource()
	for _, n := range []int{8, 64, 256} {
		q := true
		o := ops.Default()
		o.Format = ops.FormatPNG
		o.PNGEncoder = &ops.PNGEncoder{Quantize: &q, Colors: n}
		var buf bytes.Buffer
		enc := png.Encoder{CompressionLevel: png.DefaultCompression}
		if err := enc.Encode(&buf, toPalettedN(src, pngColors(o.PNGEncoder.Colors))); err != nil {
			t.Fatalf("n=%d: %v", n, err)
		}
		img, err := png.Decode(bytes.NewReader(buf.Bytes()))
		if err != nil {
			t.Fatalf("n=%d: decode: %v", n, err)
		}
		pal, ok := img.(*image.Paletted)
		if !ok {
			t.Fatalf("n=%d: expected paletted output, got %T", n, img)
		}
		if got := len(pal.Palette); n < 256 && got > n {
			t.Errorf("n=%d: palette has %d entries", n, got)
		}
	}
}
