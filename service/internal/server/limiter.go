package server

import (
	"sync"
	"time"
)

// bucket is a token bucket keyed by client identity.
type bucket struct {
	tokens float64
	last   time.Time
}

// limiter is a small per-key token bucket. It is deliberately per-process;
// distributed limits belong at the edge or in Valkey.
type limiter struct {
	mu      sync.Mutex
	buckets map[string]*bucket
	rps     float64
	burst   float64
}

func newLimiter(rps float64, burst int) *limiter {
	if rps <= 0 {
		return nil
	}
	if burst <= 0 {
		burst = int(rps)
		if burst < 1 {
			burst = 1
		}
	}
	return &limiter{buckets: map[string]*bucket{}, rps: rps, burst: float64(burst)}
}

func (l *limiter) allow(key string) bool {
	if l == nil {
		return true
	}
	now := time.Now()
	l.mu.Lock()
	defer l.mu.Unlock()
	b, ok := l.buckets[key]
	if !ok {
		if len(l.buckets) > 100_000 {
			// Bound memory under a spoofed-IP flood.
			l.buckets = make(map[string]*bucket)
		}
		b = &bucket{tokens: l.burst, last: now}
		l.buckets[key] = b
	}
	elapsed := now.Sub(b.last).Seconds()
	b.tokens = minF(l.burst, b.tokens+elapsed*l.rps)
	b.last = now
	if b.tokens < 1 {
		return false
	}
	b.tokens--
	return true
}

func minF(a, b float64) float64 {
	if a < b {
		return a
	}
	return b
}
