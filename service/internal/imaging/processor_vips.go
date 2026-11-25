//go:build vips

// Package imaging with the native libvips backend.
//
// Build with: go build -tags vips ./cmd/ioimg
// Requires libvips (pkg-config vips). This path is preferred in production:
// libvips' shrink-on-load and SIMD resize are 3-5x faster than pure Go.
package imaging

import (
	"fmt"
	"math"
	"runtime"
	"sync"
	"time"

	"ioimg/internal/ops"

	vips "github.com/davidbyttow/govips/v2/vips"
)

var vipsOnce sync.Once

func ensureVips() {
	vipsOnce.Do(func() {
		vips.LoggingSettings(nil, vips.LogLevelError)
		vips.Startup(&vips.Config{
			ConcurrencyLevel: runtime.NumCPU(),
			MaxCacheFiles:    0,
			MaxCacheMem:      0,
		})
	})
}

// DefaultProcessor returns the libvips pipeline.
func DefaultProcessor() Processor {
	ensureVips()
	return vipsProcessor{}
}

// AvailableFormats reports which encoders the linked libvips supports.
func AvailableFormats() map[ops.Format]bool {
	ensureVips()
	return map[ops.Format]bool{
		ops.FormatJPEG: vips.IsTypeSupported(vips.ImageTypeJPEG),
		ops.FormatPNG:  vips.IsTypeSupported(vips.ImageTypePNG),
		ops.FormatWebP: vips.IsTypeSupported(vips.ImageTypeWEBP),
		ops.FormatAVIF: vips.IsTypeSupported(vips.ImageTypeAVIF),
		ops.FormatGIF:  vips.IsTypeSupported(vips.ImageTypeGIF),
		ops.FormatJSON: true,
	}
}

type vipsProcessor struct{}

func (vipsProcessor) Process(src []byte, o ops.Ops, opts Options) (Result, error) {
	ensureVips()
	start := time.Now()
	img, err := vips.NewImageFromBuffer(src)
	if err != nil {
		return Result{}, ErrDecode
	}
	defer img.Close()
	if err := img.AutoRotate(); err != nil {
		return Result{}, ErrDecode
	}
	info := Info{
		Width:    img.Width(),
		Height:   img.Height(),
		Format:   imageTypeName(img.Format()),
		HasAlpha: img.HasAlpha(),
		Animated: img.Pages() > 1,
		Bytes:    len(src),
	}
	if info.Width > MaxSide || info.Height > MaxSide || info.Width*info.Height > MaxPixels {
		return Result{}, ErrDimension
	}
	timings := map[string]float64{"decode": elapsed(start)}
	res := Result{Info: info}

	if opts.OutFormat == ops.FormatJSON {
		res.DominantColor = dominantColor(img)
		return res, nil
	}

	start = time.Now()
	if err := apply(img, o); err != nil {
		return Result{}, err
	}
	timings["transform"] = elapsed(start)

	if opts.OutFormat == ops.FormatJPEG && img.HasAlpha() {
		bg := backgroundColor(o.Background)
		if bg.A == 0 {
			bg = &vips.ColorRGBA{R: 255, G: 255, B: 255, A: 255}
		}
		if err := img.Flatten(&vips.Color{R: bg.R, G: bg.G, B: bg.B}); err != nil {
			return Result{}, fmt.Errorf("flatten: %w", err)
		}
	}

	start = time.Now()
	out, err := encode(img, o, opts)
	if err != nil {
		return Result{}, err
	}
	timings["encode"] = elapsed(start)
	res.Data = out
	res.Timings = timings
	return res, nil
}

