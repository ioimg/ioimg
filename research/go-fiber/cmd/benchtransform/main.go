// Ukur biaya CPU transformasi Go: decode → resize → encode.
package main

import (
	"fmt"
	"os"
	"time"

	"github.com/ioimg/ioimg/research/go-fiber/internal/ioimg"
)

func main() {
	path := "../../artifacts/datasets/src/photo00.jpg"
	if len(os.Args) > 1 {
		path = os.Args[1]
	}
	src, err := os.ReadFile(path)
	if err != nil {
		panic(err)
	}
	fmt.Printf("sumber: %s (%d KiB)\n", path, len(src)/1024)

	// decode-only
	{
		t := time.Now()
		const n = 5
		for i := 0; i < n; i++ {
			if _, err := ioimg.Transform(src, ioimg.Ops{Format: ioimg.Jpeg, Quality: 80}, ioimg.CatmullRom); err != nil {
				panic(err)
			}
		}
		_ = t
	}
	cases := []struct {
		label string
		ops   ioimg.Ops
		flt   ioimg.Filter
	}{
		{"w400 jpeg q80 catmullrom", ioimg.Ops{W: 400, Format: ioimg.Jpeg, Quality: 80}, ioimg.CatmullRom},
		{"w400 jpeg q80 bilinear", ioimg.Ops{W: 400, Format: ioimg.Jpeg, Quality: 80}, ioimg.ApproxBiLinear},
		{"w800 jpeg q80 catmullrom", ioimg.Ops{W: 800, Format: ioimg.Jpeg, Quality: 80}, ioimg.CatmullRom},
		{"w1600 jpeg q85 catmullrom", ioimg.Ops{W: 1600, Format: ioimg.Jpeg, Quality: 85}, ioimg.CatmullRom},
		{"w400,h300 cover png", ioimg.Ops{W: 400, H: 300, Fit: ioimg.Cover, Format: ioimg.Png}, ioimg.CatmullRom},
	}
	for _, c := range cases {
		// warmup
		if _, err := ioimg.Transform(src, c.ops, c.flt); err != nil {
			panic(err)
		}
		const iters = 10
		t := time.Now()
		var out []byte
		for i := 0; i < iters; i++ {
			out, err = ioimg.Transform(src, c.ops, c.flt)
			if err != nil {
				panic(err)
			}
		}
		ms := float64(time.Since(t).Microseconds()) / 1000.0 / iters
		fmt.Printf("%-28s %7.1f ms  out=%5d KiB  %6.1f img/s/core\n", c.label, ms, len(out)/1024, 1000.0/ms)
	}
}
