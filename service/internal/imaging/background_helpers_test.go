package imaging

import (
	"bytes"
	"image"
	"image/color"
	"image/png"
	"testing"
)

func semitransparentPNG(t *testing.T, w, h int, c color.NRGBA) []byte {
	t.Helper()
	img := image.NewNRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			img.SetNRGBA(x, y, c)
		}
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func decodedPixel(t *testing.T, data []byte, x, y int) color.NRGBA {
	t.Helper()
	img, _, err := image.Decode(bytes.NewReader(data))
	if err != nil {
		t.Fatalf("decode output: %v", err)
	}
	return color.NRGBAModel.Convert(img.At(x, y)).(color.NRGBA)
}

func nearByte(got, want uint8, tolerance int) bool {
	diff := int(got) - int(want)
	if diff < 0 {
		diff = -diff
	}
	return diff <= tolerance
}

func assertPixel(t *testing.T, data []byte, x, y int, want color.NRGBA, tolerance int) {
	t.Helper()
	got := decodedPixel(t, data, x, y)
	if !nearByte(got.R, want.R, tolerance) || !nearByte(got.G, want.G, tolerance) ||
		!nearByte(got.B, want.B, tolerance) || !nearByte(got.A, want.A, tolerance) {
		t.Fatalf("pixel (%d,%d) = %+v, want %+v (+/- %d)", x, y, got, want, tolerance)
	}
}
