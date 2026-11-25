//go:build !vips

package imaging

import (
	"bytes"
	"image"

	"github.com/gen2brain/avif"
	"github.com/rwcarlsen/goexif/exif"
)

// encodeAVIF lives in its own file so the heavy WASM encoder is easy to find
// and to replace with a native libavif binding in a future build tag.
func encodeAVIF(buf *bytes.Buffer, img image.Image, quality int, subsample image.YCbCrSubsampleRatio) error {
	return avif.Encode(buf, img, avif.Options{
		Quality:           quality,
		Speed:             9, // 0..10; 9 keeps latency reasonable for an on-demand proxy
		ChromaSubsampling: subsample,
	})
}

// exifOrientation extracts the EXIF orientation tag, defaulting to 1.
func exifOrientation(data []byte) int {
	x, err := exif.Decode(bytes.NewReader(data))
	if err != nil {
		return 1
	}
	tag, err := x.Get(exif.Orientation)
	if err != nil {
		return 1
	}
	v, err := tag.Int(0)
	if err != nil || v < 1 || v > 8 {
		return 1
	}
	return v
}

// applyOrientation bakes EXIF orientation into pixels. Metadata is stripped
// later by the Go encoders, so this must happen before any other geometry op.
func applyOrientation(img image.Image, orientation int) image.Image {
	switch orientation {
	case 2:
		return flip(img, "h")
	case 3:
		return rotate(img, 180)
	case 4:
		return flip(img, "v")
	case 5:
		return flip(rotate(img, 90), "h")
	case 6:
		return rotate(img, 90)
	case 7:
		return flip(rotate(img, 270), "h")
	case 8:
		return rotate(img, 270)
	default:
		return img
	}
}
