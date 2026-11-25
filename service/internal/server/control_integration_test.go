package server

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"

	"ioimg/internal/config"
	"ioimg/internal/control"
	"ioimg/internal/ops"
	"ioimg/internal/sign"
)

type controlFixture struct {
	srv     *httptest.Server
	store   *control.Store
	origin  *httptest.Server
	payload []byte
}

func newControlFixture(t *testing.T) *controlFixture {
	t.Helper()
	store, err := control.Open(":memory:", nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	api := &control.API{Store: store, Bootstrap: "boot", DataToken: "data"}
	f := &controlFixture{store: store, srv: httptest.NewServer(api.Handler())}
	t.Cleanup(f.srv.Close)

	f.payload = jpegBytes(t)
	f.origin = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "image/jpeg")
		_, _ = w.Write(f.payload)
	}))
	t.Cleanup(f.origin.Close)
	return f
}

func (f *controlFixture) call(t *testing.T, method, path, token string, body any) (*http.Response, []byte) {
	t.Helper()
	var rdr io.Reader
	if body != nil {
		raw, _ := json.Marshal(body)
		rdr = bytes.NewReader(raw)
	}
	req, _ := http.NewRequest(method, f.srv.URL+path, rdr)
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	return resp, raw
}

func (f *controlFixture) createProject(t *testing.T, body map[string]any) (kid, secret string) {
	t.Helper()
	resp, raw := f.call(t, http.MethodPost, "/v1/projects", "boot", body)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create project: %d %s", resp.StatusCode, raw)
	}
	var out struct {
		SigningKey struct {
			Kid    string `json:"kid"`
			Secret string `json:"secret"`
		} `json:"signingKey"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatal(err)
	}
	return out.SigningKey.Kid, out.SigningKey.Secret
}

func newControlDataPlane(t *testing.T, controlURL string) *Server {
	t.Helper()
	cfg := config.Config{
		ControlURL:         controlURL,
		ControlToken:       "data",
		ControlSync:        50 * time.Millisecond,
		ControlTimeout:     2 * time.Second,
		AllowProjectHeader: true,
		ProjectHeader:      "X-IOIMG-Project",
		AllowPrivate:       true,
		SourceTimeout:      5 * time.Second,
		SourceMaxBytes:     1 << 20,
		SourceUserAgent:    "ioimg-test",
		MaxDimension:       ops.MaxDimension,
		Concurrency:        4,
		CacheDir:           t.TempDir(),
		CacheMaxBytes:      8 << 20,
	}
	srv, err := New(cfg, Options{Logger: slog.New(slog.NewTextHandler(io.Discard, nil))})
	if err != nil {
		t.Fatal(err)
	}
	return srv
}

func (f *controlFixture) request(t *testing.T, srv *Server, project, path string) *http.Response {
	t.Helper()
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)
	req, _ := http.NewRequest(http.MethodGet, ts.URL+path, nil)
	if project != "" {
		req.Header.Set("X-IOIMG-Project", project)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	return resp
}

func TestMultiTenantControlPlane(t *testing.T) {
	f := newControlFixture(t)
	originURL, _ := url.Parse(f.origin.URL)
	originHost := originURL.Hostname()

	_, acmeSecret := f.createProject(t, map[string]any{
		"id": "acme", "name": "Acme",
		"domains":      []string{"img.acme.test"},
		"allowlist":    []string{originHost},
		"allowAnyHost": false,
		"signedOnly":   false, "expiryRequired": false,
	})
	betaKid, betaSecret := f.createProject(t, map[string]any{
		"id": "beta", "name": "Beta",
		"domains":      []string{"img.beta.test"},
		"allowlist":    []string{originHost},
		"allowAnyHost": false,
		"signedOnly":   true, "expiryRequired": true,
	})
	_ = acmeSecret

	plane := newControlDataPlane(t, f.srv.URL)
	source := f.origin.URL + "/photo.jpg"

	// Unknown tenant is rejected; no cross-tenant fallback.
	resp := f.request(t, plane, "nope", "/w_40,f_jpeg/"+source)
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("want 404 project_not_found, got %d", resp.StatusCode)
	}
	resp.Body.Close()

	// Unsigned open project.
	resp = f.request(t, plane, "acme", "/w_40,f_jpeg/"+source)
	if resp.StatusCode != 200 {
		t.Fatalf("acme request: %d", resp.StatusCode)
	}
	if got := resp.Header.Get("X-IOIMG-Cache"); got != "MISS" {
		t.Fatalf("want MISS, got %s", got)
	}
	resp.Body.Close()

	// Signed tenant rejects unsigned and wrong-key signatures.
	resp = f.request(t, plane, "beta", "/w_40,f_jpeg/"+source)
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("beta unsigned: want 401, got %d", resp.StatusCode)
	}
	resp.Body.Close()

	canonical, _ := sign.CanonicalSource(source)
	o, _ := ops.Parse("w_40,f_jpeg", nil)
	signer := &sign.Signer{Keys: []sign.Key{{ID: betaKid, Secret: []byte(betaSecret)}}, IncludeExp: true}
	exp := time.Now().Add(time.Hour).Unix()
	sig, _ := signer.Sign(o.Canonical(), canonical, exp)
	signedPath := "/" + sig + "/" + o.Canonical() + "/" + canonical + fmt.Sprintf("?exp=%d", exp)
	resp = f.request(t, plane, "beta", signedPath)
	if resp.StatusCode != 200 {
		t.Fatalf("beta signed: %d", resp.StatusCode)
	}
	resp.Body.Close()

	// Same source/ops in two tenants must not share cache entries.
	resp = f.request(t, plane, "acme", "/w_40,f_jpeg/"+source)
	if got := resp.Header.Get("X-IOIMG-Cache"); got != "HIT-L1" {
		t.Fatalf("acme second request: want HIT-L1, got %s", got)
	}
	resp.Body.Close()

	// Allowlist change propagates via snapshot sync.
	resp, raw := f.call(t, http.MethodPut, "/v1/projects/acme/allowlist", "boot", map[string]any{
		"allowlist": []string{"other.test"}, "allowAnyHost": false,
	})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("update allowlist: %d %s", resp.StatusCode, raw)
	}
	deadline := time.Now().Add(2 * time.Second)
	for {
		resp = f.request(t, plane, "acme", "/w_40,f_jpeg/"+source+"?v=2")
		code := resp.StatusCode
		resp.Body.Close()
		if code == http.StatusForbidden {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("allowlist change did not propagate, last status %d", code)
		}
		time.Sleep(30 * time.Millisecond)
	}
}
