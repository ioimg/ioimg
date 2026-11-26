//go:build !vips

package imaging

import (
	"image"

	"github.com/ioimg/ioimg/service/pkg/ops"
)

// DefaultProcessor returns the pure-Go pipeline.
func DefaultProcessor() Processor { return pureProcessor{} }

// Backend reports the active imaging pipeline. The pure-Go fallback is several
// times slower per transform than libvips, so startup warns when it is linked.
func Backend() string { return "go" }

type pureProcessor struct{}

func (pureProcessor) Process(src []byte, o ops.Ops, opts Options) (Result, error) {
	img, info, err := DecodeWithOptions(src, o)
	if err != nil {
		return Result{}, err
	}
	res := Result{Info: info, Format: opts.OutFormat}
	if opts.OutFormat == ops.FormatJSON {
		res.DominantColor = DominantColor(img)
		return res, nil
	}
	if o.KeepMetadata {
		opts.SourceMetadata = src
	}
	out, chosen, timings, err := transformResult(img, o, opts)
	if err != nil {
		return Result{}, err
	}
	res.Data = out
	res.Format = chosen
	res.Timings = timings
	if chosen != opts.OutFormat {
		res.Info.Format = string(chosen)
	}
	return res, nil
}

// DecodeAny decodes an image with whichever backend is linked. It exists for
// auxiliary images (watermarks) that must not assume the transform pipeline.
func DecodeAny(data []byte) (image.Image, Info, error) { return decode(data, true, ops.Ops{}) }
