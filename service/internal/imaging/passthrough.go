package imaging

import "encoding/binary"

// Probe reads only enough of src to report its format and pixel dimensions.
// It never decodes pixel data, so it is safe for sources that exceed the
// decode limits: raw/skip-processing responses deliberately pass those through.
func Probe(src []byte) (Info, error) {
	info := Info{Bytes: len(src)}
	switch {
	case len(src) >= 2 && src[0] == 0xFF && src[1] == 0xD8:
		w, h, ok := jpegSize(src)
		if !ok {
			return info, ErrDecode
		}
		info.Format, info.Width, info.Height = "jpeg", w, h
	case len(src) >= 24 && string(src[:8]) == "\x89PNG\r\n\x1a\n":
		info.Format = "png"
		info.Width = int(binary.BigEndian.Uint32(src[16:20]))
		info.Height = int(binary.BigEndian.Uint32(src[20:24]))
	case len(src) >= 10 && (string(src[:6]) == "GIF87a" || string(src[:6]) == "GIF89a"):
		info.Format = "gif"
		info.Width = int(binary.LittleEndian.Uint16(src[6:8]))
		info.Height = int(binary.LittleEndian.Uint16(src[8:10]))
	case len(src) >= 12 && string(src[:4]) == "RIFF" && string(src[8:12]) == "WEBP":
		w, h, ok := webpSize(src)
		if !ok {
			return info, ErrDecode
		}
		info.Format, info.Width, info.Height = "webp", w, h
	case isAVIFMagic(src):
		w, h, ok := avifSize(src)
		if !ok {
			return info, ErrDecode
		}
		info.Format, info.Width, info.Height = "avif", w, h
	default:
		return info, ErrDecode
	}
	if info.Width <= 0 || info.Height <= 0 {
		return info, ErrDecode
	}
	return info, nil
}

// PassThrough returns src unchanged, with header-only metadata. The returned
// bytes are the exact source bytes: no decode, transform, encode, resolution
// check, or file-size check runs on this path. Unknown sources still pass
// through (raw is explicitly unchecked); their format is left empty so the
// HTTP layer can fall back to application/octet-stream.
func PassThrough(src []byte) (Result, error) {
	if len(src) == 0 {
		return Result{}, ErrDecode
	}
	info, err := Probe(src)
	if err != nil {
		info = Info{Bytes: len(src)}
	}
	return Result{Data: src, Info: info, Timings: map[string]float64{"passthrough": 0}}, nil
}

// ContentTypeFromBytes maps magic bytes to a media type. It is used by the
// pass-through paths, which do not have a trusted output format. It only needs
// the signature, so a truncated or otherwise unparseable image still gets the
// right image/* type.
func ContentTypeFromBytes(src []byte) string {
	switch {
	case len(src) >= 2 && src[0] == 0xFF && src[1] == 0xD8:
		return "image/jpeg"
	case len(src) >= 8 && string(src[:8]) == "\x89PNG\r\n\x1a\n":
		return "image/png"
	case len(src) >= 6 && (string(src[:6]) == "GIF87a" || string(src[:6]) == "GIF89a"):
		return "image/gif"
	case len(src) >= 12 && string(src[:4]) == "RIFF" && string(src[8:12]) == "WEBP":
		return "image/webp"
	case isAVIFMagic(src):
		return "image/avif"
	default:
		return "application/octet-stream"
	}
}

func isAVIFMagic(src []byte) bool {
	if len(src) < 12 || string(src[4:8]) != "ftyp" {
		return false
	}
	end := 8 + int(binary.BigEndian.Uint32(src[:4]))
	if end > len(src) || end < 12 {
		end = len(src)
	}
	brands := src[8:end]
	for _, brand := range []string{"avif", "avis"} {
		for i := 0; i+4 <= len(brands); i += 4 {
			if string(brands[i:i+4]) == brand {
				return true
			}
		}
	}
	return false
}

