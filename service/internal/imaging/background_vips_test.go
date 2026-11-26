//go:build vips

package imaging

import (
	"bytes"
	"image"
	"image/color"
	"testing"

	"github.com/ioimg/ioimg/service/pkg/ops"
)

func TestVipsExtendPadsToExactRequestedSize(t *testing.T) {
	src := vipsTestJPEG(t)
	p := DefaultProcessor()

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
		res, err := p.Process(src, o, Options{OutFormat: ops.FormatPNG})
		if err != nil {
			t.Fatalf("%s: %v", tc.raw, err)
		}
		cfg, _, err := image.DecodeConfig(bytes.NewReader(res.Data))
		if err != nil {
			t.Fatalf("%s: decode config: %v", tc.raw, err)
		}
		if cfg.Width != 160 || cfg.Height != 160 {
			t.Fatalf("%s: want 160x160, got %dx%d", tc.raw, cfg.Width, cfg.Height)
		}
		assertPixel(t, res.Data, 0, 0, tc.bg, 2)
	}
}

func TestVipsFlattenCompositesSemiTransparentBackground(t *testing.T) {
	src := semitransparentPNG(t, 4, 4, color.NRGBA{R: 255, A: 128})
	p := DefaultProcessor()

	// Opaque background: 50% red over blue.
	o, err := ops.Parse("w_4,h_4,c_force,fl_1,bg_0000ff,f_png", nil)
	if err != nil {
		t.Fatal(err)
	}
	res, err := p.Process(src, o, Options{OutFormat: ops.FormatPNG})
	if err != nil {
		t.Fatal(err)
	}
	assertPixel(t, res.Data, 2, 2, color.NRGBA{R: 128, B: 128, A: 255}, 3)

	// Semi-transparent background: alpha 128 red over alpha 128 blue.
	o, err = ops.Parse("w_4,h_4,c_force,fl_1,bg_0000ff80,f_png", nil)
	if err != nil {
		t.Fatal(err)
	}
	res, err = p.Process(src, o, Options{OutFormat: ops.FormatPNG})
	if err != nil {
		t.Fatal(err)
	}
	assertPixel(t, res.Data, 2, 2, color.NRGBA{R: 170, B: 85, A: 191}, 6)
}

func TestVipsFlattenOpaqueSource(t *testing.T) {
	src := vipsTestJPEG(t)
	p := DefaultProcessor()
	o, err := ops.Parse("w_20,h_20,c_force,fl_1,bg_0000ff,f_png", nil)
	if err != nil {
		t.Fatal(err)
	}
	res, err := p.Process(src, o, Options{OutFormat: ops.FormatPNG})
	if err != nil {
		t.Fatalf("flatten opaque source: %v", err)
	}
	// The opaque source covers the background; the request must simply succeed.
	cfg, _, err := image.DecodeConfig(bytes.NewReader(res.Data))
	if err != nil {
		t.Fatalf("decode config: %v", err)
	}
	if cfg.Width != 20 || cfg.Height != 20 {
		t.Fatalf("want 20x20, got %dx%d", cfg.Width, cfg.Height)
	}
}
