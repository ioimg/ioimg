package imaging

import (
	"bytes"
	"image"
	"image/color"
	"image/draw"
	"image/gif"
	"testing"
)

// animatedGIF builds three full-canvas frames: red, green, blue. The solid
// colours make page selection and vertical stacking easy to assert.
func animatedGIF(t *testing.T) []byte {
	t.Helper()
	const w, h = 20, 10
	palette := color.Palette{
		color.RGBA{R: 255, A: 255},
		color.RGBA{G: 255, A: 255},
		color.RGBA{B: 255, A: 255},
	}
	g := &gif.GIF{Config: image.Config{Width: w, Height: h, ColorModel: palette}}
	for i := range palette {
		frame := image.NewPaletted(image.Rect(0, 0, w, h), palette)
		draw.Draw(frame, frame.Bounds(), &image.Uniform{C: palette[i]}, image.Point{}, draw.Src)
		g.Image = append(g.Image, frame)
		g.Delay = append(g.Delay, 10)
	}
	var buf bytes.Buffer
	if err := gif.EncodeAll(&buf, g); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}
