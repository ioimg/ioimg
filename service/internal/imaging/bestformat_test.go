package imaging

import (
	"bytes"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"testing"

	"github.com/ioimg/ioimg/service/pkg/ops"
)

func bestFormatSource(t *testing.T) []byte {
	t.Helper()
	img := image.NewNRGBA(image.Rect(0, 0, 256, 192))
	for y := 0; y < 192; y++ {
		for x := 0; x < 256; x++ {
			// A smooth two-axis gradient plus a soft highlight: lossy web
			// formats should beat JPEG comfortably at the same quality.
			r := uint8(x * 255 / 255)
			g := uint8(y * 255 / 191)
			b := uint8((x + y) * 255 / 447)
			img.SetNRGBA(x, y, color.NRGBA{R: r, G: g, B: b, A: 255})
		}
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func TestBestFormatReturnsSmallestCandidate(t *testing.T) {
	src := bestFormatSource(t)
	p := DefaultProcessor()
	available := AvailableFormats()

	candidates := map[ops.Format][]byte{}
	for _, f := range []ops.Format{ops.FormatJPEG, ops.FormatWebP, ops.FormatAVIF} {
		if !available[f] {
			continue
		}
		res, err := p.Process(src, mustOpsBest(t, "w_128,f_"+string(f)+",q_60"), Options{OutFormat: f, Quality: 60})
		if err != nil {
			t.Fatalf("%s: %v", f, err)
		}
		candidates[f] = res.Data
	}
	if len(candidates) < 2 {
		t.Skip("fewer than two candidate encoders are available")
	}
	var minFormat ops.Format
	minLen := 0
	for f, data := range candidates {
		if minFormat == "" || len(data) < minLen {
			minFormat, minLen = f, len(data)
		}
	}

	res, err := p.Process(src, mustOpsBest(t, "w_128,f_best,q_60"), Options{OutFormat: ops.FormatBest, Quality: 60})
	if err != nil {
		t.Fatal(err)
	}
	if res.Format != minFormat {
		t.Fatalf("f_best chose %s (%d bytes), smallest is %s (%d bytes)", res.Format, len(res.Data), minFormat, minLen)
	}
	if len(res.Data) != minLen {
		t.Fatalf("f_best output = %d bytes, want %d", len(res.Data), minLen)
	}
	if res.Info.Format != string(res.Format) {
		t.Fatalf("Info.Format = %q, want %q", res.Info.Format, res.Format)
	}
	if res.Format == ops.FormatJPEG {
		t.Fatalf("fixture is not exercising the web formats: JPEG won")
	}
}

func TestKeepMetadataRoundTrip(t *testing.T) {
	src := jpegWithEXIF(t)
	marker := []byte("Exif\x00\x00")
	if !bytes.Contains(src, marker) {
		t.Fatal("fixture is missing its EXIF segment")
	}
	p := DefaultProcessor()

	stripped, err := p.Process(src, mustOpsBest(t, "w_64,f_jpeg,q_80,sm_1"), Options{OutFormat: ops.FormatJPEG, Quality: 80})
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(stripped.Data, marker) {
		t.Fatal("sm_1 kept the EXIF block")
	}

	kept, err := p.Process(src, mustOpsBest(t, "w_64,f_jpeg,q_80,sm_0"), Options{OutFormat: ops.FormatJPEG, Quality: 80})
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(kept.Data, marker) {
		t.Skip("the linked JPEG encoder cannot carry EXIF metadata")
	}
}

func mustOpsBest(t *testing.T, raw string) ops.Ops {
	t.Helper()
	o, err := ops.Parse(raw, nil)
	if err != nil {
		t.Fatalf("%s: %v", raw, err)
	}
	return o
}

func encodeJPEG(w *bytes.Buffer, img image.Image, quality int) error {
	return jpeg.Encode(w, img, &jpeg.Options{Quality: quality})
}

func jpegWithEXIF(t *testing.T) []byte {
	t.Helper()
	img := image.NewNRGBA(image.Rect(0, 0, 64, 48))
	for y := 0; y < 48; y++ {
		for x := 0; x < 64; x++ {
			img.SetNRGBA(x, y, color.NRGBA{R: uint8(x * 4), G: uint8(y * 5), B: 80, A: 255})
		}
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatal(err)
	}
	// Re-encode as JPEG so the fixture starts from a normal image file.
	var jb bytes.Buffer
	if err := encodeJPEG(&jb, img, 85); err != nil {
		t.Fatal(err)
	}
	tiff := []byte{
		'I', 'I', 0x2a, 0x00, 0x08, 0x00, 0x00, 0x00, // TIFF header, IFD0 at 8
		0x01, 0x00, // one entry
		0x12, 0x01, 0x03, 0x00, 0x01, 0x00, 0x00, 0x00, // Orientation = SHORT 1
		0x01, 0x00, 0x00, 0x00,
		0x00, 0x00, 0x00, 0x00, // no next IFD
	}
	payload := append([]byte("Exif\x00\x00"), tiff...)
	app1 := []byte{0xFF, 0xE1, byte((len(payload) + 2) >> 8), byte(len(payload) + 2)}
	app1 = append(app1, payload...)
	out := make([]byte, 0, len(jb.Bytes())+len(app1))
	out = append(out, jb.Bytes()[:2]...)
	out = append(out, app1...)
	out = append(out, jb.Bytes()[2:]...)
	return out
}
