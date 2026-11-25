package ops

import (
	"encoding/base64"
	"strings"
	"testing"
)

func TestParsePNGEncoder(t *testing.T) {
	cases := []struct {
		raw   string
		canon string
	}{
		{"pngo_1", "pngo_1::"},
		{"pngo_1:1:64", "pngo_1:1:64"},
		{"pngo_::32", "pngo_::32"},
	}
	for _, c := range cases {
		raw := "w_100,f_png," + c.raw
		o, err := Parse(raw, nil)
		if err != nil {
			t.Fatalf("%s: %v", raw, err)
		}
		if got := lastCanonical(o, "pngo"); got != c.canon {
			t.Errorf("%s: pngo canonical %q, want %q (full %q)", raw, got, c.canon, o.Canonical())
		}
	}
	for _, bad := range []string{"pngo_2", "pngo_1:1:1", "pngo_1:1:257"} {
		if _, err := Parse("w_100,f_png,"+bad, nil); err == nil {
			t.Errorf("%s: expected error", bad)
		}
	}
}

func TestParseWebPEncoder(t *testing.T) {
	o, err := Parse("w_100,f_webp,webpo_6:1:2", nil)
	if err != nil {
		t.Fatal(err)
	}
	if o.WebPEncoder == nil || o.WebPEncoder.Compression == nil || *o.WebPEncoder.Compression != 6 {
		t.Fatalf("compression not parsed: %+v", o.WebPEncoder)
	}
	if !contains(o.Canonical(), "webpo_6:1:2") {
		t.Fatalf("canonical %q", o.Canonical())
	}
	for _, bad := range []string{"webpo_7", "webpo_-1", "webpo_1:2", "webpo_1:1:6"} {
		if _, err := Parse("w_100,f_webp,"+bad, nil); err == nil {
			t.Errorf("%s: expected error", bad)
		}
	}
}

func TestParseAVIFEncoder(t *testing.T) {
	o, err := Parse("w_100,f_avif,avifo_0", nil)
	if err != nil {
		t.Fatal(err)
	}
	if o.AVIFEncoder == nil || o.AVIFEncoder.Subsample == nil || *o.AVIFEncoder.Subsample != 0 {
		t.Fatalf("subsample not parsed: %+v", o.AVIFEncoder)
	}
	if !contains(o.Canonical(), "avifo_0") {
		t.Fatalf("canonical %q", o.Canonical())
	}
	for _, bad := range []string{"avifo_3", "avifo_-1", "avifo_1:2"} {
		if _, err := Parse("w_100,f_avif,"+bad, nil); err == nil {
			t.Errorf("%s: expected error", bad)
		}
	}
}

func TestParseGradient(t *testing.T) {
	o, err := Parse("w_100,gr_0.5:ff0000:down:0.2:0.8", nil)
	if err != nil {
		t.Fatal(err)
	}
	g := o.Gradient
	if g.Opacity != 0.5 || g.Color.Hex() != "ff0000" || g.Direction != "down" || g.Start != 0.2 || g.Stop != 0.8 {
		t.Fatalf("gradient parsed as %+v", g)
	}
	if !contains(o.Canonical(), "gr_0.5:ff0000:down:0.2:0.8") {
		t.Fatalf("canonical %q", o.Canonical())
	}

	// Logos and rotations are equivalent directions.
	for raw, want := range map[string]float64{
		"gr_1::up":    180,
		"gr_1::right": 270,
		"gr_1::left":  90,
		"gr_1:::0":    0,
		"gr_1::90":    90,
		"gr_1::450":   90,
	} {
		o, err := Parse("w_100,"+raw, nil)
		if err != nil {
			t.Fatalf("%s: %v", raw, err)
		}
		if got := o.Gradient.Angle(); got != want {
			t.Errorf("%s: angle %v, want %v", raw, got, want)
		}
	}

	// Opacity 0 is off and must not change the canonical form.
	o, err = Parse("w_100,gr_0:ff0000", nil)
	if err != nil {
		t.Fatal(err)
	}
	if !o.Gradient.IsZero() {
		t.Fatal("gr_0 should be a no-op")
	}
	if contains(o.Canonical(), "gr_") {
		t.Fatalf("gr_0 leaked into canonical: %q", o.Canonical())
	}

	for _, bad := range []string{"gr_2", "gr_1:zzz", "gr_1::sideways", "gr_1:::0.8:0.2", "gr_1:::2"} {
		if _, err := Parse("w_100,"+bad, nil); err == nil {
			t.Errorf("%s: expected error", bad)
		}
	}
}

