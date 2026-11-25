//go:build vips

package imaging

import (
	"bytes"
	"image"
	"image/color"
	"os"
	"image/jpeg"
	_ "image/png"
	"math"
	"testing"

	"github.com/ioimg/ioimg/service/pkg/ops"
)

// paritySource builds a deterministic 960x640 photo-ish source: a gradient with
// a high-frequency overlay, so resampling differences are visible.
func paritySource(t *testing.T) []byte {
	if t != nil {
		t.Helper()
	}
	img := image.NewRGBA(image.Rect(0, 0, 960, 640))
	for y := 0; y < 640; y++ {
		for x := 0; x < 960; x++ {
			r := uint8(x * 255 / 959)
			g := uint8(y * 255 / 639)
			b := uint8(0)
			if (x/8+y/8)%2 == 0 {
				b = 200
			}
			img.Set(x, y, color.RGBA{R: r, G: g, B: b, A: 255})
		}
	}
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, img, &jpeg.Options{Quality: 92}); err != nil {
		if t != nil {
			t.Fatal(err)
		}
		panic(err)
	}
	return buf.Bytes()
}

func withThumbnail(t *testing.T, enabled bool, fn func()) {
	t.Helper()
	v := "1"
	if !enabled {
		v = "0"
	}
	t.Setenv("IOIMG_VIPS_THUMBNAIL", v)
	fn()
}

// TestVipsThumbnailParity pins that the shrink-on-load path produces the same
// geometry and visually equivalent pixels as full decode + resize. Bytes are
// not compared: JPEG DCT scaling resamples a reduced image, so encoders differ
// slightly, which is the whole point of the fast path.
func TestVipsThumbnailParity(t *testing.T) {
	src := paritySource(t)
	p := DefaultProcessor()

	cases := []struct {
		raw string
		// fast is true when the request is a pure resize and must be served by
		// shrink-on-load.
		fast bool
	}{
		{"w_400,c_fit,q_80", true},
		{"w_400,h_300,c_fit,q_80", true},
		{"h_250,c_fit,q_75", true},
		{"w_800,c_scale,q_80", true},
		{"w_300,h_300,c_fill,g_center,q_80", true},
		{"w_300,h_200,c_fill,g_north,q_80", true},
		{"w_250,h_150,c_force,q_80", true},
		{"w_120,c_fit,f_webp,q_70", true},
		// Fallback cases: everything else keeps the full-decode path.
		{"w_200,c_fit,q_80,blur_5", false},
		{"w_200,c_fit,q_80,rot_90", false},
		{"w_200,h_200,c_pad,q_80", false},
	}
	for _, c := range cases {
		t.Run(c.raw, func(t *testing.T) {
			raw := c.raw
			o, err := ops.Parse(raw, nil)
			if err != nil {
				t.Fatalf("parse %q: %v", raw, err)
			}
			opts := Options{OutFormat: o.Format, Quality: o.Quality}

			var fast, slow Result
			withThumbnail(t, true, func() {
				fast, err = p.Process(src, o, opts)
			})
			if err != nil {
				t.Fatalf("fast path: %v", err)
			}
			withThumbnail(t, false, func() {
				slow, err = p.Process(src, o, opts)
			})
			if err != nil {
				t.Fatalf("slow path: %v", err)
			}

			fa, _, err := image.Decode(bytes.NewReader(fast.Data))
			if err != nil {
				t.Fatalf("decode fast: %v", err)
			}
			sa, _, err := image.Decode(bytes.NewReader(slow.Data))
			if err != nil {
				t.Fatalf("decode slow: %v", err)
			}
			if fa.Bounds().Dx() != sa.Bounds().Dx() || fa.Bounds().Dy() != sa.Bounds().Dy() {
				t.Fatalf("geometry differs: fast %v slow %v", fa.Bounds(), sa.Bounds())
			}
			// The fast path has no separate decode stage, so its timings prove the
			// path actually taken; this keeps the test from silently covering a
			// fallback while claiming to test shrink-on-load.
			_, fastDecoded := fast.Timings["decode"]
			if c.fast && fastDecoded {
				t.Fatalf("expected shrink-on-load, got full decode: %v", fast.Timings)
			}
			if !c.fast && !fastDecoded {
				t.Fatalf("expected fallback to full decode, got: %v", fast.Timings)
			}

			diff := meanAbsDiff(fa, sa)
			if diff > 5.0 {
				t.Fatalf("pixels differ too much: mean abs diff %.2f/255", diff)
			}
			t.Logf("%dx%d mean abs diff %.2f/255, %d vs %d bytes",
				fa.Bounds().Dx(), fa.Bounds().Dy(), diff, len(fast.Data), len(slow.Data))
		})
	}
}

