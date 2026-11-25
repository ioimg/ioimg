package server

import (
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/ioimg/ioimg/service/internal/config"
)

// TestHostileInput asserts the boring contract that keeps ioimg alive in
// production: no request shape may panic, hang, or return an undocumented 5xx.
// Everything a client can control is either served or a structured 4xx; the
// only accepted 5xx are upstream failures (502/504) and our own shedding (503).
func TestHostileInput(t *testing.T) {
	h := newHarness(t, nil)
	origin := h.origin.URL

	cases := []struct {
		name string
		path string
		// want lists acceptable statuses. Any other status fails the case, which
		// is how a new "impossible" state gets noticed.
		want []int
	}{
		{"baseline", "/w_400,c_fit,q_80/" + origin + "/photo.jpg", []int{200}},
		{"no ops", "/" + origin + "/photo.jpg", []int{400, 422}},
		{"empty ops", "//" + origin + "/photo.jpg", []int{400, 422}},
		{"zero width", "/w_0,c_fit/" + origin + "/photo.jpg", []int{400, 422}},
		{"negative width", "/w_-5,c_fit/" + origin + "/photo.jpg", []int{400, 422}},
		{"huge width", "/w_999999999/" + origin + "/photo.jpg", []int{400, 422}},
		{"both auto", "/w_auto,h_auto/" + origin + "/photo.jpg", []int{400, 422}},
		{"quality zero", "/w_400,q_0/" + origin + "/photo.jpg", []int{200, 400, 422}},
		{"quality huge", "/w_400,q_999/" + origin + "/photo.jpg", []int{200, 400, 422}},
		{"dpr zero", "/w_400,dpr_0/" + origin + "/photo.jpg", []int{200, 400, 422}},
		{"dpr negative", "/w_400,dpr_-1/" + origin + "/photo.jpg", []int{200, 400, 422}},
		{"dpr nan", "/w_400,dpr_nan/" + origin + "/photo.jpg", []int{400, 422}},
		{"dpr huge", "/w_400,dpr_1e400/" + origin + "/photo.jpg", []int{400, 422}},
		{"blur negative", "/w_400,blur_-1/" + origin + "/photo.jpg", []int{200, 400, 422}},
		{"blur huge", "/w_400,blur_1e9/" + origin + "/photo.jpg", []int{200, 400, 422}},
		{"sharpen huge", "/w_400,sharpen_99/" + origin + "/photo.jpg", []int{200, 400, 422}},
		{"bad background", "/w_400,bg_zzz/" + origin + "/photo.jpg", []int{200, 400, 422}},
		{"unknown algorithm", "/w_400,ra_bogus/" + origin + "/photo.jpg", []int{400, 422}},
		{"algorithm nearest", "/w_400,ra_nearest/" + origin + "/photo.jpg", []int{200}},
		{"max bytes zero", "/w_400,mb_0/" + origin + "/photo.jpg", []int{400, 422}},
		{"max bytes huge", "/w_400,mb_99999999/" + origin + "/photo.jpg", []int{400, 422}},
		{"max bytes ok", "/w_400,mb_6000/" + origin + "/photo.jpg", []int{200}},
		{"filename traversal", "/w_400,fn_../../etc/passwd/" + origin + "/photo.jpg", []int{400, 422}},
		{"filename quote", `/w_400,fn_a";x=1/` + origin + "/photo.jpg", []int{400, 422}},
		{"filename ok", "/w_400,fn_hero.jpg/" + origin + "/photo.jpg", []int{200}},
		{"cachebuster bad", "/w_400,cb_😀/" + origin + "/photo.jpg", []int{400, 422}},
		{"cachebuster ok", "/w_400,cb_v2/" + origin + "/photo.jpg", []int{200}},
		{"bad rotate", "/rot_45/" + origin + "/photo.jpg", []int{400, 422}},
		{"bad flip", "/flip_x/" + origin + "/photo.jpg", []int{400, 422}},
		{"trim zero", "/trim_0/" + origin + "/photo.jpg", []int{400, 422}},
		{"duplicate ops", "/w_200,w_400/" + origin + "/photo.jpg", []int{200, 400, 422}},
		{"unknown op", "/w_400,f_nope/" + origin + "/photo.jpg", []int{400, 422}},
		{"missing source", "/w_400/" + origin + "/missing.jpg", []int{404}},
		{"not a url", "/w_400/not-a-url", []int{400, 422}},
		{"scheme only", "/w_400/http://", []int{400, 422}},
		{"host refused", "/w_400/http://127.0.0.1:1/x.jpg", []int{502, 504}},
		{"bad signature params", "/w_400,c_fit/" + origin + "/photo.jpg?exp=abc&sig=x", []int{200, 401, 403, 422}},
		{"empty sig value", "/w_400,c_fit/" + origin + "/photo.jpg?sig=", []int{200, 401, 403, 422}},
		{"encoded dot", "/w_400,c_fit/" + origin + "/photo%2Ejpg", []int{200, 400, 404, 422}},
		{"long ops", "/" + strings.Repeat("w_400,", 2000) + "q_80/" + origin + "/photo.jpg", []int{200, 400, 414, 422, 431}},
		{"long path", "/" + strings.Repeat("a", 8000) + "/" + origin + "/photo.jpg", []int{400, 414, 422, 431}},
		{"many slashes", "/w_400/" + strings.Repeat("/", 50) + origin + "/photo.jpg", []int{400, 404, 422}},
		{"dotdot", "/w_400/../../../etc/passwd", []int{400, 422}},
		{"unicode", "/w_400/" + origin + "/фото.jpg", []int{200, 404, 422}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			resp, err := http.Get(h.srv.URL + tc.path)
			if err != nil {
				t.Fatalf("request failed: %v", err)
			}
			defer func() { _ = resp.Body.Close() }()
			body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<16))

			if !contains(tc.want, resp.StatusCode) {
				t.Fatalf("status %d not in %v; body: %s", resp.StatusCode, tc.want, truncate(body))
			}
			if resp.StatusCode >= 500 && resp.StatusCode != http.StatusServiceUnavailable {
				// Upstream failures are legitimate, but they must still be documented
				// and structured rather than a bare 500 from a panic.
				if resp.StatusCode != http.StatusBadGateway && resp.StatusCode != http.StatusGatewayTimeout {
					t.Fatalf("undocumented %d; body: %s", resp.StatusCode, truncate(body))
				}
			}
			if resp.StatusCode >= 400 {
				var payload struct {
					Error struct {
						Code string `json:"code"`
					} `json:"error"`
				}
				if err := json.Unmarshal(body, &payload); err != nil || payload.Error.Code == "" {
					t.Fatalf("error body is not structured: %s", truncate(body))
				}
			}
		})
	}
}

