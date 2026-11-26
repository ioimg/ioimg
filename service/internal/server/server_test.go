package server

import (
	"bytes"
	"encoding/json"
	"fmt"
	"image"
	"image/color"
	"image/gif"
	"image/jpeg"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ioimg/ioimg/service/internal/config"
	"github.com/ioimg/ioimg/service/pkg/ops"
	"github.com/ioimg/ioimg/service/pkg/sign"
)

func jpegBytes(t *testing.T) []byte {
	t.Helper()
	img := image.NewNRGBA(image.Rect(0, 0, 64, 48))
	for y := 0; y < 48; y++ {
		for x := 0; x < 64; x++ {
			img.Set(x, y, color.NRGBA{R: uint8(x * 4), G: uint8(y * 5), B: 100, A: 255})
		}
	}
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, img, &jpeg.Options{Quality: 90}); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

type harness struct {
	origin *httptest.Server
	srv    *httptest.Server
	server *Server
	hits   atomic.Int64
	cfg    config.Config
	signer *sign.Signer
}

func newHarness(t *testing.T, mutate func(*config.Config)) *harness {
	return newHarnessOpts(t, mutate, nil)
}

func newHarnessOpts(t *testing.T, mutate func(*config.Config), opts func(*Options)) *harness {
	t.Helper()
	payload := jpegBytes(t)
	h := &harness{}
	h.origin = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h.hits.Add(1)
		if r.URL.Path == "/missing.jpg" {
			http.NotFound(w, r)
			return
		}
		if r.URL.Path == "/bomb.png" {
			w.Header().Set("Content-Type", "image/png")
			_, _ = w.Write(bombPNGHeader(100_000, 100_000))
			return
		}
		w.Header().Set("Content-Type", "image/jpeg")
		_, _ = w.Write(payload)
	}))
	t.Cleanup(h.origin.Close)

	keys, _ := sign.ParseKeys("test:secret")
	h.signer = &sign.Signer{Keys: keys, IncludeExp: true}
	h.cfg = config.Config{
		AllowAnyHost:    true,
		AllowPrivate:    true,
		SourceTimeout:   5 * time.Second,
		SourceMaxBytes:  1 << 20,
		SourceUserAgent: "ioimg-test",
		MaxDimension:    ops.MaxDimension,
		Concurrency:     4,
		CacheDir:        t.TempDir(),
		CacheMaxBytes:   8 << 20,
		Presets:         map[string]string{"thumb": "h_32,c_fill,f_auto,q_auto"},
		Signing:         h.signer,
	}
	if mutate != nil {
		mutate(&h.cfg)
	}
	o := Options{Logger: slog.New(slog.NewTextHandler(io.Discard, nil))}
	if opts != nil {
		opts(&o)
	}
	srv, err := New(h.cfg, o)
	if err != nil {
		t.Fatal(err)
	}
	h.server = srv
	h.srv = httptest.NewServer(srv.Handler())
	t.Cleanup(h.srv.Close)
	return h
}

func (h *harness) get(t *testing.T, path string, headers map[string]string) *http.Response {
	t.Helper()
	req, err := http.NewRequest(http.MethodGet, h.srv.URL+path, nil)
	if err != nil {
		t.Fatal(err)
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	return resp
}

func (h *harness) signed(t *testing.T, opsRaw, source string, exp time.Time) string {
	t.Helper()
	o, err := ops.Parse(opsRaw, h.cfg.Presets)
	if err != nil {
		t.Fatal(err)
	}
	canonical, err := sign.CanonicalSource(source)
	if err != nil {
		t.Fatal(err)
	}
	sig, err := h.signer.Sign(o.Canonical(), canonical, exp.Unix())
	if err != nil {
		t.Fatal(err)
	}
	u := "/" + sig + "/" + o.Canonical() + "/" + canonical
	if !exp.IsZero() {
		sep := "?"
		if strings.Contains(canonical, "?") {
			sep = "&"
		}
		u += fmt.Sprintf("%sexp=%d", sep, exp.Unix())
	}
	return u
}

func decodeErr(t *testing.T, resp *http.Response) (int, string) {
	t.Helper()
	defer resp.Body.Close()
	var body struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("response is not JSON: %v", err)
	}
	return resp.StatusCode, body.Error.Code
}

