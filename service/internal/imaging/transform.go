//go:build !vips

// Package imaging decodes, transforms, and encodes images for ioimg.
//
// The default build is pure Go (no cgo, works on Windows/macOS/Linux). WebP
// and AVIF are encoded through WASM builds of libwebp/libavif.
package imaging

import (
	"bytes"
	"fmt"
	"image"
	"image/color"
	"image/gif"
	"image/jpeg"
	"image/png"
	"math"
	"strings"

	"github.com/ioimg/ioimg/service/pkg/ops"

	_ "github.com/gen2brain/avif"
	webp "github.com/gen2brain/webp"
	xdraw "golang.org/x/image/draw"

	// Register additional input decoders. These are decode-only: the output
	// formats advertised by AvailableFormats stay the same as imgproxy's
	// common web targets.
	_ "golang.org/x/image/bmp"
	_ "golang.org/x/image/tiff"
)

// AvailableFormats reports which output encoders this build supports.
func AvailableFormats() map[ops.Format]bool {
	return map[ops.Format]bool{
		ops.FormatJPEG: true,
		ops.FormatPNG:  true,
		ops.FormatWebP: true,
		ops.FormatAVIF: true,
		ops.FormatGIF:  true,
		ops.FormatJSON: true,
	}
}

// Decode sniffs and decodes an image. For GIFs it selects the first frame;
// other multi-frame formats keep their first frame because image/gif is the
// only multi-frame decoder linked into the pure-Go build.
func Decode(data []byte) (image.Image, Info, error) { return decode(data, true, ops.Ops{}) }

// DecodeWithOptions decodes with the request's orientation policy (ar_*) and
// GIF page selection (pg_/pgs_/da_).
func DecodeWithOptions(data []byte, o ops.Ops) (image.Image, Info, error) {
	return decode(data, o.AutoRotates(), o)
}

func decode(data []byte, autoRotate bool, o ops.Ops) (image.Image, Info, error) {
	if len(data) == 0 {
		return nil, Info{}, ErrDecode
	}
	cfg, format, err := image.DecodeConfig(bytes.NewReader(data))
	if err != nil {
		return nil, Info{}, ErrDecode
	}
	if cfg.Width <= 0 || cfg.Height <= 0 {
		return nil, Info{}, ErrDecode
	}
	if cfg.Width > MaxSide || cfg.Height > MaxSide || cfg.Width*cfg.Height > MaxPixels {
		return nil, Info{}, ErrDimension
	}
	if format == "gif" {
		img, info, err := decodeGIF(data, o)
		if err != nil {
			return nil, Info{}, err
		}
		if autoRotate {
			img = applyOrientation(img, exifOrientation(data))
		}
		info.Bytes = len(data)
		return img, info, nil
	}
	img, decFormat, err := image.Decode(bytes.NewReader(data))
	if err != nil {
		return nil, Info{}, ErrDecode
	}
	if decFormat != "" {
		format = decFormat
	}
	if autoRotate {
		img = applyOrientation(img, exifOrientation(data))
	}
	info := Info{
		Width:    img.Bounds().Dx(),
		Height:   img.Bounds().Dy(),
		Format:   format,
		HasAlpha: hasAlpha(img),
		Bytes:    len(data),
	}
	return img, info, nil
}

// Transform applies ops and encodes the result. It returns the encoded bytes
// and a phase timing map for the Server-Timing header.
func Transform(img image.Image, o ops.Ops, opts Options) ([]byte, map[string]float64, error) {
	timings := map[string]float64{}
	start := nowMs()

	if o.Trim > 0 {
		img = trim(img, o.Trim)
	}
	if o.Rotate != 0 {
		img = rotate(img, o.Rotate)
	}
	if o.Flip != "" {
		img = flip(img, o.Flip)
	}
	if o.Blur > 0 {
		img = blur(img, o.Blur)
	}
	if o.Sharpen > 0 {
		img = sharpen(img, o.Sharpen)
	}
	if o.Width != 0 || o.Height != 0 || o.WidthAuto || o.HeightAuto {
		var err error
		img, err = geometry(img, o)
		if err != nil {
			return nil, timings, err
		}
	}
	img = enforceMinimum(img, o)
	// Reshape order: resize -> extend -> flatten -> effects -> watermark ->
	// encode. Extend pads the resized result into the requested canvas; flatten
	// then composites it onto the background (white unless bg_ is set).
	if o.Extend {
		img = extendImage(img, o, backgroundFor(o, false))
	}
	if o.Flatten {
		img = flattenImage(img, backgroundFor(o, true))
	}
	if o.Pixelate > 0 {
		img = pixelate(img, o.Pixelate)
	}
	if o.Brightness != 0 || o.Contrast != 0 || o.Saturation != 0 || o.Monochrome {
		img = adjustColor(img, o.Brightness, o.Contrast, o.Saturation, o.Monochrome)
	}
	img = applyColorEffects(img, o)
	if o.Watermark != nil && opts.Watermark != nil {
		start = nowMs()
		img = applyWatermark(img, opts.Watermark, *o.Watermark)
		timings["watermark"] = nowMs() - start
	}
	timings["transform"] = nowMs() - start

	start = nowMs()
	out, err := encode(img, o, opts)
	timings["encode"] = nowMs() - start
	if err != nil {
		return nil, timings, err
	}
	return out, timings, nil
}

