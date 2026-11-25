// Ukur biaya transformasi libvips in-process (bukan CLI), sebagai pembanding
// pipeline image crate (Rust) dan x/image/draw (Go).
package main

import (
	"fmt"
	"os"
	"time"

	"github.com/davidbyttow/govips/v2/vips"
)

func main() {
	path := "../poc/data/src/photo00.jpg"
	if len(os.Args) > 1 {
		path = os.Args[1]
	}
	src, err := os.ReadFile(path)
	if err != nil {
		panic(err)
	}
	vips.Startup(nil)
	defer vips.Shutdown()
	fmt.Printf("sumber: %s (%d KiB) libvips %s\n", path, len(src)/1024, vips.Version)

	type job struct {
		label string
		w, h  int
		webp  bool
	}
	jobs := []job{
		{"vips w400 jpeg q80", 400, 267, false},
		{"vips w800 jpeg q80", 800, 533, false},
		{"vips w1600 jpeg q85", 1600, 1067, false},
		{"vips w400 webp q80", 400, 267, true},
	}
	// Jalur shrink-on-load: decode + resize dalam satu operasi (setara
	// vips_thumbnail_buffer yang dipakai benchmark Rust).
	for _, j := range jobs {
		if j.webp {
			continue
		}
		runThumb := func() (int, error) {
			img, err := vips.NewThumbnailFromBuffer(src, j.w, j.h, vips.InterestingNone)
			if err != nil {
				return 0, err
			}
			defer img.Close()
			out, _, err := img.ExportJpeg(&vips.JpegExportParams{Quality: 80, StripMetadata: true})
			return len(out), err
		}
		if _, err := runThumb(); err != nil {
			fmt.Println("thumbnail_buffer tidak tersedia:", err)
			continue
		}
		const n = 20
		t := time.Now()
		var sz int
		for i := 0; i < n; i++ {
			sz, err = runThumb()
			if err != nil {
				panic(err)
			}
		}
		ms := float64(time.Since(t).Microseconds()) / 1000.0 / n
		fmt.Printf("%-24s %7.2f ms  out=%5d KiB  %6.1f img/s/core\n", "govips thumb-buf w"+fmt.Sprint(j.w), ms, sz/1024, 1000.0/ms)
	}

	for _, j := range jobs {
		run := func() (int, error) {
			img, err := vips.NewImageFromBuffer(src)
			if err != nil {
				return 0, err
			}
			defer img.Close()
			if err := img.ThumbnailWithSize(j.w, j.h, vips.InterestingNone, vips.SizeDown); err != nil {
				return 0, err
			}
			var out []byte
			if j.webp {
				out, _, err = img.ExportWebp(&vips.WebpExportParams{Quality: 80, StripMetadata: true})
			} else {
				out, _, err = img.ExportJpeg(&vips.JpegExportParams{Quality: 80, StripMetadata: true})
			}
			return len(out), err
		}
		if _, err := run(); err != nil {
			panic(err)
		}
		const n = 10
		t := time.Now()
		var sz int
		for i := 0; i < n; i++ {
			sz, err = run()
			if err != nil {
				panic(err)
			}
		}
		ms := float64(time.Since(t).Microseconds()) / 1000.0 / n
		fmt.Printf("%-24s %7.1f ms  out=%5d KiB  %6.1f img/s/core\n", j.label, ms, sz/1024, 1000.0/ms)
	}
}