func TestOpenPathCacheAndETag(t *testing.T) {
	h := newHarness(t, nil)
	source := h.origin.URL + "/photo.jpg"

	resp := h.get(t, "/w_40,f_jpeg/"+source, nil)
	if resp.StatusCode != 200 {
		t.Fatalf("want 200, got %d", resp.StatusCode)
	}
	if ct := resp.Header.Get("Content-Type"); ct != "image/jpeg" {
		t.Fatalf("unexpected content type: %s", ct)
	}
	if got := resp.Header.Get("X-IOIMG-Cache"); got != "MISS" {
		t.Fatalf("want MISS, got %s", got)
	}
	etag := resp.Header.Get("ETag")
	if etag == "" {
		t.Fatal("missing ETag")
	}
	_, _ = io.Copy(io.Discard, resp.Body)
	resp.Body.Close()

	resp = h.get(t, "/w_40,f_jpeg/"+source, map[string]string{"If-None-Match": etag})
	if resp.StatusCode != http.StatusNotModified {
		t.Fatalf("want 304, got %d", resp.StatusCode)
	}
	resp.Body.Close()

	// Different Accept must not poison the jpeg cache entry.
	resp = h.get(t, "/w_40,f_jpeg/"+source, map[string]string{"Accept": "image/webp"})
	if resp.StatusCode != 200 || resp.Header.Get("Content-Type") != "image/jpeg" {
		t.Fatalf("explicit format must ignore Accept: %d %s", resp.StatusCode, resp.Header.Get("Content-Type"))
	}
	resp.Body.Close()
}

func TestAutoFormatAndPreset(t *testing.T) {
	h := newHarness(t, nil)
	source := h.origin.URL + "/photo.jpg"

	resp := h.get(t, "/w_40,f_auto/"+source, map[string]string{"Accept": "image/webp,image/jpeg"})
	if resp.StatusCode != 200 {
		t.Fatalf("want 200, got %d", resp.StatusCode)
	}
	if ct := resp.Header.Get("Content-Type"); ct != "image/webp" {
		t.Fatalf("want webp, got %s", ct)
	}
	if v := resp.Header.Get("Vary"); v != "Accept" {
		t.Fatalf("want Vary: Accept, got %q", v)
	}
	resp.Body.Close()

	resp = h.get(t, "/t_thumb/"+source, nil)
	if resp.StatusCode != 200 {
		t.Fatalf("preset failed: %d", resp.StatusCode)
	}
	resp.Body.Close()
}

func TestSignedURL(t *testing.T) {
	h := newHarness(t, nil)
	source := h.origin.URL + "/photo.jpg"

	path := h.signed(t, "w_40,f_jpeg", source, time.Now().Add(time.Hour))
	resp := h.get(t, path, nil)
	if resp.StatusCode != 200 {
		t.Fatalf("want 200, got %d", resp.StatusCode)
	}
	resp.Body.Close()

	withQuery := h.signed(t, "w_40,f_jpeg", source+"?v=42", time.Now().Add(time.Hour))
	if resp := h.get(t, withQuery, nil); resp.StatusCode != 200 {
		t.Fatalf("signed source query failed: %d", resp.StatusCode)
	} else {
		resp.Body.Close()
	}

	bad := strings.Replace(path, "w_40", "w_41", 1)
	code, errCode := decodeErr(t, h.get(t, bad, nil))
	if code != 401 || errCode != "invalid_signature" {
		t.Fatalf("want 401 invalid_signature, got %d %s", code, errCode)
	}

	expired := h.signed(t, "w_40,f_jpeg", source, time.Now().Add(-time.Hour))
	code, errCode = decodeErr(t, h.get(t, expired, nil))
	if code != 410 || errCode != "signature_expired" {
		t.Fatalf("want 410 signature_expired, got %d %s", code, errCode)
	}
}

func TestSignedOnly(t *testing.T) {
	h := newHarness(t, func(c *config.Config) { c.SignedOnly = true })
	source := h.origin.URL + "/photo.jpg"
	code, errCode := decodeErr(t, h.get(t, "/w_40,f_jpeg/"+source, nil))
	if code != 401 || errCode != "invalid_signature" {
		t.Fatalf("want 401 invalid_signature, got %d %s", code, errCode)
	}
}

func TestErrorsAndJSONMode(t *testing.T) {
	h := newHarness(t, nil)
	source := h.origin.URL + "/photo.jpg"

	code, errCode := decodeErr(t, h.get(t, "/w_zero,f_jpeg/"+source, nil))
	if code != 400 || errCode != "invalid_ops" {
		t.Fatalf("want 400 invalid_ops, got %d %s", code, errCode)
	}
	code, errCode = decodeErr(t, h.get(t, "/w_40,f_jpeg/"+h.origin.URL+"/missing.jpg", nil))
	if code != 404 || errCode != "source_not_found" {
		t.Fatalf("want 404 source_not_found, got %d %s", code, errCode)
	}

	resp := h.get(t, "/f_json/"+source, nil)
	if resp.StatusCode != 200 || resp.Header.Get("Content-Type") != "application/json" {
		t.Fatalf("json mode failed: %d %s", resp.StatusCode, resp.Header.Get("Content-Type"))
	}
	var meta map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&meta); err != nil {
		t.Fatal(err)
	}
	if meta["width"].(float64) != 64 || meta["height"].(float64) != 48 {
		t.Fatalf("unexpected metadata: %+v", meta)
	}
	resp.Body.Close()
}

