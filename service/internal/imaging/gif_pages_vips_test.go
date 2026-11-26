//go:build vips

package imaging

import (
	"bytes"
	"image"
	"image/color"
	"testing"

	"github.com/ioimg/ioimg/service/pkg/ops"
)

func TestVipsGIFPageSelection(t *testing.T) {
	data := animatedGIF(t)
	p := DefaultProcessor()

	render := func(raw string) (image.Image, Info) {
		t.Helper()
		o, err := ops.Parse(raw, nil)
		if err != nil {
			t.Fatalf("%s: %v", raw, err)
		}
		res, err := p.Process(data, o, Options{OutFormat: ops.FormatPNG})
		if err != nil {
			t.Fatalf("%s: %v", raw, err)
		}
		img, _, err := image.Decode(bytes.NewReader(res.Data))
		if err != nil {
			t.Fatalf("%s: decode output: %v", raw, err)
		}
		return img, res.Info
	}

	img, info := render("pg_1,pgs_2,f_png")
	if info.Height != 20 {
		t.Fatalf("pages_2 height = %d, want 20", info.Height)
	}
	if got := colorOfVips(img, 5, 5); got != (color.RGBA{G: 255, A: 255}) {
		t.Fatalf("top frame = %v, want frame 1 (green)", got)
	}
	if got := colorOfVips(img, 5, 15); got != (color.RGBA{B: 255, A: 255}) {
		t.Fatalf("bottom frame = %v, want frame 2 (blue)", got)
	}

	img, info = render("pg_9,pgs_2,f_png")
	if info.Height != 10 {
		t.Fatalf("page beyond end height = %d, want 10", info.Height)
	}
	if got := colorOfVips(img, 5, 5); got != (color.RGBA{B: 255, A: 255}) {
		t.Fatalf("page beyond end = %v, want the last frame (blue)", got)
	}

	img, info = render("da_1,pg_2,pgs_3,f_png")
	if info.Animated {
		t.Fatal("da_1 must report the source as non-animated")
	}
	if info.Height != 10 {
		t.Fatalf("da_1 height = %d, want the first frame height 10", info.Height)
	}
	if got := colorOfVips(img, 5, 5); got != (color.RGBA{R: 255, A: 255}) {
		t.Fatalf("da_1 = %v, want frame 0 (red)", got)
	}
}

func colorOfVips(img image.Image, x, y int) color.RGBA {
	r, g, b, a := img.At(x, y).RGBA()
	return color.RGBA{R: uint8(r >> 8), G: uint8(g >> 8), B: uint8(b >> 8), A: uint8(a >> 8)}
}
