package ops

import (
	"reflect"
	"strings"
	"testing"
)

func TestParseSkipProcessing(t *testing.T) {
	cases := []struct {
		raw  string
		want []string
	}{
		{"f_jpeg,skp_jpg", []string{"jpg"}},
		{"f_jpeg,skp_jpg:png", []string{"jpg", "png"}},
		{"f_jpeg,skip_processing_png:jpeg", []string{"jpeg", "png"}},
		{"f_jpeg,skp_jpg,png", []string{"jpg", "png"}},     // comma continuation
		{"f_jpeg,skp_png:jpg:png", []string{"jpg", "png"}}, // deduplicated
	}
	for _, tc := range cases {
		o, err := Parse(tc.raw, nil)
		if err != nil {
			t.Fatalf("Parse(%q): %v", tc.raw, err)
		}
		if !reflect.DeepEqual(o.SkipProcessing, tc.want) {
			t.Fatalf("Parse(%q).SkipProcessing = %v, want %v", tc.raw, o.SkipProcessing, tc.want)
		}
	}
	// The extension order must not change the canonical form.
	a, _ := Parse("f_jpeg,skp_png:jpg", nil)
	b, _ := Parse("f_jpeg,skp_jpg:png", nil)
	if a.Canonical() != b.Canonical() {
		t.Fatalf("skip list order changed canonical: %q vs %q", a.Canonical(), b.Canonical())
	}
}

func TestParseSkipProcessingRejectsInvalid(t *testing.T) {
	for _, raw := range []string{
		"skp_", "skp_tiff", "skp_jpg:bmp", "skip_processing_svg", "f_jpeg,skp_",
	} {
		if _, err := Parse(raw, nil); err == nil {
			t.Errorf("Parse(%q) should fail", raw)
		}
	}
}

func TestParseRawBool(t *testing.T) {
	for _, raw := range []string{"1", "t", "true", "TRUE"} {
		o, err := Parse("raw_"+raw, nil)
		if err != nil {
			t.Fatalf("Parse(raw_%s): %v", raw, err)
		}
		if !o.Raw {
			t.Fatalf("raw_%s did not set Raw", raw)
		}
		if !strings.Contains(o.Canonical(), "raw_1") {
			t.Fatalf("canonical missing raw_1: %q", o.Canonical())
		}
	}
	for _, raw := range []string{"0", "f", "false"} {
		o, err := Parse("w_10,f_jpeg,raw_"+raw, nil)
		if err != nil {
			t.Fatalf("Parse(raw_%s): %v", raw, err)
		}
		if o.Raw {
			t.Fatalf("raw_%s set Raw", raw)
		}
		if strings.Contains(o.Canonical(), "raw") {
			t.Fatalf("raw_%s leaked into canonical: %q", raw, o.Canonical())
		}
	}
	for _, raw := range []string{"raw_2", "raw_yes", "raw_truefalse"} {
		if _, err := Parse(raw, nil); err == nil {
			t.Errorf("Parse(%q) should fail", raw)
		}
	}
}

func TestCanonicalDiffersForPassthrough(t *testing.T) {
	base, err := Parse("w_100,f_jpeg", nil)
	if err != nil {
		t.Fatal(err)
	}
	skip, err := Parse("w_100,f_jpeg,skp_jpg", nil)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := Parse("w_100,f_jpeg,raw_1", nil)
	if err != nil {
		t.Fatal(err)
	}
	keys := map[string]string{
		"base": base.Canonical(),
		"skip": skip.Canonical(),
		"raw":  raw.Canonical(),
	}
	if keys["base"] == keys["skip"] || keys["base"] == keys["raw"] || keys["skip"] == keys["raw"] {
		t.Fatalf("canonical/cache keys collide: %+v", keys)
	}
	if !strings.Contains(keys["skip"], "skp_jpg") || !strings.Contains(keys["raw"], "raw_1") {
		t.Fatalf("passthrough options missing from canonical: %+v", keys)
	}
	if base.SigningCanonical() == skip.SigningCanonical() || base.SigningCanonical() == raw.SigningCanonical() {
		t.Fatal("passthrough options missing from signing canonical")
	}
}