func animatedGIFBytes(t *testing.T) []byte {
	t.Helper()
	const w, h = 20, 10
	palette := color.Palette{
		color.RGBA{R: 255, A: 255},
		color.RGBA{G: 255, A: 255},
		color.RGBA{B: 255, A: 255},
	}
	g := &gif.GIF{Config: image.Config{Width: w, Height: h, ColorModel: palette}}
	for i := range palette {
		frame := image.NewPaletted(image.Rect(0, 0, w, h), palette)
		for p := 0; p < len(frame.Pix); p++ {
			frame.Pix[p] = uint8(i)
		}
		g.Image = append(g.Image, frame)
		g.Delay = append(g.Delay, 10)
	}
	var buf bytes.Buffer
	if err := gif.EncodeAll(&buf, g); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// TestJSONAnimationReporting pins the f_json contract for pg_/pgs_/da_: an
// animated source reports isAnimated=true, and da_1 suppresses it.
func TestJSONAnimationReporting(t *testing.T) {
	h := newHarness(t, nil)
	gifData := animatedGIFBytes(t)
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "image/gif")
		_, _ = w.Write(gifData)
	}))
	t.Cleanup(origin.Close)
	source := origin.URL + "/anim.gif"

	meta := func(path string) map[string]any {
		t.Helper()
		resp := h.get(t, path, nil)
		defer resp.Body.Close()
		if resp.StatusCode != 200 {
			t.Fatalf("%s: status %d", path, resp.StatusCode)
		}
		var body map[string]any
		if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		return body
	}

	full := meta("/f_json/" + source)
	if full["isAnimated"] != true {
		t.Fatalf("f_json isAnimated = %v, want true", full["isAnimated"])
	}
	if full["height"].(float64) != 10 {
		t.Fatalf("f_json height = %v, want the first frame height 10", full["height"])
	}

	paged := meta("/pg_1,pgs_2,f_json/" + source)
	if paged["isAnimated"] != true {
		t.Fatalf("paged isAnimated = %v, want true", paged["isAnimated"])
	}
	if paged["height"].(float64) != 20 {
		t.Fatalf("paged height = %v, want two stacked frames (20)", paged["height"])
	}

	still := meta("/da_1,f_json/" + source)
	if still["isAnimated"] != false {
		t.Fatalf("da_1 isAnimated = %v, want false", still["isAnimated"])
	}
	if still["height"].(float64) != 10 {
		t.Fatalf("da_1 height = %v, want the first frame height 10", still["height"])
	}
}

func TestQueryMode(t *testing.T) {
	h := newHarness(t, nil)
	source := h.origin.URL + "/photo.jpg"
	resp := h.get(t, "/img?src="+urlQueryEscape(source)+"&w=32&f=png", nil)
	if resp.StatusCode != 200 {
		t.Fatalf("want 200, got %d", resp.StatusCode)
	}
	if ct := resp.Header.Get("Content-Type"); ct != "image/png" {
		t.Fatalf("want png, got %s", ct)
	}
	resp.Body.Close()
}

func TestPrivateBlocked(t *testing.T) {
	h := newHarness(t, func(c *config.Config) { c.AllowPrivate = false })
	source := h.origin.URL + "/photo.jpg"
	code, errCode := decodeErr(t, h.get(t, "/w_40,f_jpeg/"+source, nil))
	if code != 403 || errCode != "private_address_blocked" {
		t.Fatalf("want 403 private_address_blocked, got %d %s", code, errCode)
	}
}

func TestPurge(t *testing.T) {
	t.Setenv("IOIMG_ADMIN_TOKEN", "admin-secret")
	h := newHarness(t, nil)
	source := h.origin.URL + "/photo.jpg"
	resp := h.get(t, "/w_40,f_jpeg/"+source, nil)
	resp.Body.Close()

	req, _ := http.NewRequest(http.MethodPost, h.srv.URL+"/v1/purge", strings.NewReader(`{"all":true}`))
	req.Header.Set("Authorization", "Bearer admin-secret")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("want 202, got %d", resp.StatusCode)
	}
	resp.Body.Close()
}

func urlQueryEscape(s string) string {
	r := strings.NewReplacer(":", "%3A", "/", "%2F", "?", "%3F", "&", "%26", "=", "%3D")
	return r.Replace(s)
}
