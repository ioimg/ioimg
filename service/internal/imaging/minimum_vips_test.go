//go:build vips

package imaging

import (
	"bytes"
	"image"
	"image/color"
	"image/jpeg"
	"testing"

	"github.com/davidbyttow/govips/v2/vips"

	"github.com/ioimg/ioimg/service/pkg/ops"
)

// vipsSizedJPEG builds a solid JPEG of the requested size without relying on
// the pure-Go pipeline (which is excluded under the vips build tag).
func vipsSizedJPEG(t *testing.T, w, h int) []byte {
	t.Helper()
	img := image.NewNRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			img.Set(x, y, color.NRGBA{R: 200, G: 40, B: 90, A: 255})
		}
	}
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, img, &jpeg.Options{Quality: 90}); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func TestVipsMinimumDimensions(t *testing.T) {
	src := vipsSizedJPEG(t, 100, 50)
	for _, tc := range []struct {
		raw  string
		w, h int
	}{
		{"mw_200", 200, 100},
		{"w_100,mw_200", 200, 100},
		{"mw_150,mh_120", 240, 120},
	} {
		o, err := ops.Parse(tc.raw, nil)
		if err != nil {
			t.Fatalf("%s: parse: %v", tc.raw, err)
		}
		res, err := DefaultProcessor().Process(src, o, Options{OutFormat: ops.FormatPNG})
		if err != nil {
			t.Fatalf("%s: process: %v", tc.raw, err)
		}
		img, err := vips.NewImageFromBuffer(res.Data)
		if err != nil {
			t.Fatalf("%s: reopen: %v", tc.raw, err)
		}
		defer img.Close()
		if got := img.Width(); got != tc.w {
			t.Fatalf("%s: width = %d, want %d", tc.raw, got, tc.w)
		}
		if got := img.Height(); got != tc.h {
			t.Fatalf("%s: height = %d, want %d", tc.raw, got, tc.h)
		}
	}
}

// ar_0 must not silently keep vips_thumbnail's autorotate.
func TestVipsAutoRotateOffSkipsThumbnailPath(t *testing.T) {
	o, err := ops.Parse("w_100,ar_0", nil)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if _, ok := thumbnailPlan(vipsSizedJPEG(t, 100, 50), o, Options{OutFormat: ops.FormatJPEG}); ok {
		t.Fatal("ar_0 must take the explicit path, not shrink-on-load")
	}
	on, err := ops.Parse("w_100", nil)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if _, ok := thumbnailPlan(vipsSizedJPEG(t, 100, 50), on, Options{OutFormat: ops.FormatJPEG}); !ok {
		t.Fatal("default auto-rotate should still use shrink-on-load")
	}
}
