package transport

import (
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/ioimg/ioimg/service/internal/acmetls"
	"github.com/ioimg/ioimg/service/internal/cache"
	"github.com/ioimg/ioimg/service/internal/config"
)

func TestPreforkEnv(t *testing.T) {
	cases := []struct {
		value   string
		want    bool
		enabled bool
	}{
		{"", false, true},
		{"1", true, true},
		{"true", true, true},
		{"yes", true, true},
		{"on", true, true},
		{"0", false, false},
		{"false", false, false},
		{"no", false, false},
		{"off", false, false},
		{"bogus", false, true},
	}
	for _, tc := range cases {
		t.Setenv("IOIMG_PREFORK", tc.value)
		if got := preforkRequested(); got != tc.want {
			t.Errorf("preforkRequested(%q) = %v, want %v", tc.value, got, tc.want)
		}
		if got := preforkEnabled(); got != tc.enabled {
			t.Errorf("preforkEnabled(%q) = %v, want %v", tc.value, got, tc.enabled)
		}
	}
}

// The cache read mode is chosen per transport: blocking reads for the pinned
// celeris loops, io_uring for fibre, one fd for net/http.
func TestReadModeSelection(t *testing.T) {
	uring := cache.UringAvailable()
	d, err := cache.NewDisk(t.TempDir(), 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	for _, mode := range []cache.ReadMode{cache.ReadOpenStat, cache.ReadUring, cache.ReadOS} {
		d.SetReadMode(mode)
		want := string(mode)
		if mode == cache.ReadAuto {
			want = string(cache.ReadOpenStat)
			if uring {
				want = string(cache.ReadUring)
			}
		}
		if got := d.ReadMode(); got != want {
			t.Errorf("SetReadMode(%q) resolved to %q, want %q", mode, got, want)
		}
	}
}

func TestRedirectToHTTPS(t *testing.T) {
	cases := []struct {
		name      string
		host      string
		target    string
		httpsAddr string
		want      string
	}{
		{"default port", "img.example.com", "/a/b?c=1", ":443", "https://img.example.com/a/b?c=1"},
		{"custom port", "img.example.com:8080", "/x", ":8443", "https://img.example.com:8443/x"},
		{"host with port stripped", "img.example.com:80", "/", ":443", "https://img.example.com/"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "http://"+tc.host+tc.target, nil)
			rec := httptest.NewRecorder()
			redirectToHTTPS(rec, req, tc.httpsAddr)
			if rec.Code != http.StatusPermanentRedirect {
				t.Fatalf("status = %d, want 308", rec.Code)
			}
			if got := rec.Header().Get("Location"); got != tc.want {
				t.Fatalf("Location = %q, want %q", got, tc.want)
			}
		})
	}

	// Non-GET requests are redirected too; 308 preserves the method.
	req := httptest.NewRequest(http.MethodPost, "http://img.example.com/upload", nil)
	rec := httptest.NewRecorder()
	redirectToHTTPS(rec, req, ":443")
	if rec.Code != http.StatusPermanentRedirect {
		t.Fatalf("POST status = %d, want 308", rec.Code)
	}
}

func TestPlainHandlerServesHealthAndChallengeOnly(t *testing.T) {
	mgr, err := acmetls.New(acmetls.Options{CacheDir: t.TempDir(), Hosts: []string{"img.example.com"}})
	if err != nil {
		t.Fatal(err)
	}
	app := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/healthz" {
			t.Errorf("app received %q", r.URL.Path)
		}
		_, _ = w.Write([]byte("ok"))
	})
	h := newPlainHandler(mgr, app, ":443", true)

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "http://img.example.com/healthz", nil))
	if rec.Code != http.StatusOK || rec.Body.String() != "ok" {
		t.Fatalf("healthz = %d %q", rec.Code, rec.Body.String())
	}

	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "http://img.example.com/img.jpg", nil))
	if rec.Code != http.StatusPermanentRedirect {
		t.Fatalf("image path status = %d, want 308", rec.Code)
	}
	if got := rec.Header().Get("Location"); got != "https://img.example.com/img.jpg" {
		t.Fatalf("Location = %q", got)
	}

	// The dedicated challenge listener redirects even /healthz.
	challengeOnly := newPlainHandler(mgr, app, ":443", false)
	rec = httptest.NewRecorder()
	challengeOnly.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "http://img.example.com/healthz", nil))
	if rec.Code != http.StatusPermanentRedirect {
		t.Fatalf("challenge-only healthz status = %d, want 308", rec.Code)
	}

	rec = httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "http://img.example.com/.well-known/acme-challenge/missing", nil)
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("challenge status = %d, want 404", rec.Code)
	}
}

func TestServeTLSRejectsAddressCollision(t *testing.T) {
	cfg := config.Config{Addr: ":8443", TLS: config.TLSConfig{Enabled: true, Addr: ":8443"}}
	if err := serveTLS(context.Background(), nil, cfg, slog.Default()); err == nil {
		t.Fatal("HTTPS and plain HTTP on one address must be rejected")
	}
}
