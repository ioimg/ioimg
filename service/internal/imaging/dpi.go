package imaging

import (
	"bytes"
	"encoding/binary"
	"hash/crc32"
	"math"
)

// setDPI rewrites the density metadata of an already encoded JPEG or PNG. It is
// deliberately post-encode: both encoders we use either cannot write density
// (image/png, image/jpeg) or strip it during export, and post-processing keeps
// the pixel pipeline untouched. Formats without a density field (WebP, AVIF,
// GIF) are returned unchanged.
func setDPI(data []byte, format string, dpi float64) []byte {
	if dpi <= 0 {
		return data
	}
	switch format {
	case "jpeg", "jpg":
		return setJPEGDPI(data, dpi)
	case "png":
		return setPNGDPI(data, dpi)
	}
	return data
}

// setJPEGDPI writes a JFIF APP0 segment with the requested density. Go's JPEG
// encoder emits no APP0 at all (it starts with a DQT), so the segment is
// inserted right after SOI; when a JFIF APP0 already exists its density fields
// are patched in place instead so the file is not grown.
func setJPEGDPI(data []byte, dpi float64) []byte {
	if len(data) < 4 || data[0] != 0xFF || data[1] != 0xD8 {
		return data
	}
	v := uint16(math.Round(dpi))
	if v == 0 {
		v = 1
	}
	// Walk the segment list looking for APP0/JFIF.
	for i := 2; i+10 < len(data); {
		if data[i] != 0xFF {
			break
		}
		marker := data[i+1]
		if marker == 0xDA || marker == 0xD9 { // SOS / EOI: no metadata before pixels
			break
		}
		segLen := int(binary.BigEndian.Uint16(data[i+2:]))
		if segLen < 2 || i+2+segLen > len(data) {
			break
		}
		if marker == 0xE0 && segLen >= 16 && bytes.HasPrefix(data[i+4:], []byte("JFIF\x00")) {
			data[i+11] = 1 // units: dots per inch
			binary.BigEndian.PutUint16(data[i+12:], v)
			binary.BigEndian.PutUint16(data[i+14:], v)
			return data
		}
		i += 2 + segLen
	}

	app0 := []byte{
		0xFF, 0xE0, 0x00, 0x10,
		'J', 'F', 'I', 'F', 0x00,
		0x01, 0x01, // version 1.1
		0x01,       // units: dpi
		0, 0, 0, 0, // x/y density, filled below
		0x00, 0x00, // no thumbnail
	}
	binary.BigEndian.PutUint16(app0[12:], v)
	binary.BigEndian.PutUint16(app0[14:], v)

	out := make([]byte, 0, len(data)+len(app0))
	out = append(out, data[:2]...)
	out = append(out, app0...)
	out = append(out, data[2:]...)
	return out
}

// setPNGDPI inserts a pHYs chunk directly after IHDR. pHYs carries pixels per
// metre and is the only density representation PNG has.
func setPNGDPI(data []byte, dpi float64) []byte {
	const ihdrEnd = 8 + 4 + 4 + 13 + 4 // signature + len + type + IHDR data + crc
	if len(data) < ihdrEnd || !bytes.HasPrefix(data, []byte("\x89PNG\r\n\x1a\n")) {
		return data
	}
	ppm := uint32(math.Round(dpi / 0.0254))
	// An existing pHYs is rewritten in place, which keeps the chunk unique and
	// makes a second call idempotent (libvips already writes density when the
	// source carried it).
	if idx := bytes.Index(data[:min(len(data), 128)], []byte("pHYs")); idx >= 4 {
		binary.BigEndian.PutUint32(data[idx+4:], ppm)
		binary.BigEndian.PutUint32(data[idx+8:], ppm)
		data[idx+12] = 1
		binary.BigEndian.PutUint32(data[idx+13:], crc32.ChecksumIEEE(data[idx-4:idx+13]))
		return data
	}
	chunk := make([]byte, 0, 21)
	var head [4]byte
	binary.BigEndian.PutUint32(head[:], 9)
	chunk = append(chunk, head[:]...)
	chunk = append(chunk, "pHYs"...)
	var ppmv [4]byte
	binary.BigEndian.PutUint32(ppmv[:], ppm)
	chunk = append(chunk, ppmv[:]...)
	chunk = append(chunk, ppmv[:]...)
	chunk = append(chunk, 1) // unit: metre
	binary.BigEndian.PutUint32(head[:], crc32.ChecksumIEEE(chunk[4:]))
	chunk = append(chunk, head[:]...)

	out := make([]byte, 0, len(data)+len(chunk))
	out = append(out, data[:ihdrEnd]...)
	out = append(out, chunk...)
	out = append(out, data[ihdrEnd:]...)
	return out
}
