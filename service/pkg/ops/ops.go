// Package ops parses and canonicalises ioimg URL transformation parameters.
//
// A parameter list is a comma separated sequence of name_value pairs, for
// example: w_800,h_600,c_fill,g_auto,f_auto,q_auto. Order never matters: the
// canonical form is what signatures and cache keys are built from.
package ops

import (
	"encoding/base64"
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

// Algorithm is the resampling kernel (ra_*). The default matches imgproxy.
type Algorithm string

const (
	AlgoLanczos3 Algorithm = "lanczos3"
	AlgoLanczos2 Algorithm = "lanczos2"
	AlgoCubic    Algorithm = "cubic"
	AlgoLinear   Algorithm = "linear"
	AlgoNearest  Algorithm = "nearest"
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
	// Background is the padding/flatten colour. It is stored in canonical
	// form: "transparent", RRGGBB, or RRGGBBAA when the alpha is not 255.
	// Empty means "not set" (transparent for padding, white for flattening).
	Background string
	// BackgroundAlpha is multiplied into the background alpha (imgproxy bga_).
	// Default() keeps the historical opaque background at 1.
	BackgroundAlpha float64
	backgroundSet   bool // bga_ was explicitly present (bga_0 is meaningful)
	// Extend pads a smaller result up to the requested w/h canvas (imgproxy ex_)
	// using Background. ExtendGravity anchors the image in that canvas.
	Extend        bool
	ExtendGravity Gravity
	// Flatten composites the result onto Background regardless of output format
	// (imgproxy fl_). The default background is white.
	Flatten    bool
	Blur       float64
	Sharpen    float64
	Brightness int    // -100..100
	Contrast   int    // -100..100
	Saturation int    // -100..100
	Pixelate   int    // block size in pixels; 0 = off
	Monochrome bool   // grayscale filter
	Rotate     int    // 0, 90, 180, 270
	Flip       string // "", "h", "v", "hv"
	Trim       int
	Strip      *bool // nil = default (true)
	Enlarge    bool
	Palette    bool
	Preset     string
	// Algorithm selects the resampling kernel. Empty means lanczos3 and keeps
	// the canonical form (and therefore the cache key) of existing URLs.
	Algorithm Algorithm
	// MaxBytes bounds the encoded response. Lossy formats re-encode at a lower
	// quality until the result fits; zero means "no budget".
	MaxBytes int
	// Filename only affects Content-Disposition: it never changes pixels, so it
	// is signed but excluded from the cache key.
	Filename string
	// Cachebuster is a CDN bypass token: signed, never part of the cache key.
	Cachebuster string
	// Duotone and Colorize are per-pixel colour effects. Both are imgproxy Pro
	// features; they never change geometry, only colour.
	Duotone  Duotone
	Colorize Colorize
	// Gradient overlays a colour ramp (imgproxy Pro). Zero opacity = off.
	Gradient Gradient
	// Watermark composites a watermark image over the result (imgproxy wm_).
	// Nil or Opacity 0 means no watermark.
	Watermark *Watermark
	// WatermarkURL is the per-request watermark image (imgproxy wmu_).
	WatermarkURL string
	// DPI sets the density metadata of the encoded image (imgproxy dpi_). It
	// never changes pixels, so it is part of the cache key only because the
	// encoded bytes differ.
	DPI float64
	// Hashsum verifies the source digest before any decoding (imgproxy hs_).
	Hashsum Hashsum
	// FallbackURL is served (processed with the same ops) when the source cannot
	// be fetched or decoded (imgproxy fiu_, URL-safe base64).
	FallbackURL string
	// Per-format encoder overrides (imgproxy Pro). Nil = encoder default, so
	// the canonical form of URLs that do not use them is unchanged.
	PNGEncoder  *PNGEncoder
	WebPEncoder *WebPEncoder
	AVIFEncoder *AVIFEncoder
	// MinWidth and MinHeight bound the resulting size from below (imgproxy
	// mw/mh): a smaller result is enlarged, preserving the aspect ratio. Zero
	// means "no minimum".
	MinWidth  int
	MinHeight int
	// AutoRotate applies the EXIF orientation to the pixels. Nil means the
	// default (true), so a plain Ops{} keeps the historical behaviour.
	AutoRotate *bool
	// SkipProcessing lists source formats for which processing is skipped when
	// the requested output format equals the source format (imgproxy skp_).
	SkipProcessing []string
	// Raw streams the source bytes untouched, without decode or limits
	// (imgproxy raw_).
	Raw bool
}

// AutoRotates reports whether EXIF orientation should be baked into pixels.
func (o Ops) AutoRotates() bool { return o.AutoRotate == nil || *o.AutoRotate }

// BackgroundAlphaMultiplier returns the bga_ multiplier. A zero Ops{} keeps the
// historical opaque background; an explicit bga_0 still returns 0.
func (o Ops) BackgroundAlphaMultiplier() float64 {
	if o.BackgroundAlpha == 0 && !o.backgroundSet {
		return 1
	}
	return o.BackgroundAlpha
}

// Default returns the implicit defaults from docs/API.md.
func Default() Ops {
	return Ops{
		Resize:          ResizeFit,
		Gravity:         GravityCenter,
		Format:          FormatAuto,
		DPR:             1,
		BackgroundAlpha: 1,
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

// bareOptions are the options whose arguments are all optional.
var bareOptions = map[string]bool{"wm": true, "watermark": true}

func isBareOption(s string) bool { return bareOptions[strings.ToLower(s)] }

func isSkipOption(s string) bool {
	k := strings.ToLower(s)
	return k == "skp" || k == "skip_processing"
}

func splitPairs(raw string) ([]pair, error) {
	parts := strings.Split(raw, ",")
	out := make([]pair, 0, len(parts))
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		// imgproxy allows a few options with every argument optional, so the bare
		// name is a valid way to request their defaults.
		if isBareOption(part) {
			out = append(out, pair{key: part, value: ""})
			continue
		}
		// Some option names contain an underscore themselves, so they cannot go
		// through the generic first-underscore split.
		lower := strings.ToLower(part)
		if strings.HasPrefix(lower, "skip_processing_") {
			out = append(out, pair{key: "skip_processing", value: part[len("skip_processing_"):]})
			continue
		}
		if strings.HasPrefix(lower, "background_alpha_") {
			out = append(out, pair{key: "background_alpha", value: part[len("background_alpha_"):]})
			continue
		}
		k, v, ok := strings.Cut(part, "_")
		if !ok {
			// imgproxy writes skp_ extensions separated by colons, but a comma is
			// also accepted here for symmetry with the ops list. A bare extension
			// immediately after skp_ is therefore a continuation of that value.
			if len(out) > 0 && isSkipOption(out[len(out)-1].key) && isSkipExtension(part) {
				out[len(out)-1].value += ":" + part
				continue
			}
		}
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
		g, err := parseGravity(value)
		if err != nil {
			return fmt.Errorf("g: %w", err)
		}
		o.Gravity = g
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
	case "bg", "background":
		bg, err := parseBackground(value)
		if err != nil {
			return fmt.Errorf("bg: %w", err)
		}
		o.Background = bg
	case "bga", "background_alpha":
		f, err := strconv.ParseFloat(value, 64)
		if err != nil || f < 0 || f > 1 {
			return fmt.Errorf("bga must be a number between 0 and 1")
		}
		o.BackgroundAlpha = f
		o.backgroundSet = true
	case "ex", "extend":
		parts := strings.Split(value, ":")
		if len(parts) > 2 {
			return fmt.Errorf("ex takes extend:gravity")
		}
		on, err := parseBool(parts[0])
		if err != nil {
			return fmt.Errorf("ex must be 1 or 0")
		}
		o.Extend = on
		o.ExtendGravity = GravityCenter
		if len(parts) == 2 && parts[1] != "" {
			g, err := parseGravity(parts[1])
			if err != nil {
				return fmt.Errorf("ex: %w", err)
			}
			if g == GravityAuto || g == GravityFace {
				return fmt.Errorf("ex gravity must not be auto or face")
			}
			o.ExtendGravity = g
		}
	case "fl", "flatten":
		on, err := parseBool(value)
		if err != nil {
			return fmt.Errorf("fl must be 1 or 0")
		}
		o.Flatten = on
	case "blur":
		f, err := strconv.ParseFloat(value, 64)
		if err != nil || f < 0 || f > 100 {
			return fmt.Errorf("blur must be 0..100")
		}
		o.Blur = f
	case "bri", "brightness":
		n, err := parseSigned(value)
		if err != nil {
			return fmt.Errorf("bri must be -100..100")
		}
		o.Brightness = n
	case "con", "contrast":
		n, err := parseSigned(value)
		if err != nil {
			return fmt.Errorf("con must be -100..100")
		}
		o.Contrast = n
	case "sat", "saturation":
		n, err := parseSigned(value)
		if err != nil {
			return fmt.Errorf("sat must be -100..100")
		}
		o.Saturation = n
	case "pixel", "px":
		n, err := strconv.Atoi(value)
		if err != nil || n < 1 || n > 200 {
			return fmt.Errorf("pixel must be 1..200")
		}
		o.Pixelate = n
	case "mono", "monochrome":
		switch value {
		case "0":
			o.Monochrome = false
		case "1":
			o.Monochrome = true
		default:
			return fmt.Errorf("mono must be 0 or 1")
		}
	case "sharp", "sharpen":
		f, err := strconv.ParseFloat(value, 64)
		if err != nil || f < 0 || f > 10 {
			return fmt.Errorf("sharp must be 0..10")
		}
		o.Sharpen = f
	case "dt", "duotone":
		d, err := parseDuotone(value)
		if err != nil {
			return err
		}
		o.Duotone = d
	case "gr":
		g, err := parseGradient(value)
		if err != nil {
			return err
		}
		o.Gradient = g
	case "pngo":
		p, err := parsePNGEncoder(value)
		if err != nil {
			return err
		}
		o.PNGEncoder = &p
	case "webpo":
		w, err := parseWebPEncoder(value)
		if err != nil {
			return err
		}
		o.WebPEncoder = &w
	case "avifo":
		a, err := parseAVIFEncoder(value)
		if err != nil {
			return err
		}
		o.AVIFEncoder = &a
	case "wm", "watermark":
		w, err := parseWatermark(value)
		if err != nil {
			return err
		}
		if w.Opacity > 0 {
			o.Watermark = &w
		}
	case "wmu", "watermark_url":
		u, err := DecodeWatermarkURL(value)
		if err != nil {
			return err
		}
		o.WatermarkURL = u
	case "dpi":
		f, err := strconv.ParseFloat(value, 64)
		if err != nil || f < 0 || f > 100000 {
			return fmt.Errorf("dpi must be a number between 0 and 100000")
		}
		o.DPI = f
	case "hs", "hashsum":
		h, err := parseHashsum(value)
		if err != nil {
			return err
		}
		o.Hashsum = h
	case "fiu", "fallback_image_url":
		u, err := DecodeFallbackURL(value)
		if err != nil {
			return err
		}
		o.FallbackURL = u
	case "col", "colorize":
		c, err := parseColorize(value)
		if err != nil {
			return err
		}
		o.Colorize = c
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
	case "strip", "sm":
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
	case "mw":
		n, err := strconv.Atoi(value)
		if err != nil || n < 1 || n > MaxDimension {
			return fmt.Errorf("mw must be an integer between 1 and %d", MaxDimension)
		}
		o.MinWidth = n
	case "mh":
		n, err := strconv.Atoi(value)
		if err != nil || n < 1 || n > MaxDimension {
			return fmt.Errorf("mh must be an integer between 1 and %d", MaxDimension)
		}
		o.MinHeight = n
	case "ar":
		switch strings.ToLower(value) {
		case "0", "f", "false":
			b := false
			o.AutoRotate = &b
		case "1", "t", "true":
			b := true
			o.AutoRotate = &b
		default:
			return fmt.Errorf("ar must be 0 or 1")
		}
	case "skp", "skip_processing":
		exts, err := parseSkipProcessing(value)
		if err != nil {
			return err
		}
		o.SkipProcessing = exts
	case "raw":
		b, err := parseBool(value)
		if err != nil {
			return fmt.Errorf("raw must be 1, t, true, 0, f or false")
		}
		o.Raw = b
	case "ra":
		switch Algorithm(value) {
		case AlgoLanczos3, AlgoLanczos2, AlgoCubic, AlgoLinear, AlgoNearest:
			o.Algorithm = Algorithm(value)
		default:
			return fmt.Errorf("ra: unknown algorithm %q", value)
		}
	case "mb":
		n, err := strconv.Atoi(value)
		if err != nil || n < 1024 || n > 8<<20 {
			return fmt.Errorf("mb must be 1024..8388608")
		}
		o.MaxBytes = n
	case "fn":
		clean, err := sanitizeFilename(value)
		if err != nil {
			return err
		}
		o.Filename = clean
	case "cb":
		if len(value) > 64 {
			return fmt.Errorf("cb must be at most 64 characters")
		}
		for _, c := range value {
			if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '-' || c == '_') {
				return fmt.Errorf("cb must be alphanumeric, dash or underscore")
			}
		}
		o.Cachebuster = value
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
	if o.Width == 0 && o.Height == 0 && !o.WidthAuto && !o.HeightAuto &&
		o.MinWidth == 0 && o.MinHeight == 0 && !o.Raw && len(o.SkipProcessing) == 0 {
		if o.Format == FormatAuto {
			return fmt.Errorf("at least one of w, h, mw, mh or f is required")
		}
	}
	if o.Extend && o.Width == 0 && o.Height == 0 && !o.WidthAuto && !o.HeightAuto {
		return fmt.Errorf("extend requires w or h")
	}
	if o.Extend && (o.ExtendGravity == GravityAuto || o.ExtendGravity == GravityFace) {
		return fmt.Errorf("extend gravity must not be auto or face")
	}
	if o.Rotate != 0 && o.Flip != "" {
		return fmt.Errorf("rot and flip cannot be combined")
	}
	if o.MaxBytes > 0 && (o.Format == FormatPNG || o.Format == FormatGIF || o.Format == FormatJSON) {
		return fmt.Errorf("mb requires a lossy format (jpeg, webp, avif or auto)")
	}
	return nil
}

// sanitizeFilename keeps a Content-Disposition filename inert: no path
// separators, quotes, control characters or backslashes.
func sanitizeFilename(v string) (string, error) {
	if v == "" || len(v) > 120 {
		return "", fmt.Errorf("fn must be 1..120 characters")
	}
	for _, c := range v {
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9':
		case c == '.' || c == '-' || c == '_':
		default:
			return "", fmt.Errorf("fn may only contain letters, digits, dot, dash and underscore")
		}
	}
	if strings.HasPrefix(v, ".") || strings.Contains(v, "..") {
		return "", fmt.Errorf("fn must not start with a dot or contain ..")
	}
	return v, nil
}

func parseBool(v string) (bool, error) {
	switch strings.ToLower(v) {
	case "1", "t", "true":
		return true, nil
	case "0", "f", "false":
		return false, nil
	}
	return false, fmt.Errorf("must be 1, t, true, 0, f or false")
}

var skipExtensions = map[string]bool{
	"jpg": true, "jpeg": true, "png": true, "webp": true, "avif": true, "gif": true,
}

func isSkipExtension(s string) bool { return skipExtensions[strings.ToLower(s)] }

// parseSkipProcessing accepts a colon- or comma-separated extension list and
// returns it lowercased, deduplicated and sorted so the canonical form is
// stable regardless of argument order.
func parseSkipProcessing(v string) ([]string, error) {
	parts := strings.FieldsFunc(v, func(r rune) bool { return r == ':' || r == ',' })
	seen := make(map[string]bool, len(parts))
	out := make([]string, 0, len(parts))
	for _, part := range parts {
		ext := strings.ToLower(strings.TrimSpace(part))
		if ext == "" {
			continue
		}
		if !skipExtensions[ext] {
			return nil, fmt.Errorf("skp: unknown extension %q (want jpg, jpeg, png, webp, avif or gif)", part)
		}
		if !seen[ext] {
			seen[ext] = true
			out = append(out, ext)
		}
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("skp requires at least one extension")
	}
	sort.Strings(out)
	return out, nil
}

func parseSigned(v string) (int, error) {
	n, err := strconv.Atoi(v)
	if err != nil || n < -100 || n > 100 {
		return 0, fmt.Errorf("out of range")
	}
	return n, nil
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

// parseBackground normalises the accepted background forms to the canonical
// string used in cache keys: "transparent", RRGGBB, or RRGGBBAA. It accepts
// imgproxy's 6/8-digit hex and the decimal R:G:B (optionally :A) form.
func parseBackground(v string) (string, error) {
	v = strings.TrimSpace(v)
	if strings.EqualFold(v, "transparent") {
		return "transparent", nil
	}
	var r, g, b, a uint8 = 0, 0, 0, 255
	if strings.Contains(v, ":") {
		parts := strings.Split(v, ":")
		if len(parts) != 3 && len(parts) != 4 {
			return "", fmt.Errorf("must be RRGGBB, RRGGBBAA, R:G:B, R:G:B:A or transparent")
		}
		vals := [4]uint8{r, g, b, a}
		for i, part := range parts {
			n, err := strconv.Atoi(part)
			if err != nil || n < 0 || n > 255 {
				return "", fmt.Errorf("component %d must be 0..255", i+1)
			}
			vals[i] = uint8(n)
		}
		r, g, b, a = vals[0], vals[1], vals[2], vals[3]
	} else {
		v = strings.TrimPrefix(v, "#")
		if (len(v) != 6 && len(v) != 8) || !isHex(v) {
			return "", fmt.Errorf("must be RRGGBB, RRGGBBAA, R:G:B or transparent")
		}
		v = strings.ToLower(v)
		n, _ := strconv.ParseUint(v, 16, 32)
		if len(v) == 8 {
			r = uint8(n >> 24)
			g = uint8(n >> 16)
			b = uint8(n >> 8)
			a = uint8(n)
		} else {
			r = uint8(n >> 16)
			g = uint8(n >> 8)
			b = uint8(n)
		}
	}
	hex := fmt.Sprintf("%02x%02x%02x", r, g, b)
	if a != 255 {
		hex += fmt.Sprintf("%02x", a)
	}
	return hex, nil
}

// parseGravity accepts both ioimg's full names and imgproxy's short aliases.
func parseGravity(v string) (Gravity, error) {
	switch strings.ToLower(v) {
	case "center", "ce":
		return GravityCenter, nil
	case "north", "no":
		return GravityNorth, nil
	case "south", "so":
		return GravitySouth, nil
	case "east", "ea":
		return GravityEast, nil
	case "west", "we":
		return GravityWest, nil
	case "north_east", "northeast", "noea":
		return GravityNorthEast, nil
	case "north_west", "northwest", "nowe":
		return GravityNorthWest, nil
	case "south_east", "southeast", "soea":
		return GravitySouthEast, nil
	case "south_west", "southwest", "sowe":
		return GravitySouthWest, nil
	case "auto":
		return GravityAuto, nil
	case "face":
		return GravityFace, nil
	}
	return "", fmt.Errorf("unknown gravity %q", v)
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
	if o.BackgroundAlpha != 1 && (o.backgroundSet || o.BackgroundAlpha != 0) {
		add("bga", trimFloat(o.BackgroundAlpha))
	}
	if o.Blur != 0 {
		add("blur", trimFloat(o.Blur))
	}
	if o.Brightness != 0 {
		add("bri", strconv.Itoa(o.Brightness))
	}
	add("c", string(o.Resize))
	if o.Algorithm != "" && o.Algorithm != AlgoLanczos3 {
		add("ra", string(o.Algorithm))
	}
	if o.MinWidth > 0 {
		add("mw", strconv.Itoa(o.MinWidth))
	}
	if o.MinHeight > 0 {
		add("mh", strconv.Itoa(o.MinHeight))
	}
	if o.AutoRotate != nil && !*o.AutoRotate {
		add("ar", "0")
	}
	if o.MaxBytes != 0 {
		add("mb", strconv.Itoa(o.MaxBytes))
	}
	if o.Colorize.Opacity > 0 {
		add("col", o.Colorize.canonical())
	}
	if o.AVIFEncoder != nil {
		add("avifo", o.AVIFEncoder.canonical())
	}
	if o.Contrast != 0 {
		add("con", strconv.Itoa(o.Contrast))
	}
	if o.Watermark != nil {
		add("wm", o.Watermark.canonical())
	}
	if o.WatermarkURL != "" {
		add("wmu", base64.RawURLEncoding.EncodeToString([]byte(o.WatermarkURL)))
	}
	if o.DPI > 0 {
		add("dpi", trimFloat(o.DPI))
	}
	if o.DPR != 1 {
		add("dpr", trimFloat(o.DPR))
	}
	if o.Duotone.Intensity > 0 {
		add("dt", o.Duotone.canonical())
	}
	if o.Enlarge {
		add("enlarge", "1")
	}
	if o.Extend {
		g := o.ExtendGravity
		if g == "" {
			g = GravityCenter
		}
		add("ex", "1:"+string(g))
	}
	add("f", string(o.Format))
	if o.Flatten {
		add("fl", "1")
	}
	if o.Flip != "" {
		add("flip", o.Flip)
	}
	add("g", string(o.Gravity))
	if !o.Gradient.IsZero() {
		add("gr", o.Gradient.canonical())
	}
	if !o.Hashsum.IsZero() {
		add("hs", o.Hashsum.canonical())
	}
	if o.FallbackURL != "" {
		add("fiu", base64.RawURLEncoding.EncodeToString([]byte(o.FallbackURL)))
	}
	if o.HeightAuto {
		add("h", "auto")
	} else if o.Height != 0 {
		add("h", strconv.Itoa(o.Height))
	}
	if o.Monochrome {
		add("mono", "1")
	}
	if o.Palette {
		add("palette", "1")
	}
	if o.PNGEncoder != nil {
		add("pngo", o.PNGEncoder.canonical())
	}
	if o.Pixelate != 0 {
		add("pixel", strconv.Itoa(o.Pixelate))
	}
	if o.Quality != 0 {
		add("q", strconv.Itoa(o.Quality))
	}
	if o.Rotate != 0 {
		add("rot", strconv.Itoa(o.Rotate))
	}
	if o.Saturation != 0 {
		add("sat", strconv.Itoa(o.Saturation))
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
	if o.WebPEncoder != nil {
		add("webpo", o.WebPEncoder.canonical())
	}
	if len(o.SkipProcessing) > 0 {
		add("skp", strings.Join(o.SkipProcessing, ":"))
	}
	if o.Raw {
		add("raw", "1")
	}
	return b.String()
}

// SigningCanonical is the canonical form plus the presentation-only parameters.
// Filename and cachebuster must be signed (they change what the client sees and
// can be abused to bypass a CDN) but must not split the cache: two URLs that
// differ only in cb_ or fn_ share one cache entry.
func (o Ops) SigningCanonical() string {
	base := o.Canonical()
	var extra strings.Builder
	if o.Filename != "" {
		extra.WriteString(",fn_")
		extra.WriteString(o.Filename)
	}
	if o.Cachebuster != "" {
		extra.WriteString(",cb_")
		extra.WriteString(o.Cachebuster)
	}
	if extra.Len() == 0 {
		return base
	}
	return base + extra.String()
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

// -----------------------------------------------------------------------------
// Colour effects (imgproxy Pro: duotone, colorize)
// -----------------------------------------------------------------------------

// RGB is a 24-bit colour used by the colour effects.
type RGB struct{ R, G, B uint8 }

func (c RGB) String() string {
	return strconv.FormatUint(uint64(c.R)<<16|uint64(c.G)<<8|uint64(c.B), 16)
}

// Hex returns the colour as six lowercase hex digits, the canonical form used
// in cache keys.
func (c RGB) Hex() string {
	const digits = "0123456789abcdef"
	return string([]byte{
		digits[c.R>>4], digits[c.R&0xf],
		digits[c.G>>4], digits[c.G&0xf],
		digits[c.B>>4], digits[c.B&0xf],
	})
}

// Duotone maps luminance onto a two-colour palette. Intensity 0 disables the
// effect; Color1 paints the dark areas, Color2 the light ones.
type Duotone struct {
	Intensity      float64
	Color1, Color2 RGB
}

func (d Duotone) canonical() string {
	return trimFloat(d.Intensity) + ":" + d.Color1.Hex() + ":" + d.Color2.Hex()
}

// Colorize paints a solid colour over the image. KeepAlpha preserves the
// source alpha channel; otherwise the result is opaque.
type Colorize struct {
	Opacity   float64
	Color     RGB
	KeepAlpha bool
}

func (c Colorize) canonical() string {
	keep := "0"
	if c.KeepAlpha {
		keep = "1"
	}
	return trimFloat(c.Opacity) + ":" + c.Color.Hex() + ":" + keep
}

// parseDuotone accepts %intensity:%color1:%color2 with every part optional,
// mirroring imgproxy's defaults (0:000000:ffffff).
func parseDuotone(v string) (Duotone, error) {
	d := Duotone{Color2: RGB{0xff, 0xff, 0xff}}
	parts := strings.Split(v, ":")
	if len(parts) > 3 {
		return d, fmt.Errorf("dt takes intensity:color1:color2")
	}
	if parts[0] != "" {
		f, err := strconv.ParseFloat(parts[0], 64)
		if err != nil || f < 0 || f > 1 {
			return d, fmt.Errorf("dt intensity must be 0..1")
		}
		d.Intensity = f
	}
	if len(parts) > 1 && parts[1] != "" {
		c, err := parseRGB(parts[1])
		if err != nil {
			return d, fmt.Errorf("dt color1: %w", err)
		}
		d.Color1 = c
	}
	if len(parts) > 2 && parts[2] != "" {
		c, err := parseRGB(parts[2])
		if err != nil {
			return d, fmt.Errorf("dt color2: %w", err)
		}
		d.Color2 = c
	}
	return d, nil
}

// parseColorize accepts %opacity:%color:%keep_alpha with imgproxy's defaults
// (1:000:0). Opacity 0 leaves the image untouched.
func parseColorize(v string) (Colorize, error) {
	c := Colorize{Opacity: 1}
	parts := strings.Split(v, ":")
	if len(parts) > 3 {
		return c, fmt.Errorf("col takes opacity:color:keep_alpha")
	}
	if parts[0] != "" {
		f, err := strconv.ParseFloat(parts[0], 64)
		if err != nil || f < 0 || f > 1 {
			return c, fmt.Errorf("col opacity must be 0..1")
		}
		c.Opacity = f
	}
	if len(parts) > 1 && parts[1] != "" {
		rgb, err := parseRGB(parts[1])
		if err != nil {
			return c, fmt.Errorf("col color: %w", err)
		}
		c.Color = rgb
	}
	if len(parts) > 2 && parts[2] != "" {
		switch parts[2] {
		case "1", "t", "true":
			c.KeepAlpha = true
		case "0", "f", "false":
			c.KeepAlpha = false
		default:
			return c, fmt.Errorf("col keep_alpha must be 0 or 1")
		}
	}
	return c, nil
}

// parseRGB accepts 3- or 6-digit hex with an optional leading '#'.
func parseRGB(v string) (RGB, error) {
	v = strings.TrimPrefix(strings.ToLower(strings.TrimSpace(v)), "#")
	switch len(v) {
	case 3:
		v = string([]byte{v[0], v[0], v[1], v[1], v[2], v[2]})
	case 6:
	default:
		return RGB{}, fmt.Errorf("colour must be 3 or 6 hex digits")
	}
	if strings.ContainsFunc(v, func(r rune) bool {
		return !(r >= '0' && r <= '9' || r >= 'a' && r <= 'f')
	}) {
		return RGB{}, fmt.Errorf("colour must be 3 or 6 hex digits")
	}
	n, err := strconv.ParseUint(v, 16, 32)
	if err != nil {
		return RGB{}, fmt.Errorf("colour must be 3 or 6 hex digits")
	}
	return RGB{R: uint8(n >> 16), G: uint8(n >> 8), B: uint8(n)}, nil
}
