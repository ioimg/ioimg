//go:build !vips

package imaging

import (
	"github.com/ioimg/ioimg/service/pkg/ops"
)

// DefaultProcessor returns the pure-Go pipeline.
func DefaultProcessor() Processor { return pureProcessor{} }

type pureProcessor struct{}

func (pureProcessor) Process(src []byte, o ops.Ops, opts Options) (Result, error) {
	img, info, err := Decode(src)
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
