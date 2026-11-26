package ops

import (
	"encoding/base64"
	"fmt"
	"math"
	"strconv"
	"strings"
)

// Watermark position values, matching imgproxy's position argument.
const (
	WatermarkCenter     = "ce"
	WatermarkNorth      = "no"
	WatermarkSouth      = "so"
	WatermarkEast       = "ea"
	WatermarkWest       = "we"
	WatermarkNorthEast  = "noea"
	WatermarkNorthWest  = "nowe"
	WatermarkSouthEast  = "soea"
	WatermarkSouthWest  = "sowe"
	WatermarkRepeat     = "re"
	WatermarkChessboard = "ch"
)

var watermarkPositions = map[string]bool{
	WatermarkCenter: true, WatermarkNorth: true, WatermarkSouth: true,
	WatermarkEast: true, WatermarkWest: true, WatermarkNorthEast: true,
	WatermarkNorthWest: true, WatermarkSouthEast: true, WatermarkSouthWest: true,
	WatermarkRepeat: true, WatermarkChessboard: true,
}

// Watermark is imgproxy's wm_ option: opacity/position/offsets/scale applied to
// a watermark image supplied per request (wmu_) or by configuration.
type Watermark struct {
	Opacity  float64 // modifier on the watermark's own alpha; 1 = unchanged
	Position string
	XOffset  float64 // |v| >= 1 = pixels, otherwise a fraction of the image
	YOffset  float64
	Scale    float64 // watermark width relative to the result; 0 = native size
	// Size is the explicit wms_ fit size; nil keeps the native size.
	Size *Size
	// Rotate is the clockwise wmr_ rotation in degrees.
	Rotate float64
	// Shadow is the wmsh_ alpha-blur sigma; 0 disables the shadow.
	Shadow float64
}

func (w Watermark) canonical() string {
	return strings.Join([]string{
		trimFloat(w.Opacity), w.Position, trimFloat(w.XOffset), trimFloat(w.YOffset), trimFloat(w.Scale),
	}, ":")
}

// parseWatermark accepts wm_opacity:position:x_offset:y_offset:scale with every
// argument optional, which is what imgproxy allows.
func parseWatermark(v string) (Watermark, error) {
	w := Watermark{Opacity: 1, Position: WatermarkCenter}
	args := strings.Split(v, ":")
	if len(args) > 5 {
		return w, fmt.Errorf("wm takes at most 5 arguments (opacity:position:x_offset:y_offset:scale)")
	}
	if len(args) > 0 && args[0] != "" {
		f, err := strconv.ParseFloat(args[0], 64)
		if err != nil || f < 0 || f > 1 {
			return w, fmt.Errorf("wm opacity must be between 0 and 1")
		}
		w.Opacity = f
	}
	if len(args) > 1 && args[1] != "" {
		pos := strings.ToLower(args[1])
		if !watermarkPositions[pos] {
			return w, fmt.Errorf("wm position must be one of ce|no|so|ea|we|noea|nowe|soea|sowe|re|ch")
		}
		w.Position = pos
	}
	if len(args) > 2 && args[2] != "" {
		f, err := strconv.ParseFloat(args[2], 64)
		if err != nil {
			return w, fmt.Errorf("wm x_offset must be a number")
		}
		w.XOffset = f
	}
	if len(args) > 3 && args[3] != "" {
		f, err := strconv.ParseFloat(args[3], 64)
		if err != nil {
			return w, fmt.Errorf("wm y_offset must be a number")
		}
		w.YOffset = f
	}
	if len(args) > 4 && args[4] != "" {
		f, err := strconv.ParseFloat(args[4], 64)
		if err != nil || f < 0 {
			return w, fmt.Errorf("wm scale must be a non-negative number")
		}
		w.Scale = f
	}
	if w.Opacity == 0 {
		return Watermark{}, nil
	}
	return w, nil
}

// DecodeWatermarkURL decodes wmu_: a URL-safe base64 absolute http(s) URL.
func DecodeWatermarkURL(v string) (string, error) {
	u, err := DecodeFallbackURL(v)
	if err != nil {
		return "", fmt.Errorf("wmu must encode an absolute http(s) URL")
	}
	return u, nil
}

// Size is an explicit watermark size (imgproxy wms_). A zero side is derived
// from the watermark's aspect ratio; both sides zero is invalid.
type Size struct {
	Width  int
	Height int
}

func (s Size) canonical() string {
	return strconv.Itoa(s.Width) + ":" + strconv.Itoa(s.Height)
}

func parseWatermarkSize(v string) (Size, error) {
	parts := strings.Split(v, ":")
	if len(parts) != 2 {
		return Size{}, fmt.Errorf("wms takes width:height")
	}
	parse := func(side string) (int, error) {
		n, err := strconv.Atoi(side)
		if err != nil || n < 0 || n > MaxDimension {
			return 0, fmt.Errorf("wms sides must be integers between 0 and %d", MaxDimension)
		}
		return n, nil
	}
	w, err := parse(parts[0])
	if err != nil {
		return Size{}, err
	}
	h, err := parse(parts[1])
	if err != nil {
		return Size{}, err
	}
	if w == 0 && h == 0 {
		return Size{}, fmt.Errorf("wms requires at least one non-zero side")
	}
	return Size{Width: w, Height: h}, nil
}

// DecodeWatermarkText decodes wmt_: URL-safe base64 text. The text is rendered
// as plain text; Pango markup is not interpreted.
func DecodeWatermarkText(v string) (string, error) {
	raw, err := base64.RawURLEncoding.DecodeString(strings.TrimRight(v, "="))
	if err != nil {
		return "", fmt.Errorf("wmt must be URL-safe base64 text")
	}
	return string(raw), nil
}

func parseWatermarkRotate(v string) (float64, error) {
	f, err := strconv.ParseFloat(v, 64)
	if err != nil || math.IsNaN(f) || math.IsInf(f, 0) {
		return 0, fmt.Errorf("wmr must be a finite number of degrees")
	}
	f = math.Mod(f, 360)
	if f < 0 {
		f += 360
	}
	if f == 0 {
		f = 0 // normalise -0
	}
	return f, nil
}

func parseWatermarkShadow(v string) (float64, error) {
	f, err := strconv.ParseFloat(v, 64)
	if err != nil || math.IsNaN(f) || math.IsInf(f, 0) || f < 0 {
		return 0, fmt.Errorf("wmsh must be a non-negative finite number")
	}
	return f, nil
}
