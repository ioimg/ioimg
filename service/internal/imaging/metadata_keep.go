package imaging

import (
	"encoding/binary"

	"github.com/ioimg/ioimg/service/pkg/ops"
)

// FormatFromBytes maps an encoded payload back to the ops format vocabulary.
// It is used for f_best cache hits, where only the winning bytes are known.
func FormatFromBytes(src []byte) ops.Format {
	switch {
	case len(src) >= 2 && src[0] == 0xFF && src[1] == 0xD8:
		return ops.FormatJPEG
	case len(src) >= 8 && string(src[:8]) == "\x89PNG\r\n\x1a\n":
		return ops.FormatPNG
	case len(src) >= 6 && (string(src[:6]) == "GIF87a" || string(src[:6]) == "GIF89a"):
		return ops.FormatGIF
	case len(src) >= 12 && string(src[:4]) == "RIFF" && string(src[8:12]) == "WEBP":
		return ops.FormatWebP
	case isAVIFMagic(src):
		return ops.FormatAVIF
	}
	return ""
}

// carryMetadata copies the source EXIF/ICC/DPI blocks into an encoded JPEG or
// PNG. Pure-Go encoders do not write metadata, so sm_0 is a post-encode pass;
// formats without a supported metadata writer are returned unchanged.
func carryMetadata(out []byte, format ops.Format, source []byte) []byte {
	if len(source) == 0 {
		return out
	}
	switch format {
	case ops.FormatJPEG:
		return copyJPEGMetadata(out, source)
	case ops.FormatPNG:
		return copyPNGMetadata(out, source)
	}
	return out
}

// copyJPEGMetadata inserts APP0..APP15 segments from src after SOI. It stops at
// SOS, so pixel data is never scanned as metadata.
func copyJPEGMetadata(out, src []byte) []byte {
	if len(out) < 2 || out[0] != 0xFF || out[1] != 0xD8 ||
		len(src) < 4 || src[0] != 0xFF || src[1] != 0xD8 {
		return out
	}
	var segs [][]byte
	for i := 2; i+1 < len(src); {
		if src[i] != 0xFF {
			break
		}
		for i < len(src) && src[i] == 0xFF {
			i++
		}
		if i >= len(src) {
			break
		}
		marker := src[i]
		start := i - 1
		i++
		switch {
		case marker == 0xD9 || marker == 0xDA: // EOI/SOS
			i = len(src)
		case marker == 0x01 || marker == 0xD8 || (marker >= 0xD0 && marker <= 0xD7):
			continue
		default:
			if i+2 > len(src) {
				i = len(src)
				continue
			}
			n := int(binary.BigEndian.Uint16(src[i:]))
			if n < 2 || i+n > len(src) {
				i = len(src)
				continue
			}
			if marker >= 0xE0 && marker <= 0xEF {
				segs = append(segs, src[start:i+n])
			}
			i += n
		}
	}
	if len(segs) == 0 {
		return out
	}
	total := 2
	for _, s := range segs {
		total += len(s)
	}
	dst := make([]byte, 0, total+len(out)-2)
	dst = append(dst, out[:2]...)
	for _, s := range segs {
		dst = append(dst, s...)
	}
	dst = append(dst, out[2:]...)
	return dst
}

// copyPNGMetadata copies the EXIF, ICC and density chunks from src into out,
// directly after IHDR. Existing chunks of the same type in out are left alone.
func copyPNGMetadata(out, src []byte) []byte {
	const sig = "\x89PNG\r\n\x1a\n"
	if len(out) < 8 || string(out[:8]) != sig || len(src) < 8 || string(src[:8]) != sig {
		return out
	}
	want := map[string]bool{"eXIf": true, "iCCP": true, "pHYs": true}
	present := map[string]bool{}
	var chunks [][]byte
	for off := 8; off+12 <= len(out); {
		n := int(binary.BigEndian.Uint32(out[off:]))
		if n < 0 || off+12+n > len(out) {
			break
		}
		present[string(out[off+4:off+8])] = true
		off += 12 + n
	}
	for off := 8; off+12 <= len(src); {
		n := int(binary.BigEndian.Uint32(src[off:]))
		if n < 0 || off+12+n > len(src) {
			break
		}
		kind := string(src[off+4 : off+8])
		if want[kind] && !present[kind] {
			chunks = append(chunks, src[off:off+12+n])
			present[kind] = true
		}
		off += 12 + n
	}
	if len(chunks) == 0 {
		return out
	}
	// IHDR is always the first chunk: signature + length + type + 13 data + CRC.
	at := 8 + 4 + 4 + 13 + 4
	if len(out) < at {
		return out
	}
	dst := make([]byte, 0, len(out)+12*len(chunks))
	dst = append(dst, out[:at]...)
	for _, c := range chunks {
		dst = append(dst, c...)
	}
	dst = append(dst, out[at:]...)
	return dst
}