func encode(img image.Image, o ops.Ops, opts Options) ([]byte, error) {
	format := opts.OutFormat
	quality := opts.Quality
	if quality == 0 {
		quality = autoQuality(format, img.Bounds().Dx(), img.Bounds().Dy())
	}
	out, err := encodeAt(img, o, format, quality)
	if err != nil {
		return nil, err
	}
	if o.DPI > 0 {
		out = setDPI(out, string(format), o.DPI)
	}
	if o.MaxBytes <= 0 || !lossy(format) || len(out) <= o.MaxBytes {
		return out, nil
	}
	res, err := fitBytes(img, o, format, quality, out)
	if err != nil {
		return nil, err
	}
	if o.DPI > 0 {
		res = setDPI(res, string(format), o.DPI)
	}
	return res, nil
}

// fitBytes finds the highest quality whose output fits the mb_ budget. It never
// fails the request: if even the floor does not fit, the smallest encoding is
// returned so the client still gets an image (and the CDN caches it).
func fitBytes(img image.Image, o ops.Ops, format ops.Format, startQuality int, first []byte) ([]byte, error) {
	best := []byte(nil)
	lo, hi := minQuality, startQuality-1
	for i := 0; i < maxQualityProbes && lo <= hi; i++ {
		mid := (lo + hi) / 2
		cand, err := encodeAt(img, o, format, mid)
		if err != nil {
			return nil, err
		}
		if len(cand) <= o.MaxBytes {
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
	if floor, err := encodeAt(img, o, format, minQuality); err == nil && len(floor) < len(smallest) {
		smallest = floor
	}
	return smallest, nil
}

func encodeAt(img image.Image, o ops.Ops, format ops.Format, quality int) ([]byte, error) {
	var buf bytes.Buffer
	switch format {
	case ops.FormatWebP:
		method := 4
		if o.WebPEncoder != nil && o.WebPEncoder.Compression != nil {
			method = *o.WebPEncoder.Compression
		}
		if err := webp.Encode(&buf, img, webp.Options{Quality: quality, Method: method}); err != nil {
			return nil, fmt.Errorf("encode webp: %w", err)
		}
	case ops.FormatAVIF:
		// AVIF goes through the gen2brain WASM encoder.
		subsample := image.YCbCrSubsampleRatio420
		if o.AVIFEncoder != nil && o.AVIFEncoder.Subsample != nil {
			switch *o.AVIFEncoder.Subsample {
			case 0:
				subsample = image.YCbCrSubsampleRatio444
			case 1:
				subsample = image.YCbCrSubsampleRatio422
			}
		}
		if err := encodeAVIF(&buf, img, quality, subsample); err != nil {
			return nil, fmt.Errorf("encode avif: %w", err)
		}
	case ops.FormatPNG:
		enc := png.Encoder{CompressionLevel: png.DefaultCompression}
		if o.Palette {
			img = toPaletted(img)
		}
		// pngo_: the Go encoder has no interlace option, so only quantize is
		// honoured here (documented in docs/API.md).
		if o.PNGEncoder != nil && isTrue(o.PNGEncoder.Quantize) {
			img = toPalettedN(img, pngColors(o.PNGEncoder.Colors))
		}
		if err := enc.Encode(&buf, img); err != nil {
			return nil, fmt.Errorf("encode png: %w", err)
		}
	case ops.FormatGIF:
		if err := gif.Encode(&buf, img, &gif.Options{NumColors: 256}); err != nil {
			return nil, fmt.Errorf("encode gif: %w", err)
		}
	default:
		// JPEG has no alpha: flatten onto the requested background (white by
		// default) so transparent PNGs do not turn black.
		if hasAlpha(img) {
			img = flattenImage(img, backgroundForJPEG(o))
		}
		if err := jpeg.Encode(&buf, img, &jpeg.Options{Quality: quality}); err != nil {
			return nil, fmt.Errorf("encode jpeg: %w", err)
		}
	}
	return buf.Bytes(), nil
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

// geometry applies the c_* mode and g_* anchor.
// enforceMinimum applies mw_/mh_: a result smaller than the requested minimum
// is enlarged, keeping the aspect ratio.
func enforceMinimum(img image.Image, o ops.Ops) image.Image {
	if o.MinWidth <= 0 && o.MinHeight <= 0 {
		return img
	}
	rw, rh := img.Bounds().Dx(), img.Bounds().Dy()
	if rw <= 0 || rh <= 0 {
		return img
	}
	f := 1.0
	if o.MinWidth > 0 && rw < o.MinWidth {
		f = math.Max(f, float64(o.MinWidth)/float64(rw))
	}
	if o.MinHeight > 0 && rh < o.MinHeight {
		f = math.Max(f, float64(o.MinHeight)/float64(rh))
	}
	if f <= 1 {
		return img
	}
	nw := int(math.Round(float64(rw) * f))
	nh := int(math.Round(float64(rh) * f))
	if nw > ops.MaxDimension || nh > ops.MaxDimension {
		// Clamp to the hard limit and recompute so the aspect ratio survives.
		g := math.Min(float64(ops.MaxDimension)/float64(nw), float64(ops.MaxDimension)/float64(nh))
		nw = int(math.Round(float64(nw) * g))
		nh = int(math.Round(float64(nh) * g))
	}
	if nw <= rw || nh <= rh {
		return img
	}
	return scale(img, nw, nh, o.Algorithm)
}

func geometry(src image.Image, o ops.Ops) (image.Image, error) {
	sw, sh := src.Bounds().Dx(), src.Bounds().Dy()
	tw, th := o.EffectiveWidth(), o.EffectiveHeight()

	// Width/height "auto" follows the source aspect ratio.
	if o.WidthAuto {
		tw = int(math.Round(float64(th) * float64(sw) / float64(sh)))
	}
	if o.HeightAuto {
		th = int(math.Round(float64(tw) * float64(sh) / float64(sw)))
	}
	if tw <= 0 && th <= 0 {
		return src, nil
	}
	if tw > ops.MaxDimension || th > ops.MaxDimension {
		return nil, ErrDimension
	}

	mode := o.Resize
	switch mode {
	case ops.ResizeFit, ops.ResizeScale:
		allow := o.Enlarge || mode == ops.ResizeScale
		nw, nh := fitSize(sw, sh, tw, th, allow)
		if nw == sw && nh == sh {
			return src, nil
		}
		return scale(src, nw, nh, o.Algorithm), nil
	case ops.ResizeFill:
		if tw <= 0 || th <= 0 {
			nw, nh := fitSize(sw, sh, tw, th, o.Enlarge)
			return scale(src, nw, nh, o.Algorithm), nil
		}
		scaleF := math.Max(float64(tw)/float64(sw), float64(th)/float64(sh))
		if !o.Enlarge && scaleF > 1 {
			scaleF = 1
		}
		nw := int(math.Round(float64(sw) * scaleF))
		nh := int(math.Round(float64(sh) * scaleF))
		scaled := scale(src, nw, nh, o.Algorithm)
		return cropTo(scaled, tw, th, o.Gravity), nil
	case ops.ResizeForce:
		nw, nh := tw, th
		if nw <= 0 {
			nw = int(math.Round(float64(th) * float64(sw) / float64(sh)))
		}
		if nh <= 0 {
			nh = int(math.Round(float64(tw) * float64(sh) / float64(sw)))
		}
		return scale(src, clampSide(nw), clampSide(nh), o.Algorithm), nil
	case ops.ResizePad:
		if tw <= 0 || th <= 0 {
			return nil, fmt.Errorf("c_pad requires w and h")
		}
		nw, nh := fitSize(sw, sh, tw, th, o.Enlarge)
		inner := scale(src, nw, nh, o.Algorithm)
		canvas := image.NewNRGBA(image.Rect(0, 0, tw, th))
		fillCanvas(canvas, backgroundFor(o, false))
		dx, dy := anchor(inner.Bounds().Dx(), inner.Bounds().Dy(), tw, th, o.Gravity)
		xdraw.Draw(canvas, image.Rect(dx, dy, dx+inner.Bounds().Dx(), dy+inner.Bounds().Dy()), inner, image.Point{}, xdraw.Over)
		return canvas, nil
	case ops.ResizeCrop:
		if tw <= 0 || th <= 0 {
			return nil, fmt.Errorf("c_crop requires w and h")
		}
		return cropTo(src, min(tw, sw), min(th, sh), o.Gravity), nil
	}
	return src, nil
}

func fitSize(sw, sh, tw, th int, enlarge bool) (int, int) {
	scaleF := math.Inf(1)
	if tw > 0 {
		scaleF = math.Min(scaleF, float64(tw)/float64(sw))
	}
	if th > 0 {
		scaleF = math.Min(scaleF, float64(th)/float64(sh))
	}
	if math.IsInf(scaleF, 1) {
		return sw, sh
	}
	if !enlarge && scaleF > 1 {
		scaleF = 1
	}
	nw := max(1, int(math.Round(float64(sw)*scaleF)))
	nh := max(1, int(math.Round(float64(sh)*scaleF)))
	return clampSide(nw), clampSide(nh)
}

func clampSide(v int) int {
	if v < 1 {
		return 1
	}
	if v > ops.MaxDimension {
		return ops.MaxDimension
	}
	return v
}

// scalerFor maps ra_* onto the pure-Go interpolators. golang.org/x/image has no
// Lanczos implementation, so both Lanczos values fall back to Catmull-Rom here;
// the vips backend honours them exactly.
func scalerFor(a ops.Algorithm) xdraw.Interpolator {
	switch a {
	case ops.AlgoNearest:
		return xdraw.NearestNeighbor
	case ops.AlgoLinear:
		return xdraw.ApproxBiLinear
	default:
		return xdraw.CatmullRom
	}
}

func scale(src image.Image, w, h int, a ops.Algorithm) image.Image {
	dst := image.NewNRGBA(image.Rect(0, 0, w, h))
	scalerFor(a).Scale(dst, dst.Bounds(), src, src.Bounds(), xdraw.Src, nil)
	return dst
}

func cropTo(src image.Image, w, h int, g ops.Gravity) image.Image {
	b := src.Bounds()
	w = min(w, b.Dx())
	h = min(h, b.Dy())
	dx, dy := anchor(w, h, b.Dx(), b.Dy(), g)
	r := image.Rect(dx, dy, dx+w, dy+h)
	dst := image.NewNRGBA(image.Rect(0, 0, w, h))
	xdraw.Draw(dst, dst.Bounds(), src, r.Min, xdraw.Src)
	return dst
}

// anchor returns the offset of an inner box inside an outer box.
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
	return max(0, x), max(0, y)
}

func rotate(src image.Image, deg int) image.Image {
	b := src.Bounds()
	w, h := b.Dx(), b.Dy()
	switch deg {
	case 90:
		dst := image.NewNRGBA(image.Rect(0, 0, h, w))
		for y := 0; y < h; y++ {
			for x := 0; x < w; x++ {
				dst.Set(h-1-y, x, src.At(b.Min.X+x, b.Min.Y+y))
			}
		}
		return dst
	case 180:
		dst := image.NewNRGBA(image.Rect(0, 0, w, h))
		for y := 0; y < h; y++ {
			for x := 0; x < w; x++ {
				dst.Set(w-1-x, h-1-y, src.At(b.Min.X+x, b.Min.Y+y))
			}
		}
		return dst
	case 270:
		dst := image.NewNRGBA(image.Rect(0, 0, h, w))
		for y := 0; y < h; y++ {
			for x := 0; x < w; x++ {
				dst.Set(y, w-1-x, src.At(b.Min.X+x, b.Min.Y+y))
			}
		}
		return dst
	}
	return src
}

func flip(src image.Image, mode string) image.Image {
	b := src.Bounds()
	w, h := b.Dx(), b.Dy()
	dst := image.NewNRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			nx, ny := x, y
			if strings.Contains(mode, "h") {
				nx = w - 1 - x
			}
			if strings.Contains(mode, "v") {
				ny = h - 1 - y
			}
			dst.Set(nx, ny, src.At(b.Min.X+x, b.Min.Y+y))
		}
	}
	return dst
}