// TestEncoderOverrideIsSigned pins that encoder overrides and the gradient are
// part of both the cache key and the signature: they change the bytes.
func TestEncoderOverrideIsSigned(t *testing.T) {
	o, err := Parse("w_100,f_webp,webpo_6,gr_0.5:000000", nil)
	if err != nil {
		t.Fatal(err)
	}
	if o.SigningCanonical() != o.Canonical() {
		t.Fatalf("signing canonical diverged: %q vs %q", o.SigningCanonical(), o.Canonical())
	}
	if !contains(o.Canonical(), "webpo_6::") || !contains(o.Canonical(), "gr_") {
		t.Fatalf("canonical %q", o.Canonical())
	}
}

func lastCanonical(o Ops, key string) string {
	parts := splitComma(o.Canonical())
	for _, p := range parts {
		if hasPrefix(p, key+"_") {
			return p
		}
	}
	return ""
}

func splitComma(s string) []string {
	var out []string
	cur := ""
	for _, r := range s {
		if r == ',' {
			out = append(out, cur)
			cur = ""
			continue
		}
		cur += string(r)
	}
	return append(out, cur)
}

func hasPrefix(s, p string) bool { return len(s) >= len(p) && s[:len(p)] == p }

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}

func TestParseHashsum(t *testing.T) {
	sum := "a" + strings.Repeat("b", 63)
	o, err := Parse("w_100,hs_sha256:"+sum, nil)
	if err != nil {
		t.Fatal(err)
	}
	if o.Hashsum.Type != "sha256" || o.Hashsum.Hex != sum {
		t.Fatalf("hashsum parsed as %+v", o.Hashsum)
	}
	if !contains(o.Canonical(), "hs_sha256:"+sum) {
		t.Fatalf("canonical %q", o.Canonical())
	}

	// type none means "no verification" and must not change the canonical form.
	o, err = Parse("w_100,hs_none::", nil)
	if err != nil {
		t.Fatal(err)
	}
	if !o.Hashsum.IsZero() || contains(o.Canonical(), "hs_") {
		t.Fatalf("hs_none leaked: %+v %q", o.Hashsum, o.Canonical())
	}

	for _, bad := range []string{"hs_sha256:abc", "hs_sha256:" + strings.Repeat("z", 64), "hs_crc32:abcd", "hs_md5"} {
		if _, err := Parse("w_100,"+bad, nil); err == nil {
			t.Errorf("%s: expected error", bad)
		}
	}
}

func TestParseFallbackURL(t *testing.T) {
	target := "https://cdn.example.com/fallback.jpg?x=1"
	enc := base64.RawURLEncoding.EncodeToString([]byte(target))
	o, err := Parse("w_100,fiu_"+enc, nil)
	if err != nil {
		t.Fatal(err)
	}
	if o.FallbackURL != target {
		t.Fatalf("fallback %q, want %q", o.FallbackURL, target)
	}
	if !contains(o.Canonical(), "fiu_"+enc) {
		t.Fatalf("canonical %q", o.Canonical())
	}
	// Round-trip: the canonical form must parse back to the same URL.
	again, err := Parse("w_100,"+lastCanonical(o, "fiu"), nil)
	if err != nil || again.FallbackURL != target {
		t.Fatalf("round-trip: %v %q", err, again.FallbackURL)
	}

	for _, bad := range []string{"fiu_!!!", "fiu_aGVsbG8"} {
		if _, err := Parse("w_100,"+bad, nil); err == nil {
			t.Errorf("%s: expected error", bad)
		}
	}
}

func TestParseDPI(t *testing.T) {
	o, err := Parse("w_100,dpi_144", nil)
	if err != nil {
		t.Fatal(err)
	}
	if o.DPI != 144 {
		t.Fatalf("dpi = %v", o.DPI)
	}
	if !contains(o.Canonical(), "dpi_144") {
		t.Fatalf("canonical %q", o.Canonical())
	}
	// dpi_0 means "leave metadata alone" and must not split the cache.
	o, err = Parse("w_100,dpi_0", nil)
	if err != nil {
		t.Fatal(err)
	}
	if o.DPI != 0 || contains(o.Canonical(), "dpi_") {
		t.Fatalf("dpi_0 leaked: %+v %q", o.DPI, o.Canonical())
	}
	for _, bad := range []string{"dpi_-1", "dpi_abc", "dpi_100001"} {
		if _, err := Parse("w_100,"+bad, nil); err == nil {
			t.Errorf("%s: expected error", bad)
		}
	}
}
