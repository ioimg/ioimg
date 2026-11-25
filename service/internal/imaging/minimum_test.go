//go:build !vips

package imaging

import (
	"bytes"
	"image"
	"image/color"
	"image/jpeg"
	"testing"

	"github.com/ioimg/ioimg/service/pkg/ops"
)

// mw_/mh_ must enlarge a too-small result, keeping the aspect ratio, and must
// apply even when no width/height was requested.
func TestMinimumDimensionsEnlarge(t *testing.T) {
	src := jpegSized(t, 100, 50)

	cases := []struct {
		name string
		ops  string
		w, h int
	}{
		{"mw only, no resize", "mw_200", 200, 100},
		{"mh only, no resize", "mh_100", 200, 100},
		{"mw after fit", "w_100,mw_200", 200, 100},
		{"mw and mh keep ratio", "mw_150,mh_120", 240, 120},
		{"no minimum", "w_100", 100, 50},
	}
	for _, tc := range cases {
		o, err := ops.Parse(tc.ops, nil)
		if err != nil {
			t.Fatalf("%s: parse: %v", tc.name, err)
		}
		decoded, _, err := Decode(src)
		if err != nil {
			t.Fatalf("%s: decode src: %v", tc.name, err)
		}
		out, _, err := Transform(decoded, o, Options{OutFormat: ops.FormatPNG})
		if err != nil {
			t.Fatalf("%s: transform: %v", tc.name, err)
		}
		img, _, err := Decode(out)
		if err != nil {
			t.Fatalf("%s: decode: %v", tc.name, err)
		}
		if got := img.Bounds().Dx(); got != tc.w {
			t.Fatalf("%s: width = %d, want %d", tc.name, got, tc.w)
		}
		if got := img.Bounds().Dy(); got != tc.h {
			t.Fatalf("%s: height = %d, want %d", tc.name, got, tc.h)
		}
	}
}

// A minimum above the hard limit must clamp instead of allocating past it.
// jpegSized builds a solid JPEG of the requested size.
func jpegSized(t *testing.T, w, h int) []byte {
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

func TestMinimumDimensionsClamp(t *testing.T) {
	src := jpegSized(t, 100, 50)
	o, err := ops.Parse("mw_8000,mh_8000", nil)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	decoded, _, err := Decode(src)
	if err != nil {
		t.Fatalf("decode src: %v", err)
	}
	out, _, err := Transform(decoded, o, Options{OutFormat: ops.FormatPNG})
	if err != nil {
		t.Fatalf("transform: %v", err)
	}
	img, _, err := Decode(out)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	b := img.Bounds()
	if b.Dx() > ops.MaxDimension || b.Dy() > ops.MaxDimension {
		t.Fatalf("result %dx%d exceeds MaxDimension", b.Dx(), b.Dy())
	}
	if b.Dx() != ops.MaxDimension || b.Dy() != ops.MaxDimension/2 {
		t.Fatalf("result = %dx%d, want %dx%d", b.Dx(), b.Dy(), ops.MaxDimension, ops.MaxDimension/2)
	}
}
