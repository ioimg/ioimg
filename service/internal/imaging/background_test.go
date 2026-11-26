//go:build !vips

package imaging

import (
	"bytes"
	"image"
	"image/color"
	"testing"

	"github.com/ioimg/ioimg/service/pkg/ops"
)

func TestExtendPadsToExactRequestedSize(t *testing.T) {
	src, _, err := Decode(testJPEG(t))
	if err != nil {
		t.Fatal(err)
	}

	for _, tc := range []struct {
		raw string
		bg  color.NRGBA
	}{
		{"w_160,h_160,c_fit,ex_1,bg_ff0000,f_png", color.NRGBA{R: 255, A: 255}},
		{"w_160,h_160,c_fit,ex_1:ce,bg_ff0000,bga_0.5,f_png", color.NRGBA{R: 255, A: 128}},
	} {
		o, err := ops.Parse(tc.raw, nil)
		if err != nil {
			t.Fatalf("%s: %v", tc.raw, err)
		}
		out, _, err := Transform(src, o, Options{OutFormat: ops.FormatPNG})
		if err != nil {
			t.Fatalf("%s: %v", tc.raw, err)
		}
		cfg, _, err := image.DecodeConfig(bytes.NewReader(out))
		if err != nil {
			t.Fatalf("%s: decode config: %v", tc.raw, err)
		}
		if cfg.Width != 160 || cfg.Height != 160 {
			t.Fatalf("%s: want 160x160, got %dx%d", tc.raw, cfg.Width, cfg.Height)
		}
		assertPixel(t, out, 0, 0, tc.bg, 2)
	}
}

func TestFlattenCompositesSemiTransparentBackground(t *testing.T) {
	srcBytes := semitransparentPNG(t, 4, 4, color.NRGBA{R: 255, A: 128})
	src, _, err := Decode(srcBytes)
	if err != nil {
		t.Fatal(err)
	}

	// Opaque background: 50% red over blue.
	o, err := ops.Parse("w_4,h_4,c_force,fl_1,bg_0000ff,f_png", nil)
	if err != nil {
		t.Fatal(err)
	}
	out, _, err := Transform(src, o, Options{OutFormat: ops.FormatPNG})
	if err != nil {
		t.Fatal(err)
	}
	assertPixel(t, out, 2, 2, color.NRGBA{R: 128, B: 128, A: 255}, 3)

	// Semi-transparent background: alpha 128 red over alpha 128 blue.
	o, err = ops.Parse("w_4,h_4,c_force,fl_1,bg_0000ff80,f_png", nil)
	if err != nil {
		t.Fatal(err)
	}
	out, _, err = Transform(src, o, Options{OutFormat: ops.FormatPNG})
	if err != nil {
		t.Fatal(err)
	}
	assertPixel(t, out, 2, 2, color.NRGBA{R: 170, B: 85, A: 191}, 4)
}

func TestFlattenDefaultsToWhite(t *testing.T) {
	srcBytes := semitransparentPNG(t, 2, 2, color.NRGBA{R: 255, A: 128})
	src, _, err := Decode(srcBytes)
	if err != nil {
		t.Fatal(err)
	}
	o, err := ops.Parse("w_2,h_2,c_force,fl_1,f_png", nil)
	if err != nil {
		t.Fatal(err)
	}
	out, _, err := Transform(src, o, Options{OutFormat: ops.FormatPNG})
	if err != nil {
		t.Fatal(err)
	}
	assertPixel(t, out, 0, 0, color.NRGBA{R: 255, G: 128, B: 128, A: 255}, 3)
}
