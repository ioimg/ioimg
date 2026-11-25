// Package ops parses and canonicalises ioimg URL transformation parameters.
//
// A parameter list is a comma separated sequence of name_value pairs, for
// example: w_800,h_600,c_fill,g_auto,f_auto,q_auto. Order never matters: the
// canonical form is what signatures and cache keys are built from.
package ops

import (
	"encoding/json"
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"
)

// Format is an output image format.
type Format string

const (
	FormatAuto Format = "auto"
	FormatAVIF Format = "avif"
	FormatWebP Format = "webp"
	FormatJPEG Format = "jpeg"
	FormatPNG  Format = "png"
	FormatGIF  Format = "gif"
	FormatJSON Format = "json"
)

// Resize is the geometry mode (c_*).
type Resize string

const (
	ResizeFit   Resize = "fit"
	ResizeFill  Resize = "fill"
	ResizeForce Resize = "force"
	ResizePad   Resize = "pad"
	ResizeCrop  Resize = "crop"
	ResizeScale Resize = "scale"
)

// Gravity is the crop/pad anchor (g_*).
type Gravity string

const (
	GravityCenter    Gravity = "center"
	GravityNorth     Gravity = "north"
	GravitySouth     Gravity = "south"
	GravityEast      Gravity = "east"
	GravityWest      Gravity = "west"
	GravityNorthEast Gravity = "north_east"
	GravityNorthWest Gravity = "north_west"
	GravitySouthEast Gravity = "south_east"
	GravitySouthWest Gravity = "south_west"
	GravityAuto      Gravity = "auto"
	GravityFace      Gravity = "face"
)

// MaxDimension is the hard limit for a requested width or height.
const MaxDimension = 8192

// Ops is a normalised transformation request. Zero values mean "not set".
type Ops struct {
	Width      int
	Height     int
	WidthAuto  bool
	HeightAuto bool
	Resize     Resize
	Gravity    Gravity
	Format     Format
	Quality    int // 0 = auto
	DPR        float64
	Background string // RRGGBB or "transparent"; empty = not set
	Blur       float64
	Sharpen    float64
	Rotate     int    // 0, 90, 180, 270
	Flip       string // "", "h", "v", "hv"
	Trim       int
	Strip      *bool // nil = default (true)
	Enlarge    bool
	Palette    bool
	Preset     string
}

// Default returns the implicit defaults from docs/API.md.
func Default() Ops {
	return Ops{
		Resize:  ResizeFit,
		Gravity: GravityCenter,
		Format:  FormatAuto,
		DPR:     1,
	}
}

// Parse parses a raw parameter list, expanding an optional preset first.
// Unknown names, bad values, and conflicting combinations are errors.
func Parse(raw string, presets map[string]string) (Ops, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return Ops{}, fmt.Errorf("empty ops")
	}
	pairs, err := splitPairs(raw)
	if err != nil {
		return Ops{}, err
	}

	// Pass 1: resolve preset (at most one, no recursion).
	preset := ""
	for _, p := range pairs {
		if p.key == "t" {
			if preset != "" {
				return Ops{}, fmt.Errorf("only one preset is allowed")
			}
			preset = p.value
		}
	}
	o := Default()
	if preset != "" {
		expanded, ok := presets[preset]
		if !ok {
			return Ops{}, fmt.Errorf("unknown preset %q", preset)
		}
		ep, err := splitPairs(expanded)
		if err != nil {
			return Ops{}, fmt.Errorf("preset %q: %w", preset, err)
		}
		for _, p := range ep {
			if p.key == "t" {
				return Ops{}, fmt.Errorf("preset %q may not reference another preset", preset)
			}
			if err := o.set(p.key, p.value); err != nil {
				return Ops{}, fmt.Errorf("preset %q: %w", preset, err)
			}
		}
		o.Preset = preset
	}

	// Pass 2: explicit parameters override the preset.
	for _, p := range pairs {
		if p.key == "t" {
			continue
		}
		if err := o.set(p.key, p.value); err != nil {
			return Ops{}, err
		}
	}
	if err := o.validate(); err != nil {
		return Ops{}, err
	}
	return o, nil
}

type pair struct{ key, value string }

func splitPairs(raw string) ([]pair, error) {
	parts := strings.Split(raw, ",")
	out := make([]pair, 0, len(parts))
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		k, v, ok := strings.Cut(part, "_")
		if !ok || k == "" || v == "" {
			return nil, fmt.Errorf("invalid op %q (want name_value)", part)
		}
		if strings.ContainsAny(k, " \t") || strings.ContainsAny(v, " \t") {
			return nil, fmt.Errorf("invalid op %q (spaces are not allowed)", part)
		}
		out = append(out, pair{key: k, value: v})
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("empty ops")
	}
	return out, nil
}