func meanAbsDiff(a, b image.Image) float64 {
	ba, bb := a.Bounds(), b.Bounds()
	var sum float64
	var n int
	for y := ba.Min.Y; y < ba.Max.Y; y += 2 {
		for x := ba.Min.X; x < ba.Max.X; x += 2 {
			r1, g1, b1, _ := a.At(x, y).RGBA()
			r2, g2, b2, _ := b.At(bb.Min.X+(x-ba.Min.X), bb.Min.Y+(y-ba.Min.Y)).RGBA()
			sum += math.Abs(float64(r1>>8)-float64(r2>>8)) +
				math.Abs(float64(g1>>8)-float64(g2>>8)) +
				math.Abs(float64(b1>>8)-float64(b2>>8))
			n += 3
		}
	}
	if n == 0 {
		return 0
	}
	return sum / float64(n)
}

// BenchmarkVipsResize compares the shrink-on-load path with full decode +
// resize on a 2400x1600 JPEG (the shape of a real source photo).
func BenchmarkVipsResize(b *testing.B) {
	src := paritySource(nil)
	path := os.Getenv("IOIMG_BENCH_SRC")
	if path == "" {
		path = realPhotoPath
	}
	if raw, err := osReadFile(path); err == nil {
		src = raw
		b.Logf("source: %s (%d bytes)", path, len(raw))
	}
	p := DefaultProcessor()
	cases := []struct{ name, raw string }{
		{"fit_w400", "w_400,c_fit,q_80"},
		{"fill_300x300", "w_300,h_300,c_fill,g_center,q_80"},
	}
	for _, c := range cases {
		o, err := ops.Parse(c.raw, nil)
		if err != nil {
			b.Fatal(err)
		}
		opts := Options{OutFormat: o.Format, Quality: o.Quality}
		for _, mode := range []struct {
			name string
			fast bool
		}{{"slow", false}, {"thumb", true}} {
			b.Run(c.name+"/"+mode.name, func(b *testing.B) {
				v := "0"
				if mode.fast {
					v = "1"
				}
				b.Setenv("IOIMG_VIPS_THUMBNAIL", v)
				b.ResetTimer()
				for i := 0; i < b.N; i++ {
					if _, err := p.Process(src, o, opts); err != nil {
						b.Fatal(err)
					}
				}
			})
		}
	}
}

// realPhotoPath is the benchmark corpus used by the repo's other benches.
const realPhotoPath = "../../../../poc/data/src/photo00.jpg"

func osReadFile(path string) ([]byte, error) { return os.ReadFile(path) }

// BenchmarkVipsParallel measures aggregate throughput with many concurrent
// transforms, which is how the HTTP server drives libvips. Run it twice with
// IOIMG_VIPS_CONCURRENCY=1 and =16 to pick the default:
//
//	IOIMG_VIPS_CONCURRENCY=1 go test -tags vips ./internal/imaging/ -bench Parallel
//	IOIMG_VIPS_CONCURRENCY=16 go test -tags vips ./internal/imaging/ -bench Parallel
func BenchmarkVipsParallel(b *testing.B) {
	src := paritySource(nil)
	path := os.Getenv("IOIMG_BENCH_SRC")
	if path == "" {
		path = realPhotoPath
	}
	if raw, err := osReadFile(path); err == nil {
		src = raw
	}
	o, err := ops.Parse("w_400,c_fit,q_80", nil)
	if err != nil {
		b.Fatal(err)
	}
	opts := Options{OutFormat: o.Format, Quality: o.Quality}
	p := DefaultProcessor()
	b.Setenv("IOIMG_VIPS_THUMBNAIL", "1")
	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			if _, err := p.Process(src, o, opts); err != nil {
				b.Fatal(err)
			}
		}
	})
	b.ReportMetric(float64(vipsConcurrency()), "vips-concurrency")
}
