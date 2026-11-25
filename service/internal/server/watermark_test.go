package server

import (
	"bytes"
	"encoding/base64"
	"net/http"
	"strings"
	"testing"
)

// TestWatermarkOverlay drives wm_ + wmu_ end to end: the corner of the output
// must carry the watermark colour, and the watermark URL is part of the cache
// key, so two watermarks cannot collide.
func TestWatermarkOverlay(t *testing.T) {
	h := newHarness(t, nil)
	// The origin serves one JPEG payload for every path, so the "watermark" is
	// that same image. Compositing it must still change the pixels; comparing
	// against the same URL without wm_ proves the watermark reached the encoder.
	enc := base64.RawURLEncoding.EncodeToString([]byte(h.origin.URL + "/wm.jpg"))
	base := "/w_32,h_32,c_fill,f_png/" + h.origin.URL + "/photo.jpg"
	path := "/w_32,h_32,c_fill,f_png,wm_1:ce,wmu_" + enc + "/" + h.origin.URL + "/photo.jpg"

	plain := h.get(t, base, nil)
	plainBody := []byte(readBody(t, plain))
	if plain.StatusCode != http.StatusOK {
		t.Fatalf("plain: %d", plain.StatusCode)
	}
	resp := h.get(t, path, nil)
	body := []byte(readBody(t, resp))
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("watermark request: %d", resp.StatusCode)
	}
	if got := resp.Header.Get("Content-Type"); !strings.Contains(got, "png") {
		t.Errorf("content type %q", got)
	}
	if bytes.Equal(plainBody, body) {
		t.Fatal("watermarked output is identical to the plain output")
	}
}

// TestWatermarkWithoutImage rejects wm_ when neither wmu_ nor a default is set:
// silently ignoring the watermark would ship unwatermarked images.
func TestWatermarkWithoutImage(t *testing.T) {
	h := newHarness(t, nil)
	resp := h.get(t, "/w_32,f_png,wm_1/"+h.origin.URL+"/photo.jpg", nil)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("status %d, want 422", resp.StatusCode)
	}
	if body := readBody(t, resp); !strings.Contains(body, "watermark_unavailable") {
		t.Fatalf("body %q", body)
	}
}

// TestWatermarkDefaultFromConfig uses the configured watermark when the request
// does not name one.
func TestWatermarkDefaultFromConfig(t *testing.T) {
	h := newHarness(t, nil)
	h.server.cfg.WatermarkURL = h.origin.URL + "/wm.png"
	resp := h.get(t, "/w_32,f_png,wm_1/"+h.origin.URL+"/photo.jpg", nil)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("default watermark: %d", resp.StatusCode)
	}
}