// TestHostileMethods keeps verbs other than GET/HEAD a 405 with an Allow header
// instead of falling through to the image path.
func TestHostileMethods(t *testing.T) {
	h := newHarness(t, nil)
	for _, method := range []string{http.MethodPost, http.MethodPut, http.MethodDelete, http.MethodPatch} {
		req, err := http.NewRequest(method, h.srv.URL+"/w_400/"+h.origin.URL+"/photo.jpg", nil)
		if err != nil {
			t.Fatal(err)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("%s: %v", method, err)
		}
		_ = resp.Body.Close()
		if resp.StatusCode != http.StatusMethodNotAllowed {
			t.Fatalf("%s: got %d, want 405", method, resp.StatusCode)
		}
		if resp.Header.Get("Allow") == "" && resp.Header.Get("allow") == "" {
			t.Fatalf("%s: missing Allow header", method)
		}
	}
}

// TestSigningRejectsTampering makes sure the signature path fails closed.
func TestSigningRejectsTampering(t *testing.T) {
	h := newHarness(t, func(c *config.Config) { c.SignedOnly = true })
	if !strings.Contains(h.srv.URL, "127.0.0.1") {
		t.Fatal("unexpected harness url")
	}
	req := h.srv.URL + "/w_400,c_fit/" + url.QueryEscape(h.origin.URL) + "/photo.jpg"
	resp, err := http.Get(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("unsigned request on signed-only project: got %d, want 401", resp.StatusCode)
	}
}

func contains(list []int, v int) bool {
	for _, x := range list {
		if x == v {
			return true
		}
	}
	return false
}

func truncate(b []byte) string {
	if len(b) > 200 {
		return string(b[:200]) + "..."
	}
	return string(b)
}
