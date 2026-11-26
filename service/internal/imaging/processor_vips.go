//go:build vips

// Package imaging with the native libvips backend.
//
// Build with: go build -tags vips ./cmd/ioimg
// Requires libvips (pkg-config vips). This path is preferred in production:
// libvips' shrink-on-load and SIMD resize are 3-5x faster than pure Go.
package imaging

import (
	"fmt"
	"image"
	"math"
	"os"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/ioimg/ioimg/service/pkg/ops"

	vips "github.com/davidbyttow/govips/v2/vips"
)

var vipsOnce sync.Once

func ensureVips() {
	vipsOnce.Do(func() {
		vips.LoggingSettings(nil, vips.LogLevelError)
		vips.Startup(&vips.Config{
			ConcurrencyLevel: vipsConcurrency(),
			MaxCacheFiles:    0,
			MaxCacheMem:      0,
		})
	})
}

// vipsConcurrency returns libvips' per-image thread count. The HTTP layer
// already runs transforms concurrently (one goroutine per in-flight request),
// so letting libvips spread a single image across every core oversubscribes the
// machine: at 8 concurrent MISS requests, ConcurrencyLevel=NumCPU (16) served
// 120 img/s while ConcurrencyLevel=1 served 169 img/s. Default 1, overridable
// with IOIMG_VIPS_CONCURRENCY for single-request (batch/warm) workloads.
func vipsConcurrency() int {
	n := 1
	if raw := os.Getenv("IOIMG_VIPS_CONCURRENCY"); raw != "" {
		if v, err := strconv.Atoi(raw); err == nil && v > 0 {
			n = v
		}
	}
	if n > runtime.NumCPU() {
		n = runtime.NumCPU()
	}
	return n
}

