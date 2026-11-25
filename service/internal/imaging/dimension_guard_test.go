package imaging

import (
	"bytes"
	"encoding/binary"
	"errors"
	"hash/crc32"
	"testing"
)

// pngHeader builds a PNG signature + IHDR only. DecodeConfig succeeds because
// the dimensions live in IHDR; a full decode fails because there is no IDAT.
// That is exactly a decode bomb: cheap to send, expensive to decode.
func pngHeader(w, h uint32) []byte {
	var buf bytes.Buffer
	buf.Write([]byte{0x89, 'P', 'N', 'G', 0x0d, 0x0a, 0x1a, 0x0a})

	ihdr := make([]byte, 13)
	binary.BigEndian.PutUint32(ihdr[0:4], w)
	binary.BigEndian.PutUint32(ihdr[4:8], h)
	ihdr[8] = 8 // bit depth
	ihdr[9] = 2 // colour type: truecolour

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

// TestDecodeRejectsOversizedHeaderBeforeDecoding pins that MaxSide/MaxPixels
// are enforced from the header, so a 100k x 100k bomb never reaches image.Decode.
func TestDecodeRejectsOversizedHeaderBeforeDecoding(t *testing.T) {
	cases := []struct {
		name string
		w, h uint32
		want error
	}{
		{"side limit", 100_000, 10, ErrDimension},
		{"pixel limit", 8000, 6000, ErrDimension},
		{"both limits", 100_000, 100_000, ErrDimension},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, _, err := Decode(pngHeader(tc.w, tc.h))
			if !errors.Is(err, tc.want) {
				t.Fatalf("Decode(%dx%d) = %v, want %v", tc.w, tc.h, err, tc.want)
			}
		})
	}
}

// TestDecodeGuardDoesNotRejectLegalHeader keeps the guard from becoming a
// blanket denial: a header inside the limits must fall through to the decoder
// (which fails here only because the body is truncated).
func TestDecodeGuardDoesNotRejectLegalHeader(t *testing.T) {
	_, _, err := Decode(pngHeader(8000, 5000)) // exactly 40 MP
	if errors.Is(err, ErrDimension) {
		t.Fatalf("40 MP header rejected by the dimension guard: %v", err)
	}
	if !errors.Is(err, ErrDecode) {
		t.Fatalf("want ErrDecode for a header-only PNG, got %v", err)
	}
}
