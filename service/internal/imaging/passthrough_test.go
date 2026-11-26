package imaging

import (
	"bytes"
	"encoding/binary"
	"hash/crc32"
	"image"
	"image/color"
	"image/jpeg"
	"testing"
)

func passthroughJPEG(t *testing.T, w, h int) []byte {
	t.Helper()
	img := image.NewNRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			img.Set(x, y, color.NRGBA{R: uint8(x * 3), G: uint8(y * 5), B: 90, A: 255})
		}
	}
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, img, &jpeg.Options{Quality: 88}); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func passthroughPNGHeader(w, h uint32) []byte {
	var buf bytes.Buffer
	buf.Write([]byte{0x89, 'P', 'N', 'G', 0x0d, 0x0a, 0x1a, 0x0a})
	ihdr := make([]byte, 13)
	binary.BigEndian.PutUint32(ihdr[0:4], w)
	binary.BigEndian.PutUint32(ihdr[4:8], h)
	ihdr[8] = 8
	ihdr[9] = 2
	var length [4]byte
	binary.BigEndian.PutUint32(length[:], uint32(len(ihdr)))
	buf.Write(length[:])
	chunk := append([]byte("IHDR"), ihdr...)
	buf.Write(chunk)
	var crc [4]byte
	binary.BigEndian.PutUint32(crc[:], crc32.ChecksumIEEE(chunk))
	buf.Write(crc[:])
	return buf.Bytes()
}

func TestPassThroughReturnsSourceBytesUntouched(t *testing.T) {
	src := passthroughJPEG(t, 19, 11)
	res, err := PassThrough(src)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(res.Data, src) {
		t.Fatal("pass-through changed the source bytes")
	}
	if res.Info.Format != "jpeg" || res.Info.Width != 19 || res.Info.Height != 11 || res.Info.Bytes != len(src) {
		t.Fatalf("unexpected info: %+v", res.Info)
	}
	if _, ok := res.Timings["passthrough"]; !ok {
		t.Fatalf("timings do not mark the pass-through path: %+v", res.Timings)
	}
	if got := ContentTypeFromBytes(res.Data); got != "image/jpeg" {
		t.Fatalf("content type = %q", got)
	}
}

// TestPassThroughBypassesDimensionGuard proves the path never decodes: a
// header claiming 100k x 100k is returned unchanged, while the normal decoder
// rejects it long before pixels are read.
func TestPassThroughBypassesDimensionGuard(t *testing.T) {
	src := passthroughPNGHeader(100_000, 100_000)
	res, err := PassThrough(src)
	if err != nil {
		t.Fatalf("pass-through rejected oversized header: %v", err)
	}
	if !bytes.Equal(res.Data, src) {
		t.Fatal("pass-through changed the bomb bytes")
	}
	if res.Info.Format != "png" || res.Info.Width != 100_000 || res.Info.Height != 100_000 {
		t.Fatalf("unexpected probed info: %+v", res.Info)
	}
	if got := ContentTypeFromBytes(src); got != "image/png" {
		t.Fatalf("content type = %q", got)
	}
}

func TestProbeWebPAndAVIFHeaders(t *testing.T) {
	// Minimal WebP VP8X header: canvas is 320x200.
	webp := []byte("RIFF\x00\x00\x00\x00WEBPVP8X\x0a\x00\x00\x00\x00\x00\x00\x00\x3f\x01\x00\xc7\x00\x00")
	if info, err := Probe(webp); err != nil || info.Format != "webp" || info.Width != 320 || info.Height != 200 {
		t.Fatalf("Probe(WebP) = %+v, %v", info, err)
	}

	// Minimal AVIF header: ftyp + meta(full box) > iprp > ipco > ispe.
	box := func(kind string, payload []byte) []byte {
		out := make([]byte, 8, 8+len(payload))
		binary.BigEndian.PutUint32(out[0:4], uint32(8+len(payload)))
		copy(out[4:8], kind)
		return append(out, payload...)
	}
	ispe := make([]byte, 12)
	binary.BigEndian.PutUint32(ispe[4:8], 640)
	binary.BigEndian.PutUint32(ispe[8:12], 480)
	avif := box("ftyp", []byte("avif\x00\x00\x00\x00"))
	avif = append(avif, box("meta", append([]byte{0, 0, 0, 0}, box("iprp", box("ipco", box("ispe", ispe)))...))...)
	if info, err := Probe(avif); err != nil || info.Format != "avif" || info.Width != 640 || info.Height != 480 {
		t.Fatalf("Probe(AVIF) = %+v, %v", info, err)
	}
}
