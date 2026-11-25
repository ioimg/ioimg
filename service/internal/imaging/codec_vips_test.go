//go:build vips

package imaging

import (
	"bytes"
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