func (o *Ops) set(key, value string) error {
	switch key {
	case "w":
		if value == "auto" {
			o.WidthAuto = true
			return nil
		}
		n, err := parseDim(value)
		if err != nil {
			return fmt.Errorf("w: %w", err)
		}
		o.Width = n
	case "h":
		if value == "auto" {
			o.HeightAuto = true
			return nil
		}
		n, err := parseDim(value)
		if err != nil {
			return fmt.Errorf("h: %w", err)
		}
		o.Height = n
	case "c":
		switch Resize(value) {
		case ResizeFit, ResizeFill, ResizeForce, ResizePad, ResizeCrop, ResizeScale:
			o.Resize = Resize(value)
		default:
			return fmt.Errorf("c: unknown mode %q", value)
		}
	case "g":
		switch Gravity(value) {
		case GravityCenter, GravityNorth, GravitySouth, GravityEast, GravityWest,
			GravityNorthEast, GravityNorthWest, GravitySouthEast, GravitySouthWest,
			GravityAuto, GravityFace:
			o.Gravity = Gravity(value)
		default:
			return fmt.Errorf("g: unknown gravity %q", value)
		}
	case "f":
		f, ok := parseFormat(value)
		if !ok {
			return fmt.Errorf("f: unknown format %q", value)
		}
		o.Format = f
	case "q":
		if value == "auto" {
			o.Quality = 0
			return nil
		}
		n, err := strconv.Atoi(value)
		if err != nil || n < 1 || n > 100 {
			return fmt.Errorf("q must be 1..100 or auto")
		}
		o.Quality = n
	case "dpr":
		f, err := strconv.ParseFloat(value, 64)
		if err != nil {
			return fmt.Errorf("dpr must be a number")
		}
		switch f {
		case 1, 1.5, 2, 3:
			o.DPR = f
		default:
			return fmt.Errorf("dpr must be 1, 1.5, 2 or 3")
		}
	case "bg":
		if value != "transparent" {
			if len(value) != 6 || !isHex(value) {
				return fmt.Errorf("bg must be RRGGBB or transparent")
			}
			value = strings.ToLower(value)
		}
		o.Background = value
	case "blur":
		f, err := strconv.ParseFloat(value, 64)
		if err != nil || f < 0 || f > 100 {
			return fmt.Errorf("blur must be 0..100")
		}
		o.Blur = f
	case "sharp", "sharpen":
		f, err := strconv.ParseFloat(value, 64)
		if err != nil || f < 0 || f > 10 {
			return fmt.Errorf("sharp must be 0..10")
		}
		o.Sharpen = f
	case "rot":
		n, err := strconv.Atoi(value)
		if err != nil || (n != 90 && n != 180 && n != 270) {
			return fmt.Errorf("rot must be 90, 180 or 270")
		}
		o.Rotate = n
	case "flip":
		if value != "h" && value != "v" && value != "hv" {
			return fmt.Errorf("flip must be h, v or hv")
		}
		o.Flip = value
	case "trim":
		n, err := strconv.Atoi(value)
		if err != nil || n < 1 || n > 40 {
			return fmt.Errorf("trim must be 1..40")
		}
		o.Trim = n
	case "strip":
		switch value {
		case "0":
			v := false
			o.Strip = &v
		case "1":
			v := true
			o.Strip = &v
		default:
			return fmt.Errorf("strip must be 0 or 1")
		}
	case "enlarge":
		switch value {
		case "0":
			o.Enlarge = false
		case "1":
			o.Enlarge = true
		default:
			return fmt.Errorf("enlarge must be 0 or 1")
		}
	case "palette":
		switch value {
		case "0":
			o.Palette = false
		case "1":
			o.Palette = true
		default:
			return fmt.Errorf("palette must be 0 or 1")
		}
	case "t":
		o.Preset = value
	default:
		return fmt.Errorf("unknown op %q", key)
	}
	return nil
}

func (o *Ops) validate() error {
	if o.WidthAuto && o.HeightAuto {
		return fmt.Errorf("w_auto and h_auto cannot be combined")
	}
	if o.WidthAuto && o.Height == 0 {
		return fmt.Errorf("w_auto requires h")
	}
	if o.HeightAuto && o.Width == 0 {
		return fmt.Errorf("h_auto requires w")
	}
	if o.Resize == ResizeForce && (o.WidthAuto || o.HeightAuto) {
		return fmt.Errorf("c_force cannot be used with w_auto/h_auto")
	}
	if o.Resize != ResizeForce && o.Resize != ResizeCrop {
		if o.WidthAuto && o.HeightAuto {
			return fmt.Errorf("conflicting geometry")
		}
	}
	if o.Width == 0 && o.Height == 0 && !o.WidthAuto && !o.HeightAuto {
		if o.Format == FormatAuto {
			return fmt.Errorf("at least one of w, h or f is required")
		}
	}
	if o.Rotate != 0 && o.Flip != "" {
		return fmt.Errorf("rot and flip cannot be combined")
	}
	return nil
}

