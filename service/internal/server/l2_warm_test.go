package server

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"ioimg/internal/purge"
)

type fakeL2 struct {
	mu   sync.Mutex
	data map[string][]byte
}

func newFakeL2() *fakeL2 { return &fakeL2{data: map[string][]byte{}} }

func (f *fakeL2) Get(_ context.Context, key string) ([]byte, bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	v, ok := f.data[key]
	return v, ok, nil
}

func (f *fakeL2) Put(_ context.Context, key string, data []byte) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.data[key] = append([]byte(nil), data...)
	return nil
}

func (f *fakeL2) Delete(_ context.Context, key string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.data, key)
	return nil
}

func (f *fakeL2) DeletePrefix(_ context.Context, prefix string) (int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := 0
	for k := range f.data {
		if strings.HasPrefix(k, prefix) {
			delete(f.data, k)
			n++
		}
	}
	return n, nil
}

func (f *fakeL2) size() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.data)
}

type fakePurger struct {
	mu   sync.Mutex
	urls int
}

func (f *fakePurger) Name() string { return "fake" }

func (f *fakePurger) Purge(_ context.Context, urls, _ []string) (purge.Result, error) {
	f.mu.Lock()
	f.urls += len(urls)
	f.mu.Unlock()
	return purge.Result{Provider: f.Name(), Result: "ok", URLs: len(urls)}, nil
}

func waitFor(t *testing.T, what string, fn func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if fn() {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

func TestL2ReadThrough(t *testing.T) {
	l2 := newFakeL2()
	h := newHarnessOpts(t, nil, func(o *Options) { o.L2 = l2 })
	source := h.origin.URL + "/photo.jpg"

	resp := h.get(t, "/w_40,f_jpeg/"+source, nil)
	if resp.StatusCode != 200 {
		t.Fatalf("want 200, got %d", resp.StatusCode)
	}
	resp.Body.Close()
	waitFor(t, "l2 write", func() bool { return l2.size() == 1 })
	hitsAfterMiss := h.hits.Load()

	// Drop L1 only: the next request must be served from L2 without origin.
	if err := h.server.cache.Clear(); err != nil {
		t.Fatal(err)
	}
	resp = h.get(t, "/w_40,f_jpeg/"+source, nil)
	if resp.StatusCode != 200 {
		t.Fatalf("want 200, got %d", resp.StatusCode)
	}
	if got := resp.Header.Get("X-IOIMG-Cache"); got != "HIT-L2" {
		t.Fatalf("want HIT-L2, got %s", got)
	}
	resp.Body.Close()
	if h.hits.Load() != hitsAfterMiss {
		t.Fatalf("origin was fetched on an L2 hit (%d -> %d)", hitsAfterMiss, h.hits.Load())
	}
	// L2 hit must repopulate L1.
	resp = h.get(t, "/w_40,f_jpeg/"+source, nil)
	if got := resp.Header.Get("X-IOIMG-Cache"); got != "HIT-L1" {
		t.Fatalf("want HIT-L1 after rehydrate, got %s", got)
	}
	resp.Body.Close()
}

func TestWarmJob(t *testing.T) {
	t.Setenv("IOIMG_ADMIN_TOKEN", "admin")
	h := newHarness(t, nil)
	source := h.origin.URL + "/photo.jpg"

	body := fmt.Sprintf(`{"targets":[{"source":%q,"ops":"w_40,f_jpeg"},{"source":%q,"ops":"t_thumb"}]}`, source, source)
	req, _ := http.NewRequest(http.MethodPost, h.srv.URL+"/v1/warm", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer admin")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("want 202, got %d", resp.StatusCode)
	}
	var created struct {
		JobID string `json:"jobId"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&created); err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()

	var job Job
	waitFor(t, "job completion", func() bool {
		req, _ := http.NewRequest(http.MethodGet, h.srv.URL+"/v1/jobs/"+created.JobID, nil)
		req.Header.Set("Authorization", "Bearer admin")
		r, err := http.DefaultClient.Do(req)
		if err != nil {
			return false
		}
		defer r.Body.Close()
		if r.StatusCode != 200 {
			return false
		}
		job = Job{}
		return json.NewDecoder(r.Body).Decode(&job) == nil && job.Status == "completed"
	})
	if job.Total != 2 || job.Done != 2 || job.Failed != 0 {
		t.Fatalf("unexpected job: %+v", job)
	}

	hits := h.hits.Load()
	resp = h.get(t, "/w_40,f_jpeg/"+source, nil)
	if got := resp.Header.Get("X-IOIMG-Cache"); got != "HIT-L1" {
		t.Fatalf("warmed request should be HIT-L1, got %s", got)
	}
	resp.Body.Close()
	if h.hits.Load() != hits {
		t.Fatal("warmed request hit the origin")
	}
}

func TestPurgeCallsCDNAndClearsCache(t *testing.T) {
	t.Setenv("IOIMG_ADMIN_TOKEN", "admin")
	cdn := &fakePurger{}
	h := newHarnessOpts(t, nil, func(o *Options) { o.Purgers = []purge.Purger{cdn} })
	source := h.origin.URL + "/photo.jpg"
	imgURL := h.srv.URL + "/w_40,f_jpeg/" + source

	resp := h.get(t, "/w_40,f_jpeg/"+source, nil)
	resp.Body.Close()
	body := fmt.Sprintf(`{"urls":[%q],"tags":["src:1"]}`, imgURL)
	req, _ := http.NewRequest(http.MethodPost, h.srv.URL+"/v1/purge", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer admin")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("want 202, got %d", resp.StatusCode)
	}
	var out struct {
		Local struct {
			Keys int `json:"keys"`
		} `json:"local"`
		CDN []purge.Result `json:"cdn"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if out.Local.Keys == 0 {
		t.Fatal("no local keys deleted")
	}
	if len(out.CDN) != 1 || out.CDN[0].Provider != "fake" {
		t.Fatalf("cdn fan-out missing: %+v", out.CDN)
	}
	cdn.mu.Lock()
	calls := cdn.urls
	cdn.mu.Unlock()
	if calls != 1 {
		t.Fatalf("want 1 CDN url, got %d", calls)
	}

	resp = h.get(t, "/w_40,f_jpeg/"+source, nil)
	if got := resp.Header.Get("X-IOIMG-Cache"); got != "MISS" {
		t.Fatalf("purged entry should be MISS, got %s", got)
	}
	resp.Body.Close()
}
