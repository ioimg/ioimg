//go:build !vips

package imaging

import (
	"bytes"
	"encoding/binary"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"testing"

	"github.com/ioimg/ioimg/service/pkg/ops"
)

func dpiSource(t *testing.T) image.Image {
	t.Helper()
	img := image.NewNRGBA(image.Rect(0, 0, 16, 12))
	for y := 0; y < 12; y++ {
		for x := 0; x < 16; x++ {
			img.Set(x, y, color.NRGBA{R: 10, G: 200, B: 30, A: 255})
		}
	}
	return img
}

func TestSetJPEGDPI(t *testing.T) {
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, dpiSource(t), &jpeg.Options{Quality: 80}); err != nil {
		t.Fatal(err)
	}
	out := setDPI(buf.Bytes(), "jpeg", 144)
	if out[13] != 1 {
		t.Fatalf("units = %d, want 1 (dpi)", out[13])
	}
	if got := binary.BigEndian.Uint16(out[14:]); got != 144 {
		t.Fatalf("xdensity = %d, want 144", got)
	}
	if got := binary.BigEndian.Uint16(out[16:]); got != 144 {
		t.Fatalf("ydensity = %d, want 144", got)
	}
	// Still a decodable JPEG with the same pixels.
	if _, err := jpeg.Decode(bytes.NewReader(out)); err != nil {
		t.Fatalf("patched JPEG does not decode: %v", err)
	}
}

func TestSetPNGDPI(t *testing.T) {
	var buf bytes.Buffer
	if err := png.Encode(&buf, dpiSource(t)); err != nil {
		t.Fatal(err)
	}
	out := setDPI(buf.Bytes(), "png", 300)
	if !bytes.Contains(out, []byte("pHYs")) {
		t.Fatal("no pHYs chunk")
	}
	idx := bytes.Index(out, []byte("pHYs"))
	if ppm := binary.BigEndian.Uint32(out[idx+4:]); ppm < 11810 || ppm > 11812 {
		t.Fatalf("pHYs = %d px/m, want ~11811 (300 dpi)", ppm)
	}
	if out[idx+12] != 1 {
		t.Fatalf("pHYs unit = %d, want 1 (metre)", out[idx+12])
	}
	if _, err := png.Decode(bytes.NewReader(out)); err != nil {
		t.Fatalf("patched PNG does not decode: %v", err)
	}
	// Idempotent: a second pass must not duplicate the chunk.
	again := setDPI(out, "png", 300)
	if bytes.Count(again, []byte("pHYs")) != 1 {
		t.Fatalf("pHYs duplicated: %d", bytes.Count(again, []byte("pHYs")))
	}
}

func TestSetDPIIgnoresOtherFormats(t *testing.T) {
	data := []byte("not an image")
	if got := setDPI(data, "webp", 300); !bytes.Equal(got, data) {
		t.Fatal("webp should be untouched")
	}
	if got := setDPI(data, "jpeg", 0); !bytes.Equal(got, data) {
		t.Fatal("dpi 0 should be a no-op")
	}
}

func TestDPIViaProcessor(t *testing.T) {
	src := dpiSource(t)
	o, err := ops.Parse("w_8,f_jpeg,dpi_96", nil)
	if err != nil {
		t.Fatal(err)
	}
	res, err := DefaultProcessor().Process(encodePNGForTest(t, src), o, Options{OutFormat: ops.FormatJPEG, Quality: 80})
	if err != nil {
		t.Fatal(err)
	}
	if got := binary.BigEndian.Uint16(res.Data[14:]); got != 96 {
		t.Fatalf("density = %d, want 96", got)
	}
}

func encodePNGForTest(t *testing.T, img image.Image) []byte {
	t.Helper()
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}