// blur is a three-pass box blur, which approximates a Gaussian cheaply.
func blur(src image.Image, amount float64) image.Image {
	b := src.Bounds()
	radius := int(math.Round(amount * float64(max(b.Dx(), b.Dy())) / 500))
	radius = max(1, min(radius, 100))
	img := toNRGBA(src)
	for i := 0; i < 3; i++ {
		img = boxBlur(img, radius)
	}
	return img
}

func boxBlur(src *image.NRGBA, r int) *image.NRGBA {
	w, h := src.Bounds().Dx(), src.Bounds().Dy()
	tmp := image.NewNRGBA(image.Rect(0, 0, w, h))
	dst := image.NewNRGBA(image.Rect(0, 0, w, h))
	// Horizontal pass with a sliding window.
	for y := 0; y < h; y++ {
		var sum [4]int
		count := 0
		for x := -r; x <= r; x++ {
			if x >= 0 && x < w {
				off := src.PixOffset(x, y)
				sum[0] += int(src.Pix[off])
				sum[1] += int(src.Pix[off+1])
				sum[2] += int(src.Pix[off+2])
				sum[3] += int(src.Pix[off+3])
			}
			count++
		}
		for x := 0; x < w; x++ {
			off := tmp.PixOffset(x, y)
			tmp.Pix[off] = uint8(sum[0] / count)
			tmp.Pix[off+1] = uint8(sum[1] / count)
			tmp.Pix[off+2] = uint8(sum[2] / count)
			tmp.Pix[off+3] = uint8(sum[3] / count)
			out := x - r
			in := x + r + 1
			if out >= 0 {
				off = src.PixOffset(out, y)
				sum[0] -= int(src.Pix[off])
				sum[1] -= int(src.Pix[off+1])
				sum[2] -= int(src.Pix[off+2])
				sum[3] -= int(src.Pix[off+3])
			}
			if in < w {
				off = src.PixOffset(in, y)
				sum[0] += int(src.Pix[off])
				sum[1] += int(src.Pix[off+1])
				sum[2] += int(src.Pix[off+2])
				sum[3] += int(src.Pix[off+3])
			}
		}
	}
	// Vertical pass.
	for x := 0; x < w; x++ {
		var sum [4]int
		count := 0
		for y := -r; y <= r; y++ {
			if y >= 0 && y < h {
				off := tmp.PixOffset(x, y)
				sum[0] += int(tmp.Pix[off])
				sum[1] += int(tmp.Pix[off+1])
				sum[2] += int(tmp.Pix[off+2])
				sum[3] += int(tmp.Pix[off+3])
			}
			count++
		}
		for y := 0; y < h; y++ {
			off := dst.PixOffset(x, y)
			dst.Pix[off] = uint8(sum[0] / count)
			dst.Pix[off+1] = uint8(sum[1] / count)
			dst.Pix[off+2] = uint8(sum[2] / count)
			dst.Pix[off+3] = uint8(sum[3] / count)
			out := y - r
			in := y + r + 1
			if out >= 0 {
				off = tmp.PixOffset(x, out)
				sum[0] -= int(tmp.Pix[off])
				sum[1] -= int(tmp.Pix[off+1])
				sum[2] -= int(tmp.Pix[off+2])
				sum[3] -= int(tmp.Pix[off+3])
			}
			if in < h {
				off = tmp.PixOffset(x, in)
				sum[0] += int(tmp.Pix[off])
				sum[1] += int(tmp.Pix[off+1])
				sum[2] += int(tmp.Pix[off+2])
				sum[3] += int(tmp.Pix[off+3])
			}
		}
	}
	return dst
}

