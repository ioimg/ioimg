// These tests never contact an ACME directory. The certificate-issuance path
// (account registration, challenge fulfilment, certificate download) is left
// untested on purpose: it needs either the Let's Encrypt staging CA behind an
// explicit build tag or a fake ACME server. The policy, cache, and HTTP-01
// layers below are the parts that can be verified deterministically offline.
package acmetls

import (
	"context"
	"crypto/tls"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ioimg/ioimg/service/pkg/tenant"
)

func newTestManager(t *testing.T, o Options) *Manager {
	t.Helper()
	if o.CacheDir == "" {
		o.CacheDir = t.TempDir()
	}
	m, err := New(o)
	if err != nil {
		t.Fatal(err)
	}
	return m
}

func TestHostPolicyRejectsArbitrarySNI(t *testing.T) {
	m := newTestManager(t, Options{Hosts: []string{"img.example.com", "*.static.example"}})
	for _, host := range []string{
		"evil.example",
		"img.example.com.evil",
		"",
		"127.0.0.1",
		"../etc/passwd",
		"*.example.com",
	} {
		if m.AllowsHost(host) {
			t.Errorf("AllowsHost(%q) = true, want false", host)
		}
	}
	for _, host := range []string{"img.example.com", "IMG.EXAMPLE.COM.", "img.example.com:443", "a.static.example", "a.b.static.example"} {
		if !m.AllowsHost(host) {
			t.Errorf("AllowsHost(%q) = false, want true", host)
		}
	}
	// The wildcard entry authorises subdomains, never the bare suffix.
	if m.AllowsHost("static.example") {
		t.Fatal("wildcard entry must not authorise the bare suffix")
	}
}

// A domain that appears in the current control-plane snapshot becomes eligible
// without a restart, while domains outside it stay rejected.
func TestHostPolicyFollowsSnapshot(t *testing.T) {
	store := tenant.NewStore(nil)
	store.Apply(tenant.Snapshot{Revision: 1, Projects: []tenant.Project{
		{ID: "p1", Domains: []string{"cdn.shop.example", "*.images.shop.example"}},
	}})
	m := newTestManager(t, Options{Allowed: store.HasDomain})
	if !m.AllowsHost("cdn.shop.example") {
		t.Fatal("exact custom domain from snapshot must be allowed")
	}
	if !m.AllowsHost("x.images.shop.example") {
		t.Fatal("wildcard custom domain from snapshot must be allowed")
	}
	if m.AllowsHost("other.shop.example") {
		t.Fatal("host outside the snapshot must be rejected")
	}

	// A later snapshot removes eligibility again.
	store.Apply(tenant.Snapshot{Revision: 2, Projects: []tenant.Project{
		{ID: "p1", Domains: []string{"new.shop.example"}},
	}})
	if m.AllowsHost("cdn.shop.example") {
		t.Fatal("removed domain must no longer be allowed")
	}
	if !m.AllowsHost("new.shop.example") {
		t.Fatal("newly added domain must be allowed")
	}
}

func TestGetCertificateRejectsUnknownSNIWithoutNetwork(t *testing.T) {
	m := newTestManager(t, Options{Hosts: []string{"img.example.com"}})
	// GetCertificate consults HostPolicy before any cache or ACME request, so an
	// unknown name fails locally even though no fake ACME server is running.
	if _, err := m.manager.GetCertificate(&tls.ClientHelloInfo{ServerName: "evil.example"}); err == nil {
		t.Fatal("GetCertificate accepted an unknown SNI")
	}
}

func TestCacheSanitizesFileNames(t *testing.T) {
	dir := t.TempDir()
	c := NewCache(dir)
	ctx := context.Background()

	for _, key := range []string{"../../escape", "/etc/passwd", "..", ".", `..\..\win`, "a/b/c"} {
		name := SanitizeCacheName(key)
		if name == "" || name == "." || name == ".." {
			t.Fatalf("SanitizeCacheName(%q) = %q", key, name)
		}
		if strings.ContainsAny(name, `/\`) {
			t.Fatalf("SanitizeCacheName(%q) = %q contains a path separator", key, name)
		}
		p, err := c.path(key)
		if err != nil {
			t.Fatalf("path(%q): %v", key, err)
		}
		if filepath.Dir(p) != filepath.Clean(dir) {
			t.Fatalf("path(%q) = %q, want a direct child of %q", key, p, dir)
		}
	}

	if err := c.Put(ctx, "../../escape", []byte("x")); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(dir), "escape")); err == nil {
		t.Fatal("cache write escaped the cache directory")
	}
	got, err := c.Get(ctx, "../../escape")
	if err != nil || string(got) != "x" {
		t.Fatalf("round trip failed: %q %v", got, err)
	}
	if _, err := c.Get(ctx, "missing"); err == nil {
		t.Fatal("missing key must return an error")
	}
	if err := c.Delete(ctx, "../../escape"); err != nil {
		t.Fatal(err)
	}
}

// The HTTP-01 handler must answer only challenge paths for hosts that are being
// validated; everything else goes to the fallback.
func TestHTTPHandlerOnlyAnswersChallenges(t *testing.T) {
	m := newTestManager(t, Options{Hosts: []string{"img.example.com"}})
	fallback := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})
	h := m.HTTPHandler(fallback)

	cases := []struct {
		name string
		host string
		path string
		want int
	}{
		{"non-challenge falls through", "img.example.com", "/healthz", http.StatusNoContent},
		{"unknown token for allowed host", "img.example.com", "/.well-known/acme-challenge/not-a-token", http.StatusNotFound},
		{"challenge for unknown host", "evil.example", "/.well-known/acme-challenge/x", http.StatusForbidden},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, tc.path, nil)
			req.Host = tc.host
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, req)
			if rec.Code != tc.want {
				t.Fatalf("status = %d, want %d (body %q)", rec.Code, tc.want, rec.Body.String())
			}
		})
	}
}

func TestNormalizeHost(t *testing.T) {
	cases := map[string]string{
		"IMG.Example.COM.":  "img.example.com",
		"example.com:8443":  "example.com",
		"  example.com  ":   "example.com",
		"xn--bcher-kva.ch":  "xn--bcher-kva.ch",
		"-bad.example":      "",
		"bad..example":      "",
		"http://example.co": "",
		"[::1]:443":         "",
	}
	for in, want := range cases {
		got, ok := NormalizeHost(in)
		if want == "" {
			if ok {
				t.Errorf("NormalizeHost(%q) = %q, want rejection", in, got)
			}
			continue
		}
		if !ok || got != want {
			t.Errorf("NormalizeHost(%q) = %q, %v; want %q", in, got, ok, want)
		}
	}
}
