package ops

import (
	"encoding/base64"
	"math"
	"testing"
)

func TestParseWatermarkTextAndOptions(t *testing.T) {
	enc := base64.RawURLEncoding.EncodeToString([]byte("Hello ioimg"))
	o, err := Parse("w_100,wmt_"+enc+",wms_40:0,wmr_-90,wmsh_2.5,watermark_text_"+enc, nil)
	if err != nil {
		t.Fatal(err)
	}
	if o.WatermarkText != "Hello ioimg" {
		t.Fatalf("text %q", o.WatermarkText)
	}
	if o.WatermarkSize == nil || o.WatermarkSize.Width != 40 || o.WatermarkSize.Height != 0 {
		t.Fatalf("size %+v", o.WatermarkSize)
	}
	if o.WatermarkRotate != 270 {
		t.Fatalf("rotate %v, want 270", o.WatermarkRotate)
	}
	if o.WatermarkShadow != 2.5 {
		t.Fatalf("shadow %v", o.WatermarkShadow)
	}
	// wmt_ alone activates the watermark, and every option is canonical.
	if o.Watermark == nil || o.Watermark.Size != o.WatermarkSize || o.Watermark.Rotate != 270 || o.Watermark.Shadow != 2.5 {
		t.Fatalf("watermark %+v", o.Watermark)
	}
	for _, want := range []string{"wmt_" + enc, "wms_40:0", "wmr_270", "wmsh_2.5"} {
		if !contains(o.Canonical(), want) {
			t.Errorf("canonical %q missing %q", o.Canonical(), want)
		}
	}

	// Long names and the wm_rot alias work in the comma form.
	o, err = Parse("w_100,watermark_size_10:20,wm_rot_45,watermark_shadow_1.5", nil)
	if err != nil {
		t.Fatal(err)
	}
	if o.WatermarkSize == nil || o.WatermarkSize.Width != 10 || o.WatermarkSize.Height != 20 ||
		o.WatermarkRotate != 45 || o.WatermarkShadow != 1.5 {
		t.Fatalf("long names: %+v", o)
	}

	// An explicit wm_0 disables the implicit text watermark.
	o, err = Parse("w_100,wm_0,wmt_"+enc, nil)
	if err != nil {
		t.Fatal(err)
	}
	if o.Watermark != nil {
		t.Fatalf("wm_0 must disable the text watermark: %+v", o.Watermark)
	}
}

func TestParseWatermarkTextInvalid(t *testing.T) {
	for _, raw := range []string{
		"w_100,wmt_!!!!",
		"w_100,watermark_text_abc$",
	} {
		if _, err := Parse(raw, nil); err == nil {
			t.Errorf("%s: expected base64 error", raw)
		}
	}
}

func TestParseWatermarkSizeInvalid(t *testing.T) {
	for _, raw := range []string{
		"w_100,wms_0:0",
		"w_100,wms_-1:20",
		"w_100,wms_10:-1",
		"w_100,wms_10",
		"w_100,wms_10:20:30",
		"w_100,wms_a:20",
	} {
		if _, err := Parse(raw, nil); err == nil {
			t.Errorf("%s: expected size error", raw)
		}
	}
}

func TestParseWatermarkRotateInvalid(t *testing.T) {
	for _, raw := range []string{
		"w_100,wmr_nope",
		"w_100,wmr_NaN",
		"w_100,wmr_Inf",
		"w_100,wmr_-Inf",
	} {
		if _, err := Parse(raw, nil); err == nil {
			t.Errorf("%s: expected rotate error", raw)
		}
	}
	// 360 is normalised onto the canonical circle rather than stored as-is.
	if o, err := Parse("w_100,wmr_360", nil); err != nil || o.WatermarkRotate != 0 || contains(o.Canonical(), "wmr_") {
		t.Fatalf("360: %+v %v %q", o, err, o.Canonical())
	}
}

func TestParseWatermarkShadowInvalid(t *testing.T) {
	for _, raw := range []string{
		"w_100,wmsh_-1",
		"w_100,wmsh_NaN",
		"w_100,wmsh_Inf",
		"w_100,wmsh_x",
	} {
		if _, err := Parse(raw, nil); err == nil {
			t.Errorf("%s: expected shadow error", raw)
		}
	}
	if o, err := Parse("w_100,wmsh_0", nil); err != nil || o.WatermarkShadow != 0 || math.Signbit(o.WatermarkShadow) {
		t.Fatalf("shadow 0: %+v %v", o, err)
	}
}
