package ops

import "testing"

func TestBestFormatAliasesAndCanonical(t *testing.T) {
	want := ""
	for _, raw := range []string{"w_100,f_best", "w_100,f_bf", "w_100,f_best_format"} {
		o, err := Parse(raw, nil)
		if err != nil {
			t.Fatalf("%s: %v", raw, err)
		}
		if o.Format != FormatBest {
			t.Fatalf("%s: format = %q, want best", raw, o.Format)
		}
		if got, expected := o.Canonical(), "c_fit,f_best,g_center,strip_1,w_100"; got != expected {
			t.Fatalf("%s: canonical = %q, want %q", raw, got, expected)
		}
		if want == "" {
			want = o.Canonical()
		} else if o.Canonical() != want {
			t.Fatalf("%s: canonical differs from alias: %q vs %q", raw, o.Canonical(), want)
		}
	}
}

func TestBestFormatConflicts(t *testing.T) {
	for _, raw := range []string{
		"w_100,f_jpeg,f_best",
		"w_100,f_best,f_webp",
		"w_100,f_best,f_auto",
	} {
		if _, err := Parse(raw, nil); err == nil {
			t.Fatalf("%s should be rejected", raw)
		}
	}
	// f_auto before f_best is just the default being replaced.
	if _, err := Parse("w_100,f_auto,f_best", nil); err != nil {
		t.Fatalf("f_auto,f_best: %v", err)
	}
}

func TestKeepMetadataCanonical(t *testing.T) {
	o, err := Parse("w_100,sm_0", nil)
	if err != nil {
		t.Fatal(err)
	}
	if !o.KeepMetadata {
		t.Fatal("sm_0 must set KeepMetadata")
	}
	if got, want := o.Canonical(), "c_fit,f_auto,g_center,strip_0,w_100"; got != want {
		t.Fatalf("canonical = %q, want %q", got, want)
	}
	plain, _ := Parse("w_100,sm_1", nil)
	if plain.KeepMetadata {
		t.Fatal("sm_1 must keep metadata stripping on")
	}
	if plain.Canonical() != "c_fit,f_auto,g_center,strip_1,w_100" {
		t.Fatalf("sm_1 canonical = %q", plain.Canonical())
	}
}