func parseDim(v string) (int, error) {
	n, err := strconv.Atoi(v)
	if err != nil {
		return 0, fmt.Errorf("must be a number or auto")
	}
	if n < 1 || n > MaxDimension {
		return 0, fmt.Errorf("must be 1..%d", MaxDimension)
	}
	return n, nil
}

func parseFormat(v string) (Format, bool) {
	switch Format(v) {
	case FormatAuto, FormatAVIF, FormatWebP, FormatJPEG, FormatPNG, FormatGIF, FormatJSON:
		return Format(v), true
	case "jpg":
		return FormatJPEG, true
	}
	return "", false
}

func isHex(s string) bool {
	for _, c := range s {
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f' || c >= 'A' && c <= 'F') {
			return false
		}
	}
	return true
}

// EffectiveWidth returns the clamped pixel width after DPR, or 0.
func (o Ops) EffectiveWidth() int {
	return clampDim(float64(o.Width) * o.DPR)
}

// EffectiveHeight returns the clamped pixel height after DPR, or 0.
func (o Ops) EffectiveHeight() int {
	return clampDim(float64(o.Height) * o.DPR)
}

func clampDim(v float64) int {
	if v <= 0 {
		return 0
	}
	if v > MaxDimension {
		return MaxDimension
	}
	return int(math.Round(v))
}

// Clamped reports whether DPR scaling had to clamp a dimension.
func (o Ops) Clamped() bool {
	if o.DPR <= 1 {
		return false
	}
	return float64(o.Width)*o.DPR > MaxDimension || float64(o.Height)*o.DPR > MaxDimension
}

// Canonical returns the stable string used for signatures and cache keys.
func (o Ops) Canonical() string {
	var b strings.Builder
	add := func(k, v string) {
		if b.Len() > 0 {
			b.WriteByte(',')
		}
		b.WriteString(k)
		b.WriteByte('_')
		b.WriteString(v)
	}
	if o.Background != "" {
		add("bg", o.Background)
	}
	if o.Blur != 0 {
		add("blur", trimFloat(o.Blur))
	}
	add("c", string(o.Resize))
	if o.DPR != 1 {
		add("dpr", trimFloat(o.DPR))
	}
	if o.Enlarge {
		add("enlarge", "1")
	}
	add("f", string(o.Format))
	if o.Flip != "" {
		add("flip", o.Flip)
	}
	add("g", string(o.Gravity))
	if o.HeightAuto {
		add("h", "auto")
	} else if o.Height != 0 {
		add("h", strconv.Itoa(o.Height))
	}
	if o.Palette {
		add("palette", "1")
	}
	if o.Quality != 0 {
		add("q", strconv.Itoa(o.Quality))
	}
	if o.Rotate != 0 {
		add("rot", strconv.Itoa(o.Rotate))
	}
	if o.Sharpen != 0 {
		add("sharp", trimFloat(o.Sharpen))
	}
	if o.Strip == nil || *o.Strip {
		add("strip", "1")
	} else {
		add("strip", "0")
	}
	if o.Trim != 0 {
		add("trim", strconv.Itoa(o.Trim))
	}
	if o.WidthAuto {
		add("w", "auto")
	} else if o.Width != 0 {
		add("w", strconv.Itoa(o.Width))
	}
	return b.String()
}

func trimFloat(f float64) string {
	s := strconv.FormatFloat(f, 'f', 2, 64)
	s = strings.TrimRight(s, "0")
	return strings.TrimSuffix(s, ".")
}

// ParsePresets decodes a JSON object of preset name -> ops string.
func ParsePresets(raw string) (map[string]string, error) {
	out := map[string]string{}
	if strings.TrimSpace(raw) == "" {
		return out, nil
	}
	if err := json.Unmarshal([]byte(raw), &out); err != nil {
		return nil, fmt.Errorf("presets must be a JSON object: %w", err)
	}
	for name, expansion := range out {
		if strings.TrimSpace(name) == "" {
			return nil, fmt.Errorf("preset name cannot be empty")
		}
		if _, err := Parse(expansion, nil); err != nil {
			return nil, fmt.Errorf("preset %q: %w", name, err)
		}
		if strings.Contains(expansion, "t_") {
			return nil, fmt.Errorf("preset %q may not reference another preset", name)
		}
	}
	return out, nil
}

// SortedPresetNames returns preset names in stable order (for diagnostics).
func SortedPresetNames(p map[string]string) []string {
	names := make([]string, 0, len(p))
	for n := range p {
		names = append(names, n)
	}
	sort.Strings(names)
	return names
}