func apply(img *vips.ImageRef, o ops.Ops) error {
	if o.Trim > 0 {
		left, top, width, height, err := img.FindTrim(float64(o.Trim), nil)
		if err == nil && width > 0 && height > 0 {
			if err := img.Crop(left, top, width, height); err != nil {
				return err
			}
		}
	}
	switch o.Rotate {
	case 90:
		if err := img.Rotate(vips.Angle90); err != nil {
			return err
		}
	case 180:
		if err := img.Rotate(vips.Angle180); err != nil {
			return err
		}
	case 270:
		if err := img.Rotate(vips.Angle270); err != nil {
			return err
		}
	}
	if o.Flip != "" {
		if o.Flip == "h" || o.Flip == "hv" {
			if err := img.Flip(vips.DirectionHorizontal); err != nil {
				return err
			}
		}
		if o.Flip == "v" || o.Flip == "hv" {
			if err := img.Flip(vips.DirectionVertical); err != nil {
				return err
			}
		}
	}
	if o.Blur > 0 {
		sigma := o.Blur * float64(maxInt(img.Width(), img.Height())) / 500
		if sigma < 0.3 {
			sigma = 0.3
		}
		if err := img.GaussianBlur(sigma); err != nil {
			return err
		}
	}
	if o.Sharpen > 0 {
		if err := img.Sharpen(1, 1+o.Sharpen*0.2, 2); err != nil {
			return err
		}
	}
	if err := geometry(img, o); err != nil {
		return err
	}
	if o.Pixelate > 0 {
		if err := pixelateVips(img, o.Pixelate); err != nil {
			return err
		}
	}
	if o.Brightness != 0 || o.Contrast != 0 || o.Saturation != 0 || o.Monochrome {
		if err := adjustColorVips(img, o); err != nil {
			return err
		}
	}
	return nil
}

func pixelateVips(img *vips.ImageRef, block int) error {
	w, h := img.Width(), img.Height()
	block = maxInt(1, minInt(block, maxInt(w, h)))
	if block <= 1 {
		return nil
	}
	bw := maxInt(1, w/block)
	bh := maxInt(1, h/block)
	if err := img.ResizeWithVScale(float64(bw)/float64(w), float64(bh)/float64(h), vips.KernelNearest); err != nil {
		return err
	}
	return img.ResizeWithVScale(float64(w)/float64(bw), float64(h)/float64(bh), vips.KernelNearest)
}

func adjustColorVips(img *vips.ImageRef, o ops.Ops) error {
	if o.Brightness != 0 {
		if err := linearVips(img, 1, float64(o.Brightness)*2.55); err != nil {
			return err
		}
	}
	if o.Contrast != 0 {
		f := (100 + float64(o.Contrast)) / 100
		if err := linearVips(img, f, 128*(1-f)); err != nil {
			return err
		}
	}
	if o.Saturation != 0 || o.Monochrome {
		sat := (100 + float64(o.Saturation)) / 100
		if o.Monochrome {
			sat = 0
		}
		if err := img.Modulate(1, sat, 0); err != nil {
			return err
		}
	}
	return nil
}

// linearVips applies output = input*a + b without touching the alpha band.
func linearVips(img *vips.ImageRef, a, b float64) error {
	bands := img.Bands()
	if img.HasAlpha() && bands > 1 {
		aa := make([]float64, bands)
		bb := make([]float64, bands)
		for i := 0; i < bands-1; i++ {
			aa[i] = a
			bb[i] = b
		}
		aa[bands-1] = 1
		return img.Linear(aa, bb)
	}
	return img.Linear1(a, b)
}

