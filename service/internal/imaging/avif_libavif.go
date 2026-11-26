//go:build vips && libavif

package imaging

/*
#cgo pkg-config: libavif
#include <avif/avif.h>
#include <stdlib.h>
#include <string.h>

// encodeAVIF turns an 8-bit RGBA buffer into an AVIF still image with libavif.
// libvips' heifsave hides libaom's cpu-used behind a coarse "effort" knob, which
// costs ~4x the encode time for the same bytes; libavif exposes it as `speed`.
static avifResult encodeAVIF(const uint8_t *rgba, int width, int height,
                             int speed, int quality, int threads,
                             uint8_t **out, size_t *outLen) {
	avifImage *img = avifImageCreate(width, height, 8, AVIF_PIXEL_FORMAT_YUV444);
	if (!img) {
		return AVIF_RESULT_OUT_OF_MEMORY;
	}

	avifRGBImage src;
	avifRGBImageSetDefaults(&src, img);
	src.width = width;
	src.height = height;
	src.depth = 8;
	src.format = AVIF_RGB_FORMAT_RGBA;
	src.pixels = (uint8_t *)rgba;
	src.rowBytes = width * 4;

	avifResult r = avifImageRGBToYUV(img, &src);
	if (r != AVIF_RESULT_OK) {
		avifImageDestroy(img);
		return r;
	}

	avifEncoder *e = avifEncoderCreate();
	if (!e) {
		avifImageDestroy(img);
		return AVIF_RESULT_OUT_OF_MEMORY;
	}
	e->speed = speed;
	e->quality = quality;
	e->qualityAlpha = quality;
	e->maxThreads = threads;
	e->minQuantizer = AVIF_QUANTIZER_BEST_QUALITY;
	e->maxQuantizer = AVIF_QUANTIZER_WORST_QUALITY;
	e->minQuantizerAlpha = AVIF_QUANTIZER_BEST_QUALITY;
	e->maxQuantizerAlpha = AVIF_QUANTIZER_WORST_QUALITY;

	avifRWData data = AVIF_DATA_EMPTY;
	r = avifEncoderAddImage(e, img, 1, AVIF_ADD_IMAGE_FLAG_SINGLE);
	if (r == AVIF_RESULT_OK) {
		r = avifEncoderFinish(e, &data);
	}

	avifEncoderDestroy(e);
	avifImageDestroy(img);
	if (r != AVIF_RESULT_OK) {
		avifRWDataFree(&data);
		return r;
	}

	*out = malloc(data.size);
	if (!*out) {
		avifRWDataFree(&data);
		return AVIF_RESULT_OUT_OF_MEMORY;
	}
	memcpy(*out, data.data, data.size);
	*outLen = data.size;
	avifRWDataFree(&data);
	return AVIF_RESULT_OK;
}
*/
import "C"

import (
	"errors"
	"fmt"
	"image"
	"os"
	"strconv"
	"strings"
	"sync"
	"unsafe"

	vips "github.com/davidbyttow/govips/v2/vips"
)

// libavifEncoder reports whether the libavif path is compiled in.
const libavifEncoder = true

// avifSpeedLevel is libaom's cpu-used knob (0 slowest .. 10 fastest) for the
// libavif path. Measured on a 512x340 thumbnail at q65: speed 8 = 47 ms /
// 37.7 KB, speed 9 = 12.7 ms / 38.2 KB. Default 9 mirrors imgproxy.
var (
	avifSpeedOnce sync.Once
	avifSpeed     int
)

func avifSpeedLevel() int {
	avifSpeedOnce.Do(func() {
		avifSpeed = 9
		if raw := strings.TrimSpace(os.Getenv("IOIMG_AVIF_SPEED")); raw != "" {
			if n, err := strconv.Atoi(raw); err == nil && n >= 0 && n <= 10 {
				avifSpeed = n
			}
		}
	})
	return avifSpeed
}

// encodeAVIFLibavif encodes the image as AVIF with libavif + libaom.
func encodeAVIFLibavif(img *vips.ImageRef, quality int) ([]byte, error) {
	goImg, err := img.ToGoImage()
	if err != nil {
		return nil, fmt.Errorf("avif: %w", err)
	}
	rgba, ok := goImg.(*image.NRGBA)
	if !ok {
		return nil, errors.New("avif: unexpected image type")
	}
	if rgba.Rect.Dx() == 0 || rgba.Rect.Dy() == 0 || len(rgba.Pix) == 0 {
		return nil, errors.New("avif: empty image")
	}

	var out *C.uint8_t
	var outLen C.size_t
	res := C.encodeAVIF(
		(*C.uint8_t)(unsafe.Pointer(&rgba.Pix[0])),
		C.int(rgba.Rect.Dx()), C.int(rgba.Rect.Dy()),
		C.int(avifSpeedLevel()), C.int(quality), C.int(1),
		&out, &outLen,
	)
	if res != C.AVIF_RESULT_OK {
		return nil, fmt.Errorf("avif: encode failed (avifResult %d)", int(res))
	}
	defer C.free(unsafe.Pointer(out))
	return C.GoBytes(unsafe.Pointer(out), C.int(outLen)), nil
}