func jpegSize(src []byte) (int, int, bool) {
	if len(src) < 4 {
		return 0, 0, false
	}
	for i := 2; i+1 < len(src); {
		if src[i] != 0xFF {
			i++
			continue
		}
		for i < len(src) && src[i] == 0xFF {
			i++
		}
		if i >= len(src) {
			break
		}
		marker := src[i]
		i++
		// Standalone markers have no length field.
		if marker == 0x01 || marker == 0xD8 || marker == 0xD9 || (marker >= 0xD0 && marker <= 0xD7) {
			continue
		}
		if i+2 > len(src) {
			break
		}
		segLen := int(binary.BigEndian.Uint16(src[i : i+2]))
		if segLen < 2 || i+segLen > len(src) {
			break
		}
		// SOF0..SOF15, excluding DHT (C4), JPG (C8) and DAC (CC).
		if marker >= 0xC0 && marker <= 0xCF && marker != 0xC4 && marker != 0xC8 && marker != 0xCC {
			if segLen < 7 {
				break
			}
			h := int(binary.BigEndian.Uint16(src[i+3 : i+5]))
			w := int(binary.BigEndian.Uint16(src[i+5 : i+7]))
			return w, h, w > 0 && h > 0
		}
		i += segLen
	}
	return 0, 0, false
}

func webpSize(src []byte) (int, int, bool) {
	for off := 12; off+8 <= len(src); {
		kind := string(src[off : off+4])
		size := int(binary.LittleEndian.Uint32(src[off+4 : off+8]))
		data := off + 8
		if size < 0 || data+size > len(src) {
			size = len(src) - data
		}
		switch kind {
		case "VP8X":
			if size >= 10 {
				w := 1 + int(uint32(src[data+4])|uint32(src[data+5])<<8|uint32(src[data+6])<<16)
				h := 1 + int(uint32(src[data+7])|uint32(src[data+8])<<8|uint32(src[data+9])<<16)
				return w, h, true
			}
		case "VP8L":
			if size >= 5 && src[data] == 0x2F {
				p := src[data:]
				w := 1 + int(uint32(p[1])|uint32(p[2]&0x3F)<<8)
				h := 1 + int(uint32(p[2]&0xC0)>>6|uint32(p[3])<<2|uint32(p[4]&0x0F)<<10)
				return w, h, true
			}
		case "VP8 ":
			if size >= 10 && src[data+3] == 0x9D && src[data+4] == 0x01 && src[data+5] == 0x2A {
				w := int(binary.LittleEndian.Uint16(src[data+6:data+8]) & 0x3FFF)
				h := int(binary.LittleEndian.Uint16(src[data+8:data+10]) & 0x3FFF)
				return w, h, true
			}
		}
		off = data + size
		if size&1 == 1 {
			off++
		}
	}
	return 0, 0, false
}

// avifSize walks the ISO BMFF box tree and reads the first ispe (image spatial
// extents) box. Only container boxes are descended into, so arbitrary payload
// bytes cannot be mistaken for a box header.
func avifSize(src []byte) (int, int, bool) {
	return walkBMFF(src, 0)
}

var bmffContainers = map[string]bool{
	"meta": true, "iprp": true, "ipco": true, "iinf": true, "iref": true,
	"dinf": true, "moov": true, "trak": true, "mdia": true, "minf": true,
	"stbl": true,
}

var bmffFullBoxes = map[string]bool{
	"meta": true, "iinf": true, "iref": true,
}

func walkBMFF(data []byte, depth int) (int, int, bool) {
	if depth > 8 {
		return 0, 0, false
	}
	for off := 0; off+8 <= len(data); {
		size := uint64(binary.BigEndian.Uint32(data[off : off+4]))
		kind := string(data[off+4 : off+8])
		hdr := uint64(8)
		switch size {
		case 0:
			size = uint64(len(data) - off)
		case 1:
			if off+16 > len(data) {
				return 0, 0, false
			}
			size = binary.BigEndian.Uint64(data[off+8 : off+16])
			hdr = 16
		}
		if size < hdr || uint64(off)+size > uint64(len(data)) {
			return 0, 0, false
		}
		payload := data[off+int(hdr) : off+int(size)]
		if kind == "ispe" {
			if len(payload) >= 12 {
				w := int(binary.BigEndian.Uint32(payload[4:8]))
				h := int(binary.BigEndian.Uint32(payload[8:12]))
				return w, h, w > 0 && h > 0
			}
			return 0, 0, false
		}
		if bmffContainers[kind] {
			sub := payload
			if bmffFullBoxes[kind] {
				if len(sub) < 4 {
					return 0, 0, false
				}
				sub = sub[4:]
			}
			if w, h, ok := walkBMFF(sub, depth+1); ok {
				return w, h, true
			}
		}
		off += int(size)
	}
	return 0, 0, false
}
