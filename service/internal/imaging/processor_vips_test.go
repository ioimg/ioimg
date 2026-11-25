//go:build vips

package imaging

import (
	"bytes"
	"errors"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
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

// TestVipsMaxBytes covers the mb_ search on the vips export path. The budget is
// derived from the encoder itself (between q=20 and q=92) so the test checks the
// search, not the compressibility of the sample.
func TestVipsMaxBytes(t *testing.T) {
	data := vipsNoisyJPEG(t, 400, 300)
	p := DefaultProcessor()

	size := func(raw string) int {
		res, err := p.Process(data, mustOps(t, raw), Options{OutFormat: ops.FormatJPEG})
		if err != nil {
			t.Fatal(err)
		}
		return len(res.Data)
	}
	floor := size("w_240,f_jpeg,q_20")
	plain := size("w_240,f_jpeg,q_92")
	if plain <= floor {
		t.Skipf("sample does not compress monotonically (q20=%d q92=%d)", floor, plain)
	}
	budget := (floor + plain) / 2
	if budget < 1024 {
		budget = 1024
	}

	res, err := p.Process(data, mustOps(t, "w_240,f_jpeg,q_92,mb_"+itoa(budget)), Options{OutFormat: ops.FormatJPEG})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Data) > budget {
		t.Fatalf("mb budget missed: %d > %d (q20=%d q92=%d)", len(res.Data), budget, floor, plain)
	}
	if len(res.Data) < floor {
		t.Fatalf("output smaller than the quality floor: %d < %d", len(res.Data), floor)
	}
}

// TestVipsKernelIsHonoured pins that ra_* is not silently ignored: the kernel
// changes the bytes and forces the explicit-resize path, because vips_thumbnail
// cannot accept a kernel.
func TestVipsKernelIsHonoured(t *testing.T) {
	data := vipsTestJPEG(t)
	p := DefaultProcessor()
	render := func(raw string) []byte {
		res, err := p.Process(data, mustOps(t, raw), Options{OutFormat: ops.FormatPNG})
		if err != nil {
			t.Fatal(err)
		}
		return res.Data
	}
	nearest := render("w_61,f_png,ra_nearest")
	lanczos := render("w_61,f_png")
	if bytes.Equal(nearest, lanczos) {
		t.Fatal("ra_nearest produced the lanczos3 output; kernel ignored")
	}

	// The default stays on shrink-on-load (no "decode" timing), while a custom
	// kernel reports a decode phase. That is the documented cost of ra_*.
	defaultRes, err := p.Process(data, mustOps(t, "w_61,f_png"), Options{OutFormat: ops.FormatPNG})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := defaultRes.Timings["decode"]; ok {
		t.Fatal("default resize left the shrink-on-load path")
	}
	customRes, err := p.Process(data, mustOps(t, "w_61,f_png,ra_nearest"), Options{OutFormat: ops.FormatPNG})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := customRes.Timings["decode"]; !ok {
		t.Fatal("ra_nearest skipped the explicit path; the kernel would be ignored")
	}
}

// TestVipsRejectsOversizedSide covers the vips-side guard: it reads dimensions
// from the lazy header, so a side beyond MaxSide is refused before the pixel
// data is materialised.
func TestVipsRejectsOversizedSide(t *testing.T) {
	img := image.NewNRGBA(image.Rect(0, 0, 9000, 4))
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatal(err)
	}
	_, err := DefaultProcessor().Process(buf.Bytes(), mustOps(t, "w_100,f_png"), Options{OutFormat: ops.FormatPNG})
	if !errors.Is(err, ErrDimension) {
		t.Fatalf("want ErrDimension for a 9000px side, got %v", err)
	}
}

// vipsNoisyJPEG produces poorly compressible data so a quality search has to
// move instead of returning the first encode.
func vipsNoisyJPEG(t *testing.T, w, h int) []byte {
	t.Helper()
	img := image.NewNRGBA(image.Rect(0, 0, w, h))
	seed := uint32(7)
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			seed = seed*1664525 + 1013904223
			img.Set(x, y, color.NRGBA{R: uint8(seed >> 24), G: uint8(seed >> 16), B: uint8(seed >> 8), A: 255})
		}
	}
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, img, &jpeg.Options{Quality: 92}); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func mustOps(t *testing.T, raw string) ops.Ops {
	t.Helper()
	o, err := ops.Parse(raw, nil)
	if err != nil {
		t.Fatalf("%s: %v", raw, err)
	}
	return o
}

func itoa(v int) string {
	if v == 0 {
		return "0"
	}
	var b [20]byte
	i := len(b)
	for v > 0 {
		i--
		b[i] = byte('0' + v%10)
		v /= 10
	}
	return string(b[i:])
}
