package ops

import "testing"

func TestMinDimensionsCanonical(t *testing.T) {
	o, err := Parse("w_100,mw_200,mh_300", nil)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if o.MinWidth != 200 || o.MinHeight != 300 {
		t.Fatalf("min dims: %+v", o)
	}
	if got, want := o.Canonical(), "c_fit,mw_200,mh_300,f_auto,g_center,strip_1,w_100"; got != want {
		t.Fatalf("canonical = %q, want %q", got, want)
	}
	// Order must not matter.
	other, err := Parse("mh_300,mw_200,w_100", nil)
	if err != nil {
		t.Fatalf("parse reordered: %v", err)
	}
	if other.Canonical() != o.Canonical() {
		t.Fatalf("canonical unstable: %q vs %q", other.Canonical(), o.Canonical())
	}
}

func TestMinDimensionsRejected(t *testing.T) {
	for _, raw := range []string{"mw_0", "mh_0", "mw_-1", "mw_abc", "mh_9000"} {
		if _, err := Parse(raw, nil); err == nil {
			t.Fatalf("%q should be rejected", raw)
		}
	}
}

func TestAutoRotateDefaultAndCanonical(t *testing.T) {
	def, err := Parse("w_100", nil)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if !def.AutoRotates() {
		t.Fatal("auto-rotate must default to true")
	}
	// The default must not enter the canonical form: existing cache keys and
	// signatures stay stable.
	if got := def.Canonical(); got != "c_fit,f_auto,g_center,strip_1,w_100" {
		t.Fatalf("canonical with default ar = %q", got)
	}
	off, err := Parse("w_100,ar_0", nil)
	if err != nil {
		t.Fatalf("parse ar_0: %v", err)
	}
	if off.AutoRotates() {
		t.Fatal("ar_0 must disable auto-rotate")
	}
	if got, want := off.Canonical(), "c_fit,ar_0,f_auto,g_center,strip_1,w_100"; got != want {
		t.Fatalf("canonical = %q, want %q", got, want)
	}
	// ar_1 is the default and normalises away.
	on, err := Parse("w_100,ar_1", nil)
	if err != nil {
		t.Fatalf("parse ar_1: %v", err)
	}
	if on.Canonical() != def.Canonical() {
		t.Fatalf("ar_1 should normalise to the default: %q", on.Canonical())
	}
	if _, err := Parse("ar_2", nil); err == nil {
		t.Fatal("ar_2 must be rejected")
	}
}

func TestStripMetadataAlias(t *testing.T) {
	if _, err := Parse("w_100,sb_0", nil); err == nil {
		t.Fatal("sb is not a valid op")
	}
	long, err := Parse("w_100,strip_0", nil)
	if err != nil {
		t.Fatalf("parse strip_0: %v", err)
	}
	short, err := Parse("w_100,sm_0", nil)
	if err != nil {
		t.Fatalf("parse sm_0: %v", err)
	}
	if long.Canonical() != short.Canonical() {
		t.Fatalf("sm/sm alias mismatch: %q vs %q", long.Canonical(), short.Canonical())
	}
}