// sharpen applies an unsharp mask.
func sharpen(src image.Image, amount float64) image.Image {
	base := toNRGBA(src)
	soft := boxBlur(base, 1)
	w, h := base.Bounds().Dx(), base.Bounds().Dy()
	dst := image.NewNRGBA(image.Rect(0, 0, w, h))
	k := amount / 10 * 1.5
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			off := base.PixOffset(x, y)
			for c := 0; c < 3; c++ {
				v := float64(base.Pix[off+c]) + k*(float64(base.Pix[off+c])-float64(soft.Pix[off+c]))
				dst.Pix[off+c] = uint8(math.Max(0, math.Min(255, v)))
			}
			dst.Pix[off+3] = base.Pix[off+3]
		}
	}
	return dst
}

// pixelate averages blocks of block x block pixels. Large blocks are clamped
// to the smaller image side so the whole frame becomes its average colour.
func pixelate(src image.Image, block int) image.Image {
	img := toNRGBA(src)
	w, h := img.Bounds().Dx(), img.Bounds().Dy()
	block = min(max(block, 1), max(w, h))
	if block <= 1 {
		return img
	}
	dst := image.NewNRGBA(image.Rect(0, 0, w, h))
	for by := 0; by < h; by += block {
		for bx := 0; bx < w; bx += block {
			x1, y1 := min(bx+block, w), min(by+block, h)
			var sum [4]int
			for y := by; y < y1; y++ {
				for x := bx; x < x1; x++ {
					off := img.PixOffset(x, y)
					sum[0] += int(img.Pix[off])
					sum[1] += int(img.Pix[off+1])
					sum[2] += int(img.Pix[off+2])
					sum[3] += int(img.Pix[off+3])
				}
			}
			n := (x1 - bx) * (y1 - by)
			avg := color.NRGBA{uint8(sum[0] / n), uint8(sum[1] / n), uint8(sum[2] / n), uint8(sum[3] / n)}
			for y := by; y < y1; y++ {
				for x := bx; x < x1; x++ {
					dst.SetNRGBA(x, y, avg)
				}
			}
		}
	}
	return dst
}

