package ops

import "testing"

func TestParseDuotone(t *testing.T) {
	cases := []struct {
		in   string
		want Duotone
	}{
		{"0.5", Duotone{Intensity: 0.5, Color1: RGB{0, 0, 0}, Color2: RGB{0xff, 0xff, 0xff}}},
		{"1:f00:0f0", Duotone{Intensity: 1, Color1: RGB{0xff, 0, 0}, Color2: RGB{0, 0xff, 0}}},
		{":#112233:#445566", Duotone{Intensity: 0, Color1: RGB{0x11, 0x22, 0x33}, Color2: RGB{0x44, 0x55, 0x66}}},
		{"0.25:abc", Duotone{Intensity: 0.25, Color1: RGB{0xaa, 0xbb, 0xcc}, Color2: RGB{0xff, 0xff, 0xff}}},
	}
	for _, c := range cases {
		o, err := Parse("w_10,dt_"+c.in, nil)
		if err != nil {
			t.Fatalf("dt_%s: %v", c.in, err)
		}
		if o.Duotone != c.want {
			t.Fatalf("dt_%s = %+v, want %+v", c.in, o.Duotone, c.want)
		}
	}
	for _, bad := range []string{"w_10,dt_2", "w_10,dt_-1", "w_10,dt_1:zzz", "w_10,dt_1:1:2:3"} {
		if _, err := Parse(bad, nil); err == nil {
			t.Fatalf("%s: expected error", bad)
		}
	}
}

func TestParseColorize(t *testing.T) {
	cases := []struct {
		in   string
		want Colorize
	}{
		{"1", Colorize{Opacity: 1, Color: RGB{}, KeepAlpha: false}},
		{"1:ff0000:1", Colorize{Opacity: 1, Color: RGB{0xff, 0, 0}, KeepAlpha: true}},
		{"0.3:00f", Colorize{Opacity: 0.3, Color: RGB{0, 0, 0xff}}},
		{"0.5:123456:true", Colorize{Opacity: 0.5, Color: RGB{0x12, 0x34, 0x56}, KeepAlpha: true}},
	}
	for _, c := range cases {
		o, err := Parse("w_10,col_"+c.in, nil)
		if err != nil {
			t.Fatalf("col_%s: %v", c.in, err)
		}
		if o.Colorize != c.want {
			t.Fatalf("col_%s = %+v, want %+v", c.in, o.Colorize, c.want)
		}
	}
	for _, bad := range []string{"w_10,col_1.5", "w_10,col_1:xyz", "w_10,col_1:000:2"} {
		if _, err := Parse(bad, nil); err == nil {
			t.Fatalf("%s: expected error", bad)
		}
	}
}

func TestColorEffectsCanonical(t *testing.T) {
	o, err := Parse("w_10,dt_0.5:c00:0c0,col_0.25:00f:1", nil)
	if err != nil {
		t.Fatal(err)
	}
	want := "c_fit,col_0.25:0000ff:1,dt_0.5:cc0000:00cc00,f_auto,g_center,strip_1,w_10"
	if got := o.Canonical(); got != want {
		t.Fatalf("canonical = %q, want %q", got, want)
	}
	// Zero values must not perturb keys that predate the feature.
	plain, _ := Parse("w_10", nil)
	if plain.Canonical() != "c_fit,f_auto,g_center,strip_1,w_10" {
		t.Fatalf("plain canonical changed: %q", plain.Canonical())
	}
}
