package ops

import (
	"fmt"
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
