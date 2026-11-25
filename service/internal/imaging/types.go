package imaging

import (
	"errors"

	"github.com/ioimg/ioimg/service/pkg/ops"
)

// ErrDecode is returned when the payload is not a supported image.
var ErrDecode = errors.New("decode_failed")

// ErrDimension is returned when the image exceeds the configured pixel limit.
var ErrDimension = errors.New("dimension_limit_exceeded")

// Info describes the decoded source.
type Info struct {
	Width    int
	Height   int
	Format   string
	HasAlpha bool
	Animated bool
	Bytes    int
}

// Options controls the transform pipeline.
type Options struct {
	OutFormat ops.Format
	Quality   int
}

// Result is one processed image (or its metadata when f_json is requested).
type Result struct {
	Data          []byte
	Info          Info
	DominantColor string
	Timings       map[string]float64
}

// Processor is the image pipeline implemented by the pure-Go backend
// (default) or the libvips backend (`-tags vips`).
type Processor interface {
	Process(src []byte, o ops.Ops, opts Options) (Result, error)
}

// MaxPixels is the hard decode limit (40 MP, matching docs/API.md).
const MaxPixels = 40_000_000

// MaxSide is the hard side limit.
const MaxSide = 8192

// ContentType maps an output format to its media type.
func ContentType(f ops.Format) string {
	switch f {
	case ops.FormatAVIF:
		return "image/avif"
	case ops.FormatWebP:
		return "image/webp"
	case ops.FormatPNG:
		return "image/png"
	case ops.FormatGIF:
		return "image/gif"
	case ops.FormatJSON:
		return "application/json"
	default:
		return "image/jpeg"
	}
}
