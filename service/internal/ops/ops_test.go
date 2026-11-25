package ops

import (
	"strings"
	"testing"
)

func TestParseCanonicalOrderIndependent(t *testing.T) {
	a, err := Parse("w_800,h_600,c_fill,g_north,f_webp,q_75", nil)
	if err != nil {
		t.Fatal(err)
	}
	b, err := Parse("q_75,f_webp,g_north,c_fill,h_600,w_800", nil)
	if err != nil {
		t.Fatal(err)
	}
	if a.Canonical() != b.Canonical() {
		t.Fatalf("canonical differs:\n%s\n%s", a.Canonical(), b.Canonical())
	}
	if a.EffectiveWidth() != 800 || a.EffectiveHeight() != 600 {
		t.Fatalf("unexpected dims: %d x %d", a.EffectiveWidth(), a.EffectiveHeight())
	}
}

func TestParseFiltersAndCanonical(t *testing.T) {
	a, err := Parse("w_100,bri_20,con_-10,sat_15,px_8,mono_1,f_webp", nil)
	if err != nil {
		t.Fatal(err)
	}
	b, err := Parse("mono_1,px_8,sat_15,con_-10,bri_20,f_webp,w_100", nil)
	if err != nil {
		t.Fatal(err)
	}
	if a.Canonical() != b.Canonical() {
		t.Fatalf("canonical differs:\n%s\n%s", a.Canonical(), b.Canonical())
	}
	for _, want := range []string{"bri_20", "con_-10", "sat_15", "pixel_8", "mono_1"} {
		if !strings.Contains(a.Canonical(), want) {
			t.Fatalf("canonical %q missing %q", a.Canonical(), want)
		}
	}
}

func TestParseRejectsBadInput(t *testing.T) {
	cases := []string{
		"", "w", "w_0", "w_9000", "w_auto", "f_tiff", "c_unknown", "q_101",
		"dpr_4", "bg_red", "rot_45", "flip_x", "trim_99", "unknown_1",
		"bri_101", "con_-101", "sat_101", "pixel_0", "mono_2",
	}
	for _, c := range cases {
		if _, err := Parse(c, nil); err == nil {
			t.Errorf("Parse(%q) should fail", c)
		}
	}
}

func TestPresetExpansionAndOverride(t *testing.T) {
	presets := map[string]string{"thumb": "h_320,c_fill,f_auto,q_auto"}
	o, err := Parse("t_thumb,w_200", presets)
	if err != nil {
		t.Fatal(err)
	}
	if o.Height != 320 || o.Width != 200 || o.Resize != ResizeFill {
		t.Fatalf("unexpected ops: %+v", o)
	}
	if !strings.Contains(o.Canonical(), "h_320") {
		t.Fatalf("canonical missing preset value: %s", o.Canonical())
	}
}

func TestDPRClamp(t *testing.T) {
	o, err := Parse("w_8000,dpr_2", nil)
	if err != nil {
		t.Fatal(err)
	}
	if o.EffectiveWidth() != MaxDimension {
		t.Fatalf("want clamp to %d, got %d", MaxDimension, o.EffectiveWidth())
	}
	if !o.Clamped() {
		t.Fatal("expected Clamped")
	}
}

func TestNegotiate(t *testing.T) {
	avail := map[Format]bool{FormatAVIF: true, FormatWebP: true, FormatJPEG: true, FormatPNG: true}
	if got := Negotiate("image/avif,image/webp,image/jpeg", avail, false); got != FormatAVIF {
		t.Fatalf("want avif, got %s", got)
	}
	if got := Negotiate("image/webp;q=1,image/avif;q=0.5", avail, false); got != FormatWebP {
		t.Fatalf("want webp, got %s", got)
	}
	if got := Negotiate("image/png", avail, false); got != FormatPNG {
		t.Fatalf("want png, got %s", got)
	}
	if got := Negotiate("text/html", avail, false); got != FormatJPEG {
		t.Fatalf("want jpeg fallback, got %s", got)
	}
}
