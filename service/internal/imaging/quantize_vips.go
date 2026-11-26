//go:build vips

package imaging

/*
#cgo LDFLAGS: -limagequant
#include <libimagequant.h>
#include <stddef.h>
#include <string.h>

// quantize_rgba runs the same libimagequant quantiser that libvips' PNG
// palette writer uses. govips 2.20 exposes the palette flag but not
// liq_set_max_colors, so we call the library directly to honour the requested
// colour ceiling instead of always quantising to 256 colours.
static int quantize_rgba(const unsigned char *rgba, int width, int height,
                         int max_colors, unsigned char *indices,
                         unsigned char *palette, int *palette_count) {
	liq_attr *attr = liq_attr_create();
	if (!attr) {
		return 1;
	}
	if (liq_set_max_colors(attr, max_colors) != LIQ_OK) {
		liq_attr_destroy(attr);
		return 2;
	}
	liq_image *image = liq_image_create_rgba(attr, rgba, width, height, 0.0);
	if (!image) {
		liq_attr_destroy(attr);
		return 3;
	}
	liq_result *result = NULL;
	liq_error err = liq_image_quantize(image, attr, &result);
	if (err != LIQ_OK) {
		liq_image_destroy(image);
		liq_attr_destroy(attr);
		return 4;
	}
	// The palette is already the requested size, so dithering would only
	// introduce colours outside the quantised set.
	liq_set_dithering_level(result, 0.0f);
	err = liq_write_remapped_image(result, image, indices,
	                               (size_t)width * (size_t)height);
	if (err != LIQ_OK) {
		liq_result_destroy(result);
		liq_image_destroy(image);
		liq_attr_destroy(attr);
		return 5;
	}
	const liq_palette *pal = liq_get_palette(result);
	*palette_count = (int)pal->count;
	memcpy(palette, pal->entries, (size_t)pal->count * sizeof(liq_color));
	liq_result_destroy(result);
	liq_image_destroy(image);
	liq_attr_destroy(attr);
	return 0;
}
*/
import "C"

import (
	"errors"
	"fmt"
	"image"
	"image/color"
	"image/draw"
	"unsafe"

	vips "github.com/davidbyttow/govips/v2/vips"
)

// quantizeVipsPNG reduces a vips image to a palette of at most colors entries
// using libimagequant. The result is a Go paletted image; exportAt hands it to
// libvips' PNG palette writer so the file is a real indexed PNG.
func quantizeVipsPNG(img *vips.ImageRef, colors int) (*image.Paletted, error) {
	src, err := img.ToGoImage()
	if err != nil {
		return nil, fmt.Errorf("vips pngo quantization: %w", err)
	}
	b := src.Bounds()
	w, h := b.Dx(), b.Dy()
	if w == 0 || h == 0 {
		return nil, errors.New("vips pngo quantization: empty image")
	}
	// libimagequant wants tightly packed RGBA rows. ToGoImage usually already
	// gives NRGBA, but normalise the rectangle and stride defensively.
	rgba := image.NewNRGBA(image.Rect(0, 0, w, h))
	draw.Draw(rgba, rgba.Bounds(), src, b.Min, draw.Src)

	idx := make([]byte, w*h)
	palette := make([]byte, 256*4)
	var count C.int
	res := C.quantize_rgba(
		(*C.uchar)(unsafe.Pointer(&rgba.Pix[0])),
		C.int(w), C.int(h), C.int(colors),
		(*C.uchar)(unsafe.Pointer(&idx[0])),
		(*C.uchar)(unsafe.Pointer(&palette[0])),
		&count,
	)
	if res != 0 {
		return nil, fmt.Errorf("vips pngo quantization: libimagequant failed (code %d)", int(res))
	}
	if count < 1 || int(count) > colors {
		return nil, fmt.Errorf("vips pngo quantization: libimagequant returned %d colours for a %d-colour limit", int(count), colors)
	}
	pal := make(color.Palette, int(count))
	for i := range pal {
		pal[i] = color.NRGBA{
			R: palette[i*4],
			G: palette[i*4+1],
			B: palette[i*4+2],
			A: palette[i*4+3],
		}
	}
	out := image.NewPaletted(image.Rect(0, 0, w, h), pal)
	copy(out.Pix, idx)
	return out, nil
}
