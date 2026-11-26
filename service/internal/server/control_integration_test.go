package server

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ioimg/ioimg/service/internal/config"
	"github.com/ioimg/ioimg/service/pkg/ops"
	"github.com/ioimg/ioimg/service/pkg/sign"
	"github.com/ioimg/ioimg/service/pkg/tenant"
)

// fakeControl implements the control-plane snapshot contract used by the data
// plane. The real control plane lives in a separate repository; this fixture
// keeps the data-plane integration test self-contained.
type fakeControl struct {
	mu       sync.Mutex
	revision int64
	projects map[string]tenant.Project
	srv      *httptest.Server
	origin   *httptest.Server
	payload  []byte
}

func newFakeControl(t *testing.T) *fakeControl {
	t.Helper()
	f := &fakeControl{revision: 1, projects: map[string]tenant.Project{}}

	mux := http.NewServeMux()
	mux.HandleFunc("/v1/config/snapshot", f.handleSnapshot)
	mux.HandleFunc("/v1/usage", f.handleUsage)
	mux.HandleFunc("/v1/projects", f.handleProjects)
	mux.HandleFunc("/v1/projects/", f.handleProject)
	f.srv = httptest.NewServer(mux)
	t.Cleanup(f.srv.Close)

	f.payload = jpegBytes(t)
	f.origin = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "image/jpeg")
		_, _ = w.Write(f.payload)
	}))
	t.Cleanup(f.origin.Close)
	return f
}

func (f *fakeControl) handleSnapshot(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	f.mu.Lock()
	projects := make([]tenant.Project, 0, len(f.projects))
	for _, p := range f.projects {
		projects = append(projects, p)
	}
	snap := tenant.Snapshot{Revision: f.revision, Projects: projects}
	f.mu.Unlock()
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(snap)
}

func (f *fakeControl) handleUsage(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	_, _ = io.Copy(io.Discard, r.Body)
	w.WriteHeader(http.StatusNoContent)
}

func (f *fakeControl) handleProjects(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var in struct {
		ID             string            `json:"id"`
		Name           string            `json:"name"`
		Domains        []string          `json:"domains"`
		Allowlist      []string          `json:"allowlist"`
		AllowAnyHost   bool              `json:"allowAnyHost"`
		SignedOnly     bool              `json:"signedOnly"`
		ExpiryRequired bool              `json:"expiryRequired"`
		Presets        map[string]string `json:"presets"`
		Concurrency    int               `json:"concurrency"`
	}
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	kid, secret := "kid-"+in.ID, "secret-"+in.ID
	p := tenant.Project{
		ID:             in.ID,
		Name:           in.Name,
		Domains:        in.Domains,
		Allowlist:      in.Allowlist,
		AllowAnyHost:   in.AllowAnyHost,
		SignedOnly:     in.SignedOnly,
		ExpiryRequired: in.ExpiryRequired,
		Presets:        in.Presets,
		SigningKeys:    []tenant.SigningKey{{Kid: kid, Secret: secret}},
		Limits:         tenant.Limits{Concurrency: in.Concurrency},
	}
	f.mu.Lock()
	f.projects[in.ID] = p
	f.revision++
	f.mu.Unlock()
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	_ = json.NewEncoder(w).Encode(map[string]any{
		"project": p,
		"signingKey": map[string]string{
			"kid":    kid,
			"secret": secret,
		},
	})
}

func (f *fakeControl) handleProject(w http.ResponseWriter, r *http.Request) {
	rest := strings.TrimPrefix(r.URL.Path, "/v1/projects/")
	parts := strings.Split(rest, "/")
	if len(parts) != 2 || parts[1] != "allowlist" || r.Method != http.MethodPut {
		http.NotFound(w, r)
		return
	}
	var in struct {
		Allowlist    []string `json:"allowlist"`
		AllowAnyHost bool     `json:"allowAnyHost"`
	}
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	f.mu.Lock()
	p, ok := f.projects[parts[0]]
	if ok {
		p.Allowlist = in.Allowlist
		p.AllowAnyHost = in.AllowAnyHost
		f.projects[parts[0]] = p
		f.revision++
	}
	f.mu.Unlock()
	if !ok {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"project": p})
}

func (f *fakeControl) call(t *testing.T, method, path, token string, body any) (*http.Response, []byte) {
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

func (f *fakeControl) createProject(t *testing.T, body map[string]any) (kid, secret string) {
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

func (f *fakeControl) request(t *testing.T, srv *Server, project, path string) *http.Response {
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
	f := newFakeControl(t)
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

// TestProjectConcurrencyLimit pins the admission rule: one project may not take
// more than its snapshot concurrency, a rejected request is a retryable 429, and
// the slot is returned when the in-flight request finishes.
func TestProjectConcurrencyLimit(t *testing.T) {
	f := newFakeControl(t)
	f.createProject(t, map[string]any{
		"id": "slow", "name": "Slow", "allowAnyHost": true, "concurrency": 1,
	})
	plane := newControlDataPlane(t, f.srv.URL)

	// An origin that accepts the connection and never answers: the first
	// request holds the only slot for as long as we need it to.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	var (
		connMu sync.Mutex
		conns  []net.Conn
	)
	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			connMu.Lock()
			conns = append(conns, conn)
			connMu.Unlock()
		}
	}()
	source := "http://" + ln.Addr().String() + "/slow.jpg"
	path := "/w_40,f_jpeg/" + source

	first := make(chan int, 1)
	go func() {
		resp := f.request(t, plane, "slow", path)
		_, _ = io.Copy(io.Discard, resp.Body)
		resp.Body.Close()
		first <- resp.StatusCode
	}()

	// Wait until the first request owns the slot (it is blocked on the origin).
	deadline := time.Now().Add(2 * time.Second)
	for {
		inflight, _ := plane.gate.stats()
		if inflight["slow"] == 1 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("first request never took a slot: %v", inflight)
		}
		time.Sleep(10 * time.Millisecond)
	}

	resp := f.request(t, plane, "slow", path)
	if resp.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("want 429 concurrency_exceeded, got %d", resp.StatusCode)
	}
	if got := resp.Header.Get("Retry-After"); got != "1" {
		t.Fatalf("want Retry-After 1, got %q", got)
	}
	raw, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if !bytes.Contains(raw, []byte("concurrency_exceeded")) {
		t.Fatalf("want concurrency_exceeded in body, got %s", raw)
	}

	// Release the blocked request and make sure the slot comes back.
	_ = ln.Close()
	connMu.Lock()
	for _, c := range conns {
		_ = c.Close()
	}
	connMu.Unlock()
	<-done
	<-first

	deadline = time.Now().Add(2 * time.Second)
	for {
		inflight, _ := plane.gate.stats()
		if inflight["slow"] == 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("slot was not released: %v", inflight)
		}
		time.Sleep(10 * time.Millisecond)
	}

	if !bytes.Contains([]byte(plane.metrics.Render(0, 0)), []byte("ioimg_concurrency_rejected_total 1")) {
		t.Fatalf("rejection not counted:\n%s", plane.metrics.Render(0, 0))
	}
}
