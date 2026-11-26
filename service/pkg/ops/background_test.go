package ops

import (
	"strings"
	"testing"
)

func TestParseBackgroundForms(t *testing.T) {
	cases := []struct {
		raw  string
		want string
	}{
		{"w_100,bg_ff8800", "ff8800"},
		{"w_100,background_FF880080", "ff880080"},
		{"w_100,bg_255:136:0", "ff8800"},
		{"w_100,bg_255:136:0:128", "ff880080"},
		{"w_100,bg_#ff8800", "ff8800"},
		{"w_100,bg_transparent", "transparent"},
		{"w_100,bg_0000ff00", "0000ff00"},
	}
	for _, tc := range cases {
		o, err := Parse(tc.raw, nil)
		if err != nil {
			t.Fatalf("%s: %v", tc.raw, err)
		}
		if o.Background != tc.want {
			t.Errorf("%s: Background = %q, want %q", tc.raw, o.Background, tc.want)
		}
		if !strings.Contains(o.Canonical(), "bg_"+tc.want) {
			t.Errorf("%s: canonical %q missing bg_%s", tc.raw, o.Canonical(), tc.want)
		}
	}

	// The hex and decimal forms must share one cache key.
	hexForm, _ := Parse("w_100,bg_ff8800", nil)
	decForm, _ := Parse("w_100,bg_255:136:0", nil)
	if hexForm.Canonical() != decForm.Canonical() {
		t.Fatalf("bg forms canonical differ: %q vs %q", hexForm.Canonical(), decForm.Canonical())
	}

	for _, bad := range []string{
		"w_100,bg_red", "w_100,bg_12345", "w_100,bg_1234567",
		"w_100,bg_256:0:0", "w_100,bg_-1:0:0", "w_100,bg_1:2",
		"w_100,bg_ff00zz", "w_100,bg_ff0000:0",
	} {
		if _, err := Parse(bad, nil); err == nil {
			t.Errorf("%s: expected error", bad)
		}
	}
}

func TestParseBackgroundAlpha(t *testing.T) {
	o, err := Parse("w_100,bg_ff0000,bga_0.5", nil)
	if err != nil {
		t.Fatal(err)
	}
	if o.BackgroundAlpha != 0.5 {
		t.Fatalf("BackgroundAlpha = %v, want 0.5", o.BackgroundAlpha)
	}
	if !strings.Contains(o.Canonical(), "bga_0.5") {
		t.Fatalf("canonical %q missing bga_0.5", o.Canonical())
	}

	o, err = Parse("w_100,bg_ff0000,background_alpha_0", nil)
	if err != nil {
		t.Fatal(err)
	}
	if o.BackgroundAlpha != 0 || !strings.Contains(o.Canonical(), "bga_0") {
		t.Fatalf("bga_0 parsed as %+v, canonical %q", o, o.Canonical())
	}

	// 1 is the default and must not split the cache key.
	o, err = Parse("w_100,bg_ff0000,bga_1", nil)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(o.Canonical(), "bga_") {
		t.Fatalf("bga_1 leaked into canonical: %q", o.Canonical())
	}

	for _, bad := range []string{"w_100,bga_1.1", "w_100,bga_-0.1", "w_100,bga_abc"} {
		if _, err := Parse(bad, nil); err == nil {
			t.Errorf("%s: expected error", bad)
		}
	}
}

func TestParseExtend(t *testing.T) {
	o, err := Parse("w_100,h_80,ex_1", nil)
	if err != nil {
		t.Fatal(err)
	}
	if !o.Extend || o.ExtendGravity != GravityCenter {
		t.Fatalf("extend parsed as %+v", o)
	}
	if !strings.Contains(o.Canonical(), "ex_1:center") {
		t.Fatalf("canonical %q missing ex_1:center", o.Canonical())
	}

	aliases := []string{
		"w_100,h_80,extend_1:ce",
		"w_100,h_80,ex_1:center",
	}
	for _, raw := range aliases {
		a, err := Parse(raw, nil)
		if err != nil {
			t.Fatalf("%s: %v", raw, err)
		}
		if a.Canonical() != o.Canonical() {
			t.Errorf("%s: canonical %q, want %q", raw, a.Canonical(), o.Canonical())
		}
	}

	o, err = Parse("w_100,h_80,ex_1:noea", nil)
	if err != nil {
		t.Fatal(err)
	}
	if o.ExtendGravity != GravityNorthEast || !strings.Contains(o.Canonical(), "ex_1:north_east") {
		t.Fatalf("extend gravity parsed as %+v, canonical %q", o, o.Canonical())
	}

	o, err = Parse("w_100,h_80,ex_0", nil)
	if err != nil {
		t.Fatal(err)
	}
	if o.Extend || strings.Contains(o.Canonical(), "ex_") {
		t.Fatalf("ex_0 leaked: %+v %q", o, o.Canonical())
	}

	for _, bad := range []string{
		"w_100,h_80,ex_2", "w_100,h_80,ex_1:auto", "w_100,h_80,ex_1:face",
		"w_100,h_80,ex_1:sideways", "f_png,ex_1",
	} {
		if _, err := Parse(bad, nil); err == nil {
			t.Errorf("%s: expected error", bad)
		}
	}
}

func TestParseFlatten(t *testing.T) {
	o, err := Parse("w_100,fl_1", nil)
	if err != nil {
		t.Fatal(err)
	}
	if !o.Flatten || !strings.Contains(o.Canonical(), "fl_1") {
		t.Fatalf("flatten parsed as %+v, canonical %q", o, o.Canonical())
	}

	alias, err := Parse("w_100,flatten_1", nil)
	if err != nil {
		t.Fatal(err)
	}
	if alias.Canonical() != o.Canonical() {
		t.Fatalf("flatten alias canonical differs: %q vs %q", alias.Canonical(), o.Canonical())
	}

	o, err = Parse("w_100,fl_0", nil)
	if err != nil {
		t.Fatal(err)
	}
	if o.Flatten || strings.Contains(o.Canonical(), "fl_") {
		t.Fatalf("fl_0 leaked: %+v %q", o, o.Canonical())
	}

	if _, err := Parse("w_100,fl_2", nil); err == nil {
		t.Error("fl_2: expected error")
	}
}