// adjustColor applies brightness, contrast, saturation, and grayscale in one
// pass. Values follow imgproxy's -100..100 convention; alpha is untouched.
func adjustColor(src image.Image, brightness, contrast, saturation int, mono bool) image.Image {
	img := toNRGBA(src)
	w, h := img.Bounds().Dx(), img.Bounds().Dy()
	dst := image.NewNRGBA(image.Rect(0, 0, w, h))
	bri := float64(brightness) * 2.55
	con := (100 + float64(contrast)) / 100
	sat := (100 + float64(saturation)) / 100
	if mono {
		sat = 0
	}
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			off := img.PixOffset(x, y)
			r := (float64(img.Pix[off])+bri-128)*con + 128
			g := (float64(img.Pix[off+1])+bri-128)*con + 128
			b := (float64(img.Pix[off+2])+bri-128)*con + 128
			luma := 0.299*r + 0.587*g + 0.114*b
			dst.Pix[off] = clampByte(luma + (r-luma)*sat)
			dst.Pix[off+1] = clampByte(luma + (g-luma)*sat)
			dst.Pix[off+2] = clampByte(luma + (b-luma)*sat)
			dst.Pix[off+3] = img.Pix[off+3]
		}
	}
	return dst
}

func clampByte(v float64) uint8 {
	if v <= 0 {
		return 0
	}
	if v >= 255 {
		return 255
	}
	return uint8(math.Round(v))
}

