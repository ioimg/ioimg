//go:build vips && libavif

package imaging

import (
	"bytes"
	"testing"

	vips "github.com/davidbyttow/govips/v2/vips"
	"github.com/ioimg/ioimg/service/pkg/ops"
)

// TestAvifLibavifEncode checks the libavif path emits a real AVIF that libvips
// can decode back at the expected size. The encoder is asm/SIMD-heavy and
// silently produres garbage when the pixel layout is wrong, so the round trip
// matters more than the byte count.
func TestAvifLibavifEncode(t *testing.T) {
	ensureVips()
	src := vipsSizedJPEG(t, 64, 48)

	img, err := vips.NewImageFromBuffer(src)
	if err != nil {
		t.Fatal(err)
	}
	defer img.Close()

	out, err := encodeAVIFLibavif(img, 60, false)
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	if len(out) < 32 || !bytes.Contains(out[:32], []byte("ftyp")) {
		t.Fatalf("not an AVIF container: % x", out[:min(len(out), 32)])
	}

	check, err := vips.NewImageFromBuffer(out)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	defer check.Close()
	if check.Width() != img.Width() || check.Height() != img.Height() {
		t.Fatalf("size = %dx%d, want %dx%d", check.Width(), check.Height(), img.Width(), img.Height())
	}
	if got := check.Format(); got != vips.ImageTypeAVIF {
		t.Fatalf("format = %v, want AVIF", got)
	}
}

// TestAvifEncoderSelection documents the switch: libavif by default, libvips
// when asked, and the override is case/space insensitive.
func TestAvifEncoderSelection(t *testing.T) {
	cases := []struct {
		env  string
		want bool
	}{
		{"", true},
		{"auto", true},
		{"libavif", true},
		{"vips", false},
		{" libVIPS ", false},
	}
	for _, c := range cases {
		t.Setenv("IOIMG_AVIF_ENCODER", c.env)
		if got := avifUsesLibavif(); got != c.want {
			t.Errorf("IOIMG_AVIF_ENCODER=%q -> %v, want %v", c.env, got, c.want)
		}
	}
}

// TestAvifEndToEndUsable asserts the default pipeline (no IOIMG_* set) produces
// AVIF through libavif, so a deployment cannot silently fall back to the slow
// saver.
func TestAvifEndToEndUsable(t *testing.T) {
	ensureVips()
	t.Setenv("IOIMG_AVIF_ENCODER", "")
	t.Setenv("IOIMG_AVIF_SPEED", "")

	o, err := ops.Parse("w_32,f_avif,q_60", nil)
	if err != nil {
		t.Fatal(err)
	}
	res, err := DefaultProcessor().Process(vipsSizedJPEG(t, 64, 48), o, Options{OutFormat: ops.FormatAVIF, Quality: 60})
	if err != nil {
		t.Fatalf("process: %v", err)
	}
	if len(res.Data) == 0 {
		t.Fatal("no output")
	}
	check, err := vips.NewImageFromBuffer(res.Data)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	defer check.Close()
	if got := check.Format(); got != vips.ImageTypeAVIF {
		t.Fatalf("format = %v, want AVIF", got)
	}
}

// TestAvifLibavifSubsampling pins that avifo_subsample reaches libavif: the
// three YUV formats produce different bytes, and the default stays 4:4:4.
func TestAvifLibavifSubsampling(t *testing.T) {
	ensureVips()
	t.Setenv("IOIMG_AVIF_ENCODER", "")
	t.Setenv("IOIMG_AVIF_SPEED", "")
	src := deterministicSource(t)

	encode := func(raw string) []byte {
		t.Helper()
		o, err := ops.Parse(raw, nil)
		if err != nil {
			t.Fatalf("%s: %v", raw, err)
		}
		res, err := DefaultProcessor().Process(src, o, Options{OutFormat: ops.FormatAVIF, Quality: 60})
		if err != nil {
			t.Fatalf("%s: %v", raw, err)
		}
		return res.Data
	}

	defaultOut := encode("w_64,h_48,c_fill,f_avif,q_60")
	out444 := encode("w_64,h_48,c_fill,f_avif,q_60,avifo_0")
	out422 := encode("w_64,h_48,c_fill,f_avif,q_60,avifo_1")
	out420 := encode("w_64,h_48,c_fill,f_avif,q_60,avifo_2")

	if !bytes.Equal(defaultOut, out444) {
		t.Fatal("default AVIF is not 4:4:4")
	}
	if bytes.Equal(out444, out422) || bytes.Equal(out444, out420) || bytes.Equal(out422, out420) {
		t.Fatalf("subsampling formats produced identical bytes: 444=%d 422=%d 420=%d",
			len(out444), len(out422), len(out420))
	}
}

// TestAvifLibavifOddSizeFallback pins the documented degradation: 4:2:0 on an
// odd-sized image falls back to 4:4:4, succeeds, and records the fallback.
func TestAvifLibavifOddSizeFallback(t *testing.T) {
	ensureVips()
	t.Setenv("IOIMG_AVIF_ENCODER", "")
	src := deterministicSource(t)
	process := func(raw string) Result {
		t.Helper()
		o, err := ops.Parse(raw, nil)
		if err != nil {
			t.Fatalf("%s: %v", raw, err)
		}
		res, err := DefaultProcessor().Process(src, o, Options{OutFormat: ops.FormatAVIF, Quality: 60})
		if err != nil {
			t.Fatalf("%s: %v", raw, err)
		}
		return res
	}

	fallback := process("w_63,h_47,c_fill,f_avif,q_60,avifo_2")
	if got := fallback.Timings["avif_subsample_fallback"]; got != 1 {
		t.Fatalf("fallback timing = %v, want 1", got)
	}
	explicit444 := process("w_63,h_47,c_fill,f_avif,q_60,avifo_0")
	if !bytes.Equal(fallback.Data, explicit444.Data) {
		t.Fatal("odd-size 4:2:0 fallback did not encode as 4:4:4")
	}
}
