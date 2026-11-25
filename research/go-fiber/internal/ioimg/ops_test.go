package ioimg

import (
	"os"
	"testing"
)

func TestParseOps(t *testing.T) {
	o, err := ParseOps("w_800,h_600,fit_cover,f_png,q_75")
	if err != nil {
		t.Fatal(err)
	}
	if o.W != 800 || o.H != 600 || o.Fit != Cover || o.Format != Png || o.Quality != 75 {
		t.Fatalf("hasil salah: %+v", o)
	}
}

func TestCanonicalOrderIndependent(t *testing.T) {
	a, _ := ParseOps("w_800,f_png")
	b, _ := ParseOps("f_png,w_800")
	if CacheKey(a, "x") != CacheKey(b, "x") {
		t.Fatal("cache key bergantung urutan")
	}
}

func TestRejectsBadOps(t *testing.T) {
	for _, s := range []string{"zzz_1", "f_gif", "", "w_abc"} {
		if _, err := ParseOps(s); err == nil {
			t.Fatalf("harus ditolak: %q", s)
		}
	}
}

func TestTransformProducesBytes(t *testing.T) {
	src, err := osReadTestdata()
	if err != nil {
		t.Skip("tanpa testdata")
	}
	out, err := Transform(src, Ops{W: 64, Format: Jpeg, Quality: 70}, CatmullRom)
	if err != nil {
		t.Fatal(err)
	}
	if len(out) == 0 {
		t.Fatal("output kosong")
	}
}

func osReadTestdata() ([]byte, error) {
	return os.ReadFile("testdata/tiny.jpg")
}
