package server

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// Metrics holds Prometheus-style counters. All hot-path operations are atomic.
type Metrics struct {
	Requests    atomic.Uint64
	Hits        atomic.Uint64
	Misses      atomic.Uint64
	Coalesced   atomic.Uint64
	Errors      atomic.Uint64
	BytesIn     atomic.Uint64
	BytesOut    atomic.Uint64
	TransformNs atomic.Uint64
	CacheReadNs atomic.Uint64
	FetchNs     atomic.Uint64
	L2Hits      atomic.Uint64

	errorMu sync.Mutex
	byCode  map[string]uint64
}

// NewMetrics returns initialised metrics.
func NewMetrics() *Metrics { return &Metrics{byCode: map[string]uint64{}} }

// RecordError increments the total and the per-code counter.
func (m *Metrics) RecordError(code string) {
	m.Errors.Add(1)
	m.errorMu.Lock()
	m.byCode[code]++
	m.errorMu.Unlock()
}

// Render writes the Prometheus text exposition.
func (m *Metrics) Render(cacheEntries int, cacheBytes int64) string {
	var b strings.Builder
	line := func(name, help, typ string, v any) {
		fmt.Fprintf(&b, "# HELP %s %s\n# TYPE %s %s\n%s %v\n", name, help, name, typ, name, v)
	}
	line("ioimg_requests_total", "Image requests received.", "counter", m.Requests.Load())
	line("ioimg_cache_hits_total", "Cache hits.", "counter", m.Hits.Load())
	line("ioimg_cache_misses_total", "Cache misses.", "counter", m.Misses.Load())
	line("ioimg_coalesced_total", "Requests that joined an in-flight transform instead of repeating it.", "counter", m.Coalesced.Load())
	line("ioimg_cache_l2_hits_total", "Hits served by the L2 object cache.", "counter", m.L2Hits.Load())
	line("ioimg_errors_total", "Errors returned.", "counter", m.Errors.Load())
	line("ioimg_source_bytes_total", "Bytes fetched from origins.", "counter", m.BytesIn.Load())
	line("ioimg_bytes_out_total", "Bytes served.", "counter", m.BytesOut.Load())
	line("ioimg_transform_ns_total", "Time spent transforming.", "counter", m.TransformNs.Load())
	line("ioimg_fetch_ns_total", "Time spent fetching origins.", "counter", m.FetchNs.Load())
	line("ioimg_cache_entries", "Entries in the L1 disk cache.", "gauge", cacheEntries)
	line("ioimg_cache_bytes", "Bytes used by the L1 disk cache.", "gauge", cacheBytes)
	m.errorMu.Lock()
	fmt.Fprintf(&b, "# HELP ioimg_errors_by_code_total Errors by public code.\n# TYPE ioimg_errors_by_code_total counter\n")
	for code, n := range m.byCode {
		fmt.Fprintf(&b, "ioimg_errors_by_code_total{code=%q} %d\n", code, n)
	}
	m.errorMu.Unlock()
	return b.String()
}

// server time helpers are kept here to avoid importing time everywhere.

func newRequestID() string {
	var b [12]byte
	if _, err := rand.Read(b[:]); err != nil {
		return fmt.Sprintf("%d", time.Now().UnixNano())
	}
	return hex.EncodeToString(b[:])
}

func requestID(r *http.Request) string {
	if id := r.Header.Get("X-Request-Id"); id != "" && len(id) <= 128 {
		return id
	}
	return newRequestID()
}

func bearerToken(r *http.Request) string {
	h := r.Header.Get("Authorization")
	if strings.HasPrefix(h, "Bearer ") {
		return strings.TrimSpace(strings.TrimPrefix(h, "Bearer "))
	}
	return ""
}

func basicAuthToken(r *http.Request) string {
	if u, p, ok := r.BasicAuth(); ok {
		return u + ":" + p
	}
	return ""
}

// constantTimeEqual compares two strings without leaking length via timing
// beyond the length check itself.
func constantTimeEqual(a, b string) bool {
	if len(a) != len(b) {
		return false
	}
	var v byte
	for i := 0; i < len(a); i++ {
		v |= a[i] ^ b[i]
	}
	return v == 0
}

// hostFromURL is a small helper for logging without importing net/url.
func hostFromURL(raw string) string {
	if i := strings.Index(raw, "://"); i >= 0 {
		rest := raw[i+3:]
		if j := strings.IndexAny(rest, "/?#"); j >= 0 {
			return rest[:j]
		}
		return rest
	}
	return ""
}