func geometry(img *vips.ImageRef, o ops.Ops) error {
	sw, sh := img.Width(), img.Height()
	tw, th := o.EffectiveWidth(), o.EffectiveHeight()
	if o.WidthAuto {
		tw = int(math.Round(float64(th) * float64(sw) / float64(sh)))
	}
	if o.HeightAuto {
		th = int(math.Round(float64(tw) * float64(sh) / float64(sw)))
	}
	if tw <= 0 && th <= 0 {
		return nil
	}
	if tw > ops.MaxDimension || th > ops.MaxDimension {
		return ErrDimension
	}

	switch o.Resize {
	case ops.ResizeFit, ops.ResizeScale:
		allow := o.Enlarge || o.Resize == ops.ResizeScale
		nw, nh := fit(sw, sh, tw, th, allow)
		if nw == sw && nh == sh {
			return nil
		}
		return img.ResizeWithVScale(float64(nw)/float64(sw), float64(nh)/float64(sh), vips.KernelLanczos3)
	case ops.ResizeFill:
		if tw <= 0 || th <= 0 {
			nw, nh := fit(sw, sh, tw, th, o.Enlarge)
			return img.ResizeWithVScale(float64(nw)/float64(sw), float64(nh)/float64(sh), vips.KernelLanczos3)
		}
		// ThumbnailWithSize with SizeBoth scales to cover then crops using the
		// requested area of interest.
		return img.ThumbnailWithSize(tw, th, interesting(o.Gravity), vips.SizeBoth)
	case ops.ResizeForce:
		if tw <= 0 {
			tw = int(math.Round(float64(th) * float64(sw) / float64(sh)))
		}
		if th <= 0 {
			th = int(math.Round(float64(tw) * float64(sh) / float64(sw)))
		}
		if err := img.ResizeWithVScale(float64(tw)/float64(sw), float64(th)/float64(sh), vips.KernelLanczos3); err != nil {
			return err
		}
		return img.Crop(0, 0, tw, th)
	case ops.ResizePad:
		if tw <= 0 || th <= 0 {
			return fmt.Errorf("c_pad requires w and h")
		}
		nw, nh := fit(sw, sh, tw, th, o.Enlarge)
		if nw != sw || nh != sh {
			if err := img.ResizeWithVScale(float64(nw)/float64(sw), float64(nh)/float64(sh), vips.KernelLanczos3); err != nil {
				return err
			}
		}
		dx, dy := anchor(nw, nh, tw, th, o.Gravity)
		return img.EmbedBackgroundRGBA(dx, dy, tw, th, backgroundColor(o.Background))
	case ops.ResizeCrop:
		if tw <= 0 || th <= 0 {
			return fmt.Errorf("c_crop requires w and h")
		}
		tw = minInt(tw, sw)
		th = minInt(th, sh)
		dx, dy := anchor(tw, th, sw, sh, o.Gravity)
		return img.Crop(dx, dy, tw, th)
	}
	return nil
}

func encode(img *vips.ImageRef, o ops.Ops, opts Options) ([]byte, error) {
	quality := opts.Quality
	if quality == 0 {
		quality = autoQuality(opts.OutFormat, img.Width(), img.Height())
	}
	format := vips.ImageTypeJPEG
	switch opts.OutFormat {
	case ops.FormatPNG:
		format = vips.ImageTypePNG
	case ops.FormatWebP:
		format = vips.ImageTypeWEBP
	case ops.FormatAVIF:
		format = vips.ImageTypeAVIF
	case ops.FormatGIF:
		format = vips.ImageTypeGIF
	}
	if !vips.IsTypeSupported(format) {
		return nil, fmt.Errorf("unsupported_format: %s", opts.OutFormat)
	}
	out, _, err := img.Export(&vips.ExportParams{
		Format:        format,
		Quality:       quality,
		Compression:   6,
		Interlaced:    false,
		StripMetadata: true,
		Effort:        4,
		Speed:         8,
	})
	if err != nil {
		return nil, fmt.Errorf("encode %s: %w", opts.OutFormat, err)
	}
	return out, nil
}

func autoQuality(f ops.Format, w, h int) int {
	pixels := w * h
	switch f {
	case ops.FormatAVIF:
		if pixels > 1_000_000 {
			return 50
		}
		return 55
	case ops.FormatWebP:
		if pixels > 1_000_000 {
			return 72
		}
		return 78
	default:
		if pixels > 1_000_000 {
			return 76
		}
		return 82
	}
}

