//go:build !libavif

package imaging

import (
	"errors"

	vips "github.com/davidbyttow/govips/v2/vips"
)

// libavifEncoder reports whether the libavif path is compiled in.
const libavifEncoder = false

func encodeAVIFLibavif(*vips.ImageRef, int, bool) ([]byte, error) {
	return nil, errors.New("avif: libavif support is not compiled in")
}

func encodeAVIFLibavifSubsample(*vips.ImageRef, int, bool, *int) ([]byte, bool, error) {
	return nil, false, errors.New("avif: libavif support is not compiled in")
}
