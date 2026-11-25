package ops

import (
	"encoding/base64"
	"fmt"
	"math"
	"net/url"
	"strconv"
	"strings"
)

// Per-format encoder overrides. These are the imgproxy Pro "PNG/WebP/AVIF
// options" knobs. A nil pointer means "argument not given" so that a URL which
// only overrides one knob keeps the encoder defaults for the rest — and, more
// importantly, so that the canonical form (and therefore the cache key) of
// existing URLs never changes.
type PNGEncoder struct {
	Interlaced *bool
	Quantize   *bool
	Colors     int // 0 = encoder default (256)
}

type WebPEncoder struct {
	Compression    *int // 0..6, maps to libwebp method / libvips reduction effort
	SmartSubsample *bool
	Preset         *int // 0..5
}

type AVIFEncoder struct {
	Subsample *int // 0 = 4:4:4, 1 = 4:2:2, 2 = 4:2:0
}

// Gradient overlays a colour ramp on the processed image: transparent at
// Start, fully (Opacity times) opaque at Stop, along Direction.
type Gradient struct {
	Opacity   float64 // 0 = off
	Color     RGB
	Direction string // down, up, right, left, or an angle in degrees
	Start     float64
	Stop      float64
}

// IsZero reports whether the gradient would change any pixel.
func (g Gradient) IsZero() bool { return g.Opacity <= 0 }

func (g Gradient) canonical() string {
	return fmt.Sprintf("%s:%s:%s:%s:%s",
		trimFloat(g.Opacity), g.Color.Hex(), g.Direction,
		trimFloat(g.Start), trimFloat(g.Stop))
}

// Angle returns the gradient direction as a clockwise angle in degrees, with 0
// pointing top→bottom (matching imgproxy).
func (g Gradient) Angle() float64 {
	switch g.Direction {
	case "up":
		return 180
	case "right":
		return 270
	case "left":
		return 90
	}
	if f, err := strconv.ParseFloat(g.Direction, 64); err == nil {
		return math.Mod(f, 360)
	}
	return 0
}

func (p PNGEncoder) canonical() string {
	return strings.Join([]string{
		boolArg(p.Interlaced), boolArg(p.Quantize), intArg(p.Colors),
	}, ":")
}

func (w WebPEncoder) canonical() string {
	return strings.Join([]string{
		intArgPtr(w.Compression), boolArg(w.SmartSubsample), intArgPtr(w.Preset),
	}, ":")
}

func (a AVIFEncoder) canonical() string {
	return intArgPtr(a.Subsample)
}

func boolArg(b *bool) string {
	if b == nil {
		return ""
	}
	if *b {
		return "1"
	}
	return "0"
}

func intArg(v int) string {
	if v == 0 {
		return ""
	}
	return strconv.Itoa(v)
}

func intArgPtr(v *int) string {
	if v == nil {
		return ""
	}
	return strconv.Itoa(*v)
}

func parsePNGEncoder(v string) (PNGEncoder, error) {
	var p PNGEncoder
	parts := strings.Split(v, ":")
	if len(parts) > 3 {
		return p, fmt.Errorf("pngo takes interlaced:quantize:quantization_colors")
	}
	if len(parts) > 0 && parts[0] != "" {
		b, err := parseBoolArg(parts[0])
		if err != nil {
			return p, fmt.Errorf("pngo interlaced: %w", err)
		}
		p.Interlaced = &b
	}
	if len(parts) > 1 && parts[1] != "" {
		b, err := parseBoolArg(parts[1])
		if err != nil {
			return p, fmt.Errorf("pngo quantize: %w", err)
		}
		p.Quantize = &b
	}
	if len(parts) > 2 && parts[2] != "" {
		n, err := strconv.Atoi(parts[2])
		if err != nil || n < 2 || n > 256 {
			return p, fmt.Errorf("pngo quantization_colors must be 2..256")
		}
		p.Colors = n
	}
	return p, nil
}

func parseWebPEncoder(v string) (WebPEncoder, error) {
	var w WebPEncoder
	parts := strings.Split(v, ":")
	if len(parts) > 3 {
		return w, fmt.Errorf("webpo takes compression:smart_subsample:preset")
	}
	if len(parts) > 0 && parts[0] != "" {
		n, err := strconv.Atoi(parts[0])
		if err != nil || n < 0 || n > 6 {
			return w, fmt.Errorf("webpo compression must be 0..6")
		}
		w.Compression = &n
	}
	if len(parts) > 1 && parts[1] != "" {
		b, err := parseBoolArg(parts[1])
		if err != nil {
			return w, fmt.Errorf("webpo smart_subsample: %w", err)
		}
		w.SmartSubsample = &b
	}
	if len(parts) > 2 && parts[2] != "" {
		n, err := strconv.Atoi(parts[2])
		if err != nil || n < 0 || n > 5 {
			return w, fmt.Errorf("webpo preset must be 0..5")
		}
		w.Preset = &n
	}
	return w, nil
}

