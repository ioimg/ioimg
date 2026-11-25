//go:build vips

package imaging

import (
	"bytes"
	"image"
	"image/color"
	"image/jpeg"
	"testing"

	"github.com/ioimg/ioimg/service/pkg/ops"
)

func vipsTestJPEG(t *testing.T) []byte {
	t.Helper()
	img := image.NewNRGBA(image.Rect(0, 0, 120, 80))
	for y := 0; y < 80; y++ {
		for x := 0; x < 120; x++ {
			img.Set(x, y, color.NRGBA{R: uint8(x), G: uint8(y), B: 120, A: 255})
		}
	}
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, img, &jpeg.Options{Quality: 90}); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func TestVipsModes(t *testing.T) {
	data := vipsTestJPEG(t)
	p := DefaultProcessor()
	cases := []struct {
		raw  string
		w, h int
	}{
		{"w_60,f_png", 60, 40},
		{"w_60,h_60,c_fit,f_png", 60, 40},
		{"w_60,h_60,c_fill,f_png", 60, 60},
		{"w_60,h_60,c_force,f_png", 60, 60},
		{"w_60,h_60,c_pad,f_png", 60, 60},
		{"w_40,h_40,c_crop,f_png", 40, 40},
		{"w_200,f_png", 120, 80},
		{"w_60,f_png,rot_90", 60, 90},
		{"w_60,f_png,flip_h", 60, 40},
	}
	for _, tc := range cases {
		o, err := ops.Parse(tc.raw, nil)
		if err != nil {
			t.Fatalf("%s: %v", tc.raw, err)
		}
		res, err := p.Process(data, o, Options{OutFormat: ops.FormatPNG})
		if err != nil {
			t.Fatalf("%s: %v", tc.raw, err)
		}
		cfg, format, err := image.DecodeConfig(bytes.NewReader(res.Data))
		if err != nil {
			t.Fatalf("%s: decode output: %v", tc.raw, err)
		}
		if format != "png" {
			t.Fatalf("%s: want png, got %s", tc.raw, format)
		}
		if cfg.Width != tc.w || cfg.Height != tc.h {
			t.Fatalf("%s: want %dx%d, got %dx%d", tc.raw, tc.w, tc.h, cfg.Width, cfg.Height)
		}
	}
}

func TestVipsFormatsAndJSON(t *testing.T) {
	data := vipsTestJPEG(t)
	p := DefaultProcessor()
	o, _ := ops.Parse("w_40,f_jpeg", nil)
	for _, f := range []ops.Format{ops.FormatJPEG, ops.FormatPNG, ops.FormatWebP, ops.FormatGIF} {
		if !AvailableFormats()[f] {
			t.Logf("format %s not supported by linked libvips; skipping", f)
			continue
		}
		res, err := p.Process(data, o, Options{OutFormat: f})
		if err != nil {
			t.Fatalf("%s: %v", f, err)
		}
		cfg, _, err := image.DecodeConfig(bytes.NewReader(res.Data))
		if err != nil {
			t.Fatalf("%s: output not decodable: %v", f, err)
		}
		if cfg.Width != 40 {
			t.Fatalf("%s: want width 40, got %d", f, cfg.Width)
		}
	}
	jo, _ := ops.Parse("f_json", nil)
	res, err := p.Process(data, jo, Options{OutFormat: ops.FormatJSON})
	if err != nil {
		t.Fatal(err)
	}
	if res.Info.Width != 120 || res.Info.Height != 80 {
		t.Fatalf("unexpected info: %+v", res.Info)
	}
	if res.DominantColor == "" {
		t.Fatal("missing dominant color")
	}
}
