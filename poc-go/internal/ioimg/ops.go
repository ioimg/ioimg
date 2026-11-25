// Package ioimg berisi logika inti PoC: parsing ops, cache key, transformasi.
// Sengaja tidak menyentuh HTTP supaya bisa dipakai Fiber maupun net/http,
// dan diuji sendiri.
package ioimg

import (
	"fmt"
	"strconv"
	"strings"
)

type Format uint8

const (
	Jpeg Format = iota
	Png
)

func ParseFormat(s string) (Format, bool) {
	switch s {
	case "jpeg", "jpg":
		return Jpeg, true
	case "png":
		return Png, true
	}
	return Jpeg, false
}

func (f Format) Ext() string {
	if f == Png {
		return "png"
	}
	return "jpg"
}

func (f Format) ContentType() string {
	if f == Png {
		return "image/png"
	}
	return "image/jpeg"
}

type Fit uint8

const (
	Inside Fit = iota
	Cover
)

type Ops struct {
	W, H    int // 0 = tidak di-set
	Fit     Fit
	Format  Format
	Quality int
}

func DefaultOps() Ops { return Ops{Format: Jpeg, Quality: 80} }

// Parse format path: w_800,h_600,fit_cover,f_jpeg,q_75
func ParseOps(s string) (Ops, error) {
	o := DefaultOps()
	seenFormat := false
	for _, part := range strings.Split(s, ",") {
		if part == "" {
			continue
		}
		k, v, ok := strings.Cut(part, "_")
		if !ok {
			return o, fmt.Errorf("ops tidak valid: %q (harus k_v)", part)
		}
		switch k {
		case "w":
			n, err := strconv.Atoi(v)
			if err != nil {
				return o, fmt.Errorf("w harus angka")
			}
			o.W = n
		case "h":
			n, err := strconv.Atoi(v)
			if err != nil {
				return o, fmt.Errorf("h harus angka")
			}
			o.H = n
		case "q":
			n, err := strconv.Atoi(v)
			if err != nil {
				return o, fmt.Errorf("q harus angka")
			}
			o.Quality = clamp(n, 1, 100)
		case "fit":
			switch v {
			case "inside":
				o.Fit = Inside
			case "cover":
				o.Fit = Cover
			default:
				return o, fmt.Errorf("fit harus inside|cover")
			}
		case "f":
			f, ok := ParseFormat(v)
			if !ok {
				return o, fmt.Errorf("format tidak dikenal: %s", v)
			}
			o.Format = f
			seenFormat = true
		default:
			return o, fmt.Errorf("ops tidak dikenal: %s", k)
		}
	}
	if o.W == 0 && o.H == 0 && !seenFormat {
		return o, fmt.Errorf("minimal satu operasi (w/h/f)")
	}
	if o.W < 0 || o.H < 0 || o.W == 0 && o.H == 0 && seenFormat && o.W < 0 {
		return o, fmt.Errorf("w/h tidak boleh negatif")
	}
	if o.W == 0 && o.H == 0 && seenFormat {
		// ukuran asli, format dikonversi — sah
		return o, nil
	}
	return o, nil
}

func (o Ops) Canonical() string {
	return fmt.Sprintf("w=%d;h=%d;fit=%d;f=%s;q=%d", o.W, o.H, o.Fit, o.Format.Ext(), o.Quality)
}

func clamp(v, lo, hi int) int {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}