// Backend reports the active imaging pipeline, so startup can warn when the
// pure-Go fallback is linked in by mistake (it is ~9x slower per transform).
func Backend() string { return "vips" }

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
	if plan, ok := thumbnailPlan(src, o, opts); ok {
		return processThumbnail(src, o, opts, plan)
	}
	start := time.Now()
	img, err := vips.NewImageFromBuffer(src)
	if err != nil {
		return Result{}, ErrDecode
	}
	defer img.Close()
	if o.AutoRotates() {
		if err := img.AutoRotate(); err != nil {
			return Result{}, ErrDecode
		}
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
	img, err = apply(img, o)
	if err != nil {
		return Result{}, err
	}
	img, err = applyWatermarkVips(img, o, opts)
	if err != nil {
		return Result{}, err
	}
	if err := enforceMinimumVips(img, o); err != nil {
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

// processThumbnail runs the common case — a pure resize — through
// vips_thumbnail_buffer, which lets libvips decode at the requested scale
// (JPEG DCT scaling, PNG/WebP reduced-size decode) instead of decoding full
// resolution and downscaling afterwards. Measured on a 2400x1600 JPEG:
// 9.6 ms/img vs 21.7 ms/img for the decode-then-resize path.
func processThumbnail(src []byte, o ops.Ops, opts Options, plan thumbnailOp) (Result, error) {
	// vips_thumbnail decodes at the requested scale, so the shrink-on-load path
	// would otherwise never see the source dimensions: a 60000px-wide source
	// asked for w_8000 would allocate 8000x8000 before any guard ran. Probe the
	// header (lazy, no pixel data) and apply the documented limits first.
	if w, h, err := probeDimensions(src); err != nil {
		return Result{}, ErrDecode
	} else if w > MaxSide || h > MaxSide || w*h > MaxPixels {
		return Result{}, ErrDimension
	}

	start := time.Now()
	img, err := vips.LoadThumbnailFromBuffer(src, plan.width, plan.height, plan.crop, plan.size, nil)
	if err != nil {
		return Result{}, ErrDecode
	}
	// A closure, not defer img.Close(): applyColorEffectsVips replaces img.
	defer func() { img.Close() }()
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
	if err := enforceMinimumVips(img, o); err != nil {
		return Result{}, err
	}
	img, err = applyColorEffectsVips(img, o)
	if err != nil {
		return Result{}, err
	}
	img, err = applyWatermarkVips(img, o, opts)
	if err != nil {
		return Result{}, err
	}
	info.Width, info.Height = img.Width(), img.Height()
	res := Result{Info: info}
	timings := map[string]float64{"transform": elapsed(start)}

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

// enforceMinimumVips applies mw_/mh_ by scaling a too-small result up, keeping
// the aspect ratio.
func enforceMinimumVips(img *vips.ImageRef, o ops.Ops) error {
	if o.MinWidth <= 0 && o.MinHeight <= 0 {
		return nil
	}
	w, h := img.Width(), img.Height()
	if w <= 0 || h <= 0 {
		return nil
	}
	f := 1.0
	if o.MinWidth > 0 && w < o.MinWidth {
		f = math.Max(f, float64(o.MinWidth)/float64(w))
	}
	if o.MinHeight > 0 && h < o.MinHeight {
		f = math.Max(f, float64(o.MinHeight)/float64(h))
	}
	if f <= 1 {
		return nil
	}
	if math.Round(float64(w)*f) > ops.MaxDimension || math.Round(float64(h)*f) > ops.MaxDimension {
		f = math.Min(float64(ops.MaxDimension)/float64(w), float64(ops.MaxDimension)/float64(h))
	}
	if f <= 1 {
		return nil
	}
	return img.ResizeWithVScale(f, f, kernelFor(o.Algorithm))
}

// probeDimensions reads only the header: NewImageFromBuffer is lazy, so this
// costs one header parse and no pixel decode.
func probeDimensions(src []byte) (int, int, error) {
	img, err := vips.NewImageFromBuffer(src)
	if err != nil {
		return 0, 0, err
	}
	defer img.Close()
	return img.Width(), img.Height(), nil
}

// thumbnailOp is a resize expressible as a single vips_thumbnail_buffer call.
type thumbnailOp struct {
	width  int
	height int
	crop   vips.Interesting
	size   vips.Size
}

// thumbnailPlan decides whether a request is a pure resize that shrink-on-load
// can serve exactly. Everything with trim/rotate/flip/blur/sharpen/pixelate/
// colour work, padding, or cropping-from-source keeps the full-decode path, as
// does animated input and JSON metadata.
//
// IOIMG_VIPS_THUMBNAIL=0 disables the fast path (used by the parity test).
func thumbnailPlan(src []byte, o ops.Ops, opts Options) (thumbnailOp, bool) {
	if os.Getenv("IOIMG_VIPS_THUMBNAIL") == "0" {
		return thumbnailOp{}, false
	}
	if opts.OutFormat == ops.FormatJSON {
		return thumbnailOp{}, false
	}
	// vips_thumbnail auto-rotates by default and offers no per-call switch, so
	// ar_0 must take the explicit path where AutoRotate is skipped.
	if !o.AutoRotates() {
		return thumbnailOp{}, false
	}
	// vips_thumbnail has no kernel override (only a loader option string), so a
	// non-default ra_* must take the explicit-resize path or the kernel would be
	// silently ignored. That costs shrink-on-load, which is why lanczos3 stays
	// the default in the URL parser.
	if o.Algorithm != "" && o.Algorithm != ops.AlgoLanczos3 {
		return thumbnailOp{}, false
	}
	if o.Trim > 0 || o.Rotate != 0 || o.Flip != "" || o.Blur > 0 || o.Sharpen > 0 ||
		o.Pixelate > 0 || o.Brightness != 0 || o.Contrast != 0 || o.Saturation != 0 || o.Monochrome {
		return thumbnailOp{}, false
	}
	if !stillImage(src) {
		return thumbnailOp{}, false
	}
	tw, th := o.EffectiveWidth(), o.EffectiveHeight()
	if o.WidthAuto {
		tw = 0
	}
	if o.HeightAuto {
		th = 0
	}
	// vips_thumbnail builds a bounding box from both dimensions. For inside-fit
	// modes a single requested side becomes "fit inside (side, unbounded)", which
	// yields exactly the proportional size the slow path computes. Crop-to-fill
	// and force need both sides, and stay on the slow path otherwise.
	const unbounded = 1 << 20
	box := func(a, b int) (int, int) {
		if a <= 0 && b > 0 {
			a = unbounded
		}
		if b <= 0 && a > 0 {
			b = unbounded
		}
		return a, b
	}
	switch o.Resize {
	case ops.ResizeFit:
		if tw <= 0 && th <= 0 {
			return thumbnailOp{}, false
		}
		tw, th = box(tw, th)
		size := vips.SizeDown
		if o.Enlarge {
			size = vips.SizeBoth
		}
		return thumbnailOp{width: tw, height: th, crop: vips.InterestingNone, size: size}, true
	case ops.ResizeScale:
		if tw <= 0 && th <= 0 {
			return thumbnailOp{}, false
		}
		tw, th = box(tw, th)
		return thumbnailOp{width: tw, height: th, crop: vips.InterestingNone, size: vips.SizeBoth}, true
	case ops.ResizeFill:
		// With only one side requested there is nothing to crop away: c_fill
		// degenerates to an aspect-preserving fit to that side (see the slow
		// path), which shrink-on-load can serve exactly.
		if tw <= 0 || th <= 0 {
			if tw <= 0 && th <= 0 {
				return thumbnailOp{}, false
			}
			tw, th = box(tw, th)
			size := vips.SizeDown
			if o.Enlarge {
				size = vips.SizeBoth
			}
			return thumbnailOp{width: tw, height: th, crop: vips.InterestingNone, size: size}, true
		}
		return thumbnailOp{width: tw, height: th, crop: interesting(o.Gravity), size: vips.SizeBoth}, true
	case ops.ResizeForce:
		// c_force on one side is also aspect-preserving, so it maps to the same
		// single-dimension thumbnail call.
		if tw <= 0 || th <= 0 {
			if tw <= 0 && th <= 0 {
				return thumbnailOp{}, false
			}
			tw, th = box(tw, th)
			return thumbnailOp{width: tw, height: th, crop: vips.InterestingNone, size: vips.SizeBoth}, true
		}
		return thumbnailOp{width: tw, height: th, crop: vips.InterestingNone, size: vips.SizeForce}, true
	}
	return thumbnailOp{}, false
}

// stillImage reports whether the bytes are a still raster format that
// shrink-on-load handles identically to the full-decode path. Animated GIF and
// container-level animation (WebP/AVIF "ANIM") stay on the slow path.
func stillImage(src []byte) bool {
	if len(src) < 12 {
		return false
	}
	switch {
	case src[0] == 0xFF && src[1] == 0xD8: // JPEG
		return true
	case src[0] == 0x89 && string(src[1:4]) == "PNG":
		return true
	case string(src[0:4]) == "RIFF" && string(src[8:12]) == "WEBP":
		head := src
		if len(head) > 64 {
			head = head[:64]
		}
		return !strings.Contains(string(head[12:]), "ANIM")
	}
	return false
}

func apply(img *vips.ImageRef, o ops.Ops) (*vips.ImageRef, error) {
	if o.Trim > 0 {
		left, top, width, height, err := img.FindTrim(float64(o.Trim), nil)
		if err == nil && width > 0 && height > 0 {
			if err := img.Crop(left, top, width, height); err != nil {
				return nil, err
			}
		}
	}
	switch o.Rotate {
	case 90:
		if err := img.Rotate(vips.Angle90); err != nil {
			return nil, err
		}
	case 180:
		if err := img.Rotate(vips.Angle180); err != nil {
			return nil, err
		}
	case 270:
		if err := img.Rotate(vips.Angle270); err != nil {
			return nil, err
		}
	}
	if o.Flip != "" {
		if o.Flip == "h" || o.Flip == "hv" {
			if err := img.Flip(vips.DirectionHorizontal); err != nil {
				return nil, err
			}
		}
		if o.Flip == "v" || o.Flip == "hv" {
			if err := img.Flip(vips.DirectionVertical); err != nil {
				return nil, err
			}
		}
	}
	if o.Blur > 0 {
		sigma := o.Blur * float64(maxInt(img.Width(), img.Height())) / 500
		if sigma < 0.3 {
			sigma = 0.3
		}
		if err := img.GaussianBlur(sigma); err != nil {
			return nil, err
		}
	}
	if o.Sharpen > 0 {
		if err := img.Sharpen(1, 1+o.Sharpen*0.2, 2); err != nil {
			return nil, err
		}
	}
	if err := geometry(img, o); err != nil {
		return nil, err
	}
	if o.Pixelate > 0 {
		if err := pixelateVips(img, o.Pixelate); err != nil {
			return nil, err
		}
	}
	if o.Brightness != 0 || o.Contrast != 0 || o.Saturation != 0 || o.Monochrome {
		if err := adjustColorVips(img, o); err != nil {
			return nil, err
		}
	}
	return applyColorEffectsVips(img, o)
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

// applyColorEffectsVips runs the shared colour effects. govips has no cheap
// per-pixel hook, so duotone/colorize round-trip through a Go image: the effect
// only runs when the URL asks for it, and the result stays identical to the
// pure-Go path.
// applyWatermarkVips composites the watermark through the same pure-Go routine
// the default backend uses, so both backends place pixels identically. The
// round-trip is the price of parity; watermarks are small and this only runs on
// cache misses.
func applyWatermarkVips(img *vips.ImageRef, o ops.Ops, opts Options) (*vips.ImageRef, error) {
	if o.Watermark == nil || opts.Watermark == nil {
		return img, nil
	}
	goImg, err := img.ToImage(&vips.ExportParams{})
	if err != nil {
		return nil, fmt.Errorf("decode for watermark: %w", err)
	}
	next, err := vips.NewImageFromGoImage(applyWatermark(goImg, opts.Watermark, *o.Watermark))
	if err != nil {
		return nil, fmt.Errorf("reload after watermark: %w", err)
	}
	img.Close()
	return next, nil
}

func applyColorEffectsVips(img *vips.ImageRef, o ops.Ops) (*vips.ImageRef, error) {
	if o.Colorize.Opacity <= 0 && o.Duotone.Intensity <= 0 && o.Gradient.IsZero() {
		return img, nil
	}
	// ToGoImage reads raw pixels straight out of libvips. ToImage (deprecated)
	// encodes the image and decodes it again in Go, which cost ~3 ms per
	// request on a 400 px result and returned YCbCr, forcing another full
	// conversion in nrgbaOf.
	goImg, err := img.ToGoImage()
	if err != nil {
		return nil, fmt.Errorf("decode for colour effects: %w", err)
	}
	// Hand the pixel buffer back directly instead of going through
	// NewImageFromGoImage, which walks the image a second time to build the same
	// RGBA bytes. The effects already produce a non-premultiplied buffer.
	out := nrgbaOf(applyColorEffects(goImg, o))
	next, err := vips.NewImageFromMemory(
		out.Pix, out.Rect.Dx(), out.Rect.Dy(), 4,
		vips.BandFormatUchar, vips.InterpretationSRGB,
	)
	if err != nil {
		return nil, fmt.Errorf("reload after colour effects: %w", err)
	}
	// The caller adopts the returned reference, so release the one we replaced.
	img.Close()
	return next, nil
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
		return img.ResizeWithVScale(float64(nw)/float64(sw), float64(nh)/float64(sh), kernelFor(o.Algorithm))
	case ops.ResizeFill:
		if tw <= 0 || th <= 0 {
			nw, nh := fit(sw, sh, tw, th, o.Enlarge)
			return img.ResizeWithVScale(float64(nw)/float64(sw), float64(nh)/float64(sh), kernelFor(o.Algorithm))
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
			if err := img.ResizeWithVScale(float64(nw)/float64(sw), float64(nh)/float64(sh), kernelFor(o.Algorithm)); err != nil {
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
	out, err := exportAt(img, format, quality, o)
	if err != nil {
		return nil, fmt.Errorf("encode %s: %w", opts.OutFormat, err)
	}
	if o.MaxBytes <= 0 || !lossy(opts.OutFormat) || len(out) <= o.MaxBytes {
		return out, nil
	}
	return fitBytesVips(img, format, o, o.MaxBytes, quality, out)
}

// avifEffortLevel is libheif's effort knob (0..9). libaom is pathologically
// slow above 3 in libvips 8.18 (measured on a 400px image: effort 4 = 173 ms,
// effort 3 = 40 ms, effort 2 = 22 ms, with a 0.2% size difference between 3 and
// 4), so the default is 2 rather than libvips' own 4. On a DIV2K corpus (512px
// fit, AVIF source) effort 2 serves 22 images/s at 37.1 kB, effort 3 14 images/s
// and effort 1 26 images/s at 36.8 kB.
var (
	avifEffortOnce sync.Once
	avifEffort     int
)

func avifEffortLevel() int {
	avifEffortOnce.Do(func() {
		avifEffort = 2
		if raw := strings.TrimSpace(os.Getenv("IOIMG_AVIF_EFFORT")); raw != "" {
			if n, err := strconv.Atoi(raw); err == nil && n >= 0 && n <= 9 {
				avifEffort = n
			}
		}
	})
	return avifEffort
}

// exportAt maps the per-format encoder overrides onto libvips. Only the knobs
// libvips actually exposes are applied (PNG interlace, WebP effort); the rest
// are documented as pure-Go-only in docs/API.md.
func exportAt(img *vips.ImageRef, format vips.ImageType, quality int, o ops.Ops) ([]byte, error) {
	params := &vips.ExportParams{
		Format:        format,
		Quality:       quality,
		Compression:   6,
		Interlaced:    false,
		StripMetadata: true,
		Effort:        4,
		Speed:         8,
	}
	if v, err := strconv.ParseBool(os.Getenv("IOIMG_JPEG_PROGRESSIVE")); err == nil && v && format == vips.ImageTypeJPEG {
		params.Interlaced = true
	}
	if o.PNGEncoder != nil && o.PNGEncoder.Interlaced != nil {
		params.Interlaced = *o.PNGEncoder.Interlaced
	}
	if o.WebPEncoder != nil && o.WebPEncoder.Compression != nil {
		params.Effort = *o.WebPEncoder.Compression
	}

	var out []byte
	if format == vips.ImageTypeAVIF {
		// ExportParams.Speed is deprecated and does not reach libheif; Effort is
		// the knob that actually changes encode time.
		out, _, err := img.ExportAvif(&vips.AvifExportParams{
			Quality:       quality,
			Effort:        avifEffortLevel(),
			StripMetadata: true,
			Bitdepth:      8,
		})
		if err != nil {
			return nil, err
		}
		return applyDPI(out, format, o)
	}
	out, _, err := img.Export(params)
	if err != nil {
		return nil, err
	}
	return applyDPI(out, format, o)
}

// applyDPI adds the density metadata libvips strips on export.
func applyDPI(out []byte, format vips.ImageType, o ops.Ops) ([]byte, error) {
	if o.DPI > 0 {
		// Same post-encode metadata pass as the pure-Go path: libvips strips
		// density when StripMetadata is on, and this keeps both backends
		// byte-comparable on the metadata we promise.
		out = setDPI(out, formatName(format), o.DPI)
	}
	return out, nil
}

// formatName maps a vips image type to the string setDPI understands.
func formatName(t vips.ImageType) string {
	switch t {
	case vips.ImageTypePNG:
		return "png"
	case vips.ImageTypeWEBP:
		return "webp"
	case vips.ImageTypeAVIF:
		return "avif"
	case vips.ImageTypeGIF:
		return "gif"
	}
	return "jpeg"
}

// fitBytesVips mirrors fitBytes: highest quality that fits the mb_ budget, and
// never an error when nothing fits.
func fitBytesVips(img *vips.ImageRef, format vips.ImageType, o ops.Ops, maxBytes, startQuality int, first []byte) ([]byte, error) {
	// A buffer-backed vips image is streamed sequentially: the first export
	// consumes it, so a second export fails with "out of order read". The search
	// needs repeatable reads, so materialise the pixels once.
	// ToBytes serialises the pipeline in vips' native format; reloading it gives
	// a memory-backed image whose pixels can be read repeatedly.
	raw, err := img.ToBytes()
	if err != nil {
		return first, nil
	}
	mem, err := vips.NewImageFromBuffer(raw)
	if err != nil {
		return first, nil
	}
	defer mem.Close()
	img = mem

	best := []byte(nil)
	lo, hi := minQuality, startQuality-1
	for i := 0; i < maxQualityProbes && lo <= hi; i++ {
		mid := (lo + hi) / 2
		cand, err := exportAt(img, format, mid, o)
		if err != nil {
			return nil, err
		}
		if len(cand) <= maxBytes {
			best = cand
			lo = mid + 1
		} else {
			hi = mid - 1
		}
	}
	if best != nil {
		return best, nil
	}
	smallest := first
	if floor, err := exportAt(img, format, minQuality, o); err == nil && len(floor) < len(smallest) {
		smallest = floor
	}
	return smallest, nil
}

// kernelFor maps ra_* onto vips kernels. The empty value keeps lanczos3, which
// is vips' own default.
func kernelFor(a ops.Algorithm) vips.Kernel {
	switch a {
	case ops.AlgoNearest:
		return vips.KernelNearest
	case ops.AlgoLinear:
		return vips.KernelLinear
	case ops.AlgoCubic:
		return vips.KernelCubic
	case ops.AlgoLanczos2:
		return vips.KernelLanczos2
	default:
		return vips.KernelLanczos3
	}
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

// DecodeAny decodes an image with libvips, mirroring the pure-Go helper.
func DecodeAny(data []byte) (image.Image, Info, error) {
	ensureVips()
	img, err := vips.NewImageFromBuffer(data)
	if err != nil {
		return nil, Info{}, ErrDecode
	}
	defer img.Close()
	info := Info{
		Width:    img.Width(),
		Height:   img.Height(),
		Format:   imageTypeName(img.Format()),
		HasAlpha: img.HasAlpha(),
		Animated: img.Pages() > 1,
		Bytes:    len(data),
	}
	goImg, err := img.ToImage(&vips.ExportParams{})
	if err != nil {
		return nil, Info{}, ErrDecode
	}
	return goImg, info, nil
}