// trim removes a uniform border, with fuzz on a 0..40 scale.
func trim(src image.Image, fuzz int) image.Image {
	img := toNRGBA(src)
	w, h := img.Bounds().Dx(), img.Bounds().Dy()
	if w < 3 || h < 3 {
		return src
	}
	ref := img.NRGBAAt(0, 0)
	limit := uint8(fuzz * 255 / 40)
	diff := func(c color.NRGBA) bool {
		return absDiff(c.R, ref.R) > limit || absDiff(c.G, ref.G) > limit || absDiff(c.B, ref.B) > limit || absDiff(c.A, ref.A) > limit
	}
	top, bottom, left, right := 0, h-1, 0, w-1
	for top < h && rowUniform(img, top, diff) {
		top++
	}
	for bottom >= top && rowUniform(img, bottom, diff) {
		bottom--
	}
	for left < w && colUniform(img, left, top, bottom, diff) {
		left++
	}
	for right >= left && colUniform(img, right, top, bottom, diff) {
		right--
	}
	if top >= bottom || left >= right {
		return src
	}
	return cropRect(img, image.Rect(left, top, right+1, bottom+1))
}

func rowUniform(img *image.NRGBA, y int, diff func(color.NRGBA) bool) bool {
	for x := 0; x < img.Bounds().Dx(); x++ {
		if diff(img.NRGBAAt(x, y)) {
			return false
		}
	}
	return true
}