func parseAVIFEncoder(v string) (AVIFEncoder, error) {
	var a AVIFEncoder
	parts := strings.Split(v, ":")
	if len(parts) > 1 {
		return a, fmt.Errorf("avifo takes subsample")
	}
	if parts[0] != "" {
		n, err := strconv.Atoi(parts[0])
		if err != nil || n < 0 || n > 2 {
			return a, fmt.Errorf("avifo subsample must be 0 (4:4:4), 1 (4:2:2) or 2 (4:2:0)")
		}
		a.Subsample = &n
	}
	return a, nil
}

func parseGradient(v string) (Gradient, error) {
	g := Gradient{Opacity: 1, Direction: "down", Start: 0, Stop: 1}
	parts := strings.Split(v, ":")
	if len(parts) > 5 {
		return g, fmt.Errorf("gr takes opacity:color:direction:start:stop")
	}
	if parts[0] != "" {
		f, err := strconv.ParseFloat(parts[0], 64)
		if err != nil || f < 0 || f > 1 {
			return g, fmt.Errorf("gr opacity must be 0..1")
		}
		g.Opacity = f
	}
	if len(parts) > 1 && parts[1] != "" {
		rgb, err := parseRGB(parts[1])
		if err != nil {
			return g, fmt.Errorf("gr color: %w", err)
		}
		g.Color = rgb
	}
	if len(parts) > 2 && parts[2] != "" {
		switch parts[2] {
		case "down", "up", "right", "left":
			g.Direction = parts[2]
		default:
			f, err := strconv.ParseFloat(parts[2], 64)
			if err != nil {
				return g, fmt.Errorf("gr direction must be down|up|right|left or degrees")
			}
			g.Direction = trimFloat(math.Mod(math.Abs(f), 360))
		}
	}
	if len(parts) > 3 && parts[3] != "" {
		f, err := strconv.ParseFloat(parts[3], 64)
		if err != nil || f < 0 || f > 1 {
			return g, fmt.Errorf("gr start must be 0..1")
		}
		g.Start = f
	}
	if len(parts) > 4 && parts[4] != "" {
		f, err := strconv.ParseFloat(parts[4], 64)
		if err != nil || f < 0 || f > 1 {
			return g, fmt.Errorf("gr stop must be 0..1")
		}
		g.Stop = f
	}
	if g.Stop <= g.Start {
		return g, fmt.Errorf("gr stop must be greater than start")
	}
	return g, nil
}

func parseBoolArg(v string) (bool, error) {
	switch v {
	case "1", "t", "true":
		return true, nil
	case "0", "f", "false":
		return false, nil
	}
	return false, fmt.Errorf("must be 0 or 1")
}

// Hashsum is imgproxy's hs_ verification: the source must hash to exactly this
// value or the request fails with 422. Only hex digests are accepted, so a URL
// cannot smuggle a pattern.
type Hashsum struct {
	Type string // "", md5, sha1, sha256, sha512
	Hex  string
}

// IsZero reports whether no verification was requested.
func (h Hashsum) IsZero() bool { return h.Type == "" || h.Hex == "" }

func (h Hashsum) canonical() string { return h.Type + ":" + h.Hex }

var hashsumHexLen = map[string]int{
	"md5":    32,
	"sha1":   40,
	"sha256": 64,
	"sha512": 128,
}

func parseHashsum(v string) (Hashsum, error) {
	h := Hashsum{}
	parts := strings.SplitN(v, ":", 2)
	if len(parts) != 2 {
		return h, fmt.Errorf("hs takes hashsum_type:hashsum")
	}
	kind := strings.ToLower(strings.TrimSpace(parts[0]))
	if kind == "none" || kind == "" {
		return Hashsum{}, nil
	}
	want, ok := hashsumHexLen[kind]
	if !ok {
		return h, fmt.Errorf("hs hashsum_type must be none|md5|sha1|sha256|sha512")
	}
	sum := strings.ToLower(strings.TrimSpace(parts[1]))
	if len(sum) != want {
		return h, fmt.Errorf("hs %s hashsum must be %d hex characters", kind, want)
	}
	for i := 0; i < len(sum); i++ {
		c := sum[i]
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return h, fmt.Errorf("hs hashsum must be hex")
		}
	}
	return Hashsum{Type: kind, Hex: sum}, nil
}

// DecodeFallbackURL decodes imgproxy's URL-safe base64 fiu_ argument.
func DecodeFallbackURL(v string) (string, error) {
	raw, err := base64.RawURLEncoding.DecodeString(strings.TrimRight(v, "="))
	if err != nil {
		return "", fmt.Errorf("fiu must be URL-safe base64")
	}
	u, err := url.Parse(string(raw))
	if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") {
		return "", fmt.Errorf("fiu must encode an absolute http(s) URL")
	}
	return u.String(), nil
}
