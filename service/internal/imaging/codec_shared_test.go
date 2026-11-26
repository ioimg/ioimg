package imaging

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"image"
	"image/color"
	"image/png"
	"testing"

	"github.com/ioimg/ioimg/service/pkg/ops"
)

// deterministicSource is a small, fully reproducible image used by the codec
// regression and backend-parity tests. It deliberately contains both a smooth
// ramp and hard colour changes so quantisation and chroma subsampling change
// the encoded bytes.
func deterministicSource(t *testing.T) []byte {
	t.Helper()
	img := image.NewNRGBA(image.Rect(0, 0, 96, 64))
	for y := 0; y < 64; y++ {
		for x := 0; x < 96; x++ {
			img.SetNRGBA(x, y, color.NRGBA{
				R: uint8((x*7 + y*3) % 256),
				G: uint8((x*5 + y*11 + 37) % 256),
				B: uint8((x*13 + y*17 + 91) % 256),
				A: uint8(128 + (x+y)%128),
			})
		}
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func testSHA(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

// TestCodecOptionsAccepted pins that every codec option URL parses on both
// backends. Backend-specific handling is asserted in the *_vips tests.
func TestCodecOptionsAccepted(t *testing.T) {
	urls := []string{
		"pngo_1:1:64",
		"pngo_0:1:8",
		"webpo_0:1:3",
		"avifo_0",
		"avifo_1",
		"avifo_2",
	}
	for _, raw := range urls {
		if _, err := ops.Parse("w_64,h_48,f_png,"+raw, nil); err != nil {
			t.Errorf("%s: %v", raw, err)
		}
	}
}

// TestPlainRequestBytesRegression pins output for a request with no codec
// options. The opt-in codec work must not change these bytes on any backend.
func TestPlainRequestBytesRegression(t *testing.T) {
	src := deterministicSource(t)

	t.Run("png", func(t *testing.T) {
		o, err := ops.Parse("w_64,h_48,c_fill,f_png", nil)
		if err != nil {
			t.Fatal(err)
		}
		res, err := DefaultProcessor().Process(src, o, Options{OutFormat: ops.FormatPNG, Quality: 80})
		if err != nil {
			t.Fatal(err)
		}
		want := map[string]string{
			"go":   "03e67f19a384fd454eba8ef02487559d4670c10bd3845231be471ac1a49a5f1a",
			"vips": "8690b13b4baddba75481c23119527e6fdde83c283f4ddb30cb569f276f2c9e68",
		}[Backend()]
		if got := testSHA(res.Data); got != want {
			t.Fatalf("plain PNG hash = %s, want %s", got, want)
		}
	})

	t.Run("avif-default", func(t *testing.T) {
		o, err := ops.Parse("w_64,h_48,c_fill,f_avif,q_60", nil)
		if err != nil {
			t.Fatal(err)
		}
		res, err := DefaultProcessor().Process(src, o, Options{OutFormat: ops.FormatAVIF, Quality: 60})
		if err != nil {
			t.Fatal(err)
		}
		want := "6386faa5b9ff88e31d684184f6eb1c7243530a9cbc9d355c27df3e87ea95a938" // pure Go
		if Backend() == "vips" {
			if libavifEncoder {
				want = "78d816c1f84c557a1aa82a2b8de2abc2f0c47ede7ff5e4dd4d8a07cad8d41e4d"
			} else {
				want = "e354f149158fd2b7feb73429d36f7ad1a3dd90cab2d5222a7e5cf2015245021a"
			}
		}
		if got := testSHA(res.Data); got != want {
			t.Fatalf("default AVIF hash = %s, want %s", got, want)
		}
	})
}

// TestPNGQuantizeURL runs the same pngo URL through whichever backend is
// linked and checks the semantic contract both must satisfy: an indexed PNG
// with at most the requested number of colours.
func TestPNGQuantizeURL(t *testing.T) {
	src := deterministicSource(t)
	o, err := ops.Parse("w_64,h_48,c_fill,f_png,pngo_1:1:64", nil)
	if err != nil {
		t.Fatal(err)
	}
	res, err := DefaultProcessor().Process(src, o, Options{OutFormat: ops.FormatPNG, Quality: 80})
	if err != nil {
		t.Fatal(err)
	}
	img, err := png.Decode(bytes.NewReader(res.Data))
	if err != nil {
		t.Fatal(err)
	}
	pal, ok := img.(*image.Paletted)
	if !ok {
		t.Fatalf("expected paletted output, got %T", img)
	}
	if len(pal.Palette) == 0 || len(pal.Palette) > 64 {
		t.Fatalf("palette has %d entries, want 1..64", len(pal.Palette))
	}
}