func fit(sw, sh, tw, th int, enlarge bool) (int, int) {
	scale := math.Inf(1)
	if tw > 0 {
		scale = math.Min(scale, float64(tw)/float64(sw))
	}
	if th > 0 {
		scale = math.Min(scale, float64(th)/float64(sh))
	}
	if math.IsInf(scale, 1) {
		return sw, sh
	}
	if !enlarge && scale > 1 {
		scale = 1
	}
	return clampInt(int(math.Round(float64(sw) * scale))), clampInt(int(math.Round(float64(sh) * scale)))
}

func clampInt(v int) int {
	if v < 1 {
		return 1
	}
	if v > ops.MaxDimension {
		return ops.MaxDimension
	}
	return v
}

func anchor(iw, ih, ow, oh int, g ops.Gravity) (int, int) {
	var x, y int
	switch g {
	case ops.GravityNorth, ops.GravityNorthEast, ops.GravityNorthWest:
		y = 0
	case ops.GravitySouth, ops.GravitySouthEast, ops.GravitySouthWest:
		y = oh - ih
	default:
		y = (oh - ih) / 2
	}
	switch g {
	case ops.GravityWest, ops.GravityNorthWest, ops.GravitySouthWest:
		x = 0
	case ops.GravityEast, ops.GravityNorthEast, ops.GravitySouthEast:
		x = ow - iw
	default:
		x = (ow - iw) / 2
	}
	return maxInt(0, x), maxInt(0, y)
}

func interesting(g ops.Gravity) vips.Interesting {
	switch g {
	case ops.GravityAuto, ops.GravityFace:
		return vips.InterestingAttention
	case ops.GravityNorth, ops.GravityNorthEast, ops.GravityNorthWest:
		return vips.InterestingHigh
	case ops.GravitySouth, ops.GravitySouthEast, ops.GravitySouthWest:
		return vips.InterestingLow
	default:
		return vips.InterestingCentre
	}
}

func backgroundColor(raw string) *vips.ColorRGBA {
	if raw == "" || raw == "transparent" {
		return &vips.ColorRGBA{}
	}
	if len(raw) != 6 {
		return &vips.ColorRGBA{}
	}
	return &vips.ColorRGBA{
		R: hexByte(raw[0:2]),
		G: hexByte(raw[2:4]),
		B: hexByte(raw[4:6]),
		A: 255,
	}
}

func hexByte(s string) uint8 {
	var v uint8
	for _, c := range s {
		v <<= 4
		switch {
		case c >= '0' && c <= '9':
			v |= uint8(c - '0')
		case c >= 'a' && c <= 'f':
			v |= uint8(c-'a') + 10
		case c >= 'A' && c <= 'F':
			v |= uint8(c-'A') + 10
		}
	}
	return v
}

func dominantColor(img *vips.ImageRef) string {
	w, h := img.Width(), img.Height()
	if w == 0 || h == 0 {
		return "#000000"
	}
	step := maxInt(1, minInt(w, h)/32)
	var r, g, b, n float64
	for y := 0; y < h; y += step {
		for x := 0; x < w; x += step {
			px, err := img.GetPoint(x, y)
			if err != nil || len(px) < 3 {
				continue
			}
			r += px[0]
			g += px[1]
			b += px[2]
			n++
		}
	}
	if n == 0 {
		return "#000000"
	}
	to := func(f float64) uint8 {
		f = f / n
		if f < 0 {
			f = 0
		}
		if f > 255 {
			f = 255
		}
		return uint8(math.Round(f))
	}
	return fmt.Sprintf("#%02x%02x%02x", to(r), to(g), to(b))
}

func imageTypeName(t vips.ImageType) string {
	if name, ok := vips.ImageTypes[t]; ok {
		return name
	}
	return "unknown"
}

func elapsed(start time.Time) float64 {
	return float64(time.Since(start).Microseconds()) / 1000
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}
