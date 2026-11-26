//go:build vips

package imaging

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"testing"

	"github.com/davidbyttow/govips/v2/vips"
	"github.com/ioimg/ioimg/service/pkg/ops"
)

// TestVipsGradient checks the vips path goes through the shared per-pixel
// implementation: a black gradient over white must darken the bottom.
func TestVipsGradient(t *testing.T) {
	src := vipsSizedJPEG(t, 40, 20)
	ensureVips()

	img, err := vips.NewImageFromBuffer(src)
	if err != nil {
		t.Fatal(err)
	}
	o, err := ops.Parse("w_40,gr_1:000000:down", nil)
	if err != nil {
		t.Fatal(err)
	}
	out, err := applyColorEffectsVips(img, o)
	if err != nil {
		t.Fatal(err)
	}
	defer out.Close()

	goImg, err := out.ToImage(&vips.ExportParams{})
	if err != nil {
		t.Fatal(err)
	}
	b := goImg.Bounds()
	top := nrgbaOf(goImg).NRGBAAt(b.Dx()/2, 0)
	bottom := nrgbaOf(goImg).NRGBAAt(b.Dx()/2, b.Dy()-1)
	if top.R < 150 {
		t.Errorf("top row should stay bright, got %d", top.R)
	}
	if bottom.R > 60 {
		t.Errorf("bottom row should be darkened, got %d", bottom.R)
	}
}

// TestVipsPNGInterlaced pins that pngo_ reaches libvips (it writes the
// interlace flag into the PNG stream) and does not break export.
func TestVipsPNGInterlaced(t *testing.T) {
	ensureVips()
	src := vipsSizedJPEG(t, 32, 16)
	for _, interlaced := range []bool{true, false} {
		img, err := vips.NewImageFromBuffer(src)
		if err != nil {
			t.Fatal(err)
		}
		b := interlaced
		o := ops.Default()
		o.Format = ops.FormatPNG
		o.PNGEncoder = &ops.PNGEncoder{Interlaced: &b}
		out, err := exportAt(img, vips.ImageTypePNG, 80, o)
		img.Close()
		if err != nil {
			t.Fatalf("interlaced=%v: %v", interlaced, err)
		}
		if !bytes.HasPrefix(out, []byte("\x89PNG\r\n\x1a\n")) {
			t.Fatalf("interlaced=%v: not a PNG", interlaced)
		}
	}
}

// TestVipsDPI pins that dpi_ reaches the encoded bytes through the shared
// post-encode pass, for both PNG (pHYs) and JPEG (JFIF density).
func TestVipsDPI(t *testing.T) {
	ensureVips()
	src := vipsSizedJPEG(t, 24, 16)
	for _, format := range []string{"png", "jpeg"} {
		o, err := ops.Parse("w_24,f_"+format+",dpi_144", nil)
		if err != nil {
			t.Fatal(err)
		}
		res, err := DefaultProcessor().Process(src, o, Options{OutFormat: ops.Format(format), Quality: 80})
		if err != nil {
			t.Fatalf("%s: %v", format, err)
		}
		switch format {
		case "png":
			idx := bytes.Index(res.Data, []byte("pHYs"))
			if idx < 0 {
				t.Fatalf("png: no pHYs chunk")
			}
			ppm := binary.BigEndian.Uint32(res.Data[idx+4:])
			if ppm < 5600 || ppm > 5750 {
				t.Fatalf("png pHYs = %d px/m, want ~5669 (144 dpi)", ppm)
			}
		case "jpeg":
			if len(res.Data) < 18 || res.Data[13] != 1 {
				t.Fatalf("jpeg: units = %d, want 1", res.Data[13])
			}
			if got := binary.BigEndian.Uint16(res.Data[14:]); got != 144 {
				t.Fatalf("jpeg density = %d, want 144", got)
			}
		}
	}
}

// TestVipsWatermarkParity pins that the vips pipeline composites the watermark
// through the same routine as the pure-Go pipeline.
func TestVipsWatermarkParity(t *testing.T) {
	ensureVips()
	src := vipsSizedJPEG(t, 64, 48)
	wmImg := image.NewNRGBA(image.Rect(0, 0, 8, 8))
	for y := 0; y < 8; y++ {
		for x := 0; x < 8; x++ {
			wmImg.SetNRGBA(x, y, color.NRGBA{R: 255, A: 255})
		}
	}
	wm := image.Image(wmImg)

	o, err := ops.Parse("w_64,h_48,c_fill,wm_1:ce", nil)
	if err != nil {
		t.Fatal(err)
	}
	plain, err := DefaultProcessor().Process(src, o, Options{OutFormat: ops.FormatPNG, Quality: 80})
	if err != nil {
		t.Fatal(err)
	}
	marked, err := DefaultProcessor().Process(src, o, Options{OutFormat: ops.FormatPNG, Quality: 80, Watermark: wm})
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Equal(plain.Data, marked.Data) {
		t.Fatal("vips watermark produced an identical image")
	}
	img, err := png.Decode(bytes.NewReader(marked.Data))
	if err != nil {
		t.Fatal(err)
	}
	if got := color.NRGBAModel.Convert(img.At(32, 24)).(color.NRGBA); got.R < 200 {
		t.Fatalf("centre pixel %+v, want the red watermark", got)
	}
}

// TestVipsPNGQuantize pins that pngo_ quantize is honoured on the vips backend:
// the output must be an indexed PNG with at most the requested number of
// colours, the same URL semantics as the pure-Go path.
func TestVipsPNGQuantize(t *testing.T) {
	ensureVips()
	src := deterministicSource(t)
	for _, n := range []int{8, 64, 256} {
		o, err := ops.Parse(fmt.Sprintf("w_64,h_48,c_fill,f_png,pngo_1:1:%d", n), nil)
		if err != nil {
			t.Fatalf("n=%d: %v", n, err)
		}
		res, err := DefaultProcessor().Process(src, o, Options{OutFormat: ops.FormatPNG, Quality: 80})
		if err != nil {
			t.Fatalf("n=%d: %v", n, err)
		}
		img, err := png.Decode(bytes.NewReader(res.Data))
		if err != nil {
			t.Fatalf("n=%d: decode: %v", n, err)
		}
		pal, ok := img.(*image.Paletted)
		if !ok {
			t.Fatalf("n=%d: expected paletted output, got %T", n, img)
		}
		if len(pal.Palette) == 0 || len(pal.Palette) > n {
			t.Fatalf("n=%d: palette has %d entries", n, len(pal.Palette))
		}
	}
}
