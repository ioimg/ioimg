//go:build !vips

package imaging

import (
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
	res := Result{Info: info}
	if opts.OutFormat == ops.FormatJSON {
		res.DominantColor = DominantColor(img)
		return res, nil
	}
	out, timings, err := Transform(img, o, opts)
	if err != nil {
		return Result{}, err
	}
	res.Data = out
	res.Timings = timings
	return res, nil
}
