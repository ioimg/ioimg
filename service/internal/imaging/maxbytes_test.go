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

// noisyJPEG builds an image that does not compress well, so a quality search
// actually has to move.
func noisyJPEG(t *testing.T) []byte {
	t.Helper()
	const w, h = 640, 480
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	seed := uint32(12345)
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			seed = seed*1664525 + 1013904223
			img.SetRGBA(x, y, color.RGBA{uint8(seed >> 24), uint8(seed >> 16), uint8(seed >> 8), 255})
		}
	}
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, img, &jpeg.Options{Quality: 95}); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func TestMaxBytesShrinksEncoding(t *testing.T) {
	src := noisyJPEG(t)
	o, err := ops.Parse("w_320,f_jpeg,q_90", nil)
	if err != nil {
		t.Fatal(err)
	}
	img, _, err := Decode(src)
	if err != nil {
		t.Fatal(err)
	}
	plain, _, err := Transform(img, o, Options{OutFormat: ops.FormatJPEG, Quality: 90})
	if err != nil {
		t.Fatal(err)
	}

	budget := len(plain) / 2
	o.MaxBytes = budget
	out, _, err := Transform(img, o, Options{OutFormat: ops.FormatJPEG, Quality: 90})
	if err != nil {
		t.Fatal(err)
	}
	if len(out) > budget {
		t.Fatalf("mb budget missed: %d bytes > %d (unbudgeted %d)", len(out), budget, len(plain))
	}
	if len(out) == 0 {
		t.Fatal("empty output")
	}
	// Determinism matters: the search result is cached and must be reproducible.
	again, _, err := Transform(img, o, Options{OutFormat: ops.FormatJPEG, Quality: 90})
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(out, again) {
		t.Fatal("mb search is not deterministic")
	}
}

func TestMaxBytesNeverFails(t *testing.T) {
	src := noisyJPEG(t)
	o, _ := ops.Parse("w_320,f_jpeg", nil)
	img, _, err := Decode(src)
	if err != nil {
		t.Fatal(err)
	}
	o.MaxBytes = 1024 // impossible for a 320px noise image
	out, _, err := Transform(img, o, Options{OutFormat: ops.FormatJPEG, Quality: 82})
	if err != nil {
		t.Fatalf("impossible budget must still return an image: %v", err)
	}
	if len(out) == 0 {
		t.Fatal("empty output")
	}
}

func TestAlgorithmChangesPixels(t *testing.T) {
	src := noisyJPEG(t)
	img, _, err := Decode(src)
	if err != nil {
		t.Fatal(err)
	}
	render := func(ra string) []byte {
		o, err := ops.Parse("w_160,f_png,"+ra, nil)
		if err != nil {
			t.Fatal(err)
		}
		out, _, err := Transform(img, o, Options{OutFormat: ops.FormatPNG})
		if err != nil {
			t.Fatal(err)
		}
		return out
	}
	nearest := render("ra_nearest")
	cubic := render("ra_cubic")
	if bytes.Equal(nearest, cubic) {
		t.Fatal("ra_nearest and ra_cubic produced identical output")
	}
	if !bytes.Equal(nearest, render("ra_nearest")) {
		t.Fatal("rendering is not deterministic")
	}
}