func colUniform(img *image.NRGBA, x, top, bottom int, diff func(color.NRGBA) bool) bool {
	for y := top; y <= bottom; y++ {
		if diff(img.NRGBAAt(x, y)) {
			return false
		}
	}
	return true
}

func cropRect(src image.Image, r image.Rectangle) image.Image {
	r = r.Intersect(src.Bounds())
	dst := image.NewNRGBA(image.Rect(0, 0, r.Dx(), r.Dy()))
	xdraw.Draw(dst, dst.Bounds(), src, r.Min, xdraw.Src)
	return dst
}

func isTrue(b *bool) bool { return b != nil && *b }

// pngColors normalises pngo quantization_colors: 0 means the encoder default.
func pngColors(n int) int {
	if n < 2 {
		return 256
	}
	if n > 256 {
		return 256
	}
	return n
}

// toPalettedN quantises to roughly n colours by splitting each channel into
// k levels where k^3 >= n. It is deliberately simple and deterministic: two
// processes must produce the same bytes for the same URL and cache key.
func toPalettedN(src image.Image, n int) image.Image {
	if n >= 256 {
		return toPaletted(src)
	}
	k := 2
	for k*k*k < n {
		k++
	}
	step := 255 / (k - 1)
	if step < 1 {
		step = 1
	}
	levels := make([]uint8, k)
	for i := range levels {
		levels[i] = uint8(i * step)
	}
	pal := make(color.Palette, 0, k*k*k)
	for _, r := range levels {
		for _, g := range levels {
			for _, b := range levels {
				pal = append(pal, color.NRGBA{R: r, G: g, B: b, A: 255})
			}
		}
	}
	b := src.Bounds()
	img := image.NewPaletted(image.Rect(0, 0, b.Dx(), b.Dy()), pal)
	idx := func(v uint8) int { return int(v) / step }
	for y := 0; y < b.Dy(); y++ {
		for x := 0; x < b.Dx(); x++ {
			c := color.NRGBAModel.Convert(src.At(b.Min.X+x, b.Min.Y+y)).(color.NRGBA)
			img.SetColorIndex(x, y, uint8(idx(c.R)*k*k+idx(c.G)*k+idx(c.B)))
		}
	}
	return img
}

func toPaletted(src image.Image) image.Image {
	// 3-3-2 uniform palette: predictable and allocation-free per pixel.
	pal := make(color.Palette, 256)
	for i := 0; i < 256; i++ {
		pal[i] = color.NRGBA{
			R: uint8((i >> 5) & 0x7 * 36),
			G: uint8((i >> 2) & 0x7 * 36),
			B: uint8(i & 0x3 * 85),
			A: 255,
		}
	}
	b := src.Bounds()
	img := image.NewPaletted(image.Rect(0, 0, b.Dx(), b.Dy()), pal)
	for y := 0; y < b.Dy(); y++ {
		for x := 0; x < b.Dx(); x++ {
			c := color.NRGBAModel.Convert(src.At(b.Min.X+x, b.Min.Y+y)).(color.NRGBA)
			idx := int(c.R/36)<<5 | int(c.G/36)<<2 | int(c.B/85)
			img.SetColorIndex(x, y, uint8(idx))
		}
	}
	return img
}

func toNRGBA(src image.Image) *image.NRGBA {
	if n, ok := src.(*image.NRGBA); ok {
		return n
	}
	b := src.Bounds()
	dst := image.NewNRGBA(image.Rect(0, 0, b.Dx(), b.Dy()))
	xdraw.Draw(dst, dst.Bounds(), src, b.Min, xdraw.Src)
	return dst
}

func hasAlpha(img image.Image) bool {
	switch img.(type) {
	case *image.NRGBA, *image.NRGBA64, *image.RGBA, *image.RGBA64, *image.Paletted:
		b := img.Bounds()
		step := max(1, (b.Dx()*b.Dy())/4096)
		i := 0
		for y := b.Min.Y; y < b.Max.Y; y++ {
			for x := b.Min.X; x < b.Max.X; x++ {
				if i%step == 0 {
					_, _, _, a := img.At(x, y).RGBA()
					if a < 0xffff {
						return true
					}
				}
				i++
			}
		}
	}
	return false
}

func absDiff(a, b uint8) uint8 {
	if a > b {
		return a - b
	}
	return b - a
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}

func nowMs() float64 {
	return float64(timeNowUnixNano()) / 1e6
}
