//go:build vips

package imaging

import (
	"testing"

	"github.com/davidbyttow/govips/v2/vips"

	"github.com/ioimg/ioimg/service/pkg/ops"
)

func TestVipsColorizeAndDuotone(t *testing.T) {
	src := vipsSizedJPEG(t, 40, 30)

	for _, tc := range []struct {
		raw string
		// expected dominant channel after the effect
		wantR, wantG, wantB int
	}{
		{"w_20,col_1:ff0000:0", 255, 0, 0},
		{"w_20,col_1:0000ff:0", 0, 0, 255},
		{"w_20,dt_1:000000:0000ff", 0, 0, 94},
	} {
		o, err := ops.Parse(tc.raw, nil)
		if err != nil {
			t.Fatalf("%s: parse: %v", tc.raw, err)
		}
		res, err := DefaultProcessor().Process(src, o, Options{OutFormat: ops.FormatPNG})
		if err != nil {
			t.Fatalf("%s: process: %v", tc.raw, err)
		}
		img, err := vips.NewImageFromBuffer(res.Data)
		if err != nil {
			t.Fatalf("%s: reopen: %v", tc.raw, err)
		}
		goImg, err := img.ToImage(&vips.ExportParams{})
		img.Close()
		if err != nil {
			t.Fatalf("%s: to image: %v", tc.raw, err)
		}
		r, g, b, _ := goImg.At(goImg.Bounds().Dx()/2, goImg.Bounds().Dy()/2).RGBA()
		if int(r>>8) < tc.wantR-8 || int(r>>8) > tc.wantR+8 ||
			int(b>>8) < tc.wantB-8 || int(b>>8) > tc.wantB+8 ||
			int(g>>8) < tc.wantG-8 || int(g>>8) > tc.wantG+8 {
			t.Fatalf("%s: pixel = %d,%d,%d want ~%d,%d,%d", tc.raw, r>>8, g>>8, b>>8, tc.wantR, tc.wantG, tc.wantB)
		}
	}
}
