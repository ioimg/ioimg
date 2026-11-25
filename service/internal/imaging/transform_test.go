//go:build !vips

package imaging

import (
	"bytes"
	"image"
	"image/color"
	"image/jpeg"
	"testing"

	"ioimg/internal/ops"
)

func testJPEG(t *testing.T) []byte {
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

func TestDecodeAndModes(t *testing.T) {
	data := testJPEG(t)
	img, info, err := Decode(data)
	if err != nil {
		t.Fatal(err)
	}
	if info.Width != 120 || info.Height != 80 || info.Format != "jpeg" {
		t.Fatalf("unexpected info: %+v", info)
	}
	cases := []struct {
		raw  string
		w, h int
	}{
		{"w_60,f_png", 60, 40},
		{"h_40,f_png", 60, 40},
		{"w_60,h_60,c_fit,f_png", 60, 40},
		{"w_60,h_60,c_fill,f_png", 60, 60},
		{"w_60,h_60,c_force,f_png", 60, 60},
		{"w_60,h_60,c_pad,f_png", 60, 60},
		{"w_40,h_40,c_crop,f_png", 40, 40},
		{"w_200,f_png", 120, 80}, // no enlarge by default
		{"w_200,enlarge_1,f_png", 200, 133},
		{"w_60,f_png,rot_90", 60, 90},
		{"w_60,f_png,flip_h", 60, 40},
	}
	for _, tc := range cases {
		o, err := ops.Parse(tc.raw, nil)
		if err != nil {
			t.Fatalf("%s: %v", tc.raw, err)
		}
		out, _, err := Transform(img, o, Options{OutFormat: ops.FormatPNG})
		if err != nil {
			t.Fatalf("%s: %v", tc.raw, err)
		}
		cfg, format, err := image.DecodeConfig(bytes.NewReader(out))
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

func TestEncodeFormats(t *testing.T) {
	img, _, err := Decode(testJPEG(t))
	if err != nil {
		t.Fatal(err)
	}
	o, _ := ops.Parse("w_40,f_jpeg", nil)
	for _, f := range []ops.Format{ops.FormatJPEG, ops.FormatPNG, ops.FormatWebP, ops.FormatGIF} {
		out, _, err := Transform(img, o, Options{OutFormat: f})
		if err != nil {
			t.Fatalf("%s: %v", f, err)
		}
		if len(out) == 0 {
			t.Fatalf("%s: empty output", f)
		}
		if _, _, err := image.Decode(bytes.NewReader(out)); err != nil {
			t.Fatalf("%s: output is not decodable: %v", f, err)
		}
	}
}

func TestBlurSharpenTrim(t *testing.T) {
	img, _, _ := Decode(testJPEG(t))
	for _, raw := range []string{"w_40,blur_5", "w_40,sharp_3", "w_40,trim_5"} {
		o, err := ops.Parse(raw, nil)
		if err != nil {
			t.Fatal(err)
		}
		if _, _, err := Transform(img, o, Options{OutFormat: ops.FormatJPEG}); err != nil {
			t.Fatalf("%s: %v", raw, err)
		}
	}
}
